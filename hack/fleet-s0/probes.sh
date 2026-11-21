#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# Called in the runner shell, so a Tier C probe cannot inherit a stale capability.
probe_a() {
  require_preflight
  curl --fail --silent --show-error 'https://container.googleapis.com/$discovery/rest?version=v1' >"$S0_OUT/discovery.json"
  local measured found
  measured="$(python3 - "$S0_OUT/discovery.json" <<'PY'
import json,sys
p=json.load(open(sys.argv[1]))["schemas"]["Cluster"]["properties"]
assert p, "empty Cluster schema"
fields=sorted(p)
found=[k for k in fields if "deletion" in k.lower() and "protect" in k.lower()]
print(json.dumps(dict(fields=fields,deletion_protection_fields=found)))
PY
)"
  found="$(python3 -c 'import json,sys; print(len(json.loads(sys.argv[1])["deletion_protection_fields"]))' "$measured")"
  if [[ $found != 0 ]]; then
    result E-a FAILS '["cloud"]' "$measured"
    return 1
  fi
  result E-a HOLDS '["cloud"]' "$measured"
}
probe_b() {
  require_preflight
  # Prove the identity can delete BEFORE attaching the deny.
  create_cluster s0-e-b-control
  cluster_gc delete s0-e-b-control --impersonate-service-account="$FLEET_S0_TEST_IDENTITY"
  create_cluster s0-e-b
  deny_policy
  sleep "${FLEET_S0_DENY_SETTLE_SECONDS:-120}"
  expect_denied cluster_gc delete s0-e-b --impersonate-service-account="$FLEET_S0_TEST_IDENTITY"
  [[ $(cluster_gc describe s0-e-b --format='value(status)') == RUNNING ]] ||
    die "E-b cluster is not RUNNING after refusal"
  remove_deny
  sleep "${FLEET_S0_DENY_SETTLE_SECONDS:-120}"
  cluster_gc delete s0-e-b --impersonate-service-account="$FLEET_S0_TEST_IDENTITY"
  result E-b HOLDS '["cloud"]' '{"baseline_delete":true,"deny":"PERMISSION_DENIED","status":"RUNNING","remove_only_deny_delete":true}'
}
probe_c() {
  require_preflight
  required FLEET_S0_UPDATE_ONLY_IDENTITY
  required FLEET_S0_UPDATE_ONLY_ROLE
  # The subject role is deliberately narrower than the candidate apply role.
  gcloud iam roles describe "$FLEET_S0_UPDATE_ONLY_ROLE" --project="$FLEET_S0_PROJECT" --format=json >"$S0_OUT/update-role.json"
  python3 - "$S0_OUT/update-role.json" <<'PY'
import json,sys
p=set(json.load(open(sys.argv[1]))["includedPermissions"])
assert "container.clusters.update" in p
assert p <= {"container.clusters.update","container.clusters.get","container.operations.get"}
PY
  create_cluster s0-e-c
  gcloud container node-pools create probe --cluster=s0-e-c --num-nodes=1 --machine-type=e2-standard-2     --project="$FLEET_S0_PROJECT" --location="$FLEET_S0_LOCATION"
  gcloud container node-pools delete probe --cluster=s0-e-c --project="$FLEET_S0_PROJECT"     --location="$FLEET_S0_LOCATION" --impersonate-service-account="$FLEET_S0_UPDATE_ONLY_IDENTITY" --quiet
  # Remove ONLY the measured role, then repeat on a second node pool.
  gcloud container node-pools create control --cluster=s0-e-c --num-nodes=1 --machine-type=e2-standard-2     --project="$FLEET_S0_PROJECT" --location="$FLEET_S0_LOCATION"
  S0_RESTORE_UPDATE_ROLE=true
  gcloud projects remove-iam-policy-binding "$FLEET_S0_PROJECT"     --member="serviceAccount:$FLEET_S0_UPDATE_ONLY_IDENTITY"     --role="projects/$FLEET_S0_PROJECT/roles/$FLEET_S0_UPDATE_ONLY_ROLE" --condition=None --quiet
  sleep "${FLEET_S0_DENY_SETTLE_SECONDS:-120}"
  expect_denied gcloud container node-pools delete control --cluster=s0-e-c --project="$FLEET_S0_PROJECT"     --location="$FLEET_S0_LOCATION" --impersonate-service-account="$FLEET_S0_UPDATE_ONLY_IDENTITY" --quiet
  cluster_gc delete s0-e-c
  result E-c HOLDS '["cloud"]' '{"update_only_pool_delete":true,"without_update":"PERMISSION_DENIED","scope":"control plane only"}'
}
iam_has_delete() {
  local token code answer
  token="$(gcloud auth print-access-token --impersonate-service-account="$FLEET_S0_TEST_IDENTITY")" || die "could not issue permission-probe token"
  code="$(curl --silent --show-error --output "$S0_OUT/permissions.json" --write-out '%{http_code}'     --header "Authorization: Bearer $token" --header 'Content-Type: application/json'     --data '{"permissions":["container.clusters.delete"]}'     "https://cloudresourcemanager.googleapis.com/v1/projects/$FLEET_S0_PROJECT:testIamPermissions")" || die "permission-probe transport failure"
  [[ $code == 200 ]] || die "testIamPermissions HTTP $code"
  answer="$(python3 - "$S0_OUT/permissions.json" <<'PY'
import json,sys
p=json.load(open(sys.argv[1]))
assert isinstance(p,dict) and "error" not in p
assert isinstance(p.get("permissions",[]),list)
print("allowed" if "container.clusters.delete" in p.get("permissions",[]) else "denied")
PY
)" || die "malformed testIamPermissions response"
  case "$answer" in allowed) return 0 ;; denied) return 1 ;; *) die "invalid permission result" ;; esac
}
probe_e() {
  require_preflight
  local n start elapsed deadline
  : >"$S0_OUT/deny-latencies"
  for n in 1 2 3 4 5; do
    iam_has_delete || die "E-e baseline lacks delete permission"
    start="$(date +%s)"
    deny_policy
    deadline=$((SECONDS + ${FLEET_S0_TIMEOUT_SECONDS:-900}))
    while iam_has_delete; do
      (( SECONDS < deadline )) || die "deny did not become effective"
      sleep "${FLEET_S0_POLL_SECONDS:-2}"
    done
    elapsed=$(( $(date +%s) - start ))
    echo "$elapsed" >>"$S0_OUT/deny-latencies"
    remove_deny
    deadline=$((SECONDS + ${FLEET_S0_TIMEOUT_SECONDS:-900}))
    until iam_has_delete; do
      (( SECONDS < deadline )) || die "removed deny did not restore baseline permission"
      sleep "${FLEET_S0_POLL_SECONDS:-2}"
    done
  done
  result E-e HOLDS '["cloud"]' "$(python3 - "$S0_OUT/deny-latencies" <<'PY'
import json,statistics,sys
samples=[int(x) for x in open(sys.argv[1])]
assert len(samples)==5
print(json.dumps(dict(samples_s=samples,median_s=statistics.median(samples),max_s=max(samples),
measurement="Resource Manager testIamPermissions for container.clusters.delete")))
PY
)"
}
probe_f2() {
  require_preflight
  # Do not disable the operator or either teardown identity.
  assert_deny
  create_cluster s0-e-f2
  local token start deadline code cached=-1 fresh=-1 pre_status post_status
  token="$(gcloud auth print-access-token --impersonate-service-account="$FLEET_S0_TEST_IDENTITY" --lifetime=3600)"
  local uri="https://container.googleapis.com/v1/projects/$FLEET_S0_PROJECT/locations/$FLEET_S0_LOCATION/clusters/s0-e-f2"
  token_status() {
    curl --silent --show-error --output "$S0_OUT/token-probe.json" --write-out '%{http_code}'       --header "Authorization: Bearer $token" "$uri"
  }
  pre_status="$(token_status)"
  [[ $pre_status == 200 ]] || die "cached-token baseline HTTP $pre_status"
  start="$(date +%s)"
  # EXIT restores containment even when a measurement fails; teardown does not use this identity.
  S0_DISABLED_IDENTITY="$FLEET_S0_TEST_IDENTITY"
  gcloud iam service-accounts disable "$FLEET_S0_TEST_IDENTITY" --project="$FLEET_S0_PROJECT" --quiet
  deadline=$((SECONDS + 900))
  while (( cached < 0 || fresh < 0 )); do
    code="$(token_status)"
    case "$code" in
      200) ;;
      401|403)
        python3 - "$S0_OUT/token-probe.json" <<'PY'
import json,sys
assert json.load(open(sys.argv[1]))["error"]["status"] in ("PERMISSION_DENIED","UNAUTHENTICATED")
PY
        (( cached >= 0 )) || cached=$(( $(date +%s) - start ))
        ;;
      *) die "unexpected cached-token HTTP $code" ;;
    esac
    if ! gcloud auth print-access-token --impersonate-service-account="$FLEET_S0_TEST_IDENTITY" >"$S0_OUT/fresh-token" 2>"$S0_OUT/fresh-error"; then
      grep -Eq 'PERMISSION_DENIED|disabled' "$S0_OUT/fresh-error" || die "fresh issuance failed for an unrelated reason"
      (( fresh >= 0 )) || fresh=$(( $(date +%s) - start ))
    fi
    (( SECONDS < deadline )) || die "not observed before re-enable deadline; token must remain unexpired"
    (( cached >= 0 && fresh >= 0 )) || sleep "${FLEET_S0_POLL_SECONDS:-5}"
  done
  gcloud iam service-accounts enable "$FLEET_S0_TEST_IDENTITY" --project="$FLEET_S0_PROJECT" --quiet
  sleep "${FLEET_S0_DENY_SETTLE_SECONDS:-120}"
  (( $(date +%s) - start < 3300 )) || die "pre-disable token may have expired"
  post_status="$(token_status)"
  [[ $post_status == 200 || $post_status == 401 || $post_status == 403 ]] || die "unexpected re-enable HTTP $post_status"
  S0_DISABLED_IDENTITY=
  cluster_gc delete s0-e-f2
  result E-f2 HOLDS '["cloud"]' "{\"cached_refusal_s\":$cached,\"fresh_issuance_refusal_s\":$fresh,\"pre_disable_token_after_reenable_http\":$post_status,\"containment_only\":true}"
}
probe_g_gcs() {
  require_preflight
  local bucket="$FLEET_S0_PROJECT-s0-lock-$S0_RUN" pid rc=0
  export GOOGLE_OAUTH_ACCESS_TOKEN
  GOOGLE_OAUTH_ACCESS_TOKEN="$(gcloud auth print-access-token)"
  mkdir "$S0_OUT/gcs"
  gcloud storage buckets create "gs://$bucket" --project="$FLEET_S0_PROJECT" --location="$FLEET_S0_SCHEDULER_LOCATION" --uniform-bucket-level-access
  cat >"$S0_OUT/gcs/main.tf" <<EOF
terraform {
  required_version = "= 1.10.10"
  backend "gcs" {
    bucket = "$bucket"
    prefix = "probe"
  }
}
resource "terraform_data" "probe" {
  provisioner "local-exec" {
    command = "touch started; while [ ! -f release ]; do sleep 1; done"
  }
}
EOF
  tofu -chdir="$S0_OUT/gcs" init -input=false
  tofu -chdir="$S0_OUT/gcs" apply -auto-approve -input=false >"$S0_OUT/gcs-apply.log" 2>&1 &
  pid=$!
  wait_file "$S0_OUT/gcs/started" "$pid"
  kill -KILL "$pid"
  wait "$pid" || rc=$?
  [[ $rc != 0 ]] || die "SIGKILL did not stop writer"
  gcloud storage cat "gs://$bucket/probe/default.tflock" >"$S0_OUT/gcs-lock.json"
  python3 - "$S0_OUT/gcs-lock.json" <<'PY'
import json,sys
p=json.load(open(sys.argv[1]))
assert p["ID"] and p["Who"] and p["Operation"] and p["Created"]
PY
  # Negative control: after holder verification, unlock only the killed writer, run to completion.
  local lock
  lock="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["ID"])' "$S0_OUT/gcs-lock.json")"
  tofu -chdir="$S0_OUT/gcs" force-unlock -force "$lock"
  touch "$S0_OUT/gcs/release"
  tofu -chdir="$S0_OUT/gcs" apply -auto-approve -input=false
  if gcloud storage cat "gs://$bucket/probe/default.tflock" >"$S0_OUT/lock-control" 2>&1; then
    die "completed writer still has a lock"
  fi
  grep -Eq '404|not found|No URLs matched' "$S0_OUT/lock-control" || die "cannot prove lock absent"
  result E-g-gcs HOLDS '["tofu","cloud"]' "$(python3 - "$S0_OUT/gcs-lock.json" <<'PY'
import json,sys
print(json.dumps(dict(sigkill_lock=json.load(open(sys.argv[1])),completed_apply_lock_absent=True)))
PY
)"
}
wait_file() {
  local deadline=$((SECONDS + 120))
  while [[ ! -f $1 ]]; do
    kill -0 "$2" 2>/dev/null || die "runner exited before checkpoint $1"
    (( SECONDS < deadline )) || die "checkpoint timed out: $1"
    sleep 1
  done
}
probe_k() {
  require_preflight
  create_cluster s0-e-k
  export KUBECONFIG="$S0_OUT/kubeconfig"
  cluster_gc get-credentials s0-e-k
  S0_KUBERNETES_VERSION="$(kubectl version -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)["serverVersion"]["gitVersion"].removeprefix("v"))')"
  cat >"$S0_OUT/pvc.yaml" <<'YAML'
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: s0-retain
provisioner: pd.csi.storage.gke.io
reclaimPolicy: Retain
volumeBindingMode: WaitForFirstConsumer
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: s0-data
spec:
  storageClassName: s0-retain
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: s0-writer
spec:
  containers:
    - name: writer
      image: busybox:1.37.0
      command: [sh, -c, 'echo s0 > /data/probe; sleep 86400']
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: s0-data
---
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: s0-retain
driver: pd.csi.storage.gke.io
deletionPolicy: Retain
---
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: s0-snapshot
spec:
  volumeSnapshotClassName: s0-retain
  source:
    persistentVolumeClaimName: s0-data
YAML
  kubectl apply -f "$S0_OUT/pvc.yaml"
  kubectl wait pod/s0-writer --for=condition=Ready --timeout=300s
  kubectl wait volumesnapshot/s0-snapshot --for=jsonpath='{.status.readyToUse}'=true --timeout=300s
  kubectl get pv -o json >"$S0_OUT/pv-before.json"
  kubectl get volumesnapshotcontent -o json >"$S0_OUT/snapshot-before.json"
  gcloud compute disks list --project="$FLEET_S0_PROJECT" --format=json >"$S0_OUT/disks-before.json"
  gcloud compute snapshots list --project="$FLEET_S0_PROJECT" --format=json >"$S0_OUT/snapshots-before.json"
  cluster_gc delete s0-e-k
  gcloud compute disks list --project="$FLEET_S0_PROJECT" --format=json >"$S0_OUT/disks-after.json"
  gcloud compute snapshots list --project="$FLEET_S0_PROJECT" --format=json >"$S0_OUT/snapshots-after.json"
  result E-k HOLDS '["cloud","kubernetes"]' "$(python3 - "$S0_OUT" <<'PY'
import json,pathlib,sys
d=pathlib.Path(sys.argv[1])
pv=json.loads((d/"pv-before.json").read_text())["items"]
contents=json.loads((d/"snapshot-before.json").read_text())["items"]
disks={p["spec"]["csi"]["volumeHandle"].split("/")[-1] for p in pv}
snapshots={p["status"]["snapshotHandle"].split("/")[-1] for p in contents}
assert disks and snapshots, "disk and snapshot creation controls missing"
after_d=json.loads((d/"disks-after.json").read_text())
after_s=json.loads((d/"snapshots-after.json").read_text())
print(json.dumps(dict(disks=[x["name"] for x in after_d if x["name"] in disks],
snapshots=[x["name"] for x in after_s if x["name"] in snapshots],
disk_handles=sorted(disks),snapshot_handles=sorted(snapshots))))
PY
)"
}
experiment() {
  case "$1" in
    E-a) probe_a ;;
    E-b) probe_b ;;
    E-c) probe_c ;;
    E-d) not_implemented "E-d: pinned-issuer JWKS publisher and kind runner credential fixtures, including recreated KSA UID and three rejected exchanges" ;;
    E-e) probe_e ;;
    E-f) not_implemented "E-f: surviving partitioned runner instrumentation for KSA, STS and access-token lifetimes and no-cut control beyond computed bound" ;;
    E-f2) probe_f2 ;;
    E-g) if [[ $S0_BACKEND == pg ]]; then probe_g_pg; else probe_g_gcs; fi ;;
    E-h) not_implemented "E-h: provider checkpoint instrumentation for five crash points, recovery import and unplanned-delete capture" ;;
    E-i) not_implemented "E-i: candidate apply-role fixture and workload-data targets for every indirect access path in spec section 3.4" ;;
    E-j) probe_j ;;
    E-k) probe_k ;;
    E-l) probe_l ;;
    *) die "unknown experiment $1" ;;
  esac
}
