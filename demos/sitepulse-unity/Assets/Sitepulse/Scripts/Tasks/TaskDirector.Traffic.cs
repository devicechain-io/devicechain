// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>
    /// How the machines on the site keep out of each other's way, whoever is driving them. A quarry runs on following distance: a truck
    /// does not drive into the one in front, a machine stops for what it would meet, and the one that has to get past something standing
    /// in the road swerves round it. Every rule here is about FOOTPRINTS (<see cref="Footprint"/>, the ones the choreography is checked
    /// against), never about centres, so what it keeps clear is what is drawn.
    ///
    /// <list type="bullet">
    /// <item>A machine on its routine track cannot look, so the director looks for it: it reads where the track will have the machine in
    /// the next few seconds and slows its track down, to a stop, for anything that is ahead of it in that way, and for the way a machine
    /// on an errand is going to take (<see cref="StepTracks"/>). It holds a following distance of <see cref="TrackStandoff"/> metres of
    /// clear air. The machines on their tracks give way, early, to the one on an errand: that one then has the room to go round.</item>
    /// <item>A machine driving a route (<see cref="Steer"/>) stops for what is standing in its way or comes at it, takes the line round
    /// what is there with room to spare (the machines on their tracks are read where they will be when it gets there), and when it has stood
    /// a while facing what it cannot pass it backs up out of the way.</item>
    /// </list>
    ///
    /// Nothing here is random: the same positions give the same answer. What is remembered is each track's rate, and each errand's
    /// wait.
    /// </summary>
    public sealed partial class TaskDirector
    {
        /// <summary>The clear air a machine on its routine track keeps from whatever is ahead of it, in metres (the choreography keeps two haul trucks about 3 m apart, so a little under that is the nearest it ever asks).</summary>
        public const double TrackStandoff = 2.5;

        /// <summary>The clear air a machine driving a route stops short of whatever it would meet, and keeps from what it passes, in metres.</summary>
        public const double DriveStandoff = 0.6, SwerveClearance = 1.0;

        /// <summary>Two machines on their tracks are in each other's way when their footprints, at the same moment of both tracks, touch.</summary>
        public const double TrackTouch = 0.02;

        /// <summary>
        /// A track is read ahead of the machine on it, in seconds of the track: a conflict nearer than the time it takes to stop (at
        /// <see cref="TrackDecel"/>, and <see cref="TrackMarginSeconds"/> more) holds it, one more than <see cref="TrackSlowSeconds"/> beyond that
        /// does not slow it, and in between it slows in proportion. It gets going again at <see cref="TrackResume"/> of its pace per second.
        /// </summary>
        public const double TrackSampleSeconds = 0.1, TrackMarginSeconds = 0.5, TrackSlowSeconds = 1.5, TrackResume = 0.5;

        /// <summary>A conflict this near (seconds of the track, and metres of air) is a touch about to happen: the machine does not brake, it stops.</summary>
        const double ImminentSeconds = 0.25, ImminentGap = 0.3;

        /// <summary>Where the air a machine is held to is only the touch itself (a loader's boom comes down in a moment), the moment is longer.</summary>
        const double ImminentTouchSeconds = 0.6;

        /// <summary>How hard a held track machine brakes, in metres per second squared: a heavy truck stopping in its own length.</summary>
        public const double TrackDecel = 3.5;

        /// <summary>The air a machine taken off its track must find round it, in metres.</summary>
        public const double DetachClearance = 0.3;

        /// <summary>The air a machine put back on its track must find round it, in metres.</summary>
        public const double RejoinClearance = 1.0;

        /// <summary>The air under which a step is not taken (see <see cref="WouldTouch"/>), in metres.</summary>
        const double TouchGap = 0.05;

        /// <summary>A machine on an errand stops for anything it is about to touch, whatever the right of way, when it is this near (metres) and this close (metres of air).</summary>
        const double NearMetres = 1.5, TouchStopGap = 0.3;

        /// <summary>A machine moving is swept forward this far in time, for the machines that would cross its way.</summary>
        const double SweepSeconds = 1.5;

        /// <summary>The seconds of a machine's track that others are read against (every <see cref="PredictStepSeconds"/>), and the metres of a route (every <see cref="PlanStepMetres"/>).</summary>
        const double PredictSeconds = 8.0, PredictStepSeconds = 0.5, PlanMetres = 48.0, PlanStepMetres = 4.0;

        /// <summary>How long (seconds) either side of the time a machine on an errand is expected at a place a machine on its track is held off it.</summary>
        const double PlanWindowSeconds = 1.5;

        /// <summary>How far ahead of itself a machine looks for what to swerve round, and the sideways steps it tries, in metres.</summary>
        const double SwerveLookMetres = 30.0, SwerveStepMetres = 1.5, SwerveMaxMetres = 7.5, SampleMetres = 0.5, SwerveSampleMetres = 2.0;

        /// <summary>The pace a machine on an errand is assumed to keep, in metres per second, when it works out when it will be where.</summary>
        const double PlanSpeed = 3.0;

        sealed class Mover
        {
            public string Id;
            public int Order;
            public EquipmentKind Kind;
            public double X, Z, H, Speed;
            public bool Attached, Moving;

            /// <summary>Where it stands, and (moving) where it would be a moment on.</summary>
            public readonly FootprintShape[] Shapes = new FootprintShape[3];
            public int ShapeCount;

            /// <summary>A machine on its track and under way: where the track will have it, every <see cref="PredictStepSeconds"/>.</summary>
            public readonly FootprintShape[] Predicted = new FootprintShape[(int)(PredictSeconds / PredictStepSeconds) + 1];
            public int PredictedCount;

            /// <summary>A machine driving a route: where it is going to be, every <see cref="PlanStepMetres"/>, and when.</summary>
            public readonly FootprintShape[] Plan = new FootprintShape[(int)(PlanMetres / PlanStepMetres)];
            public readonly double[] PlanAt = new double[(int)(PlanMetres / PlanStepMetres)];
            public int PlanCount;
        }

        readonly Dictionary<string, Mover> movers = new Dictionary<string, Mover>(StringComparer.Ordinal);
        readonly List<Mover> candidates = new List<Mover>();
        readonly HashSet<string> unsynced = new HashSet<string>(StringComparer.Ordinal), wasOff = new HashSet<string>(StringComparer.Ordinal);
        bool anyDetached;
        readonly Dictionary<string, double> trackRates = new Dictionary<string, double>(StringComparer.Ordinal);

        // the gap each machine was at when the machine now being read started (indexed by roster order)
        double[] gapNow = new double[0];

        /// <summary>How fast the machine's routine track is playing, from 0 (held) to 1 (its own pace). 1 for a machine that is not on its track.</summary>
        public double TrackRateOf(string id) => trackRates.TryGetValue(id, out var r) ? r : 1.0;

        // Where every machine is, as it stands at the start of a step, and what it covers; where a machine on its track will be; and where a
        // machine on an errand is going.
        void Observe()
        {
            if (gapNow.Length < order.Count) gapNow = new double[order.Count];
            anyDetached = false;
            foreach (var id in order)
                if (!bodies[id].Attached) { anyDetached = true; break; }
            for (var i = 0; i < order.Count; i++)
            {
                var id = order[i];
                var b = bodies[id];
                if (!movers.TryGetValue(id, out var m)) movers[id] = m = new Mover { Id = id, Order = i, Kind = b.Kind };
                m.X = b.X;
                m.Z = b.Z;
                m.H = b.HeadingDegrees;
                m.Attached = b.Attached;
                m.Speed = speeds.TryGetValue(id, out var v) ? v : 0.0;
                m.Moving = !speeds.ContainsKey(id) || m.Speed >= StandingSpeed;
                m.Shapes[0] = Footprint.At(m.Kind, m.X, m.Z, m.H, b.BoomRaised);
                m.ShapeCount = 1;
                m.PredictedCount = 0;
                m.PlanCount = 0;
                if (m.Moving && m.Speed > 0)
                {
                    var h = m.H * Math.PI / 180.0;
                    double fx = Math.Sin(h), fz = Math.Cos(h);
                    for (var k = 1; k <= 2; k++)
                    {
                        var d = m.Speed * SweepSeconds * k / 2.0;
                        m.Shapes[k] = Footprint.At(m.Kind, m.X + fx * d, m.Z + fz * d, m.H, b.BoomRaised);
                    }

                    m.ShapeCount = 3;
                }

                if (m.Attached && m.Moving && anyDetached)
                {
                    for (var t = 0.0; m.PredictedCount < m.Predicted.Length; t += PredictStepSeconds)
                    {
                        if (!b.TryTrackPoseAhead(t, out var x, out var z, out var h, out var boom)) break;
                        m.Predicted[m.PredictedCount++] = Footprint.At(m.Kind, x, z, h, boom);
                    }
                }
                else if (!m.Attached && controllers.TryGetValue(id, out var c) && c.CurrentRoute != null)
                {
                    var route = c.CurrentRoute;
                    var pace = Math.Max(PlanSpeed, m.Speed);
                    for (var d = PlanStepMetres; m.PlanCount < m.Plan.Length && c.RouteProgress + d <= route.Length; d += PlanStepMetres)
                    {
                        PoseOn(route, c.RouteProgress, d, c.RouteLateral, c.RouteLateral, out var x, out var z, out var h);
                        m.PlanAt[m.PlanCount] = d / pace;
                        m.Plan[m.PlanCount++] = Footprint.At(m.Kind, x, z, h);
                    }
                }
            }
        }

        static double Gap(in FootprintShape me, Mover other)
        {
            var best = double.MaxValue;
            for (var k = 0; k < other.ShapeCount; k++)
                best = Math.Min(best, Footprint.Gap(me, other.Shapes[k]));
            return best;
        }

        /// <summary>
        /// Whether a machine has to give way to another that it comes within the standoff of at this pose. It does only when going on
        /// brings it nearer than it is now (<paramref name="gap"/> against <paramref name="gapNow"/>): one already past it, or leaving it,
        /// is not in its way. Anything standing is given way to, and so is anything of the other kind (a machine on its track and one on an
        /// errand each give way to the other, which leaves each of them the room to go round); two of a kind that cross or meet (not a queue)
        /// give way by roster order, so that one of them always goes on.
        /// </summary>
        static bool GiveWay(double gap, double gapNow, double standoff, double headingDegrees, bool onTrack, int order, double oHeading, bool oMoving, bool oOnTrack, int oOrder)
        {
            if (gap >= standoff || gap >= gapNow - 0.005) return false;
            if (!oMoving || onTrack != oOnTrack) return true;
            if (Math.Abs(RouteFollower.Delta(oHeading, headingDegrees)) < 60.0) return true;
            return oOrder < order;
        }

        // ---------------------------------------------------------------- a machine on its routine track

        void StepTracks(double simDt)
        {
            foreach (var id in order)
            {
                var body = bodies[id];
                if (!body.Attached)
                {
                    trackRates.Remove(id);
                    wasOff.Add(id);
                    continue;
                }

                var rate = trackRates.TryGetValue(id, out var r) ? r : 1.0;
                // put back on its track by the task layer: no longer at the moment it was laid out at
                if (wasOff.Remove(id)) unsynced.Add(id);
                // the machine's pace along its track, in metres of the track per second
                var pace = 1.0;
                if (body.TryTrackPoseAhead(0.0, out var x0, out var z0, out _, out _) && body.TryTrackPoseAhead(0.5, out var x1, out var z1, out _, out _))
                    pace = Math.Max(0.5, Math.Sqrt((x1 - x0) * (x1 - x0) + (z1 - z0) * (z1 - z0)) / 0.5);
                // seconds of the track the machine needs to stop from here: its speed squared over twice the braking, over its pace
                var stopSeconds = pace * rate * rate / (2.0 * TrackDecel);
                var target = TrackTarget(movers[id], body, stopSeconds + TrackMarginSeconds, pace * rate, out var imminent);
                rate = target > rate ? Math.Min(target, rate + TrackResume * simDt) : Math.Max(target, rate - TrackDecel / pace * simDt);
                // about to touch, whatever the ramp says: it stops now
                if (imminent) rate = 0.0;
                trackRates[id] = rate;
                if (rate < 0.999) unsynced.Add(id);
                body.SetTrackRate(rate);
            }
        }

        // about to touch, whoever has the road: closer than the air given, and getting closer, within the next moment of the track
        static bool Touching(double t, double gap, double gapNow, double air) =>
            t <= (air <= TrackTouch ? ImminentTouchSeconds : ImminentSeconds) && gap < air && gap < gapNow - 0.001;

        // 1 when nothing is in the way in the next few seconds of the track; less as something nears; 0 when it is about to be reached
        double TrackTarget(Mover me, IMachineBody body, double hardSeconds, double speedNow, out bool imminent)
        {
            imminent = false;
            var air = ImminentGap + speedNow * ImminentSeconds;
            var look = hardSeconds + TrackSlowSeconds;

            // who is worth looking at at all: whatever is driving a route and near; and, of the machines on their tracks, only those that
            // are no longer at the moment of the choreography they were laid out at (one that has never been held up is exactly where it
            // was designed to be, and so is every other that has not)
            candidates.Clear();
            var meMoved = unsynced.Contains(me.Id);
            foreach (var o in movers.Values)
            {
                if (o == me) continue;
                double dx = o.X - me.X, dz = o.Z - me.Z;
                if (o.Attached)
                {
                    if ((me.Kind != EquipmentKind.Hauler && o.Kind != EquipmentKind.Hauler) || !(meMoved || unsynced.Contains(o.Id))) continue;
                    if (dx * dx + dz * dz > 90.0 * 90.0) continue;
                }
                else if (dx * dx + dz * dz > 110.0 * 110.0) continue;
                candidates.Add(o);
            }

            if (candidates.Count == 0) return 1.0;
            for (var t = 0.0; t <= look + 1e-9; t += TrackSampleSeconds)
            {
                if (!body.TryTrackPoseAhead(t, out var x, out var z, out var h, out var boom)) return 1.0;
                var shape = Footprint.At(me.Kind, x, z, h, boom);
                foreach (var o in candidates)
                {
                    bool conflict;
                    if (o.Attached)
                    {
                        // Two machines on their tracks were laid out clear of each other, at the same moment of both tracks. One held up
                        // (or put back on the loop) is no longer at that moment, so each looks for the other where it will be when it
                        // gets there, and a loader or dozer works round a hauler that has been held but not round another of its own kind.
                        var oRate = trackRates.TryGetValue(o.Id, out var rr) ? rr : 1.0;
                        if (!bodies[o.Id].TryTrackPoseAhead(t * oRate, out var ox, out var oz, out var oh, out var oBoom)) continue;
                        double ex = ox - x, ez = oz - z;
                        if (ex * ex + ez * ez > 60.0 * 60.0) continue;
                        var gap = Footprint.Gap(shape, Footprint.At(o.Kind, ox, oz, oh, oBoom));
                        if (t == 0.0) gapNow[o.Order] = gap;
                        var haulers = o.Kind == EquipmentKind.Hauler && me.Kind == EquipmentKind.Hauler;
                        if (Touching(t, gap, gapNow[o.Order], haulers ? air : TrackTouch)) { imminent = true; return 0.0; }
                        conflict = haulers
                            ? GiveWay(gap, gapNow[o.Order], TrackStandoff, h, true, me.Order, oh, oRate > 0.05, true, o.Order)
                            : gap < TrackTouch;
                    }
                    else
                    {
                        double dx = o.X - x, dz = o.Z - z;
                        if (dx * dx + dz * dz > 80.0 * 80.0) continue;
                        var gap = Gap(shape, o);
                        if (t == 0.0) gapNow[o.Order] = gap;
                        if (Touching(t, gap, gapNow[o.Order], air)) { imminent = true; return 0.0; }
                        conflict = GiveWay(gap, gapNow[o.Order], TrackStandoff, h, true, me.Order, o.H, o.Moving, false, o.Order);
                        // and the way it is going, when it would be there: the machine on its track waits for it, which leaves it the room
                        for (var k = 0; !conflict && k < o.PlanCount; k++)
                            conflict = Math.Abs(t - o.PlanAt[k]) <= PlanWindowSeconds && Footprint.Gap(shape, o.Plan[k]) < TrackStandoff;
                    }

                    if (conflict) return Math.Max(0.0, Math.Min(1.0, (t - hardSeconds) / TrackSlowSeconds));
                }
            }

            return 1.0;
        }

        // ---------------------------------------------------------------- a machine driving a route

        /// <summary>
        /// How long a machine goes nowhere (less than <see cref="StalledMetres"/> in that time) while something holds it before it backs up,
        /// and how far it will back, in seconds and metres.
        /// </summary>
        public const double ReverseAfterSeconds = 6.0, StalledMetres = 2.0, ReverseMaxMetres = 30.0;

        readonly Dictionary<string, (double S, double At)> progress = new Dictionary<string, (double S, double At)>(StringComparer.Ordinal);
        readonly Dictionary<string, double> lastHeld = new Dictionary<string, double>(StringComparer.Ordinal);
        readonly Dictionary<string, double> backedFrom = new Dictionary<string, double>(StringComparer.Ordinal);

        /// <inheritdoc />
        public SteerAdvice Steer(string id, EquipmentKind kind, Route route, double s, double speed, double lateral)
        {
            if (!movers.TryGetValue(id, out var me) || route == null || route.Legs == 0) return default;
            var goal = ChooseLateral(me, kind, route, s, lateral, speed, out var passable);
            var held = StopAhead(me, kind, route, s, speed, lateral, goal);
            if (held) lastHeld[id] = now;
            var backing = backedFrom.TryGetValue(id, out var from);

            // a machine that is held and going nowhere (it may creep up and stop again, which is still one wait)
            if (!progress.TryGetValue(id, out var mark) || Math.Abs(s - mark.S) >= StalledMetres)
                progress[id] = mark = (s, now);
            var heldRecently = lastHeld.TryGetValue(id, out var last) && now - last < 2.0;
            if (!heldRecently && !backing)
            {
                progress[id] = (s, now);
                return new SteerAdvice(false, goal);
            }

            if (!backing && now - mark.At < ReverseAfterSeconds) return new SteerAdvice(held, goal);

            // it cannot pass what is in front of it and it is not going anywhere: it backs up the way it came, over to the side, until it can
            if (!backing) backedFrom[id] = from = s;
            if (passable && backing && from - s > 3.0)
            {
                backedFrom.Remove(id);
                progress[id] = (s, now);
                return new SteerAdvice(false, goal);
            }

            var side = SideToBackTo(me, kind, route, s, lateral, speed);
            if (s <= 0.5 || from - s >= ReverseMaxMetres || !RoomBehind(me, kind, route, s, lateral, side)) return new SteerAdvice(held, goal);
            return new SteerAdvice(true, side, true);
        }

        // where the machine would be d metres on, having swerved toward goal as fast as it can
        static void PoseOn(Route route, double s, double d, double lateral, double goal, out double x, out double z, out double heading)
        {
            route.PointAt(s + d, out var px, out var pz, out heading, out _);
            var step = RouteFollower.SwerveMetresPerMetre * Math.Abs(d);
            var lat = lateral + Math.Max(-step, Math.Min(step, goal - lateral));
            var h = heading * Math.PI / 180.0;
            x = px + Math.Cos(h) * lat;
            z = pz - Math.Sin(h) * lat;
        }

        bool StopAhead(Mover me, EquipmentKind kind, Route route, double s, double speed, double lateral, double goal)
        {
            var look = 2.5 + speed * 0.5 + speed * speed / 5.0;
            var here = Footprint.At(kind, me.X, me.Z, me.H);
            foreach (var o in movers.Values)
                if (o != me) gapNow[o.Order] = Gap(here, o);
            for (var d = 0.0; d <= look + 1e-9 && s + d <= route.Length + 1e-9; d += SampleMetres)
            {
                PoseOn(route, s, d, lateral, goal, out var x, out var z, out var h);
                var shape = Footprint.At(kind, x, z, h);
                foreach (var o in movers.Values)
                {
                    if (o == me) continue;
                    double dx = o.X - x, dz = o.Z - z;
                    if (dx * dx + dz * dz > 50.0 * 50.0) continue;
                    var gap = Gap(shape, o);
                    // whoever has the road, a machine about to touch another does not go on
                    if (d <= NearMetres && gap < TouchStopGap && gap < gapNow[o.Order] - 0.001) return true;
                    if (GiveWay(gap, gapNow[o.Order], DriveStandoff, h, false, me.Order, o.H, o.Moving, o.Attached, o.Order)) return true;
                }
            }

            return false;
        }

        /// <summary>The machine can be taken off its track where it stands: what it covers with its implements stowed has room.</summary>
        bool DetachClear(string id)
        {
            if (!movers.TryGetValue(id, out var me)) return true;
            var stowed = Footprint.At(me.Kind, me.X, me.Z, me.H);
            foreach (var o in movers.Values)
            {
                if (o == me) continue;
                double dx = o.X - me.X, dz = o.Z - me.Z;
                if (dx * dx + dz * dz > 30.0 * 30.0) continue;
                if (Gap(stowed, o) < DetachClearance) return false;
            }

            return true;
        }

        /// <inheritdoc />
        public bool TrackClearFor(string id, EquipmentKind kind, double x, double z, double headingDegrees)
        {
            if (!TrackClear(id, x, z)) return false;
            var there = Footprint.At(kind, x, z, headingDegrees);
            foreach (var o in movers.Values)
            {
                if (o.Id == id) continue;
                double dx = o.X - x, dz = o.Z - z;
                if (dx * dx + dz * dz > 30.0 * 30.0) continue;
                if (Gap(there, o) < RejoinClearance) return false;
            }

            return true;
        }

        /// <inheritdoc />
        public bool WouldTouch(string id, EquipmentKind kind, double x, double z, double headingDegrees)
        {
            if (!movers.TryGetValue(id, out var me)) return false;
            var there = Footprint.At(kind, x, z, headingDegrees);
            var here = Footprint.At(kind, me.X, me.Z, me.H);
            foreach (var o in movers.Values)
            {
                if (o == me) continue;
                double dx = o.X - x, dz = o.Z - z;
                if (dx * dx + dz * dz > 30.0 * 30.0) continue;
                var gap = Footprint.Gap(there, o.Shapes[0]);
                if (gap < TouchGap && gap < Footprint.Gap(here, o.Shapes[0]) - 0.001) return true;
            }

            return false;
        }

        // The line to take: its own, unless something stands on it (or comes down it) in the next few tens of metres, and then the
        // nearest to where it is now that clears whatever there is by SwerveClearance, to the right or left.
        double ChooseLateral(Mover me, EquipmentKind kind, Route route, double s, double lateral, double speed, out bool passable)
        {
            passable = true;
            var horizon = Math.Min(SwerveLookMetres, route.Length - s - RouteFollower.OnLineMetres);
            if (horizon <= 0) return 0.0;
            var near = Nearby(me, route, s);
            if (near == null) return 0.0;
            var pace = Math.Max(PlanSpeed, speed);
            if (Clear(me, kind, route, s, lateral, 0.0, horizon, near, pace)) return 0.0;
            for (var k = 1; k * SwerveStepMetres <= SwerveMaxMetres + 1e-9; k++)
            {
                // nearest the line it is on first, then the right-hand side
                var a = k * SwerveStepMetres;
                var first = Math.Abs(a - lateral) <= Math.Abs(-a - lateral) ? a : -a;
                if (Clear(me, kind, route, s, lateral, first, horizon, near, pace)) return first;
                if (Clear(me, kind, route, s, lateral, -first, horizon, near, pace)) return -first;
            }

            passable = false;
            return lateral;
        }

        List<Mover> Nearby(Mover me, Route route, double s)
        {
            route.PointAt(s, out var px, out var pz, out _, out _);
            List<Mover> near = null;
            foreach (var o in movers.Values)
            {
                if (o == me) continue;
                double dx = o.X - px, dz = o.Z - pz;
                // a machine on its track can be a long way off and still be there when the machine gets to it
                var reach = o.Attached && o.Moving ? SwerveLookMetres + 60.0 : SwerveLookMetres + 15.0;
                if (dx * dx + dz * dz > reach * reach) continue;
                (near ??= new List<Mover>()).Add(o);
            }

            return near;
        }

        bool Clear(Mover me, EquipmentKind kind, Route route, double s, double lateral, double goal, double horizon, List<Mover> near, double pace)
        {
            for (var d = SwerveSampleMetres; d <= horizon + 1e-9; d += SwerveSampleMetres)
            {
                PoseOn(route, s, d, lateral, goal, out var x, out var z, out var h);
                var shape = Footprint.At(kind, x, z, h);
                foreach (var o in near)
                {
                    if (o.Attached && o.Moving && o.PredictedCount > 0)
                    {
                        // where it will be when the machine gets there (a little either side: neither of them keeps to the minute)
                        var at = (int)Math.Round(d / pace / PredictStepSeconds);
                        for (var k = Math.Max(0, at - 2); k <= Math.Min(o.PredictedCount - 1, at + 2); k++)
                            if (Footprint.Gap(shape, o.Predicted[k]) < SwerveClearance) return false;
                        continue;
                    }

                    // one going the same way ahead of it is followed, not passed
                    if (o.Moving && Math.Abs(RouteFollower.Delta(o.H, h)) < 60.0 && Ahead(x, z, h, o)) continue;
                    if (Gap(shape, o) < SwerveClearance) return false;
                }
            }

            return true;
        }

        // nothing within reach of the machine's tail as it backs
        bool RoomBehind(Mover me, EquipmentKind kind, Route route, double s, double lateral, double goal)
        {
            for (var d = 0.5; d <= 5.0; d += 0.5)
            {
                PoseOn(route, s, -d, lateral, goal, out var x, out var z, out var h);
                var shape = Footprint.At(kind, x, z, h);
                foreach (var o in movers.Values)
                {
                    if (o == me) continue;
                    double dx = o.X - x, dz = o.Z - z;
                    if (dx * dx + dz * dz > 40.0 * 40.0) continue;
                    if (Gap(shape, o) < DriveStandoff * 0.5) return false;
                }
            }

            return true;
        }

        // the side to back off to: the right-hand one, unless the left is the one that is clear of what is in the way
        double SideToBackTo(Mover me, EquipmentKind kind, Route route, double s, double lateral, double speed)
        {
            var back = Math.Max(0.0, s - 12.0);
            var side = SwerveMaxMetres;
            var near = Nearby(me, route, back);
            if (near == null) return side;
            var horizon = Math.Min(SwerveLookMetres, route.Length - back - RouteFollower.OnLineMetres);
            var pace = Math.Max(PlanSpeed, speed);
            if (Clear(me, kind, route, back, side, side, horizon, near, pace)) return side;
            if (Clear(me, kind, route, back, -side, -side, horizon, near, pace)) return -side;
            return side;
        }

        static bool Ahead(double x, double z, double headingDegrees, Mover o)
        {
            var h = headingDegrees * Math.PI / 180.0;
            return (o.X - x) * Math.Sin(h) + (o.Z - z) * Math.Cos(h) > 0.5;
        }
    }
}
