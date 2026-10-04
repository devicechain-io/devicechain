// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
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

        // 🔴 QoS 1 IS THE WHOLE cleanSession=false PROMISE. At QoS 0 the broker queues nothing
        // while the device is away and never redelivers, so a command issued during a blip is
        // simply lost — the exact failure persistence exists to prevent. An adversarial pass
        // flipped this to AtMostOnce and every gate stayed green, including the real-broker
        // persistence test, because that test subscribes with its own raw connection rather than
        // through the session.
        Assert.Equal(MqttQos.AtLeastOnce, connection.SubscribedQos);

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
    public async Task ASucceededHandlerIsAnsweredAsSuccessOnTheDevicesOwnTopic()
    {
        var connection = new FakeMqttConnection();
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (_, _) => Task.FromResult(CommandOutcome.Succeeded("arrived")));

        await connection.DeliverCommandAsync("cmd-1", "goRefuel");

        var response = connection.LastResponse();
        Assert.True(response.Success);
        Assert.Equal("arrived", response.Payload);
        // 🔴 THE TOPIC IS PART OF THE ANSWER, NOT PLUMBING. The platform reads the responding
        // device off the topic and refuses a response for a command belonging to someone else,
        // so a session publishing on the old tenant-wide topic would run the handler, report
        // success locally, and have its answer discarded — the command then TIMEOUTs looking
        // like a device that never replied.
        Assert.Equal("inst/acme/command-responses/sensor-001", connection.Published[^1].Topic);
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

    // ── the dispatch nonce ───────────────────────────────────────────────────

    // 🔴 THE ANSWER MUST NAME THE DISPATCH IT ANSWERS, OR THE PLATFORM REFUSES IT. The command
    // then sits at SENT until it expires as TIMEOUT — a record blaming the device for a reply it
    // did send. The value is asserted, not merely its presence: an invented one names a dispatch
    // nobody is holding and is refused exactly the same way.
    [Fact]
    public async Task TheResponseEchoesTheDispatchNonceItWasSent()
    {
        var connection = new FakeMqttConnection();
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (_, _) => Task.FromResult(CommandOutcome.Succeeded()));

        await connection.DeliverCommandAsync("cmd-1", "goRefuel", dispatchNonce: "nonce-7");

        Assert.Equal("nonce-7", connection.LastResponse().DispatchNonce);
    }

    // 🔴🔴 A REDELIVERY UNDER A NEW NONCE IS ANSWERED UNDER THE NEW ONE, AND THIS IS THE CASE
    // THE WHOLE FIELD EXISTS FOR. A command whose publish reported an error is returned to the
    // platform's queue and dispatched again under a different nonce — to a device that already
    // ran it. The handler must NOT run a second time (a machine would move twice), so the
    // remembered outcome is republished; but it must be republished under the nonce THIS
    // delivery carried, because the platform has moved off the first one and would refuse an
    // answer naming it. Caching the response envelope whole is the plausible-looking bug, and it
    // would leave such a command permanently unsettleable.
    [Fact]
    public async Task ARedeliveryUnderANewNonceIsAnsweredUnderThatNonceWithoutRerunningTheHandler()
    {
        var connection = new FakeMqttConnection();
        var invocations = 0;
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (_, _) =>
        {
            Interlocked.Increment(ref invocations);
            return Task.FromResult(CommandOutcome.Failed("the dozer is stuck"));
        });

        await connection.DeliverCommandAsync("cmd-1", "goRefuel", dispatchNonce: "nonce-first");
        await connection.DeliverCommandAsync("cmd-1", "goRefuel", dispatchNonce: "nonce-second");

        Assert.Equal(1, invocations);
        Assert.Equal(2, connection.Published.Count);
        var response = connection.LastResponse();
        Assert.Equal("nonce-second", response.DispatchNonce);
        // The OUTCOME is still the remembered one: only the dispatch it names is new.
        Assert.False(response.Success);
        Assert.Equal("the dozer is stuck", response.Error);
    }

    // A frame naming no dispatch is answered honestly rather than doctored. The SDK cannot
    // invent a value the platform would accept, and inventing one would be worse than the
    // refusal: it would name a dispatch this device may not have received.
    [Fact]
    public async Task ACommandNamingNoDispatchIsAnsweredWithNoNonce()
    {
        var connection = new FakeMqttConnection();
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (_, _) => Task.FromResult(CommandOutcome.Succeeded()));

        await connection.DeliverRawAsync("inst/acme/device-commands/sensor-001",
            "{\"token\":\"cmd-1\",\"deviceToken\":\"sensor-001\",\"name\":\"goRefuel\"}");

        Assert.Single(connection.Published);
        Assert.True(string.IsNullOrEmpty(connection.LastResponse().DispatchNonce));
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

    // 🔑 AND THE REDELIVERY MUST REPEAT THE ORIGINAL OUTCOME, NOT A FABRICATED SUCCESS. The
    // earlier test redelivers a SUCCEEDED command and asserts only the token, so answering every
    // redelivery `success:true` escaped it — which would drive a command the machine REFUSED all
    // the way to SUCCESSFUL.
    [Fact]
    public async Task ARedeliveredFailedCommandIsAnsweredAsFailedAgain()
    {
        var connection = new FakeMqttConnection();
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (_, _) => Task.FromResult(CommandOutcome.Failed("the dozer is stuck")));

        await connection.DeliverCommandAsync("cmd-1", "goRefuel");
        await connection.DeliverCommandAsync("cmd-1", "goRefuel");

        Assert.Equal(2, connection.Published.Count);
        var response = connection.LastResponse();
        Assert.False(response.Success);
        Assert.Equal("the dozer is stuck", response.Error);
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

    // 🔴 THE WINDOW A COMPLETED-ONLY CACHE MISSES. The broker redelivers precisely when the first
    // delivery has NOT been answered yet — a handler still working, or a response lost — which is
    // exactly when a cache of finished commands holds nothing. A duplicate arriving then must
    // coalesce onto the first execution, not start a second one: a dozer that receives "go refuel"
    // twice mid-move drives twice.
    [Fact]
    public async Task ADuplicateArrivingWhileTheHandlerIsStillRunningDoesNotRunItAgain()
    {
        var connection = new FakeMqttConnection();
        var invocations = 0;
        var release = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        var entered = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);

        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, async (_, _) =>
        {
            Interlocked.Increment(ref invocations);
            entered.TrySetResult(true);
            await release.Task;
            return CommandOutcome.Succeeded();
        });

        var first = connection.DeliverCommandAsync("cmd-1", "goRefuel");
        await entered.Task.WaitAsync(Timeout);

        // The redelivery lands while the handler is still in flight.
        var duplicate = connection.DeliverCommandAsync("cmd-1", "goRefuel");
        release.TrySetResult(true);
        await Task.WhenAll(first, duplicate).WaitAsync(Timeout);

        Assert.Equal(1, invocations);
        // Still answered twice: the redelivery was probably caused by a lost response.
        Assert.Equal(2, connection.Published.Count);
    }

    // ── what the session tells the outside world ─────────────────────────────

    // The public StateChanged event had NO subscriber anywhere in the suite, so raising it with a
    // wrong value went unnoticed — a Blind session could announce Ready.
    [Fact]
    public async Task StateChangedReportsTheStateItActuallyReached()
    {
        var connection = new FakeMqttConnection { RefuseSubscribe = true };
        var observed = new List<MqttSessionState>();
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        session.StateChanged += state => observed.Add(state);

        await Assert.ThrowsAsync<MqttSubscribeRefusedException>(
            () => session.StartAsync((_, _) => Task.FromResult(CommandOutcome.Succeeded()), CancellationToken.None));

        Assert.Contains(MqttSessionState.Blind, observed);
        Assert.DoesNotContain(MqttSessionState.Ready, observed);
    }

    // The topic filter's only exerciser is this test. It matters the day the session subscribes to
    // anything else: without it, every inbound frame would be parsed as a command.
    [Fact]
    public async Task AFrameOnAnotherTopicIsIgnoredEntirely()
    {
        var connection = new FakeMqttConnection();
        var invocations = 0;
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (_, _) =>
        {
            Interlocked.Increment(ref invocations);
            return Task.FromResult(CommandOutcome.Succeeded());
        });

        await connection.DeliverRawAsync(
            "inst/acme/device-commands/SOMEONE-ELSE",
            "{\"token\":\"cmd-1\",\"deviceToken\":\"other\",\"name\":\"goRefuel\"}");

        Assert.Equal(0, invocations);
        Assert.Empty(connection.Published);
        // Not a malformed frame either — it decoded fine, it simply was not ours.
        Assert.Equal(0, session.MalformedFrames);
    }

    // Starting twice would leave the first connection live, still wired to the handler, and both
    // present the same client id — so the broker would evict one with the other.
    [Fact]
    public async Task StartingTwiceIsRefused()
    {
        var connection = new FakeMqttConnection();
        await using var session = new MqttDeviceSession(Options(), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (_, _) => Task.FromResult(CommandOutcome.Succeeded()));

        await Assert.ThrowsAsync<InvalidOperationException>(
            () => session.StartAsync((_, _) => Task.FromResult(CommandOutcome.Succeeded()), CancellationToken.None));
    }

    // The SESSION must impose its own deadline. Relying on the caller's token means a broker that
    // accepts the connection and then goes quiet hangs startup forever.
    [Fact]
    public async Task StartIsBoundedByTheSessionsOwnOperationTimeout()
    {
        var connection = new FakeMqttConnection();
        var options = Options();
        options.OperationTimeout = TimeSpan.FromMilliseconds(250);
        await using var session = new MqttDeviceSession(options, new FakeMqttClientFactory(connection));

        // CancellationToken.None: only the session's own timeout can end this.
        await Assert.ThrowsAnyAsync<OperationCanceledException>(
            () => session.StartAsync((_, _) => Task.FromResult(CommandOutcome.Succeeded()), CancellationToken.None));
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

    // 🔴 THE RECONNECT-STORM TEST. MQTTnet raises its disconnected event for a connection that was
    // NEVER ESTABLISHED — measured against the real library — so an implementation that wires the
    // lost-connection handler before connecting spawns a new reconnect loop per FAILED ATTEMPT and
    // the loops multiply: ~1,100 dials per second against a down broker, where one backed-off loop
    // should manage about two. With 18 machines that is a self-inflicted denial of service on the
    // gateway during exactly the outage the reconnect exists to survive.
    //
    // The fake reproduces the behaviour that causes it (a failed connect raises ConnectionLost),
    // so this measures the session's structure rather than a fixture that cannot storm.
    //
    // 🔑 TWO INDEPENDENT DEFENCES STOP IT, AND EITHER ONE ALONE IS SUFFICIENT — measured, so the
    // claim is not louder than the evidence: wiring ConnectionLost only after a successful connect,
    // and the single-loop interlock. Removing either leaves this test passing; removing BOTH — the
    // shape the code originally had — produces 5,377 dial attempts in 1.2 seconds. So this test
    // pins the pair rather than either mechanism, which is why both carry their own comment.
    [Fact]
    public async Task AFailingReconnectDoesNotMultiplyIntoAStorm()
    {
        var first = new FakeMqttConnection();
        var factory = new StormFactory(first);
        var options = Options();
        options.ReconnectInitialDelay = TimeSpan.FromMilliseconds(50);
        options.ReconnectMaxDelay = TimeSpan.FromMilliseconds(200);

        await using var session = new MqttDeviceSession(options, factory);
        await StartAsync(session, first, (_, _) => Task.FromResult(CommandOutcome.Succeeded()));

        first.DropConnection(new Exception("broker went away"));
        await Task.Delay(1200);

        // One backed-off loop over ~1.2s at 50→200ms with jitter is a handful of attempts. A
        // multiplying implementation reaches the hundreds or thousands.
        var attempts = factory.FailedAttempts;
        Assert.InRange(attempts, 1, 40);

        // And every failed attempt must be disposed: _connection is only assigned on success, so
        // without explicit cleanup each retry leaks a client for the length of the outage.
        Assert.Equal(attempts, factory.DisposedFailures);
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

    // The discriminator is deliberately NOT held to the token grammar — it is chosen inside a
    // namespace the broker already authenticated — but it must still not carry the characters that
    // would stop the composed id being a single NATS subject token. That guard had no test, so
    // deleting it entirely passed every gate.
    [Theory]
    [InlineData("has.dot")]
    [InlineData("has*star")]
    [InlineData("has>gt")]
    [InlineData("has space")]
    public void DeviceClientIdRefusesADiscriminatorThatBreaksTheSubjectToken(string discriminator)
    {
        Assert.Throws<ArgumentException>(
            () => DevicePlane.DeviceClientId("inst", "acme", "sensor-001", discriminator));
    }

    [Fact]
    public void DevicePlaneTopicsMatchThePlatformsSubjects()
    {
        Assert.Equal("inst/acme/devices/sensor-001/events", DevicePlane.EventsTopic("inst", "acme", "sensor-001"));
        Assert.Equal("inst/acme/device-commands/sensor-001", DevicePlane.CommandsTopic("inst", "acme", "sensor-001"));
        Assert.Equal("inst/acme/command-responses/sensor-001",
            DevicePlane.CommandResponsesTopic("inst", "acme", "sensor-001"));

        // 🔴 COMMANDS AND RESPONSES MUST BE SCOPED THE SAME WAY. While responses were
        // tenant-wide, a device's grant let it report an outcome for any command in the
        // tenant — so a response topic that stopped naming the device would reopen that
        // without breaking a single delivery test.
        Assert.NotEqual(
            DevicePlane.CommandResponsesTopic("inst", "acme", "sensor-001"),
            DevicePlane.CommandResponsesTopic("inst", "acme", "sensor-002"));
    }

    // ── concurrent command handlers ──────────────────────────────────────────
    //
    // Deliveries here go through DeliverInOrder, which models the shipped transport: ONE pump
    // that calls the receive callback for message k+1 only after the callback for message k has
    // returned. The Task it returns completes when the callback returns, which is the point at
    // which the transport acknowledges the message.

    private static async Task<bool> CompletesAsync(Task task)
    {
        return await Task.WhenAny(task, Task.Delay(Timeout)) == task;
    }

    private static MqttSessionOptions ConcurrentOptions(int max, Func<DeviceCommand, string?>? lane = null)
    {
        var options = Options();
        options.MaxConcurrentCommands = max;
        options.CommandLane = lane;
        return options;
    }

    // Handler A blocks a thread synchronously, which is what catches an executor that runs the
    // handler inline on the receive thread. It is one blocked thread-pool thread, which is fine;
    // do not scale this pattern up into thread-pool starvation.
    [Fact]
    public async Task TwoSlowHandlersOverlapWhenTwoMayRunAtOnce()
    {
        var connection = new FakeMqttConnection();
        var probe = new HandlerProbe();
        probe.BlockSynchronously("a");
        await using var session = new MqttDeviceSession(ConcurrentOptions(2), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, probe.Handler);
        try
        {
            var a = connection.DeliverInOrder("a", "work");
            var b = connection.DeliverInOrder("b", "work");

            Assert.True(await CompletesAsync(Task.WhenAll(probe.Entered("a"), probe.Entered("b"))),
                "both handlers should be running at once");
            // The callback returned for A while A is still held: the receive path is free.
            Assert.True(await CompletesAsync(a));
            Assert.True(await CompletesAsync(b));
            Assert.Equal(2, probe.MaxRunning);
        }
        finally
        {
            probe.ReleaseAll();
        }
    }

    // The counterweight to the test above: with the default, behaviour is exactly as before. It
    // passes on the old code by design.
    [Fact]
    public async Task WithTheDefaultHandlersRunOneAtATimeExactlyAsBefore()
    {
        var connection = new FakeMqttConnection();
        var probe = new HandlerProbe();
        var options = Options();
        Assert.Equal(1, options.MaxConcurrentCommands);
        await using var session = new MqttDeviceSession(options, new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, probe.Handler);
        try
        {
            var a = connection.DeliverInOrder("a", "work");
            var b = connection.DeliverInOrder("b", "work");

            Assert.True(await CompletesAsync(probe.Entered("a")));
            Assert.False(a.IsCompleted);
            Assert.False(probe.Entered("b").IsCompleted);

            probe.Release("a");
            // The acknowledgement point is after the response is published.
            Assert.True(await CompletesAsync(a));
            Assert.Contains(connection.Responses(), r => r.CommandToken == "a");

            probe.Release("b");
            Assert.True(await CompletesAsync(b));
            Assert.Equal(new[] { "a", "b" }, probe.EntryOrder());
            Assert.Equal(1, probe.MaxRunning);
        }
        finally
        {
            probe.ReleaseAll();
        }
    }

    [Fact]
    public async Task SameLaneCommandsRunInArrivalOrderWhileAnotherLaneRuns()
    {
        var connection = new FakeMqttConnection();
        var probe = new HandlerProbe();
        probe.Release("x2");
        probe.Release("y1");
        probe.Release("y2");
        await using var session = new MqttDeviceSession(
            ConcurrentOptions(3, c => c.Name), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, probe.Handler);
        try
        {
            // Lane is the command name: x1 and x2 share lane "X", y1 and y2 share lane "Y".
            _ = connection.DeliverInOrder("x1", "X");
            _ = connection.DeliverInOrder("x2", "X");
            _ = connection.DeliverInOrder("y1", "Y");
            _ = connection.DeliverInOrder("y2", "Y");

            // The other lane completes while x1 is held, which gives the executor every chance to
            // start x2 early.
            Assert.True(await CompletesAsync(connection.WaitForPublishedAsync(2)));
            Assert.Equal(new[] { "y1", "y2" }, connection.Responses().Select(r => r.CommandToken).OrderBy(t => t));
            Assert.False(probe.Entered("x2").IsCompleted);
            Assert.Equal(1, session.QueuedCommands);

            probe.Release("x1");
            Assert.True(await CompletesAsync(probe.Entered("x2")));
            var order = probe.EntryOrder();
            // The three that may start together can reach the handler in any order; x2 is last.
            Assert.Equal(new[] { "x1", "y1", "y2" }, order.Take(3).OrderBy(t => t));
            Assert.Equal("x2", order[3]);
        }
        finally
        {
            probe.ReleaseAll();
        }
    }

    [Fact]
    public async Task NoMoreThanTheCapRunAndTheRestStartInArrivalOrder()
    {
        var connection = new FakeMqttConnection();
        var probe = new HandlerProbe();
        await using var session = new MqttDeviceSession(ConcurrentOptions(2), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, probe.Handler);
        try
        {
            var deliveries = new[]
            {
                _ = connection.DeliverInOrder("c1", "work"),
                _ = connection.DeliverInOrder("c2", "work"),
                _ = connection.DeliverInOrder("c3", "work"),
                _ = connection.DeliverInOrder("c4", "work"),
            };
            Assert.True(await CompletesAsync(Task.WhenAll(deliveries)));
            Assert.True(await CompletesAsync(Task.WhenAll(probe.Entered("c1"), probe.Entered("c2"))));
            Assert.Equal(2, session.RunningCommands);
            Assert.Equal(2, session.QueuedCommands);
            Assert.False(probe.Entered("c3").IsCompleted);

            probe.Release("c1");
            // c3, not c4: waiting commands start in arrival order.
            Assert.True(await CompletesAsync(probe.Entered("c3")));
            Assert.False(probe.Entered("c4").IsCompleted);
            Assert.Equal(1, session.QueuedCommands);

            probe.Release("c2");
            probe.Release("c3");
            probe.Release("c4");
            Assert.True(await CompletesAsync(connection.WaitForPublishedAsync(4)));
            Assert.Equal(2, probe.MaxRunning);
            Assert.Equal(new[] { "c1", "c2" }, probe.EntryOrder().Take(2).OrderBy(t => t));
            Assert.Equal(new[] { "c3", "c4" }, probe.EntryOrder().Skip(2));
        }
        finally
        {
            probe.ReleaseAll();
        }
    }

    [Fact]
    public async Task ADuplicateOfARunningCommandIsAnsweredWithoutASecondExecutionAndBlocksNothing()
    {
        var connection = new FakeMqttConnection();
        var probe = new HandlerProbe();
        probe.Release("cmd-2");
        await using var session = new MqttDeviceSession(ConcurrentOptions(2), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, probe.Handler);
        try
        {
            _ = connection.DeliverInOrder("cmd-1", "work", "nonce-a");
            Assert.True(await CompletesAsync(probe.Entered("cmd-1")));
            var duplicate = connection.DeliverInOrder("cmd-1", "work", "nonce-b");
            _ = connection.DeliverInOrder("cmd-2", "work", "nonce-c");

            // Neither the duplicate nor the command behind it waits for the slow handler.
            Assert.True(await CompletesAsync(duplicate));
            Assert.True(await CompletesAsync(connection.WaitForPublishedAsync(1)));
            Assert.Equal("cmd-2", connection.Responses()[0].CommandToken);

            probe.Release("cmd-1");
            Assert.True(await CompletesAsync(connection.WaitForPublishedAsync(3)));
            Assert.Equal(1, probe.Invocations("cmd-1"));
            var answers = connection.Responses().Where(r => r.CommandToken == "cmd-1").ToList();
            Assert.Equal(new[] { "nonce-a", "nonce-b" }, answers.Select(r => r.DispatchNonce).OrderBy(n => n));
            Assert.All(answers, r => Assert.True(r.Success));
        }
        finally
        {
            probe.ReleaseAll();
        }
    }

    [Fact]
    public async Task ADuplicateOfAQueuedCommandCoalescesOntoIt()
    {
        var connection = new FakeMqttConnection();
        var probe = new HandlerProbe();
        probe.Release("c2");
        await using var session = new MqttDeviceSession(
            ConcurrentOptions(2, _ => "L"), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, probe.Handler);
        try
        {
            _ = connection.DeliverInOrder("c1", "work", "n1");
            Assert.True(await CompletesAsync(probe.Entered("c1")));
            _ = connection.DeliverInOrder("c2", "work", "n2a");
            var duplicate = connection.DeliverInOrder("c2", "work", "n2b");

            // c2 is queued behind c1 in the same lane; its duplicate must not wait for c1.
            Assert.True(await CompletesAsync(duplicate));
            Assert.Equal(1, session.QueuedCommands);

            probe.Release("c1");
            Assert.True(await CompletesAsync(connection.WaitForPublishedAsync(3)));
            Assert.Equal(1, probe.Invocations("c2"));
            var answers = connection.Responses().Where(r => r.CommandToken == "c2").ToList();
            Assert.Equal(new[] { "n2a", "n2b" }, answers.Select(r => r.DispatchNonce).OrderBy(n => n));
        }
        finally
        {
            probe.ReleaseAll();
        }
    }

    [Fact]
    public async Task AThrowingHandlerIsAnsweredFailedWhileAnotherStillRuns()
    {
        var connection = new FakeMqttConnection();
        var probe = new HandlerProbe();
        probe.ThrowFor("b");
        await using var session = new MqttDeviceSession(ConcurrentOptions(2), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, probe.Handler);
        try
        {
            _ = connection.DeliverInOrder("a", "work");
            Assert.True(await CompletesAsync(probe.Entered("a")));
            _ = connection.DeliverInOrder("b", "work");

            Assert.True(await CompletesAsync(connection.WaitForPublishedAsync(1)));
            var response = connection.Responses()[0];
            Assert.Equal("b", response.CommandToken);
            Assert.False(response.Success);
            Assert.StartsWith("the device's command handler threw", response.Error);
            Assert.False(probe.Exited("a").IsCompleted);
        }
        finally
        {
            probe.ReleaseAll();
        }
    }

    [Fact]
    public async Task ALaneSelectorThatThrowsFailsTheCommandWithoutRunningIt()
    {
        var connection = new FakeMqttConnection();
        var probe = new HandlerProbe();
        probe.Release("bad-1");
        await using var session = new MqttDeviceSession(
            ConcurrentOptions(2, c => c.Name == "bad" ? throw new InvalidOperationException("no lane") : null),
            new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, probe.Handler);
        try
        {
            await connection.DeliverInOrder("bad-1", "bad");

            Assert.True(await CompletesAsync(connection.WaitForPublishedAsync(1)));
            Assert.Equal(0, probe.Invocations("bad-1"));
            var response = connection.Responses().Single();
            Assert.Equal("bad-1", response.CommandToken);
            Assert.False(response.Success);
            Assert.Contains("lane selector", response.Error);
        }
        finally
        {
            probe.ReleaseAll();
        }
    }

    [Fact]
    public async Task DisposeCancelsRunningHandlersWaitsForThemAndNeverStartsQueuedOnes()
    {
        const string marker = "dc-concurrent-dispose-marker";
        var unobserved = 0;
        void OnUnobserved(object? sender, UnobservedTaskExceptionEventArgs e)
        {
            // The event is process-wide and test classes run in parallel, so count only ours.
            if (e.Exception.Flatten().InnerExceptions.Any(x => x.Message.Contains(marker)))
            {
                Interlocked.Increment(ref unobserved);
            }
        }

        TaskScheduler.UnobservedTaskException += OnUnobserved;
        var connection = new FakeMqttConnection();
        var probe = new HandlerProbe();
        probe.OnCancel("a", () => new OperationCanceledException());
        probe.OnCancel("b", () => new InvalidOperationException(marker));
        var session = new MqttDeviceSession(ConcurrentOptions(2), new FakeMqttClientFactory(connection));
        try
        {
            await StartAsync(session, connection, probe.Handler);
            _ = connection.DeliverInOrder("a", "work");
            _ = connection.DeliverInOrder("b", "work");
            var c = connection.DeliverInOrder("c", "work");
            Assert.True(await CompletesAsync(c));
            Assert.True(await CompletesAsync(Task.WhenAll(probe.Entered("a"), probe.Entered("b"))));
            Assert.Equal(1, session.QueuedCommands);

            await session.DisposeAsync().AsTask().WaitAsync(Timeout);

            // Disposal waited for both: they have already exited, not merely been told to.
            Assert.True(probe.Exited("a").IsCompleted);
            Assert.True(probe.Exited("b").IsCompleted);
            Assert.False(probe.Entered("c").IsCompleted);
            Assert.Equal(2, probe.Started);

            GC.Collect();
            GC.WaitForPendingFinalizers();
            GC.Collect();
            Assert.Equal(0, Volatile.Read(ref unobserved));
        }
        finally
        {
            TaskScheduler.UnobservedTaskException -= OnUnobserved;
            probe.ReleaseAll();
            await session.DisposeAsync();
        }
    }

    [Fact]
    public async Task DisposeIsBoundedByTheShutdownTimeout()
    {
        var connection = new FakeMqttConnection();
        var probe = new HandlerProbe();
        var options = ConcurrentOptions(2);
        options.CommandShutdownTimeout = TimeSpan.FromMilliseconds(200);
        var session = new MqttDeviceSession(options, new FakeMqttClientFactory(connection));
        try
        {
            // This handler ignores cancellation and stays running.
            await StartAsync(session, connection, probe.Handler);
            _ = connection.DeliverInOrder("stuck", "work");
            Assert.True(await CompletesAsync(probe.Entered("stuck")));

            await session.DisposeAsync().AsTask().WaitAsync(Timeout);

            Assert.Equal(1, session.RunningCommands);
        }
        finally
        {
            // Do not leak a running handler into later tests.
            probe.ReleaseAll();
            await session.DisposeAsync();
        }
    }

    [Fact]
    public async Task ALargestAllowedShutdownTimeoutStillTearsTheConnectionDown()
    {
        var connection = new FakeMqttConnection();
        var options = ConcurrentOptions(2);
        options.CommandShutdownTimeout = MqttSessionOptions.MaxCommandShutdownTimeout;
        var session = new MqttDeviceSession(options, new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (c, ct) => Task.FromResult(CommandOutcome.Succeeded()));

        await session.DisposeAsync().AsTask().WaitAsync(Timeout);

        Assert.False(connection.IsConnected);
        Assert.Equal(MqttSessionState.Stopped, session.State);
    }

    [Fact]
    public async Task ARedeliveryAfterCompletionIsNotRunAgainWhenConcurrent()
    {
        var connection = new FakeMqttConnection();
        var probe = new HandlerProbe();
        probe.Release("cmd-1");
        await using var session = new MqttDeviceSession(ConcurrentOptions(2), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, probe.Handler);

        await connection.DeliverInOrder("cmd-1", "work", "n1");
        Assert.True(await CompletesAsync(connection.WaitForPublishedAsync(1)));
        await connection.DeliverInOrder("cmd-1", "work", "n2");
        Assert.True(await CompletesAsync(connection.WaitForPublishedAsync(2)));

        Assert.Equal(1, probe.Invocations("cmd-1"));
        Assert.Equal(new[] { "n1", "n2" }, connection.Responses().Select(r => r.DispatchNonce).OrderBy(n => n));
    }

    [Fact]
    public async Task ALaneSelectorFailureIsAnsweredUnderTheDeliverysOwnNonce()
    {
        var connection = new FakeMqttConnection();
        await using var session = new MqttDeviceSession(
            ConcurrentOptions(2, c => throw new InvalidOperationException("lane")), new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, (c, ct) => Task.FromResult(CommandOutcome.Succeeded()));

        await connection.DeliverInOrder("bad", "bad", "nz");
        Assert.True(await CompletesAsync(connection.WaitForPublishedAsync(1)));

        Assert.Equal("nz", connection.Responses().Single().DispatchNonce);
    }

    [Fact]
    public async Task ADuplicateOfAQueuedCommandDoesNotHoldDisposeOpen()
    {
        var connection = new FakeMqttConnection();
        var options = ConcurrentOptions(2, _ => "L");
        options.CommandShutdownTimeout = TimeSpan.FromSeconds(3);
        var session = new MqttDeviceSession(options, new FakeMqttClientFactory(connection));
        await StartAsync(session, connection, async (c, ct) =>
        {
            try { await Task.Delay(System.Threading.Timeout.InfiniteTimeSpan, ct); } catch (OperationCanceledException) { }
            return CommandOutcome.Succeeded();
        });
        _ = connection.DeliverInOrder("a", "work");
        _ = connection.DeliverInOrder("b", "work", "n1");
        await connection.DeliverInOrder("b", "work", "n2");

        var sw = System.Diagnostics.Stopwatch.StartNew();
        await session.DisposeAsync();

        Assert.True(sw.Elapsed < TimeSpan.FromSeconds(2), $"dispose took {sw.Elapsed}");
    }

    [Fact]
    public void ConcurrencySettingsOutOfRangeAreRefused()
    {
        var options = Options();
        Assert.Throws<ArgumentOutOfRangeException>(() => options.MaxConcurrentCommands = 0);
        Assert.Throws<ArgumentOutOfRangeException>(() => options.MaxConcurrentCommands = -1);
        Assert.Throws<ArgumentOutOfRangeException>(() => options.CommandShutdownTimeout = TimeSpan.FromSeconds(-1));
        Assert.Throws<ArgumentOutOfRangeException>(() => options.CommandShutdownTimeout = TimeSpan.FromDays(60));
        Assert.Equal(1, options.MaxConcurrentCommands);
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
    // Hands out one working connection, then connections that always FAIL to connect — and, like
    // the real library, raise ConnectionLost when they do.
    private sealed class StormFactory : IMqttClientFactory
    {
        private readonly FakeMqttConnection _first;
        private int _served;
        private int _failed;
        private int _disposedFailures;

        public StormFactory(FakeMqttConnection first) => _first = first;

        public int FailedAttempts => Volatile.Read(ref _failed);

        public int DisposedFailures => Volatile.Read(ref _disposedFailures);

        public IMqttConnection Create()
        {
            if (Interlocked.Exchange(ref _served, 1) == 0)
            {
                return _first;
            }

            Interlocked.Increment(ref _failed);
            return new FailingConnection(() => Interlocked.Increment(ref _disposedFailures));
        }
    }

    private sealed class FailingConnection : IMqttConnection
    {
        private readonly Action _onDispose;

        public FailingConnection(Action onDispose) => _onDispose = onDispose;

        public bool IsConnected => false;

        public event Func<MqttInbound, Task>? MessageReceived;

        public event Action<Exception?>? ConnectionLost;

        public Task ConnectAsync(MqttConnectOptions options, CancellationToken cancellationToken)
        {
            // The real library raises its disconnected event even for a connection that never
            // came up. Reproducing that is the whole point of this fake.
            ConnectionLost?.Invoke(new Exception("connect failed"));
            _ = MessageReceived;
            throw new MqttConnectionException("connect failed");
        }

        public Task SubscribeAsync(string topicFilter, MqttQos qos, CancellationToken cancellationToken) =>
            Task.CompletedTask;

        public Task PublishAsync(string topic, byte[] payload, MqttQos qos, CancellationToken cancellationToken) =>
            Task.CompletedTask;

        public Task DisconnectAsync(TimeSpan quiesce, CancellationToken cancellationToken) => Task.CompletedTask;

        public ValueTask DisposeAsync()
        {
            _onDispose();
            return default;
        }
    }

    // Per-command gates for the concurrency tests: records entry order and peak concurrency, and
    // holds each handler until the test releases it.
    private sealed class HandlerProbe
    {
        private readonly object _lock = new();
        private readonly Dictionary<string, TaskCompletionSource<bool>> _entered = new();
        private readonly Dictionary<string, TaskCompletionSource<bool>> _exited = new();
        private readonly Dictionary<string, TaskCompletionSource<bool>> _release = new();
        private readonly Dictionary<string, int> _invocations = new();
        private readonly HashSet<string> _sync = new();
        private readonly HashSet<string> _throws = new();
        private readonly Dictionary<string, Func<Exception>> _onCancel = new();
        private readonly List<string> _order = new();
        private int _running;
        private int _max;

        public int MaxRunning { get { lock (_lock) { return _max; } } }

        public int Started { get { lock (_lock) { return _order.Count; } } }

        private static TaskCompletionSource<bool> Gate(Dictionary<string, TaskCompletionSource<bool>> map, string key)
        {
            if (!map.TryGetValue(key, out var gate))
            {
                gate = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
                map[key] = gate;
            }

            return gate;
        }

        public Task Entered(string token) { lock (_lock) { return Gate(_entered, token).Task; } }

        public Task Exited(string token) { lock (_lock) { return Gate(_exited, token).Task; } }

        public void Release(string token) { lock (_lock) { Gate(_release, token).TrySetResult(true); } }

        public void BlockSynchronously(string token) { lock (_lock) { _sync.Add(token); } }

        public void ThrowFor(string token) { lock (_lock) { _throws.Add(token); } }

        // The handler waits for cancellation, then fails the way the factory says.
        public void OnCancel(string token, Func<Exception> exception) { lock (_lock) { _onCancel[token] = exception; } }

        public int Invocations(string token) { lock (_lock) { return _invocations.GetValueOrDefault(token); } }

        public string[] EntryOrder() { lock (_lock) { return _order.ToArray(); } }

        public void ReleaseAll()
        {
            lock (_lock)
            {
                foreach (var token in _entered.Keys.Concat(_order).ToList())
                {
                    Gate(_release, token).TrySetResult(true);
                }
            }
        }

        public async Task<CommandOutcome> Handler(DeviceCommand command, CancellationToken cancellationToken)
        {
            Task releaseTask;
            bool sync, throws;
            Func<Exception>? onCancel;
            lock (_lock)
            {
                _invocations[command.Token] = _invocations.GetValueOrDefault(command.Token) + 1;
                _order.Add(command.Token);
                _running++;
                _max = Math.Max(_max, _running);
                releaseTask = Gate(_release, command.Token).Task;
                sync = _sync.Contains(command.Token);
                throws = _throws.Contains(command.Token);
                onCancel = _onCancel.GetValueOrDefault(command.Token);
                Gate(_entered, command.Token).TrySetResult(true);
            }

            try
            {
                if (throws)
                {
                    throw new InvalidOperationException("handler failed on purpose");
                }

                if (onCancel != null)
                {
                    try
                    {
                        await Task.Delay(System.Threading.Timeout.InfiniteTimeSpan, cancellationToken);
                    }
                    catch (OperationCanceledException)
                    {
                        throw onCancel();
                    }
                }

                if (sync)
                {
                    releaseTask.Wait();
                }
                else
                {
                    await releaseTask;
                }

                return CommandOutcome.Succeeded();
            }
            finally
            {
                lock (_lock)
                {
                    _running--;
                    Gate(_exited, command.Token).TrySetResult(true);
                }
            }
        }
    }

    private sealed class FakeMqttConnection : IMqttConnection
    {
        private readonly TaskCompletionSource<bool> _subackGate =
            new(TaskCreationOptions.RunContinuationsAsynchronously);

        public TaskCompletionSource<bool> SubscribeCalled { get; } =
            new(TaskCreationOptions.RunContinuationsAsynchronously);

        public bool RefuseSubscribe { get; set; }

        public MqttConnectOptions? Options { get; private set; }

        public string? SubscribedFilter { get; private set; }

        // Recorded because discarding it is exactly how the command subscription's QoS came to be
        // mutable to 0 with every gate green.
        public MqttQos? SubscribedQos { get; private set; }

        private readonly object _publishedLock = new();
        private readonly List<(string Topic, byte[] Payload, MqttQos Qos)> _published = new();
        private readonly List<(int Count, TaskCompletionSource<bool> Signal)> _publishWaiters = new();
        private readonly object _orderLock = new();
        private Task _orderTail = Task.CompletedTask;

        // A snapshot: handlers publish from worker threads in the concurrency tests.
        public List<(string Topic, byte[] Payload, MqttQos Qos)> Published
        {
            get { lock (_publishedLock) { return _published.ToList(); } }
        }

        public List<CommandResponseEnvelope> Responses() =>
            Published.Select(p => JsonSerializer.Deserialize<CommandResponseEnvelope>(p.Payload)!).ToList();

        // Completes once at least `count` messages have been published. Signalled by the publish
        // itself, never polled.
        public Task WaitForPublishedAsync(int count)
        {
            lock (_publishedLock)
            {
                if (_published.Count >= count)
                {
                    return Task.CompletedTask;
                }

                var signal = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
                _publishWaiters.Add((count, signal));
                return signal.Task;
            }
        }

        // Models the shipped transport: ONE pump that calls the receive callback for message k+1
        // only after the callback for message k has returned. The returned Task completes when
        // the callback returns, which is the point at which the transport acknowledges the message.
        public Task DeliverInOrder(string token, string name, string dispatchNonce = "nonce-1")
        {
            lock (_orderLock)
            {
                var previous = _orderTail;
                var delivery = Task.Run(async () =>
                {
                    try
                    {
                        await previous;
                    }
                    catch (Exception)
                    {
                        // An earlier delivery's failure is its own caller's to see.
                    }

                    await DeliverCommandAsync(token, name, null, dispatchNonce);
                });
                _orderTail = delivery;
                return delivery;
            }
        }

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
            SubscribedQos = qos;
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
            List<TaskCompletionSource<bool>> ready = new();
            lock (_publishedLock)
            {
                _published.Add((topic, payload, qos));
                for (var i = _publishWaiters.Count - 1; i >= 0; i--)
                {
                    if (_published.Count >= _publishWaiters[i].Count)
                    {
                        ready.Add(_publishWaiters[i].Signal);
                        _publishWaiters.RemoveAt(i);
                    }
                }
            }

            foreach (var signal in ready)
            {
                signal.TrySetResult(true);
            }

            return Task.CompletedTask;
        }

        public Task DisconnectAsync(TimeSpan quiesce, CancellationToken cancellationToken)
        {
            IsConnected = false;
            return Task.CompletedTask;
        }

        public ValueTask DisposeAsync() => default;

        // dispatchNonce names the dispatch this delivery is. It defaults to a value rather than
        // to nothing on purpose: the platform refuses an answer that names no dispatch, so every
        // test here that is NOT about a missing nonce must deliver one, or it would be asserting
        // against a round trip the platform would reject.
        public Task DeliverCommandAsync(string token, string name, string? payloadJson = null,
            string dispatchNonce = "nonce-1")
        {
            var payload = payloadJson == null ? "null" : payloadJson;
            var json = $"{{\"token\":\"{token}\",\"deviceToken\":\"sensor-001\",\"name\":\"{name}\"," +
                $"\"payload\":{payload},\"dispatchNonce\":\"{dispatchNonce}\"}}";
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
