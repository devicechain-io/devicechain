// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text.Json;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    public partial class DirectorTestsObserver
    {
        static List<DirectorRead> TerminalChildren(double sim = 26.7)
        {
            var devices = Kit.Layout().Devices.Where(d => d.Kind == DeviceKind.Charger).Select(d => d.Token).ToArray();
            var targets = devices.Select((d, i) => "{\"deviceToken\":" + Q(d) + ",\"outcome\":" + Q(i == 0 ? "FAILED" : "SUCCEEDED") + ",\"assignmentToken\":" + Q("assignment-" + i) + "}").ToArray();
            var result = new List<DirectorRead> { Read("rollout", sim, Rollout("COMPLETED")), Read("targets", sim, Page(targets), "rollout-real") };
            for (var i = 0; i < devices.Length; i++) {
                var assignment = Assignment(i == 0 ? "VERIFICATION_FAILED" : "OTHER", "false").Replace("hp-charger-north-1", devices[i]).Replace("assignment-real", "assignment-" + i).Replace("attempt-real", "attempt-" + i);
                if (i > 0) assignment = assignment.Replace("\"FAILED\"", "\"UPDATED\"");
                result.Add(Read("assignments", sim, assignment, "assignment-" + i));
            }
            return result;
        }
        [TestCase("missing-targets")][TestCase("failed-targets")][TestCase("waiting-targets")]
        [TestCase("stale-targets")][TestCase("missing-assignment")][TestCase("failed-assignment")]
        [TestCase("stale-assignment")][TestCase("missing-failure")][TestCase("partial-targets")]
        [TestCase("missing-success-assignment")][TestCase("unknown-target")][TestCase("truncated-targets")]
        public void TerminalWithoutCompleteChildrenNeverInventsCounts(string fault)
        {
            var data = TerminalChildren(); var targetRead = data.Single(r => r.Name == "targets");
            if (fault == "missing-success-assignment" || fault == "unknown-target") {
                var body = targetRead.Data.Value.GetProperty("updateRolloutTargets").GetRawText();
                body = fault == "unknown-target" ? body.Replace("SUCCEEDED", "UNKNOWN") : body.Replace("\"assignmentToken\":\"assignment-1\"", "\"assignmentToken\":null");
                data[1] = Read("targets", 26.7, body, "rollout-real");
            }
            if (fault.Contains("targets")) data.RemoveAll(r => r.Name == "targets");
            if (fault == "truncated-targets") data.Add(Read("targets", 26.7, targetRead.Data.Value.GetProperty("updateRolloutTargets").GetRawText().Replace("totalRecords\":6", "totalRecords\":7"), "rollout-real"));
            if (fault == "failed-targets") data.Add(Read("targets", 26.7, Page(), "rollout-real", error: "HTTP 503"));
            if (fault == "waiting-targets") data.Add(new DirectorRead("targets", "rollout-real"));
            if (fault == "stale-targets") data.Add(new DirectorRead("targets", "rollout-real", DirectorProbe.PROBE_OK, At(26.7), data: targetRead.Data, asOf: At(26.59)));
            if (fault == "partial-targets") data.Add(Read("targets", 26.7, Page(Target), "rollout-real"));
            if ((fault.Contains("assignment") && fault != "missing-success-assignment") || fault == "missing-failure") {
                var old = data.First(r => r.Name == "assignments"); data.Remove(old);
                if (fault == "failed-assignment") data.Add(Read("assignments", 26.7, Assignment(), "assignment-0", error: "HTTP 503"));
                if (fault == "stale-assignment") data.Add(new DirectorRead("assignments", old.Scope, DirectorProbe.PROBE_OK, At(26.7), data: old.Data, asOf: At(26.59)));
                if (fault == "missing-failure") {
                    var a = DirectorProbeEvaluation.Records("assignments", old.Data.Value).Single().GetRawText();
                    using var doc = JsonDocument.Parse(a); var failure = doc.RootElement.GetProperty("attempt").GetProperty("failure").GetRawText();
                    data.Add(Read("assignments", 26.7, a.Replace(failure, "null"), "assignment-0"));
                }
            }
            var snapshot = new DirectorReadSnapshot(data); var obs = new List<Observation>(); var gaps = new List<CoverageInterval>();
            var adapter = new DirectorObserver(Kit.Layout(), () => snapshot); adapter.Update(Inputs(26.7), obs.Add, gaps.Add);
            Assert.That(obs.Any(o => o.Kind == "rollout" && o.Status == "TERMINAL"), Is.True, "terminal status is independent");
            Assert.That(obs.Any(o => o.Fields.ContainsKey("updated") || o.Fields.ContainsKey("refused")), Is.False);
            Assert.That(adapter.Problems.Any(p => p.Contains("complete child reads")), Is.True);
            Assert.That(gaps.Any(g => g.Stream == "updates" && g.Gap), Is.True);
        }
        [TestCase("VERIFICATION_FAILED", "false", "1")][TestCase("VERIFICATION_FAILED", "true", "0")][TestCase("DOWNLOAD_FAILED", "false", "0")]
        public void TerminalCountsUsePlatformProgressAndEveryFailure(string code, string platform, string refused)
        {
            var data = TerminalChildren();
            data[0] = Read("rollout", 26.7, Rollout("COMPLETED").Replace("\"succeeded\":5", "\"succeeded\":4"));
            var a = data[2]; data[2] = Read("assignments", 26.7, DirectorProbeEvaluation.Records("assignments", a.Data.Value).Single().GetRawText().Replace("VERIFICATION_FAILED", code).Replace("\"platform\":false", "\"platform\":" + platform), a.Scope);
            var obs = new List<Observation>(); new DirectorObserver(Kit.Layout(), () => new DirectorReadSnapshot(data)).Update(Inputs(26.7), obs.Add, _ => { });
            var counts = obs.Single(o => o.Status == "COUNTS");
            Assert.That(counts.Field("updated"), Is.EqualTo("4"), "use platform progress, not target enumeration");
            Assert.That(counts.Field("refused"), Is.EqualTo(refused));
            Assert.That(obs.Single(o => o.Status == "TERMINAL").Field("updated"), Is.Null);
        }
        [Test]
        public void MeasurementTapRetainsIntermediateAboveFloatAndRecording()
        {
            var tap = new DirectorMeasurementTap();
            foreach (var m in new[] { new DirectorMeasurement("hp-pump-north", Metrics.PumpRunning, 0, At(17.1)), new DirectorMeasurement("hp-pump-north", Metrics.WetWell, 3.4, At(17.2)), new DirectorMeasurement("hp-pump-north", Metrics.WetWell, 2.6, At(17.4)) })
                tap.Watch(m);
            var source = new Source(); source.Values["PAW-PUMP-NORTH/wet_well_m"] = new Reading(2.6, At(17.4));
            var snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 17.6, Page(Alarm(raised: 17.5, device: "hp-pump-north", key: "wet-well-high", stamp: null))) }, true, measurements: tap.Records);
            var store = new ObservationStore(); var output = new StringWriter(); var recorder = new Recorder(output, TextWriter.Null, TextWriter.Null, TextWriter.Null);
            new DirectorObserver(Kit.Layout(), () => snapshot).Update(Inputs(17.6, source), o => { store.Add(o); recorder.Observe(696, o); }, _ => { });
            var wells = store.Records.Where(o => o.Key == Metrics.WetWell).ToArray(); Assert.That(wells.Length, Is.EqualTo(2));
            Assert.That(wells.All(o => o.Cycle == Ts(17.5)), Is.True); Assert.That(output.ToString(), Does.Contain("3.4"));
            var trigger = store.Records.Single(o => o.Kind == "alarm");
            store.Add(new Observation("run", "chip", "running", "hp-pump-north", "", Ts(17.5), "", "", "PLATFORM", 696, 692, 17.5333333333, EvidenceSource.Platform,
                new Dictionary<string, string> { ["runningAt"] = "692", ["alarmToken"] = "alarm-real" }));
            store.AddCoverage(new CoverageInterval("run", "pump", 17, 17.6, false));
            var page = DirectorKit.Story().Page("4"); Assert.That(page.Captions.Single(c => c.Id == "before-float").Needs.Evaluate(store, page.Context("run", 696, trigger: trigger)).Holds, Is.False);
        }
        static string JoinedCommand() => "{\"token\":\"command-real\",\"deviceToken\":\"hp-light-east-3\",\"name\":\"derate\",\"status\":\"SENT\",\"queuedTime\":" + Q(Ts(11.11)) + ",\"sentTime\":" + Q(Ts(11.111)) + ",\"cause\":{\"kind\":\"ALARM\",\"ruleId\":\"tenant/hp-light@v1/hp-lamp-flicker\",\"detectionKind\":\"threshold\",\"detectedAt\":" + Q(Ts(11.11)) + ",\"alarmKey\":\"lamp-flicker\",\"deviceId\":\"device-row\"}}";
        static string JoinedAttempt() => "{\"id\":\"attempt-real\",\"alarmToken\":\"alarm-real\",\"attemptedAt\":" + Q(Ts(11.11)) + ",\"status\":\"SENT\",\"trigger\":\"ESCALATION\",\"policyToken\":\"hp-duty-escalation\"}";
        [Test]
        public void ReorderedCycleRecordsDeferUntilAlarmReadCoversOwnInstant()
        {
            var snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 11.1, Page(Alarm())), Read("commands", 11.12, Page(JoinedCommand())), Read("notifications", 11.12, Page(JoinedAttempt())) });
            var obs = new List<Observation>(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
            adapter.Update(Inputs(11.12), obs.Add, _ => { });
            Assert.That(obs.Any(o => o.Kind == "command" || o.Kind == "attempt"), Is.False, "newer lifecycle has not arrived yet");
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 11.14, Page(Alarm(raised: 11.105))), Read("commands", 11.14, Page(JoinedCommand())), Read("notifications", 11.14, Page(JoinedAttempt())) });
            adapter.Update(Inputs(11.14), obs.Add, _ => { });
            Assert.That(obs.Where(o => o.Kind == "command" || o.Kind == "attempt").Count(), Is.EqualTo(3));
            Assert.That(obs.Where(o => o.Kind == "command" || o.Kind == "attempt").All(o => o.Cycle == Ts(11.105)), Is.True);
        }
        [Test]
        public void RepeatedAlarmCyclesDoNotRekeyUnchangedCommandsOrAttempts()
        {
            var snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 11.12, Page(Alarm(raised: 11.105))), Read("commands", 11.12, Page(JoinedCommand())), Read("notifications", 11.12, Page(JoinedAttempt())) });
            var obs = new List<Observation>(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot); adapter.Update(Inputs(11.12), obs.Add, _ => { });
            var keys = obs.Where(o => o.Kind == "command" || o.Kind == "attempt").Select(o => o.EventKey).ToArray();
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 11.16, Page(Alarm(raised: 11.15))), Read("commands", 11.16, Page(JoinedCommand())), Read("notifications", 11.16, Page(JoinedAttempt())) });
            adapter.Update(Inputs(11.16), obs.Add, _ => { });
            Assert.That(obs.Where(o => o.Kind == "command" || o.Kind == "attempt").Select(o => o.EventKey), Is.EqualTo(keys));
        }
        [Test]
        public void SnapshotStatesCannotManufactureOccurrenceOrderingOrArtifactHistory()
        {
            var snapshot = new DirectorReadSnapshot(new[] { Read("rollout", 26.4, Rollout()), Read("artifact", 26.4, Artifact), Read("signer", 26.4, Signer), Read("targets", 26.4, Page(Target), "rollout-real"), Read("credentials", 26.4, "[" + Credential() + "]"), Read("replacements", 26.4, Page(Journal())), Read("membership", 26.4, Page(), "hp-east-quay") });
            var store = new ObservationStore(); new DirectorObserver(Kit.Layout(), () => snapshot).Update(Inputs(26.4), o => store.Add(o), _ => { });
            foreach (var o in store.Records.Where(o => new[] { "artifact", "target", "credential", "membership" }.Contains(o.Kind))) {
                Assert.That(o.Field("asOf"), Is.Not.Null); Assert.That(o.HasOccurrence, Is.False); Assert.That(o.OccurredAtS, Is.EqualTo(o.ObservedAtS));
                Assert.That(o.Field("occurredAtS"), Is.Null);
                foreach (var relation in new[] { "before", "after" }) {
                    var need = DirectorKit.Need("{" + Q(o.Kind) + ":{" + Q(relation) + ":\"$trigger.occurredAtS\"}}");
                    var trigger = DirectorKit.O("rollout", sim: 26.4, wall: o.ObservedAtS + (relation == "before" ? 1 : -1));
                    Assert.That(need.Evaluate(store, new EvidenceContext("run", 6, 28, 2000, trigger: trigger)).Holds, Is.False, o.Kind + " " + relation);
                }
            }
            Assert.That(store.Records.Single(o => o.Kind == "artifact").OccurredAtS, Is.GreaterThan(1200), "trust was not observed at rollout start");
        }
        [Test]
        public void EmptyMembershipReplacesExpectedSetAndAllQuaysControlMap()
        {
            var layout = Kit.Layout(); var source = new Source(); var data = layout.Devices.GroupBy(d => d.Quay).Select(g => Read("membership", 20.6, Page(g.Select(d => "{\"token\":" + Q(d.Token) + "}").ToArray()), g.Key)).ToList();
            var snapshot = new DirectorReadSnapshot(data); var obs = new List<Observation>(); var adapter = new DirectorObserver(layout, () => snapshot);
            data.Add(Read("alarms", 20.6, Page(Alarm(device: "hp-light-east-1", key: "light-silent", raised: 20.5, stamp: null))));
            snapshot = new DirectorReadSnapshot(data); adapter.Update(Inputs(20.6), obs.Add, _ => { });
            data.RemoveAll(r => r.Name == "membership" && r.Scope == "hp-east-quay"); data.Add(Read("membership", 20.61, Page(), "hp-east-quay"));
            // Move one known north device to Basin, with a complete replacement of both snapshots.
            var north = data.Single(r => r.Name == "membership" && r.Scope == "hp-north-quay"); data.Remove(north);
            data.Add(Read("membership", 20.61, Page(layout.Devices.Where(d => d.Quay == "hp-north-quay" && d.Token != "hp-pump-north").Select(d => "{\"token\":" + Q(d.Token) + "}").ToArray()), "hp-north-quay"));
            var basin = data.Single(r => r.Name == "membership" && r.Scope == "hp-basin"); data.Remove(basin);
            data.Add(Read("membership", 20.61, Page(layout.Devices.Where(d => d.Quay == "hp-basin" || d.Token == "hp-pump-north").Select(d => "{\"token\":" + Q(d.Token) + "}").ToArray()), "hp-basin"));
            snapshot = new DirectorReadSnapshot(data); adapter.Update(Inputs(20.61), obs.Add, _ => { });
            Assert.That(obs.Last(o => o.Identity == "east-absence").Sets["tokens"], Is.Empty);
            Assert.That(PresentationMembership.TryRead(layout, snapshot, id => source.TryToken(id, out var token) ? token : null, At(20.61), out var members, out var problem), Is.True, problem);
            var map = new PresentationMap(layout, members); Assert.That(map.QuayFor(layout.Devices.Single(d => d.Token == "hp-pump-north")), Is.EqualTo("hp-basin"));
            Assert.That(map.Pose("pose:pump-north:well").Camera.X, Is.GreaterThan(map.Anchors["hp-pump-north"].X), "moved close pose still stands on its new quay water side");
            Assert.That(map.ReadinessWarnings.Any(w => w.Contains("differs")), Is.True); Assert.That(map.ReadinessWarnings.Any(w => w.Contains("unknown")), Is.True);
            Assert.That(PresentationMembership.TryRead(layout, new DirectorReadSnapshot(data.Where(r => r.Scope != "hp-basin")), id => source.TryToken(id, out var token) ? token : null, At(20.61), out _, out problem), Is.False); Assert.That(problem, Does.Contain("waiting/failed"));
        }
        [Test]
        public void SuppressionBackendEnumsProducePlatformWindow()
        {
            Assert.That(RepoFile("backend/services/device-management/model/model_alarm_suppression.go"), Does.Contain("\"ONE_SHOT\""));
            var snapshot = new DirectorReadSnapshot(new[] { Read("windows", 11.1, "[" + Window() + "]") }); var obs = new List<Observation>();
            new DirectorObserver(Kit.Layout(), () => snapshot).Update(Inputs(11.1), obs.Add, _ => { });
            Assert.That(obs.Single(o => o.Kind == "window").Device, Is.EqualTo("hp-light-east-3"));
        }
        [Test]
        public void PageTwoClaimsOnlyRecordedAbsenceAndKeepsAuthoredLaw()
        {
            var page = DirectorKit.Story().Page("2"); var caption = page.Captions.Single(c => c.Id == "none-work");
            Assert.That(string.Join("\n", caption.Lines), Is.EqualTo("No notification recorded\nwhile the work is on."));
            Assert.That(caption.Lines.All(l => l.Length <= 42), Is.True);
            Assert.That(page.Nodes.Single(n => n.Id == "none").Label, Does.Contain("recorded"));
        }
        [Test]
        public void RecordingFailureSurvivesDigestChecksUntilEstablished()
        {
            var status = new RecordingStatus(); Assert.That(status.Establish<object>(() => throw new IOException("unwritable directory")), Is.Null);
            LayoutDigest.Matches("same", "same", out _); Assert.That(status.Problem, Does.Contain("unwritable directory"));
            status.Establish<object>(() => throw new IOException("still unwritable")); Assert.That(status.Problem, Does.Contain("still unwritable"));
            Assert.That(status.Establish(() => new object()), Is.Not.Null); Assert.That(status.Problem, Is.Null);
        }
        [Test]
        public void SixtySecondReconnectRetainsFrozenCaptionOpportunity()
        {
            var scheduler = new Scheduler(DirectorKit.Story(), Kit.Layout()); var store = DirectorTimeline.Clean();
            scheduler.Tick(new DirectorSample("run", 6, 0, defects: new[] { "reconnect-test" }), store);
            scheduler.Tick(new DirectorSample("run", 6, 1, stale: true), store); scheduler.Tick(new DirectorSample("run", 6, 3, stale: true), store);
            store.Add(DirectorKit.O("binding", status: "READY", sim: 6, wall: 61, fields: new Dictionary<string, string> { ["freshDevices"] = "43" }));
            for (var t = 61; t <= 67; t++) {
                store.Add(DirectorKit.O("binding", status: "READY", sim: 6, wall: t, fields: new Dictionary<string, string> { ["freshDevices"] = "43" }));
                scheduler.Tick(new DirectorSample("run", 6, t), store);
            }
            Assert.That(scheduler.Log.Any(e => e.Kind == "caption" && e.ShowS >= 61 && e.Value.Contains("43 devices")), Is.True);
        }
        [TestCase("caption")][TestCase("title")][TestCase("visible-caption")]
        public void CaptionCheckerRejectsFinalShortCaptionAtEofIncludingTitle(string kind)
        {
            var output = new StringWriter(); var recorder = new Recorder(TextWriter.Null, TextWriter.Null, output, TextWriter.Null);
            recorder.Decision(new DirectorEvent(0, kind, "", "final text")); recorder.Decision(new DirectorEvent(1, "sim-hour", "", "6"));
            Assert.That(DirectorLogCheck.Check(new StringReader(output.ToString())).Single(), Does.Contain("visible 1 s"));
        }
        [Test]
        public void CaptionCheckerRejectsEmptyAndUsesRecordingEndForLastTitle()
        {
            Assert.That(DirectorLogCheck.Check(new StringReader("")), Is.Not.Empty);
            var output = new StringWriter(); var recorder = new Recorder(TextWriter.Null, TextWriter.Null, output, TextWriter.Null);
            recorder.Decision(new DirectorEvent(0, "title", "", "title")); recorder.Advance(3); recorder.End();
            Assert.That(DirectorLogCheck.Check(new StringReader(output.ToString())), Is.Empty);
        }
    
        [Test]
        public void PlatformBoundaryInstantRoundsOntoTheAuthoredHourAndBindsAWindowOpenedInPreRoll()
        {
            // Live: the 10:45 window's platform startTime converts to 10.7499999967 sim hours, just outside a page that starts at 10.75.
            Assert.That(DirectorObserver.RoundSim(10.749999996666668), Is.EqualTo(10.75));
            var window = new Dictionary<string, string>();
            var need = "{\"window\":{\"device\":\"hp-light-east-3\",\"key\":\"lamp-flicker\",\"status\":\"ACTIVE\",\"mode\":\"current\"}}";
            var alarm = DirectorKit.O("alarm", "hp-light-east-3", "lamp-flicker", "RAISED", sim: 11.1666, wall: 311.5, id: "alarm-token", cycle: "c1",
                fields: new Dictionary<string, string> { ["raisedDuringSuppression"] = "true", ["suppressionWindowToken"] = "service" });
            var context = new EvidenceContext("run", 10.75, 12.1, 312, trigger: alarm);
            var store = new ObservationStore();
            store.Add(DirectorKit.O("window", "hp-light-east-3", "lamp-flicker", "ACTIVE", sim: DirectorObserver.RoundSim(10.749999996666668), wall: 284.2, id: "service"));
            store.Add(alarm);
            Assert.That(DirectorKit.Need(need).Evaluate(store, context).Holds, Is.True);
            // The suppression stamp binds the window that was active at the alarm's instant, wherever the page's own window starts.
            var preRoll = new ObservationStore();
            preRoll.Add(DirectorKit.O("window", "hp-light-east-3", "lamp-flicker", "ACTIVE", sim: 10.7, wall: 281, id: "service")); preRoll.Add(alarm);
            var stamp = "{\"alarm\":{\"device\":\"hp-light-east-3\",\"key\":\"lamp-flicker\",\"status\":\"RAISED\",\"fields\":{\"raisedDuringSuppression\":\"true\",\"suppressionWindowToken\":\"$window.identity\"},\"cycle\":\"$trigger.cycle\",\"mode\":\"occurred\",\"identity\":\"$trigger.identity\"}}";
            Assert.That(DirectorKit.Need(stamp).Evaluate(preRoll, context).Holds, Is.True);
            // Control: a different window's identity never satisfies the stamp.
            var other = new ObservationStore();
            other.Add(DirectorKit.O("window", "hp-light-east-3", "lamp-flicker", "ACTIVE", sim: 10.7, wall: 281, id: "another-window")); other.Add(alarm);
            Assert.That(DirectorKit.Need(stamp).Evaluate(other, context).Holds, Is.False);
        }
}
}
