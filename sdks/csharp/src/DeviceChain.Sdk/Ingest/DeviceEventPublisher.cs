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

    private readonly IHttpTransport _transport;
    private readonly Uri _ingressOrigin;
    private readonly string _instanceId;
    private readonly string _tenant;

    /// <param name="transport">The HTTP transport (default <see cref="HttpClientTransport"/>; Unity WebGL injects its own).</param>
    /// <param name="ingressOrigin">
    /// The device-plane ingress origin. This is a SEPARATE listener from the GraphQL/`/api` ingress
    /// (event-sources' HTTP port) — the cluster ingress does not route `/{instanceId}/{tenant}/events`,
    /// so this must point at wherever that endpoint is exposed, not the GraphQL origin.
    /// </param>
    /// <param name="instanceId">The instance segment (ADR-048).</param>
    /// <param name="tenant">The tenant segment.</param>
    public DeviceEventPublisher(IHttpTransport transport, Uri ingressOrigin, string instanceId, string tenant)
    {
        _transport = transport ?? throw new ArgumentNullException(nameof(transport));
        _ingressOrigin = ingressOrigin ?? throw new ArgumentNullException(nameof(ingressOrigin));
        _instanceId = instanceId ?? throw new ArgumentNullException(nameof(instanceId));
        _tenant = tenant ?? throw new ArgumentNullException(nameof(tenant));
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
        return PostAsync(evt, cancellationToken);
    }

    private async Task PostAsync(MeasurementEvent evt, CancellationToken cancellationToken)
    {
        byte[] body = JsonSerializer.SerializeToUtf8Bytes(evt, SdkJson.Default.MeasurementEvent);
        string path = $"/{_instanceId}/{_tenant}/events";

        // No Authorization header — a device authenticates by presenting its credential IN the body
        // (credentialType + credentialId), not a Bearer (ADR-014).
        var request = new HttpTransportRequest
        {
            Uri = new Uri(_ingressOrigin, path),
            Body = body,
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

    // RFC3339 in UTC without fractional seconds — matches the Go sim's time.RFC3339 emit.
    private static string FormatRfc3339(DateTimeOffset when) =>
        when.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ", CultureInfo.InvariantCulture);
}
