#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Instrumentation helpers for the Tier C premises E-d, E-f, E-h and E-i.

The probes in instrument.sh orchestrate the CLIs; everything here is either a pure verdict function
over what the probes recorded, or a small local primitive (a forged token, a JWKS check). Verdicts
are computed here, never in shell, so the self-test can drive each one with scripted inputs.

Every verdict function returns {"verdict": "HOLDS"|"FAILS", "measured": {...}}. It RAISES instead of
returning a verdict when its own control is ineffective: a measurement whose negative control could
not fail proves nothing, and the record format has no way to say so except by having no entry.

usage: instrument.py jwt-forge ISS AUD SUB | jwks-check FILE | kubeconfig ENDPOINT CA (token on stdin) |
       jwks-verdict FILE | chain-verdict FILE | crash-verdict FILE | exposure-verdict FILE
"""
import base64
import json
import os
import re
import subprocess
import sys
import tempfile
import time

PRIVATE_JWK_MEMBERS = {"d", "p", "q", "dp", "dq", "qi", "k"}


class Inconclusive(Exception):
    """The measurement's own control did not behave: no verdict may be recorded."""


def b64u(raw):
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def forge(iss, aud, sub):
    """An RS256 token signed by a key nobody registered: a wrong issuer must be refused by signature or by name."""
    with tempfile.TemporaryDirectory() as d:
        key = os.path.join(d, "k.pem")
        subprocess.run(["openssl", "genrsa", "-out", key, "2048"], check=True, capture_output=True)
        now = int(time.time())
        head = b64u(json.dumps({"alg": "RS256", "typ": "JWT", "kid": "forged"}).encode())
        body = b64u(json.dumps({"iss": iss, "aud": aud, "sub": sub, "iat": now, "exp": now + 600}).encode())
        sig = subprocess.run(["openssl", "dgst", "-sha256", "-sign", key], input=(head + "." + body).encode(),
                             check=True, capture_output=True).stdout
    return head + "." + body + "." + b64u(sig)


def jwks_check(path):
    """The publisher's own guard: public RSA keys only, at least one, every one with a kid."""
    doc = json.load(open(path))
    keys = doc.get("keys")
    assert isinstance(keys, list) and keys, "JWKS has no keys"
    for k in keys:
        assert k.get("kty") == "RSA" and k.get("kid") and k.get("n") and k.get("e"), "JWKS key is not a public RSA key with a kid"
        leaked = PRIVATE_JWK_MEMBERS & set(k)
        assert not leaked, "JWKS carries private key members: " + ",".join(sorted(leaked))
    return dict(key_ids=sorted(k["kid"] for k in keys), key_count=len(keys))


def jwks_verdict(cfg):
    """E-d. cfg: {jwks, baseline, controls:{name:http}, recreated:{http}}.

    baseline: the bound subject's exchange, which must be accepted or there is nothing to refuse.
    controls: another KSA subject, a wrong audience, a wrong issuer; each must be refused.
    recreated: a deleted and recreated same-name KSA; RECORDED, never a verdict (the expectation is
    that it is accepted, which tells S5 to pin more than the name).
    """
    if cfg["baseline"] != 200:
        raise Inconclusive("the bound subject's exchange was not accepted (HTTP %s): the refusals prove nothing" % cfg["baseline"])
    wanted = {"other_subject", "wrong_audience", "wrong_issuer"}
    if set(cfg["controls"]) != wanted:
        raise Inconclusive("controls must be exactly " + ",".join(sorted(wanted)))
    accepted = sorted(n for n, c in cfg["controls"].items() if c == 200)
    other = sorted(n for n, c in cfg["controls"].items() if c not in (200, 400, 401, 403))
    if other:
        raise Inconclusive("control answered with a non-refusal, non-acceptance status: " + ",".join(other))
    rec = cfg["recreated"]
    measured = dict(jwks=cfg["jwks"], baseline_http=cfg["baseline"], control_http=cfg["controls"],
                    accepted_controls=accepted, recreated_same_name_ksa_http=rec,
                    recreated_ksa_accepted=rec == 200,
                    consequence=("a recreated KSA with the same name is indistinguishable by subject: S5 must pin more than the name "
                                 "or document the namespace admin as trusted") if rec == 200 else
                    "a recreated KSA with the same name was refused")
    return dict(verdict="FAILS" if accepted else "HOLDS", measured=measured)


def parse_chain_log(text):
    """Runner log: '<epoch> <http>' request lines and 'L sts|access <seconds>' lifetime lines."""
    series, life = [], {}
    for line in text.splitlines():
        f = line.split()
        if len(f) == 3 and f[0] == "L" and f[2].isdigit():
            life[f[1]] = int(f[2])
        elif len(f) == 2 and f[0].isdigit() and f[1].isdigit():
            series.append([int(f[0]), int(f[1])])
    return series, life


def chain_verdict(cfg):
    """E-f. cfg: {cut, ksa_ttl_s, sts_ttl_s, access_ttl_s, skew_s, partitioned:[[ts,code]], control:[[ts,code]]}.

    The bound is COMPUTED from the three configured lifetimes; the measurement validates the formula
    and is never itself the bound. A refusal is a 401 or a 403 after the cut; a 5xx is not a refusal.
    The control is the same chain left uncut: it must still be authorized at or after the bound.
    """
    if "partitioned_log" in cfg:
        cfg = dict(cfg)
        cfg["partitioned"], life = parse_chain_log(cfg["partitioned_log"])
        cfg["control"], _ = parse_chain_log(cfg["control_log"])
        if "sts" not in life or "access" not in life:
            raise Inconclusive("the runner never recorded the STS and access-token lifetimes")
        cfg["sts_ttl_s"], cfg["access_ttl_s"] = life["sts"], life["access"]
    cut = cfg["cut"]
    bound = cfg["ksa_ttl_s"] + cfg["sts_ttl_s"] + cfg["access_ttl_s"] + cfg["skew_s"]
    part = [(t, c) for t, c in cfg["partitioned"] if t >= cut]
    ctl = cfg["control"]
    if not any(t < cut and c == 200 for t, c in cfg["partitioned"]):
        raise Inconclusive("the runner was not authorized before the cut")
    if not any(t >= cut + bound and c == 200 for t, c in ctl):
        raise Inconclusive("the uncut control was not observed authorized at or after the bound: a refusal would prove nothing")
    refusals = [t for t, c in part if c in (401, 403)]
    first = min(refusals) - cut if refusals else None
    last_seen = max((t for t, _ in part), default=cut) - cut
    if first is None and last_seen < bound:
        raise Inconclusive("the partitioned runner was observed for %ds, less than the bound %ds, with no refusal" % (last_seen, bound))
    ok = first is not None and first <= bound
    measured = dict(credential_mode="kind_jwks", ksa_ttl_s=cfg["ksa_ttl_s"], sts_ttl_s=cfg["sts_ttl_s"],
                    access_ttl_s=cfg["access_ttl_s"], skew_margin_s=cfg["skew_s"], computed_bound_s=bound,
                    first_refusal_after_cut_s=first, observed_until_s=last_seen, control_authorized_at_or_after_bound=True,
                    process_survived_cut=True)
    return dict(verdict="HOLDS" if ok else "FAILS", measured=measured)


def crash_verdict(cfg):
    """E-h. cfg: {points:[...], failed_create:{...}, deletion_protection:{...}, id_stability:{...}}.

    A point records what state held, the next plain plan, the next plain apply, and the import plan.
    HOLDS requires, at every point: the plan succeeded; a cluster absent from state yields a plan that
    proposes a create and an apply that fails ALREADY_EXISTS; and no point ever left a second cluster.
    The provider-deletes-on-failed-create observation and the API id behaviour are RECORDED findings:
    they decide what S2 and RECOVER must expect, not whether the harness worked.
    """
    pts = cfg["points"]
    if [p["point"] for p in pts] != [1, 2, 3, 4, 5]:
        raise Inconclusive("E-h needs exactly the five kill points, in order")
    problems = []
    for p in pts:
        if not p["killed_mid_apply"]:
            raise Inconclusive("point %d: the writer was not killed mid-apply (it had already finished or never started)" % p["point"])
        if not p["plan_succeeded"]:
            problems.append("point %d: plain plan failed" % p["point"])
        if p["cluster_in_state"]:
            continue
        if not p["plan_proposes_create"]:
            problems.append("point %d: plan after the crash does not propose the create" % p["point"])
        if not p["apply_failed_already_exists"]:
            problems.append("point %d: plain apply did not fail ALREADY_EXISTS" % p["point"])
        if p["clusters_with_name"] != 1:
            problems.append("point %d: %d clusters carry the name after the retry" % (p["point"], p["clusters_with_name"]))
    dp = cfg["deletion_protection"]
    if not dp["control_deleted"]:
        raise Inconclusive("the orphan destroy with protection off did not delete the cluster: the refusal proves nothing")
    if not dp["refused_with_protection"]:
        problems.append("an orphan destroy with deletion_protection=true in state was not refused")
    measured = dict(points=pts, failed_create=cfg["failed_create"], deletion_protection=dp,
                    id_stability=cfg["id_stability"], problems=problems)
    return dict(verdict="FAILS" if problems else "HOLDS", measured=measured)


# GKE refuses actAs of a service account the caller may not use with HTTP 400 "does not have access to
# service account" (the first real run of E-i found it): a refusal, though not spelled as a 403.
REFUSAL = re.compile(r"PERMISSION_DENIED|Forbidden|403|does not have permission|Required '[a-z.]+' permission"
                     r"|does not have access to service account", re.I)


def classify(exit_code, output):
    if exit_code == 0:
        return "succeeded"
    return "refused" if REFUSAL.search(output or "") else "invalid"


def exposure_verdict(cfg):
    """E-i. cfg: {identity, attempts:[{id, klass, assigned_control, subject:{exit,output}, control:{exit,output}}]}.

    klass 'refuse': IAM must refuse the apply identity. klass 'must_succeed': a calibration the role must pass
    (the designated node identity), or its refusals say nothing. klass 'open_by_design': an attempt IAM cannot
    separate from clusters.update, recorded open and assigned to a named control. The control is the
    same command as the invoking operator, and it must SUCCEED: a refusal of a command that could not
    have worked proves nothing.
    """
    rows, bad = [], []
    for a in cfg["attempts"]:
        control = classify(a["control"]["exit"], a["control"]["output"])
        if control != "succeeded":
            raise Inconclusive("attempt %s: the operator control %s, so the attempt is not well formed" % (a["id"], control))
        subject = classify(a["subject"]["exit"], a["subject"]["output"])
        if subject == "invalid":
            raise Inconclusive("attempt %s: the apply identity failed for a reason that is not a permission refusal" % a["id"])
        if a["klass"] == "must_succeed" and subject != "succeeded":
            raise Inconclusive("attempt %s: the apply role cannot do the work it exists for, so its refusals are not evidence" % a["id"])
        if a["klass"] == "refuse" and subject == "succeeded":
            bad.append(a["id"])
        if a["klass"] == "open_by_design" and not a.get("assigned_control"):
            raise Inconclusive("attempt %s: an open path must name its control" % a["id"])
        rows.append(dict(id=a["id"], klass=a["klass"], apply_identity=subject, operator_control=control,
                         assigned_control=a.get("assigned_control")))
    measured = dict(apply_identity=cfg["identity"], attempts=rows, open_paths=bad,
                    open_by_design=[r["id"] for r in rows if r["klass"] == "open_by_design" and r["apply_identity"] == "succeeded"])
    return dict(verdict="FAILS" if bad else "HOLDS", measured=measured)


def kubeconfig(endpoint, ca_data):
    """A kubeconfig for ONE bearer token read from stdin, so the token is never on a command line."""
    token = sys.stdin.read().strip()
    assert token, "no token on stdin"
    return json.dumps(dict(apiVersion="v1", kind="Config", **{"current-context": "s0"},
        clusters=[dict(name="s0", cluster={"server": "https://" + endpoint, "certificate-authority-data": ca_data})],
        users=[dict(name="s0", user=dict(token=token))],
        contexts=[dict(name="s0", context=dict(cluster="s0", user="s0"))]))


def main(argv):
    cmd = argv[1]
    if cmd == "kubeconfig":
        print(kubeconfig(argv[2], argv[3]))
        return 0
    if cmd == "jwt-forge":
        print(forge(*argv[2:5]))
        return 0
    if cmd == "jwks-check":
        print(json.dumps(jwks_check(argv[2])))
        return 0
    fn = dict(**{"jwks-verdict": jwks_verdict, "chain-verdict": chain_verdict, "crash-verdict": crash_verdict,
                 "exposure-verdict": exposure_verdict}).get(cmd)
    if fn is None:
        print("unknown subcommand " + cmd, file=sys.stderr)
        return 2
    try:
        print(json.dumps(fn(json.load(open(argv[2])))))
    except Inconclusive as e:
        # No verdict and a distinct status: the caller must record nothing, never a pass.
        print("fleet-s0 ERROR[INCONCLUSIVE]: " + str(e), file=sys.stderr)
        return 3
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
