// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Text.Json;
using System.Text.RegularExpressions;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    public partial class DirectorTestsObserver
    {
        static readonly DateTimeOffset Epoch = DateTimeOffset.Parse("2026-01-01T00:00:00Z", CultureInfo.InvariantCulture);
        static DateTimeOffset At(double sim) => Epoch.AddSeconds((sim - 6) * 60);
        static string Ts(double sim) => At(sim).ToString("O", CultureInfo.InvariantCulture);
        static string Q(string value) => "\"" + value + "\"";
        sealed class Source : IHarborSource
        {
            readonly Dictionary<string, string> tokens = Kit.Layout().Devices.ToDictionary(d => d.ExternalId, d => d.Token);
            public ScenarioStatus Status { get; set; }
            public Dictionary<string, Reading> Values { get; } = new Dictionary<string, Reading>();
            public bool Live = true;
            public IReadOnlyList<HarborAlarm> Alarms { get; set; } = Array.Empty<HarborAlarm>();
            public IReadOnlyList<HarborCommand> Commands => Array.Empty<HarborCommand>();
            public CommandCoverage CommandPoll => new CommandCoverage(Epoch, Epoch.AddDays(1));
            public AlarmCoverage AlarmObservation => new AlarmCoverage(Live, Live);
            public RolloutEvidence Rollout => null;
            public IReadOnlyList<SuppressionEvidence> Suppressions => Array.Empty<SuppressionEvidence>();
            public string ReadProblem => null;
            public bool TryToken(string id, out string token) => tokens.TryGetValue(id ?? "", out token);
            public bool TryGet(string id, string metric, out Reading reading) => Values.TryGetValue(id + "/" + metric, out reading);
            public bool TryLocation(string id, out HarborLocation location) { location = default; return false; }
            public bool TryAttribute(string id, string key, out Reading reading) { reading = default; return false; }
        }
        static JsonElement Json(string text) { using var d = JsonDocument.Parse(text); return d.RootElement.Clone(); }
        static string Page(params string[] records) => "{\"results\":[" + string.Join(",", records) + "],\"pagination\":{\"totalRecords\":" + records.Length + "}}";
        static DirectorRead Read(string name, double sim, string body, string scope = "test", DirectorProbe probe = DirectorProbe.PROBE_OK, string error = null)
            => new DirectorRead(name, scope, probe, At(sim), error, Json("{" + Q(DirectorProbeEvaluation.Roots[name]) + ":" + body + "}"), new[] { new ReadSpan(Epoch, At(sim), false) });
        static DirectorInputs Inputs(double sim, Source source = null) => new DirectorInputs { Run = "run", RecordingEpoch = Epoch, Now = At(sim), Sim = sim, Source = source ?? new Source() };
        static string Alarm(string state = "ACTIVE", double raised = 11, string device = "hp-light-east-3", string key = "lamp-flicker", string stamp = "service", string severity = "MINOR")
            => "{\"token\":\"alarm-real\",\"originatorToken\":" + Q(device) + ",\"originatorId\":\"device-row\",\"alarmKey\":" + Q(key) + ",\"state\":" + Q(state) + ",\"severity\":" + Q(severity) + ",\"raisedTime\":" + Q(Ts(raised)) + ",\"clearedTime\":" + (state == "CLEARED" ? Q(Ts(11.6)) : "null") + ",\"raisedDuringSuppression\":" + (stamp == null ? "null" : Q(stamp)) + "}";
        static string Window(string state = "ACTIVE") => "{\"token\":\"service\",\"kind\":\"ONE_SHOT\",\"status\":" + Q(state) + ",\"startTime\":" + Q(Ts(10.75)) + ",\"endTime\":" + Q(Ts(11.5)) + ",\"endedAt\":null,\"targets\":[{\"type\":\"DEVICE\",\"token\":\"hp-light-east-3\"}],\"alarmKeys\":[\"lamp-flicker\"]}";
        static string Journal() => "{\"id\":\"journal-real\",\"occurredTime\":" + Q(Ts(12.5)) + ",\"retiredCredentialTokens\":[\"old-real\"],\"newCredentialToken\":\"new-real\",\"device\":{\"id\":\"device-real\",\"token\":\"hp-gauge-north-3\",\"externalId\":\"hp-gauge-north-3\"}}";
        static string Credential(string enabled = "false") => "{\"token\":\"old-real\",\"enabled\":" + enabled + ",\"device\":{\"token\":\"hp-gauge-north-3\"}}";
        static string Measure(string id, double sim, double value = 1, string device = "hp-gauge-north-3", string metric = "water_level_m")
            => "{\"id\":" + Q(id) + ",\"deviceToken\":" + Q(device) + ",\"occurredTime\":" + Q(Ts(sim)) + ",\"name\":" + Q(metric) + ",\"value\":" + value.ToString(CultureInfo.InvariantCulture) + "}";
        [TestCase(2.6, 3.4)]
        [TestCase(3.4, 2.6)]
        public void SameTimestampWellCorrectionsUseFinalBatchValue(double first, double final)
        {
            var snapshot = new DirectorReadSnapshot(Array.Empty<DirectorRead>(), measurements: new[] {
                new DirectorMeasurement("hp-pump-north", Metrics.WetWell, first, At(17)),
                new DirectorMeasurement("hp-pump-north", Metrics.WetWell, final, At(17))
            });
            var store = new ObservationStore(); var observations = new List<Observation>();
            var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
            adapter.Update(Inputs(17.1), o => { observations.Add(o); store.Add(o); }, _ => { });
            var well = observations.Where(o => o.Kind == "measure" && o.Key == Metrics.WetWell).ToArray();
            Assert.That(well.Length, Is.EqualTo(1), "one final revision per identity and batch");
            Assert.That(well.Single().Fields["value"], Is.EqualTo(final.ToString(CultureInfo.InvariantCulture)));
            store.Add(new Observation("run", "chip", "start", "hp-pump-north", "", "", "", "", "PLATFORM", 666, 666, 17.1, EvidenceSource.Platform,
                new Dictionary<string, string> { ["runningAt"] = "666" }));
            store.AddCoverage(new CoverageInterval("run", "pump", 17, 17.1, false));
            using var doc = JsonDocument.Parse("{\"all\":[{\"chip\":{\"device\":\"hp-pump-north\",\"status\":\"PLATFORM\",\"as\":\"running\"}},{\"measure\":{\"device\":\"hp-pump-north\",\"key\":\"wet_well_m\",\"allBelow\":3.2,\"until\":\"$match.running.runningAt\",\"stream\":\"pump\",\"fromSim\":17,\"toSim\":18}}]}");
            var trigger = new Observation("run", "alarm", "high", "hp-pump-north", "wet-well-high", "", "", "", "RAISED", 660, 660, 17, EvidenceSource.Platform);
            Assert.That(Needs.Parse(doc.RootElement).Evaluate(store, new EvidenceContext("run", 17, 18, 1000, trigger: trigger)).Holds, Is.EqualTo(final < 3.2));
        }
        [Test]
        public void FutureWellReadingsRetryWhenDueWithoutRevisitingHistory()
        {
            var tap = new DirectorMeasurementTap();
            // Arrival order differs from due order; a correction arrives before its timestamp is due.
            tap.Watch(new DirectorMeasurement("hp-pump-north", Metrics.WetWell, 2.6, At(17.3)));
            tap.Watch(new DirectorMeasurement("hp-pump-north", Metrics.WetWell, 2.8, At(17.2)));
            var snapshot = new DirectorReadSnapshot(Array.Empty<DirectorRead>(), measurements: tap.Records);
            var observations = new List<Observation>(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
            adapter.Update(Inputs(17.1), observations.Add, _ => { });
            Assert.That(observations.Any(o => o.Kind == "measure"), Is.False);
            var work = adapter.MeasurementWorkUnits;
            for (var tick = 1; tick <= 120; tick++) adapter.Update(Inputs(17.1 + tick / 10000.0), observations.Add, _ => { });
            Assert.That(adapter.MeasurementWorkUnits, Is.EqualTo(work));
            tap.Watch(new DirectorMeasurement("hp-pump-north", Metrics.WetWell, 3.4, At(17.3)));
            snapshot = new DirectorReadSnapshot(Array.Empty<DirectorRead>(), measurements: tap.Records);
            adapter.Update(Inputs(17.2), observations.Add, _ => { });
            Assert.That(observations.Where(o => o.Kind == "measure").Select(o => o.Fields["value"]), Is.EqualTo(new[] { "2.8" }));
            adapter.Update(Inputs(17.3), observations.Add, _ => { });
            var wells = observations.Where(o => o.Kind == "measure").ToArray();
            Assert.That(wells.Select(o => o.Fields["value"]), Is.EqualTo(new[] { "2.8", "3.4" }));
            Assert.That(wells[1].OccurredAtS, Is.EqualTo((At(17.3) - Epoch).TotalSeconds));
            Assert.That(wells[1].ObservedAtS, Is.EqualTo(wells[1].OccurredAtS));
            work = adapter.MeasurementWorkUnits;
            adapter.Update(Inputs(17.4), observations.Add, _ => { });
            Assert.That(adapter.MeasurementWorkUnits, Is.EqualTo(work));
        }
        static string Rollout(string state = "RUNNING") => "{\"token\":\"rollout-real\",\"state\":" + Q(state) + ",\"startedAt\":" + Q(Ts(26)) + ",\"finishedAt\":" + Q(Ts(26.6)) + ",\"stateChangedAt\":" + Q(Ts(26)) + ",\"maxInFlight\":2,\"artifact\":{\"token\":\"artifact-real\"},\"progress\":{\"total\":6,\"pending\":0,\"inFlight\":0,\"succeeded\":5,\"failed\":1,\"unknown\":0,\"cancelled\":0,\"skipped\":0}}";
        const string Artifact = "{\"token\":\"artifact-real\",\"state\":\"AVAILABLE\",\"trustStatus\":\"SIGNED\",\"signatures\":[{\"countsTowardTrust\":true,\"signer\":{\"token\":\"signer-real\",\"keyId\":\"key-real\",\"state\":\"ACTIVE\"}}]}";
        const string Signer = "{\"token\":\"signer-real\",\"keyId\":\"key-real\",\"state\":\"ACTIVE\"}";
        static string Assignment(string code = "VERIFICATION_FAILED", string platform = "false") => "{\"token\":\"assignment-real\",\"deviceToken\":\"hp-charger-north-1\",\"artifact\":{\"token\":\"artifact-real\"},\"attempt\":{\"token\":\"attempt-real\",\"state\":\"FAILED\",\"lastReportAt\":" + Q(Ts(26.3)) + ",\"failure\":{\"stage\":\"VERIFY\",\"code\":" + Q(code) + ",\"platform\":" + platform + "}}}";
        const string Target = "{\"deviceToken\":\"hp-charger-north-1\",\"outcome\":\"FAILED\",\"assignmentToken\":\"assignment-real\"}";
        [TestCase("{\"data\":{\"deviceCredentialsByToken\":[]}}", 200, true)]
        [TestCase("{\"errors\":[{\"message\":\"unauthorized: credential:read denied\"}]}", 200, false)]
        [TestCase("{}", 401, false)]
        [TestCase("{}", 403, false)]
        [TestCase("{\"data\":{\"deviceCredentialsByToken\":null}}", 200, false)]
        [TestCase("{\"data\":{\"deviceCredentialsByToken\":[{\"token\":\"old\",\"enabled\":null,\"device\":{\"token\":\"d\"}}]}}", 200, false)]
        [TestCase("{\"data\":{\"deviceCredentialsByToken\":[{\"token\":\"old\",\"enabled\":false,\"device\":{\"token\":\"d\"}}]}}", 200, true)]
        public void ProbeVerdictsPreserveErrorsAndAllowEmptyResults(string json, int http, bool ok)
        {
            var verdict = DirectorProbeEvaluation.Evaluate("credentials", http, json, out var data, out var error);
            Assert.That(verdict == DirectorProbe.PROBE_OK, Is.EqualTo(ok)); Assert.That(data.HasValue, Is.EqualTo(ok));
            if (!ok) Assert.That(error, Is.Not.Empty);
            if (json.Contains("unauthorized")) Assert.That(error, Does.Contain("unauthorized"));
        }
        [Test]
        public void WaitingSnapshotIsAbsentAndDetached()
        {
            var waiting = new DirectorRead("credentials", "retired"); Assert.That(waiting.State, Is.EqualTo("waiting")); Assert.That(waiting.Data, Is.Null); Assert.That(waiting.LastSuccess, Is.Null);
            var list = new List<DirectorRead> { waiting }; var snapshot = new DirectorReadSnapshot(list); list.Clear(); Assert.That(snapshot.Reads.Count, Is.EqualTo(1));
        }
        [Test]
        public void MissingNullableRolloutTargetsIsReadableAbsence()
        {
            Assert.That(DirectorProbeEvaluation.Evaluate("targets", 200, "{\"data\":{\"updateRolloutTargets\":null}}", out var data, out var error), Is.EqualTo(DirectorProbe.PROBE_OK));
            Assert.That(data.HasValue, Is.True); Assert.That(error, Is.Null);
        }
        [Test]
        public void AlarmIdentityCycleSuppressionAndWallTimesAreExact()
        {
            var snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 11.1, Page(Alarm())) }); var obs = new List<Observation>();
            var adapter = new DirectorObserver(Kit.Layout(), () => snapshot); adapter.Update(Inputs(11.1), obs.Add, _ => { });
            Assert.That(adapter.Problems, Is.Empty, string.Join("; ", adapter.Problems));
            var a = obs.Single(o => o.Kind == "alarm"); Assert.That(a.Identity, Is.EqualTo("alarm-real")); Assert.That(a.Cycle, Is.EqualTo(Ts(11)));
            Assert.That(a.Status, Is.EqualTo("RAISED")); Assert.That(a.Device, Is.EqualTo("hp-light-east-3")); Assert.That(a.OccurredAtS, Is.EqualTo(300)); Assert.That(a.ObservedAtS, Is.EqualTo(306));
            Assert.That(a.Fields, Is.EquivalentTo(new Dictionary<string, string> { ["alarmToken"] = "alarm-real", ["raisedDuringSuppression"] = "true", ["suppressionWindowToken"] = "service" }));
        }
        [TestCase(false)]
        [TestCase(true)]
        public void EscalationUsesRetainedStreamTransitionAndNeverHighSeverityAlone(bool transition)
        {
            var changes = transition ? new[] { new DirectorAlarmChange("alarm-real", "hp-gauge-east-1", "flood-level", At(17)) } : Array.Empty<DirectorAlarmChange>();
            var snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 17.1, Page(Alarm(raised: 16.9, device: "hp-gauge-east-1", key: "flood-level", stamp: null, severity: "MAJOR"))) }, alarmChanges: changes);
            var obs = new List<Observation>(); new DirectorObserver(Kit.Layout(), () => snapshot).Update(Inputs(17.1), obs.Add, _ => { });
            Assert.That(obs.Count(o => o.Status == "ESCALATED"), Is.EqualTo(transition ? 1 : 0));
            if (transition) { var a = obs.Single(o => o.Status == "ESCALATED"); Assert.That(a.OccurredAtS, Is.EqualTo(660)); Assert.That(a.Cycle, Is.EqualTo(Ts(16.9))); Assert.That(a.Identity, Is.EqualTo("alarm-real")); }
        }
        [Test]
        public void UnchangedRepollKeepsEventKeyAndStatusChangeCreatesNewKey()
        {
            DirectorReadSnapshot snapshot = null; var obs = new List<Observation>(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
            for (var i = 0; i < 3; i++) { var sim = 11.6 + i / 60.0; snapshot = new DirectorReadSnapshot(new[] { Read("alarms", sim, Page(Alarm(i == 2 ? "CLEARED" : "ACTIVE"))) }); adapter.Update(Inputs(sim), obs.Add, _ => { }); }
            var alarms = obs.Where(o => o.Kind == "alarm").ToArray(); Assert.That(alarms.Count(a => a.Status == "RAISED"), Is.EqualTo(1)); Assert.That(alarms.Select(a => a.EventKey).Distinct().Count(), Is.EqualTo(2));
        }
        [Test]
        public void MissingFieldReportsProblemAndEmitsNoRecord()
        {
            var snapshot = new DirectorReadSnapshot(new[] { Read("credentials", 12.6, "[" + Credential().Replace("\"enabled\":false,", "") + "]"), Read("replacements", 12.6, Page(Journal())) });
            var adapter = new DirectorObserver(Kit.Layout(), () => snapshot); var obs = new List<Observation>(); adapter.Update(Inputs(12.6), obs.Add, _ => { });
            Assert.That(adapter.Problems.Any(p => p.Contains("enabled")), Is.True); Assert.That(obs.Any(o => o.Kind == "credential"), Is.False);
        }
        [Test]
        public void FailedProbeMakesWholeRunGapAndNeverPlatformEvidence()
        {
            DirectorReadSnapshot snapshot = null; var adapter = new DirectorObserver(Kit.Layout(), () => snapshot); var obs = new List<Observation>(); var coverage = new List<CoverageInterval>();
            adapter.Update(Inputs(6), obs.Add, coverage.Add);
            snapshot = new DirectorReadSnapshot(new[] { Read("credentials", 7, "[]") }); adapter.Update(Inputs(7), obs.Add, coverage.Add);
            snapshot = new DirectorReadSnapshot(new[] { Read("credentials", 12.6, "[" + Credential() + "]", probe: DirectorProbe.PROBE_FAILED, error: "HTTP 403"), Read("replacements", 12.6, Page(Journal())) });
            adapter.Update(Inputs(12.6), obs.Add, coverage.Add);
            Assert.That(coverage.Any(c => c.Stream == "credentials" && c.Gap && c.FromSim == 6 && c.ToSim == 12.6), Is.True);
            Assert.That(adapter.Problems.Any(p => p.Contains("403")), Is.True); Assert.That(obs.Any(o => o.Kind == "credential"), Is.False);
            snapshot = new DirectorReadSnapshot(new[] { Read("credentials", 12.7, "[" + Credential() + "]"), Read("replacements", 12.7, Page(Journal())) }); adapter.Update(Inputs(12.7), obs.Add, coverage.Add);
            Assert.That(obs.Any(o => o.Kind == "credential"), Is.False, "failed probe is latched for the run");
        }
        [Test]
        public void NotificationWindowFilterUsesAttemptInstantAndRealCycle()
        {
            string Attempt(string id, double sim) => "{\"id\":" + Q(id) + ",\"alarmToken\":\"alarm-real\",\"attemptedAt\":" + Q(Ts(sim)) + ",\"trigger\":\"ESCALATION\",\"status\":\"SENT\",\"policyToken\":\"hp-duty-escalation\"}";
            var snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 11.6, Page(Alarm())), Read("windows", 11.6, "[" + Window("ENDED") + "]"), Read("notifications", 11.6, Page(Attempt("before-cycle", 10.9), Attempt("during", 11.2), Attempt("after", 11.52))) });
            var obs = new List<Observation>(); new DirectorObserver(Kit.Layout(), () => snapshot).Update(Inputs(11.6), obs.Add, _ => { });
            var attempts = obs.Where(o => o.Kind == "attempt").ToArray(); Assert.That(attempts.Select(a => a.Identity), Is.EquivalentTo(new[] { "during", "after" }));
            Assert.That(attempts.Single(a => a.Identity == "during").Field("afterClose"), Is.EqualTo("false")); Assert.That(attempts.Single(a => a.Identity == "after").OccurredAtS, Is.EqualTo((11.52 - 6) * 60).Within(1e-6));
            Assert.That(attempts.All(a => a.Cycle == Ts(11) && a.Field("trigger") == "ESCALATION" && a.Field("policy") == "hp-duty-escalation"), Is.True);
        }
        [TestCase("false", "false", "DISABLED")]
        [TestCase("true", "true", "ENABLED")]
        public void CredentialEnabledParsingPreservesPlatformBoolean(string wire, string field, string status)
        {
            var snapshot = new DirectorReadSnapshot(new[] { Read("credentials", 12.6, "[" + Credential(wire) + "]"), Read("replacements", 12.6, Page(Journal())) });
            var obs = new List<Observation>(); new DirectorObserver(Kit.Layout(), () => snapshot).Update(Inputs(12.6), obs.Add, _ => { });
            var c = obs.Single(o => o.Kind == "credential"); Assert.That(c.Field("enabled"), Is.EqualTo(field)); Assert.That(c.Status, Is.EqualTo(status)); Assert.That(c.Field("replacementToken"), Is.EqualTo("journal-real"));
        }
        [TestCase("VERIFICATION_FAILED", "false")]
        [TestCase("DOWNLOAD_FAILED", "true")]
        public void OtaFailureCodeMappingPreservesCodePlatformAndAttempt(string code, string platform)
        {
            var snapshot = new DirectorReadSnapshot(new[] { Read("rollout", 26.4, Rollout()), Read("targets", 26.4, Page(Target), "rollout-real"), Read("assignments", 26.4, Assignment(code, platform)), Read("artifact", 26.4, Artifact), Read("signer", 26.4, Signer) });
            var obs = new List<Observation>(); new DirectorObserver(Kit.Layout(), () => snapshot).Update(Inputs(26.4), obs.Add, _ => { });
            var a = obs.Single(o => o.Kind == "attempt"); Assert.That(a.Identity, Is.EqualTo("attempt-real")); Assert.That(a.Attempt, Is.EqualTo("attempt-real")); Assert.That(a.Field("code"), Is.EqualTo(code)); Assert.That(a.Field("platform"), Is.EqualTo(platform)); Assert.That(a.Cycle, Is.EqualTo(Ts(26))); Assert.That(a.OccurredAtS, Is.EqualTo(1218));
            Assert.That(obs.Single(o => o.Kind == "artifact").Field("signer"), Is.EqualTo("signer-real")); Assert.That(obs.Single(o => o.Kind == "target").Status, Is.EqualTo("FAILED")); Assert.That(obs.Single(o => o.Kind == "rollout").Field("maxInFlight"), Is.EqualTo("2"));
        }
        [Test]
        public void ReplacementAndConsecutivePlatformReadingsEstablishHistoryAndTrend()
        {
            var snapshot = new DirectorReadSnapshot(new[] { Read("replacements", 12.6, Page(Journal())), Read("measurements", 12.6, Page(Measure("before-real", 12.45, 2), Measure("after-real", 12.51, 1))) });
            var obs = new List<Observation>(); new DirectorObserver(Kit.Layout(), () => snapshot).Update(Inputs(12.6), obs.Add, _ => { });
            var r = obs.Single(o => o.Kind == "replacement"); Assert.That(r.Identity, Is.EqualTo("journal-real")); Assert.That(r.Field("beforeReading"), Is.EqualTo("before-real")); Assert.That(r.Field("afterReading"), Is.EqualTo("after-real")); Assert.That(r.Sets["disabledCredentials"], Is.EqualTo(new[] { "old-real" }));
            var m = obs.Single(o => o.Identity == "after-real"); Assert.That(m.Field("falling"), Is.EqualTo("true")); Assert.That(m.Field("afterReplacement"), Is.EqualTo("true")); Assert.That(m.Cycle, Is.EqualTo(Ts(12.5)));
        }
        [Test]
        public void ReplacementTriggerWaitsForTheFirstReadingAfterTheSwap()
        {
            // A trigger is captured once, so a journal emitted before its after-reading was read could never join that reading.
            DirectorReadSnapshot snapshot = null; var obs = new List<Observation>(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
            snapshot = new DirectorReadSnapshot(new[] { Read("replacements", 12.6, Page(Journal())), Read("measurements", 12.6, Page(Measure("before-real", 12.45, 2))) });
            adapter.Update(Inputs(12.6), obs.Add, _ => { });
            Assert.That(obs.Any(o => o.Kind == "replacement"), Is.False);
            snapshot = new DirectorReadSnapshot(new[] { Read("replacements", 12.7, Page(Journal())), Read("measurements", 12.7, Page(Measure("before-real", 12.45, 2), Measure("after-real", 12.65, 1))) });
            adapter.Update(Inputs(12.7), obs.Add, _ => { });
            Assert.That(obs.Single(o => o.Kind == "replacement").Field("afterReading"), Is.EqualTo("after-real"));
            // With no reading after the swap, the journal still appears once the wait is over, and the claims needing the reading stay unmet.
            var late = new List<Observation>(); var lone = new DirectorObserver(Kit.Layout(), () => new DirectorReadSnapshot(new[] { Read("replacements", 13.6, Page(Journal())), Read("measurements", 13.6, Page(Measure("before-real", 12.45, 2))) }));
            lone.Update(Inputs(13.6), late.Add, _ => { });
            Assert.That(late.Single(o => o.Kind == "replacement").Field("afterReading"), Is.Null);
        }
        [Test]
        public void CommandWithoutPlatformAlarmJoinDoesNotInventOne()
        {
            var command = "{\"token\":\"command-real\",\"deviceToken\":\"hp-pump-north\",\"name\":\"start-pump\",\"status\":\"SUCCESSFUL\",\"queuedTime\":" + Q(Ts(17.51)) + ",\"sentTime\":" + Q(Ts(17.52)) + ",\"respondedTime\":" + Q(Ts(17.53)) + ",\"payload\":null,\"cause\":null}";
            var snapshot = new DirectorReadSnapshot(new[] { Read("commands", 17.6, Page(command)) }); var obs = new List<Observation>();
            new DirectorObserver(Kit.Layout(), () => snapshot).Update(Inputs(17.6), obs.Add, _ => { });
            Assert.That(obs.Where(o => o.Kind == "command").Select(o => o.Status), Is.EquivalentTo(new[] { "QUEUED", "SENT", "SUCCESSFUL" }));
            Assert.That(obs.Where(o => o.Kind == "command").All(o => o.Identity == "command-real" && o.Field("alarmToken") == null), Is.True);
        }
        [Test]
        public void MembershipUsesRecordedAreaMembersAndBoundTypes()
        {
            var snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 20.5, Page(Alarm(device: "hp-light-east-1", key: "light-silent", raised: 20.5, stamp: null))), Read("membership", 20.5, Page("{\"token\":\"hp-light-east-1\"}", "{\"token\":\"hp-charger-east-1\"}", "{\"token\":\"hp-gauge-east-1\"}"), "hp-east-quay") });
            var obs = new List<Observation>(); new DirectorObserver(Kit.Layout(), () => snapshot).Update(Inputs(20.5), obs.Add, _ => { });
            Assert.That(obs.Single(o => o.Identity == "east-absence").Sets["tokens"], Is.EquivalentTo(new[] { "hp-light-east-1", "hp-charger-east-1" }));
        }
        [Test]
        public void OfflinePreviewCannotProducePlatformObservations()
        {
            var layout = Kit.Layout(); var source = new PreviewSource(layout); source.SetTime(11 * 3600, At(11)); var i = Inputs(11); i.Source = source;
            var obs = new List<Observation>(); var gaps = new List<CoverageInterval>(); var adapter = new DirectorObserver(layout, () => null); adapter.Update(i, obs.Add, gaps.Add);
            Assert.That(obs, Is.Not.Empty); Assert.That(obs.All(o => o.Source != EvidenceSource.Platform), Is.True); Assert.That(gaps.Any(g => g.Stream == "notifications" && g.Gap), Is.True); Assert.That(adapter.Problems.Any(p => p.Contains("OFFLINE PREVIEW")), Is.True);
        }

        static Scheduler CleanDay(string remove)
        {
            var layout = Kit.Layout(); var scheduler = new Scheduler(DirectorKit.Story(), layout); var store = new ObservationStore();
            DirectorReadSnapshot snapshot = null; var adapter = new DirectorObserver(layout, () => snapshot); var source = new Source();
            for (var wall = 0; wall <= 1320; wall++) {
                var sim = 6 + wall / 60.0;
                var data = new List<DirectorRead>();
                foreach (var name in DirectorProbeEvaluation.Roots.Keys) {
                    var body = DirectorProbeEvaluation.Paged(name) ? Page() : new[] { "artifact", "signer", "rollout", "assignments" }.Contains(name) ? "null" : "[]";
                    if (name == "alarms" && sim >= 11) body = Page(Alarm());
                    if (name == "windows" && remove != "window") body = "[" + Window(sim >= 11.5 ? "ENDED" : "ACTIVE") + "]";
                    if (name == "replacements" && sim >= 12.5) body = Page(Journal());
                    if (name == "credentials" && sim >= 12.5 && remove != "credential") body = "[" + Credential() + "]";
                    if (name == "measurements" && sim >= 12.45) body = sim >= 12.51 ? Page(Measure("before", 12.45), Measure("after", 12.51)) : Page(Measure("before", 12.45));
                    if (name == "rollout" && sim >= 26) body = Rollout(sim >= 26.6 ? "COMPLETED" : "RUNNING");
                    if (name == "artifact" && sim >= 26 && remove != "artifact") body = Artifact;
                    if (name == "signer" && sim >= 26) body = Signer;
                    data.Add(Read(name, sim, body));
                }
                snapshot = new DirectorReadSnapshot(data, true); adapter.Update(Inputs(sim, source), o => store.Add(o), store.AddCoverage);
                scheduler.Tick(new DirectorSample("run", sim, wall), store);
            }
            return scheduler;
        }
        [Test]
        public void CleanDayObserverFeedsCommittedSchedulerAndPlatformControlsRemoveOnlyTheirClaim()
        {
            bool[] Claims(Scheduler s) => new[] {
                s.Log.Any(e => e.Page == "2" && e.Kind == "caption" && e.Value == "No notification recorded\nwhile the work is on."),
                s.Log.Any(e => e.Page == "3" && e.Kind == "caption" && e.Value == "The old unit's credentials\nare disabled."),
                s.Log.Any(e => e.Page == "8" && e.Kind == "node" && e.Value == "signed") };
            Assert.That(Claims(CleanDay("")), Is.EqualTo(new[] { true, true, true }));
            Assert.That(Claims(CleanDay("window")), Is.EqualTo(new[] { false, true, true }));
            Assert.That(Claims(CleanDay("credential")), Is.EqualTo(new[] { true, false, true }));
            var unsigned = CleanDay("artifact"); Assert.That(Claims(unsigned), Is.EqualTo(new[] { true, true, false }));
            Assert.That(unsigned.Log.Any(e => e.Page == "8" && e.Kind == "caption" && e.Value == "Overnight: a firmware update."), Is.True);
        }
        [Test]
        public void RealOtaAttemptsJoinTheirOwnFailureRatherThanRolloutAttempt()
        {
            var source = new Source(); source.Values["PAW-CHARGER-NORTH-1/power_kw"] = new Reading(4, At(26.31));
            var snapshot = new DirectorReadSnapshot(new[] {
                Read("rollout", 26.4, Rollout()), Read("targets", 26.4, Page(Target, "{\"deviceToken\":\"hp-charger-east-1\",\"outcome\":\"SUCCEEDED\",\"assignmentToken\":\"assignment-other\"}"), "rollout-real"),
                Read("assignments", 26.4, Assignment()), Read("assignments", 26.4, Assignment().Replace("hp-charger-north-1", "hp-charger-east-1").Replace("assignment-real", "assignment-other").Replace("attempt-real", "attempt-other").Replace("\"FAILED\"", "\"UPDATED\""), "other"),
                Read("artifact", 26.4, Artifact), Read("signer", 26.4, Signer) }, true, measurements: new[] { new DirectorMeasurement("hp-charger-north-1", "power_kw", 4, At(26.31)) });
            var store = new ObservationStore(); new DirectorObserver(Kit.Layout(), () => snapshot).Update(Inputs(26.4, source), o => store.Add(o), store.AddCoverage);
            var trigger = store.Records.Single(o => o.Kind == "rollout"); Assert.That(trigger.Attempt, Is.Empty, "a rollout has no device attempt");
            var page = DirectorKit.Story().Page("8"); var context = page.Context("run", (26.4 - 6) * 60, trigger: trigger);
            foreach (var id in new[] { "few", "refused", "working" }) Assert.That(page.Captions.Single(c => c.Id == id).Needs.Evaluate(store, context).Holds, Is.True, id);
        }
        [Test]
        public void RealAbsenceAlarmsMayHaveDifferentRaisedTimes()
        {
            var devices = new[] { "hp-light-east-1", "hp-light-east-2", "hp-light-east-3", "hp-light-east-4", "hp-charger-east-1", "hp-charger-east-2" };
            var alarms = devices.Select((d, i) => Alarm(raised: 20.5 + i / 6000.0, device: d, key: d.Contains("light") ? "light-silent" : "charger-offline", stamp: null).Replace("alarm-real", "alarm-real-" + i)).ToArray();
            var snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 20.55, Page(alarms)), Read("membership", 20.55, Page(devices.Select(d => "{\"token\":" + Q(d) + "}").ToArray()), "hp-east-quay") });
            var store = new ObservationStore(); new DirectorObserver(Kit.Layout(), () => snapshot).Update(Inputs(20.55), o => store.Add(o), _ => { });
            store.AddCoverage(new CoverageInterval("run", "alarms", 19.75, 20.55, false));
            var trigger = store.Records.First(o => o.Kind == "alarm"); var page = DirectorKit.Story().Page("6");
            Assert.That(page.Captions.Single(c => c.Id == "exact").Needs.Evaluate(store, page.Context("run", (20.55 - 6) * 60, trigger: trigger)).Holds, Is.True);
        }
        [Test]
        public void U1ChipAndFreshBindingUseReportedReadings()
        {
            var source = new Source(); var layout = Kit.Layout(); var model = new HarborObservedModel(layout);
            source.Values["PAW-PUMP-NORTH/pump_running"] = new Reading(0, At(17.4));
            source.Values["PAW-PUMP-NORTH/wet_well_m"] = new Reading(3.3, At(17.4));
            model.Update(source, new ClockReading(17.4 * 3600, ClockMode.Live, ""), At(17.4));
            source.Values["PAW-PUMP-NORTH/pump_running"] = new Reading(1, At(17.5));
            source.Values["PAW-PUMP-NORTH/wet_well_m"] = new Reading(3.3, At(17.5));
            var frame = model.Update(source, new ClockReading(17.5 * 3600, ClockMode.Live, ""), At(17.5));
            var snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 17.5, Page(Alarm(raised: 17.4, device: "hp-pump-north", key: "wet-well-high", stamp: null))) }, true);
            var inputs = Inputs(17.5, source); inputs.Frame = frame; var obs = new List<Observation>(); new DirectorObserver(layout, () => snapshot).Update(inputs, obs.Add, _ => { });
            var chip = obs.Single(o => o.Kind == "chip"); Assert.That(chip.Device, Is.EqualTo("hp-pump-north")); Assert.That(chip.Status, Is.EqualTo("FLOAT-CONSISTENT")); Assert.That(chip.Field("runningAt"), Is.EqualTo("690")); Assert.That(chip.Cycle, Is.EqualTo(Ts(17.4)));
            Assert.That(obs.Single(o => o.Kind == "binding").Field("freshDevices"), Is.EqualTo("1"), "count devices, not metric rows");
        }
        [Test]
        public void AboveFloatReadingBeforeHighAlarmRemainsInThatPumpCycle()
        {
            var source = new Source(); DirectorReadSnapshot snapshot = null; var adapter = new DirectorObserver(Kit.Layout(), () => snapshot); var obs = new List<Observation>();
            source.Values["PAW-PUMP-NORTH/pump_running"] = new Reading(0, At(17.2)); source.Values["PAW-PUMP-NORTH/wet_well_m"] = new Reading(3.4, At(17.2));
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 17.2, Page()) }, true, measurements: new[] { new DirectorMeasurement("hp-pump-north", Metrics.PumpRunning, 0, At(17.2)), new DirectorMeasurement("hp-pump-north", Metrics.WetWell, 3.4, At(17.2)) }); adapter.Update(Inputs(17.2, source), obs.Add, _ => { });
            source.Values["PAW-PUMP-NORTH/wet_well_m"] = new Reading(2.6, At(17.5));
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 17.5, Page(Alarm(raised: 17.5, device: "hp-pump-north", key: "wet-well-high", stamp: null))) }, true, measurements: snapshot.Measurements.Concat(new[] { new DirectorMeasurement("hp-pump-north", Metrics.WetWell, 2.6, At(17.5)) })); adapter.Update(Inputs(17.5, source), obs.Add, _ => { });
            Assert.That(obs.Any(o => o.Kind == "measure" && o.Key == "wet_well_m" && o.Cycle == Ts(17.5) && o.Field("value") == "3.4" && Math.Abs(o.OccurredAtS - 672) < 1e-6), Is.True);
        }
        static ScenarioStatus RestoreStatus() => ScenarioStatus.Parse("{\"state\":\"RUNNING\",\"scenario\":{\"name\":\"harborpulse\",\"started\":true,\"startedAt\":" + Q(Ts(6)) + ",\"dayLengthSeconds\":1440,\"startHour\":6,\"simHour\":21.6,\"day\":0,\"defects\":[\"drop-backfill\"],\"layoutDigest\":" + Q(new string('a', 64)) + ",\"backfill\":[{\"device\":\"hp-charger-east-1\",\"day\":0,\"buffered\":2,\"first\":" + Q(Ts(20)) + ",\"last\":" + Q(Ts(20.1)) + ",\"times\":[" + Q(Ts(20)) + "," + Q(Ts(20.1)) + "]}],\"s5\":{\"replacement\":{\"device\":\"hp-gauge-north-3\",\"at\":" + Q(Ts(12.5)) + ",\"unitIdentifier\":\"unit\"},\"restoreDrains\":[{\"day\":0,\"offered\":2,\"accepted\":2,\"refused429\":3,\"firstRefusalAt\":" + Q(Ts(21.26)) + ",\"caughtUpAt\":" + Q(Ts(21.3)) + "}]}}}");
        [TestCase(false)]
        [TestCase(true)]
        public void RestoreLedgerSourcesIncludeUnexpectedPlatformTimesAndEveryBufferedReadingIsAtLeastTheLedger(bool extra)
        {
            var source = new Source { Status = RestoreStatus() }; var records = new List<string> {
                Measure("buffer-1", 20, device: "hp-charger-east-1", metric: "power_kw"), Measure("buffer-2", 20.1, device: "hp-charger-east-1", metric: "power_kw") };
            if (extra) records.Add(Measure("unexpected", 20.05, device: "hp-charger-east-1", metric: "power_kw"));
            var snapshot = new DirectorReadSnapshot(new[] { Read("measurements", 21.6, Page(records.ToArray())) }, true); var store = new ObservationStore();
            new DirectorObserver(Kit.Layout(), () => snapshot).Update(Inputs(21.6, source), o => store.Add(o), _ => { });
            var sender = store.Records.Single(o => o.Kind == "ledger" && o.Key == "restore"); Assert.That(sender.Source, Is.EqualTo(EvidenceSource.DeviceSide)); Assert.That(sender.Field("value"), Is.EqualTo("3"));
            var ledger = store.Records.Single(o => o.Kind == "ledger" && o.Key == "backfill"); Assert.That(ledger.Source, Is.EqualTo(EvidenceSource.Scenario)); Assert.That(ledger.Sets["times"].Length, Is.EqualTo(2));
            var metadata = store.Records.Single(o => o.Kind == "ledger" && o.Key == "run"); Assert.That(metadata.Field("layoutDigest"), Is.EqualTo(new string('a', 64))); Assert.That(metadata.Sets["defects"], Is.EqualTo(new[] { "drop-backfill" }));
            Assert.That(store.Records.Single(o => o.Kind == "replacement").Source, Is.EqualTo(EvidenceSource.Scenario));
            var back = store.Records.Where(o => o.Key == "backfill" && o.Kind == "measure").ToArray(); Assert.That(back.Length, Is.EqualTo(extra ? 3 : 2)); Assert.That(back[0].OccurredAtS, Is.EqualTo(840)); Assert.That(back[0].ObservedAtS, Is.EqualTo(936));
            var page = DirectorKit.Story().Page("7"); // no stream coverage is added: an at-least set asks for none
            // "Every buffered reading, stored at the time it was taken": every time in the sender's ledger is read back. A reading the
            // ledger does not list (on a live day, the one taken at the outage's first instant) is still observed and still counted,
            // and does not refute it; a ledger time that is NOT read back does.
            var every = page.Captions.Single(c => c.Id == "all-buffered").Needs;
            Assert.That(every.Evaluate(store, page.Context("run", 936)).Holds, Is.True);
            Assert.That(every.Explain(store, page.Context("run", 936)), Does.Contain("observed " + (extra ? 3 : 2) + ", expected 2, missing 0, extra " + (extra ? "1" : "0")));
            var short1 = new ObservationStore(); foreach (var o in store.Records.Where(o => !(o.Kind == "measure" && o.Key == "backfill" && o.Identity == "buffer-2"))) short1.Add(o);
            Assert.That(every.Evaluate(short1, page.Context("run", 936)).Holds, Is.False, "one buffered reading never stored");
            Assert.That(every.Explain(short1, page.Context("run", 936)), Does.Contain("missing 1"));
        }
        [Test]
        public void ReadFailureAndReconnectCountersPreserveGapsBetweenUpdates()
        {
            DirectorReadSnapshot snapshot = null; var source = new Source(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot); var gaps = new List<CoverageInterval>();
            snapshot = new DirectorReadSnapshot(new[] { Read("measurements", 6, Page()) }, true); adapter.Update(Inputs(6, source), _ => { }, gaps.Add);
            snapshot = new DirectorReadSnapshot(new[] { new DirectorRead("measurements", "test", DirectorProbe.PROBE_OK, At(6.1), null, Json("{\"measurementEvents\":" + Page() + "}"), new[] { new ReadSpan(At(6.01), At(6.05), true) }) }, true, 1, 1);
            adapter.Update(Inputs(6.1, source), _ => { }, gaps.Add);
            Assert.That(gaps.Any(g => g.Stream == "measurements" && g.Gap && g.FromSim == 6 && g.ToSim == 6.1), Is.True, "reconnect returned live before the next frame");
            Assert.That(gaps.Any(g => g.Stream == "measurements" && g.Gap && Math.Abs(g.FromSim - 6.01) < 1e-8 && Math.Abs(g.ToSim - 6.05) < 1e-8), Is.True, "failure history survives a later successful poll");
        }

        static string CSharpConstant(string file, string name)
        {
            var source = RepoFile("demos/shared/io.devicechain.demo-platform/Runtime/Platform/" + file + ".cs");
            var expression = Regex.Match(source, @"const string " + name + @"\s*=([\s\S]*?);").Groups[1].Value;
            Assert.That(expression, Is.Not.Empty, file + "." + name);
            return string.Concat(Regex.Matches(expression, "\"(?:\\\\.|[^\"\\\\])*\"").Cast<Match>().Select(m => Json(m.Value).GetString()));
        }

        // A small parser validates every selected path, including aliases and nested results. It reads the committed served SDL,
        // not an SDK schema snapshot, so an unknown field or missing schema is always a hard failure.
        sealed class Tokens
        {
            public readonly string[] Items; public int Index;
            public Tokens(string text) { text = Regex.Replace(text, "\"\"\"[\\s\\S]*?\"\"\"|#[^\\r\\n]*", ""); Items = Regex.Matches(text, "\"(?:\\\\.|[^\"\\\\])*\"|[_A-Za-z][_0-9A-Za-z]*|[!$():@{}\\[\\]=&]").Cast<Match>().Select(m => m.Value).ToArray(); }
            public string Peek => Index < Items.Length ? Items[Index] : "";
            public string Take() => Items[Index++];
            public void Skip(string open, string close) { Assert.That(Take(), Is.EqualTo(open)); int depth = 1; while (depth > 0) { var t = Take(); if (t == open) depth++; if (t == close) depth--; } }
        }
        static string RepoFile(string relative)
        {
            for (var dir = new DirectoryInfo(Directory.GetCurrentDirectory()); dir != null; dir = dir.Parent) {
                var root = Path.Combine(dir.FullName, "backend/services");
                if (Directory.Exists(root)) { var path = Path.Combine(dir.FullName, relative); Assert.That(File.Exists(path), Is.True, "missing committed SDL: " + path); return File.ReadAllText(path); }
            }
            throw new FileNotFoundException("repository root for " + relative);
        }
        static void CheckDocument(string service, string document)
        {
            HarborReadQueries.RequireReadOnly(document);
            Assert.That(document, Does.Not.Contain("credentialValue"));
            var schema = new Tokens(RepoFile("backend/services/" + service + "/graphql/schema.graphql"));
            var types = new Dictionary<string, Dictionary<string, string>>();
            while (schema.Peek != "") {
                var token = schema.Take(); if (token != "type" && token != "interface") continue;
                var name = schema.Take(); while (schema.Peek != "{") schema.Take(); schema.Take(); var fields = new Dictionary<string, string>();
                while (schema.Peek != "}") {
                    var field = schema.Take(); if (schema.Peek == "(") schema.Skip("(", ")"); Assert.That(schema.Take(), Is.EqualTo(":"), name + "." + field);
                    if (schema.Peek == "[") schema.Take(); var type = schema.Take(); while (schema.Peek == "!" || schema.Peek == "]") schema.Take(); fields[field] = type;
                    while (schema.Peek == "@") { schema.Take(); schema.Take(); if (schema.Peek == "(") schema.Skip("(", ")"); }
                }
                schema.Take(); types[name] = fields;
            }
            var query = new Tokens(document); var rootType = query.Take() == "subscription" ? "Subscription" : "Query";
            while (query.Peek != "{") { if (query.Peek == "(") query.Skip("(", ")"); else query.Take(); }
            void Selection(string type, string path) {
                Assert.That(query.Take(), Is.EqualTo("{"));
                while (query.Peek != "}") {
                    var field = query.Take(); if (query.Peek == ":") { query.Take(); field = query.Take(); }
                    Assert.That(types.ContainsKey(type), Is.True, path + ": unknown type " + type);
                    Assert.That(types[type].ContainsKey(field), Is.True, service + ": " + path + "." + field);
                    var resultType = types[type][field]; if (query.Peek == "(") query.Skip("(", ")");
                    if (query.Peek == "{") Selection(resultType, path + "." + field);
                    else Assert.That(types.ContainsKey(resultType), Is.False, "missing sub-selection " + path + "." + field);
                }
                query.Take();
            }
            Selection(rootType, rootType); Assert.That(query.Peek, Is.Empty);
        }
        [Test]
        public void EveryU1AndDirectorDocumentSelectsOnlyServedFields()
        {
            foreach (var d in new[] { HarborReadQueries.DirectorAlarms, HarborReadQueries.AlarmSuppressionStatus, HarborReadQueries.Replacements, HarborReadQueries.Credentials, HarborReadQueries.Membership, HarborReadQueries.Suppressions, HarborReadQueries.Attributes, CSharpConstant("DeviceBinder", "ResolveQuery"), CSharpConstant("ObserverQueries", "ActiveAlarmsQuery"), CSharpConstant("ObserverQueries", "AlarmSubscription") }) CheckDocument("device-management", d);
            foreach (var d in new[] { HarborReadQueries.PumpCommandsOne, HarborReadQueries.PumpCommandsTwo, HarborReadQueries.PumpCommands, HarborReadQueries.DirectorCommands }) CheckDocument("command-delivery", d);
            foreach (var d in new[] { HarborReadQueries.Artifact, HarborReadQueries.Signer, HarborReadQueries.DirectorRollout, HarborReadQueries.Targets, HarborReadQueries.Assignment, HarborReadQueries.Rollout }) CheckDocument("update-management", d);
            CheckDocument("notification-management", HarborReadQueries.NotificationAttempts);
            foreach (var d in new[] { HarborReadQueries.MeasurementEvents, CSharpConstant("ObserverQueries", "MeasurementSubscription") }) CheckDocument("event-management", d);
            foreach (var d in new[] { CSharpConstant("ObserverQueries", "LocationsQuery"), CSharpConstant("ObserverQueries", "PresenceQuery") }) CheckDocument("device-state", d);
        }
    }
}
