// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.Tasks;
using UnityEngine;
using UnityEngine.Rendering;
using UnityEngine.UI;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>The data layer's look, shared by the elements added to the overlay's canvas (the cards keep their own copy).</summary>
    static class OverlayStyle
    {
        public static readonly Color Panel = new Color(0.06f, 0.08f, 0.10f, 0.96f);
        public static readonly Color Accent = new Color(0.36f, 0.86f, 0.96f, 1f);
        public static readonly Color Ok = new Color(0.36f, 0.86f, 0.46f, 1f);
        public static readonly Color Warn = new Color(1f, 0.70f, 0.12f, 1f);
        public static readonly Color Ink = new Color(0.93f, 0.95f, 0.96f, 1f);
        public static readonly Color Muted = new Color(0.60f, 0.68f, 0.72f, 1f);
        public static readonly Color Dim = new Color(0.62f, 0.66f, 0.66f, 1f);
        public static readonly Color Grey = new Color(0.40f, 0.44f, 0.46f, 1f);
        public static readonly Color Violet = new Color(0.74f, 0.62f, 1f, 1f);

        public const float RefH = 1080f, Margin = 18f;

        public static Color Of(RowTone tone) => tone == RowTone.Dim ? Dim : tone == RowTone.Ink ? Ink : tone == RowTone.Warn ? Warn : Grey;

        public static Color Of(ProofSource source) => source == ProofSource.Platform ? Accent : source == ProofSource.Device ? Ok : Violet;

        /// <summary>The smooth ease of a presence that goes from 0 to 1.</summary>
        public static float Ease(float t) => t * t * (3f - 2f * t);
    }

    /// <summary>A box that fades in and out, the calm way the cards do: it appears over <see cref="CardTiming.FadeSeconds"/> and goes the same way.</summary>
    abstract class FadeBox
    {
        protected RectTransform root;
        protected CanvasGroup group;
        float presence;

        /// <summary>0 hidden, 1 shown.</summary>
        protected float Presence => presence;

        protected bool Built => root != null;

        protected void Fade(bool wanted, float dt, bool reduced, bool snap)
        {
            if (snap) presence = wanted ? 1f : 0f;
            else presence = Mathf.MoveTowards(presence, wanted ? 1f : 0f, dt / (reduced ? CardTiming.ReducedFadeSeconds : CardTiming.FadeSeconds));
            if (root == null) return;
            var on = presence > 0f;
            if (root.gameObject.activeSelf != on) root.gameObject.SetActive(on);
            if (group != null) group.alpha = presence;
        }

        protected void MakeRoot(RectTransform canvas, string name)
        {
            root = IotOverlay.Image(canvas, name, Vector2.zero, new Vector2(10f, 10f), OverlayStyle.Panel);
            group = root.gameObject.AddComponent<CanvasGroup>();
            group.interactable = false;
            group.blocksRaycasts = false;
            group.alpha = 0f;
            root.gameObject.SetActive(false);
        }
    }

    /// <summary>
    /// The proof drawer: the selected machine's evidence chain, one row for each thing that happened, with its time (UTC) and who says so
    /// (the platform, the device, the presenter's hand). It draws what <see cref="IProofRows"/> holds and nothing else: a row that is not
    /// there is not drawn, and a machine with no rows says so. At the right edge of a landscape frame, centred under the
    /// subject in a portrait one, or large and central (<see cref="DrawerMode.Full"/>).
    /// </summary>
    sealed class ProofDrawerView : FadeBox
    {
        public const float W = DrawerLayout.W, RowH = DrawerLayout.RowH, HeadH = DrawerLayout.HeadH, Pad = DrawerLayout.Pad;
        public const int MaxRows = DrawerLayout.MaxRows;
        const int MaxChars = 60;
        const float TimeW = 134f;

        sealed class Line
        {
            public Text time, text, source;
        }

        readonly Line[] lines = new Line[MaxRows];
        readonly List<ProofRow> rows = new List<ProofRow>();
        readonly Dictionary<string, float> born = new Dictionary<string, float>(StringComparer.Ordinal);
        Text title, sub, empty;
        RectTransform edge;
        string shownDevice;
        int shownVersion = -1;
        float clock, shownH, heightVelocity;
        bool placed;

        void Build(RectTransform canvas)
        {
            MakeRoot(canvas, "Proof Drawer");
            edge = IotOverlay.Image(root, "Edge", Vector2.zero, new Vector2(4f, 10f), OverlayStyle.Accent);
            title = IotOverlay.Label(root, "Title", Vector2.zero, new Vector2(W - 2 * Pad, 26f), 20, FontStyle.Bold, OverlayStyle.Accent);
            sub = IotOverlay.Label(root, "Sub", Vector2.zero, new Vector2(W - 2 * Pad, 20f), 14, FontStyle.Normal, OverlayStyle.Muted);
            empty = IotOverlay.Label(root, "Empty", Vector2.zero, new Vector2(W - 2 * Pad, 22f), 15, FontStyle.Italic, OverlayStyle.Muted);
            for (var i = 0; i < MaxRows; i++)
            {
                var l = new Line
                {
                    time = IotOverlay.Label(root, "Time" + i, Vector2.zero, new Vector2(TimeW, 22f), 15, FontStyle.Normal, OverlayStyle.Muted),
                    text = IotOverlay.Label(root, "Text" + i, Vector2.zero, new Vector2(W - 2 * Pad - TimeW - 92f, 22f), 15, FontStyle.Normal, OverlayStyle.Ink),
                    source = IotOverlay.Label(root, "Source" + i, Vector2.zero, new Vector2(88f, 22f), 13, FontStyle.Bold, OverlayStyle.Accent),
                };
                l.time.font = IotOverlay.CardMono;
                l.source.alignment = TextAnchor.UpperRight;
                lines[i] = l;
            }
        }

        /// <summary>One frame. Returns the drawer's rectangle on screen (layout units) while it is visible, or a rectangle of no size.</summary>
        public Rect Update(RectTransform canvas, DrawerMode mode, string device, IProofRows source, float refW, float dt, bool reduced, bool snap)
        {
            var wanted = mode != DrawerMode.Off && device != null && source != null;
            if (wanted && !Built) Build(canvas);
            clock += dt;
            Fade(wanted, dt, reduced, snap);
            if (!Built || Presence <= 0f)
            {
                placed = false;
                return default;
            }

            if (wanted && (device != shownDevice || source.Version != shownVersion))
            {
                if (device != shownDevice)
                {
                    born.Clear();
                    placed = false;
                }
                shownDevice = device;
                shownVersion = source.Version;
                rows.Clear();
                source.Rows(device, rows, MaxRows);
                Fill(device);
            }

            var n = rows.Count;
            // the box's height glides to the rows it holds, its top edge pinned (DrawerLayout), so a row more moves nothing already drawn
            var target = DrawerLayout.HeightFor(n);
            var h = placed ? CalmGlide.Step(shownH, target, ref heightVelocity, dt, reduced, snap) : target;
            placed = true;
            shownH = h;
            var rect = DrawerLayout.Place(mode, refW, h);
            root.anchoredPosition = rect.position;
            root.sizeDelta = new Vector2(W, h);
            root.localScale = Vector3.one * DrawerLayout.ScaleFor(mode, refW);
            edge.sizeDelta = new Vector2(4f, h);
            Layout(n, h, snap);
            return rect;
        }

        void Fill(string device)
        {
            title.text = "PROOF · " + device;
            sub.text = "what happened to this machine, and who says so · times UTC";
            for (var i = 0; i < MaxRows; i++)
            {
                var on = i < rows.Count;
                lines[i].time.enabled = lines[i].text.enabled = lines[i].source.enabled = on;
                if (!on) continue;
                var r = rows[i];
                lines[i].time.text = (r.Seen ? "seen " : "") + PanelModel.Clock(r.At);
                var text = r.Text.Length > MaxChars ? r.Text.Substring(0, MaxChars - 1) + "…" : r.Text;
                lines[i].text.text = text;
                lines[i].source.text = r.SourceLabel;
                lines[i].source.color = OverlayStyle.Of(r.Source);
                var key = r.At.UtcTicks + "|" + r.Source + "|" + r.Text;
                if (!born.ContainsKey(key)) born[key] = clock;
            }

            empty.text = "nothing has happened to this machine yet";
        }

        void Layout(int n, float h, bool snap)
        {
            title.rectTransform.anchoredPosition = new Vector2(Pad + 4f, h - Pad - 24f);
            sub.rectTransform.anchoredPosition = new Vector2(Pad + 4f, h - Pad - 44f);
            empty.enabled = n == 0;
            empty.rectTransform.anchoredPosition = new Vector2(Pad + 4f, h - HeadH - Pad - 22f);
            for (var i = 0; i < n; i++)
            {
                var y = h - HeadH - Pad - (i + 1) * RowH + 2f;
                var l = lines[i];
                l.time.rectTransform.anchoredPosition = new Vector2(Pad + 4f, y);
                l.text.rectTransform.anchoredPosition = new Vector2(Pad + 4f + TimeW + 4f, y);
                l.source.rectTransform.anchoredPosition = new Vector2(W - Pad - 88f, y);
                var key = rows[i].At.UtcTicks + "|" + rows[i].Source + "|" + rows[i].Text;
                var a = snap || !born.TryGetValue(key, out var b) ? 1f : Mathf.Clamp01((clock - b) / CardTiming.FadeSeconds);
                SetAlpha(l.time, a);
                SetAlpha(l.text, a);
                SetAlpha(l.source, a);
            }
        }

        static void SetAlpha(Text t, float a)
        {
            var c = t.color;
            if (Mathf.Approximately(c.a, a)) return;
            c.a = a;
            t.color = c;
        }
    }

    /// <summary>The selected machine's panel: every metric of its profile, its unit, and when it was last observed, inked by freshness.</summary>
    sealed class MachinePanelView : FadeBox
    {
        public const float W = 336f, HeadH = 62f, RowH = 46f, FootH = 32f, Pad = 14f;
        public const int MaxRows = 5;

        sealed class Line
        {
            public Text label, value, time;
        }

        readonly Line[] lines = new Line[MaxRows];
        Text title, kind, footer;
        RectTransform edge;

        void Build(RectTransform canvas)
        {
            MakeRoot(canvas, "Machine Panel");
            edge = IotOverlay.Image(root, "Edge", Vector2.zero, new Vector2(4f, 10f), OverlayStyle.Accent);
            title = IotOverlay.Label(root, "Title", Vector2.zero, new Vector2(W - 2 * Pad, 26f), 22, FontStyle.Bold, OverlayStyle.Accent);
            kind = IotOverlay.Label(root, "Kind", Vector2.zero, new Vector2(W - 2 * Pad, 20f), 15, FontStyle.Normal, OverlayStyle.Muted);
            footer = IotOverlay.Label(root, "Footer", Vector2.zero, new Vector2(W - 2 * Pad, 20f), 14, FontStyle.Normal, OverlayStyle.Muted);
            for (var i = 0; i < MaxRows; i++)
            {
                var l = new Line
                {
                    label = IotOverlay.Label(root, "Label" + i, Vector2.zero, new Vector2(150f, 22f), 16, FontStyle.Normal, OverlayStyle.Muted),
                    value = IotOverlay.Label(root, "Value" + i, Vector2.zero, new Vector2(W - 2 * Pad - 150f, 24f), 21, FontStyle.Normal, OverlayStyle.Ink),
                    time = IotOverlay.Label(root, "Time" + i, Vector2.zero, new Vector2(W - 2 * Pad, 18f), 13, FontStyle.Normal, OverlayStyle.Muted),
                };
                l.value.font = IotOverlay.CardMono;
                l.value.alignment = TextAnchor.UpperRight;
                l.time.font = IotOverlay.CardMono;
                lines[i] = l;
            }
        }

        /// <summary>One frame. <paramref name="view"/> null hides the panel. Returns its rectangle while it is visible.</summary>
        public Rect Update(RectTransform canvas, PanelView view, float refW, float dt, bool reduced, bool snap)
        {
            var wanted = view != null;
            if (wanted && !Built) Build(canvas);
            Fade(wanted, dt, reduced, snap);
            if (!Built || Presence <= 0f) return default;
            if (wanted) Fill(view);

            var n = Math.Min(MaxRows, view != null ? view.Rows.Count : lines.Length);
            var h = HeadH + n * RowH + FootH;
            var portrait = refW < OverlayStyle.RefH;
            var scale = Mathf.Min(1f, (refW - 2f * OverlayStyle.Margin) / W);
            var size = new Vector2(W, h) * scale;
            // it slides in from the edge it sits against while it fades in
            var slide = (1f - OverlayStyle.Ease(Presence)) * 40f;
            Vector2 at = portrait
                ? new Vector2((refW - size.x) / 2f, OverlayStyle.RefH * 0.20f + OverlayStyle.Margin)
                : new Vector2(OverlayStyle.Margin - slide, (OverlayStyle.RefH - size.y) / 2f);
            root.anchoredPosition = at;
            root.sizeDelta = new Vector2(W, h);
            root.localScale = Vector3.one * scale;
            edge.sizeDelta = new Vector2(4f, h);
            return new Rect(new Vector2(OverlayStyle.Margin, at.y), size);
        }

        void Fill(PanelView v)
        {
            var n = Math.Min(MaxRows, v.Rows.Count);
            var h = HeadH + n * RowH + FootH;
            title.text = v.Title;
            kind.text = v.Kind;
            title.rectTransform.anchoredPosition = new Vector2(Pad + 4f, h - Pad - 24f);
            kind.rectTransform.anchoredPosition = new Vector2(Pad + 4f, h - Pad - 46f);
            footer.text = v.Footer;
            footer.rectTransform.anchoredPosition = new Vector2(Pad + 4f, 8f);
            for (var i = 0; i < MaxRows; i++)
            {
                var on = i < n;
                lines[i].label.enabled = lines[i].value.enabled = lines[i].time.enabled = on;
                if (!on) continue;
                var r = v.Rows[i];
                var y = h - HeadH - (i + 1) * RowH;
                lines[i].label.text = r.Label;
                lines[i].value.text = r.Value;
                lines[i].value.color = OverlayStyle.Of(r.Tone);
                lines[i].time.text = r.ObservedAt.Length > 0 ? "observed " + r.ObservedAt : "";
                lines[i].label.rectTransform.anchoredPosition = new Vector2(Pad + 4f, y + 14f);
                lines[i].value.rectTransform.anchoredPosition = new Vector2(Pad + 150f, y + 12f);
                lines[i].time.rectTransform.anchoredPosition = new Vector2(Pad + 4f, y - 4f);
            }
        }
    }

    /// <summary>
    /// Small print drawn into a render's frames while its time holds, each fading in and out: stacked up from the lower left of a landscape frame, and
    /// down from the top left, under the platform's own caption zone, of a portrait one. Nothing but a render's shot file gives it a chip.
    /// </summary>
    sealed class ChipsView
    {
        const float Gap = 8f;

        sealed class Chip
        {
            public RectTransform box;
            public Text text;
            public CanvasGroup group;
            public Vector2 shown, velocity;
            public bool placed;
        }

        readonly List<Chip> chips = new List<Chip>();
        readonly List<Rect> rects = new List<Rect>();
        readonly List<Vector2> sizes = new List<Vector2>();
        readonly List<bool> onFlags = new List<bool>();
        readonly List<float> alphas = new List<float>();
        readonly List<Vector2> targets = new List<Vector2>();

        public IReadOnlyList<Rect> Rects => rects;

        /// <summary>One frame at <paramref name="time"/> seconds into the shot; fills <see cref="Rects"/> with what is on screen.</summary>
        public void Update(RectTransform canvas, IReadOnlyList<ChipView> wanted, double time, float refW, float safeTop, bool snap, float dt = 0f, bool reduced = false)
        {
            rects.Clear();
            var count = wanted == null ? 0 : wanted.Count;
            while (chips.Count < count) chips.Add(Make(canvas, chips.Count));
            var portrait = refW < OverlayStyle.RefH;
            var scale = portrait ? 0.75f : 1f;
            var y0 = portrait ? OverlayStyle.RefH * (1f - Mathf.Max(safeTop, 0.14f)) - OverlayStyle.Margin : OverlayStyle.Margin * 2f;
            var x = portrait ? OverlayStyle.Margin : OverlayStyle.Margin * 2f;
            sizes.Clear();
            onFlags.Clear();
            alphas.Clear();
            for (var i = 0; i < chips.Count; i++)
            {
                var c = chips[i];
                var a = 0f;
                if (i < count)
                {
                    var w = wanted[i];
                    var rise = (float)Math.Min(1.0, Math.Max(0.0, (time - w.From) / CardTiming.FadeSeconds));
                    var fall = (float)Math.Min(1.0, Math.Max(0.0, (w.Until - time) / CardTiming.FadeSeconds));
                    a = snap ? (time >= w.From && time < w.Until ? 1f : 0f) : Math.Min(rise, fall);
                    if (c.text.text != w.Text)
                    {
                        c.text.text = w.Text;
                        c.box.sizeDelta = new Vector2(c.text.preferredWidth + 36f, 40f);
                    }
                }

                alphas.Add(a);
                onFlags.Add(a > 0f);
                sizes.Add(c.box.sizeDelta * scale);
            }

            // where each chip stands in the stack; one that is already showing glides there as the cards do (a chip arriving or leaving
            // above it does not make it jump), one that is just appearing starts there
            ChipStack.Place(sizes, onFlags, portrait, x, y0, Gap, targets);
            for (var i = 0; i < chips.Count; i++)
            {
                var c = chips[i];
                var on = onFlags[i];
                if (c.box.gameObject.activeSelf != on) c.box.gameObject.SetActive(on);
                if (!on)
                {
                    c.placed = false;
                    continue;
                }

                c.group.alpha = alphas[i];
                c.shown = c.placed ? CalmGlide.Step(c.shown, targets[i], ref c.velocity, dt, reduced, snap) : targets[i];
                c.placed = true;
                c.box.anchoredPosition = c.shown;
                c.box.localScale = Vector3.one * scale;
                rects.Add(new Rect(c.shown.x, c.shown.y, sizes[i].x, sizes[i].y));
            }
        }

        static Chip Make(RectTransform canvas, int index)
        {
            var c = new Chip { box = IotOverlay.Image(canvas, "Chip " + index, Vector2.zero, new Vector2(200f, 40f), OverlayStyle.Panel) };
            IotOverlay.Image(c.box, "Edge", Vector2.zero, new Vector2(4f, 40f), OverlayStyle.Accent);
            c.text = IotOverlay.Label(c.box, "Text", new Vector2(18f, 8f), new Vector2(400f, 26f), 22, FontStyle.Bold, OverlayStyle.Ink);
            c.group = c.box.gameObject.AddComponent<CanvasGroup>();
            c.group.interactable = false;
            c.group.blocksRaycasts = false;
            c.group.alpha = 0f;
            c.box.gameObject.SetActive(false);
            return c;
        }
    }

    /// <summary>
    /// The route highlight: the route a machine is driving, drawn on the terrain as a dashed line that draws itself on from the machine's start
    /// and fades out (over <see cref="FadeOutSeconds"/>) when the machine stops driving it. It draws what the route source gives it, so a live scene
    /// and a replay of it draw the same line.
    /// </summary>
    sealed class RouteHighlight
    {
        public const float DrawOnSeconds = 0.9f, FadeOutSeconds = 1.8f, Lift = 0.45f;

        sealed class Trail
        {
            public LineRenderer line;
            public RoutePolyline route;
            public float drawn, alpha;
        }

        readonly Dictionary<string, Trail> trails = new Dictionary<string, Trail>(StringComparer.Ordinal);
        readonly List<Vector3> scratch = new List<Vector3>();

        /// <summary>The machines whose route is drawn (or fading) now.</summary>
        public int Active
        {
            get
            {
                var n = 0;
                foreach (var t in trails.Values)
                    if (t.alpha > 0f) n++;
                return n;
            }
        }

        public void Update(Transform parent, Material material, QuarryTerrain terrain, Camera cam, IEnumerable<string> machines, Func<string, RoutePolyline> routeOf, bool enabled, float dt, bool snap)
        {
            foreach (var id in machines)
            {
                var want = enabled && routeOf != null ? routeOf(id) : null;
                trails.TryGetValue(id, out var t);
                if (t == null && want == null) continue;
                if (t == null) trails[id] = t = new Trail();
                if (want != null && !ReferenceEquals(want, t.route))
                {
                    t.route = want;
                    t.drawn = snap ? 1f : 0f;
                }

                if (t.line == null && t.route != null) t.line = NewLine(parent, material, id);
                if (want != null)
                {
                    t.drawn = Mathf.Min(1f, t.drawn + (snap ? 1f : dt / DrawOnSeconds));
                    t.alpha = snap ? 1f : Mathf.Min(1f, t.alpha + dt / CardTiming.FadeSeconds);
                }
                else t.alpha = snap ? 0f : Mathf.Max(0f, t.alpha - dt / FadeOutSeconds);

                if (t.line == null) continue;
                var on = t.alpha > 0f && t.route != null;
                if (t.line.gameObject.activeSelf != on) t.line.gameObject.SetActive(on);
                if (!on) continue;
                Shape(t, terrain);
                var c = OverlayStyle.Ok;
                c.a = t.alpha;
                t.line.startColor = t.line.endColor = c;
                var dist = Vector3.Distance(cam.transform.position, t.line.GetPosition(0));
                t.line.widthMultiplier = Mathf.Clamp(dist * 0.004f, 0.5f, 2.2f);
                t.line.textureScale = new Vector2(1f / (t.line.widthMultiplier * 6f), 1f);
            }
        }

        void Shape(Trail t, QuarryTerrain terrain)
        {
            var r = t.route;
            var upTo = r.Length * Mathf.Clamp01(t.drawn);
            scratch.Clear();
            for (var i = 0; i < r.Count; i++)
            {
                if (r.At(i) <= upTo) scratch.Add(Point(r.X(i), r.Z(i), terrain));
                else
                {
                    if (i > 0)
                    {
                        var span = r.At(i) - r.At(i - 1);
                        var f = span > 1e-4f ? (upTo - r.At(i - 1)) / span : 0f;
                        scratch.Add(Point(Mathf.Lerp(r.X(i - 1), r.X(i), f), Mathf.Lerp(r.Z(i - 1), r.Z(i), f), terrain));
                    }

                    break;
                }
            }

            if (scratch.Count < 2 && r.Count >= 2) scratch.Add(Point(r.X(0) + 0.01f, r.Z(0), terrain));
            t.line.positionCount = scratch.Count;
            t.line.SetPositions(scratch.ToArray());
        }

        static Vector3 Point(float x, float z, QuarryTerrain terrain) => new Vector3(x, terrain.HeightAt(x, z) + Lift, z);

        static LineRenderer NewLine(Transform parent, Material material, string id)
        {
            var go = new GameObject("Route " + id) { hideFlags = HideFlags.DontSave };
            go.transform.SetParent(parent, false);
            var line = go.AddComponent<LineRenderer>();
            line.sharedMaterial = material;
            line.useWorldSpace = true;
            line.textureMode = LineTextureMode.Tile;
            line.alignment = LineAlignment.View;
            line.shadowCastingMode = ShadowCastingMode.Off;
            line.receiveShadows = false;
            line.numCornerVertices = 2;
            return line;
        }

        public void Clear()
        {
            foreach (var kv in trails)
                if (kv.Value.line != null) UnityEngine.Object.DestroyImmediate(kv.Value.line.gameObject);
            trails.Clear();
        }
    }
}
