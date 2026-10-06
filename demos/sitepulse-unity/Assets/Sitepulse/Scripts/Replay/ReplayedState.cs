// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Recording;

namespace DeviceChain.Sitepulse.Replay
{
    /// <summary>
    /// What the platform had said, as the live viewer's app held it, rebuilt from observed.ndjson by applying each line in the order
    /// it was received. It follows the live merge rules (a value older than the one held is dropped; a finished command is never
    /// replaced by an unfinished one; a complete active-alarm snapshot clears what it does not list) because a replay that merged
    /// differently would show cards the viewer never saw. It lives apart from the live state on purpose: the live state is in the
    /// Platform assembly, which a replay does not reference (see <c>ReplayAssemblyTests</c>); the two are held to the same answers by a
    /// test that feeds them one stream.
    /// </summary>
    public sealed class ReplayedState
    {
        const int MaxCommandsPerDevice = 16;

        sealed class AlarmRow
        {
            public string Token, Key, State, Severity;
            public DateTimeOffset OccurredAt, ObservedAt;
            public bool IsActive => State == "ACTIVE";
        }

        sealed class CommandRow
        {
            public string Token, Name, Status;
            public DateTimeOffset QueuedAt, ObservedAt;
            public bool IsTerminal => CommandStatus.Parse(Status).IsTerminal;
        }

        sealed class LocationRow
        {
            public double? SpeedMps;
            public DateTimeOffset OccurredAt, ObservedAt;
        }

        sealed class DeviceState
        {
            public readonly Dictionary<string, MeasuredValue> Measurements = new Dictionary<string, MeasuredValue>(StringComparer.Ordinal);
            public readonly Dictionary<string, AlarmRow> Alarms = new Dictionary<string, AlarmRow>(StringComparer.Ordinal);
            public readonly Dictionary<string, CommandRow> Commands = new Dictionary<string, CommandRow>(StringComparer.Ordinal);
            public LocationRow Location;
        }

        readonly Dictionary<string, DeviceState> devices = new Dictionary<string, DeviceState>(StringComparer.Ordinal);
        readonly Facts facts = new Facts();

        /// <summary>The measurement stream was live (as the live app's status said) at the last line applied.</summary>
        public bool MeasurementsLive { get; private set; }

        public int Applied { get; private set; }

        public void Reset()
        {
            devices.Clear();
            MeasurementsLive = false;
            Applied = 0;
        }

        /// <summary>The facts for a device, or null when nothing was said about it. The returned object is reused: read it before asking again.</summary>
        public IDeviceFacts FactsOf(string deviceToken)
        {
            if (deviceToken == null || !devices.TryGetValue(deviceToken, out var d)) return null;
            facts.Device = d;
            return facts;
        }

        DeviceState Of(string token)
        {
            if (!devices.TryGetValue(token, out var d)) devices[token] = d = new DeviceState();
            return d;
        }

        public void Apply(ObservedLine l)
        {
            Applied++;
            switch (l.K)
            {
                case ObservedKinds.Measurement:
                    ApplyMeasurement(l);
                    break;
                case ObservedKinds.Location:
                    ApplyLocation(l);
                    break;
                case ObservedKinds.Alarm:
                    ApplyAlarm(l);
                    break;
                case ObservedKinds.AlarmSnapshot:
                    var listed = new HashSet<string>(StringComparer.Ordinal);
                    foreach (var a in l.Alarms)
                    {
                        listed.Add(a.Token);
                        ApplyAlarm(a);
                    }

                    if (!l.Truncated) Reconcile(listed, l.RequestedAt ?? l.Utc, l.ObservedAt ?? l.Utc);
                    break;
                case ObservedKinds.Command:
                    ApplyCommand(l);
                    break;
                case ObservedKinds.Status:
                    if (l.Name == "measurements") MeasurementsLive = l.State == "Live";
                    break;
                case ObservedKinds.Presence:
                    break;
            }
        }

        void ApplyMeasurement(ObservedLine l)
        {
            var d = Of(l.Device);
            var occurred = l.OccurredAt ?? l.Utc;
            if (d.Measurements.TryGetValue(l.Name, out var held))
            {
                // newer wins; at the same instant the stream's word stands over a snapshot's
                if (occurred < held.OccurredAt || (occurred == held.OccurredAt && l.FromSnapshot)) return;
            }

            d.Measurements[l.Name] = new MeasuredValue(l.Value, occurred, l.ObservedAt ?? l.Utc);
        }

        void ApplyLocation(ObservedLine l)
        {
            var d = Of(l.Device);
            var occurred = l.OccurredAt ?? l.Utc;
            if (d.Location != null && occurred < d.Location.OccurredAt) return;
            // a location with no speed is held as such: it does not become a speed of zero
            d.Location = new LocationRow { SpeedMps = l.SpeedMps, OccurredAt = occurred, ObservedAt = l.ObservedAt ?? l.Utc };
        }

        void ApplyAlarm(ObservedLine l)
        {
            var d = Of(l.Device);
            var occurred = l.OccurredAt ?? l.Utc;
            if (d.Alarms.TryGetValue(l.Token, out var held) && occurred < held.OccurredAt) return;
            d.Alarms[l.Token] = new AlarmRow { Token = l.Token, Key = l.Name, State = l.State, Severity = l.Severity, OccurredAt = occurred, ObservedAt = l.ObservedAt ?? l.Utc };
        }

        void Reconcile(ISet<string> listed, DateTimeOffset requestedAt, DateTimeOffset observedAt)
        {
            foreach (var d in devices.Values)
                foreach (var a in d.Alarms.Values)
                {
                    if (!a.IsActive || listed.Contains(a.Token) || a.OccurredAt >= requestedAt) continue;
                    a.State = "CLEARED";
                    a.ObservedAt = observedAt;
                }
        }

        void ApplyCommand(ObservedLine l)
        {
            var d = Of(l.Device);
            var row = new CommandRow { Token = l.Token, Name = l.Name, Status = l.State, QueuedAt = l.QueuedAt ?? l.Utc, ObservedAt = l.ObservedAt ?? l.Utc };
            if (d.Commands.TryGetValue(row.Token, out var held) && held.IsTerminal && !row.IsTerminal) return;
            d.Commands[row.Token] = row;
            if (d.Commands.Count <= MaxCommandsPerDevice) return;
            // the device's oldest command, preferring a finished one, makes room
            CommandRow drop = null;
            foreach (var c in d.Commands.Values)
                if (drop == null || (c.IsTerminal && !drop.IsTerminal) || (c.IsTerminal == drop.IsTerminal && c.QueuedAt < drop.QueuedAt)) drop = c;
            d.Commands.Remove(drop.Token);
        }

        sealed class Facts : IDeviceFacts
        {
            public DeviceState Device;

            public bool TryMeasurement(string key, out MeasuredValue value) => Device.Measurements.TryGetValue(key, out value);

            public bool TryLocation(out LocationFact location)
            {
                var loc = Device.Location;
                if (loc != null && loc.SpeedMps.HasValue)
                {
                    location = new LocationFact(loc.SpeedMps.Value, loc.OccurredAt, loc.ObservedAt);
                    return true;
                }

                location = default;
                return false;
            }

            public void ActiveAlarms(List<AlarmFact> into)
            {
                foreach (var a in Device.Alarms.Values)
                    if (a.IsActive) into.Add(new AlarmFact(a.Token, a.Key, a.State, a.Severity, a.OccurredAt, a.ObservedAt));
            }

            public bool TryLastCommand(out CommandFact command)
            {
                CommandRow last = null;
                foreach (var c in Device.Commands.Values)
                    if (last == null || c.QueuedAt > last.QueuedAt || (c.QueuedAt == last.QueuedAt && string.CompareOrdinal(c.Token, last.Token) > 0)) last = c;
                if (last == null)
                {
                    command = default;
                    return false;
                }

                command = new CommandFact(last.Name, last.Status, last.QueuedAt, last.ObservedAt);
                return true;
            }
        }
    }

    /// <summary>
    /// The replay's reading source. Every value it writes is <see cref="Provenance.Replayed"/>: it can fill a replayed reading and
    /// refuses an observed or an illustrative one (the reading's own check), and a recorded value is never relabelled as observed.
    /// </summary>
    public sealed class ReplayReadingSource : DeviceChain.Sitepulse.Visuals.IReadingSource
    {
        readonly ReplaySession session;
        readonly FactsFiller filler;

        public ReplayReadingSource(ReplaySession session, Action<string> warn = null)
        {
            this.session = session ?? throw new ArgumentNullException(nameof(session));
            filler = new FactsFiller(Provenance.Replayed, warn);
        }

        public Provenance Provenance => Provenance.Replayed;

        /// <summary>Whether the measurement stream was live when the viewer saw this moment.</summary>
        public bool StreamLive => session.State.MeasurementsLive;

        public void Fill(in DeviceChain.Sitepulse.Visuals.ReadingSubject subject, DeviceReading reading, DateTimeOffset now)
        {
            var token = session.Header.TokenOf(subject.DeviceId);
            filler.Fill(subject.DeviceId, token == null ? null : session.State.FactsOf(token), reading);
        }
    }
}
