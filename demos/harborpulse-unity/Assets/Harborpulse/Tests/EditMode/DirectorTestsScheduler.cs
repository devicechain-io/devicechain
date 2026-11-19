// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    static class DirectorTimeline
    {
        public static ObservationStore Clean(string defect = "")
        {
            var s = new ObservationStore();
            void Add(string kind, string device, string key, string status, double sim, string id = null, string source = EvidenceSource.Platform,
                Dictionary<string, string> fields = null, Dictionary<string, string[]> sets = null)
            {
                if (defect == "truck-stays-home" && device == "hp-truck-1") return;
                if (defect == "replacement-new-device" && (kind == "replacement" || kind == "credential" || (fields != null && fields.ContainsKey("afterReplacement")))) return;
                if (defect == "no-command-far-end" && kind == "command" && key == "start-pump") return;
                if (defect == "no-command-far-end" && kind == "chip") status = "FLOAT-CONSISTENT";
                if (defect == "refuse-derate" && kind == "command" && key == "derate") status = "FAILED";
                if (defect == "suppression-wrong-device" && key == "lamp-flicker" && kind == "alarm") fields = null;
                if (defect == "replacement-old-credential-left-live" && kind == "credential") fields["enabled"] = "true";
                if (defect == "ota-signature-omitted" && kind == "artifact") status = "UNVERIFIED";
                if (defect == "ota-cap-three" && kind == "rollout" && fields != null) fields["maxInFlight"] = "3";
                if (defect == "ota-failure-disabled" && kind == "attempt" && status == "FAILED") return;
                if (defect == "smtp-sink-stopped" && kind == "attempt" && status == "SENT") return;
                if (defect == "acked-before-delay" && kind == "attempt" && fields != null && fields.ContainsKey("trigger")) return;
                if (defect == "air-lock-basin" && key == "pump-air-lock") device = "hp-pump-basin";
                if (defect == "drop-backfill" && id == "back-2") return;
                s.Add(DirectorKit.O(kind, device, key, status, sim, (sim - 6) * 60, id ?? kind + ":" + device + ":" + key + ":" + status,
                    source: source, fields: fields, sets: sets));
            }
            Add("binding", "", "", "READY", 6, fields: new Dictionary<string, string> { ["freshDevices"] = "37" });
            Add("alarm", "hp-meter-ferry-terminal", "peak-demand", "RAISED", 8.5);
            Add("alarm", "hp-meter-ferry-terminal", "peak-demand", "CLEARED", 8.65);
            Add("window", "hp-light-east-3", "lamp-flicker", "ACTIVE", 10.75, "service-this-run");
            Add("window", "hp-light-east-3", "", "ACTIVE", 10.75, source: EvidenceSource.Scenario);
            Add("alarm", "hp-light-east-3", "lamp-flicker", "RAISED", 11, fields: new Dictionary<string, string> { ["raisedDuringSuppression"] = "true", ["suppressionWindowToken"] = "service-this-run" });
            Add("window", "hp-light-east-3", "", "ENDED", 11.5);
            Add("attempt", "hp-light-east-3", "lamp-flicker", "SENT", 11.52, fields: new Dictionary<string, string> { ["policy"] = "hp-duty-escalation", ["afterClose"] = "true", ["alarmActive"] = "true" });
            Add("replacement", "hp-gauge-north-3", "", "RECORDED", 12.5, sets: new Dictionary<string, string[]> { ["disabledCredentials"] = new[] { "old-credential" } });
            Add("replacement", "hp-gauge-north-3", "", "", 12.5, source: EvidenceSource.Scenario);
            Add("measure", "hp-gauge-north-3", "water_level_m", "", 12.51, fields: new Dictionary<string, string> { ["afterReplacement"] = "true" });
            Add("credential", "hp-gauge-north-3", "", "", 12.51, "old-credential", fields: new Dictionary<string, string> { ["enabled"] = "false" });
            Add("alarm", "hp-charger-basin-2", "connector-hot", "RAISED", 14);
            Add("command", "hp-charger-basin-2", "derate", "SUCCESSFUL", 14.02, fields: new Dictionary<string, string> { ["max_kw"] = "11" });
            Add("measure", "hp-charger-basin-2", "max_kw", "", 14.1, fields: new Dictionary<string, string> { ["value"] = "11" });
            Add("alarm", "hp-charger-basin-2", "connector-hot", "CLEARED", 14.1);
            Add("alarm", "hp-gauge-east-1", "flood-level", "ESCALATED", 17);
            Add("measure", "hp-gauge-east-1", "water_level_m", "", 17, fields: new Dictionary<string, string> { ["rising"] = "true" });
            Add("measure", "hp-pump-north", "wet_well_m", "", 17.2, fields: new Dictionary<string, string> { ["value"] = "2.6" });
            Add("alarm", "hp-pump-north", "wet-well-high", "RAISED", 17.5);
            Add("command", "hp-pump-north", "start-pump", "SENT", 17.51);
            Add("chip", "hp-pump-north", "", "PLATFORM", 17.52, fields: new Dictionary<string, string> { ["runningAt"] = "692" });
            Add("measure", "hp-pump-north", "wet_well_m", "", 17.58, "well-falling", fields: new Dictionary<string, string> { ["value"] = "2.2", ["falling"] = "true" });
            Add("alarm", "hp-truck-1", "truck-in-flood-plain", "RAISED", 17.75);
            Add("alarm", "hp-pump-east", "pump-air-lock", "RAISED", Scheduler.WallOffsetSim(19, Kit.Layout().Cues.AirLockHoldS, Kit.Layout().DayLengthSeconds));
            Add("window", "", "feeder-outage", "ACTIVE", 19.75, source: EvidenceSource.Scenario);
            var expected = new[] { "hp-light-east-1", "hp-light-east-2", "hp-light-east-3", "hp-light-east-4", "hp-charger-east-1", "hp-charger-east-2" };
            Add("membership", "", "", "", 20.5, "east-absence", sets: new Dictionary<string, string[]> { ["tokens"] = expected });
            var detected = Scheduler.WallOffsetSim(19.75, Kit.Layout().Cues.SilenceS, Kit.Layout().DayLengthSeconds);
            foreach (var d in expected) Add("alarm", d, d.Contains("light") ? "light-silent" : "charger-offline", "RAISED", detected);
            if (defect == "silence-north-lamp") Add("alarm", "hp-light-north-1", "light-silent", "RAISED", detected);
            Add("attempt", "", "absence", "SENT", 20.6);
            Add("attempt", "", "absence", "SENT", Scheduler.WallOffsetSim(20.6, Kit.Layout().Cues.EscalateAfterS, Kit.Layout().DayLengthSeconds), "escalation", fields: new Dictionary<string, string> { ["trigger"] = "ESCALATION" });
            Add("window", "", "feeder-outage", "ENDED", 21.25, source: EvidenceSource.Scenario);
            Add("measure", "hp-charger-east-1", "backfill", "", 21.28, "back-1", fields: new Dictionary<string, string> { ["backdated"] = "true", ["readingIdentity"] = "charger-1@outage-time-1" });
            Add("measure", "hp-charger-east-2", "backfill", "", 21.29, "back-2", fields: new Dictionary<string, string> { ["backdated"] = "true", ["readingIdentity"] = "charger-2@outage-time-2" });
            Add("ledger", "", "backfill", "", 21.3, source: EvidenceSource.Scenario, sets: new Dictionary<string, string[]> { ["times"] = new[] { "charger-1@outage-time-1", "charger-2@outage-time-2" } });
            Add("ledger", "", "restore", "", 21.3, source: EvidenceSource.DeviceSide, fields: new Dictionary<string, string> { ["value"] = "3" });
            Add("alarm", "hp-charger-east-1", "charger-offline", "CLEARED", 21.6);
            Add("alarm", "hp-truck-1", "truck-in-flood-plain", "CLEARED", 21.75);
            Add("rollout", "", "", "RUNNING", 26, fields: new Dictionary<string, string> { ["maxInFlight"] = "2" });
            Add("artifact", "", "", "VERIFIED", 26, fields: new Dictionary<string, string> { ["signer"] = "works-key" });
            Add("target", "", "", "SUCCEEDED", 26.15);
            Add("attempt", "hp-charger-north-1", "", "FAILED", 26.3, fields: new Dictionary<string, string> { ["code"] = "VERIFICATION_FAILED", ["platform"] = "false" });
            Add("measure", "hp-charger-north-1", "power_kw", "", 26.31, fields: new Dictionary<string, string> { ["afterFailure"] = "true" });
            Add("rollout", "", "", "TERMINAL", 26.6, fields: new Dictionary<string, string> { ["updated"] = defect == "ota-failure-disabled" ? "6" : "5", ["refused"] = defect == "ota-failure-disabled" ? "0" : "1", ["maxInFlight"] = "2" });
            foreach (var stream in new[] { "alarms", "notifications", "credentials", "pump", "measurements" }) s.AddCoverage(new CoverageInterval("run", stream, 6, 30, false));
            if (defect == "stream-cut") { s.AddCoverage(new CoverageInterval("run", "alarms", 20, 21, true)); s.AddCoverage(new CoverageInterval("run", "notifications", 11, 11.1, true)); }
            return s;
        }
        public static Scheduler Run(string defect = "")
        {
            var s = new Scheduler(DirectorKit.Story(), Kit.Layout()); var store = Clean(defect);
            for (var wall = 0; wall <= 1260; wall++) s.Tick(new DirectorSample("run", 6 + wall / 60.0, wall, defects: defect == "" ? null : new[] { defect }), store);
            return s;
        }
    }

    public class DirectorTestsScheduler
    {
        static Storyboard ArbitrationStory(double lowEnd = 10, double queueTtl = 300)
        {
            var root = DirectorKit.Object(DirectorKit.StoryText());
            foreach (var value in DirectorKit.Arr(root["pages"]))
            {
                var p = DirectorKit.Obj(value); var id = (string)p["id"];
                p["windowSim"] = new List<object> { 40, 41 }; p["plannedSim"] = null; p["trigger"] = null; p["watch"] = null;
                if (!new[] { "1", "1b", "2", "3b" }.Contains(id)) continue;
                p["windowSim"] = new List<object> { 6, id == "1" ? lowEnd : 10 }; p["queueTtlS"] = queueTtl;
                p["priority"] = id == "1b" ? 100 : id == "1" ? 50 : 10;
                p["trigger"] = DirectorKit.Object("{\"alarm\":{\"key\":\"" + id + "\",\"status\":\"RAISED\"}}");
                if (id == "1") p["plannedSim"] = 6;
                foreach (var s in DirectorKit.Arr(p["shots"])) { var shot = DirectorKit.Obj(s); shot["cueSim"] = 6; }
            }
            return Storyboard.Parse(DirectorKit.Json(root), new PresentationMap(Kit.Layout()));
        }
        static Scheduler Arbitrate(double lowEnd = 10, double ttl = 300)
        {
            var scheduler = new Scheduler(ArbitrationStory(lowEnd, ttl), Kit.Layout()); var store = new ObservationStore();
            store.Add(DirectorKit.O("binding", status: "READY", sim: 6, wall: 0, fields: new Dictionary<string, string> { ["freshDevices"] = "37" }));
            store.Add(DirectorKit.O("alarm", key: "1", status: "RAISED", sim: 6, wall: 0));
            for (var t = 0; t <= 150; t++)
            {
                if (t == 1) store.Add(DirectorKit.O("alarm", key: "1b", status: "RAISED", sim: 6.01, wall: 1));
                if (t == 2) foreach (var key in new[] { "2", "3b" }) store.Add(DirectorKit.O("alarm", key: key, status: "RAISED", sim: 6.02, wall: 2, id: key));
                scheduler.Tick(new DirectorSample("run", 6 + t / 60.0, t), store);
            }
            return scheduler;
        }
        [Test]
        public void Rule1_HighPriorityPreemptsAtCaptionBoundaryWithOneActivePage()
        {
            var s = Arbitrate(); var preemption = s.Log.First(e => e.Kind == "preempt");
            Assert.That(preemption.ShowS, Is.EqualTo(3)); Assert.That(preemption.Page, Is.EqualTo("1")); Assert.That(preemption.Value, Is.EqualTo("1b"));
            var playing = new HashSet<string>();
            foreach (var e in s.Log)
            {
                if (e.Kind == "preempt") playing.Remove(e.Page);
                if ((e.Kind == "state" && e.Value == "Staged") || e.Kind == "resume") playing.Add(e.Page);
                if (e.Kind == "state" && e.Value == "Presented") playing.Remove(e.Page);
                Assert.That(playing.Count, Is.LessThanOrEqualTo(1));
            }
        }
        [Test]
        public void Rule2_LowerTriggersQueueByPriorityThenTimeThenIdAndExpire()
        {
            var s = Arbitrate(); var shots = s.Log.Where(e => e.Kind == "shot").Select(e => e.Page).ToArray();
            Assert.That(Array.IndexOf(shots, "2"), Is.LessThan(Array.IndexOf(shots, "3b")));
            Assert.That(s.Log.Count(e => e.Kind == "preempt"), Is.EqualTo(1));
            var expired = Arbitrate(ttl: 1); Assert.That(expired.Log.Where(e => e.Kind == "missed").Select(e => e.Page), Does.Contain("2"));
            Assert.That(expired.Log.Any(e => e.Kind == "shot" && e.Page == "2"), Is.False);
        }
        [Test]
        public void Rule3_PreemptedPageResumesLastCaptionOrMissesClosedWindow()
        {
            var s = Arbitrate(); Assert.That(s.Log.Any(e => e.Kind == "resume" && e.Page == "1"), Is.True);
            var baseline = s.Log.Where(e => e.Kind == "caption" && e.Page == "1").Take(2).ToArray();
            Assert.That(baseline.Length, Is.EqualTo(2)); Assert.That(baseline[1].Value, Is.EqualTo(baseline[0].Value));
            var closed = Arbitrate(lowEnd: 6.1); Assert.That(closed.Log.Any(e => e.Kind == "resume" && e.Page == "1"), Is.False);
            Assert.That(closed.Log.Any(e => e.Kind == "missed" && e.Page == "1" && e.Value.Contains("resume window")), Is.True);
        }
        [Test]
        public void CleanRunExactPageAndShotSequence()
        {
            var s = DirectorTimeline.Run();
            var actual = s.Log.Where(e => e.Kind == "shot").Select(e => e.Page + ":" + e.Value).ToArray();
            var expected = new[] { "1:1a", "1:1b", "1b:1b-1", "2:2a", "2:2b", "2:2c", "2:2d", "2:2e", "3:3a", "3:3b", "3:3c", "3b:3b-1", "3b:3b-2", "3b:3b-3", "4:4a", "4:4b", "4:4c", "4:4d", "4:4e", "4:4f", "4:4g", "5:5a", "5:5b", "4b:4b-1", "6:6a", "6:6b", "6:6c", "6:6d", "7:7a", "7:7b", "7:7c", "7:7d", "7:7e", "7:7f", "8:8a", "8:8b", "8:8c", "8:8d", "9:9a" };
            Assert.That(actual, Is.EqualTo(expected), string.Join(", ", actual));
        }
        [Test]
        public void EveryVisibleCaptionHasThreeSecondsMechanically()
        {
            var s = DirectorTimeline.Run(); var open = new Dictionary<string, double>();
            foreach (var e in s.Log)
            {
                if (e.Kind == "caption") { Assert.That(open.ContainsKey(e.Page), Is.False); open[e.Page] = e.ShowS; }
                if (e.Kind == "caption-end") { Assert.That(e.ShowS - open[e.Page], Is.GreaterThanOrEqualTo(3), e.Page + ":" + e.Value); open.Remove(e.Page); }
            }
            Assert.That(open, Is.Empty);
        }
        // Each control changes evidence, never scheduler code. Compare the entire claim/node truth matrix.
        static Dictionary<string, string> Proofs(ObservationStore store)
        {
            var result = new Dictionary<string, string>();
            foreach (var p in DirectorKit.Story().Pages)
            {
                var now = (p.ToSim - 6) * 60;
                var trigger = p.Trigger?.Evaluate(store, p.Context("run", now)).Records.LastOrDefault();
                foreach (var c in p.Captions.Where(c => c.Kind == "claim"))
                { var evidence = c.Needs.Evaluate(store, p.Context("run", now, c.Source, trigger)); if (evidence.Holds) result[p.Id + ":caption:" + c.Id] = c.Render(evidence); }
                foreach (var n in p.Nodes) if (n.Needs.Evaluate(store, p.Context("run", now, n.Source, trigger)).Holds) result[p.Id + ":node:" + n.Id] = n.Label;
            }
            return result;
        }
        [TestCase("truck-stays-home", "5:caption:truck,7:caption:truck-out")]
        [TestCase("silence-north-lamp", "6:caption:exact,6:node:absence")]
        [TestCase("refuse-derate", "3b:caption:derate,3b:caption:cool")]
        [TestCase("no-command-far-end", "4:caption:platform-start,4:caption:before-float,4:node:command,4:node:running")]
        [TestCase("air-lock-basin", "")]
        [TestCase("drop-backfill", "7:caption:all-buffered,7:node:stored")]
        [TestCase("suppression-wrong-device", "2:caption:fault,2:node:fault")]
        [TestCase("replacement-new-device", "3:caption:new-hardware,3:caption:same,3:caption:disabled,3:node:swapped,3:node:journal,3:node:same,3:node:disabled")]
        [TestCase("replacement-old-credential-left-live", "3:caption:disabled,3:node:disabled")]
        [TestCase("ota-signature-omitted", "8:caption:signed,8:node:signed")]
        [TestCase("ota-cap-three", "8:caption:ota-summary")]
        [TestCase("ota-failure-disabled", "8:caption:refused,8:caption:failed,8:caption:working,8:caption:ota-summary,8:node:refused")]
        [TestCase("smtp-sink-stopped", "2:caption:work-sent,2:node:sent,6:node:notify,6:node:escalated")]
        [TestCase("acked-before-delay", "6:node:escalated")]
        [TestCase("stream-cut", "2:caption:none-work,2:node:none,6:caption:exact,6:node:absence")]
        public void ControlsChangeOnlyDeclaredClaimsOrNodes(string control, string differences)
        {
            var clean = Proofs(DirectorTimeline.Clean()); var actual = Proofs(DirectorTimeline.Clean(control));
            var changed = clean.Keys.Union(actual.Keys).Where(k => !clean.ContainsKey(k) || !actual.ContainsKey(k) || clean[k] != actual[k]).ToArray();
            Assert.That(changed, Is.EquivalentTo(differences == "" ? Array.Empty<string>() : differences.Split(',')));
            var run = DirectorTimeline.Run(control); Assert.That(run.Banner, Does.StartWith("TEST CONTROL: "));
            var unaffectedPages = DirectorKit.Story().Pages.Select(p => p.Id).Where(p => !changed.Any(k => k.StartsWith(p + ":", StringComparison.Ordinal))).ToArray();
            var cleanRun = DirectorTimeline.Run();
            foreach (var p in unaffectedPages.Where(p => p != "5")) Assert.That(run.Log.Where(e => e.Page == p && e.Kind == "shot").Select(e => e.Value), Is.EqualTo(cleanRun.Log.Where(e => e.Page == p && e.Kind == "shot").Select(e => e.Value)), control + ":" + p);
            if (control == "air-lock-basin") Assert.That(run.Log.Single(e => e.Kind == "follow").Value, Is.EqualTo("hp-pump-basin"));
        }
        [Test]
        public void OtaRetiresOnlyByPresentationOrPreJoinTerminal()
        {
            var s = new Scheduler(DirectorKit.Story(), Kit.Layout()); var store = DirectorTimeline.Clean();
            s.Tick(new DirectorSample("run", 26, 1200), store);
            Assert.That(s.ActivePage, Is.EqualTo("8")); Assert.That(s.State("8"), Is.EqualTo(PageState.Playing));
            var late = new Scheduler(DirectorKit.Story(), Kit.Layout()); late.Tick(new DirectorSample("run", 26.9, 1254), store);
            Assert.That(late.State("8"), Is.EqualTo(PageState.Presented));
            Assert.That(DirectorTimeline.Run().Log.Count(e => e.Kind == "shot" && e.Page == "8"), Is.EqualTo(4));
        }
        [Test]
        public void LateJoinRecapConsumesOldEventsAndResubscribeCannotRetrigger()
        {
            var store = DirectorTimeline.Clean(); var s = new Scheduler(DirectorKit.Story(), Kit.Layout());
            s.Tick(new DirectorSample("run", 17.6, 696), store);
            Assert.That(s.Log.Any(e => e.Kind == "recap" && e.Page == "4"), Is.True);
            Assert.That(s.Log.Any(e => e.Kind == "caption" && e.Page == "4" && e.Value == "The platform starts the pump."), Is.True);
            Assert.That(s.Consumed, Does.Contain(store.Records.Single(o => o.Kind == "binding").EventKey));
            var keys = s.Consumed.Count;
            for (var t = 697; t <= 706; t++) s.Tick(new DirectorSample("run", 17.6, t), store);
            Assert.That(s.Log.Count(e => e.Kind == "recap" && e.Page == "4"), Is.EqualTo(1)); Assert.That(s.Consumed.Count, Is.GreaterThanOrEqualTo(keys));
            Assert.That(s.Log.Any(e => e.Kind == "reaction" && e.Page == "4"), Is.False);
        }
        [Test]
        public void RepeatsAreFiveSecondsAndAtMostOnePerThreeSimHours()
        {
            var root = DirectorKit.Object(DirectorKit.StoryText());
            var p = DirectorKit.Obj(DirectorKit.Arr(root["pages"])[1]); p["windowSim"] = new List<object> { 6, 20 };
            p["trigger"] = DirectorKit.Object("{\"alarm\":{\"key\":\"repeat\",\"status\":\"RAISED\"}}");
            foreach (var other in DirectorKit.Arr(root["pages"]).Skip(2)) DirectorKit.Obj(other)["windowSim"] = new List<object> { 40, 41 };
            var scheduler = new Scheduler(Storyboard.Parse(DirectorKit.Json(root), new PresentationMap(Kit.Layout())), Kit.Layout()); var store = new ObservationStore();
            store.Add(DirectorKit.O("alarm", key: "repeat", status: "RAISED", sim: 6, wall: 0));
            store.Add(DirectorKit.O("binding", status: "READY", sim: 6, wall: 0, fields: new Dictionary<string, string> { ["freshDevices"] = "37" }));
            for (var t = 0; t <= 300; t++)
            {
                if (t == 140 || t == 150 || t == 160) store.Add(DirectorKit.O("alarm", key: "repeat", status: "RAISED", sim: 6 + t / 60.0, wall: t, id: "repeat-" + t, cycle: "cycle-" + t));
                scheduler.Tick(new DirectorSample("run", 6 + t / 60.0, t), store);
            }
            var reaction = scheduler.Log.Single(e => e.Kind == "reaction");
            Assert.That(scheduler.Log.First(e => e.Kind == "state" && e.Page == "1" && e.Value == "Presented" && e.ShowS > reaction.ShowS).ShowS - reaction.ShowS, Is.EqualTo(5));
        }
        [Test]
        public void StaleFreezesAndMismatchDisablesDirectorButNavigationWorks()
        {
            var s = new Scheduler(DirectorKit.Story(), Kit.Layout()); var store = DirectorTimeline.Clean();
            s.Tick(new DirectorSample("run", 17, 660, stale: true), store); Assert.That(s.ActivePage, Is.Null); Assert.That(s.Banner, Is.EqualTo("reconnecting"));
            s.Tick(new DirectorSample("run", 17.5, 690, digestMatches: false), store); Assert.That(s.Mode, Is.EqualTo(DirectorMode.LayoutMismatch)); Assert.That(s.Banner, Is.EqualTo("layout mismatch: director off"));
            var roam = new FreeRoam(); roam.Input(690, "W"); Assert.That(roam.DirectorWeight(690), Is.Zero);
        }
        [Test]
        public void WallDurationsUseLayoutAndDayLength()
        { var s = new Scheduler(DirectorKit.Story(), Kit.Layout()); Assert.That(s.ExpectedSilenceSim(19.75), Is.EqualTo(20.5)); Assert.That(s.ExpectedAirLockSim, Is.EqualTo(19.5)); Assert.That(Scheduler.WallOffsetSim(19.75, 45, 2880), Is.EqualTo(20.125)); }
        [TestCase("E")] [TestCase("D")] [TestCase("H")] [TestCase("Space")]
        public void ReservedKeysNeverTakeCamera(string key)
        { var r = new FreeRoam(); r.Input(10, key); Assert.That(r.DirectorWeight(10), Is.EqualTo(1)); }
        [TestCase("W")] [TestCase("A")] [TestCase("S")] [TestCase("Q")] [TestCase("ArrowUp")] [TestCase("drag")] [TestCase("wheel")]
        public void NavigationOwnsThisFrameAndReturnsContinuously(string key)
        { var r = new FreeRoam(); r.Input(10, key, key == "drag", key == "wheel"); Assert.That(r.DirectorWeight(10), Is.Zero); Assert.That(r.DirectorWeight(30), Is.Zero); Assert.That(r.DirectorWeight(31.5), Is.EqualTo(0.5)); Assert.That(r.DirectorWeight(33), Is.EqualTo(1)); }
    }
}
