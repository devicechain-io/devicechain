// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text.RegularExpressions;
using DeviceChain.Towerpulse.Model;
using DeviceChain.Towerpulse.Plane;
using NUnit.Framework;

namespace DeviceChain.Towerpulse.Tests
{
    /// <summary>The device plane's engine-free half: the wire vocabulary against the platform scenario, the 31 devices, the readings, and command completion.</summary>
    public class PlaneTests
    {
        const string GoDir = "backend/sims/dc-simulator/sim/";

        static string RepoFile(string rel)
        {
            foreach (var start in new[] { AppContext.BaseDirectory, Environment.CurrentDirectory })
            {
                var dir = new DirectoryInfo(start);
                while (dir != null)
                {
                    var p = Path.Combine(dir.FullName, rel);
                    if (File.Exists(p)) return p;
                    dir = dir.Parent;
                }
            }
            Assert.Fail("cannot find " + rel + " above " + Environment.CurrentDirectory);
            return null;
        }

        static string Go(string file) { return File.ReadAllText(RepoFile(GoDir + file)); }

        static string GoConst(string src, string name)
        {
            var m = Regex.Match(src, @"\b" + name + @"\s*=\s*""([^""]+)""");
            Assert.That(m.Success, "the Go scenario no longer defines " + name);
            return m.Groups[1].Value;
        }

        // ---- the wire vocabulary

        [Test]
        public void MetricKeysCommandKeysAndTypesMatchTheGoScenario()
        {
            var src = Go("towerpulse.go");
            var pairs = new Dictionary<string, string>
            {
                { "TowerpulseTempKey", Wire.Temp }, { "TowerpulseSetpointKey", Wire.Setpoint }, { "TowerpulseCO2Key", Wire.Co2 },
                { "TowerpulseOccupancyKey", Wire.Occupancy }, { "TowerpulseDamperKey", Wire.Damper },
                { "TowerpulseSupplyAirKey", Wire.SupplyAir }, { "TowerpulseFanKey", Wire.Fan }, { "TowerpulseFilterDPKey", Wire.FilterDp },
                { "TowerpulseAHUModeKey", Wire.Mode }, { "TowerpulseChillerSupplyKey", Wire.ChwSupply }, { "TowerpulsePowerKey", Wire.Power },
                { "TowerpulseCoolingKey", Wire.Cooling }, { "TowerpulseRunningKey", Wire.Running }, { "TowerpulseMeterKwKey", Wire.MeterKw },
                { "TowerpulseMeterKwhKey", Wire.MeterKwh },
                { "TowerpulseBoostCommandKey", Wire.BoostCooling }, { "TowerpulseSetpointCommandKey", Wire.SetSetpoint },
                { "TowerpulseModeCommandKey", Wire.SetMode }, { "TowerpulseRestartCommandKey", Wire.Restart },
                { "TowerpulseVAVTypeToken", Wire.VavType }, { "TowerpulseAHUTypeToken", Wire.AhuType },
                { "TowerpulseChillerTypeToken", Wire.ChillerType }, { "TowerpulseMeterTypeToken", Wire.MeterType },
                { "TowerpulseSetpointParameter", Wire.CelsiusParam },
            };
            foreach (var kv in pairs) Assert.That(kv.Value, Is.EqualTo(GoConst(src, kv.Key)), kv.Key);
        }

        [Test]
        public void ParameterNamesAndBoundsMatchTheGoCommandSchemas()
        {
            var src = Go("towerpulse.go");
            var boost = GoConst2(src, "towerpulseBoostParameterSchema");
            Assert.That(boost, Does.Contain("\"name\":\"minutes\"").And.Contain("\"minValue\":" + (int)Wire.BoostMinutesMin).And.Contain("\"maxValue\":" + (int)Wire.BoostMinutesMax));
            var sp = GoConst2(src, "towerpulseSetpointParameterSchema");
            Assert.That(sp, Does.Contain("\"name\":\"celsius\"").And.Contain("\"minValue\":" + (int)Wire.SetpointMin).And.Contain("\"maxValue\":" + (int)Wire.SetpointMax));
            Assert.That(GoConst2(src, "towerpulseModeParameterSchema"), Does.Contain("\"name\":\"mode\"").And.Contain("\"normal\",\"economy\",\"boost\""));
        }

        static string GoConst2(string src, string name)
        {
            var m = Regex.Match(src, name + @"\s*=\s*`([^`]+)`");
            Assert.That(m.Success, "the Go scenario no longer defines " + name);
            return m.Groups[1].Value;
        }

        [Test]
        public void ProfilesDeclareEveryMetricTheDeviceSendsAndTheCalculatedOnesAreNeverSent()
        {
            var src = Go("towerpulse.go");
            Assert.That(src, Does.Contain("Expression: TowerpulseCOPFormula"), "cop is a calculated metric");
            Assert.That(src, Does.Contain("Expression: TowerpulseKwhPerM2Formula"), "kwh_per_m2 is a calculated metric");
            var plane = new TowerPlane(1, PlaneClock.FromHour(7));
            foreach (var d in plane.Devices)
            {
                var r = Readings.For(plane.Model, d);
                Assert.That(r.Keys, Does.Not.Contain("cop").And.Not.Contain("kwh_per_m2"), d.ExternalId);
                var allowed = d.Kind == PlaneKind.Vav ? Wire.VavMetrics : d.Kind == PlaneKind.Ahu ? Wire.AhuMetrics
                    : d.Kind == PlaneKind.Chiller ? Wire.ChillerNumericMetrics.Concat(Wire.ChillerBooleanMetrics).ToArray() : Wire.MeterMetrics;
                Assert.That(r.Keys.OrderBy(k => k), Is.EqualTo(allowed.OrderBy(k => k)), d.ExternalId);
            }
        }

        // ---- the 31 devices

        [Test]
        public void ThereAreThirtyOneDevicesWithUniqueTokensAndExternalIds()
        {
            var devs = DeviceMap.All(BuildingLayout.Standard());
            Assert.That(devs.Count, Is.EqualTo(31));
            Assert.That(devs.Count(d => d.Kind == PlaneKind.Vav), Is.EqualTo(24));
            Assert.That(devs.Count(d => d.Kind == PlaneKind.Ahu), Is.EqualTo(3));
            Assert.That(devs.Count(d => d.Kind == PlaneKind.Chiller), Is.EqualTo(2));
            Assert.That(devs.Count(d => d.Kind == PlaneKind.Meter), Is.EqualTo(2));
            Assert.That(devs.Select(d => d.Token).Distinct().Count(), Is.EqualTo(31));
            Assert.That(devs.Select(d => d.ExternalId).Distinct().Count(), Is.EqualTo(31));
            Assert.That(devs.Select(d => d.LayoutId).Distinct().Count(), Is.EqualTo(31));
        }

        [Test]
        public void TheStoryDevicesCarryTheScenariosTokens()
        {
            var devs = DeviceMap.All(BuildingLayout.Standard());
            var model = Go("towerpulse_model.go");
            Assert.That(devs.Single(d => d.LayoutId == BuildingLayout.HotZone).Token, Is.EqualTo(GoConst(model, "TowerpulseHotZone")));
            Assert.That(devs.Single(d => d.LayoutId == BuildingLayout.AnnexChiller).Token, Is.EqualTo(GoConst(model, "TowerpulseDropChiller")));
            Assert.That(devs.Single(d => d.LayoutId == BuildingLayout.HotZone).ExternalId, Is.EqualTo("TP-VAV-T-F4-E"));
            Assert.That(devs.Single(d => d.LayoutId == BuildingLayout.AnnexChiller).ExternalId, Is.EqualTo("TP-CH-A"));
            // every token follows the scenario's naming scheme
            var go = Go("towerpulse.go");
            StringAssert.Contains("\"tp-vav-%s-f%d-%s\"", go);
            StringAssert.Contains("\"TP-VAV-%s-F%d-%s\"", go);
            StringAssert.Contains("\"tp-ahu-%s-%d\"", go);
            StringAssert.Contains("\"TP-AHU-%s-%d\"", go);
            StringAssert.Contains("\"tp-chiller-\"", go);
            StringAssert.Contains("\"TP-CH-\"", go);
            StringAssert.Contains("\"tp-meter-\"", go);
            StringAssert.Contains("\"TP-EM-\"", go);
            StringAssert.Contains("towerpulseAnnexDirections = []string{\"north\", \"south\"}", go);
        }

        // ---- readings and the silent chiller

        [Test]
        public void TheAnnexChillerGoesSilentAtTheScriptedTimeButTheOtherReportsOn()
        {
            var plane = new TowerPlane(1, new PlaneClock(TimeClock.At(0, 16, 28)));
            var annex = plane.Devices.Single(d => d.LayoutId == BuildingLayout.AnnexChiller);
            var tower = plane.Devices.First(d => d.Kind == PlaneKind.Chiller && d != annex);
            Assert.That(Readings.For(plane.Model, annex), Is.Not.Null);
            plane.Advance(5 * 60);
            Assert.That(Readings.For(plane.Model, annex), Is.Null, "no telemetry at all: that is what the absence rule sees");
            Assert.That(Readings.NotReporting(plane.Model, annex), Is.True);
            Assert.That(Readings.For(plane.Model, tower), Is.Not.Null);
        }

        // ---- command validation

        [TestCase("boost-cooling", 15.0, true)]
        [TestCase("boost-cooling", 1.0, true)]
        [TestCase("boost-cooling", 120.0, true)]
        [TestCase("boost-cooling", 0.0, false)]
        [TestCase("boost-cooling", 121.0, false)]
        [TestCase("boost-cooling", 2.5, false)]
        public void BoostMinutesFollowTheProfile(string name, double minutes, bool ok)
        {
            var a = new CommandArgs();
            a.Numbers["minutes"] = minutes;
            Assert.That(CommandEngine.Validate(PlaneKind.Vav, name, a) == null, Is.EqualTo(ok));
        }

        [TestCase(18.0, true)]
        [TestCase(28.0, true)]
        [TestCase(17.9, false)]
        [TestCase(28.1, false)]
        public void SetpointFollowsTheProfileRangeNotTheModelsWiderOne(double c, bool ok)
        {
            var a = new CommandArgs();
            a.Numbers["celsius"] = c;
            Assert.That(CommandEngine.Validate(PlaneKind.Vav, "set-setpoint", a) == null, Is.EqualTo(ok));
        }

        [Test]
        public void CommandsThatDoNotBelongToTheDeviceKindAreRefused()
        {
            Assert.That(CommandEngine.Validate(PlaneKind.Meter, "restart", new CommandArgs()), Is.Not.Null);
            Assert.That(CommandEngine.Validate(PlaneKind.Vav, "restart", new CommandArgs()), Is.Not.Null);
            Assert.That(CommandEngine.Validate(PlaneKind.Chiller, "restart", new CommandArgs()), Is.Null);
            var extra = new CommandArgs();
            extra.Numbers["x"] = 1;
            Assert.That(CommandEngine.Validate(PlaneKind.Chiller, "restart", extra), Is.Not.Null, "restart takes no parameters");
            var bad = new CommandArgs { Malformed = true };
            Assert.That(CommandEngine.Validate(PlaneKind.Vav, "boost-cooling", bad), Is.Not.Null);
            var mode = new CommandArgs();
            mode.Strings["mode"] = "turbo";
            Assert.That(CommandEngine.Validate(PlaneKind.Ahu, "set-mode", mode), Is.Not.Null);
        }

        // ---- completion semantics

        static CommandArgs Num(string k, double v) { var a = new CommandArgs(); a.Numbers[k] = v; return a; }

        static void RunWall(TowerPlane p, ref double wall, double seconds)
        {
            double end = wall + seconds;
            while (wall < end) { wall += 5; p.Advance(wall); }
        }

        [Test]
        public void BoostIsNotSuccessfulBeforeTheZoneReachesSetpointAndIsAtSetpoint()
        {
            var plane = new TowerPlane(1, PlaneClock.FromHour(13));
            var zone = plane.Devices.Single(d => d.LayoutId == BuildingLayout.HotZone);
            var z = plane.Model.Zones[plane.Model.Layout.ZoneIndex(zone.LayoutId)];
            double wall = 0;
            CommandResult? set = null, boost = null;
            plane.Commands.Submit(zone, "c-set", "set-setpoint", Num("celsius", 22), r => set = r);
            Assert.That(set.HasValue && set.Value.Success, Is.True, "set-setpoint is applied and answered at once");
            plane.Commands.Submit(zone, "c-boost", "boost-cooling", Num("minutes", 120), r => boost = r);
            Assert.That(boost.HasValue, Is.False, "nothing answered on arrival");

            int checks = 0;
            while (!boost.HasValue && wall < 3 * 3600)
            {
                RunWall(plane, ref wall, 30);
                if (!boost.HasValue)
                {
                    checks++;
                    Assert.That(z.TempC, Is.GreaterThan(22 + BuildingModel.SetpointToleranceC), "still pending only while the room is above its setpoint");
                }
            }
            Assert.That(checks, Is.GreaterThan(3), "it took real cooling time, it was not instant");
            Assert.That(boost.HasValue, Is.True);
            Assert.That(boost.Value.Success, Is.True, boost.Value.Reason);
            Assert.That(z.TempC, Is.LessThanOrEqualTo(22 + BuildingModel.SetpointToleranceC), "SUCCESSFUL means the zone is at its setpoint");
        }

        [Test]
        public void ABoostThatCannotReachSetpointInItsWindowIsReportedFailedNotSuccessful()
        {
            var plane = new TowerPlane(1, PlaneClock.FromHour(13));
            var zone = plane.Devices.Single(d => d.LayoutId == BuildingLayout.HotZone);
            double wall = 0;
            CommandResult? set = null, boost = null;
            plane.Commands.Submit(zone, "s", "set-setpoint", Num("celsius", 22), r => set = r);
            plane.Commands.Submit(zone, "b", "boost-cooling", Num("minutes", 1), r => boost = r);
            RunWall(plane, ref wall, 120);
            Assert.That(boost.HasValue, Is.True);
            Assert.That(boost.Value.Success, Is.False);
            StringAssert.Contains("expired", boost.Value.Reason);
        }

        [Test]
        public void RestartBringsTheChillerBackAndOnlyThenIsSuccessful()
        {
            var plane = new TowerPlane(1, new PlaneClock(TimeClock.At(0, 16, 29)));
            var ch = plane.Devices.Single(d => d.LayoutId == BuildingLayout.AnnexChiller);
            double wall = 0;
            RunWall(plane, ref wall, 4 * 60);
            Assert.That(Readings.For(plane.Model, ch), Is.Null);
            CommandResult? res = null;
            plane.Commands.Submit(ch, "r1", "restart", new CommandArgs(), r => res = r);
            Assert.That(res.HasValue, Is.False);
            RunWall(plane, ref wall, 60);
            Assert.That(res.HasValue, Is.False, "not answered while the chiller is still coming up");
            var mid = Readings.For(plane.Model, ch);
            Assert.That(mid, Is.Not.Null, "it reports again as soon as the controller is back, so the absence rule can clear");
            Assert.That(mid[Wire.Running], Is.EqualTo(0));
            int guard = 0;
            while (!res.HasValue && guard++ < 400) RunWall(plane, ref wall, 10);
            Assert.That(res.HasValue, Is.True);
            Assert.That(res.Value.Success, Is.True, res.Value.Reason);
            var after = Readings.For(plane.Model, ch);
            Assert.That(after[Wire.Running], Is.EqualTo(1));
            Assert.That(after[Wire.ChwSupply], Is.LessThanOrEqualTo(BuildingModel.CompleteChwC));
        }

        [Test]
        public void SetModeIsAppliedAtOnce()
        {
            var plane = new TowerPlane(1, PlaneClock.FromHour(9));
            var ahu = plane.Devices.First(d => d.Kind == PlaneKind.Ahu);
            var a = new CommandArgs();
            a.Strings["mode"] = "economy";
            CommandResult? res = null;
            plane.Commands.Submit(ahu, "m", "set-mode", a, r => res = r);
            Assert.That(res.HasValue && res.Value.Success, Is.True);
            Assert.That(Readings.For(plane.Model, ahu)[Wire.Mode], Is.EqualTo(1));
        }

        // ---- delivery identity: one actuation, every delivery answered

        sealed class Replies
        {
            public readonly List<string> Order = new List<string>();
            public readonly Dictionary<string, CommandResult> ByNonce = new Dictionary<string, CommandResult>();
            public Action<CommandResult> For(string nonce) { return r => { Order.Add(nonce); ByNonce[nonce] = r; }; }
        }

        static TowerPlane HotPlane(out PlaneDevice zone)
        {
            var plane = new TowerPlane(1, PlaneClock.FromHour(13));
            zone = plane.Devices.Single(d => d.LayoutId == BuildingLayout.HotZone);
            plane.Commands.Submit(zone, "s", "set-setpoint", Num("celsius", 22), _ => { });
            return plane;
        }

        [Test]
        public void ASameNonceDuplicateWhilePendingActuatesOnceAndBothCopiesAreAnswered()
        {
            var plane = HotPlane(out var zone);
            int before = plane.Commands.Actuations;
            var rep = new Replies();
            plane.Commands.Submit(zone, "tok", "boost-cooling", Num("minutes", 120), rep.For("n1"));
            plane.Commands.Submit(zone, "tok", "boost-cooling", Num("minutes", 120), rep.For("n1"));
            Assert.That(plane.Commands.Actuations - before, Is.EqualTo(1));
            double wall = 0;
            while (rep.Order.Count < 2 && wall < 3 * 3600) RunWall(plane, ref wall, 30);
            Assert.That(rep.Order.Count, Is.EqualTo(2));
            Assert.That(rep.ByNonce["n1"].Success, Is.True);
        }

        [Test]
        public void ANewNonceWhilePendingGetsTheEventualOutcomeWithoutReactuating()
        {
            var plane = HotPlane(out var zone);
            int before = plane.Commands.Actuations;
            var rep = new Replies();
            plane.Commands.Submit(zone, "tok", "boost-cooling", Num("minutes", 120), rep.For("n1"));
            double wall = 0;
            RunWall(plane, ref wall, 60);
            plane.Commands.Submit(zone, "tok", "boost-cooling", Num("minutes", 120), rep.For("n2"));
            Assert.That(rep.Order, Is.Empty);
            while (rep.Order.Count < 2 && wall < 3 * 3600) RunWall(plane, ref wall, 30);
            Assert.That(plane.Commands.Actuations - before, Is.EqualTo(1), "the machine must not move twice");
            Assert.That(rep.ByNonce.Keys, Is.EquivalentTo(new[] { "n1", "n2" }), "each distinct attempt is answered under its own nonce");
            Assert.That(rep.ByNonce["n2"].Success, Is.EqualTo(rep.ByNonce["n1"].Success));
        }

        [Test]
        public void ANewNonceThenALateOldOneAreBothAnsweredAndTheLateOneChangesNothing()
        {
            var plane = HotPlane(out var zone);
            int before = plane.Commands.Actuations;
            var rep = new Replies();
            plane.Commands.Submit(zone, "tok", "boost-cooling", Num("minutes", 120), rep.For("n1"));
            plane.Commands.Submit(zone, "tok", "boost-cooling", Num("minutes", 120), rep.For("n2"));
            plane.Commands.Submit(zone, "tok", "boost-cooling", Num("minutes", 120), rep.For("n1"));
            double wall = 0;
            while (rep.Order.Count < 3 && wall < 3 * 3600) RunWall(plane, ref wall, 30);
            Assert.That(plane.Commands.Actuations - before, Is.EqualTo(1));
            Assert.That(rep.Order.Count, Is.EqualTo(3));
            Assert.That(rep.ByNonce["n1"].Success && rep.ByNonce["n2"].Success, Is.True, "one outcome, whichever nonce asks");
        }

        [Test]
        public void ACompletedCommandReplayedIsAnsweredAtOnceFromMemory()
        {
            var plane = HotPlane(out var zone);
            var rep = new Replies();
            plane.Commands.Submit(zone, "tok", "boost-cooling", Num("minutes", 120), rep.For("n1"));
            double wall = 0;
            while (rep.Order.Count < 1 && wall < 3 * 3600) RunWall(plane, ref wall, 30);
            int actuated = plane.Commands.Actuations;
            plane.Commands.Submit(zone, "tok", "boost-cooling", Num("minutes", 120), rep.For("n9"));
            Assert.That(rep.ByNonce.ContainsKey("n9"), Is.True, "answered synchronously");
            Assert.That(rep.ByNonce["n9"].Success, Is.EqualTo(rep.ByNonce["n1"].Success));
            Assert.That(plane.Commands.Actuations, Is.EqualTo(actuated));
        }

        // ---- take lifecycle and clock

        [Test]
        public void ResetIsARealRewindFromThePinnedSeed()
        {
            var plane = new TowerPlane(7, PlaneClock.FromHour(13.9));
            var zone = plane.Devices.Single(d => d.LayoutId == BuildingLayout.HotZone);
            var first = Readings.For(plane.Model, zone);
            CommandResult? res = null;
            plane.Commands.Submit(zone, "b", "boost-cooling", Num("minutes", 30), r => res = r);
            plane.Advance(40 * 60);
            Assert.That(plane.Model.Tickets.Count, Is.GreaterThan(0));
            plane.Reset();
            Assert.That(res.HasValue && !res.Value.Success || res.HasValue, Is.True, "a pending command is answered, not left hanging");
            Assert.That(plane.Model.Tickets.Count, Is.EqualTo(0), "tickets are gone");
            Assert.That(plane.Model.TimeS, Is.EqualTo(plane.Clock.StartSimS).Within(BuildingModel.StepS));
            Assert.That(plane.Day.ChillerTripped, Is.False);
            var again = Readings.For(plane.Model, plane.Devices.Single(d => d.LayoutId == BuildingLayout.HotZone));
            foreach (var kv in first) Assert.That(again[kv.Key], Is.EqualTo(kv.Value), kv.Key);
        }

        [Test]
        public void TheDefaultClockIsRealTimeAndAnAcceleratedOneSaysSo()
        {
            var real = PlaneClock.FromHour(13);
            Assert.That(real.Accelerated, Is.False);
            Assert.That(real.SimAt(90) - real.SimAt(0), Is.EqualTo(90));
            StringAssert.Contains("real time", real.Label);
            var fast = PlaneClock.FromHour(13, 4);
            Assert.That(fast.Accelerated, Is.True);
            Assert.That(fast.SimAt(90) - fast.SimAt(0), Is.EqualTo(360));
            StringAssert.Contains("ACCELERATED", fast.Label);
            Assert.Throws<ArgumentOutOfRangeException>(() => PlaneClock.FromHour(13, 0));
        }
    }
}
