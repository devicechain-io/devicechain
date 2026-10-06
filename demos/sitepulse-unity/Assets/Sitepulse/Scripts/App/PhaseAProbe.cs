// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text;
using System.Text.Json;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.DevicePlane;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>One measured thing: what was asked, what was found, and whether that passes. Pass is null for a record that is not a check.</summary>
    public sealed class ProbeItem
    {
        public ProbeItem(string id, string description, bool? pass, string detail, double? seconds = null)
        {
            Id = id;
            Description = description;
            Pass = pass;
            Detail = detail;
            Seconds = seconds;
        }

        public string Id { get; }
        public string Description { get; }
        public bool? Pass { get; }
        public string Detail { get; }
        public double? Seconds { get; }
    }

    /// <summary>What the probe may look at and do. Everything is a delegate so the probe runs over a fake in a test.</summary>
    public sealed class ProbeWorld
    {
        public ReadinessBoard Board { get; set; }
        public ObservedState Observed { get; set; }

        /// <summary>The readings the data layer's cards show right now.</summary>
        public Func<IEnumerable<DeviceReading>> Cards { get; set; }

        /// <summary>The provenance of the data layer's source; null when it has none.</summary>
        public Func<Provenance?> CardSource { get; set; }

        /// <summary>Whether the data layer's source is told the measurement stream is live; null when it has no source.</summary>
        public Func<bool?> CardStreamLive { get; set; }

        public Func<Timeline> Timeline { get; set; }

        /// <summary>The presenter's "prepare low fuel" for one machine; returns what it said.</summary>
        public Func<string, string> PrepareLowFuel { get; set; }

        public Func<string, bool> FileExists { get; set; }

        /// <summary>Writes the result file (name, text), replacing the previous one whole.</summary>
        public Action<string, string> WriteFile { get; set; }

        public string SamplePath { get; set; }
        public Func<long> SampleLines { get; set; }
        public string UnityVersion { get; set; }

        // what the Phase B controls and the soak read (all optional: a probe that does not need one never asks)

        /// <summary>The observer's two streams, as the banner and the readiness line read them.</summary>
        public ObserverStatus Observer { get; set; }

        /// <summary>One machine's task state and local tank, or null when it has none.</summary>
        public Func<string, MachineView?> Machine { get; set; }

        /// <summary>The refuel bay's holder and queue, or null before the task layer exists.</summary>
        public Func<BayView?> Bay { get; set; }

        /// <summary>How many device sessions the plane holds.</summary>
        public Func<int> SessionCount { get; set; }

        /// <summary>Samples the outbound rings gave up on (summed over every device).</summary>
        public Func<long> DroppedSamples { get; set; }

        /// <summary>Sends the sessions reported as failed (summed over every device).</summary>
        public Func<long> SendErrors { get; set; }

        /// <summary>This frame's unscaled delta time in seconds.</summary>
        public Func<double> FrameSeconds { get; set; }
    }

    /// <summary>A machine's controller state and its local tank, as the probe samples it.</summary>
    public readonly struct MachineView
    {
        public MachineView(MachineMode mode, TaskPhase phase, double fuelPct, bool wantsBay)
        {
            Mode = mode;
            Phase = phase;
            FuelPct = fuelPct;
            WantsBay = wantsBay;
        }

        public MachineMode Mode { get; }
        public TaskPhase Phase { get; }
        public double FuelPct { get; }
        public bool WantsBay { get; }

        /// <summary>On its routine track and not in line for the bay.</summary>
        public bool OnTrack => Mode == MachineMode.Working && !WantsBay;

        public override string ToString() => Mode + (Phase != TaskPhase.None ? "/" + Phase : "");
    }

    /// <summary>The refuel bay: who holds it (null when free) and how many wait.</summary>
    public readonly struct BayView
    {
        public BayView(string holder, int waiting)
        {
            Holder = holder;
            Waiting = waiting;
        }

        public string Holder { get; }
        public int Waiting { get; }
    }

    /// <summary>
    /// The in-player half of the Phase A acceptance (<c>-sitepulse-acceptance phaseA</c>). It measures
    /// what only the player can see, from the objects the player already holds and not from pixels: how
    /// long the fleet took to bind, get credentials, publish and be observed by the platform; whether every
    /// value on a Live card is an observed one; and what each machine's own timeline says about the
    /// commands it was given, including the platform's REACT goto-refuel after the probe prepares a low
    /// tank on SP-HL-0006. It never creates a command or an alarm and never raises a tank: the platform's
    /// rule does the sending, and the WSL checker corroborates from the platform's side.
    ///
    /// Run as a control (<c>-sitepulse-control</c>), it takes no commands: it waits a fixed time and judges
    /// that the injected fault failed the way it should have.
    /// </summary>
    public sealed partial class PhaseAProbe
    {
        public const string ResultFile = "phaseA-result.json";
        public const string SampleFile = "phaseA-samples.jsonl";

        /// <summary>The WSL side writes this file when it has finished its own checks; the probe then finishes.</summary>
        public const string FinishFile = "phaseA-finish";

        public const string RefuelMachine = "SP-HL-0006";
        public const double ObservedBudgetSeconds = 30.0;
        public const double ReachBudgetSeconds = 120.0;
        public const double TotalBudgetSeconds = 480.0;
        public const double ControlCheckSeconds = 60.0;
        public const double RunnerStopWindowSeconds = 75.0;
        const double CardsAfterObservedSeconds = 5.0;
        const double TickEverySeconds = 0.5;

        readonly AcceptanceOptions options;
        readonly ProbeWorld world;
        readonly Func<DateTimeOffset> clock;
        readonly Action<string> log;
        readonly DateTimeOffset startedAt;
        readonly List<ProbeItem> items = new List<ProbeItem>();

        DateTimeOffset lastTick = DateTimeOffset.MinValue;
        DateTimeOffset lastWrite = DateTimeOffset.MinValue;
        double? sessionsAt, boundAt, credentialedAt, publishingAt, observedAt;
        DateTimeOffset? cardsAt, preparedAt;
        string prepared;
        bool cardsDone;
        bool wroteMilestones;
        string finishedAtText;
        readonly Dictionary<string, List<TimelineRow>> deviceLog = new Dictionary<string, List<TimelineRow>>(StringComparer.Ordinal);

        public PhaseAProbe(AcceptanceOptions options, ProbeWorld world, DateTimeOffset startedAt, Func<DateTimeOffset> clock = null, Action<string> log = null)
        {
            this.options = options ?? throw new ArgumentNullException(nameof(options));
            this.world = world ?? throw new ArgumentNullException(nameof(world));
            this.startedAt = startedAt;
            this.clock = clock ?? (() => DateTimeOffset.UtcNow);
            this.log = log ?? (_ => { });
        }

        public bool Finished { get; private set; }
        public bool Passed { get; private set; }

        /// <summary>The process exit code once finished: 0 when every check passed, 1 otherwise.</summary>
        public int ExitCode => Passed ? 0 : 1;

        public IReadOnlyList<ProbeItem> Items => items;

        double Elapsed(DateTimeOffset now) => (now - startedAt).TotalSeconds;

        /// <summary>Call every frame; it does its work at most twice a second.</summary>
        public void Tick()
        {
            if (Finished) return;
            var now = clock();
            SampleFrame();
            if ((now - lastTick).TotalSeconds < TickEverySeconds) return;
            lastTick = now;
            var t = Elapsed(now);

            var changed = Milestones(t);
            if (observedAt.HasValue && phaseText == "starting") phaseText = "observed";
            if (options.IsSoak) TickSoak(now, t);
            else if (options.IsControl) TickControl(now, t);
            else TickPhaseA(now, t);
            if (Finished) return;

            if (changed || !wroteMilestones || (now - lastWrite).TotalSeconds >= 5.0)
            {
                wroteMilestones = true;
                Write(now, false);
            }
        }

        bool Milestones(double t)
        {
            var b = world.Board;
            int bound = 0, credentialed = 0, publishing = 0, observed = 0;
            foreach (var d in b.Devices)
            {
                if (d.Bind != null && d.Bind.IsBound) bound++;
                if (d.Bind != null && d.Bind.IsBound && d.Stage >= DeviceStage.Credentialed && !d.Failed) credentialed++;
                if (d.Stage >= DeviceStage.Publishing && !d.Failed) publishing++;
                if (d.Stage >= DeviceStage.Observed && !d.Failed) observed++;
            }

            var total = b.Total;
            var changed = false;
            if (!sessionsAt.HasValue && b.SessionsBegun) { sessionsAt = t; changed = true; }
            if (!boundAt.HasValue && bound == total) { boundAt = t; changed = true; }
            if (!credentialedAt.HasValue && credentialed == total) { credentialedAt = t; changed = true; }
            if (!publishingAt.HasValue && publishing == total) { publishingAt = t; changed = true; }
            if (!observedAt.HasValue && observed == total) { observedAt = t; changed = true; }
            return changed;
        }

        // ------------------------------------------------------------------ phase A

        void TickPhaseA(DateTimeOffset now, double t)
        {
            if (!cardsDone && ((observedAt.HasValue && t >= observedAt.Value + CardsAfterObservedSeconds) || t >= ReachBudgetSeconds))
            {
                cardsDone = true;
                cardsAt = now;
                items.Add(CheckCards(world));
            }

            if (cardsDone && prepared == null && observedAt.HasValue)
            {
                preparedAt = now;
                prepared = world.PrepareLowFuel(RefuelMachine) ?? "the presenter could not prepare the tank";
                log($"probe: {RefuelMachine}: {prepared}");
            }

            // nothing further can be measured if the fleet never came up
            if (cardsDone && !observedAt.HasValue) { Finish(now); return; }
            var flagged = prepared != null && world.FileExists(FinishFile);
            if (flagged || t >= TotalBudgetSeconds) Finish(now);
        }

        // ------------------------------------------------------------------ controls

        // a runner-stop control watches the fleet for a while after it came up (the script stops the runner then)
        void TickControl(DateTimeOffset now, double t)
        {
            if (options.Control.Kind == ControlSpec.RuleDisabled) { TickRuleDisabled(now, t); return; }
            if (options.Control.Kind == ControlSpec.ObserverOutage) { TickObserverOutage(now, t); return; }
            var due = options.Control.Kind == ControlSpec.RunnerStop
                ? (observedAt.HasValue ? observedAt.Value + RunnerStopWindowSeconds : ReachBudgetSeconds)
                : ControlCheckSeconds;
            if (t >= due) Finish(now);
        }

        // ------------------------------------------------------------------ finishing

        void Finish(DateTimeOffset now)
        {
            if (Finished) return;
            var t = Elapsed(now);
            finishedAtText = now.UtcDateTime.ToString("O", CultureInfo.InvariantCulture);
            CollectDeviceLog();
            var board = world.Board;

            items.Insert(0, TimeItem("bound-19", "every scene device resolved on the platform", boundAt, board, d => d.Bind != null && d.Bind.IsBound, null));
            items.Insert(1, TimeItem("credentialed-19", "every scene device has its credential", credentialedAt, board, d => d.Bind != null && d.Bind.IsBound && d.Stage >= DeviceStage.Credentialed && !d.Failed, null));
            items.Insert(2, TimeItem("publishing-19", "every device's session is publishing (the broker acknowledged a sample)", publishingAt, board, d => d.Stage >= DeviceStage.Publishing && !d.Failed, null));
            items.Insert(3, TimeItem("observed-19", $"every device observed by the platform within {ObservedBudgetSeconds:0} s of the player's start", observedAt, board, d => d.Stage >= DeviceStage.Observed && !d.Failed, ObservedBudgetSeconds));

            if (options.IsSoak) AddSoakItems(now);
            else if (options.IsControl && options.Control.Kind == ControlSpec.RuleDisabled) AddRuleDisabledItems(now);
            else if (options.IsControl && options.Control.Kind == ControlSpec.ObserverOutage) AddObserverOutageItems(now);
            else if (options.IsControl) items.Add(EvaluateControl(options.Control, board, world.Board.Brief()));
            else
            {
                items.Add(new ProbeItem("low-fuel-prepared", $"the presenter's prepare-low-fuel ran on {RefuelMachine}", prepared != null && cardsAt.HasValue, prepared ?? "not reached"));
                items.Add(EvaluateRefuelLog(world.Timeline?.Invoke(), RefuelMachine, preparedAt));
                items.Add(new ProbeItem("device-log", "every machine's own account of the commands it was given (see deviceLog)", null, $"{deviceLog.Count} machine(s) had commands"));
            }

            Passed = true;
            foreach (var i in items)
                if (i.Pass == false) Passed = false;
            Finished = true;
            log($"probe: finished after {t:0.0} s · {(Passed ? "PASS" : "FAIL")}");
            Write(now, true);
        }

        // a control expects the fleet NOT to come up whole, so its progress is a record and not a check
        ProbeItem TimeItem(string id, string what, double? at, ReadinessBoard board, Func<DeviceReadiness, bool> has, double? budget)
        {
            var n = 0;
            foreach (var d in board.Devices)
                if (has(d)) n++;
            var ok = at.HasValue && n == board.Total && (!budget.HasValue || at.Value <= budget.Value);
            var detail = at.HasValue ? $"{board.Total}/{board.Total} after {at.Value:0.0} s" : $"{n}/{board.Total} at the end; never reached {board.Total}/{board.Total}";
            if (at.HasValue && budget.HasValue && at.Value > budget.Value) detail += $" (budget {budget.Value:0} s)";
            return new ProbeItem(id, what, FleetMustComeUp ? ok : (bool?)null, detail, at);
        }

        // ------------------------------------------------------------------ checks (pure over their inputs)

        /// <summary>
        /// Every value on every Live card is an observed one: the source says Observed, each reading says
        /// Observed, every value, speed and command carries the time it occurred and the time it was seen,
        /// and a fresh reading filled for each of the 19 devices from <see cref="ObservedState"/> alone holds
        /// the measurement keys of its profile, each occurring no earlier than this run began.
        /// </summary>
        public static ProbeItem CheckCards(ProbeWorld w)
        {
            var problems = new List<string>();
            var source = w.CardSource?.Invoke();
            if (source != Provenance.Observed) problems.Add($"the cards' source is {(source.HasValue ? source.Value.ToString() : "absent")}, not Observed");

            var cards = 0;
            var values = 0;
            if (w.Cards != null)
                foreach (var r in w.Cards())
                {
                    cards++;
                    values += CheckReading(r, problems);
                }

            if (cards == 0) problems.Add("no card is on screen to check");

            // all 19: the same source the cards use, filled from ObservedState
            var observedSource = new ObservedReadingSource(w.Observed, id => w.Board.TokenOf(id), () => true);
            var now = DateTimeOffset.UtcNow;
            var filled = 0;
            foreach (var d in w.Board.Devices)
            {
                var kind = d.Device.Kind == SceneKind.Plant ? DeviceReading.Profile.Plant : DeviceReading.Profile.Equipment;
                var r = new DeviceReading(d.Device.ExternalId, kind, Provenance.Observed);
                observedSource.Fill(new Visuals.ReadingSubject(d.Device.ExternalId), r, now);
                // what this kind of machine emits (the telemetry model's own key set): a dozer has no payload or tyres
                foreach (var key in ExpectedKeys(d.Device.Kind))
                {
                    var present = key == MeasurementKeys.PlantRunning ? r.TryGetFlag(key, out _) : r.TryGet(key, out _);
                    if (!present) { problems.Add($"{d.Device.ExternalId}: no observed {key}"); continue; }
                    if (r.TryGetStamp(key, out var s) && s.OccurredAt < w.Observed.OwnRunFloor)
                        problems.Add($"{d.Device.ExternalId}: {key} occurred before this run began");
                    filled++;
                }

                values += CheckReading(r, problems);
            }

            var ok = problems.Count == 0;
            var detail = ok
                ? $"{cards} card(s) on screen and 19 devices filled from ObservedState: {values} value(s), every one Observed and stamped; {filled} profile measurements present"
                : Clip(string.Join("; ", problems));
            return new ProbeItem("cards-observed", "every value on a Live card has Observed provenance (read from ObservedState, not pixels)", ok, detail);
        }

        /// <summary>The measurement keys a device of this kind emits, from the simulation's own model (one source).</summary>
        public static IReadOnlyList<string> ExpectedKeys(SceneKind kind) => MachineModel.KeysFor(DeviceSessionHost.ToEquipment(kind));

        static int CheckReading(DeviceReading r, List<string> problems)
        {
            var n = 0;
            if (r.Provenance != Provenance.Observed) problems.Add($"{r.DeviceId}: the reading is {r.Provenance}");
            var keys = r.Kind == DeviceReading.Profile.Equipment ? MeasurementKeys.Equipment : MeasurementKeys.Plant;
            foreach (var key in keys)
            {
                if (!r.TryGet(key, out _)) continue;
                n++;
                if (!r.TryGetStamp(key, out _)) problems.Add($"{r.DeviceId}: {key} has no observation time");
            }

            if (r.Kind == DeviceReading.Profile.Plant)
                foreach (var key in MeasurementKeys.PlantFlags)
                {
                    if (!r.TryGetFlag(key, out _)) continue;
                    n++;
                    if (!r.TryGetStamp(key, out _)) problems.Add($"{r.DeviceId}: {key} has no observation time");
                }

            if (r.SpeedKmh.HasValue)
            {
                n++;
                if (!r.SpeedStamp.HasValue) problems.Add($"{r.DeviceId}: speed has no observation time");
            }

            if (r.Command != null)
            {
                n++;
                if (!r.CommandStamp.HasValue) problems.Add($"{r.DeviceId}: command {r.Command} has no observation time");
            }

            return n;
        }

        /// <summary>
        /// A machine's own account of one refuel: received, accepted, the service started and finished, and an
        /// outcome that says SUCCESS, in that order, for a goto-refuel received at or after <paramref name="notBefore"/>.
        /// </summary>
        public static ProbeItem EvaluateRefuelLog(Timeline timeline, string machine, DateTimeOffset? notBefore)
        {
            const string Id = "device-log-refuel";
            var what = $"{machine}'s own timeline shows the platform's goto-refuel received, accepted, serviced and ended in SUCCESS";
            if (timeline == null) return new ProbeItem(Id, what, false, "the task layer never started");
            var rows = timeline.Rows(machine);
            var floor = notBefore ?? DateTimeOffset.MinValue;
            string lastMissing = "no goto-refuel was received after the tank was prepared";
            for (var i = 0; i < rows.Count; i++)
            {
                if (rows[i].Kind != TimelineKinds.Received || !rows[i].Text.StartsWith(CommandKeys.GotoRefuel, StringComparison.Ordinal) || rows[i].At < floor) continue;
                var at = i;
                var steps = new (string kind, string prefix, string name)[]
                {
                    (TimelineKinds.Accepted, "", "accepted"),
                    (TimelineKinds.Refuelling, "service started", "service started"),
                    (TimelineKinds.Refuelling, "service finished", "service finished"),
                    (TimelineKinds.Outcome, "SUCCESS", "outcome SUCCESS"),
                };
                var missing = (string)null;
                foreach (var step in steps)
                {
                    var found = -1;
                    for (var j = at + 1; j < rows.Count; j++)
                        if (rows[j].Kind == step.kind && rows[j].Text.StartsWith(step.prefix, StringComparison.Ordinal)) { found = j; break; }
                    if (found < 0) { missing = step.name; break; }
                    at = found;
                }

                var tail = TokenTail(rows[i].Text);
                if (missing == null) return new ProbeItem(Id, what, true, $"command {tail}: received, accepted, service started, service finished, SUCCESS");
                lastMissing = $"command {tail}: no \"{missing}\" row after it";
            }

            return new ProbeItem(Id, what, false, lastMissing);
        }

        /// <summary>The "(…abcdef1234)" a timeline row ends with, without its brackets; the whole text when there is none.</summary>
        public static string TokenTail(string rowText)
        {
            var open = rowText.LastIndexOf('(');
            var close = rowText.LastIndexOf(')');
            return open >= 0 && close > open ? rowText.Substring(open + 1, close - open - 1) : rowText;
        }

        /// <summary>
        /// Did the injected fault fail the way it should have? Each control says what it expects of the board
        /// and judges the board it is given; a control that does not fail is a failed control.
        /// </summary>
        public static ProbeItem EvaluateControl(ControlSpec control, ReadinessBoard board, string brief)
        {
            var id = "control-" + control.Label;
            var problems = new List<string>();
            var others = 0;
            switch (control.Kind)
            {
                case ControlSpec.BogusBinding:
                case ControlSpec.BadCredential:
                    foreach (var d in board.Devices)
                    {
                        var isTarget = d.Device.ExternalId == control.Target;
                        if (isTarget) continue;
                        if (d.Stage >= DeviceStage.Observed && !d.Failed) others++;
                        else problems.Add($"{d.Device.ExternalId} is {Describe(d)}, expected observed");
                    }

                    if (!Has(board, control.Target)) { problems.Add($"{control.Target} is not a scene device"); break; }
                    var t = board[control.Target];
                    if (t.Stage >= DeviceStage.Ready || t.Published > 0) problems.Add($"{control.Target} got past the broker (stage {t.Stage}, {t.Published} sent)");
                    if (!t.IsGrey) problems.Add($"{control.Target} is not a grey placeholder");
                    if (string.IsNullOrEmpty(t.FailReason)) problems.Add($"{control.Target} has no message");
                    if (control.Kind == ControlSpec.BogusBinding)
                    {
                        if (t.Bind == null || t.Bind.Outcome != BindOutcome.Missing) problems.Add($"{control.Target} did not come back Missing from the platform");
                    }
                    else if (!BrokerRefusedAtConnect(t)) problems.Add($"{control.Target} is {Describe(t)}, expected a device the broker refused at CONNECT (Blind, or a session start that failed with NotAuthorized)");
                    if (brief == null || !brief.Contains(control.Target)) problems.Add($"the readiness line does not name {control.Target}: \"{brief}\"");
                    break;

                case ControlSpec.RunnerStop:
                    // the observer talks to the platform, not the runner: with the runner gone the fleet must still be observed
                    // (the operator token lasts 15 minutes, so the window here cannot show it expiring)
                    foreach (var d in board.Devices)
                        if (d.Stage >= DeviceStage.Observed && !d.Failed && !d.Quiet && d.Side == DeviceSide.None) others++;
                        else problems.Add($"{d.Device.ExternalId} is {Describe(d)}{(d.Quiet ? ", quiet" : "")}, expected still observed");
                    break;

                case ControlSpec.WrongCa:
                    foreach (var d in board.Devices)
                    {
                        if (d.Stage >= DeviceStage.Ready || d.Published > 0) problems.Add($"{d.Device.ExternalId} is {Describe(d)}: it connected or published");
                        else if (!d.Failed && d.Side != DeviceSide.Reconnecting && d.Stage != DeviceStage.Connecting)
                            problems.Add($"{d.Device.ExternalId} is {Describe(d)}: neither failed, nor reconnecting, nor still connecting");
                        else others++;
                    }

                    if (string.IsNullOrEmpty(brief) || brief.StartsWith("all ", StringComparison.Ordinal)) problems.Add($"the readiness line claims nothing is wrong: \"{brief}\"");
                    break;
            }

            var ok = problems.Count == 0;
            var detail = ok
                ? $"failed the right way ({ControlSummary(control, board, others)}); readiness: {brief}"
                : Clip(string.Join("; ", problems));
            return new ProbeItem(id, "the injected fault fails the right way and is never mistaken for a working device", ok, detail);
        }

        static string ControlSummary(ControlSpec control, ReadinessBoard board, int others)
        {
            switch (control.Kind)
            {
                case ControlSpec.WrongCa: return $"{others} devices refused or still not connected";
                case ControlSpec.RunnerStop: return $"{others} devices still observed with the runner stopped";
                default: return $"{control.Target}: {Describe(board[control.Target])}; {others} others observed";
            }
        }

        /// <summary>
        /// The broker said no at CONNECT: the link went Blind, or the session start itself failed and the message says the
        /// broker refused it. Either way the device is failed and never got as far as Ready.
        /// </summary>
        public static bool BrokerRefusedAtConnect(DeviceReadiness d)
            => d.Failed && d.Stage < DeviceStage.Ready && d.Published == 0
               && (d.Side == DeviceSide.Blind || SessionFailure.SaysBrokerRefused(d.FailReason));

        static bool Has(ReadinessBoard board, string id)
        {
            foreach (var d in board.Devices)
                if (d.Device.ExternalId == id) return true;
            return false;
        }

        static string Describe(DeviceReadiness d)
            => d.Failed
                ? $"failed ({(d.Side != DeviceSide.None ? d.Side + ", " : "")}never past {d.Stage}): {Redactor.Redact(d.FailReason)}"
                : $"stage {d.Stage}{(d.Side != DeviceSide.None ? ", " + d.Side : "")}";

        static string Clip(string s) => s.Length <= 600 ? s : s.Substring(0, 600) + "…";

        // ------------------------------------------------------------------ the device log

        void CollectDeviceLog()
        {
            var tl = world.Timeline?.Invoke();
            if (tl == null) return;
            foreach (var d in world.Board.Devices)
            {
                var rows = tl.Rows(d.Device.ExternalId);
                if (rows.Count == 0) continue;
                var any = false;
                foreach (var r in rows)
                    if (r.Kind == TimelineKinds.Received || r.Kind == TimelineKinds.Refused) { any = true; break; }
                if (any) deviceLog[d.Device.ExternalId] = new List<TimelineRow>(rows);
            }
        }

        // ------------------------------------------------------------------ the result file

        /// <summary>The result of a run that could not get as far as measuring anything: final, failed, and why.</summary>
        public static string AbortedResult(AcceptanceOptions options, DateTimeOffset startedAt, DateTimeOffset now, string unity, string why)
        {
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms, new JsonWriterOptions { Indented = true }))
            {
                w.WriteStartObject();
                w.WriteNumber("schema", 1);
                w.WriteString("acceptance", options.Name);
                w.WriteString("run", options.RunLabel);
                w.WriteBoolean("control", options.IsControl);
                w.WriteBoolean("final", true);
                w.WriteBoolean("aborted", true);
                w.WriteString("startedAt", startedAt.UtcDateTime.ToString("O", CultureInfo.InvariantCulture));
                w.WriteString("finishedAt", now.UtcDateTime.ToString("O", CultureInfo.InvariantCulture));
                w.WriteString("unity", unity ?? "");
                w.WriteBoolean("pass", false);
                w.WriteNumber("exitCode", 1);
                w.WriteString("error", Redactor.Redact(why ?? ""));
                w.WriteEndObject();
            }

            return Encoding.UTF8.GetString(ms.ToArray());
        }

        void Write(DateTimeOffset now, bool final)
        {
            lastWrite = now;
            try
            {
                world.WriteFile(ResultFile, Render(now, final));
            }
            catch (Exception e) when (e is IOException || e is UnauthorizedAccessException)
            {
                log($"probe: cannot write {ResultFile}: {e.Message}");
            }
        }

        string Render(DateTimeOffset now, bool final)
        {
            using var ms = new MemoryStream();
            using (var w = new Utf8JsonWriter(ms, new JsonWriterOptions { Indented = true }))
            {
                w.WriteStartObject();
                w.WriteNumber("schema", 1);
                w.WriteString("acceptance", options.Name);
                w.WriteString("run", options.RunLabel);
                w.WriteBoolean("control", options.IsControl);
                if (options.IsControl)
                {
                    w.WriteStartObject("controlSpec");
                    w.WriteString("kind", options.Control.Kind);
                    if (options.Control.Target != null) w.WriteString("target", options.Control.Target);
                    w.WriteEndObject();
                }

                w.WriteBoolean("final", final);
                w.WriteString("phase", final ? "final" : PhaseName);
                w.WriteString("startedAt", startedAt.UtcDateTime.ToString("O", CultureInfo.InvariantCulture));
                w.WriteString("writtenAt", now.UtcDateTime.ToString("O", CultureInfo.InvariantCulture));
                if (final) w.WriteString("finishedAt", finishedAtText);
                w.WriteString("unity", world.UnityVersion ?? "");
                if (final)
                {
                    w.WriteBoolean("pass", Passed);
                    w.WriteNumber("exitCode", ExitCode);
                }

                w.WriteStartObject("timing");
                Seconds(w, "sessionsBeganSeconds", sessionsAt);
                Seconds(w, "boundSeconds", boundAt);
                Seconds(w, "credentialedSeconds", credentialedAt);
                Seconds(w, "publishingSeconds", publishingAt);
                Seconds(w, "observedSeconds", observedAt);
                w.WriteNumber("observedBudgetSeconds", ObservedBudgetSeconds);
                w.WriteEndObject();

                RenderScenarios(w);

                w.WriteStartObject("samples");
                w.WriteString("path", Redactor.Redact(world.SamplePath ?? ""));
                w.WriteNumber("lines", world.SampleLines?.Invoke() ?? 0);
                w.WriteEndObject();

                w.WriteStartArray("devices");
                foreach (var d in world.Board.Devices)
                {
                    w.WriteStartObject();
                    w.WriteString("id", d.Device.ExternalId);
                    w.WriteString("stage", d.Stage.ToString());
                    w.WriteString("side", d.Side.ToString());
                    w.WriteBoolean("failed", d.Failed);
                    w.WriteBoolean("grey", d.IsGrey);
                    if (d.Failed) w.WriteString("reason", Redactor.Redact(d.FailReason));
                    w.WriteNumber("published", d.Published);
                    w.WriteNumber("sendErrors", d.SendErrors);
                    w.WriteEndObject();
                }

                w.WriteEndArray();

                w.WriteStartArray("items");
                foreach (var i in items)
                {
                    w.WriteStartObject();
                    w.WriteString("id", i.Id);
                    w.WriteString("description", i.Description);
                    if (i.Pass.HasValue) w.WriteBoolean("pass", i.Pass.Value);
                    else w.WriteNull("pass");
                    w.WriteString("detail", Redactor.Redact(i.Detail ?? ""));
                    if (i.Seconds.HasValue) w.WriteNumber("seconds", Math.Round(i.Seconds.Value, 1));
                    w.WriteEndObject();
                }

                w.WriteEndArray();

                w.WriteStartObject("deviceLog");
                foreach (var kv in deviceLog)
                {
                    w.WriteStartArray(kv.Key);
                    foreach (var r in kv.Value)
                    {
                        w.WriteStartObject();
                        w.WriteString("at", r.At.UtcDateTime.ToString("O", CultureInfo.InvariantCulture));
                        w.WriteString("kind", r.Kind);
                        w.WriteString("text", Redactor.Redact(r.Text));
                        w.WriteEndObject();
                    }

                    w.WriteEndArray();
                }

                w.WriteEndObject();
                w.WriteEndObject();
            }

            return Encoding.UTF8.GetString(ms.ToArray());
        }

        static void Seconds(Utf8JsonWriter w, string name, double? v)
        {
            if (v.HasValue) w.WriteNumber(name, Math.Round(v.Value, 1));
            else w.WriteNull(name);
        }
    }
}
