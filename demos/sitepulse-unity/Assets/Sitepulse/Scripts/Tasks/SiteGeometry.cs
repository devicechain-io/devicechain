// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>A point of a road centreline: Unity X east, Y height, Z north, metres.</summary>
    public readonly struct RoadPoint
    {
        public RoadPoint(double x, double y, double z)
        {
            X = x;
            Y = y;
            Z = z;
        }

        public double X { get; }
        public double Y { get; }
        public double Z { get; }
    }

    /// <summary>A road: a centreline, how wide it is and what kind (which sets how fast a machine takes it).</summary>
    public sealed class RoadLine
    {
        public RoadLine(string name, string kind, double width, IReadOnlyList<RoadPoint> points)
        {
            Name = name ?? throw new ArgumentNullException(nameof(name));
            Kind = kind ?? "";
            Width = width;
            Points = points ?? throw new ArgumentNullException(nameof(points));
        }

        public string Name { get; }
        public string Kind { get; }
        public double Width { get; }
        public IReadOnlyList<RoadPoint> Points { get; }
    }

    /// <summary>A named place on the site (refuel bay, refuel queue, parking, ...).</summary>
    public readonly struct Spot
    {
        public Spot(string name, double x, double z, double headingDegrees)
        {
            Name = name;
            X = x;
            Z = z;
            HeadingDegrees = headingDegrees;
        }

        public string Name { get; }
        public double X { get; }
        public double Z { get; }
        public double HeadingDegrees { get; }
    }

    /// <summary>
    /// An axis-aligned rectangle in the site's metres, stored the way the feature file stores it:
    /// <c>x0, x1, z0, z1</c> (west, east, south, north edges), NOT x, z, width, height. A zone's token is
    /// the platform's area token for it.
    /// </summary>
    public readonly struct Rect2
    {
        public Rect2(string name, double x0, double x1, double z0, double z1)
        {
            Name = name;
            X0 = Math.Min(x0, x1);
            X1 = Math.Max(x0, x1);
            Z0 = Math.Min(z0, z1);
            Z1 = Math.Max(z0, z1);
        }

        public string Name { get; }
        public double X0 { get; }
        public double X1 { get; }
        public double Z0 { get; }
        public double Z1 { get; }
        public double CentreX => (X0 + X1) / 2.0;
        public double CentreZ => (Z0 + Z1) / 2.0;

        public bool Contains(double x, double z, double margin = 0) =>
            x >= X0 - margin && x <= X1 + margin && z >= Z0 - margin && z <= Z1 + margin;
    }

    /// <summary>
    /// Something standing on the site that a parked machine must keep clear of: a prop (an oriented box) or a
    /// pile and the refuel approach (a capsule: a segment and a radius). <see cref="Distance"/> is how far a point is
    /// from its outline, negative inside it.
    /// </summary>
    public sealed class Obstacle
    {
        readonly double cx, cz, hx, hz, headingRad; // box: centre, half extents along its own X and Z, heading
        readonly double bx, bz;                      // capsule: the far end (the near end is cx, cz)
        readonly double radius;
        readonly bool isBox;

        Obstacle(string name, bool box, double cx, double cz, double hx, double hz, double headingDegrees, double bx, double bz, double radius)
        {
            Name = name;
            isBox = box;
            this.cx = cx;
            this.cz = cz;
            this.hx = hx;
            this.hz = hz;
            headingRad = headingDegrees * Math.PI / 180.0;
            this.bx = bx;
            this.bz = bz;
            this.radius = radius;
        }

        public string Name { get; }

        public static Obstacle Box(string name, double x, double z, double halfX, double halfZ, double headingDegrees) =>
            new Obstacle(name, true, x, z, halfX, halfZ, headingDegrees, 0, 0, 0);

        public static Obstacle Capsule(string name, double ax, double az, double bx, double bz, double radius) =>
            new Obstacle(name, false, ax, az, 0, 0, 0, bx, bz, radius);

        /// <summary>True when (x, z) is at least <paramref name="distance"/> from the outline: the cheap test first, and the exact one only near.</summary>
        public bool IsAtLeast(double x, double z, double distance)
        {
            var dx = x - (isBox ? cx : (cx + bx) / 2.0);
            var dz = z - (isBox ? cz : (cz + bz) / 2.0);
            var reach = (isBox ? Math.Sqrt(hx * hx + hz * hz) : Math.Sqrt((bx - cx) * (bx - cx) + (bz - cz) * (bz - cz)) / 2.0 + radius) + distance;
            if (dx * dx + dz * dz >= reach * reach) return true;
            return Distance(x, z) >= distance;
        }

        public double Distance(double x, double z)
        {
            if (!isBox)
            {
                var sx = bx - cx;
                var sz = bz - cz;
                var len2 = sx * sx + sz * sz;
                var t = len2 <= 0 ? 0.0 : Math.Max(0.0, Math.Min(1.0, ((x - cx) * sx + (z - cz) * sz) / len2));
                return Math.Sqrt((x - (cx + t * sx)) * (x - (cx + t * sx)) + (z - (cz + t * sz)) * (z - (cz + t * sz))) - radius;
            }

            // The file's heading sign convention is not stated for a prop, so a box answers with the nearer of its
            // two possible turns: an off-axis prop is never treated as farther than it may be. (Every prop standing in a
            // zone but a light tower sits at 0/90/180/270, where the two agree.)
            return Math.Min(BoxDistance(x, z, headingRad), BoxDistance(x, z, -headingRad));
        }

        /// <summary>
        /// Points to steer by around this obstacle at <paramref name="offset"/> metres off its outline: the corners of a
        /// box (for each of the two headings its distance is judged by), or a ring about each end of a capsule.
        /// </summary>
        public void CornerWaypoints(double offset, List<double> xs, List<double> zs)
        {
            if (isBox)
            {
                foreach (var h in new[] { headingRad, -headingRad })
                {
                    var c = Math.Cos(h);
                    var s = Math.Sin(h);
                    foreach (var (sx, sz) in new[] { (1, 1), (1, -1), (-1, 1), (-1, -1) })
                    {
                        var lx = sx * (hx + offset);
                        var lz = sz * (hz + offset);
                        xs.Add(cx + lx * c + lz * s);
                        zs.Add(cz - lx * s + lz * c);
                    }
                }

                return;
            }

            // a polygon whose EDGES stay offset away: its corners sit out at offset / cos(half the step)
            const int sides = 8;
            var ring = (radius + offset) / Math.Cos(Math.PI / sides);
            foreach (var (ex, ez) in new[] { (cx, cz), (bx, bz) })
                for (var i = 0; i < sides; i++)
                {
                    var a = (i + 0.5) * 2.0 * Math.PI / sides;
                    xs.Add(ex + ring * Math.Cos(a));
                    zs.Add(ez + ring * Math.Sin(a));
                }
        }

        double BoxDistance(double x, double z, double h)
        {
            var dx = x - cx;
            var dz = z - cz;
            var c = Math.Cos(h);
            var s = Math.Sin(h);
            var lx = Math.Abs(dx * c - dz * s) - hx;
            var lz = Math.Abs(dx * s + dz * c) - hz;
            if (lx <= 0 && lz <= 0) return Math.Max(lx, lz);
            return Math.Sqrt(Math.Max(lx, 0) * Math.Max(lx, 0) + Math.Max(lz, 0) * Math.Max(lz, 0));
        }
    }

    /// <summary>
    /// What the scene says about the site, as plain data: the haul roads, the named spots, the zones the
    /// platform can send a machine to, and the pads they sit on. Read from the feature file by the App
    /// layer; everything routing needs is here and nothing else.
    /// </summary>
    public sealed class SiteGeometry
    {
        public SiteGeometry(IReadOnlyList<RoadLine> roads, IReadOnlyDictionary<string, Spot> spots, IReadOnlyList<Rect2> zones, IReadOnlyList<Rect2> pads, IReadOnlyList<Obstacle> obstacles = null)
        {
            Obstacles = obstacles ?? Array.Empty<Obstacle>();
            Roads = roads ?? throw new ArgumentNullException(nameof(roads));
            Spots = spots ?? throw new ArgumentNullException(nameof(spots));
            Zones = zones ?? throw new ArgumentNullException(nameof(zones));
            Pads = pads ?? Array.Empty<Rect2>();
        }

        public IReadOnlyList<RoadLine> Roads { get; }
        public IReadOnlyDictionary<string, Spot> Spots { get; }
        public IReadOnlyList<Rect2> Zones { get; }
        public IReadOnlyList<Rect2> Pads { get; }

        /// <summary>The spot a machine sent to the zone holding it parks nearest.</summary>
        public const string ParkingSpot = "parking";

        /// <summary>The props, piles and the refuel approach a parked machine keeps clear of.</summary>
        public IReadOnlyList<Obstacle> Obstacles { get; }

        public bool TryZone(string token, out Rect2 zone)
        {
            foreach (var z in Zones)
                if (string.Equals(z.Name, token, StringComparison.Ordinal))
                {
                    zone = z;
                    return true;
                }

            zone = default;
            return false;
        }

        public bool HasZone(string token) => TryZone(token, out _);
    }
}
