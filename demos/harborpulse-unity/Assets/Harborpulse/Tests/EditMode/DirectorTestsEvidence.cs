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
            Assert.That(s.Add(a), Is.True); Assert.That(s.Add(DirectorKit.O("alarm", "lamp", "silent", "RAISED", wall: 99, cycle: "first")), Is.False);
            Assert.That(s.Add(DirectorKit.O("alarm", "lamp", "silent", "RAISED", wall: 100, cycle: "second")), Is.True);
            Assert.That(s.Add(DirectorKit.O("alarm", "lamp", "silent", "CLEARED", wall: 101, cycle: "second")), Is.True); Assert.That(s.Records.Count, Is.EqualTo(3));
        }
        [Test]
        public void NoneAndExactlyNeedPositiveCoverageAndRejectGaps()
        {
            var n = DirectorKit.Need("{\"attempt\":{\"count\":0,\"stream\":\"notifications\",\"fromSim\":10.75,\"toSim\":11.5}}");
            var c = new EvidenceContext("run", 10, 12, 1000); var s = new ObservationStore(); Assert.That(n.Evaluate(s, c).Holds, Is.False);
            s.AddCoverage(new CoverageInterval("run", "notifications", 10, 12, false)); Assert.That(n.Evaluate(s, c).Holds, Is.True);
            s.AddCoverage(new CoverageInterval("run", "notifications", 11, 11.1, true)); Assert.That(n.Evaluate(s, c).Holds, Is.False);
            var exact = DirectorKit.Story().Page("6").Captions.First(x => x.Id == "exact").Needs;
            var context = new EvidenceContext("run", 19.75, 21.2, 1000); var store = new ObservationStore();
            store.Add(DirectorKit.O("membership", id: "east-absence", sim: 20, wall: 900, sets: new Dictionary<string, string[]> { ["tokens"] = new[] { "lamp" } }));
            store.Add(DirectorKit.O("alarm", "lamp", "light-silent", "RAISED", sim: 20.5, wall: 900));
            store.AddCoverage(new CoverageInterval("run", "alarms", 19, 22, false)); Assert.That(exact.Evaluate(store, context).Holds, Is.True);
            store.Add(DirectorKit.O("alarm", "outside", "light-silent", "RAISED", sim: 20.5, wall: 901)); Assert.That(exact.Evaluate(store, context).Holds, Is.False);
        }
        [Test]
        public void AllBelowRequiresEveryContemporaneousReadingAndCoverageUntilRunning()
        {
            var need = DirectorKit.Story().Page("4").Captions.First(x => x.Id == "before-float").Needs;
            var s = new ObservationStore(); var c = new EvidenceContext("run", 16, 18.6, 1000, trigger: DirectorKit.O("alarm", "hp-pump-north", "wet-well-high", "RAISED"));
            s.Add(DirectorKit.O("chip", "hp-pump-north", status: "PLATFORM", fields: new Dictionary<string, string> { ["runningAt"] = "692" }));
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
            s.Add(DirectorKit.O("rollout", status: "TERMINAL", sim: 26.6, fields: new Dictionary<string, string> { ["updated"] = "5", ["refused"] = "1", ["maxInFlight"] = "3" }));
            Assert.That(c.Render(c.Needs.Evaluate(s, new EvidenceContext("run", 26, 27, 1000))), Does.Contain("limit 3"));
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
            store.Add(DirectorKit.O("chip", "hp-pump-north", status: "PLATFORM", cycle: "new")); Assert.That(need.Evaluate(store, context).Holds, Is.True);
        }
    }
}
