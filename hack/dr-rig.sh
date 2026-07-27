#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The ADR-028 / ADR-020 A5 root-key RESTORE DRILL.
#
# WHAT THIS GATES
#
# Not the merge. The escrow primitive, the dcctl wiring and the DR runbook are
# all landable and CI-tested without a cluster, and they are. What needs a real
# disaster is the CLAIM they make — that an instance rebuilt from an escrow
# artifact can read the secrets in a database backup taken before it existed.
#
# Until this passes, WITH its negative control, nothing in the docs should tell
# an operator their instance is recoverable. Every restore procedure that skips
# the root key looks exactly like one that includes it, right up until the day it
# matters: the rows rehydrate, the restore reports success, and the secrets are
# gone. That is the failure this drill exists to make visible in a rehearsal
# instead of an incident.
#
#   hack/dr-rig.sh up         cluster + bootstrap WITH escrow, seed a real secret
#                             through the API, take the database backup
#   hack/dr-rig.sh backup     re-take the database backup only (resume a run whose
#                             `up` got as far as seeding)
#   hack/dr-rig.sh disaster   destroy the cluster and the local instance state,
#                             keeping only what an off-site backup would have
#   hack/dr-rig.sh restore    fresh cluster from the escrow artifact + the backup;
#                             the secret MUST decrypt
#   hack/dr-rig.sh control    THE NEGATIVE CONTROL: the identical restore with the
#                             escrow WITHHELD; the secret MUST fail to decrypt, and
#                             must fail AT THE DECRYPT
#   hack/dr-rig.sh all        up → disaster → restore → disaster → control
#   hack/dr-rig.sh down       delete the cluster and the rig's working directory
#
# `all` is the one worth running. `restore` on its own reports a pass from a check
# whose ability to FAIL has not been demonstrated in this session — and a drill
# that cannot fail is the thing this rig exists to argue against.
#
# WHY THE CONTROL IS THE HARD PART
#
# It is easy to write a negative control that fails for the wrong reason. If the
# escrow-withheld run fails because the cluster did not come up, or the dump did
# not restore, or a flag was misspelled, the control "held" while testing nothing
# — and would hold just as happily against a positive path that was also broken.
#
# So the control asserts an EXACT exit code, imported from the tool rather than
# written out here (see load_exit_codes). drdrill separates its outcomes, and its
# decrypt-failure code means "the row is here, the run was not interrupted, the
# database still answers, the envelope is well formed, and this instance's key
# still cannot open it". Every other failure — no database, no key, no row, a
# damaged envelope — gets a different code, and the rig reports those as
# INCONCLUSIVE rather than as a control that held.
#
# Both rebuild phases additionally assert their own premise with `dcctl secrets
# escrow verify`: the restored instance must be running the escrowed key, and the
# control instance must NOT be. Without that, the phases were only as good as the
# operator having remembered to run `disaster` first.
#
# Requires: kind, kubectl, docker, `tofu` OR `terraform` on PATH, and a Go
# toolchain (the script builds dcctl and drdrill itself). A full run brings up
# three clusters in sequence; budget time accordingly and run `down` afterwards.

set -euo pipefail

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '\033[0;37m    %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# NOT configurable: kind takes the cluster name from the top-level `name:` in the
# config file in preference to --name, so a variable here would name the context
# one thing and the cluster another. Change both or neither.
cluster="devicechain-dr"
kind_config="$repo_root/deploy/local/kind-cluster-dr.yaml"
instance="${DC_DR_INSTANCE:-drdrill}"
kube_context="kind-$cluster"
# The ingress host:port the drill reaches the platform's API on. The cluster maps
# the ingress onto high host ports because a developer's own local cluster is
# almost certainly already holding 80 and 443.
api_server="localhost:18080"
# Local port the Postgres forward binds for the drill's own connection.
pg_local_port="${DC_DR_PG_PORT:-15432}"

# The rig's working directory stands in for OFF-SITE storage: the escrow artifact,
# the database backup and the receipt all have to survive the destruction of both
# the cluster and ~/.devicechain/<instance>. It therefore lives outside both, and
# `disaster` deliberately does not touch it.
work="${DC_DR_WORK:-$HOME/.devicechain-dr-rig}"
escrow_file="$work/rootkey.escrow"
receipt_file="$work/receipt.json"
dump_file="$work/instance.sql"
replicas_file="$work/replicas.txt"

# A fixed throwaway passphrase. This is a rig whose instances exist to be
# destroyed; the passphrase is not protecting anything, and prompting would stop
# the run dead in CI. A real instance takes this from a secret manager — see
# docs/deployment/disaster-recovery.md.
export DCCTL_ESCROW_PASSPHRASE="${DCCTL_ESCROW_PASSPHRASE:-dr-rig-throwaway-passphrase}"

dcctl="$repo_root/backend/cli/build/dcctl"
drdrill="$work/bin/drdrill"

# load_exit_codes imports drdrill's exit-code taxonomy instead of repeating it.
#
# The codes are the interface between the tool and this script, and an interface
# written out as bare literals in shell is not one: renaming the decrypt-failure
# code in Go would leave every Go test green, every build clean, and this script's
# negative control reporting INCONCLUSIVE forever — which reads as an environment
# problem, not as a broken rig.
load_exit_codes() {
  local defs
  defs="$("$drdrill" codes)" || fail "drdrill could not report its exit codes"
  eval "$defs"
  [[ -n "${DRDRILL_EXIT_DECRYPT_FAILED:-}" ]] || fail "drdrill reported no decrypt-failure exit code"
}

# Image source: HEAD, built from source, BY DEFAULT — for the same reason the A0
# HA rig gives. The drill's positive result depends on the deployed services
# holding the ADR-059 secret store as it exists in the working tree; running
# published images would test a different platform than the one under review, and
# the confound would be invisible in the output. DC_VERSION=<tag> deliberately
# checks a published release instead, which is a different and also useful run.
if [[ -n "${DC_VERSION:-}" ]]; then
  image_args=(--version "$DC_VERSION")
else
  image_args=(--build)
fi

# ---------------------------------------------------------------------------
# building
# ---------------------------------------------------------------------------

build_tools() {
  say "building dcctl and drdrill"
  make -C "$repo_root/backend/cli" build >/dev/null
  [[ -x "$dcctl" ]] || fail "dcctl was not built at $dcctl"
  mkdir -p "$work/bin"
  (cd "$repo_root/backend/tools/drdrill" && go build -o "$drdrill" .)
  [[ -x "$drdrill" ]] || fail "drdrill was not built at $drdrill"
}

create_cluster() {
  if kind get clusters 2>/dev/null | grep -qx "$cluster"; then
    say "kind cluster $cluster already exists; reusing it"
    return
  fi
  say "creating kind cluster $cluster"
  kind create cluster --name "$cluster" --config "$kind_config" --wait 120s
}

delete_cluster() {
  if kind get clusters 2>/dev/null | grep -qx "$cluster"; then
    say "deleting kind cluster $cluster"
    kind delete cluster --name "$cluster"
  fi
}

# ---------------------------------------------------------------------------
# reading the instance's own configuration
#
# Every credential the rig needs is in the instance config Secret, which is the
# same place the services read it from. Deriving them rather than hardcoding
# means the rig cannot drift from what was actually deployed — and the database
# password is generated per bootstrap, so there is nothing to hardcode anyway.
# ---------------------------------------------------------------------------

instance_config() {
  kubectl --context "$kube_context" -n "$instance" get secret "dci-$instance-config" \
    -o jsonpath='{.data.instance}' | base64 -d
}

# cfg_get REFUSES a missing value rather than returning jq's "null".
#
# The first live run of this rig died inside pg_dump with `role "null" does not
# exist`, because the paths below were written in the Go field names and the
# rendered config uses lowerCamelCase. jq answered "null", the shell passed it on
# as a username, and the error surfaced three layers from the mistake. Anything
# read from the instance config is load-bearing here, so a miss stops the rig
# where it happened and names the path.
cfg_get() {
  local value
  value="$(instance_config | jq -r "$1")"
  if [[ -z "$value" || "$value" == "null" ]]; then
    fail "the instance config has no value at $1.
The rig reads the deployed instance's own credentials from Secret
dci-$instance-config; a missing path means that Secret's shape has changed and
every credential below it is wrong too."
  fi
  printf '%s' "$value"
}

db_user() { cfg_get '.persistence.rdb.configuration.username'; }
db_password() { cfg_get '.persistence.rdb.configuration.password'; }
root_key() { cfg_get '.infrastructure.secrets.rootKey'; }

# pg_pod finds the relational Postgres pod. The dump and the restore run INSIDE
# it, so the rig needs no Postgres client on the host — only the drill's own
# verify needs a port-forward.
#
# It resolves the pod through the `dc-postgresql` SERVICE rather than through a
# pod label, and that is deliberate (ADR-020 A2.1). The label this used to select
# on — `app.kubernetes.io/name=dc-postgresql` — is authored by the OpenTofu
# module that provisions today's single-node StatefulSet. Under the A2 topology
# the store becomes a CloudNativePG Cluster whose pods carry `cnpg.io/*` labels
# instead, and this lookup would find nothing while the service name is unchanged
# — so the rig would break on a topology change that is invisible from here.
#
# The service is the thing both topologies actually agree on. Under A2 the CNPG
# Cluster declares a `managed.services.additional` entry with `selectorType: rw`
# and this exact name, so the operator keeps it pointed at the primary. That was
# verified on a real CNPG 1.30 cluster: it is a genuine ClusterIP Service WITH
# EndpointSlices (an ExternalName alias would have none, and this lookup would
# find nothing), its selector is identical to CNPG's own `-rw` service, and it
# followed a promotion in about three seconds.
#
# 🔴 What this does NOT give us today: the primary. The current OpenTofu service
# is HEADLESS and selects every pod carrying the module's label. At `replicas =
# 1` that is the only pod, so the lookup is correct — but EndpointSlice ordering
# is not stable, so the moment that module grows a replica this would dump from
# an ARBITRARY instance. A dump taken off a lagging standby is missing the
# drill's seeded row, and the rig would report that as a missing row rather than
# as a bad backup source: a destroyed drill result blamed on the wrong thing.
# The primary-tracking property arrives with the A2 alias; until then it is the
# single-replica topology, not this function, that makes the dump correct.
#
# EndpointSlice, not the older Endpoints API, which is deprecated. Only READY
# endpoints count: a terminating or unready pod has a name but cannot serve a
# dump, and picking one would fail later and further away.
pg_pod() {
  local slices pod
  slices="$(kubectl --context "$kube_context" -n dc-system get endpointslices \
    -l "kubernetes.io/service-name=dc-postgresql" -o json)" ||
    fail "could not list endpointslices for the dc-postgresql service"

  # `|| fail` is load-bearing. pg_pod is only ever called as `pod="$(pg_pod)"`,
  # and bash suppresses errexit for the whole dynamic extent of a command
  # substitution — so a jq blow-up here does NOT abort, it yields an empty
  # string, which the refusal below then reports as "no READY pod". That is the
  # same misdiagnosis cfg_get's scar comment was written about: a parse failure
  # wearing the costume of an absent object.
  #
  # `.conditions.ready != false` rather than `== true`: the EndpointSlice API
  # says a nil `ready` must be interpreted as ready.
  pod="$(printf '%s' "$slices" |
    jq -r 'first(.items[].endpoints[]
             | select(.conditions.ready != false)
             | .targetRef.name // empty)')" ||
    fail "could not parse the endpointslices for dc-postgresql (jq failed)"

  # Same refusal as cfg_get: an empty answer here is a broken instance, not a
  # pod named "". Say which of the two it is, because they are fixed differently.
  if [[ -z "$pod" || "$pod" == "null" ]]; then
    fail "no READY pod backs the dc-postgresql service in dc-system.$(
      printf '\n  endpointslices found: %s' \
        "$(printf '%s' "$slices" | jq -r '.items | length')"
    )"
  fi
  printf '%s' "$pod"
}

# wait_for_api blocks until the instance's ingress actually routes to a live
# backend for one functional area.
#
# `dcctl bootstrap` waits for the workloads to become READY, which is not the same
# claim: the ingress controller still has to pick the now-ready pod up as a healthy
# upstream. A live run of this rig seeded immediately after a successful bootstrap,
# got a 503 from nginx, and lost the entire bring-up to a race that had nothing to
# do with the drill.
#
# Readiness is what the platform asserts; being reachable through the ingress is
# what the drill depends on, so the rig waits for the thing it depends on.
wait_for_api() {
  local area="$1" url code waited=0
  url="http://$api_server/api/$area/graphql"
  while true; do
    # A trivial introspection POST. Any HTTP answer at all means the request
    # reached the service — 400 and 401 are answers, and only a gateway error or a
    # dead connection means there is nothing behind the ingress yet.
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
      -X POST "$url" -H 'Content-Type: application/json' \
      -d '{"query":"{__typename}"}' 2>/dev/null || echo 000)"
    case "$code" in
      000 | 502 | 503 | 504) ;;
      *) return 0 ;;
    esac
    waited=$((waited + 1))
    [[ $waited -lt 60 ]] || fail "the $area API never became reachable at $url (last status $code)"
    sleep 5
  done
}

# ---------------------------------------------------------------------------
# up: a real instance, a real secret, a real backup
# ---------------------------------------------------------------------------

cmd_up() {
  need kind; need kubectl; need docker; need jq; need curl
  build_tools
  mkdir -p "$work"

  if [[ -e "$escrow_file" ]]; then
    fail "$escrow_file already exists — a previous run's artifact is still here.
Run 'hack/dr-rig.sh down' first. Continuing would either refuse mid-bootstrap or
drill against an artifact that belongs to a cluster that no longer exists."
  fi

  create_cluster

  say "bootstrapping $instance WITH a root-key escrow"
  note "artifact: $escrow_file"
  "$dcctl" bootstrap local "$instance" --yes --compact \
    --kube-context "$kube_context" --host localhost --no-tls \
    --escrow-file "$escrow_file" "${image_args[@]}"

  [[ -s "$escrow_file" ]] || fail "bootstrap reported success but wrote no escrow artifact at $escrow_file"

  say "waiting for the instance API to route"
  wait_for_api user-management
  wait_for_api notification-management

  say "seeding a secret through the platform's own API"
  # Through the ingress, as a tenant, into the deployed service — so the
  # ciphertext in the backup below is sealed by the real KEK, not manufactured by
  # the drill.
  "$drdrill" seed --server "$api_server" --scheme http \
    --instance "$instance" --receipt "$receipt_file" \
    || fail "could not seed the drill secret; there is nothing to restore"

  say "backing up the relational database"
  take_backup

  say "UP COMPLETE — instance $instance holds a secret, and $work has the two
things a real operator would keep off-site: the escrow artifact and the backup."
}

# dump_file_under_test is the in-progress dump take_backup is building; it becomes
# $dump_file only once the guard below has looked inside it.
dump_file_under_test=""

take_backup() {
  local pod user
  pod="$(pg_pod)"
  user="$(db_user)"

  # The password is never expanded on the host. POSTGRES_PASSWORD is already in the
  # pod's own environment, so `sh -c` reads it there — whereas `env PGPASSWORD=...`
  # would put the cleartext into kubectl's argv, visible in `ps` to every user on
  # the box for the duration of the dump. drdrill refuses to take the root key as a
  # flag for exactly this reason; the rig should not be looser with the database
  # password.
  #
  # --clean --if-exists so the restore drops what the rebuilt instance created on
  # first start before laying the backup down. The alternative — DROP DATABASE —
  # cannot run while anything is connected to it, and something always is.
  #
  # Written to a temp file and moved into place only once it is complete and
  # checked: `>"$dump_file"` truncates at redirection time, so re-taking a backup
  # against a cluster that has since gone unhealthy would destroy the last good one
  # before discovering it could not make a new one.
  local tmp="$dump_file.partial"
  umask 077
  # 🔴 `-U postgres`, and NO PGPASSWORD. Both halves changed when the relational
  # store moved to CloudNativePG (ADR-020 A2.3), and the old form fails on the
  # new topology for two independent reasons:
  #
  #   1. $POSTGRES_PASSWORD does not exist. It came from the old StatefulSet's
  #      `env_from` on a Secret the CNPG pod does not mount, so it expanded to
  #      the empty string.
  #   2. The CNPG pod sets PGHOST=/controller/run, which forces the LOCAL SOCKET
  #      regardless of what we pass — and pg_hba there is `peer`, so the
  #      connection is authenticated by uid, not by password.
  #
  # The pod runs as uid 26 = postgres, so `-U postgres` authenticates by peer
  # with no secret in the process table at all — which is also why this no longer
  # needs $user. Dumping as the superuser is the right call for a restore drill
  # regardless: it captures ownership faithfully instead of whatever the app role
  # happens to be able to see.
  if ! kubectl --context "$kube_context" -n dc-system exec -i "$pod" -- \
    sh -c 'pg_dump -U postgres -d "$1" --clean --if-exists' \
    _ "$instance" >"$tmp"; then
    rm -f "$tmp"
    fail "pg_dump failed; the previous backup (if any) is untouched"
  fi

  [[ -s "$tmp" ]] || { rm -f "$tmp"; fail "pg_dump produced an empty file"; }
  dump_file_under_test="$tmp"
  assert_backup_carries_the_secret "$dump_file_under_test"
  mv "$dump_file_under_test" "$dump_file"
  note "backup: $dump_file ($(wc -c <"$dump_file") bytes)"
}

# THE CHECK THAT COULD NOT FAIL OTHERWISE.
#
# A backup that carried the secrets TABLE but not the ciphertext ROW would make
# the positive restore fail and the negative control "hold" — the exact
# combination that reads as a working drill while proving the opposite. So the rig
# asserts the row is in the file before it destroys the only other copy of it.
#
# Checking for the table is not enough, and neither is a bare grep for the
# handle: the audit journal also records the handle of every secret written, so a
# dump with an empty secrets table still mentions "channel/1/secret". This reads
# the COPY block itself.
assert_backup_carries_the_secret() {
  local file="$1" schema handle tenant rows
  schema="$(jq -r '.schema' "$receipt_file")"
  handle="$(jq -r '.secretName' "$receipt_file")"
  tenant="$(jq -r '.tenant' "$receipt_file")"

  # pg_dump quotes any schema name that is not a bare identifier, and every
  # functional area has a hyphen in it — so the quotes are the normal case here,
  # not an edge one. Missing them is how the first version of this guard passed a
  # dump it had not actually looked inside.
  rows="$(awk -v tbl="COPY \"$schema\".secrets " '
    index($0, tbl) == 1 { inblock = 1; next }
    inblock && $0 == "\\." { exit }
    inblock { print }
  ' "$file")"

  if [[ -z "$rows" ]]; then
    fail "the backup has no rows in $schema.secrets.
Nothing downstream of here would be evidence about anything: the restore would
find no ciphertext, and the negative control would 'hold' against an empty table."
  fi
  # Match the tenant AND the handle on the SAME row. The handle is
  # channel/<id>/secret with <id> almost always 1, so it is maximally collidable
  # across tenants; a bare grep for it would accept another tenant's ciphertext
  # while claiming to have ruled exactly that out. Columns are
  # id, created_at, updated_at, deleted_at, tenant_id, scope, name, ...
  if ! awk -F'\t' -v t="$tenant" -v h="$handle" \
    '$5 == t && $6 == "tenant" && $7 == h { found = 1 } END { exit !found }' <<<"$rows"; then
    fail "the backup's $schema.secrets rows carry no row for tenant $tenant with handle $handle.
The drill would be testing somebody else's ciphertext."
  fi
  note "backup carries $(wc -l <<<"$rows") row(s) in $schema.secrets, including $handle"
}

# cmd_backup exists so a run that seeded successfully and then tripped over the
# backup does not have to pay for a whole bring-up again. It is also the only way
# to exercise the guard above without one.
cmd_backup() {
  need kubectl; need jq
  [[ -s "$receipt_file" ]] || fail "no receipt at $receipt_file; there is nothing seeded to back up"
  say "backing up the relational database"
  take_backup
}

# ---------------------------------------------------------------------------
# disaster: lose everything except the backup and the artifact
# ---------------------------------------------------------------------------

cmd_disaster() {
  need kind
  # Check BEFORE destroying. A disaster that discovers the backup was missing
  # afterwards is a real disaster rather than a drill.
  for f in "$escrow_file" "$receipt_file" "$dump_file"; do
    [[ -s "$f" ]] || fail "refusing to simulate a disaster: $f is missing or empty. Run 'up' first."
  done

  say "SIMULATING TOTAL LOSS of the cluster and the local instance state"
  delete_cluster
  # ~/.devicechain/<instance> holds the OpenTofu state and nothing recoverable.
  # A real disaster takes it too, and leaving it behind would let the rebuild
  # reconcile against infrastructure that no longer exists — which is not the
  # procedure an operator would be following.
  rm -rf "${HOME:?}/.devicechain/$instance"
  note "kept (as an off-site copy would be): $escrow_file, $dump_file, $receipt_file"
}

# ---------------------------------------------------------------------------
# rebuilding, restoring, verifying
# ---------------------------------------------------------------------------

# require_no_instance enforces the premise BOTH rebuild paths depend on: a cluster
# that is not already running this instance.
#
# create_cluster reuses an existing cluster, which is right after `disaster` has
# destroyed it and catastrophic when it has not. A surviving instance still holds
# its ORIGINAL root key, and `dcctl bootstrap` deliberately preserves the key of an
# instance that already exists (that is the guard in ExistingRootKey). So without
# this check:
#
#   `up` then `restore` prints DRILL PASSED having decrypted a secret that was
#       never lost, in a cluster whose etcd never went away — which is precisely
#       the restore-in-place rehearsal this whole workstream exists to discredit.
#   `up` then `control` prints the loudest banner in the script, claiming the root
#       key does not protect the data, over an operator-sequencing mistake.
#
# `all` was safe because it runs `disaster` between phases, but the standalone
# phases are documented and both were wrong. The precondition belongs here, not in
# the caller.
require_no_instance() {
  if kubectl --context "$kube_context" get ns "$instance" >/dev/null 2>&1; then
    fail "cluster $cluster is still running instance $instance.

This phase rebuilds the instance from a backup, and its whole premise is a cluster
that lost everything. A surviving instance keeps its original root key, so the
drill would decrypt a secret it never lost (a meaningless PASS) and the negative
control would blame the crypto for a sequencing mistake.

Run 'hack/dr-rig.sh disaster' first, or 'hack/dr-rig.sh all' to do it in order."
  fi
}

# rebuild brings up a fresh cluster and instance. With "$escrow_file" it seeds the
# ORIGINAL root key; with "none" it mints a fresh one — which is the whole
# difference between the drill and its control.
rebuild() {
  local restore_from="$1"
  create_cluster
  require_no_instance

  if [[ "$restore_from" == "none" ]]; then
    say "bootstrapping a fresh $instance WITHOUT the escrow (the negative control)"
    "$dcctl" bootstrap local "$instance" --yes --compact --no-escrow \
      --kube-context "$kube_context" --host localhost --no-tls "${image_args[@]}"
  else
    say "bootstrapping a fresh $instance FROM the escrow artifact"
    # No --escrow-file here, and dcctl refuses the combination outright: a
    # restored instance keeps the artifact it was restored from rather than
    # writing a second one. The first live run of this rig passed both and was
    # stopped by that guard, which is the guard working.
    "$dcctl" bootstrap local "$instance" --yes --compact \
      --kube-context "$kube_context" --host localhost --no-tls \
      --restore-root-key "$restore_from" "${image_args[@]}"
  fi
}

restore_backup() {
  local pod user ns="$instance"

  say "quiescing the instance for the restore"
  # The services are already running and have created their own empty schemas.
  # Restoring underneath a live service means dropping tables it holds open and
  # racing its migrations, so the rig scales the instance down first — which is
  # also what a real restore procedure does. The replica counts are recorded so
  # scaling back up restores the deployment as it was rather than assuming 1.
  kubectl --context "$kube_context" -n "$ns" get deploy \
    -o custom-columns=NAME:.metadata.name,R:.spec.replicas --no-headers >"$replicas_file"
  kubectl --context "$kube_context" -n "$ns" scale deploy --all --replicas=0 >/dev/null
  # `wait --for=delete` errors both when nothing matched (fine — they are already
  # gone) and when it genuinely timed out (not fine: restoring under a pod that
  # still holds those tables open is the exact thing the note above forbids). The
  # two were previously both swallowed by `|| true`, so the real one could only
  # ever be diagnosed as "the backup did not restore cleanly", layers from the
  # cause. Distinguish them by asking what is actually left.
  kubectl --context "$kube_context" -n "$ns" wait --for=delete pod --all --timeout=180s >/dev/null 2>&1 || true
  local remaining
  remaining="$(kubectl --context "$kube_context" -n "$ns" get pods --no-headers 2>/dev/null | wc -l)"
  if [[ "$remaining" -ne 0 ]]; then
    fail "$remaining pod(s) are still running in $ns after the scale-down.
Restoring underneath them would drop tables a live service holds open and race its
migrations. Find what is stuck (kubectl -n $ns get pods) and re-run."
  fi

  say "restoring the relational backup"
  pod="$(pg_pod)"
  user="$(db_user)"
  # ON_ERROR_STOP so a restore that half-worked is a failure here rather than a
  # confusing decrypt result three steps later.
  # `-U postgres` with no password, for the reasons spelled out at the pg_dump
  # call above: the CNPG pod has no $POSTGRES_PASSWORD and its PGHOST forces the
  # peer-authenticated local socket.
  if ! kubectl --context "$kube_context" -n dc-system exec -i "$pod" -- \
    sh -c 'psql -U postgres -d "$1" -v ON_ERROR_STOP=1 --quiet' \
    _ "$instance" <"$dump_file"; then
    fail "the backup did not restore cleanly; nothing after this point would be evidence"
  fi

  say "bringing the instance back up"
  while read -r name replicas; do
    [[ -n "$name" ]] || continue
    kubectl --context "$kube_context" -n "$ns" scale "deploy/$name" --replicas="$replicas" >/dev/null
  done <"$replicas_file"
  kubectl --context "$kube_context" -n "$ns" wait --for=condition=available deploy --all --timeout=300s
  # Available is the platform's claim; the drill's first check goes through the
  # ingress, so wait for that too rather than racing it.
  wait_for_api user-management
  wait_for_api notification-management
}

# run_verify runs the drill and echoes drdrill's exit code. It does NOT decide
# what the code means — the caller does, because the same code is a pass in one
# phase and a failure in the other.
pf_pid=""
stop_port_forward() {
  [[ -n "$pf_pid" ]] || return 0
  kill "$pf_pid" 2>/dev/null || true
  # Wait, so the socket is released before the next phase tries to bind it.
  # Without this a later phase can find the port still held by the forward this
  # one just asked to stop.
  wait "$pf_pid" 2>/dev/null || true
  pf_pid=""
}
# The forward is a background child of this shell, so it outlives any `fail`
# unless something reaps it. An EXIT trap covers every way out, including the
# ones that abort mid-drill.
trap stop_port_forward EXIT

run_verify() {
  local rc=0 waited=0
  # Read the credentials FIRST, as plain assignments.
  #
  # These used to be inline $( ) inside the drdrill invocation, where cfg_get's
  # `fail` exits only the subshell: a missing config path printed a red banner and
  # the rig carried on with empty values, contradicting cfg_get's own contract. As
  # separate statements, `set -e` aborts here, where the problem is.
  local key user password
  key="$(root_key)"
  user="$(db_user)"
  password="$(db_password)"

  kubectl --context "$kube_context" -n dc-system port-forward svc/dc-postgresql "$pg_local_port":5432 >/dev/null 2>&1 &
  pf_pid=$!

  # "Something is listening" is not "my forward is up". A stale forward from an
  # aborted run, or anyone else's tunnel on this port, satisfies a bare TCP probe
  # while kubectl dies on a bind conflict — and the drill then verifies against a
  # database this rig did not choose, which in the control phase means an exit 0
  # and the catastrophic banner. So the liveness of OUR process is checked too.
  until (exec 3<>/dev/tcp/127.0.0.1/"$pg_local_port") 2>/dev/null; do
    if ! kill -0 "$pf_pid" 2>/dev/null; then
      fail "the port-forward to Postgres exited immediately — port $pg_local_port is most
likely already held by another process (a leftover forward, or another rig). Free it,
or set DC_DR_PG_PORT to a port that is free, and re-run."
    fi
    waited=$((waited + 1))
    [[ $waited -lt 60 ]] || fail "the port-forward to Postgres never came up"
    sleep 1
  done

  DRDRILL_ROOT_KEY="$key" PGPASSWORD="$password" \
    "$drdrill" verify --receipt "$receipt_file" \
    --db-host 127.0.0.1 --db-port "$pg_local_port" \
    --db-user "$user" \
    --server "$api_server" --scheme http || rc=$?

  stop_port_forward
  return "$rc"
}

cmd_restore() {
  need kind; need kubectl; need jq; need curl
  build_tools
  load_exit_codes
  for f in "$escrow_file" "$receipt_file" "$dump_file"; do
    [[ -s "$f" ]] || fail "$f is missing or empty; run 'up' then 'disaster' first"
  done

  rebuild "$escrow_file"

  # The premise, asserted with the shipped tool rather than assumed: this instance
  # really is running the key the artifact holds. `escrow verify` compares the
  # artifact's fingerprint against the key the instance is live on, so a restore
  # that silently seeded something else is caught here rather than being blamed on
  # the decrypt two steps later.
  say "confirming the rebuilt instance runs the ESCROWED key"
  "$dcctl" secrets escrow verify "$escrow_file" --instance "$instance" \
    --kube-context "$kube_context" \
    || fail "the rebuilt instance is NOT running the key in $escrow_file, so nothing
after this point would be a test of the escrow"

  restore_backup

  say "THE DRILL — can this cluster read the old cluster's secret?"
  local rc=0
  run_verify || rc=$?
  if [[ $rc -ne 0 ]]; then
    fail "the restored instance could NOT read the secret (drdrill exit $rc).
This is the finding the whole A5 workstream exists to prevent, reproduced in a
rehearsal. Read drdrill's output above: exit 3 means the escrowed key did not fit,
exit 4 means the row never came back, exit 1 means the drill could not run."
  fi
  say "DRILL PASSED"
}

# cmd_control is the check on the check.
#
# It rebuilds the SAME instance from the SAME backup with the escrow withheld, so
# the instance mints a root key of its own. Everything else is identical. If the
# secret still decrypts, then the drill's pass says nothing — the verifier is not
# actually testing the key — and the rig says so in those words.
cmd_control() {
  need kind; need kubectl; need jq; need curl
  build_tools
  load_exit_codes
  for f in "$receipt_file" "$dump_file"; do
    [[ -s "$f" ]] || fail "$f is missing or empty; run 'up' then 'disaster' first"
  done

  rebuild none

  # The control's premise, asserted directly: this instance is running a key that
  # is NOT the escrowed one. Here the shipped verifier is required to FAIL, and a
  # PASS means the control never became a control — the instance somehow has the
  # original key, so the decrypt below would succeed for a reason that has nothing
  # to say about the escrow.
  say "confirming the control instance runs a DIFFERENT key from the escrowed one"
  if "$dcctl" secrets escrow verify "$escrow_file" --instance "$instance" \
    --kube-context "$kube_context" >/dev/null 2>&1; then
    fail "the control instance is running the ESCROWED key, so it is not a control.
It was bootstrapped with --no-escrow and should have minted a key of its own; that
it did not means the instance survived the disaster, or the key came from somewhere
this rig does not know about. Nothing below would be evidence."
  fi

  restore_backup

  say "NEGATIVE CONTROL — the same restore, with the escrow withheld"
  local rc=0
  run_verify || rc=$?

  case "$rc" in
    "$DRDRILL_EXIT_OK")
      fail "THE NEGATIVE CONTROL DID NOT HOLD.

An instance that never saw the escrow artifact decrypted the old cluster's secret.
That is catastrophic in one of two ways, and both invalidate every drill result:

  - the root key is not actually what protects the data, or
  - the drill is not reading what it claims to read.

No restore result may be recorded from this run."
      ;;
    "$DRDRILL_EXIT_DECRYPT_FAILED")
      say "NEGATIVE CONTROL HELD — the secret was present and did NOT decrypt.
The row restored, the instance served it, and the key it minted for itself could
not open it. That is the failure mode the escrow artifact exists to prevent, and
it has now been observed rather than assumed."
      ;;
    *)
      fail "THE NEGATIVE CONTROL DID NOT RUN (drdrill exit $rc).

That is INCONCLUSIVE — neither a pass nor a failure. The control is only evidence
when it fails at the DECRYPT (exit 3); exit 4 means the row never restored and
exit 1 means the drill could not get far enough to have an opinion. Fix what the
output above reports and re-run rather than reading this either way."
      ;;
  esac
}

cmd_down() {
  need kind
  delete_cluster
  if [[ -d "$work" ]]; then
    say "removing the rig's working directory $work"
    rm -rf "${work:?}"
  fi
  rm -rf "${HOME:?}/.devicechain/$instance"
}

case "${1:-all}" in
  up) cmd_up ;;
  backup) cmd_backup ;;
  disaster) cmd_disaster ;;
  restore) cmd_restore ;;
  control) cmd_control ;;
  down) cmd_down ;;
  all)
    cmd_up
    cmd_disaster
    cmd_restore
    # A second disaster, so the control starts from exactly the state the drill
    # started from. Rebuilding the control on top of the restored cluster would
    # leave it holding a root key that had already been seeded from the escrow.
    cmd_disaster
    cmd_control
    say "A5 RESTORE DRILL COMPLETE: an instance rebuilt from the escrow artifact read
the old cluster's secret, and an instance rebuilt WITHOUT it demonstrably could not.
This is the evidence the ADR-028 recovery procedure rests on."
    ;;
  *) fail "unknown command ${1}; try up | backup | disaster | restore | control | all | down" ;;
esac
