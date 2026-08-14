#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guard: every rejection code command-delivery declares is allowed in a metric label.
#
# WHY. `batch_refusals_total` carries a `code` label, and a Prometheus label fed from an
# open vocabulary is an unbounded cardinality vector — the thing the platform's no-tenant-
# label rule exists to prevent. So model/metrics.go keeps `metricSafeCodes`, an allowlist,
# and folds anything outside it to "other".
#
# That allowlist has two kinds of entry, and only one of them can be checked here:
#
#   OURS      the RejectionCode constants this service declares. If one is added and not
#             allowlisted, its refusals silently pile into "other" — the panel that is
#             supposed to say WHY fleet writes are being refused answers "something".
#             That is what this guard catches.
#
#   RELAYED   codes device-management produces, which command-delivery passes through
#             unchanged and deliberately does NOT re-declare (see RejectionCode's own
#             doc: two constant sets naming one enum drift silently). A new code of
#             theirs cannot be caught from this tree at all. It shows up at runtime as a
#             growing "other" series, which is the intended signal.
#
# The asymmetry is the point: ours drift silently, so a gate catches them; theirs
# announce themselves, so they do not need one.
#
# It also covers the OTHER fold in the same file — targetKindLabel, which maps a stored
# BatchTargetKind to a label and answers anything it does not recognise with "unknown". A
# third target kind added without touching that switch would report 100% of its batches as
# unknown, silently, on the same dashboard. Same failure, same file, so the same guard.
#
#   hack/check-rejection-code-labels.sh              # check
#   hack/check-rejection-code-labels.sh --self-test  # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

MODEL_DIR="backend/services/command-delivery/model"
METRICS_FILE="$MODEL_DIR/metrics.go"

# Declared codes are read from the CONSTANT DECLARATIONS, not from a list anyone maintains
# alongside them. A guard whose input is itself hand-written has the drift problem it was
# written to solve.
declared_codes() {
  local dir="$1"
  grep -rhoE 'RejectionCode = "[A-Z_]+"' "$dir" | sed -E 's/.*"([A-Z_]+)"/\1/' | sort -u
}

# The allowlist is read only from the map literal, so a code that merely appears somewhere
# in metrics.go — in a comment, say — does not count as allowlisted.
allowlisted_codes() {
  local file="$1"
  sed -n '/^var metricSafeCodes = map\[RejectionCode\]struct{}{$/,/^}$/p' "$file" |
    grep -oE '"[A-Z_]+"|Reject[A-Za-z]+:' || true
}

check_tree() {
  local dir="$1" file="$2" missing=0 code const_name allow
  allow="$(allowlisted_codes "$file")"

  while read -r code; do
    [ -n "$code" ] || continue
    # A code is allowlisted either by its literal value (the relayed entries) or by the
    # constant that carries it (ours). Both forms are legitimate, so both are accepted.
    const_name="$(grep -rhE "Reject[A-Za-z]+ +RejectionCode = \"$code\"" "$dir" |
      sed -E 's/^[[:space:]]*(Reject[A-Za-z]+).*/\1/' | head -1)"
    if printf '%s\n' "$allow" | grep -qx "\"$code\""; then
      continue
    fi
    if [ -n "$const_name" ] && printf '%s\n' "$allow" | grep -qx "$const_name:"; then
      continue
    fi
    echo "  $code${const_name:+  (${const_name})}"
    missing=1
  done <<<"$(declared_codes "$dir")"

  return "$missing"
}

# check_target_kinds asserts every declared BatchTargetKind is named in targetKindLabel's
# switch. The switch has a default arm, so an unnamed kind compiles, runs, and reports
# "unknown" forever — there is no runtime signal at all.
check_target_kinds() {
  local dir="$1" file="$2" missing=0 const_name
  local body
  body="$(sed -n '/^func targetKindLabel(/,/^}$/p' "$file")"

  while read -r const_name; do
    [ -n "$const_name" ] || continue
    if ! printf '%s\n' "$body" | grep -q "case ${const_name}:"; then
      echo "  $const_name"
      missing=1
    fi
  done <<<"$(grep -rhoE '\b(BatchTarget[A-Za-z]+) +BatchTargetKind = ' "$dir" |
    sed -E 's/ +BatchTargetKind = //' | sort -u)"

  return "$missing"
}

self_test() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  # A clean tree must pass, or every failing case below proves nothing.
  cp -r "$MODEL_DIR" "$tmp/model"
  if ! check_tree "$tmp/model" "$tmp/model/metrics.go" >/dev/null; then
    echo "🔴 self-test: the real tree does not pass; every case below is meaningless" >&2
    return 1
  fi
  echo "  ok   a clean tree passes"

  # Each defect is planted ALONE. A self-test that plants several at once is satisfied
  # by a checker that finds any one of them.

  # 1. A new code declared and never allowlisted — the drift this exists for.
  rm -rf "$tmp/model"
  cp -r "$MODEL_DIR" "$tmp/model"
  printf '\nconst RejectFleetOnFire RejectionCode = "FLEET_ON_FIRE"\n' >>"$tmp/model/api.go"
  if check_tree "$tmp/model" "$tmp/model/metrics.go" >/dev/null; then
    echo "🔴 self-test: an unallowlisted code was not caught" >&2
    return 1
  fi
  echo "  ok   an unallowlisted code is caught"

  # 2. An existing code dropped FROM the allowlist. Same defect reached from the other
  #    side, and the one a refactor produces.
  rm -rf "$tmp/model"
  cp -r "$MODEL_DIR" "$tmp/model"
  sed -i '/^\tRejectBatchTooLarge: *{},$/d' "$tmp/model/metrics.go"
  if check_tree "$tmp/model" "$tmp/model/metrics.go" >/dev/null; then
    echo "🔴 self-test: a code removed from the allowlist was not caught" >&2
    return 1
  fi
  echo "  ok   a code removed from the allowlist is caught"

  # 3. A code mentioned in metrics.go but NOT inside the map literal. Without reading only
  #    the literal, a comment naming a code would satisfy the guard.
  rm -rf "$tmp/model"
  cp -r "$MODEL_DIR" "$tmp/model"
  sed -i '/^\tRejectBatchTooLarge: *{},$/d' "$tmp/model/metrics.go"
  printf '\n// RejectBatchTooLarge: is mentioned here but allowlisted nowhere.\n' >>"$tmp/model/metrics.go"
  if check_tree "$tmp/model" "$tmp/model/metrics.go" >/dev/null; then
    echo "🔴 self-test: a code named only in a comment satisfied the guard" >&2
    return 1
  fi
  echo "  ok   a mention outside the map literal does not count"

  # 4. A target kind declared and never named in targetKindLabel's switch.
  rm -rf "$tmp/model"
  cp -r "$MODEL_DIR" "$tmp/model"
  printf '\nconst BatchTargetFacet BatchTargetKind = "FACET"\n' >>"$tmp/model/model_batch.go"
  if check_target_kinds "$tmp/model" "$tmp/model/metrics.go" >/dev/null; then
    echo "🔴 self-test: a target kind missing from targetKindLabel was not caught" >&2
    return 1
  fi
  echo "  ok   a target kind missing from targetKindLabel is caught"

  return 0
}

# A guard that cannot read its inputs must FAIL, not skip. "No findings" and "never ran"
# have to look different from each other.
for required in "$MODEL_DIR" "$METRICS_FILE"; do
  if [ ! -e "$required" ]; then
    echo "🔴 $required is missing; this guard cannot report on a tree it cannot read" >&2
    exit 1
  fi
done
if ! grep -q '^var metricSafeCodes = map\[RejectionCode\]struct{}{$' "$METRICS_FILE"; then
  echo "🔴 $METRICS_FILE has no metricSafeCodes map literal in the expected form;" >&2
  echo "   this guard reads that literal and would otherwise pass by finding nothing." >&2
  exit 1
fi

if [ "${1:-}" = "--self-test" ]; then
  self_test
  echo "==> self-test passed: the guard fails on each defect, alone."
  exit 0
fi

echo "==> Checking command-delivery metric labels name every declared value."
failed=0
if ! output="$(check_tree "$MODEL_DIR" "$METRICS_FILE")"; then
  echo "🔴 declared but not allowlisted in metricSafeCodes:" >&2
  echo "$output" >&2
  echo >&2
  echo "   Their refusals would be counted as \"other\", so the panel that answers WHY" >&2
  echo "   fleet writes are refused would stop naming them. Add each to metricSafeCodes" >&2
  echo "   in $METRICS_FILE." >&2
  failed=1
fi
if ! output="$(check_target_kinds "$MODEL_DIR" "$METRICS_FILE")"; then
  echo "🔴 declared but not named in targetKindLabel's switch:" >&2
  echo "$output" >&2
  echo >&2
  echo "   Every batch of that kind would report target_kind=\"unknown\", with no error" >&2
  echo "   anywhere. Add a case to targetKindLabel in $METRICS_FILE." >&2
  failed=1
fi
[ "$failed" -eq 0 ] || exit 1
echo "==> Every declared rejection code and target kind reaches a real label."
