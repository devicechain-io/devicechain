// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;

namespace DeviceChain.Sim.Traffic
{
    /// <summary>The machine a timed station is coupled to (a loader at a load point): whether it is away, and whether it can meet the next machine.</summary>
    public interface IStationPartner
    {
        /// <summary>The partner is not working the station (it was sent elsewhere): the station lets machines ride through.</summary>
        bool Away { get; }

        /// <summary>
        /// The partner will be where its cycle meets a machine entering the core <paramref name="secondsToEntry"/> seconds from now (done with the
        /// last one, and back at its ready pose or on its way there in time): admission waits for it, so the partner never jumps to catch up.
        /// </summary>
        bool ReadyFor(double secondsToEntry);
    }

    /// <summary>Seconds from now until a mover's pose reaches a place on its path (an absolute path index), going at its own pace.</summary>
    public delegate double MoverEta(long pathIndex);

    /// <summary>
    /// The interlocking: railway block signalling over the cells of a site's lanes (and of the free routes machines are given off them).
    /// Every rule here is about CELLS: a machine is only ever posed in a cell it holds, and it moves only as far as the cells it holds reach.
    ///
    /// <list type="bullet">
    /// <item><b>R1 hold to occupy, contiguously.</b> A mover's holdings are one run along its path: the cells behind its pose that the length of a
    /// slot covers (its shadow, which keeps the next one a slot behind), its pose cell, and the cells ahead it has taken, up to as far as it wants
    /// to see (its braking distance and a margin, set by whoever drives it). It never takes a cell beyond one it could not take.</item>
    /// <item><b>R2 exclusive holdings.</b> A cell is taken only when no other mover holds it, no cell conflicting with it is occupied by another (a
    /// mover's pose and the cells ahead of it: the shadow is about following, not about the ground), and no other mover has it or a cell conflicting
    /// with it reserved.</item>
    /// <item><b>R3 lanes are queues.</b> Taking cells one at a time along a lane is following: a mover stops a shadow behind the one in front.</item>
    /// <item><b>R4 sections.</b> A cell of a junction box or of a station's core is never taken one at a time: the mover asks for the whole of
    /// its way through (every box and core it meets back to back) and a cell beyond where it may stand, and is granted
    /// all of it at once or none of it. Requests are granted first come first served, but a younger one is granted early when it conflicts
    /// with no older one.</item>
    /// <item><b>R5 head blocking.</b> While the oldest request waits, nobody who holds or has reserved none of the ground it asks for (and the
    /// ground that conflicts with it) may take any of that ground: those inside drain, those outside wait, so the request is granted.</item>
    /// <item><b>R7 stations.</b> A station's core is entered only by admission, which takes a place (no more than its capacity), keeps its
    /// headway (the time between two entries is at least the design spacing) and, at a station coupled to a partner, waits for the partner at
    /// its ready pose unless the partner is away. Two members of one station are timed apart: in the core they are not held to each other's
    /// conflicts, only never to overtake.</item>
    /// </list>
    ///
    /// A mover that holds a grant never waits for anything but the following rule beyond it, and a member of a station never waits in its core
    /// while the buffer behind has room: that and the site data's capacities (checked by <see cref="TopologyValidator"/>) are why it cannot
    /// deadlock. The backstop (<see cref="BackstopSeconds"/>) is for a site that breaks them: a request kept waiting that long by machines that are
    /// standing is reported, never broken by force.
    ///
    /// Nothing here knows how a mover moves: whoever drives it says where its pose is (<see cref="Move"/>) and asks how far it may go
    /// (<see cref="Authority"/>). Main thread only; deterministic for the same calls.
    /// </summary>
    public sealed class Interlocking
    {
        /// <summary>A predicted station entry this much earlier than the headway allows still keeps it (rounding in the clocks).</summary>
        public const double HeadwayToleranceSeconds = 1e-3;

        readonly CellSpace cells;
        readonly SiteTopology topology;
        readonly List<Mover> movers = new List<Mover>();
        readonly Dictionary<string, Mover> byId = new Dictionary<string, Mover>(StringComparer.Ordinal);
        readonly List<Request> pending = new List<Request>();
        readonly StationState[] stations;
        long sequence;
        double now;

        public Interlocking(CellSpace cells)
        {
            this.cells = cells ?? throw new ArgumentNullException(nameof(cells));
            topology = cells.Topology;
            SlotM = topology.SlotM;
            RoomM = topology.CellM;
            stations = new StationState[topology.Stations.Count];
            for (var s = 0; s < stations.Length; s++) stations[s] = new StationState(topology.Stations[s]);
        }

        public CellSpace Cells => cells;

        /// <summary>How long a mover's shadow is: a hauler's length and the air kept, metres. A follower stops a shadow behind the one in front.</summary>
        public double SlotM { get; }

        /// <summary>
        /// The room a grant reaches beyond a section, metres: a cell. Cells conflict by the ground a machine POSED in them covers, its whole
        /// body, so a machine whose pose is a cell clear of a section is clear of everything the section guards; beyond it, following does the
        /// rest. (A slot of room would reach into the queue for the next box where two boxes lie close, and a truck waiting there would hold
        /// up the grant that would let the queue move.)
        /// </summary>
        public double RoomM { get; }

        /// <summary>How long (seconds) a request may wait on machines that are standing before it is reported (<see cref="Defect"/>).</summary>
        public double BackstopSeconds { get; set; } = 30.0;

        /// <summary>The time of the last <see cref="Step"/>.</summary>
        public double Now => now;

        /// <summary>
        /// Raised when a request has waited <see cref="BackstopSeconds"/> on movers that are all standing: (requester, the standing mover in its
        /// way, the section it asked for). The site breaks P3; the owner of the requester decides what to do (re-plan, give up).
        /// </summary>
        public event Action<string, string, string> Defect;

        /// <summary>How many times <see cref="Defect"/> has been raised.</summary>
        public int Defects { get; private set; }

        /// <summary>How many times a driver moved a mover past the cells it held (never, while the drivers keep to <see cref="Authority"/>), and the last.</summary>
        public int Overruns { get; private set; }

        public string LastOverrun { get; private set; }

        // ------------------------------------------------------------------------------------------------ movers

        /// <summary>One machine the interlocking keeps: its path, its pose and what it holds.</summary>
        sealed class Mover
        {
            public string Id;
            public int Order;
            public int[] Path;
            public bool Cyclic;
            public long Pose;
            public double Offset;
            public MoverEta Eta;
            public double Want;
            public bool Standing;
            public readonly List<int> Held = new List<int>();
            public int PoseAt;
            public readonly HashSet<int> HeldSet = new HashSet<int>();
            public readonly HashSet<int> Occupied = new HashSet<int>();
            public readonly HashSet<int> Reserved = new HashSet<int>();
            public Request Pending;
            public string Waiting;
            public string Blocker;

            public int CellAt(long index) => Cyclic ? Path[(int)(((index % Path.Length) + Path.Length) % Path.Length)] : Path[index];

            public bool OnPath(long index) => Cyclic || (index >= 0 && index < Path.Length);

            public long HeadIndex => Pose + (Held.Count - 1 - PoseAt);
        }

        /// <summary>A mover's request for a section: the cells of its way through and the room beyond, granted all at once.</summary>
        sealed class Request
        {
            public Mover Mover;
            public long Seq;
            public long Start;
            public readonly List<int> Span = new List<int>();
            public readonly HashSet<int> SpanSet = new HashSet<int>();
            public readonly HashSet<int> Closure = new HashSet<int>();
            public readonly List<int> Stations = new List<int>();
            public double Since;
            public bool Reported;
            public string Name;
        }

        /// <summary>A mover's place at a station: from its grant until it has passed the end of the way it was granted (core, buffer and room).</summary>
        sealed class Admission
        {
            public Mover Mover;
            public double Entry;
            public bool Entered;

            /// <summary>How long it takes to go through the core at its own pace (from its clock): it is in the core from its entry for that long.</summary>
            public double Duration;

            /// <summary>Its pose has left the core: it no longer takes one of the station's places.</summary>
            public bool PastCore;

            /// <summary>
            /// The absolute path index of the last cell of its way through (the core, and on to where it may stand beyond): it stays a member
            /// until its pose is past it. A new path (a truck leaving the loop inside the core) moves it.
            /// </summary>
            public long EndIndex;
        }

        sealed class StationState
        {
            public StationState(StationSpec spec) => Spec = spec;
            public readonly StationSpec Spec;
            public readonly List<Admission> Members = new List<Admission>();
            public double LastEntry = double.NegativeInfinity;
            public IStationPartner Partner;
            public int Entries;
        }

        Mover Get(string id) => byId.TryGetValue(id, out var m) ? m : throw new KeyNotFoundException("the interlocking keeps no mover " + id);

        public bool Has(string id) => byId.ContainsKey(id);

        public IEnumerable<string> Movers => movers.Select(m => m.Id);

        /// <summary>
        /// Puts a mover on the site: its path (cells in the order it will drive them; a cyclic path goes round for ever), the absolute index of
        /// the cell its pose is in and how far into it, and how to tell how long it takes to get somewhere along it. It takes its pose cell and its
        /// shadow behind along the path. A mover put down inside a junction box or a station's core (the site as it starts) is given the rest of
        /// its way through as if it had been granted it, and is a member of the station whose core it is in.
        /// </summary>
        public void Place(string id, IReadOnlyList<int> path, bool cyclic, long poseIndex, double offset, MoverEta eta)
        {
            if (byId.ContainsKey(id)) throw new InvalidOperationException(id + " is already placed");
            if (path == null || path.Count == 0) throw new ArgumentException("a mover has a path of at least one cell");
            var m = new Mover { Id = id, Order = movers.Count, Path = path.ToArray(), Cyclic = cyclic, Pose = poseIndex, Offset = offset, Eta = eta ?? (_ => 0.0) };
            if (!m.OnPath(poseIndex)) throw new ArgumentOutOfRangeException(nameof(poseIndex));
            movers.Add(m);
            byId[id] = m;
            var pose = m.CellAt(poseIndex);
            // the shadow behind, along the path as far as it goes
            var behind = new List<int>();
            var len = 0.0;
            for (var i = poseIndex - 1; m.OnPath(i) && len < SlotM && behind.Count < m.Path.Length - 1; i--)
            {
                var c = m.CellAt(i);
                behind.Insert(0, c);
                len += cells.Length(c);
            }

            foreach (var c in behind) AddHeld(m, c, false);
            AddHeld(m, pose, true);
            m.PoseAt = m.Held.Count - 1;
            if (cells.Section(pose)) GrantInPlace(m);
        }

        // the site as it starts: a mover inside a section has the rest of it, and is in the station it is in
        void GrantInPlace(Mover m)
        {
            // back to where its way through began, so the span is the one it would have been granted
            var start = m.Pose;
            for (var i = m.Pose - 1; m.OnPath(i) && m.Pose - i < m.Path.Length; i--)
            {
                var c = m.CellAt(i);
                if (!cells.Section(c)) break;
                start = i;
            }

            var r = NewRequest(m, start);
            var end = start + r.Span.Count - 1;
            // what is behind its pose it has already been through
            var through = r.Span.GetRange(0, (int)Math.Min(r.Span.Count, m.Pose - start));
            r.Span.RemoveRange(0, through.Count);
            foreach (var c in r.Span) m.Reserved.Add(c);
            foreach (var s in r.Stations)
            {
                var st = stations[s];
                var coreStart = FirstIndexOfCore(m, start, s);
                var entry = now + m.Eta(coreStart);
                st.Members.Add(new Admission { Mover = m, Entry = entry, Duration = CoreDuration(m, coreStart, s), Entered = cells.Core(m.CellAt(m.Pose)) == s || coreStart <= m.Pose, EndIndex = end });
                st.LastEntry = Math.Max(st.LastEntry, entry);
            }
        }

        // whether a mover still holds any of a station's core (its shadow trails out of it): until it does not, it is one of its members
        bool HoldsCore(Mover m, int station)
        {
            foreach (var c in m.HeldSet)
                if (cells.Core(c) == station) return true;
            return false;
        }

        // how long the mover takes through the core that starts at coreStart, at its own pace
        double CoreDuration(Mover m, long coreStart, int station)
        {
            var end = coreStart;
            while (m.OnPath(end) && end - coreStart <= m.Path.Length && cells.Core(m.CellAt(end)) == station) end++;
            return m.Eta(end) - m.Eta(coreStart);
        }

        long FirstIndexOfCore(Mover m, long from, int station)
        {
            for (var i = from; m.OnPath(i) && i - from <= m.Path.Length; i++)
                if (cells.Core(m.CellAt(i)) == station) return i;
            return from;
        }

        /// <summary>Takes a mover off the site: everything it holds, has reserved, asked for or was admitted to is given back.</summary>
        public void Remove(string id)
        {
            if (!byId.TryGetValue(id, out var m)) return;
            Cancel(m);
            foreach (var st in stations) st.Members.RemoveAll(a => a.Mover == m);
            movers.Remove(m);
            byId.Remove(id);
            for (var i = 0; i < movers.Count; i++) movers[i].Order = i;
        }

        /// <summary>
        /// Gives a mover a new path that goes on from the cell its pose is in (<paramref name="poseIndex"/> of the new path must be that cell). The
        /// cells ahead it held and the reservations it had are kept as far as the new path goes the same way; the rest are given back, with any
        /// request it was waiting on and any station it was admitted to but has not reached.
        /// </summary>
        public void Repath(string id, IReadOnlyList<int> path, bool cyclic, long poseIndex, double offset, MoverEta eta = null)
        {
            var m = Get(id);
            var np = path.ToArray();
            int CellOf(long i) => cyclic ? np[(int)(((i % np.Length) + np.Length) % np.Length)] : np[i];
            if (!(cyclic || (poseIndex >= 0 && poseIndex < np.Length)) || CellOf(poseIndex) != m.Held[m.PoseAt])
                throw new ArgumentException(id + ": a new path goes on from the cell the mover's pose is in (" + cells.Name(m.Held[m.PoseAt]) + ")");
            // the cells ahead that the new path also drives next, in the same order
            var keep = m.PoseAt + 1;
            for (var j = 1; m.PoseAt + j < m.Held.Count; j++)
            {
                var i = poseIndex + j;
                if (!(cyclic || i < np.Length) || CellOf(i) != m.Held[m.PoseAt + j]) break;
                keep = m.PoseAt + j + 1;
            }

            for (var k = m.Held.Count - 1; k >= keep; k--) DropHeld(m, k);
            Cancel(m, keepHeld: true);
            // reservations stay only where the new path goes next (a section it is in the middle of)
            var ahead = new HashSet<int>();
            for (var j = 0; j < np.Length && (cyclic || poseIndex + j < np.Length); j++)
            {
                var c = CellOf(poseIndex + j);
                if (!m.Reserved.Contains(c)) break;
                ahead.Add(c);
            }

            m.Reserved.RemoveWhere(c => !ahead.Contains(c));
            foreach (var st in stations)
                st.Members.RemoveAll(a => a.Mover == m && !a.Entered && !ahead.Any(c => cells.Core(c) == Array.IndexOf(stations, st)));
            m.Path = np;
            m.Cyclic = cyclic;
            m.Pose = poseIndex;
            m.Offset = offset;
            if (eta != null) m.Eta = eta;
            // a member is a member through to the end of its way through as the new path goes: a truck that leaves the loop inside a core
            // drives out of it by its own lane, and stays one of the station's members until it is clear of it (or stands, see SetStanding)
            for (var s = 0; s < stations.Length; s++)
                foreach (var a in stations[s].Members)
                    if (a.Mover == m) a.EndIndex = SpanEnd(m, a.Entered ? m.Pose : FirstIndexOfCore(m, m.Pose, s));
        }

        // the last index of a way through from a section's first cell: through every section and the cells beside a station's region, then
        // a slot of room where the mover may stand (the same way a request's span runs)
        long SpanEnd(Mover m, long from)
        {
            var room = 0.0;
            var last = from;
            for (var i = from; m.OnPath(i) && i - from < m.Path.Length; i++)
            {
                last = i;
                var c = m.CellAt(i);
                if (cells.Section(c) || !cells.Room(c))
                {
                    room = 0.0;
                    continue;
                }

                room += cells.Length(c);
                if (room >= RoomM) return i;
            }

            return last;
        }

        /// <summary>How far ahead of its pose (metres along its path) the mover wants to hold: what it needs to stop in, and a margin.</summary>
        public void SetWant(string id, double metres) => Get(id).Want = Math.Max(0.0, metres);

        /// <summary>
        /// A mover that stands (parked, stalled, serving): it takes nothing more, and a request kept waiting by movers that all stand is a defect of
        /// the site (see <see cref="Defect"/>).
        /// </summary>
        public void SetStanding(string id, bool standing)
        {
            var m = Get(id);
            m.Standing = standing;
            // standing past a station's core, it is no longer one of its members (it is not timed with them)
            if (standing)
                for (var s = 0; s < stations.Length; s++) stations[s].Members.RemoveAll(a => a.Mover == m && a.PastCore && !HoldsCore(m, s));
        }

        /// <summary>
        /// The mover's pose is now in the cell at <paramref name="poseIndex"/> of its path, <paramref name="offset"/> metres into it. It never moves
        /// back, and never past what it holds; one moved past is a fault of its driver, counted in <see cref="Overruns"/> rather than hidden.
        /// </summary>
        public void Move(string id, long poseIndex, double offset)
        {
            var m = Get(id);
            if (poseIndex < m.Pose) throw new InvalidOperationException(id + " moved back along its path (" + m.Pose + " -> " + poseIndex + ")");
            if (poseIndex > m.HeadIndex)
            {
                // a driver that went further than it was allowed: the cells are taken all the same (the body is there), and counted
                Overruns++;
                LastOverrun = id + " moved past the cells it holds (to " + cells.Name(m.CellAt(poseIndex)) + ", holding to " + cells.Name(m.Held[m.Held.Count - 1]) + ")";
                while (m.HeadIndex < poseIndex)
                {
                    var c = m.CellAt(m.HeadIndex + 1);
                    AddHeld(m, c, true);
                }
            }

            while (m.Pose < poseIndex)
            {
                m.Occupied.Remove(m.Held[m.PoseAt]);
                m.Pose++;
                m.PoseAt++;
            }

            m.Offset = Math.Max(0.0, offset);
            // reservations behind the pose are spent; the shadow is cut to a slot
            for (var k = 0; k < m.PoseAt; k++) m.Reserved.Remove(m.Held[k]);
            var shadow = 0.0;
            for (var k = 0; k < m.PoseAt; k++) shadow += cells.Length(m.Held[k]);
            while (m.PoseAt > 0 && shadow - cells.Length(m.Held[0]) >= SlotM)
            {
                shadow -= cells.Length(m.Held[0]);
                DropHeld(m, 0);
                m.PoseAt--;
            }

            var pose = m.Held[m.PoseAt];
            for (var s = 0; s < stations.Length; s++)
            {
                var st = stations[s];
                for (var i = st.Members.Count - 1; i >= 0; i--)
                {
                    var a = st.Members[i];
                    if (a.Mover != m) continue;
                    if (!a.Entered && cells.Core(pose) == s)
                    {
                        // when it crossed into the core, by its own clock (not this step's time, which is up to a step late)
                        var start = m.Pose;
                        while (m.OnPath(start - 1) && m.Pose - start < m.Path.Length && cells.Core(m.CellAt(start - 1)) == s) start--;
                        a.Entered = true;
                        a.Entry = Math.Min(now, now + m.Eta(start));
                        st.Entries++;
                        st.LastEntry = Math.Max(st.LastEntry, a.Entry);
                    }
                    else if (a.Entered && cells.Core(pose) != s)
                    {
                        a.PastCore = true;
                    }

                    // past the whole of its way through, or past the core and standing where its trip ended: no longer a member
                    if (a.PastCore && (m.Pose > a.EndIndex || m.Standing) && !HoldsCore(m, s)) st.Members.RemoveAt(i);
                }
            }
        }

        /// <summary>How far (metres along its path) the mover may go from its pose: to the end of the last cell it holds.</summary>
        public double Authority(string id)
        {
            var m = Get(id);
            var d = cells.Length(m.Held[m.PoseAt]) - m.Offset;
            for (var k = m.PoseAt + 1; k < m.Held.Count; k++) d += cells.Length(m.Held[k]);
            return Math.Max(0.0, d);
        }

        /// <summary>The absolute path index of the last cell the mover holds.</summary>
        public long HeadIndex(string id) => Get(id).HeadIndex;

        /// <summary>The absolute path index of the cell the mover's pose is in.</summary>
        public long PoseIndex(string id) => Get(id).Pose;

        /// <summary>Why the mover holds no further than it does, in words (null when nothing is in its way), and the mover that is (or null).</summary>
        public string WaitingFor(string id) => Get(id).Waiting;

        public string BlockerOf(string id) => Get(id).Blocker;

        /// <summary>The cells the mover holds, from its shadow's end to its head.</summary>
        public IReadOnlyList<int> Holdings(string id) => Get(id).Held;

        /// <summary>The cell the mover's pose is in.</summary>
        public int PoseCell(string id)
        {
            var m = Get(id);
            return m.Held[m.PoseAt];
        }

        public bool Holds(string id, int cell) => Get(id).HeldSet.Contains(cell);

        /// <summary>Whether the mover occupies a cell: its pose cell or one it holds ahead of it (its shadow behind is about following, not ground).</summary>
        public bool Occupies(string id, int cell) => Get(id).Occupied.Contains(cell);

        public bool HasReserved(string id, int cell) => Get(id).Reserved.Contains(cell);

        /// <summary>Whether the mover is waiting for a section, and which (the junction or station it asked for).</summary>
        public string Requesting(string id) => Get(id).Pending?.Name;

        /// <summary>The movers waiting for a section, oldest first.</summary>
        public IEnumerable<string> Queue => pending.Select(r => r.Mover.Id);

        // ------------------------------------------------------------------------------------------------ stations

        int StationIndex(string station)
        {
            for (var s = 0; s < stations.Length; s++)
                if (stations[s].Spec.Id == station) return s;
            throw new KeyNotFoundException("no station " + station);
        }

        public void SetPartner(string station, IStationPartner partner) => stations[StationIndex(station)].Partner = partner;

        /// <summary>The movers admitted to a station (in the order they were admitted), and whether each has reached its core.</summary>
        public IReadOnlyList<(string Id, bool Entered, double Entry)> Members(string station) =>
            stations[StationIndex(station)].Members.Where(a => !a.PastCore).Select(a => (a.Mover.Id, a.Entered, a.Entry)).ToList();

        /// <summary>How many movers have entered the station's core since the site started.</summary>
        public int Entries(string station) => stations[StationIndex(station)].Entries;

        bool Member(Mover m, int station)
        {
            if (station < 0) return false;
            foreach (var a in stations[station].Members)
                if (a.Mover == m) return true;
            return false;
        }

        // ------------------------------------------------------------------------------------------------ the step

        /// <summary>
        /// One step at <paramref name="time"/>: requests are granted in the order they were made where they can be, then every mover takes the cells
        /// ahead it wants that it may. Call it after every mover's pose has been told (<see cref="Move"/>) and before anyone asks its authority.
        /// </summary>
        public void Step(double time)
        {
            now = time;
            for (var i = 0; i < pending.Count; i++)
            {
                var r = pending[i];
                if (OlderConflict(r) || !Grantable(r)) continue;
                Grant(r);
                pending.RemoveAt(i--);
            }

            foreach (var m in movers) Extend(m);
            Backstop();
        }

        void Extend(Mover m)
        {
            m.Waiting = null;
            m.Blocker = null;
            if (m.Standing) return;
            var ahead = cells.Length(m.Held[m.PoseAt]) - m.Offset;
            for (var k = m.PoseAt + 1; k < m.Held.Count; k++) ahead += cells.Length(m.Held[k]);
            var guard = 0;
            while (ahead < m.Want && guard++ < 10000)
            {
                var idx = m.HeadIndex + 1;
                if (!m.OnPath(idx)) break;
                var c = m.CellAt(idx);
                if (!m.Reserved.Contains(c) && cells.Section(c))
                {
                    if (m.Pending == null)
                    {
                        var r = NewRequest(m, idx);
                        m.Pending = r;
                        pending.Add(r);
                        if (!OlderConflict(r) && Grantable(r))
                        {
                            Grant(r);
                            pending.Remove(r);
                            continue;
                        }
                    }

                    m.Waiting = "waiting for " + m.Pending.Name;
                    m.Blocker = BlockerOfRequest(m.Pending);
                    break;
                }

                var blocker = Blocking(m, c, null, true);
                if (blocker != null)
                {
                    m.Waiting = blocker.Item1;
                    m.Blocker = blocker.Item2;
                    break;
                }

                m.Held.Add(c);
                m.HeldSet.Add(c);
                m.Occupied.Add(c);
                ahead += cells.Length(c);
            }
        }

        // a request kept back by an older one it conflicts with, unless its mover is inside the older one's span (it is in the way of the older
        // request, and its own grant is how it gets out of the way: holding it back would have each wait for the other)
        bool OlderConflict(Request r)
        {
            foreach (var o in pending)
            {
                if (o == r) return false;
                if (o.Closure.Overlaps(r.Closure) && !Inside(r.Mover, o)) return true;
            }

            return false;
        }

        Request NewRequest(Mover m, long start)
        {
            var r = new Request { Mover = m, Seq = ++sequence, Start = start, Since = now };
            var room = 0.0;
            var names = new List<string>();
            for (var i = start; m.OnPath(i) && i - start < m.Path.Length; i++)
            {
                var c = m.CellAt(i);
                r.Span.Add(c);
                var box = cells.Box(c);
                var core = cells.Core(c);
                if (cells.Section(c))
                {
                    room = 0.0;
                    if (box >= 0 && !names.Contains(topology.Junctions[box].Id)) names.Add(topology.Junctions[box].Id);
                    if (core >= 0 && !r.Stations.Contains(core))
                    {
                        r.Stations.Add(core);
                        names.Add("station " + topology.Stations[core].Id);
                    }

                    continue;
                }

                // room is where it may stand: a cell beside a station's core or buffer is not (a diverge out of a core is driven through)
                if (!cells.Room(c))
                {
                    room = 0.0;
                    continue;
                }

                room += cells.Length(c);
                if (room >= RoomM) break;
            }

            foreach (var c in r.Span)
            {
                r.SpanSet.Add(c);
                r.Closure.Add(c);
                foreach (var x in cells.Conflicts(c)) r.Closure.Add(x);
            }

            r.Name = string.Join(" + ", names);
            return r;
        }

        void Grant(Request r)
        {
            var m = r.Mover;
            foreach (var c in r.Span) m.Reserved.Add(c);
            foreach (var s in r.Stations)
            {
                var st = stations[s];
                var coreStart = FirstIndexOfCore(m, r.Start, s);
                var entry = now + m.Eta(coreStart);
                st.Members.Add(new Admission { Mover = m, Entry = entry, Duration = CoreDuration(m, coreStart, s), EndIndex = r.Start + r.Span.Count - 1 });
                st.LastEntry = Math.Max(st.LastEntry, entry);
            }

            m.Pending = null;
        }

        bool Grantable(Request r) => Refusal(r) == null;

        /// <summary>Why a request is not granted now, in words, or null when it may be.</summary>
        string Refusal(Request r)
        {
            var m = r.Mover;
            foreach (var s in r.Stations)
            {
                var st = stations[s];
                if (Member(m, s)) continue;
                var entry = now + m.Eta(FirstIndexOfCore(m, r.Start, s));
                // a member runs through the core at its own pace, so it is in it from its entry for the station's window: those still there
                // when this one would enter take the places
                var inside = 0;
                string who = null;
                foreach (var a in st.Members)
                    if (!a.PastCore && a.Entry + a.Duration > entry + HeadwayToleranceSeconds)
                    {
                        inside++;
                        who = a.Mover.Id;
                    }

                if (inside >= st.Spec.Capacity) return "station " + st.Spec.Id + " is full (" + who + " is in it)";
                if (entry < st.LastEntry + st.Spec.HeadwaySeconds - HeadwayToleranceSeconds)
                    return string.Format(CultureInfo.InvariantCulture, "station {0} keeps its headway ({1:0.0} s to go)", st.Spec.Id, st.LastEntry + st.Spec.HeadwaySeconds - entry);
                if (st.Partner != null && !st.Partner.Away && !st.Partner.ReadyFor(entry - now))
                    return "station " + st.Spec.Id + " waits for " + (st.Spec.Partner != null ? st.Spec.Partner.Machine : "its partner") + " to be ready";
            }

            foreach (var c in r.Span)
            {
                var b = Blocking(m, c, r, false);
                if (b != null) return b.Item1;
            }

            return null;
        }

        /// <summary>Why the mover's request is not granted (null when it has none, or it may be): the words its timeline quotes.</summary>
        public string Explain(string id)
        {
            var m = Get(id);
            if (m.Pending == null) return null;
            foreach (var o in pending)
            {
                if (o == m.Pending) break;
                if (o.Closure.Overlaps(m.Pending.Closure) && !Inside(m, o)) return "after " + o.Mover.Id + ", which asked first for " + o.Name;
            }

            return Refusal(m.Pending);
        }

        // the station two movers are both members of, or -1; a request's stations count as the requester's own while it is being granted.
        // Two members of one station are timed apart on the one way through it: neither's reservation is in the other's way (each keeps to its
        // own holdings, and the one behind never takes the pose of the one in front), and in the core they are not held to each other's conflicts.
        int CoStation(Mover m, Mover n, Request asking)
        {
            for (var s = 0; s < stations.Length; s++)
            {
                var mine = Member(m, s) || (asking != null && asking.Stations.Contains(s));
                if (mine && Member(n, s)) return s;
            }

            return -1;
        }

        /// <summary>
        /// Why <paramref name="m"/> may not have <paramref name="c"/> (null when it may): (words, the mover in the way). With
        /// <paramref name="asking"/> the question is for a grant (the request's own stations count as joined); <paramref name="headRule"/> applies
        /// R5.
        /// </summary>
        Tuple<string, string> Blocking(Mover m, int c, Request asking, bool headRule)
        {
            foreach (var n in movers)
            {
                if (n == m) continue;
                var co = CoStation(m, n, asking);
                // a grant to a member is never kept back by another member: the two are timed apart, and taking each cell is still held to R2
                if (asking != null && co >= 0) continue;
                if (n.HeldSet.Contains(c))
                {
                    var behindIt = n.Held.IndexOf(c) < n.PoseAt;
                    // two members of a station timed apart may share core ground behind the one in front (never its pose or ahead of it); and the
                    // ground a machine going on its way has just left is no reason to refuse a grant to one that comes after it (it follows)
                    var shadowOnly = behindIt && ((co >= 0 && cells.Core(c) == co) || (asking != null && n.Pending == null && !n.Standing));
                    if (!shadowOnly) return Tuple.Create(Says(n, c, "in"), n.Id);
                }

                if (co < 0 && n.Reserved.Contains(c)) return Tuple.Create(Says(n, c, "reserved by"), n.Id);
                foreach (var x in cells.Conflicts(c))
                {
                    if (n.Occupied.Contains(x) && !(co >= 0 && cells.Core(c) == co && cells.Core(x) == co)) return Tuple.Create(Says(n, x, "crossing"), n.Id);
                    if (co < 0 && n.Reserved.Contains(x)) return Tuple.Create(Says(n, x, "reserved across by"), n.Id);
                }
            }

            if (headRule && pending.Count > 0)
            {
                var head = pending[0];
                if (head.Mover != m && head.Closure.Contains(c) && !Inside(m, head))
                    return Tuple.Create("held back for " + head.Mover.Id + "'s way through " + head.Name, head.Mover.Id);
            }

            return null;
        }

        string Says(Mover n, int cell, string how)
        {
            var box = cells.Box(cell);
            var where = box >= 0 ? topology.Junctions[box].Id : cells.Region(cell) >= 0 ? "station " + topology.Stations[cells.Region(cell)].Id : null;
            if (how == "in") return "behind " + n.Id + (where != null ? " (" + where + ")" : "");
            return how + " " + n.Id + (where != null ? " at " + where : "");
        }

        static bool Inside(Mover m, Request head)
        {
            foreach (var c in m.HeldSet)
                if (head.Closure.Contains(c)) return true;
            foreach (var c in m.Reserved)
                if (head.Closure.Contains(c)) return true;
            return false;
        }

        string BlockerOfRequest(Request r)
        {
            foreach (var c in r.Span)
            {
                var b = Blocking(r.Mover, c, r, false);
                if (b != null) return b.Item2;
            }

            foreach (var o in pending)
            {
                if (o == r) break;
                if (o.Closure.Overlaps(r.Closure) && !Inside(r.Mover, o)) return o.Mover.Id;
            }

            return null;
        }

        void Backstop()
        {
            foreach (var r in pending)
            {
                if (r.Reported || now - r.Since < BackstopSeconds) continue;
                var standing = new List<string>();
                var moving = false;
                foreach (var c in r.Span)
                {
                    var b = Blocking(r.Mover, c, r, false);
                    if (b == null) continue;
                    var n = byId[b.Item2];
                    if (n.Standing) { if (!standing.Contains(n.Id)) standing.Add(n.Id); }
                    else moving = true;
                }

                if (moving || standing.Count == 0) continue;
                r.Reported = true;
                Defects++;
                Defect?.Invoke(r.Mover.Id, standing[0], r.Name);
            }
        }

        void AddHeld(Mover m, int c, bool occupied)
        {
            m.Held.Add(c);
            m.HeldSet.Add(c);
            if (occupied) m.Occupied.Add(c);
        }

        void DropHeld(Mover m, int k)
        {
            var c = m.Held[k];
            m.Held.RemoveAt(k);
            if (!m.Held.Contains(c)) m.HeldSet.Remove(c);
            m.Occupied.Remove(c);
        }

        void Cancel(Mover m, bool keepHeld = false)
        {
            if (m.Pending != null)
            {
                pending.Remove(m.Pending);
                m.Pending = null;
            }

            if (keepHeld) return;
            m.Held.Clear();
            m.HeldSet.Clear();
            m.Occupied.Clear();
            m.Reserved.Clear();
        }

        // ------------------------------------------------------------------------------------------------ checks

        /// <summary>
        /// Every breach of R2 among the movers as they hold now: two movers holding one cell, or one occupying a cell that conflicts with one
        /// another occupies (members of one station in its core excepted). Empty when the rules held; the property tests read it every step.
        /// </summary>
        public List<string> Breaches()
        {
            var found = new List<string>();
            for (var i = 0; i < movers.Count; i++)
                for (var j = i + 1; j < movers.Count; j++)
                {
                    var a = movers[i];
                    var b = movers[j];
                    foreach (var c in a.HeldSet)
                        if (b.HeldSet.Contains(c))
                        {
                            var s = cells.Core(c);
                            var shared = s >= 0 && Member(a, s) && Member(b, s) && (a.Held.IndexOf(c) < a.PoseAt || b.Held.IndexOf(c) < b.PoseAt);
                            if (!shared) found.Add(a.Id + " and " + b.Id + " both hold " + cells.Name(c));
                        }

                    foreach (var c in a.Occupied)
                        foreach (var x in cells.Conflicts(c))
                            if (b.Occupied.Contains(x))
                            {
                                var s = cells.Core(c);
                                if (s >= 0 && cells.Core(x) == s && Member(a, s) && Member(b, s)) continue;
                                found.Add(a.Id + " in " + cells.Name(c) + " conflicts with " + b.Id + " in " + cells.Name(x));
                            }
                }

            return found;
        }

        public override string ToString()
        {
            var parts = movers.Select(m => m.Id + "@" + cells.Name(m.Held[m.PoseAt]) + "->" + cells.Name(m.Held[m.Held.Count - 1]) +
                                           (m.Pending != null ? " ?" + m.Pending.Name : "") + (m.Standing ? " standing" : ""));
            return string.Join("; ", parts) + string.Format(CultureInfo.InvariantCulture, " (t={0:0.0}, {1} waiting)", now, pending.Count);
        }
    }
}
