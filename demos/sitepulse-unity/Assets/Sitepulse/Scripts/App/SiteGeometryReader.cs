// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text.Json;
using DeviceChain.Sitepulse.Tasks;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>
    /// Reads what routing needs from the quarry feature file (<c>quarry_features.json</c>): the roads
    /// (centreline points as <c>[x, y, z]</c>), the named spots, the zones and the pads. The rect convention
    /// of the file is <c>[x0, x1, z0, z1]</c> (its generator's own), not x, z, width, height. Hand-read with
    /// <see cref="JsonElement"/>: nested arrays are beyond Unity's own JSON utility and a reflection serializer
    /// is not safe under IL2CPP. A file that does not say what the router needs is refused, loudly.
    /// </summary>
    public static class SiteGeometryReader
    {
        public static SiteGeometry Parse(string featuresJson)
        {
            if (string.IsNullOrWhiteSpace(featuresJson)) throw new FormatException("the feature file is empty");
            using var doc = JsonDocument.Parse(featuresJson);
            var root = doc.RootElement;

            var roads = new List<RoadLine>();
            foreach (var r in Array(root, "roads"))
            {
                var points = new List<RoadPoint>();
                foreach (var p in Array(r, "points"))
                {
                    if (p.ValueKind != JsonValueKind.Array || p.GetArrayLength() < 3) throw new FormatException("a road point is not [x, y, z]");
                    points.Add(new RoadPoint(p[0].GetDouble(), p[1].GetDouble(), p[2].GetDouble()));
                }

                roads.Add(new RoadLine(Str(r, "name"), Str(r, "kind"), r.TryGetProperty("width", out var w) && w.ValueKind == JsonValueKind.Number ? w.GetDouble() : 0.0, points));
            }

            var spots = new Dictionary<string, Spot>(StringComparer.Ordinal);
            if (root.TryGetProperty("spots", out var sp) && sp.ValueKind == JsonValueKind.Object)
                foreach (var s in sp.EnumerateObject())
                {
                    var o = s.Value;
                    spots[s.Name] = new Spot(s.Name, Num(o, "x"), Num(o, "z"), o.TryGetProperty("heading", out var h) && h.ValueKind == JsonValueKind.Number ? h.GetDouble() : 0.0);
                }

            var zones = new List<Rect2>();
            foreach (var z in Array(root, "zones")) zones.Add(Rect(z, Str(z, "token")));
            var pads = new List<Rect2>();
            if (root.TryGetProperty("pads", out var pd) && pd.ValueKind == JsonValueKind.Array)
                foreach (var p in pd.EnumerateArray()) pads.Add(Rect(p, Str(p, "name")));

            if (roads.Count == 0) throw new FormatException("the feature file lists no roads");
            if (zones.Count == 0) throw new FormatException("the feature file lists no zones");
            return new SiteGeometry(roads, spots, zones, pads, Obstacles(root, spots));
        }

        /// <summary>
        /// Half extents (m) along a prop's own X and Z, read off the generator that models it
        /// (ArtSource/props/build_props.py), outermost box of the piece including its slab, bund, steps or drawbar.
        /// A prop type the file lists with no entry here is refused: a parking slot must never be judged against a size nobody wrote down.
        /// </summary>
        static readonly Dictionary<string, (double X, double Z)> PropHalfExtents = new Dictionary<string, (double, double)>(StringComparer.Ordinal)
        {
            ["site_office"] = (4.85, 2.5),       // L 4.8, W 1.5; steps reach to W + 1.0
            ["workshop"] = (9.3, 6.3),           // X 9.0, Zh 6.0, slab +0.3
            ["container_blue"] = (3.03, 1.22),
            ["container_red"] = (3.03, 1.22),
            ["fuel_tank"] = (5.2, 3.5),          // bund BX 5.2, BZ 2.3, dispenser out to BZ + 1.2
            ["light_tower"] = (2.5, 1.7),        // body 1.35, drawbar to 2.5, outriggers out to 1.5 + pad
            ["cone"] = (0.2, 0.2),
            ["barrier"] = (1.48, 0.3),           // prism 2.96 m long, 0.6 m wide at the foot
            ["site_sign"] = (1.65, 0.3),         // panel 3.2 m, posts
            ["crusher_plant"] = (22.0, 16.0),    // stands in no zone; a generous bound of the plant's footprint, not a measurement
        };

        /// <summary>The refuel approach: a machine queueing for, or in, the bay holds this strip clear (radius = a hauler's footprint).</summary>
        const double ApproachRadius = 6.4;

        /// <summary>The slope a pile's toe reaches along, 1.3 x its height over tan(37 deg) (ArtSource/terrain/quarry_heightmap.py, REPOSE_DEG and the pile's toe wobble).</summary>
        static double PileRadius(double height) => height / Math.Tan(37.0 * Math.PI / 180.0) * 1.3;

        static List<Obstacle> Obstacles(JsonElement root, IReadOnlyDictionary<string, Spot> spots)
        {
            var list = new List<Obstacle>();
            if (root.TryGetProperty("props", out var props) && props.ValueKind == JsonValueKind.Array)
                foreach (var p in props.EnumerateArray())
                {
                    var type = Str(p, "p");
                    if (!PropHalfExtents.TryGetValue(type, out var half)) throw new FormatException($"the feature file lists a \"{type}\" prop with no known footprint");
                    list.Add(Obstacle.Box(type, Num(p, "x"), Num(p, "z"), half.X, half.Z, p.TryGetProperty("heading", out var hd) && hd.ValueKind == JsonValueKind.Number ? hd.GetDouble() : 0.0));
                }

            if (root.TryGetProperty("piles", out var piles) && piles.ValueKind == JsonValueKind.Array)
                foreach (var p in piles.EnumerateArray())
                {
                    var x = Num(p, "x");
                    var z = Num(p, "z");
                    var len = p.TryGetProperty("len", out var l) && l.ValueKind == JsonValueKind.Number ? l.GetDouble() : 0.0;
                    var heading = (p.TryGetProperty("heading", out var hd) && hd.ValueKind == JsonValueKind.Number ? hd.GetDouble() : 0.0) * Math.PI / 180.0;
                    var ux = Math.Sin(heading) * len / 2.0;
                    var uz = Math.Cos(heading) * len / 2.0;
                    list.Add(Obstacle.Capsule(Str(p, "name"), x - ux, z - uz, x + ux, z + uz, PileRadius(Num(p, "h"))));
                }

            if (spots.TryGetValue(RouteGraph.QueueSpot, out var queue) && spots.TryGetValue(RouteGraph.BaySpot, out var bay))
                list.Add(Obstacle.Capsule("refuel-approach", queue.X, queue.Z, bay.X, bay.Z, ApproachRadius));
            return list;
        }

        static IEnumerable<JsonElement> Array(JsonElement o, string name)
        {
            if (!o.TryGetProperty(name, out var a) || a.ValueKind != JsonValueKind.Array) throw new FormatException($"the feature file has no \"{name}\" list");
            return a.EnumerateArray();
        }

        static string Str(JsonElement o, string name) =>
            o.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.String ? v.GetString() : throw new FormatException($"a feature has no \"{name}\"");

        static double Num(JsonElement o, string name) =>
            o.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.Number ? v.GetDouble() : throw new FormatException($"a feature has no number \"{name}\"");

        static Rect2 Rect(JsonElement o, string name)
        {
            if (!o.TryGetProperty("rect", out var r) || r.ValueKind != JsonValueKind.Array || r.GetArrayLength() != 4) throw new FormatException($"\"{name}\" has no rect [x0, x1, z0, z1]");
            return new Rect2(name, r[0].GetDouble(), r[1].GetDouble(), r[2].GetDouble(), r[3].GetDouble());
        }
    }
}
