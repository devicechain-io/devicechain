// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.IO.Compression;
using System.Linq;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>
    /// The cut renderer's model against a real day: the clean live day recorded on 2026-11-25 (Fixtures/day-20261125.ndjson.gz,
    /// thinned to the readings the storyboard's predicates name and to a sample of the repeating snapshots; the reduced day gives the
    /// same trace as the whole recording). HARBORPULSE_UPDATE_FIXTURES=1 rewrites the expected trace.
    /// </summary>
    public class RecordedDayCutTests
    {
        static RecordedDay day; static PresentationMap map; static Storyboard story;
        static string Fixture(string name) => Path.Combine(DirectorKit.FixtureDirectory(), name);
        static string DayText(string name = "day-20261125.ndjson.gz")
        {
            using (var file = File.OpenRead(Fixture(name))) using (var gz = new GZipStream(file, CompressionMode.Decompress)) using (var reader = new StreamReader(gz))
                return reader.ReadToEnd();
        }
        static RecordedDay Day => day ?? (day = RecordedDay.Load(new StringReader(DayText())));
        // The first recorded day (2026-11-24) was made before a recording sampled alarm severities and the truck's fixes.
        static RecordedDay oldDay;
        static RecordedDay OldDay => oldDay ?? (oldDay = RecordedDay.Load(new StringReader(DayText("day-20261124.ndjson.gz"))));
        static PresentationMap Map => map ?? (map = new PresentationMap(Kit.Layout()));
        static Storyboard Story => story ?? (story = Storyboard.Parse(DirectorKit.StoryText(), Map));
        static string S(double v) => v.ToString("0.0", CultureInfo.InvariantCulture);
        static List<CutFrame> Play(CutPlayer player, int fps)
        {
            var frames = new List<CutFrame>(); var count = (int)Math.Round(player.DurationS * fps);
            for (var i = 0; i < count; i++) frames.Add(player.Frame(i / (double)fps));
            return frames;
        }
        static IEnumerable<string> Lines(CutPlayer player) => player.Trace.Select(e => player.Cut.Id + "|" + S(e.ShowS) + "|" + e.Page + "|" + e.Shot + "|" + e.Caption + "|" + (e.Overlay ? "overlay" : e.Kind) + "|"
            + (e.Shown ? "SHOWN|" + S(e.RecordedWallS) + "|" + e.Text.Replace("\n", " / ") : "unmet|" + e.Reason + (e.FirstHoldsAtS.HasValue ? " (first holds at " + S(e.FirstHoldsAtS.Value) + ")" : "")));

        [Test]
        public void BothCutsOverTheRecordedDayShowExactlyTheseCaptions()
        {
            var actual = new[] { "c90", "c300" }.SelectMany(cut => { var p = new CutPlayer(Story, cut, Day, Map); Play(p, 30); return Lines(p); }).ToArray();
            var path = Fixture("cut-trace-20261125.txt");
            if (Environment.GetEnvironmentVariable("HARBORPULSE_UPDATE_FIXTURES") == "1") File.WriteAllLines(path, actual);
            Assert.That(actual, Is.EqualTo(File.ReadAllLines(path)));
        }

        [TestCase("c90", 90)]
        [TestCase("c300", 300)]
        public void EveryShownCaptionHoldsThreeSecondsAndEveryClaimNamesItsRecords(string cut, double target)
        {
            var player = new CutPlayer(Story, cut, Day, Map); var frames = Play(player, 30);
            Assert.That(player.DurationS, Is.EqualTo(target).Within(0.5));
            // runs of one caption text within one segment
            var runs = new List<KeyValuePair<string, int>>();
            for (var i = 0; i < frames.Count; i++)
            {
                var key = frames[i].Segment + "|" + frames[i].Caption;
                if (runs.Count > 0 && runs[runs.Count - 1].Key == key) runs[runs.Count - 1] = new KeyValuePair<string, int>(key, runs[runs.Count - 1].Value + 1);
                else runs.Add(new KeyValuePair<string, int>(key, 1));
            }
            foreach (var run in runs.Where(r => !r.Key.EndsWith("|", StringComparison.Ordinal)))
                Assert.That(run.Value, Is.GreaterThanOrEqualTo(89), run.Key + " is visible for less than 3 s");
            foreach (var e in player.Trace.Where(e => e.Shown && e.Kind == "claim"))
                Assert.That(e.Evidence, Is.Not.Empty, e.Caption + " is shown without a record");
            foreach (var e in player.Trace.Where(e => e.Shown))
                Assert.That(frames.Any(f => f.Caption == e.Text || f.Overlay.Split('\n').Contains(e.Text.Split('\n')[0])), e.Caption + " is in the trace and on no frame");
            // every frame's caption is one the trace accounts for
            var accounted = new HashSet<string>(player.Trace.Where(e => e.Shown).Select(e => e.Text));
            foreach (var f in frames.Where(f => f.Caption.Length > 0)) Assert.That(accounted, Does.Contain(f.Caption));
        }

        [Test]
        public void ARateChipMarksEverySegmentThatDoesNotRunAtTheRecordedRate()
        {
            foreach (var cut in new[] { "c90", "c300" })
            {
                var player = new CutPlayer(Story, cut, Day, Map);
                foreach (var f in Play(player, 10))
                {
                    Assert.That(f.RateChip.Length > 0, Is.EqualTo(Math.Abs(f.Authored.Rate - 1) > 0.01), cut + " " + f.Shot.Id);
                    Assert.That(f.BorrowedChip, Is.EqualTo(f.Authored.Borrowed == "none" ? "" : f.Authored.Borrowed));
                    Assert.That(f.RecordedWallS, Is.InRange(f.Authored.RecordedWallFromS, f.Authored.RecordedWallToS));
                    Assert.That(f.Sim, Is.EqualTo(6 + f.RecordedWallS / 60).Within(0.05), "the runner's clock at the recorded instant");
                }
                using (var doc = System.Text.Json.JsonDocument.Parse(player.RenderJson(30, 1920, 1080)))
                    Assert.That(doc.RootElement.GetProperty("segments").GetArrayLength(), Is.EqualTo(player.Cut.Segments.Count));
            }
        }

        [Test]
        public void TheSameShowSecondIsTheSameFrameAtThirtyAndSixtyFramesASecond()
        {
            var thirty = Play(new CutPlayer(Story, "c90", Day, Map), 30); var sixty = Play(new CutPlayer(Story, "c90", Day, Map), 60);
            for (var i = 0; i < thirty.Count; i++)
            {
                var a = thirty[i]; var b = sixty[i * 2];
                Assert.That(b.RecordedWallS, Is.EqualTo(a.RecordedWallS)); Assert.That(b.Caption, Is.EqualTo(a.Caption)); Assert.That(b.Overlay, Is.EqualTo(a.Overlay));
                Assert.That(b.Move, Is.EqualTo(a.Move)); Assert.That(b.Dissolve, Is.EqualTo(a.Dissolve));
                Assert.That(b.Rail.Select(n => n.Id + n.Label), Is.EqualTo(a.Rail.Select(n => n.Id + n.Label)), "rail at " + S(a.ShowS));
                Assert.That(b.Links, Is.EquivalentTo(a.Links));
            }
        }

        [Test]
        public void ThePumpChainsRailGainsItsNodesInTheOrderTheRecordsCame()
        {
            var frames = Play(new CutPlayer(Story, "c90", Day, Map), 30); var seen = new List<string>();
            foreach (var f in frames.Where(f => f.Page.Id == "4")) foreach (var n in f.Rail) if (!seen.Contains(n.Id)) seen.Add(n.Id);
            Assert.That(seen, Is.EqualTo(new[] { "well", "command", "running", "falling" }));
            var filling = frames.Last(f => f.Shot.Id == "4d");
            Assert.That(filling.Rail.Select(n => n.Id), Has.No.Member("falling"), "the well is still filling: an earlier dip in its level is not the pump's doing");
            var started = frames.Last(f => f.Shot.Id == "4f");
            Assert.That(started.Links, Is.SupersetOf(new[] { "well->command", "command->running" }));
            // no arrow ever joins a gauge to a pump, and the short cut carries no gauge node at all
            Assert.That(frames.SelectMany(f => f.Rail).Select(n => n.Id), Has.No.Member("flood").And.No.Member("water"));
        }

        // CONTROL (the storyboard's frame-check acceptance): strip the records a claim binds to, and that claim, by name, is the one that goes.
        [Test]
        public void StrippingThePumpStartRecordsRemovesExactlyTheClaimsThatStandOnThem()
        {
            var stripped = RecordedDay.Load(new StringReader(string.Join("\n", DayText().Split('\n').Where(l => !(l.Contains("\"kind\":\"chip\"") && l.Contains("\"status\":\"PLATFORM\""))))));
            var whole = new CutPlayer(Story, "c90", Day, Map); var control = new CutPlayer(Story, "c90", stripped, Map);
            string[] Shown(CutPlayer p) => p.Trace.Where(e => e.Shown).Select(e => e.Caption).ToArray();
            Assert.That(Shown(whole), Does.Contain("platform-start"));
            // the float claim stands on the same chip record, so it goes with it; nothing else does
            Assert.That(Shown(whole).Except(Shown(control)).ToArray(), Is.EqualTo(new[] { "platform-start", "before-float" }));
            Assert.That(Shown(control).Except(Shown(whole)), Is.Empty, "nothing may take the place of a claim the recording does not support");
            Assert.That(control.Trace.First(e => e.Caption == "platform-start").Reason, Does.StartWith("the recording does not support it"));
        }

        [Test]
        public void ACutThatReadsPastTheRecordingIsRefused()
        {
            var cutShort = RecordedDay.Load(new StringReader(string.Join("\n", DayText().Split('\n').Where(l => l.Length > 0 && double.Parse(l.Substring(9, l.IndexOf(',') - 9), CultureInfo.InvariantCulture) < 900))));
            var e = Assert.Throws<FormatException>(() => new CutPlayer(Story, "c90", cutShort, Map));
            Assert.That(e.Message, Does.Contain("after the recording ends"));
        }

        [Test]
        public void TheCompactStreamIsTheSameDay()
        {
            var text = new StringWriter { NewLine = "\n" }; Day.WriteCompact(text);
            var again = RecordedDay.Load(new StringReader(text.ToString()));
            var a = new CutPlayer(Story, "c300", Day, Map); var b = new CutPlayer(Story, "c300", again, Map);
            Play(a, 10); Play(b, 10);
            Assert.That(Lines(b), Is.EqualTo(Lines(a)));
            Assert.That(again.Run, Is.EqualTo(Day.Run)); Assert.That(again.Epoch, Is.EqualTo(Day.Epoch));
        }

        [Test]
        public void TheReplaySourceAnswersFromTheRecordingAndLeavesTheRestAbsent()
        {
            var layout = Kit.Layout(); var source = new ReplaySource(OldDay, layout);
            string Id(string token) => layout.Devices.First(d => d.Token == token).ExternalId;
            source.Seek(600);
            Assert.That(source.Status.StartedAt, Is.EqualTo(OldDay.StartedAt)); Assert.That(source.Status.SimSeconds / 3600, Is.EqualTo(16).Within(0.05));
            Assert.That(source.TryGet(Id("hp-pump-north"), Metrics.WetWell, out var well), Is.True);
            Assert.That(well.OccurredAt, Is.LessThanOrEqualTo(source.Now)); Assert.That(source.Now - well.OccurredAt, Is.LessThan(TimeSpan.FromSeconds(15)));
            Assert.That(source.Alarms.Any(a => a.Key == "wet-well-high" && a.Active), Is.False, "not raised yet at 16:00");
            // 17:35: the alarm, and the command the platform's rule sent, with the platform's own instants
            source.Seek(696);
            var alarm = source.Alarms.Single(a => a.Device == Id("hp-pump-north") && a.Key == "wet-well-high");
            Assert.That(alarm.Active, Is.True); Assert.That(alarm.Severity, Is.EqualTo(ReplaySource.UnrecordedSeverity), "this recording did not sample severities");
            var start = source.Commands.Single(c => c.Device == Id("hp-pump-north") && c.Name == "start-pump" && c.Status == "SUCCESSFUL" && c.ObservedAt > source.Now.AddSeconds(-20));
            Assert.That(start.SentTime, Is.LessThanOrEqualTo(start.RespondedTime)); Assert.That(start.IsPlatformRecord, Is.True);
            Assert.That(source.TryLocation(Id("hp-truck-1"), out _), Is.False, "this recording did not sample the truck's fixes");
            Assert.That(source.TryAttribute(Id("hp-pump-north"), "well_alarm_m", out _), Is.False);
            // the outage: nothing new from an East Quay charger, and the absence alarms once the platform had raised them
            source.Seek(890);
            Assert.That(source.Alarms.Count(a => a.Active && (a.Key == "light-silent" || a.Key == "charger-offline")), Is.EqualTo(6));
            // going back rebuilds: the alarms are not there yet
            source.Seek(860);
            Assert.That(source.Alarms.Count(a => a.Active && (a.Key == "light-silent" || a.Key == "charger-offline")), Is.EqualTo(0));
            // the rollout as the platform's records stood at 02:38
            source.Seek(1240);
            Assert.That(source.Rollout.MaxInFlight, Is.EqualTo(2)); Assert.That(source.Rollout.Targets.Count, Is.EqualTo(6));
            Assert.That(source.Rollout.Target("hp-charger-north-1").Outcome, Is.EqualTo("FAILED")); Assert.That(source.Rollout.Progress["succeeded"], Is.EqualTo(1));
            Assert.That(source.Status.Ota.Rollout, Is.EqualTo(source.Rollout.Token));
            // and a frame from it: the observed model takes the replay like any source
            var model = new HarborObservedModel(layout);
            var frame = model.Update(source, new ClockReading(OldDay.SimAt(1240) * 3600, ClockMode.Live, ""), source.Now);
            Assert.That(frame.Rollout.Token, Is.EqualTo(source.Rollout.Token));
            // an alarm whose severity the recording did not sample is drawn as the weakest alarm; another unknown word is still refused
            source.Seek(890); var outage = new HarborObservedModel(layout).Update(source, new ClockReading(OldDay.SimAt(890) * 3600, ClockMode.Live, ""), source.Now);
            Assert.That(outage.Beacons.Count(b => !b.Cleared), Is.GreaterThanOrEqualTo(6));
            foreach (var beacon in outage.Beacons) { Assert.That(beacon.Rings, Is.EqualTo(1)); Assert.That(beacon.Pulse(source.Now), Is.InRange(0, 1)); }
            Assert.Throws<ArgumentException>(() => AlarmBeacons.Period("SEVERE"));
        }

        [Test]
        public void ARecordingThatSamplesSeveritiesAndTruckFixesReplaysThem()
        {
            var layout = Kit.Layout(); var truck = layout.Devices.First(d => d.Kind == DeviceKind.Truck); var gauge = layout.Devices.First(d => d.Token == "hp-gauge-east-1");
            string Row(double at, int seq, string kind, string data) => "{\"wallS\":" + S(at) + ",\"sequence\":" + seq + ",\"kind\":\"" + kind + "\",\"data\":" + data + "}";
            var run = "2026-11-24T20:41:47.0000000+00:00";
            string Obs(Observation o) => DirectorWire.Json(w => DirectorWire.Observation(w, o));
            var lines = new[] {
                Row(0, 1, "observation", Obs(DirectorKit.O("ledger", key: "run", id: "run/" + run, source: EvidenceSource.Scenario, run: run, wall: 0, sim: 6, cycle: ""))),
                Row(5, 2, "observation", Obs(DirectorKit.O("alarm", gauge.Token, "flood-level", "RAISED", sim: 6.08, wall: 5, id: "alarm-1", run: run))),
                Row(0, 3, "world", "{\"sample\":\"/status\",\"simHour\":6,\"alarms\":[]}"),
                Row(6, 4, "world", "{\"sample\":\"/status\",\"simHour\":6.1,\"alarms\":[{\"device\":\"" + gauge.ExternalId + "\",\"key\":\"flood-level\",\"severity\":\"MAJOR\",\"acknowledged\":true}],"
                    + "\"truck\":{\"lat\":49.8,\"lon\":-12.2,\"heading\":90,\"at\":\"2026-11-24T20:41:52.0000000+00:00\"}}") };
            var source = new ReplaySource(RecordedDay.Load(new StringReader(string.Join("\n", lines))), layout);
            source.Seek(5.5);
            Assert.That(source.Alarms.Single().Severity, Is.EqualTo(ReplaySource.UnrecordedSeverity), "not sampled yet");
            Assert.That(source.TryLocation(truck.ExternalId, out _), Is.False);
            source.Seek(6);
            Assert.That(source.Alarms.Single().Severity, Is.EqualTo("MAJOR")); Assert.That(source.Alarms.Single().Acknowledged, Is.True);
            Assert.That(source.TryLocation(truck.ExternalId, out var fix), Is.True); Assert.That(fix.Longitude, Is.EqualTo(-12.2));
            Assert.That(source.TryLocation(gauge.ExternalId, out _), Is.False, "a fix belongs to the truck");
        }

        [Test]
        public void ALateBufferedReadingDoesNotReplaceANewerOne()
        {
            var run = "2026-11-24T20:41:47.0000000+00:00";
            string Row(double at, int seq, string kind, string data) => "{\"wallS\":" + S(at) + ",\"sequence\":" + seq + ",\"kind\":\"" + kind + "\",\"data\":" + data + "}";
            string Reading(int seq, double observed, double occurred, string value) => Row(observed, seq, "observation", DirectorWire.Json(w => DirectorWire.Observation(w,
                DirectorKit.O("measure", "hp-charger-east-1", "power_kw", sim: 6, wall: observed, occurred: occurred, id: "r" + seq, run: run, cycle: "", fields: new Dictionary<string, string> { ["value"] = value }))));
            var lines = new[] {
                Row(0, 1, "observation", DirectorWire.Json(w => DirectorWire.Observation(w, DirectorKit.O("ledger", key: "run", id: "run/" + run, source: EvidenceSource.Scenario, run: run, wall: 0, sim: 6, cycle: "")))),
                Row(0, 2, "world", "{\"sample\":\"/status\",\"simHour\":6}"), Row(60, 3, "world", "{\"sample\":\"/status\",\"simHour\":7}"),
                Reading(4, 10, 10, "7"), Reading(5, 40, 40, "9"), Reading(6, 50, 20, "8") };
            var d = RecordedDay.Load(new StringReader(string.Join("\n", lines)));
            Assert.That(d.SimAt(30), Is.EqualTo(6.5).Within(1e-9)); Assert.That(d.SimAt(-5), Is.EqualTo(6)); Assert.That(d.SimAt(999), Is.EqualTo(7));
            Assert.That(d.TryMeasure("hp-charger-east-1", "power_kw", 5, out _), Is.False);
            Assert.That(d.TryMeasure("hp-charger-east-1", "power_kw", 45, out var before), Is.True); Assert.That(before.Field("value"), Is.EqualTo("9"));
            Assert.That(d.TryMeasure("hp-charger-east-1", "power_kw", 55, out var after), Is.True); Assert.That(after.Field("value"), Is.EqualTo("9"), "the reading taken at second 20 arrived at second 50");
        }
    }
}
