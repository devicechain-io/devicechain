#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Sends one command to a device and follows it until it ends, printing each status change.
#
#   ./send-command.sh pump-station/pump-1 sparkplug.write '{"metric":"Outputs/Run","value":true}'
#   ./send-command.sh pump-station sparkplug.rebirth
#
# The target is the Sparkplug identity, "node" or "node/device" in group plant-a.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

[ $# -ge 2 ] || die "usage: $0 <node[/device]> <sparkplug.write|sparkplug.rebirth> [payload-json]"
ext=$1 key=$2 payload=${3:-}
WAIT="${WAIT:-180}"

TOK="$(tenant_token "$(login)")"

# The device's external id is the whole Sparkplug path: plant-a/pump-station/pump-1.
dev="$(gql "$DM_GQL" "$TOK" 'query($e:[String!]!){devicesByExternalId(externalIds:$e){token}}' \
  "$(jq -nc --arg e "plant-a/$ext" '{e:[$e]}')" | jq -er '.data.devicesByExternalId[0].token')" \
  || die "no device with external id plant-a/$ext (has it been born yet?)"

ctoken="spb-$(date +%s)-$RANDOM"
req="$(jq -nc --arg t "$ctoken" --arg d "$dev" --arg n "$key" --arg p "$payload" \
  '{r:({token:$t,deviceToken:$d,name:$n} + (if $p == "" then {} else {payload:$p} end))}')"
res="$(gql "$CMD_GQL" "$TOK" 'mutation($r:CommandCreateRequest!){createCommand(request:$r){command{token status} rejection{code reason}}}' "$req")" \
  || die "createCommand failed"
if [ "$(jq -r '.data.createCommand.rejection' <<<"$res")" != "null" ]; then
  echo "REFUSED at enqueue: $(jq -c '.data.createCommand.rejection' <<<"$res")"
  exit 3
fi
echo "command $ctoken to $dev: $key $payload"

last=""
end=$((SECONDS + WAIT))
while [ $SECONDS -lt $end ]; do
  c="$(gql "$CMD_GQL" "$TOK" 'query($t:[String!]!){commandsByToken(tokens:$t){status outcome error responsePayload queuedTime sentTime respondedTime}}' \
    "$(jq -nc --arg t "$ctoken" '{t:[$t]}')" | jq -c '.data.commandsByToken[0]')"
  st="$(jq -r .status <<<"$c")"
  if [ "$st" != "$last" ]; then echo "$(date +%T) $st"; last=$st; fi
  case "$st" in
    SENT|QUEUED|HELD|PARKED) ;;
    *)
      echo "queued:   $(jq -r '.queuedTime // "-"' <<<"$c")"
      echo "sent:     $(jq -r '.sentTime // "-"' <<<"$c")"
      echo "answered: $(jq -r '.respondedTime // "-"' <<<"$c")"
      echo "outcome:  $(jq -r '.outcome // "-"' <<<"$c")"
      echo "error:    $(jq -r '.error // "-"' <<<"$c")"
      echo "evidence: $(jq -r '.responsePayload // "-"' <<<"$c")"
      exit 0;;
  esac
  sleep 1
done
echo "still $last after ${WAIT}s"
