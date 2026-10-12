// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// InstallSigningForTest gives a Manager built by NewManager the two things a successful
// password sign-in needs beyond its credential checker -- a signing key and a session store
// (a stub) -- which Initialize would otherwise provide from NATS and the secret store. With
// them the external test package can drive a login through the served schema all the way to
// an issued token, instead of to a nil dereference that graphql-go recovers into an
// ordinary-looking error.
func InstallSigningForTest(t *testing.T, m *Manager) {
	t.Helper()
	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	m.issuer = auth.NewIssuer(key, "test", time.Minute, time.Hour)
	m.refreshKV = &stubRefreshKV{}
}

// NewServedManager hands the external identity_test package a Manager built the way the
// session tests build one - real issuer and validator, refresh state in a real JetStream
// bucket, a seeded tenant "acme" - with one member, pat@example.com / pw-1. It lives here
// because the Manager's collaborators are unexported, and the graphql package cannot be
// imported from package identity itself (it imports identity).
func NewServedManager(t *testing.T) *Manager {
	t.Helper()
	e := newSessionEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	return e.m
}

// ServedSso is a Manager wired for single sign-on over a stub provider, for the external
// test package that serves it through the real GraphQL handlers.
type ServedSso struct {
	Manager *Manager
	// Authorize plays the provider's sign-in page for (sub, email): it returns the state and
	// code the browser is redirected back with.
	Authorize func(authURL, sub, email string) (state, code string)
	// Connection is the token of the tenant connection, serving acme; LinkedEmail is linked
	// to it. SuperuserEmail/SuperuserPassword sign in with a password.
	Connection        string
	LinkedEmail       string
	SuperuserEmail    string
	SuperuserPassword string
	ReturnTo          string
	Secret            string
	CallbackPrefix    string
	// EnsureRole creates a role in the given scope ("tenant" or "system").
	EnsureRole func(scope, token string, authorities ...string)
	// CreateMember creates a password identity with a membership in acme.
	CreateMember func(email, password string)
	// CreateIdentity creates a password identity with no membership at all.
	CreateIdentity func(email, password string)
}

// NewServedSsoManager builds the single sign-on harness and a password superuser.
func NewServedSsoManager(t *testing.T) *ServedSso {
	t.Helper()
	e := newSsoEnv(t)
	e.linkedPat(t)
	e.createMember(t, "su@example.com", "su-pass")
	e.grantSuperuser(t, "su@example.com")
	return &ServedSso{
		Manager: e.m, Authorize: e.op.authorize, Connection: "acme-idp", LinkedEmail: linkedEmail,
		SuperuserEmail: "su@example.com", SuperuserPassword: "su-pass",
		ReturnTo: ssoTestReturnTo, Secret: ssoTestSecret, CallbackPrefix: SsoCallbackPrefix,
		EnsureRole: func(scope, token string, authorities ...string) {
			t.Helper()
			require.NoError(t, e.store.EnsureRole(context.Background(), iam.RoleScope(scope), token, token, authorities))
		},
		CreateMember: func(email, password string) {
			t.Helper()
			e.createMember(t, email, password)
		},
		CreateIdentity: func(email, password string) {
			t.Helper()
			_, err := e.admin.CreateIdentity(context.Background(), admin.CreateIdentityInput{Email: email, Password: password, Enabled: true})
			require.NoError(t, err)
		},
	}
}

// ChallengeForTest is the S256 code challenge for a browser secret.
func ChallengeForTest(verifier string) string { return pkceChallenge(verifier) }
