// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Sitepulse.Domain;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    public sealed class TelemetryTests
    {
        const Provenance I = Provenance.Illustrative;

        [Test]
        public void EquipmentCardTakesTheProfilesMeasurementKeys()
        {
            var r = new DeviceReading("SP-HL-0001", DeviceReading.Profile.Equipment);
            foreach (var key in MeasurementKeys.Equipment) r.Set(key, 1.0, I);
            Assert.AreEqual(new[] { "fuel_pct", "engine_temp_c", "engine_hours", "payload_t", "tyre_pressure_kpa" },
                            MeasurementKeys.Equipment);
            Assert.IsTrue(r.TryGet("payload_t", out var v));
            Assert.AreEqual(1.0, v);
        }

        [Test]
        public void ACardRefusesAMetricItsProfileDoesNotModel()
        {
            var truck = new DeviceReading("SP-HL-0001", DeviceReading.Profile.Equipment);
            Assert.Throws<ArgumentException>(() => truck.Set("haul_cycle_s", 220, I));
            Assert.Throws<ArgumentException>(() => truck.Set(MeasurementKeys.ThroughputTph, 720, I));
            var plant = new DeviceReading("SP-PL-0001", DeviceReading.Profile.Plant);
            Assert.Throws<ArgumentException>(() => plant.Set(MeasurementKeys.FuelPct, 50, I));
            Assert.DoesNotThrow(() => plant.Set(MeasurementKeys.ThroughputTph, 720, I));
        }

        [Test]
        public void OnlyTheRulesAlarmsCanBeRaised()
        {
            var truck = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment);
            truck.Raise(AlarmKeys.LowFuel, I).Raise(AlarmKeys.LowFuel, I);
            Assert.AreEqual(1, truck.Alarms.Count);
            Assert.AreEqual("low-fuel", truck.Alarms[0].Key);
            Assert.Throws<ArgumentException>(() => truck.Raise("brake-wear", I));
            var plant = new DeviceReading("SP-PL-0001", DeviceReading.Profile.Plant);
            Assert.Throws<ArgumentException>(() => plant.Raise(AlarmKeys.EngineOverheat, I));
            // spelled as the platform's rules spell them
            Assert.AreEqual(new[] { "low-fuel", "engine-overheat", "tyre-pressure-low" }, AlarmKeys.All);
            Assert.Throws<ArgumentException>(() => truck.Raise("overheat", I));
        }

        [Test]
        public void ValuesCarryTheirUnits()
        {
            var r = new DeviceReading("SP-LD-0003", DeviceReading.Profile.Equipment)
                .Set(MeasurementKeys.FuelPct, 57, I).Set(MeasurementKeys.PayloadT, 6.2, I).Set(MeasurementKeys.TyrePressureKpa, 540, I)
                .Set(MeasurementKeys.EngineTempC, 91, I).Set(MeasurementKeys.EngineHours, 4210, I);
            Assert.AreEqual("57 %", r.Format(MeasurementKeys.FuelPct));
            Assert.AreEqual("6.2 t", r.Format(MeasurementKeys.PayloadT));
            Assert.AreEqual("540 kPa", r.Format(MeasurementKeys.TyrePressureKpa));
            Assert.AreEqual("91 °C", r.Format(MeasurementKeys.EngineTempC));
            Assert.AreEqual("4210 h", r.Format(MeasurementKeys.EngineHours));
            r.Set(MeasurementKeys.PayloadT, 0, I);
            Assert.AreEqual("0 t", r.Format(MeasurementKeys.PayloadT));
            Assert.IsNull(new DeviceReading("x", DeviceReading.Profile.Plant).Format(MeasurementKeys.ThroughputTph));
        }

        [Test]
        public void TheRunningStateIsThePlantsBooleanMetric()
        {
            var plant = new DeviceReading("SP-PL-0001", DeviceReading.Profile.Plant).Set(MeasurementKeys.PlantRunning, true, I);
            Assert.IsTrue(plant.TryGetFlag(MeasurementKeys.PlantRunning, out bool running) && running);
            Assert.AreEqual("Yes", plant.Format(MeasurementKeys.PlantRunning));
            plant.Set(MeasurementKeys.PlantRunning, false, I);
            Assert.AreEqual("No", plant.Format(MeasurementKeys.PlantRunning));
            // a boolean is not a number, and the equipment profile has no running state
            Assert.Throws<ArgumentException>(() => plant.Set(MeasurementKeys.PlantRunning, 1.0, I));
            Assert.Throws<ArgumentException>(() => plant.Set(MeasurementKeys.ThroughputTph, true, I));
            Assert.Throws<ArgumentException>(() => new DeviceReading("SP-HL-0001", DeviceReading.Profile.Equipment).Set(MeasurementKeys.PlantRunning, true, I));
        }

        [Test]
        public void OnlyTheProfilesCommandsCanBeRecorded()
        {
            var truck = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment);
            truck.SetCommand(CommandKeys.GotoRefuel, CommandStatus.Of(CommandState.Sent), I);
            Assert.AreEqual("goto-refuel", truck.Command);
            Assert.AreEqual(CommandState.Sent, truck.CommandStatus.Value.State);
            truck.SetCommand(CommandKeys.GotoArea, CommandStatus.Of(CommandState.Queued), I);
            Assert.AreEqual("goto-area", truck.Command);
            Assert.AreEqual(new[] { "goto-area", "goto-refuel" }, CommandKeys.Equipment);

            // a command the profile does not define is refused and leaves the last one in place
            Assert.Throws<ArgumentException>(() => truck.SetCommand("return-to-base", CommandStatus.Of(CommandState.Sent), I));
            Assert.Throws<ArgumentException>(() => truck.SetCommand("sp-cmd-refuel", CommandStatus.Of(CommandState.Sent), I));   // a definition token, not a key
            Assert.Throws<ArgumentException>(() => truck.SetCommand(null, CommandStatus.Of(CommandState.Sent), I));
            Assert.AreEqual("goto-area", truck.Command);
            Assert.AreEqual(CommandState.Queued, truck.CommandStatus.Value.State);

            // the plant answers no commands
            var plant = new DeviceReading("SP-PL-0001", DeviceReading.Profile.Plant);
            Assert.Throws<ArgumentException>(() => plant.SetCommand(CommandKeys.GotoRefuel, CommandStatus.Of(CommandState.Sent), I));
        }

        [Test]
        public void CardsShowLabelsForTheKeys()
        {
            var labels = new System.Collections.Generic.List<string>();
            foreach (var k in MeasurementKeys.Equipment) labels.Add(MeasurementKeys.Label(k));
            Assert.AreEqual(new[] { "Fuel", "Engine temp", "Engine hours", "Payload", "Tyre pressure" }, labels);
            Assert.AreEqual("Throughput", MeasurementKeys.Label(MeasurementKeys.ThroughputTph));
            Assert.AreEqual("Running", MeasurementKeys.Label(MeasurementKeys.PlantRunning));
            Assert.AreEqual("Low fuel", AlarmKeys.Label(AlarmKeys.LowFuel));
            Assert.AreEqual("Go refuel", CommandKeys.Label(CommandKeys.GotoRefuel));
            Assert.Throws<ArgumentException>(() => MeasurementKeys.Label("haul_cycle_s"));
            Assert.AreEqual(MeasurementKeys.FuelPct, AlarmKeys.Metric(AlarmKeys.LowFuel));
            Assert.AreEqual(MeasurementKeys.EngineTempC, AlarmKeys.Metric(AlarmKeys.EngineOverheat));
            Assert.AreEqual(MeasurementKeys.TyrePressureKpa, AlarmKeys.Metric(AlarmKeys.TyrePressureLow));
        }

        [Test]
        public void EveryPlatformCommandStateKeepsItsOwnName()
        {
            // QUEUED, HELD, SENT, PARKED, SUCCESSFUL, FAILED, TIMEOUT, EXPIRED, CANCELLED: command-delivery's vocabulary.
            // TIMEOUT and CANCELLED are not FAILED, and a card must not say they are.
            var expect = new[]
            {
                ("QUEUED", CommandState.Queued, false), ("HELD", CommandState.Held, false), ("SENT", CommandState.Sent, false),
                ("PARKED", CommandState.Parked, false), ("SUCCESSFUL", CommandState.Successful, true), ("FAILED", CommandState.Failed, true),
                ("TIMEOUT", CommandState.Timeout, true), ("EXPIRED", CommandState.Expired, true), ("CANCELLED", CommandState.Cancelled, true),
            };
            foreach (var (raw, state, terminal) in expect)
            {
                var s = CommandStatus.Parse(raw);
                Assert.AreEqual(state, s.State, raw);
                Assert.AreEqual(raw, s.Label, raw);
                Assert.AreEqual(terminal, s.IsTerminal, raw);
                Assert.AreEqual(raw, CommandStatus.Of(state).Label, raw);
            }

            Assert.AreEqual(expect.Length + 1, Enum.GetValues(typeof(CommandState)).Length, "nine states and Unknown, nothing else");
            Assert.AreNotEqual(CommandStatus.Parse("TIMEOUT").Label, CommandStatus.Parse("FAILED").Label);
            Assert.AreNotEqual(CommandStatus.Parse("CANCELLED").Label, CommandStatus.Parse("FAILED").Label);
        }

        [Test]
        public void AnUnknownCommandStatePrintsWhatThePlatformSaid()
        {
            var s = CommandStatus.Parse("ABORTED");
            Assert.AreEqual(CommandState.Unknown, s.State);
            Assert.AreEqual("ABORTED", s.Label);
            Assert.IsFalse(s.IsTerminal, "an unknown state is not assumed to be finished");
            // the platform's spelling is upper case; anything else is taken as unknown, not guessed at
            Assert.AreEqual(CommandState.Unknown, CommandStatus.Parse("successful").State);
            Assert.AreEqual("successful", CommandStatus.Parse("successful").Label);
            Assert.AreEqual("UNKNOWN", CommandStatus.Parse(null).Label);
            Assert.Throws<ArgumentException>(() => CommandStatus.Of(CommandState.Unknown));
        }

        [Test]
        public void ACardShowsTheCommandStateTheReadingHolds()
        {
            var truck = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment, Provenance.Observed);
            var at = new Observation(T0, T0);
            truck.SetCommand(CommandKeys.GotoRefuel, CommandStatus.Parse("TIMEOUT"), Provenance.Observed, at);
            Assert.AreEqual("TIMEOUT", truck.CommandStatus.Value.Label);
            truck.SetCommand(CommandKeys.GotoRefuel, CommandStatus.Parse("RETIRED"), Provenance.Observed, at);
            Assert.AreEqual("RETIRED", truck.CommandStatus.Value.Label);
            truck.ClearCommand();
            Assert.IsNull(truck.CommandStatus);
        }

        static readonly DateTimeOffset T0 = new DateTimeOffset(2026, 10, 6, 14, 2, 11, TimeSpan.Zero);

        [Test]
        public void AnObservedReadingRefusesAnIllustrativeValueAndTheOtherWayRound()
        {
            var observed = new DeviceReading("SP-HL-0001", DeviceReading.Profile.Equipment, Provenance.Observed);
            var illustrative = new DeviceReading("SP-HL-0001", DeviceReading.Profile.Equipment, Provenance.Illustrative);
            var stamp = new Observation(T0, T0);

            // an illustrative value cannot reach an observed reading, whichever kind of value it is
            Assert.Throws<InvalidOperationException>(() => observed.Set(MeasurementKeys.FuelPct, 40, Provenance.Illustrative));
            Assert.Throws<InvalidOperationException>(() => observed.SetSpeedKmh(12, Provenance.Illustrative));
            Assert.Throws<InvalidOperationException>(() => observed.Raise(AlarmKeys.LowFuel, Provenance.Illustrative));
            Assert.Throws<InvalidOperationException>(() => observed.SetCommand(CommandKeys.GotoRefuel, CommandStatus.Of(CommandState.Sent), Provenance.Illustrative));
            Assert.IsFalse(observed.TryGet(MeasurementKeys.FuelPct, out _));
            Assert.IsNull(observed.SpeedKmh);
            Assert.IsFalse(observed.HasAlarm);
            Assert.IsNull(observed.CommandStatus);

            // and an observed value cannot reach an illustrative one
            Assert.Throws<InvalidOperationException>(() => illustrative.Set(MeasurementKeys.FuelPct, 40, Provenance.Observed, stamp));
            Assert.Throws<InvalidOperationException>(() => illustrative.Set(MeasurementKeys.FuelPct, 40, Provenance.Replayed, stamp));
            Assert.IsFalse(illustrative.TryGet(MeasurementKeys.FuelPct, out _));

            // each accepts its own
            Assert.DoesNotThrow(() => observed.Set(MeasurementKeys.FuelPct, 40, Provenance.Observed, stamp));
            Assert.DoesNotThrow(() => illustrative.Set(MeasurementKeys.FuelPct, 40, Provenance.Illustrative));
            Assert.AreEqual(Provenance.Observed, observed.Provenance);
            Assert.AreEqual(Provenance.Illustrative, illustrative.Provenance);
            Assert.DoesNotThrow(() => new DeviceReading("x", DeviceReading.Profile.Equipment, Provenance.Replayed).Set(MeasurementKeys.FuelPct, 1, Provenance.Replayed, stamp));
        }

        [Test]
        public void AnObservedValueSaysWhenItHappenedAndAnIllustrativeOneHasNoTime()
        {
            var observed = new DeviceReading("SP-HL-0001", DeviceReading.Profile.Equipment, Provenance.Observed);
            Assert.Throws<ArgumentException>(() => observed.Set(MeasurementKeys.FuelPct, 40, Provenance.Observed));
            var illustrative = new DeviceReading("SP-HL-0001", DeviceReading.Profile.Equipment);
            Assert.Throws<ArgumentException>(() => illustrative.Set(MeasurementKeys.FuelPct, 40, Provenance.Illustrative, new Observation(T0, T0)));

            var seen = T0.AddMilliseconds(140);
            observed.Set(MeasurementKeys.FuelPct, 40, Provenance.Observed, new Observation(T0, seen));
            Assert.IsTrue(observed.TryGetStamp(MeasurementKeys.FuelPct, out var st));
            Assert.AreEqual(T0, st.OccurredAt);
            Assert.AreEqual(seen, st.ObservedAt);
            Assert.AreEqual(T0, observed.NewestOccurredAt);
            illustrative.Set(MeasurementKeys.FuelPct, 40, Provenance.Illustrative);
            Assert.IsFalse(illustrative.TryGetStamp(MeasurementKeys.FuelPct, out _));
            Assert.IsNull(illustrative.NewestOccurredAt);
        }

        [Test]
        public void SpeedIsTheLocationEventsAndEquipmentOnly()
        {
            var truck = new DeviceReading("SP-HL-0001", DeviceReading.Profile.Equipment, Provenance.Observed);
            truck.SetSpeedKmh(18.9, Provenance.Observed, new Observation(T0, T0));
            Assert.AreEqual(18.9, truck.SpeedKmh);
            Assert.AreEqual(T0, truck.SpeedStamp.Value.OccurredAt);
            Assert.Throws<ArgumentException>(() => new DeviceReading("SP-PL-0001", DeviceReading.Profile.Plant).SetSpeedKmh(1, Provenance.Illustrative));
        }

        [Test]
        public void AnAlarmCarriesItsSeverityAndState()
        {
            var truck = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment, Provenance.Observed);
            var at = new Observation(T0, T0);
            truck.Raise(AlarmKeys.LowFuel, Provenance.Observed, "CRITICAL", "ACTIVE", at);
            Assert.IsTrue(truck.HasAlarm);
            Assert.AreEqual("CRITICAL", truck.FirstAlarm.Severity);
            Assert.AreEqual("low-fuel", truck.FirstAlarm.Key);
            // an alarm the platform has cleared is carried as cleared and is not an active alarm
            truck.Raise(AlarmKeys.LowFuel, Provenance.Observed, "CRITICAL", "CLEARED", at);
            Assert.AreEqual(1, truck.Alarms.Count);
            Assert.IsFalse(truck.HasAlarm);
            Assert.Throws<InvalidOperationException>(() => truck.FirstAlarm.ToString());
        }
    }
}
