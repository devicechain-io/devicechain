#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The side-by-side pilot, and the way back out of it.
#
#   ./pilot.sh up         start 3 simulated devices that publish to the previous platform's
#                         stand-in broker AND to DeviceChain
#   ./pilot.sh compare    do the two platforms agree on what the devices reported?
#   ./pilot.sh status     targets, enabled flag and reading counts on each side
#   ./pilot.sh rollback   step 1: the devices stop publishing to DeviceChain (a one-line config flip)
#                         step 2: with --disable, the devices are also shut out in DeviceChain;
#                                 with --delete, they are removed from it (history goes with them)
#   ./pilot.sh resume     the reverse: re-enable the devices and publish to both again
#   ./pilot.sh down       stop the simulation
#
# Needs docker (with compose), kubectl, jq, curl and openssl... and a cluster from ./README.md.
# Set KUBECONFIG (or KUBE_CONTEXT), DC_URL, INSTANCE and KIND_CLUSTER to match the cluster.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

PILOT="cc-0001 cc-0002 pc-0001"
KIND_CLUSTER="${KIND_CLUSTER:-devicechain}"
export DC_HOST="${DC_HOST:-$KIND_CLUSTER-control-plane}"
export INSTANCE DC_TENANT
compose() { docker compose -p migration-pilot -f pilot/docker-compose.yml --env-file pilot/.env "$@"; }

# access_token SERIAL: the token the device already holds, which is what the export carried.
access_token() { awk -F, -v s="$1" '{ sub(/\r$/, "") } toupper($2) == toupper(s) { print $7; exit }' source/devices.csv; }

up() {
  mkdir -p pilot/state pilot/pki pilot/logs
  ( umask 077
    {
      echo "DC_HOST=$DC_HOST"
      for d in $PILOT; do echo "TOKEN_$(echo "$d" | tr 'a-z-' 'A-Z_')=$(access_token "$d")"; done
    } > pilot/.env )
  # the broker presents a certificate the instance's own CA issued; the device needs that CA
  local -a ctx=(); [ -z "${KUBE_CONTEXT:-}" ] || ctx=(--context "$KUBE_CONTEXT")
  kubectl "${ctx[@]}" -n "dci-$INSTANCE" get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > pilot/pki/instance-ca.crt
  [ -s pilot/pki/instance-ca.crt ] || die "could not read the broker CA from secret dc-nats-tls in dci-$INSTANCE"
  echo "legacy dc" > pilot/state/targets; date -u +%Y-%m-%dT%H:%M:%SZ > pilot/state/started
  : > pilot/logs/legacy.log
  compose up -d
  echo "pilot devices: $PILOT  (targets: $(cat pilot/state/targets))"
}

# --- reading the two platforms ---------------------------------------------------------------
# legacy_values DEVICE KEY: what the previous platform's stand-in received, oldest first
legacy_values() {
  awk -v t="legacy/telemetry/$1" '$1 == t { $1 = ""; print }' pilot/logs/legacy.log |
    jq -s --arg k "$2" '[.[] | .[$k] | select(. != null) | if type == "boolean" then (if . then 1 else 0 end) else . end]'
}
# started: when this pilot run began. A device's readings outlive the device (deleting it does not
# erase them), so counts are taken from here on, not from the beginning of the device's history.
started() { cat pilot/state/started 2>/dev/null || echo 1970-01-01T00:00:00Z; }
# dc_values DEVICE KEY: what DeviceChain stored, oldest first
dc_values() {
  gql "$EVT_GQL" "$TOK" '
    query($d:String!,$s:String){measurementEvents(criteria:{pageNumber:1,pageSize:1000,deviceToken:$d,startTime:$s}){results{name value occurredTime}}}' \
    "$(jq -nc --arg d "$1" --arg s "$(started)" '{d:$d,s:$s}')" |
    jq --arg k "$2" '[.data.measurementEvents.results[] | select(.name == $k)] | sort_by(.occurredTime) | map(.value)'
}

compare() {
  session
  local bad=0 d k keys L D
  printf '%-8s %-12s %8s %8s  %s\n' device key legacy deviceChain result
  for d in $PILOT; do
    case "$d" in cc-*) keys="temperature humidity battery door_open status_text" ;; *) keys="flow_rate pressure running" ;; esac
    for k in $keys; do
      D="$(dc_values "$d" "$k")"; L="$(legacy_values "$d" "$k")"
      local nl nd res
      nl="$(jq length <<<"$L")"; nd="$(jq length <<<"$D")"
      # DeviceChain's list must be the previous platform's, from the start, up to the readings
      # still in flight (read after DeviceChain's, so it can only be ahead): the same values in the same order
      if [ "$k" = status_text ]; then
        res="not sent: a text value cannot be a metric"; [ "$nd" -eq 0 ] || { res="UNEXPECTED"; bad=1; }
        nd=0; nl="$(awk -v t="legacy/telemetry/$d" '$1 == t' pilot/logs/legacy.log | wc -l | tr -d ' ')"
      elif [ "$nd" -gt 0 ] && jq -e --argjson l "$L" 'length as $n | . == $l[0:$n]' <<<"$D" >/dev/null && [ $((nl - nd)) -le 5 ]; then
        res="match"
      else
        res="MISMATCH"; bad=1
      fi
      printf '%-8s %-12s %8s %8s  %s\n' "$d" "$k" "$nl" "$nd" "$res"
    done
  done
  echo
  echo "alarms raised in DeviceChain by the migrated rules:"
  gql "$DM_GQL" "$TOK" '{alarms(criteria:{pageNumber:1,pageSize:20}){results{originatorToken alarmKey severity state}}}' 2>/dev/null |
    jq -r '.data.alarms.results[] | "  \(.originatorToken)  \(.alarmKey)  \(.severity)  \(.state)"' || echo "  (alarm query unavailable)"
  return "$bad"
}

status() {
  session
  echo "targets: $(cat pilot/state/targets 2>/dev/null || echo '(not started)')"
  printf '%-8s %-8s %16s %22s\n' device enabled 'legacy readings' 'DeviceChain readings'
  local d en nl nd
  for d in $PILOT; do
    en="$(gql "$DM_GQL" "$TOK" "$(printf '{devicesByToken(tokens:["%s"]){enabled}}' "$d")" | jq -r '.data.devicesByToken[0] | if . == null then "absent" else .enabled end')"
    nl="$(awk -v t="legacy/telemetry/$d" '$1 == t' pilot/logs/legacy.log 2>/dev/null | wc -l | tr -d ' ')"
    nd="$(gql "$EVT_GQL" "$TOK" "$(printf '{measurementEvents(criteria:{pageNumber:1,pageSize:1,deviceToken:"%s",startTime:"%s"}){pagination{totalRecords}}}' "$d" "$(started)")" | jq -r '.data.measurementEvents.pagination.totalRecords')"
    printf '%-8s %-8s %16s %22s\n' "$d" "$en" "$nl" "$nd"
  done
}

# --- the way back ----------------------------------------------------------------------------
set_enabled() {
  local d
  session
  for d in $PILOT; do
    gql "$DM_GQL" "$TOK" 'mutation($t:String!,$e:Boolean!){updateDevice(token:$t,request:{enabled:$e}){token enabled}}' \
      "$(jq -nc --arg t "$d" --argjson e "$1" '{t:$t,e:$e}')" | jq -c '.data.updateDevice'
  done
}

rollback() {
  echo legacy > pilot/state/targets
  echo "step 1: devices now publish to the previous platform only (targets: $(cat pilot/state/targets))"
  case "${1:-}" in
    --disable) echo "step 2: disabling the pilot devices in DeviceChain"; set_enabled false ;;
    --delete)
      echo "step 2: deleting the pilot devices from DeviceChain (their readings go with them)"
      session
      local d
      for d in $PILOT; do
        gql "$DM_GQL" "$TOK" 'mutation($t:String!){deleteDevice(token:$t)}' "$(jq -nc --arg t "$d" '{t:$t}')" | jq -c ".data | {\"$d\": .deleteDevice}"
      done ;;
    "") echo "(step 2 skipped: the devices are still enabled in DeviceChain, just silent)" ;;
    *) die "usage: $0 rollback [--disable|--delete]" ;;
  esac
}

resume() {
  set_enabled true
  echo "legacy dc" > pilot/state/targets
  echo "devices publish to both again (targets: $(cat pilot/state/targets))"
}

case "${1:-}" in
  up) up ;;
  compare) compare ;;
  status) status ;;
  quarantine) set_enabled false ;; # disable in DeviceChain only, leaving the devices publishing: shows the hard stop
  rollback) rollback "${2:-}" ;;
  resume) resume ;;
  down) compose down ;;
  *) echo "usage: $0 up|compare|rollback [--disable|--delete]|resume|down" >&2; exit 2 ;;
esac
