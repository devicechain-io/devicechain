// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>
    /// What a live day did that the scripted timeline never does (the walkthrough recorded on 2026-11-24): a poll's success is
    /// stamped by the system clock a few milliseconds after the frame's own "now", and a snapshot is read again every poll, so
    /// its instant keeps moving past the interval a claim was authored for.
    /// </summary>
    public partial class DirectorTestsObserver
    {
        static DirectorReadSnapshot CredentialReads(DateTimeOffset success) => new DirectorReadSnapshot(new[] {
            new DirectorRead("credentials", "test", DirectorProbe.PROBE_OK, success, null, Json("{" + Q(DirectorProbeEvaluation.Roots["credentials"]) + ":[" + Credential() + "]}"), new[] { new ReadSpan(Epoch, success, false) }),
            new DirectorRead("replacements", "test", DirectorProbe.PROBE_OK, success, null, Json("{" + Q(DirectorProbeEvaluation.Roots["replacements"]) + ":" + Page(Journal()) + "}"), new[] { new ReadSpan(Epoch, success, false) }) });

        [Test]
        public void APollStampedMillisecondsAfterTheFramesNowOpensNoGap()
        {
            // every completed poll looked like this to the next frame, which made it a one-frame gap in its stream every three seconds
            var snapshot = CredentialReads(At(12.7).AddMilliseconds(8));
            var coverage = new List<CoverageInterval>(); var seen = new List<Observation>(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
            adapter.Update(Inputs(12.69), seen.Add, coverage.Add); coverage.Clear();
            adapter.Update(Inputs(12.7), seen.Add, coverage.Add);
            Assert.That(coverage.Where(c => c.Stream == "credentials" && c.Gap), Is.Empty, "a success eight milliseconds ahead of the frame is a clock difference, not an outage");
            Assert.That(coverage.Any(c => c.Stream == "credentials" && !c.Gap && c.ToSim == 12.7), Is.True);
            Assert.That(seen.Any(o => o.Kind == "credential" && o.Status == "DISABLED"), Is.True);
        }

        [Test]
        public void AReadFromTheFutureOrTooOldIsStillAGap()
        {
            foreach (var offsetS in new[] { 2.0, -7.0 })
            {
                var snapshot = CredentialReads(At(12.7).AddSeconds(offsetS));
                var coverage = new List<CoverageInterval>(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
                adapter.Update(Inputs(12.69), _ => { }, coverage.Add); coverage.Clear();
                adapter.Update(Inputs(12.7), _ => { }, coverage.Add);
                Assert.That(coverage.Any(c => c.Stream == "credentials" && c.Gap), Is.True, offsetS + " s");
                Assert.That(coverage.Any(c => c.Stream == "credentials" && !c.Gap), Is.False, offsetS + " s");
            }
            Assert.That(DirectorRead.ClockSlack, Is.LessThanOrEqualTo(TimeSpan.FromSeconds(1)), "the slack covers a clock difference, never a poll interval (3 s)");
        }

        [Test]
        public void TheDisabledCredentialsClaimHoldsOnALaterPollOfTheSameCredentials()
        {
            // The live page asks at 12:44 (its last shot's cue is 12:42); the credentials are read again every three seconds, and
            // a read after 12:45, the end of the claim's interval, used to take the claim away for good.
            var page = DirectorKit.Story().Page("3"); var claim = page.Captions.Single(c => c.Id == "disabled").Needs;
            DirectorReadSnapshot snapshot = null; var store = new ObservationStore(); var adapter = new DirectorObserver(Kit.Layout(), () => snapshot);
            foreach (var readAt in new[] { 12.6, 12.74, 12.76, 12.95 })
            {
                snapshot = new DirectorReadSnapshot(new[] { Read("credentials", readAt, "[" + Credential() + "]"), Read("replacements", readAt, Page(Journal())),
                    Read("measurements", readAt, Page(Measure("before", 12.45), Measure("after", 12.55)), "hp-gauge-north-3") });
                adapter.Update(Inputs(readAt), o => store.Add(o), _ => { });
                store.AddCoverage(new CoverageInterval("run", "credentials", 12.5, readAt, false));
                var trigger = store.Records.First(o => o.Kind == "replacement" && o.Status == "RECORDED");
                var holds = claim.Evaluate(store, new EvidenceContext("run", page.FromSim, page.ToSim, (readAt - 6) * 60, trigger: trigger, nowSim: readAt)).Holds;
                Assert.That(holds, Is.EqualTo(readAt >= 12.75), "read at " + readAt + ": the claim needs its interval covered, and then holds whenever the credentials are next read");
            }
            // CONTROL: the newer read must still say what the claim says
            var enabled = new DirectorReadSnapshot(new[] { Read("credentials", 12.9, "[" + Credential("true") + "]"), Read("replacements", 12.9, Page(Journal())),
                Read("measurements", 12.9, Page(Measure("before", 12.45), Measure("after", 12.55)), "hp-gauge-north-3") });
            var control = new ObservationStore(); new DirectorObserver(Kit.Layout(), () => enabled).Update(Inputs(12.9), o => control.Add(o), _ => { });
            control.AddCoverage(new CoverageInterval("run", "credentials", 12.5, 12.9, false));
            Assert.That(claim.Evaluate(control, new EvidenceContext("run", page.FromSim, page.ToSim, (12.9 - 6) * 60, trigger: control.Records.First(o => o.Kind == "replacement" && o.Status == "RECORDED"), nowSim: 12.9)).Holds, Is.False);
            // and a snapshot read before the interval began is no evidence about it
            var early = new ObservationStore();
            early.Add(DirectorKit.O("membership", id: "east-absence", sim: 19.5, wall: 810, sets: new Dictionary<string, string[]> { ["tokens"] = new[] { "lamp" } }));
            early.Add(DirectorKit.O("alarm", "lamp", "light-silent", "RAISED", sim: 20.4, wall: 864));
            early.AddCoverage(new CoverageInterval("run", "alarms", 19, 22, false));
            var exact = DirectorKit.Need("{\"set\":{\"observed\":{\"alarm\":{\"key\":\"light-silent\",\"status\":\"RAISED\",\"mode\":\"occurred\"}},\"expected\":{\"membership\":{\"identity\":\"east-absence\",\"mode\":\"current\"}},\"field\":\"device\",\"expectedField\":\"tokens\",\"stream\":\"alarms\",\"fromSim\":19.75,\"toSim\":20.55}}");
            Assert.That(exact.Evaluate(early, new EvidenceContext("run", 19.75, 21.2, 900)).Holds, Is.False);
            early.Add(DirectorKit.O("membership", id: "east-absence", sim: 20.7, wall: 882, sets: new Dictionary<string, string[]> { ["tokens"] = new[] { "lamp" } }));
            Assert.That(exact.Evaluate(early, new EvidenceContext("run", 19.75, 21.2, 900)).Holds, Is.True, "the membership read after the tally closed is the newest read of it");
        }

        // The platform's alarms are read a few seconds after they are raised. "Exactly the six that went quiet" was recorded on a live day
        // with all six alarms occurred inside its interval (sim 20.42, interval to 20.55) and first seen at sim 20.67; bounding a set's
        // records by the poll that saw them dropped all six and the claim could never hold. A record is placed by when it happened.
        [Test]
        public void ASetPlacesAnAlarmByWhenItHappenedNotByWhenAPollFirstSawIt()
        {
            var exact = DirectorKit.Need("{\"set\":{\"observed\":{\"alarm\":{\"key\":\"light-silent\",\"status\":\"RAISED\",\"mode\":\"occurred\"}},\"expected\":{\"membership\":{\"identity\":\"east-absence\",\"mode\":\"current\"}},\"field\":\"device\",\"expectedField\":\"tokens\",\"stream\":\"alarms\",\"fromSim\":19.75,\"toSim\":20.55}}");
            var store = new ObservationStore();
            store.Add(DirectorKit.O("membership", id: "east-absence", sim: 20.7, wall: 882, sets: new Dictionary<string, string[]> { ["tokens"] = new[] { "lamp" } }));
            store.Add(DirectorKit.O("alarm", "lamp", "light-silent", "RAISED", sim: 20.42, wall: 880, occurred: 864));
            store.AddCoverage(new CoverageInterval("run", "alarms", 19, 22, false));
            var late = new EvidenceContext("run", 19.75, 21.2, 900, nowSim: 21.0);
            Assert.That(exact.Evaluate(store, late).Holds, Is.True, "occurred inside the interval, first read after its end");
            // CONTROL: one that happened after the interval's end is still outside it, however it was read
            var after = new ObservationStore();
            after.Add(DirectorKit.O("membership", id: "east-absence", sim: 20.7, wall: 882, sets: new Dictionary<string, string[]> { ["tokens"] = new[] { "lamp" } }));
            after.Add(DirectorKit.O("alarm", "lamp", "light-silent", "RAISED", sim: 20.62, wall: 880, occurred: 877));
            after.AddCoverage(new CoverageInterval("run", "alarms", 19, 22, false));
            Assert.That(exact.Evaluate(after, late).Holds, Is.False, "an alarm that happened after the interval is not in it");
        }

        // The signed rollout as the live day ran it: one charger's attempt takes about 35 wall seconds, so the first target succeeds at
        // 02:38 (the scripted timeline said 02:09) and six chargers, two at a time, finish near 03:50 (scripted: 02:36).
        static Scheduler RolloutNight(string storyText)
        {
            var layout = Kit.Layout(); var scheduler = new Scheduler(Storyboard.Parse(storyText, new PresentationMap(layout)), layout); var store = new ObservationStore();
            Observation At(Observation o, double sim) => new Observation(o.Run, o.Kind, o.Identity, o.Device, o.Key, o.Cycle, o.Command, o.Attempt, o.Status, (sim - 6) * 60, (sim - 6) * 60, sim, o.Source,
                o.Fields.ToDictionary(f => f.Key, f => f.Value), o.Sets.ToDictionary(f => f.Key, f => f.Value));
            var night = DirectorTimeline.Clean().Records.Select(o => o.Kind == "target" && o.Status == "SUCCEEDED" ? At(o, 26.63) : o.Kind == "rollout" && o.Status != "RUNNING" ? At(o, 27.83) : o).OrderBy(o => o.ObservedAtS).ToArray();
            for (var wall = 1140; wall <= 1500; wall++)
            {
                var sim = 6 + wall / 60.0;
                foreach (var o in night.Where(o => o.ObservedAtS <= wall)) store.Add(o);
                foreach (var stream in new[] { "alarms", "notifications", "credentials", "pump", "measurements", "updates" }) store.AddCoverage(new CoverageInterval("run", stream, 6, sim, false));
                scheduler.Tick(new DirectorSample("run", sim, wall), store);
            }
            return scheduler;
        }
        static string[] Captions(Scheduler s, string page) => s.Log.Where(e => e.Kind == "caption" && e.Page == page).Select(e => e.Value.Replace("\n", " / ")).ToArray();

        [Test]
        public void TheRolloutPageWaitsForARealRolloutAndTheEndCardFollowsIt()
        {
            var s = RolloutNight(DirectorKit.StoryText()); var captions = Captions(s, "8");
            Assert.That(captions, Does.Contain("A few chargers at a time."));
            Assert.That(captions, Does.Contain("Rollout done. Nothing hidden."));
            Assert.That(captions, Does.Contain("5 updated · 1 refused / concurrency limit 2"));
            Assert.That(Array.IndexOf(captions, "A few chargers at a time."), Is.LessThan(Array.IndexOf(captions, "Rollout done. Nothing hidden.")));
            var done = s.Log.First(e => e.Kind == "caption" && e.Value.StartsWith("Rollout done", StringComparison.Ordinal));
            Assert.That(done.ShowS, Is.GreaterThanOrEqualTo((27.83 - 6) * 60), "the claim waits for the rollout's own end");
            Assert.That(s.Log.Any(e => e.Kind == "missed"), Is.False);
            Assert.That(Captions(s, "9"), Does.Contain("Agents read the fleet. / People approve the action."), "the end card is not lost while the rollout runs");
            Assert.That(s.Log.First(e => e.Kind == "shot" && e.Page == "9").ShowS, Is.GreaterThan(done.ShowS));

            // CONTROL: the page as it was authored against the scripted rollout loses all three on the same night.
            var story = DirectorKit.Object(DirectorKit.StoryText()); var page = DirectorKit.Obj(DirectorKit.Arr(story["pages"]).First(p => (string)DirectorKit.Obj(p)["id"] == "8"));
            page["windowSim"] = new List<object> { 26.0, 27.0 };
            foreach (var shot in DirectorKit.Arr(page["shots"]).Select(DirectorKit.Obj))
            {
                if ((string)shot["id"] == "8b") shot["captions"] = new List<object> { "few" };
                if ((string)shot["id"] == "8d") { shot["captions"] = new List<object> { "done", "ota-summary" }; shot["until"] = null; shot["holdMaxS"] = 11.0; }
            }
            var before = Captions(RolloutNight(DirectorKit.Json(story)), "8");
            Assert.That(before, Does.Not.Contain("A few chargers at a time.")); Assert.That(before, Does.Not.Contain("Rollout done. Nothing hidden."));
            Assert.That(before.Any(c => c.Contains("updated")), Is.False);
        }
    }
}
