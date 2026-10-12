# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# shellcheck shell=bash
#
# Sourced, not run. A STUB OpenID Connect identity provider (Dex) inside a kind cluster,
# and the two RIG-ONLY edits that let one instance's user-management reach and trust it.
#
# Two rigs need exactly this and must agree on it:
#   hack/sso-rig.sh      signs people in through it (Dex's authproxy connector);
#   hack/upgrade-rig.sh  needs a provider the instance can fetch a discovery document from,
#                        so the upgrade drill can create a single sign-on connection and
#                        cover the tables that hang off one.
#
# 🔴 TRUSTING DEX'S CERTIFICATE -- WHAT PRODUCTION DOES NOT OFFER, SAID PLAINLY.
# Discovery must be https and the provider is reached through the egress guard. Neither
# is configurable per connection, and PRODUCTION HAS NO WAY TO TRUST A PRIVATE CA:
# user-management's HTTP client uses the system roots of its image, and the chart has no
# value to add a CA bundle or mount one. A provider with a private certificate therefore
# cannot be connected today. That is a product gap, reported rather than papered over:
# nothing in the platform is weakened for a rig. Instead sso_stub_trust_and_egress changes
# THE RIG'S OWN cluster: it mounts a ConfigMap holding (the node's system roots + the rig
# CA) and sets SSL_CERT_FILE, which Go's standard library honours on Linux, on the
# user-management Deployment of ONE instance. The next `helm upgrade` of that instance
# reverts it, which is acceptable for a throwaway cluster -- and is why the upgrade drill
# applies it AFTER its upgrade, where the connection is created.
#
# THE EGRESS ALLOWANCE is the same kind of rig-only edit: the stub's ClusterIP, as a /32,
# is written into the instance's config Secret under infrastructure.egress.allowedDestinations.
# The default stays deny.
#
# THE STUB IS AN IDENTITY ORACLE for whoever can reach it (authproxy trusts request
# headers), so it is never exposed: a rig reaches it only through a `kubectl port-forward`
# on 127.0.0.1, and it lives in a throwaway cluster.
#
# The names below are the CALLER'S, so they read as unassigned here.
# shellcheck disable=SC2154
#
# CALLER CONTRACT -- variables and functions the sourcing script must define first:
#   k            kubectl bound to the rig's context
#   say note fail
#   rig_tmp      a scratch directory the CA and certificate are written into
#   cluster      the kind cluster name (the node container is "$cluster-control-plane")
#   namespace    the instance's namespace
#   instance     the instance id (its config Secret is dci-$instance-config)
#   sso_stub_redirect_uri   a redirect URI Dex's static client accepts
# and may set sso_stub_ca_configmap (default sso-rig-ca) to name the CA ConfigMap.

# Pinned by digest like every image a tracked script pulls (hack/check-image-pins.sh).
dex_image="ghcr.io/dexidp/dex:v2.41.1@sha256:bc7cfce7c17f52864e2bb2a4dc1d2f86a41e3019f6d42e81d92a301fad0c8a1d"

dex_ns="dex"
dex_host="dex.$dex_ns.svc.cluster.local"
dex_port=5556
dex_issuer="https://$dex_host:$dex_port/dex"
dex_client="dc-rig"
dex_secret="rig-client-secret-not-a-secret"
sso_stub_ca_configmap="${sso_stub_ca_configmap:-sso-rig-ca}"

# sso_stub_make_pki writes a CA and a server certificate for the Dex Service's DNS name into
# $rig_tmp. The CA never leaves the rig.
sso_stub_make_pki() {
  # MSYS2_ARG_CONV_EXCL: on a Windows git-bash a native openssl is handed "/CN=..." rewritten as a
  # drive path and refuses it; the exclusion is ignored everywhere else. File arguments still convert.
  [[ -s "$rig_tmp/ca.crt" ]] && return 0
  say "minting the rig CA and Dex's certificate"
  MSYS2_ARG_CONV_EXCL="/CN=" openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=dc-sso-rig-ca" \
    -keyout "$rig_tmp/ca.key" -out "$rig_tmp/ca.crt" 2>/dev/null
  MSYS2_ARG_CONV_EXCL="/CN=" openssl req -newkey rsa:2048 -nodes -subj "/CN=$dex_host" \
    -keyout "$rig_tmp/tls.key" -out "$rig_tmp/tls.csr" 2>/dev/null
  printf 'subjectAltName=DNS:%s\n' "$dex_host" >"$rig_tmp/san.ext"
  openssl x509 -req -in "$rig_tmp/tls.csr" -CA "$rig_tmp/ca.crt" -CAkey "$rig_tmp/ca.key" \
    -CAcreateserial -days 2 -extfile "$rig_tmp/san.ext" -out "$rig_tmp/tls.crt" 2>/dev/null
  [[ -s "$rig_tmp/tls.crt" ]] || fail "could not mint Dex's certificate"
}

sso_stub_install_dex() {
  sso_stub_make_pki
  say "installing Dex ($dex_image)"
  k create namespace "$dex_ns" --dry-run=client -o yaml | k apply -f - >/dev/null
  k -n "$dex_ns" create secret tls dex-tls --cert="$rig_tmp/tls.crt" --key="$rig_tmp/tls.key" \
    --dry-run=client -o yaml | k apply -f - >/dev/null
  k apply -f - <<EOF >/dev/null
apiVersion: v1
kind: ConfigMap
metadata:
  name: dex
  namespace: $dex_ns
data:
  config.yaml: |
    issuer: $dex_issuer
    storage:
      type: memory
    web:
      https: 0.0.0.0:$dex_port
      tlsCert: /tls/tls.crt
      tlsKey: /tls/tls.key
    oauth2:
      skipApprovalScreen: true
    staticClients:
      - id: $dex_client
        name: DeviceChain rig
        secret: $dex_secret
        redirectURIs:
          - ${sso_stub_redirect_uri:?the caller sets the stub client redirect URI}
    connectors:
      - type: authproxy
        id: proxy
        name: Rig proxy
        config:
          userHeader: X-Remote-User
          emailHeader: X-Remote-Email
          groupHeader: X-Remote-Group
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dex
  namespace: $dex_ns
spec:
  replicas: 1
  selector:
    matchLabels: {app: dex}
  template:
    metadata:
      labels: {app: dex}
    spec:
      containers:
        - name: dex
          image: $dex_image
          args: ["dex", "serve", "/etc/dex/config.yaml"]
          ports:
            - containerPort: $dex_port
          volumeMounts:
            - {name: config, mountPath: /etc/dex}
            - {name: tls, mountPath: /tls, readOnly: true}
      volumes:
        - name: config
          configMap: {name: dex}
        - name: tls
          secret: {secretName: dex-tls}
---
apiVersion: v1
kind: Service
metadata:
  name: dex
  namespace: $dex_ns
spec:
  selector: {app: dex}
  ports:
    - port: $dex_port
      targetPort: $dex_port
EOF
  k -n "$dex_ns" rollout status deployment/dex --timeout=180s
}

# sso_stub_um_deploy prints THIS instance's user-management Deployment as deployment.apps/<name>.
sso_stub_um_deploy() {
  local deploy
  deploy="$(k -n "$namespace" get deploy -o name)"
  deploy="$(grep 'user-management' <<<"$deploy" || true)"
  [[ "$(wc -l <<<"$deploy" | tr -d ' ')" == 1 && -n "$deploy" ]] ||
    fail "expected exactly one user-management Deployment in $namespace, found: ${deploy:-none}"
  printf '%s\n' "$deploy"
}

# sso_stub_trust_and_egress is the RIG-ONLY edit described at the top of this file.
sso_stub_trust_and_egress() {
  say "letting THIS instance's user-management reach and trust Dex (rig-only edits)"
  local cidr doc new bundle deploy container
  cidr="$(k -n "$dex_ns" get svc dex -o jsonpath='{.spec.clusterIP}')/32"
  [[ "$cidr" != "/32" ]] || fail "Dex's Service has no ClusterIP"
  note "egress allowance: $cidr (instance config Secret dci-$instance-config)"

  doc="$(k -n "$namespace" get secret "dci-$instance-config" -o jsonpath='{.data.instance}' | base64 -d)"
  jq -e . >/dev/null <<<"$doc" ||
    fail "the instance configuration document is not JSON, so this rig cannot edit it safely"
  new="$(jq --arg cidr "$cidr" '
    walk(if type == "object"
         then with_entries(if (.key | ascii_downcase) == "alloweddestinations" then .value = [$cidr] else . end)
         else . end)' <<<"$doc")"
  jq -e --arg cidr "$cidr" '[.. | objects | to_entries[] | select((.key | ascii_downcase) == "alloweddestinations") | .value[]] | index($cidr) != null' \
    >/dev/null <<<"$new" ||
    fail "the instance configuration has no egress allowedDestinations key to set; the document shape changed"
  k -n "$namespace" patch secret "dci-$instance-config" --type merge \
    -p "$(jq -n --arg v "$(printf '%s\n' "$new" | openssl base64 -A)" '{data: {instance: $v}}')" >/dev/null

  # The CA the node trusts, plus the rig CA. SSL_CERT_FILE REPLACES the system roots, so
  # the bundle has to carry them or every other https call of the service would fail.
  bundle="$rig_tmp/bundle.crt"
  MSYS_NO_PATHCONV=1 docker exec "$cluster-control-plane" cat /etc/ssl/certs/ca-certificates.crt >"$bundle"
  cat "$rig_tmp/ca.crt" >>"$bundle"
  k -n "$namespace" create configmap "$sso_stub_ca_configmap" --from-file=ca.crt="$bundle" \
    --dry-run=client -o yaml | k apply -f - >/dev/null

  deploy="$(sso_stub_um_deploy)"
  container="$(k -n "$namespace" get "$deploy" -o jsonpath='{.spec.template.spec.containers[0].name}')"
  # 🔴 THE PATCH MUST ALWAYS ROLL THE PODS, which is what the stamp is for. The egress
  # allowance is in the instance config Secret, which a service reads ONLY at startup (there
  # is no in-place reload), so it takes effect only through a new pod. Without the stamp a
  # second application -- the upgrade drill's, after an upgrade that rewrote the Secret but
  # left this out-of-band patch on the Deployment in place -- would be a no-op patch: no
  # rollout, the running pod keeps a config with no allowance, and the connection is refused
  # for a reason that has nothing to do with the release. A timestamp, not a hash of what was
  # applied: the question is whether the RUNNING pod read it, not whether it changed.
  k -n "$namespace" patch "$deploy" -p "$(jq -n --arg c "$container" --arg cm "$sso_stub_ca_configmap" \
    --arg stamp "$(date -u +%Y%m%dT%H%M%SZ)-$$-$RANDOM" '{spec: {template: {
    metadata: {annotations: {"devicechain.io/sso-stub-applied": $stamp}},
    spec: {
    volumes: [{name: "sso-stub-ca", configMap: {name: $cm}}],
    containers: [{name: $c,
      env: [{name: "SSL_CERT_FILE", value: "/etc/sso-stub-ca/ca.crt"}],
      volumeMounts: [{name: "sso-stub-ca", mountPath: "/etc/sso-stub-ca", readOnly: true}]}]}}}}')" >/dev/null
  k -n "$namespace" rollout status "$deploy" --timeout=240s

  # 🔴 `rollout status` returns when the NEW pods are ready, not when the OLD ones are gone, and an
  # old pod keeps answering for its termination grace period -- through the ingress's kept-alive
  # connections -- with neither the trust nor the egress allowance just applied. A request in that
  # window is refused for a reason a new pod would not give. Wait until only the Deployment's own
  # replica count of pods remains (bounded: 90 polls, 2 s apart).
  local pods="" i want
  want="$(k -n "$namespace" get "$deploy" -o jsonpath='{.spec.replicas}')"
  [[ "$want" =~ ^[1-9][0-9]*$ ]] || fail "could not read the replica count of $deploy (got '$want')"
  for ((i = 0; i < 90; i++)); do
    pods="$(k -n "$namespace" get pods -o name)"
    [[ "$(grep -c user-management <<<"$pods" || true)" == "$want" ]] && break
    sleep 2
  done
  [[ "$(grep -c user-management <<<"$pods" || true)" == "$want" ]] ||
    fail "user-management still has more pods than its $want replica(s) after the rollout, so a request could still reach one without the trust and egress edits: $pods"
}
