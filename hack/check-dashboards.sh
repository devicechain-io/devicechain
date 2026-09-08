#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guard: the chart's Grafana dashboards must be REAL, must name REAL series, and
# must keep the ConfigMap names they already have.
#
# THREE GAPS, ALL OF THEM SILENT, NONE OF THEM COVERED BY ANYTHING ELSE.
#
# 1. NOTHING PARSES THE DASHBOARD JSON. templates/grafana-dashboard.yaml embeds
#    each file with `.Files.Get | indent 4`, which turns ANY bytes into a valid
#    YAML block scalar. A truncated file, a trailing comma, a stray backtick —
#    `helm lint` passes, `helm template` passes, the profile render passes,
#    `kubectl apply` passes, and the ConfigMap is created. The failure surfaces
#    in the Grafana sidecar's log at runtime, on somebody else's cluster, as a
#    dashboard that is simply not there. There is no local signal at all.
#
# 2. NOTHING CHECKS THAT A SERIES NAME EXISTS. A misspelled metric in a panel
#    draws an empty graph, which looks like a quiet system. A misspelled metric
#    in an ALERT is worse: `sum(rate(devicechain_..._typo_total[5m])) > 0` over
#    an empty vector is EMPTY, not false, so the rule never fires and never
#    complains. promtool's `check rules` cannot help — it parses PromQL and has
#    no idea which series this platform actually registers. That is a rule that
#    is indistinguishable, from every angle, from a platform that is healthy.
#
#    🔴 THE NAMES DO NOT EXIST ANYWHERE IN THE GO TREE AS WRITTEN, which is why
#    a plain grep for the full series name finds nothing and would make this
#    check vacuous. core.Microservice.NewCounter et al. assemble the exported
#    name from three parts:
#
#      devicechain _ <FunctionalArea with dashes removed> _ <registered name>
#
#    so `devicechain_commanddelivery_batch_refusals_total` is registered in Go
#    as the literal "batch_refusals_total" inside the command-delivery service,
#    and `command-delivery` becomes `commanddelivery` by
#    strings.ReplaceAll(area, "-", ""). This script inverts that: it splits the
#    subsystem off, checks it against the real functional areas on disk, and
#    looks for the remainder as a quoted Go string literal.
#
# 3. NOTHING PINS THE CONFIGMAP NAMES. The dashboard ConfigMap name is a
#    published interface — a Grafana sidecar has loaded it, and operators pin it
#    in their own tooling — and the chart's profile golden matches Deployments
#    only, so nothing in this repository would have noticed it changing. That
#    mattered the moment grafana-dashboard.yaml stopped hardcoding one filename
#    and became a glob: the glob had to reproduce `{instance}-event-processing-
#    dashboard` byte for byte, and "I checked by eye" is not a gate.
#
# 🔴 IF A TOOL IS MISSING THIS HARD FAILS. A checker that cannot parse must not
# report; "skipped because python3 was absent" and "no problems found" must
# never reach CI wearing the same green tick.
#
#   hack/check-dashboards.sh              # check
#   hack/check-dashboards.sh --self-test  # prove each check can fail, ALONE

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

say() { printf '\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '\033[0;37m    %s\033[0m\n' "$*"; }

for tool in python3 helm; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "FAIL: $tool is required but is not on PATH." >&2
    echo "This check cannot run and will not pretend it passed." >&2
    exit 1
  }
done

# ---------------------------------------------------------------------------
# THE PINNED CONFIGMAP NAMES. Literal, not derived from the dashboards on disk:
# deriving them would restate the template's own logic and assert nothing. A new
# dashboard therefore has to be added here deliberately, which is the point —
# that is the moment to notice a name is about to become an interface.
#
# `{instance}` is substituted with .Values.instance.id at check time.
# ---------------------------------------------------------------------------
EXPECTED_CONFIGMAPS=(
  "{instance}-command-delivery-dashboard"
  "{instance}-event-processing-dashboard"
)

# ---------------------------------------------------------------------------
# check_content <repo-root>
#
# Gaps 1 and 2, over a tree. Takes a root so the self-test can plant defects in
# a throwaway copy rather than in the repository — and so the self-test proves
# the PATTERNS fire, while the real invocation below proves it is pointed at the
# real tree.
# ---------------------------------------------------------------------------
check_content() {
  python3 - "$1" <<'PY'
import json, os, re, subprocess, sys

root = sys.argv[1]
chart = os.path.join(root, "deploy", "helm", "devicechain")
dashboard_dir = os.path.join(chart, "dashboards")
template_dir = os.path.join(chart, "templates")
services_dir = os.path.join(root, "backend", "services")
go_root = os.path.join(root, "backend")

problems = []

# --- gap 1: every dashboard must be JSON ------------------------------------
dashboards = sorted(
    os.path.join(dashboard_dir, f)
    for f in os.listdir(dashboard_dir)
    if f.endswith(".json")
) if os.path.isdir(dashboard_dir) else []

# A guard that silently narrows to nothing is the shape this whole file exists
# to prevent, so an empty dashboard directory is a failure rather than a pass.
if not dashboards:
    sys.exit("no dashboards found under %s -- this check would pass by looking at nothing" % dashboard_dir)

for path in dashboards:
    with open(path, "rb") as fh:
        raw = fh.read()
    try:
        json.loads(raw.decode("utf-8"))
    except (ValueError, UnicodeDecodeError) as exc:
        problems.append(
            "%s is not parseable JSON: %s\n"
            "    The chart embeds it with `.Files.Get | indent`, which accepts ANY bytes as a\n"
            "    YAML block scalar -- so helm lint, helm template and kubectl apply all pass and\n"
            "    the dashboard silently fails to load in the Grafana sidecar at runtime."
            % (os.path.relpath(path, root), exc)
        )

# --- gap 2: every devicechain_* series must be registered somewhere in Go ----

# Sources that name series: the dashboards, and the rendered-from-these rule
# templates. Both are REQUIRED to exist; a renamed directory must not turn this
# into a check over an empty list.
rule_files = sorted(
    os.path.join(template_dir, f)
    for f in os.listdir(template_dir)
    if f.startswith("prometheusrule") and f.endswith(".yaml")
) if os.path.isdir(template_dir) else []
if not rule_files:
    sys.exit("no prometheusrule templates found under %s -- half this check would be inert" % template_dir)

# 🔴 NOT EVERY devicechain_* TOKEN IS A SERIES, and the exceptions are NAMED
# rather than pattern-matched away. `devicechain_instance` is the alert label
# every rule attaches. Leaving it to a heuristic ("labels appear after a colon")
# would mean a genuinely misspelled subsystem could slip through the same hole.
ALLOWED_NON_SERIES = {"devicechain_instance"}

# The functional areas, read off disk. `strings.ReplaceAll(area, "-", "")` is
# exactly what core.Microservice does to build the metric subsystem, so this
# inverts the real transformation rather than a remembered one.
areas = {}
if not os.path.isdir(services_dir):
    sys.exit("no services under %s -- the subsystem check cannot run" % services_dir)
for name in os.listdir(services_dir):
    if os.path.isdir(os.path.join(services_dir, name)):
        areas[name.replace("-", "")] = name
if not areas:
    sys.exit("no functional areas discovered under %s" % services_dir)

# Every quoted lower-snake string literal in the Go tree, in one pass. A metric
# name reaches promauto as such a literal (`ms.NewCounter("batch_refusals_total", ...)`),
# so membership here is the closest a static check gets to "this series is
# registered".
try:
    out = subprocess.run(
        ["grep", "-rhoE", '"[a-z][a-z0-9_]*"', go_root, "--include=*.go"],
        capture_output=True, text=True, check=False,
    )
except OSError as exc:
    sys.exit("could not run grep over the Go tree: %s" % exc)
literals = {line.strip('"') for line in out.stdout.splitlines()}
if not literals:
    sys.exit(
        "found no Go string literals under %s.\n"
        "  Every series would resolve to 'missing' and this check would fail everything,\n"
        "  which is as broken as passing everything." % go_root
    )

series_re = re.compile(r"devicechain_[a-z0-9_]+")
seen = {}
for path in dashboards + rule_files:
    with open(path, "rb") as fh:
        text = fh.read().decode("utf-8", "replace")
    for match in series_re.findall(text):
        seen.setdefault(match, set()).add(os.path.relpath(path, root))

for series in sorted(seen):
    if series in ALLOWED_NON_SERIES:
        continue
    where = ", ".join(sorted(seen[series]))
    rest = series[len("devicechain_"):]
    subsystem, _, metric = rest.partition("_")
    if not metric:
        problems.append("%s (in %s) has no metric name after its subsystem" % (series, where))
        continue
    if subsystem not in areas:
        problems.append(
            "%s (in %s) names the subsystem %r, which is not a functional area.\n"
            "    The subsystem is the area directory with dashes removed; known: %s"
            % (series, where, subsystem, ", ".join(sorted(areas)))
        )
        continue
    if metric not in literals:
        problems.append(
            "%s (in %s) resolves to the metric %r in the %s area, and NO Go source registers\n"
            "    that name. A panel over a series that does not exist draws an empty graph; an\n"
            "    ALERT over one evaluates an empty vector, which is not false -- it never fires\n"
            "    and never complains."
            % (series, where, metric, areas[subsystem])
        )

# --- gap 3: an alert's comparison must bind to the WHOLE expression ----------
#
# 🔴 THIS CLASS SHIPPED, PAST REVIEW, AND WOULD HAVE PAGED EVERY OPERATOR.
# `sum(rate(x)) or vector(0) > 0` reads as "the rate, defaulting to zero, above
# zero". It is not. `>` binds TIGHTER than `or`, so it parses as
# `sum(rate(x)) or (vector(0) > 0)`: the right side filters the sample 0 by
# `> 0` and is EMPTY, leaving a bare `sum(rate(x))` with no comparison at all.
# An alert fires on ANY sample its expression returns, whatever the value, and a
# plain Counter is exported at 0 from construction -- so the series always
# exists, rate() always returns a sample, and the alert fires forever.
#
# promtool cannot help: the expression is valid PromQL and means exactly what it
# says. Neither can gap 2 -- every series in it is spelled correctly. The defect
# is entirely in the precedence, which is why it needs its own check.
#
# Two shapes are caught, and the second is the general case of the first:
#   1. `or vector(N)` immediately followed by a comparison -- the comparison has
#      bound to the vector() instead of to the expression.
#   2. an alert whose expr contains no comparison operator at all.
alert_re = re.compile(r"-\s*alert:\s*(\S+)(.*?)(?=\n\s*-\s*alert:|\Z)", re.S)
expr_re = re.compile(r"\n\s*expr:\s*\|?(.*?)(?=\n\s*(?:for|labels|annotations|record):)", re.S)
# `or vector(0) > 0` -- a comparison that binds to the vector() rather than to
# the whole expression. `(... or vector(0)) > 0` does not match: the next
# non-space character there is the closing paren.
unbound_re = re.compile(r"\bor\s+vector\([^)]*\)\s*(?:<|>|==|!=)")
comparison_re = re.compile(r"<=|>=|<|>|==|!=")
# SOME PromQL CONSTRUCTS ARE THEMSELVES THE PREDICATE, and an alert built on one
# is correct with no comparison anywhere. Demanding a comparison would push an
# author into bolting a meaningless `> 0` onto a rule that is already right.
#   absent() / absent_over_time()  -- a sample only when the series is MISSING
#   X unless Y                     -- a sample only for an X with no matching Y
# Both were found by this check's first draft flagging real, correct rules
# (CNPGClusterStateUnobserved and PostgresWALArchivingNotConfigured). The list is
# an allowlist of predicates rather than a loosening: anything NOT on it still
# has to say what it is comparing against.
absence_re = re.compile(r"\babsent(?:_over_time)?\s*\(|\bunless\b")
alerts_checked = 0
for path in rule_files:
    with open(path, "rb") as fh:
        text = fh.read().decode("utf-8", "replace")
    rel = os.path.relpath(path, root)
    for name, body in alert_re.findall(text):
        match = expr_re.search(body)
        if not match:
            continue
        alerts_checked += 1
        # Strip comment lines: they quote the broken form on purpose.
        expr = "\n".join(
            line for line in match.group(1).splitlines()
            if not line.lstrip().startswith("#")
        )
        if unbound_re.search(expr):
            problems.append(
                "%s (in %s) writes `or vector(...)` immediately before its comparison.\n"
                "    `>` binds tighter than `or`, so the comparison applies to the vector()\n"
                "    and not to the expression -- what is left is an alert with NO comparison,\n"
                "    which fires permanently. Parenthesise it: `(... or vector(0)) > 0`."
                % (name, rel)
            )
        elif not comparison_re.search(expr) and not absence_re.search(expr):
            problems.append(
                "%s (in %s) has an expr with no comparison and no absence/unless\n"
                "    predicate, so every sample it returns is an alert. If it is meant to fire on\n"
                "    any sample at all, say so explicitly rather than by omission."
                % (name, rel)
            )
if not alerts_checked:
    sys.exit(
        "no alert expressions were found in %d rule template(s) -- gap 3 would be inert"
        % len(rule_files)
    )

if problems:
    print("The chart's dashboards and alert rules do not hold up:\n", file=sys.stderr)
    for p in problems:
        print("  - %s" % p, file=sys.stderr)
    sys.exit(1)

print("    %d dashboard(s) parse; %d devicechain_* series all resolve to a Go registration;\n"
      "    %d alert expression(s) compare against something"
      % (len(dashboards), len([s for s in seen if s not in ALLOWED_NON_SERIES]), alerts_checked))
PY
}

# ---------------------------------------------------------------------------
# check_configmaps <chart-dir>
#
# Gap 3. Renders the chart and compares the dashboard ConfigMaps it produces
# against EXPECTED_CONFIGMAPS. Asserts the set both ways: a renamed ConfigMap
# fails, and so does a dashboard that ships without being pinned here.
# ---------------------------------------------------------------------------
check_configmaps() {
  local chart="$1" out
  out="$(mktemp)"
  # 🔑 The render goes to a FILE, not down a pipe. `python3 - <<'PY'` reads its
  # PROGRAM from stdin, so piping the YAML in as well hands the interpreter the
  # heredoc and leaves sys.stdin.read() empty — which does not error, it just
  # finds no ConfigMaps and reports every pinned name as MISSING. Caught by the
  # self-test's clean-chart case, which is the whole reason that case exists.
  #
  # Render-only, never leaves this script: any profile carrying a secret-store
  # area refuses to render without an instance root key.
  if ! helm template dc "$chart" \
    --set "instance.config.infrastructure.secrets.rootKey=$(openssl rand -base64 32)" >"$out"; then
    rm -f "$out"
    echo "FAIL: rendering $chart failed, so nothing was checked" >&2
    return 1
  fi

  local rc=0
  python3 - "$chart" "$out" "${EXPECTED_CONFIGMAPS[@]}" <<'PY' || rc=$?
import re, subprocess, sys

chart = sys.argv[1]
rendered = open(sys.argv[2], encoding="utf-8").read()
expected_patterns = sys.argv[3:]

instance = subprocess.run(
    ["helm", "show", "values", chart], capture_output=True, text=True, check=True
).stdout
m = re.search(r"^instance:\s*$.*?^  id:\s*(\S+)\s*$", instance, re.M | re.S)
if not m:
    sys.exit("could not read instance.id out of the chart's values")
expected = {p.replace("{instance}", m.group(1)) for p in expected_patterns}

# The dashboard ConfigMaps, identified by the sidecar label rather than by a
# name pattern -- matching on the name would make this assertion circular.
found = set()
for doc in rendered.split("\n---"):
    if re.search(r"^kind: ConfigMap\s*$", doc, re.M) and re.search(r'^\s+grafana_dashboard: "1"\s*$', doc, re.M):
        name = re.search(r"^\s+name:\s*(\S+)\s*$", doc, re.M)
        if name:
            found.add(name.group(1))

if found == expected:
    print("    %d Grafana dashboard ConfigMap(s), all named as pinned" % len(found))
    sys.exit(0)

print("the Grafana dashboard ConfigMap names are not what this repository pins.\n", file=sys.stderr)
for name in sorted(expected - found):
    print("  MISSING: %s" % name, file=sys.stderr)
for name in sorted(found - expected):
    print("  UNPINNED: %s" % name, file=sys.stderr)
print("""
A dashboard ConfigMap name is a published interface: a Grafana sidecar has
already loaded it and operators pin it in their own tooling, so renaming one
orphans the dashboard on every existing install with nothing to say so.

If a name genuinely has to change, or a new dashboard is being added, update
EXPECTED_CONFIGMAPS in hack/check-dashboards.sh in the same commit.""", file=sys.stderr)
sys.exit(1)
PY
  rm -f "$out"
  return "$rc"
}

# ---------------------------------------------------------------------------
# Self-test. 🔴 EACH DEFECT IS PLANTED ALONE. A self-test that plants several at
# once is satisfied by a checker that finds any ONE of them and is blind to the
# rest — which is precisely the "green run proving nothing" shape these checks
# exist to stop.
#
# 🔴 AND THE CONTENT CASES RUN ON A SYNTHETIC TREE, NOT ON A COPY OF THIS ONE,
# which was a deliberate change of design. Planting into a copy of the real tree
# makes every case read "check_content failed" — and check_content also fails
# whenever the REPOSITORY has a dangling series, which is an ordinary state
# mid-branch while a dashboard lands before the Go metrics it names. The
# self-test would then report success for the wrong reason on exactly the days
# it matters most. A tree this script builds itself is clean by construction, so
# "it failed" can only mean "it found what was planted".
#
# The cost is real and is covered elsewhere: a synthetic tree proves the
# PATTERNS fire, not that the real invocation looks anywhere real. The empty-
# directory case below pins the second half from one side, and the real run
# fails loudly ("no dashboards found under ...") from the other.
# ---------------------------------------------------------------------------
if [ "${1:-}" = "--self-test" ]; then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  fail() { echo "SELF-TEST FAILED: $1" >&2; exit 1; }

  work="$tmp/tree"
  dash="$work/deploy/helm/devicechain/dashboards"
  tmpl="$work/deploy/helm/devicechain/templates"
  # `self-test-area` rather than a single word on purpose: the dash is what
  # core.Microservice strips to build the metric subsystem, so this exercises
  # the inversion rather than assuming areas never contain one.
  gosrc="$work/backend/services/self-test-area"
  mkdir -p "$dash" "$tmpl" "$gosrc"

  cat >"$gosrc/metrics.go" <<'EOF'
package selftestarea

func register(ms *Microservice) {
	ms.NewCounter("probe_total", "A probe counter.", nil)
	ms.NewCounter("other_probe_total", "A second probe counter.", nil)
}
EOF

  # The pristine fixtures, kept aside so each case can restore what it mutated
  # and leave the next one planting its defect ALONE.
  cat >"$tmp/probe.json" <<'EOF'
{
  "title": "Self-test probe",
  "uid": "dc-self-test-probe",
  "panels": [
    {
      "type": "timeseries",
      "targets": [
        {
          "expr": "sum(rate(devicechain_selftestarea_probe_total{namespace=\"$namespace\"}[5m]))"
        }
      ]
    }
  ]
}
EOF
  cat >"$tmp/prometheusrule-probe.yaml" <<'EOF'
spec:
  groups:
    - name: devicechain.self-test-area
      rules:
        - alert: ProbeFiring
          expr: sum(rate(devicechain_selftestarea_other_probe_total{namespace="dc"}[5m])) > 0
          labels:
            devicechain_instance: dc
EOF
  restore() {
    cp "$tmp/probe.json" "$dash/probe.json"
    cp "$tmp/prometheusrule-probe.yaml" "$tmpl/prometheusrule-probe.yaml"
  }
  restore

  # Case 0 — THE COUNTERWEIGHT, and it runs first. Every "the defect was caught"
  # below is satisfied just as well by a checker that fails everything, so a
  # tree with nothing wrong with it has to come back clean before any of them
  # means anything. It also proves devicechain_instance — a LABEL, not a series
  # — does not trip the resolver.
  check_content "$work" >/dev/null || fail "a clean synthetic tree was reported as broken"
  echo "  ok: a clean tree passes, and the devicechain_instance label is not mistaken for a series"

  # Case 1 — MALFORMED JSON, alone. Truncation rather than a syntax typo,
  # because truncation is what a bad merge or a half-written file produces and
  # it is the shape most likely to look fine in a diff.
  head -c 60 "$tmp/probe.json" >"$dash/probe.json"
  if check_content "$work" >/dev/null 2>&1; then
    fail "did not flag a truncated dashboard JSON file"
  fi
  restore
  echo "  ok: malformed dashboard JSON is caught"

  # Case 2 — A MISSPELLED SERIES IN A DASHBOARD, alone. One character, in a
  # name that reads perfectly.
  sed -i 's/devicechain_selftestarea_probe_total/devicechain_selftestarea_probe_totl/' "$dash/probe.json"
  grep -q 'devicechain_selftestarea_probe_totl' "$dash/probe.json" ||
    fail "the dashboard series mutation did not apply"
  if check_content "$work" >/dev/null 2>&1; then
    fail "did not flag a misspelled series name in a dashboard"
  fi
  restore
  echo "  ok: a misspelled series in a dashboard is caught"

  # Case 3 — A MISSPELLED SERIES IN AN ALERT RULE, alone. Same defect, the far
  # more dangerous half: this one is a rule that can never fire, and promtool
  # parses it perfectly.
  sed -i 's/other_probe_total/othr_probe_total/' "$tmpl/prometheusrule-probe.yaml"
  grep -q 'othr_probe_total' "$tmpl/prometheusrule-probe.yaml" ||
    fail "the alert-rule series mutation did not apply"
  if check_content "$work" >/dev/null 2>&1; then
    fail "did not flag a misspelled series name in a prometheusrule template"
  fi
  restore
  echo "  ok: a misspelled series in an alert rule is caught"

  # Case 4 — A SERIES UNDER A SUBSYSTEM THAT IS NOT A FUNCTIONAL AREA, alone.
  # The other half of the resolution: a metric name that exists, attributed to
  # a service that does not.
  sed -i 's/devicechain_selftestarea_probe_total/devicechain_slftestarea_probe_total/' "$dash/probe.json"
  grep -q 'devicechain_slftestarea_probe_total' "$dash/probe.json" ||
    fail "the subsystem mutation did not apply"
  if check_content "$work" >/dev/null 2>&1; then
    fail "did not flag a series naming an unknown functional area"
  fi
  restore
  echo "  ok: a series under an unknown functional area is caught"

  # Case 5 — AN EMPTY DASHBOARD DIRECTORY MUST BE AN ERROR, not a quiet pass.
  # This is the case that separates "the check is clean" from "the check looked
  # at nothing" — and the only one here that speaks to the real invocation, by
  # pinning that a directory with nothing in it cannot come back green.
  rm -f "$dash/probe.json"
  if check_content "$work" >/dev/null 2>&1; then
    fail "reported success with no dashboards to check"
  fi
  restore
  echo "  ok: an empty dashboard directory is refused"

  # -------------------------------------------------------------------------
  # The ConfigMap-name cases DO run against a copy of the real chart, and can:
  # they depend on the chart alone, not on the Go tree, so their clean baseline
  # is not hostage to a branch mid-landing.
  # -------------------------------------------------------------------------
  chart="$tmp/chart"
  cp -r "$ROOT/deploy/helm/devicechain" "$chart"

  # Case 6 — A RENAMED CONFIGMAP, alone. The exact regression the glob rewrite
  # could have introduced: everything renders, everything parses, every series
  # is real, and an existing install's dashboard is orphaned with nothing to say
  # so.
  check_configmaps "$chart" >/dev/null ||
    fail "the untouched chart's ConfigMap names did not match the pinned set"
  cp "$chart/templates/grafana-dashboard.yaml" "$tmp/keep-tmpl.yaml"
  sed -i 's/-dashboard$/-grafana-dashboard/' "$chart/templates/grafana-dashboard.yaml"
  grep -q -- '-grafana-dashboard' "$chart/templates/grafana-dashboard.yaml" ||
    fail "the ConfigMap-name mutation did not apply"
  if check_configmaps "$chart" >/dev/null 2>&1; then
    fail "did not flag a renamed dashboard ConfigMap"
  fi
  cp "$tmp/keep-tmpl.yaml" "$chart/templates/grafana-dashboard.yaml"
  echo "  ok: a renamed dashboard ConfigMap is caught"

  # Case 7 — AN UNPINNED NEW DASHBOARD, alone. The set is asserted both ways, so
  # adding a dashboard without recording its ConfigMap name here fails too —
  # which is what makes the pinned list a decision rather than a formality.
  printf '{"title": "scratch", "uid": "scratch", "panels": []}\n' >"$chart/dashboards/scratch-board.json"
  if check_configmaps "$chart" >/dev/null 2>&1; then
    fail "did not flag a dashboard whose ConfigMap name is not pinned"
  fi
  rm -f "$chart/dashboards/scratch-board.json"
  echo "  ok: an unpinned new dashboard is caught"

  # Case 8 — THE PRECEDENCE DEFECT, alone. This is the real one: it shipped past
  # review into a release window. Every series in it is spelled correctly, so
  # gap 2 passes it, and it is valid PromQL, so promtool passes it. The mutation
  # is the exact text that was written -- `or vector(0) > 0` with no parentheses
  # binding the comparison to the whole expression.
  sed -i 's/\[5m\])) > 0/[5m])) or vector(0) > 0/' "$tmpl/prometheusrule-probe.yaml"
  grep -q 'or vector(0) > 0' "$tmpl/prometheusrule-probe.yaml" ||
    fail "the precedence mutation did not apply"
  if check_content "$work" >/dev/null 2>&1; then
    fail "did not flag an alert whose comparison binds to vector() instead of the expression"
  fi
  restore
  echo "  ok: an alert comparing against vector() instead of its expression is caught"

  # Case 9 — THE GENERAL CASE, alone: an expr with no comparison at all, which
  # is what the defect in case 8 DEGRADES INTO once PromQL has parsed it. Worth
  # planting separately, because a checker could catch the literal text of case
  # 8 by pattern and still miss an alert that simply forgot its threshold.
  sed -i 's/\[5m\])) > 0/[5m]))/' "$tmpl/prometheusrule-probe.yaml"
  grep -q '5m\]))$' "$tmpl/prometheusrule-probe.yaml" ||
    fail "the missing-comparison mutation did not apply"
  if check_content "$work" >/dev/null 2>&1; then
    fail "did not flag an alert expression with no comparison operator"
  fi
  restore
  echo "  ok: an alert with no comparison at all is caught"

  # Case 10 — THE COUNTERWEIGHT FOR GAP 3, and it matters as much as case 0.
  # The CORRECT parenthesisation must still pass, or the check would be pushing
  # authors away from the very idiom the sibling alert needs: without
  # `or vector(0)`, an alert summing several services goes ABSENT rather than
  # false the moment one of them stops being scraped.
  sed -i 's/\[5m\])) > 0/[5m])) or vector(0)) > 0/; s/expr: sum(/expr: (sum(/' "$tmpl/prometheusrule-probe.yaml"
  grep -q 'or vector(0)) > 0' "$tmpl/prometheusrule-probe.yaml" ||
    fail "the correct-parenthesisation mutation did not apply"
  check_content "$work" >/dev/null ||
    fail "flagged a CORRECTLY parenthesised or-vector(0) -- the check rejects the right idiom"
  restore
  echo "  ok: a correctly parenthesised or-vector(0) still passes"

  # Case 11 — THE SECOND COUNTERWEIGHT FOR GAP 3. An `absent()` alert is correct
  # with no comparison: absent() returns a sample only when the series is
  # MISSING, so it already is the test. The first draft of this check flagged a
  # real one (CNPGClusterStateUnobserved), which is exactly how a well-meant gate
  # teaches an author to bolt a meaningless `> 0` onto a correct rule.
  sed -i 's|expr: sum(rate(devicechain_selftestarea_other_probe_total{namespace="dc"}\[5m\])) > 0|expr: absent(devicechain_selftestarea_other_probe_total{namespace="dc"})|' "$tmpl/prometheusrule-probe.yaml"
  grep -q 'expr: absent(' "$tmpl/prometheusrule-probe.yaml" ||
    fail "the absent() mutation did not apply"
  check_content "$work" >/dev/null ||
    fail "flagged an absent() alert, which is correct with no comparison"
  restore
  echo "  ok: an absent() alert with no comparison still passes"

  echo "self-test passed: 9 defects, each planted alone, each caught; three clean trees pass"
  exit 0
fi

say "parsing the chart's Grafana dashboards and resolving their series"
check_content "$ROOT"

say "checking the dashboard ConfigMap names against the pinned set"
check_configmaps "$ROOT/deploy/helm/devicechain"

note "dashboards parse, every series they and the alert rules name is registered, names are pinned"
