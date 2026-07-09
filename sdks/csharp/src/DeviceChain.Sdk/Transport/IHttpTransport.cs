// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sdk.Transport;

/// <summary>
/// The one-way-request half of the client wire, abstracted so the SDK never hard-depends on
/// <c>System.Net.Http.HttpClient</c>. On plain .NET the default <see cref="HttpClientTransport"/>
/// is used; under Unity WebGL/IL2CPP (where <c>HttpClient</c> does not work) a host injects a
/// transport backed by <c>UnityWebRequest</c> / browser <c>fetch</c>. The transport returns the
/// full response for EVERY status (2xx and error alike) — status classification is the caller's
/// job — and throws only on a genuine transport failure (connection refused, timeout).
/// </summary>
public interface IHttpTransport
{
    /// <summary>Sends one request and returns the materialized response (body already read).</summary>
    Task<HttpTransportResponse> SendAsync(HttpTransportRequest request, CancellationToken cancellationToken);
}

/// <summary>A transport-agnostic request. Absolute <see cref="Uri"/> only — the SDK builds it.</summary>
public sealed class HttpTransportRequest
{
    /// <summary>The absolute request URI.</summary>
    public Uri Uri { get; init; } = null!;

    /// <summary>The HTTP method (the SDK only issues POST today).</summary>
    public string Method { get; init; } = "POST";

    /// <summary>The request body bytes, or null for no body.</summary>
    public byte[]? Body { get; init; }

    /// <summary>The body's content type (e.g. <c>application/json</c>), or null when there is no body.</summary>
    public string? ContentType { get; init; }

    /// <summary>A Bearer token to attach as <c>Authorization: Bearer …</c>, or null for no auth header.</summary>
    public string? BearerToken { get; init; }
}

/// <summary>A transport-agnostic response with the body already materialized.</summary>
public sealed class HttpTransportResponse
{
    /// <summary>The HTTP status code (0 is reserved for callers that synthesize a transport error).</summary>
    public int Status { get; init; }

    /// <summary>The response body bytes (empty, never null).</summary>
    public byte[] Body { get; init; } = Array.Empty<byte>();
}
