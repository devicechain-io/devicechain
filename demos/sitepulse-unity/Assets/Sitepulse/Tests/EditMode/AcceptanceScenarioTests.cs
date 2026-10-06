// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Tasks;
using DeviceChain.Sitepulse.Visuals;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>
    /// The Phase B controls (<c>rule-disabled</c>, <c>observer-outage</c>) and the Phase C soak, as the in-player probe judges them.
    /// Each scenario has a run that passes and the runs that must not: a probe that cannot fail proves nothing.
    /// </summary>
    public sealed class AcceptanceScenarioTests
    {
        static readonly DateTimeOffset T0 = new DateTimeOffset(2026, 10, 6, 12, 0, 0, TimeSpan.Zero);

        // ------------------------------------------------------------------ flags

        static AcceptanceOptions Parse(params string[] args)
        {
            var r = AcceptanceFlags.FromCommandLine(new[] { "x" }.Concat(args).ToArray());
            Assert.IsTrue(r.Ok, r.Error);
            return r.Value;
        }

        [Test]
        public void ThePhaseBControlsAreControlsWithNoDevice()
        {
            var rule = Parse("-sitepulse-acceptance", "phaseA", "-sitepulse-control", "rule-disabled");
            Assert.IsTrue(rule.IsControl);
            Assert.AreEqual("ACCEPTANCE CONTROL rule-disabled", rule.RunLabel);
            var outage = Parse("-sitepulse-acceptance", "phaseA", "-sitepulse-control", "observer-outage");
            Assert.AreEqual(ControlSpec.ObserverOutage, outage.Control.Kind);
            foreach (var bad in new[] { "rule-disabled:SP-HL-0006", "observer-outage:x" })
                Assert.IsFalse(AcceptanceFlags.FromCommandLine(new[] { "x", "-sitepulse-acceptance", "phaseA", "-sitepulse-control", bad }).Ok, bad);
        }

        [Test]
        public void ASoakDefaultsToThirtyMinutesAndTakesItsOwn()
        {
            var plain = Parse("-sitepulse-acceptance", "soak");
            Assert.IsTrue(plain.IsSoak);
            Assert.IsFalse(plain.IsControl);
            Assert.AreEqual(30, plain.SoakMinutes);
            Assert.AreEqual("ACCEPTANCE soak", plain.RunLabel);
            Assert.AreEqual(45, Parse("-sitepulse-acceptance", "soak", "-sitepulse-soak-minutes", "45").SoakMinutes);
        }

        [TestCase("0")]
        [TestCase("241")]
        [TestCase("ten")]
        [TestCase("1.5")]
        public void ASoakLengthThatIsNotAWholeNumberOfMinutesIsRefused(string minutes)
        {
            Assert.IsFalse(AcceptanceFlags.FromCommandLine(new[] { "x", "-sitepulse-acceptance", "soak", "-sitepulse-soak-minutes", minutes }).Ok, minutes);
        }

        [Test]
        public void TheSoakFlagsStandAloneOnlyAsASoak()
        {
            Assert.IsFalse(AcceptanceFlags.FromCommandLine(new[] { "x", "-sitepulse-soak-minutes", "5" }).Ok, "without the acceptance flag");
            Assert.IsFalse(AcceptanceFlags.FromCommandLine(new[] { "x", "-sitepulse-acceptance", "phaseA", "-sitepulse-soak-minutes", "5" }).Ok, "on another acceptance");
            Assert.IsFalse(AcceptanceFlags.FromCommandLine(new[] { "x", "-sitepulse-acceptance", "soak", "-sitepulse-control", "wrong-ca" }).Ok, "a soak runs unfaulted");
        }

        [Test]
        public void TheSoakScheduleGivesEveryCycleTimeToFinish()
        {
            Assert.AreEqual(5, PhaseAProbe.ScheduledCycles(30));
            Assert.AreEqual(2, PhaseAProbe.ScheduledCycles(12));
            Assert.AreEqual(1, PhaseAProbe.ScheduledCycles(1), "even a smoke soak runs one cycle");
            Assert.AreEqual(1800.0, PhaseAProbe.SoakSeconds(30));
            Assert.AreEqual(PhaseAProbe.SoakFirstCycleSeconds + PhaseAProbe.SoakCycleEvalSeconds, PhaseAProbe.SoakSeconds(1), "a short soak is stretched to hold its one cycle");
            for (var m = 1; m <= 60; m++)
            {
                var last = PhaseAProbe.SoakFirstCycleSeconds + (PhaseAProbe.ScheduledCycles(m) - 1) * PhaseAProbe.SoakCycleSeconds + PhaseAProbe.SoakCycleEvalSeconds;
                Assert.LessOrEqual(last, PhaseAProbe.SoakSeconds(m), m + " minutes");
            }
        }

        [Test]
        public void TheSoakRotatesThroughSixDifferentTrucks()
        {
            CollectionAssert.AllItemsAreUnique(PhaseAProbe.SoakRotation);
            Assert.AreEqual(6, PhaseAProbe.SoakRotation.Length);
            Assert.IsTrue(PhaseAProbe.SoakRotation.All(id => id.StartsWith("SP-HL-")));
        }

        // ------------------------------------------------------------------ the rig

        sealed class Rig
        {
            public DateTimeOffset Now = T0;
            public readonly ReadinessBoard Board;
            public readonly ObservedState Observed = new ObservedState(T0);
            public readonly Timeline Timeline;
            public readonly ObserverStatus Observer = new ObserverStatus();
            public readonly Dictionary<string, string> Files = new Dictionary<string, string>();
            public readonly HashSet<string> Flags = new HashSet<string>();
            public readonly Dictionary<string, MachineView> Machines = new Dictionary<string, MachineView>();
            public readonly List<string> Prepared = new List<string>();
            public readonly HashSet<string> Silent = new HashSet<string>();
            public readonly string[] Tokens = { "tok-hl6", "tok-ld1", "tok-pl1" };
            public readonly string[] Ids = { "SP-HL-0006", "SP-LD-0001", "SP-PL-0001" };
            public PhaseAProbe Probe;
            public double Fuel = 60.0;
            public MachineMode Mode = MachineMode.Working;
            public BayView? Bay = new BayView(null, 0);
            public int Sessions = 3;
            public long Dropped, SendErrors;
            public bool? CardsLive = true;
            public double FrameSeconds = 0.016;

            public Rig(AcceptanceOptions options)
            {
                var devices = new[]
                {
                    new SceneDevice(Ids[0], SceneKind.Hauler),
                    new SceneDevice(Ids[1], SceneKind.Loader),
                    new SceneDevice(Ids[2], SceneKind.Plant),
                };
                Board = new ReadinessBoard(devices, "t");
                Timeline = new Timeline(() => Now);
                for (var i = 0; i < devices.Length; i++)
                {
                    Board.SetBind(new BindResult { ExternalId = Ids[i], Outcome = BindOutcome.Bound, DeviceToken = Tokens[i] });
                    Board.SetCredential(new CredentialOutcome { ExternalId = Ids[i], Ok = true, Path = CredentialPath.Batched });
                    var keys = i == 2 ? new[] { "throughput_tph", "plant_running" } : MeasurementKeys.Equipment.ToArray();
                    foreach (var key in keys) Observed.ApplyMeasurement(Tokens[i], key, 30.0, T0.AddSeconds(1), T0.AddSeconds(1), false);
                }

                var world = new ProbeWorld
                {
                    Board = Board,
                    Observed = Observed,
                    Cards = () => Cards(),
                    CardSource = () => Provenance.Observed,
                    CardStreamLive = () => CardsLive,
                    Timeline = () => Timeline,
                    PrepareLowFuel = id => { Prepared.Add(id); Timeline.Add(id, TimelineKinds.Presenter, "prepare low-fuel cycle"); return "prepare low-fuel cycle: fuel 40.0% -> 15.5%, crosses 15% in about 60 s"; },
                    FileExists = name => Flags.Contains(name),
                    WriteFile = (name, text) => Files[name] = text,
                    SamplePath = "C:\\out\\samples.jsonl",
                    SampleLines = () => 12,
                    UnityVersion = "test",
                    Observer = Observer,
                    Machine = id => id == "SP-HL-0006" ? new MachineView(Mode, TaskPhase.None, Fuel, Mode != MachineMode.Working) : Machines.TryGetValue(id, out var v) ? v : new MachineView(MachineMode.Working, TaskPhase.None, 80.0, false),
                    Bay = () => Bay,
                    SessionCount = () => Sessions,
                    DroppedSamples = () => Dropped,
                    SendErrors = () => SendErrors,
                    FrameSeconds = () => FrameSeconds,
                };
                Probe = new PhaseAProbe(options, world, T0, () => Now);
            }

            IEnumerable<DeviceReading> Cards()
            {
                var r = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment, Provenance.Observed);
                r.Set(MeasurementKeys.FuelPct, 50, Provenance.Observed, new Observation(T0, T0));
                yield return r;
            }

            public void Advance(double seconds)
            {
                Now = Now.AddSeconds(seconds);
                Probe.Tick();
            }

            /// <summary>The fleet comes up and is observed at <paramref name="observedAt"/> seconds; the stream is Live.</summary>
            public void BringUp(double observedAt = 12)
            {
                Observer.Measurements.State = StreamState.Live;
                Advance(1);
                Board.BeginSessions();
                foreach (var d in Board.Devices) Board.SetStage(d.Device.ExternalId, DeviceStage.Publishing);
                Advance(observedAt - 1);
                foreach (var t in Tokens) Board.MarkObserved(t);
                Advance(1);
            }

            /// <summary>The app's own once-a-frame judgement of which observed devices have gone quiet.</summary>
            public void Evaluate() => Board.EvaluateObserved(Now, tok => Observed.NewestOwnRunAt(tok));

            public void PublishOne()
            {
                foreach (var d in Board.Devices) d.Published++;
            }

            public void Refuel(string machine)
            {
                Timeline.Add(machine, TimelineKinds.Received, "goto-refuel (\u2026abcdef1234)");
                Timeline.Add(machine, TimelineKinds.Accepted, "goto-refuel: route 212 m, ETA 48 s");
                Timeline.Add(machine, TimelineKinds.Refuelling, "service started at 14.9% fuel");
                Timeline.Add(machine, TimelineKinds.Refuelling, "service finished: 14.9% -> 100.0%");
                Timeline.Add(machine, TimelineKinds.Outcome, "SUCCESS");
            }

            public JsonDocument Result() => JsonDocument.Parse(Files[PhaseAProbe.ResultFile]);

            public string Failures() => string.Join("; ", Probe.Items.Where(i => i.Pass == false).Select(i => i.Id + ": " + i.Detail));

            public ProbeItem Item(string id) => Probe.Items.First(i => i.Id == id);
        }

        static Rig RuleRig() => new Rig(Parse("-sitepulse-acceptance", "phaseA", "-sitepulse-control", "rule-disabled"));

        // ------------------------------------------------------------------ rule-disabled

        // The tank falls from 15.5 across the line and the platform's side stays quiet; the player judges all of it from its own objects
        static void RunRuleWindow(Rig h, double fuelStep = 0.1, Action<int> each = null)
        {
            h.Flags.Add(PhaseAProbe.GoFile);
            h.Advance(1);
            h.Fuel = 15.5;
            for (var i = 0; i < 400 && !h.Probe.Finished; i++)
            {
                h.Fuel = Math.Max(0, h.Fuel - fuelStep);
                each?.Invoke(i);
                h.Advance(1);
            }
        }

        static void ThePlatformSawTheLowTank(Rig h) => h.Observed.ApplyMeasurement(h.Tokens[0], MeasurementKeys.FuelPct, 12.0, h.Now, h.Now, false);

        [Test]
        public void TheRuleDisabledProbeWaitsForTheGoBeforeItPreparesTheTank()
        {
            var h = RuleRig();
            h.BringUp();
            h.Advance(6);
            h.Advance(2);
            Assert.AreEqual(0, h.Prepared.Count, "the script has not said the rule is off yet");
            using (var doc = h.Result()) Assert.AreEqual("awaiting-go", doc.RootElement.GetProperty("phase").GetString());
            h.Flags.Add(PhaseAProbe.GoFile);
            h.Advance(1);
            CollectionAssert.AreEqual(new[] { "SP-HL-0006" }, h.Prepared);
            h.Advance(1);
            Assert.AreEqual(1, h.Prepared.Count, "once");
            using (var doc = h.Result()) Assert.AreEqual("running", doc.RootElement.GetProperty("phase").GetString());
        }

        [Test]
        public void TheRuleDisabledProbePassesWhenNothingReactsToTheLowTank()
        {
            var h = RuleRig();
            h.BringUp();
            h.Advance(6);
            ThePlatformSawTheLowTank(h);
            RunRuleWindow(h);
            Assert.IsTrue(h.Probe.Finished);
            Assert.IsTrue(h.Probe.Passed, h.Failures());
            Assert.IsTrue(h.Item("rd-fuel-crossed").Pass == true);
            Assert.IsTrue(h.Item("rd-prepared").Pass == true);
            using var doc = h.Result();
            Assert.AreEqual("final", doc.RootElement.GetProperty("phase").GetString());
            Assert.IsTrue(doc.RootElement.GetProperty("ruleDisabled").TryGetProperty("crossedAt", out _));
        }

        [Test]
        public void TheRuleDisabledProbeFailsWhenTheTruckLeavesItsTrack()
        {
            var h = RuleRig();
            h.BringUp();
            h.Advance(6);
            ThePlatformSawTheLowTank(h);
            RunRuleWindow(h, each: i => { if (i == 90) h.Mode = MachineMode.Commanded; });
            Assert.IsFalse(h.Probe.Passed);
            Assert.IsFalse(h.Item("rd-stayed-on-track").Pass == true);
            StringAssert.Contains("Commanded", h.Item("rd-stayed-on-track").Detail);
        }

        [Test]
        public void TheRuleDisabledProbeFailsWhenTheMachineWasGivenACommand()
        {
            var h = RuleRig();
            h.BringUp();
            h.Advance(6);
            ThePlatformSawTheLowTank(h);
            RunRuleWindow(h, each: i => { if (i == 80) h.Timeline.Add("SP-HL-0006", TimelineKinds.Received, "goto-refuel (\u2026abcdef1234)"); });
            Assert.IsFalse(h.Probe.Passed);
            var item = h.Item("rd-no-command-received");
            Assert.IsFalse(item.Pass == true);
            StringAssert.Contains("goto-refuel", item.Detail);
        }

        [Test]
        public void TheRuleDisabledProbeFailsWhenTheTankRises()
        {
            var h = RuleRig();
            h.BringUp();
            h.Advance(6);
            ThePlatformSawTheLowTank(h);
            RunRuleWindow(h, each: i => { if (i == 100) h.Fuel = 90.0; });
            Assert.IsFalse(h.Item("rd-fuel-never-rose").Pass == true);
        }

        [Test]
        public void TheRuleDisabledProbeFailsWhenThePlatformRaisedTheAlarmOrQueuedACommand()
        {
            var alarm = RuleRig();
            alarm.BringUp();
            alarm.Advance(6);
            ThePlatformSawTheLowTank(alarm);
            alarm.Observed.ApplyAlarm(alarm.Tokens[0], new ObservedAlarm { Token = "a1", AlarmKey = AlarmKeys.LowFuel, State = ObservedAlarm.Active, OccurredAt = alarm.Now, ObservedAt = alarm.Now });
            RunRuleWindow(alarm);
            Assert.IsFalse(alarm.Item("rd-platform-silent").Pass == true);
            StringAssert.Contains(AlarmKeys.LowFuel, alarm.Item("rd-platform-silent").Detail);

            var command = RuleRig();
            command.BringUp();
            command.Advance(6);
            ThePlatformSawTheLowTank(command);
            command.Flags.Add(PhaseAProbe.GoFile);
            command.Advance(1);
            command.Observed.ApplyCommand(command.Tokens[0], new ObservedCommand { Token = "c1", Name = "goto-refuel", Status = "QUEUED", QueuedAt = command.Now, ObservedAt = command.Now });
            RunRuleWindow(command);
            Assert.IsFalse(command.Item("rd-platform-silent").Pass == true);
            StringAssert.Contains("goto-refuel", command.Item("rd-platform-silent").Detail);
        }

        [Test]
        public void TheRuleDisabledProbeFailsASilenceThatIsNotBecauseTheTankWasLow()
        {
            var h = RuleRig();
            h.BringUp();
            h.Advance(6);
            // the platform's own last fuel reading is still high: it never saw the low tank, so its silence says nothing
            RunRuleWindow(h);
            var item = h.Item("rd-platform-silent");
            Assert.IsFalse(item.Pass == true);
            StringAssert.Contains("never reached", item.Detail);
        }

        [Test]
        public void TheRuleDisabledProbeFailsATankThatNeverCrossedTheLine()
        {
            var h = RuleRig();
            h.BringUp();
            h.Advance(6);
            ThePlatformSawTheLowTank(h);
            RunRuleWindow(h, fuelStep: 0.0);
            Assert.IsFalse(h.Item("rd-fuel-crossed").Pass == true);
            Assert.IsFalse(h.Probe.Passed);
        }

        [Test]
        public void TheRuleDisabledProbeGivesUpWhenItIsNeverToldToGo()
        {
            var h = RuleRig();
            h.BringUp();
            for (var i = 0; i < 400 && !h.Probe.Finished; i++) h.Advance(1);
            Assert.IsTrue(h.Probe.Finished);
            Assert.IsFalse(h.Probe.Passed);
            Assert.AreEqual(0, h.Prepared.Count, "it never prepared a tank on a rule nobody said was off");
            StringAssert.Contains("never told", h.Item("rd-prepared").Detail);
        }

        [Test]
        public void TheRuleDisabledProbeFailsAFleetThatNeverCameUp()
        {
            var h = RuleRig();
            h.Advance(1);
            for (var i = 0; i < 400 && !h.Probe.Finished; i++) h.Advance(1);
            Assert.IsTrue(h.Probe.Finished);
            Assert.IsFalse(h.Probe.Passed);
            Assert.IsFalse(h.Item("observed-19").Pass == true, "for this control the fleet's coming up is a check, not a record");
        }

        [Test]
        public void ARuleDisabledRunNeverTakesAFaultControlsEvaluation()
        {
            // the probe's own item for this control is rd-*, not control-rule-disabled (which is for faults that expect devices to fail)
            var h = RuleRig();
            h.BringUp();
            h.Advance(6);
            ThePlatformSawTheLowTank(h);
            RunRuleWindow(h);
            Assert.IsFalse(h.Probe.Items.Any(i => i.Id.StartsWith("control-")));
        }

        // ------------------------------------------------------------------ observer-outage

        static Rig OutageRig() => new Rig(Parse("-sitepulse-acceptance", "phaseA", "-sitepulse-control", "observer-outage"));

        static void ToSteady(Rig h)
        {
            h.BringUp();
            h.PublishOne();
            for (var i = 0; i < 20; i++) h.Advance(1);
        }

        // event-management goes away: the stream drops and the cards read it; the devices go on publishing meanwhile
        static void RunOutage(Rig h, int seconds, bool devicesPublish = true)
        {
            h.Observer.Measurements.State = StreamState.Reconnecting;
            h.Observer.Measurements.LastLiveAt = h.Now;
            h.CardsLive = false;
            h.Observed.ApplyMeasurement(h.Tokens[0], MeasurementKeys.FuelPct, 40.0, h.Now, h.Now, false);
            for (var i = 0; i < seconds; i++)
            {
                if (devicesPublish) h.PublishOne();
                h.Advance(1);
            }
        }

        static void Recover(Rig h, bool snapshot = true, bool observedAgain = true)
        {
            h.Observer.Measurements.State = StreamState.Live;
            h.Observer.Measurements.Reconnects++;
            h.CardsLive = true;
            if (snapshot)
                foreach (var t in h.Tokens) h.Observed.ApplyMeasurement(t, MeasurementKeys.FuelPct, 41.0, h.Now, h.Now, true);
            h.Advance(1);
            if (!observedAgain) return;
            foreach (var t in h.Tokens) h.Observed.ApplyMeasurement(t, MeasurementKeys.FuelPct, 42.0, h.Now, h.Now, false);
            h.Evaluate();
            h.Advance(1);
        }

        [Test]
        public void TheOutageProbeWritesItsSteadyMarkBeforeTheScriptTakesAnythingDown()
        {
            var h = OutageRig();
            ToSteady(h);
            using var doc = h.Result();
            Assert.AreEqual("steady", doc.RootElement.GetProperty("phase").GetString());
            Assert.IsTrue(doc.RootElement.GetProperty("observerOutage").TryGetProperty("steadyAt", out _));
        }

        [Test]
        public void TheOutageProbePassesAStreamThatLeavesLiveShowsItAndComesBack()
        {
            var h = OutageRig();
            ToSteady(h);
            RunOutage(h, 40);
            Recover(h);
            Assert.IsTrue(h.Probe.Finished, "everything was observed again");
            Assert.IsTrue(h.Probe.Passed, h.Failures());
            foreach (var id in new[] { "oo-left-live", "oo-banner", "oo-never-fresh", "oo-values-grey", "oo-devices-publishing", "oo-returned-live", "oo-snapshot-refresh", "oo-banner-cleared", "oo-all-observed-again" })
                Assert.IsTrue(h.Item(id).Pass == true, id + ": " + h.Item(id).Detail);
            using var doc = h.Result();
            var o = doc.RootElement.GetProperty("observerOutage");
            Assert.IsTrue(o.GetProperty("bannerSeen").GetBoolean());
            Assert.IsTrue(o.TryGetProperty("backLiveAt", out _));
        }

        [Test]
        public void TheOutageProbeFailsAStreamThatNeverLeavesLive()
        {
            var h = OutageRig();
            ToSteady(h);
            for (var i = 0; i < 200 && !h.Probe.Finished; i++) h.Advance(1);
            Assert.IsTrue(h.Probe.Finished);
            Assert.IsFalse(h.Probe.Passed);
            Assert.IsFalse(h.Item("oo-left-live").Pass == true);
        }

        [Test]
        public void TheOutageProbeFailsDevicesThatStoppedPublishing()
        {
            var h = OutageRig();
            ToSteady(h);
            RunOutage(h, 40, devicesPublish: false);
            Recover(h);
            Assert.IsFalse(h.Item("oo-devices-publishing").Pass == true);
            Assert.IsFalse(h.Probe.Passed);
        }

        [Test]
        public void TheOutageProbeFailsASendErrorDuringTheOutage()
        {
            var h = OutageRig();
            ToSteady(h);
            h.Observer.Measurements.State = StreamState.Reconnecting;
            h.Observer.Measurements.LastLiveAt = h.Now;
            h.CardsLive = false;
            h.Advance(1);
            for (var i = 0; i < 30; i++) { h.PublishOne(); h.SendErrors++; h.Advance(1); }
            Recover(h);
            var item = h.Item("oo-devices-publishing");
            Assert.IsFalse(item.Pass == true);
            StringAssert.Contains("send error", item.Detail);
        }

        [Test]
        public void TheOutageProbeFailsAReconnectThatTookNoSnapshot()
        {
            var h = OutageRig();
            ToSteady(h);
            RunOutage(h, 40);
            Recover(h, snapshot: false);
            Assert.IsFalse(h.Item("oo-snapshot-refresh").Pass == true);
            Assert.IsFalse(h.Probe.Passed);
        }

        [Test]
        public void TheOutageProbeFailsDevicesNotObservedAgainInTime()
        {
            var h = OutageRig();
            ToSteady(h);
            RunOutage(h, 40);
            Recover(h, observedAgain: false);
            foreach (var d in h.Board.Devices) d.Quiet = true;   // no fresh measurement reaches any of them
            for (var i = 0; i < 100 && !h.Probe.Finished; i++) h.Advance(1);
            Assert.IsTrue(h.Probe.Finished);
            Assert.IsFalse(h.Item("oo-all-observed-again").Pass == true);
        }

        [Test]
        public void TheOutageProbeFailsCardsThatAreStillToldTheStreamIsLive()
        {
            var h = OutageRig();
            ToSteady(h);
            h.Observer.Measurements.State = StreamState.Reconnecting;
            h.Observer.Measurements.LastLiveAt = h.Now;
            h.Observed.ApplyMeasurement(h.Tokens[0], MeasurementKeys.FuelPct, 40.0, h.Now, h.Now, false);
            // the cards' source is wired to a stream flag that stayed true: a value seconds old would read fresh
            for (var i = 0; i < 30; i++) h.Advance(1);
            Recover(h);
            var item = h.Item("oo-never-fresh");
            Assert.IsFalse(item.Pass == true);
            StringAssert.Contains("live", item.Detail);
        }

        [Test]
        public void TheOutageProbeNeedsTheBannerToSayValuesAreFrozen()
        {
            Assert.IsTrue(PhaseAProbe.BannerFrozen.StartsWith("Observer reconnecting"));
            var status = new ObserverStatus();
            status.Measurements.State = StreamState.Reconnecting;
            status.Measurements.LastLiveAt = T0;
            StringAssert.StartsWith(PhaseAProbe.BannerFrozen, ObserverBanner.Text(status, new ObservedState(T0)), "the probe's expected text is the app's real banner");
        }

        // ------------------------------------------------------------------ soak

        static Rig SoakRig(int minutes = 1) => new Rig(Parse("-sitepulse-acceptance", "soak", "-sitepulse-soak-minutes", minutes.ToString()));

        // runs the soak for its time, a second at a time, keeping the machine, the observer and the lag measurements going
        static void RunSoak(Rig h, bool refuel = true, Action<int> each = null)
        {
            var served = false;
            var limit = (int)PhaseAProbe.SoakSeconds(1) + 400;
            for (var i = 0; i < limit && !h.Probe.Finished; i++)
            {
                each?.Invoke(i);
                foreach (var t in h.Tokens)
                    if (!h.Silent.Contains(t)) h.Observed.ApplyMeasurement(t, MeasurementKeys.FuelPct, 50.0 + (i % 7), h.Now.AddSeconds(-0.1), h.Now, false);
                h.Evaluate();
                if (refuel && !served && h.Prepared.Count > 0 && (h.Now - T0).TotalSeconds > 100) { h.Refuel(h.Prepared[0]); served = true; }
                h.Advance(1);
                if (h.Probe.Finished) break;
                using var doc = h.Result();
                if (doc.RootElement.TryGetProperty("soak", out var soak) && soak.GetProperty("ended").GetBoolean()) h.Flags.Add(PhaseAProbe.FinishFile);
            }
        }

        [Test]
        public void ASoakPassesAFleetThatStaysUpAndRefuelsAndRecordsWhatItMeasured()
        {
            var h = SoakRig();
            h.BringUp();
            RunSoak(h);
            Assert.IsTrue(h.Probe.Finished);
            Assert.IsTrue(h.Probe.Passed, h.Failures());
            CollectionAssert.AreEqual(new[] { "SP-HL-0006" }, h.Prepared);
            using var doc = h.Result();
            var soak = doc.RootElement.GetProperty("soak");
            Assert.IsTrue(soak.GetProperty("ended").GetBoolean());
            Assert.AreEqual(16.0, soak.GetProperty("frame").GetProperty("p50Ms").GetDouble(), 0.01);
            Assert.Greater(soak.GetProperty("frame").GetProperty("n").GetInt32(), 100);
            Assert.AreEqual(100.0, soak.GetProperty("lag").GetProperty("p50Ms").GetDouble(), 1.0);
            Assert.AreEqual(1, soak.GetProperty("cycles").GetArrayLength());
            Assert.AreEqual("SP-HL-0006", soak.GetProperty("cycles")[0].GetProperty("machine").GetString());
            StringAssert.StartsWith("SUCCESS", soak.GetProperty("cycles")[0].GetProperty("outcome").GetString());
            Assert.AreEqual(3, soak.GetProperty("sessions").GetProperty("min").GetInt32());
            Assert.AreEqual(3, soak.GetProperty("sessions").GetProperty("max").GetInt32());
            Assert.GreaterOrEqual(soak.GetProperty("perMinute").GetArrayLength(), 4);
            Assert.AreEqual(0, soak.GetProperty("dropped").GetInt64());
        }

        [Test]
        public void ASoakTakesNoFramesBeforeTheFleetIsObservedAndSettled()
        {
            var h = SoakRig();
            h.Advance(1);
            h.Advance(1);
            using var doc = h.Result();
            Assert.AreEqual(0, doc.RootElement.GetProperty("soak").GetProperty("frame").GetProperty("n").GetInt32());
        }

        [Test]
        public void ASoakWaitsForTheScriptsFlagAndThenGivesUp()
        {
            var h = SoakRig();
            h.BringUp();
            for (var i = 0; i < (int)PhaseAProbe.SoakSeconds(1) + 100 && !h.Probe.Finished; i++)
            {
                if (h.Prepared.Count > 0 && i > 100 && h.Timeline.Rows("SP-HL-0006").All(r => r.Kind != TimelineKinds.Outcome)) h.Refuel("SP-HL-0006");
                h.Advance(1);
            }

            Assert.IsFalse(h.Probe.Finished, "the soak is over but the platform-side checks are not");
            using (var doc = h.Result()) Assert.AreEqual("ending", doc.RootElement.GetProperty("phase").GetString());
            for (var i = 0; i < (int)PhaseAProbe.SoakEndWaitSeconds + 5 && !h.Probe.Finished; i++) h.Advance(1);
            Assert.IsTrue(h.Probe.Finished, "no flag within the wait: the probe finishes rather than hang");
        }

        [Test]
        public void ASoakFailsASampleThatWasDropped()
        {
            var h = SoakRig();
            h.BringUp();
            RunSoak(h, each: i => { if (i == 150) h.Dropped = 3; });
            Assert.IsFalse(h.Item("soak-dropped").Pass == true);
            StringAssert.Contains("3", h.Item("soak-dropped").Detail);
        }

        [Test]
        public void ASoakFailsALeakedSession()
        {
            var h = SoakRig();
            h.BringUp();
            RunSoak(h, each: i => h.Sessions = i == 120 ? 4 : 3);
            Assert.IsFalse(h.Item("soak-sessions").Pass == true);
            StringAssert.Contains("max 4", h.Item("soak-sessions").Detail);
        }

        [Test]
        public void ASoakFailsASessionThatWentMissing()
        {
            var h = SoakRig();
            h.BringUp();
            RunSoak(h, each: i => h.Sessions = i >= 120 && i < 125 ? 2 : 3);
            Assert.IsFalse(h.Item("soak-sessions").Pass == true);
        }

        // the loader's measurements stop reaching the platform (it goes quiet after the board's 15 s) and then resume
        static void SilenceLoader(Rig h, bool silent)
        {
            if (silent) h.Silent.Add(h.Tokens[1]);
            else h.Silent.Remove(h.Tokens[1]);
        }

        [Test]
        public void ASoakAllowsABriefGapAndFailsALongOne()
        {
            var brief = SoakRig();
            brief.BringUp();
            RunSoak(brief, each: i => SilenceLoader(brief, i >= 120 && i < 140));
            Assert.IsTrue(brief.Item("soak-observed").Pass == true, brief.Item("soak-observed").Detail);

            var longGap = SoakRig();
            longGap.BringUp();
            RunSoak(longGap, each: i => SilenceLoader(longGap, i >= 120 && i < 170));
            var item = longGap.Item("soak-observed");
            Assert.IsFalse(item.Pass == true);
            StringAssert.Contains("SP-LD-0001", item.Detail);
        }

        [Test]
        public void ASoakFailsACycleWhoseRefuelNeverEnded()
        {
            var h = SoakRig();
            h.BringUp();
            RunSoak(h, refuel: false);
            var cycle = h.Item("soak-cycle-1");
            Assert.IsFalse(cycle.Pass == true);
            Assert.IsFalse(h.Probe.Passed);
        }

        [Test]
        public void ASoakFailsACycleOnATruckThatWasBusy()
        {
            var h = SoakRig();
            h.BringUp();
            h.Mode = MachineMode.Commanded;
            RunSoak(h);
            Assert.AreEqual(0, h.Prepared.Count, "a truck that is not working its track is not given a low tank");
            StringAssert.Contains("not prepared", h.Item("soak-cycle-1").Detail);
            Assert.IsFalse(h.Item("soak-cycles").Pass == true);
        }

        [Test]
        public void ASoakFailsABayThatIsStillHeldAtTheEnd()
        {
            var h = SoakRig();
            h.BringUp();
            RunSoak(h, each: i => h.Bay = i >= 150 ? new BayView("SP-HL-0006", 1) : new BayView(null, 0));
            var item = h.Item("soak-reservations");
            Assert.IsFalse(item.Pass == true);
            StringAssert.Contains("SP-HL-0006", item.Detail);
        }

        [Test]
        public void ASoakFailsATruckLeftOffItsTrackAtTheEnd()
        {
            var h = SoakRig();
            h.BringUp();
            RunSoak(h, each: i => { if (i >= 330) h.Mode = MachineMode.Stalled; });
            Assert.IsFalse(h.Item("soak-reservations").Pass == true);
        }

        [Test]
        public void ASoakRecordsTheMeasurementsAsInformationAndNotAsAGate()
        {
            var h = SoakRig();
            h.BringUp();
            RunSoak(h);
            foreach (var id in new[] { "soak-frame-time", "soak-observation-lag", "soak-presence", "soak-send-errors" })
                Assert.IsNull(h.Item(id).Pass, id);
            StringAssert.Contains("p95", h.Item("soak-frame-time").Detail);
        }

        // ------------------------------------------------------------------ the quantile helper

        [Test]
        public void QuantilesAreNearestRank()
        {
            var q = new Quantiles();
            Assert.IsNull(q.Of(0.5));
            Assert.IsNull(q.Max);
            for (var i = 1; i <= 100; i++) q.Add(i);
            Assert.AreEqual(50.0, q.Of(0.5));
            Assert.AreEqual(95.0, q.Of(0.95));
            Assert.AreEqual(99.0, q.Of(0.99));
            Assert.AreEqual(100.0, q.Max);
            q.Clear();
            Assert.AreEqual(0, q.Count);
        }

        [Test]
        public void TheObservedStateCountsSnapshotsAndTellsTheLagOfEachStreamedValue()
        {
            var s = new ObservedState(T0);
            var lags = new List<double>();
            s.MeasurementLag = span => lags.Add(span.TotalMilliseconds);
            s.ApplyMeasurement("t", "fuel_pct", 1, T0.AddSeconds(1), T0.AddSeconds(1.25), false);
            s.ApplyMeasurement("t", "fuel_pct", 2, T0.AddSeconds(2), T0.AddSeconds(2), true);
            s.ApplyMeasurement("t", "fuel_pct", 0, T0, T0.AddSeconds(3), false);   // older than what is held: dropped, no lag
            CollectionAssert.AreEqual(new[] { 250.0 }, lags, "only an applied streamed value has a lag");
            Assert.AreEqual(1, s.SnapshotMeasurements);
        }
    }
}
