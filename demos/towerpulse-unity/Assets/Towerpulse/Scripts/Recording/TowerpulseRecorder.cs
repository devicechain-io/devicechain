// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Text;
using DeviceChain.Demos.Platform.Recording;
using DeviceChain.Towerpulse.Story;

namespace DeviceChain.Towerpulse.Recording
{
    /// <summary>
    /// Records a live Towerpulse run in the shared recording format, plus the one file only this demo needs. A run folder holds
    ///
    ///   run.json           the shared header (run id, build, platform version, instance, tenant, manifest, seed, devices, clock segments, how it ended)
    ///   towerpulse.json    the model's name, the story clock, the rule and profile versions the day was judged against
    ///   device.ndjson      what the devices SENT: every reading the broker acknowledged, with the values as sent
    ///   observed.ndjson    what the PLATFORM said back: measurements, alarms and commands as its own record shows them
    ///   story.ndjson       the story layer's feed: every fact the boards were told, at the model time it was told
    ///
    /// The four kinds of fact are kept apart on purpose: "the device sent it" and "the platform recorded it" are different claims, and the film is
    /// about the second. A run folder is never overwritten and is closed with an end summary; a recorder that fails says so and stops writing, it never
    /// throws into the frame loop.
    /// </summary>
    public sealed class TowerpulseRecorder : IDisposable
    {
        readonly RunHeader header;
        readonly TowerpulseRunInfo info;
        readonly Func<double> clock;
        readonly Func<DateTimeOffset> utc;
        readonly StreamWriter observed, device, story;
        readonly object gate = new object();
        long observedLines, deviceLines, storyLines;
        string fault;
        bool closed;
        readonly Stopwatch writeCost = new Stopwatch();

        TowerpulseRecorder(string directory, RunHeader header, TowerpulseRunInfo info, StreamWriter observed, StreamWriter device, StreamWriter story, Func<double> clock, Func<DateTimeOffset> utc)
        {
            Directory = directory; this.header = header; this.info = info; this.observed = observed; this.device = device; this.story = story; this.clock = clock; this.utc = utc;
        }

        public string Directory { get; }
        public RunHeader Header => header;
        public TowerpulseRunInfo Info => info;
        public string Fault => fault;
        public long StoryLines { get { lock (gate) return storyLines; } }
        public long DeviceLines { get { lock (gate) return deviceLines; } }
        public long ObservedLines { get { lock (gate) return observedLines; } }
        public double Now => clock();

        public static string NewRunId(DateTimeOffset at) => "run-" + at.UtcDateTime.ToString("yyyyMMdd'T'HHmmss'Z'", CultureInfo.InvariantCulture);

        public static TowerpulseRecorder Create(string baseDirectory, RunHeader header, TowerpulseRunInfo info, Func<double> clock = null, Func<DateTimeOffset> utc = null)
        {
            if (string.IsNullOrEmpty(baseDirectory)) throw new ArgumentException("a recording needs a directory", nameof(baseDirectory));
            if (header == null) throw new ArgumentNullException(nameof(header));
            if (info == null) throw new ArgumentNullException(nameof(info));
            utc ??= () => DateTimeOffset.UtcNow;
            if (string.IsNullOrEmpty(header.RunId)) { header.StartedAtUtc = utc(); header.RunId = NewRunId(header.StartedAtUtc); }
            else if (header.StartedAtUtc == default) header.StartedAtUtc = utc();
            header.Manifest ??= info.ManifestId;
            header.Seed ??= info.Seed.ToString(CultureInfo.InvariantCulture);
            if (header.Clock.Count == 0)
                header.Clock.Add(new ClockSegment { From = 0, Mode = info.Speed > 1.0000001 ? ClockSegment.Accelerated : ClockSegment.Real, Scale = info.Speed });

            var dir = Path.Combine(baseDirectory, header.RunId);
            if (System.IO.Directory.Exists(dir) && System.IO.Directory.GetFileSystemEntries(dir).Length > 0)
                throw new IOException("the recording directory " + dir + " already exists and is not empty: a run is never overwritten");
            System.IO.Directory.CreateDirectory(dir);

            var watch = Stopwatch.StartNew();
            clock ??= () => watch.Elapsed.TotalSeconds;
            StreamWriter o = null, d = null, s = null;
            try
            {
                o = Open(Path.Combine(dir, RecordingFiles.Observed));
                d = Open(Path.Combine(dir, RecordingFiles.Device));
                s = Open(Path.Combine(dir, TowerpulseFiles.Story));
                var r = new TowerpulseRecorder(dir, header, info, o, d, s, clock, utc);
                r.WriteHeaders();
                return r;
            }
            catch
            {
                o?.Dispose(); d?.Dispose(); s?.Dispose();
                throw;
            }
        }

        static StreamWriter Open(string path) =>
            new StreamWriter(new FileStream(path, FileMode.CreateNew, FileAccess.Write, FileShare.Read, 1 << 16), new UTF8Encoding(false)) { AutoFlush = true, NewLine = "\n" };

        void WriteHeaders()
        {
            lock (gate)
            {
                header.EndedCleanly = false;
                header.EndedAtUtc = null;
                AtomicWrite(Path.Combine(Directory, RecordingFiles.RunJson), header.ToJson());
                AtomicWrite(Path.Combine(Directory, TowerpulseFiles.Info), info.ToJson(header));
            }
        }

        static void AtomicWrite(string path, string text)
        {
            var tmp = path + ".tmp";
            File.WriteAllText(tmp, text, new UTF8Encoding(false));
            if (File.Exists(path)) File.Replace(tmp, path, null);
            else File.Move(tmp, path);
        }

        /// <summary>Notes a device for the header: its external id (what the story names), its platform token and its kind.</summary>
        public void AddDevice(string externalId, string token, string kind)
        {
            lock (gate)
            {
                if (closed) return;
                foreach (var d in header.Devices) if (d.Id == externalId) return;
                header.Devices.Add(new RunDevice { Id = externalId, Token = token, Kind = kind });
            }
        }

        /// <summary>Writes the header again (after devices were added or the platform's versions were learned).</summary>
        public void RewriteHeaders()
        {
            try { WriteHeaders(); }
            catch (Exception e) when (e is IOException || e is UnauthorizedAccessException) { Fail(e); }
        }

        void Fail(Exception e)
        {
            lock (gate) { if (fault == null) fault = e.GetType().Name + ": " + e.Message; }
        }

        void Append(StreamWriter file, string text, ref long counter)
        {
            lock (gate)
            {
                if (fault != null || closed) return;
                try
                {
                    writeCost.Start();
                    file.WriteLine(text);
                    counter++;
                }
                catch (Exception e) when (e is IOException || e is UnauthorizedAccessException || e is ObjectDisposedException) { Fail(e); }
                finally { writeCost.Stop(); }
            }
        }

        /// <summary>A reading the broker acknowledged. <paramref name="occurred"/> is what the reading was stamped with; <paramref name="acked"/> when the acknowledgement came.</summary>
        public void Sample(string externalId, IReadOnlyDictionary<string, double> values, DateTimeOffset occurred, DateTimeOffset acked)
        {
            var line = DeviceLine.Of(DeviceKinds.Sample, externalId);
            line.SampleKind = "measurement";
            line.OccurredAt = occurred;
            line.AckedAt = acked;
            foreach (var kv in values) line.Values[kv.Key] = kv.Value;
            lock (gate)
            {
                line.T = Now; line.Utc = utc();
                Append(device, line.ToJson(), ref deviceLines);
            }
        }

        /// <summary>One thing the platform's observer reported (already converted to the shared line type).</summary>
        public void Observe(ObservedLine line)
        {
            lock (gate)
            {
                line.T = Now; line.Utc = utc();
                Append(observed, line.ToJson(), ref observedLines);
            }
        }

        /// <summary>One fact for the story layer. The caller applies the same fact to its boards; this only keeps it.</summary>
        public void Fact(StoryFact f)
        {
            lock (gate) Append(story, StoryLine.ToJson(Now, f), ref storyLines);
        }

        /// <summary>The story clock's speed changed at this moment of the run.</summary>
        public void ClockChanged(double scale)
        {
            var mode = Math.Abs(scale - 1.0) < 1e-6 ? ClockSegment.Real : ClockSegment.Accelerated;
            lock (gate)
            {
                var last = header.Clock.Count > 0 ? header.Clock[header.Clock.Count - 1] : null;
                if (last != null && last.Mode == mode && Math.Abs(last.Scale - scale) < 1e-6) return;
                header.Clock.Add(new ClockSegment { From = Now, Mode = mode, Scale = scale });
            }
            RewriteHeaders();
        }

        public void Close()
        {
            lock (gate)
            {
                if (closed) return;
                closed = true;
                try { observed.Dispose(); device.Dispose(); story.Dispose(); }
                catch (Exception e) when (e is IOException || e is ObjectDisposedException) { if (fault == null) fault = e.GetType().Name + ": " + e.Message; }
                header.EndedAtUtc = utc();
                header.EndedCleanly = fault == null;
                header.DurationSeconds = Now;
                header.ObservedLines = observedLines;
                header.DeviceLines = deviceLines;
                header.PresenterLines = storyLines;
                header.WriteMillisTotal = writeCost.Elapsed.TotalMilliseconds;
                try
                {
                    AtomicWrite(Path.Combine(Directory, RecordingFiles.RunJson), header.ToJson());
                    AtomicWrite(Path.Combine(Directory, TowerpulseFiles.Info), info.ToJson(header));
                }
                catch (Exception e) when (e is IOException || e is UnauthorizedAccessException) { fault ??= e.GetType().Name + ": " + e.Message; }
            }
        }

        public void Dispose() => Close();
    }
}
