// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sim.Traffic
{
    /// <summary>A pose along a free route: where a machine is and which way it faces (degrees clockwise from north).</summary>
    public readonly struct FreePose
    {
        public FreePose(double x, double z, double headingDegrees)
        {
            X = x;
            Z = z;
            HeadingDegrees = headingDegrees;
        }

        public double X { get; }
        public double Z { get; }
        public double HeadingDegrees { get; }
    }

    /// <summary>A free route cut into cells: each cell's length, and the lane cells (by <see cref="CellSpace"/> number) it conflicts with.</summary>
    public sealed class FreeCells
    {
        public FreeCells(IReadOnlyList<double> lengths, IReadOnlyList<IReadOnlyList<int>> conflicts)
        {
            Lengths = lengths;
            Conflicts = conflicts;
        }

        public IReadOnlyList<double> Lengths { get; }
        public IReadOnlyList<IReadOnlyList<int>> Conflicts { get; }
        public int Count => Lengths.Count;
    }

    /// <summary>
    /// What an interlocking needs to know about a site that takes measuring, measured once when the site loads (design L7: nothing is built while
    /// machines move): which lane cells conflict, and the ground each lane cell sweeps, so that a free route a machine is to drive off the lanes
    /// can be checked against them (<see cref="Cut"/>). Immutable; any number of <see cref="Interlocking"/>s (one per run) are built over it.
    /// </summary>
    public sealed class PreparedSite
    {
        /// <summary>A free route is cut into cells this long (metres), as the lanes are.</summary>
        public double FreeCellM => Topology.CellM;

        /// <summary>A machine on a free route is posed at most this far apart (metres) when its swept ground is measured.</summary>
        public const double FreePoseStepM = 0.25;

        const double Grid = 30.0;

        readonly ISiteWorld world;
        readonly List<(string LaneA, int CellA, string LaneB, int CellB)> conflicts;
        readonly List<IReadOnlyList<Polygon>> sweeps = new List<IReadOnlyList<Polygon>>();
        readonly List<(double X, double Z, double R)> circles = new List<(double X, double Z, double R)>();
        readonly List<string> laneKinds = new List<string>();
        readonly List<string> laneIds = new List<string>();
        readonly Dictionary<(long, long), List<int>> grid = new Dictionary<(long, long), List<int>>();

        PreparedSite(SiteTopology topology, ISiteWorld world, TopologyValidator validator, List<(string, int, string, int)> conflicts)
        {
            Topology = topology;
            this.world = world;
            this.conflicts = conflicts;
            var n = 0;
            foreach (var lane in topology.Lanes)
                for (var k = 0; k < lane.Cells.Count; k++, n++)
                {
                    var swept = validator.SweptOf(lane, k);
                    Geometry.CenterRadius(swept, out var cx, out var cz, out var r);
                    sweeps.Add(swept);
                    circles.Add((cx, cz, r));
                    laneKinds.Add(lane.Kind);
                    laneIds.Add(lane.Id);
                    var key = ((long)Math.Floor(cx / Grid), (long)Math.Floor(cz / Grid));
                    if (!grid.TryGetValue(key, out var list)) grid[key] = list = new List<int>();
                    list.Add(n);
                }
        }

        /// <summary>Measures a site: every conflicting pair of lane cells (<see cref="TopologyValidator.Conflicts"/>) and every lane cell's sweep.</summary>
        public static PreparedSite Measure(SiteTopology topology, ISiteWorld world)
        {
            if (topology == null) throw new ArgumentNullException(nameof(topology));
            if (world == null) throw new ArgumentNullException(nameof(world));
            var validator = new TopologyValidator(topology, world);
            var pairs = new List<(string, int, string, int)>();
            foreach (var c in validator.Conflicts()) pairs.Add((c.LaneA, c.CellA, c.LaneB, c.CellB));
            return new PreparedSite(topology, world, validator, pairs);
        }

        public SiteTopology Topology { get; }

        /// <summary>The fleet as laid out (its loop track's period and every machine's offset).</summary>
        public FleetView Fleet => world.Live;

        public IReadOnlyList<(string LaneA, int CellA, string LaneB, int CellB)> Conflicts => conflicts;

        /// <summary>The lane a cell (by <see cref="CellSpace"/> number) belongs to.</summary>
        public string LaneOf(int cell) => laneIds[cell];

        /// <summary>The site's cells numbered, with their conflicts, for one run's interlocking.</summary>
        public CellSpace NewCellSpace() => new CellSpace(Topology, conflicts);

        /// <summary>
        /// A free route a machine of <paramref name="kind"/> drives along <paramref name="poses"/> (in order, a pose at least every
        /// <see cref="FreePoseStepM"/>), cut into cells of <see cref="FreeCellM"/>: for each its length and the lane cells it conflicts with, by
        /// the topology's thresholds (a metre with a haul lane, the access threshold with an access lane). Numbers are those of
        /// <see cref="NewCellSpace"/>'s cells.
        /// </summary>
        public FreeCells Cut(string kind, IReadOnlyList<FreePose> poses)
        {
            if (poses == null || poses.Count < 2) throw new ArgumentException("a free route has two poses at least");
            var lengths = new List<double>();
            var lists = new List<IReadOnlyList<int>>();
            var rules = Topology.Rules;
            var i = 0;
            while (i < poses.Count - 1)
            {
                // the poses of one cell: from i until a cell's length has been driven (the last cell takes what is left)
                var cell = new List<FreePose> { poses[i] };
                var len = 0.0;
                var j = i;
                while (j < poses.Count - 1 && len < FreeCellM - 1e-9)
                {
                    var a = poses[j];
                    var b = poses[j + 1];
                    len += Math.Sqrt((b.X - a.X) * (b.X - a.X) + (b.Z - a.Z) * (b.Z - a.Z));
                    cell.Add(b);
                    j++;
                }

                var swept = Sweep(kind, cell);
                Geometry.CenterRadius(swept, out var cx, out var cz, out var r);
                var found = new List<int>();
                var far = Math.Max(rules.ConflictHaulM, rules.ConflictAccessM);
                long gx = (long)Math.Floor(cx / Grid), gz = (long)Math.Floor(cz / Grid);
                for (var dx = -1; dx <= 1; dx++)
                    for (var dz = -1; dz <= 1; dz++)
                    {
                        if (!grid.TryGetValue((gx + dx, gz + dz), out var near)) continue;
                        foreach (var k in near)
                        {
                            var c = circles[k];
                            if (Math.Sqrt((c.X - cx) * (c.X - cx) + (c.Z - cz) * (c.Z - cz)) > c.R + r + far) continue;
                            var thr = laneKinds[k] == "access" ? rules.ConflictAccessM : rules.ConflictHaulM;
                            if (Geometry.GroupGap(swept, sweeps[k], thr) < thr) found.Add(k);
                        }
                    }

                found.Sort();
                lengths.Add(len);
                lists.Add(found);
                i = j;
            }

            return new FreeCells(lengths, lists);
        }

        // the ground a machine covers over a run of poses: the hull of each of its boxes at one pose and the next
        List<Polygon> Sweep(string kind, List<FreePose> poses)
        {
            var at = new List<IReadOnlyList<Polygon>>(poses.Count);
            foreach (var p in poses) at.Add(world.Footprint(kind, p.X, p.Z, p.HeadingDegrees, 0.0));
            if (at.Count == 1) return new List<Polygon>(at[0]);
            var boxes = new List<Polygon>();
            for (var i = 0; i + 1 < at.Count; i++)
                for (var b = 0; b < at[i].Count; b++)
                {
                    var pts = new List<(double X, double Z)>();
                    foreach (var poly in new[] { at[i][b], at[i + 1][b] })
                        for (var q = 0; q < poly.Count; q++) pts.Add((poly.X[q], poly.Z[q]));
                    boxes.Add(Geometry.Hull(pts));
                }

            return boxes;
        }
    }
}
