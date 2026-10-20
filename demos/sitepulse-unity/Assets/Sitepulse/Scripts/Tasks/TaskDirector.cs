// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sim.Traffic;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.Tasks
{
    /// <summary>
    /// Every machine's task layer and the site they share: the interlocking that keeps them out of each other's way (<see cref="SiteTraffic"/>),
    /// the route network, the one refuel bay, the parking, and the timeline. Commands reach it through <see cref="ITaskSink"/>; the app steps it
    /// once a frame, before the scene's own choreography moves the machines that are still on their tracks.
    ///
    /// The haul trucks ride the loop under the interlocking, and their errands run on the site's lanes: to an exit at speed, up a stand's lane,
    /// and back onto the loop through a merge. Every other machine works in its own area, and an errand of one is a free route that must stay
    /// off the lanes altogether (<see cref="OffLaneWay"/>), or it is refused with the reason. Nothing waits on a guess about where anything will be.
    ///
    /// A command for a machine that has no controller (the plant, an unknown device) is answered failed on the spot, and a command that arrives
    /// after <see cref="FailAll"/> is too: nothing is ever left waiting for an answer that will not come.
    /// </summary>
    public sealed class TaskDirector : ITaskSink, ITaskWorld
    {
        public const double BayClearRadius = 5.0;

        /// <summary>A machine slower than this (m/s) is standing.</summary>
        public const double StandingSpeed = 0.2;

        readonly Dictionary<string, (double X, double Z)> lastSeen = new Dictionary<string, (double X, double Z)>(StringComparer.Ordinal);
        readonly Dictionary<string, double> speeds = new Dictionary<string, double>(StringComparer.Ordinal);
        double now;

        readonly Dictionary<string, MachineController> controllers = new Dictionary<string, MachineController>(StringComparer.Ordinal);
        readonly Dictionary<string, IMachineBody> bodies = new Dictionary<string, IMachineBody>(StringComparer.Ordinal);
        readonly List<string> order = new List<string>();
        readonly SiteGeometry site;
        readonly BayReservations bay = new BayReservations();
        readonly ParkingLot parking = new ParkingLot();
        readonly FreeTrips free;
        readonly SiteTraffic traffic;
        readonly OffLaneWay offLane;
        readonly Dictionary<string, Polygon> workAreas = new Dictionary<string, Polygon>(StringComparer.Ordinal);
        readonly int generation;
        bool reset;

        public TaskDirector(SiteGeometry site, RouteGraph graph, PreparedSite prepared, Timeline timeline, IEnumerable<(IMachineBody body, MachineModel model)> machines, int generation)
        {
            this.site = site ?? throw new ArgumentNullException(nameof(site));
            Graph = graph ?? throw new ArgumentNullException(nameof(graph));
            Timeline = timeline ?? throw new ArgumentNullException(nameof(timeline));
            if (prepared == null) throw new ArgumentNullException(nameof(prepared), "the task layer runs under the interlocking: it needs the measured site");
            this.generation = generation;
            free = new FreeTrips(site, graph, parking);
            traffic = new SiteTraffic(prepared);
            foreach (var w in prepared.Topology.WorkAreas) workAreas[w.Machine] = w.Polygon;
            offLane = new OffLaneWay(prepared, Working, Standing);

            foreach (var (body, model) in machines)
            {
                if (model.IsPlant) continue;
                bodies[body.Id] = body;
                order.Add(body.Id);
                controllers[body.Id] = new MachineController(body, model, bay, timeline, this);
                if (body.Kind == EquipmentKind.Hauler && body.Attached && body.TryTrackSeconds(out _)) traffic.AddLoopTruck(body);
            }

            foreach (var id in order)
                foreach (var st in prepared.Topology.Stations)
                    if (st.Partner != null && st.Partner.Machine == id && bodies[id].Attached) traffic.AddPartner(bodies[id]);
        }

        public RouteGraph Graph { get; }
        public Timeline Timeline { get; }
        public BayReservations Bay => bay;
        public IReadOnlyList<string> Machines => order;

        /// <summary>The interlocking over the site's machines and lanes.</summary>
        public SiteTraffic Traffic => traffic;

        public MachineController this[string id] => controllers[id];

        /// <summary>How fast the machine has been going since the last step, wherever the motion came from; 0 until it has been seen to move.</summary>
        public double SpeedOf(string id) => speeds.TryGetValue(id, out var v) ? v : 0.0;

        /// <summary>How fast the machine's routine track is playing, from 0 (held) to 1 (its own pace). 1 for a machine the interlocking does not hold back.</summary>
        public double TrackRateOf(string id) => traffic.RateOf(id);

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
            // the interlocking first: every machine's way for this step is settled before any of them is driven
            traffic.Step(now, simDt);

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

        // ---------------------------------------------------------------- the trips, as the controllers ask for them

        bool OnLanes(IMachineBody body) => traffic.IsLoopTruck(body.Id);

        public ITrip TripToArea(IMachineBody body, string area, double laneShare, out string refusal) =>
            OnLanes(body) ? traffic.HaulToZone(body.Id, area, out refusal)
                : free.ToArea(body, area, laneShare, offLane, (x, z) => Occupied(body.Id, x, z, ParkingLot.ClearRadius), Avoid(body.Id), out refusal);

        public ITrip TripToQueue(IMachineBody body, double laneShare, out string refusal) =>
            OnLanes(body) ? traffic.HaulToQueue(body.Id, out refusal) : free.ToSpot(body, RouteGraph.QueueSpot, "the refuel queue", laneShare, offLane, out refusal);

        public ITrip TripIntoBay(IMachineBody body, out string refusal) =>
            OnLanes(body) ? traffic.HaulIntoBay(body.Id, out refusal) : free.IntoBay(body, offLane, out refusal);

        public ITrip TripBack(IMachineBody body, double laneShare, out string refusal) =>
            OnLanes(body) ? traffic.HaulBack(body.Id, out refusal) : free.Back(body, laneShare, offLane, out refusal);

        public void Release(string id)
        {
            free.Release(id);
            traffic.ReleaseStand(id);
        }

        public bool BayClear(string id)
        {
            if (!site.Spots.TryGetValue(RouteGraph.BaySpot, out var at)) return true;
            return !Occupied(id, at.X, at.Z, BayClearRadius);
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

        /// <summary>
        /// What a free route's planner keeps clear of besides the site's own outlines: every machine standing off its track, and the ground every
        /// other machine at work on its track covers (its work area, as the circle round it). <see cref="OffLaneWay"/> checks the whole route.
        /// </summary>
        IReadOnlyList<Obstacle> Avoid(string id)
        {
            var list = new List<Obstacle>(site.Obstacles);
            foreach (var (machine, x, z, r) in Standing(id)) list.Add(Obstacle.Capsule(machine, x, z, x, z, r));
            foreach (var (machine, area) in Working(id))
            {
                Geometry.CenterRadius(new[] { area }, out var cx, out var cz, out var r);
                list.Add(Obstacle.Capsule(machine + " at work", cx, cz, cx, cz, r));
            }

            return list;
        }

        // every other machine at work on its track, and the area it works
        IEnumerable<(string Machine, Polygon Area)> Working(string id)
        {
            foreach (var kv in bodies)
                if (kv.Key != id && kv.Value.Attached && workAreas.TryGetValue(kv.Key, out var area)) yield return (kv.Key, area);
        }

        // every other machine standing off its track: where, and the circle it may cover facing any way
        IEnumerable<(string Machine, double X, double Z, double Radius)> Standing(string id)
        {
            foreach (var kv in bodies)
                if (kv.Key != id && !kv.Value.Attached) yield return (kv.Key, kv.Value.X, kv.Value.Z, ParkingLot.FootprintRadius(kv.Value.Kind));
        }
    }
}
