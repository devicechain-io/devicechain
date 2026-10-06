// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.Domain;
using UnityEngine;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>What a source is asked to fill a reading for: the card's device, by id and nothing else.
    /// The choreography's inputs (the machine in the scene, how fast it moves) are deliberately not here, so
    /// a source that reports what the platform said cannot be handed them; the illustrative source takes
    /// them separately (<see cref="IllustrativeReadingSource.SetModel"/>).</summary>
    public readonly struct ReadingSubject
    {
        public ReadingSubject(string deviceId)
        {
            DeviceId = deviceId;
        }

        public string DeviceId { get; }
    }

    /// <summary>
    /// Where a card's values come from: the one seam between the data layer and what feeds it. The
    /// overlay is handed a source by the composition root and never knows which. Every value a source
    /// writes carries its <see cref="Provenance"/>, which is also the provenance of the readings the
    /// overlay creates for it, so a source cannot write another kind of value.
    /// </summary>
    public interface IReadingSource
    {
        Provenance Provenance { get; }

        /// <summary>The observer is currently live: freshness is judged against it. An illustrative source is always live.</summary>
        bool StreamLive { get; }

        /// <summary>Refresh <paramref name="reading"/> for <paramref name="subject"/> as of <paramref name="now"/>.</summary>
        void Fill(in ReadingSubject subject, DeviceReading reading, DateTimeOffset now);
    }

    /// <summary>
    /// The illustrative source: readings derived from the choreography, under the platform's
    /// measurement keys. What Choreographed mode shows, and labelled as such.
    /// </summary>
    public sealed class IllustrativeReadingSource : IReadingSource
    {
        /// <summary>A wheel loader's bucket load (t), and the loader that feeds the crusher.</summary>
        const float LoaderBucket = 6.2f;
        public const string PlantFeeder = "SP-LD-0003";

        readonly QuarryFleetPreview fleet;
        readonly Dictionary<string, (MachineRig rig, float speed)> model =
            new Dictionary<string, (MachineRig, float)>(StringComparer.Ordinal);
        readonly string alarmMachine;
        readonly string alarmKey;

        public IllustrativeReadingSource(QuarryFleetPreview fleet, string alarmMachine, string alarmKey)
        {
            this.fleet = fleet;
            this.alarmMachine = alarmMachine;
            this.alarmKey = alarmKey;
        }

        public Provenance Provenance => Provenance.Illustrative;

        public bool StreamLive => true;

        /// <summary>The choreography's inputs for a device: the machine that stands for it (null for the plant) and its speed in m/s, smoothed.</summary>
        public void SetModel(string deviceId, MachineRig rig, float speedMetresPerSecond) => model[deviceId] = (rig, speedMetresPerSecond);

        static int Hash(string s)
        {
            int h = 17;
            foreach (char c in s) h = h * 31 + c;
            return Mathf.Abs(h);
        }

        public void Fill(in ReadingSubject subject, DeviceReading r, DateTimeOffset now)
        {
            const Provenance P = Provenance.Illustrative;
            if (!model.TryGetValue(subject.DeviceId, out var m))
                throw new InvalidOperationException($"no model inputs were set for {subject.DeviceId}: call SetModel first");
            var rig = m.rig;
            if (rig == null)
            {
                // what the feeding loader delivers: a bucket a cycle
                float cycle = fleet.CycleOf(PlantFeeder);
                float tph = cycle > 0f ? LoaderBucket / cycle * 3600f : 0f;
                r.Set(MeasurementKeys.ThroughputTph, Mathf.Round(tph / 10f) * 10f, P);
                r.Set(MeasurementKeys.PlantRunning, tph > 0f, P);
                return;
            }

            int h = Hash(rig.name);
            bool loaded = rig.Kind == MachineKind.Loader ? rig.boom < -10f : rig.loaded && rig.dump < 2f;
            float payload = rig.Kind == MachineKind.Loader ? LoaderBucket : 86 + h % 9;
            r.Set(MeasurementKeys.PayloadT, loaded ? payload : 0f, P);
            r.Set(MeasurementKeys.FuelPct, 38 + h % 50, P);
            r.Set(MeasurementKeys.EngineTempC, 86 + h % 5 + (loaded ? 5 : 0), P);
            r.Set(MeasurementKeys.EngineHours, 4200 + h % 3800, P);
            bool alarm = rig.name == alarmMachine;
            r.Set(MeasurementKeys.TyrePressureKpa, alarm && alarmKey == AlarmKeys.TyrePressureLow ? 540 : 690 + h % 25, P);
            r.ClearCommand();
            if (alarm && alarmKey == AlarmKeys.LowFuel)
            {
                // the low-fuel rule sends the truck to the refuel bay. While the alarm is active the tank is
                // still low and the command is still on its way: a refuelled tank would have cleared the alarm,
                // so no successful command is ever shown beside it
                r.Set(MeasurementKeys.FuelPct, 11, P);
                r.SetCommand(CommandKeys.GotoRefuel, CommandStatus.Of(CommandState.Sent), P);
            }

            if (alarm && alarmKey == AlarmKeys.EngineOverheat) r.Set(MeasurementKeys.EngineTempC, 112, P);
            r.SetSpeedKmh(Math.Round(m.speed * 3.6f), P);
            r.ClearAlarms();
            if (alarm) r.Raise(alarmKey, P);
        }
    }
}
