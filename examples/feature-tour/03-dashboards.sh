#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Chapter 6: the dashboards. Two boards from one definition (dashboard.jq): the operator's, with a
# command button, and the customer's, without. Both are created, published (a viewer is only ever
# served a published version), and the customer's is shared with the customer.
#
# Converges: a board that exists with a different definition is updated and published again as a
# new version, so editing dashboard.jq and re-running this script rolls the change out.
#
#   ./03-dashboards.sh sites/site-a.env
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

TOK="$(operator_token)"

publish() {
  gql "$DASH_GQL" "$TOK" 'mutation($t:String!){publishDashboard(token:$t,label:"tour"){version{version}}}' "$(jq -nc --arg t "$1" '{t:$t}')" >/dev/null \
    || die "could not publish dashboard $1"
}

# board TOKEN TITLE DEVICE BUTTONS
board() {
  local token=$1 title=$2 device=$3 buttons=$4 def have ver
  def="$(jq -nc --arg title "$title" --arg device "$device" --arg command boost-cooling --argjson buttons "$buttons" -f dashboard.jq)"
  create "dashboard $token" "$DASH_GQL" "$TOK" \
    'mutation($r:DashboardCreateRequest!){createDashboard(request:$r){token}}' \
    "$(jq -nc --arg t "$token" --arg n "$title" --arg d "$def" '{r:{token:$t,name:$n,definition:$d}}')"
  # Compare the stored draft with what the parameters ask for (key order does not matter).
  have="$(gql "$DASH_GQL" "$TOK" 'query($t:String!){dashboard(token:$t){definition}}' "$(jq -nc --arg t "$token" '{t:$t}')" \
    | jq -r '.data.dashboard.definition' | jq -S -c .)"
  if [ "$have" != "$(jq -S -c . <<<"$def")" ]; then
    gql "$DASH_GQL" "$TOK" 'mutation($t:String!,$r:DashboardUpdateRequest!){updateDashboard(token:$t,request:$r){token}}' \
      "$(jq -nc --arg t "$token" --arg d "$def" '{t:$t,r:{definition:$d}}')" >/dev/null || die "could not update dashboard $token"
    echo "updated the draft of dashboard $token"
    publish "$token"; echo "published dashboard $token (new version)"
    return
  fi
  ver="$(gql "$DASH_GQL" "$TOK" 'query($t:String!){dashboardVersions(token:$t,limit:1){version}}' "$(jq -nc --arg t "$token" '{t:$t}')" \
    | jq -r '.data.dashboardVersions[0].version // empty')"
  if [ -n "$ver" ]; then
    echo "dashboard $token already published (version $ver)"
  else
    publish "$token"; echo "published dashboard $token"
  fi
}

board "$TOUR_TENANT-ops" "$TOUR_TENANT_NAME: operations" "$TOUR_DEVICE_1" true
board "$TOUR_TENANT-customer" "$TOUR_CUSTOMER_NAME: your cold rooms" "$TOUR_DEVICE_1" false

# Share the customer's board. Sharing replaces the list, so this is the same on every run.
gql "$DASH_GQL" "$TOK" 'mutation($t:String!,$a:[ScopeAnchorInput!]!){shareDashboard(token:$t,anchors:$a){token}}' \
  "$(jq -nc --arg t "$TOUR_TENANT-customer" --arg c "$TOUR_CUSTOMER" '{t:$t,a:[{kind:"CUSTOMER",token:$c}]}')" >/dev/null \
  || die "could not share the customer dashboard"
echo "shared $TOUR_TENANT-customer with customer $TOUR_CUSTOMER"
