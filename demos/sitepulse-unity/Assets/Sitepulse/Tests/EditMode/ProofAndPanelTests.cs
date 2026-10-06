// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Recording;
using DeviceChain.Sitepulse.Replay;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    /// <summary>The proof drawer's evidence: only things that happened, each said by someone, never a row nobody said.</summary>
    public sealed class ProofLogTests
    {
        static readonly DateTimeOffset T0 = new DateTimeOffset(2026, 10, 6, 14, 0, 0, TimeSpan.Zero);
        const string Truck = "SP-HL-0006";

        static List<ProofRow> Rows(ProofLog log, string device = Truck, int max = 12)
        {
            var rows = new List<ProofRow>();
            log.Rows(device, rows, max);
            return rows;
        }

        static string[] Texts(ProofLog log) => Rows(log).Select(r => r.Text).ToArray();

        [Test]
        public void TheFirstSampleBelowARulesLineIsARowAndTheNextOnesAreNot()
        {
            var log = new ProofLog();
            log.SampleObserved(Truck, "fuel_pct", 15.4, T0, false);
            log.SampleObserved(Truck, "fuel_pct", 14.96, T0.AddSeconds(1), false);
            log.SampleObserved(Truck, "fuel_pct", 14.9, T0.AddSeconds(2), false);
            var rows = Rows(log);
            Assert.AreEqual(1, rows.Count, "one crossing is one row");
            StringAssert.Contains("fuel_pct 14.96 %", rows[0].Text);
            StringAssert.Contains("below the rule line (15 %)", rows[0].Text);
            Assert.AreEqual(T0.AddSeconds(1), rows[0].At, "dated when the sample happened");
            Assert.AreEqual(ProofSource.Platform, rows[0].Source);
        }

        [Test]
        public void ALineIsRearmedByASampleAtOrAboveItSoARefuelAndASecondCrossingIsASecondRow()
        {
            var log = new ProofLog();
            log.SampleObserved(Truck, "fuel_pct", 14.0, T0, false);
            log.SampleObserved(Truck, "fuel_pct", 15.0, T0.AddSeconds(1), false);
            log.SampleObserved(Truck, "fuel_pct", 14.5, T0.AddSeconds(2), false);
            Assert.AreEqual(2, Rows(log).Count);
        }

        [Test]
        public void ASnapshotsMeasurementIsNotAnEventAndAKeyNoRuleWatchesHasNoRow()
        {
            var log = new ProofLog();
            log.SampleObserved(Truck, "fuel_pct", 3.0, T0, true);
            log.SampleObserved(Truck, "engine_temp_c", 120, T0, false);
            log.SampleObserved(Truck, "payload_t", 0, T0, false);
            Assert.AreEqual(0, Rows(log).Count);
            // and the snapshot did not use up the line: the first live sample below it is still a row
            log.SampleObserved(Truck, "fuel_pct", 14.2, T0.AddSeconds(5), false);
            Assert.AreEqual(1, Rows(log).Count);
        }

        [Test]
        public void ATyreSampleBelowSixHundredIsARowWithItsUnit()
        {
            var log = new ProofLog();
            log.SampleObserved("SP-HL-0003", "tyre_pressure_kpa", 599.4, T0, false);
            var row = Rows(log, "SP-HL-0003").Single();
            StringAssert.Contains("tyre_pressure_kpa 599.4 kPa", row.Text);
            StringAssert.Contains("(600 kPa)", row.Text);
        }

        [Test]
        public void EachStateOfAnAlarmAndOfACommandIsOneRowHoweverOftenItIsRepeated()
        {
            var log = new ProofLog();
            for (var i = 0; i < 4; i++)
            {
                log.AlarmObserved(Truck, "al-1", "low-fuel", "ACTIVE", "MAJOR", T0);
                log.CommandObserved(Truck, "c-1", "goto-refuel", "SENT", T0, T0.AddSeconds(1 + i));
            }

            Assert.AreEqual(new[] { "alarm low-fuel ACTIVE · MAJOR", "command goto-refuel SENT" }, Texts(log));
            log.AlarmObserved(Truck, "al-1", "low-fuel", "CLEARED", "MAJOR", T0.AddSeconds(30));
            Assert.AreEqual(3, Rows(log).Count);
            // a second alarm of the same key is another alarm, with its own rows
            log.AlarmObserved(Truck, "al-2", "low-fuel", "ACTIVE", "MAJOR", T0.AddSeconds(90));
            Assert.AreEqual(4, Rows(log).Count);
        }

        [Test]
        public void ACommandIsDatedWhenItWasQueuedAndEveryOtherStateWhenItWasSeen()
        {
            var log = new ProofLog();
            log.CommandObserved(Truck, "c-1", "goto-refuel", "QUEUED", T0.AddSeconds(1), T0.AddSeconds(2));
            log.CommandObserved(Truck, "c-1", "goto-refuel", "SENT", T0.AddSeconds(1), T0.AddSeconds(3));
            log.CommandObserved(Truck, "c-1", "goto-refuel", "SUCCESSFUL", T0.AddSeconds(1), T0.AddSeconds(50));
            CollectionAssert.AreEqual(new[] { T0.AddSeconds(1), T0.AddSeconds(3), T0.AddSeconds(50) }, Rows(log).Select(r => r.At).ToArray());
        }

        [Test]
        public void AMissingPlatformRowStaysMissing()
        {
            // the observer's poll saw the command only once it was SENT: there is no QUEUED row, and none is made up
            var log = new ProofLog();
            log.CommandObserved(Truck, "c-1", "goto-refuel", "SENT", T0, T0.AddSeconds(1));
            CollectionAssert.AreEqual(new[] { "command goto-refuel SENT" }, Texts(log));

            // the device finished and said SUCCESS: that is the device's row, never the platform's SUCCESSFUL
            log.DeviceRow(Truck, T0.AddSeconds(40), "outcome", "SUCCESS: refuelled 14.6% to 95.0%");
            var rows = Rows(log);
            Assert.IsFalse(rows.Any(r => r.Source == ProofSource.Platform && r.Text.Contains("SUCCESSFUL")), "the platform has not said SUCCESSFUL");
            Assert.AreEqual(ProofSource.Device, rows.Last().Source);

            // an alarm nobody reported cleared has no CLEARED row, however far the fuel has risen since
            log.AlarmObserved(Truck, "al-1", "low-fuel", "ACTIVE", "MAJOR", T0);
            log.SampleObserved(Truck, "fuel_pct", 90, T0.AddSeconds(60), false);
            Assert.IsFalse(Rows(log).Any(r => r.Text.Contains("CLEARED")));
        }

        [Test]
        public void RowsAreInTheOrderTheThingsHappenedWhateverTheOrderTheyWereToldIn()
        {
            var log = new ProofLog();
            log.DeviceRow(Truck, T0.AddSeconds(5), "received", "goto-refuel (…abc)");
            log.AlarmObserved(Truck, "al-1", "low-fuel", "ACTIVE", "MAJOR", T0.AddSeconds(2));
            log.CommandObserved(Truck, "c-1", "goto-refuel", "QUEUED", T0.AddSeconds(3), T0.AddSeconds(4));
            Assert.AreEqual(new[] { "alarm low-fuel ACTIVE · MAJOR", "command goto-refuel QUEUED", "received · goto-refuel (…abc)" }, Texts(log));
        }

        [Test]
        public void OnlyTheDevicesOwnAccountOfWhatItDidIsShownAndAPresentersRowSaysItWasTheirHand()
        {
            var log = new ProofLog();
            foreach (var kind in new[] { "received", "accepted", "refuelling", "outcome", "arrived", "returning", "working", "presenter" })
                log.DeviceRow(Truck, T0.AddSeconds(1), kind, "text");
            var rows = Rows(log);
            CollectionAssert.AreEquivalent(new[] { "received", "accepted", "refuelling", "outcome", "presenter" }, rows.Select(r => r.Text.Split(' ')[0]).ToArray(),
                "the machine's own business (queue arrivals, the drive back, being back on its track) is not part of the chain");
            Assert.AreEqual(ProofSource.Presenter, rows.Single(r => r.Text.StartsWith("presenter", StringComparison.Ordinal)).Source);
            Assert.AreEqual("presenter", rows.Single(r => r.Source == ProofSource.Presenter).SourceLabel);
            Assert.IsTrue(rows.Where(r => r.Source == ProofSource.Device).All(r => r.SourceLabel == "device"));
        }

        [Test]
        public void TheNewestRowsAreKeptAndAMachineWithNoRowsHasNone()
        {
            var log = new ProofLog();
            for (var i = 0; i < ProofLog.MaxPerDevice + 20; i++) log.DeviceRow(Truck, T0.AddSeconds(i), "received", "c" + i);
            Assert.AreEqual(ProofLog.MaxPerDevice, Rows(log, max: 1000).Count);
            Assert.AreEqual("received · c" + (ProofLog.MaxPerDevice + 19), Rows(log, max: 1000).Last().Text);
            Assert.AreEqual(3, Rows(log, max: 3).Count);
            Assert.AreEqual(0, Rows(log, "SP-LD-0001").Count);
            Assert.AreEqual(0, Rows(log, null).Count);
        }

        [Test]
        public void TheVersionMovesWhenARowIsAddedAndNotWhenNothingWas()
        {
            var log = new ProofLog();
            var v = log.Version;
            log.SampleObserved(Truck, "fuel_pct", 50, T0, false);
            Assert.AreEqual(v, log.Version, "a sample above the line adds nothing");
            log.AlarmObserved(Truck, "al-1", "low-fuel", "ACTIVE", "MAJOR", T0);
            Assert.Greater(log.Version, v);
            v = log.Version;
            log.AlarmObserved(Truck, "al-1", "low-fuel", "ACTIVE", "MAJOR", T0);
            Assert.AreEqual(v, log.Version, "a repeat adds nothing");
        }

        [Test]
        public void TheWholeLowFuelChainReadsInOrderFromTheSampleToTheClear()
        {
            var log = new ProofLog();
            log.DeviceRow(Truck, T0, "presenter", "prepare low-fuel cycle: fuel 18.0% -> 15.5%");
            log.SampleObserved(Truck, "fuel_pct", 14.97, T0.AddSeconds(40), false);
            log.AlarmObserved(Truck, "al-1", "low-fuel", "ACTIVE", "MAJOR", T0.AddSeconds(41));
            log.CommandObserved(Truck, "c-1", "goto-refuel", "QUEUED", T0.AddSeconds(41.5), T0.AddSeconds(42));
            log.CommandObserved(Truck, "c-1", "goto-refuel", "SENT", T0.AddSeconds(41.5), T0.AddSeconds(43));
            log.DeviceRow(Truck, T0.AddSeconds(43.2), "received", "goto-refuel (…abc)");
            log.DeviceRow(Truck, T0.AddSeconds(43.3), "accepted", "refuel bay: route 212 m, ETA 48 s");
            log.DeviceRow(Truck, T0.AddSeconds(100), "refuelling", "service started at 14.6% fuel");
            log.AlarmObserved(Truck, "al-1", "low-fuel", "CLEARED", "MAJOR", T0.AddSeconds(102));
            log.DeviceRow(Truck, T0.AddSeconds(140), "refuelling", "service finished: 14.6% -> 95.0%");
            log.DeviceRow(Truck, T0.AddSeconds(140), "outcome", "SUCCESS: refuelled 14.6% to 95.0%");
            log.CommandObserved(Truck, "c-1", "goto-refuel", "SUCCESSFUL", T0.AddSeconds(41.5), T0.AddSeconds(141));
            var texts = Texts(log);
            Assert.AreEqual(12, texts.Length, "the chain fits the drawer's twelve rows");
            Assert.That(texts[1], Does.StartWith("sample fuel_pct 14.97 %"));
            Assert.AreEqual("alarm low-fuel CLEARED · MAJOR", texts[8]);
            Assert.AreEqual("command goto-refuel SUCCESSFUL", texts[11]);
            Assert.AreEqual(ProofDrawerRowsFit, Rows(log).Count);
        }

        const int ProofDrawerRowsFit = 12;
    }

    /// <summary>What a recording's lines become: the same rows the live app held, from the one function both use.</summary>
    public sealed class ProofFeedTests
    {
        static readonly DateTimeOffset T0 = SyntheticRun.Start;
        const string Token = SyntheticRun.TruckToken;

        static string IdOf(string token) => token == Token ? SyntheticRun.Truck : null;

        static ObservedLine Cmd(string status, double queued, double seen) =>
            SyntheticRun.Command(Token, "c-1", "goto-refuel", status, T0.AddSeconds(queued), T0.AddSeconds(seen));

        [Test]
        public void ObservedLinesBecomeRowsForTheSceneIdOfTheDeviceTheyNameAndNoOtherDevice()
        {
            var log = new ProofLog();
            ProofFeed.Apply(log, SyntheticRun.Measurement(Token, "fuel_pct", 14.9, T0, T0), IdOf);
            ProofFeed.Apply(log, SyntheticRun.Alarm(Token, "al-1", "low-fuel", "ACTIVE", T0.AddSeconds(1)), IdOf);
            ProofFeed.Apply(log, Cmd("SENT", 2, 3), IdOf);
            ProofFeed.Apply(log, SyntheticRun.Alarm("sp-unknown-device", "al-9", "low-fuel", "ACTIVE", T0.AddSeconds(1)), IdOf);
            var rows = new List<ProofRow>();
            log.Rows(SyntheticRun.Truck, rows, 12);
            Assert.AreEqual(3, rows.Count);
            Assert.IsTrue(rows.All(r => r.Source == ProofSource.Platform));
            rows.Clear();
            log.Rows("sp-unknown-device", rows, 12);
            Assert.AreEqual(0, rows.Count, "a device the scene does not know has no drawer");
        }

        [Test]
        public void AnAlarmSnapshotsActiveAlarmsAreRowsButAnAlarmItLeavesOutIsNotMadeIntoAClear()
        {
            var log = new ProofLog();
            ProofFeed.Apply(log, SyntheticRun.Alarm(Token, "al-1", "low-fuel", "ACTIVE", T0), IdOf);
            var snap = ObservedLine.Of(ObservedKinds.AlarmSnapshot);
            snap.RequestedAt = T0.AddSeconds(30);
            snap.ObservedAt = T0.AddSeconds(31);
            snap.Alarms.Add(SyntheticRun.Alarm(Token, "al-7", "tyre-pressure-low", "ACTIVE", T0.AddSeconds(20)));
            ProofFeed.Apply(log, snap, IdOf);
            var rows = new List<ProofRow>();
            log.Rows(SyntheticRun.Truck, rows, 12);
            Assert.AreEqual(2, rows.Count, "al-1 is not listed by the snapshot, and nobody said it cleared");
            Assert.IsFalse(rows.Any(r => r.Text.Contains("CLEARED")));
            Assert.IsTrue(rows.Any(r => r.Text.Contains("tyre-pressure-low ACTIVE")));
        }

        [Test]
        public void ARecordingsProofRowsAreTheLiveOnesAndRewindingRemovesWhatHasNotHappenedYet()
        {
            using var run = new SyntheticRun();
            run.Drive(0, 12.0);
            run.Obs(2.5, SyntheticRun.Measurement(Token, "fuel_pct", 14.9, T0.AddSeconds(2.5), T0.AddSeconds(2.5)));
            run.Obs(3.0, SyntheticRun.Alarm(Token, "al-1", "low-fuel", "ACTIVE", T0.AddSeconds(3)));
            run.Dev(3.2, SyntheticRun.Row(SyntheticRun.Truck, "received", "goto-refuel (…abc)"));
            run.Obs(3.5, Cmd("SENT", 3.4, 3.5));
            run.Dev(7.0, SyntheticRun.Row(SyntheticRun.Truck, "arrived", "refuel queue"));
            run.Obs(9.0, SyntheticRun.Alarm(Token, "al-1", "low-fuel", "CLEARED", T0.AddSeconds(9)));
            var data = run.Reload();
            var session = new ReplaySession(data);

            // what the live app held: the same lines through the same function
            var live = new ProofLog();
            foreach (var l in data.Observed) ProofFeed.Apply(live, l, IdOf);
            foreach (var d in data.Device)
                if (d.K == DeviceKinds.Timeline) live.DeviceRow(d.Device, d.Utc, d.RowKind, d.Text);

            string Say(ProofLog log)
            {
                var rows = new List<ProofRow>();
                log.Rows(SyntheticRun.Truck, rows, 12);
                return string.Join("|", rows.Select(r => PanelModel.Clock(r.At) + " " + r.SourceLabel + " " + r.Text));
            }

            session.Seek(12.0);
            Assert.AreEqual(Say(live), Say(session.Proof), "a drawer drawn from the recording holds the rows the live one held");

            session.Seek(3.3);
            var rows3 = new List<ProofRow>();
            session.Proof.Rows(SyntheticRun.Truck, rows3, 12);
            Assert.AreEqual(3, rows3.Count, "at 3.3 s: the sample, the alarm and the device's receipt; nothing that comes later");
            Assert.IsFalse(rows3.Any(r => r.Text.Contains("CLEARED") || r.Text.Contains("SENT")));

            session.Seek(0.5);
            var rows0 = new List<ProofRow>();
            session.Proof.Rows(SyntheticRun.Truck, rows0, 12);
            Assert.AreEqual(0, rows0.Count, "rewound to before anything happened, the drawer is empty");
            session.Seek(12.0);
            Assert.AreEqual(Say(live), Say(session.Proof), "and played forward again it is the same");
        }
    }

    /// <summary>The selected-machine panel: the profile's own metrics, with units and the time each was last observed.</summary>
    public sealed class PanelModelTests
    {
        static readonly DateTimeOffset Now = new DateTimeOffset(2026, 10, 6, 14, 30, 0, TimeSpan.Zero);

        static DeviceReading Reading(DeviceReading.Profile profile, string id = "SP-DZ-0001", Provenance p = Provenance.Observed) => new DeviceReading(id, profile, p);

        static Observation Seen(double secondsAgo) => new Observation(Now.AddSeconds(-secondsAgo), Now.AddSeconds(-secondsAgo + 0.2));

        static IReadOnlyList<string> KeysOf(Simulation.EquipmentKind k) => Simulation.MachineModel.KeysFor(k);

        [Test]
        public void ADozerShowsItsThreeKeysATruckAndALoaderTheirFiveAndTheCrusherItsTwo()
        {
            var dozer = PanelModel.Build(Reading(DeviceReading.Profile.Equipment), "Dozer", KeysOf(Simulation.EquipmentKind.Dozer), Now, true);
            CollectionAssert.AreEqual(new[] { "Fuel", "Engine temp", "Engine hours" }, dozer.Rows.Select(r => r.Label).ToArray(), "a tracked dozer has no payload or tyres");
            foreach (var kind in new[] { Simulation.EquipmentKind.Hauler, Simulation.EquipmentKind.Loader })
            {
                var v = PanelModel.Build(Reading(DeviceReading.Profile.Equipment, "SP-HL-0001"), "Haul truck", KeysOf(kind), Now, true);
                CollectionAssert.AreEqual(new[] { "Fuel", "Engine temp", "Engine hours", "Payload", "Tyre pressure" }, v.Rows.Select(r => r.Label).ToArray());
            }

            var plant = PanelModel.Build(Reading(DeviceReading.Profile.Plant, "SP-PL-0001"), "Primary crusher", KeysOf(Simulation.EquipmentKind.Plant), Now, true);
            CollectionAssert.AreEqual(new[] { "Throughput", "Running" }, plant.Rows.Select(r => r.Label).ToArray());
        }

        [Test]
        public void EachRowHasItsUnitAndTheTimeItWasLastObservedAndTheFooterNamesTheNewest()
        {
            var r = Reading(DeviceReading.Profile.Equipment);
            r.Set(MeasurementKeys.FuelPct, 47.4, Provenance.Observed, Seen(1));
            r.Set(MeasurementKeys.EngineTempC, 91.2, Provenance.Observed, Seen(2));
            r.Set(MeasurementKeys.EngineHours, 4321.6, Provenance.Observed, Seen(0.5));
            var v = PanelModel.Build(r, "Dozer", KeysOf(Simulation.EquipmentKind.Dozer), Now, true);
            Assert.AreEqual("47 %", v.Rows[0].Value);
            Assert.AreEqual("91 °C", v.Rows[1].Value);
            Assert.AreEqual("4322 h", v.Rows[2].Value);
            Assert.AreEqual("14:29:59", v.Rows[0].ObservedAt);
            Assert.AreEqual("14:29:58", v.Rows[1].ObservedAt);
            Assert.AreEqual("last observed 14:29:59 UTC", v.Footer, "the newest of the rows' times: 0.5 s ago rounds to the second it happened in");
            Assert.IsTrue(v.Rows.All(x => x.Tone == RowTone.Ink), "fresh values are inked fresh");
        }

        [Test]
        public void AStaleValueIsDimAnOldOneGreyAndOneAMinuteOldIsADashNeverANumber()
        {
            var r = Reading(DeviceReading.Profile.Equipment);
            r.Set(MeasurementKeys.FuelPct, 47, Provenance.Observed, Seen(8));
            r.Set(MeasurementKeys.EngineTempC, 91, Provenance.Observed, Seen(30));
            r.Set(MeasurementKeys.EngineHours, 4321, Provenance.Observed, Seen(90));
            var v = PanelModel.Build(r, "Dozer", KeysOf(Simulation.EquipmentKind.Dozer), Now, true);
            Assert.AreEqual(RowTone.Dim, v.Rows[0].Tone, "late: dimmed");
            Assert.AreEqual(RowTone.Grey, v.Rows[1].Tone, "old: greyed");
            Assert.AreEqual(CardPresenter.NoValue, v.Rows[2].Value);
            Assert.AreEqual("", v.Rows[2].ObservedAt, "and no time is claimed for a value that is not shown");
            Assert.AreEqual("last observed 14:29:52 UTC", v.Footer);
        }

        [Test]
        public void ANothingObservedReadingShowsDashesAndSaysSo()
        {
            var v = PanelModel.Build(Reading(DeviceReading.Profile.Equipment), "Dozer", KeysOf(Simulation.EquipmentKind.Dozer), Now, true);
            Assert.IsTrue(v.Rows.All(x => x.Value == CardPresenter.NoValue && x.ObservedAt == ""));
            Assert.AreEqual("nothing observed yet", v.Footer);
        }

        [Test]
        public void AnIllustrativePanelSaysItsValuesHaveNoPlatformTime()
        {
            var r = Reading(DeviceReading.Profile.Equipment, p: Provenance.Illustrative);
            r.Set(MeasurementKeys.FuelPct, 47, Provenance.Illustrative);
            var v = PanelModel.Build(r, "Dozer", KeysOf(Simulation.EquipmentKind.Dozer), Now, true);
            Assert.AreEqual("47 %", v.Rows[0].Value);
            Assert.AreEqual("", v.Rows[0].ObservedAt);
            Assert.AreEqual("illustrative values (no platform time)", v.Footer);
        }

        [Test]
        public void AReplayedPanelAgesAgainstTheRecordingsClockLikeALiveOne()
        {
            var r = Reading(DeviceReading.Profile.Equipment, p: Provenance.Replayed);
            r.Set(MeasurementKeys.FuelPct, 47, Provenance.Replayed, Seen(1));
            var live = PanelModel.Build(r, "Dozer", KeysOf(Simulation.EquipmentKind.Dozer), Now, true);
            var stopped = PanelModel.Build(r, "Dozer", KeysOf(Simulation.EquipmentKind.Dozer), Now, false);
            Assert.AreEqual(RowTone.Ink, live.Rows[0].Tone);
            Assert.AreEqual(RowTone.Grey, stopped.Rows[0].Tone, "while the stream was not live nothing is called fresh");
        }

        [Test]
        public void ThePlantsRunningFlagReadsYesOrNo()
        {
            var r = Reading(DeviceReading.Profile.Plant, "SP-PL-0001");
            r.Set(MeasurementKeys.ThroughputTph, 720, Provenance.Observed, Seen(1));
            r.Set(MeasurementKeys.PlantRunning, true, Provenance.Observed, Seen(1));
            var v = PanelModel.Build(r, "Primary crusher", KeysOf(Simulation.EquipmentKind.Plant), Now, true);
            Assert.AreEqual("720 t/h", v.Rows[0].Value);
            Assert.AreEqual("Yes", v.Rows[1].Value);
        }
    }
}
