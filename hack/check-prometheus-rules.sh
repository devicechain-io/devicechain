#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Parse every PrometheusRule the chart renders, with promtool.
#
# WHY: A PROMQL TYPO DISABLES THE WHOLE GROUP, SILENTLY
#
# Prometheus loads rule groups atomically. One unparseable expression and the
# ENTIRE group is rejected — every other alert in it included — and the only
# evidence is a line in the Prometheus server's log and a bump in
# `prometheus_rule_group_load_failures_total`. Nothing in the cluster turns red,
# `kubectl get prometheusrule` shows the object present and healthy, and the
# alerts simply never fire.
#
# That is the same failure mode as an alert with no series, reached by a
# different route, and this repo now ships five rule files: the DETECT/REACT
# rules, the JetStream replication rules (ADR-020 A0), the database backup rules
# (ADR-028, ADR-020 A2.5), the database control-plane rules (ADR-020 A1.5) and
# the command-delivery rules. A break in any one takes its neighbours with it.
#
# 🔴 THIS SCRIPT CANNOT SEE A MISSPELLED SERIES NAME. promtool parses PromQL; it
# has no idea whether `devicechain_commanddelivery_batch_refusals_total` is a
# metric this platform exports or a plausible-looking string nobody registers. A
# rule over a series that does not exist evaluates an empty vector, never fires,
# and passes every check in this file. hack/check-dashboards.sh is what closes
# that, by resolving every devicechain_* series named here back to the Go
# registration that creates it.
#
# Nothing else catches this. `helm lint` checks YAML, not PromQL. The values
# schema does not see rendered output. The Prometheus Operator's own admission
# webhook DOES validate rules — but it is optional, it is not installed on every
# cluster, and by the time it speaks the release is already being applied.
#
# 🔴 promtool must be able to FAIL for a pass to mean anything. Verified by
# mutation while this was written: an unbalanced label selector
# (`{namespace="dc-system" > 900`) produces
#   `parse error: unexpected character inside braces: '>'`
# and a non-zero exit. Note the first attempt at that control did NOT mutate the
# file — a sed against the pre-render text missed because the YAML dump quotes the
# expression differently — and promtool reported SUCCESS on the UNCHANGED rules,
# which reads exactly like a check that cannot fail. Confirm the mutation applied
# before believing the result.
#
# That mutation is no longer a thing somebody did once by hand: `--self-test`
# plants it, and nine other defects, EACH ALONE, and requires this file's own
# checking functions to reject every one of them. It carries the lesson above
# forward too — every planted mutation is grep-asserted before the verdict is
# believed, because a mutation that did not apply produces a SUCCESS that reads
# exactly like a check that cannot fail.
#
#   hack/check-prometheus-rules.sh              # check
#   hack/check-prometheus-rules.sh --self-test  # prove each check can fail, ALONE

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$repo_root/deploy/helm/devicechain"

say() { printf '\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '\033[0;37m    %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

# Reject anything unrecognised. A mistyped `--selftest` falling through to the
# real check would exit 0, so a CI step spelled that way would report a passing
# self-test that never ran — the check-that-cannot-fail shape, one level up.
case "${1:-}" in
  "" | --self-test) ;;
  *)
    echo "usage: $(basename "$0") [--self-test]" >&2
    exit 2
    ;;
esac

for tool in helm python3; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is required but not on PATH"
done
python3 -c 'import yaml' 2>/dev/null || fail "python3 needs PyYAML (pip install pyyaml)"

# promtool, from wherever it is. The container is the fallback rather than the
# default so a developer with a local Prometheus does not pay for a pull.
promtool_image="${PROMTOOL_IMAGE:-prom/prometheus:v3.5.0}"
if command -v promtool >/dev/null 2>&1; then
  run_promtool() { promtool check rules "$@"; }
else
  command -v docker >/dev/null 2>&1 ||
    fail "neither promtool nor docker is on PATH; this check cannot run and will not pretend it passed"

  # 🔴 OBTAINING THE TOOL IS A SEPARATE FAILURE FROM THE TOOL'S VERDICT, and the
  # two used to be the same message. Measured on PR #708: Docker Hub timed out,
  # the image never pulled, and this script reported
  #   "FAIL: promtool rejected a rule group."
  # about rules promtool had never seen.
  #
  # It fails CLOSED either way, so this was never a false pass. The damage is to
  # trust: a gate that blames your rules when a registry is slow teaches the next
  # maintainer that it is flaky, and "re-run it, probably docker again" is the
  # reflex that then gets applied to the run where a rule really is broken. That
  # is the same failure this whole script exists to prevent, one level up — a
  # result that does not mean what it says.
  #
  # So the pull happens HERE, once, with its own wording. Anything after this
  # point that exits non-zero is genuinely promtool speaking.
  if ! docker image inspect "$promtool_image" >/dev/null 2>&1; then
    say "pulling $promtool_image"
    docker pull "$promtool_image" >/dev/null 2>&1 ||
      fail "could not obtain promtool ($promtool_image) -- THE RULES WERE NOT CHECKED.

This is a failure to get the tool, not a verdict about the rules. The registry
may be unreachable or the tag may have moved. Re-run, or set PROMTOOL_IMAGE to
an image you already have."
  fi

  run_promtool() {
    # The mount is derived from the files' OWN directory rather than from the
    # script's work dir, so the self-test can point this at a synthetic bundle
    # and still be running the real invocation.
    local dir files mounted=() f
    dir="$(cd "$(dirname "$1")" && pwd)"
    files=("$@")
    for f in "${files[@]}"; do mounted+=("/w/$(basename "$f")"); done
    # -u 0 because the rendered files are written with the caller's umask and the
    # image's unprivileged user cannot necessarily read them.
    docker run --rm -u 0 -v "$dir:/w" --entrypoint /bin/promtool "$promtool_image" \
      check rules "${mounted[@]}"
  }
fi

work="$(mktemp -d)"
chmod 755 "$work"
trap 'rm -rf "$work"' EXIT

# ---------------------------------------------------------------------------
# The checking logic, as functions over PATHS. 🔑 Every stage takes its inputs as
# arguments rather than reaching for the work dir, which is what lets the
# self-test run the REAL logic over a bundle it built to be broken. A self-test
# that re-implements the check tests the re-implementation.
# ---------------------------------------------------------------------------

# extract_rules <rendered-manifest> <out-dir> <required-group>...
#
# Splits every PrometheusRule out of a rendered manifest into its own promtool
# rule file, and enforces the positive control.
extract_rules() {
  local rendered="$1" outdir="$2"
  shift 2
  python3 - "$rendered" "$outdir" "$@" <<'PY'
import os, sys, yaml

rendered, outdir = sys.argv[1], sys.argv[2]
required = set(sys.argv[3:])

names = []
for doc in yaml.safe_load_all(open(rendered)):
    if not doc or doc.get("kind") != "PrometheusRule":
        continue
    name = doc["metadata"]["name"]
    names.append(name)
    with open(os.path.join(outdir, "rule-%s.yaml" % name), "w") as f:
        yaml.safe_dump({"groups": doc["spec"]["groups"]}, f)

# 🔴 THE POSITIVE CONTROL. Everything downstream is a loop over whatever was
# rendered, so a chart that emitted no PrometheusRules at all -- a broken gate, a
# renamed value, a template that stopped being included -- would produce zero
# files, zero failures, and a green run. Requiring the rules we know we ship is
# what stops this passing by rendering nothing.
missing = required - set(names)
if missing:
    sys.exit(
        "these PrometheusRules were not rendered: %s.\n"
        "  Either the chart stopped emitting them or their gating values changed.\n"
        "  Both make this check vacuous, which is why it fails rather than passing quietly."
        % ", ".join(sorted(missing))
    )

print("\n".join(names))
PY
}

# collect_rule_files <dir> — prints one extracted rule file per line, and FAILS
# when there are none. "Nothing to check" must never be spelled the same way as
# "nothing wrong".
collect_rule_files() {
  local dir="$1" files=()
  mapfile -t files < <(find "$dir" -maxdepth 1 -name 'rule-*.yaml' | sort)
  if [[ ${#files[@]} -eq 0 ]]; then
    echo "no rule files were written; nothing would have been checked" >&2
    return 1
  fi
  printf '%s\n' "${files[@]}"
}

# check_group_accounting <dir> <tested-group>... -- <known-untested-group>...
#
# Every rendered group must be accounted for on one of the two lists, and every
# listed name must correspond to a group that actually rendered.
check_group_accounting() {
  python3 - "$@" <<'PY'
import os, sys

work = sys.argv[1]
split = sys.argv.index("--")
tested = set(sys.argv[2:split])
known_untested = set(sys.argv[split + 1:])

rendered = {
    f[len("rule-"):-len(".yaml")]
    for f in os.listdir(work)
    if f.startswith("rule-") and f.endswith(".yaml")
}

unaccounted = rendered - tested - known_untested
if unaccounted:
    sys.exit(
        "these rendered PrometheusRule groups are in neither the unit-tested set nor the\n"
        "  known-untested list: %s.\n"
        "  A group nobody listed is a group this check skips WITHOUT SAYING SO. Add it to\n"
        "  rule_tests with a test file, or to untested_groups to record it as a known gap."
        % ", ".join(sorted(unaccounted))
    )

phantom = (tested | known_untested) - rendered
if phantom:
    sys.exit(
        "these groups are listed here but the chart renders no such rule: %s.\n"
        "  A stale name means the tests it points at run against nothing, or that a group\n"
        "  was renamed and its coverage quietly followed the old name into the void."
        % ", ".join(sorted(phantom))
    )

print("    %d rendered group(s): %d unit-tested, %d recorded as untested"
      % (len(rendered), len(tested), len(known_untested)))
PY
}

# check_alert_coverage <group> <rendered-rule-file> <tests-file>
#
# 🔴 EVERY RULE MUST BE NAMED IN THE TESTS, and this is not tidiness.
#
# Found by mutation: re-introducing a staleness-threshold rule -- the exact
# false-positive-on-an-idle-cluster shape the current design exists to avoid --
# did NOT fail the unit tests. The "idle cluster raises nothing" scenario asserts
# `exp_alerts: []` for the three rules that existed when it was written, so a
# FOURTH rule firing on that same synthetic idle cluster was invisible.
#
# A negative assertion can only name rules its author knew about. This turns
# "the tests cover what I remembered" into "the tests cover every rule", so a new
# alert is untested loudly rather than silently.
check_alert_coverage() {
  python3 - "$1" "$2" "$3" <<'PY'
import os, sys, yaml

group, rendered, tests = sys.argv[1], sys.argv[2], sys.argv[3]

# A guard that cannot read its input must FAIL, not skip: an unreadable tests
# file would otherwise mean "no alert names found", which reads as full coverage
# of nothing.
for path in (rendered, tests):
    if not os.path.isfile(path):
        sys.exit("no such file: %s -- the %s coverage check read nothing, which is not a pass"
                 % (path, group))

defined = set()
for g in yaml.safe_load(open(rendered))["groups"]:
    for rule in g.get("rules", []):
        if "alert" in rule:
            defined.add(rule["alert"])

covered = set()
for case in yaml.safe_load(open(tests)).get("tests", []):
    for assertion in case.get("alert_rule_test", []):
        if "alertname" in assertion:
            covered.add(assertion["alertname"])

if not defined:
    sys.exit("the rendered %s group defines no alerts at all; this check would pass vacuously" % group)

untested = defined - covered
if untested:
    sys.exit(
        "these %s alerts appear in no unit test: %s.\n"
        "  Every rule needs at least one scenario that fires it AND at least one that does\n"
        "  not, including the idle-cluster case -- a rule nobody asserts about is a rule that\n"
        "  can be silently wrong in either direction. Add cases to %s."
        % (group, ", ".join(sorted(untested)), tests)
    )

stale = covered - defined
if stale:
    sys.exit(
        "these %s unit tests name alerts the chart no longer renders: %s.\n"
        "  Their assertions still pass -- an alert that does not exist fires no alerts, so\n"
        "  every `exp_alerts: []` is trivially satisfied. Remove them or fix the name."
        % (group, ", ".join(sorted(stale)))
    )

print("    %s: %d alert(s) defined, all covered by unit tests" % (group, len(defined)))
PY
}

# run_rule_tests <stage-dir> <rendered-rule-file> <tests-file>
#
# Each group runs in its OWN directory. The tests name their rule file as
# `rendered-rules.yaml`, relative to the test file's own directory, so two groups
# staged into one work dir would overwrite each other's rules and the second
# would silently be checked against the first's -- a green run proving nothing
# about either.
run_rule_tests() {
  local gwork="$1" rules="$2" tests="$3"
  mkdir -p "$gwork"
  cp "$tests" "$gwork/rules-tests.yaml"
  cp "$rules" "$gwork/rendered-rules.yaml"
  chmod 755 "$gwork"
  chmod 644 "$gwork/rules-tests.yaml" "$gwork/rendered-rules.yaml"

  if command -v promtool >/dev/null 2>&1; then
    (cd "$gwork" && promtool test rules rules-tests.yaml)
  else
    docker run --rm -u 0 -v "$gwork:/w" -w /w --entrypoint /bin/promtool "$promtool_image" \
      test rules rules-tests.yaml
  fi
}

# ---------------------------------------------------------------------------
# SELF-TEST. 🔴 EACH DEFECT IS PLANTED ALONE. A self-test that plants several at
# once is satisfied by a checker that finds any ONE of them and is blind to the
# rest — which is precisely the "green run proving nothing" shape this whole file
# exists to stop.
#
# 🔴 AND IT RUNS ON A SYNTHETIC BUNDLE, NOT ON A COPY OF THE REAL RENDER. Planting
# into the chart's own output would make every case read "the check failed" —
# including on the days the REPOSITORY is legitimately mid-change (a new rule file
# landing before its tests), when the self-test would report success for the wrong
# reason. A bundle this script writes itself is clean by construction, so "it
# failed" can only mean "it found what was planted".
#
# The cost is real and is named: a synthetic bundle proves the STAGES fire, not
# that the real invocation is pointed anywhere real. Cases 1 and 2 pin that from
# one side (a render missing a required group, and a render with no rules at all,
# both refused), and the real run below fails loudly from the other.
# ---------------------------------------------------------------------------

st_fail() { printf '\033[1;31mSELF-TEST FAILED: %s\033[0m\n' "$*" >&2; exit 1; }
st_ok() { printf '  ok: %s\n' "$*"; }

# st_bundle <dir> — a clean synthetic bundle: a rendered manifest carrying two
# PrometheusRules and one document that is NOT one (so the kind filter is
# exercised rather than assumed), plus unit tests covering every alert in the
# first. Rewritten from scratch per case, so no case inherits the last one's
# damage.
st_bundle() {
  local dir="$1"
  rm -rf "$dir"
  mkdir -p "$dir"
  chmod 755 "$dir"

  cat >"$dir/rendered.yaml" <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: selftest-not-a-rule
data:
  note: "a rendered document that is not a PrometheusRule"
---
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: selftest-alpha
spec:
  groups:
    - name: devicechain.selftest-alpha
      rules:
        - alert: SelfTestProbeFiring
          expr: dc_selftest_probe > 0
          for: 1m
          labels:
            severity: warning
          annotations:
            summary: "The self-test probe is above zero."
---
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: selftest-beta
spec:
  groups:
    - name: devicechain.selftest-beta
      rules:
        - alert: SelfTestBetaFiring
          expr: dc_selftest_beta > 0
          labels:
            severity: critical
EOF

  # Both directions, as the coverage check demands of the real tests: a state
  # that fires, and a state that must stay quiet.
  cat >"$dir/alpha-tests.yaml" <<'EOF'
rule_files:
  - rendered-rules.yaml
evaluation_interval: 1m
tests:
  - interval: 1m
    input_series:
      - series: dc_selftest_probe
        values: "1+0x5"
    alert_rule_test:
      - eval_time: 3m
        alertname: SelfTestProbeFiring
        exp_alerts:
          - exp_labels:
              severity: warning
            exp_annotations:
              summary: "The self-test probe is above zero."
  - interval: 1m
    input_series:
      - series: dc_selftest_probe
        values: "0+0x5"
    alert_rule_test:
      - eval_time: 3m
        alertname: SelfTestProbeFiring
        exp_alerts: []
EOF
}

self_test() {
  local st="$work/self-test" d
  mkdir -p "$st"

  # -------------------------------------------------------------------------
  # Case 0 — THE COUNTERWEIGHT, and it runs first. Every "the defect was caught"
  # below is satisfied just as well by a checker that fails everything, so a
  # bundle with nothing wrong with it has to come back clean through ALL FIVE
  # stages before any of them means anything.
  # -------------------------------------------------------------------------
  d="$st/clean"
  st_bundle "$d"
  extract_rules "$d/rendered.yaml" "$d" selftest-alpha selftest-beta >/dev/null ||
    st_fail "a clean synthetic render was rejected by the extractor"
  chmod 644 "$d"/rule-*.yaml
  collect_rule_files "$d" >/dev/null ||
    st_fail "a clean synthetic render produced no rule files"
  run_promtool "$d/rule-selftest-alpha.yaml" "$d/rule-selftest-beta.yaml" >/dev/null ||
    st_fail "promtool rejected clean synthetic rule groups"
  check_group_accounting "$d" selftest-alpha -- selftest-beta >/dev/null ||
    st_fail "a fully accounted-for set of groups was reported as unaccounted"
  check_alert_coverage selftest-alpha "$d/rule-selftest-alpha.yaml" "$d/alpha-tests.yaml" >/dev/null ||
    st_fail "fully covered alerts were reported as untested"
  run_rule_tests "$d/stage" "$d/rule-selftest-alpha.yaml" "$d/alpha-tests.yaml" >/dev/null ||
    st_fail "the clean synthetic rules failed their own unit tests"
  st_ok "a clean bundle passes all five stages, and a non-PrometheusRule document is ignored"

  # -------------------------------------------------------------------------
  # Case 1 — A REQUIRED GROUP STOPPED RENDERING, alone. The positive control:
  # this is the shape where a template quietly falls out of the chart and every
  # downstream loop then iterates over one fewer thing, in silence.
  # -------------------------------------------------------------------------
  d="$st/missing-required"
  st_bundle "$d"
  sed -i 's/^  name: selftest-alpha$/  name: selftest-renamed/' "$d/rendered.yaml"
  grep -q '^  name: selftest-renamed$' "$d/rendered.yaml" ||
    st_fail "the missing-required-group mutation did not apply"
  if extract_rules "$d/rendered.yaml" "$d" selftest-alpha selftest-beta >/dev/null 2>&1; then
    st_fail "did not flag a required PrometheusRule that was not rendered"
  fi
  st_ok "a required group that stopped rendering is caught"

  # -------------------------------------------------------------------------
  # Case 2 — A RENDER CARRYING NO PrometheusRules AT ALL, alone. Reached with an
  # EMPTY required set on purpose, so the positive control above cannot be what
  # catches it: this pins the separate guard that refuses to report on zero files.
  # -------------------------------------------------------------------------
  d="$st/no-rules"
  st_bundle "$d"
  printf 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: only-a-configmap\n' >"$d/rendered.yaml"
  extract_rules "$d/rendered.yaml" "$d" >/dev/null ||
    st_fail "the extractor failed for a reason other than the one under test"
  if collect_rule_files "$d" >/dev/null 2>&1; then
    st_fail "reported success with no rule files to check"
  fi
  st_ok "a render with no PrometheusRules is refused rather than passed"

  # -------------------------------------------------------------------------
  # Case 3 — UNPARSEABLE PromQL, alone. The mutation from the header comment, and
  # it is grep-asserted for the reason recorded there: a sed that misses produces
  # a promtool SUCCESS on unchanged rules, which reads exactly like a check that
  # cannot fail.
  # -------------------------------------------------------------------------
  d="$st/bad-promql"
  st_bundle "$d"
  extract_rules "$d/rendered.yaml" "$d" selftest-alpha selftest-beta >/dev/null ||
    st_fail "the extractor failed for a reason other than the one under test"
  chmod 644 "$d"/rule-*.yaml
  sed -i 's/dc_selftest_probe > 0/dc_selftest_probe{namespace="dc-system" > 900/' \
    "$d/rule-selftest-alpha.yaml"
  grep -q 'namespace="dc-system" > 900' "$d/rule-selftest-alpha.yaml" ||
    st_fail "the unparseable-PromQL mutation did not apply -- believe no verdict from this run"
  if run_promtool "$d/rule-selftest-alpha.yaml" >/dev/null 2>&1; then
    st_fail "promtool accepted an unbalanced label selector"
  fi
  st_ok "an unparseable expression is rejected by promtool"

  # -------------------------------------------------------------------------
  # Case 4 — A RENDERED GROUP ON NEITHER LIST, alone. The new-rule-file moment:
  # without this the accounting loop is a loop over whatever has tests.
  # -------------------------------------------------------------------------
  d="$st/unaccounted"
  st_bundle "$d"
  extract_rules "$d/rendered.yaml" "$d" selftest-alpha selftest-beta >/dev/null ||
    st_fail "the extractor failed for a reason other than the one under test"
  if check_group_accounting "$d" selftest-alpha -- >/dev/null 2>&1; then
    st_fail "did not flag a rendered group listed neither as tested nor as a known gap"
  fi
  st_ok "a rendered group accounted for nowhere is caught"

  # -------------------------------------------------------------------------
  # Case 5 — A LISTED GROUP THAT RENDERS NOTHING, alone. The same accounting from
  # the other end: coverage that quietly followed a renamed group into the void.
  # -------------------------------------------------------------------------
  if check_group_accounting "$d" selftest-alpha -- selftest-beta selftest-ghost >/dev/null 2>&1; then
    st_fail "did not flag a listed group the render does not produce"
  fi
  st_ok "a listed group that renders no such rule is caught"

  # -------------------------------------------------------------------------
  # Cases 6-9 exercise the coverage check over MINIMAL hand-written pairs rather
  # than the bundle, so each defect can be planted with the others genuinely
  # absent: mutating a name in the bundle's tests would trip BOTH the untested and
  # the stale arm at once, and either one firing would look like success.
  # -------------------------------------------------------------------------
  d="$st/coverage"
  mkdir -p "$d"

  # Case 6 — AN ALERT NAMED IN NO UNIT TEST, alone.
  cat >"$d/two-alerts.yaml" <<'EOF'
groups:
  - name: selftest
    rules:
      - alert: CoveredAlert
        expr: up > 0
      - alert: UncoveredAlert
        expr: up > 1
EOF
  cat >"$d/covers-one.yaml" <<'EOF'
tests:
  - alert_rule_test:
      - alertname: CoveredAlert
EOF
  if check_alert_coverage selftest "$d/two-alerts.yaml" "$d/covers-one.yaml" >/dev/null 2>&1; then
    st_fail "did not flag an alert that appears in no unit test"
  fi
  st_ok "an alert named in no unit test is caught"

  # Case 7 — A UNIT TEST NAMING AN ALERT THAT NO LONGER RENDERS, alone. Its
  # assertions still pass: an alert that does not exist fires nothing.
  cat >"$d/one-alert.yaml" <<'EOF'
groups:
  - name: selftest
    rules:
      - alert: CoveredAlert
        expr: up > 0
EOF
  cat >"$d/covers-a-ghost.yaml" <<'EOF'
tests:
  - alert_rule_test:
      - alertname: CoveredAlert
      - alertname: RemovedAlert
EOF
  if check_alert_coverage selftest "$d/one-alert.yaml" "$d/covers-a-ghost.yaml" >/dev/null 2>&1; then
    st_fail "did not flag a unit test naming an alert the chart no longer renders"
  fi
  st_ok "a stale unit test naming a removed alert is caught"

  # Case 8 — A GROUP WITH NO ALERTS AT ALL, alone. Recording rules only: every
  # set-difference below it is empty, so without this arm it passes vacuously.
  cat >"$d/no-alerts.yaml" <<'EOF'
groups:
  - name: selftest
    rules:
      - record: selftest:probe
        expr: up
EOF
  cat >"$d/empty-tests.yaml" <<'EOF'
tests: []
EOF
  if check_alert_coverage selftest "$d/no-alerts.yaml" "$d/empty-tests.yaml" >/dev/null 2>&1; then
    st_fail "did not flag a group that defines no alerts at all"
  fi
  st_ok "a group defining no alerts is refused rather than passed vacuously"

  # Case 9 — A MISSING UNIT-TEST FILE, alone. An unreadable tests file yields no
  # alert names, which is indistinguishable from full coverage unless it errors.
  #
  # 🔴 THIS ONE ASSERTS THE MESSAGE, NOT JUST THE EXIT STATUS, and that is the
  # difference between a control and a decoration. Measured by mutation: deleting
  # the explicit guard leaves the stage failing anyway — open() raises and python
  # exits non-zero — so an exit-status-only assertion SURVIVES the removal of the
  # thing it exists to pin, and would keep passing while the diagnosis degraded
  # into a stack trace nobody can act on.
  local out
  if out="$(check_alert_coverage selftest "$d/one-alert.yaml" "$d/does-not-exist.yaml" 2>&1)"; then
    st_fail "reported coverage against a unit-test file that does not exist"
  fi
  case "$out" in
  *"coverage check read nothing"*) ;;
  *) st_fail "a missing unit-test file failed, but not with a diagnosis naming it: $out" ;;
  esac
  st_ok "a missing unit-test file is refused with a diagnosis, not silent full coverage"

  # -------------------------------------------------------------------------
  # Case 10 — A SEMANTICALLY INVERTED RULE, alone. It parses, it renders, it is
  # fully covered by tests -- and it never fires on the state it exists to catch.
  # Everything above this line would pass it; only evaluating the expression does
  # not, which is why the unit tests exist at all.
  # -------------------------------------------------------------------------
  d="$st/inverted"
  st_bundle "$d"
  extract_rules "$d/rendered.yaml" "$d" selftest-alpha selftest-beta >/dev/null ||
    st_fail "the extractor failed for a reason other than the one under test"
  chmod 644 "$d"/rule-*.yaml
  sed -i 's/dc_selftest_probe > 0/dc_selftest_probe < 0/' "$d/rule-selftest-alpha.yaml"
  grep -q 'dc_selftest_probe < 0' "$d/rule-selftest-alpha.yaml" ||
    st_fail "the inverted-rule mutation did not apply -- believe no verdict from this run"
  check_alert_coverage selftest-alpha "$d/rule-selftest-alpha.yaml" "$d/alpha-tests.yaml" >/dev/null ||
    st_fail "the inverted rule was caught by the COVERAGE check, so case 10 proves nothing about evaluation"
  if run_rule_tests "$d/stage" "$d/rule-selftest-alpha.yaml" "$d/alpha-tests.yaml" >/dev/null 2>&1; then
    st_fail "a rule inverted so it can never fire still passed its own unit tests"
  fi
  st_ok "a rule that parses but can never fire is caught by the unit tests"

  echo "self-test passed: 10 defects, each planted alone, each caught; a clean bundle passes"
}

if [ "${1:-}" = "--self-test" ]; then
  self_test
  exit 0
fi

# ---------------------------------------------------------------------------
# The real check.
# ---------------------------------------------------------------------------
say "rendering the chart's PrometheusRules"

# The chart refuses to render without an instance root key (ADR-059). A throwaway
# one is correct here: nothing is deployed and the rules do not reference it.
root_key="$(head -c 32 /dev/urandom | base64)"

helm template dc "$chart" --set "instance.config.infrastructure.secrets.rootKey=$root_key" \
  >"$work/rendered.yaml" 2>"$work/render.err" ||
  fail "rendering the chart failed:$(printf '\n%s' "$(cat "$work/render.err")")"

# The rule files this repository knows it ships. Literal, not derived from what
# rendered: deriving it would restate the render's own output and assert nothing.
required_groups=(database-backup jetstream-replication database-control-plane command-delivery)

extract_rules "$work/rendered.yaml" "$work" "${required_groups[@]}" ||
  fail "the chart did not render the PrometheusRules this check requires"

rule_list="$(collect_rule_files "$work")" || fail "no rule files were written; nothing would have been checked"
mapfile -t rule_files <<<"$rule_list"

chmod 644 "${rule_files[@]}"

say "checking ${#rule_files[@]} PrometheusRule(s) with promtool"
run_promtool "${rule_files[@]}" || fail "promtool rejected a rule group.

A group Prometheus cannot parse is rejected ENTIRELY -- every alert in it, not
just the broken one -- and it fails in the server's log rather than anywhere a
cluster operator would see. The object stays present and healthy-looking and the
alerts never fire."

note "every rendered rule group parses"

# ---------------------------------------------------------------------------
# UNIT TESTS. Parsing is not meaning.
#
# `check rules` never evaluates an expression against a single sample, so a rule
# can parse perfectly and be semantically inverted. That is not hypothetical: the
# first version of the database-backup rules parsed, linted, rendered, and was
# GREEN on the exact failure it was written to catch -- a Cluster whose backup
# configuration had been deleted, where CloudNativePG's archive_command returns 0
# for every segment and pg_stat_archiver reports healthy archiving forever.
#
# The tests feed synthetic series for that state and for the healthy and idle
# states that must stay quiet.
# ---------------------------------------------------------------------------
# Which rendered group is exercised by which test file. Each pair runs on its own,
# against its own group -- NOT against a concatenation of all four, because these
# files encode one group's semantics each and an unrelated rule's labels showing up
# in the results would be a failure here for no reason.
declare -A rule_tests=(
  [database-backup]="$repo_root/hack/testdata/prometheus-rules-tests.yaml"
  [database-control-plane]="$repo_root/hack/testdata/prometheus-rules-control-plane-tests.yaml"
  [command-delivery]="$repo_root/hack/testdata/prometheus-rules-command-delivery-tests.yaml"
)

# 🔴 AND THE GROUPS THAT ARE KNOWINGLY UNTESTED, NAMED. Without this list the loop
# below is a loop over whatever has tests, so a NEW rule file -- the moment most
# likely to ship a semantically inverted alert -- would be skipped in silence and
# the run would still be green. That is the same silent-exemption shape the
# `required` positive control above exists to close, reached from the other end:
# there, a group that stopped rendering; here, a group that never got tests.
#
# Adding a rule file therefore forces a decision. Write tests for it, or write it
# down here as a known gap. Both are fine; drifting past the question is not.
untested_groups=(event-processing jetstream-replication)

check_group_accounting "$work" "${!rule_tests[@]}" -- "${untested_groups[@]}" ||
  fail "the rendered rule groups and the coverage lists in this script do not line up"

for group in "${!rule_tests[@]}"; do
  tests_file="${rule_tests[$group]}"

  check_alert_coverage "$group" "$work/rule-$group.yaml" "$tests_file" ||
    fail "the $group rule unit tests do not name every alert the chart renders"

  say "running the $group rule unit tests"
  run_rule_tests "$work/t-$group" "$work/rule-$group.yaml" "$tests_file" ||
    fail "a $group alerting rule does not behave as specified.

These tests are the only thing in the repository that evaluates an alert
expression. A failure here means a rule fires when it should not, or -- far worse
-- stays silent on a state this platform is supposed to catch."

  note "the $group rules fire on the states they claim to"
done
