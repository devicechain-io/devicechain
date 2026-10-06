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
    /// panel (or a startup error) under it, the observer banner at the top centre while the observer is not
    /// live, and the local simulation panel at the bottom left when asked for. Styled like the data layer's cards (same panel, accent,
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

        const float RefH = 1080f, Margin = 18f, PanelW = 860f, CompactW = 640f, Pad = 14f, ChipH = 36f;

        static Font font, mono;

        readonly GameObject root;
        readonly RectTransform chip, panel, banner, sim;
        readonly Image chipImage;
        readonly Text chipText, panelText, bannerText, simText;

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

            banner = Box(root.transform, "Banner", out _);
            banner.anchorMin = banner.anchorMax = banner.pivot = new Vector2(0.5f, 1f);
            bannerText = Label(banner, "Text", 20, FontStyle.Bold, Warn, font);
            bannerText.alignment = TextAnchor.MiddleCenter;
            bannerText.horizontalOverflow = HorizontalWrapMode.Overflow;
            banner.anchoredPosition = new Vector2(0f, -Margin);
            banner.gameObject.SetActive(false);

            sim = Box(root.transform, "Local Simulation", out _);
            sim.anchorMin = sim.anchorMax = sim.pivot = new Vector2(0f, 0f);
            simText = Label(sim, "Text", 14, FontStyle.Normal, Ink, mono);
            simText.rectTransform.offsetMin = new Vector2(Pad, Pad);
            simText.rectTransform.offsetMax = new Vector2(-Pad, -Pad);
            simText.alignment = TextAnchor.LowerLeft;
            simText.horizontalOverflow = HorizontalWrapMode.Overflow;
            simText.verticalOverflow = VerticalWrapMode.Overflow;
            sim.gameObject.SetActive(false);
        }

        /// <summary>The site-wide observer banner; null hides it.</summary>
        public void SetBanner(string text)
        {
            if (string.IsNullOrEmpty(text))
            {
                banner.gameObject.SetActive(false);
                return;
            }

            banner.gameObject.SetActive(true);
            if (bannerText.text != text) bannerText.text = text;
            banner.sizeDelta = new Vector2(bannerText.preferredWidth + 40f, ChipH);
        }

        /// <summary>The local simulation panel (its text says it is not platform data); null hides it.</summary>
        public void ShowSimulation(string text)
        {
            if (text == null)
            {
                sim.gameObject.SetActive(false);
                return;
            }

            sim.gameObject.SetActive(true);
            sim.anchoredPosition = new Vector2(Margin, Margin);
            simText.text = text;
            sim.sizeDelta = new Vector2(simText.preferredWidth + 2f * Pad, simText.preferredHeight + 2f * Pad);
        }

        public void SetBadge(string text, BadgeTone tone)
        {
            chipText.text = text;
            chipText.color = tone == BadgeTone.Live ? Accent : tone == BadgeTone.Illustrative ? Warn : Fault;
            var w = chipText.preferredWidth + 24f;
            chip.anchoredPosition = new Vector2(Margin, -Margin);
            chip.sizeDelta = new Vector2(w, ChipH);
        }

        /// <summary>The readiness panel: compact (a header, the token and observer lines, one line on whichever devices are not well) or, expanded, every device and the fleet notes.</summary>
        public void ShowReadiness(string summary, string tokenLine, string observerLine, string brief, IReadOnlyList<PanelLine> lines, IReadOnlyList<string> notes, bool expanded) =>
            Show(HudText.Readiness(summary, tokenLine, observerLine, brief, lines, notes, expanded), expanded ? PanelW : CompactW);

        /// <summary>A startup refusal: what could not start, and the lines that say why.</summary>
        public void ShowError(string title, string message)
        {
            var sb = new StringBuilder();
            sb.Append("<b><color=").Append(HudText.Hex(Fault)).Append('>').Append(HudText.Esc(title)).Append("</color></b>\n\n");
            sb.Append(HudText.Esc(message));
            Show(sb.ToString(), PanelW);
        }

        void Show(string text, float width)
        {
            panel.gameObject.SetActive(true);
            panel.anchoredPosition = new Vector2(Margin, -(Margin + ChipH + 8f));
            panel.sizeDelta = new Vector2(width, 100f);
            panelText.text = text;
            panel.sizeDelta = new Vector2(width, panelText.preferredHeight + 2f * Pad);
        }

        /// <summary>A rect given as a distance from the top-left of the frame, in the bottom-left-origin units the data layer lays out in.</summary>
        public static Rect FromTopLeft(float x, float fromTop, Vector2 size) => new Rect(x, RefH - fromTop - size.y, size.x, size.y);

        /// <summary>A rect centred across a frame <paramref name="frameWidth"/> wide, given as a distance from the top.</summary>
        public static Rect FromTopCentre(float frameWidth, float fromTop, Vector2 size) => new Rect((frameWidth - size.x) / 2f, RefH - fromTop - size.y, size.x, size.y);

        /// <summary>
        /// What the HUD has on screen right now, for the data layer to keep its cards off: the badge, the
        /// readiness panel, the banner and the local simulation panel, as the overlay's layout units (a 1080-high
        /// frame <paramref name="frameWidth"/> wide, origin bottom-left).
        /// </summary>
        public void Obstacles(float frameWidth, List<Rect> into)
        {
            if (chip.gameObject.activeSelf && chip.sizeDelta.x > 0f) into.Add(FromTopLeft(Margin, Margin, chip.sizeDelta));
            if (panel.gameObject.activeSelf) into.Add(FromTopLeft(Margin, Margin + ChipH + 8f, panel.sizeDelta));
            if (banner.gameObject.activeSelf) into.Add(FromTopCentre(frameWidth, Margin, banner.sizeDelta));
            if (sim.gameObject.activeSelf) into.Add(new Rect(Margin, Margin, sim.sizeDelta.x, sim.sizeDelta.y));
        }

        public void Destroy()
        {
            if (root != null) UnityEngine.Object.Destroy(root);
        }

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
