// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>The level-of-detail switch and the near-detail plans, with no engine in the loop.</summary>
    public class NearDetailTests
    {
        // ---------------------------------------------------------------- the switch

        static DetailSwitch OneAt(float x, float radius = 0)
        {
            var s = new DetailSwitch(4);
            s.Add(x, 0, 0, radius);
            return s;
        }

        [Test]
        public void HysteresisLeavesNoFlickerBandBetweenEnterAndLeave()
        {
            // pins: a set enters at 110 m and leaves at 130 m, so a camera sitting anywhere between them keeps whatever it had
            var s = OneAt(0);
            long frame = 0;
            s.Update(frame++, 125, 0, 0);
            Assert.That(s.IsNear(0), Is.False, "125 m is outside the 110 m enter range");
            s.Update(frame++, 111, 0, 0);
            Assert.That(s.IsNear(0), Is.False, "111 m has not crossed the enter range");
            s.Update(frame++, 109, 0, 0);
            Assert.That(s.IsNear(0), Is.True);
            var changes = 0;
            foreach (var d in new[] { 112f, 128f, 115f, 129.5f, 111f, 127f }) changes += s.Update(frame++, d, 0, 0);
            Assert.That(changes, Is.EqualTo(0), "no change anywhere inside the band");
            Assert.That(s.IsNear(0), Is.True);
            Assert.That(s.Update(frame++, 131, 0, 0), Is.EqualTo(1));
            Assert.That(s.IsNear(0), Is.False);
            changes = 0;
            foreach (var d in new[] { 128f, 115f, 111f, 125f, 129f }) changes += s.Update(frame++, d, 0, 0);
            Assert.That(changes, Is.EqualTo(0), "back inside the band it stays out until it crosses 110 m");
        }

        [Test]
        public void RangeIsMeasuredToTheSetsNearestEdge()
        {
            var big = OneAt(0, 40);
            big.Update(0, 151, 0, 0);
            Assert.That(big.IsNear(0), Is.False, "151 m to the centre of a 40 m radius set is 111 m to its edge");
            big.Update(1, 149, 0, 0);
            Assert.That(big.IsNear(0), Is.True, "149 m to the centre is 109 m to the edge");
        }

        [Test]
        public void AtMostMaxNearAreNearAndTheNearestWin()
        {
            var s = new DetailSwitch(8);
            for (var i = 0; i < 60; i++) s.Add(i * 1.5f, 0, 0, 0);
            s.Update(0, 0, 0, 0);
            Assert.That(s.NearCount, Is.EqualTo(DetailSwitch.MaxNear));
            for (var i = 0; i < DetailSwitch.MaxNear; i++) Assert.That(s.IsNear(i), Is.True, "set " + i + " is among the nearest");
            Assert.That(s.IsNear(DetailSwitch.MaxNear), Is.False);
            // standing still does not shuffle the cap
            for (var f = 1; f < 20; f++) Assert.That(s.Update(f, 0, 0, 0), Is.EqualTo(0));
            // a camera drifting one step keeps the set that was near (the stickiness) unless another is clearly nearer
            Assert.That(s.Update(30, .4f, 0, 0), Is.EqualTo(0));
        }

        [Test]
        public void AMovingSetSwitchesByItsCurrentPosition()
        {
            var s = new DetailSwitch(4);
            var truck = s.Add(300, 0, 0, 4);
            Assert.That(s.Update(0, 0, 0, 0), Is.EqualTo(0));
            s.Move(truck, 100, 0, 0);
            Assert.That(s.Update(1, 0, 0, 0), Is.EqualTo(1));
            Assert.That(s.IsNear(truck), Is.True);
            s.Move(truck, 133, 0, 0);
            Assert.That(s.Update(2, 0, 0, 0), Is.EqualTo(0), "129 m to its edge is inside the leave range");
            s.Move(truck, 140, 0, 0);
            Assert.That(s.Update(3, 0, 0, 0), Is.EqualTo(1), "136 m to its edge is outside it");
            Assert.That(s.IsNear(truck), Is.False);
        }

        [Test]
        public void AnInactiveSetIsNeverNear()
        {
            var s = OneAt(5);
            s.SetActive(0, false);
            s.Update(0, 0, 0, 0);
            Assert.That(s.IsNear(0), Is.False, "a charger's car while no session shows it");
            s.SetActive(0, true);
            s.Update(1, 0, 0, 0);
            Assert.That(s.IsNear(0), Is.True);
        }

        [Test]
        public void TheDecisionIsRetakenOnlyWhenDue()
        {
            var s = OneAt(0);
            Assert.That(s.Due(0, 0, 0, 0), Is.True, "the first decision is always due");
            s.Update(0, 0, 0, 0);
            Assert.That(s.Due(1, 0, 0, 0), Is.False);
            Assert.That(s.Due(DetailSwitch.EveryFrames - 1, .5f, 0, 0), Is.False, "half a metre is not a move");
            Assert.That(s.Due(DetailSwitch.EveryFrames, 0, 0, 0), Is.True, "a few frames have passed");
            Assert.That(s.Due(1, 1.2f, 0, 0), Is.True, "a metre and a bit is a move, whatever the frame");
        }

        [Test]
        public void TheSwitchAllocatesNothingOnceBuilt()
        {
            var s = new DetailSwitch(8);
            for (var i = 0; i < 200; i++) s.Add(i * 3f % 400, i % 7, i * 5f % 300, 4 + i % 9);
            s.Update(0, 10, 5, 10);
            // a collection count rather than allocated bytes: the byte counter is not in every runtime the tests run on, and a hundred thousand
            // decisions with even a small allocation each would force several gen-0 collections
            GC.Collect();
            var before = GC.CollectionCount(0);
            for (var f = 1; f < 100000; f++)
            {
                var x = (f % 90) * 3f; s.Move(3, x, 0, 20); s.SetActive(5, f % 2 == 0);
                if (s.Due(f, x + (f % 3), 5, 10)) s.Update(f, x + (f % 3), 5, 10);
                s.IsNear(f % 200);
            }
            Assert.That(GC.CollectionCount(0) - before, Is.EqualTo(0), "a per-frame allocation would be paid for 60 times a second");
        }

        // ---------------------------------------------------------------- the poses

        static string StoryboardText()
        {
            var rel = Path.Combine("Assets", "Harborpulse", "Resources", "storyboard.json");
            for (var dir = new DirectoryInfo(Directory.GetCurrentDirectory()); dir != null; dir = dir.Parent)
            {
                var direct = Path.Combine(dir.FullName, rel);
                if (File.Exists(direct)) return File.ReadAllText(direct);
                var inRepo = Path.Combine(dir.FullName, "demos", "harborpulse-unity", rel);
                if (File.Exists(inRepo)) return File.ReadAllText(inRepo);
            }
            throw new FileNotFoundException("storyboard.json not found above " + Directory.GetCurrentDirectory());
        }

        sealed class Site
        {
            public string Kind; public float X, Y, Z, R;
        }

        static Site Lot(WorldProp p, float height, string kind = null)
        {
            var ground = (float)CoastalTerrain.Height(p.At.X, p.At.Z);
            return new Site { Kind = kind ?? p.Kind, X = (float)p.At.X, Y = ground + height / 2, Z = (float)p.At.Z,
                R = .5f * (float)Math.Sqrt(p.Width * p.Width + (p.Depth + 7) * (p.Depth + 7) + height * height) };
        }

        /// <summary>Every building and vehicle that has a near set: the plan's lots, the metered buildings, the truck at its depot, the moorings.</summary>
        static List<Site> Sites(HarborLayout layout, PresentationMap map)
        {
            var plan = WorldPropPlan.Generate(map, layout);
            var sites = new List<Site>();
            foreach (var p in plan.Props)
            {
                switch (p.Kind)
                {
                    case "House": sites.Add(Lot(p, 7.2f)); break;
                    case "Shopfront": sites.Add(Lot(p, 7.2f)); break;
                    case "Commercial": sites.Add(Lot(p, 11)); break;
                    case "Inn": sites.Add(Lot(p, 9.9f)); break;
                    case "Shed": sites.Add(Lot(p, 5.2f)); break;
                    case "Church": sites.Add(Lot(p, 12)); break;
                    case "ParkedCar": case "ChargingCar": sites.Add(new Site { Kind = p.Kind, X = (float)p.At.X, Y = (float)SitePlan.DeckHeightM + 1, Z = (float)p.At.Z, R = 2.6f }); break;
                    case "HauledBoat": sites.Add(new Site { Kind = p.Kind, X = (float)p.At.X, Y = (float)SitePlan.DeckHeightM + 2, Z = (float)p.At.Z, R = 7 }); break;
                }
            }
            foreach (var d in layout.Devices.Where(d => d.Kind == DeviceKind.Meter))
                sites.Add(new Site { Kind = "Meter", X = (float)map.Anchors[d.Token].X, Y = (float)SitePlan.DeckHeightM + 8, Z = (float)map.Anchors[d.Token].Z, R = 30 });
            sites.Add(new Site { Kind = "Depot", X = (float)map.Depot.X - 18, Y = (float)SitePlan.DeckHeightM + 4, Z = (float)map.Depot.Z - 14, R = 17 });
            sites.Add(new Site { Kind = "Truck", X = (float)map.Depot.X, Y = (float)SitePlan.DeckHeightM + 1, Z = (float)map.Depot.Z, R = 4.5f });
            foreach (var b in new[] { new[] { 112f, -64.5f }, new[] { 104f, -76f }, new[] { -128f, -96.5f }, new[] { -136f, -108f }, new[] { -104f, -18f }, new[] { -92f, 22f }, new[] { 92f, -12f }, new[] { 108f, 30f } })
                sites.Add(new Site { Kind = "Boat", X = b[0], Y = 1, Z = b[1], R = 8 });
            return sites;
        }

        /// <summary>The vertices of a typical near set of each kind (the average house, the largest of the rest), from the plans themselves.</summary>
        static Dictionary<string, int> KindCost()
        {
            var houses = EveryHouse().ToList();
            return new Dictionary<string, int>
            {
                ["House"] = (int)houses.Average(p => p.Vertices), ["Shopfront"] = NearBuildings.House(Spec(30, 30, 11, 11, 2, true, true, false)).Vertices,
                ["Commercial"] = NearBuildings.Commercial(60, 90, 17, 13, 3, true, true).Vertices, ["Inn"] = NearBuildings.Inn(10, 20, 24, 14, true).Vertices,
                ["Shed"] = NearBuildings.Shed(20, 30, 20, 11, true, NearReach.Lot).Vertices, ["Church"] = NearBuildings.Church().Vertices,
                ["ParkedCar"] = NearVehicles.Car(CarSpec.Parked).Vertices, ["ChargingCar"] = NearVehicles.Car(CarSpec.Charging).Vertices,
                ["HauledBoat"] = NearVehicles.HauledBoat().Vertices, ["Depot"] = NearLandmarks.Depot().Vertices, ["Truck"] = NearVehicles.Truck().Vertices,
                ["Boat"] = NearVehicles.Boat(BoatKind.Lobster, 11, 3.5f).Vertices,
                // the two metered buildings and the ferry berth: whichever is near, the larger plan
                ["Meter"] = NearLandmarks.Lofts().Vertices + NearLandmarks.Ferry().Vertices,
            };
        }

        static int NearAt(IReadOnlyList<Site> sites, NearVec camera, out int vertices)
        {
            var s = new DetailSwitch(sites.Count);
            foreach (var site in sites) s.Add(site.X, site.Y, site.Z, site.R);
            s.Update(0, camera.X, camera.Y, camera.Z);
            var cost = KindCost(); vertices = 0;
            for (var i = 0; i < sites.Count; i++) if (s.IsNear(i)) vertices += cost[sites[i].Kind];
            return s.NearCount;
        }

        static IEnumerable<(string Name, CameraPose Pose)> Poses(PresentationMap map)
        {
            var names = DetailCandidates.PoseNames().Concat(DetailCandidates.PoseNamesIn(StoryboardText())).Distinct();
            foreach (var n in names)
            {
                CameraPose pose;
                try { pose = map.Pose(n); } catch (FormatException) { continue; }
                yield return (n, pose);
            }
        }

        [Test]
        public void HomeSelectsNoNearSetInEitherAspect()
        {
            var layout = Kit.Layout(); var map = new PresentationMap(layout);
            var sites = Sites(layout, map);
            foreach (var name in new[] { "pose:home", "pose:home:portrait", "pose:night-home", "pose:storm-home" })
            {
                var p = map.Pose(name);
                var near = NearAt(sites, new NearVec((float)p.Camera.X, (float)p.Camera.Y, (float)p.Camera.Z), out _);
                Assert.That(near, Is.EqualTo(0), name + " draws the far level only");
            }
        }

        [Test]
        public void EveryGroundLevelPoseHasANearBuildingOrVehicleAndNeverMoreThanTheCap()
        {
            var layout = Kit.Layout(); var map = new PresentationMap(layout);
            var sites = Sites(layout, map);
            var ground = 0;
            foreach (var (name, pose) in Poses(map))
            {
                var camera = new NearVec((float)pose.Camera.X, (float)pose.Camera.Y, (float)pose.Camera.Z);
                var near = NearAt(sites, camera, out var vertices);
                Assert.That(near, Is.LessThanOrEqualTo(DetailSwitch.MaxNear), name);
                // the mouth-wide pose stands over open water and frames the far quay: nothing is within near range of it by design
                if (camera.Y > 12 || name.Contains("home") || name == "pose:east-quay:mouth-wide") continue;
                ground++;
                TestContext.Out.WriteLine(name + ": " + near + " near sets, about " + vertices + " vertices");
                Assert.That(near, Is.GreaterThan(0), name + " is a ground-level pose and must show some near detail");
                Assert.That(vertices, Is.LessThan(250000), name + " stays inside the near-detail vertex budget");
            }
            Assert.That(ground, Is.GreaterThan(5), "the ground-level poses were found");
        }

        [Test]
        public void EveryPoseTheStoryboardNamesResolvesAndIsACandidateSource()
        {
            var layout = Kit.Layout(); var map = new PresentationMap(layout);
            var named = DetailCandidates.PoseNamesIn(StoryboardText());
            Assert.That(named, Is.Not.Empty);
            foreach (var n in named) Assert.DoesNotThrow(() => map.Pose(n), n);
            Assert.That(DetailCandidates.PoseNames().Distinct().Count(), Is.EqualTo(DetailCandidates.PoseNames().Count), "no pose is listed twice");
        }

        [Test]
        public void CandidatesCoverWhateverAStoryCameraCanBringNear()
        {
            var layout = Kit.Layout(); var map = new PresentationMap(layout);
            var sites = Sites(layout, map);
            var poses = Poses(map).ToList();
            var cams = poses.Select(p => new NearVec((float)p.Pose.Camera.X, (float)p.Pose.Camera.Y, (float)p.Pose.Camera.Z)).ToList();
            var looks = poses.Select(p => new NearVec((float)p.Pose.Look.X, (float)p.Pose.Look.Y, (float)p.Pose.Look.Z)).ToList();
            // anything the decision picks at a pose camera, or twenty metres from it, is a candidate
            foreach (var cam in cams)
                foreach (var dx in new[] { 0f, 20f, -20f })
                {
                    var s = new DetailSwitch(sites.Count);
                    foreach (var site in sites) s.Add(site.X, site.Y, site.Z, site.R);
                    s.Update(0, cam.X + dx, cam.Y, cam.Z);
                    for (var i = 0; i < sites.Count; i++)
                        if (s.IsNear(i)) Assert.That(DetailCandidates.Reachable(sites[i].X, sites[i].Y, sites[i].Z, sites[i].R, cams, looks), Is.True, sites[i].Kind);
                }
            var candidates = sites.Count(x => DetailCandidates.Reachable(x.X, x.Y, x.Z, x.R, cams, looks));
            Assert.That(candidates, Is.LessThan(sites.Count), "the far end of the town is not built at load");
        }

        // ---------------------------------------------------------------- the plans

        static HouseSpec Spec(float x, float z, float w, float d, int floors, bool porch, bool shop, bool gableFront, int style = 0, bool rich = true)
        {
            var h = floors * 3.1f + .6f; var rise = Math.Min(w, d) * (gableFront ? .42f : .36f);
            return new HouseSpec(x, z, w, d, floors, porch, shop, gableFront, style, rise, rich, rich && style == 0 && !gableFront, w * .3f, h + rise * .6f, d * .15f, 2.8f);
        }

        static IEnumerable<NearPlan> EveryHouse()
        {
            foreach (var w in new[] { 9f, 10.5f, 12f }) foreach (var d in new[] { 8f, 9.5f, 11f })
                foreach (var porch in new[] { false, true }) foreach (var gable in new[] { false, true }) foreach (var style in new[] { 0, 1, 4 })
                    yield return NearBuildings.House(Spec(w * 3.1f, d * 7.3f, w, d, 2, porch, false, gable, style));
            foreach (var w in new[] { 10f, 11f, 12f })
                yield return NearBuildings.House(Spec(w * 5f, 50, w, 11, 2, true, true, false));
            yield return NearBuildings.House(Spec(500, 400, 10, 9, 1, true, false, false, 0, false));
        }

        static void Inside(NearPlan plan, float halfX, float frontZ, float backZ, float tolerance, string what)
        {
            plan.Bounds(out var x0, out var x1, out _, out var y1, out var z0, out var z1);
            Assert.That(x0, Is.GreaterThanOrEqualTo(-halfX - tolerance), what + " min x");
            Assert.That(x1, Is.LessThanOrEqualTo(halfX + tolerance), what + " max x");
            Assert.That(z0, Is.GreaterThanOrEqualTo(-frontZ - tolerance), what + " front");
            Assert.That(z1, Is.LessThanOrEqualTo(backZ + tolerance), what + " back");
            Assert.That(y1, Is.LessThan(60), what + " height");
        }

        [Test]
        public void NearDetailStaysInsideTheLotTheBuildingReserved()
        {
            // a town lot is (w+1) by (d+7): half a metre either side of the walls, three and a half front and back
            foreach (var w in new[] { 9f, 10.5f, 12f }) foreach (var d in new[] { 8f, 9.5f, 11f })
                foreach (var porch in new[] { false, true }) foreach (var gable in new[] { false, true }) foreach (var style in new[] { 0, 1, 4 })
                    Inside(NearBuildings.House(Spec(w * 3.1f, d * 7.3f, w, d, 2, porch, false, gable, style)), w / 2 + .5f, d / 2 + 3.5f, d / 2 + 3.5f, 0, "house " + w + "x" + d + " porch=" + porch + " gable=" + gable + " style=" + style);
            foreach (var w in new[] { 10f, 11f, 12f })
                Inside(NearBuildings.House(Spec(w * 5f, 50, w, 11, 2, true, true, false)), w / 2 + .5f, 11 / 2f + 3.5f, 11 / 2f + 3.5f, 0, "shopfront " + w);
            foreach (var w in new[] { 15f, 18f, 20f }) foreach (var floors in new[] { 2, 3 })
                Inside(NearBuildings.Commercial(w * 4, 90, w, 13, floors, true, true), w / 2 + .5f, 13 / 2f + 3.5f, 13 / 2f + 3.5f, 0, "commercial " + w + " floors " + floors);
            Inside(NearBuildings.Inn(10, 20, 24, 14, true), 12.5f, 14 / 2f + 3.5f, 14 / 2f + 3.5f, 0, "inn");
            foreach (var w in new[] { 18f, 21f, 24f })
                Inside(NearBuildings.Shed(w, 30, w, 11, true, NearReach.Lot), w / 2 + .5f, 11 / 2f + 3.5f, 11 / 2f + 3.5f, 0, "town shed " + w);
            // a boatyard shed reserves one metre all round
            foreach (var (w, d) in new[] { (20f, 11f), (18f, 10f), (22f, 12f) })
                Inside(NearBuildings.Shed(w, d * 3, w, d, true, new NearReach(1, 1, 1)), w / 2 + 1, d / 2 + 1, d / 2 + 1, 0, "yard shed " + w);
            // the church (footprint 14 x 38), the lofts (39.2 x 19.2), the terminal (57 x 23), the depot (29 x 17)
            Inside(NearBuildings.Church(), 7, 19, 19, 0, "church");
            Inside(NearLandmarks.Lofts(), 19.6f, 9.6f, 9.6f, 0, "lofts");
            Inside(NearLandmarks.Terminal(), 28.5f, 11.5f, 11.5f, 0, "terminal");
            Inside(NearLandmarks.Depot(), 14.5f, 8.5f, 8.5f, 0, "depot");
        }

        [Test]
        public void VehiclesStayWithinTheirFootprintsPlusTheThinnestTrim()
        {
            // trim a few centimetres proud of the far level's bodies (lamps, plates, hub caps) is allowed to touch the 0.15 m clearance between cars
            const float trim = .06f;
            Inside(NearVehicles.Car(CarSpec.Parked), (float)WorldPropPlan.ParkedWidth / 2, (float)WorldPropPlan.ParkedDepth / 2, (float)WorldPropPlan.ParkedDepth / 2, trim, "parked car");
            Inside(NearVehicles.Car(CarSpec.Charging), (float)WorldPropPlan.CarWidth / 2, (float)WorldPropPlan.CarDepth / 2, (float)WorldPropPlan.CarDepth / 2, trim, "charging car");
            Inside(NearVehicles.Truck(), 1.7f, 3.8f, 3.8f, 0, "truck");
            Inside(NearVehicles.HauledBoat(), 2, 6, 6, trim, "hauled boat");
        }

        [Test]
        public void ACutToHomeDecidesFromScratchWhateverTheLastCameraHad()
        {
            // the HOME check poses the camera at HOME right after the director left it at a ground pose: sets near there must not survive the cut
            var layout = Kit.Layout(); var map = new PresentationMap(layout);
            var sites = Sites(layout, map);
            var ground = Poses(map).Select(p => p.Pose).First(p => p.Camera.Y <= 12 && sites.Count(x => Near(x, p)) > 3);
            var home = map.Pose("pose:home");
            var s = new DetailSwitch(sites.Count);
            foreach (var site in sites) s.Add(site.X, site.Y, site.Z, site.R);
            s.Update(0, (float)ground.Camera.X, (float)ground.Camera.Y, (float)ground.Camera.Z);
            Assert.That(s.NearCount, Is.GreaterThan(0), "the ground pose has near sets to leave behind");
            // forced: from scratch, ignoring hysteresis and the retake schedule
            s.Update(1, (float)home.Camera.X, (float)home.Camera.Y, (float)home.Camera.Z, true);
            Assert.That(s.NearCount, Is.EqualTo(0), "a forced decision at HOME shows nothing");
            // unforced but a jump of more than 150 m is a cut as well: the answer a fresh start gives
            var t = new DetailSwitch(sites.Count);
            foreach (var site in sites) t.Add(site.X, site.Y, site.Z, site.R);
            t.Update(0, (float)ground.Camera.X, (float)ground.Camera.Y, (float)ground.Camera.Z);
            t.Update(1, (float)home.Camera.X, (float)home.Camera.Y, (float)home.Camera.Z);
            Assert.That(t.NearCount, Is.EqualTo(0));
            // and a set sitting in the leave band of the old camera does not keep itself near across a cut that lands in the same band
            var band = OneAt(0);
            band.Update(0, 100, 0, 0);
            Assert.That(band.IsNear(0), Is.True);
            band.Update(1, -125, 0, 0, true);
            Assert.That(band.IsNear(0), Is.False, "125 m is outside the enter range, and a forced decision has no memory");
            band.Update(2, 100, 0, 0);
            band.Update(3, -125, 0, 0);
            Assert.That(band.IsNear(0), Is.False, "a 225 m jump is a cut too");
        }

        static bool Near(Site x, CameraPose p)
        {
            float dx = x.X - (float)p.Camera.X, dy = x.Y - (float)p.Camera.Y, dz = x.Z - (float)p.Camera.Z;
            return Math.Sqrt(dx * dx + dy * dy + dz * dz) - x.R < DetailSwitch.EnterM;
        }

        [Test]
        public void HousesHaveAStreetSideDoorAndTheCarIsACompleteModel()
        {
            foreach (var w in new[] { 9f, 10f, 11f, 12f })
            {
                var plan = NearBuildings.House(Spec(40, 60, w, 9, 2, true, false, false));
                Assert.That(plan.Parts.Count(p => p.Shape == NearShape.Box && p.Tone == NearTone.Door && Math.Abs(p.B.Y - 2.2f) < .01f && p.A.Z > 4), Is.EqualTo(1), "a rear door at width " + w);
            }
            var car = NearVehicles.Car(CarSpec.Parked);
            Assert.That(car.Parts.Count(p => p.Shape == NearShape.Quad && p.Tone == NearTone.Glass), Is.EqualTo(4), "windscreen, rear window and two sides");
            Assert.That(car.Parts.Count(p => p.Shape == NearShape.Quad && p.Tone == NearTone.Paint), Is.GreaterThanOrEqualTo(24), "hull, bonnet, shoulder and cabin are paint");
            Assert.That(car.Parts.Count(p => p.Shape == NearShape.Cylinder && p.Tone == NearTone.Black), Is.EqualTo(8), "an arch liner and a tyre at each of four wheels");
            // the cabin is narrower at the roof than at its base
            car.Bounds(out _, out _, out var y0, out var y1, out _, out _);
            Assert.That(y1, Is.GreaterThan(1.5f));
        }

        [Test]
        public void NearPlansAreWellFormed()
        {
            var plans = new List<NearPlan>(EveryHouse())
            {
                NearBuildings.Commercial(60, 90, 16, 13, 3, true, true), NearBuildings.Inn(10, 20, 24, 14, true), NearBuildings.Shed(20, 30, 20, 11, true, NearReach.Lot),
                NearBuildings.Church(), NearLandmarks.Lofts(), NearLandmarks.Terminal(), NearLandmarks.Depot(), NearLandmarks.Ferry(),
                NearVehicles.Car(CarSpec.Parked), NearVehicles.Car(CarSpec.Charging), NearVehicles.Truck(), NearVehicles.HauledBoat(),
            };
            foreach (var kind in Enum.GetValues(typeof(BoatKind)).Cast<BoatKind>()) plans.Add(NearVehicles.Boat(kind, 11, 3.5f));
            foreach (var plan in plans)
            {
                Assert.That(plan.Parts, Is.Not.Empty, plan.Name);
                var counted = 0;
                foreach (var p in plan.Parts)
                {
                    counted += p.Vertices;
                    foreach (var v in new[] { p.A, p.B, p.C, p.D, p.N }) Assert.That(float.IsFinite(v.X + v.Y + v.Z), Is.True, plan.Name + " has a non-finite part");
                    if (p.Shape == NearShape.Box) Assert.That(p.B.X > 0 && p.B.Y > 0 && p.B.Z > 0, Is.True, plan.Name + " has a flat or inside-out box");
                    if (p.Shape == NearShape.Cylinder) Assert.That(p.C.Y, Is.GreaterThanOrEqualTo(3), plan.Name + " cylinder segments");
                    if (p.Shape == NearShape.Quad)
                    {
                        var ab = new[] { p.B.X - p.A.X, p.B.Y - p.A.Y, p.B.Z - p.A.Z }; var ac = new[] { p.C.X - p.A.X, p.C.Y - p.A.Y, p.C.Z - p.A.Z };
                        var cx = ab[1] * ac[2] - ab[2] * ac[1]; var cy = ab[2] * ac[0] - ab[0] * ac[2]; var cz = ab[0] * ac[1] - ab[1] * ac[0];
                        Assert.That(cx * cx + cy * cy + cz * cz, Is.GreaterThan(1e-8f), plan.Name + " has a degenerate quad");
                    }
                }
                Assert.That(plan.Vertices, Is.EqualTo(counted));
            }
        }

        [Test]
        public void NearPlansHaveBoundedCostPerKind()
        {
            // pins: a near set is a few thousand vertices, so twenty-five of them stay well inside the 250 k budget
            var house = EveryHouse().Max(p => p.Vertices);
            Assert.That(house, Is.LessThan(10000), "house");
            Assert.That(NearBuildings.Commercial(60, 90, 20, 13, 3, true, true).Vertices, Is.LessThan(14000));
            Assert.That(25 * (int)EveryHouse().Average(p => p.Vertices), Is.LessThan(250000), "twenty-five typical houses fit the budget");
            Assert.That(NearBuildings.Inn(10, 20, 24, 14, true).Vertices, Is.LessThan(16000));
            Assert.That(NearBuildings.Shed(20, 30, 24, 11, true, NearReach.Lot).Vertices, Is.LessThan(7000));
            Assert.That(NearBuildings.Church().Vertices, Is.LessThan(9000));
            Assert.That(NearLandmarks.Lofts().Vertices, Is.LessThan(16000));
            Assert.That(NearLandmarks.Terminal().Vertices, Is.LessThan(12000));
            Assert.That(NearLandmarks.Depot().Vertices, Is.LessThan(6000));
            Assert.That(NearLandmarks.Ferry().Vertices, Is.LessThan(9000));
            // the truck is one vehicle, and its near set replaces the far boxes instead of dressing them
            Assert.That(NearVehicles.Truck().Vertices, Is.LessThan(8000));
            Assert.That(NearVehicles.Car(CarSpec.Parked).Vertices, Is.LessThan(3500));
            foreach (var kind in Enum.GetValues(typeof(BoatKind)).Cast<BoatKind>()) Assert.That(NearVehicles.Boat(kind, 14, 4.5f).Vertices, Is.LessThan(3500), kind.ToString());
            Assert.That(NearVehicles.HauledBoat().Vertices, Is.LessThan(2500));
            // and a near house is clearly richer than the flat window panels it dresses: dozens of parts, not a handful
            Assert.That(NearBuildings.House(Spec(10, 10, 10, 9, 2, true, false, false)).Parts.Count, Is.GreaterThan(150));
        }

        [Test]
        public void ChoicesComeFromThePositionAndAreRepeatable()
        {
            var a1 = NearBuildings.House(Spec(41.3f, 207.9f, 10, 9, 2, true, false, false));
            var a2 = NearBuildings.House(Spec(41.3f, 207.9f, 10, 9, 2, true, false, false));
            Assert.That(a1.Parts.Count, Is.EqualTo(a2.Parts.Count));
            Assert.That(a1.Vertices, Is.EqualTo(a2.Vertices));
            for (var i = 0; i < a1.Parts.Count; i++) Assert.That(a1.Parts[i].A.X, Is.EqualTo(a2.Parts[i].A.X), "part " + i);
            // over many positions about half of the houses are shuttered and the porch things vary: neither is constant
            var counts = new HashSet<int>();
            var shuttered = 0; const int n = 400;
            for (var i = 0; i < n; i++)
            {
                var u = NearHash.Unit(i * 7.37f, i * 3.11f, 11);
                if (u < .45f) shuttered++;
                counts.Add(NearBuildings.House(Spec(i * 7.37f, i * 3.11f, 10, 9, 2, true, false, false)).Parts.Count);
            }
            Assert.That(shuttered / (double)n, Is.InRange(.35, .55));
            Assert.That(counts.Count, Is.GreaterThan(4), "porch items and shutters vary house to house");
            Assert.That(NearHash.Unit(10, 20, 1), Is.Not.EqualTo(NearHash.Unit(10, 20, 2)), "the salt separates the choices");
        }

        [Test]
        public void NearPlanSizesAreReported()
        {
            // not an assertion: the sizes the budget tests above bound, printed so a run shows what each kind costs
            var houses = EveryHouse().ToList();
            TestContext.Out.WriteLine("house vertices min/avg/max " + houses.Min(p => p.Vertices) + "/" + (int)houses.Average(p => p.Vertices) + "/" + houses.Max(p => p.Vertices)
                + ", parts max " + houses.Max(p => p.Parts.Count));
            void Line(NearPlan p)
            {
                p.Sphere(out _, out _, out _, out var r);
                TestContext.Out.WriteLine(p.Name + ": " + p.Vertices + " vertices, " + p.Parts.Count + " parts, windows=" + p.HasWindows + ", radius " + r.ToString("0.0"));
            }
            Line(NearBuildings.House(Spec(30, 30, 10, 9, 2, true, false, false))); Line(NearBuildings.House(Spec(30, 30, 11, 11, 2, true, true, false)));
            Line(NearBuildings.Commercial(60, 90, 17, 13, 3, true, true)); Line(NearBuildings.Inn(10, 20, 24, 14, true)); Line(NearBuildings.Shed(20, 30, 20, 11, true, NearReach.Lot));
            Line(NearBuildings.Church()); Line(NearLandmarks.Lofts()); Line(NearLandmarks.Terminal()); Line(NearLandmarks.Depot()); Line(NearLandmarks.Ferry());
            Line(NearVehicles.Truck()); Line(NearVehicles.Car(CarSpec.Parked)); Line(NearVehicles.Car(CarSpec.Charging)); Line(NearVehicles.HauledBoat());
            foreach (var kind in Enum.GetValues(typeof(BoatKind)).Cast<BoatKind>()) Line(NearVehicles.Boat(kind, 11, 3.5f));
            Assert.Pass();
        }

        // ---------------------------------------------------------------- alignment with the far level

        sealed class Pane { public float X0, X1, Y0, Y1, Z0, Z1; }

        /// <summary>The far level's flat window panes (boxes 0.12 deep on the wall plane), laid out as the town builder lays them out.</summary>
        static List<Pane> FarPanes(float w, float d, params (int facade, float origin, float span, int floors, float floorH, float sill, float sw, float sh, float spacing)[] rows)
        {
            var result = new List<Pane>();
            foreach (var r in rows)
            {
                var f = r.facade == 0 ? NearFacade.Front(w, d) : r.facade == 1 ? NearFacade.Rear(w, d) : r.facade == 2 ? NearFacade.Left(w, d) : NearFacade.Right(w, d);
                var count = WindowRow.Count(r.span, r.spacing); var start = WindowRow.Start(count, r.spacing);
                for (var fl = 0; fl < r.floors; fl++) for (var i = 0; i < count; i++)
                {
                    var u = r.origin + start + i * r.spacing; var v = r.sill + fl * r.floorH + r.sh / 2;
                    float cx = f.X(u, .06f), cz = f.Z(u, .06f), sx = f.SizeX(r.sw, .12f), sz = f.SizeZ(r.sw, .12f);
                    result.Add(new Pane { X0 = cx - sx / 2, X1 = cx + sx / 2, Y0 = v - r.sh / 2, Y1 = v + r.sh / 2, Z0 = cz - sz / 2, Z1 = cz + sz / 2 });
                }
            }
            return result;
        }

        static void NoPartCoversAPane(NearPlan plan, List<Pane> panes, string what)
        {
            // a near box that sank into a pane's face would z-fight with it or hide it; a frame beside it or a bar in front of it must not
            const float tolerance = .03f;
            foreach (var pane in panes)
                foreach (var part in plan.Parts.Where(p => p.Shape == NearShape.Box && p.Tone != NearTone.Window && Math.Abs(p.Yaw) < .01f))
                {
                    float hx = part.B.X / 2, hy = part.B.Y / 2, hz = part.B.Z / 2;
                    var ox = Math.Min(pane.X1, part.A.X + hx) - Math.Max(pane.X0, part.A.X - hx);
                    var oy = Math.Min(pane.Y1, part.A.Y + hy) - Math.Max(pane.Y0, part.A.Y - hy);
                    var oz = Math.Min(pane.Z1, part.A.Z + hz) - Math.Max(pane.Z0, part.A.Z - hz);
                    Assert.That(ox > tolerance && oy > tolerance && oz > tolerance, Is.False,
                        what + ": " + part.Tone + " box at (" + part.A.X + "," + part.A.Y + "," + part.A.Z + ") covers a pane");
                }
        }

        [Test]
        public void NearDetailDoesNotCoverTheFarWindowPanes()
        {
            foreach (var gable in new[] { false, true })
            {
                float w = 10, d = 9; const int floors = 2;
                var panes = FarPanes(w, d, (0, 0, w, floors, 3.1f, .9f, 1f, 1.4f, 2.8f), (1, 0, w, floors, 3.1f, .9f, 1f, 1.4f, 2.8f),
                    (2, 0, d, floors, 3.1f, .9f, 1f, 1.4f, 3.6f), (3, 0, d, floors, 3.1f, .9f, 1f, 1.4f, 3.6f));
                NoPartCoversAPane(NearBuildings.House(Spec(30, 30, w, d, floors, true, false, gable)), panes, "house");
            }
            {
                float w = 11, d = 11;
                var panes = FarPanes(w, d, (0, 0, w, 1, 3.1f, 4f, 1f, 1.5f, 2.6f), (1, 0, w, 2, 3.1f, .9f, 1f, 1.4f, 2.8f), (2, 0, d, 2, 3.1f, .9f, 1f, 1.4f, 3.6f), (3, 0, d, 2, 3.1f, .9f, 1f, 1.4f, 3.6f));
                NoPartCoversAPane(NearBuildings.House(Spec(30, 30, w, d, 2, true, true, false)), panes, "shopfront");
            }
            {
                float w = 16, d = 13;
                var panes = FarPanes(w, d, (0, 0, w, 2, 3.5f, 4.4f, 1.1f, 1.8f, 2.7f), (1, 0, w, 3, 3.5f, .9f, 1.1f, 1.8f, 2.9f), (2, 0, d, 3, 3.5f, .9f, 1.1f, 1.8f, 3.4f), (3, 0, d, 3, 3.5f, .9f, 1.1f, 1.8f, 3.4f));
                NoPartCoversAPane(NearBuildings.Commercial(30, 30, w, d, 3, true, true), panes, "commercial");
            }
            {
                float w = 24, d = 14;
                var panes = FarPanes(w, d, (0, 0, w, 3, 3.1f, .9f, 1f, 1.5f, 2.6f), (1, 0, w, 3, 3.1f, .9f, 1f, 1.5f, 2.6f), (2, 0, d, 3, 3.1f, .9f, 1f, 1.5f, 3.4f), (3, 0, d, 3, 3.1f, .9f, 1f, 1.5f, 3.4f));
                NoPartCoversAPane(NearBuildings.Inn(30, 30, w, d, true), panes, "inn");
            }
            {
                float w = 20, d = 11;
                var panes = FarPanes(w, d, (0, w * .2f, w * .5f, 1, 3f, 1.4f, 1f, 1f, 2.6f), (2, 0, d, 1, 3f, 1.4f, 1f, 1f, 3.2f));
                NoPartCoversAPane(NearBuildings.Shed(20, 30, w, d, true, NearReach.Lot), panes, "shed");
            }
        }

        [Test]
        public void WindowRowsAreTheFormulaTheFarWindowsAlwaysUsed()
        {
            // pins the row arithmetic shared by the far windows and the near frames: count = max(1, floor((span - 1.5) / spacing)), centred
            Assert.That(WindowRow.Count(10, 2.8f), Is.EqualTo(3));
            Assert.That(WindowRow.Count(9, 3.6f), Is.EqualTo(2));
            Assert.That(WindowRow.Count(2, 2.8f), Is.EqualTo(1), "never fewer than one");
            Assert.That(WindowRow.Start(3, 2.8f), Is.EqualTo(-2.8f).Within(1e-5));
            Assert.That(WindowRow.Start(1, 2.8f), Is.EqualTo(0).Within(1e-5));
        }

        [Test]
        public void TheLandmarkPanesAreFramedAtTheirFarPositions()
        {
            // the building builder's window rows: x from -w/2+2.2 every 3.4 while x < w/2-1.5, the pane 0.7 right of x, 0.08 deep on the +Z wall
            var lofts = NearLandmarks.Lofts();
            float w = NearLandmarks.LoftsW, d = NearLandmarks.LoftsD;
            var panes = new List<Pane>();
            for (var x = -w / 2 + 2.2f; x < w / 2 - 1.5f; x += 3.4f)
                for (var f = 0; f < 5; f++)
                    panes.Add(new Pane { X0 = x + .7f - .7f, X1 = x + .7f + .7f, Y0 = 1.7f + f * 3.2f - .75f, Y1 = 1.7f + f * 3.2f + .75f, Z0 = d / 2, Z1 = d / 2 + .08f });
            Assert.That(panes.Count, Is.EqualTo(55));
            NoPartCoversAPane(lofts, panes, "lofts");
            // every pane has a head (a box above it) in the plan: a frame was drawn for each
            var heads = lofts.Parts.Count(p => p.Shape == NearShape.Box && p.Tone == NearTone.Stone && Math.Abs(p.B.X - 1.9f) < .01f && Math.Abs(p.B.Y - .14f) < .01f);
            Assert.That(heads, Is.GreaterThanOrEqualTo(55));
        }

        [Test]
        public void NearOnlyWindowsUseTheWindowRole()
        {
            // the attic window and the second dormer are the only windows the near level adds; they must carry the shared window materials' role
            var gable = NearBuildings.House(Spec(33, 71, 10, 9, 2, true, false, true));
            Assert.That(gable.HasWindows, Is.True);
            Assert.That(gable.Parts.Count(p => p.Tone == NearTone.Window), Is.GreaterThan(0));
            // the wall-and-glass of a house with neither stays vertex-coloured
            var plain = NearBuildings.House(Spec(500, 400, 10, 9, 1, false, false, false, 0, false));
            Assert.That(plain.Parts.Any(p => p.Tone == NearTone.Window), Is.False);
        }
    }
}
