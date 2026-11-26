// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using System.Globalization;
using DeviceChain.Demos.Hud;

namespace DeviceChain.Harborpulse.Model.Director
{
    /// <summary>
    /// A render's HUD panel check. For every frame, on the text-free margin of every opaque panel, the frame's own pixels are
    /// sampled: what is there must be the panel's scrim over the scene, dark enough that the theme's text holds its contrast floor
    /// on it. A lamp's bloom drawn over a panel, a marker on it, or a scrim too thin over a bright sky all show as a sample that is
    /// too bright, and the render fails. It reads pixels, so it sees what a viewer sees whatever drew it.
    /// </summary>
    public sealed class HudPanelCheck
    {
        /// <summary>The brightest a panel's background may be for the theme's dimmest text (the grey of kickers, marks and cues) to hold the floor on it.</summary>
        public static readonly double MaxLuminance = (HudTheme.Dim.Luminance + 0.05) / HudTheme.MinContrast - 0.05;

        public int Samples { get; private set; }
        public int MarkersHidden { get; set; }
        public double Brightest { get; private set; }
        public string BrightestAt { get; private set; } = "";
        public List<string> Violations { get; } = new List<string>();
        readonly Dictionary<string, int> perPanel = new Dictionary<string, int>();

        public void Sample(int frame, string panel, double luminance)
        {
            Samples++; perPanel[panel] = perPanel.TryGetValue(panel, out var n) ? n + 1 : 1;
            if (luminance > Brightest) { Brightest = luminance; BrightestAt = "frame " + frame.ToString(CultureInfo.InvariantCulture) + ", " + panel; }
            if (luminance > MaxLuminance && Violations.Count < 200)
                Violations.Add("frame " + frame.ToString(CultureInfo.InvariantCulture) + ", " + panel + ": background luminance " + luminance.ToString("0.000", CultureInfo.InvariantCulture)
                    + ", small text contrast " + HudTheme.ContrastOn(HudTheme.Dim, luminance).ToString("0.0", CultureInfo.InvariantCulture) + ":1");
        }

        public string Json(bool control) => DirectorWire.Json(w =>
        {
            w.WriteStartObject(); w.WriteString("check", "every opaque HUD panel's text-free margin, every frame: the frame's pixels there are the panel's");
            w.WriteBoolean("control", control); w.WriteString("verdict", Violations.Count == 0 ? "pass" : "FAIL");
            w.WriteNumber("samples", Samples); w.WriteNumber("contrastFloor", HudTheme.MinContrast); w.WriteNumber("maxBackgroundLuminance", System.Math.Round(MaxLuminance, 4));
            w.WriteNumber("brightestBackground", System.Math.Round(Brightest, 4)); w.WriteString("brightestAt", BrightestAt);
            w.WriteNumber("worstInkContrast", System.Math.Round(HudTheme.ContrastOn(HudTheme.Ink, Brightest), 2)); w.WriteNumber("worstDimTextContrast", System.Math.Round(HudTheme.ContrastOn(HudTheme.Dim, Brightest), 2));
            w.WriteNumber("markersHiddenUnderPanels", MarkersHidden);
            w.WriteStartObject("samplesByPanel"); foreach (var p in perPanel) w.WriteNumber(p.Key, p.Value); w.WriteEndObject();
            w.WriteStartArray("violations"); foreach (var v in Violations) w.WriteStringValue(v); w.WriteEndArray();
            w.WriteEndObject();
        });
    }
}
