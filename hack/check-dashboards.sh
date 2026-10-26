#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guard: the chart's Grafana dashboards must be REAL, must name REAL series,
# must keep the ConfigMap names they already have, and must keep two instances'
# boards apart.
#
# SEVEN GAPS (the fifth is the published-text rule below), ALL OF THEM SILENT, NONE OF THEM COVERED BY ANYTHING ELSE.
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
#    instance's NAMESPACE, two instances sharing one namespace, or a ConfigMap
#    without that instance's `grafana_folder`.
#
#    🔴 THE NAMESPACE IS NOT THE INSTANCE ID. It was, and this check asserted the
#    id, until an instance's namespace gained its `dci-` prefix; the id still names
#    the ConfigMap, the data key, the folder and the uid hash input. So the expected
#    value is not reconstructed from the id here — it is read from the Deployments in
#    the same render, which is the namespace whose label the instance's own series
#    actually carry, and is therefore an assertion about what the board READS rather
#    than about a naming convention.
#
# 6. NOTHING CHECKED THAT A SUM OF COUNTERS SURVIVES ONE OF THEM BEING ABSENT. `sum(increase(a)) +
#    sum(increase(b)) or vector(0)` is EMPTY when `b` has no series, and the fallback then shows 0 for
#    the total while `a` holds real values (400 malformed requests left both "malformed" panels at 0).
#    Every `+`/`-` over two or more rate()/increase() terms must guard each term with `or vector(0)`,
#    or end with `# absent-ok: <why>`. See "gap 6" in check_content.
#
# 7. A WHOLE-NUMBER summary quantile matched with `=` (quantile="1") selects nothing on Prometheus 3,
#    which stores "1.0"; the panel or alert over it is empty for good. See "gap 7".
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
BOARDS=(
  command-delivery
  delivery
  event-processing
  ingest
  messaging
  overview
  persistence
  platform
)
# ...and the two published names, spelled out from that list. This is the only
# place they are derived: the list above is the literal, the names below are what
# the template must render for each entry.
EXPECTED_CONFIGMAPS=()
EXPECTED_DATA_KEYS=()
for board in "${BOARDS[@]}"; do
  EXPECTED_CONFIGMAPS+=("{instance}-$board-dashboard")
  EXPECTED_DATA_KEYS+=("{instance}-$board.json")
done

# check_board_list <dashboards-dir> <board>...
#
# The board list and the files on disk must name the same set, in BOTH
# directions. A file the list does not name would still render (the template
# globs dashboards/*.json) and only the render-time NAME-UNPINNED check would
# object; a name with no file would be a board promised and never shipped. Doing
# it on the files makes the failure say which side is missing and what to edit.
check_board_list() {
  local dir="$1" f stem rc=0
  shift
  [ "$#" -gt 0 ] || { echo "FAIL: the board list is empty -- this check would pass over nothing" >&2; return 1; }
  [ -d "$dir" ] || { echo "FAIL: $dir is not a directory" >&2; return 1; }
  for f in "$dir"/*.json; do
    [ -e "$f" ] || continue
    stem="$(basename "$f" .json)"
    case " $* " in
      *" $stem "*) ;;
      *) echo "BOARD-UNLISTED: dashboards/$stem.json exists but BOARDS in hack/check-dashboards.sh does not name \"$stem\". Add it to that list (one entry) so its names and keys are pinned." >&2; rc=1 ;;
    esac
  done
  for stem in "$@"; do
    [ -f "$dir/$stem.json" ] || {
      echo "BOARD-MISSING: BOARDS names \"$stem\" but dashboards/$stem.json does not exist. Add the file or drop the entry." >&2
      rc=1
    }
  done
  return "$rc"
}

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
import json, os, re, subprocess, sys, tempfile

root = sys.argv[1]
chart = os.path.join(root, "deploy", "helm", "devicechain")
dashboard_dir = os.path.join(chart, "dashboards")
template_dir = os.path.join(chart, "templates")
services_dir = os.path.join(root, "backend", "services")
go_root = os.path.join(root, "backend")

problems = []

# --- gap 5: a board's TEXT is a published surface ----------------------------
# Board titles, descriptions, legends and row titles are rendered in Grafana and end
# up in the docs site's screenshots, where hack/check-docs-adr-refs.sh -- a guard
# over Markdown -- cannot read them. The ADRs live in a private repository, so an
# ADR number there is a citation nobody who reads the page can follow. The same goes
# for the planning workspace and the maintainer's severity marker. This is the ADR
# rule extended to the one surface it could not see, not a drift gate.
PUBLISHED_TEXT_KEYS = {"title", "description", "legendFormat", "displayName", "text"}
INTERNAL_REFERENCES = (
    (re.compile(r"\bADRs?\b(?:[-\s]?[0-9]+)?"), "an ADR reference"),
    (re.compile(r"\bSD-[0-9]+\b"), "a design-decision reference"),
    (re.compile(r"\.agent-os"), "a path in the private planning workspace"),
    (re.compile("\U0001F534"), "the red-circle severity marker used in source comments"),
)

def published_text_problems(node, rel, trail="$"):
    found = []
    if isinstance(node, dict):
        for key, value in node.items():
            where = "%s.%s" % (trail, key)
            if key in PUBLISHED_TEXT_KEYS and isinstance(value, str):
                for pattern, what in INTERNAL_REFERENCES:
                    hit = pattern.search(value)
                    if hit:
                        found.append(
                            "%s %s contains %s (%r).\n"
                            "    Board text is published in screenshots; say the thing itself in plain\n"
                            "    operator language instead of citing an internal document."
                            % (rel, where, what, hit.group(0)))
            else:
                found.extend(published_text_problems(value, rel, where))
    elif isinstance(node, list):
        for i, value in enumerate(node):
            found.extend(published_text_problems(value, rel, "%s[%d]" % (trail, i)))
    return found

# --- gap 1: every dashboard must be JSON ------------------------------------
# 🔑 THE BOARD CONVENTION (deploy/helm/devicechain/dashboards/README.md). Every board
# is one of N lanes' output, so the shape every lane shares is checked here rather than
# remembered: file stem -> uid, title prefix, the tags and variables the template
# rewrites, and a "Firing alerts" table on top that selects this instance's alerts.
ALERT_SELECTORS = ('ALERTS{', 'alertstate="firing"', 'devicechain_instance="$instance"')
INSTANCE_SELECTOR = re.compile(r'devicechain_instance\s*=~?\s*"([^"]*)"')

def convention_problems(board, stem, rel):
    found = []
    def bad(msg):
        found.append("%s: %s" % (rel, msg))
    if board.get("uid") != "dc-%s-ops" % stem:
        bad("uid is %r, want 'dc-%s-ops'. The chart replaces it per instance, but the file\n"
            "    keeps the convention so a reader can tell which board a file is." % (board.get("uid"), stem))
    if not str(board.get("title", "")).startswith("DeviceChain — "):
        bad("title %r must start with 'DeviceChain — ' (the chart appends [<instance>])." % board.get("title"))
    if "devicechain" not in (board.get("tags") or []):
        bad("tags must include 'devicechain'.")
    names = [v.get("name") for v in (board.get("templating") or {}).get("list") or [] if isinstance(v, dict)]
    for need in ("datasource", "namespace", "instance"):
        if names.count(need) != 1:
            bad("needs exactly one %r template variable (found %d). The chart replaces namespace\n"
                "    and instance with per-instance constants and refuses a board without them."
                % (need, names.count(need)))
    panels = board.get("panels") or []
    first = panels[0] if panels else {}
    exprs = " ".join(t.get("expr", "") for t in first.get("targets") or [])
    if (first.get("type") != "table" or first.get("title") != "Firing alerts"
            or any(sel not in exprs for sel in ALERT_SELECTORS)):
        bad("the first panel must be the 'Firing alerts' table over\n"
            '    ALERTS{alertstate="firing", devicechain_instance="$instance", ...} (copy it from README.md).')
    elif (first.get("gridPos") or {}).get("y") != 0 or (first.get("gridPos") or {}).get("x") != 0:
        bad("the 'Firing alerts' table must sit at the top left (gridPos x=0, y=0).")
    def exprs_of(node):
        if isinstance(node, dict):
            for key, value in node.items():
                if key == "expr" and isinstance(value, str):
                    yield value
                else:
                    yield from exprs_of(value)
        elif isinstance(node, list):
            for value in node:
                yield from exprs_of(value)
    for expr in exprs_of(board):
        for m in INSTANCE_SELECTOR.finditer(expr):
            if m.group(1) != "$instance":
                bad("selects devicechain_instance=%r. Use \"$instance\": a literal ID is one\n"
                    "    instance's board shipped to all of them." % m.group(1))
    if re.search(r"/d/dc-[0-9a-f]{20}", json.dumps(board)):
        bad("links to a literal board uid. A uid is a hash of the instance; write /d/@board:<file stem>@.")
    return found

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
        board = json.loads(raw.decode("utf-8"))
    except (ValueError, UnicodeDecodeError) as exc:
        problems.append(
            "%s is not parseable JSON: %s\n"
            "    The chart parses it to scope it per instance, so every render of the chart\n"
            "    fails on this file."
            % (os.path.relpath(path, root), exc)
        )
        continue
    problems.extend(published_text_problems(board, os.path.relpath(path, root)))
    problems.extend(convention_problems(
        board, os.path.basename(path)[:-len(".json")], os.path.relpath(path, root)))

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
#
# 🔴 THE PATTERN GOES TO grep IN A FILE (-f), NEVER AS AN ARGUMENT. It contains
# double quotes, and on Windows an argument list is flattened into ONE command
# line that the MSYS grep then re-parses. The pattern arrived as
# \[a-z][a-z0-9_]*" -- the leading quote turned into a backslash -- matched
# nothing, and the run stopped at "found no Go string literals" below, before
# checking a single dashboard. A file reaches grep byte for byte on every
# platform. Written in binary, so no platform adds a carriage return to it either.
with tempfile.NamedTemporaryFile("wb", suffix=".pattern", delete=False) as pattern_file:
    pattern_file.write(b'"[a-z][a-z0-9_]*"\n')
try:
    out = subprocess.run(
        ["grep", "-rhoE", "-f", pattern_file.name, go_root, "--include=*.go"],
        capture_output=True, text=True, check=False,
    )
except OSError as exc:
    sys.exit("could not run grep over the Go tree: %s" % exc)
finally:
    os.unlink(pattern_file.name)
literals = {line.strip('"') for line in out.stdout.splitlines()}
if not literals:
    sys.exit(
        "found no Go string literals under %s.\n"
        "  Every series would resolve to 'missing' and this check would fail everything,\n"
        "  which is as broken as passing everything." % go_root
    )

# 🔴 A HISTOGRAM EXPOSES MORE SERIES THAN IT IS REGISTERED UNDER. Registering
# `detect_rule_stat_write_seconds` as a histogram exports `_bucket`, `_sum` and
# `_count` and NO series under the bare name, so a latency panel can only ever name
# a derived series -- which no Go literal spells. The literal test above therefore
# rejected every latency panel. The way out is NOT to loosen the suffix match: a
# suffix is accepted only when its base is registered, IN THAT AREA, as the type
# that exports it. `_bucket` on a counter or a gauge is a typo that draws an empty
# graph, and `_buckets` is one on any type; both stay rejected.
#
#   histogram -> _bucket, _sum, _count        summary -> _sum, _count (no _bucket)
#
# Types are read from the registration call, `ms.NewHistogramVec("name", ...)`,
# because the name's type is not recoverable from the name. Test files are skipped:
# a metric registered only in a test is not exported by any service; so are lines
# that are comments, because a commented-out histogram exports nothing either.
#
# 🔴 A NAME CAN BE SEEN UNDER MORE THAN ONE TYPE, so each name keeps the SET of types
# it was registered as, and a derived suffix is accepted only when that set is
# exactly the type exporting it. Last-write-wins would let file-walk order decide
# whether a gauge and a histogram of one name make `_bucket` real.
#
# 🔴 THE BARE NAME OF A HISTOGRAM IS ITSELF A DEFECT. It IS a Go literal, so the
# literal test passes it, but a histogram exports no series under it:
# `rate(devicechain_x_lat_seconds[5m])` is an empty graph, which is exactly the
# defect this file exists to catch. So a name registered as a histogram in the area
# is rejected bare. A SUMMARY's bare name is not: it carries the quantile series
# ({quantile="0.99"}), so it has samples and stays accepted.
#
# The core helpers that assemble names from a prefix are taught here too, by the
# suffixes core/core/metrics.go and core/core/periodic.go append, so a processing
# loop's or a periodic task's histogram resolves rather than failing closed. A
# helper called with anything but a string literal is not seen, and its series fail
# closed; so do the histograms core/messaging registers for every area that streams.
DERIVED_SUFFIXES = {
    "histogram": ("_bucket", "_sum", "_count"),
    "summary": ("_sum", "_count"),
}
REGISTRATION = re.compile(
    r"\.New(Histogram|Summary|Counter|Gauge)(?:Vec)?\(\s*\"([a-z][a-z0-9_]*)\"")
HELPER = re.compile(r"\.(NewProcessorMetrics|NewPeriodicTaskMetrics)\(\s*\"([a-z][a-z0-9_]*)\"")
HELPER_FAMILIES = {
    "NewProcessorMetrics": (
        ("_messages_total", "counter"), ("_duration_seconds", "histogram"), ("_inflight", "gauge")),
    "NewPeriodicTaskMetrics": (
        ("_passes_total", "counter"), ("_pass_duration_seconds", "histogram"),
        ("_last_success_timestamp_seconds", "gauge")),
}
COMMENT_LINE = re.compile(r"^[ \t]*//.*$", re.M)
kinds = {}  # area directory -> {registered name: {histogram|summary|counter|gauge}}
for dirpath, _dirs, files in os.walk(services_dir):
    rel_parts = os.path.relpath(dirpath, services_dir).split(os.sep)
    area_dir = rel_parts[0]
    if area_dir == ".":
        continue
    for fname in files:
        if not fname.endswith(".go") or fname.endswith("_test.go"):
            continue
        with open(os.path.join(dirpath, fname), "rb") as fh:
            src = COMMENT_LINE.sub("", fh.read().decode("utf-8", "replace"))
        area_kinds = kinds.setdefault(area_dir, {})
        for reg in REGISTRATION.finditer(src):
            area_kinds.setdefault(reg.group(2), set()).add(reg.group(1).lower())
        for reg in HELPER.finditer(src):
            for suffix, kind in HELPER_FAMILIES[reg.group(1)]:
                area_kinds.setdefault(reg.group(2) + suffix, set()).add(kind)

# 🔑 CORE REGISTERS METRICS UNDER EVERY SERVICE'S SUBSYSTEM. The messaging families
# (jetstream_*, kv_cache_*, ...) are built once in backend/core and exported by each
# area that touches the broker, so a histogram there (jetstream_publish_duration_seconds)
# exports _bucket/_sum/_count under every area's name. Reading only backend/services
# left those histograms invisible: their derived series failed as unregistered and their
# bare names passed as plain literals. They are read here into a pseudo-area that every
# lookup includes.
CORE = "(core)"
core_dir = os.path.join(go_root, "core")
for dirpath, _dirs, files in os.walk(core_dir):
    for fname in files:
        if not fname.endswith(".go") or fname.endswith("_test.go"):
            continue
        with open(os.path.join(dirpath, fname), "rb") as fh:
            src = COMMENT_LINE.sub("", fh.read().decode("utf-8", "replace"))
        for reg in REGISTRATION.finditer(src):
            kinds.setdefault(CORE, {}).setdefault(reg.group(2), set()).add(reg.group(1).lower())

def lookup_areas(area_dir):
    """area_dir None means any area (a by-name selector spans them all)."""
    return [area_dir, CORE] if area_dir else list(kinds)

def registered(metric, area_dir=None):
    """Registered by a call read above, including a core helper's assembled names."""
    return any(metric in kinds.get(a, {}) for a in lookup_areas(area_dir))

def derived_base(metric, area_dir=None):
    """The registered histogram/summary a derived series belongs to, else None."""
    for kind, suffixes in DERIVED_SUFFIXES.items():
        for suffix in suffixes:
            if not metric.endswith(suffix):
                continue
            base = metric[:-len(suffix)]
            if any(kinds.get(a, {}).get(base) == {kind} for a in lookup_areas(area_dir)):
                return base
    return None

def bare_histogram(metric, area_dir=None):
    return any("histogram" in kinds.get(a, {}).get(metric, ()) for a in lookup_areas(area_dir))

BARE_HISTOGRAM = (
    "    is the bare name of a HISTOGRAM, which exports only _bucket, _sum and _count and\n"
    "    NO series under its bare name. A panel over it draws an empty graph; an alert over\n"
    "    it never fires. Use histogram_quantile(q, sum by (le) (rate(<name>_bucket[5m])))\n"
    "    or rate(<name>_sum[5m]) / rate(<name>_count[5m]).")

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

# 🔴 A SELECTOR BY __name__ REGEX IS INVISIBLE TO series_re, and it used to be skipped
# in silence. `devicechain_[a-z0-9_]+` never matches the text `devicechain_.+_...` or
# `devicechain_[a-z0-9]+_...`, because the character after `devicechain_` is a regex
# metacharacter — so an alert that selects every service's copy of one metric by name,
# which is exactly the shape that has no per-service list to keep, was the one shape
# this check said nothing about. A misspelled suffix there selects nothing, and the
# alert over it evaluates an empty vector and never fires.
#
# So the two forms the chart uses are resolved: the subsystem is a wildcard, and the
# SUFFIX after it must be a registered Go literal, exactly as the metric part of a
# literal series must be. Any OTHER regex after `devicechain_` is reported rather than
# skipped, because a form this check does not understand is a form it cannot vouch for.
REGEX_SELECTOR = re.compile(r"devicechain_(\[a-z0-9\]\+|\.\+)_([a-z0-9_]+)")
NAME_CHAR = re.compile(r"[a-z0-9_]")

seen = {}
regex_seen = {}
for path in dashboards + rule_files:
    with open(path, "rb") as fh:
        text = fh.read().decode("utf-8", "replace")
    rel = os.path.relpath(path, root)
    for start in [m.start() for m in re.finditer(r"devicechain_", text)]:
        after = start + len("devicechain_")
        if after < len(text) and NAME_CHAR.match(text, after):
            continue  # a literal series (or label) -- series_re's business below
        sel = REGEX_SELECTOR.match(text, start)
        if sel is None or text[sel.end():sel.end() + 1] in ILLEGAL_IN_NAME:
            token = text[start:start + 60].split("\n")[0]
            problems.append(
                "%s... (in %s) follows devicechain_ with a form this check cannot resolve.\n"
                "    A selector by name must be devicechain_[a-z0-9]+_<metric> or\n"
                "    devicechain_.+_<metric>, with <metric> a registered name; anything else is\n"
                "    a selector nothing here can vouch for." % (token, rel))
            continue
        regex_seen.setdefault((sel.group(1), sel.group(2)), set()).add(rel)
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
    if bare_histogram(metric, areas[subsystem]):
        problems.append("%s (in %s)\n%s" % (series, where, BARE_HISTOGRAM))
        continue
    if (metric not in literals and not registered(metric, areas[subsystem])
            and derived_base(metric, areas[subsystem]) is None):
        problems.append(
            "%s (in %s) resolves to the metric %r in the %s area, and NO Go source registers\n"
            "    that name. A panel over a series that does not exist draws an empty graph; an\n"
            "    ALERT over one evaluates an empty vector, which is not false -- it never fires\n"
            "    and never complains."
            % (series, where, metric, areas[subsystem])
        )

for (form, suffix) in sorted(regex_seen):
    if bare_histogram(suffix):
        problems.append("devicechain_%s_%s (in %s)\n%s" % (
            form, suffix, ", ".join(sorted(regex_seen[(form, suffix)])), BARE_HISTOGRAM))
        continue
    if suffix not in literals and not registered(suffix) and derived_base(suffix) is None:
        problems.append(
            "devicechain_%s_%s (in %s) selects every area's %r by name, and NO Go source\n"
            "    registers that name. The selector matches nothing, and an ALERT over it\n"
            "    evaluates an empty vector, which is not false -- it never fires and never\n"
            "    complains."
            % (form, suffix, ", ".join(sorted(regex_seen[(form, suffix)])), suffix))

# SERIES THAT ARE NOT DEVICECHAIN'S OWN. A board or an alert rule may read what other software exports
# -- kube-state-metrics, cAdvisor, the kubelet, the Go and process collectors, the database operator,
# the backup plugin, the message broker's exporter -- but nothing in this tree registers those names, so
# the Go-source check above cannot vouch for them and a misspelling would draw an empty graph (or, in an
# alert, a rule that never fires). They are pinned here instead: ONE set of exact names and ONE set of
# prefixes, scanned over every dashboard and every alert-rule template. A token that starts with a pinned
# prefix and is not in the set fails; adding a name is the moment to confirm it against the exporter's
# source or a live scrape, and a name used in one exporter version only does not belong here.
#
#   container_*, kube_*  cAdvisor and kube-state-metrics (kube-prometheus-stack versions the chart supports);
#                        kube_cnpg_* come from the kube-state-metrics custom-resource config in
#                        deploy/opentofu/modules/monitoring.
#   go_*, process_*      the client_golang Go and process collectors.
#   kubelet_*            the kubelet's volume statistics; the storage alert rules read them.
#   cnpg_*               CloudNativePG 1.30.0 (chart 0.29.0): the default monitoring queries
#                        (config/manager/default-monitoring.yaml, exported as cnpg_<query>_<column>) and the
#                        built-in collector (pkg/management/postgres/webserver/metricserver).
#   barman_*             the Barman Cloud plugin; the alert rules read the same name.
#   nats_*               the prometheus-nats-exporter sidecar the nats chart runs beside each server
#                        (image natsio/prometheus-nats-exporter:0.20.2, started with -varz -connz -routez
#                        -subz -jsz=all -prefix=nats). Varz names are the server's /varz JSON path joined
#                        with "_"; jsz names are fixed in the exporter's collector/jsz.go. Pinned to that
#                        version: when the image moves, re-read those two sources and edit this set.
EXTERNAL_PREFIXES = ("barman", "cnpg", "container", "go", "kube", "kubelet", "nats", "process")
EXTERNAL_SERIES = {
    # cAdvisor
    "container_cpu_cfs_periods_total",
    "container_cpu_cfs_throttled_periods_total",
    "container_cpu_usage_seconds_total",
    "container_memory_working_set_bytes",
    # Go runtime and process collectors
    "go_gc_duration_seconds",
    "go_gc_duration_seconds_count",
    "go_goroutines",
    "go_memstats_heap_inuse_bytes",
    "process_max_fds",
    "process_open_fds",
    "process_resident_memory_bytes",
    # kube-state-metrics
    "kube_cnpg_backup_stopped_at",
    "kube_cnpg_cluster_condition",
    "kube_cnpg_scheduledbackup_snapshot_retention_checked_at",
    "kube_deployment_spec_replicas",
    "kube_deployment_status_replicas_available",
    "kube_pod_container_resource_limits",
    "kube_pod_container_resource_requests",
    "kube_pod_container_status_last_terminated_reason",
    "kube_pod_container_status_restarts_total",
    "kube_pod_status_ready",
    # kubelet
    "kubelet_volume_stats_available_bytes",
    "kubelet_volume_stats_capacity_bytes",
    "kubelet_volume_stats_used_bytes",
    # CloudNativePG
    "cnpg_backends_max_tx_duration_seconds",
    "cnpg_backends_total",
    "cnpg_collector_pg_wal_archive_status",
    "cnpg_collector_up",
    "cnpg_pg_database_size_bytes",
    "cnpg_pg_database_xid_age",
    "cnpg_pg_replication_in_recovery",
    "cnpg_pg_replication_lag",
    "cnpg_pg_replication_streaming_replicas",
    "cnpg_pg_settings_setting",
    "cnpg_pg_stat_archiver_archived_count",
    "cnpg_pg_stat_archiver_failed_count",
    "cnpg_pg_stat_archiver_last_archived_time",
    "cnpg_pg_stat_archiver_last_failed_time",
    "cnpg_pg_stat_archiver_seconds_since_last_archival",
    "cnpg_pg_stat_database_deadlocks",
    "cnpg_pg_stat_database_xact_commit",
    "cnpg_pg_stat_database_xact_rollback",
    # Barman Cloud plugin
    "barman_cloud_cloudnative_pg_io_last_available_backup_timestamp",
    # prometheus-nats-exporter, /varz
    "nats_varz_cpu", "nats_varz_mem", "nats_varz_connections", "nats_varz_total_connections",
    "nats_varz_routes", "nats_varz_remotes", "nats_varz_in_msgs", "nats_varz_out_msgs",
    "nats_varz_in_bytes", "nats_varz_out_bytes", "nats_varz_slow_consumers",
    "nats_varz_stale_connections", "nats_varz_stalled_clients", "nats_varz_subscriptions",
    "nats_varz_jetstream_stats_api_total", "nats_varz_jetstream_stats_api_errors",
    # prometheus-nats-exporter, /jsz server level
    "nats_server_jetstream_disabled", "nats_server_total_streams", "nats_server_total_consumers",
    "nats_server_total_messages", "nats_server_total_message_bytes", "nats_server_max_memory",
    "nats_server_max_storage",
    # /jsz account level
    "nats_account_max_memory", "nats_account_max_storage", "nats_account_storage_used",
    "nats_account_memory_used",
    # /jsz stream level
    "nats_stream_total_messages", "nats_stream_total_bytes", "nats_stream_limit_messages",
    "nats_stream_limit_bytes", "nats_stream_first_seq", "nats_stream_last_seq",
    "nats_stream_consumer_count", "nats_stream_subject_count",
    # /jsz consumer level
    "nats_consumer_delivered_consumer_seq", "nats_consumer_delivered_stream_seq",
    "nats_consumer_last_delivery_seconds", "nats_consumer_last_ack_seconds",
    "nats_consumer_num_ack_pending", "nats_consumer_num_redelivered", "nats_consumer_num_waiting",
    "nats_consumer_num_pending", "nats_consumer_ack_floor_stream_seq",
    "nats_consumer_ack_floor_consumer_seq",
}
# The look-behind keeps a token that merely ENDS in a prefix out: the platform's own
# devicechain_<area>_nats_connection_dead_total is a DeviceChain series, checked above.
EXTERNAL_RE = re.compile(
    r"(?<![A-Za-z0-9_])(?:%s)_[a-z0-9_]+" % "|".join(sorted(EXTERNAL_PREFIXES, key=len, reverse=True)))
def rule_exprs(text):
    """The text of every `expr:` in a rule template: the inline value, or the block scalar under it.
    Prose, comments and descriptions are left out on purpose -- they name configuration, modules and
    families (`cnpg_cluster_metrics`, `barman_cloud_*`) that are not series."""
    lines = text.split("\n")
    for i, line in enumerate(lines):
        m = re.match(r"^(\s*)(?:- )?expr:\s*(.*)$", line)
        if not m:
            continue
        indent, rest = len(m.group(1)), m.group(2)
        if rest and rest[0] not in "|>":
            yield rest
            continue
        for follow in lines[i + 1:]:
            if follow.strip() and len(follow) - len(follow.lstrip()) <= indent:
                break
            yield follow

external_seen = {}
def scan_external(path, text):
    for m in EXTERNAL_RE.finditer(text):
        external_seen.setdefault(m.group(0), set()).add(os.path.relpath(path, root))
# A dashboard is scanned whole: a series named in a panel title or description is a claim too.
for path in dashboards:
    with open(path, "rb") as fh:
        scan_external(path, fh.read().decode("utf-8", "replace"))
# A rule template is scanned in its expressions only.
for path in rule_files:
    with open(path, "rb") as fh:
        scan_external(path, "\n".join(rule_exprs(fh.read().decode("utf-8", "replace"))))
for series in sorted(external_seen):
    if series not in EXTERNAL_SERIES:
        problems.append(
            "%s (in %s) is neither a DeviceChain series nor on the pinned list of external series\n"
            "    (EXTERNAL_SERIES in hack/check-dashboards.sh: kube-state-metrics, cAdvisor, the kubelet,\n"
            "    Go and process, CloudNativePG, Barman Cloud, the broker exporter). Nothing here can vouch\n"
            "    for it: a misspelling draws an empty graph, or an alert that never fires. If the name is\n"
            "    real, confirm it against the exporter and add it to that list."
            % (series, ", ".join(sorted(external_seen[series]))))

# --- gap 6: ONE ABSENT TERM EMPTIES A SUM, AND THE FALLBACK THEN SHOWS ZERO ----
# 🔴 `sum(increase(a[r])) + sum(increase(b[r])) or vector(0)` LOOKS LIKE "THE TOTAL, OR ZERO" AND IS NOT.
# PromQL's `+` between two vectors keeps only the label sets present on BOTH sides, so when
# one counter has no series at all -- a label vector nothing has incremented yet, or a service
# the profile does not install -- the whole sum is EMPTY, and `or vector(0)` then substitutes 0
# for the TOTAL while the other terms hold real values. Measured live: 400 malformed requests
# left both "malformed" panels at 0. `or` binds LOOSER than `+`, so a fallback written once, at
# the end, guards nothing about any one term.
#
# The shape that is correct guards EACH counter term: `(sum(increase(a)) or vector(0)) +
# (sum(increase(b)) or vector(0))`. This refuses an arithmetic `+`/`-` whose operands include two
# or more rate()/irate()/increase() terms unless EVERY such term is wrapped in parentheses holding
# an `or`. A sum that is meant to be empty when a term is absent (a ratio whose denominator must
# exist) says so with a trailing PromQL comment, `# absent-ok: <why>`, which both Grafana and
# Prometheus accept at the end of an expression.
#
# It is a parser, not a regex: the same text split on a naive `+` would read the `+` inside a
# label value or a nested function as arithmetic, and a respelling would walk past it.
COUNTER_TERM = re.compile(r"\b(?:rate|irate|increase)\s*\(")
ABSENT_OK = re.compile(r"#\s*absent-ok\b")
BACKSLASH = chr(92)
OR_ARM = re.compile(r"\s+(?:or|and|unless)\s+")
ONLY_OR = re.compile(r"\s+or\s+")
ARITH = re.compile(r"\s*\+\s*|\s+-\s+")
COMPARE = re.compile(r"\s*(?:>=|<=|==|!=|>|<)\s*(?:bool\s+)?")
COMMA = re.compile(r",")

def _skip_string(text, i):
    quote = text[i]
    j = i + 1
    while j < len(text) and text[j] != quote:
        j += 2 if text[j] == BACKSLASH else 1
    return j + 1

def _skip_selector(text, i):
    close = "}" if text[i] == "{" else "]"
    j = i + 1
    while j < len(text) and text[j] != close:
        j = _skip_string(text, j) if text[j] in "\"'`" else j + 1
    return j + 1

def _split_top(text, seps):
    """Split text at the depth-0 matches of seps, never inside (), {}, [] or a string."""
    parts, cur, i, depth = [], [], 0, 0
    while i < len(text):
        c = text[i]
        if c in "\"'`" or c in "{[":
            j = _skip_string(text, i) if c in "\"'`" else _skip_selector(text, i)
            cur.append(text[i:j])
            i = j
            continue
        if c == "(":
            depth += 1
        elif c == ")":
            depth -= 1
        if depth == 0:
            hit = next((m for m in (s.match(text, i) for s in seps) if m and m.end() > i), None)
            if hit:
                parts.append("".join(cur))
                cur = []
                i = hit.end()
                continue
        cur.append(c)
        i += 1
    parts.append("".join(cur))
    return parts

def _paren_groups(text):
    stack, i = [], 0
    while i < len(text):
        c = text[i]
        if c in "\"'`":
            i = _skip_string(text, i)
            continue
        if c in "{[":
            i = _skip_selector(text, i)
            continue
        if c == "(":
            stack.append(i)
        elif c == ")" and stack:
            start = stack.pop()
            yield text[start + 1:i]
        i += 1

def _wraps_whole(s):
    depth = 0
    for k, ch in enumerate(s):
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
            if depth == 0 and k < len(s) - 1:
                return False
    return True

def _term_guarded(term):
    s = term.strip()
    while s.startswith("(") and s.endswith(")") and _wraps_whole(s):
        inner = s[1:-1]
        if len(_split_top(inner, [ONLY_OR])) > 1:
            return True
        s = inner.strip()
    return False

def absent_term_arms(expr):
    """The arithmetic arms of expr that add or subtract two counter terms without guarding each."""
    if ABSENT_OK.search(expr):
        return []
    found = []
    for group in list(_paren_groups(expr)) + [expr]:
        for arg in _split_top(group, [COMMA]):
            for arm in _split_top(arg, [OR_ARM]):
                # a comparison binds looser than + and -, so each side of it is its own sum
                for side in _split_top(arm, [COMPARE]):
                    operands = _split_top(side, [ARITH])
                    terms = [o for o in operands if COUNTER_TERM.search(o)]
                    if len(operands) > 1 and len(terms) > 1 and not all(_term_guarded(o) for o in terms):
                        if side.strip() not in found:
                            found.append(side.strip())
    return found

def _board_exprs(node, trail="$"):
    if isinstance(node, dict):
        for key, value in node.items():
            if key == "expr" and isinstance(value, str):
                yield "%s.expr" % trail, value
            else:
                yield from _board_exprs(value, "%s.%s" % (trail, key))
    elif isinstance(node, list):
        for i, value in enumerate(node):
            yield from _board_exprs(value, "%s[%d]" % (trail, i))

def _rule_expr_blocks(text):
    lines = text.split("\n")
    for i, line in enumerate(lines):
        m = re.match(r"^(\s*)(?:- )?expr:\s*(.*)$", line)
        if not m:
            continue
        indent, rest = len(m.group(1)), m.group(2)
        if rest and rest[0] not in "|>":
            yield i + 1, rest
            continue
        block = []
        for follow in lines[i + 1:]:
            if follow.strip() and len(follow) - len(follow.lstrip()) <= indent:
                break
            block.append(follow)
        yield i + 1, "\n".join(block)

ABSENT_TERM_ADVICE = (
    "    adds or subtracts two or more rate()/increase() terms without guarding each one. If any ONE\n"
    "    of those counters has no series (a label vector nothing has incremented yet, an area the\n"
    "    profile does not install) the whole arithmetic is EMPTY, and an `or vector(0)` written once at\n"
    "    the end then shows 0 for the TOTAL while the other terms hold real values. Guard each term:\n"
    "    (sum(increase(a[r])) or vector(0)) + (sum(increase(b[r])) or vector(0)).\n"
    "    If emptiness really is the right answer, end the expression with `# absent-ok: <why>`.")
absent_term_count = 0
for path in dashboards:
    try:
        with open(path, "rb") as fh:
            board = json.loads(fh.read().decode("utf-8"))
    except (ValueError, UnicodeDecodeError):
        continue  # reported as unparseable above
    for where, expr in _board_exprs(board):
        absent_term_count += 1
        for arm in absent_term_arms(expr):
            problems.append("%s %s\n    %s\n%s" % (os.path.relpath(path, root), where, arm[:200], ABSENT_TERM_ADVICE))
for path in rule_files:
    with open(path, "rb") as fh:
        text = fh.read().decode("utf-8", "replace")
    for line_no, expr in _rule_expr_blocks(text):
        absent_term_count += 1
        for arm in absent_term_arms(expr):
            problems.append("%s:%d\n    %s\n%s" % (
                os.path.relpath(path, root), line_no, " ".join(arm.split())[:200], ABSENT_TERM_ADVICE))
if absent_term_count == 0:
    sys.exit("found no expressions to check for absent terms -- this rule would pass over nothing")

# --- gap 7: quantile="1" NEVER MATCHES ON PROMETHEUS 3 -------------------------
# A summary's quantile label is stored as the float the client formatted: Prometheus 3 and the
# Go client write 1 as "1.0", so an exact `quantile="1"` selects nothing and the panel or alert
# over it is empty forever (the Platform board's longest-GC-pause panel was). Fractions such as
# "0.99" are spelled the same either way. The exact match on a whole number is refused; match
# `quantile=~"1|1.0"` (or `0|0.0`).
WHOLE_QUANTILE = re.compile(r'quantile\s*=\s*(?:\\?")(?:0|1)(?:\\?")')
for path in list(dashboards) + list(rule_files):
    with open(path, "rb") as fh:
        text = fh.read().decode("utf-8", "replace")
    if WHOLE_QUANTILE.search(text):
        problems.append(
            "%s selects a summary quantile of exactly 0 or 1 with `=`.\n"
            "    Prometheus 3 stores those as \"0.0\" and \"1.0\", so the selector matches nothing and the\n"
            "    panel or alert over it is empty for good. Use quantile=~\"1|1.0\" (or \"0|0.0\")."
            % os.path.relpath(path, root))

# --- gap 8: A BACKLOG THAT ONLY THE READER EXPORTS IS BLIND WHILE THE READER IS DOWN ---
# A service that drains a stream exports its own backlog (the jetstream_consumer_* gauges every
# service shares, and DETECT's detect_consumer_* pair). When that service stops, which is the main
# way a stream backs up, its series vanish: a panel over them reads "No data" and an alert over them
# evaluates an empty vector and stays quiet, while the message broker holds the whole backlog
# (measured: device-state down, its board panel empty for the whole outage with 115,779 waiting).
#
# A BACKLOG TARGET is a panel target, or a rule's expr, that selects any of:
#   *_jetstream_consumer_pending_messages, *_jetstream_consumer_ack_pending_messages,
#   *_jetstream_consumer_unread_ratio, *_detect_consumer_pending, *_detect_consumer_ack_pending.
# Deliberately NOT one: unread_gap_messages and unread_skipped_total (a cursor position and a loss
# counter, which only the reader can know -- the broker has no equivalent), the
# backpressure_unread_ratio (exported by the stream's WRITERS), and mqtt_failclosed_durable_* (sampled
# from the MQTT gateway's own store, not a platform stream a reader drains).
#
# It must carry the broker's own count of the same quantity AND THE SAME AREA after an `or`, built from the
# exporter's own labels only: nats_consumer_num_pending
# for pending, nats_consumer_num_ack_pending for ack-pending, both plus nats_stream_limit_* for the
# ratio. In a rule file the chart's helpers (devicechain.brokerPending, devicechain.brokerUnreadRatio)
# stand for those. On a dashboard a panel is also covered when ANOTHER of its targets reads the broker
# series of that kind (a reader line drawn beside the broker's own, to compare them). An expression
# that really is only the reader's (it needs a counter only the reader has) ends with
# `# reader-only: <why>`.
BACKLOG_READER = {
    "pending": re.compile(r"_jetstream_consumer_pending_messages\b|_detect_consumer_pending\b"),
    "ack": re.compile(r"_jetstream_consumer_ack_pending_messages\b|_detect_consumer_ack_pending\b"),
    "ratio": re.compile(r"_jetstream_consumer_unread_ratio\b"),
}
READER_ONLY = re.compile(r"#\s*reader-only\b")
OR_WORD = re.compile(r"\bor\b")
# The label keys the message broker's exporter puts on its consumer series. A selector keyed on
# anything else (a reader-side label such as `stream` or `durable`) matches nothing: a dead arm.
BROKER_LABELS = {"__name__", "stream_name", "consumer_name", "namespace", "is_consumer_leader", "pod", "job"}
BROKER_NAME = re.compile(r"nats_consumer_num_(ack_)?pending\b")
LABEL_MATCH = re.compile(r'([A-Za-z_][A-Za-z0-9_]*)\s*(=~|!~|!=|=)\s*"((?:[^"\\]|\\.)*)"')
DURABLE_AREA = re.compile(r"^\.[+*]_([a-z0-9-]+)_")
READER_NAME_AREA = re.compile(r"devicechain_([a-z0-9]+)_(?:jetstream|detect)_")
HELPER_INCLUDE = re.compile(r'include\s+"devicechain\.(brokerPending|brokerUnreadRatio)"\s*\(dict((?:[^()"]|"[^"]*")*)\)')

def _norm_area(text):
    return text.replace("-", "").lower() if text else None

def _enclosing_selector(expr, pos, end):
    """The {...} selector holding position pos (a by-name selector), else the one right after it."""
    depth, i = 0, pos - 1
    while i >= 0:
        c = expr[i]
        if c == "}":
            depth += 1
        elif c == "{":
            if depth == 0:
                break
            depth -= 1
        i -= 1
    if i >= 0:
        depth, j = 0, i
        while j < len(expr):
            if expr[j] == "{":
                depth += 1
            elif expr[j] == "}":
                depth -= 1
                if depth == 0:
                    return expr[i:j + 1]
            j += 1
        return expr[i:]
    m = re.compile(r"\s*\{").match(expr, end)
    return expr[m.end() - 1:_skip_selector(expr, m.end() - 1)] if m else ""

def reader_occurrences(expr):
    """[(kind, start, area)] for every reader-side backlog selector in expr; area None = every area."""
    found = []
    for kind, rx in BACKLOG_READER.items():
        for m in rx.finditer(expr):
            sel = _enclosing_selector(expr, m.start(), m.end())
            area = None
            for key, op, val in LABEL_MATCH.findall(sel):
                if key == "durable" and op == "=~":
                    dm = DURABLE_AREA.match(val)
                    area = _norm_area(dm.group(1)) if dm else "?" + val
            if area is None:
                name = expr[max(0, expr.rfind("devicechain_", 0, m.end())):m.end()]
                am = READER_NAME_AREA.match(name)
                if am:
                    area = _norm_area(am.group(1))
            found.append((kind, m.start(), area))
    return found

def broker_selectors(expr):
    """[(kinds, start, area, problem)] for every broker-side consumer selector or chart helper in expr."""
    found = []
    for m in BROKER_NAME.finditer(expr):
        sel = _enclosing_selector(expr, m.start(), m.end())
        kinds = set()
        for nm in BROKER_NAME.finditer(sel if "__name__" in sel else m.group(0)):
            kinds.add("ack" if nm.group(1) else "pending")
        problem, consumer = None, None
        for key, op, val in LABEL_MATCH.findall(sel):
            if key not in BROKER_LABELS:
                problem = "label `%s` is not one the broker exporter sets (%s)" % (key, ", ".join(sorted(BROKER_LABELS - {"__name__"})))
            if key == "consumer_name" and op == "=~":
                consumer = val
        area = None
        if consumer is not None:
            dm = DURABLE_AREA.match(consumer)
            area = _norm_area(dm.group(1)) if dm else "?" + consumer
        found.append((kinds, m.start(), area, problem, consumer is not None))
    for m in HELPER_INCLUDE.finditer(expr):
        dur = re.search(r'"durable"\s+"([^"]*)"', m.group(2))
        kinds = {"pending"} if m.group(1) == "brokerPending" else {"pending", "ack"}
        area = None
        if dur:
            dm = DURABLE_AREA.match(dur.group(1))
            area = _norm_area(dm.group(1)) if dm else "?" + dur.group(1)
        found.append((kinds, m.start(), area, None, bool(dur)))
    return found

def _arm_serves(kind, rarea, arm, expr, after):
    kinds, _pos, barea, problem, has_consumer = arm
    if problem:
        return False
    if kind == "ratio":
        if not {"pending", "ack"} <= kinds or not ("nats_stream_limit_" in expr[after:] or "brokerUnreadRatio" in expr[after:]):
            return False
    elif kind not in kinds:
        return False
    if rarea is None:
        return True
    return has_consumer and barea == rarea

def arm_problems(expr, beside=()):
    """Why each reader-side backlog selector in expr has no working broker arm; [] when all do."""
    if READER_ONLY.search(expr):
        return []
    arms = broker_selectors(expr)
    out = []
    for kind, start, rarea, in reader_occurrences(expr):
        label = "%s%s" % (kind, " (%s)" % rarea if rarea else "")
        ors = [m.end() for m in OR_WORD.finditer(expr, start)]
        served = any(_arm_serves(kind, rarea, a, expr, a[1]) for a in arms if any(o <= a[1] for o in ors))
        if not served:
            served = any(_arm_serves(kind, rarea, a, expr, 0) for a in beside)
        if served:
            continue
        why = "no broker arm of that kind and area after an `or`"
        bad = [a[3] for a in arms if a[3]]
        if bad:
            why = bad[0]
        elif any(kind in a[0] for a in arms):
            why = "the broker arm names a different area than the reader"
        out.append("%s: %s" % (label, why))
    return out

def _panels(nodes):
    for p in nodes:
        yield p
        yield from _panels(p.get("panels", []))

BACKLOG_ADVICE = (
    "    selects a backlog that only the READING service exports, without a working broker-side arm. While\n"
    "    that service is down its series vanish and the panel reads \"No data\" (an alert stays silent) just\n"
    "    when the backlog is building. Add the broker's own count after an `or`, labelled to match:\n"
    "    ... or max by (durable) (label_replace(nats_consumer_num_pending{namespace=\"$namespace\",\n"
    "    stream_name=~\"${instance}_.+\", consumer_name=~\".+_<same area>_.+\"}, \"durable\", \"$1\", \"consumer_name\", \"(.+)\")).\n"
    "    The arm must be of the same kind (num_ack_pending for an ack-pending gauge), name the SAME area as\n"
    "    the reader, and use only the broker's labels (stream_name, consumer_name, namespace); a rule file\n"
    "    uses the devicechain.brokerPending helper. If the expression truly needs a counter only the\n"
    "    reader has, end it with `# reader-only: <why>`.")
backlog_checked = 0
for path in dashboards:
    try:
        with open(path, "rb") as fh:
            board = json.loads(fh.read().decode("utf-8"))
    except (ValueError, UnicodeDecodeError):
        continue  # reported as unparseable above
    for panel in _panels(board.get("panels", [])):
        targets = [t.get("expr", "") for t in panel.get("targets", []) if isinstance(t.get("expr"), str)]
        for expr in targets:
            if not reader_occurrences(expr):
                continue
            backlog_checked += 1
            # A reader line drawn beside the broker's own line (another target of the panel) is covered
            # by that line, but only for the same kind and area.
            beside = [a for other in targets if other is not expr for a in broker_selectors(other)]
            bad = arm_problems(expr, beside)
            if bad:
                problems.append("%s panel %s (%s)\n    %s\n    %s\n%s" % (
                    os.path.relpath(path, root), panel.get("id"), panel.get("title"),
                    "; ".join(bad), expr[:160], BACKLOG_ADVICE))
for path in rule_files:
    with open(path, "rb") as fh:
        text = fh.read().decode("utf-8", "replace")
    for line_no, expr in _rule_expr_blocks(text):
        if not reader_occurrences(expr):
            continue
        backlog_checked += 1
        bad = arm_problems(expr)
        if bad:
            problems.append("%s:%d\n    %s\n    %s\n%s" % (
                os.path.relpath(path, root), line_no, "; ".join(bad), " ".join(expr.split())[:160], BACKLOG_ADVICE))
if backlog_checked == 0:
    sys.exit("found no reader-side backlog target to check -- this rule would pass over nothing")

if problems:
    print("The chart's dashboards and alert rules do not hold up:\n", file=sys.stderr)
    for p in problems:
        print("  - %s" % p, file=sys.stderr)
    sys.exit(1)

print("    %d dashboard(s) parse; %d devicechain_* series and %d by-name selector(s) all resolve\n"
      "    to a Go registration; %d external series are all on the pinned list"
      % (len(dashboards), len([s for s in seen if s not in ALLOWED_NON_SERIES]), len(regex_seen),
         len(external_seen)))
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
import json, os, re, sys
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
scopes = {}    # namespace a board scopes itself to -> [instance]
tags = {}      # the per-instance tag -> [instance]
boards = 0

for inst in ids:
    folder = "devicechain-%s" % inst
    found_names, found_keys = set(), set()
    inst_boards = []  # (where, board, raw body) of this instance, for the cross-board checks
    with open(os.path.join(render_dir, inst + ".yaml"), encoding="utf-8") as fh:
        docs = [d for d in yaml.safe_load_all(fh) if d]

    # 🔑 THE NAMESPACE A BOARD MUST NAME IS READ OUT OF THIS RENDER, NOT DERIVED FROM
    # THE ID. Every panel filters namespace="$namespace" against series whose
    # `namespace` label is the Kubernetes namespace of the POD that emitted them, so
    # the only value that makes a board read its own instance is the namespace that
    # instance's workloads are in. That used to be the instance id and is now `dci-`
    # plus the id, and an assertion written against either spelling is an assertion
    # about the naming convention rather than about what the board reads. Taking it
    # from the Deployments in the same render asks the question that actually matters
    # and survives the next rename without being weakened for it.
    #
    # 🔴 NOT the dashboard ConfigMap's own metadata.namespace, which is the obvious
    # source and the useless one: the template writes that and the board's constant
    # from the SAME expression, so comparing them compares a value with itself and
    # holds however wrong they both are.
    workload_ns = sorted({(d.get("metadata") or {}).get("namespace")
                          for d in docs if d.get("kind") == "Deployment"} - {None, ""})
    if len(workload_ns) != 1:
        problems.append(
            "WORKLOAD-NAMESPACE: instance %s renders its Deployments into %s.\n"
            "    Exactly one namespace is expected -- it is what every board must scope itself\n"
            "    to, and with none (or several) there is no single right answer to hold the\n"
            "    boards to, so the namespace check below would be asserting nothing."
            % (inst, json.dumps(workload_ns)))
        continue
    scope_ns = workload_ns[0]

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
                  and ns[0].get("query") == scope_ns
                  and (ns[0].get("current") or {}).get("value") == scope_ns)
            if not ok:
                problems.append(
                    "NAMESPACE: %s does not scope itself to its instance. Want exactly one\n"
                    "    `namespace` template variable, a hidden constant (type constant, hide 2) whose\n"
                    "    query and current value are %r -- the namespace this instance's Deployments\n"
                    "    are in, which is where its pods' series carry that label; found %s.\n"
                    "    Every panel filters namespace=\"$namespace\", so this is what decides which\n"
                    "    instance's series the board reads. Note the instance id (%r) is NOT the\n"
                    "    answer: it still names the ConfigMap, the folder, the data key and the uid\n"
                    "    hash input, but a board scoped to it reads a namespace nothing runs in."
                    % (where, scope_ns, json.dumps(ns), inst))
            elif len(ns) == 1:
                scopes.setdefault(ns[0].get("query"), set()).add(inst)

            # The `instance` constant is what the firing-alerts table selects ALERTS by:
            # it must be this instance's ID, which is what the alert rules stamp as
            # devicechain_instance. Anything else lists another instance's alerts or none.
            iv = [v for v in (board.get("templating") or {}).get("list") or []
                  if isinstance(v, dict) and v.get("name") == "instance"]
            if not (len(iv) == 1 and iv[0].get("type") == "constant" and iv[0].get("hide") == 2
                    and iv[0].get("query") == inst and (iv[0].get("current") or {}).get("value") == inst):
                problems.append(
                    "INSTANCE: %s does not carry a hidden `instance` constant equal to %r; found %s.\n"
                    "    The firing-alerts table selects ALERTS{devicechain_instance=\"$instance\"}, so a\n"
                    "    wrong value lists another instance's alerts, or none." % (where, inst, json.dumps(iv)))

            # The per-instance tag, and the one link that lists boards by it. A link by
            # tag can only offer this instance's boards if no other instance renders it.
            own = [t for t in board.get("tags") or [] if isinstance(t, str) and t.startswith("dc-")]
            links = [l for l in board.get("links") or [] if isinstance(l, dict) and l.get("type") == "dashboards"]
            if len(own) != 1 or len(own[0]) > 50:
                problems.append(
                    "TAG: %s carries tags %s; want exactly one `dc-<hash>` per-instance tag of at most\n"
                    "    50 characters (Grafana's tag limit)." % (where, json.dumps(board.get("tags"))))
            else:
                tags.setdefault(own[0], set()).add(inst)
                if not any(l.get("tags") == [own[0]] for l in links):
                    problems.append(
                        "LINK: %s has no `dashboards` link listing exactly its own instance tag %s.\n"
                        "    Without it the boards of one instance do not link to each other; with a shared\n"
                        "    tag they would link across instances." % (where, own[0]))
            inst_boards.append((where, board, body))

    # Board-to-board references are written @board:<stem>@ and resolved per instance.
    # Every /d/<uid> in a rendered board must be a uid of THIS instance's boards, and no
    # unresolved token may survive. A literal or foreign uid is a link that opens
    # another instance's board.
    own_uids = {b.get("uid") for _w, b, _t in inst_boards}
    for where, _b, text in inst_boards:
        if "@board:" in text:
            problems.append("LINK-UNRESOLVED: %s still contains an @board:<name>@ reference." % where)
        for ref in re.findall(r"/d/([A-Za-z0-9_-]+)", text):
            if ref not in own_uids:
                problems.append(
                    "LINK-FOREIGN: %s links to /d/%s, which is not a board of instance %s.\n"
                    "    Write /d/@board:<file stem>@ so the chart resolves it for this instance."
                    % (where, ref, inst))

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

for tag, insts in sorted(tags.items()):
    if len(insts) > 1:
        problems.append(
            "DUPLICATE-TAG: tag %s is rendered by instances %s.\n"
            "    The boards' links list boards by this tag, so each instance's boards would\n"
            "    link to the other's." % (tag, ", ".join(sorted(insts))))
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
# The per-board check above holds each instance's boards to that instance's OWN render,
# which is right and is blind in one direction: if the chart put two instances' workloads
# in ONE namespace, every board would still match the namespace its own render named, and
# every board would read both instances' series. Only the comparison ACROSS the two
# renders can see it, which is the same argument DUPLICATE-UID and DUPLICATE-KEY make.
for scope, insts in sorted(scopes.items(), key=lambda kv: str(kv[0])):
    if len(insts) > 1:
        problems.append(
            "SHARED-NAMESPACE: instances %s both scope their boards to namespace %r.\n"
            "    Their workloads are therefore in one namespace, so every panel's\n"
            "    namespace=\"$namespace\" filter selects BOTH instances' series and each\n"
            "    instance's boards read the other's data."
            % (", ".join(sorted(insts)), scope))

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
BOARDS in hack/check-dashboards.sh in the same
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
	ms.NewHistogramVec("lat_seconds", "A probe histogram.", nil, nil)
	ms.NewSummary(
		"sum_seconds", "A probe summary.")
	ms.NewGauge("level", "A probe gauge.")
	ms.NewGauge("detect_consumer_pending", "A probe backlog only the reader exports.")
	ms.NewProcessorMetrics("loop")
	ms.NewPeriodicTaskMetrics("sweep")
	// ms.NewHistogramVec("ghost_seconds", "Commented out: exports nothing.", nil, nil)
	ms.NewGauge("mixed_seconds", "One name, two types.")
}
EOF
  cat >"$gosrc/more_metrics.go" <<'EOF'
package selftestarea

func registerMore(ms *Microservice) {
	ms.NewHistogramVec("mixed_seconds", "One name, two types.", nil, nil)
}
EOF
  # A histogram registered only in a test file is exported by no service.
  cat >"$gosrc/metrics_test.go" <<'EOF'
package selftestarea

func registerForTest(ms *Microservice) {
	ms.NewHistogramVec("test_only_seconds", "Registered only by a test.", nil, nil)
}
EOF
  # A CORE-REGISTERED HISTOGRAM: backend/core builds the messaging families once and
  # every service exports them, so its derived series must resolve under any area.
  mkdir -p "$work/backend/core/messaging"
  cat >"$work/backend/core/messaging/metrics.go" <<'EOF'
package messaging

func register(ms *Microservice) {
	ms.NewHistogramVec("core_lat_seconds", "A histogram registered by core.", nil, nil)
}
EOF
  # A SECOND AREA, so the area scoping of a histogram has something to be wrong
  # about: remote_seconds is a histogram in other-area only.
  mkdir -p "$work/backend/services/other-area"
  cat >"$work/backend/services/other-area/metrics.go" <<'EOF'
package otherarea

func register(ms *Microservice) {
	ms.NewHistogramVec("remote_seconds", "A histogram in another area.", nil, nil)
}
EOF

  # The pristine fixtures, kept aside so each case can restore what it mutated
  # and leave the next one planting its defect ALONE.
  cat >"$tmp/probe.json" <<'EOF'
{
  "title": "DeviceChain \u2014 Self-test probe",
  "uid": "dc-probe-ops",
  "tags": ["devicechain", "probe"],
  "templating": {"list": [{"name": "datasource", "type": "datasource"}, {"name": "namespace", "type": "constant"}, {"name": "instance", "type": "constant"}]},
  "panels": [
    {
      "type": "table",
      "title": "Firing alerts",
      "gridPos": {"h": 6, "w": 24, "x": 0, "y": 0},
      "targets": [
        {
          "expr": "max by (alertname) (ALERTS{alertstate=\"firing\", devicechain_instance=\"$instance\", alertname=~\".+\"})"
        }
      ]
    },
    {
      "type": "timeseries",
      "targets": [
        {
          "expr": "sum(rate(devicechain_selftestarea_probe_total{namespace=\"$namespace\"}[5m]))"
        },
        {
          "expr": "histogram_quantile(0.99, sum by (le) (rate(devicechain_selftestarea_lat_seconds_bucket[5m]))) / (sum(rate(devicechain_selftestarea_lat_seconds_sum[5m])) / sum(rate(devicechain_selftestarea_lat_seconds_count[5m]))) + rate(devicechain_selftestarea_sum_seconds_sum[5m]) + rate(devicechain_selftestarea_sum_seconds_count[5m]) # absent-ok: resolver fixture"
        },
        {
          "expr": "(sum(increase(devicechain_selftestarea_probe_total{namespace=\"$namespace\"}[1h])) or vector(0)) + (sum(increase(devicechain_selftestarea_other_probe_total{namespace=\"$namespace\"}[1h])) or vector(0))"
        },
        {
          "expr": "sum(kube_pod_container_status_restarts_total{namespace=\"$namespace\"}) + max(go_goroutines{namespace=\"$namespace\"}) + max(container_memory_working_set_bytes) + max(process_open_fds) + max(nats_varz_cpu) + min(cnpg_collector_up) + max(kubelet_volume_stats_used_bytes) + max(barman_cloud_cloudnative_pg_io_last_available_backup_timestamp)"
        },
        {
          "expr": "max(devicechain_selftestarea_detect_consumer_pending{namespace=\"$namespace\"}) or max(nats_consumer_num_pending{namespace=\"$namespace\", consumer_name=~\".+_self-test-area_.+\"})"
        },
        {
          "expr": "devicechain_selftestarea_sum_seconds{quantile=\"0.99\"} + histogram_quantile(0.5, sum by (le) (rate(devicechain_selftestarea_loop_duration_seconds_bucket[5m]))) + rate(devicechain_selftestarea_loop_messages_total[5m]) + rate(devicechain_selftestarea_sweep_pass_duration_seconds_count[5m]) + histogram_quantile(0.5, sum by (le) (rate({__name__=~\"devicechain_[a-z0-9]+_core_lat_seconds_bucket\"}[5m]))) # absent-ok: resolver fixture"
        }
      ]
    },
    {
      "type": "timeseries",
      "targets": [
        {
          "expr": "max(devicechain_selftestarea_detect_consumer_pending{namespace=\"$namespace\"})"
        },
        {
          "expr": "max(nats_consumer_num_pending{namespace=\"$namespace\", consumer_name=~\".+_self-test-area_.*\"})"
        }
      ]
    },
    {
      "type": "stat",
      "targets": [
        {
          "expr": "max(devicechain_selftestarea_detect_consumer_pending{namespace=\"$namespace\"}) # reader-only: fixture needs a counter only the reader has"
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
        - alert: ProbeBacklog
          expr: (max(devicechain_selftestarea_detect_consumer_pending{namespace="dc"}) or max(nats_consumer_num_pending{consumer_name=~".+_self-test-area_.+"})) > 100
          labels:
            devicechain_instance: dc
        - alert: ProbeGuardedSum
          expr: (sum(increase(devicechain_selftestarea_probe_total{namespace="dc"}[5m])) or vector(0)) + (sum(increase(devicechain_selftestarea_other_probe_total{namespace="dc"}[5m])) or vector(0)) > 0
          labels:
            devicechain_instance: dc
        - alert: ProbeByNameFiring
          expr: sum(rate({__name__=~"devicechain_[a-z0-9]+_probe_total", namespace="dc"}[5m])) > 0
          labels:
            devicechain_instance: dc
        - alert: ProbeLatencyByName
          expr: histogram_quantile(0.99, sum by (le) (rate({__name__=~"devicechain_[a-z0-9]+_lat_seconds_bucket", namespace="dc"}[5m]))) > 1
          labels:
            devicechain_instance: dc
        - alert: ProbeExternalInline
          expr: min(kube_pod_status_ready{namespace="dc"}) < 1
          labels:
            devicechain_instance: dc
        - alert: ProbeExternalBlock
          expr: |
            max(cnpg_pg_database_size_bytes{namespace="dc"})
              > 1e12
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

  # Cases 2h — HISTOGRAM-DERIVED SERIES. The clean fixture above already names
  # _bucket/_sum/_count of a histogram and _sum/_count of a summary, and Case 0
  # passed over them (the accepted half). Each defect below is planted ALONE
  # into that panel by rewriting ONE series to a name that must not resolve.
  histcase() { # <from> <to> <description> [file; default the dashboard]
    local f="${4:-$dash/probe.json}"
    sed -i "s/$1/$2/" "$f"
    grep -q "$2" "$f" || fail "the mutation for $3 did not apply"
    if check_content "$work" >/dev/null 2>&1; then
      fail "did not flag $3"
    fi
    restore
    echo "  ok: $3 is caught"
  }
  histcase 'lat_seconds_bucket' 'lat_seconds_buckets' 'a misspelled histogram suffix (_buckets)'
  histcase 'lat_seconds_bucket' 'lat_secnds_bucket' 'a histogram suffix on a base that is not registered'
  histcase 'devicechain_selftestarea_sum_seconds_sum' 'devicechain_selftestarea_sum_seconds_bucket' '_bucket on a summary'
  histcase 'devicechain_selftestarea_lat_seconds_sum' 'devicechain_selftestarea_probe_total_bucket' '_bucket on a counter'
  histcase 'devicechain_selftestarea_lat_seconds_sum' 'devicechain_selftestarea_level_count' '_count on a gauge'
  # The bare name of a histogram IS a Go literal, and exports no series at all.
  histcase 'devicechain_selftestarea_lat_seconds_sum' 'devicechain_selftestarea_lat_seconds' 'the bare name of a histogram'
  histcase 'loop_duration_seconds_bucket' 'loop_duration_seconds' "the bare name of a core helper's histogram"
  histcase 'devicechain_selftestarea_lat_seconds_count' 'devicechain_selftestarea_remote_seconds_count' "_count on another area's histogram"
  histcase 'devicechain_selftestarea_lat_seconds_count' 'devicechain_selftestarea_mixed_seconds_count' '_count on a name also registered as a gauge'
  histcase 'devicechain_selftestarea_lat_seconds_count' 'devicechain_selftestarea_ghost_seconds_count' '_count on a commented-out histogram'
  histcase 'devicechain_selftestarea_lat_seconds_count' 'devicechain_selftestarea_test_only_seconds_count' '_count on a histogram registered only in a test file'
  histcase '_lat_seconds_bucket"' '_lat_seconds"' 'a by-name selector over the bare name of a histogram' "$tmpl/prometheusrule-probe.yaml"
  histcase '_lat_seconds_bucket"' '_lat_seconds_buckets"' 'a by-name selector with a misspelled histogram suffix' "$tmpl/prometheusrule-probe.yaml"

  # Cases 2z -- ONE ABSENT TERM EMPTIES A SUM. The clean fixture carries a sum whose two
  # counter terms are each guarded (dashboard and rule), and two bare sums marked
  # `# absent-ok`, and Case 0 passed over all of them (the accepted half). Each defect
  # below is planted ALONE by rewriting the guarded sum.
  absentcase() { # <file> <old> <new> <description>
    python3 - "$1" "$2" "$3" <<'PY' || fail "the mutation for $4 did not apply"
import sys
path, old, new = sys.argv[1:]
text = open(path, encoding="utf-8").read()
if text.count(old) != 1:
    sys.exit(1)
open(path, "w", encoding="utf-8").write(text.replace(old, new))
PY
    if check_content "$work" >/dev/null 2>&1; then
      fail "did not flag $4"
    fi
    restore
    echo "  ok: $4 is caught"
  }
  G1='(sum(increase(devicechain_selftestarea_probe_total{namespace=\"$namespace\"}[1h])) or vector(0))'
  absentcase "$dash/probe.json" "$G1 + (sum" \
    'sum(increase(devicechain_selftestarea_probe_total{namespace=\"$namespace\"}[1h])) + (sum' \
    'a dashboard sum with one unguarded counter term'
  absentcase "$dash/probe.json" \
    "(sum(increase(devicechain_selftestarea_other_probe_total{namespace=\\\"\$namespace\\\"}[1h])) or vector(0))\"" \
    "sum(increase(devicechain_selftestarea_other_probe_total{namespace=\\\"\$namespace\\\"}[1h])) or vector(0)\"" \
    'a dashboard sum whose only fallback is written once, at the end'
  absentcase "$dash/probe.json" "$G1 + (sum(increase(devicechain_selftestarea_other_probe_total{namespace=\\\"\$namespace\\\"}[1h])) or vector(0))" \
    'sum(increase(devicechain_selftestarea_probe_total{namespace=\"$namespace\"}[1h]) + increase(devicechain_selftestarea_other_probe_total{namespace=\"$namespace\"}[1h]))' \
    'a dashboard sum that adds two counters inside one aggregation'
  # Case 2y -- A WHOLE-NUMBER QUANTILE MATCHED WITH `=`, alone: the probe board already selects
  # quantile="0.99" (accepted); rewriting it to "1" is exactly the Platform board's defect.
  absentcase "$dash/probe.json" 'sum_seconds{quantile=\"0.99\"}' 'sum_seconds{quantile=\"1\"}' \
    'a summary quantile of exactly 1 matched with ='
  absentcase "$tmpl/prometheusrule-probe.yaml" \
    '(sum(increase(devicechain_selftestarea_probe_total{namespace="dc"}[5m])) or vector(0)) + (' \
    'sum(increase(devicechain_selftestarea_probe_total{namespace="dc"}[5m])) + (' \
    'an alert expression with one unguarded counter term'

  # Cases 2w -- A BACKLOG ONLY THE READER EXPORTS. The clean fixture carries the reader's gauge
  # with its broker arm (dashboard and rule) and once more marked `# reader-only`, and Case 0
  # passed over all of them (the accepted half). Each defect below is planted ALONE.
  absentcase "$dash/probe.json" ' or max(nats_consumer_num_pending{namespace=\"$namespace\", consumer_name=~\".+_self-test-area_.+\"})' ''     'a dashboard backlog target with no broker arm'
  absentcase "$dash/probe.json" 'nats_consumer_num_pending{namespace=\"$namespace\", consumer_name=~\".+_self-test-area_.+\"}' 'nats_consumer_num_ack_pending{namespace=\"$namespace\", consumer_name=~\".+_self-test-area_.+\"}'     'a dashboard backlog target whose broker arm is the wrong kind (ack-pending for pending)'
  absentcase "$dash/probe.json" '{namespace=\"$namespace\"}) # reader-only: fixture needs a counter only the reader has' '{namespace=\"$namespace\"})'     'a dashboard backlog target that lost its reader-only marker'
  absentcase "$tmpl/prometheusrule-probe.yaml" ' or max(nats_consumer_num_pending{consumer_name=~".+_self-test-area_.+"})' ''     'an alert over a reader-side backlog with no broker arm'

  # ...and the arm must WORK, not merely exist: same area as the reader, labels the broker exporter
  # sets, on every reader selector, and a line beside the reader counts only for its own area.
  absentcase "$dash/probe.json" 'consumer_name=~\".+_self-test-area_.+\"})"' 'consumer_name=~\".+_self-tset-area_.+\"})"' \
    'a dashboard broker arm naming a misspelled area'
  absentcase "$dash/probe.json" '{namespace=\"$namespace\", consumer_name=~\".+_self-test-area_.+\"})"' '{stream=~\"x\", consumer_name=~\".+_self-test-area_.+\"})"' \
    'a dashboard broker arm keyed on a label the broker does not set (stream)'
  absentcase "$dash/probe.json" 'consumer_name=~\".+_self-test-area_.+\"})"' 'consumer_name=~\".+_self-test-area_.+\"}) + max(devicechain_selftestarea_detect_consumer_pending{namespace=\"$namespace\"})"' \
    'a dashboard with a second reader selector the arm does not cover'
  absentcase "$dash/probe.json" 'consumer_name=~\".+_self-test-area_.*\"})"' 'consumer_name=~\".+_other-area_.*\"})"' \
    "a reader line drawn beside another area's broker line"
  absentcase "$tmpl/prometheusrule-probe.yaml" 'consumer_name=~".+_self-test-area_.+"})) > 100' 'consumer_name=~".+_self-tset-area_.+"})) > 100' \
    'an alert whose broker arm names a misspelled area'

  # Cases 2x -- SERIES THAT ARE NOT DEVICECHAIN'S, one misspelling per prefix family, each alone.
  # The clean fixture names one pinned series of every family and passed Case 0, so the accepted half
  # is proven; each defect below drops a letter from a real name.
  histcase 'kube_pod_container_status_restarts_total' 'kube_pod_container_status_restart_total' 'a misspelled kube-state-metrics series (kube_*)'
  histcase 'container_memory_working_set_bytes' 'container_memory_working_set_byte' 'a misspelled cAdvisor series (container_*)'
  histcase 'go_goroutines' 'go_goroutine' 'a misspelled Go runtime series (go_*)'
  histcase 'process_open_fds' 'process_open_fd' 'a misspelled process series (process_*)'
  histcase 'nats_varz_cpu' 'nats_varz_cpuu' 'a misspelled broker exporter series (nats_*)'
  histcase 'nats_varz_cpu' 'nats_varz_cpu_percent' 'a broker exporter series with a suffix the exporter does not export'
  histcase 'cnpg_collector_up' 'cnpg_collector_upp' 'a misspelled database operator series (cnpg_*)'
  histcase 'kubelet_volume_stats_used_bytes' 'kubelet_volume_stats_used_byte' 'a misspelled kubelet series (kubelet_*)'
  histcase 'barman_cloud_cloudnative_pg_io_last_available_backup_timestamp' 'barman_cloud_cloudnative_pg_io_last_available_backup_timestam' 'a misspelled backup plugin series (barman_*)'

  # ...and in an ALERT RULE, in both spellings of `expr:` (inline, and a block scalar), where a
  # misspelling is worse than an empty graph: the rule never fires and never complains.
  histcase 'kube_pod_status_ready' 'kube_pod_status_redy' 'a misspelled external series in an inline alert expression' "$tmpl/prometheusrule-probe.yaml"
  histcase 'cnpg_pg_database_size_bytes' 'cnpg_pg_database_size_byts' 'a misspelled external series in a block-scalar alert expression' "$tmpl/prometheusrule-probe.yaml"

  # Cases 2p — INTERNAL REFERENCES IN PUBLISHED BOARD TEXT, each alone. The board
  # text is screenshotted onto the public docs site, so an ADR number in a title, a
  # description, a legend or a row title is a dead citation. The probe is given the
  # field under test by rewriting the pristine fixture, so the clean fixture (Case 0)
  # is the counterweight: the same fields, free of references, pass.
  textcase() { # <python expression that mutates board> <description>
    python3 - "$tmp/probe.json" "$dash/probe.json" "$1" <<'FIXTURE' || fail "the mutation for $2 did not apply"
import json, sys
board = json.load(open(sys.argv[1]))
exec(sys.argv[3])
json.dump(board, open(sys.argv[2], "w"), indent=2)
FIXTURE
    if check_content "$work" >/dev/null 2>&1; then
      fail "did not flag $2"
    fi
    restore
    echo "  ok: $2 is caught"
  }
  # A clean board with every published-text field populated must still pass.
  python3 - "$tmp/probe.json" "$dash/probe.json" <<'FIXTURE' || fail "the clean-text fixture could not be built"
import json, sys
board = json.load(open(sys.argv[1]))
board["description"] = "Plain operator language."
board["panels"].append({"type": "row", "title": "A row", "panels": []})
board["panels"][1]["title"] = "A panel"
board["panels"][1]["description"] = "A description."
board["panels"][1]["targets"][0]["legendFormat"] = "{{reason}}"
json.dump(board, open(sys.argv[2], "w"), indent=2)
FIXTURE
  check_content "$work" >/dev/null || fail "a board with plain text in every published field was reported as broken"
  restore
  textcase 'board["title"] = "DeviceChain — Probe (ADR-023)"' 'an ADR reference in a board title'
  textcase 'board["description"] = "see ADR-051 for the thread"' 'an ADR reference in a board description'
  textcase 'board["panels"].append({"type": "row", "title": "State budget (ADR-023)", "panels": []})' 'an ADR reference in a row title'
  textcase 'board["panels"][1]["targets"][0]["legendFormat"] = "{{reason}} ADR-057"' 'an ADR reference in a legend'
  textcase 'board["panels"][1]["description"] = "superseded (ADR-057 D6)"' 'an ADR reference in a panel description'
  textcase 'board["panels"][1]["description"] = "\U0001F534 look here first"' 'the red-circle marker in a panel description'

  # Case 2b — THE BOARD CONVENTION, each rule broken alone. These start from the
  # clean fixture (which satisfies the convention, so the clean-tree case above
  # already proves the rules do not fire on a conforming board).
  textcase 'board["uid"] = "dc-other-ops"' 'a board uid that does not follow its file name'
  textcase 'board["title"] = "Probe"' 'a board title without the DeviceChain prefix'
  textcase 'board["tags"] = []' 'a board without the devicechain tag'
  textcase 'board["templating"]["list"] = [v for v in board["templating"]["list"] if v["name"] != "instance"]' 'a board with no instance variable'
  textcase 'board["templating"]["list"] = [v for v in board["templating"]["list"] if v["name"] != "namespace"]' 'a board with no namespace variable'
  textcase 'del board["panels"][0]' 'a board with no firing-alerts table'
  textcase 'board["panels"][0]["gridPos"]["y"] = 5' 'a firing-alerts table that is not at the top'
  textcase 'board["panels"][0]["targets"][0]["expr"] = board["panels"][0]["targets"][0]["expr"].replace("alertstate=\"firing\", ", "")' 'a firing-alerts table that also lists pending alerts'
  textcase 'board["panels"][1]["targets"][0]["expr"] = "sum(ALERTS{devicechain_instance=\"prod\"})"' 'an alert selector with a literal instance ID'
  textcase 'board["panels"][1]["links"] = [{"url": "/d/dc-0123456789abcdef0123"}]' 'a link to a literal board uid'

  # Case 2c — THE BOARD LIST, both directions, and an empty list. The failure
  # message names which side is missing, so each case checks the message too.
  bl="$tmp/boardlist"
  mkdir -p "$bl"
  : >"$bl/alpha.json"
  : >"$bl/beta.json"
  check_board_list "$bl" alpha beta >/dev/null 2>&1 || fail "a board list matching the files was refused"
  blcase() { # <expected code> <description> <board>...
    local code="$1" desc="$2" out rc=0
    shift 2
    out="$(check_board_list "$bl" "$@" 2>&1)" || rc=$?
    [ "$rc" -ne 0 ] || fail "did not flag $desc"
    grep -q "^$code" <<<"$out" || { echo "$out" >&2; fail "flagged $desc, but not as $code"; }
    echo "  ok: $desc is caught ($code)"
  }
  blcase BOARD-UNLISTED 'a board file the list does not name' alpha
  blcase BOARD-MISSING 'a listed board with no file' alpha beta gamma
  blcase FAIL 'an empty board list'

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

  # Case 6b — A MISSPELLED SUFFIX IN A BY-NAME SELECTOR, alone.
  # 🔴 THIS ONE WAS SKIPPED IN SILENCE BEFORE THE CHECK WAS TAUGHT TO READ IT. The
  # character after `devicechain_` is a regex metacharacter, so the literal-series
  # pattern never matched the selector at all and a typo in its suffix read green —
  # over an alert that then selects nothing and never fires.
  sed -i 's/devicechain_\[a-z0-9\]+_probe_total/devicechain_[a-z0-9]+_prbe_total/' "$tmpl/prometheusrule-probe.yaml"
  grep -qF 'devicechain_[a-z0-9]+_prbe_total' "$tmpl/prometheusrule-probe.yaml" ||
    fail "the by-name selector mutation did not apply"
  if check_content "$work" >/dev/null 2>&1; then
    fail "did not flag a misspelled suffix in a by-name selector"
  fi
  restore
  echo "  ok: a misspelled suffix in a by-name selector is caught"

  # Case 6c — A BY-NAME SELECTOR IN A FORM THE CHECK DOES NOT RESOLVE, alone. A
  # suffix it cannot parse out is one it cannot vouch for, so the form is refused
  # rather than skipped: `.*` would pass here otherwise, typo or not.
  sed -i 's/devicechain_\[a-z0-9\]+_probe_total/devicechain_.*_probe_total/' "$tmpl/prometheusrule-probe.yaml"
  grep -qF 'devicechain_.*_probe_total' "$tmpl/prometheusrule-probe.yaml" ||
    fail "the unresolvable-form mutation did not apply"
  if check_content "$work" >/dev/null 2>&1; then
    fail "did not flag a by-name selector in a form the check cannot resolve"
  fi
  restore
  echo "  ok: a by-name selector in an unresolvable form is refused"

  # Case 6d — CORE-REGISTERED HISTOGRAMS, each defect alone. The clean fixture names
  # core_lat_seconds_bucket by name and passed Case 0, so the accepted half is proven.
  histcase 'core_lat_seconds_bucket' 'core_lat_seconds' 'the bare name of a histogram registered in core'
  histcase 'core_lat_seconds_bucket' 'core_lat_seconds_buckets' 'a misspelled suffix on a core histogram'
  histcase 'core_lat_seconds_bucket' 'core_lat_secnds_bucket' 'a core histogram suffix on a base that is not registered'

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
  printf '%s\n' '{"title": "scratch", "uid": "scratch", "panels": [], "templating": {"list": [{"name": "namespace", "type": "constant", "query": ""}, {"name": "instance", "type": "constant", "query": ""}]}}' \
    >"$chart/dashboards/scratch-board.json"
  expect_configmaps NAME-UNPINNED "a dashboard whose ConfigMap name is not pinned"
  rm -f "$chart/dashboards/scratch-board.json"

  # Cases 9b-9e — BOARD-TO-BOARD REFERENCES. A board names another as
  # @board:<stem>@ and the chart resolves it per instance. The scratch board is
  # unpinned (NAME-UNPINNED is expected and beside the point), so each case asserts
  # the presence or absence of the LINK-* code it is about.
  scratch_board() { # <link url>
    printf '%s\n' "{\"title\": \"scratch\", \"uid\": \"scratch\", \"panels\": [{\"id\": 1, \"type\": \"text\", \"links\": [{\"url\": \"$1\"}]}], \"templating\": {\"list\": [{\"name\": \"namespace\", \"type\": \"constant\", \"query\": \"\"}, {\"name\": \"instance\", \"type\": \"constant\", \"query\": \"\"}]}}" \
      >"$chart/dashboards/scratch-board.json"
  }
  scratch_board "/d/dc-00000000000000000000/x"
  expect_configmaps LINK-FOREIGN "a board linking to a literal uid"
  scratch_board "/d/@board:event-processing@/x"
  out="$(check_configmaps "$chart" 2>&1)" || true
  grep -q -- "- LINK" <<<"$out" && { echo "$out" >&2; fail "flagged a correctly written board reference"; }
  echo "  ok: a correctly written @board:<stem>@ reference passes"
  scratch_board "/d/@board:no-such-board@/x"
  missing_rc=0
  missing_err="$(helm template dc "$chart" \
    --set "instance.config.infrastructure.secrets.rootKey=$(openssl rand -base64 32)" 2>&1 >/dev/null)" || missing_rc=$?
  [ "$missing_rc" -ne 0 ] || fail "rendered a reference to a board that does not exist"
  grep -qF 'references a board that does not exist' <<<"$missing_err" || {
    echo "$missing_err" >&2
    fail "the render of an unresolved board reference failed, but not with the template's own refusal"
  }
  rm -f "$chart/dashboards/scratch-board.json"
  echo "  ok: a reference to a board that does not exist is refused by the template"

  # Cases 9f-9h — THE PER-INSTANCE VARIABLE, TAG AND LINK, each broken alone.
  plant "$tpl" '"label" "Instance ID" "query" $id' '"label" "Instance ID" "query" $ns'
  expect_configmaps INSTANCE "an instance variable that holds the namespace instead of the instance ID"
  plant "$tpl" '(sha256sum $id | trunc 20)' '(sha256sum "shared" | trunc 20)'
  expect_configmaps DUPLICATE-TAG "one per-instance tag shared across instances"
  plant "$tpl" '"tags" (list $tag) "asDropdown"' '"tags" (list "dc-shared") "asDropdown"'
  expect_configmaps LINK "a boards link that lists a tag other than the instance's own"

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

  # Case 17 — A BOARD SCOPED TO THE INSTANCE ID INSTEAD OF ITS NAMESPACE, alone.
  #
  # 🔴 THIS IS THE CHECK'S OWN OLD PREMISE, PLANTED BACK. Until an instance's namespace
  # gained its `dci-` prefix the id and the namespace were one string, and the assertion
  # above was written against the id — so the two spellings parting company is the one
  # regression the previous version of this case structurally could not see. The board
  # still has exactly one hidden constant `namespace` variable, its uid, title, folder,
  # ConfigMap name and data key are all untouched, and every panel's
  # namespace="$namespace" filter now names a namespace nothing runs in: the board is
  # empty, for ever, silently. Only comparing against the render's own Deployments
  # catches it, which is what this case is here to prove is what happens.
  plant "$tpl" '{{- $ns := include "devicechain.instanceNamespace" . }}' '{{- $ns := $id }}'
  expect_configmaps NAMESPACE "a board scoped to the instance id rather than to its namespace"

  # Case 18 — EVERY INSTANCE IN ONE NAMESPACE, alone, and it is the case the per-board
  # assertion cannot take. The namespace helper stops varying by instance, so both
  # renders put their workloads in one namespace AND both boards name it — each board
  # still agrees with its own render, so every per-instance check holds. What is wrong
  # is only visible ACROSS the two: both instances' pods emit series carrying the same
  # namespace label, so each instance's boards read the other's data. Same argument as
  # DUPLICATE-UID and DUPLICATE-KEY, on the axis this prefix introduced.
  #
  # It mutates _helpers.tpl rather than $tpl, so it keeps and restores that file itself;
  # expect_configmaps only puts $tpl back.
  helpers="$chart/templates/_helpers.tpl"
  cp "$helpers" "$tmp/keep-helpers.tpl"
  plant "$helpers" '{{- printf "dci-%s" .Values.instance.id -}}' '{{- printf "dci-shared" -}}'
  expect_configmaps SHARED-NAMESPACE "one namespace shared by every instance"
  cp "$tmp/keep-helpers.tpl" "$helpers"
  # The restore is load-bearing: everything after this renders the chart, and a helper
  # left mutated would make the next case pass or fail for a reason that is not its own.
  check_configmaps "$chart" >/dev/null ||
    fail "the chart did not pass again after restoring _helpers.tpl, so every case below
would be measuring the leftover mutation rather than its own"

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

  # The same for the `instance` variable: without it the firing-alerts table selects
  # nothing, so the render refuses rather than shipping a table that is always empty.
  python3 - "$chart/dashboards/command-delivery.json" "$chart/dashboards/unscoped-board.json" <<'FIXTURE' ||
import json, sys
board = json.load(open(sys.argv[1]))
before = len(board["templating"]["list"])
board["templating"]["list"] = [v for v in board["templating"]["list"] if v.get("name") != "instance"]
if len(board["templating"]["list"]) != before - 1:
    sys.exit(1)
json.dump(board, open(sys.argv[2], "w"), indent=2)
FIXTURE
    fail "the instance-less fixture could not be built"
  noinst_rc=0
  noinst_err="$(helm template dc "$chart" \
    --set "instance.config.infrastructure.secrets.rootKey=$(openssl rand -base64 32)" 2>&1 >/dev/null)" || noinst_rc=$?
  [ "$noinst_rc" -ne 0 ] || fail "rendered a dashboard that has no instance variable"
  grep -qF 'dashboard dashboards/unscoped-board.json has no "instance" template variable' <<<"$noinst_err" || {
    echo "$noinst_err" >&2
    fail "the render of an instance-less dashboard failed, but not with the template's own refusal"
  }
  rm -f "$chart/dashboards/unscoped-board.json"
  echo "  ok: a dashboard with no instance variable is refused by the template"

  echo "self-test passed: 86 defects, each planted alone, each caught; a clean tree and chart pass"
  exit 0
fi

say "board list: every dashboards/*.json is named in BOARDS, and every name has its file"
check_board_list "$ROOT/deploy/helm/devicechain/dashboards" "${BOARDS[@]}"

say "parsing the chart's Grafana dashboards and resolving their series"
check_content "$ROOT"

say "rendering two instances: pinned names and keys, and nothing the two share"
check_configmaps "$ROOT/deploy/helm/devicechain"

note "dashboards parse, every series they and the alert rules name is registered, names and keys are pinned, instances share no board"
