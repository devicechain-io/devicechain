#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Rebuild ONE image from source and roll it onto a running local instance — the
# fast inner loop when iterating on a single service or the web console.
#
# It pushes a UNIQUELY-tagged image and `kubectl set image`s the deployment to
# it, rather than reusing a tag: the chart pulls images IfNotPresent, so a reused
# tag would serve the node's stale cached layer and your change would not appear.
# A fresh tag forces the new build to be pulled, every time.
#
# Usage:
#   deploy/local/bounce.sh <frontend|user-management|device-management|...> [instance]
#
# Env overrides: REGISTRY (default localhost:5000), CONTEXT (default kind-<instance>).
#
# NOTE: for active UI work the faster loop is `npm run dev` (Vite HMR) pointed at
# the instance ingress (frontend/.env.example) — no image rebuild at all. Use this
# when you need to validate the actual served artifact (nginx config, build output).
set -euo pipefail

TARGET=${1:?usage: bounce.sh <frontend|service-area> [instance]}
INSTANCE=${2:-devicechain}
REGISTRY=${REGISTRY:-localhost:5000}
CONTEXT=${CONTEXT:-kind-$INSTANCE}

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

# A unique tag per bounce sidesteps the IfNotPresent image cache (see header).
TAG="dev-$(date +%s)"
IMG="$REGISTRY/$TARGET:$TAG"

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != "dumb" ]; then
  G=$'\e[1;32m'; B=$'\e[1m'; R=$'\e[0m'
else G=""; B=""; R=""; fi
log() { printf '\n%s==>%s %s%s%s\n' "$G" "$R" "$B" "$1" "$R"; }

log "🏗️  Building $TARGET → $IMG"
if [ "$TARGET" = "frontend" ]; then
  docker build -t "$IMG" "$ROOT/frontend" >/dev/null
  docker push "$IMG" >/dev/null
else
  DIR="$ROOT/backend/services/$TARGET"
  [ -f "$DIR/main.go" ] || {
    printf '❌ unknown target %q (no backend/services/%s/main.go, and not "frontend")\n' "$TARGET" "$TARGET" >&2
    exit 1
  }
  # ko --bare names the image exactly $REGISTRY/$TARGET:$TAG (what the chart pulls).
  ( cd "$DIR" && KO_DOCKER_REPO="$REGISTRY/$TARGET" KO_CONFIG_PATH="$ROOT/.ko.yaml" \
      ko build --bare --tags "$TAG" --platform linux/amd64 ./ >/dev/null )
fi

# The chart names each container after the area (or "frontend"), == the
# Deployment name, so `set image deploy/$TARGET $TARGET=...` targets it.
log "🔄 Rolling deploy/$TARGET in namespace $INSTANCE onto the new image"
kubectl --context "$CONTEXT" -n "$INSTANCE" set image "deploy/$TARGET" "$TARGET=$IMG"
kubectl --context "$CONTEXT" -n "$INSTANCE" rollout status "deploy/$TARGET" --timeout=120s

log "✅ $TARGET bounced ($IMG)"
