#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
d="$(mktemp -d)"
trap 'rm -rf "$d"' EXIT
mkdir -p "$d/bin"
export S0_FAKE_DIR="$d" S0_FAKE_CASE=good
if command -v cygpath >/dev/null; then
  S0_FAKE_DIR="$(cygpath -w "$d")"
  export S0_FAKE_DIR
fi
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
export FLEET_S0_DENY_SETTLE_SECONDS=0 FLEET_S0_IAM_PROPAGATION_SECONDS=1
export FLEET_S0_PROVIDER_VERSION=6.49.0 FLEET_S0_HEARTBEAT_SECONDS=0
export FLEET_S0_PG_ADMIN_URL=postgres://admin@localhost/s0
export FLEET_S0_APPLY_IDENTITY=apply@s0-spike.iam.gserviceaccount.com FLEET_S0_NODE_IDENTITY=node@s0-spike.iam.gserviceaccount.com
export FLEET_S0_OTHER_IDENTITY=other@s0-spike.iam.gserviceaccount.com FLEET_S0_PARTITION_SETTLE_SECONDS=0 FLEET_S0_FAILED_CREATE_SETTLE_SECONDS=0
export FLEET_S0_UPDATE_ONLY_IDENTITY=update@s0-spike.iam.gserviceaccount.com FLEET_S0_UPDATE_ONLY_ROLE=updateOnly
run() {
  rm -f "$d/deny" "$d/disabled" "$d/role-removed" "$d/revoked" "$d/lock" "$d/node-deleted" "$d/heartbeat" "$d/pg-state" "$d/canary-role" "$d/sessions-dropped" "$d/noinherit" "$d/cutat" "$d/clusters" "$d/ops" "$d/idseq" "$d/protect" "$d"/id-*
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
    # Verdict functions first: fast, no CLI. Every verdict must be reachable in all of its values.
    python3 - "$here" "$1" <<'PY' || fail "$1 verdict function"
import sys
sys.path.insert(0, sys.argv[1])
import instrument as I
which = sys.argv[2]
def raises(fn, cfg):
    try:
        fn(cfg)
    except I.Inconclusive:
        return True
    return False
if which == "E-d":
    ok = dict(jwks={}, baseline=200, recreated=200, controls=dict(other_subject=403, wrong_audience=400, wrong_issuer=400))
    assert I.jwks_verdict(ok)["verdict"] == "HOLDS"
    assert I.jwks_verdict(ok)["measured"]["recreated_ksa_accepted"] is True
    for k in ok["controls"]:
        bad = dict(ok, controls=dict(ok["controls"], **{k: 200}))
        assert I.jwks_verdict(bad)["verdict"] == "FAILS", k
    assert raises(I.jwks_verdict, dict(ok, baseline=403)), "a refused baseline must not yield a verdict"
    assert raises(I.jwks_verdict, dict(ok, controls=dict(ok["controls"], wrong_issuer=503))), "a 503 is not a refusal"
    pub = {"keys": [{"kty": "RSA", "kid": "k", "n": "a", "e": "AQAB"}]}
    import json, tempfile, os
    f = tempfile.NamedTemporaryFile("w", suffix=".json", delete=False); json.dump(pub, f); f.close()
    I.jwks_check(f.name)
    pub["keys"][0]["d"] = "x"
    f = tempfile.NamedTemporaryFile("w", suffix=".json", delete=False); json.dump(pub, f); f.close()
    try:
        I.jwks_check(f.name); raise SystemExit("a private JWK member was published")
    except AssertionError:
        pass
    assert I.forge("https://x.invalid", "a", "s").count(".") == 2
if which == "E-f":
    def log(refuse_at, ctl_ok=True, until=6000):
        part = "L sts 3600\nL access 600\n" + "\n".join("%d %d" % (1000 + o, 403 if refuse_at is not None and o >= refuse_at else 200) for o in range(-30, until, 300))
        ctl = "\n".join("%d %d" % (1000 + o, 200 if ctl_ok or o < 100 else 403) for o in range(-30, 6000, 300))
        return dict(cut=1000, ksa_ttl_s=600, skew_s=60, partitioned_log=part, control_log=ctl)
    assert I.chain_verdict(log(2000))["verdict"] == "HOLDS"
    assert I.chain_verdict(log(2000))["measured"]["computed_bound_s"] == 600 + 3600 + 600 + 60
    assert I.chain_verdict(log(5400))["verdict"] == "FAILS", "a refusal after the computed bound"
    assert I.chain_verdict(log(None))["verdict"] == "FAILS", "no refusal at all"
    assert raises(I.chain_verdict, log(2000, ctl_ok=False)), "a dead control must not yield a verdict"
    assert raises(I.chain_verdict, log(None, until=1000)), "too short an observation must not yield a verdict"
if which == "E-h":
    pt = lambda n, st=False: dict(point=n, killed_mid_apply=True, cluster_in_state=st, plan_succeeded=True, plan_proposes_create=not st,
                                  apply_failed_already_exists=not st, clusters_with_name=1)
    ok = dict(points=[pt(1), pt(2), pt(3, True), pt(4, True), pt(5, True)], failed_create={}, id_stability={},
              deletion_protection=dict(refused_with_protection=True, control_deleted=True))
    assert I.crash_verdict(ok)["verdict"] == "HOLDS"
    dup = dict(ok, points=[dict(pt(1), clusters_with_name=2)] + ok["points"][1:])
    assert I.crash_verdict(dup)["verdict"] == "FAILS", "a duplicate cluster"
    noerr = dict(ok, points=[dict(pt(1), apply_failed_already_exists=False)] + ok["points"][1:])
    assert I.crash_verdict(noerr)["verdict"] == "FAILS", "a retry that did not fail ALREADY_EXISTS"
    assert I.crash_verdict(dict(ok, deletion_protection=dict(refused_with_protection=False, control_deleted=True)))["verdict"] == "FAILS"
    assert raises(I.crash_verdict, dict(ok, deletion_protection=dict(refused_with_protection=True, control_deleted=False))), "control"
    assert raises(I.crash_verdict, dict(ok, points=[dict(pt(1), killed_mid_apply=False)] + ok["points"][1:])), "not killed mid-apply"
    assert raises(I.crash_verdict, dict(ok, points=ok["points"][:4])), "four points"
if which == "E-i":
    a = lambda i, k, s, c=0, ctl="x": dict(id=i, klass=k, assigned_control=ctl if k == "open_by_design" else None,
                                          subject=dict(exit=s, output="PERMISSION_DENIED" if s else ""), control=dict(exit=c, output=""))
    ok = dict(identity="a@b", attempts=[a("snap", "refuse", 1), a("node", "must_succeed", 0), a("abac", "open_by_design", 0)])
    assert I.exposure_verdict(ok)["verdict"] == "HOLDS"
    assert I.exposure_verdict(ok)["measured"]["open_by_design"] == ["abac"]
    open_ = dict(ok, attempts=[a("snap", "refuse", 0)] + ok["attempts"][1:])
    assert I.exposure_verdict(open_)["verdict"] == "FAILS" and I.exposure_verdict(open_)["measured"]["open_paths"] == ["snap"]
    assert raises(I.exposure_verdict, dict(ok, attempts=[a("snap", "refuse", 1, 1)] + ok["attempts"][1:])), "control refused"
    assert raises(I.exposure_verdict, dict(ok, attempts=[a("node", "must_succeed", 1)])), "role too narrow"
    gke = a("other", "refuse", 1); gke["subject"]["output"] = 'ResponseError: code=400, message=The user does not have access to service account "o@p.iam.gserviceaccount.com".'
    assert I.exposure_verdict(dict(ok, attempts=[gke]))["verdict"] == "HOLDS", "GKE's actAs refusal is a refusal"
    bad = a("snap", "refuse", 1); bad["subject"]["output"] = "NOT_FOUND"
    assert raises(I.exposure_verdict, dict(ok, attempts=[bad])), "a failure that is not a permission refusal"
    assert raises(I.exposure_verdict, dict(ok, attempts=[a("abac", "open_by_design", 0, ctl="")])), "an open path with no control"
PY
    : >"$d/calls"
    run "$1" >"$d/out" 2>&1 || fail "$1 positive control"
    grep -q "$1 HOLDS" "$d/out" || fail "$1 produced no HOLDS verdict"
    if [[ $1 == E-h ]]; then
      # The import into a copy of the state is a RECORDED finding, so no verdict guards it: check it here.
      newest=""
      for r in "$d"/run.*; do if [[ -z $newest || $r -nt $newest ]]; then newest=$r; fi; done
      python3 -c 'import json,sys; r=json.loads(open(sys.argv[1]).read().splitlines()[0]); assert all(p["import_succeeded"] for p in r["measured"]["points"]), "import never succeeded"' "$newest/results.jsonl" || fail "E-h import into a copy of the state did not run"
    fi
    # Each scripted defect must turn THIS probe red with a FAILS verdict.
    case "$1" in
      E-d) fails=(wif-aud-open wif-iss-open wif-subject-open); inconclusive=(wif-baseline-refused jwks-private) ;;
      E-f) fails=(late-refusal no-refusal); inconclusive=(ctl-dead) ;;
      E-h) fails=(dup-create protect-ineffective); inconclusive=(dp-control-ineffective) ;;
      E-i) fails=(apply-open); inconclusive=(control-broken apply-narrow) ;;
    esac
    for bad in "${fails[@]}"; do
      expect_fail run "$1" S0_FAKE_CASE="$bad"
      grep -q "$1 FAILS" "$d/out" || fail "$1 under $bad did not record FAILS"
    done
    # A control that could not fail leaves NO verdict: neither HOLDS nor FAILS, and a non-zero exit.
    for bad in "${inconclusive[@]}"; do
      expect_fail run "$1" S0_FAKE_CASE="$bad"
      if grep -Eq "$1 (HOLDS|FAILS)" "$d/out"; then fail "$1 under $bad recorded a verdict although its control was ineffective"; fi
    done
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
