// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sdk.Transport;

/// <summary>
/// The default <see cref="IHttpTransport"/> — a thin wrapper over a shared
/// <see cref="HttpClient"/>. It builds absolute URIs, so the client's <c>BaseAddress</c> is
/// neither read nor mutated. The body is materialized here (inside the transport) so a read
/// failure surfaces from <see cref="SendAsync"/> like any other transport error. This is the
/// transport used everywhere except Unity WebGL, which injects its own.
/// </summary>
public sealed class HttpClientTransport : IHttpTransport
{
    private readonly HttpClient _http;

    /// <param name="http">A shared HttpClient (its BaseAddress is not read or mutated).</param>
    public HttpClientTransport(HttpClient http)
    {
        _http = http ?? throw new ArgumentNullException(nameof(http));
    }

    /// <inheritdoc />
    public async Task<HttpTransportResponse> SendAsync(HttpTransportRequest request, CancellationToken cancellationToken)
    {
        if (request is null) throw new ArgumentNullException(nameof(request));

        using var message = new HttpRequestMessage(new HttpMethod(request.Method), request.Uri);
        if (request.Body is not null)
        {
            message.Content = new ByteArrayContent(request.Body);
            if (!string.IsNullOrEmpty(request.ContentType))
            {
                message.Content.Headers.ContentType = new MediaTypeHeaderValue(request.ContentType);
            }
        }
        if (!string.IsNullOrEmpty(request.BearerToken))
        {
            message.Headers.Authorization = new AuthenticationHeaderValue("Bearer", request.BearerToken);
        }

        using HttpResponseMessage response = await _http.SendAsync(message, cancellationToken).ConfigureAwait(false);
        byte[] body = await response.Content.ReadAsByteArrayAsync().ConfigureAwait(false);
        return new HttpTransportResponse { Status = (int)response.StatusCode, Body = body };
    }
}
