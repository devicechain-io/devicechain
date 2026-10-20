// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Sim.Traffic;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>
    /// The interlocking's rules on small sites made here, with nothing of the quarry in them: lanes of 2 m cells along a line, junction boxes
    /// and stations given by hand, and movers driven by a plain driver that goes as far as the interlocking lets it, at most 4 m/s.
    /// </summary>
    public sealed class InterlockingTests
    {
        const double CellM = 2.0, SlotM = 6.0, Dt = 0.25, Speed = 4.0, Want = 16.0;

        // ---- a site, made by hand

        sealed class SiteBuilder
        {
            public readonly List<Lane> Lanes = new List<Lane>();
            public readonly List<Junction> Junctions = new List<Junction>();
            public readonly List<StationSpec> Stations = new List<StationSpec>();
            public readonly List<(string, int, string, int)> Conflicts = new List<(string, int, string, int)>();

            /// <summary>A lane of <paramref name="n"/> cells from (x, z) the way (dx, dz) points, each 2 m.</summary>
            public SiteBuilder Lane(string id, int n, double x, double z, double dx, double dz, bool cyclic = false)
            {
                var cells = new List<Cell>();
                for (var k = 0; k < n; k++)
                    cells.Add(new Cell(x + dx * CellM * k, z + dz * CellM * k, x + dx * CellM * (k + 1), z + dz * CellM * (k + 1), k * 2.0));
                Lanes.Add(new Lane(id, "road", cyclic, cells, -1, id, SlotM));
                return this;
            }

            public SiteBuilder Conflict(string a, int i, string b, int j)
            {
                Conflicts.Add((a, i, b, j));
                return this;
            }

            public SiteBuilder Box(string id, params (string Lane, int A, int B)[] members)
            {
                Junctions.Add(new Junction(id, "oncoming", new Polygon(new[] { 0.0, 1, 0 }, new[] { 0.0, 0, 1 }),
                    members.Select(m => new LaneRun(m.Lane, new CellRun(m.A, m.B))).ToList(), new List<LaneCell>(), new List<LaneRun>()));
                return this;
            }

            public SiteBuilder Station(string id, string lane, int coreA, int coreB, int bufA, int bufB, int capacity, double headway, string partner = null)
            {
                Stations.Add(new StationSpec(id, lane, new CellRun(coreA, coreB), new CellRun(bufA, bufB), capacity, headway, headway, false,
                    partner == null ? null : new StationPartner(partner, 1, 0, 0, 0, 0, 0), 0, new List<string>()));
                return this;
            }

            public Interlocking Build()
            {
                var rules = new TopologyRules(1.0, 0.3, 0.0, 0.25, 1.0, 12, 40, 14.0);
                var topology = new SiteTopology("test", new SourceHashes("", "", "", ""), CellM, SlotM, rules, new FleetBlock(0, 0, 0), new List<ZoneKinds>(),
                    Lanes, new List<LaneExit>(), new List<LaneEntry>(), Junctions, new List<StandPlace>(), null, Stations, new List<WorkArea>(), new List<Exemption>());
                return new Interlocking(new CellSpace(topology, Conflicts)) { BackstopSeconds = 30.0 };
            }
        }

        /// <summary>A mover on a lane, driven as far as the interlocking lets it: at most <see cref="Speed"/>, stopping short of what it holds.</summary>
        sealed class Driver
        {
            public readonly string Id;
            public readonly int[] Path;
            public readonly bool Cyclic;
            public double S;          // metres along the lane from the start of its first cell (a cyclic lane: unwound)
            public bool Halted;       // told to stand still
            public double Last;       // how far it went in the last step

            public Driver(string id, int[] path, bool cyclic)
            {
                Id = id;
                Path = path;
                Cyclic = cyclic;
            }

            public long Index => Cyclic ? (long)Math.Floor(S / CellM + 1e-9) : Math.Min(Path.Length - 1, (long)Math.Floor(S / CellM + 1e-9));
            public double Offset => S - Index * CellM;
            public bool Done => !Cyclic && S >= (Path.Length - 1) * CellM;
            public double End => (Path.Length - 1) * CellM;
        }

        static int[] LaneCells(Interlocking il, string lane) =>
            Enumerable.Range(0, il.Cells.Topology.Lane(lane).Cells.Count).Select(k => il.Cells.Of(lane, k)).ToArray();

        /// <summary>Puts a mover on the whole of a lane, <paramref name="at"/> metres along it.</summary>
        static Driver Put(Interlocking il, string id, string lane, double at)
        {
            var cyclic = il.Cells.Topology.Lane(lane).Cyclic;
            var d = new Driver(id, LaneCells(il, lane), cyclic) { S = at };
            il.Place(id, d.Path, cyclic, d.Index, d.Offset, i => (i * CellM - d.S) / Speed);
            il.SetWant(id, Want);
            return d;
        }

        /// <summary>One step: every driver's pose told, the interlocking steps, every driver moves as far as it may.</summary>
        static void Step(Interlocking il, double now, IEnumerable<Driver> drivers)
        {
            var all = drivers.ToList();
            foreach (var d in all)
                if (il.Has(d.Id)) il.Move(d.Id, d.Index, d.Offset);
            il.Step(now);
            foreach (var d in all)
            {
                if (d.Halted || d.Done || !il.Has(d.Id)) continue;
                var ds = Math.Max(0.0, Math.Min(Speed * Dt, il.Authority(d.Id) - 0.05));
                d.S += ds;
                d.Last = ds;
                if (!d.Cyclic) d.S = Math.Min(d.S, d.End);
            }

            // a mover at the end of its lane leaves the site
            foreach (var d in all)
                if (d.Done && il.Has(d.Id)) il.Remove(d.Id);
        }

        /// <summary>Steps until <paramref name="until"/> holds or <paramref name="seconds"/> have passed, checking R2 at every step. True when it held.</summary>
        static bool Run(Interlocking il, ref double now, double seconds, Func<bool> until, params Driver[] drivers)
        {
            for (var t = 0.0; t < seconds; t += Dt)
            {
                if (until != null && until()) return true;
                now += Dt;
                Step(il, now, drivers);
                CollectionAssert.IsEmpty(il.Breaches(), "at " + now.ToString("0.00") + " s: " + il);
            }

            return until != null && until();
        }

        static int Cell(Interlocking il, string lane, int k) => il.Cells.Of(lane, k);

        // what a mover waits for, in words; one that has left the site waits for nothing
        static string Why(Interlocking il, string id) => il.Has(id) ? il.Explain(id) ?? il.WaitingFor(id) ?? "nothing" : "gone";

        // ---- R1 and R3: holding is contiguous from the body forward; a lane is a queue

        [Test]
        public void AFollowerStopsAShadowBehindAStandingLeaderAndGoesOnWhenItGoes()
        {
            var il = new SiteBuilder().Lane("east", 60, 0, 0, 1, 0).Build();
            var leader = Put(il, "lead", "east", 40.0);
            var follower = Put(il, "follow", "east", 0.0);
            leader.Halted = true;
            il.SetStanding("lead", true);
            var now = 0.0;
            Run(il, ref now, 30, null, leader, follower);
            // the leader's shadow is a slot of cells behind its pose cell: the follower's pose stops before the shadow's first cell
            Assert.LessOrEqual(follower.S, 40.0 - SlotM, "it stopped a shadow behind: at " + follower.S + " m");
            Assert.Greater(follower.S, 40.0 - SlotM - CellM - 0.5, "and not far short of it");
            Assert.AreEqual("lead", il.BlockerOf("follow"), "and it says who it is behind");

            leader.Halted = false;
            il.SetStanding("lead", false);
            var before = follower.S;
            Run(il, ref now, 10, null, leader, follower);
            Assert.Greater(follower.S, before + 20.0, "it went on when the leader did");
        }

        [Test]
        public void AFollowerNeverHoldsACellOfTheMoverInFrontOfIt()
        {
            var il = new SiteBuilder().Lane("east", 60, 0, 0, 1, 0).Build();
            var leader = Put(il, "lead", "east", 20.0);
            var follower = Put(il, "follow", "east", 0.0);
            var now = 0.0;
            for (var i = 0; i < 200; i++)
            {
                leader.Halted = i % 40 < 20;   // stop and go
                now += Dt;
                Step(il, now, new[] { leader, follower });
                if (!il.Has("lead") || !il.Has("follow")) break;
                var mine = new HashSet<int>(il.Holdings("follow"));
                Assert.IsFalse(il.Holdings("lead").Any(mine.Contains), "they never hold one cell, at " + now);
                Assert.Less(follower.S, leader.S - SlotM + 1e-9, "and the follower stays a slot behind, at " + now);
            }
        }

        // ---- R2: a cell and the cells that conflict with it are one mover's at a time

        [Test]
        public void TwoMoversConvergingOnConflictingCellsAreNeverBothOnThem()
        {
            // two lanes that cross at one cell each: a[10] and b[10] conflict, with no box: the rule alone keeps them apart
            var il = new SiteBuilder().Lane("a", 30, 0, 0, 1, 0).Lane("b", 30, 20, -20, 0, 1).Conflict("a", 10, "b", 10).Build();
            var a = Put(il, "A", "a", 0.0);
            var b = Put(il, "B", "b", 0.0);
            var both = 0;
            var now = 0.0;
            for (var i = 0; i < 400; i++)
            {
                now += Dt;
                Step(il, now, new[] { a, b });
                if (il.Has("A") && il.Has("B") && il.Occupies("A", Cell(il, "a", 10)) && il.Occupies("B", Cell(il, "b", 10))) both++;
            }

            Assert.AreEqual(0, both, "never both on the crossing");
            Assert.IsTrue(a.Done && b.Done, "and both got through: A at " + a.S + ", B at " + b.S);
        }

        // ---- R4: a section is granted whole or not at all

        static SiteBuilder Narrows()
        {
            // "a" runs east and "b" runs west along one road that narrows to one truck between x = 20 and x = 40: a[10..19] and b[10..19]
            // are the same ground (b's cell j covers x from 60 - 2j - 2 to 60 - 2j), and every pair of them within a cell of each other conflicts
            var site = new SiteBuilder().Lane("a", 30, 0, 0, 1, 0).Lane("b", 30, 60, 0, -1, 0);
            for (var i = 10; i <= 19; i++)
                for (var j = 10; j <= 19; j++)
                    if (Math.Abs(i + j - 29) <= 1) site.Conflict("a", i, "b", j);
            return site.Box("narrows", ("a", 10, 19), ("b", 10, 19));
        }

        [Test]
        public void ASectionIsReservedWholeOrNotAtAll()
        {
            var il = Narrows().Build();
            var a = Put(il, "A", "a", 12.0);
            var now = 0.0;
            Assert.IsTrue(Run(il, ref now, 10, () => il.HasReserved("A", Cell(il, "a", 10)), a), "A is granted the narrows");
            for (var k = 10; k <= 19; k++) Assert.IsTrue(il.HasReserved("A", Cell(il, "a", k)), "a[" + k + "] is A's");
            Assert.IsTrue(il.HasReserved("A", Cell(il, "a", 20)), "and the cell beyond, where it may stand clear of it");
            Assert.IsFalse(il.HasReserved("A", Cell(il, "a", 21)), "and no more: beyond that is following");

            // another, from the other end, while A's way is through: none of it, and it waits short of it
            var b = Put(il, "B", "b", 12.0);
            Run(il, ref now, 1, null, a, b);
            Assert.AreEqual("narrows", il.Requesting("B"));
            for (var k = 10; k <= 19; k++) Assert.IsFalse(il.HasReserved("B", Cell(il, "b", k)), "b[" + k + "] is not B's");
            Assert.IsFalse(il.Holds("B", Cell(il, "b", 10)), "nor does it hold the first of it");
        }

        [Test]
        public void TwoMoversMeetingInAOneWayStretchFromBothEndsBothGetThroughOneAfterTheOther()
        {
            // the Sitepulse ramp bottom in miniature: a mover from each end of a stretch only one can be in at a time. Taking it cell by cell,
            // both would drive in, meet nose to nose and stand there for good
            var il = Narrows().Build();
            var a = Put(il, "A", "a", 2.0);
            var b = Put(il, "B", "b", 2.0);
            var now = 0.0;
            Assert.IsTrue(Run(il, ref now, 60, () => a.Done && b.Done, a, b), "both got through: A at " + a.S + " m (" + Why(il, "A") + "), B at " + b.S + " m (" + Why(il, "B") + ")");
        }

        [Test]
        public void AYoungerRequestThatConflictsWithNoOlderOneIsGrantedFirst()
        {
            // two separate narrows: A waits for the first (C stands in it); B asks later for the second, which nobody holds, and is granted
            var site = new SiteBuilder().Lane("a", 30, 0, 0, 1, 0).Lane("c", 30, 60, 0, -1, 0).Lane("b", 30, 0, 100, 1, 0).Lane("d", 30, 60, 100, -1, 0);
            foreach (var (x, y) in new[] { ("a", "c"), ("b", "d") })
                for (var i = 10; i <= 19; i++)
                    for (var j = 10; j <= 19; j++)
                        if (Math.Abs(i + j - 29) <= 1) site.Conflict(x, i, y, j);
            site.Box("first", ("a", 10, 19), ("c", 10, 19)).Box("second", ("b", 10, 19), ("d", 10, 19));
            var il = site.Build();
            var c = Put(il, "C", "c", 22.0);   // inside the first narrows, granted in place, and standing there
            c.Halted = true;
            var a = Put(il, "A", "a", 12.0);
            var now = 0.0;
            Assert.IsTrue(Run(il, ref now, 10, () => il.Requesting("A") != null, c, a));
            Assert.AreEqual("first", il.Requesting("A"), "A waits for the first narrows");
            var b = Put(il, "B", "b", 12.0);
            Assert.IsTrue(Run(il, ref now, 10, () => il.HasReserved("B", Cell(il, "b", 10)), c, a, b), "B, younger, is granted the second: it conflicts with nothing A asked for");
            Assert.AreEqual("first", il.Requesting("A"), "A still waits");
        }

        [Test]
        public void ARequestWaitsForAnOlderOneItConflictsWith()
        {
            var il = Narrows().Build();
            var c = Put(il, "C", "b", 22.0);    // in the narrows from the b end, granted in place, and standing
            c.Halted = true;
            var a = Put(il, "A", "a", 12.0);
            var now = 0.0;
            Assert.IsTrue(Run(il, ref now, 10, () => il.Requesting("A") != null, c, a));
            var b = Put(il, "B", "b", 0.0);     // behind C on the b side; when C goes on it asks, later than A, for the same narrows
            c.Halted = false;
            Assert.IsTrue(Run(il, ref now, 30, () => c.S > 22 * CellM + SlotM, c, a, b));
            Assert.IsTrue(Run(il, ref now, 30, () => il.Requesting("B") != null || il.HasReserved("B", Cell(il, "b", 10)), c, a, b));
            Assert.IsTrue(il.HasReserved("A", Cell(il, "a", 10)) || a.S > 20.0, "A, which asked first, has the narrows before B: " + Why(il, "B"));
            Assert.IsTrue(Run(il, ref now, 60, () => a.Done && b.Done && c.Done, c, a, b), "all three got through");
        }

        // ---- R5: the oldest request is not starved by a stream of others

        [Test]
        public void AStreamFromOneEndDoesNotStarveTheOldestRequestFromTheOther()
        {
            var il = Narrows().Build();
            var b = Put(il, "B", "b", 12.0);
            var stream = new List<Driver>();
            var now = 0.0;
            var bThrough = -1.0;
            for (var i = 0; i < 600; i++)
            {
                // a mover enters at the start of lane a every 4 s, when the one before it has made room there
                if (i % 16 == 0 && stream.Count < 12 && stream.All(p => p.S > SlotM + 2 * CellM)) stream.Add(Put(il, "A" + stream.Count, "a", 0.0));
                now += Dt;
                Step(il, now, stream.Concat(new[] { b }));
                CollectionAssert.IsEmpty(il.Breaches());
                if (bThrough < 0 && b.S >= 22 * CellM) bThrough = now;
            }

            Assert.Greater(bThrough, 0, "B got through");
            Assert.Less(bThrough, 40.0, "within the stream, not after it ran dry: " + bThrough);
        }

        // ---- R7: stations

        [Test]
        public void AStationAdmitsNoMoreThanItsCapacityAndKeepsItsHeadway()
        {
            // a station whose core is cells 20..29 (20 m, 5 s at 4 m/s), its buffer behind it; capacity 1, headway 30 s
            var il = new SiteBuilder().Lane("east", 80, 0, 0, 1, 0).Station("st", "east", 20, 29, 30, 34, 1, 30.0).Build();
            var first = Put(il, "first", "east", 30.0);
            var second = Put(il, "second", "east", 6.0);
            var now = 0.0;
            var entries = new Dictionary<string, double>();
            Run(il, ref now, 150, () =>
            {
                foreach (var (id, entered, at) in il.Members("st"))
                    if (entered && !entries.ContainsKey(id)) entries[id] = at;
                Assert.LessOrEqual(il.Members("st").Count(m => m.Entered), 1, "at most one in the core at " + now);
                return first.Done && second.Done;
            }, first, second);
            Assert.AreEqual(2, entries.Count, string.Join(", ", entries));
            Assert.GreaterOrEqual(entries["second"] - entries["first"], 30.0 - 1e-3, "the second entered a headway after the first");
            Assert.AreEqual(2, il.Entries("st"));
        }

        sealed class Partner : IStationPartner
        {
            public bool Away { get; set; }
            public bool Ready;
            public bool ReadyFor(double secondsToEntry) => Ready;
        }

        [Test]
        public void AStationWithAPartnerAdmitsOnlyWhenThePartnerIsReadyOrAway()
        {
            var il = new SiteBuilder().Lane("east", 80, 0, 0, 1, 0).Station("st", "east", 20, 29, 30, 34, 1, 5.0, partner: "loader").Build();
            var partner = new Partner();
            il.SetPartner("st", partner);
            var truck = Put(il, "truck", "east", 20.0);
            var now = 0.0;
            Assert.IsTrue(Run(il, ref now, 20, () => il.Requesting("truck") != null, truck));
            Run(il, ref now, 10, null, truck);
            Assert.AreEqual("station st", il.Requesting("truck"), "it waits for the partner");
            StringAssert.Contains("waits for loader", il.Explain("truck"));
            Assert.Less(truck.S, 20 * CellM, "short of the core");

            partner.Ready = true;
            Assert.IsTrue(Run(il, ref now, 5, () => il.Requesting("truck") == null, truck), "admitted once the partner is ready");

            // with the partner away (and never ready), the next rides through
            partner.Ready = false;
            partner.Away = true;
            var next = Put(il, "next", "east", 0.0);
            Assert.IsTrue(Run(il, ref now, 120, () => next.Done, truck, next), "a partner away lets movers ride through: " + Why(il, "next"));
        }

        [Test]
        public void TwoMembersOfAStationAreNotHeldToEachOthersConflictsInItsCore()
        {
            // a core in which the lane folds back over itself (a dump manoeuvre): its cells 22 and 27 conflict. Two members a headway apart
            // pass through it at their own pace (the timing keeps them apart, not the cells): the second reaches 22 as the first is at 27
            var il = new SiteBuilder().Lane("east", 80, 0, 0, 1, 0).Conflict("east", 22, "east", 27).Station("pad", "east", 20, 29, 30, 39, 2, 2.0).Build();
            var first = Put(il, "first", "east", 46.0);    // in the core, a member as the site starts
            var second = Put(il, "second", "east", 36.0);  // 10 m behind (more than a slot), admitted as it comes to the core
            var now = 0.0;
            var slowed = new List<string>();
            Assert.IsTrue(Run(il, ref now, 60, () =>
            {
                if (now > 0 && second.S > 40.0 && second.S < 30 * CellM && second.Last < Speed * Dt - 1e-9)
                    slowed.Add(second.S.ToString("0.0") + " m (" + Why(il, "second") + ")");
                return first.Done && second.Done;
            }, first, second), "both went through: " + il);
            CollectionAssert.IsEmpty(slowed, "the second member was never held back in the core");
        }

        [Test]
        public void AMoverThatIsNotAMemberIsHeldToTheConflictsOfTheCore()
        {
            // the same fold, but no station: the one behind is held where the fold would bring it onto the one in front
            var il = new SiteBuilder().Lane("east", 80, 0, 0, 1, 0).Conflict("east", 22, "east", 27).Build();
            var first = Put(il, "first", "east", 54.0);    // posed in cell 27
            first.Halted = true;
            var second = Put(il, "second", "east", 36.0);
            var now = 0.0;
            Run(il, ref now, 20, null, first, second);
            Assert.Less(second.S, 22 * CellM, "held short of the cell that folds onto the one in front: at " + second.S);
            StringAssert.Contains("first", Why(il, "second"));
        }

        // ---- the backstop

        [Test]
        public void ARequestKeptWaitingByAStandingMoverIsReportedOnce()
        {
            var il = Narrows().Build();
            var reported = new List<(string, string, string)>();
            il.Defect += (who, blocker, what) => reported.Add((who, blocker, what));
            var parked = Put(il, "parked", "b", 30.0);   // standing in the narrows for good
            parked.Halted = true;
            il.SetStanding("parked", true);
            var a = Put(il, "A", "a", 12.0);
            var now = 0.0;
            Assert.IsTrue(Run(il, ref now, 10, () => il.Requesting("A") != null, parked, a));
            Run(il, ref now, 20, null, parked, a);
            Assert.AreEqual(0, reported.Count, "not before the backstop's time: " + now);
            Run(il, ref now, 30, null, parked, a);
            CollectionAssert.AreEqual(new[] { ("A", "parked", "narrows") }, reported, "reported once, naming who is in the way");
            Assert.AreEqual(1, il.Defects);
        }

        // ---- the properties, over random traffic

        [Test]
        public void RandomTrafficOnARingWithACrossingNeverBreaksTheRulesAndEveryMoverKeepsMoving()
        {
            foreach (var seed in new[] { 1, 2, 3, 4, 5 })
            {
                var rnd = new Random(seed);
                // a ring of 100 cells, and a road across it whose cells 8..11 are the ground of the ring's cells 48..51 (a crossing box)
                var site = new SiteBuilder().Lane("ring", 100, 0, 0, 1, 0, cyclic: true).Lane("cross", 20, 98, -20, 0, 1);
                for (var i = 48; i <= 51; i++)
                    for (var j = 8; j <= 11; j++)
                        site.Conflict("ring", i, "cross", j);
                site.Box("crossing", ("ring", 48, 51), ("cross", 8, 11));
                var il = site.Build();
                var ring = new List<Driver>();
                foreach (var k in new[] { 0, 40, 80, 120, 160 }) ring.Add(Put(il, "R" + k, "ring", k));
                var crossers = new List<Driver>();
                var now = 0.0;
                var start = ring.ToDictionary(d => d.Id, d => d.S);
                for (var i = 0; i < 2400; i++)
                {
                    // the ring's movers stop for a moment now and then; a mover crosses the ring every 20 s
                    foreach (var d in ring) d.Halted = d.Halted ? rnd.NextDouble() < 0.7 : rnd.NextDouble() < 0.02;
                    if (i % 80 == 0) crossers.Add(Put(il, "C" + crossers.Count, "cross", 0.0));
                    now += Dt;
                    Step(il, now, ring.Concat(crossers.Where(c => il.Has(c.Id))));
                    // a crosser that has crossed leaves the site
                    foreach (var c in crossers.Where(c => c.Done && il.Has(c.Id))) il.Remove(c.Id);
                    CollectionAssert.IsEmpty(il.Breaches(), "seed " + seed + " at " + now);
                }

                Assert.AreEqual(0, il.Overruns, il.LastOverrun);
                foreach (var d in ring)
                    Assert.Greater(d.S - start[d.Id], 600.0, "seed " + seed + ": " + d.Id + " went round (" + (d.S - start[d.Id]).ToString("0") + " m in 600 s)");
                Assert.GreaterOrEqual(crossers.Count(c => c.Done), crossers.Count - 2, "seed " + seed + ": every crosser but the last got across");
            }
        }
    }
}
