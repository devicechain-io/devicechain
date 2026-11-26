// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Demos.Hud;
using DeviceChain.Harborpulse.Model.Director;
using DeviceChain.Harborpulse.Visuals;
using UnityEngine;
using UnityEngine.UI;

namespace DeviceChain.Harborpulse.Hud
{
    /// <summary>
    /// Harborpulse's story HUD: the shared demo HUD (top strip, top-centre caption box, chain card, alarm pins, end card) fed from
    /// the director's view, plus the few lines only a live or test run has (a banner, a problem line, the idle "up next" chip, the
    /// offline preview's disclosure). Text, evidence, card rows and caption holds belong to the model; this draws them.
    /// </summary>
    public sealed class HarborStoryHud : IStoryHud
    {
        readonly HarborpulseApp app;
        readonly StoryHud hud;
        readonly Text banner, problem, upNext, preview;
        readonly Image bannerBackground;
        public bool Visible { get => hud.Visible; set => hud.Visible = value; }
        public IEnumerable<string> DrawnText => hud.DrawnText();
        List<StoryHud.Panel> panels = new List<StoryHud.Panel>();
        public IReadOnlyList<KeyValuePair<string, float[]>> Panels => panels.Select(p => new KeyValuePair<string, float[]>(p.Name, new[] { p.Pixels.x, p.Pixels.y, p.Pixels.width, p.Pixels.height, p.Alpha })).ToArray();
        public int MarkersHidden => hud.Pins.Hidden;

        [RuntimeInitializeOnLoadMethod(RuntimeInitializeLoadType.BeforeSceneLoad)]
        static void Register() => HarborpulseApp.HudFactory = app => new HarborStoryHud(app);
#if UNITY_EDITOR
        [UnityEditor.InitializeOnLoadMethod]
        static void RegisterEditor() => Register();
#endif
        public HarborStoryHud(HarborpulseApp app)
        {
            this.app = app;
            hud = new StoryHud(app.sceneCamera, inScene: app.HudInSceneControl);
            banner = Label("Persistent banner", HudTheme.RowPx, out bannerBackground);
            problem = Label("Feed and probe problems", HudTheme.MarkPx + 3, out _); problem.color = new Color(1, .79f, .32f);
            upNext = Label("Up next", HudTheme.MarkPx + 3, out _);
            preview = Label("Offline preview disclosure", HudTheme.MarkPx + 3, out _);
        }
        Text Label(string name, int size, out Image scrim)
        {
            var box = HudUi.Rect(name, hud.Extras);
            scrim = HudUi.Image("Scrim", box, hud.Sprites.Rounded(), HudSprites.C(HudTheme.Scrim), true); HudUi.Stretch(scrim.rectTransform);
            var text = HudUi.Text("Text", box, hud.Font, size, HudSprites.C(HudTheme.Ink), TextAnchor.MiddleCenter); HudUi.Stretch(text.rectTransform);
            box.gameObject.SetActive(false);
            return text;
        }
        /// <summary>A line of the run's own, stacked down the left edge: as wide as its words, hidden when it has none.</summary>
        float Show(Text text, string value, float top, bool bottom = false)
        {
            var box = (RectTransform)text.transform.parent; box.gameObject.SetActive(!string.IsNullOrEmpty(value));
            if (string.IsNullOrEmpty(value)) return top;
            var width = Mathf.Min(hud.FrameSize.x - 2 * HudTheme.SafeMarginPx, HudUi.Width(text, value) + 28); var height = text.fontSize + 16f;
            if (bottom) HudUi.Pin(box, Vector2.zero, Vector2.zero, new Vector2(HudTheme.SafeMarginPx, top), new Vector2(width, height));
            else HudUi.Pin(box, new Vector2(0, 1), new Vector2(0, 1), new Vector2(HudTheme.SafeMarginPx, -top), new Vector2(width, height));
            return top + height + 8;
        }
        public void Cut() { hud.Caption.Cut(); hud.Pins.Settle(); }
        public void Apply(HudFrame f)
        {
            var view = f.View; var tall = app.TallFrame;
            hud.Strip.Set(HudModel.Strip(f.Badge, f.Sim, f.RateChip ?? view.RateChip, view.Chip));
            // A tall frame keeps the bands its camera poses were composed for: the caption low, the card above it across the frame.
            hud.Caption.Top = tall ? (1 - HudModel.CaptionTopTall) * HudTheme.ReferenceHeight : CaptionBoxView.TopPx;
            hud.Caption.Set(new CaptionContent(view.Kicker, view.Caption, view.Note, view.Tone), f.ClockS);
            hud.Card.Dock = view.CardDock; hud.Card.MaxRows = tall ? HudModel.RailRowsTall : ChainCardState.MaxRows;
            hud.Card.BottomPx = tall ? HudModel.RailBottomTall * HudTheme.ReferenceHeight : -1;
            hud.Card.WidthPx = tall ? hud.FrameSize.x - 2 * HudTheme.SafeMarginPx : -1;
            hud.Card.Set(view.Card, f.ClockS, view.CardExitAtS);
            hud.End.Set(view.EndCard, f.ClockS);
            // the run's own lines, down the left edge under the frame's top
            var y = 28f;
            y = Show(banner, view.Banner == "reconnecting" ? "reconnecting · STALE CLOCK" : view.Banner, y);
            bannerBackground.color = (view.Banner ?? "").StartsWith("TEST CONTROL", StringComparison.Ordinal) ? new Color(.55f, .035f, .04f, .98f) : HudSprites.C(HudTheme.Scrim);
            y = Show(problem, string.Join(" | ", new[] { view.Problem, f.ExtraProblem }.Where(s => !string.IsNullOrEmpty(s))), y);
            Show(upNext, view.UpNext, y);
            Show(preview, f.Badge == "OFFLINE PREVIEW" ? "OFFLINE PREVIEW · stand-in values, not platform data" : "", HudTheme.SafeMarginPx, true);
            // The panels first, then the world's markers: a pin, a tick or a name whose place is on a panel is not drawn.
            Canvas.ForceUpdateCanvases();
            panels = hud.Panels(new[] { banner, problem, upNext, preview }.Select(t => (RectTransform)t.transform.parent));
            float w = Mathf.Max(1, Screen.width), h = Mathf.Max(1, Screen.height);
            hud.Pins.Panels = panels.Select(p => new[] { p.Pixels.xMin / w, p.Pixels.yMin / h, p.Pixels.xMax / w, p.Pixels.yMax / h }).ToArray();
            hud.Pins.Draw(f.Pins, f.ClockS, app.sceneCamera,
                key => app.TryPinPlace(key, out var top, out var ground) ? new PinPlace(top, ground) : (PinPlace?)null,
                f.Tags.Select(t => new DeviceTag(t.ExternalId, t.Text)).ToArray());
        }
        public void Dispose() => hud.Dispose();
    }
}
