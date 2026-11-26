// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;

namespace DeviceChain.Demos.Hud
{
    /// <summary>
    /// The small right-aligned strip at the top of the frame: what kind of picture this is (RECORDED, LIVE), the place, the scene's
    /// clock, and a rate chip when the shot does not run at the speed it was recorded. It replaces a full-width bar.
    /// </summary>
    public static class TopStrip
    {
        public const string Separator = "  ·  ";

        /// <summary>"×1.2" for a shot that runs faster or slower than it was recorded, "" for one that does not. The sign is ×, not the letter x.</summary>
        public static string Rate(double rate)
        {
            if (double.IsNaN(rate) || double.IsInfinity(rate) || rate <= 0 || Math.Abs(rate - 1) <= 0.01) return "";
            return "×" + (rate >= 10 ? rate.ToString("0", CultureInfo.InvariantCulture) : rate.ToString("0.#", CultureInfo.InvariantCulture));
        }

        /// <summary>The strip's one line: its parts in order, the empty ones left out.</summary>
        public static string Join(params string[] parts) => string.Join(Separator, (parts ?? Array.Empty<string>()).Where(p => !string.IsNullOrEmpty(p)));

        /// <summary>The badge in capitals, then the place, the clock, the rate chip and any other chip (a shot shown out of order says so here).</summary>
        public static string Text(string badge, string place, string clock, string rateChip, string chip = null)
            => Join((badge ?? "").ToUpperInvariant(), place, clock, rateChip, chip);
    }

    /// <summary>A film's last card, in three levels: one large line, a tagline with its sub-line, and small print.</summary>
    public sealed class EndCardContent
    {
        public EndCardContent(string bridge, string tagline, string subline, string smallPrint)
        { Bridge = bridge ?? ""; Tagline = tagline ?? ""; Subline = subline ?? ""; SmallPrint = smallPrint ?? ""; }
        public string Bridge { get; }
        public string Tagline { get; }
        public string Subline { get; }
        public string SmallPrint { get; }
        public bool IsEmpty => Bridge.Length + Tagline.Length + Subline.Length + SmallPrint.Length == 0;
        public IEnumerable<string> Lines => new[] { Bridge, Tagline, Subline, SmallPrint }.Where(l => l.Length > 0);
    }

    /// <summary>The end card's fades: each level fades in when its words first arrive, and the card goes with its shot.</summary>
    public sealed class EndCardState
    {
        public const double FadeS = 0.4;
        readonly double[] since = { double.NaN, double.NaN, double.NaN };
        public EndCardContent Content { get; private set; } = new EndCardContent("", "", "", "");
        /// <summary>0 .. 1 for the bridge, the tagline pair and the small print.</summary>
        public float[] Alpha { get; } = new float[3];

        public void Set(EndCardContent content, double nowS)
        {
            Content = content ?? new EndCardContent("", "", "", "");
            var has = new[] { Content.Bridge.Length > 0, Content.Tagline.Length + Content.Subline.Length > 0, Content.SmallPrint.Length > 0 };
            for (var i = 0; i < 3; i++)
            {
                if (!has[i]) since[i] = double.NaN;
                else if (double.IsNaN(since[i])) since[i] = nowS;
                Alpha[i] = has[i] ? (float)Math.Max(0, Math.Min(1, (nowS - since[i]) / FadeS)) : 0;
            }
        }
    }
}
