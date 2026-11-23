#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# Shared S0 primitives. Creation is possible only inside the current runner's shell.
set -euo pipefail
S0_HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
S0_CLUSTER_NAMES=(s0-e-b-control s0-e-b s0-e-c s0-e-e s0-e-f2 s0-e-k s0-e-h-1 s0-e-h-2 s0-e-h-3 s0-e-h-4 s0-e-h-5 s0-e-h-fc s0-e-h-dp s0-e-i)
die() { echo "fleet-s0 ERROR[PRECONDITION]: $*" >&2; exit 2; }
not_implemented() { echo "fleet-s0 ERROR[NOT_IMPLEMENTED]: not implemented: $*" >&2; exit 78; }
required() { [[ -n ${!1:-} ]] || die "required: $1"; }
require_preflight() {
  [[ ${S0_PREFLIGHT:-} == "$$" ]] || die "reaper preflight has not passed in this run"
}
cluster_gc() { gcloud container clusters "$@" --project="$FLEET_S0_PROJECT" --location="$FLEET_S0_LOCATION" --quiet; }
create_cluster() {
  require_preflight
  local name="$1" allowed=false item
  for item in "${S0_CLUSTER_NAMES[@]}"; do [[ $item != "$name" ]] || allowed=true; done
  [[ $allowed == true ]] || die "cluster has no armed deterministic DELETE target: $name"
  cluster_gc create "$name" --num-nodes=1 --machine-type=e2-standard-2 --disk-size=20
}
assert_no_lien() {
  local liens
  # Liens are an alpha-track command group in current gcloud; the GA spelling is "Invalid choice".
  liens="$(gcloud alpha resource-manager liens list --project="$1" --format='value(name)')"
  [[ -z $liens ]] || die "project $1 carries a lien"
}
assert_deny() {
  required FLEET_S0_TEST_IDENTITY
  local denied="${FLEET_S0_DENIED_PRINCIPAL:-$FLEET_S0_TEST_IDENTITY}"
  [[ $denied == "$FLEET_S0_TEST_IDENTITY" && $denied != "$S0_REAPER" && $denied != "$S0_CLUSTER_REAPER" ]] ||
    die "E-b deny principal set must equal only the test identity, excluding both reapers"
  [[ $denied =~ ^[a-zA-Z0-9._-]+@[a-zA-Z0-9.-]+\.iam\.gserviceaccount\.com$ ]] ||
    die "test identity must be one service-account email"
}
check_delete_role() { # role permission target-project
  local role="$1" permission="$2" project="$3" rc=0
  gcloud iam roles describe "$role" --project="$project" --format=json >"$S0_OUT/role.json" 2>"$S0_OUT/role-error" || rc=$?
  if (( rc != 0 )); then
    grep -Eq 'NOT_FOUND|not found' "$S0_OUT/role-error" || die "cannot inspect reaper role"
    gcloud iam roles create "$role" --project="$project" --title="S0 scoped deletion" --permissions="$permission" --stage=GA --quiet
    gcloud iam roles describe "$role" --project="$project" --format=json >"$S0_OUT/role.json"
  fi
  python3 - "$S0_OUT/role.json" "$permission" <<'PY'
import json,sys
p=json.load(open(sys.argv[1]))
assert p.get("includedPermissions")==[sys.argv[2]] and not p.get("deleted",False), "reaper role must have exactly one delete permission"
PY
}
scheduler_delete() { # job URI identity delay-seconds; yearly cron retries after scheduled time.
  local job="$1" uri="$2" identity="$3" delay="$4" base="${5:-$S0_STARTED}" schedule
  schedule="$(python3 - "$base" "$delay" <<'PY'
import datetime,sys
at=datetime.datetime.fromtimestamp(int(sys.argv[1])+int(sys.argv[2]),datetime.timezone.utc)
# Round UP to a minute: never fire before the requested deadline.
at=(at+datetime.timedelta(seconds=59)).replace(second=0,microsecond=0)
print(at.strftime("%M %H %d %m *"))
PY
)"
  gcloud scheduler jobs create http "$job" --project="$FLEET_S0_ADMIN_PROJECT"     --location="$FLEET_S0_SCHEDULER_LOCATION" --schedule="$schedule" --time-zone=Etc/UTC     --uri="$uri" --http-method=DELETE --oauth-service-account-email="$identity"     --oauth-token-scope=https://www.googleapis.com/auth/cloud-platform     --attempt-deadline=180s --max-retry-attempts=5 --quiet
}
arm_outer() {
  local target="$1" seconds="$2" number
  number="$(gcloud projects describe "$target" --format='value(projectNumber)')"
  [[ $number =~ ^[0-9]+$ ]] || die "missing project number for $target"
  check_delete_role fleetS0ProjectDelete resourcemanager.projects.delete "$target"
  gcloud projects add-iam-policy-binding "$target" --member="serviceAccount:$S0_REAPER"     --role="projects/$target/roles/fleetS0ProjectDelete" --condition=None --quiet >/dev/null
  scheduler_delete "s0-outer-$target-$S0_RUN"     "https://cloudresourcemanager.googleapis.com/v3/projects/$number" "$S0_REAPER" "$seconds" "${3:-$S0_STARTED}"
}
reaper_preflight() {
  # Clear any inherited or previous-run capability before checking anything.
  unset S0_PREFLIGHT
  for key in FLEET_S0_ADMIN_PROJECT FLEET_S0_PROJECT FLEET_S0_FOLDER FLEET_S0_LOCATION       FLEET_S0_SCHEDULER_LOCATION FLEET_S0_TEST_IDENTITY FLEET_S0_CANARY_PROJECT; do required "$key"; done
  [[ $FLEET_S0_PROJECT != "$FLEET_S0_ADMIN_PROJECT" &&
     $FLEET_S0_CANARY_PROJECT != "$FLEET_S0_PROJECT" &&
     $FLEET_S0_CANARY_PROJECT != "$FLEET_S0_ADMIN_PROJECT" ]] || die "admin, spike and canary projects must differ"
  S0_REAPER="s0-reaper@$FLEET_S0_ADMIN_PROJECT.iam.gserviceaccount.com"
  S0_CLUSTER_REAPER="s0-cluster-reaper@$FLEET_S0_ADMIN_PROJECT.iam.gserviceaccount.com"
  assert_deny
  [[ -z ${GOOGLE_APPLICATION_CREDENTIALS:-} && -z ${CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE:-} ]] ||
    die "Tier C requires short-lived user credentials, no credential key file"
  local account label name state deadline
  account="$(gcloud auth list --filter=status:ACTIVE --format='value(account)')"
  [[ -n $account && $account != *gserviceaccount.com* && $account != *dc-perf-runner* && $account != *$'\n'* ]] ||
    die "Tier C requires exactly one active user identity"
  label="$(gcloud projects describe "$FLEET_S0_PROJECT" --format='value(labels.fleet-s0)')"
  [[ $label == throwaway ]] || die "spike project must carry fleet-s0=throwaway"
  assert_no_lien "$FLEET_S0_PROJECT"
  check_delete_role fleetS0ClusterDelete container.clusters.delete "$FLEET_S0_PROJECT"
  # The reaper is armed against fixed cluster names: it must never be armed against a cluster this run did not create.
  existing="$(gcloud container clusters list --project="$FLEET_S0_PROJECT" --format='value(name)')"
  for name in "${S0_CLUSTER_NAMES[@]}"; do
    [[ $'
'$existing$'
' != *$'
'$name$'
'* ]] || die "cluster $name already exists in the spike project; the reaper would delete one this run did not create"
  done
  S0_STARTED="$(date +%s)"
  S0_RUN="$S0_STARTED-$$"
  arm_outer "$FLEET_S0_PROJECT" 21600
  gcloud projects add-iam-policy-binding "$FLEET_S0_PROJECT"     --member="serviceAccount:$S0_CLUSTER_REAPER"     --role="projects/$FLEET_S0_PROJECT/roles/fleetS0ClusterDelete" --condition=None --quiet >/dev/null
  for name in "${S0_CLUSTER_NAMES[@]}"; do
    scheduler_delete "s0-inner-$name-$S0_RUN"       "https://container.googleapis.com/v1/projects/$FLEET_S0_PROJECT/locations/$FLEET_S0_LOCATION/clusters/$name"       "$S0_CLUSTER_REAPER" 14400
  done
  # The canary and its reaper are the only bootstrap creations before the capability is issued.
  # Never use Scheduler 'run': the scheduled job itself must actually fire.
  # The parent is a folder ID, or organizations/ID when the projects sit directly under the organization.
  local parent=(--folder="$FLEET_S0_FOLDER")
  [[ $FLEET_S0_FOLDER != organizations/* ]] || parent=(--organization="${FLEET_S0_FOLDER#organizations/}")
  gcloud projects create "$FLEET_S0_CANARY_PROJECT" "${parent[@]}" --labels=fleet-s0=canary --quiet
  assert_no_lien "$FLEET_S0_CANARY_PROJECT"
  arm_outer "$FLEET_S0_CANARY_PROJECT" 300 "$(date +%s)"
  deadline=$((SECONDS + ${FLEET_S0_TIMEOUT_SECONDS:-900}))
  while :; do
    state="$(gcloud projects describe "$FLEET_S0_CANARY_PROJECT" --format='value(lifecycleState)')"
    if [[ $state == DELETE_REQUESTED ]]; then echo "canary: DELETE_REQUESTED observed"; break; fi
    (( SECONDS < deadline )) || die "canary never reached DELETE_REQUESTED"
    sleep "${FLEET_S0_POLL_SECONDS:-10}"
  done
  assert_no_lien "$FLEET_S0_PROJECT"
  assert_deny
  S0_PREFLIGHT="$$"
}
deny_policy() {
  require_preflight
  assert_deny
  assert_no_lien "$FLEET_S0_PROJECT"
  python3 - "$FLEET_S0_TEST_IDENTITY" >"$S0_OUT/deny.json" <<'PY'
import json,sys
print(json.dumps({"displayName":"fleet-s0 E-b","rules":[{"denyRule":{
"deniedPrincipals":["principal://iam.googleapis.com/projects/-/serviceAccounts/"+sys.argv[1]],
"deniedPermissions":["container.googleapis.com/clusters.delete"]}}]}))
PY
  S0_PROJECT_NUMBER="$(gcloud projects describe "$FLEET_S0_PROJECT" --format='value(projectNumber)')"
  gcloud iam policies create "s0-eb-$S0_RUN" --attachment-point="cloudresourcemanager.googleapis.com/projects/$S0_PROJECT_NUMBER"     --kind=denypolicies --policy-file="$S0_OUT/deny.json"
}
remove_deny() {
  gcloud iam policies delete "s0-eb-$S0_RUN"     --attachment-point="cloudresourcemanager.googleapis.com/projects/$S0_PROJECT_NUMBER" --kind=denypolicies --quiet
}
expect_denied() {
  local output code=0
  output="$("$@" 2>&1)" || code=$?
  printf '%s\n' "$output"
  [[ $code != 0 && $output == *PERMISSION_DENIED* ]] || die "expected PERMISSION_DENIED (exit $code)"
}
result() { # ID verdict binds-json measurements-json
  printf '%s %s\n' "$1" "$2"
  python3 - "$S0_OUT/results.jsonl" "$@" "${S0_KUBERNETES_VERSION:-}" <<'PY'
import json,sys
with open(sys.argv[1],"a") as f:
    p=dict(id=sys.argv[2],verdict=sys.argv[3],binds=json.loads(sys.argv[4]),measured=json.loads(sys.argv[5]))
    if "kubernetes" in p["binds"]:
        assert sys.argv[6], "missing measured Kubernetes version"
        p["kubernetes_version"]=sys.argv[6]
    f.write(json.dumps(p)+"\n")
PY
  [[ $2 != FAILS ]] || return 1
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
  [[ ${1:-} == --self-test ]] || die "lib.sh is a sourced library"
  exec bash "$S0_HERE/self-test.sh" lib
fi
