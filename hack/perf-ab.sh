#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# A/B performance rig: measure a build's PER-EVENT costs at a fixed offered rate, on a local
# kind cluster, so a change can be judged before a GKE round is spent on it.
#
#   hack/perf-ab.sh --kubeconfig KC --namespace dci-perf --instance perf --arm main-1 \
#       --bin-dir DIR --dcctl PATH --out DIR [--rebalance] [--roll] [--mqtt-rate 1000] \
#       [--http-rate 500] [--duration 180] [--warmup 45] [--settle 40]
#
#   hack/perf-ab.sh --self-test          # runs the summariser against a synthetic fixture
#
# One invocation is one ARM, run once: warm-up, a steady window, counters read before and
# after the window, then <out>/<arm>/summary.json and summary.md. Compare arms (and set the
# noise band from identical replicates) with hack/perf-ab-compare.sh.
#
# WHAT IT MEASURES, per event persisted in the window (events = rows inserted into the event
# store's events table, from pg_stat_statements, read at the same instant as that database's WAL):
#
#   NATS messages        client in/out and non-client in/out (varz in/out minus the client
#                        connections' /connz), with the route share split out (/routez), plus
#                        bytes and JetStream API calls (varz jetstream.stats.api.total)
#   database WAL         pg_current_wal_lsn() bytes, WAL records and full-page images on the event-store
#                        and the relational primary (bytes are not reported if a checkpoint fell in the window)
#   checkpoint cause     per database, checkpoints timed (checkpoint_timeout) vs requested (max_wal_size or
#                        an explicit CHECKPOINT), and a one-word cause for the window
#   table writes         per table, from pg_stat_user_tables: the HOT-update ratio (n_tup_hot_upd / n_tup_upd),
#                        dead tuples at the end of the window, autovacuum and autoanalyze runs in it
#   statements           rows per INSERT into events and per device_states upsert, statements/s
#   CPU                  cAdvisor container CPU-seconds per 1,000 events, per component; and the postgres
#                        container of each database split by ROLE (cpu.dbinst.<store>.primary / .standby),
#                        so the standby replay cost is not folded into the primary's
#   the harness verdict  accepted, persisted, loss and duplicates, from dc-loadtest(-mqtt)'s own report
#
# WHAT TRANSFERS TO GKE, and what does not
#
#   transfers      message counts per event, bytes per event, WAL bytes per event, rows per
#                  statement: all counts, compared AT THE SAME OFFERED RATE. Messages per event
#                  FALL as the rate rises (raft rounds batch more entries), so a kind figure at
#                  1,500 events/s is comparable to a GKE figure at 1,500 events/s and to nothing else.
#   a ratio only   CPU-seconds per 1,000 events: absolute values depend on the machine, a ratio
#                  between two arms on the same machine is usable.
#   does not       latency, saturation, throttling, absolute CPU, tail behaviour, anything that
#                  depends on the disk or the network.
#
# THE LOAD runs INSIDE the cluster (one pod, `perf-ab-loadgen`, which this script creates), so no
# port-forward is held open: the harnesses reach event-sources and the broker by service name. The
# sims (one tenant per transport, prefix --sim-prefix) are created on the first run and REUSED: a
# destroyed tenant is purged by every area, and that purge would overlap and distort the next arm.
# Before the load it waits for the databases to stop deleting, and takes a CHECKPOINT on both
# primaries, so no timed checkpoint (and the full-page images after it) lands in the window.
# --roll restarts the instance's Deployments first (the broker and the databases are left alone):
# rolling the pods moves the route message count by 15-20 percent on an identical build, so an arm
# that changes the deployment must be compared with baselines that were rolled too.
#
# PRECONDITIONS (once per cluster, see hack/README-perf.md "Local A/B rig"):
#   - the instance was bootstrapped with TF_VAR_timescale_statement_stats=true, and the cluster
#     installed with TF_VAR_postgres_statement_stats=true (pg_stat_statements on both primaries);
#   - Linux binaries of dc-loadtest and dc-loadtest-mqtt in --bin-dir
#     (GOOS=linux CGO_ENABLED=0 go build ./cmd/loadtest ./cmd/loadtest-mqtt in backend/sims/dc-simulator);
#   - dcctl on PATH or --dcctl, and a ready instance.
#
# SAFETY: only the kubeconfig named is used (KUBECONFIG is replaced, the context never switched);
# nothing is port-forwarded; the loadgen pod is removed on exit (and the sims too with --fresh-sims).
# Credentials are never printed: the sim handshakes are rewritten and streamed into the pod, never read.
#
# Requires: kubectl, jq, python3 (or python), dcctl (when sims are created), date, awk.
# Exit: 0 measured and the harnesses' verdicts passed; 1 measured, a verdict failed (a counted shed, or a
# stall in the window); 2 usage; 3 refused/aborted; 4 an UNEXPLAINED LOSS (a durability alarm).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SELF="$ROOT/hack/$(basename "${BASH_SOURCE[0]}")"
HELPER="$ROOT/hack/perf-ab/perf_ab.py"
KUBECTL="${KUBECTL:-kubectl}"

say() { echo "perf-ab: $*" >&2; }
die() { say "$*"; exit 3; }
usage_err() { say "$*"; exit 2; }
usage() { sed -n '4,12p' "$SELF" | sed 's/^# \{0,1\}//'; exit 2; }

py() { # the interpreter: python3 where it is real, python on Windows
  if [[ -n "${PYTHON:-}" ]]; then "$PYTHON" -I "$@"
  elif python3 -c 'import sys' >/dev/null 2>&1; then python3 -I "$@"
  else python -I "$@"; fi
}

# ---------------------------------------------------------------------------
# Arguments
# ---------------------------------------------------------------------------
kubeconfig="" ns="" instance="" arm="" out="perf-ab-out" dcctl="${DCCTL:-dcctl}" bin_dir=""
mqtt_rate=1000 http_rate=500 duration=180 warmup=45 settle=40 tail=20 stagger=90
rebalance=0 roll=0 rdb_ns="dc-system" rdb_cluster="dc-rdb" tsdb_cluster="dc-tsdb" nats_prefix="dc-nats"
console_host="localhost:8080" fresh_sims=0 sim_prefix="abt" tier_multiple=10 self_test=0
loadgen="perf-ab-loadgen" loadgen_image="${LOADGEN_IMAGE:-debian:12-slim}"
nats_monitor_port=8222

while [[ $# -gt 0 ]]; do
  case "$1" in
    --self-test) self_test=1; shift ;;
    --kubeconfig) kubeconfig="$2"; shift 2 ;;
    --namespace|-n) ns="$2"; shift 2 ;;
    --instance) instance="$2"; shift 2 ;;
    --arm) arm="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    --dcctl) dcctl="$2"; shift 2 ;;
    --bin-dir) bin_dir="$2"; shift 2 ;;
    --mqtt-rate) mqtt_rate="$2"; shift 2 ;;
    --http-rate) http_rate="$2"; shift 2 ;;
    --duration) duration="$2"; shift 2 ;;
    --warmup) warmup="$2"; shift 2 ;;
    --settle) settle="$2"; shift 2 ;;
    --stagger) stagger="$2"; shift 2 ;;
    --rebalance) rebalance=1; shift ;;
    --roll) roll=1; shift ;;
    --rdb-namespace) rdb_ns="$2"; shift 2 ;;
    --console-host) console_host="$2"; shift 2 ;;
    --fresh-sims) fresh_sims=1; shift ;;
    --sim-prefix) sim_prefix="$2"; shift 2 ;;
    --tier-multiple) tier_multiple="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) say "unknown argument: $1"; usage ;;
  esac
done

if [[ "$self_test" == 1 ]]; then
  py "$HELPER" self-test
  exit $?
fi

[[ -n "$kubeconfig" ]] || usage_err "--kubeconfig is required (a lane-private file, never the ambient one)"
[[ -n "$ns" ]] || usage_err "--namespace is required"
[[ -n "$instance" ]] || usage_err "--instance is required"
[[ -n "$arm" ]] || usage_err "--arm is required"
[[ "$arm" =~ ^[a-z0-9][a-z0-9-]{0,19}$ ]] || usage_err "--arm must be [a-z0-9-] and at most 20 characters (it names the sims, with a run suffix)"
[[ -n "$bin_dir" && -f "$bin_dir/dc-loadtest" && -f "$bin_dir/dc-loadtest-mqtt" ]] \
  || usage_err "--bin-dir must hold Linux dc-loadtest and dc-loadtest-mqtt"
for n in mqtt_rate http_rate duration warmup settle tail stagger tier_multiple; do
  [[ "${!n}" =~ ^[0-9]+$ ]] || usage_err "--${n//_/-} must be a whole number"
done
[[ "$duration" -ge 60 ]] || usage_err "--duration under 60 s is mostly noise"

k() { # kubectl bound to the lane-private kubeconfig. MSYS2_ARG_CONV_EXCL: Git Bash would rewrite
  # an argument beginning with "/" (a --raw path, a pod-side path) into a Windows path.
  env -u KUBECONFIG MSYS2_ARG_CONV_EXCL='*' "$KUBECTL" --kubeconfig "$kubeconfig" "$@"
}
kctx="$(k config current-context)" || die "cannot read the kubeconfig's context"

ts="$(date -u +%Y%m%dT%H%M%SZ)"
run_dir="$out/$arm"
raw="$run_dir/raw"
if [[ -e "$run_dir" ]]; then run_dir="$out/$arm-$ts"; raw="$run_dir/raw"; fi
mkdir -p "$raw"
say "arm=$arm context=$kctx namespace=$ns -> $run_dir"

# ---------------------------------------------------------------------------
# Cleanup: stop the harnesses, remove the sims and the loadgen pod. Idempotent.
# ---------------------------------------------------------------------------
sims=()
cleaned=0
# shellcheck disable=SC2317,SC2329 # run from cleanup, which runs from the EXIT trap
stop_load() { # kill every dc-loadtest* process in the pod, by name, and nothing else
  k exec -n "$ns" "$loadgen" -- sh -c 'for p in /proc/[0-9]*; do
      c="$(tr "\0" " " < "$p/cmdline" 2>/dev/null)"
      case "$c" in /rc/bin/dc-loadtest*) kill "${p#/proc/}" 2>/dev/null;; esac
    done; true' >/dev/null 2>&1 || true
}
# shellcheck disable=SC2317,SC2329 # the EXIT trap (shellcheck versions name it differently)
cleanup() {
  [[ "$cleaned" == 1 ]] && return 0
  cleaned=1
  stop_load
  if [[ "$fresh_sims" == 1 ]]; then
    local s
    for s in "${sims[@]}"; do
      env KUBECONFIG="$kubeconfig" "$dcctl" sim destroy "$s" -s "$console_host" -i "$instance" --kube-context "$kctx" >/dev/null 2>&1 || true
    done
  fi
  k delete pod "$loadgen" -n "$ns" --ignore-not-found --wait=false --grace-period=2 >/dev/null 2>&1 || true
}
trap 'cleanup' EXIT
trap 'say "interrupted"; exit 3' INT TERM

# ---------------------------------------------------------------------------
# Inventory
# ---------------------------------------------------------------------------
pods_json="$(k get pods -n "$ns" -o json)"
mapfile -t nats_pods < <(jq -r --arg p "^${nats_prefix}-[0-9]+\$" '.items[]|select(.metadata.name|test($p))|select(.status.phase=="Running")|.metadata.name' <<<"$pods_json" | tr -d '\r' | sort)
[[ "${#nats_pods[@]}" -ge 1 ]] || die "no running ${nats_prefix}-N pods in $ns"
tsdb_pod="$(jq -r --arg c "$tsdb_cluster" '[.items[]|select(.metadata.labels["cnpg.io/cluster"]==$c and .metadata.labels["cnpg.io/instanceRole"]=="primary" and .status.phase=="Running")|.metadata.name][0] // ""' <<<"$pods_json" | tr -d '\r')"
[[ -n "$tsdb_pod" ]] || die "no running primary for CNPG cluster $tsdb_cluster in $ns"
rdb_pod="$(k get pods -n "$rdb_ns" -o json | jq -r --arg c "$rdb_cluster" '[.items[]|select(.metadata.labels["cnpg.io/cluster"]==$c and .metadata.labels["cnpg.io/instanceRole"]=="primary" and .status.phase=="Running")|.metadata.name][0] // ""' | tr -d '\r')"
[[ -n "$rdb_pod" ]] || die "no running primary for CNPG cluster $rdb_cluster in $rdb_ns"
mapfile -t node_names < <(k get nodes -o name | sed 's|^node/||' | tr -d '\r')
say "nats: ${nats_pods[*]}; event-store primary: $tsdb_pod; relational primary: $rdb_pod; nodes: ${#node_names[@]}"

psql_at() { # namespace pod sql -> stdout
  k exec -n "$1" "$2" -c postgres -- psql -X -At -v ON_ERROR_STOP=1 -d postgres -c "$3"
}
psql_db() { # namespace pod database sql -> stdout
  k exec -n "$1" "$2" -c postgres -- psql -X -At -v ON_ERROR_STOP=1 -d "$3" -c "$4"
}
# shellcheck source=lib/dbstats.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/dbstats.sh"
for pair in "$ns:$tsdb_pod" "$rdb_ns:$rdb_pod"; do
  psql_at "${pair%%:*}" "${pair#*:}" "CREATE EXTENSION IF NOT EXISTS pg_stat_statements" >/dev/null 2>&1 || true
  psql_at "${pair%%:*}" "${pair#*:}" "SELECT count(*) FROM pg_stat_statements_info" >/dev/null 2>&1 \
    || die "pg_stat_statements is not readable on ${pair#*:}: turn it on at install/bootstrap (hack/README-perf.md: TF_VAR_postgres_statement_stats / TF_VAR_timescale_statement_stats)"
done

# ---------------------------------------------------------------------------
# Roll the services (optional)
# ---------------------------------------------------------------------------
# Where each service's broker connection lands, and which node each pod runs on, are fixed when the
# pod starts, and they move the route message count by 10-15 percent (measured: identical
# configuration, before and after a rollout). An arm that changes the deployment is rolled by that
# change, so compare it with baselines that were rolled too: --roll restarts every Deployment of the
# instance (not the broker or the databases) before the measurement.
if [[ "$roll" == 1 ]]; then
  say "rolling the instance's Deployments"
  mapfile -t deployments < <(k get deploy -n "$ns" -o name | tr -d '\r')
  for d in "${deployments[@]}"; do k rollout restart -n "$ns" "$d" >/dev/null; done
  for d in "${deployments[@]}"; do k rollout status -n "$ns" "$d" --timeout=300s >/dev/null || die "$d did not finish rolling"; done
  sleep 30
fi

# ---------------------------------------------------------------------------
# Broker layout (and optionally rebalance to the even layout first)
# ---------------------------------------------------------------------------
jsz_layout() { # label
  k get --raw "/api/v1/namespaces/$ns/pods/${nats_pods[0]}:$nats_monitor_port/proxy/jsz?accounts=true&streams=true&consumers=true" \
    > "$raw/layout-$1.json" 2>/dev/null || say "warning: could not read /jsz for the layout record"
}
jsz_layout before-rebalance
if [[ "$rebalance" == 1 ]]; then
  say "rebalancing broker leaders (dcctl broker rebalance $instance)"
  env KUBECONFIG="$kubeconfig" "$dcctl" broker rebalance "$instance" --kube-context "$kctx" >"$run_dir/rebalance.log" 2>&1 \
    || die "dcctl broker rebalance failed; see $run_dir/rebalance.log"
  sleep 10
fi
jsz_layout run

# ---------------------------------------------------------------------------
# Loadgen pod and sims
# ---------------------------------------------------------------------------
k delete pod "$loadgen" -n "$ns" --ignore-not-found --wait=true >/dev/null 2>&1 || true
k apply -n "$ns" -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $loadgen
  labels: {app: perf-ab-loadgen}
spec:
  restartPolicy: Never
  containers:
  - name: lg
    image: $loadgen_image
    command: ["sleep", "infinity"]
    resources:
      requests: {cpu: "500m", memory: 512Mi}
      limits: {cpu: "3", memory: 2Gi}
EOF
k wait -n "$ns" --for=condition=Ready "pod/$loadgen" --timeout=240s >/dev/null || die "the loadgen pod did not become ready"
k exec -n "$ns" "$loadgen" -- mkdir -p /rc/bin /rc/hs /rc/run
put() { # local file -> path in the pod, over stdin (kubectl cp mistakes a Windows drive letter for a pod name)
  k exec -i -n "$ns" "$loadgen" -- sh -c "cat > '$2'" < "$1"
}
put "$bin_dir/dc-loadtest" /rc/bin/dc-loadtest
put "$bin_dir/dc-loadtest-mqtt" /rc/bin/dc-loadtest-mqtt
k exec -n "$ns" "$loadgen" -- chmod +x /rc/bin/dc-loadtest /rc/bin/dc-loadtest-mqtt

sim_dir="${DC_HOME:-$HOME/.devicechain}/sims"
# The sims run in a tier whose ingest ceiling is far above the offered rate and whose burst holds many
# seconds of it. The rig measures cost, not governance: a tenant AT the platform default ceiling
# (1,000/s, burst 2,000) has its PUBACKed events shed by the ingest gate whenever the host stalls the
# broker for more than ~2 s, which the harness reports as loss. The tier is named for the rate it was
# sized for and the sim names carry it, so a sim made at another tier is never reused by mistake.
max_rate=$((mqtt_rate > http_rate ? mqtt_rate : http_rate))
tier_token="ab-load-$max_rate"
tier_msgs=$((tier_multiple * max_rate))
tier_burst=$((2 * tier_msgs))
tier_config="{\"ingestReadingsPerSecond\":$tier_msgs,\"ingestBurst\":$tier_burst}"
mk_sim() { # name
  if [[ "$fresh_sims" == 1 || ! -f "$sim_dir/$1.json" ]]; then
    env KUBECONFIG="$kubeconfig" "$dcctl" sim create "$1" -s "$console_host" -i "$instance" --kube-context "$kctx" \
      --tier "$tier_token" --tier-config "$tier_config" \
      --ingress "http://event-sources.$ns.svc.cluster.local:8081" \
      --mqtt-broker "ssl://${nats_prefix}.$ns.svc.cluster.local:1883" >"$run_dir/sim-$1.log" 2>&1 \
      || die "dcctl sim create $1 failed; see $run_dir/sim-$1.log"
  fi
  sims+=("$1")
  # The handshake names the console host dcctl used (reachable from this machine, not from the pod).
  # Point each GraphQL endpoint at its Service instead: /api/<area>/graphql is <area>:8080/graphql.
  # The rewritten file goes straight into the pod over stdin and is never printed.
  jq --arg ns "$ns" '.endpoints |= with_entries(.value |= (if type == "string" then sub("^(?<s>https?|wss?)://[^/]+/api/(?<a>[a-z-]+)/graphql$"; "\(.s)://\(.a).\($ns).svc.cluster.local:8080/graphql") else . end))'     "$sim_dir/$1.json" | put /dev/stdin "/rc/hs/$1.json"
}
# THE SIMS ARE REUSED across runs (one tenant per transport; devices are created on the first run).
# Destroying them after each run starts a tenant purge in every area: DELETEs of the whole run's
# rows that overlap the NEXT run and drift its statement, WAL and CPU figures (measured: the
# event-store statement rate rose 1,450 -> 2,770 /s over five identical arms). --fresh-sims
# gives each run tenants of its own instead (a destroyed tenant's token stays reserved, so the
# run's time of day is part of the name) and destroys them on exit.
if [[ "$fresh_sims" == 1 ]]; then sim_m="${arm}-${ts:9:6}-m"; sim_h="${arm}-${ts:9:6}-h"
else sim_m="${sim_prefix}-${max_rate}-m"; sim_h="${sim_prefix}-${max_rate}-h"; fi
[[ "$mqtt_rate" -gt 0 ]] && mk_sim "$sim_m"
[[ "$http_rate" -gt 0 ]] && mk_sim "$sim_h"

# ---------------------------------------------------------------------------
# Snapshots. Every source is read in parallel so the "before" (and "after") instants
# are within a second or two of each other.
# ---------------------------------------------------------------------------
dbstats_init

snap() { # label
  local d="$raw/$1" p n pid rc=0
  local -a pids=()
  mkdir -p "$d"
  date -u +%s > "$d/start.epoch"
  for p in "${nats_pods[@]}"; do
    (
      local base="/api/v1/namespaces/$ns/pods/$p:$nats_monitor_port/proxy"
      k get --raw "$base/varz" > "$d/nats-$p-varz.json"
      k get --raw "$base/routez" > "$d/nats-$p-routez.json"
      k get --raw "$base/connz?state=all&limit=100000" > "$d/nats-$p-connz.json"
      k get --raw "$base/jsz?accounts=true&streams=true&consumers=true" > "$d/nats-$p-jsz.json"
    ) &
    pids+=($!)
  done
  # The database snapshots (statements, WAL, checkpoint cause, per-table writes, pod roles) are one
  # shared function, so hack/perf-ab/db-rows-rig.sh runs exactly the code a real arm runs.
  ( dbstats_snap_dbs "$d" ) & pids+=($!)
  for n in "${node_names[@]}"; do
    ( k get --raw "/api/v1/nodes/$n/proxy/metrics/cadvisor" \
        | grep -E '^container_cpu_(usage_seconds_total|cfs_throttled_seconds_total)\{' > "$d/cadvisor-$n.prom" || true ) &
    pids+=($!)
  done
  for pid in "${pids[@]}"; do wait "$pid" || rc=1; done
  date -u +%s > "$d/end.epoch"
  k get pods -n "$ns" -o json | jq -c '[.items[]|{n:.metadata.name,u:.metadata.uid,r:([.status.containerStatuses[]?.restartCount]|add)}]' > "$d/pods.json"
  return "$rc"
}

# ---------------------------------------------------------------------------
# Quiet: a tenant purge or a drain still running from earlier work would be measured as this arm's.
# Wait until the databases have run no DELETE for two consecutive 10 s looks.
# ---------------------------------------------------------------------------
deletes_now() { # namespace pod -> total calls of every DELETE statement, or x
  psql_at "$1" "$2" "SELECT coalesce(sum(calls), 0)::bigint FROM pg_stat_statements WHERE query ILIKE 'delete from%'" 2>/dev/null | tr -d '\r' || echo x
}
quiet_deadline=$((SECONDS + 900))
calm=0 prev=""
while [[ $calm -lt 2 ]]; do
  cur="$(deletes_now "$ns" "$tsdb_pod")/$(deletes_now "$rdb_ns" "$rdb_pod")"
  if [[ "$cur" == "$prev" ]]; then calm=$((calm + 1)); else calm=0; fi
  prev="$cur"
  [[ $SECONDS -gt $quiet_deadline ]] && die "the databases are still deleting after 15 minutes (a tenant purge?); not measuring on top of it"
  [[ $calm -lt 2 ]] && sleep 10
done
# a reused tenant must have been idle for 30 s before the harnesses accept it
[[ "$fresh_sims" == 1 ]] || sleep 35

# ---------------------------------------------------------------------------
# Drive
# ---------------------------------------------------------------------------
# The harnesses set up (devices, sessions) for different lengths of time before their drives start, so
# their steady windows are offset; --stagger is the margin that keeps the window inside both.
drive=$((warmup + settle + duration + tail + stagger))
# The ingest gate's shed counters, over the whole drive (the harness's loss is over the whole drive too).
shed_snap() { # label
  local p
  local -a es_pods=()
  mkdir -p "$raw/$1"
  mapfile -t es_pods < <(k get pods -n "$ns" -o json | jq -r '.items[]|select(.metadata.name|test("^event-sources-"))|.metadata.name' | tr -d '\r')
  for p in "${es_pods[@]}"; do
    k get --raw "/api/v1/namespaces/$ns/pods/$p:8080/proxy/metrics" \
      | grep -E '^devicechain_eventsources_total_(msg_rate_limited|msg_reading_limited|readings_rate_limited|msg_publish_failed|msg_backpressured)[{ ]' > "$raw/$1/shed-$p.prom" || true
  done
}
shed_snap shed-start
load_start_iso="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
marker_dir=/rc/run
if [[ "$mqtt_rate" -gt 0 ]]; then
  # One MQTT session per device at one event per second, as the GKE rounds did.
  k exec -n "$ns" "$loadgen" -- sh -c "nohup setsid /rc/bin/dc-loadtest-mqtt --handshake /rc/hs/$sim_m.json --devices $mqtt_rate --rate $mqtt_rate --duration ${drive}s --qos 1 --identity count --quiesce-timeout 3m --quiesce-settle 15s --report $marker_dir/mqtt.json --steady-marker $marker_dir/steady-mqtt.json --steady-phase AB --steady-warmup ${warmup}s > $marker_dir/mqtt.log 2>&1 < /dev/null &"
fi
if [[ "$http_rate" -gt 0 ]]; then
  http_dev=$((http_rate / 5 > 0 ? http_rate / 5 : 1)) # 200 ms emit interval: devices = rate / 5
  k exec -n "$ns" "$loadgen" -- sh -c "nohup setsid /rc/bin/dc-loadtest --handshake /rc/hs/$sim_h.json --devices $http_dev --emit-interval 200ms --hold ${drive}s --min-accepted 1000 --quiesce-timeout 3m --quiesce-settle 15s --state-timeout 2m --report $marker_dir/http.json --steady-marker $marker_dir/steady-http.json --steady-phase AB --steady-warmup ${warmup}s > $marker_dir/http.log 2>&1 < /dev/null &"
fi

marker_state() { # name -> state
  k exec -n "$ns" "$loadgen" -- cat "$marker_dir/steady-$1.json" 2>/dev/null | jq -r '.state // "none"' 2>/dev/null | tr -d '\r' || echo none
}
names=()
[[ "$mqtt_rate" -gt 0 ]] && names+=(mqtt)
[[ "$http_rate" -gt 0 ]] && names+=(http)
say "load started: mqtt=${mqtt_rate}/s http=${http_rate}/s, warm-up ${warmup}s, window ${duration}s"
deadline=$((SECONDS + warmup + 240))
while :; do
  all=1
  for n in "${names[@]}"; do [[ "$(marker_state "$n")" == steady ]] || all=0; done
  [[ "$all" == 1 ]] && break
  [[ $SECONDS -gt $deadline ]] && die "the harnesses never reached steady state (see the loadgen logs); aborting"
  sleep 4
done
# Both steady windows must still be open for the whole measurement. Their ends differ by the
# harnesses' set-up times; refuse now rather than after the window has been spent.
need=$((settle + duration + 25))
for n in "${names[@]}"; do
  wend="$(k exec -n "$ns" "$loadgen" -- cat "$marker_dir/steady-$n.json" | jq -r '.windowEnds' | tr -d '\r')"
  left=$(( $(date -u -d "${wend%%.*}Z" +%s) - $(date -u +%s) ))
  say "$n steady window has ${left}s left; the measurement needs ${need}s"
  [[ "$left" -ge "$need" ]] || die "the $n window closes too soon (raise --stagger by $((need - left)) s)"
done
# A checkpoint inside the window makes every page touched after it write a full-page image, which
# moves WAL bytes per event by tens of percent. A timed one comes 5 minutes after the last, so take
# one now: the window then starts --settle seconds after it and ends well before the next.
psql_at "$ns" "$tsdb_pod" "CHECKPOINT" >/dev/null 2>&1 || say "warning: could not checkpoint the event store"
psql_at "$rdb_ns" "$rdb_pod" "CHECKPOINT" >/dev/null 2>&1 || say "warning: could not checkpoint the relational store"
say "steady; checkpointed; settling ${settle}s"
sleep "$settle"

snap pre || die "the 'before' snapshot failed"
t_pre="$(date -u +%s)"
say "window open"
sleep $((duration - ($(date -u +%s) - t_pre)))
snap post || die "the 'after' snapshot failed"
# WAL bytes include the full-page images that follow a checkpoint, and a window of this length at this
# rate holds one about half the time (max_wal_size triggers a checkpoint every ~350 MB). Split them
# out exactly: pg_walinspect sums the images in the window's LSN range. Done now, while the segments
# are still in pg_wal; it reads the window's WAL once, after the measurement.
wal_split() { # namespace pod name
  local start end
  start="$(jq -r '.lsnText' "$raw/pre/db-$3.json" | tr -d '\r')"
  end="$(jq -r '.lsnText' "$raw/post/db-$3.json" | tr -d '\r')"
  psql_at "$1" "$2" "CREATE EXTENSION IF NOT EXISTS pg_walinspect" >/dev/null 2>&1 || true
  psql_at "$1" "$2" "SELECT jsonb_build_object('fpiBytes', coalesce(sum(fpi_size), 0)::bigint, 'combinedBytes', coalesce(sum(combined_size), 0)::bigint) FROM pg_get_wal_stats('$start', '$end')" \
    > "$raw/post/walstats-$3.json" 2>/dev/null || { rm -f "$raw/post/walstats-$3.json"; say "warning: could not split full-page images out of the $3 WAL (pg_walinspect)"; }
}
wal_split "$ns" "$tsdb_pod" tsdb
wal_split "$rdb_ns" "$rdb_pod" rdb
say "window closed (snapshots took $(( $(date -u +%s) - t_pre - duration )) s over the window)"
for n in "${names[@]}"; do
  [[ "$(marker_state "$n")" == steady ]] || die "the $n harness left the steady state before the 'after' snapshot: the window is not clean"
done

# ---------------------------------------------------------------------------
# Wait for the harnesses, fetch the reports
# ---------------------------------------------------------------------------
say "waiting for the harnesses to finish (quiesce and read-back)"
wait_end=$((SECONDS + tail + 900))
while :; do
  left="$(k exec -n "$ns" "$loadgen" -- sh -c 'n=0; for p in /proc/[0-9]*; do c="$(tr "\0" " " < "$p/cmdline" 2>/dev/null)"; case "$c" in /rc/bin/dc-loadtest*) n=$((n+1));; esac; done; echo $n' | tr -d '\r ')"
  [[ "$left" == 0 ]] && break
  [[ $SECONDS -gt $wait_end ]] && { say "harness still running at the timeout"; break; }
  sleep 8
done
shed_snap shed-end
# Stalls the broker logged since the load started (UTC, as the server stamps them): the summary places
# each against the measured window.
: > "$raw/nats-events.log"
for p in "${nats_pods[@]}"; do
  { k logs -n "$ns" "$p" -c nats --since-time="$load_start_iso" \
      | grep -E 'took too long|NO quorum|stalled|new (stream |consumer |metadata )?leader|leader (changed|lost)|[Ss]tep[- ]?down' \
      | sed "s|^|$p |" >> "$raw/nats-events.log"; } || true
done
for n in "${names[@]}"; do
  k exec -n "$ns" "$loadgen" -- cat "$marker_dir/$n.json" > "$raw/report-$n.json" 2>/dev/null || say "warning: no $n report"
  k exec -n "$ns" "$loadgen" -- tail -n 40 "$marker_dir/$n.log" > "$raw/harness-$n.log" 2>/dev/null || true
done
for n in "${names[@]}"; do
  if [[ "$(wc -c < "$raw/report-$n.json" 2>/dev/null || echo 0)" -lt 10 ]]; then rm -f "$raw/report-$n.json"; fi
done

# ---------------------------------------------------------------------------
# Summarise
# ---------------------------------------------------------------------------
jq -n --arg arm "$arm" --arg ctx "$kctx" --arg ns "$ns" --arg ts "$ts" --arg tsdb "$tsdb_pod" --arg rdb "$rdb_pod" \
  --argjson mqtt "$mqtt_rate" --argjson http "$http_rate" --argjson dur "$duration" --argjson warm "$warmup" \
  --argjson rb "$rebalance" --arg nats "${nats_pods[*]}" --arg tier "$tier_token" --argjson tmsgs "$tier_msgs" --argjson tburst "$tier_burst" \
  '{arm:$arm, context:$ctx, namespace:$ns, startedAt:$ts, eventStorePrimary:$tsdb, relationalPrimary:$rdb,
    offeredMqtt:$mqtt, offeredHttp:$http, offered:($mqtt+$http), durationS:$dur, warmupS:$warm, rebalanced:($rb==1),
    natsPods:($nats|split(" ")), tier:$tier, tierIngestPerS:$tmsgs, tierBurst:$tburst}' > "$run_dir/meta.json"
rc=0
py "$HELPER" summarize --dir "$run_dir" || rc=$?
exit "$rc"
