// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Threading.Tasks;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Recording;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using DeviceChain.Sitepulse.Visuals;
using NUnit.Framework;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>The live recorder against a scene's own parts (rigs it makes itself) and a clock the test moves.</summary>
    public sealed class LiveRecorderTests
    {
        readonly string baseDir = Path.Combine(Path.GetTempPath(), "sitepulse-a7-live-" + Guid.NewGuid().ToString("N").Substring(0, 12));
        readonly List<GameObject> made = new List<GameObject>();
        readonly List<string> log = new List<string>();
        double now;

        readonly List<LiveRecorder> started = new List<LiveRecorder>();

        [TearDown]
        public void Clean()
        {
            // a test that failed before it closed its recording must not leave the files held, or the next test finds a run in the way
            foreach (var r in started)
            {
                try { r.Dispose(); } catch (Exception) { /* a recording broken on purpose */ }
            }

            started.Clear();
            log.Clear();
            now = 0;
            foreach (var go in made) UnityEngine.Object.DestroyImmediate(go);
            made.Clear();
            try { if (Directory.Exists(baseDir)) Directory.Delete(baseDir, true); } catch (IOException) { }
        }

        MachineRig Rig(string id, MachineKind kind, Vector3 at, Vector3 euler)
        {
            var go = new GameObject(id) { hideFlags = HideFlags.HideAndDontSave };
            made.Add(go);
            go.transform.SetPositionAndRotation(at, Quaternion.Euler(euler));
            var rig = go.AddComponent<MachineRig>();
            rig.Bind(kind);
            return rig;
        }

        LiveRecorder Start(LiveRecorderWorld world, bool enabled = true, Func<string, Stream> openStream = null)
        {
            var options = RecordOptions.Parse(enabled ? new[] { "x", "-sitepulse-record", baseDir } : new[] { "x", "-sitepulse-no-record" }, out var error);
            Assert.IsNull(error);
            world.BuildInfoPath = Path.Combine(baseDir, "none.json");
            var rec = LiveRecorder.TryStart(options, world, log.Add, () => now, openStream);
            if (rec != null) started.Add(rec);
            return rec;
        }

        [Test]
        public void ItSamplesEveryRigAtTwentyHertzFromWhatWasDrawnAndSaysWhatItIsDoing()
        {
            var truck = Rig("SP-HL-0006", MachineKind.Hauler, new Vector3(100, 12.5f, 10), new Vector3(2f, 90f, -1f));
            truck.dump = 12f; truck.steer = 5f; truck.loaded = true;
            var dozer = Rig("SP-DZ-0001", MachineKind.Dozer, new Vector3(-20, 3, 4), new Vector3(0f, 270f, 0f));
            dozer.bladeArm = -10f; dozer.ripper = -20f;
            var rec = Start(new LiveRecorderWorld
            {
                Machines = () => new[] { truck, dozer },
                TimeScale = () => 1.0,
                IsOnTrack = id => id == "SP-DZ-0001",
                Devices = new[] { new RunDevice { Id = "SP-HL-0006", Token = "sp-hauler-06", Kind = "Hauler" } },
                Tenant = "sitepulse-tenant", Instance = "sitepulse", Manifest = "test",
            });
            Assert.IsNotNull(rec, string.Join("; ", log));
            now = 0.00; rec.Tick();                       // a frame is due at once
            now = 0.02; rec.Tick();                       // not yet
            now = 0.05; rec.Tick();                       // due
            truck.transform.position += new Vector3(5f, 0f, 0f);   // five metres along its heading (east)
            now = 0.08; rec.Tick();                       // not yet
            now = 0.11; rec.Tick();                       // due
            rec.Dispose();

            var data = RecordingData.Load(rec.Directory);
            Assert.AreEqual(3, data.Sim.FrameCount, "frames at 0.00, 0.05 and 0.11 s; the ticks between were not due");
            Assert.AreEqual(0.0, data.Sim.TimeOf(0));
            Assert.AreEqual(0.05, data.Sim.TimeOf(1), 1e-12);
            Assert.AreEqual(0.11, data.Sim.TimeOf(2), 1e-12, "a frame carries the time it was taken, not the time it was due");
            Assert.IsTrue(data.Sim.TryIndexOf("SP-HL-0006", out var t));
            Assert.IsTrue(data.Sim.TryIndexOf("SP-DZ-0001", out var d));
            var a = data.Sim.Frame(0, t);
            Assert.AreEqual(100f, a.X, 1e-4f); Assert.AreEqual(12.5f, a.Y, 1e-4f); Assert.AreEqual(10f, a.Z, 1e-4f);
            Assert.AreEqual(90f, a.Heading, 1e-3f); Assert.AreEqual(2f, a.Pitch, 1e-3f); Assert.AreEqual(359f, a.Roll, 1e-3f, "Unity's own euler angle: 0 to 360, and the same attitude");
            Assert.AreEqual(12f, a.P1); Assert.AreEqual(5f, a.Steer); Assert.IsTrue(a.Loaded);
            Assert.IsFalse(a.OnTrack);
            Assert.IsTrue(float.IsNaN(a.FuelPct), "a machine with no session has no model to read, and says NaN rather than zero");
            Assert.AreEqual(0f, a.Travel);
            Assert.AreEqual(0f, data.Sim.Frame(1, t).Travel);
            Assert.AreEqual(5f, data.Sim.Frame(2, t).Travel, 1e-3f, "five metres forward is +5 on the wheels");
            var z = data.Sim.Frame(0, d);
            Assert.AreEqual(-10f, z.P1); Assert.AreEqual(-20f, z.P2); Assert.IsTrue(z.OnTrack);
            Assert.AreEqual(270f, z.Heading, 1e-3f);
            Assert.AreEqual("unknown", data.Header.Build.GitSha, "a build that cannot say what it was built from says unknown");
            Assert.AreEqual("sp-hauler-06", data.Header.TokenOf("SP-HL-0006"));
            Assert.AreEqual(ClockSegment.Real, data.Header.Clock.Single().Mode);
            Assert.IsTrue(log.Any(l => l.StartsWith("recording closed")), string.Join("; ", log));
        }

        [Test]
        public void ItWritesTheTimelineAndTheBrokersAcknowledgedSamplesAndStopsListeningWhenClosed()
        {
            var truck = Rig("SP-HL-0006", MachineKind.Hauler, Vector3.zero, Vector3.zero);
            var timeline = new Timeline();
            var rec = Start(new LiveRecorderWorld { Machines = () => new[] { truck }, TimeScale = () => 1.0, IsOnTrack = _ => true, Timeline = timeline, Devices = new RunDevice[0] });
            Assert.IsNotNull(rec, string.Join("; ", log));
            now = 1.0;
            timeline.Add("SP-HL-0006", TimelineKinds.Received, "goto-refuel c1");
            now = 1.5;
            rec.Record("SP-HL-0006", "sp-hauler-06", Sample.Measurement(1, SyntheticRun.Start.AddSeconds(1.4), new Dictionary<string, double> { ["fuel_pct"] = 14.5 }));
            rec.Tick();
            rec.Dispose();
            timeline.Add("SP-HL-0006", TimelineKinds.Outcome, "after the run");   // nobody is listening any more

            var data = RecordingData.Load(rec.Directory);
            var rows = data.Device.Where(l => l.K == DeviceKinds.Timeline).ToList();
            Assert.AreEqual(1, rows.Count);
            Assert.AreEqual("received", rows[0].RowKind);
            Assert.AreEqual("goto-refuel c1", rows[0].Text);
            Assert.AreEqual(1.0, rows[0].T, 1e-9);
            var sample = data.Device.Single(l => l.K == DeviceKinds.Sample);
            Assert.AreEqual(14.5, sample.Values["fuel_pct"]);
            Assert.AreEqual(1.5, sample.T, 1e-9);
            Assert.IsNotNull(sample.AckedAt, "the sample says when the broker took it");
            Assert.GreaterOrEqual(sample.AckedAt.Value, data.Header.StartedAtUtc);
            Assert.AreEqual(SyntheticRun.Start.AddSeconds(1.4), sample.OccurredAt, "and its own time is the device's, untouched");
        }

        [Test]
        public void ARecorderThatIsOffOrHasNothingToRecordStartsNothingAndLeavesNothingOnDisk()
        {
            var truck = Rig("SP-HL-0006", MachineKind.Hauler, Vector3.zero, Vector3.zero);
            Assert.IsNull(Start(new LiveRecorderWorld { Machines = () => new[] { truck }, TimeScale = () => 1.0, IsOnTrack = _ => true }, enabled: false));
            Assert.IsFalse(Directory.Exists(baseDir));
            Assert.IsNull(Start(new LiveRecorderWorld { Machines = () => new MachineRig[0], TimeScale = () => 1.0, IsOnTrack = _ => true }));
            Assert.IsTrue(log.Any(l => l.Contains("no machines")), string.Join("; ", log));
            Assert.IsFalse(Directory.Exists(baseDir) && Directory.GetDirectories(baseDir).Length > 0, "no run directory for a recording that did not start");
        }

        [Test]
        public void ASceneClockThatIsSpedUpIsRecordedAsAcceleratedFromWhenItChanged()
        {
            var truck = Rig("SP-HL-0006", MachineKind.Hauler, Vector3.zero, Vector3.zero);
            var scale = 1.0;
            var rec = Start(new LiveRecorderWorld { Machines = () => new[] { truck }, TimeScale = () => scale, IsOnTrack = _ => true, Devices = new RunDevice[0] });
            now = 0.0; rec.Tick();
            now = 10.0; scale = 8.0; rec.Tick();
            now = 20.0; scale = 1.0; rec.Tick();
            rec.Dispose();
            var h = RecordingData.Load(rec.Directory).Header;
            CollectionAssert.AreEqual(new[] { ClockSegment.Real, ClockSegment.Accelerated, ClockSegment.Real }, h.Clock.Select(c => c.Mode).ToList());
            Assert.AreEqual(8.0, h.Clock[1].Scale);
            Assert.IsTrue(h.AnyAccelerated(12, 15));
            Assert.IsFalse(h.AnyAccelerated(0, 9));
        }

        [Test]
        public void ARigThatIsGoneDoesNotStopTheOtherMachinesBeingRecorded()
        {
            var truck = Rig("SP-HL-0006", MachineKind.Hauler, new Vector3(100, 12.5f, 10), new Vector3(0f, 90f, 0f));
            var dozer = Rig("SP-DZ-0001", MachineKind.Dozer, new Vector3(-20, 3, 4), new Vector3(0f, 270f, 0f));
            var rec = Start(new LiveRecorderWorld { Machines = () => new[] { truck, dozer }, TimeScale = () => 1.0, IsOnTrack = _ => true, Devices = new RunDevice[0] });
            Assert.IsNotNull(rec, string.Join("; ", log));
            now = 0.00; rec.Tick();
            UnityEngine.Object.DestroyImmediate(dozer.gameObject);               // the scene lost a machine
            truck.transform.position += new Vector3(5f, 0f, 0f);
            now = 0.05; rec.Tick();
            truck.transform.position += new Vector3(5f, 0f, 0f);
            now = 0.11; rec.Tick();
            now = 0.17; rec.Tick();
            rec.Dispose();

            var data = RecordingData.Load(rec.Directory);
            Assert.AreEqual(4, data.Sim.FrameCount, "the truck is still recorded every frame");
            Assert.IsTrue(data.Sim.TryIndexOf("SP-HL-0006", out var t));
            Assert.IsTrue(data.Sim.TryIndexOf("SP-DZ-0001", out var d));
            Assert.AreEqual(100f, data.Sim.Frame(0, t).X, 1e-4f);
            Assert.AreEqual(110f, data.Sim.Frame(2, t).X, 1e-4f);
            Assert.IsFalse(data.Sim.Frame(0, d).Missing);
            for (var f = 1; f < 4; f++)
            {
                var z = data.Sim.Frame(f, d);
                Assert.IsTrue(z.Missing, "frame " + f + ": marked missing");
                Assert.AreEqual(-20f, z.X, 1e-4f, "and left where it was last seen");
                Assert.IsFalse(data.Sim.Frame(f, t).Missing);
            }

            Assert.AreEqual(1, log.Count(l => l.Contains("SP-DZ-0001") && l.Contains("gone")), "said once, not every frame: " + string.Join("; ", log));
            Assert.IsTrue(data.Header.EndedCleanly, "a machine going is not a recording failing");
        }

        [Test]
        public void ARigThatWasNeverSeenIsMarkedMissingWithNoPoseRatherThanAZeroOne()
        {
            var truck = Rig("SP-HL-0006", MachineKind.Hauler, new Vector3(100, 12.5f, 10), Vector3.zero);
            var dozer = Rig("SP-DZ-0001", MachineKind.Dozer, new Vector3(-20, 3, 4), Vector3.zero);
            var rec = Start(new LiveRecorderWorld { Machines = () => new[] { truck, dozer }, TimeScale = () => 1.0, IsOnTrack = _ => true, Devices = new RunDevice[0] });
            UnityEngine.Object.DestroyImmediate(dozer.gameObject);
            now = 0.0; rec.Tick();
            rec.Dispose();
            var data = RecordingData.Load(rec.Directory);
            data.Sim.TryIndexOf("SP-DZ-0001", out var d);
            var z = data.Sim.Frame(0, d);
            Assert.IsTrue(z.Missing);
            Assert.IsTrue(float.IsNaN(z.X) && float.IsNaN(z.Y) && float.IsNaN(z.Z), "not (0, 0, 0): that is a place");
            data.Sim.TryIndexOf("SP-HL-0006", out var t);
            Assert.AreEqual(100f, data.Sim.Frame(0, t).X, 1e-4f);
        }

        [Test]
        public void ThePlatformObserversItemsReachObservedNdjsonThroughTheRealTap()
        {
            var truck = Rig("SP-HL-0006", MachineKind.Hauler, Vector3.zero, Vector3.zero);
            var observer = ObserverTapTests.Observer(out _);
            var rec = Start(new LiveRecorderWorld { Machines = () => new[] { truck }, TimeScale = () => 1.0, IsOnTrack = _ => true, Observer = observer, Devices = new RunDevice[0] });
            Assert.IsNotNull(rec, string.Join("; ", log));
            now = 1.25;
            observer.Inbox.Post(new MeasurementItem("sp-hauler-06", "fuel_pct", 41.5, SyntheticRun.Start.AddSeconds(1.2), SyntheticRun.Start.AddSeconds(1.25), false));
            observer.Pump();
            now = 2.0;
            rec.Tick();
            rec.Dispose();
            Assert.IsNull(observer.OnItem, "closing the recording lets go of the observer");

            var data = RecordingData.Load(rec.Directory);
            Assert.AreEqual(1, data.Observed.Count);
            Assert.AreEqual("fuel_pct", data.Observed[0].Name);
            Assert.AreEqual(41.5, data.Observed[0].Value);
            Assert.AreEqual(1.25, data.Observed[0].T, 1e-9);
            Assert.AreEqual("sp-hauler-06", data.Observed[0].Device);
        }

        [Test]
        public void ATapThatFailsFaultsTheRecordingLoudlyAndTheObserverCarriesOn()
        {
            var truck = Rig("SP-HL-0006", MachineKind.Hauler, Vector3.zero, Vector3.zero);
            var observer = ObserverTapTests.Observer(out var state);
            var disk = new FlakyDisk();
            var rec = Start(new LiveRecorderWorld { Machines = () => new[] { truck }, TimeScale = () => 1.0, IsOnTrack = _ => true, Observer = observer, Devices = new RunDevice[0] }, openStream: disk.Open);
            Assert.IsNotNull(rec, string.Join("; ", log));
            disk.Failure = new InvalidOperationException("the writer broke");        // not a disk error: the kind the recorder does not expect
            observer.Inbox.Post(new MeasurementItem("sp-hauler-06", "fuel_pct", 40, SyntheticRun.Start, SyntheticRun.Start, false));
            observer.Inbox.Post(new MeasurementItem("sp-hauler-06", "payload_t", 80, SyntheticRun.Start, SyntheticRun.Start, false));
            Assert.DoesNotThrow(() => observer.Pump());
            Assert.IsNull(observer.OnItem, "the observer removed the tap");
            Assert.IsTrue(state.TryGet("sp-hauler-06", out var device));
            Assert.AreEqual(2, device.Measurements.Count, "and applied both items");
            Assert.IsTrue(rec.Recorder.IsFaulted, "the recording knows it stopped");
            Assert.IsTrue(log.Any(l => l.StartsWith("recording STOPPED") && l.Contains("the writer broke")), string.Join("; ", log));
            disk.Failure = null;
            rec.Dispose();
            Assert.IsFalse(RunHeader.Parse(File.ReadAllText(Path.Combine(rec.Directory, RecordingFiles.RunJson))).EndedCleanly, "run.json says the run did not end cleanly");
        }

        LiveRecorder StartFailing(LiveRecorderWorld world, FlakyDisk disk)
        {
            world.Machines = () => new[] { Rig("SP-HL-0006", MachineKind.Hauler, Vector3.zero, Vector3.zero) };
            world.TimeScale = () => 1.0;
            world.IsOnTrack = _ => true;
            world.Devices = new RunDevice[0];
            var rec = Start(world, openStream: disk.Open);
            Assert.IsNotNull(rec, string.Join("; ", log));
            disk.Failure = new InvalidOperationException("the writer broke");        // not a disk error: the kind the recorder does not expect
            return rec;
        }

        void AssertStoppedOnceAndSaidSo(LiveRecorder rec, FlakyDisk disk)
        {
            Assert.IsTrue(rec.Recorder.IsFaulted);
            Assert.AreEqual(1, log.Count(l => l.StartsWith("recording STOPPED") && l.Contains("the writer broke")), string.Join("; ", log));
            disk.Failure = null;
            rec.Dispose();
            Assert.IsFalse(RunHeader.Parse(File.ReadAllText(Path.Combine(rec.Directory, RecordingFiles.RunJson))).EndedCleanly);
        }

        [Test]
        public void ARecorderThatFailsNeverRaisesIntoTheTimeline()
        {
            var timeline = new Timeline();
            var disk = new FlakyDisk();
            var rec = StartFailing(new LiveRecorderWorld { Timeline = timeline }, disk);
            Assert.DoesNotThrow(() => timeline.Add("SP-HL-0006", TimelineKinds.Received, "goto-refuel c1"), "the task layer is not told the recorder failed");
            Assert.AreEqual(1, timeline.Rows("SP-HL-0006").Count, "and the row is in the timeline all the same");
            Assert.AreEqual(1, timeline.Version);
            AssertStoppedOnceAndSaidSo(rec, disk);
        }

        [Test]
        public void ARecorderThatFailsNeverRaisesIntoThePresenter()
        {
            var timeline = new Timeline();
            var body = new FakeBody("SP-HL-0006", EquipmentKind.Hauler, 0, 0);
            var model = new MachineModel(EquipmentKind.Hauler, "SP-HL-0006");
            var director = new TaskDirector(CommandKit.Site, CommandKit.Graph, timeline, new[] { ((IMachineBody)body, model) }, 1);
            var presenter = new PresenterControls(director, id => model, () => null);
            var disk = new FlakyDisk();
            var rec = StartFailing(new LiveRecorderWorld { Presenter = presenter }, disk);
            Assert.DoesNotThrow(() => presenter.BeginFresh(System.Threading.CancellationToken.None).GetAwaiter().GetResult());
            StringAssert.Contains("not available", presenter.Message, "the presenter's action completed and said what it said");
            Assert.AreEqual(1, presenter.Version, "and finished: its own bookkeeping after the event ran");
            AssertStoppedOnceAndSaidSo(rec, disk);
        }

        [Test]
        public void ARecorderThatFailsNeverRaisesIntoTheSampleSink()
        {
            var disk = new FlakyDisk();
            var rec = StartFailing(new LiveRecorderWorld(), disk);
            Assert.DoesNotThrow(() => rec.Record("SP-HL-0006", "sp-hauler-06", Sample.Measurement(1, SyntheticRun.Start, new Dictionary<string, double> { ["fuel_pct"] = 1 })));
            AssertStoppedOnceAndSaidSo(rec, disk);
        }
    }

    public sealed class ObserverTapTests
    {
        static readonly DateTimeOffset T0 = SyntheticRun.Start;

        internal static PlatformObserver Observer(out ObservedState state)
        {
            state = new ObservedState(T0);
            var broker = new TokenBroker(new RunnerConfig { Token = PlatformTestData.JwtExp(DateTimeOffset.UtcNow.AddHours(1).ToUnixTimeSeconds()) },
                _ => Task.FromResult(Parsed<RunnerConfig>.Fail("not in this test")));
            return new PlatformObserver(new RunnerConfig(), broker, new[] { "sp-hauler-06" }, state, new ObserverStatus());
        }

        [Test]
        public void TheTapSeesEachItemJustBeforeItIsAppliedAndInTheOrderItIsApplied()
        {
            var observer = Observer(out var state);
            var order = new List<string>();
            var heldWhenSeen = new List<bool>();
            observer.OnItem = item =>
            {
                var m = (MeasurementItem)item;
                order.Add(m.Name);
                heldWhenSeen.Add(state.TryGet(m.DeviceToken, out var d) && d.Measurements.ContainsKey(m.Name));
            };
            observer.Inbox.Post(new MeasurementItem("sp-hauler-06", "fuel_pct", 40, T0, T0, false));
            observer.Inbox.Post(new MeasurementItem("sp-hauler-06", "payload_t", 80, T0, T0, false));
            observer.Pump();
            CollectionAssert.AreEqual(new[] { "fuel_pct", "payload_t" }, order);
            CollectionAssert.AreEqual(new[] { false, false }, heldWhenSeen, "the viewer's first sight of an item is its effect: the tap is before it");
            Assert.IsTrue(state.TryGet("sp-hauler-06", out var device));
            Assert.AreEqual(2, device.Measurements.Count, "and the item is applied all the same");
        }

        [Test]
        public void ATapThatThrowsIsRemovedAndTheObserverCarriesOn()
        {
            var observer = Observer(out var state);
            observer.OnItem = _ => throw new InvalidOperationException("the recorder broke");
            observer.Inbox.Post(new MeasurementItem("sp-hauler-06", "fuel_pct", 40, T0, T0, false));
            observer.Inbox.Post(new MeasurementItem("sp-hauler-06", "payload_t", 80, T0, T0, false));
            Assert.DoesNotThrow(() => observer.Pump());
            Assert.IsNull(observer.OnItem);
            Assert.IsTrue(state.TryGet("sp-hauler-06", out var device));
            Assert.AreEqual(2, device.Measurements.Count, "both items applied: watching never stops the observer");
        }
    }
}
