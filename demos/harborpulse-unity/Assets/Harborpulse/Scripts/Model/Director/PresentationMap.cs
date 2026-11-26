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
        // a pose's own 9:16 version, where one frame cannot serve both shapes (keyed by the pose itself: a shot hands the camera a pose, not a name)
        readonly Dictionary<CameraPose, CameraPose> tall = new Dictionary<CameraPose, CameraPose>();
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
            if (layout.Devices.Count != DeviceCount) throw new FormatException("M1: authored presentation expects " + DeviceCount + " device anchors");
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

        /// <summary>
        /// How many devices the presentation is authored for: the district's 37 and the six that hold their own session (two chargers on MQTT
        /// under a token, two under a certificate, two lamps that ask for their commands). Any other count is refused.
        /// </summary>
        public const int DeviceCount = 43;

        /// <summary>North Quay's charging row: the first bay's share of the wall's length, and each next bay's (9 m).</summary>
        public const double NorthChargerRow = .6, NorthChargerPitch = .04;

        /// <summary>
        /// Where a charger with its own session stands in North Quay's row, counting bays from the district's first: the two on a token take
        /// the third and fourth, the fifth is left to the fourth street light, and the two on a certificate take the sixth and seventh.
        /// </summary>
        public static int LinkedBay(string kind, int index) => (kind == DeviceKind.CertCharger ? 5 : 2) + Math.Max(1, index);

        /// <summary>The pull lamps stand at North Quay's east end, by the wall: how far the first is from the end, their spacing, and their depth behind the wall.</summary>
        public const double PullLampFromEndM = 14.5, PullLampSpacingM = 12, PullLampDepthM = 3;
        /// <summary>
        /// On a side quay, should the platform put them there: how far the first pull lamp stands from the wall's southern end. The ladder
        /// between the two is then 26 m from that end, clear of the bollards and cleats set every 28 m along either side quay's wall.
        /// </summary>
        public const double SidePullLampFromEndM = 20;

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
                // The manifest puts the six on North Quay, where they are authored: the chargers continue the charging row, the lamps
                // flank the landing at the wall's east end. Should the platform report one on a side quay, the chargers stand in a row
                // of their own well behind the wall there, clear of everything that quay was laid out with, and the lamps by the wall
                // near its southern end, the same distance behind it and apart: a landing is at the water wherever it is.
                case DeviceKind.MqttCharger: case DeviceKind.CertCharger:
                    return north ? At(NorthChargerRow + NorthChargerPitch * LinkedBay(d.Kind, i), 18) : At((d.Kind == DeviceKind.CertCharger ? .34 : .10) + .08 * i, 46);
                case DeviceKind.PullLight:
                    return north ? new Ground(maxX - PullLampFromEndM + PullLampSpacingM * (i - 1), minZ + PullLampDepthM)
                        : At((SidePullLampFromEndM + PullLampSpacingM * (i - 1)) / (maxZ - minZ), PullLampDepthM);
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
            // HOME, 16:9: lower and flatter than the first cut, wider, aimed above the water so the harbour sits in the lower
            // four fifths and the horizon falls a fifth of the way down; both breakwater lighthouses, the mouth and all three
            // quays stay inside the frame with a margin, and its bottom edge meets the water south of z = -190.
            poses.Add("pose:home", new CameraPose(new Point3(0, 100, -420), new Point3(0, HomeLookY, 50), 35));
            // The 9:16 HOME looks east along the horseshoe from over the western town: the harbour's long (x) axis then runs up
            // the tall frame. The storyboard's first draft, (0, 160, -330) looking north, holds only North Quay at 9:16 (its
            // horizontal half-angle is about 15 degrees), so 12 of the then 37 devices were in frame; this pose holds all three quays.
            // The land now stands far below this eye, so the haze-covered far ground ends well under the true horizon; the look is
            // steepened slightly (the horizon just above the frame) so the air over it is 8-14 % of the frame and the fog closes the view.
            // U6: the eye is lower and nearer and the look is flattened, so the horizon falls an eighth of the way down the frame: the
            // sky is in it, the sun rises in it (the camera looks a little north of east), the harbour holds the middle of the frame (U7) and the eastern uplands close the view.
            poses.Add("pose:home:portrait", Stand(-700, 235, -60, 0, -5, 44, PortraitHorizon));
            // where the dawn shot's crane starts: higher and further out over the water, on HOME's own line of sight
            poses.Add(StoryPoses.HomeHigh, new CameraPose(new Point3(0, 168, -535), new Point3(0, HomeLookY, 50), 35));
            poses.Add("pose:night-home", poses["pose:home"]);
            poses.Add("pose:storm-home", poses["pose:home"]);
            foreach (var pair in StoryPoses.Families)
            {
                var a = anchors[pair.Token];
                // Stand off on the device's water side (its quay's wall faces the harbour), so a close pose looks back at
                // the device across the quay edge instead of into the ground behind it.
                var quay = QuayFor(layout.Devices.First(d => d.Token == pair.Token));
                var wx = quay == "hp-east-quay" ? -1.0 : quay == "hp-basin" ? 1.0 : 0; var wz = quay == "hp-north-quay" ? -1.0 : 0;
                var isLamp = layout.Devices.First(d => d.Token == pair.Token).Kind == DeviceKind.Light;
                foreach (var suffix in StoryPoses.GroundSuffixes)
                {
                    if (isLamp)
                    {
                        // A lamp's story is its head, 8.3 m above the deck: stand 20-24 m off, 25-35 degrees round from the quay's axis, eye 5-6 m,
                        // and look up a little (horizon below the middle) so the whole lamp, lantern included, stands against the sky.
                        double lamp, lampEye, lampFov = 46;
                        switch (suffix)
                        {
                            case "kiosk-ext": lamp = 23; lampEye = 5.5; break;
                            case "pair": lamp = 22; lampEye = 5.5; break;
                            case "close": lamp = 20; lampEye = 5; break;
                            case "wide": lamp = 24; lampEye = 6; lampFov = 48; break;
                            case "promenade": lamp = 23; lampEye = 5.5; break;
                            default: lamp = 21; lampEye = 5; break;
                        }
                        var turn = LampTurn(pair.Family) * Math.PI / 180;
                        var dx2 = wx * Math.Cos(turn) - wz * Math.Sin(turn); var dz2 = wx * Math.Sin(turn) + wz * Math.Cos(turn);
                        poses.Add("pose:" + pair.Family + ":" + suffix, Stand(a.X + dx2 * lamp, lampEye, a.Z + dz2 * lamp, a.X, a.Z, lampFov, LampHorizon));
                        continue;
                    }
                    // Ground-level views: the eye is 5-8 m above the deck and the look is flattened until the horizon falls about
                    // 30 % of the way down, so the sky shows and the device stays in the middle third of the frame.
                    double back, eye, fov, horizon = .29;
                    switch (suffix)
                    {
                        case "kiosk-ext": back = 36; eye = 8; fov = 38; break;
                        case "pair": back = 34; eye = 7.6; fov = 38; break;
                        case "close": back = 22; eye = 6.6; fov = 36; break;
                        case "wide": back = 52; eye = 10; fov = 44; horizon = .30; break;
                        case "promenade": back = 44; eye = 8.6; fov = 40; horizon = .30; break;
                        default: back = 28; eye = 7; fov = 36; break;
                    }
                    // Some stand-off lines run straight through a lamp or a gauge mast on the quay edge; a sideways step of the eye clears them.
                    var side = Sidestep(pair.Family, suffix);
                    poses.Add("pose:" + pair.Family + ":" + suffix, Stand(a.X + wx * back + (wx == 0 ? side : 0), eye, a.Z + wz * back + (wx == 0 ? 0 : side), a.X, a.Z, fov, horizon));
                }
                // The cut-away view looks down into the wet well. A pump's well is not at its anchor: it stands 13 m along the quay from the
                // pump house (WellFootprint, moved there to clear a lamp), and a pose aimed at the anchor framed the pump house's door with
                // the well out of the frame. Every other family keeps its anchor.
                var well = a;
                if (layout.Devices.First(d => d.Token == pair.Token).Kind == DeviceKind.Pump)
                    well = new PropFootprint("well", a, 1, 1, WorldPropPlan.QuayYaw(quay)).Local(WellFootprint.LocalX, WellFootprint.LocalZ);
                // Every well is looked into from the same stand-off (WorldPropPlan.InWellView keeps charging bays out of the line: East Quay's
                // had a canopy in it, and its view was a nearer, higher one over a canopy that still filled the lower left). The look is at
                // a point under the deck, WellViewLookY: the well and its three level tags then stand in the upper two thirds of the frame,
                // and the caption's lines (the lower quarter, HarborStoryHud) are written under the "low cut-off" tag instead of across it.
                // A 9:16 frame is a quarter as wide for its height and its caption is written higher (the lower 23-37 %, with the rail's
                // nodes across the frame from 60 % up), so the same eye wrote "low cut-off" inside the caption's box, cut the attribute
                // rule's tag off at the frame's edge and put the well behind the rail. The tall view stands twice as far back and as high on
                // the same line, looking at the same point: the well and all four tags then stand between the caption and the rail, and
                // the text in a 1080 x 1920 frame is nine tenths the size, in pixels, that the 16:9 view gives it in a 1920 x 1080 one.
                var tallWell = new CameraPose(new Point3(well.X + wx * WellViewBackM * WellViewTallScale, WellViewEyeM * WellViewTallScale, well.Z + wz * WellViewBackM * WellViewTallScale),
                    new Point3(well.X, WellViewLookY, well.Z), 42);
                foreach (var suffix in StoryPoses.WellSuffixes)
                {
                    var wide = new CameraPose(new Point3(well.X + wx * WellViewBackM, WellViewEyeM, well.Z + wz * WellViewBackM), new Point3(well.X, WellViewLookY, well.Z), 42);
                    poses.Add("pose:" + pair.Family + ":" + suffix, wide);
                    poses.Add("pose:" + pair.Family + ":" + suffix + StoryPoses.TallSuffix, tallWell);
                    tall.Add(wide, tallWell);
                }
            }
            // A view only a cut uses. The rise: up and back over the water off East Quay from its lamp's wide view, until the whole dark quay is in the frame.
            var east = anchors["hp-light-east-3"];
            poses.Add(StoryPoses.EastRise, new CameraPose(new Point3(east.X - 78, 34, east.Z - 14), new Point3(east.X + 4, 3, east.Z), 46));
            // Outside the quay the eye sits just above the water and the look is flattened to put the horizon about a third of the way down.
            // from inside the harbour, over the water off the basin, looking east-south-east across the quay's devices: no breakwater or lighthouse is
            // in front of the subject, and the east light stands at the right edge in the middle distance
            poses.Add("pose:east-quay:mouth-wide", Stand(-130, 9.5, 70, 150, -10, 56, .36));
            // The night sky: HOME looks north and the sun and the moon cross the south, so neither ever enters it. This stands just off the
            // east quay's north end and looks west-south-west across the water at the basin's lamps and the west light, with the horizon
            // low in the frame: at StoryPoses.NightSkyHour the moon stands in the middle of it, 18 degrees up. Not a story pose.
            poses.Add(StoryPoses.NightSky, Stand(118, 6.5, 56, -144, -89, 50, .62));
            var plain = fences[layout.Fences[0].Token];
            var centre = new Ground(plain.Average(p => p.X), plain.Average(p => p.Z));
            poses.Add("pose:plain:wide", Stand(centre.X - 46, 10, centre.Z - 60, centre.X, centre.Z, 45, .30));
            var direction = Road[1]; var dx = direction.X - Depot.X; var dz = direction.Z - Depot.Z;
            var length = Math.Sqrt(dx * dx + dz * dz);
            poses.Add("pose:truck:chase", Stand(centre.X - 30 * dx / length, 6.5, centre.Z - 30 * dz / length, centre.X, centre.Z, 42, .30));
            // from the south-east, so the yard building is not between the eye and the truck
            poses.Add("pose:depot:wide", Stand(Depot.X + 24, 10, Depot.Z - 43, Depot.X, Depot.Z, 45, .30));
        }

        /// <summary>Sideways step (metres along the quay's wall) of a family's ground-level eye, so its sight line passes beside the lamps and masts on the quay edge.</summary>
        static double LampTurn(string family) => family == "lamp-east" ? -30 : 30;

        // (U8: the Basin's chargers are the derate page's subject, and a tide gauge's mast on the quay edge stood between the eye and the
        // second charger in its close and insert views. The eye steps 14 m towards the quay's north end and looks back past the mast.)
        // U9: that step put the wide view's eye, 52 m off the quay, over the berthed ferry: its cabin roof and mast filled the lower third
        // of the page's last shot. The wide view stands off the ferry's stern instead (BasinWideSidestepM), south of the pontoon, and looks
        // north-west at the chargers with the terminal behind them and the berth at the frame's right.)
        static double Sidestep(string family, string suffix) => family == "pump-east" || family == "pump-basin" ? 4 : family == "chargers-east" ? -16 :
            family == "chargers-basin" ? (suffix == "wide" ? BasinWideSidestepM : 14) : 0;

        /// <summary>How far along the quay (negative: south) the eye of the Basin chargers' wide view stands from the chargers' own line.</summary>
        public const double BasinWideSidestepM = -34;

        /// <summary>A pose from an eye position looking at a ground point's x and z, its pitch chosen so the horizon falls at <paramref name="horizonFromTop"/> of the frame.</summary>
        /// <summary>Where the horizon falls in a lamp pose, from the top: below the middle, so the lantern has sky behind it.</summary>
        public const double LampHorizon = .52;

        /// <summary>Where the horizon falls in the 9:16 HOME, from the top.</summary>
        public const double PortraitHorizon = .125;

        /// <summary>
        /// The height the 16:9 HOME looks at, 470 m in front of it. It was 4: the ranges added in front of the far ridge filled the ridge's
        /// saddles and took half a point of sky, and the tree line now drawn on every crest takes a little more, so the look is raised by
        /// under half a degree. The ranges keep their height and the sky has its fifth of the frame again.
        /// </summary>
        public const double HomeLookY = 7.5;
        /// <summary>The wet well's cut-away view: how far the eye stands off on the water side, how high, and the height it looks at (under the deck).</summary>
        public const double WellViewBackM = 16, WellViewEyeM = 10, WellViewLookY = 0;
        /// <summary>The well's view in a 9:16 frame: how many times further off and higher its eye stands, on the same line.</summary>
        public const double WellViewTallScale = 2;

        static CameraPose Stand(double camX, double eye, double camZ, double subjectX, double subjectZ, double vfov, double horizonFromTop)
        {
            var distance = Math.Sqrt((subjectX - camX) * (subjectX - camX) + (subjectZ - camZ) * (subjectZ - camZ));
            var down = (1 - 2 * horizonFromTop) * CameraMath.TanHalf(vfov);
            return new CameraPose(new Point3(camX, eye, camZ), new Point3(subjectX, eye - distance * down, subjectZ), vfov);
        }
        public CameraPose Pose(string name) => poses.TryGetValue(name, out var pose) ? pose : throw new FormatException("unknown pose " + name);

        /// <summary>The pose a 9:16 frame takes in place of <paramref name="pose"/>: its own tall version where it has one, else the pose itself.</summary>
        public CameraPose Tall(CameraPose pose) => pose != null && tall.TryGetValue(pose, out var t) ? t : pose;
    }
}
