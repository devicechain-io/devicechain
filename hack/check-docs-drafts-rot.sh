#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guard: a feature draft in docs-drafts/ must still describe the tree it cites.
#
# WHY. These drafts exist to hold the ONE thing no source file holds — the
# traversal across components — and their whole value is that every claim was
# checked against the code when it was written. That value decays silently: a
# file moves, a type is renamed, and the draft keeps asserting the old shape in
# confident prose. A stale map is worse than no map, because it is trusted.
#
# So the drafts get the same treatment as code: a cheap gate that fails when the
# tree no longer matches.
#
# TWO CHECKS, AND THEY ARE NOT EQUALLY STRONG:
#
#   PATHS   — every backend/ hack/ deploy/ frontend/ sdks/ path a draft names
#             must exist. This is a real check, and it is the one that catches
#             the common rot (a file moved or renamed).
#
#   SYMBOLS — every backticked identifier carrying a capital letter must appear
#             SOMEWHERE in the tree. This is deliberately weak: it proves the
#             name still exists, not that it still means what the draft says.
#             It catches a rename, which is the failure that actually happens,
#             and it cannot catch a symbol that kept its name and changed its
#             behaviour. Nothing here can. Do not read a green run as "the draft
#             is accurate" — read it as "the draft is not obviously stale".
#
#   hack/check-docs-drafts-rot.sh              # check
#   hack/check-docs-drafts-rot.sh --self-test  # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DRAFTS="${DRAFTS_DIR:-docs-drafts}"
SEARCH_ROOTS=(backend hack deploy frontend sdks docs)

# 🔴 THE SELF-TEST MUST BE ABLE TO POINT THIS SOMEWHERE ELSE, and the reason is a
# trap this script fell into on its first draft. The symbol check greps the tree
# for a name; `hack/` is part of that tree; so a planted "this symbol does not
# exist anywhere" control written as a literal in THIS FILE is found — in this
# file — and the control passes no matter how broken the check is. Pointing the
# search at a throwaway tree makes the self-test hermetic, so neither planted
# case can be satisfied by the guard's own source.
search_roots() {
  if [ -n "${SEARCH_ROOTS_OVERRIDE:-}" ]; then
    printf '%s\n' "$SEARCH_ROOTS_OVERRIDE"
    return 0
  fi
  local r
  for r in "${SEARCH_ROOTS[@]}"; do [ -d "$r" ] && printf '%s\n' "$r"; done
}

# Words that look like symbols and are not. Kept SHORT on purpose: every entry
# is a hole in the check, so an entry earns its place by being a real English
# word a draft cannot avoid, not by being inconvenient to fix.
ALLOW_SYMBOLS=(TODO NOTE README JSON SQL HTTP HTTPS URL API CRUD)

# Every path-shaped token a draft names. Trailing punctuation is stripped so a
# path at the end of a sentence is checked as a path rather than reported as a
# missing file ending in a full stop.
draft_paths() {
  local f="$1"
  grep -oE '(backend|hack|deploy|frontend|sdks)/[A-Za-z0-9_./-]+' "$f" |
    sed -E 's/[.,;:)]+$//' | sort -u
}

# Backticked identifiers carrying a capital letter. Anything with a space,
# bracket or operator in it is prose or a signature, not a symbol, and is left
# alone — this check is worth having only while it stays quiet enough to be read.
draft_symbols() {
  local f="$1"
  grep -oE '`[A-Za-z][A-Za-z0-9_.]*`' "$f" |
    tr -d '`' |
    grep -E '[A-Z]' |
    awk 'length($0) >= 4' |
    sort -u
}

symbol_is_allowed() {
  local s="$1" a
  for a in "${ALLOW_SYMBOLS[@]}"; do [ "$s" = "$a" ] && return 0; done
  return 1
}

# Reports every problem it finds rather than stopping at the first, so one run
# tells the whole story of what a draft has outlived.
check_drafts() {
  local base="$1" problems="" f p s roots=()
  [ -d "$base" ] || return 0

  while IFS= read -r r; do [ -n "$r" ] && roots+=("$r"); done < <(search_roots)

  while IFS= read -r -d '' f; do
    while IFS= read -r p; do
      [ -n "$p" ] || continue
      [ -e "$p" ] && continue
      problems+="${f}: names a path that no longer exists: ${p}"$'\n'
    done < <(draft_paths "$f")

    [ ${#roots[@]} -eq 0 ] && continue
    while IFS= read -r s; do
      [ -n "$s" ] || continue
      symbol_is_allowed "$s" && continue
      grep -rqF -- "$s" "${roots[@]}" 2>/dev/null && continue
      problems+="${f}: names a symbol that appears nowhere in the tree: ${s}"$'\n'
    done < <(draft_symbols "$f")
  done < <(find "$base" -type f -name '*.md' -print0)

  printf '%s' "$problems"
}

if [ "${1:-}" = "--self-test" ]; then
  echo "==> Self-test: the guard must report a planted path and a planted symbol"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/drafts" "$tmp/tree"

  # The searched tree is a throwaway holding exactly one known name, so nothing
  # below can be satisfied by this script's own text. See search_roots.
  printf 'package t\n\nfunc SymbolThatIsPresent() {}\n' > "$tmp/tree/known.go"
  export SEARCH_ROOTS_OVERRIDE="$tmp/tree"

  # A draft that is entirely truthful must produce nothing. Without this the
  # planted-failure checks below would pass just as well with a guard that
  # reports everything.
  printf 'See `hack/check-docs-drafts-rot.sh` and `SymbolThatIsPresent`.\n' \
    > "$tmp/drafts/clean.md"
  if [ -n "$(check_drafts "$tmp/drafts")" ]; then
    echo "  FAIL: the guard reports a problem in a draft whose path and symbol both exist" >&2
    check_drafts "$tmp/drafts" >&2
    exit 1
  fi
  echo "  ok: a truthful draft produces no findings"

  printf 'See `backend/core/tenantpurge/no_such_file.go`.\n' > "$tmp/drafts/clean.md"
  case "$(check_drafts "$tmp/drafts")" in
    *"no longer exists"*) echo "  ok: planted missing path detected" ;;
    *) echo "  FAIL: the guard did not see a path that does not exist" >&2; exit 1 ;;
  esac

  printf 'The `SymbolThatIsAbsent` type owns it.\n' > "$tmp/drafts/clean.md"
  case "$(check_drafts "$tmp/drafts")" in
    *"appears nowhere in the tree"*) echo "  ok: planted missing symbol detected" ;;
    *) echo "  FAIL: the guard did not see a symbol that does not exist" >&2; exit 1 ;;
  esac

  unset SEARCH_ROOTS_OVERRIDE

  echo "==> Self-test passed"
  exit 0
fi

found="$(check_drafts "$DRAFTS")"
if [ -n "$found" ]; then
  cat >&2 <<EOF

==> A feature draft no longer matches the tree

$found
A draft in ${DRAFTS}/ is a map of code that has since moved. Re-read the section
that carries each of these and correct the claim — not just the name, since a
path that moved usually means the behaviour around it moved too.

EOF
  exit 1
fi

echo "==> Feature drafts still match the tree."
