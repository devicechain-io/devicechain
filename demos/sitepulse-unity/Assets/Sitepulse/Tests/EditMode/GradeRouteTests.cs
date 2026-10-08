// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    // Routes a loaded haul truck can drive: the grade limit, and what a command does when the only way to a place is steeper.
    public sealed class GradeRouteTests
    {
        /// <summary>Ground defined by a function, so a test says what the slope is.</summary>
        sealed class Field : IHeightField
        {
            readonly Func<double, double, double> f;
            public Field(Func<double, double, double> f) { this.f = f; }
            public bool Covers(double x, double z) => true;
            public double HeightAt(double x, double z) => f(x, z);
        }

        /// <summary>The quarry's own ground with its relief multiplied: the same ramp, steeper.</summary>
        sealed class Steeper : IHeightField
        {
            readonly IHeightField inner;
            readonly double factor, reference;
            public Steeper(IHeightField inner, double factor) { this.inner = inner; this.factor = factor; reference = inner.HeightAt(-92, -40); }
            public bool Covers(double x, double z) => inner.Covers(x, z);
            public double HeightAt(double x, double z) => reference + (inner.HeightAt(x, z) - reference) * factor;
        }

        static double[] Line(double from, double to, int n) => Enumerable.Range(0, n + 1).Select(i => from + (to - from) * i / n).ToArray();

        static double Slope(IHeightField f, double metres = 40.0) => Grade.MaxSustained(f, Line(0, metres, 40), new double[41]);

        // ---- the measure

        [Test]
        public void FlatGroundIsNoGradeAndASlopeIsReadAsItsPercent()
        {
            Assert.AreEqual(0.0, Slope(new Field((x, z) => 100.0)), 1e-9);
            Assert.AreEqual(10.0, Slope(new Field((x, z) => 100.0 + 0.10 * x)), 1e-6);
            Assert.AreEqual(10.0, Slope(new Field((x, z) => 100.0 - 0.10 * x)), 1e-6, "downhill is as steep as uphill");
        }

        [Test]
        public void TheLimitIsTwelvePercentSustained()
        {
            Assert.AreEqual(12.0, Grade.MaxPct);
            Assert.AreEqual(10.0, Grade.WindowMetres, "a slope is read over 10 m");
            Assert.AreEqual(4.0, Grade.StepMetres, "and a step over 4 m");
            Assert.LessOrEqual(Slope(new Field((x, z) => 0.119 * x)), Grade.MaxPct, "11.9 % is a road");
            Assert.Greater(Slope(new Field((x, z) => 0.125 * x)), Grade.MaxPct, "12.5 % is not");
        }

        [Test]
        public void AStepTooHighForATruckIsRefusedHoweverShortItIs()
        {
            // 0.9 m in one metre: 9 % over a 10 m window, 22.5 % over a step (10.8 % on the common scale): fine
            Assert.LessOrEqual(Slope(new Field((x, z) => x < 20.0 ? 0.0 : 0.9)), Grade.MaxPct, "a 0.9 m lip is climbable");
            // 1.1 m: 11 % over the window, 27.5 % over a step (13.2 %): a berm, over the limit by the step alone
            Assert.Greater(Slope(new Field((x, z) => x < 20.0 ? 0.0 : 1.1)), Grade.MaxPct, "a 1.1 m lip is a wall to a truck even though its window average is under 12 %");
        }

        [Test]
        public void ThePerLegGradeNamesTheLegsTheSteepStretchTouches()
        {
            var ground = new Field((x, z) => x < 30.0 ? 0.0 : 0.20 * (x - 30.0));
            var xs = new[] { 0.0, 20.0, 40.0, 60.0, 80.0 };
            var legs = Grade.PerLeg(ground, xs, new double[5]);
            Assert.AreEqual(4, legs.Length);
            Assert.AreEqual(0.0, legs[0], 1e-9, "flat leg");
            Assert.Greater(legs[2], Grade.MaxPct);
            Assert.Greater(legs[3], Grade.MaxPct);
        }

        [Test]
        public void AWindowThatRunsFromOneLegIntoTheNextNamesBoth()
        {
            // a 1.5 m lip half a metre into the second leg: every window over it starts on the first leg and ends on the second
            var ground = new Field((x, z) => x < 20.5 ? 0.0 : 1.5);
            var legs = Grade.PerLeg(ground, new[] { 0.0, 20.0, 40.0, 60.0 }, new double[4]);
            Assert.Greater(legs[0], Grade.MaxPct, "the leg the steep windows start on");
            Assert.Greater(legs[1], Grade.MaxPct, "the leg they end on, which holds the lip");
            Assert.AreEqual(0.0, legs[2], 1e-9, "a leg no window over the lip reaches");
        }

        [Test]
        public void APathShorterThanAWindowIsReadOverAWindowSoAShortJoinIsNotRefusedForItsLength()
        {
            // 1 m over 7 m: 14.3 % over its own length, but a slope is the rise over 10 m, and a truck that climbs 10 % climbs this
            Assert.AreEqual(10.0, Grade.MaxSustained(new Field((x, z) => x / 7.0), new[] { 0.0, 7.0 }, new[] { 0.0, 0.0 }), 1e-6);
            // 0.5 m over 3 m: 5 % over a window, and over a step 0.5 m in 4 m (12.5 %, 6 % on the common scale)
            Assert.AreEqual(6.0, Grade.MaxSustained(new Field((x, z) => x / 6.0), new[] { 0.0, 3.0 }, new[] { 0.0, 0.0 }), 1e-6);
            // the step still sees a lip however short the path is: 1.1 m in 3 m is 27.5 % over a step (13.2 %)
            Assert.AreEqual(13.2, Grade.MaxSustained(new Field((x, z) => x * 1.1 / 3.0), new[] { 0.0, 3.0 }, new[] { 0.0, 0.0 }), 1e-6);
            // and a path as long as a window is read as it was
            Assert.AreEqual(12.5, Grade.MaxSustained(new Field((x, z) => 0.125 * x), new[] { 0.0, 10.0 }, new[] { 0.0, 0.0 }), 1e-6);
        }

        [Test]
        public void APointOffTheSiteHasNoHeightAndNothingIsPlannedFromIt()
        {
            var ground = CommandKit.Ground;
            Assert.IsTrue(ground.Covers(511.0, -511.0), "the site's ground runs to its edges");
            Assert.IsFalse(ground.Covers(600.0, 0.0));
            Assert.IsFalse(ground.Covers(0.0, -513.0));
            Assert.Throws<ArgumentOutOfRangeException>(() => ground.HeightAt(600.0, 0.0), "its height is refused, never guessed from the edge");
            Assert.AreEqual(double.PositiveInfinity, Grade.MaxSustained(ground, new[] { 500.0, 600.0 }, new[] { 0.0, 0.0 }), "a path off the site is not one a truck is sent along");
            Assert.IsTrue(Grade.PerLeg(ground, new[] { 400.0, 500.0, 600.0 }, new[] { 0.0, 0.0, 0.0 }).All(double.IsPositiveInfinity));
            var queue = CommandKit.Site.Spots[RouteGraph.QueueSpot];
            Assert.IsFalse(CommandKit.Graph.OnSite(600.0, 0.0));
            Assert.IsTrue(CommandKit.Graph.OnSite(queue.X, queue.Z));
            Assert.IsNull(CommandKit.Graph.Plan(600.0, 0.0, queue.X, queue.Z));
            Assert.IsNull(CommandKit.Graph.Plan(queue.X, queue.Z, 600.0, 0.0));
        }

        [Test]
        public void ACommandToAMachineOffTheSiteIsRefusedAsOutsideTheSite()
        {
            foreach (var (key, area) in new[] { ("goto-area", "sp-zone-cut"), ("goto-refuel", (string)null) })
            {
                var r = new Rig("SP-HL-0003", EquipmentKind.Hauler, 600, 0, withTrack: false);
                var cmd = r.Send(key, area);
                var result = cmd.Completion.Answer();
                Assert.IsFalse(result.Succeeded, key);
                StringAssert.StartsWith("the machine is outside the site at (600, 0)", result.Reason, key);
                Assert.AreEqual(0, r.Body.Detaches, "it never left its place for a drive over ground nobody knows");
            }
        }

        // ---- the network

        [Test]
        public void EveryStretchOfTheNetworkIsWithinTheGradeLimitOnTheGround()
        {
            // the ground is the packed heightmap the scene builds its Terrain from (quarry_height.bytes, read as QuarryTerrain reads it)
            var n = 0;
            foreach (var e in CommandKit.Graph.Edges())
            {
                n++;
                Assert.LessOrEqual(e.SustainedGradePct, Grade.MaxPct, $"the stretch ({e.Ax:0},{e.Az:0}) to ({e.Bx:0},{e.Bz:0}) is recorded as within the limit");
                // a join across open ground is its own straight line; a stretch of road is read in the road it is part of (a road's
                // grade is the rise over 10 m of it, which a few metres of it alone do not show)
                if (!e.IsRoad)
                    Assert.LessOrEqual(Grade.MaxSustained(CommandKit.Ground, new[] { e.Ax, e.Bx }, new[] { e.Az, e.Bz }), Grade.MaxPct, $"the join ({e.Ax:0},{e.Az:0}) to ({e.Bx:0},{e.Bz:0}) on the ground");
            }

            Assert.Greater(n, 20, "a network was read");
            foreach (var road in CommandKit.Site.Roads)
            {
                var xs = road.Points.Select(p => p.X).ToList();
                var zs = road.Points.Select(p => p.Z).ToList();
                Assert.LessOrEqual(Grade.PerLeg(CommandKit.Ground, xs, zs).Max(), Grade.MaxPct, $"{road.Name} on the ground");
            }
        }

        [Test]
        public void ASpotNoRoadClimbsToIsNotOnTheNetwork()
        {
            // the plant's hopper and head stand on a terrace 35 to 63 % up from the nearest road: nothing drives there, so they have no node
            foreach (var plant in new[] { "plant-hopper", "plant-head" })
                Assert.AreEqual(-1, CommandKit.Graph.SpotNode(plant), plant);
            Assert.GreaterOrEqual(CommandKit.Graph.SpotNode("plant-feed"), 0, "the feed end is reached by an 11.5 % join");
            foreach (var spot in new[] { RouteGraph.QueueSpot, RouteGraph.BaySpot, SiteGeometry.ParkingSpot, "load-point", "dump-point" })
                Assert.GreaterOrEqual(CommandKit.Graph.SpotNode(spot), 0, spot);
        }

        [Test]
        public void ARoadStretchSteeperThanTheLimitIsNotInTheNetwork()
        {
            // the quarry's ramp climbs at 9.9 %; with the relief doubled it is 19.8 % and no truck can take it
            var steep = RouteGraph.Build(CommandKit.Site, new Steeper(CommandKit.Ground, 2.0));
            Assert.Greater(steep.Components(), CommandKit.Graph.Components(), "cutting the ramp cuts the pit off");
            foreach (var e in steep.Edges()) Assert.LessOrEqual(e.SustainedGradePct, Grade.MaxPct);
            var ramp = CommandKit.Site.Roads.First(r => r.Name == "pit-ramp");
            var bottom = ramp.Points[0];
            var top = ramp.Points[ramp.Points.Count - 1];
            Assert.IsNotNull(CommandKit.Graph.Plan(top.X, top.Z, bottom.X, bottom.Z), "on the real ground the ramp is driveable");
            Assert.IsNull(steep.Plan(top.X, top.Z, bottom.X, bottom.Z), "on steeper ground it is not");
        }

        [Test]
        public void ARouteClimbableStretchByStretchButNotAsAWholeIsNotPlanned()
        {
            // a 1 m approach at 11.9 % to a road whose first 9 m climb at 13.25 %: each is within the limit by its own reading (the road over its
            // first 10 m rises 11.9 %), and joined they climb 13.1 % over 10 m
            var road = new RoadLine("t", "haul-road", 12.0, new[] { new RoadPoint(0, 0, 0), new RoadPoint(20, 0, 0) });
            var site = new SiteGeometry(new[] { road }, new Dictionary<string, Spot>(), new List<Rect2>(), new List<Rect2>());
            var ground = new Field((x, z) => x < -1.0 ? 0.0 : x < 0.0 ? 0.119 * (x + 1.0) : x < 9.0 ? 0.119 + 0.1325 * x : 0.119 + 0.1325 * 9.0);
            Assert.LessOrEqual(Grade.MaxSustained(ground, new[] { -1.0, 0.0 }, new[] { 0.0, 0.0 }), Grade.MaxPct, "premise: the approach alone");
            Assert.LessOrEqual(Grade.MaxSustained(ground, new[] { 0.0, 20.0 }, new[] { 0.0, 0.0 }), Grade.MaxPct, "premise: the road alone");
            Assert.Greater(Grade.MaxSustained(ground, new[] { -1.0, 20.0 }, new[] { 0.0, 0.0 }), Grade.MaxPct, "premise: the two together");
            Assert.IsNull(RouteGraph.Build(site, ground).Plan(-1.0, 0.0, 20.0, 0.0), "so no route is planned");
            Assert.IsNotNull(RouteGraph.Build(site, new Field((x, z) => 0.0)).Plan(-1.0, 0.0, 20.0, 0.0), "where the same places are on flat ground there is one");
        }

        [Test]
        public void TheTakesRefuelRouteFromThePitUsesTheRampWithinTheLimit()
        {
            // SP-HL-0006 is at the bottom of the pit when the take sends it to refuel (221 s into the live choreography)
            var at = CommandKit.MachineAt("SP-HL-0006", 221.0);
            var queue = CommandKit.Site.Spots[RouteGraph.QueueSpot];
            var route = CommandKit.Graph.Plan(at.X, at.Z, queue.X, queue.Z);
            Assert.IsNotNull(route, $"a route from ({at.X:0},{at.Z:0}) to the refuel queue");
            Assert.LessOrEqual(Grade.MaxSustained(CommandKit.Ground, route.Xs, route.Zs), Grade.MaxPct);
            var ramp = CommandKit.Site.Roads.First(r => r.Name == "pit-ramp");
            var onRamp = Enumerable.Range(0, route.Xs.Count).Count(i => ramp.Points.Any(p => Math.Abs(p.X - route.Xs[i]) < 0.01 && Math.Abs(p.Z - route.Zs[i]) < 0.01));
            Assert.GreaterOrEqual(onRamp, 10, "it goes up the ramp, along its own lane of points");
            Assert.That(route.Length, Is.InRange(250.0, 400.0), "the long way round by the ramp, not straight across the pit wall");
        }

        [Test]
        public void EveryTrackPositionCanReachEveryDestinationOnTheNetworkWithinTheLimit()
        {
            var goals = new List<(string, double, double)>();
            foreach (var spot in new[] { RouteGraph.QueueSpot, RouteGraph.BaySpot, SiteGeometry.ParkingSpot })
            {
                var sp = CommandKit.Site.Spots[spot];
                goals.Add((spot, sp.X, sp.Z));
            }

            foreach (var z in CommandKit.Site.Zones) goals.Add((z.Name, z.CentreX, z.CentreZ));
            var queries = 0;
            for (var t = 0; t < CommandKit.TrackCount; t++)
            {
                var frames = CommandKit.Track(t);
                var every = Math.Max(1, frames.Count / (t == 0 ? 90 : 12));
                for (var i = 0; i < frames.Count; i += every)
                    foreach (var (name, gx, gz) in goals)
                    {
                        var route = CommandKit.Graph.Plan(frames[i].X, frames[i].Z, gx, gz);
                        Assert.IsNotNull(route, $"track {t} frame {i} ({frames[i].X:0},{frames[i].Z:0}) to {name}");
                        Assert.LessOrEqual(Grade.MaxSustained(CommandKit.Ground, route.Xs, route.Zs), Grade.MaxPct, $"track {t} frame {i} to {name}");
                        queries++;
                    }
            }

            Assert.Greater(queries, 500);
        }

        // ---- a command that cannot be driven

        [Test]
        public void ACommandToAPlaceOnlyASteeperWayReachesIsRefusedWithTheReason()
        {
            var steep = RouteGraph.Build(CommandKit.Site, new Steeper(CommandKit.Ground, 2.0));
            var r = new Rig("SP-HL-0003", EquipmentKind.Hauler, -92, -40, withTrack: false, graph: steep);
            var cmd = r.Send("goto-area", "sp-zone-cut");
            var result = cmd.Completion.Answer();
            Assert.IsFalse(result.Succeeded, "the pit cannot be reached by a grade a truck can climb");
            StringAssert.StartsWith("no drivable route to sp-zone-cut", result.Reason);
            StringAssert.Contains("12 % grade", result.Reason, "it names why");
            Assert.AreEqual(0, r.Body.Detaches, "the machine never left its place for a drive that cannot be made");
        }

        [Test]
        public void ACommandWhoseNearestSlotOnlyASteeperWayReachesParksInTheNextOne()
        {
            var road = new RoadLine("r", "haul-road", 12.0, new[] { new RoadPoint(0, 0, 0), new RoadPoint(200, 0, 0) });
            var zone = new Rect2("z-test", 100, 160, 20, 60);
            var site = new SiteGeometry(new[] { road }, new Dictionary<string, Spot>(), new List<Rect2> { zone }, new List<Rect2>());
            const double startX = 130, startZ = 0;
            var slots = ParkingLot.Slots(zone, startX, startZ, ParkingLot.Margin(EquipmentKind.Hauler), site.Obstacles, ParkingLot.FootprintRadius(EquipmentKind.Hauler));
            Assert.GreaterOrEqual(slots.Count, 2, "premise: a zone of several slots");
            var first = slots[0];
            // the slot nearest the truck stands on a 3 m mesa, a wall to a truck on every side
            var ground = new Field((x, z) => Math.Abs(x - first.X) < 4.0 && Math.Abs(z - first.Z) < 4.0 ? 3.0 : 0.0);
            var graph = RouteGraph.Build(site, ground);
            Assert.IsNull(graph.Plan(startX, startZ, first.X, first.Z), "premise: the nearest slot has no route");
            Assert.IsNotNull(graph.Plan(startX, startZ, slots[1].X, slots[1].Z), "premise: the next one has");

            var r = new Rig("SP-HL-0003", EquipmentKind.Hauler, startX, startZ, withTrack: false, graph: graph, site: site);
            var cmd = r.Send("goto-area", "z-test");
            Assert.IsFalse(cmd.IsComplete, "accepted: it is on its way, not refused because its first choice cannot be driven to");
            Assert.AreEqual(TaskPhase.ToDestination, r.Controller.Phase);
            Assert.AreEqual(slots[1].X, r.Controller.CurrentRoute.EndX, 1e-6, "to the next slot");
            Assert.AreEqual(slots[1].Z, r.Controller.CurrentRoute.EndZ, 1e-6);
        }

        [Test]
        public void TheSameCommandOnTheRealGroundIsAccepted()
        {
            var r = new Rig("SP-HL-0003", EquipmentKind.Hauler, -92, -40, withTrack: false);
            var cmd = r.Send("goto-area", "sp-zone-cut");
            Assert.IsFalse(cmd.IsComplete, "it is on its way");
            Assert.AreEqual(TaskPhase.ToDestination, r.Controller.Phase);
            Assert.LessOrEqual(Grade.MaxSustained(CommandKit.Ground, r.Controller.CurrentRoute.Xs, r.Controller.CurrentRoute.Zs), Grade.MaxPct);
        }
    }
}
