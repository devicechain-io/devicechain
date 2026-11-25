// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Text.Json;

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
        /// <summary>The device a shot that follows its page's trigger is centred on, or null.</summary>
        public string FollowDevice { get; internal set; }
        public string Caption { get; internal set; } = "";
        public string CaptionKind { get; internal set; } = "";
        public double CaptionAtS { get; internal set; }
        public string Overlay { get; internal set; } = "";
        /// <summary>"x12" when the shot runs faster or slower than it was recorded, else "".</summary>
        public string RateChip { get; internal set; } = "";
        /// <summary>The disclosure of a shot borrowed out of order ("later that day"), else "".</summary>
        public string BorrowedChip { get; internal set; } = "";
        /// <summary>0..1: how much of the previous shot's last frame still shows (the seam's dissolve).</summary>
        public double Dissolve { get; internal set; }
        public IReadOnlyList<RailNode> Rail { get; internal set; } = Array.Empty<RailNode>();
        public IReadOnlyCollection<string> Links { get; internal set; } = Array.Empty<string>();
    }

    /// <summary>
    /// The cut renderer's director: plays one authored cut of the storyboard over a recorded day. It selects nothing: the cut
    /// lists its shots and when each caption appears. It decides one thing per caption, whether the recording supports it at
    /// the recorded instant the caption appears, with the same predicates the live director uses. A claim the recording does
    /// not support at that instant is not shown, and the trace says so and when (if ever) the day would have supported it.
    /// </summary>
    public sealed class CutPlayer
    {
        sealed class Line { public CutCaption At; public StoryCaption Caption; public string Text; public double AppearS, EndS; }
        sealed class Plan
        {
            public CutSegment Segment; public StoryPage Page; public StoryShot Shot; public double StartS;
            public readonly List<Line> Captions = new List<Line>(), Overlays = new List<Line>();
            public string FollowDevice;
        }
        sealed class Node { public ChainNode Authored; public string Label; public double AtS, EvidenceS; public int Plan; }
        readonly Storyboard story;
        readonly RecordedDay day;
        readonly PresentationMap map;
        readonly HarborLayout layout;
        readonly List<Plan> plans = new List<Plan>();
        readonly List<CutTraceEntry> trace = new List<CutTraceEntry>();
        readonly Dictionary<string, Observation> triggers = new Dictionary<string, Observation>(StringComparer.Ordinal);
        readonly Dictionary<string, List<Node>> rails = new Dictionary<string, List<Node>>(StringComparer.Ordinal);
        public StoryCut Cut { get; }
        public double DurationS { get; }
        public IReadOnlyList<CutTraceEntry> Trace => trace;

        public CutPlayer(Storyboard story, string cut, RecordedDay day, PresentationMap map)
        {
            this.story = story; this.day = day; this.map = map; layout = map.Layout;
            Cut = story.Cuts.FirstOrDefault(c => c.Id == cut) ?? throw new FormatException("the storyboard has no cut " + cut);
            var start = 0.0; var shown = new Dictionary<string, HashSet<string>>(StringComparer.Ordinal);
            foreach (var segment in Cut.Segments)
            {
                if (segment.RecordedWallToS > day.EndS) throw new FormatException("cut " + cut + " shot " + segment.Shot + " reads recorded second " + N(segment.RecordedWallToS) + ", after the recording ends at " + N(day.EndS));
                var page = story.Page(segment.Page);
                var plan = new Plan { Segment = segment, Page = page, Shot = page.Shots.First(s => s.Id == segment.Shot), StartS = start };
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
                plans.Add(plan); start += segment.DurS;
            }
            DurationS = start;
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
            if (triggers.TryGetValue(page.Id, out var chosen) && chosen != null) { records.RemoveAll(o => o.EventKey == chosen.EventKey); records.Insert(0, chosen); }
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
        int PlanAt(double showS)
        {
            for (var i = 0; i < plans.Count; i++) if (showS < plans[i].StartS + plans[i].Segment.DurS) return i;
            return plans.Count - 1;
        }
        /// <summary>
        /// The frame at a show second. Frames must be asked for in increasing order: the chain rail gains its nodes as the cut
        /// plays (at most one every three seconds, like the live rail), and a node's evidence is read at the frame's recorded instant.
        /// </summary>
        public CutFrame Frame(double showS)
        {
            showS = Math.Max(0, Math.Min(DurationS, showS));
            var index = PlanAt(showS); var plan = plans[index]; var segment = plan.Segment; var into = Math.Min(segment.DurS, showS - plan.StartS);
            var wallS = segment.RecordedAt(into);
            var frame = new CutFrame { Segment = index, Authored = segment, Page = plan.Page, Shot = plan.Shot, ShowS = showS, IntoS = into, RecordedWallS = wallS, Sim = day.SimAt(wallS),
                From = map.Pose(plan.Shot.From), To = map.Pose(plan.Shot.To), FollowDevice = plan.FollowDevice };
            var move = segment.MoveS <= 0 ? 1 : Math.Max(0, Math.Min(1, into / segment.MoveS)); frame.Move = move * move * (3 - 2 * move);
            var line = plan.Captions.LastOrDefault(c => c.AppearS <= into && into < c.EndS + 1e-9);
            if (line != null) { frame.Caption = line.Text; frame.CaptionKind = line.Caption.Kind; frame.CaptionAtS = plan.StartS + line.AppearS; }
            frame.Overlay = string.Join("\n", plan.Overlays.Where(o => o.At.AtS <= into).Select(o => o.Text));
            var rate = segment.Rate;
            if (Math.Abs(rate - 1) > 0.01) frame.RateChip = "x" + (rate >= 10 ? rate.ToString("0", CultureInfo.InvariantCulture) : rate.ToString("0.#", CultureInfo.InvariantCulture));
            if (segment.Borrowed != "none") frame.BorrowedChip = segment.Borrowed;
            if (index > 0 && segment.DissolveS > 0 && into < segment.DissolveS) frame.Dissolve = 1 - into / segment.DissolveS;
            Rail(frame, index, plan, wallS);
            return frame;
        }
        void Rail(CutFrame frame, int index, Plan plan, double wallS)
        {
            if (!rails.TryGetValue(plan.Page.Id, out var rail)) rails.Add(plan.Page.Id, rail = new List<Node>());
            // A rail belongs to one run of shots of its page; a later visit to the page starts it again.
            if (rail.Count > 0 && rail[rail.Count - 1].Plan < index - 1 && plans.Skip(rail[rail.Count - 1].Plan + 1).Take(index - rail[rail.Count - 1].Plan - 1).Any(p => p.Page != plan.Page)) rail.Clear();
            foreach (var n in rail) n.Plan = index;
            if (rail.Count == 0 || frame.ShowS - rail[rail.Count - 1].AtS >= 3)
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
                    rail.Add(new Node { Authored = authored, Label = label, AtS = frame.ShowS, Plan = index, EvidenceS = result.Records.Select(o => o.ObservedAtS).DefaultIfEmpty(wallS).Max() });
                    break;
                }
            var nodes = new List<RailNode>();
            foreach (var n in rail.Skip(Math.Max(0, rail.Count - 5)))
            {
                var node = new RailNode { Id = n.Authored.Id, Label = n.Label, Source = n.Authored.Source, AtS = n.AtS, SincePreviousS = 0 };
                nodes.Add(node);
            }
            frame.Rail = nodes;
            // A link is drawn only between two nodes whose records were observed in that order.
            frame.Links = plan.Page.Links.Where(l => rail.Any(n => n.Authored.Id == l[0]) && rail.Any(n => n.Authored.Id == l[1])
                && rail.First(n => n.Authored.Id == l[0]).EvidenceS <= rail.First(n => n.Authored.Id == l[1]).EvidenceS).Select(l => l[0] + "->" + l[1]).ToArray();
        }

        /// <summary>frame_check.json: every caption the cut authored, whether it was shown, and the records behind each claim.</summary>
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
            w.WriteEndArray(); w.WriteEndObject();
        });
        /// <summary>render.json: every segment of the cut, its recorded span, its rate and its disclosures.</summary>
        public string RenderJson(int fps, int width, int height) => DirectorWire.Json(w =>
        {
            w.WriteStartObject(); w.WriteString("cut", Cut.Id); w.WriteString("run", day.Run); w.WriteNumber("fps", fps); w.WriteNumber("width", width); w.WriteNumber("height", height);
            w.WriteNumber("durationS", DurationS); w.WriteNumber("frames", (int)Math.Round(DurationS * fps));
            w.WriteStartArray("segments");
            foreach (var p in plans)
            {
                w.WriteStartObject(); w.WriteNumber("showFromS", p.StartS); w.WriteNumber("durS", p.Segment.DurS); w.WriteString("page", p.Page.Id); w.WriteString("shot", p.Shot.Id);
                w.WriteNumber("recordedWallFromS", p.Segment.RecordedWallFromS); w.WriteNumber("recordedWallToS", p.Segment.RecordedWallToS); w.WriteNumber("rate", p.Segment.Rate);
                w.WriteBoolean("rateChip", Math.Abs(p.Segment.Rate - 1) > 0.01); w.WriteString("borrowed", p.Segment.Borrowed);
                w.WriteNumber("simFrom", day.SimAt(p.Segment.RecordedWallFromS)); w.WriteNumber("simTo", day.SimAt(p.Segment.RecordedWallToS));
                w.WriteStartArray("captions"); foreach (var c in p.Captions) w.WriteStringValue(c.Text); w.WriteEndArray();
                w.WriteStartArray("overlays"); foreach (var c in p.Overlays) w.WriteStringValue(c.Text); w.WriteEndArray();
                w.WriteEndObject();
            }
            w.WriteEndArray(); w.WriteEndObject();
        });
    }
}
