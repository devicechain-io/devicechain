#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Validate the rendered CloudNativePG chart against the REAL CustomResourceDefinition
# schemas (ADR-020 A2, ADR-028).
#
# WHY THIS EXISTS — the one failure the chart cannot protect itself from
#
# deploy/opentofu/modules/cnpg-cluster/chart/templates/cluster.yaml carries a long
# header about a hole it cannot close from the inside, measured both ways:
#
#   kubectl apply   REJECTS an unknown field — `strict decoding error: unknown
#                   field "spec.postgresql.synchronus"`. Loud and immediate.
#   helm install    ACCEPTS it. Exit 0, release deployed, and the created object
#                   simply does not have the field.
#
# The module applies through Helm. So a one-character slip in a template yields a
# green `tofu apply` and an object missing whatever that field controlled. The
# existing consequence was three pods replicating ASYNCHRONOUSLY while every
# pod-counting check agreed they were fine. A2.5 adds a second one of the same
# shape and arguably worse: misspell `isWALArchiver` and the Cluster still carries
# the plugin, still names the ObjectStore, still reads as configured for backup —
# and Postgres has no archive_command, so nothing is ever shipped anywhere. The
# instance is not being backed up and nothing says so.
#
# Neither failure is detectable from the values, so no amount of `required` or
# `fail` in the templates can catch them. They are detectable from the RENDERED
# OBJECT against the schema the API server would have used, which is what this does.
#
# 🔑 The schemas come from the CRDs of the PINNED chart versions — read out of the
# OpenTofu variables, not restated here — so this validates against the versions
# the platform actually installs. Pinning them a second time in this file would
# make the check pass against a schema nobody deploys.
#
# WHAT IT DOES NOT DO. It does not know whether a field is CORRECT, only whether
# it EXISTS. `isWALArchiver: false` passes here and archives nothing; that is the
# live rig's job, not a schema's.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart="$repo_root/deploy/opentofu/modules/cnpg-cluster/chart"
variables="$repo_root/deploy/opentofu/variables.tf"

say() { printf '\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '\033[0;37m    %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

for tool in helm python3; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is required but not on PATH"
done
python3 -c 'import yaml' 2>/dev/null || fail "python3 needs PyYAML (pip install pyyaml)"

# The pinned chart versions, read from the file that decides them. A regex rather
# than an HCL parser, bounded to the variable's own block for the same reason
# backend/cli/bootstrap/cnpg_test.go bounds its reader: an unbounded scan happily
# reports the NEXT variable's default and the check then validates against a
# schema nobody installs.
tofu_default() {
  python3 - "$variables" "$1" <<'PY'
import re, sys
source, name = open(sys.argv[1]).read(), sys.argv[2]
start = source.find('\nvariable "%s"' % name)
if start < 0:
    sys.exit('no variable %r in variables.tf' % name)
block = source[start + 1:]
nxt = block[1:].find('\nvariable "')
if nxt >= 0:
    block = block[:nxt + 1]
m = re.search(r'(?m)^\s*default\s*=\s*"([^"]*)"', block)
if not m:
    sys.exit('variable %r declares no string default' % name)
print(m.group(1))
PY
}

operator_version="$(tofu_default cnpg_chart_version)"
plugin_version="$(tofu_default cnpg_plugin_chart_version)"
[[ -n "$operator_version" && -n "$plugin_version" ]] ||
  fail "could not read the pinned chart versions from variables.tf"

say "validating the CNPG chart against cloudnative-pg $operator_version / plugin-barman-cloud $plugin_version"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# 🔴 A HARD FAILURE, never a skip. A check that quietly passes when it could not
# fetch its schemas is worse than no check: it reports success for every run on a
# machine with no network, including CI the day a repository URL changes.
helm repo add cnpg https://cloudnative-pg.github.io/charts >/dev/null 2>&1 || true
helm repo update cnpg >/dev/null 2>&1 ||
  fail "could not refresh the cloudnative-pg Helm repository; this check cannot run without the CRD schemas and will not pretend otherwise"

for spec in "cloudnative-pg:$operator_version" "plugin-barman-cloud:$plugin_version"; do
  name="${spec%%:*}"
  version="${spec##*:}"
  helm pull "cnpg/$name" --version "$version" --untar --untardir "$work" >/dev/null 2>&1 ||
    fail "could not pull cnpg/$name $version"
done

# Both charts render their CRDs as templates gated on `crds.create`, so the raw
# files carry Helm actions. Stripping the two guard lines is enough to make them
# parseable YAML; nothing else in them is templated.
crds="$work/crds.yaml"
: >"$crds"
for f in "$work"/*/templates/crds/crds.yaml; do
  [[ -f "$f" ]] || fail "no CRD template found in $f"
  sed -e 's/{{- if .Values.crds.create }}//' -e 's/{{- end }}//' "$f" >>"$crds"
  printf '\n---\n' >>"$crds"
done

# The configurations to render. Each is a values overlay; the point is to cover
# the branches whose rendered SHAPE differs, since an unknown field inside an
# `{{- if }}` that never fires is not validated by a single render.
render_case() {
  local name="$1"
  shift
  helm template dc-store "$chart" "$@" >"$work/$name.yaml" 2>"$work/$name.err" ||
    fail "rendering the $name case failed:$(printf '\n%s' "$(cat "$work/$name.err")")"
  printf '%s' "$work/$name.yaml"
}

# Which `fail` guards this run actually tripped, for the coverage control below.
tripped=()

# refuses <case> <expected message fragment> <helm args...> — the render must FAIL,
# and it must fail with the named guard's own message.
#
# 🔴 EVERY CASE ABOVE IS A POSITIVE ONE, and render_case aborts the script if a
# render fails — so until this existed, not one of the chart's `fail` guards had
# ever been executed. They are the chart's whole defence against configurations
# that apply cleanly and are wrong at runtime (a restore archiving over its own
# source, a five-field schedule that computes no next run), and an inverted
# condition or an `if` that no longer fires would leave every gate green.
#
# The message fragment is not decoration. A refusal only says something about the
# case if it came from the guard under test: several of these configurations are
# invalid in more than one way, and a render refused by a NEIGHBOURING guard would
# otherwise read as proof of a guard that has stopped firing entirely.
refuses() {
  local name="$1" want="$2"
  shift 2
  if helm template dc-store "$chart" "$@" >"$work/$name.yaml" 2>"$work/$name.err"; then
    fail "the $name case RENDERED. This configuration is supposed to be refused at render time; it now reaches a cluster."
  fi
  if ! grep -qF -- "$want" "$work/$name.err"; then
    fail "the $name case was refused, but not by the guard under test (wanted a message containing: $want):$(printf '\n%s' "$(cat "$work/$name.err")")"
  fi
  tripped+=("$want")
  say "  refused: $name"
}

base=(
  --set name=dc-store
  --set imageName=ghcr.io/example/postgres:17
  --set aliasServiceName=dc-postgresql
  --set storage.size=8Gi
  --set bootstrap.database=dc
  --set bootstrap.owner=devicechain
  --set bootstrap.secretName=dc-store-app-credentials
)

backup=(
  --set backup.enabled=true
  --set backup.bucket=devicechain-rdb
  --set backup.endpointURL=http://dc-object-store.dc-system:9000
  --set backup.credentialsSecret=dc-object-store-credentials
  --set backup.accessKeyIdKey=MINIO_ROOT_USER
  --set backup.secretAccessKeyKey=MINIO_ROOT_PASSWORD
)

rendered=()
# The `single` case also carries the four OPTIONAL blocks, because each of them
# lives behind a `{{- with }}` that no other case fires, and an unknown field
# inside a template action that never runs is validated against nothing at all.
# postInitTemplateSQL is the one that matters most: it is the hook that installs
# the TimescaleDB extension into template1, so every database the platform
# subsequently creates inherits it. It renders through `toYaml` as a LIST, and a
# shape the CRD does not accept there would have shipped unseen.
# The `branch_coverage` control at the bottom of the python block is what keeps
# these from quietly dropping out of the case again.
rendered+=("$(render_case single "${base[@]}" --set instances=1 \
  --set storage.storageClass=fast-ssd \
  --set resources.requests.cpu=500m --set resources.requests.memory=1Gi \
  --set resources.limits.memory=2Gi \
  --set 'bootstrap.postInitTemplateSQL[0]=CREATE EXTENSION IF NOT EXISTS timescaledb')")
rendered+=("$(render_case ha "${base[@]}" --set instances=3 --set synchronous.enabled=true)")
rendered+=("$(render_case backup-single "${base[@]}" --set instances=1 "${backup[@]}")")
rendered+=("$(render_case backup-ha "${base[@]}" --set instances=3 --set synchronous.enabled=true "${backup[@]}" \
  --set backup.serverName=dc-store-restored --set walStorage.enabled=true --set walStorage.size=2Gi \
  --set walStorage.storageClass=fast-ssd \
  --set sharedPreloadLibraries[0]=timescaledb --set parameters.timescaledb\\.telemetry_level=off \
  --set backup.endpointCASecret.name=dc-ca --set backup.endpointCASecret.key=ca.crt)")

# The RESTORE shape (A2.5c). A separate case rather than a variation, because
# recovery replaces the whole bootstrap block: every field under
# `bootstrap.initdb` disappears and a differently-shaped `bootstrap.recovery`
# plus a top-level `externalClusters` takes its place. None of that is exercised
# by any render above, so without these two the restore path would ship having
# been validated against nothing.
rendered+=("$(render_case restore "${base[@]}" --set instances=1 "${backup[@]}" \
  --set backup.serverName=dc-store-restored \
  --set restore.enabled=true --set restore.sourceServerName=dc-store)")
rendered+=("$(render_case restore-pitr "${base[@]}" --set instances=3 --set synchronous.enabled=true "${backup[@]}" \
  --set backup.serverName=dc-store-restored \
  --set restore.enabled=true --set restore.sourceServerName=dc-store \
  --set restore.recoveryTarget.targetTime='2026-07-28 03:00:00+00' \
  --set restore.recoveryTarget.targetTLI=latest \
  --set restore.recoveryTargetImmediate=true)")

# --- the configurations the chart must REFUSE ---------------------------------
say "checking the render-time guards"

# CloudNativePG owns these role names. Connecting as one of them means the
# platform is running as the superuser rather than as an application.
refuses reserved-owner "which CloudNativePG reserves for its own use" \
  "${base[@]}" --set instances=1 --set bootstrap.owner=postgres

# Two instances under synchronous replication: one standby, so losing it stalls
# every write. Worse availability than a single node, bought with a second node.
refuses sync-too-few-instances "requires at least" \
  "${base[@]}" --set instances=2 --set synchronous.enabled=true

# A restore with no source. There is no safe inference — the cluster's own name
# points at an empty path on a fresh install, and at the WRONG database if it
# happens to collide.
refuses restore-without-source "restore.sourceServerName is unset" \
  "${base[@]}" --set instances=1 "${backup[@]}" --set restore.enabled=true

# A restore with nowhere to read from: no ObjectStore named, and none rendered
# because this release archives nowhere.
refuses restore-without-store "no restore.objectStoreName" \
  "${base[@]}" --set instances=1 \
  --set restore.enabled=true --set restore.sourceServerName=dc-store

# 🔴 THE WEDGE, both halves. A restored cluster that archives must own a path
# that is not the one it recovered from — unset means "my own name", which is the
# same collision written differently. CloudNativePG does not fail this cleanly:
# it sits in `Setting up primary` with its WAL going nowhere, during a recovery.
refuses restore-archiving-under-its-own-name "but backup.serverName is unset" \
  "${base[@]}" --set instances=1 "${backup[@]}" \
  --set restore.enabled=true --set restore.sourceServerName=dc-store
refuses restore-archiving-over-its-source "and restore.sourceServerName are both" \
  "${base[@]}" --set instances=1 "${backup[@]}" --set backup.serverName=dc-store \
  --set restore.enabled=true --set restore.sourceServerName=dc-store

# An ObjectStore missing its destination renders successfully and then fails every
# archive attempt — which stops nothing and crashes nothing. It just means this
# store is not being backed up.
refuses backup-without-destination "fails every archive attempt" \
  "${base[@]}" --set instances=1 --set backup.enabled=true

# A five-field schedule is accepted by the API, creates the object, computes no
# next run, and takes no backup — on a cluster that reads healthy.
refuses five-field-schedule "CloudNativePG schedules take SIX" \
  "${base[@]}" --set instances=1 "${backup[@]}" --set backup.schedule='0 3 * * *'

# COVERAGE: every `fail` in the templates must have been tripped by a case above.
# A guard nothing exercises is indistinguishable from one that has stopped firing,
# and this is the only thing in the repo that renders these templates at all.
python3 - "$chart" "${tripped[@]}" <<'PY'
import glob, os, re, sys

chart, tripped = sys.argv[1], sys.argv[2:]

guards, seen = [], 0
# Every template, at any depth and any extension -- a guard in a .tpl or a
# subdirectory is still a guard, and an extractor that cannot see it EXEMPTS it.
for path in sorted(glob.glob(os.path.join(chart, "templates", "**", "*"), recursive=True)):
    if not os.path.isfile(path):
        continue
    text = open(path).read()
    # Count `fail` as a template ACTION only. These templates carry long prose
    # explaining why each guard is a `fail` and not a warning, and counting that
    # prose would make the control fire on documentation -- which trains people to
    # "fix" it by loosening the count, i.e. by removing the control.
    code = re.sub(r"\{\{-?\s*/\*.*?\*/\s*-?\}\}", "", text, flags=re.S)  # Helm comments
    code = "\n".join(ln for ln in code.split("\n") if not ln.lstrip().startswith("#"))
    seen += len(re.findall(r"\bfail\b", code))
    # The message literal, with \" handled: several guards quote a cron example.
    for m in re.finditer(r'fail \(?printf?\s*"((?:[^"\\]|\\.)*)"', text):
        guards.append((os.path.relpath(path, chart), m.group(1)))

# The control's own control, in both directions. An extractor that matches
# nothing reports full coverage over an empty set -- and one that matches only
# SOME guard forms silently exempts the rest, which is the same failure wearing a
# smaller number. So the messages it extracted are counted against every `fail`
# token in the same files.
if not guards:
    sys.exit("found no `fail` guards in the chart templates -- the coverage check "
             "below is reading nothing, so it cannot fail. Fix the extractor.")
if seen != len(guards):
    sys.exit("the templates contain %d `fail` token(s) but only %d could be read as a "
             "guard message. The rest are exempt from the coverage requirement below "
             "with nothing saying so -- a guard written `fail \"literal\"`, built from a "
             "variable, or reached through an include is invisible here. Fix the "
             "extractor rather than the count." % (seen, len(guards)))

missing = [(f, g) for f, g in guards if not any(w in g for w in tripped)]
for f, g in missing:
    sys.stderr.write("UNTRIPPED GUARD in %s: %s...\n" % (f, g[:140]))
if missing:
    sys.exit("%d of %d render-time guard(s) are never exercised. Nothing else in this "
             "repo renders these templates, so an inverted condition or an `if` that no "
             "longer fires would ship green." % (len(missing), len(guards)))
print("  all %d render-time guard(s) exercised" % len(guards))
PY

say "checking $(( ${#rendered[@]} )) rendered configurations"

python3 - "$crds" "${rendered[@]}" <<'PY'
import sys, yaml

crd_path, rendered_paths = sys.argv[1], sys.argv[2:]

schemas = {}
for doc in yaml.safe_load_all(open(crd_path)):
    if not doc or doc.get("kind") != "CustomResourceDefinition":
        continue
    group = doc["spec"]["group"]
    kind = doc["spec"]["names"]["kind"]
    for version in doc["spec"]["versions"]:
        api = "%s/%s" % (group, version["name"])
        schemas[(api, kind)] = version["schema"]["openAPIV3Schema"]

if not schemas:
    sys.exit("no CRD schemas were parsed -- the check would pass vacuously")


def walk(obj, schema, path, problems):
    """Report fields the schema does not define. Structural pruning only: this is
    what the API server drops silently and Helm never mentions."""
    if schema is None:
        return
    # A subtree the CRD explicitly refuses to constrain. Anything goes; recursing
    # would invent violations.
    if schema.get("x-kubernetes-preserve-unknown-fields"):
        return

    if isinstance(obj, dict):
        props = schema.get("properties")
        if props is None:
            # additionalProperties: a map whose VALUES are typed but whose keys
            # are free (spec.postgresql.parameters, labels, ...).
            extra = schema.get("additionalProperties")
            if isinstance(extra, dict):
                for k, v in obj.items():
                    walk(v, extra, "%s.%s" % (path, k), problems)
            return
        for k, v in obj.items():
            if k not in props:
                problems.append("%s.%s" % (path, k))
                continue
            walk(v, props[k], "%s.%s" % (path, k), problems)
    elif isinstance(obj, list):
        items = schema.get("items")
        for i, v in enumerate(obj):
            walk(v, items, "%s[%d]" % (path, i), problems)


BARMAN = "barman-cloud.cloudnative-pg.io"


def check_plugin_wiring(docs, case, failures):
    """The one thing the schema CANNOT check, asserted separately.

    `spec.plugins[].parameters` is `additionalProperties: {type: string}` -- a
    free-form map. So `barmanObjectName` misspelled as `barmanObjectname` is a
    perfectly legal key: the walk above does not flag it, and neither does the API
    server, because there is no schema to prune it against. Measured, by mutation:
    every other field in the plugin entry is caught and this one is not.

    It is also the field that names the DESTINATION. Get it wrong and the plugin
    finds no object store, so the Cluster carries a WAL archiver pointed at
    nothing -- green apply, no archiving, no complaint.

    So the parameter is checked by NAME here, and its value is required to match an
    ObjectStore actually rendered alongside it. That second half is what makes this
    more than a spell-check: it is the only place the two objects are compared.

    🔴 AND THE ARCHIVER MUST BE THERE AT ALL. Everything below is a loop over
    whatever plugins happened to render, so for a long time it could only catch a
    plugin entry that was WRONG -- never one that was MISSING. Both misses were
    measured:

      - deleting `isWALArchiver: true` was UNDETECTED. The entry still matched
        BARMAN, still named its ObjectStore, and still passed every assertion
        here, while Postgres had no `archive_command` and shipped nothing
        anywhere. This is the exact failure this file's header describes, and it
        went straight through. (A MISSPELLING was caught, by the schema walk --
        which is what made the gap so easy to believe closed.)
      - deleting the whole `{{- if .Values.backup.enabled }}` plugin block was
        UNDETECTED. The Cluster rendered with no `spec.plugins` at all, so the
        loop ran zero times and contributed zero failures, while ObjectStore and
        ScheduledBackup still rendered and satisfied the `required` kinds check.
        The result reads as a fully configured backup: a destination, a retention
        policy, a schedule -- and a database wired to none of it.

    Both are closed the way check_restore_wiring already closes its half: count
    what was actually asserted and require the count to be non-zero, plus a
    per-case invariant that an archiver exists wherever a destination does.
    """
    stores = {d["metadata"]["name"] for d in docs if d.get("kind") == "ObjectStore"}
    archivers_checked = 0

    for d in docs:
        if d.get("kind") != "Cluster":
            continue
        plugins = d.get("spec", {}).get("plugins", []) or []
        # EXACTLY ONE WAL ARCHIVER, wherever this chart rendered a destination.
        # Keyed on an ObjectStore having rendered rather than on a values flag,
        # because the rendered objects are what would actually be applied -- and
        # because "there is somewhere to archive to" is precisely the premise that
        # makes a missing archiver a silent data-loss bug rather than a
        # deliberately backup-less install.
        #
        # Two, not one, is also a failure: CloudNativePG takes a single archiver,
        # and a second entry marked isWALArchiver is the shape an edit produces
        # when the recovery source is pasted into spec.plugins instead of
        # externalClusters[].plugin.
        archivers = [
            p for p in plugins
            if p.get("name") == BARMAN and p.get("isWALArchiver")
        ]
        archivers_checked += len(archivers)
        if stores and len(archivers) != 1:
            # Two different bugs, two different consequences -- say which one.
            if not archivers:
                why = (
                    "so there IS a destination and this database is not archiving "
                    "to it. Postgres gets no `archive_command`: the apply is "
                    "green, the ObjectStore and ScheduledBackup exist, and no WAL "
                    "ever leaves the cluster"
                )
            else:
                why = (
                    "and CloudNativePG takes a SINGLE archiver. This is the shape "
                    "an edit produces when a recovery source is pasted into "
                    "spec.plugins instead of externalClusters[].plugin -- the "
                    "restored cluster then archives over the very path it "
                    "recovered from"
                )
            failures.append(
                "%s: Cluster/%s carries %d barman plugin entries marked "
                "`isWALArchiver`, want exactly 1 -- this chart rendered "
                "ObjectStore(s) %s, %s"
                % (case, d["metadata"]["name"], len(archivers),
                   ", ".join(sorted(stores)), why)
            )
        for i, plugin in enumerate(plugins):
            if plugin.get("name") != BARMAN:
                continue
            where = "%s: Cluster/%s .spec.plugins[%d]" % (case, d["metadata"]["name"], i)
            params = plugin.get("parameters") or {}

            if "barmanObjectName" not in params:
                near = [k for k in params if k.lower() == "barmanobjectname"]
                failures.append(
                    "%s has no `barmanObjectName` parameter%s -- the WAL archiver "
                    "would be attached to no object store, and `parameters` is a "
                    "free-form map so nothing else rejects this"
                    % (where, (" (found %r -- wrong case)" % near[0]) if near else "")
                )
                continue

            target = params["barmanObjectName"]
            if target not in stores:
                failures.append(
                    "%s names ObjectStore %r, which this chart does not render "
                    "(rendered: %s) -- the archiver would point at an object that "
                    "does not exist"
                    % (where, target, ", ".join(sorted(stores)) or "none")
                )

    return archivers_checked


def check_restore_wiring(docs, case, failures):
    """The restore path's own free-form map, and the wedge the schema cannot see.

    `externalClusters[].plugin.parameters` is the same `additionalProperties`
    map as the archiver's, so `serverName` misspelled there is a legal key that
    nothing rejects -- and its absence is not even an error: CloudNativePG falls
    back to the externalCluster's own NAME as the folder in the bucket. So a
    typo does not fail, it recovers from a different path.

    Three things are asserted, none of which any schema can express:

      1. recovery and externalClusters exist TOGETHER. `bootstrap.recovery.source`
         naming an entry that is not there is a cluster that never bootstraps.
      2. the entry is wired to barman with barmanObjectName + serverName.
      3. 🔴 the restored cluster does not archive back over what it recovered
         from. The chart guards this from its VALUES; this reads it off the
         rendered objects, which is the thing that would actually be applied.
    """
    stores = {d["metadata"]["name"] for d in docs if d.get("kind") == "ObjectStore"}
    seen = 0

    for d in docs:
        if d.get("kind") != "Cluster":
            continue
        spec = d.get("spec", {})
        recovery = (spec.get("bootstrap") or {}).get("recovery")
        externals = spec.get("externalClusters") or []
        if not recovery and not externals:
            continue

        seen += 1
        name = d["metadata"]["name"]
        where = "%s: Cluster/%s" % (case, name)

        if not recovery:
            failures.append("%s has externalClusters but no bootstrap.recovery -- nothing reads them" % where)
            continue

        source = recovery.get("source")
        if not source:
            failures.append("%s sets bootstrap.recovery with no `source`" % where)
            continue

        entry = next((e for e in externals if e.get("name") == source), None)
        if entry is None:
            failures.append(
                "%s recovers from source %r, which names no externalClusters entry "
                "(present: %s) -- the cluster would never bootstrap"
                % (where, source, ", ".join(sorted(e.get("name", "?") for e in externals)) or "none")
            )
            continue

        plugin = entry.get("plugin") or {}
        if plugin.get("name") != BARMAN:
            failures.append("%s externalCluster %r is not wired to %s" % (where, source, BARMAN))
            continue

        params = plugin.get("parameters") or {}
        for key in ("barmanObjectName", "serverName"):
            if key not in params:
                near = [k for k in params if k.lower() == key.lower()]
                failures.append(
                    "%s externalCluster %r has no `%s` parameter%s. `parameters` is a "
                    "free-form map, so nothing else rejects this -- and a missing "
                    "serverName does not even fail, it silently recovers from the "
                    "entry's own name instead"
                    % (where, source, key, (" (found %r -- wrong case)" % near[0]) if near else "")
                )

        target = params.get("barmanObjectName")
        if target and target not in stores:
            failures.append(
                "%s recovers from ObjectStore %r, which this chart does not render "
                "(rendered: %s)" % (where, target, ", ".join(sorted(stores)) or "none")
            )

        # 🔴 The wedge. Whatever this cluster archives UNDER must differ from what
        # it recovered FROM, or CloudNativePG refuses to archive into a non-empty
        # archive and the restore hangs in `Setting up primary`. An archiver with
        # no serverName parameter falls back to the cluster's own name, so that is
        # what gets compared -- not "unset", which would let the collision through.
        from_path = params.get("serverName") or source
        for plug in spec.get("plugins") or []:
            if plug.get("name") != BARMAN or not plug.get("isWALArchiver"):
                continue
            to_path = (plug.get("parameters") or {}).get("serverName") or name
            if to_path == from_path:
                failures.append(
                    "%s recovers from serverName %r and archives back to the SAME "
                    "path. CloudNativePG refuses to archive into a non-empty archive, "
                    "so this cluster would recover and then hang in `Setting up "
                    "primary` -- during a restore" % (where, from_path)
                )

    return seen


# Optional blocks that render only when their value is set, each with what it
# costs to ship one unvalidated. Every one of these sits behind a `{{- with }}`,
# and a template action that never fires validates nothing -- so the check would
# be green on a chart whose storageClass key was misspelled, whose resources
# block was the wrong shape, or whose TimescaleDB hook rendered a string where
# CloudNativePG wants a list. `branch_coverage` below asserts each was actually
# exercised by SOME case, which is what stops a `--set` quietly disappearing
# from the render list and taking its coverage with it.
OPTIONAL_BRANCHES = [
    (("spec", "storage", "storageClass"),
     "the data volume's StorageClass -- unset, every install lands on the "
     "cluster default, which on a cloud provider is rarely the one a database wants"),
    (("spec", "walStorage", "storageClass"),
     "the WAL volume's StorageClass; the walStorage block renders only when "
     "walStorage.enabled, so nothing else reaches this branch"),
    (("spec", "resources"),
     "the requests/limits passthrough -- a toYaml of whatever the caller "
     "supplied, so its SHAPE is only ever checked against the CRD here"),
    (("spec", "bootstrap", "initdb", "postInitTemplateSQL"),
     "the hook that installs the TimescaleDB extension into template1, so "
     "every database the platform creates inherits it. It renders as a LIST "
     "through toYaml, and it runs exactly once per cluster -- at initdb, on a "
     "path a restore never takes"),
]


def dig(doc, path):
    """The value at a dotted path, or None if any segment is absent."""
    cur = doc
    for seg in path:
        if not isinstance(cur, dict) or seg not in cur:
            return None
        cur = cur[seg]
    return cur


failures = []
checked = 0
kinds_seen = set()
restores_checked = 0
archivers_checked = 0
branch_coverage = {p: 0 for p, _ in OPTIONAL_BRANCHES}

for path in rendered_paths:
    case = path.rsplit("/", 1)[-1]
    docs = [d for d in yaml.safe_load_all(open(path)) if d]

    for doc in docs:
        key = (doc.get("apiVersion"), doc.get("kind"))
        if key not in schemas:
            # Only custom resources are validated here; a built-in type would be
            # caught by kubectl's own strict decoding and has no CRD to check.
            continue
        checked += 1
        kinds_seen.add(key[1])
        problems = []
        walk(doc, schemas[key], doc["kind"], problems)
        for p in problems:
            failures.append("%s: %s (%s)" % (case, p, doc["metadata"]["name"]))

        if doc["kind"] == "Cluster":
            for path, _ in OPTIONAL_BRANCHES:
                if dig(doc, path) is not None:
                    branch_coverage[path] += 1

    archivers_checked += check_plugin_wiring(docs, case, failures)
    restores_checked += check_restore_wiring(docs, case, failures)

# 🔴 THE POSITIVE CONTROL. Everything above is a loop over whatever happened to
# render, so a chart that emitted nothing -- or a schema map that failed to match
# any apiVersion -- produces zero failures and reads as a pass. Naming the kinds
# that MUST have been validated is what makes a green run mean something.
#
# The restore half needs its own counter rather than a kind: recovery is a SHAPE
# of Cluster, not a kind, so `Cluster` being present says nothing about whether
# any render exercised it. A chart that stopped emitting externalClusters
# entirely would satisfy the kind check and skip every assertion above.
if archivers_checked == 0:
    sys.exit(
        "no rendered Cluster carried a barman WAL archiver, so every backup-wiring\n"
        "  assertion above was skipped. A Cluster with no archiver is a database\n"
        "  shipping no WAL anywhere -- and it renders a green apply, alongside an\n"
        "  ObjectStore and a ScheduledBackup that satisfy the kind check below.\n"
        "  Either the chart stopped emitting the plugin or no case enables backups."
    )

if restores_checked == 0:
    sys.exit(
        "no rendered Cluster carried a recovery bootstrap, so every restore\n"
        "  assertion was skipped. Either the restore cases stopped rendering it or\n"
        "  the chart no longer emits it -- both make this check vacuous."
    )

uncovered = [(p, why) for p, why in OPTIONAL_BRANCHES if branch_coverage[p] == 0]
if uncovered:
    sys.exit(
        "these optional blocks were never rendered by any case, so the CRD walk\n"
        "  above never saw them and this run says nothing about whether they are\n"
        "  valid:\n\n%s\n\n"
        "  Either a `--set` was dropped from the render list or the chart stopped\n"
        "  emitting the block. Both leave a branch shipping unvalidated."
        % "\n".join("    spec.%s -- %s" % (".".join(p[1:]), why) for p, why in uncovered)
    )

required = {"Cluster", "ObjectStore", "ScheduledBackup"}
missing = required - kinds_seen
if missing:
    sys.exit(
        "these kinds were never validated: %s.\n"
        "  Either the chart stopped rendering them or their apiVersion no longer\n"
        "  matches a CRD in the pinned charts. Both make this check vacuous, which\n"
        "  is why it is a failure rather than a quiet pass."
        % ", ".join(sorted(missing))
    )

if failures:
    print("Fields the CustomResourceDefinition does not define:\n")
    for f in failures:
        print("  " + f)
    sys.exit(
        "\nHelm PRUNES these silently -- the release deploys, exits 0, and the object\n"
        "simply lacks the field. Whatever it controlled is off. Check the spelling\n"
        "against the CRD for the pinned chart version."
    )

print("    %d custom resources validated across %d configurations" % (checked, len(rendered_paths)))
print("    kinds covered: %s" % ", ".join(sorted(kinds_seen)))
print("    %d WAL archiver(s) checked for backup wiring" % archivers_checked)
print("    %d recovery bootstrap(s) checked for restore wiring" % restores_checked)
print("    %d optional block(s) exercised: %s" % (
    len(OPTIONAL_BRANCHES),
    ", ".join("spec.%s x%d" % (".".join(p[1:]), branch_coverage[p]) for p, _ in OPTIONAL_BRANCHES)))
PY

note "every rendered field exists in the pinned CRD schemas"
