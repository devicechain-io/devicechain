// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.IO.Compression;
using System.Linq;
using DeviceChain.Demos.Hud;
using DeviceChain.Harborpulse.Model;
using DeviceChain.Harborpulse.Model.Director;
using NUnit.Framework;

namespace DeviceChain.Harborpulse.Tests
{
    /// <summary>
    /// The cut renderer's model against a real day: the clean live day recorded on 2026-11-25 (Fixtures/day-20261125.ndjson.gz,
    /// thinned to the readings the storyboard's predicates name and to a sample of the repeating snapshots; the reduced day gives the
    /// same trace as the whole recording). HARBORPULSE_UPDATE_FIXTURES=1 rewrites the expected trace.
    /// </summary>
    public class RecordedDayCutTests
    {
        static RecordedDay day; static PresentationMap map; static Storyboard story;
        static string Fixture(string name) => Path.Combine(DirectorKit.FixtureDirectory(), name);
        static string DayText(string name = "day-20261125.ndjson.gz")
        {
            using (var file = File.OpenRead(Fixture(name))) using (var gz = new GZipStream(file, CompressionMode.Decompress)) using (var reader = new StreamReader(gz))
                return reader.ReadToEnd();
        }
        static RecordedDay Day => day ?? (day = RecordedDay.Load(new StringReader(DayText())));
        // The first recorded day (2026-11-24) was made before a recording sampled alarm severities and the truck's fixes.
        static RecordedDay oldDay;
        static RecordedDay OldDay => oldDay ?? (oldDay = RecordedDay.Load(new StringReader(DayText("day-20261124.ndjson.gz"))));
        static PresentationMap Map => map ?? (map = new PresentationMap(Kit.Layout()));
        static Storyboard Story => story ?? (story = Storyboard.Parse(DirectorKit.StoryText(), Map));
        static string S(double v) => v.ToString("0.0", CultureInfo.InvariantCulture);
        static List<CutFrame> Play(CutPlayer player, int fps)
        {
            var frames = new List<CutFrame>(); var count = (int)Math.Round(player.DurationS * fps);
            for (var i = 0; i < count; i++) frames.Add(player.Frame(i / (double)fps));
            return frames;
        }
        static IEnumerable<string> Lines(CutPlayer player) => player.Trace.Select(e => player.Cut.Id + "|" + S(e.ShowS) + "|" + e.Page + "|" + e.Shot + "|" + e.Caption + "|" + (e.Overlay ? "overlay" : e.Kind) + "|"
            + (e.Shown ? "SHOWN|" + S(e.RecordedWallS) + "|" + e.Text.Replace("\n", " / ") : "unmet|" + e.Reason + (e.FirstHoldsAtS.HasValue ? " (first holds at " + S(e.FirstHoldsAtS.Value) + ")" : "")));

        [Test]
        public void BothCutsOverTheRecordedDayShowExactlyTheseCaptions()
        {
            var actual = new[] { "c90", "c300" }.SelectMany(cut => { var p = new CutPlayer(Story, cut, Day, Map); Play(p, 30); return Lines(p); }).ToArray();
            var path = Fixture("cut-trace-20261125.txt");
            if (Environment.GetEnvironmentVariable("HARBORPULSE_UPDATE_FIXTURES") == "1") File.WriteAllLines(path, actual);
            Assert.That(actual, Is.EqualTo(File.ReadAllLines(path)));
        }

        [TestCase("c90", 90)]
        [TestCase("c300", 300)]
        public void EveryShownCaptionHoldsThreeSecondsAndEveryClaimNamesItsRecords(string cut, double target)
        {
            var player = new CutPlayer(Story, cut, Day, Map); var frames = Play(player, 30);
            Assert.That(player.DurationS, Is.EqualTo(target).Within(0.5));
            // runs of one caption text within one segment
            var runs = new List<KeyValuePair<string, int>>();
            for (var i = 0; i < frames.Count; i++)
            {
                var key = frames[i].Segment + "|" + frames[i].Caption;
                if (runs.Count > 0 && runs[runs.Count - 1].Key == key) runs[runs.Count - 1] = new KeyValuePair<string, int>(key, runs[runs.Count - 1].Value + 1);
                else runs.Add(new KeyValuePair<string, int>(key, 1));
            }
            foreach (var run in runs.Where(r => !r.Key.EndsWith("|", StringComparison.Ordinal)))
                Assert.That(run.Value, Is.GreaterThanOrEqualTo(89), run.Key + " is visible for less than 3 s");
            foreach (var e in player.Trace.Where(e => e.Shown && e.Kind == "claim"))
                Assert.That(e.Evidence, Is.Not.Empty, e.Caption + " is shown without a record");
            foreach (var e in player.Trace.Where(e => e.Shown))
                Assert.That(frames.Any(f => f.Caption == e.Text || f.Overlay.Split('\n').Contains(e.Text.Split('\n')[0])), e.Caption + " is in the trace and on no frame");
            // every frame's caption is one the trace accounts for
            var accounted = new HashSet<string>(player.Trace.Where(e => e.Shown).Select(e => e.Text));
            foreach (var f in frames.Where(f => f.Caption.Length > 0)) Assert.That(accounted, Does.Contain(f.Caption));
        }

        [Test]
        public void ARateChipMarksEverySegmentThatDoesNotRunAtTheRecordedRate()
        {
            foreach (var cut in new[] { "c90", "c300" })
            {
                var player = new CutPlayer(Story, cut, Day, Map);
                foreach (var f in Play(player, 10))
                {
                    Assert.That(f.RateChip.Length > 0, Is.EqualTo(Math.Abs(f.Authored.Rate - 1) > 0.01), cut + " " + f.Shot.Id);
                    Assert.That(f.BorrowedChip, Is.EqualTo(f.Authored.Borrowed == "none" ? "" : f.Authored.Borrowed));
                    Assert.That(f.RecordedWallS, Is.InRange(f.Authored.RecordedWallFromS, f.Authored.RecordedWallToS));
                    Assert.That(f.Sim, Is.EqualTo(6 + f.RecordedWallS / 60).Within(0.05), "the runner's clock at the recorded instant");
                }
                using (var doc = System.Text.Json.JsonDocument.Parse(player.RenderJson(30, 1920, 1080)))
                    Assert.That(doc.RootElement.GetProperty("segments").GetArrayLength(), Is.EqualTo(player.Cut.Segments.Count));
            }
        }

        [Test]
        public void TheSameShowSecondIsTheSameFrameAtThirtyAndSixtyFramesASecond()
        {
            var thirty = Play(new CutPlayer(Story, "c90", Day, Map), 30); var sixty = Play(new CutPlayer(Story, "c90", Day, Map), 60);
            for (var i = 0; i < thirty.Count; i++)
            {
                var a = thirty[i]; var b = sixty[i * 2];
                Assert.That(b.RecordedWallS, Is.EqualTo(a.RecordedWallS)); Assert.That(b.Caption, Is.EqualTo(a.Caption)); Assert.That(b.Overlay, Is.EqualTo(a.Overlay));
                Assert.That(b.Move, Is.EqualTo(a.Move)); Assert.That(b.Dissolve, Is.EqualTo(a.Dissolve));
                Assert.That(b.Rail.Select(n => n.Id + n.Label), Is.EqualTo(a.Rail.Select(n => n.Id + n.Label)), "rail at " + S(a.ShowS));
                Assert.That(b.Links, Is.EquivalentTo(a.Links));
            }
        }

        [Test]
        public void ThePumpChainsRailGainsItsNodesInTheOrderTheRecordsCame()
        {
            var frames = Play(new CutPlayer(Story, "c90", Day, Map), 30); var seen = new List<string>();
            foreach (var f in frames.Where(f => f.Page.Id == "4")) foreach (var n in f.Rail) if (!seen.Contains(n.Id)) seen.Add(n.Id);
            Assert.That(seen, Is.EqualTo(new[] { "well", "command", "running", "falling" }));
            var filling = frames.Last(f => f.Shot.Id == "4d");
            Assert.That(filling.Rail, Is.Empty, "the well is still filling: the alarm that starts the chain has not been raised, and an earlier dip in its level is not the pump's doing");
            var started = frames.Last(f => f.Shot.Id == "4f");
            Assert.That(started.Links, Is.SupersetOf(new[] { "well->command", "command->running" }));
            // no arrow ever joins a gauge to a pump, and the short cut carries no gauge node at all
            Assert.That(frames.SelectMany(f => f.Rail).Select(n => n.Id), Has.No.Member("flood").And.No.Member("water"));
        }

        // CONTROL (the storyboard's frame-check acceptance): strip the records a claim binds to, and that claim, by name, is the one that goes.
        [Test]
        public void StrippingThePumpStartRecordsRemovesExactlyTheClaimsThatStandOnThem()
        {
            var stripped = RecordedDay.Load(new StringReader(string.Join("\n", DayText().Split('\n').Where(l => !(l.Contains("\"kind\":\"chip\"") && l.Contains("\"status\":\"PLATFORM\""))))));
            var whole = new CutPlayer(Story, "c90", Day, Map); var control = new CutPlayer(Story, "c90", stripped, Map);
            string[] Shown(CutPlayer p) => p.Trace.Where(e => e.Shown).Select(e => e.Caption).ToArray();
            Assert.That(Shown(whole), Does.Contain("platform-start"));
            // the caption stands on the chip record, so it goes with it; nothing else does
            Assert.That(Shown(whole).Except(Shown(control)).ToArray(), Is.EqualTo(new[] { "platform-start" }));
            Assert.That(Shown(control).Except(Shown(whole)), Is.Empty, "nothing may take the place of a claim the recording does not support");
            Assert.That(control.Trace.First(e => e.Caption == "platform-start").Reason, Does.StartWith("the recording does not support it"));
            // and the narrator: the line that says the platform started the pump, before its float would, falls to the take that says neither
            Assert.That(whole.Vo.Single(c => c.Line == "c90-E").Take, Is.EqualTo("c90-E-claim"));
            var fallen = control.Vo.Single(c => c.Line == "c90-E"); Assert.That(fallen.Take, Is.EqualTo("c90-E-floor"));
            Assert.That(fallen.Text, Does.Not.Contain("platform")); Assert.That(fallen.Takes[0].Claims.Where(c => !c.Held).Select(c => c.Claim), Is.EquivalentTo(new[] { "platform-start", "before-float" }));
            // every other line of the cut says what it said
            Assert.That(control.Vo.Where(c => c.Line != "c90-E").Select(c => c.Take), Is.EqualTo(whole.Vo.Where(c => c.Line != "c90-E").Select(c => c.Take)));
            Assert.That(control.VoProblems(), Is.Empty);
        }

        static RecordedDay Without(Func<string, bool> row) => RecordedDay.Load(new StringReader(string.Join("\n", DayText().Split('\n').Where(l => !row(l)))));
        static string Take(CutPlayer p, string line) => p.Vo.Single(c => c.Line == line).Take;

        [Test]
        public void EveryVoiceOverLineOfBothCutsPlaysItsClaimTakeOnTheRecordedDay()
        {
            foreach (var cut in new[] { "c90", "c300" })
            {
                var p = new CutPlayer(Story, cut, Day, Map);
                Assert.That(p.Vo.Count, Is.EqualTo(p.Cut.Segments.Count), "one line a shot");
                foreach (var c in p.Vo)
                {
                    Assert.That(c.TakeIndex, Is.EqualTo(0), cut + " " + c.Line + " falls back: " + string.Join("; ", c.Takes[0].Claims.Where(x => !x.Held).Select(x => x.Claim + ": " + x.Reason)));
                    Assert.That(c.EndS + StoryCut.VoTailS, Is.LessThanOrEqualTo(p.Cut.Segments.Take(c.Segment + 1).Sum(s => s.DurS) + 1e-9), c.Line + " runs into the next shot");
                    foreach (var claim in c.Takes[0].Claims) Assert.That(claim.Evidence, Is.Not.Empty, c.Line + " states " + claim.Claim + " with no record behind it");
                }
                Assert.That(p.VoProblems(), Is.Empty);
                // the spoken numbers are the recorded ones
                var numbers = p.Vo.SelectMany(c => c.Takes[0].Claims).Where(c => c.Bound.Count > 0).ToDictionary(c => c.Claim, c => c.Bound);
                Assert.That(numbers["baseline"]["n"], Is.EqualTo("43")); Assert.That(numbers["absence-count"]["n"], Is.EqualTo("6")); Assert.That(numbers["absence-notified"]["n"], Is.EqualTo("6"));
                Assert.That(numbers["ota-summary"]["updated"], Is.EqualTo("5")); Assert.That(numbers["ota-summary"]["refused"], Is.EqualTo("1")); Assert.That(numbers["cap"]["cap"], Is.EqualTo("2"));
            }
        }

        // CONTROLS: take away the records a new claim stands on, and the line that states it falls back; the frame check agrees with itself.
        [Test]
        public void EachNewClaimsControlTakesItsLineToAFallback()
        {
            // a seventh silent device (silence-north-lamp): the count is seven, so the take that says "six" is not licensed
            var lines = DayText().Split('\n').ToList();
            var at = lines.FindIndex(l => l.Contains("\"kind\":\"alarm\"") && l.Contains("\"key\":\"light-silent\"") && l.Contains("hp-light-east-1") && l.Contains("\"status\":\"RAISED\""));
            lines.Insert(at + 1, lines[at].Replace("hp-light-east-1", "hp-light-north-1").Replace("\"identity\":\"", "\"identity\":\"north-"));
            var seventh = RecordedDay.Load(new StringReader(string.Join("\n", lines)));
            var p = new CutPlayer(Story, "c90", seventh, Map);
            var h = p.Vo.Single(c => c.Line == "c90-H"); Assert.That(h.Take, Is.EqualTo("c90-H-fb1"));
            Assert.That(h.Takes[0].Claims.Single(c => c.Claim == "absence-count").Reason, Does.Contain("the take says n = 6; the recording bound 7"));
            Assert.That(h.Text, Does.Not.Contain("six"));
            // no mail sent (smtp-sink-stopped): no notification is claimed anywhere
            var silentSink = Without(l => l.Contains("\"kind\":\"attempt\"") && l.Contains("\"status\":\"SENT\""));
            Assert.That(Take(new CutPlayer(Story, "c90", silentSink, Map), "c90-H"), Is.EqualTo("c90-H-fb2"));
            var long1 = new CutPlayer(Story, "c300", silentSink, Map);
            Assert.That(Take(long1, "c300-20"), Is.EqualTo("c300-20-fb1"), "the air lock's alarm, without its notification");
            Assert.That(Take(long1, "c300-06b"), Is.EqualTo("c300-06b-fb1"), "the fault under maintenance, without the lamp that had no window");
            Assert.That(Take(long1, "c300-07"), Is.EqualTo("c300-07-floor")); Assert.That(Take(long1, "c300-23"), Is.EqualTo("c300-23-floor"));
            // the refused update disabled (ota-failure-disabled): nothing is refused, so nothing "keeps working"
            var noFailure = Without(l => l.Contains("\"status\":\"FAILED\"") || l.Contains("VERIFICATION_FAILED") || l.Contains("\"afterFailure\":\"true\""));
            Assert.That(Take(new CutPlayer(Story, "c90", noFailure, Map), "c90-L"), Is.EqualTo("c90-L-floor"));
            // one buffered reading never stored (drop-backfill): "every buffered reading" goes, the rest of the line stays
            var first = DayText().Split('\n').Last(l => l.Contains("\"key\":\"backfill\"") && l.Contains("\"kind\":\"measure\""));
            var id = first.Substring(first.IndexOf("\"readingIdentity\":\"", StringComparison.Ordinal) + 19); id = id.Substring(0, id.IndexOf('"'));
            var dropped = Without(l => l.Contains("\"kind\":\"measure\"") && l.Contains("\"readingIdentity\":\"" + id + "\""));
            Assert.That(Take(new CutPlayer(Story, "c90", dropped, Map), "c90-J"), Is.EqualTo("c90-J-fb1"));
            Assert.That(Take(new CutPlayer(Story, "c90", Day, Map), "c90-J"), Is.EqualTo("c90-J-claim"));
        }

        [Test]
        public void TheFrameCheckRefusesATakeThatDoesNotMatchItsGate()
        {
            var p = new CutPlayer(Story, "c90", Day, Map);
            var played = CutPlayer.ReadVoCues(p.VoCuesJson());
            Assert.That(played.Count, Is.EqualTo(14)); Assert.That(p.VoProblems(played), Is.Empty, "the cut's own cues pass");
            // an assembly plays the fallback where the recording licenses the claim take
            var weaker = played.ToDictionary(x => x.Key, x => x.Value); weaker["c90-E"] = "c90-E-floor";
            Assert.That(p.VoProblems(weaker).Single(), Is.EqualTo("c90 line c90-E: take c90-E-floor is played, but the recording licenses c90-E-claim"));
            // an assembly plays a claim take over a day that does not support it: the one that must never ship
            var stripped = new CutPlayer(Story, "c90", Without(l => l.Contains("\"kind\":\"chip\"") && l.Contains("\"status\":\"PLATFORM\"")), Map);
            var problem = stripped.VoProblems(played).Single();
            Assert.That(problem, Does.StartWith("c90 line c90-E: take c90-E-claim is played while its claim platform-start is unmet"));
            // a line left out, a take or a line the cut does not have
            var missing = played.Where(x => x.Key != "c90-M").ToDictionary(x => x.Key, x => x.Value); Assert.That(p.VoProblems(missing).Single(), Does.Contain("c90-M: no take is played"));
            var unknown = played.ToDictionary(x => x.Key, x => x.Value); unknown["c90-A"] = "c90-A-claim"; unknown["c90-Z"] = "c90-Z-line";
            Assert.That(p.VoProblems(unknown), Is.EquivalentTo(new[] { "c90 line c90-A: the cut has no take c90-A-claim", "c90: the cut has no voice-over line c90-Z" }));
            Assert.Throws<FormatException>(() => CutPlayer.ReadVoCues("{\"lines\":[{\"line\":\"x\"}]}")); Assert.Throws<FormatException>(() => CutPlayer.ReadVoCues("not json"));
            // frame_check.json carries every line, each take's claims, and the verdict
            using (var doc = System.Text.Json.JsonDocument.Parse(p.TraceJson()))
            {
                var root = doc.RootElement; Assert.That(root.GetProperty("voCheck").GetString(), Is.EqualTo("pass")); Assert.That(root.GetProperty("voLines").GetInt32(), Is.EqualTo(14));
                var e = root.GetProperty("vo").EnumerateArray().Single(l => l.GetProperty("line").GetString() == "c90-E");
                var claimTake = e.GetProperty("takes").EnumerateArray().First();
                Assert.That(claimTake.GetProperty("licensed").GetBoolean(), Is.True); Assert.That(claimTake.GetProperty("played").GetBoolean(), Is.True);
                Assert.That(claimTake.GetProperty("claims").EnumerateArray().Select(c => c.GetProperty("claim").GetString()), Is.EqualTo(new[] { "platform-start", "before-float", "command" }));
                Assert.That(claimTake.GetProperty("claims").EnumerateArray().All(c => c.GetProperty("evidence").GetArrayLength() > 0), Is.True);
            }
        }

        [Test]
        public void ALaterRecordNeverLicensesAnEarlierInstant()
        {
            // The pump's page is triggered by its alarm. Before the alarm a claim that names no trigger can hold (the well is filling);
            // one that names it cannot, even once the trigger has been chosen later in the cut.
            var p = new CutPlayer(Story, "c90", Day, Map); Play(p, 10);
            var d = p.Cut.Segments.First(s => s.Vo.Id == "c90-D");
            Assert.That(p.Explain("4", "well-rising", d.RecordedWallToS), Does.StartWith("measure HOLDS"));
            Assert.That(p.Explain("4", "platform-start", d.RecordedWallToS), Does.StartWith("chip unmet"));
            Assert.That(p.Explain("4", "platform-start", p.Cut.Segments.First(s => s.Vo.Id == "c90-E").RecordedWallToS), Does.StartWith("chip HOLDS"));
            Assert.That(p.VoProblems(), Is.Empty, "deciding again after the whole cut has played gives the same takes");
            Assert.Throws<FormatException>(() => p.Explain("4", "no-such-claim", 700));
        }

        [Test]
        public void TheChainCardBuildsOneRecordedStepAtATimeWithRealSecondsAndLeavesWithItsLine()
        {
            var p = new CutPlayer(Story, "c90", Day, Map); var frames = Play(p, 30);
            // the pump chain: each row arrives when its record does, no sooner than RowGapS after the one before
            var pump = frames.Where(f => f.Page.Id == "4").ToArray(); var first = new Dictionary<string, double>();
            foreach (var f in pump) foreach (var n in f.Rail) if (!first.ContainsKey(n.Id)) first[n.Id] = f.ShowS;
            Assert.That(first.Keys, Is.EqualTo(new[] { "well", "command", "running", "falling" }));
            var times = first.Values.ToArray(); for (var i = 1; i < times.Length; i++) Assert.That(times[i] - times[i - 1], Is.GreaterThanOrEqualTo(CutPlayer.RowGapS - 1e-9));
            // the cues are the platform's own latency in real seconds, whatever rate the shot runs at
            var rows = pump.Last(f => f.Authored.Vo.Id == "c90-E").Rail;
            Assert.That(rows.Take(3).Select(n => n.Label), Is.EqualTo(new[] { "Well above alarm level", "Start command sent", "Pump running" }));
            Assert.That(rows.Take(3).Select(n => ChainCardState.CueText(n.CueS)), Is.EqualTo(new[] { "", "+1 s", "+4 s" }));
            // the outage chain: a scenario trigger, then counts of what was recorded
            var outage = frames.Last(f => f.Authored.Vo.Id == "c90-H").Rail.Select(n => n.Row).ToArray();
            Assert.That(outage.Select(r => r.Label), Is.EqualTo(new[] { "East Quay loses power", "6 absence alarms", "6 notifications sent" }));
            Assert.That(outage[0].Kind, Is.EqualTo(ChainRowKind.ScenarioTrigger)); // (the scenario's silence window is 45 s; the recorded gaps are this day's: alarms 40 s after the feeder dropped, notifications 8 s after them)
            Assert.That(outage[1].CueS.Value, Is.InRange(35, 50)); Assert.That(outage[2].CueS.Value, Is.InRange(1, 20));
            Assert.That(ChainCardState.CueText(outage[1].CueS), Does.Match("^[+][0-9]+ s$"));
            // the restore: the sender's count keeps its own mark
            var restore = frames.Last(f => f.Authored.Vo.Id == "c90-J").Rail.Select(n => n.Row).ToArray();
            Assert.That(restore.Single(r => r.Label == "Asked to slow down").Kind, Is.EqualTo(ChainRowKind.SenderCount));
            Assert.That(restore.Select(r => r.Label), Does.Contain("All buffered readings stored"));
            // a card leaves after the line of its chain's last shot and never sits through the next beat
            foreach (var cut in new[] { "c90", "c300" })
            {
                var player = cut == "c90" ? p : new CutPlayer(Story, cut, Day, Map); var all = cut == "c90" ? frames : Play(player, 10);
                foreach (var f in all.Where(f => f.CardExitAtS.HasValue))
                {
                    var end = f.ShowS - f.IntoS + f.Authored.DurS;
                    Assert.That(f.CardExitAtS.Value + ChainCardState.ExitFadeS, Is.LessThanOrEqualTo(end + 1e-9), cut + " " + f.Shot.Id + ": the card is gone when its shot ends");
                    var vo = player.Vo.Single(c => c.Segment == f.Segment); Assert.That(f.CardExitAtS.Value, Is.LessThanOrEqualTo(vo.EndS + CutPlayer.CardLingerS + 1e-9));
                }
                // every frame's rows are rows its shot lists, at most the card's four after windowing
                foreach (var f in all) { Assert.That(f.Rail.Select(n => n.Id), Is.SubsetOf(f.Authored.Nodes)); Assert.That(ChainCardState.Window(f.Rail.Select(n => n.Row).ToArray(), ChainCardState.MaxRows).Count, Is.LessThanOrEqualTo(4)); }
            }
        }

        [Test]
        public void TheTitleTheChapterTagsAndTheEndCard()
        {
            var p = new CutPlayer(Story, "c90", Day, Map); var frames = Play(p, 30);
            // the title, with the disclosure under it for the whole dawn shot
            var dawn = frames.Where(f => f.Authored.Vo.Id == "c90-B").ToArray();
            Assert.That(dawn.Last().Caption, Is.EqualTo("One harbour, one day, one platform."));
            Assert.That(dawn.All(f => f.Note == "A simulated day on a live DeviceChain instance, recorded and sped up"), Is.True);
            Assert.That(dawn.First().From, Is.Not.SameAs(dawn.First().To), "the crane: from high over the water down to HOME");
            Assert.That(dawn.First().Move, Is.EqualTo(0)); Assert.That(dawn.Last().Move, Is.GreaterThan(0.99));
            // a chapter's tag takes the kicker for its first 2.5 s, then the page's own place and subject come back
            var act = frames.Where(f => f.Authored.Vo.Id == "c90-C").ToArray();
            Assert.That(act.Where(f => f.IntoS < StoryCut.ChapterTagS).All(f => f.Kicker == "1 · Act"), Is.True);
            Assert.That(act.Where(f => f.IntoS >= StoryCut.ChapterTagS).All(f => f.Kicker == Story.Page("4").ContextLine), Is.True);
            Assert.That(frames.Where(f => f.Authored.Vo.Id == "c90-D").All(f => f.Kicker == Story.Page("4").ContextLine), Is.True, "only a chapter's first shot carries its tag");
            Assert.That(frames.Where(f => f.Kicker == "2 · Notice").Select(f => f.Authored.Vo.Id).Distinct(), Is.EqualTo(new[] { "c90-G" }));
            Assert.That(frames.Where(f => f.Kicker == "3 · Update").Select(f => f.Authored.Vo.Id).Distinct(), Is.EqualTo(new[] { "c90-K" }));
            // dissolves only where time jumps between chapters; everything else is a hard cut
            foreach (var group in frames.GroupBy(f => f.Segment)) Assert.That(group.Any(f => f.Dissolve > 0), Is.EqualTo(group.Key > 0 && group.First().Authored.DissolveS > 0));
            // slow pushes last their shot
            var push = frames.Where(f => f.Authored.Vo.Id == "c90-J").ToArray(); Assert.That(push.First().Push, Is.EqualTo(0)); Assert.That(push.Last().Push, Is.GreaterThan(0.11));
            // the end card, in three levels; the bridge first, the rest with it from 2.5 s
            var end = frames.Where(f => f.EndCard != null).ToArray(); Assert.That(end.Select(f => f.Authored.Vo.Id).Distinct(), Is.EqualTo(new[] { "c90-N" }));
            Assert.That(end.First().EndCard.Bridge, Is.EqualTo("Every rule you saw was written and published by a person.")); Assert.That(end.First().EndCard.Tagline, Is.EqualTo(""));
            var card = end.Last().EndCard;
            Assert.That(card.Tagline, Is.EqualTo("Agents read the fleet.\nPeople approve the action.")); Assert.That(card.Subline, Is.EqualTo("DeviceChain: open-source IoT platform, ready for AI agents."));
            Assert.That(card.SmallPrint, Is.EqualTo("Port Alder is fictional. The district is drawn compact; the platform holds each device's own position."));
            Assert.That(end.All(f => f.Note == "" && f.Caption == ""), Is.True);
            // the loop's seam: the last 1.5 s dissolve to the film's own first frame
            Assert.That(frames.Where(f => f.LoopSeam > 0).All(f => f.ShowS > p.DurationS - 1.5), Is.True); Assert.That(frames.Last().LoopSeam, Is.GreaterThan(0.95));
            // the top strip's chips
            Assert.That(frames.First().BorrowedChip, Is.EqualTo("later that day")); Assert.That(frames.Where(f => f.RateChip.Length > 0).All(f => f.RateChip.StartsWith("×", StringComparison.Ordinal)), Is.True);
            // a voice-over line is "being spoken" between its start and its end
            Assert.That(frames.Where(f => f.Vo != null).All(f => f.ShowS >= f.Vo.ShowS && f.ShowS < f.Vo.EndS), Is.True);
            Assert.That(frames.Count(f => f.Vo != null) / 30.0, Is.EqualTo(p.Vo.Sum(c => c.DurS)).Within(1.5));
        }

        [Test]
        public void ACutThatReadsPastTheRecordingIsRefused()
        {
            var cutShort = RecordedDay.Load(new StringReader(string.Join("\n", DayText().Split('\n').Where(l => l.Length > 0 && double.Parse(l.Substring(9, l.IndexOf(',') - 9), CultureInfo.InvariantCulture) < 900))));
            var e = Assert.Throws<FormatException>(() => new CutPlayer(Story, "c90", cutShort, Map));
            Assert.That(e.Message, Does.Contain("after the recording ends"));
        }

        [Test]
        public void TheCompactStreamIsTheSameDay()
        {
            var text = new StringWriter { NewLine = "\n" }; Day.WriteCompact(text);
            var again = RecordedDay.Load(new StringReader(text.ToString()));
            var a = new CutPlayer(Story, "c300", Day, Map); var b = new CutPlayer(Story, "c300", again, Map);
            Play(a, 10); Play(b, 10);
            Assert.That(Lines(b), Is.EqualTo(Lines(a)));
            Assert.That(again.Run, Is.EqualTo(Day.Run)); Assert.That(again.Epoch, Is.EqualTo(Day.Epoch));
        }

        [Test]
        public void TheReplaySourceAnswersFromTheRecordingAndLeavesTheRestAbsent()
        {
            var layout = Kit.Layout(); var source = new ReplaySource(OldDay, layout);
            string Id(string token) => layout.Devices.First(d => d.Token == token).ExternalId;
            source.Seek(600);
            Assert.That(source.Status.StartedAt, Is.EqualTo(OldDay.StartedAt)); Assert.That(source.Status.SimSeconds / 3600, Is.EqualTo(16).Within(0.05));
            Assert.That(source.TryGet(Id("hp-pump-north"), Metrics.WetWell, out var well), Is.True);
            Assert.That(well.OccurredAt, Is.LessThanOrEqualTo(source.Now)); Assert.That(source.Now - well.OccurredAt, Is.LessThan(TimeSpan.FromSeconds(15)));
            Assert.That(source.Alarms.Any(a => a.Key == "wet-well-high" && a.Active), Is.False, "not raised yet at 16:00");
            // 17:35: the alarm, and the command the platform's rule sent, with the platform's own instants
            source.Seek(696);
            var alarm = source.Alarms.Single(a => a.Device == Id("hp-pump-north") && a.Key == "wet-well-high");
            Assert.That(alarm.Active, Is.True); Assert.That(alarm.Severity, Is.EqualTo(ReplaySource.UnrecordedSeverity), "this recording did not sample severities");
            var start = source.Commands.Single(c => c.Device == Id("hp-pump-north") && c.Name == "start-pump" && c.Status == "SUCCESSFUL" && c.ObservedAt > source.Now.AddSeconds(-20));
            Assert.That(start.SentTime, Is.LessThanOrEqualTo(start.RespondedTime)); Assert.That(start.IsPlatformRecord, Is.True);
            Assert.That(source.TryLocation(Id("hp-truck-1"), out _), Is.False, "this recording did not sample the truck's fixes");
            Assert.That(source.TryAttribute(Id("hp-pump-north"), "well_alarm_m", out _), Is.False);
            // the outage: nothing new from an East Quay charger, and the absence alarms once the platform had raised them
            source.Seek(890);
            Assert.That(source.Alarms.Count(a => a.Active && (a.Key == "light-silent" || a.Key == "charger-offline")), Is.EqualTo(6));
            // going back rebuilds: the alarms are not there yet
            source.Seek(860);
            Assert.That(source.Alarms.Count(a => a.Active && (a.Key == "light-silent" || a.Key == "charger-offline")), Is.EqualTo(0));
            // the rollout as the platform's records stood at 02:38
            source.Seek(1240);
            Assert.That(source.Rollout.MaxInFlight, Is.EqualTo(2)); Assert.That(source.Rollout.Targets.Count, Is.EqualTo(6));
            Assert.That(source.Rollout.Target("hp-charger-north-1").Outcome, Is.EqualTo("FAILED")); Assert.That(source.Rollout.Progress["succeeded"], Is.EqualTo(1));
            Assert.That(source.Status.Ota.Rollout, Is.EqualTo(source.Rollout.Token));
            // and a frame from it: the observed model takes the replay like any source
            var model = new HarborObservedModel(layout);
            var frame = model.Update(source, new ClockReading(OldDay.SimAt(1240) * 3600, ClockMode.Live, ""), source.Now);
            Assert.That(frame.Rollout.Token, Is.EqualTo(source.Rollout.Token));
            // an alarm whose severity the recording did not sample is drawn as the weakest alarm; another unknown word is still refused
            source.Seek(890); var outage = new HarborObservedModel(layout).Update(source, new ClockReading(OldDay.SimAt(890) * 3600, ClockMode.Live, ""), source.Now);
            Assert.That(outage.Beacons.Count(b => !b.Cleared), Is.GreaterThanOrEqualTo(6));
            foreach (var beacon in outage.Beacons) { Assert.That(beacon.Rings, Is.EqualTo(1)); Assert.That(beacon.Pulse(source.Now), Is.InRange(0, 1)); }
            Assert.Throws<ArgumentException>(() => AlarmBeacons.Period("SEVERE"));
        }

        [Test]
        public void ARecordingThatSamplesSeveritiesAndTruckFixesReplaysThem()
        {
            var layout = Kit.Layout(); var truck = layout.Devices.First(d => d.Kind == DeviceKind.Truck); var gauge = layout.Devices.First(d => d.Token == "hp-gauge-east-1");
            string Row(double at, int seq, string kind, string data) => "{\"wallS\":" + S(at) + ",\"sequence\":" + seq + ",\"kind\":\"" + kind + "\",\"data\":" + data + "}";
            var run = "2026-11-24T20:41:47.0000000+00:00";
            string Obs(Observation o) => DirectorWire.Json(w => DirectorWire.Observation(w, o));
            var lines = new[] {
                Row(0, 1, "observation", Obs(DirectorKit.O("ledger", key: "run", id: "run/" + run, source: EvidenceSource.Scenario, run: run, wall: 0, sim: 6, cycle: ""))),
                Row(5, 2, "observation", Obs(DirectorKit.O("alarm", gauge.Token, "flood-level", "RAISED", sim: 6.08, wall: 5, id: "alarm-1", run: run))),
                Row(0, 3, "world", "{\"sample\":\"/status\",\"simHour\":6,\"alarms\":[]}"),
                Row(6, 4, "world", "{\"sample\":\"/status\",\"simHour\":6.1,\"alarms\":[{\"device\":\"" + gauge.ExternalId + "\",\"key\":\"flood-level\",\"severity\":\"MAJOR\",\"acknowledged\":true}],"
                    + "\"truck\":{\"lat\":49.8,\"lon\":-12.2,\"heading\":90,\"at\":\"2026-11-24T20:41:52.0000000+00:00\"}}") };
            var source = new ReplaySource(RecordedDay.Load(new StringReader(string.Join("\n", lines))), layout);
            source.Seek(5.5);
            Assert.That(source.Alarms.Single().Severity, Is.EqualTo(ReplaySource.UnrecordedSeverity), "not sampled yet");
            Assert.That(source.TryLocation(truck.ExternalId, out _), Is.False);
            source.Seek(6);
            Assert.That(source.Alarms.Single().Severity, Is.EqualTo("MAJOR")); Assert.That(source.Alarms.Single().Acknowledged, Is.True);
            Assert.That(source.TryLocation(truck.ExternalId, out var fix), Is.True); Assert.That(fix.Longitude, Is.EqualTo(-12.2));
            Assert.That(source.TryLocation(gauge.ExternalId, out _), Is.False, "a fix belongs to the truck");
        }

        [Test]
        public void ALateBufferedReadingDoesNotReplaceANewerOne()
        {
            var run = "2026-11-24T20:41:47.0000000+00:00";
            string Row(double at, int seq, string kind, string data) => "{\"wallS\":" + S(at) + ",\"sequence\":" + seq + ",\"kind\":\"" + kind + "\",\"data\":" + data + "}";
            string Reading(int seq, double observed, double occurred, string value) => Row(observed, seq, "observation", DirectorWire.Json(w => DirectorWire.Observation(w,
                DirectorKit.O("measure", "hp-charger-east-1", "power_kw", sim: 6, wall: observed, occurred: occurred, id: "r" + seq, run: run, cycle: "", fields: new Dictionary<string, string> { ["value"] = value }))));
            var lines = new[] {
                Row(0, 1, "observation", DirectorWire.Json(w => DirectorWire.Observation(w, DirectorKit.O("ledger", key: "run", id: "run/" + run, source: EvidenceSource.Scenario, run: run, wall: 0, sim: 6, cycle: "")))),
                Row(0, 2, "world", "{\"sample\":\"/status\",\"simHour\":6}"), Row(60, 3, "world", "{\"sample\":\"/status\",\"simHour\":7}"),
                Reading(4, 10, 10, "7"), Reading(5, 40, 40, "9"), Reading(6, 50, 20, "8") };
            var d = RecordedDay.Load(new StringReader(string.Join("\n", lines)));
            Assert.That(d.SimAt(30), Is.EqualTo(6.5).Within(1e-9)); Assert.That(d.SimAt(-5), Is.EqualTo(6)); Assert.That(d.SimAt(999), Is.EqualTo(7));
            Assert.That(d.TryMeasure("hp-charger-east-1", "power_kw", 5, out _), Is.False);
            Assert.That(d.TryMeasure("hp-charger-east-1", "power_kw", 45, out var before), Is.True); Assert.That(before.Field("value"), Is.EqualTo("9"));
            Assert.That(d.TryMeasure("hp-charger-east-1", "power_kw", 55, out var after), Is.True); Assert.That(after.Field("value"), Is.EqualTo("9"), "the reading taken at second 20 arrived at second 50");
        }
    }
}
