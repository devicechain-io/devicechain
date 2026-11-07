#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# check-security-txt.sh — the published security.txt (RFC 9116) must stay valid.
#
# The file carries an Expires field, and RFC 9116 says an expired file must be treated as absent.
# That makes it the one file in the repository that silently goes bad with the calendar: nothing
# changes in the tree, and one day the vulnerability-contact is no longer discoverable. This
# guard fails while there is still MIN_DAYS of margin, so renewing it is a scheduled chore and
# never an incident. It also requires a Contact, a Canonical and a Policy field, and that Expires
# is at most one year out (RFC 9116 section 2.5.5 recommends it).
#
#   hack/check-security-txt.sh [file]
#   hack/check-security-txt.sh --self-test   # prove the check can fail
#
# NOW overrides the clock (epoch seconds) for the self-test.

set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIN_DAYS="${MIN_DAYS:-60}"

check() {
  local f="$1" now exp exp_s
  [ -f "$f" ] || { echo "FAIL: $f does not exist" >&2; return 1; }
  now="${NOW:-$(date -u +%s)}"
  local field
  for field in Contact Canonical Policy Expires; do
    grep -q "^$field: ." "$f" || { echo "FAIL: $f has no $field field" >&2; return 1; }
  done
  exp="$(sed -n 's/^Expires: *//p' "$f" | tr -d '\r' | head -1)"
  exp_s="$(date -u -d "$exp" +%s 2>/dev/null)" || { echo "FAIL: cannot parse Expires '$exp'" >&2; return 1; }
  if [ "$exp_s" -lt $((now + MIN_DAYS * 86400)) ]; then
    echo "FAIL: $f expires $exp, less than $MIN_DAYS days away; renew it (RFC 9116 treats an expired file as absent)" >&2
    return 1
  fi
  if [ "$exp_s" -gt $((now + 366 * 86400)) ]; then
    echo "FAIL: $f expires $exp, more than a year out" >&2
    return 1
  fi
  echo "ok: $f expires $exp"
}

if [ "${1:-}" = "--self-test" ]; then
  t="$(mktemp -d)"
  trap 'rm -rf "$t"' EXIT
  good() { printf 'Contact: mailto:a@example.org\nCanonical: https://example.org/.well-known/security.txt\nPolicy: https://example.org/p\nExpires: %s\n' "$1"; }
  base="$(date -u -d '2026-11-07' +%s)"
  good 2027-06-01T00:00:00Z > "$t/ok"
  good 2026-11-20T00:00:00Z > "$t/soon"
  good 2026-01-01T00:00:00Z > "$t/past"
  good 2029-01-01T00:00:00Z > "$t/far"
  sed '/^Contact/d' "$t/ok" > "$t/nocontact"
  NOW=$base check "$t/ok" >/dev/null || { echo "SELF-TEST FAIL: valid file rejected" >&2; exit 1; }
  for bad in soon past far nocontact; do
    if NOW=$base check "$t/$bad" >/dev/null 2>&1; then echo "SELF-TEST FAIL: $bad accepted" >&2; exit 1; fi
  done
  echo "self-test ok"
  exit 0
fi

check "${1:-$root/docs/static/.well-known/security.txt}"
