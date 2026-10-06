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
        public const double MetresPerSecondToKmh = FactsFiller.MetresPerSecondToKmh;

        readonly ObservedState state;
        readonly Func<string, string> tokenOf;
        readonly Func<bool> streamLive;
        readonly FactsFiller filler = new FactsFiller(Provenance.Observed, PlatformLog.Warn);
        readonly Facts facts = new Facts();

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
            var token = tokenOf(subject.DeviceId);
            if (token == null || !state.TryGet(token, out var device))
            {
                filler.Fill(subject.DeviceId, null, reading);
                return;
            }

            facts.Device = device;
            filler.Fill(subject.DeviceId, facts, reading);
            facts.Device = null;
        }

        /// <summary>One device's observed state as the facts a card is filled from.</summary>
        sealed class Facts : IDeviceFacts
        {
            public ObservedDevice Device;

            public bool TryMeasurement(string key, out MeasuredValue value)
            {
                if (Device.Measurements.TryGetValue(key, out var v))
                {
                    value = new MeasuredValue(v.Value, v.OccurredAt, v.ObservedAt);
                    return true;
                }

                value = default;
                return false;
            }

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
                    if (a.IsActive) into.Add(new AlarmFact(a.Token, a.AlarmKey, a.State, a.Severity, a.OccurredAt, a.ObservedAt));
            }

            public bool TryLastCommand(out CommandFact command)
            {
                var c = Device.LastCommand;
                if (c == null)
                {
                    command = default;
                    return false;
                }

                command = new CommandFact(c.Name, c.Status, c.QueuedAt, c.ObservedAt);
                return true;
            }
        }
    }
}
