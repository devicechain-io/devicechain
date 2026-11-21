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
            // (the read is three seconds old, so it is a healthy one: a newer one is on its way)
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 13.99, Page(HotAlarm(raised: 13.5))), Read("commands", 14.04, Page(Derate(Cause(), "QUEUED"))) });
            adapter.Update(Inputs(14.04), obs.Add, _ => { });
            Assert.That(obs.Any(o => o.Kind == "command"), Is.False);
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 14.11, Page(HotAlarm(raised: 13.5))), Read("commands", 14.11, Page(Derate(Cause()))) });
            adapter.Update(Inputs(14.11), obs.Add, _ => { });
            Assert.That(obs.Where(o => o.Kind == "command").All(o => o.Field("alarmToken") == "alarm-hot" && o.Cycle == Ts(13.5)), Is.True);
            Assert.That(obs.Count(o => o.Kind == "command"), Is.EqualTo(3));
        }
        [Test]
        public void WithNoAlarmReadAtAllTheCommandIsStillEvidenceAndIsTiedWhenTheReadsComeBack()
        {
            // An alarm read that fails must not make a command that was read disappear: there is nothing to wait for.
            DirectorReadSnapshot snapshot = null; var obs = new List<Observation>(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 14.1, Page(HotAlarm()), error: "HTTP 503"), Read("commands", 14.1, Page(Derate(Cause()))) });
            adapter.Update(Inputs(14.1), obs.Add, _ => { });
            AssertUnlinked(obs); Assert.That(obs.Where(o => o.Kind == "command").All(o => o.Field("causeUnresolved").Contains("no alarm read")), Is.True);
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 14.15, Page(HotAlarm())), Read("commands", 14.15, Page(Derate(Cause()))) });
            adapter.Update(Inputs(14.15), obs.Add, _ => { });
            var tied = obs.Where(o => o.Kind == "command" && o.Field("alarmToken") == "alarm-hot").ToArray();
            Assert.That(tied.Select(o => o.Status), Is.EquivalentTo(new[] { "QUEUED", "SENT", "SUCCESSFUL" })); Assert.That(tied.All(o => o.Cycle == Ts(14) && o.Field("causeUnresolved") == null), Is.True);
        }
        [TestCase("no-kind", "no kind")]
        [TestCase("no-detected-at", "cannot be read")]
        [TestCase("bad-detected-at", "cannot be read")]
        public void ACauseThatCannotBeReadCostsTheLinkNotTheCommand(string fault, string why)
        {
            var cause = fault == "no-kind" ? Cause().Replace("\"kind\":\"ALARM\",", "") : fault == "no-detected-at" ? Cause().Replace("\"detectedAt\":" + Q(Ts(14)) + ",", "") : Cause(detectedAt: "yesterday");
            var snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 14.1, Page(HotAlarm())), Read("commands", 14.1, Page(Derate(cause))) });
            var obs = new List<Observation>(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
            adapter.Update(Inputs(14.1), obs.Add, _ => { });
            AssertUnlinked(obs);
            Assert.That(obs.Where(o => o.Kind == "command").All(o => o.Field("causeUnresolved").Contains(why)), Is.True);
            Assert.That(adapter.Problems.Any(p => p.StartsWith("commands: ", StringComparison.Ordinal)), Is.True, "and it is reported");
        }
        [Test]
        public void ACauseIsResolvedOncePerAlarmReadNotOncePerFrame()
        {
            DirectorReadSnapshot snapshot = null; var obs = new List<Observation>(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
            // unresolved (the alarm's cycle began after the detection): the same reads, frame after frame, resolve it once
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 14.1, Page(HotAlarm(raised: 14.05))), Read("commands", 14.1, Page(Derate(Cause()))) });
            for (var i = 0; i < 30; i++) adapter.Update(Inputs(14.1 + i / 6000.0), obs.Add, _ => { });
            Assert.That(adapter.CauseResolutions, Is.EqualTo(1));
            Assert.That(obs.Count(o => o.Kind == "command"), Is.EqualTo(3)); AssertUnlinked(obs);
            // waiting (the alarm read is older than the detection): the same
            var waiting = new DirectorObserver(Kit.Layout(), () => snapshot); var none = new List<Observation>();
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 13.99, Page(HotAlarm())), Read("commands", 14.04, Page(Derate(Cause()))) });
            for (var i = 0; i < 30; i++) waiting.Update(Inputs(14.04 + i / 6000.0), none.Add, _ => { });
            Assert.That(waiting.CauseResolutions, Is.EqualTo(1)); Assert.That(none.Any(o => o.Kind == "command"), Is.False);
            // a newer alarm read is a reason to try again, and a bound command is never tried again
            snapshot = new DirectorReadSnapshot(new[] { Read("alarms", 14.2, Page(HotAlarm())), Read("commands", 14.2, Page(Derate(Cause()))) });
            for (var i = 0; i < 30; i++) waiting.Update(Inputs(14.2 + i / 6000.0), none.Add, _ => { });
            Assert.That(waiting.CauseResolutions, Is.EqualTo(2));
            Assert.That(none.Where(o => o.Kind == "command").All(o => o.Field("alarmToken") == "alarm-hot"), Is.True); Assert.That(none.Count(o => o.Kind == "command"), Is.EqualTo(3));
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
        // Page 3b on a day's real timing. The rule averages a minute of readings, so the alarm is raised some forty seconds after the
        // overheating starts and the scene sees it (and the command, a moment later) about 45 s after the page is staged at 14:00: in
        // the page's SECOND shot. U7's storyboard offered the derate caption for 40 s from the first shot's start, so on a live day it
        // had expired before there was anything to say, and the page said "Alarm raised." whatever the platform had recorded.
        static string[] Page3bCaptions(bool linked, double commandLagS, string story = null)
        {
            var layout = Kit.Layout(); var scheduler = new Scheduler(Storyboard.Parse(story ?? DirectorKit.StoryText(), new PresentationMap(layout)), layout);
            double Sim(double wall) => 6 + wall * 24 / layout.DayLengthSeconds;
            const double seen = 524, cleared = 566; // 14:44 and 15:26
            var alarm = new Dictionary<string, string> { ["alarmToken"] = "alarm-hot" };
            var command = new Dictionary<string, string> { ["max_kw"] = "11", ["cause"] = linked ? "ALARM" : "RULE" }; if (linked) command["alarmToken"] = "alarm-hot";
            var source = new[] {
                DirectorKit.O("alarm", Charger, Hot, "RAISED", Sim(seen), seen, "alarm-hot", "cycle-hot", fields: alarm, occurred: seen - 9),
                DirectorKit.O("command", Charger, "derate", "SUCCESSFUL", Sim(seen + commandLagS), seen + commandLagS, "derate-real", linked ? "cycle-hot" : "", fields: command, occurred: seen - .2, command: "derate-real"),
                DirectorKit.O("measure", Charger, "max_kw", "", Sim(seen + 6), seen + 6, "cap-reading", "cycle-hot", fields: new Dictionary<string, string> { ["value"] = "11" }),
                DirectorKit.O("alarm", Charger, Hot, "CLEARED", Sim(cleared), cleared, "alarm-hot", "cycle-hot", fields: alarm, occurred: cleared - 7),
            };
            var store = new ObservationStore();
            for (var wall = 474; wall <= 600; wall++) {
                foreach (var o in source.Where(o => o.ObservedAtS <= wall)) store.Add(o);
                foreach (var stream in new[] { "alarms", "notifications", "credentials", "pump", "measurements" }) store.AddCoverage(new CoverageInterval("run", stream, 6, Sim(wall), false));
                scheduler.Tick(new DirectorSample("run", Sim(wall), wall), store);
            }
            Assert.That(scheduler.Log.Where(e => e.Kind == "shot" && e.Page == "3b").Select(e => e.Value), Is.EqualTo(new[] { "3b-1", "3b-2", "3b-3" }));
            return scheduler.Log.Where(e => e.Kind == "caption" && e.Page == "3b").Select(e => e.Value.Replace("\n", " ")).ToArray();
        }
        const string Derates = "Running hot. The platform tells it to derate.", Raised = "Running hot. Alarm raised.", Cooler = "Cooler now. Alarm cleared.";
        [Test]
        public void OnADaysRealTimingTheDerateCaptionShowsWhenTheCommandIsTiedToTheAlarm()
        {
            // the command is read a moment after the alarm: "alarm raised" first, then the stronger statement, then the outcome
            Assert.That(Page3bCaptions(true, 1), Is.EqualTo(new[] { Raised, Derates, Cooler }));
            // read in the same frame: the stronger statement at once, and the weaker one never after it
            Assert.That(Page3bCaptions(true, 0), Is.EqualTo(new[] { Derates, Cooler }));
            // a few seconds late (a slow poll): still shown
            Assert.That(Page3bCaptions(true, 5), Is.EqualTo(new[] { Raised, Derates, Cooler }));
        }
        [Test]
        public void OnADaysRealTimingThePageKeepsToAlarmRaisedWhenTheLinkIsAbsent()
        {
            // the same command with no alarm named: never "the platform tells it", never "cooler now" (which needs the same command)
            var shown = Page3bCaptions(false, 1);
            Assert.That(shown, Is.Not.Empty); Assert.That(shown.Distinct(), Is.EqualTo(new[] { Raised }));
        }
        [Test]
        public void TheSameDayUnderU7sStoryboardNeverSaidThePlatformDerated()
        {
            // U7's page: the derate caption offered for 40 s, and "alarm raised" free to follow it
            var u7 = DirectorKit.StoryText().Replace("\r\n", "\n");
            var first = u7.IndexOf("\"id\": \"3b-1\"", StringComparison.Ordinal); var max = u7.IndexOf("\"holdMaxS\": 80", first, StringComparison.Ordinal);
            Assert.That(first > 0 && max > first && max - first < 400, Is.True, "the first shot of page 3b holds its caption's offer for 80 s");
            u7 = u7.Substring(0, max) + "\"holdMaxS\": 40" + u7.Substring(max + "\"holdMaxS\": 80".Length);
            Assert.That(Page3bCaptions(true, 1, u7), Does.Not.Contain(Derates), "the check sees what a live day showed");
        }
        [Test]
        public void UnlessShownMustNameAnotherClaimOfThePage()
        {
            var story = DirectorKit.StoryText(); var map = new PresentationMap(Kit.Layout());
            Assert.That(DirectorKit.Story().Page("3b").Captions.Single(c => c.Id == "hot-only").UnlessShown, Is.EqualTo("derate"));
            Assert.That(DirectorKit.Story().Pages.SelectMany(p => p.Captions).Count(c => c.UnlessShown != null), Is.EqualTo(1), "one caption uses it");
            foreach (var bad in new[] { "hot-only", "no-such-caption", "" })
                Assert.Throws<FormatException>(() => Storyboard.Parse(story.Replace("\"unlessShown\": \"derate\"", "\"unlessShown\": \"" + bad + "\""), map), bad);
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
