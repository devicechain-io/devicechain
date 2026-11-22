// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Linq;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>
    /// U7: the break of light under the storm's deck about sunset, a dawn and a morning that are neither dark nor blue, the wood on the
    /// backdrop's crests, loose blocks and tors on the granite, glass that reads as glass, and the two HOME frames.
    /// </summary>
    public class U7LightTests
    {
        const double Deck = SitePlan.DeckHeightM;
        const double From = 16, To = 22.5;
        const double Wide = 16.0 / 9, Tall = 9.0 / 16;
        static SkyState At(double hour) => DayLighting.Evaluate(hour, Weather.Window(From, To, hour));
        static SkyState Clear(double hour) => DayLighting.Evaluate(hour, new Weather(0, 0));
        static double Luma(Rgb c) => 0.2126 * c.R + 0.7152 * c.G + 0.0722 * c.B;

        // ---------------------------------------------------------------- the storm's dusk

        [Test]
        public void TheBreakUnderTheDeckOpensOnlyAtAStormDusk()
        {
            // no deck, no break: a fair day has none at any minute
            for (var h = 0.0; h < 24; h += 1 / 60.0) Assert.That(Clear(h).HorizonBreak, Is.EqualTo(0), "fair weather at " + h);
            // the scenario's storm: nothing while the sun is high, open through the last hour of the day, gone with the afterglow
            Assert.That(At(16.5).HorizonBreak, Is.EqualTo(0), "the sun is 23 degrees up");
            foreach (var h in new[] { 17.75, 18.0, 18.3 }) Assert.That(At(h).HorizonBreak, Is.GreaterThan(.85), "open at " + h);
            Assert.That(At(18.75).HorizonBreak, Is.LessThan(.02), "the sun is seven degrees under");
            Assert.That(At(20).HorizonBreak, Is.EqualTo(0));
            // and it never jumps
            var last = At(15).HorizonBreak;
            for (var h = 15.0; h < 20; h += 1 / 60.0)
            {
                var now = At(h).HorizonBreak;
                Assert.That(Math.Abs(now - last), Is.LessThan(.08), "a step at " + h);
                last = now;
            }
        }

        [Test]
        public void TheSunReachesTheTownThroughTheBreakOnceItIsLowEnough()
        {
            // a sun ten degrees up is still behind the deck: the storm takes more than half of it, as it always did
            Assert.That(At(17.5).SunIntensity, Is.LessThan(Clear(17.5).SunIntensity * .5));
            // in the band it is a real key again: several times what the closed deck left, and still under the clear evening's
            var storm = At(18.0); var clear = Clear(18.0);
            Assert.That(storm.SunIntensity, Is.GreaterThan(.35));
            Assert.That(storm.SunIntensity, Is.GreaterThan(2.2 * clear.SunIntensity * (1 - .72)), "more than twice what a closed deck leaves");
            Assert.That(storm.SunIntensity, Is.LessThan(clear.SunIntensity));
            // and warm: red over green over blue
            Assert.That(storm.SunColor.R, Is.GreaterThan(storm.SunColor.G * 1.3)); Assert.That(storm.SunColor.G, Is.GreaterThan(storm.SunColor.B * 1.5));
            // the break itself is gold at the horizon while the sun is up and ember after it
            Assert.That(At(17.9).BreakColor.G, Is.GreaterThan(At(18.5).BreakColor.G + .1));
            Assert.That(At(18.0).BreakColor.R, Is.GreaterThan(At(18.0).BreakColor.B * 2));
        }

        [Test]
        public void TheBreakReachesIntoHomesFrameFromTheWest()
        {
            // HOME looks north and the sun sets in the west, 90 degrees round: a break that kept to the sun's own bearing would never be seen
            Assert.That(SkyState.BreakReach(1), Is.EqualTo(1));
            Assert.That(SkyState.BreakReach(SkyState.BreakReachCos), Is.EqualTo(0));
            var s = At(18.0);
            var pose = U5Plan.Map.Pose("pose:home");
            var half = Math.Atan(CameraMath.TanHalf(pose.VerticalFov) * Wide) * 180 / Math.PI;
            double Reach(double bearingDeg) => SkyState.BreakReach(Math.Cos((bearingDeg - s.SunAzimuthDeg) * Math.PI / 180));
            Assert.That(half, Is.InRange(27, 31), "HOME sees about 29 degrees either side of north");
            Assert.That(Reach(-half), Is.GreaterThan(.55), "tall at the frame's western edge");
            Assert.That(Reach(0), Is.InRange(.2, .4), "lower in the middle");
            Assert.That(Reach(half), Is.EqualTo(0), "gone before the eastern edge");
            // and in the 9:16 HOME, which looks east with its back to the sunset, there is no break at all: the town is lit from behind the camera
            Assert.That(Reach(90), Is.EqualTo(0));
        }

        // ---------------------------------------------------------------- exposure and white balance

        [Test]
        public void DawnIsNotDarkAndNoHourTurnsTheFlatGroundBlue()
        {
            // 06:24: the sun is two degrees up and a weak, grazing key. The flat ground is lit by the floor, lifted and near neutral
            var dawn = Clear(6.4);
            Assert.That(Luma(dawn.AmbientSky), Is.GreaterThan(.38), "U6 left 0.19 here");
            Assert.That(dawn.AmbientSky.B / dawn.AmbientSky.R, Is.LessThan(1.15));
            // 07:06 and noon: the ground is lit by the whole sky, not by the zenith's blue (U6: blue 2.8 times red)
            foreach (var h in new[] { 7.1, 9.0, 12.5, 15.0 })
            {
                var s = Clear(h);
                Assert.That(s.AmbientSky.B / s.AmbientSky.R, Is.LessThan(1.75), "sky ambient at " + h);
                Assert.That(s.AmbientSky.B, Is.GreaterThan(s.AmbientSky.R), "still a sky's light, not grey, at " + h);
            }
            // the storm's dusk, when the pump runs: no violet in the light on the ground or the walls (U6: blue 1.6 times red at 18:18)
            foreach (var h in new[] { 17.5, 17.75, 18.0, 18.3 })
            {
                var s = At(h);
                Assert.That(s.AmbientSky.B / s.AmbientSky.R, Is.LessThan(1.12), "sky ambient at " + h);
                Assert.That(s.AmbientEquator.B / s.AmbientEquator.G, Is.LessThan(1.1), "equator ambient at " + h);
            }
            // the night keeps its blue: the floor only turns neutral once the sky is light
            Assert.That(Clear(2).AmbientSky.B / Clear(2).AmbientSky.R, Is.GreaterThan(1.5));
            // daylight luminance is what it was: draining a colour keeps its luma
            Assert.That(Luma(Clear(12.5).AmbientSky), Is.InRange(.55, .62));
        }

        [Test]
        public void TheAmbientNeverJumpsThroughDawnAndDusk()
        {
            foreach (var storm in new[] { false, true })
            {
                var last = storm ? At(4) : Clear(4);
                for (var h = 4.0; h < 21; h += 1 / 60.0)
                {
                    var s = storm ? At(h) : Clear(h);
                    Assert.That(Math.Abs(Luma(s.AmbientSky) - Luma(last.AmbientSky)), Is.LessThan(.07), "sky ambient step at " + h + (storm ? " (storm)" : ""));
                    Assert.That(Math.Abs(s.SunIntensity - last.SunIntensity), Is.LessThan(.06), "sun step at " + h + (storm ? " (storm)" : ""));
                    Assert.That(Math.Abs(s.ExposureLift - last.ExposureLift), Is.LessThan(.06), "exposure step at " + h + (storm ? " (storm)" : ""));
                    last = s;
                }
            }
        }

        // ---------------------------------------------------------------- the HOME frames

        [Test]
        public void TheWideHomeLooksALittleHigherAndThePortraitHomeHoldsTheHarbourInItsMiddle()
        {
            var home = U5Plan.Map.Pose("pose:home");
            Assert.That(home.Camera.X, Is.EqualTo(0)); Assert.That(home.Camera.Y, Is.EqualTo(100)); Assert.That(home.Camera.Z, Is.EqualTo(-420));
            Assert.That(home.Look.Y, Is.EqualTo(PresentationMap.HomeLookY));
            Assert.That(CameraMath.HorizonFromTop(home), Is.InRange(.185, .195), "U6: 17.6 %");
            var tall = U5Plan.Map.Pose("pose:home:portrait");
            double top = 1, bottom = 0;
            foreach (var d in U5Plan.Layout.Devices)
            {
                var a = U5Plan.Map.Anchors[d.Token];
                Assert.That(CameraMath.Project(tall, Tall, a.X, Deck + 1, a.Z, out var nx, out var ny), Is.True, d.Token);
                Assert.That(Math.Abs(nx), Is.LessThanOrEqualTo(.9), d.Token + " x");
                top = Math.Min(top, (1 - ny) / 2); bottom = Math.Max(bottom, (1 - ny) / 2);
            }
            TestContext.WriteLine("9:16 HOME: devices from " + top.ToString("P1") + " to " + bottom.ToString("P1") + " down the frame");
            // U6 held them between 49 % and 83 %: the harbour sat in the lower half
            Assert.That(top, Is.InRange(.40, .50)); Assert.That(bottom, Is.InRange(.60, .70));
            Assert.That((top + bottom) / 2, Is.InRange(.50, .58), "the harbour's middle is the frame's");
            // from further back and lower, with a longer lens: the farthest device is drawn no smaller than U6 drew it
            double Far(CameraPose p) => U5Plan.Layout.Devices.Max(d => { var a = U5Plan.Map.Anchors[d.Token]; return Math.Sqrt(Math.Pow(a.X - p.Camera.X, 2) + Math.Pow(Deck - p.Camera.Y, 2) + Math.Pow(a.Z - p.Camera.Z, 2)); });
            var u6 = new CameraPose(new Point3(-520, 260, 30), new Point3(0, 67.1, 10), 52);
            Assert.That(1 / (Far(tall) * CameraMath.TanHalf(tall.VerticalFov)), Is.GreaterThanOrEqualTo(.99 / (Far(u6) * CameraMath.TanHalf(u6.VerticalFov))));
        }

        // ---------------------------------------------------------------- the wood on the backdrop

        [Test]
        public void EveryRangeCarriesATreeLineThatStandsOnItAndKeepsTheForestsMix()
        {
            var all = 0;
            foreach (var layer in CoastalTerrain.HillLayers)
            {
                var trees = layer.CrestTrees().ToList();
                all += trees.Count;
                Assert.That(trees.Count, Is.GreaterThan(150), layer.Name);
                Assert.That(trees.Count(t => t.Conifer) / (double)trees.Count, Is.InRange(.54, .66), layer.Name + ": six conifers to four broadleaves");
                var crestBack = layer.BackAtFraction(1);
                Assert.That(crestBack, Is.EqualTo(layer.CrestZ));
                foreach (var t in trees)
                {
                    layer.World(t.Along, t.Back, out var x, out var z);
                    var ground = layer.Height(x, z);
                    Assert.That(layer.Crest(t.Along), Is.GreaterThanOrEqualTo(8), layer.Name + ": no tree where the layer has no hill");
                    Assert.That(ground - t.Foot, Is.EqualTo(CoastalTerrain.HillLayer.CrestTreeSinkM).Within(1e-9), layer.Name + ": the foot is sunk into the layer");
                    Assert.That(t.Height, Is.InRange(8, CoastalTerrain.HillLayer.CrestTreeMaxM));
                    Assert.That(t.Back, Is.LessThanOrEqualTo(crestBack + 1e-9), layer.Name + ": on the crest or the face in front of it, never behind");
                    Assert.That(Math.Abs(t.Along - layer.AlongCentre), Is.LessThan(layer.HalfWidth));
                }
                // the crest line is broken: tops, and gaps where the wood opens
                var crest = trees.Where(t => Math.Abs(t.Back - crestBack) < 1e-9).OrderBy(t => t.Along).ToList();
                Assert.That(crest.Count, Is.GreaterThan(60), layer.Name);
                Assert.That(crest.Zip(crest.Skip(1), (a, b) => b.Along - a.Along).Count(g => g > 40), Is.GreaterThan(1), layer.Name + ": gaps in the line");
                // a tree at two kilometres is a few pixels: HOME draws the foothills' at 8 px or more (1080 rows, 35 degrees)
                if (layer == CoastalTerrain.Foothills)
                    Assert.That(crest.Average(t => t.Height) / (layer.CrestZ + 420) * 540 / CameraMath.TanHalf(35), Is.GreaterThan(8));
            }
            TestContext.WriteLine(all + " crest trees on " + CoastalTerrain.HillLayers.Length + " layers");
            // one triangle a conifer, three a broadleaf: about five vertices a tree
            Assert.That(all, Is.InRange(3000, 6500));
        }

        [Test]
        public void TheAutumnColourComesInDriftsAlongARange()
        {
            // neighbours on the crest mostly share a drift value; a per-tree hash would not
            foreach (var layer in CoastalTerrain.Backdrop)
            {
                var crest = layer.CrestTrees().Where(t => Math.Abs(t.Back - layer.CrestZ) < 1e-9).OrderBy(t => t.Along).ToList();
                var near = crest.Zip(crest.Skip(1), (a, b) => Math.Abs(a.Drift - b.Drift)).Average();
                var spread = crest.Max(t => t.Drift) - crest.Min(t => t.Drift);
                Assert.That(near, Is.LessThan(.05), layer.Name);
                Assert.That(spread, Is.GreaterThan(.5), layer.Name + ": and the whole range runs through the colours");
            }
        }

        [Test]
        public void TheTreeLineStaysLowFromTheQuaysAndUnderHomesSkyBudget()
        {
            // the ground of every crest is under 3 degrees from a quay (U6); its trees add under half a degree
            foreach (var layer in CoastalTerrain.HillLayers)
                foreach (var t in layer.CrestTrees())
                {
                    layer.World(t.Along, t.Back, out var x, out var z);
                    var d = Math.Sqrt(Math.Pow(Math.Max(0, Math.Abs(x) - 140), 2) + Math.Pow(Math.Max(0, z - 100), 2));
                    Assert.That(Math.Atan2(t.Top - 7, d) * 180 / Math.PI, Is.LessThan(3.5), layer.Name + " at " + t.Along);
                }
            // HOME's sky above the ranges, with and without their tree line: the highest crest point or tree top in each of 960 columns. It is
            // a silhouette estimate (no near ground, no planted trees, a tree counted in one column only); the mask is the measurement, and
            // it reads 20.1 % with the trees (U5: 19.6 %, U6: 19.1 %).
            double Sky(CameraPose pose, bool trees)
            {
                const int columns = 960;
                var topY = new double[columns];
                var horizon = Math.Max(-1, Math.Min(1, 1 - 2 * CameraMath.HorizonFromTop(pose)));
                for (var i = 0; i < columns; i++) topY[i] = horizon;
                void P(double x, double y, double z)
                {
                    if (!CameraMath.Project(pose, Wide, x, y, z, out var nx, out var ny) || nx <= -1 || nx >= 1) return;
                    var c = (int)((nx + 1) * .5 * columns);
                    if (ny > topY[c]) topY[c] = ny;
                }
                foreach (var layer in CoastalTerrain.HillLayers)
                {
                    for (var along = layer.AlongCentre - layer.HalfWidth; along <= layer.AlongCentre + layer.HalfWidth; along += 2)
                    {
                        layer.World(along, layer.CrestZ, out var x, out var z);
                        P(x, layer.Height(x, z), z);
                    }
                    if (!trees) continue;
                    foreach (var t in layer.CrestTrees())
                    {
                        layer.World(t.Along, t.Back, out var x, out var z);
                        P(x, t.Top, z);
                    }
                }
                return topY.Select(y => Math.Max(0, Math.Min(1, (1 - y) / 2))).Average();
            }
            var home = U5Plan.Map.Pose("pose:home");
            var u6 = new CameraPose(home.Camera, new Point3(0, 4, 50), 35);
            double bare = Sky(home, false), wooded = Sky(home, true), before = Sky(u6, false);
            TestContext.WriteLine("HOME 16:9 sky above the ranges: " + before.ToString("P2") + " at U6's look, " + bare.ToString("P2") + " at U7's, " + wooded.ToString("P2") + " with the tree tops");
            Assert.That(bare - wooded, Is.LessThan(.008), "the tree line costs well under a point of sky");
            Assert.That(wooded, Is.GreaterThan(before + .004), "and the raised look gives back more than the tree line and U6's ranges took");
        }

        // ---------------------------------------------------------------- granite: blocks and tors

        static bool Embedded(TerrainGrid grid, Point3[] block)
        {
            for (var i = 0; i < 4; i++)
                if (block[i].Y > grid.MeshHeight(block[i].X, block[i].Z) - Outcrop.BlockSinkM + 1e-9) return false;
            return true;
        }

        [Test]
        public void EveryBlockComesUpThroughItsLedgeAndStaysInsideItsFootprint()
        {
            var plan = U5Plan.Plan; var blocks = 0; var tors = 0; var withBlocks = 0;
            foreach (var o in plan.Outcrops)
            {
                if (o.Radius >= 5) Assert.That(o.Blocks.Length, Is.GreaterThanOrEqualTo(2), "a ledge of " + o.PlanSize.ToString("F0") + " m carries loose rock");
                if (o.Blocks.Length > 0) withBlocks++;
                var a = o.YawDeg * Math.PI / 180;
                foreach (var b in o.Blocks)
                {
                    blocks++;
                    Assert.That(b.Length, Is.EqualTo(8));
                    // (a slab of the landmark tor's stacks starts inside the slab under it instead: U8Tests)
                    if (!o.Landmark) Assert.That(Embedded(plan.Terrain, b), Is.True, "a block's bottom is under the drawn ground at every corner");
                    for (var i = 0; i < 8; i++)
                    {
                        // inside the ledge's own plan rectangle (the registry reserves that plus a metre and a half)
                        double dx = b[i].X - o.Center.X, dz = b[i].Z - o.Center.Z;
                        double lx = dx * Math.Cos(a) - dz * Math.Sin(a), lz = dx * Math.Sin(a) + dz * Math.Cos(a);
                        Assert.That(Math.Abs(lx), Is.LessThanOrEqualTo(o.HalfX + 1e-6), "block corner x");
                        Assert.That(Math.Abs(lz), Is.LessThanOrEqualTo(o.HalfZ + 1e-6), "block corner z");
                    }
                    // a top above its own bottom by at least half a metre, and its top face smaller than its base
                    Assert.That(Enumerable.Range(4, 4).Min(i => b[i].Y) - b[0].Y, Is.GreaterThan(.5));
                }
                Assert.That(o.Peak, Is.GreaterThanOrEqualTo(o.Top));
                Assert.That(o.BlockFoot.Length, Is.EqualTo(o.Blocks.Length));
                if (o.Landmark) continue; // the one tor that is meant to stand high: U8LandmarkTests
                Assert.That(o.Peak - o.Top, Is.LessThanOrEqualTo(Math.Max(3.2, Math.Min(Outcrop.TorMaxRiseM, Outcrop.TorRadiusShare * o.Radius)) + 1e-6), "no block stands higher over its ledge than a tor may");
                if (o.Radius >= Outcrop.TorMinRadiusM && o.Peak - o.Top > 2.5) tors++;
                else if (o.Radius < Outcrop.TorMinRadiusM) Assert.That(o.Peak - o.Top, Is.LessThanOrEqualTo(3.2 + 1e-6), "only the larger ledges carry a tor");
            }
            TestContext.WriteLine(blocks + " blocks on " + withBlocks + " of " + plan.Outcrops.Count + " outcrops, " + tors + " tors");
            Assert.That(tors, Is.InRange(4, 30), "a few larger tors");
            // one triangle pair a face, eight faces drawn a block (four walls, their lit upper thirds, the top): the terrain tiles gain under 12 k vertices
            Assert.That(blocks * 36, Is.LessThan(12000));
            // the same plan gives the same blocks
            var again = WorldPropPlan.Generate(U5Plan.Map, U5Plan.Layout);
            Assert.That(again.Outcrops.Sum(o => o.Blocks.Sum(b => b.Sum(p => p.X + p.Y + p.Z))), Is.EqualTo(plan.Outcrops.Sum(o => o.Blocks.Sum(b => b.Sum(p => p.X + p.Y + p.Z)))));
        }

        [Test]
        public void ALiftedBlockFailsTheEmbedCheck()
        {
            var plan = U5Plan.Plan;
            var b = plan.Outcrops.First(o => o.Blocks.Length > 0).Blocks[0];
            Assert.That(Embedded(plan.Terrain, b), Is.True);
            var lifted = b.Select(p => new Point3(p.X, p.Y + 1.2, p.Z)).ToArray();
            Assert.That(Embedded(plan.Terrain, lifted), Is.False, "a stone set down on the ground is not embedded");
        }

        [Test]
        public void TorsStandWhereHomeLooks()
        {
            var plan = U5Plan.Plan; var pose = U5Plan.Map.Pose("pose:home");
            var seen = plan.Outcrops.Count(o => o.Peak - o.Top > 2.5 && CameraMath.Project(pose, Wide, o.Center.X, o.Peak, o.Center.Z, out var nx, out var ny) && Math.Abs(nx) <= .95 && Math.Abs(ny) <= .95);
            Assert.That(seen, Is.GreaterThanOrEqualTo(3), "tors inside the 16:9 HOME frame");
        }

        // ---------------------------------------------------------------- the wet well's view and its tags

        static Ground Well(string token) => U5Plan.Plan.Registry.Entries.First(f => f.Id == "device:" + token + ":well").At;

        [Test]
        public void APumpsWellPoseLooksIntoTheWellNotAtThePumpHouse()
        {
            foreach (var f in StoryPoses.Families.Where(f => f.Token.StartsWith("hp-pump-", StringComparison.Ordinal)))
            {
                var well = Well(f.Token); var anchor = U5Plan.Map.Anchors[f.Token];
                Assert.That(Math.Sqrt(Math.Pow(well.X - anchor.X, 2) + Math.Pow(well.Z - anchor.Z, 2)), Is.GreaterThan(12), "the well stands off from the pump house");
                foreach (var s in StoryPoses.WellSuffixes)
                {
                    var p = U5Plan.Map.Pose("pose:" + f.Family + ":" + s);
                    Assert.That(p.Look.X, Is.EqualTo(well.X).Within(1e-9), f.Family); Assert.That(p.Look.Z, Is.EqualTo(well.Z).Within(1e-9), f.Family);
                    // the well's middle is the frame's, and its level tags are drawn
                    Assert.That(CameraMath.Project(p, Wide, well.X, Deck, well.Z, out var nx, out var ny), Is.True);
                    // (U8: the look is under the deck, so the well stands a little above the middle and its tags clear the caption)
                    Assert.That(Math.Abs(nx), Is.LessThan(.05)); Assert.That(ny, Is.InRange(.1, .38));
                    Assert.That(InView(p, well), Is.True, f.Family + ":" + s + " draws the tags");
                    // U6 aimed it at the anchor: from there the well is outside the frame
                    var side = U5Plan.Map.Pose("pose:" + f.Family + ":close").Camera;
                    var u6 = new CameraPose(new Point3(anchor.X + Math.Sign(side.X - anchor.X) * (Math.Abs(side.X - anchor.X) > 5 ? 16 : 0), 10, anchor.Z + Math.Sign(side.Z - anchor.Z) * (Math.Abs(side.Z - anchor.Z) > 5 ? 16 : 0)), new Point3(anchor.X, 2, anchor.Z), 42);
                    Assert.That(CameraMath.Project(u6, Wide, well.X, Deck, well.Z, out var ox, out _) && Math.Abs(ox) < 1, Is.False, f.Family + ": the old aim did not hold the well");
                }
            }
        }

        static bool InView(CameraPose p, Ground well)
        {
            double fx = p.Look.X - p.Camera.X, fy = p.Look.Y - p.Camera.Y, fz = p.Look.Z - p.Camera.Z; var n = Math.Sqrt(fx * fx + fy * fy + fz * fz);
            return WellFootprint.TagsInView(well.X - p.Camera.X, Deck - p.Camera.Y, well.Z - p.Camera.Z, fx / n, fy / n, fz / n);
        }

        [Test]
        public void AWellsTagsAreNotDrawnForACameraThatOnlyStandsBesideIt()
        {
            // the lamp, gauge and charger views near a well never draw its tags (U5 and U6 drew them through the quay at the frame's edge)
            var wells = StoryPoses.Families.Where(f => f.Token.StartsWith("hp-pump-", StringComparison.Ordinal)).Select(f => Well(f.Token)).ToList();
            var checkedPoses = 0;
            foreach (var f in StoryPoses.Families.Where(f => !f.Token.StartsWith("hp-pump-", StringComparison.Ordinal)))
                foreach (var s in StoryPoses.GroundSuffixes)
                {
                    var p = U5Plan.Map.Pose("pose:" + f.Family + ":" + s); checkedPoses++;
                    foreach (var w in wells) Assert.That(InView(p, w), Is.False, f.Family + ":" + s);
                }
            Assert.That(checkedPoses, Is.GreaterThan(40));
            foreach (var h in StoryPoses.Home) foreach (var w in wells) Assert.That(InView(U5Plan.Map.Pose(h), w), Is.False, h);
            // the rule itself: in range and on the axis, yes; behind, to the side or far off, no
            Assert.That(WellFootprint.TagsInView(0, -6, 10, 0, -.5, .866), Is.True);
            Assert.That(WellFootprint.TagsInView(0, -6, -10, 0, -.5, .866), Is.False, "behind the camera");
            Assert.That(WellFootprint.TagsInView(8, -6, 10, 0, -.5, .866), Is.False, "at the frame's edge");
            Assert.That(WellFootprint.TagsInView(0, -15, 26, 0, -.5, .866), Is.False, "too far to read");
        }

        // ---------------------------------------------------------------- glass

        [Test]
        public void VehicleGlassCarriesTheSkyAndAGlint()
        {
            foreach (var plan in new[] { NearVehicles.Truck(), NearVehicles.Car(CarSpec.Parked), NearVehicles.Car(CarSpec.Charging) })
            {
                var panes = plan.Parts.Where(p => p.Shape == NearShape.Quad && p.Tone == NearTone.Glass).ToList();
                var skies = plan.Parts.Where(p => p.Tone == NearTone.GlassSky).ToList();
                var glints = plan.Parts.Where(p => p.Tone == NearTone.GlassGlint).ToList();
                Assert.That(panes.Count, Is.EqualTo(4), plan.Name);
                Assert.That(skies.Count, Is.EqualTo(4), plan.Name + ": the sky in every pane");
                Assert.That(glints.Count, Is.EqualTo(4), plan.Name);
                for (var i = 0; i < 4; i++)
                {
                    // each sky quad lies in the upper part of its pane and inside its outline
                    var pane = new[] { panes[i].A, panes[i].B, panes[i].C, panes[i].D }; var sky = new[] { skies[i].A, skies[i].B, skies[i].C, skies[i].D };
                    Assert.That(sky.Min(c => c.Y), Is.GreaterThan(pane.Min(c => c.Y) + .15f * (pane.Max(c => c.Y) - pane.Min(c => c.Y))), plan.Name);
                    Assert.That(sky.Max(c => c.Y), Is.LessThanOrEqualTo(pane.Max(c => c.Y) + .01f), plan.Name);
                    foreach (var q in new[] { sky, new[] { glints[i].A, glints[i].B, glints[i].C, glints[i].D } })
                    {
                        Assert.That(q.Min(c => c.X), Is.GreaterThanOrEqualTo(pane.Min(c => c.X) - .02f)); Assert.That(q.Max(c => c.X), Is.LessThanOrEqualTo(pane.Max(c => c.X) + .02f));
                        Assert.That(q.Min(c => c.Z), Is.GreaterThanOrEqualTo(pane.Min(c => c.Z) - .02f)); Assert.That(q.Max(c => c.Z), Is.LessThanOrEqualTo(pane.Max(c => c.Z) + .02f));
                    }
                }
            }
        }
    }
}
