// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Collections.Generic;
using UnityEngine;
using UnityEngine.UI;

namespace DeviceChain.Demos.Hud
{
    /// <summary>
    /// The chain card: a compact card docked in a corner, with the trigger row set apart and the platform's steps on a spine under
    /// one "PLATFORM RECORD" header, each with the recorded seconds since the step above. Rows and timing are
    /// <see cref="ChainCardState"/>'s; this only draws them.
    /// </summary>
    public sealed class ChainCardView
    {
        const float Pad = 16, RowH = 38, TriggerGap = 9, HeaderH = 24, IconX = 22, LabelX = 44, CuePx = 18, MarkTextPx = 12;
        sealed class RowView { public RectTransform Rect; public CanvasGroup Group; public Image Icon, MarkFrame; public Text Label, Cue, Mark; public Image Spine; }
        readonly StoryHud hud; readonly RectTransform box; readonly CanvasGroup group; readonly Image hairline; readonly Text header;
        readonly RowView[] rows = new RowView[ChainCardState.MaxRows];
        public ChainCardState State { get; } = new ChainCardState();
        public RectTransform Box => box;
        /// <summary>The clock of the last <see cref="Set"/>.</summary>
        public double LastS { get; private set; }
        public CardDock Dock { get; set; } = CardDock.BottomRight;
        /// <summary>How many rows the card may show: fewer in a tall frame.</summary>
        public int MaxRows { get; set; } = ChainCardState.MaxRows;
        /// <summary>Reference pixels between a bottom-docked card and the frame's bottom; negative leaves the default (clear of a player's control bar).</summary>
        public float BottomPx { get; set; } = -1;
        /// <summary>The card's width in reference pixels; negative leaves the default (at most 440, out of the centre third). A tall frame, a third of which is too narrow for a row, sets it.</summary>
        public float WidthPx { get; set; } = -1;

        public ChainCardView(StoryHud hud)
        {
            this.hud = hud;
            box = HudUi.Rect("Chain card", hud.Root.transform); group = HudUi.Group(box);
            var scrim = HudUi.Image("Scrim", box, hud.Sprites.Rounded(), HudSprites.C(HudTheme.Scrim), true); HudUi.Stretch(scrim.rectTransform);
            hairline = HudUi.Image("Hairline", box, null, HudSprites.C(HudTheme.Line));
            header = HudUi.Text("Header", box, hud.Font, HudTheme.MarkPx - 2, HudSprites.C(HudTheme.BlueText), TextAnchor.MiddleLeft);
            for (var i = 0; i < rows.Length; i++)
            {
                var r = new RowView { Rect = HudUi.Rect("Row " + i, box) }; r.Group = HudUi.Group(r.Rect);
                r.Spine = HudUi.Image("Spine", r.Rect, null, HudSprites.C(HudTheme.Blue, 0.55f));
                r.Icon = HudUi.Image("Icon", r.Rect, hud.Sprites.Dot(), Color.white);
                r.Label = HudUi.Text("Label", r.Rect, hud.Font, HudTheme.RowPx, HudSprites.C(HudTheme.Ink), TextAnchor.MiddleLeft);
                r.Cue = HudUi.Text("Cue", r.Rect, hud.Font, (int)CuePx, HudSprites.C(HudTheme.Dim), TextAnchor.MiddleRight);
                r.MarkFrame = HudUi.Image("Mark frame", r.Rect, hud.Sprites.PillOutline(), HudSprites.C(HudTheme.Dim, 0.7f), true);
                r.Mark = HudUi.Text("Mark", r.Rect, hud.Font, (int)MarkTextPx, HudSprites.C(HudTheme.Dim), TextAnchor.MiddleCenter);
                rows[i] = r;
            }
            box.gameObject.SetActive(false);
        }

        /// <summary>The rows that are true now (see <see cref="ChainCardState.Set"/>), drawn as they stand at <paramref name="nowS"/>.</summary>
        public void Set(IReadOnlyList<ChainRow> current, double nowS, double? exitAtS = null)
        {
            State.Set(current, nowS, exitAtS); LastS = nowS;
            var draws = State.Draw(nowS, Mathf.Clamp(MaxRows, 1, rows.Length)); var alpha = State.CardAlpha(nowS);
            box.gameObject.SetActive(draws.Count > 0 && alpha > 0.001f);
            if (!box.gameObject.activeSelf) return;
            group.alpha = alpha;
            ChainCardState.Span(Dock, hud.FrameSize.x, out var left, out var right);
            if (WidthPx > 0) { left = Dock == CardDock.BottomLeft ? HudTheme.SafeMarginPx : hud.FrameSize.x - HudTheme.SafeMarginPx - WidthPx; right = left + WidthPx; }
            var width = right - left;
            // lay the rows out from the top; the card is as tall as what it holds
            var y = Pad; var steps = 0; var headerShown = false; var hairlineY = -1f; var ys = new float[draws.Count];
            for (var i = 0; i < draws.Count; i++)
            {
                var trigger = draws[i].Row.IsTrigger;
                if (!trigger && !headerShown) { headerShown = true; header.rectTransform.anchoredPosition = new Vector2(LabelX, -y); y += HeaderH; }
                ys[i] = y; y += RowH;
                if (trigger && i + 1 < draws.Count) { hairlineY = y + TriggerGap / 2; y += TriggerGap; }
                if (!trigger) steps++;
            }
            var height = y + Pad - 4;
            var bottom = BottomPx >= 0 ? BottomPx : ChainCardState.BottomMarginPx(hud.FrameSize.y);
            if (Dock == CardDock.TopRight) HudUi.Pin(box, new Vector2(0, 1), new Vector2(0, 1), new Vector2(left, -(TopStripView.BottomPx + 22)), new Vector2(width, height));
            else HudUi.Pin(box, Vector2.zero, Vector2.zero, new Vector2(left, bottom), new Vector2(width, height));
            header.gameObject.SetActive(headerShown); header.text = ChainCardState.PlatformHeader;
            if (headerShown) { header.rectTransform.anchorMin = header.rectTransform.anchorMax = new Vector2(0, 1); header.rectTransform.pivot = new Vector2(0, 1); header.rectTransform.sizeDelta = new Vector2(width - LabelX - Pad, HeaderH); }
            hairline.gameObject.SetActive(hairlineY >= 0);
            if (hairlineY >= 0) HudUi.Pin(hairline.rectTransform, new Vector2(0, 1), new Vector2(0, 1), new Vector2(Pad, -hairlineY), new Vector2(width - 2 * Pad, 1));
            for (var i = 0; i < rows.Length; i++)
            {
                var r = rows[i]; r.Rect.gameObject.SetActive(i < draws.Count);
                if (i >= draws.Count) continue;
                var d = draws[i]; var row = d.Row; var trigger = row.IsTrigger;
                HudUi.Pin(r.Rect, new Vector2(0, 1), new Vector2(0, 1), new Vector2(0, -ys[i] - d.SlidePx), new Vector2(width, RowH));
                r.Group.alpha = d.Alpha;
                r.Icon.sprite = trigger ? hud.Sprites.Diamond() : row.Kind == ChainRowKind.SenderCount ? hud.Sprites.HatchedDot() : hud.Sprites.Dot();
                r.Icon.color = HudSprites.C(row.Kind == ChainRowKind.ScenarioTrigger ? HudTheme.Dim : HudTheme.Blue);
                HudUi.Pin(r.Icon.rectTransform, new Vector2(0, 0.5f), new Vector2(0.5f, 0.5f), new Vector2(IconX, 0), Vector2.one * (trigger ? 17 : 14));
                // the spine joins a step to the step above it
                var joined = !trigger && i > 0 && !draws[i - 1].Row.IsTrigger;
                r.Spine.gameObject.SetActive(joined);
                if (joined) HudUi.Pin(r.Spine.rectTransform, new Vector2(0, 0.5f), new Vector2(0.5f, 0), new Vector2(IconX, 0), new Vector2(2, ys[i] - ys[i - 1]));
                r.Label.text = row.Label; r.Label.fontStyle = d.Newest && !trigger ? FontStyle.Bold : FontStyle.Normal;
                r.Label.color = HudSprites.C(row.Kind == ChainRowKind.ScenarioTrigger ? HudTheme.Dim : HudTheme.Ink);
                var mark = row.Kind == ChainRowKind.ScenarioTrigger ? ChainCardState.ScenarioMark : row.Kind == ChainRowKind.SenderCount ? ChainCardState.SenderMark : "";
                r.Mark.gameObject.SetActive(mark.Length > 0); r.MarkFrame.gameObject.SetActive(mark.Length > 0); r.Cue.gameObject.SetActive(mark.Length == 0 && d.Cue.Length > 0);
                var rightEdge = width - Pad;
                if (mark.Length > 0)
                {
                    var markW = HudUi.Width(r.Mark, mark) + 18;
                    HudUi.Pin(r.MarkFrame.rectTransform, new Vector2(0, 0.5f), new Vector2(1, 0.5f), new Vector2(rightEdge, 0), new Vector2(markW, 22));
                    HudUi.Pin(r.Mark.rectTransform, new Vector2(0, 0.5f), new Vector2(1, 0.5f), new Vector2(rightEdge, 0), new Vector2(markW, 22));
                    rightEdge -= markW + 8;
                }
                else if (d.Cue.Length > 0)
                {
                    var cueW = HudUi.Width(r.Cue, d.Cue);
                    HudUi.Pin(r.Cue.rectTransform, new Vector2(0, 0.5f), new Vector2(1, 0.5f), new Vector2(rightEdge, 0), new Vector2(cueW, RowH));
                    rightEdge -= cueW + 10;
                }
                HudUi.Pin(r.Label.rectTransform, new Vector2(0, 0.5f), new Vector2(0, 0.5f), new Vector2(LabelX, 0), new Vector2(Mathf.Max(10, rightEdge - LabelX), RowH));
                // a label too long for its row is drawn smaller rather than over its cue
                r.Label.horizontalOverflow = HorizontalWrapMode.Wrap; r.Label.verticalOverflow = VerticalWrapMode.Truncate;
                r.Label.resizeTextForBestFit = true; r.Label.resizeTextMaxSize = HudTheme.RowPx; r.Label.resizeTextMinSize = 16;
            }
        }
    }
}
