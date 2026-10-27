#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The calculated-metrics cost gate: the measurement that decides whether calculated metrics can
# be on by default, and at what per-profile count. It authors N formulas on the sim profile,
# drives a load command, and records what the platform did, one row per (arm, run).
#
#   C0      0 formulas                       at the C0 rate (e.g. 10000 readings/s)
#   C1      1 formula                        at the C0 rate
#   C4      4 formulas                       at the C0 rate
#   C8-eq   8 formulas   at the rate that stores the SAME rows/s as C0  (see `plan`)
#   C4-eq   4 formulas   the fallback if C8-eq fails: same rows/s as C0, at 4 formulas
#   B0      0 formulas   optional: the same build with no calculated profile, to bound C0's noise
#   C8-knee 8 formulas   stepped up from C8-eq until consumer lag starts to grow
#
# WHAT THE GATE ASKS is not "can an instance take 9x its rows". It is whether a DERIVED row costs
# what a RAW row costs. That is why C8-eq holds stored rows/s equal to C0 and compares cost per
# stored row, and why C8-knee only reports a capacity.
#
# 🔴 A NUMBER FROM AN ARM THAT DID NOT COMPUTE WHAT ITS LABEL SAYS IS WORTH NOTHING. So:
#   1. `control` runs before anything else and is the harness's negative control: with the tier at
#      0 a publish of 8 formulas MUST be refused. If it is not, the harness is measuring a
#      formula-free profile under another label; it exits 3, writes control.failed, and `arm`/`run`
#      refuse to continue until a later `control` passes.
#   2. `run` checks each arm after the load: calcmetric_evaluations_total moved (and
#      calcmetric_skipped_total did not) for N>0 and did not move for N=0; the newest stored
#      instants hold exactly raws+N rows each; and, when --accepted is given, the stored row count
#      in the window equals accepted x (raws+N). A failed check exits 4 and the row is written
#      with ok=false, which tabulate.py never lets pass.
#
# 🔴 dc-loadtest / dc-loadtest-mqtt COUNT EVERY STORED MEASUREMENT ROW, derived ones included, so on
# an N>0 arm their own persisted-beyond-accepted invariant reports an excess. That is expected,
# not a loss: the harness records the load command's exit code (load_rc) and does NOT abort on it
# for N>0, and replaces the loss check with the exact one above (--accepted). Pass the number of
# events the load command PUBACKed/accepted as --accepted; without it the arm is "unverified" and
# cannot pass. On an N=0 arm a non-zero load_rc fails the arm as usual.
#
# THE KUBE CONTEXT IS NEVER TOUCHED. Every kubectl call carries --kubeconfig <file> explicitly,
# and the script refuses to run without one (--kubeconfig, or KUBECONFIG pointing at an existing
# file). It never calls `kubectl config use-context`. Give each lane its own file.
#
# USAGE
#   export DC_ADMIN_EMAIL=... DC_ADMIN_PASSWORD=...   # a superuser: tier edits are /admin/graphql
#   hack/calc-cost-gate.sh plan    --c0-rate 10000 [--raws 1]
#   hack/calc-cost-gate.sh control --handshake H --kubeconfig K
#   hack/calc-cost-gate.sh arm     --arm C4 --handshake H --kubeconfig K       # tier + draft + publish
#   hack/calc-cost-gate.sh run     --arm C4 --run 1 --rate 10000 --accepted 6000000 \
#         --handshake H --kubeconfig K --namespace NS [--raws 1] [--out DIR] \
#         [--prom-url URL --prom name='PROMQL with $WINDOW'] [--metric name=value ...] \
#         -- ./dc-loadtest-mqtt --handshake H --duration 10m ...
#   hack/calc-cost-gate.sh restore --handshake H                               # tier config back
#   hack/calc-cost-gate.sh table   [--out DIR]                                 # tabulate.py
#   hack/calc-cost-gate.sh self-test
#
#   H  the sim record `dcctl sim create` wrote (tenant, sim login, endpoints, adminURL). The sim
#      profile devicepulse-profile (metric speed_kph) must already exist: provision it by
#      running the load command once (a short --duration), then wait 30 s (its clean-tenant check).
#   `arm` sets the tenant's tier ceiling to --ceiling (default 8: the platform default is still 0
#   until the gate passes), reconciles the draft to exactly N formulas, and publishes. The tier
#   change reaches device-management within about a minute, so publish is retried for --propagate
#   seconds (default 150); `control` waits the whole window before it tries, so a stale cached
#   ceiling can only make the control fail loudly, never pass falsely.
#   The tier edit is a MERGE of one key into the tier's existing config (the original is saved in
#   DIR/tier-<token>.orig.json once, and `restore` writes it back).
#   Every `run` appends one JSON object to DIR/results.jsonl (default ./calc-cost-gate-out);
#   `table` / tabulate.py turns that into the verdicts. Metrics the harness cannot read itself
#   (throughput, p99, EM CPU, lag, bytes) come in through --prom or --metric under the names
#   tabulate.py documents. Pod metrics are read through the kube API (no port-forward), from every
#   device-management pod.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SELF="$ROOT/hack/$(basename "${BASH_SOURCE[0]}")"
TABULATE="$ROOT/hack/calc-cost-gate/tabulate.py"

PROFILE="devicepulse-profile"
RAW_KEY="speed_kph"
KEY_PREFIX="g21_calc_"
# Eight formulas, all over the profile's one raw key, so the sim's sample carries ONE raw key and
# the amplification is the worst case: raws + N stored rows per event. They are cheap on purpose;
# the gate measures what a stored row costs, not what CEL costs (the Calc benchmarks do that).
FORMULAS=(
  "speed_kph * 0.621371"
  "speed_kph / 3.6"
  "speed_kph * 0.539957"
  "speed_kph * speed_kph / 100.0"
  "speed_kph + 10.0"
  "speed_kph - 5.0"
  "speed_kph * 1.1"
  "speed_kph / 2.0"
)

# jq built for Windows ends every line with CRLF, which turns a "$(jq -r ...)" token into one
# with a stray carriage return. Strip it; the status of jq itself still propagates (pipefail).
jq() { command jq "$@" | tr -d '\r'; }

die() { echo "calc-cost-gate: $*" >&2; exit "${DIE_RC:-1}"; }
log() { echo "calc-cost-gate: $*" >&2; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required on PATH"; }
usage() { sed -n '4,64p' "$SELF" | sed 's/^# \{0,1\}//'; exit 2; }

# ---- the pure checks (self-tested) --------------------------------------------------------

# tier_merge VALUE: stdin = the tier's config JSON string (or empty/null). Prints the merged config
# object with calculatedMetricsPerProfile set and every other key kept.
tier_merge() {
  local raw
  raw="$(cat)"
  if [[ -z "$raw" || "$raw" == null ]]; then raw='{}'; fi
  jq -c --argjson v "$1" '. + {calculatedMetricsPerProfile: $v}' <<<"$raw"
}

# rows_check WANT N: stdin = measurementEvents results JSON array [{deviceToken,occurredTime,name}].
# Prints "<complete instants> <bad instants>". The oldest instant is dropped because the page may
# have cut it. A complete instant is good only when it holds WANT rows, all under distinct names,
# exactly N of them formula keys.
rows_check() {
  jq -r --argjson want "$1" --arg prefix "$KEY_PREFIX" --argjson n "$2" '
    group_by([.deviceToken, .occurredTime]) | sort_by(.[0].occurredTime) | .[1:] as $g
    | ($g | map(select(
          ((map(.name) | unique | length) != $want)
          or ((map(.name) | unique | map(select(startswith($prefix))) | length) != $n)
          or (length != (map(.name) | unique | length))))) as $bad
    | "\($g | length) \($bad | length)"'
}

# counters_check N EVAL_DELTA SKIP_DELTA: prints a reason and returns 1 when the counters
# contradict the arm.
counters_check() {
  local n="$1" ev="$2" sk="$3"
  if [[ "$n" -eq 0 ]]; then
    [[ "$ev" -eq 0 ]] || { echo "N=0 but calcmetric_evaluations_total moved by $ev (the profile still computes formulas)"; return 1; }
    return 0
  fi
  [[ "$ev" -gt 0 ]] || { echo "N=$n but calcmetric_evaluations_total did not move (nothing computed: this arm measured a formula-free profile)"; return 1; }
  [[ "$sk" -eq 0 ]] || { echo "calcmetric_skipped_total moved by $sk (formulas skipped; the arm is not what its label says)"; return 1; }
  return 0
}

# ---- argument parsing ---------------------------------------------------------------------

SUB="${1:-}"
[[ -n "$SUB" ]] || usage
shift || true
HANDSHAKE="" KCFG="" NS="" ARM="" RUN="1" RATE="" ACCEPTED="" RAWS="1" OUT="./calc-cost-gate-out"
CEILING=8 PROPAGATE=150 SETTLE=300 PROM_URL="" C0_RATE=""
declare -a PROMS=() METRICS=() LOADCMD=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --handshake) HANDSHAKE="$2"; shift 2 ;;
    --kubeconfig) KCFG="$2"; shift 2 ;;
    --namespace) NS="$2"; shift 2 ;;
    --arm) ARM="$2"; shift 2 ;;
    --run) RUN="$2"; shift 2 ;;
    --rate) RATE="$2"; shift 2 ;;
    --accepted) ACCEPTED="$2"; shift 2 ;;
    --raws) RAWS="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    --ceiling) CEILING="$2"; shift 2 ;;
    --propagate) PROPAGATE="$2"; shift 2 ;;
    --settle) SETTLE="$2"; shift 2 ;;
    --prom-url) PROM_URL="$2"; shift 2 ;;
    --prom) PROMS+=("$2"); shift 2 ;;
    --metric) METRICS+=("$2"); shift 2 ;;
    --c0-rate) C0_RATE="$2"; shift 2 ;;
    --) shift; LOADCMD=("$@"); break ;;
    -h|--help) usage ;;
    *) die "unknown argument: $1" ;;
  esac
done

# arm_formulas ARM: how many formulas an arm authors.
arm_formulas() {
  case "$1" in
    C0|B0) echo 0 ;; C1) echo 1 ;; C4|C4-eq) echo 4 ;; C8-eq|C8-knee|C8) echo 8 ;;
    *) die "unknown arm '$1' (B0, C0, C1, C4, C4-eq, C8-eq, C8-knee)" ;;
  esac
}

# ---- self-test ----------------------------------------------------------------------------
# A check is worth nothing until it has been seen to fail, so every case below has a twin that
# must be refused.
self_test() {
  need jq; need python3
  local rc=0 out good bad
  t() { echo "self-test: $*" >&2; rc=1; }

  out="$(echo '{"shedPriority":2,"calculatedMetricsPerProfile":0}' | tier_merge 8)"
  [[ "$out" == '{"shedPriority":2,"calculatedMetricsPerProfile":8}' ]] || t "tier_merge lost or kept the wrong keys: $out"
  out="$(echo 'null' | tier_merge 8)"
  [[ "$out" == '{"calculatedMetricsPerProfile":8}' ]] || t "tier_merge of a null config: $out"
  out="$(echo '' | tier_merge 8)"
  [[ "$out" == '{"calculatedMetricsPerProfile":8}' ]] || t "tier_merge of an empty config: $out"

  counters_check 8 1000 0 >/dev/null || t "counters_check refused a good N=8 arm"
  counters_check 0 0 0 >/dev/null || t "counters_check refused a good N=0 arm"
  if counters_check 8 0 0 >/dev/null; then t "counters_check admitted an N=8 arm that computed nothing (the label lies)"; fi
  if counters_check 8 1000 3 >/dev/null; then t "counters_check admitted skipped formulas"; fi
  if counters_check 0 5 0 >/dev/null; then t "counters_check admitted evaluations on an N=0 arm"; fi

  good='[]'
  for i in 1 2 3; do
    good="$(jq -c --arg t "2026-10-27T10:00:0${i}Z" '. + [{deviceToken:"d1",occurredTime:$t,name:"speed_kph"},{deviceToken:"d1",occurredTime:$t,name:"g21_calc_1"},{deviceToken:"d1",occurredTime:$t,name:"g21_calc_2"}]' <<<"$good")"
  done
  out="$(rows_check 3 2 <<<"$good")"
  [[ "$out" == "2 0" ]] || t "rows_check on good rows: '$out'"
  # twin: a formula row missing from a complete instant must be counted bad
  bad="$(jq -c 'del(.[4])' <<<"$good")"
  out="$(rows_check 3 2 <<<"$bad")"
  [[ "$out" == "2 1" ]] || t "rows_check admitted an instant missing a derived row: '$out'"
  # twin: raw-only instants (a formula-free profile) must all be counted bad for N=2
  bad="$(jq -c '[.[] | select(.name == "speed_kph")]' <<<"$good")"
  out="$(rows_check 3 2 <<<"$bad")"
  [[ "$out" == "2 2" ]] || t "rows_check admitted raw-only instants for N=2: '$out'"

  if [[ -f "$TABULATE" ]]; then
    python3 -I "$TABULATE" --self-test >&2 || rc=1
  else
    t "tabulate.py is missing"
  fi
  # twin: a duplicated row inside an instant (a derived row stored twice) must be counted bad
  bad="$(jq -c '. + [.[4]]' <<<"$good")"
  out="$(rows_check 3 2 <<<"$bad")"
  [[ "$out" == "2 1" ]] || t "rows_check admitted a duplicated row: '$out'"

  if [[ "$rc" -eq 0 ]]; then echo "calc-cost-gate self-test: ok"; fi
  return "$rc"
}

if [[ "$SUB" == self-test ]]; then self_test; exit $?; fi

if [[ "$SUB" == plan ]]; then
  [[ -n "$C0_RATE" ]] || die "plan needs --c0-rate"
  need python3
  python3 -I - "$C0_RATE" "$RAWS" <<'PY'
import sys
rate, raws = float(sys.argv[1]), int(sys.argv[2])
print("# readings/s per arm, holding stored rows/s equal to C0 (raws per sample = %d)" % raws)
for arm, n in (("C0", 0), ("C1", 1), ("C4", 4), ("C4-eq", 4), ("C8-eq", 8)):
    r = rate if not arm.endswith("-eq") else rate * raws / (raws + n)
    print("%-8s N=%d  rate=%.0f readings/s  stored=%.0f rows/s" % (arm, n, r, r * (raws + n)))
print("C8-knee  N=8  step up from the C8-eq rate by x1.5 until EM lag starts to grow")
PY
  exit 0
fi

if [[ "$SUB" == table ]]; then
  need python3
  exec python3 -I "$TABULATE" "$OUT/results.jsonl"
fi

# ---- everything below talks to a cluster --------------------------------------------------

need jq; need curl; need python3
[[ -n "$HANDSHAKE" && -f "$HANDSHAKE" ]] || die "--handshake FILE (the dcctl sim record) is required"
HS="$(cat "$HANDSHAKE")"
hs() { jq -er "$1" <<<"$HS"; }
TENANT="$(hs .tenant)"; SIM_EMAIL="$(hs .simEmail)"; SIM_PASSWORD="$(hs .simPassword)"
USER_GQL="$(hs .endpoints.userGraphQL)"; DM_GQL="$(hs .endpoints.deviceMgmtGraphQL)"
EM_WS="$(hs .endpoints.eventMgmtWS)"; ADMIN_URL="$(hs .adminURL)"
EM_GQL="${EM_WS/#ws:/http:}"; EM_GQL="${EM_GQL/#wss:/https:}"
mkdir -p "$OUT"
STATE="$OUT"

# gql runs inside $(...) at every call site, so a variable it set would die with the subshell:
# the last error's code and message go to a file instead (gql_code / gql_err read it back).
GQL_ERRFILE="$(mktemp)"
trap 'rm -f "$GQL_ERRFILE"' EXIT
gql_code() { sed -n 1p "$GQL_ERRFILE"; }
gql_err() { sed -n 2p "$GQL_ERRFILE"; }
# gql URL TOKEN QUERY [VARS]: POST, print the body; return 1 on a GraphQL error, 2 on a
# transport failure. Either way the error is left in GQL_ERRFILE; a success empties it.
gql() {
  local url="$1" tok="$2" q="$3" v="${4:-}" body resp
  local -a auth=()
  : >"$GQL_ERRFILE"
  [[ -n "$v" ]] || v='{}'
  [[ -z "$tok" ]] || auth=(-H "Authorization: Bearer $tok")
  body="$(jq -nc --arg q "$q" --argjson v "$v" '{query:$q,variables:$v}')"
  if ! resp="$(curl -sS -m 60 -H 'Content-Type: application/json' "${auth[@]}" --data-binary "$body" "$url")"; then
    printf 'TRANSPORT
transport failure talking to %s
' "$url" >"$GQL_ERRFILE"
    return 2
  fi
  if jq -e '.errors' <<<"$resp" >/dev/null 2>&1; then
    jq -r '(.errors[0].extensions.code // "NONE"), .errors[0].message' <<<"$resp" >"$GQL_ERRFILE"
    printf '%s' "$resp"
    return 1
  fi
  if ! jq -e '.data' <<<"$resp" >/dev/null 2>&1; then
    printf 'BADRESPONSE
response from %s is not a GraphQL result
' "$url" >"$GQL_ERRFILE"
    return 2
  fi
  printf '%s' "$resp"
}

LOGIN='mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}'
SELECT='mutation($t:String!,$n:String!){selectTenant(identityToken:$t,tenant:$n){accessToken}}'

ADMIN_TOKEN=""
admin_token() {
  [[ -n "${DC_ADMIN_EMAIL:-}" && -n "${DC_ADMIN_PASSWORD:-}" ]] || die "set DC_ADMIN_EMAIL and DC_ADMIN_PASSWORD (a superuser)"
  local r
  r="$(gql "$USER_GQL" "" "$LOGIN" "$(jq -nc --arg e "$DC_ADMIN_EMAIL" --arg p "$DC_ADMIN_PASSWORD" '{e:$e,p:$p}')")" \
    || die "admin login failed: $(gql_err) ($(gql_code); a second factor is not supported by this harness)"
  jq -er '.data.login.identityToken' <<<"$r"
}
tenant_token() {
  local r id
  r="$(gql "$USER_GQL" "" "$LOGIN" "$(jq -nc --arg e "$SIM_EMAIL" --arg p "$SIM_PASSWORD" '{e:$e,p:$p}')")" \
    || die "sim login failed: $(gql_err)"
  id="$(jq -er '.data.login.identityToken' <<<"$r")"
  r="$(gql "$USER_GQL" "" "$SELECT" "$(jq -nc --arg t "$id" --arg n "$TENANT" '{t:$t,n:$n}')")" \
    || die "selectTenant failed: $(gql_err)"
  jq -er '.data.selectTenant.accessToken' <<<"$r"
}

# k ARGS...: kubectl against the lane's own kubeconfig, always explicit.
k() {
  if [[ -z "$KCFG" && -n "${KUBECONFIG:-}" ]]; then KCFG="${KUBECONFIG%%:*}"; fi
  [[ -n "$KCFG" && -f "$KCFG" ]] || die "a lane-private kubeconfig is required (--kubeconfig FILE); this harness never uses the shared current-context"
  kubectl --kubeconfig "$KCFG" "$@"
}

# ---- tier ---------------------------------------------------------------------------------

TIER="" TIER_CFG=""
resolve_tier() {
  local r
  ADMIN_TOKEN="$(admin_token)"
  r="$(gql "$ADMIN_URL" "$ADMIN_TOKEN" 'query{tenants{token tier{token config}}}')" || die "tenants query failed: $(gql_err)"
  TIER="$(jq -er --arg t "$TENANT" '.data.tenants[] | select(.token==$t) | .tier.token' <<<"$r")" || die "tenant $TENANT not found"
  TIER_CFG="$(jq -r --arg t "$TENANT" '.data.tenants[] | select(.token==$t) | .tier.config // ""' <<<"$r")"
}

# set_tier_ceiling VALUE: merge calculatedMetricsPerProfile into the tenant's tier config.
set_tier_ceiling() {
  resolve_tier
  local orig="$STATE/tier-$TIER.orig.json" merged r got
  if [[ ! -f "$orig" ]]; then printf '%s' "$TIER_CFG" >"$orig"; log "saved tier $TIER's original config to $orig"; fi
  merged="$(printf '%s' "$TIER_CFG" | tier_merge "$1")"
  r="$(gql "$ADMIN_URL" "$ADMIN_TOKEN" 'mutation($t:String!,$c:String){updateTenantTier(token:$t,request:{config:$c}){token config}}' \
        "$(jq -nc --arg t "$TIER" --arg c "$merged" '{t:$t,c:$c}')")" || die "updateTenantTier failed: $(gql_err)"
  got="$(jq -er '.data.updateTenantTier.config | fromjson | .calculatedMetricsPerProfile' <<<"$r")"
  [[ "$got" == "$1" ]] || die "tier $TIER reads back calculatedMetricsPerProfile=$got, wanted $1"
  log "tier $TIER (tenant $TENANT): calculatedMetricsPerProfile=$1"
}

# ---- the draft ----------------------------------------------------------------------------

DM_TOKEN=""
list_calc() {
  gql "$DM_GQL" "$DM_TOKEN" 'query($p:String){metricDefinitions(criteria:{pageNumber:1,pageSize:200,deviceProfile:$p}){results{token metricKey expression}}}' \
    "$(jq -nc --arg p "$PROFILE" '{p:$p}')" || die "metricDefinitions failed: $(gql_err)"
}

# author_formulas N: make the draft hold exactly g21_calc_1..N (and no other calculated metric).
author_formulas() {
  local n="$1" r i key tok foreign
  r="$(list_calc)"
  jq -e --arg k "$RAW_KEY" '[.data.metricDefinitions.results[] | select(.metricKey==$k)] | length == 1' <<<"$r" >/dev/null \
    || die "profile $PROFILE has no metric $RAW_KEY: run the load command once (short --duration) so the sim provisions it, then wait 30 s"
  foreign="$(jq -r --arg p "$KEY_PREFIX" '[.data.metricDefinitions.results[] | select(.expression != null and (.metricKey|startswith($p)|not)) | .metricKey] | join(",")' <<<"$r")"
  [[ -z "$foreign" ]] || die "the profile carries calculated metrics this harness did not author ($foreign); refusing to measure over them"
  for tok in $(jq -r --arg p "$KEY_PREFIX" '.data.metricDefinitions.results[] | select(.expression != null and (.metricKey|startswith($p))) | .token' <<<"$r"); do
    key="${tok#md-}"
    i="${key#"$KEY_PREFIX"}"
    if [[ "$i" -gt "$n" ]]; then
      gql "$DM_GQL" "$DM_TOKEN" 'mutation($t:String!){deleteMetricDefinition(token:$t)}' "$(jq -nc --arg t "$tok" '{t:$t}')" >/dev/null \
        || die "deleteMetricDefinition $tok failed: $(gql_err)"
    fi
  done
  for ((i = 1; i <= n; i++)); do
    key="$KEY_PREFIX$i"
    if jq -e --arg k "$key" '[.data.metricDefinitions.results[] | select(.metricKey==$k)] | length > 0' <<<"$r" >/dev/null; then continue; fi
    gql "$DM_GQL" "$DM_TOKEN" 'mutation($r:MetricDefinitionCreateRequest){createMetricDefinition(request:$r){token}}' \
      "$(jq -nc --arg t "md-$key" --arg p "$PROFILE" --arg k "$key" --arg e "${FORMULAS[$((i - 1))]}" \
         '{r:{token:$t,deviceProfileToken:$p,metricKey:$k,dataType:"DOUBLE",expression:$e}}')" >/dev/null \
      || die "createMetricDefinition $key failed: $(gql_err)"
  done
  log "draft holds $n calculated metric(s) on $PROFILE"
}

# publish_once: prints the new version; returns 1 on a refusal (see gql_code / gql_err).
publish_once() {
  local r
  r="$(gql "$DM_GQL" "$DM_TOKEN" 'mutation($t:String!){publishDeviceProfile(token:$t){version}}' "$(jq -nc --arg t "$PROFILE" '{t:$t}')")" || return 1
  jq -er '.data.publishDeviceProfile.version' <<<"$r"
}

# ---- subcommands --------------------------------------------------------------------------

control_ok() { [[ -f "$STATE/control.ok" && ! -f "$STATE/control.failed" ]]; }

cmd_control() {
  local v
  DM_TOKEN="$(tenant_token)"
  rm -f "$STATE/control.ok" "$STATE/control.failed" "$STATE/armed"
  set_tier_ceiling 0
  log "negative control: waiting ${PROPAGATE}s for the tier change to reach device-management"
  sleep "$PROPAGATE"
  author_formulas 8
  if v="$(publish_once)"; then
    echo "version $v published 8 formulas under a tier at 0" >"$STATE/control.failed"
    DIE_RC=3 die "NEGATIVE CONTROL FAILED: a publish of 8 calculated metrics was ACCEPTED (version $v) with the tier ceiling at 0. The harness cannot tell a calculated profile from a formula-free one; nothing it measures can be believed. Stopping."
  fi
  if [[ "$(gql_code)" != LIMIT_EXCEEDED ]]; then
    echo "refused for the wrong reason: $(gql_code): $(gql_err)" >"$STATE/control.failed"
    DIE_RC=3 die "NEGATIVE CONTROL INCONCLUSIVE: the publish was refused, but with '$(gql_code)' ($(gql_err)), not LIMIT_EXCEEDED."
  fi
  now >"$STATE/control.ok"
  log "negative control passed: the publish was refused LIMIT_EXCEEDED ($(gql_err))"
}

cmd_arm() {
  [[ -n "$ARM" ]] || die "--arm is required"
  local n v deadline
  n="$(arm_formulas "$ARM")"
  control_ok || die "the negative control has not passed on this state dir ($STATE): run 'control' first"
  DM_TOKEN="$(tenant_token)"
  set_tier_ceiling "$CEILING"
  author_formulas "$n"
  deadline=$((SECONDS + PROPAGATE))
  while true; do
    if v="$(publish_once)"; then break; fi
    if [[ "$n" -gt 0 && "$(gql_code)" == LIMIT_EXCEEDED && "$SECONDS" -lt "$deadline" ]]; then
      log "publish still refused ($(gql_err)); the tier change has not propagated, retrying"
      sleep 10; continue
    fi
    die "publish failed: $(gql_code): $(gql_err)"
  done
  echo "$ARM $n $v" >"$STATE/armed"
  log "arm $ARM armed: $n formula(s), profile version $v"
}

# dm_counter NAME: the sum of a counter over every device-management pod, through the kube API.
dm_counter() {
  [[ -n "$NS" ]] || die "--namespace is required"
  local pods p total=0 v
  pods="$(k -n "$NS" get pods -l devicechain.io/functional-area=device-management -o jsonpath='{.items[*].metadata.name}')"
  [[ -n "$pods" ]] || die "no device-management pods in $NS"
  for p in $pods; do
    v="$(k -n "$NS" get --raw "/api/v1/namespaces/$NS/pods/$p:8080/proxy/metrics" \
        | awk -v n="$1" '$1 !~ /^#/ && $1 ~ ("(^|_)" n "(\\{|$)") {s += $2} END {printf "%.0f", s}')" \
        || die "could not read /metrics of $p"
    total=$((total + v))
  done
  echo "$total"
}

now() { date -u +%FT%TZ; }
epoch() { date -u +%s; }
iso() { date -u -d "@$1" +%FT%TZ; }

TENANT_TOKEN=""
# stored_rows FROM TO: measurement rows stored in [FROM, TO] for the tenant.
stored_rows() {
  local r
  r="$(gql "$EM_GQL" "$TENANT_TOKEN" 'query($c:EventSearchCriteria!){events(criteria:$c){pagination{totalRecords}}}' \
      "$(jq -nc --arg a "$1" --arg b "$2" '{c:{pageNumber:1,pageSize:1,eventTypes:[2],startTime:$a,endTime:$b}}')")" \
    || die "events count failed: $(gql_err)"
  jq -er '.data.events.pagination.totalRecords' <<<"$r"
}

cmd_run() {
  [[ -n "$ARM" ]] || die "--arm is required"
  [[ ${#LOADCMD[@]} -gt 0 ]] || die "give the load command after --"
  local n armed_arm armed_n armed_v
  n="$(arm_formulas "$ARM")"
  control_ok || die "the negative control has not passed on this state dir ($STATE): run 'control' first"
  [[ -f "$STATE/armed" ]] || die "no arm is armed: run 'arm' first"
  read -r armed_arm armed_n armed_v <"$STATE/armed"
  [[ "$armed_n" == "$n" ]] || die "the armed profile holds $armed_n formula(s) (arm $armed_arm, version $armed_v), not $n for $ARM: run 'arm --arm $ARM'"
  TENANT_TOKEN="$(tenant_token)"
  local ev0 sk0 ev1 sk1 t0 t1 rc=0 e0 e1 ok=true why="" reason evd skd wall
  ev0="$(dm_counter calcmetric_evaluations_total)"; sk0="$(dm_counter calcmetric_skipped_total)"
  t0="$(now)"; e0="$(epoch)"
  log "arm $ARM run $RUN: starting the load command"
  "${LOADCMD[@]}" || rc=$?
  t1="$(now)"; e1="$(epoch)"
  ev1="$(dm_counter calcmetric_evaluations_total)"; sk1="$(dm_counter calcmetric_skipped_total)"
  evd=$((ev1 - ev0)); skd=$((sk1 - sk0)); wall=$((e1 - e0))

  if [[ "$rc" -ne 0 ]]; then
    if [[ "$n" -eq 0 ]]; then ok=false; why="the load command exited $rc on a formula-free arm"
    else log "load command exited $rc: on an N>0 arm its persisted-beyond-accepted invariant counts derived rows; relying on the exact row check instead"; fi
  fi
  if ! reason="$(counters_check "$n" "$evd" "$skd")"; then ok=false; why="${why:+$why; }$reason"; fi

  # The newest stored instants: every complete one must hold raws + N rows.
  local page rowsres inst bad
  page="$(gql "$EM_GQL" "$TENANT_TOKEN" 'query($c:EventSearchCriteria!){measurementEvents(criteria:$c){results{deviceToken occurredTime name}}}' \
        "$(jq -nc --arg a "$(iso "$((e1 - 60))")" '{c:{pageNumber:1,pageSize:400,startTime:$a}}')")" \
    || die "measurementEvents failed: $(gql_err)"
  rowsres="$(jq -c '.data.measurementEvents.results' <<<"$page" | rows_check "$((RAWS + n))" "$n")"
  inst="${rowsres% *}"; bad="${rowsres#* }"
  if [[ "$inst" -lt 3 ]]; then ok=false; why="${why:+$why; }only $inst complete stored instant(s) in the last 60 s: nothing to check the row shape against"
  elif [[ "$bad" -ne 0 ]]; then ok=false; why="${why:+$why; }$bad of $inst stored instants do not hold exactly $((RAWS + n)) rows"; fi

  # The exact stored-row count, once it settles.
  local stored="null" want="null" rows_ok=null
  if [[ -n "$ACCEPTED" ]]; then
    want=$((ACCEPTED * (RAWS + n)))
    local prev=-1 cur from to deadline=$((SECONDS + SETTLE))
    from="$(iso "$((e0 - 5))")"
    while true; do
      to="$(iso "$(($(epoch) + 5))")"
      cur="$(stored_rows "$from" "$to")"
      if [[ "$cur" == "$prev" && "$cur" -ge "$want" ]] || [[ "$SECONDS" -ge "$deadline" ]]; then break; fi
      prev="$cur"; sleep 10
    done
    stored="$cur"
    if [[ "$stored" -eq "$want" ]]; then rows_ok=true; else
      rows_ok=false; ok=false
      why="${why:+$why; }stored $stored rows in the window, wanted accepted x (raws+N) = $ACCEPTED x $((RAWS + n)) = $want"
    fi
  else
    log "no --accepted: the stored-row count is UNVERIFIED and tabulate.py will not let this arm pass"
  fi

  # Optional Prometheus reads, then free-form metrics.
  local extra='{}' spec name expr val
  for spec in ${PROMS[@]+"${PROMS[@]}"}; do
    [[ -n "$PROM_URL" ]] || die "--prom needs --prom-url"
    name="${spec%%=*}"; expr="${spec#*=}"; expr="${expr//\$WINDOW/${wall}s}"
    if ! val="$(curl -sS -m 60 -G --data-urlencode "query=$expr" --data-urlencode "time=$e1" "$PROM_URL/api/v1/query" \
          | jq -er '.data.result[0].value[1] | tonumber')"; then
      log "prometheus query '$name' returned no value"; val=null
    fi
    extra="$(jq -c --arg k "$name" --argjson v "$val" '. + {($k): $v}' <<<"$extra")"
  done
  for spec in ${METRICS[@]+"${METRICS[@]}"}; do
    extra="$(jq -c --arg k "${spec%%=*}" --argjson v "${spec#*=}" '. + {($k): $v}' <<<"$extra")" || die "--metric $spec is not name=<number>"
  done

  jq -nc --arg arm "$ARM" --argjson run "$RUN" --argjson n "$n" --argjson raws "$RAWS" \
        --arg t0 "$t0" --arg t1 "$t1" --argjson wall "$wall" --argjson rc "$rc" \
        --argjson evd "$evd" --argjson skd "$skd" --argjson inst "$inst" --argjson bad "$bad" \
        --argjson rate "${RATE:-null}" --argjson accepted "${ACCEPTED:-null}" \
        --argjson stored "$stored" --argjson want "$want" --argjson rows_ok "$rows_ok" \
        --argjson ok "$ok" --arg why "$why" --argjson extra "$extra" \
        '{arm:$arm,run:$run,formulas:$n,raws:$raws,start:$t0,end:$t1,wall_s:$wall,load_rc:$rc,
          eval_delta:$evd,skip_delta:$skd,instants_checked:$inst,instants_bad:$bad,
          readings_per_s:$rate,accepted:$accepted,stored_rows:$stored,expected_rows:$want,rows_ok:$rows_ok,
          ok:$ok,why:$why} + $extra' >>"$OUT/results.jsonl"
  if [[ "$ok" != true ]]; then DIE_RC=4 die "arm $ARM run $RUN FAILED its checks: $why (row written with ok=false)"; fi
  log "arm $ARM run $RUN ok: evaluations +$evd, skipped +$skd, $inst instants hold $((RAWS + n)) rows"
}

cmd_restore() {
  resolve_tier
  local orig="$STATE/tier-$TIER.orig.json" cfg
  [[ -f "$orig" ]] || die "no saved original at $orig"
  cfg="$(cat "$orig")"
  gql "$ADMIN_URL" "$ADMIN_TOKEN" 'mutation($t:String!,$c:String){updateTenantTier(token:$t,request:{config:$c}){token}}' \
        "$(jq -nc --arg t "$TIER" --arg c "$cfg" '{t:$t,c:$c}')" >/dev/null || die "restore failed: $(gql_err)"
  log "tier $TIER restored to its original config"
}

case "$SUB" in
  control) cmd_control ;;
  arm) cmd_arm ;;
  run) cmd_run ;;
  restore) cmd_restore ;;
  *) die "unknown subcommand '$SUB' (plan, control, arm, run, restore, table, self-test)" ;;
esac
