#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Points ChirpStack at DeviceChain: runs setup/configure.py in a container, which gives ChirpStack
# a gateway, a device profile with a payload codec, an application, two devices and the
# application's HTTP integration. That integration posts to
#
#   http://host.docker.internal:$DC_INGRESS_PORT/api/integrations/<tenant>/chirpstack
#
# with the bearer token ./provision.sh saved. The ChirpStack containers reach your instance through
# the Docker host, so the ingress must be published on a host port (80 on a default
# `dcctl install local`).
#
#   DC_INGRESS_PORT   the host port of the instance's ingress (default 80)
#   DC_INGRESS_HOST   the host name the instance's ingress routes (default localhost, which is
#                     what `dcctl bootstrap --host localhost` serves); sent as the Host header
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh
[ -s .integration-token ] || die "run ./provision.sh first"

port="${DC_INGRESS_PORT:-80}"
ihost="${DC_INGRESS_HOST:-localhost}"
export DC_UPLINK_URL="http://host.docker.internal:$port/api/integrations/$DC_TENANT/chirpstack"
export DC_INGRESS_HOST="$ihost"
docker compose --profile setup run --rm setup
