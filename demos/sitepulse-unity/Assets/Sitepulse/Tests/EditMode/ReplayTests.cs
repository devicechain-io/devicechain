// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Text;
using System.Text.Json;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Recording;
using DeviceChain.Sitepulse.Replay;
using DeviceChain.Sitepulse.Tasks;
using DeviceChain.Sitepulse.Visuals;
using NUnit.Framework;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>The replay must hold no network object: enforced on the assembly graph, which is where a stray reference would show.</summary>
    public sealed class ReplayAssemblyTests
    {
        static HashSet<string> Closure(Assembly root)
        {
            var seen = new HashSet<string>(StringComparer.Ordinal);
            var queue = new Queue<Assembly>();
            queue.Enqueue(root);
            seen.Add(root.GetName().Name);
            while (queue.Count > 0)
            {
                foreach (var r in queue.Dequeue().GetReferencedAssemblies())
                {
                    if (!seen.Add(r.Name)) continue;
                    // only this project's own assemblies and the SDK / MQTT client are followed: the framework's are not ours to read
                    if (r.Name.StartsWith("DeviceChain.", StringComparison.Ordinal)) queue.Enqueue(Assembly.Load(r));
                }
            }

            return seen;
        }

        static readonly string[] Forbidden = { "DeviceChain.Sitepulse.Platform", "DeviceChain.Sitepulse.DevicePlane", "DeviceChain.Sitepulse.App", "DeviceChain.Sdk", "DeviceChain.Sdk.Unity", "MQTTnet" };

        [Test]
        public void TheReplayRootReferencesNothingThatCanTouchTheNetwork()
        {
            var closure = Closure(typeof(ReplayRoot).Assembly);
            foreach (var name in Forbidden) CollectionAssert.DoesNotContain(closure, name, "the Replay assembly reaches " + name);
            // and what it does reach is the base, the recording format and Unity's own
            CollectionAssert.Contains(closure, "DeviceChain.Sitepulse");
            CollectionAssert.Contains(closure, "DeviceChain.Sitepulse.Recording");
        }

        [Test]
        public void TheRecordingAssemblyIsPureAndCannotReachThePlatformEither()
        {
            var closure = Closure(typeof(RunRecorder).Assembly);
            foreach (var name in Forbidden) CollectionAssert.DoesNotContain(closure, name, "the Recording assembly reaches " + name);
            var direct = typeof(RunRecorder).Assembly.GetReferencedAssemblies().Select(a => a.Name).ToList();
            Assert.IsFalse(direct.Any(n => n.StartsWith("UnityEngine", StringComparison.Ordinal)), "the recording format needs no engine module: " + string.Join(", ", direct));
        }

        [Test]
        public void TheAssemblyDefinitionsSayTheSameAsTheBuiltAssemblies()
        {
            foreach (var dir in new[] { "Replay", "Recording" })
            {
                var path = Directory.GetFiles(Path.Combine(Application.dataPath, "Sitepulse", "Scripts", dir), "*.asmdef").Single();
                using var doc = JsonDocument.Parse(File.ReadAllText(path));
                var refs = doc.RootElement.GetProperty("references").EnumerateArray().Select(e => e.GetString()).ToList();
                foreach (var name in Forbidden) CollectionAssert.DoesNotContain(refs, name, dir + " asmdef");
                foreach (var r in refs) StringAssert.DoesNotContain("Platform", r);
            }
        }

        [Test]
        public void TheCheckCanFail_TheAppAssemblyDoesReachThePlatformAndTheSdk()
        {
            // the negative control: the same walk, pointed at the Live composition, finds what the replay's must not have
            var closure = Closure(typeof(SitepulseApp).Assembly);
            CollectionAssert.Contains(closure, "DeviceChain.Sitepulse.Platform");
            CollectionAssert.Contains(closure, "DeviceChain.Sitepulse.DevicePlane");
            CollectionAssert.Contains(closure, "DeviceChain.Sitepulse.Replay", "the App hosts the replay root and so reaches it");
            CollectionAssert.Contains(closure, "DeviceChain.Sdk");
        }

        [Test]
        public void NoRecordingOrReplaySourceUsesReflectionJson()
        {
            // IL2CPP strips what reflection needs: these files read and write by hand
            foreach (var dir in new[] { "Recording", "Replay" })
                foreach (var f in Directory.GetFiles(Path.Combine(Application.dataPath, "Sitepulse", "Scripts", dir), "*.cs"))
                {
                    var text = File.ReadAllText(f);
                    foreach (var banned in new[] { "JsonSerializer.", "JsonUtility", "System.Reflection", "Activator.CreateInstance", "Newtonsoft" })
                        StringAssert.DoesNotContain(banned, text, Path.GetFileName(f));
                }
        }
    }

    public sealed class ReplayStateTests
    {
        static readonly DateTimeOffset T0 = SyntheticRun.Start;
        const string Dev = SyntheticRun.TruckToken;

        static SyntheticRun Run() => new SyntheticRun(h => h.Devices.Add(new RunDevice { Id = "SP-PL-0001", Token = "sp-plant-01", Kind = "Plant" }));

        static string Describe(DeviceReading r)
        {
            var sb = new StringBuilder();
            foreach (var key in MeasurementKeys.Equipment.Concat(MeasurementKeys.Plant))
                if (r.TryGet(key, out var v))
                {
                    sb.Append(key).Append('=').Append(v.ToString("R"));
                    if (r.TryGetStamp(key, out var s)) sb.Append('@').Append(s.OccurredAt.ToString("O")).Append('/').Append(s.ObservedAt.ToString("O"));
                    sb.Append(';');
                }

            foreach (var key in MeasurementKeys.PlantFlags)
                if (r.TryGetFlag(key, out var f))
                {
                    sb.Append(key).Append('=').Append(f);
                    if (r.TryGetStamp(key, out var s)) sb.Append('@').Append(s.OccurredAt.ToString("O"));
                    sb.Append(';');
                }

            if (r.SpeedKmh.HasValue) sb.Append("speed=").Append(r.SpeedKmh.Value.ToString("R")).Append('@').Append(r.SpeedStamp.Value.OccurredAt.ToString("O")).Append(';');
            foreach (var a in r.Alarms) sb.Append("alarm:").Append(a.Key).Append('/').Append(a.Severity).Append('/').Append(a.State).Append(';');
            if (r.Command != null) sb.Append("cmd:").Append(r.Command).Append('/').Append(r.CommandStatus.Value.Label).Append('@').Append(r.CommandStamp.Value.ObservedAt.ToString("O")).Append(';');
            return sb.ToString();
        }

        static List<ObserverItem> Stream()
        {
            var items = new List<ObserverItem>
            {
                new StatusItem("measurements", "Live", null, T0),
                new MeasurementItem(Dev, "fuel_pct", 50, T0.AddSeconds(1), T0.AddSeconds(1.1), false),
                new MeasurementItem(Dev, "fuel_pct", 49, T0.AddSeconds(0), T0.AddSeconds(1.2), false),         // older than the one held: dropped
                new MeasurementItem(Dev, "fuel_pct", 48, T0.AddSeconds(1), T0.AddSeconds(2), true),            // same instant from a snapshot: the stream's stands
                new MeasurementItem(Dev, "payload_t", 86.4, T0.AddSeconds(1), T0.AddSeconds(1.1), false),
                new MeasurementItem(Dev, "haul_cycle_s", 220, T0, T0, false),                                    // no Sitepulse key: not shown
                new MeasurementItem("sp-plant-01", "throughput_tph", 720, T0.AddSeconds(1), T0.AddSeconds(1.1), false),
                new MeasurementItem("sp-plant-01", "plant_running", 1, T0.AddSeconds(1), T0.AddSeconds(1.1), false),
                new LocationItem(Dev, new ObservedLocation { SpeedMps = 5.2397, OccurredAt = T0.AddSeconds(2), ObservedAt = T0.AddSeconds(2.2) }),
                new AlarmItem(Dev, Alarm("a1", "low-fuel", "ACTIVE", 3)),
                new AlarmItem(Dev, Alarm("a2", "engine-overheat", "ACTIVE", 3.5)),
                new AlarmItem(Dev, Alarm("a3", "brake-wear", "ACTIVE", 3.6)),                                    // no card knows it
                new AlarmItem(Dev, Alarm("a1", "low-fuel", "CLEARED", 2)),                                       // older than the held ACTIVE: dropped
                new AlarmSnapshotItem(new[] { new AlarmItem(Dev, Alarm("a2", "engine-overheat", "ACTIVE", 3.5)) }, T0.AddSeconds(4), T0.AddSeconds(4.1)),   // whole answer: a1 ended unseen
                new AlarmItem(Dev, Alarm("a4", "tyre-pressure-low", "ACTIVE", 5)),
                new AlarmSnapshotItem(new AlarmItem[0], T0.AddSeconds(6), T0.AddSeconds(6.1), true, 3),         // partial: clears nothing
                new CommandItem(Dev, Command("c1", "goto-refuel", "SENT", 6)),
                new CommandItem(Dev, Command("c1", "goto-refuel", "SUCCESSFUL", 6)),
                new CommandItem(Dev, Command("c1", "goto-refuel", "SENT", 6)),                                   // a finished command is not unfinished again
                new LocationItem(Dev, new ObservedLocation { SpeedMps = null, OccurredAt = T0.AddSeconds(7), ObservedAt = T0.AddSeconds(7) }),   // no speed is not zero
                new PresenceItem(Dev, new ObservedPresence { Active = true, ObservedAt = T0.AddSeconds(7) }),
            };
            for (var i = 0; i < 18; i++)
                items.Add(new CommandItem(Dev, Command("many-" + i.ToString("00"), i % 2 == 0 ? "goto-area" : "bogus", i % 3 == 0 ? "SUCCESSFUL" : "QUEUED", 7 + i * 0.1)));
            items.Add(new StatusItem("measurements", "Reconnecting", "dropped", T0.AddSeconds(10)));
            return items;
        }

        static ObservedAlarm Alarm(string token, string key, string state, double at) =>
            new ObservedAlarm { Token = token, AlarmKey = key, State = state, Severity = "MAJOR", OccurredAt = T0.AddSeconds(at), ObservedAt = T0.AddSeconds(at + 0.1) };

        static ObservedCommand Command(string token, string name, string status, double queued) =>
            new ObservedCommand { Token = token, Name = name, Status = status, QueuedAt = T0.AddSeconds(queued), ObservedAt = T0.AddSeconds(queued + 0.3) };

        [Test]
        public void ARecordedStreamReplaysToTheSameCardsTheLiveStateShowedForIt()
        {
            using var run = Run();
            run.Drive(0, 12.0);
            var live = new ObservedState(T0);
            var status = new ObserverStatus();
            var items = Stream();
            // the live app applies each item and the recording takes it just before: the same stream, two readers
            var t = 0.0;
            foreach (var item in items)
            {
                t += 0.1;
                run.T = t;
                run.Recorder.Observed(RecordingMaps.Observed(item));
                ObserverApplier.Apply(live, status, item, null);
            }

            var data = run.Reload();
            Assert.AreEqual(items.Count, data.Observed.Count);
            var session = new ReplaySession(data);
            session.Seek(session.Duration);

            var observed = new ObservedReadingSource(live, id => id == "SP-HL-0006" ? Dev : id == "SP-PL-0001" ? "sp-plant-01" : null, () => status.Measurements.IsLive);
            var replayed = new ReplayReadingSource(session);
            Assert.AreEqual(observed.StreamLive, replayed.StreamLive, "the measurement stream's last word");
            Assert.IsFalse(replayed.StreamLive);

            foreach (var (id, profile) in new[] { ("SP-HL-0006", DeviceReading.Profile.Equipment), ("SP-PL-0001", DeviceReading.Profile.Plant), ("SP-LD-0003", DeviceReading.Profile.Equipment) })
            {
                var a = new DeviceReading(id, profile, Provenance.Observed);
                var b = new DeviceReading(id, profile, Provenance.Replayed);
                observed.Fill(new ReadingSubject(id), a, T0);
                replayed.Fill(new ReadingSubject(id), b, T0);
                Assert.AreEqual(Describe(a), Describe(b), id);
                if (id == "SP-HL-0006")
                {
                    StringAssert.Contains("fuel_pct=50", Describe(b), "the stream's 50 stood over the snapshot's 48 and the older 49");
                    StringAssert.Contains("alarm:engine-overheat", Describe(b));
                    StringAssert.Contains("alarm:tyre-pressure-low", Describe(b));
                    StringAssert.DoesNotContain("low-fuel", Describe(b), "a1 ended while the observer was not looking, and the snapshot said so");
                    StringAssert.DoesNotContain("speed=", Describe(b), "a location with no speed does not become a speed of zero");
                }
            }
        }

        [Test]
        public void TheReplayBuildsTheStateAsOfTheCursorAndGoingBackRebuildsIt()
        {
            using var run = Run();
            run.Drive(0, 10.0);
            run.Obs(1.0, SyntheticRun.Measurement(Dev, "fuel_pct", 30, T0, T0));
            run.Obs(2.0, SyntheticRun.Alarm(Dev, "al-1", "low-fuel", "ACTIVE", T0.AddSeconds(2)));
            run.Obs(8.0, SyntheticRun.Alarm(Dev, "al-1", "low-fuel", "CLEARED", T0.AddSeconds(8)));
            var data = run.Reload();
            var session = new ReplaySession(data);
            bool Active()
            {
                var f = session.State.FactsOf(Dev);
                var list = new List<AlarmFact>();
                f?.ActiveAlarms(list);
                return list.Count > 0;
            }

            session.Seek(1.5);
            Assert.IsFalse(Active());
            session.Seek(5.0);
            Assert.IsTrue(Active());
            session.Seek(9.0);
            Assert.IsFalse(Active());
            session.Seek(3.0);
            Assert.IsTrue(Active(), "going back rebuilds from the start: the alarm was active at 3 s");
            session.Seek(0.0);
            Assert.IsNull(session.State.FactsOf(Dev), "nothing had been said by t=0");
            Assert.AreEqual(0, session.State.Applied);
        }

        [Test]
        public void TheSameStepsGiveTheSameStateAndTheClockIsTheRecordings()
        {
            using var run = Run();
            run.Drive(0, 4.0);
            run.Obs(1.0, SyntheticRun.Measurement(Dev, "fuel_pct", 30, T0, T0));
            var data = run.Reload();
            var a = new ReplaySession(data);
            var b = new ReplaySession(data);
            for (var i = 0; i < 100; i++) a.Advance(1.0 / 60.0);
            b.Seek(100.0 / 60.0);
            Assert.AreEqual(a.Time, b.Time, 1e-9);
            Assert.AreEqual(a.State.Applied, b.State.Applied);
            a.TrySample(SyntheticRun.Truck, out var pa);
            b.TrySample(SyntheticRun.Truck, out var pb);
            Assert.AreEqual(pa.X, pb.X, 1e-4f);
            Assert.AreEqual(T0 + TimeSpan.FromSeconds(a.Time), a.WallClock, "a card's age is judged against the moment the viewer was living in, not today");
            a.Seek(1000);
            Assert.IsTrue(a.AtEnd);
            Assert.AreEqual(a.Duration, a.Time, 1e-9, "the cursor stops at the end of the recording");
            a.Seek(-5);
            Assert.AreEqual(0.0, a.Time);
            Assert.IsFalse(a.TrySample("SP-NOPE", out _));
        }

        [Test]
        public void TheTimelinePanelIsTheRecordedTimelineUpToTheCursor()
        {
            using var run = Run();
            run.Drive(0, 6.0);
            run.Dev(1.0, SyntheticRun.Row(SyntheticRun.Truck, "received", "goto-refuel c1"));
            run.Dev(1.5, SyntheticRun.Row(SyntheticRun.Truck, "accepted", "route 212 m"));
            run.Dev(2.0, SyntheticRun.Row(SyntheticRun.Loader, "received", "goto-area yard"));
            run.Dev(4.0, SyntheticRun.Row(SyntheticRun.Truck, "arrived", "at the bay"));
            var session = new ReplaySession(run.Reload());
            session.Seek(0.5);
            Assert.IsNull(session.LastCommanded);
            Assert.IsNull(session.TimelineText(SyntheticRun.Truck));
            session.Seek(1.6);
            Assert.AreEqual(SyntheticRun.Truck, session.LastCommanded);
            Assert.AreEqual(2, session.Rows(SyntheticRun.Truck).Count);
            session.Seek(2.5);
            Assert.AreEqual(SyntheticRun.Loader, session.LastCommanded, "the machine most recently commanded");
            session.Seek(5.0);
            var text = session.TimelineText(SyntheticRun.Truck);
            StringAssert.Contains("received", text);
            StringAssert.Contains("arrived", text);
            StringAssert.Contains("at the bay", text);
            Assert.AreEqual(3, session.Rows(SyntheticRun.Truck).Count);
            // the rows are printed as the live panel prints them, time of day included
            StringAssert.Contains("14:00:01  received", text);
        }

        [Test]
        public void GoingBackBeforeTheStreamWentLiveLeavesNoLiveStreamBehind()
        {
            using var run = Run();
            run.Drive(0, 8.0);
            run.Obs(5.0, SyntheticRun.Status("measurements", "Live", T0.AddSeconds(5)));
            var session = new ReplaySession(run.Reload());
            var source = new ReplayReadingSource(session);
            session.Seek(2.0);
            Assert.IsFalse(source.StreamLive);
            session.Seek(6.0);
            Assert.IsTrue(source.StreamLive, "the status line at 5 s is applied by 6 s");
            session.Seek(2.0);
            Assert.IsFalse(source.StreamLive, "back at 2 s the stream was not yet live: a rebuilt state does not remember the later one");
            Assert.AreEqual(0, session.State.Applied);
        }

        [Test]
        public void ALineAtExactlyTheCursorIsAppliedAndOneJustAfterIsNot()
        {
            using var run = Run();
            run.Drive(0, 4.0);
            run.Obs(2.0, SyntheticRun.Measurement(Dev, "fuel_pct", 30, T0.AddSeconds(2), T0.AddSeconds(2)));
            var data = run.Reload();

            var forward = new ReplaySession(data);
            forward.Seek(1.999);
            Assert.AreEqual(0, forward.State.Applied);
            forward.Seek(2.0);
            Assert.AreEqual(1, forward.State.Applied, "the cursor is at the line's own time: the viewer had seen it");

            var back = new ReplaySession(data);
            back.Seek(3.0);
            back.Seek(2.0);
            Assert.AreEqual(1, back.State.Applied, "a rebuilt state is as of the cursor, boundary included");
            back.Seek(1.999);
            Assert.AreEqual(0, back.State.Applied);

            var stepped = new ReplaySession(data);
            stepped.Advance(1.0);
            stepped.Advance(1.0);
            Assert.AreEqual(1, stepped.State.Applied);
        }

        [Test]
        public void TwoCommandTokensThatShareTheirLastFourReplayAsTwoCommandsJustAsTheyWereShownLive()
        {
            const string first = "0123456789abcdef0123456789abcdef", second = "fedcba9876543210fedcba9876abcdef";
            using var run = Run();
            run.Drive(0, 12.0);
            var live = new ObservedState(T0);
            var status = new ObserverStatus();
            var items = new List<ObserverItem>
            {
                new CommandItem(Dev, Command(first, "goto-refuel", "SUCCESSFUL", 6)),
                new CommandItem(Dev, Command(second, "goto-area", "SENT", 8)),          // a different command, after: it is the latest, and unfinished
            };
            var t = 0.0;
            foreach (var item in items)
            {
                t += 1.0;
                run.T = t;
                run.Recorder.Observed(RecordingMaps.Observed(item));
                ObserverApplier.Apply(live, status, item, null);
            }

            var data = run.Reload();
            Assert.AreNotEqual(data.Observed[0].Token, data.Observed[1].Token, "what was written keeps them apart");
            var session = new ReplaySession(data);
            session.Seek(session.Duration);
            var observed = new ObservedReadingSource(live, id => id == "SP-HL-0006" ? Dev : null, () => status.Measurements.IsLive);
            var replayed = new ReplayReadingSource(session);
            var a = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment, Provenance.Observed);
            var b = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment, Provenance.Replayed);
            observed.Fill(new ReadingSubject("SP-HL-0006"), a, T0);
            replayed.Fill(new ReadingSubject("SP-HL-0006"), b, T0);
            StringAssert.Contains("cmd:goto-area/", Describe(a), "live, the second command is the one on the card");
            Assert.AreEqual(Describe(a), Describe(b));
        }
    }

    public sealed class ReplayProvenanceTests
    {
        static readonly DateTimeOffset T0 = SyntheticRun.Start;

        static ReplaySession SessionWithFuel(SyntheticRun run)
        {
            run.Drive(0, 3.0);
            run.Obs(1.0, SyntheticRun.Measurement(SyntheticRun.TruckToken, "fuel_pct", 33, T0, T0));
            return new ReplaySession(run.Reload());
        }

        [Test]
        public void EveryReplayedValueIsReplayedNeverObserved()
        {
            using var run = new SyntheticRun();
            var session = SessionWithFuel(run);
            session.Seek(2.0);
            var source = new ReplayReadingSource(session);
            Assert.AreEqual(Provenance.Replayed, source.Provenance);
            var r = new DeviceReading(SyntheticRun.Truck, DeviceReading.Profile.Equipment, source.Provenance);
            source.Fill(new ReadingSubject(SyntheticRun.Truck), r, T0);
            Assert.IsTrue(r.TryGet("fuel_pct", out var v));
            Assert.AreEqual(33, v);
            Assert.AreEqual(Provenance.Replayed, r.Provenance);
            Assert.IsTrue(r.TryGetStamp("fuel_pct", out var stamp), "a replayed value carries when it happened and was seen");
            Assert.AreEqual(T0, stamp.OccurredAt);
        }

        [Test]
        public void AReplayedSourceRefusesAnObservedOrAnIllustrativeReading()
        {
            using var run = new SyntheticRun();
            var session = SessionWithFuel(run);
            session.Seek(2.0);
            var source = new ReplayReadingSource(session);
            var observed = new DeviceReading(SyntheticRun.Truck, DeviceReading.Profile.Equipment, Provenance.Observed);
            var illustrative = new DeviceReading(SyntheticRun.Truck, DeviceReading.Profile.Equipment, Provenance.Illustrative);
            Assert.Throws<InvalidOperationException>(() => source.Fill(new ReadingSubject(SyntheticRun.Truck), observed, T0), "a recorded value must not be written to a reading that says it was observed");
            Assert.Throws<InvalidOperationException>(() => source.Fill(new ReadingSubject(SyntheticRun.Truck), illustrative, T0));
            Assert.IsFalse(observed.TryGet("fuel_pct", out _));
        }

        [Test]
        public void AnObservedFillerCannotFillAReplayedReadingAndNothingIllustrativeCanFillFromFacts()
        {
            var facts = new OneFact();
            var replayed = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment, Provenance.Replayed);
            Assert.Throws<InvalidOperationException>(() => new FactsFiller(Provenance.Observed).Fill("SP-HL-0006", facts, replayed));
            Assert.Throws<ArgumentException>(() => new FactsFiller(Provenance.Illustrative));
            Assert.DoesNotThrow(() => new FactsFiller(Provenance.Replayed).Fill("SP-HL-0006", facts, replayed));
        }

        sealed class OneFact : IDeviceFacts
        {
            public bool TryMeasurement(string key, out MeasuredValue value)
            {
                value = new MeasuredValue(40, SyntheticRun.Start, SyntheticRun.Start);
                return key == "fuel_pct";
            }

            public bool TryLocation(out LocationFact location)
            {
                location = default;
                return false;
            }

            public void ActiveAlarms(List<AlarmFact> into)
            {
            }

            public bool TryLastCommand(out CommandFact command)
            {
                command = default;
                return false;
            }
        }

        [Test]
        public void AReplayedCardAgesAsItDidLiveAndSaysItIsReplayedUnlessItIsRenderedOffline()
        {
            var at = T0;
            var fresh = CardPresenter.Status(Provenance.Replayed, at, at.AddSeconds(1), true, false, false);
            Assert.AreEqual("replayed", fresh.Tag);
            Assert.AreEqual(DotTone.Ok, fresh.Dot);
            var stale = CardPresenter.Status(Provenance.Replayed, at, at.AddSeconds(8), true, false, false);
            Assert.AreEqual("replayed · stale 8 s", stale.Tag);
            Assert.AreEqual(DotTone.Warn, stale.Dot);
            var gone = CardPresenter.Status(Provenance.Replayed, null, at, true, false, false);
            Assert.AreEqual("replayed · no data", gone.Tag);
            // an offline render draws no words: a fresh card says nothing, a stale one still says it is stale
            Assert.AreEqual("", CardPresenter.Status(Provenance.Replayed, at, at.AddSeconds(1), true, false, false, replayTag: false).Tag);
            Assert.AreEqual("stale 8 s", CardPresenter.Status(Provenance.Replayed, at, at.AddSeconds(8), true, false, false, replayTag: false).Tag);
            // the other two provenances are as they were
            Assert.AreEqual("observed", CardPresenter.Status(Provenance.Observed, at, at.AddSeconds(1), true, false, false).Tag);
            Assert.AreEqual("illustrative", CardPresenter.Status(Provenance.Illustrative, null, at, true, false, false).Tag);
            // and a replayed value is a value that can go stale: a dash for one a minute old, never a number
            Assert.AreEqual(CardPresenter.NoValue, CardPresenter.Row(true, "33 %", at, at.AddSeconds(90), true).Text);
        }
    }

    public sealed class ReplayShotTests
    {
        static readonly DateTimeOffset T0 = SyntheticRun.Start;
        const string Dev = SyntheticRun.TruckToken;

        static SyntheticRun Run()
        {
            var run = new SyntheticRun();
            run.Drive(0, 12.0);
            var snap = ObservedLine.Of(ObservedKinds.AlarmSnapshot);
            snap.RequestedAt = T0.AddSeconds(0.4); snap.ObservedAt = T0.AddSeconds(0.5);
            snap.Alarms.Add(SyntheticRun.Alarm(Dev, "old", "low-fuel", "ACTIVE", T0.AddSeconds(-600)));
            run.Obs(0.2, SyntheticRun.Measurement(Dev, "fuel_pct", 10, T0, T0, snapshot: true));
            run.Obs(0.5, snap);                                                                                              // an alarm left over from an earlier run, listed by a snapshot
            return run;
        }

        static void Events(SyntheticRun run)
        {
            run.Obs(1.0, SyntheticRun.Measurement(Dev, "fuel_pct", 20, T0.AddSeconds(1), T0.AddSeconds(1)));
            run.Obs(2.5, SyntheticRun.Measurement(Dev, "fuel_pct", 14.9, T0.AddSeconds(2.5), T0.AddSeconds(2.5)));
            run.Obs(3.0, SyntheticRun.Alarm(Dev, "al-1", "low-fuel", "ACTIVE", T0.AddSeconds(3)));
            run.Obs(3.5, SyntheticRun.Command(Dev, "c-1", "goto-refuel", "SENT", T0.AddSeconds(3.4), T0.AddSeconds(3.5)));
            run.Obs(4.0, SyntheticRun.Alarm(SyntheticRun.LoaderToken, "al-2", "low-fuel", "ACTIVE", T0.AddSeconds(4)));
            run.Dev(7.0, SyntheticRun.Row(SyntheticRun.Truck, "arrived", "at the Bay"));
            run.Obs(9.0, SyntheticRun.Alarm(Dev, "al-1", "low-fuel", "CLEARED", T0.AddSeconds(9)));
        }

        [Test]
        public void ASelectorFindsTheRecordedEventItNamesAndNoOtherOne()
        {
            using var run = Run();
            Events(run);
            var data = run.Reload();
            var active = new EventSelector { Kind = "alarm", Key = "low-fuel", State = "ACTIVE", Device = SyntheticRun.Truck }.Resolve(data);
            Assert.AreEqual(3.0, active.T, 1e-9, "the truck's alarm: not the snapshot's leftover (0.5), not the loader's (4.0), not the clearing (9.0)");
            Assert.AreEqual(T0.AddSeconds(3.0), active.Utc);
            StringAssert.Contains("low-fuel ACTIVE on SP-HL-0006", active.Description);
            Assert.AreEqual(4.0, new EventSelector { Kind = "alarm", Key = "low-fuel", State = "ACTIVE", Occurrence = 1 }.Resolve(data).T, 1e-9, "the second ACTIVE low-fuel on any device");
            Assert.AreEqual(9.0, new EventSelector { Kind = "alarm", Key = "low-fuel", State = "CLEARED", Device = SyntheticRun.Truck }.Resolve(data).T, 1e-9);
            Assert.AreEqual(3.5, new EventSelector { Kind = "command", Name = "goto-refuel", Status = "SENT", Device = SyntheticRun.Truck }.Resolve(data).T, 1e-9);
            Assert.AreEqual(2.5, new EventSelector { Kind = "measurement", Name = "fuel_pct", Below = 15, Device = SyntheticRun.Truck }.Resolve(data).T, 1e-9, "the first sample under the line, not the snapshot's old 10");
            Assert.AreEqual(7.0, new EventSelector { Kind = "timeline", Device = SyntheticRun.Truck, RowKind = "arrived", Contains = "bay" }.Resolve(data).T, 1e-9);
            Assert.AreEqual(0.0, new EventSelector { Kind = "runStart" }.Resolve(data).T);
        }

        [Test]
        public void ASelectorThatMatchesNothingFailsLoudlyAndSaysWhatTheRecordingHolds()
        {
            using var run = Run();
            Events(run);
            var data = run.Reload();
            var ex = Assert.Throws<ShotException>(() => new EventSelector { Kind = "alarm", Key = "engine-overheat", State = "ACTIVE" }.Resolve(data));
            StringAssert.Contains("no recorded event matches", ex.Message);
            StringAssert.Contains("low-fuel ACTIVE", ex.Message, "it names what there is");
            ex = Assert.Throws<ShotException>(() => new EventSelector { Kind = "alarm", Key = "low-fuel", State = "ACTIVE", Occurrence = 5 }.Resolve(data));
            StringAssert.Contains("occurrence 5 does not exist", ex.Message);
            ex = Assert.Throws<ShotException>(() => new EventSelector { Kind = "alarm", Key = "low-fuel", Device = "SP-XX-0001" }.Resolve(data));
            StringAssert.Contains("SP-HL-0006", ex.Message, "it lists the devices it does hold");
            Assert.Throws<ShotException>(() => new EventSelector { Kind = "command", Name = "goto-area" }.Resolve(data));
            Assert.Throws<ShotException>(() => new EventSelector { Kind = "measurement", Name = "fuel_pct", Below = 1, Device = SyntheticRun.Truck }.Resolve(data));
            Assert.Throws<ShotException>(() => new EventSelector { Kind = "timeline", Device = SyntheticRun.Truck, RowKind = "refuelling" }.Resolve(data));
        }

        const string Good = @"{""fps"":60,""shots"":[{""name"":""s09"",""startEvent"":{""kind"":""alarm"",""key"":""low-fuel"",""state"":""ACTIVE"",""device"":""SP-HL-0006""},""offset"":-2,""duration"":5,
            ""camera"":{""rig"":""follow"",""target"":""SP-HL-0006"",""fov"":50}}]}";

        [Test]
        public void ShotsStartFromEventsAndAreResolvedAgainstTheRecordingBeforeAnyFrameIsRendered()
        {
            using var run = Run();
            Events(run);
            var data = run.Reload();
            var plan = ShotPlanner.Plan(ShotFile.Parse(Good), data);
            Assert.AreEqual(1, plan.Count);
            Assert.AreEqual(1.0, plan[0].Start, 1e-9, "two seconds before the alarm line was received");
            Assert.AreEqual(300, plan[0].Frames, "5 s at 60 fps");
            Assert.AreEqual(0.0, plan[0].PrerollFrom, 1e-9, "the lead-in cannot start before the recording does");
            Assert.AreEqual(1920, plan[0].Shot.Width);
            Assert.AreEqual(1080, plan[0].Shot.Height);

            // a shot past the end, or following a machine the recording does not hold, stops the whole render
            var late = Good.Replace("\"duration\":5", "\"duration\":20");
            StringAssert.Contains("is 12", Assert.Throws<ShotException>(() => ShotPlanner.Plan(ShotFile.Parse(late), data)).Message);
            var ghost = Good.Replace("\"target\":\"SP-HL-0006\"", "\"target\":\"SP-HL-9999\"");
            StringAssert.Contains("SP-HL-9999", Assert.Throws<ShotException>(() => ShotPlanner.Plan(ShotFile.Parse(ghost), data)).Message);
            var absent = Good.Replace("low-fuel", "engine-overheat");
            Assert.Throws<ShotException>(() => ShotPlanner.Plan(ShotFile.Parse(absent), data));
            // and a shot that would begin before the recording does begins at its start
            var early = Good.Replace("\"offset\":-2", "\"offset\":-30");
            Assert.AreEqual(0.0, ShotPlanner.Plan(ShotFile.Parse(early), data)[0].Start);
            var preroll = Good.Replace("\"offset\":-2", "\"offset\":2");
            Assert.AreEqual(2.0, ShotPlanner.Plan(ShotFile.Parse(preroll), data)[0].PrerollFrom, 1e-9, "3 s of lead-in before a start at 5 s");
        }

        [Test]
        public void AShotsFileThatCannotBeTrustedIsRefusedWithTheShotAndTheField()
        {
            void Refuses(string json, string expect) => StringAssert.Contains(expect, Assert.Throws<ShotException>(() => ShotFile.Parse(json)).Message);
            Refuses("{", "not JSON");
            Refuses(@"{""shots"":[]}", "no \"shots\"");
            Refuses(Good.Replace("\"startEvent\":{\"kind\":\"alarm\",\"key\":\"low-fuel\",\"state\":\"ACTIVE\",\"device\":\"SP-HL-0006\"}", "\"t\":12.5"), "unknown field \"t\"");
            Refuses(@"{""shots"":[{""name"":""a"",""duration"":2,""camera"":{""rig"":""follow"",""target"":""x""}}]}", "startEvent");
            Refuses(Good.Replace("\"duration\":5,", ""), "duration");
            Refuses(Good.Replace("\"name\":\"s09\"", "\"name\":\"../s09\""), "directory name");
            Refuses(Good.Replace("\"fps\":60", "\"fps\":24"), "fps 24");
            Refuses(Good.Replace("\"kind\":\"alarm\"", "\"kind\":\"alarmm\""), "alarmm");
            Refuses(Good.Replace("\"state\":\"ACTIVE\"", "\"stat\":\"ACTIVE\""), "unknown field \"stat\"");
            Refuses(Good.Replace("\"rig\":\"follow\"", "\"rig\":\"crane\""), "crane");
            Refuses(Good.Replace("\"fov\":50", "\"fov\":500"), "fov");
            Refuses(Good.Replace("\"duration\":5,", "\"duration\":5,\"aspect\":\"4:3\","), "4:3");
            Refuses(@"{""shots"":[{""name"":""a"",""startEvent"":{""kind"":""runStart""},""duration"":2,""camera"":{""rig"":""fixed"",""lookAt"":[0,0,0]}}]}", "pos");
            var twice = Good.Replace("]}", ",") + Good.Substring(Good.IndexOf("{\"name\"", StringComparison.Ordinal)) ;
            Refuses(twice, "used twice");
        }

        [Test]
        public void AspectsGiveLandscapeAndPortraitFramesOfTheSameTimeline()
        {
            var land = ShotFile.Parse(Good).Shots[0];
            var port = ShotFile.Parse(Good.Replace("\"duration\":5,", "\"duration\":5,\"aspect\":\"9:16\",")).Shots[0];
            Assert.AreEqual((1920, 1080), (land.Width, land.Height));
            Assert.AreEqual((1080, 1920), (port.Width, port.Height));
            var custom = ShotFile.Parse(Good.Replace("\"duration\":5,", "\"duration\":5,\"width\":1280,\"height\":720,")).Shots[0];
            Assert.AreEqual((1280, 720), (custom.Width, custom.Height));
            Assert.Throws<ShotException>(() => ShotFile.Parse(Good.Replace("\"duration\":5,", "\"duration\":5,\"width\":1281,\"height\":720,")));
        }

        [Test]
        public void TheShippedSampleShotsFileParses()
        {
            var path = Path.GetFullPath(Path.Combine(Application.dataPath, "..", "tools", "shots", "sitepulse-video-sample.json"));
            Assert.IsTrue(File.Exists(path), path);
            var file = ShotFile.Parse(File.ReadAllText(path));
            Assert.GreaterOrEqual(file.Shots.Count, 2);
            Assert.IsTrue(file.Shots.Any(s => s.StartEvent.Kind == "alarm" && s.StartEvent.Key == AlarmKeys.LowFuel && s.StartEvent.Device == "SP-HL-0006"), "S09: the low-fuel alarm on SP-HL-0006");
            Assert.IsTrue(file.Shots.Any(s => s.Camera.Rig == RigKind.Fixed), "an establishing shot");
        }

        [Test]
        public void ADeviceInASelectorPicksThatDevicesEventNotAnEarlierOneOnAnotherMachine()
        {
            using var run = Run();
            // the loader has the same alarm, the same command and a low fuel reading, all BEFORE the truck's
            run.Obs(0.7, SyntheticRun.Measurement(SyntheticRun.LoaderToken, "fuel_pct", 5, T0.AddSeconds(0.7), T0.AddSeconds(0.7)));
            run.Obs(0.8, SyntheticRun.Alarm(SyntheticRun.LoaderToken, "al-0", "low-fuel", "ACTIVE", T0.AddSeconds(0.8)));
            run.Obs(0.9, SyntheticRun.Command(SyntheticRun.LoaderToken, "c-0", "goto-refuel", "SENT", T0.AddSeconds(0.85), T0.AddSeconds(0.9)));
            Events(run);
            var data = run.Reload();
            const string truck = SyntheticRun.Truck, loader = SyntheticRun.Loader;
            Assert.AreEqual(3.0, new EventSelector { Kind = "alarm", Key = "low-fuel", State = "ACTIVE", Device = truck }.Resolve(data).T, 1e-9);
            Assert.AreEqual(3.5, new EventSelector { Kind = "command", Name = "goto-refuel", Status = "SENT", Device = truck }.Resolve(data).T, 1e-9);
            Assert.AreEqual(2.5, new EventSelector { Kind = "measurement", Name = "fuel_pct", Below = 15, Device = truck }.Resolve(data).T, 1e-9);
            // the other way round, and with no device the first of any
            Assert.AreEqual(0.8, new EventSelector { Kind = "alarm", Key = "low-fuel", State = "ACTIVE", Device = loader }.Resolve(data).T, 1e-9);
            Assert.AreEqual(0.9, new EventSelector { Kind = "command", Name = "goto-refuel", Status = "SENT", Device = loader }.Resolve(data).T, 1e-9);
            Assert.AreEqual(0.7, new EventSelector { Kind = "measurement", Name = "fuel_pct", Below = 15, Device = loader }.Resolve(data).T, 1e-9);
            Assert.AreEqual(0.8, new EventSelector { Kind = "alarm", Key = "low-fuel", State = "ACTIVE" }.Resolve(data).T, 1e-9);
            Assert.AreEqual(0.9, new EventSelector { Kind = "command", Name = "goto-refuel" }.Resolve(data).T, 1e-9);
            Assert.AreEqual(0.7, new EventSelector { Kind = "measurement", Name = "fuel_pct", Below = 15 }.Resolve(data).T, 1e-9);
            // and the occurrence counts that device's events alone
            Assert.AreEqual(9.0, new EventSelector { Kind = "alarm", Key = "low-fuel", Device = truck, Occurrence = 1 }.Resolve(data).T, 1e-9, "the truck's second low-fuel line is its clearing, not the loader's activation");
        }

        [Test]
        public void AShotThatWouldStartBeforeTheRecordingStartsAtZeroAndSaysSo()
        {
            using var run = Run();
            Events(run);
            var data = run.Reload();
            var warnings = new List<string>();
            var early = ShotPlanner.Plan(ShotFile.Parse(Good.Replace("\"offset\":-2", "\"offset\":-30")), data, warnings.Add)[0];
            Assert.AreEqual(0.0, early.Start);
            Assert.IsTrue(early.StartClamped);
            Assert.AreEqual(27.0, early.ClampedBySeconds, 1e-9, "the alarm is at 3 s and the shot asked for 30 s before it: 27 s before the recording began");
            Assert.AreEqual(1, warnings.Count);
            StringAssert.Contains("shot s09", warnings[0]);
            StringAssert.Contains("27.0 s before the recording began", warnings[0]);
            var fine = ShotPlanner.Plan(ShotFile.Parse(Good), data, warnings.Add)[0];
            Assert.IsFalse(fine.StartClamped);
            Assert.AreEqual(0.0, fine.ClampedBySeconds);
            Assert.AreEqual(1, warnings.Count, "a shot that starts where it asked to says nothing");

            var file = ShotFile.Parse(Good.Replace("\"offset\":-2", "\"offset\":-30"));
            var json = RenderReport.Build(data, run.Dir, file, new List<PlannedShot> { early }, null, T0, BuildInfo.Unknown(), null);
            using var doc = JsonDocument.Parse(json);
            var shot = doc.RootElement.GetProperty("shots")[0];
            Assert.IsTrue(shot.GetProperty("startClamped").GetBoolean());
            Assert.AreEqual(27.0, shot.GetProperty("startClampedBySeconds").GetDouble(), 1e-6);
            Assert.AreEqual(0.0, shot.GetProperty("startRunSeconds").GetDouble());
            using var doc2 = JsonDocument.Parse(RenderReport.Build(data, run.Dir, ShotFile.Parse(Good), new List<PlannedShot> { fine }, null, T0, BuildInfo.Unknown(), null));
            Assert.IsFalse(doc2.RootElement.GetProperty("shots")[0].TryGetProperty("startClamped", out _));
        }
    }

    public sealed class ReplayCameraTests
    {
        static MachineSample At(float x, float y, float z, float heading) => new MachineSample { X = x, Y = y, Z = z, Heading = heading };

        [Test]
        public void AFollowCameraSitsBehindAndToTheSideOfTheMachineWhicheverWayItFaces()
        {
            var spec = new CameraSpec { Rig = RigKind.Follow, Target = "m", Back = 12, Up = 4, Side = -3, Fov = 50, LookHeight = 1.5f };
            // facing east (heading 90): behind is west, the left is north
            var east = CameraRigs.Evaluate(spec, _ => At(100, 10, 50, 90f), 0, 5);
            Assert.AreEqual(88f, east.Position.x, 1e-3f);
            Assert.AreEqual(14f, east.Position.y, 1e-3f);
            Assert.AreEqual(53f, east.Position.z, 1e-3f);
            Assert.AreEqual(new Vector3(100, 11.5f, 50), east.LookAt);
            Assert.AreEqual(50f, east.Fov);
            // facing north (heading 0): behind is south, the left is west
            var north = CameraRigs.Evaluate(spec, _ => At(0, 0, 0, 0f), 0, 5);
            Assert.AreEqual(-3f, north.Position.x, 1e-3f);
            Assert.AreEqual(-12f, north.Position.z, 1e-3f);
        }

        [Test]
        public void AnOrbitCameraGoesRoundAtItsRateAndAFixedOneEasesOverTheShot()
        {
            var orbit = new CameraSpec { Rig = RigKind.Orbit, Point = new[] { 10f, 0f, 20f }, Radius = 30, Height = 8, DegreesPerSecond = 90, StartDegrees = 0, Fov = 40 };
            var a = CameraRigs.Evaluate(orbit, _ => default, 0, 10);
            var b = CameraRigs.Evaluate(orbit, _ => default, 1, 10);
            Assert.AreEqual(10f, a.Position.x, 1e-3f); Assert.AreEqual(50f, a.Position.z, 1e-3f); Assert.AreEqual(8f, a.Position.y, 1e-3f);
            Assert.AreEqual(40f, b.Position.x, 1e-3f, "a quarter turn a second at 90 degrees a second"); Assert.AreEqual(20f, b.Position.z, 1e-3f);

            var crane = new CameraSpec { Rig = RigKind.Fixed, Pos = new[] { -130f, 64f, -165f }, LookAt = new[] { 6f, -8f, -32f }, Fov = 42, ToPos = new[] { -100f, 40f, -140f }, ToFov = 36 };
            var start = CameraRigs.Evaluate(crane, _ => default, 0, 6);
            var end = CameraRigs.Evaluate(crane, _ => default, 6, 6);
            var mid = CameraRigs.Evaluate(crane, _ => default, 3, 6);
            Assert.AreEqual(new Vector3(-130, 64, -165), start.Position);
            Assert.AreEqual(new Vector3(-100, 40, -140), end.Position);
            Assert.AreEqual(39f, mid.Fov, 1e-3f, "half way, halfway between 42 and 36");
            Assert.AreEqual(new Vector3(6, -8, -32), mid.LookAt, "a crane that does not re-aim keeps its look-at");
        }
    }

    public sealed class ReplayCompositionTests
    {
        [Test]
        public void AnOfflineRenderDrawsNoHudNoBadgeAndNoReplayTagAndWritesItsOwnAccount()
        {
            var render = ReplayOptions.Parse(new[] { "x", "-sitepulse-render", "shots.json", "-sitepulse-replay", "run-1", "-sitepulse-out", "out" }, out var e);
            Assert.IsNull(e);
            Assert.IsTrue(render.Render);
            var c = ReplayComposition.For(render);
            Assert.IsFalse(c.Badge, "no badge on a rendered frame (D4)");
            Assert.IsFalse(c.Hud);
            Assert.IsFalse(c.ReplayTag, "no replay tag on the cards either");
            Assert.IsFalse(c.ReadsKeyboard);
            Assert.IsTrue(c.WritesRenderJson, "the disclosure is render.json's, for the description and captions");
        }

        [Test]
        public void AnInteractiveReplayAlwaysNamesItselfOnScreen()
        {
            var play = ReplayOptions.Parse(new[] { "x", "-sitepulse-replay", "run-1", "-sitepulse-replay-start", "42.5" }, out var e);
            Assert.IsNull(e);
            Assert.IsFalse(play.Render);
            Assert.AreEqual(42.5, play.StartSeconds);
            var c = ReplayComposition.For(play);
            Assert.IsTrue(c.Badge);
            Assert.IsTrue(c.Hud);
            Assert.IsTrue(c.ReplayTag);
            Assert.IsTrue(c.ReadsKeyboard);
            Assert.IsFalse(c.WritesRenderJson);
            Assert.AreEqual("REPLAY · recorded live run run-20261006T140000Z · 2026-10-06", ReplayRoot.Badge("run-20261006T140000Z", "2026-10-06"));
            StringAssert.StartsWith("REPLAY", SitepulseModes.Badge(SitepulseMode.Replay));
        }

        [Test]
        public void TheReplayFlagsAreRefusedWhenTheyContradictEachOtherOrAreIncomplete()
        {
            void Refuses(string expect, params string[] args)
            {
                Assert.IsNull(ReplayOptions.Parse(args, out var error), string.Join(" ", args));
                StringAssert.Contains(expect, error);
            }

            Refuses("-sitepulse-replay <runId|path> is required", "x", "-sitepulse-replay-start", "3");
            Refuses("needs a value", "x", "-sitepulse-replay");
            Refuses("needs -sitepulse-out", "x", "-sitepulse-render", "s.json", "-sitepulse-replay", "r");
            Refuses("only for -sitepulse-render", "x", "-sitepulse-replay", "r", "-sitepulse-out", "o");
            Refuses("interactive", "x", "-sitepulse-render", "s.json", "-sitepulse-replay", "r", "-sitepulse-out", "o", "-sitepulse-replay-start", "3");
            Refuses("not a number", "x", "-sitepulse-replay", "r", "-sitepulse-replay-start", "soon");
            Assert.IsTrue(ReplayOptions.Wanted(new[] { "x", "-sitepulse-render", "s.json" }));
            Assert.IsFalse(ReplayOptions.Wanted(new[] { "x", "-sitepulse-mode", "live" }));
        }

        [Test]
        public void ARunIdIsLookedForUnderTheRecordingsAndAPathIsTakenAsGiven()
        {
            var byId = ReplayOptions.Parse(new[] { "x", "-sitepulse-replay", "run-1" }, out _);
            Assert.AreEqual(Path.Combine("base", "run-1"), byId.RecordingDirectory("base"));
            var withDir = ReplayOptions.Parse(new[] { "x", "-sitepulse-replay", "run-1", "-sitepulse-record", "elsewhere" }, out _);
            Assert.AreEqual(Path.Combine("elsewhere", "run-1"), withDir.RecordingDirectory("base"));
            var path = Path.Combine(Path.GetTempPath(), "somewhere", "run-9");
            var byPath = ReplayOptions.Parse(new[] { "x", "-sitepulse-replay", path }, out _);
            Assert.AreEqual(Path.GetFullPath(path), byPath.RecordingDirectory("base"));
        }

        [Test]
        public void RenderJsonSaysTheFramesAreAReplayOfARecordedRunAndWhichAndWhen()
        {
            using var run = new SyntheticRun();
            run.Recorder.ClockChanged(1.0);
            run.Drive(0, 4.0);
            run.Recorder.ClockChanged(4.0);
            run.Drive(4.01, 3.0);
            run.Recorder.ClockChanged(1.0);
            run.Drive(7.02, 4.0);
            run.Obs(2.0, SyntheticRun.Alarm(SyntheticRun.TruckToken, "al-1", "low-fuel", "ACTIVE", SyntheticRun.Start.AddSeconds(2)));
            var data = run.Reload();
            var file = ShotFile.Parse(@"{""fps"":30,""shots"":[
                {""name"":""s09"",""startEvent"":{""kind"":""alarm"",""key"":""low-fuel"",""state"":""ACTIVE"",""device"":""SP-HL-0006""},""offset"":-1,""duration"":2,""camera"":{""rig"":""follow"",""target"":""SP-HL-0006""}},
                {""name"":""wide"",""startEvent"":{""kind"":""runStart""},""offset"":5,""duration"":3,""aspect"":""9:16"",""camera"":{""rig"":""fixed"",""pos"":[0,50,0],""lookAt"":[10,0,10]}}]}");
            var plan = ShotPlanner.Plan(file, data);
            var written = new Dictionary<string, int> { ["s09"] = 60, ["wide"] = 90 };
            var json = RenderReport.Build(data, run.Dir, file, plan, written, SyntheticRun.Start.AddDays(1), new BuildInfo { GitSha = "abcdef012345", UnityVersion = "6000.5.3f1" }, null);
            using var doc = JsonDocument.Parse(json);
            var r = doc.RootElement;
            Assert.AreEqual("complete", r.GetProperty("status").GetString());
            Assert.IsTrue(r.GetProperty("isReplay").GetBoolean());
            StringAssert.Contains("replay, not a live capture", r.GetProperty("statement").GetString());
            StringAssert.Contains("No HUD", r.GetProperty("frameOverlay").GetString());
            Assert.AreEqual("run-20261006T140000Z", r.GetProperty("runId").GetString());
            Assert.AreEqual(run.Dir, r.GetProperty("sourceRecording").GetString());
            Assert.AreEqual("2026-10-06", r.GetProperty("recordingDate").GetString());
            Assert.AreEqual(30, r.GetProperty("fps").GetInt32());
            Assert.AreEqual("79bf619b38ea", r.GetProperty("recordingBuild").GetProperty("gitSha").GetString());
            Assert.AreEqual("abcdef012345", r.GetProperty("renderBuild").GetProperty("gitSha").GetString());
            var shots = r.GetProperty("shots").EnumerateArray().ToList();
            Assert.AreEqual(2, shots.Count);
            Assert.AreEqual("s09", shots[0].GetProperty("name").GetString());
            StringAssert.Contains("low-fuel ACTIVE on SP-HL-0006", shots[0].GetProperty("startEvent").GetProperty("found").GetString());
            Assert.AreEqual(2.0, shots[0].GetProperty("startEvent").GetProperty("atRunSeconds").GetDouble(), 1e-6);
            Assert.AreEqual(1.0, shots[0].GetProperty("startRunSeconds").GetDouble(), 1e-6);
            Assert.AreEqual(60, shots[0].GetProperty("frames").GetInt32(), "2 s at 30 fps");
            Assert.AreEqual(60, shots[0].GetProperty("framesWritten").GetInt32());
            Assert.IsFalse(shots[0].GetProperty("acceleratedClock").GetBoolean());
            Assert.IsFalse(shots[0].TryGetProperty("captionRequired", out _));
            Assert.AreEqual(1080, shots[1].GetProperty("width").GetInt32());
            Assert.AreEqual(1920, shots[1].GetProperty("height").GetInt32());
            Assert.IsTrue(shots[1].GetProperty("acceleratedClock").GetBoolean(), "5 s to 8 s reaches into the stretch the scene ran fast");
            Assert.AreEqual("Accelerated simulation clock", shots[1].GetProperty("captionRequired").GetString());

            var failed = RenderReport.Build(data, run.Dir, file, plan, written, SyntheticRun.Start, BuildInfo.Unknown(), "a frame cannot be written");
            using var doc2 = JsonDocument.Parse(failed);
            Assert.AreEqual("failed", doc2.RootElement.GetProperty("status").GetString());
            Assert.AreEqual("a frame cannot be written", doc2.RootElement.GetProperty("error").GetString());
        }

        const string Shots = @"{""fps"":30,""shots"":[
            {""name"":""before"",""startEvent"":{""kind"":""runStart""},""offset"":0,""duration"":3.5,""camera"":{""rig"":""fixed"",""pos"":[0,50,0],""lookAt"":[10,0,10]}},
            {""name"":""cross"",""startEvent"":{""kind"":""runStart""},""offset"":3,""duration"":2,""camera"":{""rig"":""fixed"",""pos"":[0,50,0],""lookAt"":[10,0,10]}},
            {""name"":""after"",""startEvent"":{""kind"":""runStart""},""offset"":8,""duration"":2,""camera"":{""rig"":""fixed"",""pos"":[0,50,0],""lookAt"":[10,0,10]}}]}";

        static List<JsonElement> ShotsOf(JsonDocument doc) => doc.RootElement.GetProperty("shots").EnumerateArray().ToList();

        static JsonDocument Report(RecordingData data, string dir)
        {
            var file = ShotFile.Parse(Shots);
            return JsonDocument.Parse(RenderReport.Build(data, dir, file, ShotPlanner.Plan(file, data), null, SyntheticRun.Start, BuildInfo.Unknown(), null));
        }

        [Test]
        public void AShotThatStartsInRealTimeAndRunsIntoTheFastStretchNeedsTheCaption()
        {
            using var run = new SyntheticRun();
            run.Recorder.ClockChanged(1.0);
            run.Drive(0, 4.0);
            run.Recorder.ClockChanged(4.0);          // fast from 4.0 s
            run.Drive(4.01, 3.0);
            run.Recorder.ClockChanged(1.0);          // real again from 7.01 s
            run.Drive(7.02, 4.0);
            var data = run.Reload();
            using var doc = Report(data, run.Dir);
            var shots = ShotsOf(doc);
            Assert.AreEqual("recorded", doc.RootElement.GetProperty("clock").GetString());
            Assert.IsFalse(shots[0].GetProperty("acceleratedClock").GetBoolean(), "0 to 3.5 s is all real time");
            Assert.IsFalse(shots[0].TryGetProperty("captionRequired", out _));
            Assert.IsTrue(shots[1].GetProperty("acceleratedClock").GetBoolean(), "3 to 5 s begins in real time and reaches the stretch the scene ran fast");
            Assert.AreEqual("Accelerated simulation clock", shots[1].GetProperty("captionRequired").GetString());
            Assert.IsFalse(shots[2].GetProperty("acceleratedClock").GetBoolean(), "8 to 10 s is real again");
        }

        [Test]
        public void ARunKilledAfterTheClockSpedUpStillRendersWithTheCaption()
        {
            using var run = new SyntheticRun();
            run.T = 0.0; run.Recorder.ClockChanged(1.0);
            run.Drive(0, 11.0);
            run.T = 2.0; run.Recorder.ClockChanged(8.0);        // fast from 2 s, and the player never closed the recording
            var data = RecordingData.Load(run.CopyAsKilled());
            Assert.IsFalse(data.Header.EndedCleanly);
            using var doc = Report(data, run.Dir);
            var shots = ShotsOf(doc);
            Assert.AreEqual("reconstructed", doc.RootElement.GetProperty("clock").GetString());
            Assert.IsTrue(shots[0].GetProperty("acceleratedClock").GetBoolean(), "0 to 3.5 s of a clock that sped up at 2 s");
            Assert.AreEqual("Accelerated simulation clock", shots[0].GetProperty("captionRequired").GetString());
            Assert.IsTrue(shots[1].GetProperty("acceleratedClock").GetBoolean());
            Assert.IsTrue(shots[2].GetProperty("acceleratedClock").GetBoolean());
        }

        [Test]
        public void ARunKilledWithNoTraceOfItsClockRendersWithTheCaptionAndSaysTheClockIsUnknown()
        {
            using var run = new SyntheticRun();
            run.Drive(0, 11.0);
            var data = RecordingData.Load(run.CopyAsKilled());
            using var doc = Report(data, run.Dir);
            Assert.AreEqual("unknown", doc.RootElement.GetProperty("clock").GetString());
            foreach (var shot in ShotsOf(doc))
            {
                Assert.IsTrue(shot.GetProperty("acceleratedClock").GetBoolean(), "an unknown clock is not assumed to be real time");
                Assert.AreEqual("Accelerated simulation clock", shot.GetProperty("captionRequired").GetString());
            }
        }

        [Test]
        public void TheRootHandsTheOverlayTheReplaysSourceTheRecordingsClockAndTheRightReplayTag()
        {
            using var run = new SyntheticRun();
            run.Drive(0, 6.0);
            var data = run.Reload();
            var go = new GameObject("overlay") { hideFlags = HideFlags.HideAndDontSave };
            try
            {
                var overlay = go.AddComponent<IotOverlay>();
                var session = new ReplaySession(data);
                var render = ReplayComposition.For(ReplayOptions.Parse(new[] { "x", "-sitepulse-render", "s.json", "-sitepulse-replay", "r", "-sitepulse-out", "o" }, out _));
                var play = ReplayComposition.For(ReplayOptions.Parse(new[] { "x", "-sitepulse-replay", "r" }, out _));

                overlay.ReplayTag = true;
                var source = ReplayRoot.Wire(overlay, session, render, null);
                Assert.AreSame(source, overlay.Source);
                Assert.AreEqual(Provenance.Replayed, overlay.Source.Provenance);
                session.Seek(3.0);
                Assert.AreEqual(session.WallClock, overlay.Clock(), "cards age against the recording's moment, not today");
                Assert.AreEqual(SyntheticRun.Start.AddSeconds(3.0), overlay.Clock());
                session.Seek(4.5);
                Assert.AreEqual(SyntheticRun.Start.AddSeconds(4.5), overlay.Clock(), "and the clock follows the cursor");
                Assert.IsFalse(overlay.ReplayTag, "a render says nothing on the frame");

                overlay.ReplayTag = false;
                ReplayRoot.Wire(overlay, session, play, null);
                Assert.IsTrue(overlay.ReplayTag, "an interactive replay names itself on every card");
            }
            finally
            {
                UnityEngine.Object.DestroyImmediate(go);
            }
        }
    }

    public sealed class ReplayEffectsTests
    {
        static Vector3[] Roll(ParticleSystem ps, int steps)
        {
            QuarryEffects.Rate(ps, 200f);
            for (var i = 0; i < steps; i++) QuarryEffects.Advance(ps, 0.05f, true);
            var buffer = new ParticleSystem.Particle[ps.particleCount];
            var n = ps.GetParticles(buffer);
            return buffer.Take(n).Select(p => p.position).ToArray();
        }

        [Test]
        public void ASystemThatIsRestartedRollsTheSameDiceWhateverItDidBefore()
        {
            var go = new GameObject("effects-holder") { hideFlags = HideFlags.HideAndDontSave };
            try
            {
                var ps = QuarryEffects.Emitter(go.transform, "dust", Vector3.zero, null, 500);
                var first = Roll(ps, 12);
                Assert.Greater(first.Length, 5, "something was emitted to compare");
                Roll(ps, 40);                                     // a different history: the random sequence has moved on
                QuarryEffects.Restart(ps);
                Assert.AreEqual(0, ps.particleCount, "restarting empties it");
                Assert.AreEqual(QuarryEffects.SeedFor("effects-holder", "dust"), ps.randomSeed, "and puts it back on the seed it was made with");
                var second = Roll(ps, 12);
                Assert.AreEqual(first.Length, second.Length);
                for (var i = 0; i < first.Length; i++)
                    Assert.AreEqual(first[i].x, second[i].x, 1e-5f, "particle " + i + ": a shot rendered after others looks like one rendered alone");
            }
            finally
            {
                UnityEngine.Object.DestroyImmediate(go);
            }
        }
    }
}
