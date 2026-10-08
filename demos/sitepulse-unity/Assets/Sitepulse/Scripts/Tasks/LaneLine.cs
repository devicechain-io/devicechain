// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>
    /// The line a machine drives along a <see cref="Route"/> when it keeps to the lane its road gives the way it is going: the route's own
    /// line moved to the left by the lane's offset, and measured by its OWN length (round a corner the lane is longer or shorter than the
    /// road's, so a machine that covers a metre of it per metre of its speed does not speed up or slow down there).
    /// </summary>
    /// <remarks>
    /// Where the lane changes (a road meeting open ground, or the ends of the route) the offset eases across, at most
    /// <see cref="SlewMetresPerMetre"/> metres sideways for each metre forward, so a machine moves into or out of a lane and never steps:
    /// it starts and arrives on the route's own line, and takes as long to reach its lane as the slope needs. It eases within the road:
    /// off a road (where the offset is none) and on a narrower lane than the one before, it is already where that lane has it.
    /// </remarks>
    public sealed class LaneLine
    {
        /// <summary>The most a machine moves sideways for each metre it moves forward changing lanes (a gentle move across, not a sidestep).</summary>
        public const double SlewMetresPerMetre = 0.25;

        /// <summary>The spacing (metres) the line is built at, along the route.</summary>
        public const double SampleMetres = 0.1;

        readonly double[] x, z, along, centre, lateral;

        LaneLine(double[] x, double[] z, double[] along, double[] centre, double[] lateral, Route route)
        {
            this.x = x;
            this.z = z;
            this.along = along;
            this.centre = centre;
            this.lateral = lateral;
            Route = route;
        }

        /// <summary>The route this line follows.</summary>
        public Route Route { get; }

        /// <summary>How long the line is: the distance a machine drives along it.</summary>
        public double Length => along[along.Length - 1];

        /// <summary>
        /// The line a machine keeps to on a route. <paramref name="laneShare"/> is how much of its lane's offset it takes, from 0 (the
        /// route's own line) to 1 (the lane): a machine's own, so a later change can have one drift toward the middle of a road it has to itself.
        /// </summary>
        public static LaneLine Build(Route route, double laneShare = 1.0)
        {
            if (route == null) throw new ArgumentNullException(nameof(route));
            var share = Math.Max(0.0, Math.Min(1.0, laneShare));
            var len = route.Length;
            var n = Math.Max(2, (int)Math.Ceiling(len / SampleMetres) + 1);
            var step = len / (n - 1);
            var lat = new double[n];
            var cl = new double[n];
            for (var i = 0; i < n; i++)
            {
                cl[i] = Math.Min(len, i * step);
                // the route's own line at either end, and from there no further than the slope allows
                var reach = SlewMetresPerMetre * Math.Min(cl[i], len - cl[i]);
                lat[i] = route.Legs == 0 ? 0.0 : Math.Max(-reach, -route.LaneAt(cl[i]) * share);
            }

            // however the lane changes along the way, the line never moves across faster than the limit, and it is never further from the
            // route's line than the lane it is on: the offset is the largest within a slope of the lane everywhere, from both directions.
            // So it leaves a lane before the lane ends (on the road, never past the road onto open ground, where the offset is none) and
            // takes up a lane after the lane begins, and the ends, being on the route's own line, stay there.
            var slew = SlewMetresPerMetre * step;
            for (var i = 1; i < n; i++) lat[i] = Math.Max(lat[i - 1] - slew, lat[i]);
            for (var i = n - 2; i >= 0; i--) lat[i] = Math.Max(lat[i + 1] - slew, lat[i]);

            var px = new double[n];
            var pz = new double[n];
            var al = new double[n];
            for (var i = 0; i < n; i++)
            {
                route.OffsetPointAt(cl[i], lat[i], out px[i], out pz[i], out _);
                if (i > 0) al[i] = al[i - 1] + Math.Sqrt((px[i] - px[i - 1]) * (px[i] - px[i - 1]) + (pz[i] - pz[i - 1]) * (pz[i] - pz[i - 1]));
            }

            return new LaneLine(px, pz, al, cl, lat, route);
        }

        /// <summary>Where the line is at <paramref name="d"/> metres along it, which way it is heading, and which leg of the route that is on.</summary>
        public void PointAt(double d, out double px, out double pz, out double headingDegrees, out int leg)
        {
            var i = IndexAt(d, out var w);
            px = x[i] + (x[i + 1] - x[i]) * w;
            pz = z[i] + (z[i + 1] - z[i]) * w;
            // the direction over a few metres either side, so the heading turns smoothly where the line does
            var a = IndexAt(d - 1.0, out var wa);
            var b = IndexAt(d + 1.0, out var wb);
            double ax = x[a] + (x[a + 1] - x[a]) * wa, az = z[a] + (z[a + 1] - z[a]) * wa;
            double bx = x[b] + (x[b + 1] - x[b]) * wb, bz = z[b] + (z[b + 1] - z[b]) * wb;
            if ((bx - ax) * (bx - ax) + (bz - az) * (bz - az) > 1e-9) headingDegrees = SiteDefinition.HeadingDegrees(bx - ax, bz - az);
            else Route.PointAt(centre[i], out _, out _, out headingDegrees, out _);
            Route.PointAt(centre[i], out _, out _, out _, out leg);
        }

        /// <summary>The line as points <paramref name="spacing"/> metres apart along it (and its end), to be read as a path.</summary>
        public void Polyline(double spacing, out List<double> xs, out List<double> zs)
        {
            xs = new List<double>();
            zs = new List<double>();
            var n = Math.Max(1, (int)Math.Ceiling(Length / spacing));
            for (var k = 0; k <= n; k++)
            {
                PointAt(Math.Min(Length, k * spacing), out var px, out var pz, out _, out _);
                xs.Add(px);
                zs.Add(pz);
            }
        }

        /// <summary>How far to the right of the route's own line the machine is, at <paramref name="d"/> metres along (negative: to the left).</summary>
        public double LateralAt(double d)
        {
            var i = IndexAt(d, out var w);
            return lateral[i] + (lateral[i + 1] - lateral[i]) * w;
        }

        /// <summary>How far along the route's own line the machine is, at <paramref name="d"/> metres along this one.</summary>
        public double CentrelineAt(double d)
        {
            var i = IndexAt(d, out var w);
            return centre[i] + (centre[i + 1] - centre[i]) * w;
        }

        int IndexAt(double d, out double w)
        {
            var last = along.Length - 1;
            if (d <= 0.0) { w = 0.0; return 0; }
            if (d >= along[last]) { w = 1.0; return last - 1; }
            int lo = 0, hi = last;
            while (hi - lo > 1)
            {
                var mid = (lo + hi) >> 1;
                if (along[mid] <= d) lo = mid; else hi = mid;
            }

            var span = along[lo + 1] - along[lo];
            w = span > 1e-12 ? (d - along[lo]) / span : 0.0;
            return lo;
        }
    }
}
