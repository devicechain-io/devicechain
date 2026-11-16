#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""CI leg selection: which Go modules, test packages and integration legs a pull request runs.

    hack/ci-select.py plan      # in `discover`: print the go legs, integration legs and frontend verdict
    hack/ci-select.py check     # the reader table and split table still describe the tree
    hack/ci-select.py replay SHA...   # what a merged commit would have selected (no CI env needed)
    hack/ci-select.py --self-test     # the selection's safety rails, each against a case that must trip it

WHAT IS SELECTED, AND WHY IT IS SAFE TO SKIP THE REST
-----------------------------------------------------
A pull request runs a Go module's whole job (fmt, build, vet, tidy, govulncheck, test, race) when
the module's own files changed, or when a module it requires (through go.mod) did, transitively.
The require graph is read from the go.mod files themselves (`go mod edit -json`), never from a
list here. It is complete for imports because the `go.mod and go.sum are tidy` step resolves each
module with GOWORK=off: a workspace module importing another without requiring it fails there.

Imports are not the only edge. Many tests READ files outside their own module: the served GraphQL
SDL, the chart, the OpenTofu tree, the published docs, another module's source. Go's own graph
cannot see those, so they are written down in READERS below, one row per (module, package): when
a changed file matches a row's glob, that PACKAGE's tests run (test and race), and nothing else of
the module, because nothing else of it could have changed its result. `check` refuses a module
whose Go files reach outside it (a ".." path, a go.work or git-root lookup, runtime.Caller) from a
package that has no row and no written reason, so a new reader cannot be added silently.

FAIL SAFE, NOT FAIL QUIET
-------------------------
Everything runs when: the event is not a pull_request (push to main, merge_group, schedule,
workflow_dispatch), go.work / go.work.sum / .github / hack changed, a changed file sits outside
every module and matches neither a READERS row nor an INERT glob, the diff cannot be computed or
is empty, or the selection raises. Each of those prints the reason it ran everything. A module that
is skipped is printed with the reason it was skipped. Nothing is capped.

SPLITS
------
A few packages are long enough that one leg per module would set the run's critical path (core's
messaging package alone is ~36 minutes of test + race). SPLITS gives such a package its own legs,
its top-level tests dealt round-robin over N shards by hack/go-leg.sh; the rest of the module runs
in one catch-all leg that also carries the module's static checks. The catch-all is `go list ./...`
MINUS the split packages, computed in the leg, so a new package lands there by construction.
"""
import fnmatch
import json
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# ---------------------------------------------------------------------------------------------
# Paths whose change runs EVERYTHING.
# ---------------------------------------------------------------------------------------------
# go.work / go.work.sum: the workspace itself (which modules exist, which versions build).
# .github/**: the workflows that decide what runs, and files tests read (ci.yml, release.yml,
#   release-highlights.json).
# hack/**: the scripts the go jobs call (go-mod-download, go-race, govulncheck, the tidy and
#   workspace-sum checks, this file, go-leg.sh) and files tests read (hack/vocab/sources.lock,
#   hack/dr-rig.sh, hack/verify-release-signatures.sh, every hack/ path a published doc quotes).
FULL_TRIGGERS = [
    "go.work",
    "go.work.sum",
    ".github/**",
    "hack/**",
]

# ---------------------------------------------------------------------------------------------
# Paths outside every module that NO Go test reads, except through a READERS row below. A file
# outside every module that matches no READERS row and no INERT glob runs everything.
# ---------------------------------------------------------------------------------------------
INERT = [
    "docs/**",
    "frontend/**",
    "sdks/**",
    "demos/**",
    "_legacy/**",
    "backend/testdata/**",
    "*.md",
    "LICENSE",
    "NOTICE",
    ".gitignore",
    ".gitattributes",
    ".editorconfig",
]

# ---------------------------------------------------------------------------------------------
# READERS: (module, package dir relative to the module, [globs read], [globs excluded], why).
# ---------------------------------------------------------------------------------------------
# Globs are repo-relative; `**` crosses directories, `*` does not. A row's package runs when a
# changed file matches one of its globs and none of its exclusions. Rows for a module whose own
# code changed are subsumed by its full run. Evidence is file:line in the reading package.
NONTEST = ["**/*_test.go"]
SDL = "backend/services/*/graphql/**"
DOCS_DEPLOY = [
    "docs/docs/deployment/**",
    "docs/i18n/*/docusaurus-plugin-content-docs/current/deployment/**",
]
READERS = [
    # build_paths_test.go:22-81 — the reproducible-build settings in every place the broker image is built.
    ("backend/broker/dc-nats-server", ".", [
        "deploy/local/build-images.sh",
        "backend/cli/bootstrap/brokerimage.go",
        "backend/cli/bootstrap/steps.go",
    ], [], "build_paths_test.go"),
    # cmd/docs_commands_test.go (every dcctl command in the published docs, every locale; README.md;
    # go.mod/dirs named by doc `cd` lines), docs_graphql_commands_test.go (SDL), docs_sql_commands_test.go.
    ("backend/cli", "cmd", [
        "docs/**", "README.md", "**/go.mod", SDL,
        "backend/services/event-management/model/analytics.go",
    ], [], "cmd/docs_*_test.go"),
    # bootstrap: deployment docs (fence/backupsizing/diskbudget/size_docs), every service's non-test
    # source (install_test.go:432), service configs (sizing/sparkplug), user-management go.mod
    # (breakglass), core/rdb/postgres.go (install_test.go:473), the broker's main.go/go.mod
    # (brokerimage_test via kube.go repoRoot). deploy/** is the deploy module, which cli requires.
    ("backend/cli", "bootstrap", DOCS_DEPLOY + [
        "backend/services/**/*.go",
        "backend/services/user-management/go.mod",
        "backend/core/rdb/postgres.go",
        "backend/broker/dc-nats-server/main.go",
        "backend/broker/dc-nats-server/go.mod",
    ], NONTEST, "bootstrap/*_test.go"),
    ("backend/cli", "attr", ["backend/services/device-management/graphql/**"], [], "attr/documents_test.go:38"),
    ("backend/cli", "product", [SDL], [], "product/documents_test.go:32"),
    ("backend/cli", "failedevents", ["backend/services/event-management/graphql/**"], [], "failedevents/*_test.go"),
    ("backend/cli", "sim", ["backend/sims/dc-simulator/sim/handshake.go"], [], "sim/handshake_lockstep_test.go:30"),
    # core/test walks every .go file in the repository (logsink, defaultorder, sharedcache, ...).
    ("backend/core", "test", ["**/*.go", "go.work"], [], "test/*_repo_test.go"),
    # core/test/frontend runs vitest in frontend/packages/widgets and frontend/apps/console.
    ("backend/core", "test/frontend", ["frontend/**"], [], "test/frontend/state_test.go:24"),
    ("backend/core", "backupset", [
        "backend/**/*.go",
        "docs/**/disaster-recovery.md",
    ], NONTEST, "backupset/backupset_test.go:120,248"),
    ("backend/core", "graphql/schemaplane", [SDL], [], "graphql/schemaplane/schemaplane_test.go:19"),
    ("backend/core", "rdb/partialupdatetest", ["backend/services/*/model/*.go"], NONTEST, "rdb/partialupdatetest/savescan_test.go:133"),
    ("backend/core", "config", ["deploy/opentofu/modules/traefik/main.tf"], [], "config/edge_timeout_lockstep_test.go:24"),
    ("backend/core", "natsauth", ["deploy/opentofu/modules/nats/main.tf"], [], "natsauth/identity_account_test.go:241"),
    ("backend/core", "messaging", [
        "deploy/helm/devicechain/templates/prometheusrule-broker.yaml",
        "deploy/opentofu/instance/variables.tf",
        "deploy/opentofu/modules/nats/main.tf",
    ], [], "messaging/rebalanceable_streams_test.go:32, dedup_window_test.go:443"),
    ("backend/core", "semantic", ["NOTICE"], [], "semantic/semantic_test.go:487"),
    # functionalarea: the services list, release.yml's ko modules and their RUNBOOK.md, the operator,
    # the chart helpers and Chart.yaml, the local image build, the nats module, the broker image.
    ("backend/k8s", "functionalarea", [
        "backend/services/*/main.go",
        "backend/cli/operator/operator.go",
        "backend/cli/bootstrap/brokerimage.go",
        "deploy/helm/devicechain/templates/_helpers.tpl",
        "deploy/helm/devicechain/Chart.yaml",
        "deploy/local/build-images.sh",
        "deploy/opentofu/modules/nats/main.tf",
        "**/RUNBOOK.md",
    ], [], "functionalarea/release_*contract_test.go"),
    ("backend/k8s", "controllers", ["deploy/opentofu/modules/cnpg-cluster/chart/**"], [], "controllers/snapshotretention_helm_test.go:31"),
    ("backend/services/command-delivery", ".", [
        "frontend/apps/console/src/routes/command-batches/CreateBatchForm.tsx",
        "frontend/apps/console/src/routes/devices/DeviceCommandsPanel.tsx",
        "deploy/helm/devicechain/dashboards/command-delivery.json",
    ], [], "console_submission_contract_test.go:17, dashboard_series_test.go:43"),
    ("backend/services/command-delivery", "model", ["backend/testdata/authored-rules/**"], [], "model/authored_sim_command_wire_test.go:30"),
    ("backend/services/device-management", "config", ["deploy/helm/devicechain/values.yaml"], [], "config/chart_floor_test.go:25"),
    ("backend/services/device-management", "model", [
        "backend/testdata/authored-rules/**",
        "backend/sims/dc-simulator/sim/fleetpulse_speed_limit_schema.json",
    ], [], "model/authored_sim_*_test.go"),
    ("backend/services/device-management", "processor", ["frontend/testdata/connect-snippets/**"], [], "processor/connect_snippet_golden_test.go:32"),
    ("backend/services/event-management", "model", ["docs/**/releases-and-upgrades.md"], [], "model/upgrade_notes_bounds_test.go:34"),
    ("backend/services/event-management", "processor/backfill", ["backend/services/device-management/graphql/**"], [], "processor/backfill/*_test.go"),
    ("backend/services/event-processing", "graphql", ["backend/testdata/authored-rules/**"], [], "graphql/authored_sim_rules_test.go:39"),
    # event-sources/processor walks every non-test .go file under backend/.
    ("backend/services/event-sources", "processor", [
        "backend/**/*.go",
        "frontend/testdata/connect-snippets/**",
        "backend/core/integrationcodec/testdata/**",
    ], [], "processor/broker_assertion_test.go:218, integration_marks_test.go:159"),
    ("backend/services/event-sources", "integration", ["backend/services/device-management/graphql/**"], [], "integration/backend_test.go:32"),
    ("backend/services/mcp", "server", [SDL], [], "server/documents_test.go:35"),
    ("backend/services/sparkplug-ingest", "downlink", ["frontend/apps/console/src/routes/devices/sparkplugCommands.ts"], [], "downlink/reasons_test.go:89"),
    ("backend/services/update-management", "model", ["deploy/opentofu/modules/traefik/**"], [], "model/edge_lockstep_test.go:31"),
    ("backend/services/user-management", "basemap", ["frontend/apps/console/src/components/basemap/catalog.json"], [], "basemap/catalog_test.go:32"),
    # loadtest/documents_test.go reads the served GraphQL SDL; the authored-rules fixture.
    ("backend/sims/dc-simulator", "loadtest", [SDL, "backend/testdata/authored-rules/**"], [], "loadtest/*_test.go"),
    ("backend/sims/dc-simulator", "sim", [
        SDL,
        "frontend/packages/widgets/src/builtin-descriptors.ts",
        "frontend/packages/dashboards/src/types.ts",
        "frontend/testdata/**",
        "backend/cli/sim/*.go",
        "backend/services/event-processing/internal/review/testdata/**",
        "demos/**",
    ], [], "sim/*_test.go"),
    ("backend/sims/dc-simulator", "updater", ["backend/core/ota/testdata/**"], [], "updater/*_test.go"),
    ("backend/tools/apiprobe", ".", [SDL], [], "baseline_test.go, readsweep_test.go, documents_test.go"),
    ("backend/tools/docsgql", ".", ["docs/**", SDL], [], "docsgql_test.go:241"),
    ("backend/tools/docsgql", "decoderfuncs", ["docs/**/decoder-functions.md"], [], "decoderfuncs/decoderfuncs_test.go:19"),
    ("backend/tools/docsgql", "sdlcompat", [SDL], [], "sdlcompat/sdlcompat_test.go:17"),
    ("backend/tools/docsgql", "sdldesc", [SDL, "backend/services/**/*.go"], [], "sdldesc/sdldesc_test.go:18,241"),
    ("backend/tools/docsgql", "apicompat/jsonschema", ["backend/k8s/config/crd/bases/**"], [], "apicompat/jsonschema/jsonschema_test.go:239"),
    # export runs `go test` in backend/core/natsauth through go.work.
    ("backend/tools/docsgql", "apicompat/export", ["backend/core/**", "go.work"], [], "apicompat/export/export.go:46"),
    ("backend/tools/playground", ".", [
        "backend/cli/sim/record.go",
        "backend/cli/deletions/deletions.go",
        "backend/cli/cmd/*.go",
        "deploy/playground/**",
    ], [], "playground_test.go:507-597"),
    ("backend/tools/rdbguard", ".", [
        "backend/core/**/*.go",
        "backend/services/**/*.go",
        "backend/tools/migrationdiff/golden/**",
    ], [], "rawsql_test.go:422-513"),
    ("examples/ota-device", "internal/device", ["backend/core/ota/testdata/**"], [], "internal/device/device_test.go:588"),
    ("examples/ota-device", "internal/verify", ["backend/core/ota/testdata/**"], [], "internal/verify/verify_test.go:100"),
    ("examples/ota-device", "internal/wire", ["backend/core/ota/testdata/**"], [], "internal/wire/conformance_test.go:21"),
]

# Packages whose Go files LOOK like they reach outside the module (a ".." path, runtime.Caller, a
# go.work lookup) but do not, or do so only at run time and never from a test. `check` accepts
# these without a READERS row; each needs its reason.
IN_MODULE = {
    ("backend/cli", "backup"): "path-traversal fixtures (\"../outside\") and in-module testdata",
    ("backend/cli", "dcdir"): "walks its own module; \"../escape\" is a refused-input fixture",
    ("backend/cli", "internal/kubeisolation"): "walks its own module",
    ("backend/cli", "operator"): "runtime-only repo lookup for --build, not exercised by tests",
    ("backend/core", "blob"): "\"../../../etc/passwd\" is a refused-input fixture",
    ("backend/core", "httptransport"): "walks its own module",
    ("backend/core", "resolvedevent"): "reads ../presence inside backend/core",
    ("backend/core", "ruletest"): "\"../x\" is a refused-input fixture",
    ("backend/edge/dc-edge-agent", "agent"): "reads ../dashboards inside its own module",
    ("backend/edge/dc-edge-agent", "."): "reads its own dashboards dir",
    ("backend/services/event-management", "processor"): "\"../../model\" is inside its own module",
    ("backend/services/event-processing", "internal/nldraft"): "reads inside its own module",
    ("backend/services/event-processing", "internal/nightshift"): "runtime.Caller root is its own module",
    ("backend/services/event-processing", "internal/rules"): "\"../../etc/passwd\" and ../replayinput are fixtures/in-module",
    ("backend/services/user-management", "identity"): "\"../\" is a refused-redirect fixture",
    ("backend/sims/dc-simulator", "cmd/nightshift-capture"): "a generator run by hand, never by a test",
    ("backend/tools/docsgql", "cmd/*"): "command defaults (--head ../../..), run by hand or by api-compat, not by tests",
    ("backend/tools/vocabgen", "."): "hack/vocab/sources.lock (hack/** runs everything)",
    ("backend/core", "secrets"): "hack/dr-rig.sh (hack/** runs everything)",
    ("backend/tools/redlines", "."): "temp fixtures only",
    ("examples/ota-device", "internal/testutil"): "runtime.Caller root is its own module",
    ("backend/core", "core"): "\"..\" appears only in a comment about os.ReadDir",
    ("backend/core", "replication"): "\"..\" appears only in a comment",
    ("backend/core", "webhooksig"): "\"..\" is a refused path segment",
    ("backend/k8s", "api/v1beta1"): "reads ../../config/crd inside backend/k8s",
    ("backend/services/command-delivery", "processor"): "reads ../contract inside its own module",
    ("backend/services/event-processing", "internal/review"): "reads ../replayinput inside its own module",
    ("backend/services/device-management", "graphql"): "\"../x\" is a refused-key fixture",
    ("backend/services/outbound-connectors", "lpwan"): "\"../etc/passwd\" and \"../x\" are refused-id fixtures",
    ("deploy", "."):"\"../modules/<x>\" appears only in comments; the embed is in-module",
}

# ---------------------------------------------------------------------------------------------
# SPLITS: module -> {package dir: shard count}. Measured on run 43945176007 (test + race, seconds):
#   core/messaging 1014+1136; device-management/processor 549+1270, /model 57+872;
#   cli/bootstrap 125+1147; user-management/identity 197+1258; event-processing/internal/detect/core 82+646.
# ---------------------------------------------------------------------------------------------
SPLITS = {
    "backend/core": {"messaging": 3},
    "backend/services/device-management": {"processor": 3, "model": 2},
    "backend/cli": {"bootstrap": 2},
    "backend/services/user-management": {"identity": 2},
    "backend/services/event-processing": {"internal/detect/core": 2},
}

ESCAPE_RE = re.compile(r'"\.\."|"(\.\./)+|show-toplevel|"go\.work"|runtime\.Caller\(|Abs\("\.\.')
TAG_RE = re.compile(r'^(//go:build (.* )?integration( .*)?|// \+build .*integration.*)$', re.M)


def glob_re(pattern):
    """`**` matches across directories (including none), `*` and `?` within one."""
    out, i = "", 0
    while i < len(pattern):
        if pattern.startswith("**/", i):
            out += "(?:.*/)?"
            i += 3
        elif pattern.startswith("**", i):
            out += ".*"
            i += 2
        elif pattern[i] == "*":
            out += "[^/]*"
            i += 1
        elif pattern[i] == "?":
            out += "[^/]"
            i += 1
        else:
            out += re.escape(pattern[i])
            i += 1
    return re.compile("^" + out + "$")


_GLOBS = {}


def match(path, patterns):
    for p in patterns:
        r = _GLOBS.get(p) or _GLOBS.setdefault(p, glob_re(p))
        if r.match(path):
            return True
    return False


def norm_pkg(p):
    p = p.strip().strip("/")
    return "." if p in ("", ".") else p


def pkg_arg(p):
    """A package dir as a go command argument: "." or "./a/b/"."""
    p = norm_pkg(p)
    return "." if p == "." else "./" + p + "/"


# ---------------------------------------------------------------------------------------------
# The workspace: module dirs (repo-relative) and the reverse require graph.
# ---------------------------------------------------------------------------------------------
class Workspace:
    def __init__(self, dirs, requires):
        """dirs: list of module dirs. requires: {dir: set of dirs it requires (workspace only)}."""
        self.dirs = sorted(dirs)
        self.requires = requires
        self.rdeps = {d: set() for d in self.dirs}
        for d, reqs in requires.items():
            for r in reqs:
                self.rdeps.setdefault(r, set()).add(d)

    def owner(self, path):
        best = None
        for d in self.dirs:
            if path == d or path.startswith(d + "/"):
                if best is None or len(d) > len(best):
                    best = d
        return best

    def closure(self, seeds):
        """Every module that transitively requires a seed, the seeds included."""
        out, todo = set(), list(seeds)
        while todo:
            m = todo.pop()
            if m in out:
                continue
            out.add(m)
            todo.extend(self.rdeps.get(m, ()))
        return out


def load_workspace(dirs=None):
    if dirs is None:
        raw = subprocess.check_output(["go", "list", "-m", "-f", "{{.Dir}}"], cwd=ROOT, text=True)
        dirs = [os.path.relpath(l.strip(), ROOT).replace("\\", "/") for l in raw.splitlines() if l.strip()]
    if not dirs:
        raise RuntimeError("the workspace lists no modules")
    paths, reqs = {}, {}
    for d in dirs:
        doc = json.loads(subprocess.check_output(["go", "mod", "edit", "-json", os.path.join(ROOT, d, "go.mod")], text=True))
        paths[doc["Module"]["Path"]] = d
        reqs[d] = [r["Path"] for r in (doc.get("Require") or [])]
    requires = {d: {paths[p] for p in rs if p in paths} for d, rs in reqs.items()}
    return Workspace(dirs, requires)


# ---------------------------------------------------------------------------------------------
# Selection
# ---------------------------------------------------------------------------------------------
class Selection:
    def __init__(self):
        self.full = False
        self.reasons = []          # why everything runs
        self.changed = {}          # module -> first changed file in it
        self.modules = set()       # modules run whole (changed + reverse deps)
        self.via = {}              # module -> why it runs whole
        self.readers = {}          # module -> {pkg: why}
        self.frontend = True
        self.frontend_why = ""

    def everything(self, why):
        self.full = True
        self.reasons.append(why)


# Paths the frontend job reads outside frontend/ (codegen schemas, the rule sources and testdata the
# console's lockstep tests import, the chart helpers, the hack scripts its steps run). Anything
# outside frontend/ that is not one of these, and that is inside a Go module or in an INERT zone
# other than frontend/, leaves the frontend job out. Unknown paths keep it.
FRONTEND_INPUTS = [
    "frontend/**",
    "backend/services/*/graphql/**",
    "backend/services/event-processing/**",
    "deploy/helm/**",
    "deploy/nginx.conf",
    "hack/**",
    ".github/**",
    "go.work",
    "go.work.sum",
    "package.json",
    "package-lock.json",
]
FRONTEND_INERT = ["backend/**", "deploy/**", "docs/**", "examples/**", "sdks/**", "demos/**", "_legacy/**", "*.md", "LICENSE", "NOTICE"]


def select(ws, changed_files):
    s = Selection()
    files = [f.strip().replace("\\", "/") for f in changed_files if f.strip()]
    if not files:
        s.everything("the diff is empty, so there is nothing to select from")
        return s

    fe_hits = [f for f in files if match(f, FRONTEND_INPUTS) or not match(f, FRONTEND_INERT)]
    s.frontend = bool(fe_hits)
    s.frontend_why = ("runs: " + fe_hits[0]) if fe_hits else "skipped: no changed file is a frontend input (frontend/**, a GraphQL schema, event-processing, deploy/helm, hack)"

    for f in files:
        if match(f, FULL_TRIGGERS):
            s.everything("%s changed (a workspace, workflow or hack/ file)" % f)
            continue
        owner = ws.owner(f)
        if owner:
            s.changed.setdefault(owner, f)
        read = False
        for module, pkg, globs, excl, why in READERS:
            if match(f, globs) and not match(f, excl):
                read = True
                if module != owner:
                    s.readers.setdefault(module, {}).setdefault(norm_pkg(pkg), "%s (read by %s)" % (f, why))
        if not owner and not read and not match(f, INERT):
            s.everything("%s is outside every module and no reader row or inert glob accounts for it" % f)

    if s.full:
        s.modules = set(ws.dirs)
        s.frontend, s.frontend_why = True, "runs: everything runs"
        return s

    whole = ws.closure(s.changed)
    for m in whole:
        s.via[m] = ("changed: " + s.changed[m]) if m in s.changed else "requires a changed module"
    s.modules = whole
    for m in list(s.readers):
        if m in whole:
            del s.readers[m]
    return s


def changed_files(base, head):
    mb = subprocess.check_output(["git", "merge-base", base, head], cwd=ROOT, text=True).strip()
    out = subprocess.check_output(["git", "diff", "--name-only", "--no-renames", mb, head], cwd=ROOT, text=True)
    return [l for l in out.splitlines() if l.strip()]


# ---------------------------------------------------------------------------------------------
# Plan: go legs, integration legs
# ---------------------------------------------------------------------------------------------
def legs_for(module, pkgs=None):
    """pkgs None = the whole module (static checks + every package); otherwise just those packages."""
    splits = {norm_pkg(k): v for k, v in SPLITS.get(module, {}).items()}
    out = []
    if pkgs is None:
        for p, n in sorted(splits.items()):
            for i in range(1, n + 1):
                out.append(dict(module=module, leg="%s %d/%d" % (p, i, n), static=False, pkgs=pkg_arg(p), exclude="", shard="%d/%d" % (i, n)))
        out.append(dict(module=module, leg="rest" if splits else "", static=True, pkgs="", exclude=" ".join(pkg_arg(p) for p in sorted(splits)), shard=""))
        return out
    rest = []
    for p in sorted(norm_pkg(x) for x in pkgs):
        n = splits.get(p)
        if n:
            for i in range(1, n + 1):
                out.append(dict(module=module, leg="%s %d/%d" % (p, i, n), static=False, pkgs=pkg_arg(p), exclude="", shard="%d/%d" % (i, n)))
        else:
            rest.append(pkg_arg(p))
    if rest:
        out.append(dict(module=module, leg="readers", static=False, pkgs=" ".join(rest), exclude="", shard=""))
    return out


def has_integration(module, pkg):
    d = os.path.join(ROOT, module, norm_pkg(pkg))
    try:
        names = os.listdir(d)
    except OSError:
        return True  # cannot tell: run it
    for n in names:
        if n.endswith("_test.go"):
            with open(os.path.join(d, n), encoding="utf-8", errors="replace") as fh:
                if TAG_RE.search(fh.read(4096)):
                    return True
    return False


def plan(ws, s, integration_plan):
    go = []
    for m in ws.dirs:
        if m in s.modules:
            go.extend(legs_for(m))
        elif m in s.readers:
            go.extend(legs_for(m, list(s.readers[m])))
    integ_modules = set(s.modules)
    for m, pkgs in s.readers.items():
        if any(has_integration(m, p) for p in pkgs):
            integ_modules.add(m)
    integ = []
    for leg in integration_plan:
        mods = [x for x in leg["modules"].split() if x in integ_modules]
        if not mods:
            continue
        leg = dict(leg)
        if mods != leg["modules"].split():
            leg["modules"] = " ".join(mods)
            leg["cache"] = "\n".join("%s/go.mod\n%s/go.sum" % (x, x) for x in mods)
        integ.append(leg)
    return go, integ


def report(ws, s, go, integ, integration_plan, out=sys.stderr):
    p = lambda *a: print(*a, file=out)
    if s.full:
        p("SELECTION: everything runs")
        for r in s.reasons:
            p("  because " + r)
    else:
        p("SELECTION: %d of %d modules whole, %d by reader packages only" % (len(s.modules), len(ws.dirs), len(s.readers)))
        for m in sorted(s.modules):
            p("  whole    %-45s %s" % (m, s.via[m]))
        for m in sorted(s.readers):
            for pkg, why in sorted(s.readers[m].items()):
                p("  package  %-45s %-28s %s" % (m, pkg, why))
        for m in ws.dirs:
            if m not in s.modules and m not in s.readers:
                p("  SKIPPED  %-45s no changed file is in it, in a module it requires, or read by its tests" % m)
    p("go legs: %d" % len(go))
    for g in go:
        p("  %-45s %-24s static=%s %s%s" % (g["module"], g["leg"] or "-", str(g["static"]).lower(), g["pkgs"] or "./...", (" minus " + g["exclude"]) if g["exclude"] else ""))
    kept = {l["name"] for l in integ}
    p("integration legs: %d of %d" % (len(integ), len(integration_plan)))
    for l in integration_plan:
        p("  %s %-20s %s" % ("run " if l["name"] in kept else "SKIP", l["name"], l["modules"]))
    p("frontend: " + s.frontend_why)


def full_selection(ws, why):
    s = Selection()
    s.everything(why)
    s.modules = set(ws.dirs)
    s.frontend_why = "runs: everything runs"
    return s


def cmd_plan():
    event = os.environ.get("GITHUB_EVENT_NAME", "")
    modules = json.loads(os.environ["DISCOVERED_MODULES"])  # the count-cross-checked full list
    integration_plan = json.loads(os.environ["INTEGRATION_PLAN"])
    ws = load_workspace(modules)
    if sorted(ws.dirs) != sorted(modules):
        raise RuntimeError("workspace mismatch")
    if event != "pull_request":
        s = full_selection(ws, "the event is %r, not a pull_request: push, merge_group, schedule and dispatch always run everything" % event)
    else:
        try:
            files = changed_files(os.environ["PR_BASE_SHA"], os.environ["PR_HEAD_SHA"])
            print("changed files (%d):" % len(files), file=sys.stderr)
            for f in files[:200]:
                print("  " + f, file=sys.stderr)
            if len(files) > 200:
                print("  ... and %d more" % (len(files) - 200), file=sys.stderr)
            s = select(ws, files)
        except Exception as e:  # noqa: BLE001 - any failure to select means run everything
            print("::warning::affected-module selection failed (%s: %s); running everything" % (type(e).__name__, e), file=sys.stderr)
            s = full_selection(ws, "the selection itself failed: %s" % e)
    go, integ = plan(ws, s, integration_plan)
    if s.full and len({g["module"] for g in go}) != len(ws.dirs):
        raise RuntimeError("a full run planned %d modules of %d" % (len({g["module"] for g in go}), len(ws.dirs)))
    if s.full and len(integ) != len(integration_plan):
        raise RuntimeError("a full run planned %d integration legs of %d" % (len(integ), len(integration_plan)))
    report(ws, s, go, integ, integration_plan)
    print(json.dumps({"go": go, "integration": integ, "frontend": s.frontend, "full": s.full}, separators=(",", ":")))


# ---------------------------------------------------------------------------------------------
# check: the tables still describe the tree
# ---------------------------------------------------------------------------------------------
def cmd_check():
    ws = load_workspace()
    errs = []
    declared = set()
    for module, pkg, globs, excl, why in READERS:
        if module not in ws.dirs:
            errs.append("READERS names %s, which go.work does not declare" % module)
            continue
        d = os.path.join(ROOT, module, norm_pkg(pkg))
        if not os.path.isdir(d) or not any(n.endswith("_test.go") for n in os.listdir(d)):
            errs.append("READERS row %s %s: the package has no tests (moved or renamed?)" % (module, pkg))
        declared.add((module, norm_pkg(pkg)))
    for module, splits in SPLITS.items():
        if module not in ws.dirs:
            errs.append("SPLITS names %s, which go.work does not declare" % module)
        for pkg, n in splits.items():
            if not os.path.isdir(os.path.join(ROOT, module, norm_pkg(pkg))):
                errs.append("SPLITS %s %s: no such package" % (module, pkg))
            if not (1 <= n <= 8):
                errs.append("SPLITS %s %s: shard count %d out of range" % (module, pkg, n))

    def allowed(module, pkg):
        if (module, pkg) in declared:
            return True
        for (m, p) in IN_MODULE:
            if m == module and fnmatch.fnmatchcase(pkg, p):
                return True
        return False

    for module in ws.dirs:
        base = os.path.join(ROOT, module)
        for dirpath, dirnames, filenames in os.walk(base):
            dirnames[:] = [d for d in dirnames if not d.startswith(".") and d not in ("node_modules", "vendor")]
            rel = os.path.relpath(dirpath, base).replace("\\", "/")
            # a nested module is its own module
            if rel != "." and os.path.exists(os.path.join(dirpath, "go.mod")):
                dirnames[:] = []
                continue
            for n in filenames:
                if not n.endswith(".go"):
                    continue
                with open(os.path.join(dirpath, n), encoding="utf-8", errors="replace") as fh:
                    if ESCAPE_RE.search(fh.read()) and not allowed(module, norm_pkg(rel)):
                        errs.append("%s/%s/%s may read outside its module: add a READERS row for (%s, %s) or an IN_MODULE reason" % (module, rel, n, module, norm_pkg(rel)))
    if errs:
        for e in errs:
            print("::error::" + e)
        sys.exit(1)
    print("ci-select tables: %d reader rows, %d split modules, every out-of-module reader accounted for" % (len(READERS), len(SPLITS)))


# ---------------------------------------------------------------------------------------------
# replay: what a merged commit would have selected
# ---------------------------------------------------------------------------------------------
def cmd_replay(shas):
    ws = load_workspace()
    integration_plan = json.loads(subprocess.check_output(["bash", os.path.join(ROOT, "hack", "integration-tests.sh"), "--matrix"], cwd=ROOT, text=True))
    for sha in shas:
        title = subprocess.check_output(["git", "log", "-1", "--format=%h %s", sha], cwd=ROOT, text=True).strip()
        files = changed_files(sha + "^", sha)
        s = select(ws, files)
        go, integ = plan(ws, s, integration_plan)
        print("=" * 100)
        print(title)
        print("changed files: %d (%s%s)" % (len(files), ", ".join(files[:4]), ", ..." if len(files) > 4 else ""))
        if s.full:
            print("  EVERYTHING: " + "; ".join(s.reasons[:2]))
        else:
            print("  whole modules (%d): %s" % (len(s.modules), " ".join(sorted(m.split("/")[-1] for m in s.modules)) or "-"))
            print("  reader packages: %s" % (", ".join("%s:%s" % (m.split("/")[-1], ",".join(sorted(p))) for m, p in sorted(s.readers.items())) or "-"))
        print("  go legs: %d   integration legs: %d of %d (%s)   frontend: %s" % (
            len(go), len(integ), len(integration_plan), " ".join(l["name"] for l in integ) or "-", "run" if s.frontend else "skip"))


# ---------------------------------------------------------------------------------------------
# self-test
# ---------------------------------------------------------------------------------------------
def fake_ws():
    # core <- svc-a <- svc-b ; core <- cli ; tool (standalone) ; deploy <- cli
    return Workspace(
        ["backend/core", "backend/services/svc-a", "backend/services/svc-b", "backend/cli", "backend/tools/tool", "deploy"],
        {
            "backend/core": set(),
            "backend/services/svc-a": {"backend/core"},
            "backend/services/svc-b": {"backend/services/svc-a"},  # core only THROUGH svc-a
            "backend/cli": {"backend/core", "deploy"},
            "backend/tools/tool": set(),
            "deploy": set(),
        })


def self_test():
    global READERS, SPLITS
    fails = []

    def expect(cond, what):
        print(("  ok: " if cond else "  FAIL: ") + what)
        if not cond:
            fails.append(what)

    saved = (READERS, SPLITS)
    READERS = [
        ("backend/cli", "cmd", ["docs/**"], [], "t"),
        ("backend/tools/tool", ".", ["backend/services/*/graphql/**"], [], "t"),
        ("backend/core", "test", ["**/*.go"], [], "t"),
    ]
    SPLITS = {"backend/core": {"messaging": 3}}
    ws = fake_ws()
    try:
        print("==> globs")
        expect(match("a/b/c.go", ["**/*.go"]) and match("c.go", ["**/*.go"]), "** spans zero or more directories")
        expect(not match("a/b.go", ["*.go"]), "* does not cross a slash")
        expect(match("docs/x/y.md", ["docs/**"]) and not match("docsy/x", ["docs/**"]), "a dir glob matches only that dir")

        print("==> reverse dependencies are transitive")
        s = select(ws, ["backend/core/x.go"])
        expect(not s.full and s.modules == set(ws.dirs) - {"backend/tools/tool", "deploy"},
               "a core change runs core, svc-a, svc-b (through svc-a) and cli, not tool or deploy: %s" % sorted(s.modules))
        s = select(ws, ["backend/services/svc-a/graphql/schema.graphql"])
        expect(s.modules == {"backend/services/svc-a", "backend/services/svc-b"}, "a service change runs it and its dependants: %s" % sorted(s.modules))
        expect(s.readers.get("backend/tools/tool") == {".": s.readers["backend/tools/tool"]["."]}, "a reader of a changed file runs that package")
        s = select(ws, ["deploy/helm/x.yaml"])
        expect(s.modules == {"deploy", "backend/cli"}, "a deploy change runs deploy and cli: %s" % sorted(s.modules))

        print("==> fail safe")
        # Each named trigger must fire as ITSELF, not only through the unknown-file rule behind it.
        for f, what, why in [("go.work", "go.work", "workspace, workflow or hack/"),
                             ("go.work.sum", "go.work.sum", "workspace, workflow or hack/"),
                             (".github/workflows/ci.yml", ".github", "workspace, workflow or hack/"),
                             ("hack/go-race.sh", "hack/", "workspace, workflow or hack/"),
                             ("mystery/file.txt", "an unknown file outside every module", "no reader row or inert glob")]:
            s = select(ws, ["backend/tools/tool/x.go", f])
            expect(s.full and s.modules == set(ws.dirs) and s.frontend and any(why in r for r in s.reasons),
                   "%s runs everything, for that reason" % what)
        s = select(ws, [])
        expect(s.full, "an empty diff runs everything")

        print("==> docs-only")
        s = select(ws, ["docs/docs/x.md", "README.md"])
        expect(not s.full and not s.modules and set(s.readers) == {"backend/cli"} and not s.frontend,
               "a docs change runs only the docs-reading package, no frontend: %s %s" % (sorted(s.modules), s.readers))

        print("==> frontend")
        expect(select(ws, ["frontend/apps/x.ts"]).frontend, "a frontend change runs frontend")
        expect(select(ws, ["backend/services/svc-a/graphql/schema.graphql"]).frontend, "a schema change runs frontend")
        expect(not select(ws, ["backend/core/x.go"]).frontend, "a core-only change skips frontend")

        print("==> plan")
        integ_plan = [{"name": "core", "modules": "backend/core", "cache": ""},
                      {"name": "small", "modules": "backend/services/svc-a backend/tools/tool", "cache": ""}]
        s = select(ws, ["backend/services/svc-b/x.go"])
        go, integ = plan(ws, s, integ_plan)
        expect([g["module"] for g in go] == ["backend/core", "backend/services/svc-b"] and go[0]["pkgs"] == "./test/" and not go[0]["static"],
               "svc-b alone runs svc-b whole and core's repo-walking test package: %s" % [(g["module"], g["pkgs"]) for g in go])
        expect(integ == [], "and no integration leg (neither is integration-selected): %s" % integ)
        s = select(ws, ["backend/services/svc-a/x.go"])
        go, integ = plan(ws, s, integ_plan)
        expect([l["modules"] for l in integ] == ["backend/services/svc-a"], "the small leg narrows to the selected module: %s" % integ)
        s = full_selection(ws, "test")
        go, integ = plan(ws, s, integ_plan)
        core = [g for g in go if g["module"] == "backend/core"]
        expect([g["leg"] for g in core] == ["messaging 1/3", "messaging 2/3", "messaging 3/3", "rest"]
               and core[-1]["static"] and core[-1]["exclude"] == "./messaging/" and not any(g["static"] for g in core[:-1]),
               "a split module runs N shards plus one static catch-all excluding the split package")
        expect(sum(1 for g in go if g["static"]) == len(ws.dirs), "a full run has exactly one static leg per module")
        expect(integ == integ_plan, "a full run keeps every integration leg")
        go = legs_for("backend/core", ["messaging", "test"])
        expect([g["leg"] for g in go] == ["messaging 1/3", "messaging 2/3", "messaging 3/3", "readers"] and go[-1]["pkgs"] == "./test/",
               "a reader-selected split package is still sharded")
    finally:
        READERS, SPLITS = saved
    if fails:
        print("self-test FAILED: %d case(s)" % len(fails))
        sys.exit(1)
    print("self-test passed")


def main(argv):
    if not argv:
        print(__doc__)
        return 2
    if argv[0] == "--self-test":
        self_test()
    elif argv[0] == "plan":
        cmd_plan()
    elif argv[0] == "check":
        cmd_check()
    elif argv[0] == "replay":
        cmd_replay(argv[1:])
    else:
        print(__doc__)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
