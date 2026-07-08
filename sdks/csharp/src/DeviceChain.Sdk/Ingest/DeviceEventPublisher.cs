// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Json;

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

    private readonly HttpClient _http;
    private readonly Uri _ingressOrigin;
    private readonly string _instanceId;
    private readonly string _tenant;

    /// <param name="http">A shared HttpClient (BaseAddress not read/mutated — absolute URIs are built).</param>
    /// <param name="ingressOrigin">
    /// The device-plane ingress origin. This is a SEPARATE listener from the GraphQL/`/api` ingress
    /// (event-sources' HTTP port) — the cluster ingress does not route `/{instanceId}/{tenant}/events`,
    /// so this must point at wherever that endpoint is exposed, not the GraphQL origin.
    /// </param>
    /// <param name="instanceId">The instance segment (ADR-048).</param>
    /// <param name="tenant">The tenant segment.</param>
    public DeviceEventPublisher(HttpClient http, Uri ingressOrigin, string instanceId, string tenant)
    {
        _http = http ?? throw new ArgumentNullException(nameof(http));
        _ingressOrigin = ingressOrigin ?? throw new ArgumentNullException(nameof(ingressOrigin));
        _instanceId = instanceId ?? throw new ArgumentNullException(nameof(instanceId));
        _tenant = tenant ?? throw new ArgumentNullException(nameof(tenant));
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
        var uri = new Uri(_ingressOrigin, path);

        // No Authorization header — a device authenticates by presenting its credential IN the body
        // (credentialType + credentialId), not a Bearer (ADR-014).
        using var request = new HttpRequestMessage(HttpMethod.Post, uri)
        {
            Content = new ByteArrayContent(body),
        };
        request.Content.Headers.ContentType = new MediaTypeHeaderValue("application/json");

        HttpResponseMessage response;
        try
        {
            response = await _http.SendAsync(request, cancellationToken).ConfigureAwait(false);
        }
        catch (Exception ex) when (ex is not OperationCanceledException || !cancellationToken.IsCancellationRequested)
        {
            throw new GraphQlRequestException(ex.Message, 0);
        }

        using (response)
        {
            if (response.StatusCode != HttpStatusCode.Accepted)
            {
                string detail;
                try
                {
                    detail = await response.Content.ReadAsStringAsync().ConfigureAwait(false);
                }
                catch (IOException)
                {
                    detail = "";
                }
                throw new GraphQlRequestException(
                    $"ingress {path} returned {(int)response.StatusCode}: {detail.Trim()}",
                    (int)response.StatusCode);
            }
        }
    }

    // RFC3339 in UTC without fractional seconds — matches the Go sim's time.RFC3339 emit.
    private static string FormatRfc3339(DateTimeOffset when) =>
        when.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ", CultureInfo.InvariantCulture);
}
