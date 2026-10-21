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
/// 🔴 WHETHER THE BODY CARRIES A CREDENTIAL IS THE CARRIER'S DECLARATION, NOT A GUESS. The two
/// wires differ here on purpose. HTTP and an external broker authenticate nothing at the
/// transport, so the body must present <c>credentialType</c>/<c>credentialId</c>. The PLATFORM
/// MQTT broker authenticates the CONNECT and asserts the device from the topic, and it REFUSES any
/// event whose body carries a credential field (the broker has already written that body to a
/// stream before any platform code can look at it, so a secret in it is a secret at rest in
/// plaintext). Everything else in the body stays identical across carriers.
/// See <see cref="CarriesBodyCredential"/>.
/// </para>
/// </remarks>
public interface IDeviceEventCarrier
{
    /// <summary>
    /// True when this carrier's wire authenticates the device only through the event body (HTTP,
    /// an external broker), so the body must carry <c>credentialType</c>/<c>credentialId</c>. False
    /// for the platform MQTT broker, which authenticated the connection and refuses a body that
    /// carries one. Implementations return a constant: the answer is a property of the wire.
    /// </summary>
    bool CarriesBodyCredential { get; }

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
