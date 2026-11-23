// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    public class DirectorTestsEvidence
    {
        [Test]
        public void IdentityIncludesRunDeviceKeyCycleCommandAttemptAndExactStatus()
        {
            var need = DirectorKit.Need("{\"attempt\":{\"device\":\"lamp\",\"key\":\"absence\",\"cycle\":\"raised-now\",\"command\":\"cmd\",\"attempt\":\"a1\",\"status\":\"SENT\",\"fields\":{\"code\":\"VERIFICATION_FAILED\"}}}");
            var c = new EvidenceContext("run", 16, 18, 700);
            foreach (var wrong in new[] { "run", "device", "key", "cycle", "command", "attempt", "status", "code", "source", "old", "future" })
            {
                var store = new ObservationStore();
                store.Add(DirectorKit.O("attempt", wrong == "device" ? "other" : "lamp", wrong == "key" ? "other" : "absence", wrong == "status" ? "QUEUED" : "SENT", wrong == "old" ? 15 : 17,
                    wrong == "future" ? 701 : 690, cycle: wrong == "cycle" ? "old" : "raised-now", command: wrong == "command" ? "old" : "cmd", attempt: wrong == "attempt" ? "old" : "a1", run: wrong == "run" ? "old" : "run",
                    fields: new Dictionary<string, string> { ["code"] = wrong == "code" ? "OTHER" : "VERIFICATION_FAILED" }, source: wrong == "source" ? EvidenceSource.Scenario : EvidenceSource.Platform));
                Assert.That(need.Evaluate(store, c).Holds, Is.False, wrong);
            }
            var good = new ObservationStore(); good.Add(DirectorKit.O("attempt", "lamp", "absence", "SENT", cycle: "raised-now", command: "cmd", attempt: "a1", fields: new Dictionary<string, string> { ["code"] = "VERIFICATION_FAILED" }));
            Assert.That(need.Evaluate(good, c).Holds, Is.True);
        }
        [Test]
        public void ReconnectDeduplicatesButNewCyclesAndStatusChangesSurvive()
        {
            var s = new ObservationStore(); var a = DirectorKit.O("alarm", "lamp", "silent", "RAISED", wall: 1, cycle: "first");
            Assert.That(s.Add(a), Is.True); Assert.That(s.Add(DirectorKit.O("alarm", "lamp", "silent", "RAISED", wall: 99, cycle: "first")), Is.True);
            Assert.That(s.Add(DirectorKit.O("alarm", "lamp", "silent", "RAISED", wall: 100, cycle: "second")), Is.True);
            Assert.That(s.Add(DirectorKit.O("alarm", "lamp", "silent", "CLEARED", wall: 101, cycle: "second")), Is.True); Assert.That(s.Records.Count, Is.EqualTo(4));
        }
        [Test]
        public void NoneAndExactlyNeedPositiveCoverageAndRejectGaps()
        {
            var n = DirectorKit.Need("{\"attempt\":{\"count\":0,\"stream\":\"notifications\",\"fromSim\":10.75,\"toSim\":11.5}}");
            var c = new EvidenceContext("run", 10, 12, 1000); var s = new ObservationStore(); Assert.That(n.Evaluate(s, c).Holds, Is.False);
            s.AddCoverage(new CoverageInterval("run", "notifications", 10, 12, false)); Assert.That(n.Evaluate(s, c).Holds, Is.True);
            s.AddCoverage(new CoverageInterval("run", "notifications", 11, 11.1, true)); Assert.That(n.Evaluate(s, c).Holds, Is.False);
            var exact = DirectorKit.Story().Page("6").Captions.First(x => x.Id == "exact").Needs;
            var context = new EvidenceContext("run", 19.75, 21.2, 1000, trigger: DirectorKit.O("alarm")); var store = new ObservationStore();
            store.Add(DirectorKit.O("membership", id: "east-absence", sim: 20, wall: 900, sets: new Dictionary<string, string[]> { ["tokens"] = new[] { "lamp" } }));
            store.Add(DirectorKit.O("alarm", "lamp", "light-silent", "RAISED", sim: 20.5, wall: 900));
            store.AddCoverage(new CoverageInterval("run", "alarms", 19, 22, false)); Assert.That(exact.Evaluate(store, context).Holds, Is.False);
            store = DirectorTimeline.Clean(); store.AddCoverage(new CoverageInterval("run", "alarms", 19.75, 20.55, false));
            context = new EvidenceContext("run", 19.75, 21.2, 1000, trigger: store.Records.First(o => o.Kind == "alarm" && o.Key == "light-silent"));
            Assert.That(exact.Evaluate(store, context).Holds, Is.True);
            store.Add(DirectorKit.O("alarm", "outside", "light-silent", "RAISED", sim: 20.5, wall: 901, cycle: "outage-cycle")); Assert.That(exact.Evaluate(store, context).Holds, Is.False);
        }
        [Test]
        public void AllBelowRequiresEveryContemporaneousReadingAndCoverageUntilRunning()
        {
            var need = DirectorKit.Story().Page("4").Captions.First(x => x.Id == "before-float").Needs;
            var s = new ObservationStore(); var c = new EvidenceContext("run", 16, 18.6, 1000, trigger: DirectorKit.O("alarm", "hp-pump-north", "wet-well-high", "RAISED"));
            s.Add(DirectorKit.O("chip", "hp-pump-north", status: "PLATFORM", wall: 692, fields: new Dictionary<string, string> { ["runningAt"] = "692", ["alarmToken"] = "record" }));
            s.Add(DirectorKit.O("measure", "hp-pump-north", "wet_well_m", fields: new Dictionary<string, string> { ["value"] = "2.6" }));
            Assert.That(need.Evaluate(s, c).Holds, Is.False); s.AddCoverage(new CoverageInterval("run", "pump", 17, 17.6, false)); Assert.That(need.Evaluate(s, c).Holds, Is.True);
            s.Add(DirectorKit.O("measure", "hp-pump-north", "wet_well_m", id: "high", wall: 691, fields: new Dictionary<string, string> { ["value"] = "3.2" }));
            Assert.That(need.Evaluate(s, c).Holds, Is.False);
        }
        [Test]
        public void PlatformClaimsCannotBorrowScenarioOrSenderProof()
        {
            var need = DirectorKit.Need("{\"measure\":{\"key\":\"power\",\"gte\":1,\"source\":\"DEVICE-SIDE\"}}");
            var s = new ObservationStore(); s.Add(DirectorKit.O("measure", key: "power", fields: new Dictionary<string, string> { ["value"] = "4" }, source: EvidenceSource.DeviceSide));
            Assert.That(need.Evaluate(s, new EvidenceContext("run", 16, 18, 1000)).Holds, Is.False);
            Assert.That(need.Evaluate(s, new EvidenceContext("run", 16, 18, 1000, EvidenceSource.DeviceSide)).Holds, Is.True);
        }
        [Test]
        public void PlaceholderNumberComesFromMatchingRecord()
        {
            var c = DirectorKit.Story().Page("8").Captions.First(x => x.Id == "ota-summary"); var s = new ObservationStore();
            s.Add(DirectorKit.O("rollout", status: "COUNTS", sim: 26.6, fields: new Dictionary<string, string> { ["updated"] = "5", ["refused"] = "1", ["maxInFlight"] = "3" }));
            Assert.That(c.Render(c.Needs.Evaluate(s, new EvidenceContext("run", 26, 27, 1000, trigger: DirectorKit.O("rollout")))), Does.Contain("limit 3"));
        }
        [TestCase("{\"mystery\":{}}")] [TestCase("{\"alarm\":{\"typo\":1}}")] [TestCase("{\"coverage\":{\"stream\":\"x\",\"fromSim\":1,\"toSim\":2,\"typo\":1}}")]
        [TestCase("{\"attempt\":{\"count\":0}}")] [TestCase("{\"measure\":{\"allBelow\":3}}")] [TestCase("{\"any\":[]}")]
        [TestCase("{\"alarm\":{\"fields\":{\"typo\":true}}}")] [TestCase("{\"measure\":{\"bind\":{\"n\":\"typo\"}}}")]
        [TestCase("{\"alarm\":{\"cycle\":\"$unknown.cycle\"}}")] [TestCase("{\"alarm\":{\"maxAgeS\":-1}}")]
        public void InvalidPredicateFailsLoudly(string json) => Assert.Throws<FormatException>(() => DirectorKit.Need(json));
        [Test]
        public void TriggerCycleReferenceRejectsPreviousCycleProof()
        {
            var need = DirectorKit.Story().Page("4").Captions.Single(c => c.Id == "platform-start").Needs;
            var store = new ObservationStore(); store.Add(DirectorKit.O("chip", "hp-pump-north", status: "PLATFORM", cycle: "old"));
            var context = new EvidenceContext("run", 16, 18.6, 1000, trigger: DirectorKit.O("alarm", cycle: "new"));
            Assert.That(need.Evaluate(store, context).Holds, Is.False);
            store.Add(DirectorKit.O("chip", "hp-pump-north", status: "PLATFORM", cycle: "new", fields: new Dictionary<string, string> { ["alarmToken"] = "record" })); Assert.That(need.Evaluate(store, context).Holds, Is.True);
        }

        [TestCase("2", "work-sent", "attempt", "alarmToken")]
        [TestCase("2", "work-sent", "attempt", "cycle")]
        [TestCase("1b", "peak-clear", "alarm", "identity")]
        [TestCase("3c", "dropped", "alarm", "identity")]
        [TestCase("3c", "back", "alarm", "identity")]
        [TestCase("3", "same", "measure", "identity")]
        [TestCase("4", "platform-start", "chip", "alarmToken")]
        [TestCase("8", "signed", "artifact", "identity")]
        [TestCase("8", "few", "target", "rolloutToken")]
        [TestCase("8", "working", "attempt", "attempt")]
        [TestCase("8", "ota-summary", "rollout", "cycle")]
        public void CommittedIdentityBindingRejectsWrongCausalRecord(string pageId, string captionId, string kind, string field)
        {
            var story = DirectorKit.Story(); var page = story.Page(pageId); var clean = DirectorTimeline.Clean();
            foreach (var stream in new[] { "alarms", "notifications", "credentials", "pump", "measurements" }) clean.AddCoverage(new CoverageInterval("run", stream, 6, 30, false));
            var trigger = page.Trigger.Evaluate(clean, page.Context("run", (page.ToSim - 6) * 60)).Records.Last();
            var context = page.Context("run", (page.ToSim - 6) * 60, trigger: trigger); var caption = page.Captions.Single(c => c.Id == captionId);
            Assert.That(caption.Needs.Evaluate(clean, context).Holds, Is.True, "green causal baseline");
            var wrong = new ObservationStore();
            foreach (var o in clean.Records) {
                var fields = o.Fields.ToDictionary(v => v.Key, v => v.Value);
                if (o.Kind == kind && fields.ContainsKey(field)) fields[field] = "unrelated";
                wrong.Add(new Observation(o.Run, o.Kind, o.Kind == kind && field == "identity" ? "unrelated" : o.Identity, o.Device, o.Key,
                    o.Kind == kind && field == "cycle" ? "wrong-cycle" : o.Cycle, o.Command, o.Kind == kind && field == "attempt" ? "wrong-attempt" : o.Attempt,
                    o.Status, o.ObservedAtS, o.OccurredAtS, o.Sim, o.Source, fields, o.Sets));
            }
            foreach (var coverage in clean.Coverage) wrong.AddCoverage(coverage);
            Assert.That(caption.Needs.Evaluate(wrong, context).Holds, Is.False);
        }
        [Test]
        public void CurrentSnapshotsRefreshWithoutReconsumingImmutableEvents()
        {
            var store = new ObservationStore(); var first = DirectorKit.O("window", status: "ACTIVE", wall: 10);
            store.Add(first); store.Add(DirectorKit.O("window", status: "ACTIVE", wall: 20, fields: new Dictionary<string, string> { ["value"] = "new" }));
            var current = DirectorKit.Need("{\"window\":{\"mode\":\"current\",\"status\":\"ACTIVE\",\"maxAgeS\":5}}");
            Assert.That(current.Evaluate(store, new EvidenceContext("run", 17, 18, 24)).Holds, Is.True);
            store.Add(DirectorKit.O("window", status: "ENDED", wall: 25));
            Assert.That(current.Evaluate(store, new EvidenceContext("run", 17, 18, 26)).Holds, Is.False);
            Assert.That(DirectorKit.Need("{\"window\":{\"mode\":\"occurred\",\"status\":\"ACTIVE\"}}").Evaluate(store, new EvidenceContext("run", 17, 18, 26)).Holds, Is.True);
            Assert.That(first.EventKey, Is.EqualTo(DirectorKit.O("window", status: "ACTIVE", wall: 99).EventKey));
            Assert.That(store.Records.Count, Is.EqualTo(3));
        }
        [Test]
        public void SetClaimsRequireMembersFieldsIntervalsAndChildConstraints()
        {
            var need = DirectorKit.Story().Page("7").Captions.Single(c => c.Id == "all-buffered").Needs;
            var store = new ObservationStore(); store.AddCoverage(new CoverageInterval("run", "measurements", 19.75, 21.6, false));
            store.Add(DirectorKit.O("ledger", key: "backfill", sim: 21.3, source: EvidenceSource.Scenario));
            var context = new EvidenceContext("run", 21.25, 22.1, 2000);
            Assert.That(need.Evaluate(store, context).Holds, Is.False);
            store.Add(DirectorKit.O("measure", key: "backfill", sim: 21.3, fields: new Dictionary<string, string> { ["readingIdentity"] = "one" }));
            Assert.That(need.Evaluate(store, context).Holds, Is.False, "missing expected field"); Assert.That(need.Evaluate(store, context).Error, Does.Contain("missing required set field"));
            var child = DirectorKit.Need("{\"set\":{\"observed\":{\"measure\":{\"gte\":2}},\"expected\":{\"membership\":{}},\"field\":\"device\",\"expectedField\":\"tokens\",\"stream\":\"measurements\",\"fromSim\":17,\"toSim\":18}}");
            store = new ObservationStore(); store.AddCoverage(new CoverageInterval("run", "measurements", 17, 18, false));
            store.Add(DirectorKit.O("membership", sets: new Dictionary<string, string[]> { ["tokens"] = new[] { "gauge" } }));
            store.Add(DirectorKit.O("measure", "gauge", fields: new Dictionary<string, string> { ["value"] = "1" }));
            Assert.That(child.Evaluate(store, new EvidenceContext("run", 16, 20, 2000)).Holds, Is.False);
            store.Add(DirectorKit.O("measure", "gauge", wall: 700, sim: 19, fields: new Dictionary<string, string> { ["value"] = "3" }));
            Assert.That(child.Evaluate(store, new EvidenceContext("run", 16, 20, 2000)).Holds, Is.False, "outer interval excludes the qualifying row");
        }
        [Test]
        public void FloatProofCoversTheExactTransitionBeyondOldEstimate()
        {
            var clean = DirectorTimeline.Clean(); var store = new ObservationStore();
            var trigger = clean.Records.Single(o => o.Kind == "alarm" && o.Key == "wet-well-high");
            foreach (var o in clean.Records.Where(o => o.Kind != "chip")) store.Add(o);
            var chip = clean.Records.Single(o => o.Kind == "chip");
            var fields = chip.Fields.ToDictionary(v => v.Key, v => v.Value); fields["runningAt"] = "714";
            store.Add(new Observation(chip.Run, chip.Kind, chip.Identity, chip.Device, chip.Key, chip.Cycle, chip.Command, chip.Attempt, chip.Status, 714, 714, 17.9, chip.Source, fields));
            var need = DirectorKit.Story().Page("4").Captions.Single(c => c.Id == "before-float").Needs; var context = new EvidenceContext("run", 16, 18.6, 1000, trigger: trigger);
            store.AddCoverage(new CoverageInterval("run", "pump", 17, 17.6, false)); Assert.That(need.Evaluate(store, context).Holds, Is.False);
            store.AddCoverage(new CoverageInterval("run", "pump", 17.6, 17.9, false)); Assert.That(need.Evaluate(store, context).Holds, Is.True);
            store.Add(DirectorKit.O("measure", chip.Device, "wet_well_m", sim: 17.8, wall: 708, cycle: chip.Cycle, fields: new Dictionary<string, string> { ["value"] = "3.2" }));
            Assert.That(need.Evaluate(store, context).Holds, Is.False);
        }
        [TestCase("{\"alarm\":{\"status\":\"RAISED\",\"state\":\"CLEARED\"}}")]
        [TestCase("{\"measure\":{\"until\":\"$match.running.runningAt\"}}")]
        [TestCase("{\"measure\":{\"count\":0,\"gte\":2,\"stream\":\"x\",\"fromSim\":1,\"toSim\":2}}")]
        public void UnsupportedPredicateCombinationsAreRefused(string json) => Assert.Throws<FormatException>(() => DirectorKit.Need(json));
    }
}
