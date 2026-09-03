#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Assert the rendered NetworkPolicy refuses the same address space as the Go egress
# guard's compiled deny table.
#
# WHY THIS EXISTS. The tenant-egress boundary is enforced in two places that cannot see
# each other: core/egress refuses a destination in the dialer for the three paths that
# have a dialer, and the chart's NetworkPolicy bounds the three that do not (MQTT, Kafka
# and AWS SNS/SQS are built inside embedded Bento, which exposes no dialer seam). One is
# Go, the other is a Helm template, and nothing at compile time or render time relates
# them.
#
# So the failure this guards is drift in the WIDENING direction, and it is silent: someone
# adds a range to ranges.go — a new cloud's metadata address, say — the Go paths start
# refusing it, the Bento paths keep reaching it, and every test on both sides stays green.
# The boundary is then two different boundaries wearing one name.
#
#   hack/check-egress-ranges.sh              # compare, fail on drift
#   hack/check-egress-ranges.sh --self-test  # prove the comparison can fail, then compare
#
# 🔴 IT COMPARES THE COMPILED TABLE AGAINST THE RENDERED POLICY, AND BOTH HALVES OF THAT
# SENTENCE ARE THE FIX FOR A GATE THAT DID NOT WORK.
#
# The previous version read the two SOURCE files with grep and awk. Both inputs were
# wrong, and a mutation run proved it: every edit to the TEMPLATE survived — deleting the
# whole `except` range, wrapping it in a false `if`, pointing it at another values key —
# because the gate never read the template, only values.yaml. It reported the lists in
# perfect agreement while the rendered policy allowed all of 0.0.0.0/0. And on the Go
# side, commenting a table row out left it green, because a comment is still text that
# matches a regex.
#
# Reading the compiled table (via a tiny command that imports the package) and the
# rendered object (via a YAML parser) removes both classes at once: there is no spelling
# of an edit on either side that changes what is enforced without changing what these
# two print. It costs a Go toolchain and a helm binary in the job that runs it.
#
# 🔴 What this still does NOT check: whether either list is CORRECT for the clouds you
# run on, or whether the policy behaves as intended on a cluster. Validity is checked by
# a server-side dry-run in the upgrade gate, on the kind cluster it already stands up —
# a CLIENT-side dry-run is worthless here, it accepts every invalid form we tried.
# Behaviour needs a rig.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CORE_MODULE="$ROOT/backend/core"
CHART="$ROOT/deploy/helm/devicechain"

# The areas the policy needs in order to render at all: outbound-connectors, plus its
# hard dependencies. Stated explicitly rather than as `profile=full`, so this gate does
# not fail for an unrelated reason the day another area in that profile gains a required
# value of its own.
AREAS='{user-management,device-management,event-processing,outbound-connectors}'

# The Go side: the deny table as the COMPILED package reports it.
#
# `mapped` entries are excluded, and the exclusion is derived from the address rather
# than from a list kept here. Kubernetes refuses an IPv4-mapped prefix inside an ipBlock
# — twice over, since Go's net.IPNet.Contains collapses a mapped address to four bytes so
# ::/0 does not contain it either — so such a prefix CANNOT appear in the chart and would
# make a literal comparison fail forever.
#
# 🔴 The previous version kept that exception as a hard-coded list of five prefixes, and
# a mutation run showed what that costs: four of the five were not in the deny table at
# all (they are separate variables the guard reads INTO, not prefixes it denies), so the
# list was mostly dead; and because the filter was unconditional, a prefix the Go guard
# STARTED denying could be silently dropped from the comparison — the exact widening this
# gate exists to catch. Deriving it from Is4In6 makes the exception set exactly the set of
# prefixes that genuinely cannot be expressed.
go_ranges() {
  ( cd "$CORE_MODULE" && go run ./egress/internal/rangedump ) \
    | awk '$3 != "mapped" { print $1, $2 }' | sort
}

# What was excluded, printed rather than filtered in silence — an exception nobody can
# see is how a check stops meaning anything.
go_excluded() {
  ( cd "$CORE_MODULE" && go run ./egress/internal/rangedump ) \
    | awk '$3 == "mapped" { print $1, $2 }' | sort
}

# The chart side: render the policy and parse the rendered object.
#
# The root key is a throwaway. The chart requires one for any profile carrying a
# secret-store area, and rendering is all this does with it.
chart_ranges() {
  chart_ranges_from "$CHART"
}

chart_ranges_from() {
  local chart_dir="$1" key
  key="$(openssl rand -base64 32)"
  helm template "$chart_dir" \
    --set "instance.config.infrastructure.secrets.rootKey=$key" \
    --set "enabledFunctionalAreas=$AREAS" \
    --set networkPolicy.enabled=true \
    --show-only templates/networkpolicy.yaml \
    | ( cd "$CORE_MODULE" && go run ./egress/internal/policyranges ) \
    | sort
}

# gather writes the two sides to the files named by $1 (Go) and $2 (chart).
#
# Each side reports its own failure rather than aborting the script, so "the policy would
# not render" and "the two lists disagree" are different messages. They have different
# causes and different fixes, and errexit would collapse them into one silent non-zero
# exit.
gather() {
  if ! go_ranges > "$1"; then
    echo "could not read the Go deny table (backend/core/egress). This needs a Go" >&2
    echo "toolchain; the comparison was not performed." >&2
    return 2
  fi
  if ! chart_ranges > "$2"; then
    echo "could not render or parse the chart's NetworkPolicy. This needs helm and a Go" >&2
    echo "toolchain; the comparison was not performed. If the render itself failed, the" >&2
    echo "message above is helm's and the policy is invalid or unrenderable — which is a" >&2
    echo "failure in its own right, not a missing prerequisite." >&2
    return 2
  fi
}

# compare takes the two extractions as FILES and does not call the extractors itself.
#
# 🔴 THAT SIGNATURE IS A BUG FIX, FOUND BY MUTATING THIS SCRIPT. The first version had
# compare() call go_ranges/chart_ranges directly, and the self-test arms swapped in
# tampered versions by redefining those functions and then redefining them BACK. The
# restore was a second copy of the real function body — so when the real go_ranges was
# rotted to emit nothing, the self-test's own cleanup silently reinstated a working one
# and the gate passed. A gate that repairs the defect it is meant to detect is worse than
# no gate. Passing files means there is nothing to restore and no second copy of anything.
compare() {
  local go_file="$1" chart_file="$2"

  # 🔴 The negative control. An empty extraction on either side would make the comparison
  # trivially pass, and a `diff` of two empty files is the quietest green there is — the
  # exact shape this script exists to catch, reproduced in the script itself.
  local n_go n_chart
  n_go="$(wc -l < "$go_file")"; n_chart="$(wc -l < "$chart_file")"
  if [ "$n_go" -lt 20 ] || [ "$n_chart" -lt 20 ]; then
    echo "extraction produced too few ranges (go=$n_go chart=$n_chart); the comparison would" >&2
    echo "have passed without comparing anything — check the two commands under" >&2
    echo "backend/core/egress/internal, not the lists." >&2
    return 2
  fi

  if diff -u "$go_file" "$chart_file" > /dev/null; then
    local excluded
    excluded="$(go_excluded | tr '\n' ' ')"
    echo "==> egress ranges agree ($n_go prefixes, compared per address family)"
    if [ -n "$excluded" ]; then
      echo "    not comparable, excluded by address form: ${excluded% }"
    fi
    return 0
  fi

  echo "The Go egress deny table and the rendered NetworkPolicy have drifted." >&2
  echo >&2
  echo "  - lines only in backend/core/egress/ranges.go   (the dialer refuses, the network does not)" >&2
  echo "  + lines only in the rendered policy             (the network refuses, the dialer does not)" >&2
  echo >&2
  diff -u "$go_file" "$chart_file" | tail -n +3 >&2 || true
  echo >&2
  echo "Both halves of the boundary have to name the same address space, or the three Bento" >&2
  echo "egress paths and the three Go ones are protected differently and nothing says so." >&2
  echo "The chart's half is templates/_helpers.tpl (devicechain.egressDeniedIPv4 / ...IPv6)." >&2
  return 1
}

# A tampered copy of the chart, for the self-test. Copying the real chart and editing the
# copy means the arms below exercise the real render path — the parser, the template and
# the comparison — rather than a substituted function that only proves `diff` works.
tampered_chart() {
  local sed_expr="$1" dir
  dir="$(mktemp -d)"
  cp -r "$CHART" "$dir/chart"
  sed -i "$sed_expr" "$dir/chart/templates/_helpers.tpl"
  echo "$dir/chart"
}

self_test() {
  local dir g c
  g="$(mktemp)"; c="$(mktemp)"
  # shellcheck disable=SC2064
  trap "rm -f '$g' '$c'" RETURN

  # Arm 1: a single prefix removed from the chart's deny table. The everyday drift.
  dir="$(tampered_chart '/^- "172.16.0.0\/12"$/d')"
  if chart_ranges_from "$dir" 2>/dev/null | grep -qxF 'v4 172.16.0.0/12'; then
    echo "SELF-TEST FAILED: the tamper did not take effect, so nothing was proven" >&2
    return 1
  fi
  go_ranges > "$g" || { echo "SELF-TEST FAILED: the Go side could not be read" >&2; return 1; }
  chart_ranges_from "$dir" > "$c" || { echo "SELF-TEST FAILED: the tampered chart would not render" >&2; return 1; }
  if compare "$g" "$c" > /dev/null 2>&1; then
    echo "SELF-TEST FAILED: a prefix removed from the chart was not detected" >&2
    return 1
  fi
  echo "==> self-test: a prefix removed from the chart's deny table is detected"

  # Arm 2: the whole IPv4 deny list emptied, which renders an ipBlock permitting all of
  # 0.0.0.0/0. 🔴 THIS IS THE ARM THAT MATTERS. Under the previous gate every edit of
  # this shape passed, because it read values.yaml and the boundary is rendered from a
  # template. It is an arm rather than a comment so it cannot quietly stop being true.
  dir="$(tampered_chart '/^{{- define "devicechain.egressDeniedIPv4" -}}$/,/^{{- end }}$/{/^- "/d}')"
  if chart_ranges_from "$dir" >/dev/null 2>&1; then
    echo "SELF-TEST FAILED: emptying the IPv4 deny table produced a policy that parsed" >&2
    echo "cleanly. It should have been refused — either by the chart's own render guard or" >&2
    echo "by the parser requiring two except-bearing ipBlocks." >&2
    return 1
  fi
  echo "==> self-test: emptying the chart's IPv4 deny table is refused outright"

  # Arm 3: the GO side tampered. Tampering only one side leaves a whole class untested —
  # an extractor that derived its answer from the other side would pass every arm above.
  go_ranges | grep -vxF 'v4 10.0.0.0/8' > "$g" \
    || { echo "SELF-TEST FAILED: the Go side could not be read" >&2; return 1; }
  chart_ranges > "$c" || { echo "SELF-TEST FAILED: the chart would not render" >&2; return 1; }
  if compare "$g" "$c" > /dev/null 2>&1; then
    echo "SELF-TEST FAILED: a prefix removed from the Go deny table was not detected" >&2
    return 1
  fi
  echo "==> self-test: a prefix removed from the Go deny table is detected"
}

main() {
  local g c
  g="$(mktemp)"; c="$(mktemp)"
  # shellcheck disable=SC2064
  trap "rm -f '$g' '$c'" RETURN
  gather "$g" "$c" || return $?
  compare "$g" "$c"
}

if [ "${1:-}" = "--self-test" ]; then
  self_test
  main
  exit $?
fi

main
