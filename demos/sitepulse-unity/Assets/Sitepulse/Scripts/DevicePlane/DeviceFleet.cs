// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.DevicePlane
{
    /// <summary>
    /// The device plane: one <see cref="DeviceSessionHost"/> per credentialed device, started at most
    /// four at a time, fed by the local simulation, and reported on the readiness board. It holds
    /// device credentials only (it never sees the operator token). One plane is one run: it has a
    /// generation, every callback from its sessions carries it, and shutting it down bumps it, so
    /// nothing a torn-down run says can touch the next one. A plane never starts a device twice, and a
    /// new plane is built only after the old one's sessions are disposed, so two sessions never share
    /// a client id.
    ///
    /// The main thread owns everything here except the hosts' pumps: <see cref="StartAll"/>,
    /// <see cref="Pump"/> and <see cref="Advance"/> are called from <c>Update</c>.
    /// </summary>
    public sealed class DeviceFleet
    {
        public const int MaxStartsInFlight = 4;
        public static readonly TimeSpan DisposeTimeout = TimeSpan.FromSeconds(6);

        readonly ReadinessBoard board;
        readonly DeviceCredentials credentials;
        readonly IDeviceLinkFactory factory;
        readonly int maxStarts;
        readonly DeviceInbox inbox = new DeviceInbox();
        readonly List<DeviceSessionHost> hosts = new List<DeviceSessionHost>();
        readonly Dictionary<string, DeviceSessionHost> byId = new Dictionary<string, DeviceSessionHost>(StringComparer.Ordinal);
        readonly CancellationTokenSource cts = new CancellationTokenSource();
        readonly List<Task> disposals = new List<Task>();
        readonly List<Sample> scratch = new List<Sample>();
        int generation;
        bool begun;
        DateTimeOffset lastAdvance = DateTimeOffset.MinValue;
        Task shutdown;

        public DeviceFleet(ReadinessBoard board, DeviceCredentials credentials, IDeviceLinkFactory factory, int generation = 1, int maxStartsInFlight = MaxStartsInFlight)
        {
            this.board = board ?? throw new ArgumentNullException(nameof(board));
            this.credentials = credentials ?? throw new ArgumentNullException(nameof(credentials));
            this.factory = factory ?? throw new ArgumentNullException(nameof(factory));
            maxStarts = maxStartsInFlight < 1 ? 1 : maxStartsInFlight;
            this.generation = generation;
        }

        /// <summary>The run generation: callbacks stamped with any other value are dropped.</summary>
        public int Generation => Volatile.Read(ref generation);

        public IReadOnlyList<DeviceSessionHost> Hosts => hosts;
        public DeviceInbox Inbox => inbox;
        public bool IsShutDown => shutdown != null;

        public DeviceSessionHost this[string externalId] => byId[externalId];

        /// <summary>
        /// Builds a host for every device that has a credential and starts them, at most
        /// <see cref="MaxStartsInFlight"/> at a time so 19 connects do not arrive at the broker's auth
        /// callout as one burst. Returns when every start attempt has finished; run it without
        /// awaiting from <c>Update</c>. A device that failed to bind or to get a credential has no
        /// host and so never publishes.
        /// </summary>
        public Task StartAll()
        {
            if (begun) throw new InvalidOperationException("this device plane has already been started");
            begun = true;
            board.BeginSessions();
            var starts = new List<Func<Task>>();
            foreach (var d in board.Devices)
            {
                if (d.Failed || d.Stage != DeviceStage.Connecting) continue;
                var id = d.Device.ExternalId;
                if (!credentials.TryGet(d.Bind.DeviceToken, out var credentialId))
                {
                    board.FailSession(id, "no credential for the session");
                    continue;
                }

                DeviceSessionHost host;
                try
                {
                    var link = factory.Create(id, d.Bind.DeviceToken, credentialId);
                    host = new DeviceSessionHost(d.Device, d.Bind.DeviceToken, link, Generation, inbox);
                }
                catch (Exception e)
                {
                    board.FailSession(id, "session could not be created · " + Redactor.Redact(e.GetType().Name + ": " + e.Message));
                    continue;
                }

                hosts.Add(host);
                byId[id] = host;
                starts.Add(() => host.StartAsync(cts.Token));
            }

            return RunStarts(starts, cts.Token);
        }

        async Task RunStarts(List<Func<Task>> starts, CancellationToken ct)
        {
            var gate = new SemaphoreSlim(maxStarts);
            var tasks = new List<Task>(starts.Count);
            foreach (var start in starts)
            {
                var s = start;
                tasks.Add(Task.Run(async () =>
                {
                    try
                    {
                        await gate.WaitAsync(ct).ConfigureAwait(false);
                    }
                    catch (OperationCanceledException)
                    {
                        return;
                    }

                    try
                    {
                        await s().ConfigureAwait(false);
                    }
                    finally
                    {
                        gate.Release();
                    }
                }));
            }

            await Task.WhenAll(tasks).ConfigureAwait(false);
        }

        /// <summary>Applies queued session events to the board and copies the counters in. Main thread, once a frame.</summary>
        public void Pump()
        {
            if (shutdown != null) return;
            inbox.Drain(Generation, Apply);
            foreach (var h in hosts)
                board.SetStats(h.ExternalId, h.Published, h.SendErrors, h.Ring.Dropped, h.LastPublishUtc);
        }

        void Apply(DeviceEvent e)
        {
            if (!byId.TryGetValue(e.ExternalId, out var host)) return;
            var id = e.ExternalId;
            var d = board[id];
            switch (e.Kind)
            {
                case DeviceEventKind.LinkState:
                    switch (e.State)
                    {
                        case LinkState.Starting:
                            if (d.Stage < DeviceStage.Connecting) board.SetStage(id, DeviceStage.Connecting);
                            break;
                        case LinkState.Ready:
                            if (d.Stage < DeviceStage.Ready) board.SetStage(id, DeviceStage.Ready);
                            board.SetSide(id, DeviceSide.None);
                            break;
                        case LinkState.Reconnecting:
                            board.SetSide(id, DeviceSide.Reconnecting);
                            break;
                        case LinkState.Blind:
                            board.SetSide(id, DeviceSide.Blind);
                            board.FailSession(id, "blind · the broker refused this device; it will not publish and no command can reach it");
                            Retire(host);
                            break;
                        case LinkState.Stopped:
                            if (!d.Failed) board.SetSide(id, DeviceSide.Stopped);
                            break;
                    }

                    break;
                case DeviceEventKind.Started:
                    if (d.Stage < DeviceStage.Ready) board.SetStage(id, DeviceStage.Ready);
                    break;
                case DeviceEventKind.StartFailed:
                    board.FailSession(id, "session could not start · " + Redactor.Redact(e.Text ?? ""));
                    Retire(host);
                    break;
                case DeviceEventKind.FirstPublish:
                    board.SetStage(id, DeviceStage.Publishing);
                    break;
                case DeviceEventKind.Command:
                    board.SetCommand(id, e.Text);
                    break;
            }
        }

        // a device with no working session is released at once: its client id is free and it never publishes
        void Retire(DeviceSessionHost host)
        {
            host.Halt();
            lock (disposals) disposals.Add(host.DisposeAsync(DisposeTimeout));
        }

        /// <summary>
        /// Advances every live device's simulation by the real time since the last call and queues
        /// what is due. A device that failed, is blind, or has no session yet produces nothing to send.
        /// </summary>
        public void Advance(DateTimeOffset now, IPoseSource poses)
        {
            if (shutdown != null || hosts.Count == 0) return;
            var elapsed = lastAdvance == DateTimeOffset.MinValue ? 0.0 : (now - lastAdvance).TotalSeconds;
            lastAdvance = now;
            if (elapsed < 0) elapsed = 0;
            if (elapsed > 1.0) elapsed = 1.0;

            foreach (var h in hosts)
            {
                if (!poses.TryGet(h.ExternalId, out var pose)) continue;
                scratch.Clear();
                h.Simulation.Advance(now, elapsed, pose, h.Accepting, scratch);
                foreach (var s in scratch) h.Enqueue(s);
            }
        }

        /// <summary>
        /// Teardown: bump the generation first (so nothing still in flight is believed), then dispose
        /// every session, waiting at most <see cref="DisposeTimeout"/> for each. Idempotent.
        /// </summary>
        public Task ShutdownAsync()
        {
            if (shutdown != null) return shutdown;
            Interlocked.Increment(ref generation);
            cts.Cancel();
            var all = new List<Task>();
            foreach (var h in hosts) all.Add(h.DisposeAsync(DisposeTimeout));
            lock (disposals) all.AddRange(disposals);
            shutdown = Task.WhenAll(all);
            return shutdown;
        }

        /// <summary>Teardown for a caller that cannot await (quit, destroy): blocks for at most <paramref name="timeout"/>.</summary>
        public bool Shutdown(TimeSpan timeout)
        {
            var t = ShutdownAsync();
            try
            {
                return Task.Run(() => t).Wait(timeout);
            }
            catch (AggregateException)
            {
                return true;
            }
        }
    }
}
