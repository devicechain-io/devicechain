# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Named-test mutations of U2b guards. Original production bytes are restored in finally."""
from pathlib import Path
import json
import subprocess
import xml.etree.ElementTree as ET

BASE = Path(__file__).resolve().parent
MODEL = BASE.parent / "Assets/Harborpulse/Scripts/Model"
CASES = [
    ("tide pins", "TideField.cs", "if (d2 == 0) return levels[i];", "if (d2 == 0) return levels[i] + .1;", "TideFieldPinsQuayInteriorsExactly"),
    ("tide continuity", "TideField.cs", "var w = 1 / (d2 + Epsilon);", "var w = d2 < 400 ? 1e9 : 1 / (d2 + Epsilon);", "TideFieldHalfMetreGridHasNoCentimetreSteps"),
    ("LF normalization", "Director/LayoutDigest.cs", '.Replace("\\r\\n", "\\n")', '.Replace("\\r\\n", "\\r\\n")', "LayoutDigestNormalizesCrLfToLf"),
    ("absent digest", "Director/LayoutDigest.cs", 'reason = "runner reports no layout digest"; return false;', 'reason = "runner reports no layout digest"; return true;', "AbsentRunnerDigestIsMismatchWithReason"),
    ("banner precedence", "Director/HudModel.cs", "if (!digestMatches)", "if (!digestMatches && !stale)", "BannerPrecedenceDigestThenStaleThenControl"),
    ("caption floor", "Director/HudModel.cs", "now - CaptionAtS < 3", "now - CaptionAtS < 1", "HudCaptionCannotEndBeforeThreeSeconds"),
    ("rail cap", "Director/HudModel.cs", "while (rail.Count > 5)", "while (rail.Count > 6)", "HudRailCapsFiveNewestAtBottomWithDeclaredLinksOnly"),
]


def main():
    output = BASE / ".build/u2b/mutants"
    output.mkdir(parents=True, exist_ok=True)
    results = []
    for label, relative, before, after, test in CASES:
        path = MODEL / relative
        original = path.read_bytes()
        source = original.decode("utf-8")
        if source.count(before) != 1:
            raise RuntimeError(f"{label}: expected exactly one production line, got {source.count(before)}")
        try:
            path.write_bytes(source.replace(before, after).encode("utf-8"))
            run = subprocess.run(["powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", str(BASE / "Verify-Director.ps1"), "-Filter", test], cwd=BASE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
            (output / (test + ".log")).write_text(run.stdout, encoding="utf-8")
            trx = BASE / ".build/u2b/results/director.trx"
            tree = ET.parse(trx)
            failed = [e.attrib.get("testName", "") for e in tree.iter() if e.tag.endswith("UnitTestResult") and e.attrib.get("outcome") == "Failed"]
            killed = run.returncode != 0 and any(test in name for name in failed) and "Build succeeded." in run.stdout
            results.append({"guard": label, "test": test, "killed": killed, "failed": failed})
            print(f"{label}: {'KILLED' if killed else 'SURVIVED'} by {test}", flush=True)
            if not killed:
                raise RuntimeError(f"mutation not killed by named test: {label}")
        finally:
            path.write_bytes(original)
            if path.read_bytes() != original:
                raise RuntimeError(f"restore failed: {path}")
    (output / "summary.json").write_text(json.dumps(results, indent=2), encoding="utf-8")


if __name__ == "__main__":
    main()
