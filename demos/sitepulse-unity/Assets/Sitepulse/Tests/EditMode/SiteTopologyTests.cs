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

        static void AssertDefect(IEnumerable<string> defects, Func<string, bool> expect, string what)
        {
            var list = defects.ToList();
            Assert.IsTrue(list.Any(expect), $"{what}: expected a matching defect, got: {(list.Count == 0 ? "none" : string.Join("; ", list.Take(3)))}");
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
        public void TheFileStatesItsThresholdsAndTheChecksReadThemFromIt()
        {
            var r = Topology.Rules;
            Assert.AreEqual(1.0, r.ConflictHaulM, "a metre of air between two haul lanes");
            Assert.AreEqual(0.3, r.ConflictAccessM, "0.3 m for a pair with a lane of the bay's");
            Assert.AreEqual(0.0, r.ConflictSameRouteM, "a lane or a stand's way through it conflicts with itself only where it overlaps");
            Assert.AreEqual(12, r.DivergeWindow);
            Assert.AreEqual(40, r.DivergeMax);
            foreach (var l in Topology.Lanes) Assert.AreEqual(Topology.SlotM, l.NeighbourSpanM, 1e-9, l.Id + ": cells within a slot of each other along the lane are a machine and its follower");
            Assert.AreEqual(1.0, r.ConflictM("loop", "road"));
            Assert.AreEqual(1.0, r.ConflictM("road", "road"));
            Assert.AreEqual(0.3, r.ConflictM("loop", "access"));
            Assert.AreEqual(0.3, r.ConflictM("access", "access"));
        }

        [Test]
        public void ReaderRefusesUnknownKeysAndDanglingReferencesAndRaggedCells()
        {
            // an unknown key
            var unknown = Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n["stands"][0]["colour"] = "red"));
            StringAssert.Contains("colour", unknown.Message);
            var root = Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n["extras"] = 1));
            StringAssert.Contains("extras", root.Message);
            var rule = Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n["rules"]["colour"] = 1));
            StringAssert.Contains("colour", rule.Message);
            // a missing block, rule or span
            var missing = Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n.AsObject().Remove("junctions")));
            StringAssert.Contains("junctions", missing.Message);
            Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n["rules"].AsObject().Remove("conflict_haul_m")));
            Assert.Throws<FormatException>(() => TopologyKit.Edit(n => TopologyKit.Lane(n, "loop").AsObject().Remove("neighbour_span_m")));
            // a junction member naming a lane the file does not hold
            var dangling = Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n["junctions"][0]["members"][0]["lane"] = "road/nowhere"));
            StringAssert.Contains("road/nowhere", dangling.Message);
            // a stand's lane, an exit's target and an entry's junction likewise
            Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n["stands"][0]["in"] = "access/nowhere/in"));
            Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n["exits"][0]["to"] = "access/nowhere/in"));
            // a stand's role is one of two
            var role = Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n["stands"][0]["role"] = "parking"));
            StringAssert.Contains("parking", role.Message);
            // a cell array of the wrong arity: a loop cell has five numbers, any other lane's four
            var ragged = Assert.Throws<FormatException>(() => TopologyKit.Edit(n => TopologyKit.Lane(n, "loop")["cells"][5].AsArray().RemoveAt(4)));
            StringAssert.Contains("loop", ragged.Message);
            Assert.Throws<FormatException>(() => TopologyKit.Edit(n => TopologyKit.Lane(n, "road/fill-road/back")["cells"][0].AsArray().Add(0.0)));
            // a run of cells outside its lane
            Assert.Throws<FormatException>(() => TopologyKit.Edit(n => n["junctions"][0]["members"][0]["cells"][1] = 100000));
            // a loop lane that is not cyclic would make the capacity checks pass for nothing
            var open = Assert.Throws<FormatException>(() => TopologyKit.Edit(n => TopologyKit.Lane(n, "loop")["cyclic"] = false));
            StringAssert.Contains("not cyclic", open.Message);
            Assert.Throws<FormatException>(() => SiteTopologyReader.Parse(""));
        }

        [Test]
        public void TheTestKitRefusesAMachineKindTheQuarryHasNone()
        {
            Assert.Throws<ArgumentException>(() => World.Footprint("Crane", 0.0, 0.0, 0.0, 0.0));
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

        /// <summary>The topology with one lane replaced by a single cell, and every box member and room that named the lane dropped (the reader refuses a run outside its lane).</summary>
        static SiteTopology WithSingleCellLane(string lane, double x0, double z0, double x1, double z1) => TopologyKit.Edit(n =>
        {
            TopologyKit.Lane(n, lane)["cells"] = new JsonArray(new JsonArray(x0, z0, x1, z1));
            foreach (var j in n["junctions"].AsArray())
                foreach (var key in new[] { "members", "room" })
                {
                    var keep = new JsonArray();
                    foreach (var m in j[key].AsArray())
                        if ((string)m["lane"] != lane) keep.Add(m.DeepClone());
                    j[key] = keep;
                }
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

        [Test]
        public void P1HoldsOnEveryDirectedCycleNotOnlyTheLoop()
        {
            var cycles = new TopologyValidator(Topology, World).DirectedCycles();
            Assert.AreEqual(2, cycles.Count, "the loop, and the detour through the refuel bay");
            Assert.IsTrue(cycles.Any(c => c.Name == "loop"));
            Assert.IsTrue(cycles.Any(c => c.Name.StartsWith("loop via access/bay/in")), "the bay's detour is a cycle of its own");
            // the detour round the loop that skips most of it, with a fleet the loop itself still holds
            AssertDefect(TopologyKit.Check(v => v.CycleCapacity(), TopologyKit.Edit(n =>
            {
                n["fleet"]["machines"] = 40;
                n["entries"][0]["cell"] = 1;
            })), "V1 P1: loop via access/bay/in", "the bay's detour short for the fleet");
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

        [Test]
        public void P2IsRunOnTheBoxWhereTheLoopCrossesItself()
        {
            var rows = new TopologyValidator(Topology, World).SpanComplements();
            var bottom = Topology.Junctions.First(j => j.Id.Contains("ramp-bottom"));
            Assert.AreEqual(2, bottom.Members.Count(m => m.Lane == "loop"), "the loop is in that box twice: the stretch leaving the load and the stretch queueing for it");
            var row = rows.First(r => r.Junction == bottom.Id);
            Assert.GreaterOrEqual(row.Free, Topology.Fleet.Machines - 1, "its complement holds everyone else");
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

        [Test]
        public void AStandsClearanceIsTheSameNumberInBothLanguages()
        {
            // the python side records the nearest each stand comes to anything it keeps clear of (its exact distance to an outline); this side measures it
            // again by sampling the footprint's edges against the outline, and the two agree to a centimetre
            var v = new TopologyValidator(Topology, World);
            foreach (var s in Topology.Stands)
            {
                var measured = s.Kinds.Min(kind => v.StandGaps(s, kind).Values.Min(g => g.Gap));
                Assert.AreEqual(s.ClearanceM, measured, 0.01, s.Id + ": the recorded clearance and the measured one");
            }
        }

        [Test]
        public void AStandThatListsNoKindIsRefused()
        {
            AssertDefect(TopologyKit.Check(v => v.Stands(), TopologyKit.Edit(n => n["stands"][0]["kinds"] = new JsonArray())), "V3 stand refuel-queue: it lists no kind", "a stand with no kind");
        }

        [Test]
        public void AStandOnATrackEitherFleetPlaysIsRefused()
        {
            // on a track the live fleet plays
            var tr = World.Live.Tracks[3];
            AssertDefect(TopologyKit.Check(v => v.Stands(), TopologyKit.Edit(n => n["stands"][0]["pose"] = new JsonArray(tr.X[0], tr.Z[0], tr.Heading[0]))),
                "from sweep live fleet track 3", "a stand on the live fleet's third track");
            // the scripted refuel visit's track is played by the preview fleet alone: the table exempts the queue and the bay from it, and without that the preview fleet's sweep is found
            AssertDefect(TopologyKit.Check(v => v.Stands(), TopologyKit.Edit(n =>
            {
                var keep = new JsonArray();
                foreach (var e in n["exempt"].AsArray())
                    if (e["machine"] == null) keep.Add(e.DeepClone());
                n["exempt"] = keep;
            })), "from sweep preview fleet track 1 (SP-HL-0006)", "the visit's track not exempted");
        }

        /// <summary>The world with one more box standing at a given distance from a stand's footprint.</summary>
        sealed class WorldWithBox : ISiteWorld
        {
            readonly ISiteWorld inner;
            readonly List<IWorldObstacle> extra;

            /// <summary>A box whose near face is <paramref name="air"/> metres from the footprint's side, or (<paramref name="diagonal"/>) whose near corner is that far from the footprint's far corner along the diagonal.</summary>
            public WorldWithBox(ISiteWorld inner, string stand, string kind, double air, bool diagonal = false)
            {
                this.inner = inner;
                var s = Topology.Stands.First(x => x.Id == stand);
                var fp = inner.Footprint(kind, s.X, s.Z, s.HeadingDegrees, 0.0);
                if (diagonal)
                {
                    // the stand faces north: its far corner is the greatest x of the box with the greatest z
                    var top = fp.OrderByDescending(p => Enumerable.Range(0, p.Count).Max(i => p.Z[i])).First();
                    var cx = Enumerable.Range(0, top.Count).Max(i => top.X[i]);
                    var cz = Enumerable.Range(0, top.Count).Max(i => top.Z[i]);
                    var off = 0.5 + air / Math.Sqrt(2.0);
                    extra = new List<IWorldObstacle> { new BoxOutline("test-box", cx + off, cz + off, 0.5, 0.5, 0.0) };
                    return;
                }

                // a tall thin box whose near face is `air` metres from the footprint's side: the footprint's half width and the box's own
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
        public void AnOutlineFacingAStandsCornerIsMeasuredByDistanceNotByEdgeNormal()
        {
            // a box standing corner to corner with the bay's footprint, 1.4 m and 1.6 m off along the diagonal: the gap along an edge normal would read
            // 1.0 m and 1.13 m for those and refuse both; the distance is what the python side measures too
            var stand = Topology.Bay.BayStand;
            var near = new TopologyValidator(Topology, new WorldWithBox(World, stand, "Hauler", 1.4, true)).Stands();
            AssertDefect(near, "V3 stand refuel-bay (Hauler): 1.40 m from obstacle test-box", "an outline 1.4 m off a stand's corner");
            var far = new TopologyValidator(Topology, new WorldWithBox(World, stand, "Hauler", 1.6, true)).Stands();
            Assert.IsFalse(far.Any(d => d.Contains("test-box")), "an outline 1.6 m off the corner is clear: " + string.Join("; ", far.Where(d => d.Contains("test-box"))));
        }

        [Test]
        public void NoStandIsACulDeSac()
        {
            Assert.IsNotNull(Topology.Bay, "the site has a refuel bay");
            AssertClean(TopologyKit.Check(v => v.Stands()).Where(d => d.Contains("cul-de-sac")), "no stand's in and out lanes overlap along the whole way through");
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

        /// <summary>The bay's out lane laid over its in lane far from either stand: out cell 30 copied onto in cell 1 (the two cross 74 m along the way through).</summary>
        static SiteTopology OutOverIn() => TopologyKit.Edit(n =>
        {
            var from = TopologyKit.Lane(n, Topology.Bay.Out)["cells"][30].AsArray();
            TopologyKit.Lane(n, Topology.Bay.In)["cells"][1] = new JsonArray((double)from[0], (double)from[1], (double)from[2], (double)from[3]);
        });

        [Test]
        public void ALaneCrossingItsOwnWayThroughFarFromTheStandIsACulDeSacAndAConflict()
        {
            AssertDefect(TopologyKit.Check(v => v.Stands(), OutOverIn()), "V3 cul-de-sac", "the out lane crossing the in lane far from the stand");
            AssertDefect(TopologyKit.Check(v => v.Boxes(), OutOverIn()), d => d.StartsWith("V5 conflict: access/bay/in cell 1 and access/bay/out cell"), "and no box holds the crossing");
        }

        [Test]
        public void TheBayIsKnownAsOneWayThroughOrItsLanesConflictNearTheStand()
        {
            // without the bay's chain the in and out lanes, which lie within a slot of each other along the way through, are two lanes that overlap
            AssertDefect(TopologyKit.Check(v => v.Boxes(), TopologyKit.Edit(n => n["bay"] = null)), d => d.StartsWith("V5 conflict: access/bay/in cell"), "the bay not known as a way through");
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

        [Test]
        public void TheNearestApproachIsTheOneOverEveryHeadwayNotTheOneAtTheDesignSpacing()
        {
            // the pad's trucks come nearest at a spacing longer than the design one: a record made as if only the design spacing were swept is refused
            var pad = Topology.Stations.First(s => s.Id == "pad");
            var v = new TopologyValidator(Topology, World);
            v.StationTimes(pad, out var tIn, out _, out var tOut);
            var atDesign = v.StationMinGap(tIn, tOut, pad.HeadwaySeconds, 0.0, out _, out _);
            var swept = v.StationMinGap(tIn, tOut, pad.HeadwaySeconds, pad.WindowSeconds, out var atHeadway, out _);
            Assert.Less(swept, atDesign - 0.01, "sweeping the window finds a nearer approach than the design spacing alone");
            Assert.Greater(atHeadway, pad.HeadwaySeconds, "at a spacing longer than the design one");
            AssertDefect(TopologyKit.Check(x => x.Stations(), TopologyKit.Edit(n => n["stations"].AsArray().First(s => (string)s["id"] == "pad")["min_gap_m"] = atDesign)),
                "V4 station pad: it records a nearest approach", "a record made at the design spacing alone");
        }

        // ---- V5: boxes

        [Test]
        public void EveryConflictIsInsideOneBox()
        {
            AssertClean(TopologyKit.Check(v => v.Boxes()), "every conflict between two cells is held by a box, a diverge, a station or a stand's own way through");
            Assert.GreaterOrEqual(Topology.Junctions.Count, 5, "there are boxes");
            AssertDefect(TopologyKit.Check(v => v.Boxes(), WithoutJunctions(j => ((string)j["id"]).Contains("ramp-top"))), "V5 conflict", "the ramp-top box deleted");
        }

        [Test]
        public void ABayLaneIsHeldByItsMergeBoxAndNotByTheLoopAndRoadCellsAlone()
        {
            // the merge box holds the bay's out lane, the loop and the yard road together; take the out lane's cells out of it and what is left unheld is the out lane's
            // conflicts with the others (the loop and the road are still in the box together)
            var merge = Topology.Junctions.First(j => j.Id.StartsWith("merge-")).Id;
            var without = TopologyKit.Edit(n =>
            {
                var j = n["junctions"].AsArray().First(x => (string)x["id"] == merge);
                var keep = new JsonArray();
                foreach (var m in j["members"].AsArray())
                    if (!((string)m["lane"]).StartsWith("access/")) keep.Add(m.DeepClone());
                j["members"] = keep;
            });
            var defects = TopologyKit.Check(v => v.Boxes(), without).Where(d => d.StartsWith("V5 conflict")).ToList();
            Assert.IsTrue(defects.Any(d => d.Contains("access/bay/out")), "the out lane's conflicts are found: " + string.Join("; ", defects.Take(3)));
            Assert.IsFalse(defects.Any(d => !d.Contains("access/bay/")), "and only the out lane's: " + string.Join("; ", defects.Where(d => !d.Contains("access/bay/")).Take(3)));
        }

        [Test]
        public void TheLoopCrossingItselfAtTheRampBottomIsHeldByABox()
        {
            // the loop leaving the load and the loop queueing for it overlap by up to five metres near (-22, 15), 160 m apart along the lane: a box holds them
            AssertDefect(TopologyKit.Check(v => v.Boxes(), WithoutJunctions(j => ((string)j["id"]).Contains("ramp-bottom"))),
                d => d.StartsWith("V5 conflict: loop cell") && d.Contains("the same lane"), "the ramp-bottom box deleted");
        }

        [Test]
        public void ALaneThatFoldsBackOverItselfWithNoBoxIsAConflict()
        {
            AssertDefect(TopologyKit.Check(v => v.Boxes(), TopologyKit.Edit(n =>
            {
                var cells = new JsonArray();
                for (var i = 0; i < 10; i++) cells.Add(new JsonArray(2.0 * i, 0.0, 2.0 * i + 2.0, 0.0));
                for (var i = 0; i < 10; i++) cells.Add(new JsonArray(20.0 - 2.0 * i, 0.4, 18.0 - 2.0 * i, 0.4));
                TopologyKit.Lane(n, "road/fill-road/back")["cells"] = cells;
            })), d => d.StartsWith("V5 conflict: road/fill-road/back cell") && d.Contains("and road/fill-road/back cell") && d.Contains("the same lane"), "a lane driving out and back over itself");
        }

        [Test]
        public void TheDumpPadsTrucksOverlapEachOtherAndOnlyTheStationHoldsThat()
        {
            AssertDefect(TopologyKit.Check(v => v.Boxes(), TopologyKit.Edit(n =>
            {
                var keep = new JsonArray();
                foreach (var s in n["stations"].AsArray())
                    if ((string)s["id"] != "pad") keep.Add(s.DeepClone());
                n["stations"] = keep;
            })), d => d.StartsWith("V5 conflict: loop cell") && d.Contains("the same lane"), "the pad station removed");
        }

        /// <summary>The nearest two cells of the classes come that do not conflict and no box holds, as the python report prints them.</summary>
        static double NearestClear(TopologyValidator v, Func<Lane, Lane, bool> klass, double threshold)
        {
            var held = new Dictionary<(string, int), HashSet<string>>();
            foreach (var j in Topology.Junctions)
                foreach (var m in j.Members)
                    foreach (var k in TopologyValidator.RunCells(m.Cells, Topology.Lane(m.Lane).Cells.Count))
                    {
                        if (!held.TryGetValue((m.Lane, k), out var set)) held[(m.Lane, k)] = set = new HashSet<string>();
                        set.Add(j.Id);
                    }

            var routes = v.RouteIndex();
            var best = double.MaxValue;
            foreach (var c in v.Conflicts(3.0))
            {
                var a = Topology.Lane(c.LaneA);
                var b = Topology.Lane(c.LaneB);
                if (a.Id == b.Id || !klass(a, b) || c.Gap < threshold) continue;
                if (routes.TryGetValue((c.LaneA, c.CellA), out var ra) && routes.TryGetValue((c.LaneB, c.CellB), out var rb) && ra.Any(x => rb.Any(y => x.Chain == y.Chain))) continue;
                if (held.TryGetValue((c.LaneA, c.CellA), out var sa) && held.TryGetValue((c.LaneB, c.CellB), out var sb) && sa.Overlaps(sb)) continue;
                best = Math.Min(best, c.Gap);
            }

            return best;
        }

        [Test]
        public void TheHaulThresholdIsReadFromTheFileAndSitsJustUnderTheNearestPairThatDoesNotConflict()
        {
            var v = new TopologyValidator(Topology, World);
            var haul = NearestClear(v, (a, b) => (a.Kind == "loop" || a.Kind == "road") && (b.Kind == "loop" || b.Kind == "road"), Topology.Rules.ConflictHaulM);
            // the python report prints the same pair: 1.004 m against the metre (the loop's cell 217 and the first cell of the fill road's return lane)
            Assert.AreEqual(1.004, haul, 0.005, "the nearest haul pair that does not conflict");
            Assert.Greater(haul, Topology.Rules.ConflictHaulM);
            AssertDefect(TopologyKit.Check(x => x.Boxes(), TopologyKit.Edit(n => n["rules"]["conflict_haul_m"] = haul + 0.01)), "V5 conflict", "the threshold raised past it");
            AssertClean(TopologyKit.Check(x => x.Boxes(), TopologyKit.Edit(n => n["rules"]["conflict_haul_m"] = haul - 0.01)), "the threshold lowered below it");
            var access = NearestClear(v, (a, b) => a.Kind == "access" || b.Kind == "access", Topology.Rules.ConflictAccessM);
            Assert.AreEqual(0.526, access, 0.005, "the nearest pair with a bay lane that does not conflict");
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

        // the cells are appended to a lane far from every other and past the cells any box lists, so nothing holds them
        static SiteTopology Hairpin(int cellsOut) => TopologyKit.Edit(n =>
        {
            var cells = TopologyKit.Lane(n, "road/fill-return/east")["cells"].AsArray();
            for (var i = 0; i < cellsOut; i++) cells.Add(new JsonArray(900.0 + 2.0 * i, 900.0, 900.0 + 2.0 * i + 2.0, 900.0));
            for (var i = 0; i < cellsOut; i++) cells.Add(new JsonArray(900.0 + 2.0 * cellsOut - 2.0 * i, 900.4, 900.0 + 2.0 * cellsOut - 2.0 * i - 2.0, 900.4));
        });

        [Test]
        public void ALaneDoublingBackOverItselfJustPastAFollowersDistanceIsAConflict()
        {
            // the overlapping cells are 14 m apart along the lane (the follower's distance is 12.8 m), nowhere near a box or a station: a check that skips
            // same-lane pairs within twice the span passes it
            AssertDefect(TopologyKit.Check(v => v.Boxes(), Hairpin(4)),
                d => d.StartsWith("V5 conflict: road/fill-return/east cell") && d.Contains("and road/fill-return/east cell") && d.Contains("the same lane 14 m apart along it"), "doubled back 14 m apart");
            // 10 m apart is a machine and its follower
            Assert.IsFalse(TopologyKit.Check(v => v.Boxes(), Hairpin(3)).Any(d => d.StartsWith("V5 conflict: road/fill-return/east cell") && d.Contains("the same lane")),
                "doubled back 10 m apart is a follower, not a conflict");
        }

        // ---- V10: nobody waits in a station core

        static SiteTopology WithHolds(string station, params string[] holds) => TopologyKit.Edit(n =>
        {
            var arr = new JsonArray();
            foreach (var h in holds) arr.Add(h);
            n["stations"].AsArray().First(s => (string)s["id"] == station)["holds"] = arr;
        });

        const string MergedBox = "oncoming-ramp-top+pad-gate+pad-south";

        [Test]
        public void NoBoxMakesATruckWaitInAStationCoreUnlessTheStationHoldsIt()
        {
            AssertClean(TopologyKit.Check(v => v.WaitingCells()), "every box with cells in a core is held by that station");
            var pad = Topology.Stations.First(s => s.Id == "pad");
            CollectionAssert.AreEquivalent(new[] { "oncoming-pad-south", MergedBox }, pad.Holds, "the pad holds the two boxes beside it");

            var none = TopologyKit.Check(v => v.WaitingCells(), WithHolds("pad")).ToList();
            AssertDefect(none, "junction oncoming-pad-south has approach cell 151 on loop inside the core of station pad", "pad-south's approach is in the pad core");
            AssertDefect(none, "has room cell 134-140 on loop inside the core of station pad", "the merged box's room is in the pad core");
            AssertDefect(TopologyKit.Check(v => v.WaitingCells(), WithHolds("pad", MergedBox)), "junction oncoming-pad-south has approach cell 151", "only the merged box held");
            AssertDefect(TopologyKit.Check(v => v.WaitingCells(), WithHolds("load")), "junction oncoming-ramp-bottom has room cell 363-366 on loop inside the core of station load", "the load core's first cells");
        }

        [Test]
        public void AHoldNothingNeedsOrNothingNamesIsRefused()
        {
            AssertDefect(TopologyKit.Check(v => v.WaitingCells(), WithHolds("load", "oncoming-ramp-bottom", "merge-bay")),
                d => d.Contains("station load holds junction merge-bay") && d.Contains("a hold nothing needs"), "a box that touches nothing of the core");
            AssertDefect(TopologyKit.Check(v => v.WaitingCells(), WithHolds("pad", "oncoming-pad-south", MergedBox, "no-such-box")),
                "station pad holds junction no-such-box, which the file does not have", "a box the file lacks");
        }

        // ---- V6: lanes

        /// <summary>The light tower with nothing else standing near it.</summary>
        static IWorldObstacle QuietTower => World.Obstacles.Where(o => o.Name == "light_tower").ElementAt(2);

        /// <summary>A point on the +x side of an obstacle whose distance from its outline is <paramref name="target"/>.</summary>
        static (double X, double Z) AtDistance(IWorldObstacle o, double target)
        {
            double lo = 0.0, hi = 80.0;
            for (var i = 0; i < 60; i++)
            {
                var mid = (lo + hi) / 2.0;
                if (o.Distance(o.CenterX + mid, o.CenterZ) < target) lo = mid;
                else hi = mid;
            }

            return (o.CenterX + hi, o.CenterZ);
        }

        [Test]
        public void EveryLaneIsDrivable()
        {
            AssertClean(TopologyKit.Check(v => v.Drivable()), "every lane is within the grade limit and every pose on it clear of every outline by the reach a hauler keeps");
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
        public void TheReachAHaulerKeepsFromAnOutlineIsHeldAtItsBoundary()
        {
            // a driving hauler keeps its half width and 0.2 m of air from an outline: 3.2 m. A lane cell 3.1 m off a tower is inside that, one 3.3 m off is not
            // (the 0.2 m of air is the part that is easy to leave out: without it the line would sit at 3.0 m)
            var tower = QuietTower;
            Assert.AreEqual(3.2, World.TravelReach, 1e-9);
            var near = AtDistance(tower, 3.1);
            AssertDefect(TopologyKit.Check(v => v.Drivable(), WithSingleCellLane("road/fill-road/back", near.X, near.Z, near.X + 1.0, near.Z)),
                "V6 reach: road/fill-road/back cell 0 comes 3.10 m from light_tower", "a lane cell 3.1 m off a light tower");
            var far = AtDistance(tower, 3.3);
            var clear = TopologyKit.Check(v => v.Drivable(), WithSingleCellLane("road/fill-road/back", far.X, far.Z, far.X + 1.0, far.Z));
            Assert.IsFalse(clear.Any(d => d.StartsWith("V6 reach: road/fill-road/back cell 0") && d.Contains("light_tower")), "a lane cell 3.3 m off it is clear: " + string.Join("; ", clear.Take(3)));
        }

        [Test]
        public void EveryPoseOnACellIsHeldToTheReachNotOnlyItsEnds()
        {
            // a long cell whose two ends are well clear of a tower and whose middle is 2.0 m from it
            var p = AtDistance(QuietTower, 2.0);
            AssertDefect(TopologyKit.Check(v => v.Drivable(), WithSingleCellLane("road/fill-road/back", p.X, p.Z + 8.0, p.X, p.Z - 8.0)),
                d => d.StartsWith("V6 reach: road/fill-road/back cell 0 comes") && d.Contains("light_tower")
                    && double.Parse(d.Substring(d.IndexOf("comes ") + 6, 4), System.Globalization.CultureInfo.InvariantCulture) is var m && m >= 1.95 && m <= 2.01,
                "the middle of a long cell");
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

        [Test]
        public void TheBaysConeExemptionNamesTheConeLineNotARadiusRoundTheBay()
        {
            // seven cones 3 m apart down x = -55.6; the exemption's spot is the line's middle and its radius the line's half length and a cone's half width
            var cones = World.Obstacles.Where(o => o.Name == "cone" && Math.Abs(o.CenterX + 55.6) < 0.01).ToList();
            Assert.AreEqual(7, cones.Count, "the cone line");
            var row = Topology.Exempt.First(e => e.To == "access/bay/out" && e.Obstacle == "cone");
            Assert.AreEqual(-55.6, row.SpotX, 1e-9);
            Assert.AreEqual(-55.0, row.SpotZ, 1e-9);
            Assert.AreEqual(9.5, row.RadiusM, 1e-9);
            foreach (var c in cones) Assert.LessOrEqual(Math.Sqrt(Math.Pow(c.CenterX - row.SpotX, 2) + Math.Pow(c.CenterZ - row.SpotZ, 2)), row.RadiusM, "the exemption covers the cone at " + c.CenterZ);
            Assert.AreEqual(7, World.Obstacles.Count(o => o.Name == "cone" && Math.Sqrt(Math.Pow(o.CenterX - row.SpotX, 2) + Math.Pow(o.CenterZ - row.SpotZ, 2)) <= row.RadiusM), "and no other cone on the site");
        }

        // ---- V8, V9: zones and work areas

        [Test]
        public void EveryZoneHasAStandForEachKind()
        {
            AssertClean(TopologyKit.Check(v => v.Zones()), "every zone has a stand for each kind it accepts, wholly inside it");
            Assert.GreaterOrEqual(Topology.Zones.Count, 3, "the zones are listed");
            foreach (var z in Topology.Zones)
                foreach (var kind in z.Kinds)
                    Assert.IsTrue(Topology.Stands.Any(s => s.Zone == z.Token && s.Role == "zone" && s.Kinds.Contains(kind)), $"{z.Token} has a zone stand for a {kind}");
            // a zone asked for a stand for a kind it has none of
            AssertDefect(TopologyKit.Check(v => v.Zones(), TopologyKit.Edit(n => n["zones"][0]["kinds"] = new JsonArray("Hauler"))), "V8 zone: sp-zone-cut has no stand", "a zone with no stand for a hauler");
            // the yard has the refuel bay's two stands, which are service stands and do not make it a place to send a hauler to
            Assert.IsTrue(Topology.Stands.All(s => s.Role == "service"), "the bay's stands are service stands");
            AssertDefect(TopologyKit.Check(v => v.Zones(), TopologyKit.Edit(n => n["zones"][2]["kinds"] = new JsonArray("Hauler"))), "V8 zone: sp-zone-yard has no stand for a Hauler", "the yard asked for a hauler with only the bay's stands in it");
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
        public void ALanesAirFromAWorkAreaIsHeldAtItsBoundary()
        {
            // an authored lane keeps 1.5 m from another machine's work area: a cell 1.4 m off SP-DZ-0002's is inside that, one 1.6 m off is not
            // (the haul loop is held only to stay out, so a lane held to 0 m instead of 1.5 would pass the first)
            var rip = Topology.WorkAreas.First(w => w.Machine == "SP-DZ-0002");
            double cx = rip.Polygon.X.Average(), cz = rip.Polygon.Z.Average();
            var probe = new TopologyValidator(Topology, World);
            (double X, double Z) At(double gap)
            {
                double lo = 0.0, hi = 120.0;
                for (var i = 0; i < 60; i++)
                {
                    var mid = (lo + hi) / 2.0;
                    var lane = new Lane("probe", "road", false, new[] { new Cell(cx + mid, cz, cx + mid, cz + 1.0, double.NaN) }, -1, null, Topology.SlotM);
                    if (Geometry.GroupGap(probe.CellSwept(lane, 0), new[] { rip.Polygon }) < gap) lo = mid;
                    else hi = mid;
                }

                return (cx + hi, cz);
            }

            var near = At(1.4);
            AssertDefect(TopologyKit.Check(v => v.WorkAreas(), WithSingleCellLane("road/fill-road/back", near.X, near.Z, near.X, near.Z + 1.0)),
                "V9 work area: road/fill-road/back cell 0 is 1.4", "a lane cell 1.4 m off SP-DZ-0002's area");
            var far = At(1.6);
            var clear = TopologyKit.Check(v => v.WorkAreas(), WithSingleCellLane("road/fill-road/back", far.X, far.Z, far.X, far.Z + 1.0));
            Assert.IsFalse(clear.Any(d => d.StartsWith("V9 work area: road/fill-road/back cell 0") && d.Contains("SP-DZ-0002")), "a cell 1.6 m off it is clear: " + string.Join("; ", clear.Take(3)));
        }

        [Test]
        public void TheWholeTopologyIsCleanUnderEveryCheck()
        {
            AssertClean(new TopologyValidator(Topology, World).All(), "the committed topology");
        }
    }
}
