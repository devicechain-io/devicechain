// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>
    /// Every machine's task layer and the site they share: the route network, the one refuel bay, the
    /// parking, the timeline, and the yield rule (stop while another machine's footprint is within
    /// <see cref="YieldDistance"/> metres ahead in the same lane). Commands reach it through
    /// <see cref="ITaskSink"/>; the app steps it once a frame, before the scene's own choreography moves the
    /// machines that are still on their tracks.
    ///
    /// A command for a machine that has no controller (the plant, an unknown device) is answered failed on
    /// the spot, and a command that arrives after <see cref="FailAll"/> is too: nothing is ever left
    /// waiting for an answer that will not come.
    /// </summary>
    public sealed class TaskDirector : ITaskSink, ITaskWorld
    {
        public const double YieldDistance = 8.0, LaneHalfWidth = 3.0, BayClearRadius = 5.0;

        /// <summary>
        /// A machine rejoining its track waits while another that is driving is within <see cref="RejoinMovingRadius"/> metres of the place it
        /// would rejoin at (a truck on its routine track follows the track blind, and a machine set down on one in step with it travels
        /// through it), or another of any kind is within <see cref="RejoinStandingRadius"/> of it (a hauler's footprint).
        /// </summary>
        public const double RejoinMovingRadius = 24.0, RejoinStandingRadius = 8.0;

        /// <summary>A machine slower than this (m/s) is standing.</summary>
        public const double StandingSpeed = 0.2;

        /// <summary>How long a machine waits behind one that stands in its lane before it carries on past it, in simulation seconds.</summary>
        public const double StandingYieldSeconds = 10.0;

        readonly Dictionary<string, (double X, double Z)> lastSeen = new Dictionary<string, (double X, double Z)>(StringComparer.Ordinal);
        readonly Dictionary<string, double> speeds = new Dictionary<string, double>(StringComparer.Ordinal);
        readonly Dictionary<string, double> waitingSince = new Dictionary<string, double>(StringComparer.Ordinal);
        double now;

        readonly Dictionary<string, MachineController> controllers = new Dictionary<string, MachineController>(StringComparer.Ordinal);
        readonly Dictionary<string, IMachineBody> bodies = new Dictionary<string, IMachineBody>(StringComparer.Ordinal);
        readonly List<string> order = new List<string>();
        readonly SiteGeometry site;
        readonly BayReservations bay = new BayReservations();
        readonly ParkingLot parking = new ParkingLot();
        readonly int generation;
        bool reset;

        public TaskDirector(SiteGeometry site, RouteGraph graph, Timeline timeline, IEnumerable<(IMachineBody body, MachineModel model)> machines, int generation)
        {
            this.site = site ?? throw new ArgumentNullException(nameof(site));
            Graph = graph ?? throw new ArgumentNullException(nameof(graph));
            Timeline = timeline ?? throw new ArgumentNullException(nameof(timeline));
            this.generation = generation;
            foreach (var (body, model) in machines)
            {
                if (model.IsPlant) continue;
                bodies[body.Id] = body;
                order.Add(body.Id);
                controllers[body.Id] = new MachineController(body, model, site, graph, bay, parking, timeline, this);
            }
        }

        public RouteGraph Graph { get; }
        public Timeline Timeline { get; }
        public BayReservations Bay => bay;
        public IReadOnlyList<string> Machines => order;

        public MachineController this[string id] => controllers[id];

        /// <summary>
        /// Raised, after a step, when a machine starts driving a route (the route), changes to another, or stops driving (null). The route
        /// highlight and the recording listen here, so the highlight can be drawn again from the recording.
        /// </summary>
        public event Action<string, Route> RouteChanged;

        readonly Dictionary<string, Route> routesAnnounced = new Dictionary<string, Route>(StringComparer.Ordinal);

        public bool TryController(string id, out MachineController controller) => controllers.TryGetValue(id, out controller);

        public void Submit(string externalId, TaskRequest request)
        {
            if (reset || request.Generation != generation)
            {
                request.Complete(TaskResult.Fail(TaskReasons.Reset));
                return;
            }

            if (!controllers.TryGetValue(externalId, out var c))
            {
                request.Complete(TaskResult.Fail(TaskReasons.NoExecutor));
                return;
            }

            c.Submit(request);
        }

        public void Refused(string externalId, string text) => Timeline.Add(externalId, TimelineKinds.Refused, text);

        /// <summary>One step of every machine. <paramref name="simDt"/> is simulation seconds, <paramref name="wallDt"/> real seconds.</summary>
        public void Step(double simDt, double wallDt)
        {
            if (reset) return;

            // how fast everybody has been going since the last step, wherever the motion came from (a track or a drive)
            foreach (var id in order)
            {
                var b = bodies[id];
                if (simDt > 0 && lastSeen.TryGetValue(id, out var was))
                    speeds[id] = Math.Sqrt((b.X - was.X) * (b.X - was.X) + (b.Z - was.Z) * (b.Z - was.Z)) / simDt;
                lastSeen[id] = (b.X, b.Z);
            }

            now += simDt;

            // a bay held by a machine that is no longer in the middle of refuelling is not held
            var holder = bay.Holder;
            if (holder != null && (!controllers.TryGetValue(holder, out var h) || !h.WantsBay)) bay.Release(holder);
            foreach (var queued in new List<string>(bay.Queue))
                if (!controllers.TryGetValue(queued, out var q) || !q.WantsBay) bay.Release(queued);

            foreach (var id in order) controllers[id].Step(simDt, wallDt);
            AnnounceRoutes();
        }

        void AnnounceRoutes()
        {
            foreach (var id in order)
            {
                var route = controllers[id].CurrentRoute;
                routesAnnounced.TryGetValue(id, out var was);
                if (ReferenceEquals(route, was)) continue;
                routesAnnounced[id] = route;
                RouteChanged?.Invoke(id, route);
            }
        }

        /// <summary>The run is ending: every running task is answered with <paramref name="reason"/>, and later commands are too. Returns how many tasks it answered.</summary>
        public int FailAll(string reason = TaskReasons.Reset)
        {
            reset = true;
            var answered = 0;
            foreach (var id in order) answered += controllers[id].FailAll(reason);
            return answered;
        }

        public bool IsReset => reset;

        /// <summary>The machine in the Refuelling state right now (what the bay's attendant serves), or null.</summary>
        public string Servicing
        {
            get
            {
                foreach (var id in order)
                    if (controllers[id].Phase == TaskPhase.Refuelling) return id;
                return null;
            }
        }

        // ---------------------------------------------------------------- the world, as the controllers see it

        /// <summary>
        /// Another machine's footprint is within <see cref="YieldDistance"/> ahead in the lane. A machine that is moving is
        /// always yielded to. One that is standing (parked, serviced, stalled, waiting its turn) is yielded to for at most
        /// <see cref="StandingYieldSeconds"/>: after that the machine behind carries on, so nothing waits forever
        /// behind a machine that is not going anywhere. (A machine nothing has been seen to do yet counts as moving.)
        /// </summary>
        public bool Blocked(string id, double x, double z, double headingDegrees)
        {
            var h = headingDegrees * Math.PI / 180.0;
            double fx = Math.Sin(h), fz = Math.Cos(h);
            var moving = false;
            var standing = false;
            foreach (var kv in bodies)
            {
                if (kv.Key == id) continue;
                var dx = kv.Value.X - x;
                var dz = kv.Value.Z - z;
                var ahead = dx * fx + dz * fz;
                if (ahead <= 0 || ahead > YieldDistance) continue;
                var across = Math.Abs(dx * fz - dz * fx);
                if (across >= LaneHalfWidth) continue;
                if (!speeds.TryGetValue(kv.Key, out var v) || v >= StandingSpeed) moving = true;
                else standing = true;
            }

            if (moving)
            {
                waitingSince.Remove(id);
                return true;
            }

            if (!standing)
            {
                waitingSince.Remove(id);
                return false;
            }

            if (!waitingSince.TryGetValue(id, out var since))
            {
                waitingSince[id] = now;
                return true;
            }

            return now - since < StandingYieldSeconds;
        }

        public bool BayClear(string id)
        {
            if (!site.Spots.TryGetValue(RouteGraph.BaySpot, out var at)) return true;
            return !Occupied(id, at.X, at.Z, BayClearRadius);
        }

        public bool Occupied(string id, double x, double z) => Occupied(id, x, z, ParkingLot.ClearRadius);

        public bool TrackClear(string id, double x, double z)
        {
            foreach (var kv in bodies)
            {
                if (kv.Key == id) continue;
                var dx = kv.Value.X - x;
                var dz = kv.Value.Z - z;
                var d2 = dx * dx + dz * dz;
                if (d2 < RejoinStandingRadius * RejoinStandingRadius) return false;
                // a machine nothing has been seen to do yet counts as moving
                var moving = !speeds.TryGetValue(kv.Key, out var v) || v >= StandingSpeed;
                if (moving && d2 < RejoinMovingRadius * RejoinMovingRadius) return false;
            }

            return true;
        }

        bool Occupied(string id, double x, double z, double radius)
        {
            foreach (var kv in bodies)
            {
                if (kv.Key == id) continue;
                var dx = kv.Value.X - x;
                var dz = kv.Value.Z - z;
                if (dx * dx + dz * dz < radius * radius) return true;
            }

            return false;
        }
    }
}
