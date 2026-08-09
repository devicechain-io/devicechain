// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Net;
using System.Net.Http;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk;
using DeviceChain.Sdk.Ingest;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sdk.Transport;
using Xunit;

namespace DeviceChain.Sdk.Tests;

// The carrier seam: one publisher, two wires, one body. DeviceEventPublisherTests already covers
// the HTTP behaviour and is the regression guard for the extraction — these cover the seam itself.
public class DeviceEventCarrierTests
{
    private static readonly TimeSpan Timeout = TimeSpan.FromSeconds(10);

    // 🔑 THE CLAIM THE WHOLE DESIGN RESTS ON, PINNED — AND IT HAS TO CROSS A REAL BOUNDARY ON BOTH
    // SIDES TO MEAN ANYTHING. Comparing two capturing fakes would only prove the publisher does not
    // branch on carrier type, which it visibly does not; that check cannot fail for any interesting
    // reason. So BOTH sides run the real carrier: the HTTP side goes through
    // HttpDeviceEventCarrier over a stub message handler, and the MQTT side through the real
    // carrier and the real session. The bytes compared are the ones that reached each wire.
    //
    // 🔴 The HTTP side used to be a CapturingCarrier fake, and that gap was found by mutation
    // rather than by reading: a publisher that built a DIFFERENT body when its carrier was the HTTP
    // one survived the entire 45-test suite. The fake stood exactly where the mutation lived, so
    // the pair actually compared was (fake, real-MQTT) and the branch was invisible from both ends.
    // The comment above it claimed the comparison "crosses a real boundary" — true of one side, and
    // the untrue half was the half being tested. A fake that stands in for the subject measures the
    // fixture.
    [Fact]
    public async Task TheBytesThatReachTheMqttWireAreIdenticalToTheHttpBody()
    {
        var occurred = new DateTimeOffset(2026, 8, 7, 12, 0, 0, TimeSpan.Zero);
        var metrics = new Dictionary<string, double> { ["fuelPct"] = 12.5, ["engineTempC"] = 91 };

        var handler = new StubHandler((_, _) => (HttpStatusCode.Accepted, ""));
        await new DeviceEventPublisher(new HttpClient(handler), new Uri("https://ingress.local"), "inst", "acme")
            .EmitMeasurementsAsync("sensor-001", "cred-1", metrics, occurred);
        byte[] overHttp = Encoding.UTF8.GetBytes(Assert.Single(handler.Calls).Body);

        var connection = new RecordingConnection();
        await using var session = new MqttDeviceSession(
            new MqttSessionOptions(new Uri("tcp://127.0.0.1:1883"), "inst", "acme", "sensor-001", "cred-1"),
            new SingleConnectionFactory(connection));
        await session.StartAsync((_, _) => Task.FromResult(CommandOutcome.Succeeded()), CancellationToken.None);

        await new DeviceEventPublisher(new MqttDeviceEventCarrier(session, "sensor-001"))
            .EmitMeasurementsAsync("sensor-001", "cred-1", metrics, occurred);

        var onTheWire = Assert.Single(connection.Published).Payload;
        Assert.Equal(overHttp, onTheWire);

        // And the body must still carry its own credentials on the MQTT wire: the device-plane
        // gateway does not stamp events as transport-authenticated (only the LwM2M and Sparkplug
        // adapters do), so a body without them is rejected under the default deviceAuthMode.
        using var document = JsonDocument.Parse(onTheWire);
        Assert.Equal("ACCESS_TOKEN", document.RootElement.GetProperty("credentialType").GetString());
        Assert.Equal("cred-1", document.RootElement.GetProperty("credentialId").GetString());
        Assert.Equal("sensor-001", document.RootElement.GetProperty("device").GetString());
    }

    // The same claim for the Location leg, and it is not redundant with the measurement one: the
    // property is "the publisher builds ONE body and hands it to a carrier", and a second event
    // type is exactly how that stops being true — a second serialize-and-send path would satisfy
    // the measurement test above forever while quietly diverging here.
    [Fact]
    public async Task TheLocationBytesThatReachTheMqttWireAreIdenticalToTheHttpBody()
    {
        var occurred = new DateTimeOffset(2026, 8, 7, 12, 0, 0, TimeSpan.Zero);
        var fix = new LocationFix(33.74912345, -84.38812345) { Elevation = 320.53, Heading = 271.53 };

        var handler = new StubHandler((_, _) => (HttpStatusCode.Accepted, ""));
        await new DeviceEventPublisher(new HttpClient(handler), new Uri("https://ingress.local"), "inst", "acme")
            .EmitLocationAsync("sensor-001", "cred-1", fix, occurred);
        byte[] overHttp = Encoding.UTF8.GetBytes(Assert.Single(handler.Calls).Body);

        var connection = new RecordingConnection();
        await using var session = new MqttDeviceSession(
            new MqttSessionOptions(new Uri("tcp://127.0.0.1:1883"), "inst", "acme", "sensor-001", "cred-1"),
            new SingleConnectionFactory(connection));
        await session.StartAsync((_, _) => Task.FromResult(CommandOutcome.Succeeded()), CancellationToken.None);

        await new DeviceEventPublisher(new MqttDeviceEventCarrier(session, "sensor-001"))
            .EmitLocationAsync("sensor-001", "cred-1", fix, occurred);

        var onTheWire = Assert.Single(connection.Published).Payload;
        Assert.Equal(overHttp, onTheWire);

        using var document = JsonDocument.Parse(onTheWire);
        Assert.Equal("Location", document.RootElement.GetProperty("eventType").GetString());
        Assert.Equal("ACCESS_TOKEN", document.RootElement.GetProperty("credentialType").GetString());
        Assert.Equal("cred-1", document.RootElement.GetProperty("credentialId").GetString());
        // And the unreported optionals are absent on the MQTT wire too, not zeroed by a second path.
        JsonElement entry = document.RootElement.GetProperty("payload").GetProperty("entries")[0];
        Assert.False(entry.TryGetProperty("accuracy", out _));
        Assert.False(entry.TryGetProperty("speed", out _));
    }

    // The MQTT carrier is device-scoped where the HTTP one is not. Emitting another device's event
    // down this connection would be dead-lettered downstream, out of sight of the publisher — so
    // it fails here instead.
    [Fact]
    public async Task TheMqttCarrierRefusesToEmitForAnotherDevice()
    {
        var connection = new RecordingConnection();
        await using var session = new MqttDeviceSession(
            new MqttSessionOptions(new Uri("tcp://127.0.0.1:1883"), "inst", "acme", "sensor-001", "cred-1"),
            new SingleConnectionFactory(connection));
        await session.StartAsync((_, _) => Task.FromResult(CommandOutcome.Succeeded()), CancellationToken.None);

        var carrier = new MqttDeviceEventCarrier(session, "sensor-001");

        await carrier.SendAsync("sensor-001", Encoding.UTF8.GetBytes("{}"), CancellationToken.None);
        await Assert.ThrowsAsync<InvalidOperationException>(
            () => carrier.SendAsync("sensor-002", Encoding.UTF8.GetBytes("{}"), CancellationToken.None));
    }

    // Telemetry goes to the device's OWN events topic at QoS 1 — the topic the broker grant is
    // minted from, and the QoS whose ack is the durability point.
    [Fact]
    public async Task TheMqttCarrierPublishesToTheDevicesEventsTopicAtQos1()
    {
        var connection = new RecordingConnection();
        await using var session = new MqttDeviceSession(
            new MqttSessionOptions(new Uri("tcp://127.0.0.1:1883"), "inst", "acme", "sensor-001", "cred-1"),
            new SingleConnectionFactory(connection));
        await session.StartAsync((_, _) => Task.FromResult(CommandOutcome.Succeeded()), CancellationToken.None);

        var publisher = new DeviceEventPublisher(new MqttDeviceEventCarrier(session, "sensor-001"));
        await publisher.EmitMeasurementsAsync(
            "sensor-001", "cred-1", new Dictionary<string, double> { ["fuelPct"] = 12.5 });

        var published = Assert.Single(connection.Published);
        Assert.Equal("inst/acme/devices/sensor-001/events", published.Topic);
        Assert.Equal(MqttQos.AtLeastOnce, published.Qos);
    }

    // A session that is not connected must fail loudly rather than drop telemetry on the floor.
    [Fact]
    public async Task TheMqttCarrierFailsWhenTheSessionIsNotConnected()
    {
        var connection = new RecordingConnection();
        await using var session = new MqttDeviceSession(
            new MqttSessionOptions(new Uri("tcp://127.0.0.1:1883"), "inst", "acme", "sensor-001", "cred-1"),
            new SingleConnectionFactory(connection));

        // Never started, so never connected.
        var carrier = new MqttDeviceEventCarrier(session, "sensor-001");
        await Assert.ThrowsAsync<MqttConnectionException>(
            () => carrier.SendAsync("sensor-001", Encoding.UTF8.GetBytes("{}"), CancellationToken.None));
    }

    private sealed class CapturingCarrier : IDeviceEventCarrier
    {
        public byte[]? LastBody { get; private set; }

        public Task SendAsync(string deviceToken, byte[] jsonEvent, CancellationToken cancellationToken)
        {
            LastBody = jsonEvent;
            return Task.CompletedTask;
        }
    }

    private sealed class SingleConnectionFactory : IMqttClientFactory
    {
        private readonly IMqttConnection _connection;

        public SingleConnectionFactory(IMqttConnection connection) => _connection = connection;

        public IMqttConnection Create() => _connection;
    }

    private sealed class RecordingConnection : IMqttConnection
    {
        public List<(string Topic, byte[] Payload, MqttQos Qos)> Published { get; } = new();

        public bool IsConnected { get; private set; }

        public event Func<MqttInbound, Task>? MessageReceived;

        public event Action<Exception?>? ConnectionLost;

        public Task ConnectAsync(MqttConnectOptions options, CancellationToken cancellationToken)
        {
            IsConnected = true;
            return Task.CompletedTask;
        }

        public Task SubscribeAsync(string topicFilter, MqttQos qos, CancellationToken cancellationToken) =>
            Task.CompletedTask;

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

        // Present to satisfy the interface; these tests drive publishing, not delivery.
        public void Unused()
        {
            MessageReceived?.Invoke(default);
            ConnectionLost?.Invoke(null);
        }
    }
}
