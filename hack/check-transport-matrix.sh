#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guard: the symbol cells of the transport capability matrix, in all three locales, are what the
# conformance rig's results say they are.
#
#   hack/check-transport-matrix.sh            check (CI): exit 1 on any hand-edited cell
#   hack/check-transport-matrix.sh render     rewrite the cells from the results
#   hack/check-transport-matrix.sh release    check, and ALSO refuse results that are older than
#                                             DC_CONFORMANCE_MAX_AGE (default 1080h, 45 days) or whose
#                                             commit is not in this tree's history. Run before a release
#                                             candidate; CI cannot, because a docs-only change must not
#                                             fail on the age of the last rig run
#   hack/check-transport-matrix.sh --self-test   prove the guard fails on a hand edit, on a doctored
#                                             results file and on a stale one, before trusting it to pass
#
# WHY IT EXISTS. The page used to say, truthfully, that it was maintained by hand and could fall behind.
# A filled circle is a promise to a customer, and nothing made the promise cost anything. Now every
# filled circle names the third-party-client checks that substantiate it (hack/conformance/matrix.json),
# the rig's results say whether they passed, and this script is the thing that notices when the page and
# the results part ways. The spec also refuses a check that has no negative control, and results whose
# controls did not all fail first.

set -euo pipefail

# `pwd -W` (Git Bash on Windows) gives D:/... rather than /d/...: the paths below reach a Go program, and
# MSYS rewrites only an argument that is a path, not each entry of the comma-separated -docs list.
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && { pwd -W 2>/dev/null || pwd; })"
mod="$root/backend/tools/transportmatrix"
spec="$root/hack/conformance/matrix.json"
results="$root/hack/conformance/results.json"

tm() { (cd "$mod" && go run ./cmd/transportmatrix "$@"); }

docs=(
  "$root/docs/docs/reference/transport-matrix.md"
  "$root/docs/i18n/es/docusaurus-plugin-content-docs/current/reference/transport-matrix.md"
  "$root/docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/reference/transport-matrix.md"
)
docs_csv="$(IFS=,; echo "${docs[*]}")"

self_test() {
  local tmp
  tmp="$(cd "$(mktemp -d)" && { pwd -W 2>/dev/null || pwd; })"
  trap 'rm -rf "$tmp"' RETURN
  cp "$spec" "$tmp/matrix.json"
  cp "$results" "$tmp/results.json"
  local d i=0 copies=()
  for d in "${docs[@]}"; do
    i=$((i + 1))
    cp "$d" "$tmp/page$i.md"
    copies+=("$tmp/page$i.md")
  done
  local csv
  csv="$(IFS=,; echo "${copies[*]}")"

  # 1. The pristine copies pass.
  tm check -spec "$tmp/matrix.json" -results "$tmp/results.json" -docs "$csv" >/dev/null ||
    { echo "self-test: the unmodified pages do not pass; the guard is broken or the pages have drifted" >&2; return 1; }

  # 2. A hand edit of a cell makes it fail, in each locale. The first filled circle on the page becomes
  #    an empty one.
  local n
  for n in 1 2 3; do
    cp "${docs[$((n - 1))]}" "$tmp/page$n.md"
    # Replace the first Subscribe filled circle of the HTTP row (the only filled circle in the table).
    sed -i '0,/| ● |$/s//| ○ |/' "$tmp/page$n.md"
    if cmp -s "$tmp/page$n.md" "${docs[$((n - 1))]}"; then
      echo "self-test: could not hand-edit locale $n (no cell to change); the self-test is broken" >&2
      return 1
    fi
    if tm check -spec "$tmp/matrix.json" -results "$tmp/results.json" -docs "$csv" >/dev/null 2>&1; then
      echo "self-test: a hand-edited cell in locale $n was NOT caught" >&2
      return 1
    fi
    cp "${docs[$((n - 1))]}" "$tmp/page$n.md"
  done

  # 3. Results with a dirty tree, then one whose negative control went green, then one that is stale,
  #    each fail.
  sed 's/"dirty": false/"dirty": true/' "$results" >"$tmp/dirty.json"
  if tm check -spec "$tmp/matrix.json" -results "$tmp/dirty.json" -docs "$csv" >/dev/null 2>&1; then
    echo "self-test: results from a dirty tree were accepted" >&2
    return 1
  fi
  sed '0,/"outcome": "red"/s//"outcome": "green"/' "$results" >"$tmp/green.json"
  if tm check -spec "$tmp/matrix.json" -results "$tmp/green.json" -docs "$csv" >/dev/null 2>&1; then
    echo "self-test: results with a negative control that did not go red were accepted" >&2
    return 1
  fi
  if tm check -spec "$tmp/matrix.json" -results "$tmp/results.json" -docs "$csv" -max-age 1s >/dev/null 2>&1; then
    echo "self-test: results were accepted with a one-second age limit" >&2
    return 1
  fi
  echo "transport-matrix self-test: the guard fails on a hand edit in each locale, on a dirty tree, on a blind control and on stale results"
}

case "${1:-check}" in
  check) tm check -spec "$spec" -results "$results" -docs "$docs_csv" ;;
  render) tm render -spec "$spec" -results "$results" -docs "$docs_csv" ;;
  release) tm check -spec "$spec" -results "$results" -docs "$docs_csv" -max-age "${DC_CONFORMANCE_MAX_AGE:-1080h}" -repo "$root" ;;
  --self-test) self_test ;;
  *) echo "usage: hack/check-transport-matrix.sh [check|render|release|--self-test]" >&2; exit 2 ;;
esac
