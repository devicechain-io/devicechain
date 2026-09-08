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

        byte[] pinnedCa;
        try
        {
            pinnedCa = File.ReadAllBytes(args[0]);
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or ArgumentException)
        {
            // A stack trace here reads as a defect in the rig rather than a mistyped path.
            Console.Error.WriteLine($"cannot read the pinned CA at \"{args[0]}\": {ex.Message}");
            return 2;
        }

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
            // Refused rather than clamped: a negative value went on to build a CancellationTokenSource
            // that was already cancelled (or threw outright), and the resulting cancellation was then
            // reported as a broker refusal -- a mistyped argument wearing a platform failure's clothes.
            if (waitSeconds < 0)
            {
                Console.Error.WriteLine($"waitSeconds cannot be negative, got {waitSeconds}; use 0 to skip stage 4");
                return 2;
            }
        }

        // DevicePlane throws on a token that cannot form a client id. Catching it here distinguishes
        // "you typed the identifier wrong" from "the platform said no", which otherwise both surface
        // as a crash before any stage has run.
        string clientId;
        try
        {
            clientId = DevicePlane.DeviceClientId(instanceId, tenant, deviceToken);
        }
        catch (ArgumentException ex)
        {
            Console.Error.WriteLine($"cannot build a client id from ({instanceId}, {tenant}, {deviceToken}): {ex.Message}");
            return 2;
        }

        var brokerUri = new Uri($"ssl://{host}:1883");
        Console.WriteLine($"broker      : {brokerUri}");
        Console.WriteLine($"pinned CA   : {args[0]} ({pinnedCa.Length} bytes)");
        Console.WriteLine($"client id   : {clientId}");
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

        // Every terminal path routes through this, so no run can report a pass without the control
        // having held. It returns the caller's code when the bogus credential was refused, and 1 when
        // it was ACCEPTED -- because at that point the run proves nothing about authentication and
        // reporting it as a pass would be worse than reporting nothing.
        async Task<int> FinishAsync(int codeIfControlHolds)
        {
            Console.WriteLine("[control] the same connect with ONE character changed in the credential");
            bool refused;
            try
            {
                refused = await RefusesABogusCredentialAsync(
                    brokerUri, pinnedCa, instanceId, tenant, deviceToken, credentialId, cts.Token);
            }
            catch (Exception ex)
            {
                Console.WriteLine($"  ⚠️  control could not be run ({ex.GetType().Name}: {ex.Message}).");
                Console.WriteLine("     Treating that as a FAILED control: an unrun control is not a passed one.");
                return 1;
            }

            if (!refused)
            {
                Console.WriteLine("  ❌ THE BROKER ACCEPTED A CREDENTIAL THAT DOES NOT EXIST.");
                Console.WriteLine("     Everything above is therefore vacuous -- it would score identically");
                Console.WriteLine("     against a broker with no auth callout configured at all, which is");
                Console.WriteLine("     exactly what the SDK's own test rig runs. Check that the callout is");
                Console.WriteLine("     wired and that device-management is answering it.");
                return 1;
            }

            Console.WriteLine("  ✅ refused, so the acceptance above was earned and not vacuous");
            Console.WriteLine();
            return codeIfControlHolds;
        }

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
            Console.WriteLine("Stages 1-3 passed: this SDK authenticated against the real callout and the broker");
            Console.WriteLine("PUBACKed its measurement. 🔑 A PUBACK is the BROKER taking ownership, not the platform");
            Console.WriteLine("accepting the event -- decode, device attribution and the ingest gate all run after it,");
            Console.WriteLine("and a rejection there is invisible on MQTT. Only stage 4 firing proves the event landed.");
            Console.WriteLine();
            return await FinishAsync(0);
        }

        Console.WriteLine($"[4/4] AWAIT     the platform's own sendCommand, up to {waitSeconds}s");
        Console.WriteLine("        DETECT evaluates on a sweep and dispatch polls on another,");
        Console.WriteLine("        so the honest expectation here is seconds, not milliseconds.");
        Task finished = await Task.WhenAny(commandSeen.Task, Task.Delay(TimeSpan.FromSeconds(waitSeconds), cts.Token));
        if (finished != commandSeen.Task)
        {
            Console.WriteLine();
            Console.WriteLine($"  ⏳ NO COMMAND within {waitSeconds}s — reported as its own outcome, not a failure.");
            Console.WriteLine("     Stages 1-3 still passed, so the connect, the grant and the PUBACK are fine.");
            Console.WriteLine("     Look at the DETECT rule, the alarm, and the command row before suspecting either:");
            Console.WriteLine($"     an instance with no rule for {Metric} has nothing to send, and a rule already");
            Console.WriteLine("     latched by an earlier crossing fires once and not again until it re-arms.");
            Console.WriteLine("     A command key the profile does not declare also lands here: the enqueue");
            Console.WriteLine("     gate rejects it and REACT dead-letters, so nothing ever reaches the device.");
            Console.WriteLine();
            return await FinishAsync(3);
        }

        // The response is published from the handler's continuation. Give it room to reach the broker
        // before `await using` disposes the session, because DisposeAsync cancels the very token the
        // response publish runs under.
        //
        // 🔴 THIS DELAY IS NOT AN OBSERVATION, AND THE BANNER BELOW SAYS SO. PublishResponseAsync
        // returns silently when the connection is gone and CATCHES EVERY EXCEPTION from the QoS-1
        // publish (by design -- the broker redelivers the command and the cached outcome answers it).
        // So from here a response that was PUBACKed, one that failed, and one that was never attempted
        // are indistinguishable. Claiming "answered" would be the rig reporting a pass it did not earn,
        // which is the exact defect it exists to catch elsewhere.
        await Task.Delay(TimeSpan.FromSeconds(5), cts.Token);
        Console.WriteLine($"  [{elapsed.ElapsedMilliseconds,6}ms] ✅ command RECEIVED and the handler answered it");
        Console.WriteLine();
        Console.WriteLine("STAGES 1-4 PASSED at the device: connected, granted, published, and a command arrived.");
        Console.WriteLine();
        Console.WriteLine("🔴 THE LOOP IS NOT CLOSED UNTIL THE PLATFORM SAYS SO, AND THIS RIG CANNOT SEE THAT.");
        Console.WriteLine("   The SDK publishes the response fire-and-forget, so a response that never left,");
        Console.WriteLine("   one the platform refused, and one it accepted all look identical from here.");
        Console.WriteLine("   Read the command row: it must be SUCCESSFUL and carry a respondedTime.");
        Console.WriteLine("   Anything else -- still SENT, or FAILED -- means the answer did not land, and");
        Console.WriteLine("   the device end of this run was fine regardless.");
        Console.WriteLine();
        return await FinishAsync(0);
    }

    // The negative control, run IN PROCESS after the positive so a lockout cannot poison it.
    //
    // 🔴 WITHOUT THIS THE WHOLE RIG IS UNFALSIFIABLE. Stage 1 asks only for a Success CONNACK, and an
    // authless broker -- which is exactly what the SDK's own real-broker test rung runs -- answers
    // Success to anything at all. So a green run against a misconfigured cluster would print "the auth
    // callout accepted a real credential" when there was no callout to accept it. TrustProbe carries
    // its control inside the run for the same reason (its case C, the wrong CA); a control described in
    // a runbook is a control most people skip.
    //
    // The discriminator is one mutated character, so everything else -- host, CA, client id grammar,
    // topic shape -- is held identical and only the credential differs. A distinct client-id
    // discriminator keeps it from colliding with the real session.
    private static async Task<bool> RefusesABogusCredentialAsync(
        Uri brokerUri, byte[] pinnedCa, string instanceId, string tenant,
        string deviceToken, string credentialId, CancellationToken cancellationToken)
    {
        string bogus = MutateLastCharacter(credentialId);
        var options = new MqttSessionOptions(brokerUri, instanceId, tenant, deviceToken, bogus)
        {
            Trust = MqttTrust.PinnedCa(pinnedCa),
            ClientIdDiscriminator = "authprobe-control",
        };

        await using var session = new MqttDeviceSession(options);
        try
        {
            await session.StartAsync((_, _) => Task.FromResult(CommandOutcome.Succeeded()), cancellationToken);
        }
        catch (MqttConnectionException)
        {
            return true;
        }
        return false;
    }

    // Flip the last character within its own alphabet, so the result stays the same length and shape
    // as a real credential id and can only differ in VALUE. A shorter or malformed string risks being
    // refused by a length or grammar check before the credential is ever looked up, which would make
    // the control pass for the wrong reason.
    private static string MutateLastCharacter(string credentialId)
    {
        if (credentialId.Length == 0)
        {
            return "0";
        }
        char last = credentialId[^1];
        char replacement = last switch
        {
            >= '0' and <= '8' => (char)(last + 1),
            '9' => '0',
            >= 'a' and <= 'e' => (char)(last + 1),
            'f' => 'a',
            _ => last == 'z' ? 'y' : (char)(last + 1),
        };
        return credentialId[..^1] + replacement;
    }

    // Classify on the EXCEPTION TYPE, using the vocabulary the SDK already types. An earlier
    // version of this had two buckets -- "TLS failed" and "everything else is auth" -- which
    // misreported the three commonest local failures. A connection refused (no port-forward), a
    // DNS failure and a black-holed port all landed in the auth bucket and printed "TLS completed"
    // for a handshake that never started, sending the reader to device-management's logs for a
    // problem that never reached the cluster. That is the same defect as the classifier that
    // matched the substring "SSL" -- a wrong instrument, one layer up.
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

        return ex switch
        {
            MqttConnectRefusedException =>
                "❌ REFUSED AT CONNACK — TLS completed, so the chain is fine. The auth callout rejected\n" +
                "   the credential, or refused the client id. Every callout failure returns the same\n" +
                "   opaque message by design, so diagnose from the device-management logs.\n" +
                "   🔑 A common cause is passing the device's externalId where its addressing TOKEN is\n" +
                "      wanted: both are grammar-valid, so the client id builds and the callout refuses it.",

            MqttSubscribeRefusedException =>
                "❌ SUBSCRIBE REFUSED — connected and authenticated, but the minted grant does not cover\n" +
                "   the command topic. This is the grant, not the credential.",

            OperationCanceledException =>
                "❌ NO ANSWER within the operation timeout — nothing refused us, nothing answered.\n" +
                "   A black-holed port or a broker that is not listening looks like this.",

            MqttConnectionException =>
                "❌ COULD NOT REACH THE BROKER — the connection never got far enough to be refused.\n" +
                "   Connection refused, DNS, or no route. Check the port is published:\n" +
                "     docker inspect <instance>-control-plane --format '{{json .NetworkSettings.Ports}}' | grep 31883\n" +
                "   🔑 kind fixes port mappings at CREATE time — a cluster predating the map shows a\n" +
                "      perfect Service and a dead route.",

            _ => "❌ FAILED before the device plane answered — see the exception chain below.",
        };
    }
}
