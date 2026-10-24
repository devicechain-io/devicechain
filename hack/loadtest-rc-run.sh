#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The release-candidate performance run: the HTTP rung, the MQTT rung and the DETECT oracle, all
# at once, for one duration, each on its OWN tenant.
#
#   dc-loadtest            HTTP ingress at a target rate, persisted == accepted, identity,
#                          live state (the L1 oracle)
#   dc-loadtest-mqtt       one MQTT session per device, QoS 1 over TLS; PUBACKed must equal
#                          persisted, and a loss exits 3
#   dc-loadtest-detection  the planted detection probes, here over MQTT too (--transport mqtt),
#                          so the DETECT oracle is judged while the broker is under load
#
# Three handshakes on three tenants ('dcctl sim create <name>' each), because every harness
# refuses a tenant that already carries recent events: they need the tenant to themselves.
#
# 🔴 THE RUN'S VERDICT IS ALL THREE. This script exits non-zero if ANY harness did, and it prints
# each harness's own exit code, because the codes differ in meaning (dc-loadtest-mqtt: 1 an
# invariant failed, 2 no verdict, 3 PUBACKed events were LOST).
#
#   hack/loadtest-rc-run.sh \
#       --http-handshake  ~/.devicechain/sims/rc-http/handshake.json \
#       --mqtt-handshake  ~/.devicechain/sims/rc-mqtt/handshake.json \
#       --detect-handshake ~/.devicechain/sims/rc-detect/handshake.json \
#       [--duration 60m] [--http-devices 200 --http-interval 200ms] \
#       [--mqtt-devices 3000 --mqtt-rate 6000] [--out ./rc-run] [--build]
#
# Defaults are the 60 minute run. The MQTT rung uses --identity count above ~2M events (a full
# read-back is one query per 1,000 events); see backend/sims/dc-simulator/README.md.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SIM="$ROOT/backend/sims/dc-simulator"

duration=60m
out="$ROOT/rc-run-$(date -u +%Y%m%dT%H%M%SZ)"
build=0
http_hs="" mqtt_hs="" detect_hs=""
http_devices=200 http_interval=200ms
mqtt_devices=3000 mqtt_rate=6000
mqtt_extra=()

usage() { sed -n '4,31p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

while [[ $# -gt 0 ]]; do
  case "$1" in
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
if [[ "$build" -eq 1 ]]; then
  (cd "$SIM" && for b in loadtest loadtest-mqtt loadtest-detection; do
     go build -o "$out/bin/dc-$b" "./cmd/$b"
   done)
elif [[ ! -x "$out/bin/dc-loadtest-mqtt" ]]; then
  echo "no binaries in $out/bin: pass --build, or put dc-loadtest, dc-loadtest-mqtt and dc-loadtest-detection there" >&2
  exit 2
fi

echo "rc run: $duration ($secs s) -> $out"
pids=() names=()
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
  --identity count --quiesce-timeout 10m --report "$out/mqtt.json" ${mqtt_extra[@]+"${mqtt_extra[@]}"}

# One probe step per second for the whole duration: K cycles = 2 steps each, so K = secs / 2.
start detect "$out/bin/dc-loadtest-detection" --handshake "$detect_hs" --transport mqtt \
  --bg-devices 500 --bg-interval 250ms --cycles "$((secs / 2))" --probe-interval 1s \
  --report "$out/detect.json"

rc=0
for i in "${!pids[@]}"; do
  code=0
  wait "${pids[$i]}" || code=$?
  echo "  ${names[$i]} exited $code"
  [[ "$code" -eq 0 ]] || rc=1
  eval "code_${names[$i]}=$code"
done

echo
echo "reports: $out/{http,mqtt,detect}.json; logs: $out/{http,mqtt,detect}.log"
[[ "${code_mqtt:-0}" -ne 3 ]] || echo "MQTT LOSS: PUBACKed events are missing from the store (see $out/mqtt.json, storage.lost)"
exit "$rc"
