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
        static readonly double[] Lat = { 49.797, 49.802, 49.8025, 49.804, 49.8055, 49.8096, 49.810, 49.8104, 49.816 };
        static readonly double[] DrawZ = { -160, 60, 80, 100, 120, 147, 153, 159, 180 };
        static double Strip(double value, double[] keys, double[] values)
        {
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
            switch (d.Kind)
            {
                case DeviceKind.Light: return new Ground(minX + (maxX - minX) * i / 5, quay == "hp-north-quay" ? 95 : maxZ - (maxZ - minZ) * i / 5);
                case DeviceKind.Gauge: return new Ground(minX + (maxX - minX) * i / 4, minZ + 5);
                case DeviceKind.LoraGauge: return new Ground(minX + 8, minZ + 12);
                case DeviceKind.LegacyGauge: return new Ground(minX + 16, minZ + 12);
                case DeviceKind.Pump: return new Ground((minX + maxX) / 2 - 15, (minZ + maxZ) / 2);
                case DeviceKind.Charger: return new Ground((minX + maxX) / 2 + i * 9, minZ + 18);
                case DeviceKind.Meter: return new Ground(minX + 20, maxZ - 10);
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
            poses.Add("pose:home:portrait", new CameraPose(new Point3(0, 160, -330), new Point3(0, 0, 60), 52));
            poses.Add("pose:night-home", poses["pose:home"]);
            poses.Add("pose:storm-home", poses["pose:home"]);
            foreach (var pair in new[] { ("pump-north", "hp-pump-north"), ("pump-east", "hp-pump-east"), ("pump-basin", "hp-pump-basin"), ("chargers-east", "hp-charger-east-1"), ("chargers-basin", "hp-charger-basin-2"), ("lamp-east", "hp-light-east-3"), ("gauge-north", "hp-gauge-north-3"), ("gauge-east", "hp-gauge-east-1"), ("terminal", "hp-meter-ferry-terminal"), ("north-quay", "hp-light-north-1") })
                foreach (var suffix in new[] { "kiosk-ext", "well", "well-locked", "pair", "close", "wide", "promenade", "insert" })
                {
                    var a = anchors[pair.Item2]; var close = suffix == "well" || suffix == "well-locked";
                    poses.Add("pose:" + pair.Item1 + ":" + suffix, new CameraPose(new Point3(a.X, close ? 8 : 25, a.Z - (close ? 8 : 35)), new Point3(a.X, 1, a.Z), 38));
                }
            poses.Add("pose:east-quay:mouth-wide", new CameraPose(new Point3(30, 55, -180), new Point3(175, 0, 20), 40));
            poses.Add("pose:plain:wide", new CameraPose(new Point3(135, 60, 40), new Point3(181, 0, 100), 45));
            poses.Add("pose:truck:chase", new CameraPose(new Point3(181, 14, 130), new Point3(181, 0, 100), 42));
            poses.Add("pose:depot:wide", new CameraPose(new Point3(150, 35, 110), new Point3(181, 0, 153), 45));
        }
        public CameraPose Pose(string name) => poses.TryGetValue(name, out var pose) ? pose : throw new FormatException("unknown pose " + name);
    }
}
