// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text.Json;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    // What every moving machine of the committed choreography drives past and over, and the haul loop's grade: the files against the site's
    // props, piles and ground, by the rules the task layer plans a commanded route by (the travel reach and clearance, the grade limit).
    // ArtSource/terrain/quarry_fleet.py checks the same things when it writes the fleets (`check`); this reads the files it wrote, with the
    // same table of what a machine works AT. The controls below pin each reading on its own: a check that cannot fail is not a check.
    public sealed class SiteClearanceTests
    {
        static readonly string[] FleetFiles = { "Data/quarry_fleet.json", "Data/quarry_fleet_live.json" };

        /// <summary>
        /// What a machine works AT on purpose, and so is not held to the reach: (machine, obstacle, spot, radius). A spot of null is the whole
        /// track (a pile is its loader's or its dozer's own workface); otherwise only while the machine is within the radius of that spot.
        /// The same table as <c>WORKS_AT</c> in quarry_fleet.py, and nothing else is exempt.
        /// </summary>
        static readonly (string Machine, string Obstacle, string Spot, double Radius)[] WorksAt =
        {
            ("SP-HL-0006", "refuel-approach", null, 0.0),
            ("SP-HL-0006", "fuel_tank", "refuel-bay", 12.0),
            ("SP-HL-0006", "cone", "refuel-bay", 12.0),
            ("SP-LD-0001", "muck-pile", null, 0.0),
            ("SP-LD-0002", "pit-stockpile", null, 0.0),
            ("SP-LD-0003", "feed-stockpile", null, 0.0),
            ("SP-LD-0003", "crusher_plant", "plant-feed", 16.0),
            ("SP-LD-0004", "product-coarse", null, 0.0),
            ("SP-DZ-0001", "muck-pile", null, 0.0),
            ("SP-DZ-0003", "fill-heap-3", null, 0.0),
        };

        /// <summary>How near a driven line comes to what stands, as margins: the distance minus what is required (negative = short).</summary>
        readonly struct Clearance
        {
            public Clearance(double reach, string reachAt, double air, string airAt)
            {
                Reach = reach;
                ReachAt = reachAt;
                Air = air;
                AirAt = airAt;
            }

            /// <summary>The planner's reading: the machine's point at least <see cref="ParkingLot.TravelRadius"/> + <see cref="ParkingLot.TravelClearance"/> from every outline.</summary>
            public double Reach { get; }
            public string ReachAt { get; }

            /// <summary>The machine's own footprint at least <see cref="ParkingLot.TravelClearance"/> from every outline: props, piles and the refuel approach alike.</summary>
            public double Air { get; }
            public string AirAt { get; }
        }

        /// <summary>One moving track of a fleet file, and the machine that plays it.</summary>
        sealed class Moving
        {
            public string Machine;
            public EquipmentKind Kind;
            public List<TrackPoint> Frames;
            public List<bool> BoomRaised;
        }

        static EquipmentKind KindOf(string kind) => kind == "Hauler" ? EquipmentKind.Hauler : kind == "Loader" ? EquipmentKind.Loader : EquipmentKind.Dozer;

        /// <summary>Every track of the file that moves (a parked machine does not drive), with the first machine on it.</summary>
        static List<Moving> MovingTracks(string file)
        {
            using var doc = JsonDocument.Parse(File.ReadAllText(CommandKit.AssetPath(file)));
            var dt = doc.RootElement.GetProperty("dt").GetDouble();
            var owner = new Dictionary<int, string>();
            foreach (var m in doc.RootElement.GetProperty("machines").EnumerateArray())
                owner.TryAdd(m.GetProperty("track").GetInt32(), m.GetProperty("id").GetString());
            var list = new List<Moving>();
            var tracks = doc.RootElement.GetProperty("tracks");
            for (var t = 0; t < tracks.GetArrayLength(); t++)
            {
                var data = tracks[t].GetProperty("data");
                var n = data.GetArrayLength() / 8;
                var frames = new List<TrackPoint>(n);
                var boom = new List<bool>(n);
                double x0 = double.MaxValue, x1 = double.MinValue, z0 = double.MaxValue, z1 = double.MinValue;
                for (var i = 0; i < n; i++)
                {
                    double x = data[i * 8].GetDouble(), z = data[i * 8 + 1].GetDouble();
                    frames.Add(new TrackPoint(x, z, data[i * 8 + 2].GetDouble(), i * dt));
                    boom.Add(data[i * 8 + 4].GetDouble() < Footprint.LoaderRaisedBoomDegrees);
                    x0 = Math.Min(x0, x); x1 = Math.Max(x1, x); z0 = Math.Min(z0, z); z1 = Math.Max(z1, z);
                }

                if (x1 - x0 < 1.0 && z1 - z0 < 1.0) continue;
                list.Add(new Moving { Machine = owner[t], Kind = KindOf(tracks[t].GetProperty("kind").GetString()), Frames = frames, BoomRaised = boom });
            }

            return list;
        }

        /// <summary>The haul loop (track 0) of one fleet file.</summary>
        static List<TrackPoint> LoopOf(string file)
        {
            var loop = MovingTracks(file)[0];
            Assert.AreEqual(EquipmentKind.Hauler, loop.Kind, "track 0 is the haul loop");
            return loop.Frames;
        }

        /// <summary>Whether the machine works at this outline from this place: the callback of <see cref="Measure"/> from <see cref="WorksAt"/>.</summary>
        static Func<Obstacle, double, double, bool> ExemptFor(string machine, IReadOnlyDictionary<string, Spot> spots)
        {
            var mine = WorksAt.Where(e => e.Machine == machine).ToList();
            return (o, x, z) => mine.Any(e => e.Obstacle == o.Name && (e.Spot == null || Math.Sqrt(Math.Pow(x - spots[e.Spot].X, 2) + Math.Pow(z - spots[e.Spot].Z, 2)) <= e.Radius));
        }

        /// <summary>The points of a footprint, every quarter metre over its edges and half a metre inside them.</summary>
        static IEnumerable<(double X, double Z)> Samples(in FootprintShape shape)
        {
            var list = new List<(double, double)>();
            foreach (var q in shape.Count == 2 ? new[] { shape.First, shape.Second } : new[] { shape.First })
            {
                // A -> B is the front edge, A -> D the side: a point is A + u (B - A) + v (D - A)
                double bx = q.Bx - q.Ax, bz = q.Bz - q.Az, dx = q.Dx - q.Ax, dz = q.Dz - q.Az;
                var nu = Math.Max(1, (int)Math.Ceiling(Math.Sqrt(bx * bx + bz * bz) / 0.25));
                var nv = Math.Max(1, (int)Math.Ceiling(Math.Sqrt(dx * dx + dz * dz) / 0.25));
                for (var i = 0; i <= nu; i++)
                    for (var j = 0; j <= nv; j++)
                    {
                        if (i % 2 == 1 && j % 2 == 1 && i != nu && j != nv) continue;   // inside: every half metre
                        double u = (double)i / nu, v = (double)j / nv;
                        list.Add((q.Ax + u * bx + v * dx, q.Az + u * bz + v * dz));
                    }
            }

            return list;
        }

        static Clearance Measure(IReadOnlyList<TrackPoint> loop, IReadOnlyList<Obstacle> obstacles, EquipmentKind kind = EquipmentKind.Hauler,
            Func<Obstacle, double, double, bool> exempt = null, IReadOnlyList<bool> boom = null)
        {
            var need = ParkingLot.TravelRadius(kind) + ParkingLot.TravelClearance;
            double reach = double.MaxValue, air = double.MaxValue;
            string reachAt = null, airAt = null;
            for (var i = 0; i < loop.Count; i++)
            {
                var p = loop[i];
                FootprintShape? shape = null;
                foreach (var o in obstacles)
                {
                    var d = o.Distance(p.X, p.Z) - need;
                    if (exempt != null && exempt(o, p.X, p.Z)) continue;
                    if (d < reach) { reach = d; reachAt = $"{o.Name} (t = {p.TrackSeconds:0.00} s, the machine at {p.X:0.0}, {p.Z:0.0})"; }
                    // an outline farther than the footprint's radius (a hauler's is the largest) plus the air cannot be touched from here
                    if (o.IsAtLeast(p.X, p.Z, ParkingLot.FootprintRadius(EquipmentKind.Hauler) + 2.0)) continue;
                    shape ??= Footprint.At(kind, p.X, p.Z, p.HeadingDegrees, boom != null && boom[i]);
                    var gap = double.MaxValue;
                    foreach (var (x, z) in Samples(shape.Value)) gap = Math.Min(gap, o.Distance(x, z));
                    gap -= ParkingLot.TravelClearance;
                    if (gap < air) { air = gap; airAt = $"{o.Name} (t = {p.TrackSeconds:0.00} s, the machine at {p.X:0.0}, {p.Z:0.0})"; }
                }
            }

            return new Clearance(reach, reachAt, air, airAt);
        }

        // ---- every moving machine against what stands

        [Test]
        public void EveryMovingMachineIsClearOfEverythingStandingOnTheSiteExceptWhatItWorksAt()
        {
            var obstacles = CommandKit.Site.Obstacles;
            Assert.Greater(obstacles.Count(o => o.IsBox), 30, "the site's props are read (the check has something to find)");
            Assert.Greater(obstacles.Count(o => !o.IsBox), 10, "and its piles and the refuel approach");
            foreach (var file in FleetFiles)
            {
                var tracks = MovingTracks(file);
                Assert.GreaterOrEqual(tracks.Count, 8, $"{file}: the moving tracks are read (the haul loop, the loaders, the dozers)");
                Assert.AreEqual("SP-HL-0001", tracks[0].Machine, $"{file}: track 0 is the loop");
                foreach (var t in tracks)
                {
                    Assert.Greater(t.Frames.Count, 40, $"{file} {t.Machine}: the track has frames");
                    var c = Measure(t.Frames, obstacles, t.Kind, ExemptFor(t.Machine, CommandKit.Site.Spots), t.BoomRaised);
                    var need = ParkingLot.TravelRadius(t.Kind) + ParkingLot.TravelClearance;
                    Assert.GreaterOrEqual(c.Reach, 0.0, $"{file} {t.Machine}: the machine's point comes {-c.Reach:0.00} m nearer than the {need:0.0} m a driving {t.Kind} keeps, to {c.ReachAt}");
                    Assert.GreaterOrEqual(c.Air, 0.0, $"{file} {t.Machine}: the machine's footprint comes {-c.Air:0.00} m nearer than the {ParkingLot.TravelClearance:0.0} m of air kept, to {c.AirAt}");
                }
            }
        }

        [Test]
        public void EveryExemptionIsNeededByTheMachineItNames()
        {
            // an exemption nothing exercises is free to delete: without it, the machine must come short of what it is exempt from
            foreach (var file in FleetFiles)
            {
                var tracks = MovingTracks(file);
                foreach (var e in WorksAt)
                {
                    var t = tracks.FirstOrDefault(x => x.Machine == e.Machine);
                    if (t == null) { Assert.AreEqual("SP-HL-0006", e.Machine, $"{file}: only the preview fleet's scripted refuel visit may be missing from a fleet"); continue; }
                    var only = CommandKit.Site.Obstacles.Where(o => o.Name == e.Obstacle).ToList();
                    Assert.IsNotEmpty(only, $"{e.Machine}: the site has a {e.Obstacle}");
                    var c = Measure(t.Frames, only, t.Kind, null, t.BoomRaised);
                    Assert.Less(Math.Min(c.Reach, c.Air), 0.0, $"{file}: {e.Machine} is exempt from {e.Obstacle} but comes no nearer than the reach and the air kept");
                }
            }
        }

        // ---- the controls: the same measure on a site made to break each reading

        [Test]
        public void ATowerStandingOnTheLoopIsCaughtByBothReadings()
        {
            var loop = LoopOf("Data/quarry_fleet_live.json");
            var on = loop[loop.Count / 3];
            var site = CommandKit.Site.Obstacles.Concat(new[] { Obstacle.Box("light_tower", on.X, on.Z, 2.5, 1.7, 0.0) }).ToList();
            var c = Measure(loop, site);
            StringAssert.StartsWith("light_tower", c.ReachAt, "the tower on the loop is the nearest thing by the planner's reading");
            StringAssert.StartsWith("light_tower", c.AirAt, "and by the footprint's");
            Assert.Less(c.Reach, -3.0, "its point is inside the tower");
            Assert.Less(c.Air, -1.5, "and its footprint overlaps");
        }

        /// <summary>The out leg along z = 26.5 heading west, straight for 24 m: the stretch the controls stand beside.</summary>
        static List<TrackPoint> StraightLeg() => LoopOf("Data/quarry_fleet_live.json")
            .Where(p => Math.Abs(p.X - 5.0) < 12.0 && Math.Abs(p.Z - 26.5) < 0.5 && Math.Abs(Math.Sin(p.HeadingDegrees * Math.PI / 180.0)) > 0.99).ToList();

        [Test]
        public void ATowerBeyondTheReachPassesAndOneJustInsideItFailsAtTheBoundary()
        {
            var stretch = StraightLeg();
            Assert.Greater(stretch.Count, 8, "the straight leg is found");
            // the line's nearest point to a tower standing north of it, over the tower's width
            var zNear = stretch.Where(p => Math.Abs(p.X - 5.0) <= 2.5).Max(p => p.Z);
            var reach = ParkingLot.TravelRadius(EquipmentKind.Hauler) + ParkingLot.TravelClearance;
            Assert.AreEqual(3.2, reach, 1e-9, "the reach the planner uses: the truck's half width and the air kept");
            foreach (var (d, passes) in new[] { (3.1, false), (3.3, true) })
            {
                var tower = Obstacle.Box("light_tower", 5.0, zNear + d + 1.7, 2.5, 1.7, 0.0);
                var c = Measure(stretch, new[] { tower });
                Assert.AreEqual(d - reach, c.Reach, 0.03, $"a tower {d:0.0} m off the line is {d - reach:+0.0;-0.0} m from the reach");
                Assert.AreEqual(passes, c.Reach >= 0.0, $"a tower {d:0.0} m off the line {(passes ? "passes" : "fails")}");
            }
        }

        [Test]
        public void TheFootprintIsHeldToTheAirKeptEvenWhereThePointIsNearlyFarEnough()
        {
            // a tower 2.95 m off the line: the truck's side (2.85 m from its line) is 0.10 m from it, 0.10 m short of the 0.2 m of air kept
            var stretch = StraightLeg();
            var zNear = stretch.Where(p => Math.Abs(p.X - 5.0) <= 2.5).Max(p => p.Z);
            var tower = Obstacle.Box("light_tower", 5.0, zNear + 2.95 + 1.7, 2.5, 1.7, 0.0);
            var c = Measure(stretch, new[] { tower });
            Assert.AreEqual(-0.10, c.Air, 0.04, "the air is the gap less the 0.2 m kept, so 0.10 m of gap is 0.10 m short");
        }

        [Test]
        public void ATowerAheadOnTheFrontDeckCornerIsCaughtWhereThePointIsFarOff()
        {
            // one frame of the west-heading leg and a tower whose near face is 5.0 m ahead: the point is 5.0 m from it (1.8 m beyond the reach) but the
            // truck's front deck, 5.7 m ahead, is 0.7 m into it. Only the footprint reading sees it, and only if it looks at props beyond arm's length
            var at = StraightLeg().First(p => Math.Abs(p.X - 5.0) < 1.0);
            var tower = Obstacle.Box("light_tower", at.X - 5.0 - 2.5, at.Z, 2.5, 1.7, 0.0);
            var c = Measure(new[] { at }, new[] { tower });
            Assert.AreEqual(5.0 - 3.2, c.Reach, 0.05, "the point is well clear by the planner's reading");
            Assert.Less(c.Air, -0.7, $"the front deck is in the tower ({c.Air:0.00} m)");
        }

        [Test]
        public void APileStandingBesideTheLoopIsHeldToBothReadingsToo()
        {
            // piles and the refuel approach are outlines like props: a capsule whose edge is 3.0 m off the line
            var stretch = StraightLeg();
            var zNear = stretch.Max(p => p.Z);
            var pile = Obstacle.Capsule("a-pile", -5.0, zNear + 3.0 + 4.0, 15.0, zNear + 3.0 + 4.0, 4.0);
            var c = Measure(stretch, new[] { pile });
            Assert.AreEqual(3.0 - 3.2, c.Reach, 0.03, "the pile's edge is 0.2 m inside the reach");
            Assert.AreEqual("a-pile", c.ReachAt.Split(' ')[0]);
            Assert.AreEqual(3.0 - 2.85 - 0.2, c.Air, 0.04, "and 0.05 m short of the air kept");
            Assert.AreEqual("a-pile", c.AirAt.Split(' ')[0]);
        }

        [Test]
        public void AnExemptionHoldsOnlyForItsMachineAndWithinItsSpot()
        {
            var spots = CommandKit.Site.Spots;
            var bay = spots["refuel-bay"];
            var tank = Obstacle.Box("fuel_tank", 0, 0, 5.2, 3.5, 0.0);
            var exemptHl6 = ExemptFor("SP-HL-0006", spots);
            Assert.IsTrue(exemptHl6(tank, bay.X, bay.Z), "the refuel visit works at the tank by the bay");
            Assert.IsTrue(exemptHl6(tank, bay.X + 11.9, bay.Z), "within 12 m of it");
            Assert.IsFalse(exemptHl6(tank, bay.X + 12.1, bay.Z), "not beyond: the way back to park is held to the tank");
            Assert.IsFalse(ExemptFor("SP-HL-0001", spots)(tank, bay.X, bay.Z), "and no other machine is exempt");
            Assert.IsFalse(exemptHl6(Obstacle.Box("light_tower", 0, 0, 2.5, 1.7, 0.0), bay.X, bay.Z), "nor from any other prop");
            Assert.IsFalse(ExemptFor("SP-LD-0001", spots)(Obstacle.Capsule("pit-stockpile", 0, 0, 1, 0, 1), 0, 0), "a loader works its own pile, not another's");
        }

        // ---- the loop's driven line against the grade limit

        static double SteepestPct(IReadOnlyList<TrackPoint> loop, IHeightField ground, out string at)
        {
            var xs = loop.Select(p => p.X).ToArray();
            var zs = loop.Select(p => p.Z).ToArray();
            var worst = Grade.MaxSustained(ground, xs, zs);
            var legs = Grade.PerLeg(ground, xs, zs);
            var k = Array.IndexOf(legs, legs.Max());
            at = $"near ({xs[k]:0.0}, {zs[k]:0.0}), t = {loop[k].TrackSeconds:0.00} s";
            return worst;
        }

        /// <summary>The one place the loop's grade is held to the limit: the test of the committed files and the controls both come through it.</summary>
        static void AssertWithinGradeLimit(IReadOnlyList<TrackPoint> loop, IHeightField ground, string what)
        {
            var worst = SteepestPct(loop, ground, out var at);
            Assert.LessOrEqual(worst, Grade.MaxPct, $"{what}: the loop climbs {worst:0.00}% sustained, {at}; a planned route is held to {Grade.MaxPct}%");
        }

        [Test]
        public void TheHaulLoopsDrivenLineIsWithinTheGradeLimit()
        {
            foreach (var file in FleetFiles)
            {
                AssertWithinGradeLimit(LoopOf(file), CommandKit.Ground, file);
                // the steepest place is the ramp's inside lane, eased only as far as the limit needs: the check reads the whole loop to find it
                var worst = SteepestPct(LoopOf(file), CommandKit.Ground, out var at);
                Assert.Greater(worst, 11.0, $"{file}: the steepest place the loop drives is the ramp's inside lane, near the limit ({worst:0.00}%, {at})");
            }
        }

        /// <summary>Ground that is flat but for a hill along the west-heading leg: it climbs at <c>slope</c> for 14 m from x = -12 and falls again over the next 14 m.</summary>
        sealed class Hill : IHeightField
        {
            readonly double slope;

            public Hill(double slope) { this.slope = slope; }

            public bool Covers(double x, double z) => true;

            public double HeightAt(double x, double z)
            {
                var u = x + 12.0;
                if (u <= 0.0 || u >= 28.0) return 0.0;
                return u <= 14.0 ? slope * u : slope * (28.0 - u);
            }
        }

        [Test]
        public void AClimbJustOverTheLimitIsRefusedAndOneJustUnderItIsAccepted()
        {
            var leg = LoopOf("Data/quarry_fleet_live.json")
                .Where(p => p.X > -14.0 && p.X < 17.0 && Math.Abs(p.Z - 26.5) < 0.6 && Math.Abs(Math.Sin(p.HeadingDegrees * Math.PI / 180.0)) > 0.99).ToList();
            Assert.Greater(leg.Count, 20, "the leg is found");
            // 12.3% is over 12 and under 12.5, so a limit loosened by half a percent lets it through
            var over = Assert.Throws<AssertionException>(() => AssertWithinGradeLimit(leg, new Hill(0.123), "control"));
            StringAssert.Contains("12.3", over.Message, "the climb is read at its own grade");
            Assert.DoesNotThrow(() => AssertWithinGradeLimit(leg, new Hill(0.117), "control"), "11.7% is within the limit");
            // read over a 10 m window: the same hill read over twice the window would be half as steep
            Assert.AreEqual(12.3, SteepestPct(leg, new Hill(0.123), out _), 0.1, "the grade is the rise over 10 m");
        }

        /// <summary>A cone of the given slope and 12 m radius about a point on flat ground.</summary>
        sealed class Cone : IHeightField
        {
            readonly double cx, cz, slope;

            public Cone(double cx, double cz, double slope) { this.cx = cx; this.cz = cz; this.slope = slope; }

            public bool Covers(double x, double z) => true;

            public double HeightAt(double x, double z) => Math.Max(0.0, 12.0 - Math.Sqrt((x - cx) * (x - cx) + (z - cz) * (z - cz))) * slope;
        }

        [Test]
        public void AClimbOnlyInTheSecondHalfOfTheLoopIsSeenToo()
        {
            // the check reads the whole loop: put a steep cone under the place in its second half that is farthest from anywhere the first half goes
            var loop = LoopOf("Data/quarry_fleet_live.json");
            var first = loop.Take(loop.Count / 2).ToList();
            var late = loop.Skip(loop.Count / 2).OrderByDescending(p => first.Min(q => Math.Sqrt((p.X - q.X) * (p.X - q.X) + (p.Z - q.Z) * (p.Z - q.Z)))).First();
            Assert.Greater(first.Min(q => Math.Sqrt((late.X - q.X) * (late.X - q.X) + (late.Z - q.Z) * (late.Z - q.Z))), 14.0, "the cone is out of the first half's reach");
            Assert.Throws<AssertionException>(() => AssertWithinGradeLimit(loop, new Cone(late.X, late.Z, 0.5), "control"), "a hill under only the loop's second half is refused");
        }
    }
}
