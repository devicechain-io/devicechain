// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Sdk.Transport;

namespace DeviceChain.Sdk.Mqtt;

/// <summary>Everything one device's MQTT session needs.</summary>
public sealed class MqttSessionOptions
{
    /// <summary>Creates session options for one device.</summary>
    /// <param name="brokerUri">
    /// The MQTT gateway, e.g. <c>ssl://127.0.0.1:1883</c>. A sim handshake's <c>mqttBroker</c>
    /// endpoint can be passed through verbatim.
    /// </param>
    /// <param name="instanceId">The DeviceChain instance id.</param>
    /// <param name="tenant">The tenant the device belongs to.</param>
    /// <param name="deviceToken">The device's token.</param>
    /// <param name="credentialId">
    /// The device's access-token credential id — the bearer presented as the CONNECT password's
    /// counterpart in the username.
    /// </param>
    public MqttSessionOptions(Uri brokerUri, string instanceId, string tenant, string deviceToken, string credentialId)
    {
        BrokerUri = brokerUri ?? throw new ArgumentNullException(nameof(brokerUri));
        InstanceId = instanceId;
        Tenant = tenant;
        DeviceToken = deviceToken;
        CredentialId = credentialId;
    }

    /// <summary>The MQTT gateway address.</summary>
    public Uri BrokerUri { get; }

    /// <summary>The instance id.</summary>
    public string InstanceId { get; }

    /// <summary>The tenant.</summary>
    public string Tenant { get; }

    /// <summary>The device token.</summary>
    public string DeviceToken { get; }

    /// <summary>The device's credential id.</summary>
    public string CredentialId { get; }

    /// <summary>
    /// An optional client-id discriminator, for a device that deliberately wants more than one
    /// session. Leave null for the single-session case every first-party client uses.
    /// </summary>
    public string? ClientIdDiscriminator { get; set; }

    /// <summary>How TLS trust is established. Defaults to system roots.</summary>
    public MqttTrust Trust { get; set; } = MqttTrust.SystemRoots;

    /// <summary>The MQTT keep-alive interval.</summary>
    public TimeSpan KeepAlive { get; set; } = TimeSpan.FromSeconds(60);

    /// <summary>
    /// How long a connect or confirmed-subscribe may take before it is abandoned. Bounds startup
    /// so a broker that accepts the connection and then stops answering fails rather than hangs.
    /// </summary>
    public TimeSpan OperationTimeout { get; set; } = TimeSpan.FromSeconds(30);

    /// <summary>
    /// How many command tokens to remember for de-duplication.
    /// </summary>
    /// <remarks>
    /// 🔑 IT IS BOUNDED, AND THE UNBOUNDED VERSION IS THE TEMPTING ONE. Delivery is at-least-once,
    /// so the same token can arrive twice and the receiver must not act twice — which needs a
    /// memory of what it has seen. A plain growing set is fine for a test cohort that runs for
    /// minutes and is a slow leak in an SDK a machine runs for months. The bound is what makes it
    /// safe to leave running; redelivery windows are short, so remembering the recent past is
    /// enough.
    /// </remarks>
    public int CommandHistorySize { get; set; } = 256;

    /// <summary>The delay before the first reconnect attempt; it backs off from here.</summary>
    public TimeSpan ReconnectInitialDelay { get; set; } = TimeSpan.FromSeconds(1);

    /// <summary>The ceiling on reconnect backoff.</summary>
    public TimeSpan ReconnectMaxDelay { get; set; } = TimeSpan.FromSeconds(30);
}
