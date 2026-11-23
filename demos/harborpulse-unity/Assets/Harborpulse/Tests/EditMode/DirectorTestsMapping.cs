// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text.Json;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    static class DirectorKit
    {
        public static string StoryText()
        {
            for (var dir = new DirectoryInfo(Directory.GetCurrentDirectory()); dir != null; dir = dir.Parent)
                foreach (var relative in new[] { "Assets/Harborpulse/Resources/storyboard.json", "demos/harborpulse-unity/Assets/Harborpulse/Resources/storyboard.json" })
                { var path = Path.Combine(dir.FullName, relative); if (File.Exists(path)) return File.ReadAllText(path); }
            throw new FileNotFoundException("storyboard.json");
        }
        public static string FixtureDirectory() {
            for (var dir = new DirectoryInfo(Directory.GetCurrentDirectory()); dir != null; dir = dir.Parent) {
                var path = Path.Combine(dir.FullName, "demos/harborpulse-unity/Dotnet~/Tests/Fixtures");
                if (Directory.Exists(path)) return path;
                path = Path.Combine(dir.FullName, "Tests/Fixtures"); if (Directory.Exists(path)) return path;
            }
            throw new DirectoryNotFoundException("director fixtures");
        }
        public static Storyboard Story() => Storyboard.Parse(StoryText(), new PresentationMap(Kit.Layout()));
        public static Observation O(string kind, string device = "", string key = "", string status = "", double sim = 17.5, double wall = 690,
            string id = "record", string cycle = "cycle-this-run", string source = EvidenceSource.Platform, IReadOnlyDictionary<string, string> fields = null, IReadOnlyDictionary<string, string[]> sets = null,
            string run = "run", double? occurred = null, string command = "", string attempt = "")
            => new Observation(run, kind, id, device, key, cycle, command, attempt, status, wall, occurred ?? wall, sim, source, fields, sets);
        public static Needs Need(string json) { using (var doc = JsonDocument.Parse(json)) return Needs.Parse(doc.RootElement); }
        public static Dictionary<string, object> Object(string json) { using (var doc = JsonDocument.Parse(json)) return (Dictionary<string, object>)Copy(doc.RootElement); }
        static object Copy(JsonElement e)
        {
            switch (e.ValueKind)
            {
                case JsonValueKind.Object: return e.EnumerateObject().ToDictionary(p => p.Name, p => Copy(p.Value));
                case JsonValueKind.Array: return e.EnumerateArray().Select(Copy).ToList();
                case JsonValueKind.String: return e.GetString();
                case JsonValueKind.Number: return e.GetDouble();
                case JsonValueKind.True: return true; case JsonValueKind.False: return false; default: return null;
            }
        }
        public static string Json(object value) => DirectorWire.Json(w => Write(w, value));
        static void Write(Utf8JsonWriter w, object value)
        {
            if (value == null) { w.WriteNullValue(); return; }
            if (value is Dictionary<string, object> d) { w.WriteStartObject(); foreach (var p in d) { w.WritePropertyName(p.Key); Write(w, p.Value); } w.WriteEndObject(); }
            else if (value is List<object> a) { w.WriteStartArray(); foreach (var v in a) Write(w, v); w.WriteEndArray(); }
            else if (value is string s) w.WriteStringValue(s);
            else if (value is bool b) w.WriteBooleanValue(b);
            else w.WriteNumberValue(Convert.ToDouble(value));
        }
        public static Dictionary<string, object> Obj(object o) => (Dictionary<string, object>)o;
        public static List<object> Arr(object o) => (List<object>)o;
    }

    public class DirectorTestsMapping
    {
        [Test]
        public void M1_BijectionByToken()
        {
            var l = Kit.Layout(); var map = new PresentationMap(l);
            Assert.That(map.Anchors.Keys, Is.EquivalentTo(l.Devices.Select(d => d.Token)));
            var anchors = map.Anchors.ToDictionary(p => p.Key, p => p.Value); anchors.Remove(l.Devices[0].Token);
            Assert.Throws<FormatException>(() => PresentationMap.ValidateAnchors(l, anchors));
            anchors["unknown"] = new Ground(0, 0);
            Assert.Throws<FormatException>(() => PresentationMap.ValidateAnchors(l, anchors));
        }
        [TestCase(42)] [TestCase(44)]
        public void M1_RejectsChangedManifest(int count)
        {
            var json = DirectorKit.Object(Kit.LayoutText()); var devices = DirectorKit.Arr(json["devices"]);
            if (count == 42) devices.RemoveAt(0);
            else { var d = new Dictionary<string, object>(DirectorKit.Obj(devices[0])); d["token"] = "hp-new-device"; d["externalId"] = "NEW-DEVICE"; devices.Add(d); }
            Assert.Throws<FormatException>(() => new PresentationMap(HarborLayout.Parse(DirectorKit.Json(json))));
        }
        [Test]
        public void M2_PlatformMembershipMovesMismatches()
        {
            var l = Kit.Layout(); var membership = l.Devices.ToDictionary(d => d.Token, d => d.Quay);
            membership["hp-gauge-north-1"] = "hp-east-quay"; var map = new PresentationMap(l, membership);
            Assert.That(map.ReadinessWarnings.Single(), Does.Contain("hp-gauge-north-1"));
            foreach (var d in l.Devices.Where(d => d.Kind != DeviceKind.Truck)) Assert.That(PresentationMap.Contains(map.Quays[membership[d.Token]], map.Anchors[d.Token]), Is.True, d.Token);
        }
        [Test]
        public void M3_LampLongitudeOrderAndSwapControl()
        {
            var l = Kit.Layout(); var map = new PresentationMap(l); Assert.That(map.LampOrderValid(), Is.True);
            var overrides = new Dictionary<string, Ground> { ["hp-light-north-1"] = map.Anchors["hp-light-north-2"], ["hp-light-north-2"] = map.Anchors["hp-light-north-1"] };
            Assert.That(new PresentationMap(l, anchorOverrides: overrides).LampOrderValid(), Is.False);
        }
        [Test]
        public void M4_IdentityAndHeldMissingInvalid()
        {
            foreach (var h in new[] { -2.1, 0, 0.8, 2.2, 2.4, 2.5, 3.2, 4.0 }) Assert.That(PresentationMap.Height(h), Is.EqualTo(h));
            var now = Kit.T0; var fresh = TimeSpan.FromSeconds(30);
            Assert.That(PresentationMap.ReadingHeight(new Reading(2.2, now), now, fresh).Metres, Is.EqualTo(2.2));
            var stale = PresentationMap.ReadingHeight(new Reading(3.2, now.AddMinutes(-1)), now, fresh);
            Assert.That(stale.Metres, Is.EqualTo(3.2)); Assert.That(stale.Label, Is.EqualTo("held"));
            Assert.That(PresentationMap.ReadingHeight(null, now, fresh).Label, Is.EqualTo("no reading"));
            Assert.That(PresentationMap.ReadingHeight(new Reading(double.NaN, now), now, fresh).Metres, Is.Null);
            Assert.That(PresentationMap.ReadingHeight(new Reading(double.PositiveInfinity, now), now, fresh, 1.8).Metres, Is.EqualTo(1.8));
            var tide = new TideBoard(Kit.Layout()); Assert.That(PresentationMap.WaterHeight(tide.Region("hp-east-quay")).Label, Is.EqualTo("no reading"));
        }
        [Test]
        public void M5_NoFlipsAndDecalsAreMappedRings()
        {
            var l = Kit.Layout(); var m = new PresentationMap(l); Assert.That(m.Validate(), Is.Empty);
            foreach (var f in l.Fences)
            {
                for (var i = 0; i < f.Ring.Count; i++)
                { var actual = m.MapFix(f.Ring[i].Lat, f.Ring[i].Lon); Assert.That(m.FenceRings[f.Token][i].X, Is.EqualTo(actual.X).Within(1e-8)); Assert.That(m.FenceRings[f.Token][i].Z, Is.EqualTo(actual.Z).Within(1e-8)); }
                // Every straight fence edge is a chain of mesh edges, including the Steiner splits.
                for (var i = 1; i < f.Ring.Count; i++)
                {
                    var a = m.FenceRings[f.Token][i - 1]; var b = m.FenceRings[f.Token][i];
                    for (var k = 0; k <= 100; k++)
                    {
                        var t = k / 100.0; var p = m.MapFix(f.Ring[i - 1].Lat + t * (f.Ring[i].Lat - f.Ring[i - 1].Lat), f.Ring[i - 1].Lon + t * (f.Ring[i].Lon - f.Ring[i - 1].Lon));
                        Assert.That(Math.Abs((b.X - a.X) * (p.Z - a.Z) - (b.Z - a.Z) * (p.X - a.X)), Is.LessThan(1e-5));
                    }
                }
            }
        }
        static bool ManifestInside(LayoutFence f, LatLon p) => PresentationMap.Contains(f.Ring.Select(x => new Ground(x.Lon, x.Lat)).ToArray(), new Ground(p.Lon, p.Lat));
        static int GridMismatches(PresentationMap map)
        {
            var l = Kit.Layout(); var plain = l.Fences[0]; var frame = new WorldFrame(l.ControlFrame[0]);
            var controls = plain.Ring.Concat(l.TruckRoute).Append(l.Depot).Select(frame.ToGround).ToArray();
            var min = new Ground(controls.Min(p => p.X), controls.Min(p => p.Z)); var max = new Ground(controls.Max(p => p.X), controls.Max(p => p.Z)); var failures = 0;
            for (var x = (int)min.X - 10; x <= max.X + 10; x++) for (var z = (int)min.Z - 10; z <= max.Z + 10; z++)
            {
                var lat = l.ControlFrame[0].Lat + z * 180 / (Math.PI * WorldFrame.EarthRadiusM);
                var lon = l.ControlFrame[0].Lon + x * 180 / (Math.PI * WorldFrame.EarthRadiusM * Math.Cos(l.ControlFrame[0].Lat * Math.PI / 180));
                var p = new LatLon(lat, lon);
                if (ManifestInside(plain, p) != map.InsideFence(plain.Token, map.MapFix(lat, lon))) failures++;
            }
            return failures;
        }
        [Test]
        public void M5_MetreGridAndBothCleanDayLegsPreserveContainment()
        {
            var l = Kit.Layout(); var map = new PresentationMap(l); Assert.That(GridMismatches(map), Is.Zero);
            for (var step = 0; step <= 2400; step++)
            {
                var h = step / 100.0;
                // Port of harborTruckFix: depart 17, parked 18, return 21.5, home 22.5.
                var fraction = h >= 17 && h < 18 ? h - 17 : h >= 18 && h < 21.5 ? 1 : h >= 21.5 && h < 22.5 ? 1 - (h - 21.5) : 0;
                var p = new LatLon(l.Depot.Lat + fraction * (l.TruckRoute[1].Lat - l.Depot.Lat), l.Depot.Lon);
                foreach (var fence in l.Fences) Assert.That(map.InsideFence(fence.Token, map.MapFix(p.Lat, p.Lon)), Is.EqualTo(ManifestInside(fence, p)), h.ToString());
            }
        }
        [Test]
        public void M5_CrossedCornerFailsOrientationAndContainment()
        {
            var l = Kit.Layout(); var controls = l.ControlFrame.Concat(l.Fences.SelectMany(f => f.Ring)).Concat(l.TruckRoute).ToArray();
            var xs = controls.Select(p => p.Lon).Distinct().OrderBy(x => x).ToArray(); var zs = controls.Select(p => p.Lat).Distinct().OrderBy(x => x).ToArray();
            var corner = l.Fences[0].Ring[0]; var index = Array.IndexOf(zs, corner.Lat) * xs.Length + Array.IndexOf(xs, corner.Lon);
            var clean = new PresentationMap(l); var opposite = clean.MapFix(l.Fences[0].Ring[2].Lat, l.Fences[0].Ring[2].Lon);
            var map = new PresentationMap(l, vertexOverrides: new Dictionary<int, Ground> { [index] = new Ground(opposite.X + 20, opposite.Z + 20) });
            Assert.That(map.Validate(), Is.Not.Empty); Assert.That(GridMismatches(map), Is.GreaterThan(0));
        }
        [Test]
        public void M6_TimeInterpolationSnappingAndSeekEqualsPlay()
        {
            var l = Kit.Layout(); var map = new PresentationMap(l);
            var fixes = new[] { new TimedFix(0, l.Depot), new TimedFix(20, l.TruckRoute[1]), new TimedFix(100, l.Depot) };
            var a = map.TruckAt(fixes, 0).At; var b = map.TruckAt(fixes, 20).At; var mid = map.TruckAt(fixes, 10).At;
            Assert.That(mid.Z, Is.EqualTo((a.Z + b.Z) / 2).Within(1e-8));
            var at40 = map.TruckAt(fixes, 40).At; Assert.That(at40.Z, Is.EqualTo(b.Z + (a.Z - b.Z) * 0.25).Within(1e-8));
            var roadA = map.Road[0]; var roadB = map.Road[1]; var dx = roadB.X - roadA.X; var dz = roadB.Z - roadA.Z; var length = Math.Sqrt(dx * dx + dz * dz);
            var middle = new Ground((roadA.X + roadB.X) / 2, (roadA.Z + roadB.Z) / 2);
            foreach (var offset in new[] { 2.9, 3.0, 3.1 }) {
                var fix = map.SnapToRoad(new Ground(middle.X - dz * offset / length, middle.Z + dx * offset / length));
                Assert.That(fix.OffRoad, Is.EqualTo(offset > 3));
                if (offset <= 3) { Assert.That(fix.At.X, Is.EqualTo(middle.X).Within(1e-8)); Assert.That(fix.At.Z, Is.EqualTo(middle.Z).Within(1e-8)); }
            }
            for (var t = 100; t >= 0; t--) { var play = map.TruckAt(fixes, t); map.TruckAt(fixes, 0); var seek = map.TruckAt(fixes, t); Assert.That(seek.At.Z, Is.EqualTo(play.At.Z)); Assert.That(seek.At.X, Is.EqualTo(play.At.X)); Assert.That(seek.OffRoad, Is.EqualTo(play.OffRoad)); }
        }
        [Test]
        public void HomePosesAndDisclosures()
        {
            var m = new PresentationMap(Kit.Layout()); Assert.That(m.Pose("pose:home").Camera.Z, Is.EqualTo(-420)); Assert.That(m.Pose("pose:home").VerticalFov, Is.EqualTo(35)); Assert.That(m.Pose("pose:home:portrait").VerticalFov, Is.EqualTo(44));
            Assert.That(m.Disclosure("hp-light-east-3"), Is.EqualTo("manifest 49.80200 N, 12.15800 W · drawn here (compact layout)"));
            Assert.That(m.Disclosure("hp-pump-north"), Does.Not.Contain("manifest")); Assert.That(m.Disclosure("hp-truck-1", Kit.Layout().Depot), Does.Contain(Kit.Layout().Depot.Lat.ToString("0.00000", System.Globalization.CultureInfo.InvariantCulture)));
        }

        [Test]
        public void SouthernControlsAreCompactNorthboundAndRejectBothBounds()
        {
            var l = Kit.Layout(); var map = new PresentationMap(l); Assert.That(map.Depot.Z, Is.GreaterThan(-180));
            Assert.That(map.Road[1].Z, Is.GreaterThan(map.Depot.Z));
            Assert.That(map.Pose("pose:depot:wide").Look.Z, Is.EqualTo(map.Depot.Z));
            foreach (var lat in new[] { l.ControlFrame.Min(p => p.Lat) - 0.001, l.ControlFrame.Max(p => p.Lat) + 0.001 }) Assert.Throws<FormatException>(() => map.MapFix(lat, l.Depot.Lon));
            foreach (var lon in new[] { l.ControlFrame.Min(p => p.Lon) - 0.001, l.ControlFrame.Max(p => p.Lon) + 0.001 }) Assert.Throws<FormatException>(() => map.MapFix(l.Depot.Lat, lon));
        }
    }
}
