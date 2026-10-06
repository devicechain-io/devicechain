// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using System.Text;
using DeviceChain.Sitepulse.Platform;
using UnityEngine;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>
    /// The readiness panel's text (rich text for the per-line colour), decided from values alone so it can
    /// be tested without a canvas. The panel is compact by default (a header, the token line, the observer
    /// line and one line naming whichever devices are not where the header says they should be) and lists
    /// every device only when asked (key R).
    /// </summary>
    public static class HudText
    {
        public static readonly Color Accent = new Color(0.36f, 0.86f, 0.96f, 1f);
        public static readonly Color Ok = new Color(0.36f, 0.86f, 0.46f, 1f);
        public static readonly Color Warn = new Color(1f, 0.70f, 0.12f, 1f);
        public static readonly Color Muted = new Color(0.60f, 0.68f, 0.72f, 1f);
        public static readonly Color Fault = new Color(1f, 0.42f, 0.38f, 1f);

        public const string CompactHint = "R · all devices";
        public const string ExpandedHint = "R · compact";

        public static string Readiness(string summary, string tokenLine, string observerLine, string brief,
            IReadOnlyList<PanelLine> lines, IReadOnlyList<string> notes, bool expanded)
        {
            var sb = new StringBuilder();
            sb.Append("<b><color=").Append(Hex(Accent)).Append('>').Append(Esc(summary)).Append("</color></b>\n");
            sb.Append("<color=").Append(Hex(Muted)).Append('>').Append(Esc(tokenLine)).Append("</color>\n");
            sb.Append("<color=").Append(Hex(Muted)).Append('>').Append(Esc(observerLine)).Append("</color>\n");
            sb.Append("<color=").Append(Hex(brief != null && brief.StartsWith("all ") ? Ok : Warn)).Append('>').Append(Esc(brief)).Append("</color>\n");
            sb.Append("<color=").Append(Hex(Muted)).Append('>').Append(expanded ? ExpandedHint : CompactHint).Append("</color>");
            if (!expanded) return sb.ToString();

            sb.Append("\n\n");
            foreach (var l in lines)
            {
                var c = l.Kind == LineKind.Ok ? Ok : l.Kind == LineKind.Failed ? Warn : Muted;
                sb.Append("<color=").Append(Hex(c)).Append('>').Append(Esc(l.Text)).Append("</color>\n");
            }

            foreach (var n in notes)
                sb.Append("\n<color=").Append(Hex(Warn)).Append('>').Append(Esc(n)).Append("</color>");
            return sb.ToString();
        }

        // a server's reason may carry angle brackets; rich text would read them as tags
        public static string Esc(string s) => (s ?? "").Replace('<', '(').Replace('>', ')');

        public static string Hex(Color c) => "#" + ColorUtility.ToHtmlStringRGB(c);
    }
}
