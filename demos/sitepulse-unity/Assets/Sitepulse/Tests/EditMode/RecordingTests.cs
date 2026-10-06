// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text;
using System.Text.RegularExpressions;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.DevicePlane;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Recording;
using DeviceChain.Sitepulse.Simulation;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    public sealed class RecordingFormatTests
    {
        static readonly DateTimeOffset S = SyntheticRun.Start;

        static void AssertSame(MachineSample a, MachineSample b, string what)
        {
            Assert.AreEqual(a.X, b.X, what + " x"); Assert.AreEqual(a.Y, b.Y, what + " y"); Assert.AreEqual(a.Z, b.Z, what + " z");
            Assert.AreEqual(a.Heading, b.Heading, what + " heading"); Assert.AreEqual(a.Pitch, b.Pitch, what + " pitch"); Assert.AreEqual(a.Roll, b.Roll, what + " roll");
            Assert.AreEqual(a.P1, b.P1, what + " p1"); Assert.AreEqual(a.P2, b.P2, what + " p2"); Assert.AreEqual(a.Steer, b.Steer, what + " steer"); Assert.AreEqual(a.Travel, b.Travel, what + " travel");
            Assert.AreEqual(a.FuelPct, b.FuelPct, what + " fuel"); Assert.AreEqual(a.EngineTempC, b.EngineTempC, what + " temp"); Assert.AreEqual(a.EngineHours, b.EngineHours, what + " hours");
            Assert.AreEqual(a.PayloadT, b.PayloadT, what + " payload"); Assert.AreEqual(a.TyrePressureKpa, b.TyrePressureKpa, what + " tyre");
            Assert.AreEqual(a.Mode, b.Mode, what + " mode"); Assert.AreEqual(a.Phase, b.Phase, what + " phase");
            Assert.AreEqual(a.Loaded, b.Loaded, what + " loaded"); Assert.AreEqual(a.OnTrack, b.OnTrack, what + " ontrack");
        }

        // ---- the round trip

        [Test]
        public void ARecordedRunReadsBackWithEveryFrameAndEveryLineIdentical()
        {
            using var run = new SyntheticRun();
            run.Drive(0, 2.0);
            run.Obs(0.5, SyntheticRun.Measurement(SyntheticRun.TruckToken, "fuel_pct", 49.2, S.AddSeconds(0.4), S.AddSeconds(0.45)));
            run.Obs(1.0, SyntheticRun.Alarm(SyntheticRun.TruckToken, "al-1", "low-fuel", "ACTIVE", S.AddSeconds(0.9)));
            run.Obs(1.2, SyntheticRun.Command(SyntheticRun.TruckToken, "cmd-1", "goto-refuel", "SENT", S.AddSeconds(1.1), S.AddSeconds(1.15)));
            run.Obs(1.3, SyntheticRun.Status("measurements", "Live", S.AddSeconds(1.3)));
            var loc = ObservedLine.Of(ObservedKinds.Location);
            loc.Device = SyntheticRun.TruckToken; loc.SpeedMps = 5.25; loc.HeadingDegrees = 91.5; loc.ElevationMetres = 1812.25; loc.OccurredAt = S.AddSeconds(1.3); loc.ObservedAt = S.AddSeconds(1.4);
            run.Obs(1.4, loc);
            var pres = ObservedLine.Of(ObservedKinds.Presence);
            pres.Device = SyntheticRun.TruckToken; pres.PresenceActive = true; pres.LastActivityAt = S.AddSeconds(1); pres.ObservedAt = S.AddSeconds(1.5);
            run.Obs(1.5, pres);
            var snap = ObservedLine.Of(ObservedKinds.AlarmSnapshot);
            snap.RequestedAt = S.AddSeconds(1.6); snap.ObservedAt = S.AddSeconds(1.7); snap.Truncated = true; snap.TotalRecords = 7;
            snap.Alarms.Add(SyntheticRun.Alarm(SyntheticRun.TruckToken, "al-1", "low-fuel", "ACTIVE", S.AddSeconds(0.9)));
            run.Obs(1.7, snap);

            var sample = DeviceLine.Of(DeviceKinds.Sample, SyntheticRun.Truck);
            sample.SampleKind = "measurement"; sample.OccurredAt = S.AddSeconds(0.9); sample.AckedAt = S.AddSeconds(0.95); sample.Values["fuel_pct"] = 49.25; sample.Values["engine_temp_c"] = 91;
            run.Dev(0.95, sample);
            var task = DeviceLine.Of(DeviceKinds.TaskReceived, SyntheticRun.Truck);
            task.Token = "cmd-1"; task.Key = "goto-refuel"; task.Sequence = 3;
            run.Dev(1.25, task);
            run.Dev(1.26, SyntheticRun.Row(SyntheticRun.Truck, "received", "goto-refuel cmd-1"));
            run.Dev(1.27, SyntheticRun.Row(SyntheticRun.Truck, "accepted", "route 212 m, ETA 48 s"));
            var done = DeviceLine.Of(DeviceKinds.TaskCompleted, SyntheticRun.Truck);
            done.Token = "cmd-1"; done.Succeeded = true; done.Reason = "refuelled to 95%";
            run.Dev(1.9, done);
            run.Pres(1.0, PresenterLine.Of(PresenterKinds.Action, "SP-HL-0006: prepare low-fuel cycle", SyntheticRun.Truck));

            var data = run.Reload();

            Assert.AreEqual(run.Frames.Count, data.Sim.FrameCount);
            for (var f = 0; f < run.Frames.Count; f++)
            {
                Assert.AreEqual(run.FrameTimes[f], data.Sim.TimeOf(f), 0.0, "frame " + f + " time");
                for (var m = 0; m < 2; m++) AssertSame(run.Frames[f][m], data.Sim.Frame(f, m), $"frame {f} machine {m}");
            }

            CollectionAssert.AreEqual(run.Observed, data.Observed.Select(l => l.ToJson()).ToList(), "observed.ndjson, line for line");
            CollectionAssert.AreEqual(run.Device, data.Device.Select(l => l.ToJson()).ToList(), "device.ndjson, line for line");
            CollectionAssert.AreEqual(run.Presenter, data.Presenter.Select(l => l.ToJson()).ToList(), "presenter.ndjson, line for line");
            Assert.AreEqual(1.4, data.Observed[4].T);
            Assert.AreEqual(5.25, data.Observed[4].SpeedMps);
            Assert.AreEqual("low-fuel", data.Observed[1].Name);
            Assert.AreEqual(1, data.Observed[6].Alarms.Count);
            Assert.IsTrue(data.Observed[6].Truncated);
            Assert.AreEqual(49.25, data.Device[0].Values["fuel_pct"]);
            Assert.AreEqual("refuelled to 95%", data.Device[4].Reason);
            Assert.AreEqual(0, data.Warnings.Count, string.Join("; ", data.Warnings));
            Assert.AreEqual(2.0, data.Duration, 0.2, "the run is as long as its last frame");
        }

        [Test]
        public void LinesWrittenFromManyThreadsAtOnceStillReadBackInTimeOrder()
        {
            // Acknowledgements are recorded from the SDK's threads. A line stamped before it reaches the
            // file can land after a later one; the reader refuses that, so the recorder must not write it.
            long ticks = 0;
            // every reading of the clock is later than the one before, as a real monotonic clock's would be
            using var run = new SyntheticRun(clock: () => System.Threading.Interlocked.Increment(ref ticks) / 1e6);
            run.Frame(0, SyntheticRun.Sample(0f, 0f, 0f), SyntheticRun.Sample(0f, 0f, 0f));
            var threads = Enumerable.Range(0, 8).Select(n => new System.Threading.Thread(() =>
            {
                for (var i = 0; i < 400; i++)
                {
                    var l = DeviceLine.Of(DeviceKinds.Sample, SyntheticRun.Truck);
                    l.SampleKind = "measurement"; l.OccurredAt = S; l.AckedAt = S; l.Values["fuel_pct"] = n * 1000 + i;
                    run.Recorder.Device(l);
                }
            })).ToList();
            threads.ForEach(t => t.Start());
            foreach (var t in threads) Assert.IsTrue(t.Join(TimeSpan.FromSeconds(20)), "a writer thread finished");
            run.Recorder.Close();

            var data = RecordingData.Load(run.Dir);
            Assert.AreEqual(3200, data.Device.Count);
            for (var i = 1; i < data.Device.Count; i++)
                Assert.GreaterOrEqual(data.Device[i].T, data.Device[i - 1].T, "device.ndjson line " + (i + 1));
        }

        [Test]
        public void TheHeaderRecordsTheRunAndHowItEndedAndTheCostOfRecordingIt()
        {
            using var run = new SyntheticRun();
            run.Recorder.ClockChanged(1.0);
            run.Drive(0, 3.0);
            run.Recorder.ClockChanged(4.0);
            run.T = 3.5;
            run.Recorder.ClockChanged(1.0);
            run.T = 5.0;
            var data = run.Reload();
            var h = data.Header;
            Assert.AreEqual("run-20261006T140000Z", h.RunId);
            Assert.AreEqual(S, h.StartedAtUtc);
            Assert.AreEqual("79bf619b38ea", h.Build.GitSha);
            Assert.AreEqual("sitepulse", h.Instance);
            Assert.AreEqual(SyntheticRun.TruckToken, h.TokenOf(SyntheticRun.Truck));
            Assert.AreEqual(SyntheticRun.Truck, h.IdOf(SyntheticRun.TruckToken));
            Assert.IsTrue(h.EndedCleanly);
            Assert.AreEqual(5.0, h.DurationSeconds, 1e-9);
            Assert.AreEqual(run.Frames.Count, h.SimFrames);
            Assert.AreEqual(new FileInfo(Path.Combine(run.Dir, RecordingFiles.SimBin)).Length, h.SimBytes, "the header's size is the file's");
            Assert.GreaterOrEqual(h.WriteMillisTotal, 0.0);
            Assert.AreEqual(3, h.Clock.Count, "real, accelerated, real again");
            Assert.AreEqual(ClockSegment.Accelerated, h.Clock[1].Mode);
            Assert.IsTrue(h.IsAccelerated(3.2));
            Assert.IsFalse(h.IsAccelerated(1.0));
            Assert.IsFalse(h.IsAccelerated(4.0));
            Assert.IsTrue(h.AnyAccelerated(2.0, 3.2), "a shot that reaches into the fast stretch is a fast shot");
            Assert.IsFalse(h.AnyAccelerated(0, 2.0));
            Assert.IsNull(h.ClockOffsetMs, "this build does not measure the host-to-cluster offset, and says so rather than writing zero");
        }

        [Test]
        public void ARunThatWasNeverClosedSaysSoAndPlaysWithAWarning()
        {
            using var run = new SyntheticRun();
            run.Drive(0, 1.0);
            // the recorder is not closed: the player was killed. The files are flushed as they go.
            run.Recorder.Flush();
            var copy = Path.Combine(run.Base, "copy");
            Directory.CreateDirectory(copy);
            foreach (var f in Directory.GetFiles(run.Dir))
            {
                using var from = new FileStream(f, FileMode.Open, FileAccess.Read, FileShare.ReadWrite);
                using var to = File.Create(Path.Combine(copy, Path.GetFileName(f)));
                from.CopyTo(to);
            }

            var data = RecordingData.Load(copy);
            Assert.IsFalse(data.Header.EndedCleanly);
            Assert.IsTrue(data.Warnings.Any(w => w.Contains("did not end cleanly")));
        }

        [Test]
        public void ARunIsNeverWrittenOverAnExistingOne()
        {
            using var run = new SyntheticRun();
            Assert.Throws<IOException>(() => RunRecorder.Create(run.Base, new RunHeader { RunId = "run-20261006T140000Z", StartedAtUtc = S },
                new[] { new SimMachine("a", SimKind.Dozer) }));
        }

        // ---- sim.bin's layout

        [Test]
        public void SimBinHasTheDocumentedLayout()
        {
            using var run = new SyntheticRun();
            run.Drive(0, 0.5);
            run.Recorder.Close();
            var bytes = File.ReadAllBytes(Path.Combine(run.Dir, RecordingFiles.SimBin));
            Assert.AreEqual("SPSB", Encoding.ASCII.GetString(bytes, 0, 4));
            Assert.AreEqual(1, BitConverter.ToUInt16(bytes, 4), "version");
            Assert.AreEqual(0, BitConverter.ToUInt16(bytes, 6));
            Assert.AreEqual(2u, BitConverter.ToUInt32(bytes, 8), "machines");
            Assert.AreEqual(8u + 2u * 64u, BitConverter.ToUInt32(bytes, 12), "bytes per frame: a time and 64 per machine");
            Assert.AreEqual(20.0, BitConverter.ToDouble(bytes, 16), "nominal rate");
            var table = 24 + (2 + SyntheticRun.Truck.Length) + (2 + SyntheticRun.Loader.Length);
            Assert.AreEqual((byte)SimKind.Hauler, bytes[24]);
            Assert.AreEqual((byte)SyntheticRun.Truck.Length, bytes[25]);
            Assert.AreEqual(SyntheticRun.Truck, Encoding.UTF8.GetString(bytes, 26, SyntheticRun.Truck.Length));
            Assert.AreEqual(table + run.Frames.Count * 136, bytes.Length, "the file is the header and a whole number of frames");
            // the first frame's first machine, field by field
            var o = table + 8;
            Assert.AreEqual(100f, BitConverter.ToSingle(bytes, o), "x");
            Assert.AreEqual(12.5f, BitConverter.ToSingle(bytes, o + 4), "y");
            Assert.AreEqual(10f, BitConverter.ToSingle(bytes, o + 8), "z");
            Assert.AreEqual(90f, BitConverter.ToSingle(bytes, o + 12), "heading");
            Assert.AreEqual(3f, BitConverter.ToSingle(bytes, o + 24), "p1");
            Assert.AreEqual(50f, BitConverter.ToSingle(bytes, o + 40), "fuel");
            Assert.AreEqual(3, bytes[o + 62], "flags: loaded (1) and on track (2)");
            Assert.AreEqual(0, bytes[o + 63], "the last byte is zero");
        }

        [Test]
        public void ATruncatedSimBinIsRefusedNotReadAsFarAsItGoes()
        {
            using var run = new SyntheticRun();
            run.Drive(0, 1.0);
            run.Recorder.Close();
            var bytes = File.ReadAllBytes(Path.Combine(run.Dir, RecordingFiles.SimBin));
            var ex = Assert.Throws<RecordingFormatException>(() => SimBinReader.Parse(bytes.Take(bytes.Length - 1).ToArray()));
            StringAssert.Contains("cut short", ex.Message);
            Assert.Throws<RecordingFormatException>(() => SimBinReader.Parse(bytes.Take(bytes.Length - 136 - 40).ToArray()), "a frame and a bit missing");
            Assert.Throws<RecordingFormatException>(() => SimBinReader.Parse(bytes.Take(20).ToArray()), "cut inside the header");
            Assert.Throws<RecordingFormatException>(() => SimBinReader.Parse(bytes.Take(27).ToArray()), "cut inside the machine table");
            Assert.Throws<RecordingFormatException>(() => SimBinReader.Parse(new byte[0]));
            // a whole number of frames fewer is a shorter run, which is a different thing and is read
            var shorter = SimBinReader.Parse(bytes.Take(bytes.Length - 136).ToArray());
            Assert.AreEqual(run.Frames.Count - 1, shorter.FrameCount);
            // and the whole directory is refused, with the file named, when its sim.bin is cut
            File.WriteAllBytes(Path.Combine(run.Dir, RecordingFiles.SimBin), bytes.Take(bytes.Length - 5).ToArray());
            var dirEx = Assert.Throws<RecordingFormatException>(() => RecordingData.Load(run.Dir));
            StringAssert.Contains("cut short", dirEx.Message);
        }

        [Test]
        public void SimBinOfAnotherVersionOrNoMarkerOrAnotherLayoutIsRefused()
        {
            using var run = new SyntheticRun();
            run.Drive(0, 0.2);
            run.Recorder.Close();
            var good = File.ReadAllBytes(Path.Combine(run.Dir, RecordingFiles.SimBin));
            Assert.DoesNotThrow(() => SimBinReader.Parse(good));

            var v2 = (byte[])good.Clone();
            v2[4] = 2;
            StringAssert.Contains("version 2", Assert.Throws<RecordingFormatException>(() => SimBinReader.Parse(v2)).Message);

            var marker = (byte[])good.Clone();
            marker[0] = (byte)'X';
            StringAssert.Contains("SPSB", Assert.Throws<RecordingFormatException>(() => SimBinReader.Parse(marker)).Message);

            var layout = (byte[])good.Clone();
            layout[12] = 100;
            StringAssert.Contains("not this layout", Assert.Throws<RecordingFormatException>(() => SimBinReader.Parse(layout)).Message);

            var kind = (byte[])good.Clone();
            kind[24] = 9;
            Assert.Throws<RecordingFormatException>(() => SimBinReader.Parse(kind), "an unknown machine kind");

            var back = (byte[])good.Clone();
            BitConverter.GetBytes(99.0).CopyTo(back, back.Length - 136 * 2 + 0);   // the second last frame's time moves after the last's
            StringAssert.Contains("back in time", Assert.Throws<RecordingFormatException>(() => SimBinReader.Parse(back)).Message);
        }

        [Test]
        public void TheWriterRefusesATimeThatGoesBackwardsAndAFrameOfTheWrongSize()
        {
            using var ms = new MemoryStream();
            using var w = new SimBinWriter(ms, new[] { new SimMachine("a", SimKind.Dozer) }, 20.0);
            w.WriteFrame(1.0, new[] { new MachineSample() });
            Assert.Throws<ArgumentException>(() => w.WriteFrame(0.5, new[] { new MachineSample() }));
            Assert.Throws<ArgumentException>(() => w.WriteFrame(2.0, new[] { new MachineSample(), new MachineSample() }));
            Assert.AreEqual(1, w.Frames);
        }

        [Test]
        public void TheSizeOfARunIsWhatTheLayoutSays()
        {
            // 18 machines for a minute at 20 Hz: the file is exactly its header and 1200 frames of 8 + 18 x 64 bytes
            var table = Enumerable.Range(0, 18).Select(i => new SimMachine("SP-M-" + i.ToString("0000"), SimKind.Hauler)).ToList();
            using var ms = new MemoryStream();
            var frame = new MachineSample[18];
            long length, written;
            using (var w = new SimBinWriter(ms, table, 20.0))
            {
                for (var i = 0; i < 1200; i++) w.WriteFrame(i / 20.0, frame);
                Assert.AreEqual(1200, w.Frames);
                length = ms.Length;
                written = w.BytesWritten;
            }

            Assert.AreEqual(length, written, "the writer's own count is the stream's");
            Assert.AreEqual(1160, 8 + 18 * 64, "bytes a frame");
            var header = 24 + table.Sum(m => 2 + m.Id.Length);
            Assert.AreEqual(header + 1200L * 1160, length);
        }

        [Test]
        public void PosesInterpolateBetweenFramesTheShortWayRoundAndHoldAcrossAGap()
        {
            using var run = new SyntheticRun();
            run.Frame(0.0, SyntheticRun.Sample(0, 0, 350f, 50f), SyntheticRun.Sample(0, 0, 0, 70f));
            run.Frame(0.1, SyntheticRun.Sample(10, 20, 10f, 40f, mode: 1, phase: 2), SyntheticRun.Sample(0, 0, 0, 70f));
            run.Frame(5.0, SyntheticRun.Sample(99, 99, 99f, 30f), SyntheticRun.Sample(0, 0, 0, 70f));
            var sim = run.Reload().Sim;
            var mid = sim.Sample(0.05, 0);
            Assert.AreEqual(5f, mid.X, 1e-4); Assert.AreEqual(10f, mid.Z, 1e-4);
            Assert.AreEqual(360f, mid.Heading, 1e-3, "350 to 10 goes through north, not round the long way (180)");
            Assert.AreEqual(45f, mid.FuelPct, 1e-4);
            Assert.AreEqual(0, mid.Mode, "what a machine was doing is as of the earlier frame, never blended");
            var after = sim.Sample(0.1, 0);
            Assert.AreEqual(1, after.Mode);
            Assert.AreEqual(10f, after.X, 1e-4);
            var held = sim.Sample(2.0, 0);
            Assert.AreEqual(10f, held.X, "the recorder was not running for 4.9 s: the machine is held where it was, not slid across the gap");
            Assert.AreEqual(0f, sim.Sample(-3.0, 0).X, "before the first frame, the first");
            Assert.AreEqual(99f, sim.Sample(60.0, 0).X, "after the last, the last");
            Assert.IsTrue(sim.TryIndexOf(SyntheticRun.Loader, out var i));
            Assert.AreEqual(1, i);
            Assert.IsFalse(sim.TryIndexOf("SP-NOPE", out _));
        }

        // ---- the logs

        [Test]
        public void ALastLineCutMidWriteIsDroppedWithAWarningButABrokenLineBeforeItIsRefused()
        {
            using var run = new SyntheticRun();
            run.Drive(0, 0.5);
            run.Obs(0.1, SyntheticRun.Measurement(SyntheticRun.TruckToken, "fuel_pct", 40, S, S));
            run.Obs(0.2, SyntheticRun.Measurement(SyntheticRun.TruckToken, "fuel_pct", 39, S.AddSeconds(1), S.AddSeconds(1)));
            run.Recorder.Close();
            var path = Path.Combine(run.Dir, RecordingFiles.Observed);
            var text = File.ReadAllText(path);
            File.WriteAllText(path, text.TrimEnd('\n').Substring(0, text.TrimEnd('\n').Length - 12));   // no newline, and half a line
            var data = RecordingData.Load(run.Dir);
            Assert.AreEqual(1, data.Observed.Count);
            Assert.IsTrue(data.Warnings.Any(w => w.Contains("partial line")), string.Join("; ", data.Warnings));

            File.WriteAllText(path, "{\"t\":0.1,\n" + text);   // a broken line that is not the last
            var ex = Assert.Throws<RecordingFormatException>(() => RecordingData.Load(run.Dir));
            StringAssert.Contains("observed.ndjson line 1", ex.Message);
        }

        [Test]
        public void ARecordingMissingAFileOrOfAnotherVersionIsRefused()
        {
            using var run = new SyntheticRun();
            run.Drive(0, 0.2);
            run.Recorder.Close();
            var json = File.ReadAllText(Path.Combine(run.Dir, RecordingFiles.RunJson));
            File.WriteAllText(Path.Combine(run.Dir, RecordingFiles.RunJson), json.Replace("\"formatVersion\": 1", "\"formatVersion\": 7"));
            StringAssert.Contains("version 7", Assert.Throws<RecordingFormatException>(() => RecordingData.Load(run.Dir)).Message);
            File.WriteAllText(Path.Combine(run.Dir, RecordingFiles.RunJson), json);
            File.Delete(Path.Combine(run.Dir, RecordingFiles.Device));
            StringAssert.Contains("device.ndjson", Assert.Throws<RecordingFormatException>(() => RecordingData.Load(run.Dir)).Message);
            Assert.Throws<RecordingFormatException>(() => RecordingData.Load(Path.Combine(run.Base, "nowhere")));
        }

        [Test]
        public void ALineWithATimeGoingBackIsRefused()
        {
            using var run = new SyntheticRun();
            run.Drive(0, 0.2);
            run.Obs(1.0, SyntheticRun.Measurement(SyntheticRun.TruckToken, "fuel_pct", 40, S, S));
            run.Obs(0.5, SyntheticRun.Measurement(SyntheticRun.TruckToken, "fuel_pct", 39, S, S));
            run.Recorder.Close();
            StringAssert.Contains("back in time", Assert.Throws<RecordingFormatException>(() => RecordingData.Load(run.Dir)).Message);
        }

        // ---- no credential is ever written

        static readonly Regex Hex32 = new Regex("[0-9A-Fa-f]{32,}");
        static readonly Regex JwtShape = new Regex(@"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*");

        [Test]
        public void NoCredentialOrTokenReachesAnyFileOfARecording()
        {
            const string credential = "0123456789abcdef0123456789abcdef";
            const string jwt = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ4IiwiZXhwIjo5OTk5OTk5OTk5fQ.c2lnbmF0dXJlc2lnbmF0dXJl";
            using var run = new SyntheticRun(h =>
            {
                h.Instance = "inst-" + credential;
                h.PlatformVersion = "v0.19.0 " + jwt;
                h.Build.GitSha = credential + "ff";
            });
            run.Drive(0, 0.5);
            run.Dev(0.1, SyntheticRun.Row(SyntheticRun.Truck, "received", "goto-refuel with credential " + credential + " and token " + jwt));
            var refused = DeviceLine.Of(DeviceKinds.CommandRefused, SyntheticRun.Truck);
            refused.Text = "bad " + credential;
            run.Dev(0.2, refused);
            var done = DeviceLine.Of(DeviceKinds.TaskCompleted, SyntheticRun.Truck);
            done.Token = credential; done.Reason = jwt;
            run.Dev(0.3, done);
            var st = SyntheticRun.Status("alarms", "Reconnecting", S);
            st.Reason = "401 for " + jwt + " " + credential;
            run.Obs(0.4, st);
            var cmd = SyntheticRun.Command(SyntheticRun.TruckToken, credential, "goto-refuel", "SENT", S, S);
            run.Obs(0.5, cmd);
            run.Pres(0.6, PresenterLine.Of(PresenterKinds.Action, "typed " + credential));
            run.Recorder.Close();

            var scanned = 0;
            foreach (var file in Directory.GetFiles(run.Dir).Where(f => !f.EndsWith(RecordingFiles.SimBin)))
            {
                var text = File.ReadAllText(file);
                scanned++;
                Assert.IsFalse(Hex32.IsMatch(text), Path.GetFileName(file) + " holds a 32-hex run");
                Assert.IsFalse(JwtShape.IsMatch(text), Path.GetFileName(file) + " holds a JWT");
                Assert.IsFalse(text.Contains("c2lnbmF0dXJl"), Path.GetFileName(file) + " holds a JWT's signature");
            }

            Assert.AreEqual(4, scanned, "run.json and the three logs");
            // a credential is written as a fingerprint of itself (never its tail): two tokens that share their last four stay two
            var expected = "hex:" + Fingerprint12(credential);
            var deviceLog = File.ReadAllText(Path.Combine(run.Dir, RecordingFiles.Device));
            StringAssert.Contains(expected, deviceLog);
            StringAssert.DoesNotContain("cred:", deviceLog, "the log's own spelling is not the recording's");
            StringAssert.DoesNotContain("cdef\"", deviceLog.Replace(expected, ""), "and no tail of it is left");
            StringAssert.Contains("jwt:", deviceLog);
            Assert.AreEqual("cred:…cdef", Redactor.Redact(credential), "log output is unchanged");
            // what the header says is still the header (the run's id is not a credential and is untouched)
            Assert.AreEqual("run-20261006T140000Z", RunHeader.Parse(File.ReadAllText(Path.Combine(run.Dir, RecordingFiles.RunJson))).RunId);
        }

        static string Fingerprint12(string run)
        {
            using var sha = System.Security.Cryptography.SHA256.Create();
            var hash = sha.ComputeHash(Encoding.UTF8.GetBytes(run));
            return string.Concat(hash.Take(6).Select(b => b.ToString("x2")));
        }

        [Test]
        public void ARecordingFingerprintsAHexRunSoDistinctTokensStayDistinctAndNothingIsWrittenOfThem()
        {
            const string a = "0123456789abcdef0123456789abcdef", b = "fedcba9876543210fedcba9876abcdef";   // both end in cdef
            var ra = Redactor.RedactForRecording("token " + a);
            var rb = Redactor.RedactForRecording("token " + b);
            Assert.AreEqual("token hex:" + Fingerprint12(a), ra);
            Assert.AreNotEqual(ra, rb, "the same last four characters, and still two tokens");
            Assert.AreEqual(ra, Redactor.RedactForRecording("token " + a), "stable: the same run is the same fingerprint every time");
            Assert.AreEqual(ra, Redactor.RedactForRecording(ra), "and it is not redacted again");
            Assert.AreEqual("hex:" + Fingerprint12(a + "00"), Redactor.RedactForRecording(a + "00"), "a longer run is one run");
            Assert.AreEqual("abc-0123456789abcdef0123456789abcde", Redactor.RedactForRecording("abc-0123456789abcdef0123456789abcde"), "31 hex characters is not a credential");
            var jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl";
            StringAssert.StartsWith("jwt:", Redactor.RedactForRecording(jwt));
            Assert.AreEqual(Redactor.Redact(jwt), Redactor.RedactForRecording(jwt), "a JWT is fingerprinted as in a log");
        }

        [Test]
        public void ARunIdIsNotMistakenForACredential()
        {
            var id = RunRecorder.NewRunId(S);
            Assert.AreEqual("run-20261006T140000Z", id);
            Assert.AreEqual(id, Redactor.Redact(id));
        }

        [Test]
        public void AClosedRecorderIgnoresFurtherWritesAndSaysNothingWentWrong()
        {
            using var run = new SyntheticRun();
            string fault = null;
            run.Recorder.Faulted += why => fault = why;
            run.Drive(0, 0.2);
            var before = run.Recorder.Stats.SimFrames;
            run.Recorder.Close();
            run.Recorder.WriteSimFrame(99.0, new[] { new MachineSample(), new MachineSample() });
            run.Recorder.Observed(SyntheticRun.Measurement(SyntheticRun.TruckToken, "fuel_pct", 1, S, S));
            Assert.AreEqual(before, run.Recorder.Stats.SimFrames);
            Assert.AreEqual(0, run.Recorder.Stats.ObservedLines);
            Assert.IsNull(fault, "closing is not a fault");
            Assert.IsFalse(run.Recorder.IsFaulted);
        }
        // ---- the clock of a run that was killed

        static RunHeader OnDisk(string dir) => RunHeader.Parse(File.ReadAllText(Path.Combine(dir, RecordingFiles.RunJson)));

        [Test]
        public void TheClockIsOnDiskAsSoonAsItChangesNotOnlyWhenTheRunEnds()
        {
            using var run = new SyntheticRun();
            run.T = 0.0; run.Recorder.ClockChanged(1.0);
            run.Drive(0, 1.0);
            Assert.AreEqual(1, OnDisk(run.Dir).Clock.Count);
            run.T = 2.0; run.Recorder.ClockChanged(8.0);
            var live = OnDisk(run.Dir);
            Assert.IsFalse(live.EndedCleanly, "the run is still going");
            Assert.AreEqual(2, live.Clock.Count, "the speed-up is in run.json now: nothing has been closed");
            Assert.AreEqual(ClockSegment.Accelerated, live.Clock[1].Mode);
            Assert.AreEqual(2.0, live.Clock[1].From, 1e-9);
            Assert.AreEqual(8.0, live.Clock[1].Scale);
            Assert.IsFalse(File.Exists(Path.Combine(run.Dir, RecordingFiles.RunJson + ".tmp")), "written beside and moved into place, never left half-written");
        }

        [Test]
        public void AClockChangeAfterTheRunEndedDoesNotTakeTheEndSummaryBack()
        {
            using var run = new SyntheticRun();
            run.Drive(0, 1.0);
            run.Recorder.Close();
            run.Recorder.ClockChanged(8.0);
            var h = OnDisk(run.Dir);
            Assert.IsTrue(h.EndedCleanly, "a closed recording is not reopened by a late change of clock");
            Assert.AreEqual(0, h.Clock.Count);
        }

        [Test]
        public void ARunKilledAfterTheClockSpedUpIsStillReadAsAcceleratedFromThatMoment()
        {
            using var run = new SyntheticRun();
            run.T = 0.0; run.Recorder.ClockChanged(1.0);
            run.Drive(0, 5.0);
            run.T = 2.0; run.Recorder.ClockChanged(8.0);
            var data = RecordingData.Load(run.CopyAsKilled());
            Assert.IsFalse(data.Header.EndedCleanly);
            Assert.IsTrue(data.Header.IsAccelerated(3.0));
            Assert.IsFalse(data.Header.IsAccelerated(1.0));
            Assert.AreEqual(RunHeader.ClockRebuilt, data.Header.ClockBasis);
        }

        [Test]
        public void ARunWhoseHeaderLostTheClockHasItRebuiltFromThePresenterLog()
        {
            using var run = new SyntheticRun();
            run.T = 0.0; run.Recorder.ClockChanged(1.0);
            run.Drive(0, 5.0);
            run.T = 2.0; run.Recorder.ClockChanged(8.0);
            run.T = 4.0; run.Recorder.ClockChanged(1.0);
            var killed = run.CopyAsKilled();
            // the header the player was killed before it could rewrite: it knows nothing of the clock
            var bare = OnDisk(killed);
            bare.Clock.Clear();
            File.WriteAllText(Path.Combine(killed, RecordingFiles.RunJson), bare.ToJson());
            var data = RecordingData.Load(killed);
            CollectionAssert.AreEqual(new[] { ClockSegment.Real, ClockSegment.Accelerated, ClockSegment.Real }, data.Header.Clock.Select(c => c.Mode).ToList());
            Assert.AreEqual(2.0, data.Header.Clock[1].From, 1e-9);
            Assert.AreEqual(8.0, data.Header.Clock[1].Scale);
            Assert.IsTrue(data.Header.IsAccelerated(3.0));
            Assert.IsFalse(data.Header.IsAccelerated(4.5));
            Assert.AreEqual(RunHeader.ClockRebuilt, data.Header.ClockBasis);
            Assert.IsTrue(data.Warnings.Any(w => w.Contains("rebuilt from presenter.ndjson")), string.Join("; ", data.Warnings));
        }

        [Test]
        public void ARunThatLeftNoTraceOfItsClockIsAnUnknownClockAndCountsAsAccelerated()
        {
            using var run = new SyntheticRun();
            run.Drive(0, 3.0);
            var data = RecordingData.Load(run.CopyAsKilled());
            Assert.AreEqual(RunHeader.ClockUnknown, data.Header.ClockBasis);
            Assert.IsTrue(data.Header.AnyAccelerated(0.0, 1.0), "unknown is not real time: the caption is required");
            Assert.IsTrue(data.Warnings.Any(w => w.Contains("unknown")));
            // a run that ended cleanly with no change of clock is a real-time run, and says so
            using var clean = new SyntheticRun();
            clean.Drive(0, 3.0);
            var ok = clean.Reload();
            Assert.AreEqual(RunHeader.ClockRecorded, ok.Header.ClockBasis);
            Assert.IsFalse(ok.Header.AnyAccelerated(0.0, 3.0));
        }

        // ---- a disk that fails

        [Test]
        public void ADiskThatFailsMidRunStopsTheRecordingOnceAndTheRunCarriesOn()
        {
            var disk = new FlakyDisk();
            using var run = new SyntheticRun(openStream: disk.Open);
            var faults = new List<string>();
            run.Recorder.Faulted += faults.Add;
            run.Drive(0, 0.5);
            run.Obs(0.6, SyntheticRun.Measurement(SyntheticRun.TruckToken, "fuel_pct", 40, S, S));
            var frames = run.Recorder.Stats.SimFrames;
            Assert.AreEqual(11, frames);
            disk.Failure = new IOException("disk full");

            Assert.DoesNotThrow(() => run.Frame(1.0, SyntheticRun.Sample(1, 1, 1), SyntheticRun.Sample(2, 2, 2)));
            Assert.IsTrue(run.Recorder.IsFaulted);
            Assert.AreEqual(1, faults.Count);
            StringAssert.Contains("disk full", faults[0]);

            Assert.DoesNotThrow(() =>
            {
                run.Frame(1.1, SyntheticRun.Sample(1, 1, 1), SyntheticRun.Sample(2, 2, 2));
                run.Obs(1.2, SyntheticRun.Measurement(SyntheticRun.TruckToken, "fuel_pct", 39, S, S));
                run.Dev(1.3, SyntheticRun.Row(SyntheticRun.Truck, "received", "x"));
                run.Pres(1.4, PresenterLine.Of(PresenterKinds.Action, "x"));
                run.Recorder.ClockChanged(8.0);
                run.Recorder.Flush();
            });
            Assert.AreEqual(1, faults.Count, "said once");
            Assert.AreEqual(frames, run.Recorder.Stats.SimFrames, "later writes are ignored");
            Assert.AreEqual(1, run.Recorder.Stats.ObservedLines);
            Assert.AreEqual(0, run.Recorder.Stats.DeviceLines);

            Assert.DoesNotThrow(() => run.Recorder.Close(), "closing a recording on a failed disk is not another failure");
            Assert.AreEqual(1, faults.Count);
            var h = OnDisk(run.Dir);
            Assert.IsFalse(h.EndedCleanly, "run.json says the recording did not end cleanly");
            Assert.AreEqual(frames, h.SimFrames);
            Assert.IsNotNull(h.EndedAtUtc, "and the end summary was still written");
        }

        [Test]
        public void ALogThatFailsStopsTheSimulationFramesToo()
        {
            var disk = new FlakyDisk();
            using var run = new SyntheticRun(openStream: disk.Open);
            var faults = new List<string>();
            run.Recorder.Faulted += faults.Add;
            run.Drive(0, 0.2);
            disk.Failure = new IOException("device not ready");
            Assert.DoesNotThrow(() => run.Obs(0.5, SyntheticRun.Measurement(SyntheticRun.TruckToken, "fuel_pct", 40, S, S)));
            Assert.AreEqual(1, faults.Count);
            var frames = run.Recorder.Stats.SimFrames;
            disk.Failure = null;                                    // the disk comes back: the recording is not resumed, a gap in it would pass for the run
            run.Frame(1.0, SyntheticRun.Sample(1, 1, 1), SyntheticRun.Sample(2, 2, 2));
            Assert.AreEqual(frames, run.Recorder.Stats.SimFrames);
            Assert.AreEqual(1, faults.Count);
            Assert.DoesNotThrow(() => run.Recorder.Close());
            Assert.IsFalse(OnDisk(run.Dir).EndedCleanly);
        }

        // ---- the cursor and the interpolation at their edges

        [Test]
        public void TwoFramesNoFurtherApartThanTheGapAreBlendedAndFurtherApartAreHeld()
        {
            using var run = new SyntheticRun();
            run.Frame(0.0, SyntheticRun.Sample(0, 0, 0, 50f), SyntheticRun.Sample(0, 0, 0, 70f));
            run.Frame(0.45, SyntheticRun.Sample(10, 0, 0, 40f), SyntheticRun.Sample(0, 0, 0, 70f));
            run.Frame(1.05, SyntheticRun.Sample(20, 0, 0, 30f), SyntheticRun.Sample(0, 0, 0, 70f));   // 0.6 s after the one before
            run.Frame(2.0, SyntheticRun.Sample(30, 0, 0, 20f), SyntheticRun.Sample(0, 0, 0, 70f));
            run.Frame(2.5, SyntheticRun.Sample(40, 0, 0, 10f), SyntheticRun.Sample(0, 0, 0, 70f));    // exactly the gap
            var sim = run.Reload().Sim;
            Assert.AreEqual(5f, sim.Sample(0.225, 0).X, 1e-4, "0.45 s apart: blended");
            Assert.AreEqual(10f, sim.Sample(0.75, 0).X, 1e-6, "0.6 s apart: held where it was, not slid");
            Assert.AreEqual(40f, sim.Sample(0.75, 0).FuelPct, 1e-6, "fuel is held with the pose");
            Assert.AreEqual(35f, sim.Sample(2.25, 0).X, 1e-4, "exactly 0.5 s apart still blends: the gap that holds is a longer one");
            Assert.AreEqual(0.5, SimBinReader.MaxInterpolationGap);
        }

        [Test]
        public void WhatAMachineWasDoingIsTheEarlierFramesAndNeverBlended()
        {
            using var run = new SyntheticRun();
            var a = SyntheticRun.Sample(0, 0, 0); a.Loaded = false; a.OnTrack = true; a.Mode = 1; a.Phase = 3;
            var b = SyntheticRun.Sample(10, 0, 0); b.Loaded = true; b.OnTrack = false; b.Mode = 2; b.Phase = 4;
            run.Frame(0.0, a, SyntheticRun.Sample(0, 0, 0));
            run.Frame(0.4, b, SyntheticRun.Sample(0, 0, 0));
            var mid = run.Reload().Sim.Sample(0.2, 0);
            Assert.AreEqual(5f, mid.X, 1e-4);
            Assert.AreEqual((byte)1, mid.Mode); Assert.AreEqual((byte)3, mid.Phase);
            Assert.IsFalse(mid.Loaded); Assert.IsTrue(mid.OnTrack);
        }
    }

    public sealed class RecordingMapTests
    {
        static readonly DateTimeOffset T0 = SyntheticRun.Start;

        [Test]
        public void EveryObserverItemBecomesTheLineThatSaysWhatItWas()
        {
            var m = RecordingMaps.Observed(new MeasurementItem("sp-hauler-06", "fuel_pct", 14.8, T0, T0.AddMilliseconds(90), true));
            Assert.AreEqual(ObservedKinds.Measurement, m.K);
            Assert.AreEqual(14.8, m.Value);
            Assert.AreEqual(T0, m.OccurredAt);
            Assert.IsTrue(m.FromSnapshot);

            var alarm = new ObservedAlarm { Token = "al", AlarmKey = "low-fuel", MetricKey = "fuel_pct", State = "ACTIVE", Severity = "MAJOR", OccurredAt = T0, ObservedAt = T0.AddSeconds(1) };
            var a = RecordingMaps.Observed(new AlarmItem("sp-hauler-06", alarm));
            Assert.AreEqual(ObservedKinds.Alarm, a.K);
            Assert.AreEqual("low-fuel", a.Name);
            Assert.AreEqual("MAJOR", a.Severity);

            var snap = RecordingMaps.Observed(new AlarmSnapshotItem(new[] { new AlarmItem("sp-hauler-06", alarm) }, T0, T0.AddSeconds(2), true, 9));
            Assert.AreEqual(ObservedKinds.AlarmSnapshot, snap.K);
            Assert.AreEqual(1, snap.Alarms.Count);
            Assert.IsTrue(snap.Truncated);
            Assert.AreEqual(9, snap.TotalRecords);

            var loc = RecordingMaps.Observed(new LocationItem("sp-hauler-06", new ObservedLocation { SpeedMps = 4, HeadingDegrees = 90, ElevationMetres = 1800, OccurredAt = T0, ObservedAt = T0 }));
            Assert.AreEqual(4.0, loc.SpeedMps);

            var cmd = RecordingMaps.Observed(new CommandItem("sp-hauler-06", new ObservedCommand { Token = "c", Name = "goto-refuel", Status = "SENT", QueuedAt = T0, ObservedAt = T0 }));
            Assert.AreEqual("SENT", cmd.State);
            Assert.AreEqual("goto-refuel", cmd.Name);

            var pres = RecordingMaps.Observed(new PresenceItem("sp-hauler-06", new ObservedPresence { Active = true, ObservedAt = T0 }));
            Assert.IsTrue(pres.PresenceActive);

            var st = RecordingMaps.Observed(new StatusItem("measurements", "Live", null, T0));
            Assert.AreEqual("measurements", st.Name);
            Assert.AreEqual("Live", st.State);
        }

        [Test]
        public void ADevicePlaneEventBecomesALineAndACommandCarriesItsTokenKeyPayloadAndSequence()
        {
            var task = new DeviceChain.Sitepulse.Tasks.TaskRequest("cmd-9", "goto-area", "sp-zone-yard", 4, 1);
            var l = RecordingMaps.Device(new DeviceEvent(1, "SP-HL-0003", DeviceEventKind.Task, text: "goto-area sp-zone-yard", task: task));
            Assert.AreEqual(DeviceKinds.TaskReceived, l.K);
            Assert.AreEqual("cmd-9", l.Token);
            Assert.AreEqual("goto-area", l.Key);
            Assert.AreEqual("sp-zone-yard", l.Payload);
            Assert.AreEqual(4, l.Sequence);

            var link = RecordingMaps.Device(new DeviceEvent(1, "SP-HL-0003", DeviceEventKind.LinkState, LinkState.Blind));
            Assert.AreEqual("Blind", link.State);
            Assert.AreEqual(DeviceKinds.CommandRefused, RecordingMaps.Device(new DeviceEvent(1, "SP-HL-0003", DeviceEventKind.CommandRefused, text: "no such place")).K);

            var done = RecordingMaps.Completed("SP-HL-0003", "cmd-9", DeviceChain.Sitepulse.Tasks.TaskResult.Fail("superseded by cmd-10"));
            Assert.IsFalse(done.Succeeded);
            Assert.AreEqual("superseded by cmd-10", done.Reason);
        }

        [Test]
        public void ASampleIsRecordedWithItsOwnTimeAndWhenTheBrokerTookItAndNoNaN()
        {
            var values = new Dictionary<string, double> { ["fuel_pct"] = 55.5, ["engine_temp_c"] = double.NaN };
            var l = RecordingMaps.Sample("SP-HL-0006", Sample.Measurement(1, T0, values), T0.AddMilliseconds(30));
            Assert.AreEqual(T0, l.OccurredAt);
            Assert.AreEqual(T0.AddMilliseconds(30), l.AckedAt);
            Assert.AreEqual(1, l.Values.Count, "a value that is not a number is not a value");
            var loc = RecordingMaps.Sample("SP-HL-0006", Sample.Location(2, T0, new GeoPoint(39.0004, -116.9996), 1800, 5.5, 91), T0);
            Assert.AreEqual("location", loc.SampleKind);
            Assert.AreEqual(5.5, loc.SpeedMps);
            Assert.AreEqual(-116.9996, loc.Longitude);
            // and it survives the line
            using var doc = System.Text.Json.JsonDocument.Parse(loc.ToJson());
            Assert.AreEqual(91.0, DeviceLine.Read(doc.RootElement).HeadingDegrees);
        }

        [Test]
        public void TheRecorderIsOnByDefaultAndTheFlagsSayWhereAndWhetherAndAContradictionIsRefused()
        {
            var on = RecordOptions.Parse(new[] { "Sitepulse.exe" }, out var e0);
            Assert.IsNull(e0);
            Assert.IsTrue(on.Enabled);
            Assert.IsNull(on.Directory);
            var dir = RecordOptions.Parse(new[] { "x", "-sitepulse-record", "D:\\rec", "-sitepulse-platform-version", "v0.19.0" }, out _);
            Assert.AreEqual("D:\\rec", dir.Directory);
            Assert.AreEqual("v0.19.0", dir.PlatformVersion);
            Assert.IsFalse(RecordOptions.Parse(new[] { "x", "-sitepulse-no-record" }, out _).Enabled);
            Assert.IsNull(RecordOptions.Parse(new[] { "x", "-sitepulse-no-record", "-sitepulse-record", "d" }, out var both));
            StringAssert.Contains("contradict", both);
            Assert.IsNull(RecordOptions.Parse(new[] { "x", "-sitepulse-record" }, out var bare));
            StringAssert.Contains("needs a value", bare);
        }
    }
}
