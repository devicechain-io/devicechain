#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guard: the chart's Grafana dashboards must be REAL, must name REAL series,
# must keep the ConfigMap names they already have, and must keep two instances'
# boards apart.
#
# FOUR GAPS, ALL OF THEM SILENT, NONE OF THEM COVERED BY ANYTHING ELSE.
#
# 1. NOTHING PARSED THE DASHBOARD JSON. templates/grafana-dashboard.yaml used to
#    embed each file with `.Files.Get | indent 4`, which turns ANY bytes into a
#    valid YAML block scalar. A truncated file, a trailing comma, a stray
#    backtick — `helm lint` passed, `helm template` passed, `kubectl apply`
#    passed, and the failure surfaced in the Grafana sidecar's log at runtime, on
#    somebody else's cluster, as a dashboard that was simply not there. The
#    template now parses each file (`mustFromJson`) to scope it per instance, so
#    a render fails too — but the parse here stays, because it is what reports
#    WHICH file and why, without a Helm render in the way.
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
# 4. NOTHING KEPT TWO INSTANCES' BOARDS APART. The Grafana sidecar is
#    cluster-wide, so two instances rendering the same data key and uid were one
#    file and one board, and deleting either took the other's board away
#    (measured; templates/grafana-dashboard.yaml has the account). A
#    single-instance render cannot see that: every value is unique when there is
#    one of it. So this renders the chart for TWO instances — one of them at the longest id
#    the chart's schema accepts, because Grafana refuses a uid over 40 characters
#    — and fails on a duplicate uid or data key across the pair, a uid too long,
#    a board whose `namespace` variable is not a hidden constant naming its own
#    instance, or a ConfigMap without that instance's `grafana_folder`.
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

for tool in python3 helm openssl; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "FAIL: $tool is required but is not on PATH." >&2
    echo "This check cannot run and will not pretend it passed." >&2
    exit 1
  }
done
python3 -c 'import yaml' 2>/dev/null || {
  echo "FAIL: python3 cannot import yaml (PyYAML), which reads the rendered ConfigMaps." >&2
  echo "This check cannot run and will not pretend it passed." >&2
  exit 1
}

# ---------------------------------------------------------------------------
# THE PINNED CONFIGMAP NAMES AND DATA KEYS. Literal, not derived from the
# dashboards on disk: deriving them would restate the template's own logic and
# assert nothing. A new dashboard therefore has to be added here deliberately,
# which is the point — that is the moment to notice a name is about to become an
# interface.
#
# The DATA KEY is the second published name: it is the file the Grafana sidecar
# writes, so it has to carry the instance or two instances share one file.
#
# `{instance}` is substituted with the instance id each render uses.
# ---------------------------------------------------------------------------
EXPECTED_CONFIGMAPS=(
  "{instance}-command-delivery-dashboard"
  "{instance}-event-processing-dashboard"
)
EXPECTED_DATA_KEYS=(
  "{instance}-command-delivery.json"
  "{instance}-event-processing.json"
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
            "    The chart parses it to scope it per instance, so every render of the chart\n"
            "    fails on this file."
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

# 🔴 THE RESOLVER TRUNCATES, SO IT HAS TO CHECK WHAT IT TRUNCATED. series_re stops at
# the first character a metric name cannot contain, which means a series written with a
# hyphen is not reported as unknown — it is silently shortened to its legal prefix and
# that prefix is then resolved. `devicechain_devicemanagement_resolve-loop_inflight`
# becomes `devicechain_devicemanagement_resolve`, whose metric part `resolve` IS a
# registered Go literal (every processing loop passes its name as one), so the check
# goes green over an alert naming a series that does not exist.
#
# A hyphen is not legal in a Prometheus metric name at all, so any such token is a
# defect wherever it appears — reported here rather than resolved.
ILLEGAL_IN_NAME = "-."
TOKEN_TAIL = re.compile(r"[A-Za-z0-9_.:-]*")

seen = {}
for path in dashboards + rule_files:
    with open(path, "rb") as fh:
        text = fh.read().decode("utf-8", "replace")
    for match in series_re.finditer(text):
        if text[match.end():match.end() + 1] in ILLEGAL_IN_NAME:
            token = match.group(0) + TOKEN_TAIL.match(text, match.end()).group(0)
            problems.append(
                "%s (in %s) contains a character that cannot appear in a Prometheus metric\n"
                "    name, which must match [a-zA-Z_:][a-zA-Z0-9_:]*. No series is exported under\n"
                "    that name, and the surrounding selector cannot reference it by identifier."
                % (token, os.path.relpath(path, root))
            )
            continue
        seen.setdefault(match.group(0), set()).add(os.path.relpath(path, root))

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

if problems:
    print("The chart's dashboards and alert rules do not hold up:\n", file=sys.stderr)
    for p in problems:
        print("  - %s" % p, file=sys.stderr)
    sys.exit(1)

print("    %d dashboard(s) parse; %d devicechain_* series all resolve to a Go registration"
      % (len(dashboards), len([s for s in seen if s not in ALLOWED_NON_SERIES])))
PY
}

# ---------------------------------------------------------------------------
# check_configmaps <chart-dir>
#
# Gaps 3 and 4. Renders the chart for two instances and checks the dashboard
# ConfigMaps both produce. The pinned sets are asserted both ways, per instance:
# a renamed ConfigMap or data key fails, and so does a dashboard that ships
# without being pinned here. Every problem found is reported, each under its own
# code, so one render says everything that is wrong with it.
# ---------------------------------------------------------------------------
check_configmaps() {
  local chart="$1" dir rc=0 id
  dir="$(mktemp -d)"

  # The two instances. The second is exactly as long as the chart's schema
  # allows, read from the schema rather than restated, so the render exercises
  # the longest id a real install can carry whatever that limit becomes.
  local short_id="dashcheck-a" long_id
  if ! long_id="$(python3 - "$chart/values.schema.json" <<'PY'
import json, sys
spec = json.load(open(sys.argv[1]))["properties"]["instance"]["properties"]["id"]
n = spec["maxLength"]
prefix = "dashcheck-b-"
print((prefix + "x" * n)[:n])
PY
  )"; then
    rm -rf "$dir"
    echo "FAIL: could not read the instance id's maxLength from $chart/values.schema.json" >&2
    return 1
  fi

  # 🔑 The renders go to FILES, not down a pipe. `python3 - <<'PY'` reads its
  # PROGRAM from stdin, so piping the YAML in as well hands the interpreter the
  # heredoc and leaves sys.stdin.read() empty — which does not error, it just
  # finds no ConfigMaps and reports every pinned name as MISSING. Caught by the
  # self-test's clean-chart case, which is the whole reason that case exists.
  #
  # Render-only, never leaves this script: any profile carrying a secret-store
  # area refuses to render without an instance root key.
  for id in "$short_id" "$long_id"; do
    if ! helm template dc "$chart" --set "instance.id=$id" \
      --set "instance.config.infrastructure.secrets.rootKey=$(openssl rand -base64 32)" >"$dir/$id.yaml"; then
      rm -rf "$dir"
      echo "FAIL: rendering $chart for instance $id failed, so nothing was checked" >&2
      return 1
    fi
  done

  EXPECTED_NAMES="$(printf '%s\n' "${EXPECTED_CONFIGMAPS[@]}")" \
    EXPECTED_KEYS="$(printf '%s\n' "${EXPECTED_DATA_KEYS[@]}")" \
    python3 - "$dir" "$short_id" "$long_id" <<'PY' || rc=$?
import json, os, sys
import yaml  # presence already enforced at the top of the script

render_dir, ids = sys.argv[1], sys.argv[2:]
names = [p for p in os.environ["EXPECTED_NAMES"].split("\n") if p]
keys = [p for p in os.environ["EXPECTED_KEYS"].split("\n") if p]
if not names or not keys:
    sys.exit("no pinned ConfigMap names or data keys were passed -- this check would pass over nothing")

# Grafana's own limit on a dashboard uid.
UID_MAX = 40

problems = []
uids = {}      # uid -> [(instance, key)]
all_keys = {}  # data key -> [instance]
boards = 0

for inst in ids:
    folder = "devicechain-%s" % inst
    found_names, found_keys = set(), set()
    with open(os.path.join(render_dir, inst + ".yaml"), encoding="utf-8") as fh:
        docs = [d for d in yaml.safe_load_all(fh) if d]
    for doc in docs:
        # The dashboard ConfigMaps, identified by the sidecar label rather than by
        # a name pattern -- matching on the name would make this circular.
        meta = doc.get("metadata") or {}
        if doc.get("kind") != "ConfigMap" or (meta.get("labels") or {}).get("grafana_dashboard") != "1":
            continue
        name = meta.get("name")
        found_names.add(name)
        got_folder = (meta.get("annotations") or {}).get("grafana_folder")
        if got_folder != folder:
            problems.append(
                "FOLDER: ConfigMap %s carries grafana_folder=%r, want %r.\n"
                "    The sidecar files a board under that annotation; without this instance's own\n"
                "    value its boards land in a folder shared with every other instance."
                % (name, got_folder, folder))
        for key, body in (doc.get("data") or {}).items():
            found_keys.add(key)
            all_keys.setdefault(key, []).append(inst)
            boards += 1
            where = "%s (instance %s)" % (key, inst)
            try:
                board = json.loads(body)
            except ValueError as exc:
                problems.append("JSON: %s does not parse as rendered: %s" % (where, exc))
                continue
            uid = board.get("uid")
            uids.setdefault(uid, []).append(where)
            if not isinstance(uid, str) or not uid:
                problems.append("UID-MISSING: %s has no uid" % where)
            elif len(uid) > UID_MAX:
                problems.append(
                    "UID-TOO-LONG: %s has uid %r, %d characters. Grafana refuses a uid over %d,\n"
                    "    so this board would never load."
                    % (where, uid, len(uid), UID_MAX))
            ns = [v for v in (board.get("templating") or {}).get("list") or []
                  if isinstance(v, dict) and v.get("name") == "namespace"]
            ok = (len(ns) == 1 and ns[0].get("type") == "constant" and ns[0].get("hide") == 2
                  and ns[0].get("query") == inst
                  and (ns[0].get("current") or {}).get("value") == inst)
            if not ok:
                problems.append(
                    "NAMESPACE: %s does not scope itself to its instance. Want exactly one\n"
                    "    `namespace` template variable, a hidden constant (type constant, hide 2) whose\n"
                    "    query and current value are %r; found %s.\n"
                    "    Every panel filters namespace=\"$namespace\", so this is what decides which\n"
                    "    instance's series the board reads."
                    % (where, inst, json.dumps(ns)))

    want_names = {p.replace("{instance}", inst) for p in names}
    want_keys = {p.replace("{instance}", inst) for p in keys}
    for n in sorted(want_names - found_names):
        problems.append("NAME-MISSING: %s (instance %s)" % (n, inst))
    for n in sorted(found_names - want_names):
        problems.append("NAME-UNPINNED: %s (instance %s)" % (n, inst))
    for k in sorted(want_keys - found_keys):
        problems.append("KEY-MISSING: %s (instance %s)" % (k, inst))
    for k in sorted(found_keys - want_keys):
        problems.append("KEY-UNPINNED: %s (instance %s)" % (k, inst))

for uid, where in sorted(uids.items(), key=lambda kv: str(kv[0])):
    if len(where) > 1:
        problems.append(
            "DUPLICATE-UID: %r is the uid of %s.\n"
            "    Grafana holds one board per uid, so these are one board: whichever file the\n"
            "    sidecar loads last wins, and removing either removes it."
            % (uid, ", ".join(where)))
for key, insts in sorted(all_keys.items()):
    if len(insts) > 1:
        problems.append(
            "DUPLICATE-KEY: data key %s is rendered by instances %s.\n"
            "    The sidecar writes every data key to one directory, so these are one file:\n"
            "    deleting either instance's ConfigMap deletes the other's board."
            % (key, ", ".join(insts)))

if not problems:
    print("    %d dashboard ConfigMap(s) across 2 instances (ids of %s characters): names and\n"
          "    data keys as pinned, every uid and file distinct, every board scoped and foldered"
          % (boards, " and ".join(str(len(i)) for i in ids)))
    sys.exit(0)

print("the rendered Grafana dashboard ConfigMaps do not hold up:\n", file=sys.stderr)
for p in problems:
    print("  - %s" % p, file=sys.stderr)
if any(p.startswith(("NAME-", "KEY-")) for p in problems):
    print("""
A dashboard ConfigMap name and its data key are published interfaces: a Grafana
sidecar has already loaded them and operators pin the names in their own tooling,
so renaming one orphans the dashboard on every existing install with nothing to
say so.

If one genuinely has to change, or a new dashboard is being added, update
EXPECTED_CONFIGMAPS / EXPECTED_DATA_KEYS in hack/check-dashboards.sh in the same
commit.""", file=sys.stderr)
sys.exit(1)
PY
  rm -rf "$dir"
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
	ms.NewCounter("probe_total", "A probe counter.")
	ms.NewCounter("other_probe_total", "A second probe counter.")
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

  # Case 6 — A SERIES CARRYING A CHARACTER A METRIC NAME CANNOT CONTAIN, alone.
  # 🔴 THIS ONE PASSED THE RESOLVER BEFORE IT WAS TAUGHT TO LOOK. The series
  # pattern stops at the hyphen, so the name is not reported as unknown — it is
  # shortened to `devicechain_selftestarea_probe_total`, which resolves
  # perfectly, and the rule naming a series that is not exported reads green.
  # The mutation is on the ALERT half deliberately: an alert over a series that
  # does not exist evaluates an empty vector, which never fires.
  sed -i 's/other_probe_total/probe_total-seconds/' "$tmpl/prometheusrule-probe.yaml"
  grep -q 'probe_total-seconds' "$tmpl/prometheusrule-probe.yaml" ||
    fail "the illegal-character mutation did not apply"
  if check_content "$work" >/dev/null 2>&1; then
    fail "did not flag a series containing a character illegal in a metric name"
  fi
  restore
  echo "  ok: a series carrying a character illegal in a metric name is caught"

  # -------------------------------------------------------------------------
  # The ConfigMap cases DO run against a copy of the real chart, and can: they
  # depend on the chart alone, not on the Go tree, so their clean baseline is not
  # hostage to a branch mid-landing.
  #
  # 🔴 EACH OF THESE ALSO NAMES THE PROBLEM IT EXPECTS. The render is checked as
  # a whole and several of its checks overlap — a data key that stops carrying
  # the instance is both unpinned AND duplicated — so "it failed" alone could be
  # any check firing. A case passes only when the checker exits non-zero AND
  # reports the planted problem's code.
  # -------------------------------------------------------------------------
  chart="$tmp/chart"
  cp -r "$ROOT/deploy/helm/devicechain" "$chart"
  tpl="$chart/templates/grafana-dashboard.yaml"
  cp "$tpl" "$tmp/keep-tmpl.yaml"

  # plant <file> <old> <new> — exactly one occurrence, or the case is void.
  plant() {
    python3 - "$1" "$2" "$3" <<'PY' || fail "a mutation did not apply: $2"
import sys
path, old, new = sys.argv[1:]
text = open(path, encoding="utf-8").read()
if text.count(old) != 1:
    sys.exit(1)
open(path, "w", encoding="utf-8").write(text.replace(old, new))
PY
  }
  # expect_configmaps <code> <description> — the case's verdict.
  expect_configmaps() {
    local out rc=0
    out="$(check_configmaps "$chart" 2>&1)" || rc=$?
    [ "$rc" -ne 0 ] || fail "did not flag $2"
    grep -q -- "- $1:" <<<"$out" || {
      echo "$out" >&2
      fail "flagged something while planting $2, but not as $1"
    }
    cp "$tmp/keep-tmpl.yaml" "$tpl"
    echo "  ok: $2 is caught ($1)"
  }

  # Case 7 — THE COUNTERWEIGHT for everything below: the untouched chart passes.
  check_configmaps "$chart" >/dev/null ||
    fail "the untouched chart's dashboard ConfigMaps did not pass"
  echo "  ok: the untouched chart's dashboard ConfigMaps pass across two instances"

  # Case 8 — A RENAMED CONFIGMAP, alone. The exact regression the glob rewrite
  # could have introduced: everything renders, everything parses, every series
  # is real, and an existing install's dashboard is orphaned with nothing to say
  # so.
  plant "$tpl" '{{ $stem }}-dashboard' '{{ $stem }}-grafana-dashboard'
  expect_configmaps NAME-UNPINNED "a renamed dashboard ConfigMap"

  # Case 9 — AN UNPINNED NEW DASHBOARD, alone. The set is asserted both ways, so
  # adding a dashboard without recording its name here fails too — which is what
  # makes the pinned list a decision rather than a formality. The board carries
  # a `namespace` variable because the template refuses to render one without.
  printf '%s\n' '{"title": "scratch", "uid": "scratch", "panels": [], "templating": {"list": [{"name": "namespace", "type": "constant", "query": ""}]}}' \
    >"$chart/dashboards/scratch-board.json"
  expect_configmaps NAME-UNPINNED "a dashboard whose ConfigMap name is not pinned"
  rm -f "$chart/dashboards/scratch-board.json"

  # Case 10 — ONE UID FOR EVERY INSTANCE, alone. Today's-shape regression on the
  # uid only: every file is still distinct, so nothing but the cross-instance
  # comparison can see that Grafana will hold one board for both.
  plant "$tpl" '(printf "dc-%s" (sha256sum (printf "%s/%s" $id $stem) | trunc 20))' '(printf "dc-%s-ops" $stem)'
  expect_configmaps DUPLICATE-UID "one dashboard uid shared across instances"

  # Case 11 — ONE DATA KEY FOR EVERY INSTANCE, alone. The bare file name the
  # chart used to render — WITH the pinned list "fixed" to match in the same
  # commit, which is exactly how this regression would arrive past the pin. Only
  # the cross-instance comparison is left to see that it is one file.
  plant "$tpl" '{{ $id }}-{{ $stem }}.json: |-' '{{ $stem }}.json: |-'
  (
    EXPECTED_DATA_KEYS=("command-delivery.json" "event-processing.json")
    expect_configmaps DUPLICATE-KEY "one data key (one sidecar file) shared across instances"
  ) || exit 1

  # Case 12 — A UID GRAFANA WILL REFUSE, alone. Still unique per instance, so
  # only the length check sees it: `dc-` plus 40 hex characters is 43.
  plant "$tpl" '| trunc 20))' '| trunc 40))'
  expect_configmaps UID-TOO-LONG "a dashboard uid over Grafana's 40-character limit"

  # Case 13 — A BOARD THAT IS NOT SCOPED TO ITS INSTANCE, alone. The template
  # keeps the file's own `namespace` entry instead of replacing it: every uid,
  # key and folder is still right, and the board reads no instance's series.
  plant "$tpl" '{{- $vars = append $vars $namespaceVar }}' '{{- $vars = append $vars . }}'
  expect_configmaps NAMESPACE "a board whose namespace variable does not name its instance"

  # Case 14 — ONE FOLDER FOR EVERY INSTANCE, alone.
  plant "$tpl" 'grafana_folder: devicechain-{{ $id }}' 'grafana_folder: devicechain'
  expect_configmaps FOLDER "a grafana_folder annotation shared across instances"

  # Case 15 — A PINNED DASHBOARD THAT NO LONGER RENDERS, alone. The template's
  # glob is narrowed so event-processing.json is skipped: every board that does
  # render is right, and only the pinned sets notice the one that is gone.
  plant "$tpl" '.Files.Glob "dashboards/*.json"' '.Files.Glob "dashboards/c*.json"'
  expect_configmaps KEY-MISSING "a pinned dashboard that no longer renders"

  # Case 16 — A BOARD WITH NO `namespace` VARIABLE, alone. This one is refused
  # by the TEMPLATE, not by the checker above: without the variable there is
  # nothing to replace with the instance's constant, and every panel's
  # namespace="$namespace" selector would match nothing. The render must fail,
  # and fail with the template's own message rather than for an unrelated reason.
  python3 - "$chart/dashboards/command-delivery.json" "$chart/dashboards/unscoped-board.json" <<'FIXTURE' ||
import json, sys
board = json.load(open(sys.argv[1]))
before = len(board["templating"]["list"])
board["templating"]["list"] = [v for v in board["templating"]["list"] if v.get("name") != "namespace"]
if len(board["templating"]["list"]) != before - 1:
    sys.exit(1)
json.dump(board, open(sys.argv[2], "w"), indent=2)
FIXTURE
    fail "the unscoped-board fixture could not be built"
  unscoped_rc=0
  unscoped_err="$(helm template dc "$chart" \
    --set "instance.config.infrastructure.secrets.rootKey=$(openssl rand -base64 32)" 2>&1 >/dev/null)" || unscoped_rc=$?
  [ "$unscoped_rc" -ne 0 ] || fail "rendered a dashboard that has no namespace variable"
  grep -qF 'dashboard dashboards/unscoped-board.json has no "namespace" template variable' <<<"$unscoped_err" || {
    echo "$unscoped_err" >&2
    fail "the render of an unscoped dashboard failed, but not with the template's own refusal"
  }
  rm -f "$chart/dashboards/unscoped-board.json"
  echo "  ok: a dashboard with no namespace variable is refused by the template"

  echo "self-test passed: 15 defects, each planted alone, each caught; a clean tree and chart pass"
  exit 0
fi

say "parsing the chart's Grafana dashboards and resolving their series"
check_content "$ROOT"

say "rendering two instances: pinned names and keys, and nothing the two share"
check_configmaps "$ROOT/deploy/helm/devicechain"

note "dashboards parse, every series they and the alert rules name is registered, names and keys are pinned, instances share no board"
