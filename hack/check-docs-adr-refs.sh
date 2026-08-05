#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guard: no ADR references in published documentation.
#
# WHY. ADRs live in `.agent-os/`, a gitignored symlink to the private strategy
# repo. They are the right citation in SOURCE — a maintainer reading a comment
# can look one up, and CLAUDE.md establishes that convention deliberately. They
# are the wrong citation in the DOCS SITE, where the reader is a user who has no
# access to them: "(ADR-025)" is a dead reference dressed as a source, and it
# quietly advertises that a private repo exists.
#
# The line this draws:
#
#   Rendered documentation   docs/docs, docs/i18n, docs/blog, docs/src  -> NO ADR refs
#   Draft feature docs       docs-drafts/**/*.md, body only            -> NO ADR refs
#   npm registry metadata    frontend/packages/*/package.json          -> NO ADR refs
#   Helm listing prose       deploy/helm/*/{Chart.yaml,README.md}      -> NO ADR refs
#   Source and maintainer    everything else, incl. docusaurus.config.ts -> ADR refs fine
#
# The distinguishing question is not "is this file public?" — the whole repo is
# public. It is "does a READER of the published site see this?". Each row above
# is a place where a stranger is SERVED this prose by some other host — the docs
# site, npmjs.com, Artifact Hub — with no way to follow an ADR citation.
#
#   hack/check-docs-adr-refs.sh              # check
#   hack/check-docs-adr-refs.sh --self-test  # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Rendered content only. docs/build is generated output and is not checked —
# it is a copy of what these sources already say.
TARGETS=(docs/docs docs/i18n docs/blog docs/src)
PATTERN='ADR-[0-9]{3}'

scan() {
  local dirs=() d
  for d in "$@"; do [ -d "$d" ] && dirs+=("$d"); done
  [ ${#dirs[@]} -eq 0 ] && return 0
  # grep exits 1 when there are no matches, which is the PASSING case here, so
  # the `|| true` is load-bearing under `set -e` rather than sloppiness.
  grep -rnE "$PATTERN" "${dirs[@]}" 2>/dev/null || true
}

# docs-drafts/ holds feature documents for arcs that have reached their
# GA-claimable state. They are drafts of published pages, so they are written to
# the docs site's rules from the first sentence rather than converted later — a
# draft written in the strategy repo's house style cites ADRs freely, and moving
# it into docs/ is then a rewrite rather than a `git mv`.
#
# THE FRONTMATTER IS A DELIBERATE EXCEPTION and the only one. A draft's ADR
# pointers are worth keeping with it during the holding period; putting them in
# one machine-strippable block keeps the BODY publishable as-is. So the body is
# checked exactly as a published page is, and the leading `---` … `---` block is
# blanked before the scan — blanked rather than deleted, so the line numbers in a
# failure still address the file as the author sees it.
scan_drafts() {
  local base="${1:-docs-drafts}" f
  [ -d "$base" ] || return 0
  while IFS= read -r -d '' f; do
    awk 'NR==1 && $0=="---" { fm=1; print ""; next }
         fm==1 && $0=="---" { fm=0; print ""; next }
         fm==1              { print ""; next }
                            { print }' "$f" |
      grep -nE "$PATTERN" | sed "s|^|${f}:|" || true
  done < <(find "$base" -type f -name '*.md' -print0)
}

# The other published surface, and a much easier one to miss: the `description`
# of an npm package renders on npmjs.com. frontend/packages/* are slated for
# publication to the public registry, so their descriptions are read by people
# who have never seen this repository, let alone the private one.
# Takes the packages root so the self-test can point it somewhere harmless
# rather than at the real tree.
scan_package_descriptions() {
  local base="${1:-frontend/packages}" f
  for f in "$base"/*/package.json; do
    [ -f "$f" ] || continue
    grep -nE "\"(description|keywords)\".*${PATTERN}" "$f" 2>/dev/null | sed "s|^|${f}:|" || true
  done
}

# The third published surface: the Helm chart's LISTING on Artifact Hub, which
# ingests oci://ghcr.io/devicechain-io/charts (pushed by the release workflow).
# Artifact Hub renders the packaged chart's `README.md` as the package page and
# the `Chart.yaml` metadata as its header — so both are read by people who have
# never seen this repository, exactly like an npm description.
#
# 🔴 SCOPE, stated so the guard's existence is not mistaken for a broader claim:
# this covers the chart's PROSE only. `values.yaml`, `values.schema.json` and
# `templates/` are also packaged, and Artifact Hub does expose them under its
# Values and Templates tabs — but those are chart SOURCE, browsed the same way
# this public repo is browsed, and CLAUDE.md's table deliberately keeps ADR
# citations in `deploy/`. They carry ~90 references today and are NOT checked.
# If that line ever moves, it moves as a decision, not by widening this glob.
scan_chart_listing() {
  local base="${1:-deploy/helm}" d f
  for d in "$base"/*/; do
    d="${d%/}"
    [ -f "$d/Chart.yaml" ] || continue
    for f in "$d/Chart.yaml" "$d/README.md"; do
      [ -f "$f" ] || continue
      grep -nE "$PATTERN" "$f" 2>/dev/null | sed "s|^|${f}:|" || true
    done
  done
}

if [ "${1:-}" = "--self-test" ]; then
  echo "==> Self-test: the guard must report a planted reference"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/docs"
  printf 'Secured at the broker (ADR-025).\n' > "$tmp/docs/planted.md"

  planted="$(scan "$tmp/docs")"
  if [ -z "$planted" ]; then
    echo "  FAIL: the guard did not see a planted ADR reference — it is not checking anything" >&2
    exit 1
  fi
  echo "  ok: planted reference detected"

  printf 'Secured at the broker.\n' > "$tmp/docs/planted.md"
  if [ -n "$(scan "$tmp/docs")" ]; then
    echo "  FAIL: the guard reports a match in clean content — it would fail every build" >&2
    exit 1
  fi
  echo "  ok: clean content produces no match"

  mkdir -p "$tmp/packages/example"
  printf '{ "description": "A thing (ADR-039)." }\n' > "$tmp/packages/example/package.json"
  if [ -z "$(scan_package_descriptions "$tmp/packages")" ]; then
    echo "  FAIL: the guard did not see a planted ADR reference in an npm description" >&2
    exit 1
  fi
  echo "  ok: planted npm description detected"

  printf '{ "description": "A thing." }\n' > "$tmp/packages/example/package.json"
  if [ -n "$(scan_package_descriptions "$tmp/packages")" ]; then
    echo "  FAIL: the guard reports a match in a clean npm description" >&2
    exit 1
  fi
  echo "  ok: clean npm description produces no match"

  # The draft exemption is the one rule here that makes the guard WEAKER, so it
  # gets both controls: the body must still be caught, and the frontmatter must
  # still be let through. Checking only the second would pass with the whole
  # file exempted.
  mkdir -p "$tmp/drafts"
  printf -- '---\nadrs: [ADR-077]\n---\n\nThe token is released on completion.\n' \
    > "$tmp/drafts/example.md"
  if [ -n "$(scan_drafts "$tmp/drafts")" ]; then
    echo "  FAIL: the guard reports a match in a draft's frontmatter, which is the one" >&2
    echo "        place a draft is allowed to name an ADR" >&2
    exit 1
  fi
  echo "  ok: a draft's frontmatter ADR list is exempt"

  printf -- '---\nadrs: [ADR-077]\n---\n\nThe token is released (ADR-077).\n' \
    > "$tmp/drafts/example.md"
  planted="$(scan_drafts "$tmp/drafts")"
  if [ -z "$planted" ]; then
    echo "  FAIL: the guard did not see an ADR reference in a draft's BODY — the" >&2
    echo "        frontmatter exemption is swallowing the whole file" >&2
    exit 1
  fi
  case "$planted" in
    *:5:*) echo "  ok: planted draft body reference detected, at its real line number" ;;
    *)     echo "  FAIL: draft body reference reported at the wrong line: $planted" >&2; exit 1 ;;
  esac

  # The chart listing is TWO files, and the failure that matters is covering one
  # and not the other — a guard that only ever sees README.md would pass a
  # Chart.yaml description full of ADR refs, which is the string Artifact Hub
  # puts on the search-results card. So each file gets its own planted case,
  # with the other held clean, rather than one case planting in both.
  mkdir -p "$tmp/helm/example"
  clean_chart() {
    printf 'apiVersion: v2\nname: example\ndescription: A thing.\n' > "$tmp/helm/example/Chart.yaml"
    printf '# Example\n\nRenders a thing.\n' > "$tmp/helm/example/README.md"
  }

  clean_chart
  if [ -n "$(scan_chart_listing "$tmp/helm")" ]; then
    echo "  FAIL: the guard reports a match in a clean chart listing" >&2
    exit 1
  fi
  echo "  ok: clean chart listing produces no match"

  clean_chart
  printf 'apiVersion: v2\nname: example\ndescription: A thing (ADR-022).\n' \
    > "$tmp/helm/example/Chart.yaml"
  if [ -z "$(scan_chart_listing "$tmp/helm")" ]; then
    echo "  FAIL: the guard did not see a planted ADR reference in a chart description" >&2
    exit 1
  fi
  echo "  ok: planted chart description detected"

  clean_chart
  printf '# Example\n\nRenders a thing (ADR-022).\n' > "$tmp/helm/example/README.md"
  if [ -z "$(scan_chart_listing "$tmp/helm")" ]; then
    echo "  FAIL: the guard did not see a planted ADR reference in a chart README —" >&2
    echo "        that file IS the Artifact Hub package page" >&2
    exit 1
  fi
  echo "  ok: planted chart README reference detected"

  # A directory with a README but no Chart.yaml is not a chart, and scanning it
  # would quietly widen the guard past the surface it documents.
  mkdir -p "$tmp/helm/notachart"
  printf '# Notes\n\nSee ADR-022.\n' > "$tmp/helm/notachart/README.md"
  clean_chart
  if [ -n "$(scan_chart_listing "$tmp/helm")" ]; then
    echo "  FAIL: the guard scanned a directory that has no Chart.yaml" >&2
    exit 1
  fi
  echo "  ok: a non-chart directory is not scanned"

  echo "==> Self-test passed"
  exit 0
fi

hits="$(scan "${TARGETS[@]}")"
for extra in "$(scan_package_descriptions)" "$(scan_drafts)" "$(scan_chart_listing)"; do
  [ -n "$extra" ] || continue
  hits="${hits:+$hits
}${extra}"
done

if [ -n "$hits" ]; then
  cat >&2 <<EOF

==> ADR references found in published documentation

$hits

ADRs live in the private strategy repo (.agent-os/), so a reader of the docs
site cannot follow these. Rewrite each one to say the thing itself, or link to
a public page that explains it:

  "secured at the broker (ADR-025)"  ->  "secured at the broker"
  "[ADR-026](../concepts/architecture.md)"
                                     ->  "[data lifecycle](../concepts/architecture.md)"

ADR citations remain correct in source comments and maintainer-facing files.
This guard covers only the surfaces a stranger is SERVED by another host:
${TARGETS[*]}, docs-drafts (body), frontend/packages/*/package.json
(description/keywords), and deploy/helm/*/{Chart.yaml,README.md} — the prose
Artifact Hub renders as a chart's listing. Chart values/templates are source
and are NOT covered.

EOF
  exit 1
fi

echo "==> No ADR references in published documentation."
