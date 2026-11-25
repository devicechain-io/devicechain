// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Text.Json;
using System.Text.RegularExpressions;

namespace DeviceChain.Harborpulse.Model.Director
{
    public sealed class StoryCaption
    {
        public string Id { get; internal set; }
        public string Kind { get; internal set; }
        public string Source { get; internal set; }
        public string[] Lines { get; internal set; }
        public Needs Needs { get; internal set; }
        public string Slot { get; internal set; }
        public double HoldS { get; internal set; }
        /// <summary>
        /// Another caption of the page: once that one has been shown in a presentation of the page, this one is no longer offered. A
        /// weaker statement of the same fact ("alarm raised") must not follow the stronger one it stands in for ("the platform tells it
        /// to derate"), whichever shot's slot would offer it.
        /// </summary>
        public string UnlessShown { get; internal set; }
        public string Render(EvidenceResult evidence)
        {
            var text = string.Join("\n", Lines);
            foreach (var b in evidence.Bindings) text = text.Replace("{" + b.Key + "}", b.Value);
            if (Regex.IsMatch(text, @"\{[^}]+\}")) throw new FormatException("unbound caption " + Id);
            if (text.Split('\n').Any(line => line.Length > 42)) throw new FormatException("rendered caption exceeds 42 characters: " + Id);
            return text;
        }
    }
    public sealed class StoryShot
    {
        public string Id { get; internal set; }
        public string From { get; internal set; }
        public string To { get; internal set; }
        public string FollowDeviceFromTrigger { get; internal set; }
        public double MoveS { get; internal set; }
        public double HoldMinS { get; internal set; }
        public double HoldMaxS { get; internal set; }
        public double CueSim { get; internal set; }
        public Needs Until { get; internal set; }
        public IReadOnlyList<string> Captions { get; internal set; }
    }
    public sealed class ChainNode
    {
        public string Id { get; internal set; }
        public double AfterTriggerS { get; internal set; }
        public double? AfterPageS { get; internal set; }
        public string Family { get; internal set; }
        public string Label { get; internal set; }
        public string Source { get; internal set; }
        public Needs Needs { get; internal set; }
    }
    public sealed class StoryPage
    {
        public string Id { get; internal set; }
        public int Priority { get; internal set; }
        public double FromSim { get; internal set; }
        public double ToSim { get; internal set; }
        public double? PlannedSim { get; internal set; }
        public double PreRollS { get; internal set; }
        public double TriggerTimeoutS { get; internal set; }
        public double QueueTtlS { get; internal set; }
        public string FallbackPolicy { get; internal set; } = "show-fallback";
        public bool Once { get; internal set; }
        public bool CutsOnly { get; internal set; }
        /// <summary>
        /// The page's context line: where the viewer is and what they are looking at ("North Quay · the storm drain pump and its
        /// well"). It is orientation, never evidence: like a pre caption it may carry no platform verb and no digit, so it cannot
        /// say what the platform did. Empty for a page that needs none.
        /// </summary>
        public string ContextLine { get; internal set; } = "";
        public Needs Watch { get; internal set; }
        public Needs Trigger { get; internal set; }
        public Needs Terminal { get; internal set; }
        public IReadOnlyList<StoryShot> Shots { get; internal set; }
        public IReadOnlyList<StoryCaption> Captions { get; internal set; }
        public IReadOnlyList<ChainNode> Nodes { get; internal set; }
        public IReadOnlyList<string[]> Links { get; internal set; }
        public EvidenceContext Context(string run, double nowS, string source = EvidenceSource.Platform, Observation trigger = null) => new EvidenceContext(run, FromSim, ToSim, nowS, source, trigger);
    }
    public sealed class CutCaption
    {
        public string Id { get; internal set; }
        public double AtS { get; internal set; }
        public double HoldS { get; internal set; }
    }
    public sealed class CutSegment
    {
        public string Page { get; internal set; }
        public string Shot { get; internal set; }
        public double DurS { get; internal set; }
        public double MoveS { get; internal set; }
        public double RecordedWallFromS { get; internal set; }
        public double RecordedWallToS { get; internal set; }
        public string SeamPose { get; internal set; }
        public double DissolveS { get; internal set; }
        public IReadOnlyList<CutCaption> Overlays { get; internal set; }
        public string Borrowed { get; internal set; }
        public IReadOnlyList<CutCaption> Captions { get; internal set; }
        public IReadOnlyList<string> Nodes { get; internal set; }
        public double Rate => (RecordedWallToS - RecordedWallFromS) / DurS;
        public double RecordedAt(double showIntoS) => RecordedWallFromS + Math.Max(0, Math.Min(1, showIntoS / DurS)) * (RecordedWallToS - RecordedWallFromS);
    }
    public sealed class StoryCut
    {
        public string Id { get; internal set; }
        public double TargetS { get; internal set; }
        public IReadOnlyList<CutSegment> Segments { get; internal set; }
    }

    public sealed class Storyboard
    {
        public IReadOnlyList<StoryPage> Pages { get; private set; }
        public IReadOnlyList<StoryCut> Cuts { get; private set; }
        public StoryPage Page(string id) => Pages.First(p => p.Id == id);
        static readonly Regex PlatformVerb = new Regex(@"\b(detect(?:s|ed|ing)?|start(?:s|ed|ing)?|notif(?:y|ies|ied|ying)|record(?:s|ed|ing)?|stor(?:e|es|ed|ing)|disabl(?:e|es|ed|ing)|refus(?:e|es|ed|ing)|pac(?:e|es|ed|ing)|updat(?:e|es|ed|ing))\b", RegexOptions.IgnoreCase | RegexOptions.CultureInvariant);
        public static Storyboard Parse(string json, PresentationMap map)
        {
            try
            {
                using (var doc = JsonDocument.Parse(json))
                {
                    var root = doc.RootElement; StrictJson.Keys(root, "version", "pages", "cuts");
                    if (StrictJson.Num(root, "version") != 1) throw new FormatException("unknown storyboard version");
                    var pages = StrictJson.Array(root, "pages").Select(e => ReadPage(e, map)).ToArray();
                    Unique(pages.Select(p => p.Id), "page");
                    if (pages.Length == 0) throw new FormatException("no pages");
                    var cuts = StrictJson.Array(root, "cuts").Select(e => ReadCut(e, pages)).ToArray();
                    Unique(cuts.Select(c => c.Id), "cut");
                    if (!cuts.Select(c => c.Id).OrderBy(x => x, StringComparer.Ordinal).SequenceEqual(new[] { "c300", "c90" })) throw new FormatException("cuts require c90 and c300");
                    return new Storyboard { Pages = pages, Cuts = cuts };
                }
            }
            catch (JsonException e) { throw new FormatException("storyboard JSON: " + e.Message, e); }
            catch (InvalidOperationException e) { throw new FormatException("storyboard value: " + e.Message, e); }
            catch (KeyNotFoundException e) { throw new FormatException("storyboard missing field: " + e.Message, e); }
        }
        static void Unique(IEnumerable<string> values, string kind)
        { var seen = new HashSet<string>(StringComparer.Ordinal); foreach (var v in values) if (!seen.Add(v)) throw new FormatException("duplicate " + kind + " " + v); }
        static bool Bool(JsonElement e, string key)
        { var v = e.GetProperty(key); if (v.ValueKind != JsonValueKind.True && v.ValueKind != JsonValueKind.False) throw new FormatException("expected boolean " + key); return v.GetBoolean(); }
        static Needs Predicate(JsonElement e, string key) => e.TryGetProperty(key, out var v) && v.ValueKind != JsonValueKind.Null ? Needs.Parse(v) : null;
        static StoryPage ReadPage(JsonElement e, PresentationMap map)
        {
            StrictJson.Keys(e, "fallbackPolicy", "id", "priority", "windowSim", "plannedSim", "preRollS", "triggerTimeoutS", "queueTtlS", "once", "cutsOnly", "watch", "trigger", "terminal", "shots", "captions", "chain", "context");
            var window = StrictJson.Array(e, "windowSim").ToArray();
            if (window.Length != 2 || !window[0].TryGetDouble(out var from) || !window[1].TryGetDouble(out var to) || !double.IsFinite(from) || !double.IsFinite(to) || to <= from) throw new FormatException("windowSim requires increasing finite pair");
            var priority = StrictJson.Num(e, "priority");
            if (priority < 0 || priority % 1 != 0) throw new FormatException("priority must be nonnegative integer");
            var policy = StrictJson.Str(e, "fallbackPolicy", "show-fallback");
            if (policy != "show-fallback" && policy != "skip-page") throw new FormatException("unknown fallback policy");
            var page = new StoryPage { FallbackPolicy = policy, Id = StrictJson.Str(e, "id"), Priority = (int)priority, FromSim = from, ToSim = to,
                PlannedSim = e.GetProperty("plannedSim").ValueKind == JsonValueKind.Null ? (double?)null : StrictJson.Num(e, "plannedSim"),
                PreRollS = StrictJson.Num(e, "preRollS"), TriggerTimeoutS = StrictJson.Num(e, "triggerTimeoutS"), QueueTtlS = StrictJson.Num(e, "queueTtlS"),
                Once = Bool(e, "once"), CutsOnly = Bool(e, "cutsOnly"), Watch = Predicate(e, "watch"), Trigger = Predicate(e, "trigger"), Terminal = Predicate(e, "terminal") };
            page.ContextLine = StrictJson.Str(e, "context", "");
            if (page.ContextLine.Length > 64 || page.ContextLine.Contains("\n") || PlatformVerb.IsMatch(page.ContextLine) || page.ContextLine.Any(char.IsDigit))
                throw new FormatException("context line: at most 64 characters on one line, no platform verb, no digit: " + page.Id);
            if (page.PreRollS < 0 || page.TriggerTimeoutS < 3 || page.QueueTtlS < 0) throw new FormatException("page duration invalid " + page.Id);
            page.Captions = StrictJson.Array(e, "captions").Select(ReadCaption).ToArray(); Unique(page.Captions.Select(c => c.Id), "caption");
            foreach (var c in page.Captions.Where(c => c.UnlessShown != null))
                if (c.UnlessShown == c.Id || !page.Captions.Any(o => o.Id == c.UnlessShown && o.Kind == "claim")) throw new FormatException("unlessShown must name another claim of the page: " + c.Id);
            page.Shots = StrictJson.Array(e, "shots").Select(s => ReadShot(s, page, map)).ToArray(); Unique(page.Shots.Select(s => s.Id), "shot");
            if (page.Shots.Count == 0) throw new FormatException("page has no shots " + page.Id);
            var chain = e.GetProperty("chain"); StrictJson.Keys(chain, "nodes", "links");
            page.Nodes = StrictJson.Array(chain, "nodes").Select(n =>
            {
                StrictJson.Keys(n, "afterTriggerS", "afterPageS", "id", "family", "label", "source", "needs"); var family = StrictJson.Str(n, "family");
                if (!new[] { "gauge", "pump", "other" }.Contains(family)) throw new FormatException("unknown node family " + family);
                var source = StrictJson.Str(n, "source"); EvidenceSource.Check(source);
                return new ChainNode { AfterTriggerS = StrictJson.Num(n, "afterTriggerS", 0), AfterPageS = n.TryGetProperty("afterPageS", out _) ? StrictJson.Num(n, "afterPageS") : (double?)null, Id = StrictJson.Str(n, "id"), Family = family, Label = StrictJson.Str(n, "label"), Source = source, Needs = Needs.Parse(n.GetProperty("needs")) };
            }).ToArray(); Unique(page.Nodes.Select(n => n.Id), "node");
            foreach (var need in new[] { page.Watch, page.Trigger, page.Terminal }.Concat(page.Captions.Select(c => c.Needs)).Concat(page.Nodes.Select(n => n.Needs)).Concat(page.Shots.Select(shot => shot.Until))) need?.ResolveTimes(map.Layout);
            page.Links = StrictJson.Array(chain, "links").Select(l =>
            {
                if (l.ValueKind != JsonValueKind.Array) throw new FormatException("chain link requires pair");
                var ids = l.EnumerateArray().Select(v => v.GetString()).ToArray();
                if (ids.Length != 2 || ids[0] == ids[1] || ids.Any(id => !page.Nodes.Any(n => n.Id == id))) throw new FormatException("unknown chain link node");
                var families = ids.Select(id => page.Nodes.First(n => n.Id == id).Family).ToArray();
                if (families.Contains("gauge") && families.Contains("pump")) throw new FormatException("gauge/pump chain link refused");
                return ids;
            }).ToArray();
            return page;
        }
        static StoryCaption ReadCaption(JsonElement e)
        {
            StrictJson.Keys(e, "id", "kind", "source", "slot", "lines", "needs", "holdS", "unlessShown");
            var c = new StoryCaption { UnlessShown = e.TryGetProperty("unlessShown", out _) ? StrictJson.Str(e, "unlessShown") : null, Id = StrictJson.Str(e, "id"), Kind = StrictJson.Str(e, "kind"), Source = StrictJson.Str(e, "source"), Slot = StrictJson.Str(e, "slot"), HoldS = StrictJson.Num(e, "holdS"), Needs = Predicate(e, "needs") };
            EvidenceSource.Check(c.Source);
            if (!new[] { "pre", "scenario", "fallback", "claim" }.Contains(c.Kind)) throw new FormatException("unknown caption kind " + c.Kind);
            c.Lines = StrictJson.Array(e, "lines").Select(v => v.ValueKind == JsonValueKind.String ? v.GetString() : throw new FormatException("caption line must be string")).ToArray();
            if (c.Lines.Length < 1 || c.Lines.Length > 2 || c.Lines.Any(s => s.Length == 0 || s.Length > 42 || s.Contains("\n") || s.Contains("\r"))) throw new FormatException("caption lines: at most two authored lines of 42 characters " + c.Id);
            if (c.HoldS < 3) throw new FormatException("caption minimum 3 s " + c.Id);
            if (c.Kind == "claim" && c.Needs == null) throw new FormatException("claim requires needs " + c.Id);
            if (c.Kind == "scenario" && c.Source != EvidenceSource.Scenario) throw new FormatException("scenario caption source " + c.Id);
            var text = string.Join(" ", c.Lines);
            // 'a firmware update' names the artifact; update is a noun in this authored fallback.
            var lintText = c.Id == "unsigned" && c.Kind == "fallback" && c.Source == EvidenceSource.Platform && text == "Overnight: a firmware update." ? "Overnight: a firmware artifact." : text;
            if ((c.Kind == "pre" || c.Kind == "fallback") && (PlatformVerb.IsMatch(lintText) || text.Any(char.IsDigit))) throw new FormatException("claim lint: platform verb or digit in " + c.Kind + " " + c.Id);
            if (c.Kind == "pre" || c.Kind == "fallback")
                if (Regex.IsMatch(text, @"\{[^}]+\}")) throw new FormatException("pre/fallback placeholders refused");
            if (c.Kind == "claim")
            {
                if (text.Any(char.IsDigit)) throw new FormatException("claim lint: literal digit " + c.Id);
                foreach (Match match in Regex.Matches(text, @"\{([^}]+)\}"))
                    if (!c.Needs.BoundNames.Contains(match.Groups[1].Value)) throw new FormatException("claim lint: unbound placeholder " + match.Value);
            }
            return c;
        }
        static StoryShot ReadShot(JsonElement e, StoryPage page, PresentationMap map)
        {
            StrictJson.Keys(e, "id", "from", "to", "moveS", "holdMinS", "holdMaxS", "cueRef", "cueSim", "until", "captions", "followDeviceFromTrigger");
            var s = new StoryShot { Id = StrictJson.Str(e, "id"), From = StrictJson.Str(e, "from"), To = StrictJson.Str(e, "to"), MoveS = StrictJson.Num(e, "moveS"), HoldMinS = StrictJson.Num(e, "holdMinS"), HoldMaxS = StrictJson.Num(e, "holdMaxS"), CueSim = StrictJson.Num(e, "cueSim"), Until = Predicate(e, "until"), FollowDeviceFromTrigger = StrictJson.Str(e, "followDeviceFromTrigger", "none"), Captions = StrictJson.Array(e, "captions").Select(v => v.GetString()).ToArray() };
            if (e.TryGetProperty("cueRef", out _)) s.CueSim = Needs.Cue(StrictJson.Str(e, "cueRef"), map.Layout);
            map.Pose(s.From); map.Pose(s.To);
            if (s.FollowDeviceFromTrigger != "none" && s.FollowDeviceFromTrigger != "device") throw new FormatException("unknown followDeviceFromTrigger " + s.FollowDeviceFromTrigger);
            if (s.Captions.Any(id => !page.Captions.Any(c => c.Id == id))) throw new FormatException("shot unknown caption " + s.Id);
            var longest = page.Captions.Where(c => s.Captions.Contains(c.Id)).Select(c => c.HoldS).DefaultIfEmpty(0).Max();
            if (s.MoveS < 0 || s.HoldMinS < longest || s.HoldMaxS < s.HoldMinS || s.HoldMaxS < 3) throw new FormatException("shot timing law " + s.Id);
            return s;
        }
        static StoryCut ReadCut(JsonElement e, IReadOnlyList<StoryPage> pages)
        {
            StrictJson.Keys(e, "id", "targetS", "segments"); var id = StrictJson.Str(e, "id");
            if (id != "c90" && id != "c300") throw new FormatException("unknown cut " + id);
            var target = StrictJson.Num(e, "targetS"); if (target != (id == "c90" ? 90 : 300)) throw new FormatException("cut target " + id);
            var segments = StrictJson.Array(e, "segments").Select(s =>
            {
                StrictJson.Keys(s, "seamPose", "dissolveS", "overlays", "page", "shot", "durS", "moveS", "recordedWallFromS", "recordedWallToS", "borrowed", "captions", "nodes");
                var segment = new CutSegment { Page = StrictJson.Str(s, "page"), Shot = StrictJson.Str(s, "shot"), DurS = StrictJson.Num(s, "durS"), MoveS = StrictJson.Num(s, "moveS"), RecordedWallFromS = StrictJson.Num(s, "recordedWallFromS"), RecordedWallToS = StrictJson.Num(s, "recordedWallToS"), Borrowed = StrictJson.Str(s, "borrowed", "none") };
                segment.SeamPose = StrictJson.Str(s, "seamPose"); segment.DissolveS = StrictJson.Num(s, "dissolveS");
                if (segment.SeamPose != "pose:home" || segment.DissolveS < 0 || segment.DissolveS > segment.DurS) throw new FormatException("invalid seam");
                var page = pages.FirstOrDefault(p => p.Id == segment.Page) ?? throw new FormatException("unknown cut page " + segment.Page);
                if (!page.Shots.Any(shot => shot.Id == segment.Shot)) throw new FormatException("unknown cut shot " + segment.Shot);
                if (id == "c90" && !new[] { "0", "1", "4", "6", "7", "8", "9" }.Contains(page.Id)) throw new FormatException("c90 contains extra chain " + page.Id);
                segment.Nodes = StrictJson.Array(s, "nodes").Select(v => v.GetString()).ToArray();
                if (segment.Nodes.Any(n => !page.Nodes.Any(p => p.Id == n))) throw new FormatException("unknown cut node");
                if (id == "c90" && page.Id == "4" && page.Nodes.Any(n => n.Family == "gauge" && segment.Nodes.Contains(n.Id))) throw new FormatException("c90 contains extra gauge chain");
                segment.Captions = StrictJson.Array(s, "captions").Select(c =>
                {
                    StrictJson.Keys(c, "id", "atS", "holdS"); var caption = new CutCaption { Id = StrictJson.Str(c, "id"), AtS = StrictJson.Num(c, "atS"), HoldS = StrictJson.Num(c, "holdS") };
                    if (!page.Captions.Any(pc => pc.Id == caption.Id)) throw new FormatException("unknown cut caption " + caption.Id);
                    if (caption.HoldS < 3 || caption.AtS < segment.MoveS || caption.AtS + caption.HoldS > segment.DurS + 1e-9) throw new FormatException("cut caption timing law " + caption.Id);
                    return caption;
                }).ToArray();
                segment.Overlays = StrictJson.Array(s, "overlays").Select(o => {
                    StrictJson.Keys(o, "id", "atS", "holdS"); var overlay = new CutCaption { Id = StrictJson.Str(o, "id"), AtS = StrictJson.Num(o, "atS"), HoldS = StrictJson.Num(o, "holdS") };
                    if (!page.Captions.Any(c => c.Id == overlay.Id && (c.Kind == "scenario" || c.Kind == "claim")) || overlay.HoldS < 3 || overlay.AtS < 0 || overlay.AtS + overlay.HoldS > segment.DurS) throw new FormatException("invalid disclosure overlay");
                    return overlay;
                }).ToArray();
                var ordered = segment.Captions.OrderBy(c => c.AtS).ToArray();
                for (var i = 1; i < ordered.Length; i++)
                    if (ordered[i].AtS < ordered[i - 1].AtS + ordered[i - 1].HoldS && page.Captions.First(c => c.Id == ordered[i].Id).Slot != page.Captions.First(c => c.Id == ordered[i - 1].Id).Slot) throw new FormatException("cut overlapping caption slots");
                var longest = segment.Captions.Select(c => c.HoldS).DefaultIfEmpty(0).Max();
                if (segment.DurS <= 0 || segment.MoveS < 0 || segment.DurS < segment.MoveS + longest || segment.RecordedWallToS < segment.RecordedWallFromS) throw new FormatException("cut shot timing law " + segment.Shot);
                return segment;
            }).ToArray();
            if (Math.Abs(segments.Sum(s => s.DurS) - target) > 0.5) throw new FormatException("cut timing law total " + id);
            return new StoryCut { Id = id, TargetS = target, Segments = segments };
        }
    }
}
