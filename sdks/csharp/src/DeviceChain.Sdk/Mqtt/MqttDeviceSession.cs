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
    private readonly CancellationTokenSource _stopped = new();

    // A dedicated lock object rather than locking something disposable that this class also owns.
    private readonly object _stateLock = new();

    // Commands whose handler is RUNNING RIGHT NOW, keyed by command token. See OnMessageAsync.
    private readonly Dictionary<string, Task<CommandResponseEnvelope>> _inFlight =
        new(StringComparer.Ordinal);

    private readonly object _inFlightLock = new();

    private IMqttConnection? _connection;
    private CommandHandler? _handler;
    private MqttSessionState _state = MqttSessionState.Starting;
    private int _disposed;
    private int _started;
    private int _reconnectRunning;
    private int _malformedFrames;

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
        _responsesTopic = DevicePlane.CommandResponsesTopic(options.InstanceId, options.Tenant);
        _eventsTopic = DevicePlane.EventsTopic(options.InstanceId, options.Tenant, options.DeviceToken);
        _history = new CommandHistory(options.CommandHistorySize);
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
        // 🔴 BUT BE HONEST ABOUT WHEN IT IS REACHABLE — AN EARLIER VERSION OF THIS COMMENT WAS
        // NOT. Over the shipped MQTTnet transport that window CANNOT OCCUR: MQTTnet dispatches
        // inbound PUBLISHes strictly SEQUENTIALLY, measured — with a handler blocked, a second
        // command published at t=85ms was not dispatched until the first handler returned at
        // t=1587ms. So a real redelivery always arrives AFTER the handler finished and is
        // answered from the completed-command cache above; only the fake reaches this branch.
        //
        // It stays because the seam admits other transports — the hand-rolled MQTT 3.1.1 client
        // that is the stated fallback would be free to dispatch concurrently — and because the
        // cost is a dictionary lookup. It is defence in depth, not a live guard, and saying so is
        // better than leaving a comment that implies coverage the shipped path does not exercise.
        //
        // 🔑 THE SEQUENTIAL DISPATCH HAS A CONSEQUENCE WORTH KNOWING: a slow command handler
        // HEAD-OF-LINE BLOCKS every later command for that device. A machine part-way through a
        // multi-second command cannot be redirected until it finishes. That is a property to
        // design scenes around, not a defect to fix here.
        Task<CommandResponseEnvelope>? existing = null;
        TaskCompletionSource<CommandResponseEnvelope>? owned = null;

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
                owned = new TaskCompletionSource<CommandResponseEnvelope>(
                    TaskCreationOptions.RunContinuationsAsynchronously);
                _inFlight[envelope.Token] = owned.Task;
            }
        }

        if (existing != null)
        {
            var previous = await existing.ConfigureAwait(false);
            await PublishResponseAsync(previous).ConfigureAwait(false);
            return;
        }

        CommandOutcome outcome;
        try
        {
            outcome = await handler(
                new DeviceCommand(envelope.Token, envelope.Name ?? string.Empty, envelope.Payload),
                _stopped.Token).ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            // A handler that threw did not carry the command out. Reporting that is strictly
            // better than reporting nothing, which would leave the command at SENT until it
            // expired with no indication of why.
            outcome = CommandOutcome.Failed($"the device's command handler threw: {ex.Message}");
        }

        var response = new CommandResponseEnvelope
        {
            CommandToken = envelope.Token,
            Success = outcome.Success,
            Payload = outcome.Payload,
            Error = outcome.Error,
        };

        lock (_inFlightLock)
        {
            _history.Add(envelope.Token, response);
            _inFlight.Remove(envelope.Token);
        }

        owned!.TrySetResult(response);
        await PublishResponseAsync(response).ConfigureAwait(false);
    }

    private async Task PublishResponseAsync(CommandResponseEnvelope response)
    {
        var connection = Volatile.Read(ref _connection);
        if (connection == null || !connection.IsConnected)
        {
            return;
        }

        var payload = JsonSerializer.SerializeToUtf8Bytes(response, SdkJson.Default.CommandResponseEnvelope);
        try
        {
            // QoS 1 and awaited: an unacked response is not a delivered response, and this is the
            // message that drives the durable command to its terminal state.
            await connection.PublishAsync(_responsesTopic, payload, MqttQos.AtLeastOnce, _stopped.Token)
                .ConfigureAwait(false);
        }
        catch (Exception)
        {
            // The broker will redeliver the command if this never landed, and the cached outcome
            // above answers the redelivery without re-running the handler.
        }
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

        _stopped.Cancel();
        SetState(MqttSessionState.Stopped);

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

    /// <summary>
    /// A bounded most-recently-used memory of answered commands.
    /// </summary>
    /// <remarks>
    /// Deliberately a plain dictionary plus an insertion-ordered queue rather than anything
    /// cleverer: the size is small, the operations are add and lookup, and an SDK that a device
    /// runs for months should have nothing here that can grow without limit.
    /// </remarks>
    private sealed class CommandHistory
    {
        private readonly int _capacity;
        private readonly Dictionary<string, CommandResponseEnvelope> _byToken;
        private readonly Queue<string> _order;
        private readonly object _lock = new();

        public CommandHistory(int capacity)
        {
            _capacity = capacity < 1 ? 1 : capacity;
            _byToken = new Dictionary<string, CommandResponseEnvelope>(StringComparer.Ordinal);
            _order = new Queue<string>();
        }

        public bool TryGet(string token, out CommandResponseEnvelope? response)
        {
            lock (_lock)
            {
                return _byToken.TryGetValue(token, out response);
            }
        }

        public void Add(string token, CommandResponseEnvelope response)
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
