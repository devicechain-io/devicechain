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
            var story = DirectorKit.Story(); Assert.That(story.Pages.Select(p => p.Id), Is.EqualTo(new[] { "0", "1", "1b", "2", "3", "3b", "4", "5", "4b", "6", "7", "8", "9" }));
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
            var r = DirectorKit.Object(DirectorKit.StoryText()); var pages = DirectorKit.Arr(r["pages"]); var page4 = DirectorKit.Obj(pages[6]);
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
}
}
