// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;

namespace DeviceChain.Harborpulse.Model.Director
{
    public enum PageState { Dormant, Eligible, Staged, Watching, Playing, Fallback, Presented }
    public enum DirectorMode { Active, Reconnecting, LayoutMismatch }
    public sealed class DirectorEvent
    {
        public DirectorEvent(double showS, string kind, string page, string value, IEnumerable<string> evidence = null)
        { ShowS = showS; Kind = kind; Page = page; Value = value; Evidence = (evidence ?? Array.Empty<string>()).ToArray(); }
        public double ShowS { get; }
        public string Kind { get; }
        public string Page { get; }
        public string Value { get; }
        public IReadOnlyList<string> Evidence { get; }
        /// <summary>For a chain node: when its fact happened, on the recording's clock (not serialized; the chain card's cue reads it).</summary>
        public double? InstantS { get; internal set; }
    }
    public sealed class DirectorSample
    {
        public DirectorSample(string run, double sim, double monotonicS, bool stale = false, bool digestMatches = true, IEnumerable<string> defects = null)
        { Run = run; Sim = sim; MonotonicS = monotonicS; Stale = stale; DigestMatches = digestMatches; Defects = (defects ?? Array.Empty<string>()).ToArray(); }
        public string Run { get; }
        public double Sim { get; }
        public double MonotonicS { get; }
        public bool Stale { get; }
        public bool DigestMatches { get; }
        public IReadOnlyList<string> Defects { get; }
    }

    /// <summary>Pure read-only director: callers supply the monotonic clock, clock status and detached observations.</summary>
    public sealed class Scheduler
    {
        sealed class PageRun
        {
            public StoryPage Page;
            public PageState State;
            public int Shot;
            public int Slot;
            public double StartedS;
            public double ShotS;
            public double CaptionS = double.NegativeInfinity;
            public string Caption;
            public string Text;
            public double CaptionHoldS = 3;
            public bool Provisional;
            public readonly List<Opportunity> Pending = new List<Opportunity>();
            // the captions shown in this presentation of the page (StoryCaption.UnlessShown)
            public readonly HashSet<string> Shown = new HashSet<string>(StringComparer.Ordinal);
            public double TriggerS;
            public bool Triggered;
            public bool Recap;
            public bool Reaction;
            public double LastReactionSim = double.NegativeInfinity;
            public bool Suspended;
            public bool Retired;
            public Observation TriggerRecord;
            public readonly HashSet<string> Nodes = new HashSet<string>(StringComparer.Ordinal);
        }
        sealed class Opportunity { public StoryShot Shot; public string Slot; public double FromS; public double DeadlineS; public string Shown; }
        sealed class QueueEntry { public PageRun Runtime; public double AtS; public bool Resume; }
        readonly Storyboard story;
        readonly HarborLayout layout;
        readonly Dictionary<string, PageRun> pages = new Dictionary<string, PageRun>(StringComparer.Ordinal);
        readonly HashSet<string> consumed = new HashSet<string>(StringComparer.Ordinal);
        readonly List<QueueEntry> queue = new List<QueueEntry>();
        readonly List<DirectorEvent> log = new List<DirectorEvent>();
        PageRun active;
        string run;
        double lastS = double.NegativeInfinity;
        double joinS;
        double currentSim;
        double staleAtS = double.NaN;
        bool staleCaptionPending;
        string ambientPose;
        public DirectorMode Mode { get; private set; }
        public string Banner { get; private set; } = "";
        public string UpNext { get; private set; } = "";
        public string ActivePage => active?.Page.Id;
        public IReadOnlyCollection<string> Consumed => consumed;
        public IReadOnlyList<DirectorEvent> Log => log.AsReadOnly();
        public Scheduler(Storyboard story, HarborLayout layout) { this.story = story; this.layout = layout; }
        public PageState State(string page) => pages.TryGetValue(page, out var p) ? p.State : PageState.Dormant;
        public static double WallOffsetSim(double cueSim, double durationS, double dayLengthSeconds)
        { if (dayLengthSeconds <= 0) throw new ArgumentOutOfRangeException(nameof(dayLengthSeconds)); return cueSim + durationS * 24 / dayLengthSeconds; }
        public double ExpectedSilenceSim(double cueSim) => WallOffsetSim(cueSim, layout.Cues.SilenceS, layout.DayLengthSeconds);
        public double ExpectedAirLockSim => WallOffsetSim(layout.Cues.AirLockFromSim, layout.Cues.AirLockHoldS, layout.DayLengthSeconds);
        public double ExpectedEscalationSim(double cueSim) => WallOffsetSim(cueSim, layout.Cues.EscalateAfterS, layout.DayLengthSeconds);
        void Emit(double now, string kind, PageRun p, string value, IEnumerable<Observation> proof = null)
        {
            var e = new DirectorEvent(now, kind, p?.Page.Id ?? "", value, proof?.Select(o => o.EventKey));
            if (kind == "node" && proof != null) { var rows = proof.ToArray(); if (rows.Length > 0) e.InstantS = rows.Max(o => o.HasOccurrence ? o.OccurredAtS : o.ObservedAtS); }
            log.Add(e);
        }
        void Transition(PageRun p, PageState state, double now)
        { p.State = state; Emit(now, "state", p, state.ToString()); }
        bool Open(PageRun p, double sim) => sim >= p.Page.FromSim && sim <= p.Page.ToSim;
        EvidenceResult Evaluate(Needs needs, PageRun p, ObservationStore store, double now, string source = EvidenceSource.Platform)
            => needs?.Evaluate(store, new EvidenceContext(run, p.Page.FromSim, p.Page.ToSim, now, source, p.TriggerRecord, currentSim, layout.DayLengthSeconds)) ?? new EvidenceResult();
        public void Join(DirectorSample sample, ObservationStore store)
        {
            currentSim = sample.Sim; run = sample.Run; joinS = sample.MonotonicS; lastS = joinS; active = null; consumed.Clear(); queue.Clear(); pages.Clear();
            foreach (var page in story.Pages) pages.Add(page.Id, new PageRun { Page = page });
            foreach (var o in store.Records.Where(o => o.Run == run && o.OccurredAtS < joinS)) consumed.Add(o.EventKey);
            foreach (var p in pages.Values.Where(p => p.Page.Once))
            {
                var terminal = Evaluate(p.Page.Terminal, p, store, joinS);
                if (terminal.Holds && terminal.Records.Any(o => o.ObservedAtS < joinS)) { p.Retired = true; Transition(p, PageState.Presented, joinS); }
            }
            foreach (var p in pages.Values.Where(p => !p.Retired && !p.Page.CutsOnly && Open(p, sample.Sim)))
            {
                var trigger = Evaluate(p.Page.Trigger, p, store, joinS);
                if (trigger.Holds && trigger.Records.Any(o => o.OccurredAtS < joinS))
                { p.Recap = true; p.Triggered = true; p.TriggerRecord = trigger.Records.LastOrDefault(); p.TriggerS = p.TriggerRecord.OccurredAtS; Enqueue(p, joinS, false); }
            }
            Emit(joinS, "join", null, run);
            if (sample.Defects.Count == 0)
            {
                var title = story.Pages.FirstOrDefault(p => p.Id == "0")?.Captions.FirstOrDefault(c => c.Id == "title");
                if (title != null) Emit(joinS, "title", null, string.Join("\n", title.Lines));
            }
        }
        void Enqueue(PageRun p, double now, bool resume)
        {
            if (queue.Any(q => q.Runtime == p) || active == p) return;
            queue.Add(new QueueEntry { Runtime = p, AtS = now, Resume = resume });
            Emit(now, "queue", p, resume ? "resume" : "trigger");
        }
        public IReadOnlyList<DirectorEvent> Tick(DirectorSample sample, ObservationStore store)
        {
            var start = log.Count;
            if (sample.MonotonicS < lastS) throw new ArgumentException("director monotonic clock moved backwards");
            lastS = sample.MonotonicS;
            var now = lastS; currentSim = sample.Sim;
            if (!sample.DigestMatches)
            {
                Mode = DirectorMode.LayoutMismatch; Banner = "layout mismatch: director off";
                Emit(now, "banner", null, Banner); return log.Skip(start).ToArray();
            }
            if (sample.Stale)
            {
                Mode = DirectorMode.Reconnecting; Banner = "reconnecting";
                if (double.IsNaN(staleAtS)) { staleAtS = now; staleCaptionPending = active != null; }
                EndStaleCaption(now);
                if (log.Count == 0 || log.Last().Value != Banner) Emit(now, "banner", null, Banner);
                return log.Skip(start).ToArray();
            }
            if (!double.IsNaN(staleAtS)) { if (active != null) { active.ShotS += now - staleAtS; active.StartedS += now - staleAtS; foreach (var opportunity in active.Pending) { opportunity.FromS += now - staleAtS; opportunity.DeadlineS += now - staleAtS; } } staleAtS = double.NaN; }
            EndStaleCaption(now);
            Mode = DirectorMode.Active;
            if (run != sample.Run) Join(sample, store);
            var banner = sample.Defects.Count > 0 ? "TEST CONTROL: " + string.Join(", ", sample.Defects) : "";
            if (Banner != banner) Emit(now, "banner", null, banner);
            Banner = banner;
            foreach (var p in pages.Values.OrderBy(p => p.Page.Id, StringComparer.Ordinal))
            {
                if (p.Page.CutsOnly || p.Retired) continue;
                if (sample.Sim > p.Page.ToSim && p.Page.FallbackPolicy == "skip-page" && !p.Triggered && p.State != PageState.Presented) { Emit(now, "skip-page", p, "no trigger"); Transition(p, PageState.Presented, now); continue; }
                if (!Open(p, sample.Sim)) continue;
                if (p.State == PageState.Dormant) Transition(p, PageState.Eligible, now);
                var result = Evaluate(p.Page.Trigger, p, store, now);
                var newRecords = result.Records.Where(o => !consumed.Contains(o.EventKey)).ToArray();
                if (result.Holds && newRecords.Length > 0)
                {
                    foreach (var o in result.Records) consumed.Add(o.EventKey);
                    p.TriggerRecord = newRecords.Last();
                    if (p.State == PageState.Presented)
                    {
                        if (p.Page.Once || sample.Sim - p.LastReactionSim < 3) continue;
                        p.LastReactionSim = sample.Sim; p.Reaction = true; p.Shot = 0; p.Slot = 0;
                        Transition(p, PageState.Eligible, now);
                    }
                    p.Triggered = true; p.TriggerS = p.TriggerRecord.OccurredAtS;
                    if (p != active) Enqueue(p, now, false);
                    else if (p.State == PageState.Watching)
                    {
                        Transition(p, PageState.Playing, now);
                        if (p.Page.Shots[p.Shot].FollowDeviceFromTrigger != "none") Emit(now, "follow", p, p.TriggerRecord.Device);
                    }
                }
                var planned = p.Page.PlannedSim.HasValue && sample.Sim >= p.Page.PlannedSim.Value - p.Page.PreRollS * 24 / layout.DayLengthSeconds;
                if (p.Page.FallbackPolicy != "skip-page" && p.State == PageState.Eligible && (planned || Evaluate(p.Page.Watch, p, store, now).Holds)) Enqueue(p, now, false);
            }
            foreach (var q in queue.ToArray())
                if ((!q.Resume && now - (q.Runtime.Triggered ? q.Runtime.TriggerS : q.AtS) > q.Runtime.Page.QueueTtlS) || !Open(q.Runtime, sample.Sim))
                { Miss(q.Runtime, now, q.Resume ? "resume window closed" : "queue TTL/window expired"); queue.Remove(q); }
            var candidate = queue.OrderByDescending(q => q.Runtime.Page.Priority).ThenBy(q => q.AtS).ThenBy(q => q.Runtime.Page.Id, StringComparer.Ordinal).FirstOrDefault();
            if (active != null && candidate != null && candidate.Runtime.Page.Priority > active.Page.Priority && Boundary(active, now))
            {
                Hide(active, now); active.Suspended = true;
                Emit(now, "preempt", active, candidate.Runtime.Page.Id);
                queue.Add(new QueueEntry { Runtime = active, AtS = now, Resume = true }); active = null;
            }
            if (active == null && candidate != null)
            { queue.Remove(candidate); Activate(candidate.Runtime, candidate.Resume, now, store); }
            if (active != null) Advance(active, sample, store);
            if (active == null)
            {
                var next = pages.Values.Where(p => !p.Retired && p.State != PageState.Presented && !p.Page.CutsOnly && p.Page.FromSim > sample.Sim).OrderBy(p => p.Page.FromSim).FirstOrDefault();
                UpNext = next == null ? "" : "up next: " + PageTitle(next.Page.Id) + " at " + ((int)next.Page.FromSim % 24).ToString("00", System.Globalization.CultureInfo.InvariantCulture) + ":" + ((int)Math.Round((next.Page.FromSim % 1) * 60)).ToString("00", System.Globalization.CultureInfo.InvariantCulture);
                if (log.Count == 0 || log.Last().Kind != "ambient" || log.Last().Value != UpNext) Emit(now, "ambient", null, UpNext);
                var pose = new[] { "pose:home", "pose:north-quay:promenade", "pose:east-quay:mouth-wide" }[(int)(now / 12) % 3];
                if (ambientPose != pose) { ambientPose = pose; Emit(now, "ambient-pose", null, pose); }
            }
            return log.Skip(start).ToArray();
        }
        void EndStaleCaption(double now)
        {
            // Even a reconnect must not put a sub-three-second caption into the recording.
            // Keep this pending across a short outage, then offer the slot again so recovery
            // rechecks proof instead of retaining the pre-outage claim indefinitely.
            if (!staleCaptionPending || active == null || now - active.CaptionS < 3) return;
            Hide(active, now); active.Caption = null; active.Text = null; active.Provisional = false;
            active.Pending.Clear(); Offer(active, now); staleCaptionPending = false;
        }
        static bool Boundary(PageRun p, double now) => p.Caption == null || now - p.CaptionS >= p.CaptionHoldS;
        void Activate(PageRun p, bool resume, double now, ObservationStore store)
        {
            active = p;
            if (!resume)
            {
                p.StartedS = now; p.ShotS = now; p.Shot = 0; p.Slot = 0; p.Caption = null; p.Shown.Clear();
                if (p.Recap || p.Reaction)
                {
                    // A recap uses snapshot proof, rather than re-enacting a planned establishing shot.
                    for (var i = 0; i < p.Page.Shots.Count; i++)
                        if (p.Page.Captions.Any(c => p.Page.Shots[i].Captions.Contains(c.Id) && c.Kind == "claim" && Evaluate(c.Needs, p, store, now, c.Source).Holds)) p.Shot = i;
                }
                Offer(p, now);
                Transition(p, PageState.Staged, now);
                Transition(p, p.Triggered || p.Page.Trigger == null ? PageState.Playing : PageState.Watching, now);
                Emit(now, "shot", p, p.Page.Shots[p.Shot].Id);
                if (p.Page.Shots[p.Shot].FollowDeviceFromTrigger != "none" && p.TriggerRecord != null) Emit(now, "follow", p, p.TriggerRecord.Device);
                if (p.Recap) Emit(now, "recap", p, "already happened");
                if (p.Reaction) Emit(now, "reaction", p, "5 s");
            }
            else
            {
                p.Suspended = false; p.ShotS = now; p.CaptionS = now;
                Emit(now, "resume", p, p.Page.Shots[p.Shot].Id);
                var caption = p.Page.Captions.FirstOrDefault(c => c.Id == p.Caption);
                if (caption != null) {
                    var proof = caption.Needs == null ? new EvidenceResult { Holds = true } : Evaluate(caption.Needs, p, store, now, caption.Source);
                    if (proof.Holds) { p.Text = caption.Render(proof); Emit(now, "caption", p, p.Text, proof.Records); }
                    else { p.Caption = null; p.Text = null; p.Slot = Math.Max(0, p.Slot - 1); }
                }
            }
        }
        void Hide(PageRun p, double now)
        { if (p.Caption != null) Emit(now, "caption-end", p, p.Caption); }
        void Miss(PageRun p, double now, string reason)
        { Emit(now, "missed", p, "missed (live): " + reason); p.Retired = p.Page.Once; Transition(p, PageState.Presented, now); }
        void Finish(PageRun p, double now)
        {
            Hide(p, now); p.Caption = null; p.Text = null; Transition(p, PageState.Presented, now);
            if (p.Page.Once) p.Retired = true;
            active = null;
        }
        void Offer(PageRun p, double now) {
            var shot = p.Page.Shots[p.Shot];
            foreach (var slot in shot.Captions.Select(id => p.Page.Captions.First(c => c.Id == id).Slot).Distinct())
                p.Pending.Add(new Opportunity { Shot = shot, Slot = slot, FromS = now + shot.MoveS, DeadlineS = now + shot.MoveS + shot.HoldMaxS });
        }
        void Fallback(PageRun p, double now, ObservationStore store)
        {
            if (!Boundary(p, now)) return;
            // Only an unresolved slot may fall back. A page-wide search can replay an
            // unrelated fallback (for example the restore tally while waiting for the truck).
            foreach (var opportunity in p.Pending.ToArray())
            {
                p.Pending.Remove(opportunity);
                foreach (var caption in p.Page.Captions.Where(c => c.Kind == "fallback" &&
                    opportunity.Shot.Captions.Contains(c.Id) && c.Slot == opportunity.Slot && c.Id != opportunity.Shown))
                {
                    if (caption.UnlessShown != null && p.Shown.Contains(caption.UnlessShown)) continue;
                    var proof = caption.Needs == null ? new EvidenceResult { Holds = true } : Evaluate(caption.Needs, p, store, now, caption.Source);
                    if (!proof.Holds) continue;
                    Hide(p, now); p.Caption = caption.Id; p.Text = caption.Render(proof); p.CaptionS = now; p.CaptionHoldS = caption.HoldS; p.Shown.Add(caption.Id);
                    Emit(now, "caption", p, p.Text, proof.Records); return;
                }
            }
            // No declared fallback means true numbers on the rail, with no claim caption.
            Finish(p, now);
        }
        void Advance(PageRun p, DirectorSample sample, ObservationStore store)
        {
            var now = sample.MonotonicS; var shot = p.Page.Shots[p.Shot];
            if ((p.Reaction || p.Recap) && now - p.StartedS >= 5) { Finish(p, now); return; }
            if ((!Open(p, sample.Sim) || (p.State == PageState.Watching && now - p.StartedS >= p.Page.TriggerTimeoutS)) && !Boundary(p, now)) return;
            if (!Open(p, sample.Sim) || (p.State == PageState.Watching && now - p.StartedS >= p.Page.TriggerTimeoutS))
            {
                if (p.State != PageState.Fallback) Transition(p, PageState.Fallback, now);
            }
            if (p.State == PageState.Fallback) { Fallback(p, now, store); return; }
            foreach (var node in p.Page.Nodes)
            {
                if (p.Nodes.Contains(node.Id) || now < p.StartedS + (node.AfterPageS ?? (p.Page.Nodes.ToList().IndexOf(node) + 1) * 3) || (node.AfterTriggerS > 0 && (!p.Triggered || now < p.TriggerS + node.AfterTriggerS))) continue;
                var result = Evaluate(node.Needs, p, store, now, node.Source);
                if (!result.Holds) continue;
                // A rail gains at most one node per three seconds.
                var previous = log.LastOrDefault(e => e.Kind == "node" && e.Page == p.Page.Id);
                if (previous != null && now - previous.ShowS < 3) break;
                p.Nodes.Add(node.Id); Emit(now, "node", p, node.Id, result.Records);
                var label = node.Label; foreach (var binding in result.Bindings) label = label.Replace("{" + binding.Key + "}", binding.Value);
                if (label.Contains("{")) throw new FormatException("unbound node label");
                Emit(now, "node-label", p, label, result.Records);
                foreach (var o in result.Records) consumed.Add(o.EventKey);
                foreach (var link in p.Page.Links.Where(l => l[1] == node.Id && p.Nodes.Contains(l[0])))
                {
                    var before = log.Last(e => e.Kind == "node" && e.Page == p.Page.Id && e.Value == link[0]);
                    var earlier = store.LatestEvidenceTime(before.Evidence);
                    if (before.ShowS < now && earlier.HasValue && result.Records.Count > 0 && earlier.Value <= result.Records.Max(o => o.ObservedAtS))
                        Emit(now, "link", p, link[0] + "->" + link[1]);
                }
                break;
            }
            if (!Boundary(p, now)) return;
            foreach (var opportunity in p.Pending.ToArray())
            {
                if (now < opportunity.FromS) continue;
                if (now > opportunity.DeadlineS) { p.Pending.Remove(opportunity); continue; }
                var captions = p.Page.Captions.Where(c => opportunity.Shot.Captions.Contains(c.Id) && c.Slot == opportunity.Slot).ToArray();
                StoryCaption chosen = null; EvidenceResult evidence = new EvidenceResult();
                foreach (var c in captions.OrderBy(c => c.Kind == "claim" ? 0 : c.Kind == "scenario" ? 1 : c.Kind == "pre" ? 2 : 3)) {
                    // a weaker statement is not offered after the stronger one it stands in for
                    if (c.UnlessShown != null && p.Shown.Contains(c.UnlessShown)) continue;
                    var result = c.Needs == null ? new EvidenceResult { Holds = true } : Evaluate(c.Needs, p, store, now, c.Source);
                    if (result.Holds && (p.State != PageState.Fallback || c.Kind == "fallback")) { chosen = c; evidence = result; break; }
                }
                if (chosen == null || chosen.Id == opportunity.Shown) continue;
                var provisional = chosen.Kind != "claim" && captions.Any(c => c.Kind == "claim") && p.State != PageState.Fallback;
                opportunity.Shown = chosen.Id;
                if (!provisional) p.Pending.Remove(opportunity);
                Hide(p, now); p.Caption = chosen.Id; p.Text = chosen.Render(evidence); p.CaptionS = now; p.CaptionHoldS = chosen.HoldS; p.Shown.Add(chosen.Id);
                Emit(now, "caption", p, p.Text, evidence.Records);
                foreach (var o in evidence.Records) consumed.Add(o.EventKey);
                return;
            }
            var elapsed = now - p.ShotS;
            var complete = elapsed >= shot.MoveS + shot.HoldMinS && (shot.Until == null || Evaluate(shot.Until, p, store, now).Holds);
            if (elapsed >= shot.MoveS + shot.HoldMaxS || p.State == PageState.Fallback) complete = true;
            if (!complete || (!Open(p, sample.Sim) && !Boundary(p, now))) return;
            if (p.Shot + 1 >= p.Page.Shots.Count && p.State == PageState.Watching) return;
            // The last shot stays while ANY offer is open, an earlier shot's included: page 3b's derate caption, the page's headline claim, is
            // offered for 80 s from its first shot, and a command read late must still be captioned. The price is paid on a day with no link,
            // when the last shot stands on "Alarm raised." until that first offer runs out (U9Tests pins both).
            if (p.Shot + 1 >= p.Page.Shots.Count && p.Pending.Count > 0 && Open(p, sample.Sim) && p.State != PageState.Fallback) return;
            if (p.Shot + 1 >= p.Page.Shots.Count || p.State == PageState.Fallback) { Finish(p, now); return; }
            if (sample.Sim < p.Page.Shots[p.Shot + 1].CueSim) return;
            Hide(p, now); p.Caption = null; p.Text = null; p.Shot++; p.Slot = 0; p.Provisional = false; p.ShotS = now;
            Offer(p, now);
            var next = p.Page.Shots[p.Shot];
            Emit(now, "shot", p, next.Id);
            if (next.FollowDeviceFromTrigger != "none" && p.TriggerRecord != null)
                Emit(now, "follow", p, p.TriggerRecord.Device);
        }
        static string PageTitle(string id)
        {
            switch (id)
            {
                case "1": return "Morning"; case "1b": return "Terminal demand"; case "2": return "Planned work";
                case "3": return "Gauge replacement"; case "3b": return "Hot connector"; case "3c": return "North Quay"; case "4": return "Storm";
                case "5": return "Flood plain"; case "4b": return "Pump flow"; case "6": return "East Quay";
                case "7": return "Power restored"; case "8": return "Overnight update"; default: return "Harbour";
            }
        }
    }

    /// <summary>Navigation ownership changes on the input frame; return is a continuous smooth blend.</summary>
    public sealed class FreeRoam
    {
        double lastNavigationS = double.NegativeInfinity;
        public void Input(double nowS, string key = null, bool mouseDrag = false, bool wheel = false)
        {
            if (new[] { "E", "D", "H", "Space" }.Contains(key)) key = null;
            if (mouseDrag || wheel || new[] { "W", "A", "S", "Q", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight" }.Contains(key)) lastNavigationS = nowS;
        }
        public double DirectorWeight(double nowS)
        {
            var t = Math.Max(0, Math.Min(1, (nowS - lastNavigationS - 20) / 3));
            return t * t * (3 - 2 * t);
        }
    }
}
