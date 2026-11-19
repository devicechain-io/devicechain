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
# telemetry, an area correlation, a store-and-forward burst, a geofence rule, a calculated metric,
# LoRaWAN gauges decoded by tenant decoders, a signed webhook, an MQTT connector, an escalating
# mail notification, dynamic groups with a group-scoped rule, CALENDAR and SOLAR campaigns with a
# holiday, a maintenance window, an asset hierarchy and a typed relationship graph through one
# 24-minute sim day. scenario-verify reads the platform back over the
# tenant GraphQL APIs as the sim's own identity, and the in-cluster sink's record, and judges:
#
#   flood-escalates             one flood alarm per gauge, raised at MINOR, escalating in place to
#                               CRITICAL on East Quay only without clearing on the way, and clearing
#   pump-commanded              start-pump SUCCESSFUL, the pump running and its well falling, the
#                               well never reaching the pump's own float
#   derate-obeyed               derate SUCCESSFUL, the cap reported at 11 kW, the connector reading
#                               below the rule's level before the long session ends, the alarm cleared
#   air-lock-only-east          pump-air-lock for the East Quay pump only, inside its window
#   correlation-fires           previewRule of quay-flooding fires for East Quay only
#   correlation-control-is-red  the same preview with a count no quay reaches is judged red (the
#                               verifier runs this control itself, in the same day)
#   outage-blast-radius         light-silent / charger-offline for exactly the East Quay devices
#   backfill-stored             every reading a charger buffered in the outage is stored at the
#                               time it was taken, and nothing else in the outage: the exact set
#   truck-fence                 truck-in-flood-plain while the truck is in the flood plain fence
#   well-fill-calculated        each pump's calculated well fill is its level over its well's depth
#   lora-decoded                each LoRaWAN gauge's uplinks decoded to exactly the level it sent
#   attribute-threshold-moves   pump alarms follow shared thresholds before/after the change
#   downlink-acknowledged       one confirmed gauge command succeeds and changes stored cadence
#   backfill-completes          a platform backfill fills a measured gap with correct values
#   decoder-failure-visible     the legacy gauge's undecodable uplinks recorded as DECODE_FAILED
#   webhook-signed              the flood webhook reached the sink for exactly the East Quay gauges,
#                               signed with the tenant's secret
#   webhook-signature-control-is-red  the same deliveries under a secret the platform never had are
#                               judged red (the verifier runs this control itself)
#   mqtt-bulletin               a flood bulletin on the tenant's MQTT topic for every gauge
#   notify-escalates            each gauge's flood alarm paged the duty officer and, unacknowledged,
#                               paged again; each page reached the sink as a mail naming that
#                               gauge and that trigger
#   notify-ack-control-is-red   the gauge the verifier acknowledged was paged and not escalated
#                               (the verifier runs this control itself)
#   groups-resolve              each dynamic group resolves to exactly the devices its attributes put in it
#   group-scoped-rule           wet-well-watch for the critical pumps only, though every well crossed it
#   night-dim-campaign          the day's CALENDAR dim reached exactly East Quay's lights, which reported 40 %
#   solar-wave                  every lamp's SOLAR instant is the sun at its platform-held position, east
#                               to west (judged on the PLANNED cycles: real dusk is not compressed)
#   holiday-skipped             the dusk campaign planned nothing for its holiday; the dawn one did
#   window-holds-flush          the DISRUPTIVE flush waited for the pump controllers' maintenance window
#   maintenance-holds-page     East Quay flicker page held through lamp service and released afterward
#   maintenance-control-pages  identical North Quay flicker pages promptly, with the hold check red
#   asset-hierarchy             meters under their buildings, pumps under their stations, each under its quay
#   feeder-blast-radius         each feeder feeds its own quay; every device the failed one feeds went silent
#
# THE SINK. The webhook, the MQTT bulletins and the mail go to ONE pod inside the cluster
# (cmd/scenario-sink in namespace harborpulse-sinks, service hp-sink), so the rig needs no route out
# of the cluster. The platform's egress guard refuses a private address unless the operator admits
# it, so `up` creates the sink BEFORE the instance and bootstraps it with
# --dev-allow-egress <the sink's ClusterIP> and --enable-area outbound-connectors. Every sim's
# deliveries land in the same sink; the verifier tells its own apart by the tenant's signing secret
# (webhooks), the tenant in the topic (MQTT) and in the recipient (mail).
#
# It is a manual rig like hack/outage-rig.sh and hack/ota-rig.sh, and it follows their one rule: a
# check is worth nothing until it has been shown to fail. So beside the clean day it runs one day
# per planted defect, each on a sim of its own (its own tenant), all at once:
#
#   control             planted as                          must turn red
#   flood-split-key     --scenario-defect flood-split-key   flood-escalates
#   flood-clear-between-tiers  --scenario-defect flood-clear-between-tiers  flood-escalates
#   no-far-end          --no-command-far-end                pump-commanded
#   refuse-derate       --scenario-defect refuse-derate     derate-obeyed
#   air-lock-basin      --scenario-defect air-lock-basin    air-lock-only-east
#   silence-north-lamp  --scenario-defect silence-north-lamp outage-blast-radius
#   drop-backfill       --scenario-defect drop-backfill     backfill-stored
#   unsigned-webhook    --scenario-defect unsigned-webhook  webhook-signed
#   mqtt-unreachable    --scenario-defect mqtt-unreachable  mqtt-bulletin
#   truck-stays-home    --scenario-defect truck-stays-home  truck-fence
#   wrong-well-depth    --scenario-defect wrong-well-depth  well-fill-calculated
#   lora-wrong-scale    --scenario-defect lora-wrong-scale  lora-decoded
#   legacy-decoder-fixed --scenario-defect legacy-decoder-fixed decoder-failure-visible
#   attribute-not-updated --scenario-defect attribute-not-updated attribute-threshold-moves
#   downlink-nack       --scenario-defect downlink-nack       downlink-acknowledged
#   backfill-wrong-window --scenario-defect backfill-wrong-window backfill-completes
#   east-lamp-mislabelled --scenario-defect east-lamp-mislabelled groups-resolve
#   unscoped-watch      --scenario-defect unscoped-watch    group-scoped-rule
#   night-dim-all-lights --scenario-defect night-dim-all-lights night-dim-campaign
#   lamp-unsited        --scenario-defect lamp-unsited      solar-wave
#   holiday-dropped     --scenario-defect holiday-dropped   holiday-skipped
#   flush-ungated       --scenario-defect flush-ungated     window-holds-flush
#   lofts-under-basin   --scenario-defect lofts-under-basin asset-hierarchy
#   feeder-miswired     --scenario-defect feeder-miswired   feeder-blast-radius
#   suppression-wrong-device --scenario-defect suppression-wrong-device maintenance-holds-page
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
# developer cluster (kind devicechain) under any context name: it resolves the context's cluster
# entry, server and CA, and the nodes its API server reports. It never changes a kubeconfig's
# current context.
# `run` deploys the sink there too (it needs ko and the cluster's localhost:5000 registry), but it
# cannot admit the sink's address to that instance's egress: the instance must run
# outbound-connectors with lpwanDownlink.enabled, event-management with calculatedBackfill.enabled
# and a TSDB pool holding persistence.writers + 1 + 4 connections, and allow the sink's ClusterIP (`dcctl upgrade --dev-allow-egress <ip>` on a
# local cluster), or every delivery is refused and the sink invariants are red.
#
# OFFLINE CONTROLS (`controls`, and run first by `run`): the verifier's own unit controls (a clean
# day passes; each planted defect turns exactly its invariant red), and this script's judge fed
# fabricated reports.
#
# Knobs: DC_HPRIG_CONTROLS=none runs the clean day alone, or a comma list of control names runs
# those (all control sims at once is a lot for a laptop); DC_HPRIG_KEEP=1 keeps the sims' tenants
# afterwards; DC_HPRIG_SIM_PORT is the first sim's control port (the rest follow);
# DC_HPRIG_SINK_PORT is the loopback port-forward to the sink.
#
# Requires: kind, kubectl, docker, jq, curl, go and ko (ko builds the sink image).
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
sink_port="${DC_HPRIG_SINK_PORT:-28182}" # loopback port-forward to the in-cluster sink
sink_ns=harborpulse-sinks
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

k() { kubectl --kubeconfig "$kubeconfig" --context "$kube_context" "$@"; }

# ---------------------------------------------------------------------------
# The shared developer cluster (kind "devicechain") is never this rig's, under ANY name. A context
# is only a label: an alias for it in another kubeconfig reaches the same API server. So the guard
# resolves what the selected context actually points at (its cluster entry, server and CA) and
# compares that with the shared cluster's own (from kind, and from the default kubeconfig); and
# before changing anything it asks the API server for its nodes, which kind names
# <cluster>-control-plane whatever a kubeconfig calls the cluster.
# ---------------------------------------------------------------------------
refuse_shared() { echo "refusing: $* -- that is the shared developer cluster (kind devicechain)" >&2; exit 2; }

# ctx_identity <kubeconfig> <context>: "<cluster entry>|<server>|<CA data>" of what the context
# points at, or nothing when the kubeconfig has no such context.
ctx_identity() {
  kubectl config view --kubeconfig "$1" --context "$2" --minify --raw \
    -o jsonpath='{.contexts[0].context.cluster}|{.clusters[0].cluster.server}|{.clusters[0].cluster.certificate-authority-data}' 2>/dev/null || true
}

# norm_server: one spelling per loopback server, so localhost:N and 127.0.0.1:N compare equal.
norm_server() {
  local s="${1%/}"
  s="${s/:\/\/localhost:/://127.0.0.1:}"
  s="${s/:\/\/\[::1\]:/://127.0.0.1:}"
  printf '%s' "$s"
}

bounded() { if command -v timeout >/dev/null 2>&1; then timeout 60 "$@"; else "$@"; fi; }

guard_not_shared() {
  [[ "$cluster" != devicechain && "$kube_context" != kind-devicechain ]] || refuse_shared "cluster '$cluster', context '$kube_context'"
  [[ -f "$kubeconfig" ]] || return 0 # this rig's own cluster, not created yet
  local mine entry server ca
  mine="$(ctx_identity "$kubeconfig" "$kube_context")"
  [[ -n "$mine" ]] || return 0
  IFS='|' read -r entry server ca <<<"$mine"
  [[ "$entry" != kind-devicechain ]] || refuse_shared "context '$kube_context' points at the cluster entry kind-devicechain"
  local shared_cfg="$state_dir/shared-cluster.kubeconfig" src theirs s_entry s_server s_ca
  for src in default kind; do
    theirs=""
    if [[ "$src" == kind ]]; then
      # Bounded: kind asks docker, which can be slow to answer for a cluster that is stopped.
      if command -v kind >/dev/null 2>&1 && bounded kind get kubeconfig --name devicechain >"$shared_cfg" 2>/dev/null; then
        theirs="$(ctx_identity "$(winpath "$shared_cfg")" kind-devicechain)"
      fi
      rm -f "$shared_cfg"
    elif [[ -f "$HOME/.kube/config" ]]; then
      theirs="$(ctx_identity "$(winpath "$HOME/.kube/config")" kind-devicechain)"
    fi
    [[ -n "$theirs" ]] || continue
    IFS='|' read -r s_entry s_server s_ca <<<"$theirs"
    [[ -z "$s_server" || "$(norm_server "$server")" != "$(norm_server "$s_server")" ]] ||
      refuse_shared "context '$kube_context' points at $server, the shared cluster's server (from $src, entry $s_entry)"
    [[ -z "$s_ca" || "$ca" != "$s_ca" ]] ||
      refuse_shared "context '$kube_context' trusts the shared cluster's CA (from $src, entry $s_entry)"
  done
}

# guard_live: the API server the context reaches is not the shared cluster. Run before anything
# that changes a cluster. An unreachable one passes: nothing can be changed through it.
guard_live() {
  local nodes
  nodes="$(k get nodes -o name --request-timeout=10s 2>/dev/null || true)"
  if grep -Eq '^node/devicechain-(control-plane|worker[0-9]*)$' <<<"$nodes"; then
    refuse_shared "context '$kube_context' reaches nodes $(tr '\n' ' ' <<<"$nodes")"
  fi
}

guard_not_shared

# The runs: name | simulator flags | the invariant it must turn red ("" for the clean day).
runs=(
  "clean||"
  "attribute-not-updated|--scenario-defect attribute-not-updated|attribute-threshold-moves"
  "downlink-nack|--scenario-defect downlink-nack|downlink-acknowledged"
  "backfill-wrong-window|--scenario-defect backfill-wrong-window|backfill-completes"
  "flood-split-key|--scenario-defect flood-split-key|flood-escalates"
  "flood-clear-between-tiers|--scenario-defect flood-clear-between-tiers|flood-escalates"
  "no-far-end|--no-command-far-end|pump-commanded"
  "refuse-derate|--scenario-defect refuse-derate|derate-obeyed"
  "air-lock-basin|--scenario-defect air-lock-basin|air-lock-only-east"
  "silence-north-lamp|--scenario-defect silence-north-lamp|outage-blast-radius"
  "drop-backfill|--scenario-defect drop-backfill|backfill-stored"
  "unsigned-webhook|--scenario-defect unsigned-webhook|webhook-signed"
  "mqtt-unreachable|--scenario-defect mqtt-unreachable|mqtt-bulletin"
  "truck-stays-home|--scenario-defect truck-stays-home|truck-fence"
  "wrong-well-depth|--scenario-defect wrong-well-depth|well-fill-calculated"
  "lora-wrong-scale|--scenario-defect lora-wrong-scale|lora-decoded"
  "legacy-decoder-fixed|--scenario-defect legacy-decoder-fixed|decoder-failure-visible"
  "east-lamp-mislabelled|--scenario-defect east-lamp-mislabelled|groups-resolve"
  "unscoped-watch|--scenario-defect unscoped-watch|group-scoped-rule"
  "night-dim-all-lights|--scenario-defect night-dim-all-lights|night-dim-campaign"
  "lamp-unsited|--scenario-defect lamp-unsited|solar-wave"
  "holiday-dropped|--scenario-defect holiday-dropped|holiday-skipped"
  "flush-ungated|--scenario-defect flush-ungated|window-holds-flush"
  "lofts-under-basin|--scenario-defect lofts-under-basin|asset-hierarchy"
  "feeder-miswired|--scenario-defect feeder-miswired|feeder-blast-radius"
  "suppression-wrong-device|--scenario-defect suppression-wrong-device|maintenance-holds-page"
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
    go test -count=1 -run 'TestCleanDayPassesEveryInvariant|TestEachControlTurnsExactlyItsInvariantRed|TestEachS3ControlTurnsExactlyItsInvariantRed|TestEachS3bControlTurnsExactlyItsInvariantRed|TestEachS4ControlTurnsExactlyItsInvariantRed|TestWithoutASinkTheSinkInvariantsAreNotJudged|TestCorrelationJudgesANonFiringRed|TestCoverageRefusesAGapATierCouldHideIn|TestFoldedDecodeFailures|TestFoldedGrowthOfOne|TestNotificationAttemptsAreJudgedOnTheWatchedDayOnly|TestDutyMailIsAttributed|TestMaintenance|TestHarborLamp' ./scenarioverify/ ./sim/ >/dev/null &&
    go test -count=1 ./scenariosink/ >/dev/null) ||
    fail "the verifier's or the sink's own controls failed (go test ./scenarioverify/ ./scenariosink/ in backend/sims/dc-simulator)"
  pass "the verifier: a clean day passes, each planted defect turns exactly its invariant red"
  pass "the sink: what real MQTT, SMTP and HTTP clients deliver is recorded exactly"

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

# deploy_sinks builds the sink image into the cluster's local registry and runs it; prints nothing.
deploy_sinks() {
  need ko
  local tag image
  tag="hprig-sink-$(git -C "$repo_root" rev-parse --short=8 HEAD)-$(date -u +%Y%m%dT%H%M%SZ)"
  image="localhost:5000/scenario-sink:$tag"
  (cd "$repo_root/backend/sims/dc-simulator" &&
    KO_DOCKER_REPO=localhost:5000/scenario-sink \
      "$repo_root/hack/ko-build.sh" --bare --tags "$tag" --platform linux/amd64 ./cmd/scenario-sink >/dev/null) ||
    fail "could not build the sink image (ko, and the localhost:5000 registry dcctl install creates)"
  k apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: $sink_ns
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hp-sink
  namespace: $sink_ns
spec:
  replicas: 1
  selector:
    matchLabels: {app: hp-sink}
  template:
    metadata:
      labels: {app: hp-sink}
    spec:
      containers:
        - name: sink
          image: $image
          args: ["--http", ":8080", "--mqtt", ":1883", "--smtp", ":2525"]
          ports:
            - {name: http, containerPort: 8080}
            - {name: mqtt, containerPort: 1883}
            - {name: smtp, containerPort: 2525}
          readinessProbe:
            httpGet: {path: /healthz, port: 8080}
---
apiVersion: v1
kind: Service
metadata:
  name: hp-sink
  namespace: $sink_ns
spec:
  selector: {app: hp-sink}
  ports:
    - {name: http, port: 8080, targetPort: 8080}
    - {name: mqtt, port: 1883, targetPort: 1883}
    - {name: smtp, port: 2525, targetPort: 2525}
EOF
  k -n "$sink_ns" rollout status deploy/hp-sink --timeout 180s >/dev/null || fail "the sink did not become ready"
}

# sink_ip is the sink Service's ClusterIP: the address the platform dials, and so the one its egress
# guard must admit.
sink_ip() { k -n "$sink_ns" get svc hp-sink -o jsonpath='{.spec.clusterIP}'; }

cmd_up() {
  ((own_cluster)) || fail "DC_HPRIG_KUBECONFIG is set: this rig uses that instance and does not create one"
  need kind; need kubectl; need docker; need jq; need go
  need ko
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
  say "installing prerequisites (${images[*]})"
  "$dcctl" install local --yes --cluster "$cluster" --compact "${images[@]}"
  # The sink first: the instance's egress allowance names its address.
  say "deploying the scenario sink"
  deploy_sinks
  local ip
  ip="$(sink_ip)"
  [[ "$ip" =~ ^[0-9.]+$ ]] || fail "the sink has no ClusterIP ($ip)"
  note "sink at $ip; the instance admits it with --dev-allow-egress"
  # The chart derives command-delivery routing and event-sources' acknowledgement feed
  # from the outbound adapter switch. Never hand-set those derived flags.
  # The default profile and --compact keep the service pool at 20 and writers at 10:
  # 10 writers + 1 backfill worker + 4 spare connections fit without a pool overlay.
  cat >"$state_dir/harborpulse-values.yaml" <<'VALUES'
functionalAreas:
  outbound-connectors:
    config:
      lpwanDownlink:
        enabled: true
  event-management:
    config:
      calculatedBackfill:
        enabled: true
VALUES
  say "bootstrapping $instance with LPWAN downlink and calculated backfill"
  "$dcctl" bootstrap local "$instance" --yes --no-escrow --cluster "$cluster" --host localhost --no-tls \
    --enable-area outbound-connectors --dev-allow-egress "$ip" --values "$(winpath "$state_dir/harborpulse-values.yaml")" "${images[@]}"
  say "UP COMPLETE: run 'hack/harborpulse-rig.sh run'"
}

cmd_down() {
  ((own_cluster)) || fail "DC_HPRIG_KUBECONFIG is set: this rig did not create that cluster and will not delete it"
  need kind
  if cluster_exists; then
    guard_live
    k get ns kube-system >/dev/null 2>&1 ||
      fail "kind cluster $cluster exists but is not in $kubeconfig: not this rig's; refusing to delete it"
    local uid
    uid="$(k get ns kube-system -o jsonpath='{.metadata.uid}')"
    # --kube-context pins the destroy to this rig's cluster: without it dcctl falls back to the
    # cluster recorded at bootstrap, and a record from another run of the same instance name could
    # point elsewhere. dcctl refuses a context that differs from its record, which is the point.
    [[ -x "$dcctl" ]] && "$dcctl" destroy local "$instance" --yes --kube-context "$kube_context" || true
    kind delete cluster --name "$cluster" --kubeconfig "$kubeconfig"
    [[ -n "$uid" && "$uid" != */* ]] && rm -rf "${HOME:?}/.devicechain/clusters/$uid"
  fi
  rm -f "$state_dir/kubeconfig"
}

# ---------------------------------------------------------------------------
# a run
# ---------------------------------------------------------------------------
# Every background process starts in a process group of its own (job control is on while it
# starts; `setsid` is not in Git Bash) and is stopped as a group. Killing only a port-forward
# loop's bash leaves its foreground kubectl listening, which a later run's readiness probe then
# takes for its own forward. Sims and verifiers are stopped before the tenants are destroyed; the
# forwards, which the destroy goes through, after. The exit trap keeps that order, so a run that
# fails part-way still removes the tenants it created (unless DC_HPRIG_KEEP=1).
bg_pids=()      # sims and verifiers
fwd_pids=()     # port-forward loops
created_sims=() # every sim this run asked dcctl to create, whose tenant it must destroy
last_bg=""
start_bg() { # start_bg <log> <command...>; leaves the pid (= its process group) in last_bg
  local log="$1"
  shift
  set -m
  "$@" </dev/null >"$log" 2>&1 &
  last_bg="$!"
  set +m
}
stop_group() { # stop_group <pid...>: TERM each process group, then reap its leader
  local p
  for p in "$@"; do kill -TERM -- "-$p" 2>/dev/null || kill -TERM "$p" 2>/dev/null || true; done
  for p in "$@"; do wait "$p" 2>/dev/null || true; done
}
stop_procs() {
  ((${#bg_pids[@]} == 0)) || stop_group "${bg_pids[@]}"
  bg_pids=()
}
stop_forwards() {
  ((${#fwd_pids[@]} == 0)) || stop_group "${fwd_pids[@]}"
  fwd_pids=()
}
destroy_sims() { # destroy every sim's tenant this run created, once; a failure is noted, not fatal
  local sname
  if [[ "${DC_HPRIG_KEEP:-0}" != 1 ]]; then
    for sname in ${created_sims[@]+"${created_sims[@]}"}; do
      "$dcctl" sim destroy "$sname" -i "$instance" -s "localhost:$http_port" --kube-context "$kube_context" \
        >"$state_dir/$sname.destroy.log" 2>&1 || note "could not destroy $sname (see its destroy log)"
    done
  fi
  created_sims=()
}
stop_all() {
  stop_procs
  destroy_sims
  stop_forwards
}
trap stop_all EXIT

forward() { # forward <log> <namespace> <target> <local:remote>; keeps the forward up across pod restarts
  local port="${4%%:*}"
  ! curl -s -o /dev/null --max-time 2 "http://localhost:$port/" ||
    fail "something already answers on localhost:$port before its port-forward started (a forward left by another run?)"
  start_bg "$state_dir/$1" bash -c "while :; do kubectl --kubeconfig '$kubeconfig' --context '$kube_context' -n '$2' port-forward '$3' '$4' || true; sleep 2; done"
  fwd_pids+=("$last_bg")
  for _ in $(seq 1 60); do
    curl -s -o /dev/null --max-time 2 "http://localhost:$port/" && return 0
    sleep 1
  done
  fail "nothing answered on localhost:$port (port-forward $3; see $state_dir/$1)"
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
  guard_live
  k get ns "$namespace" >/dev/null 2>&1 || fail "no instance $instance on $kube_context: run 'hack/harborpulse-rig.sh up' first"
  build_tools
  if ! k -n "$sink_ns" get deploy hp-sink >/dev/null 2>&1; then
    say "deploying the scenario sink"
    deploy_sinks
    note "the sink is at $(sink_ip): the instance must admit it (dcctl upgrade --dev-allow-egress) and run outbound-connectors"
  fi
  forward forward-http.log traefik svc/traefik "$http_port:80"
  forward forward-ingest.log "$namespace" svc/event-sources "$ingest_port:8081"
  forward forward-sink.log "$sink_ns" svc/hp-sink "$sink_port:8080"

  local stamp r name flags want port i=0 sims=() hs
  stamp="$(date -u +%H%M%S)"
  for r in "${runs[@]}"; do
    IFS='|' read -r name flags want <<<"$r"
    selected "$name" || continue
    port=$((sim_port + i))
    i=$((i + 1))
    local sname="hprig-$name-$stamp"
    say "sim $sname${flags:+ ($flags)}"
    # Registered before the create: a create that fails part-way can still leave its tenant.
    created_sims+=("$sname")
    "$dcctl" sim create "$sname" -i "$instance" -s "localhost:$http_port" --kube-context "$kube_context" \
      --ingress "http://localhost:$ingest_port" --mqtt-broker "ssl://localhost:$mqtt_port" \
      --manifest harborpulse --control-addr "http://127.0.0.1:$port" >"$state_dir/$sname.create.log" 2>&1 ||
      { tail -5 "$state_dir/$sname.create.log" >&2; fail "dcctl sim create $sname failed"; }
    hs="$(winpath "$HOME/.devicechain/sims/$sname.json")"
    # shellcheck disable=SC2086 # the flags are words on purpose
    start_bg "$state_dir/$sname.sim.log" env "DC_SIM_HARBORPULSE_SINK_URL=http://127.0.0.1:$sink_port" "$simulator" --handshake "$hs" --port "$port" $flags
    bg_pids+=("$last_bg")
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
    start_bg "$state_dir/$sname.verify.log" "$verifier" --handshake "$hsp" --sim "http://127.0.0.1:$port" \
      --sink "http://127.0.0.1:$sink_port" --report "$(winpath "$state_dir/$sname.report.json")"
    bg_pids+=("$last_bg")
    vpids+=("$last_bg")
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

  stop_procs
  destroy_sims
  stop_forwards
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
