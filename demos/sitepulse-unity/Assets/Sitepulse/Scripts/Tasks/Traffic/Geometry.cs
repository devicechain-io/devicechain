// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;

namespace DeviceChain.Sim.Traffic
{
    /// <summary>A convex polygon on the ground: corners in order, site metres (x east, z north).</summary>
    public sealed class Polygon
    {
        public Polygon(double[] x, double[] z)
        {
            if (x.Length != z.Length || x.Length < 3) throw new ArgumentException("a polygon has at least three corners");
            X = x;
            Z = z;
        }

        public double[] X { get; }
        public double[] Z { get; }
        public int Count => X.Length;
    }

    /// <summary>The ground arithmetic the checks share; the same separations <c>quarry_topology.py</c> measures.</summary>
    public static class Geometry
    {
        /// <summary>Separation along the best axis between two convex polygons: positive is air, negative is overlap.</summary>
        public static double SatGap(Polygon a, Polygon b)
        {
            var best = double.MinValue;
            for (var pass = 0; pass < 2; pass++)
            {
                var poly = pass == 0 ? a : b;
                var n = poly.Count;
                for (var i = 0; i < n; i++)
                {
                    var j = (i + 1) % n;
                    double ex = poly.X[j] - poly.X[i], ez = poly.Z[j] - poly.Z[i];
                    var len = Math.Sqrt(ex * ex + ez * ez) + 1e-9;
                    double nx = -ez / len, nz = ex / len;
                    Project(a, nx, nz, out var minA, out var maxA);
                    Project(b, nx, nz, out var minB, out var maxB);
                    var g = Math.Max(minB - maxA, minA - maxB);
                    if (g > best) best = g;
                }
            }

            return best;
        }

        static void Project(Polygon p, double nx, double nz, out double min, out double max)
        {
            min = double.MaxValue;
            max = double.MinValue;
            for (var i = 0; i < p.Count; i++)
            {
                var d = nx * p.X[i] + nz * p.Z[i];
                if (d < min) min = d;
                if (d > max) max = d;
            }
        }

        static double PointSegment(double px, double pz, double ax, double az, double bx, double bz)
        {
            double sx = bx - ax, sz = bz - az;
            var l2 = sx * sx + sz * sz;
            var t = l2 <= 0.0 ? 0.0 : Math.Max(0.0, Math.Min(1.0, ((px - ax) * sx + (pz - az) * sz) / l2));
            var qx = px - (ax + t * sx);
            var qz = pz - (az + t * sz);
            return Math.Sqrt(qx * qx + qz * qz);
        }

        /// <summary>The distance between two convex polygons known not to overlap: the nearest corner of either to an edge of the other.</summary>
        static double Separated(Polygon a, Polygon b)
        {
            var best = double.MaxValue;
            for (var pass = 0; pass < 2; pass++)
            {
                var p = pass == 0 ? a : b;
                var q = pass == 0 ? b : a;
                for (var i = 0; i < q.Count; i++)
                {
                    var j = (i + 1) % q.Count;
                    for (var k = 0; k < p.Count; k++)
                        best = Math.Min(best, PointSegment(p.X[k], p.Z[k], q.X[i], q.Z[i], q.X[j], q.Z[j]));
                }
            }

            return best;
        }

        /// <summary>
        /// The clear distance between two convex polygons (Euclidean: the nearest point of one to the other), negative when they overlap (then
        /// <see cref="SatGap"/>'s depth). The gap along an edge normal alone is less than the distance where two polygons face each other corner
        /// to corner; every comparison of ground in the checks is this distance.
        /// </summary>
        public static double Distance(Polygon a, Polygon b)
        {
            var g = SatGap(a, b);
            return g <= 0.0 ? g : Separated(a, b);
        }

        /// <summary>
        /// The smallest distance between any polygon of one group and any of the other. Exact when it is less than <paramref name="limit"/>;
        /// otherwise <paramref name="limit"/> or more (the edge-normal gap, never more than the distance, is enough to rule a pair out).
        /// </summary>
        public static double GroupGap(IReadOnlyList<Polygon> a, IReadOnlyList<Polygon> b, double limit = double.MaxValue)
        {
            var best = double.MaxValue;
            foreach (var p in a)
                foreach (var q in b)
                {
                    var s = SatGap(p, q);
                    if (s >= best || s >= limit) continue;
                    var e = s <= 0.0 ? s : Separated(p, q);
                    if (e < best) best = e;
                }

            return limit < double.MaxValue ? Math.Min(best, limit) : best;
        }

        /// <summary>The convex hull of a set of points, counter-clockwise (Andrew's monotone chain, corners rounded to a millimetre as the generator does).</summary>
        public static Polygon Hull(IEnumerable<(double X, double Z)> points)
        {
            var pts = new SortedSet<(double, double)>();
            foreach (var p in points) pts.Add((Math.Round(p.X, 3, MidpointRounding.ToEven), Math.Round(p.Z, 3, MidpointRounding.ToEven)));
            var list = new List<(double X, double Z)>(pts.Select(p => (p.Item1, p.Item2)));
            if (list.Count < 3) throw new ArgumentException("a hull needs three distinct points");
            double Cross((double X, double Z) o, (double X, double Z) a, (double X, double Z) b) => (a.X - o.X) * (b.Z - o.Z) - (a.Z - o.Z) * (b.X - o.X);
            var lo = new List<(double X, double Z)>();
            foreach (var p in list)
            {
                while (lo.Count >= 2 && Cross(lo[lo.Count - 2], lo[lo.Count - 1], p) <= 0) lo.RemoveAt(lo.Count - 1);
                lo.Add(p);
            }

            var up = new List<(double X, double Z)>();
            for (var i = list.Count - 1; i >= 0; i--)
            {
                var p = list[i];
                while (up.Count >= 2 && Cross(up[up.Count - 2], up[up.Count - 1], p) <= 0) up.RemoveAt(up.Count - 1);
                up.Add(p);
            }

            lo.RemoveAt(lo.Count - 1);
            up.RemoveAt(up.Count - 1);
            lo.AddRange(up);
            return new Polygon(lo.Select(p => p.X).ToArray(), lo.Select(p => p.Z).ToArray());
        }

        /// <summary>The middle of a group's corners and the radius of the circle round them from there.</summary>
        public static void CenterRadius(IReadOnlyList<Polygon> group, out double cx, out double cz, out double radius)
        {
            double sx = 0.0, sz = 0.0;
            var n = 0;
            foreach (var p in group)
                for (var i = 0; i < p.Count; i++)
                {
                    sx += p.X[i];
                    sz += p.Z[i];
                    n++;
                }

            cx = sx / n;
            cz = sz / n;
            var r2 = 0.0;
            foreach (var p in group)
                for (var i = 0; i < p.Count; i++)
                    r2 = Math.Max(r2, (p.X[i] - cx) * (p.X[i] - cx) + (p.Z[i] - cz) * (p.Z[i] - cz));
            radius = Math.Sqrt(r2);
        }

        /// <summary>Whether (x, z) lies in or on a convex polygon whose corners run counter-clockwise.</summary>
        public static bool InPolygon(Polygon poly, double x, double z)
        {
            var n = poly.Count;
            for (var i = 0; i < n; i++)
            {
                var j = (i + 1) % n;
                if ((poly.X[j] - poly.X[i]) * (z - poly.Z[i]) - (poly.Z[j] - poly.Z[i]) * (x - poly.X[i]) < -1e-9) return false;
            }

            return true;
        }

        /// <summary>The shortest angle from b to a, in degrees, -180 to 180.</summary>
        public static double Wrap(double degrees) => ((degrees + 180.0) % 360.0 + 360.0) % 360.0 - 180.0;
    }
}
