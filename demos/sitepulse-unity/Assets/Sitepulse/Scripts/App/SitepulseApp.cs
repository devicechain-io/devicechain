// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using System.Threading;
using System.Threading.Tasks;
using System.Collections.Generic;
using DeviceChain.Sdk;
using DeviceChain.Sitepulse.Domain;
using DeviceChain.Sitepulse.DevicePlane;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Recording;
using DeviceChain.Sitepulse.Replay;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;
using DeviceChain.Sitepulse.Visuals;
using UnityEngine;
using UnityEngine.InputSystem;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>
    /// The composition root. It picks the mode once, at startup, from <c>-sitepulse-mode</c> (an
    /// Editor-only override applies when the flag is absent), puts the mode badge on screen, and
    /// builds whichever world that mode names. Choreographed leaves the scene as it is, illustrative
    /// cards and all. Live gives the cards an observed source for good (an illustrative value on a Live
    /// screen is the one thing the demo must never do), reads the runner's config, binds the scene's 19
    /// devices to the platform, starts the 19 device sessions, and starts the observer, whose reports are
    /// all the cards show, and records the run (on by default). Replay plays a recorded run back through
    /// the same scene by a root of its own that holds no network object (<see cref="ReplayRoot"/>), or renders shots from it offline. A mode
    /// that cannot start shows why and stays stopped.
    ///
    /// Runs before the data layer's first frame so its cards are never drawn for a frame in a mode
    /// that forbids them.
    /// </summary>
    [DefaultExecutionOrder(-900)]
    public sealed class SitepulseApp : MonoBehaviour
    {
        public enum EditorModeOverride { None, Choreographed, Live, Replay }

        [Tooltip("The data layer: Choreographed shows its illustrative cards, Live feeds it observed values, and Replay or a mode error switches it off.")]
        public IotOverlay overlay;
        [Tooltip("The preview fleet: its choreography file names the scene's machines, and the machines are tinted when they do not bind.")]
        public QuarryFleetPreview fleet;
        [Tooltip("What the fleet plays in Live mode: the six-hauler loop with no scripted refuel, so no machine "
                 + "refuels on a script. Required in Live; it is never replaced by the Choreographed file.")]
        public TextAsset liveChoreography;
        [Tooltip("Editor only, and only when -sitepulse-mode is not on the command line.")]
        [SerializeField] EditorModeOverride editorMode = EditorModeOverride.None;

        const string NoObserver = "observer not started";

        SitepulseHud hud;
        SitepulseMode mode;
        bool started;
        bool? previousRunInBackground;
        CancellationTokenSource cts;

        // Live
        TokenBroker broker;
        ReadinessBoard board;
        string tenant;
        int shownVersion = -1;
        string shownToken;
        bool stopped, blocked;
        readonly HashSet<string> greyed = new HashSet<string>(StringComparer.Ordinal);
        DeviceFleet plane;
        RigPoseSource poses;
        TaskDirector director;
        RefuelVignette vignette;
        PresenterControls presenter;

        // the proof drawer's log (fed by the observer and the device timeline) and the route highlight's source: Live's own, as a replay has its own
        readonly ProofLog proofLog = new ProofLog();
        readonly RouteCache routeCache = new RouteCache();
        readonly Dictionary<string, string> idByToken = new Dictionary<string, string>(StringComparer.Ordinal);

        // the feature video's one live take (-sitepulse-video-run): null in every other run
        VideoRun videoRun;
        bool videoRunRequested;
        string focusShown;
        SiteGeometry site;
        Timeline timeline;
        string tasksShown;
        bool showTimeline = true;
        RunnerConfig runnerConfig;
        PlatformObserver observer;
        ObservedState observed;
        readonly ObserverStatus observerStatus = new ObserverStatus();
        int shownObserver = -1;
        bool showSimulation, showAllDevices;
        bool shownAllDevices;
        float nextSimulation;
        float nextRender;
        const float RenderEverySeconds = 0.25f;
        float nextStatusLog;
        const float StatusLogEverySeconds = 10f;

        // recording (Live): on by default
        RecordOptions recordOptions;
        LiveRecorder liveRecorder;
        readonly SampleRelay sampleRelay = new SampleRelay();

        // acceptance (-sitepulse-acceptance): null in every normal run
        AcceptanceOptions acceptance;
        PhaseAProbe probe;
        EmittedSampleLog sampleLog;
        string acceptanceDir;
        DateTimeOffset appStartedAt;
        bool quitIssued;

        public SitepulseMode Mode => mode;

        void Awake()
        {
            cts = new CancellationTokenSource();
            hud = new SitepulseHud(transform);
            // the data layer keeps its cards off the HUD
            if (overlay != null)
            {
                overlay.Obstacles = hud.Obstacles;
                overlay.Picked += OnPicked;
            }

            appStartedAt = DateTimeOffset.UtcNow;
            var args = Environment.GetCommandLineArgs();
            var parsed = SitepulseModes.FromCommandLine(args, out var present);
            var accepting = AcceptanceFlags.FromCommandLine(args);
            if (!parsed.Ok || !accepting.Ok)
            {
                var why = parsed.Ok ? accepting.Error : accepting.Ok ? parsed.Error : parsed.Error + "\n" + accepting.Error;
                HideCards();
                hud.SetBadge("MODE ERROR · nothing is running", BadgeTone.Error);
                hud.ShowError("The mode could not be chosen", why);
                PlatformLog.Error(why);
                AbortAcceptance(args, why);
                return;
            }

            acceptance = accepting.Value;
            mode = parsed.Value;
            if (acceptance != null && (mode != SitepulseMode.Live || !present))
            {
                const string need = "-sitepulse-acceptance needs -sitepulse-mode live";
                HideCards();
                hud.SetBadge("MODE ERROR · nothing is running", BadgeTone.Error);
                hud.ShowError("The mode could not be chosen", need);
                PlatformLog.Error(need);
                acceptance = null;
                AbortAcceptanceWith(accepting.Value, need);
                return;
            }

            videoRunRequested = VideoRunFlags.Requested(args);
            if (videoRunRequested && (mode != SitepulseMode.Live || !present || acceptance != null))
            {
                var need = acceptance != null
                    ? $"{VideoRunFlags.Flag} is a take of its own: it cannot be combined with {AcceptanceFlags.Flag}"
                    : $"{VideoRunFlags.Flag} needs -sitepulse-mode live";
                HideCards();
                hud.SetBadge("MODE ERROR · nothing is running", BadgeTone.Error);
                hud.ShowError("The mode could not be chosen", need);
                PlatformLog.Error(need);
                videoRunRequested = false;
                return;
            }

            if (!present && Application.isEditor && editorMode != EditorModeOverride.None)
                mode = (SitepulseMode)Enum.Parse(typeof(SitepulseMode), editorMode.ToString());
            // a replay or a render is asked for by its own flags, and they are a mode of their own: a line that names another mode and
            // also asks for a replay is contradicting itself, and is refused rather than guessed at
            if (ReplayOptions.Wanted(args))
            {
                if (present && mode != SitepulseMode.Replay)
                {
                    const string clash = "the replay flags (-sitepulse-replay, -sitepulse-render, -sitepulse-out, -sitepulse-replay-start) need -sitepulse-mode replay, or no -sitepulse-mode at all";
                    HideCards();
                    hud.SetBadge("MODE ERROR · nothing is running", BadgeTone.Error);
                    hud.ShowError("The mode could not be chosen", clash);
                    PlatformLog.Error(clash);
                    AbortAcceptanceWith(acceptance, clash);
                    return;
                }

                mode = SitepulseMode.Replay;
            }

            started = true;

            switch (mode)
            {
                case SitepulseMode.Choreographed:
                    hud.SetBadge(SitepulseModes.Badge(mode), BadgeTone.Illustrative);
                    break;
                case SitepulseMode.Replay:
                    StartReplay(args);
                    break;
                case SitepulseMode.Live:
                    // a Live run is a device on the network: it keeps publishing when the window loses focus
                    previousRunInBackground = Application.runInBackground;
                    Application.runInBackground = true;
                    UseObservedCards();
                    hud.SetBadge(LiveBadge(), LiveTone());
                    UseLiveChoreography();
                    break;
            }
        }

        /// <summary>
        /// Live's cards are fed by the observer and by nothing else: the overlay is handed an observed
        /// source before it builds a card, so every reading it makes has Observed provenance and refuses a
        /// value of any other kind. What the observer has not (yet) seen shows as a dash.
        /// </summary>
        void UseObservedCards()
        {
            // a measurement that happened before this moment is not this run's: it is shown with its age,
            // but it is not evidence that the device's telemetry arrived
            var sessionStart = DateTimeOffset.UtcNow;
            observed = new ObservedState(sessionStart);
            if (overlay == null) return;
            overlay.Source = new ObservedReadingSource(observed, id => board?.TokenOf(id), () => observerStatus.Measurements.IsLive);
            overlay.CommandsSince = sessionStart;
            overlay.Proof = proofLog;
        }

        /// <summary>
        /// Live plays its own choreography, swapped in before the fleet reads it (the app runs at -900,
        /// ahead of the fleet's OnEnable, and an already-spawned fleet is respawned to be sure). With no
        /// live file Live stops with a visible error and the fleet is switched off: the five-truck file
        /// is never played silently under a Live badge.
        /// </summary>
        void UseLiveChoreography()
        {
            if (fleet == null) return;
            if (!PlayLiveFleet())
            {
                blocked = true;
                fleet.enabled = false;
                Stop("Live mode cannot start", "the Sitepulse App has no live choreography (Assets/Sitepulse/Data/quarry_fleet_live.json): rebuild the scene or run Sitepulse > Quarry > Add Sitepulse App To Scene");
            }
        }

        /// <summary>Gives the fleet the live file's 18 machines (a replay stands the same machines up). False when the app has none to give.</summary>
        bool PlayLiveFleet()
        {
            if (liveChoreography == null) return false;
            fleet.choreography = liveChoreography;
            if (fleet.isActiveAndEnabled)
            {
                fleet.enabled = false;
                fleet.enabled = true;
            }

            return true;
        }

        // ------------------------------------------------------------------ replay

        /// <summary>
        /// Replay's whole composition: its own root, in an assembly that cannot see the platform or the device plane, handed the scene's fleet
        /// and data layer and (unless this is an offline render) a screen. No token broker, no observer, no session is created here or there.
        /// </summary>
        void StartReplay(string[] args)
        {
            var options = ReplayOptions.Parse(args, out var error);
            if (options == null)
            {
                HideCards();
                hud.SetBadge("MODE ERROR · nothing is running", BadgeTone.Error);
                hud.ShowError("The replay cannot start", error);
                PlatformLog.Error("replay: " + error);
                if (ReplayOptions.Get(args, ReplayOptions.RenderFlag, out _) != null) Application.Quit(1);
                return;
            }

            var composition = ReplayComposition.For(options);
            if (!composition.Hud)
            {
                // an offline render has nothing of the app's own on its frames
                if (overlay != null) overlay.Obstacles = null;
                hud.Destroy();
                hud = null;
            }

            if (fleet == null || !PlayLiveFleet())
            {
                const string why = "the Sitepulse App has no live choreography (Assets/Sitepulse/Data/quarry_fleet_live.json): rebuild the scene or run Sitepulse > Quarry > Add Sitepulse App To Scene";
                HideCards();
                if (hud != null)
                {
                    hud.SetBadge("REPLAY · not running", BadgeTone.Error);
                    hud.ShowError("The replay cannot start", why);
                }

                PlatformLog.Error("replay: " + why);
                if (options.Render) Application.Quit(1);
                return;
            }

            ReplayRoot.Begin(gameObject, new ReplayRoot.Inputs
            {
                Fleet = fleet,
                Overlay = overlay,
                Options = options,
                Screen = composition.Hud ? new HudReplayScreen(hud) : null,
                DefaultRecordings = System.IO.Path.Combine(Application.persistentDataPath, "Recordings"),
                RenderBuild = BuildInfo.Read(LiveRecorder.FindBuildInfo()),
            });
        }

        async void Start()
        {
            if (!started || blocked || mode != SitepulseMode.Live) return;
            try
            {
                await RunLive(cts.Token);
            }
            catch (OperationCanceledException)
            {
                // the app is going away
            }
            catch (Exception e)
            {
                Stop("Live mode stopped", $"{e.GetType().Name}: {e.Message}");
            }
        }

        void HideCards()
        {
            if (overlay != null) overlay.show = false;
        }

        void Stop(string title, string message)
        {
            PlatformLog.Error(title + ": " + message);
            stopped = true;
            // nothing will step the task layer or drain the plane again: whatever is running or waiting is answered now
            Teardown();
            HideCards();
            hud.SetBanner(null);
            hud.SetBadge("LIVE · not running", BadgeTone.Error);
            hud.ShowError(title, message);
            if (probe == null && acceptance != null) AbortAcceptanceWith(acceptance, title + ": " + message);
        }

        // ------------------------------------------------------------------ acceptance

        string LiveBadge(string tenantName = null, string instance = null)
            => SitepulseModes.Badge(mode, tenantName, instance) + (acceptance != null ? " · " + acceptance.RunLabel : "") + (videoRunRequested ? " · VIDEO RUN" : "");

        // a control is amber, so a run with a fault injected can never be taken for a normal one
        BadgeTone LiveTone() => acceptance != null && acceptance.IsControl ? BadgeTone.Illustrative : BadgeTone.Live;

        static bool HasDevice(IReadOnlyList<SceneDevice> devices, string id)
        {
            foreach (var d in devices)
                if (d.ExternalId == id) return true;
            return false;
        }

        string AcceptanceDir() => acceptanceDir ?? (acceptanceDir = string.IsNullOrEmpty(acceptance.Directory) ? Application.persistentDataPath : acceptance.Directory);

        void StartProbe()
        {
            var dir = AcceptanceDir();
            var world = new ProbeWorld
            {
                Board = board,
                Observed = observed,
                Cards = () => overlay != null ? overlay.Readings : Array.Empty<DeviceReading>(),
                CardSource = () => overlay != null && overlay.Source != null ? overlay.Source.Provenance : (Provenance?)null,
                Timeline = () => timeline,
                PrepareLowFuel = id => presenter?.PrepareLowFuel(id),
                FileExists = name => File.Exists(Path.Combine(dir, name)),
                WriteFile = (name, text) => WriteAtomically(Path.Combine(dir, name), text),
                SamplePath = Path.Combine(dir, PhaseAProbe.SampleFile),
                SampleLines = () => sampleLog?.Lines ?? 0,
                UnityVersion = Application.unityVersion,
                CardStreamLive = () => overlay != null && overlay.Source != null ? overlay.Source.StreamLive : (bool?)null,
                Observer = observerStatus,
                Machine = ProbeMachine,
                Bay = () => director != null ? new BayView(director.Bay.Holder, director.Bay.Waiting) : (BayView?)null,
                SessionCount = () => plane != null ? plane.Hosts.Count : 0,
                DroppedSamples = () => SumOverHosts(h => h.Ring.Dropped),
                SendErrors = () => SumOverHosts(h => h.SendErrors),
                FrameSeconds = () => Time.unscaledDeltaTime,
            };
            probe = new PhaseAProbe(acceptance, world, appStartedAt, null, PlatformLog.Info);
        }

        // a machine's controller state and its own tank, for the acceptance probe; null while it has no session or no task layer
        MachineView? ProbeMachine(string id)
        {
            if (director == null || plane == null || !director.TryController(id, out var controller)) return null;
            foreach (var host in plane.Hosts)
                if (host.ExternalId == id) return new MachineView(controller.Mode, controller.Phase, host.Simulation.Model.FuelPct, controller.WantsBay);
            return null;
        }

        long SumOverHosts(Func<DeviceSessionHost, long> read)
        {
            var total = 0L;
            if (plane == null) return total;
            foreach (var host in plane.Hosts) total += read(host);
            return total;
        }

        void TickProbe()
        {
            if (probe == null) return;
            probe.Tick();
            if (!probe.Finished || quitIssued) return;
            quitIssued = true;
            PlatformLog.Info($"{acceptance.RunLabel} · {(probe.Passed ? "PASS" : "FAIL")} · quitting with exit code {probe.ExitCode}");
            Application.Quit(probe.ExitCode);
        }

        // a run that never got far enough to measure anything still leaves a result, and a failing exit code
        void AbortAcceptance(string[] args, string why)
        {
            var parsed = AcceptanceFlags.FromCommandLine(args);
            if (parsed.Ok && parsed.Value != null) AbortAcceptanceWith(parsed.Value, why);
        }

        void AbortAcceptanceWith(AcceptanceOptions options, string why)
        {
            if (options == null || quitIssued) return;
            quitIssued = true;
            try
            {
                var dir = string.IsNullOrEmpty(options.Directory) ? Application.persistentDataPath : options.Directory;
                WriteAtomically(Path.Combine(dir, PhaseAProbe.ResultFile), PhaseAProbe.AbortedResult(options, appStartedAt, DateTimeOffset.UtcNow, Application.unityVersion, why));
            }
            catch (Exception e) when (e is IOException || e is UnauthorizedAccessException)
            {
                PlatformLog.Warn("acceptance: cannot write the result: " + e.Message);
            }

            Application.Quit(1);
        }

        static void WriteAtomically(string path, string text)
        {
            Directory.CreateDirectory(Path.GetDirectoryName(Path.GetFullPath(path)));
            var tmp = path + ".tmp";
            File.WriteAllText(tmp, text, new System.Text.UTF8Encoding(false));
            if (File.Exists(path)) File.Replace(tmp, path, null);
            else File.Move(tmp, path);
        }

        async Task RunLive(CancellationToken ct)
        {
            recordOptions = RecordOptions.Parse(Environment.GetCommandLineArgs(), out var recordError);
            if (recordOptions == null)
            {
                Stop("Live mode cannot start", recordError);
                return;
            }

            var path = Path.Combine(Application.persistentDataPath, LiveSettingsLoader.FileName);
            var settings = LiveSettingsLoader.Resolve(Environment.GetCommandLineArgs(), path, File.Exists, File.ReadAllText);
            if (!settings.Ok)
            {
                Stop("Live mode cannot start", settings.Error);
                return;
            }

            if (fleet == null || fleet.choreography == null || overlay == null || overlay.features == null)
            {
                Stop("Live mode cannot start", "the Sitepulse App component is not wired to the fleet and overlay (rebuild the scene or run Sitepulse > Quarry > Add Sitepulse App To Scene)");
                return;
            }

            var devices = SitepulseScene.Devices(fleet.choreography.text, overlay.plantId);
            var contract = SitepulseScene.Contract(SitepulseScene.Zones(overlay.features.text));
            try
            {
                site = SiteGeometryReader.Parse(overlay.features.text);
            }
            catch (Exception e) when (e is FormatException || e is System.Text.Json.JsonException || e is InvalidOperationException)
            {
                Stop("Live mode cannot start", "the quarry feature file cannot be used for routing: " + e.Message);
                return;
            }

            var client = new RunnerConfigClient(settings.Value);
            var cfg = await client.Fetch(ct);
            if (!cfg.Ok)
            {
                Stop("The runner's config is not usable", cfg.Error);
                return;
            }

            tenant = cfg.Value.Tenant;
            runnerConfig = cfg.Value;
            hud.SetBadge(LiveBadge(cfg.Value.Tenant, cfg.Value.InstanceId), LiveTone());
            PlatformLog.Info($"live · {cfg.Value} · api {cfg.Value.ApiOrigin}");
            if (acceptance != null) PlatformLog.Info($"{acceptance.RunLabel} · results in {AcceptanceDir()}");

            broker = new TokenBroker(cfg.Value, client.Fetch);
            var queries = OperatorQueries.Create(cfg.Value, broker);
            board = new ReadinessBoard(devices, tenant);
            var credentials = new DeviceCredentials();
            if (acceptance != null && acceptance.IsControl && acceptance.Control.Target != null && !HasDevice(devices, acceptance.Control.Target))
            {
                Stop("The acceptance control cannot start", $"{AcceptanceFlags.ControlFlag} names {acceptance.Control.Target}, which is not a device of this scene");
                return;
            }

            if (acceptance != null) StartProbe();
            Render();

            byte[] caPem;
            try { caPem = File.ReadAllBytes(settings.Value.CaPemPath); }
            catch (Exception e)
            {
                Stop("Live mode cannot start", $"the broker CA {settings.Value.CaPemPath} cannot be read: {e.Message}");
                return;
            }

            // an acceptance control may rename one device in the resolve query; nothing else touches it
            await new DeviceBinder(AcceptanceControls.WrapBind(queries.AsQueryFn(), acceptance?.Control), contract).BindAsync(devices, board, credentials, ct);
            PlatformLog.Info($"bind complete · {board.Summary()}");
            if (acceptance != null && acceptance.IsControl && acceptance.Control.Kind == ControlSpec.BadCredential)
            {
                var corrupted = AcceptanceControls.CorruptCredential(credentials, board.TokenOf(acceptance.Control.Target));
                PlatformLog.Info($"{acceptance.RunLabel} · one character of {acceptance.Control.Target}'s credential changed: {(corrupted ? "yes" : "NO (it has no credential)")}");
            }

            ApplyGhosts();
            Render();

            // each machine resumes from the platform's last observed fuel and engine hours (device-state),
            // and from its seed only where the platform has nothing
            var tokens = new List<string>();
            foreach (var d in board.Devices)
                if (!d.Failed && d.Stage == DeviceStage.Credentialed && d.Device.Kind != SceneKind.Plant) tokens.Add(d.Bind.DeviceToken);
            var lastState = await LastStateQuery.FetchAsync(
                OperatorQueries.Create(cfg.Value, broker, Area.DeviceState).AsQueryFn(), tokens, ct);

            // the app may have been torn down while the platform was being asked: build nothing then
            ct.ThrowIfCancellationRequested();

            // sessions: one per credentialed device; a device without one is grey and does not publish
            poses = new RigPoseSource(fleet, overlay);
            IDeviceLinkFactory links = new SdkDeviceLinkFactory(cfg.Value, caPem);
            var sinks = new List<IAckedSampleSink>();
            if (acceptance != null)
            {
                sampleLog = new EmittedSampleLog(Path.Combine(AcceptanceDir(), PhaseAProbe.SampleFile));
                sinks.Add(sampleLog);
            }

            // the recorder is built once the plane and the observer exist; until then the relay holds nothing
            if (recordOptions.Enabled) sinks.Add(sampleRelay);
            if (sinks.Count > 0) links = new RecordingLinkFactory(links, new SampleSinks(sinks.ToArray()));

            plane = new DeviceFleet(board, credentials, links, platformState: lastState, sceneHasZone: site.HasZone);
            _ = plane.StartAll();
            BuildTaskLayer();

            // the observer: the platform's own report of what the 19 devices are doing. It starts after the
            // sessions so the first thing it can honestly confirm is the telemetry they publish.
            var watched = new List<string>();
            foreach (var d in board.Devices)
                if (!d.Failed && d.Bind != null && d.Bind.IsBound) watched.Add(d.Bind.DeviceToken);
            observer = new PlatformObserver(cfg.Value, broker, watched, observed, observerStatus);
            WatchObserver(observer);
            observer.Start();
            board.BeginObserver();
            StartRecorder(cfg.Value, devices.Count);
            if (videoRunRequested) videoRun = new VideoRun(VideoWorld());
            Render();
        }

        // What the observer is told goes to the proof log through the same function a replay uses on the recorded lines, so the drawer holds the same rows.
        void WatchObserver(PlatformObserver o)
        {
            idByToken.Clear();
            foreach (var d in board.Devices)
                if (d.Bind != null && d.Bind.IsBound) idByToken[d.Bind.DeviceToken] = d.Device.ExternalId;
            string IdOf(string token) => token != null && idByToken.TryGetValue(token, out var id) ? id : null;
            o.Watch = item =>
            {
                var line = RecordingMaps.Observed(item);
                if (line != null) ProofFeed.Apply(proofLog, line, IdOf);
            };
        }

        // the video run's eyes and hands: what the platform said (observed state), the device's own account, and the presenter's keys
        VideoRunWorld VideoWorld() => new VideoRunWorld
        {
            Clock = () => DateTimeOffset.UtcNow,
            FleetObserved = () =>
            {
                if (board == null || board.Total == 0) return false;
                foreach (var d in board.Devices)
                    if (d.Stage < DeviceStage.Observed || d.Failed) return false;
                return true;
            },
            PrepareLowFuel = id => presenter?.PrepareLowFuel(id),
            PrepareTyreLeak = id => presenter?.PrepareTyreLeak(id),
            AlarmActive = (id, key) =>
            {
                var token = board?.TokenOf(id);
                if (token == null || !observed.TryGet(token, out var dev)) return false;
                foreach (var a in dev.Alarms.Values)
                    if (a.IsActive && a.AlarmKey == key) return true;
                return false;
            },
            CommandSuccessful = (id, name, since) =>
            {
                var token = board?.TokenOf(id);
                if (token == null || !observed.TryGet(token, out var dev)) return false;
                var c = dev.LastCommand;
                return c != null && c.Name == name && c.Status == "SUCCESSFUL" && c.QueuedAt >= since;
            },
            OnTrack = id => ProbeMachine(id)?.OnTrack ?? false,
            Log = PlatformLog.Info,
            Quit = code =>
            {
                WriteVideoRun();
                PlatformLog.Info($"video-run · {(code == 0 ? "complete" : "INCOMPLETE")} · quitting with exit code {code}");
                Application.Quit(code);
            },
        };

        // beside the recording, so what the take did and what it did not see travels with it
        void WriteVideoRun()
        {
            try
            {
                var dir = liveRecorder != null ? liveRecorder.Directory : Application.persistentDataPath;
                WriteAtomically(Path.Combine(dir, "video-run.json"), videoRun.ToJson());
            }
            catch (Exception e) when (e is IOException || e is UnauthorizedAccessException)
            {
                PlatformLog.Warn("video-run: cannot write video-run.json: " + e.Message);
            }
        }

        // The recording of this run (on by default). It is built last, when there is something to record, and it watches: a recorder that
        // cannot start leaves a logged reason and a run that goes on without it.
        void StartRecorder(RunnerConfig cfg, int deviceCount)
        {
            if (recordOptions == null || !recordOptions.Enabled) return;
            var bound = new List<RunDevice>();
            foreach (var d in board.Devices)
                if (!d.Failed && d.Bind != null && d.Bind.IsBound)
                    bound.Add(new RunDevice { Id = d.Device.ExternalId, Token = d.Bind.DeviceToken, Kind = d.Device.Kind.ToString() });
            liveRecorder = LiveRecorder.TryStart(recordOptions, new LiveRecorderWorld
            {
                Fleet = fleet,
                Plane = plane,
                Director = director,
                Timeline = timeline,
                Presenter = presenter,
                Observer = observer,
                Devices = bound,
                Tenant = cfg.Tenant,
                Instance = cfg.InstanceId,
                Manifest = $"{(fleet.choreography != null ? fleet.choreography.name : "fleet")} · {deviceCount} devices",
            }, PlatformLog.Info);
            sampleRelay.Target = liveRecorder;
        }

        // every machine that has a session also has a task layer: a command it is sent drives its rig. The
        // director is wired to the plane before the plane's first Pump, so no command meets a plane without one.
        void BuildTaskLayer()
        {
            if (plane == null || fleet == null) return;
            var rigs = new Dictionary<string, MachineRig>(StringComparer.Ordinal);
            foreach (var rig in fleet.Machines) rigs[rig.name] = rig;
            var machines = new List<(IMachineBody, MachineModel)>();
            foreach (var host in plane.Hosts)
                if (rigs.TryGetValue(host.ExternalId, out var rig)) machines.Add((new FleetBody(fleet, rig), host.Simulation.Model));
            timeline = new Timeline();
            timeline.Added += (machine, row) => proofLog.DeviceRow(machine, row.At, row.Kind, row.Text);
            director = new TaskDirector(site, RouteGraph.Build(site), timeline, machines, plane.Generation);
            plane.Tasks = director;
            // the bay's attendant serves the machine the Refuelling state names, not whoever stands in the bay
            vignette = FindAnyObjectByType<RefuelVignette>();
            if (vignette != null) vignette.drivenByState = true;
            PlatformLog.Info($"task layer · {machines.Count} machines · route network of {director.Graph.NodeCount} nodes in {director.Graph.Components()} piece(s)");
            presenter = new PresenterControls(director, id => plane[id].Simulation.Model,
                () => runnerConfig == null || broker == null ? null : OperatorQueries.Create(runnerConfig, broker, Area.CommandDelivery).AsQueryFn());
            // a machine whose own task is still running keeps its card, as one with an unfinished command does
            if (overlay != null)
            {
                overlay.TaskRunning = id => director != null && director.TryController(id, out var c) && c.Running != null;
                // the route a machine is driving is drawn on the terrain; a replay draws it from the recording of the same
                overlay.RouteOf = id => director != null && director.TryController(id, out var c) ? routeCache.Of(id, c.CurrentRoute) : null;
            }
        }

        /// <summary>A click on the scene: the presenter and the overlay share the one selected machine.</summary>
        void OnPicked(string id)
        {
            if (presenter == null) return;
            if (!presenter.Pick(id)) presenter.ClearFocus();
            focusShown = presenter.Focus;
        }

        void Update()
        {
            if (broker == null) return;
            broker.Tick();
            TickProbe();
            if (plane != null && !stopped)
            {
                plane.Pump();
                var scale = fleet != null ? fleet.timeScale : 1f;
                director?.Step(Time.deltaTime * scale, Time.unscaledDeltaTime);
                if (vignette != null && director != null) vignette.Servicing = director.Servicing;
                ApplyGhosts();
            }

            if (observer != null && !stopped)
            {
                foreach (var token in observer.Pump()) board.MarkObserved(token);
                board.EvaluateObserved(DateTimeOffset.UtcNow, observed.NewestOwnRunAt);
                hud.SetBanner(ObserverBanner.Text(observerStatus, observed));
                UpdateSimulation();
                UpdatePresenter();
                videoRun?.Tick();
                // the header in the log too, so an unattended (batchmode) run can be read afterwards
                if (Time.unscaledTime >= nextStatusLog)
                {
                    nextStatusLog = Time.unscaledTime + StatusLogEverySeconds;
                    PlatformLog.Info($"status · {board.Summary()} · {ObserverBanner.Line(observerStatus)}");
                }
            }

            Render();
        }

        // key R: the readiness panel lists every device (the default is the compact one); key L: the model's
        // own values before publish, never platform data
        void UpdateSimulation()
        {
            var kb = Keyboard.current;
            if (kb != null && kb.rKey.wasPressedThisFrame) showAllDevices = !showAllDevices;
            if (kb != null && kb.lKey.wasPressedThisFrame)
            {
                showSimulation = !showSimulation;
                nextSimulation = 0f;
                if (!showSimulation) hud.ShowSimulation(null);
            }

            if (!showSimulation || plane == null || Time.unscaledTime < nextSimulation) return;
            nextSimulation = Time.unscaledTime + RenderEverySeconds;
            hud.ShowSimulation(LocalSimulationView.Text(plane.Hosts, DateTimeOffset.UtcNow));
        }

        // The presenter's keys (design 4.5): inputs only, and each says what it did in the timeline panel.
        void UpdatePresenter()
        {
            if (presenter == null) return;
            hud.ShowHelp(PresenterControls.HelpLine);
            var kb = Keyboard.current;
            if (kb != null)
            {
                if (kb.leftBracketKey.wasPressedThisFrame) presenter.Select(-1);
                if (kb.rightBracketKey.wasPressedThisFrame) presenter.Select(1);
                if (kb.tKey.wasPressedThisFrame) showTimeline = !showTimeline;
                if (kb.pKey.wasPressedThisFrame) presenter.PrepareLowFuel();
                if (kb.kKey.wasPressedThisFrame) presenter.PrepareTyreLeak();
                if (overlay != null)
                {
                    if (kb.dKey.wasPressedThisFrame) overlay.Layers = overlay.Layers.With(drawer: overlay.Layers.Drawer == DrawerMode.Off ? DrawerMode.Side : DrawerMode.Off);
                    if (kb.iKey.wasPressedThisFrame) overlay.Layers = overlay.Layers.With(panel: !overlay.Layers.Panel);
                    if (kb.zKey.wasPressedThisFrame) overlay.Layers = overlay.Layers.With(zoneLabels: !overlay.Layers.ZoneLabels);
                }
                if (kb.gKey.wasPressedThisFrame) presenter.Resume();
                if (kb.fKey.wasPressedThisFrame) RunPresenterAsync(presenter.BeginFresh);
                if (presenter.Fresh == FreshState.Confirm)
                {
                    if (kb.yKey.wasPressedThisFrame) RunPresenterAsync(presenter.ConfirmFresh);
                    else if (kb.nKey.wasPressedThisFrame || kb.escapeKey.wasPressedThisFrame) presenter.DeclineFresh();
                }
            }

            presenter.FollowLatest(timeline?.LastCommanded);
            // the keys and a click select the same machine: the overlay shows whichever the presenter holds
            if (overlay != null && presenter.Focus != focusShown)
            {
                focusShown = presenter.Focus;
                overlay.Selected = focusShown;
            }

            var text = showTimeline ? presenter.PanelText(DateTimeOffset.UtcNow) : null;
            // the panel's text moves with the clock (a message ages out), so it is set when it differs
            if (text != tasksShown)
            {
                tasksShown = text;
                hud.ShowTasks(text != null ? HudText.Esc(text) : null);
            }
        }

        async void RunPresenterAsync(Func<CancellationToken, Task> action)
        {
            try
            {
                await action(cts.Token);
            }
            catch (OperationCanceledException)
            {
                // the app is going away
            }
            catch (Exception e)
            {
                PlatformLog.Warn($"presenter action failed: {e.GetType().Name}: {e.Message}");
            }
        }

        // After every Update has run, so the fleet has already moved the machines for this frame: the pose is
        // read where the machine is now, over the time the scene itself advanced (the clock that moved it).
        void LateUpdate()
        {
            if (plane == null || stopped || poses == null) return;
            var scale = fleet != null ? fleet.timeScale : 1f;
            plane.Advance(DateTimeOffset.UtcNow, poses, Time.deltaTime * scale);
            liveRecorder?.Tick();
        }

        string TokenLine()
        {
            switch (broker.State)
            {
                case TokenState.Fresh: return $"operator token fresh · expires {broker.ExpiresAt.UtcDateTime:HH:mm:ss}Z";
                case TokenState.Refreshing: return "operator token refreshing";
                default: return $"operator token EXPIRED · {broker.Reason}";
            }
        }

        // redraw only when something changed: the board's version or the token line
        void Render()
        {
            if (stopped || board == null || broker == null) return;
            if (Time.unscaledTime < nextRender) return;
            var token = TokenLine();
            var observerVersion = observerStatus.Version;
            if (board.Version == shownVersion && token == shownToken && observerVersion == shownObserver && showAllDevices == shownAllDevices) return;
            nextRender = Time.unscaledTime + RenderEverySeconds;
            shownVersion = board.Version;
            shownToken = token;
            shownObserver = observerVersion;
            shownAllDevices = showAllDevices;
            hud.ShowReadiness(board.Summary(), token, observer != null ? ObserverBanner.Line(observerStatus) : NoObserver, board.Brief(), board.Lines(), board.Notes(), showAllDevices);
        }

        /// <summary>
        /// A machine with no session is drawn as a grey placeholder, not as a working one: it did not
        /// bind, or it has no credential, or the broker refused it (blind), or its session could not
        /// start (<see cref="DeviceReadiness.IsGrey"/>). Grey is applied once per machine and never lifted.
        /// </summary>
        void ApplyGhosts()
        {
            if (fleet == null || board == null) return;
            foreach (var d in board.TakeNewlyGrey(greyed))
            {
                if (d.Kind == SceneKind.Plant)
                {
                    GhostTint.Apply(overlay != null ? overlay.plant : null);
                    continue;
                }

                foreach (var rig in fleet.Machines)
                    if (rig.name == d.ExternalId) GhostTint.Apply(rig.transform);
            }
        }

        // the sessions go first so the answers they publish on the way down are in the recording, which is closed last
        void Teardown()
        {
            TeardownRun();
            if (liveRecorder == null) return;
            sampleRelay.Target = null;
            liveRecorder.Dispose();
            liveRecorder = null;
        }

        void TeardownRun()
        {
            if (observer != null)
            {
                observer.Dispose();
                observer = null;
            }

            if (plane == null) return;
            var p = plane;
            plane = null;
            // the run is over: whatever a machine was doing is answered failed (reset), the handlers are given a
            // moment to return and the SDK to publish what they returned, and only then do the sessions go
            var answered = director != null ? director.FailAll() : 0;
            var unanswered = p.QuiesceCommands(TimeSpan.FromSeconds(2), TimeSpan.FromMilliseconds(300), answered);
            if (unanswered > 0) PlatformLog.Warn($"{unanswered} command handler(s) had not returned when the sessions were closed");
            if (!p.Shutdown(DeviceFleet.DisposeTimeout + TimeSpan.FromSeconds(1)))
                PlatformLog.Warn("device sessions did not all close in time");
        }

        void OnApplicationQuit()
        {
            Teardown();
            sampleLog?.Dispose();
        }

        void OnDestroy()
        {
            if (overlay != null) overlay.Picked -= OnPicked;
            cts?.Cancel();
            Teardown();
            sampleLog?.Dispose();
            if (previousRunInBackground.HasValue) Application.runInBackground = previousRunInBackground.Value;
            cts?.Dispose();
            hud?.Destroy();
        }
    }
}
