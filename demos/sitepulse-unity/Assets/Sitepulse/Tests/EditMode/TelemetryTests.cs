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
            Assert.Throws<ArgumentException>(() => plant.Raise(AlarmKeys.Overheat));
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
        public void CommandStatesAreThePlatformsFour()
        {
            Assert.AreEqual(new[] { "QUEUED", "SENT", "SUCCESSFUL", "FAILED" },
                Array.ConvertAll((CommandState[])Enum.GetValues(typeof(CommandState)), DeviceReading.Label));
        }
    }
}
