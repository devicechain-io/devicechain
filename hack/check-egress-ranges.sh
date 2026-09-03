#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Assert the rendered egress NetworkPolicy still refuses what the Go guard refuses, and
# that the rest of the policy is what it was when somebody last looked at it.
#
# WHY THIS EXISTS. The tenant-egress boundary is enforced in two places that cannot see
# each other: core/egress refuses a destination in the dialer for the three paths that
# have a dialer, and the chart's NetworkPolicy bounds the three that do not (MQTT, Kafka
# and AWS SNS/SQS are built inside embedded Bento, which exposes no dialer seam). One is
# Go, the other is a Helm template, and nothing at compile time or render time relates
# them. The drift is silent and always widening: add a range to ranges.go, the Go paths
# refuse it, the Bento paths keep reaching it, every test on both sides stays green.
#
#   hack/check-egress-ranges.sh              # compare, fail on drift
#   hack/check-egress-ranges.sh --self-test  # prove the checks can fail, then compare
#   hack/check-egress-ranges.sh --update     # regenerate the golden policy
#
# 🔴 IT MAKES TWO SEPARATE COMPARISONS, AND NEITHER IS SUFFICIENT ALONE. That split is
# the correction of a real defect, so it is worth stating plainly rather than as a design
# flourish:
#
#   1. THE PREFIXES. The compiled Go deny table, against the `except` lists of the
#      rendered policy, per address family.
#   2. THE POLICY. Every rendered NetworkPolicy document, against a golden file.
#
# An earlier version made only comparison 1 and claimed — in five places, including this
# header — that "there is no spelling of a template edit that changes what the policy
# permits without changing what this prints". That was FALSE, and a review produced eight
# spellings. `policyTypes: [Ingress]` makes every egress rule inert and reads like a
# tidy-up. A podSelector matching no pods protects nothing. An empty namespaceSelector on
# the DNS rule opens port 53 to the cluster. An extra `- {}` rule allows everything. A
# SECOND NetworkPolicy selecting the same pods opens everything, because policies union —
# and if it lives in a second template file, `--show-only` hid it from the gate, from the
# server-side dry-run, and from the profile-workloads check simultaneously. Every one of
# those renders 28 agreeing prefixes, and every one is valid Kubernetes, so a dry-run
# accepts them too.
#
# Hence the golden: the prefixes are compared BY VALUE against Go, and everything else is
# pinned so that changing it is a diff a human accepts on purpose. The golden holds the
# prefixes as well. That is deliberate duplication — it means a deny-table change shows up
# twice in one commit, which is the right number of times for an edit to a security
# boundary.
#
# 🔴 THE LIMIT OF THE SELF-TEST, STATED RATHER THAN GLOSSED. The arms drive the same
# wiring CI drives, so rot in the extractors, the comparison or the wiring is caught. What
# they cannot catch is `main` itself: a main that ignores run_against and returns 0 passes
# every arm, because main's status IS the script's status and the arms run inside it. No
# check can be its own last line of defence. That one is caught by reading the diff.
#
# 🔴 What this still does NOT check: whether the deny table is CORRECT for the clouds you
# run on, or whether the policy behaves as intended on a cluster. It also compares the
# TABLE rather than the guard's decision — a Go edit that stops consulting the table is a
# question for core/egress's own tests, and TestEveryDeniedPrefixIsRefused is the one that
# makes that link total rather than sampled. Validity is checked by a server-side dry-run
# in the upgrade gate, on the kind cluster it already stands up; a CLIENT-side dry-run is
# worthless here, it accepts every invalid form we tried. Behaviour needs a rig.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CORE_MODULE="$ROOT/backend/core"
CHART="$ROOT/deploy/helm/devicechain"
GOLDEN="$ROOT/hack/testdata/egress-networkpolicy.yaml"

# The areas the policy needs in order to render: outbound-connectors plus its hard
# dependencies. Stated explicitly rather than as `profile=full`, so this gate does not
# fail for an unrelated reason the day another area in that profile gains a required
# value of its own.
AREAS='{user-management,device-management,event-processing,outbound-connectors}'

# Render the WHOLE chart with the policy enabled.
#
# 🔴 NOT `--show-only templates/networkpolicy.yaml`. That flag is why a policy added in a
# second template file was invisible to every instrument at once. The documents are
# selected by KIND, from the parsed stream, downstream of here.
render_chart() {
  local chart_dir="$1" key
  key="$(openssl rand -base64 32)"
  helm template "$chart_dir" \
    --set "instance.config.infrastructure.secrets.rootKey=$key" \
    --set "enabledFunctionalAreas=$AREAS" \
    --set networkPolicy.enabled=true
}

# The Go side: the deny table as the COMPILED package reports it.
#
# `mapped` entries are excluded, and the exclusion is derived from the address rather than
# from a list kept here: Kubernetes refuses an IPv4-mapped prefix inside an ipBlock, twice
# over, so such a prefix cannot appear in the chart and would make a literal comparison
# fail forever. The previous version kept that exception as a hard-coded list of five
# prefixes, four of which were never in the deny table at all — and because the filter was
# unconditional, a prefix the guard STARTED denying could be dropped from the comparison
# silently, which is the widening this gate exists to catch.
go_ranges() {
  ( cd "$CORE_MODULE" && go run ./egress/internal/rangedump ) \
    | awk '$3 != "mapped" { print $1, $2 }' | sort
}

# What was excluded, printed rather than filtered in silence — an exception nobody can see
# is how a check stops meaning anything.
go_excluded() {
  ( cd "$CORE_MODULE" && go run ./egress/internal/rangedump ) \
    | awk '$3 == "mapped" { print $1, $2 }' | sort
}

chart_ranges_from() {
  render_chart "$1" | ( cd "$CORE_MODULE" && go run ./egress/internal/policyranges ) | sort
}

chart_policies_from() {
  render_chart "$1" | ( cd "$CORE_MODULE" && go run ./egress/internal/policyranges -policies )
}

# The golden's exact bytes, header included, for a given chart directory.
#
# Both --update and the comparison go through this one function, so the file on disk and
# the file compared against it are produced identically. Writing the header in one place
# and stripping it in the other is how a golden acquires a diff that is not a change.
golden_body() {
  echo "# The rendered egress NetworkPolicy. Generated by hack/check-egress-ranges.sh --update."
  echo "#"
  echo "# Pinned so that a change to the policy AROUND the deny lists — a selector, policyTypes,"
  echo "# a peer, an added rule, or a whole second policy document — is a diff somebody accepts on"
  echo "# purpose. Comparing the prefix lists says nothing about whether the object enforces"
  echo "# anything at all: policyTypes: [Ingress] leaves the prefixes identical and makes every"
  echo "# egress rule inert."
  chart_policies_from "$1"
}

# gather writes the three extractions to the files named by $1 (Go table), $2 (rendered
# prefixes) and $3 (rendered policy documents), rendering the chart at $4.
#
# Each side reports its own failure rather than aborting, so "the policy would not render"
# and "the two lists disagree" stay different messages with different causes and different
# fixes; errexit would collapse them into one silent non-zero exit.
gather() {
  local go_file="$1" chart_file="$2" policy_file="$3" chart_dir="$4" drop="${5:-}"
  # `drop` removes one line from the Go extraction, and it exists so the self-test can
  # tamper the GO side WITHOUT bypassing this function. The alternative — redefining
  # go_ranges around a call — is what a previous version did, and its restore silently
  # healed a rotted extractor. A seam the real path carries and never uses is cheaper
  # than a seam that reaches around the real path.
  if [ -n "$drop" ]; then
    if ! go_ranges | grep -vxF "$drop" > "$go_file"; then
      echo "could not read the Go deny table (backend/core/egress); nothing was compared." >&2
      return 2
    fi
  elif ! go_ranges > "$go_file"; then
    echo "could not read the Go deny table (backend/core/egress). This needs a Go" >&2
    echo "toolchain; nothing was compared." >&2
    return 2
  fi
  if ! chart_ranges_from "$chart_dir" > "$chart_file"; then
    echo "could not render or parse the chart's NetworkPolicy. This needs helm and a Go" >&2
    echo "toolchain; nothing was compared. If the render itself failed, the message above" >&2
    echo "is helm's and the policy is unrenderable — a failure in its own right, not a" >&2
    echo "missing prerequisite." >&2
    return 2
  fi
  if ! golden_body "$chart_dir" > "$policy_file"; then
    echo "could not extract the rendered NetworkPolicy documents; nothing was compared." >&2
    return 2
  fi
}

compare() {
  local go_file="$1" chart_file="$2" policy_file="$3"

  # 🔴 The negative control. An empty extraction on either side would make the comparison
  # trivially pass, and a diff of two empty files is the quietest green there is — the
  # exact shape this script exists to catch, reproduced in the script itself.
  local n_go n_chart
  n_go="$(wc -l < "$go_file")"; n_chart="$(wc -l < "$chart_file")"
  if [ "$n_go" -lt 20 ] || [ "$n_chart" -lt 20 ]; then
    echo "extraction produced too few ranges (go=$n_go chart=$n_chart); the comparison would" >&2
    echo "have passed without comparing anything — check the two commands under" >&2
    echo "backend/core/egress/internal, not the lists." >&2
    return 2
  fi

  if ! diff -u "$go_file" "$chart_file" > /dev/null; then
    echo "The Go egress deny table and the rendered NetworkPolicy have drifted." >&2
    echo >&2
    echo "  - only in backend/core/egress/ranges.go   (the dialer refuses, the network does not)" >&2
    echo "  + only in the rendered policy             (the network refuses, the dialer does not)" >&2
    echo >&2
    diff -u "$go_file" "$chart_file" | tail -n +3 >&2 || true
    echo >&2
    echo "Both halves of the boundary have to name the same address space, or the three Bento" >&2
    echo "egress paths and the three Go ones are protected differently and nothing says so." >&2
    echo "The chart's half is written in the body of templates/networkpolicy.yaml." >&2
    return 1
  fi

  if [ ! -f "$GOLDEN" ]; then
    echo "the golden policy $GOLDEN does not exist; run $0 --update" >&2
    return 2
  fi
  if ! diff -u "$GOLDEN" "$policy_file" > /dev/null; then
    echo "The rendered egress NetworkPolicy differs from the golden." >&2
    echo >&2
    echo "  - the golden          (hack/testdata/egress-networkpolicy.yaml)" >&2
    echo "  + what renders now" >&2
    echo >&2
    diff -u "$GOLDEN" "$policy_file" | tail -n +3 >&2 || true
    echo >&2
    echo "The prefix lists agree, so this is a change to the policy AROUND them — a selector," >&2
    echo "policyTypes, a peer, an added rule, or a second policy document. Those decide whether" >&2
    echo "the object enforces anything at all, and comparing prefixes says nothing about them." >&2
    echo "If the change is intended, run $0 --update and commit the golden with it." >&2
    return 1
  fi

  local excluded
  excluded="$(go_excluded | tr '\n' ' ')"
  echo "==> egress ranges agree ($n_go prefixes, compared per address family)"
  if [ -n "$excluded" ]; then
    echo "    not comparable, excluded by address form: ${excluded% }"
  fi
  echo "==> the rendered policy matches the golden ($(grep -c '^---$' "$policy_file") document(s))"
}

# A tampered copy of the chart. Copying the real chart and editing the copy means the arms
# below exercise the real render path — the template, the parser and the comparison —
# rather than a substituted function that only proves diff works.
tampered_chart() {
  local sed_expr="$1" file="$2" dir
  dir="$(mktemp -d)"
  TAMPER_DIRS+=("$dir")
  cp -r "$CHART" "$dir/chart"
  sed -i "$sed_expr" "$dir/chart/$file"
  echo "$dir/chart"
}

# run_against performs the whole check against a chart directory, exactly as CI does.
#
# 🔴 THE ARMS GO THROUGH THIS, NOT STRAIGHT INTO compare(). An earlier self-test called
# compare() directly, so the wiring in main() — which is the code CI actually runs — was
# exercised by nothing, and changing main to compare the Go file against itself passed all
# three arms.
run_against() {
  local chart_dir="$1" drop="${2:-}" g c p
  g="$(mktemp)"; c="$(mktemp)"; p="$(mktemp)"
  local rc=0
  gather "$g" "$c" "$p" "$chart_dir" "$drop" && compare "$g" "$c" "$p" || rc=$?
  rm -f "$g" "$c" "$p"
  return $rc
}

TAMPER_DIRS=()
cleanup_tampered() {
  local d
  for d in "${TAMPER_DIRS[@]:-}"; do
    [ -n "$d" ] && rm -rf "$d"
  done
  # 🔴 Explicit, and not belt-and-braces. On an EMPTY array "${a[@]:-}" expands to one
  # empty string, so the loop runs once, `[ -n "" ]` is false, and the function returns 1
  # — which, from a RETURN trap, became the status of the whole self-test. Every arm
  # printed and passed and the script still exited 1. Same shape as the sweep loop whose
  # status is its last echo.
  return 0
}

self_test() {
  local dir
  trap cleanup_tampered RETURN

  # Arm 1: one prefix removed from the chart's deny table. The everyday drift.
  dir="$(tampered_chart '/^    "172.16.0.0\/12"$/d' templates/networkpolicy.yaml)"
  if chart_ranges_from "$dir" 2>/dev/null | grep -qxF 'v4 172.16.0.0/12'; then
    echo "SELF-TEST FAILED: the tamper did not take effect, so nothing was proven" >&2
    return 1
  fi
  if run_against "$dir" > /dev/null 2>&1; then
    echo "SELF-TEST FAILED: a prefix removed from the chart was not detected" >&2
    return 1
  fi
  echo "==> self-test: a prefix removed from the chart's deny table is detected"

  # Arm 2: the policy still refuses the same addresses, and stops enforcing anything.
  # 🔴 THIS IS THE ARM THE PREVIOUS GATE COULD NOT HAVE HAD. Flipping policyTypes leaves
  # the prefixes identical and makes every egress rule inert; only the golden sees it.
  dir="$(tampered_chart 's/^    - Egress$/    - Ingress/' templates/networkpolicy.yaml)"
  if ! chart_policies_from "$dir" 2>/dev/null | grep -q 'Ingress'; then
    echo "SELF-TEST FAILED: the policyTypes tamper did not take effect, so nothing was proven" >&2
    return 1
  fi
  if run_against "$dir" > /dev/null 2>&1; then
    echo "SELF-TEST FAILED: a policy whose egress rules are inert was not detected" >&2
    return 1
  fi
  echo "==> self-test: a policy that stops enforcing egress is detected"

  # Arm 3: the GO extraction tampered. Tampering only the chart leaves a whole class
  # untested — an extractor deriving its answer from the other side passes every arm above.
  # 🔴 Through run_against, like every other arm. When this called compare() directly it
  # could not see a change to the wiring, and comparing the Go file against itself passed
  # the whole self-test while the chart/Go relation was dead.
  if run_against "$CHART" 'v4 10.0.0.0/8' > /dev/null 2>&1; then
    echo "SELF-TEST FAILED: a prefix removed from the Go extraction was not detected" >&2
    return 1
  fi
  echo "==> self-test: a prefix removed from the Go extraction is detected"

  # Arm 4: INDEPENDENCE. Arm 3 proves compare() notices a one-sided difference; it does
  # not prove the two sides are gathered independently. If go_ranges were derived from the
  # chart, every arm above still passes and the gate compares the chart with itself.
  local go_real go_tampered
  go_real="$(go_ranges | md5sum)"
  dir="$(tampered_chart '/^    "172.16.0.0\/12"$/d' templates/networkpolicy.yaml)"
  go_tampered="$(CHART="$dir" go_ranges | md5sum)"
  if [ "$go_real" != "$go_tampered" ]; then
    echo "SELF-TEST FAILED: the Go extraction CHANGED when only the chart was tampered," >&2
    echo "so the two sides are not independent and the comparison is circular." >&2
    return 1
  fi
  echo "==> self-test: the Go extraction does not move when the chart does"
}

update_golden() {
  local p; p="$(mktemp)"
  if ! golden_body "$CHART" > "$p"; then
    echo "could not render the policy; the golden was not updated" >&2
    rm -f "$p"; return 1
  fi
  mkdir -p "$(dirname "$GOLDEN")"
  mv "$p" "$GOLDEN"
  echo "==> wrote $GOLDEN"
}

# main is run_against the real chart, and that is the whole point: there is ONE piece of
# wiring, and the self-test arms drive it.
#
# 🔴 An earlier version had main gather-and-compare on its own while the arms called a
# near-identical copy. Changing main to compare the Go file against ITSELF then passed
# every arm — the path CI actually runs was exercised by nothing. Two copies of the wiring
# is one copy too many for the same reason two copies of a deny list is.
main() {
  run_against "$CHART"
}

case "${1:-}" in
  --update)    update_golden ;;
  --self-test) self_test && main ;;
  *)           main ;;
esac
