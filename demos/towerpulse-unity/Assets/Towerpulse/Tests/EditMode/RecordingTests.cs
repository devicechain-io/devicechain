// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using DeviceChain.Demos.Platform.Recording;
using DeviceChain.Towerpulse.Model;
using DeviceChain.Towerpulse.Plane;
using DeviceChain.Towerpulse.Recording;
using DeviceChain.Towerpulse.Story;
using NUnit.Framework;

namespace DeviceChain.Towerpulse.Tests
{
    /// <summary>
    /// The run header and the recorder. The claims that matter: the header names what the day was (seed, model, rules, clock), a recorded run read back
    /// gives the story layer EXACTLY the states the live run gave it, and a recording that is wrong in a way a replay could not notice is refused.
    /// </summary>
    public sealed class RecordingTests
    {
        string dir;
        static readonly DateTimeOffset T0 = new DateTimeOffset(2026, 10, 29, 13, 54, 0, TimeSpan.Zero);

        [SetUp] public void SetUp() { now = 0; dir = Path.Combine(Path.GetTempPath(), "tp-rec-" + Guid.NewGuid().ToString("N")); }

        [TearDown] public void TearDown() { try { if (Directory.Exists(dir)) Directory.Delete(dir, true); } catch (IOException) { } }

        static TowerpulseRunInfo Info(double speed = 1.0)
        {
            var i = new TowerpulseRunInfo { ModelVersion = TowerDay.ModelVersion, ManifestId = "towerpulse", Seed = 7, StartHour = 13.9, Speed = speed, IntervalSeconds = 10, Label = "control" };
            i.RuleVersions["tp-r1-zone-hot"] = 2; i.RuleVersions["tp-r4-chiller-silent"] = 1;
            i.RuleEnabled["tp-r1-zone-hot"] = true; i.RuleEnabled["tp-r4-chiller-silent"] = true;
            i.ProfileVersions["tp-vav-zone"] = 3;
            return i;
        }

        static RunHeader Header()
        {
            var h = new RunHeader { Instance = "tp", Tenant = "sim-tp-x", PlatformVersion = "0.26.0" };
            h.Devices.Add(new RunDevice { Id = "TP-F4-E", Token = "tp-f4-e", Kind = "vav" });
            return h;
        }

        double now;

        TowerpulseRecorder Create(TowerpulseRunInfo info)
        {
            return TowerpulseRecorder.Create(dir, Header(), info, () => now, () => T0.AddSeconds(now));
        }

        // ------------------------------------------------------------------ header

        [Test]
        public void TheRunHeaderNamesTheSeedModelRulesAndClock()
        {
            using (var r = Create(Info())) { r.Close(); }
            var folder = Directory.GetDirectories(dir)[0];
            var rec = TowerpulseRecording.Open(folder);
            Assert.AreEqual("towerpulse", rec.Header.Manifest);
            Assert.AreEqual("7", rec.Header.Seed);
            Assert.AreEqual("sim-tp-x", rec.Header.Tenant);
            Assert.AreEqual("tp", rec.Header.Instance);
            Assert.AreEqual(TowerDay.ModelVersion, rec.Info.ModelVersion);
            Assert.AreEqual(7UL, rec.Info.Seed);
            Assert.AreEqual(13.9, rec.Info.StartHour, 1e-9);
            Assert.AreEqual("wall", rec.Info.ClockMode);
            Assert.AreEqual(2, rec.Info.RuleVersions["tp-r1-zone-hot"]);
            Assert.AreEqual(1, rec.Info.RuleVersions["tp-r4-chiller-silent"]);
            Assert.AreEqual(3, rec.Info.ProfileVersions["tp-vav-zone"]);
            Assert.IsTrue(rec.Info.RuleEnabled["tp-r1-zone-hot"]);
            Assert.AreEqual("control", rec.Info.Label);
            Assert.AreEqual(1, rec.Header.Clock.Count);
            Assert.AreEqual(ClockSegment.Real, rec.Header.Clock[0].Mode);
            Assert.IsTrue(rec.Header.EndedCleanly);
        }

        [Test]
        public void AnAcceleratedClockIsRecordedAsASegmentAndAChangeAddsAnother()
        {
            using (var r = Create(Info(4)))
            {
                now = 30;
                r.ClockChanged(1.0);
                r.ClockChanged(1.0); // no change, no segment
                r.Close();
            }
            var rec = TowerpulseRecording.Open(Directory.GetDirectories(dir)[0]);
            Assert.AreEqual("accelerated", rec.Info.ClockMode);
            Assert.AreEqual(2, rec.Header.Clock.Count);
            Assert.AreEqual(ClockSegment.Accelerated, rec.Header.Clock[0].Mode);
            Assert.AreEqual(4.0, rec.Header.Clock[0].Scale, 1e-9);
            Assert.AreEqual(ClockSegment.Real, rec.Header.Clock[1].Mode);
            Assert.AreEqual(30.0, rec.Header.Clock[1].From, 1e-9);
            Assert.IsTrue(rec.Header.AnyAccelerated(0, 10));
            Assert.IsFalse(rec.Header.AnyAccelerated(31, 40));
        }

        [Test]
        public void ARunIsNeverOverwritten()
        {
            var h = Header(); h.RunId = "run-fixed";
            TowerpulseRecorder.Create(dir, h, Info(), () => now, () => T0).Close();
            var h2 = Header(); h2.RunId = "run-fixed";
            Assert.Throws<IOException>(() => TowerpulseRecorder.Create(dir, h2, Info(), () => now, () => T0));
        }

        [Test]
        public void TheEndSummaryCountsWhatWasWritten()
        {
            using (var r = Create(Info()))
            {
                r.Sample("TP-F4-E", new Dictionary<string, double> { { "temp_c", 24.5 }, { "co2_ppm", 700 } }, T0, T0.AddMilliseconds(40));
                r.Fact(StoryFact.Alarm("TP-F4-E", "zone-hot", "MAJOR", true, FactSource.Platform, 50000));
                r.Close();
            }
            var rec = TowerpulseRecording.Open(Directory.GetDirectories(dir)[0]);
            Assert.AreEqual(1, rec.Header.DeviceLines);
            Assert.AreEqual(1, rec.Header.PresenterLines);
            Assert.AreEqual(1, rec.Samples.Count);
            Assert.AreEqual(24.5, rec.Samples[0].Values["temp_c"], 1e-9);
            Assert.IsNotNull(rec.Samples[0].AckedAt, "a sent reading is recorded with the time the broker acknowledged it");
        }

        // ------------------------------------------------------------------ round trip

        // The day F4-East has in the film: the platform raises the alarm and queues a boost, the device receives it, the platform marks it SENT, the room
        // reaches setpoint and the device answers, the platform marks it SUCCESSFUL and the alarm clears. Then the annex chiller's absence and restart.
        static List<StoryFact> Day(double start)
        {
            var f = new List<StoryFact>();
            double t = start;
            f.Add(StoryFact.Alarm("TP-F4-E", "zone-hot", "MAJOR", true, FactSource.Platform, t += 20));
            f.Add(StoryFact.Command("c-1", "TP-F4-E", "boost-cooling", "QUEUED", FactSource.Platform, t += 1));
            var recv = new StoryFact { Kind = StoryFactKind.LedgerReceived, Source = FactSource.Device, Device = "TP-F4-E", Key = "boost-cooling", Token = "c-1", SimS = t += 2 };
            recv.Numbers["minutes"] = 15;
            f.Add(recv);
            f.Add(StoryFact.Command("c-1", "TP-F4-E", "boost-cooling", "SENT", FactSource.Platform, t += 1));
            f.Add(new StoryFact { Kind = StoryFactKind.LedgerAnswered, Source = FactSource.Device, Device = "TP-F4-E", Key = "boost-cooling", Token = "c-1", Success = true, SimS = t += 240 });
            f.Add(StoryFact.Command("c-1", "TP-F4-E", "boost-cooling", "SUCCESSFUL", FactSource.Platform, t += 1));
            f.Add(StoryFact.Alarm("TP-F4-E", "zone-hot", "MAJOR", false, FactSource.Platform, t += 30));
            f.Add(StoryFact.Alarm("TP-A-CH", "chiller-silent", "CRITICAL", true, FactSource.Platform, t += 600));
            f.Add(StoryFact.Command("c-2", "TP-A-CH", "restart", "SUCCESSFUL", FactSource.Platform, t += 90));
            return f;
        }

        static string Live(IReadOnlyList<StoryFact> facts, int count)
        {
            var cb = new CommandBoard(); var ab = new AlarmBoard(); var led = new CommandLedger();
            for (int i = 0; i < count; i++) { StoryFacts.Apply(facts[i], cb, ab, led); cb.Sync(led, facts[i].SimS); }
            return StoryFacts.Digest(cb, ab, led);
        }

        [Test]
        public void ARecordedRunReplaysToTheSameStoryStatesAsTheLiveRun()
        {
            double start = 13.9 * 3600 + 60;
            var facts = Day(start);
            using (var r = Create(Info()))
            {
                foreach (var f in facts) { now += 1; r.Fact(f); }
                r.Close();
            }
            var rec = TowerpulseRecording.Open(Directory.GetDirectories(dir)[0]);
            Assert.AreEqual(facts.Count, rec.Facts.Count);

            var plane = new TowerPlane(rec.Info.Seed, PlaneClock.FromHour(rec.Info.StartHour));
            // replay against a model that starts where the live run began and steps in 10 s; every step is compared with the live boards at that time
            var cb = new CommandBoard(); var ab = new AlarmBoard(); var led = new CommandLedger();
            var replay = new RecordingReplay(plane, rec.Facts, cb, ab, led);
            int compared = 0;
            double endS = facts[facts.Count - 1].SimS + 30;
            double wall = 0;
            while (plane.Model.TimeS < endS)
            {
                wall += 4;
                plane.Advance(wall);
                replay.Step();
                int due = 0;
                while (due < facts.Count && facts[due].SimS <= plane.Model.TimeS) due++;
                Assert.AreEqual(Live(facts, due), StoryFacts.Digest(cb, ab, led), "the replayed story layer differs from the live one at model time " + TimeClock.Format(plane.Model.TimeS));
                compared++;
            }
            Assert.IsTrue(replay.Finished);
            Assert.Greater(compared, 200);
            Assert.AreEqual(Live(facts, facts.Count), StoryFacts.Digest(cb, ab, led));
        }

        [Test]
        public void TheReplayCardsKeepTheirSources()
        {
            var facts = Day(13.9 * 3600 + 60);
            using (var r = Create(Info())) { foreach (var f in facts) r.Fact(f); r.Close(); }
            var rec = TowerpulseRecording.Open(Directory.GetDirectories(dir)[0]);
            var plane = new TowerPlane(rec.Info.Seed, PlaneClock.FromHour(rec.Info.StartHour));
            var cb = new CommandBoard(); var ab = new AlarmBoard(); var led = new CommandLedger();
            var replay = new RecordingReplay(plane, rec.Facts, cb, ab, led);
            for (double wall = 0; !replay.Finished && wall < 4000; wall += 5) { plane.Advance(wall); replay.Step(); }
            var c = cb.Get("c-1");
            Assert.IsNotNull(c);
            Assert.AreEqual(CardOutcome.Successful, c.Outcome);
            Assert.AreEqual(FactSource.Platform, c.SourceOf(CardStage.Queued), "QUEUED is the platform's word");
            Assert.AreEqual(FactSource.Device, c.SourceOf(CardStage.Done), "the device answered a second before the platform marked it, and the first word counts");
            Assert.AreEqual(FactSource.Platform, ab.Find("TP-F4-E", "zone-hot").Source);
            Assert.IsFalse(ab.Find("TP-F4-E", "zone-hot").Active);
        }

        [Test]
        public void AReplayedCommandReachesTheModelSoTheRoomCoolsAsItDid()
        {
            var facts = Day(13.9 * 3600 + 60);
            var plane = new TowerPlane(7, PlaneClock.FromHour(13.9));
            facts[2].Device = OfflinePreview.ByLayout(plane, BuildingLayout.HotZone).ExternalId; // the device the film boosts
            using (var r = Create(Info())) { foreach (var f in facts) r.Fact(f); r.Close(); }
            var rec = TowerpulseRecording.Open(Directory.GetDirectories(dir)[0]);
            Assert.AreEqual(15.0, rec.Facts[2].Numbers["minutes"], 1e-9, "the command's parameters survive the file");
            var replay = new RecordingReplay(plane, rec.Facts, new CommandBoard(), new AlarmBoard(), new CommandLedger());
            for (double wall = 0; plane.Model.TimeS < rec.Facts[2].SimS + 1; wall += 5) { plane.Advance(wall); replay.Step(); }
            Assert.AreEqual(1, plane.Commands.Actuations, "the recorded command was actuated on the replayed model");
        }

        // ------------------------------------------------------------------ refusals

        [Test]
        public void ARecordingWithoutItsStoryFileIsRefused()
        {
            using (var r = Create(Info())) r.Close();
            var folder = Directory.GetDirectories(dir)[0];
            File.Delete(Path.Combine(folder, TowerpulseFiles.Story));
            var e = Assert.Throws<RecordingFormatException>(() => TowerpulseRecording.Open(folder));
            StringAssert.Contains("story.ndjson", e.Message);
        }

        [Test]
        public void AStoryWhoseModelTimeRunsBackwardsIsRefused()
        {
            using (var r = Create(Info()))
            {
                r.Fact(StoryFact.Alarm("TP-F4-E", "zone-hot", "MAJOR", true, FactSource.Platform, 5000));
                now = 1;
                r.Fact(StoryFact.Alarm("TP-F4-E", "zone-hot", "MAJOR", false, FactSource.Platform, 4000));
                r.Close();
            }
            var e = Assert.Throws<RecordingFormatException>(() => TowerpulseRecording.Open(Directory.GetDirectories(dir)[0]));
            StringAssert.Contains("backwards", e.Message);
        }

        [Test]
        public void AFileOfAnotherVersionIsRefusedNotGuessedAt()
        {
            using (var r = Create(Info())) r.Close();
            var folder = Directory.GetDirectories(dir)[0];
            var path = Path.Combine(folder, TowerpulseFiles.Info);
            File.WriteAllText(path, File.ReadAllText(path).Replace("\"formatVersion\": 1", "\"formatVersion\": 9"));
            Assert.Throws<RecordingFormatException>(() => TowerpulseRecording.Open(folder));
        }

        [Test]
        public void AStoryLineOfAnUnknownKindIsRefused()
        {
            using (var r = Create(Info())) r.Close();
            var folder = Directory.GetDirectories(dir)[0];
            File.WriteAllText(Path.Combine(folder, TowerpulseFiles.Story), "{\"t\":1,\"sim\":2,\"k\":\"teleport\",\"src\":\"platform\",\"dev\":\"x\",\"key\":\"y\"}\n");
            Assert.Throws<RecordingFormatException>(() => TowerpulseRecording.Open(folder));
        }

        [Test]
        public void AReplayRefusesFactsOutOfModelTimeOrder()
        {
            var plane = new TowerPlane(1, PlaneClock.FromHour(14));
            var facts = new List<StoryFact>
            {
                StoryFact.Alarm("a", "k", "MAJOR", true, FactSource.Platform, 100),
                StoryFact.Alarm("a", "k", "MAJOR", false, FactSource.Platform, 50),
            };
            Assert.Throws<ArgumentException>(() => new RecordingReplay(plane, facts, new CommandBoard(), new AlarmBoard(), new CommandLedger()));
        }

        [Test]
        public void ACredentialShapedValueNeverReachesTheFile()
        {
            using (var r = Create(Info()))
            {
                var f = new StoryFact { Kind = StoryFactKind.LedgerAnswered, Source = FactSource.Device, Device = "TP-F4-E", Key = "boost-cooling", Token = "c-1", SimS = 100 };
                f.Reason = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl";
                r.Fact(f);
                r.Close();
            }
            var text = File.ReadAllText(Path.Combine(Directory.GetDirectories(dir)[0], TowerpulseFiles.Story));
            StringAssert.DoesNotContain("eyJhbGciOi", text);
        }
    }
}
