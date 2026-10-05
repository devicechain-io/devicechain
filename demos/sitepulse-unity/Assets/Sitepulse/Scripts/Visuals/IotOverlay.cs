// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using UnityEngine;
using UnityEngine.Rendering;
using UnityEngine.UI;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>
    /// The scene's data layer: what a DeviceChain console would show over the site. A tag above
    /// each haul truck (payload, fuel, speed and a status dot), a pulsing ring under a machine
    /// with an active alert, a dashed geofence round the rim of the cut, and a callout at the
    /// load point with the haul cycle time and the truck being loaded. One overlay, switched on
    /// and off as a whole (<see cref="show"/>), so the same scene gives clean stills.
    ///
    /// Until the scene is connected to a DeviceChain instance the values are illustrative: they
    /// follow the preview choreography (whether a truck is loaded, how fast it moves, which truck
    /// stands at the load point) and are not platform data.
    /// </summary>
    [ExecuteAlways]
    public sealed class IotOverlay : MonoBehaviour, IQuarryEffect
    {
        public QuarryFleetPreview fleet;
        public QuarryTerrain terrain;
        [Tooltip("The generated feature file (its geofence and the load point).")]
        public TextAsset features;
        [Tooltip("The processing plant (its Hopper node carries the crusher's tag).")]
        public Transform plant;
        [Tooltip("Show the overlay.")]
        public bool show = true;
        [Tooltip("The machines that carry a tag.")]
        public string[] tagged = { "SP-HL-0001", "SP-HL-0003", "SP-HL-0004", "SP-LD-0003" };
        [Tooltip("The machine with an active alert, and its message.")]
        public string alertMachine = "SP-HL-0004";
        public string alertText = "Tyre pressure low";
        [Tooltip("Tag height as a share of the screen's height.")]
        [Range(0.03f, 0.2f)] public float tagScreenHeight = 0.065f;
        public Material lineMaterial, ringMaterial;

        static readonly Color Panel = new Color(0.07f, 0.09f, 0.11f, 0.86f);
        static readonly Color Accent = new Color(0.36f, 0.86f, 0.96f, 1f);
        static readonly Color Ok = new Color(0.36f, 0.86f, 0.46f, 1f);
        static readonly Color Warn = new Color(1f, 0.70f, 0.12f, 1f);
        const float TagW = 430f, TagH = 104f, Leader = 70f;

        [Serializable] sealed class Spot { public float x, y, z; }
        [Serializable] sealed class Geofence { public string label; public Spot[] points; }
        [Serializable] sealed class FeatureFile { public Geofence geofence; }

        sealed class Tag
        {
            public GameObject go;
            public RectTransform rect, card, leader;
            public float height;
            public Text title, body;
            public RawImage dot;
            public MachineRig rig;
            public Vector3 last;
            public bool hasLast;
            public float speed;
            public Func<Vector3> anchor;
        }

        readonly List<Tag> tags = new List<Tag>();
        GameObject root;
        LineRenderer fenceLine;
        Tag loadTag, fenceTag, plantTag;
        MeshRenderer ring;
        Vector3 loadPoint, fenceCentre;
        float clock;
        static Texture2D dotTexture;
        static Font font;

        void OnEnable()
        {
            QuarryEffects.Active.Add(this);
        }

        void OnDisable()
        {
            QuarryEffects.Active.Remove(this);
            Clear();
        }

        void Clear()
        {
            tags.Clear();
            loadTag = fenceTag = plantTag = null;
            if (root != null) DestroyImmediate(root);
            root = null;
        }

        bool Ready => fleet != null && terrain != null && terrain.Built && fleet.Count > 0;

        void Build()
        {
            Clear();
            if (!Ready || features == null) return;
            root = new GameObject("IoT Overlay (generated)") { hideFlags = HideFlags.DontSave };
            root.transform.SetParent(transform, false);
            font = font != null ? font : Resources.GetBuiltinResource<Font>("LegacyRuntime.ttf");
            dotTexture = dotTexture != null ? dotTexture : Disc(32);

            var f = Parse(features.text, out var fence);
            loadPoint = new Vector3(f.x, f.y, f.z);
            foreach (var rig in fleet.Machines)
            {
                if (Array.IndexOf(tagged, rig.name) < 0) continue;
                var r = rig;
                var t = MakeTag(rig.name, () => Top(r));
                t.rig = rig;
                tags.Add(t);
            }
            loadTag = MakeTag("Load point", () => loadPoint + Vector3.up * 7.5f);
            tags.Add(loadTag);
            var hopper = plant != null ? FleetRig.Find(plant, "Hopper") : null;
            if (hopper != null)
            {
                plantTag = MakeTag("Primary crusher", () => hopper.position + Vector3.up * 5.5f);
                tags.Add(plantTag);
            }

            if (fence.Count > 2 && lineMaterial != null)
            {
                var go = new GameObject("Geofence") { hideFlags = HideFlags.DontSave };
                go.transform.SetParent(root.transform, false);
                fenceLine = go.AddComponent<LineRenderer>();
                fenceLine.sharedMaterial = lineMaterial;
                fenceLine.loop = true;
                fenceLine.useWorldSpace = true;
                fenceLine.textureMode = LineTextureMode.Tile;
                fenceLine.alignment = LineAlignment.View;
                fenceLine.shadowCastingMode = ShadowCastingMode.Off;
                fenceLine.receiveShadows = false;
                fenceLine.numCornerVertices = 2;
                var pts = new Vector3[fence.Count];
                fenceCentre = Vector3.zero;
                for (int i = 0; i < fence.Count; i++)
                {
                    pts[i] = fence[i] + Vector3.up * 0.6f;
                    fenceCentre += fence[i] / fence.Count;
                }
                fenceLine.positionCount = pts.Length;
                fenceLine.SetPositions(pts);
                fenceLine.startColor = fenceLine.endColor = Accent;
                // the label sits on the fence's southern-most point, where the ramp leaves the cut
                int south = 0;
                for (int i = 1; i < fence.Count; i++)
                    if (fence[i].z < fence[south].z) south = i;
                var at = fence[south];
                fenceTag = MakeTag("Geofence · Cut", () => at + Vector3.up * 1.5f, small: true);
                fenceTag.body.text = "Zone sp-zone-cut · 4 machines inside";
                tags.Add(fenceTag);
            }
            if (ringMaterial != null)
            {
                var go = new GameObject("Alert Ring") { hideFlags = HideFlags.DontSave };
                go.transform.SetParent(root.transform, false);
                go.AddComponent<MeshFilter>().sharedMesh = RingMesh();
                ring = go.AddComponent<MeshRenderer>();
                ring.sharedMaterial = ringMaterial;
                ring.shadowCastingMode = ShadowCastingMode.Off;
                ring.receiveShadows = false;
            }
            Refresh(Camera.main);
        }

        static Vector3 Top(MachineRig rig)
        {
            var p = rig.transform.position;
            float h = rig.Kind == MachineKind.Hauler ? 6.2f : 6.0f;
            return p + Vector3.up * h;
        }

        Tag MakeTag(string title, Func<Vector3> anchor, bool small = false)
        {
            var go = new GameObject(title) { hideFlags = HideFlags.DontSave };
            go.transform.SetParent(root.transform, false);
            var canvas = go.AddComponent<Canvas>();
            canvas.renderMode = RenderMode.WorldSpace;
            var rect = (RectTransform)go.transform;
            float h = small ? TagH * 0.62f : TagH;
            rect.sizeDelta = new Vector2(TagW, h + Leader);
            rect.pivot = new Vector2(0.5f, 0f);

            // the leader runs from the anchor up to the card; the card can be lifted clear of
            // another tag's card by lengthening the leader
            var leader = Image(go.transform, "Leader", new Vector2(TagW / 2 - 1.5f, 0f), new Vector2(3f, Leader), Accent);
            var card = new GameObject("Card", typeof(RectTransform)).GetComponent<RectTransform>();
            card.SetParent(go.transform, false);
            Place(card, new Vector2(0f, Leader), new Vector2(TagW, h));
            Image(card, "Panel", Vector2.zero, new Vector2(TagW, h), Panel);
            Image(card, "Edge", Vector2.zero, new Vector2(6f, h), Accent);
            var dot = new GameObject("Dot", typeof(RectTransform)).AddComponent<RawImage>();
            dot.transform.SetParent(card, false);
            Place(dot.rectTransform, new Vector2(TagW - 34f, h - 34f), new Vector2(20f, 20f));
            dot.texture = dotTexture;
            dot.color = Ok;
            dot.raycastTarget = false;
            dot.gameObject.SetActive(!small);
            var tt = Label(card, "Title", new Vector2(20f, h - 44f), new Vector2(TagW - 70f, 38f), small ? 24 : 28, FontStyle.Bold, Accent);
            tt.text = title;
            var body = Label(card, "Body", new Vector2(20f, 8f), new Vector2(TagW - 30f, h - 52f), small ? 21 : 22, FontStyle.Normal, new Color(0.92f, 0.94f, 0.95f));
            return new Tag { go = go, rect = rect, card = card, leader = leader, height = h, title = tt, body = body, dot = dot, anchor = anchor };
        }

        static void Place(RectTransform r, Vector2 min, Vector2 size)
        {
            r.anchorMin = r.anchorMax = Vector2.zero;
            r.pivot = Vector2.zero;
            r.anchoredPosition = min;
            r.sizeDelta = size;
        }

        static RectTransform Image(Transform parent, string name, Vector2 min, Vector2 size, Color color)
        {
            var img = new GameObject(name, typeof(RectTransform)).AddComponent<Image>();
            img.transform.SetParent(parent, false);
            Place(img.rectTransform, min, size);
            img.color = color;
            img.raycastTarget = false;
            return img.rectTransform;
        }

        static Text Label(Transform parent, string name, Vector2 min, Vector2 size, int fontSize, FontStyle style, Color color)
        {
            var t = new GameObject(name, typeof(RectTransform)).AddComponent<Text>();
            t.transform.SetParent(parent, false);
            Place(t.rectTransform, min, size);
            t.font = font;
            t.fontSize = fontSize;
            t.fontStyle = style;
            t.color = color;
            t.alignment = TextAnchor.UpperLeft;
            t.horizontalOverflow = HorizontalWrapMode.Overflow;
            t.verticalOverflow = VerticalWrapMode.Overflow;
            t.raycastTarget = false;
            return t;
        }

        static Texture2D Disc(int n)
        {
            var tex = new Texture2D(n, n, TextureFormat.RGBA32, false) { hideFlags = HideFlags.DontSave, wrapMode = TextureWrapMode.Clamp };
            var px = new Color32[n * n];
            for (int y = 0; y < n; y++)
                for (int x = 0; x < n; x++)
                {
                    float d = Vector2.Distance(new Vector2(x + 0.5f, y + 0.5f), new Vector2(n / 2f, n / 2f));
                    px[y * n + x] = new Color32(255, 255, 255, (byte)(255 * Mathf.Clamp01(n / 2f - 1f - d + 0.5f)));
                }
            tex.SetPixels32(px);
            tex.Apply();
            return tex;
        }

        static Mesh ringMesh;

        static Mesh RingMesh()
        {
            if (ringMesh != null) return ringMesh;
            const int n = 48;
            var v = new List<Vector3>();
            var uv = new List<Vector2>();
            var tri = new List<int>();
            for (int i = 0; i <= n; i++)
            {
                float a = i * Mathf.PI * 2f / n;
                var d = new Vector3(Mathf.Sin(a), 0f, Mathf.Cos(a));
                v.Add(d * 0.86f);
                v.Add(d * 1f);
                uv.Add(new Vector2(i / (float)n, 0f));
                uv.Add(new Vector2(i / (float)n, 1f));
                if (i < n)
                {
                    int k = i * 2;
                    tri.AddRange(new[] { k, k + 1, k + 2, k + 2, k + 1, k + 3 });
                }
            }
            ringMesh = new Mesh { name = "AlertRing", hideFlags = HideFlags.DontSave };
            ringMesh.SetVertices(v);
            ringMesh.SetUVs(0, uv);
            ringMesh.SetTriangles(tri, 0);
            ringMesh.RecalculateNormals();
            ringMesh.RecalculateBounds();
            return ringMesh;
        }

        /// <summary>The load point and the geofence from the feature file.</summary>
        static Vector3 Parse(string json, out List<Vector3> fence)
        {
            fence = new List<Vector3>();
            var f = JsonUtility.FromJson<FeatureFile>(json);
            if (f.geofence != null && f.geofence.points != null)
                foreach (var p in f.geofence.points) fence.Add(new Vector3(p.x, p.y, p.z));
            // the spots are keyed by name ("load-point"), which JsonUtility cannot map to a field
            var m = System.Text.RegularExpressions.Regex.Match(json, "\"load-point\":(\\{[^}]*\\})");
            var lp = m.Success ? JsonUtility.FromJson<Spot>(m.Groups[1].Value) : new Spot();
            return new Vector3(lp.x, lp.y, lp.z);
        }

        public void ClearParticles()
        {
            foreach (var t in tags) t.hasLast = false;
        }

        void Update()
        {
            if (Application.isPlaying) Step(Time.deltaTime, false);
        }

        void LateUpdate()
        {
            if (root != null) Refresh(Camera.main);
        }

        public void Step(float dt, bool simulate)
        {
            if (root == null && show && Ready) Build();
            if (root == null) return;
            root.SetActive(show);
            clock += dt;
            foreach (var t in tags)
            {
                if (t.rig == null) continue;
                var p = t.rig.transform.position;
                if (t.hasLast && dt > 0f)
                {
                    float inst = new Vector2(p.x - t.last.x, p.z - t.last.z).magnitude / dt;
                    if (inst < 30f) t.speed = Mathf.Lerp(t.speed, inst, 1f - Mathf.Exp(-dt * 4f));
                }
                t.last = p;
                t.hasLast = true;
            }
            UpdateText();
        }

        /// <summary>A wheel loader's bucket load (t), and the loader that feeds the crusher.</summary>
        const float LoaderBucket = 6.2f;
        const string plantFeeder = "SP-LD-0003";

        static int Hash(string s)
        {
            int h = 17;
            foreach (char c in s) h = h * 31 + c;
            return Mathf.Abs(h);
        }

        void UpdateText()
        {
            MachineRig atLoad = null;
            foreach (var t in tags)
            {
                if (t.rig == null) continue;
                var rig = t.rig;
                int h = Hash(rig.name);
                bool alert = rig.name == alertMachine;
                string payload = rig.loaded ? $"{86 + h % 9} t" : "Empty";
                int fuel = 38 + h % 50;
                int kmh = Mathf.RoundToInt(t.speed * 3.6f);
                if (rig.Kind == MachineKind.Loader)
                {
                    bool tipping = rig.boom < -35f && rig.bucket > 25f;
                    string job = tipping ? "Feeding the crusher" : kmh >= 1 ? "Carrying" : "Loading the bucket";
                    t.body.text = $"Bucket {LoaderBucket:0.0} t  ·  Fuel {fuel}%  ·  {kmh} km/h\n" + job;
                    t.body.color = new Color(0.92f, 0.94f, 0.95f);
                    t.dot.color = Ok;
                    continue;
                }
                bool atLoadPoint = Vector3.Distance(rig.transform.position, loadPoint) < 9f && t.speed < 0.3f;
                string state = rig.dump > 2f ? "Tipping at the dump"
                    : rig.loaded ? (kmh < 1 ? "Loaded" : "Hauling to the dump")
                    : atLoadPoint ? "Loading" : kmh < 1 ? "Waiting" : "Returning to the cut";
                t.body.text = $"{payload}  ·  Fuel {fuel}%  ·  {kmh} km/h\n" + (alert ? alertText : state);
                t.body.color = alert ? Warn : new Color(0.92f, 0.94f, 0.95f);
                t.dot.color = alert ? Warn : Ok;
                if (atLoadPoint) atLoad = rig;
                if (alert && ring != null)
                {
                    float pulse = 0.5f + 0.5f * Mathf.Sin(clock * Mathf.PI * 2f * 0.8f);
                    var pos = rig.transform.position;
                    ring.transform.SetPositionAndRotation(new Vector3(pos.x, terrain.HeightAt(pos.x, pos.z) + 0.25f, pos.z), Quaternion.identity);
                    float s = 7.5f + 1.5f * pulse;
                    ring.transform.localScale = new Vector3(s, 1f, s);
                }
            }
            if (plantTag != null)
            {
                // what the feeding loader delivers: a bucket a cycle
                float cycle = fleet.CycleOf(plantFeeder);
                float tph = cycle > 0f ? LoaderBucket / cycle * 3600f : 0f;
                plantTag.body.text = $"Throughput {Mathf.RoundToInt(tph / 10f) * 10} t/h  ·  Running\nFed by {plantFeeder}";
                plantTag.dot.color = Ok;
            }
            if (loadTag != null)
            {
                float cyc = fleet.HaulCycle;
                loadTag.body.text = $"Haul cycle {Mathf.FloorToInt(cyc / 60f)} min {Mathf.RoundToInt(cyc % 60f):00} s\n"
                                    + (atLoad != null ? "Loading " + atLoad.name : "Waiting for the next truck");
            }
        }

        /// <summary>Face every tag to <paramref name="cam"/> at a constant size on screen, and set
        /// the geofence's width for the camera's distance. Call before rendering a still.</summary>
        public void Refresh(Camera cam)
        {
            if (root == null || cam == null) return;
            root.SetActive(show);
            if (!show) return;
            float k = 2f * Mathf.Tan(cam.fieldOfView * 0.5f * Mathf.Deg2Rad);
            // nearest first: a farther tag's card is lifted until it clears the cards already placed
            var order = new List<(Tag t, float dist)>();
            foreach (var t in tags)
            {
                var to = t.anchor() - cam.transform.position;
                float dist = Vector3.Dot(to, cam.transform.forward);
                t.go.SetActive(dist > 1f);
                if (dist > 1f) order.Add((t, dist));
            }
            order.Sort((a, b) => a.dist.CompareTo(b.dist));
            var placed = new List<Rect>();
            float aspect = cam.pixelWidth > 0 ? cam.pixelWidth / (float)cam.pixelHeight : 16f / 9f;
            foreach (var (t, dist) in order)
            {
                var a = t.anchor();
                float s = tagScreenHeight * dist * k / TagH;                     // metres per canvas unit
                t.rect.position = a;
                t.rect.rotation = Quaternion.LookRotation(cam.transform.forward, cam.transform.up);
                t.rect.localScale = new Vector3(s, s, s);
                // the card's rectangle on screen (viewport units), lifted in steps until it is clear
                var vp = cam.WorldToViewportPoint(a);
                float unit = tagScreenHeight / TagH;                             // viewport height per canvas unit
                float w = TagW * unit / aspect, h = t.height * unit;
                // a tag whose card would leave the frame is not drawn at all, rather than cut off
                if (vp.x - w / 2f < 0.01f || vp.x + w / 2f > 0.99f || vp.y < 0f || vp.y + (Leader * unit) + h > 0.99f)
                {
                    t.go.SetActive(false);
                    continue;
                }
                float lift = 0f;
                for (int step = 0; step < 12; step++)
                {
                    var r = new Rect(vp.x - w / 2f, vp.y + (Leader + lift) * unit, w, h);
                    bool clear = true;
                    foreach (var o in placed)
                        if (o.Overlaps(r)) { clear = false; break; }
                    if (clear) { placed.Add(r); break; }
                    lift += t.height * 0.55f;
                }
                t.leader.sizeDelta = new Vector2(3f, Leader + lift);
                t.card.anchoredPosition = new Vector2(0f, Leader + lift);
                t.rect.sizeDelta = new Vector2(TagW, t.height + Leader + lift);
            }
            if (fenceLine != null)
            {
                float d = Vector3.Distance(cam.transform.position, fenceCentre);
                fenceLine.widthMultiplier = Mathf.Clamp(d * 0.0035f, 0.25f, 1.6f);
                fenceLine.textureScale = new Vector2(1f / (fenceLine.widthMultiplier * 6f), 1f);
            }
        }
    }
}
