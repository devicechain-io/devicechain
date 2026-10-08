// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>One box on the ground: four corners, in order round it (metres, site axes).</summary>
    public readonly struct Quad
    {
        public Quad(double ax, double az, double bx, double bz, double cx, double cz, double dx, double dz)
        {
            Ax = ax; Az = az; Bx = bx; Bz = bz; Cx = cx; Cz = cz; Dx = dx; Dz = dz;
        }

        public double Ax { get; }
        public double Az { get; }
        public double Bx { get; }
        public double Bz { get; }
        public double Cx { get; }
        public double Cz { get; }
        public double Dx { get; }
        public double Dz { get; }
    }

    /// <summary>The ground a machine covers at one pose: one box, or two for a haul truck (its wide front deck and its narrower body and rear tyres).</summary>
    public readonly struct FootprintShape
    {
        public FootprintShape(Quad first, Quad? second)
        {
            First = first;
            Second = second ?? default;
            Count = second.HasValue ? 2 : 1;
        }

        public Quad First { get; }
        public Quad Second { get; }
        public int Count { get; }
    }

    /// <summary>
    /// What a machine covers, as the choreography is generated and checked against it (ArtSource/terrain/quarry_fleet.py: <c>FOOT</c>,
    /// <c>FOOT_PARTS</c>, <c>sat_gap</c>): the same boxes and the same separation, so "the machines never overlap" means one thing in the
    /// generator, the task layer and the tests. A pose is the middle of the machine and a heading in degrees clockwise from north (+Z).
    /// </summary>
    public static class Footprint
    {
        /// <summary>A loader's front with its boom raised: its front tyres, since its bucket is then over the body it is tipping into.</summary>
        public const double LoaderRaisedFront = 2.6;

        /// <summary>A loader's boom angle below which it is raised (degrees; the generator's own line).</summary>
        public const double LoaderRaisedBoomDegrees = -40.0;

        /// <summary>Half width and half length (front, rear) of the machine's box, and the boxes of a haul truck.</summary>
        public static void Dimensions(EquipmentKind kind, out double halfWidth, out double front, out double rear)
        {
            switch (kind)
            {
                case EquipmentKind.Hauler: halfWidth = 2.85; front = 5.7; rear = 4.6; break;
                case EquipmentKind.Loader: halfWidth = 1.55; front = 5.2; rear = 3.6; break;
                default: halfWidth = 1.75; front = 3.7; rear = 3.4; break;
            }
        }

        /// <summary>The footprint at a pose. <paramref name="boomRaised"/> (a loader with its boom up) pulls its front back to its tyres.</summary>
        public static FootprintShape At(EquipmentKind kind, double x, double z, double headingDegrees, bool boomRaised = false)
        {
            var h = headingDegrees * Math.PI / 180.0;
            double fx = Math.Sin(h), fz = Math.Cos(h), rx = fz, rz = -fx;
            if (kind == EquipmentKind.Hauler)
                return new FootprintShape(Box(x, z, fx, fz, rx, rz, 2.85, 5.7, -1.2), Box(x, z, fx, fz, rx, rz, 2.25, 1.2, 4.6));
            Dimensions(kind, out var w, out var f, out var r);
            if (kind == EquipmentKind.Loader && boomRaised) f = LoaderRaisedFront;
            return new FootprintShape(Box(x, z, fx, fz, rx, rz, w, f, r), null);
        }

        static Quad Box(double x, double z, double fx, double fz, double rx, double rz, double w, double front, double rear) => new Quad(
            x + fx * front + rx * w, z + fz * front + rz * w,
            x + fx * front - rx * w, z + fz * front - rz * w,
            x - fx * rear - rx * w, z - fz * rear - rz * w,
            x - fx * rear + rx * w, z - fz * rear + rz * w);

        /// <summary>
        /// The clear distance between two footprints along the best separating axis: positive is air between them, zero touching,
        /// negative overlapping (by how far they would have to move apart).
        /// </summary>
        public static double Gap(in FootprintShape a, in FootprintShape b)
        {
            var best = double.MaxValue;
            for (var i = 0; i < a.Count; i++)
                for (var j = 0; j < b.Count; j++)
                    best = Math.Min(best, Gap(i == 0 ? a.First : a.Second, j == 0 ? b.First : b.Second));
            return best;
        }

        /// <summary>The clear distance between two boxes (the generator's <c>sat_gap</c>).</summary>
        public static double Gap(in Quad a, in Quad b)
        {
            var best = double.MinValue;
            for (var pass = 0; pass < 2; pass++)
            {
                var p = pass == 0 ? a : b;
                for (var i = 0; i < 4; i++)
                {
                    Corner(p, i, out var x0, out var z0);
                    Corner(p, (i + 1) % 4, out var x1, out var z1);
                    double ex = x1 - x0, ez = z1 - z0;
                    var len = Math.Sqrt(ex * ex + ez * ez) + 1e-9;
                    double nx = -ez / len, nz = ex / len;
                    Project(a, nx, nz, out var minA, out var maxA);
                    Project(b, nx, nz, out var minB, out var maxB);
                    best = Math.Max(best, Math.Max(minB - maxA, minA - maxB));
                }
            }

            return best;
        }

        static void Corner(in Quad q, int i, out double x, out double z)
        {
            switch (i)
            {
                case 0: x = q.Ax; z = q.Az; break;
                case 1: x = q.Bx; z = q.Bz; break;
                case 2: x = q.Cx; z = q.Cz; break;
                default: x = q.Dx; z = q.Dz; break;
            }
        }

        static void Project(in Quad q, double nx, double nz, out double min, out double max)
        {
            min = double.MaxValue;
            max = double.MinValue;
            for (var i = 0; i < 4; i++)
            {
                Corner(q, i, out var x, out var z);
                var d = x * nx + z * nz;
                if (d < min) min = d;
                if (d > max) max = d;
            }
        }
    }
}
