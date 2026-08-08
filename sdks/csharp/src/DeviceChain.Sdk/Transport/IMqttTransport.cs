// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Net.Sockets;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sdk.Transport;

/// <summary>
/// Creates <see cref="IMqttConnection"/>s — the fourth platform seam, beside
/// <see cref="IHttpTransport"/>, <see cref="IWebSocketFactory"/> and
/// <c>IReadLoopDriver</c>. One factory yields a fresh connection per device session; a dropped
/// connection is replaced, never reused.
/// </summary>
/// <remarks>
/// <para>
/// 🔑 THE SEAM IS DELIBERATELY PROTOCOL-DUMB — it speaks connect/subscribe/publish/receive and
/// nothing about DeviceChain. Every platform rule (client-id composition, topic construction,
/// command envelopes, ack discipline) lives ABOVE it, in <c>MqttDeviceSession</c>. That split is
/// what keeps the default MQTTnet-backed implementation swappable: if the library proves hostile
/// under IL2CPP in a real Unity player — the one thing no CI rung can prove — a hand-rolled
/// MQTT 3.1.1 client implements this same surface and every test above the seam survives the swap
/// untouched. A seam shaped around DeviceChain concepts instead would have to be rewritten.
/// </para>
/// <para>
/// 🔴 THIS SEAM EXCLUDES WEBGL, AND THAT IS NOT AN OVERSIGHT. A browser cannot open a raw TCP or
/// TLS socket, so there is no browser-backed implementation to write — unlike
/// <see cref="IWebSocketFactory"/>, which exists precisely because a <c>.jslib</c> shim can back
/// it. MQTT is a native/desktop/mobile capability here.
/// </para>
/// </remarks>
public interface IMqttClientFactory
{
    /// <summary>Creates a new, unconnected connection.</summary>
    IMqttConnection Create();
}

/// <summary>
/// A single MQTT 3.1.1 connection. The caller drives exactly these operations, so an alternate
/// client need implement only this surface.
/// </summary>
public interface IMqttConnection : IAsyncDisposable
{
    /// <summary>
    /// Opens the connection and completes when the broker has returned a successful CONNACK.
    /// Throws <see cref="MqttConnectionException"/> if the broker refuses or the dial fails.
    /// </summary>
    Task ConnectAsync(MqttConnectOptions options, CancellationToken cancellationToken);

    /// <summary>True while the connection is established and usable.</summary>
    bool IsConnected { get; }

    /// <summary>
    /// Raised for each inbound application message. Handlers are invoked on a background thread;
    /// marshalling to a UI/main thread is the caller's concern (the SDK never captures a
    /// <see cref="SynchronizationContext"/> of its own — see the Unity pump).
    /// </summary>
    event Func<MqttInbound, Task>? MessageReceived;

    /// <summary>
    /// Raised when an established connection drops, carrying the cause where one is known.
    /// Reconnect is the caller's concern: this seam never reconnects on its own.
    /// </summary>
    event Action<Exception?>? ConnectionLost;

    /// <summary>
    /// Subscribes and completes ONLY when the broker has GRANTED the subscription.
    /// </summary>
    /// <remarks>
    /// <para>
    /// 🔴 GRANT-OR-THROW IS THE ENTIRE CONTRACT OF THIS METHOD, and it is stated on the interface
    /// rather than left to each implementation because the failure it prevents is invisible.
    /// A broker REFUSES a subscription with SUBACK return code 0x80 (MQTT 3.1.1 §3.9.3) — an ACL
    /// that denies read on the filter, or a filter it will not accept. An implementation that
    /// merely waits for the SUBACK, rather than reading it, produces a client that connects
    /// cleanly, reports healthy, logs that it subscribed, and then receives nothing for the life
    /// of the process. There is no later signal, because from the client's side nothing went
    /// wrong.
    /// </para>
    /// <para>
    /// This is not hypothetical here. The Go side shipped exactly that defect: paho copies the
    /// return codes into the token's result map and calls <c>flowComplete()</c> — it never calls
    /// <c>setError</c> — so both obvious spellings of "subscribe and check the error" were wrong
    /// in the same way. <c>messaging.SubscribeMqttConfirmed</c> is the Go counterpart of this
    /// contract, and <c>tools/subconfirm</c> is the analyzer that now keeps Go callers honest.
    /// MQTTnet is one notch better than paho and still not safe by default: it exposes the code
    /// on the returned result, but <c>SubscribeAsync</c> does not throw on a refusal — verified
    /// against a real broker, where a forbidden filter returned normally with
    /// <c>ResultCode=UnspecifiedError</c>. So the read is the wrapper's job, on every client.
    /// </para>
    /// <para>
    /// A QoS DOWNGRADE is a grant, not a failure: a broker may legally return a lower QoS than
    /// was asked for. Only 0x80 and above is a refusal.
    /// </para>
    /// <para>
    /// The cancellation token is the other half. An unbounded wait turns a broker that accepts
    /// the connection and then stops answering into a caller that hangs at startup forever
    /// rather than failing it.
    /// </para>
    /// </remarks>
    /// <exception cref="MqttSubscribeRefusedException">The broker refused the filter.</exception>
    Task SubscribeAsync(string topicFilter, MqttQos qos, CancellationToken cancellationToken);

    /// <summary>
    /// Publishes one application message. At <see cref="MqttQos.AtLeastOnce"/> this completes
    /// only when the broker has PUBACKed — which is the durability point that matters: an
    /// unacked QoS-1 publish is not a delivered message.
    /// </summary>
    Task PublishAsync(string topic, byte[] payload, MqttQos qos, CancellationToken cancellationToken);

    /// <summary>
    /// Sends a DISCONNECT and closes, allowing <paramref name="quiesce"/> for in-flight work.
    /// A clean DISCONNECT matters with <c>cleanSession=false</c>: it tells the broker this was
    /// intentional rather than a drop.
    /// </summary>
    Task DisconnectAsync(TimeSpan quiesce, CancellationToken cancellationToken);
}

/// <summary>Quality of service for a publish or subscribe.</summary>
/// <remarks>
/// Only the two levels the device plane uses are modelled. QoS 2 is deliberately absent: nothing
/// in the device plane asks for it, and admitting it to the seam would oblige every alternate
/// implementation — including the hand-rolled fallback — to carry the PUBREC/PUBREL/PUBCOMP flow
/// for a level no caller uses.
/// </remarks>
public enum MqttQos
{
    /// <summary>Fire and forget.</summary>
    AtMostOnce = 0,

    /// <summary>Acknowledged delivery; may be redelivered, so receivers must de-duplicate.</summary>
    AtLeastOnce = 1,
}

/// <summary>One inbound application message.</summary>
public readonly struct MqttInbound
{
    /// <summary>Creates an inbound message.</summary>
    public MqttInbound(string topic, byte[] payload)
    {
        Topic = topic;
        Payload = payload;
    }

    /// <summary>The topic it was published to.</summary>
    public string Topic { get; }

    /// <summary>The raw payload bytes. Never null; an empty payload is a zero-length array.</summary>
    public byte[] Payload { get; }
}

/// <summary>How the client establishes trust in the broker's TLS certificate.</summary>
/// <remarks>
/// <para>
/// 🔑 TRUST IS ITS OWN OBJECT AND IS NEVER INFERRED FROM THE BROKER URI. It mirrors the Go
/// simulator's split, where <c>Handshake.MqttTLSInsecure</c> is deliberately separate from
/// <c>Endpoints.MqttBroker</c> — a trust decision is about an address, not part of one. Folding
/// it into the URI (an <c>ssl+insecure://</c> scheme, say) would make the weakest setting the
/// easiest thing to type.
/// </para>
/// <para>
/// The default is full system-root validation. The SDK never silently weakens it, and the
/// accept-anything mode exists only under a name that cannot be mistaken for a normal option.
/// </para>
/// </remarks>
public sealed class MqttTrust
{
    private MqttTrust(MqttTrustMode mode, byte[]? caCertificate)
    {
        Mode = mode;
        CaCertificate = caCertificate;
    }

    /// <summary>Which trust rule applies.</summary>
    public MqttTrustMode Mode { get; }

    /// <summary>The pinned root for <see cref="MqttTrustMode.PinnedCa"/>; null otherwise.</summary>
    public byte[]? CaCertificate { get; }

    /// <summary>
    /// Validate the broker's chain against the operating system's root store — the default, and
    /// the right answer for any broker holding a publicly-issued certificate.
    /// </summary>
    public static MqttTrust SystemRoots { get; } = new(MqttTrustMode.SystemRoots, null);

    /// <summary>
    /// Validate the broker's chain against exactly one supplied root (DER or PEM). This is the
    /// RECOMMENDED setting for a development bring-up, whose gateway presents a self-signed
    /// certificate that system roots cannot validate: it still verifies the chain, so a wrong or
    /// substituted certificate is still refused.
    /// </summary>
    public static MqttTrust PinnedCa(byte[] certificate)
    {
        if (certificate == null || certificate.Length == 0)
        {
            throw new ArgumentException("a pinned CA certificate must not be empty", nameof(certificate));
        }

        return new MqttTrust(MqttTrustMode.PinnedCa, certificate);
    }

    /// <summary>
    /// Accept ANY server certificate without validating it. This disables the protection TLS
    /// exists to provide: it stops an eavesdropper reading the traffic but does nothing to stop
    /// an impersonator, so anything able to intercept the connection can present its own
    /// certificate and be believed — including the device credentials sent in CONNECT.
    /// </summary>
    /// <remarks>
    /// It exists because a local bring-up's self-signed certificate is a real situation the
    /// platform already acknowledges (<c>Handshake.MqttTLSInsecure</c>). Prefer
    /// <see cref="PinnedCa"/>, which handles that same case while still verifying. The method is
    /// named the way it is so that no call site can describe itself as anything other than what
    /// it does.
    /// </remarks>
    public static MqttTrust DangerouslyAcceptAnyServerCertificate() =>
        new(MqttTrustMode.AcceptAny, null);
}

/// <summary>The trust rules <see cref="MqttTrust"/> can express.</summary>
public enum MqttTrustMode
{
    /// <summary>Validate against the OS root store.</summary>
    SystemRoots,

    /// <summary>Validate against exactly one supplied root certificate.</summary>
    PinnedCa,

    /// <summary>Accept any certificate, validating nothing.</summary>
    AcceptAny,
}

/// <summary>Everything needed to open one MQTT connection.</summary>
public sealed class MqttConnectOptions
{
    /// <summary>Creates connect options.</summary>
    /// <param name="brokerUri">
    /// The broker address, as <c>tcp://host:port</c> (plaintext) or <c>ssl://host:port</c> (TLS).
    /// These are the schemes the platform's own handshake record uses, so an
    /// <c>Endpoints.MqttBroker</c> value can be passed through verbatim.
    /// </param>
    /// <param name="clientId">
    /// The MQTT client id. The device plane dictates this value — see <c>DeviceClientId</c>; the
    /// auth callout refuses anything else.
    /// </param>
    public MqttConnectOptions(Uri brokerUri, string clientId)
    {
        BrokerUri = brokerUri ?? throw new ArgumentNullException(nameof(brokerUri));
        if (string.IsNullOrEmpty(clientId))
        {
            throw new ArgumentException("an MQTT client id is required", nameof(clientId));
        }

        ClientId = clientId;
    }

    /// <summary>The broker address.</summary>
    public Uri BrokerUri { get; }

    /// <summary>
    /// Which IP address family to dial, when the broker host resolves to more than one.
    /// </summary>
    /// <remarks>
    /// 🔴 THIS EXISTS BECAUSE A DUAL-STACK HOST SILENTLY PICKS THE WRONG ONE. On Windows,
    /// <c>localhost</c> resolves to <c>::1</c> BEFORE <c>127.0.0.1</c>, while a container runtime
    /// publishes its ports on <c>0.0.0.0</c> — IPv4 only. The IPv6 attempt is refused and the
    /// caller sees an opaque socket error naming a host that is demonstrably reachable, which
    /// sends you looking at TLS or credentials instead of at name resolution.
    /// <para>
    /// Left <see cref="System.Net.Sockets.AddressFamily.Unspecified"/> the connection tries the
    /// resolver's own order and, if that fails while the host has addresses in both families,
    /// retries ONCE over IPv4 — so the common case heals itself rather than requiring the caller
    /// to have diagnosed it first. Set it explicitly to pin the choice and skip the fallback.
    /// </para>
    /// </remarks>
    public AddressFamily AddressFamily { get; set; } = AddressFamily.Unspecified;

    /// <summary>The client id presented in CONNECT.</summary>
    public string ClientId { get; }

    /// <summary>The CONNECT username. On the device plane this is <c>{tenant}:{credentialId}</c>.</summary>
    public string? Username { get; set; }

    /// <summary>
    /// The CONNECT password. On the device plane this is EMPTY, which is what selects
    /// access-token credential mode at the auth callout — an empty password is meaningful here,
    /// not a missing value.
    /// </summary>
    public string? Password { get; set; }

    /// <summary>
    /// Whether the broker should discard any existing session for this client id. Defaults to
    /// false so the broker holds subscriptions and undelivered QoS-1 messages across a
    /// reconnect — which is what stops commands being lost while a device is briefly away.
    /// </summary>
    public bool CleanSession { get; set; }

    /// <summary>The keep-alive interval sent in CONNECT.</summary>
    public TimeSpan KeepAlive { get; set; } = TimeSpan.FromSeconds(60);

    /// <summary>
    /// How TLS trust is established when <see cref="BrokerUri"/> uses the <c>ssl</c> scheme.
    /// Defaults to <see cref="MqttTrust.SystemRoots"/>; ignored for a plaintext broker.
    /// </summary>
    public MqttTrust Trust { get; set; } = MqttTrust.SystemRoots;
}

/// <summary>Thrown when a connection cannot be established or the broker refuses CONNECT.</summary>
public class MqttConnectionException : Exception
{
    /// <summary>Creates the exception.</summary>
    public MqttConnectionException(string message)
        : base(message)
    {
    }

    /// <summary>Creates the exception, preserving the underlying cause.</summary>
    public MqttConnectionException(string message, Exception? innerException)
        : base(message, innerException)
    {
    }
}

/// <summary>
/// Thrown when the broker REFUSED the connection outright (a CONNACK return code other than
/// "accepted") — a revoked credential, or a client id the auth callout will not admit.
/// </summary>
/// <remarks>
/// Distinct from a dial failure for the same reason a refused subscription is: retrying a dial
/// failure is right, and retrying a refusal is a hammering loop against a decision that will not
/// change. A caller that cannot tell them apart does the wrong one forever.
/// </remarks>
public sealed class MqttConnectRefusedException : MqttConnectionException
{
    /// <summary>Creates the exception.</summary>
    public MqttConnectRefusedException(string message)
        : base(message)
    {
    }
}

/// <summary>
/// Thrown when the broker REFUSED a subscription (SUBACK return code 0x80 or above).
/// </summary>
/// <remarks>
/// It is a distinct type, rather than a general connection failure, because the two demand
/// different responses and are reached by different causes. A connection failure is usually
/// transient and retrying is right; a refusal means the credential is not permitted to read that
/// topic, so the connection is healthy and retrying it forever changes nothing. A caller that
/// cannot tell them apart will silently do the wrong one.
/// </remarks>
public sealed class MqttSubscribeRefusedException : MqttConnectionException
{
    /// <summary>Creates the exception for a refused filter.</summary>
    public MqttSubscribeRefusedException(string topicFilter)
        : base($"the broker REFUSED the subscription to \"{topicFilter}\" (SUBACK 0x80). " +
               "The connection is fine and the client reports no error, so nothing else will " +
               "report this: the credential is most likely not permitted to read that topic")
    {
        TopicFilter = topicFilter;
    }

    /// <summary>The filter the broker refused.</summary>
    public string TopicFilter { get; }
}
