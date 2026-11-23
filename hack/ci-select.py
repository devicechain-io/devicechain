#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""CI leg selection: which Go modules, test packages and integration legs a pull request runs.

    hack/ci-select.py plan      # in `discover`: print the go legs, integration legs and frontend verdict
    hack/ci-select.py check     # the reader table and split table still describe the tree
    hack/ci-select.py codeql    # in codeql.yml: the module dirs the Go analysis builds, one per line
    hack/ci-select.py migration-deps  # in `migrations`: the import graph covers what `go list` compiles
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
workflow_dispatch), go.work, go.work.sum, any go.mod or go.sum, .github or hack changed, a changed file sits outside
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
# **/go.mod, **/go.sum: a workspace resolves ONE version of each external dependency for all of
#   its modules (minimal version selection over every member's go.mod), so a bump in one module's
#   go.mod can move what every other module builds and tests against. The require graph below
#   cannot see that, so any go.mod/go.sum change runs everything.
FULL_TRIGGERS = [
    "go.work",
    "go.work.sum",
    "**/go.mod",
    "**/go.sum",
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
    # tofu_roots_test.go — walks every .tf under deploy/ and compares the roots with what
    # hack/tofu-roots.sh --all prints. plan_fixtures_test.go — scans the sanitized plan-gate
    # fixtures for confidential material.
    ("deploy", ".", [
        "deploy/**/*.tf",
        "hack/tofu-roots.sh",
        "deploy/gke/fixtures/**",
        "hack/fixtures/gke-extraction/**",
    ], [], "tofu_roots_test.go, plan_fixtures_test.go"),
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
        "backend/core/config/instance-config.schema.json",  # config_schema_test.go: the released schema asset
    ], [], "cmd/docs_*_test.go, cmd/config_schema_test.go, cmd/relationship_test.go via apitest"),
    # bootstrap: README.md and deployment docs (fence/backupsizing/diskbudget/size_docs), every service's non-test
    # source (install_test.go:432), service configs (sizing/sparkplug), user-management go.mod
    # (breakglass), core/rdb/postgres.go (install_test.go:473), the broker's main.go/go.mod
    # (brokerimage_test via kube.go repoRoot). deploy/** is the deploy module, which cli requires.
    ("backend/cli", "bootstrap", DOCS_DEPLOY + [
        "README.md",
        "backend/services/**/*.go",
        "backend/services/user-management/go.mod",
        "backend/core/rdb/postgres.go",
        "backend/broker/dc-nats-server/main.go",
        "backend/broker/dc-nats-server/go.mod",
        # executor_apiserver_test.go installs the release CRDs in envtest.
        "backend/k8s/config/crd/bases/**",
        "hack/harborpulse-rig.sh",  # valuesoverlay_test.go: the rig's --values heredoc
    ], NONTEST, "bootstrap/*_test.go"),
    ("backend/cli", "attr", ["backend/services/device-management/graphql/**"], [], "attr/documents_test.go:38"),
    ("backend/cli", "fleetop", ["backend/services/device-management/graphql/**"], [], "fleetop/documents_test.go:16"),
    ("backend/cli", "product", [SDL], [], "product/documents_test.go:32"),
    # internal/apitest/capabilities.go:44-48 serves the real SDL from every service's graphql dir; the cmd and
    # sim tests (cmd/product_rotate_test.go, sim/*_test.go) call it through their fixtures.
    ("backend/cli", "internal/apitest", [SDL], [], "internal/apitest/capabilities_test.go"),
    ("backend/cli", "sim", [SDL], [], "sim/admin_conflict_test.go, hostheader_test.go, tier_test.go via apitest"),
    # deploy/fleet_premises_test.go:82 pins the recorded premises to the toolchain versions in ci.yml and
    # to the kind node image in hack/fleet-s0/kind.sh.
    ("deploy", ".", [".github/workflows/ci.yml", "hack/fleet-s0/kind.sh"], [], "fleet_premises_test.go:82"),
    ("backend/cli", "failedevents", ["backend/services/event-management/graphql/**"], [], "failedevents/*_test.go"),
    ("backend/cli", "sim", ["backend/sims/dc-simulator/sim/handshake.go"], [], "sim/handshake_lockstep_test.go:30"),
    # core/test walks every .go file in the repository (logsink, defaultorder, sharedcache, ...).
    ("backend/core", "test", ["**/*.go", "go.work"], [], "test/*_repo_test.go"),
    ("backend/core", "backupset", [
        "backend/**/*.go",
        "docs/**/disaster-recovery.md",
    ], NONTEST, "backupset/backupset_test.go:120,248"),
    ("backend/core", "graphql/schemaplane", [
        SDL,
        "frontend/packages/dashboards/src/**",
        "frontend/apps/dashboard/src/**",
    ], [], "graphql/schemaplane/schemaplane_test.go:19, frontend_documents_test.go:85"),
    ("backend/core", "rdb/partialupdatetest", ["backend/services/*/model/*.go", "backend/services/*/model/*/*.go"], NONTEST, "rdb/partialupdatetest/savescan_test.go:133"),
    ("backend/core", "config", [
        "deploy/opentofu/modules/traefik/main.tf",
        "docs/**/releases-and-upgrades.md",
    ], [], "config/edge_timeout_lockstep_test.go:24, config/nats_shard_docs_test.go:20"),
    ("backend/core", "natsauth", ["deploy/opentofu/modules/nats/main.tf"], [], "natsauth/identity_account_test.go:241"),
    ("backend/core", "messaging", [
        "deploy/helm/devicechain/templates/prometheusrule-broker.yaml",
        "deploy/opentofu/instance/variables.tf",
        "deploy/opentofu/modules/nats/main.tf",
    ], [], "messaging/rebalanceable_streams_test.go:32, dedup_window_test.go:443"),
    ("backend/core", "semantic", ["NOTICE"], [], "semantic/semantic_test.go:487"),
    # core/core: the production-hardening page (hardening_docs_test.go:19); functionalareas*.go
    # name ".." only in a comment about os.ReadDir.
    ("backend/core", "core", ["docs/docs/deployment/production-hardening.md"], [], "core/hardening_docs_test.go:19"),
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
    ("backend/k8s", "controllers", ["deploy/opentofu/modules/cnpg-cluster/chart/**"], [], "controllers/snapshotretention_helm_test.go:31; executor_test.go reads ../config/executor/requester-role.yaml inside the module"),
    # executor_security_test.go walks every Helm manifest for requester bindings
    # and the chart templates for accidental executor installation.
    ("backend/k8s", "config", ["deploy/helm/**"], [], "config/executor_security_test.go"),
    # Both tests copy/read the current CLI refusal probe; git baseline trees are
    # mocked temporary fixtures, not additional current-tree dependencies.
    ("backend/k8s", "executor", ["backend/cli/bootstrap/foundation_probe_test.go"], [], "executor/foundation_test.go, executor/foundation_execution_test.go"),
    ("backend/services/command-delivery", ".", [
        "frontend/apps/console/src/routes/command-batches/CreateBatchForm.tsx",
        "frontend/apps/console/src/routes/devices/DeviceCommandsPanel.tsx",
        "deploy/helm/devicechain/dashboards/command-delivery.json",
    ], [], "console_submission_contract_test.go:17, dashboard_series_test.go:43"),
    ("backend/services/command-delivery", "model", ["backend/testdata/authored-rules/**"], [], "model/authored_sim_command_wire_test.go:30"),
    ("backend/services/command-delivery", "verify", ["backend/services/device-management/graphql/schema.graphql"], [], "verify/proposal_target_test.go:143"),
    ("backend/services/device-management", "config", ["deploy/helm/devicechain/values.yaml"], [], "config/chart_floor_test.go:25"),
    ("backend/services/device-management", "model", [
        "backend/testdata/authored-rules/**",
        "backend/sims/dc-simulator/sim/fleetpulse_speed_limit_schema.json",
    ], [], "model/authored_sim_*_test.go"),
    ("backend/services/device-management", "processor", ["frontend/testdata/connect-snippets/**"], [], "processor/connect_snippet_golden_test.go:32"),
    # graphql: the console picker documents (console_picker_documents_test.go:27); its other
    # escaping file only names "../x" as a refused-key fixture.
    ("backend/services/device-management", "graphql", ["frontend/apps/console/src/lib/api/**"], [], "graphql/console_picker_documents_test.go:27"),
    ("backend/services/event-management", "model", ["docs/**/releases-and-upgrades.md"], [], "model/upgrade_notes_bounds_test.go:34"),
    ("backend/services/event-management", "processor/backfill", ["backend/services/device-management/graphql/**"], [], "processor/backfill/*_test.go"),
    ("backend/services/event-processing", "graphql", ["backend/testdata/authored-rules/**"], [], "graphql/authored_sim_rules_test.go:39"),
    # event-sources/processor walks every non-test .go file under backend/; the connect snippet
    # golden test also reads both templates that publish the external MQTT port.
    ("backend/services/event-sources", "processor", [
        "backend/**/*.go",
        "frontend/testdata/connect-snippets/**",
        "backend/core/integrationcodec/testdata/**",
        "deploy/helm/devicechain/templates/mqtt-exposure.yaml",
        "deploy/helm/devicechain/templates/gateway.yaml",
    ], [], "processor/broker_assertion_test.go:218, integration_marks_test.go:159, connect_snippet_golden_test.go:288-289"),
    ("backend/services/event-sources", "integration", ["backend/services/device-management/graphql/**"], [], "integration/backend_test.go:32"),
    ("backend/services/mcp", "server", [SDL], [], "server/documents_test.go:35, alarm_suppression_test.go:27, command_cause_test.go:25"),
    ("backend/services/notification-management", "processor", ["backend/services/device-management/graphql/**"], [], "processor/alarm_lookup_wire_test.go:25"),
    # Each peer client's recorded documents, validated against the SDL its target serves
    # (documents_test.go validateDocuments: ../../<target>/graphql).
    ("backend/services/ai-inference", "inference", ["backend/services/user-management/graphql/**"], [], "inference/documents_test.go"),
    ("backend/services/notification-management", "processor", ["backend/services/device-management/graphql/**"], [], "processor/documents_test.go"),
    # suppression_rolling_matrix_test.go:77 — the served device-management SDL, with alarmSuppressionStatus cut out to model an old server.
    ("backend/services/notification-management", "processor", ["backend/services/device-management/graphql/schema.graphql"], [], "processor/suppression_rolling_matrix_test.go:77"),
    ("backend/services/event-sources", "adapter", [
        "backend/services/device-management/graphql/**",
        "backend/services/device-state/graphql/**",
    ], [], "adapter/documents_test.go"),
    ("backend/services/event-sources", "commandpull", [
        "backend/services/device-management/graphql/**",
        "backend/services/command-delivery/graphql/**",
    ], [], "commandpull/documents_test.go"),
    ("backend/services/event-sources", "credcheck", ["backend/services/device-management/graphql/**"], [], "credcheck/documents_test.go"),
    ("backend/services/event-sources", "presence", [
        "backend/services/user-management/graphql/**",
        "backend/services/device-state/graphql/**",
        "backend/services/command-delivery/graphql/**",
    ], [], "presence/documents_test.go"),
    ("backend/services/lwm2m-ingest", "downlink", [
        "backend/services/command-delivery/graphql/**",
        "backend/services/device-management/graphql/**",
    ], [], "downlink/documents_test.go"),
    # lpwan: also "../etc/passwd" and "../x" as refused-id fixtures (chirpstack_test.go, tts_test.go).
    ("backend/services/outbound-connectors", "lpwan", ["backend/services/device-management/graphql/**"], [], "lpwan/documents_test.go"),
    ("backend/services/update-management", "profilecheck", ["backend/services/device-management/graphql/**"], [], "profilecheck/documents_test.go"),
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
    # scenarioverify/documents_test.go:21 reads the served SDL of every area the verifier queries
    # (verifierDocuments: device-management, command-delivery, event-management, event-processing,
    # outbound-connectors, notification-management, update-management).
    ("backend/sims/dc-simulator", "scenarioverify", [
        "backend/services/device-management/graphql/**",
        "backend/services/command-delivery/graphql/**",
        "backend/services/event-management/graphql/**",
        "backend/services/event-processing/graphql/**",
        "backend/services/outbound-connectors/graphql/**",
        "backend/services/notification-management/graphql/**",
        "backend/services/update-management/graphql/**",
    ], [], "scenarioverify/documents_test.go:21"),
    # scenarioverify/rig_s7_test.go:17 extracts the rig's OAuth-client cleanup from hack/harborpulse-rig.sh and runs it.
    ("backend/sims/dc-simulator", "scenarioverify", ["hack/harborpulse-rig.sh"], [], "scenarioverify/rig_s7_test.go:17"),
    ("backend/sims/dc-simulator", "updater", ["backend/core/ota/testdata/**"], [], "updater/*_test.go"),
    # signer_baseline_test.go:24 loads ../../services as a baseline tree: each area's served graphql SDL.
    ("backend/tools/apiprobe", ".", [SDL, "backend/services/ai-inference/**"], [], "baseline_test.go, readsweep_test.go, documents_test.go, signer_baseline_test.go:24, tablesweep_test.go (ai-inference secret-scope guard)"),
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
    ("backend/services/event-processing", "processor"): "suppression_isolation_test.go walks \"..\", its own module root",
    ("backend/services/user-management", "identity"): "\"../\" is a refused-redirect fixture",
    ("backend/sims/dc-simulator", "cmd/nightshift-capture"): "a generator run by hand, never by a test",
    ("backend/sims/dc-simulator", "scenariosink"): "\"..\" is SMTP dot-stuffing in a DATA line; it reads no files",
    ("backend/tools/docsgql", "cmd/*"): "command defaults (--head ../../..), run by hand or by api-compat, not by tests",
    ("backend/tools/vocabgen", "."): "hack/vocab/sources.lock (hack/** runs everything)",
    ("backend/core", "secrets"): "hack/dr-rig.sh (hack/** runs everything)",
    ("backend/tools/redlines", "."): "temp fixtures only",
    ("examples/ota-device", "internal/testutil"): "runtime.Caller root is its own module",
    ("backend/core", "test/frontend"): "build-tagged `frontend`, so the go job never compiles it; the frontend job runs the same vitest suites",
    ("backend/core", "replication"): "\"..\" appears only in a comment",
    ("backend/fleet", "tofurun"): "\"..\" is the refused root-path segment and the \"../x\" fixtures of its tests; its fixture trees are its own testdata/",
    ("backend/fleet", "internal/tofutest"): "runtime.Caller locates tofurun/testdata inside backend/fleet",
    ("backend/core", "webhooksig"): "\"..\" is a refused path segment",
    ("backend/k8s", "api/v1"): "schema_identity_test.go reads ../../config/crd/bases inside backend/k8s",
    ("backend/k8s", "api/v1beta1"): "instance_crd_test.go, deprecations_test.go, executor_crd_test.go and executor_schema_test.go read ../../config/crd/bases inside backend/k8s",
    ("backend/services/command-delivery", "processor"): "reads ../contract inside its own module",
    ("backend/services/event-processing", "internal/review"): "reads ../replayinput inside its own module",
    ("backend/services/device-management", "graphql"): "\"../x\" is a refused-key fixture",
    ("backend/services/outbound-connectors", "connectorspec"): "\"..\" is a refused template path segment",
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
    # 3, not 2: on run 43972121210 the two bootstrap shards took 20.5 and 18.9 min (2.8 min of each is
    # the `go test -list` compile), the longest legs of the run. A third shard costs one more setup
    # (~3 min) and brings each to ~13-16 min, under integration (core) at 20.1. device-management's
    # processor stays at 3: its slow shard (19.3 min) carries one ~350 s test that round-robin cannot
    # split, so a fourth shard would buy ~1 min on a leg that is not the critical path.
    "backend/cli": {"bootstrap": 3},
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
    def __init__(self, dirs, requires, modpaths=None):
        """dirs: list of module dirs. requires: {dir: set of dirs it requires (workspace only)}.
        modpaths: {module path: dir}, needed only by the package import graph."""
        self.dirs = sorted(dirs)
        self.requires = requires
        self.modpaths = modpaths or {}
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
    return Workspace(dirs, requires, paths)


# ---------------------------------------------------------------------------------------------
# The package import graph, for the jobs that run a FEW packages rather than whole modules.
# ---------------------------------------------------------------------------------------------
# The `migrations` jobs build and run three roots (MIGRATION_ROOTS below) and nothing else of
# Go. Which files can change their result is therefore the set of workspace PACKAGES those roots
# import, transitively, plus everything those packages embed. A module-level answer would be
# far too wide here: migrationdiff requires most of the workspace's modules but imports a few
# packages of each (a service's schema or model package, core's rdb), so it is the package graph
# that decides.
#
# The graph is read from the source with no Go toolchain and no network, which is what lets
# `discover` compute it before anything is downloaded. It deliberately OVER-approximates:
# every file's imports count whatever its build tags, so a tag-gated import can only add an
# edge, never lose one. `migration-deps` cross-checks the result against `go list -deps -test`
# in the migrations job itself, where the module cache is warm, so an edge this parser cannot
# see fails that job instead of silently narrowing the selection.
_IMPORT_HEADER_END = re.compile(r"^(?:func|var|type|const)\b", re.M)
_IMPORT_BLOCK = re.compile(r"^import\s*\((.*?)\)", re.M | re.S)
_IMPORT_LINE = re.compile(r'^import\s+(?:[\w.]+\s+)?"([^"]+)"', re.M)
_IMPORT_SPEC = re.compile(r'(?:^|\n)\s*(?:[\w.]+\s+)?"([^"]+)"')
_BLOCK_COMMENT = re.compile(r"/\*.*?\*/", re.S)
_LINE_COMMENT = re.compile(r"//[^\n]*")


def go_imports(text):
    """The import paths of one Go file. Comments are stripped first, so a commented-out import
    is not counted; the header ends at the first top-level declaration, as Go requires."""
    text = _LINE_COMMENT.sub("", _BLOCK_COMMENT.sub("", text))
    m = _IMPORT_HEADER_END.search(text)
    head = text[:m.start()] if m else text
    out = set(_IMPORT_LINE.findall(head))
    for block in _IMPORT_BLOCK.findall(head):
        out.update(_IMPORT_SPEC.findall(block))
    return out


class PackageGraph:
    """Workspace packages by repo-relative dir: {dir: (imports, test_imports)}, both as dirs."""

    def __init__(self, ws, root=ROOT):
        self.ws = ws
        self.pkgs = {}
        self.missing = set()  # workspace imports with no package in the tree, from the last closure()
        bypath = sorted(ws.modpaths.items(), key=lambda kv: -len(kv[0]))

        def resolve(imp):
            for mp, d in bypath:
                if imp == mp:
                    return d
                if imp.startswith(mp + "/"):
                    return d + "/" + imp[len(mp) + 1:]
            return None

        for module in ws.dirs:
            base = os.path.join(root, module)
            for dirpath, dirnames, filenames in os.walk(base):
                rel = os.path.relpath(dirpath, base).replace("\\", "/")
                if rel != "." and os.path.exists(os.path.join(dirpath, "go.mod")):
                    dirnames[:] = []
                    continue
                # The go command ignores testdata, vendor and dirs starting with "." or "_".
                dirnames[:] = [d for d in dirnames if not d.startswith((".", "_")) and d not in ("testdata", "vendor", "node_modules")]
                gofiles = [n for n in filenames if n.endswith(".go")]
                if not gofiles:
                    continue
                pkg = module if rel == "." else module + "/" + rel
                imps, timps = set(), set()
                for n in gofiles:
                    with open(os.path.join(dirpath, n), encoding="utf-8", errors="replace") as fh:
                        found = {r for r in (resolve(i) for i in go_imports(fh.read())) if r}
                    (timps if n.endswith("_test.go") else imps).update(found)
                self.pkgs[pkg] = (imps, timps)

    def closure(self, roots):
        """roots: [(dir, recursive)]. Every package the roots' test binaries compile: the roots
        with their test imports, then everything reachable through ordinary imports."""
        start = set()
        self.missing = set()
        for d, recursive, *_ in roots:
            for p in self.pkgs:
                if p == d or (recursive and p.startswith(d + "/")):
                    start.add(p)
            if not any(p == d or (recursive and p.startswith(d + "/")) for p in self.pkgs):
                raise RuntimeError("root %s has no Go package" % d)
        out, todo = set(), []
        for p in start:
            out.add(p)
            todo.extend(self.pkgs[p][0] | self.pkgs[p][1])
        while todo:
            p = todo.pop()
            if p in out:
                continue
            if p not in self.pkgs:
                self.missing.add(p)
                continue
            out.add(p)
            todo.extend(self.pkgs[p][0])
        return out

    def hits(self, path, closure):
        """Can a change to `path` change what a package in `closure` compiles or embeds?
        A .go file in a package dir belongs to that package. Anything else (an embedded asset,
        a testdata file, a .go file go ignores) counts for EVERY package above it in its module,
        which over-approximates embedding and fixture reads rather than guessing at them."""
        owner = self.ws.owner(path)
        if not owner:
            return False
        d = os.path.dirname(path)
        if path.endswith(".go") and d in self.pkgs:
            return d in closure
        while True:
            if d in closure:
                return True
            if d == owner or "/" not in d:
                return False
            d = os.path.dirname(d)


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
        self.migrations = True
        self.migrations_why = "runs: everything runs"
        self.fleet_runner = True
        self.fleet_runner_why = "runs: everything runs"
        self.draft = False
        # The race step's scope on a pull request: {module: set of package dirs to race}, a module
        # missing from it racing nothing; None races every leg in full. See race_select.
        self.race = None
        self.race_full = {}        # module -> why its legs race in full anyway (doubt)
        self.race_why = "full: everything runs"

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
    "backend/services/user-management/settingsdefs/**",  # admin/settings/sections.test.ts:27
    "backend/services/user-management/basemap/**",       # basemap/placeholders.test.ts:50
    "backend/services/user-management/iam/tierconfig.go",  # admin/tiers/tierConfig.test.ts:191
    "backend/core/governance/**",                        # admin/tiers/tierConfig.test.ts:190-225
    "docs/src/**",                                       # packages/brand/tools/verify.mjs:107
    "deploy/helm/**",
    "deploy/nginx.conf",
    "hack/**",
    ".github/**",
    "go.work",
    "go.work.sum",
    "package.json",
    "package-lock.json",
]
# The `fleet-runner` job (backend/fleet/tofurun against a real OpenTofu and PostgreSQL, then the runner
# image built for real): what it builds from or runs against. The image copies backend/fleet, backend/core's
# go.mod and go.sum, the fleet roots and the shared gke-cluster module; the count check is its gate.
FLEET_RUNNER_INPUTS = [
    "backend/fleet/**",
    "backend/core/**",  # the runner imports core/rdb, which pulls in ~14 core packages
    "deploy/fleet/**",
    "deploy/opentofu/modules/gke-cluster/**",
    "hack/check-fleetpg-count.sh",
    "go.work",
    "go.work.sum",
    ".gitignore",
]


# ---------------------------------------------------------------------------------------------
# The `migrations` and `migrations-timescaledb-next` jobs: what they run, so what can change them.
# ---------------------------------------------------------------------------------------------
# Both jobs run hack/migration-diff.sh verify, and that script runs exactly these Go roots
# against a real TimescaleDB (see the script for each):
#   - backend/tools/migrationdiff: `go run .` (verify, coverage, replay) and
#     `go test -tags integration ./...` (the tenant-purge drill);
#   - backend/core ./rdb/...: the guest-connection integration suite;
#   - backend/cli ./bootstrap: dcctl's per-instance database suite (`-run` filtered, but the whole
#     package's test binary is compiled, so the whole package is an input).
# (dir, recursive, readers). A change runs the jobs when it reaches a package these roots compile,
# or when it matches a READERS row of a package whose tests they RUN (readers=True): those tests
# read files no import edge shows (core/rdb/partialupdatetest parses every service's model source).
# cli/bootstrap is False on purpose: the script runs it with -run 'InstanceDatabase|Provisioner|
# ADropBlockedByASuperuserSession', and the files holding those tests (instancedb*_test.go,
# objectstore_storage_class_test.go) read nothing outside the module (none is in
# hack/ci-select-escapes.txt); its READERS row is for the package's OTHER tests.
MIGRATION_ROOTS = [
    ("backend/tools/migrationdiff", True, True),
    ("backend/core/rdb", True, True),
    ("backend/cli/bootstrap", False, False),
]
# Non-Go inputs the jobs read directly: the operand image they build and launch
# (deploy/images/timescaledb/build.sh, standalone.sh) and the fleet module, whose tagged
# real-Postgres suite the script runs (a build-tagged file is invisible to the import graph, so
# the whole module is an input). hack/** (migration-diff.sh,
# check-duplicate-indexes.sh, docker-pull.sh, go-mod-download.sh), .github/**, go.work and every
# go.mod/go.sum already run everything, so they need no row here.
MIGRATION_INPUTS = ["deploy/images/timescaledb/**", "backend/fleet/**"]

# ---------------------------------------------------------------------------------------------
# The `image-smoke` legs: one ko build per image module, so what can change one.
# ---------------------------------------------------------------------------------------------
# An image is built from its module's Go code and from the workspace modules it requires, so a leg
# runs when its module is in the whole-module set `select` computes (changed, or requiring a
# changed module, transitively); a full selection runs every leg. Dependency versions (go.work,
# go.work.sum, any go.mod/go.sum), the workflow and hack/ (ko-build.sh, the broker smoke scripts)
# already run everything. ko reads nothing else from a module's own tree that `go build` would not
# (embeds cannot leave the module), so the only other inputs are these, read by a leg's steps:
#   - .ko.yaml: the pinned base of EVERY image (hack/ko-build.sh points ko at it);
#   - backend/k8s runs hack/check-ko-base-used.sh, which scans .github/, hack/, deploy/ and
#     backend/cli for a ko call that bypasses the wrapper (non-test files);
#   - backend/services/user-management execs the binary named in backend/cli/bootstrap/breakglass.go.
# (module, [globs], [excluded globs]); module "*" is every leg.
SMOKE_INPUTS = [
    ("*", [".ko.yaml"], []),
    ("backend/k8s", ["deploy/**", "backend/cli/**"], ["**/*_test.go"]),
    ("backend/services/user-management", ["backend/cli/bootstrap/breakglass.go"], []),
]


def smoke_select(s, files, smoke_all):
    """(legs, {module: why}). Every leg on a full selection; otherwise a leg whose module runs whole,
    or that a SMOKE_INPUTS row names for a changed file."""
    if s.full:
        return list(smoke_all), {m: "everything runs" for m in smoke_all}
    why = {}
    for m in smoke_all:
        if m in s.modules:
            why[m] = s.via.get(m, "its module runs")
            continue
        for module, globs, excl in SMOKE_INPUTS:
            if module not in ("*", m):
                continue
            hit = next((f for f in files if match(f, globs) and not match(f, excl)), None)
            if hit:
                why[m] = "%s is an input of its image build" % hit
                break
    return [m for m in smoke_all if m in why], why


# ---------------------------------------------------------------------------------------------
# Go build-cache groups: which actions/cache entry a leg restores, and which ONE leg saves it.
# ---------------------------------------------------------------------------------------------
# The repository cache is 10 GB, shared by every workflow. One entry per module (39 of them, a
# measured 16.6 GB of main-saved entries with the module cache included) cannot fit, and an
# over-full cache evicts continuously, which is how the hit rate fell to 27%. Modules that share
# a dependency stack share one entry instead: the build cache is content-addressed, so a service
# restoring the entry device-management saved reuses every compiled package they have in common
# (core and the whole third-party stack) and compiles only what is its own. Each group is SAVED
# by exactly one leg, the static leg of its saver, and only on a push to main (ci.yml), so no
# two legs race for a write-once key and no pull request writes an entry another cannot read.
# (group, saver module, [module globs]); the first group whose glob matches a module is its group.
#
# The groups of their own are the modules whose dependency stacks nothing else shares (helm and
# client-go, controller-runtime, kafka and the AWS SDK, the simulator's, the tools' and the edge
# agent's), so restoring the core or services entry would leave most of their build cold. Budget,
# from the entries main saved on run 44046620451 (module cache included) scaled by the ~58% that
# is build cache (device-state's module cache measured ~290 MB of its 683 MB entry): cli ~0.8 GB,
# k8s ~0.6, services ~0.4, outbound-connectors ~0.4, drdrill ~0.35, core ~0.3, simulator ~0.3,
# migrationdiff ~0.3, apiprobe ~0.25, edge agent ~0.2, ota-device ~0.15: about 4 GB, so the new
# generation a dependency bump creates fits beside the old one until LRU evicts it.
# Split shards get no key of their own: a shard is race-bound, and a hit measured ~0 minutes there.
CACHE_GROUPS = [
    ("cli", "backend/cli", ["backend/cli"]),
    ("k8s", "backend/k8s", ["backend/k8s"]),
    ("outbound-connectors", "backend/services/outbound-connectors", ["backend/services/outbound-connectors"]),
    ("services", "backend/services/device-management", ["backend/services/*"]),
    ("dc-simulator", "backend/sims/dc-simulator", ["backend/sims/dc-simulator"]),
    ("migrationdiff", "backend/tools/migrationdiff", ["backend/tools/migrationdiff"]),
    ("drdrill", "backend/tools/drdrill", ["backend/tools/drdrill"]),
    ("apiprobe", "backend/tools/apiprobe", ["backend/tools/apiprobe"]),
    ("dc-edge-agent", "backend/edge/dc-edge-agent", ["backend/edge/dc-edge-agent"]),
    ("ota-device", "examples/ota-device", ["examples/ota-device"]),
    ("core", "backend/core", ["*", "*/*", "*/*/*", "*/*/*/*"]),
]


def cache_group(module):
    for name, saver, globs in CACHE_GROUPS:
        if match(module, globs):
            return name, saver
    raise RuntimeError("no cache group for %s" % module)


FRONTEND_INERT = ["backend/**","deploy/**", "docs/**", "examples/**", "sdks/**", "demos/**", "_legacy/**", "*.md", "LICENSE", "NOTICE"]


def migrations_verdict(s, files, graph):
    """Sets s.migrations. A full selection always runs them; otherwise a changed file must reach
    a package MIGRATION_ROOTS compile, or be one of MIGRATION_INPUTS."""
    if s.full:
        s.migrations, s.migrations_why = True, "runs: everything runs"
        return
    if graph is None:
        s.migrations, s.migrations_why = True, "runs: no package graph to decide from"
        return
    closure = graph.closure(MIGRATION_ROOTS)
    if graph.missing:
        # An import of a workspace package that has no Go file left: the change deleted it, and a
        # build of the roots would now fail. "Cannot tell" runs the jobs.
        s.migrations, s.migrations_why = True, "runs: the roots import %s, which has no Go file in this tree" % sorted(graph.missing)[0]
        return
    readers = []
    for module, pkg, globs, excl, why in READERS:
        d = module if norm_pkg(pkg) == "." else module + "/" + norm_pkg(pkg)
        for root, recursive, use_readers in MIGRATION_ROOTS:
            if use_readers and (d == root or (recursive and d.startswith(root + "/"))):
                readers.append((globs, excl, "%s (%s)" % (d, why)))
    for f in files:
        for globs, excl, who in readers:
            if match(f, globs) and not match(f, excl):
                s.migrations, s.migrations_why = True, "runs: %s is read by %s, whose tests these jobs run" % (f, who)
                return
        if match(f, MIGRATION_INPUTS):
            s.migrations, s.migrations_why = True, "runs: %s is an input of migration-diff.sh" % f
            return
        if graph.hits(f, closure):
            s.migrations, s.migrations_why = True, "runs: %s is in a package the migration roots compile" % f
            return
    s.migrations = False
    s.migrations_why = "skipped: no changed file reaches the %d packages migration-diff.sh compiles, or its operand image" % len(closure)


# ---------------------------------------------------------------------------------------------
# The race step on a pull request: which packages it covers
# ---------------------------------------------------------------------------------------------
# The race detector was 59% of every Go leg's minutes, and on pull requests only 21% of those
# minutes were in a module with a changed Go file at all: a change to core raced every module that
# requires it, whole, on every pull request. On a pull request the race step therefore covers
# only the packages that hold a changed file and the packages that DIRECTLY import one of them
# (through their code or their tests), across modules. The plain `go test` step is untouched: it
# still runs every package of every selected leg. Every other event races everything, and the
# main run is the net for a race this narrower scope does not reach.
#
# 🔴 WHY ONE LEVEL OF IMPORTERS AND NOT THE CLOSURE. Replayed over 80 merged pull requests (70
# of them narrowable) with the measured per-leg race minutes, direct importers cut the race minutes
# by at least 30% (partial legs at full cost; ~42% scaling them by the share of packages raced),
# and the full reverse-import closure by at least 23% (~32%): the closure of a core package reaches
# most of the workspace. A race lives in code that runs
# concurrently, and the tests most likely to drive a changed package's code concurrently are its
# own and those of the packages written against its API, its direct importers. A race reachable
# only through a deeper chain surfaces on main's full run instead: that is the tradeoff the
# maintainer accepted, for this step only. Widening to the closure is a change to race_select's
# importer loop alone.
#
# A package whose tests READ a changed file through a READERS row races too, wherever it sits.
#
# FAIL SAFE, per module: a changed non-Go file (an embed, a fixture, a golden file, a Go file
# under testdata/) races its module in full, because any package of the module can read it
# through a relative path no graph records; so does a Go file in a directory that is no package
# of this tree (a package was deleted or moved). A full selection, no package graph, or any
# exception in here races everything.
_IGNORED_DIR = re.compile(r"(^|/)(testdata|vendor|node_modules)(/|$)|(^|/)[._][^/]*(/|$)")


def race_select(ws, s, files, graph):
    """Sets s.race ({module: set of package dirs}, or None for everything) and s.race_full."""
    s.race, s.race_full = None, {}
    if s.full:
        s.race_why = "full: everything runs"
        return
    if graph is None:
        s.race_why = "full: no package graph to decide from"
        return
    try:
        changed = set()
        readers = set()
        for f in files:
            # A package whose tests READ the file (a READERS row) races, whether or not its module
            # runs whole and whatever leg (a shard included) carries it: no import edge shows it.
            for module, pkg, globs, excl, _ in READERS:
                if match(f, globs) and not match(f, excl):
                    readers.add(module if norm_pkg(pkg) == "." else module + "/" + norm_pkg(pkg))
            owner = ws.owner(f)
            if not owner:
                continue
            d = os.path.dirname(f)
            if not f.endswith(".go") or _IGNORED_DIR.search("" if d == owner else os.path.relpath(d, owner).replace("\\", "/") + "/"):
                # A non-Go file (or a Go file go ignores, under testdata/ and the like): an embed,
                # a fixture or a golden file can be read by ANY package of its module, through a
                # relative path no graph records (dc-edge-agent's agent tests read ../dashboards).
                s.race_full.setdefault(owner, "%s is not Go source, and no graph says which tests read it" % f)
                continue
            if d not in graph.pkgs:
                s.race_full.setdefault(owner, "%s is in no package of this tree (a deleted or moved package?)" % f)
                continue
            changed.add(d)
        importers = set()
        for p, (imps, timps) in graph.pkgs.items():
            if changed & (imps | timps):
                importers.add(p)
        race = {}
        for p in changed | importers | readers:
            m = ws.owner(p)
            if m:
                race.setdefault(m, set()).add(p)
        s.race = race
        s.race_changed = changed
        s.race_why = "changed: %d changed package(s), %d direct importer(s), %d reader package(s), in %d module(s); %d module(s) in full" % (
            len(changed), len(importers - changed), len(readers - changed - importers), len(race), len(s.race_full))
    except Exception as e:  # noqa: BLE001 - doubt races everything
        s.race, s.race_full = None, {}
        s.race_why = "full: the race selection failed (%s: %s)" % (type(e).__name__, e)


def apply_race(s, go):
    """Annotate each go leg with race_scope (all | changed | none) and race_pkgs."""
    for leg in go:
        leg["race_scope"], leg["race_pkgs"] = "all", ""
        if s.race is None or s.full:
            continue
        m = leg["module"]
        if m in s.race_full or leg["leg"] == "readers":
            # Doubt, or a reader leg: its packages were selected because a file their tests read
            # changed, which no import edge describes. Its packages are few; race them all.
            continue
        mine = s.race.get(m, set())
        rel = {"." if p == m else os.path.relpath(p, m).replace("\\", "/") for p in mine}
        if leg["shard"]:
            # A shard runs one split package (its share of the tests): all of it, or none.
            if leg["pkgs"] not in {pkg_arg(r) for r in rel}:
                leg["race_scope"] = "none"
            continue
        split = {norm_pkg(p) for p in SPLITS.get(m, {})}
        want = sorted(r for r in rel if r not in split)
        if not want:
            leg["race_scope"] = "none"
        else:
            leg["race_scope"], leg["race_pkgs"] = "changed", " ".join(pkg_arg(r) for r in want)


def select(ws, changed_files, graph=None):
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
            s.everything("%s changed (a workspace, go.mod/go.sum, workflow or hack/ file)" % f)
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

    fr_hits = [f for f in files if match(f, FLEET_RUNNER_INPUTS)]
    s.fleet_runner = s.full or bool(fr_hits)
    s.fleet_runner_why = "runs: everything runs" if s.full else (("runs: " + fr_hits[0]) if fr_hits else "skipped: no changed file is a fleet-runner input (backend/fleet, the fleet roots, the gke-cluster module)")

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
    migrations_verdict(s, files, graph)
    race_select(ws, s, files, graph)
    return s


# ---------------------------------------------------------------------------------------------
# CodeQL: which modules the Go analysis builds on a pull request
# ---------------------------------------------------------------------------------------------
# Go extraction is tracer-based: CodeQL analyzes what the build compiles, and building a module
# compiles every workspace package it imports. So the analysis of a changed package needs its
# module built (its imports come with it) AND every module that requires it, transitively,
# because a taint path can start in a caller: a request handler in a service flowing into a
# changed sink in core. That is exactly the reverse-require closure `select` already computes.
#
# Only Go SOURCE matters to the Go analysis, so the seeds are the owners of changed .go files.
# Everything is analyzed when the inputs of the analysis itself change: dependency versions
# (go.work, go.work.sum, any go.mod/go.sum), the workflow and the scripts it runs, or a .go file
# outside every module (unknown, so assumed to matter). Push, schedule and dispatch are never
# narrowed: those runs are what keep main's code-scanning state whole-repository.
CODEQL_FULL = [
    "go.work",
    "go.work.sum",
    "**/go.mod",
    "**/go.sum",
    ".github/workflows/codeql.yml",
    "hack/ci-select.py",
    "hack/go-mod-download.sh",
]


def codeql_select(ws, files, graph=None):
    """(modules, packages, reasons). packages is {module: [package args]} naming exactly what to
    build, or None to build every selected module whole (./...). Every module, whole, when the
    analysis must cover everything."""
    files = [f.strip().replace("\\", "/") for f in files if f.strip()]
    if not files:
        return list(ws.dirs), None, ["the diff is empty, so there is nothing to select from"]
    full, seeds, pkg_seeds = [], {}, {}
    for f in files:
        if match(f, CODEQL_FULL):
            full.append("%s changed (a dependency version or the analysis itself)" % f)
        elif f.endswith(".go"):
            owner = ws.owner(f)
            if owner:
                seeds.setdefault(owner, f)
                d = os.path.dirname(f)
                if graph is not None:
                    if d in graph.pkgs:
                        pkg_seeds.setdefault(d, f)
                    elif not re.search(r"(^|/)(testdata|vendor)(/|$)|(^|/)[._][^/]*/", d + "/"):
                        # A Go file in a directory that is no longer a package: a package was
                        # deleted. Its importers changed too, but "cannot tell" analyzes everything.
                        full.append("%s is in no package of this tree (a deleted package?)" % f)
            elif not match(f, ["_legacy/**"]):
                full.append("%s is a Go file outside every workspace module" % f)
    if full:
        return list(ws.dirs), None, full
    if not seeds:
        # The workflow only triggers on Go-shaped changes, so reaching here with no module to
        # build means something unforeseen. An unknown selection is never an empty one.
        return list(ws.dirs), None, ["no changed Go file is in a workspace module, which the trigger should not allow"]
    if graph is None:
        # No package graph: the module-level answer. The callers are the reverse-require closure;
        # 🔴 AND EVERYTHING THOSE REQUIRE, because compiling an imported module is NOT extracting
        # it — the Go extractor extracts bodies only for the packages it was asked to build.
        callers = ws.closure(seeds)
        mods, todo = set(), list(callers)
        while todo:
            m = todo.pop()
            if m not in mods:
                mods.add(m)
                todo.extend(ws.requires.get(m, ()))
        return [m for m in ws.dirs if m in mods], None, ["%s: %s" % (m, "changed: " + seeds[m] if m in seeds else "requires or is required by a changed module") for m in sorted(mods)]
    # THE PACKAGE-LEVEL ANSWER. Every package that imports a changed one, transitively (the
    # callers, up to every main that links it), and then every package those import (the
    # callees, whose bodies a flow passes through). Every program that links a changed package
    # is therefore built whole, and each package is named as a build target so its bodies are
    # extracted rather than loaded for types. Only non-test imports count: `go build` compiles
    # no test, so CodeQL never sees one. A change to test files alone selects the package itself.
    rimports = {}
    for p, (imps, _) in graph.pkgs.items():
        for i in imps:
            rimports.setdefault(i, set()).add(p)
    callers, todo = set(), list(pkg_seeds)
    while todo:
        p = todo.pop()
        if p not in callers:
            callers.add(p)
            todo.extend(rimports.get(p, ()))
    build, todo = set(), list(callers)
    graph.missing = set()
    while todo:
        p = todo.pop()
        if p in build:
            continue
        if p not in graph.pkgs:
            graph.missing.add(p)
            continue
        build.add(p)
        todo.extend(graph.pkgs[p][0])
    if graph.missing:
        return list(ws.dirs), None, ["a selected package imports %s, which has no Go file in this tree" % sorted(graph.missing)[0]]
    pkgs = {}
    for p in sorted(build):
        m = ws.owner(p)
        rel = os.path.relpath(p, m).replace("\\", "/") if p != m else "."
        pkgs.setdefault(m, []).append(pkg_arg(rel).rstrip("/") if rel != "." else ".")
    why = ["%s: changed (%s)" % (p, pkg_seeds[p]) for p in sorted(pkg_seeds)]
    why.append("%d packages import those, transitively; %d packages are built in all" % (len(callers) - len(pkg_seeds), len(build)))
    return [m for m in ws.dirs if m in pkgs], pkgs, why


def cmd_codeql():
    """Prints one line per module to build on stdout, `<module dir><TAB><package args>`, where the
    args are `./...` for the whole module. The reasoning goes to stderr."""
    event = os.environ.get("GITHUB_EVENT_NAME", "")
    ws = load_workspace()
    pkgs = None
    if event != "pull_request":
        mods, why = list(ws.dirs), ["the event is %r: only a pull_request is narrowed" % event]
    else:
        try:
            files = changed_files(os.environ["PR_BASE_SHA"], os.environ["PR_HEAD_SHA"])
            print("changed files (%d):" % len(files), file=sys.stderr)
            for f in files:
                print("  " + f, file=sys.stderr)
            mods, pkgs, why = codeql_select(ws, files, PackageGraph(ws))
        except Exception as e:  # noqa: BLE001 - any failure to select means analyze everything
            print("::warning::CodeQL module selection failed (%s: %s); analyzing everything" % (type(e).__name__, e), file=sys.stderr)
            mods, pkgs, why = list(ws.dirs), None, ["the selection itself failed: %s" % e]
    if not mods:
        raise RuntimeError("selected no modules")
    full = pkgs is None and sorted(mods) == sorted(ws.dirs)
    if event != "pull_request" and not full:
        raise RuntimeError("a %s run selected a subset" % event)
    print("CODEQL: %s (%d of %d modules)" % ("every module, whole" if full else "a subset", len(mods), len(ws.dirs)), file=sys.stderr)
    for w in why:
        print("  " + w, file=sys.stderr)
    for m in ws.dirs:
        if m not in mods:
            print("  SKIPPED  %s: no package in it imports, or is imported by an importer of, a changed package" % m, file=sys.stderr)
    for m in mods:
        print("%s	%s" % (m, "./..." if pkgs is None else " ".join(pkgs[m])))


# ---------------------------------------------------------------------------------------------
# migration-deps: the source-parsed closure must contain everything `go list` says is compiled
# ---------------------------------------------------------------------------------------------
def cmd_migration_deps():
    ws = load_workspace()
    graph = PackageGraph(ws)
    mine = graph.closure(MIGRATION_ROOTS)
    theirs = set()
    for d, recursive, _ in MIGRATION_ROOTS:
        owner = ws.owner(d)
        rel = os.path.relpath(os.path.join(ROOT, d), os.path.join(ROOT, owner)).replace("\\", "/")
        pattern = ("./" + rel + "/...") if recursive else ("./" + rel)
        if rel == "." and recursive:
            pattern = "./..."
        out = subprocess.check_output(
            ["go", "list", "-deps", "-test", "-tags", "integration", "-f", "{{if not .Standard}}{{.Dir}}{{end}}", pattern],
            cwd=os.path.join(ROOT, owner), text=True)
        for line in out.splitlines():
            line = line.strip()
            if not line:
                continue
            rel = os.path.relpath(line, ROOT).replace("\\", "/")
            if not rel.startswith("..") and ws.owner(rel):
                theirs.add(rel)
    if not theirs:
        print("::error::go list reported no workspace package for the migration roots; this check examined nothing")
        sys.exit(1)
    missing = sorted(theirs - mine)
    if missing:
        for p in missing:
            print("::error::the migrations jobs compile %s, but hack/ci-select.py's import graph does not reach it, so a change there would skip them" % p)
        sys.exit(1)
    print("migration roots: go list compiles %d workspace packages, all inside the %d the selection watches" % (len(theirs), len(mine)))


def changed_files(base, head):
    mb = subprocess.check_output(["git", "merge-base", base, head], cwd=ROOT, text=True).strip()
    out = subprocess.check_output(["git", "diff", "--name-only", "--no-renames", mb, head], cwd=ROOT, text=True)
    return [l for l in out.splitlines() if l.strip()]


# ---------------------------------------------------------------------------------------------
# Plan: go legs, integration legs
# ---------------------------------------------------------------------------------------------
def legs_for(module, pkgs=None):
    """pkgs None = the whole module (static checks + every package); otherwise just those packages."""
    out = _legs_for(module, pkgs)
    group, saver = cache_group(module)
    for leg in out:
        leg["cache_group"] = group
        # The one leg that may save its group's entry (ci.yml saves only on a push to main).
        leg["cache_save"] = bool(leg["static"] and module == saver)
    return out


def _legs_for(module, pkgs=None):
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


def plan(ws, s, integration_plan, smoke_all=(), files=()):
    """(go legs, integration legs, smoke legs). A draft pull request plans none of the three: they
    run when it is marked ready for review, and ci-complete fails a draft run (see ci.yml)."""
    if s.draft:
        s.smoke_why = {}
        return [], [], []
    go = []
    for m in ws.dirs:
        if m in s.modules:
            go.extend(legs_for(m))
        elif m in s.readers:
            go.extend(legs_for(m, list(s.readers[m])))
    apply_race(s, go)
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
        leg["cache_group"] = cache_group(mods[0])[0]
        integ.append(leg)
    smoke, s.smoke_why = smoke_select(s, files, smoke_all)
    return go, integ, smoke


def report(ws, s, go, integ, integration_plan, smoke=(), smoke_all=(), out=sys.stderr):
    p = lambda *a: print(*a, file=out)
    if s.draft:
        p("DRAFT: the pull request is a draft, so the go, integration, image-smoke and migrations jobs")
        p("  are not planned. They run when it is marked ready for review; until then ci-complete fails.")
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
        p("  %-45s %-24s static=%s %s%s  race=%s%s" % (g["module"], g["leg"] or "-", str(g["static"]).lower(), g["pkgs"] or "./...", (" minus " + g["exclude"]) if g["exclude"] else "",
                                                      g.get("race_scope", "all"), (" " + g["race_pkgs"]) if g.get("race_pkgs") else ""))
    p("race: " + s.race_why)
    for m, why in sorted(s.race_full.items()):
        p("  full     %-45s %s" % (m, why))
    kept = {l["name"] for l in integ}
    p("integration legs: %d of %d" % (len(integ), len(integration_plan)))
    for l in integration_plan:
        p("  %s %-20s %s" % ("run " if l["name"] in kept else "SKIP", l["name"], l["modules"]))
    p("image-smoke legs: %d of %d" % (len(smoke), len(smoke_all)))
    why = getattr(s, "smoke_why", {})
    for m in smoke_all:
        p("  %s %-45s %s" % ("run " if m in smoke else "SKIP", m, why.get(m, "its module does not run and no image input changed")))
    p("frontend: " + s.frontend_why)
    p("migrations: " + s.migrations_why)
    p("fleet-runner: " + s.fleet_runner_why)


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
            s = select(ws, files, PackageGraph(ws))
        except Exception as e:  # noqa: BLE001 - any failure to select means run everything
            print("::warning::affected-module selection failed (%s: %s); running everything" % (type(e).__name__, e), file=sys.stderr)
            s = full_selection(ws, "the selection itself failed: %s" % e)
            files = []
        # 🔴 A draft skips the heavy jobs, and only an explicit "true" makes one: anything else
        # (missing, empty, malformed) runs them. ci-complete FAILS a draft run, so its green can
        # never stand in for the heavy jobs once the pull request is marked ready.
        if os.environ.get("PR_DRAFT", "") == "true":
            mark_draft(s)
    # The full image list, cross-checked against the tree in discover. Every leg off a pull request.
    smoke_all = json.loads(os.environ["SMOKE_MODULES"])
    if not smoke_all:
        raise RuntimeError("SMOKE_MODULES is empty: no image module to plan from")
    go, integ, smoke = plan(ws, s, integration_plan, smoke_all, files if event == "pull_request" else [])
    if s.full and not s.draft and len({g["module"] for g in go}) != len(ws.dirs):
        raise RuntimeError("a full run planned %d modules of %d" % (len({g["module"] for g in go}), len(ws.dirs)))
    if s.full and not s.draft and len(integ) != len(integration_plan):
        raise RuntimeError("a full run planned %d integration legs of %d" % (len(integ), len(integration_plan)))
    if s.full and not s.draft and smoke != smoke_all:
        raise RuntimeError("a full run planned %d image-smoke legs of %d" % (len(smoke), len(smoke_all)))
    if (s.full or event != "pull_request") and any(g["race_scope"] != "all" for g in go):
        raise RuntimeError("a %s run narrowed the race step" % ("full" if s.full else event))
    if event != "pull_request" and s.draft:
        raise RuntimeError("a %s run was planned as a draft" % event)
    report(ws, s, go, integ, integration_plan, smoke, smoke_all)
    if s.full and not s.draft and not s.migrations:
        raise RuntimeError("a full run skipped the migrations jobs")
    print(json.dumps({"go": go, "integration": integ, "smoke": smoke, "frontend": s.frontend, "migrations": s.migrations, "fleet_runner": s.fleet_runner,
                      "full": s.full, "draft": s.draft}, separators=(",", ":")))


def mark_draft(s):
    """A draft pull request: plan none of the heavy jobs (go, integration, image-smoke, migrations)."""
    s.draft = True
    s.migrations, s.migrations_why = False, "skipped: the pull request is a draft (runs when it is marked ready)"
    s.fleet_runner, s.fleet_runner_why = False, "skipped: the pull request is a draft (runs when it is marked ready)"


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

    # Every module has a cache group, and every group's saver is a workspace module in that group:
    # a saver that left the workspace would leave its group never saved, silently.
    for module in ws.dirs:
        try:
            cache_group(module)
        except RuntimeError as e:
            errs.append(str(e))
    for name, saver, _ in CACHE_GROUPS:
        if saver not in ws.dirs:
            errs.append("CACHE_GROUPS %s: saver %s is not a workspace module" % (name, saver))
        elif cache_group(saver)[0] != name:
            errs.append("CACHE_GROUPS %s: saver %s belongs to group %s" % (name, saver, cache_group(saver)[0]))
    for module, _, _ in SMOKE_INPUTS:
        if module != "*" and module not in ws.dirs:
            errs.append("SMOKE_INPUTS names %s, which go.work does not declare" % module)

    def allowed(module, pkg):
        if (module, pkg) in declared:
            return True
        for (m, p) in IN_MODULE:
            if m == module and fnmatch.fnmatchcase(pkg, p):
                return True
        return False

    # 🔴 PER FILE, NOT ONLY PER PACKAGE. A package that already has a READERS row would accept a
    # new file reading something the row does not name (core/config gained a docs reader that way,
    # beside its row for a .tf file). So every escaping file is also pinned by path in
    # hack/ci-select-escapes.txt: a file not listed there fails until someone has read what it
    # reads, extended READERS to match, and listed it; a listed file that no longer escapes is stale.
    known = set()
    with open(os.path.join(ROOT, "hack", "ci-select-escapes.txt"), encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if line and not line.startswith("#"):
                known.add(line)
    seen = set()
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
                    if not ESCAPE_RE.search(fh.read()):
                        continue
                path = module + "/" + (n if rel == "." else rel + "/" + n)
                seen.add(path)
                if not allowed(module, norm_pkg(rel)):
                    errs.append("%s may read outside its module: add a READERS row for (%s, %s) or an IN_MODULE reason" % (path, module, norm_pkg(rel)))
                elif path not in known:
                    errs.append("%s may read outside its module and is not in hack/ci-select-escapes.txt: check that READERS names what it reads, then list it" % path)
    for path in sorted(known - seen):
        errs.append("hack/ci-select-escapes.txt lists %s, which no longer exists or no longer reaches outside its module" % path)

    # The frontend job's reads outside frontend/: every string literal in its sources naming a path
    # under another top-level tree must fall inside FRONTEND_INPUTS, or a change there would skip it.
    lit = re.compile(r"""['"`]((?:\.\./)*(?:backend|docs|deploy|hack|sdks|demos|examples|testdata)/[A-Za-z0-9_./*-]+)['"`]""")
    for dirpath, dirnames, filenames in os.walk(os.path.join(ROOT, "frontend")):
        dirnames[:] = [d for d in dirnames if d not in ("node_modules", "dist", "build", "coverage") and not d.startswith(".")]
        for n in filenames:
            if not n.endswith((".ts", ".tsx", ".js", ".mjs", ".cjs", ".mts")):
                continue
            src = os.path.join(dirpath, n)
            with open(src, encoding="utf-8", errors="replace") as fh:
                text = fh.read()
            for m in lit.finditer(text):
                raw = m.group(1)
                if raw.startswith(".."):
                    target = os.path.normpath(os.path.join(dirpath, raw))
                else:
                    # a bare literal is either repo-root-relative (tierConfig.test.ts) or relative to
                    # a frontend directory (board-replay's testdata/...): only the first can be outside.
                    target = os.path.join(ROOT, raw.split("*")[0].rstrip("/"))
                    if not os.path.exists(target):
                        continue
                rel = os.path.relpath(target, ROOT).replace("\\", "/")
                if rel.startswith("..") or rel.startswith("frontend/"):
                    continue
                probe = rel.replace("*", "x")
                if not (match(probe, FRONTEND_INPUTS) or match(probe + "/x", FRONTEND_INPUTS)):
                    errs.append("%s names %s, outside FRONTEND_INPUTS: a change there would skip the frontend job" % (os.path.relpath(src, ROOT).replace("\\", "/"), rel))
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
    graph = PackageGraph(ws)
    integration_plan = json.loads(subprocess.check_output(["bash", os.path.join(ROOT, "hack", "integration-tests.sh"), "--matrix"], cwd=ROOT, text=True))
    # The image list as discover builds it: every service with a main.go, plus the four fixed images.
    smoke_all = sorted("backend/services/" + d for d in os.listdir(os.path.join(ROOT, "backend", "services"))
                       if os.path.exists(os.path.join(ROOT, "backend", "services", d, "main.go")))
    smoke_all += ["backend/k8s", "backend/edge/dc-edge-agent", "backend/admin/dc-broker-admin", "backend/broker/dc-nats-server"]
    for sha in shas:
        title = subprocess.check_output(["git", "log", "-1", "--format=%h %s", sha], cwd=ROOT, text=True).strip()
        files = changed_files(sha + "^", sha)
        s = select(ws, files, graph)
        go, integ, smoke = plan(ws, s, integration_plan, smoke_all, files)
        cq, cqp, _ = codeql_select(ws, files, graph)
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
        scopes = [g["race_scope"] for g in go]
        print("  race: %s; legs all=%d changed=%d none=%d" % (s.race_why, scopes.count("all"), scopes.count("changed"), scopes.count("none")))
        print("  image-smoke legs: %d of %d (%s)" % (len(smoke), len(smoke_all), " ".join(m.split("/")[-1] for m in smoke) or "-"))
        print("  migrations: %s   codeql: %s" % (
            "run" if s.migrations else "skip",
            ("all %d modules" % len(cq)) if cqp is None and len(cq) == len(ws.dirs) else ("%d packages in %d modules (%s)" % (
                sum(len(v) for v in (cqp or {}).values()) if cqp else -1, len(cq), " ".join(m.split("/")[-1] for m in cq)))))


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

    print("==> repository reader regressions")
    reader_ws = Workspace(
        ["backend/cli", "backend/services/event-sources", "deploy"],
        {"backend/cli": {"deploy"}, "backend/services/event-sources": set(), "deploy": set()},
    )
    for path, module, pkg in [
        ("deploy/helm/devicechain/templates/mqtt-exposure.yaml", "backend/services/event-sources", "processor"),
        ("deploy/helm/devicechain/templates/gateway.yaml", "backend/services/event-sources", "processor"),
        ("README.md", "backend/cli", "bootstrap"),
    ]:
        s = select(reader_ws, [path])
        expect(not s.full and pkg in s.readers.get(module, {}),
               "%s alone selects %s/%s" % (path, module, pkg))
        legs = plan(reader_ws, s, [])[0]
        expect(any(leg["module"] == module and "./%s/" % pkg in leg["pkgs"].split() for leg in legs),
               "%s alone plans the %s tests" % (path, pkg))

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
        for f, what, why in [("go.work", "go.work", "workspace, go.mod/go.sum, workflow or hack/"),
                             ("go.work.sum", "go.work.sum", "workspace, go.mod/go.sum, workflow or hack/"),
                             (".github/workflows/ci.yml", ".github", "workspace, go.mod/go.sum, workflow or hack/"),
                             ("hack/go-race.sh", "hack/", "workspace, go.mod/go.sum, workflow or hack/"),
                             ("backend/tools/tool/go.mod", "a go.mod (shared dependency versions)", "workspace, go.mod/go.sum, workflow or hack/"),
                             ("backend/services/svc-a/go.sum", "a go.sum", "workspace, go.mod/go.sum, workflow or hack/"),
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

        print("==> fleet-runner")
        expect(not select(ws, ["docs/x.md"]).fleet_runner, "a docs change skips the fleet runner")
        expect(not select(ws, ["backend/core/x.go"]).fleet_runner, "a core Go change that is not go.mod/go.sum skips the fleet runner")
        expect(select(ws, ["backend/core/go.mod"]).fleet_runner, "the image copies core's go.mod: it runs the fleet runner")
        expect(select(ws, ["backend/core/rdb/x.go"]).fleet_runner, "the runner imports core/rdb: a core code change runs the fleet runner")
        expect(select(ws, ["deploy/fleet/gke-pg/main.tf"]).fleet_runner, "a fleet root change runs the fleet runner")
        print("==> frontend")
        expect(select(ws, ["frontend/apps/x.ts"]).frontend, "a frontend change runs frontend")
        expect(select(ws, ["backend/services/svc-a/graphql/schema.graphql"]).frontend, "a schema change runs frontend")
        expect(not select(ws, ["backend/core/x.go"]).frontend, "a core-only change skips frontend")

        print("==> migrations (a package graph over a fake tree on disk)")
        import tempfile
        global MIGRATION_ROOTS
        saved_roots = MIGRATION_ROOTS
        ws.modpaths = {"example.com/core": "backend/core", "example.com/tool": "backend/tools/tool",
                       "example.com/svc-a": "backend/services/svc-a", "example.com/svc-b": "backend/services/svc-b",
                       "example.com/cli": "backend/cli", "example.com/deploy": "deploy"}
        tree = {
            # the root, recursive: rdb and rdb/sub
            "backend/core/rdb/rdb.go": 'package rdb\n\nimport (\n\t"fmt"\n\tsn "example.com/core/sqlnull"\n)\n\nvar _ = fmt.Sprint\nvar _ = sn.X\n',
            "backend/core/rdb/sub/sub.go": 'package sub\n',
            # a root's TEST import is compiled into its test binary
            "backend/core/rdb/rdb_test.go": 'package rdb_test\n\nimport _ "example.com/core/testonly"\n',
            "backend/core/rdb/testdata/fixture.sql": "",
            # sqlnull -> leaf: transitive
            "backend/core/sqlnull/n.go": 'package sqlnull\n\nimport "example.com/core/leaf"\n\nvar X = leaf.Y\n',
            "backend/core/leaf/l.go": 'package leaf\n\nvar Y = 1\n',
            # a NON-root's test import is not compiled by anything the jobs run
            "backend/core/sqlnull/n_test.go": 'package sqlnull\n\nimport _ "example.com/core/deep"\n',
            "backend/core/deep/d.go": 'package deep\n',
            "backend/core/testonly/t.go": 'package testonly\n',
            # imported only from a comment, and not at all
            "backend/core/messaging/m.go": 'package messaging\n\n// import "example.com/core/rdb"\n',
            "backend/services/svc-a/x.go": 'package svca\n\n/* import "example.com/core/messaging" */\n',
            "backend/tools/tool/main.go": 'package main\n\nimport "example.com/core/rdb/sub"\n\nfunc main() {}\n',
            "backend/cli/cli.go": 'package cli\n',
        }
        with tempfile.TemporaryDirectory() as tmp:
            for rel, body in tree.items():
                os.makedirs(os.path.join(tmp, os.path.dirname(rel)), exist_ok=True)
                with open(os.path.join(tmp, rel), "w", encoding="utf-8") as fh:
                    fh.write(body)
            MIGRATION_ROOTS = [("backend/core/rdb", True, True), ("backend/tools/tool", True, True), ("backend/cli", False, False)]
            try:
                g = PackageGraph(ws, root=tmp)
                expect(go_imports(tree["backend/core/rdb/rdb.go"]) == {"fmt", "example.com/core/sqlnull"},
                       "an aliased import in a block is read: %s" % go_imports(tree["backend/core/rdb/rdb.go"]))
                mig = lambda *fs: select(ws, list(fs), g).migrations
                expect(mig("backend/core/leaf/l.go"), "a package the roots reach TRANSITIVELY runs the migrations jobs")
                expect(mig("backend/core/testonly/t.go"), "a root's TEST import runs them")
                expect(mig("backend/core/rdb/sub/sub.go"), "a package under a recursive root runs them")
                expect(mig("backend/core/rdb/testdata/fixture.sql"), "a fixture under a root package runs them")
                expect(mig("deploy/images/timescaledb/build.sh"), "the operand image runs them")
                expect(mig("hack/migration-diff.sh"), "a full selection runs them")
                expect(mig("backend/core/messaging/m.go", "backend/core/rdb/rdb.go"), "any one reaching file is enough")
                # THE COUNTERWEIGHTS: "always run" passes every case above.
                expect(not mig("backend/core/deep/d.go"), "a non-root's test import does NOT run them")
                expect(not mig("backend/core/messaging/m.go"), "a package nothing imports (or imports in a comment) does NOT run them")
                expect(not mig("docs/x.md", "frontend/a.ts"), "docs and frontend do NOT run them")
                expect(select(ws, ["backend/core/leaf/l.go"]).migrations, "with no package graph, they run")
                # READERS of a package whose tests the jobs RUN count; of one they only compile, not.
                READERS.append(("backend/core", "rdb/sub", ["backend/services/*/model/*.go"], ["**/*_test.go"], "t"))
                READERS.append(("backend/cli", ".", ["frontend/**"], [], "t"))
                expect(mig("backend/services/svc-b/model/m.go"), "a file a run root package's tests READ runs them")
                expect(not mig("backend/services/svc-b/model/m_test.go"), "...but not one its row excludes")
                expect(not mig("frontend/a.ts"), "a reader row of a root whose tests are -run filtered does not")
                # CodeQL at package level: the importers of a changed package (transitively) and
                # everything they import, each named; nothing that neither calls nor is called.
                cqp = lambda *fs: codeql_select(ws, list(fs), g)[1]
                expect(cqp("backend/core/leaf/l.go") == {"backend/core": ["./leaf", "./rdb", "./sqlnull"]},
                       "codeql builds a changed package, its transitive importers and their imports: %s" % cqp("backend/core/leaf/l.go"))
                expect(cqp("backend/core/rdb/sub/sub.go") == {"backend/core": ["./rdb/sub"], "backend/tools/tool": ["."]},
                       "...across modules, up to the main that links it: %s" % cqp("backend/core/rdb/sub/sub.go"))
                expect(cqp("backend/services/svc-a/x.go") == {"backend/services/svc-a": ["."]}, "a package nothing imports builds alone")
                expect(codeql_select(ws, ["backend/core/gone/x.go"], g)[1] is None, "a Go file in no package (deleted?) analyzes everything")
                # THE RACE SCOPE on a pull request: changed packages plus their DIRECT importers,
                # plus the packages whose tests read a changed file. One reader row, on a SPLIT
                # package of a module that also runs whole, is all the race cases below see.
                saved_readers = READERS
                READERS = [("backend/core", "messaging", ["docs/nats.md"], [], "t"),
                           ("backend/core", "leaf", ["docs/l*"], ["docs/lx.md"], "t")]
                def race(*fs):
                    sel = select(ws, list(fs), g)
                    return None if sel.race is None else {m: sorted(v) for m, v in sel.race.items()}, sel.race_full
                expect(race("backend/core/leaf/l.go") == ({"backend/core": ["backend/core/leaf", "backend/core/sqlnull"]}, {}),
                       "a changed package races with its direct importer, not the importer's importers: %s" % (race("backend/core/leaf/l.go"),))
                expect(race("backend/core/testonly/t.go")[0] == {"backend/core": ["backend/core/rdb", "backend/core/testonly"]},
                       "an importer through its TESTS counts: %s" % (race("backend/core/testonly/t.go"),))
                expect(race("backend/core/rdb/sub/sub.go")[0] == {"backend/core": ["backend/core/rdb/sub"], "backend/tools/tool": ["backend/tools/tool"]},
                       "importers are followed across modules: %s" % (race("backend/core/rdb/sub/sub.go"),))
                expect("backend/core" in race("backend/core/rdb/testdata/fixture.sql")[1],
                       "a fixture races its module in full: any package may read it by a relative path")
                expect(race("backend/core/rdb/testdata/gen.go")[1].get("backend/core", "").endswith("is not Go source, and no graph says which tests read it"),
                       "so does a Go file go ignores (testdata), as an input rather than a deleted package")
                expect(race("docs/ly.md")[0] == {"backend/core": ["backend/core/leaf"]} and race("docs/lx.md") == ({}, {}),
                       "a reader row's exclusions hold for the race scope too")
                expect("backend/core" in race("backend/core/rdb/schema.graphql")[1], "so does a non-Go file inside a package (an embed)")
                expect(race("docs/nats.md") == ({"backend/core": ["backend/core/messaging"]}, {}),
                       "a file a package's tests READ races that package: %s" % (race("docs/nats.md"),))
                expect(race("docs/nats.md", "backend/core/leaf/l.go")[0] == {"backend/core": ["backend/core/leaf", "backend/core/messaging", "backend/core/sqlnull"]},
                       "...also when its module runs whole for another change")
                expect(race("backend/core/deep/d_test.go")[0] == {"backend/core": ["backend/core/deep", "backend/core/sqlnull"]},
                       "a changed test file races its package and the packages whose tests import it")
                expect(race("docs/x.md", "frontend/a.ts") == ({}, {}), "a change outside every module that no package reads races nothing")
                expect(race("backend/core/gone/x.go")[1].get("backend/core", "").startswith("backend/core/gone/x.go is in no package"),
                       "a Go file in no package (deleted?) races its module in full: %s" % (race("backend/core/gone/x.go")[1],))
                expect("backend/core" in race("backend/core/NOTES.txt")[1], "a module-level non-Go file races its module in full")
                expect(race("hack/x.sh") == (None, {}), "a full selection races everything")
                expect(select(ws, ["backend/core/leaf/l.go"]).race is None, "no package graph races everything")
                # Each layer refuses a narrowed full run on its own, not only through the other.
                full = full_selection(ws, "push")
                race_select(ws, full, ["backend/core/leaf/l.go"], g)
                expect(full.race is None, "race_select on a full selection races everything, whatever changed")
                full.race = {}
                expect(all(x["race_scope"] == "all" for x in plan(ws, full, [])[0]), "apply_race never narrows a full selection")
                broken = select(ws, ["backend/core/leaf/l.go"], g)
                race_select(ws, broken, ["backend/core/leaf/l.go"], object())  # a graph that raises
                expect(broken.race is None and broken.race_why.startswith("full: the race selection failed"),
                       "a race selection that raises races everything: %s" % broken.race_why)
                SPLITS = {"backend/core": {"messaging": 3}}
                sc = {(x["module"], x["leg"]): x["race_scope"] for x in plan(ws, select(ws, ["docs/nats.md", "backend/core/leaf/l.go"], g), [])[0]}
                expect([sc[("backend/core", "messaging %d/3" % i)] for i in (1, 2, 3)] == ["all"] * 3,
                       "the shards of a split READER package race when a file it reads changes: %s" % sc)
                def scopes(*fs):
                    go_, _, _ = plan(ws, select(ws, list(fs), g), [])
                    return {(x["module"], x["leg"]): (x["race_scope"], x["race_pkgs"]) for x in go_}
                sc = scopes("backend/core/messaging/m.go")
                expect([sc[("backend/core", "messaging %d/3" % i)][0] for i in (1, 2, 3)] == ["all"] * 3 and sc[("backend/core", "rest")] == ("none", ""),
                       "a changed split package races every shard of it and not the catch-all: %s" % sc)
                expect(sc[("backend/services/svc-a", "")] == ("none", "") and sc[("backend/cli", "")] == ("none", ""),
                       "a module selected only for REQUIRING a changed one races nothing: %s" % sc)
                sc = scopes("backend/core/leaf/l.go")
                expect([sc[("backend/core", "messaging %d/3" % i)][0] for i in (1, 2, 3)] == ["none"] * 3
                       and sc[("backend/core", "rest")] == ("changed", "./leaf/ ./sqlnull/"),
                       "the catch-all races exactly the changed packages and their importers, minus the split ones: %s" % sc)
                sc = scopes("backend/services/svc-a/x.go")
                expect(sc[("backend/services/svc-a", "")] == ("changed", ".") and sc[("backend/services/svc-b", "")] == ("none", ""),
                       "a module-root package races as '.', and its dependant module, importing nothing of it, races nothing: %s" % sc)
                sc = scopes("backend/core/gone/x.go", "backend/services/svc-a/x.go")
                expect(all(v == ("all", "") for (m, _), v in sc.items() if m == "backend/core") and sc[("backend/services/svc-a", "")] == ("changed", "."),
                       "doubt is per module: core races in full, svc-a only what changed: %s" % sc)
                READERS = saved_readers
                sc = scopes("docs/docs/x.md")
                expect(sc.get(("backend/cli", "readers")) == ("all", ""), "a readers leg races its packages in full: %s" % sc)
                sc = {k: v for k, v in scopes("hack/x.sh").items()}
                expect(sc and all(v == ("all", "") for v in sc.values()), "a full selection races every leg in full")
                s_full = full_selection(ws, "push")
                expect(all(x["race_scope"] == "all" for x in plan(ws, s_full, [])[0]), "a non-pull-request plan races every leg in full")
                SPLITS = {"backend/core": {"messaging": 3}}
                # A deleted package that a root still imports: unknown, so they run.
                with open(os.path.join(tmp, "backend/core/leaf/l2.go"), "w", encoding="utf-8") as fh:
                    fh.write('package leaf\n\nimport "example.com/core/gone"\n')
                g2 = PackageGraph(ws, root=tmp)
                expect(select(ws, ["docs/x.md"], g2).migrations, "an import of a package with no Go file left runs them")
                expect(codeql_select(ws, ["backend/core/leaf/l.go"], g2)[1] is None, "...and analyzes everything")
                MIGRATION_ROOTS = [("backend/core/nosuch", True, True)]
                try:
                    select(ws, ["backend/core/leaf/l.go"], g)
                    expect(False, "a root with no package raises (and so fails safe in cmd_plan)")
                except RuntimeError:
                    expect(True, "a root with no package raises (and so fails safe in cmd_plan)")
            finally:
                MIGRATION_ROOTS = saved_roots

        print("==> codeql")
        cq = lambda *fs: set(codeql_select(ws, list(fs))[0])  # no graph: the module-level fallback
        everything = set(ws.dirs)
        expect(cq("backend/core/x.go") == {"backend/core", "backend/services/svc-a", "backend/services/svc-b", "backend/cli", "deploy"},
               "a core change analyzes core, everything that requires it, transitively, and what those require")
        expect(cq("backend/services/svc-a/h.go") == {"backend/services/svc-a", "backend/services/svc-b", "backend/core"},
               "a service change analyzes it, its dependants and the modules it requires (their bodies)")
        expect(cq("backend/services/svc-b/x.go") == {"backend/services/svc-b", "backend/services/svc-a", "backend/core"},
               "the required modules are followed transitively")
        expect(cq("backend/services/svc-a/README.md", "backend/tools/tool/x.go") == {"backend/tools/tool"},
               "a non-Go file seeds nothing")
        expect(cq("_legacy/x.go", "backend/tools/tool/x.go") == {"backend/tools/tool"}, "_legacy/ is never analyzed")
        for f, what in [("backend/tools/tool/go.mod", "a go.mod"), ("go.work.sum", "go.work.sum"),
                        (".github/workflows/codeql.yml", "the workflow"), ("hack/ci-select.py", "the selection"),
                        ("mystery/x.go", "a Go file outside every module")]:
            expect(cq("backend/services/svc-b/x.go", f) == everything, "%s analyzes every module" % what)
        expect(cq() == everything, "an empty diff analyzes every module")
        expect(cq("docs/x.md") == everything, "no Go seed at all analyzes every module, never none")

        print("==> plan")
        integ_plan = [{"name": "core", "modules": "backend/core", "cache": ""},
                      {"name": "small", "modules": "backend/services/svc-a backend/tools/tool", "cache": ""}]
        s = select(ws, ["backend/services/svc-b/x.go"])
        go, integ, _ = plan(ws, s, integ_plan)
        expect([g["module"] for g in go] == ["backend/core", "backend/services/svc-b"] and go[0]["pkgs"] == "./test/" and not go[0]["static"],
               "svc-b alone runs svc-b whole and core's repo-walking test package: %s" % [(g["module"], g["pkgs"]) for g in go])
        expect(integ == [], "and no integration leg (neither is integration-selected): %s" % integ)
        s = select(ws, ["backend/services/svc-a/x.go"])
        go, integ, _ = plan(ws, s, integ_plan)
        expect([l["modules"] for l in integ] == ["backend/services/svc-a"], "the small leg narrows to the selected module: %s" % integ)
        s = full_selection(ws, "test")
        go, integ, _ = plan(ws, s, integ_plan)
        core = [g for g in go if g["module"] == "backend/core"]
        expect([g["leg"] for g in core] == ["messaging 1/3", "messaging 2/3", "messaging 3/3", "rest"]
               and core[-1]["static"] and core[-1]["exclude"] == "./messaging/" and not any(g["static"] for g in core[:-1]),
               "a split module runs N shards plus one static catch-all excluding the split package")
        expect(sum(1 for g in go if g["static"]) == len(ws.dirs), "a full run has exactly one static leg per module")
        expect([(l["name"], l["modules"]) for l in integ] == [(l["name"], l["modules"]) for l in integ_plan],
               "a full run keeps every integration leg")

        print("==> image-smoke")
        smoke_all = ["backend/services/svc-a", "backend/services/svc-b", "backend/services/user-management", "backend/k8s"]
        sm = lambda *fs: plan(ws, select(ws, list(fs)), integ_plan, smoke_all, list(fs))[2]
        expect(sm("backend/core/x.go") == ["backend/services/svc-a", "backend/services/svc-b"],
               "a core change smokes every image that requires core, transitively: %s" % sm("backend/core/x.go"))
        expect(sm("backend/services/svc-b/x.go") == ["backend/services/svc-b"], "a service change smokes that image alone")
        expect(sm("docs/docs/x.md", "README.md", "frontend/a.ts") == [], "a docs/frontend change smokes nothing")
        expect(sm("backend/tools/tool/x.go") == [], "a module no image requires smokes nothing")
        expect(sm("backend/cli/bootstrap/breakglass.go") == ["backend/services/user-management", "backend/k8s"],
               "breakglass.go smokes user-management (its exec path) and k8s (the ko-wrapper scan): %s" % sm("backend/cli/bootstrap/breakglass.go"))
        expect(sm("backend/cli/bootstrap/x_test.go") == [], "a cli TEST file is no image input")
        expect(sm("deploy/helm/x.yaml") == ["backend/k8s"], "a deploy/ change smokes k8s (the ko-wrapper scan reads deploy/)")
        for f in ["hack/ko-build.sh", ".github/workflows/ci.yml", "backend/services/svc-a/go.mod", "go.work", ".ko.yaml"]:
            expect(sm(f) == smoke_all, "%s smokes every image" % f)
        s = select(ws, ["backend/core/x.go"])
        expect(smoke_select(s, ["mystery.txt"], smoke_all)[0] == ["backend/services/svc-a", "backend/services/svc-b"], "smoke_select reads the module set")
        s.full = True
        expect(smoke_select(s, [], smoke_all)[0] == smoke_all, "a full selection smokes every image")
        expect(smoke_select(select(ws, ["x/.ko.yaml"]), ["x/.ko.yaml"], smoke_all)[0] == smoke_all,
               "an unknown file runs everything, so every image")

        print("==> draft")
        s = select(ws, ["backend/core/x.go"])
        mark_draft(s)
        go, integ, sk = plan(ws, s, integ_plan, smoke_all, ["backend/core/x.go"])
        expect(go == [] and integ == [] and sk == [] and not s.migrations and s.draft and s.frontend is False,
               "a draft plans no go, integration, smoke or migrations job: %s %s %s %s" % (len(go), len(integ), len(sk), s.migrations))
        s = full_selection(ws, "test")
        mark_draft(s)
        expect(plan(ws, s, integ_plan, smoke_all, [])[0] == [], "a draft with a full selection still plans no heavy job")
        expect(not select(ws, ["backend/core/x.go"]).draft and not full_selection(ws, "t").draft, "nothing else is a draft")

        print("==> cache groups")
        global CACHE_GROUPS
        saved_groups = CACHE_GROUPS
        CACHE_GROUPS = [("cli", "backend/cli", ["backend/cli"]), ("services", "backend/services/svc-a", ["backend/services/*"]),
                        ("core", "backend/core", ["*", "*/*", "*/*/*"])]
        try:
            go, integ, _ = plan(ws, full_selection(ws, "t"), integ_plan)
            savers = sorted((g["cache_group"], g["module"], g["static"]) for g in go if g["cache_save"])
            expect(savers == [("cli", "backend/cli", True), ("core", "backend/core", True), ("services", "backend/services/svc-a", True)],
                   "a full plan saves each group from exactly one leg, its saver's static leg: %s" % savers)
            expect({g["module"]: g["cache_group"] for g in go}["backend/tools/tool"] == "core" and
                   {g["module"]: g["cache_group"] for g in go}["backend/services/svc-b"] == "services", "a module takes the first group that matches it")
            expect(not any(g["cache_save"] for g in go if g["module"] == "backend/core" and not g["static"]), "a split shard never saves")
            expect([l["cache_group"] for l in integ] == ["core", "services"], "an integration leg restores its first module's group")
            CACHE_GROUPS = [("cli", "backend/cli", ["backend/cli"])]
            try:
                legs_for("backend/core")
                expect(False, "a module in no cache group raises")
            except RuntimeError:
                expect(True, "a module in no cache group raises")
        finally:
            CACHE_GROUPS = saved_groups
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
    elif argv[0] == "codeql":
        cmd_codeql()
    elif argv[0] == "migration-deps":
        cmd_migration_deps()
    elif argv[0] == "replay":
        cmd_replay(argv[1:])
    else:
        print(__doc__)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
