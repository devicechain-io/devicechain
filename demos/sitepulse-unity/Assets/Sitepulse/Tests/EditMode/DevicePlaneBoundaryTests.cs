// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Ingest;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.DevicePlane;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Visuals;
using NUnit.Framework;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    // The seams where our types meet the SDK, the platform's stored state and the scene.
    public sealed class DevicePlaneBoundaryTests
    {
        // ---- the SDK boundary

        [Test]
        public void EverySessionStateMapsToItsOwnLinkState()
        {
            Assert.AreEqual(LinkState.Starting, SdkDeviceLink.Map(MqttSessionState.Starting));
            Assert.AreEqual(LinkState.Ready, SdkDeviceLink.Map(MqttSessionState.Ready));
            Assert.AreEqual(LinkState.Reconnecting, SdkDeviceLink.Map(MqttSessionState.Reconnecting));
            Assert.AreEqual(LinkState.Blind, SdkDeviceLink.Map(MqttSessionState.Blind), "a refused device must not read as merely stopped");
            Assert.AreEqual(LinkState.Stopped, SdkDeviceLink.Map(MqttSessionState.Stopped));
            foreach (MqttSessionState s in Enum.GetValues(typeof(MqttSessionState)))
                Assert.DoesNotThrow(() => SdkDeviceLink.Map(s), s.ToString());
        }

        sealed class CapturingCarrier : IDeviceEventCarrier
        {
            public string Token;
            public byte[] Body;

            public Task SendAsync(string deviceToken, byte[] jsonEvent, CancellationToken cancellationToken)
            {
                Token = deviceToken;
                Body = jsonEvent;
                return Task.CompletedTask;
            }
        }

        static double Num(JsonElement e) =>
            e.ValueKind == JsonValueKind.String ? double.Parse(e.GetString(), CultureInfo.InvariantCulture) : e.GetDouble();

        [Test]
        public void ALocationSampleReachesTheSdkWithEachFieldInItsOwnPlace()
        {
            var carrier = new CapturingCarrier();
            var link = SdkDeviceLink.OverCarrier(carrier, "dev-token", "cred-id");
            var at = new DateTimeOffset(2026, 10, 5, 12, 34, 56, 789, TimeSpan.Zero);
            var sample = Sample.Location(7, at, new GeoPoint(39.0004, -116.9996), 1234.5, 6.25, 271.5);

            link.PublishAsync(sample, CancellationToken.None).GetAwaiter().GetResult();

            Assert.AreEqual("dev-token", carrier.Token);
            using var doc = JsonDocument.Parse(carrier.Body);
            var root = doc.RootElement;
            Assert.AreEqual("Location", root.GetProperty("eventType").GetString());
            Assert.AreEqual("cred-id", root.GetProperty("credentialId").GetString());
            var entry = root.GetProperty("payload").GetProperty("entries")[0];
            Assert.AreEqual(6.25, Num(entry.GetProperty("speed")), 1e-9, "speed is the speed");
            Assert.AreEqual(271.5, Num(entry.GetProperty("heading")), 1e-9, "heading is the heading");
            Assert.AreEqual(1234.5, Num(entry.GetProperty("elevation")), 1e-9);
            Assert.AreEqual(39.0004, Num(entry.GetProperty("latitude")), 1e-9);
            Assert.AreEqual(-116.9996, Num(entry.GetProperty("longitude")), 1e-9);
            var expected = at.UtcDateTime.ToString("O", CultureInfo.InvariantCulture);
            Assert.AreEqual(expected, root.GetProperty("occurredTime").GetString(), "the sample's own time, not the send time");
            Assert.AreEqual(expected, entry.GetProperty("occurredTime").GetString());
        }

        [Test]
        public void AMeasurementSampleKeepsItsValuesAndItsOwnTime()
        {
            var carrier = new CapturingCarrier();
            var link = SdkDeviceLink.OverCarrier(carrier, "dev-token", "cred-id");
            var at = new DateTimeOffset(2026, 10, 5, 1, 2, 3, TimeSpan.Zero);
            var sample = Sample.Measurement(1, at, new Dictionary<string, double> { [MeasurementKeys.FuelPct] = 61.25, [MeasurementKeys.EngineTempC] = 88.4 });
            link.PublishAsync(sample, CancellationToken.None).GetAwaiter().GetResult();

            using var doc = JsonDocument.Parse(carrier.Body);
            var m = doc.RootElement.GetProperty("payload").GetProperty("entries")[0].GetProperty("measurements");
            Assert.AreEqual(61.25, Num(m.GetProperty(MeasurementKeys.FuelPct)), 1e-9);
            Assert.AreEqual(88.4, Num(m.GetProperty(MeasurementKeys.EngineTempC)), 1e-9);
            Assert.AreEqual(at.UtcDateTime.ToString("O", CultureInfo.InvariantCulture), doc.RootElement.GetProperty("occurredTime").GetString());
        }

        // ---- resuming from the platform's last observed values

        static IReadOnlyDictionary<string, double> Platform(double? fuel, double? hours)
        {
            var d = new Dictionary<string, double>();
            if (fuel.HasValue) d[MeasurementKeys.FuelPct] = fuel.Value;
            if (hours.HasValue) d[MeasurementKeys.EngineHours] = hours.Value;
            return d;
        }

        [Test]
        public void AValueThePlatformHasWinsOverTheSeed()
        {
            var c = LastState.Choose(80.0, 5000.0, Platform(41.5, 7321.125));
            Assert.AreEqual(41.5, c.FuelPct);
            Assert.AreEqual(StateSource.Platform, c.FuelSource);
            Assert.AreEqual(7321.125, c.EngineHours);
            Assert.AreEqual(StateSource.Platform, c.EngineHoursSource);
        }

        [Test]
        public void AValueThePlatformLacksFallsBackToTheSeedPerValue()
        {
            var none = LastState.Choose(80.0, 5000.0, null);
            Assert.AreEqual(80.0, none.FuelPct);
            Assert.AreEqual(StateSource.Seed, none.FuelSource);
            Assert.AreEqual(StateSource.Seed, none.EngineHoursSource);

            var onlyFuel = LastState.Choose(80.0, 5000.0, Platform(41.5, null));
            Assert.AreEqual(41.5, onlyFuel.FuelPct);
            Assert.AreEqual(5000.0, onlyFuel.EngineHours);
            Assert.AreEqual(StateSource.Seed, onlyFuel.EngineHoursSource);

            var junk = LastState.Choose(80.0, 5000.0, Platform(double.NaN, double.PositiveInfinity));
            Assert.AreEqual(StateSource.Seed, junk.FuelSource);
            Assert.AreEqual(StateSource.Seed, junk.EngineHoursSource);
        }

        [Test]
        public void APlatformFuelBelowTheSeedRangeIsUsedAsItIs()
        {
            // the seed range is 35 to 95; a tank the platform saw at 9 percent is 9 percent
            var c = LastState.Choose(60.0, 5000.0, Platform(9.0, 5000.5));
            Assert.AreEqual(9.0, c.FuelPct);
            Assert.AreEqual(StateSource.Platform, c.FuelSource);
            var empty = LastState.Choose(60.0, 5000.0, Platform(0.0, null));
            Assert.AreEqual(0.0, empty.FuelPct);
            Assert.AreEqual(StateSource.Platform, empty.FuelSource);
        }

        [Test]
        public void ARestoredMachineStartsWhereThePlatformLastSawItAndOnlyBeforeItHasRun()
        {
            var m = new MachineModel(EquipmentKind.Hauler, "SP-HL-0001");
            m.Restore(9.0, 7000.25);
            Assert.AreEqual(9.0, m.FuelPct);
            Assert.AreEqual(7000.25, m.EngineHours);
            m.Step(1, new MachineInput(3, true));
            Assert.Throws<InvalidOperationException>(() => m.Restore(90, 1), "restoring is not a way to refuel a running machine");
            Assert.Throws<InvalidOperationException>(() => new MachineModel(EquipmentKind.Plant, "SP-PL-0001").Restore(50, 1));
            Assert.Throws<ArgumentOutOfRangeException>(() => new MachineModel(EquipmentKind.Hauler, "SP-HL-0002").Restore(101, 1));
        }

        [Test]
        public void ARelaunchedFleetKeepsItsFuelAndLogsWhereEachValueCameFrom()
        {
            // two "launches" over the same platform state start every machine from the same level,
            // where the seed alone would have given a different one
            var seed = new MachineModel(EquipmentKind.Hauler, "SP-HL-0001").FuelPct;
            var platformFuel = seed - 10.0;
            var state = Platform(platformFuel, 6000.0);
            for (var launch = 0; launch < 2; launch++)
            {
                var m = new MachineModel(EquipmentKind.Hauler, "SP-HL-0001");
                var c = LastState.Choose(m.FuelPct, m.EngineHours, state);
                m.Restore(c.FuelPct, c.EngineHours);
                Assert.AreEqual(platformFuel, m.FuelPct, 1e-12, $"launch {launch}");
            }
        }

        [Test]
        public void ThePlatformsLastValuesAreParsedPerDeviceAndDevicesItLacksAreAbsent()
        {
            var tokens = new[] { "tok-a", "tok-b", "tok-c" };
            const string data = "{\"d0\":[{\"name\":\"fuel_pct\",\"value\":41.5},{\"name\":\"engine_hours\",\"value\":7000.125},{\"name\":\"note\",\"value\":null}]," +
                                "\"d1\":[],\"d2\":[{\"name\":\"fuel_pct\",\"value\":0}]}";
            var parsed = LastStateQuery.Parse(tokens, data);
            Assert.AreEqual(2, parsed.Count);
            Assert.AreEqual(41.5, parsed["tok-a"][MeasurementKeys.FuelPct]);
            Assert.AreEqual(7000.125, parsed["tok-a"][MeasurementKeys.EngineHours]);
            Assert.IsFalse(parsed["tok-a"].ContainsKey("note"));
            Assert.IsFalse(parsed.ContainsKey("tok-b"));
            Assert.AreEqual(0.0, parsed["tok-c"][MeasurementKeys.FuelPct]);

            StringAssert.Contains("$t2: String!", LastStateQuery.Build(tokens));
            StringAssert.Contains("d1: latestMeasurements(deviceToken: $t1)", LastStateQuery.Build(tokens));
            using var vars = JsonDocument.Parse(LastStateQuery.Variables(tokens));
            Assert.AreEqual("tok-c", vars.RootElement.GetProperty("t2").GetString());
        }

        [Test]
        public void AFailedQueryYieldsNoStateSoEveryMachineFallsBackToItsSeed()
        {
            QueryFn boom = (q, v, ct) => Task.FromException<string>(new InvalidOperationException("device-state is down"));
            var got = LastStateQuery.FetchAsync(boom, new[] { "tok-a" }, CancellationToken.None).GetAwaiter().GetResult();
            Assert.AreEqual(0, got.Count);
        }

        // ---- the scene's poses

        [Test]
        public void ADestroyedRigIsNeverReadAgainAndTheReplacementIsFound()
        {
            var made = new List<GameObject>();
            MachineRig Make(string name, Vector3 at)
            {
                var go = new GameObject(name);
                made.Add(go);
                go.transform.position = at;
                return go.AddComponent<MachineRig>();
            }

            try
            {
                var rigs = new List<MachineRig> { Make("SP-HL-0001", new Vector3(10, 5, 20)) };
                var source = new RigPoseSource(() => rigs.Count, () => rigs, null);
                Assert.IsTrue(source.TryGet("SP-HL-0001", out var first));
                Assert.AreEqual(10.0, first.East);
                Assert.AreEqual(20.0, first.North);

                // the fleet respawns at the same size: the cached rig is destroyed, a new one stands in its place
                UnityEngine.Object.DestroyImmediate(rigs[0].gameObject);
                rigs[0] = Make("SP-HL-0001", new Vector3(30, 5, 40));
                Assert.IsTrue(source.TryGet("SP-HL-0001", out var again));
                Assert.AreEqual(30.0, again.East, "the live rig, not the destroyed one");
                Assert.AreEqual(40.0, again.North);

                // destroyed and not replaced: no pose, never an exception
                UnityEngine.Object.DestroyImmediate(rigs[0].gameObject);
                rigs.Clear();
                Assert.IsFalse(source.TryGet("SP-HL-0001", out _));
            }
            finally
            {
                foreach (var go in made) if (go != null) UnityEngine.Object.DestroyImmediate(go);
            }
        }
    }
}
