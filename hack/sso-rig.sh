#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The single sign-on VALIDATION RIG: a real sign-in, end to end, against a real OpenID
# Connect provider running in a real cluster.
#
# WHAT THIS GATES
#
# Not the merge. Every rule of the sign-in flow is unit-tested against in-process
# providers. What needs a cluster is the CLAIM that the deployed pieces agree with each
# other: that user-management can reach a provider THROUGH THE EGRESS GUARD, over TLS,
# with the discovery snapshot an administrator approved; that the ingress routes the
# callback; that a first sign-in provisions an identity and a membership whose role is
# the one the provider's group maps to. As with the other rigs, a check is worth nothing
# until it has been shown to fail, so `control` runs first inside `all`.
#
#   hack/sso-rig.sh up        kind cluster + dcctl install + bootstrap, Dex as the
#                             upstream provider, and the RIG'S OWN instance trusting it
#   hack/sso-rig.sh verify    register Dex as a tenant connection and sign in headlessly as
#                             a person NO identity exists for (checked before): they must
#                             be provisioned with the mapped role
#   hack/sso-rig.sh control   THE NEGATIVE CONTROLS: an address outside the verified
#                             domain (asserted by the provider of a connection the sign-in
#                             really began on), a tampered state and a replayed login code
#                             must EACH be refused FOR ITS OWN REASON -- the browser-visible
#                             answer and the outcome counter both -- after first proving the
#                             detector rejects a success and a refusal of the wrong kind
#   hack/sso-rig.sh all       up + control + verify
#   hack/sso-rig.sh down      delete the cluster, and the instance's local state only when
#                             this rig's ownership marker says it created it
#
# THE UPSTREAM PROVIDER. Dex with its `authproxy` connector, chosen because it is the one
# connector that hands back a person AND their groups without a directory behind it: the
# rig names the email and group in request headers when it drives Dex's callback, so the
# "user" is whatever the rig says it is. THAT MAKES DEX AN IDENTITY ORACLE FOR WHOEVER CAN
# REACH IT. It is therefore never exposed: the rig reaches it only through a
# `kubectl port-forward` on 127.0.0.1, and it exists in a throwaway cluster.
#
# TRUST is the product's own: Dex's certificate is signed by a CA the rig mints, and the
# connection carries that CA as its private CA bundle, exactly as an operator would supply
# an on-premises provider's. Nothing is mounted into user-management. The only rig-only
# edit is the EGRESS allowance for Dex's address. `control` shows each is load-bearing:
# with the bundle cleared the provider is refused as untrusted, and with the allowance
# taken away the egress guard refuses the address. The stub provider, its certificate and
# the egress edit live in hack/lib/sso-stub-idp.sh, which hack/upgrade-rig.sh shares.
#
# The bootstrap prints the superuser's seed password once; this rig filters it out of its
# own output (redact_secrets), so a teed log of a run does not hold it.
#
# Requires: kind, kubectl, docker, jq, curl, openssl, a Go toolchain (the script builds
# dcctl) and `tofu` OR `terraform` on PATH. It binds host ports 80 and 443 (the kind
# config's ingress mapping) and 5556 (the Dex port-forward). NOT RUN IN CI. It has been run
# end to end on local kind from a Windows git-bash.

# GraphQL documents are single-quoted on purpose: their $variables are GraphQL's, not the
# shell's.
# The dex_* names are defined by hack/lib/sso-stub-idp.sh, which shellcheck does not follow
# (SC1091 is excluded repo-wide), so their use here reads as unassigned.
# shellcheck disable=SC2016,SC2154
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="devicechain-sso"
instance="${DC_SSO_INSTANCE:-ssorig}"
kube_context="kind-$cluster"
namespace="dci-$instance"
dcctl="$repo_root/backend/cli/build/dcctl$(go env GOEXE)"

# The ingress is plain http on localhost (--no-tls), which is a loopback host, so the
# chart derives the issuer as http://localhost/api/user-management.
api="http://localhost"
um="$api/api/user-management"
return_to="$api/login/sso/complete"

tenant="sso-rig"
connection="dex"
# shellcheck disable=SC2034 # read by hack/lib/sso-stub-idp.sh
sso_stub_redirect_uri="$um/sso/callback/$connection"
# shellcheck disable=SC2034 # read by hack/lib/sso-stub-idp.sh: trust is the connection's bundle
sso_stub_mount_ca=false
verified_domain="rig.example"
mapped_group="dc-viewers"
mapped_role="sso-viewer"
good_email="alice@$verified_domain"
bad_email="mallory@evil.example"

rig_tmp="$(mktemp -d "${TMPDIR:-/tmp}/dc-sso-rig.XXXXXX")"
pf_pid_file="$rig_tmp/pf.pid"
# How curl trusts Dex: the rig CA, and verification otherwise unchanged. --ssl-no-revoke is
# for the Schannel curl of a Windows git-bash, which asks for revocation status that the rig
# CA (no CRL, no OCSP) cannot give and refuses with CERT_TRUST_REVOCATION_STATUS_UNKNOWN; an
# OpenSSL curl, which checks no revocation by default, accepts and ignores it.
dex_tls=(--cacert "$rig_tmp/ca.crt" --ssl-no-revoke)
trap 'stop_forward; rm -rf "$rig_tmp"' EXIT

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '\033[0;37m    %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }
warn() { printf '\n\033[1;33mWARN: %s\033[0m\n' "$*" >&2; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }

# The instance name becomes a path under ~/.devicechain, so it is held to a DNS label: no
# separator or dot can walk `down` out of the directory it means to remove.
[[ "$instance" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] ||
  fail "DC_SSO_INSTANCE must be a lower-case DNS label, got '$instance'"

# OWNERSHIP. ~/.devicechain/instances/<name> holds an instance's local state, credentials
# included, and the name is the only thing tying it to a cluster. `up` records here that
# THIS rig created <name> on THIS kind cluster; `down` removes the state only when the
# record says so. The marker lives outside the instance directory so that nothing in this
# script ever needs to look inside that directory.
instance_dir="${HOME:?}/.devicechain/instances/$instance"
owner_marker="${HOME:?}/.devicechain/sso-rig-owners/$instance"
owned_by_rig() { [[ -f "$owner_marker" && "$(cat "$owner_marker")" == "$cluster" ]]; }

if [[ -n "${DC_VERSION:-}" ]]; then
  image_args=(--version "$DC_VERSION")
else
  image_args=(--build)
fi

k() { kubectl --context "$kube_context" "$@"; }

# The stub provider (Dex), its certificate, and the rig-only trust and egress edits, shared
# with hack/upgrade-rig.sh. It reads rig_tmp, cluster, namespace, instance and k from here.
# shellcheck source=lib/sso-stub-idp.sh
. "$repo_root/hack/lib/sso-stub-idp.sh"

# redact_secrets filters dcctl's output: the bootstrap prints the superuser's generated
# password once, on a line of its own ("password: <value>"), and a run of this rig is
# usually teed to a log.
redact_secrets() { sed -u -E 's/^([[:space:]]*password:[[:space:]]*).+$/\1[redacted by hack\/sso-rig.sh]/'; }

# redact_self_check proves the filter removes a password before any real one goes through it.
redact_self_check() {
  local out
  out="$(printf '  Superuser: superuser@devicechain.local\n           password: s3cr3t-Probe_value\n' | redact_secrets)"
  ! grep -q 's3cr3t-Probe_value' <<<"$out" || fail "redact_secrets let a password through: $out"
  grep -q 'Superuser: superuser@devicechain.local' <<<"$out" || fail "redact_secrets removed a line it should keep: $out"
}

build_dcctl() {
  say "building dcctl"
  make -C "$repo_root/backend/cli" build >/dev/null
  [[ -x "$dcctl" ]] || fail "dcctl was not built at $dcctl"
}

create_cluster() {
  if grep -qx -- "$cluster" <<<"$(kind get clusters 2>/dev/null)"; then
    say "kind cluster $cluster already exists; reusing it"
    return
  fi
  say "creating kind cluster $cluster"
  kind create cluster --name "$cluster" --config "$repo_root/deploy/local/kind-cluster.yaml" --wait 120s
}

# sso_outcome_total <outcome> [connection] prints the sum, across every user-management pod,
# of devicechain_usermanagement_sso_logins_total for that outcome (and connection, when
# given). It reads each pod's /metrics through the API server's pod proxy, so it needs no
# port-forward. A pod whose metrics cannot be read FAILS the rig: reading it as zero would
# let a control "see" a refusal it never measured.
sso_outcome_total() {
  local outcome="$1" connection="${2:-}" deploy port selector pods pod body total=0 n
  deploy="$(sso_stub_um_deploy)"
  port="$(k -n "$namespace" get "$deploy" -o jsonpath='{.spec.template.spec.containers[0].ports[?(@.name=="graphql")].containerPort}')"
  [[ -n "$port" ]] || fail "user-management has no container port named graphql to read metrics from"
  selector="$(k -n "$namespace" get "$deploy" -o json |
    jq -r '.spec.selector.matchLabels | to_entries | map("\(.key)=\(.value)") | join(",")')"
  pods="$(k -n "$namespace" get pods -l "$selector" --field-selector=status.phase=Running -o name)"
  [[ -n "$pods" ]] || fail "no running user-management pod to read metrics from"
  for pod in $pods; do
    body="$(k get --raw "/api/v1/namespaces/$namespace/pods/${pod#pod/}:$port/proxy/metrics")" ||
      fail "could not read the metrics of $pod"
    n="$(awk -v o="outcome=\"$outcome\"" -v c="connection=\"$connection\"" '
      /^devicechain_usermanagement_sso_logins_total\{/ && index($0, o) && (c == "connection=\"\"" || index($0, c)) { s += $NF }
      END { printf "%d", s }' <<<"$body")"
    total=$((total + n))
  done
  printf '%s\n' "$total"
}

forward_dex() {
  [[ -s "$pf_pid_file" ]] && kill -0 "$(cat "$pf_pid_file")" 2>/dev/null && return 0
  # kubectl itself, not the k function: backgrounding a function forks a subshell, so $!
  # would name the subshell and stop_forward would leave the real port-forward running.
  kubectl --context "$kube_context" -n "$dex_ns" port-forward svc/dex "$dex_port:$dex_port" >"$rig_tmp/pf.log" 2>&1 &
  echo $! >"$pf_pid_file"
  for _ in $(seq 1 30); do
    if curl -s "${dex_tls[@]}" --resolve "$dex_host:$dex_port:127.0.0.1" \
      -o /dev/null "https://$dex_host:$dex_port/dex/.well-known/openid-configuration"; then
      return 0
    fi
    sleep 1
  done
  # The scratch directory is removed on exit, so say what the log and a last probe said now.
  local last_probe
  last_probe="$(curl -sS "${dex_tls[@]}" --resolve "$dex_host:$dex_port:127.0.0.1" -o /dev/null \
    "https://$dex_host:$dex_port/dex/.well-known/openid-configuration" 2>&1)" || true
  fail "the Dex port-forward never answered. kubectl said: $(tail -n 5 "$rig_tmp/pf.log" 2>/dev/null)
curl said: $(sed -n '1p' <<<"$last_probe")"
}

stop_forward() {
  [[ -s "$pf_pid_file" ]] || return 0
  local pid winpid
  pid="$(cat "$pf_pid_file")"
  # On a Windows git-bash, kubectl is often a launcher shim that starts the real kubectl as a
  # child, and `kill` ends only the shim, leaving the real port-forward holding the port. End
  # the whole Windows process tree instead.
  if [[ -r "/proc/$pid/winpid" ]] && command -v taskkill >/dev/null 2>&1; then
    winpid="$(cat "/proc/$pid/winpid")"
    MSYS_NO_PATHCONV=1 taskkill /F /T /PID "$winpid" >/dev/null 2>&1 || true
  fi
  kill "$pid" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# API helpers
# ---------------------------------------------------------------------------

# gql <url> <token|-> <query> <variables-json> prints .data on success; on a GraphQL
# error prints the errors to stderr and returns 1.
gql() {
  # The default is assigned apart: inside double quotes "${4:-\{\}}" keeps the first
  # backslash, so a call with no variables sent the invalid JSON `\{}`.
  local url="$1" token="$2" query="$3" vars="${4:-}" body resp
  [[ -n "$vars" ]] || vars='{}'
  body="$(jq -n --arg q "$query" --argjson v "$vars" '{query: $q, variables: $v}')"
  if [[ "$token" == "-" ]]; then
    resp="$(curl -s -X POST "$url" -H 'Content-Type: application/json' -d "$body")"
  else
    resp="$(curl -s -X POST "$url" -H 'Content-Type: application/json' -H "Authorization: Bearer $token" -d "$body")"
  fi
  # An empty or non-JSON answer (a refused connection, an ingress error page) is a transport
  # failure, and it must not read as a GraphQL answer with no data in it.
  if [[ -z "$resp" ]] || ! jq -e . >/dev/null 2>&1 <<<"$resp"; then
    echo "transport: no GraphQL response from $url: ${resp:0:200}" >&2
    return 1
  fi
  if jq -e '.errors' >/dev/null 2>&1 <<<"$resp"; then
    jq -c '.errors' <<<"$resp" >&2
    return 1
  fi
  jq -c '.data' <<<"$resp" 2>/dev/null || { echo "not JSON: $resp" >&2; return 1; }
}

admin_token=""
admin_login() {
  local pw
  pw="$(k -n "$namespace" get secret "dci-$instance-superuser" -o jsonpath='{.data.password}' | base64 -d)"
  [[ -n "$pw" ]] || fail "Secret $namespace/dci-$instance-superuser holds no superuser password"
  admin_token="$(gql "$um/graphql" - \
    'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
    "$(jq -n --arg e "superuser@devicechain.local" --arg p "$pw" '{e:$e,p:$p}')" |
    jq -r '.login.identityToken')"
  [[ -n "$admin_token" && "$admin_token" != null ]] || fail "could not sign in as the superuser"
}

adm() { gql "$um/admin/graphql" "$admin_token" "$@"; }

# adm_tolerate_exists runs adm and treats an "already exists" conflict as success, so a
# re-run over a half-built rig does not stop at the first thing that was done last time.
adm_tolerate_exists() {
  local out
  if out="$(adm "$@" 2>&1)"; then printf '%s\n' "$out"; return 0; fi
  if grep -qi 'conflict\|already' <<<"$out"; then note "already present; continuing"; return 0; fi
  printf '%s\n' "$out" >&2
  return 1
}

# ---------------------------------------------------------------------------
# The headless sign-in
# ---------------------------------------------------------------------------

location_of() { tr -d '\r' | sed -n 's/^[Ll]ocation: //p'; }

dexcurl() { curl -s -i "${dex_tls[@]}" --resolve "$dex_host:$dex_port:127.0.0.1" "$@"; }

pkce_challenge() {
  printf '%s' "$1" | openssl dgst -sha256 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '='
}

# sso_signin <begin-email> <asserted-email> <group> [tamper-state] prints, on stdout, ONE
# line, and only the first three shapes are answers user-management gave:
#   code=<login code>|<verifier>   the provider's answer was accepted
#   error=<class>                  user-management sent the browser back with an error class
#   invalid_link                   the callback answered 400 with its "not valid" page (the
#                                  state was unusable, so there was nowhere to send it back)
#   begin_failed / begin_empty / unexpected:<detail>
#                                  anything else -- a transport failure, an ingress error, a
#                                  route that is missing. These are NEVER a refusal.
# It does what a browser does: begin, follow the provider, return to the callback.
#
# <begin-email> only selects the connection (by its domain), exactly as the console's
# sign-in page does; <asserted-email> is who the provider then says the person is. They
# differ only in the out-of-domain control, which needs a REAL connection selected so that
# the provider's answer reaches the callback's own address checks.
sso_signin() {
  local begin_email="$1" email="$2" group="$3" tamper="${4:-}" verifier challenge begin authurl loc cb dest resp status final
  verifier="$(openssl rand -hex 24)"
  challenge="$(pkce_challenge "$verifier")"
  begin="$(gql "$um/graphql" - \
    'mutation($e:String,$r:String!,$c:String!){beginSso(email:$e,returnTo:$r,codeChallenge:$c){authorizationUrl}}' \
    "$(jq -n --arg e "$begin_email" --arg r "$return_to" --arg c "$challenge" '{e:$e,r:$r,c:$c}')")" ||
    { echo "begin_failed"; return 0; }
  authurl="$(jq -r '.beginSso.authorizationUrl // empty' <<<"$begin")"
  [[ -n "$authurl" ]] || { echo "begin_empty"; return 0; }

  local hdr=(-H "X-Remote-User: ${email%%@*}" -H "X-Remote-Email: $email" -H "X-Remote-Group: $group")
  # Dex redirects /dex/auth to the connector's page, then the connector's page to its
  # callback; the proxy connector reads the person from the headers on both.
  loc="$(dexcurl "$authurl" | location_of)"
  [[ -n "$loc" ]] || fail "Dex did not redirect from $authurl"
  case "$loc" in /*) loc="https://$dex_host:$dex_port$loc" ;; esac
  cb="$(dexcurl "${hdr[@]}" "$loc" | location_of)"
  [[ -n "$cb" ]] || fail "Dex's proxy connector did not redirect from $loc"
  case "$cb" in /*) cb="https://$dex_host:$dex_port$cb" ;; esac
  dest="$(dexcurl "${hdr[@]}" "$cb" | location_of)"
  [[ "$dest" == "$um/sso/callback/$connection?"* ]] ||
    fail "Dex sent the browser to '$dest', not to the connection's callback address"

  if [[ -n "$tamper" ]]; then
    [[ "$dest" == *state=* ]] || fail "the callback address carries no state to tamper with: $dest"
    dest="${dest/state=/state=tampered}"
  fi
  resp="$(curl -s -i "$dest")" || { echo "unexpected:transport_error"; return 0; }
  status="$(head -n1 <<<"$resp" | tr -d '\r' | awk '{print $2}')"
  final="$(location_of <<<"$resp")"
  case "$status:$final" in
    "302:$return_to"*code=*)
      final="${final#*code=}"
      echo "code=${final%%&*}|$verifier" ;;
    "302:$return_to"*error=*)
      final="${final#*error=}"
      echo "error=${final%%&*}" ;;
    "400:")
      if grep -q 'This sign-in link is not valid or has already been used' <<<"$resp"; then
        echo "invalid_link"
      else
        echo "unexpected:status=400_without_the_callback_page"
      fi ;;
    *) echo "unexpected:status=${status:-none}:location=${final:-none}" ;;
  esac
}

# complete <code>|<verifier> prints the identity auth JSON, or returns 1.
complete_signin() {
  local code="${1%%|*}" verifier="${1#*|}"
  gql "$um/graphql" - \
    'mutation($c:String!,$v:String!){completeSso(code:$c,codeVerifier:$v){identityToken superuser memberships{tenant roles}}}' \
    "$(jq -n --arg c "$code" --arg v "$verifier" '{c:$c,v:$v}')"
}

# ---------------------------------------------------------------------------
# up
# ---------------------------------------------------------------------------

cmd_up() {
  need kind; need kubectl; need docker; need jq; need curl; need openssl; need go
  build_dcctl
  create_cluster
  if [[ -n "$(k get ns -l "devicechain.io/instance=$instance" -o name 2>/dev/null || true)" ]]; then
    fail "cluster $cluster already runs instance $instance; run 'hack/sso-rig.sh down' first"
  fi
  # Claim the name BEFORE bootstrap writes any local state for it, so a bootstrap that
  # fails half-way is still cleaned up by `down`. A name with local state this rig did not
  # create belongs to someone else: refuse rather than adopt it.
  if [[ -e "$instance_dir" ]] && ! owned_by_rig; then
    fail "$instance_dir already exists and this rig did not create it (no marker at $owner_marker).
Choose another name with DC_SSO_INSTANCE; this rig will not bootstrap over another instance's state."
  fi
  mkdir -p "$(dirname "$owner_marker")"
  printf '%s\n' "$cluster" >"$owner_marker"
  redact_self_check
  say "installing the cluster prerequisites"
  "$dcctl" install local --yes --compact --no-tls --kube-context "$kube_context" "${image_args[@]}" 2>&1 |
    redact_secrets
  say "bootstrapping $instance (plain http on localhost: a loopback issuer)"
  "$dcctl" bootstrap local "$instance" --yes --no-escrow --kube-context "$kube_context" \
    --host localhost --no-tls "${image_args[@]}" 2>&1 | redact_secrets | tee "$rig_tmp/bootstrap.out"
  # The filter matches dcctl's present wording. If that wording moves, the password would
  # pass straight through, so check the OUTPUT for the password itself, not for the filter's
  # marker. The password goes to grep on stdin (-f -), never on a command line.
  local pw
  pw="$(k -n "$namespace" get secret "dci-$instance-superuser" -o jsonpath='{.data.password}' | base64 -d)"
  [[ -n "$pw" ]] || fail "Secret $namespace/dci-$instance-superuser holds no superuser password to check the redaction against"
  if printf '%s\n' "$pw" | grep -qF -f - "$rig_tmp/bootstrap.out"; then
    fail "the superuser password reached this rig's output despite redact_secrets: dcctl's wording changed; fix the filter"
  fi
  note "the bootstrap output was checked: the superuser password is not in it"
  if [[ -n "$(k -n "$namespace" get networkpolicy -o name 2>/dev/null || true)" ]]; then
    warn "the instance namespace has NetworkPolicies; user-management's egress to Dex may be blocked"
  fi
  sso_stub_install_dex
  sso_stub_trust_and_egress
  say "UP COMPLETE: run 'hack/sso-rig.sh control' then 'hack/sso-rig.sh verify'"
}

# ---------------------------------------------------------------------------
# configure the connection (idempotent)
# ---------------------------------------------------------------------------

# ensure_egress puts the egress allowance back if a CONTROL 5 that stopped half-way left
# it out of the instance configuration, so a re-run does not start from a refusal.
ensure_egress() {
  local cidr doc
  cidr="$(sso_stub_dex_cidr)"
  doc="$(k -n "$namespace" get secret "dci-$instance-config" -o jsonpath='{.data.instance}' | base64 -d)"
  jq -e --arg c "$cidr" '[.. | objects | to_entries[] | select((.key | ascii_downcase) == "alloweddestinations") | .value[]] | index($c) != null'     >/dev/null <<<"$doc" && return 0
  warn "the egress allowance for Dex ($cidr) is missing; putting it back"
  sso_stub_set_egress "$cidr"
  sso_stub_roll_um
}

configure_connection() {
  local tier fp
  say "creating the tenant, the role and the connection"
  ensure_egress
  admin_login
  tier="$(adm '{tenantTiers{token}}' | jq -r '.tenantTiers[0].token')"
  [[ -n "$tier" && "$tier" != null ]] || fail "the instance has no tenant tier to create a tenant at"
  adm_tolerate_exists 'mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}' \
    "$(jq -n --arg t "$tenant" --arg tier "$tier" '{r:{token:$t,name:"SSO rig",tierToken:$tier}}')" >/dev/null
  adm_tolerate_exists 'mutation($r:AdminRoleCreateRequest!){createRole(request:$r){token}}' \
    "$(jq -n --arg t "$mapped_role" '{r:{scope:"tenant",token:$t,name:"SSO viewer",authorities:[]}}')" >/dev/null

  # Created disabled, discovery fetched THROUGH the egress guard over TLS, trusting the rig
  # CA through the connection's own bundle: this call is where a missing egress allowance or
  # an untrusted certificate shows up.
  adm_tolerate_exists 'mutation($r:IdpConnectionCreateRequest!){createIdpConnection(request:$r){token}}' \
    "$(jq -n --arg t "$connection" --arg i "$dex_issuer" --arg c "$dex_client" --arg s "$dex_secret" \
      --rawfile ca "$rig_tmp/ca.crt" \
      '{r:{token:$t,name:"Dex (rig)",kind:"tenant",issuer:$i,clientId:$c,clientSecret:$s,
           scopes:["openid","email","profile","groups"],caBundle:$ca}}')" >/dev/null ||
    fail "the connection could not be created. If the message names a certificate, the CA bundle was not
taken; if it names a blocked address, the rig's egress edit did not take effect (see
sso_stub_trust_and_egress in hack/lib/sso-stub-idp.sh)."
  # A connection left by an earlier run (or by a control that stopped half-way) is brought
  # back to the rig CA. An unchanged bundle is a no-op.
  set_ca_bundle "$(cat "$rig_tmp/ca.crt")" >/dev/null ||
    fail "the connection's CA bundle could not be set to the rig CA"
  fp="$(adm 'query($t:String!){idpConnection(token:$t){discovery{fingerprint}}}' "$(jq -n --arg t "$connection" '{t:$t}')" |
    jq -r '.idpConnection.discovery.fingerprint')"
  adm 'mutation($t:String!,$f:String!){approveIdpDiscovery(token:$t,fingerprint:$f){token}}' \
    "$(jq -n --arg t "$connection" --arg f "$fp" '{t:$t,f:$f}')" >/dev/null
  adm_tolerate_exists 'mutation($t:String!,$d:String!){addIdpDomain(token:$t,domain:$d){token}}' \
    "$(jq -n --arg t "$connection" --arg d "$verified_domain" '{t:$t,d:$d}')" >/dev/null
  adm 'mutation($t:String!,$x:[String!]!){setIdpConnectionTenants(token:$t,tenants:$x){token}}' \
    "$(jq -n --arg t "$connection" --arg x "$tenant" '{t:$t,x:[$x]}')" >/dev/null
  adm 'mutation($c:String!){setIdpJit(connection:$c,enabled:true,defaultTenantRoles:[]){token}}' \
    "$(jq -n --arg c "$connection" '{c:$c}')" >/dev/null
  adm 'mutation($c:String!,$t:String!,$m:[IdpRoleMappingInput!]!){setIdpRoleMappings(connection:$c,tenant:$t,mappings:$m){token}}' \
    "$(jq -n --arg c "$connection" --arg t "$tenant" --arg g "$mapped_group" --arg r "$mapped_role" \
      '{c:$c,t:$t,m:[{group:$g,role:$r}]}')" >/dev/null
  adm 'mutation($t:String!,$r:IdpConnectionUpdateRequest!){updateIdpConnection(token:$t,request:$r){enabled}}' \
    "$(jq -n --arg t "$connection" '{t:$t,r:{enabled:true}}')" >/dev/null
}

# identity_exists <email> succeeds when an identity has that email and returns 1 when none
# does. A listing that cannot be read FAILS the rig instead: read as "absent", it would let
# a control pass and a precondition hold on nothing.
identity_exists() {
  local ids rc=0
  ids="$(adm '{identities{email}}')" || fail "could not list identities to look for $1"
  jq -e --arg e "$1" '[.identities[].email] | index($e) != null' >/dev/null <<<"$ids" || rc=$?
  case "$rc" in
    0) return 0 ;;
    1) return 1 ;;
    *) fail "could not read the identity listing while looking for $1" ;;
  esac
}

# ---------------------------------------------------------------------------
# verify: the positive path
# ---------------------------------------------------------------------------

cmd_verify() {
  need kubectl; need jq; need curl; need openssl
  [[ -s "$rig_tmp/ca.crt" ]] || fetch_ca
  forward_dex
  configure_connection
  # A person NO identity exists for, proven absent first: the controls sign $good_email in,
  # so verifying with that address would credit provisioning for an identity that a control
  # had already created. A fresh address per run keeps verify re-runnable on one cluster.
  local fresh_email
  fresh_email="jit-$(date +%s)-$RANDOM@$verified_domain"
  identity_exists "$fresh_email" &&
    fail "an identity already exists for $fresh_email, so this run cannot show provisioning"
  say "signing in as $fresh_email (no identity yet), a member of group $mapped_group"
  local result auth roles
  result="$(sso_signin "$fresh_email" "$fresh_email" "$mapped_group")"
  [[ "$result" == code=* ]] || fail "the sign-in was not accepted: $result"
  auth="$(complete_signin "${result#code=}")" || fail "completeSso failed for an accepted sign-in"
  roles="$(jq -r --arg t "$tenant" '[.completeSso.memberships[] | select(.tenant == $t) | .roles[]] | sort | join(",")' <<<"$auth")"
  [[ "$roles" == "$mapped_role" ]] ||
    fail "expected membership in $tenant with role $mapped_role, got roles '$roles': $auth"
  [[ "$(jq -r '.completeSso.superuser' <<<"$auth")" == false ]] || fail "a tenant connection produced a superuser session"
  identity_exists "$fresh_email" || fail "no identity was provisioned for $fresh_email"
  say "VERIFY COMPLETE: $fresh_email, absent before, was provisioned and holds $mapped_role in $tenant, through Dex over TLS via the egress guard"
}

# fetch_ca recovers the rig CA from the cluster when verify runs in a fresh shell (the
# working directory is per run). It is the CA the Dex certificate was minted by.
fetch_ca() {
  k -n "$dex_ns" get configmap dex-ca -o jsonpath='{.data.ca\.crt}' >"$rig_tmp/ca.crt" 2>/dev/null ||
    fail "the rig CA ConfigMap $dex_ns/dex-ca is missing; run 'hack/sso-rig.sh up' first"
  grep -q 'BEGIN CERTIFICATE' "$rig_tmp/ca.crt" || fail "could not recover the rig CA from $dex_ns/dex-ca"
}

# set_ca_bundle <pem|""> replaces the connection's private CA bundle ("" clears it).
set_ca_bundle() {
  adm 'mutation($t:String!,$b:String!){setIdpCaBundle(token:$t,caBundle:$b){token caCertificates{subject fingerprintSha256}}}' \
    "$(jq -n --arg t "$connection" --arg b "$1" '{t:$t,b:$b}')"
}

# refresh_discovery fetches the provider's discovery document again, under the connection's
# current bundle and through the egress guard as the running pod has it configured. It
# prints the data on success; on failure the errors go to stderr and it returns 1.
refresh_discovery() {
  adm 'mutation($t:String!){refreshIdpDiscovery(token:$t){token}}' "$(jq -n --arg t "$connection" '{t:$t}')"
}

# refused_for <want-regex> <reject-regex> <what>: refresh_discovery must FAIL, with an
# error that matches <want-regex> and not <reject-regex>. A transport failure (no GraphQL
# answer) is never a refusal.
refused_for() {
  local want="$1" reject="$2" what="$3" out
  if out="$(refresh_discovery 2>&1)"; then
    fail "$what: discovery was fetched successfully, so nothing was refused"
  fi
  [[ "$out" != transport:* ]] || fail "$what: no GraphQL answer, so this proves nothing: $out"
  grep -qE -- "$want" <<<"$out" || fail "$what: refused, but not for the expected reason ($want): $out"
  ! grep -qE -- "$reject" <<<"$out" || fail "$what: refused for the OTHER reason ($reject): $out"
  printf '%s\n' "$out"
}

# ---------------------------------------------------------------------------
# control
# ---------------------------------------------------------------------------

# refused_as <result> <expected> succeeds only when the sign-in ended in EXACTLY the
# expected refusal: `error=refused` (sent back with that class) or `invalid_link` (the
# callback's 400 page). A success, a refusal of another kind, and anything sso_signin could
# not attribute to user-management (begin_failed, begin_empty, unexpected:...) all fail it.
refused_as() { [[ -n "$2" && "$1" == "$2" ]]; }

# detector_self_check proves refused_as can say NO before any control relies on its YES.
detector_self_check() {
  local wrong
  refused_as "error=refused" "error=refused" || fail "the detector does not recognise the refusal it expects"
  refused_as "invalid_link" "invalid_link" || fail "the detector does not recognise the refusal it expects"
  for wrong in "code=abc|def" "error=link_required" "error=idp_error" "invalid_link" \
    "begin_failed" "begin_empty" "unexpected:status=502:location=none" "unexpected:transport_error" ""; do
    if refused_as "$wrong" "error=refused"; then
      fail "the detector accepted '$wrong' as the refusal 'error=refused'"
    fi
  done
  for wrong in "error=refused" "unexpected:status=400_without_the_callback_page" "code=abc|def"; do
    if refused_as "$wrong" "invalid_link"; then
      fail "the detector accepted '$wrong' as the refusal 'invalid_link'"
    fi
  done
}

cmd_control() {
  need kubectl; need jq; need curl; need openssl
  [[ -s "$rig_tmp/ca.crt" ]] || fetch_ca
  forward_dex
  configure_connection

  say "CONTROL 0: the detector must reject a success and a refusal of the wrong kind"
  detector_self_check
  note "refused_as rejected a success, the wrong refusal class and every non-answer"
  local good
  good="$(sso_signin "$good_email" "$good_email" "$mapped_group")"
  [[ "$good" == code=* ]] ||
    fail "the baseline sign-in was not accepted ($good), so the controls below would prove nothing. Fix the positive path first."
  if refused_as "$good" "error=refused" || refused_as "$good" "invalid_link"; then
    fail "the detector read a live success ($good) as a refusal"
  fi
  note "a live good sign-in answers 'code=...', which refused_as rejects"

  # CONTROL 1 begins with the GOOD address, so beginSso selects the real connection and the
  # provider is consulted; the provider then asserts an address outside the connection's
  # domain. Beginning with the bad address would stop at beginSso (no connection has that
  # domain) and never reach the rule this control exists to test.
  say "CONTROL 1: the provider asserting an address outside the connection's verified domain must be refused by the domain rule"
  local r1 before after
  before="$(sso_outcome_total refused_unverified_domain "$connection")"
  r1="$(sso_signin "$good_email" "$bad_email" "$mapped_group")"
  refused_as "$r1" "error=refused" ||
    fail "a sign-in asserting $bad_email was not refused with error=refused (got $r1)"
  after="$(sso_outcome_total refused_unverified_domain "$connection")"
  [[ "$after" -eq $((before + 1)) ]] ||
    fail "$bad_email was refused, but refused_unverified_domain went $before -> $after rather than up by one: some other check refused it, so the domain rule is unproven"
  if identity_exists "$bad_email"; then
    fail "an identity was created for $bad_email despite the refusal"
  fi
  note "refused with $r1, counted as refused_unverified_domain ($before -> $after)"

  say "CONTROL 2: a tampered state must be refused as an unusable state"
  local r2
  before="$(sso_outcome_total state_invalid)"
  r2="$(sso_signin "$good_email" "$good_email" "$mapped_group" tamper)"
  refused_as "$r2" "invalid_link" || fail "a tampered state was not refused with the callback's invalid-link page (got $r2)"
  after="$(sso_outcome_total state_invalid)"
  [[ "$after" -eq $((before + 1)) ]] ||
    fail "the tampered state was refused, but state_invalid went $before -> $after rather than up by one"
  note "refused with $r2, counted as state_invalid ($before -> $after)"

  say "CONTROL 3: a login code redeems exactly once"
  local r3 out
  r3="$(sso_signin "$good_email" "$good_email" "$mapped_group")"
  [[ "$r3" == code=* ]] || fail "could not obtain a code for the replay control: $r3"
  complete_signin "${r3#code=}" >/dev/null || fail "the first redemption of a fresh code failed"
  if out="$(complete_signin "${r3#code=}" 2>&1)"; then
    fail "a login code was redeemed twice"
  fi
  grep -q 'invalid or expired sign-in code' <<<"$out" ||
    fail "the second redemption failed, but not as a spent code (a transport or server error proves nothing): $out"
  note "the second redemption was refused as an invalid or expired code"

  # CONTROLS 4 and 5 each take one of the two things that let user-management reach Dex
  # away, and require a refetch of discovery to be refused for THAT reason and not the
  # other. A refetch that succeeds first shows the instrument can say yes.
  local tls_reason='x509: certificate signed by unknown authority' egress_reason='destination address is not permitted'
  refresh_discovery >/dev/null || fail "discovery could not be refetched with the bundle and the allowance both in place, so the controls below would prove nothing"
  note "with the CA bundle and the egress allowance in place, discovery refetches"

  say "CONTROL 4: with the connection's CA bundle cleared, the provider must be refused as untrusted"
  local ca_pem certs
  ca_pem="$(cat "$rig_tmp/ca.crt")"
  certs="$(set_ca_bundle "")" || fail "the connection's CA bundle could not be cleared"
  [[ "$(jq '.setIdpCaBundle.caCertificates | length' <<<"$certs")" == 0 ]] ||
    fail "the bundle was cleared but the connection still lists CA certificates: $certs"
  out="$(refused_for "$tls_reason" "$egress_reason" "CONTROL 4")"
  note "refused: ${out:0:300}"
  certs="$(set_ca_bundle "$ca_pem")" || fail "the rig CA could not be set back as the connection's bundle"
  [[ "$(jq -r '[.setIdpCaBundle.caCertificates[].subject] | join(",")' <<<"$certs")" == *dc-sso-rig-ca* ]] ||
    fail "the bundle was set back but the connection does not list the rig CA: $certs"
  refresh_discovery >/dev/null || fail "with the rig CA set back, discovery still could not be refetched"
  note "with the rig CA set back as the bundle, discovery refetches again"

  say "CONTROL 5: with the rig's egress allowance taken away, the egress guard must refuse Dex's address"
  sso_stub_set_egress
  sso_stub_roll_um
  out="$(refused_for "$egress_reason" "$tls_reason" "CONTROL 5")"
  note "refused: ${out:0:300}"
  sso_stub_set_egress "$(sso_stub_dex_cidr)"
  sso_stub_roll_um
  refresh_discovery >/dev/null || fail "with the egress allowance restored, discovery still could not be refetched"
  note "with the allowance restored, discovery refetches again"

  say "CONTROLS COMPLETE: the out-of-domain address, the tampered state, the replayed code, the missing CA bundle and the missing egress allowance were each refused for their own reason, and the detector was shown to reject a success and the wrong refusal"
}

# ---------------------------------------------------------------------------
# down
# ---------------------------------------------------------------------------

cmd_down() {
  need kind; need kubectl; need jq
  # The instance's local state is touched -- by dcctl destroy or by rm -- ONLY when this rig's
  # marker says it created this name on this cluster. Without it the name may be another
  # instance's, so the state is left alone and its path (never its contents) is printed.
  local owned=false
  owned_by_rig && owned=true
  if [[ "$owned" == true && -x "$dcctl" && -d "$instance_dir" ]] &&
    grep -qx -- "$cluster" <<<"$(kind get clusters 2>/dev/null)"; then
    "$dcctl" destroy local "$instance" --yes --kube-context "$kube_context" ||
      warn "dcctl destroy of $instance failed; deleting the cluster anyway"
  fi
  local uid=""
  if grep -qx -- "$cluster" <<<"$(kind get clusters 2>/dev/null)"; then
    uid="$(k get ns kube-system -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
    kind delete cluster --name "$cluster"
  fi
  local clusters="${HOME:?}/.devicechain/clusters" dir
  case "$uid" in . | .. | */* | *\\*) fail "refusing to remove state for cluster UID '$uid'" ;; esac
  [[ -z "$uid" ]] || rm -rf "${clusters:?}/$uid"
  if [[ -d "$clusters" ]]; then
    for dir in "$clusters"/*/; do
      [[ -f "$dir/cluster.json" ]] || continue
      if [[ "$(jq -r '.kubeContext // empty' "$dir/cluster.json" 2>/dev/null || true)" == "$kube_context" ]]; then
        rm -rf "${dir:?}"
      fi
    done
  fi
  if [[ "$owned" == true ]]; then
    rm -rf "${instance_dir:?}"
    rm -f "$owner_marker"
  elif [[ -e "$instance_dir" ]]; then
    warn "NOT removing $instance_dir: this rig has no marker ($owner_marker) saying it created
instance '$instance' on kind cluster '$cluster', so it may belong to another instance.
Remove it yourself if it is the rig's."
  fi
}

case "${1:-all}" in
  up) cmd_up ;;
  verify) cmd_verify ;;
  control) cmd_control ;;
  down) cmd_down ;;
  all)
    cmd_up
    cmd_control
    cmd_verify
    say "SINGLE SIGN-ON RIG COMPLETE: the controls were shown to refuse, then a person was provisioned
with the mapped role through a real provider.

NOT COVERED, so this is not read as more than it is: only Dex's authproxy connector was
used (not Entra, Okta or Google Workspace); only the tenant connection kind; no session
ceiling, revocation or Require-SSO path; and the egress allowance for the provider's
address was a rig-only edit to the instance configuration, not an operator's change through
the chart. The provider's certificate was trusted through the connection's own CA bundle."
    ;;
  *) fail "unknown command ${1}; try up | verify | control | all | down" ;;
esac
