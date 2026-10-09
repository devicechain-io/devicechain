// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;

namespace DeviceChain.Sim.Traffic
{
    /// <summary>
    /// The checks that say a <see cref="SiteTopology"/> is sound, one per property the interlocking rests on. Each returns a list of named
    /// defects, empty when the property holds; a defect says what is wrong and where. They are the C# half of the checks
    /// <c>ArtSource/terrain/quarry_topology.py</c> makes when it writes the file: the same thresholds (the ones the file states in its
    /// <c>rules</c>, and the stand and work-area air below), read off the committed file.
    /// </summary>
    public sealed class TopologyValidator
    {
        /// <summary>The air a stand's footprint keeps from every lane, sweep, junction and outline, in metres.</summary>
        public const double StandClearM = 1.5;

        /// <summary>The air a lane or a stand keeps from another machine's work area, in metres.</summary>
        public const double WorkClearM = 1.5;

        /// <summary>The cells a granted machine needs beyond a junction to stand in: a slot of them.</summary>
        public int RoomCells => (int)Math.Ceiling(topology.SlotM / topology.CellM);

        readonly SiteTopology topology;
        readonly ISiteWorld world;
        readonly Dictionary<string, CellGroup[]> groups = new Dictionary<string, CellGroup[]>(StringComparer.Ordinal);
        List<CellItem> index;

        public TopologyValidator(SiteTopology topology, ISiteWorld world)
        {
            this.topology = topology;
            this.world = world;
        }

        /// <summary>Every check, in order: V1 to V10.</summary>
        public List<string> All()
        {
            var all = new List<string>();
            all.AddRange(CycleCapacity());
            all.AddRange(SpanComplement());
            all.AddRange(Stands());
            all.AddRange(Stations());
            all.AddRange(Boxes());
            all.AddRange(Drivable());
            all.AddRange(Fresh());
            all.AddRange(Zones());
            all.AddRange(WorkAreas());
            all.AddRange(WaitingCells());
            return all;
        }

        static string F(string format, params object[] args) => string.Format(CultureInfo.InvariantCulture, format, args);

        // ---- cells and slots

        /// <summary>How many whole slots a length holds.</summary>
        public int Slots(double metres) => (int)Math.Floor(metres / topology.SlotM + 1e-9);

        /// <summary>The cell indices of a run, in order; a run on a cyclic lane may wrap.</summary>
        public static List<int> RunCells(CellRun run, int n)
        {
            var list = new List<int>();
            if (run.A <= run.B)
            {
                for (var k = run.A; k <= run.B; k++) list.Add(k);
            }
            else
            {
                for (var k = run.A; k < n; k++) list.Add(k);
                for (var k = 0; k <= run.B; k++) list.Add(k);
            }

            return list;
        }

        /// <summary>The sorted cell indices as contiguous runs (on a cyclic lane a run may wrap past the end).</summary>
        public static List<CellRun> RunsOf(IEnumerable<int> cells, int n, bool cyclic)
        {
            var cs = cells.Distinct().OrderBy(c => c).ToList();
            var runs = new List<int[]>();
            if (cs.Count == 0) return new List<CellRun>();
            int a = cs[0], b = cs[0];
            for (var i = 1; i < cs.Count; i++)
            {
                if (cs[i] == b + 1)
                {
                    b = cs[i];
                }
                else
                {
                    runs.Add(new[] { a, b });
                    a = b = cs[i];
                }
            }

            runs.Add(new[] { a, b });
            if (cyclic && runs.Count > 1 && runs[0][0] == 0 && runs[runs.Count - 1][1] == n - 1)
            {
                var last = runs[runs.Count - 1];
                runs.RemoveAt(runs.Count - 1);
                runs[0] = new[] { last[0], runs[0][1] };
            }

            return runs.Select(r => new CellRun(r[0], r[1])).ToList();
        }

        double RangeLength(Lane lane, CellRun run)
        {
            var sum = 0.0;
            foreach (var k in RunCells(run, lane.Cells.Count)) sum += lane.Cells[k].Length;
            return sum;
        }

        static int CycDist(int a, int b, int n)
        {
            var d = Math.Abs(a - b);
            return Math.Min(d, n - d);
        }

        // ---- V1 and V2

        /// <summary>A directed cycle of lanes a machine can circulate on, and the cells of each lane it drives.</summary>
        public sealed class LaneCycle
        {
            public LaneCycle(string name, List<(string Lane, List<int> Cells)> parts)
            {
                Name = name;
                Parts = parts;
            }

            public string Name { get; }
            public List<(string Lane, List<int> Cells)> Parts { get; }
        }

        /// <summary>
        /// Every directed cycle of lanes a machine can circulate on: each cyclic lane itself, and for each stand's way through it the cycle that leaves
        /// the lane at the stand's exit, drives the way through and rejoins at its entry. A machine goes round such a cycle forever as readily as round
        /// the loop, so each must hold everyone that can be on it.
        /// </summary>
        public List<LaneCycle> DirectedCycles()
        {
            var output = new List<LaneCycle>();
            foreach (var lane in topology.Lanes)
                if (lane.Cyclic) output.Add(new LaneCycle(lane.Id, new List<(string, List<int>)> { (lane.Id, Enumerable.Range(0, lane.Cells.Count).ToList()) }));
            foreach (var chain in StandChains())
            {
                var ex = topology.Exits.FirstOrDefault(e => e.To == chain[0]);
                var en = topology.Entries.FirstOrDefault(e => e.From == chain[chain.Length - 1]);
                if (ex == null || en == null || ex.Lane != en.Lane) continue;
                var host = topology.Lane(ex.Lane);
                var n = host.Cells.Count;
                var skipped = new HashSet<int>();
                var k = (ex.Cell + 1) % n;
                while (true)
                {
                    skipped.Add(k);
                    if (k == en.Cell % n) break;
                    k = (k + 1) % n;
                }

                var parts = new List<(string, List<int>)> { (host.Id, Enumerable.Range(0, n).Where(c => !skipped.Contains(c)).ToList()) };
                foreach (var lid in chain) parts.Add((lid, Enumerable.Range(0, topology.Lane(lid).Cells.Count).ToList()));
                output.Add(new LaneCycle(host.Id + " via " + string.Join("/", chain), parts));
            }

            return output;
        }

        /// <summary>The slots a cycle holds outside the station cores that lie on it.</summary>
        public int CycleFreeSlots(LaneCycle cycle)
        {
            var length = 0.0;
            foreach (var part in cycle.Parts)
            {
                var lane = topology.Lane(part.Lane);
                var core = new HashSet<int>();
                foreach (var st in topology.Stations.Where(s => s.Lane == part.Lane))
                    foreach (var k in RunCells(st.Core, lane.Cells.Count)) core.Add(k);
                length += part.Cells.Where(k => !core.Contains(k)).Sum(k => lane.Cells[k].Length);
            }

            return Slots(length);
        }

        /// <summary>V1 (P1): on every directed cycle (the loop, and each detour through a stand's way through) the slots outside station cores hold every machine that can be on it, and one more.</summary>
        public List<string> CycleCapacity()
        {
            var output = new List<string>();
            var nmax = topology.Fleet.Machines;
            foreach (var cycle in DirectedCycles())
            {
                var free = CycleFreeSlots(cycle);
                if (free < nmax + 1)
                    output.Add(F("V1 P1: {0} has {1} slots outside its station cores for {2} machines (needs {3})", cycle.Name, free, nmax, nmax + 1));
            }

            return output;
        }

        /// <summary>The cells of a cyclic lane a junction takes (members and room) and the largest stretch it leaves free.</summary>
        public void JunctionSpan(Junction junc, Lane lane, out HashSet<int> taken, out List<int> gap)
        {
            var n = lane.Cells.Count;
            var held = new HashSet<int>();
            foreach (var m in junc.Members)
                if (m.Lane == lane.Id)
                    foreach (var k in RunCells(m.Cells, n)) held.Add(k);
            foreach (var r in junc.Room)
                if (r.Lane == lane.Id)
                    foreach (var k in RunCells(r.Cells, n)) held.Add(k);
            taken = held;
            gap = new List<int>();
            if (held.Count == 0) return;
            var free = Enumerable.Range(0, n).Where(k => !held.Contains(k)).ToList();
            if (free.Count == 0) return;
            var runs = RunsOf(free, n, true);
            var best = runs[0];
            var bestLen = RunCells(best, n).Count;
            foreach (var r in runs)
            {
                var len = RunCells(r, n).Count;
                if (len > bestLen)
                {
                    best = r;
                    bestLen = len;
                }
            }

            gap = RunCells(best, n);
        }

        /// <summary>The slots every junction's complement holds outside the station cores, on each cyclic lane it has members on: (junction id, lane id, free slots).</summary>
        public List<(string Junction, string Lane, int Free)> SpanComplements()
        {
            var output = new List<(string, string, int)>();
            foreach (var junc in topology.Junctions)
                foreach (var lane in topology.Lanes)
                {
                    if (!lane.Cyclic || !junc.Members.Any(m => m.Lane == lane.Id)) continue;
                    JunctionSpan(junc, lane, out _, out var gap);
                    var gapSet = new HashSet<int>(gap);
                    var length = gap.Sum(k => lane.Cells[k].Length);
                    var core = 0.0;
                    foreach (var st in topology.Stations.Where(s => s.Lane == lane.Id))
                        core += RunCells(st.Core, lane.Cells.Count).Where(gapSet.Contains).Sum(k => lane.Cells[k].Length);
                    output.Add((junc.Id, lane.Id, Slots(length - core)));
                }

            return output;
        }

        /// <summary>V2 (P2): the complement of every junction's span on a cyclic lane holds everyone who can be on the lane but the requester.</summary>
        public List<string> SpanComplement()
        {
            var output = new List<string>();
            var nmax = topology.Fleet.Machines;
            foreach (var c in SpanComplements())
                if (c.Free < nmax - 1)
                    output.Add(F("V2 P2: {0} leaves {1} slots outside station cores on {2} for the {3} other machines (needs {3})", c.Junction, c.Free, c.Lane, nmax - 1));
            return output;
        }

        // ---- the ground a cell sweeps

        sealed class CellGroup
        {
            public IReadOnlyList<Polygon> Boxes;
            public double Cx, Cz, Radius;
        }

        sealed class CellItem
        {
            public string Lane;
            public Lane LaneObj;
            public int Cell;
            public CellGroup Group;
        }

        /// <summary>The pose of a track at time t by linear interpolation of its frames: x, z, heading.</summary>
        public static void PoseAt(TrackView tr, double dt, double t, out double x, out double z, out double heading)
        {
            var n = tr.X.Length;
            var tt = ((t % tr.Period) + tr.Period) % tr.Period;
            var u = tt / dt;
            var i0 = (int)Math.Floor(u);
            var i = i0 % n;
            var j = (i + 1) % n;
            var f = u - i0;
            x = tr.X[i] + (tr.X[j] - tr.X[i]) * f;
            z = tr.Z[i] + (tr.Z[j] - tr.Z[i]) * f;
            var dh = Geometry.Wrap(tr.Heading[j] - tr.Heading[i]);
            heading = (((tr.Heading[i] + dh * f) % 360.0) + 360.0) % 360.0;
        }

        /// <summary>The frame of a track at time t, as the choreography is generated and checked: no interpolation.</summary>
        public static void FrameAt(TrackView tr, double dt, double t, out double x, out double z, out double heading, out double boom)
        {
            var n = tr.X.Length;
            var tt = ((t % tr.Period) + tr.Period) % tr.Period;
            var i = (int)(tt / dt) % n;
            x = tr.X[i];
            z = tr.Z[i];
            heading = tr.Heading[i];
            boom = tr.Boom[i];
        }

        static bool SameCell(double[] a, double[] b) =>
            Math.Round(a[0], 2, MidpointRounding.ToEven) == Math.Round(b[0], 2, MidpointRounding.ToEven)
            && Math.Round(a[1], 2, MidpointRounding.ToEven) == Math.Round(b[1], 2, MidpointRounding.ToEven)
            && Math.Round(a[2], 0, MidpointRounding.ToEven) == Math.Round(b[2], 0, MidpointRounding.ToEven);

        static double HeadingOf(double dx, double dz) => (Math.Atan2(dx, dz) * 180.0 / Math.PI + 360.0) % 360.0;

        /// <summary>
        /// The headings (degrees clockwise from north) a hauler points at the start and at the end of cell k: along the line through the neighbouring
        /// vertices of the lane (a central difference, the tangent of a circle cut at equal steps), and along the cell itself where the lane ends. A
        /// cell is a chord of a curve; its two ends do not point the way the chord does.
        /// </summary>
        public static void CellTangents(Lane lane, int k, out double h0, out double h1)
        {
            var cells = lane.Cells;
            var c = cells[k];
            var chord = c.HeadingDegrees;
            h0 = k > 0 ? HeadingOf(c.X1 - cells[k - 1].X0, c.Z1 - cells[k - 1].Z0) : chord;
            h1 = k + 1 < cells.Count ? HeadingOf(cells[k + 1].X1 - c.X0, cells[k + 1].Z1 - c.Z0) : chord;
        }

        /// <summary>
        /// The poses a hauler takes crossing cell k. On the loop, every frame of the track from the cell's start to the next cell's. On any other lane,
        /// poses along the cell at most <see cref="TopologyRules.PoseStepM"/> apart and turned at most <see cref="TopologyRules.PoseStepDegrees"/> between
        /// poses, each at the tangent heading turned evenly between the cell's two ends.
        /// </summary>
        public List<double[]> CellPoses(Lane lane, int k)
        {
            var c = lane.Cells[k];
            var poses = new List<double[]>();
            if (lane.Kind == "loop")
            {
                var fl = world.Live;
                var tr = fl.Tracks[lane.Track >= 0 ? lane.Track : 0];
                var dt = fl.Dt;
                var t0 = c.T0;
                var t1 = k + 1 < lane.Cells.Count ? lane.Cells[k + 1].T0 : tr.Period;
                var raw = new List<double[]>();
                PoseAt(tr, dt, t0, out var px, out var pz, out var ph);
                raw.Add(new[] { px, pz, ph });
                var i = (int)Math.Floor(t0 / dt) + 1;
                while (i * dt < t1 - 1e-9)
                {
                    PoseAt(tr, dt, i * dt, out px, out pz, out ph);
                    raw.Add(new[] { px, pz, ph });
                    i++;
                }

                PoseAt(tr, dt, Math.Min(t1, tr.Period - 1e-6), out px, out pz, out ph);
                raw.Add(new[] { px, pz, ph });
                foreach (var p in raw)
                    if (!poses.Any(q => SameCell(p, q))) poses.Add(p);
                return poses;
            }

            CellTangents(lane, k, out var h0, out var h1);
            var dh = Geometry.Wrap(h1 - h0);
            var rules = topology.Rules;
            var n = Math.Max(1, Math.Max((int)Math.Ceiling(c.Length / rules.PoseStepM - 1e-9), (int)Math.Ceiling(Math.Abs(dh) / rules.PoseStepDegrees - 1e-9)));
            for (var i = 0; i <= n; i++)
                poses.Add(new[] { c.X0 + (c.X1 - c.X0) * i / n, c.Z0 + (c.Z1 - c.Z0) * i / n, (((h0 + dh * i / n) % 360.0) + 360.0) % 360.0 });
            return poses;
        }

        /// <summary>
        /// The ground a hauler covers over a run of poses, as convex polygons: for each of its boxes, the hull of that box at one pose and the next (what
        /// a box passes over turning and moving between the two). The hull covers a little more than the box sweeps, so the gap read is never more than
        /// the gap to the continuous sweep and at most 0.03 m less.
        /// </summary>
        List<Polygon> Swept(List<double[]> poses)
        {
            var at = poses.Select(p => world.Footprint("Hauler", p[0], p[1], p[2], 0.0)).ToList();
            if (at.Count == 1) return at[0].ToList();
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

        /// <summary>The swept ground of cell k of a lane (which need not be one of the topology's), as the checks measure it.</summary>
        public List<Polygon> CellSwept(Lane lane, int k) => Swept(CellPoses(lane, k));

        CellGroup[] GroupsOf(Lane lane)
        {
            if (groups.TryGetValue(lane.Id, out var cached)) return cached;
            var arr = new CellGroup[lane.Cells.Count];
            for (var k = 0; k < arr.Length; k++)
            {
                var boxes = Swept(CellPoses(lane, k));
                Geometry.CenterRadius(boxes, out var cx, out var cz, out var r);
                arr[k] = new CellGroup { Boxes = boxes, Cx = cx, Cz = cz, Radius = r };
            }

            groups[lane.Id] = arr;
            return arr;
        }

        List<CellItem> CellIndex()
        {
            if (index != null) return index;
            index = new List<CellItem>();
            foreach (var lane in topology.Lanes)
            {
                var gs = GroupsOf(lane);
                for (var k = 0; k < gs.Length; k++) index.Add(new CellItem { Lane = lane.Id, LaneObj = lane, Cell = k, Group = gs[k] });
            }

            return index;
        }

        /// <summary>
        /// Every pair of cells whose swept footprints come nearer than the threshold of the pair's class (<see cref="TopologyRules.ConflictM"/>): of two
        /// different lanes, and of one lane when they are further apart along it than its neighbour span (nearer than that they are a machine and its
        /// follower, which the follower rule keeps apart) and overlap. <paramref name="reach"/> lists the pairs nearer than that instead (to measure how
        /// near the pairs that do not conflict come).
        /// </summary>
        public List<(string LaneA, int CellA, string LaneB, int CellB, double Gap)> Conflicts(double? reach = null)
        {
            var items = CellIndex();
            var rules = topology.Rules;
            var far = Math.Max(Math.Max(rules.ConflictHaulM, rules.ConflictAccessM), Math.Max(rules.ConflictSameRouteM, reach ?? 0.0));
            var grid = new Dictionary<(long, long), List<int>>();
            for (var n = 0; n < items.Count; n++)
            {
                var key = ((long)Math.Floor(items[n].Group.Cx / 30.0), (long)Math.Floor(items[n].Group.Cz / 30.0));
                if (!grid.TryGetValue(key, out var list)) grid[key] = list = new List<int>();
                list.Add(n);
            }

            var found = new List<(string, int, string, int, double)>();
            for (var n = 0; n < items.Count; n++)
            {
                var it = items[n];
                long gx = (long)Math.Floor(it.Group.Cx / 30.0), gz = (long)Math.Floor(it.Group.Cz / 30.0);
                for (var dx = -1; dx <= 1; dx++)
                    for (var dz = -1; dz <= 1; dz++)
                    {
                        if (!grid.TryGetValue((gx + dx, gz + dz), out var near)) continue;
                        foreach (var m in near)
                        {
                            if (m <= n) continue;
                            var jt = items[m];
                            var same = it.Lane == jt.Lane;
                            if (same && it.LaneObj.Along(it.Cell, jt.Cell) <= it.LaneObj.NeighbourSpanM) continue;
                            var thr = reach ?? (same ? rules.ConflictSameRouteM : rules.ConflictM(it.LaneObj.Kind, jt.LaneObj.Kind));
                            var d = Math.Sqrt((it.Group.Cx - jt.Group.Cx) * (it.Group.Cx - jt.Group.Cx) + (it.Group.Cz - jt.Group.Cz) * (it.Group.Cz - jt.Group.Cz));
                            if (d > it.Group.Radius + jt.Group.Radius + far) continue;
                            var gap = Geometry.GroupGap(it.Group.Boxes, jt.Group.Boxes, thr);
                            if (gap < thr) found.Add((it.Lane, it.Cell, jt.Lane, jt.Cell, gap));
                        }
                    }
            }

            return found.OrderBy(f => f.Item1, StringComparer.Ordinal).ThenBy(f => f.Item2).ThenBy(f => f.Item3, StringComparer.Ordinal).ThenBy(f => f.Item4).ToList();
        }

        /// <summary>{(lane, cell): [(chain, arc length along it to the middle of the cell)]} for every cell of every stand's way through it.</summary>
        public Dictionary<(string, int), List<(string Chain, double S)>> RouteIndex()
        {
            var routes = new Dictionary<(string, int), List<(string, double)>>();
            foreach (var chain in StandChains())
                foreach (var kv in ChainCoords(chain))
                {
                    if (!routes.TryGetValue(kv.Key, out var l)) routes[kv.Key] = l = new List<(string, double)>();
                    l.Add((string.Join("|", chain), kv.Value));
                }

            return routes.ToDictionary(kv => kv.Key, kv => kv.Value);
        }

        /// <summary>The cells a station's trucks are timed through, its core and its exit buffer, on its lane. V4 measures two trucks there at every headway.</summary>
        public List<(string Id, string Lane, HashSet<int> Cells)> StationRegions()
        {
            var output = new List<(string, string, HashSet<int>)>();
            foreach (var st in topology.Stations)
            {
                var n = topology.Lane(st.Lane).Cells.Count;
                var cells = new HashSet<int>(RunCells(st.Core, n));
                foreach (var k in RunCells(st.Buffer, n)) cells.Add(k);
                output.Add((st.Id, st.Lane, cells));
            }

            return output;
        }

        /// <summary>
        /// What holds a conflict between cells a and b other than a junction box, or null when only a box can: "route" (the two are on one stand's way
        /// through it, within a neighbour span of each other along it or not overlapping), "station" (both lie in one station's region on its lane) or
        /// "diverge" (one is on the lane leaving an exit, in the part it shares with the lane it leaves).
        /// </summary>
        public string Covering((string, int) a, (string, int) b, double gap, Dictionary<(string, int), List<(string Chain, double S)>> routes, List<(string Id, string Lane, HashSet<int> Cells)> regions)
        {
            var span = topology.Lane(a.Item1).NeighbourSpanM;
            if (routes.TryGetValue(a, out var la) && routes.TryGetValue(b, out var lb)
                && la.Any(x => lb.Any(y => x.Chain == y.Chain && (Math.Abs(x.S - y.S) <= span || gap >= topology.Rules.ConflictSameRouteM)))) return "route";
            if (a.Item1 == b.Item1 && regions.Any(r => r.Lane == a.Item1 && r.Cells.Contains(a.Item2) && r.Cells.Contains(b.Item2))) return "station";
            foreach (var ex in topology.Exits)
                foreach (var pair in new[] { (a, b), (b, a) })
                {
                    var x = pair.Item1;
                    var y = pair.Item2;
                    if (x.Item1 == ex.To && y.Item1 == ex.Lane && x.Item2 < ex.Shared && CycDist(y.Item2, ex.Cell, topology.Lane(ex.Lane).Cells.Count) <= topology.Rules.DivergeWindow) return "diverge";
                }

            return null;
        }

        // ---- the way through a stand

        /// <summary>The lanes a machine drives through a stand, in order: (in, out) for a stand, (in, hop, out) for the refuel bay.</summary>
        public List<string[]> StandChains()
        {
            var chains = new List<string[]>();
            foreach (var s in topology.Stands)
                if (topology.Bay == null || (s.Id != topology.Bay.Queue && s.Id != topology.Bay.BayStand)) chains.Add(new[] { s.In, s.Out });
            if (topology.Bay != null) chains.Add(new[] { topology.Bay.In, topology.Bay.Hop, topology.Bay.Out });
            return chains;
        }

        /// <summary>Arc length along a chain to the middle of each of its cells.</summary>
        public Dictionary<(string, int), double> ChainCoords(string[] chain)
        {
            var coords = new Dictionary<(string, int), double>();
            var s = 0.0;
            foreach (var lid in chain)
            {
                var lane = topology.Lane(lid);
                for (var k = 0; k < lane.Cells.Count; k++)
                {
                    var len = lane.Cells[k].Length;
                    coords[(lid, k)] = s + len / 2.0;
                    s += len;
                }
            }

            return coords;
        }

        /// <summary>The lanes of every chain a stand's own lanes are part of.</summary>
        HashSet<string> OwnLanes(StandPlace stand)
        {
            var mine = new HashSet<string>(StringComparer.Ordinal) { stand.In, stand.Out };
            foreach (var chain in StandChains())
                if (chain.Any(mine.Contains))
                    foreach (var l in chain) mine.Add(l);
            return mine;
        }

        bool IsExempt(string who, string obstacle, double x, double z)
        {
            foreach (var e in topology.Exempt)
            {
                if (e.To != who || e.Obstacle == null || e.Obstacle != obstacle) continue;
                if (!e.HasSpot) return true;
                if (Math.Sqrt((x - e.SpotX) * (x - e.SpotX) + (z - e.SpotZ) * (z - e.SpotZ)) <= e.RadiusM) return true;
            }

            return false;
        }

        // ---- V3

        sealed class Sweep
        {
            public string Fleet;
            public int Track;
            public string Kind;
            public List<string> Owners;
            public List<double[]> Poses;
        }

        List<Sweep> sweeps;

        /// <summary>Every distinct pose of every track of both fleets, parked machines included.</summary>
        List<Sweep> TrackSweeps()
        {
            if (sweeps != null) return sweeps;
            sweeps = new List<Sweep>();
            foreach (var pair in new[] { ("live", world.Live), ("preview", world.Preview) })
            {
                var fl = pair.Item2;
                for (var ti = 0; ti < fl.Tracks.Count; ti++)
                {
                    var tr = fl.Tracks[ti];
                    var owners = fl.Machines.Where(m => m.Track == ti).Select(m => m.Id).ToList();
                    var poses = new List<double[]>();
                    double[] last = null;
                    for (var i = 0; i < tr.X.Length; i++)
                    {
                        double x = tr.X[i], z = tr.Z[i], hd = tr.Heading[i];
                        if (last != null && Math.Sqrt((x - last[0]) * (x - last[0]) + (z - last[1]) * (z - last[1])) < 0.2 && Math.Abs(Geometry.Wrap(hd - last[2])) < 2.0) continue;
                        last = new[] { x, z, hd };
                        poses.Add(new[] { x, z, hd, tr.Boom[i] });
                    }

                    sweeps.Add(new Sweep { Fleet = pair.Item1, Track = ti, Kind = tr.Kind, Owners = owners, Poses = poses });
                }
            }

            return sweeps;
        }

        bool TrackExempt(string standId, List<string> owners)
        {
            var ms = topology.Exempt.Where(e => e.To == standId && e.Machine != null).Select(e => e.Machine).ToList();
            return owners.Count == 1 && ms.Contains(owners[0]);
        }

        /// <summary>How near a stand's footprint (for one kind) comes to each class of thing it must keep clear of: the worst of each.</summary>
        public Dictionary<string, (double Gap, string Where)> StandGaps(StandPlace s, string kind)
        {
            var own = OwnLanes(s);
            var g = world.Footprint(kind, s.X, s.Z, s.HeadingDegrees, 0.0);
            Geometry.CenterRadius(g, out var cx, out var cz, out var r);
            var worst = new Dictionary<string, (double, string)>();

            void Note(string what, double gap, string where)
            {
                if (!worst.TryGetValue(what, out var cur) || gap < cur.Item1) worst[what] = (gap, where);
            }

            foreach (var it in CellIndex())
            {
                if (own.Contains(it.Lane)) continue;
                var d = Math.Sqrt((cx - it.Group.Cx) * (cx - it.Group.Cx) + (cz - it.Group.Cz) * (cz - it.Group.Cz));
                if (d > r + it.Group.Radius + StandClearM) continue;
                Note("lane", Geometry.GroupGap(g, it.Group.Boxes), F("{0} cell {1}", it.Lane, it.Cell));
            }

            foreach (var sw in TrackSweeps())
            {
                if (TrackExempt(s.Id, sw.Owners)) continue;
                foreach (var p in sw.Poses)
                {
                    if (Math.Sqrt((cx - p[0]) * (cx - p[0]) + (cz - p[1]) * (cz - p[1])) > r + 14.0) continue;
                    Note("sweep", Geometry.GroupGap(g, world.Footprint(sw.Kind, p[0], p[1], p[2], p[3])), F("{0} fleet track {1} ({2})", sw.Fleet, sw.Track, string.Join("/", sw.Owners)));
                }
            }

            foreach (var j in topology.Junctions) Note("junction", Geometry.GroupGap(g, new[] { j.Polygon }), j.Id);

            foreach (var o in world.Obstacles)
            {
                var d = Math.Sqrt((cx - o.CenterX) * (cx - o.CenterX) + (cz - o.CenterZ) * (cz - o.CenterZ));
                if (d > r + o.Reach + StandClearM || IsExempt(s.Id, o.Name, o.CenterX, o.CenterZ)) continue;
                Note("obstacle", o.Gap(g), F("{0} at ({1:0.0}, {2:0.0})", o.Name, o.CenterX, o.CenterZ));
            }

            return worst.ToDictionary(kv => kv.Key, kv => kv.Value);
        }

        static int KindRank(string kind) => kind == "Hauler" ? 0 : kind == "Loader" ? 1 : 2;

        /// <summary>V3 (P3): every stand, queue and bay keeps its air, does not overlap another, is driven through and stands a slot past any junction on the lane it is reached by.</summary>
        public List<string> Stands()
        {
            var output = new List<string>();
            var idx = CellIndex();
            foreach (var s in topology.Stands)
            {
                if (s.Kinds.Count == 0) output.Add(F("V3 stand {0}: it lists no kind of machine, so nothing would be checked of its footprint", s.Id));
                foreach (var kind in s.Kinds)
                    foreach (var kv in StandGaps(s, kind).OrderBy(k => k.Key, StringComparer.Ordinal))
                        if (kv.Value.Gap < StandClearM)
                            output.Add(F("V3 stand {0} ({1}): {2:0.00} m from {3} {4}, {5:0.0} m is kept", s.Id, kind, kv.Value.Gap, kv.Key, kv.Value.Where, StandClearM));
            }

            var ids = topology.Stands.OrderBy(s => s.Id, StringComparer.Ordinal).ToList();
            for (var i = 0; i < ids.Count; i++)
                for (var j = i + 1; j < ids.Count; j++)
                {
                    var a = ids[i];
                    var b = ids[j];
                    if (topology.Bay != null && ((a.Id == topology.Bay.Queue && b.Id == topology.Bay.BayStand) || (b.Id == topology.Bay.Queue && a.Id == topology.Bay.BayStand))) continue;
                    if (a.Kinds.Count == 0 || b.Kinds.Count == 0) continue;
                    var ka = a.Kinds.OrderBy(KindRank).First();
                    var kb = b.Kinds.OrderBy(KindRank).First();
                    var gap = Geometry.GroupGap(world.Footprint(ka, a.X, a.Z, a.HeadingDegrees, 0.0), world.Footprint(kb, b.X, b.Z, b.HeadingDegrees, 0.0));
                    if (gap < 0.0) output.Add(F("V3 stand {0}: overlaps stand {1} by {2:0.00} m", a.Id, b.Id, -gap));
                }

            // a stand is driven through, never backed out of: along the whole way through it (in, hop, out) the lanes do not conflict beyond a machine's
            // own length (a follower a slot behind keeps what air the bend leaves it: the lanes may not overlap). A way through that crosses itself
            // anywhere is a cul-de-sac for whoever waits at the crossing, whether or not a box holds the crossing
            foreach (var chain in StandChains())
            {
                var coords = ChainCoords(chain);
                var span = topology.Lane(chain[0]).NeighbourSpanM;
                var thr = topology.Rules.ConflictSameRouteM;
                var cells = new List<(string, int)>();
                foreach (var lid in chain)
                    for (var k = 0; k < topology.Lane(lid).Cells.Count; k++) cells.Add((lid, k));
                var pos = idx.Where(it => chain.Contains(it.Lane)).ToDictionary(it => (it.Lane, it.Cell), it => it.Group);
                var seen = false;
                for (var i = 0; i < cells.Count && !seen; i++)
                    for (var j = i + 1; j < cells.Count; j++)
                    {
                        var a = cells[i];
                        var b = cells[j];
                        if (a.Item1 == b.Item1 || Math.Abs(coords[a] - coords[b]) <= span) continue;
                        var ga = pos[a];
                        var gb = pos[b];
                        var d = Math.Sqrt((ga.Cx - gb.Cx) * (ga.Cx - gb.Cx) + (ga.Cz - gb.Cz) * (ga.Cz - gb.Cz));
                        if (d > ga.Radius + gb.Radius + thr) continue;
                        if (Geometry.GroupGap(ga.Boxes, gb.Boxes, thr) < thr)
                        {
                            output.Add(F("V3 cul-de-sac: the lanes {0} conflict at {1} cell {2} and {3} cell {4}, {5:0} m apart along the way through",
                                string.Join(" -> ", chain), a.Item1, a.Item2, b.Item1, b.Item2, Math.Abs(coords[a] - coords[b])));
                            seen = true;
                            break;
                        }
                    }
            }

            foreach (var s in topology.Stands)
                foreach (var pair in new[] { (s.In, true) })
                {
                    var lane = topology.Lane(pair.Item1);
                    var n = lane.Cells.Count;
                    foreach (var j in topology.Junctions)
                    {
                        var cells = new HashSet<int>();
                        foreach (var m in j.Members.Concat(j.Room))
                            if (m.Lane == pair.Item1)
                                foreach (var k in RunCells(m.Cells, n)) cells.Add(k);
                        foreach (var k in cells.OrderBy(c => c))
                        {
                            var dist = 0.0;
                            if (pair.Item2)
                                for (var q = k; q < n; q++) dist += lane.Cells[q].Length;
                            else
                                for (var q = 0; q <= k; q++) dist += lane.Cells[q].Length;
                            if (dist < topology.SlotM)
                            {
                                output.Add(F("V3 stand {0}: junction {1} is {2:0.0} m from its pose along {3} (a slot is {4:0.0} m)", s.Id, j.Id, dist, pair.Item1, topology.SlotM));
                                break;
                            }
                        }
                    }
                }

            return output;
        }

        // ---- V4

        /// <summary>The track times a station's region runs from and to: (start of the core, end of the core, end of the buffer).</summary>
        public void StationTimes(StationSpec st, out double tIn, out double tCore, out double tBuffer)
        {
            var loop = topology.Lane(st.Lane);
            var n = loop.Cells.Count;
            var period = topology.Fleet.PeriodSeconds;
            tIn = loop.Cells[st.Core.A].T0;
            tCore = CellTime(loop, st.Core.B + 1, period);
            tBuffer = CellTime(loop, st.Buffer.B + 1, period);
            if (tCore < tIn) tCore += period;
            if (tBuffer < tCore) tBuffer += period;
        }

        static double CellTime(Lane loop, int k, double period)
        {
            var n = loop.Cells.Count;
            return k < n ? loop.Cells[k].T0 : period + loop.Cells[k - n].T0;
        }

        /// <summary>The nearest two haul trucks come in a station's region over every headway from the design spacing to the spacing plus the window, at the fleet's frames.</summary>
        public double StationMinGap(double tIn, double tOut, double headway, double window, out double atHeadway, out double atU)
        {
            var fl = world.Live;
            var tr = fl.Tracks[0];
            var dt = fl.Dt;
            var count = (int)Math.Floor((tOut - tIn) / dt + 1e-9) + 1;
            var lead = new List<(double X, double Z, IReadOnlyList<Polygon> Boxes)>();
            for (var i = 0; i < count; i++)
            {
                FrameAt(tr, dt, tIn + i * dt, out var x, out var z, out var h, out var b);
                lead.Add((x, z, world.Footprint("Hauler", x, z, h, b)));
            }

            var best = double.MaxValue;
            atHeadway = 0.0;
            atU = 0.0;
            var hh = headway;
            while (hh <= headway + window + 1e-9)
            {
                for (var i = 0; i < count; i++)
                {
                    var u = i * dt;
                    FrameAt(tr, dt, tIn + u - hh, out var bx, out var bz, out var bh, out var bb);
                    if (Math.Sqrt((lead[i].X - bx) * (lead[i].X - bx) + (lead[i].Z - bz) * (lead[i].Z - bz)) > 30.0) continue;
                    var gap = Geometry.GroupGap(lead[i].Boxes, world.Footprint("Hauler", bx, bz, bh, bb));
                    if (gap < best)
                    {
                        best = gap;
                        atHeadway = hh;
                        atU = u;
                    }
                }

                hh += dt;
            }

            return best;
        }

        /// <summary>The nearest the station's coupled loader comes to the truck in its core, over every truck and every moment the truck is in the core.</summary>
        public double PartnerMinGap(StationSpec st)
        {
            var fl = world.Live;
            var dt = fl.Dt;
            var tr0 = fl.Tracks[0];
            var period = tr0.Period;
            var ltr = fl.Tracks[st.Partner.Track];
            var lm = fl.Machines.First(m => m.Id == st.Partner.Machine);
            StationTimes(st, out var tIn, out var tCore, out _);
            var best = double.MaxValue;
            foreach (var m in fl.Machines)
            {
                if (m.Track != 0) continue;
                var s = 0.0;
                while (s < period)
                {
                    var tt = (((s - m.Offset) % period) + period) % period;
                    if ((tIn <= tt && tt <= Math.Min(tCore, period)) || (tCore > period && tt <= tCore - period))
                    {
                        FrameAt(tr0, dt, tt, out var ax, out var az, out var ah, out var ab);
                        FrameAt(ltr, dt, s - lm.Offset, out var bx, out var bz, out var bh, out var bb);
                        if (Math.Sqrt((ax - bx) * (ax - bx) + (az - bz) * (az - bz)) < 30.0)
                            best = Math.Min(best, Geometry.GroupGap(world.Footprint("Hauler", ax, az, ah, ab), world.Footprint("Loader", bx, bz, bh, bb)));
                    }

                    s += dt;
                }
            }

            return best;
        }

        /// <summary>V4: every station is safe at every headway from the design spacing up, its coupled loader clears its truck, its buffer holds its capacity in slots and its headway is the fleet's spacing.</summary>
        public List<string> Stations()
        {
            var output = new List<string>();
            var fl = world.Live;
            var spacing = fl.Tracks[0].Period / fl.Machines.Count(m => m.Track == 0);
            var loop = topology.Loop;
            foreach (var st in topology.Stations)
            {
                if (Math.Abs(st.HeadwaySeconds - spacing) > 0.0011)
                    output.Add(F("V4 station {0}: its headway is {1:0.000} s and the fleet's spacing is {2:0.000} s", st.Id, st.HeadwaySeconds, spacing));
                var buf = Slots(RangeLength(loop, st.Buffer));
                if (buf < st.Capacity)
                    output.Add(F("V4 station {0}: its exit buffer holds {1} slots for a capacity of {2}", st.Id, buf, st.Capacity));
                StationTimes(st, out var tIn, out _, out var tOut);
                var gap = StationMinGap(tIn, tOut, st.HeadwaySeconds, st.WindowSeconds, out var h, out var u);
                if (gap < 0.0) output.Add(F("V4 station {0}: two trucks {1:0.00} s apart overlap by {2:0.00} m, {3:0.0} s into it", st.Id, h, -gap, u));
                if (Math.Abs(gap - st.MinGapM) > 0.006)
                    output.Add(F("V4 station {0}: it records a nearest approach of {1:0.000} m and measures {2:0.000} m", st.Id, st.MinGapM, gap));
                if (st.Partner != null)
                {
                    var pg = PartnerMinGap(st);
                    if (pg < 0.0) output.Add(F("V4 station {0}: its loader {1} overlaps the truck by {2:0.00} m", st.Id, st.Partner.Machine, -pg));
                    if (Math.Abs(pg - st.Partner.MinGapM) > 0.006)
                        output.Add(F("V4 station {0}: it records a loader gap of {1:0.000} m and measures {2:0.000} m", st.Id, st.Partner.MinGapM, pg));
                }
            }

            return output;
        }

        // ---- V5

        HashSet<int> JunctionCells(Junction j, Lane lane)
        {
            var set = new HashSet<int>();
            foreach (var m in j.Members)
                if (m.Lane == lane.Id)
                    foreach (var k in RunCells(m.Cells, lane.Cells.Count)) set.Add(k);
            return set;
        }

        /// <summary>
        /// V5: every conflict between two cells lies inside one junction box (or is held by what <see cref="Covering"/> says: a stand's own way through
        /// it, a station, a diverge) - including two cells of one lane further apart along it than its neighbour span that overlap, which is a lane
        /// folding back over itself; no box holds the approach cell where its own requester waits; a box's members are exactly the cells with an end in
        /// its polygon.
        /// </summary>
        public List<string> Boxes()
        {
            var output = new List<string>();
            var byJunc = topology.Junctions.ToDictionary(j => j.Id, j => j.Members.Select(m => m.Lane).Distinct().ToDictionary(l => l, l => JunctionCells(j, topology.Lane(l))));
            var routes = RouteIndex();
            var regions = StationRegions();
            var seen = new HashSet<(string, string)>();
            foreach (var cf in Conflicts())
            {
                var a = (cf.LaneA, cf.CellA);
                var b = (cf.LaneB, cf.CellB);
                if (byJunc.Values.Any(cells => cells.TryGetValue(a.Item1, out var ca) && ca.Contains(a.Item2) && cells.TryGetValue(b.Item1, out var cb) && cb.Contains(b.Item2))) continue;
                if (Covering(a, b, cf.Gap, routes, regions) != null) continue;
                if (!seen.Add((a.Item1, b.Item1))) continue;
                var c = topology.Lane(a.Item1).Cells[a.Item2];
                var same = a.Item1 == b.Item1 ? F(", the same lane {0:0} m apart along it", topology.Lane(a.Item1).Along(a.Item2, b.Item2)) : "";
                output.Add(F("V5 conflict: {0} cell {1} and {2} cell {3} come within {4:0.00} m of each other at ({5:0.0}, {6:0.0}){7} and no junction holds both",
                    a.Item1, a.Item2, b.Item1, b.Item2, cf.Gap, c.X0, c.Z0, same));
            }

            foreach (var j in topology.Junctions)
            {
                foreach (var ap in j.Approach)
                    if (JunctionCells(j, topology.Lane(ap.Lane)).Contains(ap.Cell))
                        output.Add(F("V5 approach: junction {0} holds the approach cell {1} of {2} among its own members (it reaches back over its approach)", j.Id, ap.Cell, ap.Lane));
                var want = new Dictionary<string, HashSet<int>>();
                foreach (var m in j.Members)
                {
                    if (!want.TryGetValue(m.Lane, out var set)) want[m.Lane] = set = new HashSet<int>();
                    foreach (var k in RunCells(m.Cells, topology.Lane(m.Lane).Cells.Count)) set.Add(k);
                }

                foreach (var lane in topology.Lanes)
                {
                    var got = new HashSet<int>();
                    for (var k = 0; k < lane.Cells.Count; k++)
                        if (Geometry.InPolygon(j.Polygon, lane.Cells[k].X0, lane.Cells[k].Z0) || Geometry.InPolygon(j.Polygon, lane.Cells[k].X1, lane.Cells[k].Z1)) got.Add(k);
                    want.TryGetValue(lane.Id, out var w);
                    if (!got.SetEquals(w ?? new HashSet<int>()))
                        output.Add(F("V5 members: junction {0} lists cells of {1} that are not the ones inside its polygon", j.Id, lane.Id));
                }
            }

            return output;
        }

        // ---- V6

        /// <summary>
        /// V6: every lane's driven line is within the grade a planned route is held to, and every pose a hauler takes along it (every pose of
        /// <see cref="CellPoses"/>: each cell's start, its middle where the cell is long or turns, its end, and on the loop every frame of the track) is
        /// clear of every outline by the reach a driving hauler keeps (<see cref="ISiteWorld.TravelReach"/>: its half width and the air kept - the task
        /// layer's own rule for a driving machine, which the lanes are held to), except what the table says a lane is laid out between.
        /// </summary>
        public List<string> Drivable()
        {
            var output = new List<string>();
            foreach (var lane in topology.Lanes)
            {
                var xs = lane.Cells.Select(c => c.X0).Concat(new[] { lane.Cells[lane.Cells.Count - 1].X1 }).ToList();
                var zs = lane.Cells.Select(c => c.Z0).Concat(new[] { lane.Cells[lane.Cells.Count - 1].Z1 }).ToList();
                var grade = world.SustainedGrade(xs, zs);
                if (grade.Percent > world.MaxGradePercent + 1e-9)
                    output.Add(F("V6 grade: {0} climbs {1:0.00} % sustained at ({2:0.0}, {3:0.0}); a planned route is held to {4:0} %", lane.Id, grade.Percent, grade.X, grade.Z, world.MaxGradePercent));
                var near = new Dictionary<string, (double D, int K, double X, double Z)>();
                for (var k = 0; k < lane.Cells.Count; k++)
                    foreach (var p in CellPoses(lane, k))
                        foreach (var o in world.Obstacles)
                        {
                            var d = o.Distance(p[0], p[1]);
                            if (d >= world.TravelReach || IsExempt(lane.Id, o.Name, o.CenterX, o.CenterZ)) continue;
                            if (!near.TryGetValue(o.Name, out var cur) || d < cur.D) near[o.Name] = (d, k, p[0], p[1]);
                        }

                foreach (var kv in near.OrderBy(k => k.Key, StringComparer.Ordinal))
                    output.Add(F("V6 reach: {0} cell {1} comes {2:0.00} m from {3} at ({4:0.0}, {5:0.0}); a driving hauler keeps {6:0.0} m", lane.Id, kv.Value.K, kv.Value.D, kv.Key, kv.Value.X, kv.Value.Z, world.TravelReach));
            }

            return output;
        }

        // ---- V10

        /// <summary>
        /// V10: no junction box has an approach cell, a member or a room cell inside a station core unless the station holds the box. V4 measures two
        /// trucks through a core at every headway, which is only the whole truth while nobody waits in the core: a truck held at a box's approach, or
        /// standing in its room, inside a core is closed on by the next member the station admits. Where a box cannot lie clear of a core, the station
        /// holds it (admission takes the box's grant too, before the truck enters the core). A hold must name a box that touches the core.
        /// </summary>
        public List<string> WaitingCells()
        {
            var output = new List<string>();
            var ids = new HashSet<string>(topology.Junctions.Select(j => j.Id), StringComparer.Ordinal);
            foreach (var st in topology.Stations)
            {
                var lane = topology.Lane(st.Lane);
                var n = lane.Cells.Count;
                var core = new HashSet<int>(RunCells(st.Core, n));
                var held = new HashSet<string>(st.Holds, StringComparer.Ordinal);
                foreach (var jid in held.Where(h => !ids.Contains(h)).OrderBy(h => h, StringComparer.Ordinal))
                    output.Add(F("V10 waiting: station {0} holds junction {1}, which the file does not have", st.Id, jid));
                foreach (var j in topology.Junctions)
                {
                    var touching = new List<(string What, List<int> Inside)>();
                    var approach = j.Approach.Where(a => a.Lane == lane.Id).Select(a => a.Cell);
                    var members = JunctionCells(j, lane).OrderBy(k => k);
                    var room = j.Room.Where(r => r.Lane == lane.Id).SelectMany(r => RunCells(r.Cells, n));
                    foreach (var (what, cells) in new[] { ("approach cell", approach), ("member cell", members), ("room cell", room) })
                    {
                        var inside = cells.Where(core.Contains).Distinct().OrderBy(k => k).ToList();
                        if (inside.Count > 0) touching.Add((what, inside));
                    }

                    if (held.Contains(j.Id))
                    {
                        if (touching.Count == 0)
                            output.Add(F("V10 waiting: station {0} holds junction {1}, which has no approach, member or room cell in its core (a hold nothing needs)", st.Id, j.Id));
                        continue;
                    }

                    foreach (var (what, inside) in touching)
                        output.Add(F("V10 waiting: junction {0} has {1} {2} on {3} inside the core of station {4} ({5}-{6}), where a held truck is closed on by the next member and the station does not hold the box",
                            j.Id, what, inside.Count < 4 ? string.Join(", ", inside) : inside[0] + "-" + inside[inside.Count - 1], lane.Id, st.Id, st.Core.A, st.Core.B));
                }
            }

            return output;
        }

        // ---- V7, V8, V9

        /// <summary>V7: the topology was made from the files that are committed, and its loop is the live fleet's track.</summary>
        public List<string> Fresh()
        {
            var output = new List<string>();
            var want = world.Hashes;
            var have = topology.Source;
            if (have.Features != want.Features) output.Add("V7 fresh: its features hash is not the committed file's");
            if (have.Heights != want.Heights) output.Add("V7 fresh: its heights hash is not the committed file's");
            if (have.Fleet != want.Fleet) output.Add("V7 fresh: its fleet hash is not the committed file's");
            if (have.FleetPreview != want.FleetPreview) output.Add("V7 fresh: its fleet_preview hash is not the committed file's");
            var fl = world.Live;
            var tr = fl.Tracks[0];
            var nLoop = fl.Machines.Count(m => m.Track == 0);
            var f = topology.Fleet;
            if (f.LoopTrucks != nLoop || f.Machines != fl.Machines.Count || Math.Abs(f.PeriodSeconds - tr.Period) > 1e-6)
                output.Add(F("V7 fresh: its fleet block ({0} on the loop of {1}, {2:0.000} s) is not the live fleet's ({3} of {4}, {5:0.000} s)", f.LoopTrucks, f.Machines, f.PeriodSeconds, nLoop, fl.Machines.Count, tr.Period));
            var worst = 0.0;
            var at = 0;
            var loop = topology.Loop;
            for (var k = 0; k < loop.Cells.Count; k++)
            {
                var c = loop.Cells[k];
                PoseAt(tr, fl.Dt, c.T0, out var x, out var z, out _);
                var off = Math.Sqrt((x - c.X0) * (x - c.X0) + (z - c.Z0) * (z - c.Z0));
                if (off > worst)
                {
                    worst = off;
                    at = k;
                }
            }

            if (worst > 0.05) output.Add(F("V7 fresh: loop cell {0} starts {1:0.000} m from where the track is at its time (at most 0.05 m)", at, worst));
            return output;
        }

        /// <summary>
        /// V8: every zone has a stand for each kind goto-area may send there, inside the zone. Only a zone stand counts: the refuel queue and bay are
        /// service stands (a machine goes there to be refuelled and moves on), so a zone is not covered by the yard having one.
        /// </summary>
        public List<string> Zones()
        {
            var output = new List<string>();
            foreach (var z in topology.Zones)
                foreach (var kind in z.Kinds)
                    if (!topology.Stands.Any(s => s.Zone == z.Token && s.Role == "zone" && s.Kinds.Contains(kind)))
                        output.Add(F("V8 zone: {0} has no stand for a {1}", z.Token, kind));
            foreach (var s in topology.Stands)
            {
                var zr = world.Zone(s.Zone);
                foreach (var kind in s.Kinds)
                {
                    var inside = true;
                    foreach (var b in world.Footprint(kind, s.X, s.Z, s.HeadingDegrees, 0.0))
                        for (var i = 0; i < b.Count; i++)
                            if (!zr.Holds(b.X[i], b.Z[i])) inside = false;
                    if (!inside) output.Add(F("V8 zone: stand {0} ({1}) is not wholly inside {2}", s.Id, kind, s.Zone));
                }
            }

            return output;
        }

        /// <summary>V9: no lane cell and no stand within <see cref="WorkClearM"/> of another machine's work area, but a load station's coupled loader in its own core; the haul loop must only not enter one.</summary>
        public List<string> WorkAreas()
        {
            var output = new List<string>();
            var exempt = new HashSet<(string, string, int)>();
            foreach (var st in topology.Stations)
                if (st.Partner != null)
                    foreach (var k in RunCells(st.Core, topology.Lane(st.Lane).Cells.Count)) exempt.Add((st.Partner.Machine, st.Lane, k));
            var idx = CellIndex();
            foreach (var wa in topology.WorkAreas)
            {
                var near = new Dictionary<string, (double Gap, int Cell, double Keep)>();
                foreach (var it in idx)
                {
                    if (exempt.Contains((wa.Machine, it.Lane, it.Cell))) continue;
                    var gap = Geometry.GroupGap(it.Group.Boxes, new[] { wa.Polygon });
                    var keep = topology.Lane(it.Lane).Kind == "loop" ? 0.0 : WorkClearM;
                    if (gap < keep && (!near.TryGetValue(it.Lane, out var cur) || gap < cur.Gap)) near[it.Lane] = (gap, it.Cell, keep);
                }

                foreach (var kv in near.OrderBy(k => k.Key, StringComparer.Ordinal))
                    output.Add(F("V9 work area: {0} cell {1} is {2:0.00} m from {3}'s work area ({4:0.0} m is kept)", kv.Key, kv.Value.Cell, kv.Value.Gap, wa.Machine, kv.Value.Keep));
                foreach (var s in topology.Stands)
                    foreach (var kind in s.Kinds)
                    {
                        var gap = Geometry.GroupGap(world.Footprint(kind, s.X, s.Z, s.HeadingDegrees, 0.0), new[] { wa.Polygon });
                        if (gap < WorkClearM) output.Add(F("V9 work area: stand {0} ({1}) is {2:0.00} m from {3}'s work area ({4:0.0} m is kept)", s.Id, kind, gap, wa.Machine, WorkClearM));
                    }
            }

            return output;
        }
    }
}
