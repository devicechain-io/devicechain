#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# One-command profile collection for a performance round, ONE PHASE at a time.
#
#   hack/perf-collect.sh --kubeconfig <lane-private kubeconfig> --namespace <instance ns> \
#       --round r25 --phase R1 --duration 60 --marker <steady marker> [--out DIR] [--dry-run]
#
#   hack/perf-collect.sh --self-test     # proves the refusals and failures against stub tools
#
# WHAT IT COLLECTS, all inside the harness's steady window (see the marker below):
#
#   every pod of every service   cpu profile(s) of --duration seconds (--cpu-profiles K of them,
#                                back to back), heap, allocs, mutex, block      (.pb.gz)
#   every NATS pod               cpu, heap, allocs (+ block when prof_block_rate is set) (.pb.gz);
#                                /varz, /jsz?streams=1&consumers=1, /connz before and after (.json)
#   each database primary        pg_stat_statements before/after + their delta, the delta against
#                                the previous phase, pg_stat_bgwriter/checkpointer/WAL and the
#                                per-hypertable rows inserted, before/after + delta       (.json);
#                                per-table counters (inserts, updates, HOT updates, dead tuples,
#                                autovacuum runs), before/after + delta                   (.json)
#   the manifest also records     the role (primary or replica) every database pod held before and
#                                after the window, so the analysis can split DB CPU by instance
#   the nodes                    cAdvisor container CPU/memory series before/after (.prom): the
#                                exact CPU-seconds counters the analysis divides by events
#   the namespace                metrics-API pod and node usage before/after (.json)
#   a manifest                   <round>-<phase>-manifest.json: what, when, from which pod and
#                                image digest, with a sha256 per file and every failure
#
# Output names: <round>-<phase>-<component>-<pod>-<kind>.<ext>
#
# 🔴 THE STEADY MARKER IS REQUIRED. The load harnesses (dc-loadtest, dc-loadtest-mqtt) write it
# with --steady-marker/--steady-phase/--steady-warmup: warmup, then steady, then ended/aborted. This
# script REFUSES (exit 3) unless the marker says steady, for this phase, with enough window left for
# the whole collection, and it re-reads the marker afterwards: a window that closed or restarted
# mid-collection discards everything, because a profile taken across a ramp or a stall is worth
# less than none. --no-steady-check overrides, and the manifest then says steadyChecked=false.
#
# 🔴 NOTHING IS QUIETLY PARTIAL. A component with no pods, a service with fewer ready pods than
# its Deployment wants, a profiler that is not on, a profile that answers anything but 200, a pod
# that restarted or was replaced during the window: each is a FAILURE named on the screen and in the
# manifest ("complete": false), and the exit status is 1. Whatever WAS collected stays, marked.
#
# SAFETY. Only the kubeconfig named is used (KUBECONFIG is cleared, and the context is never
# switched); every profile endpoint is reached by `kubectl port-forward` to a pod, never through a
# Service, and nothing is written to the cluster except `CREATE EXTENSION IF NOT EXISTS
# pg_stat_statements` on the database primaries. The switches that turn the endpoints on are in
# hack/README-perf.md (a heading in deploy/helm/devicechain/README.md for the services).
#
# Requires: kubectl, curl, jq, sha256sum (or shasum). KUBECTL / CURL / JQ override the binaries.
#
# Exit: 0 collected; 1 collected with failures (or a hard error); 2 bad usage; 3 refused (not steady).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SELF="$ROOT/hack/$(basename "${BASH_SOURCE[0]}")"
# shellcheck source=lib/dbstats.sh
source "$ROOT/hack/lib/dbstats.sh"
KUBECTL="${KUBECTL:-kubectl}"
CURL="${CURL:-curl}"
jq_real="${JQ:-jq}"
# jq on Windows writes CRLF, which turns every value read into a shell variable into "value\r".
# Stripping it here is safe everywhere (a CR inside a JSON string is escaped, never raw), and
# pipefail keeps jq's own exit status for `jq -e`.
_jq() { "$jq_real" "$@" | tr -d '\r'; }
JQ=_jq

usage() { sed -n '4,9p' "$SELF" | sed 's/^# \{0,1\}//'; exit 2; }
say() { echo "perf-collect: $*" >&2; }
die() { say "$*"; exit 1; }
usage_err() { say "$*"; exit 2; }

# ---------------------------------------------------------------------------
# Arguments
# ---------------------------------------------------------------------------
kubeconfig="" ns="" round="" phase="" duration="" out="" marker="" kctx=""
cpu_profiles=1 steady_check=1 dry_run=0 skip_nats=0 skip_db=0
nats_ns="" nats_prof_port="" rdb_cluster="dc-rdb" tsdb_cluster="dc-tsdb"
rdb_ns="" psql_ns=""
tsdb_database="" services_filter=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --self-test) self_test_requested=1; shift ;;
    --kubeconfig) kubeconfig="$2"; shift 2 ;;
    --context) kctx="$2"; shift 2 ;;
    --namespace|-n) ns="$2"; shift 2 ;;
    --round) round="$2"; shift 2 ;;
    --phase) phase="$2"; shift 2 ;;
    --duration) duration="$2"; shift 2 ;;
    --cpu-profiles) cpu_profiles="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    --marker) marker="$2"; shift 2 ;;
    --no-steady-check) steady_check=0; shift ;;
    --services) services_filter="$2"; shift 2 ;;
    --skip-nats) skip_nats=1; shift ;;
    --skip-db) skip_db=1; shift ;;
    --nats-namespace) nats_ns="$2"; shift 2 ;;
    --rdb-namespace) rdb_ns="$2"; shift 2 ;;
    --nats-prof-port) nats_prof_port="$2"; shift 2 ;;
    --rdb-cluster) rdb_cluster="$2"; shift 2 ;;
    --tsdb-cluster) tsdb_cluster="$2"; shift 2 ;;
    --tsdb-database) tsdb_database="$2"; shift 2 ;;
    --dry-run) dry_run=1; shift ;;
    -h|--help) usage ;;
    *) say "unknown argument: $1"; usage ;;
  esac
done

# ---------------------------------------------------------------------------
# Small helpers
# ---------------------------------------------------------------------------
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
now_utc() { date -u +%Y-%m-%dT%H:%M:%SZ; }
epoch_of() { # RFC3339 -> epoch seconds (fractions dropped); GNU and BSD date both handled
  local t="${1%%.*}"; t="${t%Z}"
  date -u -d "${t}Z" +%s 2>/dev/null || date -u -j -f %Y-%m-%dT%H:%M:%S "$t" +%s
}

# to_seconds: "60", "60s", "2m" -> seconds
to_seconds() {
  local d="$1"
  [[ "$d" =~ ^([0-9]+)([sm]?)$ ]] || usage_err "unsupported duration '$1' (use 60, 60s or 2m)"
  case "${BASH_REMATCH[2]}" in m) echo $((BASH_REMATCH[1] * 60)) ;; *) echo "${BASH_REMATCH[1]}" ;; esac
}

kargs=()
k() { # kubectl bound to the lane-private kubeconfig, never the ambient one
  # MSYS2_ARG_CONV_EXCL: Git Bash on Windows rewrites an argument that starts with "/" into a
  # Windows path, so `get --raw /api/v1/nodes/N/proxy/metrics/cadvisor` reached the API server as
  # "C:/Program Files/Git/api/..." and every node read answered NotFound (r25). Other platforms
  # ignore the variable.
  env -u KUBECONFIG MSYS2_ARG_CONV_EXCL='/api;/apis' "$KUBECTL" "${kargs[@]}" "$@"
}

# ---------------------------------------------------------------------------
# Self-test: stub kubectl/curl, then prove each refusal and failure actually fires.
# Defined before use; it re-runs this script, so it only needs the argument parser above.
# ---------------------------------------------------------------------------
self_test() {
  command -v "$jq_real" >/dev/null 2>&1 || { say "self-test needs jq"; return 1; }
  local d rc=0
  d="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$d'" RETURN
  mkdir -p "$d/bin" "$d/fx"

  # --- fixtures: two services (one with a missing pod in the broken variant), a broker, two DBs
  cat >"$d/fx/pods.json" <<'JSON'
{"items":[
 {"metadata":{"name":"device-management-aaa","uid":"u1","labels":{"devicechain.io/functional-area":"device-management"}},
  "spec":{"nodeName":"n1","containers":[{"name":"svc","env":[{"name":"DC_PROFILER_ADDRESS","value":"127.0.0.1:6060"}]}]},
  "status":{"phase":"Running","containerStatuses":[{"name":"svc","ready":true,"restartCount":0,"image":"img/dm:1","imageID":"img/dm@sha256:aa"}]}},
 {"metadata":{"name":"device-state-bbb","uid":"u2","labels":{"devicechain.io/functional-area":"device-state"}},
  "spec":{"nodeName":"n2","containers":[{"name":"svc","env":[{"name":"DC_PROFILER_ADDRESS","value":"127.0.0.1:6060"}]}]},
  "status":{"phase":"Running","containerStatuses":[{"name":"svc","ready":true,"restartCount":0,"image":"img/ds:1","imageID":"img/ds@sha256:bb"}]}},
 {"metadata":{"name":"frontend-ccc","uid":"u6","labels":{"devicechain.io/functional-area":"frontend"}},
  "spec":{"nodeName":"n1","containers":[{"name":"frontend"}]},
  "status":{"phase":"Running","containerStatuses":[{"name":"frontend","ready":true,"restartCount":0,"image":"img/fe:1","imageID":"img/fe@sha256:ee"}]}},
 {"metadata":{"name":"dc-nats-0","uid":"u3","labels":{"app.kubernetes.io/name":"nats","app.kubernetes.io/component":"nats"}},
  "spec":{"nodeName":"n1","containers":[{"name":"nats"}]},
  "status":{"phase":"Running","containerStatuses":[{"name":"nats","ready":true,"restartCount":0,"image":"img/nats:1","imageID":"img/nats@sha256:cc"}]}},
 {"metadata":{"name":"dc-rdb-1","uid":"u4","labels":{"cnpg.io/cluster":"dc-rdb","cnpg.io/instanceRole":"primary"}},
  "spec":{"nodeName":"n2","containers":[{"name":"postgres"}]},
  "status":{"phase":"Running","containerStatuses":[{"name":"postgres","ready":true,"restartCount":0,"image":"img/pg:1","imageID":"img/pg@sha256:dd"}]}},
 {"metadata":{"name":"dc-tsdb-1","uid":"u5","labels":{"cnpg.io/cluster":"dc-tsdb","cnpg.io/instanceRole":"primary"}},
  "spec":{"nodeName":"n2","containers":[{"name":"postgres"}]},
  "status":{"phase":"Running","containerStatuses":[{"name":"postgres","ready":true,"restartCount":0,"image":"img/pg:1","imageID":"img/pg@sha256:dd"}]}}
]}
JSON
  cat >"$d/fx/deploys.json" <<'JSON'
{"items":[
 {"metadata":{"name":"device-management","labels":{"devicechain.io/functional-area":"device-management"}},"spec":{"replicas":1}},
 {"metadata":{"name":"device-state","labels":{"devicechain.io/functional-area":"device-state"}},"spec":{"replicas":1}},
 {"metadata":{"name":"frontend","labels":{"devicechain.io/functional-area":"frontend"}},"spec":{"replicas":1}}
]}
JSON
  jq '.items[1].spec.replicas = 2' "$d/fx/deploys.json" >"$d/fx/deploys-short.json"
  cat >"$d/fx/natsconf.json" <<'JSON'
{"items":[{"data":{"nats.conf":"{\n  \"server_name\": \"x\",\n  \"prof_port\": 6061,\n  \"prof_block_rate\": 100000\n}\n"}}]}
JSON

  # --- stub kubectl: answers only the calls this script makes, from fixtures.
  cat >"$d/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
# args after --kubeconfig <f> [--context c]
args=("$@")
while [[ "${args[0]:-}" == --kubeconfig || "${args[0]:-}" == --context ]]; do args=("${args[@]:2}"); done
echo "${args[*]}" >>"$STUB_DIR/kubectl.log"
case "${args[0]}" in
  get)
    case "${args[*]}" in
      *"get pods"*"-n nsdb"*) echo '{"items":[]}' ;;   # a second namespace that holds no pods of its own
      *"get pods"*)
        echo x >>"$STUB_DIR/podreads"
        if [[ -n "${STUB_PODS_AFTER:-}" && "$(wc -l <"$STUB_DIR/podreads")" -ge 2 ]]; then cat "$STUB_DIR/fx/$STUB_PODS_AFTER"; else cat "$STUB_DIR/fx/${STUB_PODS:-pods.json}"; fi ;;
      *"get deploy"*) cat "$STUB_DIR/fx/${STUB_DEPLOYS:-deploys.json}" ;;
      *"get configmap"*) cat "$STUB_DIR/fx/natsconf.json" ;;
      *"--raw"*proxy/metrics/cadvisor*) printf 'container_cpu_usage_seconds_total{namespace="ns1",pod="device-management-aaa",container="svc"} %s\n' "${STUB_CPU:-10}" ;;
      *"--raw"*metrics.k8s.io*) echo '{"items":[]}' ;;
      *) echo "stub kubectl: unhandled get: ${args[*]}" >&2; exit 9 ;;
    esac ;;
  exec)
    if [[ "${args[*]}" == *"CREATE EXTENSION"* ]]; then echo CREATE
    elif [[ "${args[*]}" == *"to_regclass"* ]]; then echo t
    elif [[ "${args[*]}" == *"pg_stat_statements_info"* ]]; then
      if [[ -n "${STUB_PGSS_FAIL:-}" ]]; then echo 'ERROR: pg_stat_statements must be loaded via shared_preload_libraries' >&2; exit 1; fi
      echo '{"takenAt":"t","statsReset":"r","dealloc":0,"statements":[{"key":"1/2/3/t","db":"d","queryid":"3","query":"select 1","calls":5,"total_exec_time":10.5,"rows":5}]}'
    elif [[ "${args[*]}" == *"pg_database"* ]]; then echo "appdb"
    elif [[ "${args[*]}" == *"pg_stat_bgwriter"* ]]; then echo '{"takenAt":"t","bgwriter":{"buffers_clean":1},"wal":{"wal_bytes":100},"hypertables":{"events":10}}'
    # after the bgwriter branch: the event store's sysstat query reads pg_stat_user_tables too
    elif [[ "${args[*]}" == *"pg_stat_user_tables"* ]]; then echo '[{"t":"device-state/device_states","i":10,"u":2000,"h":1500,"d":0,"live":100,"dead":7,"av":3,"aa":1,"vc":0}]'
    else echo "stub kubectl: unhandled exec: ${args[*]}" >&2; exit 9; fi ;;
  port-forward)
    echo "Forwarding from 127.0.0.1:${STUB_PORT:-43210} -> 6060"; exec sleep 600 ;;
  *) echo "stub kubectl: unhandled: ${args[*]}" >&2; exit 9 ;;
esac
STUB
  # --- stub curl: writes the -o file; answers 404 for mutex when STUB_NO_MUTEX is set.
  cat >"$d/bin/curl" <<'STUB'
#!/usr/bin/env bash
outf="" url=""
while [[ $# -gt 0 ]]; do case "$1" in -o) outf="$2"; shift 2 ;; -w|--max-time) shift 2 ;; -*) shift ;; *) url="$1"; shift ;; esac; done
code=200
[[ -n "${STUB_NO_MUTEX:-}" && "$url" == */mutex* ]] && code=404
if [[ "$url" == */varz* || "$url" == */jsz* || "$url" == */connz* ]]; then echo '{"ok":true}' >"$outf"; else printf 'PROFILE:%s\n' "$url" >"$outf"; fi
[[ "$code" == 200 ]] || echo "the mutex profile is not collected" >"$outf"
printf '%s' "$code"
STUB
  chmod +x "$d/bin/kubectl" "$d/bin/curl"

  now="$(now_utc)"
  later="$(date -u -d '@'"$(( $(date -u +%s) + 3600 ))" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v+1H +%Y-%m-%dT%H:%M:%SZ)"
  steady_marker() { # state phase windowEnds
    printf '{"schema":1,"phase":"%s","state":"%s","since":"%s","startedAt":"%s","windowEnds":"%s","pid":1}\n' "$2" "$1" "$now" "$now" "$3" >"$d/marker.json"
  }
  run() { # expected-status, then STUB_X=y environment words and/or extra arguments
    local want="$1"; shift
    local code=0 envs=() argv=() a
    for a in "$@"; do if [[ "$a" == STUB_* ]]; then envs+=("$a"); else argv+=("$a"); fi; done
    out="$(env STUB_DIR="$d" KUBECTL="$d/bin/kubectl" CURL="$d/bin/curl" ${envs[@]+"${envs[@]}"} \
      "$SELF" --kubeconfig "$d/kc" --namespace ns1 --round rT --phase R1 --duration 1 --out "$d/out" \
      --marker "$d/marker.json" --nats-prof-port 6061 ${argv[@]+"${argv[@]}"} 2>&1)" || code=$?
    if [[ "$code" -ne "$want" ]]; then
      echo "self-test: '$*' exited $code, want $want" >&2; echo "$out" >&2; rc=1
    fi
  }
  : >"$d/kc"

  # 1. a steady marker and a healthy cluster: collects, exit 0, files named per the contract.
  steady_marker steady R1 "$later"
  run 0
  for f in rT-R1-device-management-device-management-aaa-cpu1.pb.gz rT-R1-device-state-device-state-bbb-heap.pb.gz \
           rT-R1-nats-dc-nats-0-varz-pre.json rT-R1-nats-dc-nats-0-cpu1.pb.gz rT-R1-postgres-dc-rdb-1-pgss-delta.json \
           rT-R1-timescaledb-dc-tsdb-1-sysstat-delta.json rT-R1-node-n1-cadvisor-post.prom rT-R1-manifest.json; do
    [[ -s "$d/out/$f" ]] || { echo "self-test: expected output $f is missing or empty" >&2; rc=1; }
  done
  jq -e '.complete == true and (.artifacts | length) > 20 and .steady.checked == true and (.images | length) >= 5' "$d/out/rT-R1-manifest.json" >/dev/null \
    || { echo "self-test: the manifest of a healthy run does not say complete" >&2; rc=1; }
  # the table snapshots are collected for BOTH databases and the roles are in the manifest
  for f in rT-R1-postgres-dc-rdb-1-tables-pre.json rT-R1-postgres-dc-rdb-1-tables-post.json rT-R1-postgres-dc-rdb-1-tables-delta.json \
           rT-R1-timescaledb-dc-tsdb-1-tables-delta.json; do
    [[ -s "$d/out/$f" ]] || { echo "self-test: expected table output $f is missing or empty" >&2; rc=1; }
  done
  jq -e '.valid == true and (.tables | length) == 1' "$d/out/rT-R1-postgres-dc-rdb-1-tables-delta.json" >/dev/null \
    || { echo "self-test: the tables delta is not valid" >&2; rc=1; }
  jq -e '(.dbRolesBefore | map(select(.pod == "dc-rdb-1" and .role == "primary")) | length) == 1 and (.pods | map(select(.pod == "dc-rdb-1" and .role == "primary")) | length) == 1' \
    "$d/out/rT-R1-manifest.json" >/dev/null || { echo "self-test: the manifest does not record the database roles before and after" >&2; rc=1; }
  # the delta maths, on literal documents: 1,000 updates of which 750 HOT; and a counter that went backwards is INVALID
  local ta='{"app":[{"t":"s/t","i":0,"u":100,"h":50,"d":0,"live":5,"dead":1,"av":1,"aa":0,"vc":0}]}' tb='{"app":[{"t":"s/t","i":9,"u":1100,"h":800,"d":0,"live":5,"dead":40,"av":3,"aa":1,"vc":0}]}'
  jq -n --argjson a "$ta" --argjson b "$tb" '. as $_ | '"$DBSTATS_TABLES_DELTA" \
    | jq -e '.valid == true and .tables[0].updates == 1000 and .tables[0].hot_updates == 750 and .tables[0].hot_ratio == 0.75 and .tables[0].dead_tuples == 40 and .tables[0].autovacuum_runs == 2 and .tables[0].autoanalyze_runs == 1' >/dev/null \
    || { echo "self-test: the table delta arithmetic is wrong" >&2; rc=1; }
  jq -n --argjson a "$tb" --argjson b "$ta" '. as $_ | '"$DBSTATS_TABLES_DELTA" | jq -e '.valid == false' >/dev/null \
    || { echo "self-test: a table counter going backwards was not invalid" >&2; rc=1; }

  # 2. NOT STEADY (every state but steady, a wrong phase, no marker): refused, exit 3, nothing written.
  for st in warmup ended aborted; do
    rm -rf "$d/out"; steady_marker "$st" R1 "$later"
    run 3
    [[ ! -e "$d/out" ]] || { echo "self-test: a $st marker still wrote output" >&2; rc=1; }
  done
  rm -rf "$d/out"; steady_marker steady R9 "$later"; run 3
  rm -rf "$d/out"; rm -f "$d/marker.json"; run 3
  # a window that closes before the collection could finish
  steady_marker steady R1 "$now"; rm -rf "$d/out"; run 3
  # and the override is honoured, and recorded
  rm -rf "$d/out"; rm -f "$d/marker.json"; run 0 --no-steady-check
  jq -e '.steady.checked == false' "$d/out/rT-R1-manifest.json" >/dev/null \
    || { echo "self-test: --no-steady-check was not recorded in the manifest" >&2; rc=1; }

  # 3. A service with fewer pods than its Deployment wants is a FAILURE, not a smaller sample.
  steady_marker steady R1 "$later"; rm -rf "$d/out"
  run 1 STUB_DEPLOYS=deploys-short.json
  grep -q "device-state" <<<"$out" && grep -qi "1 of 2\|wants 2" <<<"$out" \
    || { echo "self-test: the missing pod was not named in the output" >&2; echo "$out" >&2; rc=1; }
  jq -e '.complete == false and (.failures | length) >= 1' "$d/out/rT-R1-manifest.json" >/dev/null \
    || { echo "self-test: the manifest of a short run says complete" >&2; rc=1; }

  # 4. A contention profile that answers 404 (sampling off) is a failure, never an empty file.
  rm -rf "$d/out"
  run 1 STUB_NO_MUTEX=1
  [[ ! -e "$d/out/rT-R1-device-management-device-management-aaa-mutex.pb.gz" ]] \
    || { echo "self-test: a 404 mutex profile left a file behind" >&2; rc=1; }
  jq -e '[.failures[] | select(.kind == "mutex")] | length >= 1' "$d/out/rT-R1-manifest.json" >/dev/null \
    || { echo "self-test: the 404 mutex profile was not a recorded failure" >&2; rc=1; }

  # 5. pg_stat_statements not preloaded: a loud failure naming the switch.
  rm -rf "$d/out"
  run 1 STUB_PGSS_FAIL=1
  grep -q "statement_stats\|statement-stats\|shared_preload_libraries" <<<"$out" \
    || { echo "self-test: the missing pg_stat_statements was not explained" >&2; rc=1; }

  # 6. A pod replaced during the window invalidates what was taken from it.
  rm -rf "$d/out"
  jq '.items[0].metadata.uid = "u1-new"' "$d/fx/pods.json" >"$d/fx/pods-after.json"
  : >"$d/podreads"
  out="$(env STUB_DIR="$d" KUBECTL="$d/bin/kubectl" CURL="$d/bin/curl" STUB_PODS_AFTER=pods-after.json \
    "$SELF" --kubeconfig "$d/kc" --namespace ns1 --round rT --phase R1 --duration 1 --out "$d/out" \
    --marker "$d/marker.json" --nats-prof-port 6061 2>&1)" && code=0 || code=$?
  [[ "$code" -eq 1 ]] && grep -qi "replaced\|restarted" <<<"$out" \
    || { echo "self-test: a pod replaced mid-window was not a failure (exit $code)" >&2; echo "$out" >&2; rc=1; }

  # 7. --dry-run: a preflight that touches nothing and starts no port-forward.
  rm -rf "$d/out"; : >"$d/kubectl.log"
  run 0 --dry-run
  [[ ! -e "$d/out" ]] || { echo "self-test: --dry-run wrote output" >&2; rc=1; }
  ! grep -q "port-forward\|^exec" "$d/kubectl.log" || { echo "self-test: --dry-run ran a port-forward or exec" >&2; rc=1; }
  grep -q "cpu1" <<<"$out" || { echo "self-test: --dry-run did not print its plan" >&2; rc=1; }
  # ...and a dry run still REFUSES on a marker that is not steady, so it is a real preflight.
  steady_marker warmup R1 "$later"; run 3 --dry-run

  # 8. The broker's prof_port is read from its config when --nats-prof-port is not given, in the
  #    JSON form the chart renders (`"prof_port": 6061,`), and the console (a static nginx behind
  #    the same functional-area label) is not a profiling target.
  steady_marker steady R1 "$later"; rm -rf "$d/out"
  out="$(env STUB_DIR="$d" KUBECTL="$d/bin/kubectl" CURL="$d/bin/curl" "$SELF" --kubeconfig "$d/kc" --namespace ns1 \
    --round rT --phase R1 --duration 1 --out "$d/out" --marker "$d/marker.json" --dry-run 2>&1)" && code=0 || code=$?
  [[ "$code" -eq 0 ]] && grep -q "nats dc-nats-0 :6061" <<<"$out" \
    || { echo "self-test: prof_port was not read from the JSON broker config (exit $code)" >&2; echo "$out" >&2; rc=1; }
  ! grep -q "frontend" <<<"$out" || { echo "self-test: the console was planned as a profiling target" >&2; echo "$out" >&2; rc=1; }

  # 9. The relational store may live in another namespace (dc-system): its psql runs there.
  rm -rf "$d/out"; : >"$d/kubectl.log"
  run 0 --rdb-namespace nsdb
  grep -q "exec -n nsdb dc-rdb-1" "$d/kubectl.log" || { echo "self-test: --rdb-namespace did not move the relational store's psql" >&2; rc=1; }
  grep -q "exec -n ns1 dc-tsdb-1" "$d/kubectl.log" || { echo "self-test: the event store's psql left the instance namespace" >&2; rc=1; }

  [[ "$rc" -eq 0 ]] && echo "self-test: ok"
  return "$rc"
}

if [[ "${self_test_requested:-0}" == 1 ]]; then self_test; exit $?; fi

# ---------------------------------------------------------------------------
# Validate usage
# ---------------------------------------------------------------------------
[[ -n "$kubeconfig" ]] || usage_err "--kubeconfig is required: a lane-private file, never the shared context"
[[ -f "$kubeconfig" ]] || usage_err "--kubeconfig $kubeconfig does not exist"
kargs=(--kubeconfig "$kubeconfig")
[[ -z "$kctx" ]] || kargs+=(--context "$kctx")
[[ -n "$ns" ]] || usage_err "--namespace is required"
[[ "$round" =~ ^[A-Za-z0-9_.]+$ ]] || usage_err "--round is required and may use letters, digits, '_' and '.' only"
[[ "$phase" =~ ^[A-Za-z0-9_.]+$ ]] || usage_err "--phase is required and may use letters, digits, '_' and '.' only"
[[ -n "$duration" ]] || usage_err "--duration is required (seconds of each CPU profile, 1-60)"
secs="$(to_seconds "$duration")"
(( secs >= 1 && secs <= 60 )) || usage_err "--duration $duration: a CPU profile is 1-60 seconds (the services bound it at 60)"
[[ "$cpu_profiles" =~ ^[1-9][0-9]*$ ]] || usage_err "--cpu-profiles must be a positive whole number"
for tool in "$KUBECTL" "$CURL" "$jq_real"; do command -v "$tool" >/dev/null 2>&1 || usage_err "$tool is required on PATH"; done
if [[ "$steady_check" -eq 1 && -z "$marker" ]]; then
  usage_err "--marker is required (the harness's --steady-marker file); pass --no-steady-check to collect without one, and the manifest will say so"
fi
[[ -n "$out" ]] || out="$ROOT/perf-$round"
[[ -n "$nats_ns" ]] || nats_ns="$ns"
# The relational store lives in the cluster's system namespace (dc-system), not the instance's.
[[ -n "$rdb_ns" ]] || rdb_ns="$ns"
window=$(( secs * cpu_profiles + 30 ))   # profiles plus the before/after snapshots and slack

# ---------------------------------------------------------------------------
# The steady marker
# ---------------------------------------------------------------------------
marker_since=""
check_marker() { # prints nothing; sets marker_since; returns 0 steady, 3 refused
  local state mphase ends now_e rem
  if [[ ! -s "$marker" ]]; then say "REFUSED: no steady marker at $marker; absence is not steady"; return 3; fi
  state="$("$JQ" -r '.state // ""' "$marker" 2>/dev/null)" || { say "REFUSED: $marker is not JSON"; return 3; }
  mphase="$("$JQ" -r '.phase // ""' "$marker")"
  if [[ "$state" != steady ]]; then say "REFUSED: the marker says '$state', not steady; a profile across a ramp or a stall is discarded"; return 3; fi
  if [[ -n "$mphase" && "$mphase" != "$phase" ]]; then say "REFUSED: the marker is for phase '$mphase', this is phase '$phase'"; return 3; fi
  ends="$("$JQ" -r '.windowEnds // ""' "$marker")"
  if [[ -n "$ends" ]]; then
    now_e="$(date -u +%s)"; rem=$(( $(epoch_of "$ends") - now_e ))
    if (( rem < window )); then say "REFUSED: the steady window ends in ${rem}s and this collection needs ${window}s"; return 3; fi
  fi
  marker_since="$("$JQ" -r '.since' "$marker")"
  return 0
}
if [[ "$steady_check" -eq 1 ]]; then
  check_marker || exit 3
fi

# ---------------------------------------------------------------------------
# Discovery (read-only)
# ---------------------------------------------------------------------------
# all_pods: the instance's pods, plus the relational store's when it lives in another namespace
# (the manifest, the restart check and the CPU mapping all work from this one list).
all_pods() {
  local a b
  a="$(k get pods -n "$ns" -o json)" || return 1
  if [[ "$rdb_ns" == "$ns" ]]; then printf '%s\n' "$a"; return 0; fi
  b="$(k get pods -n "$rdb_ns" -o json)" || return 1
  # on stdin, not --argjson: two pod lists overflow a Windows command line
  printf '%s\n%s\n' "$a" "$b" | "$jq_real" -s '{items: (.[0].items + .[1].items)}' | tr -d '\r'
}
pods_json="$(all_pods)" || die "cannot list pods in $ns (and $rdb_ns)"
deploys_json="$(k get deploy -n "$ns" -l devicechain.io/functional-area -o json)" || die "cannot list deployments in $ns"

failures_file="$(mktemp)"; rows_file="$(mktemp)"; trap 'pf_close 2>/dev/null || true; rm -f "$failures_file" "$rows_file"; [[ -z "${pf_dir:-}" ]] || rm -rf "$pf_dir"' EXIT
fail_rec() { # component pod kind message
  "$JQ" -nc --arg c "$1" --arg p "$2" --arg k "$3" --arg m "$4" '{component:$c,pod:$p,kind:$k,message:$m}' >>"$failures_file"
  say "FAILED $1 $2 $3: $4"
}

# plan rows: component|pod|kind-of-target|port
plan=()
add_plan() { plan+=("$1|$2|$3|$4"); }

# Services: every Deployment with a functional-area label (the frontend carries none).
mapfile -t areas < <("$JQ" -r '.items[] | .metadata.labels["devicechain.io/functional-area"] + "|" + (.spec.replicas|tostring)' <<<"$deploys_json" | sort)
[[ ${#areas[@]} -gt 0 ]] || { say "FAILED: no DeviceChain Deployments found in namespace $ns"; exit 1; }
for row in "${areas[@]}"; do
  area="${row%%|*}"; want="${row##*|}"
  if [[ -n "$services_filter" && ",$services_filter," != *",$area,"* ]]; then continue; fi
  # The console is a static nginx behind the same label; it is no Go service and has no profiler.
  [[ "$area" != frontend ]] || continue
  mapfile -t svc_pods < <("$JQ" -r --arg a "$area" '.items[] | select(.metadata.labels["devicechain.io/functional-area"]==$a and .status.phase=="Running" and ([.status.containerStatuses[]?.ready]|all)) | .metadata.name' <<<"$pods_json")
  if [[ ${#svc_pods[@]} -ne "$want" ]]; then
    fail_rec "$area" "-" pods "${#svc_pods[@]} of $want pods ready (the Deployment wants $want); a smaller sample is not collected silently"
  fi
  for p in "${svc_pods[@]}"; do
    addr="$("$JQ" -r --arg p "$p" '.items[]|select(.metadata.name==$p)|[.spec.containers[].env[]?|select(.name=="DC_PROFILER_ADDRESS")|.value][0] // ""' <<<"$pods_json")"
    if [[ -z "$addr" ]]; then
      fail_rec "$area" "$p" profiler "DC_PROFILER_ADDRESS is not set: turn the listener on with functionalAreas.$area.profiler.enabled=true (and blockRate/mutexFraction for those profiles)"
      continue
    fi
    add_plan "$area" "$p" service "${addr##*:}"
  done
done

# NATS
if [[ "$skip_nats" -eq 0 ]]; then
  nats_json="$pods_json"
  if [[ "$nats_ns" != "$ns" ]]; then nats_json="$(k get pods -n "$nats_ns" -o json)" || die "cannot list pods in $nats_ns"; fi
  mapfile -t nats_pods < <("$JQ" -r '.items[]|select(.metadata.labels["app.kubernetes.io/name"]=="nats" and .metadata.labels["app.kubernetes.io/component"]=="nats" and .status.phase=="Running")|.metadata.name' <<<"$nats_json")
  if [[ ${#nats_pods[@]} -eq 0 ]]; then
    fail_rec nats "-" pods "no running NATS pod in namespace $nats_ns"
  else
    if [[ -z "$nats_prof_port" ]]; then
      cm="$(k get configmap -n "$nats_ns" -l app.kubernetes.io/name=nats -o json 2>/dev/null || true)"
      [[ -n "$cm" ]] || cm='{"items":[]}'
      # jq pulls out the config values and grep -E finds the port: jq's regex functions behave
      # differently across jq releases (this one passed on jq 1.8 and failed on CI's runner).
      cm_values="$("$JQ" -r '.items[]?.data[]?' <<<"$cm" 2>/dev/null || true)"
      nats_prof_port="$(grep -oE '"?prof_port"?[[:space:]]*[:=][[:space:]]*[0-9]+' <<<"$cm_values" \
        | grep -oE '[0-9]+$' | sed -n '1p' || true)"
    fi
    if [[ -z "$nats_prof_port" ]]; then
      fail_rec nats "-" profiler "prof_port is not in the broker config and --nats-prof-port was not given: set the OpenTofu instance variable nats_profiling_port for the run"
    fi
    for p in "${nats_pods[@]}"; do add_plan nats "$p" nats "${nats_prof_port:-0}"; done
  fi
fi

# Databases: the primary of each CNPG cluster.
if [[ "$skip_db" -eq 0 ]]; then
  for pair in "postgres|$rdb_cluster" "timescaledb|$tsdb_cluster"; do
    comp="${pair%%|*}"; cl="${pair##*|}"
    pp="$("$JQ" -r --arg c "$cl" '[.items[]|select(.metadata.labels["cnpg.io/cluster"]==$c and .metadata.labels["cnpg.io/instanceRole"]=="primary" and .status.phase=="Running")|.metadata.name][0] // ""' <<<"$pods_json")"
    if [[ -z "$pp" ]]; then fail_rec "$comp" "-" pods "no running primary pod for CNPG cluster $cl"; continue; fi
    add_plan "$comp" "$pp" db 0
  done
fi

# Every node that hosts a pod of the plan gets a cAdvisor read.
mapfile -t nodes < <("$JQ" -r '.items[]|select(.status.phase=="Running")|.spec.nodeName' <<<"$pods_json" | sort -u)

if [[ "$dry_run" -eq 1 ]]; then
  echo "perf-collect --dry-run: round=$round phase=$phase namespace=$ns duration=${secs}s x $cpu_profiles cpu profile(s); steady marker: $([[ $steady_check -eq 1 ]] && echo "OK (since $marker_since)" || echo "not checked")"
  for row in "${plan[@]}"; do
    IFS='|' read -r comp pod tgt port <<<"$row"
    case "$tgt" in
      service) echo "  $comp $pod :$port  -> cpu1..$cpu_profiles heap allocs mutex block" ;;
      nats)    echo "  $comp $pod :${port}  -> cpu1..$cpu_profiles heap allocs [block]; varz/jsz/connz pre+post" ;;
      db)      echo "  $comp $pod  -> pgss pre/post/delta/delta-prev, sysstat pre/post/delta, tables pre/post/delta" ;;
    esac
  done
  echo "  nodes: ${nodes[*]:-none}  -> cadvisor pre+post; metrics-api pods/nodes pre+post"
  if [[ -s "$failures_file" ]]; then echo "perf-collect --dry-run: the plan has $(wc -l <"$failures_file" | tr -d ' ') failure(s) above; a real run would exit 1" >&2; exit 1; fi
  exit 0
fi

# ---------------------------------------------------------------------------
# Collection
# ---------------------------------------------------------------------------
mkdir -p "$out"
out="$(cd "$out" && pwd)"
pf_dir="$(mktemp -d)"
prefix="$out/$round-$phase"
started_at="$(now_utc)"

# snapshot of pod identity, to detect a restart or replacement during the window
identity() { "$JQ" -c '[.items[]|{pod:.metadata.name,uid:.metadata.uid,restarts:([.status.containerStatuses[]?.restartCount]|add // 0)}]|sort_by(.pod)' <<<"$1"; }
identity_before="$(identity "$pods_json")"
# The role every database pod holds BEFORE the window; the manifest carries the roles after it, so a
# failover inside the window is visible and the per-instance DB CPU is not split by a stale role.
roles_before="$("$JQ" -c '[.items[] | select(.metadata.labels["cnpg.io/cluster"]) | {pod: .metadata.name, role: (.metadata.labels["cnpg.io/instanceRole"] // null)}]' <<<"$pods_json")"

artifact() { # component pod kind file   (file already written)
  local c="$1" p="$2" kd="$3" f="$4" bytes
  bytes="$(wc -c <"$f" | tr -d ' ')"
  if [[ "$bytes" -eq 0 ]]; then fail_rec "$c" "$p" "$kd" "empty output ($f)"; rm -f "$f"; return 1; fi
  "$JQ" -nc --arg c "$c" --arg p "$p" --arg k "$kd" --arg f "$(basename "$f")" --arg s "$(sha256 "$f")" --argjson b "$bytes" --arg t "$(now_utc)" \
    '{component:$c,pod:$p,kind:$k,file:$f,sha256:$s,bytes:$b,collectedAt:$t}' >>"$rows_file"
}
outname() { echo "$prefix-$1-$2-$3.$4"; } # component pod kind ext

# port-forward to a pod port; prints the local port, keeps running until killed by the caller's cleanup
pf_open() { # ns pod remote_port -> sets PF_PID PF_PORT
  local logf="$pf_dir/pf.$BASHPID.$RANDOM.log" i
  : >"$logf"
  # env execs kubectl, so $! is the port-forward itself and a kill reaches it (a backgrounded
  # shell FUNCTION would leave the kubectl child behind as an orphan)
  env -u KUBECONFIG "$KUBECTL" "${kargs[@]}" port-forward -n "$1" "pod/$2" ":$3" >"$logf" 2>&1 &
  PF_PID=$!
  PF_PORT=""
  for i in $(seq 1 100); do
    PF_PORT="$(sed -n '1s/^Forwarding from 127\.0\.0\.1:\([0-9]*\) ->.*/\1/p' "$logf")"
    [[ -n "$PF_PORT" ]] && return 0
    kill -0 "$PF_PID" 2>/dev/null || break
    sleep 0.2
  done
  PF_PORT=""; kill "$PF_PID" 2>/dev/null || true
  return 1
}
pf_close() { [[ -z "${PF_PID:-}" ]] || { kill "$PF_PID" 2>/dev/null || true; wait "$PF_PID" 2>/dev/null || true; }; PF_PID=""; }

# fetch <comp> <pod> <kind> <url> <file> <maxtime>: 200 or a recorded failure; no file on failure
fetch() {
  local c="$1" p="$2" kd="$3" url="$4" f="$5" mt="$6" code errf="$pf_dir/curl.err.$BASHPID"
  for _ in 1 2; do
    code="$("$CURL" -sS --max-time "$mt" -o "$f" -w '%{http_code}' "$url" 2>"$errf")" || code="000"
    if [[ "$code" == 200 ]]; then artifact "$c" "$p" "$kd" "$f"; return $?; fi
    [[ "$code" == 404 ]] && break   # a refusal, not a blip
    sleep 2
  done
  local body; body="$(head -c 300 "$f" 2>/dev/null | tr '\n' ' ' || true)"; [[ -n "$body" ]] || body="$(head -c 200 "$errf" 2>/dev/null | tr '\n' ' ' || true)"
  rm -f "$f"
  fail_rec "$c" "$p" "$kd" "HTTP $code from $url: $body"
  return 1
}

# ---- one Go-profile target (a service or a NATS server), run as a background job
profile_pod() { # component pod port kind(service|nats)
  local c="$1" p="$2" port="$3" tgt="$4" i kd
  trap 'pf_close' EXIT
  trap 'exit 143' TERM INT
  local pfns; pfns="$( [[ $tgt == nats ]] && echo "$nats_ns" || echo "$ns")"
  # One port-forward PER PROFILE, not one per pod: a forward that sat idle across a 60 s profile
  # was dropped by the API server on a real cluster (r25), and every later fetch of that pod then
  # failed with "connection refused". A forward costs about a second.
  pf_fetch() { # kind path file maxtime
    if ! pf_open "$pfns" "$p" "$port"; then
      fail_rec "$c" "$p" "$1" "cannot port-forward to $p:$port (is the profiling listener on?)"; return 1
    fi
    local rc=0
    fetch "$c" "$p" "$1" "http://127.0.0.1:$PF_PORT$2" "$3" "$4" || rc=$?
    pf_close
    return "$rc"
  }
  # cpu profiles back to back; the other pods run theirs at the same time, so the window is shared
  for i in $(seq 1 "$cpu_profiles"); do
    pf_fetch "cpu$i" "/debug/pprof/profile?seconds=$secs" "$(outname "$c" "$p" "cpu$i" pb.gz)" $((secs + 30)) || true
  done
  for kd in heap allocs; do
    pf_fetch "$kd" "/debug/pprof/$kd" "$(outname "$c" "$p" "$kd" pb.gz)" 60 || true
  done
  if [[ "$tgt" == service ]]; then
    for kd in mutex block; do
      pf_fetch "$kd" "/debug/pprof/$kd" "$(outname "$c" "$p" "$kd" pb.gz)" 60 || true
    done
  else
    # nats-server has no mutex-fraction setting; block needs prof_block_rate in its config
    if [[ -n "${nats_block_on:-}" ]]; then
      pf_fetch block "/debug/pprof/block" "$(outname "$c" "$p" block pb.gz)" 60 || true
    fi
  fi
}

# ---- NATS monitoring snapshots (port-forward to the monitor port)
nats_monitor() { # pod when(pre|post)
  local p="$1" when="$2" mport
  mport="$("$JQ" -r --arg p "$p" '[.items[]|select(.metadata.name==$p)|.spec.containers[].ports[]?|select(.name=="monitor")|.containerPort][0] // 8222' <<<"$pods_json")"
  if ! pf_open "$nats_ns" "$p" "$mport"; then fail_rec nats "$p" "monitor-$when" "cannot port-forward to the monitoring port $mport"; return 0; fi
  local base="http://127.0.0.1:$PF_PORT"
  fetch nats "$p" "varz-$when" "$base/varz" "$(outname nats "$p" "varz-$when" json)" 60 || true
  fetch nats "$p" "jsz-$when" "$base/jsz?streams=1&consumers=1" "$(outname nats "$p" "jsz-$when" json)" 120 || true
  fetch nats "$p" "connz-$when" "$base/connz?limit=1024" "$(outname nats "$p" "connz-$when" json)" 120 || true
  pf_close
}

# ---- databases
psql_json() { # pod database sql -> stdout
  k exec -n "${psql_ns:-$ns}" "$1" -c postgres -- psql -X -At -v ON_ERROR_STOP=1 -d "$2" -c "$3"
}
psql_db_pod() { # namespace pod database sql -> stdout   (the shape lib/dbstats.sh calls)
  psql_ns="$1"; psql_json "$2" "$3" "$4"
}
PGSS_SQL="SELECT jsonb_build_object('takenAt', now(), 'statsReset', (SELECT stats_reset FROM pg_stat_statements_info), 'dealloc', (SELECT dealloc FROM pg_stat_statements_info), 'statements', coalesce((SELECT jsonb_agg(jsonb_build_object('key', s.userid::text||'/'||s.dbid::text||'/'||s.queryid::text||'/'||s.toplevel::text, 'db', d.datname, 'queryid', s.queryid::text, 'query', left(s.query, 400), 'calls', s.calls, 'total_exec_time', s.total_exec_time, 'rows', s.rows, 'shared_blks_hit', s.shared_blks_hit, 'shared_blks_read', s.shared_blks_read, 'wal_bytes', s.wal_bytes, 'wal_records', s.wal_records)) FROM pg_stat_statements s LEFT JOIN pg_database d ON d.oid = s.dbid), '[]'::jsonb))"
SYS_SQL_BASE="'takenAt', now(), 'inRecovery', pg_is_in_recovery(), 'walLsn', pg_current_wal_lsn()::text, 'statsReset', (SELECT stats_reset FROM pg_stat_bgwriter), 'bgwriter', (SELECT to_jsonb(b) - 'stats_reset' FROM pg_stat_bgwriter b), 'wal', (SELECT to_jsonb(w) - 'stats_reset' FROM pg_stat_wal w)"
HYPER_SQL="'hypertables', coalesce((SELECT jsonb_object_agg(h, n) FROM (SELECT c.hypertable_name h, sum(t.n_tup_ins) n FROM timescaledb_information.chunks c JOIN pg_stat_user_tables t ON t.schemaname = c.chunk_schema AND t.relname = c.chunk_name GROUP BY 1) x), '{}'::jsonb)"

# jq: delta of two pg_stat_statements documents; invalid (never a plausible number) on a reset or an eviction
PGSS_DELTA='
  ($a.statements | map({(.key): .}) | add // {}) as $A
  | if ($a.statsReset != $b.statsReset) then {valid:false, reason:"pg_stat_statements was reset between the snapshots", from:$a.takenAt, to:$b.takenAt}
    elif (($a.dealloc // 0) != ($b.dealloc // 0)) then {valid:false, reason:"pg_stat_statements evicted entries between the snapshots (pg_stat_statements.max reached): raise it", from:$a.takenAt, to:$b.takenAt}
    else
      ($b.statements | map(. as $s | ($A[$s.key] // {}) as $o
        | def d($f): (($s[$f] // 0) - ($o[$f] // 0));
          {key:$s.key, db:$s.db, queryid:$s.queryid, query:$s.query,
           calls:d("calls"), total_exec_time:d("total_exec_time"), rows:d("rows"),
           shared_blks_hit:d("shared_blks_hit"), shared_blks_read:d("shared_blks_read"),
           wal_bytes:d("wal_bytes"), wal_records:d("wal_records")})) as $d
      | if ($d | any(.calls < 0 or .total_exec_time < 0)) then {valid:false, reason:"a counter went backwards: the server restarted or the statement was evicted", from:$a.takenAt, to:$b.takenAt}
        else {valid:true, from:$a.takenAt, to:$b.takenAt, statements: ($d | map(select(.calls > 0)) | sort_by(-.total_exec_time))} end
    end'
# jq: numeric delta of two sysstat documents (every number under bgwriter/checkpointer/wal/hypertables)
SYS_DELTA='
  def sub($x; $y): if ($x|type)=="object" then ($x | with_entries(.value = sub(.value; ($y[.key]) // 0))) elif ($x|type)=="number" then ($x - ($y|tonumber? // 0)) else $x end;
  if $a.statsReset != $b.statsReset then {valid:false, reason:"statistics were reset between the snapshots", from:$a.takenAt, to:$b.takenAt}
  else {valid:true, from:$a.takenAt, to:$b.takenAt, bgwriter:sub($b.bgwriter; $a.bgwriter), wal:sub($b.wal; $a.wal),
        checkpointer:(if $b.checkpointer then sub($b.checkpointer; $a.checkpointer) else null end),
        hypertables:sub($b.hypertables; $a.hypertables)} end'

db_snapshot() { # component pod when(pre|post)
  local c="$1" p="$2" when="$3" database="postgres" f pj sj hasck errf="$pf_dir/pg.err.$BASHPID"
  if [[ "$c" == postgres ]]; then psql_ns="$rdb_ns"; else psql_ns="$ns"; fi
  # The app database is only needed for the per-hypertable counts (the event store).
  if [[ "$c" == timescaledb ]]; then
    database="$tsdb_database"
    if [[ -z "$database" ]]; then
      database="$(psql_json "$p" postgres "SELECT datname FROM pg_database WHERE NOT datistemplate AND datname <> 'postgres'" 2>/dev/null || true)"
      [[ "$(wc -w <<<"$database")" -eq 1 ]] || { fail_rec "$c" "$p" "sysstat-$when" "cannot pick the event-store database (found: ${database:-none}); pass --tsdb-database"; return 0; }
    fi
  fi
  # pg_stat_statements: the extension is the VIEW; the library must already be preloaded.
  if ! psql_json "$p" postgres "CREATE EXTENSION IF NOT EXISTS pg_stat_statements" >/dev/null 2>"$errf"; then
    fail_rec "$c" "$p" "pgss-$when" "CREATE EXTENSION pg_stat_statements failed: $(head -c 300 "$errf" | tr '\n' ' ') -- turn the preload on for the run: OpenTofu postgres_statement_stats (relational) / timescale_statement_stats (event store)"
  else
    f="$(outname "$c" "$p" "pgss-$when" json)"
    if pj="$(psql_json "$p" postgres "$PGSS_SQL" 2>"$errf")" && "$JQ" -e . >/dev/null 2>&1 <<<"$pj"; then
      printf '%s\n' "$pj" >"$f"; artifact "$c" "$p" "pgss-$when" "$f" || true
    else
      fail_rec "$c" "$p" "pgss-$when" "pg_stat_statements is not readable: $(head -c 300 "$errf" | tr '\n' ' ') -- turn the preload on for the run: OpenTofu postgres_statement_stats / timescale_statement_stats"
    fi
  fi
  # system statistics. PG17 moved checkpoint counters to pg_stat_checkpointer.
  hasck="$(psql_json "$p" postgres "SELECT to_regclass('pg_stat_checkpointer') IS NOT NULL" 2>/dev/null || echo f)"
  local sys="$SYS_SQL_BASE"
  [[ "$hasck" == t ]] && sys="$sys, 'checkpointer', (SELECT to_jsonb(c) - 'stats_reset' FROM pg_stat_checkpointer c)"
  local sql="SELECT jsonb_build_object($sys"
  if [[ "$c" == timescaledb ]]; then sql="$sql, $HYPER_SQL"; fi
  sql="$sql)"
  f="$(outname "$c" "$p" "sysstat-$when" json)"
  if sj="$(psql_json "$p" "$database" "$sql" 2>"$errf")" && "$JQ" -e . >/dev/null 2>&1 <<<"$sj"; then
    printf '%s\n' "$sj" >"$f"; artifact "$c" "$p" "sysstat-$when" "$f" || true
  else
    fail_rec "$c" "$p" "sysstat-$when" "statistics are not readable: $(head -c 300 "$errf" | tr '\n' ' ')"
  fi
  # per-table write and vacuum counters, every application database of the cluster
  f="$(outname "$c" "$p" "tables-$when" json)"
  if sj="$(dbstats_tables psql_db_pod "${psql_ns:-$ns}" "$p" 2>"$errf")" && "$JQ" -e . >/dev/null 2>&1 <<<"$sj"; then
    printf '%s\n' "$sj" >"$f"; artifact "$c" "$p" "tables-$when" "$f" || true
  else
    fail_rec "$c" "$p" "tables-$when" "table statistics are not readable: $(head -c 300 "$errf" | tr '\n' ' ')"
  fi
}

db_deltas() { # component pod
  local c="$1" p="$2" pre post d
  pre="$(outname "$c" "$p" pgss-pre json)"; post="$(outname "$c" "$p" pgss-post json)"
  if [[ -s "$pre" && -s "$post" ]]; then
    d="$(outname "$c" "$p" pgss-delta json)"
    "$JQ" -n --slurpfile a "$pre" --slurpfile b "$post" '$a[0] as $a | $b[0] as $b | '"$PGSS_DELTA" >"$d"
    "$JQ" -e '.valid' "$d" >/dev/null || fail_rec "$c" "$p" pgss-delta "$("$JQ" -r .reason "$d")"
    artifact "$c" "$p" pgss-delta "$d" || true
    # the delta against the PREVIOUS phase: its post snapshot is kept beside the outputs
    local last="$out/.last-pgss-$c.json"
    if [[ -s "$last" ]]; then
      d="$(outname "$c" "$p" pgss-delta-prev json)"
      "$JQ" -n --slurpfile a "$last" --slurpfile b "$post" '$a[0] as $a | $b[0] as $b | '"$PGSS_DELTA" >"$d"
      "$JQ" -e '.valid' "$d" >/dev/null || fail_rec "$c" "$p" pgss-delta-prev "$("$JQ" -r .reason "$d") (against the previous phase's snapshot)"
      artifact "$c" "$p" pgss-delta-prev "$d" || true
    fi
    cp "$post" "$last"
  fi
  pre="$(outname "$c" "$p" sysstat-pre json)"; post="$(outname "$c" "$p" sysstat-post json)"
  if [[ -s "$pre" && -s "$post" ]]; then
    d="$(outname "$c" "$p" sysstat-delta json)"
    "$JQ" -n --slurpfile a "$pre" --slurpfile b "$post" '$a[0] as $a | $b[0] as $b | '"$SYS_DELTA" >"$d"
    "$JQ" -e '.valid' "$d" >/dev/null || fail_rec "$c" "$p" sysstat-delta "$("$JQ" -r .reason "$d")"
    artifact "$c" "$p" sysstat-delta "$d" || true
  fi
  pre="$(outname "$c" "$p" tables-pre json)"; post="$(outname "$c" "$p" tables-post json)"
  if [[ -s "$pre" && -s "$post" ]]; then
    d="$(outname "$c" "$p" tables-delta json)"
    "$JQ" -n --slurpfile a "$pre" --slurpfile b "$post" '$a[0] as $a | $b[0] as $b | '"$DBSTATS_TABLES_DELTA" >"$d"
    "$JQ" -e '.valid' "$d" >/dev/null || fail_rec "$c" "$p" tables-delta "$("$JQ" -r .reason "$d")"
    artifact "$c" "$p" tables-delta "$d" || true
  fi
}

# ---- nodes: cAdvisor and the metrics API
node_snapshots() { # when
  local when="$1" n f errf="$pf_dir/cad.err.$BASHPID"
  for n in "${nodes[@]}"; do
    f="$(outname node "$n" "cadvisor-$when" prom)"
    if k get --raw "/api/v1/nodes/$n/proxy/metrics/cadvisor" 2>"$errf" \
        | grep -E "^(container_cpu_usage_seconds_total|container_cpu_cfs_throttled_seconds_total|container_memory_working_set_bytes|container_memory_rss|machine_cpu_cores)[{ ]" \
        | grep -E "namespace=\"($ns|$rdb_ns)\"|id=\"/\"|^machine_cpu_cores" >"$f"; then
      artifact node "$n" "cadvisor-$when" "$f" || true
    else
      rm -f "$f"; fail_rec node "$n" "cadvisor-$when" "cannot read the node's cAdvisor metrics through the API server: $(head -c 200 "$errf" | tr '\n' ' ')"
    fi
  done
  f="$(outname metrics-api pods "top-$when" json)"
  if k get --raw "/apis/metrics.k8s.io/v1beta1/namespaces/$ns/pods" >"$f" 2>/dev/null && [[ -s "$f" ]]; then artifact metrics-api pods "top-$when" "$f" || true
  else rm -f "$f"; say "note: the metrics API is not available; the cAdvisor counters are the CPU source"; fi
  f="$(outname metrics-api nodes "top-$when" json)"
  if k get --raw "/apis/metrics.k8s.io/v1beta1/nodes" >"$f" 2>/dev/null && [[ -s "$f" ]]; then artifact metrics-api nodes "top-$when" "$f" || true; else rm -f "$f"; fi
}

# nats block profile only when the broker config sets prof_block_rate
nats_block_on=""
if [[ "$skip_nats" -eq 0 ]]; then
  cm_all="$(k get configmap -n "$nats_ns" -l app.kubernetes.io/name=nats -o json 2>/dev/null || true)"
  if [[ -n "$cm_all" ]] && "$JQ" -e '[.items[]?.data[]? | select(test("(^|\\n)\\s*\"?prof_block_rate\"?\\s*[:=]\\s*[1-9]"))] | length > 0' >/dev/null 2>&1 <<<"$cm_all"; then nats_block_on=1; fi
fi

say "round=$round phase=$phase namespace=$ns: $cpu_profiles x ${secs}s CPU profile(s) from ${#plan[@]} target(s) -> $out"

# --- before
node_snapshots pre
for row in "${plan[@]}"; do
  IFS='|' read -r comp pod tgt port <<<"$row"
  case "$tgt" in
    nats) nats_monitor "$pod" pre ;;
    db) db_snapshot "$comp" "$pod" pre ;;
  esac
done

# --- the window: every Go profile at once
jobs_pids=()
for row in "${plan[@]}"; do
  IFS='|' read -r comp pod tgt port <<<"$row"
  case "$tgt" in
    service|nats) ( profile_pod "$comp" "$pod" "$port" "$tgt" ) & jobs_pids+=("$!") ;;
  esac
done
for pid in ${jobs_pids[@]+"${jobs_pids[@]}"}; do wait "$pid" || true; done

# --- after
for row in "${plan[@]}"; do
  IFS='|' read -r comp pod tgt port <<<"$row"
  case "$tgt" in
    nats) nats_monitor "$pod" post ;;
    db) db_snapshot "$comp" "$pod" post; db_deltas "$comp" "$pod" ;;
  esac
done
node_snapshots post

# --- did anything move under us?
pods_after="$(all_pods)" || die "cannot list pods after the window"
identity_after="$(identity "$pods_after")"
invalid_pods="[]"
for row in "${plan[@]}"; do
  IFS='|' read -r comp pod tgt port <<<"$row"
  b="$("$JQ" -c --arg p "$pod" '.[]|select(.pod==$p)' <<<"$identity_before")"
  a="$("$JQ" -c --arg p "$pod" '.[]|select(.pod==$p)' <<<"$identity_after")"
  if [[ -z "$a" ]]; then
    fail_rec "$comp" "$pod" window "the pod is gone after the window: it was replaced; everything taken from it is discarded"; invalid_pods="$("$JQ" -c --arg p "$pod" '. + [$p]' <<<"$invalid_pods")"
  elif [[ "$a" != "$b" ]]; then
    fail_rec "$comp" "$pod" window "the pod restarted or was replaced during the window (before $b, after $a): everything taken from it is discarded"; invalid_pods="$("$JQ" -c --arg p "$pod" '. + [$p]' <<<"$invalid_pods")"
  fi
done
if [[ "$steady_check" -eq 1 ]]; then
  after_since="$("$JQ" -r '[.state, .since] | join(" ")' "$marker" 2>/dev/null || echo "unreadable")"
  if [[ "$after_since" != "steady $marker_since" ]]; then
    fail_rec harness "-" window "the steady marker moved during the collection (was 'steady $marker_since', now '$after_since'): the window closed or restarted, so this phase's profiles are discarded"
    invalid_pods='["*"]'
  fi
fi

# ---------------------------------------------------------------------------
# Manifest
# ---------------------------------------------------------------------------
finished_at="$(now_utc)"
printf '%s\n' "$pods_after" >"$pf_dir/pods_after.json"
manifest="$prefix-manifest.json"
marker_doc="null"; [[ "$steady_check" -eq 1 && -s "$marker" ]] && marker_doc="$("$JQ" -c . "$marker")"
"$JQ" -n --arg round "$round" --arg phase "$phase" --arg ns "$ns" --arg s "$started_at" --arg f "$finished_at" \
  --argjson secs "$secs" --argjson k "$cpu_profiles" --argjson checked "$([[ $steady_check -eq 1 ]] && echo true || echo false)" \
  --argjson marker "$marker_doc" --argjson invalid "$invalid_pods" --argjson rolesBefore "$roles_before" \
  --slurpfile pods "$pf_dir/pods_after.json" \
  --slurpfile rows "$rows_file" --slurpfile fails "$failures_file" \
  --arg rdb "$rdb_cluster" --arg tsdb "$tsdb_cluster" '
  def comp_of: if .metadata.labels["devicechain.io/functional-area"] then .metadata.labels["devicechain.io/functional-area"]
               elif .metadata.labels["app.kubernetes.io/name"] == "nats" then "nats"
               elif .metadata.labels["cnpg.io/cluster"] == $rdb then "postgres"
               elif .metadata.labels["cnpg.io/cluster"] == $tsdb then "timescaledb"
               else null end;
  ($rows | map(. + {invalid: ((.pod as $p | $invalid | (index("*") != null or index($p) != null)))})) as $arts
  | {schema:1, round:$round, phase:$phase, namespace:$ns, startedAt:$s, finishedAt:$f,
     cpuProfileSeconds:$secs, cpuProfiles:$k,
     steady:{checked:$checked, marker:$marker},
     complete: (($fails | length) == 0),
     failures: $fails,
     artifacts: $arts,
     dbRolesBefore: $rolesBefore,
     pods: [$pods[0].items[] | select(comp_of != null) | {pod:.metadata.name, component:comp_of, node:.spec.nodeName, uid:.metadata.uid,
             role:(.metadata.labels["cnpg.io/instanceRole"] // null)}],
     images: [$pods[0].items[] | select(comp_of != null) | . as $p | .status.containerStatuses[]? | {pod:$p.metadata.name, component:($p | comp_of), container:.name, image:.image, imageID:.imageID, restartCount:.restartCount}]}' >"$manifest"

n_art="$("$JQ" '.artifacts | length' "$manifest")"; n_fail="$("$JQ" '.failures | length' "$manifest")"
if [[ "$n_fail" -gt 0 ]]; then
  say "INCOMPLETE: $n_art artifacts, $n_fail FAILURE(S) -- see $manifest (complete=false)"
  "$JQ" -r '.failures[] | "  - \(.component) \(.pod) \(.kind): \(.message)"' "$manifest" >&2
  exit 1
fi
say "complete: $n_art artifacts -> $out ($manifest)"
