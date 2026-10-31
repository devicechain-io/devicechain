#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Chapters 8-11, checked: waits for the rule to act, sends a command of your own, then looks at the
# site twice, once as the operator and once as the customer user, and says PASS or FAIL for each
# claim the tour makes. Exits non-zero if any claim fails.
#
#   ./05-verify.sh sites/site-a.env
# Each claim is `[ test ]; check "claim" $?`: the $? is that test's result, on purpose.
# shellcheck disable=SC2319
set -uo pipefail
cd "$(dirname "$0")" || exit 1
. ./lib.sh

fails=0
check() { # check DESCRIPTION CONDITION(0=true)
  if [ "$2" = 0 ]; then echo "PASS  $1"; else echo "FAIL  $1"; fails=$((fails + 1)); fi
}
q() { gql "$@" 2>/dev/null; }

OP="$(operator_token)" || die "operator sign-in failed"

# --- the rule's loop on device 1 ---------------------------------------------------------------
echo "== waiting for the rule to act on $TOUR_DEVICE_1 (up to 5 minutes) =="
alarmq='query($c:AlarmSearchCriteria!){alarms(criteria:$c){results{alarmKey severity state raisedTime clearedTime lastValue originatorToken}}}'
cmdq='query($c:CommandSearchCriteria!){commands(criteria:$c){results{token name status outcome queuedTime respondedTime responsePayload}}}'
ok=1
for _ in $(seq 1 60); do
  a="$(q "$DM_GQL" "$OP" "$alarmq" "$(jq -nc --arg d "$TOUR_DEVICE_1" '{c:{pageNumber:1,pageSize:10,originatorType:"device",originator:$d}}')" | jq '.data.alarms.results | length')"
  c="$(q "$CMD_GQL" "$OP" "$cmdq" "$(jq -nc --arg d "$TOUR_DEVICE_1" '{c:{pageNumber:1,pageSize:10,deviceToken:$d,status:"SUCCESSFUL"}}')" | jq '.data.commands.results | length')"
  if [ "${a:-0}" -gt 0 ] && [ "${c:-0}" -gt 0 ]; then ok=0; break; fi
  sleep 5
done
check "the rule raised an alarm on $TOUR_DEVICE_1 and its command ended SUCCESSFUL" "$ok"

# --- a command of your own ---------------------------------------------------------------------
mytoken="tour-manual-$(date +%s)"
r="$(q "$CMD_GQL" "$OP" 'mutation($r:CommandCreateRequest!){createCommand(request:$r){command{token status} rejection{code reason}}}' \
  "$(jq -nc --arg t "$mytoken" --arg d "$TOUR_DEVICE_1" '{r:{token:$t,deviceToken:$d,name:"boost-cooling",payload:"{\"targetC\":3}"}}')")"
accepted=1; [ "$(jq -r '.data.createCommand.command.status // empty' <<<"$r")" != "" ] && accepted=0
check "createCommand boost-cooling was accepted (QUEUED), not rejected" "$accepted"
st=""
for _ in $(seq 1 30); do
  st="$(q "$CMD_GQL" "$OP" 'query($t:[String!]!){commandsByToken(tokens:$t){status outcome responsePayload}}' "$(jq -nc --arg t "$mytoken" '{t:[$t]}')" | jq -r '.data.commandsByToken[0].status // empty')"
  case "$st" in SUCCESSFUL|FAILED|TIMEOUT|EXPIRED|REJECTED) break ;; esac
  sleep 2
done
[ "$st" = SUCCESSFUL ]; check "your own command reached SUCCESSFUL (the device answered; last status: ${st:-none})" $?

# --- the operator's view -----------------------------------------------------------------------
echo "== operator =="
nd="$(q "$DM_GQL" "$OP" '{devices(criteria:{pageNumber:1,pageSize:50}){results{token}}}' | jq '.data.devices.results | length')"
[ "${nd:-0}" -ge 2 ]; check "the operator sees both devices ($nd)" $?
na="$(q "$DM_GQL" "$OP" "$alarmq" '{"c":{"pageNumber":1,"pageSize":50}}' | jq '[.data.alarms.results[].originatorToken] | unique | length')"
[ "${na:-0}" -ge 2 ]; check "the operator sees alarms on both devices ($na devices with alarms)" $?
q "$DM_GQL" "$OP" "$alarmq" "$(jq -nc --arg d "$TOUR_DEVICE_1" '{c:{pageNumber:1,pageSize:5,originatorType:"device",originator:$d}}')" \
  | jq -r '.data.alarms.results[] | "    alarm \(.alarmKey) \(.severity) \(.state) raised \(.raisedTime) cleared \(.clearedTime // "-") last value \(.lastValue // "-")"'
q "$CMD_GQL" "$OP" "$cmdq" "$(jq -nc --arg d "$TOUR_DEVICE_1" '{c:{pageNumber:1,pageSize:5,deviceToken:$d}}')" \
  | jq -r '.data.commands.results[] | "    command \(.name) \(.status) outcome \(.outcome // "-") answered \(.respondedTime // "-") \(.responsePayload // "")"'
nb="$(q "$DASH_GQL" "$OP" '{dashboards(criteria:{pageNumber:1,pageSize:20}){results{token publishedVersion}}}' | jq '.data.dashboards.results | length')"
[ "${nb:-0}" -ge 2 ]; check "the operator sees both dashboards ($nb)" $?

# --- the customer's view -----------------------------------------------------------------------
echo "== customer user $TOUR_CUSTOMER_EMAIL =="
CIDT="$(login "$TOUR_CUSTOMER_EMAIL" "$(customer_password)")" || die "the customer user cannot sign in; run ./04-customer-user.sh"
CT="$(select_tenant "$CIDT")" || die "the customer user cannot enter the tenant"
devs="$(q "$DM_GQL" "$CT" '{devices(criteria:{pageNumber:1,pageSize:50}){results{token}}}' | jq -r '[.data.devices.results[].token] | join(",")')"
[ "$devs" = "$TOUR_DEVICE_1" ]; check "the customer sees exactly $TOUR_DEVICE_1 (saw: ${devs:-none})" $?
cal="$(q "$DM_GQL" "$CT" "$alarmq" '{"c":{"pageNumber":1,"pageSize":50}}' | jq -r '[.data.alarms.results[].originatorToken] | unique | join(",")')"
[ "$cal" = "$TOUR_DEVICE_1" ]; check "the customer sees alarms for $TOUR_DEVICE_1 only (saw: ${cal:-none})" $?
other="$(q "$DS_GQL" "$CT" '{latestMeasurements(deviceToken:"'"$TOUR_DEVICE_2"'"){name value}}' | jq -e '.data.latestMeasurements | length' 2>/dev/null)"
[ "${other:-x}" = 0 ]; check "the customer reads no state of $TOUR_DEVICE_2 (${other:-error} measurements)" $?
mine="$(q "$DS_GQL" "$CT" '{latestMeasurements(deviceToken:"'"$TOUR_DEVICE_1"'"){name value}}' | jq '.data.latestMeasurements | length')"
[ "${mine:-0}" -gt 0 ]; check "the customer reads $TOUR_DEVICE_1's latest readings ($mine)" $?
mb="$(q "$DASH_GQL" "$CT" '{dashboards(criteria:{pageNumber:1,pageSize:20}){results{token}}}' | jq -r '[.data.dashboards.results[].token] | join(",")')"
[ "$mb" = "$TOUR_TENANT-customer" ]; check "the customer lists only the board shared with them (saw: ${mb:-none})" $?
pd="$(q "$DASH_GQL" "$CT" '{publishedDashboard(token:"'"$TOUR_TENANT"'-ops"){version}}' | jq -er 'if (.data|has("publishedDashboard")) then (.data.publishedDashboard // "null") else "error" end')"
[ "$pd" = null ]; check "the operator's board is invisible to the customer" $?
err="$(gql "$CMD_GQL" "$CT" 'mutation($r:CommandCreateRequest!){createCommand(request:$r){command{token}}}' \
  "$(jq -nc --arg d "$TOUR_DEVICE_1" '{r:{token:"tour-customer-try",deviceToken:$d,name:"boost-cooling"}}')" 2>&1 >/dev/null)"
case "$err" in *FORBIDDEN*) rc=0 ;; *) rc=1 ;; esac
check "the read-only customer user cannot send a command (refused: FORBIDDEN)" "$rc"

echo
if [ "$fails" = 0 ]; then echo "tour $TOUR_TENANT: all checks passed"; else echo "tour $TOUR_TENANT: $fails check(s) FAILED"; fi
exit "$fails"
