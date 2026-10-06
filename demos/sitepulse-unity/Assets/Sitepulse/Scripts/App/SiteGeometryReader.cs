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
            return new SiteGeometry(roads, spots, zones, pads);
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
