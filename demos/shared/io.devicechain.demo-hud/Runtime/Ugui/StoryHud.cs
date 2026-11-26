// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using UnityEngine;
using UnityEngine.UI;

namespace DeviceChain.Demos.Hud
{
    /// <summary>Small builders the views share, so every element is made the same way: no raycasts, the built-in font, one scrim.</summary>
    public static class HudUi
    {
        public static RectTransform Rect(string name, Transform parent)
        {
            var go = new GameObject(name, typeof(RectTransform)); go.transform.SetParent(parent, false);
            return (RectTransform)go.transform;
        }
        /// <summary>Pins a rect to one point of its parent: the anchor and the pivot are that point, the offset is in reference pixels.</summary>
        public static void Pin(RectTransform rect, Vector2 anchor, Vector2 pivot, Vector2 offset, Vector2 size)
        {
            rect.anchorMin = rect.anchorMax = anchor; rect.pivot = pivot; rect.anchoredPosition = offset; rect.sizeDelta = size;
        }
        public static Image Image(string name, Transform parent, Sprite sprite, Color colour, bool sliced = false)
        {
            var image = Rect(name, parent).gameObject.AddComponent<Image>();
            image.sprite = sprite; image.color = colour; image.raycastTarget = false; image.type = sliced ? UnityEngine.UI.Image.Type.Sliced : UnityEngine.UI.Image.Type.Simple;
            return image;
        }
        public static Text Text(string name, Transform parent, Font font, int size, Color colour, TextAnchor anchor, bool bold = false)
        {
            var text = Rect(name, parent).gameObject.AddComponent<Text>();
            text.font = font; text.fontSize = size; text.color = colour; text.alignment = anchor; text.fontStyle = bold ? FontStyle.Bold : FontStyle.Normal;
            text.horizontalOverflow = HorizontalWrapMode.Overflow; text.verticalOverflow = VerticalWrapMode.Overflow;
            text.supportRichText = false; text.raycastTarget = false; text.lineSpacing = 1.05f;
            return text;
        }
        public static CanvasGroup Group(Component c)
        {
            var group = c.gameObject.AddComponent<CanvasGroup>(); group.interactable = false; group.blocksRaycasts = false; return group;
        }
        public static void Stretch(RectTransform rect)
        {
            rect.anchorMin = Vector2.zero; rect.anchorMax = Vector2.one; rect.offsetMin = rect.offsetMax = Vector2.zero;
        }
        public static float Width(Text text, string value) { text.text = value ?? ""; return string.IsNullOrEmpty(value) ? 0 : text.preferredWidth; }
    }

    /// <summary>
    /// A demo's story HUD: one canvas holding the five shared pieces, in the order they are drawn (pins under everything, the end
    /// card over everything). A demo makes one, feeds each piece from its own model every frame, and disposes it with its scene.
    /// The HUD is drawn LAST: in play its canvas is a screen-space overlay, composited after the scene and all of its
    /// post-processing, so no lamp's bloom, glow or halo and nothing in the world is ever drawn over a panel. (In the Editor's
    /// edit-mode stills, where only a camera renders, it falls back to that camera's space.)
    /// </summary>
    public sealed class StoryHud : IDisposable
    {
        public GameObject Root { get; }
        public Canvas Canvas { get; }
        public HudSprites Sprites { get; } = new HudSprites();
        public Font Font { get; }
        public AlarmPinLayer Pins { get; }
        public TopStripView Strip { get; }
        public ChainCardView Card { get; }
        public CaptionBoxView Caption { get; }
        public EndCardView End { get; }
        /// <summary>A layer of the demo's own (a banner, a problem line), drawn over the pins and under the story pieces.</summary>
        public RectTransform Extras { get; }

        /// <param name="inScene">A CONTROL: draw the HUD in the camera's own space, under the scene's post-processing, as a HUD must not be. A render's panel check then has something to fail on.</param>
        public StoryHud(Camera camera, string name = "Story HUD", int sortingOrder = 50, bool inScene = false)
        {
            Root = new GameObject(name, typeof(RectTransform));
            Font = Resources.GetBuiltinResource<Font>("LegacyRuntime.ttf");
            this.camera = camera;
            Canvas = Root.AddComponent<Canvas>(); Canvas.renderMode = Application.isPlaying && !inScene ? RenderMode.ScreenSpaceOverlay : RenderMode.ScreenSpaceCamera;
            Canvas.worldCamera = camera; Canvas.planeDistance = 1; Canvas.sortingOrder = sortingOrder;
            var scaler = Root.AddComponent<CanvasScaler>(); scaler.uiScaleMode = CanvasScaler.ScaleMode.ScaleWithScreenSize;
            scaler.referenceResolution = new Vector2(HudTheme.ReferenceWidth, HudTheme.ReferenceHeight); scaler.matchWidthOrHeight = 1;
            Pins = new AlarmPinLayer(this);
            Extras = HudUi.Rect("Extras", Root.transform); HudUi.Stretch(Extras);
            Strip = new TopStripView(this);
            Card = new ChainCardView(this);
            Caption = new CaptionBoxView(this);
            End = new EndCardView(this);
        }

        readonly Camera camera;
        public bool Visible { get => Root.activeSelf; set => Root.SetActive(value); }

        /// <summary>A panel as it stands on screen: its name, its rectangle in screen pixels (origin bottom left) and how much of it is there.</summary>
        public struct Panel { public string Name; public Rect Pixels; public float Alpha; }

        static readonly Vector3[] corners = new Vector3[4];
        Rect Pixels(RectTransform rect)
        {
            rect.GetWorldCorners(corners); Vector2 a = corners[0], b = corners[2];
            if (Canvas.renderMode != RenderMode.ScreenSpaceOverlay && camera != null) { a = camera.WorldToScreenPoint(corners[0]); b = camera.WorldToScreenPoint(corners[2]); }
            return Rect.MinMaxRect(Mathf.Min(a.x, b.x), Mathf.Min(a.y, b.y), Mathf.Max(a.x, b.x), Mathf.Max(a.y, b.y));
        }

        /// <summary>The panels drawn now. A demo adds its own (<paramref name="extra"/>); a render samples their pixels, and the pin layer keeps markers off them.</summary>
        public List<Panel> Panels(IEnumerable<RectTransform> extra = null)
        {
            var list = new List<Panel>();
            void Add(string name, RectTransform rect, float alpha) { if (rect != null && rect.gameObject.activeInHierarchy && alpha > 0.001f) list.Add(new Panel { Name = name, Pixels = Pixels(rect), Alpha = alpha }); }
            Add("strip", Strip.Box, 1); Add("caption", Caption.Box, Caption.State.Alpha); Add("card", Card.Box, Card.State.CardAlpha(Card.LastS)); Add("end card", End.Box, 1);
            if (extra != null) foreach (var rect in extra) Add(rect.name, rect, 1);
            return list;
        }

        /// <summary>The frame's size in reference pixels: the height is always the reference height, the width follows the aspect.</summary>
        public Vector2 FrameSize
        {
            get { var rect = ((RectTransform)Root.transform).rect; return rect.height > 1 ? new Vector2(rect.width, rect.height) : new Vector2(HudTheme.ReferenceWidth, HudTheme.ReferenceHeight); }
        }

        /// <summary>Every string the HUD is drawing now: what a render's text audit lists.</summary>
        public IEnumerable<string> DrawnText()
        {
            foreach (var text in Root.GetComponentsInChildren<Text>(false))
            {
                if (string.IsNullOrEmpty(text.text) || !text.enabled || text.canvasRenderer.GetInheritedAlpha() <= 0.01f) continue;
                yield return text.text;
            }
        }

        public void Dispose()
        {
            Sprites.Dispose();
            if (Root == null) return;
            if (Application.isPlaying) UnityEngine.Object.Destroy(Root); else UnityEngine.Object.DestroyImmediate(Root);
        }
    }

    /// <summary>The small right-aligned strip at the top: RECORDED · place · clock · rate.</summary>
    public sealed class TopStripView
    {
        public const int FontPx = 18;
        readonly Image scrim; readonly Text text; readonly RectTransform box;
        public RectTransform Box => box;
        public TopStripView(StoryHud hud)
        {
            box = HudUi.Rect("Top strip", hud.Root.transform);
            scrim = HudUi.Image("Scrim", box, hud.Sprites.Rounded(), HudSprites.C(HudTheme.Scrim), true); HudUi.Stretch(scrim.rectTransform);
            text = HudUi.Text("Text", box, hud.Font, FontPx, HudSprites.C(HudTheme.Ink), TextAnchor.MiddleCenter); HudUi.Stretch(text.rectTransform);
        }
        /// <summary>One line (see <see cref="TopStrip.Text(string, string, string, string, string)"/>); empty hides the strip.</summary>
        public void Set(string line)
        {
            box.gameObject.SetActive(!string.IsNullOrEmpty(line));
            if (string.IsNullOrEmpty(line)) return;
            var width = HudUi.Width(text, line) + 28;
            HudUi.Pin(box, Vector2.one, Vector2.one, new Vector2(-HudTheme.SafeMarginPx, -28), new Vector2(width, 34));
        }
        /// <summary>The strip's lower edge, in reference pixels below the frame's top: the caption box stands under it.</summary>
        public const float BottomPx = 62;
    }

    /// <summary>
    /// The caption box: top centre, under the strip, as wide as its words and no wider, fading between lines. It is never at the
    /// bottom of the frame, where a video player writes its subtitles.
    /// </summary>
    public sealed class CaptionBoxView
    {
        public const float TopPx = TopStripView.BottomPx + 22, PadX = 30, PadTop = 13, PadBottom = 15, MaxWidthPx = 1080;
        readonly StoryHud hud; readonly RectTransform box; readonly CanvasGroup group; readonly Image scrim, rule; readonly Text kicker, caption, note;
        public CaptionBoxState State { get; } = new CaptionBoxState();
        public RectTransform Box => box;
        /// <summary>Where the box's top edge stands, in reference pixels below the frame's top. A tall frame may move it.</summary>
        public float Top { get; set; } = TopPx;
        public CaptionBoxView(StoryHud hud)
        {
            this.hud = hud;
            box = HudUi.Rect("Caption box", hud.Root.transform); group = HudUi.Group(box);
            scrim = HudUi.Image("Scrim", box, hud.Sprites.Rounded(), HudSprites.C(HudTheme.Scrim), true); HudUi.Stretch(scrim.rectTransform);
            rule = HudUi.Image("Rule", box, null, HudSprites.C(HudTheme.Blue));
            kicker = HudUi.Text("Kicker", box, hud.Font, HudTheme.MarkPx, HudSprites.C(HudTheme.Dim), TextAnchor.UpperCenter);
            caption = HudUi.Text("Caption", box, hud.Font, HudTheme.CaptionPx, HudSprites.C(HudTheme.Ink), TextAnchor.UpperCenter);
            note = HudUi.Text("Note", box, hud.Font, HudTheme.MarkPx + 2, HudSprites.C(HudTheme.Dim), TextAnchor.UpperCenter);
        }
        /// <summary>Asks for a caption and draws the box as its fade stands at <paramref name="nowS"/>.</summary>
        public void Set(CaptionContent content, double nowS) { State.Set(content, nowS); Draw(); }
        public void Cut() => State.Cut();
        void Draw()
        {
            var c = State.Shown; var visible = State.Alpha > 0.001f && !c.IsEmpty;
            box.gameObject.SetActive(visible);
            if (!visible) return;
            group.alpha = State.Alpha;
            var kick = c.Kicker.ToUpperInvariant();
            var lines = c.Text.Length == 0 ? 0 : c.Text.Split('\n').Length; var noteLines = c.Note.Length == 0 ? 0 : c.Note.Split('\n').Length;
            var width = Mathf.Min(Mathf.Min(MaxWidthPx, hud.FrameSize.x - 2 * HudTheme.SafeMarginPx), Mathf.Max(HudUi.Width(kicker, kick), Mathf.Max(HudUi.Width(caption, c.Text), HudUi.Width(note, c.Note))) + 2 * PadX);
            float lineH = HudTheme.CaptionPx * 1.25f, kickH = kick.Length > 0 ? HudTheme.MarkPx + 9 : 0, noteH = noteLines * (HudTheme.MarkPx + 7f);
            var height = PadTop + kickH + lines * lineH + (noteLines > 0 ? noteH + (lines > 0 ? 6 : 0) : 0) + PadBottom;
            HudUi.Pin(box, new Vector2(0.5f, 1), new Vector2(0.5f, 1), new Vector2(0, -Top), new Vector2(width, height));
            HudUi.Pin(rule.rectTransform, new Vector2(0.5f, 1), new Vector2(0.5f, 1), Vector2.zero, new Vector2(width - 2 * HudTheme.CornerRadiusPx, 3));
            rule.color = HudSprites.C(c.Tone == CaptionTone.Platform ? HudTheme.Blue : HudTheme.Dim);
            kicker.gameObject.SetActive(kick.Length > 0); caption.gameObject.SetActive(lines > 0); note.gameObject.SetActive(noteLines > 0);
            HudUi.Pin(kicker.rectTransform, new Vector2(0.5f, 1), new Vector2(0.5f, 1), new Vector2(0, -PadTop), new Vector2(width, kickH));
            HudUi.Pin(caption.rectTransform, new Vector2(0.5f, 1), new Vector2(0.5f, 1), new Vector2(0, -PadTop - kickH), new Vector2(width, lines * lineH));
            HudUi.Pin(note.rectTransform, new Vector2(0.5f, 1), new Vector2(0.5f, 1), new Vector2(0, -PadTop - kickH - lines * lineH - (lines > 0 ? 6 : 0)), new Vector2(width, noteH));
        }
    }

    /// <summary>The end card: three levels on one scrim, in the middle of the frame.</summary>
    public sealed class EndCardView
    {
        public const int BridgePx = 44;
        readonly StoryHud hud; readonly RectTransform box; readonly Image dim; readonly Text bridge, tagline, subline, small; readonly CanvasGroup[] levels = new CanvasGroup[3];
        public EndCardState State { get; } = new EndCardState();
        public RectTransform Box => box;
        public EndCardView(StoryHud hud)
        {
            this.hud = hud;
            // the card people read longest: the whole frame is darkened behind it, and its own backing is nearly solid
            dim = HudUi.Image("End card dim", hud.Root.transform, null, new Color(0, 0, 0, 0)); HudUi.Stretch(dim.rectTransform); dim.gameObject.SetActive(false);
            box = HudUi.Rect("End card", hud.Root.transform);
            var scrim = HudUi.Image("Scrim", box, hud.Sprites.Rounded(), HudSprites.C(HudTheme.Scrim.WithAlpha(HudTheme.SolidOpacity)), true); HudUi.Stretch(scrim.rectTransform);
            bridge = HudUi.Text("Bridge", box, hud.Font, BridgePx, HudSprites.C(HudTheme.Ink), TextAnchor.UpperCenter, true);
            tagline = HudUi.Text("Tagline", box, hud.Font, HudTheme.CaptionPx, HudSprites.C(HudTheme.Ink), TextAnchor.UpperCenter);
            subline = HudUi.Text("Subline", box, hud.Font, HudTheme.RowPx, HudSprites.C(HudTheme.BlueText), TextAnchor.UpperCenter);
            small = HudUi.Text("Small print", box, hud.Font, HudTheme.MarkPx + 2, HudSprites.C(HudTheme.Dim), TextAnchor.UpperCenter);
            levels[0] = HudUi.Group(bridge); levels[1] = HudUi.Group(tagline); levels[2] = HudUi.Group(small);
            box.gameObject.SetActive(false);
        }
        static int Lines(string s) => string.IsNullOrEmpty(s) ? 0 : s.Split('\n').Length;
        public void Set(EndCardContent content, double nowS)
        {
            State.Set(content, nowS); var c = State.Content;
            box.gameObject.SetActive(!c.IsEmpty); dim.gameObject.SetActive(!c.IsEmpty);
            if (c.IsEmpty) return;
            dim.color = new Color(0, 0, 0, HudTheme.FrameDim * State.Alpha[0]);
            var width = Mathf.Max(Mathf.Max(HudUi.Width(bridge, c.Bridge), HudUi.Width(tagline, c.Tagline)), Mathf.Max(HudUi.Width(subline, c.Subline), HudUi.Width(small, c.SmallPrint))) + 96;
            float bridgeH = Lines(c.Bridge) * BridgePx * 1.25f, tagH = Lines(c.Tagline) * HudTheme.CaptionPx * 1.25f, subH = Lines(c.Subline) * HudTheme.RowPx * 1.3f, smallH = Lines(c.SmallPrint) * (HudTheme.MarkPx + 8f);
            const float pad = 36, gap = 26;
            var height = pad + bridgeH + gap + tagH + 8 + subH + gap + smallH + pad;
            HudUi.Pin(box, new Vector2(0.5f, 0.54f), new Vector2(0.5f, 0.5f), Vector2.zero, new Vector2(Mathf.Min(width, hud.FrameSize.x - 2 * HudTheme.SafeMarginPx), height));
            var y = -pad; var top = new Vector2(0.5f, 1);
            HudUi.Pin(bridge.rectTransform, top, top, new Vector2(0, y), new Vector2(width, bridgeH)); y -= bridgeH + gap;
            HudUi.Pin(tagline.rectTransform, top, top, new Vector2(0, y), new Vector2(width, tagH)); y -= tagH + 8;
            HudUi.Pin(subline.rectTransform, top, top, new Vector2(0, y), new Vector2(width, subH)); y -= subH + gap;
            HudUi.Pin(small.rectTransform, top, top, new Vector2(0, y), new Vector2(width, smallH));
            levels[0].alpha = State.Alpha[0]; levels[1].alpha = State.Alpha[1]; levels[2].alpha = State.Alpha[2];
            var sub = subline.color; sub.a = State.Alpha[1]; subline.color = sub;
        }
    }
}
