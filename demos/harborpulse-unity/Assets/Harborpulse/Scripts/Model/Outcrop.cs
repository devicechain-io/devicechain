// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Harborpulse.Model.Director;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>
    /// A granite outcrop that belongs to its slope: a low, faceted, stepped ledge whose base ring is sunk below the DRAWN
    /// terrain (<see cref="TerrainGrid.MeshHeight"/>, not the smooth field), whose top follows the local terrain plane and
    /// rises at most 4 m (and at most 0.4 of its plan radius) above the ground at its centre. The fit is pure geometry, so
    /// a sidecar test can check every perimeter point; the visual builder only stitches the rings it returns.
    /// </summary>
    public sealed class Outcrop
    {
        public const int Sides = 8;
        public Ground Center { get; private set; }
        public double YawDeg { get; private set; }
        public double HalfX { get; private set; }
        public double HalfZ { get; private set; }
        /// <summary>Ring 0 is the buried base; each further ring is one ledge's top, inset from the one below.</summary>
        public Point3[][] Rings { get; private set; }
        /// <summary>The middle of the highest ledge.</summary>
        public Point3 Crown { get; private set; }
        public double GroundAtCentre { get; private set; }
        public double Top { get; private set; }
        public int Seed { get; private set; }
        public double Radius => Math.Max(HalfX, HalfZ);
        public double PlanSize => 2 * Radius;
        public double Protrusion => Top - GroundAtCentre;

        /// <summary>The footprint the registry reserves: the plan rectangle plus a metre and a half of margin each way.</summary>
        public PropFootprint Footprint(string id) => new PropFootprint(id, Center, 2 * HalfX + 3, 2 * HalfZ + 3, YawDeg);

        static double H(int seed, int a, int b) => CoastalTerrain.Hash(seed * 7919 + a * 104729, seed * 6271 + b * 31337 + a);

        /// <summary>Fits an outcrop at <paramref name="c"/>, or returns null when the ground there is too steep or too broken to seat it.</summary>
        public static Outcrop Fit(TerrainGrid grid, Ground c, double yawDeg, double halfX, double halfZ, int seed)
        {
            var frame = new PropFootprint("outcrop", c, 1, 1, yawDeg);
            var radius = Math.Max(halfX, halfZ);
            var ground = grid.MeshHeight(c.X, c.Z);
            var local = new Ground[Sides];
            for (var i = 0; i < Sides; i++)
            {
                var a = (i + (H(seed, i, 0) - .5) * .4) * 2 * Math.PI / Sides;
                var r = .74 + .26 * H(seed, i, 1);
                local[i] = new Ground(halfX * r * Math.Cos(a), halfZ * r * Math.Sin(a));
            }

            // The terrain plane over the footprint: mean height and a gradient, from a symmetric 5 x 5 sample.
            double mean = 0, sxx = 0, szz = 0, sxh = 0, szh = 0; var n = 0;
            for (var u = -2; u <= 2; u++)
                for (var v = -2; v <= 2; v++)
                {
                    double lx = u * .4 * halfX, lz = v * .4 * halfZ;
                    var p = frame.Local(lx, lz);
                    var h = grid.MeshHeight(p.X, p.Z);
                    mean += h; n++; sxx += lx * lx; szz += lz * lz; sxh += lx * h; szh += lz * h;
                }
            mean /= n;
            // symmetric samples: the cross terms vanish and the means of lx, lz are zero
            var gx = sxh / sxx; var gz = szh / szz;
            if (Math.Sqrt(gx * gx + gz * gz) > .30) return null;

            // Base ring: on the drawn surface, sunk far enough that every chord between ring points is below it too.
            var ringXZ = new Ground[Sides]; var ringMesh = new double[Sides];
            for (var i = 0; i < Sides; i++) { ringXZ[i] = frame.Local(local[i].X, local[i].Z); ringMesh[i] = grid.MeshHeight(ringXZ[i].X, ringXZ[i].Z); }
            var sink = .6;
            for (var i = 0; i < Sides; i++)
            {
                var j = (i + 1) % Sides;
                for (var k = 1; k < 12; k++)
                {
                    var t = k / 12.0;
                    var px = ringXZ[i].X + (ringXZ[j].X - ringXZ[i].X) * t; var pz = ringXZ[i].Z + (ringXZ[j].Z - ringXZ[i].Z) * t;
                    sink = Math.Max(sink, ringMesh[i] + (ringMesh[j] - ringMesh[i]) * t - grid.MeshHeight(px, pz) + .3);
                }
            }
            if (sink > 3.2) return null;

            var limit = Math.Min(4, .4 * radius);
            var tiers = radius < 6 ? 1 : radius < 11 ? 2 : 1 + (int)(H(seed, 40, 2) * 2.999);
            var fraction = tiers == 1 ? new[] { 1.0 } : tiers == 2 ? new[] { .55, 1.0 } : new[] { .4, .72, 1.0 };
            var inset = tiers == 1 ? new[] { .82 } : tiers == 2 ? new[] { .85, .55 } : new[] { .88, .64, .40 };
            var protrusion = limit * (.62 + .38 * H(seed, 41, 3));
            for (var attempt = 0; attempt < 24; attempt++)
            {
                var rings = new Point3[tiers + 1][];
                rings[0] = new Point3[Sides];
                for (var i = 0; i < Sides; i++) rings[0][i] = new Point3(ringXZ[i].X, ringMesh[i] - sink, ringXZ[i].Z);
                var top = double.MinValue;
                for (var k = 0; k < tiers; k++)
                {
                    rings[k + 1] = new Point3[Sides];
                    for (var i = 0; i < Sides; i++)
                    {
                        double lx = local[i].X * inset[k], lz = local[i].Z * inset[k];
                        var p = frame.Local(lx, lz);
                        var y = mean + .55 * (gx * lx + gz * lz) + fraction[k] * protrusion * (1 + (H(seed, 10 + k, i) - .5) * .22);
                        rings[k + 1][i] = new Point3(p.X, y, p.Z);
                        top = Math.Max(top, y);
                    }
                }
                var crownY = mean + protrusion * 1.04;
                top = Math.Max(top, crownY);
                if (top - ground <= limit)
                {
                    if (top - ground < .6) return null;
                    return new Outcrop
                    {
                        Center = c, YawDeg = yawDeg, HalfX = halfX, HalfZ = halfZ, Rings = rings, Seed = seed,
                        Crown = new Point3(c.X, crownY, c.Z), GroundAtCentre = ground, Top = top,
                    };
                }
                protrusion *= .88;
            }
            return null;
        }

        /// <summary>1 on the outcrop's edge falling to 0 about 80 % of a radius beyond it: the ground is darkened in this skirt so the rock reads as bedrock breaking through.</summary>
        public double Skirt(double x, double z)
        {
            var a = YawDeg * Math.PI / 180;
            var dx = x - Center.X; var dz = z - Center.Z;
            var lx = dx * Math.Cos(a) - dz * Math.Sin(a);
            var lz = dx * Math.Sin(a) + dz * Math.Cos(a);
            var u = Math.Sqrt(lx * lx / (HalfX * HalfX) + lz * lz / (HalfZ * HalfZ));
            return 1 - CoastalTerrain.Smooth(.85, 1.8, u);
        }
    }
}
