#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Database write-behaviour snapshots shared by hack/perf-ab.sh (the kind A/B rig) and
# hack/perf-collect.sh (the GKE round collector). SOURCE it; it defines functions only.
#
#   dbstats_tables   <psql> <ns> <pod>        per-table counters from pg_stat_user_tables, as ONE JSON object
#                                             {"<database>": [{t,i,u,h,d,live,dead,av,aa,vc}, ...]}
#                                             t = "schema/table"; i/u/h/d = inserts, updates, HOT updates,
#                                             deletes; live/dead = tuples; av/aa/vc = autovacuum, autoanalyze,
#                                             manual vacuum runs. i/u/h/d/av/aa/vc are cumulative counters
#                                             (the analysis subtracts two snapshots), live/dead are gauges.
#   dbstats_instances <kubectl> <ns> <cluster> <store>
#                                             the CNPG pods of one cluster with the role each holds, as a JSON
#                                             array [{n,ns,cluster,store,role}], role primary|replica
#
# <psql> is a function taking: namespace pod database sql, printing psql -At output.
# <kubectl> is the script's own kubectl wrapper (it pins the kubeconfig).
#
# Timescale hypertables are folded: every chunk is counted under its hypertable ("schema/table"), so a
# window that crosses a chunk boundary compares like with like. Chunk tables of the compression and
# the timescaledb catalog are left out; they hold no application rows.
#
# Counters in pg_stat_user_tables are flushed by each backend when it goes idle, so a snapshot can lag
# the true value by about a second; over a window of minutes that is noise, over a window of seconds it
# is not -- do not use these for windows shorter than a minute.

DBSTATS_COLS="sum(t.n_tup_ins)::bigint AS i, sum(t.n_tup_upd)::bigint AS u, sum(t.n_tup_hot_upd)::bigint AS h, sum(t.n_tup_del)::bigint AS d, sum(t.n_live_tup)::bigint AS live, sum(t.n_dead_tup)::bigint AS dead, sum(t.autovacuum_count)::bigint AS av, sum(t.autoanalyze_count)::bigint AS aa, sum(t.vacuum_count)::bigint AS vc"
DBSTATS_SQL_PLAIN="SELECT coalesce(jsonb_agg(to_jsonb(x)), '[]'::jsonb) FROM (SELECT t.schemaname || '/' || t.relname AS t, $DBSTATS_COLS FROM pg_stat_user_tables t GROUP BY 1) x"
DBSTATS_SQL_TIMESCALE="SELECT coalesce(jsonb_agg(to_jsonb(x)), '[]'::jsonb) FROM (SELECT coalesce(c.hypertable_schema || '/' || c.hypertable_name, t.schemaname || '/' || t.relname) AS t, $DBSTATS_COLS FROM pg_stat_user_tables t LEFT JOIN timescaledb_information.chunks c ON c.chunk_schema = t.schemaname AND c.chunk_name = t.relname WHERE c.chunk_name IS NOT NULL OR left(t.schemaname, 12) <> '_timescaledb' GROUP BY 1) x"

# jq program: delta of two per-table documents ({database: [{t,i,u,h,d,live,dead,av,aa,vc}]}); invalid on a counter going backwards
DBSTATS_TABLES_DELTA='
  def flat: to_entries | map(.key as $db | .value[] | . + {db: $db});
  ($a | flat | map({key: (.db + "|" + .t), value: .}) | from_entries) as $A
  | ($b | flat | map(. as $r | ($A[$r.db + "|" + $r.t] // {}) as $o
      | def d($f): (($r[$f] // 0) - ($o[$f] // 0));
        {db: $r.db, table: $r.t, inserts: d("i"), updates: d("u"), hot_updates: d("h"), deletes: d("d"),
         autovacuum_runs: d("av"), autoanalyze_runs: d("aa"), vacuum_runs: d("vc"),
         dead_tuples: ($r.dead // 0), live_tuples: ($r.live // 0)})) as $d
  | if ($d | any(.inserts < 0 or .updates < 0 or .hot_updates < 0 or .deletes < 0 or .autovacuum_runs < 0 or .autoanalyze_runs < 0)) then
      {valid: false, reason: "a table counter went backwards: the server restarted or its statistics were reset"}
    else {valid: true,
          tables: ($d | map(. + {hot_ratio: (if .updates > 0 then (.hot_updates / .updates) else null end)}) | sort_by(-(.updates + .inserts)))} end'

dbstats_tables() { # psql ns pod
  local psql="$1" ns="$2" pod="$3" dbs db has sql arr out='{}'
  dbs="$("$psql" "$ns" "$pod" postgres "SELECT datname FROM pg_database WHERE NOT datistemplate AND datname <> 'postgres' ORDER BY 1" | tr -d '\r')" || return 1
  for db in $dbs; do
    has="$("$psql" "$ns" "$pod" "$db" "SELECT to_regclass('timescaledb_information.chunks') IS NOT NULL" | tr -d '\r')" || return 1
    if [[ "$has" == t ]]; then sql="$DBSTATS_SQL_TIMESCALE"; else sql="$DBSTATS_SQL_PLAIN"; fi
    arr="$("$psql" "$ns" "$pod" "$db" "$sql" | tr -d '\r')" || return 1
    out="$(jq -c --arg d "$db" --argjson a "$arr" '. + {($d): $a}' <<<"$out" | tr -d '\r')" || return 1
  done
  printf '%s\n' "$out"
}

dbstats_instances() { # kubectl ns cluster store
  local kubectl="$1" ns="$2" cluster="$3" store="$4"
  "$kubectl" get pods -n "$ns" -l "cnpg.io/cluster=$cluster" -o json \
    | jq -c --arg ns "$ns" --arg c "$cluster" --arg s "$store" \
        '[.items[] | select(.status.phase == "Running") | {n: .metadata.name, ns: $ns, cluster: $c, store: $s, role: (.metadata.labels["cnpg.io/instanceRole"] // "unknown")}]' \
    | tr -d '\r'
}

# dbstats_stmt_sql <ns> <pod>: prints the one statement that reads a store's WAL position, WAL counters,
# checkpoint counts (timed and requested apart: "requested" is max_wal_size or an explicit CHECKPOINT,
# "timed" is checkpoint_timeout, and they have different fixes) and pg_stat_statements, all at one
# instant. The checkpoint counters moved to pg_stat_checkpointer in PG17, and the two stores can run
# different majors, so the view is probed on THIS store, never assumed from the other one.
# A statement is identified by (database, queryid, userid, toplevel): two users, or a top-level and a
# nested execution, of one query are different entries.
# Needs: psql_at.
dbstats_stmt_sql() {
  local ns="$1" pod="$2" view t_expr r_expr
  view="$(psql_at "$ns" "$pod" "SELECT CASE WHEN to_regclass('pg_stat_checkpointer') IS NOT NULL THEN 'pg_stat_checkpointer' ELSE 'pg_stat_bgwriter' END" | tr -d '\r')" || return 1
  if [[ "$view" == pg_stat_checkpointer ]]; then
    t_expr="(SELECT num_timed FROM pg_stat_checkpointer)"; r_expr="(SELECT num_requested FROM pg_stat_checkpointer)"
  else
    t_expr="(SELECT checkpoints_timed FROM pg_stat_bgwriter)"; r_expr="(SELECT checkpoints_req FROM pg_stat_bgwriter)"
  fi
  printf '%s' "SELECT jsonb_build_object('t', extract(epoch from clock_timestamp()), 'ckpt', ($t_expr + $r_expr), 'ckptTimed', $t_expr, 'ckptReq', $r_expr, 'lsn', pg_wal_lsn_diff(pg_current_wal_lsn(), '0/0'), 'lsnText', pg_current_wal_lsn()::text, 'inRecovery', pg_is_in_recovery(), 'wal', (SELECT to_jsonb(w) - 'stats_reset' FROM pg_stat_wal w), 'reset', (SELECT stats_reset FROM pg_stat_statements_info), 'dealloc', (SELECT dealloc FROM pg_stat_statements_info), 'stmts', coalesce((SELECT jsonb_agg(jsonb_build_object('q', left(s.query, 300), 'id', s.queryid::text, 'uid', s.userid::text, 'top', s.toplevel, 'db', d.datname, 'calls', s.calls, 'rows', s.rows, 'ms', s.total_exec_time, 'walb', s.wal_bytes, 'walr', s.wal_records)) FROM pg_stat_statements s LEFT JOIN pg_database d ON d.oid = s.dbid WHERE s.calls > 0), '[]'::jsonb))"
}

# dbstats_init: builds STMT_SQL_TSDB and STMT_SQL_RDB, one per store. Needs: psql_at, ns, tsdb_pod, rdb_ns, rdb_pod.
dbstats_init() {
  STMT_SQL_TSDB="$(dbstats_stmt_sql "$ns" "$tsdb_pod")" || return 1
  STMT_SQL_RDB="$(dbstats_stmt_sql "$rdb_ns" "$rdb_pod")" || return 1
}

# dbstats_snap_dbs <dir>: writes db-tsdb.json, db-rdb.json (the statement snapshots), tables-tsdb.json,
# tables-rdb.json (per-table counters) and db-instances.json (every CNPG pod with its role) into <dir>,
# in parallel so the stores are read within a second of one another. Returns non-zero if any failed.
# Needs: psql_at, psql_db, k, STMT_SQL_TSDB, STMT_SQL_RDB, ns, tsdb_pod, tsdb_cluster, rdb_ns, rdb_pod, rdb_cluster.
dbstats_snap_dbs() {
  local d="$1" pid rc=0
  local -a pids=()
  ( psql_at "$ns" "$tsdb_pod" "$STMT_SQL_TSDB" > "$d/db-tsdb.json" ) & pids+=($!)
  ( psql_at "$rdb_ns" "$rdb_pod" "$STMT_SQL_RDB" > "$d/db-rdb.json" ) & pids+=($!)
  ( dbstats_tables psql_db "$ns" "$tsdb_pod" > "$d/tables-tsdb.json" ) & pids+=($!)
  ( dbstats_tables psql_db "$rdb_ns" "$rdb_pod" > "$d/tables-rdb.json" ) & pids+=($!)
  ( { dbstats_instances k "$ns" "$tsdb_cluster" tsdb; dbstats_instances k "$rdb_ns" "$rdb_cluster" rdb; } | jq -sc 'add' > "$d/db-instances.json" ) & pids+=($!)
  for pid in "${pids[@]}"; do wait "$pid" || rc=1; done
  return "$rc"
}
