#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Chapter 7: the customer's own login. The operator invites a member who holds the read-only
# "viewer" role and whose access is limited to ONE customer, then the member accepts the invitation
# and sets a password. (In the console this is Access -> Members -> Invite member; the invitation
# link is shown once and you hand it over yourself. This script accepts it for you so a tour can
# run unattended.) The password is generated into .state/<tenant>/customer-password and never printed.
#
#   ./04-customer-user.sh sites/site-a.env
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

TOK="$(operator_token)"
email="$TOUR_CUSTOMER_EMAIL"

found="$(gql "$USER_GQL" "$TOK" 'query($c:MemberSearchCriteria!){members(criteria:$c){results{email enabled invitationPending scope{kind token}}}}' \
  "$(jq -nc --arg e "$email" '{c:{pageNumber:1,pageSize:5,emailPrefix:$e}}')" \
  | jq -c --arg e "$email" '[.data.members.results[] | select(.email|ascii_downcase==($e|ascii_downcase))][0] // empty')"

if [ -n "$found" ] && [ "$(jq -r .invitationPending <<<"$found")" = false ]; then
  echo "customer user $email already exists: $(jq -c '.scope' <<<"$found")"
else
  if [ -z "$found" ]; then
    code="$(gql "$USER_GQL" "$TOK" 'mutation($r:InviteMemberRequest!){inviteMember(request:$r){invitationCode}}' \
      "$(jq -nc --arg e "$email" --arg c "$TOUR_CUSTOMER" '{r:{email:$e,roleTokens:["viewer"],scope:[{kind:"CUSTOMER",token:$c}]}}')" \
      | jq -er '.data.inviteMember.invitationCode')" || die "could not invite $email"
    echo "invited $email (role viewer, limited to customer $TOUR_CUSTOMER)"
  else
    code="$(gql "$USER_GQL" "$TOK" 'mutation($e:String!){reissueInvitation(email:$e){invitationCode}}' "$(jq -nc --arg e "$email" '{e:$e}')" \
      | jq -er '.data.reissueInvitation.invitationCode')" || die "could not reissue the invitation for $email"
    echo "reissued the pending invitation for $email"
  fi
  gql "$USER_GQL" "" 'mutation($c:String!,$p:String){acceptInvitation(code:$c,password:$p){identityToken}}' \
    "$(jq -nc --arg c "$code" --arg p "$(customer_password)" '{c:$c,p:$p}')" >/dev/null \
    || die "could not accept the invitation"
  echo "accepted: $email can sign in (password in $CUSTOMER_PASSWORD_FILE)"
fi
