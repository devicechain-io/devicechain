#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Blocks until a GKE cluster has finished every operation running on it, or fails.
#
#   wait-for-cluster-operations.sh PROJECT CLUSTER LOCATION [TIMEOUT_SECONDS]
#   wait-for-cluster-operations.sh --self-test
#
# WHY THIS EXISTS. `tofu apply` returns when the resources it manages exist, and GKE is not done
# then: after the cluster and its node pools are created it goes on to apply their settings (the
# monitoring configuration, for one) as an UPDATE_CLUSTER operation, and the cluster reports
# RECONCILING until that ends. A `dcctl install` started in that window talks to an API server
# that is being reconfigured, and in the G3 release-candidate run its first attempt timed out
# there. The apply is not finished until the cluster is, so the configuration waits for it.
#
# TWO SIGNALS, BOTH REQUIRED. The cluster's own status (RUNNING, not RECONCILING) is the one that
# cannot be misread, because it needs no filter. The operations list names what is running, node
# pool operations included, but a filter that matches nothing looks exactly like "nothing is
# running", so it can only ever add waiting, never remove it.
#
# 🔴 IT FAILS CLOSED. A timeout, a missing gcloud and a gcloud that errors for the whole wait are
# all a non-zero exit: "I could not tell" is not "the cluster is ready".
set -euo pipefail

POLL="${DC_GKE_WAIT_POLL_SECONDS:-10}"

cluster_status() { gcloud container clusters describe "$2" --project "$1" --location "$3" --format='value(status)'; }

running_operations() {
  gcloud container operations list --project "$1" \
    --filter="status!=DONE AND targetLink~/clusters/$2(/|\$)" --format='value(name,operationType,status)'
}

wait_for_cluster() { # project cluster location timeout-seconds
  local project="$1" cluster="$2" location="$3" timeout="$4"
  command -v gcloud >/dev/null 2>&1 || {
    echo "wait-for-cluster-operations: gcloud is not on PATH, so the cluster's operations cannot be checked." >&2
    echo "  Install the Google Cloud CLI, or set wait_for_cluster_operations = false and wait yourself." >&2
    return 1
  }
  local deadline=$((SECONDS + timeout)) status="" ops="" last_err="" err
  err="$(mktemp)"
  # shellcheck disable=SC2064 # expand now: $err is local
  trap "rm -f '$err'" RETURN
  while :; do
    status="" ops=""
    # stderr stays out of the answers: a gcloud notice must not turn RUNNING into something else.
    if status="$(cluster_status "$project" "$cluster" "$location" 2>"$err")" && ops="$(running_operations "$project" "$cluster" 2>"$err")"; then
      if [[ "$status" == RUNNING && -z "$ops" ]]; then
        echo "wait-for-cluster-operations: $cluster is RUNNING with no operation in progress"
        return 0
      fi
      echo "wait-for-cluster-operations: $cluster is ${status:-unknown}${ops:+, running: ${ops//$'\n'/; }}; waiting"
    else
      last_err="$(cat "$err")"
      echo "wait-for-cluster-operations: gcloud failed (${last_err//$'\n'/ }); retrying" >&2
    fi
    if (( SECONDS >= deadline )); then
      echo "wait-for-cluster-operations: $cluster was still not settled after ${timeout}s (status ${status:-unknown}${last_err:+, last error: ${last_err//$'\n'/ }})" >&2
      return 1
    fi
    sleep "$POLL"
  done
}

self_test() {
  local d rc=0 code
  d="$(mktemp -d)"
  # shellcheck disable=SC2064 # expand now: $d is local
  trap "rm -rf '$d'" EXIT
  mkdir -p "$d/bin"
  # A gcloud that answers from the environment. FAKE_STATUSES is the cluster status on successive
  # calls (the last one repeats); FAKE_OPS_CALLS is how many `operations list` calls still report an
  # operation; FAKE_FAIL makes every call fail.
  cat >"$d/bin/gcloud" <<'FAKE'
#!/usr/bin/env bash
[[ -z "${FAKE_FAIL:-}" ]] || { echo "ERROR: (gcloud) boom" >&2; exit 1; }
n() { local f="$FAKE_DIR/$1"; local c; c="$(cat "$f" 2>/dev/null || echo 0)"; echo $((c + 1)) >"$f"; echo "$c"; }
case "$2 $3" in
  "clusters describe")
    i="$(n status)"; read -ra all <<<"$FAKE_STATUSES"
    (( i < ${#all[@]} )) || i=$((${#all[@]} - 1))
    echo "${all[$i]}" ;;
  "operations list")
    i="$(n ops)"
    if (( i < ${FAKE_OPS_CALLS:-0} )); then echo "operation-1 UPDATE_CLUSTER RUNNING"; fi ;;
  *) echo "fake gcloud: unexpected $*" >&2; exit 2 ;;
esac
FAKE
  chmod +x "$d/bin/gcloud"
  run() { # expect-ok(0|1), then env assignments; the wait is 3 s so a hung wait fails the test
    local want="$1"; shift
    rm -f "$d/status" "$d/ops"
    code=0
    env FAKE_DIR="$d" DC_GKE_WAIT_POLL_SECONDS=0.2 PATH="$d/bin:$PATH" "$@" \
      bash "${BASH_SOURCE[0]}" p c l 3 >"$d/out" 2>&1 || code=$?
    if [[ "$want" == 0 && "$code" -ne 0 ]] || [[ "$want" == 1 && "$code" -eq 0 ]]; then
      echo "self-test: $* exited $code, want ${want/1/non-zero}" >&2; cat "$d/out" >&2; rc=1
    fi
  }

  run 0 FAKE_STATUSES="RUNNING"
  run 0 FAKE_STATUSES="RECONCILING RECONCILING RUNNING"
  grep -q "RECONCILING.*waiting" "$d/out" || { echo "self-test: it did not wait through RECONCILING" >&2; rc=1; }
  # A cluster that reports RUNNING while an operation is still listed is not settled yet.
  run 0 FAKE_STATUSES="RUNNING" FAKE_OPS_CALLS=3
  grep -q "UPDATE_CLUSTER" "$d/out" || { echo "self-test: it did not wait for the listed operation" >&2; rc=1; }
  # The negative controls: every way of not being ready, or not knowing, is a failure.
  run 1 FAKE_STATUSES="RECONCILING"
  run 1 FAKE_STATUSES="RUNNING" FAKE_OPS_CALLS=1000
  run 1 FAKE_STATUSES="RUNNING" FAKE_FAIL=1
  grep -q "still not settled" "$d/out" || { echo "self-test: a gcloud that always fails did not end in a timeout message" >&2; rc=1; }
  # No gcloud at all: PATH holds only a directory with no gcloud (and the bash this runs under).
  mkdir -p "$d/empty"
  code=0
  env PATH="$d/empty" "$(command -v bash)" "${BASH_SOURCE[0]}" p c l 3 >"$d/out" 2>&1 || code=$?
  [[ "$code" -ne 0 ]] || { echo "self-test: a missing gcloud passed" >&2; rc=1; }
  grep -q "gcloud is not on PATH" "$d/out" || { echo "self-test: a missing gcloud was not named" >&2; rc=1; }

  [[ "$rc" -eq 0 ]] && echo "self-test: ok"
  return "$rc"
}

if [[ "${1:-}" == --self-test ]]; then
  self_test
  exit $?
fi
[[ $# -ge 3 ]] || { echo "usage: $0 PROJECT CLUSTER LOCATION [TIMEOUT_SECONDS]" >&2; exit 2; }
wait_for_cluster "$1" "$2" "$3" "${4:-1800}"
