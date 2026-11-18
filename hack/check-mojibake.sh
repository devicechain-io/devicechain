#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guard: reject UTF-8 text mangled by a Windows cp1252 round-trip.
# Scan tracked text sources, excluding legacy, dependencies and fixtures.
#
#   hack/check-mojibake.sh
#   hack/check-mojibake.sh --self-test   # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
export LC_ALL=C

# Literal UTF-8 bytes, escaped so this script does not flag its own patterns.
# Code points: E2+20AC, C2+A7, C2+B7, C3+E9, C3+F1, F0+178.
PATTERNS=(
  $'\xc3\xa2\xe2\x82\xac'
  $'\xc3\x82\xc2\xa7'
  $'\xc3\x82\xc2\xb7'
  $'\xc3\x83\xc2\xa9'
  $'\xc3\x83\xc2\xb1'
  $'\xc3\xb0\xc5\xb8'
)
GREP_PATTERNS=()
for pattern in "${PATTERNS[@]}"; do
  GREP_PATTERNS+=(-e "$pattern")
done

# Guard exit codes: 0 clean, 1 matches, 2 scan failure.
scan() {
  local status=0
  grep -nHF "${GREP_PATTERNS[@]}" -- "$@" || status=$?
  case "$status" in
    0) return 1 ;;
    1) return 0 ;;
    *) return 2 ;;
  esac
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [ "${1:-}" = "--self-test" ]; then
  echo "==> Self-test: the guard must report planted mojibake"
  for pattern in "${PATTERNS[@]}"; do
    printf 'clean first line\nMangled: %s\n' "$pattern" > "$TMP/planted.txt"
    status=0
    output="$(scan "$TMP/planted.txt")" || status=$?
    if [ "$status" -ne 1 ] || [[ "$output" != "$TMP/planted.txt:2:"* ]]; then
      echo "  FAIL: planted mojibake was not reported at its file and line" >&2
      exit 1
    fi
  done
  echo "  ok: all six planted prefixes detected at line 2 (exit 1)"

  # Correct UTF-8: em dash, red circle, section sign.
  printf '\xe2\x80\x94 / \xf0\x9f\x94\xb4 / \xc2\xa7\n' > "$TMP/planted.txt"
  status=0
  output="$(scan "$TMP/planted.txt")" || status=$?
  if [ "$status" -ne 0 ] || [ -n "$output" ]; then
    echo "  FAIL: correctly encoded UTF-8 was flagged" >&2
    exit 1
  fi
  echo "  ok: correctly encoded em dash, red circle and section sign pass (exit 0)"
  echo "==> Self-test passed"
  exit 0
fi

if ! git ls-files -z > "$TMP/tracked"; then
  echo "ERROR: git ls-files failed; no scan performed" >&2
  exit 2
fi
if [ ! -s "$TMP/tracked" ]; then
  echo "ERROR: git ls-files returned no files; no scan performed" >&2
  exit 2
fi

result=0
files=()
scan_batch() {
  local status=0
  scan "${files[@]}" || status=$?
  if [ "$status" -eq 2 ]; then
    echo "ERROR: text scan failed" >&2
    exit 2
  fi
  if [ "$status" -eq 1 ]; then
    result=1
  fi
  files=()
}

while IFS= read -r -d '' file; do
  case "/$file" in
    */_legacy/*|*/node_modules/*|*/vendor/*|*/vendored/*|*/testdata/*|*/fixtures/*) continue ;;
  esac
  case "$file" in
    *.go|*.md|*.mdx|*.ts|*.tsx|*.js|*.json|*.yaml|*.yml|*.graphql|*.sh|*.tf|*.css|*.html|*.txt) ;;
    *) continue ;;
  esac
  files+=("$file")
  # Bound command size and avoid starting one grep process per tracked file.
  if [ "${#files[@]}" -eq 50 ]; then
    scan_batch
  fi
done < "$TMP/tracked"
if [ "${#files[@]}" -gt 0 ]; then
  scan_batch
fi
exit "$result"
