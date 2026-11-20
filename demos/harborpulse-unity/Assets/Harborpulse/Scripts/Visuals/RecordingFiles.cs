// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Concurrent;
using System.IO;
using System.Text;
using System.Threading;
using DeviceChain.Harborpulse.Model.Director;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>Caller-owned writers enqueue complete lines. Disk flushes happen off the player frame.</summary>
    public sealed class RecordingFiles : IDisposable
    {
        sealed class QueueWriter : TextWriter
        {
            readonly RecordingFiles owner; readonly int stream;
            public QueueWriter(RecordingFiles owner, int stream) { this.owner = owner; this.stream = stream; }
            public override Encoding Encoding => Encoding.UTF8;
            public override void WriteLine(string value)
            {
                if (!owner.queue.TryAdd((stream, value))) owner.Problem = "recording queue full: recording incomplete";
            }
            public override void Flush() { } // worker flushes every second and drains at teardown
        }
        readonly BlockingCollection<(int Stream, string Line)> queue = new BlockingCollection<(int, string)>(32768);
        readonly StreamWriter[] streams = new StreamWriter[4];
        readonly Thread worker;
        volatile string problem;
        bool disposed;
        public string Problem { get => problem; private set => problem = value; }
        public string DirectoryPath { get; }
        public Recorder Recorder { get; }
        public RecordingFiles(string root, DateTimeOffset runStart, DateTimeOffset epoch)
        {
            DirectoryPath = Path.Combine(root, runStart.UtcDateTime.ToString("yyyyMMddTHHmmssfffZ", System.Globalization.CultureInfo.InvariantCulture) + "-" + epoch.UtcDateTime.ToString("HHmmssfff", System.Globalization.CultureInfo.InvariantCulture));
            Directory.CreateDirectory(DirectoryPath);
            var names = new[] { "observed", "world", "director", "checkpoints" };
            try
            {
                for (var i = 0; i < 4; i++) streams[i] = new StreamWriter(Path.Combine(DirectoryPath, names[i] + ".ndjson"), false, new UTF8Encoding(false));
                Recorder = new Recorder(new QueueWriter(this, 0), new QueueWriter(this, 1), new QueueWriter(this, 2), new QueueWriter(this, 3));
                worker = new Thread(Drain) { IsBackground = true, Name = "Harbor recording" }; worker.Start();
            }
            catch { foreach (var stream in streams) stream?.Dispose(); throw; }
        }
        void Drain()
        {
            try
            {
                var lastFlush = DateTime.UtcNow;
                while (!queue.IsCompleted)
                {
                    if (queue.TryTake(out var row, 100)) streams[row.Stream].WriteLine(row.Line);
                    if ((DateTime.UtcNow - lastFlush).TotalSeconds >= 1)
                    { foreach (var stream in streams) stream.Flush(); lastFlush = DateTime.UtcNow; }
                }
            }
            catch (Exception e) { Problem = "recording IO: " + e.Message; }
            finally { foreach (var stream in streams) stream.Dispose(); }
        }
        public void Dispose() { if (disposed) return; disposed = true; Recorder.End(); queue.CompleteAdding(); worker.Join(); queue.Dispose(); }
    }
}
