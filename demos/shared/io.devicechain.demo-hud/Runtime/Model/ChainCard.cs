// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;

namespace DeviceChain.Demos.Hud
{
    /// <summary>
    /// What a chain card's row is. The trigger is what started the chain and is drawn apart (an outlined diamond, grey, a small
    /// SCENARIO mark when the scenario caused it). A platform row is a step the platform's own record shows. A sender's count is a
    /// number the sending device kept, drawn with a hatched dot and its own mark, because the platform did not count it.
    /// </summary>
    public enum ChainRowKind { ScenarioTrigger, PlatformTrigger, Platform, SenderCount }

    /// <summary>Where the card docks. Bottom right unless the action is in that corner; never the centre third of the frame.</summary>
    public enum CardDock { BottomRight, TopRight, BottomLeft }

    public sealed class ChainRow
    {
        public ChainRow(string id, string label, ChainRowKind kind, double? cueS = null) { Id = id; Label = label ?? ""; Kind = kind; CueS = cueS; }
        public string Id { get; }
        /// <summary>Written for a viewer: "Start command sent", never a rule key.</summary>
        public string Label { get; }
        public ChainRowKind Kind { get; }
        /// <summary>Recorded seconds since the row above, or null when there is nothing to measure from.</summary>
        public double? CueS { get; }
        public bool IsTrigger => Kind == ChainRowKind.ScenarioTrigger || Kind == ChainRowKind.PlatformTrigger;
    }

    public sealed class ChainRowDraw
    {
        public ChainRow Row { get; internal set; }
        /// <summary>0 .. 1: the row's fade-in times its emphasis (the newest row is full, older ones step back).</summary>
        public float Alpha { get; internal set; }
        /// <summary>Reference pixels the row still has to slide up to its place.</summary>
        public float SlidePx { get; internal set; }
        public bool Newest { get; internal set; }
        /// <summary>"+4 s", or "" when the row has no cue.</summary>
        public string Cue { get; internal set; }
    }

    /// <summary>
    /// A chain card: cause and effect, one recorded step at a time. The caller passes the rows that are true now, in order; the card
    /// remembers when each first appeared (on the caller's clock) and fades it in. Rows never appear on a timer of the card's own.
    /// </summary>
    public sealed class ChainCardState
    {
        public const double RowFadeS = 0.2, ExitFadeS = 0.6;
        public const float RowSlidePx = 8, OlderAlpha = 0.7f, MaxWidthPx = 440;
        public const int MaxRows = 4, MaxRowsTall = 2;
        public const string PlatformHeader = "PLATFORM RECORD", ScenarioMark = "SCENARIO", SenderMark = "SENDER'S COUNT";

        readonly Dictionary<string, double> appearedAtS = new Dictionary<string, double>(StringComparer.Ordinal);
        IReadOnlyList<ChainRow> rows = Array.Empty<ChainRow>();
        double? exitAtS;

        /// <summary>
        /// The rows that are true now. A row not passed again is gone at once (its shot was cut away from). <paramref name="exitAtS"/>
        /// is when the card starts to fade, or null while it stays.
        /// </summary>
        public void Set(IReadOnlyList<ChainRow> current, double nowS, double? exitAtS = null)
        {
            rows = current ?? Array.Empty<ChainRow>(); this.exitAtS = exitAtS;
            foreach (var gone in appearedAtS.Keys.Where(id => rows.All(r => r.Id != id)).ToArray()) appearedAtS.Remove(gone);
            foreach (var row in rows) if (!appearedAtS.ContainsKey(row.Id)) appearedAtS.Add(row.Id, nowS);
        }

        /// <summary>Rows already there when a shot begins are simply there: no fade for what the viewer did not see happen.</summary>
        public void Settle()
        {
            foreach (var id in appearedAtS.Keys.ToArray()) appearedAtS[id] = double.NegativeInfinity;
        }

        /// <summary>0 .. 1: the whole card, fading out from its exit instant.</summary>
        public float CardAlpha(double nowS)
        {
            if (rows.Count == 0) return 0;
            return exitAtS.HasValue ? (float)Math.Max(0, Math.Min(1, 1 - (nowS - exitAtS.Value) / ExitFadeS)) : 1;
        }

        /// <summary>
        /// The rows a card of <paramref name="maxRows"/> shows: the trigger (if there is one) and the newest steps under it. The card
        /// stays small; an old step leaves before the trigger does.
        /// </summary>
        public static IReadOnlyList<ChainRow> Window(IReadOnlyList<ChainRow> all, int maxRows)
        {
            if (maxRows < 1) throw new ArgumentOutOfRangeException(nameof(maxRows));
            if (all.Count <= maxRows) return all;
            var trigger = all.FirstOrDefault(r => r.IsTrigger); var steps = all.Where(r => r != trigger).ToArray();
            if (trigger == null || maxRows == 1) return steps.Skip(steps.Length - maxRows).ToArray();
            return new[] { trigger }.Concat(steps.Skip(steps.Length - (maxRows - 1))).ToArray();
        }

        public IReadOnlyList<ChainRowDraw> Draw(double nowS, int maxRows = MaxRows)
        {
            var window = Window(rows, maxRows); var newest = window.LastOrDefault(r => !r.IsTrigger) ?? window.LastOrDefault();
            var result = new List<ChainRowDraw>();
            foreach (var row in window)
            {
                var t = (float)Math.Max(0, Math.Min(1, (nowS - appearedAtS[row.Id]) / RowFadeS));
                result.Add(new ChainRowDraw { Row = row, Newest = row == newest, Alpha = t * (row == newest || row.IsTrigger ? 1 : OlderAlpha), SlidePx = (1 - t) * RowSlidePx, Cue = CueText(row.CueS) });
            }
            return result;
        }

        /// <summary>"+4 s" for a recorded gap; "" for none, or for one that rounds to nothing.</summary>
        public static string CueText(double? seconds)
        {
            if (!seconds.HasValue || double.IsNaN(seconds.Value) || seconds.Value < 0.5) return "";
            var s = seconds.Value;
            if (s < 99.5) return "+" + Math.Round(s, MidpointRounding.AwayFromZero).ToString("0", CultureInfo.InvariantCulture) + " s";
            return "+" + Math.Round(s / 60, MidpointRounding.AwayFromZero).ToString("0", CultureInfo.InvariantCulture) + " min";
        }

        /// <summary>
        /// The card's left and right edges in reference pixels of a frame <paramref name="frameWidthPx"/> wide, for a dock. Whatever
        /// the dock and however narrow the frame, the card keeps out of the centre third.
        /// </summary>
        public static void Span(CardDock dock, float frameWidthPx, out float left, out float right)
        {
            var width = Math.Min(MaxWidthPx, frameWidthPx / 3 - HudTheme.SafeMarginPx);
            if (dock == CardDock.BottomLeft) { left = HudTheme.SafeMarginPx; right = left + width; }
            else { right = frameWidthPx - HudTheme.SafeMarginPx; left = right - width; }
        }

        /// <summary>How far above the frame's bottom edge a bottom-docked card stands: clear of a video player's control bar.</summary>
        public static float BottomMarginPx(float frameHeightPx) => Math.Max(HudTheme.SafeMarginPx, HudTheme.ControlBarShare * frameHeightPx);
    }
}
