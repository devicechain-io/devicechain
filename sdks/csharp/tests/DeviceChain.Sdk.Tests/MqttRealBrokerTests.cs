// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sdk.Transport;
using Xunit;

namespace DeviceChain.Sdk.Tests;

/// <summary>
/// Drives the MQTT client against a REAL nats-server MQTT gateway — the broker the platform
/// actually ships — rather than a scripted socket.
/// </summary>
/// <remarks>
/// <para>
/// These are skipped unless <c>DC_MQTT_BROKER</c> names a running broker, so a local
/// <c>dotnet test</c> still passes with nothing installed. CI sets it; see the <c>csharp</c> job,
/// which builds nats-server from the version the backend already pins.
/// </para>
/// <para>
/// 🔴 WHAT THIS RIG DOES NOT COVER, stated so a green run is not over-read: it runs the broker
/// AUTHLESS, exactly as the Go rigs do. The platform's admission rules — the auth callout parsing
/// the username, the client-id refusal, the JWT minted to confine SUB to one device's subject —
/// live in device-management against a database and are not exercised here. A subscription
/// REFUSAL therefore cannot be produced by this rig at all, which is why the scripted-broker test
/// exists alongside it. What this covers is real protocol behaviour: 3.1.1 negotiation, QoS-1
/// PUBACK, session persistence across a reconnect, redelivery, and a whole scene's worth of
/// concurrent sessions sharing one broker.
/// </para>
/// <para>
/// 🔴 THE BROKER MUST BE CONFIGURED WITH <c>mqtt { ack_wait: "1s" }</c>. nats-server's default is
/// 30 seconds, and <see cref="ARealRedeliveryIsAnsweredAgainWithoutRerunningTheHandler"/> waits for
/// the broker to actually redeliver an un-PUBACKed QoS-1 command — the only way to observe
/// redelivery without faking it. On the default the redelivery never arrives inside the test's
/// window and that test FAILS rather than skipping, which is deliberate: a redelivery test that
/// quietly passes because no redelivery happened is the "no findings ≡ never ran" trap in
/// miniature.
/// </para>
/// </remarks>
public class MqttRealBrokerTests
{
    private static readonly TimeSpan Timeout = TimeSpan.FromSeconds(30);

    private static string? BrokerUri => Environment.GetEnvironmentVariable("DC_MQTT_BROKER");

    private static bool Available => !string.IsNullOrEmpty(BrokerUri);

    // A device connects with the client id the platform dictates, subscribes to its own command
    // topic, and the broker GRANTS it. This is the positive control for the refusal test: if a
    // real broker's grant did not read as a grant, the guard would be refusing valid work.
    [SkippableFact]
    public async Task AConfirmedSubscribeSucceedsAgainstTheRealGateway()
    {
        Skip.IfNot(Available, "DC_MQTT_BROKER is not set");

        await using var connection = new MqttNetConnection();
        using var cts = new CancellationTokenSource(Timeout);

        await connection.ConnectAsync(DeviceOptions("sensor-001"), cts.Token);
        await connection.SubscribeAsync(
            DevicePlane.CommandsTopic("inst", "acme", "sensor-001"), MqttQos.AtLeastOnce, cts.Token);

        Assert.True(connection.IsConnected);
    }

    // A QoS-1 publish completes only once the broker has PUBACKed — the point at which the broker
    // has taken ownership of the message.
    [SkippableFact]
    public async Task AQos1PublishIsAcknowledgedByTheRealGateway()
    {
        Skip.IfNot(Available, "DC_MQTT_BROKER is not set");

        await using var connection = new MqttNetConnection();
        using var cts = new CancellationTokenSource(Timeout);

        await connection.ConnectAsync(DeviceOptions("sensor-001"), cts.Token);
        await connection.PublishAsync(
            DevicePlane.EventsTopic("inst", "acme", "sensor-001"),
            Encoding.UTF8.GetBytes("{\"device\":\"sensor-001\"}"),
            MqttQos.AtLeastOnce,
            cts.Token);
    }

    // A command published to the device's topic reaches the session's handler and is answered on
    // the tenant-scoped responses topic — the whole two-way contract over a real broker.
    [SkippableFact]
    public async Task ACommandRoundTripsThroughTheSessionOverTheRealGateway()
    {
        Skip.IfNot(Available, "DC_MQTT_BROKER is not set");

        // 🔴 RUN-UNIQUE, AND THE RESPONSE IS MATCHED ON IT RATHER THAN TAKEN AS "THE FIRST ONE".
        // The responses topic is TENANT-scoped, so every device in the tenant answers on it, and
        // the peer's session is persistent — so on a broker that outlives the run it reconnects to
        // a backlog of other tests' answers and the first message it sees is not its own. Measured,
        // not hypothesised: asserting on responses[0] here failed on the second consecutive run
        // against one broker, reading another test's answer.
        var commandToken = "cmd-roundtrip-" + RunId();
        var received = new TaskCompletionSource<DeviceCommand>(TaskCreationOptions.RunContinuationsAsynchronously);
        var options = new MqttSessionOptions(new Uri(BrokerUri!), "inst", "acme", "sensor-001", "cred-1");

        await using var session = new MqttDeviceSession(options);
        await session.StartAsync((command, _) =>
        {
            received.TrySetResult(command);
            return Task.FromResult(CommandOutcome.Succeeded("moving"));
        }, new CancellationTokenSource(Timeout).Token);

        Assert.Equal(MqttSessionState.Ready, session.State);

        // A second connection stands in for command-delivery, and for the platform operator
        // watching the responses topic.
        await using var peer = new MqttNetConnection();
        using var cts = new CancellationTokenSource(Timeout);
        var answered = new TaskCompletionSource<string>(TaskCreationOptions.RunContinuationsAsynchronously);
        peer.MessageReceived += inbound =>
        {
            var payload = Encoding.UTF8.GetString(inbound.Payload);
            if (payload.Contains($"\"commandToken\":\"{commandToken}\"", StringComparison.Ordinal))
            {
                answered.TrySetResult(payload);
            }

            return Task.CompletedTask;
        };

        await peer.ConnectAsync(DeviceOptions("peer-001", "peer"), cts.Token);
        await peer.SubscribeAsync(
            DevicePlane.CommandResponsesTopic("inst", "acme"), MqttQos.AtLeastOnce, cts.Token);

        await peer.PublishAsync(
            DevicePlane.CommandsTopic("inst", "acme", "sensor-001"),
            Encoding.UTF8.GetBytes(
                $"{{\"token\":\"{commandToken}\",\"deviceToken\":\"sensor-001\",\"name\":\"goRefuel\"}}"),
            MqttQos.AtLeastOnce,
            cts.Token);

        var command = await received.Task.WaitAsync(Timeout);
        Assert.Equal(commandToken, command.Token);
        Assert.Equal("goRefuel", command.Name);

        var body = await answered.Task.WaitAsync(Timeout);
        Assert.Contains("\"success\":true", body);
        Assert.Contains("\"payload\":\"moving\"", body);
    }

    // cleanSession=false means the broker holds the subscription and, for a message that reached
    // it as an MQTT PUBLISH from another MQTT client, the undelivered QoS-1 message too. Proving
    // it needs a real broker: no fake models session persistence.
    //
    // 🔴🔴 READ THE PUBLISHER BEFORE READING THE NAME. This test publishes from `peer`, a second
    // MQTT client, at QoS 1 — and that is the ONE path that puts a message in the broker's QoS-1
    // store. A DeviceChain command does NOT take that path: command-delivery publishes it over
    // NATS, so it never enters that store, the durable consumer behind this persistent
    // subscription never sees it, and it is delivered only live, as QoS 0. A command issued while
    // the device is away is therefore dropped by the broker — the opposite of what this test's
    // former name ("ThePersistentSessionHoldsACommandSentWhileTheDeviceWasAway") asserted.
    //
    // So this proves the broker's session persistence works. It does NOT prove anything about
    // command delivery, and it must not be cited as if it did. Covering the real path needs a
    // publisher on the NATS side, which is out of reach from an SDK test — the gap is real and is
    // named here rather than papered over.
    [SkippableFact]
    public async Task TheBrokerHoldsAQoS1MessagePublishedByAnotherMqttClientWhileTheDeviceWasAway()
    {
        Skip.IfNot(Available, "DC_MQTT_BROKER is not set");

        using var cts = new CancellationTokenSource(Timeout);
        var commandsTopic = DevicePlane.CommandsTopic("inst", "acme", "sensor-002");

        // First connection: establish the persistent session and its subscription, then leave.
        var first = new MqttNetConnection();
        await first.ConnectAsync(DeviceOptions("sensor-002"), cts.Token);
        await first.SubscribeAsync(commandsTopic, MqttQos.AtLeastOnce, cts.Token);
        await first.DisconnectAsync(TimeSpan.FromSeconds(2), cts.Token);
        await first.DisposeAsync();

        // A command arrives while nothing is connected.
        await using var peer = new MqttNetConnection();
        await peer.ConnectAsync(DeviceOptions("peer-002", "peer"), cts.Token);
        await peer.PublishAsync(
            commandsTopic, Encoding.UTF8.GetBytes("{\"token\":\"cmd-away\"}"), MqttQos.AtLeastOnce, cts.Token);

        // Reconnecting with the same client id and cleanSession=false must deliver it.
        var delivered = new TaskCompletionSource<string>(TaskCreationOptions.RunContinuationsAsynchronously);
        await using var second = new MqttNetConnection();
        second.MessageReceived += inbound =>
        {
            delivered.TrySetResult(Encoding.UTF8.GetString(inbound.Payload));
            return Task.CompletedTask;
        };
        await second.ConnectAsync(DeviceOptions("sensor-002"), cts.Token);

        var body = await delivered.Task.WaitAsync(Timeout);
        Assert.Contains("cmd-away", body);
    }

    // 🔴 THE REDELIVERY HALF OF AT-LEAST-ONCE, DRIVEN BY THE BROKER RATHER THAN BY A FAKE. The unit
    // tests hand the session a second copy of a command and check it is not run twice; that proves
    // the dedupe but assumes the broker ever produces a duplicate in the first place. This proves
    // the assumption: nothing here re-publishes anything, so every delivery after the first is
    // nats-server redelivering a QoS-1 command it has not been PUBACKed for.
    //
    // 🔑 THE HANDLER IS WHAT HOLDS THE PUBACK BACK, and that is not a contrivance — it is the exact
    // production shape. MQTTnet PUBACKs once the message handler returns, so a machine that takes
    // longer than ack_wait to carry out a command IS an un-acked QoS-1 message, and the broker
    // redelivers underneath it. A dozer told "go refuel" must not drive twice because refuelling
    // took two seconds.
    //
    // 🔴 WHICH DEDUPE PATH THIS ACTUALLY REACHES, stated because the obvious reading is wrong.
    // MQTTnet dispatches inbound PUBLISHes ONE AT A TIME — measured, not assumed: with a handler
    // blocked, a second command published 1.5 seconds earlier was not dispatched until the first
    // handler returned. So over this transport a redelivery can never land while the handler is
    // still running; it is dispatched afterwards and answered from the COMPLETED-command history.
    // The still-running window is real for a transport that dispatches concurrently, and it is the
    // fake-backed unit test that covers it — this one cannot, and must not be read as if it did.
    [SkippableFact]
    public async Task ARealRedeliveryIsAnsweredAgainWithoutRerunningTheHandler()
    {
        Skip.IfNot(Available, "DC_MQTT_BROKER is not set");

        // 🔑 RUN-UNIQUE, BECAUSE THE ASSERTION IS A COUNT AND A DEVELOPER'S BROKER OUTLIVES THE RUN.
        // Sessions here are persistent by design, so an answer left on the responses topic by an
        // earlier run would count towards "it was answered more than once" and let this test pass
        // on a broker that had stopped redelivering entirely.
        var commandToken = "cmd-redelivered-" + RunId();
        var invocations = 0;
        var entered = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        var release = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);

        var options = new MqttSessionOptions(new Uri(BrokerUri!), "inst", "acme", "sensor-003", "cred-1");
        await using var session = new MqttDeviceSession(options);
        await session.StartAsync(async (_, _) =>
        {
            Interlocked.Increment(ref invocations);
            entered.TrySetResult(true);
            await release.Task;
            return CommandOutcome.Succeeded("refuelled");
        }, new CancellationTokenSource(Timeout).Token);

        // The peer both issues the command and watches the responses topic, standing in for
        // command-delivery on both legs.
        var responses = new List<string>();
        var responsesLock = new object();
        var answeredTwice = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);

        await using var peer = new MqttNetConnection();
        using var cts = new CancellationTokenSource(Timeout);
        peer.MessageReceived += inbound =>
        {
            var body = Encoding.UTF8.GetString(inbound.Payload);
            if (!body.Contains($"\"commandToken\":\"{commandToken}\"", StringComparison.Ordinal))
            {
                return Task.CompletedTask;
            }

            lock (responsesLock)
            {
                responses.Add(body);
                if (responses.Count >= 2)
                {
                    answeredTwice.TrySetResult(true);
                }
            }

            return Task.CompletedTask;
        };

        await peer.ConnectAsync(DeviceOptions("peer-003", "peer"), cts.Token);
        await peer.SubscribeAsync(
            DevicePlane.CommandResponsesTopic("inst", "acme"), MqttQos.AtLeastOnce, cts.Token);

        // ONE publish. Everything the session sees after this is the broker's own doing.
        await peer.PublishAsync(
            DevicePlane.CommandsTopic("inst", "acme", "sensor-003"),
            Encoding.UTF8.GetBytes(
                $"{{\"token\":\"{commandToken}\",\"deviceToken\":\"sensor-003\",\"name\":\"goRefuel\"}}"),
            MqttQos.AtLeastOnce,
            cts.Token);

        await entered.Task.WaitAsync(Timeout);

        // Long enough to span two 1-second ack_wait windows, so a single slow window still leaves a
        // redelivery inside it. Nothing is asserted until after the wait, so a broker that is
        // faster than expected costs only the wait.
        await Task.Delay(TimeSpan.FromMilliseconds(2200));

        release.TrySetResult(true);

        // 🔑 MORE THAN ONE ANSWER IS THE PROOF A REDELIVERY HAPPENED AT ALL, and it has to be
        // checked before the count below means anything: every delivery is answered, so a single
        // answer would say the broker only ever delivered once and "the handler ran once" would be
        // trivially true. If this times out the broker never redelivered — almost certainly
        // ack_wait left at its 30-second default — and the test is reporting a broken rig rather
        // than passing on one.
        await answeredTwice.Task.WaitAsync(Timeout);

        // ...and THEN: several deliveries, one execution.
        Assert.Equal(1, Volatile.Read(ref invocations));

        List<string> seen;
        lock (responsesLock)
        {
            seen = new List<string>(responses);
        }

        // 🔑 AND EACH ANSWER REPEATS THE ORIGINAL OUTCOME. Answering a redelivery with a fabricated
        // success would satisfy a count-only assertion while driving a command the machine had
        // refused all the way to SUCCESSFUL.
        Assert.All(seen, body =>
        {
            Assert.Contains("\"success\":true", body);
            Assert.Contains("\"payload\":\"refuelled\"", body);
        });
    }

    // 🔴 A WHOLE SCENE AT ONCE. One session is one device — MQTT 3.1.1 has no shared subscriptions
    // and the minted grant covers exactly one device's command subject — so a scene of 18 machines
    // is 18 broker connections, and that is a number to size for rather than discover on site.
    // Every test above opens one or two sessions, which says nothing about eighteen sharing a
    // broker.
    //
    // 🔑 THE PROPERTY THAT MATTERS IS ISOLATION, NOT THROUGHPUT. With one session there is nothing
    // a command could be mis-delivered TO, so a bug that collapses the per-device subscription —
    // a wildcard, a topic that drops the device token, a client id that is not per-device —
    // cannot show up until several sessions share the broker. Here it does: each session records
    // EVERYTHING it receives, and a session that saw a neighbour's command fails.
    [SkippableFact]
    public async Task EighteenConcurrentSessionsEachReceiveOnlyTheirOwnCommands()
    {
        Skip.IfNot(Available, "DC_MQTT_BROKER is not set");

        const int machines = 18;

        // The DEVICE tokens are stable — they key persistent broker sessions, and minting fresh
        // ones every run would leave a pile of them behind on a developer's long-lived broker. The
        // COMMAND tokens are run-unique, so anything left over from an earlier run is recognisable
        // as such instead of being mistaken for this run's traffic.
        var run = RunId();
        var tokens = new string[machines];
        var commands = new string[machines];
        for (var i = 0; i < machines; i++)
        {
            tokens[i] = $"sitepulse-machine-{i + 1:D2}";
            commands[i] = $"cmd-{run}-{tokens[i]}";
        }

        var sessions = new MqttDeviceSession[machines];
        var received = new List<string>[machines];
        var receivedLocks = new object[machines];
        var gotOwn = new TaskCompletionSource<bool>[machines];

        try
        {
            using var cts = new CancellationTokenSource(Timeout);
            var starts = new Task[machines];

            for (var i = 0; i < machines; i++)
            {
                var index = i;
                received[index] = new List<string>();
                receivedLocks[index] = new object();
                gotOwn[index] = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);

                sessions[index] = new MqttDeviceSession(
                    new MqttSessionOptions(new Uri(BrokerUri!), "inst", "acme", tokens[index], "cred-1"));

                // Started CONCURRENTLY rather than one after another. Eighteen sequential
                // connect-then-confirmed-subscribe round trips would be a different, gentler load
                // than a scene coming up, which is exactly what a device cohort does.
                starts[index] = sessions[index].StartAsync((command, _) =>
                {
                    lock (receivedLocks[index])
                    {
                        received[index].Add(command.Token);
                    }

                    if (command.Token == commands[index])
                    {
                        gotOwn[index].TrySetResult(true);
                    }

                    return Task.FromResult(CommandOutcome.Succeeded(tokens[index]));
                }, cts.Token);
            }

            await Task.WhenAll(starts).WaitAsync(Timeout);

            // Every one of them must hold a CONFIRMED grant. A session that came up merely
            // connected is deaf, and eighteen is where a broker first has a reason to refuse one.
            for (var i = 0; i < machines; i++)
            {
                Assert.Equal(MqttSessionState.Ready, sessions[i].State);
            }

            // The responses topic is tenant-scoped, so this one subscription sees all 18 answers —
            // which is what the platform's command-delivery sees too.
            var answered = new HashSet<string>(StringComparer.Ordinal);
            var answeredLock = new object();
            var allAnswered = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);

            await using var peer = new MqttNetConnection();
            peer.MessageReceived += inbound =>
            {
                var body = Encoding.UTF8.GetString(inbound.Payload);
                lock (answeredLock)
                {
                    foreach (var command in commands)
                    {
                        if (body.Contains($"\"commandToken\":\"{command}\"", StringComparison.Ordinal))
                        {
                            answered.Add(command);
                        }
                    }

                    if (answered.Count == machines)
                    {
                        allAnswered.TrySetResult(true);
                    }
                }

                return Task.CompletedTask;
            };

            await peer.ConnectAsync(DeviceOptions("peer-scene", "peer"), cts.Token);
            await peer.SubscribeAsync(
                DevicePlane.CommandResponsesTopic("inst", "acme"), MqttQos.AtLeastOnce, cts.Token);

            // One command per machine, each token naming the machine it belongs to so a
            // mis-delivery is identifiable rather than merely a count that does not add up.
            var publishes = new Task[machines];
            for (var i = 0; i < machines; i++)
            {
                publishes[i] = peer.PublishAsync(
                    DevicePlane.CommandsTopic("inst", "acme", tokens[i]),
                    Encoding.UTF8.GetBytes(
                        $"{{\"token\":\"{commands[i]}\",\"deviceToken\":\"{tokens[i]}\",\"name\":\"goRefuel\"}}"),
                    MqttQos.AtLeastOnce,
                    cts.Token);
            }

            await Task.WhenAll(publishes).WaitAsync(Timeout);

            for (var i = 0; i < machines; i++)
            {
                await gotOwn[i].Task.WaitAsync(Timeout);
            }

            await allAnswered.Task.WaitAsync(Timeout);

            // 🔑 THE SETTLE IS WHAT MAKES THE ISOLATION ASSERTION MEAN ANYTHING. Above, every
            // session has been proved to receive its OWN command; a stray delivery of a
            // NEIGHBOUR's would be in flight at roughly the same moment, so asserting the absence
            // immediately would race the very frame it is looking for.
            await Task.Delay(TimeSpan.FromMilliseconds(500));

            for (var i = 0; i < machines; i++)
            {
                List<string> seen;
                lock (receivedLocks[i])
                {
                    seen = new List<string>(received[i]);
                }

                // Filtered to THIS run: a leftover from an earlier run says nothing about whether
                // these eighteen sessions are isolated from each other, whereas a cross-delivery
                // that happened here carries this run's id and is kept.
                Assert.All(
                    seen.FindAll(token => token.StartsWith($"cmd-{run}-", StringComparison.Ordinal)),
                    token => Assert.Equal(commands[i], token));
            }
        }
        finally
        {
            // Eighteen sockets and eighteen persistent broker sessions: leaving any of them behind
            // would bleed into whatever runs next against the same broker.
            foreach (var session in sessions)
            {
                if (session != null)
                {
                    await session.DisposeAsync();
                }
            }
        }
    }

    // A short id that is distinct per test run, so traffic this run produced can be told from
    // traffic still sitting on a broker that outlived an earlier one.
    private static string RunId() => Guid.NewGuid().ToString("N").Substring(0, 8);

    private static MqttConnectOptions DeviceOptions(string deviceToken, string? discriminator = null) =>
        new(new Uri(BrokerUri!), DevicePlane.DeviceClientId("inst", "acme", deviceToken, discriminator))
        {
            Username = DevicePlane.ConnectUsername("acme", "cred-1"),
            Password = string.Empty,
            CleanSession = false,
        };
}
