#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The database-outage rig: sustained device telemetry through a REAL outage of the event store, and
# nothing is lost, dead-lettered or stored twice.
#
# WHAT THIS GATES
#
# core/storehold makes a consumer HOLD a message while its database cannot take writes, instead of
# spending the message's delivery budget (five deliveries, about four minutes) and then lettering
# and acknowledging it. Every consumer has a unit test for that. What no unit test can show is the
# CLAIM: that a DeviceChain instance, with its real broker, real PostgreSQL and real pods, loses
# nothing when the event store is gone for far longer than that budget. That needs a cluster, so
# this is a manual rig like hack/ha-rig.sh and hack/dr-rig.sh, and it follows their one rule: a
# check is worth nothing until it has been shown to fail.
#
#   hack/outage-rig.sh up        create the kind cluster dc-outrig (its own kubeconfig and host
#                                ports), install, bootstrap an instance from images built from THIS
#                                tree under a unique tag
#   hack/outage-rig.sh run       the positive run, then the negative control
#   hack/outage-rig.sh all       up + run
#   hack/outage-rig.sh controls  the offline controls only (no cluster)
#   hack/outage-rig.sh down      delete the cluster and dcctl's local state for it
#
# THE OUTAGE. FAST-FAILING, and that is the whole point. The event store (the dc-tsdb PostgreSQL
# cluster) is made READ-ONLY: `ALTER DATABASE <event db> SET default_transaction_read_only = on`,
# then every backend of that database is terminated so each pooled connection comes back with the
# setting. From then on the database ANSWERS every write at once with SQLSTATE 25006. Nothing hangs.
# That is the outage that loses events without the hold: every in-flight message is redelivered
# every AckWait (60 s), and the fifth delivery, about four minutes in, is exhausted and
# acknowledged away. (A real full disk lost 1.72M events that way.) A SLOW-failing outage -- a
# hibernated cluster, where each dial hangs for the connect timeout -- does NOT show it: the writers
# stall, the handoff channel fills and the reader stops fetching, which is an accidental hold. A rig
# built on hibernation passes against the old code, so it gates nothing.
#
# THE POSITIVE RUN. 60 MQTT devices publish 200 events/s at QoS 1 for DC_OUTRIG_DRIVE seconds
# (default 720). DC_OUTRIG_OUT_AT seconds in, the event store goes read-only for DC_OUTRIG_OUT_LEN
# seconds (default 420, seven minutes: well past the four-minute budget), then writable again. The
# load test (dc-loadtest-mqtt, --identity full) reads every PUBACKed event back by
# (device, occurredTime). The run PASSES only if
#   1  the harness exits 0 (every PUBACKed event is stored exactly once: lost 0, excess 0), and
#   2  failed-events, dead-letters, failed-decode and max-deliveries gained NOTHING, and
#   3  the event-management held gauge was above zero DURING the outage (the hold engaged: a run in
#      which the outage never touched a write would pass 1 and 2 for the wrong reason), and
#   4  it is back to zero afterwards, and
#   5  event-management logged refused writes (SQLSTATE 25006) during the outage: the outage was
#      fast-failing, not a hang.
#
# THE NEGATIVE CONTROL. The SAME outage against the OLD event-management, built from
# DC_OUTRIG_CONTROL_REF (default 69fb09b07, the parent of the merge that added the hold), swapped in
# for the control and swapped back afterwards. It MUST be red, and red because events were LOST or
# lettered (checks 1 or 2): without the hold the fifth delivery is spent and the message is gone.
# If it is green, the outage was not fast-failing (or the rig is blind) and the rig fails. It is
# the real thing, not a stand-in: DC_OUTRIG_CONTROL=quiet selects the weaker control instead (the
# same run with NO outage, which must be red only for check 3, the hold never engaging).
#
# OFFLINE CONTROLS (`controls`, and run first by `run`): the verdict function is fed fabricated
# reports -- one clean, one with a loss, one with an excess, one with a lettered event, one whose
# gauge never rose -- and must pass the first and fail every other.
#
# ISOLATION. The cluster has its own kubeconfig (DC_OUTRIG_STATE) and every kubectl, kind, helm and
# dcctl call is given it explicitly; this rig never reads or changes another kubeconfig's current
# context, and `down` deletes only its own cluster. It refuses the shared developer cluster by name.
#
# Requires: kind, kubectl, docker, jq, curl, ko, go, tofu or terraform. It pushes images under a
# unique tag to the local registry at localhost:5000 and never removes anything from it.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="${DC_OUTRIG_CLUSTER:-dc-outrig}"
kube_context="kind-$cluster"
instance="${DC_OUTRIG_INSTANCE:-outrig}"
namespace="dci-$instance"
state_dir="${DC_OUTRIG_STATE:-$repo_root/.outage-rig}"
http_port="${DC_OUTRIG_HTTP_PORT:-28080}"  # loopback port-forward to the ingress
mqtt_port="${DC_OUTRIG_MQTT_PORT:-21883}"  # kind host port -> the broker's MQTT NodePort
drive="${DC_OUTRIG_DRIVE:-720}"
out_at="${DC_OUTRIG_OUT_AT:-120}"
out_len="${DC_OUTRIG_OUT_LEN:-420}"
rate="${DC_OUTRIG_RATE:-200}"
devices="${DC_OUTRIG_DEVICES:-60}"
min_free_gb="${DC_OUTRIG_MIN_FREE_GB:-100}"
control_ref="${DC_OUTRIG_CONTROL_REF:-69fb09b07}"  # event-management before the hold (parent of its merge)
control_mode="${DC_OUTRIG_CONTROL:-old}"           # old | quiet
exe="$(go env GOEXE 2>/dev/null || true)"
dcctl="$repo_root/backend/cli/build/dcctl$exe"
loadtest="$state_dir/bin/dc-loadtest-mqtt$exe"

winpath() { if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s' "$1"; fi; }
mkdir -p "$state_dir"
kubeconfig="$(winpath "$state_dir/kubeconfig")"
export KUBECONFIG="$kubeconfig"

[[ "$cluster" != devicechain ]] || { echo "refusing: DC_OUTRIG_CLUSTER=devicechain is the shared developer cluster" >&2; exit 2; }

say() { printf '\n==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }
pass() { printf '    PASS %s\n' "$*"; }
red() { printf '    RED (as designed) %s\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }

k() { kubectl --kubeconfig "$kubeconfig" --context "$kube_context" "$@"; }
kn() { k -n "$namespace" "$@"; }

# ---------------------------------------------------------------------------
# The verdict. Pure: it reads numbers and prints the reasons it is red.
#   verdict <lost> <excess> <sink-gain> <held-peak> <held-after> [<harness-rc>]
# ---------------------------------------------------------------------------
verdict() {
  local lost="$1" excess="$2" sinks="$3" peak="$4" after="$5" rc="${6:-0}" bad=()
  [[ "$rc" == 0 ]] || bad+=("harness exited $rc")
  [[ "$lost" == 0 ]] || bad+=("$lost PUBACKed event(s) lost")
  [[ "$excess" == 0 ]] || bad+=("$excess event(s) stored more than once")
  [[ "$sinks" == 0 ]] || bad+=("$sinks message(s) reached a dead-letter / failed sink")
  awk -v p="$peak" 'BEGIN{exit !(p+0>0)}' || bad+=("the hold never engaged (held gauge peak $peak)")
  awk -v a="$after" 'BEGIN{exit !(a+0==0)}' || bad+=("held gauge is $after after recovery")
  ((${#bad[@]} == 0)) && return 0
  printf '%s\n' "${bad[@]}"
  return 1
}

offline_controls() {
  say "offline controls (no cluster)"
  verdict 0 0 0 11 0 >/dev/null || fail "control: a clean run was judged red"
  pass "clean run -> green"
  local c out
  for c in "5 0 0 11 0:a loss" "0 3 0 11 0:an excess" "0 0 7 11 0:a lettered event" "0 0 0 0 0:no hold" "0 0 0 11 4:a hold never released"; do
    # shellcheck disable=SC2086 # the fields are the point
    if out="$(verdict ${c%%:*} 2>&1)"; then fail "control: ${c##*:} was judged green"; fi
    red "${c##*:} -> red ($(sed -n 1p <<<"$out"))"
  done
  if verdict 0 0 0 11 0 2 >/dev/null 2>&1; then fail "control: a harness that exited 2 was judged green"; fi
  red "a harness exit status of 2 -> red"
}

# ---------------------------------------------------------------------------
# up / down
# ---------------------------------------------------------------------------
disk_guard() {
  local free
  free="$(df -Pk "$HOME" | awk 'NR==2 {printf "%d", $4/1024/1024}')"
  [[ "$free" -ge "$min_free_gb" ]] || fail "only ${free} GB free; the rig needs $min_free_gb (DC_OUTRIG_MIN_FREE_GB)"
  note "disk: ${free} GB free (floor $min_free_gb)"
}

cluster_exists() { grep -qx -- "$cluster" <<<"$(kind get clusters 2>/dev/null)"; }

cmd_up() {
  need kind; need kubectl; need docker; need jq; need ko; need go
  disk_guard
  if ! cluster_exists; then
    say "creating kind cluster $cluster"
    cat >"$state_dir/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
  - |-
    [plugins."io.containerd.grpc.v1.cri".registry.mirrors."localhost:5000"]
      endpoint = ["http://kind-registry:5000"]
nodes:
  - role: control-plane
    kubeadmConfigPatches:
      - |
        kind: InitConfiguration
        nodeRegistration:
          kubeletExtraArgs:
            node-labels: "ingress-ready=true"
    extraPortMappings:
      - containerPort: 31883
        hostPort: $mqtt_port
        listenAddress: "127.0.0.1"
        protocol: TCP
EOF
    kind create cluster --name "$cluster" --kubeconfig "$kubeconfig" --config "$(winpath "$state_dir/kind.yaml")" --wait 120s
  fi
  make -C "$(winpath "$repo_root")/backend/cli" build >/dev/null
  local tag
  tag="outrig-$(git -C "$repo_root" rev-parse --short=8 HEAD)-$(date -u +%Y%m%dT%H%M%SZ)"
  printf '%s\n' "$tag" >"$state_dir/image-tag"
  say "installing prerequisites and bootstrapping $instance (images $tag, built from this tree)"
  # --cluster, not --kube-context: dcctl then owns the cluster and can colocate (see hack/ota-rig.sh).
  "$dcctl" install local --yes --cluster "$cluster" --no-monitoring --build --version "$tag"
  "$dcctl" bootstrap local "$instance" --yes --no-escrow --cluster "$cluster" --host localhost --no-tls --build --version "$tag"
  say "UP COMPLETE: run 'hack/outage-rig.sh run'"
}

cmd_down() {
  need kind
  if cluster_exists; then
    k get ns kube-system >/dev/null 2>&1 ||
      fail "kind cluster $cluster exists but is not in $kubeconfig: not this rig's; refusing to delete it"
    local uid
    uid="$(k get ns kube-system -o jsonpath='{.metadata.uid}')"
    stop_forward
    [[ -x "$dcctl" ]] && "$dcctl" destroy local "$instance" --yes || true
    kind delete cluster --name "$cluster" --kubeconfig "$kubeconfig"
    [[ -n "$uid" && "$uid" != */* ]] && rm -rf "${HOME:?}/.devicechain/clusters/$uid"
  fi
  rm -f "$state_dir/kubeconfig" "$state_dir/image-tag"
}

# ---------------------------------------------------------------------------
# helpers for a run
# ---------------------------------------------------------------------------
forward_pid=""
start_forward() {
  k -n traefik port-forward svc/traefik "$http_port:80" >"$state_dir/forward.log" 2>&1 &
  forward_pid=$!
  for _ in $(seq 1 30); do
    curl -s "http://localhost:$http_port/" >/dev/null && return 0
    sleep 1
  done
  fail "the ingress did not answer on localhost:$http_port (see $state_dir/forward.log)"
}
stop_forward() { [[ -z "$forward_pid" ]] || kill "$forward_pid" 2>/dev/null || true; forward_pid=""; }
trap stop_forward EXIT

# nats runs the broker administrator's CLI in the broker's nats-box. The login travels as the CLI's
# own environment variables, never as an argument.
nats_admin() {
  local pw
  pw="$(kn get secret "dci-$instance-nats-broker-admin" -o jsonpath='{.data.password}' | base64 -d)"
  kn exec deploy/dc-nats-box -- env NATS_USER=dc-broker-admin-g1 NATS_PASSWORD="$pw" \
    nats --server nats://dc-nats:4222 --inbox-prefix _INBOX_broker-admin "$@"
}

# sink_total sums the messages in the four streams where a consumer's give-ups land.
sink_total() {
  local s n total=0
  for s in failed-events dead-letters failed-decode max-deliveries; do
    n="$(nats_admin stream info "${instance}_$s" --json | jq '.state.messages')"
    total=$((total + n))
  done
  printf '%s' "$total"
}

held_now() {
  local p v
  p="$(kn get pods -o name | grep '/event-management-' | sed -n 1p | cut -d/ -f2)"
  v="$(MSYS_NO_PATHCONV=1 k get --raw "/api/v1/namespaces/$namespace/pods/$p:8080/proxy/metrics" |
    awk '/^devicechain_eventmanagement_persist_store_held_events/ {print $2}')"
  printf '%s' "${v:-0}"
}

# psql_tsdb runs one statement as the superuser inside the event store's primary pod (local socket).
psql_tsdb() { kn exec dc-tsdb-1 -c postgres -- psql -U postgres -d postgres -v ON_ERROR_STOP=1 -tA -c "$1"; }

# event_db names the event store's database: the one holding event-management's schema.
event_db() {
  local d has
  for d in $(psql_tsdb "SELECT datname FROM pg_database WHERE NOT datistemplate AND datname <> 'postgres'"); do
    has="$(kn exec dc-tsdb-1 -c postgres -- psql -U postgres -d "$d" -tA -c "SELECT to_regclass('\"event-management\".event_management_migrations') IS NOT NULL" 2>/dev/null || true)"
    if [[ "$has" == t ]]; then printf '%s' "$d"; return 0; fi
  done
  fail "could not find the event store's database on dc-tsdb-1"
}

# store_readonly on|off makes the event store refuse every write at once (SQLSTATE 25006) or take
# writes again. The setting is a database default, so existing connections keep their old value:
# terminating the database's backends makes every pooled connection reconnect and pick it up.
store_readonly() {
  local db
  db="$(event_db)"
  psql_tsdb "ALTER DATABASE \"$db\" SET default_transaction_read_only = $1" >/dev/null
  psql_tsdb "SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity WHERE datname = '$db' AND pid <> pg_backend_pid()" >/dev/null
}

# refusals counts the lines of a saved event-management log that show a write refused by a
# read-only database (SQLSTATE 25006) -- proof the outage was fast-failing (an answer), not a hang.
refusals() {
  local n
  n="$(grep -c -e 'SQLSTATE 25006' "$1" || true)"
  printf '%s' "${n:-0}"
}

# one_run <sim-name> <outage: yes|no> prints
#   "<lost> <excess> <sink-gain> <held-peak> <held-after> <harness-rc> <refusals>"
# on stdout and nothing else (progress goes to stderr).
one_run() {
  local sim="$1" outage="$2" sinks0 lt_pid t0 now peak=0 h on=0 off=0 rc=0 rep refused=0 sampled=0
  "$dcctl" sim create "$sim" -i "$instance" -s "localhost:$http_port" --kube-context "$kube_context" \
    --mqtt-broker "ssl://localhost:$mqtt_port" >/dev/null
  sinks0="$(sink_total)"
  rep="$state_dir/$sim.json"
  "$loadtest" --handshake "$HOME/.devicechain/sims/$sim.json" --devices "$devices" --rate "$rate" \
    --duration "${drive}s" --identity full --quiesce-timeout 20m --report "$(winpath "$rep")" \
    >"$state_dir/$sim.log" 2>&1 &
  lt_pid=$!
  until grep -q "mqtt load interval" "$state_dir/$sim.log" 2>/dev/null || ! kill -0 "$lt_pid" 2>/dev/null; do sleep 3; done
  t0="$(date +%s)"
  while kill -0 "$lt_pid" 2>/dev/null; do
    now=$(($(date +%s) - t0))
    if [[ "$outage" == yes ]] && ((on == 0 && now >= out_at)); then
      note "t+${now}s: the event store goes read-only" >&2
      store_readonly on
      on=$now
    fi
    if ((on > 0 && off == 0 && now >= on + out_len)); then
      note "t+${now}s: the event store takes writes again" >&2
      store_readonly off
      off=1
    fi
    if ((on > 0 && off == 0)); then
      h="$(held_now)"
      awk -v h="$h" -v p="$peak" 'BEGIN{exit !(h+0>p+0)}' && peak="$h"
      if ((sampled == 0 && now >= on + out_len / 2)); then
        sampled=1
        kn logs deploy/event-management --since="$((now - on + 15))s" --tail=-1 >"$state_dir/$sim.outage-logs" 2>&1 || true
        refused="$(refusals "$state_dir/$sim.outage-logs")"
        note "t+${now}s: $refused refused-write (SQLSTATE 25006) log lines since the outage began" >&2
      fi
    fi
    sleep 10
  done
  wait "$lt_pid" || rc=$?
  if ((on > 0 && off == 0)); then store_readonly off; fi
  [[ -s "$rep" ]] || { echo "no-report no-report no-report $peak $(held_now) $rc $refused"; return; }
  printf '%s %s %s %s %s %s %s' "$(jq .storage.lost "$rep")" "$(jq .storage.excess "$rep")" \
    "$(($(sink_total) - sinks0))" "$peak" "$(held_now)" "$rc" "$refused"
}

build_loadtest() {
  mkdir -p "$state_dir/bin"
  (cd "$repo_root/backend/sims/dc-simulator" && go build -o "$loadtest" ./cmd/loadtest-mqtt)
}

# build_control_image builds event-management as it was at $control_ref (before the hold) and
# pushes it to the local registry under a unique tag; prints the image reference.
build_control_image() {
  local src tag
  tag="outrig-ctl-${control_ref}-$(date -u +%Y%m%dT%H%M%SZ)"
  src="$state_dir/control-src"
  rm -rf "$src"
  mkdir -p "$src"
  git -C "$repo_root" archive "$control_ref" -- backend deploy examples go.work go.work.sum .ko.yaml | tar -x -C "$src"
  (cd "$src/backend/services/event-management" &&
    KO_CONFIG_PATH="$src/.ko.yaml" KO_DOCKER_REPO=localhost:5000/event-management \
      ko build --bare --tags "$tag" --platform linux/amd64 ./ >&2)
  rm -rf "$src"
  printf 'localhost:5000/event-management:%s' "$tag"
}

cmd_run() {
  need kubectl; need jq; need curl; need go; need ko
  offline_controls
  k get ns "$namespace" >/dev/null 2>&1 || fail "no instance $instance on $cluster: run 'hack/outage-rig.sh up' first"
  [[ -x "$dcctl" ]] || fail "dcctl is not built; run 'hack/outage-rig.sh up'"
  build_loadtest
  start_forward

  say "POSITIVE: ${drive}s of ${rate} ev/s, event store refusing writes for ${out_len}s from t+${out_at}s"
  local res lost excess sinks peak after rc refused
  res="$(one_run "outrig-hold-$(date -u +%H%M%S)" yes)"
  read -r lost excess sinks peak after rc refused <<<"$res"
  note "lost=$lost excess=$excess sink-gain=$sinks held-peak=$peak held-after=$after harness-rc=$rc refused-log-lines=$refused"
  verdict "$lost" "$excess" "$sinks" "$peak" "$after" "$rc" || fail "the positive run is red"
  [[ "$refused" =~ ^[0-9]+$ && "$refused" -gt 0 ]] ||
    fail "no refused write was logged during the outage: it was not fast-failing, so this run proves nothing"
  pass "nothing lost, lettered or stored twice through a ${out_len}s fast-failing outage; the hold engaged and released"

  local why
  if [[ "$control_mode" == quiet ]]; then
    say "NEGATIVE CONTROL (weak): the same run with no outage"
    res="$(one_run "outrig-quiet-$(date -u +%H%M%S)" no)"
    read -r lost excess sinks peak after rc refused <<<"$res"
    note "lost=$lost excess=$excess sink-gain=$sinks held-peak=$peak held-after=$after harness-rc=$rc"
    if why="$(verdict "$lost" "$excess" "$sinks" "$peak" "$after" "$rc")"; then
      fail "the control PASSED with no outage: the hold-engaged check cannot fail"
    fi
    [[ "$why" == "the hold never engaged"* && "$(wc -l <<<"$why")" == 1 ]] ||
      fail "the control was red for the wrong reason: $why"
    red "no outage -> $why"
  else
    say "NEGATIVE CONTROL: the same outage against event-management as of $control_ref (no hold)"
    local img keep
    keep="$(kn get deploy event-management -o jsonpath='{.spec.template.spec.containers[0].image}')"
    img="$(build_control_image)"
    note "control image $img (restoring $keep afterwards)"
    kn set image deploy/event-management "*=$img" >/dev/null
    kn rollout status deploy/event-management --timeout=180s >/dev/null
    res="$(one_run "outrig-ctl-$(date -u +%H%M%S)" yes)" || true
    kn set image deploy/event-management "*=$keep" >/dev/null
    kn rollout status deploy/event-management --timeout=180s >/dev/null
    read -r lost excess sinks peak after rc refused <<<"$res"
    note "lost=$lost excess=$excess sink-gain=$sinks held-peak=$peak held-after=$after harness-rc=$rc refused-log-lines=$refused"
    if why="$(verdict "$lost" "$excess" "$sinks" "$peak" "$after" "$rc")"; then
      fail "the control PASSED against the pre-hold code: the outage is not fast-failing, or the rig is blind"
    fi
    [[ "$why" == *"lost"* || "$why" == *"dead-letter"* ]] ||
      fail "the control was red, but not because anything was lost or lettered: $why"
    red "pre-hold code under the same outage -> ${why//$'\n'/; }"
  fi
  say "ALL CHECKS PASSED"
}

rig_cmd="${1:-}"
case "$rig_cmd" in
  up) cmd_up ;;
  run) cmd_run ;;
  all) cmd_up; cmd_run ;;
  controls) offline_controls ;;
  down) cmd_down ;;
  *) fail "usage: hack/outage-rig.sh up | run | all | controls | down" ;;
esac
