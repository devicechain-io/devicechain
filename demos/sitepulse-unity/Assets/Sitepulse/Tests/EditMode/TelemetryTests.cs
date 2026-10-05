// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Sitepulse.Domain;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    public sealed class TelemetryTests
    {
        [Test]
        public void EquipmentCardTakesTheProfilesMeasurementKeys()
        {
            var r = new DeviceReading("SP-HL-0001", DeviceReading.Profile.Equipment);
            foreach (var key in MeasurementKeys.Equipment) r.Set(key, 1.0);
            Assert.AreEqual(new[] { "fuel_pct", "engine_temp_c", "engine_hours", "payload_t", "tyre_pressure_kpa" },
                            MeasurementKeys.Equipment);
            Assert.IsTrue(r.TryGet("payload_t", out var v));
            Assert.AreEqual(1.0, v);
        }

        [Test]
        public void ACardRefusesAMetricItsProfileDoesNotModel()
        {
            var truck = new DeviceReading("SP-HL-0001", DeviceReading.Profile.Equipment);
            Assert.Throws<ArgumentException>(() => truck.Set("haul_cycle_s", 220));
            Assert.Throws<ArgumentException>(() => truck.Set(MeasurementKeys.ThroughputTph, 720));
            var plant = new DeviceReading("SP-PL-0001", DeviceReading.Profile.Plant);
            Assert.Throws<ArgumentException>(() => plant.Set(MeasurementKeys.FuelPct, 50));
            Assert.DoesNotThrow(() => plant.Set(MeasurementKeys.ThroughputTph, 720));
        }

        [Test]
        public void OnlyTheRulesAlarmsCanBeRaised()
        {
            var truck = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment);
            truck.Raise(AlarmKeys.LowFuel).Raise(AlarmKeys.LowFuel);
            Assert.AreEqual(new[] { "low-fuel" }, truck.Alarms);
            Assert.Throws<ArgumentException>(() => truck.Raise("brake-wear"));
            var plant = new DeviceReading("SP-PL-0001", DeviceReading.Profile.Plant);
            Assert.Throws<ArgumentException>(() => plant.Raise(AlarmKeys.EngineOverheat));
            // spelled as the platform's rules spell them
            Assert.AreEqual(new[] { "low-fuel", "engine-overheat", "tyre-pressure-low" }, AlarmKeys.All);
            Assert.Throws<ArgumentException>(() => truck.Raise("overheat"));
        }

        [Test]
        public void ValuesCarryTheirUnits()
        {
            var r = new DeviceReading("SP-LD-0003", DeviceReading.Profile.Equipment)
                .Set(MeasurementKeys.FuelPct, 57).Set(MeasurementKeys.PayloadT, 6.2).Set(MeasurementKeys.TyrePressureKpa, 540)
                .Set(MeasurementKeys.EngineTempC, 91).Set(MeasurementKeys.EngineHours, 4210);
            Assert.AreEqual("57 %", r.Format(MeasurementKeys.FuelPct));
            Assert.AreEqual("6.2 t", r.Format(MeasurementKeys.PayloadT));
            Assert.AreEqual("540 kPa", r.Format(MeasurementKeys.TyrePressureKpa));
            Assert.AreEqual("91 °C", r.Format(MeasurementKeys.EngineTempC));
            Assert.AreEqual("4210 h", r.Format(MeasurementKeys.EngineHours));
            r.Set(MeasurementKeys.PayloadT, 0);
            Assert.AreEqual("0 t", r.Format(MeasurementKeys.PayloadT));
            Assert.IsNull(new DeviceReading("x", DeviceReading.Profile.Plant).Format(MeasurementKeys.ThroughputTph));
        }

        [Test]
        public void TheRunningStateIsThePlantsBooleanMetric()
        {
            var plant = new DeviceReading("SP-PL-0001", DeviceReading.Profile.Plant).Set(MeasurementKeys.PlantRunning, true);
            Assert.IsTrue(plant.TryGetFlag(MeasurementKeys.PlantRunning, out bool running) && running);
            Assert.AreEqual("Yes", plant.Format(MeasurementKeys.PlantRunning));
            plant.Set(MeasurementKeys.PlantRunning, false);
            Assert.AreEqual("No", plant.Format(MeasurementKeys.PlantRunning));
            // a boolean is not a number, and the equipment profile has no running state
            Assert.Throws<ArgumentException>(() => plant.Set(MeasurementKeys.PlantRunning, 1.0));
            Assert.Throws<ArgumentException>(() => plant.Set(MeasurementKeys.ThroughputTph, true));
            Assert.Throws<ArgumentException>(() => new DeviceReading("SP-HL-0001", DeviceReading.Profile.Equipment).Set(MeasurementKeys.PlantRunning, true));
        }

        [Test]
        public void OnlyTheProfilesCommandsCanBeRecorded()
        {
            var truck = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment);
            truck.SetCommand(CommandKeys.GotoRefuel, CommandState.Sent);
            Assert.AreEqual("goto-refuel", truck.Command);
            Assert.AreEqual(CommandState.Sent, truck.CommandState);
            truck.SetCommand(CommandKeys.GotoArea, CommandState.Queued);
            Assert.AreEqual("goto-area", truck.Command);
            Assert.AreEqual(new[] { "goto-area", "goto-refuel" }, CommandKeys.Equipment);

            // a command the profile does not define is refused and leaves the last one in place
            Assert.Throws<ArgumentException>(() => truck.SetCommand("return-to-base", CommandState.Sent));
            Assert.Throws<ArgumentException>(() => truck.SetCommand("sp-cmd-refuel", CommandState.Sent));   // a definition token, not a key
            Assert.Throws<ArgumentException>(() => truck.SetCommand(null, CommandState.Sent));
            Assert.AreEqual("goto-area", truck.Command);
            Assert.AreEqual(CommandState.Queued, truck.CommandState);

            // the plant answers no commands
            var plant = new DeviceReading("SP-PL-0001", DeviceReading.Profile.Plant);
            Assert.Throws<ArgumentException>(() => plant.SetCommand(CommandKeys.GotoRefuel, CommandState.Sent));
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
        public void CommandStatesAreThePlatformsFour()
        {
            Assert.AreEqual(new[] { "QUEUED", "SENT", "SUCCESSFUL", "FAILED" },
                Array.ConvertAll((CommandState[])Enum.GetValues(typeof(CommandState)), DeviceReading.Label));
        }
    }
}
