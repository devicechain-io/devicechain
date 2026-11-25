// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Globalization;
using System.Text;
using System.IO;
using System.Text.RegularExpressions;
using System.Linq;
using System.Collections.Generic;
using DeviceChain.Harborpulse.Model.Director;
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
        /// <summary>A labelled CONTROL for a live run: the director reads commands without their recorded cause, so a claim that needs the cause must fall back.</summary>
        public const string NoCauseControlFlag = "-hp-control-no-cause";
        /// <summary>
        /// A rig's fixed eye, <c>-hp-eye x/y/z/lookX/lookY/lookZ/fov</c>: the camera is held there for the whole run, whatever shot the
        /// director calls, so a rig can watch one place through a day. The director, its captions and its recording run as they would.
        /// Command line only: a page address cannot carry it.
        /// </summary>
        public const string EyeFlag = "-hp-eye";
        /// <summary>
        /// REPLAY: <c>-hp-replay DIR</c> plays a recorded live day (a recording directory, or one compact file) with no network, and
        /// <c>-hp-cut c90|c300</c> renders that authored cut of the storyboard over it: every frame is one step of
        /// <c>-hp-render-fps</c> (30), whatever the machine's speed, and is written as a PNG into <c>-hp-render-dir</c> beside
        /// <c>render.json</c> (the segments and their rates), <c>frame_check.json</c> (every caption, shown or not, and the records
        /// behind each claim) and <c>ui_text.txt</c> (every string drawn). The player quits after the last frame.
        /// </summary>
        public const string ReplayFlag = "-hp-replay", CutFlag = "-hp-cut", RenderDirFlag = "-hp-render-dir", RenderFpsFlag = "-hp-render-fps";
        RecordedDay replayDay;
        ReplaySource replaySource;
        CutPlayer cut;
        CutFrame cutFrame;
        string renderDir;
        int renderFps = 30, renderFrame, cutSegment = -1;
        bool renderCaptured = true, renderDone;
        Texture2D renderTexture, seamTexture;
        readonly SortedSet<string> uiText = new SortedSet<string>(StringComparer.Ordinal);
        /// <summary>Whether the scene is replaying a recorded day (no runner, no platform link).</summary>
        public bool Replay => replayDay != null;
        CameraPose fixedEye;
        bool omitCommandCause;

        [Tooltip("Run the labelled offline preview instead of following a runner.")]
        public bool offlinePreview;

        [Tooltip("The harborpulse runner's control address, when neither -dc-runner nor HARBORPULSE_RUNNER is given.")]
        public string runnerUrl = "http://localhost:8090";

        [Tooltip("Preview only: the hour of day the preview opens at.")]
        public double previewStartHour = 16.5;

        [Tooltip("The shaders the scene builds its materials from; assigned by the scene builder so a player build includes them.")]
        public Shader litShader, waterShader, skyShader, wetWellShader;

        [Tooltip("Material assets carrying the URP Lit keyword sets the runtime materials use (emission, no environment reflections, transparent). A player build compiles only the shader_feature variants some material in the build uses; without these the windows, lamps and beacons built at run time never emit.")]
        public Material[] litVariants;

        public Camera sceneCamera;

        HarborLayout layout;
        SitePlan plan;
        PresentationMap map;
        Storyboard storyboard;
        DirectorHost director;
        DirectorCamera directorCamera;
        DirectorView directorView;
        RecordingFiles recording;
        DateTimeOffset recordingEpoch;
        string directorRun, localDigest, digestProblem;
        readonly RecordingStatus recordingStatus = new RecordingStatus();
        string membershipKey, membershipRun, membershipProblem;
        double directorSeconds, lastWorldS = -1;
        readonly System.Diagnostics.Stopwatch directorWall = System.Diagnostics.Stopwatch.StartNew();
        string recordRoot, captureDir;
        double captureEvery, nextCapture, quitAfter = double.PositiveInfinity;
        // -hp-frame-stats: vsync off, and every 10 s the log gets the frames' mean, 95th percentile and worst time
        bool frameStats;
        readonly List<float> frameTimes = new List<float>();
        float frameStatsAt;
        bool engineer, hidden, portrait;

        /// <summary>
        /// Whether the frame is a tall one: asked for (<c>-hp-portrait</c>) or simply drawn taller than wide. One answer for the three things
        /// that differ in a tall frame, so they cannot disagree: the camera's poses (the 9:16 HOME, a well's tall view), where the HUD
        /// writes, and the range at which a well's tags are drawn.
        /// </summary>
        public bool TallFrame => portrait || (sceneCamera != null && sceneCamera.aspect < 1);
        readonly List<TimedFix> fixes = new List<TimedFix>();
        DateTimeOffset lastFix;
        public Func<IDirectorFeed> DirectorFeedFactory { get; set; }
        public static Func<HarborpulseApp, IStoryHud> HudFactory { get; set; }
        public IStoryHud Hud { get; private set; }
        public PresentationMap Map => map;
        public HarborWorld World => world;
        public DirectorView DirectorView => directorView;
        public DirectorCamera DirectorCamera => directorCamera;
        public bool EngineerOverlay => engineer;
        public double DirectorSeconds => directorSeconds;

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

        bool previewFairWeather;

        /// <summary>
        /// A look check of the OFFLINE PREVIEW only: the sky, the light, the rain and the sea are drawn for a dry, calm day in place of the
        /// scenario's weather, so an hour the story's storm always covers (every dusk) can be judged as a clear one. It changes no reading,
        /// no cue and nothing a live run draws, and a still made with it says so in its stamp; a live scene refuses it.
        /// </summary>
        public bool PreviewFairWeather
        {
            get => previewFairWeather;
            set
            {
                if (value && !Offline) throw new InvalidOperationException("the fair-weather look check is the offline preview's; a live scene draws the scenario's weather");
                previewFairWeather = value;
            }
        }

        /// <summary>Preview only: hold the day at an hour (a still frame).</summary>
        public void HoldPreviewAt(double hourOfDay)
        {
            if (!Offline) throw new InvalidOperationException("only the offline preview's clock can be set by hand; a live clock is the runner's");
            model = new HarborObservedModel(layout);
            ResetDirector();
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

        public void RenderReflectionNow()
        {
            // the stills path never runs Update: bring the near detail to this camera before anything is drawn
            world?.Detail.Refresh(sceneCamera.transform.position, true);
            // and the wells' tags: the last Tick decided them for the camera it had, which a still may have moved since
            views?.FrameWells(sceneCamera.transform.position, sceneCamera.transform.forward, TallFrame);
            reflection?.RenderNow();
        }
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
                map = new PresentationMap(layout);
                storyboard = Storyboard.Parse(Resources.Load<TextAsset>("storyboard").text, map);
                // The read-only observer adapter: live reads come from whichever session is current (a runner restart replaces it).
                if (DirectorFeedFactory == null) DirectorFeedFactory = () => new DirectorObserver(layout, () => live?.DirectorReads);
                var options = Options(out var optionProblem);
                if (optionProblem != null) throw new FormatException(optionProblem);
                localDigest = LayoutDigest.Override(LayoutDigest.Compute(text.bytes), CommandLineArgs.Get(options, "-hp-layout-digest-override", out _));
                portrait = Array.IndexOf(options, "-hp-portrait") >= 0;
                var eye = CommandLineArgs.Get(options, EyeFlag, out _);
                if (eye != null) fixedEye = CameraMath.ParsePose(eye);
                recordRoot = CommandLineArgs.Get(options, "-hp-record-dir", out _) ?? Path.Combine(Application.persistentDataPath, "recordings");
                captureDir = CommandLineArgs.Get(options, "-hp-capture-dir", out _);
                captureEvery = Number(options, "-hp-capture-every", 10, .1, 86400);
                frameStats = Array.IndexOf(options, "-hp-frame-stats") >= 0;
                if (frameStats && Application.isPlaying) { QualitySettings.vSyncCount = 0; Application.targetFrameRate = -1; }
                quitAfter = Number(options, "-hp-quit-after-sim", double.PositiveInfinity, 0, 100000);
                var resolution = CommandLineArgs.Get(options, "-hp-resolution", out _);
                if (resolution != null)
                {
                    var size = resolution.ToLowerInvariant().Split('x');
                    if (size.Length != 2 || !int.TryParse(size[0], out var width) || !int.TryParse(size[1], out var height) || width < 320 || height < 180 || width > 8192 || height > 8192)
                        throw new FormatException("-hp-resolution requires WxH in 320x180..8192x8192");
                    Screen.SetResolution(width, height, false);
                }
                else if (portrait) Screen.SetResolution(1080, 1920, false);
                if (captureDir != null) Directory.CreateDirectory(captureDir);
                plan = SitePlan.Build(layout);
                model = new HarborObservedModel(layout);
                mats = new HarborMaterials(litShader);
                mats.CreateWetWell(wetWellShader != null ? wetWellShader : Shader.Find("Harborpulse/WetWellWater"));
                world = HarborWorld.Build(map, layout, mats, transform);
                sky = new HarborSkyController(transform, skyShader);
                water = new HarborWaterController(transform, waterShader, map, world, 9000);
                if (sceneCamera == null) sceneCamera = Camera.main;
                if (sceneCamera == null) throw new InvalidOperationException("the scene has no camera");
                sceneCamera.farClipPlane = 3600;
                sceneCamera.nearClipPlane = 0.3f;
                rig = new HarborCameraRig(sceneCamera, map);
                directorCamera = new DirectorCamera(sceneCamera, map, portrait);
                Hud = HudFactory?.Invoke(this);
                grading = new HarborGrading(transform, sceneCamera);
                reflection = new PlanarWaterReflection(transform, sceneCamera);
                views = new HarborViews(world, mats);
            }
            catch (Exception e)
            {
                Problem = e.Message;
                Debug.LogError(DemoIdentity.LogPrefix + "the scene cannot start: " + e);
                return;
            }

            var args = Options(out _);
            var replayPath = CommandLineArgs.Get(args, ReplayFlag, out _);
            if (replayPath != null) { StartReplay(args, replayPath); return; }
            Offline = offlinePreview || Array.IndexOf(args, OfflineFlag) >= 0 || Environment.GetEnvironmentVariable(OfflineEnv) == "1";
            if (Offline) StartPreview(args);
            else StartLive(args);
        }

        /// <summary>The player's options: its command line, or in a browser (which has none) the ones its page address carries (<see cref="WebOptions"/>).</summary>
        static string[] Options(out string problem)
        {
            problem = null;
            return Application.platform == RuntimePlatform.WebGLPlayer ? WebOptions.FromUrl(Application.absoluteURL, out problem) : Environment.GetCommandLineArgs();
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

        void StartReplay(string[] args, string path)
        {
            try
            {
                if (File.Exists(path)) using (var one = new StreamReader(path)) replayDay = RecordedDay.Load(one);
                else using (var observed = new StreamReader(Path.Combine(path, "observed.ndjson"))) using (var worldSamples = new StreamReader(Path.Combine(path, "world.ndjson")))
                    replayDay = RecordedDay.Load(observed, worldSamples);
                replaySource = new ReplaySource(replayDay, layout);
                cut = new CutPlayer(storyboard, CommandLineArgs.Get(args, CutFlag, out _) ?? "c90", replayDay, map);
                renderFps = (int)Number(args, RenderFpsFlag, 30, 1, 120);
                renderDir = CommandLineArgs.Get(args, RenderDirFlag, out _);
                recordingEpoch = replayDay.Epoch;
                if (Application.isPlaying) { QualitySettings.vSyncCount = 0; Application.targetFrameRate = -1; Time.captureFramerate = renderFps; }
                if (renderDir != null)
                {
                    Directory.CreateDirectory(renderDir);
                    File.WriteAllText(Path.Combine(renderDir, "render.json"), cut.RenderJson(renderFps, Screen.width, Screen.height));
                    if (Application.isPlaying) StartCoroutine(CaptureFrames());
                }
                PlatformLog.Info("replay · no platform · " + replayDay.Run + " · cut " + cut.Cut.Id + " · " + cut.DurationS.ToString("0.#", CultureInfo.InvariantCulture) + " s at " + renderFps + " fps");
            }
            catch (Exception e) when (e is FormatException || e is IOException || e is UnauthorizedAccessException)
            {
                replayDay = null; Problem = "the recording cannot be replayed: " + e.Message;
                Debug.LogError(DemoIdentity.LogPrefix + Problem);
                if (Application.isBatchMode || CommandLineArgs.Get(args, RenderDirFlag, out _) != null) Application.Quit(2);
            }
        }

        /// <summary>
        /// One frame of the cut. The show clock is the frame's number over the frame rate, never the machine's clock; the recorded
        /// instant and the camera come from the cut player; and the observed model is rebuilt at each shot (the cut jumps through
        /// the day) and led in over the half minute before it, so alarms, command history and pump evidence stand as they did.
        /// </summary>
        bool ReplayStep(float dt, out IHarborSource source, out DateTimeOffset now)
        {
            source = replaySource; now = default;
            if (renderDone || (renderDir != null && !renderCaptured)) return false;
            var showS = renderFrame / (double)renderFps;
            if (showS >= cut.DurationS) { FinishRender(); return false; }
            cutFrame = cut.Frame(showS);
            if (cutFrame.Segment != cutSegment)
            {
                if (cutSegment >= 0 && renderTexture != null)
                {
                    if (seamTexture == null) seamTexture = new Texture2D(renderTexture.width, renderTexture.height, TextureFormat.RGB24, false);
                    Graphics.CopyTexture(renderTexture, seamTexture);
                }
                cutSegment = cutFrame.Segment; model = new HarborObservedModel(layout); fixes.Clear(); lastFix = default;
                for (var t = Math.Max(0, cutFrame.RecordedWallS - 30); t < cutFrame.RecordedWallS; t += 0.5)
                { replaySource.Seek(t); model.Update(replaySource, new ClockReading(replayDay.SimAt(t) * 3600, ClockMode.Live, ""), replayDay.At(t)); }
            }
            replaySource.Seek(cutFrame.RecordedWallS);
            directorSeconds = cutFrame.RecordedWallS; now = replayDay.At(cutFrame.RecordedWallS);
            clock = new ClockReading(cutFrame.Sim * 3600, ClockMode.Live, "");
            // The camera first: the near detail, the wells' tags and the HUD's world tags are decided for this frame's eye.
            var from = TallFrame ? map.Tall(cutFrame.From) : cutFrame.From; var to = TallFrame ? map.Tall(cutFrame.To) : cutFrame.To;
            Vector3 fromAt = DirectorCamera.V(from.Camera), fromLook = DirectorCamera.V(from.Look), toAt = DirectorCamera.V(to.Camera), toLook = DirectorCamera.V(to.Look);
            if (cutFrame.FollowDevice != null && map.Anchors.TryGetValue(cutFrame.FollowDevice, out var anchor))
            {
                var shift = new Vector3((float)anchor.X - toLook.x, 0, (float)anchor.Z - toLook.z);
                fromAt += shift; fromLook += shift; toAt += shift; toLook += shift;
            }
            var move = (float)cutFrame.Move; var at = Vector3.Lerp(fromAt, toAt, move);
            sceneCamera.transform.SetPositionAndRotation(at, Quaternion.Slerp(Quaternion.LookRotation(fromLook - fromAt), Quaternion.LookRotation(toLook - toAt), move));
            sceneCamera.fieldOfView = Mathf.Lerp((float)from.VerticalFov, (float)to.VerticalFov, move);
            return true;
        }

        void ApplyCut()
        {
            directorView = DirectorView.ForCut(cutFrame);
            Hud?.Apply(directorView, "RECORDED", cutFrame.Sim, cutFrame.RateChip, HudModel.Tags(layout, frame, sceneCamera.aspect < 1 ? 2 : 4),
                string.Join(" | ", new[] { digestProblem }.Concat(map.ReadinessWarnings).Where(p => !string.IsNullOrEmpty(p))));
            foreach (var text in new[] { directorView.Caption, directorView.Overlay, directorView.UpNext, cutFrame.RateChip, digestProblem }.Concat(directorView.Rail.Select(n => n.Label + " (" + n.Tag + ")"))
                .Concat(HudModel.Tags(layout, frame).Select(t => t.Text)))
                if (!string.IsNullOrEmpty(text)) uiText.Add(text.Replace("\n", " / "));
            renderCaptured = false;
        }

        System.Collections.IEnumerator CaptureFrames()
        {
            var end = new WaitForEndOfFrame();
            while (!renderDone)
            {
                yield return end;
                if (renderCaptured || cutFrame == null) continue;
                if (renderTexture == null || renderTexture.width != Screen.width || renderTexture.height != Screen.height)
                { MeshKit.Release(renderTexture); renderTexture = new Texture2D(Screen.width, Screen.height, TextureFormat.RGB24, false); }
                renderTexture.ReadPixels(new Rect(0, 0, Screen.width, Screen.height), 0, 0, false); renderTexture.Apply(false);
                File.WriteAllBytes(Path.Combine(renderDir, "frame-" + renderFrame.ToString("000000", CultureInfo.InvariantCulture) + ".png"), renderTexture.EncodeToPNG());
                renderFrame++; renderCaptured = true;
            }
        }

        void FinishRender()
        {
            renderDone = true;
            if (renderDir != null)
            {
                File.WriteAllText(Path.Combine(renderDir, "frame_check.json"), cut.TraceJson());
                File.WriteAllLines(Path.Combine(renderDir, "ui_text.txt"), uiText);
                PlatformLog.Info("replay · cut " + cut.Cut.Id + " rendered: " + renderFrame + " frames");
            }
            if (Application.isPlaying) Application.Quit();
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
            omitCommandCause = Array.IndexOf(args, NoCauseControlFlag) >= 0;
            live = new HarborLiveSession(layout, liveSettings) { OmitCommandCause = omitCommandCause };
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
            if (Replay && renderDir != null)
            {
                // A render that cannot draw a frame stops: a cut with a hole in it must not look finished.
                try { Tick(Time.deltaTime); }
                catch (Exception e)
                {
                    Debug.LogError(DemoIdentity.LogPrefix + "the render failed at frame " + renderFrame + ": " + e);
                    renderDone = true; Application.Quit(3);
                }
                return;
            }
            Tick(Time.deltaTime);
            if (frameStats) FrameStats(Time.unscaledDeltaTime);
        }

        void FrameStats(float dt)
        {
            frameTimes.Add(dt * 1000); frameStatsAt += dt;
            if (frameStatsAt < 10) return;
            frameTimes.Sort();
            var mean = frameTimes.Average();
            var p95 = frameTimes[Math.Min(frameTimes.Count - 1, (int)(frameTimes.Count * 0.95))];
            Debug.Log(DemoIdentity.LogPrefix + "frame time over " + frameTimes.Count + " frames at " + Screen.width + "x" + Screen.height + ": mean "
                + mean.ToString("0.00", CultureInfo.InvariantCulture) + " ms, p95 " + p95.ToString("0.00", CultureInfo.InvariantCulture) + " ms, max "
                + frameTimes[frameTimes.Count - 1].ToString("0.00", CultureInfo.InvariantCulture) + " ms");
            frameTimes.Clear(); frameStatsAt = 0;
        }

        public void Tick(float dt, DateTimeOffset? wallNow = null)
        {
            var now = wallNow ?? DateTimeOffset.UtcNow;
            IHarborSource source;
            if (Replay)
            {
                if (!ReplayStep(dt, out source, out now)) return;
            }
            else if (Offline)
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
            if (Replay) { }
            else if (!Offline && modelRun.HasValue && modelRun != started)
            {
                // A new runner can bind different platform tokens. Discard the observer as well as the reducers.
                live.Dispose();
                live = new HarborLiveSession(layout, liveSettings) { OmitCommandCause = omitCommandCause }; live.Start();
                model = new HarborObservedModel(layout); modelRun = null;
                source = live.Source; clock = live.Clock.Read(now);
            }
            else if (!Offline && started.HasValue && !modelRun.HasValue)
            {
                model = new HarborObservedModel(layout); modelRun = started;
            }
            frame = model.Update(source, clock, now, Offline && previewHeld);
            if (!Offline && !Replay) ApplyMembership(source, now);
            var hour = clock.HourOfDay;
            weather = PreviewFairWeather ? new Weather(0, 0) : frame.World.Weather;
            var state = DayLighting.Evaluate(hour, weather);
            waterTime = WaterTimeOverride ?? waterTime + dt;
            sky.Apply(state, waterTime);
            mats.SetNight((float)state.Night, (float)frame.World.EastFeederOutage);
            mats.SetRain(state);
            grading.Apply(state);
            views.Apply(frame, state, sceneCamera.transform.position, dt, waterTime, weather, sceneCamera.transform.forward, TallFrame);
            if (Replay) ApplyCut(); else TickDirector(source, now, dt);
            ApplyMappedTruck(source, now);
            world.Detail.Refresh(sceneCamera.transform.position);

            reflection.Level = (float)frame.ReflectionLevelM;
            water.Apply(q => frame.Tides[q].LevelM, 0.0, weather, waterTime, sceneCamera.transform.position);
            for (var i = 0; i < world.Buoys.Count; i++)
            {
                var rest = world.BuoyRest[i];
                var b = world.Buoys[i];
                b.position = new Vector3(rest.x, water.SurfaceAt(rest.x, rest.y), rest.y);
                b.rotation = Quaternion.Euler(Mathf.Sin(waterTime * 0.9f + i) * 6 * (0.3f + (float)weather.Storm), 0, Mathf.Cos(waterTime * 0.7f + i * 1.3f) * 6 * (0.3f + (float)weather.Storm));
            }

            if (Replay) renderFrame += renderDir == null ? 1 : 0;
            else if (director != null) { directorCamera.Portrait = TallFrame; directorCamera.Tick(directorView, director, directorSeconds, dt, fixes); }
            if (fixedEye != null)
            {
                var eyeAt = DirectorCamera.V(fixedEye.Camera);
                sceneCamera.transform.SetPositionAndRotation(eyeAt, Quaternion.LookRotation(DirectorCamera.V(fixedEye.Look) - eyeAt));
                sceneCamera.fieldOfView = (float)fixedEye.VerticalFov;
            }
            if (Replay) return;
            if (Application.isPlaying && captureDir != null && directorSeconds >= nextCapture)
            {
                nextCapture = directorSeconds + captureEvery;
                var time = HudModel.Clock(clock.SimSeconds / 3600).Replace(':', 'h');
                var page = directorView?.Page ?? "ambient";
                ScreenCapture.CaptureScreenshot(Path.Combine(captureDir, "harborpulse-d" + clock.Day + "-" + time + "-page-" + page + "-" + directorSeconds.ToString("000000.00", CultureInfo.InvariantCulture) + ".png"));
            }
            // Quit only on a caption boundary: a caption cut by the end of the recording would be on screen for less than 3 s.
            if (Application.isPlaying && clock.SimSeconds / 3600 >= quitAfter && HudModel.QuitAllowed(directorView, directorSeconds)) Application.Quit();
        }

        void ResetDirector()
        {
            recording?.Dispose(); recording = null; director = null; directorRun = null;
            fixes.Clear(); lastFix = default; directorSeconds = 0; lastWorldS = -1; nextCapture = 0; directorWall.Restart();
        }
        void ApplyMembership(IHarborSource source, DateTimeOffset now)
        {
            var run = source.Status?.StartedAt?.ToString("O", CultureInfo.InvariantCulture);
            if (membershipRun != run) { membershipKey = null; membershipRun = run; }
            if (!PresentationMembership.TryRead(layout, live.DirectorReads, id => source.TryToken(id, out var token) ? token : null,
                now, out var members, out membershipProblem)) return;
            var key = string.Join("|", members.OrderBy(p => p.Key).Select(p => p.Key + "=" + p.Value));
            if (membershipKey == key) return;
            var next = new PresentationMap(layout, members);
            var moved = map.Anchors.Any(p => next.Anchors[p.Key].X != p.Value.X || next.Anchors[p.Key].Z != p.Value.Z);
            map = next; membershipKey = key; director?.SetMap(map);
            if (!moved) return;
            // Rebuild the ground apertures and rig geometry too: a relocated pump must retain its cut-away.
            water.Dispose(); world.Dispose();
            world = HarborWorld.Build(map, layout, mats, transform);
            water = new HarborWaterController(transform, waterShader, map, world, 9000);
            views = new HarborViews(world, mats);
            rig = new HarborCameraRig(sceneCamera, map);
            directorCamera = new DirectorCamera(sceneCamera, map, portrait);
        }
        void ApplyMappedTruck(IHarborSource source, DateTimeOffset now)
        {
            if (source.TryLocation("PAW-TRUCK-1", out var location) && location.Latitude.HasValue && location.Longitude.HasValue && location.OccurredAt != lastFix)
            {
                var atS = (location.OccurredAt - recordingEpoch).TotalSeconds;
                if (fixes.Count == 0 || atS > fixes[fixes.Count - 1].WallS)
                    fixes.Add(new TimedFix(atS, new LatLon(location.Latitude.Value, location.Longitude.Value)));
                lastFix = location.OccurredAt;
            }
            try
            {
                // Both consumers sample this monotonic instant. Keep the predecessor and
                // any future fixes so validation/interpolation never revisits a whole run.
                var expired = 0;
                while (expired + 1 < fixes.Count && fixes[expired + 1].WallS <= directorSeconds) expired++;
                if (expired > 0) fixes.RemoveRange(0, expired);
                var drawn = map.TruckAt(fixes, directorSeconds).At;
                var truck = world.Truck.Root;
                truck.position = new Vector3((float)drawn.X, (float)SitePlan.DeckHeightM, (float)drawn.Z);
            }
            catch (FormatException e) { digestProblem = "truck mapping: " + e.Message; }
        }
        void TickDirector(IHarborSource source, DateTimeOffset now, float dt)
        {
            var run = Offline ? "offline-preview" : source.Status?.StartedAt?.ToString("O", CultureInfo.InvariantCulture) ?? "waiting";
            if (director == null || directorRun != run)
            {
                // Second 0 of the recording is the instant its monotonic timer starts. The frame's own "now" was read before this
                // frame's model and views ran, so an epoch taken from it put every later "now" that much behind the system clock
                // the polls stamp their successes with.
                ResetDirector(); recordingEpoch = Offline || !Application.isPlaying ? now : DateTimeOffset.UtcNow; directorRun = run;
                if (!Offline && source.Status?.StartedAt != null)
                {
                    recording = recordingStatus.Establish(() => new RecordingFiles(recordRoot, source.Status.StartedAt.Value, now));
                }
                director = new DirectorHost(storyboard, layout, map, DirectorFeedFactory?.Invoke(), recording?.Recorder);
            }
            // Live: show seconds are the wall's, read from a monotonic stopwatch. Frame deltas are capped by Unity
            // (maximumDeltaTime), so a slow frame would let the director's clock fall behind the platform's records,
            // and a record stamped after "now" is never consumed. The preview keeps its own scripted clock.
            if (Offline) directorSeconds += Math.Max(0, dt);
            else directorSeconds = Math.Max(directorSeconds, directorWall.Elapsed.TotalSeconds);
            var matches = Offline || LayoutDigest.Matches(localDigest, source.Status?.LayoutDigest, out digestProblem);
            var sim = clock.SimSeconds / 3600;
            var defects = (source.Status?.Defects ?? Array.Empty<string>()).ToList();
            if (Array.IndexOf(Environment.GetCommandLineArgs(), "-hp-layout-digest-override") >= 0) defects.Add("CONTROL layout digest override");
            if (omitCommandCause) defects.Add("CONTROL command cause not read");
            controlRun = defects.Count > 0;
            var sample = new DirectorSample(run, sim, directorSeconds, clock.Mode == ClockMode.Stale || clock.Mode == ClockMode.Waiting, matches, defects);
            directorView = director.Tick(sample, new DirectorInputs { Run = run, RecordingEpoch = recordingEpoch, Now = recordingEpoch.AddSeconds(directorSeconds), Sim = sim, Source = source, Frame = frame });
            if (recording != null && directorSeconds - lastWorldS >= 1)
            {
                lastWorldS = directorSeconds;
                // A typed /status sample, never config or credentials. simHour also maps every
                // director show second to the runner clock without changing the U2a wire format.
                recording.Recorder.World(directorSeconds, StatusSample(source.Status, sim, source));
            }
            RefreshHud();
        }
        public void RefreshHud()
        {
            if (directorView == null) return;
            var sim = clock.SimSeconds / 3600;
            // A planted defect or a scene injection makes the whole run a control, whichever banner currently wins precedence.
            var control = controlRun || directorView.Banner.StartsWith("TEST CONTROL", StringComparison.Ordinal);
            Hud?.Apply(directorView, Offline ? "OFFLINE PREVIEW" : control ? "TEST CONTROL" : "LIVE", sim,
                Offline ? "x" + previewSpeed.ToString("0.#", CultureInfo.InvariantCulture) : "x1",
                HudModel.Tags(layout, frame, sceneCamera.aspect < 1 ? 2 : 4),
                string.Join(" | ", new[] { digestProblem, recordingStatus.Problem, recording?.Problem, membershipProblem,
                    string.Join(" | ", map.ReadinessWarnings), Offline ? null : frame.ReadProblem }.Where(p => !string.IsNullOrEmpty(p))));
        }
        bool controlRun;
        DateTimeOffset previewWall;
        public void BeginDirectorPreview(double absoluteHour)
        {
            if (!Offline) throw new InvalidOperationException("offline preview only");
            ResetDirector(); model = new HarborObservedModel(layout); preview = new PreviewSource(layout);
            previewHeld = false; previewSpeed = 1; previewSimS = absoluteHour * 3600;
            previewWall = DateTimeOffset.UtcNow; Tick(0, previewWall);
        }
        public void AdvanceDirectorPreview(float wallSeconds)
        { previewWall = previewWall.AddSeconds(wallSeconds); Tick(wallSeconds, previewWall); }
        public double PreviewSecondsPerHour => layout.DayLengthSeconds / 24;
        static string StatusSample(ScenarioStatus s, double sim, IHarborSource source) => DirectorWire.Json(w =>
        {
            w.WriteStartObject(); w.WriteString("sample", "/status"); w.WriteNumber("simHour", sim);
            if (s != null)
            {
                w.WriteString("state", s.RunnerState); w.WriteString("startedAt", s.StartedAt?.ToString("O", CultureInfo.InvariantCulture));
                w.WriteString("layoutDigest", s.LayoutDigest); w.WriteString("beat", s.Beat); w.WriteNumber("day", s.Day);
                w.WriteStartArray("defects"); foreach (var defect in s.Defects) w.WriteStringValue(defect); w.WriteEndArray();
            }
            // What a replay draws and no keyed observation holds: each alarm's severity and acknowledgement, and the truck's fix.
            w.WriteStartArray("alarms");
            foreach (var a in source.Alarms)
            { w.WriteStartObject(); w.WriteString("device", a.Device); w.WriteString("key", a.Key); w.WriteString("severity", a.Severity); w.WriteBoolean("acknowledged", a.Acknowledged); w.WriteEndObject(); }
            w.WriteEndArray();
            if (source.TryLocation("PAW-TRUCK-1", out var fix) && fix.Latitude.HasValue && fix.Longitude.HasValue)
            {
                w.WriteStartObject("truck"); w.WriteNumber("lat", fix.Latitude.Value); w.WriteNumber("lon", fix.Longitude.Value);
                if (fix.Heading.HasValue) w.WriteNumber("heading", fix.Heading.Value);
                if (fix.Speed.HasValue) w.WriteNumber("speed", fix.Speed.Value);
                w.WriteString("at", fix.OccurredAt.ToString("O", CultureInfo.InvariantCulture)); w.WriteEndObject();
            }
            w.WriteEndObject();
        });

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
            Hud?.Dispose();
            MeshKit.Release(renderTexture); MeshKit.Release(seamTexture);
            recording?.Dispose();
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
            var input = Event.current;
            if (input.type == EventType.KeyDown)
            {
                if (input.keyCode == KeyCode.E) { engineer = !engineer; input.Use(); }
                else if (input.keyCode == KeyCode.H) { hidden = !hidden; if (Hud != null) Hud.Visible = !hidden; input.Use(); }
                else if (input.keyCode == KeyCode.D && director != null) { director.Enabled = !director.Enabled; directorCamera.AdoptPreset(); if (director.Enabled) directorCamera.Rejoin(directorSeconds); input.Use(); }
            }
            if (director != null)
            {
                directorCamera.HandleEvent(input, director, directorSeconds);
                if (!director.Enabled)
                {
                    var preset = input.type == EventType.KeyDown && (input.keyCode >= KeyCode.Alpha0 && input.keyCode <= KeyCode.Alpha9 || input.keyCode == KeyCode.F1 || input.keyCode == KeyCode.F2);
                    if (preset) { rig.HandleEvent(input); rig.Select(rig.Current, true); directorCamera.AdoptPreset(); }
                }
            }
            if (Problem != null)
            {
                GUI.Box(new Rect(16, 16, 760, 120), "<b>Harborpulse cannot start</b>\n" + Problem, panel);
                return;
            }

            if (Replay && cutFrame != null && cutFrame.Dissolve > 0 && seamTexture != null && Event.current.type == EventType.Repaint)
            {
                // the seam: the last frame of the shot before, fading out over the new one
                var tint = GUI.color; GUI.color = new Color(1, 1, 1, (float)cutFrame.Dissolve);
                GUI.DrawTexture(new Rect(0, 0, Screen.width, Screen.height), seamTexture, ScaleMode.StretchToFill, false); GUI.color = tint;
            }
            if (world == null || frame == null || !engineer || hidden) return;
            sb.Clear();
            sb.Append("<size=17><b>HARBORPULSE</b></size>  Port Alder waterfront\n");
            sb.Append(ModeLine()).Append('\n');
            foreach (var problem in new[] { recordingStatus.Problem, recording?.Problem, membershipProblem }.Concat(map.ReadinessWarnings).Where(p => !string.IsNullOrEmpty(p)))
                sb.Append(problem).Append('\n');
            sb.Append("<b>presentation scale</b> ? compact horizontal layout; heights in metres\n");
            if (director != null)
                foreach (var row in director.Scheduler.Log.Skip(Math.Max(0, director.Scheduler.Log.Count - 6)))
                    sb.Append(row.ShowS.ToString("0.0", CultureInfo.InvariantCulture)).Append(" ").Append(row.Page).Append(" ").Append(row.Kind).Append(" ").Append(row.Value).Append('\n');
            var nearest = world.Rigs.OrderBy(r => (r.Root.position - sceneCamera.transform.position).sqrMagnitude).FirstOrDefault();
            if (nearest != null) sb.Append(map.Disclosure(nearest.Placement.Device.Token, nearest.Placement.Device.Kind == DeviceKind.Truck && fixes.Count > 0 ? fixes[fixes.Count - 1].Position : (LatLon?)null)).Append('\n');
            sb.Append("<b>").Append(clock.Label).Append("</b>");
            var beat = Offline || Replay ? BeatAt(clock.HourOfDay) : live.Clock.Last?.Beat;
            if (!string.IsNullOrEmpty(beat)) sb.Append("  · ").Append(beat);
            sb.Append("   storm ").Append((weather.Storm * 100).ToString("0")).Append(" %\n");
            if (!Offline && !Replay) LiveLines();
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
            for (var i = 0; i < shots.Count; i++) sb.Append(i == rig.Current ? "<b>[" : "[").Append(i == 10 ? "F1" : i == 11 ? "F2" : i == 12 ? "F3" : i.ToString(CultureInfo.InvariantCulture)).Append("] ").Append(shots[i].Name).Append(i == rig.Current ? "</b>   " : "   ");
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
                    if (Replay) return "<color=#9fb3c8><b>RECORDED</b> · " + replayDay.Run + " · cut " + cut.Cut.Id + "</color>";
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
                yield return new HarborCard { At = r["CardAnchor"].position, Text = Regex.Replace(HarborCards.Device(r.Placement.Device, frame), @"(\d{2}:\d{2}:\d{2}\.\d{3})\d+", "$1") };
            }
        }
    }
}
