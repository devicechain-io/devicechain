// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;

namespace DeviceChain.Harborpulse.Model.Director
{
    public readonly struct Point3
    {
        public Point3(double x, double y, double z) { X = x; Y = y; Z = z; }
        public double X { get; }
        public double Y { get; }
        public double Z { get; }
    }

    public sealed class CameraPose
    {
        public CameraPose(Point3 camera, Point3 look, double vfov) { Camera = camera; Look = look; VerticalFov = vfov; }
        public Point3 Camera { get; }
        public Point3 Look { get; }
        public double VerticalFov { get; }
    }

    public readonly struct MapTriangle
    {
        public MapTriangle(Ground a, Ground b, Ground c, Ground pa, Ground pb, Ground pc)
        { A = a; B = b; C = c; DrawA = pa; DrawB = pb; DrawC = pc; }
        public Ground A { get; }
        public Ground B { get; }
        public Ground C { get; }
        public Ground DrawA { get; }
        public Ground DrawB { get; }
        public Ground DrawC { get; }
        public bool Flipped => PresentationMap.Cross(A, B, C) * PresentationMap.Cross(DrawA, DrawB, DrawC) <= 1e-12;
        public bool Map(Ground p, out Ground result)
        {
            var area = PresentationMap.Cross(A, B, C);
            var u = PresentationMap.Cross(p, B, C) / area;
            var v = PresentationMap.Cross(A, p, C) / area;
            var w = 1 - u - v;
            result = new Ground(u * DrawA.X + v * DrawB.X + w * DrawC.X, u * DrawA.Z + v * DrawB.Z + w * DrawC.Z);
            return u >= -1e-9 && v >= -1e-9 && w >= -1e-9;
        }
    }

    public readonly struct TimedFix
    {
        public TimedFix(double wallS, LatLon position) { WallS = wallS; Position = position; }
        public double WallS { get; }
        public LatLon Position { get; }
    }

    public readonly struct DrawnFix
    {
        public DrawnFix(Ground at, bool offRoad) { At = at; OffRoad = offRoad; }
        public Ground At { get; }
        public bool OffRoad { get; }
    }

    public readonly struct DrawnHeight
    {
        public DrawnHeight(double? metres, bool held) { Metres = metres; Held = held && metres.HasValue; }
        public double? Metres { get; }
        public bool Held { get; }
        public string Label => !Metres.HasValue ? "no reading" : Held ? "held" : "";
        public double GeometryM => Metres ?? 0;
    }

    /// <summary>
    /// The boundary between manifest positions and the compact drawing. The constrained triangulation uses
    /// rectilinear Steiner vertices: each control coordinate splits a strip, each rectangle is ear-clipped
    /// into two positive triangles. Thus ring constraints are chains of mesh edges, including at route
    /// intersections; neither a fence edge nor a route segment can cut through a triangle interior.
    /// No Delaunay/reflection dependency, tolerance-dependent edge recovery, or route projection is needed.
    /// </summary>
    public sealed class PresentationMap
    {
        readonly HarborLayout layout;
        internal HarborLayout Layout => layout;
        readonly WorldFrame frame;
        readonly List<MapTriangle> triangles = new List<MapTriangle>();
        readonly Dictionary<string, Ground[]> fences = new Dictionary<string, Ground[]>(StringComparer.Ordinal);
        readonly Dictionary<string, Ground[]> regions = new Dictionary<string, Ground[]>(StringComparer.Ordinal);
        readonly Dictionary<string, Ground> anchors = new Dictionary<string, Ground>(StringComparer.Ordinal);
        readonly Dictionary<string, CameraPose> poses = new Dictionary<string, CameraPose>(StringComparer.Ordinal);
        readonly List<string> warnings = new List<string>();
        public IReadOnlyList<MapTriangle> Triangles => triangles.AsReadOnly();
        public IReadOnlyDictionary<string, Ground> Anchors => anchors;
        public IReadOnlyDictionary<string, Ground[]> Quays => regions;
        public IReadOnlyDictionary<string, Ground[]> FenceRings => fences;
        public IReadOnlyList<string> ReadinessWarnings => warnings.AsReadOnly();
        public string QuayFor(LayoutDevice device) => device.Kind == DeviceKind.Truck ? device.Quay :
            regions.First(p => Contains(p.Value, anchors[device.Token])).Key;
        public IReadOnlyList<Ground> Road { get; private set; }
        public Ground Depot { get; private set; }
        public Ground[] InnerWater { get; } = Rect(-150, -140, 140, 75);
        public Ground[] WestBreakwater { get; } = Rect(-215, -154, -35, -142);
        public Ground[] EastBreakwater { get; } = Rect(35, -154, 220, -142);
        public Ground[] Mouth { get; } = Rect(-35, -160, 35, -140);

        public PresentationMap(HarborLayout layout, IReadOnlyDictionary<string, string> platformMembership = null,
            IReadOnlyDictionary<string, Ground> anchorOverrides = null, IReadOnlyDictionary<int, Ground> vertexOverrides = null)
        {
            this.layout = layout ?? throw new ArgumentNullException(nameof(layout));
            if (layout.Devices.Count != 37) throw new FormatException("M1: authored presentation expects 37 device anchors");
            frame = new WorldFrame(layout.ControlFrame[0]);
            regions.Add("hp-north-quay", Rect(-120, 75, 105, 130));
            regions.Add("hp-east-quay", Rect(140, -80, 225, 65));
            regions.Add("hp-basin", Rect(-230, -130, -155, 90));
            BuildMesh(vertexOverrides);
            Depot = MapFix(layout.Depot.Lat, layout.Depot.Lon);
            foreach (var fence in layout.Fences) fences.Add(fence.Token, fence.Ring.Select(p => MapFix(p.Lat, p.Lon)).ToArray());
            Road = layout.TruckRoute.Select(p => MapFix(p.Lat, p.Lon)).ToArray();
            foreach (var d in layout.Devices) anchors.Add(d.Token, Place(d, d.Quay));
            if (anchorOverrides != null) foreach (var p in anchorOverrides) anchors[p.Key] = p.Value;
            ValidateAnchors(layout, anchors);
            if (platformMembership != null)
                foreach (var d in layout.Devices.Where(d => d.Kind != DeviceKind.Truck))
                {
                    if (!platformMembership.TryGetValue(d.Token, out var quay)) { warnings.Add(d.Token + ": platform quay unknown"); continue; }
                    if (!regions.ContainsKey(quay)) throw new FormatException("platform quay: " + quay);
                    if (!Contains(regions[quay], anchors[d.Token]))
                    {
                        warnings.Add(d.Token + ": layout quay differs from platform quay " + quay);
                        anchors[d.Token] = Place(d, quay);
                    }
                }
            AddPoses();
        }

        // Authored monotone strip maps keep the plain inland of East Quay and the depot further inland.
        static readonly double[] Lon = { -12.236, -12.230, -12.212, -12.200, -12.182, -12.170, -12.1655, -12.1616, -12.161, -12.1604, -12.1565, -12.152, -12.146 };
        static readonly double[] DrawX = { -240, -220, -160, -110, 100, 145, 155, 177, 181, 185, 207, 220, 240 };
        static readonly double[] Lat = { 49.7880, 49.7936, 49.7940, 49.7944, 49.7985, 49.8000, 49.8015, 49.8070, 49.8160 };
        static readonly double[] DrawZ = { -185, -165, -155, -145, -115, -90, -65, 75, 180 };
        static double Strip(double value, double[] keys, double[] values)
        {
            if (keys.Length != values.Length || keys.Length < 2 || !double.IsFinite(value)) throw new FormatException("invalid strip");
            for (var i = 1; i < keys.Length; i++) if (keys[i] <= keys[i - 1] || values[i] <= values[i - 1]) throw new FormatException("non-monotone strip");
            if (value < keys[0] || value > keys[keys.Length - 1]) throw new FormatException("control coordinate outside authored frame");
            for (var i = 1; i < keys.Length; i++)
                if (value <= keys[i]) return values[i - 1] + (values[i] - values[i - 1]) * (value - keys[i - 1]) / (keys[i] - keys[i - 1]);
            throw new FormatException("control coordinate outside authored frame: " + value.ToString(CultureInfo.InvariantCulture));
        }
        void BuildMesh(IReadOnlyDictionary<int, Ground> overrides)
        {
            var controls = layout.ControlFrame.Concat(layout.Fences.SelectMany(f => f.Ring)).Concat(layout.TruckRoute).ToArray();
            foreach (var f in layout.Fences)
                for (var i = 1; i < f.Ring.Count; i++)
                    if (f.Ring[i].Lat != f.Ring[i - 1].Lat && f.Ring[i].Lon != f.Ring[i - 1].Lon)
                        throw new FormatException("non-rectilinear fence constraint: " + f.Token);
            var xs = controls.Select(p => p.Lon).Distinct().OrderBy(x => x).ToArray();
            var zs = controls.Select(p => p.Lat).Distinct().OrderBy(x => x).ToArray();
            var source = new Ground[xs.Length * zs.Length];
            var target = new Ground[source.Length];
            for (var z = 0; z < zs.Length; z++) for (var x = 0; x < xs.Length; x++)
            {
                var index = z * xs.Length + x;
                source[index] = frame.ToGround(new LatLon(zs[z], xs[x]));
                target[index] = overrides != null && overrides.TryGetValue(index, out var replacement) ? replacement : new Ground(Strip(xs[x], Lon, DrawX), Strip(zs[z], Lat, DrawZ));
            }
            void Triangle(int a, int b, int c) => triangles.Add(new MapTriangle(source[a], source[b], source[c], target[a], target[b], target[c]));
            for (var z = 0; z < zs.Length - 1; z++) for (var x = 0; x < xs.Length - 1; x++)
            {
                var a = z * xs.Length + x; var b = a + 1; var c = a + xs.Length; var d = c + 1;
                Triangle(a, b, d); Triangle(a, d, c);
            }
        }

        public Ground MapFix(double latitude, double longitude)
        {
            var p = frame.ToGround(new LatLon(latitude, longitude));
            foreach (var t in triangles) if (t.Map(p, out var result)) return result;
            throw new FormatException("fix outside controlFrame");
        }
        public IReadOnlyList<string> Validate()
        {
            var errors = new List<string>();
            for (var i = 0; i < triangles.Count; i++) if (triangles[i].Flipped) errors.Add("orientation flip in triangle " + i);
            return errors.AsReadOnly();
        }
        public static void ValidateAnchors(HarborLayout layout, IReadOnlyDictionary<string, Ground> anchors)
        {
            var known = new HashSet<string>(layout.Devices.Select(d => d.Token), StringComparer.Ordinal);
            if (known.Count != anchors.Count || known.Any(t => !anchors.ContainsKey(t)) || anchors.Keys.Any(t => !known.Contains(t)))
                throw new FormatException("M1: device/anchor token sets differ");
        }
        public bool LampOrderValid()
        {
            var lamps = layout.Devices.Where(d => d.Kind == DeviceKind.Light).ToArray();
            return lamps.OrderBy(d => d.Position.Value.Lon).ThenBy(d => d.Token, StringComparer.Ordinal).Select(d => d.Token)
                .SequenceEqual(lamps.OrderBy(d => anchors[d.Token].X).ThenBy(d => d.Token, StringComparer.Ordinal).Select(d => d.Token));
        }
        Ground Place(LayoutDevice d, string quay)
        {
            if (d.Kind == DeviceKind.Truck) return Depot;
            if (!regions.TryGetValue(quay, out var polygon)) throw new FormatException("unknown quay " + quay);
            var minX = polygon.Min(p => p.X); var maxX = polygon.Max(p => p.X);
            var minZ = polygon.Min(p => p.Z); var maxZ = polygon.Max(p => p.Z);
            var i = Math.Max(1, d.Index);
            // Each quay has one water-facing wall: North Quay's faces south (z = minZ), East Quay's west (x = minX) and the
            // Basin's east (x = maxX). A device stands at a fraction along that wall and a depth inland of it. The side
            // quays keep their devices on the northern two thirds, which HOME holds in frame (their southern ends are
            // closer to the camera than its frustum is wide there).
            var north = quay == "hp-north-quay"; var east = quay == "hp-east-quay";
            Ground At(double fraction, double depth, double nudgeX = 0)
            {
                if (north) return new Ground(minX + (maxX - minX) * fraction, minZ + depth);
                var z = minZ + (maxZ - minZ) * fraction;
                return east ? new Ground(minX + depth + nudgeX, z) : new Ground(maxX - depth + nudgeX, z);
            }
            switch (d.Kind)
            {
                // M3: lamp x strictly follows the manifest's west-to-east order, so on a side quay each lamp steps one metre east.
                case DeviceKind.Light: return north ? At(i / 5.0, 20) : At(0.35 + 0.13 * (i - 1), 10, i);
                case DeviceKind.Gauge: return north ? At((2 * i - 1) / 6.0, 3) : At(0.45 + 0.17 * (i - 1), 3);
                case DeviceKind.LoraGauge: return At(north ? 0.62 : 0.4, 4);
                case DeviceKind.LegacyGauge: return At(north ? 0.08 : 0.3, 4);
                case DeviceKind.Pump: return At(north ? 0.43 : 0.62, 28);
                case DeviceKind.Charger: return north ? At(0.6 + 0.04 * i, 18) : At(0.68 + 0.06 * i, 18);
                case DeviceKind.Meter: return At(north ? 0.12 : 0.88, 38);
                default: throw new FormatException("no presentation anchor for " + d.Kind);
            }
        }
        public bool InsideFence(string token, Ground p) => fences.TryGetValue(token, out var ring) ? Contains(ring, p) : throw new FormatException("unknown fence " + token);
        internal static double Cross(Ground a, Ground b, Ground c) => (b.X - a.X) * (c.Z - a.Z) - (b.Z - a.Z) * (c.X - a.X);
        public static bool Contains(IReadOnlyList<Ground> ring, Ground p)
        {
            var inside = false;
            for (int i = 0, j = ring.Count - 1; i < ring.Count; j = i++)
            {
                var a = ring[j]; var b = ring[i];
                var edgeLength = Math.Sqrt((b.X - a.X) * (b.X - a.X) + (b.Z - a.Z) * (b.Z - a.Z));
                if (Math.Abs(Cross(a, b, p)) <= 1e-9 * edgeLength && p.X >= Math.Min(a.X, b.X) - 1e-9 && p.X <= Math.Max(a.X, b.X) + 1e-9 && p.Z >= Math.Min(a.Z, b.Z) - 1e-9 && p.Z <= Math.Max(a.Z, b.Z) + 1e-9) return true;
                if ((a.Z > p.Z) != (b.Z > p.Z) && p.X < (b.X - a.X) * (p.Z - a.Z) / (b.Z - a.Z) + a.X) inside = !inside;
            }
            return inside;
        }
        public DrawnFix TruckAt(IReadOnlyList<TimedFix> fixes, double wallS)
        {
            if (fixes.Count == 0) return new DrawnFix(Depot, false);
            for (var i = 1; i < fixes.Count; i++) if (fixes[i].WallS <= fixes[i - 1].WallS) throw new FormatException("truck fixes must increase in time");
            Ground Map(TimedFix f) => MapFix(f.Position.Lat, f.Position.Lon);
            var at = Map(fixes[0]);
            if (wallS >= fixes[fixes.Count - 1].WallS) at = Map(fixes[fixes.Count - 1]);
            else for (var i = 1; i < fixes.Count; i++) if (wallS >= fixes[i - 1].WallS && wallS <= fixes[i].WallS)
            {
                var a = Map(fixes[i - 1]); var b = Map(fixes[i]); var t = (wallS - fixes[i - 1].WallS) / (fixes[i].WallS - fixes[i - 1].WallS);
                at = new Ground(a.X + (b.X - a.X) * t, a.Z + (b.Z - a.Z) * t); break;
            }
            return SnapToRoad(at);
        }
        public DrawnFix SnapToRoad(Ground at)
        {
            var best = at; var distance = double.PositiveInfinity;
            for (var i = 1; i < Road.Count; i++)
            {
                var a = Road[i - 1]; var b = Road[i]; var dx = b.X - a.X; var dz = b.Z - a.Z;
                var len = dx * dx + dz * dz;
                var t = len == 0 ? 0 : Math.Max(0, Math.Min(1, ((at.X - a.X) * dx + (at.Z - a.Z) * dz) / len));
                var p = new Ground(a.X + t * dx, a.Z + t * dz); var d = (p.X - at.X) * (p.X - at.X) + (p.Z - at.Z) * (p.Z - at.Z);
                if (d < distance) { best = p; distance = d; }
            }
            return new DrawnFix(distance <= 9 ? best : at, distance > 9);
        }
        public static double Height(double metres) => metres;
        public static DrawnHeight WaterHeight(RegionLevel region) => new DrawnHeight(region?.LevelM, region != null && region.Held);
        public static DrawnHeight ReadingHeight(Reading? reading, DateTimeOffset now, TimeSpan fresh, double? last = null)
        {
            if (!reading.HasValue || double.IsNaN(reading.Value.Value) || double.IsInfinity(reading.Value.Value)) return new DrawnHeight(last, last.HasValue);
            return new DrawnHeight(reading.Value.Value, now - reading.Value.OccurredAt > fresh);
        }
        public string Disclosure(string deviceToken, LatLon? lastTruckFix = null)
        {
            var d = layout.Devices.FirstOrDefault(x => x.Token == deviceToken) ?? throw new FormatException("unknown device " + deviceToken);
            var p = d.Kind == DeviceKind.Truck ? lastTruckFix : d.Position;
            if (!p.HasValue) return "drawn here (compact layout)";
            return "manifest " + Math.Abs(p.Value.Lat).ToString("0.00000", CultureInfo.InvariantCulture) + (p.Value.Lat >= 0 ? " N, " : " S, ") + Math.Abs(p.Value.Lon).ToString("0.00000", CultureInfo.InvariantCulture) + (p.Value.Lon >= 0 ? " E" : " W") + " · drawn here (compact layout)";
        }
        static Ground[] Rect(double x0, double z0, double x1, double z1) => new[] { new Ground(x0, z0), new Ground(x1, z0), new Ground(x1, z1), new Ground(x0, z1) };
        void AddPoses()
        {
            poses.Add("pose:home", new CameraPose(new Point3(0, 110, -420), new Point3(0, 0, 40), 30));
            // The 9:16 HOME looks east along the horseshoe from over the western town: the harbour's long (x) axis then runs up
            // the tall frame. The storyboard's first draft, (0, 160, -330) looking north, holds only North Quay at 9:16 (its
            // horizontal half-angle is about 15 degrees), so 12 of 37 devices were in frame; this pose holds all three quays.
            poses.Add("pose:home:portrait", new CameraPose(new Point3(-560, 300, -60), new Point3(40, 0, 10), 52));
            poses.Add("pose:night-home", poses["pose:home"]);
            poses.Add("pose:storm-home", poses["pose:home"]);
            foreach (var pair in new[] { ("pump-north", "hp-pump-north"), ("pump-east", "hp-pump-east"), ("pump-basin", "hp-pump-basin"), ("chargers-east", "hp-charger-east-1"), ("chargers-basin", "hp-charger-basin-2"), ("lamp-east", "hp-light-east-3"), ("gauge-north", "hp-gauge-north-3"), ("gauge-east", "hp-gauge-east-1"), ("terminal", "hp-meter-ferry-terminal"), ("north-quay", "hp-light-north-1"), ("charger-north", "hp-charger-north-1") })
                foreach (var suffix in new[] { "kiosk-ext", "well", "well-locked", "pair", "close", "wide", "promenade", "insert" })
                {
                    // Stand off on the device's water side (its quay's wall faces the harbour), so a close pose looks back at
                    // the device across the quay edge instead of into the ground behind it.
                    var a = anchors[pair.Item2]; var close = suffix == "well" || suffix == "well-locked";
                    var quay = QuayFor(layout.Devices.First(d => d.Token == pair.Item2));
                    var wx = quay == "hp-east-quay" ? -1.0 : quay == "hp-basin" ? 1.0 : 0; var wz = quay == "hp-north-quay" ? -1.0 : 0;
                    var back = close ? 16 : 38; var up = close ? 10 : 24;
                    poses.Add("pose:" + pair.Item1 + ":" + suffix, new CameraPose(new Point3(a.X + wx * back, up, a.Z + wz * back), new Point3(a.X, 2, a.Z), close ? 42 : 38));
                }
            poses.Add("pose:east-quay:mouth-wide", new CameraPose(new Point3(30, 55, -180), new Point3(175, 0, 20), 40));
            var plain = fences[layout.Fences[0].Token];
            var centre = new Ground(plain.Average(p => p.X), plain.Average(p => p.Z));
            poses.Add("pose:plain:wide", new CameraPose(new Point3(centre.X - 46, 60, centre.Z - 60), new Point3(centre.X, 0, centre.Z), 45));
            var direction = Road[1]; var dx = direction.X - Depot.X; var dz = direction.Z - Depot.Z;
            var length = Math.Sqrt(dx * dx + dz * dz);
            poses.Add("pose:truck:chase", new CameraPose(new Point3(centre.X - 30 * dx / length, 14, centre.Z - 30 * dz / length), new Point3(centre.X, 0, centre.Z), 42));
            poses.Add("pose:depot:wide", new CameraPose(new Point3(Depot.X - 31, 35, Depot.Z - 43), new Point3(Depot.X, 0, Depot.Z), 45));
        }
        public CameraPose Pose(string name) => poses.TryGetValue(name, out var pose) ? pose : throw new FormatException("unknown pose " + name);
    }
}
