// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Json;

namespace DeviceChain.Sdk.Auth;

/// <summary>
/// The two-tier auth state machine (ADR-033) the sim contract calls the "RealClient" layer:
/// <c>login → selectTenant → refresh</c>. It holds the instance-scoped identity token and the
/// tenant-scoped access/refresh pair, and exposes <see cref="GetAccessTokenAsync"/> as the
/// <see cref="TokenProvider"/> a <see cref="GraphQlClient"/> plugs in — proactively rotating
/// the pair when the access token nears expiry, so long-lived subscriptions survive it. All
/// three mutations run anonymously (the identity/refresh token is the argument, not a Bearer).
///
/// Concurrency: every write of the access/refresh/expiry triple — selectTenant, refresh, and the
/// refresh-rejected re-exchange — runs under <see cref="_refreshLock"/>, so a background refresh
/// can never interleave with a tenant switch into a torn or cross-tenant pair. The rotation RPC
/// is deliberately shielded from the caller's <see cref="CancellationToken"/>: a refresh token is
/// single-use server-side, so cancelling a rotation already in flight would strand the session on
/// a token the server has consumed.
/// </summary>
public sealed class AuthSession
{
    // Refresh when the access token is within this window of expiry (a long-lived twin/sub
    // must roll over before the socket's token is rejected).
    private static readonly TimeSpan RefreshSkew = TimeSpan.FromSeconds(60);

    // A hard ceiling on a token rotation / recovery RPC. Because these run shielded from the
    // caller's token (see RefreshLockedAsync), the SDK must own their deadline itself — a request
    // that black-holes would otherwise wedge the session under _refreshLock forever on any transport
    // that imposes no timeout of its own (Unity's UnityWebRequest defaults to infinite).
    private static readonly TimeSpan RotationTimeout = TimeSpan.FromSeconds(30);

    private const string LoginMutation =
        "mutation Login($email:String!,$password:String!){login(email:$email,password:$password){" +
        "identityToken expiresAt superuser memberships{tenant roles}}}";
    private const string SelectTenantMutation =
        "mutation SelectTenant($identityToken:String!,$tenant:String!){" +
        "selectTenant(identityToken:$identityToken,tenant:$tenant){accessToken refreshToken expiresAt}}";
    private const string RefreshMutation =
        "mutation Refresh($refreshToken:String!){refresh(refreshToken:$refreshToken){" +
        "accessToken refreshToken expiresAt}}";

    private readonly GraphQlClient _gql;
    private readonly SemaphoreSlim _refreshLock = new(1, 1);
    private readonly RequestOptions _anonymous = new() { Anonymous = true };

    private string? _identityToken;
    // The tenant last selected; retained so a rejected refresh can re-exchange the (reusable)
    // identity token for a fresh pair without the caller's password.
    private string? _tenant;
    private string? _accessToken;
    private string? _refreshToken;
    // UTC ticks of access-token expiry; 0 = unknown (disables proactive refresh). Accessed with
    // Volatile so the lock-free NeedsRefresh() read can't tear this multi-word value.
    private long _accessExpiresAtTicks;

    /// <param name="gql">A client for the (anonymous) auth mutations — its token provider is unused here.</param>
    public AuthSession(GraphQlClient gql)
    {
        _gql = gql ?? throw new ArgumentNullException(nameof(gql));
    }

    /// <summary>The tenants the logged-in identity may act in (populated by <see cref="LoginAsync"/>).</summary>
    public IReadOnlyList<Membership> Memberships { get; private set; } = Array.Empty<Membership>();

    /// <summary>Whether the logged-in identity holds the superuser role.</summary>
    public bool Superuser { get; private set; }

    /// <summary>The current tenant access token, or null before selectTenant.</summary>
    public string? AccessToken => _accessToken;

    /// <summary>
    /// Authenticates an email/password identity, capturing the identity token + memberships.
    /// Step 1 of the flow; pick a tenant next with <see cref="SelectTenantAsync"/>.
    /// </summary>
    public async Task<IdentityAuth> LoginAsync(string email, string password, CancellationToken cancellationToken = default)
    {
        LoginData data = await _gql.SendAsync(
            Area.UserManagement, LoginMutation,
            new LoginVariables { Email = email, Password = password },
            SdkJson.Default.LoginVariables, SdkJson.Default.LoginData,
            _anonymous, cancellationToken).ConfigureAwait(false);

        _identityToken = data.Login.IdentityToken;
        Memberships = data.Login.Memberships;
        Superuser = data.Login.Superuser;
        return data.Login;
    }

    /// <summary>
    /// Exchanges the identity token for a tenant-scoped access/refresh pair. Step 2; requires a
    /// prior <see cref="LoginAsync"/>. Serialized through <see cref="_refreshLock"/> so a tenant
    /// switch can't race a background refresh into a torn or cross-tenant pair.
    /// </summary>
    public async Task<AuthToken> SelectTenantAsync(string tenant, CancellationToken cancellationToken = default)
    {
        if (string.IsNullOrEmpty(_identityToken))
        {
            throw new InvalidOperationException("Call LoginAsync before SelectTenantAsync.");
        }
        await _refreshLock.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            return await SelectTenantLockedAsync(tenant, cancellationToken).ConfigureAwait(false);
        }
        finally
        {
            _refreshLock.Release();
        }
    }

    /// <summary>
    /// Rotates the access/refresh pair using the current refresh token. Serialized through the
    /// same lock as the proactive refresh, so a direct call can't race the token rotation (which
    /// would consume the single-use refresh token twice and brick the session).
    /// </summary>
    public async Task<AuthToken> RefreshAsync(CancellationToken cancellationToken = default)
    {
        await _refreshLock.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            return await RefreshLockedAsync().ConfigureAwait(false);
        }
        finally
        {
            _refreshLock.Release();
        }
    }

    // Exchanges the identity token for a fresh pair and applies it. Caller MUST hold _refreshLock.
    // Used both by the public SelectTenantAsync (a deliberate tenant switch) and by the
    // refresh-rejected recovery path, so all three writers of the token triple share one code path.
    private async Task<AuthToken> SelectTenantLockedAsync(string tenant, CancellationToken cancellationToken)
    {
        SelectTenantData data = await _gql.SendAsync(
            Area.UserManagement, SelectTenantMutation,
            new SelectTenantVariables { IdentityToken = _identityToken!, Tenant = tenant },
            SdkJson.Default.SelectTenantVariables, SdkJson.Default.SelectTenantData,
            _anonymous, cancellationToken).ConfigureAwait(false);

        _tenant = tenant;
        Apply(data.SelectTenant);
        return data.SelectTenant;
    }

    // The actual rotation; the caller MUST hold _refreshLock.
    //
    // The rotation RPC runs on an SDK-owned deadline (rotationCts), NOT the caller's token, on
    // purpose. A refresh token is single-use server-side (the refresh resolver claims its jti with a
    // CAS delete before minting the new pair), so the instant the request is in flight the old token
    // may already be spent. Tearing it down on a caller cancel/timeout would leave us holding a dead
    // token with no proof the server rotated it — every later refresh would 401 and the whole
    // session would be bricked. So once we commit the token we see the rotation through; only the
    // *wait* for the lock (in the callers above) honors the caller's cancellation. RotationTimeout
    // bounds a black-holed request so a hung network can't wedge the session under the lock forever.
    private async Task<AuthToken> RefreshLockedAsync()
    {
        if (string.IsNullOrEmpty(_refreshToken))
        {
            throw new InvalidOperationException("No refresh token — call SelectTenantAsync first.");
        }
        using var rotationCts = new CancellationTokenSource(RotationTimeout);
        try
        {
            RefreshData data = await _gql.SendAsync(
                Area.UserManagement, RefreshMutation,
                new RefreshVariables { RefreshToken = _refreshToken! },
                SdkJson.Default.RefreshVariables, SdkJson.Default.RefreshData,
                _anonymous, rotationCts.Token).ConfigureAwait(false);

            Apply(data.Refresh);
            return data.Refresh;
        }
        catch (GraphQlRequestException ex) when (IsServerRejection(ex) && CanReExchange())
        {
            // The server definitively rejected the refresh token: the refresh resolver executes on
            // the anonymous endpoint and returns a GraphQL errors payload (HTTP 2xx) only for an
            // invalid/rotated/revoked/expired token. A transport failure (Status 0) or a 5xx — even
            // one whose body happens to carry an errors array — is transient, fails IsServerRejection,
            // and rightly propagates for the caller to retry rather than abandoning a live token. The
            // single-use token is now dead; recover without a password by re-exchanging the
            // still-valid identity token for a fresh pair (also on the SDK deadline, not the caller's).
            // If the identity token is also dead the re-exchange rethrows (a genuine re-login is
            // required); we leave _identityToken/_tenant intact so a later call can retry the recovery
            // once the outage clears.
            using var recoveryCts = new CancellationTokenSource(RotationTimeout);
            return await SelectTenantLockedAsync(_tenant!, recoveryCts.Token).ConfigureAwait(false);
        }
    }

    // A definitive server rejection of a refresh token: the resolver executed (HTTP 2xx) and returned
    // a GraphQL errors payload. Distinct from a transient failure (transport error → Status 0; a 5xx
    // whose body carries an errors array → Status 5xx), which must NOT trigger recovery.
    private static bool IsServerRejection(GraphQlRequestException ex) =>
        ex.Status is >= 200 and < 300 && ex.Errors.Count > 0;

    // Whether a rejected refresh can be recovered by re-exchanging the identity token.
    private bool CanReExchange() => !string.IsNullOrEmpty(_identityToken) && !string.IsNullOrEmpty(_tenant);

    /// <summary>
    /// The <see cref="TokenProvider"/> seam: returns the current access token, proactively
    /// refreshing it first when within the skew window of expiry. Returns null before a tenant
    /// is selected. Concurrency-safe — a single refresh runs under a lock while callers await.
    /// </summary>
    public async ValueTask<string?> GetAccessTokenAsync(CancellationToken cancellationToken = default)
    {
        if (_accessToken is null)
        {
            return null;
        }
        if (NeedsRefresh() && _refreshToken is not null)
        {
            await _refreshLock.WaitAsync(cancellationToken).ConfigureAwait(false);
            try
            {
                // Re-check under the lock — a concurrent caller may have just refreshed.
                if (NeedsRefresh() && _refreshToken is not null)
                {
                    await RefreshLockedAsync().ConfigureAwait(false);
                }
            }
            finally
            {
                _refreshLock.Release();
            }
        }
        return _accessToken;
    }

    private bool NeedsRefresh()
    {
        long ticks = Volatile.Read(ref _accessExpiresAtTicks);
        return ticks != 0 && DateTimeOffset.UtcNow.Ticks >= ticks - RefreshSkew.Ticks;
    }

    private void Apply(AuthToken token)
    {
        _accessToken = token.AccessToken;
        _refreshToken = token.RefreshToken;
        DateTimeOffset? expiry = ParseExpiry(token.ExpiresAt);
        Volatile.Write(ref _accessExpiresAtTicks, expiry?.UtcTicks ?? 0);
    }

    // Parses the RFC3339 expiresAt; an unparseable value disables proactive refresh (the token
    // is used as-is until a request rejects it) rather than refreshing on every call.
    private static DateTimeOffset? ParseExpiry(string expiresAt)
    {
        if (DateTimeOffset.TryParse(expiresAt, CultureInfo.InvariantCulture,
                DateTimeStyles.AssumeUniversal | DateTimeStyles.AdjustToUniversal, out DateTimeOffset parsed))
        {
            return parsed;
        }
        return null;
    }
}
