// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using DeviceChain.Harborpulse.Model.Director;
using DeviceChain.Harborpulse.Visuals;
using UnityEngine;
using UnityEngine.UI;

namespace DeviceChain.Harborpulse.Hud
{
    /// <summary>Thin uGUI projection. Text, evidence, rail order and caption holds belong to HudModel.</summary>
    public sealed class HarborStoryHud : IStoryHud
    {
        readonly HarborpulseApp app;
        readonly GameObject root;
        readonly Font font;
        readonly Text top, caption, banner, problem, upNext, preview, overlay;
        readonly Text[] nodes = new Text[5], tags = new Text[4];
        readonly Image bannerBackground;
        readonly GameObject[,] shapes = new GameObject[5, 3];
        public bool Visible { get => root.activeSelf; set => root.SetActive(value); }

        [RuntimeInitializeOnLoadMethod(RuntimeInitializeLoadType.BeforeSceneLoad)]
        static void Register() => HarborpulseApp.HudFactory = app => new HarborStoryHud(app);
#if UNITY_EDITOR
        [UnityEditor.InitializeOnLoadMethod]
        static void RegisterEditor() => Register();
#endif
        public HarborStoryHud(HarborpulseApp app)
        {
            this.app = app;
            root = new GameObject("Story HUD", typeof(RectTransform));
            font = Resources.GetBuiltinResource<Font>("LegacyRuntime.ttf");
            var canvas = root.AddComponent<Canvas>(); canvas.renderMode = RenderMode.ScreenSpaceCamera;
            canvas.worldCamera = app.sceneCamera; canvas.planeDistance = 1; canvas.sortingOrder = 50;
            var scaler = root.AddComponent<CanvasScaler>(); scaler.uiScaleMode = CanvasScaler.ScaleMode.ScaleWithScreenSize;
            scaler.referenceResolution = new Vector2(1920, 1080); scaler.matchWidthOrHeight = 1;
            top = Label("Top bar", new Vector2(0, .935f), Vector2.one, 24, true);
            top.alignment = TextAnchor.MiddleLeft;
            caption = Label("Caption", new Vector2(.055f, HudModel.CaptionBottomWide), new Vector2(.75f, HudModel.CaptionTopWide), 34, true);
            caption.alignment = TextAnchor.MiddleLeft;
            banner = Label("Persistent banner", new Vector2(.15f, .86f), new Vector2(.85f, .925f), 28, true);
            bannerBackground = banner.transform.parent.GetComponent<Image>();
            problem = Label("Feed and probe problems", new Vector2(.03f, .79f), new Vector2(.97f, .85f), 24, true);
            problem.color = new Color(1, .79f, .32f);
            upNext = Label("Up next", new Vector2(.035f, .27f), new Vector2(.56f, .32f), 22, true);
            preview = Label("Offline preview disclosure", new Vector2(.03f, .015f), new Vector2(.97f, .06f), 20, true);
            overlay = Label("Cut disclosure", new Vector2(.055f, .40f), new Vector2(.62f, .50f), 28, true);
            for (var i = 0; i < nodes.Length; i++)
            {
                nodes[i] = Label("Chain " + i, new Vector2(.735f, HudModel.RailRowTop - HudModel.RailRowHeight - i * HudModel.RailRowStep), new Vector2(.985f, HudModel.RailRowTop - i * HudModel.RailRowStep), 22, true);
                nodes[i].GetComponent<RectTransform>().offsetMin = new Vector2(40, 4);
                for (var kind = 0; kind < 3; kind++) shapes[i, kind] = Shape(nodes[i].transform.parent, kind);
            }
            for (var i = 0; i < tags.Length; i++) tags[i] = Label("World tag " + i, Vector2.zero, Vector2.zero, 22, true);
        }
        Text Label(string name, Vector2 min, Vector2 max, int size, bool scrim)
        {
            var box = new GameObject(name, typeof(RectTransform)); box.transform.SetParent(root.transform, false);
            var rect = box.GetComponent<RectTransform>(); rect.anchorMin = min; rect.anchorMax = max;
            rect.offsetMin = Vector2.zero; rect.offsetMax = Vector2.zero;
            if (scrim) box.AddComponent<Image>().color = new Color(.018f, .028f, .045f, .94f);
            var child = new GameObject("Text", typeof(RectTransform)); child.transform.SetParent(box.transform, false);
            var tr = child.GetComponent<RectTransform>(); tr.anchorMin = Vector2.zero; tr.anchorMax = Vector2.one;
            tr.offsetMin = new Vector2(12, 4); tr.offsetMax = new Vector2(-12, -4);
            var text = child.AddComponent<Text>(); text.font = font; text.fontSize = size; text.color = new Color(.96f, .97f, .99f);
            text.alignment = TextAnchor.MiddleLeft; text.horizontalOverflow = HorizontalWrapMode.Wrap; text.verticalOverflow = VerticalWrapMode.Truncate;
            text.supportRichText = false; text.raycastTarget = false;
            return text;
        }
        static GameObject Shape(Transform parent, int kind)
        {
            var go = new GameObject("Source shape " + kind, typeof(RectTransform)); go.transform.SetParent(parent, false);
            var rect = go.GetComponent<RectTransform>(); rect.anchorMin = rect.anchorMax = new Vector2(0, .5f); rect.anchoredPosition = new Vector2(22, 0); rect.sizeDelta = new Vector2(20, 20);
            void Bar(Vector2 at, Vector2 size, float rotation = 0)
            {
                var part = new GameObject("Mark", typeof(RectTransform)); part.transform.SetParent(go.transform, false);
                var r = part.GetComponent<RectTransform>(); r.sizeDelta = size; r.anchoredPosition = at; r.localRotation = Quaternion.Euler(0, 0, rotation);
                var image = part.AddComponent<Image>(); image.color = kind == 0 ? new Color(.70f, .77f, .83f) : new Color(.94f, .95f, .97f); image.raycastTarget = false;
            }
            if (kind == 1) Bar(Vector2.zero, new Vector2(18, 18));
            else
            {
                Bar(new Vector2(-9, 0), new Vector2(2, 20)); Bar(new Vector2(9, 0), new Vector2(2, 20));
                Bar(new Vector2(0, -9), new Vector2(20, 2)); Bar(new Vector2(0, 9), new Vector2(20, 2));
                if (kind == 2) for (var y = -5; y <= 5; y += 5) Bar(new Vector2(0, y), new Vector2(16, 2), 25);
            }
            return go;
        }
        static void Show(Text text, string value) { text.text = value; text.transform.parent.gameObject.SetActive(!string.IsNullOrEmpty(value)); }
        public void Apply(DirectorView view, string badge, double sim, string rate, IReadOnlyList<WorldTag> worldTags, string extraProblem)
        {
            var vertical = app.TallFrame;
            Show(top, badge + "  ·  Port Alder       " + HudModel.Clock(sim) + "  ·  " + HudModel.Beat(sim) + "       " + rate + (badge == "RECORDED" ? "" : "   [E] engineer"));
            Show(caption, view.Caption);
            var captionRect = caption.transform.parent.GetComponent<RectTransform>();
            captionRect.anchorMin = new Vector2(.055f, vertical ? HudModel.CaptionBottomTall : HudModel.CaptionBottomWide);
            captionRect.anchorMax = new Vector2(vertical ? .945f : .73f, vertical ? HudModel.CaptionTopTall : HudModel.CaptionTopWide);
            Show(banner, view.Banner == "reconnecting" ? "reconnecting · STALE CLOCK" : view.Banner);
            bannerBackground.color = view.Banner.StartsWith("TEST CONTROL", StringComparison.Ordinal) ? new Color(.55f, .035f, .04f, .98f) : new Color(.04f, .06f, .09f, .98f);
            Show(problem, string.Join(" | ", new[] { view.Problem, extraProblem }.Where(s => !string.IsNullOrEmpty(s))));
            Show(upNext, view.UpNext);
            Show(overlay, view.Overlay);
            if (!string.IsNullOrEmpty(view.Overlay))
            {
                // one line of the block per authored line, above the caption's box
                var lines = view.Overlay.Split('\n').Length; var bottom = vertical ? HudModel.CaptionTopTall + .02f : HudModel.CaptionTopWide + .03f;
                var overlayRect = overlay.transform.parent.GetComponent<RectTransform>();
                overlayRect.anchorMin = new Vector2(.055f, bottom); overlayRect.anchorMax = new Vector2(vertical ? .945f : .62f, bottom + .012f + .034f * lines);
            }
            Show(preview, badge == "OFFLINE PREVIEW" ? "OFFLINE PREVIEW · stand-in values, not platform data" : "");
            var rail = view.Rail.Skip(Math.Max(0, view.Rail.Count - (vertical ? HudModel.RailRowsTall : HudModel.RailRowsWide))).ToArray();
            for (var i = 0; i < nodes.Length; i++)
            {
                if (i >= rail.Length) { Show(nodes[i], ""); continue; }
                var n = rail[i];
                for (var kind = 0; kind < 3; kind++) shapes[i, kind].SetActive(kind == (n.Source == EvidenceSource.Scenario ? 0 : n.Source == EvidenceSource.DeviceSide ? 2 : 1));
                var arrow = i > 0 && view.Links.Contains(rail[i - 1].Id + "->" + n.Id) ? "↓ " : "";
                Show(nodes[i], arrow + n.Label + "\n" + n.Tag + (n.SincePreviousS > 0 ? "  +" + n.SincePreviousS.ToString("0", System.Globalization.CultureInfo.InvariantCulture) + " s" : ""));
                nodes[i].color = n.Source == EvidenceSource.Scenario ? new Color(.70f, .77f, .83f) : Color.white;
                var rect = nodes[i].transform.parent.GetComponent<RectTransform>();
                rect.anchorMin = new Vector2(vertical ? .04f : .735f, HudModel.RailRowTop - HudModel.RailRowHeight - i * HudModel.RailRowStep);
                rect.anchorMax = new Vector2(.985f, HudModel.RailRowTop - i * HudModel.RailRowStep);
            }
            var tagIndex = 0;
            foreach (var tag in worldTags.Take(vertical ? 2 : 4))
            {
                if (!app.Map.Anchors.TryGetValue(tag.Token, out var a)) continue;
                var screen = app.sceneCamera.WorldToViewportPoint(new Vector3((float)a.X, 9, (float)a.Z));
                if (screen.z <= 0 || screen.x < .05f || screen.x > .95f || screen.y < .3f || screen.y > .78f) continue;
                var t = tags[tagIndex++]; Show(t, tag.Text);
                var rect = t.transform.parent.GetComponent<RectTransform>();
                rect.anchorMin = rect.anchorMax = new Vector2(screen.x, screen.y); rect.sizeDelta = new Vector2(HudModel.TagWidth(tag.Text), HudModel.TagBoxHeight);
            }
            for (; tagIndex < tags.Length; tagIndex++) Show(tags[tagIndex], "");
        }
        public void Dispose() => MeshKit.Release(root);
    }
}
