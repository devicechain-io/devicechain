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
# Idempotent: safe to re-run for a fresh cluster. NOTE (ADR-025): each run mints
# FRESH NATS auth credentials, so re-running against a LIVE cluster rotates them —
# the broker reloads first, then services are locked out of NATS until the Helm
# step rolls their pods with the new instance config (self-heals; a failure
# between the two strands the cluster locked-out until a full re-run). Prefer a
# clean down.sh + up.sh for a re-provision.
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
PROFILE=${PROFILE:-default}
SKIP_PREFLIGHT=${SKIP_PREFLIGHT:-0}
# Ingress host. Default to localhost so the browse URL below actually resolves on
# kind (the chart default is devicechain.local, which would 404 a plain localhost
# request). Override HOST for a real DNS name.
HOST=${HOST:-localhost}
# Ingress TLS. Off by default for local dev: the chart's self-signed cert makes
# the browser warn on every visit (NET::ERR_CERT_AUTHORITY_INVALID) with no
# benefit on localhost — same rationale as dcctl's --no-tls. Set INGRESS_TLS=1 to
# serve HTTPS. (Browser-facing ingress cert only; the NATS/broker TLS from
# ADR-025 is independent and always on.)
INGRESS_TLS=${INGRESS_TLS:-0}
if [ "$INGRESS_TLS" = "1" ]; then INGRESS_SCHEME=https; else INGRESS_SCHEME=http; fi

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
OPERATOR_IMG="$REGISTRY/operator:$VERSION"

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
log "🧱 Infrastructure — OpenTofu (NATS, Postgres, Timescale, ingress-nginx, cert-manager, monitoring)"
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
# Broker authentication (ADR-025): mint the NATS auth-callout credentials — the
# issuer nkey + the shared service password. nkeys aren't a TF/bash primitive, so
# a tiny Go helper is the shared minting entrypoint (dcctl mints the same in-proc).
# The public issuer + the BCRYPT password hash go to the NATS config (tofu); the
# seed + the PLAINTEXT password go to the instance config (helm). One mint feeds
# both sides, so the hash and the plaintext it verifies can't drift.
step "minting NATS broker-auth credentials"
# Clear any inherited values first: `eval` masks genauth's exit code, and the
# `${VAR:?}` guards below only trip on UNSET vars — so a stale value sourced from
# the environment could otherwise survive a failed mint. genauth single-quotes each
# value, so the bcrypt hash's `$` chars survive `eval` literally.
unset NATS_CALLOUT_ISSUER_PUBLIC NATS_CALLOUT_ISSUER_SEED NATS_SERVICE_PASSWORD NATS_SERVICE_PASSWORD_BCRYPT
eval "$(cd "$ROOT" && go run ./backend/core/natsauth/cmd/genauth)"
: "${NATS_CALLOUT_ISSUER_PUBLIC:?minting failed}" "${NATS_CALLOUT_ISSUER_SEED:?}" "${NATS_SERVICE_PASSWORD:?}" "${NATS_SERVICE_PASSWORD_BCRYPT:?}"

# monitoring_slim: kube-prometheus-stack keeps its TSDB on emptyDir with smaller
# resource requests so it fits the local single-node kind cluster. Opt out of the
# whole stack (and the chart's ServiceMonitors below) with MONITORING=0.
MONITORING="${MONITORING:-1}"
MON_ARGS=(-var "monitoring_slim=true")
[ "$MONITORING" = "0" ] && MON_ARGS=(-var "enable_monitoring=false")
( cd "$TF_DIR" && "$TF" init -input=false >/dev/null && "$TF" apply -input=false -auto-approve \
    -var "nats_enable_auth=true" \
    -var "nats_callout_issuer_public=$NATS_CALLOUT_ISSUER_PUBLIC" \
    -var "nats_service_password_bcrypt=$NATS_SERVICE_PASSWORD_BCRYPT" \
    "${MON_ARGS[@]}" )

# NATS TLS material (ADR-025): the broker terminates TLS and emits its CA as an
# output; thread it into the instance config so services + the MQTT source dial
# over TLS and verify the broker. The broker flag and the client flag MUST agree,
# so both come from the same tofu outputs. No `|| echo false` fallback: a failed
# read must abort under `set -e`, not silently disable the client while the broker
# still requires TLS (every service would then fail to connect, masked by NATS'
# retry-forever). The apply just above succeeded, so the outputs exist.
NATS_TLS_ENABLED=$(cd "$TF_DIR" && "$TF" output -raw nats_tls_enabled)
NATS_TLS_ARGS=(--set "instance.config.infrastructure.nats.tls.enabled=${NATS_TLS_ENABLED}")
if [ "$NATS_TLS_ENABLED" = "true" ]; then
  NATS_CA_FILE="$(mktemp)"
  trap 'rm -f "$NATS_CA_FILE"' EXIT
  ( cd "$TF_DIR" && "$TF" output -raw nats_ca ) > "$NATS_CA_FILE"
  NATS_TLS_ARGS+=(--set-file "instance.config.infrastructure.nats.tls.ca=${NATS_CA_FILE}")
  step "NATS TLS enabled — threading the broker CA into the instance config"
fi

# Thread the broker-auth material into the instance config so every service
# presents the shared service credential, and device-management gets the callout
# issuer seed. nkey/hex values carry no --set metacharacters, so plain --set is
# safe. The plaintext password here corresponds to the bcrypt hash the tofu apply
# configured on the broker (one mint, both sides).
NATS_AUTH_ARGS=(
  --set "instance.config.infrastructure.nats.auth.user=dc_service"
  --set "instance.config.infrastructure.nats.auth.password=${NATS_SERVICE_PASSWORD}"
  --set "instance.config.infrastructure.nats.auth.calloutIssuerSeed=${NATS_CALLOUT_ISSUER_SEED}"
)
step "NATS auth enabled — threading the service credential + callout seed into the instance config"

# The shared service secret (ADR-044 amendment) backing the synchronous
# cross-service call primitive: a caller presents it to user-management's mint
# endpoint for a short-lived service token; user-management compares the presented
# value against its copy. Threaded into every service's instance config, so one
# mint feeds every service and the presented value can't drift from the verifier's.
# WITHOUT it every svcclient feature silently disables — the anchor-reconciliation
# sweep, command-delivery device verification, and per-tenant ingest governance all
# log "…not configured" and turn themselves off. Minted fresh per run like the NATS
# creds above; hex carries no --set metacharacters. dcctl bootstrap mints the
# equivalent in-proc (backend/cli/bootstrap/steps.go).
step "minting the cross-service auth secret"
SERVICE_AUTH_SECRET=$(openssl rand -hex 32)
: "${SERVICE_AUTH_SECRET:?minting failed}"
SERVICE_AUTH_ARGS=(--set "instance.config.infrastructure.serviceAuth.secret=${SERVICE_AUTH_SECRET}")

# The instance root key (ADR-059): the KEK every envelope-encrypted secret store wraps
# its DEKs with. WITHOUT it, any area that owns a secret store fails startup CLOSED —
# "a service that cannot form its KEK does not start" — which is ai-inference and
# outbound-connectors, i.e. exactly what PROFILE=full now brings up. dcctl bootstrap
# has always minted this (backend/cli/bootstrap/steps.go); this manual mirror did not,
# so the gap only stayed invisible while those areas were unreachable from a profile.
# Base64 (not hex like the secret above): the config decodes a base64 256-bit key.
# Its +/= alphabet carries no --set metacharacters — the separator is "," and base64
# has none — and the value round-trips through --set verbatim.
step "minting the instance secret-store root key"
SECRETS_ROOT_KEY=$(openssl rand -base64 32)
: "${SECRETS_ROOT_KEY:?minting failed}"
SECRETS_ARGS=(--set "instance.config.infrastructure.secrets.rootKey=${SECRETS_ROOT_KEY}")

# ---- 5. core (CRDs + operator) ---------------------------------------------
log "🧩 Core — CRDs + operator ($OPERATOR_IMG)"
make -C "$OPERATOR_DIR" install
make -C "$OPERATOR_DIR" deploy IMG="$OPERATOR_IMG"

# ---- 6. instance chart (Helm) ----------------------------------------------
log "⎈ Instance chart — Helm (instance.id=$INSTANCE, profile=$PROFILE)"
# metrics.enabled=true: the OpenTofu step above installs kube-prometheus-stack, so
# the Prometheus Operator CRDs the ServiceMonitors/PrometheusRule need are present
# (the infra apply runs before this Helm step). The Grafana sidecar auto-imports the
# chart's dashboard ConfigMaps. Port-forward: kubectl -n monitoring port-forward
# svc/kube-prometheus-stack-grafana 3000:80 (admin / devicechain).
helm --kube-context "$CONTEXT" upgrade --install dc "$CHART_DIR" \
  --set instance.id="$INSTANCE" \
  --set profile="$PROFILE" \
  --set ingress.enabled=true \
  --set ingress.host="$HOST" \
  --set ingress.tls.enabled="$([ "$INGRESS_TLS" = "1" ] && echo true || echo false)" \
  --set image.registry="$REGISTRY" \
  --set image.tag="$VERSION" \
  --set metrics.enabled="$([ "$MONITORING" = "0" ] && echo false || echo true)" \
  "${NATS_TLS_ARGS[@]}" \
  "${NATS_AUTH_ARGS[@]}" \
  "${SERVICE_AUTH_ARGS[@]}" \
  "${SECRETS_ARGS[@]}" \
  --wait --timeout 10m

# ---- 7. seed ---------------------------------------------------------------
log "🌱 Seed"
# The bootstrap admin credential is seeded automatically by user-management on
# first start (look for "Seeded bootstrap admin" in its logs; default password —
# change it). Example datasets are seeded over GraphQL against the running
# instance (needs the API reachable + auth), so they are an on-demand step, not
# part of bring-up.
step "bootstrap admin auto-seeded by user-management on first start (default password — change it)"
step "run a simulation on demand:  dcctl sim create devicepulse --instance $INSTANCE  (then run dc-simulator with the printed handshake path)"

# ---- 8. report -------------------------------------------------------------
log "🚀 Ready"
LB_IP=$(kubectl --context "$CONTEXT" get svc -A \
  -o jsonpath='{range .items[?(@.spec.type=="LoadBalancer")]}{.metadata.namespace}/{.metadata.name}: {.status.loadBalancer.ingress[0].ip}{"\n"}{end}' 2>/dev/null || true)
echo "  context:     $CONTEXT"
echo "  instance:    $INSTANCE  (namespace: dc-$INSTANCE)"
echo "  images:      $REGISTRY/<area>:$VERSION"
echo "  console:     $INGRESS_SCHEME://$HOST  (INGRESS_TLS=1 for https)"
[ -n "$LB_IP" ] && { echo "  LoadBalancers:"; echo "$LB_IP" | sed 's/^/    /'; }
echo
echo "  kubectl --context $CONTEXT get pods -A"
echo "  Tear down: deploy/local/down.sh"
