// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Reflection;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Simulation;
using NUnit.Framework;
using UnityEditor;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    public sealed class SimulationTests
    {
        const string Features = "Assets/Sitepulse/Art/Terrain/quarry_features.json";

        [Serializable] sealed class FeatureFile { public Zone[] zones; }
        [Serializable] sealed class Zone { public string token; public float[] rect; }

        // ---- coordinates: the player's half of the contract with sitepulse_geofence.go

        [Test]
        public void TheTerrainCentreIsTheDeclaredOrigin()
        {
            var g = SiteDefinition.ToGeographic(0, 0);
            Assert.AreEqual(39.0, g.Latitude, 0.0);
            Assert.AreEqual(-117.0, g.Longitude, 0.0);
        }

        [Test]
        public void ALiteralVertexOfThePlatformsPitRingIsReproducedToSevenPlaces()
        {
            // sitepulse_geofence.go: {-116.9998611, 39.0008743}, // x=12 z=97.22
            var g = SiteDefinition.ToGeographic(12, 97.22);
            Assert.AreEqual(39.0008743, g.Latitude, 5e-8);
            Assert.AreEqual(-116.9998611, g.Longitude, 5e-8);

            // and a vertex far from the first: {-116.9993672, 39.0008700}, // x=54.68 z=96.74
            g = SiteDefinition.ToGeographic(54.68, 96.74);
            Assert.AreEqual(39.0008700, g.Latitude, 5e-8);
            Assert.AreEqual(-116.9993672, g.Longitude, 5e-8);
        }

        [Test]
        public void AxesAreEastAndNorth()
        {
            var east = SiteDefinition.ToGeographic(100, 0);
            var north = SiteDefinition.ToGeographic(0, 100);
            Assert.Greater(east.Longitude, -117.0);
            Assert.AreEqual(39.0, east.Latitude, 0.0);
            Assert.Greater(north.Latitude, 39.0);
            Assert.AreEqual(-117.0, north.Longitude, 0.0);
            // 100 m north is about 0.0009 degrees; 100 m east is wider in degrees by 1/cos(39)
            Assert.AreEqual(0.000899, north.Latitude - 39.0, 1e-6);
            Assert.AreEqual(0.001157, east.Longitude + 117.0, 1e-6);
        }

        [Test]
        public void ZoneCornersAndTheCentreRoundTrip()
        {
            var file = JsonUtility.FromJson<FeatureFile>(File.ReadAllText(Features));
            Assert.Greater(file.zones.Length, 0);
            var points = new List<(double x, double z)> { (0, 0) };
            foreach (var z in file.zones)
            {
                Assert.AreEqual(4, z.rect.Length, z.token);
                foreach (var x in new[] { z.rect[0], z.rect[1] })
                    foreach (var zz in new[] { z.rect[2], z.rect[3] })
                        points.Add((x, zz));
            }

            foreach (var (x, z) in points)
            {
                var g = SiteDefinition.ToGeographic(x, z);
                SiteDefinition.ToLocal(g.Latitude, g.Longitude, out var e, out var n);
                Assert.AreEqual(x, e, 1e-6, $"east of ({x}, {z})");
                Assert.AreEqual(z, n, 1e-6, $"north of ({x}, {z})");
            }
        }

        [Test]
        public void ElevationIsTerrainHeightPlusTheDeclaredDatum()
        {
            Assert.AreEqual(1800.0, SiteDefinition.SiteDatumEllipsoidHeightM);
            Assert.AreEqual(1800.0, SiteDefinition.EllipsoidHeight(0), 0.0);
            Assert.AreEqual(1823.5, SiteDefinition.EllipsoidHeight(23.5), 1e-9);
            Assert.AreEqual(1790.0, SiteDefinition.EllipsoidHeight(-10), 1e-9);
        }

        [Test]
        public void HeadingIsDegreesClockwiseFromNorth()
        {
            Assert.AreEqual(0.0, SiteDefinition.HeadingDegrees(0, 1), 1e-9);      // north
            Assert.AreEqual(90.0, SiteDefinition.HeadingDegrees(1, 0), 1e-9);     // east
            Assert.AreEqual(180.0, SiteDefinition.HeadingDegrees(0, -1), 1e-9);   // south
            Assert.AreEqual(270.0, SiteDefinition.HeadingDegrees(-1, 0), 1e-9);   // west
            Assert.AreEqual(45.0, SiteDefinition.HeadingDegrees(1, 1), 1e-9);
            // never the platform's refused 360
            Assert.AreEqual(0.0, SiteDefinition.HeadingDegrees(-1e-9, 1), 0.0);
        }

        // ---- speed and heading from motion

        static MotionEstimator Drive(double vEast, double vNorth, double seconds, double facing = 0)
        {
            var m = new MotionEstimator();
            double e = 0, n = 0;
            m.Update(e, n, 0.1, facing);
            for (var i = 0; i < (int)(seconds * 10); i++)
            {
                e += vEast * 0.1;
                n += vNorth * 0.1;
                m.Update(e, n, 0.1, facing);
            }

            return m;
        }

        [Test]
        public void MovingEastIsNinetyDegreesAtTheMachinesSpeed()
        {
            var m = Drive(5, 0, 8);
            Assert.AreEqual(5.0, m.SpeedMps, 0.05);
            Assert.AreEqual(90.0, m.HeadingDegrees, 0.5);
            Assert.IsTrue(m.IsMoving);
            Assert.AreEqual(0.0, Drive(0, 4, 6).HeadingDegrees, 0.5);
            Assert.AreEqual(270.0, Drive(-3, 0, 6).HeadingDegrees, 0.5);
            Assert.AreEqual(180.0, Drive(0, -3, 6).HeadingDegrees, 0.5);
        }

        [Test]
        public void AParkedMachineHoldsItsHeadingAndReportsNoSpeed()
        {
            var m = Drive(0, 0, 5, facing: 123.0);
            Assert.AreEqual(0.0, m.SpeedMps, 1e-9);
            Assert.AreEqual(123.0, m.HeadingDegrees, 1e-9);
            Assert.IsFalse(m.IsMoving);

            // after driving east and stopping it keeps east rather than swinging north
            var e = Drive(5, 0, 6);
            for (var i = 0; i < 100; i++) e.Update(30, 0, 0.1, 0);
            Assert.AreEqual(0.0, e.SpeedMps, 0.01);
            Assert.AreEqual(90.0, e.HeadingDegrees, 0.5);
            Assert.IsFalse(e.IsMoving);
        }

        [Test]
        public void ATeleportIsNotSpeed()
        {
            var m = Drive(5, 0, 4);
            m.Update(10000, 10000, 0.1, 0);
            Assert.Less(m.SpeedMps, 1.0);
        }

        // ---- the model

        static readonly string[] EquipmentKeys =
        {
            MeasurementKeys.FuelPct, MeasurementKeys.EngineTempC, MeasurementKeys.EngineHours,
            MeasurementKeys.PayloadT, MeasurementKeys.TyrePressureKpa,
        };

        [Test]
        public void EachKindReportsExactlyItsOwnKeys()
        {
            CollectionAssert.AreEquivalent(EquipmentKeys, new MachineModel(EquipmentKind.Hauler, "SP-HL-0001").Measurements().Keys);
            CollectionAssert.AreEquivalent(EquipmentKeys, new MachineModel(EquipmentKind.Loader, "SP-LD-0001").Measurements().Keys);
            CollectionAssert.AreEquivalent(
                new[] { MeasurementKeys.ThroughputTph, MeasurementKeys.PlantRunning },
                new MachineModel(EquipmentKind.Plant, "SP-PL-0001").Measurements().Keys);
        }

        [Test]
        public void ADozerEmitsNoTyrePressureAndNoPayload()
        {
            var m = new MachineModel(EquipmentKind.Dozer, "SP-DZ-0001");
            for (var i = 0; i < 100; i++) m.Step(1, new MachineInput(2, true));
            var keys = m.Measurements().Keys.ToList();
            CollectionAssert.AreEquivalent(new[] { MeasurementKeys.FuelPct, MeasurementKeys.EngineTempC, MeasurementKeys.EngineHours }, keys);
            CollectionAssert.DoesNotContain(keys, MeasurementKeys.TyrePressureKpa);
            CollectionAssert.DoesNotContain(keys, MeasurementKeys.PayloadT);
        }

        [Test]
        public void ThePlantsRunningFlagEncodesAsOneAndZero()
        {
            var p = new MachineModel(EquipmentKind.Plant, "SP-PL-0001");
            p.Step(1, new MachineInput(0, false, running: true));
            Assert.AreEqual(1.0, p.Measurements()[MeasurementKeys.PlantRunning], 0.0);
            Assert.AreEqual("1", p.Measurements()[MeasurementKeys.PlantRunning].ToString(System.Globalization.CultureInfo.InvariantCulture));
            for (var i = 0; i < 120; i++) p.Step(1, new MachineInput(0, false, running: false));
            Assert.AreEqual(0.0, p.Measurements()[MeasurementKeys.PlantRunning], 0.0);
            Assert.AreEqual(0.0, p.Measurements()[MeasurementKeys.ThroughputTph], 0.0);
        }

        [Test]
        public void APlantRunsAtAThroughputInItsBand()
        {
            var p = new MachineModel(EquipmentKind.Plant, "SP-PL-0001");
            for (var i = 0; i < 3600; i++)
            {
                p.Step(1, new MachineInput(0, false));
                Assert.That(p.ThroughputTph, Is.InRange(740.0, 900.0));
            }
        }

        [Test]
        public void PayloadIsTheLoadWhenLoadedAndZeroWhenEmpty()
        {
            var h = new MachineModel(EquipmentKind.Hauler, "SP-HL-0003");
            h.Step(1, new MachineInput(3, false));
            Assert.AreEqual(0.0, h.Measurements()[MeasurementKeys.PayloadT], 0.0);
            var seen = new HashSet<double>();
            for (var trip = 0; trip < 20; trip++)
            {
                h.Step(1, new MachineInput(3, true));
                var t = h.Measurements()[MeasurementKeys.PayloadT];
                Assert.That(t, Is.InRange(86.0, 94.0));
                seen.Add(t);
                for (var i = 0; i < 5; i++) h.Step(1, new MachineInput(3, true));
                Assert.AreEqual(t, h.Measurements()[MeasurementKeys.PayloadT], 0.0, "a load does not change while it is carried");
                h.Step(1, new MachineInput(3, false));
                Assert.AreEqual(0.0, h.Measurements()[MeasurementKeys.PayloadT], 0.0);
            }

            Assert.Greater(seen.Count, 1, "each trip carries its own load");

            var l = new MachineModel(EquipmentKind.Loader, "SP-LD-0001");
            l.Step(1, new MachineInput(0, true));
            Assert.That(l.Measurements()[MeasurementKeys.PayloadT], Is.InRange(11.0, 15.0));
        }

        [Test]
        public void SeedsAreDeterministicPerDevice()
        {
            var a = new MachineModel(EquipmentKind.Hauler, "SP-HL-0001").Measurements();
            var b = new MachineModel(EquipmentKind.Hauler, "SP-HL-0001").Measurements();
            var c = new MachineModel(EquipmentKind.Hauler, "SP-HL-0002").Measurements();
            foreach (var kv in a) Assert.AreEqual(kv.Value, b[kv.Key], 0.0, kv.Key);
            Assert.AreNotEqual(a[MeasurementKeys.FuelPct], c[MeasurementKeys.FuelPct]);
        }

        [Test]
        public void EngineHoursAccumulateInRealTime()
        {
            var m = new MachineModel(EquipmentKind.Hauler, "SP-HL-0001");
            var before = m.EngineHours;
            for (var i = 0; i < 3600; i++) m.Step(1, new MachineInput(3, false));
            Assert.AreEqual(1.0, m.EngineHours - before, 1e-6);
        }

        [Test]
        public void FuelDrainsAtAShiftScaleRateNotADemoSpeedRate()
        {
            foreach (var kind in new[] { EquipmentKind.Hauler, EquipmentKind.Loader, EquipmentKind.Dozer })
            {
                var m = new MachineModel(kind, "SP-X-0001");
                var before = m.FuelPct;
                for (var i = 0; i < 3600; i++) m.Step(1, new MachineInput(3, true));
                Assert.That(before - m.FuelPct, Is.InRange(0.3, 2.0), kind.ToString());
            }
        }

        // ---- the fuel invariant

        [Test]
        public void FuelNeverRisesWithoutAPermit()
        {
            var rng = new System.Random(11);
            foreach (var kind in new[] { EquipmentKind.Hauler, EquipmentKind.Loader, EquipmentKind.Dozer })
            {
                var m = new MachineModel(kind, "SP-" + kind + "-0001");
                var last = m.FuelPct;
                for (var i = 0; i < 20000; i++)
                {
                    m.Step(0.1 + rng.NextDouble() * 5, new MachineInput(rng.NextDouble() * 9, rng.Next(2) == 0, rng.Next(2) == 0));
                    Assert.LessOrEqual(m.FuelPct, last, $"{kind} step {i}");
                    Assert.GreaterOrEqual(m.FuelPct, 0.0);
                    last = m.FuelPct;
                }
            }
        }

        [Test]
        public void APermitedRefillDoesRaiseFuelSoTheGuardIsTheOnlyPath()
        {
            var m = new MachineModel(EquipmentKind.Hauler, "SP-HL-0001");
            for (var i = 0; i < 7200; i++) m.Step(1, new MachineInput(3, true));
            var low = m.FuelPct;

            var permit = new RefuelPermit("SP-HL-0001", 95.0);
            m.Refill(permit);
            Assert.AreEqual(95.0, m.FuelPct, 1e-9);
            Assert.Greater(m.FuelPct, low);

            Assert.Throws<InvalidOperationException>(() => m.Refill(permit), "a permit is single use");
            Assert.Throws<InvalidOperationException>(() => m.Refill(new RefuelPermit("SP-HL-0002", 95.0)), "and names one device");
            Assert.Throws<ArgumentNullException>(() => m.Refill(null));
            Assert.Throws<InvalidOperationException>(() => new MachineModel(EquipmentKind.Plant, "SP-PL-0001").Refill(new RefuelPermit("SP-PL-0001", 90)));

            // a permit never lowers the tank
            m.Refill(new RefuelPermit("SP-HL-0001", 10.0));
            Assert.AreEqual(95.0, m.FuelPct, 1e-9);
        }

        [Test]
        public void NothingOutsideTheAssemblyCanMakeAPermitOrSetFuel()
        {
            Assert.IsEmpty(typeof(RefuelPermit).GetConstructors(BindingFlags.Public | BindingFlags.Instance), "no public constructor");

            var raisers = typeof(MachineModel).GetMethods(BindingFlags.Public | BindingFlags.Instance | BindingFlags.Static)
                .Where(mm => mm.GetParameters().Any(p => p.ParameterType == typeof(RefuelPermit))).ToList();
            Assert.AreEqual(1, raisers.Count);
            Assert.AreEqual("Refill", raisers[0].Name);

            // fuel has a getter and no public setter, and no public method is named for setting it
            var fuel = typeof(MachineModel).GetProperty(nameof(MachineModel.FuelPct));
            Assert.IsNull(fuel.SetMethod);
            Assert.IsEmpty(typeof(MachineModel).GetFields(BindingFlags.Public | BindingFlags.Instance));
        }

        // ---- normal operation stays inside the platform's rules

        // sitepulse.go: low-fuel below 15, overheat above 105, tyre pressure below 600
        const double LowFuel = 15.0, Overheat = 105.0, LowTyre = 600.0;

        static IEnumerable<string> Ids()
        {
            for (var i = 1; i <= 6; i++) yield return $"SP-HL-{i:0000}";
            for (var i = 1; i <= 6; i++) yield return $"SP-LD-{i:0000}";
            for (var i = 1; i <= 6; i++) yield return $"SP-DZ-{i:0000}";
            yield return "SP-PL-0001";
            for (var i = 0; i < 150; i++) yield return "SP-SYN-" + i;
        }

        [Test]
        public void NormalOperationNeverCrossesARuleThresholdOverAnEightHourShift()
        {
            foreach (var id in Ids())
            {
                foreach (var kind in new[] { EquipmentKind.Hauler, EquipmentKind.Loader, EquipmentKind.Dozer })
                {
                    // the three duty cycles: working flat out, hauling with a stop at each end, idling
                    for (var duty = 0; duty < 3; duty++)
                    {
                        var m = new MachineModel(kind, id);
                        Assert.That(m.FuelPct, Is.InRange(35.0, 95.0), id + " seed fuel");
                        double minFuel = m.FuelPct, maxTemp = m.EngineTempC, minTyre = double.MaxValue, maxTyre = 0;
                        for (var s = 0; s < 8 * 3600; s += 2)
                        {
                            var moving = duty == 0 || (duty == 1 && (s / 60) % 2 == 0);
                            var loaded = duty == 0 || (duty == 1 && (s / 120) % 2 == 0);
                            m.Step(2, new MachineInput(moving ? 6 : 0, loaded));
                            minFuel = Math.Min(minFuel, m.FuelPct);
                            maxTemp = Math.Max(maxTemp, m.EngineTempC);
                            if (m.HasTyres)
                            {
                                minTyre = Math.Min(minTyre, m.TyrePressureKpa);
                                maxTyre = Math.Max(maxTyre, m.TyrePressureKpa);
                            }
                        }

                        var where = $"{id} {kind} duty {duty}";
                        Assert.Greater(minFuel, LowFuel + 4.0, where + " fuel");
                        Assert.Less(maxTemp, Overheat - 5.0, where + " temp");
                        Assert.Greater(maxTemp, 80.0, where + " temp");
                        if (m.HasTyres)
                        {
                            Assert.Greater(minTyre, 690.0, where + " tyre");
                            Assert.Less(maxTyre, 720.0, where + " tyre");
                            Assert.Greater(minTyre, LowTyre + 80.0);
                        }
                    }
                }
            }
        }

        // ---- the live choreography

        [Test]
        public void TheLiveChoreographyExistsAndNamesTheSameEighteenMachinesPlusThePlant()
        {
            var live = AssetDatabase.LoadAssetAtPath<TextAsset>("Assets/Sitepulse/Data/quarry_fleet_live.json");
            var five = AssetDatabase.LoadAssetAtPath<TextAsset>("Assets/Sitepulse/Data/quarry_fleet.json");
            Assert.IsNotNull(live, "quarry_fleet_live.json is what Live plays");
            Assert.IsNotNull(five);
            var a = DeviceChain.Sitepulse.App.SitepulseScene.Devices(live.text);
            var b = DeviceChain.Sitepulse.App.SitepulseScene.Devices(five.text);
            CollectionAssert.AreEqual(b.Select(d => d.ExternalId).ToList(), a.Select(d => d.ExternalId).ToList());
            Assert.AreEqual(19, a.Count);
        }
    }
}
