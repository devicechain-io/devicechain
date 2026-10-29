#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Points DeviceChain's sparkplug-ingest area at the example's Mosquitto broker, with the command
# downlink on, by giving `dcctl upgrade` one Sparkplug source. dcctl keeps the source (and the
# Secret holding the broker password) through every later upgrade, so there is nothing to re-run.
#
#   INSTANCE               the dcctl instance (default spb)
#   PROVIDER               the dcctl provider (default local)
#   BROKER_URL             how the cluster reaches the broker (default tcp://spb-mosquitto:1883)
#   KUBE_CONTEXT           passed to dcctl as --kube-context when set
#   DCCTL_ARGS             extra arguments for `dcctl upgrade`, e.g. "--registry localhost:5000 --version dev"
#                          for an instance built from source
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh
command -v dcctl >/dev/null || die "dcctl is required"
[ -f .env ] || die "run ./setup.sh first"

BROKER_URL="${BROKER_URL:-tcp://spb-mosquitto:1883}"
PROVIDER="${PROVIDER:-local}"
ctx=()
[ -z "${KUBE_CONTEXT:-}" ] || ctx=(--kube-context "$KUBE_CONTEXT")

# The sources file holds the broker password, so it exists only for this command. dcctl renders the
# password into a Secret and never into the area's configuration.
umask 077
src="$(mktemp)"
trap 'rm -f "$src"' EXIT
host_pw="$(sed -n 's/^HOST_PASSWORD=//p' .env)"
jq -n --arg tenant "$DC_TENANT" --arg url "$BROKER_URL" --arg pw "$host_pw" '[{
  tenant: $tenant,
  hostId: "devicechain",
  broker: {url: $url, username: "devicechain", password: $pw},
  groups: ["plant-a"],
  autoRegister: true,
  deviceTypeToken: "sparkplug-pump",
  commands: {enabled: true, hostRole: "primary", writableMetrics: ["Outputs/*"]}
}]' > "$src"

# shellcheck disable=SC2086  # DCCTL_ARGS is a word list on purpose
dcctl upgrade "$PROVIDER" "$INSTANCE" "${ctx[@]}" --sparkplug-source "$src" ${DCCTL_ARGS:-} >/dev/null
echo "sparkplug-ingest now connects to $BROKER_URL for tenant $DC_TENANT"
