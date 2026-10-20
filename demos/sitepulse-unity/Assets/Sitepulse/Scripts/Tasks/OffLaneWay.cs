// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using DeviceChain.Sim.Traffic;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>
    /// The way-keeper for every machine that is not a haul truck (the design's "constrain the semantics"). Such a machine works in its own area,
    /// and an errand of one is a free route over open ground, which is never interlocked with the trucks: a route is admitted only where it
    /// keeps clear of every lane (no cell of it conflicts with a lane cell), of every other machine's work and of every machine standing off its
    /// track, so a machine off the lanes never meets a truck on them or a machine at work. A loader or dozer is slow, and every way out of the
    /// pit runs along the haul road: driven there it would hold the trucks for minutes, so such an errand is refused, saying why.
    /// One such machine drives at a time: free routes are not held to each other.
    /// </summary>
    public sealed class OffLaneWay : IWayKeeper
    {
        /// <summary>The air a free route keeps from another machine's work and from a machine standing off its track, in metres.</summary>
        public const double ClearanceM = 0.5;

        /// <summary>A route is checked every this many metres along it against what it must keep clear of.</summary>
        const double CheckStepM = 0.5;

        readonly PreparedSite site;
        readonly Func<string, IEnumerable<(string Machine, Polygon Area)>> working;
        readonly Func<string, IEnumerable<(string Machine, double X, double Z, double Radius)>> standing;

        /// <param name="working">For a machine, every OTHER machine at work on its track and the area it works.</param>
        /// <param name="standing">For a machine, every OTHER machine standing off its track: where, and the radius it covers.</param>
        public OffLaneWay(PreparedSite site, Func<string, IEnumerable<(string Machine, Polygon Area)>> working,
            Func<string, IEnumerable<(string Machine, double X, double Z, double Radius)>> standing)
        {
            this.site = site ?? throw new ArgumentNullException(nameof(site));
            this.working = working ?? throw new ArgumentNullException(nameof(working));
            this.standing = standing ?? throw new ArgumentNullException(nameof(standing));
        }

        /// <summary>The machine driving a free route now, or null.</summary>
        public string Driver { get; private set; }

        static string KindName(EquipmentKind kind) => kind == EquipmentKind.Hauler ? "Hauler" : kind == EquipmentKind.Loader ? "Loader" : "Dozer";

        public string Check(IMachineBody body, LaneLine line, bool endsStanding)
        {
            if (line.Length < 0.1) return null;
            var poses = new List<FreePose>();
            var n = Math.Max(1, (int)Math.Ceiling(line.Length / PreparedSite.FreePoseStepM));
            for (var i = 0; i <= n; i++)
            {
                line.PointAt(line.Length * i / n, out var x, out var z, out var h, out _);
                poses.Add(new FreePose(x, z, h));
            }

            var cells = site.Cut(KindName(body.Kind), poses);
            for (var i = 0; i < cells.Count; i++)
                if (cells.Conflicts[i].Count > 0) return "its way crosses the haul lanes (" + site.LaneOf(cells.Conflicts[i][0]) + ")";

            var reach = ParkingLot.TravelRadius(body.Kind) + ClearanceM;
            var steps = Math.Max(1, (int)Math.Ceiling(line.Length / CheckStepM));
            foreach (var (machine, area) in working(body.Id))
                for (var i = 0; i <= steps; i++)
                {
                    line.PointAt(line.Length * i / steps, out var x, out var z, out _, out _);
                    if (Geometry.PointDistance(area, x, z) < reach) return "its way passes " + machine + " at work";
                }

            foreach (var (machine, sx, sz, radius) in standing(body.Id))
                for (var i = 0; i <= steps; i++)
                {
                    line.PointAt(line.Length * i / steps, out var x, out var z, out _, out _);
                    var d = Math.Sqrt((x - sx) * (x - sx) + (z - sz) * (z - sz));
                    if (d < reach + radius) return "its way passes " + machine + ", standing at (" + sx.ToString("0", CultureInfo.InvariantCulture) + ", " + sz.ToString("0", CultureInfo.InvariantCulture) + ")";
                }

            return null;
        }

        public bool MayLeave(string id, out string why)
        {
            why = null;
            if (Driver == null || Driver == id) return true;
            why = "waiting for the way: " + Driver + " is driving off the lanes";
            return false;
        }

        public string Begin(IMachineBody body, LaneLine line, bool endsStanding)
        {
            var refused = Check(body, line, endsStanding);
            if (refused != null) return refused;
            Driver = body.Id;
            return null;
        }

        /// <summary>An admitted route is the machine's to the end: nothing on it is anyone else's.</summary>
        public double Reach(string id, double s) => double.MaxValue;

        public void Ended(string id)
        {
            if (Driver == id) Driver = null;
        }
    }
}
