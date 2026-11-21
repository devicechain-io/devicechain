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
    /// <summary>U5 cameras: HOME framing, sky share and the ground-level story poses, all from pose/FOV/aspect maths (no engine).</summary>
    public class U5CameraTests
    {
        const double Deck = SitePlan.DeckHeightM;
        const double Wide = 16.0 / 9, Tall = 9.0 / 16;

        // The harbour's water in plan, as the HOME mask classes it: the sea south of the breakwaters and through the mouth, and the inner basin.
        static bool Water(double x, double z)
        {
            if (z < -154) return true;
            if (z < -142 && Math.Abs(x) >= 35 && ((x >= -215 && x <= -35) || (x >= 35 && x <= 220))) return false;
            if (z < -140) return true;
            return x > -155 && x < 140 && z >= -140 && z < 75;
        }

        /// <summary>Share of the frame whose ray meets the sea or the harbour, on the flat water plane (the mask's water, without boats and buoys).</summary>
        public static double WaterShare(CameraPose pose, double aspect, int columns = 128, int rows = 72)
        {
            var water = 0;
            for (var j = 0; j < rows; j++)
                for (var i = 0; i < columns; i++)
                    if (CameraMath.GroundHit(pose, aspect, (i + .5) / columns * 2 - 1, 1 - (j + .5) / rows * 2, 0, out var x, out var z) && Water(x, z)) water++;
            return water / (double)(columns * rows);
        }

        /// <summary>
        /// The fraction of the frame above the land's silhouette: the terrain mesh, the ridge and every tree top, projected and reduced to the
        /// highest point in each of 240 columns (the same measure as the HOME mask's sky, without boats, buildings and the HUD).
        /// </summary>
        public static double SkyShare(CameraPose pose, double aspect, WorldPropPlan plan, int columns = 240)
        {
            var top = new double[columns];
            for (var i = 0; i < columns; i++) top[i] = -1;
            void P(double x, double y, double z)
            {
                if (!CameraMath.Project(pose, aspect, x, y, z, out var nx, out var ny) || nx <= -1 || nx >= 1) return;
                var c = (int)((nx + 1) * .5 * columns);
                if (ny > top[c]) top[c] = ny;
            }
            for (var z = -140.0; z <= CoastalTerrain.TerrainReachM; z += 8)
                for (var x = -CoastalTerrain.TerrainHalfWidthM; x <= CoastalTerrain.TerrainHalfWidthM; x += 8)
                    P(x, plan.Terrain.MeshHeight(x, z), z);
            for (var zi = 0; zi < CoastalTerrain.RidgeRowZ.Length - 1; zi++)
                for (var z = CoastalTerrain.RidgeRowZ[zi]; z < CoastalTerrain.RidgeRowZ[zi + 1]; z += 20)
                    for (var x = -CoastalTerrain.RidgeHalfWidth; x <= CoastalTerrain.RidgeHalfWidth; x += 8) P(x, CoastalTerrain.RidgeHeight(x, z), z);
            foreach (var t in plan.Props)
                if (t.Kind == "Tree") P(t.At.X, plan.Terrain.MeshHeight(t.At.X, t.At.Z) + t.Height, t.At.Z);
            return top.Select(y => Math.Max(0, Math.Min(1, (1 - y) / 2))).Average();
        }

        static IEnumerable<LayoutDevice> Devices => U5Plan.Layout.Devices;

        [Test]
        public void ProjectionMathPlacesTheHorizonAndTheCentre()
        {
            var level = new CameraPose(new Point3(0, 50, 0), new Point3(0, 50, 100), 40);
            Assert.That(CameraMath.HorizonFromTop(level), Is.EqualTo(.5).Within(1e-9));
            Assert.That(CameraMath.Project(level, Wide, 0, 50, 100, out var nx, out var ny), Is.True);
            Assert.That(nx, Is.EqualTo(0).Within(1e-9)); Assert.That(ny, Is.EqualTo(0).Within(1e-9));
            Assert.That(CameraMath.Project(level, Wide, 100, 50, 100, out nx, out ny), Is.True);
            Assert.That(nx, Is.GreaterThan(0), "east of a camera looking north is to its right");
            // looking down by exactly half the field of view puts the horizon on the top edge
            var down = new CameraPose(new Point3(0, 50, 0), new Point3(0, 50 - 100 * Math.Tan(20 * Math.PI / 180), 100), 40);
            Assert.That(CameraMath.HorizonFromTop(down), Is.EqualTo(0).Within(1e-9));
            Assert.That(CameraMath.GroundHit(down, Wide, 0, -1, 0, out var gx, out var gz), Is.True);
            Assert.That(gx, Is.EqualTo(0).Within(1e-6)); Assert.That(gz, Is.GreaterThan(0));
            Assert.That(CameraMath.Project(level, Wide, 0, 50, -10, out _, out _), Is.False, "behind the camera");
        }

        [Test]
        public void HomeLandscapeKeepsEveryDeviceButTheTruckInFrameAndTheSkyAboveAFifth()
        {
            var pose = U5Plan.Map.Pose("pose:home");
            var horizon = CameraMath.HorizonFromTop(pose);
            Assert.That(horizon, Is.InRange(.15, .23), "the horizon about a fifth of the way down");
            foreach (var d in Devices.Where(d => d.Kind != DeviceKind.Truck))
            {
                var a = U5Plan.Map.Anchors[d.Token];
                Assert.That(CameraMath.Project(pose, Wide, a.X, Deck + 1, a.Z, out var nx, out var ny), Is.True, d.Token);
                Assert.That(Math.Abs(nx), Is.LessThanOrEqualTo(.94), d.Token + " x");
                Assert.That(Math.Abs(ny), Is.LessThanOrEqualTo(.94), d.Token + " y");
            }
            // the meters are tall buildings: their tops stay in frame too
            foreach (var d in Devices.Where(d => d.Kind == DeviceKind.Meter))
            {
                var a = U5Plan.Map.Anchors[d.Token];
                CameraMath.Project(pose, Wide, a.X, Deck + 20, a.Z, out var nx, out var ny);
                Assert.That(Math.Max(Math.Abs(nx), Math.Abs(ny)), Is.LessThanOrEqualTo(.97), d.Token + " top");
            }
            // both breakwater lighthouses, the mouth and the three quays, with a margin
            foreach (var x in new[] { -39.0, 39.0 })
                foreach (var y in new[] { 3.0, 20.0 })
                {
                    CameraMath.Project(pose, Wide, x, y, -148, out var nx, out var ny);
                    Assert.That(Math.Max(Math.Abs(nx), Math.Abs(ny)), Is.LessThanOrEqualTo(.94), "lighthouse " + x + "," + y);
                }
            foreach (var q in U5Plan.Map.Quays)
            {
                double x0 = q.Value.Min(p => p.X), x1 = q.Value.Max(p => p.X), z0 = q.Value.Min(p => p.Z), z1 = q.Value.Max(p => p.Z);
                var inside = 0;
                for (var i = 0; i <= 8; i++)
                    for (var j = 0; j <= 8; j++)
                    {
                        CameraMath.Project(pose, Wide, x0 + (x1 - x0) * i / 8, Deck, z0 + (z1 - z0) * j / 8, out var nx, out var ny);
                        if (Math.Abs(nx) <= 1 && Math.Abs(ny) <= 1) inside++;
                    }
                Assert.That(inside / 81.0, Is.GreaterThanOrEqualTo(.6), q.Key + " mostly in frame");
            }
            foreach (var c in U5Plan.Map.Mouth)
            {
                CameraMath.Project(pose, Wide, c.X, Deck, c.Z, out var nx, out var ny);
                Assert.That(Math.Max(Math.Abs(nx), Math.Abs(ny)), Is.LessThanOrEqualTo(.94), "mouth corner " + c.X + "," + c.Z);
            }
            // the frame's bottom edge meets the water south of z = -190 but not absurdly far south of the breakwaters
            Assert.That(CameraMath.GroundHit(pose, Wide, 0, -1, 0, out _, out var bottomZ), Is.True);
            Assert.That(bottomZ, Is.LessThanOrEqualTo(-190));
            Assert.That(bottomZ, Is.GreaterThan(-300));
            var water = WaterShare(pose, Wide);
            var sky = SkyShare(pose, Wide, U5Plan.Plan);
            TestContext.WriteLine("HOME 16:9: horizon " + horizon.ToString("P1") + ", sky " + sky.ToString("P1") + ", flat-plane water " + water.ToString("P1") + ", frame bottom z=" + bottomZ.ToString("F0"));
            Assert.That(water, Is.LessThanOrEqualTo(.43), "under the 45 % mask ceiling with room for boats and buoys (the flat estimate runs ~2 points high)");
            Assert.That(sky, Is.InRange(.16, .21), "sky 16-21 % of the frame");
        }

        [Test]
        public void HomePortraitKeepsAll37DevicesInFrameWithSkyAndNoVisibleEdge()
        {
            var pose = U5Plan.Map.Pose("pose:home:portrait");
            foreach (var d in Devices)
            {
                var a = U5Plan.Map.Anchors[d.Token];
                Assert.That(CameraMath.Project(pose, Tall, a.X, Deck + 1, a.Z, out var nx, out var ny), Is.True, d.Token);
                Assert.That(Math.Abs(nx), Is.LessThanOrEqualTo(.94), d.Token + " x");
                Assert.That(Math.Abs(ny), Is.LessThanOrEqualTo(.94), d.Token + " y");
            }
            var sky = SkyShare(pose, Tall, U5Plan.Plan);
            var water = WaterShare(pose, Tall, 72, 128);
            TestContext.WriteLine("HOME 9:16: horizon " + CameraMath.HorizonFromTop(pose).ToString("P1") + ", sky " + sky.ToString("P1") + ", flat-plane water " + water.ToString("P1"));
            Assert.That(sky, Is.InRange(.08, .14), "sky 8-14 % of the tall frame");
            Assert.That(water, Is.LessThanOrEqualTo(.40));
            // the terrain's far edge (x = 2400) must end in haze, not in the frame's air: by then the scenery fog has taken it all
            var noon = DayLighting.Evaluate(12.5, new Weather(0, 0));
            Assert.That(noon.SceneryTransmission(Math.Sqrt(2960.0 * 2960 + 290 * 290), 300, 2.4, 2160), Is.LessThan(.15));
            // and the night and storm homes are the daytime frames
            Assert.That(U5Plan.Map.Pose("pose:night-home"), Is.SameAs(U5Plan.Map.Pose("pose:home")));
            Assert.That(U5Plan.Map.Pose("pose:storm-home"), Is.SameAs(U5Plan.Map.Pose("pose:home")));
        }

        [Test]
        public void EveryStoryPoseNameResolvesAndTheAnchorsDidNotMove()
        {
            foreach (var name in StoryPoses.All()) Assert.That(U5Plan.Map.Pose(name), Is.Not.Null, name);
            Assert.That(StoryPoses.All().Distinct().Count(), Is.EqualTo(StoryPoses.All().Count()));
            Assert.That(StoryPoses.All().Count(), Is.EqualTo(4 + 11 * 6 + 4 + 11 * 2));
            // pinned anchors from the U4 plan (presentation anchors never move)
            Assert.That(U5Plan.Map.Anchors["hp-pump-north"].X, Is.EqualTo(-23.25).Within(1e-9));
            Assert.That(U5Plan.Map.Anchors["hp-pump-north"].Z, Is.EqualTo(103).Within(1e-9));
            Assert.That(U5Plan.Map.Anchors["hp-light-east-3"].X, Is.EqualTo(153).Within(1e-9));
            Assert.That(U5Plan.Map.Anchors["hp-meter-ferry-terminal"].Z, Is.EqualTo(63.6).Within(1e-9));
            Assert.That(U5Plan.Map.Depot.X, Is.EqualTo(181).Within(1e-9));
        }

        // ---------------------------------------------------------------- ground-level poses

        /// <summary>The subject each ground pose is about, for the middle-third and line-of-sight checks.</summary>
        static (Point3 Subject, string Token) SubjectOf(string name)
        {
            var parts = name.Split(':');
            var family = StoryPoses.Families.FirstOrDefault(f => f.Family == parts[1]);
            if (family.Token != null)
            {
                var a = U5Plan.Map.Anchors[family.Token];
                return (new Point3(a.X, Deck + 1.4, a.Z), family.Token);
            }
            var look = U5Plan.Map.Pose(name).Look;
            // the depot and chase views are about the truck, whose own footprint is not "in the way"
            return (new Point3(look.X, Deck + 1.4, look.Z), name == "pose:depot:wide" || name == "pose:truck:chase" ? "hp-truck-1" : null);
        }

        [Test]
        public void GroundPosesPutTheHorizonBetween22And40PercentAndTheSubjectInTheMiddleThird()
        {
            // pins A6: the old poses looked down ~30 degrees and showed no sky at all
            var worst = new List<string>();
            foreach (var name in StoryPoses.GroundLevel())
            {
                var pose = U5Plan.Map.Pose(name);
                var horizon = CameraMath.HorizonFromTop(pose);
                // a lamp pose looks up at the lantern, so its horizon is the one set below the middle; every other pose keeps the upper band
                var lampPose = Math.Abs(horizon - PresentationMap.LampHorizon) < 1e-6;
                if (!lampPose) Assert.That(horizon, Is.InRange(.22, .40), name + " horizon");
                Assert.That(pose.Camera.Y, Is.InRange(5, 11), name + " eye height (world y)");
                var subject = SubjectOf(name).Subject;
                Assert.That(CameraMath.Project(pose, 16.0 / 9, subject.X, subject.Y, subject.Z, out var nx, out var ny), Is.True, name);
                var fromTop = .5 - ny / 2;
                Assert.That(fromTop, Is.InRange(.34, .66), name + " subject row (clear of the HUD's bottom caption band)");
                Assert.That(Math.Abs(nx), Is.LessThan(.35), name + " subject column");
                Assert.That(pose.VerticalFov, Is.InRange(34, 56), name);
            }
            // two examples for the report
            foreach (var name in new[] { "pose:pump-north:kiosk-ext", "pose:east-quay:mouth-wide" })
            {
                var p = U5Plan.Map.Pose(name);
                worst.Add(name + " eye " + p.Camera.Y.ToString("F1") + " horizon " + CameraMath.HorizonFromTop(p).ToString("P1"));
            }
            TestContext.WriteLine(string.Join("; ", worst));
        }

        [Test]
        public void WellPosesKeepTheirDownwardLookIntoTheWetWell()
        {
            foreach (var f in StoryPoses.Families)
                foreach (var s in StoryPoses.WellSuffixes)
                {
                    var p = U5Plan.Map.Pose("pose:" + f.Family + ":" + s);
                    Assert.That(CameraMath.PitchDegrees(p), Is.LessThan(-20), f.Family + ":" + s);
                    Assert.That(p.Look.Y, Is.EqualTo(2));
                    Assert.That(p.VerticalFov, Is.EqualTo(42));
                }
        }

        static double Height(WorldPropPlan plan, PropFootprint f)
        {
            var id = f.Id;
            if (id.StartsWith("device:", StringComparison.Ordinal))
            {
                var token = id.Substring(7); var suffix = "";
                var colon = token.IndexOf(':'); if (colon >= 0) { suffix = token.Substring(colon); token = token.Substring(0, colon); }
                if (suffix == ":well") return 0;
                var device = U5Plan.Layout.Devices.First(d => d.Token == token);
                switch (device.Kind)
                {
                    case DeviceKind.Meter: return 20;
                    case DeviceKind.Pump: return 3.2;
                    case DeviceKind.Truck: return 3;
                    case DeviceKind.Light: return 5;
                    case DeviceKind.Charger: return 2.2;
                    default: return suffix == ":staff" ? 3 : 4.2;
                }
            }
            var prop = plan.Find(id);
            if (prop == null) return 0;
            switch (prop.Kind)
            {
                case "Church": return 25; case "House": return 10.5; case "Commercial": return 12; case "Shopfront": return 9; case "Inn": return 11;
                case "Shed": return 5; case "Gazebo": return 5.8; case "Bench": return .9; case "StreetLamp": return 4.6; case "Bollard": return .6;
                case "Cleat": return .3; case "PierBollard": return 1; case "ParkedCar": case "ChargingCar": return 1.6; case "Depot": return 6;
                case "HauledBoat": return 3; case "Crates": return 1.5; case "Rock": return prop.Height; case "Tree": return prop.Height;
                default: return prop.Height;
            }
        }

        static bool Inside(PropFootprint f, double x, double z, double margin)
        {
            var a = f.Yaw * Math.PI / 180; var dx = x - f.At.X; var dz = z - f.At.Z;
            var lx = dx * Math.Cos(a) - dz * Math.Sin(a); var lz = dx * Math.Sin(a) + dz * Math.Cos(a);
            return Math.Abs(lx) <= f.Width / 2 + margin && Math.Abs(lz) <= f.Depth / 2 + margin;
        }

        /// <summary>
        /// The first thing standing in the line of sight from a pose's camera to its subject, or null: a reserved footprint (with half a metre
        /// of margin) taller than the sight line at that point.
        /// </summary>
        public static string Blocker(WorldPropPlan plan, CameraPose pose, Point3 subject, string excludeToken, double margin = .5)
        {
            double ax = pose.Camera.X, az = pose.Camera.Z, bx = subject.X, bz = subject.Z;
            var length = Math.Sqrt((bx - ax) * (bx - ax) + (bz - az) * (bz - az));
            for (var s = 1.0; s < length - 1.5; s += .5)
            {
                var u = s / length; var x = ax + (bx - ax) * u; var z = az + (bz - az) * u;
                var sight = pose.Camera.Y + (subject.Y - pose.Camera.Y) * u;
                foreach (var f in plan.Registry.Entries)
                {
                    if (excludeToken != null && f.Id.StartsWith("device:" + excludeToken, StringComparison.Ordinal)) continue;
                    if (!Inside(f, x, z, margin)) continue;
                    var ground = CoastalTerrain.Height(x, z);
                    if (ground + Height(plan, f) > sight) return f.Id + " (" + (ground + Height(plan, f)).ToString("F1") + " m) over a sight line at " + sight.ToString("F1") + " m";
                }
            }
            return null;
        }

        [Test]
        public void NothingTallerThanTheSightLineStandsBetweenAGroundPoseAndItsSubject()
        {
            var plan = U5Plan.Plan;
            var failures = new List<string>();
            foreach (var name in StoryPoses.GroundLevel())
            {
                var subject = SubjectOf(name);
                var b = Blocker(plan, U5Plan.Map.Pose(name), subject.Subject, subject.Token);
                if (b != null) failures.Add(name + ": " + b);
            }
            Assert.That(failures, Is.Empty, string.Join("\n", failures));
            // no tree (the forest keeps its crowns out of the registry) crosses any pose's line of sight either
            foreach (var name in StoryPoses.All())
            {
                var raw = U5Plan.Map.Pose(name);
                var pose = new CameraPose(raw.Camera, new Point3(raw.Look.X, Math.Max(raw.Look.Y, Deck + 1.4), raw.Look.Z), raw.VerticalFov);
                foreach (var t in plan.Props.Where(p => p.Kind == "Tree"))
                {
                    var g = CoastalTerrain.Height(t.At.X, t.At.Z);
                    Assert.That(StoryPoses.Blocks(pose, t.At.X, t.At.Z, g, g + t.Height, t.Height * .45, 0), Is.False, name + " blocked by a tree at " + t.At.X.ToString("F0") + "," + t.At.Z.ToString("F0"));
                }
            }
        }

        [Test]
        public void ThePlantedBlockerIsFoundByTheLineOfSightCheck()
        {
            // negative control: a lamp-height post planted on the line must be reported, so the check above cannot pass vacuously
            var name = "pose:pump-north:kiosk-ext";
            var pose = U5Plan.Map.Pose(name);
            var mid = new Ground((pose.Camera.X + pose.Look.X) / 2, (pose.Camera.Z + pose.Look.Z) / 2);
            var copy = WorldPropPlan.Generate(U5Plan.Map, U5Plan.Layout);
            Assert.That(Blocker(copy, pose, SubjectOf(name).Subject, "hp-pump-north"), Is.Null);
            // the same call with the subject's own footprints NOT excluded must find the pump standing on its own line
            Assert.That(Blocker(copy, new CameraPose(pose.Camera, new Point3(pose.Look.X, Deck + 1.4, pose.Look.Z + 12), pose.VerticalFov), new Point3(pose.Look.X, Deck + 1.4, pose.Look.Z + 12), null), Is.Not.Null);
            Assert.That(StoryPoses.Blocks(pose, mid.X, mid.Z, Deck, Deck + 9, 1), Is.True);
            Assert.That(StoryPoses.Blocks(pose, mid.X + 20, mid.Z, Deck, Deck + 9, 1), Is.False);
            Assert.That(StoryPoses.Blocks(pose, mid.X, mid.Z, Deck, Deck + .5, 1), Is.False, "a kerb-height obstacle is below the sight line");
        }

        [Test]
        public void LampPosesKeepTheWholeLampHeadIncludedInTheUpperMiddleOfTheFrame()
        {
            // the lantern (8.3 m above the deck, as LampBuilder builds it) is the story: it must be in frame, upper-middle, for every pose about a lamp
            var lamps = StoryPoses.Families.Where(f => U5Plan.Layout.Devices.First(d => d.Token == f.Token).Kind == DeviceKind.Light).ToList();
            Assert.That(lamps.Count, Is.EqualTo(2));
            foreach (var f in lamps)
                foreach (var s in StoryPoses.GroundSuffixes)
                {
                    var name = "pose:" + f.Family + ":" + s;
                    var pose = U5Plan.Map.Pose(name); var a = U5Plan.Map.Anchors[f.Token];
                    Assert.That(CameraMath.Project(pose, Wide, a.X, Deck + 8.3, a.Z, out var nx, out var ny), Is.True, name);
                    Assert.That(Math.Abs(nx), Is.LessThanOrEqualTo(.35), name + " head column");
                    Assert.That(ny, Is.InRange(.2, .7), name + " head row (upper middle)");
                    CameraMath.Project(pose, Wide, a.X, Deck, a.Z, out _, out var baseY);
                    Assert.That(baseY, Is.GreaterThan(-.9), name + " the lamp's foot is in frame too");
                    var dist = Math.Sqrt((pose.Camera.X - a.X) * (pose.Camera.X - a.X) + (pose.Camera.Z - a.Z) * (pose.Camera.Z - a.Z));
                    Assert.That(dist, Is.InRange(19.99, 24.01), name + " stand-off");
                    Assert.That(pose.Camera.Y, Is.InRange(5, 6.01), name + " eye");
                    // swung 25-35 degrees round from the quay's axis (the water-side direction)
                    var quay = U5Plan.Map.QuayFor(U5Plan.Layout.Devices.First(d => d.Token == f.Token));
                    double wx = quay == "hp-east-quay" ? -1 : quay == "hp-basin" ? 1 : 0, wz = quay == "hp-north-quay" ? -1 : 0;
                    var angle = Math.Acos(((pose.Camera.X - a.X) * wx + (pose.Camera.Z - a.Z) * wz) / dist) * 180 / Math.PI;
                    Assert.That(angle, Is.InRange(24.9, 35.1), name + " angle off the quay axis");
                }
        }

        [Test]
        public void MouthWideKeepsBreakwatersAndLighthousesOutOfTheLineOfSight()
        {
            var name = "pose:east-quay:mouth-wide";
            var pose = U5Plan.Map.Pose(name);
            Assert.That(pose.Camera.Y, Is.InRange(9, 10));
            double ax = pose.Camera.X, az = pose.Camera.Z, bx = pose.Look.X, bz = pose.Look.Z;
            double Distance(double px, double pz)
            {
                var dx = bx - ax; var dz = bz - az; var u = Math.Max(0, Math.Min(1, ((px - ax) * dx + (pz - az) * dz) / (dx * dx + dz * dz)));
                return Math.Sqrt(Math.Pow(ax + u * dx - px, 2) + Math.Pow(az + u * dz - pz, 2));
            }
            foreach (var lighthouse in new[] { new Ground(-39, -148), new Ground(39, -148) })
                Assert.That(Distance(lighthouse.X, lighthouse.Z), Is.GreaterThanOrEqualTo(80), "lighthouse near the view axis");
            foreach (var wall in new[] { U5Plan.Map.WestBreakwater, U5Plan.Map.EastBreakwater })
                foreach (var corner in wall) Assert.That(Distance(corner.X, corner.Z), Is.GreaterThanOrEqualTo(80), "breakwater near the view axis");
            // and the camera stands over the harbour's own water, west of the East Quay
            Assert.That(Water(pose.Camera.X, pose.Camera.Z) && pose.Camera.X < 140 && pose.Camera.Z > -140, Is.True);
            // negative control: the U5 round-1 camera (30, -180) had a lighthouse within 80 m of its axis
            var old = new CameraPose(new Point3(30, 10, -180), new Point3(175, 0, 20), 44);
            ax = old.Camera.X; az = old.Camera.Z; bx = old.Look.X; bz = old.Look.Z;
            Assert.That(Distance(39, -148), Is.LessThan(80));
        }

        [Test]
        public void MeadowsAndLargeOutcropsAreOnScreenAtHomeBehindTheTown()
        {
            // pins the visible-bare-ground direction: from HOME 16:9, between 60 and 500 m beyond the town, at least four meadows of 70 m or
            // more and five granite ledges of 15 m or more are inside the frame, and two meadows reach the town's edge
            var plan = U5Plan.Plan; var pose = U5Plan.Map.Pose("pose:home");
            var lots = plan.Props.Where(p => p.Footprint.Id.StartsWith("lot:", StringComparison.Ordinal)).Select(p => p.At).ToList();
            double ToTown(double x, double z) => lots.Min(l => SceneryStreet.Distance(l, new Ground(x, z)));
            bool InFrame(double x, double z)
            {
                return CameraMath.Project(pose, Wide, x, plan.Terrain.MeshHeight(x, z), z, out var nx, out var ny) && Math.Abs(nx) <= .95 && Math.Abs(ny) <= .95;
            }
            var meadows = 0; var touching = 0;
            for (var i = 0; i < CoastalTerrain.MeadowCount; i++)
            {
                var m = CoastalTerrain.Meadow(i);
                var d = ToTown(m.X, m.Z); TestContext.WriteLine("meadow " + i + " r=" + m.R + " nearest lot " + d.ToString("F0"));
                if (m.R >= 70 && d >= 60 && d <= 500 && InFrame(m.X, m.Z) && CoastalTerrain.ClearingWeight(m.X, m.Z) > .9) { meadows++; if (d <= m.R + 15) touching++; }
            }
            var ledges = plan.Outcrops.Count(o => o.PlanSize >= 15 && ToTown(o.Center.X, o.Center.Z) is var d && d >= 60 && d <= 500 && InFrame(o.Center.X, o.Center.Z));
            TestContext.WriteLine("Visible from HOME: " + meadows + " meadows (" + touching + " touching the town), " + ledges + " ledges of 15 m or more");
            Assert.That(meadows, Is.GreaterThanOrEqualTo(4));
            Assert.That(touching, Is.GreaterThanOrEqualTo(2));
            Assert.That(ledges, Is.GreaterThanOrEqualTo(5));
            // their dry grass is clearly lighter than the forest floor, by the terrain colours' own luminance (meadow straw .60,.56,.36 vs floor .20,.23,.14)
            Assert.That(CoastalTerrain.ForestCover(CoastalTerrain.Meadow(0).X, CoastalTerrain.Meadow(0).Z), Is.LessThan(.05));
        }
    }
}
