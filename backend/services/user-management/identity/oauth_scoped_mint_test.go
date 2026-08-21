// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/glebarez/sqlite"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 🔴 THIS FILE TESTS THE LAYER THE DEFECT ACTUALLY LIVED AT.
//
// Everything else about the scope cap is asserted on the pieces — the allowance
// table, effectiveAuthorities, IntersectAuthorities, capToScope. Those are the right
// assertions and they caught the bug once it was understood, but not one of them
// would have caught it first: each piece was correct in isolation and the defect was
// in how mintScopedGrant composed them. So these tests drive the real
// mintScopedGrant, over a real store and a real signing key, and read the
// authorities back out of the ISSUED, SIGNED TOKEN through the ordinary validator —
// the same bytes an MCP resource server would verify.

// stubRefreshKV is the one collaborator that cannot be real here: mintScopedGrant
// records each refresh jti in NATS KV. Only Put is reached, so the interface is
// embedded and left nil for everything else — a test that starts calling another
// method gets a loud nil-panic naming it, rather than a silent stub answer that
// makes a broken path look healthy.
type stubRefreshKV struct {
	nats.KeyValue
	puts map[string]string
}

func (s *stubRefreshKV) Put(key string, value []byte) (uint64, error) {
	if s.puts == nil {
		s.puts = map[string]string{}
	}
	s.puts[key] = string(value)
	return uint64(len(s.puts)), nil
}

// mintTestEnv is a Manager wired the way the token endpoint has it: a real iam store
// over in-memory sqlite, a real RSA issuer, and the matching validator so a minted
// token can be read back the way a resource server reads it.
type mintTestEnv struct {
	m         *Manager
	store     *iam.Store
	validator *auth.Validator
	kv        *stubRefreshKV
	roleSeq   int
}

func newMintTestEnv(t *testing.T) *mintTestEnv {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(
		&iam.TenantTier{}, &iam.Tenant{}, &iam.Role{}, &iam.Identity{}, &iam.Membership{},
	))
	store := iam.NewStore(&rdb.RdbManager{Database: db})

	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	kv := &stubRefreshKV{}
	m := &Manager{
		iam:       store,
		issuer:    auth.NewIssuer(key, "https://as.example.com", time.Minute, time.Hour),
		validator: auth.NewValidator(&key.PublicKey),
		refreshKV: kv,
		accessTTL: time.Minute,
	}
	return &mintTestEnv{m: m, store: store, validator: auth.NewValidator(&key.PublicKey), kv: kv}
}

// seedTenant creates an active, enabled tenant the grant path will admit.
func (e *mintTestEnv) seedTenant(t *testing.T, token string) {
	t.Helper()
	ctx := context.Background()
	tier := &iam.TenantTier{Token: "silver"}
	require.NoError(t, e.store.CreateTenantTier(ctx, tier))
	require.NoError(t, e.store.CreateTenant(ctx, &iam.Tenant{
		Token: token, Enabled: true, TierID: tier.ID, PurgeState: iam.PurgeActive,
	}))
}

// seedMember creates an enabled identity with a membership in tenant, holding one
// tenant role carrying exactly the given authorities. Passing none produces the
// member every deployment has most of: a person with a role that grants nothing
// beyond the viewer baseline they receive for being enabled at all.
func (e *mintTestEnv) seedMember(t *testing.T, email, tenant string, authorities ...string) {
	t.Helper()
	ctx := context.Background()
	e.roleSeq++
	role := &iam.Role{
		Scope: iam.ScopeTenant,
		// A role token has its own grammar (letters/digits/hyphen/underscore), so it
		// cannot be derived from the email — the '@' is refused on write.
		Token:       fmt.Sprintf("test-role-%d", e.roleSeq),
		Authorities: authorities,
	}
	require.NoError(t, e.store.CreateRole(ctx, role))
	require.NoError(t, e.store.CreateIdentity(ctx, &iam.Identity{
		Email: email, Enabled: true, PasswordHash: "x",
		Memberships: []iam.Membership{{
			TenantId: tenant, Enabled: true, TenantRoles: []iam.Role{*role},
		}},
	}))
}

// seedSuperuser creates an identity holding the well-known superuser SYSTEM role,
// which is what isSuperuser keys on and what makes effectiveAuthorities return "*".
func (e *mintTestEnv) seedSuperuser(t *testing.T, email string) {
	t.Helper()
	ctx := context.Background()
	su := &iam.Role{
		Scope:       iam.ScopeSystem,
		Token:       iam.SuperuserRoleToken,
		Authorities: []string{string(auth.AuthorityAll)},
	}
	require.NoError(t, e.store.CreateRole(ctx, su))
	require.NoError(t, e.store.CreateIdentity(ctx, &iam.Identity{
		Email: email, Enabled: true, PasswordHash: "x",
		SystemRoles: []iam.Role{*su},
	}))
}

// mint drives the real grant path and returns the claims carried by the SIGNED
// access token, read back through the ordinary validator.
func (e *mintTestEnv) mint(t *testing.T, email, tenant, scope string) *auth.Claims {
	t.Helper()
	toks, err := e.m.mintScopedGrant(context.Background(), email, tenant, scope, scope,
		[]string{"https://mcp.example.com"}, "mcp-client")
	require.NoError(t, err)
	require.Equal(t, scope, toks.Scope, "the response must echo the granted scope")
	claims, err := e.validator.Validate(toks.AccessToken)
	require.NoError(t, err, "a minted access token must validate")
	return claims
}

// 🔴 THE DEFECT, AT THE LAYER IT LIVED AT. A tenant SUPERUSER — the most privileged
// subject the platform has — minting a token for the only scope that existed
// received one WITHOUT location:read, because IntersectAuthorities caps "*" to the
// allowance rather than expanding it and the allowance was the viewer baseline. MCP's
// query_locations could therefore not be called by anybody, on any install.
//
// The fix is a scope, not a wider ceiling: ask for `location` and the token carries
// position; ask for `read-only` alone and it does not, however total the subject is.
func TestMintedTokenCarriesPositionOnlyWithTheLocationScope(t *testing.T) {
	e := newMintTestEnv(t)
	e.seedTenant(t, "acme")
	e.seedSuperuser(t, "root@example.com")

	withLocation := e.mint(t, "root@example.com", "acme", "read-only location")
	require.Contains(t, withLocation.Authorities, string(auth.LocationRead),
		"a superuser granted read-only+location must receive position")

	readOnly := e.mint(t, "root@example.com", "acme", auth.ScopeReadOnly)
	require.NotContains(t, readOnly.Authorities, string(auth.LocationRead),
		"a token whose resource owner approved only read-only must not reach position")

	// The precondition that makes that absence meaningful: read-only is doing real
	// work rather than yielding an empty token.
	require.Contains(t, readOnly.Authorities, string(auth.EventRead))
	// And the cap holds in the direction it always had to: "*" is never issued.
	require.NotContains(t, withLocation.Authorities, string(auth.AuthorityAll),
		"an OAuth token must never carry the super-authority")
	require.NotContains(t, readOnly.Authorities, string(auth.AuthorityAll))
}

// 🔴 THE COUNTERWEIGHT, at token level. A scope is a CEILING: an ordinary member
// whose roles never granted location:read asks for `location`, the resource owner
// approves it, and the issued token still does not carry it. Requesting a scope
// grants nothing — only a role does.
func TestMintedTokenGivesAMemberNoPositionWithoutTheRole(t *testing.T) {
	e := newMintTestEnv(t)
	e.seedTenant(t, "acme")
	e.seedMember(t, "nolocation@example.com", "acme", string(auth.DeviceWrite))

	claims := e.mint(t, "nolocation@example.com", "acme", "read-only location")
	require.NotContains(t, claims.Authorities, string(auth.LocationRead),
		"asking for the location scope must not grant the authority")
	// Not vacuous: the member really did receive a populated read-only token.
	require.Contains(t, claims.Authorities, string(auth.EventRead))
	// ...and the write they hold on their role never survives a read-only surface.
	require.NotContains(t, claims.Authorities, string(auth.DeviceWrite),
		"a read scope must not carry a write authority the subject happens to hold")
}

// The case the scope exists to serve: a member whose ROLE grants location:read keeps
// it — but only on a token whose SCOPE admitted it. Role and scope are both
// necessary and neither is sufficient, which is the whole design in one test.
func TestMintedTokenNeedsBothTheRoleAndTheScope(t *testing.T) {
	e := newMintTestEnv(t)
	e.seedTenant(t, "acme")
	e.seedMember(t, "fleet@example.com", "acme", string(auth.LocationRead))

	granted := e.mint(t, "fleet@example.com", "acme", "read-only location")
	require.Contains(t, granted.Authorities, string(auth.LocationRead),
		"role grants it and the scope admits it, so the token must carry it")

	withheld := e.mint(t, "fleet@example.com", "acme", auth.ScopeReadOnly)
	require.NotContains(t, withheld.Authorities, string(auth.LocationRead),
		"the same subject, authorizing a client for read-only only, must be able to "+
			"withhold position from it")
}

// An unknown scope never reaches the mint: it is refused as invalid_scope before the
// identity is even resolved, so no token exists for a scope with no ceiling. Asserted
// through the error code the token endpoint renders, not just "an error happened".
func TestMintRefusesAnUndefinedScope(t *testing.T) {
	e := newMintTestEnv(t)
	e.seedTenant(t, "acme")
	e.seedMember(t, "member@example.com", "acme")

	_, err := e.m.mintScopedGrant(context.Background(), "member@example.com", "acme",
		"read-only admin", "read-only admin", nil, "mcp-client")
	require.Error(t, err)
	oe, ok := err.(*oauthError)
	require.True(t, ok, "want an oauthError, got %T", err)
	require.Equal(t, "invalid_scope", oe.Code)
}

// A refresh that NARROWS scope narrows the access token while the rotated refresh
// token keeps the original grant — so the session can widen back to what the resource
// owner actually approved, and never past it. This is the multi-scope path the
// `location` split makes reachable for the first time, so it is pinned rather than
// assumed: mintScopedGrant takes the two scopes separately for exactly this reason.
func TestRefreshNarrowingKeepsTheGrantOnTheRefreshToken(t *testing.T) {
	e := newMintTestEnv(t)
	e.seedTenant(t, "acme")
	e.seedMember(t, "fleet@example.com", "acme", string(auth.LocationRead))

	toks, err := e.m.mintScopedGrant(context.Background(), "fleet@example.com", "acme",
		auth.ScopeReadOnly, "read-only location", []string{"https://mcp.example.com"}, "mcp-client")
	require.NoError(t, err)

	access, err := e.validator.Validate(toks.AccessToken)
	require.NoError(t, err)
	require.Equal(t, auth.ScopeReadOnly, access.Scope)
	require.NotContains(t, access.Authorities, string(auth.LocationRead),
		"the narrowed access token must not carry the authority it narrowed away")

	// ValidateRefresh, not Validate: the two token types are not interchangeable, and
	// the access validator refuses a refresh token outright. Worth touching here
	// because the whole narrowing design turns on the pair carrying DIFFERENT scopes.
	_, err = e.validator.Validate(toks.RefreshToken)
	require.Error(t, err, "a refresh token must never validate as an access token")
	refresh, err := e.validator.ValidateRefresh(toks.RefreshToken)
	require.NoError(t, err)
	require.Equal(t, "read-only location", refresh.Scope,
		"the rotated refresh token keeps the originally granted scope")
	require.Contains(t, refresh.Authorities, string(auth.LocationRead),
		"...and therefore still carries what that grant reaches")

	// Narrowing is one-way at the policy layer: isScopeSubset is what stops the
	// session widening past the grant, in either direction of comparison.
	require.True(t, isScopeSubset(auth.ScopeReadOnly, "read-only location"))
	require.False(t, isScopeSubset("read-only location", auth.ScopeReadOnly),
		"a refresh must never widen back to a scope the grant did not include")

	// The refresh jti really was recorded against the subject — otherwise the token
	// is unrevocable and this whole path is only pretending to work.
	require.Len(t, e.kv.puts, 1)
	for jti, email := range e.kv.puts {
		require.NotEmpty(t, jti)
		require.Equal(t, "fleet@example.com", email)
	}
}
