// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Ingest;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sdk.Transport;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.DevicePlane
{
    /// <summary>
    /// A device's link over the SDK's <see cref="MqttDeviceSession"/>, publishing through the SDK's
    /// own <see cref="DeviceEventPublisher"/> over the session's carrier (no hand-written framing).
    /// The credential id goes into the session options and into the event body the SDK builds, and
    /// nowhere else: it is never formatted into a string here.
    /// </summary>
    public sealed class SdkDeviceLink : IDeviceLink
    {
        readonly MqttDeviceSession session;
        readonly DeviceEventPublisher publisher;
        readonly string deviceToken;
        readonly string credentialId;

        public SdkDeviceLink(MqttSessionOptions options, string deviceToken, string credentialId)
        {
            this.deviceToken = deviceToken;
            this.credentialId = credentialId;
            session = new MqttDeviceSession(options);
            publisher = new DeviceEventPublisher(new MqttDeviceEventCarrier(session, deviceToken));
            session.StateChanged += s => StateChanged?.Invoke(Map(s));
        }

        public event Action<LinkState> StateChanged;

        public bool CanPublish => session.State == MqttSessionState.Ready;

        static LinkState Map(MqttSessionState s)
        {
            switch (s)
            {
                case MqttSessionState.Ready: return LinkState.Ready;
                case MqttSessionState.Reconnecting: return LinkState.Reconnecting;
                case MqttSessionState.Blind: return LinkState.Blind;
                case MqttSessionState.Stopped: return LinkState.Stopped;
                default: return LinkState.Starting;
            }
        }

        public Task StartAsync(string refusalReason, Action<string> onCommand, CancellationToken cancellationToken)
            => session.StartAsync((command, _) =>
            {
                onCommand(command.Name);
                return Task.FromResult(CommandOutcome.Failed(refusalReason));
            }, cancellationToken);

        public Task PublishAsync(Sample sample, CancellationToken cancellationToken)
        {
            if (sample.Kind == SampleKind.Measurement)
                return publisher.EmitMeasurementsAsync(deviceToken, credentialId, sample.Values, sample.OccurredUtc, cancellationToken);
            var fix = new LocationFix(sample.Latitude, sample.Longitude)
            {
                Elevation = sample.Elevation,
                Speed = sample.SpeedMps,
                Heading = sample.HeadingDegrees,
            };
            return publisher.EmitLocationAsync(deviceToken, credentialId, fix, sample.OccurredUtc, cancellationToken);
        }

        public ValueTask DisposeAsync() => session.DisposeAsync();
    }

    /// <summary>
    /// Builds the session options of design section 2.4: the broker's CA pinned from the file (never
    /// an accept-anything trust), four concurrent command handlers, no command lane (a shared lane
    /// would queue a newer movement command behind an older one, the opposite of supersede), and a
    /// five second command shutdown.
    /// </summary>
    public sealed class SdkDeviceLinkFactory : IDeviceLinkFactory
    {
        readonly RunnerConfig config;
        readonly byte[] caPem;

        public SdkDeviceLinkFactory(RunnerConfig config, byte[] caPem)
        {
            this.config = config ?? throw new ArgumentNullException(nameof(config));
            this.caPem = caPem ?? throw new ArgumentNullException(nameof(caPem));
        }

        public IDeviceLink Create(string externalId, string deviceToken, string credentialId)
        {
            var options = new MqttSessionOptions(new Uri(config.MqttBroker), config.InstanceId, config.Tenant, deviceToken, credentialId)
            {
                Trust = MqttTrust.PinnedCa(caPem),
                MaxConcurrentCommands = 4,
                CommandLane = null,
                CommandShutdownTimeout = TimeSpan.FromSeconds(5),
            };
            return new SdkDeviceLink(options, deviceToken, credentialId);
        }
    }
}
