// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json.Nodes;
using DeviceChain.Sim.Traffic;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.Tasks;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    // The site topology (Data/quarry_topology.json, written by ArtSource/terrain/quarry_topology.py): the committed file read back, and the checks
    // that say it is sound, each run on the real site and on a site crafted to break it. A check whose negative control does not fail is not a check.
    // The python script makes the same crafted sites under --selftest; here they are made by editing the file's JSON. Nothing reads the topology at run time.
    public sealed class SiteTopologyTests
    {
        static SiteTopology Topology => TopologyKit.Topology;
        static QuarrySiteWorld World => TopologyKit.World;

        static void AssertClean(IEnumerable<string> defects, string what) => CollectionAssert.IsEmpty(defects.ToList(), what + ": " + string.Join("; ", defects.Take(3)));

        static void AssertDefect(IEnumerable<string> defects, string expect, string what)
        {
            var list = defects.ToList();
            Assert.IsTrue(list.Any(d => d.Contains(expect)), $"{what}: expected a defect containing \"{expect}\", got: {(list.Count == 0 ? "none" : string.Join("; ", list.Take(3)))}");
        }

        // ---- the file

        [Test]
        public void TopologyReadsAndIsFresh()
        {
            Assert.AreEqual("ArtSource/terrain/quarry_topology.py", Topology.Generator);
            Assert.GreaterOrEqual(Topology.Lanes.Count, 7, "the lanes are read: the loop, the other lane of three roads and the refuel bay's three");
            AssertClean(TopologyKit.Check(v => v.Fresh()), "the topology's source hashes are the committed files', and its loop cells lie on track 0");
            // the check can fail: a hash, and a loop cut from the road's centreline and not from the track
            AssertDefect(TopologyKit.Check(v => v.Fresh(), TopologyKit.Edit(n =>
            {
                var h = (string)n["source"]["fleet"];
                n["source"]["fleet"] = h.Substring(0, h.Length - 1) + (h.EndsWith("0") ? "1" : "0");
            })), "V7 fresh: its fleet hash", "one byte of the fleet hash changed");
            AssertDefect(TopologyKit.Check(v => v.Fresh(), TopologyKit.Edit(n =>
            {
                var cell = TopologyKit.Lane(n, "loop")["cells"][100].AsArray();
                cell[0] = (double)cell[0] + 1.0;
            })), "V7 fresh: loop cell 100", "a loop cell off the track");
        }

        [Test]
        public void SlotIsAHaulersLengthPlusTheAirKept()
        {
            Assert.AreEqual(World.HaulerLength + World.HaulerClearance, Topology.SlotM, 1e-9, "the slot every capacity is counted in");
            Assert.AreEqual(2.0, Topology.CellM, "cells are cut every 2 m");
        }

        [Test]
        public void ReaderRefusesUnknownKeysAndDanglingReferencesAndRaggedCells()
        {
            // an unknown key
            var unknown = Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n["stands"][0]["colour"] = "red"));
            StringAssert.Contains("colour", unknown.Message);
            var root = Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n["extras"] = 1));
            StringAssert.Contains("extras", root.Message);
            // a missing block
            var missing = Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n.AsObject().Remove("junctions")));
            StringAssert.Contains("junctions", missing.Message);
            // a junction member naming a lane the file does not hold
            var dangling = Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n["junctions"][0]["members"][0]["lane"] = "road/nowhere"));
            StringAssert.Contains("road/nowhere", dangling.Message);
            // a stand's lane, an exit's target and an entry's junction likewise
            Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n["stands"][0]["in"] = "access/nowhere/in"));
            Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n["exits"][0]["to"] = "access/nowhere/in"));
            // a cell array of the wrong arity: a loop cell has five numbers, any other lane's four
            var ragged = Assert.Throws<FormatException>(() => TopologyKit.Edit(n => TopologyKit.Lane(n, "loop")["cells"][5].AsArray().RemoveAt(4)));
            StringAssert.Contains("loop", ragged.Message);
            Assert.Throws<FormatException>(() => TopologyKit.Edit(n => TopologyKit.Lane(n, "road/fill-road/back")["cells"][0].AsArray().Add(0.0)));
            // a run of cells outside its lane
            Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n["junctions"][0]["members"][0]["cells"][1] = 100000));
            Assert.Throws<FormatException>(() => SiteTopologyReader.Parse(""));
        }

        [Test]
        public void LoopLaneIsCyclicAndCoversTheTrack()
        {
            var loop = Topology.Loop;
            Assert.IsTrue(loop.Cyclic, "the loop is cyclic");
            Assert.AreEqual(0, loop.Track, "and cut from track 0");
            var tr = World.Live.Tracks[0];
            var path = 0.0;
            for (var i = 1; i < tr.X.Length; i++) path += Math.Sqrt(Math.Pow(tr.X[i] - tr.X[i - 1], 2) + Math.Pow(tr.Z[i] - tr.Z[i - 1], 2));
            Assert.AreEqual(path, loop.Cells.Count * Topology.CellM, Topology.CellM, "cells x cell_m is the track's path length to within a cell");
            for (var k = 1; k < loop.Cells.Count; k++) Assert.Greater(loop.Cells[k].T0, loop.Cells[k - 1].T0, $"track time at the start of cell {k} rises");
            Assert.Less(loop.Cells[loop.Cells.Count - 1].T0, tr.Period, "over one period");
            Assert.AreEqual(0.0, loop.Cells[0].T0, 1e-9, "from the load point's departure");
            // the loop closes: the last cell ends where the first begins
            var last = loop.Cells[loop.Cells.Count - 1];
            Assert.AreEqual(0.0, Math.Sqrt(Math.Pow(last.X1 - loop.Cells[0].X0, 2) + Math.Pow(last.Z1 - loop.Cells[0].Z0, 2)), 0.05);
            foreach (var l in Topology.Lanes.Where(l => l.Kind != "loop")) Assert.IsFalse(l.Cyclic, l.Id + " is not cyclic");
        }

        /// <summary>The topology with the junctions <paramref name="drop"/> names removed, and the entries that went through them naming none (the reader refuses a name it does not hold).</summary>
        static SiteTopology WithoutJunctions(Func<JsonNode, bool> drop) => TopologyKit.Edit(n =>
        {
            var keep = new JsonArray();
            var gone = new HashSet<string>(StringComparer.Ordinal);
            foreach (var j in n["junctions"].AsArray())
            {
                if (drop(j)) gone.Add((string)j["id"]);
                else keep.Add(j.DeepClone());
            }

            n["junctions"] = keep;
            foreach (var e in n["entries"].AsArray())
                if (e["junction"] != null && gone.Contains((string)e["junction"])) e["junction"] = null;
        });

        // ---- V1, V2: the cycle

        static SiteTopology Ring(int slotsOfLoop, int coreCells, int machines) => TopologyKit.Edit(n =>
        {
            var cells = (int)Math.Ceiling(slotsOfLoop * Topology.SlotM / Topology.CellM);
            var loop = TopologyKit.Lane(n, "loop");
            var arr = new JsonArray();
            for (var k = 0; k < cells; k++) arr.Add(new JsonArray(0.0, 0.0, 2.0, 0.0, k * 0.1));
            loop["cells"] = arr;
            n["fleet"]["machines"] = machines;
            n["stations"] = new JsonArray(JsonNode.Parse(n["stations"][0].ToJsonString()));
            n["stations"][0]["core"] = new JsonArray(0, coreCells - 1);
            n["stations"][0]["buffer"] = new JsonArray(coreCells, coreCells + 6);
            n["stations"][0]["partner"] = null;
            n["junctions"] = new JsonArray();
            n["entries"] = new JsonArray();
            n["exits"] = new JsonArray();
            n["stands"] = new JsonArray();
            n["bay"] = null;
            n["exempt"] = new JsonArray();
            n["lanes"] = new JsonArray(loop.DeepClone());
        });

        [Test]
        public void P1CycleCapacityExcludesStationCores()
        {
            AssertClean(TopologyKit.Check(v => v.CycleCapacity()), "the real loop holds every machine outside its station cores");
            var nmax = Topology.Fleet.Machines;
            var loop = Topology.Loop;
            var cores = Topology.Stations.Sum(s => TopologyValidator.RunCells(s.Core, loop.Cells.Count).Sum(k => loop.Cells[k].Length));
            Assert.Greater(cores, 20.0, "the stations have cores to leave out");
            // a 6-truck loop of 7 slots with a one-slot core is short: 6 slots are free for 6 trucks, which needs 7
            AssertDefect(TopologyKit.Check(v => v.CycleCapacity(), Ring(7, 6, 6)), "V1 P1", "a 6-truck loop of 7 slots with a 1-slot core");
            // the same loop with its core counted as free capacity would pass (7 slots for 6 trucks needs 7): the cores must be left out
            AssertClean(TopologyKit.Check(v => v.CycleCapacity(), Ring(9, 6, 6)), "a loop of 9 slots with the same core is enough");
            Assert.Greater(nmax, 6, "the real fleet is larger than the crafted one");
        }

        static SiteTopology WidenFirstLoopJunction(int complementCells) => TopologyKit.Edit(n =>
        {
            var loop = TopologyKit.Lane(n, "loop");
            var cells = loop["cells"].AsArray().Count;
            var junctions = n["junctions"].AsArray();
            var j = junctions.First(x => x["members"].AsArray().Any(m => (string)m["lane"] == "loop"));
            var members = new JsonArray();
            foreach (var m in j["members"].AsArray().Where(m => (string)m["lane"] != "loop")) members.Add(m.DeepClone());
            members.Add(new JsonObject { ["lane"] = "loop", ["cells"] = new JsonArray(0, cells - 1 - complementCells) });
            j["members"] = members;
            j["room"] = new JsonArray();
            n["stations"] = new JsonArray();
            var only = (string)j["id"];
            var keep = new JsonArray(j.DeepClone());
            foreach (var e in n["entries"].AsArray())
                if (e["junction"] != null && (string)e["junction"] != only) e["junction"] = null;
            n["junctions"] = keep;
        });

        [Test]
        public void P2SpanComplementHoldsEveryoneOnTheCycle()
        {
            AssertClean(TopologyKit.Check(v => v.SpanComplement()), "every junction's span leaves room for everyone else");
            var need = Topology.Fleet.Machines - 1;
            var cellsFor = (int)Math.Ceiling(need * Topology.SlotM / Topology.CellM);
            AssertDefect(TopologyKit.Check(v => v.SpanComplement(), WidenFirstLoopJunction((int)(Topology.SlotM / Topology.CellM))), "V2 P2", "a box widened to the whole loop but one slot");
            // the boundary: a complement of exactly N-1 slots is enough, one cell shorter is not (a slot is 6.4 cells, so two cells are a slot short)
            AssertClean(TopologyKit.Check(v => v.SpanComplement(), WidenFirstLoopJunction(cellsFor)), "a complement of exactly N-1 slots");
            AssertDefect(TopologyKit.Check(v => v.SpanComplement(), WidenFirstLoopJunction(cellsFor - 2)), "V2 P2", "a complement a cell or two short of it");
        }

        // ---- V3: stands

        [Test]
        public void StandsAreOffEveryLaneSweepAndBox()
        {
            AssertClean(TopologyKit.Check(v => v.Stands()), "every stand keeps 1.5 m of air and is driven through");
            Assert.GreaterOrEqual(Topology.Stands.Count, 2, "there are stands: the refuel queue and the bay");
            foreach (var s in Topology.Stands) Assert.GreaterOrEqual(s.ClearanceM, TopologyValidator.StandClearM, s.Id + " records the air it keeps");
            // a stand on a loop cell
            AssertDefect(TopologyKit.Check(v => v.Stands(), TopologyKit.Edit(n =>
            {
                var c = TopologyKit.Lane(n, "loop")["cells"][60].AsArray();
                var h = new Cell((double)c[0], (double)c[1], (double)c[2], (double)c[3], 0.0).HeadingDegrees;
                n["stands"][0]["pose"] = new JsonArray(((double)c[0] + (double)c[2]) / 2.0, ((double)c[1] + (double)c[3]) / 2.0, h);
            })), "V3 stand", "a stand on a loop cell");
        }

        /// <summary>The world with one more box standing at a given distance from a stand's footprint.</summary>
        sealed class WorldWithBox : ISiteWorld
        {
            readonly ISiteWorld inner;
            readonly List<IWorldObstacle> extra;

            public WorldWithBox(ISiteWorld inner, string stand, string kind, double air)
            {
                this.inner = inner;
                var s = Topology.Stands.First(x => x.Id == stand);
                // a tall thin box whose near face is `air` metres from the footprint's side: the footprint's half width and the box's own
                var fp = inner.Footprint(kind, s.X, s.Z, s.HeadingDegrees, 0.0);
                var h = s.HeadingDegrees * Math.PI / 180.0;
                double rx = Math.Cos(h), rz = -Math.Sin(h);
                var side = fp.SelectMany(p => Enumerable.Range(0, p.Count).Select(i => p.X[i] * rx + p.Z[i] * rz)).Max() - (s.X * rx + s.Z * rz);
                var d = side + air + 0.5;
                extra = new List<IWorldObstacle> { new BoxOutline("test-box", s.X + rx * d, s.Z + rz * d, 0.5, 2.0, s.HeadingDegrees) };
            }

            public IReadOnlyList<Polygon> Footprint(string kind, double x, double z, double headingDegrees, double boom) => inner.Footprint(kind, x, z, headingDegrees, boom);
            public double HaulerLength => inner.HaulerLength;
            public double HaulerClearance => inner.HaulerClearance;
            public double TravelReach => inner.TravelReach;
            public double MaxGradePercent => inner.MaxGradePercent;
            public FleetView Live => inner.Live;
            public FleetView Preview => inner.Preview;
            public IReadOnlyList<IWorldObstacle> Obstacles => inner.Obstacles.Concat(extra).ToList();
            public GradeReading SustainedGrade(IReadOnlyList<double> xs, IReadOnlyList<double> zs) => inner.SustainedGrade(xs, zs);
            public ZoneShape Zone(string token) => inner.Zone(token);
            public SourceHashes Hashes => inner.Hashes;
        }

        /// <summary>A box of known size: its outline is exact, so a distance to it is one the test can state.</summary>
        sealed class BoxOutline : IWorldObstacle
        {
            readonly double cx, cz, hx, hz, c, s;

            public BoxOutline(string name, double cx, double cz, double halfX, double halfZ, double headingDegrees)
            {
                Name = name;
                this.cx = cx;
                this.cz = cz;
                hx = halfX;
                hz = halfZ;
                var h = headingDegrees * Math.PI / 180.0;
                c = Math.Cos(h);
                s = Math.Sin(h);
            }

            public string Name { get; }
            public double CenterX => cx;
            public double CenterZ => cz;
            public double Reach => 30.0;

            public double Distance(double x, double z)
            {
                double dx = x - cx, dz = z - cz;
                double lx = Math.Abs(dx * c - dz * s) - hx, lz = Math.Abs(dx * s + dz * c) - hz;
                return lx <= 0 && lz <= 0 ? Math.Max(lx, lz) : Math.Sqrt(Math.Max(lx, 0) * Math.Max(lx, 0) + Math.Max(lz, 0) * Math.Max(lz, 0));
            }

            public double Gap(IReadOnlyList<Polygon> footprint)
            {
                var best = double.MaxValue;
                foreach (var p in footprint)
                    for (var i = 0; i < p.Count; i++)
                    {
                        int j = (i + 1) % p.Count;
                        double dx = p.X[j] - p.X[i], dz = p.Z[j] - p.Z[i];
                        var len = Math.Sqrt(dx * dx + dz * dz);
                        var n = Math.Max(1, (int)Math.Ceiling(len / 0.05));
                        for (var k = 0; k <= n; k++) best = Math.Min(best, Distance(p.X[i] + dx * k / n, p.Z[i] + dz * k / n));
                    }

                return best;
            }
        }

        [Test]
        public void AStandsAirIsHeldAtItsBoundary()
        {
            // an outline 1.4 m from a stand's footprint is inside the 1.5 m kept, and one 1.6 m off is not: the threshold is where it says
            var stand = Topology.Stands.First(s => s.Kinds.Contains("Hauler")).Id;
            var near = new TopologyValidator(Topology, new WorldWithBox(World, stand, "Hauler", 1.4)).Stands();
            AssertDefect(near, "test-box", "an outline 1.4 m from a stand");
            var far = new TopologyValidator(Topology, new WorldWithBox(World, stand, "Hauler", 1.6)).Stands();
            Assert.IsFalse(far.Any(d => d.Contains("test-box")), "an outline 1.6 m from it is clear: " + string.Join("; ", far.Where(d => d.Contains("test-box"))));
        }

        [Test]
        public void NoStandIsACulDeSac()
        {
            Assert.IsNotNull(Topology.Bay, "the site has a refuel bay");
            AssertClean(TopologyKit.Check(v => v.Stands()).Where(d => d.Contains("cul-de-sac")), "no stand's in and out lanes conflict beyond the machine's length");
            // the bay left back the way it came: out is in reversed
            AssertDefect(TopologyKit.Check(v => v.Stands(), TopologyKit.Edit(n =>
            {
                var inCells = TopologyKit.Lane(n, Topology.Bay.In)["cells"].AsArray();
                var reversed = new JsonArray();
                for (var k = inCells.Count - 1; k >= 0; k--) reversed.Add(new JsonArray((double)inCells[k][2], (double)inCells[k][3], (double)inCells[k][0], (double)inCells[k][1]));
                TopologyKit.Lane(n, Topology.Bay.Out)["cells"] = reversed;
                n["junctions"] = new JsonArray();
                foreach (var e in n["entries"].AsArray()) e["junction"] = null;
            })), "V3 cul-de-sac", "the bay with out = the reverse of in");
        }

        // ---- V4: stations

        [Test]
        public void StationsAreSafeAtEveryHeadwayAtOrAboveH()
        {
            AssertClean(TopologyKit.Check(v => v.Stations()), "both stations are safe at every headway from the design spacing, and their records match");
            var fleet = World.Live;
            var spacing = fleet.Tracks[0].Period / fleet.Machines.Count(m => m.Track == 0);
            Assert.AreEqual(2, Topology.Stations.Count, "the load point and the dump pad");
            foreach (var st in Topology.Stations)
            {
                Assert.AreEqual(spacing, st.HeadwaySeconds, 0.001, st.Id + " headway is the fleet's spacing");
                Assert.GreaterOrEqual(st.MinGapM, 0.0, st.Id + " keeps trucks apart at every headway from H");
                Assert.GreaterOrEqual(new TopologyValidator(Topology, World).Slots(Topology.Loop.Cells.Skip(st.Buffer.A).Take(st.Buffer.B - st.Buffer.A + 1).Sum(c => c.Length)), st.Capacity, st.Id + " buffer holds its capacity in slots");
            }

            var load = Topology.Stations.First(s => s.Id == "load");
            Assert.IsNotNull(load.Partner, "the load point is coupled to its loader");
            Assert.GreaterOrEqual(load.Partner.MinGapM, 0.0, "which clears its truck");
            Assert.IsTrue(load.RideThrough, "and the station admits trucks without loading while that loader is away");
            Assert.AreEqual(2, Topology.Stations.First(s => s.Id == "pad").Capacity, "two trucks at the pad");
            // a headway under the fleet's, a buffer of one slot for a capacity of two
            AssertDefect(TopologyKit.Check(v => v.Stations(), TopologyKit.Edit(n => n["stations"][0]["headway_s"] = (double)n["stations"][0]["headway_s"] * 0.9)), "its headway", "a headway 0.9 of the fleet's");
            AssertDefect(TopologyKit.Check(v => v.Stations(), TopologyKit.Edit(n =>
            {
                var pad = n["stations"].AsArray().First(s => (string)s["id"] == "pad");
                pad["buffer"] = new JsonArray((int)pad["buffer"][0], (int)pad["buffer"][0] + 6);
            })), "exit buffer holds", "a buffer of one slot for a capacity of two");
            // the record is the measurement: a nearest approach that is not what the frames give is refused
            AssertDefect(TopologyKit.Check(v => v.Stations(), TopologyKit.Edit(n => n["stations"][0]["min_gap_m"] = 99.0)), "it records a nearest approach", "a record that is not the measurement");
        }

        // ---- V5: boxes

        [Test]
        public void EveryConflictIsInsideOneBox()
        {
            AssertClean(TopologyKit.Check(v => v.Boxes()), "every conflict between two lanes is held by a box, a diverge or a stand's own way through");
            Assert.GreaterOrEqual(Topology.Junctions.Count, 3, "there are boxes");
            AssertDefect(TopologyKit.Check(v => v.Boxes(), WithoutJunctions(j => ((string)j["id"]).Contains("ramp-top"))), "V5 conflict", "the ramp-top box deleted");
            // a conflict between a loop cell and an access lane is held too: delete a stand's merge and its out lane conflicts with the loop
            var merge = Topology.Junctions.First(j => j.Id.StartsWith("merge-")).Id;
            AssertDefect(TopologyKit.Check(v => v.Boxes(), WithoutJunctions(j => (string)j["id"] == merge)), "V5 conflict", "a stand's merge deleted");
        }

        [Test]
        public void NoBoxReachesBackOverItsApproach()
        {
            AssertClean(TopologyKit.Check(v => v.Boxes()).Where(d => d.Contains("approach")), "no box holds its own approach cell");
            foreach (var j in Topology.Junctions)
                foreach (var ap in j.Approach)
                {
                    var lane = Topology.Lane(ap.Lane);
                    var next = lane.Cyclic ? (ap.Cell + 1) % lane.Cells.Count : ap.Cell + 1;
                    Assert.IsTrue(j.Members.Any(m => m.Lane == ap.Lane && TopologyValidator.RunCells(m.Cells, lane.Cells.Count).Contains(next)), $"{j.Id}: the cell after the approach on {ap.Lane} is where the box begins");
                }

            // the seed-5 shape: a box whose members include the cell its own requester waits on
            var merge = Topology.Junctions.First(j => j.Approach.Any(a => a.Lane == "loop"));
            var ap0 = merge.Approach.First(a => a.Lane == "loop");
            AssertDefect(TopologyKit.Check(v => v.Boxes(), TopologyKit.Edit(n =>
            {
                var j = n["junctions"].AsArray().First(x => (string)x["id"] == merge.Id);
                foreach (var m in j["members"].AsArray())
                    if ((string)m["lane"] == "loop" && ((int)m["cells"][0] - 1 + Topology.Loop.Cells.Count) % Topology.Loop.Cells.Count == ap0.Cell) m["cells"][0] = ap0.Cell;
            })), "V5 approach", "a box that includes the cell it is approached by");
        }

        // ---- V6: lanes

        [Test]
        public void EveryLaneIsDrivable()
        {
            AssertClean(TopologyKit.Check(v => v.Drivable()), "every lane is within the grade limit and clear of every outline by the reach a hauler keeps");
            // a lane cell moved onto a light tower
            var lane5 = Topology.Lane("road/fill-road/back").Cells[5];
            var tower = World.Obstacles.Where(o => o.Name == "light_tower").OrderBy(o => Math.Sqrt(Math.Pow(o.CenterX - lane5.X0, 2) + Math.Pow(o.CenterZ - lane5.Z0, 2))).First();
            AssertDefect(TopologyKit.Check(v => v.Drivable(), TopologyKit.Edit(n =>
            {
                var c = TopologyKit.Lane(n, "road/fill-road/back")["cells"][5].AsArray();
                c[0] = tower.CenterX;
                c[1] = tower.CenterZ;
            })), "V6 reach: road/fill-road/back cell 5 comes", "a lane cell moved onto a light tower");
            // and a climb: a lane run straight up the pit's north wall is a grade defect
            AssertDefect(TopologyKit.Check(v => v.Drivable(), TopologyKit.Edit(n =>
            {
                var cells = TopologyKit.Lane(n, "road/fill-road/back")["cells"].AsArray();
                for (var k = 0; k < cells.Count; k++)
                {
                    cells[k][0] = 0.0;
                    cells[k][1] = 56.0 + 2.0 * k;
                    cells[k][2] = 0.0;
                    cells[k][3] = 58.0 + 2.0 * k;
                }
            })), "V6 grade", "a lane run straight up the pit's north wall");
        }

        [Test]
        public void EveryExemptionIsNeededByWhatItNames()
        {
            // an exemption nothing exercises is free to delete: without it the lane or stand must be short of what it is exempt from
            var tested = 0;
            foreach (var e in Topology.Exempt.Where(x => x.Obstacle != null))
            {
                var without = TopologyKit.Edit(n =>
                {
                    var keep = new JsonArray();
                    foreach (var x in n["exempt"].AsArray())
                        if (!((string)x["to"] == e.To && x["obstacle"] != null && (string)x["obstacle"] == e.Obstacle)) keep.Add(x.DeepClone());
                    n["exempt"] = keep;
                });
                var v = new TopologyValidator(without, World);
                var defects = v.Drivable().Concat(v.Stands()).ToList();
                Assert.IsTrue(defects.Any(d => d.Contains(e.To) && d.Contains(e.Obstacle)), $"{e.To} is exempt from {e.Obstacle} but comes no nearer it than it is held to");
                tested++;
            }

            Assert.Greater(tested, 0, "the exemptions are read");
        }

        // ---- V8, V9: zones and work areas

        [Test]
        public void EveryZoneHasAStandForEachKind()
        {
            AssertClean(TopologyKit.Check(v => v.Zones()), "every zone has a stand for each kind it accepts, wholly inside it");
            Assert.GreaterOrEqual(Topology.Zones.Count, 3, "the zones are listed");
            foreach (var z in Topology.Zones)
                foreach (var kind in z.Kinds)
                    Assert.IsTrue(Topology.Stands.Any(s => s.Zone == z.Token && s.Kinds.Contains(kind)), $"{z.Token} has a stand for a {kind}");
            // a zone asked for a stand for a kind it has none of
            AssertDefect(TopologyKit.Check(v => v.Zones(), TopologyKit.Edit(n => n["zones"][0]["kinds"] = new JsonArray("Hauler"))), "V8 zone: sp-zone-cut has no stand", "a zone with no stand for a hauler");
            // and one for a kind the zone's stands do have is satisfied: the bay's two stands are hauler places in the yard
            AssertClean(TopologyKit.Check(v => v.Zones(), TopologyKit.Edit(n => n["zones"][2]["kinds"] = new JsonArray("Hauler"))), "a zone with a stand for the kind");
            // a stand outside the zone it names is refused
            AssertDefect(TopologyKit.Check(v => v.Zones(), TopologyKit.Edit(n => n["stands"][0]["zone"] = "sp-zone-cut")), "not wholly inside", "a stand in the wrong zone");
        }

        [Test]
        public void NoZoneHasAStandYetAndTheTopologySaysSo()
        {
            // The room the choreography leaves to stand in (quarry_fleet.py stand_room) is not a way in and out: with the clearances a hauler keeps
            // from outlines and from every machine's work area, no lane reaches a stand in the cut, the fill or the yard and leaves it without
            // its two lanes running over each other. The topology therefore lists every zone as accepting no kind, and holds the refuel
            // bay's two stands only. This test is the record: it fails when a stand is added, so that the zones' kinds are decided then.
            CollectionAssert.AreEquivalent(new[] { "sp-zone-cut", "sp-zone-fill", "sp-zone-yard" }, Topology.Zones.Select(z => z.Token).ToArray());
            foreach (var z in Topology.Zones) CollectionAssert.IsEmpty(z.Kinds, z.Token + " accepts no kind until it has a stand");
            CollectionAssert.AreEquivalent(new[] { Topology.Bay.Queue, Topology.Bay.BayStand }, Topology.Stands.Select(s => s.Id).ToArray(), "the stands are the bay's");
        }

        [Test]
        public void LanesAndStandsAvoidWorkAreas()
        {
            AssertClean(TopologyKit.Check(v => v.WorkAreas()), "no lane and no stand is within 1.5 m of another machine's work area");
            Assert.AreEqual(12, Topology.WorkAreas.Count, "every machine off the loop has an area");
            // an access lane through SP-DZ-0002's rip area
            var rip = Topology.WorkAreas.First(w => w.Machine == "SP-DZ-0002");
            AssertDefect(TopologyKit.Check(v => v.WorkAreas(), TopologyKit.Edit(n =>
            {
                var cx = rip.Polygon.X.Average();
                var cz = rip.Polygon.Z.Average();
                var lane = n["lanes"].AsArray().First(l => ((string)l["id"]).StartsWith("access/"));
                lane["cells"][3][0] = cx;
                lane["cells"][3][1] = cz;
            })), "V9 work area", "an access lane through SP-DZ-0002's rip area");
            // the partner exemption is for the coupled loader alone: another loader's area at the load point is held to
            var load = Topology.Stations.First(s => s.Id == "load");
            Assert.IsNotNull(load.Partner);
            AssertDefect(TopologyKit.Check(v => v.WorkAreas(), TopologyKit.Edit(n =>
            {
                n["stations"].AsArray().First(s => (string)s["id"] == "load")["partner"]["machine"] = "SP-LD-0002";
            })), "SP-LD-0001", "the load core's exemption applied to a loader that is not its partner");
        }

        [Test]
        public void TheWholeTopologyIsCleanUnderEveryCheck()
        {
            AssertClean(new TopologyValidator(Topology, World).All(), "the committed topology");
        }
    }
}
