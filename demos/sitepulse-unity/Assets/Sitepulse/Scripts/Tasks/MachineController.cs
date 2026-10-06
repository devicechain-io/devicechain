// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Globalization;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.Tasks
{
    public enum MachineMode
    {
        /// <summary>On its routine track.</summary>
        Working,
        /// <summary>Off its track, running a command.</summary>
        Commanded,
        /// <summary>Off its track, driving back to it.</summary>
        Returning,
        /// <summary>Off its track and standing, because a command sent it there.</summary>
        Parked,
        /// <summary>Off its track and standing, because the tank ran dry.</summary>
        Stalled,
    }

    public enum TaskPhase { None, ToDestination, ToQueue, WaitingForBay, EnteringBay, Refuelling }

    /// <summary>What a machine's controller asks of the rest of the site.</summary>
    public interface ITaskWorld
    {
        /// <summary>Another machine's footprint is within the yield distance ahead of this one, in its lane.</summary>
        bool Blocked(string id, double x, double z, double headingDegrees);

        /// <summary>Nothing but <paramref name="id"/> is standing in the refuel bay.</summary>
        bool BayClear(string id);

        /// <summary>Something other than <paramref name="id"/> stands within parking-clear distance of the point.</summary>
        bool Occupied(string id, double x, double z);
    }

    /// <summary>The budgets a command's local execution is held to (design 4.3), in simulation time with a wall-clock hard cap.</summary>
    public static class TaskBudgets
    {
        public const double EtaFactor = 2.0;
        public const double AreaSlackSeconds = 60.0, AreaWallCapSeconds = 600.0;
        public const double BayWaitSeconds = 120.0, RefuelWallCapSeconds = 900.0;
        public const double ReturnWallCapSeconds = 600.0;

        /// <summary>The longest any one command can be held, for the handler's own last-resort answer.</summary>
        public const double LongestWallCapSeconds = RefuelWallCapSeconds;
    }

    /// <summary>
    /// One machine's task layer. A command arrives (already validated), the arbiter says whether it wins,
    /// and the machine leaves its routine track and drives it: <c>goto-area</c> to a free parking slot of a
    /// zone, where it stays; <c>goto-refuel</c> to the queue, into the bay when it is its turn, through the
    /// Refuelling service, and then back to its own track. Every command ends with an answer and a reason:
    /// carried out, refused, superseded, out of fuel, over budget or reset.
    ///
    /// Main thread only, no network and no scene types: the body, the world and the clock are handed in, so
    /// the whole of it runs in an EditMode test. The only way this class changes the tank is
    /// <see cref="RefuelService"/>, and it only does so in <see cref="TaskPhase.Refuelling"/>.
    /// </summary>
    public sealed class MachineController
    {
        readonly string id;
        readonly MachineModel model;
        readonly IMachineBody body;
        readonly SiteGeometry site;
        readonly RouteGraph graph;
        readonly BayReservations bay;
        readonly ParkingLot parking;
        readonly Timeline timeline;
        readonly ITaskWorld world;
        readonly TaskArbiter arbiter = new TaskArbiter();
        readonly RouteFollower follow = new RouteFollower();
        readonly Kinematics kin;

        TaskRequest active;
        RefuelService service;
        ParkingSlot? slot;
        TrackPoint returnPoint;
        double elapsedSim, elapsedWall, budgetSim, capWall, bayWait, returnWall;
        bool reset;

        public MachineController(IMachineBody body, MachineModel model, SiteGeometry site, RouteGraph graph, BayReservations bay, ParkingLot parking, Timeline timeline, ITaskWorld world)
        {
            this.body = body ?? throw new ArgumentNullException(nameof(body));
            this.model = model ?? throw new ArgumentNullException(nameof(model));
            this.site = site ?? throw new ArgumentNullException(nameof(site));
            this.graph = graph ?? throw new ArgumentNullException(nameof(graph));
            this.bay = bay ?? throw new ArgumentNullException(nameof(bay));
            this.parking = parking ?? throw new ArgumentNullException(nameof(parking));
            this.timeline = timeline ?? throw new ArgumentNullException(nameof(timeline));
            this.world = world ?? throw new ArgumentNullException(nameof(world));
            id = body.Id;
            kin = Kinematics.For(body.Kind);
            Mode = body.Attached ? MachineMode.Working : MachineMode.Parked;
        }

        public string Id => id;
        public MachineMode Mode { get; private set; }
        public TaskPhase Phase { get; private set; }
        public TaskRequest Running => active;
        public double SpeedMps => follow.Speed;

        /// <summary>The bay is held or waited for by this machine.</summary>
        public bool WantsBay => Phase == TaskPhase.WaitingForBay || Phase == TaskPhase.EnteringBay || Phase == TaskPhase.Refuelling;

        static string F0(double v) => v.ToString("0", CultureInfo.InvariantCulture);
        static string F1(double v) => v.ToString("0.0", CultureInfo.InvariantCulture);

        static string Short(string token) => token == null ? "?" : token.Length > 12 ? token.Substring(0, 12) : token;

        /// <summary>One line on what the machine is doing, for the timeline panel's header.</summary>
        public string Status()
        {
            switch (Mode)
            {
                case MachineMode.Working: return "working its track";
                case MachineMode.Returning: return "returning to its track";
                case MachineMode.Parked: return "parked";
                case MachineMode.Stalled: return "stalled: out of fuel";
                default:
                    var key = active != null ? active.Key : "?";
                    switch (Phase)
                    {
                        case TaskPhase.ToDestination: return key + ": driving to " + active.Area;
                        case TaskPhase.ToQueue: return key + ": driving to the refuel queue";
                        case TaskPhase.WaitingForBay: return key + ": waiting for the bay";
                        case TaskPhase.EnteringBay: return key + ": entering the bay";
                        case TaskPhase.Refuelling: return key + ": refuelling " + F0(service != null ? service.ElapsedSeconds : 0) + "/" + F0(RefuelService.DurationSeconds) + " s";
                        default: return key;
                    }
            }
        }

        // ---------------------------------------------------------------- a command arrives

        /// <summary>A validated command reaches the machine. It always ends up answered, now or later.</summary>
        public void Submit(TaskRequest request)
        {
            if (request == null) throw new ArgumentNullException(nameof(request));
            timeline.Add(id, TimelineKinds.Received, request.Key + (request.Area != null ? " " + request.Area : "") + " (" + Short(request.Token) + ")");
            if (reset)
            {
                request.Complete(TaskResult.Fail(TaskReasons.Reset));
                timeline.Add(id, TimelineKinds.Reset, TaskReasons.Reset);
                return;
            }

            var decision = arbiter.Offer(request);
            if (!decision.Accepted)
            {
                timeline.Add(id, TimelineKinds.Superseded, decision.Reason);
                request.Complete(TaskResult.Fail(decision.Reason));
                return;
            }

            if (decision.Superseded != null)
            {
                var reason = TaskReasons.SupersededBy(request.Token);
                timeline.Add(id, TimelineKinds.Superseded, decision.Superseded.Key + " " + Short(decision.Superseded.Token) + " " + reason);
                decision.Superseded.Complete(TaskResult.Fail(reason));
                ReleaseTask();
            }

            if (model.FuelPct <= 0.0)
            {
                Refuse(request, "out of fuel");
                return;
            }

            if (request.Key == CommandKeys.GotoArea) BeginArea(request);
            else if (request.Key == CommandKeys.GotoRefuel) BeginRefuel(request);
            else Refuse(request, "unknown command " + request.Key);
        }

        void Refuse(TaskRequest request, string reason)
        {
            timeline.Add(id, TimelineKinds.Refused, reason);
            request.Complete(TaskResult.Fail(reason));
            arbiter.Finished(request);
            parking.Release(id);
            slot = null;
            ReleaseTask();
            if (Mode == MachineMode.Commanded) Mode = MachineMode.Parked;
        }

        void TakeOver(TaskRequest request, Route route, double shownMetres, double eta, double budget, double cap, string label)
        {
            if (body.Attached) body.Detach();
            active = request;
            Mode = MachineMode.Commanded;
            elapsedSim = elapsedWall = bayWait = 0;
            budgetSim = budget;
            capWall = cap;
            follow.Start(route, body.HeadingDegrees);
            timeline.Add(id, TimelineKinds.Accepted, label + ": route " + F0(shownMetres) + " m, ETA " + F0(eta) + " s");
        }

        void BeginArea(TaskRequest request)
        {
            if (!site.TryZone(request.Area, out var zone))
            {
                Refuse(request, "no scene geometry for area " + request.Area);
                return;
            }

            var claimed = parking.Claim(id, zone, body.X, body.Z, (x, z) => world.Occupied(id, x, z));
            if (claimed == null)
            {
                Refuse(request, "no free parking slot in " + request.Area);
                return;
            }

            var route = graph.Plan(body.X, body.Z, claimed.Value.X, claimed.Value.Z);
            if (route == null)
            {
                Refuse(request, "no route to " + request.Area);
                return;
            }

            slot = claimed;
            var eta = route.Eta(kin.Cruise);
            Phase = TaskPhase.ToDestination;
            TakeOver(request, route, route.Length, eta, TaskBudgets.EtaFactor * eta + TaskBudgets.AreaSlackSeconds, TaskBudgets.AreaWallCapSeconds, request.Area);
        }

        void BeginRefuel(TaskRequest request)
        {
            if (!site.Spots.TryGetValue(RouteGraph.QueueSpot, out var queue) || !site.Spots.TryGetValue(RouteGraph.BaySpot, out var bayAt))
            {
                Refuse(request, "this site has no refuel bay");
                return;
            }

            var route = graph.Plan(body.X, body.Z, queue.X, queue.Z);
            if (route == null)
            {
                Refuse(request, "no route to the refuel queue");
                return;
            }

            parking.Release(id);
            slot = null;
            var into = Route.Straight(queue.X, queue.Z, bayAt.X, bayAt.Z, SpeedModel.BayApproachFactor);
            var eta = route.Eta(kin.Cruise) + into.Eta(kin.Cruise);
            Phase = TaskPhase.ToQueue;
            TakeOver(request, route, route.Length + into.Length, eta, TaskBudgets.EtaFactor * eta + TaskBudgets.BayWaitSeconds + RefuelService.DurationSeconds,
                TaskBudgets.RefuelWallCapSeconds, "refuel bay");
        }

        // ---------------------------------------------------------------- time passes

        /// <summary>One step. <paramref name="simDt"/> is simulation seconds, <paramref name="wallDt"/> real seconds.</summary>
        public void Step(double simDt, double wallDt)
        {
            if (reset) return;
            switch (Mode)
            {
                case MachineMode.Working:
                    if (model.FuelPct <= 0.0 && body.Attached)
                    {
                        body.Detach();
                        Mode = MachineMode.Stalled;
                        timeline.Add(id, TimelineKinds.Stalled, "out of fuel: stopped where it stood");
                    }

                    break;
                case MachineMode.Commanded:
                    StepTask(simDt, wallDt);
                    break;
                case MachineMode.Returning:
                    StepReturn(simDt, wallDt);
                    break;
            }
        }

        bool Drive(double dt) => follow.Advance(dt, body, kin, (x, z, h) => world.Blocked(id, x, z, h));

        void StepTask(double simDt, double wallDt)
        {
            if (active == null)
            {
                Mode = MachineMode.Parked;
                return;
            }

            elapsedSim += simDt;
            elapsedWall += wallDt;
            if (elapsedWall > capWall)
            {
                Fail("budget exceeded: " + F0(capWall) + " s wall-clock cap while " + PhaseWords(), false);
                return;
            }

            if (elapsedSim > budgetSim)
            {
                Fail("budget exceeded: " + F0(budgetSim) + " s of simulation time while " + PhaseWords(), false);
                return;
            }

            if (Phase != TaskPhase.Refuelling && model.FuelPct <= 0.0)
            {
                Fail("out of fuel", true);
                return;
            }

            switch (Phase)
            {
                case TaskPhase.ToDestination:
                    if (!Drive(simDt)) return;
                    timeline.Add(id, TimelineKinds.Arrived, active.Area + ": parked at slot " + (slot.HasValue ? (slot.Value.Index + 1).ToString(CultureInfo.InvariantCulture) : "?"));
                    Succeed(null, MachineMode.Parked, TaskPhase.None, keepSlot: true);
                    break;
                case TaskPhase.ToQueue:
                    if (!Drive(simDt)) return;
                    bay.Request(id);
                    Phase = TaskPhase.WaitingForBay;
                    bayWait = 0;
                    timeline.Add(id, TimelineKinds.Arrived, "refuel queue: " + (bay.Holder != null && bay.Holder != id ? "bay busy" : "bay free") + ", " + bay.Waiting + " in line");
                    break;
                case TaskPhase.WaitingForBay:
                    bayWait += simDt;
                    if (bay.TryGrant(id, world.BayClear(id)))
                    {
                        var bayAt = site.Spots[RouteGraph.BaySpot];
                        follow.Start(Route.Straight(body.X, body.Z, bayAt.X, bayAt.Z, SpeedModel.BayApproachFactor), body.HeadingDegrees);
                        Phase = TaskPhase.EnteringBay;
                        timeline.Add(id, TimelineKinds.Arrived, "bay granted after " + F0(bayWait) + " s in line");
                    }
                    else if (bayWait > TaskBudgets.BayWaitSeconds)
                    {
                        Fail("refuel bay wait exceeded " + F0(TaskBudgets.BayWaitSeconds) + " s", false);
                    }

                    break;
                case TaskPhase.EnteringBay:
                    if (!Drive(simDt)) return;
                    service = new RefuelService(model);
                    Phase = TaskPhase.Refuelling;
                    timeline.Add(id, TimelineKinds.Refuelling, "service started at " + F1(service.StartPct) + "% fuel");
                    break;
                case TaskPhase.Refuelling:
                    service.Step(simDt);
                    if (!service.Done) return;
                    var from = service.StartPct;
                    timeline.Add(id, TimelineKinds.Refuelling, "service finished: " + F1(from) + "% -> " + F1(model.FuelPct) + "%");
                    Succeed("refuelled " + F1(from) + "% to " + F1(model.FuelPct) + "%", MachineMode.Parked, TaskPhase.None, keepSlot: false);
                    StartReturn();
                    break;
            }
        }

        string PhaseWords()
        {
            switch (Phase)
            {
                case TaskPhase.ToDestination: return "driving to " + active.Area;
                case TaskPhase.ToQueue: return "driving to the refuel queue";
                case TaskPhase.WaitingForBay: return "waiting for the bay";
                case TaskPhase.EnteringBay: return "entering the bay";
                case TaskPhase.Refuelling: return "refuelling";
                default: return "idle";
            }
        }

        void Succeed(string note, MachineMode next, TaskPhase phase, bool keepSlot)
        {
            var request = active;
            timeline.Add(id, TimelineKinds.Outcome, "SUCCESS" + (note != null ? ": " + note : ""));
            ReleaseTask();
            if (!keepSlot)
            {
                parking.Release(id);
                slot = null;
            }

            active = null;
            Mode = next;
            Phase = phase;
            follow.Clear();
            arbiter.Finished(request);
            request.Complete(TaskResult.Ok(note));
        }

        void Fail(string reason, bool stalled)
        {
            var request = active;
            timeline.Add(id, TimelineKinds.Outcome, "FAILED: " + reason);
            ReleaseTask();
            parking.Release(id);
            slot = null;
            active = null;
            Phase = TaskPhase.None;
            follow.Clear();
            Mode = stalled ? MachineMode.Stalled : MachineMode.Parked;
            if (stalled) timeline.Add(id, TimelineKinds.Stalled, "out of fuel: stopped where it stood");
            arbiter.Finished(request);
            request.Complete(TaskResult.Fail(reason));
        }

        /// <summary>Gives back what a task held: the bay and the service. (Parking is kept or dropped by the caller.)</summary>
        void ReleaseTask()
        {
            bay.Release(id);
            service = null;
            follow.Clear();
            active = null;
            Phase = TaskPhase.None;
        }

        // ---------------------------------------------------------------- back to work

        /// <summary>Drives to the nearest point of the machine's own track and puts it back on it.</summary>
        void StartReturn()
        {
            parking.Release(id);
            slot = null;
            if (!body.TryNearestTrackPoint(body.X, body.Z, out var point))
            {
                Mode = MachineMode.Parked;
                timeline.Add(id, TimelineKinds.Returning, "no track of its own to return to");
                return;
            }

            var route = graph.Plan(body.X, body.Z, point.X, point.Z);
            if (route == null)
            {
                Mode = MachineMode.Parked;
                timeline.Add(id, TimelineKinds.Returning, "no route back to its track");
                return;
            }

            returnPoint = point;
            returnWall = 0;
            Mode = MachineMode.Returning;
            follow.Start(route, body.HeadingDegrees);
            timeline.Add(id, TimelineKinds.Returning, "back to work: route " + F0(route.Length) + " m, ETA " + F0(route.Eta(kin.Cruise)) + " s");
        }

        void StepReturn(double simDt, double wallDt)
        {
            returnWall += wallDt;
            if (returnWall > TaskBudgets.ReturnWallCapSeconds)
            {
                follow.Clear();
                Mode = MachineMode.Parked;
                timeline.Add(id, TimelineKinds.Returning, "could not get back to its track in " + F0(TaskBudgets.ReturnWallCapSeconds) + " s: parked");
                return;
            }

            if (model.FuelPct <= 0.0)
            {
                follow.Clear();
                Mode = MachineMode.Stalled;
                timeline.Add(id, TimelineKinds.Stalled, "out of fuel: stopped where it stood");
                return;
            }

            if (follow.Active)
            {
                Drive(simDt);
                return;
            }

            if (!follow.Align(simDt, body, kin, returnPoint.HeadingDegrees)) return;
            body.Attach(returnPoint);
            Mode = MachineMode.Working;
            timeline.Add(id, TimelineKinds.Working, "back on its track");
        }

        /// <summary>The presenter's Resume work: a parked machine drives back to its track. Anything else is said, not done.</summary>
        public string Resume()
        {
            string said;
            switch (Mode)
            {
                case MachineMode.Parked:
                    timeline.Add(id, TimelineKinds.Presenter, "resume work");
                    StartReturn();
                    return null;
                case MachineMode.Commanded: said = "resume work refused: running " + (active != null ? active.Key : "a command"); break;
                case MachineMode.Returning: said = "resume work: already on its way back"; break;
                case MachineMode.Stalled: said = "resume work refused: out of fuel"; break;
                default: said = "resume work: already on its track"; break;
            }

            timeline.Add(id, TimelineKinds.Presenter, said);
            return said;
        }

        // ---------------------------------------------------------------- the run ends

        /// <summary>The simulation is ending: the running command (if any) is answered failed, and nothing new will run.</summary>
        public void FailAll(string reason)
        {
            reset = true;
            if (active == null) return;
            var request = active;
            timeline.Add(id, TimelineKinds.Reset, reason);
            ReleaseTask();
            parking.Release(id);
            slot = null;
            arbiter.Finished(request);
            Mode = MachineMode.Parked;
            request.Complete(TaskResult.Fail(reason));
        }
    }
}
