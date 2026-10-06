// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Simulation;
using DeviceChain.Sitepulse.Tasks;

namespace DeviceChain.Sitepulse.DevicePlane
{
    /// <summary>What a session needs to judge a command: the areas its profile lists and whether the scene has somewhere to send a machine.</summary>
    public sealed class CommandContext
    {
        public CommandContext(IReadOnlyCollection<string> profileAreas, Func<string, bool> sceneHasZone)
        {
            ProfileAreas = profileAreas ?? Array.Empty<string>();
            SceneHasZone = sceneHasZone ?? (_ => false);
        }

        public IReadOnlyCollection<string> ProfileAreas { get; }
        public Func<string, bool> SceneHasZone { get; }
    }

    /// <summary>
    /// One device's session and everything that belongs to it: the link, the bounded outbound ring,
    /// the pump that sends it, and the counters the panel shows. SDK callbacks land on pool threads
    /// and are turned into plain <see cref="DeviceEvent"/> records for the main thread; the counters
    /// are interlocked fields the main thread reads. Credentials only ever pass from the plane to the
    /// link factory; this class never sees one.
    /// </summary>
    public sealed class DeviceSessionHost : ISampleSink, ISampleObserver
    {
        public const string PlantRefusal = "the crusher accepts no commands";

        readonly IDeviceLink link;
        readonly int generation;
        readonly DeviceInbox inbox;
        readonly CancellationTokenSource stop = new CancellationTokenSource();
        readonly object gate = new object();
        Task pumpTask = Task.CompletedTask;
        Task disposeTask;
        int started, halted, firstPublish;
        long published, sendErrors, lastPublishTicks;
        int consecutiveFailures, inFlight;
        long arrivalFallback;
        readonly CommandContext commands;

        public DeviceSessionHost(SceneDevice device, string deviceToken, IDeviceLink link, int generation, DeviceInbox inbox, CommandContext commands = null)
        {
            this.commands = commands ?? new CommandContext(null, null);
            Device = device;
            DeviceToken = deviceToken;
            this.link = link ?? throw new ArgumentNullException(nameof(link));
            this.generation = generation;
            this.inbox = inbox ?? throw new ArgumentNullException(nameof(inbox));
            Simulation = new DeviceSimulation(ToEquipment(device.Kind), device.ExternalId);
            Pump = new SamplePump(Ring, this, () => Simulation.NominalRatePerSecond, this);
            link.StateChanged += OnLinkState;
        }

        public SceneDevice Device { get; }
        public string ExternalId => Device.ExternalId;
        public string DeviceToken { get; }
        public DeviceSimulation Simulation { get; }
        public OutboundRing Ring { get; } = new OutboundRing();
        public SamplePump Pump { get; }

        public long Published => Interlocked.Read(ref published);
        public long SendErrors => Interlocked.Read(ref sendErrors);

        /// <summary>Failed sends since the last acknowledged one.</summary>
        public int ConsecutiveSendFailures => Volatile.Read(ref consecutiveFailures);

        public DateTimeOffset? LastPublishUtc
        {
            get
            {
                var t = Interlocked.Read(ref lastPublishTicks);
                return t == 0 ? (DateTimeOffset?)null : new DateTimeOffset(t, TimeSpan.Zero);
            }
        }

        /// <summary>True once the session is up and until it is halted: only then does the device queue what it says.</summary>
        public bool Accepting => Volatile.Read(ref started) == 1 && Volatile.Read(ref halted) == 0;

        public bool CanPublish => Volatile.Read(ref halted) == 0 && link.CanPublish;

        public static EquipmentKind ToEquipment(SceneKind kind)
        {
            switch (kind)
            {
                case SceneKind.Hauler: return EquipmentKind.Hauler;
                case SceneKind.Loader: return EquipmentKind.Loader;
                case SceneKind.Dozer: return EquipmentKind.Dozer;
                default: return EquipmentKind.Plant;
            }
        }

        /// <summary>Commands whose handler has not returned yet (waiting on the simulation for an answer).</summary>
        public int CommandsInFlight => Volatile.Read(ref inFlight);

        void Post(DeviceEventKind kind, LinkState state = LinkState.Starting, string text = null)
            => inbox.Post(new DeviceEvent(generation, ExternalId, kind, state, text));

        void OnLinkState(LinkState state)
        {
            // a refused device never publishes: stop it here, on the SDK thread, before the main thread hears
            if (state == LinkState.Blind) Halt();
            Post(DeviceEventKind.LinkState, state);
        }

        /// <summary>Connects. A host that has been halted or disposed never starts its session.</summary>
        public async Task StartAsync(CancellationToken ct)
        {
            if (Volatile.Read(ref halted) == 1) return;
            Post(DeviceEventKind.LinkState, LinkState.Starting);
            try
            {
                using (var linked = CancellationTokenSource.CreateLinkedTokenSource(ct, stop.Token))
                    await link.StartAsync(HandleAsync, linked.Token).ConfigureAwait(false);
            }
            catch (OperationCanceledException) when (ct.IsCancellationRequested || stop.IsCancellationRequested)
            {
                return;
            }
            catch (Exception e)
            {
                Halt();
                Post(DeviceEventKind.StartFailed, LinkState.Stopped, e.GetType().Name + ": " + e.Message);
                return;
            }

            lock (gate)
            {
                if (Volatile.Read(ref halted) == 1) return;
                Volatile.Write(ref started, 1);
                pumpTask = Task.Run(() => Pump.RunAsync(stop.Token)).ContinueWith(
                    t => OnPumpFaulted(t.Exception),
                    CancellationToken.None, TaskContinuationOptions.OnlyOnFaulted, TaskScheduler.Default);
            }

            Post(DeviceEventKind.Started, LinkState.Ready);
        }

        // a send loop that died (not one that was stopped) leaves a device that can never publish again: say so
        void OnPumpFaulted(AggregateException e)
        {
            Halt();
            var inner = e?.GetBaseException();
            var why = inner == null ? "unknown" : inner.GetType().Name + ": " + inner.Message;
            PlatformLog.Error($"{ExternalId}: send loop faulted: {why}");
            Post(DeviceEventKind.PumpFaulted, LinkState.Stopped, why);
        }

        /// <summary>
        /// The SDK's command handler, on a pool thread: judge the command without touching Unity, hand a valid
        /// one to the main thread as a <see cref="TaskRequest"/> and wait for its answer. The request's
        /// completion source runs continuations asynchronously and is awaited, never blocked on, so the
        /// main thread completing it cannot run this thread's code, and a shutdown or a stuck simulation
        /// still ends in an answer (a failure that says so) and not in silence.
        /// </summary>
        public async Task<CommandOutcome> HandleAsync(DeviceCommand command, CancellationToken cancellationToken)
        {
            Interlocked.Increment(ref inFlight);
            try
            {
                var shown = string.IsNullOrEmpty(command.Name) ? "(unnamed)" : CommandValidator.Clip(command.Name);
                var check = CommandValidator.Validate(command.Name, command.Payload, Device.Kind == SceneKind.Plant, commands.ProfileAreas, commands.SceneHasZone);
                if (!check.Ok)
                {
                    Post(DeviceEventKind.CommandRefused, LinkState.Starting, $"{shown}: {check.Reason}");
                    return CommandOutcome.Failed(check.Reason);
                }

                var sequence = command.Sequence > 0 ? command.Sequence : Interlocked.Increment(ref arrivalFallback);
                var request = new TaskRequest(command.Token, command.Name, check.Area, sequence, generation);
                inbox.Post(new DeviceEvent(generation, ExternalId, DeviceEventKind.Task, LinkState.Starting, shown, request));

                using (var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(TaskBudgets.LongestWallCapSeconds + 60)))
                using (var linked = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, timeout.Token))
                using (linked.Token.Register(() => request.Complete(TaskResult.Fail(
                    cancellationToken.IsCancellationRequested ? "the device shut down before completion" : "the simulation gave no answer in time"))))
                {
                    var result = await request.Completion.ConfigureAwait(false);
                    return result.Succeeded ? CommandOutcome.Succeeded(result.Reason) : CommandOutcome.Failed(result.Reason);
                }
            }
            finally
            {
                Interlocked.Decrement(ref inFlight);
            }
        }

        /// <summary>Queues a sample for sending. The ring is bounded: the oldest goes when it is full.</summary>
        public void Enqueue(Sample sample)
        {
            if (!Accepting) return;
            Ring.Enqueue(sample);
            if (sample.Kind == SampleKind.Measurement) LastMeasurementSampleUtc = sample.OccurredUtc;
        }

        /// <summary>When the local model last produced a measurement sample (main thread only); null until one has.</summary>
        public DateTimeOffset? LastMeasurementSampleUtc { get; private set; }

        /// <summary>Stops sampling and sending for good. The session itself is released by <see cref="DisposeAsync"/>.</summary>
        public void Halt()
        {
            Volatile.Write(ref halted, 1);
            try { stop.Cancel(); }
            catch (ObjectDisposedException) { }
        }

        /// <summary>Halts, then disposes the session, waiting at most <paramref name="timeout"/> for each part. Idempotent.</summary>
        public Task DisposeAsync(TimeSpan timeout)
        {
            lock (gate)
            {
                if (disposeTask == null) disposeTask = DisposeCoreAsync(timeout);
                return disposeTask;
            }
        }

        async Task DisposeCoreAsync(TimeSpan timeout)
        {
            Halt();
            Task pump;
            lock (gate) pump = pumpTask;
            await Task.WhenAny(pump, Task.Delay(timeout)).ConfigureAwait(false);
            try
            {
                var dispose = Task.Run(async () => await link.DisposeAsync().ConfigureAwait(false));
                await Task.WhenAny(dispose, Task.Delay(timeout)).ConfigureAwait(false);
            }
            catch (Exception e)
            {
                PlatformLog.Warn($"{ExternalId}: session dispose: {e.GetType().Name}");
            }
        }

        void ISampleObserver.Published(Sample sample, DateTimeOffset at)
        {
            Interlocked.Increment(ref published);
            Volatile.Write(ref consecutiveFailures, 0);
            Interlocked.Exchange(ref lastPublishTicks, at.UtcTicks);
            if (Interlocked.Exchange(ref firstPublish, 1) == 0) Post(DeviceEventKind.FirstPublish);
        }

        void ISampleObserver.SendFailed(Sample sample, string reason, bool permanent)
        {
            Interlocked.Increment(ref sendErrors);
            if (!permanent) Interlocked.Increment(ref consecutiveFailures);
            PlatformLog.Warn($"{ExternalId}: send {(permanent ? "rejected" : "failed, will retry")} ({sample.Kind}): {reason}");
        }

        Task ISampleSink.PublishAsync(Sample sample, CancellationToken cancellationToken) => link.PublishAsync(sample, cancellationToken);
    }
}
