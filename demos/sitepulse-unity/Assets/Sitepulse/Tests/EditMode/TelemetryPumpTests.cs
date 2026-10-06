// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Simulation;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    public sealed class TelemetryPumpTests
    {
        static readonly DateTimeOffset T0 = new DateTimeOffset(2026, 10, 5, 12, 0, 0, TimeSpan.Zero);

        // run a device for `seconds` at 100 ms ticks; a machine moving east at 5 m/s, or parked
        static List<(DateTimeOffset at, Sample s)> Run(EquipmentKind kind, bool moving, double seconds, double warmup = 3.0)
        {
            var model = new MachineModel(kind, "SP-T-0001");
            var motion = new MotionEstimator();
            var sched = new TelemetryScheduler(kind);
            double east = 0;
            motion.Update(east, 0, 0.1, 0);
            for (var i = 0; i < warmup * 10; i++)
            {
                if (moving) east += 0.5;
                motion.Update(east, 0, 0.1, 0);
            }

            var all = new List<(DateTimeOffset, Sample)>();
            var batch = new List<Sample>();
            for (var i = 0; i < (int)Math.Round(seconds * 10); i++)
            {
                var now = T0 + TimeSpan.FromMilliseconds(100 * i);
                if (moving) east += 0.5;
                motion.Update(east, 0, 0.1, 0);
                model.Step(0.1, new MachineInput(motion.SpeedMps, false));
                batch.Clear();
                sched.Tick(now, model, kind == EquipmentKind.Plant ? null : motion, east, 0, 10, batch);
                foreach (var s in batch) all.Add((now, s));
            }

            return all;
        }

        static int Count(List<(DateTimeOffset at, Sample s)> l, SampleKind k) => l.Count(x => x.s.Kind == k);

        // ---- cadence

        [Test]
        public void AMovingMachineSendsOneMeasurementASecondAndTwoLocationsASecond()
        {
            var l = Run(EquipmentKind.Hauler, moving: true, seconds: 60);
            Assert.AreEqual(60, Count(l, SampleKind.Measurement));
            Assert.AreEqual(120, Count(l, SampleKind.Location));
        }

        [Test]
        public void AStoppedMachineSendsALocationEveryTwoSeconds()
        {
            var l = Run(EquipmentKind.Loader, moving: false, seconds: 60);
            Assert.AreEqual(60, Count(l, SampleKind.Measurement));
            Assert.AreEqual(30, Count(l, SampleKind.Location));
        }

        [Test]
        public void ThePlantSendsMeasurementsAndNoLocation()
        {
            var l = Run(EquipmentKind.Plant, moving: false, seconds: 60);
            Assert.AreEqual(60, Count(l, SampleKind.Measurement));
            Assert.AreEqual(0, Count(l, SampleKind.Location));
        }

        [Test]
        public void EverySampleCarriesTheTimeItWasProducedAndOneMeasurementCarriesAllItsKeys()
        {
            var l = Run(EquipmentKind.Hauler, moving: true, seconds: 5);
            foreach (var (at, s) in l) Assert.AreEqual(at, s.OccurredUtc);
            var m = l.First(x => x.s.Kind == SampleKind.Measurement).s;
            CollectionAssert.AreEquivalent(
                new[] { MeasurementKeys.FuelPct, MeasurementKeys.EngineTempC, MeasurementKeys.EngineHours, MeasurementKeys.PayloadT, MeasurementKeys.TyrePressureKpa },
                m.Values.Keys);
            var loc = l.First(x => x.s.Kind == SampleKind.Location).s;
            Assert.AreEqual(SiteDefinition.EllipsoidHeight(10), loc.Elevation, 1e-9);
            Assert.AreEqual(90.0, loc.HeadingDegrees, 0.5);
            Assert.AreEqual(5.0, loc.SpeedMps, 0.5);
            Assert.GreaterOrEqual(loc.SpeedMps, 0.0);
            // strictly increasing sequence numbers
            var seq = l.Select(x => x.s.Sequence).ToList();
            CollectionAssert.AreEqual(seq.OrderBy(x => x).ToList(), seq);
            Assert.AreEqual(seq.Count, seq.Distinct().Count());
        }

        [Test]
        public void AMachineThatStartsMovingDoesNotWaitOutTheStoppedGap()
        {
            var sched = new TelemetryScheduler(EquipmentKind.Hauler);
            var model = new MachineModel(EquipmentKind.Hauler, "SP-T-0002");
            var motion = new MotionEstimator();
            motion.Update(0, 0, 0.1, 0);
            var batch = new List<Sample>();
            sched.Tick(T0, model, motion, 0, 0, 0, batch);                // t=0 location, next due at +2 s
            batch.Clear();
            double east = 0;
            DateTimeOffset firstMovingFix = default;
            for (var i = 1; i <= 15; i++)
            {
                var now = T0 + TimeSpan.FromMilliseconds(100 * i);
                east += 0.8;                                                // 8 m/s from t=0.1
                motion.Update(east, 0, 0.1, 0);
                batch.Clear();
                sched.Tick(now, model, motion, east, 0, 0, batch);
                if (firstMovingFix == default && batch.Any(s => s.Kind == SampleKind.Location)) firstMovingFix = now;
            }

            Assert.AreNotEqual(default(DateTimeOffset), firstMovingFix);
            Assert.Less((firstMovingFix - T0).TotalSeconds, 1.0, "the first moving fix came well before the stopped cadence's 2 s");
        }

        [Test]
        public void AStallResumesAtTheNormalRateNotInABurst()
        {
            var sched = new TelemetryScheduler(EquipmentKind.Hauler);
            var model = new MachineModel(EquipmentKind.Hauler, "SP-T-0003");
            var motion = new MotionEstimator();
            motion.Update(0, 0, 0.1, 0);
            var batch = new List<Sample>();
            sched.Tick(T0, model, motion, 0, 0, 0, batch);
            var stalled = T0 + TimeSpan.FromSeconds(120);
            var after = new List<(DateTimeOffset, Sample)>();
            for (var i = 0; i < 20; i++)
            {
                batch.Clear();
                var now = stalled + TimeSpan.FromMilliseconds(100 * i);
                sched.Tick(now, model, motion, 0, 0, 0, batch);
                foreach (var s in batch) after.Add((now, s));
            }

            Assert.LessOrEqual(Count(after, SampleKind.Measurement), 2, "two seconds, one a second");
            Assert.LessOrEqual(Count(after, SampleKind.Location), 2, "stopped: one every two seconds");
        }

        [Test]
        public void TheNominalRateFollowsTheMotion()
        {
            Assert.AreEqual(1.5, new TelemetryScheduler(EquipmentKind.Hauler).NominalRatePerSecond, 1e-9);
            Assert.AreEqual(1.0, new TelemetryScheduler(EquipmentKind.Plant).NominalRatePerSecond, 1e-9);
            var s = new TelemetryScheduler(EquipmentKind.Dozer);
            var model = new MachineModel(EquipmentKind.Dozer, "SP-DZ-0009");
            var motion = new MotionEstimator();
            motion.Update(0, 0, 0.1, 0);
            double east = 0;
            for (var i = 0; i < 40; i++)
            {
                east += 0.5;
                motion.Update(east, 0, 0.1, 0);
            }

            s.Tick(T0, model, motion, east, 0, 0, new List<Sample>());
            Assert.AreEqual(3.0, s.NominalRatePerSecond, 1e-9);
        }

        // ---- the ring

        static Sample Seq(long n) => Sample.Measurement(n, T0 + TimeSpan.FromSeconds(n), new Dictionary<string, double> { ["fuel_pct"] = n });

        [Test]
        public void TheRingDropsTheOldestAndCountsEveryDrop()
        {
            var ring = new OutboundRing();
            for (var i = 1; i <= 40; i++) ring.Enqueue(Seq(i));
            Assert.AreEqual(OutboundRing.Capacity, ring.Count);
            Assert.AreEqual(32, OutboundRing.Capacity);
            Assert.AreEqual(8, ring.Dropped);
            Assert.IsTrue(ring.TryPeek(out var head));
            Assert.AreEqual(9, head.Sequence, "the 8 oldest went, the newest survive");
            Assert.IsTrue(ring.Remove(9));
            Assert.IsFalse(ring.Remove(9));
            Assert.IsTrue(ring.TryPeek(out head));
            Assert.AreEqual(10, head.Sequence);
            Assert.AreEqual(8, ring.Dropped, "sending is not dropping");
        }

        // ---- the pump

        sealed class FakeSink : ISampleSink, ISampleObserver
        {
            public bool Ready = true;
            public Func<Sample, int, Exception> Fail = (s, n) => null;
            public readonly List<Sample> Attempts = new List<Sample>();
            public readonly List<(Sample s, DateTimeOffset at)> Acked = new List<(Sample, DateTimeOffset)>();
            public readonly List<(Sample s, string reason, bool permanent)> Failures = new List<(Sample, string, bool)>();

            public bool CanPublish => Ready;

            public Task PublishAsync(Sample sample, CancellationToken cancellationToken)
            {
                Attempts.Add(sample);
                var e = Fail(sample, Attempts.Count);
                return e == null ? Task.CompletedTask : Task.FromException(e);
            }

            public void Published(Sample sample, DateTimeOffset at) => Acked.Add((sample, at));
            public void SendFailed(Sample sample, string reason, bool permanent) => Failures.Add((sample, reason, permanent));
        }

        static PumpOutcome Step(SamplePump p, DateTimeOffset at) => p.StepAsync(at, CancellationToken.None).GetAwaiter().GetResult();

        [Test]
        public void ARetryResendsTheSameSampleWithTheSameTimeAndValues()
        {
            var ring = new OutboundRing();
            var sink = new FakeSink { Fail = (s, n) => n <= 2 ? new TimeoutException("broker did not answer") : null };
            var pump = new SamplePump(ring, sink, () => 1.5, sink);
            var sample = Sample.Measurement(1, T0, new Dictionary<string, double> { ["fuel_pct"] = 61.25, ["engine_temp_c"] = 88.4 });
            ring.Enqueue(sample);
            ring.Enqueue(Seq(2));

            Assert.AreEqual(PumpOutcome.Failed, Step(pump, T0 + TimeSpan.FromSeconds(10)));
            Assert.AreEqual(PumpOutcome.Backoff, Step(pump, T0 + TimeSpan.FromSeconds(10.1)), "no hammering a failed send");
            Assert.AreEqual(PumpOutcome.Failed, Step(pump, T0 + TimeSpan.FromSeconds(11)));
            Assert.AreEqual(PumpOutcome.Sent, Step(pump, T0 + TimeSpan.FromSeconds(13)));

            Assert.AreEqual(3, sink.Attempts.Count);
            foreach (var a in sink.Attempts)
            {
                Assert.AreSame(sample, a, "the same sample, not a rebuilt one");
                Assert.AreEqual(T0, a.OccurredUtc, "the occurred time of the original, not of the retry");
                Assert.AreEqual(61.25, a.Values["fuel_pct"]);
                Assert.AreEqual(88.4, a.Values["engine_temp_c"]);
            }

            Assert.AreEqual(2, sink.Failures.Count);
            Assert.IsFalse(sink.Failures[0].permanent);
            Assert.AreEqual(1, sink.Acked.Count);
            Assert.AreEqual(1, ring.Count, "only the acknowledged sample left the ring");
            Assert.IsTrue(ring.TryPeek(out var next));
            Assert.AreEqual(2, next.Sequence);
        }

        [Test]
        public void ASampleTheSdkRefusesIsDiscardedSoItCannotBlockTheQueue()
        {
            var ring = new OutboundRing();
            var sink = new FakeSink { Fail = (s, n) => n == 1 ? new ArgumentOutOfRangeException("latitude") : null };
            var pump = new SamplePump(ring, sink, () => 1.5, sink);
            ring.Enqueue(Seq(1));
            ring.Enqueue(Seq(2));
            Assert.AreEqual(PumpOutcome.Rejected, Step(pump, T0 + TimeSpan.FromSeconds(10)));
            Assert.IsTrue(sink.Failures[0].permanent);
            Assert.AreEqual(PumpOutcome.Sent, Step(pump, T0 + TimeSpan.FromSeconds(11)));
            Assert.AreEqual(0, ring.Count);
        }

        [Test]
        public void WhileTheLinkIsDownNothingIsSentAndTheRingBuffersAndDrops()
        {
            var ring = new OutboundRing();
            var sink = new FakeSink { Ready = false };
            var pump = new SamplePump(ring, sink, () => 1.5, sink);
            Assert.AreEqual(PumpOutcome.Idle, Step(pump, T0));
            for (var i = 1; i <= 50; i++) ring.Enqueue(Seq(i));
            Assert.AreEqual(PumpOutcome.NotReady, Step(pump, T0 + TimeSpan.FromSeconds(1)));
            Assert.AreEqual(0, sink.Attempts.Count);
            Assert.AreEqual(18, ring.Dropped);
            Assert.AreEqual(32, ring.Count);
        }

        [Test]
        public void ABacklogDrainsAtNoMoreThanTwiceTheNormalRate()
        {
            foreach (var nominal in new[] { 1.0, 1.5, 3.0 })
            {
                var ring = new OutboundRing();
                var sink = new FakeSink();
                var pump = new SamplePump(ring, sink, () => nominal, sink);
                long n = 0;
                for (var i = 0; i < 32; i++) ring.Enqueue(Seq(++n));
                var sent = 0;
                const int seconds = 10;
                for (var ms = 0; ms < seconds * 1000; ms += 5)
                {
                    while (ring.Count < 32) ring.Enqueue(Seq(++n));            // a backlog that never runs out
                    if (Step(pump, T0 + TimeSpan.FromMilliseconds(ms)) == PumpOutcome.Sent) sent++;
                }

                Assert.LessOrEqual(sent, 2 * nominal * seconds + 1, $"nominal {nominal}/s");
                Assert.GreaterOrEqual(sent, 2 * nominal * seconds - 4, $"nominal {nominal}/s: it does drain, at the limit");
            }
        }

        [Test]
        public void SteadyProductionAtTheNormalRateIsNeverHeldBack()
        {
            var ring = new OutboundRing();
            var sink = new FakeSink();
            var pump = new SamplePump(ring, sink, () => 3.0, sink);
            long n = 0;
            var maxBacklog = 0;
            // 3 events a second, in a measurement + two locations pattern
            for (var ms = 0; ms < 20000; ms += 5)
            {
                if (ms % 1000 == 0 || ms % 1000 == 500) ring.Enqueue(Seq(++n));
                if (ms % 1000 == 0) ring.Enqueue(Seq(++n));
                Step(pump, T0 + TimeSpan.FromMilliseconds(ms));
                maxBacklog = Math.Max(maxBacklog, ring.Count);
            }

            Assert.LessOrEqual(maxBacklog, 2, "the pace limit does not delay a device that is keeping up");
            Assert.AreEqual(0, ring.Dropped);
            Assert.AreEqual(n, sink.Acked.Count + ring.Count);
        }

        sealed class BlockingSink : ISampleSink, ISampleObserver
        {
            public readonly TaskCompletionSource<bool> Gate = new TaskCompletionSource<bool>();
            public readonly TaskCompletionSource<bool> Entered = new TaskCompletionSource<bool>();
            public int Acked;
            public bool CanPublish => true;

            public Task PublishAsync(Sample sample, CancellationToken cancellationToken)
            {
                Entered.TrySetResult(true);
                return Gate.Task;
            }

            public void Published(Sample sample, DateTimeOffset at) => Acked++;
            public void SendFailed(Sample sample, string reason, bool permanent) { }
        }

        [Test]
        public void ASampleDroppedWhileInFlightIsNotAlsoCountedPublishedAndTheUnsentHeadStays()
        {
            var ring = new OutboundRing();
            var sink = new BlockingSink();
            var pump = new SamplePump(ring, sink, () => 1.5, sink);
            long produced = 0;
            for (var i = 0; i < OutboundRing.Capacity; i++) ring.Enqueue(Seq(++produced));

            var step = pump.StepAsync(T0 + TimeSpan.FromSeconds(10), CancellationToken.None);
            Assert.IsTrue(sink.Entered.Task.Wait(TimeSpan.FromSeconds(5)), "sample 1 is in flight");

            ring.Enqueue(Seq(++produced));            // the ring is full: the in-flight sample 1 is dropped for room
            Assert.AreEqual(1, ring.Dropped);

            sink.Gate.SetResult(true);
            Assert.AreEqual(PumpOutcome.Sent, step.GetAwaiter().GetResult());

            Assert.IsTrue(ring.TryPeek(out var head));
            Assert.AreEqual(2, head.Sequence, "the unsent head is still queued: the ack removed the sample it sent, not the head");
            Assert.AreEqual(OutboundRing.Capacity, ring.Count);
            Assert.AreEqual(0, sink.Acked, "already counted as dropped, so not published as well");
            Assert.AreEqual(produced, sink.Acked + ring.Dropped + ring.Count, "produced = published + dropped + queued");
        }

        [Test]
        public void ThePumpStopsAtOnceWhenCancelled()
        {
            var ring = new OutboundRing();
            var sink = new FakeSink();
            var pump = new SamplePump(ring, sink, () => 1.5, sink);
            using (var cts = new CancellationTokenSource())
            {
                var run = Task.Run(() => pump.RunAsync(cts.Token));
                ring.Enqueue(Seq(1));
                var deadline = DateTime.UtcNow.AddSeconds(5);
                while (ring.Count > 0 && DateTime.UtcNow < deadline) Thread.Sleep(10);
                Assert.AreEqual(0, ring.Count, "the running pump sent it");
                cts.Cancel();
                Assert.IsTrue(run.Wait(TimeSpan.FromSeconds(5)));
            }
        }
    }
}
