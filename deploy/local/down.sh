#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Tear down the local DeviceChain cluster, its registry, and cloud-provider-kind.
# By default the local registry container is kept (warm image cache); pass
# --purge-registry to remove it too.
set -euo pipefail

CLUSTER=${CLUSTER:-devicechain}
REGISTRY_NAME=${REGISTRY_NAME:-kind-registry}
PURGE_REGISTRY=0
[ "${1:-}" = "--purge-registry" ] && PURGE_REGISTRY=1

# color-aware output (honours NO_COLOR and non-TTY)
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != "dumb" ]; then
  G=$'\e[1;32m'; B=$'\e[1m'; C=$'\e[36m'; R=$'\e[0m'
else G=""; B=""; C=""; R=""; fi
log()  { printf '\n%s==>%s %s%s%s\n' "$G" "$R" "$B" "$1" "$R"; }
step() { printf '%s  - %s%s\n' "$C" "$1" "$R"; }

log "🧹 Tearing down local DeviceChain"

step "🔻 deleting kind cluster '$CLUSTER'"
kind delete cluster --name "$CLUSTER" 2>/dev/null || step "(no such cluster)"

step "🌐 stopping cloud-provider-kind"
pkill -x cloud-provider-kind 2>/dev/null || step "(not running)"

if [ "$PURGE_REGISTRY" = "1" ]; then
  step "📦 removing registry container '$REGISTRY_NAME'"
  docker rm -f "$REGISTRY_NAME" 2>/dev/null || step "(no registry container)"
else
  step "📦 keeping registry '$REGISTRY_NAME' (warm cache; --purge-registry to remove)"
fi

log "✅ Done"
