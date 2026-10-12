// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"net/url"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// Signing out at the provider (RP-initiated logout), over the real sign-in harness: a stub
// provider, the real callback, real session records.

const (
	consoleSignOutReturn   = "https://dc.example.com/login"
	dashboardSignOutReturn = "https://dc.example.com/dash/"
)

// withEndSession makes the stub provider publish an end_session_endpoint for acme-idp and
// has the administrator re-fetch and approve the snapshot that now names it, as they would.
func withEndSession(t *testing.T, e *ssoEnv, path string) {
	t.Helper()
	ctx := context.Background()
	e.op.endSession = path
	d, err := e.m.RefreshIdpDiscovery(ctx, "acme-idp")
	require.NoError(t, err)
	require.Nil(t, d.Connection.DiscoveryApprovedAt, "a snapshot naming a new endpoint must need approval again")
	_, err = e.m.ApproveIdpDiscovery(ctx, "acme-idp", d.Fingerprint)
	require.NoError(t, err)
}

func setProviderSignOut(t *testing.T, e *ssoEnv, conn string, on bool) {
	t.Helper()
	_, err := e.m.UpdateIdpConnection(context.Background(), conn, &IdpConnectionUpdateRequest{
		ProviderSignOut: dcgraphql.OptionalBool{Set: true, Value: &on},
	})
	require.NoError(t, err)
}

// requireEnded asserts the platform sign-in an identity token belongs to has ended.
func requireEnded(t *testing.T, e *ssoEnv, identityToken string) {
	t.Helper()
	_, err := e.m.SelectTenant(context.Background(), identityToken, "acme")
	require.ErrorIs(t, err, ErrInvalidToken, "the platform sign-in is still open after signing out")
}

func TestSignOutOffersTheProviderOnlyWhenTheConnectionIsSwitchedOn(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.linkedPat(t)
	withEndSession(t, e, "/logout")

	// Off by default: the platform sign-in ends, and there is nothing more to do.
	require.False(t, e.reloadConn(t, "acme-idp").ProviderSignOut, "a connection must start with provider sign-out off")
	ia, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	u, err := e.m.SignOut(ctx, ia.IdentityToken, SignOutReturnConsole)
	require.NoError(t, err)
	require.Empty(t, u, "provider sign-out offered by a connection that never switched it on")
	requireEnded(t, e, ia.IdentityToken)

	// Switched on: the platform sign-in ends FIRST, and the answer is the approved endpoint.
	setProviderSignOut(t, e, "acme-idp", true)
	ia, _ = e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	u, err = e.m.SignOut(ctx, ia.IdentityToken, SignOutReturnConsole)
	require.NoError(t, err)
	requireEnded(t, e, ia.IdentityToken)
	got, err := url.Parse(u)
	require.NoError(t, err)
	require.Equal(t, e.op.url()+"/logout", got.Scheme+"://"+got.Host+got.Path)
	require.Equal(t, url.Values{
		"client_id":                {ssoTestClientID},
		"post_logout_redirect_uri": {consoleSignOutReturn},
	}, got.Query(), "the request carries client_id and the fixed return address, and nothing else (no id_token_hint, no state)")

	// The dashboard returns to its own address.
	ia, _ = e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	u, err = e.m.SignOut(ctx, ia.IdentityToken, SignOutReturnDashboard)
	require.NoError(t, err)
	got, err = url.Parse(u)
	require.NoError(t, err)
	require.Equal(t, dashboardSignOutReturn, got.Query().Get("post_logout_redirect_uri"))

	// No app named: platform sign-out only.
	ia, _ = e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	u, err = e.m.SignOut(ctx, ia.IdentityToken, "")
	require.NoError(t, err)
	require.Empty(t, u)
	requireEnded(t, e, ia.IdentityToken)

	// Repeating a sign-out finds no sign-in to describe: still a success, with no address.
	u, err = e.m.SignOut(ctx, ia.IdentityToken, SignOutReturnConsole)
	require.NoError(t, err)
	require.Empty(t, u)
}

// A provider that publishes no sign-out endpoint: switching the setting on changes nothing,
// silently.
func TestSignOutWithoutAnEndSessionEndpointIsPlatformOnly(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.linkedPat(t)
	setProviderSignOut(t, e, "acme-idp", true)

	ia, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	u, err := e.m.SignOut(ctx, ia.IdentityToken, SignOutReturnConsole)
	require.NoError(t, err)
	require.Empty(t, u)
	requireEnded(t, e, ia.IdentityToken)
}

// A password sign-in has no provider, whatever the connection for its domain says.
func TestSignOutOfAPasswordSignInIsPlatformOnly(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	withEndSession(t, e, "/logout")
	setProviderSignOut(t, e, "acme-idp", true)
	e.createMember(t, pwMember, "pw-1")

	idTok, pair := e.login(t, pwMember, "pw-1")
	u, err := e.m.SignOut(ctx, pair.RefreshToken, SignOutReturnConsole)
	require.NoError(t, err)
	require.Empty(t, u)
	_, err = e.m.SelectTenant(ctx, idTok, "acme")
	require.ErrorIs(t, err, ErrInvalidToken)
}

// The connection changed after the sign-in: disabled, or its snapshot awaiting approval
// again. The platform sign-out still completes; the provider is not offered.
func TestSignOutThroughAConnectionNoLongerUsableIsPlatformOnly(t *testing.T) {
	for name, change := range map[string]func(t *testing.T, e *ssoEnv){
		"disabled": func(t *testing.T, e *ssoEnv) {
			off := false
			_, err := e.m.UpdateIdpConnection(context.Background(), "acme-idp", &IdpConnectionUpdateRequest{
				Enabled: dcgraphql.OptionalBool{Set: true, Value: &off},
			})
			require.NoError(t, err)
		},
		"approval withdrawn": func(t *testing.T, e *ssoEnv) {
			e.op.endSession = "/logout-moved"
			_, err := e.m.RefreshIdpDiscovery(context.Background(), "acme-idp")
			require.NoError(t, err)
		},
		"switched off": func(t *testing.T, e *ssoEnv) { setProviderSignOut(t, e, "acme-idp", false) },
		"deleted": func(t *testing.T, e *ssoEnv) {
			_, err := e.m.DeleteIdpConnection(context.Background(), "acme-idp")
			require.NoError(t, err)
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newSsoEnv(t)
			ctx := context.Background()
			e.linkedPat(t)
			withEndSession(t, e, "/logout")
			setProviderSignOut(t, e, "acme-idp", true)

			ia, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
			change(t, e)
			u, err := e.m.SignOut(ctx, ia.IdentityToken, SignOutReturnConsole)
			require.NoError(t, err)
			require.Empty(t, u)
			requireEnded(t, e, ia.IdentityToken)
		})
	}
}

// THE NEGATIVE CONTROL for the return address. The caller names an app and never an
// address, so the only addresses a sign-out can ever send the provider are the two fixed
// ones on the issuer's own origin: any other app value, including one shaped like a URL or
// a path, builds nothing; and an endpoint that tries to carry its own return address has it
// replaced, not kept beside ours.
func TestTheSignOutReturnAddressIsOnlyEverOneOfTheFixedTwo(t *testing.T) {
	m := &Manager{issuerUrl: ssoTestIssuerURL}

	// Control: the two apps do build their addresses, so the refusals below are refusals.
	require.Equal(t, consoleSignOutReturn, m.signOutReturnURL(SignOutReturnConsole))
	require.Equal(t, dashboardSignOutReturn, m.signOutReturnURL(SignOutReturnDashboard))
	require.Equal(t, []string{consoleSignOutReturn, dashboardSignOutReturn}, m.signOutReturnURLs())

	for _, app := range []SignOutReturn{
		"", "console", "Console", "CONSOLE ", "DASHBOARD/", "https://evil.example/",
		"//evil.example", "/login", "../", "javascript:alert(1)",
	} {
		require.Emptyf(t, m.signOutReturnURL(app), "app %q built a return address", app)
	}

	// No public issuer: nothing to return to, so nothing is offered.
	require.Empty(t, (&Manager{}).signOutReturnURL(SignOutReturnConsole))
	require.Empty(t, (&Manager{}).signOutReturnURLs())

	conn := &iam.IdpConnection{
		ClientID: "dc-client", Enabled: true, ProviderSignOut: true,
		DiscoverySnapshot: &iam.DiscoverySnapshot{
			EndSessionEndpoint: "https://idp.example/logout?tenant=t1&post_logout_redirect_uri=https%3A%2F%2Fevil.example%2F&client_id=other",
		},
	}
	now := conn.CreatedAt
	conn.DiscoveryApprovedAt = &now
	got, err := url.Parse(providerSignOutRequest(conn, consoleSignOutReturn))
	require.NoError(t, err)
	require.Equal(t, url.Values{
		"tenant":                   {"t1"},
		"client_id":                {"dc-client"},
		"post_logout_redirect_uri": {consoleSignOutReturn},
	}, got.Query(), "the endpoint's own parameters are kept, but never its return address or client")
}

// The request is built only from an endpoint a browser may safely be sent to, even if the
// stored snapshot holds one discovery would refuse today (a row written before a rule
// tightened, or edited in the database): never a non-https address off loopback, never a
// non-web scheme, and never with a fragment, which RP-initiated logout does not allow.
func TestTheProviderSignOutRequestRefusesAnUnsafeEndpoint(t *testing.T) {
	conn := func(endpoint string) *iam.IdpConnection {
		c := &iam.IdpConnection{
			ClientID: "dc-client", Enabled: true, ProviderSignOut: true,
			DiscoverySnapshot: &iam.DiscoverySnapshot{EndSessionEndpoint: endpoint},
		}
		now := c.CreatedAt
		c.DiscoveryApprovedAt = &now
		return c
	}

	// Controls: the shapes discovery accepts do build a request.
	require.NotEmpty(t, providerSignOutRequest(conn("https://idp.example/logout"), consoleSignOutReturn))
	require.NotEmpty(t, providerSignOutRequest(conn("http://127.0.0.1:5556/logout"), consoleSignOutReturn))

	for _, bad := range []string{
		"http://idp.example/logout",
		"javascript://idp.example/%0Aalert(1)",
		"data://idp.example/logout",
		"ftp://idp.example/logout",
		"/logout",
		"",
	} {
		require.Emptyf(t, providerSignOutRequest(conn(bad), consoleSignOutReturn), "endpoint %q built a request", bad)
	}

	got, err := url.Parse(providerSignOutRequest(conn("https://idp.example/logout#frag"), consoleSignOutReturn))
	require.NoError(t, err)
	require.Empty(t, got.Fragment, "the endpoint's fragment was carried into the request")
	require.Equal(t, "https://idp.example/logout", got.Scheme+"://"+got.Host+got.Path)
}
