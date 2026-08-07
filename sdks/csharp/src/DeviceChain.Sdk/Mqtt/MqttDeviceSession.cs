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
    /// Connected, but the broker REFUSED the command subscription — so no command will ever
    /// arrive and the silence is not clean.
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

    private IMqttConnection? _connection;
    private CommandHandler? _handler;
    private MqttSessionState _state = MqttSessionState.Starting;
    private int _disposed;

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
            lock (_stopped)
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

    /// <summary>
    /// Connects, subscribes to this device's command topic, and returns ONLY once the broker has
    /// GRANTED that subscription.
    /// </summary>
    /// <remarks>
    /// 🔴 THE RETURN IS THE PROMISE, AND IT IS FAIL-CLOSED ABOUT ITS OWN BLINDNESS. If the broker
    /// refuses the subscription the session goes <see cref="MqttSessionState.Blind"/> and this
    /// throws, rather than returning a session that is connected, looks healthy, and will never
    /// receive anything. A device that never got a confirmed grant is reported un-subscribed, so
    /// its silence is never read as clean.
    /// </remarks>
    /// <exception cref="MqttSubscribeRefusedException">The broker refused the command subscription.</exception>
    public async Task StartAsync(CommandHandler handler, CancellationToken cancellationToken)
    {
        _handler = handler ?? throw new ArgumentNullException(nameof(handler));

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
        var connection = _connection;
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
        connection.MessageReceived += OnMessageAsync;
        connection.ConnectionLost += OnConnectionLost;

        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, _stopped.Token);
        timeout.CancelAfter(_options.OperationTimeout);

        var options = new MqttConnectOptions(_options.BrokerUri, _clientId)
        {
            Username = DevicePlane.ConnectUsername(_options.Tenant, _options.CredentialId),
            // Empty, not absent: it is what selects access-token credential mode at the callout.
            Password = string.Empty,
            // False so the broker holds this device's subscription and any undelivered QoS-1
            // commands across a reconnect — which is what stops a command being lost while the
            // device is briefly away.
            CleanSession = false,
            KeepAlive = _options.KeepAlive,
            Trust = _options.Trust,
        };

        await connection.ConnectAsync(options, timeout.Token).ConfigureAwait(false);
        _connection = connection;

        try
        {
            // Inside the connect path on EVERY (re)connect, so a reconnect re-establishes the
            // subscription by the same code that established it — and re-reads the grant.
            await connection.SubscribeAsync(_commandsTopic, MqttQos.AtLeastOnce, timeout.Token).ConfigureAwait(false);
        }
        catch (MqttSubscribeRefusedException)
        {
            // Connected but deaf. Surfacing this as state, not just as a throw, is what lets a
            // long-running caller tell "no commands have arrived" from "no command can arrive".
            SetState(MqttSessionState.Blind);
            throw;
        }

        SetState(MqttSessionState.Ready);
    }

    private void OnConnectionLost(Exception? cause)
    {
        if (_disposed != 0 || _stopped.IsCancellationRequested)
        {
            return;
        }

        SetState(MqttSessionState.Reconnecting);
        _ = Task.Run(ReconnectLoopAsync);
    }

    private async Task ReconnectLoopAsync()
    {
        var delay = _options.ReconnectInitialDelay;

        // Jittered so a scene's worth of devices reconnecting after one broker restart do not
        // arrive as a synchronised thundering herd — 18 machines is enough to notice.
        var jitter = new Random(_clientId.GetHashCode());

        while (!_stopped.IsCancellationRequested && _disposed == 0)
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
                var previous = _connection;
                _connection = null;
                if (previous != null)
                {
                    await previous.DisposeAsync().ConfigureAwait(false);
                }

                await ConnectAndSubscribeAsync(_stopped.Token).ConfigureAwait(false);
                return;
            }
            catch (MqttSubscribeRefusedException)
            {
                // A refusal is not transient: the credential is not permitted to read that topic,
                // so retrying forever would change nothing while looking like progress. Stay
                // Blind and stop — the state is the report.
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

    private async Task OnMessageAsync(MqttInbound inbound)
    {
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
            MalformedFrames++;
            return;
        }

        if (envelope?.Token == null || envelope.Token.Length == 0)
        {
            MalformedFrames++;
            return;
        }

        // 🔴 DE-DUPE BY COMMAND TOKEN. Delivery is at-least-once, so the same token can arrive
        // more than once — a redelivery must NOT run the handler again (a device would move
        // twice), but it must still be ANSWERED, or the redelivery was caused by a lost response
        // and dropping it silently guarantees the command never completes.
        if (_history.TryGet(envelope.Token, out var cached))
        {
            await PublishResponseAsync(cached!).ConfigureAwait(false);
            return;
        }

        var handler = _handler;
        if (handler == null)
        {
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

        _history.Add(envelope.Token, response);
        await PublishResponseAsync(response).ConfigureAwait(false);
    }

    private async Task PublishResponseAsync(CommandResponseEnvelope response)
    {
        var connection = _connection;
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

    /// <summary>How many inbound frames could not be decoded into a command.</summary>
    /// <remarks>
    /// Exposed as a count rather than logged-and-forgotten because it is the only evidence that a
    /// device is receiving something it cannot act on.
    /// </remarks>
    public int MalformedFrames { get; private set; }

    private void SetState(MqttSessionState state)
    {
        bool changed;
        lock (_stopped)
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

        var connection = _connection;
        _connection = null;
        if (connection != null)
        {
            using var quiesce = new CancellationTokenSource(TimeSpan.FromSeconds(5));
            await connection.DisconnectAsync(TimeSpan.FromMilliseconds(250), quiesce.Token).ConfigureAwait(false);
            await connection.DisposeAsync().ConfigureAwait(false);
        }

        _gate.Dispose();
        _stopped.Dispose();
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
