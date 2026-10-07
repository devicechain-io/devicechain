// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.Reflection;
using System.Text.Json;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.Platform;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>The feature video's one live take, driven over a fake world and a clock the test moves.</summary>
    public sealed class VideoRunTests
    {
        sealed class Fake
        {
            public DateTimeOffset Now = new DateTimeOffset(2026, 10, 6, 14, 0, 0, TimeSpan.Zero);
            public bool Observed, TruckOnTrack, TyreAlarm;
            public string Why = "SP-HL-0003 not observed yet (at Bound)";
            public readonly List<string> Calls = new List<string>();
            public readonly List<string> Log = new List<string>();
            public readonly List<(string id, string name, string token, bool ok)> Cmds = new List<(string, string, string, bool)>();
            public int? Quit;

            /// <summary>The platform reports a command (by its token) for a machine.</summary>
            public void Report(string id, string name, string token, bool successful) => Cmds.Add((id, name, token, successful));

            public VideoRunWorld World() => new VideoRunWorld
            {
                Clock = () => Now,
                FleetObserved = () => Observed,
                WhyNotObserved = () => Why,
                PrepareLowFuel = id => { Calls.Add("low-fuel " + id + " @" + Now.ToString("HH:mm:ss")); return "prepared"; },
                PrepareTyreLeak = id => { Calls.Add("puncture " + id + " @" + Now.ToString("HH:mm:ss")); return "punctured"; },
                AlarmActive = (id, key) => id == VideoRun.Puncture && key == "tyre-pressure-low" && TyreAlarm,
                CommandTokens = (id, name) => Cmds.Where(c => c.id == id && c.name == name).Select(c => c.token).ToList(),
                CommandSuccessful = (id, name, known) => Cmds.Any(c => c.id == id && c.name == name && c.ok && !known.Contains(c.token)),
                OnTrack = id => id == VideoRun.Protagonist && TruckOnTrack,
                Log = Log.Add,
                Quit = code => Quit = code,
            };

            public void Advance(VideoRun run, double seconds)
            {
                for (var i = 0; i < (int)seconds; i++)
                {
                    Now = Now.AddSeconds(1);
                    run.Tick();
                }
            }
        }

        static string Story(VideoRun run) => string.Join(" > ", run.Steps.Select(s => s.Name));

        [Test]
        public void NothingHappensUntilEveryDeviceIsObserved()
        {
            var f = new Fake();
            var run = new VideoRun(f.World());
            f.Advance(run, (int)VideoRun.FleetWaitSeconds - 50);
            Assert.IsEmpty(run.Steps);
            Assert.IsEmpty(f.Calls, "no presenter action before the fleet is up");
            Assert.AreEqual(0.0, run.Elapsed(f.Now));
        }

        [Test]
        public void TheTakeRunsTheLowFuelCycleThenThePunctureThenWaitsForTheOperatorAndCloses()
        {
            var f = new Fake { Observed = true };
            var run = new VideoRun(f.World());
            f.Advance(run, 1);
            Assert.AreEqual("fleet observed", Story(run));

            f.Advance(run, VideoRun.SteadyFirstSeconds - 2);
            Assert.IsEmpty(f.Calls, "the fleet works undisturbed first: the establishing shots");
            f.Advance(run, 3);
            Assert.AreEqual(1, f.Calls.Count);
            StringAssert.StartsWith("low-fuel SP-HL-0006", f.Calls[0]);

            f.Advance(run, 200);
            Assert.AreEqual(1, f.Calls.Count, "it waits for the platform's rule to do the rest and sends nothing itself");
            f.Report(VideoRun.Protagonist, "goto-refuel", "r1", true);
            f.Advance(run, 5);
            Assert.AreEqual(1, run.Steps.Count(s => s.Name == "low fuel"));
            Assert.AreEqual(0, run.Steps.Count(s => s.Name == "refuel cycle complete"), "SUCCESSFUL alone is not the end of it: the truck must be back at work");
            f.TruckOnTrack = true;
            f.Advance(run, 2);
            Assert.AreEqual(1, run.Steps.Count(s => s.Name == "refuel cycle complete"));

            f.Advance(run, VideoRun.SteadyBetweenSeconds - 2);
            Assert.AreEqual(1, f.Calls.Count);
            f.Advance(run, 4);
            Assert.AreEqual(2, f.Calls.Count);
            StringAssert.StartsWith("puncture SP-HL-0003", f.Calls[1]);

            f.Advance(run, 20);
            f.TyreAlarm = true;
            f.Advance(run, 2);
            Assert.AreEqual(1, run.Steps.Count(s => s.Name == "tyre alarm"));
            f.Advance(run, VideoRun.AfterTyreAlarmSeconds - 3);
            Assert.AreEqual(0, run.Steps.Count(s => s.Name == "operator"));
            f.Advance(run, 3);
            Assert.AreEqual(1, run.Steps.Count(s => s.Name == "operator"));
            StringAssert.Contains("goto-area sp-zone-yard to SP-HL-0003", run.Steps.Single(s => s.Name == "operator").Note);
            Assert.IsNull(f.Quit, "it waits for the operator");

            f.Advance(run, 100);
            f.Report(VideoRun.Puncture, "goto-area", "a1", true);
            f.Advance(run, 3);
            Assert.AreEqual(1, run.Steps.Count(s => s.Name == "operator command complete"));
            Assert.IsNull(f.Quit, "and a short tail after it");
            f.Advance(run, VideoRun.TailSeconds + 2);
            Assert.AreEqual(0, f.Quit);
            Assert.IsTrue(run.Finished);
            Assert.IsTrue(run.Complete);
            Assert.AreEqual("fleet observed > low fuel > refuel cycle complete > puncture > tyre alarm > operator > operator command complete > done", Story(run));
            f.Advance(run, 100);
            Assert.AreEqual(2, f.Calls.Count, "a finished run does nothing more");
        }

        [Test]
        public void ACommandTheObserverAlreadyHeldWhenTheStepBeganDoesNotCountAsThisSteps()
        {
            var f = new Fake { Observed = true };
            // the platform reported a SUCCESSFUL goto-refuel from an earlier take before the run prepared the tank
            f.Report(VideoRun.Protagonist, "goto-refuel", "earlier-take", true);
            f.TruckOnTrack = true;
            var run = new VideoRun(f.World());
            f.Advance(run, (int)VideoRun.SteadyFirstSeconds + 30);
            Assert.AreEqual(1, run.Steps.Count(s => s.Name == "low fuel"));
            Assert.AreEqual(0, run.Steps.Count(s => s.Name == "refuel cycle complete"), "an earlier take's success is not this take's");
            f.Report(VideoRun.Protagonist, "goto-refuel", "this-take", false);
            f.Advance(run, 5);
            Assert.AreEqual(0, run.Steps.Count(s => s.Name == "refuel cycle complete"), "queued is not successful");
            f.Cmds[f.Cmds.Count - 1] = (VideoRun.Protagonist, "goto-refuel", "this-take", true);
            f.Advance(run, 3);
            Assert.AreEqual(1, run.Steps.Count(s => s.Name == "refuel cycle complete"));
        }

        [Test]
        public void AGotoAreaThatSucceededBeforeTheOperatorStepDoesNotEndTheWaitButANewOneDoes()
        {
            var f = new Fake { Observed = true, TruckOnTrack = true, TyreAlarm = true };
            var run = new VideoRun(f.World());
            f.Advance(run, (int)VideoRun.SteadyFirstSeconds + 3);
            f.Report(VideoRun.Protagonist, "goto-refuel", "r1", true);
            f.Advance(run, 100);
            // an earlier goto-area for the same machine succeeded after the preparation and before the operator step
            f.Report(VideoRun.Puncture, "goto-area", "SP-HL-0003-earlier", true);
            Assert.AreEqual(0, run.Steps.Count(s => s.Name == "operator"), "the operator step has not begun yet");
            f.Advance(run, 60);
            Assert.AreEqual(1, run.Steps.Count(s => s.Name == "operator"));
            f.Advance(run, 30);
            Assert.AreEqual(0, run.Steps.Count(s => s.Name == "operator command complete"), "the earlier command is not the operator's");
            f.Report(VideoRun.Puncture, "goto-area", "operators", false);
            f.Advance(run, 5);
            Assert.AreEqual(0, run.Steps.Count(s => s.Name == "operator command complete"));
            f.Cmds[f.Cmds.Count - 1] = (VideoRun.Puncture, "goto-area", "operators", true);
            f.Advance(run, 3);
            Assert.AreEqual(1, run.Steps.Count(s => s.Name == "operator command complete"));
        }

        static readonly DateTimeOffset T0 = new DateTimeOffset(2026, 10, 6, 14, 0, 0, TimeSpan.Zero);

        static ObservedState Observed(params (string token, string status, double queuedSeconds)[] cmds)
        {
            var st = new ObservedState(T0);
            foreach (var (token, status, queued) in cmds)
                st.ApplyCommand("dev", new ObservedCommand { Token = token, Name = "goto-area", Status = status, QueuedAt = T0.AddSeconds(queued), ObservedAt = T0 });
            return st;
        }

        [Test]
        public void ANewSuccessfulCommandCountsWhateverTheClusterClockSaysItWasQueued()
        {
            // the cluster's clock is ten seconds behind this app's: the command was "queued" before the app asked for it
            var st = Observed(("old", "SUCCESSFUL", -600));
            st.TryGet("dev", out var before);
            var known = VideoCommands.Tokens(before, "goto-area");
            CollectionAssert.AreEquivalent(new[] { "old" }, known);
            Assert.IsFalse(VideoCommands.NewSuccessful(before, "goto-area", known));
            st.ApplyCommand("dev", new ObservedCommand { Token = "new", Name = "goto-area", Status = "SUCCESSFUL", QueuedAt = T0.AddSeconds(-10), ObservedAt = T0 });
            Assert.IsTrue(VideoCommands.NewSuccessful(before, "goto-area", known));
            Assert.IsFalse(VideoCommands.NewSuccessful(before, "goto-refuel", known), "another command's name does not count");
            Assert.IsFalse(VideoCommands.NewSuccessful(null, "goto-area", known));
        }

        [Test]
        public void WhileWaitingForTheFleetItSaysWhyEveryTenSecondsAndAfterFiveMinutesGivesUpWithTheReasonAndExitCodeOne()
        {
            var f = new Fake();
            var run = new VideoRun(f.World());
            f.Advance(run, 60);
            var why = f.Log.Where(l => l.Contains("waiting for the fleet")).ToList();
            Assert.That(why.Count, Is.InRange(5, 7), "about every ten seconds, not every frame");
            StringAssert.Contains("SP-HL-0003 not observed yet", why[0]);
            Assert.IsNull(f.Quit);
            f.Advance(run, (int)VideoRun.FleetWaitSeconds);
            Assert.AreEqual(1, f.Quit);
            Assert.IsTrue(run.Finished);
            Assert.IsFalse(run.Complete);
            var step = run.Steps.Single();
            Assert.AreEqual("fleet NOT observed", step.Name);
            StringAssert.Contains("SP-HL-0003 not observed yet", step.Note);
            using var doc = JsonDocument.Parse(run.ToJson());
            Assert.IsFalse(doc.RootElement.GetProperty("complete").GetBoolean());
            StringAssert.Contains("SP-HL-0003", doc.RootElement.GetProperty("steps")[0].GetProperty("note").GetString());
            Assert.IsTrue(f.Log.Any(l => l.Contains("fleet NOT observed")));
        }

        [Test]
        public void AFleetThatIsObservedInTimeStopsTheWaitingAndNeverQuitsEarly()
        {
            var f = new Fake();
            var run = new VideoRun(f.World());
            f.Advance(run, 200);
            f.Observed = true;
            f.Advance(run, 5);
            Assert.AreEqual("fleet observed", Story(run));
            Assert.IsNull(f.Quit);
        }

        [Test]
        public void AWaitThatRunsOutIsSaidOutLoudTheRunGoesOnAndTheExitCodeSaysTheTakeIsIncomplete()
        {
            var f = new Fake { Observed = true };
            var run = new VideoRun(f.World());
            f.Advance(run, (int)(VideoRun.SteadyFirstSeconds + VideoRun.RefuelCycleWaitSeconds + 5));
            Assert.AreEqual(1, run.Steps.Count(s => s.Name == "refuel cycle NOT seen"));
            f.Advance(run, (int)(VideoRun.SteadyBetweenSeconds + VideoRun.TyreAlarmWaitSeconds + VideoRun.AfterTyreAlarmSeconds + 10));
            Assert.AreEqual(1, run.Steps.Count(s => s.Name == "tyre alarm NOT seen"));
            f.Advance(run, (int)(VideoRun.OperatorWaitSeconds + VideoRun.TailSeconds + 10));
            Assert.AreEqual(1, run.Steps.Count(s => s.Name == "operator command NOT seen"));
            Assert.AreEqual(1, f.Quit);
            Assert.IsTrue(run.Finished);
            Assert.IsFalse(run.Complete);
            Assert.IsTrue(f.Log.Any(l => l.Contains("NOT seen")), "and it said so in the log");
            StringAssert.Contains("the recording lacks the console's part", run.Steps.Single(s => s.Name == "operator command NOT seen").Note);
        }

        [Test]
        public void TheRunsHandsCanOnlyMoveThePresentersInputsAndNeverWriteToThePlatform()
        {
            var names = typeof(VideoRunWorld).GetProperties(BindingFlags.Public | BindingFlags.Instance).Select(p => p.Name).OrderBy(n => n).ToArray();
            CollectionAssert.AreEqual(new[] { "AlarmActive", "Clock", "CommandSuccessful", "CommandTokens", "FleetObserved", "Log", "OnTrack", "PrepareLowFuel", "PrepareTyreLeak", "Quit", "WhyNotObserved" }, names,
                "the presenter's two preparations and what it reads: nothing that creates a command, an alarm or a value");
        }

        [Test]
        public void TheStepsAreWrittenAsJsonBesideTheRecording()
        {
            var f = new Fake { Observed = true };
            var run = new VideoRun(f.World());
            f.Advance(run, (int)VideoRun.SteadyFirstSeconds + 5);
            using var doc = JsonDocument.Parse(run.ToJson());
            Assert.IsFalse(doc.RootElement.GetProperty("complete").GetBoolean());
            var steps = doc.RootElement.GetProperty("steps");
            Assert.AreEqual(run.Steps.Count, steps.GetArrayLength());
            Assert.AreEqual("fleet observed", steps[0].GetProperty("name").GetString());
            Assert.AreEqual("low fuel", steps[1].GetProperty("name").GetString());
            Assert.AreEqual(VideoRun.SteadyFirstSeconds, steps[1].GetProperty("atSeconds").GetDouble(), 1.1);
        }

        [Test]
        public void TheFlagIsAPlainSwitch()
        {
            Assert.IsTrue(VideoRunFlags.Requested(new[] { "x", "-sitepulse-mode", "live", "-sitepulse-video-run" }));
            Assert.IsFalse(VideoRunFlags.Requested(new[] { "x", "-sitepulse-mode", "live" }));
            Assert.IsFalse(VideoRunFlags.Requested(null));
        }

        [Test]
        public void TheTimingsLeaveRoomForTheScriptsShots()
        {
            Assert.GreaterOrEqual(VideoRun.SteadyFirstSeconds, 120, "S02-S07 and S22 are cut from the first steady stretch");
            Assert.GreaterOrEqual(VideoRun.AfterTyreAlarmSeconds, 60, "S16's seven seconds, the drawer and the chase");
            Assert.AreEqual("SP-HL-0006", VideoRun.Protagonist);
            Assert.AreEqual("SP-HL-0003", VideoRun.Puncture);
            Assert.AreEqual("sp-zone-yard", VideoRun.OperatorArea);
        }
    }
}
