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
/// PUBACK, session persistence across a reconnect, and redelivery.
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
        var responses = new List<byte[]>();
        var answered = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        peer.MessageReceived += inbound =>
        {
            responses.Add(inbound.Payload);
            answered.TrySetResult(true);
            return Task.CompletedTask;
        };

        await peer.ConnectAsync(DeviceOptions("peer-001", "peer"), cts.Token);
        await peer.SubscribeAsync(
            DevicePlane.CommandResponsesTopic("inst", "acme"), MqttQos.AtLeastOnce, cts.Token);

        await peer.PublishAsync(
            DevicePlane.CommandsTopic("inst", "acme", "sensor-001"),
            Encoding.UTF8.GetBytes("{\"token\":\"cmd-1\",\"deviceToken\":\"sensor-001\",\"name\":\"goRefuel\"}"),
            MqttQos.AtLeastOnce,
            cts.Token);

        var command = await received.Task.WaitAsync(Timeout);
        Assert.Equal("cmd-1", command.Token);
        Assert.Equal("goRefuel", command.Name);

        await answered.Task.WaitAsync(Timeout);
        var body = Encoding.UTF8.GetString(responses[0]);
        Assert.Contains("\"commandToken\":\"cmd-1\"", body);
        Assert.Contains("\"success\":true", body);
    }

    // cleanSession=false means the broker holds the subscription and any undelivered QoS-1
    // commands while the device is away — which is what stops a command being lost across a
    // reconnect. Proving it needs a real broker: no fake models session persistence.
    [SkippableFact]
    public async Task ThePersistentSessionHoldsACommandSentWhileTheDeviceWasAway()
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

    private static MqttConnectOptions DeviceOptions(string deviceToken, string? discriminator = null) =>
        new(new Uri(BrokerUri!), DevicePlane.DeviceClientId("inst", "acme", deviceToken, discriminator))
        {
            Username = DevicePlane.ConnectUsername("acme", "cred-1"),
            Password = string.Empty,
            CleanSession = false,
        };
}
