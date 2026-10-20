// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using DeviceChain.Sim.Traffic;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.Tasks
{
    public sealed partial class SiteTraffic
    {
        /// <summary>How fast a truck takes a stand's lanes, as a share of its cruise: they turn tightly onto and off the pads.</summary>
        public const double AccessFactor = 0.6;

        /// <summary>How long a free route is followed into a loop lane after it joins: room for a merge's grant to reach past the junction.</summary>
        const int LoopTail = 40;

        /// <summary>How far into the cell after the merge (seconds of the track) a truck is put back on its track.</summary>
        const double JoinIntoSeconds = 0.01;

        // ------------------------------------------------------------------------------------------------ where a haul truck can go

        /// <summary>The stand a haul truck parks at in a zone that nobody has, or null with the reason.</summary>
        StandPlace FreeStand(string zone, string id, out string refusal)
        {
            refusal = null;
            var any = false;
            foreach (var s in topology.Stands)
            {
                if (s.Role != "zone" || s.Zone != zone || !s.Kinds.Contains("Hauler")) continue;
                any = true;
                if (claims.TryGetValue(s.Id, out var holder) && holder != id) continue;
                return s;
            }

            refusal = any ? "no free place in " + zone + " off the working routes (its stand is taken)" : "no place in " + zone + " a haul truck can stand off the working routes";
            return null;
        }

        StandPlace Stand(string id)
        {
            foreach (var s in topology.Stands)
                if (s.Id == id) return s;
            throw new KeyNotFoundException("the topology has no stand " + id);
        }

        /// <summary>The stand a haul truck holds (one at a time), by stand id -> truck.</summary>
        readonly Dictionary<string, string> claims = new Dictionary<string, string>(StringComparer.Ordinal);

        void Claim(string stand, string id)
        {
            ReleaseStand(id);
            claims[stand] = id;
        }

        /// <summary>Gives back the stand a haul truck held.</summary>
        public void ReleaseStand(string id)
        {
            string held = null;
            foreach (var kv in claims)
                if (kv.Value == id) held = kv.Key;
            if (held != null) claims.Remove(held);
        }

        /// <summary>Whether the machine is one of the haul trucks that ride the loop (and so take the lanes on its errands).</summary>
        public bool IsLoopTruck(string id) => riders.ContainsKey(id);

        // ------------------------------------------------------------------------------------------------ a haul truck's trips

        /// <summary>One stretch of a trip: riding the loop on the truck's own track, or driving a lane's cells.</summary>
        sealed class Leg
        {
            public bool Loop;
            public string Lane;
            public int From, To;
            public int PathStart;
            public int Count;
            public Route Route;
            public double[] Starts;
        }

        /// <summary>A trip for a haul truck to a stand (or back onto its track), as the legs it takes and the interlocking path they make.</summary>
        sealed class Plan
        {
            public readonly List<Leg> Legs = new List<Leg>();
            public readonly List<int> Path = new List<int>();
            public StandPlace Stand;
            public bool EndsOnTrack;
            public int JoinCell = -1;
        }

        // where a truck is, for planning: the cell its pose is in as the interlocking keeps it (on the loop, part-way along a lane, or at the
        // end of a stand's lane), and on a lane how far into that cell its body is. The interlocking is the one record that is never stale: a
        // trip given up part-way along a lane leaves the truck there, holding that lane, and the next trip must be planned from there.
        void Where(Rider r, out string lane, out int cell, out double into)
        {
            var pose = interlocking.PoseCell(r.Id);
            lane = interlocking.Cells.LaneOf(pose);
            cell = interlocking.Cells.IndexOf(pose);
            into = 0.0;
            if (lane == loop.Id) return;
            // the body's place along the cell (a new route starts where the truck is, never back at the cell's start), short of its end
            var c = topology.Lane(lane).Cells[cell];
            var len = c.Length;
            if (len <= IntoMarginM) return;
            var f = ((r.Body.X - c.X0) * (c.X1 - c.X0) + (r.Body.Z - c.Z0) * (c.Z1 - c.Z0)) / (len * len);
            into = Math.Max(0.0, Math.Min(len - IntoMarginM, f * len));
        }

        /// <summary>How far short of the end of its cell a route that starts part-way along one starts at most, metres (a route needs a length).</summary>
        const double IntoMarginM = 0.05;

        /// <summary>
        /// The way from where a truck is to a stand (<paramref name="target"/>), or back onto its track (null): along its lane to the lane's end,
        /// through each stand it comes to and out by that stand's lane, onto the loop where a lane joins it, round the loop to the exit for the
        /// target and up its lane to the stand. Null when there is none.
        /// </summary>
        Plan PlanWay(Rider r, StandPlace target)
        {
            var plan = new Plan { Stand = target, EndsOnTrack = target == null };
            Where(r, out var lane, out var cell, out var into);
            var n = loop.Cells.Count;
            var guard = 0;
            while (guard++ < 16)
            {
                if (lane != loop.Id)
                {
                    var l = topology.Lane(lane);
                    AddLane(plan, lane, cell, l.Cells.Count - 1, into);
                    into = 0.0;
                    // the lane ends at a stand: it is the target, or the truck goes on out of it
                    StandPlace at = null;
                    foreach (var s in topology.Stands)
                        if (s.In == lane) at = s;
                    if (at != null)
                    {
                        if (target != null && at.Id == target.Id) return plan;
                        lane = at.Out;
                        cell = 0;
                        continue;
                    }

                    // the lane joins the loop: on to the cell after the one it merges with
                    LaneEntry entry = null;
                    foreach (var e in topology.Entries)
                        if (e.From == lane) entry = e;
                    if (entry == null) return null;
                    plan.JoinCell = (entry.Cell + 1) % n;
                    if (target == null)
                    {
                        // back on the track: the path runs on along the loop far enough for the merge's grant to reach past its junction
                        AddLoop(plan, plan.JoinCell, (plan.JoinCell + LoopTail) % n, true);
                        return plan;
                    }

                    lane = loop.Id;
                    cell = plan.JoinCell;
                    continue;
                }

                if (target == null) return plan;
                LaneExit exit = null;
                foreach (var e in topology.Exits)
                    if (e.To == target.In) exit = e;
                if (exit == null) return null;
                AddLoop(plan, cell, exit.Cell, false);
                lane = exit.To;
                cell = 0;
            }

            return null;
        }

        // a lane's cells from one to another; the route starts <paramref name="into"/> metres along the first (where the truck is), so the
        // leg's distances run from there and the first cell's start is behind it
        void AddLane(Plan plan, string lane, int from, int to, double into = 0.0)
        {
            var l = topology.Lane(lane);
            var leg = new Leg { Loop = false, Lane = lane, From = from, To = to, PathStart = plan.Path.Count, Count = to - from + 1 };
            var b = new RouteBuilder();
            var first = l.Cells[from];
            var f = first.Length > 0 ? into / first.Length : 0.0;
            b.Start(first.X0 + (first.X1 - first.X0) * f, first.Z0 + (first.Z1 - first.Z0) * f);
            leg.Starts = new double[leg.Count + 1];
            var sum = -into;
            for (var k = from; k <= to; k++)
            {
                leg.Starts[k - from] = sum;
                sum += l.Cells[k].Length;
                b.Leg(l.Cells[k].X1, l.Cells[k].Z1, AccessFactor, 0.0);
                plan.Path.Add(interlocking.Cells.Of(lane, k));
            }

            leg.Starts[leg.Count] = sum;
            leg.Route = b.Build();
            plan.Legs.Add(leg);
        }

        // the loop's cells from one to another (inclusive, round the end); a tail is path only, ridden as the truck's own track afterwards
        void AddLoop(Plan plan, int from, int to, bool tail)
        {
            var n = loop.Cells.Count;
            var count = ((to - from) % n + n) % n + 1;
            if (!tail) plan.Legs.Add(new Leg { Loop = true, From = from, To = to, PathStart = plan.Path.Count, Count = count });
            for (var i = 0; i < count; i++) plan.Path.Add(loopPath[(from + i) % n]);
        }

        // the loop's geometry along a plan, for the route drawn: the loop legs' cells and the lanes' lines, one polyline
        Route DrawnRoute(Plan plan)
        {
            var b = new RouteBuilder();
            var started = false;
            foreach (var leg in plan.Legs)
            {
                var cells = leg.Loop ? loop.Cells : topology.Lane(leg.Lane).Cells;
                var n = cells.Count;
                for (var i = 0; i < leg.Count; i++)
                {
                    var c = cells[(leg.From + i) % n];
                    if (!started)
                    {
                        b.Start(c.X0, c.Z0);
                        started = true;
                    }

                    b.Leg(c.X1, c.Z1, leg.Loop ? 1.0 : AccessFactor, 0.0);
                }
            }

            return started ? b.Build() : null;
        }

        /// <summary>A haul truck's trip to the stand of a zone, or null with the reason.</summary>
        public ITrip HaulToZone(string id, string zone, out string refusal)
        {
            var r = riders[id];
            var stand = FreeStand(zone, id, out refusal);
            if (stand == null) return null;
            return Begin(r, stand, "stand " + stand.Id + " of " + zone, out refusal, claim: true);
        }

        /// <summary>A haul truck's trip to the refuel queue (the bay's first stand), or null with the reason.</summary>
        public ITrip HaulToQueue(string id, out string refusal)
        {
            refusal = null;
            if (topology.Bay == null)
            {
                refusal = "this site has no refuel bay";
                return null;
            }

            return Begin(riders[id], Stand(topology.Bay.Queue), "the refuel queue", out refusal, claim: false);
        }

        /// <summary>A haul truck's drive from the queue into the bay.</summary>
        public ITrip HaulIntoBay(string id, out string refusal)
        {
            refusal = null;
            if (topology.Bay == null)
            {
                refusal = "this site has no refuel bay";
                return null;
            }

            return Begin(riders[id], Stand(topology.Bay.BayStand), "the refuel bay", out refusal, claim: false);
        }

        /// <summary>A haul truck's trip back onto its track: out of the stand it is at (or on along the lane it is on), and onto the loop through a merge.</summary>
        public ITrip HaulBack(string id, out string refusal)
        {
            var r = riders[id];
            ReleaseStand(id);
            if (r.Body.Attached && r.Trip == null && r.Standing == null)
            {
                refusal = null;
                return new LaneTrip(this, r, null, "its track");
            }

            return Begin(r, null, "its track", out refusal, claim: false);
        }

        ITrip Begin(Rider r, StandPlace target, string place, out string refusal, bool claim)
        {
            var plan = PlanWay(r, target);
            if (plan == null || plan.Path.Count == 0)
            {
                refusal = "no way on the lanes to " + place;
                return null;
            }

            refusal = null;
            if (claim) Claim(target.Id, r.Id);
            return new LaneTrip(this, r, plan, place);
        }

        /// <summary>
        /// A haul truck's trip on the lanes. Riding its track it is the interlocking's like any truck on the loop (its clock is held to what it
        /// holds), and it comes off its track at the exit at speed; on a lane it is driven along the lane's cells as far as it holds; where its lane
        /// joins the loop it is put back on its track at the cell after the merge; at a stand it stands.
        /// </summary>
        sealed class LaneTrip : ITrip
        {
            readonly SiteTraffic t;
            readonly Rider r;
            Plan plan;
            readonly RouteFollower follow = new RouteFollower();
            readonly Kinematics kin;
            int leg;
            bool begun, done;

            /// <summary>The leg the truck is on now.</summary>
            public int CurrentLeg => leg;

            public LaneTrip(SiteTraffic traffic, Rider rider, Plan plan, string place)
            {
                t = traffic;
                r = rider;
                this.plan = plan;
                Place = place;
                kin = Kinematics.For(rider.Body.Kind);
                Route = plan == null ? null : t.DrawnRoute(plan);
                done = plan == null;
            }

            public Route Route
            {
                get => done ? null : drawn;
                private set => drawn = value;
            }

            Route drawn;

            public string Place { get; }
            public bool EndsOnTrack => plan == null || plan.EndsOnTrack;
            public double Speed => done || plan == null || plan.Legs[leg].Loop ? 0.0 : follow.Speed;

            public double Metres
            {
                get
                {
                    if (plan == null) return 0.0;
                    var m = 0.0;
                    foreach (var c in plan.Path) m += t.interlocking.Cells.Length(c);
                    return m;
                }
            }

            public double EtaSeconds
            {
                get
                {
                    if (plan == null) return 0.0;
                    var s = 0.0;
                    foreach (var l in plan.Legs)
                        s += l.Loop ? t.LoopSeconds(l.From, l.To) : l.Starts[l.Count] / (kin.Cruise * AccessFactor);
                    return s;
                }
            }

            public string Waiting => done ? null : t.interlocking.Has(r.Id) ? t.Explain(r.Id) : null;

            /// <summary>
            /// A truck may be left standing only at the stand it reached (and riding its track it is not left at all: it rides on). A trip given
            /// up part-way along a lane is over too, but the truck is still on the lane: it may not stand there.
            /// </summary>
            public bool MayStandHere => atStand || (!begun && r.Standing != null);

            bool atStand;

            public bool Step(double dt)
            {
                if (done) return true;
                if (!begun)
                {
                    if (!t.Start(r, this, plan))
                    {
                        // it moved past where the trip was planned from (it rode on while the command came in): plan it again from here
                        var again = t.PlanWay(r, plan.Stand);
                        if (again == null || !t.Start(r, this, again)) return false;
                        plan = again;
                        Route = t.DrawnRoute(plan);
                    }

                    begun = true;
                    leg = 0;
                    if (!plan.Legs[0].Loop) StartLane(plan.Legs[0], r.Body.HeadingDegrees, 0.0);
                }

                var l = plan.Legs[leg];
                if (l.Loop)
                {
                    // riding its track: the step moves it; it comes off at the end of the exit's cell
                    if (!t.AtLegEnd(r, l)) return false;
                    var speed = t.PaceAt(r.Cell) * r.Rate;
                    r.Body.Detach();
                    r.OnLoop = false;
                    leg++;
                    StartLane(plan.Legs[leg], r.Body.HeadingDegrees, speed);
                    // its pose is the exit lane's now (it held it to come this far): a trip given up before the next step is planned from there
                    var lane = plan.Legs[leg];
                    if (t.interlocking.HeadIndex(r.Id) >= lane.PathStart) t.interlocking.Move(r.Id, lane.PathStart, 0.0);
                    return false;
                }

                var arrived = follow.Advance(dt, r.Body, kin, s => s + t.interlocking.Authority(r.Id));
                var at = arrived ? l.Starts[l.Count] - 1e-6 : follow.LineProgress;
                var k = t.CellAlong(l, at);
                var idx = (long)l.PathStart + k;
                var off = at - l.Starts[k];
                // at the very start of a lane that follows another (through a stand), the truck is still at the end of the cell before it: it
                // takes the lane's first cell only as it moves into it, never by having been put there
                if (k == 0 && off <= 0.0 && idx > t.interlocking.HeadIndex(r.Id) && idx > 0)
                {
                    idx--;
                    off = t.interlocking.Cells.Length(plan.Path[(int)idx]);
                }

                t.interlocking.Move(r.Id, Math.Max(idx, t.interlocking.PoseIndex(r.Id)), off);
                t.interlocking.SetWant(r.Id, t.LaneWant(follow.Speed));
                if (!arrived) return false;

                if (leg + 1 < plan.Legs.Count)
                {
                    leg++;
                    var next = plan.Legs[leg];
                    if (next.Loop)
                    {
                        t.Join(r, plan, follow.Speed, l.PathStart + l.Count);
                        return false;
                    }

                    StartLane(next, r.Body.HeadingDegrees, follow.Speed);
                    return false;
                }

                if (plan.EndsOnTrack)
                {
                    t.Join(r, plan, follow.Speed, l.PathStart + l.Count);
                    t.Finish(r);
                    done = true;
                    return true;
                }

                // at the stand
                t.StandAt(r, plan.Stand);
                done = true;
                atStand = true;
                return true;
            }

            void StartLane(Leg l, double heading, double speed)
            {
                follow.StartMoving(l.Route, heading, speed);
            }

            public void Abandon()
            {
                if (done) return;
                done = true;
                // riding the loop it rides on as if nothing had been asked; anywhere else it is never left: whoever gave it up drives it on
                if (!begun || plan.Legs[leg].Loop)
                {
                    if (begun) t.BackToLoop(r);
                    return;
                }

                t.Stop(r);
            }
        }

        // ------------------------------------------------------------------------------------------------ the trip's side of the step

        /// <summary>How long a truck takes round the loop from the start of cell <paramref name="from"/> to the end of cell <paramref name="to"/>.</summary>
        double LoopSeconds(int from, int to)
        {
            var n = loop.Cells.Count;
            var end = t0[(to + 1) % n == 0 ? n : (to + 1) % n];
            var d = end - t0[from];
            return d <= 0 ? d + period : d;
        }

        int CellAlong(Leg l, double s)
        {
            var k = 0;
            while (k < l.Count - 1 && l.Starts[k + 1] <= s) k++;
            return k;
        }

        // how far ahead a truck driven on a lane holds: what it needs to stop from its speed, and two cells more
        double LaneWant(double speed) => speed * speed / (2.0 * BrakeMps2) + StopShortM + 2.0 * topology.CellM + 4.0;

        // a trip begins: the truck's way through the interlocking is the trip's path from where it is. It was planned a step ago at most, from
        // where it was then; riding its track it may have gone on a cell or two since, which the path's first leg already holds. False when
        // it has gone past the start of the path (it rode past the exit it was planned to take): the trip is planned again from here.
        bool Start(Rider r, LaneTrip trip, Plan plan)
        {
            var pose = interlocking.PoseCell(r.Id);
            var at = plan.Path.IndexOf(pose);
            var first = plan.Legs[0];
            if (at < 0 || at >= first.Count) return false;
            r.Trip = trip;
            r.Standing = null;
            interlocking.SetStanding(r.Id, false);
            r.TripPlan = plan;
            r.LegCells = at;
            var offset = first.Loop ? OffsetIn(r.Cell, r.Tau) : -first.Starts[0];
            interlocking.Repath(r.Id, plan.Path, false, at, offset, i => TripEta(r, i));
            return true;
        }

        // whether a truck riding a loop leg has come to its end (the end of the exit's cell), so it leaves its track there
        bool AtLegEnd(Rider r, Leg l)
        {
            if (!r.OnLoop) return true;
            var along = Along(r, l);
            if (along >= l.Count) return true;
            if (along < l.Count - 1) return false;
            var end = (l.To + 1) % loop.Cells.Count;
            return (end == 0 ? period : t0[end]) - r.Tau <= 0.005;
        }

        // seconds until a trip's path index is reached
        double TripEta(Rider r, long index)
        {
            var plan = r.TripPlan;
            if (plan == null) return 0.0;
            var s = 0.0;
            var pose = interlocking.PoseIndex(r.Id);
            foreach (var l in plan.Legs)
            {
                var a = l.PathStart;
                var b = l.PathStart + l.Count - 1;
                if (index < a) break;
                var from = Math.Max(a, pose);
                var to = Math.Min(b, index);
                if (to < from) continue;
                if (l.Loop)
                {
                    var n = loop.Cells.Count;
                    var c0 = (int)((l.From + (from - a)) % n);
                    var c1 = (int)((l.From + (to - a)) % n);
                    var d = t0[c1] - (from == pose && r.OnLoop ? r.Tau : t0[c0]);
                    s += d < 0 ? d + period : d;
                }
                else
                {
                    s += (l.Starts[to - a] - l.Starts[from - a]) / (Kinematics.For(r.Body.Kind).Cruise * AccessFactor);
                }

                if (index <= b) break;
            }

            return s;
        }

        // a truck at the end of a lane that joins the loop is put back on its track at the cell after the merge, at the speed it came at
        void Join(Rider r, Plan plan, double speed, long joinIndex)
        {
            // a hundredth of a second into the cell: a clock that reads a hair short of the cell's start would read the cell before
            var c = loop.Cells[plan.JoinCell];
            r.Body.Attach(new TrackPoint(c.X0, c.Z0, c.HeadingDegrees, t0[plan.JoinCell] + JoinIntoSeconds));
            r.Body.TryTrackSeconds(out var tau);
            r.Tau = Wrap(tau, period);
            r.Cell = CellAtTime(r.Tau);
            r.OnLoop = true;
            r.LegCells = ((r.Cell - plan.JoinCell) % loop.Cells.Count + loop.Cells.Count) % loop.Cells.Count;
            r.Rate = Math.Max(0.0, Math.Min(1.0, speed / PaceAt(r.Cell)));
            r.Body.SetTrackRate(r.Rate);
            // its pose is the loop's now: the cell after the merge, which its grant reached
            interlocking.Move(r.Id, joinIndex, OffsetIn(r.Cell, r.Tau));
        }

        // the trip is over and the truck is a truck on the loop again: its path is the loop, going round
        void Finish(Rider r)
        {
            r.Lap = 0;
            interlocking.Repath(r.Id, loopPath, true, AbsoluteIndex(r), OffsetIn(r.Cell, r.Tau), i => TimeOfIndex(i) - AbsoluteTime(r));
            r.Trip = null;
            r.TripPlan = null;
        }

        // a trip given up while the truck rode its track: it rides on
        void BackToLoop(Rider r)
        {
            if (r.OnLoop) Finish(r);
        }

        // a trip given up on a lane: the truck stops where it is, still holding its place on the lane (the controller drives it on, or back,
        // and the next trip is planned from that place: see Where)
        void Stop(Rider r)
        {
            r.Trip = null;
            r.TripPlan = null;
        }

        void StandAt(Rider r, StandPlace stand)
        {
            r.Trip = null;
            r.TripPlan = null;
            r.Standing = stand.Id;
            interlocking.SetStanding(r.Id, true);
        }

        /// <summary>Why a mover is not moving, in words: what its request waits for, or what it is behind.</summary>
        public string Explain(string id) => !interlocking.Has(id) ? null : interlocking.Explain(id) ?? interlocking.WaitingFor(id);
    }
}
