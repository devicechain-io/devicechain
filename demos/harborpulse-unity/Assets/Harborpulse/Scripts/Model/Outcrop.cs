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

        /// <summary>
        /// The loose rock on the ledge: angular blocks that break its stepped outline, and on the larger ledges a tor, two or three slabs
        /// stacked on the crown. Each is eight corners, [0..3] round the bottom and [4..7] above them. A block's bottom is under the DRAWN
        /// terrain at every one of its corners (<see cref="BlockSinkM"/>), so it is a prism that comes up through the ledge, never a stone
        /// set down on it; its plan stays inside the ledge's own rectangle, which the registry already reserves.
        /// </summary>
        public Point3[][] Blocks { get; private set; } = new Point3[0][];

        /// <summary>The highest point of the ledge and its blocks, in WORLD metres.</summary>
        public double Peak { get; private set; }

        /// <summary>
        /// For each block, the height at which it comes out of the rock under it: the ledge's surface, or the top of the slab it is stacked
        /// on. The part of a block below this is inside other rock; the builder lights a block's wall by the part above it.
        /// </summary>
        public double[] BlockFoot { get; private set; } = new double[0];

        /// <summary>
        /// The one tor that is a landmark: stacks of slabs <see cref="LandmarkRiseM"/> high on a broad ledge, where the 16:9 HOME looks. Every
        /// other tor is a few metres of rock on a ledge and reads from HOME as rubble; this one is taller than the church's ridge.
        /// </summary>
        public bool Landmark { get; private set; }

        /// <summary>The landmark tor's ledge (half its plan) and how far its tallest stack stands above the ledge's surface.</summary>
        public const double LandmarkHalfX = 20, LandmarkHalfZ = 14, LandmarkRiseM = 25;

        /// <summary>How far a block's bottom lies under the lowest drawn ground at its corners.</summary>
        public const double BlockSinkM = .5;

        /// <summary>A tor stands on ledges at least this wide in plan, and rises at most this far above the ledge's own top (and at most <see cref="TorRadiusShare"/> of its radius).</summary>
        public const double TorMinRadiusM = 11, TorMaxRiseM = 8, TorRadiusShare = .46;

        /// <summary>How far a stacked slab's bottom lies under the nominal top of the slab it stands on (whose own top is tilted by less).</summary>
        public const double SlabSinkM = .9;

        static Point3[] Block(TerrainGrid grid, PropFootprint frame, double lx, double lz, double halfA, double halfB, double yawRad, double top, double taper, double tilt, int seed, int n, double standsOn = double.NaN)
        {
            var c = new Point3[8];
            double cs = Math.Cos(yawRad), sn = Math.Sin(yawRad);
            var low = double.MaxValue;
            var plan = new Ground[4];
            for (var i = 0; i < 4; i++)
            {
                double ax = (i == 0 || i == 3 ? -1 : 1) * halfA, bz = (i < 2 ? -1 : 1) * halfB;
                plan[i] = new Ground(lx + ax * cs - bz * sn, lz + ax * sn + bz * cs);
                var p = frame.Local(plan[i].X, plan[i].Z);
                low = Math.Min(low, grid.MeshHeight(p.X, p.Z));
            }
            for (var i = 0; i < 4; i++)
            {
                var p = frame.Local(plan[i].X, plan[i].Z);
                // a slab of a stack starts inside the slab under it; every other block comes up through the ground
                c[i] = new Point3(p.X, double.IsNaN(standsOn) ? low - BlockSinkM : standsOn - SlabSinkM, p.Z);
                // the top is smaller, skewed a little and never level: a split block, not a brick
                double tx = lx + (plan[i].X - lx) * taper * (.86 + .28 * H(seed, 60 + n, i)), tz = lz + (plan[i].Z - lz) * taper * (.86 + .28 * H(seed, 70 + n, i));
                var q = frame.Local(tx, tz);
                c[4 + i] = new Point3(q.X, top - tilt * H(seed, 80 + n, i), q.Z);
            }
            return c;
        }

        void AddBlocks(TerrainGrid grid, PropFootprint frame, double mean, double gx, double gz, double protrusion)
        {
            var radius = Radius;
            Peak = Top;
            if (radius < 5) return;
            var blocks = new System.Collections.Generic.List<Point3[]>();
            // the ledge's own surface at a point of its plan, near enough: the fitted plane, and the stepped rise falling away from the crown
            double Surface(double lx, double lz)
            {
                var u = Math.Sqrt(lx * lx / (HalfX * HalfX) + lz * lz / (HalfZ * HalfZ));
                return mean + .55 * (gx * lx + gz * lz) + protrusion * (1 - CoastalTerrain.Smooth(.3, .95, u));
            }
            var loose = 2 + (int)(H(Seed, 50, 0) * 2.999) + (radius >= 12 ? 1 : 0);
            for (var n = 0; n < loose; n++)
            {
                // on the shoulders: between a third and 0.58 of the way out, all round
                var a = (n + H(Seed, 51, n)) * 2 * Math.PI / loose;
                var u = .30 + .28 * H(Seed, 52, n);
                double lx = HalfX * u * Math.Cos(a), lz = HalfZ * u * Math.Sin(a);
                var size = Math.Max(1.0, Math.Min(3.8, radius * (.12 + .11 * H(Seed, 53, n))));
                var rise = Math.Min(3.2, size * (.55 + .7 * H(Seed, 54, n)));
                blocks.Add(Block(grid, frame, lx, lz, size, size * (.6 + .35 * H(Seed, 55, n)), H(Seed, 56, n) * Math.PI, Surface(lx, lz) + rise, .72, rise * .3, Seed, n));
            }
            if (radius >= TorMinRadiusM && H(Seed, 57, 0) < .7)
            {
                // the tor: a broad slab on the crown, a narrower one on it, and on the largest ledges a cap stone, each turned from the last
                var rise = Math.Min(TorMaxRiseM, TorRadiusShare * radius) * (.72 + .28 * H(Seed, 58, 0));
                var slabs = radius >= 14 ? 3 : 2;
                var yaw = H(Seed, 59, 0) * Math.PI;
                double ox = (H(Seed, 61, 0) - .5) * .16 * HalfX, oz = (H(Seed, 62, 0) - .5) * .16 * HalfZ;
                for (var s = 0; s < slabs; s++)
                {
                    var half = Math.Min(5.5, radius * .3) * (1 - .27 * s);
                    var topY = Surface(ox, oz) + rise * (s + 1) / slabs;
                    blocks.Add(Block(grid, frame, ox + (H(Seed, 63, s) - .5) * half * .5, oz + (H(Seed, 64, s) - .5) * half * .5, half, half * (.62 + .25 * H(Seed, 65, s)),
                        yaw + s * .7 + H(Seed, 66, s) * .5, topY, .80, rise * .1, Seed, 10 + s));
                }
            }
            Blocks = blocks.ToArray();
            BlockFoot = new double[Blocks.Length];
            for (var n = 0; n < Blocks.Length; n++)
            {
                // a loose block and a tor's slab alike: the lowest of its own top corners, less its visible height, is where the ledge meets it
                double lx = 0, lz = 0; var a = YawDeg * Math.PI / 180;
                for (var i = 0; i < 4; i++) { double dx = Blocks[n][i].X - Center.X, dz = Blocks[n][i].Z - Center.Z; lx += (dx * Math.Cos(a) - dz * Math.Sin(a)) / 4; lz += (dx * Math.Sin(a) + dz * Math.Cos(a)) / 4; }
                BlockFoot[n] = Surface(lx, lz);
            }
            foreach (var b in Blocks) for (var i = 4; i < 8; i++) Peak = Math.Max(Peak, b[i].Y);
        }

        /// <summary>
        /// The landmark's rock: a main stack of six slabs, each narrower than and turned from the one under it and leaning a little, a second
        /// stack half as high beside it, a third lower again and a table stone, with clitter round their feet. The lowest slab of a stack is
        /// a prism like any block (its bottom under the drawn ground at every corner); each slab above starts <see cref="SlabSinkM"/> inside
        /// the one it stands on, and is about as wide at its foot as that one is at its top, so the joints overhang a little here and there
        /// as a tor's do and no slab is a pillar standing through the ones under it.
        /// </summary>
        void AddLandmarkTor(TerrainGrid grid, PropFootprint frame, double mean, double gx, double gz, double protrusion)
        {
            Landmark = true; Peak = Top;
            var blocks = new System.Collections.Generic.List<Point3[]>(); var feet = new System.Collections.Generic.List<double>();
            double Surface(double lx, double lz)
            {
                var u = Math.Sqrt(lx * lx / (HalfX * HalfX) + lz * lz / (HalfZ * HalfZ));
                return mean + .55 * (gx * lx + gz * lz) + protrusion * (1 - CoastalTerrain.Smooth(.3, .95, u));
            }
            void Pile(int id, double px, double pz, double baseHalf, double rise, int slabs, double leanX, double leanZ)
            {
                var yaw = H(Seed, 100 + id, 0) * Math.PI; var foot = Surface(px, pz);
                for (var s = 0; s < slabs; s++)
                {
                    var t = slabs == 1 ? 0 : s / (double)(slabs - 1);
                    var half = baseHalf * Math.Pow(.895, s);
                    // the slabs are of unequal thickness: the stack's joints are not a ladder
                    var topY = Surface(px, pz) + rise * (s + 1 + (s + 1 < slabs ? (H(Seed, 101 + id, s) - .5) * .5 : 0)) / slabs;
                    double cx = px + leanX * t + (s == 0 ? 0 : (H(Seed, 102 + id, s) - .5) * half * .16), cz = pz + leanZ * t + (s == 0 ? 0 : (H(Seed, 103 + id, s) - .5) * half * .16);
                    blocks.Add(Block(grid, frame, cx, cz, half, half * (.70 + .10 * H(Seed, 104 + id, 0)), yaw + s * .22 + (H(Seed, 105 + id, s) - .5) * .3, topY, .93, (topY - foot) * .1, Seed, 100 * id + s,
                        s == 0 ? double.NaN : foot));
                    feet.Add(foot); foot = topY;
                }
            }
            Pile(1, -3.0, .6, 8.8, LandmarkRiseM, 6, 1.5, -.7);
            Pile(2, 10.6, 1.0, 5.6, LandmarkRiseM * .58, 3, -.6, .5);
            Pile(3, -13.6, -1.5, 4.0, LandmarkRiseM * .34, 2, .4, .2);
            Pile(4, 4.6, -8.2, 3.6, LandmarkRiseM * .2, 1, 0, 0);
            // clitter: fallen blocks at the stacks' feet
            for (var n = 0; n < 5; n++)
            {
                var a = (n + .5 * H(Seed, 151, n)) * 2 * Math.PI / 5 + 1.1;
                double lx = HalfX * .66 * Math.Cos(a), lz = HalfZ * .62 * Math.Sin(a);
                var size = 1.5 + 1.3 * H(Seed, 153, n); var rise = size * (.7 + .6 * H(Seed, 154, n));
                blocks.Add(Block(grid, frame, lx, lz, size, size * (.6 + .3 * H(Seed, 155, n)), H(Seed, 156, n) * Math.PI, Surface(lx, lz) + rise, .72, rise * .3, Seed, 300 + n));
                feet.Add(Surface(lx, lz));
            }
            Blocks = blocks.ToArray(); BlockFoot = feet.ToArray();
            foreach (var b in Blocks) for (var i = 4; i < 8; i++) Peak = Math.Max(Peak, b[i].Y);
        }

        /// <summary>The footprint the registry reserves: the plan rectangle plus a metre and a half of margin each way.</summary>
        public PropFootprint Footprint(string id) => new PropFootprint(id, Center, 2 * HalfX + 3, 2 * HalfZ + 3, YawDeg);

        static double H(int seed, int a, int b) => CoastalTerrain.Hash(seed * 7919 + a * 104729, seed * 6271 + b * 31337 + a);

        /// <summary>Fits the landmark tor's ledge at <paramref name="c"/> and stacks the tor on it, or returns null when the ground cannot seat the ledge.</summary>
        public static Outcrop FitLandmark(TerrainGrid grid, Ground c, double yawDeg, int seed) => Fit(grid, c, yawDeg, LandmarkHalfX, LandmarkHalfZ, seed, true);

        /// <summary>Fits an outcrop at <paramref name="c"/>, or returns null when the ground there is too steep or too broken to seat it.</summary>
        public static Outcrop Fit(TerrainGrid grid, Ground c, double yawDeg, double halfX, double halfZ, int seed) => Fit(grid, c, yawDeg, halfX, halfZ, seed, false);

        static Outcrop Fit(TerrainGrid grid, Ground c, double yawDeg, double halfX, double halfZ, int seed, bool landmark)
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
                    var fitted = new Outcrop
                    {
                        Center = c, YawDeg = yawDeg, HalfX = halfX, HalfZ = halfZ, Rings = rings, Seed = seed,
                        Crown = new Point3(c.X, crownY, c.Z), GroundAtCentre = ground, Top = top,
                    };
                    if (landmark) fitted.AddLandmarkTor(grid, frame, mean, gx, gz, protrusion);
                    else fitted.AddBlocks(grid, frame, mean, gx, gz, protrusion);
                    return fitted;
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
