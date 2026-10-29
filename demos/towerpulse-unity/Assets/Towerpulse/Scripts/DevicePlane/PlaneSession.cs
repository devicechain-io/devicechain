// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Demos.Platform;
using DeviceChain.Demos.Platform.DevicePlane;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Towerpulse.Plane;

namespace DeviceChain.Towerpulse.DevicePlane
{
    /// <summary>A command that has arrived on a session's thread and is waiting for the main thread to apply it to the model.</summary>
    public sealed class InboundCommand
    {
        public InboundCommand(PlaneDevice device, string token, string name, CommandArgs args)
        {
            Device = device; Token = token; Name = name; Args = args;
        }

        public PlaneDevice Device { get; }
        public string Token { get; }
        public string Name { get; }
        public CommandArgs Args { get; }

        /// <summary>Completed by the main thread with the command's real outcome. Continuations run asynchronously, so completing it cannot run session code inline.</summary>
        public TaskCompletionSource<CommandOutcome> Outcome { get; } = new TaskCompletionSource<CommandOutcome>(TaskCreationOptions.RunContinuationsAsynchronously);
    }

    /// <summary>
    /// One device's session: the link, the counters, and the command handler. SDK callbacks land on pool threads; the handler only parses the payload
    /// and queues the command for the main thread, then awaits the outcome, which is set only when the model says the thing happened.
    /// </summary>
    public sealed class PlaneSession
    {
        /// <summary>Longest a command may wait for its outcome: past a two-hour boost window, so the model's own expiry always answers first.</summary>
        public static readonly TimeSpan AnswerWithin = TimeSpan.FromMinutes(150);

        readonly IDeviceLink link;
        readonly ConcurrentQueue<InboundCommand> inbox;
        readonly CancellationTokenSource stop = new CancellationTokenSource();
        int state = (int)LinkState.Starting;
        int inFlight, stopped;
        long published, sendErrors, lastPublishTicks, sequence;
        int consecutiveFailures;

        public PlaneSession(PlaneDevice device, IDeviceLink link, ConcurrentQueue<InboundCommand> inbox)
        {
            Device = device ?? throw new ArgumentNullException(nameof(device));
            this.link = link ?? throw new ArgumentNullException(nameof(link));
            this.inbox = inbox ?? throw new ArgumentNullException(nameof(inbox));
            link.StateChanged += s => Volatile.Write(ref state, (int)s);
        }

        public PlaneDevice Device { get; }
        public LinkState Link => (LinkState)Volatile.Read(ref state);
        public bool CanPublish => Volatile.Read(ref stopped) == 0 && link.CanPublish;
        public long Published => Interlocked.Read(ref published);
        public long SendErrors => Interlocked.Read(ref sendErrors);
        public int ConsecutiveSendFailures => Volatile.Read(ref consecutiveFailures);
        public DateTimeOffset? LastPublishUtc
        {
            get
            {
                var t = Interlocked.Read(ref lastPublishTicks);
                return t == 0 ? (DateTimeOffset?)null : new DateTimeOffset(t, TimeSpan.Zero);
            }
        }

        /// <summary>Set by the plane when it first published this device's telemetry.</summary>
        public bool HasPublished => Interlocked.Read(ref published) > 0;

        public Task StartAsync(CancellationToken ct)
        {
            Volatile.Write(ref state, (int)LinkState.Starting);
            return link.StartAsync(HandleAsync, ct);
        }

        /// <summary>The SDK's command handler, on a pool thread.</summary>
        public async Task<CommandOutcome> HandleAsync(DeviceCommand command, CancellationToken cancellationToken)
        {
            var args = PayloadReader.Read(command.Payload);
            var item = new InboundCommand(Device, command.Token, command.Name, args);
            inbox.Enqueue(item);
            using (var timeout = new CancellationTokenSource(AnswerWithin))
            using (var linked = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, timeout.Token, stop.Token))
            using (linked.Token.Register(() => item.Outcome.TrySetResult(CommandOutcome.Failed("the device shut down before the command finished"))))
                return await item.Outcome.Task.ConfigureAwait(false);
        }

        /// <summary>Publishes one reading unless the previous one is still on its way. Returns whether it was handed to the link.</summary>
        public bool TryPublish(IReadOnlyDictionary<string, double> values, DateTimeOffset occurredUtc)
        {
            if (!CanPublish) return false;
            if (Interlocked.CompareExchange(ref inFlight, 1, 0) != 0) return false;
            var sample = Sample.Measurement(Interlocked.Increment(ref sequence), occurredUtc, values);
            _ = SendAsync(sample);
            return true;
        }

        async Task SendAsync(Sample sample)
        {
            try
            {
                await link.PublishAsync(sample, stop.Token).ConfigureAwait(false);
                Interlocked.Increment(ref published);
                Volatile.Write(ref consecutiveFailures, 0);
                Interlocked.Exchange(ref lastPublishTicks, DateTimeOffset.UtcNow.UtcTicks);
            }
            catch (Exception e) when (!(e is OperationCanceledException))
            {
                Interlocked.Increment(ref sendErrors);
                Interlocked.Increment(ref consecutiveFailures);
                PlatformLog.Warn(Device.ExternalId + ": send failed: " + e.GetType().Name);
            }
            catch (OperationCanceledException) { }
            finally { Volatile.Write(ref inFlight, 0); }
        }

        public async Task DisposeAsync(TimeSpan timeout)
        {
            Volatile.Write(ref stopped, 1);
            try { stop.Cancel(); } catch (ObjectDisposedException) { }
            var dispose = Task.Run(async () => await link.DisposeAsync().ConfigureAwait(false));
            await Task.WhenAny(dispose, Task.Delay(timeout)).ConfigureAwait(false);
        }
    }
}
