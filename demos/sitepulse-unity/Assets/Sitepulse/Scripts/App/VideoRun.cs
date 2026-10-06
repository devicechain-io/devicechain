// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;

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

        /// <summary>The presenter's prepare-low-fuel on a machine (inputs only); returns what it said, or null if there is no such machine.</summary>
        public Func<string, string> PrepareLowFuel { get; set; }

        /// <summary>The presenter's puncture on a machine (inputs only); returns what it said, or null.</summary>
        public Func<string, string> PrepareTyreLeak { get; set; }

        /// <summary>The platform says this machine has this alarm active.</summary>
        public Func<string, string, bool> AlarmActive { get; set; }

        /// <summary>The platform says this machine's newest command of this name, queued at or after the given time, is SUCCESSFUL.</summary>
        public Func<string, string, DateTimeOffset, bool> CommandSuccessful { get; set; }

        /// <summary>The machine is on its routine track again (the device's own account).</summary>
        public Func<string, bool> OnTrack { get; set; }

        public Action<string> Log { get; set; }
        public Action<int> Quit { get; set; }
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

        /// <summary>After the tyre alarm the truck is left driving for this long, so its card, drawer and chase shots are all in the recording.</summary>
        public const double AfterTyreAlarmSeconds = 90.0;

        public const double TyreAlarmWaitSeconds = 150.0;
        public const double OperatorWaitSeconds = 480.0;
        public const double TailSeconds = 45.0;

        enum Stage { Waiting, Steady, Refuel, Between, Tyre, TyreAlarm, Operator, Tail, Done }

        readonly VideoRunWorld world;
        readonly List<VideoStep> steps = new List<VideoStep>();
        Stage stage = Stage.Waiting;
        DateTimeOffset begun, stageAt, preparedAt, operatorAt;

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
                    if (!world.FleetObserved()) return;
                    begun = now;
                    Enter(Stage.Steady, now);
                    Step("fleet observed", now, $"the fleet works undisturbed for {SteadyFirstSeconds:0} s (the establishing shots)");
                    break;
                case Stage.Steady:
                    if (Since(now) < SteadyFirstSeconds) return;
                    preparedAt = now;
                    Step("low fuel", now, $"{Protagonist}: " + (world.PrepareLowFuel(Protagonist) ?? "the presenter could not prepare the tank"));
                    Enter(Stage.Refuel, now);
                    break;
                case Stage.Refuel:
                    // the platform's rule has sent the refuel command and the truck has done it: its success is the platform's, and the
                    // truck being on its track again is the device's
                    if (world.CommandSuccessful(Protagonist, "goto-refuel", preparedAt) && world.OnTrack(Protagonist))
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
                    operatorAt = now;
                    Step("operator", now, $"send goto-area {OperatorArea} to {Puncture} (the console's device page, Commands panel); the run waits for it to succeed");
                    Enter(Stage.Operator, now);
                    break;
                case Stage.Operator:
                    if (world.CommandSuccessful(Puncture, "goto-area", operatorAt))
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
