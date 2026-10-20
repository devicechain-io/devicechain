// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using DeviceChain.Sim.Traffic;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>
    /// The quarry's machines under the interlocking (<see cref="Interlocking"/>): how a haul truck on the loop, a truck on an errand and the
    /// loader coupled to the load point are told to the site-agnostic core, and how what the core allows becomes a track rate or a distance a
    /// truck may drive. The haul loop is the topology's loop lane, cut from the live fleet's track 0: a truck's track time IS its place on the
    /// loop (every loop cell carries the track time its start is reached), so a truck on its track is posed from its clock and slowed by setting
    /// the clock's rate.
    /// </summary>
    public sealed partial class SiteTraffic
    {
        /// <summary>How far ahead (seconds of its track) a truck on the loop holds: enough to stop in from its top speed, with a margin.</summary>
        public const double LookSeconds = 3.0;

        /// <summary>
        /// A truck on its track brakes for the end of what it holds as a heavy truck does (<see cref="BrakeMps2"/>), to stop
        /// <see cref="StopShortM"/> short of it: a truck whose way is clear keeps its own pace, and one held slows only as late as it must.
        /// </summary>
        public const double BrakeMps2 = 3.5, StopShortM = 0.3;

        /// <summary>The slowest a truck's own pace is taken to be when it brakes (m/s): a track standing still is not a reason to stop dead.</summary>
        public const double MinPaceMps = 0.5;

        /// <summary>How much of its pace (per second) a truck held back gets back as its way clears.</summary>
        public const double ResumePerSecond = 1.0;

        /// <summary>How early (seconds of its track) a coupled loader may be counted on to meet a truck: the rounding of two clocks.</summary>
        public const double PartnerToleranceSeconds = 0.05;

        readonly SiteTopology topology;
        readonly Interlocking interlocking;
        readonly Lane loop;
        readonly int[] loopPath;
        readonly double[] t0;
        readonly double[] arc;
        readonly double period;
        readonly Dictionary<string, Rider> riders = new Dictionary<string, Rider>(StringComparer.Ordinal);
        readonly List<Rider> order = new List<Rider>();
        readonly List<Partner> partners = new List<Partner>();
        readonly FleetView fleet;
        double now;

        /// <summary>A haul truck on the loop's track: its body, which lap it is on, and the rate its clock plays at.</summary>
        sealed class Rider
        {
            public string Id;
            public IMachineBody Body;
            public long Lap;
            public int Cell;
            public double Tau;
            public double Rate = 1.0;
            public bool OnLoop;

            /// <summary>The trip it is on (null when it only rides the loop), the plan it follows, and the stand it stands at (null when it does not).</summary>
            public LaneTrip Trip;
            public Plan TripPlan;
            public string Standing;

            /// <summary>How many cells into the loop leg it rides it is: counted as it goes, never read off the loop (a leg can be a whole lap).</summary>
            public int LegCells;
        }

        /// <summary>
        /// The loader coupled to a load station. Its track is one loading cycle: it works from the start of the cycle to its ready pose
        /// (<see cref="StationPartner.ReadySeconds"/>) and stands there to the end. In the choreography as laid out, a truck enters the core while
        /// the loader is at <see cref="EntryPhase"/> of its cycle; coupled, the loader keeps that relation to the truck in the core (never
        /// faster than its own pace), and with no truck there it goes on to its ready pose and waits.
        /// </summary>
        sealed class Partner : IStationPartner
        {
            public StationSpec Station;
            public IMachineBody Body;
            public double Period;
            public double EntryPhase;
            public double Lambda;
            public bool Attached;
            public bool Loading;

            public double Ready => Station.Partner.ReadySeconds;

            public bool Away => !Attached;

            public bool ReadyFor(double secondsToEntry)
            {
                if (!Attached) return true;
                // at or past the place in its cycle a truck is met at (it waits at its ready pose for one that is late), or on its way there in
                // time; the station's headway (its cycle) is what keeps it from being asked for a second truck while it loads the first
                if (Lambda >= EntryPhase - PartnerToleranceSeconds) return true;
                return EntryPhase - Lambda <= secondsToEntry + PartnerToleranceSeconds;
            }
        }

        /// <summary>
        /// The quarry's traffic over a measured site (<see cref="PreparedSite"/>): its topology, the conflicts of its cells, and the live fleet
        /// (its loop track's period and the offsets that say how the coupled loader's cycle meets the trucks). One per run.
        /// </summary>
        public SiteTraffic(PreparedSite site)
        {
            if (site == null) throw new ArgumentNullException(nameof(site));
            topology = site.Topology;
            fleet = site.Fleet;
            var cells = site.NewCellSpace();
            interlocking = new Interlocking(cells);
            loop = topology.Loop;
            period = fleet.Tracks[loop.Track >= 0 ? loop.Track : 0].Period;
            var n = loop.Cells.Count;
            loopPath = new int[n];
            t0 = new double[n + 1];
            arc = new double[n + 1];
            for (var k = 0; k < n; k++)
            {
                loopPath[k] = cells.Of(loop.Id, k);
                t0[k] = loop.Cells[k].T0;
                arc[k] = loop.ArcStarts[k];
            }

            t0[n] = period;
            arc[n] = loop.TotalLength;
        }

        public Interlocking Interlocking => interlocking;
        public SiteTopology Topology => topology;

        /// <summary>The loop's period, seconds of the track.</summary>
        public double Period => period;

        /// <summary>The track rate the interlocking last set a machine's clock to (1 for a machine it does not drive).</summary>
        public double RateOf(string id) => riders.TryGetValue(id, out var r) ? r.Rate : 1.0;

        // ------------------------------------------------------------------------------------------------ the loop's clock

        /// <summary>The loop cell a truck's track time is in.</summary>
        public int CellAtTime(double tau)
        {
            var t = Wrap(tau, period);
            int lo = 0, hi = loop.Cells.Count - 1;
            while (lo < hi)
            {
                var mid = (lo + hi + 1) / 2;
                if (t0[mid] <= t) lo = mid;
                else hi = mid - 1;
            }

            return lo;
        }

        /// <summary>The track time the start of loop cell <paramref name="k"/> is reached at.</summary>
        public double TimeOfCell(int k) => t0[k];

        /// <summary>How far into its cell (metres) a truck at track time tau is.</summary>
        double OffsetIn(int k, double tau)
        {
            var span = t0[k + 1] - t0[k];
            var f = span <= 1e-9 ? 0.0 : Math.Max(0.0, Math.Min(1.0, (Wrap(tau, period) - t0[k]) / span));
            return f * (arc[k + 1] - arc[k]);
        }

        static double Wrap(double v, double p) => ((v % p) + p) % p;

        // the absolute index of a rider's pose cell, and its clock unwrapped: laps of the loop since it was placed
        long AbsoluteIndex(Rider r) => r.Lap * loop.Cells.Count + r.Cell;

        double AbsoluteTime(Rider r) => r.Lap * period + r.Tau;

        double TimeOfIndex(long index)
        {
            var n = loop.Cells.Count;
            var lap = (long)Math.Floor((double)index / n);
            var k = (int)(index - lap * n);
            return lap * period + t0[k];
        }

        // how fast a truck on the loop goes at its own pace around cell k (m/s): the faster of its cell and the next
        double PaceAt(int k)
        {
            var n = loop.Cells.Count;
            var best = MinPaceMps;
            for (var j = 0; j < 2; j++)
            {
                var c = (k + j) % n;
                var span = t0[c + 1] - t0[c];
                if (span > 1e-6) best = Math.Max(best, (arc[c + 1] - arc[c]) / span);
            }

            return best;
        }

        double ArcOfTime(long lap, double tau)
        {
            var k = CellAtTime(tau);
            return lap * loop.TotalLength + arc[k] + OffsetIn(k, tau);
        }

        // a rider's place on its interlocking path: the loop's cyclic index, or on a trip its loop leg's place in the trip's path
        long PathIndex(Rider r)
        {
            if (r.TripPlan == null) return AbsoluteIndex(r);
            var l = r.TripPlan.Legs[r.Trip.CurrentLeg];
            // never past the leg's last cell: a clock that rounds a hair past the exit is at the exit (it comes off its track there)
            return l.PathStart + Math.Min(l.Count - 1, r.LegCells);
        }

        // seconds of its track until the end of what a rider holds, while that is on the loop it rides
        double HeldTime(Rider r)
        {
            if (r.TripPlan == null) return TimeOfIndex(interlocking.HeadIndex(r.Id) + 1) - AbsoluteTime(r);
            var l = r.TripPlan.Legs[r.Trip.CurrentLeg];
            var head = interlocking.HeadIndex(r.Id);
            if (head >= l.PathStart + l.Count - 1) return double.MaxValue;
            var n = loop.Cells.Count;
            var next = (int)((l.From + (head + 1 - l.PathStart)) % n);
            var d = t0[next] - r.Tau;
            return d < 0 ? d + period : d;
        }

        // seconds of its track until a rider on a trip reaches the end of its loop leg (the end of the exit's cell); no end off a trip
        double LegEndTime(Rider r)
        {
            if (r.TripPlan == null) return double.MaxValue;
            var l = r.TripPlan.Legs[r.Trip.CurrentLeg];
            if (!l.Loop) return double.MaxValue;
            var along = r.LegCells;
            if (along >= l.Count) return 0.0;
            if (along < l.Count - 1) return double.MaxValue;
            var end = (l.To + 1) % loop.Cells.Count;
            return Math.Max(0.0, (end == 0 ? period : t0[end]) - r.Tau);
        }

        // ------------------------------------------------------------------------------------------------ riders

        /// <summary>A haul truck that plays the loop: it is placed where its clock has it and from now on runs at the rate the interlocking allows.</summary>
        public void AddLoopTruck(IMachineBody body)
        {
            if (!body.TryTrackSeconds(out var tau)) throw new InvalidOperationException(body.Id + " is not on its track");
            var r = new Rider { Id = body.Id, Body = body, Tau = Wrap(tau, period), Cell = CellAtTime(tau), OnLoop = true };
            riders[r.Id] = r;
            order.Add(r);
            interlocking.Place(r.Id, loopPath, true, AbsoluteIndex(r), OffsetIn(r.Cell, r.Tau), i => TimeOfIndex(i) - AbsoluteTime(r));
        }

        /// <summary>
        /// The loader a load station is coupled to: from now on its clock follows the truck being loaded, and waits at its ready pose. Where its
        /// cycle meets a truck comes from the fleet as laid out: the loader's phase when a loop truck reaches the start of the core.
        /// </summary>
        public void AddPartner(IMachineBody body)
        {
            foreach (var st in topology.Stations)
            {
                if (st.Partner == null || st.Partner.Machine != body.Id) continue;
                FleetMachine me = null, truck = null;
                foreach (var m in fleet.Machines)
                {
                    if (m.Id == body.Id) me = m;
                    if (truck == null && m.Track == (loop.Track >= 0 ? loop.Track : 0)) truck = m;
                }

                if (me == null || truck == null) throw new InvalidOperationException("the live fleet has no " + body.Id + " or no loop truck to couple it to");
                var p = new Partner { Station = st, Body = body, Period = fleet.Tracks[me.Track].Period };
                // fleet time t poses a machine at (t - offset) of its track: the loader's phase when the truck is at the start of the core
                p.EntryPhase = Wrap(truck.Offset + t0[st.Core.A] - me.Offset, p.Period);
                if (p.EntryPhase > p.Ready)
                    throw new InvalidOperationException(body.Id + " meets its truck after its ready pose (" + p.EntryPhase.ToString("0.00", CultureInfo.InvariantCulture) + " s): the coupling waits at the ready pose, before the truck");
                p.Attached = body.TryTrackSeconds(out var lambda);
                p.Lambda = lambda;
                partners.Add(p);
                interlocking.SetPartner(st.Id, p);
            }
        }

        /// <summary>Whether the machine is the loader a station is coupled to.</summary>
        public bool IsPartner(string id)
        {
            foreach (var p in partners)
                if (p.Body.Id == id) return true;
            return false;
        }

        /// <summary>
        /// One step: every truck's pose is told to the interlocking from its clock, the interlocking grants and hands out cells, and every truck's
        /// clock is set to the rate that keeps it within what it holds for the step to come (<paramref name="dt"/> seconds); the coupled loader is
        /// set to follow its truck.
        /// </summary>
        public void Step(double time, double dt)
        {
            now = time;
            foreach (var p in partners) ObservePartner(p);
            foreach (var r in order)
            {
                if (!r.OnLoop || !r.Body.TryTrackSeconds(out var tau)) continue;
                tau = Wrap(tau, period);
                var k = CellAtTime(tau);
                if (k < r.Cell && r.Cell - k > loop.Cells.Count / 2) r.Lap++;
                else if (k > r.Cell && k - r.Cell > loop.Cells.Count / 2) r.Lap--;
                // on a trip, the cells it has gone on by (a clock only runs forward, by a few cells a step at most)
                if (r.TripPlan != null)
                {
                    var on = ((k - r.Cell) % loop.Cells.Count + loop.Cells.Count) % loop.Cells.Count;
                    if (on < loop.Cells.Count / 2) r.LegCells += on;
                }

                r.Cell = k;
                r.Tau = tau;
                // a clock never runs back; one that reads a hair behind the cell the truck was put in is still in that cell
                interlocking.Move(r.Id, Math.Max(PathIndex(r), interlocking.PoseIndex(r.Id)), OffsetIn(k, tau));
                // as far as its track takes it in the next few seconds, and never less than it needs to stop from its pace with a cell to spare
                // (a truck standing in a cell its track spends a long time in still holds the next one)
                var arcNow = ArcOfTime(r.Lap, tau);
                var ahead = tau + LookSeconds;
                var lapAhead = r.Lap + (long)Math.Floor(ahead / period);
                var pace = PaceAt(k);
                var stop = pace * pace / (2.0 * BrakeMps2) + StopShortM + 2.0 * topology.CellM;
                interlocking.SetWant(r.Id, Math.Max(stop, ArcOfTime(lapAhead, Wrap(ahead, period)) - arcNow));
            }

            interlocking.Step(time);

            foreach (var r in order)
            {
                if (!r.OnLoop || !r.Body.Attached) continue;
                // the end of what it holds, as metres ahead and as a track time (the start of the cell after its head)
                var limit = HeldTime(r);
                var room = interlocking.Authority(r.Id) - StopShortM;
                var allowed = Math.Sqrt(2.0 * BrakeMps2 * Math.Max(0.0, room));
                var target = Math.Min(1.0, allowed / PaceAt(r.Cell));
                var rate = Math.Min(target, r.Rate + ResumePerSecond * dt);
                // and never past the end of what it holds in the step to come; nor, on a trip, past the end of the exit's cell where it leaves
                if (dt > 0) rate = Math.Min(rate, Math.Max(0.0, (limit - 0.02) / dt));
                var legEnd = LegEndTime(r);
                if (dt > 0 && legEnd < double.MaxValue) rate = Math.Min(rate, Math.Max(0.0, legEnd / dt));
                r.Rate = Math.Max(0.0, Math.Min(1.0, rate));
                r.Body.SetTrackRate(r.Rate);
            }

            foreach (var p in partners) StepPartner(p, dt);
        }

        void ObservePartner(Partner p)
        {
            p.Attached = p.Body.TryTrackSeconds(out var lambda);
            p.Lambda = Wrap(lambda, p.Period);
            p.Loading = false;
            foreach (var (_, entered, _) in interlocking.Members(p.Station.Id))
                if (entered) p.Loading = true;
        }

        void StepPartner(Partner p, double dt)
        {
            ObservePartner(p);
            if (!p.Attached) return;
            double rate;
            Rider truck = null;
            foreach (var (id, entered, _) in interlocking.Members(p.Station.Id))
                if (entered && riders.TryGetValue(id, out var r) && r.OnLoop) { truck = r; break; }

            // with no truck in the core, or ahead of the one there: on to the ready pose, and wait
            var toReady = p.Lambda < p.Ready ? p.Ready - p.Lambda : 0.0;
            if (truck == null)
            {
                rate = toReady;
            }
            else
            {
                // the phase the truck's place in the core calls for: as far past where a truck is met as the truck is past the start of the core
                // where the truck will be when this step is played (its clock runs at the rate just set)
                var target = Wrap(p.EntryPhase + Wrap(truck.Tau + truck.Rate * dt - t0[p.Station.Core.A], period), p.Period);
                var behind = Wrap(target - p.Lambda, p.Period);
                rate = behind <= p.Period / 2 ? behind : toReady;
            }

            p.Body.SetTrackRate(dt > 0 ? Math.Max(0.0, Math.Min(1.0, rate / dt)) : 1.0);
        }

        /// <summary>Each coupled loader's clock against its ready pose, in words (for the timeline and the tests).</summary>
        public string PartnerState()
        {
            var parts = new List<string>();
            foreach (var p in partners)
                parts.Add(p.Body.Id + (p.Attached
                    ? string.Format(CultureInfo.InvariantCulture, " at {0:0.00} s of {1:0.00} (meets trucks at {2:0.00}, ready {3:0.00}){4}", p.Lambda, p.Period, p.EntryPhase, p.Ready, p.Loading ? " loading" : "")
                    : " away"));
            return string.Join("; ", parts);
        }
    }
}
