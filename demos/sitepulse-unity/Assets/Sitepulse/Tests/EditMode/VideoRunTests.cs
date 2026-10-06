// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.Reflection;
using System.Text.Json;
using DeviceChain.Sitepulse.App;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>The feature video's one live take, driven over a fake world and a clock the test moves.</summary>
    public sealed class VideoRunTests
    {
        sealed class Fake
        {
            public DateTimeOffset Now = new DateTimeOffset(2026, 10, 6, 14, 0, 0, TimeSpan.Zero);
            public bool Observed, TruckDone, TruckOnTrack, TyreAlarm, OperatorDone;
            public readonly List<string> Calls = new List<string>();
            public readonly List<string> Log = new List<string>();
            public readonly List<DateTimeOffset> Since = new List<DateTimeOffset>();
            public int? Quit;

            public VideoRunWorld World() => new VideoRunWorld
            {
                Clock = () => Now,
                FleetObserved = () => Observed,
                PrepareLowFuel = id => { Calls.Add("low-fuel " + id + " @" + Now.ToString("HH:mm:ss")); return "prepared"; },
                PrepareTyreLeak = id => { Calls.Add("puncture " + id + " @" + Now.ToString("HH:mm:ss")); return "punctured"; },
                AlarmActive = (id, key) => id == VideoRun.Puncture && key == "tyre-pressure-low" && TyreAlarm,
                CommandSuccessful = (id, name, since) =>
                {
                    Since.Add(since);
                    if (id == VideoRun.Protagonist && name == "goto-refuel") return TruckDone;
                    return id == VideoRun.Puncture && name == "goto-area" && OperatorDone;
                },
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
            f.Advance(run, 600);
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
            f.TruckDone = true;
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
            f.OperatorDone = true;
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
        public void ACommandThatSucceededBeforeThisStepDoesNotCountAsThisSteps()
        {
            var f = new Fake { Observed = true, TruckDone = false };
            var run = new VideoRun(f.World());
            // the first tick sees the fleet observed; the preparation is made one steady stretch later
            f.Advance(run, (int)VideoRun.SteadyFirstSeconds + 1);
            var preparedAt = f.Now;
            Assert.AreEqual(1, f.Calls.Count);
            f.Advance(run, 5);
            Assert.IsTrue(f.Since.Count > 0);
            foreach (var since in f.Since)
                Assert.AreEqual(preparedAt, since, "it asks about a command queued after its own preparation, not one the platform had before");
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
            CollectionAssert.AreEqual(new[] { "AlarmActive", "Clock", "CommandSuccessful", "FleetObserved", "Log", "OnTrack", "PrepareLowFuel", "PrepareTyreLeak", "Quit" }, names,
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
