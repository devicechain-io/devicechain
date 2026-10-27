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

// The second factor from the SDK's side. The server answers an enrolled sign-in with a GraphQL
// error (HTTP 200, data null) whose extensions carry the challenge; these tests pin that the SDK
// turns it into a typed exception and never into a null or empty token.
public class AuthSessionMfaTests
{
    private static string Iso(DateTimeOffset when) => when.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ");

    private const string Challenge =
        "{\"errors\":[{\"message\":\"a second factor is required\",\"extensions\":{\"code\":\"MFA_CHALLENGE\"," +
        "\"challenge\":\"chal-1\",\"expiresAt\":\"2030-01-02T03:04:05Z\",\"methods\":[\"TOTP\",\"RECOVERY_CODE\"]}}],\"data\":null}";

    private static (AuthSession session, StubHandler handler) NewSession(Func<string, (HttpStatusCode, string)> responder)
    {
        var handler = new StubHandler((_, body) => responder(body));
        return (new AuthSession(new GraphQlClient(new HttpClient(handler), new Uri("https://test.local"))), handler);
    }

    private static (HttpStatusCode, string) EnrolledServer(string body, string expectedCode = "123456")
    {
        if (body.Contains("verifyMfa("))
        {
            if (body.Contains("\"challenge\":\"chal-1\"") && body.Contains("\"code\":\"" + expectedCode + "\""))
            {
                return (HttpStatusCode.OK,
                    "{\"data\":{\"verifyMfa\":{\"identityToken\":\"id-mfa\",\"expiresAt\":\"" + Iso(DateTimeOffset.UtcNow.AddHours(1)) +
                    "\",\"superuser\":true,\"memberships\":[{\"tenant\":\"acme\",\"roles\":[\"tenant-admin\"]}]}}}");
            }
            return (HttpStatusCode.OK,
                "{\"errors\":[{\"message\":\"the sign-in has expired\",\"extensions\":{\"code\":\"MFA_CHALLENGE_EXPIRED\"}}],\"data\":null}");
        }
        if (body.Contains("selectTenant("))
        {
            return (HttpStatusCode.OK,
                "{\"data\":{\"selectTenant\":{\"accessToken\":\"acc-1\",\"refreshToken\":\"ref-1\",\"expiresAt\":\"" +
                Iso(DateTimeOffset.UtcNow.AddHours(1)) + "\"}}}");
        }
        return (HttpStatusCode.OK, Challenge); // login
    }

    [Fact]
    public async Task Login_of_an_enrolled_identity_throws_a_typed_exception_and_issues_nothing()
    {
        (AuthSession session, _) = NewSession(b => EnrolledServer(b));

        MfaRequiredException ex = await Assert.ThrowsAsync<MfaRequiredException>(() => session.LoginAsync("a@b.c", "pw"));

        Assert.Equal("chal-1", ex.Challenge);
        Assert.Equal(new DateTimeOffset(2030, 1, 2, 3, 4, 5, TimeSpan.Zero), ex.ExpiresAt);
        Assert.Equal(new[] { "TOTP", "RECOVERY_CODE" }, ex.Methods);
        Assert.Equal(200, ex.Status);
        Assert.Equal("MFA_CHALLENGE", Assert.Single(ex.Errors).Code);
        // The password alone gave the session nothing: no identity, no memberships, no access token.
        Assert.Empty(session.Memberships);
        Assert.False(session.Superuser);
        Assert.Null(session.AccessToken);
        await Assert.ThrowsAsync<InvalidOperationException>(() => session.SelectTenantAsync("acme"));
    }

    // The v0.25 SDK knew nothing of this: it threw GraphQlRequestException for any errors payload
    // and never returned an empty token. The typed exception IS a GraphQlRequestException, so a
    // caller written against that SDK still catches the sign-in it could not complete.
    [Fact]
    public async Task The_typed_exception_is_still_a_GraphQlRequestException()
    {
        (AuthSession session, _) = NewSession(b => EnrolledServer(b));
        GraphQlRequestException ex = await Assert.ThrowsAsync<MfaRequiredException>(() => session.LoginAsync("a@b.c", "pw"));
        Assert.Equal("MFA_CHALLENGE", ex.Errors[0].Code);
    }

    [Fact]
    public async Task VerifyMfa_completes_the_sign_in_and_a_tenant_can_then_be_selected()
    {
        (AuthSession session, StubHandler handler) = NewSession(b => EnrolledServer(b));
        MfaRequiredException ex = await Assert.ThrowsAsync<MfaRequiredException>(() => session.LoginAsync("a@b.c", "pw"));

        IdentityAuth identity = await session.VerifyMfaAsync(ex.Challenge, "123456");

        Assert.Equal("id-mfa", identity.IdentityToken);
        Assert.True(session.Superuser);
        Assert.Equal("acme", Assert.Single(session.Memberships).Tenant);
        AuthToken token = await session.SelectTenantAsync("acme");
        Assert.Equal("acc-1", token.AccessToken);
        // verifyMfa ran anonymously against user-management, like login.
        StubHandler.Call verify = handler.Calls.Single(c => c.Body.Contains("verifyMfa("));
        Assert.Null(verify.Authorization);
        Assert.Equal("/api/user-management/graphql", verify.Path);
    }

    [Fact]
    public async Task VerifyMfa_with_a_challenge_the_server_does_not_know_throws_the_expired_code()
    {
        (AuthSession session, _) = NewSession(b => EnrolledServer(b));
        GraphQlRequestException ex = await Assert.ThrowsAsync<GraphQlRequestException>(() => session.VerifyMfaAsync("old", "123456"));
        Assert.IsNotType<MfaRequiredException>(ex);
        Assert.Equal("MFA_CHALLENGE_EXPIRED", Assert.Single(ex.Errors).Code);
        Assert.Null(session.AccessToken);
    }

    // An answer carrying the code but nothing to verify with is not a challenge a caller could act
    // on, and two errors are not the server's single answer: both stay the plain exception.
    [Theory]
    [InlineData("{\"errors\":[{\"message\":\"x\",\"extensions\":{\"code\":\"MFA_CHALLENGE\"}}],\"data\":null}")]
    [InlineData("{\"errors\":[{\"message\":\"x\",\"extensions\":{\"code\":\"MFA_CHALLENGE\",\"challenge\":\"\"}}],\"data\":null}")]
    [InlineData("{\"errors\":[{\"message\":\"x\",\"extensions\":{\"code\":\"MFA_CHALLENGE\",\"challenge\":\"c\"}}," +
                "{\"message\":\"y\"}],\"data\":null}")]
    [InlineData("{\"errors\":[{\"message\":\"x\",\"extensions\":{\"code\":\"OTHER\",\"challenge\":\"c\"}}],\"data\":null}")]
    public async Task Anything_but_a_whole_challenge_is_a_plain_GraphQlRequestException(string response)
    {
        (AuthSession session, _) = NewSession(_ => (HttpStatusCode.OK, response));
        GraphQlRequestException ex = await Assert.ThrowsAsync<GraphQlRequestException>(() => session.LoginAsync("a@b.c", "pw"));
        Assert.IsNotType<MfaRequiredException>(ex);
    }

    // Without a second factor nothing changes: the ordinary login returns its token.
    [Fact]
    public async Task A_password_only_login_is_unchanged()
    {
        (AuthSession session, _) = NewSession(_ => (HttpStatusCode.OK,
            "{\"data\":{\"login\":{\"identityToken\":\"id-tok\",\"expiresAt\":\"" + Iso(DateTimeOffset.UtcNow.AddHours(1)) +
            "\",\"superuser\":false,\"memberships\":[]}}}"));
        Assert.Equal("id-tok", (await session.LoginAsync("a@b.c", "pw")).IdentityToken);
    }
}
