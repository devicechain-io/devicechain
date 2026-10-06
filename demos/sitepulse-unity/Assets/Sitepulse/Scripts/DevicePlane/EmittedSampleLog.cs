// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.DevicePlane
{
    /// <summary>
    /// The samples this run's devices handed to the broker, one JSON line each: the device (its scene id and
    /// platform token), the kind, the time the sample occurred and its values. An acceptance run writes it so
    /// the platform's stored events can be matched against what was sent by device and time, never by count.
    /// A line is written once the SDK reports the publish done, so every line is a sample the broker took.
    /// Callers are pool threads; the writer is serialised. Nothing secret is in a line, and each line passes
    /// the Redactor all the same.
    /// </summary>
    public sealed class EmittedSampleLog : IDisposable
    {
        readonly StreamWriter writer;
        readonly object gate = new object();
        long lines;

        public EmittedSampleLog(string path)
        {
            Path = path ?? throw new ArgumentNullException(nameof(path));
            var dir = System.IO.Path.GetDirectoryName(System.IO.Path.GetFullPath(path));
            if (!string.IsNullOrEmpty(dir)) Directory.CreateDirectory(dir);
            writer = new StreamWriter(new FileStream(path, FileMode.Create, FileAccess.Write, FileShare.ReadWrite), new UTF8Encoding(false)) { AutoFlush = true, NewLine = "\n" };
        }

        public string Path { get; }

        public long Lines => Interlocked.Read(ref lines);

        public void Record(string externalId, string deviceToken, Sample sample)
        {
            var line = Redactor.Redact(Format(externalId, deviceToken, sample));
            lock (gate)
            {
                try
                {
                    writer.WriteLine(line);
                    lines++;
                }
                catch (ObjectDisposedException)
                {
                    // the run is over: a sample finishing after the log was closed is not part of it
                }
            }
        }

        public static string Format(string externalId, string deviceToken, Sample sample)
        {
            var sb = new StringBuilder(160);
            sb.Append("{\"device\":").Append(Quote(externalId))
              .Append(",\"deviceToken\":").Append(Quote(deviceToken))
              .Append(",\"kind\":\"").Append(sample.Kind == SampleKind.Measurement ? "measurement" : "location").Append('"')
              .Append(",\"occurredTime\":\"").Append(sample.OccurredUtc.UtcDateTime.ToString("O", CultureInfo.InvariantCulture)).Append('"');
            if (sample.Kind == SampleKind.Measurement)
            {
                sb.Append(",\"values\":{");
                var first = true;
                foreach (var kv in sample.Values)
                {
                    if (!first) sb.Append(',');
                    first = false;
                    sb.Append(Quote(kv.Key)).Append(':').Append(Num(kv.Value));
                }

                sb.Append('}');
            }
            else
            {
                sb.Append(",\"latitude\":").Append(Num(sample.Latitude))
                  .Append(",\"longitude\":").Append(Num(sample.Longitude))
                  .Append(",\"elevation\":").Append(Num(sample.Elevation))
                  .Append(",\"speed\":").Append(Num(sample.SpeedMps))
                  .Append(",\"heading\":").Append(Num(sample.HeadingDegrees));
            }

            return sb.Append('}').ToString();
        }

        static string Num(double v) => double.IsNaN(v) || double.IsInfinity(v) ? "null" : v.ToString("R", CultureInfo.InvariantCulture);

        static string Quote(string s)
        {
            var sb = new StringBuilder(s.Length + 2).Append('"');
            foreach (var c in s)
            {
                if (c == '"' || c == '\\') sb.Append('\\').Append(c);
                else if (c < 0x20) sb.Append("\\u").Append(((int)c).ToString("x4", CultureInfo.InvariantCulture));
                else sb.Append(c);
            }

            return sb.Append('"').ToString();
        }

        public void Dispose()
        {
            lock (gate)
            {
                try { writer.Dispose(); }
                catch (IOException) { }
            }
        }
    }

    /// <summary>A factory whose links record every sample that was published through them, and otherwise behave as the inner one does.</summary>
    public sealed class RecordingLinkFactory : IDeviceLinkFactory
    {
        readonly IDeviceLinkFactory inner;
        readonly EmittedSampleLog log;

        public RecordingLinkFactory(IDeviceLinkFactory inner, EmittedSampleLog log)
        {
            this.inner = inner ?? throw new ArgumentNullException(nameof(inner));
            this.log = log ?? throw new ArgumentNullException(nameof(log));
        }

        public IDeviceLink Create(string externalId, string deviceToken, string credentialId)
            => new RecordingLink(inner.Create(externalId, deviceToken, credentialId), externalId, deviceToken, log);

        sealed class RecordingLink : IDeviceLink
        {
            readonly IDeviceLink link;
            readonly string externalId, deviceToken;
            readonly EmittedSampleLog log;

            public RecordingLink(IDeviceLink link, string externalId, string deviceToken, EmittedSampleLog log)
            {
                this.link = link;
                this.externalId = externalId;
                this.deviceToken = deviceToken;
                this.log = log;
            }

            public event Action<LinkState> StateChanged
            {
                add => link.StateChanged += value;
                remove => link.StateChanged -= value;
            }

            public bool CanPublish => link.CanPublish;

            public Task StartAsync(DeviceChain.Sdk.Mqtt.CommandHandler handler, System.Threading.CancellationToken cancellationToken)
                => link.StartAsync(handler, cancellationToken);

            public async Task PublishAsync(Sample sample, System.Threading.CancellationToken cancellationToken)
            {
                await link.PublishAsync(sample, cancellationToken).ConfigureAwait(false);
                log.Record(externalId, deviceToken, sample);
            }

            public System.Threading.Tasks.ValueTask DisposeAsync() => link.DisposeAsync();
        }
    }
}
