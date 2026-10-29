#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Tenant-deletion duration for a LARGE tenant, measured INSIDE a kind cluster.
#
#   hack/purge-measure-kind.sh                      # 3M events (2.7M measurements), 10k devices
#   PM_EVENTS=5000000 hack/purge-measure-kind.sh
#
# hack/purge-measure.sh runs the same measurement against two throwaway containers on
# loopback. This one runs the SAME test (backend/tools/migrationdiff, TestPurgeMeasurement)
# as a pod in a kind cluster, against a TimescaleDB and a NATS JetStream server that are also
# pods in it, so every call crosses the cluster network and every byte lands on a
# PersistentVolume. It is still one server of each, unreplicated: read it as an upper bound on
# what a single-node instance does and a lower bound for a clustered one.
#
# The cluster is DEDICATED. The script refuses any cluster whose name does not start with
# `dc-erase`, passes its own kubeconfig to every kubectl call (it never reads or changes the
# current context), and removes only the namespace and the cluster it created.
#
# Knobs: PM_KIND_CLUSTER (dc-erase), PM_KUBECONFIG (~/.kube/dc-erase.yaml), PM_KEEP=1 to leave
# the cluster up, plus every PM_* knob hack/purge-measure.sh documents. PM_REPORT is where the
# JSON report is written (default ./purge-measure-kind-report.json).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER="${PM_KIND_CLUSTER:-dc-erase}"
case "$CLUSTER" in dc-erase*) ;; *) echo "refusing cluster '$CLUSTER': this rig only owns clusters named dc-erase*" >&2; exit 1 ;; esac
export KUBECONFIG="${PM_KUBECONFIG:-$HOME/.kube/dc-erase.yaml}"
NS=purge-measure
PG_IMAGE="${PM_PG_IMAGE:-timescale/timescaledb:latest-pg16@sha256:61f891691050da6032023c01ea885730eeeba06b7c17b403e7d0b9c49c37dfe9}"
NATS_IMAGE="${PM_NATS_IMAGE:-nats:2.12-alpine@sha256:b270f5e2428354c0335612694d7dd2fb588148e567a5757fdff325ef9c9332e6}"
REPORT="${PM_REPORT:-$PWD/purge-measure-kind-report.json}"
BIN="$(mktemp -d)/purge-measure.test"
CREATED=0

cleanup() {
  if [ "$CREATED" = 1 ] && [ "${PM_KEEP:-0}" != 1 ]; then
    kind delete cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

PG_IMAGE="$("$ROOT/hack/docker-pull.sh" "$PG_IMAGE")"
NATS_IMAGE="$("$ROOT/hack/docker-pull.sh" "$NATS_IMAGE")"

echo "==> building the measurement binary (linux/amd64, static)"
(cd "$ROOT/backend/tools/migrationdiff" &&
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -tags purgemeasure -o "$BIN" .)

if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  echo "cluster $CLUSTER already exists; reusing it (and not deleting it on exit)"
else
  echo "==> creating kind cluster $CLUSTER (kubeconfig $KUBECONFIG)"
  kind create cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" --wait 120s
  CREATED=1
fi
# `kind load` cannot import these from a Docker engine that uses the containerd image store (it
# fails with "content digest ... not found" on --all-platforms), so the pinned images are tagged
# locally and streamed into the node with a single-platform import.
PG_REF=dc-erase-pm/timescaledb:pinned
NATS_REF=dc-erase-pm/nats:pinned
docker tag "$PG_IMAGE" "$PG_REF"
docker tag "$NATS_IMAGE" "$NATS_REF"
for img in "$PG_REF" "$NATS_REF"; do
  docker save "$img" | docker exec --privileged -i "$CLUSTER-control-plane" ctr --namespace=k8s.io images import --platform linux/amd64 - >/dev/null
done

# On Windows (git bash) kubectl is a native binary: the commands that must not have their
# arguments path-converted (cp and exec carry in-pod paths) are given the native form of the
# local paths instead.
if command -v cygpath >/dev/null 2>&1; then KCFG="$(cygpath -m "$KUBECONFIG")"; LBIN="$(cygpath -m "$BIN")"; else KCFG="$KUBECONFIG"; LBIN="$BIN"; fi
K="kubectl --kubeconfig $KUBECONFIG -n $NS"
KN="kubectl --kubeconfig $KCFG -n $NS"
# A previous run's namespace (and its volumes) is removed first: leftover streams and rows would be
# seeded on top of, not measured from scratch.
kubectl --kubeconfig "$KUBECONFIG" delete namespace "$NS" --ignore-not-found --wait=true >/dev/null
kubectl --kubeconfig "$KUBECONFIG" create namespace "$NS" --dry-run=client -o yaml | kubectl --kubeconfig "$KUBECONFIG" apply -f - >/dev/null

kubectl --kubeconfig "$KUBECONFIG" -n "$NS" apply -f - <<EOF
apiVersion: v1
kind: Service
metadata: {name: pg}
spec: {selector: {app: pg}, ports: [{port: 5432}]}
---
apiVersion: v1
kind: Service
metadata: {name: nats}
spec: {selector: {app: nats}, ports: [{port: 4222}]}
---
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: pg}
spec:
  serviceName: pg
  replicas: 1
  selector: {matchLabels: {app: pg}}
  template:
    metadata: {labels: {app: pg}}
    spec:
      containers:
        - name: pg
          image: $PG_REF
          imagePullPolicy: IfNotPresent
          args: ["-c", "max_wal_size=8GB", "-c", "checkpoint_timeout=15min"]
          env:
            - {name: POSTGRES_PASSWORD, value: postgres}
            - {name: PGDATA, value: /var/lib/postgresql/data/pgdata}
          volumeMounts: [{name: data, mountPath: /var/lib/postgresql/data}]
  volumeClaimTemplates:
    - metadata: {name: data}
      spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 40Gi}}}
---
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: nats}
spec:
  serviceName: nats
  replicas: 1
  selector: {matchLabels: {app: nats}}
  template:
    metadata: {labels: {app: nats}}
    spec:
      containers:
        - name: nats
          image: $NATS_REF
          imagePullPolicy: IfNotPresent
          args: ["-js", "-sd", "/data"]
          volumeMounts: [{name: data, mountPath: /data}]
  volumeClaimTemplates:
    - metadata: {name: data}
      spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 20Gi}}}
---
apiVersion: v1
kind: Pod
metadata: {name: runner, labels: {app: runner}}
spec:
  containers:
    - name: runner
      image: $PG_REF
      imagePullPolicy: IfNotPresent
      command: ["sleep", "infinity"]
      volumeMounts: [{name: scratch, mountPath: /scratch}]
  volumes: [{name: scratch, emptyDir: {}}]
EOF

$K rollout status statefulset/pg --timeout=300s
$K rollout status statefulset/nats --timeout=300s
$K wait --for=condition=Ready pod/runner --timeout=300s
# The image runs a temporary server to initialise, stops it, then starts the real one.
for _ in $(seq 1 120); do
  if [ "$($K logs pg-0 2>&1 | grep -c 'ready to accept connections')" -ge 2 ]; then ok=1; break; fi
  sleep 2
done
[ "${ok:-}" = 1 ] || { echo "Postgres did not become ready" >&2; exit 1; }

echo "==> node: $(kubectl --kubeconfig "$KUBECONFIG" get nodes -o jsonpath='{.items[0].status.nodeInfo.kubeletVersion} {.items[0].status.capacity.cpu}cpu {.items[0].status.capacity.memory}')"
(cd "$(dirname "$BIN")" && MSYS_NO_PATHCONV=1 $KN cp "$(basename "$BIN")" runner:/scratch/purge-measure.test)
MSYS_NO_PATHCONV=1 $KN exec runner -- chmod +x /scratch/purge-measure.test

ENVS="DC_IT_PGHOST=pg DC_IT_PGPORT=5432 PM_NATS_URL=nats://nats:4222 PM_REPORT=/scratch/report.json"
for v in PM_DEVICES PM_EVENTS PM_SPAN_DAYS PM_BROKER_MSGS PM_KV_KEYS_PER_DEVICE PM_BLOBS PM_COMPRESS; do
  [ -n "${!v:-}" ] && ENVS="$ENVS $v=${!v}"
done
echo "==> running the measurement ($ENVS)"
MSYS_NO_PATHCONV=1 $KN exec runner -- sh -c "cd /scratch && env $ENVS ./purge-measure.test -test.run '^TestPurgeMeasurement\$' -test.v -test.timeout 4h"
MSYS_NO_PATHCONV=1 $KN exec runner -- cat /scratch/report.json >"$REPORT"
echo "==> report: $REPORT"
