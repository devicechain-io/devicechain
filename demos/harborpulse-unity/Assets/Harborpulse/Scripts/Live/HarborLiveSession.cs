// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Demos.Platform;
using DeviceChain.Harborpulse.Model;

namespace DeviceChain.Harborpulse.Live
{
    /// <summary>
    /// The scene's READ-ONLY link to a DeviceChain instance running the harborpulse simulation. It fetches the runner's <c>/config.json</c> for the
    /// platform's address and the operator token (refreshed by the shared <see cref="TokenBroker"/>), resolves the layout's 43 devices by external
    /// ID with the shared binder's query and contract, and runs the shared <see cref="PlatformObserver"/> over the ones that bound: measurement and
    /// alarm subscriptions, location, command and presence polls, all through the DeviceChain C# SDK.
    ///
    /// It never writes. It opens no device session, creates no credential (the shared binder's credential step is the one part of it this
    /// session does not run) and sends no command: everything the scene shows is the platform's own record, read under the operator's token.
    /// </summary>
    public sealed class HarborLiveSession : IDisposable, IHarborSource
    {
        readonly HarborLayout layout;
        readonly LiveSettings settings;
        readonly CancellationTokenSource cts = new CancellationTokenSource();
        readonly Dictionary<string, string> tokenByExternalId = new Dictionary<string, string>(StringComparer.Ordinal);
        HarborPlatformReads reads;
        DirectorReads directorReads;
        readonly ScenarioClockFollower clock = new ScenarioClockFollower();
        readonly ScenarioStatusClient statusClient;
        TokenBroker broker;
        PlatformObserver observer;

        public HarborLiveSession(HarborLayout layout, LiveSettings settings)
        {
            this.layout = layout ?? throw new ArgumentNullException(nameof(layout));
            this.settings = settings ?? throw new ArgumentNullException(nameof(settings));
            statusClient = new ScenarioStatusClient(settings.RunnerUrl, clock);
        }

        /// <summary>Where the session is: "connecting", "binding", "observing", or why it stopped.</summary>
        public string Stage { get; private set; } = "not started";

        /// <summary>Set when the session cannot go on; the scene shows it and does not pretend to be live.</summary>
        public string Failure { get; private set; }

        /// <summary>CONTROL (-hp-control-no-cause), set before Start: the director reads commands without their recorded cause.</summary>
        public bool OmitCommandCause { get; set; }

        public RunnerConfig Config { get; private set; }
        public ObservedState Observed { get; private set; }
        public ObserverStatus ObserverStatus { get; } = new ObserverStatus();
        public TokenBroker Broker => broker;
        public DeviceChain.Harborpulse.Model.Director.DirectorReadSnapshot DirectorReads => directorReads?.Snapshot;

        /// <summary>The platform's readings, by the layout's external IDs; empty until the observer runs.</summary>
        public IHarborSource Source => this;

        public int Bound { get; private set; }

        /// <summary>Each device that did not bind, with the binder's own account of why.</summary>
        public IReadOnlyList<string> BindProblems => bindProblems;

        readonly List<string> bindProblems = new List<string>();

        public bool TryToken(string externalId, out string token) => tokenByExternalId.TryGetValue(externalId ?? "", out token);

        /// <summary>Starts connecting. Main thread: the transports capture its context.</summary>
        public void Start() => _ = Run(cts.Token);

        async Task Run(CancellationToken ct)
        {
            try
            {
                Stage = "fetching the runner's config";
                var client = new RunnerConfigClient(settings, RunnerUse.ObserveOnly);
                var cfg = await client.Fetch(ct);
                if (!cfg.Ok)
                {
                    Fail("the runner's config is not usable: " + cfg.Error);
                    return;
                }

                Config = cfg.Value;
                broker = new TokenBroker(cfg.Value, client.Fetch);
                PlatformLog.Info("live · " + cfg.Value + " · read-only");

                Stage = "resolving the fleet";
                var devices = HarborScene.Devices(layout);
                var ids = new List<string>();
                foreach (var d in devices) ids.Add(d.ExternalId);
                var query = OperatorQueries.Create(cfg.Value, broker).AsQueryFn();
                var data = await query(DeviceBinder.ResolveQuery, VarsJson.StringList("ids", ids), ct);
                var results = DeviceBinder.Classify(HarborScene.Contract(layout), devices, data);
                var watched = new List<string>();
                foreach (var r in results)
                {
                    if (!r.IsBound)
                    {
                        bindProblems.Add(r.Describe(cfg.Value.Tenant));
                        continue;
                    }

                    tokenByExternalId[r.ExternalId] = r.DeviceToken;
                    watched.Add(r.DeviceToken);
                }

                Bound = watched.Count;
                PlatformLog.Info("bound " + Bound + "/" + devices.Count + (bindProblems.Count > 0 ? " · " + string.Join(" | ", bindProblems) : ""));
                ct.ThrowIfCancellationRequested();
                if (watched.Count == 0)
                {
                    Fail("no device of the layout resolved on tenant " + cfg.Value.Tenant + "; is the runner running the harborpulse manifest?");
                    return;
                }

                Observed = new ObservedState(DateTimeOffset.UtcNow);
                observer = new PlatformObserver(cfg.Value, broker, watched, Observed, ObserverStatus);
                reads = new HarborPlatformReads(Config, broker, layout, id => TryToken(id, out var t) ? t : null);
                directorReads = new DirectorReads(Config, broker, layout, id => TryToken(id, out var t) ? t : null, ObserverStatus, OmitCommandCause);
                observer.Watch += directorReads.Watch;
                observer.Start();
                Stage = "observing";
            }
            catch (OperationCanceledException)
            {
            }
            catch (Exception e)
            {
                Fail(e.GetType().Name + ": " + e.Message);
            }
        }

        void Fail(string why)
        {
            Failure = why;
            Stage = "stopped";
            PlatformLog.Error(why);
        }

        /// <summary>Main thread, every frame: refresh the token on schedule and apply what the observer received.</summary>
        public void Tick()
        {
            statusClient.Tick(DateTimeOffset.UtcNow);
            broker?.Tick();
            observer?.Pump();
            reads?.Tick(Status, DateTimeOffset.UtcNow);
            directorReads?.Tick(Status, DateTimeOffset.UtcNow);
        }

        public void Dispose()
        {
            cts.Cancel();
            statusClient.Dispose();
            reads?.Dispose();
            directorReads?.Dispose();
            try { observer?.Dispose(); }
            catch (Exception e) { PlatformLog.Warn("observer shutdown: " + e.GetType().Name); }
        }

        public ScenarioStatus Status => statusClient.Last;
        public ScenarioClockFollower Clock => clock;
        public RolloutEvidence Rollout => reads?.Rollout;
        public IReadOnlyList<SuppressionEvidence> Suppressions => reads?.Suppressions ?? Array.Empty<SuppressionEvidence>();
        public string ReadProblem => reads?.Problem ?? statusClient.Problem;
        public CommandCoverage CommandPoll => new CommandCoverage(ObserverStatus.CommandsHealthySince, ObserverStatus.CommandsLastOk)
            .Intersect(reads?.CommandCoverage(DateTimeOffset.UtcNow) ?? default);
        // A live stream is reported only after its snapshot attempt; a failed attempt remains in Reason.
        public AlarmCoverage AlarmObservation => new AlarmCoverage(ObserverStatus.AlarmSnapshotNote == null && ObserverStatus.Alarms.Reason == null, ObserverStatus.Alarms.IsLive);
        public IReadOnlyList<HarborAlarm> Alarms
        {
            get
            {
                var list = new List<HarborAlarm>();
                if (Observed != null) foreach (var p in tokenByExternalId)
                    if (Observed.TryGet(p.Value, out var d)) foreach (var a in d.Alarms.Values)
                        list.Add(new HarborAlarm(p.Key, a.AlarmKey, a.State, a.Severity, a.Acknowledged, a.OccurredAt, a.ObservedAt));
                return list.AsReadOnly();
            }
        }
        public IReadOnlyList<HarborCommand> Commands
        {
            get
            {
                var list = new List<HarborCommand>();
                if (reads != null) list.AddRange(reads.Commands);
                if (Observed != null) foreach (var p in tokenByExternalId)
                    if (Observed.TryGet(p.Value, out var d))
                    {
                        foreach (var c in d.Commands.Values) list.Add(new HarborCommand(p.Key, c.Token, c.Name, c.Status, c.ObservedAt));
                        var last = d.LastCommand;
                        if (last != null && !d.Commands.ContainsKey(last.Token)) list.Add(new HarborCommand(p.Key, last.Token, last.Name, last.Status, last.ObservedAt));
                    }
                return list.AsReadOnly();
            }
        }
        public bool TryLocation(string externalId, out HarborLocation location)
        {
            location = default;
            if (Observed == null || !TryToken(externalId, out var token) || !Observed.TryGet(token, out var d) || d.Location == null) return false;
            var p = d.Location;
            location = new HarborLocation(p.Latitude, p.Longitude, p.HeadingDegrees, p.SpeedMps, p.OccurredAt);
            return true;
        }
        public bool TryAttribute(string externalId, string key, out Reading value)
        {
            value = default;
            return reads != null && reads.TryAttribute(externalId, key, out value);
        }
        public bool TryGet(string externalId, string metric, out Reading reading)
        {
            reading = default;
            if (Observed == null || !TryToken(externalId, out var token) || !Observed.TryGet(token, out var d) || !d.Measurements.TryGetValue(metric, out var v)) return false;
            reading = new Reading(v.Value, v.OccurredAt);
            return true;
        }
    }
}
