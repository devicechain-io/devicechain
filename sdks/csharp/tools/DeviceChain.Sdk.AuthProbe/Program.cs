// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A manual rig that drives the shipped MQTT device plane through the WHOLE loop against a real
// cluster, under a REAL credential.
//
// ── why it exists, and why TrustProbe beside it is not enough ─────────────────────────────────
// TrustProbe connects with a deliberately BOGUS credential. That is the right shape for what it
// asks -- it proves the pinned-CA handshake completes and that the broker then refuses -- but it
// means the best outcome it can ever report is a refusal. The acceptance path has no rig at all,
// and nothing in CI can supply one:
//
//   - the real-broker test rung runs a nats-server with NO auth callout configured, because the
//     callout, the client-id admission and the minted per-device grant all live in
//     device-management against a database. An authless broker cannot refuse and cannot accept;
//     it just says yes to everything.
//   - the unit tests drive a fake transport, which cannot authenticate at all.
//
// So before this rig, "the C# SDK can authenticate against the real callout" rested on matched
// code plus INDEPENDENT literals on both sides -- the Go `messaging.DeviceClientID` and the C#
// `DevicePlane.DeviceClientId` agreeing because two people wrote the same string twice -- and had
// never been demonstrated by one live authenticated connect.
//
// It is also the cheapest possible pre-flight before Unity editor time. Editor time is the scarce
// resource; tokens are not. A broken platform hop discovered mid-session gets blamed on the scene.
//
// ── what each stage settles ──────────────────────────────────────────────────────────────────
//   1 CONNECT    TLS handshake, then the auth callout's verdict on a real credential.
//   2 SUBSCRIBE  the per-device command-topic grant the callout minted. A refusal here is a
//                SUBACK reason code >= 0x80, which MQTTnet returns rather than throws -- the
//                #668 class of dead-but-healthy-looking subscription. The SDK checks it; this
//                confirms the check passes against a real grant rather than a permissive fake.
//   3 PUBLISH    one measurement, PUBACKed. Carries the credential IN THE BODY, which the device
//                plane requires: the MQTT gateway never stamps `authenticatedTransport`, so under
//                the default `deviceAuthMode: required` a publish without credentialType and
//                credentialId is rejected. "Identical payload, different carrier" is a
//                requirement here, not a convenience.
//   4 COMMAND    the platform's own REACT sendCommand arrives and is answered. This is the only
//                stage that depends on the instance having a DETECT rule for the metric, so its
//                absence is reported as a distinct outcome rather than a failure.
//
// ── the rule it follows ──────────────────────────────────────────────────────────────────────
// A failure is classified on the EXCEPTION TYPE CHAIN, never on message text. An earlier
// classifier in this repo matched the substring "SSL" to detect a handshake failure and was fooled
// by the `ssl://` scheme in the URI it printed, scoring a clean CONNACK refusal as a TLS failure.
// The subject was fine; the instrument was not.
//
// ── running it ───────────────────────────────────────────────────────────────────────────────
//   # A dotnet installed by dotnet-install.sh lands in ~/.dotnet and is NOT on PATH; the shell
//   # then suggests `snap install dotnet`, which would put a SECOND SDK on the box. Check first:
//   #   ls ~/.dotnet/dotnet && ~/.dotnet/dotnet --version
//   export PATH="$HOME/.dotnet:$PATH"
//   cd /path/to/devicechain          # --project is relative to the CWD
//
//   kubectl -n dc-system get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > /tmp/ca.pem
//   dotnet run --project sdks/csharp/tools/DeviceChain.Sdk.AuthProbe -- \
//       /tmp/ca.pem devicechain <tenant> <deviceToken> <credentialId>
//
// 🔑 Use `localhost`, NEVER `127.0.0.1`. The broker leaf carries DNS SANs only -- no IP SAN at all
// -- so an IP literal is a name mismatch that PinnedCa correctly refuses. Reaching for the
// accept-anything trust mode to get past that would leave the hand-built pinned path unexercised
// by the very rig meant to exercise it.
//
// Exit codes: 0 all stages passed; 1 a stage failed; 2 usage; 3 connected, subscribed and
// published, but no command arrived within the wait (see stage 4).

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Security.Authentication;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Ingest;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sdk.Transport;

namespace DeviceChain.Sdk.AuthProbe;

internal static class Program
{
    // Below the sitepulse low-fuel rule's threshold of 15, so an instance carrying that rule
    // drives the whole loop. Any other instance still exercises stages 1-3 and reports stage 4
    // as "no command", which is an outcome and not a failure.
    private const double LowFuelPercent = 10;
    private const string Metric = "fuel_pct";

    private static async Task<int> Main(string[] args)
    {
        if (args.Length < 5)
        {
            Console.Error.WriteLine(
                "usage: DeviceChain.Sdk.AuthProbe <ca.pem> <instanceId> <tenant> <deviceToken> <credentialId> [host] [waitSeconds]\n" +
                "\n" +
                "  host         defaults to localhost -- NOT 127.0.0.1, the broker leaf has no IP SAN\n" +
                "  waitSeconds  how long to wait for the platform's command; 0 skips stage 4\n" +
                "\n" +
                "  extract the CA with:\n" +
                "    kubectl -n dc-system get secret dc-nats-tls -o jsonpath='{.data.ca\\.crt}' | base64 -d > ca.pem");
            return 2;
        }

        byte[] pinnedCa = File.ReadAllBytes(args[0]);
        string instanceId = args[1], tenant = args[2], deviceToken = args[3], credentialId = args[4];
        string host = args.Length > 5 ? args[5] : "localhost";
        int waitSeconds = 90;
        if (args.Length > 6)
        {
            if (!int.TryParse(args[6], NumberStyles.Integer, CultureInfo.InvariantCulture, out waitSeconds))
            {
                Console.Error.WriteLine($"waitSeconds must be a whole number of seconds, got \"{args[6]}\"");
                return 2;
            }
        }

        var brokerUri = new Uri($"ssl://{host}:1883");
        Console.WriteLine($"broker      : {brokerUri}");
        Console.WriteLine($"pinned CA   : {args[0]} ({pinnedCa.Length} bytes)");
        Console.WriteLine($"client id   : {DevicePlane.DeviceClientId(instanceId, tenant, deviceToken)}");
        Console.WriteLine($"username    : {DevicePlane.ConnectUsername(tenant, credentialId)}");
        Console.WriteLine($"events topic: {DevicePlane.EventsTopic(instanceId, tenant, deviceToken)}");
        Console.WriteLine($"cmds topic  : {DevicePlane.CommandsTopic(instanceId, tenant, deviceToken)}");
        Console.WriteLine();

        var options = new MqttSessionOptions(brokerUri, instanceId, tenant, deviceToken, credentialId)
        {
            Trust = MqttTrust.PinnedCa(pinnedCa),
        };

        var commandSeen = new TaskCompletionSource<DeviceCommand>(TaskCreationOptions.RunContinuationsAsynchronously);
        var elapsed = Stopwatch.StartNew();

        await using var session = new MqttDeviceSession(options);
        session.StateChanged += state => Console.WriteLine($"  [{elapsed.ElapsedMilliseconds,6}ms] session -> {state}");

        Task<CommandOutcome> Handle(DeviceCommand command, CancellationToken cancellationToken)
        {
            Console.WriteLine($"  [{elapsed.ElapsedMilliseconds,6}ms] COMMAND name={command.Name} token={command.Token}");
            commandSeen.TrySetResult(command);
            return Task.FromResult(CommandOutcome.Succeeded("acknowledged by AuthProbe"));
        }

        using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(waitSeconds + 120));

        Console.WriteLine("[1/4] CONNECT   TLS handshake, then the auth callout's verdict");
        Console.WriteLine("[2/4] SUBSCRIBE the per-device command grant the callout minted");
        try
        {
            await session.StartAsync(Handle, cts.Token);
        }
        catch (Exception ex)
        {
            Console.WriteLine();
            Console.WriteLine(Classify(ex));
            for (Exception? e = ex; e is not null; e = e.InnerException)
            {
                Console.WriteLine($"    {e.GetType().Name}: {e.Message}");
            }
            return 1;
        }
        Console.WriteLine($"  [{elapsed.ElapsedMilliseconds,6}ms] ✅ CONNECTED, AUTHENTICATED, SUBSCRIBE GRANTED");
        Console.WriteLine("        the auth callout accepted a real credential presented by this SDK");
        Console.WriteLine();

        Console.WriteLine($"[3/4] PUBLISH   {Metric} = {LowFuelPercent.ToString(CultureInfo.InvariantCulture)}, credential in the body");
        var publisher = new DeviceEventPublisher(new MqttDeviceEventCarrier(session, deviceToken));
        try
        {
            await publisher.EmitMeasurementsAsync(
                deviceToken, credentialId,
                new Dictionary<string, double> { [Metric] = LowFuelPercent },
                cancellationToken: cts.Token);
        }
        catch (Exception ex)
        {
            Console.WriteLine($"  ❌ PUBLISH FAILED  {ex.GetType().Name}: {ex.Message}");
            return 1;
        }
        Console.WriteLine($"  [{elapsed.ElapsedMilliseconds,6}ms] ✅ PUBACKed by the broker");
        Console.WriteLine();

        if (waitSeconds <= 0)
        {
            Console.WriteLine("[4/4] SKIPPED   waitSeconds = 0");
            Console.WriteLine();
            Console.WriteLine("Stages 1-3 passed: this SDK authenticated against the real callout and its telemetry landed.");
            return 0;
        }

        Console.WriteLine($"[4/4] AWAIT     the platform's own sendCommand, up to {waitSeconds}s");
        Console.WriteLine("        DETECT evaluates on a sweep and dispatch polls on another,");
        Console.WriteLine("        so the honest expectation here is seconds, not milliseconds.");
        Task finished = await Task.WhenAny(commandSeen.Task, Task.Delay(TimeSpan.FromSeconds(waitSeconds), cts.Token));
        if (finished != commandSeen.Task)
        {
            Console.WriteLine();
            Console.WriteLine($"  ⏳ NO COMMAND within {waitSeconds}s — reported as its own outcome, not a failure.");
            Console.WriteLine("     Stages 1-3 still passed, so the SDK and the device plane are fine.");
            Console.WriteLine("     Look at the DETECT rule, the alarm, and the command row before suspecting either:");
            Console.WriteLine($"     an instance with no rule for {Metric} has nothing to send, and a rule already");
            Console.WriteLine("     latched by an earlier crossing fires once and not again until it re-arms.");
            return 3;
        }

        // The response is published from the handler's continuation; give it room to reach the
        // broker before the session is disposed out from under it.
        await Task.Delay(TimeSpan.FromSeconds(2), cts.Token);
        Console.WriteLine($"  [{elapsed.ElapsedMilliseconds,6}ms] ✅ command answered");
        Console.WriteLine();
        Console.WriteLine("ALL FOUR STAGES PASSED — the loop closed through the platform.");
        Console.WriteLine("🔴 Confirm the platform AGREES before believing it: the command row must read");
        Console.WriteLine("   SUCCESSFUL with a respondedTime. This rig reports what the DEVICE did; a");
        Console.WriteLine("   response the platform rejects leaves the row in SENT and looks identical here.");
        return 0;
    }

    // Classify on the exception TYPE CHAIN. An AuthenticationException anywhere in it means the
    // TLS handshake itself failed; its absence means the handshake completed and we got far enough
    // to be answered -- which makes it an auth or grant problem, an entirely different fix.
    private static string Classify(Exception ex)
    {
        for (Exception? e = ex; e is not null; e = e.InnerException)
        {
            if (e is AuthenticationException)
            {
                return "❌ TLS HANDSHAKE FAILED — the pinned CA did not accept the broker's chain.\n" +
                       "   Not an auth problem. Check the CA, and check the host is `localhost` and not\n" +
                       "   an IP literal: the broker leaf carries no IP SAN, so 127.0.0.1 is a name mismatch.";
            }
        }
        return "❌ REFUSED AT CONNECT OR SUBSCRIBE — TLS completed, so the chain is fine.\n" +
               "   This is the auth callout rejecting the credential, or the minted grant refusing the\n" +
               "   subscribe. Every callout failure returns the same opaque message by design, so\n" +
               "   diagnose from the device-management logs, not from this client.";
    }
}
