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
    /// platform's address and the operator token (refreshed by the shared <see cref="TokenBroker"/>), resolves the layout's 37 devices by external
    /// ID with the shared binder's query and contract, and runs the shared <see cref="PlatformObserver"/> over the ones that bound: measurement and
    /// alarm subscriptions, location, command and presence polls, all through the DeviceChain C# SDK.
    ///
    /// It never writes. It opens no device session, creates no credential (the shared binder's credential step is the one part of it this
    /// session does not run) and sends no command: everything the scene shows is the platform's own record, read under the operator's token.
    /// </summary>
    public sealed class HarborLiveSession : IDisposable
    {
        readonly HarborLayout layout;
        readonly LiveSettings settings;
        readonly CancellationTokenSource cts = new CancellationTokenSource();
        readonly Dictionary<string, string> tokenByExternalId = new Dictionary<string, string>(StringComparer.Ordinal);
        TokenBroker broker;
        PlatformObserver observer;

        public HarborLiveSession(HarborLayout layout, LiveSettings settings)
        {
            this.layout = layout ?? throw new ArgumentNullException(nameof(layout));
            this.settings = settings ?? throw new ArgumentNullException(nameof(settings));
            Source = new ObservedSource(this);
        }

        /// <summary>Where the session is: "connecting", "binding", "observing", or why it stopped.</summary>
        public string Stage { get; private set; } = "not started";

        /// <summary>Set when the session cannot go on; the scene shows it and does not pretend to be live.</summary>
        public string Failure { get; private set; }

        public RunnerConfig Config { get; private set; }
        public ObservedState Observed { get; private set; }
        public ObserverStatus ObserverStatus { get; } = new ObserverStatus();
        public TokenBroker Broker => broker;

        /// <summary>The platform's readings, by the layout's external IDs; empty until the observer runs.</summary>
        public IReadingSource Source { get; }

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
            broker?.Tick();
            observer?.Pump();
        }

        public void Dispose()
        {
            cts.Cancel();
            try { observer?.Dispose(); }
            catch (Exception e) { PlatformLog.Warn("observer shutdown: " + e.GetType().Name); }
        }

        sealed class ObservedSource : IReadingSource
        {
            readonly HarborLiveSession session;

            public ObservedSource(HarborLiveSession session)
            {
                this.session = session;
            }

            public bool TryGet(string externalId, string metric, out Reading reading)
            {
                reading = default;
                var state = session.Observed;
                if (state == null || !session.tokenByExternalId.TryGetValue(externalId ?? "", out var token)) return false;
                if (!state.TryGet(token, out var device) || !device.Measurements.TryGetValue(metric, out var v)) return false;
                reading = new Reading(v.Value, v.OccurredAt);
                return true;
            }
        }
    }
}
