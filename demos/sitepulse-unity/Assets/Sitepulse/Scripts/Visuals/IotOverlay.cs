// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.Domain;
using UnityEngine;
using UnityEngine.Rendering;
using UnityEngine.UI;

namespace DeviceChain.Sitepulse.Visuals
{
    /// <summary>
    /// The scene's data layer: what a DeviceChain console would show over the site. A few cards,
    /// each tied by a leader to the machine or plant it describes, showing only what the Sitepulse
    /// device profiles measure (<see cref="MeasurementKeys"/>), with the speed derived from the
    /// machine's location; a pulsing ring under a machine with an active alarm; and a dashed
    /// geofence round the rim of the cut, its name on the line. One overlay, switched on and off
    /// as a whole (<see cref="show"/>), so the same scene gives clean stills.
    ///
    /// Each card draws a <see cref="DeviceReading"/> keyed by the platform's measurement keys, filled
    /// by an <see cref="IReadingSource"/> the composition root hands in. The overlay never knows which:
    /// the illustrative source derives readings from the preview choreography (whether a truck is
    /// loaded, how fast it moves, which loader feeds the crusher) and is labelled as such; the observed
    /// source reads what the platform reported. A reading has its source's provenance for life, so a
    /// value of the other kind cannot reach a card. An observed card shows how fresh each value is.
    ///
    /// Layout is done every frame in screen space: each target's on-screen bounds are taken from
    /// its renderers, its leader lands on the top of those bounds, and its card is placed where it
    /// covers no machine, no other card, no other leader and as little of the cut as it can. A
    /// machine much smaller on screen than the largest one in view gets no card, since its leader
    /// would point at a speck; the zone's name never sits over a card's target; and no two
    /// leaders cross or run alongside each other, so leaders from neighbouring targets fan out.
    /// </summary>
    // laid out in LateUpdate after everything else, so after a camera that moves in LateUpdate
    // (the benchmark's fly-over, a follow camera)
    [ExecuteAlways]
    [DefaultExecutionOrder(1000)]
    public sealed class IotOverlay : MonoBehaviour, IQuarryEffect
    {
        public QuarryFleetPreview fleet;
        public QuarryTerrain terrain;
        [Tooltip("The generated feature file (its geofence).")]
        public TextAsset features;
        [Tooltip("The processing plant (its Hopper node marks the primary crusher).")]
        public Transform plant;
        [Tooltip("Show the overlay.")]
        public bool show = true;
        [Tooltip("The machines that may carry a card, in order of preference.")]
        public string[] tagged = { "SP-HL-0006", "SP-LD-0003", "SP-HL-0003", "SP-HL-0001" };
        [Tooltip("At most this many cards at once.")]
        [Range(1, 6)] public int maxCards = 4;
        [Tooltip("The machine with an active alarm, and the alarm's key. A low-fuel alarm brings the " +
                 "goto-refuel command the platform's rule sends with it.")]
        public string alarmMachine = "SP-HL-0006";
        public string alarmKey = AlarmKeys.LowFuel;
        [Tooltip("The plant device's id.")]
        public string plantId = "SP-PL-0001";
        [Tooltip("A machine gets a card only if it is at least this share of the height of the largest machine in view.")]
        [Range(0f, 1f)] public float minTargetShare = 0.35f;
        [Tooltip("Card size relative to a 1080-pixel-high frame.")]
        [Range(0.5f, 2f)] public float cardScale = 1f;
        [Tooltip("The geofence's name on its label: the platform geofence the demo draws is named Pit.")]
        public string geofenceName = "Pit";
        public Material lineMaterial, ringMaterial;

        static readonly Color Panel = new Color(0.06f, 0.08f, 0.10f, 0.96f);
        static readonly Color Accent = new Color(0.36f, 0.86f, 0.96f, 1f);
        static readonly Color Ok = new Color(0.36f, 0.86f, 0.46f, 1f);
        static readonly Color Warn = new Color(1f, 0.70f, 0.12f, 1f);
        static readonly Color Ink = new Color(0.93f, 0.95f, 0.96f, 1f);
        static readonly Color Muted = new Color(0.60f, 0.68f, 0.72f, 1f);
        static readonly Color Dim = new Color(0.62f, 0.66f, 0.66f, 1f);
        static readonly Color Grey = new Color(0.40f, 0.44f, 0.46f, 1f);

        const float RefH = 1080f;                     // layout units: pixels of a 1080-high frame
        const float CardW = 252f, Pad = 12f, HeadH = 46f, RowH = 23f, AlarmH = 26f, Margin = 18f;
        const float LeaderGap = 28f;                  // how close two leaders may run, away from their pins
        const float LeaderFree = 30f;                 // the length of a leader next to its pin the gap ignores
        const int MaxRows = 4;

        [Serializable] sealed class Spot { public float x, y, z; }
        [Serializable] sealed class Geofence { public string token; public Spot[] points; }
        [Serializable] sealed class FeatureFile { public Geofence geofence; }

        sealed class Row
        {
            public Text label, value;
        }

        sealed class Target
        {
            public DeviceReading reading;
            public string kindLabel;
            public MachineRig rig;                    // null for the plant
            public Renderer[] renderers;
            public Renderer[] cab;                    // where the leader lands: the cab's roof
            public Vector3 last;
            public bool hasLast;
            public float speed;                       // m/s, smoothed

            // the card, its leader and the pin where the leader lands
            public RectTransform card, panel, edgeRect, leader, pin, dot, alarmBar;
            public Image edge, leaderImage;
            public RawImage dotImage, pinImage;
            public Text title, kind, tag, alarmText;
            public readonly Row[] rows = new Row[MaxRows];
            public float height;

            // this frame
            public Rect screen;
            public Vector2 anchor;
            public bool visible;
        }

        readonly List<Target> targets = new List<Target>();
        readonly List<Rect> blockers = new List<Rect>();
        readonly List<Rect> placed = new List<Rect>();
        readonly List<Target> chosen = new List<Target>();
        readonly List<(Vector2 a, Vector2 b)> leaders = new List<(Vector2, Vector2)>();
        readonly List<Rect> targetRects = new List<Rect>();
        readonly List<Vector2> fenceScreen = new List<Vector2>();
        Rect fenceBox;
        readonly Vector3[] corners = new Vector3[8];
        GameObject root;
        RectTransform canvasRect, fenceChip;
        Text fenceChipText;
        LineRenderer fenceLine;
        MeshRenderer ring;
        Target alarmTarget;
        Transform hopper;
        List<Vector3> fence = new List<Vector3>();
        Vector3 fenceCentre;
        List<Vector3> fenceBySouth = new List<Vector3>();
        float clock;
        static Texture2D dotTexture;
        static Font font, mono;
        IReadingSource source;
        readonly List<Rect> obstacleScratch = new List<Rect>();

        /// <summary>
        /// Screen space the overlay must not put a card or the zone's name on: the composition root's own
        /// HUD (badge, readiness panel, banner). Called each layout with the layout's width (the height is
        /// 1080) and a list to add rects to, in the same bottom-left-origin units the cards are laid out in.
        /// </summary>
        public Action<float, List<Rect>> Obstacles;

        /// <summary>
        /// Where the cards' values come from. Until the composition root sets one the overlay is
        /// illustrative, as the scene always was. Changing it drops the cards, which are rebuilt with
        /// readings of the new source's provenance.
        /// </summary>
        public IReadingSource Source
        {
            get => source;
            set
            {
                if (ReferenceEquals(source, value)) return;
                source = value;
                Clear();
            }
        }

        /// <summary>The readings the cards show, by device id.</summary>
        public IEnumerable<DeviceReading> Readings
        {
            get
            {
                foreach (var t in targets) yield return t.reading;
            }
        }

        void OnEnable()
        {
            QuarryEffects.Active.Add(this);
            RenderPipelineManager.beginCameraRendering += OnBeginCamera;
        }

        void OnDisable()
        {
            RenderPipelineManager.beginCameraRendering -= OnBeginCamera;
            QuarryEffects.Active.Remove(this);
            Clear();
        }

        /// <summary>
        /// The cards hang on a plane just in front of the camera, so the plane must be where the
        /// camera is when it draws. Placed in LateUpdate, it lagged a camera that something moved
        /// later in the frame by that frame's motion, and at half a metre from the lens a few
        /// centimetres of travel threw every card and pin some hundred pixels off its target. The
        /// layout itself is in screen terms and stays as it was laid out.
        /// </summary>
        void OnBeginCamera(ScriptableRenderContext context, Camera cam)
        {
            if (root != null && show && canvasRect != null && cam == Camera.main) PlaceCanvas(cam);
        }

        void PlaceCanvas(Camera cam)
        {
            // the canvas: a plane just beyond the near clip, filling the view
            float d = cam.nearClipPlane * 1.5f;
            float hWorld = 2f * d * Mathf.Tan(cam.fieldOfView * 0.5f * Mathf.Deg2Rad);
            canvasRect.SetPositionAndRotation(cam.transform.position + cam.transform.forward * d, cam.transform.rotation);
            canvasRect.localScale = Vector3.one * (hWorld / RefH);
        }

        void Clear()
        {
            targets.Clear();
            alarmTarget = null;
            if (root != null) DestroyImmediate(root);
            root = null;
        }

        bool Ready => fleet != null && terrain != null && terrain.Built && fleet.Count > 0;

        // ------------------------------------------------------------------ building
        void Build()
        {
            Clear();
            if (!Ready || features == null) return;
            source ??= new IllustrativeReadingSource(fleet, alarmMachine, alarmKey);
            root = new GameObject("IoT Overlay (generated)") { hideFlags = HideFlags.DontSave };
            root.transform.SetParent(transform, false);
            font = font != null ? font : Resources.GetBuiltinResource<Font>("LegacyRuntime.ttf");
            // values are set in a monospaced face, so digits line up from card to card and frame
            // to frame; the first of these the system has, else the card's own face
            mono = mono != null ? mono : Font.CreateDynamicFontFromOSFont(new[] { "Consolas", "Cascadia Mono", "Lucida Console", "DejaVu Sans Mono", "Menlo", "Courier New" }, 16);
            if (mono == null) mono = font;
            dotTexture = dotTexture != null ? dotTexture : Disc(32);

            // one canvas just in front of the camera, laid out in pixels of a 1080-high frame
            var cgo = new GameObject("Cards", typeof(RectTransform)) { hideFlags = HideFlags.DontSave };
            cgo.transform.SetParent(root.transform, false);
            var canvas = cgo.AddComponent<Canvas>();
            canvas.renderMode = RenderMode.WorldSpace;
            canvas.sortingOrder = 100;
            canvasRect = (RectTransform)cgo.transform;
            canvasRect.pivot = new Vector2(0.5f, 0.5f);

            foreach (var id in tagged)
            {
                MachineRig rig = null;
                foreach (var m in fleet.Machines)
                    if (m.name == id) rig = m;
                if (rig == null) continue;
                var reading = new DeviceReading(id, DeviceReading.Profile.Equipment, source.Provenance);
                var t = new Target
                {
                    reading = reading, rig = rig, renderers = Lod0(rig.gameObject), cab = CabOf(rig, Lod0(rig.gameObject)),
                    kindLabel = rig.Kind == MachineKind.Hauler ? "Haul truck" : rig.Kind == MachineKind.Loader ? "Wheel loader" : "Dozer",
                };
                MakeCard(t);
                targets.Add(t);
            }
            machineRenderers.Clear();
            foreach (var m in fleet.Machines) machineRenderers.Add(Lod0(m.gameObject));
            hopper = plant != null ? FleetRig.Find(plant, "Hopper") : null;
            if (hopper != null)
            {
                var t = new Target { reading = new DeviceReading(plantId, DeviceReading.Profile.Plant, source.Provenance), kindLabel = "Primary crusher" };
                MakeCard(t);
                targets.Add(t);
            }

            fence = Parse(features.text, geofenceName, out var zone);
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
                // the zone's name sits on the line: at the southern-most point of it in view
                fenceBySouth = new List<Vector3>(pts);
                fenceBySouth.Sort((a, b) => a.z.CompareTo(b.z));
                fenceChip = Image(canvasRect, "Geofence Label", Vector2.zero, new Vector2(10f, 26f), Panel);
                Image(fenceChip, "Edge", Vector2.zero, new Vector2(4f, 26f), Accent);
                fenceChipText = Label(fenceChip, "Text", new Vector2(12f, 3f), new Vector2(400f, 20f), 16, FontStyle.Bold, Accent);
                fenceChipText.text = zone;
                fenceChip.sizeDelta = new Vector2(fenceChipText.preferredWidth + 24f, 26f);
            }
            if (ringMaterial != null)
            {
                var go = new GameObject("Alarm Ring") { hideFlags = HideFlags.DontSave };
                go.transform.SetParent(root.transform, false);
                go.AddComponent<MeshFilter>().sharedMesh = RingMesh();
                ring = go.AddComponent<MeshRenderer>();
                ring.sharedMaterial = ringMaterial;
                ring.shadowCastingMode = ShadowCastingMode.Off;
                ring.receiveShadows = false;
            }
            UpdateReadings();
            Refresh(Camera.main);
        }

        /// <summary>The renderers of a machine's cab, from its LOD0 renderers.</summary>
        static Renderer[] CabOf(MachineRig rig, Renderer[] lod0)
        {
            var cab = FleetRig.Find(rig.transform, "Cab");
            if (cab == null) return null;
            var list = new List<Renderer>();
            foreach (var r in lod0)
                if (r != null && r.transform.IsChildOf(cab)) list.Add(r);
            return list.ToArray();
        }

        static Renderer[] Lod0(GameObject go)
        {
            var lg = go.GetComponentInChildren<LODGroup>();
            if (lg != null)
            {
                var lods = lg.GetLODs();
                if (lods.Length > 0 && lods[0].renderers.Length > 0) return lods[0].renderers;
            }
            return go.GetComponentsInChildren<Renderer>();
        }

        void MakeCard(Target t)
        {
            var card = new GameObject(t.reading.DeviceId, typeof(RectTransform)).GetComponent<RectTransform>();
            card.SetParent(canvasRect, false);
            Place(card, Vector2.zero, new Vector2(CardW, 100f));
            t.card = card;
            t.panel = Image(card, "Panel", Vector2.zero, new Vector2(CardW, 100f), Panel);
            t.edgeRect = Image(card, "Edge", Vector2.zero, new Vector2(4f, 100f), Accent);
            t.edge = t.edgeRect.GetComponent<Image>();
            t.title = Label(card, "Title", Vector2.zero, new Vector2(CardW - 40f, 24f), 20, FontStyle.Bold, Accent);
            t.title.text = t.reading.DeviceId;
            t.kind = Label(card, "Kind", Vector2.zero, new Vector2(CardW - 24f, 18f), 14, FontStyle.Normal, Muted);
            t.kind.text = t.kindLabel;
            t.tag = Label(card, "Tag", Vector2.zero, new Vector2(120f, 18f), 13, FontStyle.Normal, Muted);
            t.tag.alignment = TextAnchor.UpperRight;
            var dot = new GameObject("Status", typeof(RectTransform)).AddComponent<RawImage>();
            dot.transform.SetParent(card, false);
            dot.texture = dotTexture;
            dot.color = Ok;
            dot.raycastTarget = false;
            t.dot = dot.rectTransform;
            t.dotImage = dot;
            Place(t.dot, Vector2.zero, new Vector2(14f, 14f));
            t.alarmBar = Image(card, "Alarm", Vector2.zero, new Vector2(CardW, AlarmH), new Color(Warn.r, Warn.g, Warn.b, 0.95f));
            t.alarmText = Label(t.alarmBar, "Text", new Vector2(Pad, 3f), new Vector2(CardW - 2 * Pad, 20f), 15, FontStyle.Bold, new Color(0.10f, 0.08f, 0.04f));
            for (int i = 0; i < MaxRows; i++)
            {
                t.rows[i] = new Row
                {
                    label = Label(card, "Key" + i, Vector2.zero, new Vector2(CardW * 0.55f, 20f), 15, FontStyle.Normal, Muted),
                    value = Label(card, "Value" + i, Vector2.zero, new Vector2(CardW * 0.45f - Pad, 20f), 16, FontStyle.Normal, Ink),
                };
                t.rows[i].value.font = mono;
                t.rows[i].value.alignment = TextAnchor.UpperRight;
            }
            // the leader and the dot that marks where it lands
            t.leader = Image(canvasRect, t.reading.DeviceId + " Leader", Vector2.zero, new Vector2(10f, 2f), Accent);
            t.leader.pivot = new Vector2(0f, 0.5f);
            t.leader.SetSiblingIndex(0);
            t.leaderImage = t.leader.GetComponent<Image>();
            var pin = new GameObject(t.reading.DeviceId + " Pin", typeof(RectTransform)).AddComponent<RawImage>();
            pin.transform.SetParent(canvasRect, false);
            pin.texture = dotTexture;
            pin.color = Accent;
            pin.raycastTarget = false;
            t.pinImage = pin;
            t.pin = pin.rectTransform;
            t.pin.anchorMin = t.pin.anchorMax = Vector2.zero;
            t.pin.pivot = new Vector2(0.5f, 0.5f);
            t.pin.sizeDelta = new Vector2(11f, 11f);
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
            ringMesh = new Mesh { name = "AlarmRing", hideFlags = HideFlags.DontSave };
            ringMesh.SetVertices(v);
            ringMesh.SetUVs(0, uv);
            ringMesh.SetTriangles(tri, 0);
            ringMesh.RecalculateNormals();
            ringMesh.RecalculateBounds();
            return ringMesh;
        }

        /// <summary>The geofence's points and its label from the feature file.</summary>
        static List<Vector3> Parse(string json, string name, out string label)
        {
            var fence = new List<Vector3>();
            var f = JsonUtility.FromJson<FeatureFile>(json);
            label = null;
            if (f.geofence != null)
            {
                if (f.geofence.points != null)
                    foreach (var p in f.geofence.points) fence.Add(new Vector3(p.x, p.y, p.z));
                label = "Geofence  " + name;
            }
            return fence;
        }

        // ------------------------------------------------------------------ readings
        public void ClearParticles()
        {
            foreach (var t in targets) t.hasLast = false;
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
            foreach (var t in targets)
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
            UpdateReadings();
        }

        /// <summary>Asks the source to refresh every card's reading, then draws them.</summary>
        void UpdateReadings()
        {
            var now = DateTimeOffset.UtcNow;
            var src = source;
            alarmTarget = null;
            foreach (var t in targets)
            {
                // the choreography's inputs go only to a source that is illustrative; an observed one is handed an id
                (src as IllustrativeReadingSource)?.SetModel(t.reading.DeviceId, t.rig, t.speed);
                src.Fill(new ReadingSubject(t.reading.DeviceId), t.reading, now);
                if (t.rig != null && t.reading.HasAlarm) alarmTarget = t;
            }

            foreach (var t in targets) Fill(t, now, src.StreamLive);
        }

        static Color InkOf(RowTone tone) => tone == RowTone.Dim ? Dim : tone == RowTone.Ink ? Ink : tone == RowTone.Warn ? Warn : Grey;

        /// <summary>Write a reading into its card: the rows it shows, its alarm and its status. What each
        /// row and the status say is decided by <see cref="CardPresenter"/>; this only draws it.</summary>
        void Fill(Target t, DateTimeOffset now, bool live)
        {
            var r = t.reading;
            int n = 0;
            bool observed = r.Provenance == Provenance.Observed;
            string alarmKeyRow = r.HasAlarm ? AlarmKeys.Metric(r.FirstAlarm.Key) : null;
            void Draw(string label, RowView v)
            {
                if (!v.Shown || n >= MaxRows) return;
                var row = t.rows[n++];
                Set(row.label, label);
                Set(row.value, v.Text);
                row.value.color = InkOf(v.Tone);
            }
            DateTimeOffset? StampOf(string key) => r.TryGetStamp(key, out var st) ? st.OccurredAt : (DateTimeOffset?)null;
            void Metric(string key, bool warn = false) => Draw(MeasurementKeys.Label(key), CardPresenter.Row(observed, r.Format(key), StampOf(key), now, live, warn));
            bool stopped = false;
            if (r.Kind == DeviceReading.Profile.Plant)
            {
                Metric(MeasurementKeys.ThroughputTph);
                Metric(MeasurementKeys.PlantRunning);
                stopped = r.TryGetFlag(MeasurementKeys.PlantRunning, out bool running) && !running;
            }
            else
            {
                if (alarmKeyRow != null) Metric(alarmKeyRow, warn: true);
                Metric(MeasurementKeys.PayloadT);
                if (alarmKeyRow != MeasurementKeys.FuelPct) Metric(MeasurementKeys.FuelPct);
                Draw("Speed", CardPresenter.Row(observed, r.SpeedKmh.HasValue ? r.SpeedKmh.Value.ToString("0") + " km/h" : null,
                    r.SpeedStamp?.OccurredAt, now, live, false, FreshnessRule.LocationFreshWithin));
                if (r.CommandStatus.HasValue)
                    Draw(CommandKeys.Label(r.Command), CardPresenter.CommandRow(observed, r.CommandStatus.Value.Label, r.CommandStatus.Value.IsTerminal, r.CommandStamp?.ObservedAt, now, live));
            }
            for (int i = 0; i < MaxRows; i++)
            {
                t.rows[i].label.enabled = i < n;
                t.rows[i].value.enabled = i < n;
            }
            bool alarm = r.HasAlarm;
            t.alarmBar.gameObject.SetActive(alarm);
            if (alarm)
            {
                var a0 = r.FirstAlarm;
                Set(t.alarmText, (string.IsNullOrEmpty(a0.Severity) ? "ALARM" : a0.Severity) + "  " + AlarmKeys.Label(a0.Key));
            }

            var sv = CardPresenter.Status(r.Provenance, r.NewestOccurredAt, now, live, alarm, stopped);
            var status = sv.Dot == DotTone.Ok ? Ok : sv.Dot == DotTone.Warn ? Warn : sv.Dot == DotTone.Muted ? Muted : Grey;
            var tag = sv.Tag;

            Set(t.tag, tag);
            t.dotImage.color = status;
            t.edge.color = alarm ? Warn : Accent;
            t.leaderImage.color = alarm ? Warn : Accent;
            t.pinImage.color = alarm ? Warn : Accent;

            // lay the card out from the top: alarm bar, title and kind, rows
            float h = Pad + (alarm ? AlarmH : 0f) + HeadH + n * RowH + Pad * 0.5f;
            t.height = h;
            t.card.sizeDelta = new Vector2(CardW, h);
            t.panel.sizeDelta = new Vector2(CardW, h);
            t.edgeRect.sizeDelta = new Vector2(4f, h);
            float y = h;
            if (alarm)
            {
                t.alarmBar.anchoredPosition = new Vector2(0f, h - AlarmH);
                t.alarmBar.sizeDelta = new Vector2(CardW, AlarmH);
                y -= AlarmH;
            }
            t.title.rectTransform.anchoredPosition = new Vector2(Pad + 4f, y - Pad - 22f);
            t.dot.anchoredPosition = new Vector2(CardW - Pad - 14f, y - Pad - 17f);
            t.kind.rectTransform.anchoredPosition = new Vector2(Pad + 4f, y - Pad - 40f);
            t.tag.rectTransform.anchoredPosition = new Vector2(CardW - Pad - 120f, y - Pad - 40f);
            y -= Pad + HeadH;
            for (int i = 0; i < n; i++)
            {
                t.rows[i].label.rectTransform.anchoredPosition = new Vector2(Pad + 4f, y - 19f);
                t.rows[i].value.rectTransform.anchoredPosition = new Vector2(CardW * 0.55f, y - 20f);
                y -= RowH;
            }
        }

        static void Set(Text t, string s)
        {
            if (t.text != s) t.text = s;
        }

        // ------------------------------------------------------------------ layout
        /// <summary>
        /// Place the canvas in front of <paramref name="cam"/>, find each target on screen, and
        /// lay out the cards and leaders for this frame. Call before rendering a still.
        /// </summary>
        public void Refresh(Camera cam)
        {
            if (root == null || cam == null) return;
            root.SetActive(show);
            if (!show) return;
            float aspect = cam.aspect > 0f ? cam.aspect : 16f / 9f;            // the projection's, which places the targets
            float refW = RefH * aspect;

            canvasRect.sizeDelta = new Vector2(refW, RefH);
            PlaceCanvas(cam);

            // every machine on screen is something a card must not cover
            blockers.Clear();
            foreach (var rs in machineRenderers)
                if (ScreenRect(cam, Bounds(rs), refW, out var r)) blockers.Add(Inflate(r, 4f));
            AddObstacles(Obstacles, refW, obstacleScratch, blockers);

            foreach (var t in targets)
            {
                t.visible = false;
                var b = t.rig != null ? Bounds(t.renderers) : PlantBounds();
                if (!ScreenRect(cam, b, refW, out t.screen)) continue;
                // the leader lands on the machine itself, on its cab's roof (the top of the cab's
                // bounds), not on the top of the whole machine's box, which may be in the air
                var cb = t.cab != null && t.cab.Length > 0 ? Bounds(t.cab) : b;
                var top = t.rig != null ? new Vector3(cb.center.x, cb.max.y, cb.center.z) : hopper.position;
                var sp = cam.WorldToViewportPoint(top);
                t.anchor = new Vector2(sp.x * refW, sp.y * RefH);
                // too small to point at, the point is off the frame, or the ground hides it
                t.visible = sp.z > 0f && t.screen.height >= 10f
                            && t.anchor.x > Margin && t.anchor.x < refW - Margin && t.anchor.y > Margin && t.anchor.y < RefH - Margin
                            && !Hidden(cam, top);
                if (t.rig == null) blockers.Add(Inflate(t.screen, 2f));
            }

            // the cards: the alarm first, then the plant, then by how large the machine is on
            // screen; a machine far smaller than the largest in view is a speck, and gets none
            float largest = 0f;
            foreach (var t in targets)
                if (t.visible && t.rig != null) largest = Mathf.Max(largest, t.screen.height);
            chosen.Clear();
            foreach (var t in targets)
                if (t.visible && (t.rig == null || t.screen.height >= minTargetShare * largest)) chosen.Add(t);
            chosen.Sort(byPriority ??= (a, b) => Priority(b).CompareTo(Priority(a)));
            if (chosen.Count > maxCards) chosen.RemoveRange(maxCards, chosen.Count - maxCards);
            // nothing is laid over a target that has a card: not the zone's name, not another card
            targetRects.Clear();
            foreach (var t in chosen) targetRects.Add(Inflate(t.screen, 6f));

            // the cut, on screen: cards may cover it, at a cost
            fenceScreen.Clear();
            fenceBox = default;
            if (fenceLine != null)
            {
                // every fourth point is plenty to say how much of a card is over the cut
                for (int i = 0; i < fence.Count; i += 4)
                {
                    var v = cam.WorldToViewportPoint(fence[i]);
                    if (v.z <= 0f) continue;
                    var q = new Vector2(v.x * refW, v.y * RefH);
                    fenceBox = fenceScreen.Count == 0 ? new Rect(q, Vector2.zero) : Rect.MinMaxRect(
                        Mathf.Min(fenceBox.xMin, q.x), Mathf.Min(fenceBox.yMin, q.y), Mathf.Max(fenceBox.xMax, q.x), Mathf.Max(fenceBox.yMax, q.y));
                    fenceScreen.Add(q);
                }
                float dist = Vector3.Distance(cam.transform.position, fenceCentre);
                fenceLine.widthMultiplier = Mathf.Clamp(dist * 0.0035f, 0.25f, 1.6f);
                fenceLine.textureScale = new Vector2(1f / (fenceLine.widthMultiplier * 6f), 1f);
            }

            placed.Clear();
            leaders.Clear();
            if (fenceChip != null)
            {
                var size = fenceChip.sizeDelta * cardScale;
                Vector2 at = default;
                bool on = false;
                for (int i = 0; i < fenceBySouth.Count; i += 6)
                {
                    var p = fenceBySouth[i];
                    var v = cam.WorldToViewportPoint(p);
                    at = new Vector2(v.x * refW - size.x / 2f, v.y * RefH - size.y / 2f);
                    on = v.z > 0f && at.x > Margin && at.x + size.x < refW - Margin && at.y > Margin && at.y + size.y < RefH - Margin
                         && !Hits(new Rect(at, size), targetRects) && !Hits(new Rect(at, size), blockers)
                         && !Hidden(cam, p + Vector3.up * 0.4f);
                    if (on) break;
                }
                fenceChip.gameObject.SetActive(on);
                if (on)
                {
                    fenceChip.anchoredPosition = at;
                    fenceChip.localScale = Vector3.one * cardScale;
                    placed.Add(Inflate(new Rect(at, size), 6f));
                }
            }

            foreach (var t in targets)
            {
                bool on = chosen.Contains(t) && PlaceCard(t, refW);
                t.card.gameObject.SetActive(on);
                t.leader.gameObject.SetActive(on);
                t.pin.gameObject.SetActive(on);
            }

            if (ring != null)
            {
                bool on = alarmTarget != null && alarmTarget.rig != null;
                ring.gameObject.SetActive(on);
                if (on)
                {
                    float pulse = 0.5f + 0.5f * Mathf.Sin(clock * Mathf.PI * 2f * 0.8f);
                    var pos = alarmTarget.rig.transform.position;
                    ring.transform.SetPositionAndRotation(new Vector3(pos.x, terrain.HeightAt(pos.x, pos.z) + 0.25f, pos.z), Quaternion.identity);
                    float s = 7.5f + 1.5f * pulse;
                    ring.transform.localScale = new Vector3(s, 1f, s);
                }
            }
        }

        /// <summary>Whether the terrain stands between the camera and a point.</summary>
        bool Hidden(Camera cam, Vector3 p)
        {
            var col = terrain.Terrain != null ? terrain.Terrain.GetComponent<TerrainCollider>() : null;
            if (col == null) return false;
            var from = cam.transform.position;
            var to = p - from;
            float d = to.magnitude;
            return col.Raycast(new Ray(from, to / d), out _, d - 0.5f);
        }

        Comparison<Target> byPriority;

        float Priority(Target t)
        {
            if (t.reading.HasAlarm) return 1e6f;
            if (t.rig == null) return 1e5f;
            if (t.rig.name == IllustrativeReadingSource.PlantFeeder) return 1e4f + t.screen.height;
            return t.screen.height;
        }

        static readonly float[] Angles = { 90f, 65f, 115f, 40f, 140f, 15f, 165f, -15f, -165f, -50f, -130f };
        static readonly float[] Lengths = { 55f, 95f, 140f, 190f, 250f, 320f, 400f };

        /// <summary>Find the cheapest place for a card round its anchor: inside the frame, over no
        /// machine and no other card, its leader crossing no card, covering as little of the cut
        /// as it can. False when there is none.</summary>
        bool PlaceCard(Target t, float refW)
        {
            var size = new Vector2(CardW, t.height) * cardScale;
            float best = float.PositiveInfinity;
            Rect bestRect = default;
            Vector2 bestElbow = default;
            foreach (float ang in Angles)
            {
                var dir = new Vector2(Mathf.Cos(ang * Mathf.Deg2Rad), Mathf.Sin(ang * Mathf.Deg2Rad));
                foreach (float len in Lengths)
                {
                    var e = t.anchor + dir * len;
                    // the leader meets the card at the corner or side nearest the anchor
                    float x = Mathf.Abs(dir.x) < 0.2f ? e.x - size.x * 0.18f : dir.x > 0f ? e.x : e.x - size.x;
                    float y = dir.y > 0.35f ? e.y : dir.y < -0.35f ? e.y - size.y : e.y - size.y / 2f;
                    var r = new Rect(x, y, size.x, size.y);
                    if (r.xMin < Margin || r.yMin < Margin || r.xMax > refW - Margin || r.yMax > RefH - Margin) continue;
                    float cost = len + Mathf.Abs(ang - 90f) * 0.6f;
                    if (cost >= best) continue;
                    if (Hits(r, placed) || Hits(r, blockers) || Hits(r, targetRects)) continue;
                    if (LeaderHits(t.anchor, e, placed) || CoversLeader(r) || LeaderClash(t.anchor, e)) continue;
                    cost += CutCover(r) * 900f;
                    if (cost < best)
                    {
                        best = cost;
                        bestRect = r;
                        bestElbow = e;
                    }
                }
            }
            if (float.IsPositiveInfinity(best)) return false;
            placed.Add(Inflate(bestRect, 8f));
            leaders.Add((t.anchor, bestElbow));
            t.card.anchoredPosition = bestRect.position;
            t.card.localScale = Vector3.one * cardScale;
            var seg = bestElbow - t.anchor;
            t.leader.anchoredPosition = t.anchor;
            t.leader.sizeDelta = new Vector2(seg.magnitude, 2f * Mathf.Max(1f, cardScale));
            t.leader.localRotation = Quaternion.Euler(0f, 0f, Mathf.Atan2(seg.y, seg.x) * Mathf.Rad2Deg);
            t.pin.anchoredPosition = t.anchor;
            return true;
        }

        static bool Hits(Rect r, List<Rect> list)
        {
            foreach (var o in list)
                if (o.Overlaps(r)) return true;
            return false;
        }

        /// <summary>Whether a card would sit over a leader already drawn.</summary>
        bool CoversLeader(Rect r)
        {
            var big = Inflate(r, 6f);
            foreach (var (a, b) in leaders)
                for (int i = 0; i <= 12; i++)
                    if (big.Contains(Vector2.Lerp(a, b, i / 12f))) return true;
            return false;
        }

        /// <summary>Whether a leader from <paramref name="a"/> to <paramref name="b"/> crosses one
        /// already drawn, or runs within <see cref="LeaderGap"/> of it anywhere beyond the first
        /// <see cref="LeaderFree"/> of either from its pin. Two targets side by side may have pins
        /// close together; their leaders must still part at once and go to different places.</summary>
        bool LeaderClash(Vector2 a, Vector2 b)
        {
            float len = Vector2.Distance(a, b);
            foreach (var (c, d) in leaders)
            {
                if (SegmentsCross(a, b, c, d)) return true;
                float lenO = Vector2.Distance(c, d);
                var c2 = lenO > LeaderFree ? Vector2.Lerp(c, d, LeaderFree / lenO) : d;
                for (float s = LeaderFree; s <= len; s += 6f)
                    if (DistanceToSegment(Vector2.Lerp(a, b, s / len), c2, d) < LeaderGap) return true;
                // and the other way round, so a short new leader cannot end beside a long one
                var a2 = len > LeaderFree ? Vector2.Lerp(a, b, LeaderFree / len) : b;
                for (float s = LeaderFree; s <= lenO; s += 6f)
                    if (DistanceToSegment(Vector2.Lerp(c, d, s / lenO), a2, b) < LeaderGap) return true;
            }
            return false;
        }

        static bool SegmentsCross(Vector2 a, Vector2 b, Vector2 c, Vector2 d)
        {
            static float Cross(Vector2 o, Vector2 p, Vector2 q) => (p.x - o.x) * (q.y - o.y) - (p.y - o.y) * (q.x - o.x);
            float d1 = Cross(c, d, a), d2 = Cross(c, d, b), d3 = Cross(a, b, c), d4 = Cross(a, b, d);
            return d1 * d2 < 0f && d3 * d4 < 0f;
        }

        static float DistanceToSegment(Vector2 p, Vector2 a, Vector2 b)
        {
            var ab = b - a;
            float l2 = ab.sqrMagnitude;
            float t = l2 > 1e-6f ? Mathf.Clamp01(Vector2.Dot(p - a, ab) / l2) : 0f;
            return Vector2.Distance(p, a + ab * t);
        }

        static bool LeaderHits(Vector2 a, Vector2 b, List<Rect> list)
        {
            for (int i = 1; i < 12; i++)
            {
                var p = Vector2.Lerp(a, b, i / 12f);
                foreach (var o in list)
                    if (o.Contains(p)) return true;
            }
            return false;
        }

        /// <summary>The share of a card's area over the cut (sampled on a grid).</summary>
        float CutCover(Rect r)
        {
            if (fenceScreen.Count < 3 || !fenceBox.Overlaps(r)) return 0f;
            int inside = 0;
            for (int i = 0; i < 5; i++)
                for (int j = 0; j < 3; j++)
                    if (InPolygon(new Vector2(r.xMin + r.width * (i + 0.5f) / 5f, r.yMin + r.height * (j + 0.5f) / 3f), fenceScreen)) inside++;
            return inside / 15f;
        }

        static bool InPolygon(Vector2 p, List<Vector2> poly)
        {
            bool c = false;
            for (int i = 0, j = poly.Count - 1; i < poly.Count; j = i++)
                if ((poly[i].y > p.y) != (poly[j].y > p.y)
                    && p.x < (poly[j].x - poly[i].x) * (p.y - poly[i].y) / (poly[j].y - poly[i].y) + poly[i].x)
                    c = !c;
            return c;
        }

        /// <summary>Adds the screen space the HUD holds to the layout's blockers, a little inflated so a card clears it.</summary>
        public static void AddObstacles(Action<float, List<Rect>> obstacles, float frameWidth, List<Rect> scratch, List<Rect> blockers)
        {
            if (obstacles == null) return;
            scratch.Clear();
            obstacles(frameWidth, scratch);
            foreach (var o in scratch) blockers.Add(Inflate(o, 6f));
        }

        static Rect Inflate(Rect r, float by) => new Rect(r.xMin - by, r.yMin - by, r.width + 2f * by, r.height + 2f * by);

        static Bounds Bounds(Renderer[] rs)
        {
            var b = new Bounds();
            bool any = false;
            foreach (var r in rs)
            {
                if (r == null || !r.enabled || !r.gameObject.activeInHierarchy) continue;
                if (!any) { b = r.bounds; any = true; }
                else b.Encapsulate(r.bounds);
            }
            return b;
        }

        readonly List<Renderer[]> machineRenderers = new List<Renderer[]>();

        /// <summary>The primary crusher: its hopper and the crusher under it, not the whole plant.</summary>
        Bounds PlantBounds()
        {
            var c = hopper.position;
            return new Bounds(new Vector3(c.x, c.y - 1.5f, c.z) + plant.forward * 2.5f, new Vector3(9f, 6f, 9f));
        }

        /// <summary>A world box's rectangle on screen in layout units; false if any of it is behind the camera.</summary>
        bool ScreenRect(Camera cam, Bounds b, float refW, out Rect r)
        {
            r = default;
            if (b.size == Vector3.zero) return false;
            var mn = b.min;
            var mx = b.max;
            int k = 0;
            for (int i = 0; i < 2; i++)
                for (int j = 0; j < 2; j++)
                    for (int l = 0; l < 2; l++)
                        corners[k++] = new Vector3(i == 0 ? mn.x : mx.x, j == 0 ? mn.y : mx.y, l == 0 ? mn.z : mx.z);
            float x0 = float.PositiveInfinity, y0 = float.PositiveInfinity, x1 = float.NegativeInfinity, y1 = float.NegativeInfinity;
            foreach (var c in corners)
            {
                var v = cam.WorldToViewportPoint(c);
                if (v.z <= 0f) return false;
                x0 = Mathf.Min(x0, v.x * refW);
                x1 = Mathf.Max(x1, v.x * refW);
                y0 = Mathf.Min(y0, v.y * RefH);
                y1 = Mathf.Max(y1, v.y * RefH);
            }
            r = Rect.MinMaxRect(x0, y0, x1, y1);
            return r.xMax > 0f && r.xMin < refW && r.yMax > 0f && r.yMin < RefH;
        }
    }
}
