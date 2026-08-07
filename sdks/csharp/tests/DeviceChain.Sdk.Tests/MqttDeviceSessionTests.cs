// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sdk.Transport;
using Xunit;

namespace DeviceChain.Sdk.Tests;

// The device lifecycle above the transport seam. The fake connection here NEVER confirms anything
// on its own — the test decides when a SUBACK lands and what it says — because a fake that
// auto-grants the subscribe would measure the fixture rather than the session.
public class MqttDeviceSessionTests
{
    private static readonly TimeSpan Timeout = TimeSpan.FromSeconds(10);

    private static MqttSessionOptions Options() =>
        new(new Uri("tcp://127.0.0.1:1883"), "inst", "acme", "sensor-001", "cred-1");

    // ── the fail-closed startup promise ──────────────────────────────────────

    // StartAsync must not return a session that is connected but deaf. The subscribe is held open
    // by the test, so a StartAsync that completed early would be completing on the CONNECT alone.
    [Fact]
    public async Task StartDoesNotCompleteUntilTheBrokerGrantsTheSubscription()
    {
        var connection = new FakeMqttConnection();
        var factory = new FakeMqttClientFactory(connection);
        await using var session = new MqttDeviceSession(Options(), factory);

        var start = session.StartAsync((_, _) => Task.FromResult(CommandOutcome.Succeeded()), CancellationToken.None);

        // The broker has not answered the SUBSCRIBE yet.
        await connection.SubscribeCalled.Task.WaitAsync(Timeout);
        Assert.False(start.IsCompleted);
        Assert.NotEqual(MqttSessionState.Ready, session.State);

        connection.CompleteSubscribe();
        await start.WaitAsync(Timeout);
        Assert.Equal(MqttSessionState.Ready, session.State);
    }

    // 🔴 A refusal must be surfaced as a throw AND as observable state. The state matters because
    // it is what lets a long-running caller tell "no commands have arrived" from "no command can
    // arrive" — the #668 shape, one layer up.
    [Fact]
    public async Task ARefusedSubscriptionLeavesTheSessionBlindRatherThanReady()
    {
        var connection = new FakeMqttConnection { RefuseSubscribe = true };
        var factory = new FakeMqttClientFactory(connection);
        await using var session = new MqttDeviceSession(Options(), factory);

        await Assert.ThrowsAsync<MqttSubscribeRefusedException>(
            () => session.StartAsync((_, _) => Task.FromResult(CommandOutcome.Succeeded()), CancellationToken.None));

        Assert.Equal(MqttSessionState.Blind, session.State);
        Assert.NotEqual(MqttSessionState.Ready, session.State);
    }

    // The session must subscribe to THIS device's command topic — a wrong topic is either refused
    // by the minted grant or silently receives nothing.
    [Fact]
    public async Task StartSubscribesToTheDevicesOwnCommandTopic()
    {
        var connection = new FakeMqttConnection();
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));

        var start = session.StartAsync((_, _) => Task.FromResult(CommandOutcome.Succeeded()), CancellationToken.None);
        await connection.SubscribeCalled.Task.WaitAsync(Timeout);
        connection.CompleteSubscribe();
        await start.WaitAsync(Timeout);

        Assert.Equal("inst/acme/device-commands/sensor-001", connection.SubscribedFilter);
        Assert.Equal("inst:acme:sensor-001", connection.Options!.ClientId);
        Assert.Equal("acme:cred-1", connection.Options!.Username);
        // Empty, not null: it is what selects access-token credential mode at the callout.
        Assert.Equal(string.Empty, connection.Options!.Password);
        // False so the broker holds undelivered commands across a reconnect.
        Assert.False(connection.Options!.CleanSession);
    }

    // ── ack only what the device actually accepted ───────────────────────────

    // The response must reflect the handler's OUTCOME, not the fact of delivery. An eager ack on
    // receipt would drive the command to SUCCESSFUL while the machine did nothing.
    [Fact]
    public async Task AFailedHandlerIsAnsweredAsFailedRatherThanAcknowledged()
    {
        var connection = new FakeMqttConnection();
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (_, _) => Task.FromResult(CommandOutcome.Failed("the dozer is stuck")));

        await connection.DeliverCommandAsync("cmd-1", "goRefuel");

        var response = connection.LastResponse();
        Assert.Equal("cmd-1", response.CommandToken);
        Assert.False(response.Success);
        Assert.Equal("the dozer is stuck", response.Error);
    }

    // A handler that throws did not carry the command out either — and reporting that beats
    // reporting nothing, which leaves the command at SENT until it expires.
    [Fact]
    public async Task AThrowingHandlerIsAnsweredAsFailedRatherThanSilently()
    {
        var connection = new FakeMqttConnection();
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (_, _) => throw new InvalidOperationException("hydraulics offline"));

        await connection.DeliverCommandAsync("cmd-1", "goRefuel");

        var response = connection.LastResponse();
        Assert.False(response.Success);
        Assert.Contains("hydraulics offline", response.Error);
    }

    [Fact]
    public async Task ASucceededHandlerIsAnsweredAsSuccessOnTheTenantScopedTopic()
    {
        var connection = new FakeMqttConnection();
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (_, _) => Task.FromResult(CommandOutcome.Succeeded("arrived")));

        await connection.DeliverCommandAsync("cmd-1", "goRefuel");

        var response = connection.LastResponse();
        Assert.True(response.Success);
        Assert.Equal("arrived", response.Payload);
        Assert.Equal("inst/acme/command-responses", connection.Published[^1].Topic);
        Assert.Equal(MqttQos.AtLeastOnce, connection.Published[^1].Qos);
    }

    // The handler must receive what the platform sent, including the raw payload.
    [Fact]
    public async Task TheHandlerReceivesTheCommandNameAndPayload()
    {
        var connection = new FakeMqttConnection();
        DeviceCommand? seen = null;
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (c, _) =>
        {
            seen = c;
            return Task.FromResult(CommandOutcome.Succeeded());
        });

        await connection.DeliverCommandAsync("cmd-1", "goRefuel", "{\"station\":\"north\"}");

        Assert.NotNull(seen);
        Assert.Equal("cmd-1", seen!.Token);
        Assert.Equal("goRefuel", seen.Name);
        Assert.Equal("north", seen.Payload!.Value.GetProperty("station").GetString());
    }

    // ── at-least-once redelivery ─────────────────────────────────────────────

    // 🔑 A REDELIVERY MUST NOT RUN THE HANDLER TWICE — a machine would move twice — BUT MUST
    // STILL BE ANSWERED. Both halves are asserted, because dropping the redelivery silently is
    // the plausible-looking bug: the redelivery is usually caused by a response that was lost, so
    // staying quiet guarantees the command never completes.
    [Fact]
    public async Task ARedeliveredCommandIsAnsweredAgainWithoutRerunningTheHandler()
    {
        var connection = new FakeMqttConnection();
        var invocations = 0;
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (_, _) =>
        {
            Interlocked.Increment(ref invocations);
            return Task.FromResult(CommandOutcome.Succeeded());
        });

        await connection.DeliverCommandAsync("cmd-1", "goRefuel");
        await connection.DeliverCommandAsync("cmd-1", "goRefuel");

        Assert.Equal(1, invocations);
        Assert.Equal(2, connection.Published.Count);
        Assert.Equal("cmd-1", connection.LastResponse().CommandToken);
    }

    // Distinct commands must each be handled — the dedupe must key on the token, not merely
    // suppress everything after the first.
    [Fact]
    public async Task DistinctCommandsAreEachHandled()
    {
        var connection = new FakeMqttConnection();
        var invocations = 0;
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (_, _) =>
        {
            Interlocked.Increment(ref invocations);
            return Task.FromResult(CommandOutcome.Succeeded());
        });

        await connection.DeliverCommandAsync("cmd-1", "goRefuel");
        await connection.DeliverCommandAsync("cmd-2", "goRefuel");

        Assert.Equal(2, invocations);
    }

    // The dedupe memory is bounded, so a device left running for months cannot leak. Past the
    // bound the oldest token is forgotten and would be handled again — the deliberate trade,
    // pinned so a future edit cannot silently make it unbounded.
    [Fact]
    public async Task TheCommandHistoryIsBounded()
    {
        var connection = new FakeMqttConnection();
        var options = Options();
        options.CommandHistorySize = 2;
        var invocations = 0;
        await using var session = new MqttDeviceSession(options, new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (_, _) =>
        {
            Interlocked.Increment(ref invocations);
            return Task.FromResult(CommandOutcome.Succeeded());
        });

        await connection.DeliverCommandAsync("cmd-1", "goRefuel");
        await connection.DeliverCommandAsync("cmd-2", "goRefuel");
        await connection.DeliverCommandAsync("cmd-3", "goRefuel");
        // cmd-1 has been evicted, so it is handled afresh rather than served from memory.
        await connection.DeliverCommandAsync("cmd-1", "goRefuel");

        Assert.Equal(4, invocations);
    }

    // ── frames that are not commands ─────────────────────────────────────────

    // A frame that cannot be decoded is counted and dropped, never answered: the answer is keyed
    // by a token that could not be read, and re-raising would make the broker redeliver a poison
    // frame forever.
    [Fact]
    public async Task AMalformedFrameIsCountedAndNotAnswered()
    {
        var connection = new FakeMqttConnection();
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (_, _) => Task.FromResult(CommandOutcome.Succeeded()));

        await connection.DeliverRawAsync("inst/acme/device-commands/sensor-001", "not json at all");
        await connection.DeliverRawAsync("inst/acme/device-commands/sensor-001", "{\"name\":\"noToken\"}");

        Assert.Equal(2, session.MalformedFrames);
        Assert.Empty(connection.Published);
    }

    // ── reconnect ────────────────────────────────────────────────────────────

    // A dropped connection must be re-established AND re-subscribed. Asserting the subscribe on
    // the SECOND connection is the point: a reconnect that restores the socket but not the
    // subscription produces a device that is connected, looks healthy, and receives nothing.
    [Fact]
    public async Task AReconnectResubscribesOnTheNewConnection()
    {
        var first = new FakeMqttConnection();
        var second = new FakeMqttConnection();
        var factory = new FakeMqttClientFactory(first, second);
        var options = Options();
        options.ReconnectInitialDelay = TimeSpan.FromMilliseconds(20);

        await using var session = new MqttDeviceSession(options, factory);
        await StartAsync(session, first, (_, _) => Task.FromResult(CommandOutcome.Succeeded()));

        first.DropConnection(new Exception("broker restarted"));

        await second.SubscribeCalled.Task.WaitAsync(Timeout);
        second.CompleteSubscribe();
        await WaitForStateAsync(session, MqttSessionState.Ready);

        Assert.Equal("inst/acme/device-commands/sensor-001", second.SubscribedFilter);
    }

    // 🔴 AND THE GRANT MUST BE RE-READ ON RECONNECT, NOT ASSUMED FROM THE FIRST ONE. A broker that
    // refuses the re-subscribe (a credential revoked while the device was away) leaves a session
    // that is connected and permanently deaf — so it must land in Blind, never back in Ready.
    [Fact]
    public async Task ARefusalOnReconnectLeavesTheSessionBlindRatherThanReady()
    {
        var first = new FakeMqttConnection();
        var second = new FakeMqttConnection { RefuseSubscribe = true };
        var factory = new FakeMqttClientFactory(first, second);
        var options = Options();
        options.ReconnectInitialDelay = TimeSpan.FromMilliseconds(20);

        await using var session = new MqttDeviceSession(options, factory);
        await StartAsync(session, first, (_, _) => Task.FromResult(CommandOutcome.Succeeded()));

        first.DropConnection(new Exception("broker restarted"));

        await WaitForStateAsync(session, MqttSessionState.Blind);
        Assert.Equal(MqttSessionState.Blind, session.State);
    }

    // ── client id and topic composition ──────────────────────────────────────

    [Theory]
    [InlineData("inst", "acme", "sensor-001", null, "inst:acme:sensor-001")]
    [InlineData("inst", "acme", "sensor-001", "sub", "inst:acme:sensor-001:sub")]
    public void DeviceClientIdComposesTheAdmittedShape(
        string instance, string tenant, string device, string? discriminator, string expected)
    {
        Assert.Equal(expected, DevicePlane.DeviceClientId(instance, tenant, device, discriminator));
    }

    // It refuses rather than composes when a field is not token-grammar-safe: a "." or "*" would
    // stop the id being a single subject token, so the gateway would file the session somewhere
    // the shape does not describe.
    [Theory]
    [InlineData("inst.bad", "acme", "sensor-001")]
    [InlineData("inst", "acme*", "sensor-001")]
    [InlineData("inst", "acme", "sensor/001")]
    [InlineData("inst", "acme", "")]
    [InlineData("inst", "acme", "-leading-hyphen")]
    public void DeviceClientIdRefusesAnUnsafeField(string instance, string tenant, string device)
    {
        Assert.Throws<ArgumentException>(() => DevicePlane.DeviceClientId(instance, tenant, device));
    }

    [Fact]
    public void DevicePlaneTopicsMatchThePlatformsSubjects()
    {
        Assert.Equal("inst/acme/devices/sensor-001/events", DevicePlane.EventsTopic("inst", "acme", "sensor-001"));
        Assert.Equal("inst/acme/device-commands/sensor-001", DevicePlane.CommandsTopic("inst", "acme", "sensor-001"));
        Assert.Equal("inst/acme/command-responses", DevicePlane.CommandResponsesTopic("inst", "acme"));
    }

    // ── helpers ──────────────────────────────────────────────────────────────

    private static async Task WaitForStateAsync(MqttDeviceSession session, MqttSessionState expected)
    {
        var deadline = DateTime.UtcNow + Timeout;
        while (DateTime.UtcNow < deadline)
        {
            if (session.State == expected)
            {
                return;
            }

            await Task.Delay(10);
        }

        throw new TimeoutException($"the session never reached {expected}; it is {session.State}");
    }

    private static async Task StartAsync(MqttDeviceSession session, FakeMqttConnection connection, CommandHandler handler)
    {
        var start = session.StartAsync(handler, CancellationToken.None);
        await connection.SubscribeCalled.Task.WaitAsync(Timeout);
        connection.CompleteSubscribe();
        await start.WaitAsync(Timeout);
    }

    // Hands out one connection per Create(), the way the real factory does — a reconnect gets a
    // FRESH connection, never the dropped one. Modelling that matters: a factory that returned the
    // same instance would let a reconnect bug (re-registering handlers on a reused connection)
    // pass unnoticed.
    private sealed class FakeMqttClientFactory : IMqttClientFactory
    {
        private readonly Queue<FakeMqttConnection> _pending = new();

        public FakeMqttClientFactory(params FakeMqttConnection[] connections)
        {
            foreach (var connection in connections)
            {
                _pending.Enqueue(connection);
            }
        }

        public List<FakeMqttConnection> Created { get; } = new();

        public IMqttConnection Create()
        {
            var connection = _pending.Count > 0 ? _pending.Dequeue() : new FakeMqttConnection();
            Created.Add(connection);
            return connection;
        }
    }

    // A connection whose every confirmation is driven by the TEST. Nothing here completes a
    // subscribe on its own — that is the step under test.
    private sealed class FakeMqttConnection : IMqttConnection
    {
        private readonly TaskCompletionSource<bool> _subackGate =
            new(TaskCreationOptions.RunContinuationsAsynchronously);

        public TaskCompletionSource<bool> SubscribeCalled { get; } =
            new(TaskCreationOptions.RunContinuationsAsynchronously);

        public bool RefuseSubscribe { get; set; }

        public MqttConnectOptions? Options { get; private set; }

        public string? SubscribedFilter { get; private set; }

        public List<(string Topic, byte[] Payload, MqttQos Qos)> Published { get; } = new();

        public bool IsConnected { get; private set; }

        public event Func<MqttInbound, Task>? MessageReceived;

        public event Action<Exception?>? ConnectionLost;

        public Task ConnectAsync(MqttConnectOptions options, CancellationToken cancellationToken)
        {
            Options = options;
            IsConnected = true;
            return Task.CompletedTask;
        }

        public async Task SubscribeAsync(string topicFilter, MqttQos qos, CancellationToken cancellationToken)
        {
            SubscribedFilter = topicFilter;
            SubscribeCalled.TrySetResult(true);

            if (RefuseSubscribe)
            {
                throw new MqttSubscribeRefusedException(topicFilter);
            }

            await _subackGate.Task.WaitAsync(cancellationToken).ConfigureAwait(false);
        }

        public void CompleteSubscribe() => _subackGate.TrySetResult(true);

        public Task PublishAsync(string topic, byte[] payload, MqttQos qos, CancellationToken cancellationToken)
        {
            Published.Add((topic, payload, qos));
            return Task.CompletedTask;
        }

        public Task DisconnectAsync(TimeSpan quiesce, CancellationToken cancellationToken)
        {
            IsConnected = false;
            return Task.CompletedTask;
        }

        public ValueTask DisposeAsync() => default;

        public Task DeliverCommandAsync(string token, string name, string? payloadJson = null)
        {
            var payload = payloadJson == null ? "null" : payloadJson;
            var json = $"{{\"token\":\"{token}\",\"deviceToken\":\"sensor-001\",\"name\":\"{name}\",\"payload\":{payload}}}";
            return DeliverRawAsync("inst/acme/device-commands/sensor-001", json);
        }

        public Task DeliverRawAsync(string topic, string body)
        {
            var handler = MessageReceived;
            return handler == null
                ? Task.CompletedTask
                : handler(new MqttInbound(topic, Encoding.UTF8.GetBytes(body)));
        }

        public void DropConnection(Exception? cause = null)
        {
            IsConnected = false;
            ConnectionLost?.Invoke(cause);
        }

        public CommandResponseEnvelope LastResponse()
        {
            var last = Published[^1];
            return JsonSerializer.Deserialize<CommandResponseEnvelope>(last.Payload)!;
        }
    }
}
