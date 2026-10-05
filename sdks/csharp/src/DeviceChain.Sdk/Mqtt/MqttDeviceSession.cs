// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Json;
using DeviceChain.Sdk.Transport;

namespace DeviceChain.Sdk.Mqtt;

/// <summary>What a device's MQTT session is currently doing.</summary>
public enum MqttSessionState
{
    /// <summary>Connecting, or waiting for the broker to grant the command subscription.</summary>
    Starting,

    /// <summary>Connected AND subscribed with a confirmed grant — commands will arrive.</summary>
    Ready,

    /// <summary>The connection dropped and is being re-established.</summary>
    Reconnecting,

    /// <summary>
    /// The broker REFUSED this device — either the connection or the command subscription — so no
    /// command will ever arrive and the silence is not clean. Terminal: a refusal is not transient,
    /// so the session stops retrying rather than hammering forever.
    /// </summary>
    Blind,

    /// <summary>Disposed.</summary>
    Stopped,
}

/// <summary>Handles one command and reports what the device actually did with it.</summary>
public delegate Task<CommandOutcome> CommandHandler(DeviceCommand command, CancellationToken cancellationToken);

/// <summary>
/// One device's MQTT session: the device plane's whole lifecycle above the transport seam —
/// connect, confirmed subscribe, command receive/dedupe/answer, and reconnect.
/// </summary>
/// <remarks>
/// <para>
/// ONE SESSION IS ONE DEVICE, and that is forced rather than chosen: MQTT 3.1.1 has no shared
/// subscriptions, and the credential's minted JWT grants SUB to exactly one device's command
/// subject. A scene with 18 machines therefore holds 18 broker connections. That is a fact to
/// size for, not a surprise to discover.
/// </para>
/// <para>
/// Handlers are invoked on a background thread. The SDK deliberately does NOT marshal onto a
/// captured <see cref="SynchronizationContext"/>: doing that inside the SDK turns any throw into a
/// silent hang, which this SDK has been bitten by before. A Unity caller marshals to the main
/// thread in its own pump.
/// </para>
/// <para>
/// By default commands run strictly one at a time, in arrival order. Set
/// <see cref="MqttSessionOptions.MaxConcurrentCommands"/> above 1 to run handlers in parallel;
/// the platform's per-device delivery order then holds as EXECUTION order only within a lane
/// (<see cref="MqttSessionOptions.CommandLane"/>).
/// </para>
/// </remarks>
public sealed class MqttDeviceSession : IAsyncDisposable
{
    private readonly MqttSessionOptions _options;
    private readonly IMqttClientFactory _factory;
    private readonly string _clientId;
    private readonly string _commandsTopic;
    private readonly string _responsesTopic;
    private readonly string _eventsTopic;
    private readonly SemaphoreSlim _gate = new(1, 1);
    private readonly CommandHistory _history;
    private readonly PendingResponses _pending;
    private readonly CancellationTokenSource _stopped = new();

    // A dedicated lock object rather than locking something disposable that this class also owns.
    private readonly object _stateLock = new();

    // Commands whose handler is RUNNING RIGHT NOW, keyed by command token. See OnMessageAsync.
    //
    // 🔴 IT HOLDS THE OUTCOME, NOT THE RESPONSE ENVELOPE, AND THAT IS NOT A TIDY-UP. What is
    // remembered per command is what the device DID, which is a fact about the command; the
    // envelope also carries the dispatch nonce, which is a fact about one delivery of it. A
    // redelivery is answered from here without re-running the handler, and it must be answered
    // under the nonce IT carried — so an envelope cached whole would republish a superseded
    // dispatch's name and the command would never settle.
    private readonly Dictionary<string, Task<CommandOutcome>> _inFlight =
        new(StringComparer.Ordinal);

    private readonly object _inFlightLock = new();

    // The concurrent-command executor's state (MaxConcurrentCommands > 1), all guarded by
    // _inFlightLock so that de-dupe and admission are ONE atomic decision.
    //
    // 🔑 THE OPTIONS ARE READ ONCE, HERE. MqttSessionOptions is a mutable class and the session
    // keeps a reference to it; reading the cap on every message would let a caller who changed it
    // mid-session run handlers inline while executor jobs were still running, breaking both the
    // cap and lane order.
    private readonly int _maxConcurrent;
    private readonly Func<DeviceCommand, string?>? _laneSelector;
    private readonly TimeSpan _shutdownTimeout;

    // Admitted, not yet started, in arrival order.
    private readonly LinkedList<Job> _waiting = new();

    // Lanes that have a handler running right now.
    private readonly HashSet<string> _busyLanes = new(StringComparer.Ordinal);

    // Everything the executor has in flight that disposal must wait for: handler workers and the
    // tasks that answer duplicates or lane failures. A counter and a completion source rather
    // than a set of tasks, because a worker has no handle to the task that Task.Run returned.
    private readonly TaskCompletionSource<bool> _drained =
        new(TaskCreationOptions.RunContinuationsAsynchronously);

    private int _running;
    private int _outstanding;
    private bool _closed;

    private IMqttConnection? _connection;
    private CommandHandler? _handler;
    private MqttSessionState _state = MqttSessionState.Starting;
    private int _disposed;
    private int _started;
    private int _reconnectRunning;
    private int _malformedFrames;
    private long _arrivalSequence;

    /// <summary>Creates a session for one device.</summary>
    /// <param name="options">The device's session options.</param>
    /// <param name="factory">
    /// The MQTT transport to use. Defaults to the MQTTnet-backed one; tests and alternate
    /// platforms inject their own.
    /// </param>
    public MqttDeviceSession(MqttSessionOptions options, IMqttClientFactory? factory = null)
    {
        _options = options ?? throw new ArgumentNullException(nameof(options));
        _factory = factory ?? new MqttNetClientFactory();
        _clientId = DevicePlane.DeviceClientId(
            options.InstanceId, options.Tenant, options.DeviceToken, options.ClientIdDiscriminator);
        _commandsTopic = DevicePlane.CommandsTopic(options.InstanceId, options.Tenant, options.DeviceToken);
        _responsesTopic = DevicePlane.CommandResponsesTopic(options.InstanceId, options.Tenant, options.DeviceToken);
        _eventsTopic = DevicePlane.EventsTopic(options.InstanceId, options.Tenant, options.DeviceToken);
        _history = new CommandHistory(options.CommandHistorySize);
        _pending = new PendingResponses(options.CommandHistorySize);

        // The options setters already refuse out-of-range values, and the options class is sealed.
        _maxConcurrent = options.MaxConcurrentCommands;
        _laneSelector = options.CommandLane;
        _shutdownTimeout = options.CommandShutdownTimeout;
    }

    /// <summary>
    /// How many command handlers are running right now. Always 0 unless
    /// <see cref="MqttSessionOptions.MaxConcurrentCommands"/> is above 1.
    /// </summary>
    public int RunningCommands
    {
        get
        {
            lock (_inFlightLock)
            {
                return _running;
            }
        }
    }

    /// <summary>
    /// How many admitted commands are waiting for a handler slot or for their lane. Always 0
    /// unless <see cref="MqttSessionOptions.MaxConcurrentCommands"/> is above 1.
    /// </summary>
    public int QueuedCommands
    {
        get
        {
            lock (_inFlightLock)
            {
                return _waiting.Count;
            }
        }
    }

    /// <summary>The session's current state.</summary>
    public MqttSessionState State
    {
        get
        {
            lock (_stateLock)
            {
                return _state;
            }
        }
    }

    /// <summary>Raised whenever <see cref="State"/> changes.</summary>
    public event Action<MqttSessionState>? StateChanged;

    /// <summary>The client id this session presents.</summary>
    public string ClientId => _clientId;

    /// <summary>The topic this device publishes telemetry to.</summary>
    public string EventsTopic => _eventsTopic;

    /// <summary>How many inbound frames could not be decoded into a command.</summary>
    /// <remarks>
    /// Exposed as a count rather than logged-and-forgotten because it is the only evidence that a
    /// device is receiving something it cannot act on.
    /// </remarks>
    public int MalformedFrames => Volatile.Read(ref _malformedFrames);

    /// <summary>
    /// Connects, subscribes to this device's command topic, and returns ONLY once the broker has
    /// GRANTED that subscription.
    /// </summary>
    /// <remarks>
    /// <para>
    /// 🔴 THE RETURN IS THE PROMISE, AND IT IS FAIL-CLOSED ABOUT ITS OWN BLINDNESS. If the broker
    /// refuses the subscription the session goes <see cref="MqttSessionState.Blind"/> and this
    /// throws, rather than returning a session that is connected, looks healthy, and will never
    /// receive anything. A device that never got a confirmed grant is reported un-subscribed, so
    /// its silence is never read as clean.
    /// </para>
    /// <para>
    /// 🔑 AND WHEN IT THROWS, NOTHING IS LEFT RUNNING. A failed start leaves no connection and no
    /// background reconnect, so a caller told that startup failed does not silently own a session
    /// that dials on and later starts executing commands it believes never started.
    /// </para>
    /// </remarks>
    /// <exception cref="MqttSubscribeRefusedException">The broker refused the command subscription.</exception>
    /// <exception cref="InvalidOperationException">The session was already started, or is disposed.</exception>
    public async Task StartAsync(CommandHandler handler, CancellationToken cancellationToken)
    {
        _handler = handler ?? throw new ArgumentNullException(nameof(handler));

        if (Volatile.Read(ref _disposed) != 0)
        {
            throw new InvalidOperationException("this MQTT session has been disposed");
        }

        // 🔑 STARTING TWICE WOULD LEAK THE FIRST CONNECTION — still connected, still wired to the
        // command handler — and, because both present the same client id, the broker would evict
        // one with the other. Refusing is the only sane reading of a second start.
        if (Interlocked.Exchange(ref _started, 1) != 0)
        {
            throw new InvalidOperationException(
                $"the MQTT session for device \"{_options.DeviceToken}\" has already been started");
        }

        await _gate.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            await ConnectAndSubscribeAsync(cancellationToken).ConfigureAwait(false);
        }
        finally
        {
            _gate.Release();
        }
    }

    /// <summary>
    /// Publishes one already-serialized device event to this device's own events topic at QoS 1,
    /// completing when the broker has acknowledged it.
    /// </summary>
    /// <remarks>
    /// The body is passed in already serialized because it must be byte-identical to what the HTTP
    /// carrier posts — the gateway decodes the same shape either way, and (unlike the transport-
    /// authenticated adapters) it does not stamp events as transport-authenticated, so the body
    /// must still carry its own credential fields.
    /// </remarks>
    public async Task PublishEventAsync(byte[] jsonEvent, CancellationToken cancellationToken)
    {
        var connection = Volatile.Read(ref _connection);
        if (connection == null || !connection.IsConnected)
        {
            throw new MqttConnectionException(
                $"the MQTT session for device \"{_options.DeviceToken}\" is not connected");
        }

        await connection.PublishAsync(_eventsTopic, jsonEvent, MqttQos.AtLeastOnce, cancellationToken)
            .ConfigureAwait(false);
    }

    private async Task ConnectAndSubscribeAsync(CancellationToken cancellationToken)
    {
        var connection = _factory.Create();
        var established = false;

        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, _stopped.Token);
        timeout.CancelAfter(_options.OperationTimeout);

        var options = new MqttConnectOptions(_options.BrokerUri, _clientId)
        {
            Username = DevicePlane.ConnectUsername(_options.Tenant, _options.CredentialId),
            // Empty, not absent: it is what selects access-token credential mode at the callout.
            Password = string.Empty,
            // False so the broker restores this device's SUBSCRIPTION across a reconnect. It
            // re-arms it during CONNECT processing, before this client could send SUBSCRIBE, so
            // there is no window where the device is connected but not subscribed.
            //
            // 🔴 IT DOES NOT HOLD UNDELIVERED COMMANDS, AND AN EARLIER COMMENT HERE SAID IT DID.
            // The broker queues a QoS-1 message only if it reached it as an MQTT PUBLISH from
            // another MQTT client; a command published by the platform arrives over NATS and is
            // delivered to this subscription live, as QoS 0, with no packet id and no store. A
            // command issued while the device is away is dropped by the broker and is not
            // redelivered on reconnect. Persisting the subscription is what narrows the loss
            // window to nothing on THIS client; it does not close the away case.
            CleanSession = false,
            KeepAlive = _options.KeepAlive,
            Trust = _options.Trust,
        };

        try
        {
            await connection.ConnectAsync(options, timeout.Token).ConfigureAwait(false);

            // Wired only AFTER the connect succeeded, so a message cannot arrive unobserved
            // between subscribing and handling.
            connection.MessageReceived += OnMessageAsync;

            // Inside the connect path on EVERY (re)connect, so a reconnect re-establishes the
            // subscription by the same code that established it — and re-reads the grant.
            await connection.SubscribeAsync(_commandsTopic, MqttQos.AtLeastOnce, timeout.Token).ConfigureAwait(false);

            // 🔴 THE LOST-CONNECTION HANDLER IS WIRED LAST, AND THAT ORDERING IS LOAD-BEARING.
            // MQTTnet raises its disconnected event for a connection that was NEVER ESTABLISHED —
            // measured, not assumed: a socket-level connect failure and a refused CONNACK each
            // raise it once. Wiring it before the connect therefore made every FAILED reconnect
            // attempt spawn another reconnect loop, and the loops multiplied: ~1,100 connect
            // attempts per second against a down broker, where one backed-off loop should manage
            // about two. With 18 machines in a scene that is a self-inflicted denial of service on
            // the gateway during exactly the outage the reconnect exists to survive.
            connection.ConnectionLost += OnConnectionLost;
            established = true;

            _connection = connection;
            SetState(MqttSessionState.Ready);
        }
        catch (MqttSubscribeRefusedException)
        {
            // Connected but deaf. Surfacing this as state, not just as a throw, is what lets a
            // long-running caller tell "no commands have arrived" from "no command can arrive".
            SetState(MqttSessionState.Blind);
            throw;
        }
        finally
        {
            if (!established)
            {
                // Nothing else will ever dispose it: _connection is assigned only on success, so
                // without this every failed attempt leaks a client object — thousands of them over
                // a long outage.
                await DetachAndDisposeAsync(connection).ConfigureAwait(false);
            }
        }
    }

    private void OnConnectionLost(Exception? cause)
    {
        if (Volatile.Read(ref _disposed) != 0 || _stopped.IsCancellationRequested)
        {
            return;
        }

        // One reconnect loop at a time. Even with the wiring order above, a connection can raise
        // its lost event more than once, and each extra loop would double the dial rate.
        if (Interlocked.CompareExchange(ref _reconnectRunning, 1, 0) != 0)
        {
            return;
        }

        SetState(MqttSessionState.Reconnecting);
        _ = Task.Run(ReconnectLoopAsync);
    }

    private async Task ReconnectLoopAsync()
    {
        var reconnected = false;
        try
        {
            var delay = _options.ReconnectInitialDelay;

            // Jittered so a scene's worth of devices reconnecting after one broker restart do not
            // arrive as a synchronised thundering herd — 18 machines is enough to notice. Seeded
            // per client id so two devices do not share a sequence.
            var jitter = new Random(StringComparer.Ordinal.GetHashCode(_clientId));

            while (!_stopped.IsCancellationRequested && Volatile.Read(ref _disposed) == 0)
            {
                var scaled = TimeSpan.FromMilliseconds(delay.TotalMilliseconds * (0.5 + jitter.NextDouble()));
                try
                {
                    await Task.Delay(scaled, _stopped.Token).ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    return;
                }

                try
                {
                    await _gate.WaitAsync(_stopped.Token).ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    return;
                }

                try
                {
                    var previous = Interlocked.Exchange(ref _connection, null);
                    if (previous != null)
                    {
                        await DetachAndDisposeAsync(previous).ConfigureAwait(false);
                    }

                    await ConnectAndSubscribeAsync(_stopped.Token).ConfigureAwait(false);
                    reconnected = true;
                    return;
                }
                catch (MqttSubscribeRefusedException)
                {
                    // A refusal is not transient: the credential is not permitted to read that
                    // topic, so retrying forever would change nothing while looking like progress.
                    // Stay Blind and stop — the state is the report.
                    return;
                }
                catch (MqttConnectRefusedException)
                {
                    // Likewise for a broker that REFUSED the connection (a revoked credential, a
                    // client id the callout will not admit). Retrying that is not resilience, it
                    // is a hammering loop against a decision that will not change.
                    SetState(MqttSessionState.Blind);
                    return;
                }
                catch (OperationCanceledException)
                {
                    return;
                }
                catch (Exception)
                {
                    delay = TimeSpan.FromMilliseconds(Math.Min(
                        delay.TotalMilliseconds * 2, _options.ReconnectMaxDelay.TotalMilliseconds));
                }
                finally
                {
                    _gate.Release();
                }
            }
        }
        finally
        {
            Volatile.Write(ref _reconnectRunning, 0);

            // After the loop has released its claim, so a connection that drops again while the
            // held answers go out starts a new loop that flushes whatever is still held.
            if (reconnected)
            {
                await FlushPendingResponsesAsync().ConfigureAwait(false);
            }
        }
    }

    // Publishes the answers that could not be sent while the connection was down. Each goes out
    // through the ordinary publish, which holds it again if this connection fails too.
    private async Task FlushPendingResponsesAsync()
    {
        foreach (var held in _pending.TakeAll())
        {
            if (_stopped.IsCancellationRequested || Volatile.Read(ref _disposed) != 0)
            {
                return;
            }

            await PublishResponseAsync(held.Token, held.Outcome, held.DispatchNonce, held.Sequence).ConfigureAwait(false);
        }
    }

    private async Task OnMessageAsync(MqttInbound inbound)
    {
        // A connection carries only this device's command subscription today, but the filter is
        // what keeps that true: anything else this session ever subscribes to would otherwise be
        // parsed as a command.
        if (!string.Equals(inbound.Topic, _commandsTopic, StringComparison.Ordinal))
        {
            return;
        }

        CommandDeliveryEnvelope? envelope;
        try
        {
            envelope = JsonSerializer.Deserialize(
                inbound.Payload, SdkJson.Default.CommandDeliveryEnvelope);
        }
        catch (JsonException)
        {
            // A frame that cannot be decoded is counted and dropped, never answered. Answering it
            // is impossible anyway — the answer is keyed by a command token we could not read —
            // and re-raising here would only make the broker redeliver a poison frame forever.
            Interlocked.Increment(ref _malformedFrames);
            return;
        }

        if (envelope?.Token == null || envelope.Token.Length == 0)
        {
            Interlocked.Increment(ref _malformedFrames);
            return;
        }

        var handler = _handler;
        if (handler == null)
        {
            return;
        }

        // 🔑 THE ARRIVAL NUMBER IS TAKEN HERE, ON THE RECEIVE PATH, BEFORE ANYTHING CAN REORDER
        // THE COMMAND. The transport calls this callback one frame at a time, so the order of
        // these increments is the platform's delivery order. Above MaxConcurrentCommands = 1 each
        // handler is started on its own worker and two commands delivered together can reach user
        // code in either order, so the number must already be on the command by then. A duplicate
        // takes a number too but never shows it to a handler (it is answered from the first
        // execution), so the numbers a handler sees are increasing and may have gaps.
        var sequence = Interlocked.Increment(ref _arrivalSequence);

        // 🔴 DE-DUPE BY COMMAND TOKEN, INCLUDING WHILE THE HANDLER IS STILL RUNNING. Delivery is
        // at-least-once, so the same token can arrive more than once — a redelivery must NOT run
        // the handler again (a machine would move twice), but it must still be ANSWERED, or the
        // redelivery was caused by a lost response and dropping it guarantees the command never
        // completes.
        //
        // The in-flight map is the half a completed-only cache would miss: a duplicate arriving
        // while the handler is still working coalesces onto that execution instead of starting a
        // second one.
        //
        // 🔴 WHETHER THAT WINDOW IS REACHABLE DEPENDS ON MaxConcurrentCommands. At 1, over the
        // shipped MQTTnet transport it is closed by sequential dispatch, measured: with a handler
        // blocked, a second command published at t=85ms was not dispatched until the first
        // handler returned at t=1587ms, so a real redelivery always arrives AFTER the handler
        // finished and is answered from the completed-command cache above. Above 1 it is LIVE:
        // the callback returns at admission, so a re-dispatched command can arrive while its
        // first delivery is running or still queued, and this map is the only thing standing
        // between it and a second actuation (see AdmitConcurrent, which registers a command here
        // at admission rather than at start).
        //
        // 🔑 AT 1, A SLOW HANDLER HEAD-OF-LINE BLOCKS every later command for that device: a
        // machine part-way through a multi-second command cannot be redirected until it
        // finishes. MaxConcurrentCommands is the opt-out, and it trades away execution order
        // outside a lane.
        if (_maxConcurrent > 1)
        {
            AdmitConcurrent(envelope, handler, sequence);
            return;
        }

        Task<CommandOutcome>? existing = null;
        TaskCompletionSource<CommandOutcome>? owned = null;

        lock (_inFlightLock)
        {
            if (_history.TryGet(envelope.Token, out var cached))
            {
                existing = Task.FromResult(cached!);
            }
            else if (_inFlight.TryGetValue(envelope.Token, out var running))
            {
                existing = running;
            }
            else
            {
                owned = new TaskCompletionSource<CommandOutcome>(
                    TaskCreationOptions.RunContinuationsAsynchronously);
                _inFlight[envelope.Token] = owned.Task;
            }
        }

        if (existing != null)
        {
            // The remembered outcome, published under THIS frame's dispatch nonce. The handler is
            // not run again — the machine must not move twice — but the answer names the delivery
            // it is answering, which is what lets a re-dispatched command be settled by the device
            // that had already carried it out.
            var previous = await existing.ConfigureAwait(false);
            await PublishResponseAsync(envelope.Token, previous, envelope.DispatchNonce, sequence)
                .ConfigureAwait(false);
            return;
        }

        CommandOutcome outcome;
        try
        {
            outcome = await handler(
                new DeviceCommand(envelope.Token, envelope.Name ?? string.Empty, envelope.Payload, sequence),
                _stopped.Token).ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            // A handler that threw did not carry the command out. Reporting that is strictly
            // better than reporting nothing, which would leave the command at SENT until it
            // expired with no indication of why.
            outcome = CommandOutcome.Failed($"the device's command handler threw: {ex.Message}");
        }

        lock (_inFlightLock)
        {
            _history.Add(envelope.Token, outcome);
            _inFlight.Remove(envelope.Token);
        }

        owned!.TrySetResult(outcome);
        await PublishResponseAsync(envelope.Token, outcome, envelope.DispatchNonce, sequence)
            .ConfigureAwait(false);
    }

    private async Task PublishResponseAsync(
        string commandToken, CommandOutcome outcome, string? dispatchNonce, long sequence)
    {
        var connection = Volatile.Read(ref _connection);
        if (connection == null || !connection.IsConnected)
        {
            // 🔴 HELD FOR THE RECONNECT, NOT DROPPED. Nothing re-dispatches a command the platform
            // already sent over MQTT, so an answer that is thrown away here leaves the command at
            // SENT until it expires, however long ago the device carried it out. It is published
            // when the session is next connected and subscribed (see FlushPendingResponsesAsync).
            _pending.Hold(commandToken, outcome, dispatchNonce, sequence);
            return;
        }

        // The envelope is built per publish rather than cached, because one of its fields belongs
        // to the delivery and not to the command — see the nonce's own remarks. It is echoed as
        // received, including when it is absent: inventing a value would produce an answer the
        // platform accepts for a dispatch that may not be the one this device saw, and the honest
        // empty is refused visibly instead.
        var response = new CommandResponseEnvelope
        {
            CommandToken = commandToken,
            Success = outcome.Success,
            Payload = outcome.Payload,
            Error = outcome.Error,
            DispatchNonce = dispatchNonce,
        };

        var payload = JsonSerializer.SerializeToUtf8Bytes(response, SdkJson.Default.CommandResponseEnvelope);
        try
        {
            // QoS 1 and awaited: an unacked response is not a delivered response, and this is the
            // message that drives the durable command to its terminal state.
            await connection.PublishAsync(_responsesTopic, payload, MqttQos.AtLeastOnce, _stopped.Token)
                .ConfigureAwait(false);

            // Answered. Any older answer held for this command named an earlier dispatch and is
            // now moot.
            _pending.Remove(commandToken, sequence);
        }
        catch (Exception)
        {
            // 🔴 A PUBLISH THAT FAILED IS HELD, NOT FORGOTTEN: a platform command reaches the
            // device at QoS 0, which the broker neither stores nor redelivers, so if this answer
            // is lost nothing will ask again. It goes out after the next reconnect, under the
            // nonce of the delivery it answers. (A publish that failed after the broker already
            // had it can be sent twice; the platform refuses the second as unanswerable and
            // records it, which is the right price for never losing the first.)
            if (!_stopped.IsCancellationRequested)
            {
                _pending.Hold(commandToken, outcome, dispatchNonce, sequence);
            }
        }
    }

    // One admitted command in the concurrent executor.
    private sealed class Job
    {
        public Job(CommandDeliveryEnvelope envelope, DeviceCommand command, string? lane, CommandHandler handler)
        {
            Envelope = envelope;
            Command = command;
            Lane = lane;
            Handler = handler;
            Owned = new TaskCompletionSource<CommandOutcome>(TaskCreationOptions.RunContinuationsAsynchronously);
        }

        public CommandDeliveryEnvelope Envelope { get; }

        public DeviceCommand Command { get; }

        public string? Lane { get; }

        public CommandHandler Handler { get; }

        public TaskCompletionSource<CommandOutcome> Owned { get; }
    }

    // What the locked part of admission decided; the work it implies happens OUTSIDE the lock,
    // because publishing runs transport code and a fake or real connection may complete
    // synchronously.
    private enum Admission
    {
        Dropped,
        AnswerFrom,
        Queued,
        LaneFailed,
    }

    // The N > 1 receive path. It never awaits a handler: it decides, under the lock, what this
    // delivery is, and returns.
    private void AdmitConcurrent(CommandDeliveryEnvelope envelope, CommandHandler handler, long sequence)
    {
        var token = envelope.Token!;
        var command = new DeviceCommand(token, envelope.Name ?? string.Empty, envelope.Payload, sequence);

        // User code, so outside the lock. A throw is captured, not propagated.
        string? lane = null;
        string? laneError = null;
        if (_laneSelector != null)
        {
            try
            {
                lane = _laneSelector(command);
            }
            catch (Exception ex)
            {
                laneError = ex.Message;
            }
        }

        var admission = Admission.Dropped;
        Task<CommandOutcome>? answerFrom = null;
        CommandOutcome? laneFailure = null;
        List<Job>? toStart = null;

        lock (_inFlightLock)
        {
            if (_closed)
            {
                // Disposing: no answer. The command stays SENT until it times out, exactly like
                // an unanswered command today; answering Failed would claim the device tried.
            }
            else if (_history.TryGet(token, out var cached))
            {
                admission = Admission.AnswerFrom;
                answerFrom = Task.FromResult(cached!);
                _outstanding++;
            }
            else if (_inFlight.TryGetValue(token, out var running))
            {
                // Running OR queued: a duplicate coalesces onto it, and does not wait inline, or
                // one duplicate of a slow command would head-of-line block the device again.
                admission = Admission.AnswerFrom;
                answerFrom = running;
                _outstanding++;
            }
            else if (laneError != null)
            {
                // The command never runs, so it has no reason to wait for a slot. Duplicates were
                // coalesced above; this records the outcome so later ones are answered from it.
                laneFailure = CommandOutcome.Failed($"the command lane selector threw: {laneError}");
                _history.Add(token, laneFailure);
                admission = Admission.LaneFailed;
                _outstanding++;
            }
            else
            {
                var job = new Job(envelope, command, lane, handler);

                // Registered at ADMISSION, not at start, so a duplicate of a QUEUED command
                // coalesces too.
                _inFlight[token] = job.Owned.Task;
                _waiting.AddLast(job);
                admission = Admission.Queued;
                toStart = Pump();
            }
        }

        switch (admission)
        {
            case Admission.AnswerFrom:
                RunReserved(() => AnswerAsync(answerFrom!, envelope, sequence));
                break;
            case Admission.LaneFailed:
                RunReserved(() => PublishResponseAsync(token, laneFailure!, envelope.DispatchNonce, sequence));
                break;
            case Admission.Queued:
                StartJobs(toStart!);
                break;
        }
    }

    // Takes the startable jobs off the queue. Lock held. Start order is arrival order among the
    // jobs that CAN start: one whose lane is busy is passed over, so a later job in a free lane
    // may start first, while within a lane it is strict FIFO because a lane is released only when
    // its handler has returned.
    private List<Job> Pump()
    {
        var started = new List<Job>();
        while (!_closed && _running < _maxConcurrent)
        {
            Job? next = null;
            for (var node = _waiting.First; node != null; node = node.Next)
            {
                var lane = node.Value.Lane;
                if (lane == null || !_busyLanes.Contains(lane))
                {
                    next = node.Value;
                    _waiting.Remove(node);
                    break;
                }
            }

            if (next == null)
            {
                break;
            }

            if (next.Lane != null)
            {
                _busyLanes.Add(next.Lane);
            }

            _running++;
            _outstanding++;
            started.Add(next);
        }

        return started;
    }

    // The Task.Run is load-bearing: calling the handler inline would run a synchronously blocking
    // handler on the receive thread. _outstanding was already incremented under the lock.
    private void StartJobs(List<Job> jobs)
    {
        foreach (var job in jobs)
        {
            var captured = job;
            RunReserved(() => RunJobAsync(captured));
        }
    }

    // Every worker body is wrapped in one catch-all, so a faulted task is never left unobserved.
    // The caller has already counted this work in _outstanding, under the lock that decided it.
    private void RunReserved(Func<Task> body)
    {
        _ = Task.Run(async () =>
        {
            try
            {
                await body().ConfigureAwait(false);
            }
            catch (Exception)
            {
                // Handler failures are already turned into outcomes; this is the backstop.
            }
            finally
            {
                lock (_inFlightLock)
                {
                    _outstanding--;
                    if (_closed && _outstanding == 0)
                    {
                        _drained.TrySetResult(true);
                    }
                }
            }
        });
    }

    private async Task RunJobAsync(Job job)
    {
        CommandOutcome outcome;
        try
        {
            outcome = await job.Handler(job.Command, _stopped.Token).ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            // Same as the one-at-a-time path: a handler that threw did not carry the command out.
            outcome = CommandOutcome.Failed($"the device's command handler threw: {ex.Message}");
        }

        List<Job> next;
        lock (_inFlightLock)
        {
            _history.Add(job.Envelope.Token!, outcome);
            _inFlight.Remove(job.Envelope.Token!);
            _running--;
            if (job.Lane != null)
            {
                _busyLanes.Remove(job.Lane);
            }

            next = Pump();
        }

        job.Owned.TrySetResult(outcome);

        // The next job starts before this one's response is published: the slot is held from
        // handler invocation to handler return, not through the publish.
        StartJobs(next);

        await PublishResponseAsync(job.Envelope.Token!, outcome, job.Envelope.DispatchNonce, job.Command.Sequence)
            .ConfigureAwait(false);
    }

    private async Task AnswerAsync(Task<CommandOutcome> existing, CommandDeliveryEnvelope envelope, long sequence)
    {
        CommandOutcome previous;
        try
        {
            previous = await existing.ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            // The command was queued when the session was disposed and never ran. Nothing to say.
            return;
        }

        await PublishResponseAsync(envelope.Token!, previous, envelope.DispatchNonce, sequence)
            .ConfigureAwait(false);
    }

    private async Task DetachAndDisposeAsync(IMqttConnection connection)
    {
        // Detach FIRST: a connection being torn down still raises its lost-connection event, and
        // acting on that would start a reconnect for a connection we are deliberately discarding.
        connection.MessageReceived -= OnMessageAsync;
        connection.ConnectionLost -= OnConnectionLost;

        try
        {
            await connection.DisconnectAsync(TimeSpan.FromMilliseconds(250), CancellationToken.None)
                .ConfigureAwait(false);
        }
        catch (Exception)
        {
            // Teardown; a failure here has still achieved the goal.
        }

        await connection.DisposeAsync().ConfigureAwait(false);
    }

    private void SetState(MqttSessionState state)
    {
        bool changed;
        lock (_stateLock)
        {
            changed = _state != state;
            _state = state;
        }

        if (changed)
        {
            StateChanged?.Invoke(state);
        }
    }

    /// <inheritdoc />
    public async ValueTask DisposeAsync()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0)
        {
            return;
        }

        // 🔴 CLOSE THE EXECUTOR BEFORE CANCELLING ANYTHING. If the token were cancelled first, a
        // running handler that saw it and returned would let the pump start a queued command with
        // an already-cancelled token. Closing under the lock first means no queued command is
        // started after this point and the queued commands are cancelled, not run. One narrow
        // window remains: the pump selects a job under the lock and starts it outside it, so a job
        // selected just before the close can still enter its handler with a token that is already
        // cancelled. That is harmless: the handler sees the cancellation at once.
        if (_maxConcurrent > 1)
        {
            CloseExecutor();
        }

        _stopped.Cancel();
        SetState(MqttSessionState.Stopped);

        if (_maxConcurrent > 1)
        {
            try
            {
                await DrainExecutorAsync().ConfigureAwait(false);
            }
            catch (Exception)
            {
                // Teardown below must run whatever the drain did: skipping it leaves a live socket
                // on a session that reports Stopped, and a second dispose is a no-op.
            }
        }

        // 🔴 TAKE THE GATE. Without it, disposal races an in-flight reconnect: the loop nulls
        // _connection before dialing, so a dispose landing in that window sees nothing to close,
        // and the connect it did not wait for then completes — leaving a live, connected socket
        // with keepalive running forever on a session that reports Stopped.
        var acquired = false;
        try
        {
            acquired = await _gate.WaitAsync(TimeSpan.FromSeconds(5)).ConfigureAwait(false);
        }
        catch (Exception)
        {
            // Fall through and tear down what we can see.
        }

        try
        {
            var connection = Interlocked.Exchange(ref _connection, null);
            if (connection != null)
            {
                await DetachAndDisposeAsync(connection).ConfigureAwait(false);
            }
        }
        finally
        {
            if (acquired)
            {
                _gate.Release();
            }
        }

        // 🔑 _gate AND _stopped ARE DELIBERATELY NOT DISPOSED. A reconnect loop may still be
        // awaiting either one, and disposing them turns that into an ObjectDisposedException
        // thrown into an unobserved background task — while every later read of _stopped.Token
        // (message handling, response publishing) throws too. Neither holds an unmanaged resource
        // or a timer here, so letting the GC take them is strictly safer than a tidy Dispose.
    }

    private void CloseExecutor()
    {
        lock (_inFlightLock)
        {
            _closed = true;
            foreach (var job in _waiting)
            {
                _inFlight.Remove(job.Envelope.Token!);

                // Canceled, not faulted: a canceled task nobody observes raises no
                // UnobservedTaskException. Queued commands are neither run nor answered.
                job.Owned.TrySetCanceled();
            }

            _waiting.Clear();
            if (_outstanding == 0)
            {
                _drained.TrySetResult(true);
            }
        }
    }

    // Waits, bounded, for running handlers (which now see a cancelled token) and the tasks that
    // answer duplicates. A handler that ignores cancellation past the bound is abandoned and
    // disposal still returns. Task.WaitAsync is net6+, and this compiles for netstandard2.1.
    private async Task DrainExecutorAsync()
    {
        using var bound = new CancellationTokenSource();
        var timer = Task.Delay(_shutdownTimeout, bound.Token);
        try
        {
            await Task.WhenAny(_drained.Task, timer).ConfigureAwait(false);
        }
        finally
        {
            bound.Cancel();
        }

        // The timer is cancelled on every path; observe it so a cancelled delay is never unobserved.
        try
        {
            await timer.ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            // Expected: the drain won.
        }
    }

    /// <summary>
    /// The answers that could not be published yet, at most one per command and bounded.
    /// </summary>
    /// <remarks>
    /// One per command because a newer delivery's nonce is the only one the platform can still be
    /// waiting on: holding a second would publish an answer for a dispatch it has moved off. When
    /// the bound is reached the oldest is given up, the same trade the command history makes.
    /// </remarks>
    private sealed class PendingResponses
    {
        private readonly int _capacity;
        private readonly Dictionary<string, Held> _byToken = new(StringComparer.Ordinal);
        private readonly LinkedList<string> _order = new();
        private readonly object _lock = new();

        public PendingResponses(int capacity) => _capacity = capacity < 1 ? 1 : capacity;

        // Ordered by the arrival number of the delivery being answered, not by when the answer was
        // written: a delivery that joined a running handler is answered from a separate
        // continuation, and whichever of the two writes last must not win.
        public void Hold(string token, CommandOutcome outcome, string? dispatchNonce, long sequence)
        {
            lock (_lock)
            {
                if (_byToken.TryGetValue(token, out var existing))
                {
                    if (existing.Sequence > sequence)
                    {
                        return;
                    }
                }
                else
                {
                    _order.AddLast(token);
                }

                _byToken[token] = new Held(token, outcome, dispatchNonce, sequence);
                while (_order.Count > _capacity)
                {
                    _byToken.Remove(_order.First!.Value);
                    _order.RemoveFirst();
                }
            }
        }

        // Only an answer to a delivery at least as new as the held one makes it moot.
        public void Remove(string token, long sequence)
        {
            lock (_lock)
            {
                if (_byToken.TryGetValue(token, out var existing) && existing.Sequence <= sequence)
                {
                    _byToken.Remove(token);
                    _order.Remove(token);
                }
            }
        }

        public List<Held> TakeAll()
        {
            lock (_lock)
            {
                var all = new List<Held>(_order.Count);
                foreach (var token in _order)
                {
                    all.Add(_byToken[token]);
                }

                _byToken.Clear();
                _order.Clear();
                return all;
            }
        }
    }

    private readonly struct Held
    {
        public Held(string token, CommandOutcome outcome, string? dispatchNonce, long sequence)
        {
            Sequence = sequence;
            Token = token;
            Outcome = outcome;
            DispatchNonce = dispatchNonce;
        }

        public string Token { get; }

        public CommandOutcome Outcome { get; }

        public string? DispatchNonce { get; }

        public long Sequence { get; }
    }

    /// <summary>
    /// A bounded most-recently-used memory of what the device DID with each answered command.
    /// </summary>
    /// <remarks>
    /// Deliberately a plain dictionary plus an insertion-ordered queue rather than anything
    /// cleverer: the size is small, the operations are add and lookup, and an SDK that a device
    /// runs for months should have nothing here that can grow without limit.
    /// </remarks>
    private sealed class CommandHistory
    {
        private readonly int _capacity;
        private readonly Dictionary<string, CommandOutcome> _byToken;
        private readonly Queue<string> _order;
        private readonly object _lock = new();

        public CommandHistory(int capacity)
        {
            _capacity = capacity < 1 ? 1 : capacity;
            _byToken = new Dictionary<string, CommandOutcome>(StringComparer.Ordinal);
            _order = new Queue<string>();
        }

        public bool TryGet(string token, out CommandOutcome? response)
        {
            lock (_lock)
            {
                return _byToken.TryGetValue(token, out response);
            }
        }

        public void Add(string token, CommandOutcome response)
        {
            lock (_lock)
            {
                if (_byToken.ContainsKey(token))
                {
                    return;
                }

                _byToken[token] = response;
                _order.Enqueue(token);
                while (_order.Count > _capacity)
                {
                    _byToken.Remove(_order.Dequeue());
                }
            }
        }
    }
}
