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

        [TearDown]
        public void Clean()
        {
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

        LiveRecorder Start(LiveRecorderWorld world, bool enabled = true)
        {
            var options = RecordOptions.Parse(enabled ? new[] { "x", "-sitepulse-record", baseDir } : new[] { "x", "-sitepulse-no-record" }, out var error);
            Assert.IsNull(error);
            world.BuildInfoPath = Path.Combine(baseDir, "none.json");
            return LiveRecorder.TryStart(options, world, log.Add, () => now);
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
    }

    public sealed class ObserverTapTests
    {
        static readonly DateTimeOffset T0 = SyntheticRun.Start;

        static PlatformObserver Observer(out ObservedState state)
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
