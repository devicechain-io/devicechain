// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>
    /// The terrain mesh's grid lines and the surface they actually draw. Each quad is two triangles split along its
    /// (x0,z0)-(x1,z1) diagonal, exactly as the visual mesh builds it, so <see cref="MeshHeight"/> is the height a
    /// rock or a tree really stands on, not the smooth field the vertices were sampled from (the two differ by up
    /// to a metre between the 20 m grid lines on the rolling ground).
    /// </summary>
    public sealed class TerrainGrid
    {
        public IReadOnlyList<float> Xs { get; }
        public IReadOnlyList<float> Zs { get; }
        readonly float[] xs, zs;
        readonly double[] heights;

        TerrainGrid(float[] xs, float[] zs)
        {
            this.xs = xs; this.zs = zs; Xs = xs; Zs = zs;
            heights = new double[xs.Length * zs.Length];
            for (var j = 0; j < zs.Length; j++)
                for (var i = 0; i < xs.Length; i++) heights[j * xs.Length + i] = CoastalTerrain.Height(xs[i], zs[j]);
        }

        /// <summary>10 m cells round the town, 20 m across the rolling ground, then coarser out to the ridge's foot (and 200 m cells on the flat far flanks). Grid lines fall exactly on the core's edges.</summary>
        public static TerrainGrid Standard()
        {
            // (to, step) segments; every boundary is a multiple of its step from the start, so no accumulated rounding.
            var right = Line(0, new[] { (620.0, 10.0), (1500, 20), (2000, 50), (CoastalTerrain.TerrainReachM, 100), (CoastalTerrain.TerrainHalfWidthM, 200) });
            var xs = new List<float>();
            for (var i = right.Count - 1; i > 0; i--) xs.Add(-right[i]);
            xs.AddRange(right);
            var zs = Line(CoastalTerrain.CoastZ, new[] { (520.0, 10.0), (1500, 20), (2000, 50), (CoastalTerrain.TerrainReachM, 100) });
            return new TerrainGrid(xs.ToArray(), zs.ToArray());
        }

        static List<float> Line(double from, (double To, double Step)[] segments)
        {
            var v = new List<float> { (float)from };
            var at = from;
            foreach (var s in segments)
            {
                var n = (int)Math.Round((s.To - at) / s.Step);
                for (var k = 1; k <= n; k++) v.Add((float)(at + k * s.Step));
                at += n * s.Step;
            }
            return v;
        }

        /// <summary>The height the drawn triangles give at (x, z); the deck inside the core and the field itself outside the grid.</summary>
        public double MeshHeight(double x, double z)
        {
            if (Math.Abs(x) < CoastalTerrain.CoreHalfX && z < CoastalTerrain.CoreNorthZ) return SitePlan.DeckHeightM;
            if (x <= xs[0] || x >= xs[xs.Length - 1] || z <= zs[0] || z >= zs[zs.Length - 1]) return CoastalTerrain.Height(x, z);
            var i = Cell(xs, x); var j = Cell(zs, z);
            double x0 = xs[i], x1 = xs[i + 1], z0 = zs[j], z1 = zs[j + 1];
            double u = (x - x0) / (x1 - x0), v = (z - z0) / (z1 - z0);
            double a = heights[j * xs.Length + i], b = heights[j * xs.Length + i + 1];
            double c = heights[(j + 1) * xs.Length + i], d = heights[(j + 1) * xs.Length + i + 1];
            // triangle (a, c, d) where u <= v, triangle (a, d, b) where u > v
            return u <= v ? a + (d - c) * u + (c - a) * v : a + (b - a) * u + (d - b) * v;
        }

        static int Cell(float[] axis, double value)
        {
            int lo = 0, hi = axis.Length - 2;
            while (lo < hi)
            {
                var mid = (lo + hi + 1) / 2;
                if (axis[mid] <= value) lo = mid; else hi = mid - 1;
            }
            return lo;
        }
    }
}
