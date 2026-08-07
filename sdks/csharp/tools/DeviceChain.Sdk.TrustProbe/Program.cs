// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A manual rig that drives the SHIPPED MQTT transport's TLS trust decision against a REAL broker.
//
// ── why it exists ────────────────────────────────────────────────────────────────────────────
// MqttTrust.PinnedCa is the only security decision the SDK makes on its own, and it is hand-built
// because X509ChainPolicy.CustomTrustStore does not exist on netstandard2.1. Nothing in CI closes
// the loop on it:
//
//   - the unit tests call the validation callback directly with synthesized chains, which is not
//     the same as a live SslStream invoking it mid-handshake; and
//   - the real-broker test rung runs a PLAINTEXT nats-server, so it never negotiates TLS at all.
//
// So the pinned path had production as its only exerciser. This rig is the missing step, and it is
// also the cheapest pre-flight for the Unity player check: if the transport cannot pin here, on a
// desktop with a debugger, there is no point building a player.
//
// ── the rule it follows ──────────────────────────────────────────────────────────────────────
// A check is worth nothing until it has been shown to fail — the same rule hack/ha-rig.sh and
// hack/dr-rig.sh carry. "The pin accepted the broker" is evidence only alongside a case where the
// pin REFUSES, or `return true` would score identically. Hence four cases, not one: A asserts the
// right chain is accepted, C asserts a wrong one is not, and both cross the same live handshake.
//
// ── running it ───────────────────────────────────────────────────────────────────────────────
//   kubectl -n dc-system get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.pem
//   dotnet run --project sdks/csharp/tools/DeviceChain.Sdk.TrustProbe -- ca.pem
//
// It provisions nothing and mutates nothing: it connects with a deliberately bogus credential, so
// the best case is a TLS handshake followed by the broker's own refusal. Exit code 0 = all four
// cases behaved as predicted.

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Security.Authentication;
using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sdk.Transport;

namespace DeviceChain.Sdk.TrustProbe;

internal static class Program
{
    private const string Instance = "devicechain";
    private const string Tenant = "probe-tenant";
    private const string Device = "probe-device";

    // Deliberately not a real credential. The broker must refuse it, which is what makes case A's
    // "TLS succeeded" reading trustworthy: the run gets far enough to be told no.
    private const string BogusCredential = "0000000000000000000000000000dead";

    private static async Task<int> Main(string[] args)
    {
        if (args.Length < 1)
        {
            Console.Error.WriteLine(
                "usage: DeviceChain.Sdk.TrustProbe <ca.pem> [broker-host]\n" +
                "  extract the CA with:\n" +
                "    kubectl -n dc-system get secret dc-nats-tls -o jsonpath='{.data.ca\\.crt}' | base64 -d > ca.pem");
            return 2;
        }

        var pinnedCa = File.ReadAllBytes(args[0]);
        var host = args.Length > 1 ? args[1] : "localhost";
        Console.WriteLine($"pinned CA: {args[0]} ({pinnedCa.Length} bytes); broker host: {host}\n");

        var results = new List<Result>();

        // A: the positive case. PinnedCa must complete the handshake, leaving only the credential
        // to be refused.
        results.Add(await ProbeAsync(
            "A  <host> + correct pinned CA",
            "TLS SUCCEEDS, then the broker refuses the bogus credential",
            $"ssl://{host}:1883", MqttTrust.PinnedCa(pinnedCa), expectTlsFailure: false));

        // A': proves A's failure was the credential and not a quietly-failed handshake. Without
        // this, "refused" and "never got there" are indistinguishable from the outside.
        results.Add(await ProbeAsync(
            "A' <host> + accept-any (control for A)",
            "the SAME refusal as A — so A's failure was auth, not TLS",
            $"ssl://{host}:1883", MqttTrust.DangerouslyAcceptAnyServerCertificate(), expectTlsFailure: false));

        // B: an IP literal is matched only against IP SANs, and the bring-up's leaf carries DNS
        // SANs only (including `localhost`). This is why the Unity runbook says localhost.
        results.Add(await ProbeAsync(
            "B  127.0.0.1 + correct pinned CA",
            "TLS FAILS — the leaf has no IP SAN, so an IP literal is a name mismatch",
            "ssl://127.0.0.1:1883", MqttTrust.PinnedCa(pinnedCa), expectTlsFailure: true));

        // C: the mutation. A pin that accepts a chain it was never given the root for is not a pin.
        results.Add(await ProbeAsync(
            "C  <host> + WRONG pinned CA (control for A)",
            "TLS FAILS — proves the pin is not vacuously accepting",
            $"ssl://{host}:1883", MqttTrust.PinnedCa(UnrelatedCa()), expectTlsFailure: true));

        Console.WriteLine("\n────────────────────────── SUMMARY ──────────────────────────");
        foreach (var r in results)
        {
            Console.WriteLine($"{(r.Pass ? "PASS" : "FAIL")}  {r.Name}\n      expected: {r.Expected}\n      observed: {r.Observed}");
        }

        var failed = results.Count(r => !r.Pass);
        Console.WriteLine($"\n{results.Count - failed}/{results.Count} as expected");
        return failed == 0 ? 0 : 1;
    }

    private static async Task<Result> ProbeAsync(
        string name, string expected, string uri, MqttTrust trust, bool expectTlsFailure)
    {
        Console.WriteLine($"── {name}");

        var options = new MqttConnectOptions(new Uri(uri), DevicePlane.DeviceClientId(Instance, Tenant, Device))
        {
            Username = DevicePlane.ConnectUsername(Tenant, BogusCredential),
            Password = string.Empty,
            CleanSession = false,
            Trust = trust,
        };

        string observed;
        bool tlsFailed;
        var connection = new MqttNetClientFactory().Create();
        try
        {
            using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(15));
            await connection.ConnectAsync(options, cts.Token).ConfigureAwait(false);

            // Reachable only if the broker accepted BogusCredential, which would itself be a
            // finding worth stopping for.
            observed = "CONNECTED — the broker accepted a credential that does not exist";
            tlsFailed = false;
        }
        catch (Exception ex)
        {
            // 🔑 CLASSIFY ON THE EXCEPTION TYPE, NOT THE MESSAGE TEXT. The first version of this
            // matched the substring "SSL" and was fooled by the `ssl://` scheme in the URI it
            // prints, reporting a clean CONNACK refusal as a handshake failure — 2 of 4 cases
            // scored wrong because the INSTRUMENT was broken, not the subject. A handshake failure
            // always carries an AuthenticationException; a CONNACK refusal never does.
            tlsFailed = Unwind(ex).Any(e => e is AuthenticationException);
            observed = (tlsFailed ? "TLS FAILED: " : "TLS OK, then refused at CONNACK: ") + Flatten(ex);
        }
        finally
        {
            try
            {
                await connection.DisposeAsync().ConfigureAwait(false);
            }
            catch (Exception)
            {
                // Teardown of an already-broken connection is not a result.
            }
        }

        Console.WriteLine($"   {observed}\n");
        return new Result(name, expected, observed, tlsFailed == expectTlsFailure);
    }

    /// <summary>A real, well-formed CA that is simply not the broker's.</summary>
    private static byte[] UnrelatedCa()
    {
        using var key = RSA.Create(2048);
        var request = new CertificateRequest(
            "CN=not-the-broker-ca", key, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1);
        request.CertificateExtensions.Add(new X509BasicConstraintsExtension(true, false, 0, true));
        using var certificate = request.CreateSelfSigned(
            DateTimeOffset.UtcNow.AddDays(-1), DateTimeOffset.UtcNow.AddDays(365));
        return certificate.RawData;
    }

    private static IEnumerable<Exception> Unwind(Exception ex)
    {
        for (Exception? e = ex; e != null; e = e.InnerException)
        {
            yield return e;
        }
    }

    private static string Flatten(Exception ex) =>
        string.Join(" <- ", Unwind(ex).Select(e => $"{e.GetType().Name}: {e.Message}"));

    private sealed record Result(string Name, string Expected, string Observed, bool Pass);
}
