// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using DeviceChain.Demos.Platform;
using DeviceChain.Harborpulse.Model;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    sealed class HarborTestSource : IHarborSource
    {
        public ScenarioStatus Status { get; set; }
        public IReadOnlyList<HarborAlarm> Alarms { get; set; } = Array.Empty<HarborAlarm>();
        public IReadOnlyList<HarborCommand> Commands { get; set; } = Array.Empty<HarborCommand>();
        public CommandCoverage CommandPoll { get; set; }
        public AlarmCoverage AlarmObservation { get; set; }
        public RolloutEvidence Rollout { get; set; }
        public IReadOnlyList<SuppressionEvidence> Suppressions { get; set; } = Array.Empty<SuppressionEvidence>();
        public string ReadProblem => null;
        public readonly Dictionary<string, Reading> Values = new Dictionary<string, Reading>();
        public HarborLocation? Location { get; set; }
        public bool TryToken(string id, out string token) { token = id == null ? null : "bound-" + id; return token != null; }
        public bool TryGet(string id, string metric, out Reading value) => Values.TryGetValue(id + metric, out value);
        public bool TryAttribute(string id, string key, out Reading value) => Values.TryGetValue(id + key, out value);
        public bool TryLocation(string id, out HarborLocation location) { location = Location.GetValueOrDefault(); return Location.HasValue; }
        public void Set(string id, string metric, double value, DateTimeOffset at) => Values[id + metric] = new Reading(value, at);
    }

    public class U1Tests
    {
        static DateTimeOffset Now => Kit.T0;
        static string With(string extra) { var s = Kit.Status(); return s.Substring(0, s.Length - 2) + "," + extra + "}}"; }
        const string Ota = "\"s5\":{\"ota\":{\"artifact\":\"a\",\"version\":\"2\",\"group\":\"g\",\"maxInFlight\":2,\"failureThreshold\":2,\"scheduleHour\":26,\"rollout\":\"r\",\"createdAt\":\"2026-11-19T12:01:00Z\",\"startedAt\":\"2026-11-19T12:02:00Z\",\"refusalCode\":\"PIN_REQUIRED\"}}";
        const string Job = "\"jobs\":[{\"day\":0,\"name\":\"lamp-service\",\"campaign\":\"\",\"group\":\"\",\"window\":\"w\",\"device\":\"hp-light-east-3\",\"fireAt\":\"\",\"windowOpens\":\"2026-11-19T12:04:45Z\",\"windowCloses\":\"2026-11-19T12:05:30Z\"}]";
        static ClockReading Clock(double h) => new ClockReading(h * 3600, ClockMode.Live, "");
        static HarborAlarm Alarm(string severity, string key = "flood-level", string state = "ACTIVE", int second = 0, bool ack = false) => new HarborAlarm("gauge", key, state, severity, ack, Now.AddSeconds(second), Now.AddSeconds(second));
        static HarborCommand Command(string status, int second, string token = "c") => new HarborCommand("pump", token, "start-pump", status, Now.AddSeconds(second));
        static HarborCommand RecordedSuccess(int second) => new HarborCommand("pump", "c", "start-pump", "SUCCESSFUL", Now.AddSeconds(30), null, null, Now.AddSeconds(second), "bound-pump");

        [Test]
        public void StatusBeatsAreOrderedAndAbsentMeansLayoutFallback()
        {
            var s = ScenarioStatus.Parse(With("\"beats\":[{\"name\":\"storm\",\"from\":16,\"to\":22.5},{\"name\":\"outage\",\"from\":23,\"to\":1}]"));
            Assert.That(s.BeatsFromRunner, Is.True); Assert.That(s.Beats.Select(x => x.Name), Is.EqualTo(new[] { "storm", "outage" }));
            Assert.That(s.Beats[1].From, Is.EqualTo(23)); Assert.That(s.Beats[1].To, Is.EqualTo(1));
            var absent = ScenarioStatus.Parse(Kit.Status());
            Assert.That(absent.BeatsFromRunner, Is.False);
            var empty = ScenarioStatus.Parse(With("\"beats\":[]"));
            Assert.That(StoryCues.At(Kit.Layout(), empty, Clock(19)).Surge, Is.EqualTo(0));
            Assert.That(StoryCues.At(Kit.Layout(), absent, Clock(19)).BeatsNote, Does.Contain("committed layout"));
            Assert.That(StoryCues.At(Kit.Layout(), absent, Clock(19)).Weather.Storm, Is.EqualTo(1));
        }
        [TestCase("\"beats\":null")]
        [TestCase("\"beats\":{}")]
        [TestCase("\"beats\":[{\"name\":\"storm\",\"from\":\"16\",\"to\":22}]")]
        [TestCase("\"jobs\":false")]
        [TestCase("\"jobs\":[{\"day\":\"0\"}]")]
        [TestCase("\"s5\":null")]
        [TestCase("\"s5\":{\"ota\":[]}")]
        [TestCase("\"s5\":{\"replacement\":{\"device\":\"d\",\"at\":false,\"unitIdentifier\":\"u\"}}")]
        public void WrongTypedOptionalMetadataIsRefused(string extra) => Assert.Throws<FormatException>(() => ScenarioStatus.Parse(With(extra)));
        [Test]
        public void JobsAndS5UseTheRealWirePathsAndKeepNoCredentials()
        {
            var s = ScenarioStatus.Parse(With(Job + "," + Ota));
            Assert.That(s.Jobs[0].Device, Is.EqualTo("hp-light-east-3")); Assert.That(s.Jobs[0].Window, Is.EqualTo("w"));
            Assert.That(s.Jobs[0].WindowOpens.HasValue, Is.True); Assert.That(s.Jobs[0].FireAt, Is.Null);
            Assert.That(s.Ota.Rollout, Is.EqualTo("r")); Assert.That(s.Ota.MaxInFlight, Is.EqualTo(2)); Assert.That(s.Ota.RefusalCode, Is.EqualTo("PIN_REQUIRED"));
            var replacement = ScenarioStatus.Parse(With("\"s5\":{\"replacement\":{\"device\":\"hp-gauge-north-3\",\"at\":\"2026-11-19T12:30:00Z\",\"unitIdentifier\":\"unit-2\",\"newCredentialToken\":\"must-not-be-retained\",\"retiredCredentialTokens\":[\"old-secret\"]}}"));
            Assert.That(replacement.Replacement.UnitIdentifier, Is.EqualTo("unit-2"));
            Assert.That(typeof(StoryReplacement).GetProperties().Select(p => p.Name).OrderBy(x => x), Is.EqualTo(new[] { "At", "Device", "UnitIdentifier" }));
            Assert.That(ScenarioStatus.Parse(Kit.Status()).HasS5, Is.False);
            Assert.That(ScenarioStatus.Parse(Kit.Status()).Ota, Is.Null);
        }
        [Test]
        public void WorldInstantsUseVerifierMappingAndOtaIsNextNight()
        {
            var s = ScenarioStatus.Parse(With(Job + "," + Ota));
            var instant = s.ToWallTime(26 * 3600);
            Assert.That((instant - s.StartedAt.Value).TotalSeconds, Is.EqualTo(1200).Within(1e-7));
            Assert.That(s.ToSimSeconds(instant), Is.EqualTo(26 * 3600).Within(1e-5));
            Assert.That(StoryCues.At(Kit.Layout(), s, Clock(2.5)).ChargerOtaNight, Is.False);
            Assert.That(StoryCues.At(Kit.Layout(), s, Clock(26.5)).ChargerOtaNight, Is.True);
            Assert.That(StoryCues.At(Kit.Layout(), s, Clock(27)).ChargerOtaNight, Is.False);
            Assert.That(StoryCues.At(Kit.Layout(), null, Clock(26.5)).OtaNote, Does.Contain("no OTA cue"));
            Assert.That(StoryCues.At(Kit.Layout(), s, new ClockReading(s.ToSimSeconds(s.Jobs[0].WindowOpens.Value), ClockMode.Live, "")).LampCrew, Is.True);
        }
        [Test]
        public void WorldWindowsStayContinuousAcrossMidnightAndFadeInWallSeconds()
        {
            var s = ScenarioStatus.Parse(With("\"beats\":[{\"name\":\"feeder-outage\",\"from\":23,\"to\":1},{\"name\":\"storm\",\"from\":23,\"to\":3}]"));
            var a = StoryCues.At(Kit.Layout(), s, Clock(23.999)); var b = StoryCues.At(Kit.Layout(), s, Clock(24.001));
            Assert.That(a.EastFeederOutage, Is.EqualTo(b.EastFeederOutage).Within(1e-6));
            Assert.That(a.Weather.Storm, Is.EqualTo(b.Weather.Storm).Within(0.001));
            Assert.That(StoryCues.At(Kit.Layout(), s, Clock(23 + 24.0 / 1440)).EastFeederOutage, Is.EqualTo(0.5).Within(1e-8));
            Assert.That(StoryCues.At(Kit.Layout(), s, Clock(1)).EastFeederOutage, Is.EqualTo(0));
        }
        [Test]
        public void EscalationHasOneIdentityAndClearTickExpiresWithoutReappearing()
        {
            var reducer = new AlarmBeacons(); string identity = null;
            var i = 0;
            foreach (var severity in new[] { "MINOR", "MAJOR", "CRITICAL", "MAJOR" })
            {
                var rows = reducer.Update(new[] { Alarm(severity, second: i) }, Now.AddSeconds(i++));
                Assert.That(rows.Count, Is.EqualTo(1));
                if (identity == null) identity = rows[0].Identity;
                Assert.That(rows[0].Identity, Is.EqualTo(identity)); Assert.That(rows[0].Severity, Is.EqualTo(severity));
            }
            var clear = Alarm("MAJOR", state: "CLEARED", second: 4);
            Assert.That(reducer.Update(new[] { clear }, Now.AddSeconds(4))[0].Cleared, Is.True);
            Assert.That(reducer.Update(new[] { clear }, Now.AddSeconds(5.99)).Count, Is.EqualTo(1));
            Assert.That(reducer.Update(new[] { clear }, Now.AddSeconds(6)), Is.Empty);
            Assert.That(reducer.Update(new[] { clear }, Now.AddSeconds(7)), Is.Empty);
        }
        [Test]
        public void TwoAlarmKeysStaySeparateAndAcknowledgementStopsPulsing()
        {
            var rows = new AlarmBeacons().Update(new[] { Alarm("MINOR", ack: true), Alarm("WARNING", "surge-rising") }, Now);
            Assert.That(rows.Count, Is.EqualTo(2)); Assert.That(rows[0].Identity, Is.Not.EqualTo(rows[1].Identity));
            var ack = rows.First(b => b.Acknowledged);
            Assert.That(ack.Pulse(Now), Is.EqualTo(ack.Pulse(Now.AddSeconds(0.2))));
        }
        [TestCase("WARNING", 1.5, 1)] [TestCase("MINOR", 1, 1)] [TestCase("MAJOR", 0.7, 2)] [TestCase("CRITICAL", 0.4, 3)]
        public void BeaconCadenceMatchesDesign(string severity, double period, int rings)
        { Assert.That(AlarmBeacons.Period(severity), Is.EqualTo(period)); Assert.That(AlarmBeacons.RingCount(severity), Is.EqualTo(rings)); }
        [Test]
        public void CommandHistoryNeverInsertsMissedStatesOrChangesOldSnapshots()
        {
            var h = new CommandHistory(); h.Observe(new[] { Command("QUEUED", 0) }); var old = h.Snapshot();
            h.Observe(new[] { Command("SUCCESSFUL", 2) }); h.Observe(new[] { Command("SUCCESSFUL", 3) });
            Assert.That(h.For("c").Select(c => c.Status), Is.EqualTo(new[] { "QUEUED", "SUCCESSFUL" }));
            Assert.That(h.For("c")[1].ObservedAt, Is.EqualTo(Now.AddSeconds(2))); Assert.That(old.Count, Is.EqualTo(1));
            Assert.That(h.For("missing"), Is.Empty);
        }
        [TestCase(true, false, 2.6, PumpStartKind.Platform)]
        [TestCase(false, true, 3.3, PumpStartKind.FloatConsistent)]
        [TestCase(false, false, 3.3, PumpStartKind.Unconfirmed)]
        [TestCase(false, true, 3.1, PumpStartKind.Unconfirmed)]
        public void PumpStartChipRequiresPositiveEvidence(bool success, bool covered, double well, PumpStartKind expected)
        {
            var h = new CommandHistory(); if (success) h.Observe(new[] { RecordedSuccess(1) });
            var e = new PumpStartEvidence(); e.Update("pump", new Reading(0, Now), new Reading(2, Now), h, default);
            e.Update("pump", new Reading(1, Now.AddSeconds(2)), new Reading(well, Now.AddSeconds(2)), h,
                covered ? new CommandCoverage(Now, Now.AddSeconds(2)) : default);
            Assert.That(e.Kind, Is.EqualTo(expected));
        }
        [TestCase(true, PumpStartKind.FloatConsistent)] [TestCase(false, PumpStartKind.Unconfirmed)]
        public void PreviousPumpCycleSuccessCannotEstablishPlatformForTheNextStart(bool covered, PumpStartKind expected)
        {
            var history = new CommandHistory(); var evidence = new PumpStartEvidence();
            evidence.Update("pump", new Reading(0, Now), null, history, default);
            history.Observe(new[] { RecordedSuccess(1) });
            evidence.Update("pump", new Reading(1, Now.AddSeconds(2)), new Reading(2.6, Now.AddSeconds(2)), history, default);
            Assert.That(evidence.Kind, Is.EqualTo(PumpStartKind.Platform));
            evidence.Update("pump", new Reading(0, Now.AddSeconds(5)), null, history, default);
            evidence.Update("pump", new Reading(1, Now.AddSeconds(8)), new Reading(3.3, Now.AddSeconds(8)), history,
                covered ? new CommandCoverage(Now.AddSeconds(5), Now.AddSeconds(8)) : default);
            Assert.That(evidence.Kind, Is.EqualTo(expected));
        }
        [TestCase(5, PumpStartKind.Unconfirmed)] [TestCase(6, PumpStartKind.Platform)] [TestCase(8, PumpStartKind.Platform)] [TestCase(9, PumpStartKind.Unconfirmed)]
        public void PumpResponseMustFollowOffAndPrecedeOrEqualRunning(int successSecond, PumpStartKind expected)
        {
            var history = new CommandHistory(); var evidence = new PumpStartEvidence();
            evidence.Update("pump", new Reading(1, Now), null, history, default);
            evidence.Update("pump", new Reading(0, Now.AddSeconds(5)), null, history, default);
            history.Observe(new[] { RecordedSuccess(successSecond) });
            evidence.Update("pump", new Reading(0, Now.AddSeconds(7)), null, history, default);
            evidence.Update("pump", new Reading(1, Now.AddSeconds(8)), null, history, default);
            Assert.That(evidence.Kind, Is.EqualTo(expected));
        }
        [Test]
        public void LateCommandSuccessAndMissingOffSampleStayUnconfirmed()
        {
            var h = new CommandHistory(); h.Observe(new[] { Command("SUCCESSFUL", 3) });
            var e = new PumpStartEvidence(); e.Update("pump", new Reading(0, Now), null, h, default);
            e.Update("pump", new Reading(1, Now.AddSeconds(2)), new Reading(3.3, Now), h, default);
            Assert.That(e.Kind, Is.EqualTo(PumpStartKind.Unconfirmed));
            var unseen = new PumpStartEvidence(); unseen.Update("pump", new Reading(1, Now.AddSeconds(4)), new Reading(3.3, Now), h, new CommandCoverage(Now, Now.AddSeconds(4)));
            Assert.That(unseen.Kind, Is.EqualTo(PumpStartKind.Unconfirmed));
        }
        [TestCase(18, 0)] [TestCase(70, 1)] [TestCase(96, 1.5)] [TestCase(120, 1.5)]
        public void ChargerGlowMarksTheRuleLineAndContinuesAbove(double c, double expected) => Assert.That(ChargerBoard.GlowFor(c), Is.EqualTo(expected));
        static string RolloutJson(string outcome, string attempt = "null") => "{\"updateRollout\":{\"token\":\"r\",\"state\":\"PAUSED\",\"pauseReason\":\"FAILURE_THRESHOLD\",\"maxInFlight\":2,\"failureThreshold\":2,\"progress\":{\"total\":1,\"pending\":0,\"inFlight\":0,\"succeeded\":0,\"failed\":1,\"unknown\":0,\"cancelled\":0,\"skipped\":0}},\"updateRolloutTargets\":{\"results\":[{\"deviceToken\":\"charger\",\"outcome\":\"" + outcome + "\",\"attemptState\":" + attempt + ",\"skipCode\":null}],\"pagination\":{\"totalRecords\":1}}}";
        [TestCase("PENDING")] [TestCase("IN_FLIGHT")] [TestCase("SUCCEEDED")] [TestCase("FAILED")] [TestCase("UNKNOWN")] [TestCase("CANCELLED")] [TestCase("SKIPPED")]
        public void OtaOutcomesKeepThePlatformsWords(string outcome)
        {
            var r = RolloutEvidence.Parse(RolloutJson(outcome, "\"TIMED_OUT\""), Now);
            Assert.That(r.Target("charger").Outcome, Is.EqualTo(outcome)); Assert.That(r.Target("charger").AttemptState, Is.EqualTo("TIMED_OUT"));
            Assert.That(r.Label, Is.EqualTo("PAUSED(FAILURE_THRESHOLD)")); Assert.That(r.Progress["failed"], Is.EqualTo(1)); Assert.That(r.ObservedAt, Is.EqualTo(Now));
        }
        [Test]
        public void OtaWrongTypesAreRefusedAndNullStaysAbsent()
        {
            Assert.That(RolloutEvidence.Parse("{\"updateRollout\":null}", Now), Is.Null);
            Assert.Throws<FormatException>(() => RolloutEvidence.Parse(RolloutJson("FAILED").Replace("\"maxInFlight\":2", "\"maxInFlight\":\"2\""), Now));
            Assert.Throws<FormatException>(() => RolloutEvidence.Parse(RolloutJson("MADE_UP"), Now));
        }
        [Test]
        public void TruckRealSimulatorFloodPlainCoordinateFitsTheFrameAndAges()
        {
            var layout = Kit.Layout();
            var plan = SitePlan.Build(layout);
            // harborTruckFix at 18:00 is the route's last waypoint, the middle of the flood plain behind East Quay.
            var plain = layout.TruckRoute[layout.TruckRoute.Count - 1];
            var fix = new HarborLocation(plain.Lat, plain.Lon, 180, 8, Now);
            var t = TruckTrack.From(plan, fix, false, Now);
            // the plain and the depot are on land: south of the quay wall, the town's side (the water is north of it)
            Assert.That(t.At.Z, Is.EqualTo(plan.Frame.ToGround(plain).Z).Within(1e-8)); Assert.That(t.At.Z, Is.LessThan(plan.WallZ - 30));
            Assert.That(plan.Depot.Z, Is.LessThan(t.At.Z));
            Assert.That(t.At.X, Is.InRange(plan.MinX, plan.MaxX)); Assert.That(t.At.Z, Is.InRange(plan.MinZ, plan.MaxZ)); Assert.That(t.LightBar, Is.True);
            Assert.That(TruckTrack.From(plan, fix, false, Now.AddSeconds(11)).Stale, Is.True);
            var no = TruckTrack.From(plan, null, false, Now); Assert.That(no.Note, Is.EqualTo("no fix")); Assert.That(no.At.X, Is.EqualTo(plan.Depot.X));
            Assert.That(TruckTrack.From(plan, new HarborLocation(null, null, 0, 0, Now), false, Now).NoFix, Is.True);
            Assert.That(TruckTrack.From(plan, new HarborLocation(plain.Lat, plain.Lon, 0, 0, Now), true, Now).LightBar, Is.True);
        }
        [Test]
        public void ReplacementCountsOnlyDistinctObservedReadingsAfterTheDeclaredInstant()
        {
            var source = new HarborTestSource { Status = ScenarioStatus.Parse(With("\"s5\":{\"replacement\":{\"device\":\"hp-gauge-north-3\",\"at\":\"2026-11-19T12:30:00Z\",\"unitIdentifier\":\"unit-2\"}}")) };
            var at = source.Status.Replacement.At; var model = new HarborObservedModel(Kit.Layout()); var id = "PAW-GAUGE-NORTH-3";
            source.Set(id, Metrics.WaterLevel, 1, at); var before = model.Update(source, Clock(12), at);
            Assert.That(before.Replacement.ObservedReadingsAfter, Is.EqualTo(0));
            source.Set(id, Metrics.WaterLevel, 2, at.AddSeconds(1)); var after = model.Update(source, Clock(13), at.AddSeconds(3));
            Assert.That(after.Replacement.ObservedReadingsAfter, Is.EqualTo(1)); Assert.That(after.Replacement.NewestAge, Is.EqualTo(TimeSpan.FromSeconds(2)));
            Assert.That(model.Update(source, Clock(13), at.AddSeconds(4)).Replacement.ObservedReadingsAfter, Is.EqualTo(1));
            Assert.That(before.Replacement.ObservedReadingsAfter, Is.EqualTo(0));
        }
        static string WindowJson => "{\"alarmSuppressionWindowsByToken\":[{\"token\":\"w\",\"kind\":\"ONE_SHOT\",\"status\":\"ACTIVE\",\"startTime\":\"2026-11-19T12:00:00Z\",\"endTime\":\"2026-11-19T13:00:00Z\",\"endedAt\":null,\"targets\":[{\"type\":\"DEVICE\",\"token\":\"lamp\"}],\"alarmKeys\":[\"lamp-flicker\"],\"bypassSeverity\":null}]}";
        [TestCase(false, false)] [TestCase(false, true)] [TestCase(true, false)] [TestCase(true, true)]
        public void LampServiceSeparatesPagingWindowFromObservedAlarmState(bool snapshotComplete, bool streamLive)
        {
            var source = new HarborTestSource { Status = ScenarioStatus.Parse(With(Job)), AlarmObservation = new AlarmCoverage(snapshotComplete, streamLive) };
            var id = Kit.Layout().Devices.First(d => d.Token == "hp-light-east-3").ExternalId;
            var windows = SuppressionEvidence.Parse(WindowJson.Replace("\"lamp\"", "\"bound-" + id + "\""), Now);
            source.Suppressions = windows;
            var model = new HarborObservedModel(Kit.Layout());
            var absent = model.Update(source, Clock(11), Now).LampService;
            Assert.That(absent.WindowNote, Is.EqualTo("platform suppression window ACTIVE (holds paging for lamp-flicker)"));
            Assert.That(absent.AlarmNote, Is.EqualTo(snapshotComplete && streamLive ? "no lamp-flicker alarm" : "alarm state unknown (alarms not observed)"));
            source.Alarms = new[] { new HarborAlarm(id, "lamp-flicker", "ACTIVE", "MINOR", false, Now, Now) };
            var active = model.Update(source, Clock(11), Now).LampService;
            Assert.That(active.AlarmNote, Is.EqualTo(snapshotComplete && streamLive ? "lamp-flicker active MINOR" : "alarm state unknown (alarms not observed)"));
            Assert.That(active.WindowNote, Is.EqualTo(absent.WindowNote));
            source.Suppressions = Array.Empty<SuppressionEvidence>();
            var noWindow = model.Update(source, Clock(11), Now).LampService;
            Assert.That(noWindow.WindowOpen, Is.Null);
            Assert.That(noWindow.WindowNote, Is.EqualTo("platform suppression window not reported (holds paging for lamp-flicker)"));
            Assert.That(noWindow.AlarmNote, Is.EqualTo(active.AlarmNote));
            Assert.That(LampService.From(source.Status.Jobs[0], null, windows, new AlarmCoverage(true, true), null, Now).AlarmNote, Is.EqualTo("alarm state unknown (alarms not observed)"));
            Assert.That(LampService.From(source.Status.Jobs[0], "wrong-device", windows, source.AlarmObservation, null, Now).WindowOpen, Is.Null);
            Assert.That(LampService.From(source.Status.Jobs[0], "bound-" + id, windows, source.AlarmObservation, null, Now.AddHours(2)).WindowOpen, Is.False);
        }
        [Test]
        public void LampServiceReturnsToUnknownWhenAlarmStreamCoverageIsLost()
        {
            var source = new HarborTestSource { Status = ScenarioStatus.Parse(With(Job)), AlarmObservation = new AlarmCoverage(true, true) };
            var id = Kit.Layout().Devices.First(d => d.Token == "hp-light-east-3").ExternalId;
            source.Alarms = new[] { new HarborAlarm(id, "lamp-flicker", "ACTIVE", "MAJOR", false, Now, Now) };
            var model = new HarborObservedModel(Kit.Layout());
            var old = model.Update(source, Clock(11), Now).LampService;
            Assert.That(old.AlarmNote, Is.EqualTo("lamp-flicker active MAJOR"));
            source.AlarmObservation = new AlarmCoverage(true, false);
            Assert.That(model.Update(source, Clock(11), Now).LampService.AlarmNote, Is.EqualTo("alarm state unknown (alarms not observed)"));
            Assert.That(old.AlarmNote, Is.EqualTo("lamp-flicker active MAJOR"));
        }
        [Test]
        public void PlatformWellAttributeIsSeparateFromTheFixedAlarmMarker()
        {
            var data = "{\"entityAttributes\":{\"results\":[{\"entityType\":\"device\",\"scope\":\"SHARED\",\"attrKey\":\"well_alarm_m\",\"valueType\":\"DOUBLE\",\"value\":\"1.8\",\"lastUpdated\":\"2026-11-19T12:00:00Z\"}]}}";
            Assert.That(AttributeEvidence.ParseWellAlarm(data).Value.Value, Is.EqualTo(1.8)); Assert.That(PumpPresentation.HighMarkerM, Is.EqualTo(2.5));
            Assert.Throws<FormatException>(() => AttributeEvidence.ParseWellAlarm(data.Replace("\"1.8\"", "1.8")));
        }
        [Test]
        public void PreviewIsDeterministicAndAllU1KindsHaveInputs()
        {
            var a = new PreviewSource(Kit.Layout()); var b = new PreviewSource(Kit.Layout());
            foreach (var hour in new[] { 10.9, 12.6, 14.5, 17.78, 17.82, 17.9, 18.6, 19.5, 20.8, 22.5, 26.1, 27.0 })
            {
                a.SetTime(hour * 3600, Now); b.SetTime(hour * 3600, Now);
                var fa = new HarborObservedModel(Kit.Layout()).Update(a, new ClockReading(hour * 3600, ClockMode.Preview, "not platform data"), Now);
                var fb = new HarborObservedModel(Kit.Layout()).Update(b, new ClockReading(hour * 3600, ClockMode.Preview, "not platform data"), Now);
                Assert.That(fa.Beacons.Select(x => x.Identity + x.Severity), Is.EqualTo(fb.Beacons.Select(x => x.Identity + x.Severity)));
                foreach (var d in Kit.Layout().Devices.Where(x => x.Kind == DeviceKind.Pump || x.Kind == DeviceKind.Charger || x.Kind == DeviceKind.Meter || x.Kind == DeviceKind.Truck))
                    foreach (var metric in HarborObservedModel.MetricsFor(d.Kind)) { Assert.That(a.TryGet(d.ExternalId, metric, out var va), Is.True); b.TryGet(d.ExternalId, metric, out var vb); Assert.That(va.Value, Is.EqualTo(vb.Value)); }
                Assert.That(fa.Truck.NoFix, Is.False);
            }
            a.SetTime(26.1 * 3600, Now); Assert.That(a.Rollout.Progress["inFlight"], Is.EqualTo(2));
            a.SetTime(27 * 3600, Now); Assert.That(a.Rollout.Target("hp-charger-north-1").Outcome, Is.EqualTo("FAILED"));
            a.SetTime(10.9 * 3600, Now); Assert.That(a.Alarms.Any(x => x.Key == "lamp-flicker" && x.Active), Is.False);
        }
        [Test]
        public void PreviewLampAlarmRemainsActiveDuringThePagingHold()
        {
            var source = new PreviewSource(Kit.Layout()); source.SetTime(11.25 * 3600, Now);
            var service = new HarborObservedModel(Kit.Layout()).Update(source, new ClockReading(11.25 * 3600, ClockMode.Preview, "not platform data"), Now).LampService;
            Assert.That(service.WindowOpen, Is.True);
            Assert.That(service.AlarmNote, Is.EqualTo("lamp-flicker active MAJOR"));
        }
        [Test]
        public void PreviewObservationProducesPlatformAndFloatChipsWithoutInventingCommandStates()
        {
            var layout = Kit.Layout(); var source = new PreviewSource(layout); var model = new HarborObservedModel(layout);
            HarborFrame frame = null;
            foreach (var hour in new[] { 17.7, 17.76, 17.81, 17.9, 17.99, 18.01, 18.49, 18.51 })
            {
                var at = Now.AddSeconds((hour - 16) * 60);
                source.SetTime(hour * 3600, at);
                frame = model.Update(source, new ClockReading(hour * 3600, ClockMode.Preview, "not platform data"), at);
            }
            var north = layout.Devices.First(d => d.Kind == DeviceKind.Pump && d.Quay == "hp-north-quay");
            var basin = layout.Devices.First(d => d.Kind == DeviceKind.Pump && d.Quay == "hp-basin");
            Assert.That(frame.Pumps[north.ExternalId].StartKind, Is.EqualTo(PumpStartKind.FloatConsistent));
            Assert.That(frame.Pumps[basin.ExternalId].StartKind, Is.EqualTo(PumpStartKind.Platform));
            Assert.That(frame.Pumps[basin.ExternalId].ImpellerSpeed, Is.EqualTo(100.0 / 120).Within(1e-8));
            Assert.That(frame.Commands.Where(c => c.Device == basin.ExternalId).Select(c => c.Status), Is.EqualTo(new[] { "QUEUED", "SENT", "SUCCESSFUL" }));
        }
        [Test]
        public void ChargerFreshnessUsesTheFortyFiveSecondSilenceWindowAndMissingStaysMissing()
        {
            var src = new HarborTestSource(); var id = Kit.Layout().Devices.First(d => d.Kind == DeviceKind.Charger).ExternalId;
            var model = new HarborObservedModel(Kit.Layout());
            Assert.That(model.Update(src, Clock(14), Now).Chargers[id].NoReading, Is.True);
            src.Set(id, Metrics.ChargerPower, 11, Now);
            Assert.That(model.Update(src, Clock(14), Now.AddSeconds(45)).Chargers[id].Stale, Is.False);
            Assert.That(model.Update(src, Clock(14), Now.AddSeconds(45.1)).Chargers[id].Stale, Is.True);
        }
        [TestCase(-1, 0, false, false)] [TestCase(0, 0, true, false)]
        [TestCase(45, 0, true, false)] [TestCase(45.1, 0, false, false)]
        [TestCase(0, 1, true, true)] [TestCase(45, 1, true, true)] [TestCase(45.1, 1, false, false)]
        public void ChargerConfirmationNeedsFreshSessionAlongsidePower(double sessionAge, double active, bool confirmed, bool showCar)
        {
            var src = new HarborTestSource(); var id = Kit.Layout().Devices.First(d => d.Kind == DeviceKind.Charger).ExternalId;
            src.Set(id, Metrics.ChargerPower, 11, Now);
            if (sessionAge >= 0) src.Set(id, Metrics.SessionActive, active, Now.AddSeconds(-sessionAge));
            var model = new HarborObservedModel(Kit.Layout()); var board = model.Update(src, Clock(14), Now).Chargers[id];
            Assert.That(board.Confirmed, Is.EqualTo(confirmed)); Assert.That(board.ShowCarAndCable, Is.EqualTo(showCar));
            Assert.That(model.Update(src, Clock(14), Now.AddSeconds(46)).Chargers[id].ShowCarAndCable, Is.False);
            src.Values.Remove(id + Metrics.ChargerPower);
            Assert.That(model.Update(src, Clock(14), Now).Chargers[id].Confirmed, Is.False);
        }
        [Test]
        public void EastLanternWorldFadeDoesNotChangeFreshObservedLampState()
        {
            var src = new HarborTestSource(); var layout = Kit.Layout();
            var id = layout.Devices.First(d => d.Kind == DeviceKind.Light && d.Quay == "hp-east-quay").ExternalId;
            src.Set(id, Metrics.LampOn, 1, Now); src.Set(id, Metrics.Dim, 80, Now);
            var model = new HarborObservedModel(layout);
            foreach (var hour in new[] { 19.75, 19.75 + 24.0 / 1440, 20.0, 21.25 })
            {
                var frame = model.Update(src, Clock(hour), Now);
                var expected = hour == 20 ? 0 : hour == 19.75 || hour == 21.25 ? 1 : 0.5;
                Assert.That(frame.World.FeederPower("hp-east-quay"), Is.EqualTo(expected).Within(1e-8));
                Assert.That(frame.World.FeederPower("hp-north-quay"), Is.EqualTo(1));
                Assert.That(frame.Lamps[id].Condition, Is.EqualTo(LampCondition.On));
                Assert.That(frame.Lamps[id].DimPct, Is.EqualTo(80));
            }
        }
        [Test]
        public void CachedMetricListsAreSharedAcrossFramesAndGaugeKinds()
        {
            foreach (var kind in DeviceKind.All)
                Assert.That(ReferenceEquals(HarborObservedModel.MetricsFor(kind), HarborObservedModel.MetricsFor(kind)), Is.True);
            Assert.That(ReferenceEquals(HarborObservedModel.MetricsFor(DeviceKind.Gauge), HarborObservedModel.MetricsFor(DeviceKind.LoraGauge)), Is.True);
            Assert.That(ReferenceEquals(HarborObservedModel.MetricsFor(DeviceKind.Gauge), HarborObservedModel.MetricsFor(DeviceKind.LegacyGauge)), Is.True);
            Assert.That(HarborObservedModel.MetricsFor(DeviceKind.Pump), Is.EqualTo(new[] { Metrics.WetWell, Metrics.PumpRunning, Metrics.Flow, Metrics.PumpCurrent }));
        }
        [Test]
        public void WellFootprintsExcludeOnlyTheWellsAtEveryPump()
        {
            var plan = SitePlan.Build(Kit.Layout()); var count = 0;
            foreach (var p in plan.Placements.Where(p => p.Device.Kind == DeviceKind.Pump))
            {
                count++; var disc = new WellFootprint(p.At);
                Assert.That(disc.Center.X, Is.EqualTo(p.At.X + 9)); Assert.That(disc.Center.Z, Is.EqualTo(p.At.Z + 6));
                Assert.That(disc.Contains(disc.Center), Is.True);
                Assert.That(disc.Contains(new Ground(disc.Center.X + 2.44, disc.Center.Z)), Is.True);
                Assert.That(disc.Contains(new Ground(disc.Center.X, disc.Center.Z - 2.44)), Is.True);
                Assert.That(disc.Contains(new Ground(disc.Center.X + 2.46, disc.Center.Z)), Is.False);
                Assert.That(disc.Contains(new Ground(disc.Center.X, plan.WallZ + 260)), Is.False);
                Assert.That(disc.Contains(new Ground(disc.Center.X + 9000, disc.Center.Z)), Is.False);
            }
            Assert.That(count, Is.EqualTo(3));
        }
        [Test]
        public void GaugeCrewPropsClearTheInlandCopingEdgeByAtLeastTwoAndAHalfMetres()
        {
            var plan = SitePlan.Build(Kit.Layout());
            foreach (var p in plan.Placements.Where(p => DeviceKind.IsGauge(p.Device.Kind)))
            {
                var crew = SitePlan.GaugeCrewAt(p.At, plan.WallZ);
                // Furthest bounds: cone base z=3.275, van z=2.3, barrier z=1.5.
                foreach (var front in new[] { 3.275, 2.3, 1.5 })
                    Assert.That(plan.WallZ - 1.8 - (crew.Z + front), Is.GreaterThanOrEqualTo(2.5 - 1e-8));
            }
        }
        [Test]
        public void PreviewOutageAndCrewWindowsFollowTheDeclaredStory()
        {
            var layout = Kit.Layout(); var source = new PreviewSource(layout);
            source.SetTime(20.4 * 3600, Now);
            Assert.That(source.Alarms.Any(a => a.Key == "light-silent" && a.Active), Is.False);
            source.SetTime(20.6 * 3600, Now);
            Assert.That(source.Alarms.Any(a => a.Key == "charger-offline" && a.Active), Is.True);
            source.SetTime(12.6 * 3600, Now);
            Assert.That(StoryCues.At(layout, source.Status, Clock(12.6)).ReplacementCrew, Is.True);
            source.SetTime(13.1 * 3600, Now);
            Assert.That(StoryCues.At(layout, source.Status, Clock(13.1)).ReplacementCrew, Is.False);
        }

        [Test]
        public void FrameCopiesRemainStableAndEscalationsCountOnce()
        {
            var src = new HarborTestSource(); var id = "PAW-GAUGE-EAST-1"; src.Set(id, Metrics.WaterLevel, 1, Now);
            src.Alarms = new[] { new HarborAlarm(id, "flood-level", "ACTIVE", "MINOR", false, Now, Now) };
            var m = new HarborObservedModel(Kit.Layout()); var first = m.Update(src, Clock(19), Now);
            src.Set(id, Metrics.WaterLevel, 2, Now.AddSeconds(1)); src.Alarms = new[] { new HarborAlarm(id, "flood-level", "ACTIVE", "CRITICAL", false, Now.AddSeconds(1), Now.AddSeconds(1)) };
            var second = m.Update(src, Clock(19), Now.AddSeconds(1));
            Assert.That(second.AlarmCounts[id], Is.EqualTo(1)); Assert.That(first.Beacons[0].Severity, Is.EqualTo("MINOR")); Assert.That(first.Tides["hp-east-quay"].LevelM, Is.EqualTo(1));
        }
        [TestCase("mutation { sendCommand { token } }")]
        [TestCase("query { x } mutation Bad { y }")]
        [TestCase("# hi\n mutation\nBad { y }")]
        public void MutationGuardRejectsTopLevelWrites(string query) => Assert.Throws<InvalidOperationException>(() => HarborReadQueries.RequireReadOnly(query));
        [Test]
        public void EveryDocumentSentByLiveIsARead()
        { foreach (var query in new[] { HarborReadQueries.Rollout, HarborReadQueries.Suppressions, HarborReadQueries.Attributes, HarborReadQueries.PumpCommandsOne, HarborReadQueries.PumpCommandsTwo, HarborReadQueries.PumpCommands }) Assert.DoesNotThrow(() => HarborReadQueries.RequireReadOnly(query)); }
    }
}
