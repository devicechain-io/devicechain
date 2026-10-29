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
    /// <summary>
    /// The device plane: one <see cref="PlaneSession"/> per credentialed device (the only writer for it), started at most four at a time, fed by the
    /// <see cref="TowerPlane"/> and reported on the readiness board. The main thread owns everything here: <see cref="Tick"/> is called from Update. It holds
    /// device credentials only, never the operator token. A device with no credential is grey and has no session; nothing here invents one.
    ///
    /// Telemetry occurred-time is the wall clock. A tripped chiller is "not reporting": its session stays up (a restart must be deliverable), it simply
    /// sends nothing, and the platform's absence rule, the broker's presence and the device status are three separate signals that need not agree.
    /// </summary>
    public sealed class PlaneRunner
    {
        public const int MaxStartsInFlight = 4;
        public static readonly TimeSpan DisposeTimeout = TimeSpan.FromSeconds(6);

        readonly TowerPlane plane;
        readonly ReadinessBoard board;
        readonly DeviceCredentials credentials;
        readonly IDeviceLinkFactory factory;
        readonly double intervalS;
        readonly ConcurrentQueue<InboundCommand> inbox = new ConcurrentQueue<InboundCommand>();
        readonly List<PlaneSession> sessions = new List<PlaneSession>();
        readonly CancellationTokenSource cts = new CancellationTokenSource();
        double lastPublishWall = double.NegativeInfinity;
        int shutDown;

        public PlaneRunner(TowerPlane plane, ReadinessBoard board, DeviceCredentials credentials, IDeviceLinkFactory factory, double publishIntervalSeconds = 10)
        {
            this.plane = plane ?? throw new ArgumentNullException(nameof(plane));
            this.board = board ?? throw new ArgumentNullException(nameof(board));
            this.credentials = credentials ?? throw new ArgumentNullException(nameof(credentials));
            this.factory = factory ?? throw new ArgumentNullException(nameof(factory));
            if (!(publishIntervalSeconds > 0)) throw new ArgumentOutOfRangeException(nameof(publishIntervalSeconds));
            intervalS = publishIntervalSeconds;
        }

        public IReadOnlyList<PlaneSession> Sessions => sessions;
        public TowerPlane Plane => plane;

        /// <summary>
        /// Builds one session for each credentialed device and starts them, four at a time. A device that fails to start is marked failed on the board.
        /// The task ends when every start has returned.
        /// </summary>
        public Task StartAllAsync()
        {
            board.BeginSessions();
            var todo = new List<PlaneSession>();
            foreach (var r in board.Devices)
            {
                if (r.Failed || r.Stage != DeviceStage.Connecting) continue;
                var device = plane.Find(r.Device.ExternalId);
                if (device == null || r.Bind == null || !r.Bind.IsBound || !credentials.TryGet(r.Bind.DeviceToken, out var credentialId)) continue;
                var s = new PlaneSession(device, factory.Create(device.ExternalId, r.Bind.DeviceToken, credentialId), inbox);
                sessions.Add(s);
                todo.Add(s);
            }
            return StartThrottled(todo);
        }

        async Task StartThrottled(List<PlaneSession> todo)
        {
            using (var gate = new SemaphoreSlim(MaxStartsInFlight))
            {
                var tasks = new List<Task>();
                foreach (var s in todo)
                {
                    await gate.WaitAsync(cts.Token).ConfigureAwait(false);
                    tasks.Add(StartOne(s, gate));
                }
                await Task.WhenAll(tasks).ConfigureAwait(false);
            }
        }

        async Task StartOne(PlaneSession s, SemaphoreSlim gate)
        {
            try { await s.StartAsync(cts.Token).ConfigureAwait(false); }
            catch (OperationCanceledException) { }
            catch (Exception e)
            {
                startFailures.Enqueue(new KeyValuePair<string, string>(s.Device.ExternalId, e.GetType().Name + ": " + e.Message));
            }
            finally { gate.Release(); }
        }

        readonly ConcurrentQueue<KeyValuePair<string, string>> startFailures = new ConcurrentQueue<KeyValuePair<string, string>>();

        /// <summary>One frame on the main thread: apply queued commands, run the model, publish what is due, refresh the board.</summary>
        public void Tick(double wallSeconds, DateTimeOffset now)
        {
            if (Volatile.Read(ref shutDown) == 1) return;

            // commands first, so a ticket is stamped at the model time the command arrived in
            while (inbox.TryDequeue(out var cmd))
            {
                var item = cmd;
                board.SetCommand(item.Device.ExternalId, item.Name + " received");
                plane.Commands.Submit(item.Device, item.Token, item.Name, item.Args, r =>
                {
                    board.SetCommand(item.Device.ExternalId, item.Name + (r.Success ? " SUCCESSFUL" : " FAILED: " + r.Reason));
                    item.Outcome.TrySetResult(r.Success ? CommandOutcome.Succeeded(r.Reason) : CommandOutcome.Failed(r.Reason));
                });
            }

            plane.Advance(wallSeconds);

            if (wallSeconds - lastPublishWall >= intervalS)
            {
                lastPublishWall = wallSeconds;
                foreach (var s in sessions)
                {
                    var values = Readings.For(plane.Model, s.Device);
                    if (values == null) continue; // not reporting: sends nothing, keeps its session
                    s.TryPublish(values, now);
                }
            }

            while (startFailures.TryDequeue(out var f))
            {
                board.FailSession(f.Key, "session could not start: " + f.Value);
            }
            Refresh();
        }

        void Refresh()
        {
            foreach (var s in sessions)
            {
                var id = s.Device.ExternalId;
                if (board[id].Failed) continue;
                switch (s.Link)
                {
                    case LinkState.Ready:
                        board.SetSide(id, DeviceSide.None);
                        board.SetStage(id, s.HasPublished ? DeviceStage.Publishing : DeviceStage.Ready);
                        break;
                    case LinkState.Reconnecting: board.SetSide(id, DeviceSide.Reconnecting); break;
                    case LinkState.Blind: board.SetSide(id, DeviceSide.Blind); break;
                    case LinkState.Stopped: board.SetSide(id, DeviceSide.Stopped); break;
                }
                board.SetStats(id, s.Published, s.SendErrors, 0, s.LastPublishUtc, s.ConsecutiveSendFailures);
            }
        }

        /// <summary>The numbers the readiness line is made of.</summary>
        public PlaneReadiness Readiness()
        {
            int cred = 0, connected = 0, silent = 0;
            foreach (var r in board.Devices)
            {
                if (r.Failed) continue;
                if (r.Stage >= DeviceStage.Credentialed) cred++;
                if (r.Stage >= DeviceStage.Ready && r.Side == DeviceSide.None) connected++;
                var d = plane.Find(r.Device.ExternalId);
                if (d != null && Readings.NotReporting(plane.Model, d)) silent++;
            }
            return new PlaneReadiness(board.Total, cred, connected, silent);
        }

        /// <summary>Answers every command still waiting (silence would leave it at SENT), then closes every session. Idempotent.</summary>
        public Task ShutdownAsync()
        {
            if (Interlocked.Exchange(ref shutDown, 1) == 1) return Task.CompletedTask;
            plane.Commands.FailAll("the device plane shut down");
            while (inbox.TryDequeue(out var c)) c.Outcome.TrySetResult(CommandOutcome.Failed("the device plane shut down"));
            cts.Cancel();
            var all = new List<Task>();
            foreach (var s in sessions) all.Add(s.DisposeAsync(DisposeTimeout));
            return Task.WhenAll(all);
        }
    }

    /// <summary>The readiness board's headline: 31/31 credentialed and connected, plus how many are deliberately not reporting.</summary>
    public readonly struct PlaneReadiness
    {
        public PlaneReadiness(int total, int credentialed, int connected, int notReporting)
        {
            Total = total; Credentialed = credentialed; Connected = connected; NotReporting = notReporting;
        }

        public int Total { get; }
        public int Credentialed { get; }
        public int Connected { get; }
        public int NotReporting { get; }

        public bool Full => Total > 0 && Credentialed == Total && Connected == Total;

        public override string ToString()
        {
            return Credentialed + "/" + Total + " credentialed · " + Connected + "/" + Total + " connected" + (NotReporting > 0 ? " · " + NotReporting + " not reporting" : "");
        }
    }
}
