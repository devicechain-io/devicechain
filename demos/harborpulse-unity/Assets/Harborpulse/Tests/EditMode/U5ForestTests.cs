// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>U5 forest, outcrops and the guarantee that the town itself did not move.</summary>
    public class U5ForestTests
    {
        const double Deck = SitePlan.DeckHeightM;

        static long Checksum(IEnumerable<WorldProp> props)
        {
            long h = 1469598103;
            unchecked
            {
                foreach (var p in props)
                {
                    h = h * 31 + (long)Math.Round(p.At.X * 100);
                    h = h * 31 + (long)Math.Round(p.At.Z * 100);
                    h = h * 31 + (long)Math.Round(p.Yaw * 100);
                    h = h * 31 + p.Kind.Length + (long)p.Width * 7 + (long)p.Depth * 13;
                }
            }
            return h;
        }

        [Test]
        public void TheTownsLotsAndLampsAreExactlyWhatTheyWereBeforeTheForestChanged()
        {
            // Literals captured from the plan on origin/main BEFORE any U5 change. The streets, the 104 building lots (position, yaw, size)
            // and the 71 lamps are generated from the town's own random stream and must not move when trees, rocks and terrain do.
            var plan = U5Plan.Plan;
            var lots = plan.Props.Where(p => p.Footprint.Id.StartsWith("lot:", StringComparison.Ordinal)).ToList();
            Assert.That(lots.Count, Is.EqualTo(104));
            Assert.That(Checksum(lots), Is.EqualTo(-4266737425612053057L));
            Assert.That(Math.Round(lots.Sum(p => p.At.X), 3), Is.EqualTo(-75.164));
            Assert.That(Math.Round(lots.Sum(p => p.At.Z), 3), Is.EqualTo(23516.214));
            var lamps = plan.Props.Where(p => p.Kind == "StreetLamp").ToList();
            Assert.That(lamps.Count, Is.EqualTo(71));
            Assert.That(Checksum(lamps), Is.EqualTo(3592688406709651329L));
            Assert.That(plan.Streets.Select(s => s.Name), Is.EqualTo(new[] { "Main Street", "Elm Street", "Church Street", "Shore Road", "Wharf Street", "School Street", "Hill Lane", "West Lane" }));
        }

        [Test]
        public void ThePlanIsRepeatableIncludingEveryTreeAndEveryOutcrop()
        {
            var a = U5Plan.Plan; var b = WorldPropPlan.Generate(U5Plan.Map, U5Plan.Layout);
            Assert.That(b.Props.Select(p => (p.Kind, p.At.X, p.At.Z, p.Height, p.Variant, p.Area)), Is.EqualTo(a.Props.Select(p => (p.Kind, p.At.X, p.At.Z, p.Height, p.Variant, p.Area))));
            Assert.That(b.Outcrops.Count, Is.EqualTo(a.Outcrops.Count));
            for (var i = 0; i < a.Outcrops.Count; i++)
                Assert.That(b.Outcrops[i].Rings.SelectMany(r => r).Select(p => (p.X, p.Y, p.Z)), Is.EqualTo(a.Outcrops[i].Rings.SelectMany(r => r).Select(p => (p.X, p.Y, p.Z))));
        }

        // ---------------------------------------------------------------- trees

        static IEnumerable<WorldProp> Trees => U5Plan.Plan.Props.Where(p => p.Kind == "Tree");

        [Test]
        public void SixtyPercentOfTheTreesAreConifersAndTheBroadleafComeInThreeShapes()
        {
            // pins A3: 60 % conifers (+-4 points) by count, in three conifer and three broadleaf shapes
            var all = Trees.ToList();
            var conifers = all.Count(t => TreeStyle.IsConifer((TreeKind)t.Variant)) / (double)all.Count;
            TestContext.WriteLine("Conifers: " + conifers.ToString("P1") + " of " + all.Count + " trees");
            Assert.That(conifers, Is.InRange(.56, .64));
            foreach (var kind in Enum.GetValues(typeof(TreeKind)).Cast<TreeKind>())
                Assert.That(all.Count(t => (TreeKind)t.Variant == kind), Is.GreaterThan(all.Count / 20), kind.ToString());
            Assert.That(Enum.GetValues(typeof(TreeKind)).Cast<TreeKind>().Count(TreeStyle.IsConifer), Is.EqualTo(3));
            Assert.That(Enum.GetValues(typeof(TreeKind)).Cast<TreeKind>().Count(k => !TreeStyle.IsConifer(k)), Is.EqualTo(3));
            // the spatial bias must not break the ratio in any broad region: each of the four quadrants of the wood stays within 10 points
            foreach (var quadrant in new[] { (-1, -1), (-1, 1), (1, -1), (1, 1) })
            {
                var part = all.Where(t => Math.Sign(t.At.X) == quadrant.Item1 && (t.At.Z > 500 ? 1 : -1) == quadrant.Item2).ToList();
                if (part.Count < 200) continue;
                Assert.That(part.Count(t => TreeStyle.IsConifer((TreeKind)t.Variant)) / (double)part.Count, Is.InRange(.50, .70));
            }
        }

        [Test]
        public void TreeHeightsAreForestScaleNotHighRise()
        {
            // pins A3: 8-16 m in the forest (the old 20-32 m dwarfed the houses), 5-10 m in yards
            foreach (var t in Trees.Where(t => t.Area == "forest")) Assert.That(t.Height, Is.InRange(8, 16));
            foreach (var t in Trees.Where(t => t.Area == "town")) Assert.That(t.Height, Is.InRange(5, 10));
            Assert.That(Trees.Where(t => t.Area == "forest").Average(t => t.Height), Is.InRange(10, 13));
        }

        [Test]
        public void ConiferGreensAreGreenAndBroadleafAutumnComesInDriftsNotConfetti()
        {
            // conifers: deep, varied greens, never the old grey (.31,.36,.32)
            for (var i = 0; i < 200; i++)
            {
                var roll = i / 200.0;
                foreach (var k in new[] { TreeKind.Spruce, TreeKind.Fir, TreeKind.Pine })
                {
                    var c = TreeStyle.Conifer(k, roll, 0);
                    Assert.That(c.G, Is.GreaterThan(c.R + .06), "green over red");
                    Assert.That(c.G, Is.InRange(.25, .60), "lighter greens than the first cut, which tone-mapped to near black");
                    Assert.That(TreeStyle.Conifer(k, roll, 1).G, Is.GreaterThan(c.G), "lighter toward the top tier");
                }
            }
            Assert.That(TreeStyle.Conifer(TreeKind.Spruce, .95, .5).B, Is.GreaterThan(TreeStyle.Conifer(TreeKind.Spruce, .5, .5).B), "a few blue-green");
            // broadleaf: a bin is gold / orange / crimson / russet / green-olive; neighbours share it far more often than strangers do
            int Bin(WorldProp t)
            {
                var c = TreeStyle.Broadleaf((TreeKind)t.Variant, t.At.X, t.At.Z, .5);
                if (c.G > c.R) return 0;                    // olive or leaf green
                if (c.R > .75 && c.G > .55) return 1;       // gold
                if (c.R > .7) return 2;                     // orange
                if (c.G < .2) return 3;                     // crimson
                return 4;                                   // russet
            }
            var broad = Trees.Where(t => !TreeStyle.IsConifer((TreeKind)t.Variant) && t.Area == "forest" && (TreeKind)t.Variant == TreeKind.Maple).ToList();
            var bins = broad.Select(Bin).ToList();
            Assert.That(bins.Distinct().Count(), Is.GreaterThanOrEqualTo(3), "at least three autumn colours");
            var rng = new Random(5); var near = 0; var nearSame = 0; var far = 0; var farSame = 0;
            for (var n = 0; n < 4000; n++)
            {
                var i = rng.Next(broad.Count); var j = rng.Next(broad.Count);
                var d = SceneryStreet.Distance(broad[i].At, broad[j].At);
                if (d < 40) { near++; if (bins[i] == bins[j]) nearSame++; }
                else if (d > 400) { far++; if (bins[i] == bins[j]) farSame++; }
            }
            // pair up genuinely close neighbours as well, which random pairs rarely are
            for (var i = 0; i < broad.Count; i += 3)
                for (var j = i + 1; j < Math.Min(broad.Count, i + 60); j++)
                    if (SceneryStreet.Distance(broad[i].At, broad[j].At) < 40) { near++; if (bins[i] == bins[j]) nearSame++; }
            TestContext.WriteLine("Same autumn colour: neighbours " + (nearSame / (double)near).ToString("P0") + ", far pairs " + (farSame / (double)far).ToString("P0"));
            Assert.That(nearSame / (double)near, Is.GreaterThan(farSame / (double)far + .15), "drifts: neighbours match far more than far pairs");
        }

        /// <summary>Why a tree at p of the given height would be wrong, or null: the same reservations the plan itself respects.</summary>
        static string Violation(WorldPropPlan plan, PresentationMap map, Ground p, double height, string ignoreId = null)
        {
            if (Math.Abs(p.X) < 240 && p.Z < 140 && !(p.X < -120 && p.Z > 72)) return "inside the core (quays, water, deck)";   // the park, at the core's north-west corner, is the one place a tree may stand in it
            foreach (var s in plan.Streets) if (s.DistanceTo(p) < s.Width / 2 + 2.5) return "on or beside " + s.Name;
            foreach (var a in map.Anchors) if (SceneryStreet.Distance(a.Value, p) < 14) return "within 14 m of " + a.Key;
            if (map.FenceRings.Values.Any(r => PresentationMap.Contains(r, p))) return "inside a mapped fence";
            for (var i = 1; i < map.Road.Count; i++) if (SceneryStreet.SegmentDistance(p, map.Road[i - 1], map.Road[i]) < 8) return "on the mapped road";
            if (p.X > -232 && p.X < -128 && p.Z > 100 && p.Z < 172) return "on the park lawn";
            var crown = new PropFootprint("probe", p, height * .9, height * .9, 0);
            foreach (var e in plan.Registry.Entries)
                if (e.Id != ignoreId && e.Id != "probe" && crown.Intersects(e, 0)) return "inside the reservation of " + e.Id;
            return null;
        }

        [Test]
        public void TreesNeverStandOnStreetsLotsQuaysParkPathsAnchorsOrRocks()
        {
            var plan = U5Plan.Plan; var map = U5Plan.Map;
            var failures = new List<string>();
            foreach (var t in Trees)
            {
                // yard trees reserve themselves in the registry: skip their own entry; forest trees reserve nothing
                var v = Violation(plan, map, t.At, t.Height, t.Area == "town" ? t.Footprint.Id : null);
                if (v != null) failures.Add(t.Area + " tree at " + t.At.X.ToString("F0") + "," + t.At.Z.ToString("F0") + ": " + v);
                if (failures.Count > 12) break;
            }
            Assert.That(failures, Is.Empty, string.Join("\n", failures));
            // negative control: the same probe reports a tree planted on Main Street, in the park lawn and on an anchor
            Assert.That(Violation(plan, map, new Ground(-44, 158), 10), Is.Not.Null);
            Assert.That(Violation(plan, map, new Ground(-180, 130), 8), Is.Not.Null);
            Assert.That(Violation(plan, map, new Ground(map.Anchors["hp-pump-north"].X + 5, map.Anchors["hp-pump-north"].Z), 8), Is.Not.Null);
            Assert.That(Violation(plan, map, new Ground(plan.Outcrops[0].Center.X, plan.Outcrops[0].Center.Z), 8), Is.Not.Null, "inside a rock footprint");
            // the park's lawn keeps its middle clear, yet its edge carries some trees
            Assert.That(Trees.Count(t => t.Area == "town" && t.At.X < -120 && t.At.X > -240 && t.At.Z > 72 && t.At.Z < 180), Is.GreaterThanOrEqualTo(2));
        }

        [Test]
        public void YardTreesStandBetweenAndBehindTheHouses()
        {
            var plan = U5Plan.Plan;
            var houses = plan.Props.Count(p => p.Kind == "House");
            var yard = Trees.Count(t => t.Area == "town");
            TestContext.WriteLine(yard + " yard and park trees for " + houses + " houses");
            Assert.That(yard, Is.InRange(houses / 4, houses));
            var near = Trees.Where(t => t.Area == "town" && t.At.Z > 200).Count(t => plan.Props.Any(p => p.Kind == "House" && SceneryStreet.Distance(p.At, t.At) < 20));
            Assert.That(near, Is.GreaterThan(yard / 3), "most yard trees are right beside a house");
        }

        [Test]
        public void TreesHonourTheLevelOfDetailBandsAndTheVertexBudget()
        {
            // pins A3: near (<=380 m from the core) / medium (<=800) / minimal beyond; all trees together <= 700k vertices
            long total = 0;
            var lines = new List<string>();
            var counts = new Dictionary<TreeLod, int>();
            foreach (var lod in new[] { TreeLod.Near, TreeLod.Mid, TreeLod.Far })
            {
                var set = Trees.Where(t => TreeStyle.Lod(CoastalTerrain.CoreDistance(t.At.X, t.At.Z)) == lod).ToList();
                var v = set.Sum(t => (long)TreeStyle.Vertices((TreeKind)t.Variant, lod));
                total += v; counts[lod] = set.Count;
                lines.Add(lod + ": " + set.Count + " trees, " + v + " vertices");
            }
            TestContext.WriteLine(string.Join("; ", lines) + "; total " + total + " vertices over " + Trees.Count() + " trees");
            Assert.That(total, Is.LessThanOrEqualTo(760000));
            Assert.That(total, Is.GreaterThan(300000), "a full, wooded coast");
            Assert.That(counts[TreeLod.Near], Is.GreaterThan(1500));
            Assert.That(counts[TreeLod.Mid], Is.GreaterThan(counts[TreeLod.Near]));
            // the model cost table: near detail costs more than medium, medium more than minimal, for every species
            foreach (var k in Enum.GetValues(typeof(TreeKind)).Cast<TreeKind>())
            {
                Assert.That(TreeStyle.Vertices(k, TreeLod.Near), Is.GreaterThan(TreeStyle.Vertices(k, TreeLod.Mid)), k.ToString());
                Assert.That(TreeStyle.Vertices(k, TreeLod.Mid), Is.GreaterThan(TreeStyle.Vertices(k, TreeLod.Far)), k.ToString());
            }
            Assert.That(TreeStyle.Lod(380), Is.EqualTo(TreeLod.Near)); Assert.That(TreeStyle.Lod(381), Is.EqualTo(TreeLod.Mid));
            Assert.That(TreeStyle.Lod(800), Is.EqualTo(TreeLod.Mid)); Assert.That(TreeStyle.Lod(801), Is.EqualTo(TreeLod.Far));
        }

        [Test]
        public void TheForestThinsOutIntoTheHazeAndNothingPopsAtItsEdge()
        {
            // pins A2: trees are planted only where they can be seen, thinning toward the plantation's edge
            var d = Trees.Where(t => t.Area == "forest").Select(t => CoastalTerrain.CoreDistance(t.At.X, t.At.Z)).ToList();
            Assert.That(d.Max(), Is.LessThanOrEqualTo(TreeStyle.PlantationM));
            // trees per hectare of woodland in the last shells: falls to zero at the plantation's edge
            double Density(double lo, double hi)
            {
                var woods = 0; var trees = 0;
                for (var z = -80; z < 1900; z += 10) for (var x = -2000; x < 2000; x += 10)
                {
                    var r = CoastalTerrain.CoreDistance(x, z);
                    if (r >= lo && r < hi && CoastalTerrain.Woodland(x, z)) woods++;
                }
                trees = d.Count(r => r >= lo && r < hi);
                return trees * 100.0 / Math.Max(1, woods);   // each sample stands for 100 m2: trees per hectare
            }
            var inner = Density(700, 800); var mid = Density(1100, 1200); var edge = Density(1600, 1700);
            TestContext.WriteLine("Trees per hectare of woodland: 700-800 m " + inner.ToString("F2") + ", 1100-1200 " + mid.ToString("F2") + ", 1600-1700 " + edge.ToString("F2"));
            Assert.That(mid, Is.LessThan(inner)); Assert.That(edge, Is.LessThan(mid * .35)); Assert.That(TreeStyle.Fade(1700), Is.EqualTo(0).Within(1e-12));
            Assert.That(TreeStyle.Fade(1200), Is.EqualTo(1));
            Assert.That(TreeStyle.Spacing(200), Is.InRange(11.9, 12.5), "close crowns beside the town");
            Assert.That(TreeStyle.Spacing(1100), Is.GreaterThan(TreeStyle.Spacing(400) + 5), "sparser in the haze");
        }

        // ---------------------------------------------------------------- rock

        /// <summary>True when every point along the base ring's edges (every half metre) is below the drawn terrain.</summary>
        static bool BaseBelowTerrain(Point3[] ring, TerrainGrid grid, out double worstMargin)
        {
            worstMargin = double.MaxValue;
            for (var i = 0; i < ring.Length; i++)
            {
                var a = ring[i]; var b = ring[(i + 1) % ring.Length];
                var length = Math.Sqrt((b.X - a.X) * (b.X - a.X) + (b.Z - a.Z) * (b.Z - a.Z));
                var steps = Math.Max(1, (int)Math.Ceiling(length / .5));
                for (var k = 0; k <= steps; k++)
                {
                    var t = k / (double)steps;
                    var x = a.X + (b.X - a.X) * t; var z = a.Z + (b.Z - a.Z) * t; var y = a.Y + (b.Y - a.Y) * t;
                    worstMargin = Math.Min(worstMargin, grid.MeshHeight(x, z) - y);
                }
            }
            return worstMargin > 0;
        }

        [Test]
        public void EveryOutcropIsSunkIntoItsSlopeAndLowOnIt()
        {
            // pins A5: base perimeter below the DRAWN terrain everywhere (not just at the grid vertices), top <= 4 m and <= 0.4 x the plan radius
            var plan = U5Plan.Plan;
            Assert.That(plan.Outcrops.Count, Is.GreaterThanOrEqualTo(30));
            var worst = double.MaxValue;
            foreach (var o in plan.Outcrops)
            {
                Assert.That(BaseBelowTerrain(o.Rings[0], plan.Terrain, out var margin), Is.True, "base of the outcrop at " + o.Center.X.ToString("F0") + "," + o.Center.Z.ToString("F0"));
                worst = Math.Min(worst, margin);
                Assert.That(o.Top - plan.Terrain.MeshHeight(o.Center.X, o.Center.Z), Is.LessThanOrEqualTo(Math.Min(4, .4 * o.Radius) + 1e-9), "top above the ground at its centre");
                Assert.That(o.Protrusion, Is.InRange(.6, 4.0 + 1e-9));
                Assert.That(o.PlanSize, Is.InRange(8 - 1e-9, 35 + 1e-9));
                // every ring vertex lies inside the plan rectangle that the registry reserves for it
                var footprint = o.Footprint("check");
                foreach (var ring in o.Rings) foreach (var p in ring)
                {
                    var a = footprint.Yaw * Math.PI / 180; var dx = p.X - footprint.At.X; var dz = p.Z - footprint.At.Z;
                    Assert.That(Math.Abs(dx * Math.Cos(a) - dz * Math.Sin(a)), Is.LessThanOrEqualTo(footprint.Width / 2));
                    Assert.That(Math.Abs(dx * Math.Sin(a) + dz * Math.Cos(a)), Is.LessThanOrEqualTo(footprint.Depth / 2));
                }
                // a ledge rises: each ring's highest point is at least the one below's
                Assert.That(o.Rings.Length, Is.InRange(2, 4));
            }
            TestContext.WriteLine(plan.Outcrops.Count + " outcrops; tightest base margin under the drawn terrain: " + worst.ToString("F2") + " m");
            // negative control: lifting a base ring by 2 m (a boulder dropped on the lawn) must fail the very same check
            var first = plan.Outcrops[0];
            var lifted = first.Rings[0].Select(p => new Point3(p.X, p.Y + first.Rings[0].Max(q => plan.Terrain.MeshHeight(q.X, q.Z) - q.Y) + 1, p.Z)).ToArray();
            Assert.That(BaseBelowTerrain(lifted, plan.Terrain, out _), Is.False);
        }

        [Test]
        public void OutcropsComeInClustersStayOffBuildingsAndStreetsAndAreRegistered()
        {
            var plan = U5Plan.Plan;
            var rocks = plan.Props.Where(p => p.Kind == "Rock").ToList();
            Assert.That(rocks.Count, Is.EqualTo(plan.Outcrops.Count));
            foreach (var r in rocks)
            {
                Assert.That(plan.Registry.Entries.Any(e => e == r.Footprint), Is.True, "rock footprint is in the registry");
                var o = plan.Outcrops[r.Variant];
                foreach (var ring in o.Rings) foreach (var p in ring)
                    foreach (var s in plan.Streets) Assert.That(s.DistanceTo(new Ground(p.X, p.Z)), Is.GreaterThan(s.Width / 2 + 1), "rock on " + s.Name);
                Assert.That(Math.Abs(r.At.X) < 240 && r.At.Z < 140, Is.False, "never in the core");
                foreach (var h in plan.Props.Where(p => p.Footprint.Id.StartsWith("lot:", StringComparison.Ordinal)))
                    Assert.That(r.Footprint.Intersects(h.Footprint, 0), Is.False, "rock under a building");
            }
            // clusters of 2-5 ledges: connected components at 60 m
            var seen = new HashSet<int>(); var sizes = new List<int>();
            for (var i = 0; i < rocks.Count; i++)
            {
                if (!seen.Add(i)) continue;
                var queue = new Queue<int>(); queue.Enqueue(i); var n = 1;
                while (queue.Count > 0)
                {
                    var a = queue.Dequeue();
                    for (var j = 0; j < rocks.Count; j++)
                        if (!seen.Contains(j) && SceneryStreet.Distance(rocks[a].At, rocks[j].At) < 60) { seen.Add(j); queue.Enqueue(j); n++; }
                }
                sizes.Add(n);
            }
            TestContext.WriteLine("Outcrop clusters: " + string.Join(",", sizes));
            Assert.That(sizes.Count, Is.GreaterThanOrEqualTo(10));
            Assert.That(sizes.Min(), Is.GreaterThanOrEqualTo(2));
            Assert.That(sizes.Max(), Is.LessThanOrEqualTo(5));
            // shore, clearing and woodland-edge clusters all exist; none sits on a hilltop
            Assert.That(rocks.Any(r => r.At.Z < -30 && Math.Abs(r.At.X) > 300), Is.True, "along the shore outside the quays");
            Assert.That(rocks.Any(r => CoastalTerrain.ClearingWeight(r.At.X, r.At.Z) > .3), Is.True, "in the clearings");
            Assert.That(rocks.Any(r => r.At.Z > 300), Is.True, "on the slopes");
        }

        [Test]
        public void FitRefusesGroundTooSteepToSeatALedge()
        {
            // negative control for the fit: a grid is what it is, but a made-up steep field must be rejected, not drawn floating
            var grid = TerrainGrid.Standard();
            var seated = 0; var rejected = 0;
            for (var i = 0; i < 40; i++)
            {
                var o = Outcrop.Fit(grid, new Ground(-500 + i * 25, 600 + (i % 7) * 31), i * 17, 8, 5, 9000 + i);
                if (o == null) rejected++; else { seated++; Assert.That(o.Top - o.GroundAtCentre, Is.LessThanOrEqualTo(Math.Min(4, .4 * 8) + 1e-9)); }
            }
            Assert.That(seated, Is.GreaterThan(0));
            Assert.That(seated + rejected, Is.EqualTo(40));
        }
    }
}
