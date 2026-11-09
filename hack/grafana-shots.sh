#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Screenshots of the Grafana boards, for docs/docs/deployment/grafana-boards.md.
#
#   hack/grafana-shots.sh up        create the kind cluster dc-gshots (its own kubeconfig, loopback
#                                   host ports), install a PUBLISHED release with its monitoring
#                                   stack, bootstrap the instance `demo`
#   hack/grafana-shots.sh load      create four synthetic tenants and drive them in the background:
#                                   three simulators (telemetry, alarms raised and cleared) and the
#                                   command round-trip harness (rules send commands, devices answer)
#   hack/grafana-shots.sh shots     one frame of every stage board and the overview
#   hack/grafana-shots.sh fault     scale event-management to 0, wait until the board's own query
#                                   shows the backlog, take the fault frames, scale it back, wait
#                                   until the backlog has drained, take the recovery frames
#   hack/grafana-shots.sh unload    stop the simulators and the port-forwards
#   hack/grafana-shots.sh down      delete the cluster and dcctl's local state for it
#   hack/grafana-shots.sh deadman S block S seconds, then `down`: arm it in the background the
#                                   moment the cluster exists, so a forgotten cluster cannot run on
#   hack/grafana-shots.sh all       up, load, wait DC_GSHOTS_SOAK seconds, shots, fault, unload
#
# THE RELEASE. DC_GSHOTS_RELEASE (default v1.3.0) names the release whose dcctl and dc-simulator
# are downloaded, checked against the release's checksums and its build provenance
# (`gh attestation verify`, bound to release.yml at that tag), and used for everything but the
# command harness (a test client built from this tree): every service image and the boards
# themselves are the published ones. The boards come from the chart
# that dcctl embeds, so take the frames with the release whose boards the docs page describes.
#
# THE FAULT, AND ITS CHECK. event-management is the reader that stores events. With it at zero
# replicas the events keep arriving and wait in the broker, and the Persistence board's
# "Events waiting to be stored" panel switches from the reader's own count to the broker's (the
# fallback the docs describe). The fault frames are taken only after Prometheus answers THAT
# PANEL'S OWN QUERY, read out of the board file and with the board's variables filled in, with a
# backlog above DC_GSHOTS_FAULT_MIN and the deployment's wanted replicas at 0. The same query is
# read before the fault and must be BELOW the threshold then: that is the case in which the check
# says "not registered", shown before it is trusted to say "registered". The recovery frames wait
# for the query to fall below the threshold again.
#
# NOTHING SECRET IS PRINTED. The instance's superuser password is read by dcctl from the cluster;
# the Grafana admin password goes from its Secret into the capture process's environment and from
# there into the browser's basic-auth credentials. The sims' handshake files (which hold their
# passwords) are passed by path and never read here.
#
# ISOLATION. The cluster has its own kubeconfig under DC_GSHOTS_STATE, given explicitly to every
# kubectl, kind and dcctl call; this script never reads or changes another kubeconfig's current
# context. `down` deletes only a cluster that kubeconfig reaches, and refuses the shared developer
# cluster `devicechain` by name. Processes it starts (simulators, port-forwards) are recorded by
# PID under DC_GSHOTS_STATE and only those are stopped.
#
# THE FRAMES. hack/guide-capture/grafana-capture.mjs signs in to Grafana, opens each board in kiosk
# mode at DC_GSHOTS_WIDTH x DC_GSHOTS_HEIGHT (1600x1000), dark theme, UTC, and writes WebP files to
# DC_GSHOTS_OUT (docs/static/img/grafana). The board frames cover DC_GSHOTS_RANGE (30 minutes),
# the fault and recovery frames DC_GSHOTS_FAULT_RANGE (15 minutes): run `fault` once the earlier
# frames' window holds no other outage, or the recovery frame shows both.
#
# Requires: kind, kubectl, docker, jq, curl, gh, unzip/tar, go, node (and Chrome, for the capture),
# and `npm ci` in hack/guide-capture (done here when node_modules is missing).

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="${DC_GSHOTS_CLUSTER:-dc-gshots}"
kube_context="kind-$cluster"
instance="${DC_GSHOTS_INSTANCE:-demo}"
namespace="dci-$instance"
release="${DC_GSHOTS_RELEASE:-v1.3.0}"
state_dir="${DC_GSHOTS_STATE:-$repo_root/.grafana-shots}"
out_dir="${DC_GSHOTS_OUT:-$repo_root/docs/static/img/grafana}"
http_port="${DC_GSHOTS_HTTP_PORT:-28180}"       # kind host port -> the ingress (HTTP)
mqtt_port="${DC_GSHOTS_MQTT_PORT:-21983}"       # kind host port -> the broker's MQTT NodePort
ingest_port="${DC_GSHOTS_INGEST_PORT:-28181}"   # port-forward -> event-sources :8081
grafana_port="${DC_GSHOTS_GRAFANA_PORT:-23000}" # port-forward -> Grafana
prom_port="${DC_GSHOTS_PROM_PORT:-29090}"       # port-forward -> Prometheus
soak="${DC_GSHOTS_SOAK:-1080}"                  # seconds of load before the board frames
fault_len="${DC_GSHOTS_FAULT_LEN:-150}"         # seconds event-management stays at zero, at least
fault_min="${DC_GSHOTS_FAULT_MIN:-2000}"        # backlog that counts as the fault registering
range="${DC_GSHOTS_RANGE:-now-30m}"             # the board frames
fault_range="${DC_GSHOTS_FAULT_RANGE:-now-15m}" # the fault and recovery frames
width="${DC_GSHOTS_WIDTH:-1600}"
height="${DC_GSHOTS_HEIGHT:-1000}"
min_free_gb="${DC_GSHOTS_MIN_FREE_GB:-60}"

# name  manifest  devices  cadence  display name. Synthetic names only: they appear in frames.
sims=(
  "acme-water|devicepulse|150|1s|Acme Water"
  "northwind-hvac|buildingpulse|40|2s|Northwind HVAC"
  "metro-fleet|fleetpulse|24|2s|Metro Fleet"
)

commander="harbor-pumps"
commander_display="Harbor Pumps"

[[ "$cluster" != devicechain ]] || { echo "refusing: DC_GSHOTS_CLUSTER=devicechain is the shared developer cluster" >&2; exit 2; }
[[ "$release" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || { echo "DC_GSHOTS_RELEASE must be a release tag like v1.3.0" >&2; exit 2; }

winpath() { if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s' "$1"; fi; }
mkdir -p "$state_dir"
kubeconfig="$(winpath "$state_dir/kubeconfig")"
export KUBECONFIG="$kubeconfig"

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }
pass() { printf '\033[1;32m    PASS %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }
redact() { sed -u -E 's/^([[:space:]]*password:[[:space:]]*).+$/\1[redacted]/'; }
if command jq -b -n 1 >/dev/null 2>&1; then jq() { command jq -b "$@"; }; fi

k() { kubectl --kubeconfig "$kubeconfig" --context "$kube_context" "$@"; }
kn() { k -n "$namespace" "$@"; }

curl_revoke=()
[[ "$(curl -V 2>/dev/null)" != *[Ss]channel* ]] || curl_revoke=(--ssl-no-revoke)

host_os="$(go env GOHOSTOS 2>/dev/null || uname -s | tr '[:upper:]' '[:lower:]')"
host_arch="$(go env GOHOSTARCH 2>/dev/null || echo amd64)"
exe=""; [[ "$host_os" != windows ]] || exe=.exe
release_dir="$state_dir/release-$release"
dcctl="$release_dir/dcctl$exe"
simulator="$release_dir/dc-simulator$exe"

# ---------------------------------------------------------------------------
# the release
# ---------------------------------------------------------------------------

# fetch_release downloads the release's dcctl and dc-simulator for this host, checks each archive
# against the release's checksum files (fetched afresh) and its build provenance, and unpacks the
# binaries from the archives just checked: a binary already on disk is never trusted on its own.
fetch_release() {
  need gh
  local v="${release#v}" base f sum want tmp ext
  base="https://github.com/devicechain-io/devicechain/releases/download/$release"
  mkdir -p "$release_dir"
  say "fetching and verifying dcctl and dc-simulator $release"
  for f in checksums.txt dc-simulator_checksums.txt; do
    curl -fsSL --max-time 120 "${curl_revoke[@]}" -o "$release_dir/$f" "$base/$f" || fail "could not download $base/$f"
  done
  if [[ "$host_os" == windows ]]; then ext=zip; else ext=tar.gz; fi
  for f in "dcctl_${v}_${host_os}_${host_arch}.$ext" "dc-simulator_${v}_${host_os}_${host_arch}.$ext"; do
    want="$(awk -v f="$f" '($2 == f || $2 == "*" f) && w == "" { w = $1 } END { print w }' "$release_dir/checksums.txt" "$release_dir/dc-simulator_checksums.txt")"
    [[ -n "$want" ]] || fail "$f: the release's checksum files do not name it"
    sum=""
    [[ ! -f "$release_dir/$f" ]] || sum="$(sha256sum "$release_dir/$f" | awk '{ print $1 }')"
    if [[ "$sum" != "$want" ]]; then
      curl -fsSL --max-time 600 "${curl_revoke[@]}" -o "$release_dir/$f" "$base/$f" || fail "could not download $base/$f"
      sum="$(sha256sum "$release_dir/$f" | awk '{ print $1 }')"
    fi
    [[ "$sum" == "$want" ]] || fail "$f: sha256 $sum, the release's checksums say '$want'"
    gh attestation verify "$(winpath "$release_dir/$f")" --repo devicechain-io/devicechain \
      --signer-workflow devicechain-io/devicechain/.github/workflows/release.yml --source-ref "refs/tags/$release" >/dev/null ||
      fail "$f: no build provenance from release.yml at refs/tags/$release"
    note "$f: sha256 matches the release's checksums, and its build provenance verifies"
  done
  tmp="$(mktemp -d "$release_dir/unpack.XXXXXX")"
  for f in dcctl dc-simulator; do
    if [[ "$ext" == zip ]]; then
      (cd "$tmp" && unzip -o -q "../${f}_${v}_${host_os}_${host_arch}.zip" "$f$exe") || fail "could not unpack $f"
    else
      (cd "$tmp" && tar xzf "../${f}_${v}_${host_os}_${host_arch}.tar.gz" "$f") || fail "could not unpack $f"
    fi
    mv -f "$tmp/$f$exe" "$release_dir/$f$exe"
    chmod +x "$release_dir/$f$exe"
  done
  rmdir "$tmp"
  "$dcctl" version | sed 's/^/    /'
}

# ---------------------------------------------------------------------------
# up / down / deadman
# ---------------------------------------------------------------------------

cluster_exists() { grep -qx -- "$cluster" <<<"$(kind get clusters 2>/dev/null)"; }

cmd_up() {
  need kind; need kubectl; need docker; need jq; need curl
  local free
  free="$(df -Pk "$state_dir" | awk 'NR==2 {printf "%d", $4/1024/1024}')"
  [[ "$free" -ge "$min_free_gb" ]] || fail "only ${free} GB free under $state_dir; need $min_free_gb (DC_GSHOTS_MIN_FREE_GB)"
  fetch_release
  if ! cluster_exists; then
    say "creating kind cluster $cluster (ingress on 127.0.0.1:$http_port, MQTT on 127.0.0.1:$mqtt_port)"
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
      - containerPort: 80
        hostPort: $http_port
        listenAddress: "127.0.0.1"
        protocol: TCP
      - containerPort: 31883
        hostPort: $mqtt_port
        listenAddress: "127.0.0.1"
        protocol: TCP
EOF
    kind create cluster --name "$cluster" --kubeconfig "$kubeconfig" --config "$(winpath "$state_dir/kind.yaml")" --wait 120s
  else
    k get ns kube-system >/dev/null 2>&1 || fail "kind cluster $cluster exists but $kubeconfig does not reach it: not this script's"
  fi
  if kn get deploy event-management >/dev/null 2>&1; then
    note "instance $instance already exists on $cluster; reusing it"
    return
  fi
  say "installing the cluster prerequisites and the monitoring stack from $release"
  # The monitoring stack is on by default; it is what this script photographs.
  "$dcctl" install local --yes --cluster "$cluster" --version "$release" 2>&1 | redact
  say "bootstrapping $instance from the published $release images"
  "$dcctl" bootstrap local "$instance" --yes --no-escrow --cluster "$cluster" --host localhost --no-tls --version "$release" 2>&1 | redact
  say "UP COMPLETE"
}

cmd_down() {
  need kind
  cmd_unload || true
  if cluster_exists; then
    local uid
    uid="$(k get ns kube-system -o jsonpath='{.metadata.uid}' 2>/dev/null)" ||
      fail "kind cluster $cluster exists but $kubeconfig does not reach it: not this script's; refusing to delete it"
    [[ -n "$uid" && "$uid" != */* && "$uid" != *\\* ]] || fail "cannot read the kube-system UID of $cluster"
    local rec="${HOME:?}/.devicechain/instances/$instance/instance.json"
    if [[ -x "$dcctl" && -f "$rec" && "$(jq -r '.kubeContext // empty' "$rec")" == "$kube_context" ]]; then
      say "destroying instance $instance"
      "$dcctl" destroy local "$instance" --yes 2>&1 | redact || note "dcctl destroy exited non-zero; deleting the cluster anyway"
    fi
    say "deleting kind cluster $cluster"
    kind delete cluster --name "$cluster" --kubeconfig "$kubeconfig"
    rm -rf "${HOME:?}/.devicechain/clusters/$uid"
  fi
  # dcctl's local records for this instance and the sims, when they name this cluster.
  local rec="${HOME:?}/.devicechain/instances/$instance/instance.json"
  if [[ -f "$rec" && "$(jq -r '.kubeContext // empty' "$rec")" == "$kube_context" ]]; then
    rm -rf "${HOME:?}/.devicechain/instances/$instance"
  fi
  local s name
  for s in "${sims[@]}"; do
    name="${s%%|*}"
    rm -f "${HOME:?}/.devicechain/sims/$name.json"
  done
  rm -f "${HOME:?}/.devicechain/sims/$commander.json"
  rm -f "$state_dir/kubeconfig"
  ! cluster_exists || fail "kind cluster $cluster still exists"
  pass "cluster $cluster is gone"
}

cmd_deadman() {
  local secs="${1:?deadman needs a number of seconds}"
  note "dead-man: deleting $cluster in ${secs}s unless this process is stopped"
  sleep "$secs"
  say "dead-man fired"
  cmd_down
}

# ---------------------------------------------------------------------------
# background processes this script owns (recorded by PID)
# ---------------------------------------------------------------------------

start_bg() { # start_bg <tag> <cmd...>: run in the background, log to state, record the PID
  local tag="$1"; shift
  "$@" >"$state_dir/$tag.log" 2>&1 &
  echo "$!" >"$state_dir/$tag.pid"
}

stop_bg() {
  local tag="$1" pid
  [[ -f "$state_dir/$tag.pid" ]] || return 0
  pid="$(cat "$state_dir/$tag.pid")"
  kill "$pid" 2>/dev/null || true
  rm -f "$state_dir/$tag.pid"
}

running_bg() { [[ -f "$state_dir/$1.pid" ]] && kill -0 "$(cat "$state_dir/$1.pid")" 2>/dev/null; }

# forward_loop keeps a port-forward up: one dies when its pod goes away, and the ingest forward
# must outlive an event-sources restart.
forward_loop() {
  local ns="$1" target="$2" ports="$3"
  while :; do
    k -n "$ns" port-forward "$target" "$ports" || true
    sleep 2
  done
}

ensure_forward() { # ensure_forward <tag> <namespace> <target> <local:remote>
  running_bg "fwd-$1" || start_bg "fwd-$1" forward_loop "$2" "$3" "$4"
  local port="${4%%:*}"
  for _ in $(seq 1 60); do
    curl -s -o /dev/null --max-time 2 "http://localhost:$port/" && return 0
    sleep 1
  done
  fail "nothing answered on localhost:$port (port-forward $3; see $state_dir/fwd-$1.log)"
}

# ---------------------------------------------------------------------------
# load
# ---------------------------------------------------------------------------

cmd_load() {
  [[ -x "$dcctl" && -x "$simulator" ]] || fetch_release
  ensure_forward ingest "$namespace" svc/event-sources "$ingest_port:8081"
  local s name manifest devices cadence display i=0 port
  for s in "${sims[@]}"; do
    IFS='|' read -r name manifest devices cadence display <<<"$s"
    port=$((18091 + i)); i=$((i + 1))
    if running_bg "sim-$name"; then note "sim $name already running"; continue; fi
    say "sim $name ($manifest, $devices devices every $cadence, tenant \"$display\")"
    "$dcctl" sim create "$name" -i "$instance" -s "localhost:$http_port" --kube-context "$kube_context" \
      --ingress "http://localhost:$ingest_port" --mqtt-broker "ssl://localhost:$mqtt_port" \
      --manifest "$manifest" --tenant-name "$display" >"$state_dir/sim-create-$name.log" 2>&1 ||
      { tail -5 "$state_dir/sim-create-$name.log" >&2; fail "dcctl sim create $name failed"; }
    start_bg "sim-$name" env DC_SIM_DEVICES="$devices" DC_SIM_EMIT_INTERVAL="$cadence" \
      "$simulator" --handshake "$(winpath "$HOME/.devicechain/sims/$name.json")" --port "$port"
  done
  start_commander
  sleep 20
  for s in "${sims[@]}"; do
    name="${s%%|*}"
    running_bg "sim-$name" || { tail -5 "$state_dir/sim-$name.log" >&2; fail "simulator $name exited"; }
  done
  pass "three simulators running"
}

# The scenarios raise and clear alarms but send no commands, so a fourth tenant runs the command
# round-trip harness in a loop: a rule whose sendCommand action fires on a threshold crossing, and
# a device-plane receiver that answers each command over MQTT. It is built from this tree (it is a
# test client, not part of a release).
commander_loop() {
  local hs="$1"
  while :; do
    "$state_dir/dc-loadtest-command$exe" --handshake "$hs" --mqtt-broker "ssl://localhost:$mqtt_port"       --bg-devices 20 --bg-interval 1s --command-probes 6 --cycles 10 --probe-interval 2s --min-accepted 100 || true
    sleep 5
  done
}

start_commander() {
  running_bg commander && { note "commander already running"; return 0; }
  say "commander $commander (command round trips to SUCCESSFUL, tenant \"$commander_display\")"
  if [[ ! -x "$state_dir/dc-loadtest-command$exe" ]]; then
    (cd "$repo_root/backend/sims/dc-simulator" && go build -o "$state_dir/dc-loadtest-command$exe" ./cmd/loadtest-command) ||
      fail "could not build dc-loadtest-command"
  fi
  "$dcctl" sim create "$commander" -i "$instance" -s "localhost:$http_port" --kube-context "$kube_context"     --ingress "http://localhost:$ingest_port" --mqtt-broker "ssl://localhost:$mqtt_port"     --tenant-name "$commander_display" >"$state_dir/sim-create-$commander.log" 2>&1 ||
    { tail -5 "$state_dir/sim-create-$commander.log" >&2; fail "dcctl sim create $commander failed"; }
  start_bg commander commander_loop "$(winpath "$HOME/.devicechain/sims/$commander.json")"
}

cmd_unload() {
  local s
  for s in "${sims[@]}"; do stop_bg "sim-${s%%|*}"; done
  stop_bg commander
  stop_bg fwd-ingest; stop_bg fwd-grafana; stop_bg fwd-prom
  kill_owned
}

# kill_owned stops any process left over from this script: every one it starts names the state
# directory on its command line (the simulator binary lives there, and kubectl is given the
# kubeconfig in it), so that path is what selects them, and nothing else is touched.
kill_owned() {
  local pat
  pat="$(winpath "$state_dir")"
  if [[ "$host_os" == windows ]]; then
    PAT="$pat" powershell.exe -NoProfile -Command '
      $p = $env:PAT.ToLower()
      Get-CimInstance Win32_Process | Where-Object {
        $_.ProcessId -ne $PID -and $_.Name -ne "powershell.exe" -and $_.CommandLine -and
        ($_.CommandLine.ToLower() -replace "\\", "/").Contains($p) } |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue; "stopped $($_.Name) $($_.ProcessId)" }' |
      sed 's/^/    /'
  else
    pkill -f -- "$pat" 2>/dev/null || true
  fi
}

# ---------------------------------------------------------------------------
# prometheus: the board's own query
# ---------------------------------------------------------------------------

prom() { # prom <promql>: prints the scalar of a single-sample answer, or "none"
  curl -fsS --max-time 20 "http://localhost:$prom_port/api/v1/query" --data-urlencode "query=$1" |
    jq -r 'if (.data.result | length) == 0 then "none" else (.data.result[0].value[1]) end'
}

# board_expr <board stem> <panel title>: the panel's first query, with the board's variables filled
# in. Read from the chart's board file, so the check reads exactly what the frame shows.
board_expr() {
  jq -r --arg t "$2" '[.. | objects | select(.title? == $t) | .targets[0].expr] | first // empty' \
    "$repo_root/deploy/helm/devicechain/dashboards/$1.json" |
    sed -e "s/\\\$namespace/$namespace/g" -e "s/\\\${instance}/$instance/g" -e "s/\\\$instance/$instance/g"
}

# ---------------------------------------------------------------------------
# capture
# ---------------------------------------------------------------------------

capture() { # capture <json array of shots>
  local gc="$repo_root/hack/guide-capture"
  [[ -d "$gc/node_modules/@playwright" ]] || (cd "$gc" && npm ci --no-audit --no-fund >/dev/null)
  ensure_forward grafana monitoring svc/kube-prometheus-stack-grafana "$grafana_port:80"
  jq -n --arg g "http://localhost:$grafana_port" --arg i "$instance" --arg o "$(winpath "$out_dir")" \
    --argjson w "$width" --argjson h "$height" --argjson s "$1" \
    '{grafana: $g, instance: $i, outDir: $o, width: $w, height: $h, shots: $s}' >"$state_dir/shots.json"
  GRAFANA_PASSWORD="$(k -n monitoring get secret dc-grafana-admin -o jsonpath='{.data.admin-password}' | base64 -d)" \
    node "$(winpath "$gc/grafana-capture.mjs")" "$(winpath "$state_dir/shots.json")"
}

cmd_shots() {
  # Delivery opens at the hand-off from automation rules: its first group belongs to the optional
  # outbound-connectors area, which the default profile does not run.
  capture "$(jq -nc --arg r "$range" '
    [ "overview", "ingest", "messaging", "persistence", "event-processing", "command-delivery", "platform" ]
    | map({name: ., board: ., from: $r, to: "now"})
    + [{name: "delivery", board: "delivery", from: $r, to: "now", scrollTo: "Hand-off from automation rules"}]')"
}

cmd_fault() {
  ensure_forward prom monitoring svc/kube-prometheus-stack-prometheus "$prom_port:9090"
  local q v wanted t0
  q="$(board_expr persistence "Events waiting to be stored")"
  [[ -n "$q" ]] || fail "could not read the query of Persistence / Events waiting to be stored from the board file"
  q="max($q)"

  say "before the fault: the check must say NOT registered"
  v="$(prom "$q")"
  note "events waiting to be stored: $v (threshold $fault_min)"
  [[ "$v" != none ]] || fail "the panel's query answers nothing before the fault: the board would show no data"
  awk -v v="$v" -v m="$fault_min" 'BEGIN { exit !(v < m) }' ||
    fail "the backlog is already $v before the fault; the check could not tell a fault from steady state"
  pass "steady state is below the threshold, so the check can fail"

  say "fault: event-management to 0 replicas"
  kn scale deploy/event-management --replicas=0
  t0=$SECONDS
  while :; do
    sleep 15
    wanted="$(prom "max(kube_deployment_spec_replicas{namespace=\"$namespace\",deployment=\"event-management\"})")"
    v="$(prom "$q")"
    note "t+$((SECONDS - t0))s: wanted replicas $wanted, events waiting to be stored $v"
    if [[ "$wanted" == 0 ]] && awk -v v="$v" -v m="$fault_min" 'BEGIN { exit !(v != "none" && v >= m) }' &&
      ((SECONDS - t0 >= fault_len)); then
      break
    fi
    ((SECONDS - t0 < 900)) || { kn scale deploy/event-management --replicas=1; fail "the fault did not register on the board's data within 15 minutes"; }
  done
  pass "the fault registered: wanted replicas 0, backlog $v from the broker's own count"
  capture "$(jq -nc --arg r "$fault_range" '[
    {name: "fault-overview", board: "overview", from: $r, to: "now", scrollTo: "How much is flowing"},
    {name: "fault-persistence-backlog", board: "persistence", from: $r, to: "now", viewPanel: 12},
    {name: "fault-messaging-readers", board: "messaging", from: $r, to: "now", scrollTo: "Readers of streams"}
  ]')" || { kn scale deploy/event-management --replicas=1; fail "fault capture failed"; }

  say "recovery: event-management back to 1 replica"
  kn scale deploy/event-management --replicas=1
  kn rollout status deploy/event-management --timeout=300s
  t0=$SECONDS
  while :; do
    sleep 15
    v="$(prom "$q")"
    note "t+$((SECONDS - t0))s: events waiting to be stored $v"
    awk -v v="$v" -v m="$fault_min" 'BEGIN { exit !(v != "none" && v < m) }' && break
    ((SECONDS - t0 < 900)) || fail "the backlog did not drain within 15 minutes"
  done
  pass "the backlog drained to $v"
  sleep 60 # one more scrape of the drained state on the graph
  capture "$(jq -nc --arg r "$fault_range" '[
    {name: "recovered-persistence-backlog", board: "persistence", from: $r, to: "now", viewPanel: 12}
  ]')"
}

cmd_all() {
  cmd_up
  cmd_load
  say "soaking for ${soak}s before the frames"
  sleep "$soak"
  cmd_shots
  cmd_fault
  cmd_unload
}

rig_cmd="${1:-}"
case "$rig_cmd" in
  up) cmd_up ;;
  load) cmd_load ;;
  unload) cmd_unload ;;
  shots) cmd_shots ;;
  fault) cmd_fault ;;
  down) cmd_down ;;
  deadman) cmd_deadman "${2:-}" ;;
  all) cmd_all ;;
  *) sed -n '5,22p' "$0"; exit 2 ;;
esac
