#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Stand up the full DeviceChain stack on a local kind cluster, end to end.
#
# DEFAULT: deploys PUBLISHED images (ghcr.io/devicechain-io/<area>:$VERSION) — no
# image build, no local registry, mirrors what an end user gets. Set BUILD_IMAGES=1
# for the developer path (build from source via ko into a local registry).
#
# This performs the steps directly; the `dcctl bootstrap local` pipeline (ADR-032)
# is being implemented to automate exactly this sequence, after which the
# INFRA→CORE→CHART→SEED block collapses to one `dcctl bootstrap local` call.
#
# Idempotent: safe to re-run. Override behaviour via the env vars below.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
TF_DIR="$ROOT/deploy/opentofu"
CHART_DIR="$ROOT/deploy/helm/devicechain"
OPERATOR_DIR="$ROOT/backend/k8s"

# ---- config ----------------------------------------------------------------
CLUSTER=${CLUSTER:-devicechain}
CONTEXT="kind-${CLUSTER}"
INSTANCE=${INSTANCE:-devicechain}
PROFILE=${PROFILE:-full}
SKIP_PREFLIGHT=${SKIP_PREFLIGHT:-0}

# Image source. End users deploy published images at the repo VERSION (single
# source of truth); developers opt into building from source.
BUILD_IMAGES=${BUILD_IMAGES:-0}
REGISTRY_NAME=${REGISTRY_NAME:-kind-registry}
REGISTRY_PORT=${REGISTRY_PORT:-5000}
if [ "$BUILD_IMAGES" = "1" ]; then
  REGISTRY=${REGISTRY:-localhost:${REGISTRY_PORT}}
  VERSION=${VERSION:-dev}
else
  REGISTRY=${REGISTRY:-ghcr.io/devicechain-io}                       # published
  VERSION=${VERSION:-$(cat "$ROOT/VERSION" 2>/dev/null || echo dev)} # override: VERSION=x.y.z
fi
OPERATOR_IMG="$REGISTRY/devicechain-operator:$VERSION"

# color-aware output (honours NO_COLOR and non-TTY / dumb terminals)
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != "dumb" ]; then
  G=$'\e[1;32m'; B=$'\e[1m'; C=$'\e[36m'; R=$'\e[0m'
else G=""; B=""; C=""; R=""; fi
log()  { printf '\n%s==>%s %s%s%s\n' "$G" "$R" "$B" "$1" "$R"; }
step() { printf '%s  - %s%s\n' "$C" "$1" "$R"; }

# ---- 0. preflight ----------------------------------------------------------
if [ "$SKIP_PREFLIGHT" != "1" ]; then
  log "🔍 Preflight"
  "$HERE/preflight.sh"
fi

if [ "$BUILD_IMAGES" = "1" ]; then
  log "🧭 Image source: building from source → $REGISTRY (tag $VERSION)"
else
  log "🧭 Image source: published $REGISTRY (version $VERSION) — set BUILD_IMAGES=1 to build from source"
fi

# ---- 1. kind cluster -------------------------------------------------------
log "☸️  kind cluster '$CLUSTER'"
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  step "cluster already exists"
else
  step "creating cluster from kind-cluster.yaml"
  kind create cluster --name "$CLUSTER" --config "$HERE/kind-cluster.yaml"
fi
kubectl config use-context "$CONTEXT" >/dev/null

# ---- 2. cloud-provider-kind (LoadBalancer IPs) -----------------------------
log "🌐 cloud-provider-kind (LoadBalancer support)"
if command -v cloud-provider-kind >/dev/null 2>&1; then
  if pgrep -x cloud-provider-kind >/dev/null 2>&1; then
    step "already running"
  else
    step "starting in background (logs: /tmp/cloud-provider-kind.log)"
    nohup cloud-provider-kind >/tmp/cloud-provider-kind.log 2>&1 &
  fi
else
  step "WARN: cloud-provider-kind not installed — type=LoadBalancer services will stay <pending>"
  step "      install: go install sigs.k8s.io/cloud-provider-kind@latest"
fi

# ---- 3. images (developer path only) ---------------------------------------
# End users skip this entirely and pull published images. Image build is
# decoupled into build-images.sh (ko, pull-by-reference model).
if [ "$BUILD_IMAGES" = "1" ]; then
  log "📦 Local registry ($REGISTRY)"
  if [ "$(docker inspect -f '{{.State.Running}}' "$REGISTRY_NAME" 2>/dev/null || true)" != "true" ]; then
    step "starting registry container"
    docker run -d --restart=always -p "127.0.0.1:${REGISTRY_PORT}:5000" --name "$REGISTRY_NAME" registry:2 >/dev/null
  else
    step "registry already running"
  fi
  step "connecting registry to the kind network"
  docker network connect kind "$REGISTRY_NAME" 2>/dev/null || true
  step "advertising the registry to the cluster (KEP-1755 ConfigMap)"
  kubectl --context "$CONTEXT" apply -f - <<EOF >/dev/null
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "localhost:${REGISTRY_PORT}"
    help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
EOF
  REGISTRY="$REGISTRY" TAG="$VERSION" "$HERE/build-images.sh"
fi

# ---- 4. infrastructure (OpenTofu) ------------------------------------------
log "🧱 Infrastructure — OpenTofu (NATS, Postgres, Timescale, ingress-nginx, cert-manager)"
TF=tofu; command -v tofu >/dev/null 2>&1 || TF=terraform
if [ ! -f "$TF_DIR/terraform.tfvars" ]; then
  step "writing terraform.tfvars (targets $CONTEXT)"
  cat > "$TF_DIR/terraform.tfvars" <<EOF
# Generated by deploy/local/up.sh for the local kind cluster. Gitignored.
kubeconfig_context = "$CONTEXT"
# kind recipe: ingress binds node 80/443 via hostPort + NodePort (no LoadBalancer,
# which stays <pending> on kind and times out the apply). See the ingress-nginx module.
ingress_use_host_port = true
EOF
fi
( cd "$TF_DIR" && "$TF" init -input=false >/dev/null && "$TF" apply -input=false -auto-approve )

# ---- 5. core (CRDs + operator) ---------------------------------------------
log "🧩 Core — CRDs + operator ($OPERATOR_IMG)"
make -C "$OPERATOR_DIR" install
make -C "$OPERATOR_DIR" deploy IMG="$OPERATOR_IMG"

# ---- 6. instance chart (Helm) ----------------------------------------------
log "⎈ Instance chart — Helm (instance.id=$INSTANCE, profile=$PROFILE)"
# metrics.enabled=false: the ServiceMonitors need the Prometheus Operator CRDs,
# which a bare kind cluster does not have. Production clusters with the operator
# leave the chart default (on).
helm --kube-context "$CONTEXT" upgrade --install dc "$CHART_DIR" \
  --set instance.id="$INSTANCE" \
  --set profile="$PROFILE" \
  --set ingress.enabled=true \
  --set image.registry="$REGISTRY" \
  --set image.tag="$VERSION" \
  --set metrics.enabled=false \
  --wait --timeout 10m

# ---- 7. seed ---------------------------------------------------------------
log "🌱 Seed"
if command -v dcctl >/dev/null 2>&1; then
  step "seeding admin credential / example data"
  dcctl seed --kube-context "$CONTEXT" || step "WARN: dcctl seed failed — seed manually if needed"
else
  step "dcctl not on PATH — build it: make -C backend/cli build  (binary: backend/cli/build/dcctl)"
fi

# ---- 8. report -------------------------------------------------------------
log "🚀 Ready"
LB_IP=$(kubectl --context "$CONTEXT" get svc -A \
  -o jsonpath='{range .items[?(@.spec.type=="LoadBalancer")]}{.metadata.namespace}/{.metadata.name}: {.status.loadBalancer.ingress[0].ip}{"\n"}{end}' 2>/dev/null || true)
echo "  context:     $CONTEXT"
echo "  instance:    $INSTANCE  (namespace: dc-$INSTANCE)"
echo "  images:      $REGISTRY/<area>:$VERSION"
echo "  ingress:     http://localhost  /  https://localhost  (node port mappings)"
[ -n "$LB_IP" ] && { echo "  LoadBalancers:"; echo "$LB_IP" | sed 's/^/    /'; }
echo
echo "  kubectl --context $CONTEXT get pods -A"
echo "  Tear down: deploy/local/down.sh"
