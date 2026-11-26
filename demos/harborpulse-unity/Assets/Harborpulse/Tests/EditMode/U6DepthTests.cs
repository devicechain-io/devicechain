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
    /// U6: depth behind the town (layered hills, a haze that darkens below the horizon), the hour made legible from a camera that never sees
    /// the sun or the moon, the rocky foreshore, the truck's whole near model and a browser build's options.
    /// </summary>
    public class U6DepthTests
    {
        const double Deck = SitePlan.DeckHeightM;
        const double From = 16, To = 22.5;
        static SkyState At(double hour) => DayLighting.Evaluate(hour, Weather.Window(From, To, hour));
        static SkyState Clear(double hour) => DayLighting.Evaluate(hour, new Weather(0, 0));
        static double Luma(Rgb c) => 0.2126 * c.R + 0.7152 * c.G + 0.0722 * c.B;

        // ---------------------------------------------------------------- the backdrop's layers

        [Test]
        public void TheBackdropIsThreeRangesNearestFirstEachBehindTheLastTrees()
        {
            var layers = CoastalTerrain.Backdrop;
            Assert.That(layers.Select(l => l.Name), Is.EqualTo(new[] { "Foothills", "Middle range", "Distant ridge" }));
            // the plantation ends 1,700 m outside the core: no tree stands on, or pokes through, a backdrop hill
            Assert.That(layers[0].RowZ[0], Is.GreaterThanOrEqualTo(CoastalTerrain.CoreNorthZ + TreeStyle.PlantationM));
            foreach (var t in U5Plan.Plan.Props.Where(p => p.Kind == "Tree")) Assert.That(t.At.Z, Is.LessThan(layers[0].RowZ[0]), "a tree at " + t.At.X + "," + t.At.Z);
            for (var i = 1; i < layers.Length; i++)
            {
                Assert.That(layers[i].CrestZ, Is.GreaterThan(layers[i - 1].CrestZ), layers[i].Name + " stands behind " + layers[i - 1].Name);
                Assert.That(layers[i].Hills.Max(h => h[2]), Is.GreaterThan(layers[i - 1].Hills.Max(h => h[2])), "and is higher");
            }
            // everything lies inside the 3.6 km far clip of the HOME camera (z = -420), and is a few hundred triangles a layer
            foreach (var l in layers)
            {
                Assert.That(l.RowZ.Last() + 420, Is.LessThan(3600), l.Name);
                Assert.That((int)(2 * l.HalfWidth / l.ColumnStep) * (l.RowZ.Length - 1) * 2, Is.LessThan(700), l.Name + " triangles");
                Assert.That(l.RowFraction.First(), Is.EqualTo(0), l.Name + " starts on the ground");
                Assert.That(l.Height(0, l.RowZ[0] - 1), Is.EqualTo(Deck), l.Name + " is the deck in front of its first row");
                Assert.That(l.Crest(-l.HalfWidth), Is.EqualTo(0).Within(1e-9), l.Name + " west end");
                Assert.That(l.Crest(l.HalfWidth), Is.EqualTo(0).Within(1e-9), l.Name + " east end");
            }
        }

        [Test]
        public void FromHomeTheLayersStepUpTheFrameAndTheirCrestsCross()
        {
            // pins the layered look: seen from HOME's eye the foothills' summits are lowest, the middle range's above them, the ridge's on
            // top, all under 1 degree above the horizontal except the ridge, so the sky keeps its share
            var home = U5Plan.Map.Pose("pose:home");
            double Angle(CoastalTerrain.HillLayer l, double x) =>
                Math.Atan2(Deck + l.Crest(x) - home.Camera.Y, l.CrestZ - home.Camera.Z) * 180 / Math.PI;
            double Peak(CoastalTerrain.HillLayer l) => Enumerable.Range(-150, 301).Max(i => Angle(l, i * 10.0));
            var foothills = Peak(CoastalTerrain.Foothills); var mid = Peak(CoastalTerrain.MidHills); var ridge = Peak(CoastalTerrain.Ridge);
            TestContext.WriteLine("crest angles from HOME: foothills " + foothills.ToString("F2") + ", middle " + mid.ToString("F2") + ", ridge " + ridge.ToString("F2") + " deg");
            Assert.That(foothills, Is.LessThan(mid - .3));
            Assert.That(mid, Is.LessThan(ridge - .3));
            Assert.That(mid, Is.LessThan(0), "the nearer ranges stay below HOME's eye line");
            Assert.That(foothills, Is.GreaterThan(-2.0), "but stand clear above the last trees (about -2.1 degrees)");
            // the crest lines cross: somewhere in HOME's view a nearer range stands above the one behind it, and somewhere below it
            int midOverRidge = 0, ridgeOverMid = 0, footOverMid = 0, midOverFoot = 0;
            for (var x = -1500.0; x <= 1500; x += 25)
            {
                if (Angle(CoastalTerrain.MidHills, x) > Angle(CoastalTerrain.Ridge, x) + .05) midOverRidge++; else if (Angle(CoastalTerrain.Ridge, x) > Angle(CoastalTerrain.MidHills, x) + .05) ridgeOverMid++;
                if (Angle(CoastalTerrain.Foothills, x) > Angle(CoastalTerrain.MidHills, x) + .05) footOverMid++; else if (Angle(CoastalTerrain.MidHills, x) > Angle(CoastalTerrain.Foothills, x) + .05) midOverFoot++;
            }
            Assert.That(midOverRidge, Is.GreaterThan(8)); Assert.That(ridgeOverMid, Is.GreaterThan(8));
            Assert.That(footOverMid, Is.GreaterThan(8)); Assert.That(midOverFoot, Is.GreaterThan(8));
            // and from a quay (eye 7 m) no range rises more than 3 degrees: more sky, less hill
            foreach (var l in CoastalTerrain.Backdrop)
                for (var x = -2400.0; x <= 2400; x += 50)
                    Assert.That(Math.Atan2(Deck + l.Crest(x) - 7, l.CrestZ - 100) * 180 / Math.PI, Is.LessThan(3), l.Name + " at " + x);
        }

        [Test]
        public void TheForestedCrestsAreBrokenLinesAndTheFarRidgeIsUnchanged()
        {
            // the nearer ranges carry a tree line: neighbouring columns differ, but only where a hill stands
            foreach (var l in new[] { CoastalTerrain.Foothills, CoastalTerrain.MidHills })
            {
                var steps = 0;
                for (var x = -l.HalfWidth + 600; x < l.HalfWidth - 600; x += l.ColumnStep)
                    if (Math.Abs(l.Crest(x + l.ColumnStep) - l.Crest(x)) > .8) steps++;
                Assert.That(steps, Is.GreaterThan(20), l.Name);
            }
            // the far ridge keeps the U5 profile exactly: its summit, its saddle and both ends
            Assert.That(CoastalTerrain.RidgeCrest(-420), Is.EqualTo(124).Within(1e-9));
            Assert.That(CoastalTerrain.RidgeCrest(760), Is.EqualTo(90).Within(1e-9));
            Assert.That(CoastalTerrain.RidgeHeight(-420, 2760), Is.EqualTo(Deck + 124).Within(1e-9));
            Assert.That(CoastalTerrain.RidgeHeight(-420, 2480), Is.EqualTo(Deck + 124 * .35).Within(1e-9));
            // inside both tapers, from the U5 formula worked by hand: the east one runs 1800-2400 and the west one -2400 to -1850
            Assert.That(CoastalTerrain.RidgeCrest(2100), Is.EqualTo(5.80).Within(.05));
            Assert.That(CoastalTerrain.RidgeCrest(-2100), Is.EqualTo(12.29).Within(.05));
            Assert.That(CoastalTerrain.RidgeCrest(1800), Is.EqualTo(44.86).Within(.05), "the east taper has not begun at 1800");
        }

        // ---------------------------------------------------------------- the haze

        [Test]
        public void TheHazeDarkensBelowTheHorizonAtEveryHour()
        {
            foreach (var hour in new[] { 2.0, 6.4, 12.5, 15.4, 17.2, 18.3, 20.0, 23.6 })
            {
                var s = At(hour);
                Assert.That(Luma(s.GroundHaze), Is.LessThan(Luma(s.FogColor) * .9), "far ground is darker than the horizon at " + hour);
                Assert.That(Luma(s.GroundHaze), Is.GreaterThan(Luma(s.FogColor) * .5), "but still haze, not shadow, at " + hour);
                // the sky and the scenery draw one function: the horizon's colour at and above the horizon, the ground's well below it
                Assert.That(s.HazeColor(.01).R, Is.EqualTo(s.FogColor.R).Within(1e-12));
                Assert.That(s.HazeColor(SkyState.HazeHorizonY).G, Is.EqualTo(s.FogColor.G).Within(1e-12));
                Assert.That(s.HazeColor(SkyState.HazeGroundY).B, Is.EqualTo(s.GroundHaze.B).Within(1e-12));
                Assert.That(s.HazeColor(-.4).R, Is.EqualTo(s.GroundHaze.R).Within(1e-12));
                var last = Luma(s.HazeColor(.01));
                for (var y = .01; y >= -.08; y -= .002)
                {
                    var l = Luma(s.HazeColor(y));
                    Assert.That(l, Is.LessThanOrEqualTo(last + 1e-12), "monotone at " + hour + ", y " + y);
                    last = l;
                }
            }
            // from HOME the far ground (the last trees, 2.2 km off) is well into the darker haze, and the ridge's crest is in the horizon's
            var home = U5Plan.Map.Pose("pose:home");
            var treeLine = (Deck + 14 - home.Camera.Y) / Math.Sqrt(2260.0 * 2260 + 84 * 84);
            Assert.That(treeLine, Is.LessThan(SkyState.HazeHorizonY - .7 * (SkyState.HazeHorizonY - SkyState.HazeGroundY)));
            var crest = (Deck + 124 - home.Camera.Y) / 3180.0;
            Assert.That(crest, Is.GreaterThan(SkyState.HazeHorizonY));
        }

        [Test]
        public void EachRangeKeepsMoreOfItselfThanTheOneBehindAtNoonAndInTheStorm()
        {
            var home = U5Plan.Map.Pose("pose:home");
            foreach (var hour in new[] { 12.5, 18.0 })
            {
                var s = At(hour);
                double T(CoastalTerrain.HillLayer l)
                {
                    var peak = l.Hills.OrderByDescending(h => h[2]).First();
                    var y = Deck + l.Crest(peak[0]);
                    var d = Math.Sqrt(Math.Pow(peak[0] - home.Camera.X, 2) + Math.Pow(y - home.Camera.Y, 2) + Math.Pow(l.CrestZ - home.Camera.Z, 2));
                    return l == CoastalTerrain.Ridge ? s.RidgeTransmission(d) * Math.Min(1, (y - 6) / 50) : s.BackdropTransmission(d, y, l.FootFadeM);
                }
                var a = T(CoastalTerrain.Foothills); var b = T(CoastalTerrain.MidHills); var c = T(CoastalTerrain.Ridge);
                TestContext.WriteLine(hour + ": foothills " + a.ToString("F2") + ", middle " + b.ToString("F2") + ", ridge " + c.ToString("F2"));
                var step = hour < 16 ? .04 : .025;
                Assert.That(a, Is.GreaterThan(b + step), "foothills clearer than the middle range at " + hour);
                Assert.That(b, Is.GreaterThan(c + step), "middle range clearer than the ridge at " + hour);
                if (hour < 16) Assert.That(a, Is.InRange(.6, .9), "a definite wooded hill at noon"); else Assert.That(a, Is.InRange(.15, .5), "a shape in the storm, not a wall and not nothing");
            }
            // a layer's foot is gone into the ground haze, its crest is not
            var noon = At(12.5);
            Assert.That(noon.BackdropTransmission(2400, SkyState.BackdropFootY, 14), Is.EqualTo(0));
            Assert.That(noon.BackdropTransmission(2400, SkyState.BackdropFootY + 14, 14), Is.EqualTo(noon.RidgeTransmission(2400)).Within(1e-12));
        }

        // ---------------------------------------------------------------- the hour, from a camera that looks north

        [Test]
        public void TheSkyIsHeldBackFromTheDimLightLiftButTheTownIsNot()
        {
            Assert.That(Clear(12.5).SkyGain, Is.EqualTo(1).Within(.02), "a fair noon needs no hold");
            // the night's own lift is the eye opening, and the sky was graded with it; the story lift on top is the town's alone, so the sky is held back by exactly that
            Assert.That(Clear(0.5).SkyGain, Is.EqualTo(Math.Pow(2, -DayLighting.NightStoryLift)).Within(1e-9));
            Assert.That(Clear(0.5).ExposureLift + Math.Log(Clear(0.5).SkyGain, 2), Is.EqualTo(0.55).Within(1e-9), "the night sky is where it was");
            foreach (var hour in new[] { 6.4, 17.2, 18.3 })
            {
                var s = At(hour);
                Assert.That(s.ExposureLift, Is.GreaterThan(.8), "the town is lifted at " + hour);
                // (the storm's story lift is the town's too, so under the storm the sky is held back a little further than the dim light alone asks)
                Assert.That(s.SkyGain, Is.InRange(.38, .72), "and the sky is held back at " + hour);
                // what reaches the screen: the horizon haze, through the lift and the hold, stays under white
                Assert.That(Luma(s.FogColor) * Math.Pow(2, s.ExposureLift) * s.SkyGain, Is.LessThan(.95), "horizon not blown out at " + hour);
            }
        }

        [Test]
        public void ALowSunGivesAKeyLightAndLightsTheCloudFromBelow()
        {
            // an hour either side of the horizon the sun is already a key (it was a third of this), warm but not red
            Assert.That(Clear(7.0).SunIntensity, Is.GreaterThan(.85));
            Assert.That(Clear(6.6).SunIntensity, Is.GreaterThan(.35));
            Assert.That(Clear(12.5).SunIntensity, Is.EqualTo(2.1 * (1 - .4 * (.30 + .50 * DayLighting.FairCloud(12.5)))).Within(1e-9), "noon is unchanged");
            var dawn = Clear(6.5);
            Assert.That(dawn.SunColor.B, Is.InRange(.2, .6), "warm");
            Assert.That(dawn.SunColor.G, Is.GreaterThan(.6), "not red");
            // in the morning the sun is in the east and in the evening in the west: the side light swaps over
            Assert.That(Clear(7.0).SunAzimuthDeg, Is.InRange(90, 110));
            Assert.That(Clear(17.5).SunAzimuthDeg, Is.InRange(250, 270));
            Assert.That(Clear(7.0).ShadowStrength, Is.GreaterThanOrEqualTo(Clear(12.5).ShadowStrength));

            Assert.That(Clear(6.4).CloudUnderlightStrength, Is.GreaterThan(.6), "dawn");
            Assert.That(Clear(18.0).CloudUnderlightStrength, Is.GreaterThan(.6), "a clear dusk");
            Assert.That(Clear(12.5).CloudUnderlightStrength, Is.EqualTo(0));
            Assert.That(Clear(23.6).CloudUnderlightStrength, Is.EqualTo(0));
            Assert.That(At(18.0).CloudUnderlightStrength, Is.InRange(.02, .15), "the storm's closed front leaves an ember, no more");
            Assert.That(dawn.CloudUnderlight.R, Is.GreaterThan(dawn.CloudUnderlight.B * 2), "ember and rose, not white");
        }

        [Test]
        public void TheMoonSilversTheNightsCloudWhenItIsUpAndTheSkyIsOpen()
        {
            Assert.That(Clear(23.6).MoonCloudGlow, Is.GreaterThan(.8));
            Assert.That(Clear(2.0).MoonCloudGlow, Is.GreaterThan(.8));
            Assert.That(Clear(12.5).MoonCloudGlow, Is.EqualTo(0));
            Assert.That(At(20.0).MoonCloudGlow, Is.LessThan(.1), "not through the storm's deck");
            // and there is an hour and a pose that sees the moon over the harbour: the rig's night-sky shot
            var s = Clear(StoryPoses.NightSkyHour);
            Assert.That(s.MoonElevationDeg, Is.InRange(12, 30));
            var pose = U5Plan.Map.Pose(StoryPoses.NightSky);
            var e = s.MoonElevationDeg * Math.PI / 180; var a = s.MoonAzimuthDeg * Math.PI / 180;
            var far = 1e6;
            Assert.That(CameraMath.Project(pose, 16.0 / 9, pose.Camera.X + Math.Sin(a) * Math.Cos(e) * far, pose.Camera.Y + Math.Sin(e) * far, pose.Camera.Z + Math.Cos(a) * Math.Cos(e) * far,
                out var nx, out var ny), Is.True);
            TestContext.WriteLine("moon at " + StoryPoses.NightSkyHour + ": elevation " + s.MoonElevationDeg.ToString("F1") + ", azimuth " + s.MoonAzimuthDeg.ToString("F1") + ", frame " + nx.ToString("F2") + "," + ny.ToString("F2"));
            Assert.That(Math.Abs(nx), Is.LessThan(.6), "the moon is in the frame's middle third or so");
            Assert.That(ny, Is.InRange(.2, .85), "in the upper half, under the top edge");
        }

        // ---------------------------------------------------------------- the foreshore

        [Test]
        public void TheForeshoreBreaksTheOpenCoastAndLeavesTheHarbourAlone()
        {
            for (var x = -CoastalTerrain.ShoreStartX; x <= CoastalTerrain.ShoreStartX; x += 5) Assert.That(CoastalTerrain.ShoreReach(x), Is.EqualTo(0), "no ledge by the harbour at " + x);
            double min = double.MaxValue, max = 0; var turns = 0; var rising = true; var last = 0.0;
            for (var x = CoastalTerrain.ShoreFullX; x <= 2400; x += 10)
                foreach (var side in new[] { -1, 1 })
                {
                    var r = CoastalTerrain.ShoreReach(side * x);
                    Assert.That(r, Is.InRange(4, CoastalTerrain.ShoreMaxReachM), "reach at " + side * x);
                    min = Math.Min(min, r); max = Math.Max(max, r);
                    if (side > 0) { if (rising != r > last) turns++; rising = r > last; last = r; }
                }
            TestContext.WriteLine("foreshore reach " + min.ToString("F0") + " to " + max.ToString("F0") + " m, " + turns + " turns along the east coast");
            Assert.That(max - min, Is.GreaterThan(90), "points and coves, not a second straight line");
            Assert.That(turns, Is.GreaterThan(12));
            // exactly the deck's height at the coast (the land's own edge: no step, no seam), still dry a third of the way out, and
            // under the lowest water at its edge
            Assert.That(CoastalTerrain.ShoreHeight(0), Is.EqualTo(Deck));
            Assert.That(CoastalTerrain.Height(900, CoastalTerrain.CoastZ), Is.EqualTo(Deck), "the terrain's edge is at the deck's height too");
            Assert.That(CoastalTerrain.ShoreHeight(.3), Is.GreaterThan(1.8));
            Assert.That(CoastalTerrain.ShoreHeight(1), Is.EqualTo(-CoastalTerrain.ShoreEdgeDepthM).Within(1e-9));
            for (var t = 0.0; t < 1; t += .05) Assert.That(CoastalTerrain.ShoreHeight(t + .05), Is.LessThan(CoastalTerrain.ShoreHeight(t)));
            // HOME's 16:9 frame holds none of it: the water share and the masks there are untouched
            var home = U5Plan.Map.Pose("pose:home");
            bool InFrame(double x, double y, double z) => CameraMath.Project(home, 16.0 / 9, x, y, z, out var nx, out var ny) && Math.Abs(nx) < 1 && Math.Abs(ny) < 1;
            foreach (var side in new[] { -1, 1 })
                for (var x = CoastalTerrain.ShoreStartX; x <= 3800; x += 10)
                    for (var t = 0.0; t <= 1; t += .25)
                        Assert.That(InFrame(side * x, CoastalTerrain.ShoreHeight(t), CoastalTerrain.CoastZ - CoastalTerrain.ShoreReach(side * x) * t), Is.False, "foreshore in HOME at " + side * x);
            foreach (var s in CoastalTerrain.Skerries)
            {
                Assert.That(Math.Abs(s[0]), Is.GreaterThan(CoastalTerrain.ShoreFullX + s[2]), "a skerry stands off the open coast");
                Assert.That(s[1] + s[2], Is.LessThan(CoastalTerrain.CoastZ - CoastalTerrain.ShoreReach(s[0])), "and clear of the foreshore at " + s[0]);
                Assert.That(s[3], Is.InRange(1.5, 6));
                for (var k = 0; k < 8; k++) Assert.That(InFrame(s[0] + Math.Cos(k * Math.PI / 4) * s[2], s[3], s[1] + Math.Sin(k * Math.PI / 4) * s[2]), Is.False, "skerry in HOME");
            }
        }

        // ---------------------------------------------------------------- the truck's near model

        [Test]
        public void TheTrucksNearSetIsAWholeTruckNotTrim()
        {
            var truck = NearVehicles.Truck();
            truck.Bounds(out var x0, out var x1, out var y0, out var y1, out var z0, out var z1);
            Assert.That(y0, Is.LessThan(.1), "tyres on the deck"); Assert.That(y1, Is.GreaterThan(2.5), "a roof over the cab, and an aerial");
            Assert.That(x1 - x0, Is.InRange(2.7, 3.0)); Assert.That(z1 - z0, Is.InRange(5.4, 5.9), "the far boxes' footprint");
            // four tyres of the far wheels' radius, at the far wheels' places
            var tyres = truck.Parts.Where(p => p.Shape == NearShape.Cylinder && p.Tone == NearTone.Black && Math.Abs(p.R - .53f) < 1e-4f).ToList();
            Assert.That(tyres.Count, Is.EqualTo(4));
            foreach (var t in tyres) { Assert.That(Math.Abs(t.A.Z), Is.EqualTo(1.9f).Within(1e-4f)); Assert.That(t.A.Y, Is.EqualTo(.55f).Within(1e-4f)); }
            // a body of its own: sloped solids for the cab (not boxes), glass on four sides, the fleet stripe on both flanks
            Assert.That(truck.Parts.Count(p => p.Shape == NearShape.Quad && p.Tone == NearTone.Paint), Is.GreaterThanOrEqualTo(12), "the cab's two solids");
            Assert.That(truck.Parts.Count(p => p.Shape == NearShape.Quad && p.Tone == NearTone.Glass), Is.EqualTo(4));
            Assert.That(truck.Parts.Count(p => p.Tone == NearTone.Accent), Is.EqualTo(4));
            Assert.That(truck.Parts.Count(p => p.Shape == NearShape.Box && p.Tone == NearTone.Metal && p.B.Y > 1), Is.GreaterThanOrEqualTo(2), "the service body's cabinets");
            // the windscreen leans back: its top edge is behind its foot
            var glass = truck.Parts.Where(p => p.Shape == NearShape.Quad && p.Tone == NearTone.Glass).OrderByDescending(p => p.A.Z + p.B.Z + p.C.Z + p.D.Z).First();
            var corners = new[] { glass.A, glass.B, glass.C, glass.D };
            Assert.That(corners.OrderBy(c => c.Y).Take(2).Average(c => c.Z) - corners.OrderByDescending(c => c.Y).Take(2).Average(c => c.Z), Is.GreaterThan(.25f));
            Assert.That(truck.Parts.Count, Is.GreaterThan(150));
        }

        // ---------------------------------------------------------------- a browser build's options

        [Test]
        public void APageAddressCarriesOnlyThePreviewsOptions()
        {
            Assert.That(WebOptions.FromUrl("https://example.test/hp/index.html", out var problem), Is.Empty); Assert.That(problem, Is.Null);
            Assert.That(WebOptions.FromUrl(null, out problem), Is.Empty); Assert.That(problem, Is.Null);
            Assert.That(WebOptions.FromUrl("http://localhost:8123/index.html?hp-offline&hp-start-hour=17.3&hp-speed=4#top", out problem),
                Is.EqualTo(new[] { "-hp-offline", "-hp-start-hour", "17.3", "-hp-speed", "4" }));
            Assert.That(problem, Is.Null);
            Assert.That(WebOptions.FromUrl("http://h/?utm_source=x&hp-offline=1&hp-portrait", out problem), Is.EqualTo(new[] { "-hp-offline", "-hp-portrait" }), "a host page's own keys are left alone");
            Assert.That(WebOptions.FromUrl("http://h/?hp-start-hour=17%2E5", out problem), Is.EqualTo(new[] { "-hp-start-hour", "17.5" }));
            // refused, never ignored: an unknown option, a flag given a value, a value left out, a value that would read as a flag
            foreach (var bad in new[] { "hp-record-dir=C:/x", "dc-runner=http://evil", "hp-offline=yes", "hp-start-hour", "hp-start-hour=", "hp-speed=-hp-offline", "hp-capture-dir=/tmp" })
            {
                var args = WebOptions.FromUrl("http://h/?hp-offline&" + bad, out problem);
                if (bad.StartsWith("dc-", StringComparison.Ordinal)) { Assert.That(problem, Is.Null, "not an hp- key: the page's own"); Assert.That(args, Is.EqualTo(new[] { "-hp-offline" })); continue; }
                Assert.That(problem, Is.Not.Null, bad); Assert.That(args, Is.Empty, bad + " returns nothing at all");
            }
        }
    }
}
