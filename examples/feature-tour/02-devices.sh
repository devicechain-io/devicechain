#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Chapter 5: the two cold rooms. Each is one Mosquitto container running device/coldroom.sh, joined
# to the Docker network "kind" (every kind cluster's node is on it) and reaching the platform
# broker's MQTT node port by the node's container name.
#
#   ./02-devices.sh sites/site-a.env up | down | logs
set -euo pipefail
cd "$(dirname "$0")"
action="${2:-up}"
. ./lib.sh

IMAGE='eclipse-mosquitto:2.0.22@sha256:09b62ff32c2e7b64d4b431b9a89236073289b68db273de2b089edeb9b0d92b5c'
# Docker Desktop on Windows wants a Windows path for a bind mount, and Git Bash must not rewrite it.
HERE="$(pwd -W 2>/dev/null || pwd)"
export MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*'

[ -s pki/instance-ca.crt ] || die "pki/instance-ca.crt is missing; run ./01-provision.sh first"

case "$action" in
up)
  pw="$(device_password)"
  for d in "$TOUR_DEVICE_1" "$TOUR_DEVICE_2"; do
    name="tour-$d"
    docker rm -f "$name" >/dev/null 2>&1 || true
    docker run -d --name "$name" --network kind \
      --entrypoint /bin/sh \
      -e MQTT_HOST="$MQTT_HOST" -e MQTT_PORT="$MQTT_PORT" -e INSTANCE="$INSTANCE" \
      -e TENANT="$TOUR_TENANT" -e DEVICE="$d" -e DEVICE_PASSWORD="$pw" \
      -e TICK_SECONDS="${TOUR_TICK_SECONDS:-4}" -e START_C="$TOUR_START_C" -e TARGET_C="$TOUR_TARGET_C" \
      -v "$HERE/device/coldroom.sh:/coldroom.sh:ro" -v "$HERE/pki:/pki:ro" \
      "$IMAGE" /coldroom.sh >/dev/null
    echo "started $name"
  done
  ;;
down)
  for d in "$TOUR_DEVICE_1" "$TOUR_DEVICE_2"; do
    docker rm -f "tour-$d" >/dev/null 2>&1 && echo "removed tour-$d" || true
  done
  ;;
logs)
  for d in "$TOUR_DEVICE_1" "$TOUR_DEVICE_2"; do echo "== tour-$d =="; docker logs -t --tail 25 "tour-$d" 2>&1; done
  ;;
*) die "usage: $0 <site-file> up|down|logs" ;;
esac
