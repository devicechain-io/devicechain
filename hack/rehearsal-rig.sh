#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Rehearsal rig: dc-loadtest-mqtt --target against a broker that is NOT DeviceChain.
#
# It starts a kind cluster of its own, runs an authenticated Mosquitto in it, and drives a bring-
# your-own fleet at it through a port-forward:
#
#   1. CONTROL (positive): the fleet publishes straight to Mosquitto, the tap reads it back, and
#      the run must exit 0 with every PUBACKed message delivered.
#   2. PLANTED DEFECT (negative control): the fleet publishes through dc-loadtest-dropproxy at
#      N=100, which PUBACKs every 100th publish and never forwards it. The run must exit 3 and
#      its "lost" count must equal, exactly, the number the proxy says it dropped.
#
# A rig that has only ever passed may be unable to fail; step 2 is what makes step 1 mean
# something. Both must hold or the rig exits non-zero.
#
# Manual, needs docker and kind. It uses a lane-private kubeconfig and its own cluster name, and
# deletes only that cluster; it never touches another kind cluster or another container.
#
#   hack/rehearsal-rig.sh                # run both halves
#   KEEP=1 hack/rehearsal-rig.sh         # leave the cluster up for inspection
#
# RIG_BACKEND=docker runs the same Mosquitto as one plain container instead of in kind, for a host
# where a kind node will not come up; the verdicts under test are identical.
#
# Knobs (environment): DEVICES (20), RATE (200), DURATION (20s), DROP_EVERY (100).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SIM="$ROOT/backend/sims/dc-simulator"

# Pinned by digest (hack/check-image-pins.sh); the tag says which version a bump would move.
MOSQUITTO_IMAGE="eclipse-mosquitto:2.0.21@sha256:94f5a3d7deafa59fa3440d227ddad558f59d293c612138de841eec61bfa4d353"

DEVICES="${DEVICES:-20}"
RATE="${RATE:-200}"
DURATION="${DURATION:-20s}"
DROP_EVERY="${DROP_EVERY:-100}"
CLUSTER="dc-rehearsal-rig-$$"
CONTAINER="dc-rehearsal-mosquitto-$$"
BROKER_PORT="${BROKER_PORT:-11883}"
PROXY_PORT="${PROXY_PORT:-11884}"

docker version >/dev/null 2>&1 || { echo "docker is not answering; start it first" >&2; exit 2; }
tools="go jq"; [ "${RIG_BACKEND:-kind}" = docker ] || tools="$tools kind kubectl"
for tool in $tools; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 2; }
done

tmp="$(mktemp -d)"
export KUBECONFIG="$tmp/kubeconfig" # lane-private: never the shared current context
pf_pid="" proxy_pid=""

cleanup() {
  local rc=$?
  [ -z "$pf_pid" ] || kill "$pf_pid" 2>/dev/null || true
  [ -z "$proxy_pid" ] || kill "$proxy_pid" 2>/dev/null || true
  if [ "${KEEP:-}" = 1 ]; then
    echo "KEEP=1: cluster $CLUSTER left up; KUBECONFIG=$tmp/kubeconfig" >&2
  else
    if [ "${RIG_BACKEND:-kind}" = docker ]; then
      docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    else
      kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
    fi
    rm -rf "$tmp"
  fi
  exit "$rc"
}
trap cleanup EXIT

fail() { echo "RIG FAILED: $*" >&2; exit 1; }

echo "== build"
(cd "$SIM" && go build -o "$tmp/dc-loadtest-mqtt" ./cmd/loadtest-mqtt && go build -o "$tmp/dc-loadtest-dropproxy" ./cmd/loadtest-dropproxy)

echo "== credentials"
{
  echo "device,username,password"
  for i in $(seq 1 "$DEVICES"); do
    printf 'rig-%03d,dev%03d,%s\n' "$i" "$i" "$(head -c 12 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')"
  done
} >"$tmp/creds.csv"
tap_pass="$(head -c 12 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')"
printf '%s' "$tap_pass" >"$tmp/tap.pass"

winpath() { if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s' "$1"; fi; }
# The password file is built inside the broker's own image, so the broker is exactly what it
# claims to be: an off-the-shelf Mosquitto with real authentication on.
users="tap:$tap_pass"
while IFS=, read -r _ u p; do users="$users $u:$p"; done < <(tail -n +2 "$tmp/creds.csv")

if [ "${RIG_BACKEND:-kind}" = docker ]; then
echo "== mosquitto container $CONTAINER (RIG_BACKEND=docker)"
mkdir -p "$tmp/mosq"
cat >"$tmp/mosq/mosquitto.conf" <<'CONF'
listener 1883
allow_anonymous false
password_file /work/passwd
persistence false
max_queued_messages 0
max_inflight_messages 0
max_keepalive 65535
CONF
docker pull "$MOSQUITTO_IMAGE" >/dev/null
MSYS_NO_PATHCONV=1 docker run -d --name "$CONTAINER" -p "127.0.0.1:$BROKER_PORT:1883"   -e "USERS=$users" -v "$(winpath "$tmp/mosq"):/etc/mosquitto-conf:ro"   --entrypoint /bin/sh "$MOSQUITTO_IMAGE" -c   'mkdir -p /work && : > /work/passwd && for pair in $USERS; do mosquitto_passwd -b /work/passwd "${pair%%:*}" "${pair#*:}"; done && exec mosquitto -c /etc/mosquitto-conf/mosquitto.conf' >/dev/null
else
echo "== kind cluster $CLUSTER"
kind create cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" --wait 120s

kubectl create namespace rig >/dev/null
kubectl -n rig create secret generic users --from-literal=users="$users" >/dev/null
kubectl -n rig apply -f - <<EOF >/dev/null
apiVersion: v1
kind: ConfigMap
metadata: {name: mosquitto}
data:
  mosquitto.conf: |
    listener 1883
    allow_anonymous false
    password_file /work/passwd
    persistence false
    # A tap that is slower than the broker must not be read as the broker's loss.
    max_queued_messages 0
    max_inflight_messages 0
    max_keepalive 65535
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: mosquitto}
spec:
  replicas: 1
  selector: {matchLabels: {app: mosquitto}}
  template:
    metadata: {labels: {app: mosquitto}}
    spec:
      initContainers:
        - name: passwd
          image: $MOSQUITTO_IMAGE
          command: ["/bin/sh", "-c"]
          args:
            - |
              : > /work/passwd
              for pair in \$USERS; do mosquitto_passwd -b /work/passwd "\${pair%%:*}" "\${pair#*:}"; done
          env:
            - name: USERS
              valueFrom: {secretKeyRef: {name: users, key: users}}
          volumeMounts: [{name: work, mountPath: /work}]
      containers:
        - name: mosquitto
          image: $MOSQUITTO_IMAGE
          command: ["mosquitto", "-c", "/etc/mosquitto-conf/mosquitto.conf"]
          ports: [{containerPort: 1883}]
          volumeMounts:
            - {name: work, mountPath: /work}
            - {name: conf, mountPath: /etc/mosquitto-conf}
      volumes:
        - {name: work, emptyDir: {}}
        - {name: conf, configMap: {name: mosquitto}}
---
apiVersion: v1
kind: Service
metadata: {name: mosquitto}
spec:
  selector: {app: mosquitto}
  ports: [{port: 1883}]
EOF
# The node pulls the digest-pinned image itself (kind load cannot import a digest-addressed image
# from a containerd-backed Docker store).
kubectl -n rig rollout status deployment/mosquitto --timeout=180s

fi

if [ "${RIG_BACKEND:-kind}" != docker ]; then
  kubectl -n rig port-forward svc/mosquitto "$BROKER_PORT:1883" >"$tmp/pf.log" 2>&1 &
  pf_pid=$!
fi
for _ in $(seq 1 30); do
  (exec 3<>"/dev/tcp/127.0.0.1/$BROKER_PORT") 2>/dev/null && break
  sleep 1
done

common=(--credentials "$tmp/creds.csv" --tap-username tap --tap-password-file "$tmp/tap.pass"
        --rate "$RATE" --duration "$DURATION" --identity full --qos 1 --report-interval 5s
        --min-accepted 100 --quiesce-timeout 30s)

echo "== 1/2 CONTROL: straight to Mosquitto, must pass"
set +e
"$tmp/dc-loadtest-mqtt" --target "mqtt://127.0.0.1:$BROKER_PORT" "${common[@]}" --report "$tmp/control.json"
control_rc=$?
set -e
[ "$control_rc" -eq 0 ] || fail "the control run exited $control_rc, want 0 (Mosquitto delivered everything it PUBACKed; see $tmp/control.json)"
jq -e '.loss == false and .storage.lost == 0 and .storage.lostExact == true and .storage.promised > 0 and .storage.persisted == .storage.promised' "$tmp/control.json" >/dev/null ||
  fail "the control report does not show an exact, lossless run"

echo "== 2/2 PLANTED DEFECT: through the drop proxy at N=$DROP_EVERY, must exit 3 with the exact count"
"$tmp/dc-loadtest-dropproxy" --listen "127.0.0.1:$PROXY_PORT" --upstream "127.0.0.1:$BROKER_PORT" \
  --every "$DROP_EVERY" --report "$tmp/proxy.json" 2>"$tmp/proxy.log" &
proxy_pid=$!
sleep 1
set +e
"$tmp/dc-loadtest-mqtt" --target "mqtt://127.0.0.1:$PROXY_PORT" --tap-target "mqtt://127.0.0.1:$BROKER_PORT" \
  "${common[@]}" --report "$tmp/planted.json"
planted_rc=$?
set -e
sleep 1 # the proxy rewrites its count every 250ms
kill "$proxy_pid" 2>/dev/null || true
wait "$proxy_pid" 2>/dev/null || true
proxy_pid=""

[ "$planted_rc" -eq 3 ] || fail "the planted run exited $planted_rc, want 3 (LOSS); the rig cannot fail, so its pass above means nothing"
dropped="$(jq -r .dropped "$tmp/proxy.json")"
lost="$(jq -r .storage.lost "$tmp/planted.json")"
[ "$dropped" -gt 0 ] || fail "the proxy dropped nothing"
[ "$lost" = "$dropped" ] || fail "the verdict says $lost lost, the proxy dropped $dropped"
jq -e '.storage.lostExact == true' "$tmp/planted.json" >/dev/null || fail "the loss was not named exactly"

echo "RIG OK: control passed; the planted defect exited 3 with exactly $dropped missing (N=$DROP_EVERY)"
