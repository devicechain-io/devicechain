// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Globalization;
using System.Text;
using DeviceChain.Demos.Platform;
using DeviceChain.Harborpulse.Live;
using DeviceChain.Harborpulse.Model;
using UnityEngine;

namespace DeviceChain.Harborpulse.Visuals
{
    /// <summary>
    /// The Harborpulse scene's composition root (milestone U0). Put it on one GameObject with a camera in the scene and press Play: it reads the
    /// committed layout, builds the waterfront in code, and then either
    ///
    /// - LIVE (the default): follows a runner of the harborpulse simulation. The sim's <c>/status</c> drives the sky's clock, and the platform's own
    ///   record (read through the DeviceChain C# SDK under the runner's operator token, never written) drives the water's tide, from the gauges,
    ///   and the street lights. Runner: <c>-dc-runner URL</c>, else the HARBORPULSE_RUNNER environment variable, else <see cref="runnerUrl"/>.
    /// - OFFLINE PREVIEW (<c>-hp-offline</c>, HARBORPULSE_OFFLINE=1, or <see cref="offlinePreview"/>): no network; a labelled stand-in plays the
    ///   story's shape for rehearsal and stills. Every frame says PREVIEW.
    ///
    /// Optional flags: -hp-start-hour H and -hp-speed S (preview only), -hp-expect-tenant T (live: refuse another tenant's runner).
    /// </summary>
    public sealed class HarborpulseApp : MonoBehaviour
    {
        public const string OfflineFlag = "-hp-offline", OfflineEnv = "HARBORPULSE_OFFLINE", RunnerFlag = "-dc-runner", RunnerEnv = "HARBORPULSE_RUNNER";
        public const string StartHourFlag = "-hp-start-hour", SpeedFlag = "-hp-speed", ExpectTenantFlag = "-hp-expect-tenant";

        [Tooltip("Run the labelled offline preview instead of following a runner.")]
        public bool offlinePreview;

        [Tooltip("The harborpulse runner's control address, when neither -dc-runner nor HARBORPULSE_RUNNER is given.")]
        public string runnerUrl = "http://localhost:8090";

        [Tooltip("Preview only: the hour of day the preview opens at.")]
        public double previewStartHour = 16.5;

        [Tooltip("The shaders the scene builds its materials from; assigned by the scene builder so a player build includes them.")]
        public Shader litShader, waterShader, skyShader, wetWellShader;

        public Camera sceneCamera;

        HarborLayout layout;
        SitePlan plan;
        HarborMaterials mats;
        HarborWorld world;
        HarborSkyController sky;
        HarborWaterController water;
        HarborCameraRig rig;
        HarborObservedModel model;
        HarborFrame frame;
        HarborViews views;
        HarborGrading grading;
        PlanarWaterReflection reflection;
        DateTimeOffset? modelRun;

        PreviewSource preview;
        double previewSimS, previewSpeed = 1;
        bool previewHeld;

        HarborLiveSession live;
        LiveSettings liveSettings;

        ClockReading clock;
        Weather weather;
        float waterTime;
        GUIStyle panel, small;
        readonly StringBuilder sb = new StringBuilder();

        /// <summary>Why the scene could not start, or null.</summary>
        public string Problem { get; private set; }

        public bool Offline { get; private set; }
        public HarborCameraRig Rig => rig;
        public ClockReading Clock => clock;

        /// <summary>Stills and rehearsal: when set, the water's wave clock is this instead of the frame clock.</summary>
        public float? WaterTimeOverride { get; set; }

        /// <summary>Preview only: hold the day at an hour (a still frame).</summary>
        public void HoldPreviewAt(double hourOfDay)
        {
            if (!Offline) throw new InvalidOperationException("only the offline preview's clock can be set by hand; a live clock is the runner's");
            model = new HarborObservedModel(layout);
            preview = new PreviewSource(layout);
            previewHeld = true;
            // Observe forward through the lead-in, so a still does not manufacture command history or a start transition.
            var targetNow = DateTimeOffset.UtcNow;
            var from = System.Math.Max(0, hourOfDay - 1);
            for (var hour = from; hour < hourOfDay; hour += 0.01)
            {
                previewSimS = hour * 3600;
                Tick((float)(0.01 / 24 * layout.DayLengthSeconds), targetNow.AddSeconds((hour - hourOfDay) / 24 * layout.DayLengthSeconds));
            }
            previewSimS = hourOfDay * 3600;
            Tick(0, targetNow);
            Tick(2.1f, targetNow.AddSeconds(2.1));
        }

        public void RenderReflectionNow() => reflection?.RenderNow();
        void LateUpdate() => reflection?.LateTick();
        public HarborFrame Frame => frame;

        void Awake()
        {
            DemoIdentity.Configure(HarborScene.DemoName);
            Application.runInBackground = true;
        }

        void Start() => Initialize();

        bool initialized;

        /// <summary>
        /// Builds the scene and starts its mode. Play mode calls it from Start; the Editor's stills tool calls it on an object it made in edit mode
        /// (where no Start runs), after setting <see cref="offlinePreview"/>. A second call does nothing.
        /// </summary>
        public void Initialize()
        {
            if (initialized) return;
            initialized = true;
            DemoIdentity.Configure(HarborScene.DemoName);
            try
            {
                var text = Resources.Load<TextAsset>("harbor_layout");
                if (text == null) throw new FormatException("Resources/harbor_layout.json is missing from the build");
                layout = HarborLayout.Parse(text.text);
                plan = SitePlan.Build(layout);
                model = new HarborObservedModel(layout);
                mats = new HarborMaterials(litShader);
                mats.CreateWetWell(wetWellShader != null ? wetWellShader : Shader.Find("Harborpulse/WetWellWater"));
                world = HarborWorld.Build(plan, layout, mats, transform);
                sky = new HarborSkyController(transform, skyShader);
                water = new HarborWaterController(transform, waterShader, plan, 9000);
                if (sceneCamera == null) sceneCamera = Camera.main;
                if (sceneCamera == null) throw new InvalidOperationException("the scene has no camera");
                sceneCamera.farClipPlane = 20000;
                sceneCamera.nearClipPlane = 0.3f;
                rig = new HarborCameraRig(sceneCamera, plan);
                grading = new HarborGrading(transform, sceneCamera);
                reflection = new PlanarWaterReflection(transform, sceneCamera);
                views = new HarborViews(world, plan, mats);
            }
            catch (Exception e)
            {
                Problem = e.Message;
                Debug.LogError(DemoIdentity.LogPrefix + "the scene cannot start: " + e);
                return;
            }

            var args = Environment.GetCommandLineArgs();
            Offline = offlinePreview || Array.IndexOf(args, OfflineFlag) >= 0 || Environment.GetEnvironmentVariable(OfflineEnv) == "1";
            if (Offline) StartPreview(args);
            else StartLive(args);
        }

        void StartPreview(string[] args)
        {
            var hour = Number(args, StartHourFlag, previewStartHour, 0, 23.99);
            previewSpeed = Number(args, SpeedFlag, 1, 0.1, 60);
            if (Problem != null) return;
            preview = new PreviewSource(layout);
            previewSimS = hour * 3600.0;
            PlatformLog.Info("offline preview · no platform · from " + DayClock.FormatHourMinute(previewSimS) + " at x" + previewSpeed.ToString(CultureInfo.InvariantCulture));
        }

        void StartLive(string[] args)
        {
            var url = CommandLineArgs.Get(args, RunnerFlag, out var err) ?? Environment.GetEnvironmentVariable(RunnerEnv) ?? runnerUrl;
            var tenant = CommandLineArgs.Get(args, ExpectTenantFlag, out var err2);
            if (err != null || err2 != null)
            {
                Problem = err ?? err2;
                return;
            }

            if (!Uri.TryCreate(url, UriKind.Absolute, out var u) || (u.Scheme != "http" && u.Scheme != "https"))
            {
                Problem = "the runner address \"" + url + "\" is not an http(s) URL";
                return;
            }

            liveSettings = new LiveSettings { RunnerUrl = url, ExpectTenant = tenant, Source = "command line" };
            live = new HarborLiveSession(layout, liveSettings);
            live.Start();
        }

        double Number(string[] args, string flag, double fallback, double lo, double hi)
        {
            var raw = CommandLineArgs.Get(args, flag, out var err);
            if (err != null) Problem = err;
            if (raw == null) return fallback;
            if (double.TryParse(raw, NumberStyles.Float, CultureInfo.InvariantCulture, out var v) && v >= lo && v <= hi) return v;
            Problem = flag + " \"" + raw + "\" must be a number in [" + lo + ", " + hi + "]";
            return fallback;
        }

        void Update()
        {
            if (Problem != null || world == null) return;
            Tick(Time.deltaTime);
        }

        public void Tick(float dt, DateTimeOffset? wallNow = null)
        {
            var now = wallNow ?? DateTimeOffset.UtcNow;
            IHarborSource source;
            if (Offline)
            {
                if (!previewHeld) previewSimS += DayClock.Day * dt / layout.DayLengthSeconds * previewSpeed;
                clock = new ClockReading(previewSimS, ClockMode.Preview, "offline preview: not platform data");
                preview.SetTime(previewSimS, now);
                source = preview;
            }
            else
            {
                live.Tick();
                clock = live.Clock.Read(now);
                source = live.Source;
            }

            var started = source.Status?.StartedAt;
            if (!Offline && modelRun.HasValue && modelRun != started)
            {
                // A new runner can bind different platform tokens. Discard the observer as well as the reducers.
                live.Dispose();
                live = new HarborLiveSession(layout, liveSettings); live.Start();
                model = new HarborObservedModel(layout); modelRun = null;
                source = live.Source; clock = live.Clock.Read(now);
            }
            else if (!Offline && started.HasValue && !modelRun.HasValue)
            {
                model = new HarborObservedModel(layout); modelRun = started;
            }
            frame = model.Update(source, clock, now, Offline && previewHeld);
            var hour = clock.HourOfDay;
            weather = frame.World.Weather;
            var state = DayLighting.Evaluate(hour, weather);
            waterTime = WaterTimeOverride ?? waterTime + dt;
            sky.Apply(state, waterTime);
            mats.SetNight((float)state.Night, (float)frame.World.EastFeederOutage);
            grading.Apply(state);
            views.Apply(frame, state, sceneCamera.transform.position, dt, waterTime);
            rig.FollowTruck(frame.Truck);

            reflection.Level = (float)frame.ReflectionLevelM;
            water.Apply(q => frame.Tides[q].LevelM, 0.0, weather, waterTime, sceneCamera.transform.position);
            for (var i = 0; i < world.Buoys.Count; i++)
            {
                var rest = world.BuoyRest[i];
                var b = world.Buoys[i];
                b.position = new Vector3(rest.x, water.SurfaceAt(rest.x, rest.y), rest.y);
                b.rotation = Quaternion.Euler(Mathf.Sin(waterTime * 0.9f + i) * 6 * (0.3f + (float)weather.Storm), 0, Mathf.Cos(waterTime * 0.7f + i * 1.3f) * 6 * (0.3f + (float)weather.Storm));
            }

            rig.Tick(dt <= 0 ? 10 : dt);
        }

        void OnDestroy() => Teardown();

        bool tornDown;

        /// <summary>
        /// Stops the platform link and destroys everything the scene made at run time (meshes, materials, the sky, the readout's texture), and
        /// puts the render settings back. OnDestroy calls it; the Editor's stills tool calls it on the object it built in edit mode.
        /// </summary>
        public void Teardown()
        {
            if (tornDown) return;
            tornDown = true;
            reflection?.Dispose();
            grading?.Dispose();
            live?.Dispose();
            water?.Dispose();
            sky?.Dispose();
            world?.Dispose();
            mats?.Dispose();
            if (panel != null) MeshKit.Release(panel.normal.background);
            world = null;
        }

        // ------------------------------------------------------------------------------------------------ the readout

        void Styles()
        {
            if (panel != null) return;
            var bg = new Texture2D(1, 1);
            bg.SetPixel(0, 0, new Color(0.03f, 0.05f, 0.08f, 0.78f));
            bg.Apply();
            panel = new GUIStyle(GUI.skin.box) { alignment = TextAnchor.UpperLeft, richText = true, fontSize = 14, padding = new RectOffset(12, 12, 10, 10), wordWrap = true };
            panel.normal.background = bg;
            panel.normal.textColor = new Color(0.9f, 0.93f, 0.95f);
            small = new GUIStyle(panel) { fontSize = 12, padding = new RectOffset(8, 8, 5, 5) };
        }

        void OnGUI()
        {
            Styles();
            rig?.HandleEvent(Event.current);
            if (Problem != null)
            {
                GUI.Box(new Rect(16, 16, 760, 120), "<b>Harborpulse cannot start</b>\n" + Problem, panel);
                return;
            }

            if (world == null || frame == null) return;
            sb.Clear();
            sb.Append("<size=17><b>HARBORPULSE</b></size>  Port Alder waterfront\n");
            sb.Append(ModeLine()).Append('\n');
            sb.Append("<b>").Append(clock.Label).Append("</b>");
            var beat = Offline ? BeatAt(clock.HourOfDay) : live.Clock.Last?.Beat;
            if (!string.IsNullOrEmpty(beat)) sb.Append("  · ").Append(beat);
            sb.Append("   storm ").Append((weather.Storm * 100).ToString("0")).Append(" %\n");
            if (!Offline) LiveLines();
            sb.Append("\n<b>Tide</b> (median of each quay's fresh gauges)\n");
            foreach (var q in plan.Quays) sb.Append(TideLine(q)).Append('\n');
            var on = 0; var off = 0; var silent = 0; var unknown = 0;
            foreach (var lamp in frame.Lamps.Values)
            {
                switch (lamp.Condition) { case LampCondition.On: on++; break; case LampCondition.Off: off++; break; case LampCondition.Silent: silent++; break; default: unknown++; break; }
            }
            sb.Append("\nStreet lights · on ").Append(on).Append(" · off ").Append(off).Append(" · silent ").Append(silent).Append(" · unknown ").Append(unknown);
            sb.Append("\n").Append(frame?.World.BeatsNote ?? "layout beats (runner gives none)");
            sb.Append("\nOTA: ").Append(HarborCards.RolloutLine(frame));
            sb.Append("\nLamp service: ").Append(frame?.LampService?.Note ?? "not reported");
            sb.Append("\nReplacement: ").Append(HarborCards.ReplacementLine(frame));
            if (!string.IsNullOrEmpty(frame?.ReadProblem)) sb.Append("\n").Append(frame.ReadProblem);
            var text = sb.ToString();
            GUI.Box(new Rect(16, 16, 470, panel.CalcHeight(new GUIContent(text), 470)), text, panel);

            var shots = rig.Shots;
            sb.Clear();
            for (var i = 0; i < shots.Count; i++) sb.Append(i == rig.Current ? "<b>[" : "[").Append(i == 10 ? "F1" : i == 11 ? "F2" : i.ToString(CultureInfo.InvariantCulture)).Append("] ").Append(shots[i].Name).Append(i == rig.Current ? "</b>   " : "   ");
            sb.Append("· drag to orbit, wheel to dolly");
            var shotText = sb.ToString();
            var shotHeight = small.CalcHeight(new GUIContent(shotText), Screen.width - 32);
            GUI.Box(new Rect(16, Screen.height - shotHeight - 12, Screen.width - 32, shotHeight), shotText, small);
            var occupied = new System.Collections.Generic.List<Rect> { new Rect(16, 16, 470, panel.CalcHeight(new GUIContent(text), 470)) };

            foreach (var card in Cards())
            {
                var p = sceneCamera.WorldToScreenPoint(card.At);
                if (p.z <= 0) continue;
                var rect = new Rect(Mathf.Clamp(p.x - 155, 4, Screen.width - 314), Mathf.Clamp(Screen.height - p.y, 4, Screen.height - 250), 310, small.CalcHeight(new GUIContent(card.Text), 310));
                for (var i = 0; i < occupied.Count; i++)
                    if (rect.Overlaps(occupied[i])) { rect.x = Mathf.Clamp(occupied[i].xMax + 6, 4, Screen.width - rect.width - 4); }
                rect.y = Mathf.Min(rect.y, Screen.height - shotHeight - rect.height - 18);
                occupied.Add(rect);
                GUI.Box(rect, card.Text, small);
            }
        }

        string ModeLine()
        {
            switch (clock.Mode)
            {
                case ClockMode.Preview: return "<color=#ffcc55><b>OFFLINE PREVIEW</b> · stand-in values, not platform data</color>";
                case ClockMode.Live:
                    return "<color=#7fe39a><b>LIVE</b></color> · " + (live?.Config != null ? live.Config.Tenant + "@" + live.Config.InstanceId : "") + " · read-only";
                case ClockMode.Stale: return "<color=#ff8a65><b>STALE CLOCK</b> · " + clock.Note + "</color>";
                default: return "<color=#9fb3c8><b>WAITING</b> · " + clock.Note + "</color>";
            }
        }

        void LiveLines()
        {
            if (live.Failure != null)
            {
                sb.Append("<color=#ff8a65>platform: ").Append(live.Failure).Append("</color>\n");
                return;
            }

            sb.Append("platform: ").Append(live.Stage);
            if (live.Bound > 0) sb.Append(" · ").Append(live.Bound).Append('/').Append(layout.Devices.Count).Append(" bound");
            sb.Append('\n');
            var st = live.ObserverStatus;
            if (live.Observed != null)
                sb.Append("measurements ").Append(st.Measurements.State).Append(" · alarms ").Append(st.Alarms.State)
                    .Append(live.Broker != null && live.Broker.State != TokenState.Fresh ? " · token " + live.Broker.State : "").Append('\n');
            foreach (var p in live.BindProblems) sb.Append("<color=#ff8a65>").Append(p).Append("</color>\n");
        }

        string TideLine(QuayPlan q)
        {
            var r = frame.Tides[q.Token];
            var name = q.Name.PadRight(11);
            if (!r.LevelM.HasValue) return name + "<color=#ff8a65>no reading — the water is drawn at datum</color>";
            var age = r.NewestAt.HasValue ? Age(frame.ObservedAt - r.NewestAt.Value) : "";
            var level = r.LevelM.Value.ToString("0.00", CultureInfo.InvariantCulture) + " m";
            if (r.Held) return name + "<color=#ff8a65>" + level + " held · no fresh gauge for " + age + "</color>";
            return name + "<b>" + level + "</b>" + "  (" + r.Contributors + "/" + r.Gauges + " gauges, " + age + ")";
        }

        static string Age(TimeSpan t)
        {
            if (t < TimeSpan.Zero) t = TimeSpan.Zero;
            return t.TotalSeconds < 90 ? (int)t.TotalSeconds + " s" : (int)t.TotalMinutes + " min";
        }

        string BeatAt(double hour)
        {
            var on = new StringBuilder();
            foreach (var b in layout.Beats)
                if (hour >= b.From && hour < b.To)
                    on.Append(on.Length > 0 ? "," : "").Append(b.Name);
            return on.Length > 0 ? on.ToString() : "quiet";
        }

        public System.Collections.Generic.IEnumerable<HarborCard> Cards()
        {
            if (frame == null || world == null) yield break;
            foreach (var r in world.Rigs)
            {
                var distance = r.Placement.Device.Kind == DeviceKind.Pump ? 90 : r.Placement.Device.Kind == DeviceKind.Light ? 100 : 130;
                if ((r.Root.position - sceneCamera.transform.position).sqrMagnitude > distance * distance) continue;
                yield return new HarborCard { At = r["CardAnchor"].position, Text = HarborCards.Device(r.Placement.Device, frame) };
            }
        }
    }
}
