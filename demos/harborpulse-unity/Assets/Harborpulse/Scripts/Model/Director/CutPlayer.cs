// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Text.Json;
using DeviceChain.Demos.Hud;

namespace DeviceChain.Harborpulse.Model.Director
{
    /// <summary>One caption a cut authored, and what the recording made of it.</summary>
    public sealed class CutTraceEntry
    {
        public string Cut, Page, Shot, Caption, Kind, Text, Reason;
        public int Segment;
        public bool Overlay, Shown;
        /// <summary>The cut's own clock, and the recorded second it maps to.</summary>
        public double ShowS, RecordedWallS;
        /// <summary>The records that satisfied a claim (their event keys), in observation order.</summary>
        public IReadOnlyList<string> Evidence = Array.Empty<string>();
        /// <summary>For an unmet claim: the first recorded second at which it would have held, if any in the day.</summary>
        public double? FirstHoldsAtS;
    }

    /// <summary>One claim of a voice-over take, checked against the recording.</summary>
    public sealed class VoClaimCheck
    {
        public string Claim, Reason;
        public bool Held;
        public IReadOnlyList<string> Evidence = Array.Empty<string>();
        public IReadOnlyDictionary<string, string> Bound = new Dictionary<string, string>();
    }
    public sealed class VoTakeCheck
    {
        public string Take, Text;
        public double DurS;
        /// <summary>Every claim of the take is supported by the recording at the instant the line ends.</summary>
        public bool Licensed;
        public IReadOnlyList<VoClaimCheck> Claims = Array.Empty<VoClaimCheck>();
    }
    /// <summary>One voice-over line of a cut: the take the recording licenses, when it starts, and how every take fared.</summary>
    public sealed class VoCue
    {
        public string Cut, Line, Page, Shot, Take, Text;
        public int Segment, TakeIndex;
        /// <summary>The line's start and end on the cut's own clock.</summary>
        public double ShowS, DurS, EndS;
        public IReadOnlyList<VoTakeCheck> Takes = Array.Empty<VoTakeCheck>();
    }

    /// <summary>What one frame of a cut shows. Everything on it comes from the storyboard and the recording.</summary>
    public sealed class CutFrame
    {
        public int Segment { get; internal set; }
        public CutSegment Authored { get; internal set; }
        public StoryPage Page { get; internal set; }
        public StoryShot Shot { get; internal set; }
        public double ShowS { get; internal set; }
        public double IntoS { get; internal set; }
        public double RecordedWallS { get; internal set; }
        public double Sim { get; internal set; }
        public CameraPose From { get; internal set; }
        public CameraPose To { get; internal set; }
        /// <summary>0..1 along the shot's move, eased.</summary>
        public double Move { get; internal set; }
        /// <summary>The share of the way to its look target the camera has pushed in: a slow move that lasts the shot.</summary>
        public double Push { get; internal set; }
        /// <summary>The device a shot that follows its page's trigger is centred on, or null.</summary>
        public string FollowDevice { get; internal set; }
        public string Caption { get; internal set; } = "";
        public string CaptionKind { get; internal set; } = "";
        public double CaptionAtS { get; internal set; }
        public string Overlay { get; internal set; } = "";
        /// <summary>The small capitals over the caption: the page's place and subject, or for a chapter's first seconds its tag.</summary>
        public string Kicker { get; internal set; } = "";
        /// <summary>Small print under the caption (a disclosure), on one line; empty on the end card, which draws its own.</summary>
        public string Note { get; internal set; } = "";
        /// <summary>The end card's three levels, or null on any other shot.</summary>
        public EndCardContent EndCard { get; internal set; }
        /// <summary>"×12" when the shot runs faster or slower than it was recorded, else "".</summary>
        public string RateChip { get; internal set; } = "";
        /// <summary>The disclosure of a shot borrowed out of order ("later that day"), else "".</summary>
        public string BorrowedChip { get; internal set; } = "";
        /// <summary>0..1: how much of the previous shot's last frame still shows (a chapter's dissolve). 0 on a hard cut.</summary>
        public double Dissolve { get; internal set; }
        /// <summary>0..1: how much of the film's own first frame shows over its last seconds (the loop's seam).</summary>
        public double LoopSeam { get; internal set; }
        public IReadOnlyList<RailNode> Rail { get; internal set; } = Array.Empty<RailNode>();
        public IReadOnlyCollection<string> Links { get; internal set; } = Array.Empty<string>();
        /// <summary>The show second at which the chain card starts to fade, or null while it stays.</summary>
        public double? CardExitAtS { get; internal set; }
        /// <summary>The device tokens named in the world in this shot.</summary>
        public IReadOnlyList<string> Tags { get; internal set; } = Array.Empty<string>();
        /// <summary>The voice-over line being spoken at this instant, or null.</summary>
        public VoCue Vo { get; internal set; }
    }

    /// <summary>
    /// The cut renderer's director: plays one authored cut of the storyboard over a recorded day. It selects nothing: the cut
    /// lists its shots and when each caption appears. It decides one thing per caption, whether the recording supports it at
    /// the recorded instant the caption appears, with the same predicates the live director uses. A claim the recording does
    /// not support at that instant is not shown, and the trace says so and when (if ever) the day would have supported it.
    /// A voice-over line is decided the same way: of its takes, the one that plays is the first whose every claim the recording
    /// supports by the instant the line ends.
    /// </summary>
    public sealed class CutPlayer
    {
        /// <summary>A chain card gains at most one row in this many seconds.</summary>
        public const double RowGapS = 2;
        /// <summary>The card stays this long after its shot's voice-over line before it starts to fade.</summary>
        public const double CardLingerS = 1;

        sealed class Line { public CutCaption At; public StoryCaption Caption; public string Text; public double AppearS, EndS; }
        sealed class Plan
        {
            public CutSegment Segment; public StoryPage Page; public StoryShot Shot; public double StartS;
            public readonly List<Line> Captions = new List<Line>(), Overlays = new List<Line>();
            public string FollowDevice; public VoCue Vo; public bool OpensChapter; public string ChapterTag = ""; public double? CardExitAtS;
        }
        sealed class Node { public ChainNode Authored; public string Label; public double AtS, EvidenceS, InstantS; public int Plan; }
        readonly Storyboard story;
        readonly RecordedDay day;
        readonly PresentationMap map;
        readonly HarborLayout layout;
        readonly List<Plan> plans = new List<Plan>();
        readonly List<CutTraceEntry> trace = new List<CutTraceEntry>();
        readonly List<VoCue> cues = new List<VoCue>();
        readonly Dictionary<string, Observation> triggers = new Dictionary<string, Observation>(StringComparer.Ordinal);
        readonly Dictionary<string, List<Node>> rails = new Dictionary<string, List<Node>>(StringComparer.Ordinal);
        public StoryCut Cut { get; }
        public double DurationS { get; }
        public IReadOnlyList<CutTraceEntry> Trace => trace;
        /// <summary>Every voice-over line of the cut, with the take the recording licenses.</summary>
        public IReadOnlyList<VoCue> Vo => cues;

        public CutPlayer(Storyboard story, string cut, RecordedDay day, PresentationMap map)
        {
            this.story = story; this.day = day; this.map = map; layout = map.Layout;
            Cut = story.Cuts.FirstOrDefault(c => c.Id == cut) ?? throw new FormatException("the storyboard has no cut " + cut);
            var start = 0.0; var shown = new Dictionary<string, HashSet<string>>(StringComparer.Ordinal); string chapter = null;
            foreach (var segment in Cut.Segments)
            {
                if (segment.RecordedWallToS > day.EndS) throw new FormatException("cut " + cut + " shot " + segment.Shot + " reads recorded second " + N(segment.RecordedWallToS) + ", after the recording ends at " + N(day.EndS));
                var page = story.Page(segment.Page);
                var plan = new Plan { Segment = segment, Page = page, Shot = page.Shots.First(s => s.Id == segment.Shot), StartS = start, OpensChapter = segment.Chapter != chapter };
                chapter = segment.Chapter; plan.ChapterTag = plan.OpensChapter ? Cut.Chapters.First(c => c.Id == chapter).Tag : "";
                if (!shown.TryGetValue(page.Id, out var pageShown)) shown.Add(page.Id, pageShown = new HashSet<string>(StringComparer.Ordinal));
                // Captions authored for the same instant and slot are alternatives, strongest first. A caption appears at its authored
                // instant, or as soon after it as the recording supports it while its hold still fits in the shot; the strongest
                // alternative the shot's span supports at all is the one shown.
                var free = 0.0;
                foreach (var group in segment.Captions.GroupBy(c => N(c.AtS) + "|" + page.Captions.First(p => p.Id == c.Id).Slot).OrderBy(g => g.First().AtS))
                {
                    Line chosen = null;
                    foreach (var at in group)
                    {
                        var line = Resolve(plan, at, false, pageShown, Math.Max(at.AtS, free), chosen);
                        if (line != null) { chosen = line; plan.Captions.Add(line); pageShown.Add(at.Id); free = line.AppearS + at.HoldS; }
                    }
                }
                for (var i = 0; i < plan.Captions.Count; i++)
                    plan.Captions[i].EndS = i + 1 < plan.Captions.Count ? plan.Captions[i + 1].AppearS : segment.DurS;
                foreach (var at in segment.Overlays) { var line = Resolve(plan, at, true, pageShown, at.AtS, null); if (line != null) { line.EndS = segment.DurS; plan.Overlays.Add(line); } }
                if (plan.Shot.FollowDeviceFromTrigger != "none" && triggers.TryGetValue(page.Id, out var trigger) && trigger != null) plan.FollowDevice = trigger.Device;
                if (segment.Vo != null) { plan.Vo = Decide(plan, plans.Count); cues.Add(plan.Vo); }
                plans.Add(plan); start += segment.DurS;
            }
            DurationS = start;
            // A chain card leaves with its chain: after the line of the last shot that shows it, never sitting through the next beat.
            for (var i = 0; i < plans.Count; i++)
            {
                var p = plans[i]; if (p.Segment.Nodes.Count == 0) continue;
                var continues = i + 1 < plans.Count && plans[i + 1].Page == p.Page && plans[i + 1].Segment.Nodes.Count > 0;
                if (continues) continue;
                var latest = p.StartS + p.Segment.DurS - ChainCardState.ExitFadeS;
                p.CardExitAtS = p.Vo == null ? latest : Math.Min(latest, p.Vo.EndS + CardLingerS);
            }
        }
        static string N(double value) => value.ToString("0.###", CultureInfo.InvariantCulture);
        EvidenceContext Context(StoryPage page, double wallS, string source, Observation trigger)
            => new EvidenceContext(day.Run, page.FromSim, page.ToSim, wallS, source, trigger, day.SimAt(wallS), layout.DayLengthSeconds);
        /// <summary>The page's trigger records observed by the instant, the one already chosen for the page first.</summary>
        IEnumerable<Observation> Triggers(StoryPage page, double wallS)
        {
            if (page.Trigger == null) return new Observation[] { null };
            var result = page.Trigger.Evaluate(day.Store, Context(page, wallS, EvidenceSource.Platform, null));
            var records = result.Holds ? result.Records.ToList() : new List<Observation>();
            // The trigger chosen for the page comes first, but only once the recording holds it at this instant: a record observed
            // later must not license anything said earlier. Before any trigger, a claim is read without one, so a claim that names
            // none (the well is filling, before the alarm that will trigger its page) can hold, and one that names it cannot.
            if (triggers.TryGetValue(page.Id, out var chosen) && chosen != null && records.RemoveAll(o => o.EventKey == chosen.EventKey) > 0) records.Insert(0, chosen);
            if (records.Count == 0) records.Add(null);
            return records;
        }
        EvidenceResult Holds(StoryPage page, Needs needs, string source, double wallS, out Observation trigger)
        {
            trigger = null;
            foreach (var candidate in Triggers(page, wallS))
            {
                var result = needs.Evaluate(day.Store, Context(page, wallS, source, candidate));
                if (result.Holds) { trigger = candidate; return result; }
            }
            return new EvidenceResult();
        }
        Line Resolve(Plan plan, CutCaption at, bool overlay, HashSet<string> pageShown, double earliestS, Line stronger)
        {
            var caption = plan.Page.Captions.First(c => c.Id == at.Id); var segment = plan.Segment;
            var entry = new CutTraceEntry { Cut = Cut.Id, Segment = plans.Count, Page = plan.Page.Id, Shot = plan.Shot.Id, Caption = caption.Id, Kind = caption.Kind, Overlay = overlay,
                ShowS = plan.StartS + at.AtS, RecordedWallS = segment.RecordedAt(at.AtS) };
            trace.Add(entry);
            if (stronger != null) { entry.Reason = "not offered: " + stronger.Caption.Id + " is shown in its place"; return null; }
            if (caption.UnlessShown != null && pageShown.Contains(caption.UnlessShown)) { entry.Reason = "not offered: " + caption.UnlessShown + " was shown"; return null; }
            var evidence = new EvidenceResult { Holds = true }; var appear = earliestS; Observation trigger = null;
            var room = earliestS <= segment.DurS - at.HoldS + 1e-9;
            if (!room) { entry.Reason = "no room left in the shot for its hold"; return null; }
            if (caption.Needs != null)
            {
                evidence = new EvidenceResult();
                // The last instant a caption may appear leaves its whole hold inside the shot.
                for (appear = earliestS; appear <= segment.DurS - at.HoldS + 1e-9; appear += 0.1)
                {
                    evidence = Holds(plan.Page, caption.Needs, caption.Source, segment.RecordedAt(appear), out trigger);
                    if (evidence.Holds) break;
                }
                if (!evidence.Holds)
                {
                    var until = segment.RecordedAt(segment.DurS - at.HoldS);
                    entry.Reason = "the recording does not support it by recorded second " + N(until) + ", the last instant that leaves its hold";
                    for (var t = Math.Ceiling(until); t <= day.EndS; t += 1)
                        if (Holds(plan.Page, caption.Needs, caption.Source, t, out _).Holds) { entry.FirstHoldsAtS = t; break; }
                    return null;
                }
                if (trigger != null) triggers[plan.Page.Id] = trigger;
            }
            entry.Shown = true; entry.ShowS = plan.StartS + appear; entry.RecordedWallS = segment.RecordedAt(appear);
            entry.Text = caption.Render(evidence); entry.Evidence = evidence.Records.Select(o => o.EventKey).ToArray();
            return new Line { At = at, Caption = caption, Text = entry.Text, AppearS = appear };
        }

        /// <summary>
        /// One take of a line against the recording: every claim is read at the recorded instant the take ends, so nothing is said
        /// before the record that supports it has been seen. A claim that binds a spoken number must have bound exactly that number.
        /// </summary>
        VoTakeCheck Check(Plan plan, VoTake take, bool adoptTrigger)
        {
            var segment = plan.Segment; var wallS = segment.RecordedAt(Math.Min(segment.DurS, segment.Vo.AtS + take.DurS));
            var checks = new List<VoClaimCheck>();
            foreach (var claim in take.Claims)
            {
                var caption = plan.Page.Captions.FirstOrDefault(c => c.Id == claim.Id && c.Kind == "claim");
                var node = caption == null ? plan.Page.Nodes.First(n => n.Id == claim.Id) : null;
                var result = Holds(plan.Page, caption?.Needs ?? node.Needs, caption?.Source ?? node.Source, wallS, out var trigger);
                var check = new VoClaimCheck { Claim = claim.Id, Held = result.Holds, Evidence = result.Records.Select(o => o.EventKey).ToArray(), Bound = result.Bindings.ToDictionary(b => b.Key, b => b.Value, StringComparer.Ordinal) };
                if (!result.Holds) check.Reason = "the recording does not support it by recorded second " + N(wallS) + ", when the line ends";
                else
                    foreach (var want in claim.Bound)
                        if (!result.Bindings.TryGetValue(want.Key, out var got) || got != want.Value)
                        { check.Held = false; check.Reason = "the take says " + want.Key + " = " + want.Value + "; the recording bound " + (got ?? "nothing"); break; }
                if (check.Held && adoptTrigger && trigger != null && !triggers.ContainsKey(plan.Page.Id)) triggers[plan.Page.Id] = trigger;
                checks.Add(check);
            }
            return new VoTakeCheck { Take = take.Id, Text = take.Text, DurS = take.DurS, Claims = checks, Licensed = checks.All(c => c.Held) };
        }
        VoCue Decide(Plan plan, int index)
        {
            var vo = plan.Segment.Vo; var takes = vo.Takes.Select(t => Check(plan, t, true)).ToArray();
            var played = Array.FindIndex(takes, t => t.Licensed); // the last take needs no claim, so one always is
            return new VoCue { Cut = Cut.Id, Line = vo.Id, Page = plan.Page.Id, Shot = plan.Shot.Id, Segment = index, TakeIndex = played, Take = takes[played].Take, Text = takes[played].Text,
                ShowS = plan.StartS + vo.AtS, DurS = takes[played].DurS, EndS = plan.StartS + vo.AtS + takes[played].DurS, Takes = takes };
        }

        /// <summary>
        /// The frame check of the voice-over. <paramref name="played"/> is the take an assembly plays for each line (line id to take
        /// id); with none given, the cut's own choice is checked. Each line is decided again from the recording, and a problem is
        /// reported for a take played while one of its claims is unmet, for a weaker take played while the recording licenses a
        /// stronger one, and for a line or a take the cut does not have. An empty answer means every line plays the take its gate allows.
        /// </summary>
        public IReadOnlyList<string> VoProblems(IReadOnlyDictionary<string, string> played = null)
        {
            var problems = new List<string>();
            foreach (var plan in plans.Where(p => p.Segment.Vo != null))
            {
                var vo = plan.Segment.Vo; var takes = vo.Takes.Select(t => Check(plan, t, false)).ToArray();
                var allowed = Array.FindIndex(takes, t => t.Licensed);
                string id;
                if (played == null) id = plan.Vo.Take;
                else if (!played.TryGetValue(vo.Id, out id)) { problems.Add(Cut.Id + " line " + vo.Id + ": no take is played"); continue; }
                var at = Array.FindIndex(takes, t => t.Take == id);
                if (at < 0) { problems.Add(Cut.Id + " line " + vo.Id + ": the cut has no take " + id); continue; }
                if (!takes[at].Licensed)
                {
                    var unmet = takes[at].Claims.First(c => !c.Held);
                    problems.Add(Cut.Id + " line " + vo.Id + ": take " + id + " is played while its claim " + unmet.Claim + " is unmet (" + unmet.Reason + ")");
                }
                else if (at != allowed) problems.Add(Cut.Id + " line " + vo.Id + ": take " + id + " is played, but the recording licenses " + takes[allowed].Take);
            }
            if (played != null)
                foreach (var line in played.Keys.Where(k => !plans.Any(p => p.Segment.Vo?.Id == k))) problems.Add(Cut.Id + ": the cut has no voice-over line " + line);
            return problems;
        }
        /// <summary>The line-to-take choices of a vo_cues.json document.</summary>
        public static IReadOnlyDictionary<string, string> ReadVoCues(string json)
        {
            try
            {
                using (var doc = JsonDocument.Parse(json))
                    return doc.RootElement.GetProperty("lines").EnumerateArray().ToDictionary(l => l.GetProperty("line").GetString(), l => l.GetProperty("take").GetString(), StringComparer.Ordinal);
            }
            catch (Exception e) when (e is JsonException || e is InvalidOperationException || e is KeyNotFoundException || e is ArgumentException) { throw new FormatException("vo_cues.json: " + e.Message, e); }
        }

        /// <summary>
        /// Why a claim of a page (a claim caption or a chain row) holds or does not at a recorded second: the predicate's parts, one a
        /// line (see <see cref="Needs.Explain"/>). For re-timing a cut against a new day.
        /// </summary>
        public string Explain(string page, string claim, double wallS)
        {
            var p = story.Page(page); var caption = p.Captions.FirstOrDefault(c => c.Id == claim && c.Needs != null); var node = caption == null ? p.Nodes.FirstOrDefault(n => n.Id == claim) : null;
            if (caption == null && node == null) throw new FormatException("page " + page + " has no claim " + claim);
            var trigger = Triggers(p, wallS).FirstOrDefault();
            return (caption?.Needs ?? node.Needs).Explain(day.Store, Context(p, wallS, caption?.Source ?? node.Source, trigger));
        }
        int PlanAt(double showS)
        {
            for (var i = 0; i < plans.Count; i++) if (showS < plans[i].StartS + plans[i].Segment.DurS) return i;
            return plans.Count - 1;
        }
        /// <summary>
        /// The frame at a show second. Frames must be asked for in increasing order: the chain card gains its rows as the cut
        /// plays (at most one every <see cref="RowGapS"/> seconds), and a row's evidence is read at the frame's recorded instant.
        /// </summary>
        public CutFrame Frame(double showS)
        {
            showS = Math.Max(0, Math.Min(DurationS, showS));
            var index = PlanAt(showS); var plan = plans[index]; var segment = plan.Segment; var into = Math.Min(segment.DurS, showS - plan.StartS);
            var wallS = segment.RecordedAt(into);
            var frame = new CutFrame { Segment = index, Authored = segment, Page = plan.Page, Shot = plan.Shot, ShowS = showS, IntoS = into, RecordedWallS = wallS, Sim = day.SimAt(wallS),
                From = map.Pose(segment.From ?? plan.Shot.From), To = map.Pose(segment.To ?? plan.Shot.To), FollowDevice = plan.FollowDevice, Tags = plan.Shot.Tags, CardExitAtS = plan.CardExitAtS };
            var move = segment.MoveS <= 0 ? 1 : Math.Max(0, Math.Min(1, into / segment.MoveS)); frame.Move = move * move * (3 - 2 * move);
            frame.Push = segment.Push * Math.Max(0, Math.Min(1, into / segment.DurS));
            var line = plan.Captions.LastOrDefault(c => c.AppearS <= into && into < c.EndS + 1e-9);
            if (line != null) { frame.Caption = line.Text; frame.CaptionKind = line.Caption.Kind; frame.CaptionAtS = plan.StartS + line.AppearS; }
            var overlays = plan.Overlays.Where(o => o.At.AtS <= into).ToArray();
            frame.Overlay = string.Join("\n", overlays.Select(o => o.Text));
            frame.Kicker = plan.ChapterTag.Length > 0 && into < StoryCut.ChapterTagS ? plan.ChapterTag : plan.Page.ContextLine;
            if (segment.EndCard)
            {
                string Level(params string[] slots) => string.Join(" ", overlays.Where(o => slots.Contains(o.Caption.Slot)).Select(o => o.Text.Replace("\n", " ")));
                frame.EndCard = new EndCardContent(Level("bridge"), string.Join("\n", overlays.Where(o => o.Caption.Slot == "tagline").Select(o => o.Text)), Level("subline"), Level("fiction", "positions"));
            }
            else frame.Note = string.Join(" ", overlays.Select(o => o.Text.Replace("\n", " ")));
            frame.RateChip = TopStrip.Rate(segment.Rate);
            if (segment.Borrowed != "none") frame.BorrowedChip = segment.Borrowed;
            if (index > 0 && segment.DissolveS > 0 && into < segment.DissolveS) frame.Dissolve = 1 - into / segment.DissolveS;
            if (Cut.LoopSeamS > 0 && showS > DurationS - Cut.LoopSeamS) frame.LoopSeam = Math.Min(1, (showS - (DurationS - Cut.LoopSeamS)) / Cut.LoopSeamS);
            if (plan.Vo != null && showS >= plan.Vo.ShowS && showS < plan.Vo.EndS) frame.Vo = plan.Vo;
            Rail(frame, index, plan, wallS);
            return frame;
        }
        /// <summary>When a row's fact happened, on the recording's clock: the newest of its records' own instants (a snapshot has only the instant it was read).</summary>
        static double Instant(EvidenceResult result, double fallback)
            => result.Records.Select(o => o.HasOccurrence ? o.OccurredAtS : o.ObservedAtS).DefaultIfEmpty(fallback).Max();
        void Rail(CutFrame frame, int index, Plan plan, double wallS)
        {
            if (!rails.TryGetValue(plan.Page.Id, out var rail)) rails.Add(plan.Page.Id, rail = new List<Node>());
            // A card belongs to one run of shots of its page; a later visit to the page starts it again.
            if (rail.Count > 0 && rail[rail.Count - 1].Plan < index - 1 && plans.Skip(rail[rail.Count - 1].Plan + 1).Take(index - rail[rail.Count - 1].Plan - 1).Any(p => p.Page != plan.Page)) rail.Clear();
            foreach (var n in rail) n.Plan = index;
            if (rail.Count == 0 || frame.ShowS - rail[rail.Count - 1].AtS >= RowGapS)
                foreach (var authored in plan.Page.Nodes)
                {
                    if (!plan.Segment.Nodes.Contains(authored.Id) || rail.Any(n => n.Authored == authored)) continue;
                    // A node authored to follow the page's trigger waits for it, as it does live: the well "falling" is a node of
                    // the pump's start, and a dip in the level an hour earlier is not it.
                    if (authored.AfterTriggerS > 0 && !Triggers(plan.Page, wallS).Any(t => t != null && t.OccurredAtS + authored.AfterTriggerS <= wallS)) continue;
                    var result = Holds(plan.Page, authored.Needs, authored.Source, wallS, out var trigger);
                    if (!result.Holds) continue;
                    if (trigger != null && !triggers.ContainsKey(plan.Page.Id)) triggers[plan.Page.Id] = trigger;
                    var label = authored.Label; foreach (var b in result.Bindings) label = label.Replace("{" + b.Key + "}", b.Value);
                    if (label.Contains("{")) continue;
                    rail.Add(new Node { Authored = authored, Label = label, AtS = frame.ShowS, Plan = index, EvidenceS = result.Records.Select(o => o.ObservedAtS).DefaultIfEmpty(wallS).Max(), InstantS = Instant(result, wallS) });
                    break;
                }
            // A row that binds a number counts what the recording holds NOW: records that arrive one read after another (three
            // notifications, then six) move the number on the row, which never keeps the first count it saw.
            foreach (var n in rail)
            {
                if (!n.Authored.Label.Contains("{")) continue;
                var again = Holds(plan.Page, n.Authored.Needs, n.Authored.Source, wallS, out _);
                if (!again.Holds) continue;
                var text = n.Authored.Label; foreach (var b in again.Bindings) text = text.Replace("{" + b.Key + "}", b.Value);
                if (!text.Contains("{")) { n.Label = text; n.InstantS = Instant(again, wallS); }
            }
            var nodes = new List<RailNode>();
            // only the rows this shot lists: a shot may show part of its page's chain
            var shown = rail.Where(n => plan.Segment.Nodes.Contains(n.Authored.Id)).ToArray();
            for (var i = 0; i < shown.Length; i++)
            {
                var n = shown[i];
                // the cue is the recorded time since the row above, in real seconds: the platform's own latency
                nodes.Add(new RailNode { Id = n.Authored.Id, Label = n.Label, Source = n.Authored.Source, AtS = n.AtS, SincePreviousS = 0, CueS = i == 0 ? (double?)null : n.InstantS - shown[i - 1].InstantS });
            }
            frame.Rail = nodes;
            // A link is drawn only between two nodes whose records were observed in that order.
            frame.Links = plan.Page.Links.Where(l => rail.Any(n => n.Authored.Id == l[0]) && rail.Any(n => n.Authored.Id == l[1])
                && rail.First(n => n.Authored.Id == l[0]).EvidenceS <= rail.First(n => n.Authored.Id == l[1]).EvidenceS).Select(l => l[0] + "->" + l[1]).ToArray();
        }

        static void WriteCue(Utf8JsonWriter w, VoCue c, bool checks)
        {
            w.WriteStartObject(); w.WriteString("line", c.Line); w.WriteNumber("segment", c.Segment); w.WriteString("page", c.Page); w.WriteString("shot", c.Shot);
            w.WriteNumber("showS", Math.Round(c.ShowS, 3)); w.WriteNumber("durS", c.DurS); w.WriteNumber("endS", Math.Round(c.EndS, 3)); w.WriteString("take", c.Take); w.WriteNumber("takeIndex", c.TakeIndex); w.WriteString("text", c.Text);
            if (checks)
            {
                w.WriteStartArray("takes");
                foreach (var t in c.Takes)
                {
                    w.WriteStartObject(); w.WriteString("take", t.Take); w.WriteBoolean("licensed", t.Licensed); w.WriteBoolean("played", t.Take == c.Take); w.WriteString("text", t.Text);
                    w.WriteStartArray("claims");
                    foreach (var claim in t.Claims)
                    {
                        w.WriteStartObject(); w.WriteString("claim", claim.Claim); w.WriteBoolean("held", claim.Held);
                        if (claim.Reason != null) w.WriteString("reason", claim.Reason);
                        w.WriteStartObject("bound"); foreach (var b in claim.Bound.OrderBy(b => b.Key, StringComparer.Ordinal)) w.WriteString(b.Key, b.Value); w.WriteEndObject();
                        w.WriteStartArray("evidence"); foreach (var key in claim.Evidence) w.WriteStringValue(key); w.WriteEndArray();
                        w.WriteEndObject();
                    }
                    w.WriteEndArray(); w.WriteEndObject();
                }
                w.WriteEndArray();
            }
            w.WriteEndObject();
        }
        /// <summary>
        /// frame_check.json: every caption the cut authored, whether it was shown, and the records behind each claim; and every
        /// voice-over line with each of its takes, the claims that license it, and the take that plays.
        /// </summary>
        public string TraceJson() => DirectorWire.Json(w =>
        {
            w.WriteStartObject(); w.WriteString("cut", Cut.Id); w.WriteString("run", day.Run);
            w.WriteNumber("claimsShown", trace.Count(e => e.Shown && e.Kind == "claim")); w.WriteNumber("claimsUnmet", trace.Count(e => !e.Shown && e.Reason != null && !e.Reason.StartsWith("not offered", StringComparison.Ordinal)));
            w.WriteStartArray("captions");
            foreach (var e in trace)
            {
                w.WriteStartObject(); w.WriteNumber("showS", e.ShowS); w.WriteNumber("segment", e.Segment); w.WriteString("page", e.Page); w.WriteString("shot", e.Shot); w.WriteString("caption", e.Caption);
                w.WriteString("kind", e.Kind); w.WriteBoolean("overlay", e.Overlay); w.WriteBoolean("shown", e.Shown); w.WriteNumber("recordedWallS", e.RecordedWallS);
                if (e.Text != null) w.WriteString("text", e.Text);
                if (e.Reason != null) w.WriteString("reason", e.Reason);
                if (e.FirstHoldsAtS.HasValue) w.WriteNumber("firstHoldsAtRecordedS", e.FirstHoldsAtS.Value);
                w.WriteStartArray("evidence"); foreach (var key in e.Evidence) w.WriteStringValue(key); w.WriteEndArray();
                w.WriteEndObject();
            }
            w.WriteEndArray();
            var problems = VoProblems();
            w.WriteNumber("voLines", cues.Count); w.WriteNumber("voClaimTakes", cues.Count(c => c.Takes[c.TakeIndex].Claims.Count > 0)); w.WriteNumber("voFallbackTakes", cues.Count(c => c.TakeIndex > 0));
            w.WriteString("voCheck", problems.Count == 0 ? "pass" : "FAIL");
            w.WriteStartArray("voProblems"); foreach (var p in problems) w.WriteStringValue(p); w.WriteEndArray();
            w.WriteStartArray("vo"); foreach (var c in cues) WriteCue(w, c, true); w.WriteEndArray();
            w.WriteEndObject();
        });
        /// <summary>vo_cues.json: for the audio assembly, each line's id, the take to play, and when it starts on the cut's clock.</summary>
        public string VoCuesJson() => DirectorWire.Json(w =>
        {
            w.WriteStartObject(); w.WriteString("cut", Cut.Id); w.WriteString("run", day.Run); w.WriteNumber("durationS", DurationS);
            w.WriteStartArray("lines"); foreach (var c in cues) WriteCue(w, c, false); w.WriteEndArray();
            w.WriteEndObject();
        });
        /// <summary>render.json: every segment of the cut, its recorded span, its rate and its disclosures.</summary>
        public string RenderJson(int fps, int width, int height) => DirectorWire.Json(w =>
        {
            w.WriteStartObject(); w.WriteString("cut", Cut.Id); w.WriteString("run", day.Run); w.WriteNumber("fps", fps); w.WriteNumber("width", width); w.WriteNumber("height", height);
            w.WriteNumber("durationS", DurationS); w.WriteNumber("frames", (int)Math.Round(DurationS * fps));
            w.WriteStartArray("segments");
            foreach (var p in plans)
            {
                w.WriteStartObject(); w.WriteNumber("showFromS", p.StartS); w.WriteNumber("durS", p.Segment.DurS); w.WriteString("chapter", p.Segment.Chapter); w.WriteString("page", p.Page.Id); w.WriteString("shot", p.Shot.Id);
                w.WriteNumber("recordedWallFromS", p.Segment.RecordedWallFromS); w.WriteNumber("recordedWallToS", p.Segment.RecordedWallToS); w.WriteNumber("rate", p.Segment.Rate);
                w.WriteBoolean("rateChip", Math.Abs(p.Segment.Rate - 1) > 0.01); w.WriteString("borrowed", p.Segment.Borrowed); w.WriteString("cutIn", p == plans[0] ? "start" : p.Segment.DissolveS > 0 ? "dissolve" : "hard");
                w.WriteNumber("simFrom", day.SimAt(p.Segment.RecordedWallFromS)); w.WriteNumber("simTo", day.SimAt(p.Segment.RecordedWallToS));
                if (p.Vo != null) { w.WriteString("vo", p.Vo.Line); w.WriteString("voTake", p.Vo.Take); }
                w.WriteStartArray("captions"); foreach (var c in p.Captions) w.WriteStringValue(c.Text); w.WriteEndArray();
                w.WriteStartArray("overlays"); foreach (var c in p.Overlays) w.WriteStringValue(c.Text); w.WriteEndArray();
                w.WriteEndObject();
            }
            w.WriteEndArray(); w.WriteEndObject();
        });
    }
}
