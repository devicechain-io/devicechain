// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.IO;
using System.Text.Json;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    public class DirectorCostTests
    {
        static Observation Reading(int i, double sim, string device = "hp-pump-north") =>
            new Observation("run", "measure", "reading-" + i, device, "wet_well_m", "", "", "", "", i, i, sim, EvidenceSource.Platform,
                new Dictionary<string, string> { ["value"] = "2" });

        static long TickWork(int size)
        {
            var store = new ObservationStore();
            // Same kind/device/key as active predicates, but historical sim times. Also include
            // a large unrelated population IN the active interval, to pin the identity index.
            for (var i = 0; i < size; i++) store.Add(Reading(i, i % 2 == 0 ? 1 : 17.3, i % 2 == 0 ? "hp-pump-north" : "other-pump"));
            var scheduler = new Scheduler(DirectorKit.Story(), Kit.Layout());
            using var document = JsonDocument.Parse("{\"measure\":{\"device\":\"hp-pump-north\",\"key\":\"wet_well_m\",\"gte\":1}}");
            var needs = Needs.Parse(document.RootElement);
            store.Add(Reading(-1, 17.3));
            var now = size + 120.0; // all ingested observations have arrived, as in the live adapter
            scheduler.Tick(new DirectorSample("run", 17.3, now), store); // initial join consumes the pre-join archive once
            store.ResetWorkUnits();
            for (var tick = 1; tick <= 120; tick++) {
                store.Add(Reading(size + tick, 1)); // incoming history must not invalidate a full-store cache
                scheduler.Tick(new DirectorSample("run", 17.3, now + tick / 60.0), store);
                Assert.That(needs.Evaluate(store, new EvidenceContext("run", 17, 18, now + tick / 60.0)).Records.Single().Identity, Is.EqualTo("reading--1"));
            }
            return store.WorkUnits;
        }
        [Test]
        public void FrameWorkDoesNotGrowWithObservationArchive()
        {
            var small = TickWork(10000); var large = TickWork(100000);
            TestContext.WriteLine("120 ticks: 10k=" + small + ", 100k=" + large + " record visits");
            Assert.That(large, Is.LessThanOrEqualTo(small + 500));
            Assert.That(large, Is.LessThan(5000));
            Assert.That(small, Is.GreaterThanOrEqualTo(120));
        }
        [Test]
        public void ContinuousGapsStayBoundedAndStillVetoPositiveCoverage()
        {
            var store = new ObservationStore();
            store.AddCoverage(new CoverageInterval("run", "pump", 0, 100000, false));
            for (var i = 0; i < 100000; i++) store.AddCoverage(new CoverageInterval("run", "pump", i, i + 1, true));
            Assert.That(store.Coverage.Count, Is.EqualTo(2));
            store.ResetWorkUnits();
            for (var i = 0; i < 120; i++) Assert.That(store.Covers("run", "pump", 500, 501), Is.False);
            Assert.That(store.WorkUnits, Is.LessThanOrEqualTo(120));
        }
        [Test]
        public void CoverageIndexPreservesOriginalIntervalAndPointDecisions()
        {
            var store = new ObservationStore(); var original = new List<CoverageInterval>();
            var random = new Random(834);
            for (var i = 0; i < 100; i++) {
                var from = random.Next(100); var span = new CoverageInterval("run", "pump", from, from + random.Next(4), i % 7 == 0);
                original.Add(span); store.AddCoverage(span);
            }
            bool Original(double from, double to) {
                if (to < from || original.Any(g => g.Gap && g.FromSim <= to && g.ToSim >= from)) return false;
                var end = from;
                foreach (var span in original.Where(c => !c.Gap).OrderBy(c => c.FromSim)) {
                    if (span.FromSim > end) break;
                    if (span.ToSim >= end) end = span.ToSim;
                    if (end >= to) return true;
                }
                return false;
            }
            for (var from = 0; from < 100; from++) for (var length = 0; length < 6; length++)
                Assert.That(store.Covers("run", "pump", from, from + length), Is.EqualTo(Original(from, from + length)), from + ":" + length);
        }
        [Test]
        public void LatestRevisionPrecedesFieldAndIntervalFilteringEvenAcrossFutureArrivals()
        {
            var store = new ObservationStore();
            Observation Row(string state, double observed, double sim) => new Observation("run", "alarm", "a", "d", "k", "c", "", "", state, observed, observed, sim, EvidenceSource.Platform);
            store.Add(Row("RAISED", 1, 1)); store.Add(Row("CLEARED", 3, 3)); store.Add(Row("RAISED", 4, 4));
            using var doc = JsonDocument.Parse("{\"alarm\":{\"mode\":\"current\",\"device\":\"d\",\"key\":\"k\",\"state\":\"RAISED\"}}");
            var needs = Needs.Parse(doc.RootElement);
            Assert.That(needs.Evaluate(store, new EvidenceContext("run", 0, 2, 2)).Holds, Is.True);
            Assert.That(needs.Evaluate(store, new EvidenceContext("run", 0, 2, 3)).Holds, Is.False);
            Assert.That(needs.Evaluate(store, new EvidenceContext("run", 0, 5, 4)).Records.Single().ObservedAtS, Is.EqualTo(4));
            Assert.That(needs.Evaluate(store, new EvidenceContext("run", 0, 2, 2)).Holds, Is.True); // standalone seek
        }
        [Test]
        public void StreamPublicationDoesNotCopyHistoryAndKeepsOldSnapshotDetached()
        {
            var tap = new DirectorMeasurementTap();
            tap.Watch(new DirectorMeasurement("d", "m", 1, DateTimeOffset.UnixEpoch));
            var prefix = tap.Records;
            var first = new DirectorReadSnapshot(Array.Empty<DirectorRead>(), measurements: prefix);
            for (var i = 0; i < 100000; i++) tap.Watch(new DirectorMeasurement("d", "m", i, DateTimeOffset.UnixEpoch));
            Assert.That(first.Measurements, Is.SameAs(prefix));
            Assert.That(first.Measurements.Count, Is.EqualTo(1));
            Assert.That(tap.Records.Count, Is.EqualTo(100001));
        }
        [Test]
        public void DispatchedRecorderDefersCheckpointWorkAndPreservesEveryByte()
        {
            var sync = Enumerable.Range(0, 4).Select(_ => new StringWriter()).ToArray();
            var deferred = Enumerable.Range(0, 4).Select(_ => new StringWriter()).ToArray();
            var work = new Queue<Action>();
            var direct = new Recorder(sync[0], sync[1], sync[2], sync[3]);
            var queued = new Recorder(deferred[0], deferred[1], deferred[2], deferred[3], work.Enqueue);
            foreach (var recorder in new[] { direct, queued }) {
                recorder.Observe(1, Reading(1, 1));
                recorder.Coverage(2, new CoverageInterval("run", "pump", 0, 1, true));
                var keys = new HashSet<string> { "proof" }; recorder.Consumption(3, keys); keys.Clear();
                recorder.Advance(61); recorder.Decision(new DirectorEvent(62, "caption", "4", "test"));
                recorder.Advance(125); recorder.End();
            }
            Assert.That(deferred.All(w => w.ToString() == ""), Is.True);
            while (work.Count > 0) work.Dequeue()();
            for (var i = 0; i < 4; i++) Assert.That(deferred[i].ToString(), Is.EqualTo(sync[i].ToString()));
        }
    }
    public partial class DirectorTestsObserver
    {
        [Test]
        public void IdleFramesDoNotReprocessRetainedWellStreamHistory()
        {
            var tap = new DirectorMeasurementTap();
            for (var i = 0; i < 1000; i++) tap.Watch(new DirectorMeasurement("hp-pump-north", "wet_well_m", 2, At(17).AddTicks(i)));
            var snapshot = new DirectorReadSnapshot(Array.Empty<DirectorRead>(), measurements: tap.Records);
            var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
            var observations = new List<Observation>();
            adapter.Update(Inputs(17.1), observations.Add, _ => { });
            var work = adapter.MeasurementWorkUnits;
            Assert.That(work, Is.EqualTo(1000));
            observations.Clear();
            for (var tick = 1; tick <= 120; tick++) adapter.Update(Inputs(17.1 + tick / 10000.0), observations.Add, _ => { });
            Assert.That(adapter.MeasurementWorkUnits, Is.EqualTo(work));
            Assert.That(observations.Count(o => o.Kind == "measure"), Is.Zero);
        }
    }
}
