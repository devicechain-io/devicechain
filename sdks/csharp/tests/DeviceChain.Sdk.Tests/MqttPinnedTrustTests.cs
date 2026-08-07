// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Net.Security;
using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;
using DeviceChain.Sdk.Transport;
using Xunit;

namespace DeviceChain.Sdk.Tests;

// The pinned-root TLS decision, driven with REAL generated certificate chains.
//
// 🔴 THIS EXISTS BECAUSE THE CODE WAS OTHERWISE UNTESTABLE-IN-PRACTICE AND THEREFORE UNTESTED. An
// adversarial pass replaced the whole validation with `return true` and every gate stayed green:
// every broker in every rig speaks plaintext, so the SDK's one security decision had production as
// its only exerciser. Both directions are asserted here — a valid chain must be ACCEPTED and each
// way of being wrong must be REFUSED — because a guard that rejects everything would satisfy the
// refusal half while making the recommended trust mode useless.
public class MqttPinnedTrustTests
{
    // ── the pin accepts what it should ───────────────────────────────────────

    [Fact]
    public void ALeafSignedDirectlyByThePinnedRootIsAccepted()
    {
        using var root = Ca("pinned-root");
        using var leaf = LeafSignedBy(root, "broker.local");

        Assert.True(Validate(leaf, null, root));
    }

    // 🔑 THE INTERMEDIATE CASE — a real bug this test was written to catch, not a hypothetical.
    // Building the chain from the leaf alone stopped at PartialChain whenever the broker's
    // certificate was issued by an intermediate under the pinned root (ordinary CA hygiene), so
    // the terminal element was the LEAF and a perfectly valid deployment got an opaque connect
    // failure. The peer-supplied intermediates now go into the extra store.
    [Fact]
    public void ALeafBehindAnIntermediateUnderThePinnedRootIsAccepted()
    {
        using var root = Ca("pinned-root");
        using var intermediate = IntermediateSignedBy(root, "pinned-intermediate");
        using var leaf = LeafSignedBy(intermediate, "broker.local");

        using var peerChain = ChainOffering(intermediate);
        Assert.True(Validate(leaf, peerChain, root));
    }

    // ── and refuses every way of being wrong ─────────────────────────────────

    [Fact]
    public void ALeafSignedByADifferentRootIsRefused()
    {
        using var pinned = Ca("pinned-root");
        using var impostor = Ca("impostor-root");
        using var leaf = LeafSignedBy(impostor, "broker.local");

        Assert.False(Validate(leaf, null, pinned));
    }

    [Fact]
    public void ASelfSignedCertificateThatIsNotThePinIsRefused()
    {
        using var pinned = Ca("pinned-root");
        using var selfSigned = Ca("self-signed-broker");

        Assert.False(Validate(selfSigned, null, pinned));
    }

    // Pinning replaces the trust anchor, not the rest of validation: a hostname mismatch is fatal
    // no matter who signed the certificate.
    [Fact]
    public void ANameMismatchIsRefusedEvenUnderThePinnedRoot()
    {
        using var root = Ca("pinned-root");
        using var leaf = LeafSignedBy(root, "broker.local");

        Assert.False(Validate(leaf, null, root, SslPolicyErrors.RemoteCertificateNameMismatch));
    }

    [Fact]
    public void AnAbsentCertificateIsRefused()
    {
        using var root = Ca("pinned-root");
        using var leaf = LeafSignedBy(root, "broker.local");

        Assert.False(Validate(leaf, null, root, SslPolicyErrors.RemoteCertificateNotAvailable));
    }

    // An expired certificate is a real chain failure, distinct from the untrusted-root status that
    // private pinning deliberately tolerates.
    //
    // 🔑 WHAT ACTUALLY REFUSES IT IS `chain.Build()` RETURNING FALSE, NOT THE CHAIN-STATUS LOOP —
    // measured, because the obvious assumption was wrong: narrowing that loop to reject only
    // `Revoked` leaves this test passing. So the loop is belt-and-braces that this suite does not
    // independently pin, and saying so is better than implying coverage the suite does not have.
    [Fact]
    public void AnExpiredLeafIsRefusedEvenUnderThePinnedRoot()
    {
        using var root = Ca("pinned-root");
        using var expired = LeafSignedBy(
            root, "broker.local", notBefore: DateTimeOffset.UtcNow.AddDays(-10),
            notAfter: DateTimeOffset.UtcNow.AddDays(-1));

        Assert.False(Validate(expired, null, root));
    }

    // 🔑 THE ATTACKER-SUPPLIED-INTERMEDIATE CASE. The peer chooses what intermediates it offers, so
    // stuffing them into the extra store must NOT let a chain that terminates somewhere other than
    // the pin succeed. It only ever helps a chain BUILD; it still has to end at the pin.
    [Fact]
    public void AnAttackerSuppliedIntermediateCannotRedirectTheChainAwayFromThePin()
    {
        using var pinned = Ca("pinned-root");
        using var impostorRoot = Ca("impostor-root");
        using var impostorIntermediate = IntermediateSignedBy(impostorRoot, "impostor-intermediate");
        using var leaf = LeafSignedBy(impostorIntermediate, "broker.local");

        // The peer offers a complete, internally-valid chain — to the wrong root.
        using var peerChain = ChainOffering(impostorIntermediate, impostorRoot);
        Assert.False(Validate(leaf, peerChain, pinned));
    }

    // ── the trust object's own contract ──────────────────────────────────────

    [Fact]
    public void SystemRootsIsTheDefaultTrust()
    {
        Assert.Equal(MqttTrustMode.SystemRoots, new MqttConnectOptions(new Uri("ssl://broker:8883"), "id").Trust.Mode);
        Assert.Equal(MqttTrustMode.SystemRoots, MqttTrust.SystemRoots.Mode);
    }

    [Fact]
    public void AcceptAnyIsReachableOnlyThroughItsExplicitlyNamedFactory()
    {
        Assert.Equal(MqttTrustMode.AcceptAny, MqttTrust.DangerouslyAcceptAnyServerCertificate().Mode);
        Assert.Equal(MqttTrustMode.PinnedCa, MqttTrust.PinnedCa(new byte[] { 1, 2, 3 }).Mode);
        Assert.Throws<ArgumentException>(() => MqttTrust.PinnedCa(Array.Empty<byte>()));
    }

    // A pinned CA must be accepted in either encoding a user is likely to hold.
    [Fact]
    public void APinnedCaLoadsFromBothDerAndPem()
    {
        using var root = Ca("pinned-root");
        using var leaf = LeafSignedBy(root, "broker.local");

        using var fromDer = new X509Certificate2(MqttTrust.PinnedCa(root.RawData).CaCertificate!);
        Assert.True(Validate(leaf, null, fromDer));

        var pem = System.Text.Encoding.ASCII.GetBytes(
            "-----BEGIN CERTIFICATE-----\n" +
            Convert.ToBase64String(root.RawData, Base64FormattingOptions.InsertLineBreaks) +
            "\n-----END CERTIFICATE-----\n");
        using var fromPem = new X509Certificate2(MqttTrust.PinnedCa(pem).CaCertificate!);
        Assert.True(Validate(leaf, null, fromPem));
    }

    // ── helpers ──────────────────────────────────────────────────────────────

    private static bool Validate(
        X509Certificate2 leaf, X509Chain? peerChain, X509Certificate2 pinned,
        SslPolicyErrors errors = SslPolicyErrors.RemoteCertificateChainErrors) =>
        MqttNetConnection.IsChainPinnedTo(leaf, peerChain, errors, pinned);

    private static X509Certificate2 Ca(string name)
    {
        using var key = RSA.Create(2048);
        var request = new CertificateRequest($"CN={name}", key, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1);
        request.CertificateExtensions.Add(new X509BasicConstraintsExtension(true, false, 0, true));
        request.CertificateExtensions.Add(
            new X509KeyUsageExtension(X509KeyUsageFlags.KeyCertSign | X509KeyUsageFlags.CrlSign, true));
        return request.CreateSelfSigned(DateTimeOffset.UtcNow.AddDays(-30), DateTimeOffset.UtcNow.AddDays(365));
    }

    private static X509Certificate2 IntermediateSignedBy(X509Certificate2 issuer, string name)
    {
        using var key = RSA.Create(2048);
        var request = new CertificateRequest($"CN={name}", key, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1);
        request.CertificateExtensions.Add(new X509BasicConstraintsExtension(true, false, 0, true));
        request.CertificateExtensions.Add(
            new X509KeyUsageExtension(X509KeyUsageFlags.KeyCertSign | X509KeyUsageFlags.CrlSign, true));
        using var signed = Sign(request, issuer, DateTimeOffset.UtcNow.AddDays(-20), DateTimeOffset.UtcNow.AddDays(180));
        // Create() yields a certificate with NO private key, which cannot then sign a leaf.
        return signed.CopyWithPrivateKey(key);
    }

    private static X509Certificate2 LeafSignedBy(
        X509Certificate2 issuer, string name, DateTimeOffset? notBefore = null, DateTimeOffset? notAfter = null)
    {
        using var key = RSA.Create(2048);
        var request = new CertificateRequest($"CN={name}", key, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1);
        request.CertificateExtensions.Add(new X509BasicConstraintsExtension(false, false, 0, true));
        return Sign(
            request, issuer,
            notBefore ?? DateTimeOffset.UtcNow.AddDays(-1),
            notAfter ?? DateTimeOffset.UtcNow.AddDays(90));
    }

    private static X509Certificate2 Sign(
        CertificateRequest request, X509Certificate2 issuer, DateTimeOffset notBefore, DateTimeOffset notAfter)
    {
        var serial = new byte[16];
        RandomNumberGenerator.Fill(serial);
        // Keep the notAfter inside the issuer's own validity, or the chain fails for that reason
        // instead of the one under test.
        var boundedAfter = notAfter > issuer.NotAfter ? new DateTimeOffset(issuer.NotAfter) : notAfter;
        var boundedBefore = notBefore < issuer.NotBefore ? new DateTimeOffset(issuer.NotBefore) : notBefore;
        return request.Create(issuer, boundedBefore, boundedAfter, serial);
    }

    // A chain object carrying the intermediates a peer would offer during a handshake.
    private static X509Chain ChainOffering(params X509Certificate2[] offered)
    {
        var chain = new X509Chain();
        chain.ChainPolicy.RevocationMode = X509RevocationMode.NoCheck;
        chain.ChainPolicy.VerificationFlags = X509VerificationFlags.AllowUnknownCertificateAuthority;
        foreach (var certificate in offered)
        {
            chain.ChainPolicy.ExtraStore.Add(certificate);
        }

        // Build it so ChainElements is populated the way a real handshake's chain would be.
        chain.Build(offered[0]);
        return chain;
    }
}
