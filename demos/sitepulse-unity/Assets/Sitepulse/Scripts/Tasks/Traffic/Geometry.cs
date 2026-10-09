// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

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

        /// <summary>The smallest separation between any polygon of one group and any of the other.</summary>
        public static double GroupGap(IReadOnlyList<Polygon> a, IReadOnlyList<Polygon> b)
        {
            var best = double.MaxValue;
            foreach (var p in a)
                foreach (var q in b)
                    best = Math.Min(best, SatGap(p, q));
            return best;
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
