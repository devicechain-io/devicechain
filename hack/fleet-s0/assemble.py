#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Fold the candidate gke.json files of Tier K runs into the reviewed premise record.

usage: assemble.py OUT.json RUN_DIR... [--blocked-date YYYY-MM-DD] [--base RECORD.json]
                   [--attempted ID RUN_DIR REASON]...

--attempted records a Tier C experiment that RAN but produced no verdict (its control or its harness
failed) as INCONCLUSIVE with measured.blocked = "ATTEMPTED" and the cause in measured.reason. Its digest
is that run's transcript. It is never a pass, and it is no longer U2: the cause is the attempt's own.

--base carries forward the MEASURED entries of an existing record (typically the checked-in one),
whose run directories are private and not at hand; a run directory's entry for the same ID replaces
it. The base's blocked entries are not carried: they are recomputed below.

Every Tier C experiment that no run measured is recorded INCONCLUSIVE with measured.blocked = "U2": it needs a throwaway
cloud project and the long-lived admin project that arms the reaper, and neither is named. That is a
refusal to measure, never a pass. The digest of a blocked entry is the SHA-256 of its notice text
(there is no transcript), which is stated in the entry itself.
"""
import datetime, hashlib, json, pathlib, sys

BLOCKED = {  # id -> (binds, why)
    "E-a": (["cloud"], "GKE v1 Cluster deletion-protection field: needs the cloud API discovery document under the rehearsed reaper"),
    "E-b": (["cloud"], "IAM deny on clusters.delete: needs a throwaway project"),
    "E-c": (["cloud"], "node-pool deletion authorization: needs a throwaway project"),
    "E-d": (["cloud"], "WIF from a kind pod with uploaded JWKS: needs a throwaway project (and the JWKS fixture is not built)"),
    "E-e": (["cloud"], "deny propagation latency: needs a throwaway project"),
    "E-f": (["cloud"], "H08 token-chain expiry bound (KSA, STS, access token): the STS and access-token lifetimes are cloud-side. "
                       "Only the local half is measured: see E-l (the process survives partition, node delete, PodGC and force-delete)"),
    "E-f2": (["cloud"], "service-account disable semantics: needs a throwaway project"),
    "E-g-gcs": (["tofu", "cloud"], "GCS lock holder after SIGKILL: needs a bucket in a throwaway project"),
    "E-h": (["cloud"], "crash points of a first cluster create: needs a throwaway project (and the crash checkpoints are not built)"),
    "E-i": (["cloud"], "indirect paths from the candidate apply role to workload data: needs a throwaway project"),
    "E-k": (["cloud"], "survivors of a bare cluster destroy: needs a throwaway project"),
}

def main(argv):
    attempted = []
    out, runs, base, blocked_date = argv[1], [], None, datetime.datetime.now(datetime.timezone.utc).date().isoformat()
    args = iter(argv[2:])
    for a in args:
        if a == "--blocked-date":
            blocked_date = next(args)
        elif a == "--base":
            base = next(args)
        elif a == "--attempted":
            attempted.append((next(args), next(args), next(args)))
        else:
            runs.append(a)
    exps, versions = {}, set()
    carried = {}
    if base:
        for p in json.load(open(base))["experiments"]:
            if not p["measured"].get("blocked"):
                carried[p["id"]] = p
                versions.add((p["tofu_version"], p["provider_version"]))
    for run in runs:
        for p in json.load(open(pathlib.Path(run) / "gke.json"))["experiments"]:
            assert p["id"] not in exps, "duplicate " + p["id"]
            exps[p["id"]] = p
            versions.add((p["tofu_version"], p["provider_version"]))
    assert len(versions) == 1, "runs disagree on tool versions: %s" % versions
    tofu, provider = next(iter(versions))
    for id_, run, why in attempted:
        assert id_ in BLOCKED and id_ not in exps, id_ + ": attempted must be a Tier C id with no verdict"
        transcript = (pathlib.Path(run) / "transcript.log").read_bytes()
        when = datetime.datetime.fromtimestamp((pathlib.Path(run) / "transcript.log").stat().st_mtime, datetime.timezone.utc)
        exps[id_] = dict(id=id_, verdict="INCONCLUSIVE", binds=BLOCKED[id_][0],
                         measured=dict(blocked="ATTEMPTED", reason=why), tofu_version=tofu, provider_version=provider,
                         date=when.date().isoformat(), transcript_sha256=hashlib.sha256(transcript).hexdigest())
    for id_, p in carried.items():
        exps.setdefault(id_, p)
    for id_, (binds, why) in BLOCKED.items():
        if id_ in exps:
            continue  # measured by a run (or carried from the base): never also blocked
        notice = "BLOCKED-U2 %s: %s" % (id_, why)
        p = dict(id=id_, verdict="INCONCLUSIVE", binds=binds,
                 measured=dict(blocked="U2", reason=why, digest_covers="the notice text, not a transcript"),
                 tofu_version=tofu, provider_version=provider, date=blocked_date,
                 transcript_sha256=hashlib.sha256(notice.encode()).hexdigest())
        exps[id_] = p
    ordered = [exps[k] for k in sorted(exps)]
    pathlib.Path(out).write_text(json.dumps(dict(schema_version=1, experiments=ordered), indent=2) + "\n", newline="\n")

main(sys.argv)
