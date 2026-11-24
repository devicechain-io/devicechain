// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    public class DirectorTestsStoryboard
    {
        [Test]
        public void CommittedStoryboardPassesTimingAndClaimLint()
        {
            var story = DirectorKit.Story(); Assert.That(story.Pages.Select(p => p.Id), Is.EqualTo(new[] { "0", "1", "1b", "2", "3", "3c", "3b", "4", "5", "4b", "6", "7", "8", "9" }));
            Assert.That(story.Cuts.Single(c => c.Id == "c90").Segments.Select(s => s.Page).Distinct(), Is.EqualTo(new[] { "0", "1", "4", "6", "7", "8", "9" }));
            foreach (var cut in story.Cuts) { Assert.That(cut.Segments.Sum(s => s.DurS), Is.EqualTo(cut.TargetS).Within(0.5)); foreach (var segment in cut.Segments) foreach (var c in segment.Captions) Assert.That(c.HoldS, Is.GreaterThanOrEqualTo(3)); }
        }
        static Dictionary<string, object> First(Dictionary<string, object> root, string key) => DirectorKit.Obj(DirectorKit.Arr(root[key])[0]);
        static Dictionary<string, object> P(Dictionary<string, object> root) => First(root, "pages");
        static Dictionary<string, object> C(Dictionary<string, object> root) => First(P(root), "captions");
        static Dictionary<string, object> S(Dictionary<string, object> root) => First(P(root), "shots");
        static Dictionary<string, object> Segment(Dictionary<string, object> root) => First(First(root, "cuts"), "segments");
        [TestCase("root-key")] [TestCase("page-key")] [TestCase("shot-key")] [TestCase("caption-key")] [TestCase("chain-key")]
        [TestCase("cut-key")] [TestCase("segment-key")] [TestCase("cut-caption-key")] [TestCase("node-key")] [TestCase("need-key")]
        [TestCase("record-kind")] [TestCase("pose")] [TestCase("units")] [TestCase("line-length")] [TestCase("line-count")] [TestCase("embedded-newline")]
        [TestCase("caption-minimum")] [TestCase("shot-hold")] [TestCase("cut-total")] [TestCase("cut-shot-move")]
        [TestCase("caption-kind")] [TestCase("claim-needs")] [TestCase("pre-verb")] [TestCase("fallback-digit")]
        [TestCase("claim-literal-digit")] [TestCase("claim-unbound-digit")] [TestCase("gauge-pump-link")] [TestCase("version")] [TestCase("duplicate-id")]
        [TestCase("source")] [TestCase("window")] [TestCase("cut-target")] [TestCase("cut-caption-minimum")] [TestCase("cut-caption-outside")]
        [TestCase("cut-extra-chain")] [TestCase("unknown-link")]
        [TestCase("cut-gauge-chain")] [TestCase("unknown-cut-node")] [TestCase("unknown-follow")]
        public void StrictLoaderRefusesEachViolation(string rule)
        {
            var r = DirectorKit.Object(DirectorKit.StoryText()); var pages = DirectorKit.Arr(r["pages"]); var page4 = DirectorKit.Obj(pages[7]);
            var cut = First(r, "cuts"); var segment = Segment(r);
            var captionSegment = DirectorKit.Obj(DirectorKit.Arr(cut["segments"])[1]);
            var cutCaption = First(captionSegment, "captions");
            switch (rule)
            {
                case "root-key": r["typo"] = 1; break; case "page-key": P(r)["typo"] = 1; break;
                case "shot-key": S(r)["typo"] = 1; break; case "caption-key": C(r)["typo"] = 1; break;
                case "chain-key": DirectorKit.Obj(P(r)["chain"])["typo"] = 1; break;
                case "cut-key": cut["typo"] = 1; break; case "segment-key": segment["typo"] = 1; break;
                case "cut-caption-key": cutCaption["typo"] = 1; break;
                case "node-key": First(DirectorKit.Obj(page4["chain"]), "nodes")["typo"] = 1; break;
                case "need-key": DirectorKit.Obj(DirectorKit.Obj(page4["trigger"])["alarm"])["typo"] = 1; break;
                case "record-kind": page4["trigger"] = DirectorKit.Object("{\"mystery\":{}}"); break;
                case "pose": S(r)["from"] = "pose:unknown"; break;
                case "units": S(r)["move"] = 3; S(r).Remove("moveS"); break;
                case "line-length": C(r)["lines"] = new List<object> { new string('x', 43) }; break;
                case "line-count": C(r)["lines"] = new List<object> { "a", "b", "c" }; break;
                case "embedded-newline": C(r)["lines"] = new List<object> { "a\nb" }; break;
                case "caption-minimum": C(r)["holdS"] = 2.99; break;
                case "shot-hold": DirectorKit.Obj(DirectorKit.Arr(P(r)["shots"])[1])["holdMinS"] = 2; break;
                case "cut-total": segment["durS"] = 5; break;
                case "cut-shot-move": segment["moveS"] = 4; break;
                case "caption-kind": C(r)["kind"] = "uncategorized"; break;
                case "claim-needs": C(r)["kind"] = "claim"; break;
                case "pre-verb": C(r)["lines"] = new List<object> { "The platform started the pump." }; break;
                case "fallback-digit": C(r)["kind"] = "fallback"; C(r)["lines"] = new List<object> { "There are 6." }; break;
                case "claim-literal-digit": C(r)["kind"] = "claim"; C(r)["needs"] = DirectorKit.Object("{\"chip\":{}}"); C(r)["lines"] = new List<object> { "There are 6." }; break;
                case "claim-unbound-digit": C(r)["kind"] = "claim"; C(r)["needs"] = DirectorKit.Object("{\"chip\":{}}"); C(r)["lines"] = new List<object> { "There are {n}." }; break;
                case "gauge-pump-link": DirectorKit.Arr(DirectorKit.Obj(page4["chain"])["links"]).Add(new List<object> { "water", "command" }); break;
                case "unknown-link": DirectorKit.Arr(DirectorKit.Obj(page4["chain"])["links"]).Add(new List<object> { "missing", "command" }); break;
                case "version": r["version"] = 99; break;
                case "duplicate-id": DirectorKit.Obj(pages[1])["id"] = "0"; break;
                case "source": C(r)["source"] = "UNKNOWN"; break;
                case "window": P(r)["windowSim"] = new List<object> { 5, 4 }; break;
                case "cut-target": cut["targetS"] = 299; break;
                case "cut-caption-minimum": cutCaption["holdS"] = 2.99; break;
                case "cut-caption-outside": cutCaption["atS"] = 100; break;
                case "cut-extra-chain": var c90 = DirectorKit.Obj(DirectorKit.Arr(r["cuts"])[1]); First(c90, "segments")["page"] = "2"; First(c90, "segments")["shot"] = "2a"; break;
                case "cut-gauge-chain": var shortCut = DirectorKit.Obj(DirectorKit.Arr(r["cuts"])[1]); var pump = DirectorKit.Arr(shortCut["segments"]).Select(DirectorKit.Obj).First(s => (string)s["page"] == "4"); DirectorKit.Arr(pump["nodes"]).Add("water"); break;
                case "unknown-cut-node": DirectorKit.Arr(segment["nodes"]).Add("missing"); break;
                case "unknown-follow": S(r)["followDeviceFromTrigger"] = "mystery"; break;
                default: throw new Exception(rule);
            }
            Assert.Throws<FormatException>(() => Storyboard.Parse(DirectorKit.Json(r), new PresentationMap(Kit.Layout())), rule);
        }
        [TestCase("detected")] [TestCase("detecting")] [TestCase("notified")] [TestCase("stores")] [TestCase("disabled")] [TestCase("refuses")] [TestCase("paced")] [TestCase("updated")]
        public void ClaimLintRecognizesVerbForms(string verb)
        {
            var r = DirectorKit.Object(DirectorKit.StoryText()); C(r)["lines"] = new List<object> { "The platform " + verb + "." };
            Assert.Throws<FormatException>(() => Storyboard.Parse(DirectorKit.Json(r), new PresentationMap(Kit.Layout())));
        }
        [Test]
        public void CutAbsoluteTimeCanCrossMidnight()
        {
            var segment = DirectorKit.Story().Cuts[0].Segments.First(s => s.Page == "8");
            Assert.That(segment.RecordedWallFromS, Is.GreaterThan(1000)); Assert.That(segment.RecordedAt(segment.DurS / 2), Is.EqualTo((segment.RecordedWallFromS + segment.RecordedWallToS) / 2));
        }

        [Test]
        public void PlaceholderMustBeBoundOnEveryAnyBranchAndFallbacksCannotSubstitute()
        {
            var root = DirectorKit.Object(DirectorKit.StoryText()); var c = C(root); c["kind"] = "claim"; c["lines"] = new List<object> { "There are {n}." };
            c["needs"] = DirectorKit.Object("{\"any\":[{\"binding\":{\"bind\":{\"n\":\"freshDevices\"}}},{\"chip\":{}}]}");
            Assert.Throws<FormatException>(() => Storyboard.Parse(DirectorKit.Json(root), new PresentationMap(Kit.Layout())));
            c["kind"] = "fallback"; c["needs"] = DirectorKit.Object("{\"binding\":{\"bind\":{\"n\":\"freshDevices\"}}}");
            Assert.Throws<FormatException>(() => Storyboard.Parse(DirectorKit.Json(root), new PresentationMap(Kit.Layout())));
        }
        [Test]
        public void BothCutsScheduleDisclosuresSummaryAndSeams()
        {
            var story = DirectorKit.Story();
            foreach (var cut in story.Cuts) {
                Assert.That(cut.Segments.Where(s => s.Page == "0").SelectMany(s => s.Overlays).Select(c => c.Id), Does.Contain("disclosure"));
                Assert.That(cut.Segments.Where(s => s.Page == "9").SelectMany(s => s.Overlays).Select(c => c.Id), Is.EquivalentTo(new[] { "subline", "fiction", "positions" }));
                Assert.That(cut.Segments.Where(s => s.Page == "8").SelectMany(s => s.Captions).Select(c => c.Id), Does.Contain("ota-summary"));
                Assert.That(cut.Segments.All(s => s.SeamPose == "pose:home" && s.DissolveS == 0.5), Is.True);
            }
            Assert.That(story.Page("4").Captions.Single(c => c.Id == "well").Lines[0], Is.EqualTo("The pump's well is filling\u2026"));
            Assert.That(DirectorKit.StoryText(), Does.Not.Contain("?"));
            Assert.That(story.Page("8").Shots.Single(s => s.Id == "8c").To, Is.EqualTo("pose:charger-north:close"));
        }
    
        [Test]
        public void AShortCutChainOutranksAnyLiveOnlyPageItsWindowOverlaps()
        {
            // Live day 2: page 4b (live and 5 min only) waited 94 s for its trigger at a higher priority than page 6, one of the
            // three 90 s chains, and page 6 expired in the queue. A short-cut page must be able to preempt a live-only page it meets.
            var root = DirectorKit.Object(DirectorKit.StoryText());
            var shortCut = new HashSet<string>(DirectorKit.Arr(root["cuts"]).Select(DirectorKit.Obj).Where(c => (string)c["id"] == "c90")
                .SelectMany(c => DirectorKit.Arr(c["segments"]).Select(seg => (string)DirectorKit.Obj(seg)["page"])));
            var pages = DirectorKit.Arr(root["pages"]).Select(DirectorKit.Obj).Where(p => !(bool)p["cutsOnly"]).ToArray();
            double D(object v) => System.Convert.ToDouble(v, System.Globalization.CultureInfo.InvariantCulture);
            foreach (var a in pages.Where(p => shortCut.Contains((string)p["id"])))
                foreach (var b in pages.Where(p => !shortCut.Contains((string)p["id"])))
                {
                    var wa = DirectorKit.Arr(a["windowSim"]); var wb = DirectorKit.Arr(b["windowSim"]);
                    if (D(wa[0]) < D(wb[1]) && D(wb[0]) < D(wa[1]))
                        Assert.That(D(a["priority"]), Is.GreaterThan(D(b["priority"])), "page " + a["id"] + " vs " + b["id"]);
                }
        }

        static readonly string[] OwnSession = { "hp-charger-mqtt-1", "hp-charger-mqtt-2", "pv-PAW-CERT-CHARGER-pin-0001-59e6c9cfa974", "pv-PAW-CERT-CHARGER-pin-0002-13a96fd1a263", "hp-light-pull-1", "hp-light-pull-2" };
        static readonly string[] OwnSessionShots = { "3c-1", "3c-2", "3c-3", "3c-4", "3c-5" };
        static readonly string[] Shown = { "shot", "caption", "caption-end", "node", "node-label", "link", "skip-page", "follow", "missed" };
        static Scheduler Day(Func<Observation, bool> keep, string defect = "")
        {
            var layout = Kit.Layout(); var s = new Scheduler(DirectorKit.Story(), layout); var source = DirectorTimeline.Clean(defect); var store = new ObservationStore();
            for (var wall = 0; wall <= 11 * layout.DayLengthSeconds / 24; wall++) {
                foreach (var o in source.Records.Where(o => o.ObservedAtS <= wall && keep(o))) store.Add(o);
                s.Tick(new DirectorSample("run", 6 + wall * 24 / layout.DayLengthSeconds, wall), store);
            }
            return s;
        }
        static string[] Others(Scheduler s) => s.Log.Where(e => e.Page != "3c" && e.Page != "" && Shown.Contains(e.Kind)).Select(e => e.ShowS + "|" + e.Page + "|" + e.Kind + "|" + e.Value).ToArray();
        [Test]
        public void NorthQuayOwnSessionPageParsesAndFramesItsDevices()
        {
            // Page 3c: the six North Quay devices that hold their own session (two chargers on an MQTT token, two on a client certificate,
            // two lamps that fetch their commands). It adds no pose, so each shot's existing pose must have its subjects in a 16:9 frame.
            var layout = Kit.Layout(); var map = new PresentationMap(layout); var page = Storyboard.Parse(DirectorKit.StoryText(), map).Page("3c");
            Assert.That(page.Shots.Select(s => s.Id), Is.EqualTo(OwnSessionShots));
            Assert.That(page.Shots.Select(s => s.CueSim), Is.Ordered.Ascending); Assert.That(page.Shots.All(s => s.CueSim >= page.FromSim && s.CueSim < page.ToSim), Is.True);
            Assert.That(page.Captions.Select(c => c.Id), Is.EquivalentTo(page.Shots.SelectMany(s => s.Captions)), "every caption has a shot");
            Assert.That(OwnSession.All(token => layout.Devices.Any(d => d.Token == token)), Is.True);
            // a claim names the platform's own records; what no director read can show (the configuration report, the refused certificate) is the scenario's word
            Assert.That(page.Captions.Where(c => c.Kind == "claim").Select(c => c.Id), Is.EqualTo(new[] { "dropped", "back", "dimmed" }));
            Assert.That(page.Captions.Where(c => c.Kind != "claim").All(c => c.Kind == "scenario" && c.Source == EvidenceSource.Scenario), Is.True);
            var subjects = new[] { OwnSession.Take(2), OwnSession.Skip(4), OwnSession.Skip(2).Take(2), OwnSession, OwnSession.Skip(2).Take(2) };
            for (var i = 0; i < page.Shots.Count; i++)
                foreach (var token in subjects[i]) {
                    Assert.That(page.Shots[i].To, Is.EqualTo(page.Shots[i].From));
                    var pose = map.Pose(page.Shots[i].To); var a = map.Anchors[token];
                    Assert.That(CameraMath.Project(pose, 16.0 / 9, a.X, 1.5, a.Z, out var nx, out var ny) && Math.Abs(nx) < .95 && Math.Abs(ny) < .95, Is.True, page.Shots[i].Id + " does not frame " + token);
                }
            // it is a live page: neither recorded cut borrows it, so neither cut's length moves
            Assert.That(DirectorKit.Story().Cuts.SelectMany(c => c.Segments).Any(s => s.Page == "3c"), Is.False);
        }
        [Test]
        public void NorthQuayOwnSessionPageIsScheduledInsideItsWindowAndYieldsToTheHotConnector()
        {
            var layout = Kit.Layout(); var page = DirectorKit.Story().Page("3c"); var hot = DirectorKit.Story().Page("3b");
            double Sim(double wall) => 6 + wall * 24 / layout.DayLengthSeconds;
            var s = Day(o => true); var own = s.Log.Where(e => e.Page == "3c").ToArray();
            Assert.That(own.Where(e => e.Kind == "shot").Select(e => e.Value), Is.EqualTo(OwnSessionShots));
            Assert.That(own.Where(e => e.Kind == "caption").Select(e => e.Value.Replace("\n", " ")), Is.EqualTo(page.Captions.Select(c => string.Join(" ", c.Lines))), "each caption once, in the authored order");
            foreach (var e in own.Where(e => Shown.Contains(e.Kind) || e.Kind == "resume"))
                Assert.That(Sim(e.ShowS), Is.InRange(page.FromSim, page.ToSim), e.Kind + " " + e.Value);
            Assert.That(own.Any(e => e.Kind == "missed"), Is.False);
            // each shot waits for its beat
            var shots = own.Where(e => e.Kind == "shot").ToArray();
            for (var i = 0; i < shots.Length; i++) Assert.That(Sim(shots[i].ShowS), Is.GreaterThanOrEqualTo(page.Shots[i].CueSim), shots[i].Value);
            // the claims are tied to the alarm that triggered the page and to both lamps' own readings
            Assert.That(own.Where(e => e.Kind == "caption" && e.Evidence.Count > 0).Select(e => e.Value.Split('\n')[0]), Is.EqualTo(new[] { page.Captions[0].Lines[0], page.Captions[1].Lines[0], page.Captions[3].Lines[0] }));
            Assert.That(own.Single(e => e.Kind == "caption" && e.Value.StartsWith(page.Captions[3].Lines[0])).Evidence.Count, Is.EqualTo(2));
            // The certificate is disabled on the hot connector's beat (14:00), and that page is in both recorded cuts at the clean day's
            // times. So this page ranks below it and waits on a shot with no caption: it gives the camera up on that very second, with
            // nothing cut off, and says its last line once that page is done and the charger has stood silent long enough to be drawn so.
            Assert.That(page.Priority, Is.LessThan(hot.Priority));
            var preempt = own.Single(e => e.Kind == "preempt"); var resume = own.Single(e => e.Kind == "resume");
            Assert.That(preempt.Value, Is.EqualTo("3b")); Assert.That(preempt.ShowS, Is.EqualTo((hot.FromSim - 6) * layout.DayLengthSeconds / 24));
            Assert.That(resume.Value, Is.EqualTo("3c-4")); Assert.That(page.Shots[3].Captions, Is.Empty);
            Assert.That(own.Last(e => e.Kind == "caption-end" && e.ShowS <= preempt.ShowS).ShowS, Is.LessThan(preempt.ShowS), "no caption is cut by the hand-over");
            Assert.That(resume.ShowS, Is.EqualTo(s.Log.Last(e => e.Page == "3b" && e.Kind == "state" && e.Value == "Presented").ShowS).Within(1));
            Assert.That(shots[4].ShowS, Is.InRange(resume.ShowS + 3, resume.ShowS + 4));
            // every other page's shots and captions are the same, to the second, as on a day when this page never plays
            Assert.That(Others(s), Is.EqualTo(Others(Day(o => o.Key != "mqtt-charger-disconnected"))));
        }
        [Test]
        public void NorthQuayOwnSessionPageStaysOffScreenWithoutItsAlarmAndClaimsOnlyWhatWasRead()
        {
            // The session never dropped (the simulator's mqtt-never-disconnect control): no cleared alarm, so the page never takes the camera.
            var quiet = Day(o => o.Key != "mqtt-charger-disconnected");
            Assert.That(quiet.Log.Any(e => e.Page == "3c" && (Shown.Contains(e.Kind) || e.Kind == "queue")), Is.False);
            // One lamp never fetched its command (pull-polling-stopped): the page plays, and the dim claim is not made.
            var page = DirectorKit.Story().Page("3c"); var dark = Day(o => !(o.Kind == "measure" && o.Device == "hp-light-pull-2"));
            var captions = dark.Log.Where(e => e.Page == "3c" && e.Kind == "caption").Select(e => e.Value.Split('\n')[0]).ToArray();
            Assert.That(captions, Does.Contain(page.Captions.Single(c => c.Id == "pull").Lines[0])); Assert.That(captions, Does.Not.Contain(page.Captions.Single(c => c.Id == "dimmed").Lines[0]));
            Assert.That(dark.Log.Where(e => e.Page == "3c" && e.Kind == "shot").Select(e => e.Value), Is.EqualTo(OwnSessionShots));
            // A day when the derate command never ties to its alarm keeps the hot connector's page up longest: the last line still fits its window.
            foreach (var control in new[] { "refuse-derate", "command-cause-not-read" }) {
                var late = Day(o => true, control); var last = late.Log.Single(e => e.Page == "3c" && e.Kind == "shot" && e.Value == "3c-5");
                Assert.That(Others(late), Is.EqualTo(Others(Day(o => o.Key != "mqtt-charger-disconnected", control))), control);
                Assert.That(late.Log.Any(e => e.Page == "3c" && e.Kind == "missed"), Is.False, control);
                Assert.That(late.Log.Last(e => e.Page == "3c" && e.Kind == "caption-end").ShowS, Is.InRange(last.ShowS + 5, (page.ToSim - 6) * Kit.Layout().DayLengthSeconds / 24), control);
            }
        }
}
}
