// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Net;
using System.Net.Security;
using System.Net.Sockets;
using System.Security.Cryptography.X509Certificates;
using System.Threading;
using System.Threading.Tasks;
using MQTTnet;
using MQTTnet.Client;
using MQTTnet.Formatter;
using MQTTnet.Protocol;

namespace DeviceChain.Sdk.Transport;

/// <summary>
/// The default <see cref="IMqttClientFactory"/>, backed by MQTTnet.
/// </summary>
/// <remarks>
/// <para>
/// The MQTTnet version is pinned to the 4.x line and that is a HARD CONSTRAINT, not inertia:
/// MQTTnet 5 targets <c>net8.0</c>/<c>net10.0</c> only, having dropped <c>netstandard2.1</c> —
/// which is precisely the target Unity/IL2CPP consumes. Upgrading to 5 would silently remove the
/// Unity half of this SDK. If a future Unity gains a <c>net8.0</c>-capable runtime, revisit;
/// until then, 4.x is the only version that can ship here.
/// </para>
/// <para>
/// Why MQTTnet 4 is safe for the AOT guarantee this SDK makes: its source contains no
/// <c>Reflection.Emit</c>, <c>DynamicMethod</c>, <c>Activator.CreateInstance</c> or
/// <c>Assembly.Load</c> — it is socket and buffer code — and on <c>netstandard2.1</c> it has ZERO
/// transitive package dependencies, so it stages into a Unity project as a single DLL. Measured,
/// not assumed: the trimmer reports no <c>IL2xxx</c> warnings across its IL, and a NativeAOT
/// publish of the connect/subscribe/publish path produces a native binary that runs.
/// The residual gap is stated where it belongs — NativeAOT is not IL2CPP, so the Unity player
/// check is the only thing that closes it.
/// </para>
/// <para>
/// The managed-client extension (<c>MQTTnet.Extensions.ManagedClient</c>) is deliberately NOT
/// used. Its auto-reconnect owns the subscribe lifecycle, and that lifecycle is exactly what has
/// to be observable here — a re-subscribe whose grant is never read is the defect this seam
/// exists to prevent. Reconnect stays above the seam, where it can be seen.
/// </para>
/// </remarks>
public sealed class MqttNetClientFactory : IMqttClientFactory
{
    /// <inheritdoc />
    public IMqttConnection Create() => new MqttNetConnection();
}

/// <summary>One MQTTnet-backed MQTT 3.1.1 connection.</summary>
public sealed class MqttNetConnection : IMqttConnection
{
    private readonly IMqttClient _client;
    private int _disposed;

    /// <summary>Creates an unconnected connection.</summary>
    public MqttNetConnection()
    {
        _client = new MqttFactory().CreateMqttClient();

        _client.ApplicationMessageReceivedAsync += async e =>
        {
            var handler = MessageReceived;
            if (handler == null)
            {
                return;
            }

            // PayloadSegment's array is MQTTnet's buffer and is not ours to hold past this
            // callback, so the copy is required rather than defensive.
            var segment = e.ApplicationMessage.PayloadSegment;
            var payload = new byte[segment.Count];
            if (segment.Count > 0 && segment.Array != null)
            {
                Buffer.BlockCopy(segment.Array, segment.Offset, payload, 0, segment.Count);
            }

            await handler(new MqttInbound(e.ApplicationMessage.Topic, payload)).ConfigureAwait(false);
        };

        _client.DisconnectedAsync += e =>
        {
            ConnectionLost?.Invoke(e.Exception);
            return Task.CompletedTask;
        };
    }

    /// <inheritdoc />
    public event Func<MqttInbound, Task>? MessageReceived;

    /// <inheritdoc />
    public event Action<Exception?>? ConnectionLost;

    /// <inheritdoc />
    public bool IsConnected => _client.IsConnected;

    /// <inheritdoc />
    public async Task ConnectAsync(MqttConnectOptions options, CancellationToken cancellationToken)
    {
        if (options == null)
        {
            throw new ArgumentNullException(nameof(options));
        }

        var builder = new MqttClientOptionsBuilder()
            .WithTcpServer(options.BrokerUri.Host, PortOf(options.BrokerUri))
            // Pinned explicitly: the platform's broker speaks MQTT 3.1.1 only, and letting the
            // library negotiate would make the wire depend on a default that can move.
            .WithProtocolVersion(MqttProtocolVersion.V311)
            .WithClientId(options.ClientId)
            .WithCleanSession(options.CleanSession)
            .WithKeepAlivePeriod(options.KeepAlive);

        if (options.Username != null)
        {
            // The device plane's password is EMPTY and that is meaningful — it selects
            // access-token credential mode at the auth callout — so it is passed through as
            // given rather than treated as "no credentials".
            builder = builder.WithCredentials(options.Username, options.Password ?? string.Empty);
        }

        if (IsTls(options.BrokerUri))
        {
            var trust = options.Trust ?? MqttTrust.SystemRoots;
            builder = builder.WithTlsOptions(tls =>
            {
                tls.UseTls(true);
                switch (trust.Mode)
                {
                    case MqttTrustMode.SystemRoots:
                        // Leave MQTTnet's default validation in place: full chain validation
                        // against the OS root store.
                        break;

                    case MqttTrustMode.PinnedCa:
                        var pinned = new X509Certificate2(trust.CaCertificate!);
                        tls.WithCertificateValidationHandler(ctx => ValidateAgainstPinnedRoot(ctx, pinned));
                        break;

                    case MqttTrustMode.AcceptAny:
                        tls.WithCertificateValidationHandler(_ => true);
                        break;

                    default:
                        throw new MqttConnectionException($"unknown MQTT trust mode {trust.Mode}");
                }
            });
        }

        var built = builder.Build();
        ApplyAddressFamily(built, options.AddressFamily);

        try
        {
            await ConnectOnceAsync(built, options, cancellationToken).ConfigureAwait(false);
        }
        catch (Exception ex) when (!(ex is MqttConnectionException) && !(ex is OperationCanceledException))
        {
            // 🔑 MQTTnet SWALLOWS THE CANCELLATION AND RETHROWS ITS OWN TYPE. A caller's expired
            // token surfaces as MqttCommunicationTimedOutException, not OperationCanceledException,
            // so wrapping unconditionally would present a deliberate cancellation as a broker
            // failure — and a caller catching OperationCanceledException for shutdown would never
            // see it. Re-assert the convention before wrapping. (Found by the bounded-subscribe
            // test, which is why it asserts the type rather than merely that something was thrown.)
            cancellationToken.ThrowIfCancellationRequested();

            // 🔴 THE DUAL-STACK RETRY. A host resolving to both families is dialled in the
            // resolver's order, and on Windows `localhost` yields ::1 first while a container
            // runtime publishes on 0.0.0.0 — IPv4 only. Retrying once over IPv4 turns that from an
            // opaque socket error into a connection. Bounded to one extra attempt, attempted only
            // when the caller did NOT pin a family, and only when the host genuinely has both — so
            // a real IPv6 deployment is never quietly downgraded.
            if (options.AddressFamily == AddressFamily.Unspecified &&
                await HasBothFamiliesAsync(options.BrokerUri.Host).ConfigureAwait(false))
            {
                ApplyAddressFamily(built, AddressFamily.InterNetwork);
                try
                {
                    await ConnectOnceAsync(built, options, cancellationToken).ConfigureAwait(false);
                    return;
                }
                catch (Exception retry) when (!(retry is OperationCanceledException))
                {
                    cancellationToken.ThrowIfCancellationRequested();
                    throw new MqttConnectionException(
                        $"connecting to {options.BrokerUri} as \"{options.ClientId}\" failed over both " +
                        $"address families (resolver order: {ex.Message}; IPv4: {retry.Message})", retry);
                }
            }

            throw new MqttConnectionException(
                $"connecting to {options.BrokerUri} as \"{options.ClientId}\" failed: {ex.Message}", ex);
        }
    }

    private async Task ConnectOnceAsync(
        MqttClientOptions built, MqttConnectOptions options, CancellationToken cancellationToken)
    {
        var result = await _client.ConnectAsync(built, cancellationToken).ConfigureAwait(false);
        if (result.ResultCode != MqttClientConnectResultCode.Success)
        {
            throw new MqttConnectRefusedException(
                $"the broker refused the connection for client id \"{options.ClientId}\": {result.ResultCode}");
        }
    }

    private static void ApplyAddressFamily(MqttClientOptions built, AddressFamily family)
    {
        if (built.ChannelOptions is MqttClientTcpOptions tcp)
        {
            tcp.AddressFamily = family;
        }
    }

    /// <summary>
    /// Whether the host resolves to addresses in BOTH families — the only case where retrying over
    /// IPv4 could succeed where the resolver's own order did not.
    /// </summary>
    private static async Task<bool> HasBothFamiliesAsync(string host)
    {
        try
        {
            var addresses = await Dns.GetHostAddressesAsync(host).ConfigureAwait(false);
            var v4 = false;
            var v6 = false;
            foreach (var address in addresses)
            {
                if (address.AddressFamily == AddressFamily.InterNetwork) v4 = true;
                else if (address.AddressFamily == AddressFamily.InterNetworkV6) v6 = true;
            }

            return v4 && v6;
        }
        catch (Exception)
        {
            // A resolver failure is not this method's business to report — the original connect
            // error already says the host could not be reached, and inventing a second one here
            // would replace it with a worse message.
            return false;
        }
    }

    /// <inheritdoc />
    public async Task SubscribeAsync(string topicFilter, MqttQos qos, CancellationToken cancellationToken)
    {
        if (string.IsNullOrEmpty(topicFilter))
        {
            throw new ArgumentException("a topic filter is required", nameof(topicFilter));
        }

        var options = new MqttClientSubscribeOptionsBuilder()
            .WithTopicFilter(topicFilter, (MqttQualityOfServiceLevel)(int)qos)
            .Build();

        MqttClientSubscribeResult result;
        try
        {
            result = await _client.SubscribeAsync(options, cancellationToken).ConfigureAwait(false);
        }
        catch (Exception ex) when (!(ex is OperationCanceledException))
        {
            // See ConnectAsync: a cancelled token reaches here as an MQTTnet timeout type.
            cancellationToken.ThrowIfCancellationRequested();
            throw new MqttConnectionException($"subscribing to \"{topicFilter}\" failed: {ex.Message}", ex);
        }

        // 🔴 THE HALF MQTTnet WILL NOT DO FOR US. SubscribeAsync completes successfully on a
        // REFUSED subscription — verified against a real broker, where a forbidden filter
        // returned normally carrying ResultCode=UnspecifiedError (0x80) and threw nothing. The
        // naive `await client.SubscribeAsync(...)` is therefore the paho defect in a different
        // library. Reading the per-filter code is the only thing that catches it.
        foreach (var item in result.Items)
        {
            // Anything below 0x80 is a granted QoS, possibly downgraded from the one asked for.
            // A downgrade is legal and is NOT a failure.
            if ((int)item.ResultCode >= 0x80)
            {
                throw new MqttSubscribeRefusedException(topicFilter);
            }
        }
    }

    /// <inheritdoc />
    public async Task PublishAsync(string topic, byte[] payload, MqttQos qos, CancellationToken cancellationToken)
    {
        if (string.IsNullOrEmpty(topic))
        {
            throw new ArgumentException("a topic is required", nameof(topic));
        }

        var message = new MqttApplicationMessageBuilder()
            .WithTopic(topic)
            .WithPayload(payload ?? Array.Empty<byte>())
            .WithQualityOfServiceLevel((MqttQualityOfServiceLevel)(int)qos)
            .Build();

        MqttClientPublishResult result;
        try
        {
            // At QoS 1 this awaits the PUBACK, so completion means the broker took ownership.
            result = await _client.PublishAsync(message, cancellationToken).ConfigureAwait(false);
        }
        catch (Exception ex) when (!(ex is OperationCanceledException))
        {
            // See ConnectAsync: a cancelled token reaches here as an MQTTnet timeout type.
            cancellationToken.ThrowIfCancellationRequested();
            throw new MqttConnectionException($"publishing to \"{topic}\" failed: {ex.Message}", ex);
        }

        if (!result.IsSuccess)
        {
            throw new MqttConnectionException(
                $"the broker did not accept the publish to \"{topic}\": {result.ReasonCode}");
        }
    }

    /// <inheritdoc />
    public async Task DisconnectAsync(TimeSpan quiesce, CancellationToken cancellationToken)
    {
        if (!_client.IsConnected)
        {
            return;
        }

        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        timeout.CancelAfter(quiesce);
        try
        {
            await _client.DisconnectAsync(new MqttClientDisconnectOptions(), timeout.Token).ConfigureAwait(false);
        }
        catch (Exception)
        {
            // A disconnect that fails has still achieved the goal — the connection is going away.
            // Surfacing it would oblige every caller to guard a teardown path that cannot be
            // acted on.
        }
    }

    /// <inheritdoc />
    public ValueTask DisposeAsync()
    {
        if (Interlocked.Exchange(ref _disposed, 1) != 0)
        {
            return default;
        }

        _client.Dispose();
        return default;
    }

    private static bool IsTls(Uri uri) =>
        uri.Scheme.Equals("ssl", StringComparison.OrdinalIgnoreCase) ||
        uri.Scheme.Equals("mqtts", StringComparison.OrdinalIgnoreCase) ||
        uri.Scheme.Equals("tls", StringComparison.OrdinalIgnoreCase);

    private static int PortOf(Uri uri) => uri.IsDefaultPort ? (IsTls(uri) ? 8883 : 1883) : uri.Port;

    /// <summary>
    /// Validates the broker's certificate against exactly one pinned root.
    /// </summary>
    /// <remarks>
    /// 🔑 THIS IS HAND-BUILT BECAUSE THE ONE-LINE ANSWER DOES NOT EXIST ON UNITY'S RUNTIME.
    /// <c>X509ChainPolicy.CustomTrustStore</c>/<c>TrustMode</c> — the obvious way to say "trust
    /// exactly this root" — were added in .NET 5 and are absent from <c>netstandard2.1</c>, so
    /// using them would compile on the <c>net8.0</c> target and break the Unity one. Instead the
    /// chain is built with the pinned certificate supplied as an extra store, unknown authorities
    /// tolerated, and then the terminal element compared byte-for-byte against the pin — which
    /// is the check that actually does the work, since tolerating an unknown authority is what
    /// lets a self-signed root build at all.
    /// </remarks>
    private static bool ValidateAgainstPinnedRoot(MqttClientCertificateValidationEventArgs ctx, X509Certificate2 pinned)
    {
        if (ctx.Certificate == null)
        {
            return false;
        }

        var leaf = ctx.Certificate as X509Certificate2 ?? new X509Certificate2(ctx.Certificate);
        return IsChainPinnedTo(leaf, ctx.Chain, ctx.SslPolicyErrors, pinned);
    }

    /// <summary>
    /// The pinned-root decision, separated from MQTTnet's callback so it can be driven directly by
    /// tests with generated certificate chains.
    /// </summary>
    /// <remarks>
    /// It is <c>internal</c> rather than private for exactly that reason. This is the only
    /// security decision the SDK makes on its own, it is hand-built because the one-line API does
    /// not exist on Unity's runtime, and leaving it reachable only through a live TLS handshake
    /// meant its sole exerciser would have been production.
    /// </remarks>
    internal static bool IsChainPinnedTo(
        X509Certificate2 leaf, X509Chain? peerChain, SslPolicyErrors sslPolicyErrors, X509Certificate2 pinned)
    {
        // A missing certificate or a hostname that does not match is fatal regardless of which
        // root signed it — pinning replaces the trust anchor, not the rest of validation.
        if ((sslPolicyErrors & SslPolicyErrors.RemoteCertificateNotAvailable) != 0 ||
            (sslPolicyErrors & SslPolicyErrors.RemoteCertificateNameMismatch) != 0)
        {
            return false;
        }

        var ctx = peerChain;
        using var chain = new X509Chain();
        chain.ChainPolicy.RevocationMode = X509RevocationMode.NoCheck;
        // Tolerating an unknown authority is what allows a privately-rooted chain to build;
        // the root-identity check below is what makes that safe.
        chain.ChainPolicy.VerificationFlags = X509VerificationFlags.AllowUnknownCertificateAuthority;
        chain.ChainPolicy.ExtraStore.Add(pinned);

        // 🔑 THE PEER'S OWN INTERMEDIATES MUST GO IN TOO, or pinning only ever works for a chain
        // exactly one link long. Building from the leaf alone stops at PartialChain when the
        // broker's certificate was issued by an intermediate under the pinned root — the normal
        // CA hygiene arrangement — and the terminal element is then the LEAF, so the identity
        // check below fails and a perfectly valid deployment gets an opaque connect failure.
        // These are attacker-suppliable, which is safe here precisely because they only help a
        // chain BUILD; it still has to terminate at the pin, and every signature is still checked.
        if (ctx != null)
        {
            foreach (var element in ctx.ChainElements)
            {
                if (element.Certificate != null)
                {
                    chain.ChainPolicy.ExtraStore.Add(element.Certificate);
                }
            }
        }

        if (!chain.Build(leaf))
        {
            return false;
        }

        // Every remaining status must be benign. UntrustedRoot/PartialChain are the expected
        // consequences of a private root and are accepted only because the root is then required
        // to BE the pin; anything else (expired, revoked, bad usage) is a real failure.
        foreach (var status in chain.ChainStatus)
        {
            if (status.Status != X509ChainStatusFlags.NoError &&
                status.Status != X509ChainStatusFlags.UntrustedRoot &&
                status.Status != X509ChainStatusFlags.PartialChain)
            {
                return false;
            }
        }

        if (chain.ChainElements.Count == 0)
        {
            return false;
        }

        var root = chain.ChainElements[chain.ChainElements.Count - 1].Certificate;
        return root != null && ByteEquals(root.RawData, pinned.RawData);
    }

    private static bool ByteEquals(byte[] a, byte[] b)
    {
        if (a.Length != b.Length)
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
}
