// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Demos.Hud;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>
    /// The story pass: the narrated cuts' structure and laws, the two predicates it added (a tally, an at-least set), and the model
    /// behind the HUD's pins, names and world text.
    /// </summary>
    public class StoryPassTests
    {
        static PresentationMap Map() => new PresentationMap(Kit.Layout());
        static Dictionary<string, object> Cut(Dictionary<string, object> root, string id) => DirectorKit.Arr(root["cuts"]).Select(DirectorKit.Obj).First(c => (string)c["id"] == id);
        static List<Dictionary<string, object>> Segments(Dictionary<string, object> root, string id) => DirectorKit.Arr(Cut(root, id)["segments"]).Select(DirectorKit.Obj).ToList();
        static Dictionary<string, object> Vo(Dictionary<string, object> segment) => DirectorKit.Obj(segment["vo"]);
        static List<Dictionary<string, object>> Takes(Dictionary<string, object> segment) => DirectorKit.Arr(Vo(segment)["takes"]).Select(DirectorKit.Obj).ToList();
        static Dictionary<string, object> Page(Dictionary<string, object> root, string id) => DirectorKit.Arr(root["pages"]).Select(DirectorKit.Obj).First(p => (string)p["id"] == id);
        static Dictionary<string, object> Node(Dictionary<string, object> root, string page, string id)
            => DirectorKit.Arr(DirectorKit.Obj(Page(root, page)["chain"])["nodes"]).Select(DirectorKit.Obj).First(n => (string)n["id"] == id);

        // ------------------------------------------------------------------------------------------------ the cuts as authored

        [TestCase("c90", 14, 14, 4)]
        [TestCase("c300", 33, 31, 5)]
        public void ACutIsFewLongShotsEachWithOneLineAndOneSlowMove(string id, int shots, int beats, double minimum)
        {
            var cut = DirectorKit.Story().Cuts.Single(c => c.Id == id);
            Assert.That(cut.Segments.Count, Is.EqualTo(shots));
            Assert.That(StoryCut.MinShotS(id), Is.EqualTo(minimum));
            foreach (var s in cut.Segments)
            {
                Assert.That(s.DurS, Is.GreaterThanOrEqualTo(minimum), s.Shot + " is shorter than the cut's minimum shot");
                Assert.That(s.Vo, Is.Not.Null, s.Shot + " has no voice-over line");
                Assert.That(s.Vo.AtS + s.Vo.LongestS + StoryCut.VoTailS, Is.LessThanOrEqualTo(s.DurS + 1e-9), s.Vo.Id + " does not fit its shot");
                // one slow move that lasts the shot: along two poses, or a push; the end card alone stands still
                Assert.That(s.Push > 0 || s.MoveS > 0 || s.EndCard, Is.True, s.Shot + " is a locked-off shot");
                if (s.MoveS > 0 && s.Push == 0) Assert.That(s.MoveS, Is.GreaterThanOrEqualTo(3), s.Shot + ": a move is slow");
            }
            // a beat is one line of the script; a beat of two shots carries its line in two parts (06a, 06b)
            Assert.That(cut.Segments.Select(s => new string(s.Vo.Id.Substring(id.Length + 1).TakeWhile(ch => !char.IsLower(ch)).ToArray())).Distinct().Count(), Is.EqualTo(beats));
            // hard cuts inside a chapter: a dissolve is a jump in time, and only a chapter's first shot has one
            string chapter = null;
            foreach (var s in cut.Segments)
            {
                if (s.Chapter == chapter) Assert.That(s.DissolveS, Is.EqualTo(0), s.Shot + " dissolves inside its chapter");
                else if (chapter != null) Assert.That(s.DissolveS, Is.EqualTo(0.5).Or.EqualTo(0), s.Shot);
                chapter = s.Chapter;
            }
            Assert.That(cut.Segments.First().Borrowed, Is.EqualTo("later that day"), "the storm cold open is borrowed, and says so");
            Assert.That(cut.Segments.Last().EndCard, Is.True);
            Assert.That(cut.Chapters.Select(c => c.Tag).Where(t => t.Length > 0), Is.EqualTo(id == "c90"
                ? new[] { "1 · Act", "2 · Notice", "3 · Update" } : new[] { "Morning · the routine", "Evening · the storm", "Overnight · the update" }));
        }

        [Test]
        public void TheShortCutIsThreeChainsAndNeitherCutSaysWhatTheDayCannotShow()
        {
            var story = DirectorKit.Story(); var c90 = story.Cuts.Single(c => c.Id == "c90");
            Assert.That(c90.Segments.Select(s => s.Page).Distinct(), Is.EqualTo(new[] { "0", "4", "6", "7", "8", "9" }));
            foreach (var cut in story.Cuts)
            {
                var said = string.Join(" ", cut.Segments.SelectMany(s => s.Vo.Takes).Select(t => t.Text));
                foreach (var never in new[] { "Exactly", "exactly", "quiet morning", "never more than", "Nothing hidden", "damaged" })
                    Assert.That(said, Does.Not.Contain(never), cut.Id + " says \"" + never + "\"");
                Assert.That(cut.Segments.SelectMany(s => s.Captions).Select(c => c.Id), Has.No.Member("exact"));
                Assert.That(cut.Segments.Any(s => s.Page == "3c"), Is.False, "the own-session devices tour stays out");
                // page 1's "A quiet morning raises nothing." is not offered: it is not true of the recorded day
                Assert.That(cut.Segments.Where(s => s.Page == "1").SelectMany(s => s.Captions).Select(c => c.Id), Has.No.Member("quiet"));
                // every line that states a platform outcome has a claim take and a fallback: its last take needs none and states none
                foreach (var vo in cut.Segments.Select(s => s.Vo))
                {
                    var floor = vo.Takes.Last(); Assert.That(floor.Claims, Is.Empty, vo.Id);
                    Assert.That(Storyboard.UnboundOutcome.IsMatch(floor.Text), Is.False, floor.Id + " states a platform outcome with no claim");
                    foreach (var take in vo.Takes.Take(vo.Takes.Count - 1)) Assert.That(take.Claims, Is.Not.Empty, take.Id);
                    foreach (var take in vo.Takes) Assert.That(take.Text.Any(char.IsDigit), Is.False, take.Id + ": a spoken number is a word");
                }
            }
            // the spoken numbers are bound: "forty-three", "six", "two", "five" and "one" each stand on a claim's value
            var bound = c90.Segments.SelectMany(s => s.Vo.Takes).SelectMany(t => t.Claims).Where(c => c.Bound.Count > 0).ToDictionary(c => c.Id + "/" + string.Join(",", c.Bound.Keys), c => string.Join(",", c.Bound.Values));
            Assert.That(bound["baseline/n"], Is.EqualTo("43")); Assert.That(bound["absence-count/n"], Is.EqualTo("6")); Assert.That(bound["absence-notified/n"], Is.EqualTo("6"));
            Assert.That(bound["cap/cap"], Is.EqualTo("2")); Assert.That(bound["ota-summary/updated,refused"], Is.EqualTo("5,1"));
        }

        [TestCase("min-shot")] [TestCase("dissolve-inside-chapter")] [TestCase("chapter-order")] [TestCase("unknown-chapter")] [TestCase("no-chapter")]
        [TestCase("vo-does-not-fit")] [TestCase("vo-floor-has-claim")] [TestCase("vo-middle-no-claim")] [TestCase("vo-digit")] [TestCase("vo-unbound-outcome")]
        [TestCase("vo-unknown-claim")] [TestCase("vo-scenario-row-claim")] [TestCase("vo-unbound-number")] [TestCase("vo-duplicate-line")] [TestCase("vo-duplicate-take")]
        [TestCase("row-digit")] [TestCase("row-unbound")] [TestCase("row-too-long")] [TestCase("shot-tags-unknown")] [TestCase("shot-tags-five")]
        [TestCase("card-dock")] [TestCase("push-range")] [TestCase("pose-override-unknown")] [TestCase("loop-seam")] [TestCase("tally-field")] [TestCase("tally-min")]
        [TestCase("atleast-cardinality")] [TestCase("atleast-not-boolean")]
        public void TheNarratedCutsLawsAreRefusedByTheLoader(string rule)
        {
            var r = DirectorKit.Object(DirectorKit.StoryText()); var c90 = Segments(r, "c90");
            var e = c90.First(s => (string)Vo(s)["id"] == "c90-E"); var h = c90.First(s => (string)Vo(s)["id"] == "c90-H");
            switch (rule)
            {
                case "min-shot": c90[0]["durS"] = 3.5; c90[1]["durS"] = (double)c90[1]["durS"] + 0.5; break;
                case "dissolve-inside-chapter": e["dissolveS"] = 0.5; break;
                case "chapter-order": e["chapter"] = "open"; break;
                case "unknown-chapter": e["chapter"] = "interlude"; break;
                case "no-chapter": e.Remove("chapter"); break;
                case "vo-does-not-fit": Takes(e)[0]["durS"] = 10.5; break;
                case "vo-floor-has-claim": Takes(e).Last()["claims"] = new List<object> { new Dictionary<string, object> { ["id"] = "platform-start" } }; break;
                case "vo-middle-no-claim": Takes(e)[1]["claims"] = new List<object>(); break;
                case "vo-digit": Takes(e)[0]["text"] = "The pump starts at 2.5 metres."; break;
                case "vo-unbound-outcome": Takes(e).Last()["text"] = "The platform starts the pump."; break;
                case "vo-unknown-claim": Takes(e)[0]["claims"] = new List<object> { new Dictionary<string, object> { ["id"] = "no-such-claim" } }; break;
                // a scenario row is what the scenario did: it licenses nothing the platform is said to have done
                case "vo-scenario-row-claim": Takes(h)[0]["claims"] = new List<object> { new Dictionary<string, object> { ["id"] = "outage" } }; break;
                case "vo-unbound-number": Takes(h)[0]["claims"] = new List<object> { new Dictionary<string, object> { ["id"] = "silence", ["bound"] = new Dictionary<string, object> { ["n"] = "6" } } }; break;
                case "vo-duplicate-line": Vo(h)["id"] = "c90-E"; break;
                case "vo-duplicate-take": Takes(e)[1]["id"] = Takes(e)[0]["id"]; break;
                case "row-digit": Node(r, "6", "absence-count")["label"] = "6 absence alarms"; break;
                case "row-unbound": Node(r, "6", "absence-count")["label"] = "{m} absence alarms"; break;
                case "row-too-long": Node(r, "4", "command")["label"] = new string('x', Storyboard.MaxRowLabel + 1); break;
                case "shot-tags-unknown": DirectorKit.Obj(DirectorKit.Arr(Page(r, "4")["shots"])[0])["tags"] = new List<object> { "hp-no-such-device" }; break;
                case "shot-tags-five": DirectorKit.Obj(DirectorKit.Arr(Page(r, "4")["shots"])[0])["tags"] = Kit.Layout().Devices.Take(5).Select(d => (object)d.Token).ToList(); break;
                case "card-dock": e["cardDock"] = "centre"; break;
                case "push-range": e["push"] = 0.9; break;
                case "pose-override-unknown": e["from"] = "pose:nowhere"; break;
                case "loop-seam": Cut(r, "c90")["loopSeamS"] = 30.0; break;
                case "tally-field": DirectorKit.Obj(DirectorKit.Obj(Node(r, "6", "absence-count")["needs"])["tally"])["field"] = "mystery"; break;
                case "tally-min": DirectorKit.Obj(DirectorKit.Obj(Node(r, "6", "absence-count")["needs"])["tally"])["min"] = 0.0; break;
                case "atleast-cardinality": DirectorKit.Obj(DirectorKit.Obj(Node(r, "7", "stored")["needs"])["set"])["cardinality"] = 6.0; break;
                case "atleast-not-boolean": DirectorKit.Obj(DirectorKit.Obj(Node(r, "7", "stored")["needs"])["set"])["atLeast"] = "yes"; break;
                default: throw new Exception(rule);
            }
            Assert.Throws<FormatException>(() => Storyboard.Parse(DirectorKit.Json(r), Map()), rule);
        }

        [Test]
        public void CardRowsAreWrittenForAViewerAndEveryNumberOnOneIsBound()
        {
            var story = DirectorKit.Story();
            foreach (var page in story.Pages)
                foreach (var node in page.Nodes)
                {
                    Assert.That(node.Label.Length, Is.LessThanOrEqualTo(Storyboard.MaxRowLabel), page.Id + ":" + node.Id);
                    Assert.That(WorldText.LooksInternal(node.Label.Replace("{", "").Replace("}", "")), Is.False, page.Id + ":" + node.Id + " reads as an engineer's label: " + node.Label);
                    if (node.Source != EvidenceSource.Scenario) Assert.That(node.Label.Any(char.IsDigit), Is.False, page.Id + ":" + node.Id);
                }
            var pump = story.Page("4").Nodes.Where(n => n.Family == "pump").Select(n => n.Label).ToArray();
            Assert.That(pump, Is.EqualTo(new[] { "Well above alarm level", "Start command sent", "Pump running", "Well falling" }));
            Assert.That(story.Page("7").Nodes.Single(n => n.Id == "paced").Source, Is.EqualTo(EvidenceSource.DeviceSide), "the slow-down is the sender's count");
            Assert.That(new RailNode().Row.Kind, Is.EqualTo(ChainRowKind.Platform));
        }

        // ------------------------------------------------------------------------------------------------ the two predicates

        static ObservationStore Outage(int silent, int notified, bool escalated = false)
        {
            var store = new ObservationStore(); var devices = new[] { "hp-light-east-1", "hp-light-east-2", "hp-light-east-3", "hp-light-east-4", "hp-charger-east-1", "hp-charger-east-2", "hp-light-north-1" };
            for (var i = 0; i < silent; i++)
                store.Add(DirectorKit.O("alarm", devices[i], devices[i].Contains("light") ? "light-silent" : "charger-offline", "RAISED", sim: 20.5, wall: 870, id: "alarm-" + i));
            for (var i = 0; i < notified; i++)
                store.Add(DirectorKit.O("attempt", devices[i], "absence", "SENT", sim: 20.6, wall: 876, id: "mail-" + i, fields: new Dictionary<string, string> { ["alarmToken"] = "alarm-" + i }));
            if (escalated) store.Add(DirectorKit.O("attempt", devices[0], "absence", "SENT", sim: 20.9, wall: 900, id: "again-0", fields: new Dictionary<string, string> { ["alarmToken"] = "alarm-0", ["trigger"] = "ESCALATION" }));
            return store;
        }

        [Test]
        public void ATallyCountsWhatWasRecordedAndBindsTheNumberItFound()
        {
            var page = DirectorKit.Story().Page("6"); var count = page.Nodes.Single(n => n.Id == "absence-count"); var sent = page.Nodes.Single(n => n.Id == "absence-notified");
            // no coverage interval is added anywhere here: a count of what was recorded makes no "only these" statement
            var six = Outage(6, 6, escalated: true); var context = page.Context("run", 900);
            var counted = count.Needs.Evaluate(six, context);
            Assert.That(counted.Holds, Is.True); Assert.That(counted.Bindings["n"], Is.EqualTo("6")); Assert.That(counted.Records.Count, Is.EqualTo(6));
            // a second notification for the same alarm (an escalation) is the same alarm notified: still six
            Assert.That(sent.Needs.Evaluate(six, context).Bindings["n"], Is.EqualTo("6"));
            // CONTROL (silence-north-lamp): a seventh silent device changes the count, and the row says seven
            Assert.That(count.Needs.Evaluate(Outage(7, 6), context).Bindings["n"], Is.EqualTo("7"));
            // CONTROL (smtp-sink-stopped): nothing sent, nothing counted, no row
            Assert.That(sent.Needs.Evaluate(Outage(6, 0), context).Holds, Is.False);
            Assert.That(count.Needs.Evaluate(new ObservationStore(), context).Holds, Is.False);
            // before the records are observed the count is of what has been seen so far
            Assert.That(count.Needs.Evaluate(six, page.Context("run", 860)).Holds, Is.False);
            // the captions bind the same number
            Assert.That(page.Captions.Single(c => c.Id == "absence-count").Render(counted), Is.EqualTo("6 silent devices, 6 alarms."));
            Assert.That(count.Needs.Explain(six, context), Does.StartWith("tally HOLDS: 6 distinct device"));
            Assert.That(count.Needs.BoundNames, Is.EqualTo(new[] { "n" }));
        }

        [Test]
        public void AnAtLeastSetAsksThatEveryExpectedMemberWasObservedAndNothingMore()
        {
            var need = DirectorKit.Need("{\"set\":{\"observed\":{\"measure\":{\"key\":\"backfill\",\"mode\":\"occurred\"}},\"expected\":{\"ledger\":{\"source\":\"SCENARIO\",\"key\":\"backfill\",\"mode\":\"occurred\"}},"
                + "\"field\":\"readingIdentity\",\"expectedField\":\"times\",\"stream\":\"measurements\",\"fromSim\":19.75,\"toSim\":22.1,\"atLeast\":true}}");
            var exact = DirectorKit.Need("{\"set\":{\"observed\":{\"measure\":{\"key\":\"backfill\",\"mode\":\"occurred\"}},\"expected\":{\"ledger\":{\"source\":\"SCENARIO\",\"key\":\"backfill\",\"mode\":\"occurred\"}},"
                + "\"field\":\"readingIdentity\",\"expectedField\":\"times\",\"stream\":\"measurements\",\"fromSim\":19.75,\"toSim\":22.1}}");
            ObservationStore Store(params string[] stored)
            {
                var s = new ObservationStore();
                s.Add(DirectorKit.O("ledger", key: "backfill", sim: 21.25, wall: 915, id: "ledger", source: EvidenceSource.Scenario, sets: new Dictionary<string, string[]> { ["times"] = new[] { "a", "b", "c" } }));
                var i = 0;
                foreach (var id in stored) s.Add(DirectorKit.O("measure", "hp-charger-east-1", "backfill", sim: 21.3 + i * 0.01, wall: 920 + i, id: "reading-" + i++, fields: new Dictionary<string, string> { ["readingIdentity"] = id, ["backdated"] = "true" }));
                return s;
            }
            var context = new EvidenceContext("run", 21.25, 22.1, 950, EvidenceSource.Platform, null, 21.8);
            Assert.That(need.Evaluate(Store("a", "b", "c"), context).Holds, Is.True);
            Assert.That(need.Evaluate(Store("a", "b", "c", "at-the-outage-instant"), context).Holds, Is.True, "a reading beyond the ledger does not refute it");
            Assert.That(need.Evaluate(Store("a", "b"), context).Holds, Is.False, "CONTROL (drop-backfill): a buffered reading that was never stored");
            Assert.That(need.Evaluate(Store(), context).Holds, Is.False);
            // the exact form of the same claim needs its stream covered and refuses the extra reading
            var covered = Store("a", "b", "c"); Assert.That(exact.Evaluate(covered, context).Holds, Is.False, "no coverage");
            covered.AddCoverage(new CoverageInterval("run", "measurements", 19.75, 22.1, false)); Assert.That(exact.Evaluate(covered, context).Holds, Is.True);
            var extra = Store("a", "b", "c", "x"); extra.AddCoverage(new CoverageInterval("run", "measurements", 19.75, 22.1, false)); Assert.That(exact.Evaluate(extra, context).Holds, Is.False);
            Assert.That(need.Explain(Store("a", "b"), context), Does.Contain("missing 1 (c)").And.Contain("at least"));
            Assert.That(DirectorKit.Story().Page("7").Captions.Single(c => c.Id == "all-buffered").Needs.AtLeast, Is.True);
        }

        // ------------------------------------------------------------------------------------------------ the HUD's model

        [Test]
        public void OnePinADeviceInItsWorstSeverityWithACountAndOnlyAShotsSubjectsAreNamed()
        {
            var layout = Kit.Layout(); var model = new HarborObservedModel(layout); var source = new PreviewSource(layout); var now = DateTimeOffset.Parse("2026-01-01T00:00:00Z");
            source.SetTime(19.9 * 3600, now);
            var frame = model.Update(source, new ClockReading(19.9 * 3600, ClockMode.Preview, ""), now);
            var pins = HudModel.Pins(frame);
            Assert.That(pins.Select(p => p.Key).Distinct().Count(), Is.EqualTo(pins.Count), "one pin a device");
            foreach (var pin in pins)
            {
                var alarms = frame.Beacons.Where(b => b.Device == pin.Key && !b.Cleared).ToArray();
                Assert.That(pin.Count, Is.EqualTo(alarms.Length)); Assert.That(pin.Count, Is.EqualTo(frame.AlarmCounts[pin.Key]));
                Assert.That(pin.Severity, Is.EqualTo(alarms.Max(b => AlarmPins.Severity(b.Severity))));
            }
            Assert.That(HudModel.Pins(null), Is.Empty);
            // names: the shot's subjects, by token, at most four, and only the name
            var tags = HudModel.Tags(layout, new[] { "hp-light-east-3", "hp-charger-north-1", "hp-no-such-device" });
            Assert.That(tags.Select(t => t.Text), Is.EqualTo(new[] { "Lamp E3", "Charger N1" }));
            Assert.That(tags[0].ExternalId, Is.EqualTo(layout.Devices.First(d => d.Token == "hp-light-east-3").ExternalId));
            Assert.That(HudModel.Tags(layout, layout.Devices.Select(d => d.Token)).Count, Is.EqualTo(4));
            Assert.That(HudModel.Tags(layout, null), Is.Empty);
            foreach (var d in layout.Devices) Assert.That(HudModel.ViewerName(d, layout), Does.Not.Contain("alarm").And.Not.Contain("MQTT").And.Not.Contain("Cert").And.Not.Contain("Pull"));
            // a device's name is its own: no two devices of the district share one
            var named = layout.Devices.Where(d => d.Kind != DeviceKind.Truck && d.Kind != DeviceKind.Meter).Select(d => HudModel.ViewerName(d, layout)).ToArray();
            Assert.That(named.Distinct().Count(), Is.EqualTo(named.Length), string.Join(", ", named.GroupBy(n => n).Where(g => g.Count() > 1).Select(g => g.Key)));
            Assert.That(HudModel.Strip("recorded", 17.62, "×1.2", ""), Is.EqualTo("RECORDED  ·  Port Alder  ·  17:37  ·  ×1.2"));
            Assert.That(HudModel.Strip("recorded", 19, "", "later that day"), Is.EqualTo("RECORDED  ·  Port Alder  ·  19:00  ·  later that day"));
        }

        [Test]
        public void TheWorldsWordsAreAViewersOrAnEngineersAndNeverBoth()
        {
            Assert.That(WorldText.ViewerTags, Is.EqualTo(new[] { "Alarm level 2.5 m", "Float switch 3.2 m", "Pump stops 0.8 m" }));
            foreach (var tag in WorldText.ViewerTags) Assert.That(WorldText.LooksInternal(tag), Is.False, tag);
            foreach (var tag in WorldText.EngineerTags) Assert.That(WorldText.LooksInternal(tag), Is.True, tag);
            // what leaked into the first renders, and the forms the audit knows them by
            foreach (var leaked in new[] { "feeder down (WORLD)", "replacement (runner)", "wet-well-high 2.5 m (rule)", "float start 3.2 m (pump's own)", "well_alarm_m (platform attribute, MINOR rule)", "low cut-off 0.8 m" })
                Assert.That(WorldText.LooksInternal(leaked), Is.True, leaked);
            Assert.That(WorldText.LooksInternal(""), Is.False); Assert.That(WorldText.LooksInternal("Lamp E3"), Is.False);
            // no caption of the storyboard reads as one either
            foreach (var page in DirectorKit.Story().Pages) foreach (var c in page.Captions) foreach (var line in c.Lines)
                Assert.That(line.Contains("(") || line.Contains("_") || line.Contains("WORLD"), Is.False, page.Id + ":" + c.Id);
        }

        [Test]
        public void TheLiveCardsCueIsTheRecordedTimeBetweenTwoRowsFacts()
        {
            var hud = new HudModel();
            hud.AddNode("well", "Well above alarm level", EvidenceSource.Platform, 100, 689.9);
            hud.AddNode("command", "Start command sent", EvidenceSource.Platform, 103, 690.8);
            hud.AddNode("running", "Pump running", EvidenceSource.Platform, 106, 694.9);
            hud.AddNode("paced", "Asked to slow down", EvidenceSource.DeviceSide, 109);
            var rows = hud.Rail.Select(n => n.Row).ToArray();
            Assert.That(rows.Select(r => ChainCardState.CueText(r.CueS)), Is.EqualTo(new[] { "", "+1 s", "+4 s", "" }), "the cue is the platform's latency, not when the rows were drawn (three seconds apart)");
            Assert.That(rows.Select(r => r.Kind), Is.EqualTo(new[] { ChainRowKind.Platform, ChainRowKind.Platform, ChainRowKind.Platform, ChainRowKind.SenderCount }));
            hud.ClearRail(); hud.AddNode("outage", "East Quay loses power", EvidenceSource.Scenario, 200, 825);
            hud.AddNode("absence-count", "6 absence alarms", EvidenceSource.Platform, 203, 869.9);
            Assert.That(hud.Rail[0].Row.Kind, Is.EqualTo(ChainRowKind.ScenarioTrigger)); Assert.That(ChainCardState.CueText(hud.Rail[1].CueS), Is.EqualTo("+45 s"));
            // the live director hands the row its fact's instant
            var scheduler = DirectorTimeline.Run(); var node = scheduler.Log.First(e => e.Kind == "node" && e.Value == "command");
            Assert.That(node.InstantS, Is.EqualTo((17.51 - 6) * 60).Within(1e-6));
        }

        [Test]
        public void TheRendersPanelCheckFailsOnAnyPanelPixelTooBrightForItsText()
        {
            var white = new HudColor(1, 1, 1); var check = new HudPanelCheck();
            // a panel drawn last: its scrim over the brightest thing behind it
            var last = HudTheme.Over(HudTheme.Scrim, white).Luminance;
            for (var frame = 0; frame < 30; frame++) { check.Sample(frame, "caption", last); check.Sample(frame, "card", HudTheme.Over(HudTheme.Scrim, new HudColor(0.1f, 0.12f, 0.2f)).Luminance); }
            Assert.That(check.Violations, Is.Empty); Assert.That(check.Samples, Is.EqualTo(60)); Assert.That(check.Brightest, Is.EqualTo(last).Within(1e-12));
            Assert.That(HudTheme.ContrastOn(HudTheme.Dim, check.Brightest), Is.GreaterThan(HudTheme.MinContrast));
            Assert.That(check.Json(false), Does.Contain("\"verdict\":\"pass\"").And.Contain("\"caption\":30"));
            // CONTROL: a lamp's bloom drawn OVER the panel (the HUD under the scene's post-processing) is a bright pixel where the scrim should be
            check.Sample(31, "caption", new HudColor(0.95f, 0.9f, 0.7f).Luminance);
            Assert.That(check.Violations.Single(), Does.StartWith("frame 31, caption: background luminance"));
            Assert.That(check.Json(true), Does.Contain("\"verdict\":\"FAIL\"").And.Contain("\"control\":true"));
            // the floor is the theme's: the brightest background on which its dimmest text still holds 4.5:1
            Assert.That(HudTheme.ContrastOn(HudTheme.Dim, HudPanelCheck.MaxLuminance), Is.EqualTo(HudTheme.MinContrast).Within(1e-9));
            // CONTROL: the 92 % scrim of the first render lets a lamp through far enough to fail the small grey text (measured: 4.4:1)
            Assert.That(HudTheme.Over(HudTheme.Scrim.WithAlpha(0.92f), white).Luminance, Is.GreaterThan(HudPanelCheck.MaxLuminance));
        }

        [Test]
        public void RainIsWeatherAndNeverTheSubject()
        {
            // translucent, under a pixel wide and a short streak: how hard it rains is how many streaks there are
            Assert.That(RainField.Opacity, Is.InRange(0.15, 0.35)); Assert.That(RainField.WidthPerMetre, Is.LessThan(0.0005)); Assert.That(RainField.ExposureS, Is.LessThan(0.05));
            // at 1080p with a 40 degree lens a streak 30 m off is narrower than a pixel
            var pixelAt30 = 2 * 30 * Math.Tan(40 * Math.PI / 360) / 1080;
            Assert.That(RainField.WidthPerMetre * 30 * 2, Is.LessThan(pixelAt30 * 1.3));
            Assert.That(RainField.Count(new Weather(1, 1)), Is.GreaterThan(RainField.Count(new Weather(1, 0.3))), "density is still the cue for intensity");
            Assert.That(RainField.Count(new Weather(0, 0)), Is.EqualTo(0));
        }

        [Test]
        public void PlanningAndDispatchingTheReadsDoesNotHappenEveryFrameHoweverLongTheDayHasRun()
        {
            // 100 s at 100 frames a second: a new status every second, each of 60 reads answering every three seconds
            long Run(int reads, out long dispatches)
            {
                var gate = new ReadPlanGate(); var start = DateTimeOffset.Parse("2026-01-01T00:00:00Z"); object status = new object();
                var next = new DateTimeOffset[reads]; for (var i = 0; i < reads; i++) next[i] = start.AddSeconds(i * 3.0 / reads);
                for (var frame = 0; frame < 10000; frame++)
                {
                    var now = start.AddSeconds(frame / 100.0);
                    if (frame % 100 == 0) status = new object();
                    for (var i = 0; i < reads; i++) if (next[i] <= now.AddSeconds(-0.05)) { next[i] = now.AddSeconds(3); gate.Answered(); } // an answer, 50 ms after it was asked for
                    if (gate.ShouldPlan(status, now)) { }
                    if (gate.ShouldDispatch(now)) { DateTimeOffset? idle = null; foreach (var t in next) if (t > now && (!idle.HasValue || t < idle.Value)) idle = t; gate.Dispatched(now, idle); }
                }
                dispatches = gate.Dispatches; return gate.Plans;
            }
            var few = Run(15, out var fewDispatches); var many = Run(120, out var manyDispatches);
            // the work follows the answers (reads / 3 s) and the status (1 / s), never the frame rate: 10,000 frames, far fewer passes
            Assert.That(few, Is.LessThan(700)); Assert.That(fewDispatches, Is.LessThan(1400));
            Assert.That(many, Is.LessThan(4300), "120 reads answering every 3 s over 100 s is 4,000 answers"); Assert.That(manyDispatches, Is.LessThan(8300));
            // and with nothing new, a frame plans nothing and dispatches nothing
            var idle2 = new ReadPlanGate(); var t0 = DateTimeOffset.Parse("2026-01-01T00:00:00Z"); var same = new object();
            Assert.That(idle2.ShouldPlan(same, t0), Is.True); Assert.That(idle2.ShouldDispatch(t0), Is.True); idle2.Dispatched(t0, t0.AddSeconds(3));
            for (var frame = 1; frame < 90; frame++) { Assert.That(idle2.ShouldPlan(same, t0.AddSeconds(frame / 100.0)), Is.False); Assert.That(idle2.ShouldDispatch(t0.AddSeconds(frame / 100.0)), Is.False); }
            Assert.That(idle2.ShouldPlan(same, t0.AddSeconds(1)), Is.True, "a second has passed: a device that bound late is planned for");
            idle2.Dispatched(t0.AddSeconds(1), t0.AddSeconds(3));
            Assert.That(idle2.ShouldDispatch(t0.AddSeconds(2.9)), Is.False); Assert.That(idle2.ShouldDispatch(t0.AddSeconds(3)), Is.True, "the earliest idle read is due");
            idle2.Dispatched(t0.AddSeconds(3), null); idle2.Answered(); Assert.That(idle2.ShouldDispatch(t0.AddSeconds(3.01)), Is.True, "an answer frees a slot");
            Assert.That(idle2.ShouldPlan(same, t0.AddSeconds(3.01)), Is.True, "and may bring a new alarm to ask about");
            // a new status object is a change even inside the second
            idle2.Dispatched(t0.AddSeconds(3.01), null); Assert.That(idle2.ShouldPlan(new object(), t0.AddSeconds(3.02)), Is.True);
        }

        [Test]
        public void AFrameInWhichNoReadAnsweredCopiesNeitherTheReadsNorTheAlarmChanges()
        {
            var t0 = DateTimeOffset.Parse("2026-01-01T00:00:00Z");
            var reads = Enumerable.Range(0, 400).Select(i => new DirectorRead("alarms", "device-" + i)).ToArray();
            var changes = Enumerable.Range(0, 300).Select(i => new DirectorAlarmChange("alarm-" + i, "device-" + i, "key", t0.AddSeconds(i))).ToList();
            var tap = new DirectorMeasurementTap(); tap.Watch(new DirectorMeasurement("device-1", "level", 1.0, t0));
            var first = new DirectorReadSnapshot(reads, true, 0, 0, changes, tap.Records);
            // the next frame: nothing answered, nothing was added
            var next = first.Observed(true, 0, 0, changes, tap.Records);
            Assert.That(next.Reads, Is.SameAs(first.Reads), "the reads are not copied"); Assert.That(next.Problems, Is.SameAs(first.Problems));
            Assert.That(next.AlarmChanges, Is.SameAs(first.AlarmChanges), "nor the alarm changes, however many the day has seen");
            // a new measurement and a new alarm change are both seen, and what was published before is not disturbed
            tap.Watch(new DirectorMeasurement("device-2", "level", 2.0, t0.AddSeconds(1))); changes.Add(new DirectorAlarmChange("alarm-x", "device-x", "key", t0.AddSeconds(400)));
            var later = next.Observed(false, 2, 1, changes, tap.Records);
            Assert.That(later.Measurements.Count, Is.EqualTo(2)); Assert.That(next.Measurements.Count, Is.EqualTo(1));
            Assert.That(later.AlarmChanges.Count, Is.EqualTo(301)); Assert.That(next.AlarmChanges.Count, Is.EqualTo(300));
            Assert.That(later.MeasurementsLive, Is.False); Assert.That(later.MeasurementReconnects, Is.EqualTo(2)); Assert.That(later.AlarmReconnects, Is.EqualTo(1));
            Assert.That(later.Reads, Is.SameAs(first.Reads));
        }
    }
}
