// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/config"
	"github.com/devicechain-io/dc-user-management/graphql"
	"github.com/devicechain-io/dc-user-management/identity"
)

// registeredPattern reports the mux pattern that would serve path, or "" when nothing
// would.
//
// 🔴 IT RESOLVES THE ROUTE WITHOUT INVOKING THE HANDLER, and that is what makes this
// test possible at all. These handlers need a live identity Manager, a database and KV
// buckets to RUN; they need none of that to be REGISTERED. ServeMux.Handler performs
// the match and hands back the pattern, so the question under test — is this path
// served, and by this mux — is answered without standing up user-management's world.
func registeredPattern(ms *core.Microservice, path string) string {
	_, pattern := ms.Mux().Handler(httptest.NewRequest(http.MethodGet, path, nil))
	return pattern
}

// newUserManagementForRoutes installs the package globals the registrars close over,
// with a Microservice that owns its own mux, and restores them afterwards.
//
// IdentityManager is a zero-value Manager rather than a real one: every registrar
// either passes it along or takes a method value from it, and neither dereferences it
// until a request arrives — which this test never sends.
func newUserManagementForRoutes(t *testing.T, issuerUrl string) *core.Microservice {
	t.Helper()

	prevMs, prevCfg, prevIdent, prevInternal := Microservice, Configuration, IdentityManager, InternalAuthMux
	t.Cleanup(func() {
		Microservice, Configuration, IdentityManager, InternalAuthMux = prevMs, prevCfg, prevIdent, prevInternal
	})
	InternalAuthMux = nil

	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "user-management"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	IdentityManager = &identity.Manager{}
	Configuration = config.NewUserManagementConfiguration()
	Configuration.Auth.IssuerUrl = issuerUrl

	return Microservice
}

// The routes that move with the switchover regardless of configuration.
//
// 🔴 /auth/jwks IS THE ONE THAT MATTERS MOST IN THIS WHOLE CHANGE. user-management is
// the only service that serves it, and every other service's JWT validator fetches it
// to verify tokens. Leave it on http.DefaultServeMux — which no server serves any more
// — and it answers 404: every peer's validator fails, every peer reports /readyz 503,
// and the entire instance leaves its Service endpoints. The symptom looks like an
// authentication outage, so the search starts in the wrong place entirely.
//
// /auth/service-token is beside it and is the bootstrap trust root: without it no
// service can mint the token it calls another service with, so service-to-service
// traffic stops too — not merely token validation.
//
// They are served by the INTERNAL mux and by nothing else: the public mux must not carry
// them, because the mint is a shared-secret-to-token exchange that stays private only if no
// public listener can reach it, whatever ingress sits in front.
func TestInternalAuthRoutesAreOnTheInternalMuxAndNotThePublicOne(t *testing.T) {
	ms := newUserManagementForRoutes(t, "")

	registerKeyHandlers()
	registerServiceTokenHandler()

	for _, tc := range []struct {
		path string
		why  string
	}{
		{auth.JWKSPath, "every peer service fetches this to validate tokens; losing it takes the whole instance out of its Service endpoints"},
		{auth.ServiceTokenPath, "the bootstrap trust root; losing it stops every service-to-service call, not just token validation"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			_, got := internalAuthMux().Handler(httptest.NewRequest(http.MethodGet, tc.path, nil))
			require.Equal(t, tc.path, got,
				"%s is not served by the internal auth mux — %s", tc.path, tc.why)
			require.Empty(t, registeredPattern(ms, tc.path),
				"%s is on the PUBLIC mux; the internal-only endpoints must never share it", tc.path)
		})
	}
}

// The caller-level fail-closed check: with the WHOLE public surface registered, as production
// does, a request for either internal endpoint on the public handler answers 404, and the same
// request on the internal handler is served (a bad secret is 401, not 404).
//
// 🔴 THIS IS WHAT REPLACES THE INGRESS SNIPPET. It runs with no ingress anywhere in the
// picture, so it fails if any registration puts a mint on the public mux again.
func TestPublicHandlerAnswers404ForInternalEndpointsWithNoIngressInFront(t *testing.T) {
	ms := newUserManagementForRoutes(t, "https://iot.example.com/api/user-management")
	ms.InstanceConfiguration.Infrastructure.ServiceAuth.Secret = "right-secret"

	registerKeyHandlers()
	registerServiceTokenHandler()
	registerOAuthHandlersIfEnabled()
	registerSsoHandlersIfEnabled()
	registerAdminHandler()
	registerSettingsHandler()
	ms.RegisterProbes(nil)

	public := httptest.NewServer(ms.Mux())
	defer public.Close()
	internal := httptest.NewServer(internalAuthMux())
	defer internal.Close()

	do := func(t *testing.T, base, method, path string) int {
		req, err := http.NewRequest(method, base+path, strings.NewReader(`{}`))
		require.NoError(t, err)
		req.Header.Set(auth.ServiceSecretHeader, "wrong-secret")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}

	for _, p := range []string{auth.ServiceTokenPath, auth.JWKSPath, "/auth/", "/auth/anything"} {
		for _, m := range []string{http.MethodGet, http.MethodPost} {
			require.Equal(t, http.StatusNotFound, do(t, public.URL, m, p),
				"public %s %s must be 404 with no ingress in front", m, p)
		}
	}
	require.Equal(t, http.StatusUnauthorized, do(t, internal.URL, http.MethodPost, auth.ServiceTokenPath),
		"the internal listener must serve the mint (a wrong secret is refused 401, not routed 404)")
	require.NotEqual(t, http.StatusNotFound, do(t, internal.URL, http.MethodGet, auth.JWKSPath),
		"the internal listener must serve the JWKS")
	// The public JWKS mirror an external verifier uses is unaffected.
	require.NotEqual(t, http.StatusNotFound, do(t, public.URL, http.MethodGet, identity.OAuthJwksPath))
}

// The listener itself: started from the instance configuration it binds the configured
// port, serves the internal mux, and refuses a port that would collapse it onto the public one.
func TestInternalAuthListenerBindsTheConfiguredPort(t *testing.T) {
	ms := newUserManagementForRoutes(t, "")
	prevSrv := InternalAuthServer
	t.Cleanup(func() { InternalAuthServer = prevSrv })

	registerServiceTokenHandler()

	ms.InstanceConfiguration.Infrastructure.UserManagement.InternalPort = 0
	require.Error(t, startInternalAuthListener(), "an unset port must be refused, not bound at random")
	ms.InstanceConfiguration.Infrastructure.UserManagement.InternalPort = core.HttpPort
	require.Error(t, startInternalAuthListener(), "the public port must be refused")

	// Pick a free port, then release it for the listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	ms.InstanceConfiguration.Infrastructure.UserManagement.InternalPort = uint32(port)
	require.NoError(t, startInternalAuthListener())
	t.Cleanup(func() { _ = InternalAuthServer.Shutdown(context.Background()) })

	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d%s", port, auth.ServiceTokenPath), "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.NotEqual(t, http.StatusNotFound, resp.StatusCode, "the mint is not served on the internal port")
}

// The rest of the unconditional set: the two admin-tier GraphQL planes and the
// branding-logo endpoints.
//
// /branding/logo is one of the two registrations made INDIRECTLY, by handing a
// *http.ServeMux to a helper — the shape a search for http.Handle does not find, and
// the shape that made two of user-management's ten sites invisible in the first
// enumeration of this work. It is asserted here for that reason as much as its own.
func TestAdminAndBrandingRoutesAreOnTheOwnedMux(t *testing.T) {
	ms := newUserManagementForRoutes(t, "")

	registerAdminHandler()
	registerSettingsHandler()
	graphql.RegisterBrandingLogoHandler(Microservice.Mux(), BlobStore, IdentityManager, IdentityManager.Validator())

	for _, tc := range []struct {
		path string
		why  string
	}{
		{"/admin/graphql", "the instance-scoped admin API; losing it takes the operator plane offline"},
		{"/settings/graphql", "the instance-scoped settings API, on the same identity-token lane"},
		{"/branding/logo", "registered indirectly through a *http.ServeMux argument, which is the shape a grep for http.Handle misses"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			require.Equal(t, tc.path, registeredPattern(ms, tc.path),
				"%s is not served by this microservice's mux — %s", tc.path, tc.why)
		})
	}
}

// The OAuth surface across all three configuration shapes.
//
// 🔴 THE SHIPPED DEFAULT IS OAUTH OFF, AND THE PATH-CARRYING ISSUER IS WHAT AN ENABLED
// INSTANCE LOOKS LIKE. user-management's issuerUrl is operator-set and deliberately NOT
// derived from the ingress — setting it changes the `iss` claim of every token the
// instance mints, so it is its own switch, and the AS is off until it is thrown.
// (mcp's resourceUrl IS ingress-derived; that is a different value on a different
// service, and conflating the two is what made an earlier version of this comment
// wrong.)
//
// All three shapes still earn their place. An operator following the documented value
// sets an https origin with a path, and RegisterMetadataHandlers registers ONE pattern
// for a path-less issuer and TWO for a path-carrying one — the second being the RFC
// 8414 §3.1 inserted location a spec-following client constructs first. A test that ran
// only the path-less shape would pass while an enabled instance silently lost that
// second route.
func TestOAuthRoutesAcrossEveryConfigurationShape(t *testing.T) {
	oauthPaths := []string{
		identity.MetadataPath,
		identity.TokenPath,
		identity.AuthorizePath,
		identity.UserinfoPath,
		identity.OAuthJwksPath,
	}

	t.Run("oauth off — the shipped default", func(t *testing.T) {
		ms := newUserManagementForRoutes(t, "")
		require.False(t, Configuration.OAuthEnabled(), "an empty issuer must leave the OAuth surface off")

		// 🔴 PRODUCTION'S GATE, NOT A COPY OF IT. This calls the same function the
		// initializer calls and lets it decide. An earlier draft simply did not call
		// registerOAuthHandlers — which asserts against the test's own restatement of
		// the condition and would have stayed green if the initializer had started
		// calling it unconditionally, i.e. against the exact regression it names.
		registerOAuthHandlersIfEnabled()

		for _, p := range oauthPaths {
			require.Empty(t, registeredPattern(ms, p),
				"%s is served with OAuth disabled; the surface must stay off, fail-closed", p)
		}
	})

	t.Run("oauth on, path-less issuer", func(t *testing.T) {
		const issuer = "https://as.example.com"
		ms := newUserManagementForRoutes(t, issuer)
		require.True(t, Configuration.OAuthEnabled())
		registerOAuthHandlers()

		for _, p := range oauthPaths {
			require.Equal(t, p, registeredPattern(ms, p), "%s is not on the owned mux", p)
		}
		// With no path on the issuer the inserted location IS the bare suffix, so there
		// is nothing extra to register — and registering it twice would panic.
		require.Equal(t, identity.MetadataPath, identity.MetadataPathFor(issuer),
			"a path-less issuer must not produce a second, distinct metadata location")
	})

	t.Run("oauth on, path-carrying issuer (an enabled instance)", func(t *testing.T) {
		const issuer = "https://iot.example.com/api/user-management"
		ms := newUserManagementForRoutes(t, issuer)
		require.True(t, Configuration.OAuthEnabled())
		registerOAuthHandlers()

		for _, p := range oauthPaths {
			require.Equal(t, p, registeredPattern(ms, p), "%s is not on the owned mux", p)
		}

		inserted := identity.MetadataPathFor(issuer)
		require.NotEqual(t, identity.MetadataPath, inserted,
			"this issuer carries a path, so the inserted location must differ from the bare suffix — "+
				"otherwise this case is not testing the shape it claims to")
		require.Equal(t, inserted, registeredPattern(ms, inserted),
			"the RFC 8414 inserted metadata location is not served; a spec-following client "+
				"constructs this path FIRST and aborts its discovery walk when it 404s")
	})
}

// The single sign-on callback is registered exactly when the instance has a public issuer
// URL, and on the owned mux with its method and its connection segment.
//
// 🔴 PRODUCTION'S GATE, NOT A COPY OF IT: registerSsoHandlersIfEnabled is the function the
// initializer calls. The same switch turns OAuth on, because a sign-in that has nowhere
// public to return to cannot complete.
func TestSsoCallbackRouteIsRegisteredIffAnIssuerIsConfigured(t *testing.T) {
	const callback = "/sso/callback/acme-idp"

	t.Run("no issuer: nothing is served", func(t *testing.T) {
		ms := newUserManagementForRoutes(t, "")
		registerSsoHandlersIfEnabled()
		require.Empty(t, registeredPattern(ms, callback),
			"the callback is served with no public issuer URL; single sign-on must stay off, fail-closed")
	})

	t.Run("issuer set: the per-connection callback is served", func(t *testing.T) {
		ms := newUserManagementForRoutes(t, "https://iot.example.com/api/user-management")
		registerSsoHandlersIfEnabled()
		require.Equal(t, identity.SsoCallbackPattern, registeredPattern(ms, callback))
		require.Equal(t, identity.SsoCallbackPattern, registeredPattern(ms, "/sso/callback/another-connection"),
			"every connection has its own path under the same route")
	})
}
