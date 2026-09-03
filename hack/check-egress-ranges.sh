#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Assert the NetworkPolicy's blocked ranges match the Go egress guard's deny table.
#
# WHY THIS EXISTS. The tenant-egress boundary is enforced in two places that cannot see
# each other: core/egress refuses a destination in the dialer for the three paths that
# have a dialer, and the chart's NetworkPolicy bounds the three that do not (MQTT, Kafka
# and AWS SNS/SQS are built inside embedded Bento, which exposes no dialer seam). One is
# Go, the other is YAML, and nothing at compile time or render time relates them.
#
# So the failure this guards is drift in the WIDENING direction, and it is silent: someone
# adds a range to ranges.go — a new cloud's metadata address, say — the Go paths start
# refusing it, the Bento paths keep reaching it, and every test on both sides stays green.
# The boundary is then two different boundaries wearing one name.
#
#   hack/check-egress-ranges.sh              # compare, fail on drift
#   hack/check-egress-ranges.sh --self-test  # prove the comparison can fail
#
# 🔴 What this does NOT check: whether either list is CORRECT, or whether the policy is
# enforced at all. A NetworkPolicy is honoured only by a policy-enforcing CNI, and the
# clusters this repo creates run kindnetd, which is not one. This asserts the two lists
# agree, which is the property no other check can see.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_SRC="$ROOT/backend/core/egress/ranges.go"
VALUES="$ROOT/deploy/helm/devicechain/values.yaml"

# The Go side: every prefix in the `denied` table. Anchored on the exact literal form the
# table uses, so a prefix declared some other way (the embedding-form vars below the
# table, for instance) is not swept in by accident.
go_ranges() {
  grep -oE '\{netip\.MustParsePrefix\("[^"]+"\),' "$GO_SRC" \
    | sed -E 's/.*MustParsePrefix\("([^"]+)"\),/\1/' | sort -u
}

# The chart side: the two blocked-range lists under networkPolicy.
chart_ranges() {
  awk '
    /^  blockedIPv4Ranges:/ , /^  [a-zA-Z]+:[^ ]*$/ { print }
    /^  blockedIPv6Ranges:/ , /^  [a-zA-Z]+:[^ ]*$/ { print }
  ' "$VALUES" | grep -oE '"[0-9a-fA-F:.]+/[0-9]+"' | tr -d '"' | sort -u
}

compare() {
  local go_file chart_file
  go_file="$(mktemp)"; chart_file="$(mktemp)"
  # shellcheck disable=SC2064
  trap "rm -f '$go_file' '$chart_file'" RETURN
  go_ranges > "$go_file"
  chart_ranges > "$chart_file"

  # 🔴 The negative control. An empty extraction on either side would make the comparison
  # trivially pass, and a `diff` of two empty files is the quietest green there is — the
  # exact shape this script exists to catch, reproduced in the script itself.
  local n_go n_chart
  n_go="$(wc -l < "$go_file")"; n_chart="$(wc -l < "$chart_file")"
  if [ "$n_go" -lt 20 ] || [ "$n_chart" -lt 20 ]; then
    echo "extraction produced too few ranges (go=$n_go chart=$n_chart); the comparison would" >&2
    echo "have passed without comparing anything — check the parsers, not the lists." >&2
    return 2
  fi

  if diff -u "$go_file" "$chart_file" > /dev/null; then
    echo "==> egress ranges agree ($n_go prefixes)"
    return 0
  fi

  echo "The Go egress deny table and the NetworkPolicy's blocked ranges have drifted." >&2
  echo >&2
  echo "  - lines only in backend/core/egress/ranges.go   (the dialer refuses, the network does not)" >&2
  echo "  + lines only in deploy/helm/.../values.yaml     (the network refuses, the dialer does not)" >&2
  echo >&2
  diff -u "$go_file" "$chart_file" | tail -n +3 >&2
  echo >&2
  echo "Both halves of the boundary have to name the same address space, or the three Bento" >&2
  echo "egress paths and the three Go ones are protected differently and nothing says so." >&2
  return 1
}

if [ "${1:-}" = "--self-test" ]; then
  # Prove the comparison can fail before its green is used as evidence. A tampered copy of
  # values.yaml must be detected; the real one must then still pass.
  tmp_values="$(mktemp)"
  trap 'rm -f "$tmp_values"' EXIT
  sed '0,/^    - "10.0.0.0\/8"$/{/^    - "10.0.0.0\/8"$/d}' "$VALUES" > "$tmp_values"
  if ! diff -q "$VALUES" "$tmp_values" > /dev/null; then
    VALUES="$tmp_values"
    if compare > /dev/null 2>&1; then
      echo "SELF-TEST FAILED: a removed range was not detected" >&2
      exit 1
    fi
    echo "==> self-test: a removed range is detected"
  else
    echo "SELF-TEST FAILED: could not tamper with the values file, so nothing was proven" >&2
    exit 1
  fi
  VALUES="$ROOT/deploy/helm/devicechain/values.yaml"
  compare
  exit $?
fi

compare
