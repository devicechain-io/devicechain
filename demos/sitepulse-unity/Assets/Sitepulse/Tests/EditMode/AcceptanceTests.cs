// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.DevicePlane;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using DeviceChain.Sitepulse.Visuals;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>The acceptance run's own machinery: its flags, its injected faults, its sample log and its probe. No network, no player.</summary>
    public sealed class AcceptanceTests
    {
        static readonly DateTimeOffset T0 = new DateTimeOffset(2026, 10, 6, 12, 0, 0, TimeSpan.Zero);

        // ------------------------------------------------------------------ flags

        [Test]
        public void NoAcceptanceFlagMeansNoAcceptance()
        {
            var r = AcceptanceFlags.FromCommandLine(new[] { "Sitepulse.exe", "-sitepulse-mode", "live" });
            Assert.IsTrue(r.Ok);
            Assert.IsNull(r.Value);
        }

        [Test]
        public void ATestOnlyFlagWithoutTheAcceptanceFlagIsRefused()
        {
            var control = AcceptanceFlags.FromCommandLine(new[] { "x", "-sitepulse-control", "wrong-ca" });
            Assert.IsFalse(control.Ok);
            StringAssert.Contains("without -sitepulse-acceptance", control.Error);

            var dir = AcceptanceFlags.FromCommandLine(new[] { "x", "-sitepulse-acceptance-dir", "C:\\out" });
            Assert.IsFalse(dir.Ok);
        }

        [Test]
        public void AcceptanceNamesItsRunAndItsFault()
        {
            var plain = AcceptanceFlags.FromCommandLine(new[] { "x", "-sitepulse-acceptance", "phaseA", "-sitepulse-acceptance-dir", "C:\\out" }).Value;
            Assert.AreEqual("ACCEPTANCE phaseA", plain.RunLabel);
            Assert.IsFalse(plain.IsControl);
            Assert.AreEqual("C:\\out", plain.Directory);

            var control = AcceptanceFlags.FromCommandLine(new[] { "x", "-sitepulse-acceptance", "phaseA", "-sitepulse-control", "bogus-binding:SP-HL-0004" }).Value;
            Assert.IsTrue(control.IsControl);
            Assert.AreEqual("ACCEPTANCE CONTROL bogus-binding:SP-HL-0004", control.RunLabel);
        }

        [TestCase("bogus-binding")]
        [TestCase("bad-credential")]
        [TestCase("bogus-binding:")]
        [TestCase("wrong-ca:SP-HL-0001")]
        [TestCase("unplug-the-moon")]
        public void AMalformedControlIsRefused(string spec)
        {
            var r = AcceptanceFlags.FromCommandLine(new[] { "x", "-sitepulse-acceptance", "phaseA", "-sitepulse-control", spec });
            Assert.IsFalse(r.Ok, spec);
        }

        [Test]
        public void AnUnknownAcceptanceIsRefused()
        {
            var r = AcceptanceFlags.FromCommandLine(new[] { "x", "-sitepulse-acceptance", "phaseZ" });
            Assert.IsFalse(r.Ok);
            StringAssert.Contains("phaseZ", r.Error);
        }

        [Test]
        public void AFlagWithNoValueIsRefusedNotDefaulted()
        {
            var r = AcceptanceFlags.FromCommandLine(new[] { "x", "-sitepulse-acceptance" });
            Assert.IsFalse(r.Ok);
        }

        // ------------------------------------------------------------------ faults

        [Test]
        public void ABogusBindingRenamesOnlyTheTargetInTheResolveQuery()
        {
            var seen = new List<(string query, string vars)>();
            QueryFn inner = (q, v, ct) => { seen.Add((q, v)); return Task.FromResult("{}"); };
            var wrapped = AcceptanceControls.WrapBind(inner, new ControlSpec(ControlSpec.BogusBinding, "SP-HL-0004"));

            Answer(wrapped(DeviceBinder.ResolveQuery, VarsJson.StringList("ids", new[] { "SP-HL-0003", "SP-HL-0004", "SP-HL-0005" }), CancellationToken.None));
            Answer(wrapped(CredentialResolver.BatchQuery, VarsJson.StringList("tokens", new[] { "SP-HL-0004" }), CancellationToken.None));

            using var resolve = JsonDocument.Parse(seen[0].vars);
            var ids = resolve.RootElement.GetProperty("ids").EnumerateArray().Select(e => e.GetString()).ToArray();
            CollectionAssert.AreEqual(new[] { "SP-HL-0003", "SP-HL-0004" + AcceptanceControls.BogusSuffix, "SP-HL-0005" }, ids);
            StringAssert.Contains("SP-HL-0004", seen[1].vars);
            StringAssert.DoesNotContain(AcceptanceControls.BogusSuffix, seen[1].vars, "no other query is touched");
        }

        [Test]
        public void OnlyABogusBindingWrapsTheQuery()
        {
            QueryFn inner = (q, v, ct) => Task.FromResult("{}");
            Assert.AreSame(inner, AcceptanceControls.WrapBind(inner, null));
            Assert.AreSame(inner, AcceptanceControls.WrapBind(inner, new ControlSpec(ControlSpec.WrongCa, null)));
            Assert.AreSame(inner, AcceptanceControls.WrapBind(inner, new ControlSpec(ControlSpec.BadCredential, "SP-HL-0004")));
        }

        [Test]
        public void ABogusBindingMakesTheTargetMissingAndNothingElse()
        {
            var contract = PlatformTestData.Contract();
            var devices = new[] { new SceneDevice("SP-HL-0001", SceneKind.Hauler), new SceneDevice("SP-HL-0002", SceneKind.Hauler) };
            // the platform knows both devices; the renamed query asks only for one of them under its real name
            var data = PlatformTestData.Devices(PlatformTestData.Hauler("01"));
            var results = DeviceBinder.Classify(contract, devices, data);
            Assert.AreEqual(BindOutcome.Bound, results[0].Outcome);
            Assert.AreEqual(BindOutcome.Missing, results[1].Outcome);
        }

        [Test]
        public void ACorruptedCredentialDiffersByExactlyOneCharacter()
        {
            var credentials = new DeviceCredentials();
            const string id = "0123456789abcdef0123456789abcdef";
            credentials.Set("tok", id);
            Assert.IsTrue(AcceptanceControls.CorruptCredential(credentials, "tok"));
            Assert.IsTrue(credentials.TryGet("tok", out var changed));
            Assert.AreEqual(id.Length, changed.Length);
            Assert.AreEqual(1, Enumerable.Range(0, id.Length).Count(i => id[i] != changed[i]));

            credentials.Set("zero", "0000");
            AcceptanceControls.CorruptCredential(credentials, "zero");
            credentials.TryGet("zero", out var z);
            Assert.AreEqual("0001", z);
        }

        [Test]
        public void CorruptingACredentialThatIsNotThereSaysSo()
        {
            var credentials = new DeviceCredentials();
            Assert.IsFalse(AcceptanceControls.CorruptCredential(credentials, "tok"));
            Assert.IsFalse(AcceptanceControls.CorruptCredential(credentials, null));
        }

        // ------------------------------------------------------------------ the emitted-sample log

        [Test]
        public void AMeasurementLineCarriesTheDeviceTheTimeAndTheValues()
        {
            var values = new Dictionary<string, double> { ["fuel_pct"] = 55.25, ["engine_temp_c"] = 90 };
            var line = EmittedSampleLog.Format("SP-HL-0001", "sp-hauler-01", Sample.Measurement(1, T0.AddMilliseconds(123), values));
            using var doc = JsonDocument.Parse(line);
            var root = doc.RootElement;
            Assert.AreEqual("SP-HL-0001", root.GetProperty("device").GetString());
            Assert.AreEqual("sp-hauler-01", root.GetProperty("deviceToken").GetString());
            Assert.AreEqual("measurement", root.GetProperty("kind").GetString());
            Assert.AreEqual(T0.AddMilliseconds(123), DateTimeOffset.Parse(root.GetProperty("occurredTime").GetString(), null, System.Globalization.DateTimeStyles.AssumeUniversal));
            Assert.AreEqual(55.25, root.GetProperty("values").GetProperty("fuel_pct").GetDouble());
        }

        [Test]
        public void ALocationLineCarriesSpeedHeadingAndElevation()
        {
            var line = EmittedSampleLog.Format("SP-LD-0002", "sp-loader-02", Sample.Location(2, T0, new GeoPoint(39.001, -117.002), 1432.5, 6.5, 271.0));
            using var doc = JsonDocument.Parse(line);
            var root = doc.RootElement;
            Assert.AreEqual("location", root.GetProperty("kind").GetString());
            Assert.AreEqual(6.5, root.GetProperty("speed").GetDouble());
            Assert.AreEqual(271.0, root.GetProperty("heading").GetDouble());
            Assert.AreEqual(1432.5, root.GetProperty("elevation").GetDouble());
            Assert.AreEqual(39.001, root.GetProperty("latitude").GetDouble());
        }

        [Test]
        public void TheLogRecordsOnlyWhatTheBrokerTookAndSurvivesBeingClosed()
        {
            var path = Path.Combine(Path.GetTempPath(), "sitepulse-samples-" + Guid.NewGuid().ToString("N") + ".jsonl");
            try
            {
                var log = new EmittedSampleLog(path);
                var inner = new FakeFactory();
                var factory = new RecordingLinkFactory(inner, log);
                var link = factory.Create("SP-HL-0001", "sp-hauler-01", "0123456789abcdef0123456789abcdef");

                var ok = link.PublishAsync(Sample.Measurement(1, T0, new Dictionary<string, double> { ["fuel_pct"] = 50 }), CancellationToken.None);
                Assert.IsTrue(ok.IsCompleted);
                inner.Link.Fail = true;
                var bad = link.PublishAsync(Sample.Measurement(2, T0.AddSeconds(1), new Dictionary<string, double> { ["fuel_pct"] = 49 }), CancellationToken.None);
                Assert.IsTrue(bad.IsCompleted);
                Assert.IsTrue(bad.IsFaulted, "a failed publish is still a failure to the caller");
                Assert.AreEqual(1, log.Lines, "a sample the broker did not take is not in the log");

                log.Dispose();
                inner.Link.Fail = false;
                Assert.DoesNotThrow(() => link.PublishAsync(Sample.Measurement(3, T0.AddSeconds(2), new Dictionary<string, double> { ["fuel_pct"] = 48 }), CancellationToken.None));

                var lines = File.ReadAllLines(path);
                Assert.AreEqual(1, lines.Length);
                StringAssert.DoesNotContain("0123456789abcdef0123456789abcdef", lines[0], "the credential is not part of a sample");
            }
            finally
            {
                try { File.Delete(path); } catch (IOException) { }
            }
        }

        [Test]
        public void ALogLineNeverCarriesASecret()
        {
            const string secret = "0123456789abcdef0123456789abcdef";
            var line = EmittedSampleLog.Format("SP-HL-0001", secret, Sample.Measurement(1, T0, new Dictionary<string, double> { ["fuel_pct"] = 1 }));
            var path = Path.Combine(Path.GetTempPath(), "sitepulse-samples-" + Guid.NewGuid().ToString("N") + ".jsonl");
            try
            {
                using (var log = new EmittedSampleLog(path))
                    log.Record("SP-HL-0001", secret, Sample.Measurement(1, T0, new Dictionary<string, double> { ["fuel_pct"] = 1 }));
                StringAssert.Contains(secret, line, "the raw formatter does not hide it...");
                StringAssert.DoesNotContain(secret, File.ReadAllText(path), "...the file does");
            }
            finally
            {
                try { File.Delete(path); } catch (IOException) { }
            }
        }

        // ------------------------------------------------------------------ the probe

        sealed class Harness
        {
            public DateTimeOffset Now = T0;
            public readonly ReadinessBoard Board;
            public readonly ObservedState Observed;
            public readonly Timeline Timeline;
            public readonly Dictionary<string, string> Files = new Dictionary<string, string>();
            public readonly HashSet<string> Flags = new HashSet<string>();
            public Provenance? Source = Provenance.Observed;
            public Provenance CardProvenance = Provenance.Observed;
            public int Prepared;
            public readonly PhaseAProbe Probe;
            public readonly string[] Tokens;

            public Harness(AcceptanceOptions options = null, bool fillObserved = true)
            {
                var devices = new[]
                {
                    new SceneDevice("SP-HL-0006", SceneKind.Hauler),
                    new SceneDevice("SP-LD-0001", SceneKind.Loader),
                    new SceneDevice("SP-PL-0001", SceneKind.Plant),
                };
                Board = new ReadinessBoard(devices, "t");
                Observed = new ObservedState(T0);
                Timeline = new Timeline(() => Now);
                Tokens = new[] { "tok-hl6", "tok-ld1", "tok-pl1" };
                for (var i = 0; i < devices.Length; i++)
                {
                    Board.SetBind(new BindResult { ExternalId = devices[i].ExternalId, Outcome = BindOutcome.Bound, DeviceToken = Tokens[i] });
                    Board.SetCredential(new CredentialOutcome { ExternalId = devices[i].ExternalId, Ok = true, Path = CredentialPath.Batched });
                    if (fillObserved) FillObserved(i);
                }

                var world = new ProbeWorld
                {
                    Board = Board,
                    Observed = Observed,
                    Cards = () => Cards(),
                    CardSource = () => Source,
                    Timeline = () => Timeline,
                    PrepareLowFuel = id => { Prepared++; Timeline.Add(id, TimelineKinds.Presenter, "prepare low-fuel cycle"); return "prepare low-fuel cycle: fuel 40.0% -> 15.5%"; },
                    FileExists = name => Flags.Contains(name),
                    WriteFile = (name, text) => Files[name] = text,
                    SamplePath = "C:\\out\\samples.jsonl",
                    SampleLines = () => 12,
                    UnityVersion = "test",
                };
                Probe = new PhaseAProbe(options ?? new AcceptanceOptions { Name = AcceptanceOptions.PhaseA }, world, T0, () => Now);
            }

            void FillObserved(int i)
            {
                var plant = i == 2;
                var keys = plant ? new[] { "throughput_tph", "plant_running" } : MeasurementKeys.Equipment.ToArray();
                foreach (var key in keys)
                    Observed.ApplyMeasurement(Tokens[i], key, 1.0, T0.AddSeconds(1), T0.AddSeconds(1), false);
            }

            IEnumerable<DeviceReading> Cards()
            {
                var r = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment, CardProvenance);
                var stamp = CardProvenance == Provenance.Illustrative ? (Observation?)null : new Observation(T0, T0);
                r.Set(MeasurementKeys.FuelPct, 50, CardProvenance, stamp);
                yield return r;
            }

            public void Advance(double seconds)
            {
                Now = Now.AddSeconds(seconds);
                Probe.Tick();
            }

            public void Climb(DeviceStage stage)
            {
                Board.BeginSessions();
                foreach (var d in Board.Devices)
                {
                    Board.SetStage(d.Device.ExternalId, stage);
                }
            }

            public void Refuel(bool complete = true)
            {
                Timeline.Add("SP-HL-0006", TimelineKinds.Received, "goto-refuel (\u2026abcdef1234)");
                Timeline.Add("SP-HL-0006", TimelineKinds.Accepted, "goto-refuel: route 212 m, ETA 48 s");
                Timeline.Add("SP-HL-0006", TimelineKinds.Refuelling, "service started at 14.9% fuel");
                Timeline.Add("SP-HL-0006", TimelineKinds.Refuelling, "service finished: 14.9% -> 100.0%");
                if (complete) Timeline.Add("SP-HL-0006", TimelineKinds.Outcome, "SUCCESS");
            }
        }

        static void RunToCards(Harness h, double observedAt)
        {
            h.Advance(1);
            h.Climb(DeviceStage.Publishing);
            h.Advance(observedAt - 1);
            foreach (var t in h.Tokens) h.Board.MarkObserved(t);
            h.Advance(1);
            h.Advance(6);
        }

        [Test]
        public void TheProbePassesWhenEveryThingHappensAndSaysHowLongItTook()
        {
            var h = new Harness();
            RunToCards(h, 12);
            Assert.AreEqual(1, h.Prepared, "the low-fuel cycle is prepared once the cards are checked");
            h.Refuel();
            h.Flags.Add(PhaseAProbe.FinishFile);
            h.Advance(1);

            Assert.IsTrue(h.Probe.Finished);
            Assert.IsTrue(h.Probe.Passed, string.Join("; ", h.Probe.Items.Select(i => i.Id + "=" + i.Pass + " " + i.Detail)));
            Assert.AreEqual(0, h.Probe.ExitCode);
            using var doc = JsonDocument.Parse(h.Files[PhaseAProbe.ResultFile]);
            var root = doc.RootElement;
            Assert.IsTrue(root.GetProperty("final").GetBoolean());
            Assert.IsTrue(root.GetProperty("pass").GetBoolean());
            Assert.AreEqual("ACCEPTANCE phaseA", root.GetProperty("run").GetString());
            Assert.IsFalse(root.GetProperty("control").GetBoolean());
            Assert.AreEqual(12.0, root.GetProperty("timing").GetProperty("observedSeconds").GetDouble(), 1.0);
            Assert.AreEqual(12, root.GetProperty("samples").GetProperty("lines").GetInt32());
            Assert.IsTrue(root.GetProperty("deviceLog").TryGetProperty("SP-HL-0006", out var rows));
            Assert.IsTrue(rows.GetArrayLength() >= 5);
            foreach (var item in root.GetProperty("items").EnumerateArray())
                if (item.GetProperty("pass").ValueKind != JsonValueKind.Null) Assert.IsTrue(item.GetProperty("pass").GetBoolean(), item.GetProperty("id").GetString());
        }

        [Test]
        public void TheProbeFailsAFleetThatWasObservedAfterThirtySeconds()
        {
            var h = new Harness();
            RunToCards(h, 45);
            h.Refuel();
            h.Flags.Add(PhaseAProbe.FinishFile);
            h.Advance(1);

            Assert.IsTrue(h.Probe.Finished);
            Assert.IsFalse(h.Probe.Passed);
            Assert.AreEqual(1, h.Probe.ExitCode);
            var item = h.Probe.Items.First(i => i.Id == "observed-19");
            Assert.IsFalse(item.Pass);
            StringAssert.Contains("budget", item.Detail);
        }

        [Test]
        public void TheProbeDoesNotFinishOnTheFlagBeforeItHasPreparedTheTank()
        {
            var h = new Harness();
            h.Flags.Add(PhaseAProbe.FinishFile);
            h.Advance(1);
            h.Advance(1);
            Assert.IsFalse(h.Probe.Finished, "a finish flag left behind by an earlier run must not end this one");
        }

        [Test]
        public void TheProbeFailsWhenTheRefuelNeverEndsInSuccess()
        {
            var h = new Harness();
            RunToCards(h, 12);
            h.Refuel(complete: false);
            h.Flags.Add(PhaseAProbe.FinishFile);
            h.Advance(1);

            Assert.IsFalse(h.Probe.Passed);
            var item = h.Probe.Items.First(i => i.Id == "device-log-refuel");
            Assert.IsFalse(item.Pass);
            StringAssert.Contains("outcome SUCCESS", item.Detail);
            StringAssert.Contains("abcdef1234", item.Detail);
        }

        [Test]
        public void ARefuelBeforeTheTankWasPreparedIsNotTheRulesRefuel()
        {
            var h = new Harness();
            h.Refuel();   // an earlier command's rows, already in the timeline
            h.Advance(1);
            h.Now = h.Now.AddSeconds(1);
            RunToCards(h, 12);
            h.Flags.Add(PhaseAProbe.FinishFile);
            h.Advance(1);
            Assert.IsFalse(h.Probe.Items.First(i => i.Id == "device-log-refuel").Pass == true);
        }

        [Test]
        public void TheProbeGivesUpWhenTheFleetNeverComesUp()
        {
            var h = new Harness();
            h.Advance(1);
            h.Climb(DeviceStage.Publishing);
            h.Advance(PhaseAProbe.ReachBudgetSeconds);
            h.Advance(1);
            Assert.IsTrue(h.Probe.Finished);
            Assert.IsFalse(h.Probe.Passed);
            Assert.IsFalse(h.Probe.Items.First(i => i.Id == "observed-19").Pass);
            Assert.AreEqual(0, h.Prepared, "nothing is prepared on a fleet that never came up");
        }

        [Test]
        public void TheProbeStopsAtItsTotalBudgetWhateverTheFlagSays()
        {
            var h = new Harness();
            RunToCards(h, 12);
            h.Advance(PhaseAProbe.TotalBudgetSeconds);
            Assert.IsTrue(h.Probe.Finished);
            Assert.IsFalse(h.Probe.Passed);
        }

        [Test]
        public void AnIllustrativeCardIsCaught()
        {
            var h = new Harness();
            h.CardProvenance = Provenance.Illustrative;
            var item = PhaseAProbe.CheckCards(Of(h));
            Assert.IsFalse(item.Pass);
            StringAssert.Contains("Illustrative", item.Detail);
        }

        [Test]
        public void ASourceThatIsNotObservedIsCaught()
        {
            var h = new Harness();
            h.Source = Provenance.Illustrative;
            var item = PhaseAProbe.CheckCards(Of(h));
            Assert.IsFalse(item.Pass);
            StringAssert.Contains("not Observed", item.Detail);
        }

        [Test]
        public void AnObservedCardOverObservedStatePasses()
        {
            var h = new Harness();
            var item = PhaseAProbe.CheckCards(Of(h));
            Assert.IsTrue(item.Pass, item.Detail);
        }

        [Test]
        public void AValueFromBeforeThisRunIsNotThisRunsObservation()
        {
            var h = new Harness(fillObserved: false);
            // the platform holds a value, but from before the player started
            h.Observed.ApplyMeasurement("tok-hl6", "fuel_pct", 50, T0.AddSeconds(-30), T0.AddSeconds(1), true);
            var item = PhaseAProbe.CheckCards(Of(h));
            Assert.IsFalse(item.Pass);
            StringAssert.Contains("before this run began", item.Detail);
        }

        [Test]
        public void ADeviceWithNoObservationBreaksTheCardCheck()
        {
            var h = new Harness(fillObserved: false);
            var item = PhaseAProbe.CheckCards(Of(h));
            Assert.IsFalse(item.Pass);
            StringAssert.Contains("no observed fuel_pct", item.Detail);
        }

        [Test]
        public void TheProbeExpectsWhatEachKindOfMachineEmits()
        {
            foreach (SceneKind kind in Enum.GetValues(typeof(SceneKind)))
            {
                var emitted = new MachineModel(DeviceSessionHost.ToEquipment(kind), "SP-X-0001").Measurements().Keys;
                CollectionAssert.AreEquivalent(emitted, PhaseAProbe.ExpectedKeys(kind), kind.ToString());
            }

            // spelled out once, so a change to the model's vocabulary is a decision and not a drift
            CollectionAssert.AreEquivalent(new[] { "fuel_pct", "engine_temp_c", "engine_hours" }, PhaseAProbe.ExpectedKeys(SceneKind.Dozer));
            CollectionAssert.AreEquivalent(new[] { "throughput_tph", "plant_running" }, PhaseAProbe.ExpectedKeys(SceneKind.Plant));
            CollectionAssert.Contains(PhaseAProbe.ExpectedKeys(SceneKind.Hauler), "tyre_pressure_kpa");
        }

        [Test]
        public void ADozerAndThePlantPassTheCardCheckWithOnlyWhatTheyEmit()
        {
            var devices = new[] { new SceneDevice("SP-DZ-0001", SceneKind.Dozer), new SceneDevice("SP-PL-0001", SceneKind.Plant) };
            var board = new ReadinessBoard(devices, "t");
            var observed = new ObservedState(T0);
            for (var i = 0; i < devices.Length; i++)
            {
                var token = "tok-" + i;
                board.SetBind(new BindResult { ExternalId = devices[i].ExternalId, Outcome = BindOutcome.Bound, DeviceToken = token });
                foreach (var key in PhaseAProbe.ExpectedKeys(devices[i].Kind))
                    observed.ApplyMeasurement(token, key, 1.0, T0.AddSeconds(1), T0.AddSeconds(1), false);
            }

            var world = new ProbeWorld { Board = board, Observed = observed, Cards = () => new[] { new DeviceReading("SP-DZ-0001", DeviceReading.Profile.Equipment, Provenance.Observed) }, CardSource = () => Provenance.Observed };
            var item = PhaseAProbe.CheckCards(world);
            Assert.IsTrue(item.Pass, item.Detail);
            StringAssert.DoesNotContain("payload_t", item.Detail);

            // the counter-case: a dozer that did not report its fuel is still caught
            var bare = new ObservedState(T0);
            bare.ApplyMeasurement("tok-0", "engine_temp_c", 1.0, T0.AddSeconds(1), T0.AddSeconds(1), false);
            world.Observed = bare;
            var missing = PhaseAProbe.CheckCards(world);
            Assert.IsFalse(missing.Pass);
            StringAssert.Contains("SP-DZ-0001: no observed fuel_pct", missing.Detail);
        }

        [Test]
        public void ABadCredentialWhoseSessionStartFailedWithTheBrokersRefusalPasses()
        {
            var b = Fleet();
            Observe(b, "SP-HL-0001", "SP-HL-0002", "SP-HL-0003");
            b.SetStage("SP-HL-0004", DeviceStage.Connecting);
            b.FailSession("SP-HL-0004", "session could not start · " + SessionFailure.Word("MqttConnectionException: connecting to ssl://x:1883/ failed: Connecting with MQTT server failed (NotAuthorized)."));
            b.SetSide("SP-HL-0004", DeviceSide.Stopped);
            var item = PhaseAProbe.EvaluateControl(new ControlSpec(ControlSpec.BadCredential, "SP-HL-0004"), b, b.Brief());
            Assert.IsTrue(item.Pass, item.Detail);
        }

        [Test]
        public void ABadCredentialWhoseStartFailedForAnotherReasonIsAFailedControl()
        {
            var b = Fleet();
            Observe(b, "SP-HL-0001", "SP-HL-0002", "SP-HL-0003");
            b.SetStage("SP-HL-0004", DeviceStage.Connecting);
            b.FailSession("SP-HL-0004", "session could not start · " + SessionFailure.Word("SocketException: No route to host"));
            var item = PhaseAProbe.EvaluateControl(new ControlSpec(ControlSpec.BadCredential, "SP-HL-0004"), b, b.Brief());
            Assert.IsFalse(item.Pass, "a network fault is not the broker's refusal");
            StringAssert.Contains("expected a device the broker refused", item.Detail);
        }

        [Test]
        public void ABadCredentialDeviceThatReachedReadyIsAFailedControlEvenIfFailedLater()
        {
            var b = Fleet();
            Observe(b, "SP-HL-0001", "SP-HL-0002", "SP-HL-0003");
            b.SetStage("SP-HL-0004", DeviceStage.Ready);
            b.FailSession("SP-HL-0004", "session could not start · " + SessionFailure.Word("NotAuthorized"));
            var item = PhaseAProbe.EvaluateControl(new ControlSpec(ControlSpec.BadCredential, "SP-HL-0004"), b, b.Brief());
            Assert.IsFalse(item.Pass);
            StringAssert.Contains("got past the broker", item.Detail);
        }

        [Test]
        public void ABadCredentialWhereAnotherDeviceFailedIsAFailedControl()
        {
            var b = Fleet();
            Observe(b, "SP-HL-0001", "SP-HL-0002");
            b.FailSession("SP-HL-0003", "session could not start · " + SessionFailure.Word("NotAuthorized"));
            b.SetStage("SP-HL-0004", DeviceStage.Connecting);
            b.FailSession("SP-HL-0004", "session could not start · " + SessionFailure.Word("NotAuthorized"));
            var item = PhaseAProbe.EvaluateControl(new ControlSpec(ControlSpec.BadCredential, "SP-HL-0004"), b, b.Brief());
            Assert.IsFalse(item.Pass);
            StringAssert.Contains("SP-HL-0003", item.Detail);
        }

        [Test]
        public void ABrokerRefusalIsWordedPlainlyAndAnythingElseKeepsItsText()
        {
            Assert.AreEqual("refused by the broker (NotAuthorized)", SessionFailure.Word("MqttConnectionException: connecting to ssl://localhost:1883/ as \"a:b:c\" failed: Connecting with MQTT server failed (NotAuthorized)."));
            Assert.AreEqual("refused by the broker (BadUserNameOrPassword)", SessionFailure.Word("failed (BadUserNameOrPassword)"));
            Assert.AreEqual("SocketException: No route to host", SessionFailure.Word("SocketException: No route to host"));
            Assert.IsNull(SessionFailure.BrokerRefusalCode("connection refused"), "a closed port is not a credential refusal");
            Assert.IsTrue(SessionFailure.SaysBrokerRefused("SP-DZ-0002 · blind · the broker refused this device; it will not publish"));
            Assert.IsFalse(SessionFailure.SaysBrokerRefused("SP-DZ-0002 · session could not start · the certificate is not trusted"));
        }

        static ProbeWorld Of(Harness h) => new ProbeWorld
        {
            Board = h.Board,
            Observed = h.Observed,
            Cards = () => new[] { Reading(h) },
            CardSource = () => h.Source,
        };

        static DeviceReading Reading(Harness h)
        {
            var r = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment, h.CardProvenance);
            r.Set(MeasurementKeys.FuelPct, 50, h.CardProvenance, h.CardProvenance == Provenance.Illustrative ? (Observation?)null : new Observation(T0, T0));
            return r;
        }

        // ------------------------------------------------------------------ controls

        static ReadinessBoard Fleet(int n = 4)
        {
            var devices = Enumerable.Range(1, n).Select(i => new SceneDevice("SP-HL-000" + i, SceneKind.Hauler)).ToArray();
            var b = new ReadinessBoard(devices, "t");
            foreach (var d in devices)
            {
                b.SetBind(new BindResult { ExternalId = d.ExternalId, Outcome = BindOutcome.Bound, DeviceToken = "tok-" + d.ExternalId });
                b.SetCredential(new CredentialOutcome { ExternalId = d.ExternalId, Ok = true, Path = CredentialPath.Batched });
            }

            b.BeginSessions();
            return b;
        }

        static void Observe(ReadinessBoard b, params string[] ids)
        {
            foreach (var id in ids)
            {
                b.SetStage(id, DeviceStage.Connecting);
                b.MarkObserved("tok-" + id);
            }
        }

        [Test]
        public void ABogusBindingThatLeavesThePlaceholderAndTheOthersObservedPasses()
        {
            var b = Fleet();
            Observe(b, "SP-HL-0001", "SP-HL-0002", "SP-HL-0003");
            b.SetBind(new BindResult { ExternalId = "SP-HL-0004", Outcome = BindOutcome.Missing });
            var item = PhaseAProbe.EvaluateControl(new ControlSpec(ControlSpec.BogusBinding, "SP-HL-0004"), b, b.Brief());
            Assert.IsTrue(item.Pass, item.Detail);
        }

        [Test]
        public void ABogusBindingThatStillBoundIsAFailedControl()
        {
            var b = Fleet();
            Observe(b, "SP-HL-0001", "SP-HL-0002", "SP-HL-0003", "SP-HL-0004");
            var item = PhaseAProbe.EvaluateControl(new ControlSpec(ControlSpec.BogusBinding, "SP-HL-0004"), b, b.Brief());
            Assert.IsFalse(item.Pass);
        }

        [Test]
        public void ABogusBindingThatCostAnotherDeviceItsObservationIsAFailedControl()
        {
            var b = Fleet();
            Observe(b, "SP-HL-0001", "SP-HL-0002");
            b.SetBind(new BindResult { ExternalId = "SP-HL-0004", Outcome = BindOutcome.Missing });
            var item = PhaseAProbe.EvaluateControl(new ControlSpec(ControlSpec.BogusBinding, "SP-HL-0004"), b, b.Brief());
            Assert.IsFalse(item.Pass);
            StringAssert.Contains("SP-HL-0003", item.Detail);
        }

        [Test]
        public void ABadCredentialThatTheBrokerRefusedPasses()
        {
            var b = Fleet();
            Observe(b, "SP-HL-0001", "SP-HL-0002", "SP-HL-0003");
            b.SetStage("SP-HL-0004", DeviceStage.Connecting);
            b.SetSide("SP-HL-0004", DeviceSide.Blind);
            b.FailSession("SP-HL-0004", "blind · the broker refused this device");
            var item = PhaseAProbe.EvaluateControl(new ControlSpec(ControlSpec.BadCredential, "SP-HL-0004"), b, b.Brief());
            Assert.IsTrue(item.Pass, item.Detail);
        }

        [Test]
        public void ABadCredentialThatPublishedAnywayIsAFailedControl()
        {
            var b = Fleet();
            Observe(b, "SP-HL-0001", "SP-HL-0002", "SP-HL-0003", "SP-HL-0004");
            var item = PhaseAProbe.EvaluateControl(new ControlSpec(ControlSpec.BadCredential, "SP-HL-0004"), b, b.Brief());
            Assert.IsFalse(item.Pass);
        }

        [Test]
        public void AWrongCaThatRefusedEveryoneIsNotConnectedAndQuiet()
        {
            var b = Fleet();
            foreach (var d in b.Devices) b.FailSession(d.Device.ExternalId, "session could not start · the certificate is not trusted");
            var item = PhaseAProbe.EvaluateControl(new ControlSpec(ControlSpec.WrongCa, null), b, b.Brief());
            Assert.IsTrue(item.Pass, item.Detail);
        }

        [Test]
        public void AWrongCaThatLetADeviceConnectIsAFailedControl()
        {
            var b = Fleet();
            foreach (var d in b.Devices.Skip(1)) b.FailSession(d.Device.ExternalId, "session could not start");
            b.SetStage("SP-HL-0001", DeviceStage.Ready);
            var item = PhaseAProbe.EvaluateControl(new ControlSpec(ControlSpec.WrongCa, null), b, b.Brief());
            Assert.IsFalse(item.Pass);
            StringAssert.Contains("SP-HL-0001", item.Detail);
        }

        [Test]
        public void ARunnerStopControlPassesWhileTheWholeFleetIsStillObserved()
        {
            var b = Fleet();
            Observe(b, "SP-HL-0001", "SP-HL-0002", "SP-HL-0003", "SP-HL-0004");
            var item = PhaseAProbe.EvaluateControl(new ControlSpec(ControlSpec.RunnerStop, null), b, b.Brief());
            Assert.IsTrue(item.Pass, item.Detail);
        }

        [Test]
        public void ARunnerStopThatCostTheFleetItsObservationIsNamed()
        {
            var b = Fleet();
            Observe(b, "SP-HL-0001", "SP-HL-0002", "SP-HL-0003");
            var item = PhaseAProbe.EvaluateControl(new ControlSpec(ControlSpec.RunnerStop, null), b, b.Brief());
            Assert.IsFalse(item.Pass);
            StringAssert.Contains("SP-HL-0004", item.Detail);
        }

        [Test]
        public void ARunnerStopControlParsesWithoutADevice()
        {
            Assert.IsTrue(ControlSpec.Parse("runner-stop").Ok);
            Assert.IsFalse(ControlSpec.Parse("runner-stop:SP-HL-0001").Ok);
        }

        [Test]
        public void AControlRunIsLabelledAsOneAndOnlyItsOwnVerdictDecidesIt()
        {
            var options = new AcceptanceOptions { Name = AcceptanceOptions.PhaseA, Control = new ControlSpec(ControlSpec.WrongCa, null) };
            var h = new Harness(options, fillObserved: false);
            foreach (var d in h.Board.Devices) h.Board.FailSession(d.Device.ExternalId, "session could not start · the certificate is not trusted");
            h.Board.BeginSessions();
            h.Advance(1);
            h.Advance(PhaseAProbe.ControlCheckSeconds);

            Assert.IsTrue(h.Probe.Finished);
            Assert.IsTrue(h.Probe.Passed, string.Join("; ", h.Probe.Items.Select(i => i.Id + "=" + i.Pass + " " + i.Detail)));
            using var doc = JsonDocument.Parse(h.Files[PhaseAProbe.ResultFile]);
            Assert.IsTrue(doc.RootElement.GetProperty("control").GetBoolean());
            Assert.AreEqual("ACCEPTANCE CONTROL wrong-ca", doc.RootElement.GetProperty("run").GetString());
            Assert.AreEqual("wrong-ca", doc.RootElement.GetProperty("controlSpec").GetProperty("kind").GetString());
            Assert.AreEqual(0, h.Prepared, "a control never prepares a tank");
        }

        [Test]
        public void AControlThatChangedNothingFailsItsRun()
        {
            var options = new AcceptanceOptions { Name = AcceptanceOptions.PhaseA, Control = new ControlSpec(ControlSpec.WrongCa, null) };
            var h = new Harness(options, fillObserved: false);
            h.Advance(1);
            h.Climb(DeviceStage.Publishing);
            foreach (var t in h.Tokens) h.Board.MarkObserved(t);
            h.Advance(PhaseAProbe.ControlCheckSeconds);
            Assert.IsTrue(h.Probe.Finished);
            Assert.IsFalse(h.Probe.Passed, "a wrong CA that left the fleet observed is a control that did not fail");
            Assert.AreEqual(1, h.Probe.ExitCode);
        }

        // ------------------------------------------------------------------ the result of a run that could not start

        [Test]
        public void AnAbortedRunLeavesAFailedResultWithItsReasonRedacted()
        {
            var options = new AcceptanceOptions { Name = AcceptanceOptions.PhaseA };
            var text = PhaseAProbe.AbortedResult(options, T0, T0.AddSeconds(3), "6000.5.3f1", "the runner said 0123456789abcdef0123456789abcdef");
            using var doc = JsonDocument.Parse(text);
            var root = doc.RootElement;
            Assert.IsTrue(root.GetProperty("final").GetBoolean());
            Assert.IsFalse(root.GetProperty("pass").GetBoolean());
            Assert.AreEqual(1, root.GetProperty("exitCode").GetInt32());
            StringAssert.DoesNotContain("0123456789abcdef0123456789abcdef", root.GetProperty("error").GetString());
        }

        [Test]
        public void TheTokenTailIsTheTextBetweenTheLastBrackets()
        {
            Assert.AreEqual("\u2026abc", PhaseAProbe.TokenTail("goto-refuel (\u2026abc)"));
            Assert.AreEqual("\u2026xyz", PhaseAProbe.TokenTail("goto-area sp-zone-yard (\u2026xyz)"));
            Assert.AreEqual("no brackets", PhaseAProbe.TokenTail("no brackets"));
        }

        // ------------------------------------------------------------------ fakes

        static T Answer<T>(Task<T> task)
        {
            Assert.IsTrue(task.IsCompleted, "the query did not finish");
            return task.Result;
        }

        sealed class FakeFactory : IDeviceLinkFactory
        {
            public FakeLink Link;

            public IDeviceLink Create(string externalId, string deviceToken, string credentialId) => Link = new FakeLink();
        }

        sealed class FakeLink : IDeviceLink
        {
            public bool Fail;
            public event Action<LinkState> StateChanged { add { } remove { } }
            public bool CanPublish => true;
            public Task StartAsync(CommandHandler handler, CancellationToken cancellationToken) => Task.CompletedTask;
            public Task PublishAsync(Sample sample, CancellationToken cancellationToken) => Fail ? Task.FromException(new InvalidOperationException("the broker said no")) : Task.CompletedTask;
            public ValueTask DisposeAsync() => default;
        }
    }
}
