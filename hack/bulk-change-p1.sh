#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# P-1: what bulk changes cost the interactive path and ingest, measured as an A/B on the kind A/B rig.
#
#   hack/bulk-change-p1.sh seed   --kubeconfig KC --namespace NS --instance I --apiprobe BIN --server HOST --out DIR
#   hack/bulk-change-p1.sh run    --treatment triggers|capture|caps --kubeconfig KC --namespace NS --instance I \
#                                 --apiprobe BIN --server HOST --out DIR --bin-dir DIR [--dcctl PATH] \
#                                 [--runs 5] [--rate 20] [--duration 180] [--warmup 45] [--settle 40] \
#                                 [--mqtt-rate 1000] [--http-rate 500]
#   hack/bulk-change-p1.sh decide --out DIR --treatment T
#   hack/bulk-change-p1.sh --self-test
#
# THE QUESTION (the slice spec's P-1): does the bulk-change machinery make an ordinary attribute write, or an
# ingest PUBACK, slower? Three treatments, each measured on its own against a baseline on the SAME instance:
#
#   triggers  the attribute guard's triggers ENABLED (treatment) vs DISABLED (baseline). The only arm that
#             changes the database, and it is restored to ENABLED on every exit.
#   capture   configuration reports flowing (treatment) vs not (baseline): the drill devices' reports of the
#             revisions the interactive load mints, republished every few seconds. Capture shipped before
#             the triggers and cannot be switched off in one build, so this measures the report path WITH
#             capture against no reports at all: an upper bound on capture's own cost, said as such.
#   caps      one tenant at its bulk-change caps (treatment: two changes of 10,000 lines each at 50 posts/s)
#             vs none (baseline). Both changes are aborted when the arm ends.
#
# In every arm the same three things run for the same window: the named ingest profile (hack/perf-ab.sh, one
# arm each: the harnesses run in-cluster), an interactive load of `setEntityAttribute` at --rate per second on
# 200 devices of their own (apiprobe bulkchange attrload, client-side latency), and a once-a-second sample of
# the database's lock waits and of how long each shared family lock has been held.
#
# THE DECISION RULE, so the gate neither flakes nor is ignored: --runs alternating baseline and treatment arms
# (B T B T ...). On the interactive p99 (the gate) and the PUBACK p99 (reported the same way):
#
#   FAIL          median treatment p99 > 1.10 x median baseline p99, AND the difference exceeds twice the
#                 baseline's run-to-run spread (max - min of its p99s)
#   PASS          median treatment p99 <= 1.10 x median baseline p99
#   INCONCLUSIVE  above 10 percent but inside twice the spread: rerun with longer runs. NOT a pass.
#
# Fewer than --runs usable arms on either side is refused (exit 2), never decided.
#
# Exit: 0 pass, 1 fail, 2 usage or not enough runs, 3 inconclusive, 4 a measurement failed.
#
# PRECONDITIONS: the A/B rig's (hack/README-perf.md "Local A/B rig"): an instance with pg_stat_statements,
# Linux dc-loadtest binaries in --bin-dir; and an instance whose build SERVES bulk changes (the caps arm
# creates them). `seed` creates a tenant (apiprobe seed, as the superuser: DC_ADMIN_PASSWORD) and 20,200
# devices on a profile of their own: 1-200 take the interactive load, 201-20,200 are the caps arm's targets.
#
# SAFETY: only the kubeconfig named is used; the triggers are re-ENABLED on every exit; the caps arm's changes
# are aborted on every exit.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KUBECTL="${KUBECTL:-kubectl}"

say() { echo "bulk-change-p1: $*" >&2; }
die() { say "$*"; exit 4; }
usage_err() { say "$*"; exit 2; }

# ---------------------------------------------------------------------------
# The decision (pure: rows in, verdict out). A row is one arm's JSON summary.
# ---------------------------------------------------------------------------

# p1_decide <metric> <runs> reads one JSON row per line on stdin and prints the verdict JSON for that metric.
p1_decide() {
  jq -s --arg m "$1" --argjson runs "$2" '
    def median: sort | if length % 2 == 1 then .[(length - 1) / 2] else (.[length / 2 - 1] + .[length / 2]) / 2 end;
    [ .[] | select(.ok == true) ] as $rows
    | [ $rows[] | select(.arm == "baseline")  | .[$m] | select(type == "number") ] as $b
    | [ $rows[] | select(.arm == "treatment") | .[$m] | select(type == "number") ] as $t
    | if ($b | length) < $runs or ($t | length) < $runs then
        {metric: $m, verdict: "NOT_ENOUGH_RUNS", baselineRuns: ($b | length), treatmentRuns: ($t | length)}
      else
        ($b | median) as $mb | ($t | median) as $mt | (($b | max) - ($b | min)) as $spread
        | {metric: $m, baselineMedian: $mb, treatmentMedian: $mt, baselineSpread: $spread,
           ratio: (if $mb > 0 then ($mt / $mb) else null end),
           verdict: (if $mt <= 1.10 * $mb then "PASS"
                     elif ($mt - $mb) > 2 * $spread then "FAIL"
                     else "INCONCLUSIVE" end)}
      end'
}

# p1_exit <verdict> prints the exit code a verdict carries.
p1_exit() {
  case "$1" in
  PASS) echo 0 ;;
  FAIL) echo 1 ;;
  INCONCLUSIVE) echo 3 ;;
  *) echo 2 ;;
  esac
}

# p1_lock_summary reads the sampler's lines (`<epoch> waits=<n> famhold=<seconds|empty>`) and prints
# {lockWaitP99, lockWaitMax, familyHoldMaxMs, samples}.
p1_lock_summary() {
  awk '
    { for (i = 2; i <= NF; i++) { split($i, kv, "="); v[kv[1]] = kv[2] }
      w[n++] = v["waits"] + 0
      if (v["famhold"] != "" && v["famhold"] + 0 > fh) fh = v["famhold"] + 0 }
    END {
      if (n == 0) { print "{\"samples\":0}"; exit }
      for (i = 0; i < n; i++) for (j = i + 1; j < n; j++) if (w[j] < w[i]) { t = w[i]; w[i] = w[j]; w[j] = t }
      r = int(0.99 * n + 0.999999) - 1; if (r < 0) r = 0
      printf "{\"samples\":%d,\"lockWaitP99\":%d,\"lockWaitMax\":%d,\"familyHoldMaxMs\":%.0f}\n", n, w[r], w[n-1], fh * 1000
    }'
}

# p1_deployed_areas <probe areas> <running areas> prints, comma-separated and in the probe's order, the probe areas
# that also run (each list one name per line). Pure.
p1_deployed_areas() {
  local area out=""
  while read -r area; do
    [[ -n "$area" ]] || continue
    grep -qxF -- "$area" <<<"$2" && out+="${out:+,}$area"
  done <<<"$1"
  printf '%s' "$out"
}

# p1_enable_clause <pg_trigger.tgenabled> prints the ALTER TABLE clause that puts a trigger back in that mode:
# A -> ENABLE ALWAYS, R -> ENABLE REPLICA, O -> ENABLE. D (disabled) or anything else fails. Pure.
p1_enable_clause() {
  case "$1" in
  A) echo "ENABLE ALWAYS" ;;
  R) echo "ENABLE REPLICA" ;;
  O) echo "ENABLE" ;;
  *) return 1 ;;
  esac
}

self_test() {
  local rows out rc
  rows='{"arm":"baseline","ok":true,"interactiveP99":100}
{"arm":"baseline","ok":true,"interactiveP99":104}
{"arm":"baseline","ok":true,"interactiveP99":102}
{"arm":"baseline","ok":true,"interactiveP99":101}
{"arm":"baseline","ok":true,"interactiveP99":103}'
  t() { printf '%s\n' "$rows"; for v in "$@"; do printf '{"arm":"treatment","ok":true,"interactiveP99":%s}\n' "$v"; done; }
  out="$(t 105 108 106 107 109 | p1_decide interactiveP99 5)"
  [[ "$(jq -r .verdict <<<"$out")" == PASS ]] || die "SELF-TEST: a treatment within 10% was not a PASS: $out"
  out="$(t 130 131 129 128 132 | p1_decide interactiveP99 5)"
  [[ "$(jq -r .verdict <<<"$out")" == FAIL ]] || die "SELF-TEST: a treatment 27% above, far outside the spread, was not a FAIL: $out"
  # A noisy baseline (median 100, spread 40): a treatment median of 117 is more than 10 percent above it but
  # inside twice the spread, so it is neither a pass nor a fail.
  rows='{"arm":"baseline","ok":true,"interactiveP99":90}
{"arm":"baseline","ok":true,"interactiveP99":100}
{"arm":"baseline","ok":true,"interactiveP99":100}
{"arm":"baseline","ok":true,"interactiveP99":105}
{"arm":"baseline","ok":true,"interactiveP99":130}'
  out="$(t 115 118 116 117 140 | p1_decide interactiveP99 5)"
  [[ "$(jq -r .verdict <<<"$out")" == INCONCLUSIVE ]] || die "SELF-TEST: a rise above 10% but inside twice the spread was not INCONCLUSIVE: $out"
  [[ "$(p1_exit INCONCLUSIVE)" == 3 && "$(p1_exit PASS)" == 0 && "$(p1_exit FAIL)" == 1 && "$(p1_exit NOT_ENOUGH_RUNS)" == 2 ]] ||
    die "SELF-TEST: the exit codes do not match the verdicts"
  out="$(t 105 108 106 107 | p1_decide interactiveP99 5)"
  [[ "$(jq -r .verdict <<<"$out")" == NOT_ENOUGH_RUNS ]] || die "SELF-TEST: four treatment runs were decided: $out"
  out="$({ printf '%s\n' "$rows" | sed 's/"ok":true/"ok":false/'; for v in 1 1 1 1 1; do
    printf '{"arm":"treatment","ok":true,"interactiveP99":%s}\n' "$v"; done; } | p1_decide interactiveP99 5)"
  [[ "$(jq -r .verdict <<<"$out")" == NOT_ENOUGH_RUNS ]] || die "SELF-TEST: failed baseline runs were counted: $out"
  out="$(t 105 108 106 107 109 | p1_decide pubackP99 5)"
  [[ "$(jq -r .verdict <<<"$out")" == NOT_ENOUGH_RUNS ]] || die "SELF-TEST: a metric no row carries was decided: $out"
  out="$(printf '1 waits=0 famhold=\n2 waits=3 famhold=0.012\n3 waits=1 famhold=0.250\n' | p1_lock_summary)"
  [[ "$(jq -r '"\(.samples) \(.lockWaitMax) \(.familyHoldMaxMs)"' <<<"$out")" == "3 3 250" ]] ||
    die "SELF-TEST: the lock summary is wrong: $out"
  [[ "$(printf '' | p1_lock_summary | jq -r .samples)" == 0 ]] || die "SELF-TEST: an empty sample was summarized"
  out="$(p1_deployed_areas $'device-management\noutbound-connectors\nuser-management' $'user-management\n\ndevice-management\nevent-sources')"
  [[ "$out" == "device-management,user-management" ]] || die "SELF-TEST: the deployed areas are not the probe areas that run: '$out'"
  [[ -z "$(p1_deployed_areas 'outbound-connectors' 'outbound-connectors-x')" ]] || die "SELF-TEST: an area name was matched as a prefix"
  [[ "$(p1_enable_clause A)" == "ENABLE ALWAYS" ]] || die "SELF-TEST: an ALWAYS trigger would not be restored ALWAYS"
  [[ "$(p1_enable_clause O)" == "ENABLE" && "$(p1_enable_clause R)" == "ENABLE REPLICA" ]] || die "SELF-TEST: the origin/replica clauses are wrong"
  for bad in D "" x; do p1_enable_clause "$bad" >/dev/null && die "SELF-TEST: trigger mode '$bad' was given a clause"; done
  rc=0
  "$ROOT/hack/perf-ab.sh" --self-test >/dev/null 2>&1 || rc=$?
  [[ "$rc" == 0 ]] || die "SELF-TEST: hack/perf-ab.sh --self-test failed (rc=$rc), and every arm runs through it"
  say "self-test passed: PASS, FAIL, INCONCLUSIVE and not-enough-runs are each reached, and the lock summary reads its samples"
}

# ---------------------------------------------------------------------------
# Arguments
# ---------------------------------------------------------------------------
cmd="${1:-}"
[[ "$cmd" == --self-test ]] && { self_test; exit 0; }
[[ "$cmd" == seed || "$cmd" == run || "$cmd" == decide ]] || { sed -n '4,13p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 2; }
shift
kubeconfig="" ns="" instance="" apiprobe="" server="" scheme="http" out="" bin_dir="" dcctl="${DCCTL:-dcctl}"
treatment="" runs=5 rate=20 duration=180 warmup=45 settle=40 mqtt_rate=1000 http_rate=500
while [[ $# -gt 0 ]]; do
  case "$1" in
  --kubeconfig) kubeconfig="$2"; shift 2 ;;
  --namespace | -n) ns="$2"; shift 2 ;;
  --instance) instance="$2"; shift 2 ;;
  --apiprobe) apiprobe="$2"; shift 2 ;;
  --server) server="$2"; shift 2 ;;
  --scheme) scheme="$2"; shift 2 ;;
  --out) out="$2"; shift 2 ;;
  --bin-dir) bin_dir="$2"; shift 2 ;;
  --dcctl) dcctl="$2"; shift 2 ;;
  --treatment) treatment="$2"; shift 2 ;;
  --runs) runs="$2"; shift 2 ;;
  --rate) rate="$2"; shift 2 ;;
  --duration) duration="$2"; shift 2 ;;
  --warmup) warmup="$2"; shift 2 ;;
  --settle) settle="$2"; shift 2 ;;
  --mqtt-rate) mqtt_rate="$2"; shift 2 ;;
  --http-rate) http_rate="$2"; shift 2 ;;
  *) usage_err "unknown argument: $1" ;;
  esac
done
[[ -n "$out" ]] || usage_err "--out is required"
case "$treatment" in triggers | capture | caps | "") ;; *) usage_err "--treatment must be triggers, capture or caps" ;; esac
for n in runs rate duration warmup settle mqtt_rate http_rate; do
  [[ "${!n}" =~ ^[0-9]+$ ]] || usage_err "--${n//_/-} must be a whole number"
done

if [[ "$cmd" == decide ]]; then
  [[ -n "$treatment" ]] || usage_err "--treatment is required"
  rows="$out/$treatment/rows.jsonl"
  [[ -s "$rows" ]] || usage_err "no rows at $rows"
  verdict="$(p1_decide interactiveP99 "$runs" <"$rows")"
  puback="$(p1_decide pubackP99 "$runs" <"$rows")"
  jq -n --argjson gate "$verdict" --argjson puback "$puback" --arg t "$treatment" \
    '{treatment: $t, gate: $gate, puback: $puback}' | tee "$out/$treatment/verdict.json"
  exit "$(p1_exit "$(jq -r .verdict <<<"$verdict")")"
fi

[[ -n "$kubeconfig" && -n "$ns" && -n "$instance" && -n "$apiprobe" && -n "$server" ]] ||
  usage_err "--kubeconfig (a lane-private file), --namespace, --instance, --apiprobe and --server are required"
k() { env -u KUBECONFIG MSYS2_ARG_CONV_EXCL='*' "$KUBECTL" --kubeconfig "$kubeconfig" "$@"; }
mkdir -p "$out"
receipt="$out/receipt.json"
bulk="$out/bulk.json"
api=(--server "$server" --scheme "$scheme" --receipt "$receipt")

pg_pod() {
  k -n dc-system get endpointslices -l "kubernetes.io/service-name=dc-postgresql" -o json |
    jq -r 'first(.items[].endpoints[] | select(.conditions.ready != false) | .targetRef.name // empty)'
}
psql_q() { k -n dc-system exec -i "$pg" -- psql -U postgres -d "$db" -v ON_ERROR_STOP=1 -Atq <<<"$1" | tr -d '\r'; }

forward_dm() {
  local deploy
  deploy="$(k -n "$ns" get deployments -l devicechain.io/functional-area=device-management -o jsonpath='{.items[0].metadata.name}')"
  [[ -n "$deploy" ]] || die "no device-management Deployment in $ns"
  k -n "$ns" port-forward "deployment/$deploy" 18491:8080 >"$out/forward-dm.log" 2>&1 &
  fwd=$!
  for _ in $(seq 1 30); do (exec 3<>/dev/tcp/127.0.0.1/18491) 2>/dev/null && break; sleep 1; done
  (exec 3<>/dev/tcp/127.0.0.1/18491) 2>/dev/null || die "the device-management port-forward never opened"
  dm_url="http://127.0.0.1:18491/graphql"
}

if [[ "$cmd" == seed ]]; then
  [[ -n "${DC_ADMIN_PASSWORD:-}" ]] || usage_err "DC_ADMIN_PASSWORD (the superuser's) is required to create the P-1 tenant"
  # The seed writes to every probe area it is not told is absent, and an instance of the default profile runs
  # no outbound-connectors: name the areas this namespace runs, or the seed fails on the first absent one.
  probe_areas="$("$apiprobe" areas | tr -d '\r')" || die "apiprobe could not list its areas"
  running="$(k -n "$ns" get deployments -o jsonpath='{range .items[*]}{.metadata.labels.devicechain\.io/functional-area}{"\n"}{end}' | tr -d '\r')" ||
    die "could not list the Deployments in $ns"
  deployed="$(p1_deployed_areas "$probe_areas" "$running")"
  [[ -n "$deployed" ]] || die "no probe area runs in $ns"
  [[ -s "$receipt" ]] || "$apiprobe" seed --instance "$instance" --receipt "$receipt" --tenant p1 --server "$server" --scheme "$scheme" \
    --deployed-areas "$deployed" || die "could not create the P-1 tenant"
  forward_dm
  trap 'kill "$fwd" 2>/dev/null || true' EXIT
  [[ -s "$bulk" ]] || "$apiprobe" bulkchange seed "${api[@]}" --url "$dm_url" --out "$bulk" --count 20200 --prefix p1 --workers 32 ||
    die "could not seed the P-1 devices"
  say "seeded: $(jq -r '.devices | length' "$bulk") devices in $(jq -r .tenant "$receipt")"
  exit 0
fi

# ---------------------------------------------------------------------------
# run
# ---------------------------------------------------------------------------
[[ -n "$treatment" ]] || usage_err "--treatment is required"
[[ -s "$receipt" && -s "$bulk" ]] || usage_err "run 'seed' first: $receipt and $bulk are missing"
[[ -n "$bin_dir" ]] || usage_err "--bin-dir is required (Linux dc-loadtest and dc-loadtest-mqtt, as hack/perf-ab.sh wants)"
pg="$(pg_pod)"
[[ -n "$pg" ]] || die "no ready dc-postgresql pod"
db="$(k -n dc-system exec -i "$pg" -- psql -U postgres -d postgres -Atqc \
  "SELECT datname FROM pg_database WHERE datistemplate = false AND datname <> 'postgres'" | tr -d '\r' |
  while read -r d; do
    n="$(k -n dc-system exec "$pg" -- psql -U postgres -d "$d" -Atqc "SELECT count(*) FROM information_schema.tables WHERE table_name = 'fleet_operation_lines'" </dev/null | tr -d '\r')"
    [[ "$n" != 0 ]] && echo "$d"
  done)"
[[ -n "$db" && "$(wc -l <<<"$db")" == 1 ]] || die "found no single database holding fleet_operation_lines ('$db')"
tenant="$(jq -r .tenant "$receipt")"
dir="$out/$treatment"
mkdir -p "$dir"
rows="$dir/rows.jsonl"
forward_dm
caps_tokens=()
sampler="" capture_loop="" capture_file=""

# The guard's triggers as the instance holds them, "name|tgenabled" per line, read once before the first arm.
# An arm turns them off and back on; "back on" means exactly the mode read here. The migration installs the
# guard ENABLE ALWAYS ('A'), and bulk-change activation accepts nothing else: a plain ENABLE ('O') still fires
# for ordinary sessions but reads as not enforced, so restoring with it would leave the instance unable to
# start a bulk change after the A/B, silently.
trigger_modes=""
triggers_read() {
  psql_q "SELECT tgname || '|' || tgenabled::text FROM pg_trigger WHERE tgrelid = '\"device-management\".entity_attributes'::regclass AND NOT tgisinternal AND tgname LIKE 'dc_%attr%' ORDER BY tgname"
}

triggers_disable() {
  local t
  while IFS='|' read -r t _; do
    psql_q "ALTER TABLE \"device-management\".entity_attributes DISABLE TRIGGER \"$t\"" >/dev/null || die "could not disable trigger $t"
  done <<<"$trigger_modes"
}

triggers_restore() {
  local t mode clause now
  while IFS='|' read -r t mode; do
    clause="$(p1_enable_clause "$mode")" || die "trigger $t was in mode '$mode' before the A/B; refusing to guess how to restore it"
    psql_q "ALTER TABLE \"device-management\".entity_attributes $clause TRIGGER \"$t\"" >/dev/null || die "could not restore trigger $t ($clause)"
  done <<<"$trigger_modes"
  now="$(triggers_read)" || die "could not re-read the guard's triggers"
  [[ "$now" == "$trigger_modes" ]] || die "the guard's triggers are not as they were before the A/B: before '$trigger_modes', now '$now'"
}

arm_down() {
  local t
  [[ -n "$sampler" ]] && { kill "$sampler" 2>/dev/null || true; sampler=""; }
  [[ -n "$capture_loop" ]] && { kill "$capture_loop" 2>/dev/null || true; capture_loop=""; }
  for t in "${caps_tokens[@]}"; do
    "$apiprobe" bulkchange abort "${api[@]}" --url "$dm_url" --token "$t" >/dev/null 2>&1 || true
  done
  caps_tokens=()
}

cleanup() {
  arm_down
  [[ "$treatment" == triggers && -n "$trigger_modes" ]] && triggers_restore
  kill "$fwd" 2>/dev/null || true
}
trap cleanup EXIT
if [[ "$treatment" == triggers ]]; then
  trigger_modes="$(triggers_read)" || die "could not read the guard's triggers"
  [[ -n "$trigger_modes" ]] || die "found no attribute guard trigger on entity_attributes"
  while IFS='|' read -r t mode; do
    p1_enable_clause "$mode" >/dev/null || die "trigger $t is in mode '$mode' before the A/B (disabled or unknown); the treatment arm would not be the shipped guard"
  done <<<"$trigger_modes"
fi

arm_up() { # <arm> <run>
  local arm="$1" i="$2" t
  case "$treatment:$arm" in
  triggers:baseline) triggers_disable ;;
  triggers:treatment) triggers_restore ;;
  capture:baseline | caps:baseline) ;;
  capture:treatment)
    local tls_args=()
    [[ -n "${P1_NATS_CA:-}" ]] && tls_args=(--ca "$P1_NATS_CA" --server-name "${P1_NATS_SERVER_NAME:-dc-nats}")
    capture_file="$dir/capture-$i.rounds"
    : >"$capture_file"
    (
      while :; do
        psql_q "SELECT d.token || E'\t' || r.revision || E'\t' || r.digest FROM \"device-management\".devices d
          JOIN LATERAL (SELECT revision, digest FROM \"device-management\".device_configuration_revisions x WHERE x.device_id = d.id
            ORDER BY x.revision DESC LIMIT 1) r ON true
          WHERE d.tenant_id = '$tenant' AND d.token BETWEEN 'p1-00001' AND 'p1-00200' AND d.deleted_at IS NULL" >"$dir/reports.tsv"
        # Every round is recorded: a treatment whose reports never reach the stream measures nothing, and the
        # arm is then refused rather than counted (one_arm reads this file).
        if DC_BC_NATS_PASSWORD="$P1_NATS_PASSWORD" "$apiprobe" bulkchange report --nats "${P1_NATS_URL:?}" --user "${P1_NATS_USER:?}" \
          ${tls_args[@]+"${tls_args[@]}"} --instance-id "$instance" --tenant "$tenant" --rows "$dir/reports.tsv" >"$dir/capture-last.log" 2>&1; then
          echo ok >>"$capture_file"
        else
          echo "fail: $(tail -n 1 "$dir/capture-last.log")" >>"$capture_file"
        fi
        sleep 5
      done
    ) &
    capture_loop=$!
    ;;
  caps:treatment)
    local tok
    for t in 1 2; do
      tok="p1-cap-$i-$t-$(date -u +%H%M%S)"
      "$apiprobe" bulkchange start "${api[@]}" --url "$dm_url" --bulk "$bulk" --token "$tok" --value "cap-$tok" \
        --first $((201 + (t - 1) * 10000)) --last $((200 + t * 10000)) --rate 50 >/dev/null || die "could not start caps change $tok"
      caps_tokens+=("$tok")
    done
    ;;
  esac
}

sample_locks() { # <file>
  while :; do
    printf '%s %s\n' "$(date +%s)" "$(psql_q "SELECT 'waits=' || (SELECT count(*) FROM pg_locks WHERE NOT granted) || ' famhold=' ||
      COALESCE((SELECT max(extract(epoch FROM clock_timestamp() - a.xact_start))::text FROM pg_locks l JOIN pg_stat_activity a USING (pid)
        WHERE l.locktype = 'advisory' AND l.granted AND l.mode = 'ShareLock'), '')" 2>/dev/null || echo 'waits=0 famhold=')" >>"$1"
    sleep 1
  done
}

one_arm() { # <arm> <run>
  local arm="$1" i="$2" name ok=true load_rc=0 ab_rc=0 lat locks puback lockfile
  name="p1-${treatment:0:4}-${arm:0:1}$i"
  say "run $i, $arm ($name)"
  arm_up "$arm" "$i"
  lockfile="$dir/$name.locks"
  : >"$lockfile"
  sample_locks "$lockfile" &
  sampler=$!
  "$apiprobe" bulkchange attrload "${api[@]}" --url "$dm_url" --bulk "$bulk" --first 1 --last 200 --rate "$rate" \
    --duration "$((warmup + duration + settle))s" --out "$dir/$name.latency.json" >"$dir/$name.attrload.log" 2>&1 &
  local p_load=$!
  "$ROOT/hack/perf-ab.sh" --kubeconfig "$kubeconfig" --namespace "$ns" --instance "$instance" --arm "$name" \
    --bin-dir "$bin_dir" --dcctl "$dcctl" --out "$dir/perf-ab" --mqtt-rate "$mqtt_rate" --http-rate "$http_rate" \
    --duration "$duration" --warmup "$warmup" --settle "$settle" --sim-prefix p1 >"$dir/$name.perf-ab.log" 2>&1 || ab_rc=$?
  wait "$p_load" || load_rc=$?
  arm_down
  [[ "$ab_rc" == 0 && "$load_rc" == 0 ]] || ok=false
  local cap_ok=null cap_fail=null
  if [[ "$treatment:$arm" == capture:treatment ]]; then
    cap_ok="$(grep -c '^ok$' "$capture_file" || true)"
    cap_fail="$(grep -c '^fail' "$capture_file" || true)"
    [[ "$cap_ok" -ge 1 && "$cap_fail" == 0 ]] || {
      ok=false
      say "run $i: the capture treatment's reports did not all reach the stream ($cap_ok round(s) published, $cap_fail failed): $(grep -m1 '^fail' "$capture_file")"
    }
  fi
  lat="$(cat "$dir/$name.latency.json" 2>/dev/null || echo '{}')"
  locks="$(p1_lock_summary <"$lockfile")"
  puback="$(jq -c '.drive.ackLatency // {}' "$dir/perf-ab/$name/raw/report-mqtt.json" 2>/dev/null || echo '{}')"
  jq -cn --arg arm "$arm" --argjson run "$i" --argjson ok "$ok" --argjson lat "$lat" --argjson locks "$locks" \
    --argjson puback "$puback" --argjson ab "$ab_rc" --argjson load "$load_rc" --argjson capok "$cap_ok" --argjson capfail "$cap_fail" '
    {arm: $arm, run: $run, ok: $ok, perfAbExit: $ab, loadExit: $load,
     interactiveP50: $lat.p50Ms, interactiveP99: $lat.p99Ms, interactiveErrors: $lat.errors,
     pubackP50: $puback.p50Ms, pubackP99: $puback.p99Ms, captureRounds: $capok, captureFailures: $capfail} + $locks' >>"$rows"
  tail -n 1 "$rows" >&2
}

[[ "$treatment" != capture || -n "${P1_NATS_URL:-}" ]] ||
  usage_err "the capture treatment publishes reports: set P1_NATS_URL (a port-forward to the broker), P1_NATS_USER and P1_NATS_PASSWORD (event-sources' login), and P1_NATS_CA (a PEM file; P1_NATS_SERVER_NAME, default dc-nats) when the broker serves TLS"
for i in $(seq 1 "$runs"); do
  one_arm baseline "$i"
  one_arm treatment "$i"
done
trap - EXIT
cleanup
rc=0
"$0" decide --out "$out" --treatment "$treatment" --runs "$runs" || rc=$?
exit "$rc"
