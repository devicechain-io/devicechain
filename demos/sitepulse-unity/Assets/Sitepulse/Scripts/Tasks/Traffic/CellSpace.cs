// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;

namespace DeviceChain.Sim.Traffic
{
    /// <summary>
    /// Every cell the interlocking hands out, numbered lane by lane: for each its length, the junction box it is a member of, the station whose
    /// core or exit buffer it is part of, and the cells it conflicts with. Two cells conflict when a machine posed in one would come too near one
    /// posed in the other (the topology's threshold for the pair's class). A cell never conflicts with itself; holding the same cell is the
    /// following rule's business. Fixed when the site loads.
    /// </summary>
    public sealed class CellSpace
    {
        readonly List<string> lanes = new List<string>();
        readonly List<int> indices = new List<int>();
        readonly List<double> lengths = new List<double>();
        readonly List<int> boxes = new List<int>();
        readonly List<int> cores = new List<int>();
        readonly List<int> buffers = new List<int>();
        readonly List<List<int>> conflicts = new List<List<int>>();
        readonly Dictionary<string, int> laneBase = new Dictionary<string, int>(StringComparer.Ordinal);

        /// <summary>
        /// The topology's lane cells, numbered lane by lane in the order the topology lists its lanes, with the junction boxes and stations marked
        /// and the conflicts given (pairs of (lane, cell); the order of a pair does not matter, and a pair given twice counts once).
        /// </summary>
        public CellSpace(SiteTopology topology, IEnumerable<(string LaneA, int CellA, string LaneB, int CellB)> conflictPairs)
        {
            if (topology == null) throw new ArgumentNullException(nameof(topology));
            Topology = topology;
            foreach (var lane in topology.Lanes)
            {
                laneBase[lane.Id] = lanes.Count;
                for (var k = 0; k < lane.Cells.Count; k++)
                {
                    lanes.Add(lane.Id);
                    indices.Add(k);
                    lengths.Add(lane.Cells[k].Length);
                    boxes.Add(-1);
                    cores.Add(-1);
                    buffers.Add(-1);
                    conflicts.Add(new List<int>());
                }
            }

            for (var j = 0; j < topology.Junctions.Count; j++)
                foreach (var m in topology.Junctions[j].Members)
                    foreach (var k in Run(m.Lane, m.Cells))
                    {
                        if (boxes[k] >= 0 && boxes[k] != j)
                            throw new ArgumentException("cell " + Name(k) + " is a member of two junction boxes, " + topology.Junctions[boxes[k]].Id + " and " + topology.Junctions[j].Id);
                        boxes[k] = j;
                    }

            for (var s = 0; s < topology.Stations.Count; s++)
            {
                var st = topology.Stations[s];
                foreach (var k in Run(st.Lane, st.Core)) cores[k] = s;
                foreach (var k in Run(st.Lane, st.Buffer)) buffers[k] = s;
            }

            if (conflictPairs != null)
                foreach (var (la, ca, lb, cb) in conflictPairs)
                    Link(Of(la, ca), Of(lb, cb));
        }

        public SiteTopology Topology { get; }

        /// <summary>How many cells there are: they are numbered from 0.</summary>
        public int Count => lanes.Count;

        /// <summary>The number of cell <paramref name="cell"/> of lane <paramref name="lane"/>.</summary>
        public int Of(string lane, int cell)
        {
            if (!laneBase.TryGetValue(lane, out var b)) throw new KeyNotFoundException("no lane " + lane);
            var n = Topology.Lane(lane).Cells.Count;
            if (cell < 0 || cell >= n) throw new ArgumentOutOfRangeException(nameof(cell), "lane " + lane + " has " + n + " cells, not " + cell);
            return b + cell;
        }

        /// <summary>The cells of a run of a lane, in the order they are driven (a run on a cyclic lane may wrap).</summary>
        public IEnumerable<int> Run(string lane, CellRun run)
        {
            var n = Topology.Lane(lane).Cells.Count;
            var k = run.A;
            while (true)
            {
                yield return Of(lane, k);
                if (k == run.B) yield break;
                k = (k + 1) % n;
            }
        }

        /// <summary>The lane a cell belongs to.</summary>
        public string LaneOf(int cell) => lanes[cell];

        public int IndexOf(int cell) => indices[cell];
        public double Length(int cell) => lengths[cell];

        /// <summary>The junction box a cell is a member of, or -1.</summary>
        public int Box(int cell) => boxes[cell];

        /// <summary>The station whose core a cell is part of, or -1.</summary>
        public int Core(int cell) => cores[cell];

        /// <summary>The station whose exit buffer a cell is part of, or -1.</summary>
        public int Buffer(int cell) => buffers[cell];

        /// <summary>The station whose core or exit buffer a cell is part of, or -1.</summary>
        public int Region(int cell) => cores[cell] >= 0 ? cores[cell] : buffers[cell];

        public IReadOnlyList<int> Conflicts(int cell) => conflicts[cell];

        /// <summary>Whether a cell is only ever entered with a grant (<see cref="Interlocking"/> R4): a junction box's or a station core's.</summary>
        public bool Section(int cell) => boxes[cell] >= 0 || cores[cell] >= 0;

        /// <summary>
        /// Whether a cell is a place a machine granted a section may stand in beyond it: not a section's, and not one that conflicts with a
        /// station's core or buffer (where the station's members, timed apart, are not held to each other: one standing beside them would be in
        /// the way of a member that is not held to it).
        /// </summary>
        public bool Room(int cell)
        {
            if (Section(cell)) return false;
            foreach (var x in conflicts[cell])
                if (Region(x) >= 0) return false;
            return true;
        }

        public string Name(int cell) => cell < 0 || cell >= lanes.Count ? "#" + cell : lanes[cell] + "[" + indices[cell] + "]";

        void Link(int a, int b)
        {
            if (a == b) return;
            if (!conflicts[a].Contains(b)) conflicts[a].Add(b);
            if (!conflicts[b].Contains(a)) conflicts[b].Add(a);
        }
    }
}
