#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Shows the database rows of the A/B rig (and so of the GKE round collector, which shares their
# SQL through hack/lib/dbstats.sh) on a real Kubernetes cluster, WITHOUT the platform:
#
#   hack/perf-ab/db-rows-rig.sh --kubeconfig <lane-private kubeconfig> --out DIR [--setup] [--seconds 90]
#
# It needs a cluster with the CloudNativePG operator and ~3 CPUs. With --setup it creates two
# two-instance CNPG clusters shaped like the platform's (dc-tsdb in --ns holding "event-management".events,
# dc-rdb in --rdb-ns holding "device-state".device_states), preloads pg_stat_statements, and creates the
# two tables. Without it, it expects them already there.
#
# THEN, per window, it runs the REAL snapshot code (dbstats_snap_dbs: the very function perf-ab.sh
# calls) before and after a synthetic write load, reads cAdvisor the way perf-ab.sh does, and runs the
# REAL summarizer (perf_ab.py summarize) on the result. Two windows:
#
#   hot       updates change only unindexed columns on a table with spare page space: the HOT-update
#             ratio must read HIGH (>= 0.6), dead tuples and autovacuum runs must be reported, an
#             explicit CHECKPOINT inside the window must read as a REQUESTED checkpoint, and the postgres
#             container of the primary AND of the standby must each show CPU.
#   hostile   NEGATIVE CONTROL: every update changes an INDEXED column, so none can be HOT. The ratio
#             must read LOW (<= 0.1), and autovacuum must have run to clean the dead tuples they leave. A ratio
#             that read high here would be a metric that cannot fail.
#
# It exits 0 only if every expectation held, and leaves the raw snapshots and both summary.json files in --out.
# The load is synthetic (pgbench inside each primary); what is real is every query, file and row the
# collector produces. Delete the clusters with: kubectl delete cluster dc-tsdb -n <ns>; kubectl delete cluster dc-rdb -n <rdb-ns>.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
KUBECTL="${KUBECTL:-kubectl}"
kubeconfig="" out="" ns="dci" rdb_ns="dc-system" seconds=90 setup=0
tsdb_cluster="dc-tsdb" rdb_cluster="dc-rdb"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --kubeconfig) kubeconfig="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    --ns) ns="$2"; shift 2 ;;
    --rdb-ns) rdb_ns="$2"; shift 2 ;;
    --seconds) seconds="$2"; shift 2 ;;
    --setup) setup=1; shift ;;
    -h|--help) sed -n '4,28p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "db-rows-rig: unknown argument: $1" >&2; exit 2 ;;
  esac
done
[[ -n "$kubeconfig" && -n "$out" ]] || { echo "db-rows-rig: --kubeconfig and --out are required" >&2; exit 2; }
say() { echo "db-rows-rig: $*" >&2; }
mkdir -p "$out"

k() { env -u KUBECONFIG MSYS2_ARG_CONV_EXCL='*' "$KUBECTL" --kubeconfig "$kubeconfig" "$@"; }
psql_at() { k exec -n "$1" "$2" -c postgres -- psql -X -At -v ON_ERROR_STOP=1 -d postgres -c "$3"; }
psql_db() { k exec -n "$1" "$2" -c postgres -- psql -X -At -v ON_ERROR_STOP=1 -d "$3" -c "$4"; }
# shellcheck source=../lib/dbstats.sh
source "$ROOT/hack/lib/dbstats.sh"

primary_of() { # ns cluster
  k get pods -n "$1" -l "cnpg.io/cluster=$2,cnpg.io/instanceRole=primary" -o json | jq -r '.items[] | select(.status.phase == "Running") | .metadata.name' | tr -d '\r' | sed -n '1p'
}

cluster_yaml() { # ns name instances
  cat <<YAML
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata: {name: $2, namespace: $1}
spec:
  instances: $3
  imageName: ghcr.io/cloudnative-pg/postgresql:17.11-standard-bookworm
  storage: {size: 2Gi}
  bootstrap: {initdb: {database: app, owner: app}}
  resources: {requests: {cpu: 200m, memory: 256Mi}, limits: {cpu: "1", memory: 1Gi}}
  postgresql:
    shared_preload_libraries: [pg_stat_statements]
    parameters:
      pg_stat_statements.track: all
      max_wal_size: 256MB
      checkpoint_timeout: 30min
      autovacuum_naptime: 5s
YAML
}

if [[ "$setup" -eq 1 ]]; then
  say "creating the two CNPG clusters"
  for n in "$ns" "$rdb_ns"; do k create namespace "$n" --dry-run=client -o yaml | k apply -f - >/dev/null; done
  cluster_yaml "$ns" "$tsdb_cluster" 2 | k apply -f - >/dev/null
  cluster_yaml "$rdb_ns" "$rdb_cluster" 2 | k apply -f - >/dev/null
  k wait cluster/"$tsdb_cluster" -n "$ns" --for=condition=Ready --timeout=600s >/dev/null
  k wait cluster/"$rdb_cluster" -n "$rdb_ns" --for=condition=Ready --timeout=600s >/dev/null
fi

tsdb_pod="$(primary_of "$ns" "$tsdb_cluster")"; rdb_pod="$(primary_of "$rdb_ns" "$rdb_cluster")"
[[ -n "$tsdb_pod" && -n "$rdb_pod" ]] || { say "no primary for $tsdb_cluster or $rdb_cluster (use --setup?)"; exit 2; }
say "event store primary $tsdb_pod ($ns), relational primary $rdb_pod ($rdb_ns)"

if [[ "$setup" -eq 1 ]]; then
  psql_db "$ns" "$tsdb_pod" app 'CREATE SCHEMA IF NOT EXISTS "event-management";
    CREATE TABLE IF NOT EXISTS "event-management".events (tenant_id text NOT NULL, event_id bytea NOT NULL, device_token text NOT NULL, event_type int NOT NULL,
      occurred_time timestamptz NOT NULL, processed_time timestamptz NOT NULL, PRIMARY KEY (tenant_id, occurred_time, event_id))' >/dev/null
  # fillfactor 50 leaves the page room a HOT update needs; the unique (tenant, token) index and the tag
  # index are the indexes a non-HOT update must also write. Aggressive autovacuum so a window sees it run.
  psql_db "$rdb_ns" "$rdb_pod" app 'CREATE SCHEMA IF NOT EXISTS "device-state";
    CREATE TABLE IF NOT EXISTS "device-state".device_states (tenant_id text NOT NULL, device_token text NOT NULL, seen timestamptz, cursor bigint, tag bigint,
      UNIQUE (tenant_id, device_token)) WITH (fillfactor = 50, autovacuum_vacuum_scale_factor = 0, autovacuum_vacuum_threshold = 300,
      autovacuum_analyze_scale_factor = 0, autovacuum_analyze_threshold = 300);
    CREATE INDEX IF NOT EXISTS ds_tag ON "device-state".device_states (tag);
    INSERT INTO "device-state".device_states SELECT $$t$$, $$d$$ || g, now(), 0, g FROM generate_series(1, 1000) g ON CONFLICT DO NOTHING' >/dev/null
fi
for pair in "$ns:$tsdb_pod" "$rdb_ns:$rdb_pod"; do
  psql_at "${pair%%:*}" "${pair#*:}" "CREATE EXTENSION IF NOT EXISTS pg_stat_statements" >/dev/null
done
dbstats_init

# the nodes, for cAdvisor
mapfile -t node_names < <(k get nodes -o json | jq -r '.items[].metadata.name' | tr -d '\r')

snap() { # dir
  local d="$1" n
  mkdir -p "$d"
  date -u +%s > "$d/start.epoch"
  dbstats_snap_dbs "$d"
  for n in "${node_names[@]}"; do
    k get --raw "/api/v1/nodes/$n/proxy/metrics/cadvisor" | grep -E '^container_cpu_(usage_seconds_total|cfs_throttled_seconds_total)\{' > "$d/cadvisor-$n.prom" || true
  done
  date -u +%s > "$d/end.epoch"
  echo '[]' > "$d/pods.json"
}

put() { # ns pod path  (stdin -> file in the pod)
  k exec -i -n "$1" "$2" -c postgres -- sh -c "cat > $3"
}

window() { # name rdb-script-body
  local name="$1" rdb_sql="$2" run="$out/$1"
  rm -rf "$run"; mkdir -p "$run/raw"
  printf '%s\n' \
    '\set d random(1, 1000)' \
    "INSERT INTO \"event-management\".events (tenant_id, event_id, device_token, event_type, occurred_time, processed_time) VALUES ('t', decode(md5(random()::text || clock_timestamp()::text), 'hex'), 'd' || :d, 2, clock_timestamp(), clock_timestamp());" \
    | put "$ns" "$tsdb_pod" /run/rig-events.sql
  printf '%s\n' '\set d random(1, 1000)' "$rdb_sql" | put "$rdb_ns" "$rdb_pod" /run/rig-updates.sql
  say "window $name: ${seconds}s of load"
  snap "$run/raw/pre"
  k exec -n "$ns" "$tsdb_pod" -c postgres -- pgbench -n -h /controller/run -U postgres -d app -f /run/rig-events.sql -T "$seconds" -c 2 -j 1 -R 400 >"$run/pgbench-events.log" 2>&1 &
  local p1=$!
  k exec -n "$rdb_ns" "$rdb_pod" -c postgres -- pgbench -n -h /controller/run -U postgres -d app -f /run/rig-updates.sql -T "$seconds" -c 2 -j 1 -R 300 >"$run/pgbench-updates.log" 2>&1 &
  local p2=$!
  # an explicit CHECKPOINT a third of the way in: it must read as REQUESTED, not timed
  sleep $((seconds / 3))
  psql_at "$ns" "$tsdb_pod" "CHECKPOINT" >/dev/null
  wait "$p1" "$p2"
  snap "$run/raw/post"
  jq -n --arg arm "$name" --arg tsdb "$tsdb_pod" --arg rdb "$rdb_pod" '{arm:$arm, eventStorePrimary:$tsdb, relationalPrimary:$rdb, offered:0, offeredMqtt:0, offeredHttp:0}' > "$run/meta.json"
  python3 "$ROOT/hack/perf-ab/perf_ab.py" summarize --dir "$run" >"$run/summarize.out" 2>&1 || true
  [[ -s "$run/summary.json" ]] || { say "no summary.json for $name:"; cat "$run/summarize.out" >&2; return 1; }
}

rc=0
window hot "UPDATE \"device-state\".device_states SET seen = clock_timestamp(), cursor = cursor + 1 WHERE tenant_id = 't' AND device_token = 'd' || :d;" || rc=1
window hostile "UPDATE \"device-state\".device_states SET tag = tag + 1000000 WHERE tenant_id = 't' AND device_token = 'd' || :d;" || rc=1
[[ "$rc" -eq 0 ]] || exit 1

rows='to_entries[] | select(.key | test("hot_ratio|dead_tuples|autovacuum_runs|autoanalyze_runs|checkpoints_|cpu\\.dbinst|wal_bytes_per_event|wal_records_per_event|tuple_updates")) | "\(.key)\t\(.value)"'
for w in hot hostile; do
  echo "=== window $w ==="
  jq -r ".metrics | $rows" "$out/$w/summary.json" | sort
  jq -r '"checkpoint cause: \(.info.checkpoint_cause)", "db instance cores: \(.info.db_instance_cores)"' "$out/$w/summary.json"
done

# --- the expectations
fail=0
expect() { # description jq-expression-over-summary file
  if jq -e "$3" "$out/$2/summary.json" >/dev/null; then say "ok   [$2] $1"; else say "FAIL [$2] $1"; fail=1; fi
}
expect "relational WAL is reported per event" hot '.metrics["db.rdb.wal_records_per_event"] > 0'
expect "HOT ratio is HIGH when updates touch no indexed column (>= 0.6)" hot '.metrics["db.rdb.hot_ratio"] >= 0.6'
expect "dead tuples are reported" hot '.metrics["db.rdb.dead_tuples"] != null'
expect "autovacuum runs are reported" hot '.metrics["db.rdb.autovacuum_runs"] != null'
expect "NEGATIVE CONTROL: autovacuum RAN when non-HOT updates left dead tuples behind (HOT updates are pruned in-page and need none)" hostile '.metrics["db.rdb.autovacuum_runs"] >= 1'
expect "the explicit CHECKPOINT reads as a requested checkpoint on the event store" hot '.metrics["db.tsdb.checkpoints_requested"] >= 1 and (.info.checkpoint_cause.tsdb | test("requested"))'
expect "the primary and the standby each show database CPU (event store)" hot '.metrics["cpu.dbinst.tsdb.primary.s_per_1k_events"] > 0 and .metrics["cpu.dbinst.tsdb.standby.s_per_1k_events"] > 0'
expect "the primary and the standby each show database CPU (relational)" hot '.metrics["cpu.dbinst.rdb.primary.s_per_1k_events"] > 0 and .metrics["cpu.dbinst.rdb.standby.s_per_1k_events"] > 0'
expect "NEGATIVE CONTROL: HOT ratio is LOW when every update changes an indexed column (<= 0.1)" hostile '.metrics["db.rdb.hot_ratio"] <= 0.1'
expect "NEGATIVE CONTROL: the two windows disagree on the HOT ratio by > 0.5" hostile '.metrics["db.rdb.hot_ratio"] != null'
hot="$(jq '.metrics["db.rdb.hot_ratio"]' "$out/hot/summary.json")"; hostile="$(jq '.metrics["db.rdb.hot_ratio"]' "$out/hostile/summary.json")"
awk -v a="$hot" -v b="$hostile" 'BEGIN { exit !(a - b > 0.5) }' && say "ok   the HOT ratio moved from $hostile (hostile) to $hot (hot)" || { say "FAIL the HOT ratio did not separate the windows ($hostile vs $hot)"; fail=1; }
exit "$fail"
