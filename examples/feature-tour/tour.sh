#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Runs the whole tour for one site, or one step of it.
#
#   ./tour.sh sites/site-a.env up       provision, dashboards, customer user, start the devices
#   ./tour.sh sites/site-a.env verify   wait for the loop, then check what the operator and the customer see
#   ./tour.sh sites/site-a.env down     stop the devices (the tenant stays; destroy the instance to remove it)
#   ./tour.sh sites/site-a.env all      up, then verify
#
# Each numbered script can also be run by itself, with the same site file as its first argument.
set -euo pipefail
cd "$(dirname "$0")"
site="${1:?usage: $0 <site-file> up|verify|down|all}"
step="${2:-all}"

up() {
  ./01-provision.sh "$site"
  ./03-dashboards.sh "$site"
  ./04-customer-user.sh "$site"
  ./02-devices.sh "$site" up
}

case "$step" in
up) up ;;
verify) ./05-verify.sh "$site" ;;
down) ./02-devices.sh "$site" down ;;
all) up; ./05-verify.sh "$site" ;;
*) echo "usage: $0 <site-file> up|verify|down|all" >&2; exit 2 ;;
esac
