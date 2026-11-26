// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    public class DirectorTestsRecorder
    {
        [Test]
        public void SeekEqualsPlayAtTenRandomTargetsForwardAndBackward()
        {
            var o = new StringWriter(); var w = new StringWriter(); var d = new StringWriter(); var c = new StringWriter();
            var recorder = new Recorder(o, w, d, c);
            for (var t = 0; t <= 300; t++)
            {
                recorder.Observe(t, DirectorKit.O("measure", "gauge", "water_level_m", sim: 6 + t / 60.0, wall: t, id: "reading-" + t, fields: new Dictionary<string, string> { ["value"] = (t / 100.0).ToString(System.Globalization.CultureInfo.InvariantCulture) }));
                recorder.World(t, "{\"scenario\":{\"day\":0,\"simHour\":" + (6 + t / 60.0).ToString(System.Globalization.CultureInfo.InvariantCulture) + "}}");
                if (t % 20 == 0) {
                    recorder.Decision(new DirectorEvent(t, "state", "4", t % 40 == 0 ? "Playing" : "Watching"));
                    recorder.Decision(new DirectorEvent(t, "node", "4", "node-" + t));
                    if (t > 0) recorder.Decision(new DirectorEvent(t, "link", "4", "node-" + (t - 20) + "->node-" + t));
                    recorder.Decision(new DirectorEvent(t, "caption", "4", "caption-" + t));
                }
                if (t % 20 == 5) recorder.Decision(new DirectorEvent(t, "caption-end", "4", "caption"));
                if (t % 30 == 0) recorder.Consumption(t, new[] { "key-" + t });
                if (t == 125) recorder.Coverage(t, new CoverageInterval("run", "alarms", 8, 8.1, true));
            }
            var reader = new RecordingReader(new StringReader(o.ToString()), new StringReader(w.ToString()), new StringReader(d.ToString()), new StringReader(c.ToString()));
            var random = new Random(9018); var targets = Enumerable.Range(0, 10).Select(_ => random.NextDouble() * 300).OrderBy(x => x).ToArray();
            foreach (var t in targets.Concat(targets.Reverse()))
            {
                Assert.That(reader.Seek(t).Hash(), Is.EqualTo(reader.PlayTo(t).Hash()), "target " + t);
                Assert.That(reader.LastCheckpointLoadedS, Is.EqualTo(Math.Floor(t / 60) * 60));
            }
            Assert.That(reader.CheckpointTimes, Is.EqualTo(new double[] { 0, 60, 120, 180, 240, 300 }));
            var rail = reader.Seek(241); Assert.That(rail.Decisions.Values.Count(e => e.Kind == "node"), Is.EqualTo(13)); Assert.That(rail.Decisions.Values.Count(e => e.Kind == "link"), Is.EqualTo(12));
            Assert.That(rail.VisibleCaption("4"), Is.EqualTo("caption-240")); Assert.That(reader.Seek(246).VisibleCaption("4"), Is.Null);
        }
        [Test]
        public void CheckpointsCatchUpAtExactCadenceWithoutFutureState()
        {
            var o = new StringWriter(); var w = new StringWriter(); var d = new StringWriter(); var c = new StringWriter(); var recorder = new Recorder(o, w, d, c);
            recorder.Observe(0, DirectorKit.O("measure", id: "early", wall: 0)); recorder.Observe(180, DirectorKit.O("measure", id: "future", wall: 180)); recorder.Advance(240);
            var reader = new RecordingReader(new StringReader(o.ToString()), new StringReader(w.ToString()), new StringReader(d.ToString()), new StringReader(c.ToString()));
            Assert.That(reader.CheckpointTimes, Is.EqualTo(new double[] { 0, 60, 120, 180, 240 })); Assert.That(reader.Seek(120).Observed.Records.Count, Is.EqualTo(1));
            Assert.That(reader.Seek(180).Hash(), Is.EqualTo(reader.PlayTo(180).Hash())); Assert.That(reader.Seek(240).Observed.Records.Count, Is.EqualTo(2));
        }
        // A frame reports the interval that continues the last one, every frame. Written row by row a day was 3.3 million rows (692 MB).
        [Test]
        public void ContiguousCoverageEveryFrameIsFewRowsAndReadsBackAsTheSameCoverage()
        {
            var o = new StringWriter(); var w = new StringWriter(); var d = new StringWriter(); var c = new StringWriter(); var recorder = new Recorder(o, w, d, c);
            var reference = new ObservationStore();
            // 20 s at 100 frames a second, two streams; the alarms stream has a gap at 13 s that ends at 14 s
            for (var frame = 0; frame < 2000; frame++)
            {
                var t = frame / 100.0; var from = 6 + t / 60.0; var to = 6 + (t + 0.01) / 60.0;
                foreach (var stream in new[] { "alarms", "measurements" })
                {
                    var covered = !(stream == "alarms" && t >= 13 && t < 14);
                    var interval = new CoverageInterval("run", stream, from, to, !covered);
                    reference.AddCoverage(interval); recorder.Coverage(t, interval);
                    if (!covered) { /* a gap row is written beside the missing positive coverage */ }
                }
                recorder.Advance(t);
            }
            recorder.End();
            var rows = o.ToString().Split(new[] { '\n' }, StringSplitOptions.RemoveEmptyEntries).Length;
            Assert.That(rows, Is.LessThan(120), "4000 intervals are written as the few runs they make up");
            var reader = new RecordingReader(new StringReader(o.ToString()), new StringReader(w.ToString()), new StringReader(d.ToString()), new StringReader(c.ToString()));
            var read = reader.Seek(60).Observed;
            foreach (var stream in new[] { "alarms", "measurements" })
                foreach (var span in new[] { (6.0, 6.1), (6.1, 6.2), (6.21, 6.26), (6.0, 6.33), (6.0, 6.2) })
                    Assert.That(read.Covers("run", stream, span.Item1, span.Item2), Is.EqualTo(reference.Covers("run", stream, span.Item1, span.Item2)), stream + " " + span);
            // time never goes back: every row's wall second is at or after the one before it
            var walls = o.ToString().Split(new[] { '\n' }, StringSplitOptions.RemoveEmptyEntries).Select(l => double.Parse(l.Split(new[] { "\"wallS\":" }, StringSplitOptions.None)[1].Split(',')[0], System.Globalization.CultureInfo.InvariantCulture)).ToArray();
            for (var i = 1; i < walls.Length; i++) Assert.That(walls[i], Is.GreaterThanOrEqualTo(walls[i - 1]));
        }
        [Test]
        public void HashIgnoresInsertionFieldAndSetOrdering()
        {
            var a = new RecordedState(); var b = new RecordedState();
            var one = DirectorKit.O("measure", id: "one", fields: new Dictionary<string, string> { ["b"] = "2", ["a"] = "1" }, sets: new Dictionary<string, string[]> { ["tokens"] = new[] { "b", "a", "b" } });
            var two = DirectorKit.O("measure", id: "two");
            a.Observed.Add(one); a.Observed.Add(two); b.Observed.Add(two);
            b.Observed.Add(DirectorKit.O("measure", id: "one", fields: new Dictionary<string, string> { ["a"] = "1", ["b"] = "2" }, sets: new Dictionary<string, string[]> { ["tokens"] = new[] { "a", "b" } }));
            Assert.That(a.Hash(), Is.EqualTo(b.Hash()));
            b.Consumed.Add("new"); Assert.That(a.Hash(), Is.Not.EqualTo(b.Hash()));
        }
        [Test]
        public void UnknownRecordingKindIsRefused()
        {
            var reader = new RecordingReader(new StringReader("{\"wallS\":0,\"sequence\":1,\"kind\":\"unknown\",\"data\":{}}\n"), new StringReader(""), new StringReader(""), new StringReader(""));
            Assert.Throws<FormatException>(() => reader.PlayTo(1));
        }
        [Test]
        public void ReplayCaptionBoundaryKeepsNewCaptionAndSkippedSpansConsumeSilently()
        {
            var o = new StringWriter(); var w = new StringWriter(); var d = new StringWriter(); var c = new StringWriter(); var recorder = new Recorder(o, w, d, c);
            var early = DirectorKit.O("alarm", status: "RAISED", wall: 0); recorder.Observe(0, early);
            recorder.Decision(new DirectorEvent(0, "caption", "4", "first"));
            recorder.Decision(new DirectorEvent(3, "caption-end", "4", "first-id")); recorder.Decision(new DirectorEvent(3, "caption", "4", "next"));
            recorder.Advance(1300);
            var reader = new RecordingReader(new StringReader(o.ToString()), new StringReader(w.ToString()), new StringReader(d.ToString()), new StringReader(c.ToString()));
            Assert.That(reader.Seek(3).VisibleCaption("4"), Is.EqualTo("next"));
            var cut = DirectorKit.Story().Cuts.Single(x => x.Id == "c90");
            // the short cut's first evening shot starts 10.5 s in; the day before it is skipped, and consumed silently
            Assert.That(reader.SeekCut(cut, 11).Consumed, Does.Contain(early.EventKey));
        }
    }
}
