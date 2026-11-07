// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;
using System.Text;

namespace DeviceChain.Sdk.Transport;

/// <summary>
/// An X.509 client certificate and its private key, loaded and checked, ready to authenticate a
/// device to the platform broker in the TLS handshake.
/// </summary>
/// <remarks>
/// <para>
/// Build one with <see cref="FromPfx"/>, <see cref="FromPfxFile"/>, <see cref="FromPem"/>,
/// <see cref="FromPemFiles"/> or <see cref="FromCertificate"/>, then assign it to
/// <c>MqttSessionOptions.ClientCertificate</c>. Every loader checks what the platform would
/// otherwise refuse with an opaque CONNACK 5: a private key must be present and must match the
/// certificate, the certificate must not be a CA, and if it declares an extended key usage that
/// usage must include client authentication. A failure throws
/// <see cref="MqttClientCertificateException"/> naming the problem.
/// </para>
/// <para>
/// The device's trust in the broker is separate. This object is the device's identity; whether the
/// device believes the broker is <see cref="MqttTrust"/> (for a private instance CA, pin it with
/// <see cref="MqttTrust.PinnedCa"/> or <see cref="MqttTrust.PinnedCaFromFile"/>).
/// </para>
/// <para>
/// Only RSA and ECDSA keys can be loaded: .NET has no Ed25519 implementation, so a device holding
/// an Ed25519 certificate cannot present it with this SDK. The clock is not consulted when
/// loading, because a device's clock is not always right; <see cref="NotAfter"/> is exposed so a
/// caller that trusts its clock can check, and a connect failure on a locally expired certificate
/// says so.
/// </para>
/// <para>
/// Intermediate certificates found beside the device certificate are offered to the TLS stack, but
/// whether the stack sends them is the platform's choice. The reliable arrangement is to anchor the
/// tenant at the CA that issues the device certificates, so the device sends only its own.
/// </para>
/// </remarks>
public sealed class MqttClientCertificate : IDisposable
{
    private const string ClientAuthOid = "1.3.6.1.5.5.7.3.2";

    private readonly X509Certificate2[] _extras;
    private readonly bool _owns;

    private MqttClientCertificate(X509Certificate2 certificate, X509Certificate2[] extras, bool owns)
    {
        Certificate = certificate;
        _extras = extras;
        _owns = owns;
    }

    /// <summary>The device certificate, with its private key.</summary>
    public X509Certificate2 Certificate { get; }

    /// <summary>The certificate's subject, for logs.</summary>
    public string Subject => Certificate.Subject;

    /// <summary>The start of the certificate's validity.</summary>
    public DateTimeOffset NotBefore => new DateTimeOffset(Certificate.NotBefore);

    /// <summary>The end of the certificate's validity; the platform closes the session then.</summary>
    public DateTimeOffset NotAfter => new DateTimeOffset(Certificate.NotAfter);

    /// <summary>The certificates (the device certificate first) handed to the TLS stack.</summary>
    internal X509Certificate2[] TlsCertificates
    {
        get
        {
            var all = new X509Certificate2[1 + _extras.Length];
            all[0] = Certificate;
            for (var i = 0; i < _extras.Length; i++)
            {
                all[i + 1] = _extras[i];
            }

            return all;
        }
    }

    /// <summary>Loads a PKCS#12 (PFX) bundle holding the certificate and its private key.</summary>
    /// <param name="pfx">The PFX bytes.</param>
    /// <param name="password">The bundle's password, or null for none.</param>
    /// <exception cref="MqttClientCertificateException">The bundle cannot be read, or fails the checks.</exception>
    public static MqttClientCertificate FromPfx(byte[] pfx, string? password = null)
    {
        if (pfx == null || pfx.Length == 0)
        {
            throw new MqttClientCertificateException("the PFX data is empty");
        }

        var collection = new X509Certificate2Collection();
        try
        {
            collection.Import(pfx, password, X509KeyStorageFlags.DefaultKeySet);
        }
        catch (CryptographicException ex)
        {
            throw new MqttClientCertificateException(
                "the PFX could not be read: it is corrupt, or the password is wrong (" + ex.Message + ")", ex);
        }

        X509Certificate2? leaf = null;
        var extras = new List<X509Certificate2>();
        foreach (var c in collection)
        {
            if (leaf == null && c.HasPrivateKey)
            {
                leaf = c;
            }
            else
            {
                extras.Add(c);
            }
        }

        if (leaf == null)
        {
            foreach (var c in extras)
            {
                c.Dispose();
            }

            throw new MqttClientCertificateException(
                "the PFX holds no private key; a client certificate needs the key that belongs to it");
        }

        return Create(leaf, extras.ToArray());
    }

    /// <summary>Loads a PKCS#12 (PFX) file. See <see cref="FromPfx"/>.</summary>
    /// <exception cref="MqttClientCertificateException">The file cannot be read, or fails the checks.</exception>
    public static MqttClientCertificate FromPfxFile(string path, string? password = null) =>
        FromPfx(ReadFile(path), password);

    /// <summary>
    /// Loads a PEM certificate (the device certificate first, then any intermediates) and the PEM
    /// private key that belongs to it.
    /// </summary>
    /// <param name="certificatePem">One or more <c>CERTIFICATE</c> PEM blocks.</param>
    /// <param name="privateKeyPem">
    /// A <c>PRIVATE KEY</c> (PKCS#8), <c>RSA PRIVATE KEY</c> (PKCS#1), <c>EC PRIVATE KEY</c> (SEC1)
    /// or <c>ENCRYPTED PRIVATE KEY</c> block.
    /// </param>
    /// <param name="keyPassword">The password of an encrypted key; ignored for a plain one.</param>
    /// <exception cref="MqttClientCertificateException">The PEM cannot be read, or fails the checks.</exception>
    public static MqttClientCertificate FromPem(string certificatePem, string privateKeyPem, string? keyPassword = null)
    {
        if (string.IsNullOrWhiteSpace(certificatePem))
        {
            throw new MqttClientCertificateException("the certificate PEM is empty");
        }

        if (string.IsNullOrWhiteSpace(privateKeyPem))
        {
            throw new MqttClientCertificateException("the private key PEM is empty");
        }

        var certs = new List<X509Certificate2>();
        foreach (var block in PemBlocks(certificatePem))
        {
            if (block.Label == "CERTIFICATE")
            {
                try
                {
                    certs.Add(new X509Certificate2(block.Der));
                }
                catch (CryptographicException ex)
                {
                    throw new MqttClientCertificateException("a CERTIFICATE block is not a valid certificate", ex);
                }
            }
            else if (block.Label.IndexOf("PRIVATE KEY", StringComparison.Ordinal) >= 0)
            {
                throw new MqttClientCertificateException(
                    "the certificate PEM contains a private key; pass the key as the second argument and keep it out of the certificate file");
            }
        }

        if (certs.Count == 0)
        {
            throw new MqttClientCertificateException("the certificate PEM holds no CERTIFICATE block");
        }

        var leaf = certs[0];
        var extras = certs.GetRange(1, certs.Count - 1).ToArray();
        X509Certificate2 withKey;
        try
        {
            withKey = AttachKey(leaf, privateKeyPem, keyPassword);
        }
        catch
        {
            foreach (var c in certs)
            {
                c.Dispose();
            }

            throw;
        }

        // 🔴 ROUND-TRIP THROUGH PKCS#12. A certificate joined to a key in memory is not usable for
        // TLS client authentication on Windows (Schannel needs a key it can name: "no credentials
        // are available in the security package"), while one loaded from a PFX is. The key is
        // exportable because it was imported from bytes, and the bundle never leaves this method.
        var transient = RandomPassword();
        X509Certificate2 usable;
        try
        {
            var bundle = withKey.Export(X509ContentType.Pfx, transient);
            try
            {
                usable = new X509Certificate2(bundle, transient, X509KeyStorageFlags.DefaultKeySet);
            }
            finally
            {
                Array.Clear(bundle, 0, bundle.Length);
            }
        }
        catch (CryptographicException ex)
        {
            foreach (var c in certs)
            {
                c.Dispose();
            }

            throw new MqttClientCertificateException("the key could not be prepared for TLS: " + ex.Message, ex);
        }
        finally
        {
            withKey.Dispose();
        }

        // The parsed leaf (without its key) is superseded by `usable`; the intermediates are kept.
        leaf.Dispose();
        return Create(usable, extras);
    }

    /// <summary>Loads a PEM certificate file and a PEM key file. See <see cref="FromPem"/>.</summary>
    /// <exception cref="MqttClientCertificateException">A file cannot be read, or fails the checks.</exception>
    public static MqttClientCertificate FromPemFiles(string certificatePath, string privateKeyPath, string? keyPassword = null) =>
        FromPem(Encoding.ASCII.GetString(ReadFile(certificatePath)), Encoding.ASCII.GetString(ReadFile(privateKeyPath)), keyPassword);

    /// <summary>
    /// Wraps a certificate the caller already holds (from a certificate store or a hardware-backed
    /// provider). It must carry its private key.
    /// </summary>
    /// <exception cref="MqttClientCertificateException">The certificate has no private key or fails the checks.</exception>
    public static MqttClientCertificate FromCertificate(X509Certificate2 certificate)
    {
        if (certificate == null)
        {
            throw new ArgumentNullException(nameof(certificate));
        }

        // Borrowed, not owned: the caller keeps responsibility for disposing it.
        return CreateChecked(certificate, Array.Empty<X509Certificate2>(), owns: false);
    }

    /// <summary>Whether the certificate is past its end of validity at <paramref name="now"/>.</summary>
    public bool IsExpired(DateTimeOffset now) => now > NotAfter;

    /// <summary>
    /// Releases the certificates this object loaded. A certificate passed to
    /// <see cref="FromCertificate"/> belongs to the caller and is left alone.
    /// </summary>
    public void Dispose()
    {
        if (!_owns)
        {
            return;
        }

        Certificate.Dispose();
        foreach (var e in _extras)
        {
            e.Dispose();
        }
    }

    private static MqttClientCertificate Create(X509Certificate2 leaf, X509Certificate2[] extras)
    {
        try
        {
            return CreateChecked(leaf, extras);
        }
        catch (MqttClientCertificateException)
        {
            // A rejected certificate is not handed to the caller, so nobody else can dispose it.
            leaf.Dispose();
            foreach (var e in extras)
            {
                e.Dispose();
            }

            throw;
        }
    }

    private static MqttClientCertificate CreateChecked(X509Certificate2 leaf, X509Certificate2[] extras, bool owns = true)
    {
        if (!leaf.HasPrivateKey)
        {
            throw new MqttClientCertificateException(
                "the certificate has no private key; a client certificate needs the key that belongs to it");
        }

        foreach (var ext in leaf.Extensions)
        {
            if (ext is X509BasicConstraintsExtension bc && bc.CertificateAuthority)
            {
                throw new MqttClientCertificateException(
                    $"\"{leaf.Subject}\" is a CA certificate; the platform refuses a CA as a device certificate");
            }

            if (ext is X509EnhancedKeyUsageExtension eku)
            {
                var ok = eku.EnhancedKeyUsages.Count == 0;
                foreach (var oid in eku.EnhancedKeyUsages)
                {
                    if (oid.Value == ClientAuthOid)
                    {
                        ok = true;
                    }
                }

                if (!ok)
                {
                    throw new MqttClientCertificateException(
                        $"\"{leaf.Subject}\" has an extended key usage without client authentication ({ClientAuthOid}); the platform refuses it");
                }
            }
        }

        return new MqttClientCertificate(leaf, extras, owns);
    }

    private static X509Certificate2 AttachKey(X509Certificate2 cert, string keyPem, string? keyPassword)
    {
        PemBlock? keyBlock = null;
        foreach (var b in PemBlocks(keyPem))
        {
            if (b.Label.IndexOf("PRIVATE KEY", StringComparison.Ordinal) >= 0)
            {
                keyBlock = b;
                break;
            }
        }

        if (keyBlock == null)
        {
            throw new MqttClientCertificateException("the private key PEM holds no PRIVATE KEY block");
        }

        var label = keyBlock.Label;
        var der = keyBlock.Der;
        try
        {
            return AttachKeyCore(cert, label, der, keyPassword);
        }
        finally
        {
            Array.Clear(der, 0, der.Length);
        }
    }

    private static X509Certificate2 AttachKeyCore(X509Certificate2 cert, string label, byte[] der, string? keyPassword)
    {
        try
        {
            using var rsaPub = cert.GetRSAPublicKey();
            if (rsaPub != null)
            {
                var rsa = RSA.Create();
                try
                {
                    ImportKey(rsa, label, der, keyPassword);
                    if (!RsaMatches(rsaPub, rsa))
                    {
                        throw new MqttClientCertificateException(
                            $"the private key does not belong to \"{cert.Subject}\" (public keys differ)");
                    }

                    return RSACertificateExtensions.CopyWithPrivateKey(cert, rsa);
                }
                finally
                {
                    rsa.Dispose();
                }
            }

            using var ecPub = cert.GetECDsaPublicKey();
            if (ecPub != null)
            {
                var ec = ECDsa.Create();
                try
                {
                    ImportKey(ec, label, der, keyPassword);
                    if (!EcMatches(ecPub, ec))
                    {
                        throw new MqttClientCertificateException(
                            $"the private key does not belong to \"{cert.Subject}\" (public keys differ)");
                    }

                    return ECDsaCertificateExtensions.CopyWithPrivateKey(cert, ec);
                }
                finally
                {
                    ec.Dispose();
                }
            }
        }
        catch (CryptographicException ex)
        {
            throw new MqttClientCertificateException(
                "the private key could not be read: it is malformed, or its password is wrong or missing (" + ex.Message + ")", ex);
        }

        throw new MqttClientCertificateException(
            $"\"{cert.Subject}\" has a key algorithm ({cert.PublicKey.Oid.FriendlyName ?? cert.PublicKey.Oid.Value}) this SDK cannot use; only RSA and ECDSA are supported (Ed25519 is not available in .NET)");
    }

    private static void ImportKey(AsymmetricAlgorithm alg, string label, byte[] der, string? password)
    {
        switch (label)
        {
            case "ENCRYPTED PRIVATE KEY":
                if (password == null)
                {
                    throw new MqttClientCertificateException("the private key is encrypted and no key password was given");
                }

                if (alg is RSA r0)
                {
                    r0.ImportEncryptedPkcs8PrivateKey(password.AsSpan(), der, out _);
                }
                else
                {
                    ((ECDsa)alg).ImportEncryptedPkcs8PrivateKey(password.AsSpan(), der, out _);
                }

                break;
            case "PRIVATE KEY":
                if (alg is RSA r1)
                {
                    r1.ImportPkcs8PrivateKey(der, out _);
                }
                else
                {
                    ((ECDsa)alg).ImportPkcs8PrivateKey(der, out _);
                }

                break;
            case "RSA PRIVATE KEY":
                if (!(alg is RSA r2))
                {
                    throw new MqttClientCertificateException("an RSA PRIVATE KEY block cannot belong to a non-RSA certificate");
                }

                r2.ImportRSAPrivateKey(der, out _);
                break;
            case "EC PRIVATE KEY":
                if (!(alg is ECDsa e2))
                {
                    throw new MqttClientCertificateException("an EC PRIVATE KEY block cannot belong to a non-EC certificate");
                }

                e2.ImportECPrivateKey(der, out _);
                break;
            default:
                throw new MqttClientCertificateException($"the private key block \"{label}\" is not supported");
        }
    }

    private static bool RsaMatches(RSA pub, RSA priv)
    {
        var a = pub.ExportParameters(false);
        var b = priv.ExportParameters(false);
        return SeqEq(a.Modulus, b.Modulus) && SeqEq(a.Exponent, b.Exponent);
    }

    private static bool EcMatches(ECDsa pub, ECDsa priv)
    {
        var a = pub.ExportParameters(false);
        var b = priv.ExportParameters(false);
        return SeqEq(a.Q.X, b.Q.X) && SeqEq(a.Q.Y, b.Q.Y);
    }

    private static bool SeqEq(byte[]? a, byte[]? b)
    {
        if (a == null || b == null || a.Length != b.Length)
        {
            return false;
        }

        for (var i = 0; i < a.Length; i++)
        {
            if (a[i] != b[i])
            {
                return false;
            }
        }

        return true;
    }

    private static byte[] ReadFile(string path)
    {
        if (string.IsNullOrEmpty(path))
        {
            throw new MqttClientCertificateException("a file path is required");
        }

        try
        {
            return File.ReadAllBytes(path);
        }
        catch (Exception ex) when (ex is IOException || ex is UnauthorizedAccessException)
        {
            throw new MqttClientCertificateException($"cannot read \"{path}\": {ex.Message}", ex);
        }
    }

    private static string RandomPassword()
    {
        var bytes = new byte[18];
        using (var rng = RandomNumberGenerator.Create())
        {
            rng.GetBytes(bytes);
        }

        return Convert.ToBase64String(bytes);
    }

    private sealed class PemBlock
    {
        public PemBlock(string label, byte[] der)
        {
            Label = label;
            Der = der;
        }

        public string Label { get; }

        public byte[] Der { get; }
    }

    // A small PEM reader rather than X509Certificate2.CreateFromPem / PemEncoding: both are .NET 5+
    // and absent from netstandard2.1, the target Unity consumes.
    private static List<PemBlock> PemBlocks(string pem)
    {
        const string begin = "-----BEGIN ";
        const string end = "-----END ";
        var blocks = new List<PemBlock>();
        var pos = 0;
        while (true)
        {
            var b = pem.IndexOf(begin, pos, StringComparison.Ordinal);
            if (b < 0)
            {
                break;
            }

            var labelStart = b + begin.Length;
            var labelEnd = pem.IndexOf("-----", labelStart, StringComparison.Ordinal);
            if (labelEnd < 0)
            {
                throw new MqttClientCertificateException("malformed PEM: unterminated BEGIN line");
            }

            var label = pem.Substring(labelStart, labelEnd - labelStart);
            var bodyStart = labelEnd + 5;
            var e = pem.IndexOf(end + label + "-----", bodyStart, StringComparison.Ordinal);
            if (e < 0)
            {
                throw new MqttClientCertificateException($"malformed PEM: no END line for \"{label}\"");
            }

            var body = pem.Substring(bodyStart, e - bodyStart);
            var sb = new StringBuilder(body.Length);
            foreach (var line in body.Split('\n'))
            {
                var t = line.Trim();
                // Legacy encrypted-PEM headers ("Proc-Type:", "DEK-Info:") cannot be decoded here.
                if (t.IndexOf(':') >= 0)
                {
                    throw new MqttClientCertificateException(
                        "a legacy encrypted PEM key (Proc-Type/DEK-Info headers) is not supported; convert it with `openssl pkcs8 -topk8` or use a PFX");
                }

                sb.Append(t);
            }

            byte[] der;
            try
            {
                der = Convert.FromBase64String(sb.ToString());
            }
            catch (FormatException ex)
            {
                throw new MqttClientCertificateException($"malformed PEM: the \"{label}\" block is not valid base64", ex);
            }

            blocks.Add(new PemBlock(label, der));
            pos = e + end.Length + label.Length + 5;
        }

        return blocks;
    }
}

/// <summary>
/// Thrown when a client certificate cannot be loaded or would be refused by the platform before
/// any connection is attempted. It is a configuration error: retrying cannot fix it.
/// </summary>
public sealed class MqttClientCertificateException : Exception
{
    /// <summary>Creates the exception.</summary>
    public MqttClientCertificateException(string message)
        : base(message)
    {
    }

    /// <summary>Creates the exception, preserving the cause.</summary>
    public MqttClientCertificateException(string message, Exception? innerException)
        : base(message, innerException)
    {
    }
}
