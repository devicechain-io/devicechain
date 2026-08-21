// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// What the REFRESH path does with a scope, driven through RefreshOAuth itself.
//
// 🔴 THIS FILE EXISTS BECAUSE ITS NEIGHBOUR STOPPED ONE LAYER SHORT. The multi-scope
// refresh test in oauth_scoped_mint_test.go calls mintScopedGrant with two different
// scopes — the COMPOSITION the refresh path uses, not the path. Everything between the
// signed token and that composition (the client lookup, the binding rules, the
// single-use KV rotation, isScopeSubset) went unexercised, and a real hole lived in
// exactly that gap: narrowing a client's registered scopes had no effect on any session
// already holding a refresh token, because scopesRegistered ran only at /authorize.
//
// The asymmetry is what made it dangerous rather than merely absent. Revoking the ROLE
// behind an authority takes effect on the next refresh, because mintScopedGrant
// re-resolves roles every time — so an operator who watched that lever work would
// reasonably assume de-registering a scope worked too. It did not, and the release notes
// tell operators to reach for exactly that lever.

// refreshKVStub adds the two methods the refresh path needs on top of the mint stub's
// Put. Get and Delete are the single-use rotation, and Delete is revision-checked, so a
// stub that ignored the revision would make a broken rotation look correct.
type refreshKVStub struct {
	nats.KeyValue
	entries map[string][]byte
	deleted map[string]bool
}

type kvEntry struct {
	nats.KeyValueEntry
	key   string
	value []byte
}

func (e *kvEntry) Key() string      { return e.key }
func (e *kvEntry) Value() []byte    { return e.value }
func (e *kvEntry) Revision() uint64 { return 1 }

func (s *refreshKVStub) Put(key string, value []byte) (uint64, error) {
	if s.entries == nil {
		s.entries = map[string][]byte{}
	}
	s.entries[key] = value
	return 1, nil
}

func (s *refreshKVStub) Get(key string) (nats.KeyValueEntry, error) {
	if s.deleted[key] {
		return nil, nats.ErrKeyNotFound
	}
	v, ok := s.entries[key]
	if !ok {
		return nil, nats.ErrKeyNotFound
	}
	return &kvEntry{key: key, value: v}, nil
}

func (s *refreshKVStub) Delete(key string, _ ...nats.DeleteOpt) error {
	if s.deleted == nil {
		s.deleted = map[string]bool{}
	}
	s.deleted[key] = true
	return nil
}

// refreshEnv is mintTestEnv with a KV that supports rotation and a registered client.
type refreshEnv struct {
	*mintTestEnv
	kvr *refreshKVStub
}

func newRefreshEnv(t *testing.T, clientID string, scopes ...string) *refreshEnv {
	t.Helper()
	base := newMintTestEnv(t)
	kv := &refreshKVStub{}
	base.m.refreshKV = kv
	require.NoError(t, base.store.CreateOAuthClient(context.Background(), &iam.OAuthClient{
		ClientId:     clientID,
		RedirectURIs: []string{"https://example.invalid/cb"},
		Scopes:       scopes,
		Enabled:      true,
	}))
	return &refreshEnv{mintTestEnv: base, kvr: kv}
}

// registerScopes rewrites the client's registered scopes, which is the operator action
// the release notes describe.
func (e *refreshEnv) registerScopes(t *testing.T, clientID string, scopes ...string) {
	t.Helper()
	c, err := e.store.OAuthClientByClientId(context.Background(), clientID)
	require.NoError(t, err)
	c.Scopes = scopes
	require.NoError(t, e.store.UpdateOAuthClient(context.Background(), c))
}

func (e *refreshEnv) claimsOf(t *testing.T, token string) *auth.Claims {
	t.Helper()
	c, err := e.validator.Validate(token)
	require.NoError(t, err)
	return c
}

// 🔴 THE HOLE. De-registering a scope must end the sessions that hold it.
func TestRefreshRefusesAScopeTheClientIsNoLongerRegisteredFor(t *testing.T) {
	const client = "mcp-desktop"
	e := newRefreshEnv(t, client, auth.ScopeReadOnly, auth.ScopeLocation)
	e.seedTenant(t, "acme")
	e.seedMember(t, "pat@example.invalid", "acme", string(auth.LocationRead))

	scope := auth.ScopeReadOnly + " " + auth.ScopeLocation
	tokens, err := e.m.mintScopedGrant(context.Background(), "pat@example.invalid", "acme",
		scope, scope, []string{"https://mcp.example.invalid"}, client)
	require.NoError(t, err)

	// While the scope is registered, a refresh renews it and carries position.
	renewed, err := e.m.RefreshOAuth(context.Background(), tokens.RefreshToken, "", client)
	require.NoError(t, err, "a refresh for a registered scope must succeed")
	require.Contains(t, e.claimsOf(t, renewed.AccessToken).Authorities, string(auth.LocationRead),
		"the control: with the scope registered and the role held, position is carried")

	// The operator narrows the client's registration — the lever the release notes name.
	e.registerScopes(t, client, auth.ScopeReadOnly)

	_, err = e.m.RefreshOAuth(context.Background(), renewed.RefreshToken, "", client)
	require.Error(t, err,
		"de-registering a scope left the session holding it: the refresh renewed a grant "+
			"the client is no longer registered for, and rotation renews its TTL every time")
	require.Contains(t, strings.ToLower(err.Error()), "no longer registered",
		"the refusal must say what an operator has to do about it: %v", err)
}

// The counterweight. A refusal that fired for every refresh would satisfy the test above
// while breaking every OAuth session on the platform.
func TestRefreshStillSucceedsForARegisteredScope(t *testing.T) {
	const client = "mcp-desktop"
	e := newRefreshEnv(t, client, auth.ScopeReadOnly, auth.ScopeLocation)
	e.seedTenant(t, "acme")
	e.seedMember(t, "pat@example.invalid", "acme", string(auth.LocationRead))

	scope := auth.ScopeReadOnly + " " + auth.ScopeLocation
	tokens, err := e.m.mintScopedGrant(context.Background(), "pat@example.invalid", "acme",
		scope, scope, nil, client)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		tokens, err = e.m.RefreshOAuth(context.Background(), tokens.RefreshToken, "", client)
		require.NoErrorf(t, err, "refresh %d of a fully registered grant must succeed", i+1)
		require.Contains(t, e.claimsOf(t, tokens.AccessToken).Authorities, string(auth.LocationRead))
	}
}

// Narrowing at the REQUEST (RFC 6749 §6) is a different thing from narrowing the
// registration, and must keep working: the access token drops the scope, the rotated
// refresh token keeps the original grant, and a later refresh can widen back to it.
func TestRefreshRequestNarrowingDoesNotDowngradeTheGrant(t *testing.T) {
	const client = "mcp-desktop"
	e := newRefreshEnv(t, client, auth.ScopeReadOnly, auth.ScopeLocation)
	e.seedTenant(t, "acme")
	e.seedMember(t, "pat@example.invalid", "acme", string(auth.LocationRead))

	scope := auth.ScopeReadOnly + " " + auth.ScopeLocation
	tokens, err := e.m.mintScopedGrant(context.Background(), "pat@example.invalid", "acme",
		scope, scope, nil, client)
	require.NoError(t, err)

	narrowed, err := e.m.RefreshOAuth(context.Background(), tokens.RefreshToken, auth.ScopeReadOnly, client)
	require.NoError(t, err)
	require.NotContains(t, e.claimsOf(t, narrowed.AccessToken).Authorities, string(auth.LocationRead),
		"a request-narrowed access token must drop the scope it did not ask for")

	widened, err := e.m.RefreshOAuth(context.Background(), narrowed.RefreshToken, scope, client)
	require.NoError(t, err, "the rotated refresh token keeps the ORIGINAL grant, so the "+
		"client can widen back to it")
	require.Contains(t, e.claimsOf(t, widened.AccessToken).Authorities, string(auth.LocationRead))
}

// A refresh may never widen beyond the grant, registration notwithstanding.
func TestRefreshCannotWidenBeyondTheGrant(t *testing.T) {
	const client = "mcp-desktop"
	e := newRefreshEnv(t, client, auth.ScopeReadOnly, auth.ScopeLocation)
	e.seedTenant(t, "acme")
	e.seedMember(t, "pat@example.invalid", "acme", string(auth.LocationRead))

	tokens, err := e.m.mintScopedGrant(context.Background(), "pat@example.invalid", "acme",
		auth.ScopeReadOnly, auth.ScopeReadOnly, nil, client)
	require.NoError(t, err)

	_, err = e.m.RefreshOAuth(context.Background(),
		tokens.RefreshToken, auth.ScopeReadOnly+" "+auth.ScopeLocation, client)
	require.Error(t, err, "a refresh asked for more than the grant and was allowed it")
}
