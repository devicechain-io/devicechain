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
    /// What the scene says about the site, as plain data: the haul roads, the named spots, the zones the
    /// platform can send a machine to, and the pads they sit on. Read from the feature file by the App
    /// layer; everything routing needs is here and nothing else.
    /// </summary>
    public sealed class SiteGeometry
    {
        public SiteGeometry(IReadOnlyList<RoadLine> roads, IReadOnlyDictionary<string, Spot> spots, IReadOnlyList<Rect2> zones, IReadOnlyList<Rect2> pads)
        {
            Roads = roads ?? throw new ArgumentNullException(nameof(roads));
            Spots = spots ?? throw new ArgumentNullException(nameof(spots));
            Zones = zones ?? throw new ArgumentNullException(nameof(zones));
            Pads = pads ?? Array.Empty<Rect2>();
        }

        public IReadOnlyList<RoadLine> Roads { get; }
        public IReadOnlyDictionary<string, Spot> Spots { get; }
        public IReadOnlyList<Rect2> Zones { get; }
        public IReadOnlyList<Rect2> Pads { get; }

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
