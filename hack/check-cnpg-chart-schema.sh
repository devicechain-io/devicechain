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
rendered+=("$(render_case single "${base[@]}" --set instances=1)")
rendered+=("$(render_case ha "${base[@]}" --set instances=3 --set synchronous.enabled=true)")
rendered+=("$(render_case backup-single "${base[@]}" --set instances=1 "${backup[@]}")")
rendered+=("$(render_case backup-ha "${base[@]}" --set instances=3 --set synchronous.enabled=true "${backup[@]}" \
  --set backup.serverName=dc-store-restored --set walStorage.enabled=true --set walStorage.size=2Gi \
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
    """
    stores = {d["metadata"]["name"] for d in docs if d.get("kind") == "ObjectStore"}

    for d in docs:
        if d.get("kind") != "Cluster":
            continue
        for i, plugin in enumerate(d.get("spec", {}).get("plugins", []) or []):
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


failures = []
checked = 0
kinds_seen = set()
restores_checked = 0

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

    check_plugin_wiring(docs, case, failures)
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
if restores_checked == 0:
    sys.exit(
        "no rendered Cluster carried a recovery bootstrap, so every restore\n"
        "  assertion was skipped. Either the restore cases stopped rendering it or\n"
        "  the chart no longer emits it -- both make this check vacuous."
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
print("    %d recovery bootstrap(s) checked for restore wiring" % restores_checked)
PY

note "every rendered field exists in the pinned CRD schemas"
