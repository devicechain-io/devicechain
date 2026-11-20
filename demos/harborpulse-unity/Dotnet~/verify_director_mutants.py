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
BUILD = HERE / '.build' / 'u2b'
# The caller can select an SDK through PATH or DOTNET_ROOT; never assume an account path.
import shutil
import uuid
SDK = shutil.which('dotnet')
if os.environ.get('DOTNET_ROOT'):
    SDK = str(Path(os.environ['DOTNET_ROOT']) / ('dotnet.exe' if os.name == 'nt' else 'dotnet'))
if not SDK:
    raise RuntimeError('Put the required .NET SDK on PATH or set DOTNET_ROOT')

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
    ('rule-2', 'Scheduler.cs', 'now - (q.Runtime.Triggered ? q.Runtime.TriggerS : q.AtS) > q.Runtime.Page.QueueTtlS', 'now - (q.Runtime.Triggered ? q.Runtime.TriggerS : q.AtS) < q.Runtime.Page.QueueTtlS', 'Rule2_LowerTriggersQueueByPriorityThenTimeThenIdAndExpire'),
    ('rule-3', 'Scheduler.cs', 'Activate(candidate.Runtime, candidate.Resume, now, store)', 'Activate(candidate.Runtime, false, now, store)', 'Rule3_PreemptedPageResumesLastCaptionOrMissesClosedWindow'),
    ('rule-6', 'Scheduler.cs', 'var result = Evaluate(p.Page.Trigger, p, store, now);', 'var result = Evaluate(p.Page.Trigger, p, store, now); if (p.Page.Once && result.Holds) { p.Retired = true; Transition(p, PageState.Presented, now); continue; }', 'OtaRetiresOnlyByPresentationOrPreJoinTerminal'),
    ('late-join', 'Scheduler.cs', 'o.OccurredAtS < joinS)) consumed.Add(o.EventKey);', 'o.OccurredAtS < joinS)) consumed.Add("unrelated");', 'LateJoinRecapConsumesOldEventsAndResubscribeCannotRetrigger'),
    ('pending', 'Scheduler.cs', 'if (!provisional) p.Pending.Remove(opportunity);', 'p.Pending.Remove(opportunity);', 'DelayedEvidenceRemainsPendingUntilHoldMax'),
    ('clean-fallback', 'Scheduler.cs', 'var now = sample.MonotonicS; var shot = p.Page.Shots[p.Shot];', 'var now = sample.MonotonicS; var shot = p.Page.Shots[p.Shot]; if (now == 0 && p.Page.Id == "1") Emit(now, "caption", p, "The live view is unavailable.");', 'CleanRunExactPageAndShotSequence'),
    ('set-cardinality', 'Evidence.cs', '(!Count.HasValue || a.Count == Count.Value)', 'true', 'NoneAndExactlyNeedPositiveCoverageAndRejectGaps'),
    ('identity-binding', 'Evidence.cs', 'o.Field(m.Key) == Resolve(m.Value, o)', '(m.Key == "identity" || m.Key == "cycle" || m.Key == "alarmToken" || m.Key == "rolloutToken" || m.Key == "attempt" || o.Field(m.Key) == Resolve(m.Value, o))', 'CommittedIdentityBindingRejectsWrongCausalRecord'),
    ('seek', 'Recorder.cs', 'r.Sequence > checkpoint.Sequence', 'r.Sequence < checkpoint.Sequence', 'SeekEqualsPlayAtTenRandomTargetsForwardAndBackward'),
]

def execute(env, name, test=None):
    results_dir = BUILD / 'mutants' / (name + '-' + uuid.uuid4().hex)
    results_dir.mkdir(parents=True)
    command = [str(SDK), 'test', 'Tests/DeviceChain.Harborpulse.Tests.csproj', '--no-restore', '-c', 'Release', '-p:UseSharedCompilation=false', '-nodeReuse:false',
               f'-p:DirectoryBuildPropsPath={BUILD}/Directory.Build.props',
               '--logger', 'trx;LogFileName=result.trx', '--results-directory', str(results_dir)]
    if test:
        command += ['--filter', f'FullyQualifiedName~{test}']
    run = subprocess.run(command, cwd=HERE, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    (results_dir / 'output.log').write_text(run.stdout, encoding='utf-8')
    trx = results_dir / 'result.trx'
    rows = [r.attrib for r in ET.parse(trx).iter() if r.tag.endswith('UnitTestResult')] if trx.exists() else []
    selected = [r for r in rows if test is None or test in r.get('testName', '')]
    return run.returncode, selected, results_dir

def main():
    env = os.environ.copy()
    env.update(DOTNET_CLI_HOME=str(BUILD/'home'), APPDATA=str(BUILD/'roaming'), LOCALAPPDATA=str(BUILD/'local'),
               DOTNET_CLI_TELEMETRY_OPTOUT='1', DOTNET_CLI_USE_MSBUILD_SERVER='0', MSBUILDDISABLENODEREUSE='1')
    env.pop('HARBORPULSE_UPDATE_FIXTURES', None)
    results = []
    baseline_code, baseline, baseline_dir = execute(env, 'baseline')
    if baseline_code != 0 or not baseline or any(r['outcome'] != 'Passed' for r in baseline):
        raise RuntimeError(f'A green baseline is required: {baseline_dir}')
    for _, _, _, _, test in MUTANTS:
        if not any(test in r['testName'] for r in baseline):
            raise RuntimeError(f'Selected test did not run in baseline: {test}')
    try:
        for name, filename, before, after, test in MUTANTS:
            path = SOURCE / filename
            original = path.read_bytes()
            source_text = original.decode('utf-8')
            if source_text.count(before) != 1:
                raise RuntimeError(f'{name}: expected one mutation site, found {source_text.count(before)}')
            try:
                path.write_text(source_text.replace(before, after), encoding='utf-8', newline='')
                code, selected, directory = execute(env, name, test)
                failures = [r['testName'] for r in selected if r['outcome'] == 'Failed']
                killed = code != 0 and bool(selected) and bool(failures)
                results.append(dict(mutant=name, test=test, killed=killed, executed=len(selected), failures=failures))
                print(json.dumps(results[-1]), flush=True)
                if not killed:
                    raise RuntimeError(f'{name}: survived or did not execute selected test; see {directory}')
            finally:
                path.write_bytes(original)
    finally:
        # Restoration includes rebuilding and testing the unmutated sources, even after a failed mutant.
        code, restored, directory = execute(env, 'restored')
        green = code == 0 and bool(restored) and all(r['outcome'] == 'Passed' for r in restored)
        report = dict(baselinePassed=len(baseline), mutants=results, restoredPassed=len(restored), restoredGreen=green)
        (BUILD / 'mutants.json').write_text(json.dumps(report, indent=2)+'\n', encoding='utf-8')
        print(json.dumps(dict(restoredGreen=green, restoredPassed=len(restored))), flush=True)
        if not green:
            raise RuntimeError(f'Restored tree failed validation: {directory}')

if __name__ == '__main__':
    main()
