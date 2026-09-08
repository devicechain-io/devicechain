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
# and if it lives in a second template file, `--show-only` hid it from both the gate and
# the server-side dry-run. (An earlier version of this note added the profile-workloads
# check to that list; it never used --show-only and only ever pinned Deployment names, so
# it was never looking.) Every one of
# those renders 28 agreeing prefixes, and every one is valid Kubernetes, so a dry-run
# accepts them too.
#
# Hence the golden: the prefixes are compared BY VALUE against Go, and the rest of the
# object is pinned so that changing it is a diff a human accepts on purpose. The golden
# holds the prefixes as well. That is deliberate duplication — it means a deny-table change
# shows up twice in one commit, which is the right number of times for an edit to a
# security boundary.
#
# 🔴 SAY WHAT THE GOLDEN DOES NOT COVER. Every previous version of this header replaced a
# false coverage claim with a subtler one, four rounds running, so this states the shape of
# the gap rather than claiming a boundary.
#
# A GOLDEN PINS A RENDER. Anything that does not appear in the render it was made from is
# invisible to it, and there are three ways for that to happen:
#
#   - a VALUES branch that emits nothing under the rendered values. Two combinations are
#     rendered (the defaults, and the defaults plus additionalAllowedCidrs). Replacing that
#     block's body with an allow-all rule used to pass everything.
#   - a GUARD, which emits nothing when it does not fire. assert_guards feeds each
#     operator-reachable one the input it exists to refuse and requires the render to fail;
#     two guards are unreachable from values and are defensive against a template edit.
#   - a RENDER-CONTEXT branch — lookup, .Capabilities, .Release.Is* — which differs between
#     `helm template` and `helm install`. assert_no_render_context_branches refuses those
#     outright, because a gate that renders cannot check them by rendering.
#
# A fourth way would need a fourth check. The honest response to finding one is another
# check here, not a firmer sentence.
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

# Render the WHOLE chart with the policy enabled, plus any extra --set arguments.
#
# 🔴 NOT `--show-only templates/networkpolicy.yaml`. That flag is why a policy added in a
# second template file was invisible to every instrument at once. The documents are
# selected by KIND, from the parsed stream, downstream of here.
#
# 🔴 `--include-crds` is not decoration either. `helm install` applies everything in a
# chart's `crds/` directory and does NOT check what kind those files declare, while
# `helm template` skips the directory entirely unless asked. So a NetworkPolicy dropped
# into `crds/` would be created by a real install, survive `helm uninstall`, and be
# invisible here — the same "a policy added in a new file" hole in a different spelling.
# Verified on a real cluster. The chart has no `crds/` today; this keeps it that way by
# making one visible rather than by trusting that nobody adds it.
render_chart() {
  local chart_dir="$1" key
  key="$(openssl rand -base64 32)"
  shift
  helm template "$chart_dir" \
    --include-crds \
    --set "instance.config.infrastructure.secrets.rootKey=$key" \
    --set "enabledFunctionalAreas=$AREAS" \
    --set networkPolicy.enabled=true \
    "$@"
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
  local dir="$1"; shift
  render_chart "$dir" "$@" | ( cd "$CORE_MODULE" && go run ./egress/internal/policyranges ) | sort
}

chart_policies_from() {
  local dir="$1"; shift
  render_chart "$dir" "$@" | ( cd "$CORE_MODULE" && go run ./egress/internal/policyranges -policies )
}

# The golden's exact bytes, header included, for a given chart directory.
#
# Both --update and the comparison go through this one function, so the file on disk and
# the file compared against it are produced identically. Writing the header in one place
# and stripping it in the other is how a golden acquires a diff that is not a change.
#
# 🔴 TWO RENDERS, BECAUSE A GOLDEN PINS THE COMBINATION IT WAS RENDERED WITH AND NOTHING
# ELSE. The template has a values-conditional block — `{{- with $np.additionalAllowedCidrs }}`
# — and under default values that block emits nothing, so its BODY was pinned by nothing:
# replacing it with an allow-all rule left the prefixes agreeing, the golden identical and
# all four self-test arms green, and opened every tenant destination the moment an operator
# set the value. That block is the operator-facing feature and therefore the one most
# likely to be edited. The second render sets it to a documentation range so the body is
# rendered and pinned.
#
# The limit that remains, stated rather than implied: this pins TWO combinations. A branch
# reachable only under some third set of values is still pinned by nothing, and the honest
# fix for a third branch is a third render here.
golden_body() {
  echo "# The rendered egress NetworkPolicy. Generated by hack/check-egress-ranges.sh --update."
  echo "#"
  echo "# Pinned so that a change to the policy AROUND the deny lists — a selector, policyTypes,"
  echo "# a peer, an added rule, or a whole second policy document — is a diff somebody accepts on"
  echo "# purpose. Comparing the prefix lists says nothing about whether the object enforces"
  echo "# anything at all: policyTypes: [Ingress] leaves the prefixes identical and makes every"
  echo "# egress rule inert."
  echo "#"
  echo "# TWO renders: the default values, then the same with networkPolicy.additionalAllowedCidrs"
  echo "# set, because that block emits nothing by default and would otherwise be pinned by nothing."
  echo "# ---------------------------------------------------------------- default values"
  chart_policies_from "$1"
  echo "# ------------------------------------------- with additionalAllowedCidrs set"
  chart_policies_from "$1" --set "networkPolicy.additionalAllowedCidrs={198.51.100.0/24}"
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

# assert_guards requires each render-time guard that an OPERATOR CAN TRIP to actually trip.
#
# 🔴 THIS EXISTS BECAUSE THE GOLDEN CANNOT SEE A GUARD. A `{{ fail }}` block emits nothing
# when it does not fire, so under the values the golden is rendered with, every one of them
# is invisible — deleting the dnsNamespaceSelector guard leaves the gate green, the golden
# identical and all four self-test arms passing. It is not hypothetical which operator
# reaches it: the chart README tells you to clear the shipped label key when you replace a
# namespace selector, and clearing it WITHOUT adding your own is one slip away. That renders
# `namespaceSelector: {}` on the port-53 rule, which matches every namespace in the cluster.
#
# So the guards are checked the only way a guard can be: by feeding it the input it exists
# to refuse and requiring the render to fail. Two of the five cannot be reached from values
# at all — the deny lists live in the template body now — so they are defensive against a
# template edit and are deliberately not listed here.
# assert_no_render_context_branches refuses template constructs whose value differs between
# `helm template` and `helm install`.
#
# 🔴 THE GOLDEN IS A RENDER, SO ANYTHING THAT RENDERS DIFFERENTLY UNDER INSTALL IS INVISIBLE
# TO IT. Measured: under `helm template` a chart sees `lookup` returning empty,
# `.Capabilities.APIVersions` carrying only the built-in list, and `.Release.IsUpgrade`
# false; under `helm install --dry-run=server` on a real cluster all three change. So a
# second policy gated on `.Release.IsUpgrade` is absent from every render this gate makes,
# present on the upgrade the rig performs, and inspected by nothing.
#
# This is a LEXICAL check, and that is the right shape only because it is a PROHIBITION
# rather than a comparison: the claim is "these constructs do not appear in this template",
# which is a question about the text. It would be the wrong shape for asking what the
# template MEANS — which is why everything else here parses a render instead.
assert_no_render_context_branches() {
  local chart_dir="${1:-$CHART}" hits
  # 🔴 EVERY TEMPLATE, AND BARE WORDS. The first version grepped networkpolicy.yaml with a
  # pattern requiring `.Release.Is` / `.Capabilities.` / `lookup ` — and the finding it was
  # written for was "a SECOND policy gated on .Release.IsUpgrade", which lives in a second
  # FILE. It also missed `{{ with .Release }}{{ if .IsUpgrade }}`, an aliased
  # `$c := .Capabilities`, `.Release.Revision` (1 on install, >1 on upgrade), and a `lookup`
  # whose arguments are on the next line. And _helpers.tpl is in scope for a third reason:
  # the policy `include`s devicechain.areaLabels, so a render-context branch THERE rewrites
  # the podSelector and the policy selects no pods on every upgrade.
  #
  # Bare words over the whole directory have zero false positives today — no template in
  # this chart uses .Release, .Capabilities or lookup at all — which is what makes the
  # blunt pattern affordable.
  hits="$(grep -rnE '\.Release\b|\.Capabilities\b|\blookup\b' \
    "$chart_dir/templates/" || true)"
  if [ -n "$hits" ]; then
    echo "The egress policy template branches on render CONTEXT, which the golden cannot see:" >&2
    echo "$hits" | sed 's/^/  /' >&2
    echo >&2
    echo "lookup, .Capabilities and .Release.Is* all differ between 'helm template' (what this" >&2
    echo "gate and the CI dry-run render) and 'helm install' (what an operator actually gets)," >&2
    echo "so a rule behind one of them is pinned by nothing and validated by nothing." >&2
    return 1
  fi
}

assert_guards() {
  local chart_dir="${1:-$CHART}" ok=0
  # 🔴 THE ORACLE IS THE GUARD'S OWN MESSAGE, NOT "THE RENDER FAILED". Those are not the
  # same claim, and the difference is reachable: `networkPolicy` has
  # additionalProperties:false in values.schema.json, so a TYPO in this function's own
  # --set key makes helm reject the values, the render fails, and a "did it fail?" oracle
  # reports the guard fired — even with the guard deleted. Reproduced. A check whose
  # oracle is "something went wrong" confirms itself.
  _guard_refuses() {
    local what="$1" expect="$2"; shift 2
    local out
    if out="$(render_chart "$chart_dir" "$@" 2>&1)"; then
      echo "the $what guard did NOT fire: the chart rendered with input it is supposed to" >&2
      echo "refuse, so an operator making that mistake gets a policy instead of a message." >&2
      ok=1
    elif ! printf '%s' "$out" | grep -qF "$expect"; then
      echo "the render failed for the $what case, but NOT with that guard's message." >&2
      echo "Expected to find: $expect" >&2
      printf '%s\n' "$out" | tail -5 | sed 's/^/  /' >&2
      echo "A guard check whose oracle is merely 'the render failed' passes when the chart" >&2
      echo "is broken in any unrelated way — including a typo in this check's own --set." >&2
      ok=1
    fi
  }
  # An empty selector matches EVERY namespace, so this one is the difference between a DNS
  # rule and a hole.
  _guard_refuses "empty dnsNamespaceSelector" "networkPolicy.dnsNamespaceSelector is empty" \
    --set 'networkPolicy.dnsNamespaceSelector.kubernetes\.io/metadata\.name=null'
  _guard_refuses "empty infrastructureNamespaceSelector" \
    "networkPolicy.infrastructureNamespaceSelector is empty" \
    --set 'networkPolicy.infrastructureNamespaceSelector.devicechain\.io/component=null'
  # With the area absent the policy selects no pods and protects nothing.
  _guard_refuses "outbound-connectors not enabled" "outbound-connectors is not among the enabled" \
    --set 'enabledFunctionalAreas={user-management,device-management}'
  return "$ok"
}

compare() {
  local go_file="$1" chart_file="$2" policy_file="$3" chart_dir="${4:-$CHART}"

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

  if ! assert_guards "$chart_dir"; then
    return 1
  fi
  if ! assert_no_render_context_branches "$chart_dir"; then
    return 1
  fi

  local excluded
  excluded="$(go_excluded | tr '\n' ' ')"
  echo "==> egress ranges agree ($n_go prefixes, compared per address family)"
  if [ -n "$excluded" ]; then
    echo "    not comparable, excluded by address form: ${excluded% }"
  fi
  echo "==> the rendered policy matches the golden ($(grep -c '^---$' "$policy_file") document(s))"
  echo "==> every operator-reachable render guard still refuses its input, and the template"
  echo "    branches on no render context the golden could not see"
}

# A tampered copy of the chart. Copying the real chart and editing the copy means the arms
# below exercise the real render path — the template, the parser and the comparison —
# rather than a substituted function that only proves diff works.
# 🔴 The caller records the directory, not this function. It is always invoked as
# `dir="$(tampered_chart …)"`, and a command substitution is a SUBSHELL — an array
# appended to in here is appended to a copy that dies with it, so the cleanup list stayed
# empty on every run and each self-test leaked three chart copies. The previous version of
# this file had that bug AND a comment confidently diagnosing a different one.
# 🔴 The copy is laid out at its REAL repo-relative path — $dir/deploy/helm/devicechain —
# rather than at $dir/chart, so that pointing either $ROOT or $CHART at the fake tree finds
# the tampered chart. Arm 4 needs both: an extractor that went circular by re-rendering
# "$ROOT/deploy/helm/devicechain" ignores $CHART entirely, and with a $dir/chart layout it
# would have kept reading the real chart and passed.
tampered_chart() {
  local sed_expr="$1" file="$2" dir
  dir="$(mktemp -d)"
  mkdir -p "$dir/deploy/helm"
  cp -r "$CHART" "$dir/deploy/helm/devicechain"
  sed -i "$sed_expr" "$dir/deploy/helm/devicechain/$file"
  echo "$dir/deploy/helm/devicechain"
}

# record_tamper appends to the cleanup list from the CALLER's shell.
record_tamper() {
  TAMPER_DIRS+=("${1%/deploy/helm/devicechain}")
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
  gather "$g" "$c" "$p" "$chart_dir" "$drop" && compare "$g" "$c" "$p" "$chart_dir" || rc=$?
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
  # — which, from a RETURN trap, became the status of the whole self-test: every arm
  # printed and passed and the script still exited 1. Same shape as the sweep loop whose
  # status is its last echo. (The array being empty was a second bug, in tampered_chart;
  # this guard is still right, because a run with no tampering has nothing to clean.)
  return 0
}

self_test() {
  local dir
  trap cleanup_tampered RETURN

  # Arm 1: one prefix removed from the chart's deny table. The everyday drift.
  dir="$(tampered_chart '/^    "172.16.0.0\/12"$/d' templates/networkpolicy.yaml)"; record_tamper "$dir"
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
  dir="$(tampered_chart 's/^    - Egress$/    - Ingress/' templates/networkpolicy.yaml)"; record_tamper "$dir"
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
  #
  # 🔴 BOTH PATH VARIABLES ARE TAMPERED, and that is the correction of a real miss. An
  # earlier version pointed only $CHART at the fake tree, so an extractor that had gone
  # circular by rendering "$ROOT/deploy/helm/devicechain" ignored it and passed every arm
  # while the Go table was compared against nothing. The copy now sits at that same
  # relative path, so either variable leads to the tampered chart.
  #
  # The residual, stated rather than implied, and wider than "an absolute path": ANY path
  # not read from $ROOT or $CHART at call time evades this — including ones derived from the
  # script's own variables, like "$(dirname "${BASH_SOURCE[0]}")/../deploy/helm/devicechain"
  # or "$CORE_MODULE/../../deploy/helm/devicechain". Those are deliberate edits visible in a
  # diff, like the `main` limit in the header; the arms bound accidents, not authorship.
  local go_real go_tampered
  go_real="$(go_ranges | md5sum)"
  dir="$(tampered_chart '/^    "172.16.0.0\/12"$/d' templates/networkpolicy.yaml)"; record_tamper "$dir"
  go_tampered="$(ROOT="${dir%/deploy/helm/devicechain}" CHART="$dir" go_ranges | md5sum)"
  if [ "$go_real" != "$go_tampered" ]; then
    echo "SELF-TEST FAILED: the Go extraction CHANGED when only the chart was tampered," >&2
    echo "so the two sides are not independent and the comparison is circular." >&2
    return 1
  fi
  echo "==> self-test: the Go extraction does not move when the chart does"

  # Arm 5: a render GUARD deleted. 🔴 Without this arm, CI's --self-test proved the four
  # arms above could fail and NOTHING about assert_guards — which is the check standing
  # between an operator's one-key slip and a port-53 rule matching every namespace.
  dir="$(tampered_chart '/if not $np.dnsNamespaceSelector/,+2d' templates/networkpolicy.yaml)"; record_tamper "$dir"
  if render_chart "$dir" --set 'networkPolicy.dnsNamespaceSelector.kubernetes\.io/metadata\.name=null' \
       >/dev/null 2>&1; then
    : # the tamper took: the chart now renders input the guard should have refused
  else
    echo "SELF-TEST FAILED: deleting the DNS guard did not make that input renderable," >&2
    echo "so this arm is not exercising what it claims." >&2
    return 1
  fi
  if run_against "$dir" > /dev/null 2>&1; then
    echo "SELF-TEST FAILED: a deleted render guard was not detected" >&2
    return 1
  fi
  echo "==> self-test: a deleted render guard is detected"

  # Arm 6: a render-context branch introduced in ANOTHER template — which is where the
  # finding that motivated the tripwire actually lived.
  dir="$(tampered_chart '1i {{/* {{ if .Release.IsUpgrade }} */}}' templates/_helpers.tpl)"; record_tamper "$dir"
  if ! grep -q 'Release.IsUpgrade' "$dir/templates/_helpers.tpl"; then
    echo "SELF-TEST FAILED: the render-context tamper did not take effect" >&2
    return 1
  fi
  if run_against "$dir" > /dev/null 2>&1; then
    echo "SELF-TEST FAILED: a render-context branch in a second template was not detected" >&2
    return 1
  fi
  echo "==> self-test: a render-context branch in any template is detected"
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
