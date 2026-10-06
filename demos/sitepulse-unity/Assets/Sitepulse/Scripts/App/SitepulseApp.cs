// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Visuals;
using UnityEngine;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>
    /// The composition root. It picks the mode once, at startup, from <c>-sitepulse-mode</c> (an
    /// Editor-only override applies when the flag is absent), puts the mode badge on screen, and
    /// builds whichever world that mode names. Choreographed leaves the scene as it is, illustrative
    /// cards and all. Live hides those cards for good (an illustrative value on a Live screen is the
    /// one thing the demo must never do), reads the runner's config, binds the scene's 19 devices
    /// to the platform and shows how far each got. Replay is not in this build and says so in plain
    /// sight; it does not borrow another mode. A mode that cannot start shows why and stays stopped.
    ///
    /// Runs before the data layer's first frame so its cards are never drawn for a frame in a mode
    /// that forbids them.
    /// </summary>
    [DefaultExecutionOrder(-900)]
    public sealed class SitepulseApp : MonoBehaviour
    {
        public enum EditorModeOverride { None, Choreographed, Live, Replay }

        [Tooltip("The data layer whose illustrative cards Live and Replay switch off.")]
        public IotOverlay overlay;
        [Tooltip("The preview fleet: its choreography file names the scene's machines, and the machines are tinted when they do not bind.")]
        public QuarryFleetPreview fleet;
        [Tooltip("Editor only, and only when -sitepulse-mode is not on the command line.")]
        [SerializeField] EditorModeOverride editorMode = EditorModeOverride.None;

        const string LiveNote = "Live values arrive with the observer";

        SitepulseHud hud;
        SitepulseMode mode;
        bool started;
        CancellationTokenSource cts;

        // Live
        TokenBroker broker;
        ReadinessBoard board;
        string tenant;
        int shownVersion = -1;
        string shownToken;
        bool ghostsApplied, stopped;

        public SitepulseMode Mode => mode;

        void Awake()
        {
            cts = new CancellationTokenSource();
            hud = new SitepulseHud(transform);

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
                    HideCards();
                    hud.SetBadge(SitepulseModes.Badge(mode), BadgeTone.Live);
                    break;
            }
        }

        async void Start()
        {
            if (!started || mode != SitepulseMode.Live) return;
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

            var client = new RunnerConfigClient(settings.Value);
            var cfg = await client.Fetch(ct);
            if (!cfg.Ok)
            {
                Stop("The runner's config is not usable", cfg.Error);
                return;
            }

            tenant = cfg.Value.Tenant;
            hud.SetBadge(SitepulseModes.Badge(mode, cfg.Value.Tenant, cfg.Value.InstanceId), BadgeTone.Live);
            PlatformLog.Info($"live · {cfg.Value} · api {cfg.Value.ApiOrigin}");

            broker = new TokenBroker(cfg.Value, client.Fetch);
            var queries = OperatorQueries.Create(cfg.Value, broker);
            board = new ReadinessBoard(devices, tenant);
            var credentials = new DeviceCredentials();
            Render();

            await new DeviceBinder(queries.AsQueryFn(), contract).BindAsync(devices, board, credentials, ct);
            PlatformLog.Info($"bind complete · {board.Summary()}");
            ApplyGhosts();
            Render();
        }

        void Update()
        {
            if (broker == null) return;
            broker.Tick();
            Render();
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
            var token = TokenLine();
            if (board.Version == shownVersion && token == shownToken) return;
            shownVersion = board.Version;
            shownToken = token;
            hud.ShowReadiness(board.Summary(), token, LiveNote, board.Lines(), board.Notes());
        }

        /// <summary>A machine that is not on the platform is drawn as a grey placeholder, not as a working one.</summary>
        void ApplyGhosts()
        {
            if (ghostsApplied || fleet == null) return;
            ghostsApplied = true;
            foreach (var d in board.Devices)
            {
                if (!d.Failed || d.Stage != DeviceStage.Unbound) continue;
                if (d.Device.Kind == SceneKind.Plant)
                {
                    GhostTint.Apply(overlay != null ? overlay.plant : null);
                    continue;
                }

                foreach (var rig in fleet.Machines)
                    if (rig.name == d.Device.ExternalId) GhostTint.Apply(rig.transform);
            }
        }

        void OnDestroy()
        {
            cts?.Cancel();
            cts?.Dispose();
            hud?.Destroy();
        }
    }
}
