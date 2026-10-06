// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>A route as the highlight draws it: points on the ground in the site's metres (Unity X east, Z north), start to end.</summary>
    public sealed class RoutePolyline
    {
        readonly float[] xs, zs, cumulative;

        public RoutePolyline(IReadOnlyList<double> x, IReadOnlyList<double> z)
        {
            if (x == null || z == null || x.Count != z.Count) throw new ArgumentException("a route needs as many X as Z");
            xs = new float[x.Count];
            zs = new float[x.Count];
            cumulative = new float[x.Count];
            for (var i = 0; i < x.Count; i++)
            {
                xs[i] = (float)x[i];
                zs[i] = (float)z[i];
                if (i > 0) cumulative[i] = cumulative[i - 1] + (float)Math.Sqrt((xs[i] - xs[i - 1]) * (xs[i] - xs[i - 1]) + (zs[i] - zs[i - 1]) * (zs[i] - zs[i - 1]));
            }
        }

        public int Count => xs.Length;
        public float X(int i) => xs[i];
        public float Z(int i) => zs[i];
        public float Length => cumulative.Length == 0 ? 0f : cumulative[cumulative.Length - 1];

        /// <summary>The distance of point <paramref name="i"/> from the start.</summary>
        public float At(int i) => cumulative[i];

        public static RoutePolyline Of(Route r) => r == null ? null : new RoutePolyline(r.Xs, r.Zs);

        /// <summary>A route from a flat list x0, z0, x1, z1, ...; null for an empty list (the route ended) or an odd one (not a route).</summary>
        public static RoutePolyline FromFlat(IReadOnlyList<double> flat)
        {
            if (flat == null || flat.Count < 4 || (flat.Count & 1) != 0) return null;
            var n = flat.Count / 2;
            var x = new double[n];
            var z = new double[n];
            for (var i = 0; i < n; i++)
            {
                x[i] = flat[2 * i];
                z[i] = flat[2 * i + 1];
            }

            return new RoutePolyline(x, z);
        }

        /// <summary>The route as the flat list a recording keeps, to a tenth of a metre.</summary>
        public static List<double> ToFlat(Route r)
        {
            var list = new List<double>();
            if (r == null) return list;
            for (var i = 0; i < r.Xs.Count; i++)
            {
                list.Add(Math.Round(r.Xs[i], 1));
                list.Add(Math.Round(r.Zs[i], 1));
            }

            return list;
        }
    }

    /// <summary>Hands the same <see cref="RoutePolyline"/> back for as long as a controller drives the same route, so a view redraws only when the route changes.</summary>
    public sealed class RouteCache
    {
        readonly Dictionary<string, (Route route, RoutePolyline line)> held = new Dictionary<string, (Route, RoutePolyline)>(StringComparer.Ordinal);

        public RoutePolyline Of(string id, Route route)
        {
            if (route == null || route.Legs == 0)
            {
                held.Remove(id);
                return null;
            }

            if (held.TryGetValue(id, out var h) && ReferenceEquals(h.route, route)) return h.line;
            var line = RoutePolyline.Of(route);
            held[id] = (route, line);
            return line;
        }
    }
}
