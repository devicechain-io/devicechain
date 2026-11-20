// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Globalization;
using System.Text.RegularExpressions;

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
    }
    /// <summary>One instance per runner run. Only IDirectorFeed may add observations.</summary>
    public sealed class DirectorHost
    {
        readonly Storyboard story;
        readonly PresentationMap map;
        readonly IDirectorFeed feed;
        readonly HudModel hud = new HudModel();
        string railPage;
        int consumedCount = -1;
        string feedFailure;
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
            view.Page = Scheduler.ActivePage;
            view.Caption = hud.Caption; view.CaptionKind = hud.CaptionKind; view.CaptionAtS = hud.CaptionAtS;
            view.Rail = hud.Rail; view.Links = hud.Links;
            view.Banner = HudModel.Banner(sample.DigestMatches, sample.Stale, sample.Defects);
            view.Problem = feed == null ? "no observer feed" : string.Join(" | ", (feed.Problems ?? Array.Empty<string>()).Concat(new[] { feedFailure }).Where(p => !string.IsNullOrEmpty(p)));
            view.UpNext = Scheduler.ActivePage == null ? Scheduler.UpNext : ""; // the chip belongs to idle time, not to a playing page view.Mode = Scheduler.Mode;
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
                    var node = p.Nodes.First(n => n.Id == e.Value); hud.AddNode(node.Id, node.Label, node.Source, e.ShowS); break;
                case "node-label": hud.LabelLast(e.Value); break;
                case "link": hud.AddLink(e.Value); break;
            }
        }
    }
}
