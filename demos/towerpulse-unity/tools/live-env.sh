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

# connz asks the broker's monitoring endpoint through the cluster, so nothing is exposed to the host.
cmd_connz() {
	local pod
	pod="$(kc -n "$NS" get pod -l app.kubernetes.io/name=dc-nats -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
	[ -n "$pod" ] || die "no NATS pod found in $NS"
	kc -n "$NS" exec "$pod" -- wget -qO- 'http://localhost:8222/connz?limit=1' | python3 -c \
		'import json,sys; d=json.load(sys.stdin); print("connections:", d.get("total"))'
}

case "${1:-}" in
status) cmd_status ;;
ca) shift; cmd_ca "$@" ;;
connz) cmd_connz ;;
*) echo "usage: $0 status | ca <out.pem> | connz" >&2; exit 2 ;;
esac
