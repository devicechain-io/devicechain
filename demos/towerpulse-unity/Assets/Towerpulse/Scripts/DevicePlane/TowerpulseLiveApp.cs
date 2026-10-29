// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using System.Text;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Demos.Platform;
using DeviceChain.Demos.Platform.DevicePlane;
using DeviceChain.Demos.Platform.Recording;
using DeviceChain.Towerpulse.Recording;
using DeviceChain.Towerpulse.Story;
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
    /// <c>-tp-offline</c> (or the environment variable TOWERPULSE_OFFLINE=1) runs the SAME plane with no network at all: nothing is bound, no session is
    /// opened and the platform is replaced by a scripted preview that every card labels as such. It is for rehearsals and stills, never for a take.
    ///
    /// Flags (all optional): -tp-offline, -tp-start-hour 13.9 (hour of day the model starts), -tp-speed 1 (model speed; above 1 is ACCELERATED and labelled; event
    /// time is always the wall clock), -tp-seed 1, -tp-interval 10 (seconds between readings), plus the shared -dc-runner / -dc-ca.
    /// </summary>
    public sealed class TowerpulseLiveApp : MonoBehaviour
    {
        public const string StartHourFlag = "-tp-start-hour", SpeedFlag = "-tp-speed", SeedFlag = "-tp-seed", IntervalFlag = "-tp-interval", OfflineFlag = "-tp-offline";
        /// <summary>-tp-record DIR records the run under DIR (one folder per run); -tp-replay RUNDIR plays a recorded run back with no platform; -tp-label names a run in its header.</summary>
        public const string RecordFlag = "-tp-record", ReplayFlag = "-tp-replay", LabelFlag = "-tp-label";
        public const string RecordEnv = "TOWERPULSE_RECORD";
        public const string OfflineEnv = "TOWERPULSE_OFFLINE";
        public const double DefaultStartHour = 13.9;

        readonly CancellationTokenSource cts = new CancellationTokenSource();
        TowerPlane plane;
        ReadinessBoard board;
        PlaneRunner runner;
        TokenBroker broker;
        PlatformObserver observer;
        readonly Queue<ObserverItem> observed = new Queue<ObserverItem>();
        readonly Dictionary<string, string> externalIdByToken = new Dictionary<string, string>(StringComparer.Ordinal);
        readonly Dictionary<string, string> tokenByExternalId = new Dictionary<string, string>(StringComparer.Ordinal);
        double speed = 1, seed = 1, intervalS = 10;
        string status = "starting";
        string banner = "";
        float startedAt;
        bool shutdownBegun;
        TowerpulseRecorder recorder;
        string recordBase, runLabel;

        /// <summary>The plane once live mode is up (null before); the visuals read it.</summary>
        public TowerPlane Plane => plane;
        public PlaneRunner Runner => runner;
        public string Status => status;

        /// <summary>True when there is no platform: the plane runs alone and a scripted preview stands in for it.</summary>
        public bool Offline { get; private set; }

        /// <summary>The recording being replayed, or null. A replay is an offline plane whose story layer is fed from this instead of from the preview.</summary>
        public TowerpulseRecording ReplaySource { get; private set; }

        public TowerpulseRecorder Recorder => recorder;

        /// <summary>Keeps a story fact in the recording (when one is being made). The caller has already applied the fact to its boards.</summary>
        public void RecordFact(StoryFact f) { recorder?.Fact(f); }

        /// <summary>What the plane received and answered, by command id. Replaced by <see cref="Rewind"/>.</summary>
        public CommandLedger Ledger { get; private set; } = new CommandLedger();

        /// <summary>The platform's own record of what it saw, once the observer is running; null before that and offline.</summary>
        public ObservedState Observed { get; private set; }
        public ObserverStatus ObserverState { get; } = new ObserverStatus();

        /// <summary>Counts how many times the plane was rebuilt, so a reader that cached anything about it knows to start again.</summary>
        public int Generation { get; private set; }

        /// <summary>Set by the story layer when it draws its own status, so the plain text readout steps aside.</summary>
        public bool DrawStatus { get; set; } = true;

        public string Banner => banner;

        public bool TryExternalId(string deviceToken, out string externalId) => externalIdByToken.TryGetValue(deviceToken ?? "", out externalId);
        public bool TryDeviceToken(string externalId, out string deviceToken) => tokenByExternalId.TryGetValue(externalId ?? "", out deviceToken);

        /// <summary>Hands every platform observation made since the last call to the caller, on the main thread, in order.</summary>
        public int DrainObserved(Action<ObserverItem> apply)
        {
            int n = 0;
            while (observed.Count > 0) { apply(observed.Dequeue()); n++; }
            return n;
        }

        /// <summary>The wall seconds the plane has run since it (re)started: the clock the model follows.</summary>
        public double WallSeconds => Offline ? offlineWall : Time.realtimeSinceStartup - startedAt;

        /// <summary>Offline only: holds the model still (a still frame, a rehearsal pause).</summary>
        public bool Frozen { get; set; }

        double offlineWall, lastRealtime;

        /// <summary>Offline only: runs the model forward by wall seconds, as a frame would, whatever the Editor's frame rate.</summary>
        public void AdvanceOffline(double wallSeconds)
        {
            if (!Offline) throw new InvalidOperationException("only an offline plane can be stepped by hand");
            offlineWall += wallSeconds;
            plane.Advance(offlineWall);
        }

        /// <summary>
        /// Rehearsal only: rebuild the plane at an hour of the model's day, with a fresh ledger. Refuses to touch a plane that is talking to a platform.
        /// </summary>
        public void Rewind(double startHour)
        {
            if (!Offline) throw new InvalidOperationException("only an offline plane can be rewound; a live plane's devices are bound to its model");
            plane = new TowerPlane((ulong)seed, PlaneClock.FromHour(startHour, speed));
            Ledger = new CommandLedger();
            startedAt = Time.realtimeSinceStartup;
            offlineWall = 0;
            Generation++;
        }

        void Awake()
        {
            DemoIdentity.Configure(TowerpulseScene.DemoName);
        }

        void Start()
        {
            startedAt = Time.realtimeSinceStartup;
            var replayDir = Flag(ReplayFlag, out var replayErr);
            if (replayErr != null) { Stop("Replay cannot start", replayErr); return; }
            if (replayDir != null)
            {
                StartReplay(replayDir);
                return;
            }
            if (Array.IndexOf(Environment.GetCommandLineArgs(), OfflineFlag) >= 0 || Environment.GetEnvironmentVariable(OfflineEnv) == "1")
            {
                StartOffline();
                return;
            }
            _ = RunLive(cts.Token);
        }

        void StartOffline()
        {
            if (!Number(StartHourFlag, DefaultStartHour, 0, 23.99, out var hour, out var err) || !Number(SpeedFlag, 1, 0.1, 60, out speed, out err)
                || !Number(SeedFlag, 1, 0, 1e9, out seed, out err))
            {
                Stop("Offline mode cannot start", err);
                return;
            }
            Offline = true;
            var clock = PlaneClock.FromHour(hour, speed);
            plane = new TowerPlane((ulong)seed, clock);
            banner = "OFFLINE PREVIEW · " + clock.Label;
            status = "offline";
            PlatformLog.Info("offline preview · no platform · " + clock.Label);
        }

        void StartReplay(string dir)
        {
            try
            {
                var rec = TowerpulseRecording.Open(dir);
                ReplaySource = rec;
                Offline = true;
                seed = rec.Info.Seed;
                speed = rec.Info.Speed;
                var clock = PlaneClock.FromHour(rec.Info.StartHour, speed);
                plane = new TowerPlane(rec.Info.Seed, clock);
                banner = "REPLAY OF " + rec.Header.RunId + " · " + rec.Info.ModelVersion + " · " + clock.Label;
                status = "replay";
                PlatformLog.Info("replay · " + rec.Header.RunId + " · " + rec.Facts.Count + " story facts · " + clock.Label);
            }
            catch (Exception e) when (e is RecordingFormatException || e is System.IO.IOException)
            {
                Stop("Replay cannot start", e.Message);
            }
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
                if (!Number(StartHourFlag, DefaultStartHour, 0, 23.99, out var hour, out var err) || !Number(SpeedFlag, 1, 0.1, 60, out speed, out err)
                    || !Number(SeedFlag, 1, 0, 1e9, out seed, out err) || !Number(IntervalFlag, 10, 1, 120, out var interval, out err))
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
                recordBase = Flag(RecordFlag, out err) ?? Environment.GetEnvironmentVariable(RecordEnv);
                if (err != null) { Stop("Live mode cannot start", err); return; }
                runLabel = Flag(LabelFlag, out err);
                if (err != null) { Stop("Live mode cannot start", err); return; }
                intervalS = interval;

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

                runner = new PlaneRunner(plane, board, credentials, new SdkDeviceLinkFactory(cfg.Value, caPem), interval, Ledger);
                if (!StartRecorder(cfg.Value, hour)) return;
                status = "connecting";
                await runner.StartAllAsync();
                PlatformLog.Info("sessions started · " + runner.Readiness());
                status = "running";
                startedAt = Time.realtimeSinceStartup;
                StartObserver(cfg.Value, board);
            }
            catch (OperationCanceledException) { }
            catch (Exception e)
            {
                Stop("Live mode failed", e.GetType().Name + ": " + e.Message);
            }
        }

        // Opened before the sessions start, so the first reading is in it. A recording that cannot be made refuses the run: a take nobody can replay is not a take.
        bool StartRecorder(RunnerConfig cfg, double hour)
        {
            if (string.IsNullOrEmpty(recordBase)) return true;
            try
            {
                var header = new RunHeader
                {
                    Instance = cfg.InstanceId, Tenant = cfg.Tenant, Build = BuildInfo.Read(Path.Combine(Application.dataPath, "..", "build-info.json")),
                };
                header.Clock.Add(new ClockSegment { From = 0, Mode = speed > 1.0000001 ? ClockSegment.Accelerated : ClockSegment.Real, Scale = speed });
                var info = new TowerpulseRunInfo
                {
                    ModelVersion = TowerDay.ModelVersion, ManifestId = TowerpulseScene.DemoName, Seed = (ulong)seed, StartHour = hour, Speed = speed, IntervalSeconds = intervalS, Label = runLabel,
                };
                recorder = TowerpulseRecorder.Create(recordBase, header, info);
                foreach (var d in board.Devices)
                    if (d.Bind != null && d.Bind.IsBound) recorder.AddDevice(d.Device.ExternalId, d.Bind.DeviceToken, SceneKind.Of(plane.Find(d.Device.ExternalId).Kind));
                recorder.RewriteHeaders();
                runner.Published += (dev, values, occ, ack) => recorder.Sample(dev.ExternalId, values, occ, ack);
                Ledger.ReceivedEvent += r => recorder.Fact(StoryFacts.FromReceived(r));
                Ledger.AnsweredEvent += r => recorder.Fact(StoryFacts.FromAnswered(r));
                PlatformLog.Info("recording to " + recorder.Directory);
                return true;
            }
            catch (Exception e)
            {
                Stop("Live mode cannot start", "the recording could not be made: " + e.Message);
                return false;
            }
        }

        // The platform's own account of the campus (measurements, alarms, commands), read under the operator's token. It starts after the sessions, so the
        // first thing it can confirm is the telemetry they publish. What it sees reaches the story layer through DrainObserved; nothing here draws.
        void StartObserver(RunnerConfig cfg, ReadinessBoard board)
        {
            var watched = new List<string>();
            foreach (var d in board.Devices)
            {
                if (d.Failed || d.Bind == null || !d.Bind.IsBound) continue;
                watched.Add(d.Bind.DeviceToken);
                externalIdByToken[d.Bind.DeviceToken] = d.Device.ExternalId;
                tokenByExternalId[d.Device.ExternalId] = d.Bind.DeviceToken;
            }
            Observed = new ObservedState(DateTimeOffset.UtcNow);
            observer = new PlatformObserver(cfg, broker, watched, Observed, ObserverState);
            observer.Watch = item =>
            {
                observed.Enqueue(item);
                if (recorder == null) return;
                foreach (var line in ObservedRecording.Lines(item, t => externalIdByToken.TryGetValue(t ?? "", out var e) ? e : t)) recorder.Observe(line);
            };
            observer.Start();
        }

        void Stop(string title, string why)
        {
            status = title + ": " + why;
            PlatformLog.Error(status);
        }

        void Update()
        {
            if (Offline)
            {
                double now = Time.realtimeSinceStartup;
                double dt = lastRealtime == 0 ? 0 : now - lastRealtime;
                lastRealtime = now;
                if (plane != null && !Frozen) AdvanceOffline(dt);
                return;
            }
            if (runner == null || status != "running") return;
            runner.Tick(Time.realtimeSinceStartup - startedAt, DateTimeOffset.UtcNow);
            broker?.Tick();
            observer?.Pump();
        }

        void OnGUI()
        {
            if (!DrawStatus) return;
            var sb = new StringBuilder();
            sb.Append("Towerpulse device plane · ").Append(status).Append('\n');
            if (banner.Length > 0) sb.Append(banner).Append('\n');
            if (runner != null)
            {
                sb.Append(runner.Readiness()).Append('\n');
                sb.Append("model ").Append(TimeClock.Format(plane.Model.TimeS)).Append(" · commands waiting ").Append(plane.Commands.PendingCount);
            }
            else if (Offline && plane != null) sb.Append(banner).Append(" · model ").Append(TimeClock.Format(plane.Model.TimeS));
            GUI.Label(new Rect(12, 8, 900, 90), sb.ToString());
        }

        void OnDestroy()
        {
            if (shutdownBegun) return;
            shutdownBegun = true;
            cts.Cancel();
            try { observer?.Dispose(); } catch (Exception e) { Debug.LogWarning(DemoIdentity.LogPrefix + "observer shutdown: " + e.GetType().Name); }
            if (runner != null)
            {
                // best effort and bounded: the sessions are released, but the editor is not held
                try { runner.ShutdownAsync().Wait(PlaneRunner.DisposeTimeout + TimeSpan.FromSeconds(1)); }
                catch (Exception e) { Debug.LogWarning(DemoIdentity.LogPrefix + "shutdown: " + e.GetType().Name); }
            }
            try { recorder?.Close(); } catch (Exception e) { Debug.LogWarning(DemoIdentity.LogPrefix + "recording close: " + e.GetType().Name); }
        }
    }
}
