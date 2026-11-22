#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Fold the candidate gke.json files of Tier K runs into the reviewed premise record.

usage: assemble.py OUT.json RUN_DIR... [--blocked-date YYYY-MM-DD]

Every Tier C experiment is recorded INCONCLUSIVE with measured.blocked = "U2": it needs a throwaway
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
    out, runs, blocked_date = argv[1], [], datetime.datetime.now(datetime.timezone.utc).date().isoformat()
    args = iter(argv[2:])
    for a in args:
        if a == "--blocked-date":
            blocked_date = next(args)
        else:
            runs.append(a)
    exps, versions = {}, set()
    for run in runs:
        for p in json.load(open(pathlib.Path(run) / "gke.json"))["experiments"]:
            assert p["id"] not in exps, "duplicate " + p["id"]
            exps[p["id"]] = p
            versions.add((p["tofu_version"], p["provider_version"]))
    assert len(versions) == 1, "runs disagree on tool versions: %s" % versions
    tofu, provider = next(iter(versions))
    for id_, (binds, why) in BLOCKED.items():
        assert id_ not in exps, id_ + " was measured; it must not also be blocked"
        notice = "BLOCKED-U2 %s: %s" % (id_, why)
        p = dict(id=id_, verdict="INCONCLUSIVE", binds=binds,
                 measured=dict(blocked="U2", reason=why, digest_covers="the notice text, not a transcript"),
                 tofu_version=tofu, provider_version=provider, date=blocked_date,
                 transcript_sha256=hashlib.sha256(notice.encode()).hexdigest())
        exps[id_] = p
    ordered = [exps[k] for k in sorted(exps)]
    pathlib.Path(out).write_text(json.dumps(dict(schema_version=1, experiments=ordered), indent=2) + "\n", newline="\n")

main(sys.argv)
