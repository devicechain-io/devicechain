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
