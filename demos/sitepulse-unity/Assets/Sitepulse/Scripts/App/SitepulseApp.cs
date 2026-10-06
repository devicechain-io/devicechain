// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using System.Threading;
using System.Threading.Tasks;
using System.Collections.Generic;
using DeviceChain.Sdk;
using DeviceChain.Sitepulse.DevicePlane;
using DeviceChain.Sitepulse.Platform;
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
    /// all the cards show. Replay is not in this build and says so in plain sight; it does not borrow
    /// another mode. A mode that cannot start shows why and stays stopped.
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

        public SitepulseMode Mode => mode;

        void Awake()
        {
            cts = new CancellationTokenSource();
            hud = new SitepulseHud(transform);
            // the data layer keeps its cards off the HUD
            if (overlay != null) overlay.Obstacles = hud.Obstacles;

            var args = Environment.GetCommandLineArgs();
            var parsed = SitepulseModes.FromCommandLine(args, out var present);
            if (!parsed.Ok)
            {
                HideCards();
                hud.SetBadge("MODE ERROR · nothing is running", BadgeTone.Error);
                hud.ShowError("The mode could not be chosen", parsed.Error);
                PlatformLog.Error(parsed.Error);
                return;
            }

            mode = parsed.Value;
            if (!present && Application.isEditor && editorMode != EditorModeOverride.None)
                mode = (SitepulseMode)Enum.Parse(typeof(SitepulseMode), editorMode.ToString());
            started = true;

            switch (mode)
            {
                case SitepulseMode.Choreographed:
                    hud.SetBadge(SitepulseModes.Badge(mode), BadgeTone.Illustrative);
                    break;
                case SitepulseMode.Replay:
                    HideCards();
                    hud.SetBadge(SitepulseModes.Badge(mode), BadgeTone.Error);
                    hud.ShowError("Replay is not in this build", "Start with -sitepulse-mode live or choreographed.");
                    PlatformLog.Error("Replay is not in this build");
                    break;
                case SitepulseMode.Live:
                    // a Live run is a device on the network: it keeps publishing when the window loses focus
                    previousRunInBackground = Application.runInBackground;
                    Application.runInBackground = true;
                    UseObservedCards();
                    hud.SetBadge(SitepulseModes.Badge(mode), BadgeTone.Live);
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
            observed = new ObservedState(DateTimeOffset.UtcNow);
            if (overlay == null) return;
            overlay.Source = new ObservedReadingSource(observed, id => board?.TokenOf(id), () => observerStatus.Measurements.IsLive);
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
            if (liveChoreography == null)
            {
                blocked = true;
                fleet.enabled = false;
                Stop("Live mode cannot start", "the Sitepulse App has no live choreography (Assets/Sitepulse/Data/quarry_fleet_live.json): rebuild the scene or run Sitepulse > Quarry > Add Sitepulse App To Scene");
                return;
            }

            fleet.choreography = liveChoreography;
            if (fleet.isActiveAndEnabled)
            {
                fleet.enabled = false;
                fleet.enabled = true;
            }
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
        }

        async Task RunLive(CancellationToken ct)
        {
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
            hud.SetBadge(SitepulseModes.Badge(mode, cfg.Value.Tenant, cfg.Value.InstanceId), BadgeTone.Live);
            PlatformLog.Info($"live · {cfg.Value} · api {cfg.Value.ApiOrigin}");

            broker = new TokenBroker(cfg.Value, client.Fetch);
            var queries = OperatorQueries.Create(cfg.Value, broker);
            board = new ReadinessBoard(devices, tenant);
            var credentials = new DeviceCredentials();
            Render();

            byte[] caPem;
            try { caPem = File.ReadAllBytes(settings.Value.CaPemPath); }
            catch (Exception e)
            {
                Stop("Live mode cannot start", $"the broker CA {settings.Value.CaPemPath} cannot be read: {e.Message}");
                return;
            }

            await new DeviceBinder(queries.AsQueryFn(), contract).BindAsync(devices, board, credentials, ct);
            PlatformLog.Info($"bind complete · {board.Summary()}");
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
            plane = new DeviceFleet(board, credentials, new SdkDeviceLinkFactory(cfg.Value, caPem), platformState: lastState, sceneHasZone: site.HasZone);
            _ = plane.StartAll();
            BuildTaskLayer();

            // the observer: the platform's own report of what the 19 devices are doing. It starts after the
            // sessions so the first thing it can honestly confirm is the telemetry they publish.
            var watched = new List<string>();
            foreach (var d in board.Devices)
                if (!d.Failed && d.Bind != null && d.Bind.IsBound) watched.Add(d.Bind.DeviceToken);
            observer = new PlatformObserver(cfg.Value, broker, watched, observed, observerStatus);
            observer.Start();
            board.BeginObserver();
            Render();
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
            director = new TaskDirector(site, RouteGraph.Build(site), timeline, machines, plane.Generation);
            plane.Tasks = director;
            // the bay's attendant serves the machine the Refuelling state names, not whoever stands in the bay
            vignette = FindAnyObjectByType<RefuelVignette>();
            if (vignette != null) vignette.drivenByState = true;
            PlatformLog.Info($"task layer · {machines.Count} machines · route network of {director.Graph.NodeCount} nodes in {director.Graph.Components()} piece(s)");
            presenter = new PresenterControls(director, id => plane[id].Simulation.Model,
                () => runnerConfig == null || broker == null ? null : OperatorQueries.Create(runnerConfig, broker, Area.CommandDelivery).AsQueryFn());
        }

        void Update()
        {
            if (broker == null) return;
            broker.Tick();
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
                if (kb.gKey.wasPressedThisFrame) presenter.Resume();
                if (kb.fKey.wasPressedThisFrame) RunPresenterAsync(presenter.BeginFresh);
                if (presenter.Fresh == FreshState.Confirm)
                {
                    if (kb.yKey.wasPressedThisFrame) RunPresenterAsync(presenter.ConfirmFresh);
                    else if (kb.nKey.wasPressedThisFrame || kb.escapeKey.wasPressedThisFrame) presenter.DeclineFresh();
                }
            }

            presenter.FollowLatest(timeline?.LastCommanded);
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

        void Teardown()
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

        void OnApplicationQuit() => Teardown();

        void OnDestroy()
        {
            cts?.Cancel();
            Teardown();
            if (previousRunInBackground.HasValue) Application.runInBackground = previousRunInBackground.Value;
            cts?.Dispose();
            hud?.Destroy();
        }
    }
}
