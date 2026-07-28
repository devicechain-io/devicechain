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
# different route, and this repo now ships three rule files: the DETECT/REACT
# rules, the JetStream replication rules (ADR-020 A0), and the database backup
# rules (ADR-028, ADR-020 A2.5). A break in any one takes its neighbours with it.
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

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$repo_root/deploy/helm/devicechain"

say() { printf '\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '\033[0;37m    %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

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
  run_promtool() {
    local files=("$@") mounted=()
    for f in "${files[@]}"; do mounted+=("/w/$(basename "$f")"); done
    # -u 0 because the rendered files are written with the caller's umask and the
    # image's unprivileged user cannot necessarily read them.
    docker run --rm -u 0 -v "$work:/w" --entrypoint /bin/promtool "$promtool_image" \
      check rules "${mounted[@]}"
  }
fi

work="$(mktemp -d)"
chmod 755 "$work"
trap 'rm -rf "$work"' EXIT

say "rendering the chart's PrometheusRules"

# The chart refuses to render without an instance root key (ADR-059). A throwaway
# one is correct here: nothing is deployed and the rules do not reference it.
root_key="$(head -c 32 /dev/urandom | base64)"

helm template dc "$chart" --set "instance.config.infrastructure.secrets.rootKey=$root_key" \
  >"$work/rendered.yaml" 2>"$work/render.err" ||
  fail "rendering the chart failed:$(printf '\n%s' "$(cat "$work/render.err")")"

python3 - "$work" <<'PY'
import os, sys, yaml

work = sys.argv[1]
names = []
for doc in yaml.safe_load_all(open(os.path.join(work, "rendered.yaml"))):
    if not doc or doc.get("kind") != "PrometheusRule":
        continue
    name = doc["metadata"]["name"]
    names.append(name)
    with open(os.path.join(work, "rule-%s.yaml" % name), "w") as f:
        yaml.safe_dump({"groups": doc["spec"]["groups"]}, f)

# 🔴 THE POSITIVE CONTROL. Everything downstream is a loop over whatever was
# rendered, so a chart that emitted no PrometheusRules at all -- a broken gate, a
# renamed value, a template that stopped being included -- would produce zero
# files, zero failures, and a green run. Requiring the rules we know we ship is
# what stops this passing by rendering nothing.
required = {"database-backup", "jetstream-replication"}
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

mapfile -t rule_files < <(find "$work" -name 'rule-*.yaml' | sort)
[[ ${#rule_files[@]} -gt 0 ]] || fail "no rule files were written; nothing would have been checked"

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
tests_file="$repo_root/hack/testdata/prometheus-rules-tests.yaml"
[[ -f "$tests_file" ]] || fail "no rule unit tests at $tests_file"

# The tests name their rule file as `rendered-rules.yaml`, relative to the test
# file's own directory. Both are staged into the work dir so the tests run against
# the SHIPPED rules rather than a checked-in copy that can drift from them.
#
# 🔴 The database-backup group specifically, not every group: these tests encode
# that group's semantics, and pointing them at a concatenation of all three would
# make an unrelated rule's labels a failure here.
cp "$tests_file" "$work/rules-tests.yaml"
cp "$work/rule-database-backup.yaml" "$work/rendered-rules.yaml"
chmod 644 "$work/rules-tests.yaml" "$work/rendered-rules.yaml"

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
python3 - "$work/rule-database-backup.yaml" "$tests_file" <<'PY'
import sys, yaml

rendered, tests = sys.argv[1], sys.argv[2]

defined = set()
for group in yaml.safe_load(open(rendered))["groups"]:
    for rule in group.get("rules", []):
        if "alert" in rule:
            defined.add(rule["alert"])

covered = set()
for case in yaml.safe_load(open(tests)).get("tests", []):
    for assertion in case.get("alert_rule_test", []):
        if "alertname" in assertion:
            covered.add(assertion["alertname"])

if not defined:
    sys.exit("the rendered database-backup group defines no alerts at all; this check would pass vacuously")

untested = defined - covered
if untested:
    sys.exit(
        "these database-backup alerts appear in no unit test: %s.\n"
        "  Every rule needs at least one scenario that fires it AND at least one that does\n"
        "  not, including the idle-cluster case -- a rule nobody asserts about is a rule that\n"
        "  can be silently wrong in either direction. Add cases to %s."
        % (", ".join(sorted(untested)), tests)
    )

stale = covered - defined
if stale:
    sys.exit(
        "these unit tests name alerts the chart no longer renders: %s.\n"
        "  Their assertions still pass -- an alert that does not exist fires no alerts, so\n"
        "  every `exp_alerts: []` is trivially satisfied. Remove them or fix the name."
        % ", ".join(sorted(stale))
    )

print("    %d alert(s) defined, all covered by unit tests" % len(defined))
PY

say "running the database-backup rule unit tests"
if command -v promtool >/dev/null 2>&1; then
  (cd "$work" && promtool test rules rules-tests.yaml)
else
  docker run --rm -u 0 -v "$work:/w" -w /w --entrypoint /bin/promtool "$promtool_image" \
    test rules rules-tests.yaml
fi || fail "a database-backup alerting rule does not behave as specified.

These tests are the only thing in the repository that evaluates an alert
expression. A failure here means a rule fires when it should not, or -- far worse
-- stays silent on a state that means this instance is not being backed up."

note "the database-backup rules fire on the states they claim to"
