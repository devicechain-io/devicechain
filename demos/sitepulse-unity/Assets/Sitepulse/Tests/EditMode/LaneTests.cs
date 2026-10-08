// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text.RegularExpressions;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using NUnit.Framework;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    // Traffic keeps left: the lane each direction of a road has, the route a machine drives in it, and the haul loop that lies on it.
    public sealed class LaneTests
    {
        /// <summary>One road, as the route a machine drives along it, one way or the other, in its lane.</summary>
        static Route Along(RoadLine road, bool reverse)
        {
            var pts = reverse ? road.Points.Reverse().ToList() : road.Points.ToList();
            var b = new RouteBuilder();
            b.Start(pts[0].X, pts[0].Z);
            for (var i = 1; i < pts.Count; i++) b.Leg(pts[i].X, pts[i].Z, 1.0, 0.0, road.LaneOffset);
            return b.Build();
        }

        // ---- the rule

        [Test]
        public void ALaneIsAQuarterOfTheRoadsWidthNeverLessThanTwoTrucksNeedAndARoadTooNarrowForTwoHasNone()
        {
            Assert.AreEqual(3.4, RoadLine.MinLaneOffset);
            // two haul trucks 5.7 m wide pass with air between them only if their lane centres are further apart than that
            Assert.Greater(2 * RoadLine.MinLaneOffset, RoadLine.HaulTruckWidth);
            // the narrowest road with two lanes: at the least offset each truck's outer edge is still on the road
            Assert.AreEqual(12.5, RoadLine.TwoLaneWidth, 1e-9);
            foreach (var road in CommandKit.Site.Roads)
            {
                Assert.AreEqual(road.Width < 12.5, road.SingleLane, road.Name);
                Assert.AreEqual(road.SingleLane ? 0.0 : Math.Max(road.Width / 4.0, RoadLine.MinLaneOffset), road.LaneOffset, 1e-9, road.Name);
            }

            Assert.AreEqual(5.0, CommandKit.Site.Roads.First(r => r.Name == "pit-ramp").LaneOffset, 1e-9, "the 20 m ramp's lanes are at its quarter lines");
            Assert.AreEqual(3.5, CommandKit.Site.Roads.First(r => r.Name == "fill-road").LaneOffset, 1e-9, "the 14 m road's are at its quarter lines too");
            CollectionAssert.AreEquivalent(new[] { "plant-road", "fill-return", "yard-road" }, CommandKit.Site.Roads.Where(r => r.SingleLane).Select(r => r.Name).ToArray(),
                "the 10 m plant road and the two 12 m roads are too narrow for two trucks");
        }

        static RoadLine RoadOfWidth(double width) =>
            new RoadLine("test", "haul", width, new[] { new RoadPoint(0, 0, 0), new RoadPoint(50, 0, 0) });

        [Test]
        public void ALanesOuterEdgeNeverLeavesItsRoadAndTheNarrowestRoadWithTwoLanesIsExactlyWideEnough()
        {
            // the truck is 5.7 m across, so its outer edge is LaneOffset + 2.85 m from the centreline
            foreach (var road in CommandKit.Site.Roads)
                Assert.LessOrEqual(road.LaneOffset + (road.SingleLane ? 0.0 : RoadLine.HaulTruckWidth / 2.0), road.Width / 2.0 + 1e-9, $"{road.Name}: a truck in its lane stays on the road");
            // and the same in the routes: every stretch of every road the graph holds
            foreach (var e in CommandKit.Graph.Edges().Where(e => e.IsRoad))
            {
                var road = RoadOf(e);
                if (e.Lane > 0.0) Assert.AreEqual(road.LaneOffset, e.Lane, 1e-9, $"{road.Name}: a stretch has its road's lane or none");
                Assert.LessOrEqual(e.Lane + RoadLine.HaulTruckWidth / 2.0, road.Width / 2.0 + 1e-9, $"{road.Name} ({e.Ax:0},{e.Az:0}): a truck on this stretch stays on the road");
            }

            // the boundary: just under it a road has no lane; at it the lanes are 3.4 m out and the truck's edge is on the road's edge
            Assert.IsTrue(RoadOfWidth(12.49).SingleLane);
            Assert.AreEqual(0.0, RoadOfWidth(12.49).LaneOffset);
            Assert.IsFalse(RoadOfWidth(12.5).SingleLane);
            Assert.AreEqual(3.4, RoadOfWidth(12.5).LaneOffset, 1e-9);
            Assert.AreEqual(12.5 / 2.0, RoadOfWidth(12.5).LaneOffset + RoadLine.HaulTruckWidth / 2.0, 1e-9);
        }

        [Test]
        public void TheLanesOfARoadWithTwoNeverConflictWhateverTheTwoTrucksDo()
        {
            var worst = double.MaxValue;
            string where = null;
            var checkedRoads = 0;
            foreach (var road in CommandKit.Site.Roads.Where(r => !r.SingleLane))
            {
                checkedRoads++;
                var forward = Along(road, false);
                var back = Along(road, true);
                // two trucks, one in each lane, abreast of each other and a little before and behind
                for (var s = 0.0; s <= forward.Length; s += 1.0)
                    foreach (var shift in new[] { 0.0, -4.0, 4.0 })
                    {
                        var sb = forward.Length - s + shift;
                        if (sb < 0.0 || sb > forward.Length) continue;
                        forward.OffsetPointAt(s, -road.LaneOffset, out var ax, out var az, out var ah);
                        back.OffsetPointAt(sb, -road.LaneOffset, out var bx, out var bz, out var bh);
                        var gap = Footprint.Gap(Footprint.At(EquipmentKind.Hauler, ax, az, ah), Footprint.At(EquipmentKind.Hauler, bx, bz, bh));
                        if (gap < worst) { worst = gap; where = $"{road.Name} at {s:0} m (shift {shift:0})"; }
                    }
            }

            Assert.AreEqual(2, checkedRoads, "the ramp and the fill road have a lane each way");
            Assert.Greater(worst, 0.0, $"two haul trucks passing in the lanes of a road have air between them (the least: {worst:0.00} m, {where})");
        }

        [Test]
        public void ARoadTooNarrowForTwoMustBeOneTruckAtATimeAndTwoOnItsCentrelineWouldOverlapWhereTwoLanesWouldHangOffIt()
        {
            // what the single-lane rule is for (nothing yet makes a truck wait for the stretch: that is for traffic coordination): on each of these a truck in a lane of the least offset would be off the road,
            // and two trucks on the centreline, one each way, are on top of each other (so the stretch is one truck at a time)
            foreach (var road in CommandKit.Site.Roads.Where(r => r.SingleLane))
            {
                Assert.Less(road.Width / 2.0, RoadLine.MinLaneOffset + RoadLine.HaulTruckWidth / 2.0, $"{road.Name}: a lane of the least offset would put a truck's edge off the road");
                var forward = Along(road, false);
                var back = Along(road, true);
                Assert.AreEqual(0.0, road.LaneOffset, $"{road.Name}: a truck drives the centreline");
                var worst = double.MaxValue;
                for (var s = 0.0; s <= forward.Length; s += 1.0)
                {
                    forward.OffsetPointAt(s, 0.0, out var ax, out var az, out var ah);
                    back.OffsetPointAt(forward.Length - s, 0.0, out var bx, out var bz, out var bh);
                    worst = Math.Min(worst, Footprint.Gap(Footprint.At(EquipmentKind.Hauler, ax, az, ah), Footprint.At(EquipmentKind.Hauler, bx, bz, bh)));
                }

                Assert.Less(worst, 0.0, $"{road.Name}: two trucks meeting on a single lane overlap ({worst:0.00} m), so only one truck at a time MUST be on it, and nothing enforces that yet");
            }
        }

        static RoadLine RoadOf(RouteGraph.EdgeInfo e) => CommandKit.Site.Roads.First(r => Enumerable.Range(1, r.Points.Count - 1).Any(i =>
            (Near(r.Points[i - 1], e.Ax, e.Az) && Near(r.Points[i], e.Bx, e.Bz)) || (Near(r.Points[i - 1], e.Bx, e.Bz) && Near(r.Points[i], e.Ax, e.Az))));

        // ---- the routes

        [Test]
        public void ARoadStretchCarriesItsLaneOrNoneAndAJoinAcrossOpenGroundCarriesNone()
        {
            var seen = 0;
            var lanes = 0;
            var narrow = 0;
            var steepSingles = 0;
            var nearSingles = 0;
            var clearance = ParkingLot.TravelRadius(EquipmentKind.Hauler) + ParkingLot.TravelClearance;
            foreach (var e in CommandKit.Graph.Edges())
            {
                if (!e.IsRoad)
                {
                    Assert.AreEqual(0.0, e.Lane, $"the join ({e.Ax:0},{e.Az:0}) to ({e.Bx:0},{e.Bz:0}) crosses open ground");
                    Assert.IsFalse(e.SingleLane, "a join across open ground is not a single-lane road");
                    continue;
                }

                var road = RoadOf(e);
                var where = $"{road.Name}: ({e.Ax:0},{e.Az:0}) to ({e.Bx:0},{e.Bz:0})";
                seen++;
                if (road.SingleLane)
                {
                    narrow++;
                    Assert.AreEqual(0.0, e.Lane, where + ": too narrow for two trucks");
                    Assert.IsTrue(e.SingleLane, where);
                    continue;
                }

                // a road wide enough for two has its lane on a stretch unless a lane on it would be too steep, or a truck in the lane
                // would come within a lane and its clearance of something standing: then the stretch is one truck at a time too
                var steep = LaneIsSteepOn(road, e);
                var near = double.MaxValue;
                for (var k = 0; k <= 40; k++)
                    foreach (var o in CommandKit.Site.Obstacles)
                        near = Math.Min(near, o.Distance(e.Ax + (e.Bx - e.Ax) * k / 40, e.Az + (e.Bz - e.Az) * k / 40));
                var crowded = near < road.LaneOffset + clearance;
                if (steep) steepSingles++;
                if (crowded) nearSingles++;
                if (steep || crowded)
                {
                    Assert.AreEqual(0.0, e.Lane, where + (steep ? ": a lane on it is steeper than the limit" : $": something stands {near:0.0} m from it"));
                    Assert.IsTrue(e.SingleLane, where);
                }
                else
                {
                    Assert.AreEqual(road.LaneOffset, e.Lane, 1e-9, where);
                    lanes++;
                }
            }

            Assert.Greater(seen, 50, "the roads were read");
            Assert.Greater(lanes, 20, "most of the wide roads carry their lanes");
            Assert.Greater(narrow, 20, "the narrow roads were read");
            Assert.Greater(steepSingles, 0, "a stretch of the ramp has a lane too steep to drive");
            Assert.Greater(nearSingles, 0, "a stretch of the fill road runs close to a light tower");
        }

        // is a lane of this road, either way, steeper than the limit beside this stretch? Read as the ground under the lane's own line
        // (the inside of a curve is shorter than its centreline, so it climbs more steeply).
        static bool LaneIsSteepOn(RoadLine road, RouteGraph.EdgeInfo e)
        {
            for (var rev = 0; rev < 2; rev++)
            {
                var route = Along(road, rev == 1);
                var xs = new List<double>();
                var zs = new List<double>();
                var centres = new List<(double, double)>();
                for (var s = 0.0; ; s += 0.5)
                {
                    var d = Math.Min(s, route.Length);
                    route.OffsetPointAt(d, -road.LaneOffset, out var x, out var z, out _);
                    route.PointAt(d, out var cx, out var cz, out _, out _);
                    xs.Add(x);
                    zs.Add(z);
                    centres.Add((cx, cz));
                    if (d >= route.Length) break;
                }

                var pct = Grade.PerLeg(CommandKit.Ground, xs, zs);
                for (var k = 0; k < pct.Length; k++)
                {
                    if (pct[k] <= Grade.MaxPct) continue;
                    // the centre of that piece of lane lies on this stretch
                    var mx = (centres[k].Item1 + centres[k + 1].Item1) / 2.0;
                    var mz = (centres[k].Item2 + centres[k + 1].Item2) / 2.0;
                    double dx = e.Bx - e.Ax, dz = e.Bz - e.Az;
                    var len2 = dx * dx + dz * dz;
                    var t = Math.Max(0.0, Math.Min(1.0, ((mx - e.Ax) * dx + (mz - e.Az) * dz) / len2));
                    var px = e.Ax + dx * t - mx;
                    var pz = e.Az + dz * t - mz;
                    if (px * px + pz * pz < 0.25 * 0.25) return true;
                }
            }

            return false;
        }

        static bool Near(RoadPoint p, double x, double z) => Math.Abs(p.X - x) < RouteGraph.MergeTolerance && Math.Abs(p.Z - z) < RouteGraph.MergeTolerance;

        [Test]
        public void ARoutePlannedOverRoadsKnowsTheLaneOfEachLeg()
        {
            var yard = CommandKit.Site.Spots[SiteGeometry.ParkingSpot];
            var cut = CommandKit.Site.Zones.First(z => z.Name == "sp-zone-cut");
            var route = CommandKit.Graph.Plan(yard.X, yard.Z, cut.CentreX, cut.CentreZ);
            Assert.IsNotNull(route);
            var onRoad = 0;
            for (var leg = 0; leg < route.Legs; leg++)
            {
                var lane = route.LaneOffset(leg);
                Assert.IsTrue(lane == 0.0 || lane >= RoadLine.MinLaneOffset, $"leg {leg} is on no road, or in a lane of one");
                if (lane > 0.0) onRoad++;
            }

            Assert.Greater(onRoad, 5, "most of the way is by road");
            Assert.AreEqual(0.0, route.LaneAt(0.0), "it leaves the yard across open ground");
        }

        // ---- driving in a lane

        sealed class Run
        {
            public double MaxStep, MaxLateralPerMetre, MaxLateral, Seconds;
            public bool Arrived;
        }

        static Run Drive(Route route, double share = 1.0, double dt = 0.1)
        {
            var kin = Kinematics.For(EquipmentKind.Hauler);
            var body = new FakeBody("SP-HL-0003", EquipmentKind.Hauler, route.StartX, route.StartZ);
            body.Detach();
            var f = new RouteFollower { LaneShare = share };
            route.PointAt(0.0, out _, out _, out var h0, out _);
            f.Start(route, h0);
            var run = new Run();
            double px = body.X, pz = body.Z, lat = f.Lateral, along = f.Progress;
            for (var t = 0.0; t < 3000.0; t += dt)
            {
                var done = f.Advance(dt, body, kin, null);
                var step = Math.Sqrt((body.X - px) * (body.X - px) + (body.Z - pz) * (body.Z - pz));
                run.MaxStep = Math.Max(run.MaxStep, step);
                var l = f.Active ? f.Lateral : 0.0;
                var c = f.Active ? f.Progress : route.Length;
                // sideways against forward, along the route's own line (not the length of the lane, which round a corner is not the road's)
                if (f.Active && Math.Abs(c - along) > 1e-3) run.MaxLateralPerMetre = Math.Max(run.MaxLateralPerMetre, Math.Abs(l - lat) / Math.Abs(c - along));
                run.MaxLateral = Math.Max(run.MaxLateral, Math.Abs(l));
                lat = l;
                along = c;
                px = body.X;
                pz = body.Z;
                run.Seconds = t + dt;
                if (done) { run.Arrived = true; break; }
            }

            return run;
        }

        static IEnumerable<Route> SomeRoutes()
        {
            var goals = new List<(double, double)>();
            foreach (var spot in new[] { RouteGraph.QueueSpot, RouteGraph.BaySpot, SiteGeometry.ParkingSpot, "load-point", "dump-point" })
                goals.Add((CommandKit.Site.Spots[spot].X, CommandKit.Site.Spots[spot].Z));
            foreach (var z in CommandKit.Site.Zones) goals.Add((z.CentreX, z.CentreZ));
            var frames = CommandKit.Track(0);
            for (var i = 0; i < frames.Count; i += 130)
                foreach (var (gx, gz) in goals)
                {
                    var r = CommandKit.Graph.Plan(frames[i].X, frames[i].Z, gx, gz);
                    if (r != null && r.Length > 5.0) yield return r;
                }
        }

        [Test]
        public void AMachineMovesIntoAndOutOfALaneAndNeverStepsSideways()
        {
            var routes = 0;
            double worstStep = 0, worstSlew = 0;
            foreach (var route in SomeRoutes())
            {
                var run = Drive(route);
                Assert.IsTrue(run.Arrived, $"a route of {route.Length:0} m is driven to its end");
                worstStep = Math.Max(worstStep, run.MaxStep);
                worstSlew = Math.Max(worstSlew, run.MaxLateralPerMetre);
                routes++;
            }

            Assert.Greater(routes, 40);
            // it moves sideways no faster than the lane slope, wherever a road meets open ground
            Assert.LessOrEqual(worstSlew, 0.25 + 0.03, "metres sideways per metre forward, the worst of any step");
            // and it covers no more ground in a step than its speed gives, round every corner of every route
            Assert.LessOrEqual(worstStep, Kinematics.For(EquipmentKind.Hauler).Cruise * 0.1 + 0.05, "the longest step any route has");
        }

        [Test]
        public void TheSlopeALaneIsEasedInAtIsAQuarterOfAMetreSidewaysForEveryMetreForward()
        {
            Assert.AreEqual(0.25, LaneLine.SlewMetresPerMetre);
            // a route of a road and nothing else: it eases in over 20 m (5 m at a quarter) and the middle is in the lane
            var b = new RouteBuilder();
            b.Start(0, 0);
            b.Leg(100, 0, 1.0, 0.0, 5.0);
            var line = LaneLine.Build(b.Build());
            Assert.AreEqual(0.0, line.LateralAt(0.0), 1e-9);
            Assert.AreEqual(-2.5, line.LateralAt(LineAtCentre(line, 10.0)), 0.02, "10 m in, 2.5 m to the left");
            Assert.AreEqual(-5.0, line.LateralAt(LineAtCentre(line, 20.0)), 0.02, "20 m in, in its lane");
            Assert.AreEqual(-5.0, line.LateralAt(LineAtCentre(line, 50.0)), 1e-9);
            Assert.AreEqual(-2.5, line.LateralAt(LineAtCentre(line, 90.0)), 0.02, "and out again as it came in");
            Assert.AreEqual(0.0, line.LateralAt(line.Length), 1e-9);
        }

        [Test]
        public void ARouteThatBeginsAndEndsOnARoadEasesIntoItsLaneFromTheRoadsOwnLine()
        {
            foreach (var road in CommandKit.Site.Roads.Where(r => r.Points.Count > 3 && Along(r, false).Length > 40.0))
            {
                var route = Along(road, false);
                var line = LaneLine.Build(route);
                Assert.AreEqual(0.0, line.LateralAt(0.0), 1e-9, $"{road.Name}: it starts where it is");
                Assert.AreEqual(0.0, line.LateralAt(line.Length), 1e-9, $"{road.Name}: and arrives where it was sent");
                Assert.AreEqual(-road.LaneOffset, line.LateralAt(line.Length / 2.0), 1e-6, $"{road.Name}: and between the two it is in its lane");
                for (var d = 0.0; d < line.Length; d += 0.5)
                {
                    var along = Math.Abs(line.CentrelineAt(d + 0.5) - line.CentrelineAt(d));
                    if (along > 1e-3) Assert.LessOrEqual(Math.Abs(line.LateralAt(d + 0.5) - line.LateralAt(d)) / along, 0.25 + 1e-6, $"{road.Name} at {d:0.0} m: no sideways step");
                }
            }
        }

        [Test]
        public void AMachineKeepsToTheLeftOfARoadAndArrivesOnItsLine()
        {
            var yard = CommandKit.Site.Spots[SiteGeometry.ParkingSpot];
            var cut = CommandKit.Site.Zones.First(z => z.Name == "sp-zone-cut");
            var route = CommandKit.Graph.Plan(yard.X, yard.Z, cut.CentreX, cut.CentreZ);
            var run = Drive(route);
            Assert.IsTrue(run.Arrived);
            Assert.Greater(run.MaxLateral, 3.0, "it ran in a lane, 3.4 m or more off the line of the road");
            Assert.LessOrEqual(run.MaxLateral, 5.0 + 1e-6, "and no further than the widest lane");
            var line = LaneLine.Build(route);
            Assert.AreEqual(0.0, line.LateralAt(0.0), 1e-9, "it starts where it is");
            Assert.AreEqual(0.0, line.LateralAt(line.Length), 1e-9, "and arrives where it was sent");
            for (var d = 0.0; d <= line.Length; d += 1.0) Assert.LessOrEqual(line.LateralAt(d), 1e-9, "never to the right of the line");
        }

        [Test]
        public void ALaneShareIsTheMachinesOwnAndScalesHowFarFromTheRoadsLineItRuns()
        {
            var yard = CommandKit.Site.Spots[SiteGeometry.ParkingSpot];
            var cut = CommandKit.Site.Zones.First(z => z.Name == "sp-zone-cut");
            var route = CommandKit.Graph.Plan(yard.X, yard.Z, cut.CentreX, cut.CentreZ);
            var full = Drive(route, 1.0).MaxLateral;
            var half = Drive(route, 0.5).MaxLateral;
            var none = Drive(route, 0.0).MaxLateral;
            Assert.AreEqual(0.0, none, 1e-9, "a share of 0 drives the road's own line");
            Assert.AreEqual(full * 0.5, half, 0.2, "a share of a half runs half as far from it");
            Assert.AreEqual(1.0, new RouteFollower().LaneShare, "by default a machine takes its whole lane");
        }

        [Test]
        public void ALaneShareSetOnAMachineIsTheOneItsCommandsAreDrivenWith()
        {
            double Deviation(double share)
            {
                var r = new Rig("SP-HL-0003", EquipmentKind.Hauler, -92, -40, withTrack: false);
                r.Controller.LaneShare = share;
                var cmd = r.Send("goto-area", "sp-zone-cut");
                var route = r.Controller.CurrentRoute;
                Assert.IsNotNull(route, "the command is under way");
                var worst = 0.0;
                Assert.IsTrue(r.Run(() =>
                {
                    worst = Math.Max(worst, DistanceToLine(route, r.Body.X, r.Body.Z));
                    return cmd.IsComplete;
                }), "it arrives");
                Assert.IsTrue(cmd.Completion.Answer().Succeeded);
                return worst;
            }

            Assert.AreEqual(1.0, new Rig().Controller.LaneShare, "a machine takes its whole lane unless it is told otherwise");
            Assert.Less(Deviation(0.0), 0.05, "a share of 0 stays on the road's own line");
            Assert.Greater(Deviation(1.0), 3.0, "a whole lane runs 3.4 m or more to the side of it");
        }

        static double DistanceToLine(Route route, double x, double z)
        {
            var best = double.MaxValue;
            for (var i = 0; i < route.Legs; i++)
            {
                double ax = route.Xs[i], az = route.Zs[i], dx = route.Xs[i + 1] - ax, dz = route.Zs[i + 1] - az;
                var len2 = dx * dx + dz * dz;
                var t = len2 < 1e-12 ? 0.0 : Math.Max(0.0, Math.Min(1.0, ((x - ax) * dx + (z - az) * dz) / len2));
                var px = ax + dx * t - x;
                var pz = az + dz * t - z;
                best = Math.Min(best, Math.Sqrt(px * px + pz * pz));
            }

            return best;
        }

        // ---- the shape of the line: corners, the ends of a road, the leg it is on

        /// <summary>The distance along a line at which it is <paramref name="centre"/> metres along the route's own line.</summary>
        static double LineAtCentre(LaneLine line, double centre)
        {
            double lo = 0.0, hi = line.Length;
            for (var i = 0; i < 60; i++)
            {
                var mid = (lo + hi) / 2.0;
                if (line.CentrelineAt(mid) < centre) lo = mid; else hi = mid;
            }

            return (lo + hi) / 2.0;
        }

        // two legs of 40 m with a corner between them, both on a road with a lane of 5 m
        static Route CornerRoute(double turnDegrees)
        {
            var b = new RouteBuilder();
            b.Start(0, 0);
            b.Leg(40, 0, 1.0, 0.0, 5.0);
            var r = turnDegrees * Math.PI / 180.0;
            b.Leg(40 + 40 * Math.Cos(r), -40 * Math.Sin(r), 1.0, 0.0, 5.0);
            return b.Build();
        }

        [Test]
        public void AMachineRoundACornerMovesSidewaysNoFasterThanTheCornerTurnsItsLane()
        {
            // the lane line's offset from the route's line is 5 m and turns through half the corner's angle over the 3 m either side of
            // it: at 90 degrees that is 5 m x 45 degrees (0.785) / 3 m = 1.31 m sideways for each metre forward (a corner is taken
            // slowly, so this is a slow move across, not a sidestep). A corner blended over less would step the machine across.
            foreach (var turn in new[] { 90.0, -90.0, 60.0 })
            {
                var route = CornerRoute(turn);
                var line = LaneLine.Build(route);
                var worst = 0.0;
                double lastX = 0, lastZ = 0;
                var first = true;
                for (var c = 0.0; c <= route.Length; c += 0.05)
                {
                    // the offset of the lane line from the route's line at the same place along the route
                    var d = LineAtCentre(line, c);
                    line.PointAt(d, out var lx, out var lz, out _, out _);
                    route.PointAt(c, out var rx, out var rz, out _, out _);
                    double ox = lx - rx, oz = lz - rz;
                    if (!first) worst = Math.Max(worst, Math.Sqrt((ox - lastX) * (ox - lastX) + (oz - lastZ) * (oz - lastZ)) / 0.05);
                    lastX = ox;
                    lastZ = oz;
                    first = false;
                }

                var expected = 5.0 * (Math.Abs(turn) / 2.0 * Math.PI / 180.0) / 3.0;
                Assert.LessOrEqual(worst, expected + 0.25 + 0.1, $"a {turn:0} degree corner: sideways metres per metre forward");
                Assert.GreaterOrEqual(worst, expected * 0.8, $"a {turn:0} degree corner: and the machine does move out and round it");
            }
        }

        [Test]
        public void ALaneEasesToNoOffsetWhereARoadMeetsOpenGroundAndNeverSteps()
        {
            // open ground (30 m), a road with a lane (60 m), open ground again (30 m)
            var b = new RouteBuilder();
            b.Start(0, 0);
            b.Leg(30, 0, SpeedModel.OffRoadFactor, 0.0);
            b.Leg(90, 0, 1.0, 0.0, 5.0);
            b.Leg(120, 0, SpeedModel.OffRoadFactor, 0.0);
            var line = LaneLine.Build(b.Build());
            Assert.AreEqual(0.0, line.LateralAt(LineAtCentre(line, 30.0)), 0.03, "where the road begins");
            Assert.AreEqual(0.0, line.LateralAt(LineAtCentre(line, 90.0)), 0.03, "and where it ends it is on the join's own line");
            Assert.AreEqual(-0.25, line.LateralAt(LineAtCentre(line, 31.0)), 0.03, "a metre into the road, a quarter of a metre over");
            Assert.AreEqual(-0.25, line.LateralAt(LineAtCentre(line, 89.0)), 0.03, "a metre before the end of it, the same again: it leaves the lane before the road ends");
            Assert.AreEqual(-5.0, line.LateralAt(LineAtCentre(line, 60.0)), 1e-6, "and the middle of the road is the lane");
            Assert.AreEqual(0.0, line.LateralAt(LineAtCentre(line, 29.0)), 0.03, "on the open ground before the road it is on the line");
            Assert.AreEqual(0.0, line.LateralAt(LineAtCentre(line, 91.0)), 0.03, "and after it");
        }

        [Test]
        public void OnEveryPlannedRouteTheLaneBeginsAndEndsOnTheLineWhereTheRoadDoes()
        {
            var boundaries = 0;
            foreach (var route in SomeRoutes())
            {
                var line = LaneLine.Build(route);
                for (var s = 0.05; s < route.Length; s += 0.05)
                {
                    var before = route.LaneAt(s - 0.05) > 0.0;
                    var here = route.LaneAt(s) > 0.0;
                    if (before == here) continue;
                    boundaries++;
                    var d = LineAtCentre(line, s);
                    Assert.AreEqual(0.0, line.LateralAt(d), 0.06, $"at {s:0.0} m of a {route.Length:0} m route the road {(here ? "begins" : "ends")}");
                    // and a metre on the road's side it has gone no further than the slope allows
                    var inside = LineAtCentre(line, here ? s + 1.0 : s - 1.0);
                    Assert.LessOrEqual(Math.Abs(line.LateralAt(inside)), 0.25 + 0.06, $"a metre {(here ? "into" : "before the end of")} the road at {s:0.0} m");
                }
            }

            Assert.Greater(boundaries, 40, "routes were read that join and leave roads");
        }

        [Test]
        public void TheLegAPointOfALaneLineIsReportedOnIsTheLegOfTheRoutesLineAtTheSamePlace()
        {
            // a zigzag of 40 m legs with a lane of 5 m: round each corner the lane is longer or shorter than the route by metres
            var b = new RouteBuilder();
            b.Start(0, 0);
            b.Leg(40, 0, 1.0, 0.0, 5.0);
            b.Leg(40, 40, 1.0, 0.0, 5.0);
            b.Leg(80, 40, 1.0, 0.0, 5.0);
            b.Leg(80, 80, 1.0, 0.0, 5.0);
            var zigzag = b.Build();
            var checkedPoints = 0;
            var mislaid = 0;
            foreach (var route in SomeRoutes().Concat(new[] { zigzag }))
            {
                var line = LaneLine.Build(route);
                for (var d = 0.0; d <= line.Length; d += 0.25)
                {
                    line.PointAt(d, out _, out _, out _, out var leg);
                    var c = line.CentrelineAt(d);
                    route.PointAt(c, out _, out _, out _, out var expected);
                    // the line is sampled every 0.1 m, so beside a corner the leg may be the one a sample either side puts it on
                    route.PointAt(Math.Max(0.0, c - 0.15), out _, out _, out _, out var earliest);
                    route.PointAt(Math.Min(route.Length, c + 0.15), out _, out _, out _, out var latest);
                    Assert.IsTrue(leg >= earliest && leg <= latest, $"{d:0.00} m along a {line.Length:0.0} m lane line of a {route.Length:0.0} m route: on leg {leg}, the route's line is on leg {expected} there");
                    checkedPoints++;
                    // had the leg been looked up at the lane line's own distance it would be a different one here
                    route.PointAt(Math.Min(d, route.Length), out _, out _, out _, out var byLineDistance);
                    if (byLineDistance != expected) mislaid++;
                }
            }

            Assert.Greater(checkedPoints, 5000);
            Assert.Greater(mislaid, 20, "the lane lines are measured by their own length, which puts them on another leg than the route's distance would");
        }

        // ---- the line a machine drives is the one that was checked

        static IEnumerable<(Route route, bool aware, string what)> PlannedRoutes(int trackEvery, int otherEvery)
        {
            var goals = new List<(string, double, double)>();
            foreach (var spot in new[] { RouteGraph.QueueSpot, RouteGraph.BaySpot, SiteGeometry.ParkingSpot, "load-point", "dump-point" })
                goals.Add((spot, CommandKit.Site.Spots[spot].X, CommandKit.Site.Spots[spot].Z));
            foreach (var z in CommandKit.Site.Zones) goals.Add((z.Name, z.CentreX, z.CentreZ));
            var travel = ParkingLot.TravelRadius(EquipmentKind.Hauler);
            for (var t = 0; t < CommandKit.TrackCount; t++)
            {
                var frames = CommandKit.Track(t);
                var every = Math.Max(1, t == 0 ? frames.Count / trackEvery : frames.Count / otherEvery);
                for (var i = 0; i < frames.Count; i += every)
                    foreach (var (name, gx, gz) in goals)
                    {
                        var what = $"track {t} frame {i} ({frames[i].X:0},{frames[i].Z:0}) to {name}";
                        var plain = CommandKit.Graph.Plan(frames[i].X, frames[i].Z, gx, gz);
                        if (plain != null) yield return (plain, false, what);
                        var aware = CommandKit.Graph.Plan(frames[i].X, frames[i].Z, gx, gz, CommandKit.Site.Obstacles, travel);
                        if (aware != null) yield return (aware, true, what + " (round obstacles)");
                    }
            }
        }

        [Test]
        public void ARouteWhoseLaneLineClimbsMoreThanTheLimitWhereTwoRoadsMeetIsNotPlannedHoweverGentleTheRoadsAre()
        {
            // two 20 m roads meeting at a corner, each with a lane of 5 m. Round the corner the lane line cuts inside the lane of either road
            // (it is nearest the corner, 3.5 m from each road's line, at (46.5, 3.5)), where neither road's own lane goes: a bump on that
            // place is on neither road's lanes, nor on their centrelines, and is climbed by the line a machine drives round the corner.
            var a = new RoadLine("a", "haul-road", 20.0, new[] { new RoadPoint(0, 0, 0), new RoadPoint(50, 0, 0) });
            var b = new RoadLine("b", "haul-road", 20.0, new[] { new RoadPoint(50, 0, 0), new RoadPoint(50, 0, 50) });
            var site = new SiteGeometry(new[] { a, b }, new Dictionary<string, Spot>(), new List<Rect2>(), new List<Rect2>());
            var flat = new GradeRouteTests.Field((x, z) => 0.0);
            var bump = new GradeRouteTests.Field((x, z) => 2.0 * Math.Exp(-((x - 46.5) * (x - 46.5) + (z - 3.5) * (z - 3.5)) / (2.0 * 0.6 * 0.6)));

            // the two roads taken one after the other, in their lanes
            var rb = new RouteBuilder();
            rb.Start(0, 0);
            rb.Leg(50, 0, 1.0, 0.0, 5.0);
            rb.Leg(50, 50, 1.0, 0.0, 5.0);
            var around = rb.Build();

            LaneLine.Build(around).Polyline(1.0, out var lxs, out var lzs);
            Assert.Greater(Grade.MaxSustained(bump, lxs, lzs), Grade.MaxPct, "premise: the line driven round the corner climbs more than the limit");
            Assert.LessOrEqual(Grade.MaxSustained(bump, around.Xs, around.Zs), Grade.MaxPct, "premise: the route's own line does not");
            foreach (var road in new[] { a, b })
            {
                var lane = Along(road, false);
                var px = new List<double>();
                var pz = new List<double>();
                for (var s = 0.0; s <= lane.Length; s += 1.0) { lane.OffsetPointAt(s, -5.0, out var x, out var z, out _); px.Add(x); pz.Add(z); }
                Assert.LessOrEqual(Grade.MaxSustained(bump, px, pz), Grade.MaxPct, $"premise: nor does the lane of {road.Name} on its own");
            }

            var graph = RouteGraph.Build(site, bump);
            Assert.AreEqual(2, graph.Edges().Count(e => e.IsRoad), "premise: the two roads");
            Assert.IsTrue(graph.Edges().Where(e => e.IsRoad).All(e => e.Lane == 5.0), "premise: the roads keep their lanes (nothing on either one is steep)");
            var onFlat = RouteGraph.Build(site, flat).Plan(0, 0, 50, 50);
            Assert.IsNotNull(onFlat, "premise: on flat ground the way is round the corner, by road");
            Assert.IsTrue(Enumerable.Range(0, onFlat.Legs).Any(i => onFlat.LaneOffset(i) > 0.0), "premise: in a lane");

            var route = graph.Plan(0, 0, 50, 50);
            if (route == null) return;   // no way is better than the steep one
            LaneLine.Build(route).Polyline(1.0, out var xs, out var zs);
            Assert.LessOrEqual(Grade.MaxSustained(bump, xs, zs), Grade.MaxPct, "whatever route is planned, the line driven along it is within the limit");
        }

        [Test]
        public void AStretchOfARoadKeepsItsLanesUnlessSomethingStandsWithinALaneAndATrucksClearanceOfItsCentreline()
        {
            // a 20 m road with lanes of 5 m: a truck in a lane sweeps 3 m and keeps 0.2 m clear, so something standing within 8.2 m of
            // the centreline puts it too near
            double LaneOnRoadWith(double standsAt)
            {
                var road = new RoadLine("a", "haul-road", 20.0, new[] { new RoadPoint(0, 0, 0), new RoadPoint(60, 0, 0) });
                var tower = Obstacle.Capsule("tower", 30, standsAt, 30, standsAt, 0.0);
                var site = new SiteGeometry(new[] { road }, new Dictionary<string, Spot>(), new List<Rect2>(), new List<Rect2>(), new[] { tower });
                var edges = RouteGraph.Build(site, new GradeRouteTests.Field((x, z) => 0.0)).Edges().Where(e => e.IsRoad).ToList();
                Assert.AreEqual(1, edges.Count);
                return edges[0].Lane;
            }

            Assert.AreEqual(0.0, LaneOnRoadWith(8.1), "8.1 m from the centreline is within the lane, a truck's reach and its clearance");
            Assert.AreEqual(5.0, LaneOnRoadWith(8.3), "8.3 m is clear of them");
            Assert.AreEqual(5.0, LaneOnRoadWith(-8.3), "on the other side too");
            Assert.AreEqual(0.0, LaneOnRoadWith(-8.1), "and within them");
        }

        [Test]
        public void AShortObstacleBetweenTwoSamplesOfARoadStillTakesTheLanesOffTheStretch()
        {
            // the centreline is read every half metre, so something a metre across standing between the samples a coarser reading would take
            // (here a point at 25 m, between 20 and 30) is still found
            var road = new RoadLine("a", "haul-road", 20.0, new[] { new RoadPoint(0, 0, 0), new RoadPoint(60, 0, 0) });
            var tower = Obstacle.Capsule("tower", 25, 8.1, 25, 8.1, 0.0);
            var site = new SiteGeometry(new[] { road }, new Dictionary<string, Spot>(), new List<Rect2>(), new List<Rect2>(), new[] { tower });
            var edges = RouteGraph.Build(site, new GradeRouteTests.Field((x, z) => 0.0)).Edges().Where(e => e.IsRoad).ToList();
            Assert.AreEqual(1, edges.Count);
            Assert.AreEqual(0.0, edges[0].Lane, "something 8.1 m from the line at 25 m is within a lane and a truck's clearance");
        }

        [Test]
        public void ARouteWhoseLaneLineComesNearerSomethingThanTheClearanceBecauseTheGraphMovedARoadsEndIsNotPlanned()
        {
            // road B's surveyed line is 8.3 m from the tower (outside the 8.2 m reach, so B keeps its lanes), but its start is 0.95 m from
            // road A's end and the graph moves it onto A's node: the line a truck drives is then about 0.9 m nearer the tower
            var a = new RoadLine("a", "haul-road", 10.0, new[] { new RoadPoint(-50, 0, 0.95), new RoadPoint(0, 0, 0.95) });
            var b = new RoadLine("b", "haul-road", 20.0, new[] { new RoadPoint(0, 0, 0), new RoadPoint(100, 0, 0) });
            var tower = Obstacle.Capsule("tower", 25, 8.3, 25, 8.3, 0.0);
            var site = new SiteGeometry(new[] { a, b }, new Dictionary<string, Spot>(), new List<Rect2>(), new List<Rect2>(), new[] { tower });
            var graph = RouteGraph.Build(site, new GradeRouteTests.Field((x, z) => 0.0));
            Assert.IsTrue(graph.Edges().Any(e => e.IsRoad && e.Lane == 5.0), "premise: road B keeps its lanes, the tower being outside its reach");

            var travel = ParkingLot.TravelRadius(EquipmentKind.Hauler);
            var clearance = travel + ParkingLot.TravelClearance;
            var route = graph.Plan(-50, 0.95, 100, 0, new[] { tower }, travel);
            if (route == null) return;   // no way is better than a collision
            var line = LaneLine.Build(route);
            var nearest = double.MaxValue;
            for (var d = 0.0; d <= line.Length; d += 0.1)
            {
                line.PointAt(d, out var x, out var z, out _, out _);
                nearest = Math.Min(nearest, tower.Distance(x, z));
            }

            Assert.GreaterOrEqual(nearest, clearance - 0.05, $"the line driven passes {nearest:0.000} m from the tower, and a truck needs {clearance:0.0} m");
        }

        // the lane line a machine drives round the corner of two roads, with a bump of this height on its inside (see the test below)
        static double CornerLaneGradeWithBump(double height)
        {
            var around = CornerRoadsRoute();
            LaneLine.Build(around).Polyline(1.0, out var xs, out var zs);
            return Grade.MaxSustained(CornerBump(height), xs, zs);
        }

        static GradeRouteTests.Field CornerBump(double height) =>
            new GradeRouteTests.Field((x, z) => height * Math.Exp(-((x - 46.5) * (x - 46.5) + (z - 3.5) * (z - 3.5)) / (2.0 * 0.6 * 0.6)));

        static Route CornerRoadsRoute()
        {
            var rb = new RouteBuilder();
            rb.Start(0, 0);
            rb.Leg(50, 0, 1.0, 0.0, 5.0);
            rb.Leg(50, 50, 1.0, 0.0, 5.0);
            return rb.Build();
        }

        [Test]
        public void ALaneLineJustOverTheGradeLimitIsRefusedAndOneJustUnderItIsAccepted()
        {
            var a = new RoadLine("a", "haul-road", 20.0, new[] { new RoadPoint(0, 0, 0), new RoadPoint(50, 0, 0) });
            var b = new RoadLine("b", "haul-road", 20.0, new[] { new RoadPoint(50, 0, 0), new RoadPoint(50, 0, 50) });
            var site = new SiteGeometry(new[] { a, b }, new Dictionary<string, Spot>(), new List<Rect2>(), new List<Rect2>());

            // the height of bump at which the line driven round the corner reads a given grade (it rises with the height)
            double HeightFor(double pct)
            {
                double lo = 0.0, hi = 4.0;
                for (var i = 0; i < 40; i++)
                {
                    var mid = (lo + hi) / 2.0;
                    if (CornerLaneGradeWithBump(mid) < pct) lo = mid; else hi = mid;
                }

                return (lo + hi) / 2.0;
            }

            var over = CornerBump(HeightFor(Grade.MaxPct + 0.3));
            var under = CornerBump(HeightFor(Grade.MaxPct - 0.3));
            LaneLine.Build(CornerRoadsRoute()).Polyline(1.0, out var xs, out var zs);
            Assert.AreEqual(Grade.MaxPct + 0.3, Grade.MaxSustained(over, xs, zs), 0.1, "premise: the lane line reads just over the limit");
            Assert.AreEqual(Grade.MaxPct - 0.3, Grade.MaxSustained(under, xs, zs), 0.1, "premise: and just under it");

            var refused = RouteGraph.Build(site, over).Plan(0, 0, 50, 50);
            if (refused != null) Assert.AreEqual(0.0, Enumerable.Range(0, refused.Legs).Max(i => refused.LaneOffset(i)), "just over the limit: no route in the lane round the corner");
            var accepted = RouteGraph.Build(site, under).Plan(0, 0, 50, 50);
            Assert.IsNotNull(accepted, "just under it: a route is planned");
            Assert.IsTrue(Enumerable.Range(0, accepted.Legs).Any(i => accepted.LaneOffset(i) > 0.0), "and it is in the lane");
        }

        [Test, Timeout(300000)]
        public void EveryLineAMachineDrivesAPlannedRouteIsWithinTheGradeLimitAndClearOfWhatStands()
        {
            var clearance = ParkingLot.TravelRadius(EquipmentKind.Hauler) + ParkingLot.TravelClearance;
            const double SamplingTolerance = 0.05;   // how much nearer a sampled lane line may read than the route's line: the sampling, not a step toward it
            int lines = 0, inLane = 0, steep = 0, intrusions = 0;
            double worstGrade = 0.0, worstIntrusion = 0.0;
            string steepAt = null, intrudeAt = null;
            foreach (var (route, aware, what) in PlannedRoutes(90, 12))
            {
                var line = LaneLine.Build(route);
                lines++;
                line.Polyline(1.0, out var xs, out var zs);
                var grade = Grade.MaxSustained(CommandKit.Ground, xs, zs);
                if (grade > Grade.MaxPct)
                {
                    steep++;
                    if (grade > worstGrade) { worstGrade = grade; steepAt = what; }
                }

                var lane = false;
                for (var d = 0.0; d <= line.Length; d += 0.5)
                {
                    var lat = line.LateralAt(d);
                    if (lat < -3.0) lane = true;
                    if (!aware || Math.Abs(lat) < 1e-9) continue;
                    line.PointAt(d, out var x, out var z, out _, out _);
                    route.PointAt(line.CentrelineAt(d), out var cx, out var cz, out _, out _);
                    // no nearer anything than the route's own line is, or than the clearance
                    foreach (var o in CommandKit.Site.Obstacles)
                    {
                        var short_ = Math.Min(clearance, o.Distance(cx, cz)) - o.Distance(x, z) - SamplingTolerance;
                        if (short_ <= 0.0) continue;
                        intrusions++;
                        if (short_ > worstIntrusion) { worstIntrusion = short_; intrudeAt = $"{what}: {o.Name} at ({x:0.0},{z:0.0})"; }
                        break;
                    }
                }

                if (lane) inLane++;
            }

            Assert.Greater(lines, 400, "routes were planned");
            Assert.Greater(inLane, lines / 3, "many of them drive in a lane, so the check is of lane lines and not of the routes' own");
            Assert.AreEqual(0, steep, $"lane lines steeper than {Grade.MaxPct}% (worst {worstGrade:0.0}%: {steepAt})");
            Assert.AreEqual(0, intrusions, $"samples of a lane line nearer something standing than the route's line and the clearance (worst {worstIntrusion:0.00} m short: {intrudeAt})");
        }

        // ---- the haul loop

        [Test]
        public void TheHaulLoopLiesOnTheLaneItsRoadsWidthGivesIt()
        {
            var frames = CommandKit.Track(0);
            var onRoad = 0;
            var inLane = 0;
            var offBy = new List<double>();
            var frameCount = new Dictionary<string, int>();
            for (var i = 0; i < frames.Count - 1; i++)
            {
                var p = frames[i];
                var q = frames[i + 1];
                double mx = q.X - p.X, mz = q.Z - p.Z;
                var moved = Math.Sqrt(mx * mx + mz * mz);
                if (moved < 0.5) continue;   // standing, or reversing at a stop: not driving along a lane
                mx /= moved;
                mz /= moved;
                foreach (var road in CommandKit.Site.Roads)
                {
                    if (!Beside(road, p.X, p.Z, mx, mz, out var left)) continue;
                    onRoad++;
                    frameCount[road.Name] = frameCount.TryGetValue(road.Name, out var c) ? c + 1 : 1;
                    var off = Math.Abs(left - road.LaneOffset);
                    offBy.Add(off);
                    if (off <= 1.0) inLane++;
                    break;
                }
            }

            // this reads each road's lane by its WIDTH only, as the generator lays it: the loop is on its left lane on the ramp and the 14 m road and on
            // the centreline of the narrow ones. It does not apply the per-stretch rule (a steep or obstacle-adjacent stretch of a wide road is
            // single-lane in the routes), which the generator does not know yet: there the loop still drives its lane
            foreach (var name in new[] { "pit-ramp", "fill-return", "yard-road" })
                Assert.Greater(frameCount.TryGetValue(name, out var n) ? n : 0, 20, $"the loop drives along {name}");
            Assert.Greater(onRoad, 200, "the loop spends much of its time driving along roads");
            Assert.GreaterOrEqual(inLane, onRoad * 0.9, $"{inLane} of {onRoad} frames driving along a road are within a metre of its lane (the rest are where a lane eases in at a junction)");
            offBy.Sort();
            Assert.LessOrEqual(offBy[offBy.Count / 2], 0.3, "and the typical frame is on it");
        }

        // is the point on the road, well clear of its ends, travelling along it? and how far is it to the left of its direction of travel?
        static bool Beside(RoadLine road, double x, double z, double mx, double mz, out double left)
        {
            left = 0.0;
            for (var i = 1; i < road.Points.Count; i++)
            {
                double ax = road.Points[i - 1].X, az = road.Points[i - 1].Z, bx = road.Points[i].X, bz = road.Points[i].Z;
                double dx = bx - ax, dz = bz - az;
                var len2 = dx * dx + dz * dz;
                if (len2 < 1e-9) continue;
                var t = ((x - ax) * dx + (z - az) * dz) / len2;
                if (t < 0.0 || t > 1.0) continue;
                var len = Math.Sqrt(len2);
                double ux = dx / len, uz = dz / len;
                if (Math.Abs(ux * mx + uz * mz) < 0.97) continue;
                var cx = ax + dx * t;
                var cz = az + dz * t;
                var perp = Math.Sqrt((x - cx) * (x - cx) + (z - cz) * (z - cz));
                if (perp > road.Width / 2.0) continue;
                // not near either end of the road, where a lane begins or ends
                var along = CumulativeTo(road, i - 1) + len * t;
                if (along < 14.0 || along > Total(road) - 14.0) continue;
                // the side of the direction of travel the point is on (+: left)
                double fx = ux * mx + uz * mz > 0 ? ux : -ux, fz = ux * mx + uz * mz > 0 ? uz : -uz;
                left = (x - cx) * (-fz) + (z - cz) * fx;
                return true;
            }

            return false;
        }

        static double CumulativeTo(RoadLine r, int i)
        {
            var s = 0.0;
            for (var k = 1; k <= i; k++) s += Math.Sqrt(Math.Pow(r.Points[k].X - r.Points[k - 1].X, 2) + Math.Pow(r.Points[k].Z - r.Points[k - 1].Z, 2));
            return s;
        }

        static double Total(RoadLine r) => CumulativeTo(r, r.Points.Count - 1);

        // ---- the footprint

        [Test]
        public void AHaulTruckIsTwoBoxesAndTheOthersOne()
        {
            Assert.AreEqual(2, Footprint.At(EquipmentKind.Hauler, 0, 0, 0).Count);
            Assert.AreEqual(1, Footprint.At(EquipmentKind.Loader, 0, 0, 0).Count);
            Assert.AreEqual(1, Footprint.At(EquipmentKind.Dozer, 0, 0, 0).Count);
        }

        [Test]
        public void AHaulTrucksNarrowBodyLetsAnotherPassCloserThanItsWideFrontWould()
        {
            // truck B stands beside the narrow body and rear tyres of truck A, 5.3 m to the side: 0.2 m of air to a 2.25 m half width,
            // and it would be inside the 2.85 m half width of A's front deck
            var a = Footprint.At(EquipmentKind.Hauler, 0, 0, 0);
            var b = Footprint.At(EquipmentKind.Hauler, 5.3, -8.0, 0);
            Assert.AreEqual(0.2, Footprint.Gap(a, b), 1e-6);
            Assert.AreEqual(Footprint.Gap(a, b), Footprint.Gap(b, a), 1e-9, "the gap does not depend on which is asked about");
            Assert.Less(Footprint.Gap(a, Footprint.At(EquipmentKind.Hauler, 5.0, -8.0, 0)), 0.0, "5.0 m to the side is touching");
        }

        [Test]
        public void AFarOffShapeIsToldApartByItsBoundingCircle()
        {
            var a = Footprint.At(EquipmentKind.Hauler, 0, 0, 30);
            var far = Footprint.At(EquipmentKind.Hauler, 200, 0, 30);
            var gap = Footprint.Gap(a, far);
            Assert.Greater(gap, Footprint.FarMetres, "beyond 20 m a bound is reported, not the measured gap");
            Assert.LessOrEqual(gap, 200.0 - 2.0 * 2.85);
            for (var i = 0; i < 2; i++)
            {
                var q = i == 0 ? a.First : a.Second;
                foreach (var (cx, cz) in new[] { (q.Ax, q.Az), (q.Bx, q.Bz), (q.Cx, q.Cz), (q.Dx, q.Dz) })
                    Assert.LessOrEqual(Math.Sqrt((cx - a.CentreX) * (cx - a.CentreX) + (cz - a.CentreZ) * (cz - a.CentreZ)), a.Radius + 1e-9, "every corner is inside the circle");
            }
        }

        [Test]
        public void TheFootprintsAreTheOnesTheChoreographyIsGeneratedAndCheckedWith()
        {
            var py = File.ReadAllText(Path.Combine(Application.dataPath, "..", "ArtSource", "terrain", "quarry_fleet.py"));
            double[] Nums(string pattern)
            {
                var m = Regex.Match(py, pattern);
                Assert.IsTrue(m.Success, pattern);
                return m.Groups.Cast<Group>().Skip(1).Select(g => double.Parse(g.Value, System.Globalization.CultureInfo.InvariantCulture)).ToArray();
            }

            var ci = @"(-?\d+\.\d+)";
            foreach (var (name, kind) in new[] { ("Dozer", EquipmentKind.Dozer), ("Loader", EquipmentKind.Loader), ("Hauler", EquipmentKind.Hauler) })
            {
                var v = Nums($"\"{name}\": \\({ci}, {ci}, {ci}\\)[,}}]");
                Footprint.Dimensions(kind, out var w, out var front, out var rear);
                CollectionAssert.AreEqual(new[] { w, front, rear }, v, name);
            }

            var parts = Nums($"FOOT_PARTS = {{\"Hauler\": \\[\\({ci}, {ci}, {ci}\\), \\({ci}, {ci}, {ci}\\)\\]}}");
            var s = Footprint.At(EquipmentKind.Hauler, 0, 0, 0);
            // heading 0 faces +z: a box's corners are (+w, +front), (-w, +front), (-w, -rear), (+w, -rear)
            CollectionAssert.AreEqual(new[] { s.First.Ax, s.First.Az, -s.First.Cz }, new[] { parts[0], parts[1], parts[2] }, "the wide front deck");
            CollectionAssert.AreEqual(new[] { s.Second.Ax, s.Second.Az, -s.Second.Cz }, new[] { parts[3], parts[4], parts[5] }, "and the narrow body and rear tyres");
        }
    }
}
