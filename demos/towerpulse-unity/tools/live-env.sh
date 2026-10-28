#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# live-env.sh -- read-only helpers for the instance the Towerpulse device plane runs against.
#
#   live-env.sh status          is the cluster up, do the areas answer, is the MQTT gateway accepting
#   live-env.sh ca <out.pem>    export the broker CA (a public certificate) for the player's pinned trust
#   live-env.sh connz           how many device sessions the broker holds right now, by client id prefix
#
# It never starts, stops or changes anything, and prints no secret. The instance is provisioned once with
# `dcctl install local` / `dcctl bootstrap local` and `dcctl sim create <name> --manifest towerpulse`; this
# script only looks. It is pinned to ONE cluster through TP_KUBECONFIG and never touches the current context.
#
# Inputs (environment):
#   TP_KUBECONFIG  kubeconfig of the Towerpulse cluster            (required)
#   TP_INSTANCE    instance name                                   (default: tp)
#   TP_API         base URL of the console and API ingress         (default: http://localhost:18080)
#
# ONE WRITER PER DEVICE. The towerpulse scenario's own runner publishes simplified telemetry and answers
# commands for the same 31 devices. The Unity device plane is the writer for a live take, so the runner must
# not tick at the same time (an external-device-plane mode for it is the runner's job, not this script's).

set -euo pipefail

INSTANCE="${TP_INSTANCE:-tp}"
API="${TP_API:-http://localhost:18080}"
NS="dci-${INSTANCE}"

die() { printf '[towerpulse-env] ERROR: %s\n' "$*" >&2; exit 1; }
[ -n "${TP_KUBECONFIG:-}" ] || die "set TP_KUBECONFIG to the Towerpulse cluster's kubeconfig"
[ -f "$TP_KUBECONFIG" ] || die "TP_KUBECONFIG=$TP_KUBECONFIG does not exist"
kc() { KUBECONFIG="$TP_KUBECONFIG" kubectl "$@"; }

cmd_status() {
	local rc=0 area code
	kc -n "$NS" get deploy -o 'jsonpath={range .items[*]}{.metadata.name}{" "}{.spec.replicas}{" "}{.status.readyReplicas}{"\n"}{end}' || rc=1
	for area in device-management event-management device-state command-delivery event-processing; do
		code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 -X POST -H 'Content-Type: application/json' \
			-d '{"query":"{__typename}"}' "$API/api/$area/graphql" || true)"
		echo "graphql $area -> ${code:-none}"
		[ "$code" = 200 ] || rc=1
	done
	[ "$rc" = 0 ] && echo "status: up" || echo "status: not fully up"
	return "$rc"
}

cmd_ca() {
	local out="${1:?usage: $0 ca <out.pem>}"
	kc -n "$NS" get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d >"$out.tmp"
	openssl x509 -in "$out.tmp" -noout >/dev/null 2>&1 || { rm -f "$out.tmp"; die "exported CA is not a certificate"; }
	mv "$out.tmp" "$out"
	echo "broker CA written to $out"
}

# connz asks the broker's monitoring endpoint from inside the cluster (the nats-box pod, through the headless service,
# since the server image carries no HTTP client), so nothing is exposed to the host. It counts the MQTT sessions, which
# is what the device plane holds: one per device, client id "<instance>:<tenant>:<device>".
cmd_connz() {
	local out total mqtt
	out="$(kc -n "$NS" exec deploy/dc-nats-box -- wget -qO- 'http://dc-nats-headless:8222/connz?limit=10000')" || die "cannot reach the broker's monitoring port from the nats-box pod in $NS"
	total="$(printf '%s' "$out" | grep -c '"cid"' || true)"
	mqtt="$(printf '%s' "$out" | grep -c '"mqtt_client"' || true)"
	echo "connections: $total (mqtt sessions: $mqtt)"
}

case "${1:-}" in
status) cmd_status ;;
ca) shift; cmd_ca "$@" ;;
connz) cmd_connz ;;
*) echo "usage: $0 status | ca <out.pem> | connz" >&2; exit 2 ;;
esac
