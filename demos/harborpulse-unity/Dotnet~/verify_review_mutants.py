# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""U2b review guards: green baseline, named failing tests, exact byte restoration, green final gate."""
from pathlib import Path
import hashlib
import json
import os
import subprocess
import sys
import xml.etree.ElementTree as ET

BASE = Path(__file__).resolve().parent
MODEL = BASE.parent / "Assets/Harborpulse/Scripts/Model/Director"
OUT = BASE / ".build/u2b/review-mutants"
CASES = [
    ("1 complete terminal children", MODEL / "DirectorObserver.cs", "if (!countsComplete)", "if (!countsComplete && targets.Length < 0)", "TerminalWithoutCompleteChildrenNeverInventsCounts"),
    ("3 retain every measurement", MODEL / "DirectorReadSnapshot.cs", "records.Add(measurement);", "records.Clear(); records.Add(measurement);", "MeasurementTapRetainsIntermediateAboveFloatAndRecording"),
    ("4 lifecycle covers own instant", MODEL / "DirectorObserver.cs", "h.Read.AsOf >= at && (", "h.Read.AsOf <= input.Now && (", "ReorderedCycleRecordsDeferUntilAlarmReadCoversOwnInstant"),
    ("5 snapshots cannot order", MODEL / "Evidence.cs", "c.Trigger.HasOccurrence && o.HasOccurrence &&", "", "SnapshotStatesCannotManufactureOccurrenceOrderingOrArtifactHistory"),
    ("8 backend suppression enum", MODEL / "DirectorObserver.cs", 'S(target, "type") != "DEVICE"', 'S(target, "type") != "device"', "SuppressionBackendEnumsProducePlatformWindow"),
    ("12 C# final caption boundary", MODEL / "DirectorLogCheck.cs", "Close(eof);", "Close(eof + 3);", "CaptionCheckerRejectsFinalShortCaptionAtEofIncludingTitle"),
    ("12 Python final caption boundary", BASE / "check_director_log.py", "close(eof)", "close(eof + 3)", "test_final_caption_eof_and_title"),

    ("2 recorded wording", BASE.parent / "Assets/Harborpulse/Resources/storyboard.json", '"No notification recorded",', '"No notification happened",', "PageTwoClaimsOnlyRecordedAbsenceAndKeepsAuthoredLaw"),
    ("6 empty membership replaces state", MODEL / "DirectorObserver.cs", 'foreach (var h in names["membership"].Now)', 'foreach (var h in names["membership"].Now.Where(x => x.Rows.Length > 0))', "EmptyMembershipReplacesExpectedSetAndAllQuaysControlMap"),
    ("10 persistent recording problem", MODEL / "LiveReadiness.cs", 'Problem = "recording IO: " + e.Message;', 'Problem = e.Message.Substring(0, 0);', "RecordingFailureSurvivesDigestChecksUntilEstablished"),
    ("11 frozen opportunity deadline", MODEL / "Scheduler.cs", 'opportunity.DeadlineS += now - staleAtS;', 'opportunity.DeadlineS += 0;', "SixtySecondReconnectRetainsFrozenCaptionOpportunity"),
]
if "--remaining" in sys.argv:
    CASES = CASES[-4:]
    OUT = OUT / "remaining"


def run_gate(label, test=None):
    command = ["powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", str(BASE / "Verify-Director.ps1")]
    if test:
        command += ["-Filter", test]
    env = os.environ.copy()
    env.pop("HARBORPULSE_UPDATE_FIXTURES", None)
    run = subprocess.run(command, cwd=BASE, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    (OUT / (label + ".log")).write_text(run.stdout, encoding="utf-8")
    return run


def main():
    OUT.mkdir(parents=True, exist_ok=True)
    results = []
    originals = {path: path.read_bytes() for _, path, *_ in CASES}
    try:
        baseline = run_gate("baseline")
        if baseline.returncode:
            raise RuntimeError("baseline must pass")
        for index, (label, path, before, after, test) in enumerate(CASES, 1):
            original = originals[path]
            source = original.decode("utf-8")
            expected = 2 if label.startswith("5 ") else 1
            if source.count(before) != expected:
                raise RuntimeError(f"{label}: expected {expected} guard expressions")
            try:
                path.write_bytes(source.replace(before, after).encode("utf-8"))
                if path.suffix == ".py":
                    # Do not reuse a timestamp-based .pyc while restoring equal-size source files.
                    cache = BASE / "__pycache__"
                    for pyc in cache.glob("check_director_log.*.pyc"):
                        pyc.unlink()
                    run = subprocess.run(["python", "-B", "-m", "unittest", "test_check_director_log.CaptionLogTests." + test], cwd=BASE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
                    (OUT / f"{index}.log").write_text(run.stdout, encoding="utf-8")
                    failed = [test] if "FAIL: " + test in run.stdout else []
                    killed = run.returncode != 0 and bool(failed)
                else:
                    trx = BASE / ".build/u2b/results/director.trx"
                    if trx.exists():
                        trx.unlink()
                    run = run_gate(str(index), test)
                    rows = list(ET.parse(trx).iter()) if trx.exists() else []
                    selected = [e for e in rows if e.tag.endswith("UnitTestResult") and test in e.get("testName", "")]
                    failed = [e.get("testName") for e in selected if e.get("outcome") == "Failed"]
                    killed = run.returncode != 0 and bool(failed) and "Build succeeded." in run.stdout
                    if trx.exists():
                        (OUT / f"{index}.trx").write_bytes(trx.read_bytes())
                results.append({"guard": label, "test": test, "killed": killed, "failed": failed,
                    "restoredSha256": hashlib.sha256(original).hexdigest()})
                print(f"{label}: {'KILLED' if killed else 'SURVIVED'} by {test}", flush=True)
                if not killed:
                    raise RuntimeError("named test did not kill " + label)
            finally:
                path.write_bytes(original)
                assert path.read_bytes() == original, path
    finally:
        for path, original in originals.items():
            path.write_bytes(original)
            assert path.read_bytes() == original, path
        (OUT / "summary.json").write_text(json.dumps(results, indent=2), encoding="utf-8")
        final = run_gate("restored-final")
        if final.returncode:
            raise RuntimeError("restored full gate failed")
    print("All requested guards killed; original bytes restored; complete final gate passed.", flush=True)


if __name__ == "__main__":
    main()
