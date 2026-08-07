// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
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

    // 🔑 THE CLAIM THE WHOLE DESIGN RESTS ON, PINNED — AND IT HAS TO CROSS A REAL BOUNDARY TO MEAN
    // ANYTHING. Comparing two capturing fakes would only prove the publisher does not branch on
    // carrier type, which it visibly does not; that check cannot fail for any interesting reason.
    // So the MQTT side here runs the REAL carrier and the REAL session, and the bytes compared are
    // the ones that reached the wire. A carrier that re-serialized, wrapped, or re-encoded the
    // body would fail this; a carrier that merely forwards it passes.
    [Fact]
    public async Task TheBytesThatReachTheMqttWireAreIdenticalToTheHttpBody()
    {
        var occurred = new DateTimeOffset(2026, 8, 7, 12, 0, 0, TimeSpan.Zero);
        var metrics = new Dictionary<string, double> { ["fuelPct"] = 12.5, ["engineTempC"] = 91 };

        var http = new CapturingCarrier();
        await new DeviceEventPublisher(http).EmitMeasurementsAsync("sensor-001", "cred-1", metrics, occurred);

        var connection = new RecordingConnection();
        await using var session = new MqttDeviceSession(
            new MqttSessionOptions(new Uri("tcp://127.0.0.1:1883"), "inst", "acme", "sensor-001", "cred-1"),
            new SingleConnectionFactory(connection));
        await session.StartAsync((_, _) => Task.FromResult(CommandOutcome.Succeeded()), CancellationToken.None);

        await new DeviceEventPublisher(new MqttDeviceEventCarrier(session, "sensor-001"))
            .EmitMeasurementsAsync("sensor-001", "cred-1", metrics, occurred);

        var onTheWire = Assert.Single(connection.Published).Payload;
        Assert.Equal(http.LastBody, onTheWire);

        // And the body must still carry its own credentials on the MQTT wire: the device-plane
        // gateway does not stamp events as transport-authenticated (only the LwM2M and Sparkplug
        // adapters do), so a body without them is rejected under the default deviceAuthMode.
        using var document = JsonDocument.Parse(onTheWire);
        Assert.Equal("ACCESS_TOKEN", document.RootElement.GetProperty("credentialType").GetString());
        Assert.Equal("cred-1", document.RootElement.GetProperty("credentialId").GetString());
        Assert.Equal("sensor-001", document.RootElement.GetProperty("device").GetString());
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
