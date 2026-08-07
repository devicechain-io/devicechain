// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sdk.Transport;

namespace DeviceChain.Sdk.Ingest;

/// <summary>
/// Carries device events over the device-plane HTTP ingress —
/// <c>POST /{instanceId}/{tenant}/events</c> — which returns 202 on acceptance.
/// </summary>
/// <remarks>
/// This is the SDK's default carrier and the behaviour every existing caller already had; it was
/// extracted from <see cref="DeviceEventPublisher"/> unchanged when the MQTT carrier arrived.
/// </remarks>
public sealed class HttpDeviceEventCarrier : IDeviceEventCarrier
{
    private readonly IHttpTransport _transport;
    private readonly Uri _ingressOrigin;
    private readonly string _instanceId;
    private readonly string _tenant;

    /// <summary>Creates an HTTP carrier.</summary>
    /// <param name="transport">The HTTP transport.</param>
    /// <param name="ingressOrigin">
    /// The device-plane ingress origin. This is a SEPARATE listener from the GraphQL/`/api`
    /// ingress — the cluster ingress does not route <c>/{instanceId}/{tenant}/events</c>.
    /// </param>
    /// <param name="instanceId">The instance segment.</param>
    /// <param name="tenant">The tenant segment.</param>
    public HttpDeviceEventCarrier(IHttpTransport transport, Uri ingressOrigin, string instanceId, string tenant)
    {
        _transport = transport ?? throw new ArgumentNullException(nameof(transport));
        _ingressOrigin = ingressOrigin ?? throw new ArgumentNullException(nameof(ingressOrigin));
        _instanceId = instanceId ?? throw new ArgumentNullException(nameof(instanceId));
        _tenant = tenant ?? throw new ArgumentNullException(nameof(tenant));
    }

    /// <inheritdoc />
    public async Task SendAsync(string deviceToken, byte[] jsonEvent, CancellationToken cancellationToken)
    {
        string path = $"/{_instanceId}/{_tenant}/events";

        // No Authorization header — a device authenticates by presenting its credential IN the
        // body (credentialType + credentialId), not a Bearer.
        var request = new HttpTransportRequest
        {
            Uri = new Uri(_ingressOrigin, path),
            Body = jsonEvent,
            ContentType = "application/json",
        };

        HttpTransportResponse response;
        try
        {
            response = await _transport.SendAsync(request, cancellationToken).ConfigureAwait(false);
        }
        catch (Exception ex) when (ex is not OperationCanceledException || !cancellationToken.IsCancellationRequested)
        {
            throw new GraphQlRequestException(ex.Message, 0);
        }

        const int accepted = 202;
        if (response.Status != accepted)
        {
            string detail = response.Body.Length == 0 ? "" : Encoding.UTF8.GetString(response.Body);
            throw new GraphQlRequestException(
                $"ingress {path} returned {response.Status}: {detail.Trim()}",
                response.Status);
        }
    }
}

/// <summary>
/// Carries device events over an <see cref="MqttDeviceSession"/>, publishing to the device's own
/// events topic at QoS 1.
/// </summary>
/// <remarks>
/// <para>
/// 🔑 THIS IS THE CARRIER THAT REACHES THE CAPTURE STREAM. The durability capture path is fed by
/// the MQTT route only, so telemetry published over HTTP has never touched it — which is why the
/// simulators, all of which emit over HTTP ingress, have been unable to exercise ingest durability
/// at all. Publishing over this carrier is what makes that path drivable.
/// </para>
/// <para>
/// It is DEVICE-SCOPED where the HTTP carrier is device-agnostic: a session's client id and its
/// broker grant bind it to exactly one device. That asymmetry is intrinsic to MQTT here, not a
/// design choice, which is why the token is verified rather than trusted.
/// </para>
/// </remarks>
public sealed class MqttDeviceEventCarrier : IDeviceEventCarrier
{
    private readonly MqttDeviceSession _session;
    private readonly string _deviceToken;

    /// <summary>Creates a carrier bound to one device's session.</summary>
    public MqttDeviceEventCarrier(MqttDeviceSession session, string deviceToken)
    {
        _session = session ?? throw new ArgumentNullException(nameof(session));
        if (string.IsNullOrEmpty(deviceToken))
        {
            throw new ArgumentException("a device token is required", nameof(deviceToken));
        }

        _deviceToken = deviceToken;
    }

    /// <inheritdoc />
    /// <remarks>
    /// 🔴 IT REFUSES TO EMIT FOR ANOTHER DEVICE, LOCALLY AND LOUDLY. The gateway checks that the
    /// body's device matches the topic it arrived on and DEAD-LETTERS the event when they differ —
    /// so publishing another device's event down this connection loses it silently, somewhere the
    /// publisher will never see. Failing here turns a silent downstream drop into an exception at
    /// the call site that caused it.
    /// </remarks>
    public Task SendAsync(string deviceToken, byte[] jsonEvent, CancellationToken cancellationToken)
    {
        if (!string.Equals(deviceToken, _deviceToken, StringComparison.Ordinal))
        {
            throw new InvalidOperationException(
                $"this MQTT session belongs to device \"{_deviceToken}\" and cannot emit for " +
                $"\"{deviceToken}\": the broker grant confines the connection to one device, and the " +
                "gateway dead-letters an event whose body names a different device than its topic");
        }

        return _session.PublishEventAsync(jsonEvent, cancellationToken);
    }
}
