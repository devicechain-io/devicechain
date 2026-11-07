// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using System.Net;
using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sdk.Transport;
using Xunit;

namespace DeviceChain.Sdk.Tests;

// X.509 client-certificate authentication: the loaders' checks, and the wire.
//
// 🔴 THE WIRE TESTS RUN A REAL TLS HANDSHAKE against the scripted broker, which asks for a client
// certificate and records what arrived. Loader unit tests alone would pass with a connection that
// never presented the certificate, and a password-sending bug would be invisible: the platform
// refuses a certificate connect that carries a password, so the CONNECT's username and the ABSENCE
// of a password are asserted from the broker's side, not inferred from the options.
public class MqttClientCertificateTests
{
    private static readonly TimeSpan Timeout = TimeSpan.FromSeconds(20);

    // ── loaders accept what they should ──────────────────────────────────────

    [Theory]
    [InlineData(false)]
    [InlineData(true)]
    public void APfxLoadsWithItsKey(bool ec)
    {
        using var ca = Ca();
        using var leaf = Leaf(ca, "SN-000123", ec, out _);

        var pfx = leaf.Export(X509ContentType.Pfx, "pw");
        using var loaded = MqttClientCertificate.FromPfx(pfx, "pw");

        Assert.True(loaded.Certificate.HasPrivateKey);
        Assert.Contains("SN-000123", loaded.Subject);
        Assert.Equal(leaf.NotAfter, loaded.NotAfter.UtcDateTime.ToLocalTime(), TimeSpan.FromSeconds(1));
    }

    [Theory]
    [InlineData("pkcs8", false)]
    [InlineData("pkcs8", true)]
    [InlineData("native", false)]   // PKCS#1 RSA PRIVATE KEY
    [InlineData("native", true)]    // SEC1 EC PRIVATE KEY
    [InlineData("encrypted", false)]
    [InlineData("encrypted", true)]
    public void APemCertificateAndKeyLoadInEveryCommonKeyEncoding(string encoding, bool ec)
    {
        using var ca = Ca();
        using var leaf = Leaf(ca, "SN-000123", ec, out var keyPems);

        using var loaded = MqttClientCertificate.FromPem(
            CertPem(leaf), keyPems[encoding], encoding == "encrypted" ? "s3cret" : null);

        Assert.True(loaded.Certificate.HasPrivateKey);
        // The key really signs: a loaded certificate whose key is a stub would fail here.
        if (ec)
        {
            using var key = loaded.Certificate.GetECDsaPrivateKey()!;
            Assert.True(key.VerifyData(new byte[] { 1 }, key.SignData(new byte[] { 1 }, HashAlgorithmName.SHA256), HashAlgorithmName.SHA256));
        }
        else
        {
            using var key = loaded.Certificate.GetRSAPrivateKey()!;
            Assert.True(key.VerifyData(new byte[] { 1 }, key.SignData(new byte[] { 1 }, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1), HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1));
        }
    }

    [Fact]
    public void PemFilesLoadFromDisk()
    {
        using var ca = Ca();
        using var leaf = Leaf(ca, "SN-000123", true, out var keyPems);
        var dir = Directory.CreateDirectory(Path.Combine(Path.GetTempPath(), "dcx509-" + Guid.NewGuid().ToString("N")));
        try
        {
            var crt = Path.Combine(dir.FullName, "device.crt");
            var key = Path.Combine(dir.FullName, "device.key");
            File.WriteAllText(crt, CertPem(leaf));
            File.WriteAllText(key, keyPems["pkcs8"]);

            using var loaded = MqttClientCertificate.FromPemFiles(crt, key);
            Assert.True(loaded.Certificate.HasPrivateKey);

            var pfxPath = Path.Combine(dir.FullName, "device.pfx");
            File.WriteAllBytes(pfxPath, leaf.Export(X509ContentType.Pfx, "pw"));
            using var fromPfx = MqttClientCertificate.FromPfxFile(pfxPath, "pw");
            Assert.True(fromPfx.Certificate.HasPrivateKey);
        }
        finally
        {
            dir.Delete(true);
        }
    }

    // ── and refuses each way of being wrong, by name ─────────────────────────

    [Fact]
    public void AWrongPfxPasswordIsNamed()
    {
        using var ca = Ca();
        using var leaf = Leaf(ca, "d", true, out _);
        var ex = Assert.Throws<MqttClientCertificateException>(
            () => MqttClientCertificate.FromPfx(leaf.Export(X509ContentType.Pfx, "right"), "wrong"));
        Assert.Contains("password", ex.Message);
    }

    [Fact]
    public void ACertificateWithoutItsKeyIsRefused()
    {
        using var ca = Ca();
        using var leaf = Leaf(ca, "d", true, out _);
        var ex = Assert.Throws<MqttClientCertificateException>(
            () => MqttClientCertificate.FromPfx(leaf.Export(X509ContentType.Cert)));
        Assert.Contains("no private key", ex.Message);

        using var publicOnly = new X509Certificate2(leaf.RawData);
        Assert.Throws<MqttClientCertificateException>(() => MqttClientCertificate.FromCertificate(publicOnly));
    }

    [Fact]
    public void AKeyThatDoesNotBelongToTheCertificateIsRefused()
    {
        using var ca = Ca();
        using var leaf = Leaf(ca, "d", true, out _);
        using var other = Leaf(ca, "other", true, out var otherKeys);

        var ex = Assert.Throws<MqttClientCertificateException>(
            () => MqttClientCertificate.FromPem(CertPem(leaf), otherKeys["pkcs8"]));
        Assert.Contains("does not belong", ex.Message);
    }

    // An empty password is a valid password: it must not read as "none supplied".
    [Fact]
    public void AnEncryptedKeyWithAnEmptyPasswordLoads()
    {
        using var ca = Ca();
        using var leaf = Leaf(ca, "d", true, out var keys);
        using var ec = leaf.GetECDsaPrivateKey()!;
        var pem = Pem("ENCRYPTED PRIVATE KEY", ec.ExportEncryptedPkcs8PrivateKey(
            "", new PbeParameters(PbeEncryptionAlgorithm.Aes256Cbc, HashAlgorithmName.SHA256, 2000)));

        using var loaded = MqttClientCertificate.FromPem(CertPem(leaf), pem, "");
        Assert.True(loaded.Certificate.HasPrivateKey);
    }

    // A certificate the caller passes in stays theirs after the wrapper is disposed.
    [Fact]
    public void DisposingTheWrapperDoesNotDisposeABorrowedCertificate()
    {
        using var ca = Ca();
        using var leaf = Leaf(ca, "d", true, out _);
        MqttClientCertificate.FromCertificate(leaf).Dispose();
        Assert.True(leaf.HasPrivateKey);
        Assert.NotNull(leaf.GetECDsaPrivateKey());
    }

    [Fact]
    public void AnEncryptedKeyWithoutOrWithTheWrongPasswordIsRefused()
    {
        using var ca = Ca();
        using var leaf = Leaf(ca, "d", true, out var keys);

        var missing = Assert.Throws<MqttClientCertificateException>(
            () => MqttClientCertificate.FromPem(CertPem(leaf), keys["encrypted"]));
        Assert.Contains("password", missing.Message);

        var wrong = Assert.Throws<MqttClientCertificateException>(
            () => MqttClientCertificate.FromPem(CertPem(leaf), keys["encrypted"], "nope"));
        Assert.Contains("password", wrong.Message);
    }

    [Fact]
    public void ACaCertificateIsRefusedAsADeviceCertificate()
    {
        using var ca = Ca();
        var ex = Assert.Throws<MqttClientCertificateException>(() => MqttClientCertificate.FromCertificate(ca));
        Assert.Contains("CA certificate", ex.Message);
    }

    [Fact]
    public void ACertificateWithoutClientAuthUsageIsRefused()
    {
        using var ca = Ca();
        using var serverOnly = Leaf(ca, "srv", true, out _, eku: "1.3.6.1.5.5.7.3.1");
        var ex = Assert.Throws<MqttClientCertificateException>(() => MqttClientCertificate.FromCertificate(serverOnly));
        Assert.Contains("client authentication", ex.Message);
    }

    [Fact]
    public void MalformedAndMisplacedPemIsRefusedWithAReason()
    {
        using var ca = Ca();
        using var leaf = Leaf(ca, "d", true, out var keys);

        Assert.Contains("empty", Assert.Throws<MqttClientCertificateException>(() => MqttClientCertificate.FromPem("", keys["pkcs8"])).Message);
        Assert.Contains("no CERTIFICATE", Assert.Throws<MqttClientCertificateException>(() => MqttClientCertificate.FromPem("not pem", keys["pkcs8"])).Message);
        Assert.Contains("no PRIVATE KEY", Assert.Throws<MqttClientCertificateException>(() => MqttClientCertificate.FromPem(CertPem(leaf), CertPem(leaf))).Message);
        Assert.Contains("contains a private key", Assert.Throws<MqttClientCertificateException>(() => MqttClientCertificate.FromPem(CertPem(leaf) + keys["pkcs8"], keys["pkcs8"])).Message);
        Assert.Contains("cannot read", Assert.Throws<MqttClientCertificateException>(() => MqttClientCertificate.FromPemFiles("/no/such/crt", "/no/such/key")).Message);
    }

    // ── option wiring ────────────────────────────────────────────────────────

    [Fact]
    public void ACertificateOverAPlaintextBrokerIsRefusedAtSessionConstruction()
    {
        using var ca = Ca();
        using var leaf = Leaf(ca, "d", true, out _);
        using var cert = MqttClientCertificate.FromCertificate(leaf);
        var options = new MqttSessionOptions(new Uri("tcp://127.0.0.1:1883"), "inst", "acme", "d", "cred")
        {
            ClientCertificate = cert,
        };

        var ex = Assert.Throws<ArgumentException>(() => new MqttDeviceSession(options));
        Assert.Contains("ssl://", ex.Message);
    }

    [Fact]
    public async Task TheConnectionRefusesACertificateOverPlaintextToo()
    {
        using var ca = Ca();
        using var leaf = Leaf(ca, "d", true, out _);
        using var cert = MqttClientCertificate.FromCertificate(leaf);
        var options = new MqttConnectOptions(new Uri("tcp://127.0.0.1:1883"), "inst:acme:d") { ClientCertificate = cert };

        await using var connection = new MqttNetClientFactory().Create();
        await Assert.ThrowsAsync<MqttClientCertificateException>(
            () => connection.ConnectAsync(options, CancellationToken.None));
    }

    [Fact]
    public void ThePlatformsCertificateUsernameIsTenantColonAtX509()
    {
        Assert.Equal("acme:@x509", DevicePlane.X509ConnectUsername("acme"));
    }

    [Fact]
    public void PinnedCaLoadsFromAFileAndNamesAMissingOne()
    {
        using var ca = Ca();
        var path = Path.Combine(Path.GetTempPath(), "dcca-" + Guid.NewGuid().ToString("N") + ".crt");
        File.WriteAllText(path, CertPem(ca));
        try
        {
            var trust = MqttTrust.PinnedCaFromFile(path);
            Assert.Equal(MqttTrustMode.PinnedCa, trust.Mode);
        }
        finally
        {
            File.Delete(path);
        }

        Assert.Throws<ArgumentException>(() => MqttTrust.PinnedCaFromFile(path));
    }

    // ── on the wire ──────────────────────────────────────────────────────────

    [Fact]
    public async Task TheConnectionPresentsTheCertificateAndSendsUsernameWithoutAPassword()
    {
        using var ca = Ca();
        using var server = ServerCert(ca);
        using var leaf = Leaf(ca, "SN-000123", true, out _);
        using var cert = MqttClientCertificate.FromPfx(leaf.Export(X509ContentType.Pfx, "pw"), "pw");
        using var broker = new ScriptedMqttBroker(tlsServerCertificate: server);

        var options = new MqttConnectOptions(broker.BrokerUri, "inst:acme:sn-000123")
        {
            Username = DevicePlane.X509ConnectUsername("acme"),
            Password = null,
            ClientCertificate = cert,
            Trust = MqttTrust.PinnedCa(ca.RawData),
        };

        await using var connection = new MqttNetClientFactory().Create();
        using var cts = new CancellationTokenSource(Timeout);
        await connection.ConnectAsync(options, cts.Token);

        Assert.NotNull(broker.PresentedClientCertificate);
        Assert.Equal(leaf.Thumbprint, broker.PresentedClientCertificate!.Thumbprint);
        Assert.Equal("acme:@x509", broker.Username);
        // Null, not empty: an empty password is a password the callout refuses on this username.
        Assert.Null(broker.Password);
    }

    [Fact]
    public async Task ASessionWithACertificateConnectsAsTheCertificateDevice()
    {
        using var ca = Ca();
        using var server = ServerCert(ca);
        using var leaf = Leaf(ca, "SN-000123", false, out _);
        using var cert = MqttClientCertificate.FromCertificate(leaf);
        using var broker = new ScriptedMqttBroker(tlsServerCertificate: server);

        var options = new MqttSessionOptions(broker.BrokerUri, "inst", "acme", "sn-000123", "ignored-for-certificates")
        {
            ClientCertificate = cert,
            Trust = MqttTrust.PinnedCa(ca.RawData),
        };

        await using var session = new MqttDeviceSession(options);
        await session.StartAsync((_, _) => Task.FromResult(CommandOutcome.Succeeded()), new CancellationTokenSource(Timeout).Token);

        Assert.Equal(MqttSessionState.Ready, session.State);
        Assert.Equal("acme:@x509", broker.Username);
        Assert.Null(broker.Password);
        Assert.Equal("inst:acme:sn-000123", broker.ClientId);
        Assert.Equal(leaf.Thumbprint, broker.PresentedClientCertificate!.Thumbprint);
    }

    // The token path must be untouched: same username shape, EMPTY password, no certificate.
    [Fact]
    public async Task ATokenSessionStillSendsItsCredentialAndAnEmptyPassword()
    {
        using var broker = new ScriptedMqttBroker();
        var options = new MqttSessionOptions(broker.BrokerUri, "inst", "acme", "sn-000123", "cred-1");

        await using var session = new MqttDeviceSession(options);
        await session.StartAsync((_, _) => Task.FromResult(CommandOutcome.Succeeded()), new CancellationTokenSource(Timeout).Token);

        Assert.Equal("acme:cred-1", broker.Username);
        Assert.Equal(string.Empty, broker.Password);
    }

    // ── fixtures ─────────────────────────────────────────────────────────────

    private static X509Certificate2 Ca()
    {
        using var key = RSA.Create(2048);
        var request = new CertificateRequest("CN=Test Device CA", key, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1);
        request.CertificateExtensions.Add(new X509BasicConstraintsExtension(true, false, 0, true));
        request.CertificateExtensions.Add(new X509KeyUsageExtension(X509KeyUsageFlags.KeyCertSign | X509KeyUsageFlags.CrlSign, true));
        return request.CreateSelfSigned(DateTimeOffset.UtcNow.AddDays(-1), DateTimeOffset.UtcNow.AddDays(365));
    }

    private static X509Certificate2 ServerCert(X509Certificate2 ca)
    {
        using var key = RSA.Create(2048);
        var request = new CertificateRequest("CN=127.0.0.1", key, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1);
        request.CertificateExtensions.Add(new X509BasicConstraintsExtension(false, false, 0, true));
        var san = new SubjectAlternativeNameBuilder();
        san.AddIpAddress(IPAddress.Loopback);
        request.CertificateExtensions.Add(san.Build());
        request.CertificateExtensions.Add(new X509EnhancedKeyUsageExtension(
            new OidCollection { new Oid("1.3.6.1.5.5.7.3.1") }, false));
        using var signed = Sign(request, ca);
        using var withKey = signed.CopyWithPrivateKey(key);
        // Schannel needs a key it can name; see MqttClientCertificate.FromPem.
        return new X509Certificate2(withKey.Export(X509ContentType.Pfx, "x"), "x");
    }

    // keyPems: the same private key in every encoding a user is likely to hold.
    private static X509Certificate2 Leaf(
        X509Certificate2 ca, string cn, bool ec, out System.Collections.Generic.Dictionary<string, string> keyPems,
        string eku = "1.3.6.1.5.5.7.3.2")
    {
        keyPems = new System.Collections.Generic.Dictionary<string, string>();
        var pbe = new PbeParameters(PbeEncryptionAlgorithm.Aes256Cbc, HashAlgorithmName.SHA256, 2000);
        X509Certificate2 withKey;
        if (ec)
        {
            using var key = ECDsa.Create(ECCurve.NamedCurves.nistP256);
            var request = new CertificateRequest("CN=" + cn, key, HashAlgorithmName.SHA256);
            AddLeafExtensions(request, eku);
            using var signed = Sign(request, ca);
            withKey = signed.CopyWithPrivateKey(key);
            keyPems["pkcs8"] = Pem("PRIVATE KEY", key.ExportPkcs8PrivateKey());
            keyPems["native"] = Pem("EC PRIVATE KEY", key.ExportECPrivateKey());
            keyPems["encrypted"] = Pem("ENCRYPTED PRIVATE KEY", key.ExportEncryptedPkcs8PrivateKey("s3cret", pbe));
        }
        else
        {
            using var key = RSA.Create(2048);
            var request = new CertificateRequest("CN=" + cn, key, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1);
            AddLeafExtensions(request, eku);
            using var signed = Sign(request, ca);
            withKey = signed.CopyWithPrivateKey(key);
            keyPems["pkcs8"] = Pem("PRIVATE KEY", key.ExportPkcs8PrivateKey());
            keyPems["native"] = Pem("RSA PRIVATE KEY", key.ExportRSAPrivateKey());
            keyPems["encrypted"] = Pem("ENCRYPTED PRIVATE KEY", key.ExportEncryptedPkcs8PrivateKey("s3cret", pbe));
        }

        // Exportable and nameable, as a PFX-loaded certificate is.
        using (withKey)
        {
            return new X509Certificate2(withKey.Export(X509ContentType.Pfx, "x"), "x", X509KeyStorageFlags.Exportable);
        }
    }

    private static void AddLeafExtensions(CertificateRequest request, string eku)
    {
        request.CertificateExtensions.Add(new X509BasicConstraintsExtension(false, false, 0, true));
        request.CertificateExtensions.Add(new X509KeyUsageExtension(X509KeyUsageFlags.DigitalSignature, true));
        request.CertificateExtensions.Add(new X509EnhancedKeyUsageExtension(new OidCollection { new Oid(eku) }, false));
    }

    private static X509Certificate2 Sign(CertificateRequest request, X509Certificate2 ca)
    {
        var serial = new byte[8];
        RandomNumberGenerator.Fill(serial);
        serial[0] &= 0x7F;
        // The RSA CA signs ECDSA leaves too, so the signature generator is explicit.
        using var caKey = ca.GetRSAPrivateKey()!;
        var generator = X509SignatureGenerator.CreateForRSA(caKey, RSASignaturePadding.Pkcs1);
        return request.Create(ca.SubjectName, generator, DateTimeOffset.UtcNow.AddHours(-1), DateTimeOffset.UtcNow.AddDays(30), serial);
    }

    private static string CertPem(X509Certificate2 cert) => Pem("CERTIFICATE", cert.RawData);

    private static string Pem(string label, byte[] der) =>
        $"-----BEGIN {label}-----\n{Convert.ToBase64String(der, Base64FormattingOptions.InsertLineBreaks)}\n-----END {label}-----\n";
}
