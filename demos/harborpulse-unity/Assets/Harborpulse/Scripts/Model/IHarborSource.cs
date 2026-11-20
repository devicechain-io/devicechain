// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>Engine-free copies of shared observer records, keyed by bound external ID, never runner device aliases.</summary>
    public readonly struct HarborAlarm
    {
        public HarborAlarm(string device, string key, string state, string severity, bool acknowledged, DateTimeOffset occurredAt, DateTimeOffset observedAt)
        { Device = device; Key = key; State = state; Severity = severity; Acknowledged = acknowledged; OccurredAt = occurredAt; ObservedAt = observedAt; }
        public string Device { get; }
        public string Key { get; }
        public string State { get; }
        public string Severity { get; }
        public bool Acknowledged { get; }
        public DateTimeOffset OccurredAt { get; }
        public DateTimeOffset ObservedAt { get; }
        public bool Active => State == "ACTIVE";
    }

    public readonly struct HarborCommand
    {
        public HarborCommand(string device, string token, string name, string status, DateTimeOffset observedAt) : this()
        { Device = device; Token = token; Name = name; Status = status; ObservedAt = observedAt; }
        public HarborCommand(string device, string token, string name, string status, DateTimeOffset observedAt,
            DateTimeOffset? queuedTime, DateTimeOffset? sentTime, DateTimeOffset? respondedTime, string deviceToken)
            : this(device, token, name, status, observedAt)
        { QueuedTime = queuedTime; SentTime = sentTime; RespondedTime = respondedTime; DeviceToken = deviceToken; IsPlatformRecord = true; }
        public string Device { get; }
        public string Token { get; }
        public string Name { get; }
        public string Status { get; }
        public DateTimeOffset ObservedAt { get; }
        public bool IsPlatformRecord { get; }
        public string DeviceToken { get; }
        public DateTimeOffset? QueuedTime { get; }
        public DateTimeOffset? SentTime { get; }
        public DateTimeOffset? RespondedTime { get; }
    }

    public readonly struct HarborLocation
    {
        public HarborLocation(double? latitude, double? longitude, double? heading, double? speed, DateTimeOffset occurredAt)
        { Latitude = latitude; Longitude = longitude; Heading = heading; Speed = speed; OccurredAt = occurredAt; }
        public double? Latitude { get; }
        public double? Longitude { get; }
        public double? Heading { get; }
        public double? Speed { get; }
        public DateTimeOffset OccurredAt { get; }
    }

    public readonly struct CommandCoverage
    {
        public CommandCoverage(DateTimeOffset? healthySince, DateTimeOffset? lastOk) { HealthySince = healthySince; LastOk = lastOk; }
        public DateTimeOffset? HealthySince { get; }
        public DateTimeOffset? LastOk { get; }
        /// <summary>Positive coverage, with no known failure or poll gap over three seconds. Endpoints use observation time.</summary>
        public bool Covers(DateTimeOffset before, DateTimeOffset after) => HealthySince.HasValue && LastOk.HasValue &&
            HealthySince.Value <= before && LastOk.Value >= after && after >= before;
        public CommandCoverage Intersect(CommandCoverage other) => !HealthySince.HasValue || !LastOk.HasValue || !other.HealthySince.HasValue || !other.LastOk.HasValue
            ? default : new CommandCoverage(HealthySince > other.HealthySince ? HealthySince : other.HealthySince, LastOk < other.LastOk ? LastOk : other.LastOk);
    }

    /// <summary>Positive alarm coverage requires a complete snapshot and a live stream.</summary>
    public readonly struct AlarmCoverage
    {
        public AlarmCoverage(bool snapshotComplete, bool streamLive) { SnapshotComplete = snapshotComplete; StreamLive = streamLive; }
        public bool SnapshotComplete { get; }
        public bool StreamLive { get; }
        public bool Observed => SnapshotComplete && StreamLive;
    }

    /// <summary>
    /// Read-only input to one frame. Access on the main thread after the observer inbox drains. Live copies the shared ObservedAlarm,
    /// Commands (including LastCommand), and ObservedLocation; preview supplies visibly labelled stand-ins. Missing data stays absent.
    /// </summary>
    public interface IHarborSource : IReadingSource
    {
        ScenarioStatus Status { get; }
        IReadOnlyList<HarborAlarm> Alarms { get; }
        IReadOnlyList<HarborCommand> Commands { get; }
        CommandCoverage CommandPoll { get; }
        AlarmCoverage AlarmObservation { get; }
        bool TryToken(string externalId, out string token);
        bool TryLocation(string externalId, out HarborLocation location);
        RolloutEvidence Rollout { get; }
        IReadOnlyList<SuppressionEvidence> Suppressions { get; }
        bool TryAttribute(string externalId, string key, out Reading value);
        /// <summary>Read/parse failures, shown alongside last evidence and its observation age.</summary>
        string ReadProblem { get; }
    }

    /// <summary>WORLD only: weather and runner plans. Alarm colours must never style these cues.</summary>
    public sealed class StoryCues
    {
        public Weather Weather { get; private set; }
        public double Surge { get; private set; }
        public double EastFeederOutage { get; private set; }
        public string LampCrewDevice { get; private set; } = "hp-light-east-3";
        public bool LampCrew { get; private set; }
        public bool ReplacementCrew { get; private set; }
        public bool ChargerOtaNight { get; private set; }
        public string BeatsNote { get; private set; }
        public string OtaNote { get; private set; }
        public string LampCrewNote { get; private set; }
        /// <summary>WORLD feeder fade only; never changes observed platform lamp state.</summary>
        public double FeederPower(string quay) => quay == "hp-east-quay" ? 1 - EastFeederOutage : 1;

        public static StoryCues At(HarborLayout layout, ScenarioStatus status, ClockReading clock, bool settleWorldFades = false)
        {
            if (settleWorldFades && clock.Mode != ClockMode.Preview) throw new InvalidOperationException("settled WORLD fades are only available in preview stills");
            var beats = new List<StoryBeat>();
            if (status != null && status.BeatsFromRunner) beats.AddRange(status.Beats);
            else foreach (var b in layout.Beats) beats.Add(new StoryBeat(b.Name, b.From, b.To));
            var s = new StoryCues { BeatsNote = status != null && status.BeatsFromRunner ? "runner beats" : "committed layout beats (runner did not report beats)",
                LampCrewNote = "maintenance beat; lamp-service job not reported",
                OtaNote = status?.Ota == null ? "S5 OTA not reported; no OTA cue" : "runner OTA plan" };
            var h = clock.HourOfDay;
            var length = status?.DayLengthSeconds ?? layout.DayLengthSeconds;
            foreach (var b in beats)
            {
                var x = RelativeHour(h, b.From);
                var end = b.To <= b.From ? b.To + 24 : b.To;
                if (b.Name == "storm")
                {
                    s.Weather = Weather.Window(b.From, end, x);
                    // Same relative envelope as the simulator's storm surge; follows the declared storm window.
                    s.Surge = Smooth(b.From + 1, b.From + 2.5, x) * (1 - Smooth(end - 2, end, x));
                }
                if (b.Name == "feeder-outage")
                {
                    // Convert two WALL seconds to scenario hours; ramps lie inside the window edges.
                    var fade = 2 * 24 / length;
                    s.EastFeederOutage = settleWorldFades ? (Inside(x, b.From, end) ? 1 : 0)
                        : Smooth(b.From, b.From + fade, x) * (1 - Smooth(end - fade, end, x));
                }
                if (b.Name == "lamp-maintenance") s.LampCrew = x >= b.From && x < end;
            }
            if (status != null && status.StartedAt.HasValue)
            {
                var jobReported = false;
                foreach (var j in status.Jobs)
                    if (j.Name == "lamp-service" && j.WindowOpens.HasValue && j.WindowCloses.HasValue)
                    {
                        if (!jobReported) { s.LampCrew = false; jobReported = true; }
                        var active = Inside(clock.SimSeconds, status.ToSimSeconds(j.WindowOpens.Value), status.ToSimSeconds(j.WindowCloses.Value));
                        s.LampCrew |= active;
                        if (active) s.LampCrewDevice = j.Device;
                        s.LampCrewNote = "runner's lamp-service plan";
                    }
                if (status.Replacement != null)
                {
                    var at = status.ToSimSeconds(status.Replacement.At);
                    s.ReplacementCrew = Inside(clock.SimSeconds, at, at + 0.5 * 3600);
                }
                if (status.Ota != null)
                    s.ChargerOtaNight = Inside(clock.SimSeconds, status.Ota.ScheduleHour * 3600, (status.Ota.ScheduleHour + 1) * 3600);
            }
            return s;
        }

        static double RelativeHour(double h, double start) => h < start && start > 12 ? h + 24 : h;
        static bool Inside(double x, double a, double b) => x >= a && x < b;
        static double Smooth(double a, double b, double x) { var t = Rgb.Clamp01((x - a) / (b - a)); return t * t * (3 - 2 * t); }
    }
}
