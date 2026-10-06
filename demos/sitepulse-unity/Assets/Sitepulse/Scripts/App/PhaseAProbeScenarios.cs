// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text.Json;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Tasks;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>
    /// The probe's Phase B negative controls (<c>rule-disabled</c>, <c>observer-outage</c>) and the Phase C soak. Each is the
    /// in-player half of a run whose platform half is <c>tools/phase_b_check.py</c>: the script changes the platform (and puts it
    /// back); the probe measures only what the player can see from its own objects.
    /// </summary>
    public sealed partial class PhaseAProbe
    {
        /// <summary>The script writes this when it has changed the platform and the probe may go on (rule-disabled).</summary>
        public const string GoFile = "phaseA-go";

        /// <summary>What the banner starts with while the measurement stream is down and values were seen before.</summary>
        public const string BannerFrozen = "Observer reconnecting — values frozen";

        // rule-disabled
        public const double GoWaitSeconds = 180.0;
        public const double CrossBudgetSeconds = 150.0;
        public const double QuietWindowSeconds = 180.0;
        const double SettleAfterObservedSeconds = 5.0;
        const double FuelRiseEpsilon = 0.001;

        // observer-outage
        public const double SteadySeconds = 10.0;
        public const double LeaveBudgetSeconds = 120.0;
        public const double OutageBudgetSeconds = 240.0;
        public const double RecoveryBudgetSeconds = 60.0;
        public const double GreyAfterSeconds = 20.0;

        // soak
        /// <summary>The checker writes this once the fleet's tanks are in a state a soak can use (low trucks refuelled by the platform's own command); the soak clock starts then.</summary>
        public const string SoakGoFile = "phaseA-soak-go";
        public const double SoakGoWaitSeconds = 1500.0;
        public const double SoakWarmupSeconds = 5.0;
        public const double SoakFirstCycleSeconds = 15.0;
        public const double SoakCycleSeconds = 360.0;
        public const double SoakCycleEvalSeconds = 330.0;
        public const double SoakEndWaitSeconds = 240.0;
        public const double GapBudgetSeconds = 15.0;

        /// <summary>
        /// The longest the bay may be held by one truck: a refuel service is under a minute, and the bay wait budget is
        /// <see cref="TaskBudgets.BayWaitSeconds"/>; twice that is a hold that outlasted every budget the machine itself works to.
        /// </summary>
        public const double BayHoldBudgetSeconds = 2.0 * TaskBudgets.BayWaitSeconds;

        /// <summary>The trucks the soak runs a low-fuel cycle on, in turn: a different one each time.</summary>
        public static readonly string[] SoakRotation = { "SP-HL-0006", "SP-HL-0005", "SP-HL-0004", "SP-HL-0003", "SP-HL-0002", "SP-HL-0001" };

        string phaseText = "starting";

        string PhaseName => phaseText;

        // a phase the WSL side waits on is written the moment it begins, not at the next five-second write
        void SetPhase(DateTimeOffset now, string phase)
        {
            if (phaseText == phase) return;
            phaseText = phase;
            Write(now, false);
        }

        /// <summary>A normal run and the two Phase B controls need all 19 devices up; a fault control expects some not to be.</summary>
        bool FleetMustComeUp => !options.IsControl || options.Control.Kind == ControlSpec.RuleDisabled || options.Control.Kind == ControlSpec.ObserverOutage;

        static string Iso(DateTimeOffset t) => t.UtcDateTime.ToString("O", CultureInfo.InvariantCulture);

        // ------------------------------------------------------------------ rule-disabled

        string ruleSaid;
        DateTimeOffset? ruleCrossedAt;
        double? ruleLastFuel;
        double? ruleFinalFuel;
        readonly List<string> ruleOffTrack = new List<string>();
        readonly List<string> ruleFuelRose = new List<string>();

        void TickRuleDisabled(DateTimeOffset now, double t)
        {
            if (preparedAt == null)
            {
                if (!observedAt.HasValue)
                {
                    if (t >= ReachBudgetSeconds) Finish(now);
                    return;
                }

                if (t < observedAt.Value + SettleAfterObservedSeconds) return;
                SetPhase(now, "awaiting-go");
                if (world.FileExists(GoFile))
                {
                    preparedAt = now;
                    ruleSaid = world.PrepareLowFuel(RefuelMachine) ?? "the presenter could not prepare the tank";
                    prepared = ruleSaid;
                    SetPhase(now, "running");
                    log($"probe: {RefuelMachine}: {ruleSaid}");
                    SampleRuleMachine(now);
                }
                else if (t >= observedAt.Value + GoWaitSeconds) Finish(now);
                return;
            }

            SampleRuleMachine(now);
            var end = ruleCrossedAt.HasValue ? ruleCrossedAt.Value.AddSeconds(QuietWindowSeconds) : preparedAt.Value.AddSeconds(CrossBudgetSeconds);
            if (now >= end) Finish(now);
        }

        void SampleRuleMachine(DateTimeOffset now)
        {
            var view = world.Machine?.Invoke(RefuelMachine);
            if (!view.HasValue) return;
            var v = view.Value;
            if (!v.OnTrack && ruleOffTrack.Count < 5) ruleOffTrack.Add($"{Elapsed(now):0.0} s: {v}");
            if (ruleLastFuel.HasValue && v.FuelPct > ruleLastFuel.Value + FuelRiseEpsilon && ruleFuelRose.Count < 5)
                ruleFuelRose.Add($"{Elapsed(now):0.0} s: {ruleLastFuel.Value:0.000} -> {v.FuelPct:0.000}");
            ruleLastFuel = v.FuelPct;
            ruleFinalFuel = v.FuelPct;
            if (!ruleCrossedAt.HasValue && v.FuelPct < PresenterActions.LowFuelLinePct) ruleCrossedAt = now;
        }

        void AddRuleDisabledItems(DateTimeOffset now)
        {
            var machine = RefuelMachine;
            var told = preparedAt.HasValue;
            items.Add(new ProbeItem("rd-prepared", $"the script disabled the rule and told the player to go; the presenter's prepare-low-fuel ran on {machine}",
                told && ruleSaid != null && ruleSaid.Contains("crosses"), told ? ruleSaid : $"never told to go within {GoWaitSeconds:0} s of the fleet being observed"));
            if (!told) return;

            items.Add(new ProbeItem("rd-fuel-crossed", $"{machine}'s tank crossed {PresenterActions.LowFuelLinePct:0} % within {CrossBudgetSeconds:0} s of the prepare (the condition the rule would have fired on was real)",
                ruleCrossedAt.HasValue,
                ruleCrossedAt.HasValue ? $"crossed {(ruleCrossedAt.Value - preparedAt.Value).TotalSeconds:0.0} s after the prepare; fuel now {ruleFinalFuel:0.0} %" : $"never below {PresenterActions.LowFuelLinePct:0} % (fuel {ruleFinalFuel:0.0} %)"));
            items.Add(JudgeTimelineQuiet(world.Timeline?.Invoke(), machine, preparedAt.Value));
            items.Add(new ProbeItem("rd-stayed-on-track", $"{machine} never left its routine track or queued for the bay",
                ruleOffTrack.Count == 0, ruleOffTrack.Count == 0 ? "working its track through the whole window" : "off track at " + string.Join("; ", ruleOffTrack)));
            items.Add(new ProbeItem("rd-fuel-never-rose", $"{machine}'s tank never rose (nothing refuelled it)",
                ruleFuelRose.Count == 0, ruleFuelRose.Count == 0 ? $"monotonic down to {ruleFinalFuel:0.0} %" : "rose at " + string.Join("; ", ruleFuelRose)));
            var token = world.Board.TokenOf(machine);
            items.Add(JudgePlatformSilent(world.Observed, token, preparedAt.Value));
        }

        /// <summary>
        /// The machine's own account holds nothing a command would have caused: no command received, accepted, serviced, arrived at,
        /// superseded or returned from after <paramref name="since"/>. Presenter rows and a stall are the machine running out, not reacting.
        /// </summary>
        public static ProbeItem JudgeTimelineQuiet(Timeline timeline, string machine, DateTimeOffset since)
        {
            const string Id = "rd-no-command-received";
            var what = $"{machine}'s own timeline shows no command received and no task run after the tank was prepared";
            if (timeline == null) return new ProbeItem(Id, what, false, "the task layer never started");
            var reacted = new List<string>();
            foreach (var r in timeline.Rows(machine))
            {
                if (r.At < since) continue;
                if (r.Kind == TimelineKinds.Presenter || r.Kind == TimelineKinds.Stalled) continue;
                reacted.Add($"{r.Kind}: {r.Text}");
            }

            return new ProbeItem(Id, what, reacted.Count == 0, reacted.Count == 0 ? "only presenter and stall rows" : "the machine did something: " + string.Join("; ", reacted.GetRange(0, Math.Min(4, reacted.Count))));
        }

        /// <summary>
        /// What the platform reported to the observer: the tank really is low (so the silence is not a quiet machine) and still no
        /// low-fuel alarm is active and no command was queued for the device since the tank was prepared.
        /// </summary>
        public static ProbeItem JudgePlatformSilent(ObservedState observed, string deviceToken, DateTimeOffset since)
        {
            const string Id = "rd-platform-silent";
            var what = "the observer saw the low tank reach the platform and saw no low-fuel alarm and no command for the device";
            if (observed == null || deviceToken == null || !observed.TryGet(deviceToken, out var device)) return new ProbeItem(Id, what, false, "the observer has nothing for the device");
            var problems = new List<string>();
            if (!device.Measurements.TryGetValue(MeasurementKeys.FuelPct, out var fuel)) problems.Add("no observed fuel_pct");
            else if (fuel.Value >= PresenterActions.LowFuelLinePct) problems.Add($"the platform's last fuel_pct is {fuel.Value:0.0} %, not below the line: the low tank never reached it");
            foreach (var alarm in device.Alarms.Values)
                if (alarm.AlarmKey == AlarmKeys.LowFuel && (alarm.IsActive || alarm.OccurredAt >= since)) problems.Add($"the platform reported a {alarm.AlarmKey} alarm ({alarm.State})");
            var last = device.LastCommand;
            if (last != null && last.QueuedAt >= since) problems.Add($"the platform queued {last.Name} ({last.Status})");
            return new ProbeItem(Id, what, problems.Count == 0,
                problems.Count == 0 ? $"observed fuel {fuel.Value:0.0} %, no {AlarmKeys.LowFuel} alarm, no command queued since the prepare" : string.Join("; ", problems));
        }

        // ------------------------------------------------------------------ observer-outage

        DateTimeOffset? outageSteadyAt, outageLeftAt, outageBackAt, outageAllObservedAt;
        bool outageBannerSeen;
        int outageReconnectsBefore, outageSnapshotsBefore, outageChecks, outageGreyChecks;
        readonly List<string> outageFresh = new List<string>();
        readonly List<string> outageNotGrey = new List<string>();
        readonly Dictionary<string, long> publishedAtLeft = new Dictionary<string, long>(StringComparer.Ordinal);
        readonly Dictionary<string, long> publishedAtBack = new Dictionary<string, long>(StringComparer.Ordinal);
        long sendErrorsAtLeft, sendErrorsAtBack;

        void TickObserverOutage(DateTimeOffset now, double t)
        {
            var observer = world.Observer;
            if (observer == null) { Finish(now); return; }
            if (!outageSteadyAt.HasValue)
            {
                if (!observedAt.HasValue)
                {
                    if (t >= ReachBudgetSeconds) Finish(now);
                    return;
                }

                if (t >= observedAt.Value + SteadySeconds && observer.Measurements.IsLive)
                {
                    outageSteadyAt = now;
                    outageReconnectsBefore = observer.Measurements.Reconnects;
                    outageSnapshotsBefore = world.Observed.SnapshotMeasurements;
                    SetPhase(now, "steady");
                    Write(now, false);
                }

                return;
            }

            var live = observer.Measurements.IsLive;
            if (!outageLeftAt.HasValue)
            {
                if (!live)
                {
                    outageLeftAt = now;
                    SetPhase(now, "outage");
                    SnapshotPublished(publishedAtLeft, out sendErrorsAtLeft);
                }
                else if ((now - outageSteadyAt.Value).TotalSeconds > LeaveBudgetSeconds) Finish(now);
                return;
            }

            if (!outageBackAt.HasValue)
            {
                // judged only while the stream is down: the tick on which it returns is the next phase's
                if (!live)
                {
                    var text = ObserverBanner.Text(observer, world.Observed);
                    if (text != null && text.StartsWith(BannerFrozen, StringComparison.Ordinal)) outageBannerSeen = true;
                    outageChecks++;
                    // what the cards are TOLD about the stream is what decides how they read: it must say the stream is down
                    var cardsLive = world.CardStreamLive?.Invoke();
                    if (cardsLive != false && outageFresh.Count < 5) outageFresh.Add(cardsLive.HasValue ? "the cards' source is told the stream is live" : "the cards have no source to tell");
                    foreach (var p in ValuesAgainstTheStream(now, cardsLive ?? false, false)) if (outageFresh.Count < 5) outageFresh.Add(p);
                    if ((now - outageLeftAt.Value).TotalSeconds >= GreyAfterSeconds)
                    {
                        outageGreyChecks++;
                        foreach (var p in ValuesAgainstTheStream(now, cardsLive ?? false, true)) if (outageNotGrey.Count < 5) outageNotGrey.Add(p);
                    }
                }

                if (live)
                {
                    outageBackAt = now;
                    SetPhase(now, "recovering");
                    SnapshotPublished(publishedAtBack, out sendErrorsAtBack);
                    Write(now, false);
                }
                else if ((now - outageLeftAt.Value).TotalSeconds > OutageBudgetSeconds) Finish(now);
                return;
            }

            if (world.Board.ObservedCount == world.Board.Total)
            {
                outageAllObservedAt = now;
                Finish(now);
            }
            else if ((now - outageBackAt.Value).TotalSeconds > RecoveryBudgetSeconds) Finish(now);
        }

        void SnapshotPublished(Dictionary<string, long> into, out long sendErrors)
        {
            sendErrors = world.SendErrors?.Invoke() ?? 0;
            into.Clear();
            foreach (var d in world.Board.Devices) into[d.Device.ExternalId] = d.Published;
        }

        /// <summary>
        /// What the cards would say about the values the observer holds, judged by the card presenter itself while the stream is not
        /// live: a row never reads fresh (ink or warning), and the dot never reads Ok. With <paramref name="mustBeGrey"/> every row must
        /// be grey (old, or a dash), which is where a value that has not been seen for 15 s has to be.
        /// </summary>
        List<string> ValuesAgainstTheStream(DateTimeOffset now, bool live, bool mustBeGrey)
        {
            var problems = new List<string>();
            foreach (var d in world.Board.Devices)
            {
                var token = world.Board.TokenOf(d.Device.ExternalId);
                if (token == null || !world.Observed.TryGet(token, out var od)) continue;
                foreach (var kv in od.Measurements)
                {
                    var tone = CardPresenter.Row(true, "x", kv.Value.OccurredAt, now, live).Tone;
                    var bad = mustBeGrey ? tone != RowTone.Grey : (tone == RowTone.Ink || tone == RowTone.Warn);
                    if (bad) problems.Add($"{d.Device.ExternalId} {kv.Key} reads {tone} {(now - kv.Value.OccurredAt).TotalSeconds:0} s after it happened");
                }

                if (od.NewestOwnRunAt.HasValue && CardPresenter.Status(Provenance.Observed, od.NewestOwnRunAt, now, live, false, false).Dot == DotTone.Ok)
                    problems.Add($"{d.Device.ExternalId}'s dot reads Ok");
            }

            return problems;
        }

        void AddObserverOutageItems(DateTimeOffset now)
        {
            var observer = world.Observer;
            items.Add(new ProbeItem("oo-steady", "the fleet was observed and the measurement stream Live before the outage", outageSteadyAt.HasValue,
                outageSteadyAt.HasValue ? "steady" : "never steady"));
            if (!outageSteadyAt.HasValue || observer == null) return;
            items.Add(new ProbeItem("oo-left-live", "the measurement stream left Live while event-management was away", outageLeftAt.HasValue,
                outageLeftAt.HasValue ? $"left Live {(outageLeftAt.Value - outageSteadyAt.Value).TotalSeconds:0.0} s after steady" : $"still {observer.Measurements.State} {LeaveBudgetSeconds:0} s after steady"));
            if (!outageLeftAt.HasValue) return;
            items.Add(new ProbeItem("oo-banner", $"the screen said \"{BannerFrozen}\" during the outage", outageBannerSeen, outageBannerSeen ? $"seen over {outageChecks} checks" : "the banner never said it"));
            items.Add(new ProbeItem("oo-never-fresh", "no card value or dot read fresh while the stream was down", outageFresh.Count == 0 && outageChecks > 0,
                outageFresh.Count == 0 ? $"{outageChecks} checks, none fresh" : string.Join("; ", outageFresh)));
            items.Add(new ProbeItem("oo-values-grey", $"every card value was grey {GreyAfterSeconds:0} s into the outage", outageNotGrey.Count == 0 && outageGreyChecks > 0,
                outageGreyChecks == 0 ? "the outage ended before the check" : outageNotGrey.Count == 0 ? $"{outageGreyChecks} checks, all grey" : string.Join("; ", outageNotGrey)));
            if (outageBackAt.HasValue) items.Add(PublishingThrough(outageBackAt.Value));
            else items.Add(new ProbeItem("oo-devices-publishing", "the devices kept publishing through the outage", false, "the outage did not end, so there is no window to measure"));
            items.Add(new ProbeItem("oo-returned-live", "the measurement stream returned to Live and the observer counted a reconnect", outageBackAt.HasValue && observer.Measurements.Reconnects > outageReconnectsBefore,
                outageBackAt.HasValue ? $"Live again {(outageBackAt.Value - outageLeftAt.Value).TotalSeconds:0.0} s after leaving; reconnects {outageReconnectsBefore} -> {observer.Measurements.Reconnects}"
                    : $"still {observer.Measurements.State} {(now - outageLeftAt.Value).TotalSeconds:0} s after leaving"));
            if (!outageBackAt.HasValue) return;
            var snapshots = world.Observed.SnapshotMeasurements - outageSnapshotsBefore;
            items.Add(new ProbeItem("oo-snapshot-refresh", "reconnecting took a snapshot of the platform's measurements", snapshots > 0, $"{snapshots} measurement(s) applied from a snapshot since the outage began"));
            var banner = ObserverBanner.Text(observer, world.Observed);
            items.Add(new ProbeItem("oo-banner-cleared", "the banner went away once the stream was Live", banner == null, banner ?? "no banner"));
            items.Add(new ProbeItem("oo-all-observed-again", $"every device was observed again within {RecoveryBudgetSeconds:0} s of the stream returning", outageAllObservedAt.HasValue,
                outageAllObservedAt.HasValue ? $"{world.Board.Total}/{world.Board.Total} observed {(outageAllObservedAt.Value - outageBackAt.Value).TotalSeconds:0.0} s after Live"
                    : $"{world.Board.ObservedCount}/{world.Board.Total} observed {RecoveryBudgetSeconds:0} s after Live"));
        }

        ProbeItem PublishingThrough(DateTimeOffset back)
        {
            var seconds = (back - outageLeftAt.Value).TotalSeconds;
            var need = Math.Max(1.0, Math.Floor(seconds * 0.3));
            var slow = new List<string>();
            foreach (var d in world.Board.Devices)
            {
                var id = d.Device.ExternalId;
                if (d.Failed) { slow.Add(id + " failed"); continue; }
                publishedAtLeft.TryGetValue(id, out var before);
                publishedAtBack.TryGetValue(id, out var after);
                if (after - before < need) slow.Add($"{id} +{after - before}");
            }

            var errors = sendErrorsAtBack - sendErrorsAtLeft;
            if (errors > 0) slow.Add($"{errors} send error(s)");
            return new ProbeItem("oo-devices-publishing", $"all 19 devices kept publishing through the {seconds:0} s outage (at least {need:0} each, no send errors)", slow.Count == 0,
                slow.Count == 0 ? $"every device published at least {need:0} sample(s) while the observer was blind" : string.Join("; ", slow));
        }

        // ------------------------------------------------------------------ soak

        sealed class SoakCycle
        {
            public int Index;
            public string Machine;
            public DateTimeOffset At;
            public double AtSeconds;
            public string Said;
            public bool Prepared;
            public ProbeItem Result;
        }

        sealed class MinuteRow
        {
            public int Minute, Sessions, Observed, Presence, Reconnecting;
            public long Dropped;
            public double? FrameP95Ms;
            public string BayHolder;
            public int BayWaiting;
        }

        readonly Quantiles frames = new Quantiles();
        readonly Quantiles minuteFrames = new Quantiles();
        readonly Quantiles lag = new Quantiles();
        readonly List<SoakCycle> cycles = new List<SoakCycle>();
        readonly List<MinuteRow> minuteRows = new List<MinuteRow>();
        readonly Dictionary<string, DateTimeOffset> gapStart = new Dictionary<string, DateTimeOffset>(StringComparer.Ordinal);
        readonly HashSet<string> reconnecting = new HashSet<string>(StringComparer.Ordinal);
        readonly List<string> gapsOver = new List<string>();
        DateTimeOffset? soakStart, soakEndedAt;
        bool soakEnded, lagHooked;
        int nextCycle, sessionsMin = int.MaxValue, sessionsMax, reconnectEpisodes, presenceMin = int.MaxValue;
        long dropped, soakSendErrors;
        double maxGap, maxBayHold;
        string maxGapDevice, bayHolder;
        DateTimeOffset bayHolderSince;
        BayView? endBay;

        /// <summary>How many low-fuel cycles a soak of <paramref name="minutes"/> runs: one every 6 minutes, each with time to finish, and at least one.</summary>
        public static int ScheduledCycles(int minutes)
        {
            var n = 0;
            while (SoakFirstCycleSeconds + n * SoakCycleSeconds + SoakCycleEvalSeconds <= minutes * 60.0) n++;
            return Math.Max(1, n);
        }

        /// <summary>How long a soak runs once the fleet is observed: its minutes, or longer when its last cycle needs the time.</summary>
        public static double SoakSeconds(int minutes) => Math.Max(minutes * 60.0, SoakFirstCycleSeconds + (ScheduledCycles(minutes) - 1) * SoakCycleSeconds + SoakCycleEvalSeconds);

        void HookLag()
        {
            if (lagHooked || world.Observed == null) return;
            lagHooked = true;
            world.Observed.MeasurementLag = span => { if (soakStart.HasValue) lag.Add(span.TotalMilliseconds); };
        }

        void SampleFrame()
        {
            if (!options.IsSoak || !soakStart.HasValue || world.FrameSeconds == null) return;
            var dt = world.FrameSeconds();
            if (dt <= 0.0) return;
            frames.Add(dt);
            minuteFrames.Add(dt);
        }

        void TickSoak(DateTimeOffset now, double t)
        {
            HookLag();
            if (!cardsDone && ((observedAt.HasValue && t >= observedAt.Value + CardsAfterObservedSeconds) || t >= ReachBudgetSeconds))
            {
                cardsDone = true;
                cardsAt = now;
                items.Add(CheckCards(world));
            }

            if (cardsDone && !observedAt.HasValue) { Finish(now); return; }
            if (!soakStart.HasValue)
            {
                if (!cardsDone) return;
                if (world.FileExists(SoakGoFile))
                {
                    soakStart = now;
                    SetPhase(now, "running");
                }
                else if ((now - cardsAt.Value).TotalSeconds >= SoakGoWaitSeconds)
                {
                    items.Add(new ProbeItem("soak-go", "the checker released the soak (after any precondition refuels)", false, $"no {SoakGoFile} within {SoakGoWaitSeconds:0} s of the fleet being observed"));
                    Finish(now);
                }
                else SetPhase(now, "awaiting-soak-go");

                return;
            }

            var s = (now - soakStart.Value).TotalSeconds;
            SampleSoak(now, s);
            RunCycles(now, s);
            if (!soakEnded && s >= SoakSeconds(options.SoakMinutes))
            {
                soakEnded = true;
                soakEndedAt = now;
                SetPhase(now, "ending");
                endBay = world.Bay?.Invoke();
                log("probe: the soak is over; waiting for the platform-side checks");
                Write(now, false);
            }

            if (soakEnded && (world.FileExists(FinishFile) || (now - soakEndedAt.Value).TotalSeconds >= SoakEndWaitSeconds)) Finish(now);
        }

        int rotationCursor;

        /// <summary>
        /// The truck a soak cycle runs on: from <paramref name="start"/> on, in rotation order, the first one that is working
        /// its track and whose tank is above the preparation line (a truck already at or below it has nothing to cross, and
        /// its low-fuel rule only fires on a crossing). Returns null when none qualifies; the skipped trucks come back with why.
        /// </summary>
        public static (string Machine, List<string> Skipped) PickSoakMachine(int start, Func<string, MachineView?> view)
        {
            var skipped = new List<string>();
            for (var i = 0; i < SoakRotation.Length; i++)
            {
                var id = SoakRotation[(start + i) % SoakRotation.Length];
                var v = view(id);
                if (!v.HasValue) skipped.Add($"{id} (unknown)");
                else if (!v.Value.OnTrack) skipped.Add($"{id} ({v.Value}, not working its track)");
                else if (v.Value.FuelPct <= PresenterActions.JustAbovePct) skipped.Add($"{id} (fuel {v.Value.FuelPct:0.0}%, at or below the preparation line)");
                else return (id, skipped);
            }

            return (null, skipped);
        }

        void RunCycles(DateTimeOffset now, double s)
        {
            var scheduled = ScheduledCycles(options.SoakMinutes);
            while (nextCycle < scheduled && s >= SoakFirstCycleSeconds + nextCycle * SoakCycleSeconds)
            {
                var (pick, skipped) = PickSoakMachine(rotationCursor, id => world.Machine?.Invoke(id));
                var machine = pick ?? SoakRotation[rotationCursor % SoakRotation.Length];
                var cycle = new SoakCycle { Index = nextCycle + 1, Machine = machine, At = now, AtSeconds = s };
                var skipNote = skipped.Count == 0 ? "" : "skipped " + string.Join("; ", skipped) + ". ";
                if (pick == null)
                {
                    cycle.Said = skipNote + "not prepared: no truck in the rotation could be prepared";
                    cycle.Result = new ProbeItem($"soak-cycle-{cycle.Index}", $"low-fuel cycle {cycle.Index} on {machine}: the platform's goto-refuel, serviced, ended SUCCESS", false, cycle.Said);
                    rotationCursor++;
                }
                else
                {
                    rotationCursor = Array.IndexOf(SoakRotation, pick) + 1;
                    cycle.Said = skipNote + (world.PrepareLowFuel(machine) ?? "the presenter could not prepare the tank");
                    cycle.Prepared = cycle.Said.Contains("crosses");
                    log($"probe: soak cycle {cycle.Index}: {machine}: {cycle.Said}");
                    if (!cycle.Prepared)
                        cycle.Result = new ProbeItem($"soak-cycle-{cycle.Index}", $"low-fuel cycle {cycle.Index} on {machine}: the platform's goto-refuel, serviced, ended SUCCESS", false, cycle.Said);
                }

                cycles.Add(cycle);
                nextCycle++;
            }

            foreach (var c in cycles)
                if (c.Result == null && s >= c.AtSeconds + SoakCycleEvalSeconds) EvaluateCycle(c);
        }

        void EvaluateCycle(SoakCycle c)
        {
            var r = EvaluateRefuelLog(world.Timeline?.Invoke(), c.Machine, c.At);
            c.Result = new ProbeItem($"soak-cycle-{c.Index}", $"low-fuel cycle {c.Index} on {c.Machine}: the platform's goto-refuel, serviced, ended SUCCESS", r.Pass, r.Detail);
        }

        static bool GoodNow(DeviceReadiness d) => d.Stage == DeviceStage.Observed && d.Side == DeviceSide.None && !d.Failed && !d.Stalled && !d.Quiet;

        void CloseGap(string id, double seconds)
        {
            if (seconds > maxGap)
            {
                maxGap = seconds;
                maxGapDevice = id;
            }

            if (seconds > GapBudgetSeconds) gapsOver.Add($"{id} {seconds:0.0} s");
        }

        void SampleSoak(DateTimeOffset now, double s)
        {
            var sessions = world.SessionCount?.Invoke() ?? -1;
            if (sessions >= 0)
            {
                if (sessions < sessionsMin) sessionsMin = sessions;
                if (sessions > sessionsMax) sessionsMax = sessions;
            }

            dropped = world.DroppedSamples?.Invoke() ?? 0;
            soakSendErrors = world.SendErrors?.Invoke() ?? 0;
            var presence = 0;
            var reconnectingNow = 0;
            foreach (var d in world.Board.Devices)
            {
                var id = d.Device.ExternalId;
                if (!GoodNow(d)) { if (!gapStart.ContainsKey(id)) gapStart[id] = now; }
                else if (gapStart.TryGetValue(id, out var from))
                {
                    CloseGap(id, (now - from).TotalSeconds);
                    gapStart.Remove(id);
                }

                if (d.Side == DeviceSide.Reconnecting)
                {
                    reconnectingNow++;
                    if (reconnecting.Add(id)) reconnectEpisodes++;
                }
                else reconnecting.Remove(id);

                var token = world.Board.TokenOf(id);
                if (token != null && world.Observed.TryGet(token, out var od) && od.Presence != null && od.Presence.Active) presence++;
            }

            if (s >= 60.0 && presence < presenceMin) presenceMin = presence;

            var bay = world.Bay?.Invoke();
            var holder = bay.HasValue ? bay.Value.Holder : null;
            if (holder != bayHolder)
            {
                bayHolder = holder;
                bayHolderSince = now;
            }
            else if (holder != null)
            {
                var held = (now - bayHolderSince).TotalSeconds;
                if (held > maxBayHold) maxBayHold = held;
            }

            var minute = (int)(s / 60.0);
            while (minuteRows.Count < minute)
            {
                minuteRows.Add(new MinuteRow
                {
                    Minute = minuteRows.Count + 1,
                    Sessions = sessions,
                    Observed = world.Board.ObservedCount,
                    Presence = presence,
                    Reconnecting = reconnectingNow,
                    Dropped = dropped,
                    FrameP95Ms = minuteFrames.Of(0.95) * 1000.0,
                    BayHolder = holder,
                    BayWaiting = bay.HasValue ? bay.Value.Waiting : 0,
                });
                minuteFrames.Clear();
            }
        }

        void AddSoakItems(DateTimeOffset now)
        {
            foreach (var kv in gapStart) CloseGap(kv.Key, (now - kv.Value).TotalSeconds);
            gapStart.Clear();
            var scheduled = ScheduledCycles(options.SoakMinutes);
            foreach (var c in cycles)
            {
                if (c.Result == null) EvaluateCycle(c);
                items.Add(c.Result);
            }

            var ran = cycles.Count == scheduled && cycles.TrueForAll(c => c.Prepared);
            items.Add(new ProbeItem("soak-cycles", "every scheduled low-fuel cycle was prepared on a different truck", ran,
                $"{cycles.Count} of {scheduled} cycle(s) prepared on {string.Join(", ", cycles.ConvertAll(c => c.Machine))}"));
            var total = world.Board.Total;
            items.Add(new ProbeItem("soak-sessions", $"the plane held {total} sessions throughout (none leaked, none duplicated)", sessionsMin == total && sessionsMax == total,
                sessionsMax == 0 && sessionsMin == int.MaxValue ? "the session count was never read" : $"min {sessionsMin}, max {sessionsMax}"));
            var observedNow = world.Board.ObservedCount;
            items.Add(new ProbeItem("soak-observed", $"all {total} devices stay observed (no gap over {GapBudgetSeconds:0} s)", observedNow == total && gapsOver.Count == 0,
                $"{observedNow}/{total} observed at the end; longest gap {maxGap:0.0} s{(maxGapDevice != null ? " (" + maxGapDevice + ")" : "")}" + (gapsOver.Count > 0 ? "; over the budget: " + string.Join(", ", gapsOver) : "")));
            items.Add(new ProbeItem("soak-dropped", "no sample was dropped by an outbound ring", dropped == 0, $"{dropped} dropped"));
            items.Add(new ProbeItem("soak-send-errors", "sends the sessions reported as failed", null, $"{soakSendErrors} send error(s), {reconnectEpisodes} reconnect episode(s)"));
            var bay = endBay ?? world.Bay?.Invoke();
            var off = new List<string>();
            foreach (var m in SoakRotation)
            {
                var v = world.Machine?.Invoke(m);
                if (!v.HasValue || !v.Value.OnTrack) off.Add($"{m} {(v.HasValue ? v.Value.ToString() : "unknown")}");
            }

            var holdBudget = BayHoldBudgetSeconds;
            var free = bay.HasValue && bay.Value.Holder == null && bay.Value.Waiting == 0;
            items.Add(new ProbeItem("soak-reservations", "no deadlocked reservation: the bay is free and empty, no truck is left off its track, and no hold outlasted its budget",
                free && off.Count == 0 && maxBayHold <= holdBudget,
                $"bay {(bay.HasValue ? (bay.Value.Holder ?? "free") + ", " + bay.Value.Waiting + " waiting" : "unknown")}; longest hold {maxBayHold:0.0} s (budget {holdBudget:0} s)" + (off.Count > 0 ? "; off track: " + string.Join(", ", off) : "")));
            items.Add(new ProbeItem("soak-presence", "the platform's presence for the fleet as the observer saw it", null, presenceMin == int.MaxValue ? "not read" : $"fewest devices active in any sample after the first minute: {presenceMin}"));
            items.Add(new ProbeItem("soak-frame-time", "frame time (Time.unscaledDeltaTime) while the fleet ran", null, QuantileText(frames, "ms", 1000.0)));
            items.Add(new ProbeItem("soak-observation-lag", "observation lag (observed receivedAt - occurredTime) of streamed measurements", null, QuantileText(lag, "ms", 1.0)));
        }

        static string QuantileText(Quantiles q, string unit, double scale)
        {
            if (q.Count == 0) return "none recorded";
            return string.Format(CultureInfo.InvariantCulture, "n={0}, p50 {1:0.0} {4}, p95 {2:0.0} {4}, p99 {3:0.0} {4}", q.Count, q.Of(0.5) * scale, q.Of(0.95) * scale, q.Of(0.99) * scale, unit);
        }

        static void Quant(Utf8JsonWriter w, string name, Quantiles q, double scale)
        {
            w.WriteStartObject(name);
            w.WriteNumber("n", q.Count);
            Ms(w, "p50Ms", q.Of(0.5), scale);
            Ms(w, "p95Ms", q.Of(0.95), scale);
            Ms(w, "p99Ms", q.Of(0.99), scale);
            Ms(w, "maxMs", q.Max, scale);
            w.WriteEndObject();
        }

        static void Ms(Utf8JsonWriter w, string name, double? v, double scale)
        {
            if (v.HasValue) w.WriteNumber(name, Math.Round(v.Value * scale, 2));
            else w.WriteNull(name);
        }

        // ------------------------------------------------------------------ the result file

        void RenderScenarios(Utf8JsonWriter w)
        {
            if (options.IsControl && options.Control.Kind == ControlSpec.RuleDisabled)
            {
                w.WriteStartObject("ruleDisabled");
                if (preparedAt.HasValue) w.WriteString("preparedAt", Iso(preparedAt.Value));
                if (ruleCrossedAt.HasValue) w.WriteString("crossedAt", Iso(ruleCrossedAt.Value));
                if (ruleFinalFuel.HasValue) w.WriteNumber("fuelPct", Math.Round(ruleFinalFuel.Value, 2));
                w.WriteEndObject();
            }
            else if (options.IsControl && options.Control.Kind == ControlSpec.ObserverOutage)
            {
                w.WriteStartObject("observerOutage");
                if (outageSteadyAt.HasValue) w.WriteString("steadyAt", Iso(outageSteadyAt.Value));
                if (outageLeftAt.HasValue) w.WriteString("leftLiveAt", Iso(outageLeftAt.Value));
                if (outageBackAt.HasValue) w.WriteString("backLiveAt", Iso(outageBackAt.Value));
                if (outageAllObservedAt.HasValue) w.WriteString("allObservedAt", Iso(outageAllObservedAt.Value));
                w.WriteBoolean("bannerSeen", outageBannerSeen);
                w.WriteEndObject();
            }
            else if (options.IsSoak) RenderSoak(w);
        }

        void RenderSoak(Utf8JsonWriter w)
        {
            w.WriteStartObject("soak");
            w.WriteNumber("minutes", options.SoakMinutes);
            w.WriteNumber("seconds", SoakSeconds(options.SoakMinutes));
            w.WriteBoolean("ended", soakEnded);
            if (soakStart.HasValue) w.WriteString("startedAt", Iso(soakStart.Value));
            if (soakEndedAt.HasValue) w.WriteString("endedAt", Iso(soakEndedAt.Value));
            w.WriteStartArray("cycles");
            foreach (var c in cycles)
            {
                w.WriteStartObject();
                w.WriteNumber("index", c.Index);
                w.WriteString("machine", c.Machine);
                w.WriteString("preparedAt", Iso(c.At));
                w.WriteBoolean("prepared", c.Prepared);
                w.WriteString("said", Redactor.Redact(c.Said ?? ""));
                w.WriteString("outcome", Redactor.Redact(c.Result == null ? "not evaluated yet" : (c.Result.Pass == true ? "SUCCESS: " : "FAILED: ") + c.Result.Detail));
                w.WriteEndObject();
            }

            w.WriteEndArray();
            Quant(w, "frame", frames, 1000.0);
            Quant(w, "lag", lag, 1.0);
            w.WriteStartArray("perMinute");
            foreach (var m in minuteRows)
            {
                w.WriteStartObject();
                w.WriteNumber("minute", m.Minute);
                w.WriteNumber("sessions", m.Sessions);
                w.WriteNumber("observed", m.Observed);
                w.WriteNumber("presenceActive", m.Presence);
                w.WriteNumber("reconnecting", m.Reconnecting);
                w.WriteNumber("dropped", m.Dropped);
                Ms(w, "frameP95Ms", m.FrameP95Ms, 1.0);
                if (m.BayHolder != null) w.WriteString("bayHolder", m.BayHolder);
                w.WriteNumber("bayWaiting", m.BayWaiting);
                w.WriteEndObject();
            }

            w.WriteEndArray();
            w.WriteStartObject("gaps");
            w.WriteNumber("maxSeconds", Math.Round(maxGap, 1));
            if (maxGapDevice != null) w.WriteString("device", maxGapDevice);
            w.WriteStartArray("over15s");
            foreach (var g in gapsOver) w.WriteStringValue(g);
            w.WriteEndArray();
            w.WriteEndObject();
            w.WriteStartObject("sessions");
            w.WriteNumber("expected", world.Board.Total);
            w.WriteNumber("min", sessionsMin == int.MaxValue ? 0 : sessionsMin);
            w.WriteNumber("max", sessionsMax);
            w.WriteEndObject();
            w.WriteNumber("dropped", dropped);
            w.WriteNumber("sendErrors", soakSendErrors);
            w.WriteNumber("reconnectEpisodes", reconnectEpisodes);
            w.WriteNumber("maxBayHoldSeconds", Math.Round(maxBayHold, 1));
            w.WriteEndObject();
        }
    }
}
