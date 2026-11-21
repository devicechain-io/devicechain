// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>
    /// A command is tied to an alarm only by the cause the platform recorded with it: the alarm with the cause's key on the
    /// command's device whose originatorId is the cause's deviceId, raised at or before the detection and not cleared before it.
    /// </summary>
    public partial class DirectorTestsObserver
    {
        const string Charger = "hp-charger-basin-2", Hot = "connector-hot", Row = "device-row";
        static string HotAlarm(double raised = 14, double? cleared = null, string originatorId = Row, string key = Hot, string token = "alarm-hot")
            => "{\"token\":" + Q(token) + ",\"originatorToken\":" + Q(Charger) + ",\"originatorId\":" + (originatorId == null ? "null" : Q(originatorId)) + ",\"alarmKey\":" + Q(key)
                + ",\"state\":" + Q(cleared.HasValue ? "CLEARED" : "ACTIVE") + ",\"severity\":\"MAJOR\",\"raisedTime\":" + Q(Ts(raised)) + ",\"clearedTime\":" + (cleared.HasValue ? Q(Ts(cleared.Value)) : "null") + ",\"raisedDuringSuppression\":null}";
        static string Cause(string kind = "ALARM", string detectedAt = null, string alarmKey = Hot, string deviceId = Row)
            => "{\"kind\":" + Q(kind) + ",\"ruleId\":\"tenant/hp-charger@v3/hp-connector-hot\",\"ruleToken\":\"hp-connector-hot\",\"profileToken\":\"hp-charger\",\"detectionKind\":\"aggregate\",\"detectedAt\":" + Q(detectedAt ?? Ts(14))
                + ",\"value\":71.5,\"alarmKey\":" + (alarmKey == null ? "null" : Q(alarmKey)) + ",\"deviceId\":" + (deviceId == null ? "null" : Q(deviceId)) + "}";
        static string Derate(string cause, string status = "SUCCESSFUL")
            => "{\"token\":\"derate-real\",\"deviceToken\":" + Q(Charger) + ",\"name\":\"derate\",\"status\":" + Q(status) + ",\"queuedTime\":" + Q(Ts(14.01)) + ",\"sentTime\":" + Q(Ts(14.012)) + ",\"respondedTime\":" + Q(Ts(14.02))
                + ",\"payload\":\"{\\\"max_kw\\\":11}\",\"cause\":" + (cause ?? "null") + "}";
        static List<Observation> Observe(double sim, params DirectorRead[] reads)
        {
            var snapshot = new DirectorReadSnapshot(reads); var obs = new List<Observation>(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
            adapter.Update(Inputs(sim), obs.Add, _ => { });
            Assert.That(adapter.Problems, Is.Empty, string.Join("; ", adapter.Problems));
            return obs;
        }
        static void AssertUnlinked(IEnumerable<Observation> obs)
        {
            var commands = obs.Where(o => o.Kind == "command").ToArray();
            Assert.That(commands.Select(o => o.Status), Is.EquivalentTo(new[] { "QUEUED", "SENT", "SUCCESSFUL" }), "the command itself is still evidence");
            Assert.That(commands.All(o => o.Field("alarmToken") == null && o.Cycle == ""), Is.True, "no alarm cycle may be named");
        }
        [Test]
        public void RecordedCauseTiesTheCommandToItsAlarmCycle()
        {
            var obs = Observe(14.1, Read("alarms", 14.1, Page(HotAlarm())), Read("commands", 14.1, Page(Derate(Cause()))));
            var commands = obs.Where(o => o.Kind == "command").ToArray();
            Assert.That(commands.Select(o => o.Status), Is.EquivalentTo(new[] { "QUEUED", "SENT", "SUCCESSFUL" }));
            foreach (var c in commands) {
                Assert.That(c.Field("alarmToken"), Is.EqualTo("alarm-hot")); Assert.That(c.Cycle, Is.EqualTo(Ts(14)));
                Assert.That(c.Field("cause"), Is.EqualTo("ALARM")); Assert.That(c.Field("causeUnresolved"), Is.Null); Assert.That(c.Field("max_kw"), Is.EqualTo("11"));
            }
            // The alarm observation the page triggers on carries the same identity and cycle.
            var alarm = obs.Single(o => o.Kind == "alarm"); Assert.That(alarm.Identity, Is.EqualTo("alarm-hot")); Assert.That(alarm.Cycle, Is.EqualTo(commands[0].Cycle));
        }
        [Test]
        public void ACauseWithNanosecondsStillMeetsAnAlarmStoredAtMicroseconds()
        {
            // The platform serves detectedAt at up to nine digits; the alarm's raisedTime is the same instant cut to microseconds.
            var raised = "2026-01-01T08:00:00.123456Z"; var detected = "2026-01-01T08:00:00.123456789Z";
            Assert.That(DirectorObserver.Instant(detected).UtcTicks - DirectorObserver.Instant(raised).UtcTicks, Is.EqualTo(7));
            Assert.That(DirectorObserver.Instant("2026-01-01T08:00:00Z"), Is.EqualTo(DateTimeOffset.Parse("2026-01-01T08:00:00Z")));
            Assert.That(DirectorObserver.Instant("2026-01-01T08:00:00.999999999+00:00").UtcTicks % 10000000, Is.EqualTo(9999999), "cut, never rounded into the next second");
            var alarm = HotAlarm().Replace(Q(Ts(14)), Q(raised));
            var obs = Observe(14.1, Read("alarms", 14.1, Page(alarm)), Read("commands", 14.1, Page(Derate(Cause(detectedAt: detected)))));
            Assert.That(obs.Where(o => o.Kind == "command").All(o => o.Field("alarmToken") == "alarm-hot" && o.Cycle == raised), Is.True);
        }
        // What the platform reads back when the link is absent: no cause recorded, or a reader the cause is withheld from.
        [Test]
        public void ACommandWithNoCauseNamesNoAlarm()
        {
            var obs = Observe(14.1, Read("alarms", 14.1, Page(HotAlarm())), Read("commands", 14.1, Page(Derate(null))));
            AssertUnlinked(obs); Assert.That(obs.Where(o => o.Kind == "command").All(o => o.Field("cause") == null && o.Field("causeUnresolved") == null), Is.True);
            // The read without the field at all (the -hp-control-no-cause document) reads the same.
            obs = Observe(14.1, Read("alarms", 14.1, Page(HotAlarm())), Read("commands", 14.1, Page(Derate(null).Replace(",\"cause\":null", ""))));
            AssertUnlinked(obs);
        }
        [Test]
        public void ARuleCauseNamesNoAlarm()
        {
            var obs = Observe(14.1, Read("alarms", 14.1, Page(HotAlarm())), Read("commands", 14.1, Page(Derate(Cause("RULE", alarmKey: null)))));
            AssertUnlinked(obs); Assert.That(obs.Where(o => o.Kind == "command").All(o => o.Field("cause") == "RULE" && o.Field("causeUnresolved") == null), Is.True);
        }
        [TestCase("other-device-record", "another device record")]
        [TestCase("no-originator", "another device record")]
        [TestCase("other-key", "no alarm with that key")]
        [TestCase("raised-after", "began after the detection")]
        [TestCase("cleared-before", "cleared before the detection")]
        [TestCase("cleared-at", "cleared before the detection")]
        [TestCase("no-device-id", "names no device")]
        [TestCase("no-alarm-key", "names no alarm key")]
        [TestCase("no-alarm", "no alarm with that key")]
        public void AnAlarmCauseThatDoesNotBindIsReportedUnresolved(string scenario, string why)
        {
            var alarm = HotAlarm(); var cause = Cause();
            switch (scenario) {
                case "other-device-record": alarm = HotAlarm(originatorId: "an-earlier-device-row"); break; // the token was reused
                case "no-originator": alarm = HotAlarm(originatorId: null); break;
                case "other-key": alarm = HotAlarm(key: "charger-offline"); break;
                case "raised-after": alarm = HotAlarm(raised: 14.05); break;
                case "cleared-before": alarm = HotAlarm(raised: 13.5, cleared: 13.9); break;
                case "cleared-at": alarm = HotAlarm(raised: 13.5, cleared: 14); break;
                case "no-device-id": cause = Cause(deviceId: null); break;
                case "no-alarm-key": cause = Cause(alarmKey: null); break;
                case "no-alarm": alarm = null; break;
            }
            var obs = Observe(14.1, Read("alarms", 14.1, alarm == null ? Page() : Page(alarm)), Read("commands", 14.1, Page(Derate(cause))));
            AssertUnlinked(obs);
            foreach (var c in obs.Where(o => o.Kind == "command")) { Assert.That(c.Field("cause"), Is.EqualTo("ALARM")); Assert.That(c.Field("causeUnresolved"), Does.Contain(why)); }
        }
        [Test]
        public void TheCauseWaitsForAnAlarmReadThatReachesTheDetection()
        {
            // An alarm read taken before the detection cannot show the cycle the detection met, whatever it holds.
            DirectorReadSnapshot snapshot = null; var obs = new List<Observation>(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 13.99, Page(HotAlarm(raised: 13.5))), Read("commands", 14.1, Page(Derate(Cause()))) });
            adapter.Update(Inputs(14.1), obs.Add, _ => { });
            Assert.That(obs.Any(o => o.Kind == "command"), Is.False);
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 14.11, Page(HotAlarm(raised: 13.5))), Read("commands", 14.11, Page(Derate(Cause()))) });
            adapter.Update(Inputs(14.11), obs.Add, _ => { });
            Assert.That(obs.Where(o => o.Kind == "command").All(o => o.Field("alarmToken") == "alarm-hot" && o.Cycle == Ts(13.5)), Is.True);
            Assert.That(obs.Count(o => o.Kind == "command"), Is.EqualTo(3));
        }
        [TestCase(true)]
        [TestCase(false)]
        public void AnEarlierCycleCountsOnlyWhenItsOwnRecordOutlivedTheDetection(bool seenCleared)
        {
            DirectorReadSnapshot snapshot = null; var obs = new List<Observation>(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
            // The cycle of 14:00 is read (active, or already cleared at 14:12) before the command is.
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 14.25, Page(seenCleared ? HotAlarm(cleared: 14.2) : HotAlarm())) });
            adapter.Update(Inputs(14.25), obs.Add, _ => { });
            // By the time the command is read the alarm's row is a later cycle.
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 14.4, Page(HotAlarm(raised: 14.3))), Read("commands", 14.4, Page(Derate(Cause()))) });
            adapter.Update(Inputs(14.4), obs.Add, _ => { });
            var commands = obs.Where(o => o.Kind == "command").ToArray(); Assert.That(commands.Length, Is.EqualTo(3));
            if (seenCleared) Assert.That(commands.All(o => o.Field("alarmToken") == "alarm-hot" && o.Cycle == Ts(14)), Is.True);
            else { AssertUnlinked(commands); Assert.That(commands.All(o => o.Field("causeUnresolved").Contains("began after the detection")), Is.True); }
        }
        [Test]
        public void ABoundCycleIsNotMovedByTheAlarmsLaterLife()
        {
            DirectorReadSnapshot snapshot = null; var obs = new List<Observation>(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 14.1, Page(HotAlarm())), Read("commands", 14.1, Page(Derate(Cause()))) });
            adapter.Update(Inputs(14.1), obs.Add, _ => { });
            var keys = obs.Where(o => o.Kind == "command").Select(o => o.EventKey).ToArray(); Assert.That(keys.Length, Is.EqualTo(3));
            // The alarm clears and is raised again; the command still belongs to the cycle its cause named.
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 14.6, Page(HotAlarm(raised: 14.5))), Read("commands", 14.6, Page(Derate(Cause()))) });
            adapter.Update(Inputs(14.6), obs.Add, _ => { });
            Assert.That(obs.Where(o => o.Kind == "command").Select(o => o.EventKey), Is.EqualTo(keys));
        }
        // Page 3b, from the platform's records to the caption: "The platform tells it to derate." needs the recorded cause.
        static ObservationStore DerateStore(string cause, out Observation trigger)
        {
            var store = new ObservationStore();
            foreach (var o in Observe(14.1, Read("alarms", 14.1, Page(HotAlarm())), Read("commands", 14.1, Page(Derate(cause))))) store.Add(o);
            trigger = store.Records.Single(o => o.Kind == "alarm" && o.Status == "RAISED");
            foreach (var stream in new[] { "alarms", "pump", "measurements" }) store.AddCoverage(new CoverageInterval("run", stream, 6, 14.1, false));
            return store;
        }
        static bool Holds(ObservationStore store, Observation trigger, string caption)
        {
            var page = DirectorKit.Story().Page("3b");
            return page.Captions.Single(c => c.Id == caption).Needs.Evaluate(store, page.Context("run", 490, trigger: trigger)).Holds;
        }
        [Test]
        public void DerateClaimHoldsWhenTheRecordedCauseBindsTheCommandToTheAlarm()
        {
            var store = DerateStore(Cause(), out var trigger);
            Assert.That(string.Join(" ", DirectorKit.Story().Page("3b").Captions.Single(c => c.Id == "derate").Lines), Is.EqualTo("Running hot. The platform tells it to derate."));
            Assert.That(Holds(store, trigger, "derate"), Is.True);
        }
        [TestCase("no-cause")]
        [TestCase("rule-cause")]
        [TestCase("other-device-record")]
        [TestCase("raised-after")]
        public void DerateClaimFallsBackWhenTheLinkIsAbsent(string scenario)
        {
            ObservationStore store; Observation trigger;
            switch (scenario) {
                case "no-cause": store = DerateStore(null, out trigger); break;
                case "rule-cause": store = DerateStore(Cause("RULE", alarmKey: null), out trigger); break;
                case "other-device-record": store = DerateStore(Cause(deviceId: "another-device-row"), out trigger); break;
                default: store = DerateStore(Cause(detectedAt: Ts(13.9)), out trigger); break;
            }
            // The command succeeded and capped the charger; without the link, the page may not say the platform sent it for this alarm.
            Assert.That(store.Records.Any(o => o.Kind == "command" && o.Status == "SUCCESSFUL" && o.Field("max_kw") == "11"), Is.True);
            Assert.That(Holds(store, trigger, "derate"), Is.False);
            Assert.That(Holds(store, trigger, "cool"), Is.False);
            Assert.That(Holds(store, trigger, "hot-only"), Is.True);
            Assert.That(string.Join(" ", DirectorKit.Story().Page("3b").Captions.Single(c => c.Id == "hot-only").Lines), Is.EqualTo("Running hot. Alarm raised."));
        }
        [Test]
        public void TheDirectorAsksForTheCauseAndTheControlDoesNot()
        {
            foreach (var field in new[] { "kind", "ruleId", "ruleToken", "profileToken", "detectionKind", "detectedAt", "value", "alarmKey", "deviceId" })
                Assert.That(System.Text.RegularExpressions.Regex.IsMatch(HarborReadQueries.DirectorCommands, @"cause \{[^}]*\b" + field + @"\b[^}]*\}"), Is.True, field);
            Assert.That(HarborReadQueries.DirectorAlarms, Does.Contain(" originatorId "));
            Assert.That(HarborReadQueries.DirectorCommands, Does.Not.Contain("metadata"), "nothing writes an alarm token into metadata");
            Assert.That(HarborReadQueries.DirectorCommandsNoCause, Does.Not.Contain("cause"));
            Assert.That(HarborReadQueries.DirectorCommandsNoCause, Is.EqualTo(HarborReadQueries.DirectorCommands.Replace(" cause { kind ruleId ruleToken profileToken detectionKind detectedAt value alarmKey deviceId }", "")));
            HarborReadQueries.RequireReadOnly(HarborReadQueries.DirectorCommands); HarborReadQueries.RequireReadOnly(HarborReadQueries.DirectorCommandsNoCause);
        }
    }
}
