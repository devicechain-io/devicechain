// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.IO.Compression;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>The ground's height anywhere on the site, in metres.</summary>
    public interface IHeightField
    {
        double HeightAt(double x, double z);
    }

    /// <summary>
    /// The quarry's ground as the terrain is built from it (<c>ArtSource/terrain/quarry_heightmap.py</c>): the packed heightmap decoded,
    /// and read with the same bilinear lookup the choreography generator grades its speeds with, so the task layer and the generator
    /// agree on how steep a place is. No Unity types: the route network is planned against it in an EditMode test as in the scene.
    /// </summary>
    public sealed class TerrainHeights : IHeightField
    {
        readonly float[,] h;
        readonly int res;
        readonly double cell, x0, z0, baseY, range;

        /// <param name="normalised">Heights 0..1 indexed [row along z from the south, column along x from the west].</param>
        /// <param name="sizeMetres">Side of the square the grid covers.</param>
        /// <param name="x0">West edge, metres.</param>
        /// <param name="z0">South edge, metres.</param>
        /// <param name="baseY">World height of a sample of 0.</param>
        /// <param name="rangeMetres">World height of a sample of 1, above <paramref name="baseY"/>.</param>
        public TerrainHeights(float[,] normalised, double sizeMetres, double x0, double z0, double baseY, double rangeMetres)
        {
            h = normalised ?? throw new ArgumentNullException(nameof(normalised));
            res = normalised.GetLength(0);
            if (res < 2 || normalised.GetLength(1) != res) throw new ArgumentException("a heightmap is a square of at least 2 samples", nameof(normalised));
            cell = sizeMetres / (res - 1);
            this.x0 = x0;
            this.z0 = z0;
            this.baseY = baseY;
            range = rangeMetres;
        }

        public double HeightAt(double x, double z)
        {
            var c = (x - x0) / cell;
            var r = (z - z0) / cell;
            var c0 = Math.Max(0, Math.Min(res - 2, (int)Math.Floor(c)));
            var r0 = Math.Max(0, Math.Min(res - 2, (int)Math.Floor(r)));
            double fc = c - c0, fr = r - r0;
            var v = h[r0, c0] * (1 - fc) * (1 - fr) + h[r0, c0 + 1] * fc * (1 - fr) + h[r0 + 1, c0] * (1 - fc) * fr + h[r0 + 1, c0 + 1] * fc * fr;
            return baseY + v * range;
        }

        /// <summary>
        /// Unpacks the heightmap: gzip of plane-predicted uint16 samples, row 0 = south edge. Each sample was stored as
        /// s - (west + south - southwest) mod 65536.
        /// </summary>
        public static float[,] Decode(byte[] packed, int res)
        {
            var raw = new byte[res * res * 2];
            using (var gz = new GZipStream(new MemoryStream(packed), CompressionMode.Decompress))
            {
                var off = 0;
                while (off < raw.Length)
                {
                    var n = gz.Read(raw, off, raw.Length - off);
                    if (n <= 0) throw new InvalidDataException($"heightmap ends after {off} of {raw.Length} bytes");
                    off += n;
                }
            }

            var s = new int[res * res];
            for (var j = 0; j < res; j++)
            {
                for (var i = 0; i < res; i++)
                {
                    var k = j * res + i;
                    var pred = (i > 0 ? s[k - 1] : 0) + (j > 0 ? s[k - res] : 0) - (i > 0 && j > 0 ? s[k - res - 1] : 0);
                    var r = raw[2 * k] | (raw[2 * k + 1] << 8);
                    s[k] = (r + pred) & 0xFFFF;
                }
            }

            var h = new float[res, res];
            for (var j = 0; j < res; j++)
                for (var i = 0; i < res; i++)
                    h[j, i] = s[j * res + i] / 65535f;
            return h;
        }
    }

    /// <summary>
    /// How steep a path is, as a haul truck feels it. A slope is the rise over a stretch long enough to be one
    /// (<see cref="WindowMetres"/>), and a step in the ground is a rise over a few metres (<see cref="StepMetres"/>) that a truck
    /// cannot take however short it is: a berm or a pit wall's lip. Both are reported on one scale, in percent, so one limit
    /// (<see cref="MaxPct"/>) covers them: a rise of <see cref="MaxStepPct"/> % over a step counts as <see cref="MaxPct"/> %.
    /// </summary>
    public static class Grade
    {
        /// <summary>The steepest a route may be, in percent (real haul roads run 8 to 10 % sustained and 12 % at the most).</summary>
        public const double MaxPct = 12.0;

        /// <summary>The length over which a rise is a grade, the shorter length over which a rise is a step, and the spacing the ground is read at, in metres.</summary>
        public const double WindowMetres = 10.0, StepMetres = 4.0, SampleMetres = 1.0;

        /// <summary>The steepest rise over <see cref="StepMetres"/> a truck takes, in percent.</summary>
        public const double MaxStepPct = 25.0;

        /// <summary>The steepest grade as a truck feels it (see the class) anywhere along the path, in percent.</summary>
        public static double MaxSustained(IHeightField ground, IReadOnlyList<double> xs, IReadOnlyList<double> zs) => Windows(ground, xs, zs, null);

        /// <summary>As <see cref="MaxSustained"/>, for each leg of the path: the steepest stretch that touches it.</summary>
        public static double[] PerLeg(IHeightField ground, IReadOnlyList<double> xs, IReadOnlyList<double> zs)
        {
            var legs = new double[Math.Max(0, xs.Count - 1)];
            Windows(ground, xs, zs, legs);
            return legs;
        }

        static double Windows(IHeightField ground, IReadOnlyList<double> xs, IReadOnlyList<double> zs, double[] legs)
        {
            if (ground == null) throw new ArgumentNullException(nameof(ground));
            var worst = 0.0;
            if (xs.Count < 2) return worst;
            // read the ground every SampleMetres along the path, remembering which leg each reading is on
            var cum = new double[xs.Count];
            for (var i = 1; i < xs.Count; i++) cum[i] = cum[i - 1] + Math.Sqrt((xs[i] - xs[i - 1]) * (xs[i] - xs[i - 1]) + (zs[i] - zs[i - 1]) * (zs[i] - zs[i - 1]));
            var total = cum[cum.Length - 1];
            if (total < 1e-6) return worst;
            var n = (int)Math.Ceiling(total / SampleMetres);
            var step = total / n;
            var y = new double[n + 1];
            var legOf = new int[n + 1];
            var leg = 0;
            for (var k = 0; k <= n; k++)
            {
                var s = k * step;
                while (leg < xs.Count - 2 && s > cum[leg + 1]) leg++;
                var t = cum[leg + 1] - cum[leg] > 1e-9 ? (s - cum[leg]) / (cum[leg + 1] - cum[leg]) : 1.0;
                y[k] = ground.HeightAt(xs[leg] + (xs[leg + 1] - xs[leg]) * t, zs[leg] + (zs[leg + 1] - zs[leg]) * t);
                legOf[k] = leg;
            }

            for (var pass = 0; pass < 2; pass++)
            {
                var length = pass == 0 ? WindowMetres : StepMetres;
                var scale = pass == 0 ? 1.0 : MaxPct / MaxStepPct;
                var w = Math.Min(n, Math.Max(1, (int)Math.Round(length / step)));
                for (var k = 0; k + w <= n; k++)
                {
                    var g = Math.Abs(y[k + w] - y[k]) / (w * step) * 100.0 * scale;
                    if (g > worst) worst = g;
                    if (legs == null) continue;
                    for (var q = legOf[k]; q <= legOf[k + w]; q++)
                        if (g > legs[q]) legs[q] = g;
                }
            }

            return worst;
        }
    }
}
