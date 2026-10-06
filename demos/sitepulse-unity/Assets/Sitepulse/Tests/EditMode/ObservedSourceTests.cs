// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sitepulse.App;
using DeviceChain.Sitepulse.DevicePlane;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Visuals;
using NUnit.Framework;
using UnityEngine;

namespace DeviceChain.Sitepulse.Tests
{
    public sealed class ObservedSourceTests
    {
        static readonly DateTimeOffset T0 = new DateTimeOffset(2026, 10, 6, 14, 2, 11, TimeSpan.Zero);
        const string Dev = "sp-hauler-06";

        static ObservedReadingSource Source(ObservedState st, bool live = true) =>
            new ObservedReadingSource(st, id => id == "SP-HL-0006" ? Dev : id == "SP-PL-0001" ? "sp-plant-01" : null, () => live);

        static DeviceReading Truck() => new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment, Provenance.Observed);

        static void Fill(ObservedReadingSource src, DeviceReading r, string id = "SP-HL-0006") =>
            src.Fill(new ReadingSubject(id, null, 0f), r, T0);

        [Test]
        public void ADeviceTheObserverHasNotSeenFillsNothing()
        {
            var r = Truck();
            Fill(Source(new ObservedState(T0)), r);
            foreach (var key in MeasurementKeys.Equipment) Assert.IsFalse(r.TryGet(key, out _), key);
            Assert.IsNull(r.SpeedKmh);
            Assert.IsFalse(r.HasAlarm);
            Assert.IsNull(r.CommandStatus);
            // and a device with no platform token (it did not bind) is the same: nothing is invented
            var unbound = new DeviceReading("SP-DZ-0001", DeviceReading.Profile.Equipment, Provenance.Observed);
            Fill(Source(new ObservedState(T0)), unbound, "SP-DZ-0001");
            Assert.IsFalse(unbound.TryGet(MeasurementKeys.FuelPct, out _));
        }

        [Test]
        public void ACardTakesTheValuesTheStateHoldsAndTheirTimes()
        {
            var st = new ObservedState(T0);
            st.ApplyMeasurement(Dev, "fuel_pct", 72.95, T0, T0.AddMilliseconds(80), false);
            st.ApplyMeasurement(Dev, "payload_t", 86.4, T0.AddSeconds(-1), T0.AddMilliseconds(80), false);
            st.ApplyMeasurement(Dev, "haul_cycle_s", 220, T0, T0, false);   // not a Sitepulse key: not shown
            var r = Truck();
            Fill(Source(st), r);
            Assert.IsTrue(r.TryGet("fuel_pct", out var fuel));
            Assert.AreEqual(72.95, fuel);
            Assert.AreEqual("73 %", r.Format("fuel_pct"));
            Assert.IsTrue(r.TryGetStamp("fuel_pct", out var s));
            Assert.AreEqual(T0, s.OccurredAt);
            Assert.AreEqual(T0.AddMilliseconds(80), s.ObservedAt);
            Assert.AreEqual(T0, r.NewestOccurredAt);
            Assert.AreEqual(Provenance.Observed, r.Provenance);
            Assert.IsFalse(r.TryGet("engine_temp_c", out _), "a key the platform has not reported stays empty");
        }

        [Test]
        public void SpeedIsTheLocationEventsMetresPerSecondShownInKilometresPerHour()
        {
            var st = new ObservedState(T0);
            st.ApplyLocation(Dev, new ObservedLocation { SpeedMps = 5.2397, OccurredAt = T0, ObservedAt = T0.AddMilliseconds(300) });
            var r = Truck();
            Fill(Source(st), r);
            Assert.AreEqual(5.2397 * 3.6, r.SpeedKmh.Value, 1e-9);
            Assert.AreEqual(T0, r.SpeedStamp.Value.OccurredAt);
            st.ApplyLocation(Dev, new ObservedLocation { SpeedMps = null, OccurredAt = T0.AddSeconds(1), ObservedAt = T0.AddSeconds(1) });
            var again = Truck();
            Fill(Source(st), again);
            Assert.IsNull(again.SpeedKmh, "a location with no speed does not become a speed of zero");
        }

        [Test]
        public void TheCardTakesTheActiveAlarmsWithTheirSeverityAndNotTheClearedOnes()
        {
            var st = new ObservedState(T0);
            st.ApplyAlarm(Dev, new ObservedAlarm { Token = "a1", AlarmKey = "low-fuel", State = "ACTIVE", Severity = "CRITICAL", OccurredAt = T0, ObservedAt = T0 });
            st.ApplyAlarm(Dev, new ObservedAlarm { Token = "a2", AlarmKey = "engine-overheat", State = "CLEARED", Severity = "WARNING", OccurredAt = T0, ObservedAt = T0 });
            st.ApplyAlarm(Dev, new ObservedAlarm { Token = "a3", AlarmKey = "brake-wear", State = "ACTIVE", Severity = "WARNING", OccurredAt = T0, ObservedAt = T0 });
            var r = Truck();
            Fill(Source(st), r);
            Assert.AreEqual(1, r.Alarms.Count);
            Assert.AreEqual("low-fuel", r.FirstAlarm.Key);
            Assert.AreEqual("CRITICAL", r.FirstAlarm.Severity);
            Assert.AreEqual("ACTIVE", r.FirstAlarm.State);
            // the alarm is read afresh each time: a cleared one leaves the card
            st.ApplyAlarm(Dev, new ObservedAlarm { Token = "a1", AlarmKey = "low-fuel", State = "CLEARED", Severity = "CRITICAL", OccurredAt = T0.AddSeconds(5), ObservedAt = T0 });
            Fill(Source(st), r);
            Assert.IsFalse(r.HasAlarm);
        }

        [Test]
        public void ACardShowsThePlatformsCommandStateWithoutCollapsingIt()
        {
            var st = new ObservedState(T0);
            var src = Source(st);
            var r = Truck();
            foreach (var raw in new[] { "QUEUED", "HELD", "SENT", "PARKED", "SUCCESSFUL", "FAILED", "TIMEOUT", "EXPIRED", "CANCELLED", "BOGUS" })
            {
                st.ApplyCommand(Dev, new ObservedCommand { Token = "c-" + raw, Name = "goto-refuel", Status = raw, QueuedAt = T0.AddSeconds(1 + Array.IndexOf(new[] { "QUEUED", "HELD", "SENT", "PARKED", "SUCCESSFUL", "FAILED", "TIMEOUT", "EXPIRED", "CANCELLED", "BOGUS" }, raw)), ObservedAt = T0 });
                Fill(src, r);
                Assert.AreEqual(raw, r.CommandStatus.Value.Label, raw);
                Assert.AreEqual("goto-refuel", r.Command);
            }
        }

        [Test]
        public void ThePlantsRunningFlagIsABooleanFromTheValue()
        {
            var st = new ObservedState(T0);
            st.ApplyMeasurement("sp-plant-01", "throughput_tph", 720, T0, T0, false);
            st.ApplyMeasurement("sp-plant-01", "plant_running", 0, T0, T0, false);
            var r = new DeviceReading("SP-PL-0001", DeviceReading.Profile.Plant, Provenance.Observed);
            Fill(Source(st), r, "SP-PL-0001");
            Assert.AreEqual("720 t/h", r.Format("throughput_tph"));
            Assert.IsTrue(r.TryGetFlag("plant_running", out var running));
            Assert.IsFalse(running);
            Assert.AreEqual("No", r.Format("plant_running"));
        }

        [Test]
        public void TheSourcesAreWhatTheyClaimToBeAndAnObservedReadingRefusesTheOther()
        {
            var observed = Source(new ObservedState(T0));
            Assert.AreEqual(Provenance.Observed, observed.Provenance);
            Assert.IsTrue(observed.StreamLive);
            Assert.IsFalse(Source(new ObservedState(T0), live: false).StreamLive);
            Assert.AreEqual(Provenance.Illustrative, new IllustrativeReadingSource(null, "SP-HL-0006", AlarmKeys.LowFuel).Provenance);

            // the illustrative source, handed a Live card, is refused at its first value: no illustrative number reaches it
            var go = new GameObject("SP-HL-0006");
            try
            {
                var rig = go.AddComponent<MachineRig>();
                var live = Truck();
                Assert.Throws<InvalidOperationException>(() =>
                    new IllustrativeReadingSource(null, "SP-HL-0006", AlarmKeys.LowFuel).Fill(new ReadingSubject("SP-HL-0006", rig, 3f), live, T0));
                foreach (var key in MeasurementKeys.Equipment) Assert.IsFalse(live.TryGet(key, out _), key);
                Assert.IsNull(live.SpeedKmh);
            }
            finally
            {
                UnityEngine.Object.DestroyImmediate(go);
            }
        }

        [Test]
        public void TheIllustrativeSourceNeverShowsARefuelledTankBesideAnActiveLowFuelAlarm()
        {
            var go = new GameObject("SP-HL-0006");
            try
            {
                var rig = go.AddComponent<MachineRig>();
                var src = new IllustrativeReadingSource(null, "SP-HL-0006", AlarmKeys.LowFuel);
                foreach (var speed in new[] { 0f, 0.1f, 8f })
                {
                    var r = new DeviceReading("SP-HL-0006", DeviceReading.Profile.Equipment);
                    src.Fill(new ReadingSubject("SP-HL-0006", rig, speed), r, T0);
                    Assert.IsTrue(r.HasAlarm);
                    Assert.AreEqual(CommandState.Sent, r.CommandStatus.Value.State, "speed " + speed);
                    Assert.IsTrue(r.TryGet(MeasurementKeys.FuelPct, out var fuel));
                    Assert.AreEqual(11, fuel);
                }
            }
            finally
            {
                UnityEngine.Object.DestroyImmediate(go);
            }
        }

        // ---- the observer's wire shapes

        const string FrameMeasurement = "{\"measurementStream\":{\"deviceToken\":\"sp-hauler-06\",\"name\":\"fuel_pct\",\"value\":72.95,\"occurredTime\":\"2026-10-06T03:31:00.010649Z\"}}";

        [Test]
        public void AMeasurementFrameParsesAndAnIncompleteOneSaysNothing()
        {
            var seen = T0;
            var m = ObserverQueries.ParseMeasurementFrame(JsonDocument.Parse(FrameMeasurement).RootElement, seen);
            Assert.AreEqual("sp-hauler-06", m.DeviceToken);
            Assert.AreEqual("fuel_pct", m.Name);
            Assert.AreEqual(72.95, m.Value);
            Assert.AreEqual(new DateTimeOffset(2026, 10, 6, 3, 31, 0, TimeSpan.Zero).AddTicks(106490), m.OccurredAt);
            Assert.AreEqual(seen, m.ObservedAt);
            Assert.IsFalse(m.FromSnapshot);
            foreach (var bad in new[]
            {
                "{\"measurementStream\":{\"deviceToken\":\"d\",\"name\":\"n\",\"value\":null,\"occurredTime\":\"2026-10-06T03:31:00Z\"}}",
                "{\"measurementStream\":{\"deviceToken\":\"d\",\"name\":\"n\",\"value\":1,\"occurredTime\":null}}",
                "{\"measurementStream\":{\"name\":\"n\",\"value\":1,\"occurredTime\":\"2026-10-06T03:31:00Z\"}}",
                "{\"other\":{}}",
            })
                Assert.IsNull(ObserverQueries.ParseMeasurementFrame(JsonDocument.Parse(bad).RootElement, seen), bad);
        }

        [Test]
        public void TheSnapshotAnswerIsReadPerDeviceAndMarkedAsASnapshot()
        {
            var data = "{\"d0\":[{\"name\":\"fuel_pct\",\"value\":72.95,\"occurredTime\":\"2026-10-06T03:31:00.010649Z\"},{\"name\":\"engine_hours\",\"value\":null,\"occurredTime\":\"2026-10-06T03:31:00Z\"}],\"d1\":[{\"name\":\"throughput_tph\",\"value\":720,\"occurredTime\":\"2026-10-06T03:31:00Z\"}]}";
            var items = ObserverQueries.ParseMeasurementsSnapshot(new[] { "sp-hauler-06", "sp-plant-01" }, data, T0);
            Assert.AreEqual(2, items.Count, "a row with no value is skipped");
            Assert.IsTrue(items.All(i => i.FromSnapshot));
            Assert.AreEqual("sp-plant-01", items[1].DeviceToken);
            StringAssert.Contains("d1: latestMeasurements(deviceToken: $t1)", ObserverQueries.MeasurementsSnapshotQuery(new[] { "a", "b" }));
        }

        [Test]
        public void ACommandIsNamedByNameAndKeepsTheStatusTextItCameWith()
        {
            var data = "{\"commands\":{\"results\":[{\"token\":\"c1\",\"deviceToken\":\"sp-hauler-06\",\"name\":\"goto-refuel\",\"status\":\"QUEUED\",\"queuedTime\":\"2026-10-06T03:31:00Z\"}]}," +
                       "\"commandsByToken\":[{\"token\":\"c1\",\"deviceToken\":\"sp-hauler-06\",\"name\":\"goto-refuel\",\"status\":\"CANCELLED\",\"queuedTime\":\"2026-10-06T03:31:00Z\"},{\"token\":\"c0\",\"deviceToken\":\"sp-hauler-06\",\"name\":\"goto-area\",\"status\":\"SUCCESSFUL\",\"queuedTime\":null}]}";
            var items = ObserverQueries.ParseCommands(data, T0);
            Assert.AreEqual(2, items.Count);
            Assert.AreEqual("CANCELLED", items[0].Command.Status, "the named copy is the later word");
            Assert.AreEqual("goto-refuel", items[0].Command.Name);
            Assert.AreEqual("SUCCESSFUL", items[1].Command.Status);
            StringAssert.DoesNotContain("commandKey", ObserverQueries.CommandsQuery(true));
            StringAssert.Contains("commandsByToken", ObserverQueries.CommandsQuery(true));
            StringAssert.DoesNotContain("commandsByToken", ObserverQueries.CommandsQuery(false));
        }

        [Test]
        public void TheCommandVariablesAskForTheNonTerminalStatesOnly()
        {
            using var doc = JsonDocument.Parse(ObserverQueries.CommandsVariables(new[] { "c1", "c2" }));
            var c = doc.RootElement.GetProperty("c");
            Assert.AreEqual(1, c.GetProperty("pageNumber").GetInt32());
            Assert.AreEqual(100, c.GetProperty("pageSize").GetInt32());
            CollectionAssert.AreEqual(new[] { "QUEUED", "HELD", "SENT", "PARKED" }, c.GetProperty("statuses").EnumerateArray().Select(e => e.GetString()));
            CollectionAssert.AreEqual(new[] { "c1", "c2" }, doc.RootElement.GetProperty("t").EnumerateArray().Select(e => e.GetString()));
            using var none = JsonDocument.Parse(ObserverQueries.CommandsVariables(new string[0]));
            Assert.IsFalse(none.RootElement.TryGetProperty("t", out _));
        }

        [Test]
        public void LocationsAlarmsAndPresenceParse()
        {
            var loc = ObserverQueries.ParseLocations("{\"latestLocations\":[{\"deviceToken\":\"sp-hauler-06\",\"speed\":5.2397,\"heading\":354.8,\"elevation\":1800.0,\"occurredTime\":\"2026-10-06T03:31:00Z\"},{\"deviceToken\":\"x\",\"speed\":null,\"heading\":null,\"elevation\":null,\"occurredTime\":null}]}", T0);
            Assert.AreEqual(1, loc.Count);
            Assert.AreEqual(5.2397, loc[0].Location.SpeedMps);

            var snap = ObserverQueries.ParseActiveAlarms("{\"alarms\":{\"results\":[{\"token\":\"a1\",\"originatorType\":\"device\",\"originatorToken\":\"sp-hauler-06\",\"alarmKey\":\"low-fuel\",\"metricKey\":\"fuel_pct\",\"state\":\"ACTIVE\",\"severity\":\"CRITICAL\",\"acknowledged\":false,\"raisedTime\":\"2026-10-06T03:31:00Z\"},{\"token\":\"a2\",\"originatorToken\":null,\"alarmKey\":\"low-fuel\",\"state\":\"ACTIVE\",\"severity\":\"WARNING\",\"raisedTime\":\"2026-10-06T03:31:00Z\"}]}}", T0.AddSeconds(-1), T0);
            Assert.AreEqual(1, snap.Alarms.Count, "an alarm with no device is not a card's");
            Assert.AreEqual("CRITICAL", snap.Alarms[0].Alarm.Severity);
            Assert.AreEqual(T0.AddSeconds(-1), snap.RequestedAt);

            var frame = ObserverQueries.ParseAlarmFrame(JsonDocument.Parse("{\"alarmStream\":{\"eventType\":\"CLEARED\",\"alarmToken\":\"a1\",\"originatorToken\":\"sp-hauler-06\",\"alarmKey\":\"low-fuel\",\"metricKey\":\"fuel_pct\",\"state\":\"CLEARED\",\"severity\":\"CRITICAL\",\"acknowledged\":false,\"occurredTime\":\"2026-10-06T03:35:00Z\"}}").RootElement, T0);
            Assert.AreEqual("CLEARED", frame.Alarm.State);
            Assert.AreEqual("a1", frame.Alarm.Token);

            var pres = ObserverQueries.ParsePresence("{\"deviceStatesByDeviceToken\":[{\"deviceToken\":\"sp-hauler-06\",\"active\":true,\"lastActivityTime\":\"2026-10-06T03:31:00Z\"}]}", T0);
            Assert.IsTrue(pres[0].Presence.Active);
        }

        [Test]
        public void TheAlarmStreamIsOnTheDeviceManagementAddressOfTheSameOrigin()
        {
            Assert.AreEqual("ws://localhost/api/device-management/graphql", ObserverQueries.AlarmSocket(new Uri("ws://localhost/api/event-management/graphql")).AbsoluteUri);
            Assert.AreEqual("wss://dc.example:8443/prefix/api/device-management/graphql", ObserverQueries.AlarmSocket(new Uri("wss://dc.example:8443/prefix/api/event-management/graphql")).AbsoluteUri);
            Assert.Throws<ArgumentException>(() => ObserverQueries.AlarmSocket(new Uri("ws://localhost/graphql")));
        }

        // ---- the header

        static ReadinessBoard Board(out string[] ids)
        {
            ids = new[] { "SP-HL-0001", "SP-HL-0002", "SP-HL-0003" };
            var board = new ReadinessBoard(ids.Select(i => new SceneDevice(i, SceneKind.Hauler)).ToList(), "t");
            foreach (var i in ids)
            {
                board.SetBind(new BindResult { ExternalId = i, Outcome = BindOutcome.Bound, DeviceToken = "tok-" + i });
                board.SetStage(i, DeviceStage.Publishing);
                board[i].LastPublishUtc = DateTimeOffset.UtcNow;
            }

            board.BeginSessions();
            foreach (var i in ids) board.SetStage(i, DeviceStage.Publishing);
            return board;
        }

        [Test]
        public void TheHeaderCountsObservedDevicesAndNeverRoundsUp()
        {
            var board = Board(out var ids);
            Assert.AreEqual("3/3 publishing · 0 failed", board.Summary());
            board.BeginObserver();
            Assert.AreEqual("0/3 observed · 0 failed", board.Summary(), "publishing is not observed");
            Assert.IsTrue(board.MarkObserved("tok-" + ids[0]));
            Assert.AreEqual("1/3 observed · 0 failed", board.Summary());
            Assert.IsFalse(board.MarkObserved("tok-" + ids[0]), "only the first observation counts");
            Assert.IsFalse(board.MarkObserved("tok-nobody"));
            board.FailSession(ids[2], "blind");
            Assert.IsFalse(board.MarkObserved("tok-" + ids[2]), "a failed device is not observed");
            Assert.AreEqual("1/3 observed · 1 failed", board.Summary());
        }

        [Test]
        public void ADeviceWithNoSessionIsNeverMarkedObserved()
        {
            var board = new ReadinessBoard(new[] { new SceneDevice("SP-HL-0001", SceneKind.Hauler) }, "t");
            board.SetBind(new BindResult { ExternalId = "SP-HL-0001", Outcome = BindOutcome.Bound, DeviceToken = "tok" });
            board.BeginObserver();
            Assert.IsFalse(board.MarkObserved("tok"));
            Assert.AreEqual("0/1 observed · 0 failed", board.Summary());
        }

        [Test]
        public void AStalledObservedDeviceIsNotCountedAsObserved()
        {
            var board = Board(out var ids);
            board.BeginObserver();
            foreach (var i in ids) board.MarkObserved("tok-" + i);
            Assert.AreEqual("3/3 observed · 0 failed", board.Summary());
            board.Evaluate(DateTimeOffset.UtcNow.AddSeconds(30));
            Assert.AreEqual(0, board.ObservedCount);
            Assert.AreEqual(3, board.StalledCount);
            Assert.AreEqual("0/3 observed · 3 stalled · 0 failed", board.Summary());
        }

        [Test]
        public void TheBoardMapsASceneDeviceToItsToken()
        {
            var board = Board(out var ids);
            Assert.AreEqual("tok-" + ids[1], board.TokenOf(ids[1]));
            Assert.IsNull(board.TokenOf("SP-XX-9999"));
            Assert.IsNull(board.TokenOf(null));
        }

        // ---- the local simulation panel

        sealed class IdleLink : IDeviceLink
        {
            public event Action<LinkState> StateChanged { add { } remove { } }
            public bool CanPublish => false;
            public Task StartAsync(string refusalReason, Action<string> onCommand, CancellationToken cancellationToken) => Task.CompletedTask;
            public Task PublishAsync(Sample sample, CancellationToken cancellationToken) => Task.CompletedTask;
            public ValueTask DisposeAsync() => default;
        }

        [Test]
        public void TheLocalSimulationPanelSaysItIsNotPlatformData()
        {
            var host = new DeviceSessionHost(new SceneDevice("SP-HL-0001", SceneKind.Hauler), "tok", new IdleLink(), 1, new DeviceInbox());
            var text = LocalSimulationView.Text(new[] { host }, T0);
            StringAssert.StartsWith("Local simulation (not platform data)", text);
            StringAssert.Contains("SP-HL-0001", text);
            StringAssert.Contains("fuel ", text);
            StringAssert.Contains("sample none", text);
            StringAssert.Contains("ack none", text);
            StringAssert.Contains("err 0", text);
            StringAssert.Contains("drop 0", text);
            Assert.AreEqual("0.4 s", LocalSimulationView.Age(T0.AddMilliseconds(-400), T0));
            Assert.AreEqual("0.0 s", LocalSimulationView.Age(T0.AddSeconds(1), T0), "an age never reads negative");
            StringAssert.Contains("no device has a session", LocalSimulationView.Text(new DeviceSessionHost[0], T0));
        }
    }
}
