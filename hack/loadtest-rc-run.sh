#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The release-candidate performance run: the HTTP rung, the MQTT rung and the DETECT oracle, all
# at once, for one duration, each on its OWN tenant.
#
#   dc-loadtest            HTTP ingress at a target rate, persisted == accepted, identity,
#                          live state (the L1 oracle)
#   dc-loadtest-mqtt       one MQTT session per device, QoS 1 over TLS; every PUBACKed event
#                          read back by identity, and a loss exits 3
#   dc-loadtest-detection  the planted detection probes, here over MQTT too (--transport mqtt),
#                          so the DETECT oracle is judged while the broker is under load
#
# Three handshakes on three tenants ('dcctl sim create <name>' each), because every harness
# refuses a tenant that already carries recent events: they need the tenant to themselves.
#
# 🔴 THE RUN'S VERDICT IS ALL THREE. This script exits non-zero if ANY harness did, and it prints
# each harness's own exit code, because the codes differ in meaning (dc-loadtest-mqtt: 1 an
# invariant failed, 2 no verdict, 3 PUBACKed events were LOST). The rungs are one COMBINED load,
# so the first one to fail stops the other two: what they measured after that was not the run
# that was asked for. Interrupting the script (Ctrl-C, SIGTERM) stops all three.
#
#   hack/loadtest-rc-run.sh \
#       --http-handshake  ~/.devicechain/sims/rc-http/handshake.json \
#       --mqtt-handshake  ~/.devicechain/sims/rc-mqtt/handshake.json \
#       --detect-handshake ~/.devicechain/sims/rc-detect/handshake.json \
#       [--duration 60m] [--http-devices 200 --http-interval 200ms] \
#       [--mqtt-devices 3000 --mqtt-rate 6000] [--out ./rc-run] [--build]
#
#   hack/loadtest-rc-run.sh --self-test   # proves the exit-code and stop handling against stubs
#
# Defaults are the 60 minute run. The MQTT rung reads every event back (--identity full, about
# one query per 1,000 events), because a count cannot exclude a loss once anything went
# unconfirmed; see backend/sims/dc-simulator/README.md.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SIM="$ROOT/backend/sims/dc-simulator"
SELF="$ROOT/hack/$(basename "${BASH_SOURCE[0]}")"

usage() { sed -n '4,35p' "$SELF" | sed 's/^# \{0,1\}//'; exit 2; }

# --self-test runs this script against stub harnesses and checks what it reports. A guard is
# worth nothing until it has been seen to fail, so every case but the first is a failure.
self_test() {
  local d rc=0 out code
  d="$(mktemp -d)"
  # shellcheck disable=SC2064 # expand now: $d is local
  trap "rm -rf '$d'" EXIT
  mkdir -p "$d/bin"
  for b in loadtest loadtest-mqtt loadtest-detection; do
    # STUB_<name>="<exit code> <seconds>": record the pid, sleep, exit. TERM ends it at once.
    cat >"$d/bin/dc-$b" <<'STUB'
#!/usr/bin/env bash
name="$(basename "$0")"; var="STUB_${name//-/_}"; read -r code secs <<<"${!var:-0 0}"
echo $$ >"$STUB_DIR/$name.pid"
trap 'kill "$s" 2>/dev/null; exit 143' TERM
sleep "$secs" & s=$!; wait "$s"
exit "$code"
STUB
    chmod +x "$d/bin/dc-$b"
  done
  for t in a b c; do echo "{\"tenant\": \"t-$t\"}" >"$d/hs-$t.json"; done
  run() { # expected-status, then STUB_ assignments
    local want="$1"; shift
    out="$(env STUB_DIR="$d" "$@" "$SELF" --out "$d" --duration 4s \
      --http-handshake "$d/hs-a.json" --mqtt-handshake "$d/hs-b.json" --detect-handshake "$d/hs-c.json" 2>&1)" && code=0 || code=$?
    if [[ "$want" == 0 && "$code" -ne 0 ]] || [[ "$want" != 0 && "$code" -eq 0 ]]; then
      echo "self-test: $* exited $code, want ${want/x/non-zero}" >&2; echo "$out" >&2; rc=1
    fi
  }
  alive() { local n; for n in dc-loadtest dc-loadtest-mqtt dc-loadtest-detection; do
    [[ -f "$d/$n.pid" ]] && kill -0 "$(cat "$d/$n.pid")" 2>/dev/null && echo "$n"; done; true; }

  run 0 STUB_dc_loadtest="0 1" STUB_dc_loadtest_mqtt="0 1" STUB_dc_loadtest_detection="0 1"
  for r in dc_loadtest dc_loadtest_mqtt dc_loadtest_detection; do
    rm -f "$d"/*.pid
    local started=$SECONDS
    run x STUB_dc_loadtest="0 60" STUB_dc_loadtest_mqtt="0 60" STUB_dc_loadtest_detection="0 60" "STUB_$r=1 1"
    if (( SECONDS - started > 30 )); then echo "self-test: a failed $r did not stop the other rungs" >&2; rc=1; fi
    [[ -z "$(alive)" ]] || { echo "self-test: still running after the script exited: $(alive)" >&2; rc=1; }
  done
  run x STUB_dc_loadtest="0 1" STUB_dc_loadtest_mqtt="3 1" STUB_dc_loadtest_detection="0 1"
  grep -q "MQTT LOSS" <<<"$out" || { echo "self-test: an MQTT exit 3 was not reported as a LOSS" >&2; echo "$out" >&2; rc=1; }
  grep -q "mqtt exited 3" <<<"$out" || { echo "self-test: the MQTT rung's own code was not printed" >&2; rc=1; }

  # A TERM to the script stops every rung, so an interrupted run leaves nothing behind.
  rm -f "$d"/*.pid
  env STUB_DIR="$d" STUB_dc_loadtest="0 60" STUB_dc_loadtest_mqtt="0 60" STUB_dc_loadtest_detection="0 60" \
    "$SELF" --out "$d" --duration 4s --http-handshake "$d/hs-a.json" --mqtt-handshake "$d/hs-b.json" \
    --detect-handshake "$d/hs-c.json" >/dev/null 2>&1 &
  local pid=$!
  for _ in $(seq 50); do [[ -f "$d/dc-loadtest-detection.pid" ]] && break; sleep 0.2; done
  kill -TERM "$pid"
  code=0; wait "$pid" || code=$?
  [[ "$code" -ne 0 ]] || { echo "self-test: an interrupted run exited 0" >&2; rc=1; }
  sleep 1
  [[ -z "$(alive)" ]] || { echo "self-test: still running after the script was interrupted: $(alive)" >&2; rc=1; }

  [[ "$rc" -eq 0 ]] && echo "self-test: ok"
  return "$rc"
}

duration=60m
out="$ROOT/rc-run-$(date -u +%Y%m%dT%H%M%SZ)"
build=0
http_hs="" mqtt_hs="" detect_hs=""
http_devices=200 http_interval=200ms
mqtt_devices=3000 mqtt_rate=6000
mqtt_extra=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --self-test) self_test; exit $? ;;
    --http-handshake) http_hs="$2"; shift 2 ;;
    --mqtt-handshake) mqtt_hs="$2"; shift 2 ;;
    --detect-handshake) detect_hs="$2"; shift 2 ;;
    --duration) duration="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    --http-devices) http_devices="$2"; shift 2 ;;
    --http-interval) http_interval="$2"; shift 2 ;;
    --mqtt-devices) mqtt_devices="$2"; shift 2 ;;
    --mqtt-rate) mqtt_rate="$2"; shift 2 ;;
    --mqtt-arg) mqtt_extra+=("$2"); shift 2 ;;   # any other dc-loadtest-mqtt flag, one per use
    --build) build=1; shift ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done

[[ -n "$http_hs" ]] || { echo "--http-handshake is required" >&2; usage; }
[[ -n "$mqtt_hs" ]] || { echo "--mqtt-handshake is required" >&2; usage; }
[[ -n "$detect_hs" ]] || { echo "--detect-handshake is required" >&2; usage; }
# One tenant per harness: compare the handshake files' tenants, not their paths.
tenant_of() { sed -n 's/.*"tenant"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$1" | head -1; }
tenants="$(for h in "$http_hs" "$mqtt_hs" "$detect_hs"; do tenant_of "$h"; done)"
[[ "$(echo "$tenants" | grep -c .)" -eq 3 ]] || { echo "could not read a tenant from every handshake" >&2; exit 2; }
[[ "$(echo "$tenants" | sort -u | wc -l)" -eq 3 ]] || {
  echo "the three handshakes must name three DIFFERENT tenants (got: $(echo "$tenants" | tr '\n' ' '))" >&2; exit 2; }

# A duration the shell can do arithmetic with, for the detection probes' cycle count.
to_seconds() {
  local d="$1" total=0 n u
  while [[ -n "$d" ]]; do
    [[ "$d" =~ ^([0-9]+)([hms])(.*)$ ]] || { echo "unsupported duration '$1' (use e.g. 60m, 1h30m, 90s)" >&2; exit 2; }
    n="${BASH_REMATCH[1]}"; u="${BASH_REMATCH[2]}"; d="${BASH_REMATCH[3]}"
    case "$u" in h) total=$((total + n * 3600)) ;; m) total=$((total + n * 60)) ;; s) total=$((total + n)) ;; esac
  done
  echo "$total"
}
secs="$(to_seconds "$duration")"

mkdir -p "$out/bin"
# Absolute, because the build below runs from the simulator's directory: a relative --out would
# put the binaries there and then look for them here.
out="$(cd "$out" && pwd)"
if [[ "$build" -eq 1 ]]; then
  (cd "$SIM" && for b in loadtest loadtest-mqtt loadtest-detection; do
     go build -o "$out/bin/dc-$b" "./cmd/$b"
   done)
fi
for b in loadtest loadtest-mqtt loadtest-detection; do
  [[ -x "$out/bin/dc-$b" ]] || {
    echo "no $out/bin/dc-$b: pass --build, or put dc-loadtest, dc-loadtest-mqtt and dc-loadtest-detection there" >&2; exit 2; }
done

echo "rc run: $duration ($secs s) -> $out"
pids=() names=() codes=()

# Whatever ends the script (a failed rung, an interrupt, an error here) ends the rungs with it.
stop_rungs() {
  local i
  for i in "${!pids[@]}"; do
    [[ -n "${codes[$i]:-}" ]] || kill -TERM "${pids[$i]}" 2>/dev/null || true
  done
}
trap stop_rungs EXIT
trap 'echo "interrupted: stopping every rung" >&2; exit 130' INT
trap 'echo "terminated: stopping every rung" >&2; exit 143' TERM

start() { # name, command...
  local name="$1"; shift
  "$@" >"$out/$name.log" 2>&1 &
  pids+=("$!"); names+=("$name")
  echo "  started $name (pid $!)"
}

start http "$out/bin/dc-loadtest" --handshake "$http_hs" \
  --devices "$http_devices" --emit-interval "$http_interval" --hold "$duration" \
  --min-accepted 1000 --quiesce-timeout 10m --quiesce-settle 90s --state-timeout 10m --report "$out/http.json"

start mqtt "$out/bin/dc-loadtest-mqtt" --handshake "$mqtt_hs" \
  --devices "$mqtt_devices" --rate "$mqtt_rate" --duration "$duration" --qos 1 \
  --identity full --quiesce-timeout 10m --report "$out/mqtt.json" ${mqtt_extra[@]+"${mqtt_extra[@]}"}

# One probe step per second for the whole duration: K cycles = 2 steps each, so K = secs / 2.
start detect "$out/bin/dc-loadtest-detection" --handshake "$detect_hs" --transport mqtt \
  --bg-devices 500 --bg-interval 250ms --cycles "$((secs / 2))" --probe-interval 1s \
  --report "$out/detect.json"

# Reap the rungs as they finish. The first one to fail stops the others: the run was a combined
# load, and what the survivors measure without it is not that run.
rc=0 left=${#pids[@]} stopped=""
while (( left > 0 )); do
  for i in "${!pids[@]}"; do
    [[ -z "${codes[$i]:-}" ]] || continue
    kill -0 "${pids[$i]}" 2>/dev/null && continue
    code=0
    wait "${pids[$i]}" || code=$?
    codes[i]=$code
    left=$((left - 1))
    echo "  ${names[$i]} exited $code"
    if [[ "$code" -ne 0 ]]; then
      rc=1
      if [[ -z "$stopped" ]]; then
        stopped=${names[$i]}
        echo "  ${names[$i]} failed: stopping the other rungs"
        stop_rungs
      fi
    fi
  done
  (( left == 0 )) || sleep 1
done
trap - EXIT

code_of() { local i; for i in "${!names[@]}"; do [[ "${names[$i]}" == "$1" ]] && echo "${codes[$i]}"; done; }
echo
echo "reports: $out/{http,mqtt,detect}.json; logs: $out/{http,mqtt,detect}.log"
[[ -z "$stopped" ]] || echo "STOPPED EARLY: $stopped failed first; a rung that exited 143 was stopped, not failed on its own"
[[ "$(code_of mqtt)" -ne 3 ]] || echo "MQTT LOSS: PUBACKed events are missing from the store (see $out/mqtt.json, storage.lost)"
exit "$rc"
