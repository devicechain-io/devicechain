// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;
using DeviceChain.Sitepulse.Platform;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>The video run's command line: <c>-sitepulse-video-run</c> (no value), with <c>-sitepulse-mode live</c> and without <c>-sitepulse-acceptance</c>.</summary>
    public static class VideoRunFlags
    {
        public const string Flag = "-sitepulse-video-run";

        public static bool Requested(IReadOnlyList<string> args)
        {
            if (args == null) return false;
            foreach (var a in args)
                if (a == Flag) return true;
            return false;
        }
    }

    /// <summary>What the video run may look at and do; every member is a delegate, so the run is exercised over a fake.</summary>
    public sealed class VideoRunWorld
    {
        public Func<DateTimeOffset> Clock { get; set; }

        /// <summary>Every device has been observed by the platform (the readiness board's own answer).</summary>
        public Func<bool> FleetObserved { get; set; }

        /// <summary>Why the fleet is not observed yet (which devices, and whether one failed): said while the run waits, and when it gives up. Optional.</summary>
        public Func<string> WhyNotObserved { get; set; }

        /// <summary>The presenter's prepare-low-fuel on a machine (inputs only); returns what it said, or null if there is no such machine.</summary>
        public Func<string, string> PrepareLowFuel { get; set; }

        /// <summary>The presenter's puncture on a machine (inputs only); returns what it said, or null.</summary>
        public Func<string, string> PrepareTyreLeak { get; set; }

        /// <summary>The platform says this machine has this alarm active.</summary>
        public Func<string, string, bool> AlarmActive { get; set; }

        /// <summary>The tokens of the commands of this name the platform has already told the observer about for this machine.</summary>
        public Func<string, string, IReadOnlyCollection<string>> CommandTokens { get; set; }

        /// <summary>
        /// The platform says a command of this name for this machine, whose token is NOT among the given ones, is SUCCESSFUL. A command is told
        /// apart by its token, never by a time: the platform's queued time is the cluster's clock and this app's clock is another.
        /// </summary>
        public Func<string, string, IReadOnlyCollection<string>, bool> CommandSuccessful { get; set; }

        /// <summary>The machine is on its routine track again (the device's own account).</summary>
        public Func<string, bool> OnTrack { get; set; }

        public Action<string> Log { get; set; }
        public Action<int> Quit { get; set; }
    }

    /// <summary>What the video run reads of a machine's commands in the observed state.</summary>
    public static class VideoCommands
    {
        public static HashSet<string> Tokens(ObservedDevice device, string name)
        {
            var tokens = new HashSet<string>(StringComparer.Ordinal);
            if (device == null) return tokens;
            foreach (var c in device.Commands.Values)
                if (c.Name == name) tokens.Add(c.Token);
            return tokens;
        }

        /// <summary>True when a command of this name that is not among <paramref name="known"/> has been reported SUCCESSFUL.</summary>
        public static bool NewSuccessful(ObservedDevice device, string name, IReadOnlyCollection<string> known)
        {
            if (device == null) return false;
            foreach (var c in device.Commands.Values)
                if (c.Name == name && c.Status == "SUCCESSFUL" && !Contains(known, c.Token)) return true;
            return false;
        }

        static bool Contains(IReadOnlyCollection<string> known, string token)
        {
            if (known == null) return false;
            foreach (var k in known)
                if (k == token) return true;
            return false;
        }
    }

    /// <summary>One thing the run did or waited for: when, and what happened.</summary>
    public readonly struct VideoStep
    {
        public VideoStep(string name, double atSeconds, string note)
        {
            Name = name;
            AtSeconds = atSeconds;
            Note = note;
        }

        public string Name { get; }
        public double AtSeconds { get; }
        public string Note { get; }
    }

    /// <summary>
    /// A presenter-driven sequence for ONE live take of the feature video (<c>-sitepulse-video-run</c>): once every device is observed it lets the
    /// fleet work for a while (the establishing shots), prepares SP-HL-0006's low-fuel cycle and waits for the platform's rule to do the rest
    /// (the alarm, the refuel command, the success, the clear) and for the truck to be back at work, lets the fleet work again, punctures
    /// SP-HL-0003's tyre and waits for the platform's tyre alarm, then waits for the operator's <c>goto-area sp-zone-yard</c> to SP-HL-0003 (sent from the console
    /// by the maintainer, or by the WSL driver in an unattended take) to succeed, and closes after a short tail. It changes inputs only, as the presenter's
    /// keys do: it never creates a command or an alarm, never raises a tank, and writes nothing to the platform. A wait that runs out is said
    /// out loud, and the run goes on (the recording then lacks the event, and a render that asks for it stops and says so).
    /// </summary>
    public sealed class VideoRun
    {
        public const string Protagonist = "SP-HL-0006", Puncture = "SP-HL-0003";
        public const string OperatorArea = "sp-zone-yard";

        /// <summary>The fleet works this long, undisturbed, before the low-fuel cycle: the establishing shots, the cards, the panel and the plant.</summary>
        public const double SteadyFirstSeconds = 150.0;

        /// <summary>The low-fuel cycle (about a minute to the alarm, the drive, 40 s at the bay, the drive back) is waited for this long.</summary>
        public const double RefuelCycleWaitSeconds = 480.0;

        public const double SteadyBetweenSeconds = 45.0;

        /// <summary>The run waits this long for every device to be observed, saying why not every <see cref="WaitingLogSeconds"/>, and then gives up.</summary>
        public const double FleetWaitSeconds = 300.0;

        public const double WaitingLogSeconds = 10.0;

        /// <summary>After the tyre alarm the truck is left driving for this long, so its card, drawer and chase shots are all in the recording.</summary>
        public const double AfterTyreAlarmSeconds = 90.0;

        public const double TyreAlarmWaitSeconds = 150.0;
        public const double OperatorWaitSeconds = 480.0;
        public const double TailSeconds = 45.0;

        enum Stage { Waiting, Steady, Refuel, Between, Tyre, TyreAlarm, Operator, Tail, Done }

        readonly VideoRunWorld world;
        readonly List<VideoStep> steps = new List<VideoStep>();
        Stage stage = Stage.Waiting;
        DateTimeOffset begun, stageAt;
        DateTimeOffset? waitedSince, lastWaitingLog;
        IReadOnlyCollection<string> knownRefuels, knownAreaCommands;

        public VideoRun(VideoRunWorld world)
        {
            this.world = world ?? throw new ArgumentNullException(nameof(world));
        }

        public bool Finished => stage == Stage.Done;
        public bool Complete { get; private set; }
        public IReadOnlyList<VideoStep> Steps => steps;

        /// <summary>Seconds since the fleet was observed; 0 before.</summary>
        public double Elapsed(DateTimeOffset now) => stage == Stage.Waiting ? 0.0 : (now - begun).TotalSeconds;

        void Step(string name, DateTimeOffset now, string note)
        {
            steps.Add(new VideoStep(name, Elapsed(now), note));
            world.Log($"video-run: {Elapsed(now):0} s · {name}" + (string.IsNullOrEmpty(note) ? "" : " · " + note));
        }

        void Enter(Stage next, DateTimeOffset now)
        {
            stage = next;
            stageAt = now;
        }

        double Since(DateTimeOffset now) => (now - stageAt).TotalSeconds;

        /// <summary>Call every frame.</summary>
        public void Tick()
        {
            if (stage == Stage.Done) return;
            var now = world.Clock();
            switch (stage)
            {
                case Stage.Waiting:
                    if (!world.FleetObserved())
                    {
                        WaitForFleet(now);
                        return;
                    }

                    begun = now;
                    Enter(Stage.Steady, now);
                    Step("fleet observed", now, $"the fleet works undisturbed for {SteadyFirstSeconds:0} s (the establishing shots)");
                    break;
                case Stage.Steady:
                    if (Since(now) < SteadyFirstSeconds) return;
                    knownRefuels = world.CommandTokens(Protagonist, "goto-refuel");
                    Step("low fuel", now, $"{Protagonist}: " + (world.PrepareLowFuel(Protagonist) ?? "the presenter could not prepare the tank"));
                    Enter(Stage.Refuel, now);
                    break;
                case Stage.Refuel:
                    // the platform's rule has sent the refuel command and the truck has done it: its success is the platform's, and the
                    // truck being on its track again is the device's
                    if (world.CommandSuccessful(Protagonist, "goto-refuel", knownRefuels) && world.OnTrack(Protagonist))
                    {
                        Step("refuel cycle complete", now, "the platform says goto-refuel SUCCESSFUL and the truck is back at work");
                        Enter(Stage.Between, now);
                    }
                    else if (Since(now) >= RefuelCycleWaitSeconds)
                    {
                        Step("refuel cycle NOT seen", now, $"no SUCCESSFUL goto-refuel with the truck back at work within {RefuelCycleWaitSeconds:0} s: the recording may lack it");
                        Enter(Stage.Between, now);
                    }

                    break;
                case Stage.Between:
                    if (Since(now) < SteadyBetweenSeconds) return;
                    Step("puncture", now, $"{Puncture}: " + (world.PrepareTyreLeak(Puncture) ?? "the presenter could not start the leak"));
                    Enter(Stage.Tyre, now);
                    break;
                case Stage.Tyre:
                    if (world.AlarmActive(Puncture, "tyre-pressure-low"))
                    {
                        Step("tyre alarm", now, "the platform says tyre-pressure-low is ACTIVE; the truck keeps driving");
                        Enter(Stage.TyreAlarm, now);
                    }
                    else if (Since(now) >= TyreAlarmWaitSeconds)
                    {
                        Step("tyre alarm NOT seen", now, $"no tyre-pressure-low ACTIVE within {TyreAlarmWaitSeconds:0} s: the recording may lack it");
                        Enter(Stage.TyreAlarm, now);
                    }

                    break;
                case Stage.TyreAlarm:
                    if (Since(now) < AfterTyreAlarmSeconds) return;
                    knownAreaCommands = world.CommandTokens(Puncture, "goto-area");
                    Step("operator", now, $"send goto-area {OperatorArea} to {Puncture} (the console's device page, Commands panel); the run waits for it to succeed");
                    Enter(Stage.Operator, now);
                    break;
                case Stage.Operator:
                    if (world.CommandSuccessful(Puncture, "goto-area", knownAreaCommands))
                    {
                        Step("operator command complete", now, "the platform says goto-area SUCCESSFUL");
                        Enter(Stage.Tail, now);
                    }
                    else if (Since(now) >= OperatorWaitSeconds)
                    {
                        Step("operator command NOT seen", now, $"no SUCCESSFUL goto-area within {OperatorWaitSeconds:0} s: the recording lacks the console's part");
                        Enter(Stage.Tail, now);
                    }

                    break;
                case Stage.Tail:
                    if (Since(now) < TailSeconds) return;
                    Complete = true;
                    foreach (var s in steps)
                        if (s.Name.EndsWith("NOT seen", StringComparison.Ordinal)) Complete = false;
                    Step("done", now, Complete ? "every event of the take was seen" : "some events were NOT seen (above)");
                    Enter(Stage.Done, now);
                    world.Quit(Complete ? 0 : 1);
                    break;
            }
        }

        // nothing is happening yet: say why every few seconds, and after FleetWaitSeconds stop and say that too (a take that never started is not a hang)
        void WaitForFleet(DateTimeOffset now)
        {
            if (!waitedSince.HasValue) waitedSince = now;
            var waited = (now - waitedSince.Value).TotalSeconds;
            var why = world.WhyNotObserved?.Invoke() ?? "the fleet is not all observed";
            if (waited >= FleetWaitSeconds)
            {
                var note = $"not every device was observed within {FleetWaitSeconds:0} s: {why}";
                steps.Add(new VideoStep("fleet NOT observed", waited, note));
                world.Log("video-run: fleet NOT observed · " + note);
                Complete = false;
                Enter(Stage.Done, now);
                world.Quit(1);
                return;
            }

            if (lastWaitingLog.HasValue && (now - lastWaitingLog.Value).TotalSeconds < WaitingLogSeconds) return;
            lastWaitingLog = now;
            world.Log($"video-run: waiting for the fleet ({waited:0} s) · {why}");
        }

        /// <summary>The steps as JSON, for the file the run leaves beside its recording.</summary>
        public string ToJson()
        {
            var sb = new StringBuilder("{\n  \"complete\": ").Append(Complete ? "true" : "false").Append(",\n  \"steps\": [");
            for (var i = 0; i < steps.Count; i++)
            {
                if (i > 0) sb.Append(',');
                sb.Append("\n    { \"name\": ").Append(Quote(steps[i].Name))
                    .Append(", \"atSeconds\": ").Append(steps[i].AtSeconds.ToString("0.0", CultureInfo.InvariantCulture))
                    .Append(", \"note\": ").Append(Quote(steps[i].Note ?? "")).Append(" }");
            }

            return sb.Append("\n  ]\n}\n").ToString();
        }

        static string Quote(string s)
        {
            var sb = new StringBuilder("\"");
            foreach (var c in s)
            {
                if (c == '"' || c == '\\') sb.Append('\\').Append(c);
                else if (c < 0x20) sb.Append(' ');
                else sb.Append(c);
            }

            return sb.Append('"').ToString();
        }
    }
}
