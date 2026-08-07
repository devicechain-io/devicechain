// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Net.Http;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Json;
using DeviceChain.Sdk.Transport;

namespace DeviceChain.Sdk.Ingest;

/// <summary>
/// Publishes telemetry over the DEVICE-plane HTTP ingress — <c>POST /{instanceId}/{tenant}/events</c>
/// — exactly as a real device does (ADR-025/048): the device authenticates by presenting its
/// credential IN the body (credentialType + credentialId, ADR-014), not a Bearer header. This is
/// the emit leg an interactive twin uses (contract §3c). The ingress accepts into the pipeline
/// asynchronously and returns 202.
/// </summary>
public sealed class DeviceEventPublisher
{
    /// <summary>The device-credential type a token-authenticated device presents (matches the sim + event-sources).</summary>
    public const string AccessTokenCredential = "ACCESS_TOKEN";

    private readonly IDeviceEventCarrier _carrier;

    /// <param name="transport">The HTTP transport (default <see cref="HttpClientTransport"/>; Unity WebGL injects its own).</param>
    /// <param name="ingressOrigin">
    /// The device-plane ingress origin. This is a SEPARATE listener from the GraphQL/`/api` ingress
    /// (event-sources' HTTP port) — the cluster ingress does not route `/{instanceId}/{tenant}/events`,
    /// so this must point at wherever that endpoint is exposed, not the GraphQL origin.
    /// </param>
    /// <param name="instanceId">The instance segment (ADR-048).</param>
    /// <param name="tenant">The tenant segment.</param>
    public DeviceEventPublisher(IHttpTransport transport, Uri ingressOrigin, string instanceId, string tenant)
        : this(new HttpDeviceEventCarrier(transport, ingressOrigin, instanceId, tenant))
    {
    }

    /// <summary>
    /// Publishes over an explicit carrier — HTTP (the default above) or MQTT.
    /// </summary>
    /// <remarks>
    /// The publisher builds the body once and hands it to whichever carrier moves it, so the two
    /// wires cannot drift. Only the MQTT carrier reaches the durability capture stream.
    /// </remarks>
    /// <param name="carrier">The carrier that moves the serialized event.</param>
    public DeviceEventPublisher(IDeviceEventCarrier carrier)
    {
        _carrier = carrier ?? throw new ArgumentNullException(nameof(carrier));
    }

    /// <summary>Convenience overload for the plain-.NET path: wraps a shared <see cref="HttpClient"/>.</summary>
    /// <param name="http">A shared HttpClient (BaseAddress not read/mutated — absolute URIs are built).</param>
    /// <param name="ingressOrigin">The device-plane ingress origin (see the transport overload).</param>
    /// <param name="instanceId">The instance segment (ADR-048).</param>
    /// <param name="tenant">The tenant segment.</param>
    public DeviceEventPublisher(HttpClient http, Uri ingressOrigin, string instanceId, string tenant)
        : this(new HttpClientTransport(http), ingressOrigin, instanceId, tenant)
    {
    }

    /// <summary>
    /// Emits one Measurement event carrying every entry in <paramref name="metrics"/> as a single
    /// measurements map (the rich-emit shape). Values go on the wire as invariant strings
    /// (self-describing decode downstream, ADR-016). Throws <see cref="GraphQlRequestException"/>
    /// (as the SDK's transport error) on a non-202 response.
    /// </summary>
    public Task EmitMeasurementsAsync(
        string deviceToken,
        string credentialId,
        IReadOnlyDictionary<string, double> metrics,
        DateTimeOffset? occurredTime = null,
        CancellationToken cancellationToken = default)
    {
        if (string.IsNullOrEmpty(deviceToken)) throw new ArgumentException("device token required", nameof(deviceToken));
        if (string.IsNullOrEmpty(credentialId)) throw new ArgumentException("credential id required", nameof(credentialId));

        string now = FormatRfc3339(occurredTime ?? DateTimeOffset.UtcNow);
        var values = new Dictionary<string, string>(metrics.Count);
        foreach (KeyValuePair<string, double> m in metrics)
        {
            values[m.Key] = m.Value.ToString(CultureInfo.InvariantCulture);
        }

        var evt = new MeasurementEvent
        {
            Device = deviceToken,
            EventType = "Measurement",
            OccurredTime = now,
            Payload = new MeasurementPayload
            {
                Entries = { new MeasurementEntry { Measurements = values, OccurredTime = now } },
            },
            CredentialType = AccessTokenCredential,
            CredentialId = credentialId,
        };
        return SendAsync(deviceToken, evt, cancellationToken);
    }

    // The body is built HERE, once, and handed to whichever carrier moves it — which is what makes
    // "identical payload, different carrier" true by construction rather than by discipline. The
    // equality is pinned by a test that captures what reached each carrier and compares the bytes.
    private Task SendAsync(string deviceToken, MeasurementEvent evt, CancellationToken cancellationToken) =>
        _carrier.SendAsync(
            deviceToken,
            JsonSerializer.SerializeToUtf8Bytes(evt, SdkJson.Default.MeasurementEvent),
            cancellationToken);

    // RFC3339 in UTC WITH fractional seconds ("O" is .NET's round-trip form: always Z,
    // always seven fractional digits, and Go's time.Parse accepts it).
    //
    // 🔴 The fractional part is load-bearing, not cosmetic, and this previously emitted
    // whole seconds. A base event is keyed by (tenant, device, event_type, occurred_time),
    // so two readings this SDK published inside one wall-clock second carried an identical
    // key — and the second one's envelope was silently discarded on insert. A device
    // sampling faster than once per second lost every reading after the first in each
    // second, and lost it quietly: the publish returned 202.
    //
    // The old comment justified the truncation as matching "the Go sim's time.RFC3339
    // emit". That had stopped being true — sims/dc-simulator/sim/emit.go moved to
    // RFC3339Nano precisely because second-identical emits were collapsing, and its
    // comment explains the mechanism in full. The simulator was corrected; the SDK real
    // devices actually use was not. Emitting sub-second time is also just honest: a device
    // sampling sub-second HAS a sub-second timestamp.
    private static string FormatRfc3339(DateTimeOffset when) =>
        when.UtcDateTime.ToString("O", CultureInfo.InvariantCulture);
}
