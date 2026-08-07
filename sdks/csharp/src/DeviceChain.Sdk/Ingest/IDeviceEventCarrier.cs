// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sdk.Ingest;

/// <summary>
/// Carries an already-serialized device event to the platform. The carrier chooses the wire;
/// <see cref="DeviceEventPublisher"/> owns what goes on it.
/// </summary>
/// <remarks>
/// <para>
/// 🔑 THE SPLIT IS "WHO BUILDS THE BODY" vs "WHO MOVES IT", AND IT IS LOAD-BEARING RATHER THAN
/// TIDY. The HTTP ingress and the MQTT gateway decode the SAME <c>JsonEvent</c> shape, so the
/// bytes must be identical on both — only the carrier differs. Letting each transport build its
/// own body is how the two drift, and the drift would be invisible: both would still look like
/// valid events.
/// </para>
/// <para>
/// 🔴 AND THE BODY MUST CARRY ITS OWN CREDENTIALS ON BOTH CARRIERS. It is tempting to assume MQTT
/// can omit them, since the connection is already authenticated at the broker — but the flag that
/// lets a transport-authenticated event skip in-body credentials is set only by the LwM2M and
/// Sparkplug adapters. The device-plane gateway never stamps it, so under the default
/// <c>deviceAuthMode: required</c> an MQTT publish without <c>credentialType</c>/<c>credentialId</c>
/// in the body is rejected. "Identical payload, different carrier" is therefore a requirement, not
/// a convenience.
/// </para>
/// </remarks>
public interface IDeviceEventCarrier
{
    /// <summary>
    /// Delivers one serialized event on behalf of <paramref name="deviceToken"/>.
    /// </summary>
    /// <param name="deviceToken">
    /// The device the event belongs to. A device-scoped carrier verifies this against its own
    /// identity rather than trusting it.
    /// </param>
    /// <param name="jsonEvent">The serialized event body — passed through unmodified.</param>
    /// <param name="cancellationToken">Cancels the send.</param>
    Task SendAsync(string deviceToken, byte[] jsonEvent, CancellationToken cancellationToken);
}
