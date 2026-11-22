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
    /// <summary>
    /// U9: the wet wells' view in a 9:16 frame, the derate page's wide view past the ferry, the page's end on a day with no link, the two
    /// landmark tors and their joints, and the squalls kept to an eye that stands high.
    /// </summary>
    public class U9Tests
    {
        const double Deck = SitePlan.DeckHeightM;
        const double Wide = 16.0 / 9, Tall = 9.0 / 16;

        static IEnumerable<(string Family, string Token)> Pumps => StoryPoses.Families.Where(f => f.Token.StartsWith("hp-pump-", StringComparison.Ordinal));
        static Ground Well(string token) => U5Plan.Plan.Registry.Entries.Single(e => e.Id == "device:" + token + ":well").At;

        static bool TagsDrawn(CameraPose p, Ground well, bool tallFrame)
        {
            double fx = p.Look.X - p.Camera.X, fy = p.Look.Y - p.Camera.Y, fz = p.Look.Z - p.Camera.Z; var n = Math.Sqrt(fx * fx + fy * fy + fz * fz);
            return WellFootprint.TagsInView(well.X - p.Camera.X, Deck - p.Camera.Y, well.Z - p.Camera.Z, fx / n, fy / n, fz / n, tallFrame);
        }

        // ---------------------------------------------------------------- the wet wells' view in a 9:16 frame

        [Test]
        public void InATallFrameTheWellAndItsTagsStandBetweenTheCaptionAndTheRail()
        {
            // PumpStationBuilder: the well's floor is 4.4 m under the deck; a tag is text 2.5 m in front of the well's middle, 8 cm over its
            // ring, a line 0.4 m high. The attribute rule's tag hangs 0.8 m under its ring, which stands at or near one of the three fixed
            // levels, and is 6.9 m of text centred 1.9 m to one side: its far end is 5.4 m from the well's middle.
            const double floor = Deck - 4.4, tagFront = 2.5, halfLine = .2, attributeDrop = .8, attributeReach = 1.9 + 3.5;
            foreach (var f in Pumps)
                foreach (var suffix in StoryPoses.WellSuffixes)
                {
                    var name = "pose:" + f.Family + ":" + suffix; var well = Well(f.Token);
                    var wide = U5Plan.Map.Pose(name); var tall = U5Plan.Map.Tall(wide);
                    Assert.That(tall, Is.SameAs(U5Plan.Map.Pose(name + StoryPoses.TallSuffix)), name);
                    Assert.That(tall, Is.Not.SameAs(wide), name);
                    double dx = wide.Camera.X - well.X, dz = wide.Camera.Z - well.Z; var n = Math.Sqrt(dx * dx + dz * dz); dx /= n; dz /= n;
                    // the same line of sight, from twice as far: a 9:16 frame is 1.78 times as many pixels high, so the text is 0.89 of the size
                    Assert.That(tall.Camera.X - well.X, Is.EqualTo(dx * PresentationMap.WellViewBackM * PresentationMap.WellViewTallScale).Within(1e-9));
                    Assert.That(tall.Camera.Z - well.Z, Is.EqualTo(dz * PresentationMap.WellViewBackM * PresentationMap.WellViewTallScale).Within(1e-9));
                    Assert.That(tall.Camera.Y, Is.EqualTo(PresentationMap.WellViewEyeM * PresentationMap.WellViewTallScale).Within(1e-9));
                    Assert.That((16.0 / 9) / PresentationMap.WellViewTallScale, Is.InRange(.75, 1.0), "the text is not much smaller than in a 16:9 frame of the same width");

                    double FromBottom(CameraPose pose, double level, double side, out double nx)
                    {
                        Assert.That(CameraMath.Project(pose, Tall, well.X + dx * tagFront - dz * side, floor + level, well.Z + dz * tagFront + dx * side, out nx, out var ny), Is.True);
                        return (ny + 1) / 2;
                    }
                    foreach (var level in new[] { PumpPresentation.LowCutoffM, PumpPresentation.HighMarkerM, PumpPresentation.FloatMarkerM })
                    {
                        Assert.That(FromBottom(tall, level + .08 - halfLine, 0, out _), Is.GreaterThan(HudModel.CaptionTopTall + .01), name + ": the tag at " + level + " m is over the caption's box");
                        Assert.That(FromBottom(tall, level + .08 + halfLine, 0, out _), Is.LessThan(HudModel.RailBottomTall - .01), name + ": the tag at " + level + " m is under the rail");
                        // the attribute rule's tag, hanging under a ring at this level, both of its ends
                        foreach (var side in new[] { -attributeReach, attributeReach })
                        {
                            Assert.That(FromBottom(tall, level - attributeDrop + .08 - halfLine, side, out var nx), Is.GreaterThan(HudModel.CaptionTopTall + .01), name + ": the attribute rule's tag under " + level + " m");
                            Assert.That(Math.Abs(nx), Is.LessThan(.9), name + ": the attribute rule's tag runs off the frame");
                        }
                    }
                    // the well's far rim: all but its last two hundredths are under the rail's lowest node
                    Assert.That(CameraMath.Project(tall, Tall, well.X - dx * WellFootprint.RadiusM, Deck, well.Z - dz * WellFootprint.RadiusM, out _, out var rim), Is.True);
                    Assert.That((rim + 1) / 2, Is.LessThan(HudModel.RailBottomTall + .02), name + ": the far rim");
                    // and the tags are drawn from there, which the 16:9 range would not do
                    Assert.That(TagsDrawn(tall, well, true), Is.True, name + " draws the tags in a tall frame");
                    Assert.That(TagsDrawn(tall, well, false), Is.False, "the tall view stands beyond the 16:9 range");
                    Assert.That(TagsDrawn(wide, well, false), Is.True, name + " still draws them in a 16:9 frame");

                    // the same checks see what U8 showed in a 9:16 frame from the 16:9 eye: "low cut-off" inside the caption's box, the attribute
                    // rule's tag off the frame's edge, the far rim behind the rail
                    Assert.That(FromBottom(wide, PumpPresentation.LowCutoffM + .08, 0, out _), Is.InRange(HudModel.CaptionBottomTall, HudModel.CaptionTopTall), "the check sees U8's overlap");
                    FromBottom(wide, PumpPresentation.LowCutoffM - attributeDrop + .08, attributeReach, out var off);
                    Assert.That(Math.Abs(off), Is.GreaterThan(1), "the check sees U8's cut-off tag");
                    Assert.That(CameraMath.Project(wide, Tall, well.X - dx * WellFootprint.RadiusM, Deck, well.Z - dz * WellFootprint.RadiusM, out _, out var wideRim), Is.True);
                    Assert.That((wideRim + 1) / 2, Is.GreaterThan(HudModel.RailBottomTall + .05), "the check sees U8's well behind the rail");
                }
            // the HUD's own numbers: the rail's lowest node in a tall frame ends where the free band begins
            Assert.That(HudModel.RailBottomTall, Is.EqualTo(.598).Within(1e-6));
            Assert.That(HudModel.CaptionTopTall, Is.LessThan(HudModel.RailBottomTall - .2), "there is a band to stand in");
        }

        [Test]
        public void OnlyAWellsViewHasATallVersionAndOnlyItDrawsTheTagsInATallFrame()
        {
            var map = U5Plan.Map;
            // every other pose is its own tall version (the 9:16 HOME is a pose of its own, chosen by the camera, not a version of the 16:9 one)
            foreach (var name in StoryPoses.All().Where(n => !StoryPoses.WellSuffixes.Any(s => n.EndsWith(":" + s, StringComparison.Ordinal))))
                Assert.That(map.Tall(map.Pose(name)), Is.SameAs(map.Pose(name)), name);
            Assert.That(map.Tall(null), Is.Null);
            // the lamp, gauge, charger and overview poses near a well never draw its tags in a tall frame either, at the tall range
            var wells = Pumps.Select(f => Well(f.Token)).ToList(); var checkedPoses = 0;
            foreach (var name in StoryPoses.GroundLevel().Concat(StoryPoses.Home))
            {
                if (Pumps.Any(f => name.StartsWith("pose:" + f.Family + ":", StringComparison.Ordinal))) continue;
                checkedPoses++;
                foreach (var w in wells) Assert.That(TagsDrawn(map.Pose(name), w, true), Is.False, name);
            }
            Assert.That(checkedPoses, Is.GreaterThan(50));
            // the longer range is for a camera that looks down into the well: the same eye, the same distance, looking nearly level, has the 16:9 range
            Assert.That(WellFootprint.TagsInView(0, -18, 32, 0, -.49, .872, true), Is.True);
            Assert.That(WellFootprint.TagsInView(0, -18, 32, 0, -.49, .872), Is.False, "a 16:9 frame from there");
            Assert.That(WellFootprint.TagsInView(0, -5, 36, 0, -.14, .99, true), Is.False, "a ground-level view of something beside the well");
            Assert.That(WellFootprint.TagsInView(0, -5, 20, 0, -.14, .99, true), Is.True, "inside the 16:9 range it is drawn as before");
            // the range itself: the tall view's eye with room to spare, and nothing like the whole quay
            var eye = Math.Sqrt(Math.Pow(PresentationMap.WellViewBackM, 2) + Math.Pow(PresentationMap.WellViewEyeM - Deck, 2)) * PresentationMap.WellViewTallScale;
            Assert.That(WellFootprint.TagRangeTallM, Is.InRange(eye + 2, eye + 10));
        }

        // ---------------------------------------------------------------- the derate page's wide view

        /// <summary>
        /// A lattice of points through a box lying along the Basin's wall, its middle <paramref name="off"/> metres out from the wall and
        /// <paramref name="length"/> along it: its corners, and its faces and inside every three metres or so (an eye that stands over the
        /// box has none of its corners in the frame and its roof across the frame's foot).
        /// </summary>
        static IEnumerable<Point3> BerthBox(double off, double length, double width, double y0, double y1)
        {
            var wall = U5Plan.Map.Quays["hp-basin"].Max(p => p.X); var along = U5Plan.Map.Anchors["hp-meter-ferry-terminal"].Z;
            for (var i = 0; i <= 3; i++)
                for (var j = 0; j <= 12; j++)
                    for (var k = 0; k <= 3; k++)
                        yield return new Point3(wall + off + width * (i / 3.0 - .5), y0 + (y1 - y0) * k / 3.0, along + length * (j / 12.0 - .5));
        }

        static bool InFrame(CameraPose pose, Point3 p, out double nx, out double ny) =>
            CameraMath.Project(pose, Wide, p.X, p.Y, p.Z, out nx, out ny) && Math.Abs(nx) < 1 && Math.Abs(ny) < 1;

        [Test]
        public void TheDeratePagesWideViewLooksPastTheFerry()
        {
            var map = U5Plan.Map; var wide = map.Pose("pose:chargers-basin:wide"); var charger = map.Anchors["hp-charger-basin-2"];
            // the ferry, hull to mast head: no part of it is in the frame
            var ferry = BerthBox(NearLandmarks.FerryOffM, NearLandmarks.FerryLengthM, NearLandmarks.FerryBeamM, 0, NearLandmarks.FerryMastTopM).ToArray();
            foreach (var p in ferry) Assert.That(InFrame(wide, p, out _, out _), Is.False, "the ferry is in the wide view");
            // the pontoon may show, to the right of both chargers and never in front of them
            foreach (var p in BerthBox(NearLandmarks.PontoonOffM, NearLandmarks.PontoonLengthM, NearLandmarks.PontoonWidthM, 0, Deck + 1.2))
                if (InFrame(wide, p, out var nx, out _)) Assert.That(nx, Is.GreaterThan(.3), "the pontoon is in front of the chargers");
            // both chargers stand in the middle of the frame, the one the page is about nearest its centre
            var at = new Dictionary<string, double>();
            foreach (var token in new[] { "hp-charger-basin-1", "hp-charger-basin-2" })
            {
                var c = map.Anchors[token];
                Assert.That(InFrame(wide, new Point3(c.X, Deck + 1, c.Z), out var nx, out var ny), Is.True, token);
                Assert.That(Math.Abs(nx), Is.LessThan(.4), token); Assert.That(ny, Is.InRange(-.3, .4), token);
                at[token] = nx;
            }
            Assert.That(Math.Abs(at["hp-charger-basin-2"]), Is.LessThan(.1));
            // nothing that stands on the quay's edge is in front of either: the gauges' masts and staffs and the lamps
            foreach (var e in U5Plan.Plan.Registry.Entries.Where(e => e.Id.StartsWith("device:hp-gauge-basin", StringComparison.Ordinal) || e.Id.StartsWith("device:hp-light-basin", StringComparison.Ordinal) ||
                e.Id.StartsWith("device:hp-lora-gauge-basin", StringComparison.Ordinal) || e.Id.StartsWith("device:hp-legacy-gauge-basin", StringComparison.Ordinal)))
            {
                if (!InFrame(wide, new Point3(e.At.X, Deck + 1, e.At.Z), out var nx, out _)) continue;
                // a charger and its car are under 4 m wide, 55 m off: a twentieth of the frame's half-width
                foreach (var c in at) Assert.That(Math.Abs(nx - c.Value), Is.GreaterThan(.07), e.Id + " stands in front of " + c.Key);
            }
            // the eye is over open water, south of the pontoon's end, as far off the wall as before
            var wallX = map.Quays["hp-basin"].Max(p => p.X);
            Assert.That(wide.Camera.X - charger.X, Is.EqualTo(52).Within(1e-9));
            Assert.That(wide.Camera.X, Is.GreaterThan(wallX + 20));
            Assert.That(wide.Camera.Z, Is.LessThan(map.Anchors["hp-meter-ferry-terminal"].Z - NearLandmarks.PontoonLengthM / 2 - 5));
            // the same check sees U8's view: its eye stood 14 m north of the chargers' line, over the ferry, whose cabin and mast were in the lower frame
            var u8 = new CameraPose(new Point3(wide.Camera.X, wide.Camera.Y, charger.Z + 14), wide.Look, wide.VerticalFov);
            Assert.That(ferry.Any(p => InFrame(u8, p, out _, out var ny) && ny < 0), Is.True, "the check sees U8's ferry");
            Assert.That(u8.Camera.Z, Is.InRange(map.Anchors["hp-meter-ferry-terminal"].Z - NearLandmarks.FerryLengthM / 2, map.Anchors["hp-meter-ferry-terminal"].Z + NearLandmarks.FerryLengthM / 2));
            // the page's other two views keep the step that clears the gauge's mast (U8)
            foreach (var s in new[] { "close", "insert" }) Assert.That(map.Pose("pose:chargers-basin:" + s).Camera.Z, Is.EqualTo(charger.Z + 14).Within(1e-9), s);
        }

        // ---------------------------------------------------------------- the derate page's end on a day with no link

        static (double At, string Value)[] Events(Scheduler s, string kind) => s.Log.Where(e => e.Page == "3b" && e.Kind == kind).Select(e => (e.ShowS, e.Value.Replace("\n", " "))).ToArray();

        [TestCase("command-cause-not-read")]
        [TestCase("refuse-derate")]
        public void ADayWithNoLinkLeavesTheDeratePageWhenACleanDayDoes(string control)
        {
            var clean = DirectorTimeline.Run(); var run = DirectorTimeline.Run(control);
            // the cuts are the clean day's, to the second
            Assert.That(Events(run, "shot"), Is.EqualTo(Events(clean, "shot")));
            Assert.That(Events(run, "shot").Select(e => e.Value), Is.EqualTo(new[] { "3b-1", "3b-2", "3b-3" }));
            // and so is the page's end: the last shot stood 15 s longer on "Alarm raised.", until the first shot's offer of the derate caption ran out
            var end = Events(run, "caption-end").Last(); var last = Events(run, "shot").Last();
            Assert.That(end.At, Is.EqualTo(Events(clean, "caption-end").Last().At), "the page ends when a clean day's does");
            Assert.That(end.Value, Is.EqualTo("hot-only"));
            var story = DirectorKit.Story().Pages.Single(p => p.Id == "3b"); var first = story.Shots[0]; var third = story.Shots[2];
            Assert.That(end.At - last.At, Is.EqualTo(third.HoldMinS).Within(1e-9), "the last shot's own minimum");
            Assert.That(Events(run, "shot").First().At + first.HoldMaxS, Is.GreaterThan(end.At + 10), "the first shot's offer is still open when the page ends");
            // the caption's words are what they were, and it stood for its hold
            var captions = Events(run, "caption");
            Assert.That(captions.Select(c => c.Value).Distinct().Single(), Is.EqualTo("Running hot. Alarm raised."));
            Assert.That(end.At - captions.Last().At, Is.GreaterThanOrEqualTo(3));
            // nothing outside page 3b moved, but for the page-4 node the cause control declares
            string[] Rest(Scheduler s) => s.Log.Where(e => e.Page != "3b" && e.Page != "4" && new[] { "shot", "caption", "caption-end", "node", "link" }.Contains(e.Kind))
                .Select(e => e.ShowS.ToString("R", System.Globalization.CultureInfo.InvariantCulture) + "|" + e.Page + "|" + e.Kind + "|" + e.Value).ToArray();
            Assert.That(Rest(run), Is.EqualTo(Rest(clean)));
        }

        [Test]
        public void AnOfferFromAnEarlierShotIsStillTakenInTheLastOne()
        {
            // A clean day whose derate command is read a minute late, in the page's last shot, after "Alarm raised." went up: the first
            // shot's offer is still open, and the caption is shown. The offer no longer holds the page open; it is still an offer.
            var layout = Kit.Layout(); var source = DirectorTimeline.Clean("", layout); var late = new ObservationStore();
            var shownAt = double.NaN;
            var s = new Scheduler(Storyboard.Parse(DirectorKit.StoryText(), new PresentationMap(layout)), layout);
            for (var wall = 0; wall <= 600; wall++)
            {
                var sim = 6 + wall * 24.0 / layout.DayLengthSeconds;
                foreach (var o in source.Records.Where(o => o.ObservedAtS <= (o.Kind == "command" && o.Key == "derate" ? wall - 62 : wall))) late.Add(o);
                foreach (var stream in new[] { "alarms", "notifications", "credentials", "pump", "measurements" }) late.AddCoverage(new CoverageInterval("run", stream, 6, sim, false));
                foreach (var e in s.Tick(new DirectorSample("run", sim, wall), late))
                    if (e.Page == "3b" && e.Kind == "caption" && e.Value.Contains("derate")) shownAt = e.ShowS;
            }
            var shots = Events(s, "shot");
            Assert.That(shots.Select(e => e.Value), Is.EqualTo(new[] { "3b-1", "3b-2", "3b-3" }));
            Assert.That(shownAt, Is.GreaterThan(shots.Last().At), "the derate caption is shown in the last shot");
            Assert.That(shownAt, Is.LessThan(shots.First().At + 80), "inside the first shot's offer");
            Assert.That(Events(s, "caption").Select(c => c.Value), Does.Contain("Running hot. Alarm raised."));
        }

        // ---------------------------------------------------------------- the two tors

        [Test]
        public void TheWestTorStandsWhereTheTallHomeLooksAndTheEastTorWhereTheWideOneDoes()
        {
            var plan = U5Plan.Plan; var east = plan.EastTor; var west = plan.WestTor;
            Assert.That(east, Is.Not.Null); Assert.That(west, Is.Not.Null);
            Assert.That(plan.Outcrops.Where(o => o.Landmark), Is.EquivalentTo(new[] { east, west }));
            Assert.That(west.LandmarkRise, Is.EqualTo(Outcrop.WestTorRiseM)); Assert.That(east.LandmarkRise, Is.EqualTo(Outcrop.LandmarkRiseM));
            var westRise = west.Peak - west.GroundAtCentre;
            Assert.That(westRise, Is.InRange(18, 26)); Assert.That(westRise, Is.LessThan(east.Peak - east.GroundAtCentre - 2), "the lesser of the two");
            Assert.That(plan.Outcrops.Where(o => !o.Landmark).Max(o => o.Peak - o.GroundAtCentre), Is.LessThan(westRise / 2), "every other tor is a few metres of rock");

            // in the 9:16 HOME: well inside the frame's sides, under the caption's box and over the disclosure line, 90 pixels or more of 1920
            var tall = U5Plan.Map.Pose("pose:home:portrait");
            Assert.That(CameraMath.Project(tall, Tall, west.Center.X, west.Peak, west.Center.Z, out var nx, out var top), Is.True);
            Assert.That(CameraMath.Project(tall, Tall, west.Center.X, west.GroundAtCentre, west.Center.Z, out _, out var foot), Is.True);
            Assert.That(Math.Abs(nx), Is.LessThan(.6));
            Assert.That((top + 1) / 2, Is.LessThan(HudModel.CaptionBottomTall - .01), "the tor's head is under the caption's box");
            Assert.That((foot + 1) / 2, Is.GreaterThan(.08), "its foot is over the disclosure line");
            var pixels = (top - foot) * 960;
            Assert.That(pixels, Is.GreaterThan(90));
            TestContext.WriteLine("west tor at " + west.Center.X.ToString("F0") + ", " + west.Center.Z.ToString("F0") + ": " + westRise.ToString("F1") + " m, " + pixels.ToString("F0") + " px of 1920, " + west.Blocks.Length + " blocks");
            // the east tor is outside that frame and the west tor outside the 16:9 HOME's: each frame has one
            Assert.That(CameraMath.Project(tall, Tall, east.Center.X, east.Peak, east.Center.Z, out var ex, out _) && Math.Abs(ex) < 1, Is.False);
            var wide = U5Plan.Map.Pose("pose:home");
            Assert.That(CameraMath.Project(wide, Wide, west.Center.X, west.Peak, west.Center.Z, out var wx, out _) && Math.Abs(wx) < 1, Is.False);

            // its long axis lies across the tall HOME's line of sight, as the east tor's lies across the wide one's
            Assert.That(Math.Abs(Math.Cos(west.YawDeg * Math.PI / 180)), Is.LessThan(.35)); Assert.That(Math.Abs(Math.Sin(east.YawDeg * Math.PI / 180)), Is.LessThan(.35));
            // no tree in its ledge, a few low ledges at its foot, and no street, lot or device under it
            foreach (var p in plan.Props.Where(p => p.Kind == "Tree"))
                Assert.That(Math.Sqrt(Math.Pow(p.At.X - west.Center.X, 2) + Math.Pow(p.At.Z - west.Center.Z, 2)), Is.GreaterThan(Outcrop.LandmarkHalfZ), "a tree in the west tor");
            Assert.That(plan.Outcrops.Count(o => !o.Landmark && Math.Sqrt(Math.Pow(o.Center.X - west.Center.X, 2) + Math.Pow(o.Center.Z - west.Center.Z, 2)) < 60), Is.GreaterThanOrEqualTo(2));
            var ledge = plan.Registry.Entries.Single(e => e.Id == "rock:landmark-west");
            foreach (var e in plan.Registry.Entries.Where(e => e.Id != ledge.Id && !e.Id.StartsWith("rock:", StringComparison.Ordinal)))
                Assert.That(ledge.Intersects(e, 0), Is.False, e.Id + " under the west tor");
            foreach (var a in U5Plan.Map.Anchors.Values) Assert.That(Math.Sqrt(Math.Pow(a.X - west.Center.X, 2) + Math.Pow(a.Z - west.Center.Z, 2)), Is.GreaterThan(100));
        }

        [Test]
        public void TheTwoTorsAreSistersAndNotCopies()
        {
            var east = U5Plan.Plan.EastTor; var west = U5Plan.Plan.WestTor;
            Assert.That(west.Blocks.Length, Is.EqualTo(east.Blocks.Length), "the same stacks and clitter");
            // each tor's main stack in its own ledge's frame: on opposite sides of the ledge's middle, the west one lower
            double Along(Outcrop o, Point3[] b)
            {
                var a = o.YawDeg * Math.PI / 180; double cx = b.Take(4).Average(p => p.X) - o.Center.X, cz = b.Take(4).Average(p => p.Z) - o.Center.Z;
                return cx * Math.Cos(a) - cz * Math.Sin(a);
            }
            Assert.That(Along(east, east.Blocks[0]) * Along(west, west.Blocks[0]), Is.LessThan(0), "the west tor is laid out the other way round");
            Assert.That(Along(east, east.Blocks[6]) * Along(west, west.Blocks[6]), Is.LessThan(0));
            double Span(Point3[] b) => Enumerable.Range(0, 4).Max(i => Math.Sqrt(Math.Pow(b[i].X - b[(i + 2) % 4].X, 2) + Math.Pow(b[i].Z - b[(i + 2) % 4].Z, 2)));
            Assert.That(Span(west.Blocks[0]), Is.InRange(Span(east.Blocks[0]) * .8, Span(east.Blocks[0]) * .99), "a little narrower at its foot");
            // the west tor is a stack like the east one: each slab of its main stack higher, sunk into the one under it, never a pillar to the ground
            var grid = U5Plan.Plan.Terrain;
            bool Grounded(Point3[] b) => Enumerable.Range(0, 4).All(i => b[i].Y <= grid.MeshHeight(b[i].X, b[i].Z) - Outcrop.BlockSinkM + 1e-9);
            Assert.That(Grounded(west.Blocks[0]), Is.True);
            for (var n = 1; n < 6; n++)
            {
                Assert.That(west.BlockFoot[n], Is.GreaterThan(west.BlockFoot[n - 1] + 1.2), "slab " + n);
                Assert.That(west.Blocks[n][0].Y, Is.LessThan(Enumerable.Range(4, 4).Min(i => west.Blocks[n - 1][i].Y) - .2), "slab " + n + " is sunk into the one under it");
                Assert.That(Grounded(west.Blocks[n]), Is.False, "slab " + n);
                Assert.That(Span(west.Blocks[n]), Is.LessThan(Span(west.Blocks[n - 1])), "slab " + n + " is narrower");
            }
            for (var n = 6; n < west.Blocks.Length; n++)
                Assert.That(Grounded(west.Blocks[n]) || west.Blocks[n][0].Y < Enumerable.Range(4, 4).Min(i => west.Blocks[n - 1][i].Y) - .2, Is.True, "block " + n);
            // the same seed, the same tor
            var again = WorldPropPlan.Generate(U5Plan.Map, U5Plan.Layout).WestTor;
            Assert.That(again.Blocks.Sum(b => b.Sum(p => p.X + p.Y + p.Z)), Is.EqualTo(west.Blocks.Sum(b => b.Sum(p => p.X + p.Y + p.Z))));
        }

        [Test]
        public void ASlabsJointIsAShadowAndNotHalfTheSlab()
        {
            // U8: the lower 42 % of what shows of a slab at 0.52 of its tone and the rest at 0.98, so a stack of six was six dark bands and six
            // pale ones. The joint is a sixth of the slab now and the wall over it one face, a fifth darker at its foot than under its top.
            Assert.That(Outcrop.JointShare, Is.InRange(.1f, .2f));
            Assert.That(Outcrop.WallHeadTone / Outcrop.JointTone, Is.InRange(1.3f, 1.6f), "U8's ratio was 1.88");
            Assert.That(Outcrop.WallHeadTone / Outcrop.WallFootTone, Is.InRange(1.1f, 1.3f));
            Assert.That(Outcrop.JointTone, Is.LessThan(Outcrop.WallFootTone - .1f), "the joint is still a line");
            // a slab of the east tor shows about 4 m of wall: its joint is under a metre of it
            var tor = U5Plan.Plan.EastTor;
            for (var n = 1; n < 6; n++)
            {
                var shows = Enumerable.Range(4, 4).Min(i => tor.Blocks[n][i].Y) - tor.BlockFoot[n];
                Assert.That(shows * Outcrop.JointShare, Is.InRange(.2, 1.0), "slab " + n);
            }
        }

        // ---------------------------------------------------------------- the squalls

        [Test]
        public void TheSquallsAreDrawnForTheTallHomesEyeAndNoOther()
        {
            var map = U5Plan.Map;
            Assert.That(SkyState.SquallsFromEye(map.Pose("pose:home:portrait").Camera.Y), Is.EqualTo(1), "the 9:16 HOME sees them in full");
            foreach (var name in StoryPoses.All().Where(n => n != "pose:home:portrait"))
                Assert.That(SkyState.SquallsFromEye(map.Pose(name).Camera.Y), Is.EqualTo(0), name);
            foreach (var f in Pumps) Assert.That(SkyState.SquallsFromEye(map.Pose("pose:" + f.Family + ":well" + StoryPoses.TallSuffix).Camera.Y), Is.EqualTo(0));
            Assert.That(SkyState.SquallsFromEye(map.Pose(StoryPoses.NightSky).Camera.Y), Is.EqualTo(0));
            // with room either side: the 16:9 HOME may rise a fifth, the 9:16 one fall a fifth, before either changes
            Assert.That(map.Pose("pose:home").Camera.Y * 1.2, Is.LessThanOrEqualTo(SkyState.SquallEyeFromM));
            Assert.That(map.Pose("pose:home:portrait").Camera.Y * .82, Is.GreaterThanOrEqualTo(SkyState.SquallEyeFullM));
            // it comes in without a step, and the planar reflection's eye (the same height under the water) sees what the eye does
            for (var h = 100.0; h < 200; h += 5)
            {
                Assert.That(SkyState.SquallsFromEye(h + 5), Is.GreaterThanOrEqualTo(SkyState.SquallsFromEye(h)));
                Assert.That(SkyState.SquallsFromEye(h + 5) - SkyState.SquallsFromEye(h), Is.LessThan(.12));
                Assert.That(SkyState.SquallsFromEye(-h), Is.EqualTo(SkyState.SquallsFromEye(h)));
            }
        }
    }
}
