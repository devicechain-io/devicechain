// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Text.Json;
using DeviceChain.Sitepulse.Domain;

namespace DeviceChain.Sitepulse.Recording
{
    /// <summary>Fields every line carries: when this app saw it (seconds into the run, and UTC) and which kind of line it is.</summary>
    public abstract class RecordLine
    {
        /// <summary>Seconds since the recording began, on the recorder's monotonic clock.</summary>
        public double T { get; set; }

        public DateTimeOffset Utc { get; set; }
        public string K { get; set; }

        public string ToJson()
        {
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms))
            {
                w.WriteStartObject();
                w.WriteNumber("t", JsonIo.Seconds(T));
                w.WriteString("utc", JsonIo.Time(Utc));
                w.WriteString("k", K);
                WriteFields(w);
                w.WriteEndObject();
            }

            return Redactor.RedactForRecording(Encoding.UTF8.GetString(ms.ToArray()));
        }

        protected abstract void WriteFields(Utf8JsonWriter w);

        protected void ReadCommon(JsonElement e)
        {
            T = JsonIo.Num(e, "t", double.NaN);
            if (double.IsNaN(T)) throw new RecordingFormatException("a line has no \"t\": " + Clip(e));
            Utc = JsonIo.ParseTime(JsonIo.RequireStr(e, "utc", "line"), "line.utc");
            K = JsonIo.RequireStr(e, "k", "line");
        }

        internal static string Clip(JsonElement e)
        {
            var s = e.GetRawText();
            return s.Length > 120 ? s.Substring(0, 120) + "…" : s;
        }
    }

    /// <summary>The kinds of observed.ndjson line, spelled as they are written.</summary>
    public static class ObservedKinds
    {
        public const string Measurement = "measurement", Location = "location", Alarm = "alarm", AlarmSnapshot = "alarmSnapshot",
            Command = "command", Presence = "presence", Status = "status";
    }

    /// <summary>
    /// One thing the observer handed the app, as the app received it (observed.ndjson). A field is meaningful only for the
    /// kinds that carry it; a line holds nothing else. <see cref="RecordLine.T"/> is when the app applied it, which is when
    /// the viewer could first have seen its effect.
    /// </summary>
    public sealed class ObservedLine : RecordLine
    {
        /// <summary>The platform token of the device the line is about (not a credential).</summary>
        public string Device { get; set; }

        /// <summary>A measurement's name, an alarm's key, a command's name, a status line's source.</summary>
        public string Name { get; set; }

        public double Value { get; set; }
        public bool FromSnapshot { get; set; }

        /// <summary>An alarm's or a command's token.</summary>
        public string Token { get; set; }

        /// <summary>An alarm's state, a command's status, a status line's state.</summary>
        public string State { get; set; }

        public string Severity { get; set; }
        public string MetricKey { get; set; }
        public bool Acknowledged { get; set; }

        public DateTimeOffset? OccurredAt { get; set; }
        public DateTimeOffset? ObservedAt { get; set; }

        /// <summary>A command's queued time.</summary>
        public DateTimeOffset? QueuedAt { get; set; }

        public double? SpeedMps { get; set; }
        public double? HeadingDegrees { get; set; }
        public double? ElevationMetres { get; set; }

        public bool PresenceActive { get; set; }
        public DateTimeOffset? LastActivityAt { get; set; }

        /// <summary>A status line's reason.</summary>
        public string Reason { get; set; }

        // alarm snapshot
        public DateTimeOffset? RequestedAt { get; set; }
        public bool Truncated { get; set; }
        public int? TotalRecords { get; set; }
        public List<ObservedLine> Alarms { get; } = new List<ObservedLine>();

        public static ObservedLine Of(string kind) => new ObservedLine { K = kind };

        protected override void WriteFields(Utf8JsonWriter w)
        {
            switch (K)
            {
                case ObservedKinds.Measurement:
                    w.WriteString("dev", Device);
                    w.WriteString("n", Name);
                    w.WriteNumber("v", Value);
                    JsonIo.WriteTime(w, "occ", OccurredAt);
                    JsonIo.WriteTime(w, "obs", ObservedAt);
                    if (FromSnapshot) w.WriteBoolean("snap", true);
                    break;
                case ObservedKinds.Location:
                    w.WriteString("dev", Device);
                    JsonIo.WriteNum(w, "speed", SpeedMps);
                    JsonIo.WriteNum(w, "heading", HeadingDegrees);
                    JsonIo.WriteNum(w, "elev", ElevationMetres);
                    JsonIo.WriteTime(w, "occ", OccurredAt);
                    JsonIo.WriteTime(w, "obs", ObservedAt);
                    break;
                case ObservedKinds.Alarm:
                    w.WriteString("dev", Device);
                    WriteAlarm(w);
                    break;
                case ObservedKinds.AlarmSnapshot:
                    JsonIo.WriteTime(w, "requested", RequestedAt);
                    JsonIo.WriteTime(w, "obs", ObservedAt);
                    if (Truncated) w.WriteBoolean("truncated", true);
                    if (TotalRecords.HasValue) w.WriteNumber("total", TotalRecords.Value);
                    w.WriteStartArray("alarms");
                    foreach (var a in Alarms)
                    {
                        w.WriteStartObject();
                        w.WriteString("dev", a.Device);
                        a.WriteAlarm(w);
                        w.WriteEndObject();
                    }

                    w.WriteEndArray();
                    break;
                case ObservedKinds.Command:
                    w.WriteString("dev", Device);
                    w.WriteString("token", Token);
                    w.WriteString("n", Name);
                    w.WriteString("state", State);
                    JsonIo.WriteTime(w, "queued", QueuedAt);
                    JsonIo.WriteTime(w, "obs", ObservedAt);
                    break;
                case ObservedKinds.Presence:
                    w.WriteString("dev", Device);
                    w.WriteBoolean("active", PresenceActive);
                    JsonIo.WriteTime(w, "last", LastActivityAt);
                    JsonIo.WriteTime(w, "obs", ObservedAt);
                    break;
                case ObservedKinds.Status:
                    w.WriteString("source", Name);
                    w.WriteString("state", State);
                    JsonIo.WriteStr(w, "reason", Reason);
                    JsonIo.WriteTime(w, "at", OccurredAt);
                    break;
                default:
                    throw new InvalidOperationException("unknown observed line kind " + K);
            }
        }

        void WriteAlarm(Utf8JsonWriter w)
        {
            w.WriteString("token", Token);
            w.WriteString("key", Name);
            JsonIo.WriteStr(w, "metric", MetricKey);
            w.WriteString("state", State);
            JsonIo.WriteStr(w, "sev", Severity);
            if (Acknowledged) w.WriteBoolean("ack", true);
            JsonIo.WriteTime(w, "occ", OccurredAt);
            JsonIo.WriteTime(w, "obs", ObservedAt);
        }

        public static ObservedLine Read(JsonElement e)
        {
            var l = new ObservedLine();
            l.ReadCommon(e);
            switch (l.K)
            {
                case ObservedKinds.Measurement:
                    l.Device = JsonIo.RequireStr(e, "dev", "measurement");
                    l.Name = JsonIo.RequireStr(e, "n", "measurement");
                    l.Value = JsonIo.Num(e, "v");
                    l.OccurredAt = JsonIo.TimeOrNull(e, "occ", "measurement");
                    l.ObservedAt = JsonIo.TimeOrNull(e, "obs", "measurement");
                    l.FromSnapshot = JsonIo.Bool(e, "snap");
                    break;
                case ObservedKinds.Location:
                    l.Device = JsonIo.RequireStr(e, "dev", "location");
                    l.SpeedMps = JsonIo.NumOrNull(e, "speed");
                    l.HeadingDegrees = JsonIo.NumOrNull(e, "heading");
                    l.ElevationMetres = JsonIo.NumOrNull(e, "elev");
                    l.OccurredAt = JsonIo.TimeOrNull(e, "occ", "location");
                    l.ObservedAt = JsonIo.TimeOrNull(e, "obs", "location");
                    break;
                case ObservedKinds.Alarm:
                    l.Device = JsonIo.RequireStr(e, "dev", "alarm");
                    l.ReadAlarm(e);
                    break;
                case ObservedKinds.AlarmSnapshot:
                    l.RequestedAt = JsonIo.TimeOrNull(e, "requested", "alarmSnapshot");
                    l.ObservedAt = JsonIo.TimeOrNull(e, "obs", "alarmSnapshot");
                    l.Truncated = JsonIo.Bool(e, "truncated");
                    var total = JsonIo.NumOrNull(e, "total");
                    l.TotalRecords = total.HasValue ? (int)total.Value : (int?)null;
                    if (e.TryGetProperty("alarms", out var arr) && arr.ValueKind == JsonValueKind.Array)
                        foreach (var a in arr.EnumerateArray())
                        {
                            var row = ObservedLine.Of(ObservedKinds.Alarm);
                            row.T = l.T;
                            row.Utc = l.Utc;
                            row.Device = JsonIo.RequireStr(a, "dev", "alarmSnapshot.alarms");
                            row.ReadAlarm(a);
                            l.Alarms.Add(row);
                        }

                    break;
                case ObservedKinds.Command:
                    l.Device = JsonIo.RequireStr(e, "dev", "command");
                    l.Token = JsonIo.RequireStr(e, "token", "command");
                    l.Name = JsonIo.RequireStr(e, "n", "command");
                    l.State = JsonIo.RequireStr(e, "state", "command");
                    l.QueuedAt = JsonIo.TimeOrNull(e, "queued", "command");
                    l.ObservedAt = JsonIo.TimeOrNull(e, "obs", "command");
                    break;
                case ObservedKinds.Presence:
                    l.Device = JsonIo.RequireStr(e, "dev", "presence");
                    l.PresenceActive = JsonIo.Bool(e, "active");
                    l.LastActivityAt = JsonIo.TimeOrNull(e, "last", "presence");
                    l.ObservedAt = JsonIo.TimeOrNull(e, "obs", "presence");
                    break;
                case ObservedKinds.Status:
                    l.Name = JsonIo.RequireStr(e, "source", "status");
                    l.State = JsonIo.RequireStr(e, "state", "status");
                    l.Reason = JsonIo.Str(e, "reason");
                    l.OccurredAt = JsonIo.TimeOrNull(e, "at", "status");
                    break;
                default:
                    throw new RecordingFormatException("observed.ndjson has a line of unknown kind \"" + l.K + "\"");
            }

            return l;
        }

        void ReadAlarm(JsonElement e)
        {
            Token = JsonIo.RequireStr(e, "token", "alarm");
            Name = JsonIo.RequireStr(e, "key", "alarm");
            MetricKey = JsonIo.Str(e, "metric");
            State = JsonIo.RequireStr(e, "state", "alarm");
            Severity = JsonIo.Str(e, "sev");
            Acknowledged = JsonIo.Bool(e, "ack");
            OccurredAt = JsonIo.TimeOrNull(e, "occ", "alarm");
            ObservedAt = JsonIo.TimeOrNull(e, "obs", "alarm");
        }
    }

    public static class DeviceKinds
    {
        /// <summary>A sample the broker took (PUBACK'd): values or a location.</summary>
        public const string Sample = "sample";

        public const string LinkState = "linkState", Started = "started", StartFailed = "startFailed", FirstPublish = "firstPublish",
            Command = "command", CommandRefused = "commandRefused", TaskReceived = "taskReceived", TaskCompleted = "taskCompleted",
            Timeline = "timeline";
    }

    /// <summary>
    /// What a device did, from the device plane (device.ndjson): a sample published and acknowledged, a link changing state,
    /// a command received and what became of it, and every row of the machine's own timeline.
    /// </summary>
    public sealed class DeviceLine : RecordLine
    {
        /// <summary>The scene id of the machine (SP-HL-0006).</summary>
        public string Device { get; set; }

        public string Text { get; set; }
        public string State { get; set; }

        // sample
        public string SampleKind { get; set; }
        public DateTimeOffset? OccurredAt { get; set; }
        public DateTimeOffset? AckedAt { get; set; }
        public SortedDictionary<string, double> Values { get; } = new SortedDictionary<string, double>(StringComparer.Ordinal);
        public double? Latitude { get; set; }
        public double? Longitude { get; set; }
        public double? Elevation { get; set; }
        public double? SpeedMps { get; set; }
        public double? HeadingDegrees { get; set; }

        // command / task
        public string Token { get; set; }
        public string Key { get; set; }
        public string Payload { get; set; }
        public long Sequence { get; set; }
        public bool Succeeded { get; set; }
        public string Reason { get; set; }

        // timeline
        public string RowKind { get; set; }

        public static DeviceLine Of(string kind, string device) => new DeviceLine { K = kind, Device = device };

        protected override void WriteFields(Utf8JsonWriter w)
        {
            w.WriteString("dev", Device);
            switch (K)
            {
                case DeviceKinds.Sample:
                    w.WriteString("sk", SampleKind);
                    JsonIo.WriteTime(w, "occ", OccurredAt);
                    JsonIo.WriteTime(w, "ack", AckedAt);
                    if (Values.Count > 0)
                    {
                        w.WriteStartObject("values");
                        foreach (var kv in Values) w.WriteNumber(kv.Key, kv.Value);
                        w.WriteEndObject();
                    }

                    JsonIo.WriteNum(w, "lat", Latitude);
                    JsonIo.WriteNum(w, "lon", Longitude);
                    JsonIo.WriteNum(w, "elev", Elevation);
                    JsonIo.WriteNum(w, "speed", SpeedMps);
                    JsonIo.WriteNum(w, "heading", HeadingDegrees);
                    break;
                case DeviceKinds.LinkState:
                case DeviceKinds.Started:
                case DeviceKinds.StartFailed:
                case DeviceKinds.FirstPublish:
                    JsonIo.WriteStr(w, "state", State);
                    JsonIo.WriteStr(w, "text", Text);
                    break;
                case DeviceKinds.Command:
                case DeviceKinds.CommandRefused:
                    JsonIo.WriteStr(w, "text", Text);
                    break;
                case DeviceKinds.TaskReceived:
                    w.WriteString("token", Token);
                    w.WriteString("key", Key);
                    JsonIo.WriteStr(w, "payload", Payload);
                    w.WriteNumber("seq", Sequence);
                    break;
                case DeviceKinds.TaskCompleted:
                    w.WriteString("token", Token);
                    w.WriteBoolean("ok", Succeeded);
                    JsonIo.WriteStr(w, "reason", Reason);
                    break;
                case DeviceKinds.Timeline:
                    w.WriteString("rk", RowKind);
                    w.WriteString("text", Text);
                    break;
                default:
                    throw new InvalidOperationException("unknown device line kind " + K);
            }
        }

        public static DeviceLine Read(JsonElement e)
        {
            var l = new DeviceLine();
            l.ReadCommon(e);
            l.Device = JsonIo.RequireStr(e, "dev", "device line");
            switch (l.K)
            {
                case DeviceKinds.Sample:
                    l.SampleKind = JsonIo.RequireStr(e, "sk", "sample");
                    l.OccurredAt = JsonIo.TimeOrNull(e, "occ", "sample");
                    l.AckedAt = JsonIo.TimeOrNull(e, "ack", "sample");
                    if (e.TryGetProperty("values", out var vs) && vs.ValueKind == JsonValueKind.Object)
                        foreach (var p in vs.EnumerateObject()) l.Values[p.Name] = p.Value.GetDouble();
                    l.Latitude = JsonIo.NumOrNull(e, "lat");
                    l.Longitude = JsonIo.NumOrNull(e, "lon");
                    l.Elevation = JsonIo.NumOrNull(e, "elev");
                    l.SpeedMps = JsonIo.NumOrNull(e, "speed");
                    l.HeadingDegrees = JsonIo.NumOrNull(e, "heading");
                    break;
                case DeviceKinds.LinkState:
                case DeviceKinds.Started:
                case DeviceKinds.StartFailed:
                case DeviceKinds.FirstPublish:
                    l.State = JsonIo.Str(e, "state");
                    l.Text = JsonIo.Str(e, "text");
                    break;
                case DeviceKinds.Command:
                case DeviceKinds.CommandRefused:
                    l.Text = JsonIo.Str(e, "text");
                    break;
                case DeviceKinds.TaskReceived:
                    l.Token = JsonIo.RequireStr(e, "token", "taskReceived");
                    l.Key = JsonIo.RequireStr(e, "key", "taskReceived");
                    l.Payload = JsonIo.Str(e, "payload");
                    l.Sequence = JsonIo.Long(e, "seq");
                    break;
                case DeviceKinds.TaskCompleted:
                    l.Token = JsonIo.RequireStr(e, "token", "taskCompleted");
                    l.Succeeded = JsonIo.Bool(e, "ok");
                    l.Reason = JsonIo.Str(e, "reason");
                    break;
                case DeviceKinds.Timeline:
                    l.RowKind = JsonIo.RequireStr(e, "rk", "timeline");
                    l.Text = JsonIo.RequireStr(e, "text", "timeline");
                    break;
                default:
                    throw new RecordingFormatException("device.ndjson has a line of unknown kind \"" + l.K + "\"");
            }

            return l;
        }
    }

    public static class PresenterKinds
    {
        public const string Action = "action", Clock = "clock", Mode = "mode";
    }

    /// <summary>A presenter action, or a change of the scene's clock or the run's mode (presenter.ndjson).</summary>
    public sealed class PresenterLine : RecordLine
    {
        public string Device { get; set; }
        public string Text { get; set; }
        public double? Scale { get; set; }

        public static PresenterLine Of(string kind, string text, string device = null) => new PresenterLine { K = kind, Text = text, Device = device };

        protected override void WriteFields(Utf8JsonWriter w)
        {
            JsonIo.WriteStr(w, "dev", Device);
            w.WriteString("text", Text);
            JsonIo.WriteNum(w, "scale", Scale);
        }

        public static PresenterLine Read(JsonElement e)
        {
            var l = new PresenterLine();
            l.ReadCommon(e);
            if (l.K != PresenterKinds.Action && l.K != PresenterKinds.Clock && l.K != PresenterKinds.Mode)
                throw new RecordingFormatException("presenter.ndjson has a line of unknown kind \"" + l.K + "\"");
            l.Device = JsonIo.Str(e, "dev");
            l.Text = JsonIo.Str(e, "text", "");
            l.Scale = JsonIo.NumOrNull(e, "scale");
            return l;
        }
    }
}
