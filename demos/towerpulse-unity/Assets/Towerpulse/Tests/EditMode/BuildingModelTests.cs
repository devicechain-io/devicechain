// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Linq;
using DeviceChain.Towerpulse.Model;
using NUnit.Framework;

namespace DeviceChain.Towerpulse.Tests
{
    /// <summary>The model on its own: the campus, the physics, and each command's completion semantics. No rules, no script beyond what a test sets up.</summary>
    public class BuildingModelTests
    {
        // A model with F4-East full of people on a hot afternoon, nobody doing anything about it.
        static BuildingModel HotAfternoon(ulong seed = 1)
        {
            var m = new BuildingModel(seed, TimeClock.At(0, 6, 0));
            m.OutdoorBiasC = TowerDay.HotDayBiasC;
            m.HoldOccupancy(BuildingLayout.HotZone, TimeClock.At(0, 13, 0), TimeClock.At(0, 18, 0), 14);
            m.AdvanceTo(TimeClock.At(0, 14, 15));
            return m;
        }

        static ZoneState Z(BuildingModel m, string id) { return m.Zones[m.Layout.ZoneIndex(id)]; }

        // ---- the campus

        [Test]
        public void TheCampusIsThirtyOneDevices()
        {
            var l = BuildingLayout.Standard();
            Assert.That(l.Zones.Count, Is.EqualTo(24));
            Assert.That(l.Zones.Count(z => z.Building == "tower"), Is.EqualTo(20));
            Assert.That(l.Zones.Count(z => z.Building == "annex"), Is.EqualTo(4));
            Assert.That(l.Zones.Where(z => z.Building == "tower").Select(z => z.Floor).Distinct().Count(), Is.EqualTo(5));
            Assert.That(l.Ahus.Count, Is.EqualTo(3));
            Assert.That(l.Chillers.Count, Is.EqualTo(2));
            Assert.That(l.Meters.Count, Is.EqualTo(2));
            Assert.That(l.Zones.Select(z => z.Id).Distinct().Count(), Is.EqualTo(24), "zone ids are unique");
            Assert.That(l.ZoneIndex(BuildingLayout.HotZone), Is.GreaterThanOrEqualTo(0));
            Assert.That(l.ChillerIndex(BuildingLayout.AnnexChiller), Is.EqualTo(1));
            // The annex is served by its own chiller, so losing it does not touch the tower.
            Assert.That(l.Zones.Where(z => z.Building == "annex").All(z => l.Ahus[z.Ahu].Chiller == 1), Is.True);
            Assert.That(l.Zones.Where(z => z.Building == "tower").All(z => l.Ahus[z.Ahu].Chiller == 0), Is.True);
        }

        // ---- determinism

        static double[] DayTrace(ulong seed)
        {
            var d = new TowerDay(seed);
            var h = new RuleHarness(d);
            h.RunTo(TimeClock.At(0, 17, 0));
            d.Model.RestartChiller(BuildingLayout.AnnexChiller);
            d.Model.SetSetpoint("tower-f2-north", 23);
            d.Model.SetAhuMode("ahu-tower-low", "economy");
            h.RunTo(TimeClock.At(1, 6, 0));
            return d.Model.Snapshot();
        }

        [Test]
        public void TheSameSeedGivesTheSameTrace()
        {
            Assert.That(DayTrace(42), Is.EqualTo(DayTrace(42)));
        }

        [Test]
        public void ADifferentSeedGivesADifferentTrace()
        {
            Assert.That(DayTrace(42), Is.Not.EqualTo(DayTrace(43)));
        }

        [Test]
        public void HowTimeIsSlicedDoesNotChangeTheTrace()
        {
            var a = new BuildingModel(5);
            var b = new BuildingModel(5);
            var end = TimeClock.At(0, 15, 0);
            a.AdvanceTo(end);
            for (double t = b.TimeS; t < end; t += 137.0) b.AdvanceTo(t);
            b.AdvanceTo(end);
            Assert.That(b.Snapshot(), Is.EqualTo(a.Snapshot()));
        }

        // ---- physics

        [Test]
        public void TheSunHeatsGlassOnlyByDayAndOnlyWhenItFacesIt()
        {
            var m = new BuildingModel(1);
            var east = m.Layout.Zones[m.Layout.ZoneIndex(BuildingLayout.HotZone)];
            var north = m.Layout.Zones[m.Layout.ZoneIndex("tower-f4-north")];
            Assert.That(m.SolarGainW(east, TimeClock.At(0, 3, 0)), Is.EqualTo(0.0), "no sun at night");
            Assert.That(m.SolarGainW(east, TimeClock.At(0, 14, 0)), Is.GreaterThan(1000.0));
            Assert.That(m.SolarGainW(east, TimeClock.At(0, 14, 0)), Is.GreaterThan(m.SolarGainW(east, TimeClock.At(0, 9, 0)) * 2),
                "the east wing's glazed return takes the AFTERNOON sun");
            Assert.That(m.SolarGainW(north, TimeClock.At(0, 14, 0)), Is.LessThan(m.SolarGainW(east, TimeClock.At(0, 14, 0)) / 2));
            for (double h = 0; h < 24; h += 0.25)
                foreach (var z in m.Layout.Zones)
                    Assert.That(m.SolarGainW(z, h * 3600.0), Is.GreaterThanOrEqualTo(0.0), "the sun never cools a room");
        }

        [Test]
        public void PeopleRaiseCo2AndVentilationLowersIt()
        {
            var m = HotAfternoon();
            var z = Z(m, BuildingLayout.HotZone);
            Assert.That(z.Occupancy, Is.EqualTo(14));
            Assert.That(z.Co2Ppm, Is.GreaterThan(Z(m, "tower-f1-north").Co2Ppm + 100), "a full room is stuffier than a quiet one");

            // Same room, same people; one with the damper held fully open for the next 40 minutes.
            var boosted = HotAfternoon();
            boosted.BoostCooling(BuildingLayout.HotZone, 60);
            boosted.AdvanceTo(boosted.TimeS + 40 * 60);
            var stay = HotAfternoon();
            stay.AdvanceTo(stay.TimeS + 40 * 60);
            Assert.That(Z(boosted, BuildingLayout.HotZone).Co2Ppm, Is.LessThan(Z(stay, BuildingLayout.HotZone).Co2Ppm));
        }

        [Test]
        public void AnEmptyBuildingSettlesToOutdoorAirAtNight()
        {
            var m = new BuildingModel(3, TimeClock.At(5, 6, 0));        // a Saturday: nobody comes
            m.AdvanceTo(TimeClock.At(6, 5, 0));
            foreach (var z in m.Zones)
                Assert.That(z.Co2Ppm, Is.EqualTo(BuildingModel.OutdoorCo2Ppm).Within(60), z.Spec.Id);
        }

        // ---- boost-cooling

        [Test]
        public void BoostCompletesOnlyWhenTheZoneReachesSetpoint()
        {
            var m = HotAfternoon();
            var z = Z(m, BuildingLayout.HotZone);
            Assert.That(z.TempC, Is.GreaterThan(z.SetpointC + 1.0), "the premise: the room starts well above setpoint");
            var tk = m.BoostCooling(BuildingLayout.HotZone, 60);
            Assert.That(tk.State, Is.EqualTo(CommandState.Active), "accepted is not done");

            double lastActive = double.NaN;
            while (!tk.IsFinal && m.TimeS < tk.IssuedAtS + 3600)
            {
                m.AdvanceTo(m.TimeS + BuildingModel.StepS);
                if (tk.State == CommandState.Active)
                {
                    Assert.That(z.TempC, Is.GreaterThan(z.SetpointC + BuildingModel.SetpointToleranceC), "still Active only while the room is above setpoint");
                    lastActive = m.TimeS;
                }
            }
            Assert.That(tk.State, Is.EqualTo(CommandState.Completed));
            Assert.That(z.TempC, Is.LessThanOrEqualTo(z.SetpointC + BuildingModel.SetpointToleranceC));
            Assert.That(tk.EndedAtS, Is.GreaterThan(tk.IssuedAtS + 120), "cooling a hot room takes minutes, not an instant");
            Assert.That(tk.EndedAtS, Is.EqualTo(lastActive + BuildingModel.StepS), "completed on the first step the room was at setpoint");
        }

        [Test]
        public void BoostHoldsTheDamperOpenAfterItCompletesThenReleasesIt()
        {
            var m = HotAfternoon();
            var z = Z(m, BuildingLayout.HotZone);
            var tk = m.BoostCooling(BuildingLayout.HotZone, 90);
            while (!tk.IsFinal) m.AdvanceTo(m.TimeS + BuildingModel.StepS);
            Assert.That(z.BoostOverride, Is.True, "completed at setpoint, damper still held");
            Assert.That(z.DamperPct, Is.GreaterThan(90));
            while (z.BoostOverride && m.TimeS < tk.IssuedAtS + 5400) m.AdvanceTo(m.TimeS + BuildingModel.StepS);
            Assert.That(z.BoostOverride, Is.False);
            Assert.That(z.TempC, Is.LessThanOrEqualTo(z.SetpointC - BuildingModel.BoostHoldDownC + 0.1));
            m.AdvanceTo(m.TimeS + 120);
            Assert.That(z.DamperPct, Is.LessThan(100.0));
        }

        [Test]
        public void BoostExpiresWhenTheWindowEndsBeforeSetpoint()
        {
            var m = HotAfternoon();
            var z = Z(m, BuildingLayout.HotZone);
            var tk = m.BoostCooling(BuildingLayout.HotZone, 1);      // one minute cannot cool this room
            m.AdvanceTo(tk.IssuedAtS + 50);
            Assert.That(tk.State, Is.EqualTo(CommandState.Active));
            m.AdvanceTo(tk.IssuedAtS + 70);
            Assert.That(tk.State, Is.EqualTo(CommandState.Expired));
            Assert.That(tk.Reason, Does.Contain("setpoint"));
            Assert.That(tk.EndedAtS, Is.EqualTo(tk.IssuedAtS + 60).Within(BuildingModel.StepS));
            Assert.That(z.TempC, Is.GreaterThan(z.SetpointC + BuildingModel.SetpointToleranceC), "it expired because the room never got there");
            Assert.That(z.Boost, Is.Null);
            Assert.That(z.BoostOverride, Is.False, "an expired boost releases the damper");
        }

        [Test]
        public void BoostOnAnAnnexZoneExpiresWhileItsChillerIsDown()
        {
            var m = new BuildingModel(2, TimeClock.At(0, 6, 0));
            m.OutdoorBiasC = TowerDay.HotDayBiasC;
            m.AdvanceTo(TimeClock.At(0, 13, 0));
            m.TripChiller(BuildingLayout.AnnexChiller);
            m.AdvanceTo(TimeClock.At(0, 14, 0));
            var z = Z(m, "annex-f1-laba");
            m.SetSetpoint(z.Spec.Id, 18);                  // out of reach without chilled water
            var tk = m.BoostCooling(z.Spec.Id, 20);
            m.AdvanceTo(m.TimeS + 25 * 60);
            Assert.That(tk.State, Is.EqualTo(CommandState.Expired));
        }

        [Test]
        public void BoostRefusesNonsenseAndSupersedesItself()
        {
            var m = HotAfternoon();
            Assert.That(m.BoostCooling("nope", 10).State, Is.EqualTo(CommandState.Failed));
            Assert.That(m.BoostCooling(BuildingLayout.HotZone, 0).State, Is.EqualTo(CommandState.Failed));
            Assert.That(m.BoostCooling(BuildingLayout.HotZone, -5).State, Is.EqualTo(CommandState.Failed));
            Assert.That(m.BoostCooling(BuildingLayout.HotZone, double.NaN).State, Is.EqualTo(CommandState.Failed));
            Assert.That(m.BoostCooling(BuildingLayout.HotZone, BuildingModel.MaxBoostMinutes + 1).State, Is.EqualTo(CommandState.Failed));
            Assert.That(Z(m, BuildingLayout.HotZone).Boost, Is.Null, "refused commands change nothing");
            var first = m.BoostCooling(BuildingLayout.HotZone, 30);
            var second = m.BoostCooling(BuildingLayout.HotZone, 30);
            Assert.That(first.State, Is.EqualTo(CommandState.Failed));
            Assert.That(first.Reason, Does.Contain("superseded"));
            Assert.That(second.State, Is.EqualTo(CommandState.Active));
        }

        // ---- set-setpoint and set-mode

        static double Settle(double setpoint)
        {
            var m = new BuildingModel(4, TimeClock.At(0, 6, 0));
            m.AdvanceTo(TimeClock.At(0, 10, 0));
            Assert.That(m.SetSetpoint("tower-f2-north", setpoint).State, Is.EqualTo(CommandState.Completed));
            m.AdvanceTo(TimeClock.At(0, 11, 30));
            return Z(m, "tower-f2-north").TempC;
        }

        [Test]
        public void SetSetpointMovesWhereTheRoomSettles()
        {
            Assert.That(Settle(22.0), Is.LessThan(Settle(26.0) - 1.5));
        }

        [Test]
        public void SetSetpointRefusesOutOfRange()
        {
            var m = new BuildingModel(4);
            Assert.That(m.SetSetpoint("tower-f2-north", 5).State, Is.EqualTo(CommandState.Failed));
            Assert.That(m.SetSetpoint("tower-f2-north", 45).State, Is.EqualTo(CommandState.Failed));
            Assert.That(m.SetSetpoint("tower-f2-north", double.NaN).State, Is.EqualTo(CommandState.Failed));
            Assert.That(m.SetSetpoint("tower-f2-nowhere", 22).State, Is.EqualTo(CommandState.Failed));
            Assert.That(Z(m, "tower-f2-north").SetpointC, Is.EqualTo(BuildingModel.DefaultSetpointC));
        }

        static AhuState RunMode(string mode)
        {
            var m = new BuildingModel(4, TimeClock.At(0, 9, 0));
            Assert.That(m.SetAhuMode("ahu-tower-low", mode).State, Is.EqualTo(CommandState.Completed));
            m.AdvanceTo(TimeClock.At(0, 11, 0));
            return m.Ahus[m.Layout.AhuIndex("ahu-tower-low")];
        }

        [Test]
        public void AhuModeChangesSupplyAirAndFan()
        {
            Assert.That(RunMode("boost").SupplyAirC, Is.LessThan(RunMode("normal").SupplyAirC));
            Assert.That(RunMode("normal").SupplyAirC, Is.LessThan(RunMode("economy").SupplyAirC));
            Assert.That(RunMode("boost").FanPct, Is.GreaterThan(RunMode("economy").FanPct));
            Assert.That(new BuildingModel(1).SetAhuMode("ahu-tower-low", "turbo").State, Is.EqualTo(CommandState.Failed));
            Assert.That(new BuildingModel(1).SetAhuMode("ahu-nowhere", "normal").State, Is.EqualTo(CommandState.Failed));
        }

        // ---- chillers

        [Test]
        public void ATrippedChillerGoesSilentAndTheRestartBringsItBackAfterADelay()
        {
            var m = new BuildingModel(6, TimeClock.At(0, 6, 0));
            m.OutdoorBiasC = TowerDay.HotDayBiasC;
            m.AdvanceTo(TimeClock.At(0, 13, 0));
            var c = m.Chillers[1];
            Assert.That(c.Running && c.Online, Is.True);
            Assert.That(c.PowerKw, Is.GreaterThan(5.0));
            m.TripChiller(BuildingLayout.AnnexChiller);
            Assert.That(c.Online, Is.False, "silent: it reports nothing");
            Assert.That(c.Running, Is.False);
            m.AdvanceTo(m.TimeS + 300);
            Assert.That(c.PowerKw, Is.EqualTo(0.0));
            Assert.That(c.CoolingKw, Is.EqualTo(0.0));
            Assert.That(c.Online, Is.False, "nothing but a command brings it back");
            Assert.That(c.ChwSupplyC, Is.GreaterThan(10.0), "the water warms");
            Assert.That(m.Chillers[0].Running, Is.True, "the tower chiller is untouched");

            var tk = m.RestartChiller(BuildingLayout.AnnexChiller);
            Assert.That(tk.State, Is.EqualTo(CommandState.Active));
            Assert.That(c.Online, Is.True, "the watchdog answers at once");
            Assert.That(c.Running, Is.False, "but the compressor is not running yet");
            m.AdvanceTo(tk.IssuedAtS + BuildingModel.RestartDelayS - 10);
            Assert.That(c.Running, Is.False);
            m.AdvanceTo(tk.IssuedAtS + BuildingModel.RestartDelayS + 10);
            Assert.That(c.Running, Is.True, "running after the delay");
            Assert.That(tk.State, Is.EqualTo(CommandState.Active), "running is not recovered: the water is still warm");
            Assert.That(c.ChwSupplyC, Is.GreaterThan(BuildingModel.CompleteChwC));
            m.AdvanceTo(tk.IssuedAtS + 900);
            Assert.That(tk.State, Is.EqualTo(CommandState.Completed));
            Assert.That(c.ChwSupplyC, Is.LessThanOrEqualTo(BuildingModel.CompleteChwC));
            Assert.That(tk.EndedAtS - tk.IssuedAtS, Is.GreaterThan(BuildingModel.RestartDelayS));
        }

        [Test]
        public void ARestartOfARestartingChillerIsRefusedAndUnknownChillersFail()
        {
            var m = new BuildingModel(6);
            var a = m.RestartChiller("chiller-tower");
            Assert.That(m.RestartChiller("chiller-tower").State, Is.EqualTo(CommandState.Failed));
            Assert.That(a.State, Is.EqualTo(CommandState.Active), "the refused one did not disturb the first");
            Assert.That(m.RestartChiller("chiller-nowhere").State, Is.EqualTo(CommandState.Failed));
            m.TripChiller("chiller-tower");
            Assert.That(a.State, Is.EqualTo(CommandState.Failed), "a trip during a restart fails it");
        }

        [Test]
        public void TheAnnexWarmsWithoutItsChillerAndTheTowerDoesNot()
        {
            var healthy = new BuildingModel(6, TimeClock.At(0, 6, 0));
            var broken = new BuildingModel(6, TimeClock.At(0, 6, 0));
            healthy.OutdoorBiasC = broken.OutdoorBiasC = TowerDay.HotDayBiasC;
            healthy.AdvanceTo(TimeClock.At(0, 12, 0));
            broken.AdvanceTo(TimeClock.At(0, 12, 0));
            broken.TripChiller(BuildingLayout.AnnexChiller);
            healthy.AdvanceTo(TimeClock.At(0, 14, 0));
            broken.AdvanceTo(TimeClock.At(0, 14, 0));
            Assert.That(Z(broken, "annex-f2-labb").TempC, Is.GreaterThan(Z(healthy, "annex-f2-labb").TempC + 2.0));
            Assert.That(Z(broken, "tower-f3-west").TempC, Is.EqualTo(Z(healthy, "tower-f3-west").TempC).Within(1e-9));
        }

        // ---- bounds

        public static void AssertSane(BuildingModel m, double lo, double hi, string when)
        {
            foreach (var z in m.Zones)
            {
                var what = z.Spec.Id + " at " + when;
                Assert.That(double.IsNaN(z.TempC) || double.IsInfinity(z.TempC), Is.False, "temp finite, " + what);
                Assert.That(double.IsNaN(z.Co2Ppm) || double.IsInfinity(z.Co2Ppm), Is.False, "co2 finite, " + what);
                Assert.That(z.TempC, Is.InRange(lo, hi), "temp, " + what);
                Assert.That(z.Co2Ppm, Is.GreaterThanOrEqualTo(400.0), "co2 floor, " + what);
                Assert.That(z.Co2Ppm, Is.LessThan(5000.0), "co2 ceiling, " + what);
                Assert.That(z.DamperPct, Is.InRange(0.0, 100.0), "damper, " + what);
                Assert.That(z.Occupancy, Is.InRange(0, Math.Max(z.Spec.Capacity, TowerDay.MeetingPeople)), "occupancy, " + what);
            }
            foreach (var a in m.Ahus)
            {
                Assert.That(a.SupplyAirC, Is.InRange(5.0, 30.0), "supply air " + a.Spec.Id + " at " + when);
                Assert.That(a.FanPct, Is.InRange(0.0, 100.0), "fan " + a.Spec.Id + " at " + when);
                Assert.That(double.IsNaN(a.FilterDpPa), Is.False, "filter dp " + a.Spec.Id + " at " + when);
            }
            foreach (var c in m.Chillers)
            {
                Assert.That(c.ChwSupplyC, Is.InRange(5.0, 16.0), "chw " + c.Spec.Id + " at " + when);
                Assert.That(c.PowerKw, Is.InRange(0.0, 200.0), "power " + c.Spec.Id + " at " + when);
                Assert.That(c.CoolingKw, Is.GreaterThanOrEqualTo(0.0), "cooling " + c.Spec.Id + " at " + when);
            }
            foreach (var me in m.Meters)
            {
                Assert.That(me.Kw, Is.InRange(0.0, 500.0), "kw " + me.Spec.Id + " at " + when);
                Assert.That(double.IsNaN(me.Kwh), Is.False, "kwh " + me.Spec.Id + " at " + when);
            }
        }

        [Test]
        public void EverythingStaysPhysicalThroughAScriptedDayForSeveralSeeds()
        {
            for (ulong seed = 1; seed <= 6; seed++)
            {
                ulong s = seed;
                var h = new RuleHarness(new TowerDay(s));
                h.RunTo(TimeClock.At(0, 17, 0), t => AssertSane(h.Model, 10, 40, "seed " + s + " " + TimeClock.Format(t)));
            }
        }
    }
}
