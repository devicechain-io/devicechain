#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Points DeviceChain's sparkplug-ingest area at the example's Mosquitto broker, with the command
# downlink on, by upgrading the instance's Helm release.
#
# Why Helm and not dcctl: dcctl refuses a Sparkplug source in a --values overlay (the broker URL,
# credential reference and tenant binding are wiring, not tuning), so the place for one is the
# chart's values. The cost is that a later `dcctl upgrade` composes the release again without it;
# run this script again afterwards.
#
#   RELEASE / RELEASE_NS   the instance's Helm release (found by its chart when there is one)
#   CHART                  the chart directory (default: this checkout's deploy/helm/devicechain,
#                          which must be the release the instance runs)
#   BROKER_URL             how the cluster reaches the broker (default tcp://spb-mosquitto:1883)
#   KUBE_CONTEXT           the kube context to use (default: the current one)
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh
command -v helm >/dev/null || die "helm is required"
[ -f .env ] || die "run ./setup.sh first"

CHART="${CHART:-$(git rev-parse --show-toplevel)/deploy/helm/devicechain}"
BROKER_URL="${BROKER_URL:-tcp://spb-mosquitto:1883}"
ctx=()
[ -z "${KUBE_CONTEXT:-}" ] || ctx=(--kube-context "$KUBE_CONTEXT")

if [ -z "${RELEASE:-}" ]; then
  found="$(helm "${ctx[@]}" list -A -o json | jq -c '[.[] | select(.chart | startswith("devicechain-"))]')"
  [ "$(jq length <<<"$found")" = 1 ] || die "expected one devicechain release, found $(jq -c 'map(.name)' <<<"$found"); set RELEASE and RELEASE_NS"
  RELEASE="$(jq -r '.[0].name' <<<"$found")"
  RELEASE_NS="$(jq -r '.[0].namespace' <<<"$found")"
fi

# The password reaches the release through a values file that exists only for this command.
umask 077
vals="$(mktemp)"
trap 'rm -f "$vals"' EXIT
host_pw="$(sed -n 's/^HOST_PASSWORD=//p' .env)"
jq -n --arg tenant "$DC_TENANT" --arg url "$BROKER_URL" --arg pw "$host_pw" '{
  extraSecrets: [{name: "sparkplug-broker", stringData: {password: $pw}}],
  functionalAreas: {"sparkplug-ingest": {
    extraEnv: [{name: "SPARKPLUG_BROKER_PW",
                valueFrom: {secretKeyRef: {name: "sparkplug-broker", key: "password"}}}],
    config: {sources: [{
      tenant: $tenant,
      hostId: "devicechain",
      broker: {url: $url, username: "devicechain", passwordEnv: "SPARKPLUG_BROKER_PW"},
      groups: ["plant-a"],
      autoRegister: true,
      deviceTypeToken: "sparkplug-pump",
      commands: {enabled: true, hostRole: "primary", writableMetrics: ["Outputs/*"]}
    }]}
  }}
}' > "$vals"

helm "${ctx[@]}" upgrade "$RELEASE" "$CHART" -n "${RELEASE_NS:-default}" --reuse-values -f "$vals" --wait --timeout 10m >/dev/null
echo "sparkplug-ingest now connects to $BROKER_URL for tenant $DC_TENANT"
