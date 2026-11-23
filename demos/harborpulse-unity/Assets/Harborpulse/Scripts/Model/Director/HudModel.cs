// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;

namespace DeviceChain.Harborpulse.Model.Director
{
    public sealed class RailNode
    {
        public string Id { get; internal set; }
        public string Label { get; internal set; }
        public string Source { get; internal set; }
        public double AtS { get; internal set; }
        public double SincePreviousS { get; internal set; }
        public string Tag => Source == EvidenceSource.Scenario ? "scenario" : Source == EvidenceSource.DeviceSide ? "sender's count" : "platform";
        public string Shape => Source == EvidenceSource.Scenario ? "◇" : Source == EvidenceSource.DeviceSide ? "▧" : "■";
    }
    public sealed class WorldTag
    {
        public string Token { get; set; }
        public string Text { get; set; }
    }
    /// <summary>All story text and visibility rules. Rendering has no evidence decisions.</summary>
    public sealed class HudModel
    {
        /// <summary>
        /// Where the HUD writes, as shares of the frame's height from its bottom: the caption's box in a 16:9 frame and in a 9:16 one (which
        /// writes it higher, clear of a phone's own controls), and the lower edge of the rail's lowest node in a 9:16 frame, where the nodes
        /// run across the whole width (two are shown, <see cref="RailRowTop"/> down by <see cref="RailRowStep"/> each). A camera that has
        /// something to show in a tall frame shows it between <see cref="CaptionTopTall"/> and <see cref="RailBottomTall"/>.
        /// </summary>
        public const float CaptionBottomWide = .08f, CaptionTopWide = .24f, CaptionBottomTall = .23f, CaptionTopTall = .37f;
        public const float RailRowTop = .758f, RailRowHeight = .078f, RailRowStep = .082f;
        public const int RailRowsTall = 2, RailRowsWide = 5;

        /// <summary>
        /// A world tag's box, in the HUD's reference pixels: wide enough for the names the district had ("Charger N1 · alarm"), and wider
        /// for a longer one, which the fixed box wrapped onto a second line it then cut off ("MQTT charger N1 ·").
        /// </summary>
        public const float TagBoxWidth = 250, TagBoxHeight = 40, TagGlyphWidth = 11.8f, TagPadding = 24;
        public static float TagWidth(string text) => Math.Max(TagBoxWidth, TagPadding + TagGlyphWidth * (text ?? "").Length);
        public const float RailBottomTall = RailRowTop - RailRowHeight - RailRowStep * (RailRowsTall - 1);

        readonly List<RailNode> rail = new List<RailNode>();
        readonly HashSet<string> links = new HashSet<string>(StringComparer.Ordinal);
        string pendingText, pendingKind, pendingPage;
        bool pending;
        string page;
        public string Caption { get; private set; } = "";
        public string CaptionKind { get; private set; } = "";
        public string CaptionPage => page;
        public double CaptionAtS { get; private set; } = double.NegativeInfinity;
        public IReadOnlyList<RailNode> Rail => rail.ToArray();
        public IReadOnlyCollection<string> Links => links.ToArray();
        public static string Banner(bool digestMatches, bool stale, IEnumerable<string> defects)
        {
            if (!digestMatches) return "layout mismatch: director off";
            if (stale) return "reconnecting";
            var d = defects.ToArray();
            return d.Length == 0 ? "" : "TEST CONTROL: " + string.Join(", ", d);
        }
        public void SetCaption(string text, string kind, string captionPage, double now)
        {
            if (Caption == (text ?? "") && page == captionPage) return;
            if (Caption.Length > 0 && now - CaptionAtS < 3)
            { pendingText = text; pendingKind = kind; pendingPage = captionPage; pending = true; return; }
            Caption = text ?? ""; CaptionKind = kind ?? ""; page = captionPage; CaptionAtS = now; pending = false;
        }
        public void Tick(double now)
        { if (pending && now - CaptionAtS >= 3) SetCaption(pendingText, pendingKind, pendingPage, now); }
        public void ClearRail() { rail.Clear(); links.Clear(); }
        public void AddNode(string id, string label, string source, double now)
        {
            if (rail.Any(n => n.Id == id)) return;
            var previous = rail.LastOrDefault();
            rail.Add(new RailNode { Id = id, Label = label, Source = source, AtS = now, SincePreviousS = previous == null ? 0 : now - previous.AtS });
            while (rail.Count > 5) rail.RemoveAt(0);
            links.RemoveWhere(l => !rail.Any(n => n.Id == l.Split('>')[0].TrimEnd('-')) || !rail.Any(n => n.Id == l.Split('>')[1]));
        }
        public void LabelLast(string label) { if (rail.Count > 0) rail[rail.Count - 1].Label = label; }
        public void AddLink(string link) { links.Add(link); }
        public static string Clock(double sim)
        { var minutes = (int)Math.Floor(sim * 60) % 1440; return (minutes / 60).ToString("00", CultureInfo.InvariantCulture) + ":" + (minutes % 60).ToString("00", CultureInfo.InvariantCulture); }
        public static string Beat(double hour)
        { hour %= 24; return hour < 5.75 ? "Night" : hour < 7.5 ? "Dawn" : hour < 15.5 ? "Day" : hour < 22.5 ? "Storm" : "Night"; }
        public static IReadOnlyList<WorldTag> Tags(HarborLayout layout, HarborFrame frame, int limit = 4)
        {
            if (frame == null) return Array.Empty<WorldTag>();
            return layout.Devices.Where(d => frame.AlarmCounts.TryGetValue(d.ExternalId, out var n) && n > 0)
                .Take(Math.Min(4, limit)).Select(d => new WorldTag { Token = d.Token, Text = HumanName(d) + " · alarm" }).ToArray();
        }
        public static string HumanName(LayoutDevice d)
        {
            var quay = d.Quay == "hp-east-quay" ? "E" : d.Quay == "hp-basin" ? "B" : "N";
            return (d.Kind == DeviceKind.Light ? "Lamp " : d.Kind == DeviceKind.Charger ? "Charger " : d.Kind == DeviceKind.MqttCharger ? "MQTT charger " :
                d.Kind == DeviceKind.CertCharger ? "Cert charger " : d.Kind == DeviceKind.PullLight ? "Pull lamp " : DeviceKind.IsGauge(d.Kind) ? "Gauge " : d.Kind == DeviceKind.Pump ? "Pump " : "Building ") + quay + (d.Index > 0 ? d.Index.ToString(CultureInfo.InvariantCulture) : "");
        }
        /// <summary>Whether a recording may end now: no caption is visible, or the visible one has had its 3 s floor.</summary>
        public static bool QuitAllowed(DirectorView view, double nowS) => view == null || QuitAllowed(view.Caption, view.CaptionAtS, nowS);
        public static bool QuitAllowed(string caption, double captionAtS, double nowS) => string.IsNullOrEmpty(caption) || nowS - captionAtS >= 3;
    }
}
