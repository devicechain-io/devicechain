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
#   hack/dr-rig.sh up         off-cluster object store + cluster + bootstrap WITH
#                             escrow and WITH backups; take a base backup, THEN
#                             seed a real secret, then force it into the WAL
#   hack/dr-rig.sh archive    force a WAL switch and re-run the completeness gate
#                             (resume a run whose `up` got as far as seeding)
#   hack/dr-rig.sh disaster   destroy the cluster and the local instance state,
#                             keeping only what an off-site backup would have
#   hack/dr-rig.sh restore    fresh cluster recovered from the escrow artifact +
#                             the off-cluster archive; the secret MUST decrypt
#   hack/dr-rig.sh control    THE NEGATIVE CONTROL: the identical restore under a
#                             DIFFERENT root key; the secret MUST fail to decrypt,
#                             and must fail AT THE DECRYPT
#   hack/dr-rig.sh all        up → disaster → restore → disaster → control
#   hack/dr-rig.sh down       delete the cluster, the object store and the rig's
#                             working directory
#
# `all` is the one worth running. `restore` on its own reports a pass from a check
# whose ability to FAIL has not been demonstrated in this session — and a drill
# that cannot fail is the thing this rig exists to argue against.
#
# THIS IS A PHYSICAL RESTORE, AND THE ORDERING IS THE POINT
#
# The drill used to take a pg_dump and replay it with psql. That tests a logical
# export/import and says nothing about what the platform actually ships, which is
# CloudNativePG's Barman Cloud archive: a base backup plus a continuous stream of
# WAL, recovered by standing a new cluster up FROM the archive.
#
# So the base backup is taken BEFORE the secret is seeded, deliberately. If the
# order were reversed, the restore would be satisfied by the base backup alone and
# would never replay a single WAL segment — the drill would pass without ever
# exercising the mechanism it exists to test. `up` therefore refuses to continue
# until a WAL segment that did NOT exist at base-backup time has appeared in the
# bucket. That segment is the one carrying the seeded row, and recovering it is
# the thing being proved.
#
# The object store is a MinIO container on the `kind` docker network — OUTSIDE
# the cluster, which is what makes it a backup at all. `disaster` deletes the
# cluster and leaves the container standing, exactly as an off-site bucket would
# outlive a datacentre.
#
# WHY THE CONTROL IS THE HARD PART
#
# It is easy to write a negative control that fails for the wrong reason. If the
# wrong-key run fails because the cluster did not come up, or the archive did not
# restore, or a flag was misspelled, the control "held" while testing nothing —
# and would hold just as happily against a positive path that was also broken.
#
# So the control asserts an EXACT exit code, imported from the tool rather than
# written out here (see load_exit_codes). drdrill separates its outcomes, and its
# decrypt-failure code means "the row is here, the run was not interrupted, the
# database still answers, the envelope is well formed, and this instance's key
# still cannot open it". Every other failure — no database, no key, no row, a
# damaged envelope — gets a different code, and the rig reports those as
# INCONCLUSIVE rather than as a control that held.
#
# The control's own premise is asserted BOTH ways with `dcctl secrets escrow
# verify`: the control instance must verify against the decoy artifact and must
# FAIL to verify against the real one. Without both, a decoy that was silently
# ignored would look exactly like a control that held.
#
# Requires: kind, kubectl, docker, jq, curl, `tofu` OR `terraform` on PATH (dcctl
# shells out to it), and a Go toolchain (the script builds dcctl and drdrill
# itself). A full run brings up three clusters in sequence and builds images for
# each — budget an hour, and run `down` afterwards.

set -euo pipefail

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '\033[0;37m    %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }
need_all() {
  local t
  for t in kind kubectl docker jq curl go make; do need "$t"; done
  # Either name will do — dcctl looks for both. Checked here rather than left to
  # surface from inside a bootstrap, where it reads as an infrastructure failure.
  command -v tofu >/dev/null 2>&1 || command -v terraform >/dev/null 2>&1 ||
    fail "neither tofu nor terraform is on PATH; dcctl bootstrap shells out to one of them"
}

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# NOT configurable: kind takes the cluster name from the top-level `name:` in the
# config file in preference to --name, so a variable here would name the context
# one thing and the cluster another. Change both or neither.
cluster="devicechain-dr"
kind_config="$repo_root/deploy/local/kind-cluster-dr.yaml"
instance="${DC_DR_INSTANCE:-drdrill}"
kube_context="kind-$cluster"

# The ingress host:port the drill reaches the platform's API on, and the scheme.
#
# 🔴 HTTPS, and that is load-bearing rather than cosmetic. `--compact` sets
# NoTLS by itself (resolveCompactMode), and NoTLS means no cert-manager, and the
# database backup plugin needs cert-manager — so a compact instance deployed
# without an explicit `--no-tls=false` reports `Backups: NONE` and archives
# nothing. The old rig passed `--compact --no-tls` and every drill it ever ran had
# backups switched off. Keeping TLS on is what makes this drill possible at all,
# and the port and scheme here have to follow it.
api_server="localhost:18443"
api_scheme="https"
# Local port the Postgres forward binds for the drill's own connection.
pg_local_port="${DC_DR_PG_PORT:-15432}"

# The CNPG Cluster whose archive the drill restores from, and the archive's
# serverName within the bucket. They are the same string on a first bootstrap: the
# barman plugin defaults serverName to the cluster name, so the objects land under
# <bucket>/dc-rdb/. A RESTORED cluster deliberately archives to a different
# serverName, which is why `--restore-rdb-from` names the SOURCE explicitly rather
# than being inferred.
rdb_cluster="dc-rdb"
rdb_source="$rdb_cluster"

# The rig's working directory stands in for OFF-SITE storage: the escrow artifact,
# the receipt and the object store's credentials all have to survive the
# destruction of both the cluster and ~/.devicechain/<instance>. It therefore lives
# outside both, and `disaster` deliberately does not touch it.
work="${DC_DR_WORK:-$HOME/.devicechain-dr-rig}"
escrow_file="$work/rootkey.escrow"
decoy_file="$work/decoy.escrow"
receipt_file="$work/receipt.json"
wal_baseline_file="$work/wal-at-base-backup.txt"
minio_env_file="$work/minio.env"
backup_env_file="$work/backup.env"

# The off-cluster object store. A container on the `kind` docker network, so the
# cluster can reach it by name and it survives `kind delete cluster`.
minio_container="devicechain-dr-minio"
minio_image="quay.io/minio/minio:RELEASE.2025-04-22T22-12-26Z"
minio_network="kind"
bucket_rdb="$instance-rdb"
bucket_tsdb="$instance-tsdb"

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
#
# 🔑 There is no third option. `--version dev` is REFUSED — "this dcctl build has
# no pinned image version" — so the rig cannot skip the build by reusing images it
# already pushed to the local registry under a tag. Every bootstrap in a full run
# pays for a ko build.
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
# the off-cluster object store
#
# This is the half of the drill that makes it a drill. A backup written inside the
# cluster it is backing up is not a backup, and every earlier version of this rig
# effectively had one: it dumped to a file on the developer's laptop and called
# that off-site.
# ---------------------------------------------------------------------------

# rand_token prints a URL-safe random string. Used for the object store's
# credentials, which are generated per working directory rather than hardcoded —
# a hardcoded pair would end up copied into a real deployment eventually.
rand_token() {
  # tr strips the characters S3 signing and shell quoting are fussiest about.
  # base64 of 24 bytes is 32 characters on one line, and the command
  # substitution eats the trailing newline, so there is none to remove — writing
  # \n into a single-quoted tr set would delete the letter n instead.
  dd if=/dev/urandom bs=1 count=24 2>/dev/null | base64 | tr -d '=+/'
}

# write_credentials mints the object store's credentials ONCE per working
# directory and records them in two files, both 0600.
#
# 🔴 Credentials never appear in a command line. `docker run -e MINIO_ROOT_USER=…`
# puts them in the process table, where every user on the box can read them for
# the container's whole lifetime; --env-file does not. drdrill refuses to take the
# root key as a flag for the same reason, and the rig should not be looser with
# the store holding every backup.
#
# The TF_VAR_ file is how the backup destination reaches the bootstrap. That seam
# is the rig's, not a shipped interface — dcctl has no --backup-* flags yet — so
# it is deliberately confined to this function.
write_credentials() {
  [[ -s "$minio_env_file" && -s "$backup_env_file" ]] && return 0

  local user pass
  user="dr$(rand_token)"
  pass="$(rand_token)$(rand_token)"

  umask 077
  cat >"$minio_env_file" <<EOF
MINIO_ROOT_USER=$user
MINIO_ROOT_PASSWORD=$pass
EOF
  cat >"$backup_env_file" <<EOF
export TF_VAR_backup_destination=external
export TF_VAR_backup_endpoint_url=http://$minio_container:9000
export TF_VAR_backup_bucket_rdb=$bucket_rdb
export TF_VAR_backup_bucket_tsdb=$bucket_tsdb
export TF_VAR_backup_access_key=$user
export TF_VAR_backup_secret_key=$pass
EOF
}

# backup_env exports the destination into the caller's environment. Every
# bootstrap in the rig — the original, the restore and the control — has to run
# under it, or the instance comes up with no archive and the phase after it tests
# something else entirely.
backup_env() {
  [[ -s "$backup_env_file" ]] || fail "no object-store credentials at $backup_env_file.
The archive destination is generated by 'up'; a later phase cannot invent it,
because the bucket it has to read was written under those exact credentials."
  # shellcheck disable=SC1090
  . "$backup_env_file"
}

minio_up() {
  write_credentials

  # `docker ps -a`, not `docker ps`. A container STOPPED by a host reboot still
  # holds the archive, and the old form did not see it — it went straight to
  # `rm -f` and created an empty one, destroying the only off-site copy without
  # the "run down first" refusal the rig gives everywhere else.
  if docker ps -a --format '{{.Names}}' | grep -qx "$minio_container"; then
    say "object store $minio_container already exists; reusing it"
    docker start "$minio_container" >/dev/null 2>&1 || true
  else
    say "starting the off-cluster object store"
    docker run -d --name "$minio_container" --network "$minio_network" \
      --env-file "$minio_env_file" \
      "$minio_image" server /data --console-address :9001 >/dev/null
  fi

  # A bucket is a directory under /data. There is no `mc` in this image and no
  # reason to pull a second one for mkdir.
  docker exec "$minio_container" mkdir -p "/data/$bucket_rdb" "/data/$bucket_tsdb"

  wait_for_minio
  assert_bucket_empty
}

# wait_for_minio probes with an UNAUTHENTICATED S3 ListBuckets and expects 403.
#
# 403 is a real answer from a working backend. The obvious probe —
# /minio/health/live — is worse than useless here: it returns 200 on a backend
# that cannot serve objects, and in a live run of this rig it printed nothing at
# all while the 403 came back immediately.
wait_for_minio() {
  local code waited=0
  while true; do
    # `|| true`, NOT `|| echo 000`. See the note on wait_for_api: curl already
    # prints 000 when the connection fails, so appending another one produces the
    # two-line string "000\n000", which matches no case and is read as an answer.
    code="$(docker exec "$minio_container" \
      curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:9000/ 2>/dev/null || true)"
    [[ "${code:-000}" == "403" ]] && return 0
    waited=$((waited + 1))
    [[ $waited -lt 30 ]] || fail "the object store never answered an S3 request (last status $code)"
    sleep 2
  done
}

# assert_bucket_empty refuses to start a drill on top of a previous run's archive.
#
# A leftover base backup and WAL would satisfy the completeness gate below without
# this run having archived anything — and the restore would then recover somebody
# else's data and report a pass. `down` clears this; so does removing the
# container.
assert_bucket_empty() {
  local existing
  existing="$(docker exec "$minio_container" ls -1 "/data/$bucket_rdb" 2>/dev/null || true)"
  [[ -z "$existing" ]] && return 0
  fail "bucket $bucket_rdb already holds objects:
$existing

That is a previous run's archive. The completeness gate would pass on it without
this run having backed anything up, and the restore would recover data this drill
never seeded. Run 'hack/dr-rig.sh down' first."
}

minio_down() {
  if docker ps -a --format '{{.Names}}' | grep -qx "$minio_container"; then
    say "removing the object store $minio_container"
    docker rm -f "$minio_container" >/dev/null
  fi
}

# archive_base_backups / archive_wal_segments list what is actually in the bucket
# under the SOURCE archive path. MinIO stores each object as a directory, so
# `ls -1d <parent>/*` names the objects themselves.
archive_base_backups() {
  docker exec "$minio_container" \
    sh -c 'ls -1 "$1"/base 2>/dev/null' _ "/data/$bucket_rdb/$rdb_source" || true
}

# 🔴 The grep is not tidying. MinIO represents an object as a DIRECTORY holding an
# `xl.meta`, so `wals/*/*` matches two different kinds of thing: the segments
# inside a timeline prefix (`wals/0000000100000000/…0007.gz`) and the metadata
# inside any object stored directly under `wals/` — which is what a timeline
# history file is. A recovered cluster promotes to timeline 2 and uploads
# `wals/00000002.history`, so the unfiltered listing on that path returns a bare
# `xl.meta` alongside the real segments.
#
# That is a FALSE PASS waiting to happen, not a cosmetic wart: wait_for_new_wal
# declares success when a path appears that was not in the baseline, and an
# `xl.meta` arriving from a history file would satisfy it without a single WAL
# segment having been archived. Requiring the leaf to begin with a 24-hex segment
# name keeps the two apart. It was found by running this against a restored
# cluster, where the pollution is visible; on a first-bootstrap archive there is
# no history file and the bug is invisible.
archive_wal_segments() {
  docker exec "$minio_container" \
    sh -c 'ls -1d "$1"/wals/*/* 2>/dev/null' _ "/data/$bucket_rdb/$rdb_source" 2>/dev/null |
    grep -E '/[0-9A-F]{24}[^/]*$' || true
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

# pg_pod finds the relational Postgres pod. The rig execs psql inside it to force
# a WAL switch, so it needs no Postgres client on the host — only the drill's own
# verify needs a port-forward.
#
# It resolves the pod through the `dc-postgresql` SERVICE rather than through a
# pod label, and that is deliberate (ADR-020 A2.1). The label this used to select
# on — `app.kubernetes.io/name=dc-postgresql` — was authored by the OpenTofu
# module that provisioned the old single-node StatefulSet. Under the A2 topology
# the store is a CloudNativePG Cluster whose pods carry `cnpg.io/*` labels
# instead, and this lookup would find nothing while the service name is unchanged
# — so the rig would break on a topology change that is invisible from here.
#
# The service is the thing both topologies actually agree on. Under A2 the CNPG
# Cluster declares a `managed.services.additional` entry with `selectorType: rw`
# and this exact name, so the operator keeps it pointed at the primary. Confirmed
# on a live CNPG cluster during the A2.5 restore drill: `dc-postgresql` is a real
# ClusterIP Service with EndpointSlices (an ExternalName alias would have none,
# and this lookup would find nothing), and it resolved to the primary `dc-rdb-1`.
#
# EndpointSlice, not the older Endpoints API, which is deprecated. Only READY
# endpoints count: a terminating or unready pod has a name but cannot serve, and
# picking one would fail later and further away.
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

# wait_for_cluster_healthy asserts on the DATABASE, not on dcctl's exit code.
#
# 🔴 This exists because of a live finding: `dcctl bootstrap` step 3/8 does not
# wait for the database. In the drill's first successful restore the infra apply
# returned and the bootstrap moved on to steps 4 and 5 while BOTH CNPG Clusters
# were still 14 seconds into `Setting up primary`. It converged that time — but it
# means a WEDGED restore would not fail the bootstrap. It would surface later, or
# not at all, as a running instance with an empty database. A recovering cluster
# that cannot reach its archive sits in `Setting up primary` indefinitely, and
# that is precisely the failure this drill is supposed to catch.
wait_for_cluster_healthy() {
  local name="$1" phase="" waited=0
  say "waiting for CNPG Cluster $name to reach a healthy phase"
  while true; do
    phase="$(kubectl --context "$kube_context" -n dc-system \
      get clusters.postgresql.cnpg.io "$name" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    [[ "$phase" == "Cluster in healthy state" ]] && {
      note "$name: $phase"
      return 0
    }
    waited=$((waited + 1))
    if [[ $waited -ge 120 ]]; then
      fail "CNPG Cluster $name never became healthy; its phase is stuck at ${phase:-<none>}.

'Setting up primary' here means the recovery could not complete — most often the
archive is unreachable, or --restore-rdb-from names a serverName that does not
exist in the bucket. Note that the bootstrap ITSELF may have reported success:
it does not wait for the database, which is why this check is not optional."
    fi
    sleep 5
  done
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
  url="$api_scheme://$api_server/api/$area/graphql"
  while true; do
    # A trivial introspection POST. Any HTTP answer at all means the request
    # reached the service — 400 and 401 are answers, and only a gateway error or a
    # dead connection means there is nothing behind the ingress yet.
    #
    # -k: the instance's certificate is issued by cert-manager's self-signed
    # issuer. Verifying it would need the CA out of the cluster, and this probe is
    # asking "is anything listening", not "is this the right server".
    #
    # 🔴 `|| true`, NOT `|| echo 000`. -w '%{http_code}' prints 000 by itself when
    # the connection never lands, so the old `|| echo 000` APPENDED a second one
    # and produced the two-line string "000\n000" — which matches neither the
    # retry list nor 502/503/504, falls through to `*`, and returns success. This
    # loop's entire job is to wait for an ingress that is not there yet, and it
    # was returning immediately for exactly that case: measured against a closed
    # port, it reported the API reachable. Every wait in the rig sat on top of it.
    code="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 \
      -X POST "$url" -H 'Content-Type: application/json' \
      -d '{"query":"{__typename}"}' 2>/dev/null || true)"
    # 404 is a RETRY, not an answer. ingress-nginx serves its default backend —
    # a 404 — until the Ingress route is admitted, so accepting it would return
    # success before anything routes to this area at all, which is precisely the
    # race this loop exists to absorb. The cost of being wrong is a wait that
    # times out with a diagnosis instead of a bring-up lost three steps later.
    case "${code:-000}" in
      000 | 404 | 502 | 503 | 504) ;;
      *) return 0 ;;
    esac
    waited=$((waited + 1))
    [[ $waited -lt 60 ]] || fail "the $area API never became reachable at $url (last status $code)"
    sleep 5
  done
}

# ---------------------------------------------------------------------------
# the archive
# ---------------------------------------------------------------------------

# take_base_backup asks CNPG for a base backup on demand and waits for it.
#
# WAL alone restores nothing — recovery starts from a base backup and replays
# forward — so the drill needs both, and the scheduled backup the chart installs
# is on a timer nobody wants to wait for.
take_base_backup() {
  local name stamp phase waited=0
  stamp="$(date -u +%Y%m%dt%H%M%S)"
  name="dr-rig-base-$stamp"

  say "taking a base backup ($name) — BEFORE the secret is seeded"
  kubectl --context "$kube_context" apply -f - >/dev/null <<YAML
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata:
  name: $name
  namespace: dc-system
spec:
  cluster:
    name: $rdb_cluster
  method: plugin
  pluginConfiguration:
    name: barman-cloud.cloudnative-pg.io
YAML

  while true; do
    phase="$(kubectl --context "$kube_context" -n dc-system get backup "$name" \
      -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    case "$phase" in
      completed) break ;;
      failed)
        fail "the base backup $name FAILED. Without it there is nothing to recover
from, and every phase after this one would be testing an empty archive.
  $(kubectl --context "$kube_context" -n dc-system get backup "$name" -o jsonpath='{.status.error}' 2>/dev/null)"
        ;;
    esac
    waited=$((waited + 1))
    [[ $waited -lt 60 ]] || fail "the base backup $name never completed (phase ${phase:-<none>})"
    sleep 5
  done

  note "base backup complete; $(archive_wal_segments | wc -l | tr -d ' ') WAL segment(s) archived at that point"
}

# force_wal_archive makes the seeded row reach the archive.
#
# An idle Postgres archives nothing: the row is committed and sits in the current
# WAL segment, which is not shipped until it fills or is switched. A drill that
# skipped this would destroy the cluster with the only copy of the seed still
# inside it, and then correctly report that the restore could not find the row —
# a real failure, for a reason that has nothing to do with the escrow.
#
# `-U postgres` with no password: the CNPG pod sets PGHOST=/controller/run, which
# forces the local socket, and pg_hba there is `peer` — so the connection is
# authenticated by uid (26 = postgres) and no secret enters the process table.
# Twice, because the switch archives the segment that was current when it ran.
force_wal_archive() {
  local pod i
  pod="$(pg_pod)"

  # 🔴 THE BASELINE IS TAKEN HERE, immediately before the switch — NOT when the
  # base backup was taken.
  #
  # It used to be recorded at base-backup time, which is minutes before the seed:
  # wait_for_api runs twice in between, and an instance with services running is
  # writing leases and heartbeats the whole while. Any segment that filled on its
  # own during that window would satisfy "a path appeared that was not in the
  # baseline" WITHOUT the seeded row having been archived — and `all` proceeds to
  # destroy the cluster seconds later, taking the row with it. That fails in the
  # safe direction (the restore reports exit 4) but it blames the restore for a
  # backup that was never taken, which is the misdiagnosis this gate exists to
  # prevent.
  #
  # Snapshotting here makes the claim exact rather than probable: pg_switch_wal
  # archives the segment that is CURRENT when it runs, the seed committed before
  # that, so any segment appearing after this line necessarily contains the
  # seed's commit. The residual window is between this snapshot and the switch on
  # the next line, against an archive_timeout of five minutes.
  archive_wal_segments >"$wal_baseline_file"

  say "forcing the seeded row into the WAL archive"
  for i in 1 2; do
    kubectl --context "$kube_context" -n dc-system exec -i "$pod" -- \
      psql -U postgres -d postgres -q -c 'checkpoint' -c 'select pg_switch_wal()' >/dev/null ||
      fail "could not force a WAL switch on $pod"
  done
}

# wait_for_new_wal is THE COMPLETENESS GATE, and it is the check the old rig did
# not have an equivalent of.
#
# It does not ask "is there WAL in the bucket" — there always is, from the base
# backup itself. It asks whether a segment that did NOT exist at the moment of the
# forced switch has arrived since. Because the seed committed before that switch,
# such a segment necessarily carries the seeded row, and only replaying it can
# produce a pass. Without this the drill would happily destroy the cluster,
# recover the base backup, find no row, and report exit 4.
wait_for_new_wal() {
  local waited=0 now fresh
  # -f, not -s. An EMPTY baseline is legitimate — nothing had been archived yet
  # when the switch was forced — and treating that as "missing" would refuse the
  # one case where every segment counts as new.
  [[ -f "$wal_baseline_file" ]] ||
    fail "no WAL baseline at $wal_baseline_file; force_wal_archive did not run, so
there is nothing to compare against and 'newer than the seed' has no meaning"

  say "waiting for a WAL segment archived after the seed"
  while true; do
    now="$(archive_wal_segments)"
    fresh="$(comm -13 <(sort "$wal_baseline_file") <(printf '%s\n' "$now" | sort) || true)"
    if [[ -n "$fresh" ]]; then
      note "archived since the seed:"
      printf '%s\n' "$fresh" | sed 's|.*/|      |'
      return 0
    fi
    waited=$((waited + 1))
    [[ $waited -lt 60 ]] || fail "no new WAL segment reached $bucket_rdb after the seed.

The row is committed inside a cluster that is about to be destroyed, and the
archive does not have it. Restoring would find no row and report that as a failed
restore rather than as a backup that was never taken."
    sleep 5
  done
}

# assert_archive_complete is the last look before the disaster. Both halves have
# to be there: a base backup to start from and WAL to replay onto it.
assert_archive_complete() {
  local bases wals
  bases="$(archive_base_backups)"
  wals="$(archive_wal_segments)"

  [[ -n "$bases" ]] || fail "no base backup under $bucket_rdb/$rdb_source/base.
Recovery starts from a base backup; WAL alone restores nothing."
  [[ -n "$wals" ]] || fail "no WAL under $bucket_rdb/$rdb_source/wals.
The base backup predates the seeded secret by design, so without WAL to replay
the restore cannot possibly find it."

  note "archive $bucket_rdb/$rdb_source: $(printf '%s\n' "$bases" | wc -l | tr -d ' ') base backup(s), $(printf '%s\n' "$wals" | wc -l | tr -d ' ') WAL segment(s)"
}

# ---------------------------------------------------------------------------
# up: a real instance, a real secret, a real off-cluster archive
# ---------------------------------------------------------------------------

cmd_up() {
  need_all
  build_tools
  mkdir -p "$work"

  if [[ -e "$escrow_file" ]]; then
    fail "$escrow_file already exists — a previous run's artifact is still here.
Run 'hack/dr-rig.sh down' first. Continuing would either refuse mid-bootstrap or
drill against an artifact that belongs to a cluster that no longer exists."
  fi

  minio_up
  create_cluster
  backup_env

  say "bootstrapping $instance WITH a root-key escrow and WITH backups"
  note "artifact: $escrow_file"
  note "archive:  $bucket_rdb (on $minio_container, outside the cluster)"
  # --no-tls=false is NOT redundant. See the api_server comment: --compact turns
  # TLS off by itself, and no TLS means no cert-manager, which means the backup
  # plugin is not installed and this whole drill silently becomes a test of an
  # instance with no archive at all.
  "$dcctl" bootstrap local "$instance" --yes --compact --no-tls=false \
    --kube-context "$kube_context" --host localhost \
    --escrow-file "$escrow_file" "${image_args[@]}"

  [[ -s "$escrow_file" ]] || fail "bootstrap reported success but wrote no escrow artifact at $escrow_file"

  wait_for_cluster_healthy "$rdb_cluster"
  take_base_backup

  say "waiting for the instance API to route"
  wait_for_api user-management
  wait_for_api notification-management

  say "seeding a secret through the platform's own API"
  # Through the ingress, as a tenant, into the deployed service — so the
  # ciphertext in the archive below is sealed by the real KEK, not manufactured by
  # the drill.
  "$drdrill" seed --server "$api_server" --scheme "$api_scheme" \
    --instance "$instance" --receipt "$receipt_file" ||
    fail "could not seed the drill secret; there is nothing to restore"

  force_wal_archive
  wait_for_new_wal
  assert_archive_complete

  say "UP COMPLETE — instance $instance holds a secret whose row exists ONLY in WAL
archived after the base backup, and $work holds the two things a real operator
would keep off-site: the escrow artifact and the archive's credentials."
}

# cmd_archive exists so a run that seeded successfully and then tripped over the
# archive does not have to pay for a whole bring-up again. It is also the only way
# to exercise the completeness gate without one.
cmd_archive() {
  need_all
  [[ -s "$receipt_file" ]] || fail "no receipt at $receipt_file; there is nothing seeded to archive"
  force_wal_archive
  wait_for_new_wal
  assert_archive_complete
}

# ---------------------------------------------------------------------------
# disaster: lose everything except the off-cluster archive and the artifact
# ---------------------------------------------------------------------------

cmd_disaster() {
  need kind; need docker
  # Check BEFORE destroying. A disaster that discovers the archive was missing
  # afterwards is a real disaster rather than a drill.
  for f in "$escrow_file" "$receipt_file" "$backup_env_file"; do
    [[ -s "$f" ]] || fail "refusing to simulate a disaster: $f is missing or empty. Run 'up' first."
  done
  docker ps --format '{{.Names}}' | grep -qx "$minio_container" ||
    fail "refusing to simulate a disaster: the object store $minio_container is not running,
so there is nothing holding the archive the restore would read."
  assert_archive_complete

  say "SIMULATING TOTAL LOSS of the cluster and the local instance state"
  delete_cluster
  # ~/.devicechain/<instance> holds the OpenTofu state and nothing recoverable.
  # A real disaster takes it too, and keeping it would make the rebuild a test of
  # `tofu apply` convergence rather than of a runbook an operator can follow on a
  # new laptop.
  rm -rf "${HOME:?}/.devicechain/$instance"
  note "kept (as an off-site copy would be): the $bucket_rdb archive, $escrow_file, $receipt_file"
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

This phase rebuilds the instance from the archive, and its whole premise is a
cluster that lost everything. A surviving instance keeps its original root key, so
the drill would decrypt a secret it never lost (a meaningless PASS) and the
negative control would blame the crypto for a sequencing mistake.

Run 'hack/dr-rig.sh disaster' first, or 'hack/dr-rig.sh all' to do it in order."
  fi
}

# rebuild stands a fresh cluster up and RECOVERS the instance into it from the
# off-cluster archive, seeded with whichever root key it is given.
#
# There is no separate restore step, and that is the shape of the real procedure:
# the database is recovered by CNPG as the cluster is created, from the archive,
# before any service connects to it. `--restore-root-key` is what makes that
# recovered ciphertext readable, and dcctl REFUSES `--restore-rdb-from` without it
# — because a recovery that mints a fresh key reports success and leaves every
# secret permanently unreadable.
rebuild() {
  local artifact="$1" what="$2"
  create_cluster
  require_no_instance
  backup_env

  say "recovering $instance from the archive, under $what"
  # No --escrow-file here, and dcctl refuses the combination outright: a restored
  # instance keeps the artifact it was restored from rather than writing a second
  # one. The first live run of this rig passed both and was stopped by that guard,
  # which is the guard working.
  "$dcctl" bootstrap local "$instance" --yes --compact --no-tls=false \
    --kube-context "$kube_context" --host localhost \
    --restore-root-key "$artifact" --restore-rdb-from "$rdb_source" \
    "${image_args[@]}"

  # The bootstrap's exit code is not evidence that the database recovered. See
  # wait_for_cluster_healthy.
  wait_for_cluster_healthy "$rdb_cluster"
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

# port_forward_alive refuses if OUR kubectl is not the thing holding the port.
port_forward_alive() {
  kill -0 "$pf_pid" 2>/dev/null && return 0
  fail "the port-forward to Postgres is not running, but port $pg_local_port answered anyway.

Something else is listening there — a forward leaked by a run that was SIGKILLed
(the EXIT trap does not fire on SIGKILL), a parallel rig, or an unrelated local
database. The drill would read its verdict out of a database this rig did not
choose. Free the port, or set DC_DR_PG_PORT to one that is free, and re-run."
}

run_verify() {
  local rc=0 waited=0
  # Read the credentials FIRST, and CHECK THEM. Neither half is optional.
  #
  # 🔴 `set -e` does not protect this, and a comment here used to claim it did.
  # cfg_get's `fail` runs inside a $( ), so its `exit 1` kills the subshell and
  # leaves the variable empty; and because both callers invoke this function as
  # `run_verify || rc=$?`, errexit is suppressed for run_verify's ENTIRE dynamic
  # extent — so the failing assignment does not abort either. Measured: the red
  # FAIL banner prints, the function continues with key='', and returns 0.
  #
  # Today drdrill refuses an empty root key and an empty user cannot authenticate,
  # so the outcome degrades to an INCONCLUSIVE exit 1 rather than a false verdict.
  # That protection lives in the TOOL, not here, and it is not something this
  # function should be relying on. So the values are checked where they are read.
  local key user password
  key="$(root_key)" || true
  user="$(db_user)" || true
  password="$(db_password)" || true
  local name
  for name in key user password; do
    [[ -n "${!name}" ]] || fail "run_verify could not read the instance's $name from its config Secret.
The banner above says which path was missing. Nothing below this point would be
evidence: an empty credential fails the decrypt for a reason that has nothing to
do with the escrow, which in the control phase is a control that 'held' while
testing nothing."
  done

  # 🔴 A FRESH FORWARD PER INVOCATION, every time.
  #
  # A `kubectl port-forward` to this database survives exactly ONE drdrill run.
  # The second connect through the same forward hangs with no output, and the
  # forward's own log shows `lost connection to pod`. That cost two debugging
  # sessions during the A2.5 drill and briefly looked like a defect in the
  # wrong-key path — it is not, and with a fresh forward that case returns
  # instantly. Reusing a forward makes a dead tunnel and a refused decrypt
  # indistinguishable, which is the exact false control this rig exists to avoid.
  stop_port_forward
  kubectl --context "$kube_context" -n dc-system port-forward \
    --address 127.0.0.1 "svc/dc-postgresql" "$pg_local_port":5432 >/dev/null 2>&1 &
  pf_pid=$!

  # "Something is listening" is not "my forward is up". A stale forward from an
  # aborted run, or anyone else's tunnel on this port, satisfies a bare TCP probe
  # while kubectl dies on a bind conflict — and the drill then verifies against a
  # database this rig did not choose, which in the control phase means an exit 0
  # and the catastrophic banner. So the liveness of OUR process is checked too.
  #
  # 🔴 AND THE CHECK IS AFTER THE LOOP, not only inside it. Inside the body it
  # runs only while the connect is FAILING — which is exactly the case where the
  # port is free and there is nothing to be confused by. The scenario the guard
  # was written for is the opposite one: a foreign listener answers on the first
  # attempt, the loop body never executes once, and the rig drives the whole
  # verdict through a stranger's tunnel. An EXIT trap does not fire on SIGKILL, so
  # a leaked forward from a previous run is a realistic way to arrive here.
  until (exec 3<>/dev/tcp/127.0.0.1/"$pg_local_port") 2>/dev/null; do
    port_forward_alive
    waited=$((waited + 1))
    [[ $waited -lt 60 ]] || fail "the port-forward to Postgres never came up"
    sleep 1
  done
  port_forward_alive

  # A hard bound, with --kill-after so a child that ignores the TERM is still
  # reaped. Plain `timeout` waits forever in that case, which is how an earlier
  # attempt at this check hung instead of reporting anything.
  DRDRILL_ROOT_KEY="$key" PGPASSWORD="$password" \
    timeout --kill-after=10 300 "$drdrill" verify --receipt "$receipt_file" \
    --db-host 127.0.0.1 --db-port "$pg_local_port" \
    --db-user "$user" \
    --server "$api_server" --scheme "$api_scheme" || rc=$?

  stop_port_forward

  # 🔴 A TIMEOUT IS NOT A VERDICT. 124 and 137 are `timeout`'s, not drdrill's, and
  # they mean the drill never reached an opinion. Letting either reach the caller
  # would make the negative control "hold" on a hung port-forward — a control that
  # passes when nothing was tested is worse than no control.
  if [[ $rc -eq 124 || $rc -eq 137 ]]; then
    fail "the drill TIMED OUT (exit $rc). That says nothing about the data: a dead
port-forward and a refused decrypt are indistinguishable from here, so this run is
INCONCLUSIVE and no result may be recorded from it. Re-run."
  fi

  return "$rc"
}

cmd_restore() {
  need_all
  build_tools
  load_exit_codes
  for f in "$escrow_file" "$receipt_file" "$backup_env_file"; do
    [[ -s "$f" ]] || fail "$f is missing or empty; run 'up' then 'disaster' first"
  done

  rebuild "$escrow_file" "the ESCROWED root key"

  # The premise, asserted with the shipped tool rather than assumed: this instance
  # really is running the key the artifact holds. `escrow verify` compares the
  # artifact's fingerprint against the key the instance is live on, so a restore
  # that silently seeded something else is caught here rather than being blamed on
  # the decrypt two steps later.
  say "confirming the rebuilt instance runs the ESCROWED key"
  "$dcctl" secrets escrow verify "$escrow_file" --instance "$instance" \
    --kube-context "$kube_context" ||
    fail "the rebuilt instance is NOT running the key in $escrow_file, so nothing
after this point would be a test of the escrow"

  say "waiting for the recovered instance API to route"
  wait_for_api user-management
  wait_for_api notification-management

  say "THE DRILL — can this cluster read the old cluster's secret?"
  local rc=0
  run_verify || rc=$?
  if [[ $rc -ne 0 ]]; then
    fail "the restored instance could NOT read the secret (drdrill exit $rc).
This is the finding the whole A5 workstream exists to prevent, reproduced in a
rehearsal. Read drdrill's output above: exit 3 means the escrowed key did not fit,
exit 4 means the row never came back, exit 1 means the drill could not run."
  fi
  say "DRILL PASSED — a secret sealed by a cluster that no longer exists was read by
the cluster that replaced it, from an archive and an escrow artifact alone."
}

# cmd_control is the check on the check.
#
# It recovers the SAME instance from the SAME archive under a root key that is not
# the instance's. Everything else is identical. If the secret still decrypts, then
# the drill's pass says nothing — the verifier is not actually testing the key —
# and the rig says so in those words.
#
# The wrong key arrives as a DECOY escrow artifact minted by `drdrill decoy`. That
# is not a shortcut around the CLI: dcctl refuses --restore-rdb-from without
# --restore-root-key (the guard against silently losing every secret), so the old
# control — "rebuild with --no-escrow" — can no longer be expressed at all. See
# the long comment on runDecoy for what the substitution does and does not stand
# in for.
cmd_control() {
  need_all
  build_tools
  load_exit_codes
  for f in "$escrow_file" "$receipt_file" "$backup_env_file"; do
    [[ -s "$f" ]] || fail "$f is missing or empty; run 'up' then 'disaster' first"
  done

  say "minting a decoy escrow artifact — a well-formed artifact holding the WRONG key"
  rm -f "$decoy_file"
  "$drdrill" decoy --instance "$instance" --out "$decoy_file" ||
    fail "could not mint the decoy artifact; there is no control to run"

  rebuild "$decoy_file" "a DECOY root key (the negative control)"

  # The control's premise, asserted BOTH ways. Either half alone is insufficient:
  #
  #   - verifying against the decoy proves the artifact was honoured rather than
  #     ignored, so a failed decrypt below is the key and not a broken restore;
  #   - failing to verify against the REAL artifact proves the key it is running
  #     is genuinely not the one the ciphertext was sealed under.
  #
  # If only the second were checked, an instance that had minted some third key of
  # its own would pass the check and the control would "hold" without the decoy
  # ever having taken effect.
  say "confirming the control instance runs the DECOY key"
  "$dcctl" secrets escrow verify "$decoy_file" --instance "$instance" \
    --kube-context "$kube_context" ||
    fail "the control instance is NOT running the decoy key, so --restore-root-key was
not honoured and this phase is not a control. Nothing below would be evidence."

  # 🔴 The real artifact is DECODED first, on its own.
  #
  # `escrow verify` exits non-zero identically for "the fingerprints differ", "this
  # file is not a readable artifact" and "the cluster would not answer" — one error
  # path, no distinct codes. So a truncated $escrow_file (still passing the -s
  # guard above) would fail to decode, and the premise below would be declared held
  # without a fingerprint ever having been compared. `escrow show` reads and parses
  # the artifact without touching the cluster, which separates the two.
  say "confirming the escrowed artifact is still readable at all"
  "$dcctl" secrets escrow show "$escrow_file" >/dev/null ||
    fail "$escrow_file does not parse as an escrow artifact. The check below would
'hold' for that reason rather than for a key mismatch, which is not a control."

  # And stderr is NOT suppressed here — if this refuses, the reason is the whole
  # point.
  say "confirming the control instance does NOT run the escrowed key"
  if "$dcctl" secrets escrow verify "$escrow_file" --instance "$instance" \
    --kube-context "$kube_context" >/dev/null; then
    fail "the control instance is running the ESCROWED key, so it is not a control.
It was recovered under a decoy artifact and should be running that key; that it is
not means the instance survived the disaster, or the key came from somewhere this
rig does not know about. Nothing below would be evidence."
  fi

  say "waiting for the recovered instance API to route"
  wait_for_api user-management
  wait_for_api notification-management

  say "NEGATIVE CONTROL — the same archive, recovered under a different root key"
  local rc=0
  run_verify || rc=$?

  case "$rc" in
    "$DRDRILL_EXIT_OK")
      fail "THE NEGATIVE CONTROL DID NOT HOLD.

An instance running a key it was never sealed under decrypted the old cluster's
secret. That is catastrophic in one of two ways, and both invalidate every drill
result:

  - the root key is not actually what protects the data, or
  - the drill is not reading what it claims to read.

No restore result may be recorded from this run."
      ;;
    "$DRDRILL_EXIT_DECRYPT_FAILED")
      say "NEGATIVE CONTROL HELD — the secret was present and did NOT decrypt.
The row recovered from the archive, the instance served it, and the key this
cluster was given could not open it. That is the failure mode the escrow artifact
exists to prevent, and it has now been observed rather than assumed."
      ;;
    *)
      fail "THE NEGATIVE CONTROL DID NOT RUN (drdrill exit $rc).

That is INCONCLUSIVE — neither a pass nor a failure. The control is only evidence
when it fails at the DECRYPT (exit $DRDRILL_EXIT_DECRYPT_FAILED); exit $DRDRILL_EXIT_NOT_FOUND means the row never
recovered and exit $DRDRILL_EXIT_SETUP means the drill could not get far enough to have an opinion.
Fix what the output above reports and re-run rather than reading this either way."
      ;;
  esac
}

cmd_down() {
  need kind; need docker
  delete_cluster
  minio_down
  if [[ -d "$work" ]]; then
    say "removing the rig's working directory $work"
    rm -rf "${work:?}"
  fi
  rm -rf "${HOME:?}/.devicechain/$instance"
}

case "${1:-all}" in
  up) cmd_up ;;
  archive) cmd_archive ;;
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
    say "A5 RESTORE DRILL COMPLETE: an instance recovered from an off-cluster archive
under the escrowed root key read the old cluster's secret, and the same archive
recovered under a different key demonstrably could not.
This is the evidence the ADR-028 recovery procedure rests on."
    ;;
  *) fail "unknown command ${1}; try up | archive | disaster | restore | control | all | down" ;;
esac
