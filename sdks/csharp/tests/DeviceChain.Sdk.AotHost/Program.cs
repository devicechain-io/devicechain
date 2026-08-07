// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
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

            var broker = Environment.GetEnvironmentVariable("DC_MQTT_BROKER");
            if (!string.IsNullOrEmpty(broker))
            {
                await RealBrokerRoundTripAsync(broker!).ConfigureAwait(false);
                Console.WriteLine($"PASS real-broker round-trip against {broker}");
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
