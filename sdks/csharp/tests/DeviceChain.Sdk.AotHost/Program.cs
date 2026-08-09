// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Ingest;
using DeviceChain.Sdk.Tests;
using DeviceChain.Sdk.Transport;

namespace DeviceChain.Sdk.AotHost;

/// <summary>
/// Exercises the MQTT device-plane path under a real Native-AOT compilation.
/// </summary>
/// <remarks>
/// The round-trip runs against the in-process scripted broker, so the gate needs no network, no
/// service container and no external binary — it can run anywhere the AOT toolchain runs. When
/// <c>DC_MQTT_BROKER</c> is set it additionally dials that broker, which is how the same binary
/// becomes an end-to-end check once a real nats-server is in the job.
/// </remarks>
public static class Program
{
    /// <summary>Runs the checks; a non-zero exit code fails the gate.</summary>
    public static async Task<int> Main()
    {
        try
        {
            await ScriptedRoundTripAsync().ConfigureAwait(false);
            Console.WriteLine("PASS scripted round-trip (connect + confirmed subscribe + QoS-1 publish)");

            await RefusalIsSurfacedAsync().ConfigureAwait(false);
            Console.WriteLine("PASS refused subscription surfaced under AOT");

            await LocationEmitAsync().ConfigureAwait(false);
            Console.WriteLine("PASS location emit serialized + validated under AOT");

            var broker = Environment.GetEnvironmentVariable("DC_MQTT_BROKER");
            if (!string.IsNullOrEmpty(broker))
            {
                await RealBrokerRoundTripAsync(broker!).ConfigureAwait(false);
                Console.WriteLine($"PASS real-broker round-trip against {broker}");
            }
            else if (Requires("DC_REQUIRE_MQTT_BROKER"))
            {
                // 🔴 THE SAME SILENT-SKIP TRAP THE TEST SUITE GUARDS AGAINST, AND IT WAS LEFT OPEN
                // HERE. This host is the second consumer of the broker CI starts; without this
                // branch, dropping the env line from the CI step would quietly degrade the gate to
                // scripted-only and it would stay green forever.
                throw new InvalidOperationException(
                    "DC_REQUIRE_MQTT_BROKER is set but DC_MQTT_BROKER is not, so the real-broker leg " +
                    "of this gate would silently skip. Fix the step that starts nats-server.");
            }
            else
            {
                Console.WriteLine("SKIP real-broker round-trip (DC_MQTT_BROKER unset)");
            }

            Console.WriteLine("AOT GATE OK");
            return 0;
        }
        catch (Exception ex)
        {
            // An AOT failure typically presents here, at runtime, rather than at publish time.
            Console.Error.WriteLine($"AOT GATE FAILED: {ex.GetType().FullName}: {ex.Message}");
            Console.Error.WriteLine(ex.StackTrace);
            return 1;
        }
    }

    private static bool Requires(string variable)
    {
        var value = Environment.GetEnvironmentVariable(variable);
        return !string.IsNullOrEmpty(value) && value != "0";
    }

    private static async Task ScriptedRoundTripAsync()
    {
        using var broker = new ScriptedMqttBroker();
        await using var connection = new MqttNetConnection();
        using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(20));

        var options = new MqttConnectOptions(broker.BrokerUri, "inst:acme:sensor-001")
        {
            Username = "acme:cred-1",
            Password = string.Empty,
            CleanSession = false,
        };

        await connection.ConnectAsync(options, cts.Token).ConfigureAwait(false);
        await connection.SubscribeAsync(
            "inst/acme/device-commands/sensor-001", MqttQos.AtLeastOnce, cts.Token).ConfigureAwait(false);

        var payload = Encoding.UTF8.GetBytes("{\"device\":\"sensor-001\"}");
        await connection.PublishAsync(
            "inst/acme/devices/sensor-001/events", payload, MqttQos.AtLeastOnce, cts.Token).ConfigureAwait(false);

        Require(broker.ClientId == "inst:acme:sensor-001", "the client id did not reach the wire");
        Require(broker.Username == "acme:cred-1", "the username did not reach the wire");
        Require(broker.Password == string.Empty, "the empty password did not reach the wire");
        Require(broker.PublishedTopics.Contains("inst/acme/devices/sensor-001/events"), "the publish did not arrive");

        await connection.DisconnectAsync(TimeSpan.FromSeconds(2), cts.Token).ConfigureAwait(false);
    }

    private static async Task RefusalIsSurfacedAsync()
    {
        using var broker = new ScriptedMqttBroker(subackCodes: new byte[] { 0x80 });
        await using var connection = new MqttNetConnection();
        using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(20));

        await connection.ConnectAsync(
            new MqttConnectOptions(broker.BrokerUri, "inst:acme:sensor-001"), cts.Token).ConfigureAwait(false);

        try
        {
            await connection.SubscribeAsync(
                "inst/acme/device-commands/sensor-001", MqttQos.AtLeastOnce, cts.Token).ConfigureAwait(false);
        }
        catch (MqttSubscribeRefusedException)
        {
            return;
        }

        throw new InvalidOperationException(
            "a refused subscription did NOT throw under AOT — the SUBACK read was lost in compilation");
    }

    /// <summary>
    /// Drives the Location emit path under real AOT compilation.
    /// </summary>
    /// <remarks>
    /// The MQTT legs above prove the transport survives; they say nothing about the emit path, which
    /// is where the AOT-sensitive machinery actually lives: source-generated JSON metadata for a
    /// second root type, reached through a GENERIC send taking a <c>JsonTypeInfo&lt;T&gt;</c>. If the
    /// generator's metadata for <c>LocationEvent</c> were trimmed away, or the generic instantiation
    /// were not rooted, this fails HERE at runtime — which is how AOT failures present — rather than
    /// at publish time. The unreported-optional and range-check assertions are included for the same
    /// reason: both are behaviour that would go SILENT rather than loud if a code path vanished.
    /// </remarks>
    private static async Task LocationEmitAsync()
    {
        var carrier = new CapturingCarrier();
        var publisher = new DeviceEventPublisher(carrier);
        var fix = new LocationFix(33.74912345, -84.38812345) { Elevation = 320.53, Heading = 271.53 };

        await publisher.EmitLocationAsync(
            "sensor-001", "cred-1", fix, new DateTimeOffset(2026, 8, 7, 12, 0, 0, TimeSpan.Zero))
            .ConfigureAwait(false);

        var body = Encoding.UTF8.GetString(carrier.LastBody ?? Array.Empty<byte>());
        Require(body.Contains("\"eventType\":\"Location\""), $"the Location envelope did not serialize: {body}");
        Require(body.Contains("\"entries\":["), $"the entries wrapper is missing: {body}");
        Require(body.Contains("\"latitude\":\"33.74912345\""), $"latitude did not serialize as a string: {body}");
        Require(body.Contains("\"longitude\":\"-84.38812345\""), $"longitude did not serialize as a string: {body}");
        Require(body.Contains("\"heading\":\"271.53\""), $"heading did not serialize as a string: {body}");
        Require(!body.Contains("accuracy"), $"an UNREPORTED optional reached the wire: {body}");
        Require(!body.Contains("speed"), $"an UNREPORTED optional reached the wire: {body}");

        // Small magnitudes must not fall back to exponent notation under this runtime's formatter.
        await publisher.EmitLocationAsync("sensor-001", "cred-1", new LocationFix(1e-7, -1.234e-7))
            .ConfigureAwait(false);
        var small = Encoding.UTF8.GetString(carrier.LastBody ?? Array.Empty<byte>());
        Require(small.Contains("\"latitude\":\"0.0000001\""), $"a small latitude used exponent notation: {small}");

        var refused = false;
        try
        {
            await publisher.EmitLocationAsync("sensor-001", "cred-1", new LocationFix(91, 0)).ConfigureAwait(false);
        }
        catch (ArgumentOutOfRangeException)
        {
            refused = true;
        }

        Require(refused, "an out-of-range latitude was NOT refused under AOT");
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

    private static async Task RealBrokerRoundTripAsync(string brokerUri)
    {
        await using var connection = new MqttNetConnection();
        using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(30));

        await connection.ConnectAsync(
            new MqttConnectOptions(new Uri(brokerUri), "inst:acme:sensor-001"), cts.Token).ConfigureAwait(false);
        await connection.SubscribeAsync(
            "inst/acme/device-commands/sensor-001", MqttQos.AtLeastOnce, cts.Token).ConfigureAwait(false);
        await connection.PublishAsync(
            "inst/acme/devices/sensor-001/events",
            Encoding.UTF8.GetBytes("{\"device\":\"sensor-001\"}"),
            MqttQos.AtLeastOnce,
            cts.Token).ConfigureAwait(false);
        await connection.DisconnectAsync(TimeSpan.FromSeconds(2), cts.Token).ConfigureAwait(false);
    }

    private static void Require(bool condition, string message)
    {
        if (!condition)
        {
            throw new InvalidOperationException(message);
        }
    }
}
