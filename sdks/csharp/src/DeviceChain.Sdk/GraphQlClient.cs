// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.IO;
using System.Net.Http;
using System.Text.Json;
using System.Text.Json.Serialization.Metadata;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Json;
using DeviceChain.Sdk.Transport;

namespace DeviceChain.Sdk;

/// <summary>
/// Resolves the current access token for outgoing requests, or null when unauthenticated —
/// the seam an <see cref="Auth.AuthSession"/> (or any host) plugs into, so the SDK owns no
/// credential storage. Called per request so a refreshed token is picked up. Mirrors the TS
/// SDK's token-getter.
/// </summary>
public delegate ValueTask<string?> TokenProvider(CancellationToken cancellationToken);

/// <summary>Per-request options.</summary>
public sealed class RequestOptions
{
    /// <summary>Skip attaching the Bearer token (login / selectTenant / refresh run unauthenticated).</summary>
    public bool Anonymous { get; init; }
}

/// <summary>
/// A minimal GraphQL-over-HTTP client (the TS SDK's <c>gql()</c> in .NET). Each functional
/// area serves its own <c>/api/{area}/graphql</c> endpoint; the request carries the access
/// token Bearer unless anonymous. Serialization is source-generated (AOT/IL2CPP-safe): a
/// caller passes the <see cref="JsonTypeInfo{T}"/> for its variables + data types, so the
/// SDK never reflects over user types.
/// </summary>
public sealed class GraphQlClient
{
    private readonly IHttpTransport _transport;
    private readonly Uri _origin;
    private readonly TokenProvider? _tokenProvider;

    /// <param name="transport">The HTTP transport (default <see cref="HttpClientTransport"/>; Unity WebGL injects its own).</param>
    /// <param name="origin">The platform origin (https://host) the /api/{area}/graphql paths hang off.</param>
    /// <param name="tokenProvider">Resolves the Bearer token per request; null = always anonymous.</param>
    public GraphQlClient(IHttpTransport transport, Uri origin, TokenProvider? tokenProvider = null)
    {
        _transport = transport ?? throw new ArgumentNullException(nameof(transport));
        _origin = origin ?? throw new ArgumentNullException(nameof(origin));
        _tokenProvider = tokenProvider;
    }

    /// <summary>Convenience overload for the plain-.NET path: wraps a shared <see cref="HttpClient"/>.</summary>
    /// <param name="http">A shared HttpClient (its BaseAddress is not read or mutated — the SDK builds absolute URIs).</param>
    /// <param name="origin">The platform origin (https://host) the /api/{area}/graphql paths hang off.</param>
    /// <param name="tokenProvider">Resolves the Bearer token per request; null = always anonymous.</param>
    public GraphQlClient(HttpClient http, Uri origin, TokenProvider? tokenProvider = null)
        : this(new HttpClientTransport(http), origin, tokenProvider)
    {
    }

    /// <summary>
    /// Executes <paramref name="query"/> against <paramref name="area"/> with typed variables
    /// and returns the typed <c>data</c>, throwing <see cref="GraphQlRequestException"/> on a
    /// transport error or a GraphQL <c>errors</c> payload.
    /// </summary>
    public async Task<TData> SendAsync<TVars, TData>(
        Area area,
        string query,
        TVars variables,
        JsonTypeInfo<TVars> variablesInfo,
        JsonTypeInfo<TData> dataInfo,
        RequestOptions? options = null,
        CancellationToken cancellationToken = default)
    {
        byte[] body = BuildRequestBody(query, variables, variablesInfo);

        string? token = null;
        if (options?.Anonymous != true && _tokenProvider is not null)
        {
            token = await _tokenProvider(cancellationToken).ConfigureAwait(false);
        }

        var request = new HttpTransportRequest
        {
            Uri = new Uri(_origin, Areas.Path(area)),
            Body = body,
            ContentType = "application/json",
            BearerToken = string.IsNullOrEmpty(token) ? null : token,
        };

        HttpTransportResponse response;
        try
        {
            response = await _transport.SendAsync(request, cancellationToken).ConfigureAwait(false);
        }
        // Wrap every transport failure EXCEPT genuine caller cancellation. An HttpClient timeout
        // also throws OperationCanceledException but with the caller's token NOT signalled, so it
        // surfaces as the SDK's exception type rather than masquerading as a cancel.
        catch (Exception ex) when (ex is not OperationCanceledException || !cancellationToken.IsCancellationRequested)
        {
            throw new GraphQlRequestException(ex.Message, 0);
        }

        int status = response.Status;
        if (status is < 200 or >= 300)
        {
            throw new GraphQlRequestException($"Request failed ({status})", status, TryReadErrors(response.Body));
        }
        return ParseData(response.Body, dataInfo, status);
    }

    private static byte[] BuildRequestBody<TVars>(string query, TVars variables, JsonTypeInfo<TVars> variablesInfo)
    {
        using var stream = new MemoryStream();
        using (var writer = new Utf8JsonWriter(stream))
        {
            writer.WriteStartObject();
            writer.WriteString("query", query);
            writer.WritePropertyName("variables");
            JsonSerializer.Serialize(writer, variables, variablesInfo);
            writer.WriteEndObject();
        }
        return stream.ToArray();
    }

    private static TData ParseData<TData>(byte[] bytes, JsonTypeInfo<TData> dataInfo, int status)
    {
        using var doc = JsonDocument.Parse(bytes);
        JsonElement root = doc.RootElement;

        if (root.TryGetProperty("errors", out JsonElement errors)
            && errors.ValueKind == JsonValueKind.Array
            && errors.GetArrayLength() > 0)
        {
            GraphQlError[] parsed = ReadErrors(errors);
            string message = parsed.Length > 0 ? parsed[0].Message : "GraphQL error";
            throw new GraphQlRequestException(message, status, parsed);
        }

        if (!root.TryGetProperty("data", out JsonElement data) || data.ValueKind == JsonValueKind.Null)
        {
            throw new GraphQlRequestException("Empty GraphQL response", status);
        }

        // GetRawText + the string overload is the JsonTypeInfo path supported across all
        // System.Text.Json versions (netstandard2.1 + net8.0).
        return JsonSerializer.Deserialize(data.GetRawText(), dataInfo)
            ?? throw new GraphQlRequestException("Null GraphQL data", status);
    }

    // TryReadErrors best-effort extracts a GraphQL errors array from a non-2xx body (which may
    // be plain text, not JSON) so an HTTP-level failure still surfaces a useful message.
    private static GraphQlError[]? TryReadErrors(byte[] bytes)
    {
        try
        {
            using var doc = JsonDocument.Parse(bytes);
            if (doc.RootElement.TryGetProperty("errors", out JsonElement errors)
                && errors.ValueKind == JsonValueKind.Array)
            {
                return ReadErrors(errors);
            }
        }
        catch (JsonException)
        {
            // Not JSON — nothing to extract.
        }
        return null;
    }

    private static GraphQlError[] ReadErrors(JsonElement errors) =>
        JsonSerializer.Deserialize(errors.GetRawText(), SdkJson.Default.GraphQlErrorArray)
        ?? Array.Empty<GraphQlError>();
}
