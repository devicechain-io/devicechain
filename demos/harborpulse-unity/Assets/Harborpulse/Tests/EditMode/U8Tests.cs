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
    /// U8: the landmark tor, the Basin's second charger seen from the 9:16 HOME, the wet wells' view (no bay in front of a well, the level
    /// tags above the caption), and a morning that is not blue.
    /// </summary>
    public class U8Tests
    {
        const double Deck = SitePlan.DeckHeightM;
        const double Wide = 16.0 / 9, Tall = 9.0 / 16;
        const double From = 16, To = 22.5;
        static SkyState Clear(double hour) => DayLighting.Evaluate(hour, Weather.Window(From, To, hour));

        // ---------------------------------------------------------------- the landmark tor

        [Test]
        public void OneTorIsALandmarkInTheMiddleOfWhatHomeSees()
        {
            var plan = U5Plan.Plan;
            var tor = plan.EastTor;
            // taller than anything else of rock by a wide margin, and than the terminal's ridge and the lofts
            var rise = tor.Peak - tor.GroundAtCentre;
            Assert.That(rise, Is.InRange(22, 32));
            Assert.That(plan.Outcrops.Where(o => !o.Landmark).Max(o => o.Peak - o.GroundAtCentre), Is.LessThan(rise / 2), "every other tor is a few metres of rock");
            // inside the 16:9 HOME frame, clear of its edges, and at least 50 pixels tall at 1080 lines
            var pose = U5Plan.Map.Pose("pose:home");
            Assert.That(CameraMath.Project(pose, Wide, tor.Center.X, tor.Peak, tor.Center.Z, out var nx, out var top), Is.True);
            Assert.That(CameraMath.Project(pose, Wide, tor.Center.X, tor.GroundAtCentre, tor.Center.Z, out _, out var foot), Is.True);
            Assert.That(Math.Abs(nx), Is.LessThan(.8)); Assert.That(top, Is.InRange(-.8, .8));
            var pixels = (top - foot) * 540;
            Assert.That(pixels, Is.GreaterThan(50), "U7's tallest tor was about 15 pixels");
            TestContext.WriteLine("landmark tor at " + tor.Center.X.ToString("F0") + ", " + tor.Center.Z.ToString("F0") + ": " + rise.ToString("F1") + " m, " + pixels.ToString("F0") + " px at 1080, " + tor.Blocks.Length + " blocks");
            // it stands in the meadow it was given, on open ground: no tree's footprint within its ledge, and the wood begins behind it
            var m = CoastalTerrain.Meadow(WorldPropPlan.LandmarkMeadow);
            Assert.That(Math.Sqrt(Math.Pow(tor.Center.X - m.X, 2) + Math.Pow(tor.Center.Z - m.Z, 2)), Is.LessThan(m.R));
            foreach (var p in plan.Props.Where(p => p.Kind == "Tree"))
                Assert.That(Math.Sqrt(Math.Pow(p.At.X - tor.Center.X, 2) + Math.Pow(p.At.Z - tor.Center.Z, 2)), Is.GreaterThan(Outcrop.LandmarkHalfZ), "a tree in the tor");
        }

        [Test]
        public void TheLandmarkIsStacksOfSlabsEachStandingOnTheOneUnderIt()
        {
            var tor = U5Plan.Plan.EastTor;
            // the main stack: six slabs, each one's foot the top of the one under it, each narrower
            var tops = tor.Blocks.Select(b => Enumerable.Range(4, 4).Max(i => b[i].Y)).ToArray();
            var grid = U5Plan.Plan.Terrain;
            bool Grounded(Point3[] b) => Enumerable.Range(0, 4).All(i => b[i].Y <= grid.MeshHeight(b[i].X, b[i].Z) - Outcrop.BlockSinkM + 1e-9);
            double Span(Point3[] b) => Enumerable.Range(0, 4).Max(i => Math.Sqrt(Math.Pow(b[i].X - b[(i + 2) % 4].X, 2) + Math.Pow(b[i].Z - b[(i + 2) % 4].Z, 2)));
            Assert.That(Grounded(tor.Blocks[0]), Is.True, "the lowest slab comes up through the ground");
            for (var n = 1; n < 6; n++)
            {
                var b = tor.Blocks[n]; var under = tor.Blocks[n - 1];
                Assert.That(tor.BlockFoot[n], Is.GreaterThan(tor.BlockFoot[n - 1] + 1.5), "slab " + n + " stands higher");
                Assert.That(tops[n], Is.GreaterThan(tor.BlockFoot[n] + 1.5), "slab " + n + " shows at least a metre and a half of wall");
                // it starts inside the slab under it: below that slab's lowest top corner, and well above the ground (it is no pillar)
                Assert.That(b[0].Y, Is.LessThan(Enumerable.Range(4, 4).Min(i => under[i].Y) - .2), "slab " + n + " is sunk into the one under it");
                Assert.That(Grounded(b), Is.False, "slab " + n + " does not run down to the ground through the slabs under it");
                Assert.That(Span(b), Is.LessThan(Span(under)), "slab " + n + " is narrower");
                // and over it: its middle within the lower slab's top, its foot no wider than that slab's foot
                double cx = b.Take(4).Average(p => p.X), cz = b.Take(4).Average(p => p.Z), ux = under.Skip(4).Average(p => p.X), uz = under.Skip(4).Average(p => p.Z);
                Assert.That(Math.Sqrt((cx - ux) * (cx - ux) + (cz - uz) * (cz - uz)), Is.LessThan(Span(under) * .2), "slab " + n + " is on the one under it");
            }
            // every other block of the tor is grounded, or the upper slab of a lesser stack sunk into the block before it
            for (var n = 6; n < tor.Blocks.Length; n++)
                Assert.That(Grounded(tor.Blocks[n]) || tor.Blocks[n][0].Y < Enumerable.Range(4, 4).Min(i => tor.Blocks[n - 1][i].Y) - .2, Is.True, "block " + n);
            Assert.That(tops.Take(6).Max(), Is.EqualTo(tor.Peak).Within(1e-9), "the main stack is the summit");
            // the lesser stacks are lower, so the outline steps down either side
            Assert.That(tops.Skip(6).Max(), Is.LessThan(tor.Peak * .75 + tor.Top * .25));
            // the same seed, the same tor
            var again = WorldPropPlan.Generate(U5Plan.Map, U5Plan.Layout).EastTor;
            Assert.That(again.Blocks.Sum(b => b.Sum(p => p.X + p.Y + p.Z)), Is.EqualTo(tor.Blocks.Sum(b => b.Sum(p => p.X + p.Y + p.Z))));
        }

        // ---------------------------------------------------------------- the terminal and the Basin's second charger

        /// <summary>The height a sight line has to clear at (x, z): the terminal's ridge inside its hall, the deck elsewhere.</summary>
        static double TerminalRoof(PropFootprint hall, double x, double z)
        {
            var a = hall.Yaw * Math.PI / 180; double dx = x - hall.At.X, dz = z - hall.At.Z;
            double lx = dx * Math.Cos(a) - dz * Math.Sin(a), lz = dx * Math.Sin(a) + dz * Math.Cos(a);
            if (Math.Abs(lx) > hall.Width / 2 || Math.Abs(lz) > hall.Depth / 2) return double.MinValue;
            // eaves at the wall's top, the ridge 5 m over them along the hall's middle
            return Deck + NearLandmarks.TerminalH + 5 * (1 - Math.Abs(lz) / (hall.Depth / 2));
        }

        static bool HiddenByTerminal(PropFootprint hall, Point3 eye, double x, double y, double z)
        {
            for (var k = 1; k < 400; k++)
            {
                var t = k / 400.0;
                if (eye.Y + (y - eye.Y) * t < TerminalRoof(hall, eye.X + (x - eye.X) * t, eye.Z + (z - eye.Z) * t)) return true;
            }
            return false;
        }

        [Test]
        public void TheBasinsSecondChargerShowsPastTheTerminalFromThePortraitHome()
        {
            var plan = U5Plan.Plan; var eye = U5Plan.Map.Pose("pose:home:portrait").Camera;
            var hall = plan.Registry.Entries.Single(f => f.Id == "device:hp-meter-ferry-terminal");
            Assert.That(hall.Width, Is.EqualTo(NearLandmarks.TerminalW + 1).Within(1e-6), "the hall's reserved length is its own");
            foreach (var token in new[] { "hp-charger-basin-1", "hp-charger-basin-2" })
            {
                var c = plan.Registry.Entries.Single(f => f.Id == "device:" + token).At;
                // the whole charger, foot to head, and the width of its housing
                foreach (var y in new[] { Deck + .1, Deck + 1.0, Deck + 1.9 })
                    foreach (var side in new[] { -.9, 0, .9 })
                        Assert.That(HiddenByTerminal(hall, eye, c.X, y, c.Z + side), Is.False, token + " at " + y.ToString("F1") + " m is behind the terminal");
            }
            // the same sight lines through the hall as U7 built it (56 m, centred on the meter): the second charger is behind it
            var u7 = new PropFootprint("u7", U5Plan.Map.Anchors["hp-meter-ferry-terminal"], 57, hall.Depth, hall.Yaw);
            var second = plan.Registry.Entries.Single(f => f.Id == "device:hp-charger-basin-2").At;
            Assert.That(HiddenByTerminal(u7, eye, second.X, Deck + 1.0, second.Z), Is.True, "the check sees what U7 hid");
            // the meter still stands in its building, and the hall is still on the quay
            var meter = U5Plan.Map.Anchors["hp-meter-ferry-terminal"];
            Assert.That(TerminalRoof(hall, meter.X, meter.Z), Is.GreaterThan(Deck));
        }

        // ---------------------------------------------------------------- the wet wells' view

        [Test]
        public void NoChargingBayStandsInFrontOfAWetWell()
        {
            var plan = U5Plan.Plan;
            var cars = plan.Registry.Entries.Where(f => f.Id.StartsWith("car:", StringComparison.Ordinal)).ToArray();
            Assert.That(cars.Length, Is.EqualTo(10), "every charger keeps a bay: the district's six and the four that hold their own session");
            foreach (var car in cars) Assert.That(plan.InWellView(car.At), Is.False, car.Id);
            // East Quay's first charger is the one whose bay was in the line
            var east = cars.Single(f => f.Id == "car:hp-charger-east-1"); var well = plan.Registry.Entries.Single(f => f.Id == "device:hp-pump-east:well").At;
            Assert.That(Math.Abs(east.At.Z - well.Z), Is.GreaterThan(WorldPropPlan.WellViewHalfWidthM + 1));
            // the strip is the line from each well to its view's eye, and nothing else
            foreach (var f in StoryPoses.Families.Where(f => f.Token.StartsWith("hp-pump-", StringComparison.Ordinal)))
            {
                var p = U5Plan.Map.Pose("pose:" + f.Family + ":well"); var w = plan.Registry.Entries.Single(e => e.Id == "device:" + f.Token + ":well").At;
                for (var t = .2; t < 1; t += .2) Assert.That(plan.InWellView(new Ground(w.X + (p.Camera.X - w.X) * t, w.Z + (p.Camera.Z - w.Z) * t)), Is.True, f.Family);
                Assert.That(plan.InWellView(new Ground(w.X - (p.Camera.X - w.X) * .5, w.Z - (p.Camera.Z - w.Z) * .5)), Is.False, "behind the well");
                Assert.That(plan.InWellView(U5Plan.Map.Anchors[f.Token]), Is.False, "the pump house");
            }
            // every well is seen from the same stand-off now: East Quay's was nearer and higher, over a canopy
            var poses = StoryPoses.Families.Where(f => f.Token.StartsWith("hp-pump-", StringComparison.Ordinal)).Select(f => U5Plan.Map.Pose("pose:" + f.Family + ":well")).ToArray();
            Assert.That(poses.Select(p => p.Camera.Y).Distinct().Count(), Is.EqualTo(1));
            Assert.That(poses.Select(p => Math.Round(Math.Sqrt(Math.Pow(p.Camera.X - p.Look.X, 2) + Math.Pow(p.Camera.Z - p.Look.Z, 2)), 6)).Distinct().Single(), Is.EqualTo(PresentationMap.WellViewBackM));
        }

        [Test]
        public void TheWellsLevelTagsStandAboveTheCaption()
        {
            // The caption's box is the lower 8-24 % of a 16:9 frame (HarborStoryHud); the tags are text 2.5 m in front of the well at their
            // levels (PumpStationBuilder: the well's floor is 4.4 m under the deck). U7 looked at a point 2 m up: "low cut-off" was 20 % up.
            const double captionTop = .24, floor = Deck - 4.4, tagFront = 2.5;
            foreach (var f in StoryPoses.Families.Where(f => f.Token.StartsWith("hp-pump-", StringComparison.Ordinal)))
            {
                var p = U5Plan.Map.Pose("pose:" + f.Family + ":well"); var well = U5Plan.Plan.Registry.Entries.Single(e => e.Id == "device:" + f.Token + ":well").At;
                double dx = p.Camera.X - well.X, dz = p.Camera.Z - well.Z; var n = Math.Sqrt(dx * dx + dz * dz);
                double FromBottom(double level, CameraPose pose)
                {
                    Assert.That(CameraMath.Project(pose, Wide, well.X + dx / n * tagFront, floor + level + .08, well.Z + dz / n * tagFront, out var nx, out var ny), Is.True);
                    Assert.That(Math.Abs(nx), Is.LessThan(.1));
                    return (ny + 1) / 2;
                }
                // half a line of the tag's own text is about 1.5 % of the frame
                Assert.That(FromBottom(PumpPresentation.LowCutoffM, p), Is.GreaterThan(captionTop + .05), f.Family + ": low cut-off");
                Assert.That(FromBottom(PumpPresentation.FloatMarkerM, p), Is.LessThan(.6), f.Family + ": the float tag is still under the well's rim");
                // the well's far rim is under the banner and the status line (the top 21 %)
                Assert.That(CameraMath.Project(p, Wide, well.X - dx / n * WellFootprint.RadiusM, Deck, well.Z - dz / n * WellFootprint.RadiusM, out _, out var rim), Is.True);
                Assert.That((rim + 1) / 2, Is.LessThan(.79), f.Family + ": the far rim");
                var u7 = new CameraPose(p.Camera, new Point3(p.Look.X, 2, p.Look.Z), 42);
                Assert.That(FromBottom(PumpPresentation.LowCutoffM, u7), Is.LessThan(captionTop), "the check sees U7's overlap");
            }
        }

        // ---------------------------------------------------------------- the morning's light

        [Test]
        public void TheHoursAfterSunriseAreNotBlue()
        {
            // 07:06: U7 left the sky's light 1.36 times as blue as red (a quay's top read 75, 82, 93), between a warm dawn and a neutral noon
            foreach (var h in new[] { 6.7, 7.1, 7.5 })
            {
                var s = Clear(h);
                Assert.That(s.AmbientSky.B / s.AmbientSky.R, Is.InRange(1.0, h > 7.3 ? 1.32 : 1.24), "sky ambient at " + h);
                Assert.That(s.AmbientEquator.B / s.AmbientEquator.R, Is.LessThan(1.26), "wall ambient at " + h);
            }
            Assert.That(Clear(7.1).AmbientSky.B / Clear(7.1).AmbientSky.R, Is.LessThan(1.22));
            // the drain is a morning's and an evening's: noon keeps the light it had
            var noon = Clear(12.5);
            Assert.That(noon.AmbientSky.B / noon.AmbientSky.R, Is.GreaterThan(1.5));
            Assert.That(noon.AmbientEquator.B / noon.AmbientEquator.R, Is.GreaterThan(1.35));
            // and it comes on without a step: the light's colour moves by a few percent a quarter of an hour, from dawn to mid-morning
            for (var h = 6.5; h < 10; h += .25)
            {
                double a = Clear(h).AmbientSky.B / Clear(h).AmbientSky.R, b = Clear(h + .25).AmbientSky.B / Clear(h + .25).AmbientSky.R;
                Assert.That(Math.Abs(b - a), Is.LessThan(.15), "between " + h + " and " + (h + .25));
            }
        }
    }
}
