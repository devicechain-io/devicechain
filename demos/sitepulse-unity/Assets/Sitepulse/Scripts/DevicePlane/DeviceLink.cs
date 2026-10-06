// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Mqtt;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.DevicePlane
{
    /// <summary>A session's state, as the plane names it (the SDK's own enum stays behind <see cref="SdkDeviceLink"/>).</summary>
    public enum LinkState { Starting, Ready, Reconnecting, Blind, Stopped }

    /// <summary>
    /// One device's connection to the broker. The SDK session is the real one
    /// (<see cref="SdkDeviceLink"/>); a test double is the other, which is how the plane's start
    /// order, readiness and teardown are tested without a broker. Callbacks arrive on pool threads.
    /// </summary>
    public interface IDeviceLink : IAsyncDisposable
    {
        /// <summary>Raised on a pool thread whenever the session changes state.</summary>
        event Action<LinkState> StateChanged;

        /// <summary>True while a publish can succeed (connected and subscribed).</summary>
        bool CanPublish { get; }

        /// <summary>
        /// Connects and subscribes. Every command that arrives is handed to <paramref name="handler"/> (on a pool
        /// thread, with the session's arrival order in <see cref="DeviceCommand.Sequence"/>) and answered with what it returns.
        /// </summary>
        Task StartAsync(CommandHandler handler, CancellationToken cancellationToken);

        Task PublishAsync(Sample sample, CancellationToken cancellationToken);
    }

    public interface IDeviceLinkFactory
    {
        /// <summary>One link for one device. Never called twice for the same device by one plane: two live sessions with one client id evict each other.</summary>
        IDeviceLink Create(string externalId, string deviceToken, string credentialId);
    }
}
