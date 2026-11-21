#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
d="$(mktemp -d)"
trap 'rm -rf "$d"' EXIT
mkdir -p "$d/bin"
export S0_FAKE_DIR="$d" S0_FAKE_CASE=good
if command -v cygpath >/dev/null; then export S0_FAKE_DIR="$(cygpath -w "$d")"; fi
if ! python3 -c 'pass' >/dev/null 2>&1; then
  py="$(command -v python)"
  printf '#!/usr/bin/env bash\nexec "%s" "$@"\n' "$py" >"$d/bin/python3"
  chmod +x "$d/bin/python3"
fi
export PATH="$d/bin:$PATH"
for tool in gcloud kubectl psql tofu curl kind docker unshare; do
  printf '#!/usr/bin/env bash\nexec python3 "%s/fake.py" "%s" "$@"\n' "$here" "$tool" >"$d/bin/$tool"
  chmod +x "$d/bin/$tool"
done
fail() { echo "self-test: $*" >&2; cat "$d/out" >&2; exit 1; }
expect_fail() { if "$@" >"$d/out" 2>&1; then fail "unexpected success: $*"; fi; }
export FLEET_S0_ADMIN_PROJECT=s0-admin FLEET_S0_PROJECT=s0-spike FLEET_S0_FOLDER=123
export FLEET_S0_LOCATION=us-central1-a FLEET_S0_SCHEDULER_LOCATION=us-central1
export FLEET_S0_TEST_IDENTITY=test@s0-spike.iam.gserviceaccount.com
export FLEET_S0_CANARY_PROJECT=s0-canary FLEET_S0_POLL_SECONDS=0 FLEET_S0_TIMEOUT_SECONDS=1
export FLEET_S0_DENY_SETTLE_SECONDS=0
export FLEET_S0_PROVIDER_VERSION=6.49.0 FLEET_S0_HEARTBEAT_SECONDS=0
export FLEET_S0_PG_ADMIN_URL=postgres://admin@localhost/s0
export FLEET_S0_UPDATE_ONLY_IDENTITY=update@s0-spike.iam.gserviceaccount.com FLEET_S0_UPDATE_ONLY_ROLE=updateOnly
run() {
  rm -f "$d/deny" "$d/disabled" "$d/role-removed" "$d/revoked" "$d/lock" "$d/node-deleted" "$d/heartbeat" "$d/pg-state" "$d/canary-role" "$d/sessions-dropped" "$d/noinherit"
  local id="$1"; shift
  env FLEET_S0_OUTPUT_DIR="$(mktemp -d "$d/run.XXXXXX")" "$@" bash "$here/run.sh" "$id"
}
unset GOOGLE_APPLICATION_CREDENTIALS
expect_fail bash -c 'source "$1/lib.sh"; create_cluster s0-e-b' _ "$here"
[[ ! -s "$d/calls" ]] || fail 'resource call before preflight'
expect_fail run E-a FLEET_S0_ADMIN_PROJECT=
[[ ! -s "$d/calls" ]] || fail 'resource call with unset admin project'
for bad in lien cluster-exists canary-timeout scheduler-error wrong-role wrong-project; do
  : >"$d/calls"
  expect_fail run E-a S0_FAKE_CASE="$bad"
  if grep -q 'container clusters create' "$d/calls"; then fail "$bad reached experiment creation"; fi
done
for who in other@example.invalid s0-reaper@s0-admin.iam.gserviceaccount.com; do
  : >"$d/calls"
  expect_fail run E-a FLEET_S0_DENIED_PRINCIPAL="$who"
  [[ ! -s "$d/calls" ]] || fail 'bad deny principal reached resource calls'
done
: >"$d/calls"
run E-a >"$d/out" 2>&1 || fail 'valid preflight / E-a'
grep -q 'DELETE_REQUESTED' "$d/out" || fail 'canary fire was not observed'
grep -q 'cloudresourcemanager.googleapis.com/v3/projects/' "$d/calls" || fail 'outer project delete missing'
grep -q 'container.googleapis.com/v1/projects/' "$d/calls" || fail 'inner cluster delete missing'
grep -q -- '--oauth-service-account-email=s0-reaper@s0-admin.iam.gserviceaccount.com' "$d/calls" || fail 'wrong outer identity'
grep -q 'E-a HOLDS' "$d/out" || fail 'E-a result missing'
expect_fail run E-a S0_FAKE_CASE=field-present
grep -q 'E-a FAILS' "$d/out" || fail 'deletion protection field did not stop experiment'
case "${1:-all}" in
  all|run|reaper|lib|E-a) ;;
  E-d|E-f|E-h|E-i)
    expect_fail run "$1"
    grep -q 'not implemented:' "$d/out" || fail 'unsupported probe fabricated a verdict'
    ;;
  *)
    : >"$d/calls"
    run "$1" >"$d/out" 2>&1 || fail "$1 positive control"
    case "$1" in
      E-b) bad=deny-ineffective ;;
      E-c) bad=role-ineffective ;;
      E-e) bad=deny-ineffective ;;
      E-f2) bad=token-server-error ;;
      E-g) bad=cross-schema-open ;; # pg observations have no mandatory behavioural verdict.
      E-j) bad=cross-schema-open ;;
      E-k) bad=no-disk ;;
      E-l) bad=forged-termination ;;
      *) fail "unknown self-test id" ;;
    esac
    if [[ $1 == E-g ]]; then
      grep -q 'E-g-pg HOLDS' "$d/out" || fail 'pg result absent'
      # Exercise the GCS half separately, with both readable-lock and missing-lock controls.
      env FLEET_S0_OUTPUT_DIR="$(mktemp -d "$d/run.XXXXXX")" bash "$here/run.sh" --backend=gcs E-g >"$d/out" 2>&1 || fail 'gcs positive control'
      expect_fail env S0_FAKE_CASE=lock-missing FLEET_S0_OUTPUT_DIR="$(mktemp -d "$d/run.XXXXXX")" bash "$here/run.sh" --backend=gcs E-g
    else
      expect_fail run "$1" S0_FAKE_CASE="$bad"
      if [[ $1 == E-j ]]; then
        # Each control must be able to fail on its own: an unenforced key, and an inherit-less role that still works.
        expect_fail run E-j S0_FAKE_CASE=encryption-ineffective
        expect_fail run E-j S0_FAKE_CASE=noinherit-ineffective
      fi
    fi
    ;;
esac
# Isolated mutation, never edit the working implementation.
mkdir "$d/mutant"
cp "$here/lib.sh" "$d/mutant/lib.sh"
python3 - "$d/mutant/lib.sh" <<'PY'
import pathlib, sys
p = pathlib.Path(sys.argv[1])
s = p.read_text()
old = '[[ ${S0_PREFLIGHT:-} == "$$" ]] || die "reaper preflight has not passed in this run"'
assert s.count(old) == 1, 'preflight mutant did not apply exactly once'
p.write_text(s.replace(old, ': # mutant: skip preflight'), newline='')
PY
: >"$d/calls"
bash -c 'source "$1/lib.sh"; create_cluster s0-e-b' _ "$d/mutant" >"$d/out" 2>&1 || true
grep -q 'container clusters create' "$d/calls" || fail 'preflight mutant did not reach creation'
echo "self-test ${1:-all}: ok; preflight-bypass mutant KILLED (unauthorized creation detected)"
