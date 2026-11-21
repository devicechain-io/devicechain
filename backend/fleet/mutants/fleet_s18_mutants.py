#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Mutants for the fleet writer-exclusion protocol (spec 10.5), harness name fleet-s18.

Each mutant is ONE exact edit to a source file; the named tests must then FAIL. The baseline is
run first as a no-op control and the whole run is aborted if it is red, because a mutant that
"dies" in a red tree proves nothing. Every file is restored afterwards from a byte copy.

Needs a Postgres: DC_FLEET_PG_DSN. Run from backend/fleet:

    python3 mutants/fleet_s18_mutants.py [name-substring ...]
"""
import os
import shutil
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WS = "writerslot/"

# (name, file, old, new, test regexp, guarantee)
MUTANTS = [
    # --- one manager acts
    ("drop-lease-read", WS + "manager.go",
     "func (m *Manager) requireLease(tx *gorm.DB) error {\n", "func (m *Manager) requireLease(tx *gorm.DB) error {\n\treturn nil\n",
     "TwoManagers|LeaseIsReadForShare|StaleEpoch", "one manager acts"),
    ("ignore-lease-epoch", WS + "manager.go",
     "l.Epoch != m.epoch || ", "",
     "StaleEpoch", "one manager acts"),
    ("lease-read-without-for-share", WS + "manager.go",
     "FROM manager_lease WHERE id = 1 FOR SHARE", "FROM manager_lease WHERE id = 1",
     "LeaseIsReadForShare", "one manager acts"),
    # --- one writer per cluster
    ("drop-slot-for-update", WS + "manager.go",
     "FROM writer_slots WHERE cluster_uid = ? FOR UPDATE", "FROM writer_slots WHERE cluster_uid = ?",
     "ConcurrentAcquire", "one writer per cluster"),
    ("uncertain-is-free", WS + "admission.go",
     "if slot.State != StateFree {", "if slot.State != StateFree && slot.State != StateUncertain {",
     "AcquireRefusals", "one writer per cluster"),
    ("skip-epoch-increment", WS + "admission.go",
     "epoch := slot.Epoch + 1", "epoch := slot.Epoch",
     "HappyPath|ConcurrentAcquire|Recovery", "one writer per cluster"),
    ("ignore-unsettled-operations", WS + "admission.go",
     "if unsettled > 0 {", "if false {",
     "AcquireRefusals", "one writer per cluster"),
    ("done-with-error-is-settled", WS + "admission.go",
     "(error_code IS NULL OR resolution IS NOT NULL)", "true",
     "AcquireRefusals|ReleaseGuards", "one writer per cluster"),
    ("ignore-stale-observation", WS + "admission.go",
     "if !fresh.Present || !fresh.Complete || !fresh.Fresh {", "if false {",
     "AcquireRefusals", "one writer per cluster"),
    # --- no release without both cuts
    ("pod-absence-is-termination", WS + "types.go",
     "len(p.Containers) == 0 {\n\t\treturn false", "len(p.Containers) == 0 {\n\t\treturn true",
     "AllTerminated|ExitNeeds|ExclusionNeedsBothCuts", "no release without both cuts"),
    ("pod-phase-is-termination", WS + "types.go",
     "func (p PodObservation) AllTerminated(podUID string) bool {\n", "func (p PodObservation) AllTerminated(podUID string) bool {\n\tif p.PodUID == podUID && !p.Running {\n\t\treturn true\n\t}\n",
     "AllTerminated|ExitNeeds|ExclusionNeedsBothCuts", "no release without both cuts"),
    ("skip-residual-wait", WS + "proof.go",
     "if !elapsed {", "if false {",
     "ExclusionNeedsBothCuts", "no release without both cuts"),
    ("drop-one-ttl-from-the-sum", WS + "proof.go",
     "run.KsaTokenTTLSec+run.StsTokenTTLSec+run.AccessTokenTTLSec", "run.KsaTokenTTLSec+run.StsTokenTTLSec",
     "ExclusionNeedsBothCuts", "no release without both cuts"),
    ("release-without-the-state-cut", WS + "proof.go",
     "sessions, err := verifyRoleCut(tx, *run.StateRoleName)", "sessions, err := int64(0), error(nil)",
     "ExclusionNeedsBothCuts", "no release without both cuts"),
    ("cloud-credential-accepted-on-pg", WS + "proof.go",
     "if backend != BackendGCS {", "if false {",
     "ExclusionNeedsBothCuts", "no release without both cuts"),
    ("expiry-ignores-premises", WS + "proof.go",
     "if !m.cfg.Premises.ExpiryHolds(run.CredentialMode) {", "if false {",
     "ExclusionNeedsBothCuts", "no release without both cuts"),
    ("expiry-without-refresh-cut", WS + "proof.go",
     "if slot.RefreshCutAt == nil {", "if false {",
     "ExclusionNeedsBothCuts", "no release without both cuts"),
    ("release-with-foreign-lock-holder", WS + "transitions.go",
     [("case LockAbsent, LockForceUnlockedFromExcludedRun:", "case LockAbsent, LockForceUnlockedFromExcludedRun, LockForeign:"),
      ("\t\tcase LockForeign:\n", "\t\tcase LockResolution(\"NEVER\"):\n")], None,
     "Recovery$", "no release without both cuts"),
    ("release-with-the-role-still-there", WS + "transitions.go",
     "if n > 0 {\n\t\treturn transitionRefused(\"the per-run state role", "if false {\n\t\treturn transitionRefused(\"the per-run state role",
     "ReleaseGuards", "no release without both cuts"),
    # --- doubt is not exclusion
    ("restart-doubts-a-healthy-run", WS + "transitions.go",
     "obs.PodUID == *run.PodUID && obs.Running && obs.NodeReady", "obs.PodUID == *run.PodUID && obs.Running && !obs.NodeReady",
     "RestartedManager", "doubt is not exclusion"),
    ("reaffirm-on-a-different-pod", WS + "transitions.go",
     "if run.PodUID == nil || obs.PodUID == \"\" || obs.PodUID != *run.PodUID {", "if run.PodUID == nil {",
     "DoubtAndReaffirm|RestartedManager", "doubt is not exclusion"),
    ("reaffirm-after-the-refresh-cut", WS + "transitions.go",
     "if slot.RefreshCutAt != nil {\n\t\t\treturn transitionRefused(\"a refresh cut", "if false {\n\t\t\treturn transitionRefused(\"a refresh cut",
     "DoubtAndReaffirm", "doubt is not exclusion"),
    # --- recovery is fenced
    ("recover-from-free", WS + "admission.go",
     "if slot.State != StateExcluded {", "if slot.State != StateExcluded && slot.State != StateFree {",
     "Recovery$", "recovery is fenced"),
    ("accept-a-lower-state-serial", WS + "transitions.go",
     "|| spec.Head.Serial < head.Serial)", "|| false)",
     "StateHeadRegression", "recovery is fenced"),
    ("accept-a-changed-lineage", WS + "transitions.go",
     "(head.Lineage != spec.Head.Lineage ||", "(false ||",
     "StateHeadRegression", "recovery is fenced"),
    # --- observe-only
    ("observe-only-admits-change", WS + "admission.go",
     "case id.ManagementMode != Managed && spec.Kind != KindDriftCheck:", "case false:",
     "AcquireRefusals", "observe-only is observe-only"),
    # --- no destroy
    ("destroy-passes-admission", WS + "admission.go",
     "if spec.Kind == KindDestroy {", "if false {",
     "AcquireRefusals", "no destroy in this slice"),
    # --- no import by name alone: each identity element in turn
    ("identity-drop-name", WS + "identity.go",
     "case got.Name != in.CloudName:", "case false:", "CheckClusterIdentity|IdentityCheck", "no import by name alone"),
    ("identity-drop-project", WS + "identity.go",
     "case got.Project != in.Project:", "case false:", "CheckClusterIdentity|IdentityCheck", "no import by name alone"),
    ("identity-drop-location", WS + "identity.go",
     "case got.Location != in.Location:", "case false:", "CheckClusterIdentity|IdentityCheck", "no import by name alone"),
    ("identity-drop-nonce", WS + "identity.go",
     "subtle.ConstantTimeCompare([]byte(got.NonceLabel), []byte(in.Nonce)) != 1:", "subtle.ConstantTimeCompare([]byte(got.NonceLabel), []byte(got.NonceLabel)) != 1:",
     "CheckClusterIdentity|IdentityCheck", "no import by name alone"),
    ("identity-drop-create-time", WS + "identity.go",
     "case in.FirstSubmitAt.IsZero() || got.CreateTime.Before(in.FirstSubmitAt.Add(-defaultImportSkew)):", "case false:",
     "CheckClusterIdentity|IdentityCheck", "no import by name alone"),
    ("identity-drop-resource-id", WS + "identity.go",
     "case in.CloudResourceID != \"\" && got.ResourceID != in.CloudResourceID:", "case false:",
     "CheckClusterIdentity|IdentityCheck", "no import by name alone"),
    ("pool-drop-parent-check", WS + "identity.go",
     "if verifiedClusterID == \"\" || parentResourceID != verifiedClusterID {", "if false {",
     "CheckPoolImport|IdentityCheck", "no import by name alone"),
    ("pool-drop-declared-check", WS + "identity.go",
     "if !in(poolName, declared...) {", "if false {",
     "CheckPoolImport|IdentityCheck", "no import by name alone"),
    # --- intent precedes submission
    ("no-first-submit-at", WS + "admission.go",
     "first_submit_at = COALESCE(first_submit_at, clock_timestamp()),", "first_submit_at = first_submit_at,",
     "HappyPath", "intent precedes submission"),
    # --- fail closed on unknowns (a proceeding default on each release path)
    ("unknown-slot-state-proceeds", WS + "manager.go",
     "if !in(s.State, allStates...) {", "if false {",
     "UnknownValues|AcquireRefusals", "fail closed on unknowns"),
    ("unknown-run-phase-proceeds", WS + "manager.go",
     "if !in(r.Phase, allPhases...) {", "if false {",
     "UnknownValues", "fail closed on unknowns"),
    ("unknown-run-kind-proceeds", WS + "manager.go",
     "if !in(r.Kind, allKinds...) {", "if false {",
     "UnknownValues", "fail closed on unknowns"),
    ("unknown-proof-proceeds", WS + "manager.go",
     "if s.CloudCutProof != nil && !in(*s.CloudCutProof, ProofTermination, ProofExpiry) {", "if false {",
     "UnknownValues", "fail closed on unknowns"),
    ("unknown-lock-resolution-proceeds", WS + "transitions.go",
     "default:\n\t\t\treturn unknown(\"lock resolution\", string(lock))", "default:",
     "Recovery$", "fail closed on unknowns"),
    ("no-label-reads", WS + "identity.go",
     "func (m *Manager) ExecuteImport(context.Context, string, string) error {\n\treturn ErrImportNotImplemented", "func (m *Manager) ExecuteImport(context.Context, string, string) error {\n\treturn nil",
     "FailLoudly|IdentityCheckDecisions", "unimplemented fails loudly"),
]


def run_tests(regex):
    env = dict(os.environ)
    cmd = ["go", "test", "-tags", "fleetpg", "-count=1", "-p", "1", "-run", regex, "./writerslot"]
    r = subprocess.run(cmd, cwd=ROOT, env=env, capture_output=True, text=True)
    return r.returncode, r.stdout + r.stderr


def main():
    if not os.environ.get("DC_FLEET_PG_DSN"):
        sys.exit("DC_FLEET_PG_DSN is required")
    want = sys.argv[1:]
    # The no-op control: the baseline must be green, or no mutant's death means anything.
    rc, out = run_tests(".")
    if rc != 0:
        sys.exit("ABORT: the baseline is RED, mutants prove nothing\n" + out[-2000:])
    print("baseline green")
    survived, killed = [], []
    for name, f, old, new, regex, guarantee in MUTANTS:
        if want and not any(w in name for w in want):
            continue
        path = os.path.join(ROOT, f)
        with open(path, "rb") as fh:
            orig = fh.read()
        text = orig.decode("utf-8")
        # A mutant is one edit, or a list of them (old is then a list of (old, new) pairs and new is None).
        edits = old if isinstance(old, list) else [(old, new)]
        mutated = text
        bad = None
        for o, n in edits:
            if mutated.count(o) != 1:
                bad = (o, mutated.count(o))
                break
            mutated = mutated.replace(o, n, 1)
        if bad:
            print("BAD MUTANT %s: expected exactly one occurrence of %r, found %d" % (name, bad[0], bad[1]))
            survived.append(name + " (did not apply)")
            continue
        try:
            with open(path, "wb") as fh:
                fh.write(mutated.encode("utf-8"))
            rc, out = run_tests(regex)
        finally:
            with open(path, "wb") as fh:
                fh.write(orig)
        if rc == 0:
            survived.append(name)
            print("SURVIVED %-36s [%s]" % (name, guarantee))
        elif "build failed" in out or "[build failed]" in out or "cannot use" in out:
            survived.append(name + " (build failed: not a test kill)")
            print("BUILD    %-36s mutant did not compile" % name)
        else:
            killed.append(name)
            print("killed   %-36s [%s]" % (name, guarantee))
    print("\n%d killed, %d survived/invalid" % (len(killed), len(survived)))
    for s in survived:
        print("  survivor:", s)
    sys.exit(1 if survived else 0)


main()
