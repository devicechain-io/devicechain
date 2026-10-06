// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Visuals;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>
    /// The Live source: a card's values are what the platform reported for its device, read from
    /// <see cref="ObservedState"/> and from nothing else. It has no way to write a value that was not
    /// there: a device with no observation fills nothing, and the card shows a dash. Every value is
    /// written as <see cref="Provenance.Observed"/> with its own occurrence and observation times, which
    /// is what the freshness of a card is judged from. The speed is the device's last Location event's
    /// <c>speed</c> (the platform stores metres per second) converted to km/h. It is handed a device's id
    /// and nothing of the scene's choreography (<see cref="ReadingSubject"/> carries no machine and no speed).
    /// </summary>
    public sealed class ObservedReadingSource : IReadingSource
    {
        public const double MetresPerSecondToKmh = 3.6;

        readonly ObservedState state;
        readonly Func<string, string> tokenOf;
        readonly Func<bool> streamLive;
        readonly List<ObservedAlarm> scratch = new List<ObservedAlarm>();
        readonly HashSet<string> raisedKeys = new HashSet<string>(StringComparer.Ordinal);
        readonly HashSet<string> warned = new HashSet<string>(StringComparer.Ordinal);

        /// <param name="tokenOf">A scene device's id to its platform token; null while it has none.</param>
        /// <param name="streamLive">Whether the measurement stream is live right now.</param>
        public ObservedReadingSource(ObservedState state, Func<string, string> tokenOf, Func<bool> streamLive)
        {
            this.state = state ?? throw new ArgumentNullException(nameof(state));
            this.tokenOf = tokenOf ?? throw new ArgumentNullException(nameof(tokenOf));
            this.streamLive = streamLive ?? throw new ArgumentNullException(nameof(streamLive));
        }

        public Provenance Provenance => Provenance.Observed;

        public bool StreamLive => streamLive();

        public void Fill(in ReadingSubject subject, DeviceReading reading, DateTimeOffset now)
        {
            const Provenance P = Provenance.Observed;
            reading.ClearAlarms();
            reading.ClearCommand();
            var token = tokenOf(subject.DeviceId);
            if (token == null || !state.TryGet(token, out var device)) return;

            var keys = reading.Kind == DeviceReading.Profile.Equipment ? MeasurementKeys.Equipment : MeasurementKeys.Plant;
            foreach (var key in keys)
                if (device.Measurements.TryGetValue(key, out var v)) reading.Set(key, v.Value, P, new Observation(v.OccurredAt, v.ObservedAt));
            if (reading.Kind == DeviceReading.Profile.Plant)
                foreach (var key in MeasurementKeys.PlantFlags)
                    if (device.Measurements.TryGetValue(key, out var v)) reading.Set(key, v.Value != 0.0, P, new Observation(v.OccurredAt, v.ObservedAt));

            if (reading.Kind != DeviceReading.Profile.Equipment) return;

            var loc = device.Location;
            if (loc != null && loc.SpeedMps.HasValue)
                reading.SetSpeedKmh(loc.SpeedMps.Value * MetresPerSecondToKmh, P, new Observation(loc.OccurredAt, loc.ObservedAt));

            // the newest active alarm first: it is the one the card's bar names
            scratch.Clear();
            foreach (var a in device.Alarms.Values)
                if (a.IsActive) scratch.Add(a);
            scratch.Sort((x, y) => y.OccurredAt != x.OccurredAt ? y.OccurredAt.CompareTo(x.OccurredAt) : string.CompareOrdinal(x.Token, y.Token));
            raisedKeys.Clear();
            foreach (var a in scratch)
            {
                if (!Contains(AlarmKeys.All, a.AlarmKey))
                {
                    Warn("alarm:" + a.AlarmKey, $"the platform raised alarm \"{a.AlarmKey}\" on {subject.DeviceId}, which no Sitepulse card knows how to show");
                    continue;
                }

                // one alarm per key on a card, and it is the newest one's: DeviceReading.Raise replaces an earlier
                // raise of the same key, so an older alarm met later must not overwrite the newer one
                if (!raisedKeys.Add(a.AlarmKey)) continue;
                reading.Raise(a.AlarmKey, P, a.Severity, a.State, new Observation(a.OccurredAt, a.ObservedAt));
            }

            var c = device.LastCommand;
            if (c != null)
            {
                if (Contains(CommandKeys.Equipment, c.Name))
                    reading.SetCommand(c.Name, CommandStatus.Parse(c.Status), P, new Observation(c.QueuedAt, c.ObservedAt));
                else
                    Warn("command:" + c.Name, $"the platform has command \"{c.Name}\" for {subject.DeviceId}, which no Sitepulse card knows how to show");
            }
        }

        void Warn(string key, string line)
        {
            if (warned.Add(key)) PlatformLog.Warn(line);
        }

        static bool Contains(IReadOnlyList<string> list, string key)
        {
            for (var i = 0; i < list.Count; i++)
                if (list[i] == key) return true;
            return false;
        }
    }
}
