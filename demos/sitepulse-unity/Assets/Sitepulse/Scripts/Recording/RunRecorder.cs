// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Text;

namespace DeviceChain.Sitepulse.Recording
{
    /// <summary>What a recording has cost so far: the bytes it has written and the time the writing took.</summary>
    public sealed class RecorderStats
    {
        public int SimFrames { get; internal set; }
        public long SimBytes { get; internal set; }
        public long ObservedLines { get; internal set; }
        public long DeviceLines { get; internal set; }
        public long PresenterLines { get; internal set; }
        public long NdjsonBytes { get; internal set; }
        public double WriteMillisTotal { get; internal set; }
        public double WriteMillisWorstFrame { get; internal set; }

        public long TotalBytes => SimBytes + NdjsonBytes;

        public override string ToString() =>
            string.Format(CultureInfo.InvariantCulture,
                "{0} sim frames ({1:0.0} KiB), {2} observed / {3} device / {4} presenter lines ({5:0.0} KiB), write cost {6:0.0} ms total, worst frame {7:0.00} ms",
                SimFrames, SimBytes / 1024.0, ObservedLines, DeviceLines, PresenterLines, NdjsonBytes / 1024.0, WriteMillisTotal, WriteMillisWorstFrame);
    }

    /// <summary>
    /// Records a live run into <c>&lt;base&gt;/&lt;runId&gt;/</c>: run.json (header, rewritten when the run ends), sim.bin, and the
    /// three ndjson logs. The recorder is the app's passenger: a disk that fills or a file that cannot be written stops the
    /// recording (and says so once, through <see cref="Faulted"/>) but never raises into the run. The simulation frames are
    /// written on the calling thread as one write each; the ndjson logs may be written from any thread. Memory does not grow
    /// with the run: there is one frame buffer and nothing is kept.
    /// </summary>
    public sealed class RunRecorder : IDisposable
    {
        public const double NominalHz = 20.0;
        static readonly TimeSpan FlushEvery = TimeSpan.FromSeconds(1);

        readonly RunHeader header;
        readonly Func<double> clock;
        readonly Func<DateTimeOffset> utc;
        readonly SimBinWriter sim;
        readonly NdjsonFile observed, device, presenter;
        readonly RecorderStats stats = new RecorderStats();
        readonly object statsGate = new object();
        readonly object closeGate = new object();
        readonly Stopwatch flushTimer = Stopwatch.StartNew();
        string fault;
        bool closed;

        RunRecorder(string directory, RunHeader header, SimBinWriter sim, NdjsonFile observed, NdjsonFile device, NdjsonFile presenter, Func<double> clock, Func<DateTimeOffset> utc)
        {
            Directory = directory;
            this.header = header;
            this.sim = sim;
            this.observed = observed;
            this.device = device;
            this.presenter = presenter;
            this.clock = clock;
            this.utc = utc;
        }

        public string Directory { get; }
        public string RunId => header.RunId;
        public RunHeader Header => header;
        public RecorderStats Stats => stats;

        /// <summary>Raised once, with the reason, when the recording stopped because the disk refused a write.</summary>
        public event Action<string> Faulted;

        public bool IsFaulted => fault != null;

        /// <summary>Seconds since the recording began, on a monotonic clock.</summary>
        public double Now => clock();

        public DateTimeOffset UtcNow => utc();

        /// <summary>A fresh run id: sortable, and with nothing in it a redaction rule would take for a credential.</summary>
        public static string NewRunId(DateTimeOffset at) => "run-" + at.UtcDateTime.ToString("yyyyMMdd'T'HHmmss'Z'", CultureInfo.InvariantCulture);

        /// <summary>
        /// Creates <c>baseDirectory/runId/</c> and writes the header. <paramref name="table"/> is the machines sim.bin will hold.
        /// <paramref name="clock"/> and <paramref name="utc"/> are the recorder's two clocks (tests pass their own).
        /// <paramref name="openStream"/> opens each of the four files (sim.bin and the three logs) by path: a new file, write-only, shared
        /// for reading. Tests hand in a stream that fails; the default is a <see cref="FileStream"/>.
        /// </summary>
        public static RunRecorder Create(string baseDirectory, RunHeader header, IReadOnlyList<SimMachine> table, Func<double> clock = null, Func<DateTimeOffset> utc = null,
            Func<string, Stream> openStream = null)
        {
            openStream ??= OpenFile;
            if (string.IsNullOrEmpty(baseDirectory)) throw new ArgumentException("a recording needs a directory", nameof(baseDirectory));
            if (header == null) throw new ArgumentNullException(nameof(header));
            utc ??= () => DateTimeOffset.UtcNow;
            if (string.IsNullOrEmpty(header.RunId))
            {
                header.StartedAtUtc = utc();
                header.RunId = NewRunId(header.StartedAtUtc);
            }
            else if (header.StartedAtUtc == default) header.StartedAtUtc = utc();

            var dir = Path.Combine(baseDirectory, header.RunId);
            if (System.IO.Directory.Exists(dir) && System.IO.Directory.GetFileSystemEntries(dir).Length > 0)
                throw new IOException("the recording directory " + dir + " already exists and is not empty: a run is never overwritten");
            System.IO.Directory.CreateDirectory(dir);

            var watch = Stopwatch.StartNew();
            clock ??= () => watch.Elapsed.TotalSeconds;
            SimBinWriter sim = null;
            NdjsonFile o = null, d = null, p = null;
            try
            {
                sim = new SimBinWriter(openStream(Path.Combine(dir, RecordingFiles.SimBin)), table, NominalHz);
                o = new NdjsonFile(openStream(Path.Combine(dir, RecordingFiles.Observed)));
                d = new NdjsonFile(openStream(Path.Combine(dir, RecordingFiles.Device)));
                p = new NdjsonFile(openStream(Path.Combine(dir, RecordingFiles.Presenter)));
                var r = new RunRecorder(dir, header, sim, o, d, p, clock, utc);
                sim.Flush();
                r.WriteHeader();
                return r;
            }
            catch
            {
                Quiet(sim); Quiet(o); Quiet(d); Quiet(p);
                throw;
            }
        }

        // a half-made recording is being abandoned: what closing its files says is of no use to the caller, who is told why it failed
        static void Quiet(IDisposable d)
        {
            try { d?.Dispose(); }
            catch (Exception) { /* the failure that matters is the one being thrown */ }
        }

        static Stream OpenFile(string path) => new FileStream(path, FileMode.CreateNew, FileAccess.Write, FileShare.Read, 1 << 16);

        void WriteHeader()
        {
            lock (closeGate)
            {
                header.EndedCleanly = false;
                header.EndedAtUtc = null;
                WriteRunJson();
            }
        }

        // the caller holds closeGate
        void WriteRunJson()
        {
            string json;
            lock (header) json = header.ToJson();
            AtomicWrite(Path.Combine(Directory, RecordingFiles.RunJson), json);
        }

        /// <summary>
        /// A run that is killed never reaches <see cref="Close"/>, so what the header must say of it is written as it happens: the clock's
        /// segments decide whether footage needs its caption. Never after the end summary (that would take it back).
        /// </summary>
        void RewriteHeader()
        {
            try
            {
                lock (closeGate)
                {
                    if (closed) return;
                    WriteRunJson();
                }
            }
            catch (Exception e) when (e is IOException || e is UnauthorizedAccessException) { Fault(e); }
        }

        static void AtomicWrite(string path, string text)
        {
            var tmp = path + ".tmp";
            File.WriteAllText(tmp, text, new UTF8Encoding(false));
            if (File.Exists(path)) File.Replace(tmp, path, null);
            else File.Move(tmp, path);
        }

        void Fault(Exception e)
        {
            string first = null;
            lock (statsGate)
            {
                if (fault == null) fault = first = e.GetType().Name + ": " + e.Message;
            }

            if (first == null) return;
            // whoever listens must not be able to throw into the writer
            try { Faulted?.Invoke(first); }
            catch (Exception) { /* a listener that fails has nothing left to tell */ }
        }

        /// <summary>
        /// Stops the recording because something the recorder depends on failed (a tap that threw, a callback it could not complete). Once,
        /// loudly (<see cref="Faulted"/>), and run.json will say the run did not end cleanly. Never throws.
        /// </summary>
        public void Fail(Exception e) => Fault(e ?? new InvalidOperationException("the recording failed"));

        /// <summary>One frame of every machine, as it was rendered. Main thread. A time that goes backwards is a bug and throws.</summary>
        public void WriteSimFrame(double t, MachineSample[] samples)
        {
            if (fault != null || closed) return;
            var watch = Stopwatch.StartNew();
            try
            {
                sim.WriteFrame(t, samples);
                if (flushTimer.Elapsed >= FlushEvery)
                {
                    flushTimer.Restart();
                    sim.Flush();
                }
            }
            catch (IOException e) { Fault(e); return; }
            catch (UnauthorizedAccessException e) { Fault(e); return; }
            catch (ObjectDisposedException) { return; }

            var ms = watch.Elapsed.TotalMilliseconds;
            lock (statsGate)
            {
                stats.SimFrames = sim.Frames;
                stats.SimBytes = sim.BytesWritten;
                stats.WriteMillisTotal += ms;
                if (ms > stats.WriteMillisWorstFrame) stats.WriteMillisWorstFrame = ms;
            }
        }

        public void Observed(ObservedLine line) => Append(observed, line, (s, n) => { s.ObservedLines++; s.NdjsonBytes += n; });

        public void Device(DeviceLine line) => Append(device, line, (s, n) => { s.DeviceLines++; s.NdjsonBytes += n; });

        public void Presenter(PresenterLine line) => Append(presenter, line, (s, n) => { s.PresenterLines++; s.NdjsonBytes += n; });

        void Append(NdjsonFile file, RecordLine line, Action<RecorderStats, int> count)
        {
            if (fault != null || closed) return;
            var watch = Stopwatch.StartNew();
            int bytes;
            // Stamped inside the file's own lock: lines arrive from several threads (acknowledgements
            // come back on the SDK's), and a time taken before the lock lets a later stamp reach the
            // file first. The reader refuses a file whose times go backwards, so order is the contract.
            try
            {
                lock (file)
                {
                    line.T = Now;
                    line.Utc = UtcNow;
                    bytes = file.Write(line.ToJson());
                }
            }
            catch (IOException e) { Fault(e); return; }
            catch (UnauthorizedAccessException e) { Fault(e); return; }
            catch (ObjectDisposedException) { return; }
            var ms = watch.Elapsed.TotalMilliseconds;
            lock (statsGate)
            {
                count(stats, bytes);
                stats.WriteMillisTotal += ms;
            }
        }

        /// <summary>Forces what has been written so far out to disk (the recorder does this itself about once a second).</summary>
        public void Flush()
        {
            if (fault != null || closed) return;
            try { sim.Flush(); }
            catch (ObjectDisposedException) { }
            catch (Exception e) { Fault(e); }
        }

        /// <summary>Records the scene's clock changing speed: into the header's clock segments and as a line in presenter.ndjson.</summary>
        public void ClockChanged(double scale)
        {
            if (closed) return;
            var mode = Math.Abs(scale - 1.0) < 1e-6 ? ClockSegment.Real : ClockSegment.Accelerated;
            lock (header)
            {
                var last = header.Clock.Count > 0 ? header.Clock[header.Clock.Count - 1] : null;
                if (last != null && last.Mode == mode && Math.Abs(last.Scale - scale) < 1e-6) return;
                header.Clock.Add(new ClockSegment { From = Now, Mode = mode, Scale = scale });
            }

            RewriteHeader();

            var line = PresenterLine.Of(PresenterKinds.Clock, mode == ClockSegment.Real ? "clock: real time" : "clock: accelerated x" + scale.ToString("0.##", CultureInfo.InvariantCulture));
            line.Scale = scale;
            Presenter(line);
        }

        /// <summary>Ends the recording: flushes everything and rewrites run.json with the sizes and what the writing cost. Idempotent.</summary>
        public void Close()
        {
            lock (closeGate)
            {
                if (closed) return;
                closed = true;
            }

            // every file is closed whatever the others did, and nothing here may throw: the end summary below is the point of closing
            Guarded(() => sim.Flush());
            Guarded(sim.Dispose);
            Guarded(observed.Dispose);
            Guarded(device.Dispose);
            Guarded(presenter.Dispose);
            lock (statsGate)
            {
                header.EndedAtUtc = UtcNow;
                header.EndedCleanly = fault == null;
                header.DurationSeconds = Now;
                header.SimFrames = stats.SimFrames;
                header.SimBytes = stats.SimBytes;
                header.ObservedLines = stats.ObservedLines;
                header.DeviceLines = stats.DeviceLines;
                header.PresenterLines = stats.PresenterLines;
                header.WriteMillisTotal = stats.WriteMillisTotal;
                header.WriteMillisWorstFrame = stats.WriteMillisWorstFrame;
            }

            try { lock (closeGate) WriteRunJson(); }
            catch (Exception e) when (e is IOException || e is UnauthorizedAccessException) { Fault(e); }
        }

        void Guarded(Action step)
        {
            try { step(); }
            catch (ObjectDisposedException) { }
            catch (Exception e) { Fault(e); }
        }

        public void Dispose() => Close();
    }

    /// <summary>One ndjson file: lines written whole, under a lock, flushed as they are written so a crash loses no more than the last line.</summary>
    sealed class NdjsonFile : IDisposable
    {
        readonly StreamWriter writer;
        readonly object gate = new object();
        bool disposed;

        public NdjsonFile(Stream stream)
        {
            writer = new StreamWriter(stream, new UTF8Encoding(false)) { AutoFlush = true, NewLine = "\n" };
        }

        public int Write(string line)
        {
            lock (gate)
            {
                if (disposed) throw new ObjectDisposedException(nameof(NdjsonFile));
                writer.WriteLine(line);
                return line.Length + 1;
            }
        }

        public void Dispose()
        {
            lock (gate)
            {
                if (disposed) return;
                disposed = true;
                writer.Dispose();
            }
        }
    }
}
