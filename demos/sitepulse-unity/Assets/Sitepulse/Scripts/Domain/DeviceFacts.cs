// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Domain
{
    /// <summary>A measurement as reported: its value, when it happened on the device and when this app saw it.</summary>
    public readonly struct MeasuredValue
    {
        public MeasuredValue(double value, DateTimeOffset occurredAt, DateTimeOffset observedAt)
        {
            Value = value;
            OccurredAt = occurredAt;
            ObservedAt = observedAt;
        }

        public double Value { get; }
        public DateTimeOffset OccurredAt { get; }
        public DateTimeOffset ObservedAt { get; }
    }

    /// <summary>A device's last Location event's speed (metres per second, as stored) and its times.</summary>
    public readonly struct LocationFact
    {
        public LocationFact(double speedMps, DateTimeOffset occurredAt, DateTimeOffset observedAt)
        {
            SpeedMps = speedMps;
            OccurredAt = occurredAt;
            ObservedAt = observedAt;
        }

        public double SpeedMps { get; }
        public DateTimeOffset OccurredAt { get; }
        public DateTimeOffset ObservedAt { get; }
    }

    /// <summary>An alarm as the platform last said it was.</summary>
    public readonly struct AlarmFact
    {
        public AlarmFact(string token, string key, string state, string severity, DateTimeOffset occurredAt, DateTimeOffset observedAt)
        {
            Token = token;
            Key = key;
            State = state;
            Severity = severity;
            OccurredAt = occurredAt;
            ObservedAt = observedAt;
        }

        public string Token { get; }
        public string Key { get; }
        public string State { get; }
        public string Severity { get; }
        public DateTimeOffset OccurredAt { get; }
        public DateTimeOffset ObservedAt { get; }
    }

    /// <summary>A command as the platform last said it was; the status is the platform's own text.</summary>
    public readonly struct CommandFact
    {
        public CommandFact(string name, string status, DateTimeOffset queuedAt, DateTimeOffset observedAt)
        {
            Name = name;
            Status = status;
            QueuedAt = queuedAt;
            ObservedAt = observedAt;
        }

        public string Name { get; }
        public string Status { get; }
        public DateTimeOffset QueuedAt { get; }
        public DateTimeOffset ObservedAt { get; }
    }

    /// <summary>
    /// What the platform has said about one device, as a card reads it. Two things implement it: the live observer's state and a
    /// recording's replay of it. A card is filled from this and from nothing else, by one filler (<see cref="FactsFiller"/>), so a
    /// live card and the same card replayed are filled by the same code from the same facts.
    /// </summary>
    public interface IDeviceFacts
    {
        bool TryMeasurement(string key, out MeasuredValue value);
        bool TryLocation(out LocationFact location);

        /// <summary>Adds the alarms that are active to <paramref name="into"/>.</summary>
        void ActiveAlarms(List<AlarmFact> into);

        bool TryLastCommand(out CommandFact command);
    }

    /// <summary>
    /// Fills a <see cref="DeviceReading"/> from <see cref="IDeviceFacts"/>. Every value is written under the provenance it was
    /// built with, which must be the reading's own: an observed filler cannot fill a replayed reading, and the reading refuses it.
    /// What the platform has not said stays empty (a dash on the card): nothing is invented.
    /// </summary>
    public sealed class FactsFiller
    {
        public const double MetresPerSecondToKmh = 3.6;

        readonly Provenance provenance;
        readonly Action<string> warn;
        readonly List<AlarmFact> scratch = new List<AlarmFact>();
        readonly HashSet<string> raisedKeys = new HashSet<string>(StringComparer.Ordinal);
        readonly HashSet<string> warned = new HashSet<string>(StringComparer.Ordinal);

        public FactsFiller(Provenance provenance, Action<string> warn = null)
        {
            if (provenance == Provenance.Illustrative) throw new ArgumentException("an illustrative reading has no facts to fill from", nameof(provenance));
            this.provenance = provenance;
            this.warn = warn ?? (_ => { });
        }

        public Provenance Provenance => provenance;

        /// <summary>Clears the reading's alarms and command (they are read afresh each time) and fills what <paramref name="facts"/> holds; <paramref name="facts"/> null fills nothing.</summary>
        public void Fill(string deviceId, IDeviceFacts facts, DeviceReading reading)
        {
            var P = provenance;
            reading.ClearAlarms();
            reading.ClearCommand();
            if (facts == null) return;

            var keys = reading.Kind == DeviceReading.Profile.Equipment ? MeasurementKeys.Equipment : MeasurementKeys.Plant;
            foreach (var key in keys)
                if (facts.TryMeasurement(key, out var v)) reading.Set(key, v.Value, P, new Observation(v.OccurredAt, v.ObservedAt));
            if (reading.Kind == DeviceReading.Profile.Plant)
                foreach (var key in MeasurementKeys.PlantFlags)
                    if (facts.TryMeasurement(key, out var v)) reading.Set(key, v.Value != 0.0, P, new Observation(v.OccurredAt, v.ObservedAt));

            if (reading.Kind != DeviceReading.Profile.Equipment) return;

            if (facts.TryLocation(out var loc))
                reading.SetSpeedKmh(loc.SpeedMps * MetresPerSecondToKmh, P, new Observation(loc.OccurredAt, loc.ObservedAt));

            // the newest active alarm first: it is the one the card's bar names
            scratch.Clear();
            facts.ActiveAlarms(scratch);
            scratch.Sort((x, y) => y.OccurredAt != x.OccurredAt ? y.OccurredAt.CompareTo(x.OccurredAt) : string.CompareOrdinal(x.Token, y.Token));
            raisedKeys.Clear();
            foreach (var a in scratch)
            {
                if (!Contains(AlarmKeys.All, a.Key))
                {
                    Warn("alarm:" + a.Key, $"the platform raised alarm \"{a.Key}\" on {deviceId}, which no Sitepulse card knows how to show");
                    continue;
                }

                // one alarm per key on a card, and it is the newest one's: DeviceReading.Raise replaces an earlier
                // raise of the same key, so an older alarm met later must not overwrite the newer one
                if (!raisedKeys.Add(a.Key)) continue;
                reading.Raise(a.Key, P, a.Severity, a.State, new Observation(a.OccurredAt, a.ObservedAt));
            }

            if (facts.TryLastCommand(out var c))
            {
                if (Contains(CommandKeys.Equipment, c.Name))
                    reading.SetCommand(c.Name, CommandStatus.Parse(c.Status), P, new Observation(c.QueuedAt, c.ObservedAt));
                else
                    Warn("command:" + c.Name, $"the platform has command \"{c.Name}\" for {deviceId}, which no Sitepulse card knows how to show");
            }
        }

        void Warn(string key, string line)
        {
            if (warned.Add(key)) warn(line);
        }

        static bool Contains(IReadOnlyList<string> list, string key)
        {
            for (var i = 0; i < list.Count; i++)
                if (list[i] == key) return true;
            return false;
        }
    }
}
