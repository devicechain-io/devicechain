// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Demos.Hud
{
    /// <summary>An sRGB colour with alpha, 0 .. 1 per channel. Engine-free, so the rules that pick colours can be tested without Unity.</summary>
    public readonly struct HudColor
    {
        public HudColor(float r, float g, float b, float a = 1) { R = r; G = g; B = b; A = a; }
        public float R { get; }
        public float G { get; }
        public float B { get; }
        public float A { get; }
        public HudColor WithAlpha(float a) => new HudColor(R, G, B, a);

        /// <summary>WCAG relative luminance of the opaque colour.</summary>
        public double Luminance
        {
            get
            {
                double Linear(float c) => c <= 0.03928 ? c / 12.92 : Math.Pow((c + 0.055) / 1.055, 2.4);
                return 0.2126 * Linear(R) + 0.7152 * Linear(G) + 0.0722 * Linear(B);
            }
        }

        /// <summary>WCAG contrast ratio of two opaque colours, 1 .. 21.</summary>
        public static double Contrast(HudColor a, HudColor b)
        {
            var hi = Math.Max(a.Luminance, b.Luminance); var lo = Math.Min(a.Luminance, b.Luminance);
            return (hi + 0.05) / (lo + 0.05);
        }
    }

    /// <summary>
    /// The one visual language of a DeviceChain demo's story HUD: one size scale, one corner radius, one scrim, and the series' colour
    /// grammar. Sizes are in reference pixels of a 1920 x 1080 frame; the canvas scales them with the frame's height.
    /// </summary>
    public static class HudTheme
    {
        public const float ReferenceWidth = 1920, ReferenceHeight = 1080;

        /// <summary>The size scale: a caption, a chain-card row, and the small capitals of kickers, marks and the top strip.</summary>
        public const int CaptionPx = 34, RowPx = 24, MarkPx = 15;

        public const float CornerRadiusPx = 10;

        /// <summary>Nothing of the HUD is drawn closer to the frame's edge than this.</summary>
        public const float SafeMarginPx = 48;

        /// <summary>
        /// The share of the frame's height a video player's own control bar covers at the bottom. Whatever docks at the bottom (the chain
        /// card) stands above it, and nothing is written at the bottom centre, where a player puts its subtitles.
        /// </summary>
        public const float ControlBarShare = 0.08f;

        /// <summary>
        /// How opaque a panel's scrim is. A panel is read over the worst thing the scene can put behind it (a lamp's bloom, a storm
        /// sky, bright water), so it is nearly solid (a first cut at 92 % left the small grey text at 4.4:1 over a lamp): every text colour of the theme holds <see cref="MinContrast"/> on it over pure
        /// white. The card people read longest (an end card, a final summary) is more solid still, over a darkened frame.
        /// </summary>
        public const float PanelOpacity = 0.94f, SolidOpacity = 0.96f, FrameDim = 0.5f;

        /// <summary>The least contrast any HUD text may have against what is composited behind it.</summary>
        public const double MinContrast = 4.5;

        /// <summary>
        /// A colour with alpha composited over an opaque background, in linear light: how a linear-colour-space project blends it, and
        /// the worse case for a dark scrim over a bright scene (a gamma-space blend lets less of the background through).
        /// </summary>
        public static HudColor Over(HudColor top, HudColor under)
        {
            float ToLinear(float c) => c <= 0.04045f ? c / 12.92f : (float)System.Math.Pow((c + 0.055) / 1.055, 2.4);
            float ToSrgb(float c) => c <= 0.0031308f ? c * 12.92f : (float)(1.055 * System.Math.Pow(c, 1 / 2.4) - 0.055);
            float Mix(float a, float b) => ToSrgb(ToLinear(a) * top.A + ToLinear(b) * (1 - top.A));
            return new HudColor(Mix(top.R, under.R), Mix(top.G, under.G), Mix(top.B, under.B));
        }

        /// <summary>The contrast a text colour has on a background whose WCAG luminance was measured (a render's sampled pixel).</summary>
        public static double ContrastOn(HudColor text, double backgroundLuminance)
        {
            var hi = System.Math.Max(text.Luminance, backgroundLuminance); var lo = System.Math.Min(text.Luminance, backgroundLuminance);
            return (hi + 0.05) / (lo + 0.05);
        }

        /// <summary>
        /// Whether a point of the frame (0 .. 1 across and up) lies on a panel, with a margin: a world marker there is not drawn,
        /// so a pin, a tick or a device's name never sits on the caption box, the card or the strip.
        /// </summary>
        public static bool OnPanel(float x, float y, System.Collections.Generic.IReadOnlyList<float[]> panels, float marginX, float marginY)
        {
            if (panels == null) return false;
            foreach (var p in panels) if (x >= p[0] - marginX && x <= p[2] + marginX && y >= p[1] - marginY && y <= p[3] + marginY) return true;
            return false;
        }

        /// <summary>Text, and the cool grey of anything the scenario (not the platform) says.</summary>
        public static readonly HudColor Ink = new HudColor(0.96f, 0.97f, 0.99f), Dim = new HudColor(0.70f, 0.77f, 0.83f);

        /// <summary>The scrim behind every HUD element, a hairline on it, and the dark edge of a pin, a tick and a badge.</summary>
        public static readonly HudColor Scrim = new HudColor(0.035f, 0.055f, 0.085f, PanelOpacity), Line = new HudColor(0.70f, 0.77f, 0.83f, 0.28f), Edge = new HudColor(0.035f, 0.05f, 0.075f);

        /// <summary>The series' colour grammar: blue in progress and the platform's record, amber alarms, green confirmed, coral refused or failed.</summary>
        /// <summary>The blue of the grammar as TEXT (a card's header, an end card's sub-line): lighter than the blue of dots and rules, so it holds the contrast floor on a panel.</summary>
        public static readonly HudColor BlueText = new HudColor(0.62f, 0.82f, 1f);
        public static readonly HudColor Blue = new HudColor(0.36f, 0.68f, 0.98f), Amber = new HudColor(0.96f, 0.62f, 0.14f), Green = new HudColor(0.36f, 0.86f, 0.52f), Coral = new HudColor(1f, 0.48f, 0.40f);
    }
}
