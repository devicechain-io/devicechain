# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

"""Run only after Verify-Director.ps1. Always restore production bytes, including on failure."""
import json
import os
from pathlib import Path
import subprocess
import xml.etree.ElementTree as ET

HERE = Path(__file__).resolve().parent
SOURCE = HERE.parent / 'Assets/Harborpulse/Scripts/Model/Director'
BUILD = HERE / '.build'
SDK = Path(os.environ['USERPROFILE']) / '.dotnet/dotnet.exe'
if not SDK.exists():
    SDK = Path('C:/Users/Derek/.dotnet/dotnet.exe')

MUTANTS = [
    ('M1', 'PresentationMap.cs', 'known.Count != anchors.Count || known.Any(t => !anchors.ContainsKey(t)) || anchors.Keys.Any(t => !known.Contains(t))', 'known.Count < 0', 'M1_BijectionByToken'),
    ('M2', 'PresentationMap.cs', 'anchors[d.Token] = Place(d, quay);', 'anchors[d.Token] = Place(d, d.Quay);', 'M2_PlatformMembershipMovesMismatches'),
    ('M3', 'PresentationMap.cs', '.OrderBy(d => anchors[d.Token].X)', '.OrderBy(d => d.Position.Value.Lon)', 'M3_LampLongitudeOrderAndSwapControl'),
    ('M4', 'PresentationMap.cs', 'Height(double metres) => metres;', 'Height(double metres) => metres * 2;', 'M4_IdentityAndHeldMissingInvalid'),
    ('M5', 'PresentationMap.cs', 'PresentationMap.Cross(DrawA, DrawB, DrawC) <= 1e-12', 'PresentationMap.Cross(DrawA, DrawB, DrawC) >= 1e-12', 'M5_NoFlipsAndDecalsAreMappedRings'),
    ('M6', 'PresentationMap.cs', '(wallS - fixes[i - 1].WallS) / (fixes[i].WallS - fixes[i - 1].WallS)', '(wallS - fixes[i - 1].WallS) / (2 * (fixes[i].WallS - fixes[i - 1].WallS))', 'M6_TimeInterpolationSnappingAndSeekEqualsPlay'),
    ('timing-law', 'Storyboard.cs', 'segments.Sum(s => s.DurS) - target) > 0.5', 'segments.Sum(s => s.DurS) - target) > 500', 'StrictLoaderRefusesEachViolation'),
    ('claim-lint', 'Storyboard.cs', 'PlatformVerb.IsMatch(lintText)', 'PlatformVerb.IsMatch("")', 'ClaimLintRecognizesVerbForms'),
    ('rule-1', 'Scheduler.cs', 'candidate.Runtime.Page.Priority > active.Page.Priority', 'candidate.Runtime.Page.Priority < active.Page.Priority', 'Rule1_HighPriorityPreemptsAtCaptionBoundaryWithOneActivePage'),
    ('rule-2', 'Scheduler.cs', 'now - q.AtS > q.Runtime.Page.QueueTtlS', 'now - q.AtS < q.Runtime.Page.QueueTtlS', 'Rule2_LowerTriggersQueueByPriorityThenTimeThenIdAndExpire'),
    ('rule-3', 'Scheduler.cs', 'Activate(candidate.Runtime, candidate.Resume, now, store)', 'Activate(candidate.Runtime, false, now, store)', 'Rule3_PreemptedPageResumesLastCaptionOrMissesClosedWindow'),
    ('rule-6', 'Scheduler.cs', 'var result = Evaluate(p.Page.Trigger, p, store, now);', 'var result = Evaluate(p.Page.Trigger, p, store, now); if (p.Page.Once && result.Holds) { p.Retired = true; Transition(p, PageState.Presented, now); continue; }', 'OtaRetiresOnlyByPresentationOrPreJoinTerminal'),
    ('late-join', 'Scheduler.cs', 'o.OccurredAtS < joinS)) consumed.Add(o.EventKey);', 'o.OccurredAtS < joinS)) consumed.Add("unrelated");', 'LateJoinRecapConsumesOldEventsAndResubscribeCannotRetrigger'),
    ('seek', 'Recorder.cs', 'r.Sequence > checkpoint.Sequence', 'r.Sequence < checkpoint.Sequence', 'SeekEqualsPlayAtTenRandomTargetsForwardAndBackward'),
]

def main():
    env = os.environ.copy()
    env.update(DOTNET_CLI_HOME=str(BUILD/'home'), APPDATA=str(BUILD/'roaming'), LOCALAPPDATA=str(BUILD/'local'),
               NUGET_PACKAGES='C:/Users/Derek/.nuget/packages', DOTNET_CLI_TELEMETRY_OPTOUT='1')
    results = []
    for name, filename, before, after, test in MUTANTS:
        path = SOURCE / filename
        original = path.read_bytes()
        text = original.decode('utf-8')
        if text.count(before) != 1:
            raise RuntimeError(f'{name}: expected one mutation site, found {text.count(before)}')
        try:
            path.write_text(text.replace(before, after), encoding='utf-8', newline='')
            trx = BUILD / 'mutants' / f'{name}.trx'
            command = [str(SDK), 'test', '--no-restore', '-c', 'Release',
                       f'-p:DirectoryBuildPropsPath={BUILD}/Directory.Build.props',
                       '--filter', f'FullyQualifiedName~{test}', '--logger', f'trx;LogFileName={name}.trx',
                       '--results-directory', str(trx.parent)]
            run = subprocess.run(command, cwd=HERE, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            (BUILD / f'mutant-{name}.log').write_text(run.stdout, encoding='utf-8')
            failures = []
            if trx.exists():
                failures = [r.attrib['testName'] for r in ET.parse(trx).iter() if r.tag.endswith('UnitTestResult') and r.attrib.get('outcome') == 'Failed']
            killed = run.returncode != 0 and any(test in f for f in failures)
            results.append(dict(mutant=name, test=test, killed=killed, failures=failures))
            print(json.dumps(results[-1]), flush=True)
            if not killed:
                raise RuntimeError(f'{name}: survived or failed to compile; see mutant-{name}.log')
        finally:
            path.write_bytes(original)
    (BUILD / 'mutants.json').write_text(json.dumps(results, indent=2)+'\n', encoding='utf-8')

if __name__ == '__main__':
    main()
