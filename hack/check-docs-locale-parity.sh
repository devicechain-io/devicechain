#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guard: every translated docs tree holds exactly the same FILE SET as English.
#
# WHY THIS IS NOT TIDINESS. Docusaurus resolves a missing translation by falling
# back to the English page, silently and by design. That is a reasonable runtime
# behaviour and a terrible authoring signal: a page added to docs/docs and never
# mirrored into docs/i18n/es renders in Spanish — in English — with no warning in
# the build log and no broken link. The docs build already gates broken links and
# broken anchors (`onBrokenLinks: 'throw'`), and neither of them can see this,
# because nothing is broken. The page is simply not translated.
#
# 🔑 THE BUILD CATCHES *SOME* OF THIS, AND THE PART IT CATCHES IT REPORTS AS
# SOMETHING ELSE ENTIRELY. Measured, after this guard's first draft asserted the
# opposite: an untranslated page that contains a **relative markdown link** fails
# the `es` build outright, because the fallback copy has no position in the
# translated tree from which `../foo.md` can be resolved. What you get is
# "Markdown link with URL `../deployment/bootstrap.md` couldn't be resolved" —
# pointing at a link that is perfectly correct, in a file whose real problem is
# that its translation does not exist. Only the FIRST such link in each file is
# reported, so the message also understates its own scope.
#
# So the build is neither a substitute for this guard nor irrelevant to it:
#   - a missing translation whose page has no relative links: builds clean and
#     silently serves English — invisible, which is the case this guard exists for;
#   - a missing translation whose page has one: fails the build with a diagnosis
#     that sends you to fix a link that is not broken;
#   - an orphaned translation whose English original was deleted: builds clean.
# This guard turns all three into one accurate sentence, before the build runs.
#
# 🔴 THE COST LANDS AT THE GA TAG. Cutting v1.0.0 runs Docusaurus `docs:version`,
# which freezes a `1.0` snapshot — and with i18n configured it freezes ONE SNAPSHOT
# PER LOCALE, each copied from that locale's current tree. A drifted file set is
# copied faithfully: the English snapshot gets 40 pages, the Spanish snapshot gets
# 38, and the two are then frozen and shipped. After the tag, the fix is no longer
# "write the missing page" but "amend a released version". This guard is what makes
# that step safe to run.
#
# The reverse direction is checked too, and it is not symmetric noise: a Spanish
# page whose English original was deleted is a live route with no sidebar entry
# pointing at it — reachable by URL, unreachable by navigation, and invisible to
# every other check in the repo.
#
# 🔴 AND SO IS A WHOLE MISSING VERSION TREE, which the per-file comparison alone
# could not see. The file loop is driven by the instances that exist on the
# TRANSLATED side, so an English `versioned_docs/version-1.0` with no `es`
# counterpart walked zero iterations and reported parity. That is precisely the
# shape `docs:version` produces when it warns-and-skips a locale, and the shape a
# deleted snapshot leaves behind, so the English versioned trees are enumerated
# separately and each one is required to have its translated twin.
#
# 🔴 SCOPE, stated so a green run is not read as a broader claim than it is:
# this compares FILE SETS, not content. It proves a translated page EXISTS. It
# cannot prove the page was translated rather than copied, and it cannot prove a
# translation still matches an English page that has since been rewritten. Nothing
# cheap can. Read a green run as "no page is missing", never as "the Spanish docs
# are current".
#
#   hack/check-docs-locale-parity.sh              # check
#   hack/check-docs-locale-parity.sh --self-test  # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DOCS_ROOT_DEFAULT="docs"

usage() {
  echo "usage: $0 [--self-test] [docs-root]" >&2
  exit 2
}

# ---------------------------------------------------------------------------
# doc_set <dir> — the sorted set of authored doc files under <dir>.
#
# Markdown only. Images, and everything else in `static/`, are served from the
# English tree for every locale, so requiring a per-locale copy of them would
# fail on a correct repository.
# ---------------------------------------------------------------------------
doc_set() {
  local dir="$1"
  [ -d "$dir" ] || return 0
  ( cd "$dir" && find . -type f \( -name '*.md' -o -name '*.mdx' \) ) |
    sed 's|^\./||' | LC_ALL=C sort
}

# ---------------------------------------------------------------------------
# english_tree_for <docs-root> <instance>
#
# Docusaurus stores the two halves of a docs instance in directories that do not
# resemble each other, which is the whole reason this needs a function rather
# than a glob:
#
#   current      ->  docs/docs
#   version-1.0  ->  docs/versioned_docs/version-1.0
#
# while BOTH translations live under one flat parent:
#
#   docs/i18n/<locale>/docusaurus-plugin-content-docs/{current,version-1.0}
#
# `version-*` resolves nothing today — the first snapshot is cut at the GA tag.
# It is handled here anyway, because the moment it exists it is the tree this
# guard most needs to cover, and a guard extended in the same commit as the thing
# it must check is a guard written under deadline.
# ---------------------------------------------------------------------------
english_tree_for() {
  local docs_root="$1" instance="$2"
  if [ "$instance" = "current" ]; then
    printf '%s\n' "$docs_root/docs"
  else
    printf '%s\n' "$docs_root/versioned_docs/$instance"
  fi
}

# ---------------------------------------------------------------------------
# check <docs-root>
#
# Locales are discovered from the tree rather than parsed out of
# docusaurus.config.ts. A locale directory that exists is a locale readers can
# select, and that is the thing being checked; a locale listed in the config with
# no directory has nothing to compare. Discovering them also means the post-GA
# locales enrol themselves on the day their directory lands, instead of waiting
# for someone to remember this file.
# ---------------------------------------------------------------------------
check() {
  local docs_root="${1:-$DOCS_ROOT_DEFAULT}"
  local problems=0 compared=0 locale_dir locale plugin_dir instance_dir instance
  local en_tree en_set loc_set only_en only_loc en_version_dir

  if [ ! -d "$docs_root/docs" ]; then
    echo "::error::no English docs tree at $docs_root/docs — the enumeration is broken, not the tree" >&2
    return 1
  fi

  if [ ! -d "$docs_root/i18n" ]; then
    echo "::error::no $docs_root/i18n directory — this repository ships a translated locale," >&2
    echo "         so its absence is a broken checkout, not a repository with one locale" >&2
    return 1
  fi

  for locale_dir in "$docs_root"/i18n/*/; do
    [ -d "$locale_dir" ] || continue
    locale="$(basename "$locale_dir")"
    plugin_dir="${locale_dir}docusaurus-plugin-content-docs"

    if [ ! -d "$plugin_dir" ]; then
      echo "  $locale: has no docusaurus-plugin-content-docs/ directory, so NONE of its"
      echo "      pages are translated — every one falls back to English at build time"
      problems=$((problems + 1))
      continue
    fi

    for instance_dir in "$plugin_dir"/*/; do
      [ -d "$instance_dir" ] || continue
      instance="$(basename "$instance_dir")"
      en_tree="$(english_tree_for "$docs_root" "$instance")"

      if [ ! -d "$en_tree" ]; then
        echo "  $locale/$instance: translated tree exists but $en_tree does not."
        echo "      Nothing renders from it. Delete it, or restore the English tree."
        problems=$((problems + 1))
        continue
      fi

      en_set="$(doc_set "$en_tree")"
      loc_set="$(doc_set "${instance_dir%/}")"
      compared=$((compared + 1))

      only_en="$(LC_ALL=C comm -23 <(printf '%s\n' "$en_set") <(printf '%s\n' "$loc_set"))"
      only_loc="$(LC_ALL=C comm -13 <(printf '%s\n' "$en_set") <(printf '%s\n' "$loc_set"))"

      if [ -n "$only_en" ]; then
        echo "  $locale/$instance: present in $en_tree, MISSING from the translation:"
        printf '%s\n' "$only_en" | sed 's|^|      |'
        problems=$((problems + 1))
      fi
      if [ -n "$only_loc" ]; then
        echo "  $locale/$instance: present in the translation, MISSING from $en_tree:"
        printf '%s\n' "$only_loc" | sed 's|^|      |'
        problems=$((problems + 1))
      fi
    done

    # 🔑 THE LOOP ABOVE IS DRIVEN BY THE TRANSLATED SIDE, SO IT CANNOT SEE AN
    # ENGLISH TREE THAT HAS NO TRANSLATED COUNTERPART AT ALL. For `current` that
    # is harmless — a locale with no `current` compares nothing and is caught by
    # the compared-nothing guard below — but a VERSIONED tree is not: with
    # `current` pairing cleanly, an English `versioned_docs/version-1.0` and no
    # `es/version-1.0` walks zero iterations here and the run reports parity.
    #
    # That is not hypothetical at the tag. `docs:version` WARNS AND SKIPS a locale
    # whose current directory is empty, so the half-snapshot it produces is exactly
    # this shape — one English version tree, no translated twin — and it is the
    # one direction the loop above is structurally unable to look in. A deleted
    # snapshot has the same shape and is just as invisible.
    for en_version_dir in "$docs_root"/versioned_docs/*/; do
      [ -d "$en_version_dir" ] || continue
      instance="$(basename "$en_version_dir")"
      if [ ! -d "$plugin_dir/$instance" ]; then
        echo "  $locale/$instance: $en_version_dir is a frozen English version with NO"
        echo "      translated counterpart at $plugin_dir/$instance — every page in that"
        echo "      version serves English to $locale readers, and the version is released."
        problems=$((problems + 1))
      fi
    done
  done

  # 🔑 THE COUNTERWEIGHT TO THE DISCOVERY ABOVE. Every loop in this function
  # `continue`s over a directory that is not there, so a docs root whose i18n
  # tree is shaped differently than expected — a renamed plugin directory, a
  # locale folder holding only theme JSON — walks the whole loop, finds nothing
  # to compare, and reports success. "Clean" and "never looked" must not be the
  # same output; that is the failure mode this repository has shipped before.
  if [ "$compared" -eq 0 ]; then
    echo "::error::compared no docs trees at all. A locale directory exists but no" >&2
    echo "         docs instance under it could be paired with an English tree, so this" >&2
    echo "         run proved nothing." >&2
    return 1
  fi

  if [ "$problems" -gt 0 ]; then
    return 1
  fi

  echo "==> Locale parity OK: $compared translated docs tree(s) match English file-for-file."
  return 0
}

# ---------------------------------------------------------------------------
# Self-test. Fixtures only — never the real tree, so a repository that has
# drifted still gets a truthful answer about whether the GUARD works.
# ---------------------------------------------------------------------------
self_test() {
  local tmp root out
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" RETURN
  root="$tmp/docs"

  # A clean fixture, rebuilt before each case so one case cannot leak into the
  # next. Two files and a subdirectory, because a bug that only shows up for
  # nested paths is exactly the bug a one-file fixture misses.
  fixture() {
    rm -rf "$root"
    mkdir -p "$root/docs/guides" "$root/i18n/es/docusaurus-plugin-content-docs/current/guides"
    printf 'intro\n' > "$root/docs/intro.md"
    printf 'guide\n' > "$root/docs/guides/first.md"
    printf 'intro\n' > "$root/i18n/es/docusaurus-plugin-content-docs/current/intro.md"
    printf 'guia\n'  > "$root/i18n/es/docusaurus-plugin-content-docs/current/guides/first.md"
  }

  echo "==> Self-test: the guard must catch drift in both directions, and must not cry wolf"

  # Case 1 — THE COUNTERWEIGHT, first on purpose. Without it every case below is
  # satisfied by a guard that fails unconditionally.
  fixture
  if out="$(check "$root" 2>&1)"; then
    case "$out" in
      *"1 translated docs tree(s) match"*)
        echo "  ok: a matching pair of trees passes, and exactly one pair was compared" ;;
      *) echo "  FAIL: passed, but did not report comparing one tree: $out" >&2; return 1 ;;
    esac
  else
    echo "  FAIL: a correctly mirrored tree was rejected — the guard cries wolf" >&2
    echo "  got: $out" >&2
    return 1
  fi

  # Case 2 — the everyday regression: an English page with no translation. This
  # is the one that renders in English under /es/ with nothing in any log.
  fixture
  printf 'new\n' > "$root/docs/quickstart.md"
  if out="$(check "$root" 2>&1)"; then
    echo "  FAIL: an untranslated English page was accepted. That page renders in" >&2
    echo "        English under /es/ and no other check in this repo can see it." >&2
    return 1
  fi
  case "$out" in
    *"MISSING from the translation"*quickstart.md*)
      echo "  ok: an English page with no translation is reported" ;;
    *) echo "  FAIL: wrong message for a missing translation: $out" >&2; return 1 ;;
  esac

  # Case 3 — the other direction. A guard written as a one-way subset test passes
  # this, which is why it is a case and not an assumption.
  fixture
  printf 'huerfano\n' > "$root/i18n/es/docusaurus-plugin-content-docs/current/orphan.md"
  if out="$(check "$root" 2>&1)"; then
    echo "  FAIL: an orphaned translation was accepted — a one-way subset test would" >&2
    echo "        pass this, which is exactly the bug this case exists to catch" >&2
    return 1
  fi
  case "$out" in
    *"MISSING from"*orphan.md*)
      echo "  ok: a translation with no English original is reported" ;;
    *) echo "  FAIL: wrong message for an orphaned translation: $out" >&2; return 1 ;;
  esac

  # Case 4 — drift inside a SUBDIRECTORY. A comparison built on `ls` rather than
  # a recursive walk passes cases 2 and 3 and fails only here.
  fixture
  rm "$root/i18n/es/docusaurus-plugin-content-docs/current/guides/first.md"
  if out="$(check "$root" 2>&1)"; then
    echo "  FAIL: drift in a subdirectory was accepted — the walk is not recursive" >&2
    return 1
  fi
  case "$out" in
    *guides/first.md*) echo "  ok: drift in a subdirectory is reported, with its full path" ;;
    *) echo "  FAIL: subdirectory drift reported without its path: $out" >&2; return 1 ;;
  esac

  # Case 5 — a VERSIONED instance, the tree that does not exist yet and is the
  # whole reason this guard is landing before the GA tag rather than after it.
  # `version-1.0` lives under versioned_docs/ on the English side and beside
  # `current` on the translated side; a guard that assumed one flat layout would
  # pass here by comparing nothing.
  fixture
  mkdir -p "$root/versioned_docs/version-1.0" \
           "$root/i18n/es/docusaurus-plugin-content-docs/version-1.0"
  printf 'intro\n' > "$root/versioned_docs/version-1.0/intro.md"
  printf 'frozen\n' > "$root/versioned_docs/version-1.0/frozen.md"
  printf 'intro\n' > "$root/i18n/es/docusaurus-plugin-content-docs/version-1.0/intro.md"
  if out="$(check "$root" 2>&1)"; then
    echo "  FAIL: a half-snapshotted version was accepted. Freezing 2 English pages" >&2
    echo "        against 1 Spanish page is precisely the GA-tag failure this guards." >&2
    return 1
  fi
  case "$out" in
    *"es/version-1.0"*frozen.md*)
      echo "  ok: a half-snapshotted version tree is reported, named by its version" ;;
    *) echo "  FAIL: version drift not attributed to version-1.0: $out" >&2; return 1 ;;
  esac

  # And the counterweight for case 5: a COMPLETE snapshot must pass, and the run
  # must report comparing TWO trees. Without the count, a guard that silently
  # skipped every versioned tree would look identical to this.
  printf 'frozen\n' > "$root/i18n/es/docusaurus-plugin-content-docs/version-1.0/frozen.md"
  if out="$(check "$root" 2>&1)"; then
    case "$out" in
      *"2 translated docs tree(s) match"*)
        echo "  ok: a complete snapshot passes, and BOTH trees were compared" ;;
      *)
        echo "  FAIL: passed without comparing two trees — the versioned tree was" >&2
        echo "        skipped, not checked: $out" >&2
        return 1 ;;
    esac
  else
    echo "  FAIL: a complete snapshot was rejected: $out" >&2
    return 1
  fi

  # Case 6 — AN ENGLISH VERSION TREE WITH NO TRANSLATED TWIN AT ALL. Distinct
  # from case 5, which drifts by a file: here the whole `es/version-1.0` is
  # absent, so a comparison driven by the translated side never runs and the
  # guard reports parity on a released version that serves English throughout.
  # `current` is deliberately left correct, so nothing else can fail this case —
  # it fails only if the English side is enumerated in its own right.
  fixture
  mkdir -p "$root/versioned_docs/version-1.0"
  printf 'frozen\n' > "$root/versioned_docs/version-1.0/intro.md"
  if out="$(check "$root" 2>&1)"; then
    echo "  FAIL: a frozen English version with NO translated tree was accepted." >&2
    echo "        The file comparison is driven by the translated side, so this" >&2
    echo "        direction is only covered if the English side is enumerated too." >&2
    return 1
  fi
  case "$out" in
    *"es/version-1.0"*"NO"*)
      echo "  ok: a frozen English version with no translated tree is reported" ;;
    *) echo "  FAIL: wrong message for a wholly untranslated version: $out" >&2; return 1 ;;
  esac

  # And its counterweight: adding the translated tree must make the same fixture
  # pass, comparing TWO trees. Without it, case 6 is satisfied by a check that
  # rejects every repository that has a versioned tree at all.
  mkdir -p "$root/i18n/es/docusaurus-plugin-content-docs/version-1.0"
  printf 'frozen\n' > "$root/i18n/es/docusaurus-plugin-content-docs/version-1.0/intro.md"
  if out="$(check "$root" 2>&1)"; then
    case "$out" in
      *"2 translated docs tree(s) match"*)
        echo "  ok: supplying the translated version tree clears it, and both were compared" ;;
      *) echo "  FAIL: passed without comparing two trees: $out" >&2; return 1 ;;
    esac
  else
    echo "  FAIL: a complete versioned pair was rejected — the English-side sweep" >&2
    echo "        fires even when the twin is present: $out" >&2
    return 1
  fi

  # Case 7 — a locale whose translations are entirely absent. Every loop in
  # check() steps over a directory that is not there, so this shape would
  # otherwise walk through and report success on a locale with zero pages.
  fixture
  mkdir -p "$root/i18n/de/docusaurus-theme-classic"
  printf '{}\n' > "$root/i18n/de/docusaurus-theme-classic/navbar.json"
  if out="$(check "$root" 2>&1)"; then
    echo "  FAIL: a locale with no docs plugin directory was accepted — it has zero" >&2
    echo "        translated pages and every one of them falls back to English" >&2
    return 1
  fi
  case "$out" in
    *"de: has no docusaurus-plugin-content-docs"*)
      echo "  ok: a locale with no translated docs at all is reported" ;;
    *) echo "  FAIL: wrong message for an untranslated locale: $out" >&2; return 1 ;;
  esac

  # Case 8 — THE CANNOT-FAIL SHAPE, asserted rather than assumed. If the plugin
  # directory is renamed upstream, or a locale holds only theme JSON, every
  # comparison is skipped and the guard has proved nothing. That must be a
  # failure with its own message, not the same "OK" a clean tree produces.
  fixture
  rm -rf "$root/i18n/es/docusaurus-plugin-content-docs"
  mkdir -p "$root/i18n/es/docusaurus-plugin-content-docs"
  if out="$(check "$root" 2>&1)"; then
    echo "  FAIL: a run that compared NOTHING reported success. 'Clean' and 'never" >&2
    echo "        looked' must not produce the same output." >&2
    return 1
  fi
  case "$out" in
    *"compared no docs trees at all"*)
      echo "  ok: comparing nothing is refused, with its own diagnosis" ;;
    *) echo "  FAIL: an empty comparison failed for the wrong reason: $out" >&2; return 1 ;;
  esac

  echo "==> Self-test passed"
}

case "${1:-}" in
  --self-test) self_test ;;
  --*) usage ;;
  *)
    if ! check "${1:-$DOCS_ROOT_DEFAULT}"; then
      cat >&2 <<'EOF'

==> Translated docs have drifted from English

Docusaurus falls back to the English page when a translation is missing, so a
Spanish reader silently gets English. How that shows up depends on the page:

  - a page with no relative markdown links builds clean, and nothing anywhere
    reports it — this is the case this guard exists for;
  - a page that HAS one fails the `es` build with "Markdown link ... couldn't be
    resolved", naming a link that is not actually broken. If you were sent here
    by that error, the missing translation below is the real cause.

To fix:
  - a page missing from a translation  -> write the translation
  - a page missing from English        -> delete the orphaned translation

The translated tree for the current docs lives at
docs/i18n/<locale>/docusaurus-plugin-content-docs/current/, mirroring docs/docs/
path for path.

This matters most at the release tag: `docs:version` freezes one snapshot per
locale from these trees, so drift at that moment is frozen into a shipped
version rather than fixed by writing a page.
EOF
      exit 1
    fi
    ;;
esac
