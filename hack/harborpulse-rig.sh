#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The harborpulse rig: one compressed day of the harborpulse scenario against a real instance,
# judged by cmd/scenario-verify, and the same day with each planted defect, which must turn its
# own invariant red.
#
# WHAT THIS GATES
#
# harborpulse drives seven DETECT rule kinds, two command-carrying rules whose commands change the
# telemetry, an area correlation and a store-and-forward burst through one 24-minute sim day.
# scenario-verify reads the platform back over the tenant GraphQL APIs as the sim's own identity
# and judges eight invariants:
#
#   flood-escalates             one flood alarm per gauge, raised at MINOR, escalating in place to
#                               CRITICAL on East Quay only, and clearing
#   pump-commanded              start-pump SUCCESSFUL, the pump running and its well falling, the
#                               well never reaching the pump's own float
#   derate-obeyed               derate SUCCESSFUL, the cap reported at 11 kW, connector-hot cleared
#                               before the long session ends
#   air-lock-only-east          pump-air-lock for the East Quay pump only, inside its window
#   correlation-fires           previewRule of quay-flooding fires for East Quay only
#   correlation-control-is-red  the same preview with a count no quay reaches is judged red (the
#                               verifier runs this control itself, in the same day)
#   outage-blast-radius         light-silent / charger-offline for exactly the East Quay devices
#   backfill-stored             every reading a charger buffered in the outage is stored at the
#                               time it was taken
#
# It is a manual rig like hack/outage-rig.sh and hack/ota-rig.sh, and it follows their one rule: a
# check is worth nothing until it has been shown to fail. So beside the clean day it runs one day
# per planted defect, each on a sim of its own (its own tenant), all at once:
#
#   control             planted as                          must turn red
#   flood-split-key     --scenario-defect flood-split-key   flood-escalates
#   no-far-end          --no-command-far-end                pump-commanded
#   refuse-derate       --scenario-defect refuse-derate     derate-obeyed
#   air-lock-basin      --scenario-defect air-lock-basin    air-lock-only-east
#   silence-north-lamp  --scenario-defect silence-north-lamp outage-blast-radius
#   drop-backfill       --scenario-defect drop-backfill     backfill-stored
#
# The clean day must exit 0. Each control must exit 1 WITH its own invariant red; a control that
# exits 0 means the invariant cannot fail, and the rig fails. Other invariants a control turns red
# are reported, not judged (no-far-end also leaves derate unanswered, by design).
#
#   hack/harborpulse-rig.sh up        create the kind cluster dc-hprig (its own kubeconfig and host
#                                     ports) and install and bootstrap an instance: images built
#                                     from THIS tree, or a published release with DC_HPRIG_VERSION
#   hack/harborpulse-rig.sh run       the offline controls, then the clean day and every control
#   hack/harborpulse-rig.sh all       up + run
#   hack/harborpulse-rig.sh controls  the offline controls only (no cluster)
#   hack/harborpulse-rig.sh down      delete the cluster and dcctl's local state for it
#
# USING AN INSTANCE YOU ALREADY HAVE. Set DC_HPRIG_KUBECONFIG and DC_HPRIG_KUBE_CONTEXT (and
# DC_HPRIG_INSTANCE) and skip `up`: `run` then uses that instance. The rig still refuses the shared
# developer cluster (kind-devicechain) by name, and never changes a kubeconfig's current context.
#
# OFFLINE CONTROLS (`controls`, and run first by `run`): the verifier's own unit controls (a clean
# day passes; each planted defect turns exactly its invariant red), and this script's judge fed
# fabricated reports.
#
# Knobs: DC_HPRIG_CONTROLS=none runs the clean day alone; DC_HPRIG_KEEP=1 keeps the sims' tenants
# afterwards; DC_HPRIG_SIM_PORT is the first sim's control port (the rest follow).
#
# Requires: kind, kubectl, docker, jq, curl, go (and ko, unless DC_HPRIG_VERSION names a release).
# A run takes about 40 minutes: a sim day is 24 minutes and the verifier waits for the next whole
# one.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="${DC_HPRIG_CLUSTER:-dc-hprig}"
instance="${DC_HPRIG_INSTANCE:-hprig}"
namespace="dci-$instance"
state_dir="${DC_HPRIG_STATE:-$repo_root/.harborpulse-rig}"
http_port="${DC_HPRIG_HTTP_PORT:-28180}"     # loopback port-forward to the ingress
ingest_port="${DC_HPRIG_INGEST_PORT:-28181}" # loopback port-forward to event-sources
mqtt_port="${DC_HPRIG_MQTT_PORT:-21983}"     # kind host port -> the broker's MQTT NodePort
sim_port="${DC_HPRIG_SIM_PORT:-18190}"
version="${DC_HPRIG_VERSION:-}"
controls="${DC_HPRIG_CONTROLS:-all}"
min_free_gb="${DC_HPRIG_MIN_FREE_GB:-60}"
exe="$(go env GOEXE 2>/dev/null || true)"
dcctl="$repo_root/backend/cli/build/dcctl$exe"
simulator="$state_dir/bin/dc-simulator$exe"
verifier="$state_dir/bin/scenario-verify$exe"

winpath() { if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s' "$1"; fi; }
mkdir -p "$state_dir"

if [[ -n "${DC_HPRIG_KUBECONFIG:-}" ]]; then
  kubeconfig="$DC_HPRIG_KUBECONFIG"
  kube_context="${DC_HPRIG_KUBE_CONTEXT:?DC_HPRIG_KUBE_CONTEXT is required with DC_HPRIG_KUBECONFIG}"
  own_cluster=0
else
  kubeconfig="$(winpath "$state_dir/kubeconfig")"
  kube_context="kind-$cluster"
  own_cluster=1
fi
export KUBECONFIG="$kubeconfig"

say() { printf '\n==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }
pass() { printf '    PASS %s\n' "$*"; }
red() { printf '    RED (as designed) %s\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }

[[ "$cluster" != devicechain && "$kube_context" != kind-devicechain ]] ||
  { echo "refusing: kind-devicechain is the shared developer cluster" >&2; exit 2; }

k() { kubectl --kubeconfig "$kubeconfig" --context "$kube_context" "$@"; }

# The runs: name | simulator flags | the invariant it must turn red ("" for the clean day).
runs=(
  "clean||"
  "flood-split-key|--scenario-defect flood-split-key|flood-escalates"
  "no-far-end|--no-command-far-end|pump-commanded"
  "refuse-derate|--scenario-defect refuse-derate|derate-obeyed"
  "air-lock-basin|--scenario-defect air-lock-basin|air-lock-only-east"
  "silence-north-lamp|--scenario-defect silence-north-lamp|outage-blast-radius"
  "drop-backfill|--scenario-defect drop-backfill|backfill-stored"
)

# ---------------------------------------------------------------------------
# The judge. Pure: it reads a verifier's exit status and report, and prints why it is wrong.
#   judge <invariant-or-empty> <exit-status> <report.json>
# ---------------------------------------------------------------------------
judge() {
  local want="$1" rc="$2" report="$3" passed
  if [[ -z "$want" ]]; then
    case "$rc" in
      0) return 0 ;;
      1) echo "the clean day FAILED: $(jq -r '[.invariants[] | select(.passed | not) | "\(.name): \(.detail)"] | join("; ")' "$report" 2>/dev/null)" ;;
      2) echo "the clean day has NO VERDICT: $(jq -r '.noVerdict // "no report"' "$report" 2>/dev/null)" ;;
      *) echo "the verifier exited $rc" ;;
    esac
    return 1
  fi
  case "$rc" in
    0) echo "the control PASSED: $want cannot fail"; return 1 ;;
    1) ;;
    2) echo "the control has NO VERDICT, so it shows nothing: $(jq -r '.noVerdict // "no report"' "$report" 2>/dev/null)"; return 1 ;;
    *) echo "the verifier exited $rc"; return 1 ;;
  esac
  passed="$(jq -r --arg n "$want" '[.invariants[] | select(.name == $n) | .passed] | if length == 1 then .[0] else "missing" end' "$report" 2>/dev/null || echo unreadable)"
  case "$passed" in
    false) return 0 ;;
    true) echo "the control failed, but not on $want, which stayed green: $want cannot fail" ;;
    *) echo "the report has no single $want invariant ($passed)" ;;
  esac
  return 1
}

offline_controls() {
  say "offline controls (no cluster)"
  (cd "$repo_root/backend/sims/dc-simulator" &&
    go test -count=1 -run 'TestCleanDayPassesEveryInvariant|TestEachControlTurnsExactlyItsInvariantRed|TestCorrelationJudgesANonFiringRed|TestCoverageRefusesAGapATierCouldHideIn' ./scenarioverify/ >/dev/null) ||
    fail "the verifier's own controls failed (go test ./scenarioverify/ in backend/sims/dc-simulator)"
  pass "the verifier: a clean day passes, each planted defect turns exactly its invariant red"

  local d="$state_dir/judge" out
  mkdir -p "$d"
  printf '%s\n' '{"invariants":[{"name":"flood-escalates","passed":true},{"name":"backfill-stored","passed":true}]}' >"$d/green.json"
  printf '%s\n' '{"invariants":[{"name":"flood-escalates","passed":false,"detail":"x"},{"name":"backfill-stored","passed":true}]}' >"$d/flood-red.json"
  printf '%s\n' '{"noVerdict":"the day never started"}' >"$d/none.json"
  judge "" 0 "$d/green.json" >/dev/null || fail "judge: a clean day that passed was judged wrong"
  judge flood-escalates 1 "$d/flood-red.json" >/dev/null || fail "judge: a control red on its own invariant was judged wrong"
  pass "judge: a passing clean day and a control red on its own invariant are accepted"
  local c
  for c in "|1|flood-red.json:a clean day that failed" "|2|none.json:a clean day with no verdict" \
    "flood-escalates|0|green.json:a control that passed" "backfill-stored|1|flood-red.json:a control red on another invariant" \
    "flood-escalates|2|none.json:a control with no verdict" "missing-inv|1|flood-red.json:a control whose invariant is absent"; do
    IFS='|' read -r want rc file <<<"${c%%:*}"
    if out="$(judge "$want" "$rc" "$d/$file")"; then fail "judge: ${c##*:} was accepted"; fi
    red "${c##*:} -> $out"
  done
}

# ---------------------------------------------------------------------------
# up / down
# ---------------------------------------------------------------------------
disk_guard() {
  local free
  free="$(df -Pk "$HOME" | awk 'NR==2 {printf "%d", $4/1024/1024}')"
  [[ "$free" -ge "$min_free_gb" ]] || fail "only ${free} GB free; the rig needs $min_free_gb (DC_HPRIG_MIN_FREE_GB)"
  note "disk: ${free} GB free (floor $min_free_gb)"
}

cluster_exists() { grep -qx -- "$cluster" <<<"$(kind get clusters 2>/dev/null)"; }

cmd_up() {
  ((own_cluster)) || fail "DC_HPRIG_KUBECONFIG is set: this rig uses that instance and does not create one"
  need kind; need kubectl; need docker; need jq; need go
  [[ -n "$version" ]] || need ko
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
  local images=(--build --version "hprig-$(git -C "$repo_root" rev-parse --short=8 HEAD)-$(date -u +%Y%m%dT%H%M%SZ)")
  [[ -z "$version" ]] || images=(--version "$version")
  say "installing prerequisites and bootstrapping $instance (${images[*]})"
  "$dcctl" install local --yes --cluster "$cluster" --compact "${images[@]}"
  "$dcctl" bootstrap local "$instance" --yes --no-escrow --cluster "$cluster" --host localhost --no-tls "${images[@]}"
  say "UP COMPLETE: run 'hack/harborpulse-rig.sh run'"
}

cmd_down() {
  ((own_cluster)) || fail "DC_HPRIG_KUBECONFIG is set: this rig did not create that cluster and will not delete it"
  need kind
  if cluster_exists; then
    k get ns kube-system >/dev/null 2>&1 ||
      fail "kind cluster $cluster exists but is not in $kubeconfig: not this rig's; refusing to delete it"
    local uid
    uid="$(k get ns kube-system -o jsonpath='{.metadata.uid}')"
    [[ -x "$dcctl" ]] && "$dcctl" destroy local "$instance" --yes || true
    kind delete cluster --name "$cluster" --kubeconfig "$kubeconfig"
    [[ -n "$uid" && "$uid" != */* ]] && rm -rf "${HOME:?}/.devicechain/clusters/$uid"
  fi
  rm -f "$state_dir/kubeconfig"
}

# ---------------------------------------------------------------------------
# a run
# ---------------------------------------------------------------------------
bg_pids=()
start_bg() { # start_bg <log> <command...>
  local log="$1"
  shift
  "$@" >"$log" 2>&1 &
  bg_pids+=("$!")
}
stop_all() {
  local p
  for p in "${bg_pids[@]}"; do kill "$p" 2>/dev/null || true; done
  bg_pids=()
}
trap stop_all EXIT

forward() { # forward <log> <target> <local:remote>; keeps the forward up across pod restarts
  start_bg "$state_dir/$1" bash -c "while :; do kubectl --kubeconfig '$kubeconfig' --context '$kube_context' -n '$namespace' port-forward '$2' '$3' || true; sleep 2; done"
  local port="${3%%:*}"
  for _ in $(seq 1 60); do
    curl -s -o /dev/null --max-time 2 "http://localhost:$port/" && return 0
    sleep 1
  done
  fail "nothing answered on localhost:$port (port-forward $2; see $state_dir/$1)"
}

build_tools() {
  mkdir -p "$state_dir/bin"
  (cd "$repo_root/backend/sims/dc-simulator" &&
    go build -o "$simulator" . && go build -o "$verifier" ./cmd/scenario-verify) || fail "could not build the simulator and the verifier"
  [[ -x "$dcctl" ]] || make -C "$(winpath "$repo_root")/backend/cli" build >/dev/null
}

selected() { # selected <run-name>: whether DC_HPRIG_CONTROLS includes it
  [[ "$1" == clean || "$controls" == all ]] && return 0
  [[ "$controls" == none ]] && return 1
  [[ ",$controls," == *",$1,"* ]]
}

cmd_run() {
  need kubectl; need jq; need curl; need go
  offline_controls
  k get ns "$namespace" >/dev/null 2>&1 || fail "no instance $instance on $kube_context: run 'hack/harborpulse-rig.sh up' first"
  build_tools
  forward forward-http.log svc/traefik "$http_port:80"
  forward forward-ingest.log svc/event-sources "$ingest_port:8081"

  local stamp r name flags want port i=0 sims=() hs
  stamp="$(date -u +%H%M%S)"
  for r in "${runs[@]}"; do
    IFS='|' read -r name flags want <<<"$r"
    selected "$name" || continue
    port=$((sim_port + i))
    i=$((i + 1))
    local sname="hprig-$name-$stamp"
    say "sim $sname${flags:+ ($flags)}"
    "$dcctl" sim create "$sname" -i "$instance" -s "localhost:$http_port" --kube-context "$kube_context" \
      --ingress "http://localhost:$ingest_port" --mqtt-broker "ssl://localhost:$mqtt_port" \
      --manifest harborpulse --control-addr "http://127.0.0.1:$port" >"$state_dir/$sname.create.log" 2>&1 ||
      { tail -5 "$state_dir/$sname.create.log" >&2; fail "dcctl sim create $sname failed"; }
    hs="$(winpath "$HOME/.devicechain/sims/$sname.json")"
    # shellcheck disable=SC2086 # the flags are words on purpose
    start_bg "$state_dir/$sname.sim.log" "$simulator" --handshake "$hs" --port "$port" $flags
    sims+=("$sname|$port|$want|$hs")
  done

  say "starting every sim once it has bootstrapped"
  local s sname want_inv hsp state
  for s in "${sims[@]}"; do
    IFS='|' read -r sname port want_inv hsp <<<"$s"
    state=""
    for _ in $(seq 1 120); do
      state="$(curl -s --max-time 2 "http://127.0.0.1:$port/status" | jq -r '.state' 2>/dev/null || true)"
      [[ "$state" == BOOTSTRAPPED || "$state" == RUNNING ]] && break
      sleep 5
    done
    [[ "$state" == BOOTSTRAPPED || "$state" == RUNNING ]] ||
      { tail -20 "$state_dir/$sname.sim.log" >&2; fail "$sname did not bootstrap (state '$state')"; }
    curl -s -X POST --max-time 10 "http://127.0.0.1:$port/start" >/dev/null || fail "could not start $sname"
    note "$sname running on :$port"
  done

  say "verifying (the next whole sim day of each run; about 30 minutes)"
  local vpids=()
  for s in "${sims[@]}"; do
    IFS='|' read -r sname port want_inv hsp <<<"$s"
    "$verifier" --handshake "$hsp" --sim "http://127.0.0.1:$port" --report "$(winpath "$state_dir/$sname.report.json")" \
      >"$state_dir/$sname.verify.log" 2>&1 &
    vpids+=("$!")
  done

  local bad=0 rc why idx=0
  for s in "${sims[@]}"; do
    IFS='|' read -r sname port want_inv hsp <<<"$s"
    rc=0
    wait "${vpids[$idx]}" || rc=$?
    idx=$((idx + 1))
    if why="$(judge "$want_inv" "$rc" "$state_dir/$sname.report.json")"; then
      if [[ -z "$want_inv" ]]; then
        pass "$sname: every invariant holds"
      else
        red "$sname -> $want_inv: $(jq -r --arg n "$want_inv" '.invariants[] | select(.name == $n) | .detail' "$state_dir/$sname.report.json")"
        local others
        others="$(jq -r --arg n "$want_inv" '[.invariants[] | select((.passed | not) and .name != $n) | .name] | join(", ")' "$state_dir/$sname.report.json")"
        [[ -z "$others" ]] || note "  also red (reported, not judged): $others"
      fi
    else
      printf '    FAIL %s: %s (see %s)\n' "$sname" "$why" "$state_dir/$sname.verify.log" >&2
      bad=1
    fi
  done

  stop_all
  if [[ "${DC_HPRIG_KEEP:-0}" != 1 ]]; then
    for s in "${sims[@]}"; do
      "$dcctl" sim destroy "${s%%|*}" -i "$instance" -s "localhost:$http_port" --kube-context "$kube_context" \
        >"$state_dir/${s%%|*}.destroy.log" 2>&1 || note "could not destroy ${s%%|*} (see its destroy log)"
    done
  fi
  ((bad == 0)) || fail "the harborpulse rig is red"
  say "ALL CHECKS PASSED"
}

rig_cmd="${1:-}"
case "$rig_cmd" in
  up) cmd_up ;;
  run) cmd_run ;;
  all) cmd_up; cmd_run ;;
  controls) offline_controls ;;
  down) cmd_down ;;
  *) fail "usage: hack/harborpulse-rig.sh up | run | all | controls | down" ;;
esac
