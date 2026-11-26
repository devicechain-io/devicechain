// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    static class DirectorTimeline
    {
        public static ObservationStore Clean(string defect = "", HarborLayout layout = null)
        {
            layout = layout ?? Kit.Layout();
            var s = new ObservationStore();
            void Add(string kind, string device, string key, string status, double sim, string id = null, string source = EvidenceSource.Platform,
                Dictionary<string, string> fields = null, Dictionary<string, string[]> sets = null)
            {
                if (defect == "truck-stays-home" && device == "hp-truck-1") return;
                if (defect == "replacement-new-device" && ((kind == "replacement" && source == EvidenceSource.Platform) || kind == "credential" || (fields != null && fields.ContainsKey("afterReplacement")))) return;
                if (defect == "no-command-far-end" && kind == "command" && key == "start-pump") return;
                if (defect == "no-command-far-end" && kind == "chip") status = "FLOAT-CONSISTENT";
                if (defect == "refuse-derate" && kind == "command" && key == "derate") status = "FAILED";
                if (defect == "suppression-wrong-device" && key == "lamp-flicker" && kind == "alarm") fields = null;
                if (defect == "replacement-old-credential-left-live" && kind == "credential") fields["enabled"] = "true";
                if (defect == "ota-signature-omitted" && kind == "artifact") status = "UNVERIFIED";
                if (defect == "ota-cap-three" && kind == "rollout" && fields != null) fields["maxInFlight"] = "3";
                if (defect == "ota-failure-disabled" && kind == "attempt" && status == "FAILED") return;
                if (defect == "smtp-sink-stopped" && kind == "attempt" && status == "SENT") return;
                if (defect == "acked-before-delay" && kind == "attempt" && fields != null && fields.TryGetValue("trigger", out var why) && why == "ESCALATION") return;
                if (defect == "air-lock-basin" && key == "pump-air-lock") device = "hp-pump-basin";
                if (defect == "drop-backfill" && id == "back-2") return;
                fields = fields == null ? new Dictionary<string, string>() : new Dictionary<string, string>(fields);
                var cycle = key == "absence" || key == "light-silent" || key == "charger-offline" || kind == "membership" ? "outage-cycle" :
                    device == "hp-pump-north" ? "pump-cycle" : device == "hp-gauge-north-3" ? "replacement-cycle" :
                    device == "hp-light-east-3" ? "maintenance-cycle" : device == "hp-charger-basin-2" ? "derate-cycle" :
                    device == "hp-truck-1" ? "truck-cycle" : sim >= 26 ? "ota-cycle" : "cycle-" + device + key;
                string Alarm(string d, string k) => "alarm:" + d + ":" + k;
                if (kind == "alarm") id = Alarm(device, key);
                if (kind == "window" && device == "hp-light-east-3" && source == EvidenceSource.Platform) { key = "lamp-flicker"; id = "service-this-run"; }
                if (kind == "attempt" && (key == "lamp-flicker" || key == "pump-air-lock")) fields["alarmToken"] = Alarm(device, key);
                if (kind == "command" || kind == "chip") fields["alarmToken"] = Alarm(device, device == "hp-pump-north" ? "wet-well-high" : "connector-hot");
                // The observer's control (-hp-control-no-cause): commands are read without their recorded cause, so none names an alarm or a cycle.
                if (defect == "command-cause-not-read" && kind == "command") { fields.Remove("alarmToken"); cycle = ""; }
                if (kind == "replacement") { id = "replacement-journal"; fields["beforeReading"] = "before-gauge"; fields["afterReading"] = "after-gauge"; }
                if (device == "hp-gauge-north-3" && (kind == "measure" || kind == "credential")) { fields["replacementToken"] = "replacement-journal"; if (kind == "measure") id = fields.ContainsKey("afterReplacement") ? "after-gauge" : "before-gauge"; }
                if (sim >= 26) {
                    fields["artifactToken"] = "firmware-artifact"; fields["rolloutToken"] = "rollout-this-run";
                    if (kind == "artifact") id = "firmware-artifact";
                    if (kind == "rollout") id = "rollout-this-run";
                }
                if (kind == "chip") fields["runningAt"] = ((sim - 6) * layout.DayLengthSeconds / 24).ToString(System.Globalization.CultureInfo.InvariantCulture);
                if (key == "absence") {
                    foreach (var target in new[] { "hp-light-east-1", "hp-light-east-2", "hp-light-east-3", "hp-light-east-4", "hp-charger-east-1", "hp-charger-east-2" }) {
                        var f = new Dictionary<string, string>(fields) { ["alarmToken"] = Alarm(target, target.Contains("light") ? "light-silent" : "charger-offline") };
                        s.Add(DirectorKit.O(kind, target, key, status, sim, (sim - 6) * layout.DayLengthSeconds / 24, (id ?? "notify") + target, cycle: cycle, fields: f));
                    }
                    return;
                }
                s.Add(DirectorKit.O(kind, device, key, status, sim, (sim - 6) * layout.DayLengthSeconds / 24, id ?? kind + ":" + device + ":" + key + ":" + status,
                    cycle: cycle, attempt: sim >= 26 ? "ota-attempt" : "", source: source, fields: fields, sets: sets));
            }
            Add("binding", "", "", "READY", 6, fields: new Dictionary<string, string> { ["freshDevices"] = "43" });
            Add("alarm", "hp-meter-ferry-terminal", "peak-demand", "RAISED", 8.5);
            Add("alarm", "hp-meter-ferry-terminal", "peak-demand", "CLEARED", 8.65);
            Add("window", "hp-light-east-3", "lamp-flicker", "ACTIVE", 10.75, "service-this-run");
            Add("window", "hp-light-east-3", "", "ACTIVE", 10.75, source: EvidenceSource.Scenario);
            Add("alarm", "hp-light-east-3", "lamp-flicker", "RAISED", 11, fields: new Dictionary<string, string> { ["raisedDuringSuppression"] = "true", ["suppressionWindowToken"] = "service-this-run" });
            // the lamp with no window (North Quay): it flickers at the same moment, and its notification is sent at once
            Add("alarm", "hp-light-north-3", "lamp-flicker", "RAISED", 11);
            Add("attempt", "hp-light-north-3", "lamp-flicker", "SENT", 11.02, fields: new Dictionary<string, string> { ["trigger"] = "ALARM" });
            Add("window", "hp-light-east-3", "", "ENDED", 11.5);
            Add("attempt", "hp-light-east-3", "lamp-flicker", "SENT", 11.52, fields: new Dictionary<string, string> { ["policy"] = "hp-duty-escalation", ["afterClose"] = "true", ["alarmActive"] = "true" });
            Add("measure", "hp-gauge-north-3", "water_level_m", "", 12.45);
            Add("replacement", "hp-gauge-north-3", "", "RECORDED", 12.5, sets: new Dictionary<string, string[]> { ["disabledCredentials"] = new[] { "old-credential" } });
            Add("replacement", "hp-gauge-north-3", "", "", 12.5, source: EvidenceSource.Scenario);
            Add("measure", "hp-gauge-north-3", "water_level_m", "", 12.51, fields: new Dictionary<string, string> { ["afterReplacement"] = "true" });
            Add("credential", "hp-gauge-north-3", "", "", 12.51, "old-credential", fields: new Dictionary<string, string> { ["enabled"] = "false" });
            // the devices that hold their own session (page 3c): both pull lamps report the level they fetched at 10:00, and one MQTT charger's session drops for an hour
            foreach (var lamp in new[] { "hp-light-pull-1", "hp-light-pull-2" }) Add("measure", lamp, "dim_pct", "", 10.05, fields: new Dictionary<string, string> { ["value"] = "35" });
            Add("alarm", "hp-charger-mqtt-1", "mqtt-charger-disconnected", "RAISED", 12.02);
            Add("alarm", "hp-charger-mqtt-1", "mqtt-charger-disconnected", "CLEARED", 13.02);
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
            Add("alarm", "hp-pump-east", "pump-air-lock", "RAISED", Scheduler.WallOffsetSim(layout.Cues.AirLockFromSim, layout.Cues.AirLockHoldS, layout.DayLengthSeconds));
            Add("attempt", "hp-pump-east", "pump-air-lock", "SENT", Scheduler.WallOffsetSim(layout.Cues.AirLockFromSim, layout.Cues.AirLockHoldS + 2, layout.DayLengthSeconds));
            Add("window", "", "feeder-outage", "ACTIVE", 19.75, source: EvidenceSource.Scenario);
            var expected = new[] { "hp-light-east-1", "hp-light-east-2", "hp-light-east-3", "hp-light-east-4", "hp-charger-east-1", "hp-charger-east-2" };
            Add("membership", "", "", "", Scheduler.WallOffsetSim(19.75, layout.Cues.SilenceS, layout.DayLengthSeconds), "east-absence", sets: new Dictionary<string, string[]> { ["tokens"] = expected });
            var detected = Scheduler.WallOffsetSim(19.75, layout.Cues.SilenceS, layout.DayLengthSeconds);
            foreach (var d in expected) Add("alarm", d, d.Contains("light") ? "light-silent" : "charger-offline", "RAISED", detected);
            if (defect == "silence-north-lamp") Add("alarm", "hp-light-north-1", "light-silent", "RAISED", detected);
            Add("attempt", "", "absence", "SENT", Scheduler.WallOffsetSim(detected, 6, layout.DayLengthSeconds));
            Add("attempt", "", "absence", "SENT", Scheduler.WallOffsetSim(detected, 6 + layout.Cues.EscalateAfterS, layout.DayLengthSeconds), "escalation", fields: new Dictionary<string, string> { ["trigger"] = "ESCALATION" });
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
            Add("rollout", "", "", "TERMINAL", 26.6);
            Add("rollout", "", "", "COUNTS", 26.6, fields: new Dictionary<string, string> { ["updated"] = defect == "ota-failure-disabled" ? "6" : "5", ["refused"] = defect == "ota-failure-disabled" ? "0" : "1", ["maxInFlight"] = "2" });


            return s;
        }
        public static Scheduler Run(string defect = "", HarborLayout layout = null, double streamCutAtS = 850)
        {
            layout = layout ?? Kit.Layout(); var s = new Scheduler(Storyboard.Parse(DirectorKit.StoryText(), new PresentationMap(layout)), layout); var source = Clean(defect, layout); var store = new ObservationStore();
            for (var wall = 0; wall <= 22 * layout.DayLengthSeconds / 24; wall++) {
                var sim = 6 + wall * 24 / layout.DayLengthSeconds;
                // Snapshot polls keep working during a stream cut; the missing stream
                // coverage invalidates exact/negative claims, not measurements or the clock.
                foreach (var o in source.Records.Where(o => o.ObservedAtS <= wall)) store.Add(o);
                foreach (var stream in new[] { "alarms", "notifications", "credentials", "pump", "measurements" }) {
                    store.AddCoverage(new CoverageInterval("run", stream, 6, sim, false));
                    if (defect == "stream-cut" && (stream == "alarms" || stream == "notifications") && wall >= streamCutAtS)
                        store.AddCoverage(new CoverageInterval("run", stream, 6 + streamCutAtS * 24 / layout.DayLengthSeconds,
                            6 + Math.Min(wall, streamCutAtS + 60) * 24 / layout.DayLengthSeconds, true));
                }
                s.Tick(new DirectorSample("run", sim, wall, defects: defect == "" ? null : new[] { defect }), store);
            }
            return s;
        }
    }

    public class DirectorTestsScheduler
    {
        static Storyboard ArbitrationStory(double lowEnd = 10, double queueTtl = 300, double captionHold = 3, bool current = false)
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
                if (id == "1") { if (current) DirectorKit.Obj(DirectorKit.Obj(DirectorKit.Obj(DirectorKit.Arr(p["captions"])[0])["needs"])["binding"])["mode"] = "current"; p["plannedSim"] = 6; DirectorKit.Obj(DirectorKit.Arr(p["captions"])[0])["holdS"] = captionHold; }
                foreach (var s in DirectorKit.Arr(p["shots"])) { var shot = DirectorKit.Obj(s); shot["cueSim"] = 6; }
            }
            return Storyboard.Parse(DirectorKit.Json(root), new PresentationMap(Kit.Layout()));
        }
        static Scheduler Arbitrate(double lowEnd = 10, double ttl = 300, double captionHold = 3)
        {
            var scheduler = new Scheduler(ArbitrationStory(lowEnd, ttl, captionHold), Kit.Layout()); var store = new ObservationStore();
            store.Add(DirectorKit.O("binding", status: "READY", sim: 6, wall: 0, fields: new Dictionary<string, string> { ["freshDevices"] = "43" }));
            store.Add(DirectorKit.O("alarm", key: "1", status: "RAISED", sim: 6, wall: 0));
            for (var t = 0; t <= 250; t++)
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
            Assert.That(baseline.Length, Is.EqualTo(2)); Assert.That(baseline[1].Value, Is.EqualTo(baseline[0].Value)); Assert.That(baseline[1].Evidence, Is.Not.Empty);
            var closed = Arbitrate(lowEnd: 6.1); Assert.That(closed.Log.Any(e => e.Kind == "resume" && e.Page == "1"), Is.False);
            Assert.That(closed.Log.Any(e => e.Kind == "missed" && e.Page == "1" && e.Value.Contains("resume window")), Is.True);
        }
        [Test]
        public void CleanRunExactPageAndShotSequence()
        {
            var s = DirectorTimeline.Run();
            AssertCleanOutput(s);
            var actual = s.Log.Where(e => e.Kind == "shot").Select(e => e.Page + ":" + e.Value).ToArray();
            var expected = new[] { "1:1a", "1:1b", "1b:1b-1", "2:2a", "2:2b", "2:2c", "2:2d", "2:2e", "3:3a", "3:3b", "3:3c", "3c:3c-1", "3c:3c-2", "3c:3c-3", "3c:3c-4", "3b:3b-1", "3b:3b-2", "3b:3b-3", "3c:3c-5", "4:4a", "4:4b", "4:4c", "4:4d", "4:4e", "4:4f", "4:4g", "5:5a", "5:5b", "4b:4b-1", "6:6a", "6:6b", "6:6c", "6:6d", "7:7a", "7:7b", "7:7c", "7:7d", "7:7e", "7:7f", "8:8a", "8:8b", "8:8c", "8:8d", "9:9a" };
            Assert.That(actual, Is.EqualTo(expected), string.Join(", ", actual));
            var outputPath = System.IO.Path.Combine(DirectorKit.FixtureDirectory(), "director-clean-output.txt");
            if (Environment.GetEnvironmentVariable("HARBORPULSE_UPDATE_FIXTURES") == "1") System.IO.File.WriteAllLines(outputPath, Output(s));
            Assert.That(Output(s), Is.EqualTo(System.IO.File.ReadAllLines(outputPath)));
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
        [Test]
        public void MaintenanceStreamGapRemovesOnlyTheNegativeWindowClaim()
        {
            // The committed stream-cut golden covers the outage. Exercise the same
            // injection during maintenance too, as required by the page 2 fallback.
            var clean = DirectorTimeline.Run(); var cut = DirectorTimeline.Run("stream-cut", streamCutAtS: 300);
            var unchanged = new Func<Scheduler, string[]>(s => Output(s).Where(line => {
                var fields = line.Split('|');
                return fields[1] != "2" || !(fields[3].Contains("none") || fields[3].Contains("No notification"));
            // Removing one rail node advances the following node's three-second
            // boundary and leaves the preceding caption up until the next shot.
            }).Select(line => line.Split('|')[1] == "2" ? line.Substring(line.IndexOf('|')) : line)
                .OrderBy(line => line, StringComparer.Ordinal).ToArray());
            Assert.That(unchanged(cut), Is.EqualTo(unchanged(clean)));
            Assert.That(cut.Log.Any(e => e.Kind == "caption" && e.Value.Contains("No notification")), Is.False);
        }
        static string[] Output(Scheduler scheduler) => scheduler.Log.Where(e => new[] { "shot", "caption", "caption-end", "node", "node-label", "link", "skip-page", "follow", "missed" }.Contains(e.Kind))
            .Select(e => e.ShowS.ToString("R", System.Globalization.CultureInfo.InvariantCulture) + "|" + e.Page + "|" + e.Kind + "|" + e.Value.Replace("\n", " / ") + "|" + string.Join(",", e.Evidence)).ToArray();
        static void AssertCleanOutput(Scheduler scheduler)
        {
            var story = DirectorKit.Story();
            var alternatives = new[] { "fault-only", "hot-only", "silence-only", "failed" };
            foreach (var e in scheduler.Log)
            {
                Assert.That(e.Kind == "skip-page" || e.Kind == "missed" || (e.Kind == "state" && e.Value == "Fallback"), Is.False,
                    "clean output must not fall back or skip: " + e.Page + ":" + e.Value);
                if (e.Kind != "caption" && e.Kind != "caption-end") continue;
                Assert.That(System.Text.RegularExpressions.Regex.IsMatch(e.Value, "fallback|unavailable|skip", System.Text.RegularExpressions.RegexOptions.IgnoreCase), Is.False,
                    "clean output must not contain fallback/unavailable/skip text: " + e.Value);
                var page = story.Page(e.Page);
                Assert.That(page.Captions.Where(c => c.Kind == "fallback" || alternatives.Contains(c.Id))
                    .Any(c => e.Value == c.Id || e.Value == string.Join("\n", c.Lines)), Is.False,
                    "clean output must not contain a fallback caption: " + e.Value);
            }
        }
        static bool MatchesTemplate(string template, string value)
        {
            var pattern = System.Text.RegularExpressions.Regex.Escape(template);
            pattern = System.Text.RegularExpressions.Regex.Replace(pattern, @"\\\{[^}]+}", ".+");
            return System.Text.RegularExpressions.Regex.IsMatch(value, "^" + pattern + "$");
        }
        static string ChangeKey(DirectorEvent e, Storyboard story)
        {
            var page = story.Page(e.Page);
            if (e.Kind == "caption" || e.Kind == "caption-end")
            {
                var caption = page.Captions.Single(c => e.Kind == "caption-end" ? c.Id == e.Value : MatchesTemplate(string.Join("\n", c.Lines), e.Value));
                return e.Page + ":slot:" + caption.Slot;
            }
            if (e.Kind == "node-label") return e.Page + ":node:" + page.Nodes.Single(n => MatchesTemplate(n.Label, e.Value)).Id;
            return e.Page + ":" + e.Kind + ":" + e.Value;
        }
        static string[] AllowedChanges(string control)
        {
            switch (control)
            {
                case "truck-stays-home": return new[] { "5:*", "7:slot:truck-out" };
                // the row counts what was recorded, so a seventh silent device changes its number (and the caption that says "exactly the six" goes);
                // a cut stream takes only the claims that say nothing else happened
                case "silence-north-lamp": return new[] { "6:slot:absence", "6:node:absence-count" };
                case "stream-cut": return new[] { "6:slot:absence" };
                // page 3c says its last line (3c-5) when page 3b hands the camera back, so a day that keeps 3b up longer moves that one shot
                case "refuse-derate": return new[] { "3b:slot:derate", "3b:slot:cool", "3b:node:derate", "3b:node:cool", "3b:link:hot->derate", "3b:link:derate->cool", "3c:shot:3c-5", "3c:slot:locked-out" };
                case "command-cause-not-read": return new[] { "3b:slot:derate", "3b:slot:cool", "3b:node:derate", "3b:node:cool", "3b:link:hot->derate", "3b:link:derate->cool", "3c:shot:3c-5", "3c:slot:locked-out", "4:node:command", "4:link:well->command", "4:link:command->running",
                    // a card gains a row every three seconds at most, so a row that drops out lets the rows after it come that much sooner
                    "4:node:running", "4:node:falling", "4:link:running->falling" };
                case "no-command-far-end": return new[] { "4:slot:start", "4:slot:before-float", "4:node:command", "4:node:running", "4:node:falling", "4:link:well->command", "4:link:command->running", "4:link:running->falling" };
                case "air-lock-basin": return new[] { "4b:follow:hp-pump-east", "4b:follow:hp-pump-basin", "4b:slot:air", "4b:node:air", "4b:node:airlock-notified" };
                case "drop-backfill": return new[] { "7:slot:tally", "7:node:stored", "7:link:paced->stored", "7:link:stored->clear" };
                case "suppression-wrong-device": return new[] { "2:slot:fault", "2:node:fault", "2:node:control-lamp-notified", "2:link:work->fault", "2:link:fault->none" };
                case "replacement-new-device": return new[] { "3:slot:new-hardware", "3:slot:same", "3:slot:disabled", "3:node:journal", "3:node:same", "3:node:disabled", "3:link:swapped->journal", "3:link:journal->same", "3:link:same->disabled" };
                case "replacement-old-credential-left-live": return new[] { "3:slot:disabled", "3:node:disabled", "3:link:same->disabled" };
                case "ota-signature-omitted": return new[] { "8:slot:firmware", "8:node:signed", "8:link:signed->cap" };
                case "ota-cap-three": return new[] { "8:node:cap", "8:slot:ota-summary" };
                case "ota-failure-disabled": return new[] { "8:slot:failure", "8:slot:working", "8:slot:ota-summary", "8:node:refused", "8:node:working", "8:node:updated", "8:link:cap->refused", "8:link:refused->working", "8:link:working->updated" };
                // with no mail sent, every claim that a notification went out goes: the held one, the no-window lamp's, the air lock's and the outage's
                case "smtp-sink-stopped": return new[] { "2:slot:work-sent", "2:node:sent", "2:link:closed->sent", "2:node:control-lamp-notified", "4b:node:airlock-notified", "4b:link:air->airlock-notified",
                    "6:node:absence-notified", "6:node:escalated", "6:link:absence-count->absence-notified", "6:link:absence-notified->escalated" };
                case "acked-before-delay": return new[] { "6:node:escalated", "6:link:absence-notified->escalated" };
                default: throw new ArgumentException("control needs an explicit allow-list: " + control);
            }
        }
        [TestCase("truck-stays-home") ]
        [TestCase("silence-north-lamp") ]
        [TestCase("refuse-derate") ]
        [TestCase("command-cause-not-read") ]
        [TestCase("no-command-far-end") ]
        [TestCase("air-lock-basin") ]
        [TestCase("drop-backfill") ]
        [TestCase("suppression-wrong-device") ]
        [TestCase("replacement-new-device") ]
        [TestCase("replacement-old-credential-left-live") ]
        [TestCase("ota-signature-omitted") ]
        [TestCase("ota-cap-three") ]
        [TestCase("ota-failure-disabled") ]
        [TestCase("smtp-sink-stopped") ]
        [TestCase("acked-before-delay") ]
        [TestCase("stream-cut") ]
        public void ControlsChangeOnlyDeclaredEmittedOutputs(string control)
        {
            var baseline = DirectorTimeline.Run(); var clean = Output(baseline); var run = DirectorTimeline.Run(control); var actual = Output(run);
            var delta = clean.Except(actual).Select(v => "-" + v).Concat(actual.Except(clean).Select(v => "+" + v)).OrderBy(v => v, StringComparer.Ordinal).ToArray();
            var story = DirectorKit.Story(); var allowed = AllowedChanges(control);
            foreach (var scheduler in new[] { baseline, run })
            {
                var other = scheduler == baseline ? actual : clean;
                var emitted = scheduler.Log.Where(e => new[] { "shot", "caption", "caption-end", "node", "node-label", "link", "skip-page", "follow", "missed" }.Contains(e.Kind)).ToArray();
                var lines = Output(scheduler);
                for (var i = 0; i < lines.Length; i++)
                    if (!other.Contains(lines[i]))
                        Assert.That(allowed.Contains(emitted[i].Page + ":*") || allowed.Contains(ChangeKey(emitted[i], story)), Is.True,
                            control + " changed an output outside its storyboard target: " + lines[i]);
            }
            var expectedPath = System.IO.Path.Combine(DirectorKit.FixtureDirectory(), "director-control-" + control + ".txt");
            if (Environment.GetEnvironmentVariable("HARBORPULSE_UPDATE_FIXTURES") == "1") System.IO.File.WriteAllLines(expectedPath, delta);
            Assert.That(delta, Is.Not.Empty, "control must change its target output");
            Assert.That(delta, Is.EqualTo(System.IO.File.ReadAllLines(expectedPath)), control);
            Assert.That(run.Banner, Does.StartWith("TEST CONTROL: "));
            if (control == "truck-stays-home") { Assert.That(run.Log.Any(e => e.Kind == "shot" && e.Page == "5"), Is.False); Assert.That(run.Log.Any(e => e.Kind == "skip-page" && e.Page == "5"), Is.True); }
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
            Assert.That(s.Log.Any(e => e.Kind == "recap" && e.Page == "4"), Is.True, string.Join(";", s.Log.Select(e => e.Kind + ":" + e.Page + ":" + e.Value)));
            Assert.That(s.Log.Any(e => e.Kind == "caption" && e.Page == "4" && e.Value == "The platform starts the pump."), Is.True);
            Assert.That(s.Consumed, Does.Contain(store.Records.Single(o => o.Kind == "binding").EventKey));
            var keys = s.Consumed.Count;
            for (var t = 697; t <= 706; t++) {
                var replacement = new ObservationStore();
                foreach (var o in store.Records.Where(o => o.ObservedAtS <= t)) replacement.Add(new Observation(o.Run, o.Kind, o.Identity, o.Device, o.Key, o.Cycle, o.Command, o.Attempt, o.Status, t, o.OccurredAtS, o.Sim, o.Source, o.Fields, o.Sets));
                foreach (var coverage in store.Coverage) replacement.AddCoverage(coverage);
                s.Tick(new DirectorSample("run", 17.6, t), replacement);
            }
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
            store.Add(DirectorKit.O("binding", status: "READY", sim: 6, wall: 0, fields: new Dictionary<string, string> { ["freshDevices"] = "43" }));
            for (var t = 0; t <= 300; t++)
            {
                if (t == 200 || t == 210 || t == 220) store.Add(DirectorKit.O("alarm", key: "repeat", status: "RAISED", sim: 6 + t / 60.0, wall: t, id: "repeat-" + t, cycle: "cycle-" + t));
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

        [TestCase(20, true)] [TestCase(41, false)]
        public void DelayedEvidenceRemainsPendingUntilHoldMax(int arrival, bool visible)
        {
            var root = DirectorKit.Object(DirectorKit.StoryText());
            foreach (var value in DirectorKit.Arr(root["pages"])) if ((string)DirectorKit.Obj(value)["id"] != "1") { DirectorKit.Obj(value)["windowSim"] = new List<object> { 40, 41 }; }
            var page = DirectorKit.Arr(root["pages"]).Select(DirectorKit.Obj).Single(p => (string)p["id"] == "1");
            var pre = DirectorKit.Object("{\"id\":\"waiting\",\"kind\":\"pre\",\"source\":\"PLATFORM\",\"slot\":\"baseline\",\"lines\":[\"Waiting for the harbour.\"],\"holdS\":3,\"needs\":null}");
            DirectorKit.Arr(page["captions"]).Add(pre); DirectorKit.Arr(DirectorKit.Obj(DirectorKit.Arr(page["shots"])[0])["captions"]).Add("waiting");
            var scheduler = new Scheduler(Storyboard.Parse(DirectorKit.Json(root), new PresentationMap(Kit.Layout())), Kit.Layout()); var store = new ObservationStore();
            for (var t = 0; t <= 50; t++) {
                if (t == arrival) store.Add(DirectorKit.O("binding", status: "READY", wall: t, sim: 6 + t / 60.0, fields: new Dictionary<string, string> { ["freshDevices"] = "43" }));
                scheduler.Tick(new DirectorSample("run", 6 + t / 60.0, t), store);
            }
            Assert.That(scheduler.Log.Any(e => e.Kind == "caption" && e.Value.Contains("43 devices")), Is.EqualTo(visible));
            if (visible) Assert.That(scheduler.Log.Single(e => e.Kind == "caption" && e.Value.Contains("43 devices")).ShowS, Is.EqualTo(arrival));
        }
        [TestCase("E")] [TestCase("D")] [TestCase("H")] [TestCase("Space")]
        public void ReservedAndNavigationInSameFrameStillOwnCamera(string key)
        { var r = new FreeRoam(); r.Input(10, key, mouseDrag: true); Assert.That(r.DirectorWeight(10), Is.Zero); }
        [Test]
        public void ActiveStaleEndsCaptionAndRecoveryRechecksEvidence()
        {
            var store = DirectorTimeline.Clean(); var scheduler = new Scheduler(DirectorKit.Story(), Kit.Layout());
            scheduler.Tick(new DirectorSample("run", 6, 0), store);
            Assert.That(scheduler.Log.Any(e => e.Kind == "caption"), Is.True);
            scheduler.Tick(new DirectorSample("run", 6.1, 6, stale: true), store);
            Assert.That(scheduler.Log.Last(e => e.Kind == "caption-end").ShowS, Is.EqualTo(6));
            scheduler.Tick(new DirectorSample("run", 6.2, 12), new ObservationStore());
            Assert.That(scheduler.Log.Any(e => e.Kind == "caption" && e.ShowS >= 12), Is.False);
        }

        [TestCase(1440)] [TestCase(2880)]
        public void ActualTimelineUsesWallCueDeadlinesAtBothDayLengths(double dayLength)
        {
            var json = DirectorKit.Object(Kit.LayoutText()); json["dayLengthSeconds"] = dayLength; var layout = HarborLayout.Parse(DirectorKit.Json(json));
            var scheduler = DirectorTimeline.Run(layout: layout);
            var air = scheduler.Log.Single(e => e.Kind == "caption" && e.Page == "4b");
            Assert.That(air.ShowS - (layout.Cues.AirLockFromSim - 6) * dayLength / 24, Is.EqualTo(layout.Cues.AirLockHoldS).Within(1));
            var silence = scheduler.Log.Single(e => e.Kind == "caption" && e.Value == "The platform notices the silence.");
            Assert.That(silence.ShowS - (19.75 - 6) * dayLength / 24, Is.EqualTo(layout.Cues.SilenceS).Within(1));
            Assert.That(scheduler.Log.Any(e => e.Kind == "caption" && e.Value == "A quiet morning raises nothing."), Is.True);
            var notify = scheduler.Log.Single(e => e.Kind == "node" && e.Value == "absence-notified");
            var escalation = scheduler.Log.Single(e => e.Kind == "node" && e.Value == "escalated");
            Assert.That(escalation.ShowS - notify.ShowS, Is.EqualTo(layout.Cues.EscalateAfterS).Within(3));
        }

        [Test]
        public void LongerCaptionHoldControlsPreemptionBoundary()
        { Assert.That(Arbitrate(captionHold: 8).Log.First(e => e.Kind == "preempt").ShowS, Is.EqualTo(8)); }

        [TestCase(true)] [TestCase(false)]
        public void TimeoutOrWindowEndUsesOnlyTheUnresolvedSlotsDeclaredFallback(bool timeout)
        {
            var root = DirectorKit.Object(DirectorKit.StoryText());
            foreach (var value in DirectorKit.Arr(root["pages"])) DirectorKit.Obj(value)["windowSim"] = new List<object> { 40, 41 };
            var page = DirectorKit.Arr(root["pages"]).Select(DirectorKit.Obj).Single(p => (string)p["id"] == "1");
            page["windowSim"] = new List<object> { 6, timeout ? 8 : 6.1 };
            page["triggerTimeoutS"] = 3;
            page["trigger"] = DirectorKit.Object("{\"alarm\":{\"key\":\"never-arrives\",\"status\":\"RAISED\"}}");
            foreach (var id in new[] { "unrelated", "baseline-wait" })
                DirectorKit.Arr(page["captions"]).Add(DirectorKit.Object("{\"id\":\"" + id + "\",\"kind\":\"fallback\",\"source\":\"PLATFORM\",\"slot\":\"" + (id == "unrelated" ? "quiet" : "baseline") + "\",\"lines\":[\"" + (id == "unrelated" ? "Unrelated fallback." : "Waiting for the harbour.") + "\"],\"holdS\":3,\"needs\":null}"));
            DirectorKit.Arr(DirectorKit.Obj(DirectorKit.Arr(page["shots"])[0])["captions"]).Add("baseline-wait");
            var scheduler = new Scheduler(Storyboard.Parse(DirectorKit.Json(root), new PresentationMap(Kit.Layout())), Kit.Layout());
            for (var t = 0; t <= 12; t++) scheduler.Tick(new DirectorSample("run", 6 + t / 60.0, t), new ObservationStore());
            Assert.That(scheduler.Log.Where(e => e.Kind == "caption").Select(e => e.Value), Is.EqualTo(new[] { "Waiting for the harbour." }));
            var end = scheduler.Log.Single(e => e.Kind == "caption-end"); var caption = scheduler.Log.Single(e => e.Kind == "caption");
            Assert.That(end.ShowS - caption.ShowS, Is.GreaterThanOrEqualTo(3));
            Assert.That(scheduler.State("1"), Is.EqualTo(PageState.Presented));
        }

        [Test]
        public void ResumeRechecksCurrentClaimInsteadOfReplayingCachedText()
        {
            var currentStory = ArbitrationStory(current: true);
            var scheduler = new Scheduler(currentStory, Kit.Layout()); var store = new ObservationStore();
            store.Add(DirectorKit.O("binding", status: "READY", wall: 0, sim: 6, fields: new Dictionary<string, string> { ["freshDevices"] = "43" }));
            for (var t = 0; t <= 180; t++) {
                if (t == 1) store.Add(DirectorKit.O("alarm", key: "1b", status: "RAISED", sim: 6.01, wall: 1));
                if (t == 4) store.Add(DirectorKit.O("binding", status: "ENDED", wall: 4, sim: 6));
                scheduler.Tick(new DirectorSample("run", 6 + t / 60.0, t), store);
            }
            Assert.That(scheduler.Log.Any(e => e.Kind == "resume" && e.Page == "1"), Is.True);
            Assert.That(scheduler.Log.Count(e => e.Kind == "caption" && e.Page == "1" && e.Value.Contains("43 devices")), Is.EqualTo(1));
        }
    }
}
