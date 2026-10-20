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
        /// <summary>Running a command (it may still be riding its track to where it leaves it).</summary>
        Commanded,
        /// <summary>Off its track, driving back to it.</summary>
        Returning,
        /// <summary>Off its track and standing, because a command sent it there.</summary>
        Parked,
        /// <summary>Off its track and standing, because the tank ran dry.</summary>
        Stalled,
    }

    public enum TaskPhase { None, ToDestination, ToQueue, WaitingForBay, EnteringBay, Refuelling }

    /// <summary>
    /// What a machine's controller asks of the rest of the site: the trips that take it where its commands send it (planned and held to the
    /// site's traffic by the site, see <see cref="ITrip"/>), the place it stands at, and whether the refuel bay is clear.
    /// </summary>
    public interface ITaskWorld
    {
        /// <summary>A trip to a place of zone <paramref name="area"/> the machine may stand at, or null with the reason (no such zone, no free place, no way there).</summary>
        ITrip TripToArea(IMachineBody body, string area, double laneShare, out string refusal);

        /// <summary>A trip to the refuel queue, or null with the reason.</summary>
        ITrip TripToQueue(IMachineBody body, double laneShare, out string refusal);

        /// <summary>The drive from the queue into the bay, or null with the reason.</summary>
        ITrip TripIntoBay(IMachineBody body, out string refusal);

        /// <summary>A trip back onto the machine's own track, or null with the reason.</summary>
        ITrip TripBack(IMachineBody body, double laneShare, out string refusal);

        /// <summary>The machine no longer stands where it was sent: the place it held (a parking slot, a stand) is given back.</summary>
        void Release(string id);

        /// <summary>Nothing but <paramref name="id"/> is standing in the refuel bay.</summary>
        bool BayClear(string id);
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
    /// One machine's task layer. A command arrives (already validated), the arbiter says whether it wins, and the machine is driven by a trip
    /// the site plans (<see cref="ITaskWorld"/>): <c>goto-area</c> to a place of a zone it may stand at, where it stays; <c>goto-refuel</c> to
    /// the queue, into the bay when it is its turn, through the Refuelling service, and then back to its own track. Every command ends with an
    /// answer and a reason: carried out, refused, superseded, out of fuel, over budget or reset.
    ///
    /// Main thread only, no network and no scene types: the body, the world and the clock are handed in, so the whole of it runs in an
    /// EditMode test. The only way this class changes the tank is <see cref="RefuelService"/>, and it only does so in
    /// <see cref="TaskPhase.Refuelling"/>.
    /// </summary>
    public sealed class MachineController
    {
        readonly string id;
        readonly MachineModel model;
        readonly IMachineBody body;
        readonly BayReservations bay;
        readonly Timeline timeline;
        readonly ITaskWorld world;
        readonly TaskArbiter arbiter = new TaskArbiter();

        TaskRequest active;
        RefuelService service;
        ITrip trip;
        string waitingSaid;
        double elapsedSim, elapsedWall, budgetSim, capWall, bayWait, returnWall;
        bool reset, returnOverdueSaid;
        bool wasAtBay;   // the task that just ended was at or in the bay (or its queue): a machine left standing there drives out

        public MachineController(IMachineBody body, MachineModel model, BayReservations bay, Timeline timeline, ITaskWorld world)
        {
            this.body = body ?? throw new ArgumentNullException(nameof(body));
            this.model = model ?? throw new ArgumentNullException(nameof(model));
            this.bay = bay ?? throw new ArgumentNullException(nameof(bay));
            this.timeline = timeline ?? throw new ArgumentNullException(nameof(timeline));
            this.world = world ?? throw new ArgumentNullException(nameof(world));
            id = body.Id;
            Mode = body.Attached ? MachineMode.Working : MachineMode.Parked;
        }

        public string Id => id;
        public MachineMode Mode { get; private set; }
        public TaskPhase Phase { get; private set; }
        public TaskRequest Running => active;
        public double SpeedMps => trip != null ? trip.Speed : 0.0;

        /// <summary>
        /// How much of its lane's offset this machine takes on the free routes it drives (0 the road's own line, 1 its lane; see
        /// <see cref="LaneLine"/>). It is the machine's own: the seam for a machine alone on a road to drift toward the middle of it.
        /// </summary>
        public double LaneShare { get; set; } = 1.0;

        /// <summary>The route the machine is driving now (to a destination, to the queue, into the bay, back to its track); null when it is not driving one.</summary>
        public Route CurrentRoute => Mode == MachineMode.Commanded || Mode == MachineMode.Returning ? trip?.Route : null;

        /// <summary>What the machine is waiting for while its trip is held, in words (null when it is not held).</summary>
        public string Waiting => trip?.Waiting;

        /// <summary>The bay is held or waited for by this machine.</summary>
        public bool WantsBay => Phase == TaskPhase.WaitingForBay || Phase == TaskPhase.EnteringBay || Phase == TaskPhase.Refuelling;

        static string F0(double v) => v.ToString("0", CultureInfo.InvariantCulture);
        static string F1(double v) => v.ToString("0.0", CultureInfo.InvariantCulture);

        public const int TokenTail = 10;

        // the END of a token is what tells two commands apart (they share a long prefix)
        static string Short(string token) => token == null ? "?" : token.Length > TokenTail ? "…" + token.Substring(token.Length - TokenTail) : token;

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
                // only the answer that stands is reported: a command the session had already answered was not superseded by this one
                if (decision.Superseded.Complete(TaskResult.Fail(reason)))
                    timeline.Add(id, TimelineKinds.Superseded, decision.Superseded.Key + " " + Short(decision.Superseded.Token) + " " + reason);
                else
                    timeline.Add(id, TimelineKinds.Outcome, "dropped: " + decision.Superseded.Key + " " + Short(decision.Superseded.Token) + " had already been answered by the session");
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

        // A refused command never took the machine over, so it changes nothing about what the machine is doing: a truck driving back to its
        // track keeps driving, a parked one keeps its place. The one exception is the command it superseded a moment ago: that one's task is
        // already gone, and the machine is left to stand down.
        void Refuse(TaskRequest request, string reason)
        {
            timeline.Add(id, TimelineKinds.Refused, reason);
            request.Complete(TaskResult.Fail(reason));
            arbiter.Finished(request);
            if (Mode == MachineMode.Commanded && active == null) Stand();
        }

        /// <summary>No task is running and none will: a machine that was at the bay, on a lane where it may not stand, or still on its track goes back to work; any other stops where it is.</summary>
        void Stand()
        {
            var where = trip;
            if (wasAtBay || (where != null && !where.MayStandHere) || body.Attached)
            {
                wasAtBay = false;
                StartReturn();
                return;
            }

            world.Release(id);
            trip = null;
            Mode = MachineMode.Parked;
        }

        void TakeOver(TaskRequest request, ITrip next, double budget, double cap, string label)
        {
            wasAtBay = false;
            active = request;
            Mode = MachineMode.Commanded;
            elapsedSim = elapsedWall = bayWait = 0;
            budgetSim = budget;
            capWall = cap;
            trip = next;
            waitingSaid = null;
            timeline.Add(id, TimelineKinds.Accepted, label + ": route " + F0(next.Metres) + " m, ETA " + F0(next.EtaSeconds) + " s");
        }

        void BeginArea(TaskRequest request)
        {
            var next = world.TripToArea(body, request.Area, LaneShare, out var refusal);
            if (next == null)
            {
                Refuse(request, refusal);
                return;
            }

            var eta = next.EtaSeconds;
            Phase = TaskPhase.ToDestination;
            TakeOver(request, next, TaskBudgets.EtaFactor * eta + TaskBudgets.AreaSlackSeconds, TaskBudgets.AreaWallCapSeconds, request.Area);
        }

        void BeginRefuel(TaskRequest request)
        {
            var next = world.TripToQueue(body, LaneShare, out var refusal);
            if (next == null)
            {
                Refuse(request, refusal);
                return;
            }

            var eta = next.EtaSeconds;
            Phase = TaskPhase.ToQueue;
            // the budget is for the DRIVING; the wait in line has its own limit and the service takes as long as it takes
            TakeOver(request, next, TaskBudgets.EtaFactor * eta + TaskBudgets.AreaSlackSeconds, TaskBudgets.RefuelWallCapSeconds, "refuel bay");
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

        // drives the trip one step, and says in the timeline what it is held for when that changes
        bool Drive(double dt)
        {
            var arrived = trip.Step(dt);
            var why = arrived ? null : trip.Waiting;
            if (why != null && why != waitingSaid) timeline.Add(id, TimelineKinds.Waiting, why);
            waitingSaid = why ?? waitingSaid;
            return arrived;
        }

        void StepTask(double simDt, double wallDt)
        {
            if (active == null)
            {
                Stand();
                return;
            }

            // answered by someone else (the session gave up on it): nothing is waiting on this task any more
            if (active.IsComplete)
            {
                var said = active.Completion.Result;
                Fail("the session had already answered it (" + (said.Reason ?? "no reason") + ")", false, true);
                return;
            }

            // waiting in line and being served are not the machine's travelling: neither runs the simulation-time budget
            if (Phase != TaskPhase.WaitingForBay && Phase != TaskPhase.Refuelling) elapsedSim += simDt;
            elapsedWall += wallDt;
            if (elapsedWall > capWall)
            {
                Fail("budget exceeded: " + F0(capWall) + " s wall-clock cap while " + PhaseWords() + WaitingWords(), false);
                return;
            }

            if (elapsedSim > budgetSim)
            {
                Fail("budget exceeded: " + F0(budgetSim) + " s of simulation time while " + PhaseWords() + WaitingWords(), false);
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
                    timeline.Add(id, TimelineKinds.Arrived, active.Area + ": parked at " + trip.Place);
                    Succeed(null, MachineMode.Parked, TaskPhase.None, keepPlace: true);
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
                        var next = world.TripIntoBay(body, out var refusal);
                        if (next == null)
                        {
                            Fail(refusal, false);
                            return;
                        }

                        trip = next;
                        waitingSaid = null;
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
                    Succeed("refuelled " + F1(from) + "% to " + F1(model.FuelPct) + "%", MachineMode.Parked, TaskPhase.None, keepPlace: false);
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

        // a budget that ran out while the machine was held says what held it
        string WaitingWords() => waitingSaid != null ? " (" + waitingSaid + ")" : "";

        void Succeed(string note, MachineMode next, TaskPhase phase, bool keepPlace)
        {
            var request = active;
            timeline.Add(id, TimelineKinds.Outcome, "SUCCESS" + (note != null ? ": " + note : ""));
            ReleaseTask(abandon: false);
            if (!keepPlace) world.Release(id);
            active = null;
            Mode = next;
            Phase = phase;
            arbiter.Finished(request);
            request.Complete(TaskResult.Ok(note));
        }

        void Fail(string reason, bool stalled, bool answeredElsewhere = false)
        {
            var request = active;
            var atBay = WantsBay;
            var where = trip;
            timeline.Add(id, TimelineKinds.Outcome, (answeredElsewhere ? "DROPPED: " : "FAILED: ") + reason);
            ReleaseTask();
            active = null;
            Phase = TaskPhase.None;
            Mode = stalled ? MachineMode.Stalled : MachineMode.Parked;
            if (stalled) timeline.Add(id, TimelineKinds.Stalled, "out of fuel: stopped where it stood");
            arbiter.Finished(request);
            request.Complete(TaskResult.Fail(reason));
            wasAtBay = false;
            if (stalled)
            {
                world.Release(id);
                return;
            }

            // a machine that fails at the bay or in its queue drives out: it never stays parked where the next one must stand; and one on a
            // lane, or still riding its track, is never left there either: it goes back to work
            if (atBay || (where != null && !where.MayStandHere) || body.Attached) StartReturn();
            else world.Release(id);
        }

        /// <summary>Gives back what a task held: the bay, the service and the trip (the machine stands where it is, or drives on: see <see cref="ITrip.Abandon"/>).</summary>
        void ReleaseTask(bool abandon = true)
        {
            if (Phase == TaskPhase.Refuelling && service != null && !service.Done)
                timeline.Add(id, TimelineKinds.Refuelling, "service stopped at " + F1(model.FuelPct) + "% fuel");
            wasAtBay = WantsBay;
            bay.Release(id);
            service = null;
            if (abandon && trip != null) trip.Abandon();
            active = null;
            Phase = TaskPhase.None;
        }

        // ---------------------------------------------------------------- back to work

        /// <summary>Drives back to the machine's own track and puts it back on it.</summary>
        void StartReturn()
        {
            world.Release(id);
            var next = world.TripBack(body, LaneShare, out var refusal);
            if (next == null)
            {
                trip = null;
                Mode = MachineMode.Parked;
                timeline.Add(id, TimelineKinds.Returning, refusal);
                return;
            }

            trip = next;
            returnWall = 0;
            returnOverdueSaid = false;
            waitingSaid = null;
            Mode = MachineMode.Returning;
            // a machine still on its track (a command given up before it left) has no way back to say
            if (!body.Attached || next.Metres > 0.0)
                timeline.Add(id, TimelineKinds.Returning, "back to work: route " + F0(next.Metres) + " m, ETA " + F0(next.EtaSeconds) + " s");
        }

        void StepReturn(double simDt, double wallDt)
        {
            returnWall += wallDt;
            if (returnWall > TaskBudgets.ReturnWallCapSeconds)
            {
                // a machine that may stand where it is gives up and parks; one on a lane never does (it would be in everyone's way): it says so once
                if (trip.MayStandHere)
                {
                    trip.Abandon();
                    trip = null;
                    Mode = MachineMode.Parked;
                    timeline.Add(id, TimelineKinds.Returning, "could not get back to its track in " + F0(TaskBudgets.ReturnWallCapSeconds) + " s: parked" + WaitingWords());
                    return;
                }

                if (!returnOverdueSaid)
                {
                    returnOverdueSaid = true;
                    timeline.Add(id, TimelineKinds.Returning, "still on its way back after " + F0(TaskBudgets.ReturnWallCapSeconds) + " s" + WaitingWords());
                }
            }

            if (model.FuelPct <= 0.0)
            {
                trip.Abandon();
                trip = null;
                Mode = MachineMode.Stalled;
                timeline.Add(id, TimelineKinds.Stalled, "out of fuel: stopped where it stood");
                return;
            }

            var wasOnTrack = body.Attached;
            if (!Drive(simDt)) return;
            trip = null;
            Mode = MachineMode.Working;
            if (!wasOnTrack) timeline.Add(id, TimelineKinds.Working, "back on its track");
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

        /// <summary>The simulation is ending: the running command (if any) is answered failed, and nothing new will run. Returns how many it answered (0 or 1).</summary>
        public int FailAll(string reason)
        {
            reset = true;
            if (active == null) return 0;
            var request = active;
            timeline.Add(id, TimelineKinds.Reset, reason);
            ReleaseTask();
            world.Release(id);
            arbiter.Finished(request);
            Mode = MachineMode.Parked;
            wasAtBay = false;
            return request.Complete(TaskResult.Fail(reason)) ? 1 : 0;
        }
    }
}
