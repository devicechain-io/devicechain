// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Threading.Tasks;
using DeviceChain.Sdk;
using DeviceChain.Sdk.Auth;
using Xunit;

namespace DeviceChain.Sdk.Tests;

public class AuthSessionTests
{
    private static string Iso(DateTimeOffset when) => when.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ");

    // Routes login / selectTenant / refresh by the operation name in the query.
    private static (HttpStatusCode, string) AuthResponder(HttpRequestMessage _, string body, string selectExpiry, string refreshExpiry)
    {
        if (body.Contains("login("))
        {
            return (HttpStatusCode.OK,
                "{\"data\":{\"login\":{\"identityToken\":\"id-tok\",\"expiresAt\":\"" + Iso(DateTimeOffset.UtcNow.AddHours(1)) +
                "\",\"superuser\":true,\"memberships\":[{\"tenant\":\"acme\",\"roles\":[\"tenant-admin\"]}]}}}");
        }
        if (body.Contains("selectTenant("))
        {
            return (HttpStatusCode.OK,
                "{\"data\":{\"selectTenant\":{\"accessToken\":\"acc-1\",\"refreshToken\":\"ref-1\",\"expiresAt\":\"" + selectExpiry + "\"}}}");
        }
        if (body.Contains("refresh("))
        {
            return (HttpStatusCode.OK,
                "{\"data\":{\"refresh\":{\"accessToken\":\"acc-2\",\"refreshToken\":\"ref-2\",\"expiresAt\":\"" + refreshExpiry + "\"}}}");
        }
        return (HttpStatusCode.BadRequest, "{\"errors\":[{\"message\":\"unexpected op\"}]}");
    }

    private static (AuthSession session, StubHandler handler) NewSession(string? selectExpiry = null, string? refreshExpiry = null)
    {
        string sel = selectExpiry ?? Iso(DateTimeOffset.UtcNow.AddHours(1));
        string refr = refreshExpiry ?? Iso(DateTimeOffset.UtcNow.AddHours(1));
        var handler = new StubHandler((req, body) => AuthResponder(req, body, sel, refr));
        var http = new HttpClient(handler);
        return (new AuthSession(new GraphQlClient(http, new Uri("https://test.local"))), handler);
    }

    [Fact]
    public async Task Login_then_selectTenant_captures_identity_and_access_token()
    {
        (AuthSession session, StubHandler handler) = NewSession();

        IdentityAuth identity = await session.LoginAsync("a@b.c", "pw");
        Assert.Equal("id-tok", identity.IdentityToken);
        Assert.True(session.Superuser);
        Assert.Equal("acme", Assert.Single(session.Memberships).Tenant);

        AuthToken token = await session.SelectTenantAsync("acme");
        Assert.Equal("acc-1", token.AccessToken);
        Assert.Equal("acc-1", session.AccessToken);

        // Both auth calls ran anonymously (no Authorization header) against user-management.
        Assert.All(handler.Calls, c => Assert.Null(c.Authorization));
        Assert.All(handler.Calls, c => Assert.Equal("/api/user-management/graphql", c.Path));
    }

    [Fact]
    public async Task SelectTenant_before_login_throws()
    {
        (AuthSession session, _) = NewSession();
        await Assert.ThrowsAsync<InvalidOperationException>(() => session.SelectTenantAsync("acme"));
    }

    [Fact]
    public async Task GetAccessToken_proactively_refreshes_when_near_expiry()
    {
        // selectTenant's token is already at expiry (inside the 60s skew) → the getter refreshes.
        (AuthSession session, StubHandler handler) = NewSession(
            selectExpiry: Iso(DateTimeOffset.UtcNow),
            refreshExpiry: Iso(DateTimeOffset.UtcNow.AddHours(1)));

        await session.LoginAsync("a@b.c", "pw");
        await session.SelectTenantAsync("acme");

        string? token = await session.GetAccessTokenAsync();
        Assert.Equal("acc-2", token); // rotated via refresh
        Assert.Contains(handler.Calls, c => c.Body.Contains("refresh("));

        // A second call does not refresh again (the new token is far from expiry).
        int refreshes = handler.Calls.Count(c => c.Body.Contains("refresh("));
        await session.GetAccessTokenAsync();
        Assert.Equal(refreshes, handler.Calls.Count(c => c.Body.Contains("refresh(")));
    }

    [Fact]
    public async Task GetAccessToken_returns_null_before_a_tenant_is_selected()
    {
        (AuthSession session, _) = NewSession();
        await session.LoginAsync("a@b.c", "pw");
        Assert.Null(await session.GetAccessTokenAsync());
    }
}
