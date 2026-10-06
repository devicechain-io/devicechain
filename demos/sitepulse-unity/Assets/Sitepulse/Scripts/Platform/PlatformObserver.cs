// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk;
using DeviceChain.Sdk.Subscriptions;
using DeviceChain.Sdk.Transport;
using DeviceChain.Sdk.Unity;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>The live state of one stream, as the main thread last heard it.</summary>
    public sealed class StreamStatus
    {
        public StreamState State { get; internal set; } = StreamState.Idle;

        /// <summary>Why it is reconnecting; or a note on a live stream whose snapshot failed.</summary>
        public string Reason { get; internal set; }

        public DateTimeOffset? Since { get; internal set; }
        public DateTimeOffset? LastLiveAt { get; internal set; }
        public int Reconnects { get; internal set; }
        public bool IsLive => State == StreamState.Live;
    }

    /// <summary>What the observer's two streams and its polls are doing.</summary>
    public sealed class ObserverStatus
    {
        readonly Dictionary<string, string> pollErrors = new Dictionary<string, string>(StringComparer.Ordinal);

        public StreamStatus Measurements { get; } = new StreamStatus();
        public StreamStatus Alarms { get; } = new StreamStatus();

        /// <summary>Poll name to why it is failing; absent while it works.</summary>
        public IReadOnlyDictionary<string, string> PollErrors => pollErrors;

        public int Version { get; internal set; }

        /// <summary>Set while the last alarm snapshot was only part of the platform's list; null otherwise.</summary>
        public string AlarmSnapshotNote { get; private set; }

        internal void SetAlarmSnapshot(string note)
        {
            if (note == AlarmSnapshotNote) return;
            AlarmSnapshotNote = note;
            Version++;
        }

        internal void SetPoll(string name, bool ok, string reason)
        {
            if (ok)
            {
                if (pollErrors.Remove(name)) Version++;
                return;
            }

            if (pollErrors.TryGetValue(name, out var held) && held == reason) return;
            pollErrors[name] = reason;
            Version++;
        }
    }

    /// <summary>
    /// The operator plane's observer: it watches what the platform says about the scene's devices and
    /// puts it into an <see cref="ObservedState"/>, and into nothing else. It never holds a device
    /// credential. Two subscriptions (measurements, the whole tenant; alarms) are kept alive by
    /// <see cref="StreamRunner{T}"/>, each with a snapshot after every (re)subscribe; location, commands
    /// and presence have no subscription and are polled (one batched request each, at 1 Hz, 1 Hz and
    /// every 5 s; a poll backs off while the server is down). The tasks are started from, and continue on, the
    /// main thread's context, so a frame is parsed there too: what crosses to <see cref="Pump"/> through the
    /// inbox is a plain item, and <see cref="Pump"/>, called from <c>Update</c>, applies it to the state. Start it from the main thread: the HTTP transport
    /// captures that thread's context and starts every request there.
    /// </summary>
    public sealed class PlatformObserver : IDisposable
    {
        public static readonly TimeSpan LocationEvery = TimeSpan.FromSeconds(1);
        public static readonly TimeSpan CommandsEvery = TimeSpan.FromSeconds(1);
        public static readonly TimeSpan PresenceEvery = TimeSpan.FromSeconds(5);

        static readonly System.Text.Json.Serialization.Metadata.JsonTypeInfo<JsonElement> Json = PlatformJson.Element;

        readonly RunnerConfig config;
        readonly TokenBroker broker;
        readonly IReadOnlyList<string> deviceTokens;
        readonly Func<DateTimeOffset> clock;
        readonly ObserverInbox inbox = new ObserverInbox();
        readonly List<string> newlyObserved = new List<string>();
        readonly CommandTracker commandTracker = new CommandTracker();
        readonly Dictionary<string, DateTimeOffset> lastLog = new Dictionary<string, DateTimeOffset>(StringComparer.Ordinal);
        CancellationTokenSource cts;
        GraphQlWsClient measurementSocket, alarmSocket;
        int generation, runGeneration;
        bool started;

        /// <param name="state">What the observer writes into (and the cards read from).</param>
        /// <param name="status">What the observer reports about its own streams (and the banner reads).</param>
        public PlatformObserver(RunnerConfig config, TokenBroker broker, IReadOnlyList<string> deviceTokens, ObservedState state, ObserverStatus status, Func<DateTimeOffset> clock = null)
        {
            this.config = config ?? throw new ArgumentNullException(nameof(config));
            this.broker = broker ?? throw new ArgumentNullException(nameof(broker));
            this.deviceTokens = deviceTokens ?? throw new ArgumentNullException(nameof(deviceTokens));
            this.clock = clock ?? (() => DateTimeOffset.UtcNow);
            State = state ?? throw new ArgumentNullException(nameof(state));
            Status = status ?? throw new ArgumentNullException(nameof(status));
        }

        public ObservedState State { get; }
        public ObserverStatus Status { get; }
        public ObserverInbox Inbox => inbox;
        public int Generation => Volatile.Read(ref generation);

        /// <summary>Starts the streams and polls. Main thread. A second call is refused.</summary>
        public void Start()
        {
            if (started) throw new InvalidOperationException("this observer has already been started");
            started = true;
            var alarmUri = ObserverQueries.AlarmSocket(new Uri(config.WsUrl));
            var gen = Interlocked.Increment(ref generation);
            runGeneration = gen;
            cts = new CancellationTokenSource();
            var ct = cts.Token;
            void Post(ObserverItem i)
            {
                i.Generation = gen;
                inbox.Post(i);
            }

            var measurementToken = new HandedToken(broker);
            var alarmToken = new HandedToken(broker);
            measurementSocket = new GraphQlWsClient(new ClientWebSocketFactory(), new Uri(config.WsUrl), measurementToken.Provider);
            alarmSocket = new GraphQlWsClient(new ClientWebSocketFactory(), alarmUri, alarmToken.Provider);
            var state = OperatorQueries.Create(config, broker, Area.DeviceState).AsQueryFn();
            var devices = OperatorQueries.Create(config, broker, Area.DeviceManagement).AsQueryFn();
            var commands = OperatorQueries.Create(config, broker, Area.CommandDelivery).AsQueryFn();
            var none = JsonDocument.Parse(ObserverQueries.NoVariables).RootElement.Clone();


            var measurements = new StreamRunner<JsonElement>(
                c => measurementSocket.SubscribeAsync(ObserverQueries.MeasurementSubscription, none, Json, Json, c),
                c => SnapshotMeasurements(state, Post, c),
                frame =>
                {
                    var m = ObserverQueries.ParseMeasurementFrame(frame, clock());
                    if (m != null) Post(m);
                },
                (s, reason) => Post(new StatusItem("measurements", s.ToString(), reason, clock())),
                measurementToken.RefreshAfterRejection, clock: clock);
            var alarms = new StreamRunner<JsonElement>(
                c => alarmSocket.SubscribeAsync(ObserverQueries.AlarmSubscription, none, Json, Json, c),
                c => SnapshotAlarms(devices, Post, clock, c),
                frame =>
                {
                    var a = ObserverQueries.ParseAlarmFrame(frame, clock());
                    if (a != null) Post(a);
                },
                (s, reason) => Post(new StatusItem("alarms", s.ToString(), reason, clock())),
                alarmToken.RefreshAfterRejection, clock: clock);

            _ = Guard("measurement stream", () => measurements.RunAsync(ct), ct);
            _ = Guard("alarm stream", () => alarms.RunAsync(ct), ct);
            _ = Guard("location poll", () => Poll("locations", LocationEvery, c => PollLocations(state, Post, c), ct), ct);
            _ = Guard("command poll", () => Poll("commands", CommandsEvery, c => PollCommands(commands, commandTracker, Post, clock, c), ct), ct);
            _ = Guard("presence poll", () => Poll("presence", PresenceEvery, c => PollPresence(state, Post, c), ct), ct);
        }


        async Task SnapshotMeasurements(QueryFn state, Action<ObserverItem> post, CancellationToken ct)
        {
            if (deviceTokens.Count == 0) return;
            var data = await state(ObserverQueries.MeasurementsSnapshotQuery(deviceTokens), ObserverQueries.MeasurementsSnapshotVariables(deviceTokens), ct);
            foreach (var m in ObserverQueries.ParseMeasurementsSnapshot(deviceTokens, data, clock())) post(m);
        }

        internal static async Task SnapshotAlarms(QueryFn devices, Action<ObserverItem> post, Func<DateTimeOffset> clock, CancellationToken ct)
        {
            var requested = clock();
            post(await ObserverQueries.FetchActiveAlarms(p => devices(ObserverQueries.ActiveAlarmsQuery, ObserverQueries.ActiveAlarmsVariables(p), ct), requested, clock));
        }

        async Task PollLocations(QueryFn state, Action<ObserverItem> post, CancellationToken ct)
        {
            if (deviceTokens.Count == 0) return;
            var data = await state(ObserverQueries.LocationsQuery, ObserverQueries.TokenListVariables(deviceTokens), ct);
            foreach (var l in ObserverQueries.ParseLocations(data, clock())) post(l);
        }

        async Task PollPresence(QueryFn state, Action<ObserverItem> post, CancellationToken ct)
        {
            if (deviceTokens.Count == 0) return;
            var data = await state(ObserverQueries.PresenceQuery, ObserverQueries.TokenListVariables(deviceTokens), ct);
            foreach (var p in ObserverQueries.ParsePresence(data, clock())) post(p);
        }

        // the non-terminal page every second, plus the commands seen non-terminal last time until they turn terminal
        internal static async Task PollCommands(QueryFn commands, CommandTracker tracker, Action<ObserverItem> post, Func<DateTimeOffset> clock, CancellationToken ct)
        {
            var named = tracker.Named();
            var data = await commands(ObserverQueries.CommandsQuery(named.Length > 0), ObserverQueries.CommandsVariables(named), ct);
            var items = ObserverQueries.ParseCommands(data, clock());
            tracker.Observe(named, items);
            foreach (var c in items) post(c);
        }

        Task Poll(string name, TimeSpan period, Func<CancellationToken, Task> body, CancellationToken ct) =>
            PollLoop.RunAsync(period, body, e =>
            {
                if (e == null)
                {
                    PostPoll(name, true, null);
                    return;
                }

                var reason = StreamFailure.Classify(e).Reason;
                PostPoll(name, false, reason);
                LogEvery(name, $"observer {name} poll failed: {reason}");
            }, clock, (d, c) => Task.Delay(d, c), ct);

        // the last result a poll posted, so a poll that keeps succeeding says nothing more than once
        readonly Dictionary<string, string> pollSaid = new Dictionary<string, string>(StringComparer.Ordinal);

        void PostPoll(string name, bool ok, string reason)
        {
            var said = ok ? "ok" : reason;
            lock (pollSaid)
            {
                if (pollSaid.TryGetValue(name, out var last) && last == said) return;
                pollSaid[name] = said;
            }

            var item = new StatusItem(name, ok ? "ok" : "failed", reason, clock());
            item.Generation = runGeneration;
            inbox.Post(item);
        }

        void LogEvery(string key, string line)
        {
            lock (lastLog)
            {
                var now = clock();
                if (lastLog.TryGetValue(key, out var t) && now - t < TimeSpan.FromSeconds(30)) return;
                lastLog[key] = now;
            }

            PlatformLog.Warn(line);
        }

        async Task Guard(string what, Func<Task> run, CancellationToken ct)
        {
            try
            {
                await run();
            }
            catch (OperationCanceledException) when (ct.IsCancellationRequested)
            {
                // stopped
            }
            catch (Exception e)
            {
                PlatformLog.Error($"the observer's {what} stopped: {e.GetType().Name}: {e.Message}");
            }
        }

        /// <summary>
        /// Applies everything the network tasks have queued. Main thread, once a frame. Returns the device
        /// tokens whose first own-run telemetry the platform confirmed in this call.
        /// </summary>
        public IReadOnlyList<string> Pump()
        {
            newlyObserved.Clear();
            inbox.Drain(Generation, item =>
            {
                Tap(item);
                ObserverApplier.Apply(State, Status, item, newlyObserved);
            });
            return newlyObserved;
        }

        /// <summary>
        /// Sees every item just before it is applied, on the main thread, in the order it is applied (the recorder's view of what the
        /// viewer was shown). A tap that throws is dropped, once and with a warning: watching must never stop the observer.
        /// </summary>
        public Action<ObserverItem> OnItem { get; set; }

        void Tap(ObserverItem item)
        {
            var tap = OnItem;
            if (tap == null) return;
            try
            {
                tap(item);
            }
            catch (Exception e)
            {
                OnItem = null;
                PlatformLog.Warn($"the observer's tap failed and was removed: {e.GetType().Name}: {e.Message}");
            }
        }

        public void Dispose()
        {
            if (!started) return;
            started = false;
            // the generation moves first, so nothing still in flight is believed
            Interlocked.Increment(ref generation);
            cts?.Cancel();
            var a = measurementSocket;
            var b = alarmSocket;
            measurementSocket = alarmSocket = null;
            _ = Task.Run(async () =>
            {
                try { if (a != null) await a.DisposeAsync(); } catch { /* closing a socket that is already gone */ }
                try { if (b != null) await b.DisposeAsync(); } catch { /* closing a socket that is already gone */ }
            });
            cts?.Dispose();
            cts = null;
        }
    }

    /// <summary>
    /// The one place an <see cref="ObserverItem"/> becomes state: pure over the state and status it is
    /// handed, so the merge rules and the stream bookkeeping are testable without a network.
    /// </summary>
    public static class ObserverApplier
    {
        internal static void Apply(ObservedState state, ObserverStatus status, ObserverItem item, List<string> newlyObserved)
        {
            switch (item)
            {
                case MeasurementItem m:
                    if (state.ApplyMeasurement(m.DeviceToken, m.Name, m.Value, m.OccurredAt, m.ObservedAt, m.FromSnapshot))
                        newlyObserved?.Add(m.DeviceToken);
                    break;
                case AlarmItem a:
                    state.ApplyAlarm(a.DeviceToken, a.Alarm);
                    break;
                case AlarmSnapshotItem s:
                    var listed = new HashSet<string>(StringComparer.Ordinal);
                    foreach (var a in s.Alarms)
                    {
                        listed.Add(a.Alarm.Token);
                        state.ApplyAlarm(a.DeviceToken, a.Alarm);
                    }

                    // a list that holds only part of the platform's alarms cannot say that an unlisted one ended
                    if (!s.Truncated) state.ReconcileActiveAlarms(listed, s.RequestedAt, s.ObservedAt);
                    status.SetAlarmSnapshot(s.Truncated ? $"alarm snapshot partial ({s.Alarms.Count} of {(s.TotalRecords.HasValue ? s.TotalRecords.Value.ToString() : "more")}): ended alarms are not cleared" : null);
                    break;
                case LocationItem l:
                    state.ApplyLocation(l.DeviceToken, l.Location);
                    break;
                case CommandItem c:
                    state.ApplyCommand(c.DeviceToken, c.Command);
                    break;
                case PresenceItem p:
                    state.ApplyPresence(p.DeviceToken, p.Presence);
                    break;
                case StatusItem st:
                    ApplyStatus(status, st);
                    break;
            }
        }

        static void ApplyStatus(ObserverStatus status, StatusItem st)
        {
            if (st.Source == "measurements" || st.Source == "alarms")
            {
                var stream = st.Source == "measurements" ? status.Measurements : status.Alarms;
                StreamState next;
                switch (st.State)
                {
                    case "Connecting": next = StreamState.Connecting; break;
                    case "Subscribed": next = StreamState.Subscribed; break;
                    case "Live": next = StreamState.Live; break;
                    case "Reconnecting": next = StreamState.Reconnecting; break;
                    case "Idle": next = StreamState.Idle; break;
                    default: return;
                }

                if (stream.State != next)
                {
                    if (next == StreamState.Reconnecting && (stream.State == StreamState.Live || stream.State == StreamState.Subscribed)) stream.Reconnects++;
                    stream.State = next;
                    stream.Since = st.At;
                    if (next == StreamState.Live) stream.LastLiveAt = st.At;
                }

                stream.Reason = st.Reason;
                status.Version++;
                return;
            }

            status.SetPoll(st.Source, st.State == "ok", st.Reason);
        }
    }
}
