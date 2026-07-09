// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Concurrent;
using System.Net.Http;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Auth;
using DeviceChain.Sdk.Ingest;
using DeviceChain.Sdk.Subscriptions;
using DeviceChain.Sdk.Transport;

namespace DeviceChain.Sdk;

/// <summary>
/// The top-level DeviceChain client — the one object an integrator (a .NET host, a Unity twin
/// via the slice-4 plugin) constructs. It wires the auth state machine, an authenticated
/// GraphQL client, a per-area subscription client, and the device-plane telemetry publisher
/// against one platform origin, all speaking the documented wire seams (no admin surface).
/// </summary>
public sealed class DeviceChainClient : IAsyncDisposable
{
    private readonly IHttpTransport _httpTransport;
    private readonly IWebSocketFactory _webSocketFactory;
    private readonly IDisposable? _ownedHttp;
    private readonly ConcurrentDictionary<Area, GraphQlWsClient> _subscriptions = new();
    private volatile bool _disposed;

    /// <summary>The platform origin (e.g. https://demo.devicechain.io).</summary>
    public Uri Origin { get; }

    /// <summary>The two-tier auth state machine (login → selectTenant → refresh).</summary>
    public AuthSession Auth { get; private set; } = null!;

    /// <summary>An authenticated GraphQL client (attaches the session's access token per request).</summary>
    public GraphQlClient Gql { get; private set; } = null!;

    /// <param name="origin">The platform origin the ingress serves (/api/... and the device-plane ingress).</param>
    /// <param name="httpClient">An HttpClient to reuse; when null the client owns a new one (disposed with it).</param>
    public DeviceChainClient(Uri origin, HttpClient? httpClient = null)
    {
        Origin = origin ?? throw new ArgumentNullException(nameof(origin));
        // The SDK builds absolute URIs from Origin, so a caller-supplied HttpClient is used as-is
        // (its BaseAddress is neither read nor mutated). Own one only when none is supplied.
        HttpClient http;
        if (httpClient is null)
        {
            http = new HttpClient();
            _ownedHttp = http;
        }
        else
        {
            http = httpClient;
        }
        _httpTransport = new HttpClientTransport(http);
        _webSocketFactory = new ClientWebSocketFactory();
        InitClients();
    }

    /// <summary>
    /// Transport-injecting constructor — the Unity WebGL path. A host supplies transports backed by
    /// <c>UnityWebRequest</c> and a browser <c>WebSocket</c> (via <c>.jslib</c>), since
    /// <see cref="HttpClient"/>/<c>ClientWebSocket</c> do not work under WebGL/IL2CPP. The SDK owns
    /// neither transport (the host's to dispose).
    /// </summary>
    /// <param name="origin">The platform origin the ingress serves (/api/... and the device-plane ingress).</param>
    /// <param name="httpTransport">The HTTP transport to use for GraphQL + device-plane emit.</param>
    /// <param name="webSocketFactory">The factory for live-subscription sockets.</param>
    public DeviceChainClient(Uri origin, IHttpTransport httpTransport, IWebSocketFactory webSocketFactory)
    {
        Origin = origin ?? throw new ArgumentNullException(nameof(origin));
        _httpTransport = httpTransport ?? throw new ArgumentNullException(nameof(httpTransport));
        _webSocketFactory = webSocketFactory ?? throw new ArgumentNullException(nameof(webSocketFactory));
        InitClients();
    }

    private void InitClients()
    {
        // The auth mutations run anonymously, so this inner client needs no token provider; the
        // outer Gql attaches the session's token (no construction cycle).
        var authGql = new GraphQlClient(_httpTransport, Origin);
        Auth = new AuthSession(authGql);
        Gql = new GraphQlClient(_httpTransport, Origin, Auth.GetAccessTokenAsync);
    }

    /// <summary>Authenticates an identity (step 1). Convenience over <see cref="AuthSession.LoginAsync"/>.</summary>
    public Task<IdentityAuth> LoginAsync(string email, string password, CancellationToken cancellationToken = default) =>
        Auth.LoginAsync(email, password, cancellationToken);

    /// <summary>Selects a tenant (step 2), yielding the access/refresh pair.</summary>
    public Task<AuthToken> SelectTenantAsync(string tenant, CancellationToken cancellationToken = default) =>
        Auth.SelectTenantAsync(tenant, cancellationToken);

    /// <summary>
    /// The live-subscription client for an area (lazily created, one socket reused across its
    /// subscriptions), authenticated with the session token via connection_init.
    /// </summary>
    public GraphQlWsClient Subscriptions(Area area)
    {
        ThrowIfDisposed();
        return _subscriptions.GetOrAdd(area, a => new GraphQlWsClient(_webSocketFactory, WebSocketUri(a), Auth.GetAccessTokenAsync));
    }

    /// <summary>
    /// A device-plane telemetry publisher for one instance+tenant. The device-plane ingress is a
    /// SEPARATE listener from the GraphQL origin (the cluster ingress does not route the events
    /// path), so its origin is required explicitly.
    /// </summary>
    public DeviceEventPublisher DevicePublisher(Uri ingressOrigin, string instanceId, string tenant)
    {
        ThrowIfDisposed();
        return new DeviceEventPublisher(_httpTransport, ingressOrigin, instanceId, tenant);
    }

    private void ThrowIfDisposed()
    {
        if (_disposed)
        {
            throw new ObjectDisposedException(nameof(DeviceChainClient));
        }
    }

    /// <summary>The absolute ws(s):// URL for an area (mirrors the http origin + area path).</summary>
    public Uri WebSocketUri(Area area)
    {
        var builder = new UriBuilder(Origin)
        {
            Scheme = string.Equals(Origin.Scheme, Uri.UriSchemeHttps, StringComparison.OrdinalIgnoreCase) ? "wss" : "ws",
            Path = Areas.Path(area),
        };
        return builder.Uri;
    }

    /// <inheritdoc />
    public async ValueTask DisposeAsync()
    {
        if (_disposed)
        {
            return;
        }
        _disposed = true; // stops Subscriptions/DevicePublisher from racing in a new client past teardown
        foreach (GraphQlWsClient client in _subscriptions.Values)
        {
            await client.DisposeAsync().ConfigureAwait(false);
        }
        _subscriptions.Clear();
        _ownedHttp?.Dispose();
    }
}
