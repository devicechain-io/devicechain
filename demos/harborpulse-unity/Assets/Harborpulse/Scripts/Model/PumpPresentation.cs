// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Harborpulse.Model.Director;
using System.Collections.Generic;

namespace DeviceChain.Harborpulse.Model
{
    public sealed class AlarmBeacon
    {
        public string Identity { get; internal set; }
        public string Device { get; internal set; }
        public string Key { get; internal set; }
        public string State { get; internal set; }
        public string Severity { get; internal set; }
        public bool Acknowledged { get; internal set; }
        public DateTimeOffset ObservedAt { get; internal set; }
        public bool Cleared => State == "CLEARED";
        public double PeriodSeconds => AlarmBeacons.Period(Severity);
        public int Rings => AlarmBeacons.RingCount(Severity);
        public double Pulse(DateTimeOffset now) => Cleared || Acknowledged ? 1 : 0.5 + 0.5 * Math.Cos(now.ToUnixTimeMilliseconds() / 1000.0 * 2 * Math.PI / PeriodSeconds);
    }

    /// <summary>One persistent identity per (bound external ID, alarm key), including escalation and de-escalation.</summary>
    public sealed class AlarmBeacons
    {
        sealed class Held { public HarborAlarm Alarm; public DateTimeOffset? ClearedAt; }
        readonly Dictionary<(string, string), Held> held = new Dictionary<(string, string), Held>();
        public static double Period(string severity)
        {
            // A replay of a recording that did not sample severities draws such an alarm as the weakest one (one ring, the slowest
            // pulse): never a guess upwards. Any other unknown word is still refused.
            switch (severity) { case ReplaySource.UnrecordedSeverity: case "WARNING": return 1.5; case "MINOR": return 1; case "MAJOR": return 0.7; case "CRITICAL": return 0.4; default: throw new ArgumentException("unknown alarm severity: " + severity); }
        }
        /// <summary>
        /// The yaw, degrees about the vertical, that faces a cleared alarm's tick to the eye. The tick is drawn reading left to right
        /// along its own +x (short stroke down, long stroke up); standing with its device, it read mirrored from every eye on the
        /// device's far side. Turned so its forward points from the eye to the tick, its +x is the eye's right wherever the eye is.
        /// </summary>
        public static double TickYawDeg(double atX, double atZ, double eyeX, double eyeZ) => Math.Atan2(atX - eyeX, atZ - eyeZ) * 180 / Math.PI;
        public static int RingCount(string severity) { Period(severity); return severity == "CRITICAL" ? 3 : severity == "MAJOR" ? 2 : 1; }

        public IReadOnlyList<AlarmBeacon> Update(IReadOnlyList<HarborAlarm> alarms, DateTimeOffset now)
        {
            foreach (var a in alarms)
            {
                var key = (a.Device, a.Key);
                if (!held.TryGetValue(key, out var h)) held[key] = h = new Held { Alarm = a };
                else if (a.OccurredAt < h.Alarm.OccurredAt || (a.OccurredAt == h.Alarm.OccurredAt && a.ObservedAt < h.Alarm.ObservedAt)) continue;
                if (a.State == "CLEARED" && !h.ClearedAt.HasValue) h.ClearedAt = now;
                if (a.Active) h.ClearedAt = null;
                h.Alarm = a;
            }
            var result = new List<AlarmBeacon>();
            foreach (var pair in held)
            {
                var h = pair.Value;
                if (!h.Alarm.Active && (!h.ClearedAt.HasValue || now - h.ClearedAt.Value >= TimeSpan.FromSeconds(2))) continue;
                var a = h.Alarm;
                result.Add(new AlarmBeacon { Identity = a.Device.Length + ":" + a.Device + a.Key, Device = a.Device, Key = a.Key,
                    State = a.State, Severity = a.Severity, Acknowledged = a.Acknowledged, ObservedAt = a.ObservedAt });
            }
            result.Sort((a, b) => string.CompareOrdinal(a.Identity, b.Identity));
            return result.AsReadOnly();
        }
    }

    public sealed class CommandObservation
    {
        internal CommandObservation(HarborCommand command) { Device = command.Device; Token = command.Token; Name = command.Name; Status = command.Status; ObservedAt = command.ObservedAt; }
        internal CommandObservation(HarborCommand command, string status, DateTimeOffset at) : this(command) { Status = status; PlatformAt = at; }
        public string Device { get; }
        public string Token { get; }
        public string Name { get; }
        public string Status { get; }
        public DateTimeOffset ObservedAt { get; }
        public DateTimeOffset? PlatformAt { get; }
        public string RecordLabel => PlatformAt.HasValue ? "platform record " + PlatformAt.Value.UtcDateTime.ToString("HH:mm:ss", System.Globalization.CultureInfo.InvariantCulture) + " UTC" : "observed status; platform time not reported";
    }

    /// <summary>Platform timestamp rows plus status changes actually observed. No inferred lifecycle transitions.</summary>
    public sealed class CommandHistory
    {
        readonly Dictionary<string, List<CommandObservation>> byToken = new Dictionary<string, List<CommandObservation>>(StringComparer.Ordinal);
        public void Observe(IEnumerable<HarborCommand> commands)
        {
            foreach (var c in commands)
            {
                if (!byToken.TryGetValue(c.Token, out var list)) byToken[c.Token] = list = new List<CommandObservation>();
                if (c.IsPlatformRecord)
                {
                    AddRecorded(list, c, "QUEUED", c.QueuedTime);
                    AddRecorded(list, c, "SENT", c.SentTime);
                    if (c.Status == "SUCCESSFUL" || c.Status == "FAILED" || c.Status == "TIMEOUT" || c.Status == "EXPIRED" || c.Status == "CANCELLED")
                        AddRecorded(list, c, c.Status, c.RespondedTime);
                }
                else
                {
                    CommandObservation latest = null;
                    foreach (var row in list) if (!row.PlatformAt.HasValue && (latest == null || row.ObservedAt >= latest.ObservedAt)) latest = row;
                    if (latest == null || (latest.Status != c.Status && c.ObservedAt >= latest.ObservedAt)) list.Add(new CommandObservation(c));
                }
            }
        }
        static void AddRecorded(List<CommandObservation> rows, HarborCommand c, string status, DateTimeOffset? at)
        {
            if (!at.HasValue) return;
            foreach (var row in rows) if (row.PlatformAt == at && row.Status == status) return;
            rows.Add(new CommandObservation(c, status, at.Value));
        }
        static int Compare(CommandObservation a, CommandObservation b)
        {
            var time = (a.PlatformAt ?? a.ObservedAt).CompareTo(b.PlatformAt ?? b.ObservedAt);
            if (time != 0) return time;
            var token = string.CompareOrdinal(a.Token, b.Token);
            return token != 0 ? token : Rank(a.Status).CompareTo(Rank(b.Status));
        }
        static int Rank(string status) => status == "QUEUED" ? 0 : status == "SENT" ? 1 : 2;
        // An observed status the platform has also recorded, with its own time, is shown once: as the platform's record.
        static void AddVisible(List<CommandObservation> into, List<CommandObservation> rows)
        {
            foreach (var row in rows)
            {
                var superseded = false;
                if (!row.PlatformAt.HasValue) foreach (var other in rows) if (other.PlatformAt.HasValue && other.Status == row.Status) { superseded = true; break; }
                if (!superseded) into.Add(row);
            }
        }
        public IReadOnlyList<CommandObservation> For(string token)
        {
            if (!byToken.TryGetValue(token, out var rows)) return Array.Empty<CommandObservation>();
            var copy = new List<CommandObservation>(); AddVisible(copy, rows); copy.Sort(Compare); return copy.AsReadOnly();
        }
        public IReadOnlyList<CommandObservation> Snapshot()
        {
            var result = new List<CommandObservation>();
            foreach (var list in byToken.Values) AddVisible(result, list);
            result.Sort(Compare);
            return result.AsReadOnly();
        }
    }

    public enum PumpStartKind { Unconfirmed, Platform, FloatConsistent }

    /// <summary>
    /// PLATFORM compares the recorded start-pump response time with the current interval's platform 0-to-1 event time.
    /// FLOAT-CONSISTENT requires a contemporaneous well >= 3.2 m, positive healthy command-poll coverage spanning the off/on event times,
    /// and no qualifying platform success. Missing command evidence alone NEVER establishes float. Neither chip proves physical causation.
    /// </summary>
    public sealed class PumpStartEvidence
    {
        Reading? last;
        DateTimeOffset? offAt, intervalOffAt, transition;
        bool highFloat, joinedRunning;
        public PumpStartKind Kind { get; private set; }
        public DateTimeOffset? TransitionAt => transition;
        public string Label => Kind == PumpStartKind.Platform ? "PLATFORM" : Kind == PumpStartKind.FloatConsistent ? "FLOAT-CONSISTENT" : joinedRunning ? "UNCONFIRMED (joined while running)" : "UNCONFIRMED";
        public void Update(string device, Reading? running, Reading? well, CommandHistory commands, CommandCoverage coverage)
        {
            if (!running.HasValue) return;
            var r = running.Value;
            if (last.HasValue && r.OccurredAt < last.Value.OccurredAt) return;
            if (r.Value <= 0.5)
            {
                joinedRunning = false;
                if (!last.HasValue || last.Value.Value > 0.5) intervalOffAt = r.OccurredAt;
                offAt = r.OccurredAt; transition = null; Kind = PumpStartKind.Unconfirmed;
            }
            else if (last.HasValue && last.Value.Value <= 0.5)
            {
                transition = r.OccurredAt;
                highFloat = well.HasValue && well.Value.Value >= 3.2 && Math.Abs((well.Value.OccurredAt - r.OccurredAt).TotalSeconds) <= 3;
            }
            else if (!last.HasValue) joinedRunning = true;
            last = r;
            if (!transition.HasValue) return;
            if (Kind == PumpStartKind.Platform) return;
            foreach (var c in commands.Snapshot())
                if (c.Device == device && c.Name == "start-pump" && c.Status == "SUCCESSFUL" && intervalOffAt.HasValue && c.PlatformAt.HasValue && c.PlatformAt.Value > intervalOffAt.Value && c.PlatformAt.Value <= transition.Value)
                    Kind = PumpStartKind.Platform;
            if (Kind != PumpStartKind.Platform && highFloat && offAt.HasValue && coverage.Covers(offAt.Value, transition.Value)) Kind = PumpStartKind.FloatConsistent;
        }
    }

    public sealed class PumpPresentation
    {
        public const double HighMarkerM = 2.5, FloatMarkerM = 3.2, LowCutoffM = 0.8;
        public Reading? WetWell { get; internal set; }
        public Reading? Running { get; internal set; }
        public Reading? Flow { get; internal set; }
        public Reading? Current { get; internal set; }
        /// <summary>Separately labelled shared-attribute rule level; never changes HighMarkerM.</summary>
        public Reading? WellAlarmRule { get; internal set; }
        /// <summary>Visual mapping only: running * clamp(flow_lps / 120), null if either input is missing.</summary>
        public double? ImpellerSpeed => Running.HasValue && Flow.HasValue ? (Running.Value.Value > 0.5 ? Rgb.Clamp01(Flow.Value.Value / 120) : 0) : (double?)null;
        public PumpStartKind StartKind { get; internal set; }
        public string StartLabel { get; internal set; }
        public DateTimeOffset? TransitionAt { get; internal set; }
    }
}
