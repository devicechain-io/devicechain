// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.Domain;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>One measurement the platform reported: its value, when it happened on the device and when this app saw it.</summary>
    public readonly struct ObservedValue
    {
        public ObservedValue(double value, DateTimeOffset occurredAt, DateTimeOffset observedAt)
        {
            Value = value;
            OccurredAt = occurredAt;
            ObservedAt = observedAt;
        }

        public double Value { get; }
        public DateTimeOffset OccurredAt { get; }
        public DateTimeOffset ObservedAt { get; }
    }

    /// <summary>A device's last Location event as the platform holds it. Speed is metres per second, as stored.</summary>
    public sealed class ObservedLocation
    {
        public double? SpeedMps { get; set; }
        public double? HeadingDegrees { get; set; }
        public double? ElevationMetres { get; set; }
        public DateTimeOffset OccurredAt { get; set; }
        public DateTimeOffset ObservedAt { get; set; }
    }

    /// <summary>An alarm as the platform last said it was.</summary>
    public sealed class ObservedAlarm
    {
        public const string Active = "ACTIVE";
        public const string Cleared = "CLEARED";

        public string Token { get; set; }
        public string AlarmKey { get; set; }
        public string MetricKey { get; set; }
        public string State { get; set; }
        public string Severity { get; set; }
        public bool Acknowledged { get; set; }

        /// <summary>The transition's event time (a snapshot carries the cycle's raised time instead).</summary>
        public DateTimeOffset OccurredAt { get; set; }
        public DateTimeOffset ObservedAt { get; set; }
        public bool IsActive => State == Active;
    }

    /// <summary>A command as the platform last said it was. The status is the platform's own text.</summary>
    public sealed class ObservedCommand
    {
        public string Token { get; set; }
        public string Name { get; set; }
        public string Status { get; set; }
        public DateTimeOffset QueuedAt { get; set; }
        public DateTimeOffset ObservedAt { get; set; }

        /// <summary>The platform has finished with the command. The one definition of which states those are is <see cref="CommandStatus.IsTerminal"/>.</summary>
        public static bool IsTerminalStatus(string status) => CommandStatus.Parse(status).IsTerminal;

        public bool IsTerminal => IsTerminalStatus(Status);
    }

    /// <summary>Whether the platform believes a device is online. Shown in the fleet panel only.</summary>
    public sealed class ObservedPresence
    {
        public bool Active { get; set; }
        public DateTimeOffset? LastActivityAt { get; set; }
        public DateTimeOffset ObservedAt { get; set; }
    }

    /// <summary>Everything the platform has reported about one device.</summary>
    public sealed class ObservedDevice
    {
        internal readonly Dictionary<string, ObservedValue> measurements = new Dictionary<string, ObservedValue>(StringComparer.Ordinal);
        internal readonly Dictionary<string, ObservedAlarm> alarms = new Dictionary<string, ObservedAlarm>(StringComparer.Ordinal);
        internal readonly Dictionary<string, ObservedCommand> commands = new Dictionary<string, ObservedCommand>(StringComparer.Ordinal);

        public IReadOnlyDictionary<string, ObservedValue> Measurements => measurements;
        public IReadOnlyDictionary<string, ObservedAlarm> Alarms => alarms;
        public ObservedLocation Location { get; internal set; }
        public ObservedPresence Presence { get; internal set; }

        /// <summary>The command most recently queued for the device, whatever its state; null if none was seen.</summary>
        public ObservedCommand LastCommand
        {
            get
            {
                ObservedCommand last = null;
                foreach (var c in commands.Values)
                    if (last == null || c.QueuedAt > last.QueuedAt || (c.QueuedAt == last.QueuedAt && string.CompareOrdinal(c.Token, last.Token) > 0)) last = c;
                return last;
            }
        }

        /// <summary>
        /// When a measurement from this run of the device (one that happened at or after the state's floor)
        /// was first seen; null until one has been. It is the platform's own confirmation that the device's
        /// telemetry arrived, not that an old value exists.
        /// </summary>
        public DateTimeOffset? FirstOwnObservation { get; internal set; }

        /// <summary>When the newest measurement from this run of the device happened (its own clock); null until one has.</summary>
        public DateTimeOffset? NewestOwnRunAt { get; internal set; }
    }

    /// <summary>
    /// What the platform has reported, keyed by device token: the whole of what an observed card may
    /// show. It is written only from the observer's inbox drain, on the main thread, through members
    /// that are internal to this assembly: the local simulation (which lives in an assembly that cannot
    /// even see this one) has no way to put a value here, and anything shown from here has the
    /// platform's name on it. A merge never goes backwards: a value older than the one held is dropped,
    /// so a snapshot cannot overwrite a newer streamed value, and a terminal command state is never
    /// replaced by a non-terminal one.
    /// </summary>
    public sealed class ObservedState
    {
        const int MaxCommandsPerDevice = 16;

        readonly Dictionary<string, ObservedDevice> devices = new Dictionary<string, ObservedDevice>(StringComparer.Ordinal);
        readonly DateTimeOffset floor;

        /// <param name="ownRunFloor">Measurements that happened before this are not this run's own: they are
        /// shown (with their age) but do not count as the device having been observed.</param>
        public ObservedState(DateTimeOffset ownRunFloor)
        {
            floor = ownRunFloor;
        }

        public DateTimeOffset OwnRunFloor => floor;

        /// <summary>The newest time this app saw a measurement; null before any.</summary>
        public DateTimeOffset? LastMeasurementSeenAt { get; private set; }

        public int Version { get; private set; }

        /// <summary>
        /// Measurements applied from a snapshot (the platform's whole answer after a (re)subscribe), counted whether or not a newer
        /// streamed value already held the key. A reader that sees it grow knows a snapshot refresh was taken; nothing else uses it.
        /// </summary>
        public int SnapshotMeasurements { get; private set; }

        /// <summary>
        /// Told how long after a streamed measurement happened this app saw it (observed minus occurred; both are this host's clock),
        /// for each one that was applied. The acceptance soak records the distribution; nothing else sets it.
        /// </summary>
        public Action<TimeSpan> MeasurementLag { get; set; }

        /// <summary>When the newest measurement of this run happened for a device; null if none has been seen.</summary>
        public DateTimeOffset? NewestOwnRunAt(string deviceToken) => devices.TryGetValue(deviceToken ?? "", out var d) ? d.NewestOwnRunAt : null;

        public bool TryGet(string deviceToken, out ObservedDevice device) => devices.TryGetValue(deviceToken ?? "", out device);

        ObservedDevice Of(string token)
        {
            if (!devices.TryGetValue(token, out var d)) devices[token] = d = new ObservedDevice();
            return d;
        }

        /// <summary>Applies one measurement. Returns true when the device's first own-run measurement was just seen.</summary>
        internal bool ApplyMeasurement(string deviceToken, string name, double value, DateTimeOffset occurredAt, DateTimeOffset observedAt, bool fromSnapshot)
        {
            var d = Of(deviceToken);
            if (fromSnapshot) SnapshotMeasurements++;
            if (d.measurements.TryGetValue(name, out var held))
            {
                // newer wins; at the same instant the stream's word stands over a snapshot's
                if (occurredAt < held.OccurredAt || (occurredAt == held.OccurredAt && fromSnapshot)) return false;
            }

            if (!fromSnapshot) MeasurementLag?.Invoke(observedAt - occurredAt);

            d.measurements[name] = new ObservedValue(value, occurredAt, observedAt);
            if (!LastMeasurementSeenAt.HasValue || observedAt > LastMeasurementSeenAt.Value) LastMeasurementSeenAt = observedAt;
            Version++;
            if (occurredAt < floor) return false;
            if (!d.NewestOwnRunAt.HasValue || occurredAt > d.NewestOwnRunAt.Value) d.NewestOwnRunAt = occurredAt;
            if (d.FirstOwnObservation.HasValue) return false;
            d.FirstOwnObservation = observedAt;
            return true;
        }

        internal void ApplyLocation(string deviceToken, ObservedLocation location)
        {
            var d = Of(deviceToken);
            if (d.Location != null && location.OccurredAt < d.Location.OccurredAt) return;
            d.Location = location;
            Version++;
        }

        /// <summary>Applies one alarm state (a stream event, or a row of the active-alarm snapshot).</summary>
        internal void ApplyAlarm(string deviceToken, ObservedAlarm alarm)
        {
            var d = Of(deviceToken);
            if (d.alarms.TryGetValue(alarm.Token, out var held) && alarm.OccurredAt < held.OccurredAt) return;
            d.alarms[alarm.Token] = alarm;
            Version++;
        }

        /// <summary>
        /// The active-alarm snapshot is the platform's whole answer as of <paramref name="requestedAt"/>: an
        /// alarm held as active that the snapshot does not list, and that no event newer than the request
        /// touched, ended while the observer was not looking. It is marked cleared, as of the snapshot.
        /// </summary>
        internal void ReconcileActiveAlarms(ISet<string> listedTokens, DateTimeOffset requestedAt, DateTimeOffset observedAt)
        {
            foreach (var d in devices.Values)
                foreach (var a in d.alarms.Values)
                {
                    if (!a.IsActive || listedTokens.Contains(a.Token) || a.OccurredAt >= requestedAt) continue;
                    a.State = ObservedAlarm.Cleared;
                    a.ObservedAt = observedAt;
                    Version++;
                }
        }

        internal void ApplyCommand(string deviceToken, ObservedCommand command)
        {
            var d = Of(deviceToken);
            if (d.commands.TryGetValue(command.Token, out var held) && held.IsTerminal && !command.IsTerminal) return;
            d.commands[command.Token] = command;
            Version++;
            if (d.commands.Count <= MaxCommandsPerDevice) return;
            // the device's oldest command, preferring a finished one, makes room
            ObservedCommand drop = null;
            foreach (var c in d.commands.Values)
                if (drop == null || (c.IsTerminal && !drop.IsTerminal) || (c.IsTerminal == drop.IsTerminal && c.QueuedAt < drop.QueuedAt)) drop = c;
            d.commands.Remove(drop.Token);
        }

        internal void ApplyPresence(string deviceToken, ObservedPresence presence)
        {
            Of(deviceToken).Presence = presence;
            Version++;
        }
    }
}
