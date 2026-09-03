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
# 🔴 What this does NOT check: whether either list is CORRECT, whether the rendered policy
# is valid Kubernetes, or whether it behaves as intended on a cluster. It asserts the two
# lists agree, which is the property no other check can see. Validity and behaviour need a
# server-side dry-run and a cluster that enforces policy — a local kind cluster is one,
# since kindnetd has shipped kube-network-policies since kind v0.24.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_SRC="$ROOT/backend/core/egress/ranges.go"
VALUES="$ROOT/deploy/helm/devicechain/values.yaml"

# 🔴 GO-ONLY PREFIXES. One entry in the Go table must NOT appear in the chart, so a
# literal diff would fail forever. It is declared here with its reason rather than
# filtered quietly, because an exception nobody can see is how a check stops meaning
# anything.
#
#   ::ffff:0:0/96 — Kubernetes rejects an IPv4-mapped address in an ipBlock, and would
#   reject it anyway as "not a strict subset of ::/0": Go's net.IPNet.Contains collapses
#   a v4-mapped address to four bytes, so ::/0 does not contain it (verified directly).
#   Including it makes the whole NetworkPolicy invalid and the Helm release fails to
#   install. It has no on-the-wire meaning either — a v4-mapped address never appears on
#   a packet, and the Go table carries it only as belt-and-braces behind an unmap that
#   has already run.
#
#   ::/96, 64:ff9b::/96, 2002::/16, 2001::/32 — the four IPv6 forms that CARRY an IPv4
#   address. Go does not deny these; it extracts the address inside and judges THAT, so a
#   NAT64 or 6to4 wrapper around a public host is allowed and one around 169.254.169.254
#   is not. A NetworkPolicy cannot express "look inside": ipBlock compares prefixes.
#
#   🔴 So this is a REAL RESIDUAL, not a bookkeeping detail. On a dual-stack or NAT64
#   cluster, a tenant Kafka or MQTT broker at 64:ff9b::a9fe:a9fe reaches the metadata
#   service through the Bento paths, because the policy sees an ordinary global-unicast
#   v6 address. Denying the four prefixes outright is NOT the fix — on a NAT64-only
#   cluster 64:ff9b::/96 is how everything is reached, so that would deny all egress.
#   Closing it properly needs the policy to be generated per-cluster from its actual
#   NAT64 prefix, or those paths to gain a dialer. Neither is in this change.
GO_ONLY='::ffff:0:0/96
::/96
64:ff9b::/96
2002::/16
2001::/32'

# The Go side: every prefix in the `denied` table, minus the declared Go-only set.
#
# 🔴 It anchors on the literal form that table uses, and that is a real weakness worth
# knowing rather than hiding: a prefix added in some other form — a keyed struct literal,
# a second table — is invisible here, and the floor below cannot see one missing line. If
# you change how the table is written, change this with it.
go_ranges() {
  grep -oE 'netip\.MustParsePrefix\("[^"]+"\)' "$GO_SRC" \
    | sed -E 's/.*MustParsePrefix\("([^"]+)"\)/\1/' \
    | grep -vxF -f <(printf '%s\n' "$GO_ONLY") | sort -u
}

# The chart side: the two blocked-range lists under networkPolicy.
#
# Each list is opened by its own key and closed by the next key at the SAME indentation,
# so the extraction does not depend on what happens to sit below it. The previous version
# used awk range patterns whose end condition never matched, so both ranges ran to the end
# of an unrelated block and worked only by accident of what was in between.
chart_ranges() {
  awk '
    /^  blockedIPv4Ranges:$/ { inlist = 1; next }
    /^  blockedIPv6Ranges:$/ { inlist = 1; next }
    /^  [^ ]/                { inlist = 0 }
    inlist && /^    - "/     { print }
  ' "$VALUES" | grep -oE '"[0-9a-fA-F:.]+/[0-9]+"' | tr -d '"' | sort -u
}

# 🔴 A prefix Kubernetes will not accept makes the WHOLE policy invalid, so the release
# fails to install and the feature cannot be turned on at all. That happened: an
# IPv4-mapped prefix in the v6 list was rejected twice over — "must not have an
# IPv4-mapped IPv6 address", and "must be a strict subset of ::/0", because Go's
# net.IPNet.Contains collapses a v4-mapped address to four bytes so ::/0 does not contain
# it.
#
# Nothing caught it: helm lint does not execute the body of a false `if`, no CI job renders
# this template enabled, and rendering would not have been enough anyway — it takes API
# validation. This checks the one class cheaply and without a cluster. The real proof is a
# server-side dry-run, which is what found it.
assert_no_mapped() {
  local bad
  bad="$(chart_ranges | grep -iE '^::ffff:' || true)"
  if [ -n "$bad" ]; then
    echo "The NetworkPolicy's blocked ranges contain an IPv4-mapped IPv6 prefix:" >&2
    echo "$bad" | sed 's/^/  /' >&2
    echo >&2
    echo "Kubernetes rejects these in an ipBlock, and the whole policy is then invalid —" >&2
    echo "so enabling networkPolicy fails the Helm release rather than degrading. A" >&2
    echo "v4-mapped address never appears on the wire; it belongs in the Go table only." >&2
    return 1
  fi
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

  assert_no_mapped || return 1

  if diff -u "$go_file" "$chart_file" > /dev/null; then
    echo "==> egress ranges agree ($n_go prefixes), and none is a form Kubernetes refuses"
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
