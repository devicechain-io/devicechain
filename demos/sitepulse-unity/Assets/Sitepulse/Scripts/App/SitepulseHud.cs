// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text;
using DeviceChain.Sitepulse.Platform;
using UnityEngine;
using UnityEngine.UI;

namespace DeviceChain.Sitepulse.App
{
    public enum BadgeTone { Live, Illustrative, Error }

    /// <summary>
    /// The screen-space HUD: the mode badge, always on screen at the top left, and the readiness
    /// panel (or a startup error) under it. Styled like the data layer's cards (same panel, accent,
    /// ink and muted colours; laid out in the units of a 1080-pixel-high frame) and kept separate
    /// from the overlay, whose cards hang on the camera. Plain legacy <c>Text</c>, rich text for the
    /// per-line colour, one text block so a refresh is one assignment.
    /// </summary>
    public sealed class SitepulseHud
    {
        // mirrors IotOverlay's palette, which keeps its own private
        static readonly Color Panel = new Color(0.06f, 0.08f, 0.10f, 0.96f);
        static readonly Color Accent = new Color(0.36f, 0.86f, 0.96f, 1f);
        static readonly Color Ok = new Color(0.36f, 0.86f, 0.46f, 1f);
        static readonly Color Warn = new Color(1f, 0.70f, 0.12f, 1f);
        static readonly Color Ink = new Color(0.93f, 0.95f, 0.96f, 1f);
        static readonly Color Muted = new Color(0.60f, 0.68f, 0.72f, 1f);
        static readonly Color Fault = new Color(1f, 0.42f, 0.38f, 1f);

        const float RefH = 1080f, Margin = 18f, PanelW = 860f, Pad = 14f, ChipH = 36f;

        static Font font, mono;

        readonly GameObject root;
        readonly RectTransform chip, panel;
        readonly Image chipImage;
        readonly Text chipText, panelText;

        public SitepulseHud(Transform parent)
        {
            font = font != null ? font : Resources.GetBuiltinResource<Font>("LegacyRuntime.ttf");
            mono = mono != null ? mono : Font.CreateDynamicFontFromOSFont(new[] { "Consolas", "Cascadia Mono", "Lucida Console", "DejaVu Sans Mono", "Menlo", "Courier New" }, 16);
            if (mono == null) mono = font;

            root = new GameObject("Sitepulse HUD (generated)", typeof(RectTransform)) { hideFlags = HideFlags.DontSave };
            root.transform.SetParent(parent, false);
            var canvas = root.AddComponent<Canvas>();
            canvas.renderMode = RenderMode.ScreenSpaceOverlay;
            canvas.sortingOrder = 500; // above the data layer's cards
            var scaler = root.AddComponent<CanvasScaler>();
            scaler.uiScaleMode = CanvasScaler.ScaleMode.ScaleWithScreenSize;
            scaler.referenceResolution = new Vector2(RefH * 16f / 9f, RefH);
            scaler.screenMatchMode = CanvasScaler.ScreenMatchMode.MatchWidthOrHeight;
            scaler.matchWidthOrHeight = 1f;

            chip = Box(root.transform, "Badge", out chipImage);
            chipText = Label(chip, "Text", 18, FontStyle.Bold, Accent, font);
            chipText.rectTransform.offsetMin = new Vector2(12f, 0f);
            chipText.rectTransform.offsetMax = new Vector2(-12f, 0f);
            chipText.alignment = TextAnchor.MiddleLeft;
            chipText.horizontalOverflow = HorizontalWrapMode.Overflow;

            panel = Box(root.transform, "Panel", out _);
            panelText = Label(panel, "Text", 16, FontStyle.Normal, Ink, mono);
            panelText.rectTransform.offsetMin = new Vector2(Pad, Pad);
            panelText.rectTransform.offsetMax = new Vector2(-Pad, -Pad);
            panelText.alignment = TextAnchor.UpperLeft;
            panelText.horizontalOverflow = HorizontalWrapMode.Wrap;
            panelText.verticalOverflow = VerticalWrapMode.Overflow;
            panelText.supportRichText = true;
            panel.gameObject.SetActive(false);
        }

        public void SetBadge(string text, BadgeTone tone)
        {
            chipText.text = text;
            chipText.color = tone == BadgeTone.Live ? Accent : tone == BadgeTone.Illustrative ? Warn : Fault;
            var w = chipText.preferredWidth + 24f;
            chip.anchoredPosition = new Vector2(Margin, -Margin);
            chip.sizeDelta = new Vector2(w, ChipH);
        }

        /// <summary>The readiness panel: a summary, the token line, one line per device, and fleet notes.</summary>
        public void ShowReadiness(string summary, string tokenLine, string observerLine, IReadOnlyList<PanelLine> lines, IReadOnlyList<string> notes)
        {
            var sb = new StringBuilder();
            sb.Append("<b><color=").Append(Hex(Accent)).Append('>').Append(Esc(summary)).Append("</color></b>\n");
            sb.Append("<color=").Append(Hex(Muted)).Append('>').Append(Esc(tokenLine)).Append("</color>\n");
            sb.Append("<color=").Append(Hex(Muted)).Append('>').Append(Esc(observerLine)).Append("</color>\n\n");
            foreach (var l in lines)
            {
                var c = l.Kind == LineKind.Ok ? Ok : l.Kind == LineKind.Failed ? Warn : Muted;
                sb.Append("<color=").Append(Hex(c)).Append('>').Append(Esc(l.Text)).Append("</color>\n");
            }

            foreach (var n in notes)
                sb.Append("\n<color=").Append(Hex(Warn)).Append('>').Append(Esc(n)).Append("</color>");
            Show(sb.ToString());
        }

        /// <summary>A startup refusal: what could not start, and the lines that say why.</summary>
        public void ShowError(string title, string message)
        {
            var sb = new StringBuilder();
            sb.Append("<b><color=").Append(Hex(Fault)).Append('>').Append(Esc(title)).Append("</color></b>\n\n");
            sb.Append(Esc(message));
            Show(sb.ToString());
        }

        void Show(string text)
        {
            panel.gameObject.SetActive(true);
            panel.anchoredPosition = new Vector2(Margin, -(Margin + ChipH + 8f));
            panel.sizeDelta = new Vector2(PanelW, 100f);
            panelText.text = text;
            panel.sizeDelta = new Vector2(PanelW, panelText.preferredHeight + 2f * Pad);
        }

        public void Destroy()
        {
            if (root != null) UnityEngine.Object.Destroy(root);
        }

        // a server's reason may carry angle brackets; rich text would read them as tags
        static string Esc(string s) => (s ?? "").Replace('<', '(').Replace('>', ')');

        static string Hex(Color c) => "#" + ColorUtility.ToHtmlStringRGB(c);

        static RectTransform Box(Transform parent, string name, out Image image)
        {
            var go = new GameObject(name, typeof(RectTransform));
            go.transform.SetParent(parent, false);
            var rt = (RectTransform)go.transform;
            rt.anchorMin = rt.anchorMax = rt.pivot = new Vector2(0f, 1f);
            image = go.AddComponent<Image>();
            image.color = Panel;
            image.raycastTarget = false;
            return rt;
        }

        static Text Label(Transform parent, string name, int size, FontStyle style, Color color, Font face)
        {
            var go = new GameObject(name, typeof(RectTransform));
            go.transform.SetParent(parent, false);
            var rt = (RectTransform)go.transform;
            rt.anchorMin = Vector2.zero;
            rt.anchorMax = Vector2.one;
            var t = go.AddComponent<Text>();
            t.font = face;
            t.fontSize = size;
            t.fontStyle = style;
            t.color = color;
            t.raycastTarget = false;
            return t;
        }
    }
}
