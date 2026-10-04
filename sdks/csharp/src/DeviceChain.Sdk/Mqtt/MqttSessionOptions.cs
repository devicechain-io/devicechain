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

    private int _maxConcurrentCommands = 1;
    private TimeSpan _commandShutdownTimeout = TimeSpan.FromSeconds(5);

    /// <summary>
    /// How many command handlers may run at once for this device. The default, 1, runs commands
    /// strictly one at a time in the order they arrive.
    /// </summary>
    /// <remarks>
    /// <para>
    /// Above 1, the receive callback hands each command to a bounded executor and returns, so
    /// later commands are received while earlier handlers run. At most this many handlers run at
    /// once; further commands wait and start in arrival order.
    /// </para>
    /// <para>
    /// 🔴 THE PLATFORM DELIVERS A DEVICE'S COMMANDS IN ORDER; ABOVE 1 THE SDK EXECUTES THEM IN THAT
    /// ORDER ONLY WITHIN A LANE (see <see cref="CommandLane"/>). A sequence whose order is its
    /// meaning, such as writing a firmware image and then executing it, must share a lane, or the
    /// two can run at the same time.
    /// </para>
    /// <para>
    /// Read once, when the session is constructed; changing it afterwards has no effect. A value
    /// below 1 throws, because 0 must not read as "unbounded".
    /// </para>
    /// </remarks>
    /// <exception cref="ArgumentOutOfRangeException">The value is below 1.</exception>
    public int MaxConcurrentCommands
    {
        get => _maxConcurrentCommands;
        set
        {
            if (value < 1)
            {
                throw new ArgumentOutOfRangeException(
                    nameof(value), value, "MaxConcurrentCommands must be at least 1");
            }

            _maxConcurrentCommands = value;
        }
    }

    /// <summary>
    /// Optionally maps a command to an ordered lane. Commands in the same lane run strictly in
    /// arrival order, each starting only after the previous one's handler has returned; different
    /// lanes run in parallel, subject to <see cref="MaxConcurrentCommands"/>.
    /// </summary>
    /// <remarks>
    /// <para>
    /// A null lane (no selector, or a selector that returns null) means NO ordering constraint.
    /// Someone who raises <see cref="MaxConcurrentCommands"/> is asking for parallelism, so
    /// defaulting null to one shared lane would make the setting a no-op until a selector was
    /// also written. To order everything except some commands, return one constant lane for
    /// everything else.
    /// </para>
    /// <para>
    /// Lanes are compared ordinally. A selector that throws does not run the command in no lane,
    /// which would silently drop the ordering asked for: the command is not run and is answered
    /// as failed. The selector is called on the receive path, so keep it cheap and do not call
    /// back into the session. Ignored when <see cref="MaxConcurrentCommands"/> is 1, where
    /// one-at-a-time already satisfies every lane. Read once, when the session is constructed.
    /// </para>
    /// </remarks>
    public Func<DeviceCommand, string?>? CommandLane { get; set; }

    /// <summary>
    /// How long disposing the session waits for RUNNING command handlers, after cancelling them.
    /// Applies only when <see cref="MaxConcurrentCommands"/> is above 1. Default 5 seconds.
    /// </summary>
    /// <exception cref="ArgumentOutOfRangeException">The value is negative.</exception>
    public TimeSpan CommandShutdownTimeout
    {
        get => _commandShutdownTimeout;
        set
        {
            if (value < TimeSpan.Zero)
            {
                throw new ArgumentOutOfRangeException(
                    nameof(value), value, "CommandShutdownTimeout must not be negative");
            }

            _commandShutdownTimeout = value;
        }
    }
}
