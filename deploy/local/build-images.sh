#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Build & push the DeviceChain service + operator images to a registry.
#
# Decoupled from bring-up on purpose: dcctl bootstrap's image model is
# "pull by reference from the registry, never side-load" (ADR-032 §image-model),
# so populating the registry is a separate, upstream step. up.sh calls this
# before the Helm install; CI/release publishes to ghcr the same way.
#
# Uses ko (the repo's image tool — services use local 'replace' directives that
# Dockerfiles can't resolve) with --bare so each image is named EXACTLY what the
# Helm chart pulls: {REGISTRY}/{area}:{TAG}  (chart's devicechain.image helper).
# The operator image is {REGISTRY}/operator:{TAG}.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

REGISTRY=${REGISTRY:-localhost:5000}
# Default tag from the repo VERSION file (single source of truth).
TAG=${TAG:-$(cat "$ROOT/VERSION" 2>/dev/null || echo dev)}
PLATFORM=${PLATFORM:-linux/amd64}
export KO_CONFIG_PATH="$ROOT/.ko.yaml"   # pins the chainguard distroless base

# color-aware output (honours NO_COLOR and non-TTY / dumb terminals)
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != "dumb" ]; then
  G=$'\e[1;32m'; B=$'\e[1m'; C=$'\e[36m'; Y=$'\e[33m'; R=$'\e[0m'
else G=""; B=""; C=""; Y=""; R=""; fi
log()  { printf '\n%s==>%s %s%s%s\n' "$G" "$R" "$B" "$1" "$R"; }
step() { printf '%s  - %s%s\n' "$C" "$1" "$R"; }

command -v ko >/dev/null 2>&1 || {
  printf '%s❌ ko not found%s — install: go install github.com/google/ko@latest\n' "$Y" "$R" >&2
  exit 1
}

# ko build --bare: image name = $KO_DOCKER_REPO exactly (no import path appended).
build_one() { # <module-dir> <image-name>
  step "🏗️  $2:$TAG"
  ( cd "$1" && KO_DOCKER_REPO="$2" ko build --bare --tags "$TAG" --platform "$PLATFORM" ./ >/dev/null )
}

log "📦 Building images → $REGISTRY (tag=$TAG)"

# Services: enumerate the same way CI does (any backend/services/* with main.go).
for d in "$ROOT"/backend/services/*/; do
  [ -f "${d}main.go" ] || continue
  area="$(basename "$d")"
  build_one "$d" "$REGISTRY/$area"
done

# Operator (backend/k8s) — deployed by 'make deploy IMG=...'.
build_one "$ROOT/backend/k8s" "$REGISTRY/operator"

# Web console (frontend) — a static nginx SPA, so docker (not ko) builds it. The
# chart deploys it by default at {REGISTRY}/frontend:{TAG}.
step "🏗️  $REGISTRY/frontend:$TAG"
docker build -t "$REGISTRY/frontend:$TAG" "$ROOT/frontend" >/dev/null
docker push "$REGISTRY/frontend:$TAG" >/dev/null

log "✅ Done — images pushed to $REGISTRY"
