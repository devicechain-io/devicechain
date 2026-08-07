// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Transport;
using Xunit;

namespace DeviceChain.Sdk.Tests;

// Drives the REAL MQTTnet-backed connection against a scripted wire, because the property that
// matters here lives in the adapter itself: whether it READS the broker's SUBACK answer rather
// than merely waiting for one. A fake IMqttConnection would replace exactly the code under test.
public class MqttNetConnectionTests
{
    private static readonly TimeSpan Timeout = TimeSpan.FromSeconds(10);

    private static MqttConnectOptions OptionsFor(ScriptedMqttBroker broker) =>
        new(broker.BrokerUri, "inst:acme:sensor-001")
        {
            Username = "acme:cred-1",
            Password = string.Empty,
            CleanSession = false,
        };

    // 🔴 THE #668 TEST. MQTTnet's SubscribeAsync completes SUCCESSFULLY on a refusal — it exposes
    // the code on the result and throws nothing — so a wrapper that does not read the code
    // produces a client that connects, reports healthy, and receives nothing forever. Mutating
    // the ResultCode check out of MqttNetConnection.SubscribeAsync must fail this test.
    [Fact]
    public async Task SubscribeThrowsWhenTheBrokerRefusesTheFilter()
    {
        using var broker = new ScriptedMqttBroker(subackCodes: new byte[] { 0x80 });
        await using var connection = new MqttNetConnection();
        using var cts = new CancellationTokenSource(Timeout);

        await connection.ConnectAsync(OptionsFor(broker), cts.Token);

        var refused = await Assert.ThrowsAsync<MqttSubscribeRefusedException>(
            () => connection.SubscribeAsync("inst/acme/device-commands/sensor-001", MqttQos.AtLeastOnce, cts.Token));

        Assert.Equal("inst/acme/device-commands/sensor-001", refused.TopicFilter);
        // The refusal must be distinguishable from a transport failure: retrying a refusal
        // forever is the wrong response, and callers switch on the type to tell them apart.
        Assert.IsAssignableFrom<MqttConnectionException>(refused);
    }

    // The counterweight. A guard that rejects everything would pass the test above while making
    // the client useless, so a granted subscription must still succeed.
    [Fact]
    public async Task SubscribeSucceedsWhenTheBrokerGrantsTheFilter()
    {
        using var broker = new ScriptedMqttBroker(subackCodes: new byte[] { 0x01 });
        await using var connection = new MqttNetConnection();
        using var cts = new CancellationTokenSource(Timeout);

        await connection.ConnectAsync(OptionsFor(broker), cts.Token);
        await connection.SubscribeAsync("inst/acme/device-commands/sensor-001", MqttQos.AtLeastOnce, cts.Token);

        Assert.Contains("inst/acme/device-commands/sensor-001", broker.SubscribedFilters);
    }

    // A broker may legally grant a LOWER QoS than was asked for. Treating that as a failure would
    // refuse a working subscription, so the boundary is tested rather than assumed: 0x00 is a
    // grant, not a refusal, even though it is not what was requested.
    [Fact]
    public async Task SubscribeTreatsAQosDowngradeAsAGrant()
    {
        using var broker = new ScriptedMqttBroker(subackCodes: new byte[] { 0x00 });
        await using var connection = new MqttNetConnection();
        using var cts = new CancellationTokenSource(Timeout);

        await connection.ConnectAsync(OptionsFor(broker), cts.Token);
        await connection.SubscribeAsync("inst/acme/device-commands/sensor-001", MqttQos.AtLeastOnce, cts.Token);
    }

    // A broker that accepts the connection and then never answers must fail the caller rather than
    // hang it forever — the other half of the confirmed-subscribe contract.
    [Fact]
    public async Task SubscribeIsBoundedWhenTheBrokerNeverAnswers()
    {
        using var broker = new SilentSubackBroker();
        await using var connection = new MqttNetConnection();
        using var connectCts = new CancellationTokenSource(Timeout);

        await connection.ConnectAsync(
            new MqttConnectOptions(broker.BrokerUri, "inst:acme:sensor-001"), connectCts.Token);

        using var subscribeCts = new CancellationTokenSource(TimeSpan.FromMilliseconds(750));
        await Assert.ThrowsAnyAsync<OperationCanceledException>(
            () => connection.SubscribeAsync("inst/acme/device-commands/sensor-001", MqttQos.AtLeastOnce, subscribeCts.Token));
    }

    // The CONNECT fields the auth callout parses must reach the wire exactly as given — in
    // particular an EMPTY password, which is not a missing value but the thing that selects
    // access-token credential mode.
    [Fact]
    public async Task ConnectPresentsTheClientIdAndCredentialsVerbatim()
    {
        using var broker = new ScriptedMqttBroker();
        await using var connection = new MqttNetConnection();
        using var cts = new CancellationTokenSource(Timeout);

        await connection.ConnectAsync(OptionsFor(broker), cts.Token);
        await connection.SubscribeAsync("inst/acme/device-commands/sensor-001", MqttQos.AtLeastOnce, cts.Token);

        Assert.Equal("inst:acme:sensor-001", broker.ClientId);
        Assert.Equal("acme:cred-1", broker.Username);
        Assert.Equal(string.Empty, broker.Password);
        Assert.False(broker.CleanSession);
    }

    // A refused CONNECT must surface as a typed failure rather than a connection that looks open.
    [Fact]
    public async Task ConnectThrowsWhenTheBrokerRefusesTheConnection()
    {
        // 0x05 = "not authorized" (MQTT 3.1.1 §3.2.2.3).
        using var broker = new ScriptedMqttBroker(connackCode: 0x05);
        await using var connection = new MqttNetConnection();
        using var cts = new CancellationTokenSource(Timeout);

        await Assert.ThrowsAsync<MqttConnectionException>(
            () => connection.ConnectAsync(OptionsFor(broker), cts.Token));
        Assert.False(connection.IsConnected);
    }

    // QoS-1 publish completes only on PUBACK, and the bytes must arrive unaltered — the publish
    // path carries the same JSON body the HTTP carrier posts.
    [Fact]
    public async Task PublishReachesTheBrokerWithTheExactPayload()
    {
        using var broker = new ScriptedMqttBroker();
        await using var connection = new MqttNetConnection();
        using var cts = new CancellationTokenSource(Timeout);

        await connection.ConnectAsync(OptionsFor(broker), cts.Token);

        var payload = Encoding.UTF8.GetBytes("{\"device\":\"sensor-001\"}");
        await connection.PublishAsync("inst/acme/devices/sensor-001/events", payload, MqttQos.AtLeastOnce, cts.Token);

        Assert.Contains("inst/acme/devices/sensor-001/events", broker.PublishedTopics);
        var index = broker.PublishedTopics.IndexOf("inst/acme/devices/sensor-001/events");
        Assert.Equal(payload, broker.PublishedPayloads[index]);
    }

    // A broker that answers CONNECT and then stays silent on SUBSCRIBE.
    private sealed class SilentSubackBroker : IDisposable
    {
        private readonly System.Net.Sockets.TcpListener _listener;
        private readonly CancellationTokenSource _cts = new();

        public Uri BrokerUri { get; }

        public SilentSubackBroker()
        {
            _listener = new System.Net.Sockets.TcpListener(System.Net.IPAddress.Loopback, 0);
            _listener.Start();
            var port = ((System.Net.IPEndPoint)_listener.LocalEndpoint).Port;
            BrokerUri = new Uri($"tcp://127.0.0.1:{port}");
            _ = Task.Run(AcceptAsync);
        }

        private async Task AcceptAsync()
        {
            try
            {
                var client = await _listener.AcceptTcpClientAsync().ConfigureAwait(false);
                var stream = client.GetStream();
                var buffer = new byte[1024];
                // Read the CONNECT, answer it, then deliberately answer nothing further.
                await stream.ReadAsync(buffer, 0, buffer.Length, _cts.Token).ConfigureAwait(false);
                await stream.WriteAsync(new byte[] { 0x20, 0x02, 0x00, 0x00 }, 0, 4, _cts.Token).ConfigureAwait(false);
                await Task.Delay(System.Threading.Timeout.Infinite, _cts.Token).ConfigureAwait(false);
            }
            catch (Exception)
            {
                // Expected on teardown.
            }
        }

        public void Dispose()
        {
            _cts.Cancel();
            _listener.Stop();
            _cts.Dispose();
        }
    }
}
