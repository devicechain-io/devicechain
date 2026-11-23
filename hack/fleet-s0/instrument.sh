#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# Tier C instrumentation for E-d (WIF with an uploaded JWKS), E-f (the token chain after a refresh cut),
# E-h (crashes in a first cluster create) and E-i (indirect paths from the candidate apply role).
# Sourced by run.sh. Each probe measures, runs a NEGATIVE CONTROL that must be able to fail, and records
# a verdict through result(). A probe whose control did not behave dies with no verdict: INCONCLUSIVE is
# the absence of an entry, never a pass. Verdicts are computed in instrument.py, not here.
inst() { python3 "$S0_HERE/instrument.py" "$@"; }
# emit_verdict ID BINDS-JSON SUBCOMMAND CFGFILE
emit_verdict() {
  local out rc=0 verdict measured
  out="$(inst "$3" "$4")" || rc=$?
  (( rc == 0 )) || die "$1 has no verdict: its control or its measurement was not valid (see above)"
  verdict="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["verdict"])' "$out")"
  measured="$(python3 -c 'import json,sys; print(json.dumps(json.loads(sys.argv[1])["measured"]))' "$out")"
  [[ $verdict == HOLDS ]] || echo "$1 measured: $measured"
  result "$1" "$verdict" "$2" "$measured"
}
# A kind cluster whose apiserver signs tokens under a PINNED issuer (the issuer a pool is told about).
wif_kind() { # node-count
  local cluster="fleet-s0-$$-$RANDOM" workers=$(( $1 - 1 )) i
  S0_ISSUER="https://fleet-s0-$S0_RUN.invalid"
  {
    printf 'kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nkubeadmConfigPatches:\n- |\n  kind: ClusterConfiguration\n  apiServer:\n    extraArgs:\n      service-account-issuer: %s\n' "$S0_ISSUER"
    printf 'nodes:\n  - role: control-plane\n'
    for (( i = 0; i < workers; i++ )); do printf '  - role: worker\n'; done
  } >"$S0_OUT/kind-wif.yaml"
  kind create cluster --name="$cluster" --image="$S0_KIND_NODE_IMAGE" --config="$S0_OUT/kind-wif.yaml" --wait=180s
  S0_KIND_NAME="$cluster"
  [[ -z ${FLEET_S0_KIND_INTERNAL:-} ]] || docker network connect kind "$(hostname)"
  export KUBECONFIG="$S0_OUT/kind.kubeconfig"
  kind get kubeconfig ${FLEET_S0_KIND_INTERNAL:+--internal} --name="$S0_KIND_NAME" >"$KUBECONFIG"
  S0_KUBERNETES_VERSION="$(kubectl version -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)["serverVersion"]["gitVersion"].removeprefix("v"))')"
  export S0_KUBERNETES_VERSION
}
# The JWKS publisher: the cluster's PUBLIC signing keys, checked to carry no private member, uploaded
# with the provider. Nothing is fetched by the cloud, so the issuer needs no reachable discovery host.
wif_publish() {
  kubectl get --raw /openid/v1/jwks >"$S0_OUT/jwks.json"
  S0_JWKS_INFO="$(inst jwks-check "$S0_OUT/jwks.json")"
}
wif_pool() {
  local number
  S0_WIF_POOL="s0-wif-$S0_RUN"
  S0_WIF_AUD="fleet-s0-$S0_RUN"
  number="$(gcloud projects describe "$FLEET_S0_PROJECT" --format='value(projectNumber)')"
  [[ $number =~ ^[0-9]+$ ]] || die "missing project number"
  S0_WIF_NUMBER="$number"
  gcloud iam workload-identity-pools create "$S0_WIF_POOL" --project="$FLEET_S0_PROJECT" --location=global --quiet
  gcloud iam workload-identity-pools providers create-oidc s0-kind --workload-identity-pool="$S0_WIF_POOL" \
    --project="$FLEET_S0_PROJECT" --location=global --issuer-uri="$S0_ISSUER" --jwk-json-path="$S0_OUT/jwks.json" \
    --allowed-audiences="$S0_WIF_AUD" --attribute-mapping=google.subject=assertion.sub --quiet
  S0_WIF_MEMBER="principal://iam.googleapis.com/projects/$number/locations/global/workloadIdentityPools/$S0_WIF_POOL/subject/system:serviceaccount:default:s0-bound"
  gcloud iam service-accounts add-iam-policy-binding "$FLEET_S0_TEST_IDENTITY" --project="$FLEET_S0_PROJECT" \
    --role=roles/iam.workloadIdentityUser --member="$S0_WIF_MEMBER" --condition=None --quiet >/dev/null
}
wif_pool_cleanup() {
  gcloud iam service-accounts remove-iam-policy-binding "$FLEET_S0_TEST_IDENTITY" --project="$FLEET_S0_PROJECT" \
    --role=roles/iam.workloadIdentityUser --member="$S0_WIF_MEMBER" --condition=None --quiet >/dev/null
  gcloud iam workload-identity-pools delete "$S0_WIF_POOL" --project="$FLEET_S0_PROJECT" --location=global --quiet
}
# Prints the HTTP status of the first stage that refused, or 200 when the whole chain accepted the token.
wif_chain() { # subject-token
  local code
  code="$(curl --silent --show-error --output "$S0_OUT/sts.json" --write-out '%{http_code}' --request POST https://sts.googleapis.com/v1/token \
    --data-urlencode grant_type=urn:ietf:params:oauth:grant-type:token-exchange \
    --data-urlencode "audience=//iam.googleapis.com/projects/$S0_WIF_NUMBER/locations/global/workloadIdentityPools/$S0_WIF_POOL/providers/s0-kind" \
    --data-urlencode scope=https://www.googleapis.com/auth/cloud-platform \
    --data-urlencode requested_token_type=urn:ietf:params:oauth:token-type:access_token \
    --data-urlencode subject_token_type=urn:ietf:params:oauth:token-type:jwt \
    --data-urlencode "subject_token=$1")" || die "STS transport failure"
  if [[ $code != 200 ]]; then echo "$code"; return 0; fi
  python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["access_token"])' "$S0_OUT/sts.json" >"$S0_OUT/federated"
  code="$(curl --silent --show-error --output "$S0_OUT/impersonation.json" --write-out '%{http_code}' --request POST \
    --header "Authorization: Bearer $(cat "$S0_OUT/federated")" --header 'Content-Type: application/json' \
    --data '{"scope":["https://www.googleapis.com/auth/cloud-platform"],"lifetime":"600s"}' \
    "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/$FLEET_S0_TEST_IDENTITY:generateAccessToken")" || die "IAM Credentials transport failure"
  rm -f "$S0_OUT/federated"
  echo "$code"
}
wif_workloads() {
  cat >"$S0_OUT/wif.yaml" <<YAML
apiVersion: v1
kind: ServiceAccount
metadata: {name: s0-bound}
---
apiVersion: v1
kind: ServiceAccount
metadata: {name: s0-other}
---
apiVersion: v1
kind: Pod
metadata: {name: s0-wif-bound}
spec:
  serviceAccountName: s0-bound
  restartPolicy: Never
  containers:
    - name: c
      image: busybox:1.37.0
      command: [sleep, "86400"]
      volumeMounts: [{name: wif, mountPath: /var/run/wif}]
  volumes:
    - name: wif
      projected:
        sources:
          - serviceAccountToken: {audience: $S0_WIF_AUD, expirationSeconds: 600, path: good}
          - serviceAccountToken: {audience: wrong-$S0_WIF_AUD, expirationSeconds: 600, path: wrongaud}
---
apiVersion: v1
kind: Pod
metadata: {name: s0-wif-other}
spec:
  serviceAccountName: s0-other
  restartPolicy: Never
  containers:
    - name: c
      image: busybox:1.37.0
      command: [sleep, "86400"]
      volumeMounts: [{name: wif, mountPath: /var/run/wif}]
  volumes:
    - name: wif
      projected:
        sources:
          - serviceAccountToken: {audience: $S0_WIF_AUD, expirationSeconds: 600, path: good}
YAML
  kubectl apply -f "$S0_OUT/wif.yaml"
  kubectl wait pod/s0-wif-bound pod/s0-wif-other --for=condition=Ready --timeout=180s
}
wif_read() { kubectl exec "$1" -- cat "/var/run/wif/$2"; }
probe_d() {
  require_preflight
  local good forged baseline c_other c_aud c_iss recreated
  wif_kind 1
  wif_publish
  wif_pool
  wif_workloads
  good="$(wif_read s0-wif-bound good)"
  # The workloadIdentityUser binding was added seconds ago and IAM takes minutes to propagate it: wait for
  # the baseline to be accepted (or for the deadline) BEFORE the controls, so a refusal means the control.
  local deadline=$((SECONDS + ${FLEET_S0_IAM_PROPAGATION_SECONDS:-420}))
  while baseline="$(wif_chain "$good")"; [[ $baseline != 200 ]] && (( SECONDS < deadline )); do
    sleep "${FLEET_S0_POLL_SECONDS:-15}"
  done
  c_other="$(wif_chain "$(wif_read s0-wif-other good)")"
  c_aud="$(wif_chain "$(wif_read s0-wif-bound wrongaud)")"
  forged="$(inst jwt-forge "https://forged-$S0_RUN.invalid" "$S0_WIF_AUD" system:serviceaccount:default:s0-bound)"
  c_iss="$(wif_chain "$forged")"
  # A deleted and recreated same-name KSA: recorded, never a verdict. A subject carries a name, not a uid.
  kubectl delete pod s0-wif-bound --wait=true
  kubectl delete serviceaccount s0-bound --wait=true
  wif_workloads
  recreated="$(wif_chain "$(wif_read s0-wif-bound good)")"
  python3 - "$S0_OUT/e-d-cfg.json" "$S0_JWKS_INFO" "$baseline" "$c_other" "$c_aud" "$c_iss" "$recreated" <<'PY'
import json,sys
o,jwks,base,other,aud,iss,rec=sys.argv[1:]
json.dump(dict(jwks=json.loads(jwks),baseline=int(base),recreated=int(rec),
  controls=dict(other_subject=int(other),wrong_audience=int(aud),wrong_issuer=int(iss))),open(o,"w"))
PY
  wif_pool_cleanup
  emit_verdict E-d '["cloud","kubernetes"]' jwks-verdict "$S0_OUT/e-d-cfg.json"
}
# Cut every packet in BOTH directions between a node and the control plane, on both address families.
s0_partition_node() { # node control-plane-container
  local ip ip6
  ip="$(docker inspect --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$2")"
  ip6="$(docker inspect --format='{{range .NetworkSettings.Networks}}{{.GlobalIPv6Address}}{{end}}' "$2")"
  [[ $ip =~ ^[0-9.]+$ ]] || die "missing control-plane IP"
  docker exec "$1" iptables -I OUTPUT -d "$ip" -j DROP
  docker exec "$1" iptables -I INPUT -s "$ip" -j DROP
  if [[ -n $ip6 ]]; then
    docker exec "$1" ip6tables -I OUTPUT -d "$ip6" -j DROP
    docker exec "$1" ip6tables -I INPUT -s "$ip6" -j DROP
  fi
}
# The runner: refreshes the whole chain only when its access token has EXPIRED, using whatever token file
# the kubelet last wrote, and calls the cloud every 5 seconds. Its log is the evidence.
chain_runner_yaml() { # name node
  cat <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: $1
  finalizers: [fleet-s0.devicechain.io/observe]
spec:
  nodeName: $2
  serviceAccountName: s0-bound
  restartPolicy: Never
  containers:
    - name: runner
      image: curlimages/curl:8.11.1
      command:
        - sh
        - -c
        - |
          exp=0; acc=
          while :; do
            now=\$(date +%s)
            if [ "\$now" -ge "\$exp" ]; then
              r=\$(curl -s --max-time 20 https://sts.googleapis.com/v1/token \\
                --data-urlencode grant_type=urn:ietf:params:oauth:grant-type:token-exchange \\
                --data-urlencode "audience=//iam.googleapis.com/projects/$S0_WIF_NUMBER/locations/global/workloadIdentityPools/$S0_WIF_POOL/providers/s0-kind" \\
                --data-urlencode scope=https://www.googleapis.com/auth/cloud-platform \\
                --data-urlencode requested_token_type=urn:ietf:params:oauth:token-type:access_token \\
                --data-urlencode subject_token_type=urn:ietf:params:oauth:token-type:jwt \\
                --data-urlencode "subject_token=\$(cat /var/run/wif/good)")
              fed=\$(echo "\$r" | sed -n 's/.*"access_token": *"\\([^"]*\\)".*/\\1/p')
              sttl=\$(echo "\$r" | sed -n 's/.*"expires_in": *\\([0-9]*\\).*/\\1/p')
              if [ -n "\$fed" ]; then
                r=\$(curl -s --max-time 20 -H "Authorization: Bearer \$fed" -H 'Content-Type: application/json' \\
                  --data '{"scope":["https://www.googleapis.com/auth/cloud-platform"],"lifetime":"${S0_ACCESS_TTL}s"}' \\
                  https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/$FLEET_S0_TEST_IDENTITY:generateAccessToken)
                a=\$(echo "\$r" | sed -n 's/.*"accessToken": *"\\([^"]*\\)".*/\\1/p')
                if [ -n "\$a" ]; then
                  acc=\$a; exp=\$((now + ${S0_ACCESS_TTL}))
                  echo "L sts \$sttl" >>/tmp/chain.log; echo "L access ${S0_ACCESS_TTL}" >>/tmp/chain.log
                fi
              fi
            fi
            code=\$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 -H "Authorization: Bearer \$acc" \\
              -H 'Content-Type: application/json' --data '{"permissions":["resourcemanager.projects.get"]}' \\
              https://cloudresourcemanager.googleapis.com/v1/projects/$FLEET_S0_PROJECT:testIamPermissions)
            echo "\$now \$code" >>/tmp/chain.log
            sleep 5
          done
      volumeMounts: [{name: wif, mountPath: /var/run/wif}]
  volumes:
    - name: wif
      projected:
        sources:
          - serviceAccountToken: {audience: $S0_WIF_AUD, expirationSeconds: $S0_KSA_TTL, path: good}
YAML
}
chain_log() { # node pod-uid
  local cid
  cid="$(docker exec "$1" crictl ps -o json | python3 -c 'import json,sys; p=[x["id"] for x in json.load(sys.stdin)["containers"] if x.get("labels",{}).get("io.kubernetes.pod.uid")==sys.argv[1]]; assert len(p)==1; print(p[0])' "$2")"
  docker exec "$1" crictl exec "$cid" cat /tmp/chain.log | tr -d '\r'
}
chain_authorized() { local l; l="$(chain_log "$1" "$2")" || return 1; grep -Eq '^[0-9]+ 200$' <<<"$l"; }
probe_f() {
  require_preflight
  S0_KSA_TTL="${FLEET_S0_KSA_TTL_S:-600}"
  S0_ACCESS_TTL="${FLEET_S0_ACCESS_TTL_S:-600}"
  [[ $S0_KSA_TTL =~ ^[0-9]+$ && $S0_KSA_TTL -ge 600 ]] || die "a projected token lives at least 600 seconds"
  [[ $S0_ACCESS_TTL =~ ^[0-9]+$ && $S0_ACCESS_TTL -le 3600 ]] || die "an impersonated access token lives at most 3600 seconds"
  local cut_node="" ctl_node="" cut_uid ctl_uid partition_at cut_at bound skew="${FLEET_S0_SKEW_SECONDS:-60}" deadline
  wif_kind 3
  wif_publish
  wif_pool
  kubectl create serviceaccount s0-bound
  cut_node="$S0_KIND_NAME-worker"
  ctl_node="$S0_KIND_NAME-worker2"
  chain_runner_yaml s0-runner-cut "$cut_node" >"$S0_OUT/runner-cut.yaml"
  chain_runner_yaml s0-runner-ctl "$ctl_node" >"$S0_OUT/runner-ctl.yaml"
  kubectl apply -f "$S0_OUT/runner-cut.yaml" -f "$S0_OUT/runner-ctl.yaml"
  kubectl wait pod/s0-runner-cut pod/s0-runner-ctl --for=condition=Ready --timeout=180s
  cut_uid="$(kubectl get pod s0-runner-cut -o jsonpath='{.metadata.uid}')"
  ctl_uid="$(kubectl get pod s0-runner-ctl -o jsonpath='{.metadata.uid}')"
  # Both runners must be authorized before anything is cut: a runner that never worked cannot be refused.
  deadline=$((SECONDS + 300))
  until chain_authorized "$cut_node" "$cut_uid" && chain_authorized "$ctl_node" "$ctl_uid"; do
    (( SECONDS < deadline )) || die "a runner was never authorized before the cut"
    sleep "${FLEET_S0_POLL_SECONDS:-5}"
  done
  partition_at="$(date +%s)"
  s0_partition_node "$cut_node" "$S0_KIND_NAME-control-plane"
  sleep "${FLEET_S0_PARTITION_SETTLE_SECONDS:-5}"
  kubectl delete node "$cut_node"
  # The process must SURVIVE the cut (E-l): the force delete is what the manager would record as the cut.
  kubectl delete pod s0-runner-cut --force --grace-period=0 --wait=false --ignore-not-found
  cut_at="$(date +%s)"
  bound=$(( S0_KSA_TTL + 3600 + S0_ACCESS_TTL + skew ))
  deadline=$((SECONDS + bound + 1800))
  # Read until the partitioned runner is refused, or has been observed past the bound, AND the uncut control
  # has been observed authorized at or after the bound.
  while :; do
    chain_log "$cut_node" "$cut_uid" >"$S0_OUT/chain-cut.log"
    chain_log "$ctl_node" "$ctl_uid" >"$S0_OUT/chain-ctl.log"
    python3 - "$S0_OUT/chain-cut.log" "$S0_OUT/chain-ctl.log" "$cut_at" "$bound" <<'PY' && break
import sys
cut,bound=int(sys.argv[3]),int(sys.argv[4])
def series(p):
    out=[]
    for l in open(p):
        f=l.split()
        if len(f)==2 and f[0].isdigit() and f[1].isdigit(): out.append((int(f[0]),int(f[1])))
    return out
a,b=series(sys.argv[1]),series(sys.argv[2])
decided=any(t>=cut and c in (401,403) for t,c in a) or max((t for t,_ in a),default=0)>=cut+bound
# a control observed past the bound but not authorized ends the wait: the verdict function then refuses to judge
sys.exit(0 if decided and any(t>=cut+bound for t,c in b) else 1)
PY
    (( SECONDS < deadline )) || die "the bound plus the observation margin elapsed without a decision"
    sleep "${FLEET_S0_POLL_SECONDS:-30}"
  done
  python3 - "$S0_OUT/e-f-cfg.json" "$S0_OUT/chain-cut.log" "$S0_OUT/chain-ctl.log" "$cut_at" "$partition_at" "$S0_KSA_TTL" "$skew" <<'PY'
import json,sys
o,cut_log,ctl_log,cut,part,ksa,skew=sys.argv[1:]
json.dump(dict(cut=int(cut),partition_at=int(part),ksa_ttl_s=int(ksa),skew_s=int(skew),
  partitioned_log=open(cut_log).read(),control_log=open(ctl_log).read()),open(o,"w"))
PY
  wif_pool_cleanup
  emit_verdict E-f '["cloud","kubernetes"]' chain-verdict "$S0_OUT/e-f-cfg.json"
}
# ---- E-h: kill a first create at five points, then see what a retry does -----------------------------------
h_root() { # dir
  mkdir -p "$1"
  cat >"$1/main.tf" <<HCL
terraform {
  required_version = "= 1.10.10"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "= $FLEET_S0_PROVIDER_VERSION"
    }
  }
}
provider "google" {
  project = var.project
}
variable "project" {
  type = string
}
variable "location" {
  type = string
}
variable "names" {
  type = set(string)
}
variable "protect" {
  type    = bool
  default = false
}
variable "create_timeout" {
  type    = string
  default = "40m"
}
resource "google_container_cluster" "c" {
  for_each                 = var.names
  name                     = each.value
  location                 = var.location
  remove_default_node_pool = true
  initial_node_count       = 1
  deletion_protection      = var.protect
  resource_labels          = { fleet-s0 = "e-h" }
  node_config {
    machine_type = "e2-standard-2"
    disk_size_gb = 20
  }
  timeouts {
    create = var.create_timeout
  }
}
resource "google_container_node_pool" "p1" {
  for_each   = var.names
  name       = "p1"
  cluster    = google_container_cluster.c[each.key].name
  location   = var.location
  node_count = 1
  node_config {
    machine_type = "e2-standard-2"
    disk_size_gb = 20
  }
}
resource "google_container_node_pool" "p2" {
  for_each   = var.names
  name       = "p2"
  cluster    = google_container_cluster.c[each.key].name
  location   = var.location
  node_count = 1
  depends_on = [google_container_node_pool.p1]
  node_config {
    machine_type = "e2-standard-2"
    disk_size_gb = 20
  }
}
HCL
}
h_vars() { H_VARS=(-var="project=$FLEET_S0_PROJECT" -var="location=$FLEET_S0_LOCATION"); }
# The provider takes a STATIC user access token, and the probe outlives one (a token gcloud hands back
# may already be half spent): mint it for every tofu invocation, or a later point dies on a 401.
h_token() { export GOOGLE_OAUTH_ACCESS_TOKEN; GOOGLE_OAUTH_ACCESS_TOKEN="$(gcloud auth print-access-token)"; }
h_tofu() { # dir action args...  (local state, no lock: the writer is killed, so a lock file would be stale)
  local d="$1" action="$2"
  shift 2
  h_vars
  h_token
  case "$action" in
    init) tofu -chdir="$d" init -input=false -lock=false "$@" ;;
    # import takes ADDR ID as its LAST two arguments: every flag, the variables included, must precede them.
    import) tofu -chdir="$d" import -input=false -lock=false "${H_VARS[@]}" "$@" ;;
    *) tofu -chdir="$d" "$action" -input=false -lock=false "$@" "${H_VARS[@]}" ;;
  esac
}
# A grep pipeline under pipefail can report failure when its reader exits first; these cannot.
log_has() { awk -v a="$2" -v b="$3" 'index($0,a) && index($0,b) {f=1} END {exit !f}' "$1"; }
state_has_cluster() { local s; s="$(tofu -chdir="$1" state list 2>/dev/null || true)"; [[ $s == *google_container_cluster.c* ]]; }
h_count() { gcloud container clusters list --project="$FLEET_S0_PROJECT" --location="$FLEET_S0_LOCATION" --filter="name=$1" --format='value(name)' | grep -c . || true; }
h_op_seen() { # cluster operationType
  [[ -n $(gcloud container operations list --project="$FLEET_S0_PROJECT" --location="$FLEET_S0_LOCATION" \
    --filter="operationType=$2 AND targetLink~$1" --format='value(name)') ]]
}
h_await() { # point cluster logfile pid
  local deadline=$((SECONDS + 1800))
  until
    case "$1" in
      1) h_op_seen "$2" CREATE_CLUSTER ;;
      2) h_op_seen "$2" DELETE_NODE_POOL ;;
      3) log_has "$3" apply_complete google_container_cluster.c ;;
      4) log_has "$3" apply_complete google_container_node_pool.p1 ;;
      5) log_has "$3" apply_complete google_container_node_pool.p2 ;;
    esac
  do
    kill -0 "$4" 2>/dev/null || die "point $1: the writer exited before its kill point"
    (( SECONDS < deadline )) || die "point $1: kill point never reached"
    sleep "${FLEET_S0_POLL_SECONDS:-2}"
  done
}
h_wait_settled() { # cluster: no operation in flight, so the delete is not refused
  local deadline=$((SECONDS + 1800))
  while [[ -n $(gcloud container operations list --project="$FLEET_S0_PROJECT" --location="$FLEET_S0_LOCATION" \
      --filter="targetLink~$1 AND status!=DONE" --format='value(name)') ]]; do
    (( SECONDS < deadline )) || die "cluster $1 has an operation that never finished"
    sleep "${FLEET_S0_POLL_SECONDS:-10}"
  done
}
h_point() { # N -> writes $S0_OUT/h/pN/record.json
  local n="$1" name="s0-e-h-$1" d="$S0_OUT/h/p$1" pid killed=false in_state=false plan_ok=false plan_create=false dup=false imp=false id=""
  h_root "$d"
  h_tofu "$d" init -no-color >/dev/null
  h_vars
  h_token
  tofu -chdir="$d" apply -auto-approve -input=false -lock=false -json -var="names=[\"$name\"]" "${H_VARS[@]}" >"$d/apply.jsonl" 2>"$d/apply.err" &
  pid=$!
  h_await "$n" "$name" "$d/apply.jsonl" "$pid"
  # Kill the writer where it stands. The cloud operation it started is NOT killed: that is the case.
  if kill -0 "$pid" 2>/dev/null; then killed=true; fi
  pkill -KILL -P "$pid" 2>/dev/null || true
  kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  [[ $killed == true ]] || die "point $n: the writer finished before it was killed"
  ! state_has_cluster "$d" || in_state=true
  local prc=0
  h_tofu "$d" plan -json -detailed-exitcode -var="names=[\"$name\"]" >"$d/plan.jsonl" 2>"$d/plan.err" || prc=$?
  (( prc == 0 || prc == 2 )) && plan_ok=true
  ! log_has "$d/plan.jsonl" planned_change '"action":"create"' || plan_create=true
  h_wait_settled "$name"
  local arc=0
  h_tofu "$d" apply -auto-approve -json -var="names=[\"$name\"]" >"$d/retry.jsonl" 2>"$d/retry.err" || arc=$?
  if (( arc != 0 )) && grep -Eiq 'alreadyExists|already exists|Error 409' "$d/retry.jsonl" "$d/retry.err"; then dup=true; fi
  id="$(gcloud container clusters describe "$name" --project="$FLEET_S0_PROJECT" --location="$FLEET_S0_LOCATION" --format='value(id)' 2>/dev/null || true)"
  # A verified import into a COPY of the state: what recovery would do, without disturbing the evidence.
  mkdir -p "$d-import" && cp "$d/main.tf" "$d-import/" && { [[ ! -f $d/terraform.tfstate ]] || cp "$d/terraform.tfstate" "$d-import/"; }
  h_tofu "$d-import" init -no-color >/dev/null
  if ! state_has_cluster "$d-import"; then
    h_tofu "$d-import" import -var="names=[\"$name\"]" "google_container_cluster.c[\"$name\"]" "projects/$FLEET_S0_PROJECT/locations/$FLEET_S0_LOCATION/clusters/$name" >"$d/import.log" 2>&1 && imp=true
  else
    imp=true
  fi
  python3 - "$d/record.json" "$n" "$in_state" "$plan_ok" "$plan_create" "$dup" "$(h_count "$name")" "$imp" "$id" <<'PY'
import json,sys
o,n,st,pl,pc,dup,cnt,imp,cid=sys.argv[1:]
b=lambda s: s=="true"
json.dump(dict(point=int(n),killed_mid_apply=True,cluster_in_state=b(st),plan_succeeded=b(pl),plan_proposes_create=b(pc),
  apply_failed_already_exists=b(dup),clusters_with_name=int(cnt),import_succeeded=b(imp),cluster_api_id=cid),open(o,"w"))
PY
  cluster_gc delete "$name"
}
h_failed_create() {
  local name=s0-e-h-fc d="$S0_OUT/h/pfc" rc=0 deleted=false present=false
  h_root "$d"
  h_tofu "$d" init -no-color >/dev/null
  # A create timeout the cloud has not finished: the provider stops waiting. Does IT then delete the cluster?
  h_tofu "$d" apply -auto-approve -json -var="names=[\"$name\"]" -var=create_timeout=30s >"$d/apply.jsonl" 2>"$d/apply.err" || rc=$?
  (( rc != 0 )) || die "the 30 second create timeout did not fail the apply: no failed create to observe"
  sleep "${FLEET_S0_FAILED_CREATE_SETTLE_SECONDS:-120}"
  h_op_seen "$name" DELETE_CLUSTER && deleted=true
  (( $(h_count "$name") > 0 )) && present=true
  local tainted=false
  local shown
  shown="$(tofu -chdir="$d" state show "google_container_cluster.c[\"$name\"]" 2>/dev/null || true)"
  ! grep -qi tainted <<<"$shown" || tainted=true
  python3 - "$d/record.json" "$deleted" "$present" "$tainted" <<'PY'
import json,sys
o,d,p,t=sys.argv[1:]
b=lambda s: s=="true"
json.dump(dict(provider_issued_delete_after_failed_create=b(d),cluster_present_after=b(p),resource_tainted_in_state=b(t)),open(o,"w"))
PY
  if [[ $present == true ]]; then h_wait_settled "$name"; cluster_gc delete "$name"; fi
}
h_protection() {
  local name=s0-e-h-dp d="$S0_OUT/h/pdp" refused=false control=false rc=0
  h_root "$d"
  h_tofu "$d" init -no-color >/dev/null
  h_tofu "$d" apply -auto-approve -var="names=[\"$name\"]" -var=protect=true >"$d/create.log" 2>&1
  # An ORPHAN: the address leaves the configuration, so the plan is a delete of a protected cluster.
  h_tofu "$d" apply -auto-approve -var='names=[]' -var=protect=true >"$d/orphan.log" 2>&1 || rc=$?
  if (( rc != 0 )) && grep -qi 'deletion_protection' "$d/orphan.log" && (( $(h_count "$name") == 1 )); then refused=true; fi
  # Control: the same orphan destroy with protection OFF in state must actually delete the cluster.
  h_tofu "$d" apply -auto-approve -var="names=[\"$name\"]" -var=protect=false >"$d/unprotect.log" 2>&1
  h_tofu "$d" apply -auto-approve -var='names=[]' -var=protect=false >"$d/control.log" 2>&1 || true
  (( $(h_count "$name") == 0 )) && control=true
  python3 - "$d/record.json" "$refused" "$control" <<'PY'
import json,sys
o,r,c=sys.argv[1:]
json.dump(dict(refused_with_protection=r=="true",control_deleted=c=="true"),open(o,"w"))
PY
  if [[ $control != true ]]; then cluster_gc update "$name" --no-deletion-protection 2>/dev/null || true; cluster_gc delete "$name" || true; fi
}
probe_h() {
  require_preflight
  required FLEET_S0_PROVIDER_VERSION
  h_token
  mkdir -p "$S0_OUT/h"
  local n id1 id2 d
  for n in 1 2 3 4 5; do h_point "$n"; done
  h_failed_create
  h_protection
  # Is the API id stable? Recreate point 1's name from scratch and compare.
  d="$S0_OUT/h/p1b"
  h_root "$d"
  h_tofu "$d" init -no-color >/dev/null
  h_tofu "$d" apply -auto-approve -var='names=["s0-e-h-1"]' >"$d/apply.log" 2>&1
  id2="$(gcloud container clusters describe s0-e-h-1 --project="$FLEET_S0_PROJECT" --location="$FLEET_S0_LOCATION" --format='value(id)')"
  id1="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["cluster_api_id"])' "$S0_OUT/h/p1/record.json")"
  cluster_gc delete s0-e-h-1
  python3 - "$S0_OUT" "$id1" "$id2" <<'PY'
import json,pathlib,sys
d=pathlib.Path(sys.argv[1])/"h"
rec=lambda n: json.loads((d/n/"record.json").read_text())
json.dump(dict(points=[rec("p%d"%i) for i in range(1,6)],failed_create=rec("pfc"),deletion_protection=rec("pdp"),
  id_stability=dict(first_id=sys.argv[2],recreated_id=sys.argv[3],differs_after_recreate=sys.argv[2]!=sys.argv[3])),
  open(d/"e-h-cfg.json","w"))
PY
  emit_verdict E-h '["cloud","tofu"]' crash-verdict "$S0_OUT/h/e-h-cfg.json"
}
# ---- E-i: what the candidate apply role can reach ------------------------------------------------------------
# Each attempt runs twice: as the apply identity (subject) and as the invoking operator (control). The control
# must SUCCEED, or the attempt was never well formed and a refusal says nothing.
i_perm() { # token permission -> exit 0 when held, exit 1 with PERMISSION_DENIED when not
  local code
  code="$(curl --silent --output "$S0_OUT/i-perm.json" --write-out '%{http_code}' --header "Authorization: Bearer $1" \
    --header 'Content-Type: application/json' --data "{\"permissions\":[\"$2\"]}" \
    "https://cloudresourcemanager.googleapis.com/v1/projects/$FLEET_S0_PROJECT:testIamPermissions")" || return 2
  [[ $code == 200 ]] || { echo "HTTP $code"; return 2; }
  if python3 -c 'import json,sys; sys.exit(0 if sys.argv[2] in json.load(open(sys.argv[1])).get("permissions",[]) else 1)' "$S0_OUT/i-perm.json" "$2"; then return 0; fi
  echo "PERMISSION_DENIED: the identity does not hold $2"
  return 1
}
i_run() { # who(subject|control) attempt -> prints "<exit>" on line 1 then the output
  local who="$1" id="$2" imp=() out rc=0 tok kc="$S0_OUT/kc-$1.json"
  [[ $who == subject ]] && imp=(--impersonate-service-account="$FLEET_S0_APPLY_IDENTITY")
  case "$id" in
    k8s_secrets)
      if [[ $who == subject ]]; then
        gcloud auth print-access-token "${imp[@]}" | inst kubeconfig "$S0_I_ENDPOINT" "$S0_I_CA" >"$kc"
      else
        gcloud auth print-access-token | inst kubeconfig "$S0_I_ENDPOINT" "$S0_I_CA" >"$kc"
      fi
      out="$(kubectl --kubeconfig="$kc" get secrets --all-namespaces 2>&1)" || rc=$? ;;
    get_credentials)
      tok="$(gcloud auth print-access-token "${imp[@]}")"
      out="$(i_perm "$tok" container.clusters.getCredentials 2>&1)" || rc=$? ;;
    os_login)
      tok="$(gcloud auth print-access-token "${imp[@]}")"
      out="$(i_perm "$tok" compute.instances.osAdminLogin 2>&1)" || rc=$? ;;
    disk_snapshot)
      out="$(gcloud compute disks snapshot "$S0_I_DISK" --zone="$FLEET_S0_LOCATION" --project="$FLEET_S0_PROJECT" --snapshot-names="s0-i-snap-$who" "${imp[@]}" --quiet 2>&1)" || rc=$? ;;
    disk_image)
      out="$(gcloud compute images create "s0-i-img-$who" --source-disk="$S0_I_DISK" --source-disk-zone="$FLEET_S0_LOCATION" --force --project="$FLEET_S0_PROJECT" "${imp[@]}" --quiet 2>&1)" || rc=$? ;;
    disk_attach)
      out="$(gcloud compute instances attach-disk "$S0_I_NODE" --disk=s0-i-spare --zone="$FLEET_S0_LOCATION" --project="$FLEET_S0_PROJECT" "${imp[@]}" --quiet 2>&1)" || rc=$? ;;
    node_ssh_keys)
      out="$(gcloud compute instances add-metadata "$S0_I_NODE" --zone="$FLEET_S0_LOCATION" --project="$FLEET_S0_PROJECT" --metadata=ssh-keys="s0probe:ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQ s0probe" "${imp[@]}" --quiet 2>&1)" || rc=$? ;;
    actas_designated_node_identity)
      out="$(gcloud container node-pools create "s0-i-ok-$who" --cluster=s0-e-i --num-nodes=1 --machine-type=e2-standard-2 --service-account="$FLEET_S0_NODE_IDENTITY" --project="$FLEET_S0_PROJECT" --location="$FLEET_S0_LOCATION" "${imp[@]}" --quiet 2>&1)" || rc=$? ;;
    actas_other_identity)
      out="$(gcloud container node-pools create "s0-i-no-$who" --cluster=s0-e-i --num-nodes=1 --machine-type=e2-standard-2 --service-account="$FLEET_S0_OTHER_IDENTITY" --project="$FLEET_S0_PROJECT" --location="$FLEET_S0_LOCATION" "${imp[@]}" --quiet 2>&1)" || rc=$? ;;
    legacy_abac)
      out="$(gcloud container clusters update s0-e-i --enable-legacy-authorization --project="$FLEET_S0_PROJECT" --location="$FLEET_S0_LOCATION" "${imp[@]}" --quiet 2>&1)" || rc=$? ;;
    disable_workload_identity)
      out="$(gcloud container clusters update s0-e-i --disable-workload-identity --project="$FLEET_S0_PROJECT" --location="$FLEET_S0_LOCATION" "${imp[@]}" --quiet 2>&1)" || rc=$? ;;
    *) die "unknown attempt $id" ;;
  esac
  printf '%s\n%s\n' "$rc" "$out"
}
i_cleanup() { # best effort: put back what a SUCCESSFUL attempt changed, as the operator
  local q=(--project="$FLEET_S0_PROJECT" --quiet)
  case "$1" in
    disk_snapshot) gcloud compute snapshots delete "s0-i-snap-$2" "${q[@]}" >/dev/null 2>&1 || true ;;
    disk_image) gcloud compute images delete "s0-i-img-$2" "${q[@]}" >/dev/null 2>&1 || true ;;
    disk_attach) gcloud compute instances detach-disk "$S0_I_NODE" --disk=s0-i-spare --zone="$FLEET_S0_LOCATION" "${q[@]}" >/dev/null 2>&1 || true ;;
    node_ssh_keys) gcloud compute instances remove-metadata "$S0_I_NODE" --keys=ssh-keys --zone="$FLEET_S0_LOCATION" "${q[@]}" >/dev/null 2>&1 || true ;;
    actas_designated_node_identity) gcloud container node-pools delete "s0-i-ok-$2" --cluster=s0-e-i --location="$FLEET_S0_LOCATION" "${q[@]}" >/dev/null 2>&1 || true ;;
    actas_other_identity) gcloud container node-pools delete "s0-i-no-$2" --cluster=s0-e-i --location="$FLEET_S0_LOCATION" "${q[@]}" >/dev/null 2>&1 || true ;;
    legacy_abac) gcloud container clusters update s0-e-i --no-enable-legacy-authorization --location="$FLEET_S0_LOCATION" "${q[@]}" >/dev/null 2>&1 || true ;;
    disable_workload_identity) gcloud container clusters update s0-e-i --workload-pool="$FLEET_S0_PROJECT.svc.id.goog" --location="$FLEET_S0_LOCATION" "${q[@]}" >/dev/null 2>&1 || true ;;
  esac
}
probe_i() {
  require_preflight
  local k
  for k in FLEET_S0_APPLY_IDENTITY FLEET_S0_NODE_IDENTITY FLEET_S0_OTHER_IDENTITY; do
    required "$k"
    [[ ${!k} =~ ^[a-zA-Z0-9._-]+@[a-zA-Z0-9.-]+\.iam\.gserviceaccount\.com$ ]] || die "$k must be one service-account email"
  done
  [[ $FLEET_S0_APPLY_IDENTITY != "$FLEET_S0_OTHER_IDENTITY" && $FLEET_S0_NODE_IDENTITY != "$FLEET_S0_OTHER_IDENTITY" ]] ||
    die "the 'other' identity must be neither the apply identity nor the designated node identity"
  create_cluster s0-e-i
  export KUBECONFIG="$S0_OUT/kubeconfig"
  cluster_gc get-credentials s0-e-i
  S0_KUBERNETES_VERSION="$(kubectl version -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)["serverVersion"]["gitVersion"].removeprefix("v"))')"
  export S0_KUBERNETES_VERSION
  cat >"$S0_OUT/i-workload.yaml" <<'YAML'
apiVersion: v1
kind: Secret
metadata: {name: s0-workload-secret}
stringData: {value: workload-data}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: s0-data}
spec:
  accessModes: [ReadWriteOnce]
  resources: {requests: {storage: 1Gi}}
---
apiVersion: v1
kind: Pod
metadata: {name: s0-writer}
spec:
  containers:
    - name: writer
      image: busybox:1.37.0
      command: [sh, -c, 'echo s0 > /data/probe; sleep 86400']
      volumeMounts: [{name: data, mountPath: /data}]
  volumes:
    - name: data
      persistentVolumeClaim: {claimName: s0-data}
YAML
  kubectl apply -f "$S0_OUT/i-workload.yaml"
  kubectl wait pod/s0-writer --for=condition=Ready --timeout=300s
  local pv
  pv="$(kubectl get pv -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)["items"][0]["spec"]["csi"]["volumeHandle"].split("/")[-1])')"
  [[ $pv =~ ^[a-z0-9-]+$ ]] || die "no workload disk"
  S0_I_DISK="$pv"
  S0_I_NODE="$(gcloud compute instances list --project="$FLEET_S0_PROJECT" --filter='name~gke-s0-e-i' --limit=1 --format='value(name)')"
  [[ $S0_I_NODE =~ ^[a-z0-9-]+$ ]] || die "no node instance"
  S0_I_ENDPOINT="$(cluster_gc describe s0-e-i --format='value(endpoint)')"
  S0_I_CA="$(cluster_gc describe s0-e-i --format='value(masterAuth.clusterCaCertificate)')"
  gcloud compute disks create s0-i-spare --size=10GB --zone="$FLEET_S0_LOCATION" --project="$FLEET_S0_PROJECT" --quiet
  # id|klass|control that closes it (open paths only)
  local attempts=(
    "k8s_secrets|refuse|"
    "get_credentials|refuse|"
    "os_login|refuse|"
    "disk_snapshot|refuse|"
    "disk_image|refuse|"
    "disk_attach|refuse|"
    "node_ssh_keys|refuse|"
    "actas_designated_node_identity|must_succeed|"
    "actas_other_identity|refuse|"
    "legacy_abac|open_by_design|closed input surface on the fleet roots; S2 classifies the update forbidden"
    "disable_workload_identity|open_by_design|closed input surface on the fleet roots; S2 classifies the update forbidden"
  )
  local row id klass ctl out rc
  : >"$S0_OUT/i-attempts.jsonl"
  for row in "${attempts[@]}"; do
    IFS='|' read -r id klass ctl <<<"$row"
    out="$(i_run subject "$id")"
    i_cleanup "$id" subject
    local s_out c_out
    s_out="$out"
    out="$(i_run control "$id")"
    i_cleanup "$id" control
    c_out="$out"
    python3 - "$S0_OUT/i-attempts.jsonl" "$id" "$klass" "$ctl" "$s_out" "$c_out" <<'PY'
import json,sys
o,i,k,ctl,s,c=sys.argv[1:]
def split(t):
    head,_,body=t.partition("\n")
    return dict(exit=int(head),output=body[:2000])
with open(o,"a") as f:
    f.write(json.dumps(dict(id=i,klass=k,assigned_control=ctl or None,subject=split(s),control=split(c)))+"\n")
PY
  done
  python3 - "$S0_OUT/e-i-cfg.json" "$S0_OUT/i-attempts.jsonl" "$FLEET_S0_APPLY_IDENTITY" <<'PY'
import json,sys
o,a,ident=sys.argv[1:]
json.dump(dict(identity=ident,attempts=[json.loads(l) for l in open(a)]),open(o,"w"))
PY
  cluster_gc delete s0-e-i
  gcloud compute disks delete s0-i-spare --zone="$FLEET_S0_LOCATION" --project="$FLEET_S0_PROJECT" --quiet >/dev/null 2>&1 || true
  emit_verdict E-i '["cloud","kubernetes"]' exposure-verdict "$S0_OUT/e-i-cfg.json"
}
