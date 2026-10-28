// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Demos.Platform;
using DeviceChain.Demos.Platform.DevicePlane;
using DeviceChain.Towerpulse.Model;
using DeviceChain.Towerpulse.Plane;
using UnityEngine;

namespace DeviceChain.Towerpulse.DevicePlane
{
    /// <summary>
    /// The headless bootstrap of the Towerpulse device plane: put this on an empty GameObject and press Play (the TowerpulseLive scene does). It reads the
    /// live settings, binds the 31 devices through the platform, credentials them, opens one MQTT session each and runs the scripted day at wall-clock
    /// speed. It draws only a readiness line; the building's visuals are a separate layer that reads the same <see cref="TowerPlane"/>.
    ///
    /// Flags (all optional): -tp-start-hour 13.9 (hour of day the model starts), -tp-speed 1 (model speed; above 1 is ACCELERATED and labelled; event
    /// time is always the wall clock), -tp-seed 1, -tp-interval 10 (seconds between readings), plus the shared -dc-runner / -dc-ca.
    /// </summary>
    public sealed class TowerpulseLiveApp : MonoBehaviour
    {
        public const string StartHourFlag = "-tp-start-hour", SpeedFlag = "-tp-speed", SeedFlag = "-tp-seed", IntervalFlag = "-tp-interval";
        public const double DefaultStartHour = 13.9;

        readonly CancellationTokenSource cts = new CancellationTokenSource();
        TowerPlane plane;
        ReadinessBoard board;
        PlaneRunner runner;
        TokenBroker broker;
        string status = "starting";
        string banner = "";
        float startedAt;
        bool shutdownBegun;

        /// <summary>The plane once live mode is up (null before); the visuals read it.</summary>
        public TowerPlane Plane => plane;
        public PlaneRunner Runner => runner;
        public string Status => status;

        void Awake()
        {
            DemoIdentity.Configure(TowerpulseScene.DemoName);
        }

        void Start()
        {
            startedAt = Time.realtimeSinceStartup;
            _ = RunLive(cts.Token);
        }

        static string Flag(string flag, out string error)
        {
            return CommandLineArgs.Get(Environment.GetCommandLineArgs(), flag, out error);
        }

        static bool Number(string flag, double fallback, double lo, double hi, out double value, out string error)
        {
            value = fallback;
            var raw = Flag(flag, out error);
            if (error != null) return false;
            if (raw == null) return true;
            if (!double.TryParse(raw, System.Globalization.NumberStyles.Float, System.Globalization.CultureInfo.InvariantCulture, out value) || double.IsNaN(value) || value < lo || value > hi)
            {
                error = flag + " \"" + raw + "\" must be a number in [" + lo + ", " + hi + "]";
                return false;
            }
            return true;
        }

        async Task RunLive(CancellationToken ct)
        {
            try
            {
                if (!Number(StartHourFlag, DefaultStartHour, 0, 23.99, out var hour, out var err) || !Number(SpeedFlag, 1, 0.1, 60, out var speed, out err)
                    || !Number(SeedFlag, 1, 0, 1e9, out var seed, out err) || !Number(IntervalFlag, 10, 1, 120, out var interval, out err))
                {
                    Stop("Live mode cannot start", err);
                    return;
                }

                var path = Path.Combine(Application.persistentDataPath, LiveSettingsLoader.FileName);
                var settings = LiveSettingsLoader.Resolve(Environment.GetCommandLineArgs(), path, File.Exists, File.ReadAllText);
                if (!settings.Ok) { Stop("Live mode cannot start", settings.Error); return; }

                var clock = PlaneClock.FromHour(hour, speed);
                plane = new TowerPlane((ulong)seed, clock);
                var devices = TowerpulseScene.Devices(plane.Devices);

                var client = new RunnerConfigClient(settings.Value);
                var cfg = await client.Fetch(ct);
                if (!cfg.Ok) { Stop("The runner's config is not usable", cfg.Error); return; }
                banner = cfg.Value.Tenant + "@" + cfg.Value.InstanceId + " · " + clock.Label;
                PlatformLog.Info("live · " + cfg.Value + " · model " + clock.Label + " · seed " + seed + " · readings every " + interval + " s, stamped with the wall clock");

                byte[] caPem;
                try { caPem = File.ReadAllBytes(settings.Value.CaPemPath); }
                catch (Exception e) { Stop("Live mode cannot start", "the broker CA " + settings.Value.CaPemPath + " cannot be read: " + e.Message); return; }

                broker = new TokenBroker(cfg.Value, client.Fetch);
                var queries = OperatorQueries.Create(cfg.Value, broker);
                board = new ReadinessBoard(devices, cfg.Value.Tenant);
                var credentials = new DeviceCredentials();
                await new DeviceBinder(queries.AsQueryFn(), TowerpulseScene.Contract()).BindAsync(devices, board, credentials, ct);
                PlatformLog.Info("bind complete · " + board.Summary());
                ct.ThrowIfCancellationRequested();

                runner = new PlaneRunner(plane, board, credentials, new SdkDeviceLinkFactory(cfg.Value, caPem), interval);
                status = "connecting";
                await runner.StartAllAsync();
                PlatformLog.Info("sessions started · " + runner.Readiness());
                status = "running";
                startedAt = Time.realtimeSinceStartup;
            }
            catch (OperationCanceledException) { }
            catch (Exception e)
            {
                Stop("Live mode failed", e.GetType().Name + ": " + e.Message);
            }
        }

        void Stop(string title, string why)
        {
            status = title + ": " + why;
            PlatformLog.Error(status);
        }

        void Update()
        {
            if (runner == null || status != "running") return;
            runner.Tick(Time.realtimeSinceStartup - startedAt, DateTimeOffset.UtcNow);
            broker?.Tick();
        }

        void OnGUI()
        {
            var sb = new StringBuilder();
            sb.Append("Towerpulse device plane · ").Append(status).Append('\n');
            if (banner.Length > 0) sb.Append(banner).Append('\n');
            if (runner != null)
            {
                sb.Append(runner.Readiness()).Append('\n');
                sb.Append("model ").Append(TimeClock.Format(plane.Model.TimeS)).Append(" · commands waiting ").Append(plane.Commands.PendingCount);
            }
            GUI.Label(new Rect(12, 8, 900, 90), sb.ToString());
        }

        void OnDestroy()
        {
            if (shutdownBegun) return;
            shutdownBegun = true;
            cts.Cancel();
            if (runner != null)
            {
                // best effort and bounded: the sessions are released, but the editor is not held
                try { runner.ShutdownAsync().Wait(PlaneRunner.DisposeTimeout + TimeSpan.FromSeconds(1)); }
                catch (Exception e) { Debug.LogWarning(DemoIdentity.LogPrefix + "shutdown: " + e.GetType().Name); }
            }
        }
    }
}
