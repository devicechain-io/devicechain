// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/stretchr/testify/require"
)

// The tenant plane's members, roles and invitations, on the wire (ADR-081): the codes a
// client branches on, the scoped caller's refusal, and the account-existence oracle.

type wireEnv struct {
	schema *gqlcore.Schema
	svc    *admin.Service
	mgr    *identity.Manager
	store  *iam.Store
}

func newWireEnv(t *testing.T) *wireEnv {
	t.Helper()
	db := putest.NewSQLiteDB(t, &iam.TenantTier{}, &iam.Tenant{}, &iam.Role{}, &iam.Identity{},
		&iam.Membership{}, &iam.Invitation{}, &iam.MfaFactor{}, &iam.MfaSourceFailure{}, &iam.MfaRecoveryCode{}, &iam.IdentityLink{})
	rdbm := &rdb.RdbManager{Database: db}
	store := iam.NewStore(rdbm)
	svc := admin.NewService(store, 0, 0, nil)
	svc.SetViewerBaseline(identity.ViewerAuthorities())
	ctx := context.Background()
	tier := &iam.TenantTier{Token: "silver"}
	require.NoError(t, store.CreateTenantTier(ctx, tier))
	require.NoError(t, store.CreateTenant(ctx, &iam.Tenant{Token: "acme", Enabled: true, TierID: tier.ID, PurgeState: iam.PurgeActive}))
	require.NoError(t, store.CreateRole(ctx, &iam.Role{Scope: iam.ScopeTenant, Token: iam.TenantAdminRoleToken, Authorities: []string{"*"}}))
	require.NoError(t, store.CreateRole(ctx, &iam.Role{Scope: iam.ScopeTenant, Token: iam.ViewerRoleToken, Authorities: identity.ViewerAuthorities()}))
	_, err := svc.CreateIdentity(ctx, admin.CreateIdentityInput{Email: "boss@acme.example", Password: "pw", Enabled: true})
	require.NoError(t, err)
	_, err = svc.AddMembership(ctx, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	require.NoError(t, err)
	return &wireEnv{
		schema: gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{}),
		svc:    svc,
		store:  store,
		mgr:    identity.NewManager(nil, rdbm, nil, nil, 0, 0, "", identity.BootstrapConfig{}, nil),
	}
}

func (w *wireEnv) ctx(claims *auth.Claims) context.Context {
	ctx := auth.WithClaims(context.Background(), claims)
	ctx = core.WithTenant(ctx, claims.Tenant)
	ctx = context.WithValue(ctx, ContextMembersKey, w.svc)
	return context.WithValue(ctx, ContextIdentityKey, w.mgr)
}

func bossClaims() *auth.Claims {
	return &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "boss@acme.example",
		Authorities: append([]string{"*"}, identity.ViewerAuthorities()...)}
}

// exec runs a document and returns the data and the first error's extensions.
func (w *wireEnv) exec(t *testing.T, claims *auth.Claims, doc string, vars map[string]any) (map[string]any, map[string]any) {
	t.Helper()
	res := w.schema.Exec(w.ctx(claims), doc, "", vars)
	var data map[string]any
	if len(res.Data) > 0 {
		require.NoError(t, json.Unmarshal(res.Data, &data))
	}
	if len(res.Errors) > 0 {
		ext := res.Errors[0].Extensions
		if ext == nil {
			ext = map[string]any{"message": res.Errors[0].Message}
		}
		return data, ext
	}
	return data, nil
}

const inviteDoc = `mutation($r: InviteMemberRequest!) {
  inviteMember(request: $r) {
    invitationCode
    member { email firstName lastName enabled roles { token owner } scope { kind token } managedByIdentityProvider invitationPending }
  }
}`

// 🔴 NO ACCOUNT-EXISTENCE ORACLE. Inviting an address that already has an account — one
// with a NAME, the field most likely to give it away — answers exactly what inviting a
// brand-new address answers, apart from the address and the one-time code.
func TestInviteResponseIsIdenticalForExistingAndNewIdentity(t *testing.T) {
	w := newWireEnv(t)
	ctx := context.Background()
	first, last := "Known", "Person"
	_, err := w.svc.CreateIdentity(ctx, admin.CreateIdentityInput{Email: "known@elsewhere.example", Password: "pw",
		Enabled: true, FirstName: &first, LastName: &last})
	require.NoError(t, err)

	invite := func(email string) map[string]any {
		data, errs := w.exec(t, bossClaims(), inviteDoc, map[string]any{"r": map[string]any{
			"email": email, "roleTokens": []any{iam.ViewerRoleToken},
			"scope": []any{map[string]any{"kind": "CUSTOMER", "token": "acme-corp"}},
		}})
		require.Nil(t, errs)
		inv := data["inviteMember"].(map[string]any)
		require.NotEmpty(t, inv["invitationCode"])
		m := inv["member"].(map[string]any)
		delete(m, "email")
		return m
	}
	known := invite("known@elsewhere.example")
	unknown := invite("unknown@elsewhere.example")
	require.Equal(t, unknown, known, "inviting an existing account answers differently from inviting a new one")
	require.Nil(t, known["firstName"], "a pending member's name is shown, which says the account existed")
	require.Equal(t, []any{map[string]any{"kind": "CUSTOMER", "token": "acme-corp"}}, known["scope"])
}

// A scoped member is refused every tenant-administration operation, FORBIDDEN with the
// reason SCOPE_UNSUPPORTED — even one whose token names every authority.
func TestAScopedCallerIsRefusedTenantAdministration(t *testing.T) {
	w := newWireEnv(t)
	scoped := bossClaims()
	scoped.ScopeAnchors = []string{"customer:acme-corp"}
	for name, doc := range map[string]string{
		"members":           `{ members(criteria: {pageNumber: 1, pageSize: 10}) { results { email } } }`,
		"member":            `{ member(email: "boss@acme.example") { email } }`,
		"tenantRoles":       `{ tenantRoles { token } }`,
		"tenantAuthorities": `{ tenantAuthorities }`,
		"invitations":       `{ invitations(criteria: {pageNumber: 1, pageSize: 10}) { results { email } } }`,
		"inviteMember":      `mutation { inviteMember(request: {email: "x@y.example", roleTokens: []}) { invitationCode } }`,
		"setMemberRoles":    `mutation { setMemberRoles(email: "boss@acme.example", roleTokens: []) { email } }`,
		"setMemberScope":    `mutation { setMemberScope(email: "boss@acme.example", scope: []) { email } }`,
		"setMemberAccess":   `mutation { setMemberAccess(email: "boss@acme.example", roleTokens: [], scope: []) { email } }`,
		"setMemberEnabled":  `mutation { setMemberEnabled(email: "boss@acme.example", enabled: false) { email } }`,
		"removeMember":      `mutation { removeMember(email: "boss@acme.example") }`,
		"revokeInvitation":  `mutation { revokeInvitation(email: "boss@acme.example") { email } }`,
		"reissue":           `mutation { reissueInvitation(email: "boss@acme.example") { invitationCode } }`,
		"createTenantRole":  `mutation { createTenantRole(request: {token: "x", authorities: []}) { token } }`,
		"updateTenantRole":  `mutation { updateTenantRole(token: "x", request: {}) { token } }`,
		"deleteTenantRole":  `mutation { deleteTenantRole(token: "x") }`,
	} {
		_, ext := w.exec(t, scoped, doc, nil)
		require.NotNilf(t, ext, "%s answered a scoped caller", name)
		require.Equalf(t, "FORBIDDEN", ext["code"], "%s: %v", name, ext)
		require.Equalf(t, auth.ReasonScopeUnsupported, ext["reason"], "%s: %v", name, ext)
	}
	// The negative control: the same token, unscoped, reads the members.
	data, ext := w.exec(t, bossClaims(), `{ members(criteria: {pageNumber: 1, pageSize: 10}) { results { email } } }`, nil)
	require.Nil(t, ext)
	require.NotEmpty(t, data["members"].(map[string]any)["results"])
}

// A typed refusal reaches the client with its own code, which is what a console branches
// on: here SELF_MODIFICATION and ESCALATION. graphql-go reads a code only from the error a
// resolver returns directly, so a resolver that wrapped one would serve FORBIDDEN instead.
func TestTenantPlaneRefusalsCarryTheirCodes(t *testing.T) {
	w := newWireEnv(t)
	_, ext := w.exec(t, bossClaims(), `mutation { removeMember(email: "boss@acme.example") }`, nil)
	require.Equal(t, admin.CodeSelfModification, ext["code"], "%v", ext)

	ctx := context.Background()
	_, err := w.svc.CreateRole(ctx, admin.RoleInput{Scope: "tenant", Token: "deputy",
		Authorities: []string{"member:read", "member:write"}})
	require.NoError(t, err)
	_, err = w.svc.CreateIdentity(ctx, admin.CreateIdentityInput{Email: "deputy@acme.example", Password: "pw", Enabled: true})
	require.NoError(t, err)
	_, err = w.svc.AddMembership(ctx, "deputy@acme.example", "acme", []string{"deputy"})
	require.NoError(t, err)
	deputy := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "deputy@acme.example",
		Authorities: append([]string{"member:read", "member:write"}, identity.ViewerAuthorities()...)}
	_, ext = w.exec(t, deputy, `mutation { setMemberEnabled(email: "boss@acme.example", enabled: false) { email } }`, nil)
	require.Equal(t, admin.CodeEscalation, ext["code"], "%v", ext)
}

// me.scope is the scope the request is bounded by, read from the token.
func TestMeReportsTheCallersScope(t *testing.T) {
	w := newWireEnv(t)
	scoped := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "boss@acme.example",
		ScopeAnchors: []string{"customer:acme-corp"}}
	data, ext := w.exec(t, scoped, `{ me { email scope { kind token } } }`, nil)
	require.Nil(t, ext)
	require.Equal(t, []any{map[string]any{"kind": "CUSTOMER", "token": "acme-corp"}},
		data["me"].(map[string]any)["scope"])

	data, ext = w.exec(t, bossClaims(), `{ me { scope { kind } } }`, nil)
	require.Nil(t, ext)
	require.Equal(t, []any{}, data["me"].(map[string]any)["scope"], "an unscoped member reported a scope")
}

// tenantAuthorities offers a tenant-owned role exactly the tenant-tier vocabulary and "*":
// never an instance authority, which a tenant role could not carry and the write path
// would refuse. The judge is core/auth's own tier table, so the test cannot agree with a
// hand list that drifted with it.
func TestTenantAuthoritiesAreTheTenantTierVocabulary(t *testing.T) {
	w := newWireEnv(t)
	data, ext := w.exec(t, bossClaims(), `{ tenantAuthorities }`, nil)
	require.Nil(t, ext)
	var got []string
	for _, a := range data["tenantAuthorities"].([]any) {
		got = append(got, a.(string))
	}
	require.Equal(t, auth.AuthoritiesForScope(auth.TierTenant), got)
	require.Contains(t, got, "*")
	require.Contains(t, got, string(auth.MemberWrite))
	require.NotContains(t, got, string(auth.TenantRead), "an instance authority was offered to a tenant role")
}

// tenantAuthorities needs role:read in particular: a member who may read members but not
// roles is refused. Without this, any tenant authority the fixture happens to hold would do.
func TestTenantAuthoritiesNeedRoleRead(t *testing.T) {
	w := newWireEnv(t)
	reader := bossClaims()
	reader.Authorities = append(identity.ViewerAuthorities(), string(auth.MemberRead))
	_, ext := w.exec(t, reader, `{ tenantAuthorities }`, nil)
	require.NotNil(t, ext, "tenantAuthorities answered a caller without role:read")
	require.Equal(t, "FORBIDDEN", ext["code"])
}
