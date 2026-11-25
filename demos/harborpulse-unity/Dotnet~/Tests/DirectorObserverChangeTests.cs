// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Security.Cryptography;
using System.Text;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>
    /// The observer runs a join again only when something the join reads has changed. These hold that to its two promises: a
    /// frame's work does not grow with what the polled reads already hold, and the records emitted are exactly those of an
    /// observer that recomputes everything on every frame.
    /// </summary>
    public class DirectorObserverChangeTests
    {
        /// <summary>Every record and coverage interval the observer emits over the fixture day, stamped with the frame it was emitted on.</summary>
        internal static List<string> Trace(Func<DirectorFixtureDay, Func<DirectorReadSnapshot>, IDirectorFeed> make, Action<DirectorFixtureDay> shape = null, int backfillRows = 360, double stride = 1)
        {
            var day = new DirectorFixtureDay(backfillRows); shape?.Invoke(day);
            DirectorReadSnapshot snapshot = null; var feed = make(day, () => snapshot);
            var model = new HarborObservedModel(day.Layout); var lines = new List<string>(); var problems = "";
            var frame = 0;
            // Uneven frames, a tenth of a second on average at stride 1: a frame lands before, on and after every poll and every record's instant.
            for (var wall = 0.05; wall < 1272; wall += stride * (0.04 + 0.12 * (frame * 7919 % 13) / 13), frame++) {
                snapshot = day.Snapshot(wall);
                var world = model.Update(day.Feed, new ClockReading((6 + wall / 60) * 3600, ClockMode.Live, ""), DirectorFixtureDay.At(wall));
                var at = frame.ToString(CultureInfo.InvariantCulture);
                feed.Update(day.Inputs(wall, world), o => lines.Add(at + " " + Line(o)), c => lines.Add(at + " cover " + c.Run + " " + c.Stream + " " + R(c.FromSim) + " " + R(c.ToSim) + " " + c.Gap));
                var now = string.Join(" | ", feed.Problems);
                if (now != problems) { problems = now; lines.Add(at + " problems " + now); }
            }
            return lines;
        }
        static string R(double value) => value.ToString("R", CultureInfo.InvariantCulture);
        static string Line(Observation o) => string.Join(" ", new[] { "record", o.Run, o.Kind, o.Identity, o.Device, o.Key, o.Cycle, o.Command, o.Attempt, o.Status, R(o.ObservedAtS), R(o.OccurredAtS), R(o.Sim), o.Source }.Select(v => v.Length + ":" + v))
            + " {" + string.Join(",", o.Fields.OrderBy(p => p.Key, StringComparer.Ordinal).Select(p => p.Key + "=" + p.Value)) + "} {"
            + string.Join(",", o.Sets.OrderBy(p => p.Key, StringComparer.Ordinal).Select(p => p.Key + "=[" + string.Join(";", p.Value) + "]")) + "}";
        static string Digest(IEnumerable<string> lines)
        {
            using (var sha = SHA256.Create()) return string.Concat(sha.ComputeHash(Encoding.UTF8.GetBytes(string.Join("\n", lines))).Select(b => b.ToString("x2", CultureInfo.InvariantCulture)));
        }
        static void Same(List<string> expected, List<string> actual)
        {
            for (var i = 0; i < Math.Min(expected.Count, actual.Count); i++)
                if (expected[i] != actual[i]) Assert.Fail("line " + i + " differs\n  recomputed: " + expected[i] + "\n  on change:  " + actual[i]);
            Assert.That(actual.Count, Is.EqualTo(expected.Count));
        }
        // Two dropped poll rounds, one while the replaced gauge's history is being read and one after the restore: every read turns
        // unhealthy in its turn and comes back, so the joins see their rows leave and return with nothing else changed. And an
        // alarm no join can place, which is a gap on every frame after it is listed.
        internal static void Stalls(DirectorFixtureDay day) { day.Stalls = new[] { new[] { 396, 404.5 }, new[] { 960, 968.5 } }; day.Stranger = true; }

        // The platform's clock ahead of the player's as well: records arrive stamped after "now" and each join must come back for them.
        internal static void StallsAhead(DirectorFixtureDay day) { Stalls(day); day.Lead = 2.5; }

        [TestCase(false)]
        [TestCase(true)]
        public void ADayEmitsTheSameRecordsWhetherJoinsWaitForAChangeOrRunEveryFrame(bool ahead)
        {
            // a ninth of the buffered rows: this holds the joins to their stamps, and the recomputing side pays for every row on every frame
            var shape = ahead ? StallsAhead : (Action<DirectorFixtureDay>)Stalls;
            var recomputed = Trace((day, reads) => new DirectorObserver(day.Layout, reads) { RunEveryJoin = true }, shape, 40, 2);
            var onChange = Trace((day, reads) => new DirectorObserver(day.Layout, reads), shape, 40, 2);
            Same(recomputed, onChange);
            Assert.That(onChange.Count(l => l.Contains(" 6:record ")), Is.GreaterThan(30000), "the day is a day's worth of records");
            Assert.That(onChange.Any(l => l.Contains(" 8:backfill ")), Is.True, "the restore's buffered readings are in it");
            Assert.That(onChange.Any(l => l.Contains(" problems ") && l.Contains("timeout")), Is.True, "the dropped polls are in it");
            Assert.That(onChange.Any(l => l.Contains(" problems ") && l.Contains("unbound platform device hp-visitor-berth-9")), Is.True, "the alarm no join can place is in it");
            Assert.That(onChange.Any(l => l.Contains(" 11:replacement 1:2 ")), Is.True, "the second swap is in it, emitted after its minute with no reading");
            Assert.That(onChange.Any(l => l.Contains(" problems ") && l.Contains("rollout counts")), Is.True, "the finished rollout whose counts cannot be stated yet is in it");
        }
        /// <summary>
        /// The same day as the observer emitted it before joins waited for a change (every polled read parsed and joined on every
        /// frame, every lookup a scan). The digests were taken from that observer over this fixture; a change to what the observer
        /// emits, or to the fixture, moves them, and whoever moves them says which.
        /// </summary>
        [TestCase("whole", 37163, 167658, "1a2c4a2aaa20a022e3d10bc9b33aa5a48ef9256bb071fbfc485263418c58c9c4")]
        [TestCase("stalled", 37153, 174370, "6b6e37f94f2e2cb53b21d40a859c19be5023c7cbd8b6f31ba772e7fa15ac0e06")]
        [TestCase("ahead", 33964, 102633, "ac63a067c21074f67ebaa86ba240154f142e04ff50ffa532624430bcaf6ee495")]
        public void ADayEmitsWhatTheRecomputingObserverEmitted(string shape, int records, int lines, string digest)
        {
            // "ahead" is the stalled day with the platform's clock 2.5 s ahead, at the coarser frames and smaller restore of the test above
            var trace = shape == "ahead" ? Trace((day, reads) => new DirectorObserver(day.Layout, reads), StallsAhead, 40, 2)
                : Trace((day, reads) => new DirectorObserver(day.Layout, reads), shape == "stalled" ? Stalls : (Action<DirectorFixtureDay>)null);
            Assert.That(trace.Count(l => l.Contains(" 6:record ")), Is.EqualTo(records));
            Assert.That(trace.Count, Is.EqualTo(lines));
            Assert.That(Digest(trace), Is.EqualTo(digest));
        }

        sealed class Counts { public long Read, Measured; public int HistoryRows, Frames; }
        /// <summary>The work of a quiet minute after the restore: the buffered rows are all in, and every read is polled again every three seconds.</summary>
        static Counts Quiet(int backfillRows, bool everyFrame)
        {
            var day = new DirectorFixtureDay(backfillRows); DirectorReadSnapshot snapshot = null;
            var observer = new DirectorObserver(day.Layout, () => snapshot) { RunEveryJoin = everyFrame };
            var counts = new Counts(); long read = 0, measured = 0;
            for (var tick = 0; tick < 3000; tick++) {
                var wall = 900 + tick * 0.05;
                if (tick == 1800) { read = observer.ReadWorkUnits; measured = observer.MeasurementWorkUnits; counts.HistoryRows = day.ReadRows; }
                if (tick >= 1800) counts.Frames++;
                snapshot = day.Snapshot(wall);
                observer.Update(day.Inputs(wall), _ => { }, _ => { });
            }
            Assert.That(observer.Problems, Is.Empty, string.Join("; ", observer.Problems));
            counts.Read = observer.ReadWorkUnits - read; counts.Measured = observer.MeasurementWorkUnits - measured;
            return counts;
        }
        [Test]
        public void AFramesWorkDoesNotGrowWithWhatUnchangedReadsHold()
        {
            var small = Quiet(360, false); var large = Quiet(1440, false);
            TestContext.WriteLine(small.Frames + " frames, 20 poll rounds, history rows " + small.HistoryRows + " and " + large.HistoryRows + ": read records visited " + small.Read + " and " + large.Read
                + ", readings joined " + small.Measured + " and " + large.Measured);
            Assert.That(small.Frames, Is.EqualTo(1200)); Assert.That(small.HistoryRows, Is.EqualTo(804)); Assert.That(large.HistoryRows, Is.EqualTo(2964));
            // Four times the held history, and not one more record visited: what is visited is what the polls of the small reads
            // restate (a snapshot's as-of, an alarm read new enough to cover an attempt) and the stream's own new readings.
            Assert.That(large.Read, Is.EqualTo(small.Read));
            Assert.That(large.Measured, Is.EqualTo(small.Measured));
            Assert.That(small.Read, Is.LessThan(6000));
            Assert.That(small.Measured, Is.InRange(1000, 2000), "the stream's 25 readings a second, and no history row");
        }
        [Test]
        public void TheWorkCountersSeeAnObserverThatRecomputesEveryFrame()
        {
            // The control for the test above: with every join run on every frame the same counters grow with the held history,
            // by the frames times the rows. A counter that could not see that would pass the test above for nothing.
            var small = Quiet(20, true); var large = Quiet(60, true);
            TestContext.WriteLine("every frame: read records visited " + small.Read + " and " + large.Read + ", readings joined " + small.Measured + " and " + large.Measured);
            Assert.That(small.Measured, Is.GreaterThan((long)small.Frames * small.HistoryRows));
            Assert.That(large.Measured - small.Measured, Is.EqualTo((long)small.Frames * (large.HistoryRows - small.HistoryRows)));
            Assert.That(large.Read - small.Read, Is.EqualTo((long)small.Frames * (large.HistoryRows - small.HistoryRows)));
        }
        [TestCase(false)]
        [TestCase(true)]
        public void AReadingStampedExactlyNowIsCountedAsBackdatedOnTheNextFrame(bool everyFrame)
        {
            // The platform's clock five seconds ahead: the two chargers' first readings on mains again are in the history reads
            // before their instant. On the frame that lands on the instant the reading is emitted, but it is not yet behind
            // "now", so it is counted against the runner's buffered span one frame later, with no new poll to prompt that.
            var day = new DirectorFixtureDay { Lead = 5 }; DirectorReadSnapshot snapshot = null; var seen = new List<Observation>();
            var observer = new DirectorObserver(day.Layout, () => snapshot) { RunEveryJoin = everyFrame };
            const double instant = DirectorFixtureDay.OutageTo + 2.5;
            for (var tick = 0; 900 + tick * 0.05 < instant - 0.01; tick++) { snapshot = day.Snapshot(900 + tick * 0.05); observer.Update(day.Inputs(900 + tick * 0.05), seen.Add, _ => { }); }
            Assert.That(seen.Any(o => o.Kind == "measure" && o.OccurredAtS == instant), Is.False, "held back until its instant");
            snapshot = day.Snapshot(instant); observer.Update(day.Inputs(instant), seen.Add, _ => { });
            Assert.That(seen.Count(o => o.Kind == "measure" && o.OccurredAtS == instant && o.Key != "backfill"), Is.EqualTo(8), "two chargers, four metrics");
            Assert.That(seen.Any(o => o.OccurredAtS == instant && o.Key == "backfill"), Is.False);
            observer.Update(day.Inputs(instant + 0.05), seen.Add, _ => { });
            Assert.That(seen.Count(o => o.OccurredAtS == instant && o.Key == "backfill"), Is.EqualTo(8));
        }
        [Test]
        public void FramesBetweenPollsVisitNoReadRecord()
        {
            var day = new DirectorFixtureDay(); DirectorReadSnapshot snapshot = null;
            var observer = new DirectorObserver(day.Layout, () => snapshot);
            for (var tick = 0; tick < 2000; tick++) { snapshot = day.Snapshot(900 + tick * 0.05); observer.Update(day.Inputs(900 + tick * 0.05), _ => { }, _ => { }); }
            var read = observer.ReadWorkUnits; var measured = observer.MeasurementWorkUnits; var emitted = 0;
            // the same published reads, looked at on 120 more frames (two seconds at 60 frames a second)
            for (var tick = 1; tick <= 120; tick++) observer.Update(day.Inputs(1000 + tick / 60.0), _ => emitted++, _ => { });
            Assert.That(day.ReadRows, Is.EqualTo(804));
            Assert.That(observer.ReadWorkUnits - read, Is.Zero);
            Assert.That(observer.MeasurementWorkUnits - measured, Is.Zero);
            Assert.That(emitted, Is.Zero);
        }
    }
}
