# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The checkers, controls and steps of hack/conformance-rig.sh. SOURCED by it, never run alone: it uses
# the rig's helpers (say, pass, fail, k, dk, jq) and variables (instance, tenant, node_ip, ...).
#
# THE SHAPE OF A CHECK. A scenario drives a third-party client against the instance and then READS THE
# PLATFORM: the event store, the command record, the device-state record, the platform's own counters.
# What it read goes into a RECORD (the R_* variables), and a checker is a pure function of that record:
# it prints one line per source that disagreed and returns 1 if any did. Because a checker never touches
# the cluster, a control can hand it a fabricated record of the opposite verdict and require RED, offline
# and first (`controls`). A checker that cannot go red is not a check.

# ---------------------------------------------------------------------------
# CHECKERS
# ---------------------------------------------------------------------------

R_RC=0 R_STORED=0 R_COUNTER=0 R_CODE="" R_RETRY="" R_STATUS="" R_OUTCOME="" R_PAYLOAD="" R_WANT=""
R_READBACK="" R_EXISTS=0 R_ACTIVE="" R_PRESENCE="" R_INITIAL=0 R_CHANGED=0 R_COUNT=0 R_ATTR=""

# why <message>: one line per disagreeing source, collected by the checker.
bad=0
why() { printf '      - %s\n' "$*"; bad=1; }

# chk_publish: an MQTT publish. The client's exit status is 0 only if the broker took the connection AND,
# for QoS 1, acknowledged the publish; the store must hold the value; the platform must have counted it.
chk_publish() {
  bad=0
  ((R_RC == 0)) || why "client: the publish was not accepted (exit $R_RC)"
  ((R_STORED == 1)) || why "store: the measurement was not stored"
  ((R_COUNTER >= 1)) || why "counters: the platform counted no inbound message"
  return "$bad"
}

# chk_command: a command settled by the device. The command record is the truth: SUCCESSFUL, and its
# outcome SUCCEEDED (the device reported success), not merely sent.
chk_command() {
  bad=0
  [[ "$R_STATUS" == SUCCESSFUL ]] || why "command: status is '$R_STATUS', not SUCCESSFUL"
  [[ "$R_OUTCOME" == SUCCEEDED ]] || why "command: outcome is '$R_OUTCOME', not SUCCEEDED"
  return "$bad"
}

chk_http_202() {
  bad=0
  [[ "$R_CODE" == 202 ]] || why "http: answered '$R_CODE', not 202"
  ((R_STORED == 1)) || why "store: the measurement was not stored"
  ((R_COUNTER >= 1)) || why "counters: the platform counted no inbound message"
  return "$bad"
}

# chk_http_refusal <code>: a refused event. The status, a numeric Retry-After, the store (the refused
# event must NOT be there) and the platform's refusal counter must all agree.
chk_http_429() { chk_http_refusal 429; }
chk_http_503() { chk_http_refusal 503; }
chk_http_refusal() {
  bad=0
  [[ "$R_CODE" == "$1" ]] || why "http: answered '$R_CODE', not $1"
  [[ "$R_RETRY" =~ ^[0-9]+$ ]] || why "http: Retry-After is '$R_RETRY', not a number of seconds"
  ((R_STORED == 0)) || why "store: the refused event was stored"
  ((R_COUNTER >= 1)) || why "counters: the platform counted no refusal"
  return "$bad"
}

# chk_presence: a device that registered over a presence-asserting transport and is connected.
chk_presence() {
  bad=0
  ((R_EXISTS == 1)) || why "store: no device was created for the identity"
  [[ "$R_ACTIVE" == true ]] || why "device-state: active is '$R_ACTIVE', not true"
  [[ "$R_PRESENCE" == ASSERTED ]] || why "device-state: presence is '$R_PRESENCE', not ASSERTED"
  return "$bad"
}

# chk_observe: telemetry from an observed object. The initial value comes with the Observe response, a
# changed value can only arrive as a notification, so both must be in the store.
chk_observe() {
  bad=0
  ((R_INITIAL == 1)) || why "store: the client's initial value was not stored"
  ((R_CHANGED == 1)) || why "store: no changed value (a notification) was stored"
  return "$bad"
}

# body_text <payload>: a lwm2m.read answer as the platform returned it, then the printable runs of its
# base64 decoding. The platform hands a Read body back verbatim in the device's own content format, as
# base64 when it is not text: Eclipse Leshan answers a single-resource Read in TLV, so "DeviceChain"
# arrives as the base64 of c8 00 0b followed by the eleven bytes of the string.
body_text() {
  local p="$1"
  p="${p#\"}"; p="${p%\"}"
  printf '%s\n' "$p"
  printf '%s' "$p" | base64 -d 2>/dev/null | LC_ALL=C tr -c '[:print:]' ' ' || true
}

chk_read() {
  chk_command || true
  [[ -n "$R_WANT" && "$(body_text "$R_PAYLOAD")" == *"$R_WANT"* ]] || why "command: the response does not hold '$R_WANT' (got '$R_PAYLOAD')"
  return "$bad"
}

chk_write() {
  chk_command || true
  [[ -n "$R_WANT" && "$(body_text "$R_READBACK")" == *"$R_WANT"* ]] || why "device: a read afterwards returns '$R_READBACK', not the '$R_WANT' written"
  return "$bad"
}

# chk_info_tlv_unread: the limitation HOLDS. The device-information reader decodes text/plain only, so
# against a client that answers a single-resource Read in TLV it reads nothing: the command fails and
# names the format. Green means the gap is still there (the matrix says so); red means it has closed.
chk_info_tlv_unread() {
  bad=0
  [[ "$R_STATUS" == FAILED ]] || why "command: status is '$R_STATUS', not FAILED; the limitation no longer holds"
  [[ "$R_PAYLOAD" == *"unsupported content format"* ]] ||
    why "command: the answer does not name a content format it could not decode (got '${R_PAYLOAD:0:120}')"
  return "$bad"
}

# chk_harvest_tlv_unread: the limitation HOLDS for the registration-time harvest, which uses the same
# reader. An absence proves something only if the store was actually asked, so the query must answer.
chk_harvest_tlv_unread() {
  bad=0
  ((R_QUERIED == 1)) || why "store: the client-attribute query did not answer, so an absence proves nothing"
  [[ -z "$R_ATTR" ]] || why "store: client attribute manufacturer was recorded as '$R_ATTR'; the limitation no longer holds"
  return "$bad"
}

# chk_tlv_limit: the limitation HOLDS. A client that cannot produce SenML registers and shows connected,
# and not one reading of it is stored. Green means the limitation is still there (the matrix says so).
chk_tlv_limit() {
  bad=0
  [[ "$R_ACTIVE" == true ]] || why "device-state: the TLV-only client is not connected"
  ((R_COUNT == 0)) || why "store: $R_COUNT reading(s) were stored for a client that cannot produce SenML; the limitation no longer holds"
  return "$bad"
}

chk_sp_data() {
  bad=0
  ((R_STORED == 1)) || why "store: the data value was not stored under the metric the birth gave that alias"
  return "$bad"
}

# ---------------------------------------------------------------------------
# RESULTS BOOKKEEPING
# ---------------------------------------------------------------------------

check_rows=()   # id|status|evidence
control_rows=() # id|check|outcome|detail

# one_line <text>: the first 220 characters on one line, JSON-safe via jq later.
one_line() { printf '%s' "$1" | tr '\n\r\t' '   ' | cut -c1-220; }

# record_check <id> <checker> <evidence>: runs the checker on the current record and files the verdict.
record_check() {
  local id="$1" fn="$2" ev="$3" out rc=0
  out="$("$fn" 2>&1)" || rc=$?
  if ((rc == 0)); then
    check_rows+=("$id|pass|$(one_line "$ev")")
    pass "$id: $ev"
  else
    check_rows+=("$id|fail|$(one_line "$ev; $out")")
    printf '\033[1;31m    FAIL %s\033[0m\n%s\n' "$id" "$out"
  fi
  # The verdict is filed; a failing check must not stop the rig before it has written what it saw.
  return 0
}

# record_control <id> <check id> <checker> <detail>: runs the checker on the current (defective or
# fabricated) record. The control counts only if the checker goes RED; GREEN means it cannot tell.
record_control() {
  local id="$1" chk="$2" fn="$3" detail="$4" out rc=0
  out="$("$fn" 2>&1)" || rc=$?
  if ((rc != 0)); then
    control_rows+=("$id|$chk|red|$(one_line "$detail")")
    red "$id: $chk went red ($(one_line "${out//$'\n'/ }" | cut -c1-110))"
  else
    control_rows+=("$id|$chk|green|$(one_line "$detail")")
    printf '\033[1;31m    BLIND %s: %s stayed GREEN on a record it must refuse\033[0m\n' "$id" "$chk"
  fi
}

# A fabricated record: every field is reset, then the arguments set the ones that matter.
reset_record() {
  R_RC=0 R_STORED=0 R_COUNTER=0 R_CODE="" R_RETRY="" R_STATUS="" R_OUTCOME="" R_PAYLOAD="" R_WANT=""
  R_READBACK="" R_EXISTS=0 R_ACTIVE="" R_PRESENCE="" R_INITIAL=0 R_CHANGED=0 R_COUNT=0 R_ATTR="" R_QUERIED=0
}

# ---------------------------------------------------------------------------
# OFFLINE CONTROLS: every checker is shown GREEN on a good record and RED on its opposite, with no cluster.
# ---------------------------------------------------------------------------

# expect_green <label> <checker>
expect_green() {
  local out rc=0
  out="$("$2" 2>&1)" || rc=$?
  ((rc == 0)) || fail "offline: $1: the checker is RED on a good record: $out"
}

offline_controls() {
  say "OFFLINE CONTROLS: each checker goes GREEN on a good record and RED on the opposite"
  control_rows=()

  reset_record; R_RC=0 R_STORED=1 R_COUNTER=1; expect_green "publish" chk_publish
  reset_record; R_RC=0 R_STORED=0 R_COUNTER=1; record_control R-mqtt-qos0 mqtt-qos0-publish chk_publish "the client reports a publish but nothing was stored"
  reset_record; R_RC=1 R_STORED=1 R_COUNTER=1; record_control R-mqtt-qos1 mqtt-qos1-publish chk_publish "the broker never acknowledged the publish, though a value is stored"

  reset_record; R_CODE=202 R_STORED=1 R_COUNTER=1; expect_green "http-202" chk_http_202
  reset_record; R_CODE=400 R_STORED=0 R_COUNTER=0; record_control R-http-202 http-202 chk_http_202 "the request was answered 400"
  reset_record; R_CODE=429 R_RETRY=1 R_STORED=0 R_COUNTER=1; expect_green "http-429" chk_http_429
  reset_record; R_CODE=202 R_RETRY="" R_STORED=1 R_COUNTER=0; record_control R-http-429 http-429 chk_http_429 "the request was accepted with 202 and stored"
  reset_record; R_CODE=503 R_RETRY=2 R_STORED=0 R_COUNTER=1; expect_green "http-503" chk_http_503
  reset_record; R_CODE=503 R_RETRY="" R_STORED=0 R_COUNTER=1; record_control R-http-503 http-503-retry-after chk_http_503 "the 503 carries no Retry-After header"

  reset_record; R_EXISTS=1 R_ACTIVE=true R_PRESENCE=ASSERTED; expect_green "presence" chk_presence
  reset_record; R_EXISTS=1 R_ACTIVE=false R_PRESENCE=ASSERTED; record_control R-lwm2m-register lwm2m-register chk_presence "the device exists but its presence reads disconnected"
  reset_record; R_EXISTS=0; record_control R-sparkplug-birth sparkplug-birth chk_presence "no device was registered"

  reset_record; R_INITIAL=1 R_CHANGED=1; expect_green "observe" chk_observe
  reset_record; R_INITIAL=1 R_CHANGED=0; record_control R-lwm2m-observe lwm2m-observe chk_observe "only the initial value was stored; a different value than the one notified"

  reset_record; R_STATUS=SUCCESSFUL R_OUTCOME=SUCCEEDED R_PAYLOAD="DeviceChain" R_WANT=DeviceChain; expect_green "read" chk_read
  reset_record; R_STATUS=SUCCESSFUL R_OUTCOME=SUCCEEDED R_PAYLOAD='"yAALRGV2aWNlQ2hhaW4="' R_WANT=DeviceChain; expect_green "read (TLV, base64)" chk_read
  reset_record; R_STATUS=FAILED R_OUTCOME=REJECTED R_PAYLOAD="" R_WANT=DeviceChain; record_control R-lwm2m-read lwm2m-read chk_read "the command failed"
  reset_record; R_STATUS=SUCCESSFUL R_OUTCOME=SUCCEEDED R_READBACK="Europe/Zurich" R_WANT="Europe/Zurich"; expect_green "write" chk_write
  reset_record; R_STATUS=SUCCESSFUL R_OUTCOME=SUCCEEDED R_READBACK="Etc/UTC" R_WANT="Europe/Zurich"; record_control R-lwm2m-write lwm2m-write chk_write "the command succeeded but a read afterwards returns the old value"
  reset_record; R_STATUS=SUCCESSFUL R_OUTCOME=SUCCEEDED; expect_green "execute" chk_command
  reset_record; R_STATUS=FAILED R_OUTCOME=REJECTED; record_control R-lwm2m-execute lwm2m-execute chk_command "the command failed"
  reset_record; R_STATUS=TIMEOUT R_OUTCOME=UNKNOWN; record_control R-lwm2m-reboot lwm2m-reboot chk_command "the command timed out"

  reset_record; R_STATUS=FAILED R_PAYLOAD="no Device-object resource could be read: manufacturer: unsupported content format 11542 (only text/plain is decoded)"
  expect_green "info (TLV unread)" chk_info_tlv_unread
  reset_record; R_STATUS=SUCCESSFUL R_PAYLOAD='{"manufacturer":"DeviceChain","missing":[{"resource":"firmwareVersion"}]}'
  record_control R-lwm2m-device-info lwm2m-device-info-tlv-unread chk_info_tlv_unread "the command succeeded with the manufacturer the client holds"

  reset_record; R_QUERIED=1 R_ATTR=""; expect_green "harvest (TLV unread)" chk_harvest_tlv_unread
  reset_record; R_QUERIED=1 R_ATTR=DeviceChain
  record_control R-lwm2m-harvest lwm2m-harvest-tlv-unread chk_harvest_tlv_unread "the manufacturer the client holds was recorded as a client attribute"

  reset_record; R_ACTIVE=true R_COUNT=0; expect_green "tlv limit" chk_tlv_limit
  reset_record; R_ACTIVE=true R_COUNT=3; record_control R-lwm2m-tlv-limit lwm2m-tlv-only-no-telemetry chk_tlv_limit "telemetry from the TLV-only client was stored, so the limitation no longer holds"

  reset_record; R_STORED=1; expect_green "sparkplug data" chk_sp_data
  reset_record; R_STORED=1 R_STATUS=SUCCESSFUL R_OUTCOME=SUCCEEDED; expect_green "command" chk_command
  pass "all checkers are GREEN on a good record and RED on its opposite (${#control_rows[@]} record controls)"
}

# ---------------------------------------------------------------------------
# API helpers
# ---------------------------------------------------------------------------

origin="https://$host"
api="https://$host:$https_port"
um="$api/api/user-management"
dm="$api/api/device-management"
em="$api/api/event-management"
cdl="$api/api/command-delivery"
dst="$api/api/device-state"
ingress_ca="" broker_ca="" node_ip="" admin_token="" tenant_token="" main_token="" rl_token=""

curl_revoke=()
[[ "$(curl -V 2>/dev/null)" != *[Ss]channel* ]] || curl_revoke=(--ssl-no-revoke)
curl_api() { curl -sS "${curl_revoke[@]}" --resolve "$host:$https_port:127.0.0.1" --cacert "$(winpath "$ingress_ca")" "$@"; }

# gql <url> <token|-> <query> [variables-json] prints .data; a GraphQL error prints to stderr, returns 1.
gql() {
  local url="$1" token="$2" query="$3" vars="${4:-}" body resp auth=()
  [[ -n "$vars" ]] || vars='{}'
  [[ "$token" == "-" ]] || auth=(-H "Authorization: Bearer $token")
  body="$(jq -n --arg q "$query" --argjson v "$vars" '{query: $q, variables: $v}')"
  resp="$(curl_api -X POST "$url" -H 'Content-Type: application/json' "${auth[@]}" --data-binary @- <<<"$body")" || true
  if [[ -z "$resp" ]] || ! jq -e . >/dev/null 2>&1 <<<"$resp"; then
    echo "transport: no GraphQL response from $url: ${resp:0:200}" >&2
    return 1
  fi
  if jq -e '.errors' >/dev/null 2>&1 <<<"$resp"; then
    jq -c '.errors' <<<"$resp" >&2
    return 1
  fi
  jq -c '.data' <<<"$resp"
}
adm() { gql "$um/admin/graphql" "$admin_token" "$@"; }
dmq() { gql "$dm/graphql" "$tenant_token" "$@"; }
emq() { gql "$em/graphql" "$tenant_token" "$@"; }
cdq() { gql "$cdl/graphql" "$tenant_token" "$@"; }
dsq() { gql "$dst/graphql" "$tenant_token" "$@"; }

login() {
  gql "$um/graphql" - 'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
    "$(jq -n --arg e "$1" --arg p "$2" '{e:$e,p:$p}')" | jq -r '.login.identityToken'
}

discover() {
  local secret d
  secret="$(k -n "$namespace" get gateway -o json 2>/dev/null |
    jq -r --arg h "$host" '[.items[].spec.listeners[] | select(.hostname == $h and .tls) | .tls.certificateRefs[]?.name] | .[0] // empty')"
  [[ -n "$secret" ]] || secret="$(k -n "$namespace" get ingress -o json | jq -r '[.items[].spec.tls[]?.secretName] | unique | .[0] // empty')"
  [[ -n "$secret" ]] || fail "nothing in $namespace serves TLS for $host"
  ingress_ca="$rig_tmp/ingress-ca.pem"
  k -n "$namespace" get secret "$secret" -o json | jq -r '.data["ca.crt"] // empty' | base64 -d >"$ingress_ca"
  [[ -s "$ingress_ca" ]] || fail "ingress TLS secret $secret has no ca.crt"
  broker_ca="$work/broker-ca.pem"
  d="$(k -n "$namespace" get secret -o json | jq -r '[.items[] | select(.metadata.name | endswith("nats-tls")) | .data["ca.crt"] // empty][0] // empty')"
  [[ -n "$d" ]] || fail "no *nats-tls secret with a ca.crt in $namespace"
  base64 -d <<<"$d" >"$broker_ca"
  node_ip="$(docker inspect "$cluster-control-plane" -f '{{with index .NetworkSettings.Networks "kind"}}{{.IPAddress}}{{end}}')"
  [[ -n "$node_ip" ]] || fail "cannot read the kind node's address on the kind network"
}

# ---------------------------------------------------------------------------
# Counters: the platform's own metrics, summed over a service's live pods
# ---------------------------------------------------------------------------

# svc_counter <deployment> <metric suffix> prints the sum of every series whose name ends with the suffix.
# A pod whose metrics cannot be read FAILS the rig: reading it as zero would let a check "see" a number
# it never measured.
svc_counter() {
  local dep="$1" metric="$2" sel port pod pods body total=0 n
  sel="$(k -n "$namespace" get deploy "$dep" -o json | jq -r '.spec.selector.matchLabels | to_entries | map("\(.key)=\(.value)") | join(",")')"
  port="$(k -n "$namespace" get deploy "$dep" -o json | jq -r '[.spec.template.spec.containers[0].ports[] | select(.name == "graphql" or .name == "http" or .name == "metrics")][0].containerPort // empty')"
  [[ -n "$port" ]] || fail "$dep exposes no port to read metrics from"
  pods="$(k -n "$namespace" get pods -l "$sel" --field-selector=status.phase=Running -o json | jq -r '.items[] | select(.metadata.deletionTimestamp == null) | .metadata.name')"
  [[ -n "$pods" ]] || fail "no live $dep pod to read metrics from"
  for pod in $pods; do
    body="$(k get --raw "/api/v1/namespaces/$namespace/pods/$pod:$port/proxy/metrics")" || fail "could not read the metrics of $pod"
    n="$(awk -v m="$metric" '/^#/ { next } { i = index($1, "{"); nm = (i ? substr($1, 1, i - 1) : $1); if (substr(nm, length(nm) - length(m) + 1) == m) s += $NF } END { printf "%d", s }' <<<"$body")"
    total=$((total + n))
  done
  echo "$total"
}

# ---------------------------------------------------------------------------
# Provisioning
# ---------------------------------------------------------------------------

member_password=""

# provision_tenant <tenant> [extra createTenant fields as a JSON object]: creates the tenant, a member
# of it, and prints that member's tenant-scoped access token.
provision_tenant() {
  local t="$1" extra="${2:-\{\}}" tier email ident tok
  tier="$(adm '{tenantTiers{token}}' | jq -r '.tenantTiers[0].token')"
  [[ -n "$tier" && "$tier" != null ]] || fail "the instance has no tenant tier"
  adm 'mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}' \
    "$(jq -n --arg t "$t" --arg tier "$tier" --argjson x "$extra" '{r:({token:$t,name:("Conformance "+$t),tierToken:$tier}+$x)}')" >/dev/null ||
    fail "could not create tenant $t"
  email="conformance@$t.test"
  adm 'mutation($e:String!,$p:String!){createIdentity(request:{email:$e,password:$p,enabled:true,systemRoles:[]}){email}}' \
    "$(jq -n --arg e "$email" --arg p "$member_password" '{e:$e,p:$p}')" >/dev/null || fail "could not create the member of $t"
  adm 'mutation($e:String!,$t:String!){addMembership(email:$e,tenant:$t,roleTokens:["tenant-admin"]){email}}' \
    "$(jq -n --arg e "$email" --arg t "$t" '{e:$e,t:$t}')" >/dev/null || fail "could not add the membership for $t"
  ident="$(login "$email" "$member_password")"
  tok="$(gql "$um/graphql" - 'mutation($i:String!,$t:String!){selectTenant(identityToken:$i,tenant:$t){accessToken}}' \
    "$(jq -n --arg i "$ident" --arg t "$t" '{i:$i,t:$t}')" | jq -r '.selectTenant.accessToken')"
  [[ -n "$tok" && "$tok" != null ]] || fail "could not enter tenant $t"
  printf '%s' "$tok"
}

# make_profile_and_type: the device profile (with the command vocabulary the scenarios use) and device type.
make_profile_and_type() {
  local key impact
  dmq 'mutation($r:DeviceProfileCreateRequest){createDeviceProfile(request:$r){token}}' \
    "$(jq -n --arg t "$profile" '{r:{token:$t,name:"Conformance device"}}')" >/dev/null || fail "createDeviceProfile"
  for spec in conf.ping:NORMAL lwm2m.read:NORMAL lwm2m.write:NORMAL lwm2m.execute:NORMAL lwm2m.reboot:DISRUPTIVE lwm2m.readDeviceInfo:NORMAL; do
    key="${spec%%:*}"; impact="${spec##*:}"
    dmq 'mutation($r:CommandDefinitionCreateRequest){createCommandDefinition(request:$r){token}}' \
      "$(jq -n --arg t "cd-${key//./-}" --arg p "$profile" --arg k "$key" --arg i "$impact" '{r:{token:$t,deviceProfileToken:$p,commandKey:$k,name:$k,impact:$i}}')" >/dev/null ||
      fail "createCommandDefinition $key"
  done
  dmq 'mutation($t:String!){publishDeviceProfile(token:$t){version}}' "$(jq -n --arg t "$profile" '{t:$t}')" >/dev/null || fail "publishDeviceProfile"
  dmq 'mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}' \
    "$(jq -n --arg t "$dtype" --arg p "$profile" '{r:{token:$t,name:"Conformance device",profileToken:$p}}')" >/dev/null || fail "createDeviceType"
}

# make_device <token> <MQTT_BASIC|ACCESS_TOKEN> <secret file>: a device with a credential.
make_device() {
  dmq 'mutation($r:DeviceCreateRequest){createDevice(request:$r){token}}' \
    "$(jq -n --arg t "$1" --arg ty "$dtype" '{r:{token:$t,name:$t,deviceTypeToken:$ty}}')" >/dev/null || fail "createDevice $1"
  dmq 'mutation($r:DeviceCredentialCreateRequest){createDeviceCredential(request:$r){token}}' \
    "$(jq -n --arg t "$1-cred" --arg d "$1" --arg ty "$2" --rawfile s "$3" \
      '{r:{token:$t,deviceToken:$d,credentialType:$ty,credentialId:$d,credentialValue:$s,enabled:true}}')" >/dev/null ||
    fail "createDeviceCredential $1"
}

# expose: a NodePort in front of event-sources' plain HTTP port, and one in front of lwm2m-ingest's UDP
# port, so the clients in containers on the kind network reach them. Neither is routed by the chart.
expose() {
  local sel
  sel="$(k -n "$namespace" get deploy event-sources -o json | jq -c '.spec.selector.matchLabels')"
  k -n "$namespace" apply -f - >/dev/null <<EOY
apiVersion: v1
kind: Service
metadata: {name: conformance-http, namespace: $namespace}
spec:
  type: NodePort
  selector: $sel
  ports: [{name: http, port: 8081, targetPort: 8081, nodePort: $http_node_port, protocol: TCP}]
EOY
  sel="$(k -n "$namespace" get deploy lwm2m-ingest -o json | jq -c '.spec.selector.matchLabels')"
  k -n "$namespace" apply -f - >/dev/null <<EOY
apiVersion: v1
kind: Service
metadata: {name: conformance-lwm2m, namespace: $namespace}
spec:
  type: NodePort
  selector: $sel
  ports: [{name: coaps, port: 5684, targetPort: 5684, nodePort: $lwm2m_node_port, protocol: UDP}]
EOY
}

# ---------------------------------------------------------------------------
# Reading the store
# ---------------------------------------------------------------------------

# stored <device token> prints the device's stored measurements as a JSON array of {name,value}.
stored() {
  emq 'query($c:EventSearchCriteria!){measurementEvents(criteria:$c){results{deviceToken name value}}}' \
    "$(jq -n --arg d "$1" '{c:{pageNumber:1,pageSize:200,deviceToken:$d}}')" 2>/dev/null | jq -c '[.measurementEvents.results[]? | {name, value}]'
}
# has_value <device token> <value> [name] prints 1 if a stored measurement of the device (optionally with
# that name) has that value.
has_value() {
  local s
  s="$(stored "$1")" || { echo 0; return; }
  [[ -n "$s" ]] || { echo 0; return; }
  jq -r --argjson v "$2" --arg n "${3:-}" '[.[] | select(($n == "" or .name == $n) and ((.value - $v) | fabs) < 0.0001)] | length | if . > 0 then 1 else 0 end' <<<"$s"
}
wait_value() { # <device> <value> <seconds> [name]
  local deadline=$((SECONDS + $3))
  while ((SECONDS < deadline)); do
    [[ "$(has_value "$1" "$2" "${4:-}")" == 1 ]] && { echo 1; return; }
    sleep 2
  done
  echo 0
}

# device_state <external id> sets D_TOKEN D_ACTIVE D_PRESENCE (empty when the device does not exist).
D_TOKEN="" D_ACTIVE="" D_PRESENCE=""
device_state() {
  local r
  D_TOKEN="" D_ACTIVE="" D_PRESENCE=""
  r="$(dsq 'query($e:[String!]!){deviceStatesByExternalId(externalIds:$e){deviceToken active presenceSource}}' "$(jq -n --arg e "$1" '{e:[$e]}')" 2>/dev/null)" || return 0
  D_TOKEN="$(jq -r '.deviceStatesByExternalId[0].deviceToken // empty' <<<"$r")"
  D_ACTIVE="$(jq -r '.deviceStatesByExternalId[0].active // empty' <<<"$r")"
  D_PRESENCE="$(jq -r '.deviceStatesByExternalId[0].presenceSource // empty' <<<"$r")"
}
wait_active() { # <external id> <seconds>
  local deadline=$((SECONDS + $2))
  while ((SECONDS < deadline)); do
    device_state "$1"
    [[ "$D_ACTIVE" == true ]] && return 0
    sleep 3
  done
  return 1
}

# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------

cmd_seq=0
# issue_command <device> <name> <payload json> <wait seconds> sets R_STATUS R_OUTCOME R_PAYLOAD from the
# command record, once it is terminal or the wait ends.
issue_command() {
  local dev="$1" name="$2" payload="$3" wait="$4" tok exp r deadline
  cmd_seq=$((cmd_seq + 1))
  tok="conf-$(date -u +%H%M%S)-$cmd_seq"
  exp="$(date -u -d "+$((wait + 30)) seconds" +%Y-%m-%dT%H:%M:%SZ)"
  r="$(cdq 'mutation($r:CommandCreateRequest!){createCommand(request:$r){command{token status} rejection{code}}}' \
    "$(jq -n --arg t "$tok" --arg d "$dev" --arg n "$name" --arg p "$payload" --arg e "$exp" '{r:{token:$t,deviceToken:$d,name:$n,payload:$p,expiresAt:$e}}')")" ||
    { R_STATUS="ENQUEUE-ERROR"; R_OUTCOME=""; R_PAYLOAD=""; return 0; }
  if [[ "$(jq -r '.createCommand.rejection.code // empty' <<<"$r")" != "" ]]; then
    R_STATUS="REJECTED:$(jq -r '.createCommand.rejection.code' <<<"$r")"; R_OUTCOME=""; R_PAYLOAD=""; return 0
  fi
  deadline=$((SECONDS + wait))
  while true; do
    r="$(cdq 'query($t:[String!]!){commandsByToken(tokens:$t){status outcome responsePayload error}}' "$(jq -n --arg t "$tok" '{t:[$t]}')")" || r='{}'
    R_STATUS="$(jq -r '.commandsByToken[0].status // empty' <<<"$r")"
    R_OUTCOME="$(jq -r '.commandsByToken[0].outcome // empty' <<<"$r")"
    R_PAYLOAD="$(jq -r '.commandsByToken[0].responsePayload // .commandsByToken[0].error // empty' <<<"$r")"
    case "$R_STATUS" in SUCCESSFUL | FAILED | TIMEOUT | EXPIRED | CANCELLED) return 0 ;; esac
    ((SECONDS < deadline)) || return 0
    sleep 2
  done
}

# ---------------------------------------------------------------------------
# Clients
# ---------------------------------------------------------------------------

client_name_seq=0
# client_bg <image> <args...> starts a client container in the background and prints its name.
client_bg() {
  local image="$1" name
  shift
  client_name_seq=$((client_name_seq + 1))
  name="$cluster-client-$client_name_seq-$$"
  dk run -d --name "$name" --network kind --label "devicechain.io/conformance-rig=$cluster" \
    --add-host "$host:$node_ip" -v "$(winpath "$work"):/work:ro" "$image" "$@" >/dev/null
  printf '%s' "$name"
}
client_logs() { dk logs "$1" 2>/dev/null | grep -a '^DCCONF \|^DCINTEROP ' || true; }
client_wait_event() { # <container> <event> <seconds>
  local deadline=$((SECONDS + $3))
  while ((SECONDS < deadline)); do
    client_logs "$1" | grep -aq "\"event\":\"$2\"" && return 0
    [[ "$(dk inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" == true ]] || { client_logs "$1" | grep -aq "\"event\":\"$2\"" && return 0; return 1; }
    sleep 1
  done
  return 1
}
client_rm() { dk rm -f "$1" >/dev/null 2>&1 || true; }
# all_clients_rm removes THIS rig's client containers only, found by the label this rig put on them.
all_clients_rm() {
  local c
  for c in $(dk ps -aq --filter "label=devicechain.io/conformance-rig=$cluster"); do dk rm -f "$c" >/dev/null 2>&1 || true; done
}

mqtt_payload() { printf '{"device":"%s","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"%s"}}]}}' "$1" "$2"; }

# next_value prints a number no earlier publish used, so the event it names can only be this one.
next_value() {
  local n=0
  [[ ! -f "$work/value-seq" ]] || n="$(<"$work/value-seq")"
  n=$((n + 1))
  printf '%d' "$n" >"$work/value-seq"
  printf '%d.%02d' $((1000 + n)) $((n % 100))
}

# ---------------------------------------------------------------------------
# STEPS
# ---------------------------------------------------------------------------

mq_dev="conf-mqtt"
http_dev="conf-http"
rl_dev="conf-rl"
mq_secret="" http_secret="" rl_secret=""
lw_identity="conf-lwm2m-a"
lw_tlv_identity="conf-lwm2m-tlv"

step_setup() {
  say "SETUP: tenants, profile with a command vocabulary, devices"
  local pw
  pw="$(k -n "$namespace" get secret "dci-$instance-superuser" -o jsonpath='{.data.password}' | base64 -d)"
  [[ -n "$pw" ]] || fail "Secret $namespace/dci-$instance-superuser holds no superuser password"
  admin_token="$(login "superuser@devicechain.local" "$pw")"
  [[ -n "$admin_token" && "$admin_token" != null ]] || fail "could not sign in as the superuser"
  member_password="$(rand_b64 24 | tr -d '/+=')Aa1"

  main_token="$(provision_tenant "$tenant" '{}')"
  [[ -n "$main_token" ]] || fail "no access token for tenant $tenant (it may exist already: run 'down' and 'up' for a clean instance)"
  # A tenant of its own with a tiny ingest allowance, for the 429 check: three readings, refilled so
  # slowly that nothing over the burst is admitted during a run.
  rl_tenant="${tenant}rl"
  rl_token="$(provision_tenant "$rl_tenant" '{"ingestReadingsPerSecond":0.001,"ingestBurst":3}')"
  [[ -n "$rl_token" ]] || fail "no access token for tenant $rl_tenant"

  tenant_token="$main_token"
  make_profile_and_type
  mq_secret="$work/secret-mqtt"; rand_hex 24 >"$mq_secret"
  http_secret="$work/secret-http"; rand_hex 24 >"$http_secret"
  make_device "$mq_dev" MQTT_BASIC "$mq_secret"
  make_device "$http_dev" ACCESS_TOKEN "$http_secret"

  tenant_token="$rl_token"
  make_profile_and_type
  rl_secret="$work/secret-rl"; rand_hex 24 >"$rl_secret"
  make_device "$rl_dev" ACCESS_TOKEN "$rl_secret"
  tenant_token="$main_token"

  expose
  # The Sparkplug source and the LwM2M identities name the tenant and the device type, which did not
  # exist when the services started. A restart makes each re-read what is there now; a pod that
  # started before its tenant existed is the ordinary state of this rig, not a defect under test.
  k -n "$namespace" rollout restart deploy/lwm2m-ingest deploy/sparkplug-ingest >/dev/null
  k -n "$namespace" rollout status deploy/lwm2m-ingest --timeout=300s >/dev/null || fail "lwm2m-ingest did not become ready"
  k -n "$namespace" rollout status deploy/sparkplug-ingest --timeout=300s >/dev/null || fail "sparkplug-ingest did not become ready"
  pass "tenants $tenant and $rl_tenant, profile $profile, devices $mq_dev $http_dev $rl_dev, edge services ready"
}

# --- MQTT (Eclipse Paho) -----------------------------------------------------------------------------

paho_args() { # <client id suffix>
  printf '%s\n' --url "ssl://$node_ip:$mqtt_node_port" --ca /work/broker-ca.pem --client-id "$instance:$tenant:$mq_dev$1" \
    --user "$tenant:$mq_dev" --password "$(cat "$mq_secret")"
}

mqtt_publish_check() { # <qos>
  local qos="$1" v c0 c1 name out rc=0
  v="$(next_value)"
  c0="$(svc_counter event-sources total_inbound_messages)"
  mapfile -t base < <(paho_args "")
  out="$(dk run --rm --network kind --add-host "$host:$node_ip" -v "$(winpath "$work"):/work:ro" "$client_image" \
    --scenario mqtt-publish "${base[@]}" --topic "$instance/$tenant/devices/$mq_dev/events" --qos "$qos" \
    --payload "$(mqtt_payload "$mq_dev" "$v")" 2>/dev/null)" || rc=$?
  reset_record
  R_RC=$rc
  R_STORED="$(wait_value "$mq_dev" "$v" 40)"
  c1="$(svc_counter event-sources total_inbound_messages)"
  R_COUNTER=$((c1 - c0))
  record_check "mqtt-qos$qos-publish" chk_publish "Paho QoS $qos publish of $v: client exit $rc, stored=$R_STORED, event-sources counted +$R_COUNTER"
}

mqtt_command_check() { # <subscribe true|false> -> record the command's fate
  local subscribe="$1" name
  # The PLAIN client id: presence follows the device's plain session only (a ":suffix" connection never
  # asserts it), and the publish connections above have closed, so on a suffixed id the device reads as
  # away and its command is HELD, which is the platform working as documented and not what is under test.
  mapfile -t base < <(paho_args "")
  name="$(client_bg "$client_image" --scenario mqtt-command "${base[@]}" \
    --topic "$instance/$tenant/device-commands/$mq_dev" --respond-topic "$instance/$tenant/command-responses/$mq_dev" \
    --subscribe "$subscribe" --respond true --wait 120)"
  client_wait_event "$name" subscribed 60 || { client_logs "$name" >&2; client_rm "$name"; fail "the Paho command client never became ready"; }
  reset_record
  # A healthy delivery settles in seconds; a command that is not delivered will not, so the window for
  # the planted defect only has to be long enough to be sure.
  if [[ "$subscribe" == true ]]; then issue_command "$mq_dev" conf.ping '{}' 60; else issue_command "$mq_dev" conf.ping '{}' 25; fi
  client_rm "$name"
}

step_mqtt() {
  say "MQTT 3.1.1 (Eclipse Paho) on the platform broker"
  tenant_token="$main_token"
  mqtt_publish_check 0
  mqtt_publish_check 1
  mqtt_command_check true
  record_check mqtt-command-subscription chk_command "Paho subscribed to its command topic; the command settled $R_STATUS/$R_OUTCOME"
  # N1: the planted defect. The same client, connected and authenticated but never subscribed.
  mqtt_command_check false
  record_control N1-dropped-command-subscription mqtt-command-subscription chk_command "no subscription: the command settled as '$R_STATUS'"
}

# --- HTTP (curl) -------------------------------------------------------------------------------------

curl_run() { # runs curl in a container on the kind network, args as for curl
  dk run --rm --network kind "$curl_image" "$@"
}
# http_post <tenant> <device> <secret file> <value>: prints "<code> <retry-after>" (the header may be empty).
http_post() {
  local out
  out="$(curl_run -s -i --max-time 20 -X POST "http://$node_ip:$http_node_port/$instance/$1/events" -H 'Content-Type: application/json' \
    -d "{\"device\":\"$2\",\"eventType\":\"Measurement\",\"credentialType\":\"ACCESS_TOKEN\",\"credentialId\":\"$2\",\"payload\":{\"entries\":[{\"measurements\":{\"temperature\":\"$4\"}}]}}" 2>/dev/null | tr -d '\r')" || true
  printf '%s %s' "$(awk 'NR==1 {print $2}' <<<"$out")" "$(awk 'tolower($1)=="retry-after:" {print $2}' <<<"$out")"
}

step_http_202() {
  local v c0 c1 resp
  tenant_token="$main_token"
  v="$(next_value)"
  c0="$(svc_counter event-sources total_inbound_messages)"
  resp="$(http_post "$tenant" "$http_dev" "$http_secret" "$v")"
  reset_record
  R_CODE="${resp%% *}"; R_RETRY="${resp#* }"
  R_STORED="$(wait_value "$http_dev" "$v" 40)"
  c1="$(svc_counter event-sources total_inbound_messages)"
  R_COUNTER=$((c1 - c0))
  record_check http-202 chk_http_202 "POST of $v answered $R_CODE, stored=$R_STORED, event-sources counted +$R_COUNTER"
}

# ceiling_refusals: the requests event-sources shed at a tenant's ingest ceiling, at either stage. A
# request is metered before it is decoded (total_msg_rate_limited) and its readings after it is
# (total_msg_reading_limited), and either answers 429.
ceiling_refusals() {
  echo $(($(svc_counter event-sources total_msg_rate_limited) + $(svc_counter event-sources total_msg_reading_limited)))
}

step_http_429() {
  local i v resp c0 c1 code="" retry="" refused=""
  tenant_token="$rl_token"
  c0="$(ceiling_refusals)"
  for i in 1 2 3 4 5 6 7 8; do
    v="$(next_value)"
    resp="$(http_post "$rl_tenant" "$rl_dev" "$rl_secret" "$v")"
    code="${resp%% *}"; retry="${resp#* }"
    note "attempt $i: $v -> $code${retry:+ (Retry-After $retry)}"
    if [[ "$code" == 429 ]]; then refused="$v"; break; fi
  done
  [[ -n "$refused" ]] || fail "the rate-limited tenant was never answered 429 in 8 posts"
  sleep 8
  reset_record
  R_CODE="$code"; R_RETRY="$retry"
  R_STORED="$(has_value "$rl_dev" "$refused")"
  c1="$(ceiling_refusals)"
  R_COUNTER=$((c1 - c0))
  record_check http-429 chk_http_429 "over-ceiling POST answered $R_CODE Retry-After $R_RETRY, refused value stored=$R_STORED, refusal counter +$R_COUNTER"
  tenant_token="$main_token"
}

step_http_503() {
  local replicas posts out code="" retry="" refused="" first="" c0 c1 i v
  tenant_token="$main_token"
  c0="$(svc_counter event-sources total_msg_backpressured)"
  replicas="$(k -n "$namespace" get deploy device-management -o jsonpath='{.spec.replicas}')"
  note "holding the platform in backpressure: device-management $replicas -> 0 replicas, ingest stream ceiling $stream_max_msgs messages"
  dm_scaled_from="$replicas"
  k -n "$namespace" scale deploy device-management --replicas 0 >/dev/null
  local deadline=$((SECONDS + 120))
  while [[ -n "$(k -n "$namespace" get pods -l "$(k -n "$namespace" get deploy device-management -o json | jq -r '.spec.selector.matchLabels | to_entries | map("\(.key)=\(.value)") | join(",")')" -o name 2>/dev/null)" ]]; do
    ((SECONDS < deadline)) || fail "device-management pods did not stop"
    sleep 2
  done
  # One container posts until the first 503 (or gives up), so the stream is filled in seconds and not
  # one container start per event.
  first="$(next_value)"
  out="$(curl_run sh -c '
    first="'"$first"'"; i=0
    while [ "$i" -lt '"$((stream_max_msgs * 3))"' ]; do
      v="$(awk -v f="$first" -v i="$i" "BEGIN{printf \"%.2f\", f + i/100}")"
      r="$(curl -s -i --max-time 10 -X POST "http://'"$node_ip:$http_node_port/$instance/$tenant"'/events" -H "Content-Type: application/json" \
        -d "{\"device\":\"'"$http_dev"'\",\"eventType\":\"Measurement\",\"credentialType\":\"ACCESS_TOKEN\",\"credentialId\":\"'"$http_dev"'\",\"payload\":{\"entries\":[{\"measurements\":{\"temperature\":\"$v\"}}]}}" | tr -d "\r")"
      code="$(echo "$r" | head -1 | cut -d" " -f2)"
      if [ "$code" = 503 ]; then echo "REFUSED $v $code $(echo "$r" | awk "tolower(\$1)==\"retry-after:\" {print \$2}")"; exit 0; fi
      i=$((i + 1))
    done
    echo "NEVER-REFUSED $i"
  ' 2>&1)" || true
  note "$(tail -1 <<<"$out")"
  k -n "$namespace" scale deploy device-management --replicas "$replicas" >/dev/null
  dm_scaled_from=""
  k -n "$namespace" rollout status deploy/device-management --timeout=300s >/dev/null || fail "device-management did not come back"
  [[ "$out" == *REFUSED* ]] || fail "the platform never answered 503 in $((stream_max_msgs * 3)) posts: $(tail -2 <<<"$out")"
  read -r _ refused code retry <<<"$(grep -a '^REFUSED ' <<<"$out" | tail -1)"
  # The platform is draining what it accepted; wait for the FIRST accepted event to be stored, which
  # shows the consumer is back, and only then ask whether the refused one ever appears.
  [[ "$(wait_value "$http_dev" "$first" 180)" == 1 ]] || fail "the events accepted before the 503 were never stored; the consumer did not recover"
  sleep 10
  reset_record
  R_CODE="$code"; R_RETRY="$retry"
  R_STORED="$(has_value "$http_dev" "$refused")"
  c1="$(svc_counter event-sources total_msg_backpressured)"
  R_COUNTER=$((c1 - c0))
  record_check http-503-retry-after chk_http_503 "POST answered $R_CODE Retry-After $R_RETRY while backpressured, refused value $refused stored=$R_STORED, refusal counter +$R_COUNTER"
}

# --- LwM2M (Eclipse Leshan) ---------------------------------------------------------------------------

lwm2m_psk() { jq -r --arg i "$1" '.[] | select(.identity == $i) | .psk' "$state_dir/lwm2m-identities.json"; }

step_lwm2m() {
  say "LwM2M (Eclipse Leshan) over DTLS-PSK"
  tenant_token="$main_token"
  local cli tok v
  # Image first: the Leshan client in this tree, so the Execute and Write the scenario answers exist.
  cli="$(client_bg "$leshan_image" --server "$node_ip:$lwm2m_node_port" --identity "$lw_identity" --psk-b64 "$(lwm2m_psk "$lw_identity")" --scenario observe-hold --value 21.5)"
  client_wait_event "$cli" registered 60 || { client_logs "$cli" >&2; fail "the Leshan client did not register"; }

  wait_active "$lw_identity" 90 || true
  device_state "$lw_identity"
  tok="$D_TOKEN"
  reset_record
  R_EXISTS=0; [[ -n "$tok" ]] && R_EXISTS=1
  R_ACTIVE="$D_ACTIVE"; R_PRESENCE="$D_PRESENCE"
  record_check lwm2m-register chk_presence "Leshan registered: device $tok exists, active=$R_ACTIVE, presence $R_PRESENCE"
  [[ -n "$tok" ]] || fail "no device for $lw_identity; nothing further can be judged"

  # Observe: the initial value (21.5) and a value that can only have arrived as a notification (> 100).
  local initial changed
  initial="$(wait_value "$tok" 21.5 90)"
  changed=0
  local deadline=$((SECONDS + 90)) s
  while ((SECONDS < deadline)); do
    s="$(stored "$tok")"
    changed="$(jq -r '[.[] | select(.value > 100)] | length | if . > 0 then 1 else 0 end' <<<"${s:-[]}")"
    [[ "$changed" == 1 ]] && break
    sleep 3
  done
  reset_record; R_INITIAL="$initial"; R_CHANGED="$changed"
  record_check lwm2m-observe chk_observe "stored for $tok: initial 21.5 present=$initial, a notified (changed) value present=$changed; names: $(jq -r '[.[].name] | unique | join(",")' <<<"$(stored "$tok")")"

  # Read
  issue_command "$tok" lwm2m.read '{"path":"/3/0/0"}' 60
  R_WANT=DeviceChain
  record_check lwm2m-read chk_read "lwm2m.read /3/0/0 settled $R_STATUS/$R_OUTCOME with '${R_PAYLOAD:0:60}'"
  # Write, then read back
  issue_command "$tok" lwm2m.write '{"path":"/3/0/15","value":"Europe/Zurich"}' 60
  local ws="$R_STATUS" wo="$R_OUTCOME"
  issue_command "$tok" lwm2m.read '{"path":"/3/0/15"}' 60
  R_READBACK="$R_PAYLOAD"; R_STATUS="$ws"; R_OUTCOME="$wo"; R_WANT="Europe/Zurich"
  record_check lwm2m-write chk_write "lwm2m.write /3/0/15 settled $ws/$wo; a read afterwards returned '$R_READBACK'"
  # Execute, raw and named
  issue_command "$tok" lwm2m.execute '{"path":"/3/0/4"}' 60
  record_check lwm2m-execute chk_command "lwm2m.execute /3/0/4 settled $R_STATUS/$R_OUTCOME"
  issue_command "$tok" lwm2m.reboot '{}' 60
  record_check lwm2m-reboot chk_command "lwm2m.reboot settled $R_STATUS/$R_OUTCOME"
  issue_command "$tok" lwm2m.readDeviceInfo '{}' 60
  record_check lwm2m-device-info-tlv-unread chk_info_tlv_unread "lwm2m.readDeviceInfo settled $R_STATUS with '${R_PAYLOAD:0:120}'"
  # Harvest: recorded at registration, independent of the commands above. By now the registration is
  # minutes old, past the harvest and its one retry; the store is asked until the wait ends, so a late
  # attribute is still seen.
  local attr="" queried=0 a r
  deadline=$((SECONDS + 60))
  while ((SECONDS < deadline)); do
    if r="$(dmq 'query($c:EntityAttributeSearchCriteria!){entityAttributes(criteria:$c){results{attrKey value}}}' \
      "$(jq -n --arg d "$tok" '{c:{pageNumber:1,pageSize:50,entityType:"device",entity:$d,scope:"CLIENT"}}')")"; then
      queried=1
      a="$(jq -r '[.entityAttributes.results[]? | select(.attrKey == "manufacturer")][0].value // empty' <<<"$r")"
      [[ -n "$a" ]] && { attr="$a"; break; }
    fi
    sleep 3
  done
  reset_record; R_ATTR="$attr"; R_QUERIED="$queried"
  record_check lwm2m-harvest-tlv-unread chk_harvest_tlv_unread "client attribute query answered=$queried; manufacturer recorded as '$attr'"
  client_rm "$cli"

  # N3 and the limit check, from one client: it can encode TLV and not SenML.
  local tcli ttok
  tcli="$(client_bg "$leshan_image" --server "$node_ip:$lwm2m_node_port" --identity "$lw_tlv_identity" --psk-b64 "$(lwm2m_psk "$lw_tlv_identity")" --scenario tlv-only --value 21.5)"
  client_wait_event "$tcli" registered 60 || { client_logs "$tcli" >&2; fail "the TLV-only Leshan client did not register"; }
  wait_active "$lw_tlv_identity" 90 || true
  device_state "$lw_tlv_identity"
  ttok="$D_TOKEN"
  # Long enough for an Observe refused at registration to have been followed by every retry the platform
  # makes, and for the client's value to have changed several times.
  sleep 45
  local tstored
  tstored="$(stored "$ttok")"
  reset_record
  R_ACTIVE="$D_ACTIVE"; R_COUNT="$(jq -r 'length' <<<"${tstored:-[]}")"
  record_check lwm2m-tlv-only-no-telemetry chk_tlv_limit "TLV-only client: device $ttok active=$R_ACTIVE, readings stored=$R_COUNT (the limitation holds when 0)"
  reset_record
  R_INITIAL="$(has_value "$ttok" 21.5)"
  R_CHANGED="$(jq -r '[.[]? | select(.value > 100)] | length | if . > 0 then 1 else 0 end' <<<"${tstored:-[]}")"
  record_control N3-lwm2m-tlv-only lwm2m-observe chk_observe "TLV-only client: initial stored=$R_INITIAL, notified value stored=$R_CHANGED"
  client_rm "$tcli"
}

# --- Sparkplug B (Eclipse Tahu) -----------------------------------------------------------------------

sp_node() { # <node> <alias offset> -> starts a Tahu node, waits until it has sent its data
  local node="$1" off="$2" name
  name="$(client_bg "$client_image" --scenario sparkplug-node --url "tcp://$node_ip:$broker_node_port" --client-id "conf-$node" \
    --group conf --node "$node" --alias-offset "$off" --hold true --wait-state 120)"
  client_wait_event "$name" done 150 || { client_logs "$name" >&2; client_rm "$name"; fail "the Tahu node $node did not finish publishing"; }
  printf '%s' "$name"
}

step_sparkplug() {
  say "Sparkplug B (Eclipse Tahu) through an operator-owned broker"
  tenant_token="$main_token"
  local n tok
  n="$(sp_node n1 0)"
  wait_active "conf/n1" 90 || true
  device_state "conf/n1"
  tok="$D_TOKEN"
  reset_record
  R_EXISTS=0; [[ -n "$tok" ]] && R_EXISTS=1
  R_ACTIVE="$D_ACTIVE"; R_PRESENCE="$D_PRESENCE"
  record_check sparkplug-birth chk_presence "Tahu NBIRTH: device ${tok:-none} for conf/n1 exists=$R_EXISTS, active=$R_ACTIVE, presence $R_PRESENCE"
  [[ -n "$tok" ]] || fail "no device for conf/n1"
  # The birth carries 21.5; the data carries 22.5, 23.5, 24.5 by alias, so only the data can supply them.
  reset_record
  R_STORED="$(wait_value "$tok" 24.5 90 temperature)"
  record_check sparkplug-data chk_sp_data "Tahu NDATA by alias: temperature=24.5 stored=$R_STORED; names: $(jq -r '[.[].name] | unique | join(",")' <<<"$(stored "$tok")")"
  client_rm "$n"

  # N2: the same node publishing its data one alias off.
  local n2 tok2
  n2="$(sp_node n2 1)"
  wait_active "conf/n2" 90 || true
  device_state "conf/n2"
  tok2="$D_TOKEN"
  sleep 15
  reset_record
  R_STORED="$(has_value "$tok2" 24.5 temperature)"
  record_control N2-decoder-off-by-one sparkplug-data chk_sp_data "NDATA one alias off: temperature=24.5 stored=$R_STORED (humidity 24.5 stored=$(has_value "$tok2" 24.5 humidity))"
  client_rm "$n2"
}

# ---------------------------------------------------------------------------
# RESULTS
# ---------------------------------------------------------------------------

write_results() {
  local sha dirty digest spec_win
  sha="$(git -C "$repo_root" rev-parse HEAD)"
  # Dirty means any change a build could see: tracked changes, or untracked files that are not ignored
  # and are not the results file this run is about to replace.
  dirty=false
  if [[ -n "$(git -C "$repo_root" status --porcelain --untracked-files=all -- . ':(exclude)hack/conformance/results.json' ':(exclude)docs')" ]]; then
    dirty=true
  fi
  spec_win="$(winpath "$repo_root/hack/conformance/matrix.json")"
  digest="$(cd "$repo_root/backend/tools/transportmatrix" && go run ./cmd/transportmatrix digest -spec "$spec_win")"
  [[ -n "$digest" ]] || fail "could not compute the spec digest"
  {
    printf '%s\n' "${check_rows[@]}" | jq -R -s --arg sha "$sha" --argjson dirty "$dirty" --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg dg "$digest" \
      --arg ctl "$(printf '%s\n' "${control_rows[@]}")" '
      (split("\n") | map(select(length > 0) | split("|"))) as $c
      | ($ctl | split("\n") | map(select(length > 0) | split("|"))) as $k
      | {schema: 1, commit: $sha, dirty: $dirty, generatedAt: $at, specDigest: $dg,
         checks: ($c | map({id: .[0], status: .[1], evidence: (.[2:] | join("|"))}) | sort_by(.id)),
         controls: ($k | map({id: .[0], check: .[1], outcome: .[2], detail: (.[3:] | join("|"))}) | sort_by(.id))}'
  } >"$results_out"
  note "wrote $results_out (commit ${sha:0:12}, dirty=$dirty, ${#check_rows[@]} checks, ${#control_rows[@]} controls)"
  local failed red_missing
  failed="$(printf '%s\n' "${check_rows[@]}" | awk -F'|' '$2 != "pass"' | wc -l | tr -d ' ')"
  red_missing="$(printf '%s\n' "${control_rows[@]}" | awk -F'|' '$3 != "red"' | wc -l | tr -d ' ')"
  [[ "$dirty" == false ]] || warn "the tree is dirty: the results name commit ${sha:0:12} but describe more than it; the renderer will refuse them. Commit, then re-run."
  ((failed == 0)) || fail "$failed check(s) failed"
  ((red_missing == 0)) || fail "$red_missing negative control(s) did not go red"
}

build_clients() {
  say "building the client images (Paho and Tahu; Leshan from this tree)"
  dk build -q -t "$client_image" "$(winpath "$repo_root/hack/conformance/client")" >/dev/null || fail "could not build the Paho/Tahu client image"
  dk build -q -t "$leshan_image" "$(winpath "$repo_root/backend/services/lwm2m-ingest/interop/leshan-client")" >/dev/null || fail "could not build the Leshan client image"
  # Pulled up front: a pull during a check prints "Unable to find image ..." where the status line goes.
  dk pull -q "$curl_image" >/dev/null || fail "could not pull $curl_image"
}

cmd_run() {
  need kind; need kubectl; need docker; need jq; need curl; need openssl; need go
  cluster_exists || fail "kind cluster $cluster does not exist; run 'hack/conformance-rig.sh up'"
  k get ns "$namespace" >/dev/null 2>&1 || fail "instance $instance is not on $cluster; run 'hack/conformance-rig.sh up'"
  local image want_tag
  # The tenant is FIXED: the Sparkplug source and the LwM2M identities were written for it when the
  # instance was bootstrapped, so a run on a used instance fails at tenant creation. Use down, then up.
  say "run: tenant $tenant on instance $instance (cluster $cluster)"
  image="$(k -n "$namespace" get deploy device-management -o jsonpath='{.spec.template.spec.containers[0].image}')"
  note "git HEAD $(git -C "$repo_root" rev-parse HEAD); $(git -C "$repo_root" status --porcelain | wc -l | tr -d ' ') dirty path(s)"
  note "hack/conformance-rig.sh blob $(git -C "$repo_root" hash-object hack/conformance-rig.sh); steps.sh blob $(git -C "$repo_root" hash-object hack/conformance/steps.sh)"
  note "device-management image: $image"
  [[ -f "$state_dir/image-tag" ]] || fail "$state_dir/image-tag is missing: this instance was not brought up by this rig's 'up'"
  want_tag="$(tr -d '\r\n' <"$state_dir/image-tag")"
  [[ "${image##*:}" == "$want_tag" ]] || fail "device-management runs tag '${image##*:}', but 'up' built '$want_tag': this is not the build under test"
  note "the instance was built from commit $(sed -n 's/^cf-\([0-9a-f]*\)-.*/\1/p' <<<"$want_tag"); this run is judged against HEAD $(git -C "$repo_root" rev-parse --short=8 HEAD)"
  # The instance is built from the commit 'up' recorded; the results name HEAD. They describe the same
  # platform only if nothing that is built into the instance differs between the two. The tools, the
  # clients and the docs are not part of it; everything else under backend/ and deploy/ is.
  local built
  built="$(sed -n 's/^cf-\([0-9a-f]*\)-.*/\1/p' <<<"$want_tag")"
  git -C "$repo_root" diff --quiet "$built" HEAD -- backend deploy ':(exclude)backend/tools' ':(exclude)backend/services/lwm2m-ingest/interop' ||
    fail "the instance was built from $built, and HEAD differs from it in code the instance runs: run 'down' and 'up' again, so the results name a tree they tested"
  note "the instance was built from $built; HEAD differs from it only in tooling, clients and docs"
  offline_controls
  # offline_controls filed the record controls; the live ones are added below, and a re-run starts clean.
  all_clients_rm
  build_clients
  discover
  step_setup
  step_mqtt
  step_http_202
  step_http_429
  step_lwm2m
  step_sparkplug
  step_http_503
  all_clients_rm
  write_results
  say "CONFORMANCE GREEN: ${#check_rows[@]} checks passed against third-party clients on a default instance built from this tree, and all ${#control_rows[@]} negative controls (planted defects N1-N3 and the record controls) went red first."
}
