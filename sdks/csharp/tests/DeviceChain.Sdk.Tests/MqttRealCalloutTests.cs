// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Net.Http;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Ingest;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sdk.Transport;
using Xunit;

namespace DeviceChain.Sdk.Tests;

/// <summary>
/// Drives the SDK's device plane through a REAL nats-server MQTT gateway sitting behind the
/// platform's auth callout, so that admission -- the username grammar, the client-id rule, the
/// credential lookup and the minted per-device grant -- is exercised instead of assumed.
/// </summary>
/// <remarks>
/// <para>
/// 🔴 WHY THIS EXISTS BESIDE <see cref="MqttRealBrokerTests"/>. That class runs the broker AUTHLESS,
/// and an authless broker accepts anything: a first connect that the callout would refuse is
/// invisible to it, and so is a grant that does not cover a topic the SDK uses. The only path
/// through the callout used to be a manual probe against a cluster. These tests are skipped unless
/// <c>DC_CALLOUT_FIXTURE</c> names the JSON file the fixture broker wrote
/// (<c>backend/core/natsauth/cmd/calloutfixture</c>), and CI sets it; see the <c>csharp</c> job.
/// </para>
/// <para>
/// 🔑 THE FIXTURE IS THE PLATFORM'S OWN PIECES, NOT A COPY OF THEM: the broker is configured the way
/// the deployed one is, and the responder mints each grant with the function device-management
/// calls. What it leaves out is the database -- credential revocation and event attribution are not
/// here. A green run therefore says "this SDK is admitted, and confined, by the real callout and
/// grant", not "the platform accepted the event".
/// </para>
/// <para>
/// Every refusal below is paired with a positive in the same file (the first two tests, and the
/// own-topic subscribe inside the subscribe test), because a responder that refused everything
/// would satisfy every refusal assertion perfectly.
/// </para>
/// </remarks>
[Trait("Category", "RealCallout")]
public class MqttRealCalloutTests
{
    private static readonly TimeSpan Timeout = TimeSpan.FromSeconds(30);

    private static RealCalloutFixture Fixture => RealCalloutFixture.Load()!;

    private static bool Available => RealCalloutFixture.Configured;

    private const string Reason = "DC_CALLOUT_FIXTURE is not set";

    [SkippableFact]
    public async Task ADeviceIsAdmittedGrantedAndItsMeasurementReachesThePlatformSideThroughTheCallout()
    {
        Skip.IfNot(Available, Reason);
        var fx = Fixture;
        var device = fx.Devices[0];
        var metric = "g10_metric_" + RunId();

        await using var session = new MqttDeviceSession(fx.SessionOptions(device));
        using var cts = new CancellationTokenSource(Timeout);
        await session.StartAsync((_, _) => Task.FromResult(CommandOutcome.Succeeded()), cts.Token);
        Assert.Equal(MqttSessionState.Ready, session.State);

        var publisher = new DeviceEventPublisher(new MqttDeviceEventCarrier(session, device.Token));
        await publisher.EmitMeasurementsAsync(
            device.Token, device.Credential,
            new Dictionary<string, double> { [metric] = 42 },
            cancellationToken: cts.Token);

        // 🔑 READ BACK FROM THE NATS SIDE. A PUBACK is the broker taking ownership; only a
        // subscriber seeing the body proves the minted grant let it through.
        var body = await fx.WaitForAsync("events", device.Token, b => b.Contains(metric, StringComparison.Ordinal), Timeout);

        // The platform refuses an MQTT event whose body names a credential, and the broker has
        // already stored the body before any platform code runs -- so the SDK must not send one.
        using var doc = JsonDocument.Parse(body);
        foreach (var property in doc.RootElement.EnumerateObject())
        {
            Assert.DoesNotContain("credential", property.Name, StringComparison.OrdinalIgnoreCase);
        }

        Assert.DoesNotContain(device.Credential, body, StringComparison.Ordinal);
    }

    [SkippableFact]
    public async Task ACommandPublishedOnTheNatsSideReachesTheDeviceAndItsAnswerComesBack()
    {
        Skip.IfNot(Available, Reason);
        var fx = Fixture;
        var device = fx.Devices[0];
        var commandToken = "cmd-g10-" + RunId();
        var received = new TaskCompletionSource<DeviceCommand>(TaskCreationOptions.RunContinuationsAsynchronously);

        await using var session = new MqttDeviceSession(fx.SessionOptions(device));
        await session.StartAsync((command, _) =>
        {
            received.TrySetResult(command);
            return Task.FromResult(CommandOutcome.Succeeded("moving"));
        }, new CancellationTokenSource(Timeout).Token);

        // Issued only after StartAsync returned, i.e. after the SUBACK: the grant covers the
        // command topic and the broker will deliver to it.
        await fx.PostCommandAsync(device.Token, commandToken, "goRefuel");

        var command = await received.Task.WaitAsync(Timeout);
        Assert.Equal(commandToken, command.Token);
        Assert.Equal("goRefuel", command.Name);

        var answer = await fx.WaitForAsync(
            "responses", device.Token, b => b.Contains($"\"commandToken\":\"{commandToken}\"", StringComparison.Ordinal), Timeout);
        Assert.Contains("\"success\":true", answer);
        Assert.Contains("\"payload\":\"moving\"", answer);
    }

    [SkippableFact]
    public async Task AWrongCredentialIsRefusedAtConnack()
    {
        Skip.IfNot(Available, Reason);
        var fx = Fixture;
        var device = fx.Devices[0];

        // One character changed, same length and alphabet, so only the VALUE differs and no
        // grammar check can refuse it before the credential is looked up.
        var options = new MqttSessionOptions(
            new Uri(fx.MqttUrl), fx.Instance, fx.Tenant, device.Token, RealCalloutFixture.Mutate(device.Credential));

        await using var session = new MqttDeviceSession(options);
        await Assert.ThrowsAsync<MqttConnectRefusedException>(
            () => session.StartAsync(
                (_, _) => Task.FromResult(CommandOutcome.Succeeded()), new CancellationTokenSource(Timeout).Token));
    }

    [SkippableFact]
    public async Task ACredentialPresentedWithAnotherDevicesClientIdIsRefused()
    {
        Skip.IfNot(Available, Reason);
        var fx = Fixture;

        // A genuine credential for device 0, but the session claims to be device 1: the client id
        // must be the credential's own device's, whatever the credential is otherwise good for.
        var options = new MqttSessionOptions(
            new Uri(fx.MqttUrl), fx.Instance, fx.Tenant, fx.Devices[1].Token, fx.Devices[0].Credential);

        await using var session = new MqttDeviceSession(options);
        await Assert.ThrowsAsync<MqttConnectRefusedException>(
            () => session.StartAsync(
                (_, _) => Task.FromResult(CommandOutcome.Succeeded()), new CancellationTokenSource(Timeout).Token));
    }

    [SkippableFact]
    public async Task ADeviceCannotSubscribeToAnotherDevicesCommands()
    {
        Skip.IfNot(Available, Reason);
        var fx = Fixture;
        var own = fx.Devices[0];
        var other = fx.Devices[1];
        using var cts = new CancellationTokenSource(Timeout);

        await using var connection = new MqttNetConnection();
        await connection.ConnectAsync(fx.ConnectOptions(own), cts.Token);

        // Positive first: the device's own command topic is granted, so the refusal below is the
        // grant's doing and not a connection that cannot subscribe at all.
        await connection.SubscribeAsync(
            DevicePlane.CommandsTopic(fx.Instance, fx.Tenant, own.Token), MqttQos.AtLeastOnce, cts.Token);

        await Assert.ThrowsAsync<MqttSubscribeRefusedException>(
            () => connection.SubscribeAsync(
                DevicePlane.CommandsTopic(fx.Instance, fx.Tenant, other.Token), MqttQos.AtLeastOnce, cts.Token));
    }

    [SkippableFact]
    public async Task ADevicesPublishToAnotherDevicesEventsTopicNeverReachesThePlatformSide()
    {
        Skip.IfNot(Available, Reason);
        var fx = Fixture;
        var own = fx.Devices[0];
        var other = fx.Devices[1];
        var run = RunId();
        using var cts = new CancellationTokenSource(Timeout);

        // The forged publish gets a connection of its own: the broker may close a connection that
        // violates its grant, and that must not take the proving publish down with it.
        await using (var forger = new MqttNetConnection())
        {
            await forger.ConnectAsync(fx.ConnectOptions(own, "forger"), cts.Token);
            try
            {
                await forger.PublishAsync(
                    DevicePlane.EventsTopic(fx.Instance, fx.Tenant, other.Token),
                    Encoding.UTF8.GetBytes($"{{\"device\":\"{other.Token}\",\"forged\":\"{run}\"}}"),
                    MqttQos.AtLeastOnce,
                    cts.Token);
            }
            catch (MqttConnectionException)
            {
                // A refusal surfaced as an exception is as good as a silent drop for this test.
            }
        }

        // The proving publish, on the device's own topic, from a second connection. Once it has
        // arrived the forged one -- sent earlier -- has had every chance to.
        await using var honest = new MqttNetConnection();
        await honest.ConnectAsync(fx.ConnectOptions(own, "honest"), cts.Token);
        await honest.PublishAsync(
            DevicePlane.EventsTopic(fx.Instance, fx.Tenant, own.Token),
            Encoding.UTF8.GetBytes($"{{\"device\":\"{own.Token}\",\"marker\":\"{run}\"}}"),
            MqttQos.AtLeastOnce,
            cts.Token);
        await fx.WaitForAsync("events", own.Token, b => b.Contains(run, StringComparison.Ordinal), Timeout);

        var leaked = await fx.GetAsync("events", other.Token);
        Assert.DoesNotContain(leaked, b => b.Contains(run, StringComparison.Ordinal));
    }

    private static string RunId() => Guid.NewGuid().ToString("N").Substring(0, 8);
}

/// <summary>
/// Fails the build if the real-callout tests were SKIPPED in an environment that promised the
/// fixture broker, for the reason <see cref="MqttRealBrokerGuardTests"/> exists: a skip reads as a
/// pass, and a fixture step that quietly died would leave the job green while testing nothing.
/// </summary>
public class MqttRealCalloutGuardTests
{
    [Fact]
    public void TheRealCalloutTestsMustNotBeSkippedWhereTheFixtureWasPromised()
    {
        var required = Environment.GetEnvironmentVariable("DC_REQUIRE_CALLOUT_FIXTURE");
        if (string.IsNullOrEmpty(required) || required == "0")
        {
            return;
        }

        Assert.True(
            RealCalloutFixture.Configured,
            "DC_REQUIRE_CALLOUT_FIXTURE is set but DC_CALLOUT_FIXTURE is not, so every real-callout " +
            "test would silently skip and this job would pass while exercising no callout at all. " +
            "Fix the step that starts the fixture broker rather than relaxing this check.");
    }
}

/// <summary>The fixture broker's description (see <c>calloutfixture</c>) and a client for its control surface.</summary>
internal sealed class RealCalloutFixture
{
    private static readonly HttpClient Http = new() { Timeout = TimeSpan.FromSeconds(10) };

    internal static bool Configured => !string.IsNullOrEmpty(Environment.GetEnvironmentVariable("DC_CALLOUT_FIXTURE"));

    internal sealed record Device(string Token, string Credential);

    internal string MqttUrl { get; private init; } = "";

    internal string ControlUrl { get; private init; } = "";

    internal string Instance { get; private init; } = "";

    internal string Tenant { get; private init; } = "";

    internal IReadOnlyList<Device> Devices { get; private init; } = Array.Empty<Device>();

    internal static RealCalloutFixture? Load()
    {
        var path = Environment.GetEnvironmentVariable("DC_CALLOUT_FIXTURE");
        if (string.IsNullOrEmpty(path))
        {
            return null;
        }

        using var doc = JsonDocument.Parse(System.IO.File.ReadAllText(path));
        var root = doc.RootElement;
        var devices = new List<Device>();
        foreach (var d in root.GetProperty("devices").EnumerateArray())
        {
            devices.Add(new Device(d.GetProperty("token").GetString()!, d.GetProperty("credential").GetString()!));
        }

        if (devices.Count < 2)
        {
            throw new InvalidOperationException("the fixture must seed at least two devices");
        }

        return new RealCalloutFixture
        {
            MqttUrl = root.GetProperty("mqttUrl").GetString()!,
            ControlUrl = root.GetProperty("controlUrl").GetString()!,
            Instance = root.GetProperty("instance").GetString()!,
            Tenant = root.GetProperty("tenant").GetString()!,
            Devices = devices,
        };
    }

    internal MqttSessionOptions SessionOptions(Device device) =>
        new(new Uri(MqttUrl), Instance, Tenant, device.Token, device.Credential);

    internal MqttConnectOptions ConnectOptions(Device device, string? discriminator = null) =>
        new(new Uri(MqttUrl), DevicePlane.DeviceClientId(Instance, Tenant, device.Token, discriminator))
        {
            Username = DevicePlane.ConnectUsername(Tenant, device.Credential),
            Password = string.Empty,
            CleanSession = false,
        };

    internal async Task PostCommandAsync(string device, string token, string name)
    {
        using var response = await Http.PostAsync(
            $"{ControlUrl}/command?device={Uri.EscapeDataString(device)}&token={Uri.EscapeDataString(token)}&name={Uri.EscapeDataString(name)}",
            content: null);
        response.EnsureSuccessStatusCode();
    }

    internal async Task<List<string>> GetAsync(string what, string device)
    {
        var json = await Http.GetStringAsync($"{ControlUrl}/{what}?device={Uri.EscapeDataString(device)}");
        return JsonSerializer.Deserialize<List<string>>(json) ?? new List<string>();
    }

    /// <summary>Polls until a body the fixture recorded satisfies <paramref name="match"/>.</summary>
    internal async Task<string> WaitForAsync(string what, string device, Func<string, bool> match, TimeSpan timeout)
    {
        var deadline = DateTime.UtcNow + timeout;
        while (DateTime.UtcNow < deadline)
        {
            foreach (var body in await GetAsync(what, device))
            {
                if (match(body))
                {
                    return body;
                }
            }

            await Task.Delay(100);
        }

        throw new TimeoutException($"nothing matching arrived on the platform side's {what} of {device} within {timeout}");
    }

    /// <summary>
    /// Changes the last character, so the result keeps the credential's length and shape and can
    /// differ only in value.
    /// </summary>
    internal static string Mutate(string credential)
    {
        var last = credential[^1];
        var replacement = last == '0' ? '1' : '0';
        return credential[..^1] + replacement;
    }
}
