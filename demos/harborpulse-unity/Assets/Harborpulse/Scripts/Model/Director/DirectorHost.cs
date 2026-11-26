// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Globalization;
using System.Text.RegularExpressions;
using DeviceChain.Demos.Hud;

namespace DeviceChain.Harborpulse.Model.Director
{
    public sealed class DirectorView
    {
        public string Page { get; internal set; }
        public string Shot { get; internal set; }
        public CameraPose From { get; internal set; }
        public CameraPose To { get; internal set; }
        public double ShotAtS { get; internal set; }
        public double MoveS { get; internal set; }
        public string Ease => "smooth";
        public string FollowDevice { get; internal set; }
        public Ground? FollowAnchor { get; internal set; }
        public string AmbientPose { get; internal set; }
        public string Caption { get; internal set; }
        public string CaptionKind { get; internal set; }
        public double CaptionAtS { get; internal set; }
        public IReadOnlyList<RailNode> Rail { get; internal set; }
        public IReadOnlyCollection<string> Links { get; internal set; }
        public string Banner { get; internal set; }
        public string Problem { get; internal set; }
        public string UpNext { get; internal set; }
        public DirectorMode Mode { get; internal set; }
        public double DirectorWeight { get; internal set; }
        /// <summary>A cut's disclosure overlays (the opening and end cards' lines), shown beside the caption; empty live.</summary>
        public string Overlay { get; internal set; } = "";
        /// <summary>The active page's context line (where the viewer is, what they are looking at); empty between pages.</summary>
        public string Context { get; internal set; } = "";
        /// <summary>The small capitals over the caption: the context line, or for a chapter's first seconds its tag.</summary>
        public string Kicker { get; internal set; } = "";
        /// <summary>Small print under the caption, on one line (a cut's disclosure).</summary>
        public string Note { get; internal set; } = "";
        /// <summary>A cut's end card, or null.</summary>
        public EndCardContent EndCard { get; internal set; }
        /// <summary>When the chain card starts to fade (the view's own clock), or null while it stays.</summary>
        public double? CardExitAtS { get; internal set; }
        public CardDock CardDock { get; internal set; } = CardDock.BottomRight;
        /// <summary>The devices the shot is about: the only names written in the world.</summary>
        public IReadOnlyList<string> Tags { get; internal set; } = Array.Empty<string>();
        /// <summary>The top strip's chips: a shot that does not run at the recorded rate, and one shown out of order.</summary>
        public string RateChip { get; internal set; } = "";
        public string Chip { get; internal set; } = "";
        /// <summary>The caption is a claim the platform's record supports (a blue rule), not the scenario speaking (a grey one).</summary>
        public CaptionTone Tone => CaptionKind == "claim" ? CaptionTone.Platform : CaptionTone.Scenario;
        public IReadOnlyList<ChainRow> Card => (Rail ?? Array.Empty<RailNode>()).Select(n => n.Row).ToArray();
        /// <summary>
        /// The view of one frame of a rendered cut: the same surface the live director fills, from the cut player's frame. The
        /// chip of a shot borrowed out of order takes the idle "up next" place, which a cut never uses.
        /// </summary>
        public static DirectorView ForCut(CutFrame frame) => new DirectorView
        {
            Page = frame.Page.Id, Shot = frame.Shot.Id, From = frame.From, To = frame.To, MoveS = frame.Authored.MoveS, ShotAtS = frame.ShowS - frame.IntoS,
            Caption = frame.Caption, CaptionKind = frame.CaptionKind, CaptionAtS = frame.CaptionAtS, Rail = frame.Rail, Links = frame.Links,
            Banner = "", Problem = "", UpNext = "", Mode = DirectorMode.Active, DirectorWeight = 1, Overlay = frame.Overlay, Context = frame.Page.ContextLine,
            Kicker = frame.Kicker, Note = frame.Note, EndCard = frame.EndCard, CardExitAtS = frame.CardExitAtS, Tags = frame.Tags, RateChip = frame.RateChip, Chip = frame.BorrowedChip,
            CardDock = frame.Authored.CardDock == "tr" ? CardDock.TopRight : frame.Authored.CardDock == "bl" ? CardDock.BottomLeft : CardDock.BottomRight
        };
    }
    /// <summary>One instance per runner run. Only IDirectorFeed may add observations.</summary>
    public sealed class DirectorHost
    {
        readonly Storyboard story;
        PresentationMap map;
        public void SetMap(PresentationMap value)
        {
            map = value;
            if (view.Shot != null && view.Page != null) {
                var shot = story.Page(view.Page).Shots.First(s => s.Id == view.Shot);
                view.From = map.Pose(shot.From); view.To = map.Pose(shot.To);
            }
            else view.From = view.To = map.Pose(view.AmbientPose);
            if (view.FollowDevice != null) view.FollowAnchor = map.Anchors[view.FollowDevice];
        }
        readonly IDirectorFeed feed;
        readonly HudModel hud = new HudModel();
        string railPage;
        int consumedCount = -1;
        string feedFailure;
        string visibleCaption = "", visiblePage;
        double visibleAtS;
        readonly DirectorView view = new DirectorView { AmbientPose = "pose:home" };
        public ObservationStore Store { get; } = new ObservationStore();
        public Scheduler Scheduler { get; }
        public Recorder Recorder { get; }
        public FreeRoam FreeRoam { get; } = new FreeRoam();
        public bool Enabled { get; set; } = true;
        public DirectorHost(Storyboard story, HarborLayout layout, PresentationMap map, IDirectorFeed feed = null, Recorder recorder = null)
        {
            this.story = story; this.map = map; this.feed = feed;
            Scheduler = new Scheduler(story, layout);
            Recorder = recorder ?? new Recorder(TextWriter.Null, TextWriter.Null, TextWriter.Null, TextWriter.Null);
            view.From = view.To = map.Pose("pose:home");
        }
        public DirectorView Tick(DirectorSample sample, DirectorInputs inputs)
        {
            try
            {
                feed?.Update(inputs, o => { if (Store.Add(o)) Recorder.Observe(sample.MonotonicS, o); }, c => { Store.AddCoverage(c); Recorder.Coverage(sample.MonotonicS, c); });
                feedFailure = null;
            }
            catch (Exception e) { feedFailure = "observer feed: " + e.Message; }
            var events = Scheduler.Tick(sample, Store);
            if (events.Count > 0) Recorder.Decision(new DirectorEvent(sample.MonotonicS, "sim-hour", "", sample.Sim.ToString("R", CultureInfo.InvariantCulture)));
            foreach (var e in events) { Recorder.Decision(e); Apply(e); }
            if (Scheduler.Consumed.Count != consumedCount)
            { Recorder.Consumption(sample.MonotonicS, Scheduler.Consumed); consumedCount = Scheduler.Consumed.Count; }
            Recorder.Advance(sample.MonotonicS);
            if (Scheduler.ActivePage == null && view.Shot != null)
            {
                view.From = view.To; view.To = map.Pose(view.AmbientPose); view.MoveS = 3; view.ShotAtS = sample.MonotonicS;
                view.Shot = null; view.FollowDevice = null; view.FollowAnchor = null;
            }
            if (Scheduler.ActivePage == null && hud.CaptionKind == "pre" && sample.MonotonicS - hud.CaptionAtS >= 3)
                hud.SetCaption("", "", "", sample.MonotonicS);
            hud.Tick(sample.MonotonicS);
            // Record the rendered surface after its minimum-hold queue, including the opening title.
            if (hud.Caption != visibleCaption || hud.CaptionPage != visiblePage || hud.CaptionAtS != visibleAtS) {
                if (visibleCaption.Length > 0) Recorder.Decision(new DirectorEvent(sample.MonotonicS, "visible-caption-end", visiblePage, visibleCaption));
                if (hud.Caption.Length > 0) Recorder.Decision(new DirectorEvent(sample.MonotonicS, "visible-caption", hud.CaptionPage, hud.Caption));
                visibleCaption = hud.Caption; visiblePage = hud.CaptionPage; visibleAtS = hud.CaptionAtS;
            }
            view.Page = Scheduler.ActivePage;
            view.Context = view.Page == null ? "" : story.Page(view.Page).ContextLine; view.Kicker = view.Context;
            view.Tags = view.Page == null || view.Shot == null ? Array.Empty<string>() : story.Page(view.Page).Shots.First(s => s.Id == view.Shot).Tags;
            view.Caption = hud.Caption; view.CaptionKind = hud.CaptionKind; view.CaptionAtS = hud.CaptionAtS;
            view.Rail = hud.Rail; view.Links = hud.Links;
            view.Banner = HudModel.Banner(sample.DigestMatches, sample.Stale, sample.Defects);
            view.Problem = feed == null ? "no observer feed" : string.Join(" | ", (feed.Problems ?? Array.Empty<string>()).Concat(new[] { feedFailure }).Where(p => !string.IsNullOrEmpty(p)));
            view.UpNext = Scheduler.ActivePage == null ? Scheduler.UpNext : ""; // the chip belongs to idle time
            view.Mode = Scheduler.Mode;
            view.DirectorWeight = Enabled && sample.DigestMatches ? FreeRoam.DirectorWeight(sample.MonotonicS) : 0;
            return view;
        }
        void Apply(DirectorEvent e)
        {
            var p = story.Pages.FirstOrDefault(x => x.Id == e.Page);
            switch (e.Kind)
            {
                case "shot": case "resume":
                    var shot = p.Shots.First(s => s.Id == e.Value);
                    view.Shot = shot.Id; view.From = map.Pose(shot.From); view.To = map.Pose(shot.To); view.MoveS = shot.MoveS; view.ShotAtS = e.ShowS;
                    view.FollowDevice = null; view.FollowAnchor = null;
                    if (railPage != e.Page) { hud.ClearRail(); railPage = e.Page; }
                    break;
                case "follow":
                    view.FollowDevice = e.Value;
                    view.FollowAnchor = map.Anchors.TryGetValue(e.Value, out var anchor) ? anchor : (Ground?)null;
                    break;
                case "ambient-pose":
                    view.AmbientPose = e.Value; view.From = view.To; view.To = map.Pose(e.Value); view.MoveS = 3; view.ShotAtS = e.ShowS;
                    view.Shot = null; view.FollowDevice = null; view.FollowAnchor = null; break;
                case "title": hud.SetCaption(e.Value, "pre", e.Page, e.ShowS); break;
                case "caption":
                    var caption = p.Captions.FirstOrDefault(c => Regex.IsMatch(e.Value, "^" + Regex.Replace(Regex.Escape(string.Join("\n", c.Lines)), @"\\\{[^}]+}", "[^\n]+") + "$"));
                    hud.SetCaption(e.Value, caption?.Kind ?? "fallback", e.Page, e.ShowS); break;
                case "caption-end": hud.SetCaption("", "", e.Page, e.ShowS); break;
                case "state": if (e.Value == "Presented") hud.SetCaption("", "", e.Page, e.ShowS); break;
                case "node":
                    var node = p.Nodes.First(n => n.Id == e.Value); hud.AddNode(node.Id, node.Label, node.Source, e.ShowS, e.InstantS); break;
                case "node-label": hud.LabelLast(e.Value); break;
                case "link": hud.AddLink(e.Value); break;
            }
        }
    }
}
