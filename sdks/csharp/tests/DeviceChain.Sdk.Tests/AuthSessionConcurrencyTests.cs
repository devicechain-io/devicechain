// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk;
using DeviceChain.Sdk.Auth;
using Xunit;

namespace DeviceChain.Sdk.Tests;

/// <summary>
/// Covers the auth state machine's behavior under cancellation and concurrency (the SDK
/// auth/session + threading hardening): a caller cancel must not brick a session by tearing down
/// a single-use-token rotation, a server-rejected refresh must recover via the identity token,
/// and a tenant switch must serialize with a background refresh.
/// </summary>
public class AuthSessionConcurrencyTests
{
    private static string Iso(DateTimeOffset when) => when.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ");

    private static string LoginOk() =>
        "{\"data\":{\"login\":{\"identityToken\":\"id-tok\",\"expiresAt\":\"" + Iso(DateTimeOffset.UtcNow.AddHours(1)) +
        "\",\"superuser\":true,\"memberships\":[{\"tenant\":\"acme\",\"roles\":[\"tenant-admin\"]}]}}}";

    private static string SelectOk(string access, string refresh, DateTimeOffset expiry) =>
        "{\"data\":{\"selectTenant\":{\"accessToken\":\"" + access + "\",\"refreshToken\":\"" + refresh +
        "\",\"expiresAt\":\"" + Iso(expiry) + "\"}}}";

    private static string RefreshOk(string access, string refresh, DateTimeOffset expiry) =>
        "{\"data\":{\"refresh\":{\"accessToken\":\"" + access + "\",\"refreshToken\":\"" + refresh +
        "\",\"expiresAt\":\"" + Iso(expiry) + "\"}}}";

    private static AuthSession NewSession(GatedHandler handler) =>
        new(new GraphQlClient(new HttpClient(handler), new Uri("https://test.local")));

    // ── #2b: a server-rejected refresh recovers via the identity token, never bricks ──
    [Fact]
    public async Task Refresh_rejected_by_server_recovers_by_re_exchanging_the_identity_token()
    {
        int selects = 0;
        var handler = new GatedHandler((req, body, ct) =>
        {
            if (body.Contains("login(")) return Task.FromResult((HttpStatusCode.OK, LoginOk()));
            if (body.Contains("selectTenant("))
            {
                int n = Interlocked.Increment(ref selects);
                // First selectTenant is at-expiry so the getter refreshes; the recovery
                // re-exchange (second) returns a fresh, far-from-expiry pair.
                return n == 1
                    ? Task.FromResult((HttpStatusCode.OK, SelectOk("acc-1", "ref-1", DateTimeOffset.UtcNow)))
                    : Task.FromResult((HttpStatusCode.OK, SelectOk("acc-3", "ref-3", DateTimeOffset.UtcNow.AddHours(1))));
            }
            // refresh: the server rejects the (single-use) token — a GraphQL errors payload.
            return Task.FromResult((HttpStatusCode.OK, "{\"errors\":[{\"message\":\"invalid or expired token\"}]}"));
        });
        AuthSession session = NewSession(handler);

        await session.LoginAsync("a@b.c", "pw");
        await session.SelectTenantAsync("acme");

        // The getter tries to refresh, the server rejects, and recovery re-exchanges the identity token.
        string? token = await session.GetAccessTokenAsync();
        Assert.Equal("acc-3", token);
        Assert.Equal("acc-3", session.AccessToken);
        Assert.Contains(handler.Bodies, b => b.Contains("refresh("));
        Assert.Equal(2, handler.Bodies.Count(b => b.Contains("selectTenant(")));

        // The session is healthy: a follow-up needs no more auth calls (the recovered token is fresh).
        int before = handler.Bodies.Count;
        Assert.Equal("acc-3", await session.GetAccessTokenAsync());
        Assert.Equal(before, handler.Bodies.Count);
    }

    // ── #2a: a caller cancel mid-rotation must NOT tear down the single-use-token refresh ──
    [Fact]
    public async Task Caller_cancellation_during_rotation_does_not_abort_the_refresh()
    {
        var refreshStarted = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseRefresh = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        var handler = new GatedHandler(async (req, body, ct) =>
        {
            if (body.Contains("login(")) return (HttpStatusCode.OK, LoginOk());
            if (body.Contains("selectTenant("))
                return (HttpStatusCode.OK, SelectOk("acc-1", "ref-1", DateTimeOffset.UtcNow));
            // refresh: announce we're in flight, then block until released — honoring the token we
            // were actually handed. If the rotation were (wrongly) cancellable by the caller, this
            // wait would observe the cancel and throw; shielding hands us CancellationToken.None.
            refreshStarted.TrySetResult(true);
            await releaseRefresh.Task.WaitAsync(ct).ConfigureAwait(false);
            return (HttpStatusCode.OK, RefreshOk("acc-2", "ref-2", DateTimeOffset.UtcNow.AddHours(1)));
        });
        AuthSession session = NewSession(handler);

        await session.LoginAsync("a@b.c", "pw");
        await session.SelectTenantAsync("acme");

        using var cts = new CancellationTokenSource();
        Task<string?> getToken = session.GetAccessTokenAsync(cts.Token).AsTask();

        // Cancel the caller while the rotation RPC is in flight, then let the server reply.
        await refreshStarted.Task.WaitAsync(TimeSpan.FromSeconds(5));
        cts.Cancel();
        releaseRefresh.TrySetResult(true);

        // The rotation completed despite the cancel: the new pair is applied, session intact.
        string? token = await getToken.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.Equal("acc-2", token);
        Assert.Equal("acc-2", session.AccessToken);
    }

    // ── #3: a tenant switch serializes with a background refresh under _refreshLock ──
    [Fact]
    public async Task SelectTenant_waits_for_an_in_flight_refresh_to_release_the_lock()
    {
        var refreshStarted = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        var releaseRefresh = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        var secondSelectStarted = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
        int selects = 0;
        var handler = new GatedHandler(async (req, body, ct) =>
        {
            if (body.Contains("login(")) return (HttpStatusCode.OK, LoginOk());
            if (body.Contains("selectTenant("))
            {
                int n = Interlocked.Increment(ref selects);
                if (n == 1) return (HttpStatusCode.OK, SelectOk("acc-1", "ref-1", DateTimeOffset.UtcNow));
                // The concurrent tenant switch: announce its RPC actually started. If SelectTenant
                // took the lock, this can't fire until the gated refresh below releases it.
                secondSelectStarted.TrySetResult(true);
                return (HttpStatusCode.OK, SelectOk("acc-9", "ref-9", DateTimeOffset.UtcNow.AddHours(1)));
            }
            refreshStarted.TrySetResult(true);
            await releaseRefresh.Task.WaitAsync(ct).ConfigureAwait(false);
            return (HttpStatusCode.OK, RefreshOk("acc-2", "ref-2", DateTimeOffset.UtcNow.AddHours(1)));
        });
        AuthSession session = NewSession(handler);

        await session.LoginAsync("a@b.c", "pw");
        await session.SelectTenantAsync("acme"); // acc-1, at-expiry → next getter refreshes

        // Kick off a proactive refresh; it acquires the lock and blocks in the handler.
        Task<string?> getToken = session.GetAccessTokenAsync().AsTask();
        await refreshStarted.Task.WaitAsync(TimeSpan.FromSeconds(5));

        // Now a concurrent tenant switch. It must NOT start its RPC while the refresh holds the lock.
        Task<AuthToken> switchTenant = session.SelectTenantAsync("acme");
        await Task.WhenAny(secondSelectStarted.Task, Task.Delay(TimeSpan.FromMilliseconds(300)));
        Assert.False(secondSelectStarted.Task.IsCompleted,
            "SelectTenant issued its RPC while a refresh held _refreshLock — the write is not serialized.");

        // Release the refresh; the tenant switch then proceeds and both complete cleanly.
        releaseRefresh.TrySetResult(true);
        await getToken.WaitAsync(TimeSpan.FromSeconds(5));
        AuthToken switched = await switchTenant.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.Equal("acc-9", switched.AccessToken);
        Assert.Equal("acc-9", session.AccessToken);
    }

    // ── #2b discriminator: only a definitive server verdict (2xx + errors) recovers ──

    [Fact]
    public async Task Refresh_transport_failure_propagates_without_re_exchanging()
    {
        int selects = 0;
        var handler = new GatedHandler((req, body, ct) =>
        {
            if (body.Contains("login(")) return Task.FromResult((HttpStatusCode.OK, LoginOk()));
            if (body.Contains("selectTenant("))
            {
                Interlocked.Increment(ref selects);
                return Task.FromResult((HttpStatusCode.OK, SelectOk("acc-1", "ref-1", DateTimeOffset.UtcNow)));
            }
            // refresh: a transport failure (Status 0, no errors) — NOT a server rejection. The token
            // may well still be valid, so recovery must NOT fire; the caller retries later.
            throw new HttpRequestException("connection reset");
        });
        AuthSession session = NewSession(handler);
        await session.LoginAsync("a@b.c", "pw");
        await session.SelectTenantAsync("acme");

        GraphQlRequestException ex = await Assert.ThrowsAsync<GraphQlRequestException>(
            () => session.GetAccessTokenAsync().AsTask());
        Assert.Equal(0, ex.Status);   // transport-level, not a GraphQL verdict
        Assert.Equal(1, selects);     // did NOT re-exchange the identity token
    }

    [Fact]
    public async Task Refresh_5xx_with_errors_body_propagates_without_re_exchanging()
    {
        int selects = 0;
        var handler = new GatedHandler((req, body, ct) =>
        {
            if (body.Contains("login(")) return Task.FromResult((HttpStatusCode.OK, LoginOk()));
            if (body.Contains("selectTenant("))
            {
                Interlocked.Increment(ref selects);
                return Task.FromResult((HttpStatusCode.OK, SelectOk("acc-1", "ref-1", DateTimeOffset.UtcNow)));
            }
            // refresh: a 5xx whose body happens to carry an errors array — a transient upstream
            // failure, not the server's HTTP-200 verdict that the token is dead. Must NOT recover.
            return Task.FromResult((HttpStatusCode.ServiceUnavailable, "{\"errors\":[{\"message\":\"upstream down\"}]}"));
        });
        AuthSession session = NewSession(handler);
        await session.LoginAsync("a@b.c", "pw");
        await session.SelectTenantAsync("acme");

        GraphQlRequestException ex = await Assert.ThrowsAsync<GraphQlRequestException>(
            () => session.GetAccessTokenAsync().AsTask());
        Assert.Equal(503, ex.Status);
        Assert.Equal(1, selects);     // 5xx is transient → did NOT re-exchange
    }

    /// <summary>
    /// An <see cref="HttpMessageHandler"/> whose reply is produced by an async responder that
    /// receives the cancellation token it was actually handed (so a test can prove a request is or
    /// isn't cancellable). Records every request body, thread-safely.
    /// </summary>
    private sealed class GatedHandler : HttpMessageHandler
    {
        private readonly Func<HttpRequestMessage, string, CancellationToken, Task<(HttpStatusCode, string)>> _responder;
        private readonly object _gate = new();
        private readonly List<string> _bodies = new();

        public GatedHandler(Func<HttpRequestMessage, string, CancellationToken, Task<(HttpStatusCode, string)>> responder)
        {
            _responder = responder;
        }

        public IReadOnlyList<string> Bodies
        {
            get { lock (_gate) { return _bodies.ToArray(); } }
        }

        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
        {
            string body = request.Content is null ? "" : await request.Content.ReadAsStringAsync().ConfigureAwait(false);
            lock (_gate) { _bodies.Add(body); }
            (HttpStatusCode status, string json) = await _responder(request, body, cancellationToken).ConfigureAwait(false);
            return new HttpResponseMessage(status)
            {
                Content = new StringContent(json, System.Text.Encoding.UTF8, "application/json"),
            };
        }
    }
}
