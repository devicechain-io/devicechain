// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The tenant plane's delegated administration (ADR-081), driven through the service with
// the caller taken from verified claims exactly as the resolvers leave it.

// viewerBaselineForTests is the baseline the tests wire, spelled out so a test reads
// what a member holds without a role.
var viewerBaselineForTests = []string{"device:read", "event:read", "state:read", "command:read", "alarm:read", "dashboard:read"}

type planeEnv struct {
	s  *Service
	db *gorm.DB
}

func newPlaneEnv(t *testing.T) *planeEnv {
	t.Helper()
	db := putest.NewSQLiteDB(t, &iam.TenantTier{}, &iam.Tenant{}, &iam.Role{}, &iam.Identity{},
		&iam.Membership{}, &iam.Invitation{}, &iam.IdentityLink{}, &iam.IdpConnection{},
		&iam.IdpConnectionTenant{}, &iam.IdpRoleMapping{}, &iam.ServiceAccount{}, &iam.ServiceAccountSecret{})
	s := NewService(iam.NewStore(&rdb.RdbManager{Database: db}), 0, 0, nil)
	s.SetViewerBaseline(viewerBaselineForTests)
	e := &planeEnv{s: s, db: db}
	ctx := context.Background()
	tier := &iam.TenantTier{Token: "silver"}
	require.NoError(t, s.iam.CreateTenantTier(ctx, tier))
	for _, tok := range []string{"acme", "globex"} {
		require.NoError(t, s.iam.CreateTenant(ctx, &iam.Tenant{Token: tok, Enabled: true, TierID: tier.ID, PurgeState: iam.PurgeActive}))
	}
	// The catalogue: the full-authority tenant-admin and the read-only viewer, as seeded.
	for _, r := range []iam.Role{
		{Scope: iam.ScopeTenant, Token: iam.TenantAdminRoleToken, Authorities: []string{"*"}},
		{Scope: iam.ScopeTenant, Token: iam.ViewerRoleToken, Authorities: viewerBaselineForTests},
		{Scope: iam.ScopeTenant, Token: "member-admin", Authorities: []string{"member:read", "member:write", "role:read"}},
		{Scope: iam.ScopeTenant, Token: "operator", Authorities: []string{"alarm:write", "command:write"}},
	} {
		r := r
		require.NoError(t, s.iam.CreateRole(ctx, &r))
	}
	return e
}

// member creates an identity (if needed) with an enabled membership in tenant holding the
// given catalogue roles, optionally scoped.
func (e *planeEnv) member(t *testing.T, email, tenant string, roles []string, scope ...string) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.s.iam.IdentityByEmail(ctx, email); errors.Is(err, gorm.ErrRecordNotFound) {
		_, err := e.s.CreateIdentity(ctx, CreateIdentityInput{Email: email, Password: "pw", Enabled: true})
		require.NoError(t, err)
	}
	var anchors []ScopeAnchorInput
	for _, a := range scope {
		anchors = append(anchors, ScopeAnchorInput{Kind: auth.AnchorKindCustomer, Token: a})
	}
	_, err := e.s.AddScopedMembership(ctx, email, tenant, roles, anchors)
	require.NoError(t, err)
}

// as is a request context for email acting in tenant, its token carrying authorities
// plus the viewer baseline (as the mint unions it).
func as(email, tenant string, authorities ...string) context.Context {
	return auth.WithClaims(context.Background(), &auth.Claims{
		TokenType: auth.TokenTypeAccess, Tenant: tenant, Username: email,
		Authorities: append(append([]string{}, authorities...), viewerBaselineForTests...),
	})
}

func refusalCode(err error) string {
	var r *TenantRefusal
	if errors.As(err, &r) {
		return r.Code()
	}
	return ""
}

func (e *planeEnv) epoch(t *testing.T, email string) string {
	t.Helper()
	id, err := e.s.iam.IdentityByEmail(context.Background(), email)
	require.NoError(t, err)
	return id.SessionEpoch
}

// ---- the tenant stamp ----------------------------------------------------------------------

// 🔴 THE RISKIEST LINE IN THE SLICE. A tenant-plane role written with tenant_id NULL is a
// CATALOGUE role: visible and assignable in every tenant. The stamp is checked by VALUE,
// on the row as stored, and the admin plane's create is checked as the counterweight —
// it must still write NULL.
func TestCreateTenantRoleStampsTheTenant(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})

	created, err := e.s.CreateTenantRole(as("boss@acme.example", "acme", "*"), TenantRoleInput{
		Token: "field-tech", Name: "Field technician", Authorities: []string{"device:read", "command:write"},
	})
	require.NoError(t, err)
	require.NotNil(t, created.TenantID)
	require.Equal(t, "acme", *created.TenantID)

	var stored []struct {
		Token    string
		TenantID *string
	}
	require.NoError(t, e.db.Raw(`SELECT token, tenant_id FROM iam_roles WHERE token IN ('field-tech', 'catalogue-made') ORDER BY token`).Scan(&stored).Error)
	require.Len(t, stored, 1)
	require.NotNil(t, stored[0].TenantID, "a tenant-plane role was stored with tenant_id NULL — a CATALOGUE role "+
		"created from a tenant token, visible in every tenant")
	require.Equal(t, "acme", *stored[0].TenantID)

	// The counterweight: the operator's create still writes the catalogue.
	_, err = e.s.CreateRole(context.Background(), RoleInput{Scope: "tenant", Token: "catalogue-made", Authorities: []string{"device:read"}})
	require.NoError(t, err)
	stored = nil
	require.NoError(t, e.db.Raw(`SELECT token, tenant_id FROM iam_roles WHERE token = 'catalogue-made'`).Scan(&stored).Error)
	require.Len(t, stored, 1)
	require.Nil(t, stored[0].TenantID, "the admin plane's createRole must still write a catalogue role")

	// And the role reaches only acme: globex neither lists nor can assign it.
	e.member(t, "boss@globex.example", "globex", []string{iam.TenantAdminRoleToken})
	globexRoles, err := e.s.TenantRoles(as("boss@globex.example", "globex", "*"))
	require.NoError(t, err)
	for _, r := range globexRoles {
		require.NotEqual(t, "field-tech", r.Token, "globex can see acme's own role")
	}
	_, _, err = e.s.InviteMember(as("boss@globex.example", "globex", "*"), InviteInput{
		Email: "x@globex.example", RoleTokens: []string{"field-tech"},
	})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "globex assigned acme's own role")
}

// A tenant cannot shadow an instance role's token, and the operator cannot later create an
// instance role at a token a tenant already uses: "operator" must name one role in a tenant.
func TestARoleTokenNamesOneRoleInATenant(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	ctx := as("boss@acme.example", "acme", "*")

	_, err := e.s.CreateTenantRole(ctx, TenantRoleInput{Token: "operator", Authorities: []string{"device:read"}})
	require.True(t, conflict.Is(err), "a tenant role shadowed an instance role's token: %v", err)

	_, err = e.s.CreateTenantRole(ctx, TenantRoleInput{Token: "auditor", Authorities: []string{"device:read"}})
	require.NoError(t, err)
	_, err = e.s.CreateRole(context.Background(), RoleInput{Scope: "tenant", Token: "auditor"})
	require.True(t, conflict.Is(err), "an instance role was created at a token a tenant already uses: %v", err)

	// Two tenants may each own a role at the same token.
	e.member(t, "boss@globex.example", "globex", []string{iam.TenantAdminRoleToken})
	_, err = e.s.CreateTenantRole(as("boss@globex.example", "globex", "*"), TenantRoleInput{Token: "auditor"})
	require.NoError(t, err)
}

// The instance's catalogue roles are listed READ_ONLY to a tenant: it cannot edit or
// delete them.
func TestCatalogueRolesAreReadOnlyOnTheTenantPlane(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	ctx := as("boss@acme.example", "acme", "*")

	_, err := e.s.UpdateTenantRole(ctx, "operator", &RoleUpdateRequest{Name: dcgraphql.OptionalStringOf("mine now")})
	require.Equal(t, CodeReadOnly, refusalCode(err), "got %v", err)
	_, err = e.s.DeleteTenantRole(ctx, "viewer")
	require.Equal(t, CodeReadOnly, refusalCode(err), "got %v", err)

	cat, err := e.s.iam.CatalogueRoleByScopeToken(context.Background(), iam.ScopeTenant, "operator")
	require.NoError(t, err)
	require.False(t, cat.Name.Valid, "the catalogue role was changed from a tenant plane")
}

// ---- escalation ----------------------------------------------------------------------------

func TestAdminCannotGrantAboveSelf(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "deputy@acme.example", "acme", []string{"member-admin"})
	e.member(t, "pat@acme.example", "acme", []string{iam.ViewerRoleToken})
	deputy := as("deputy@acme.example", "acme", "member:read", "member:write", "role:read")

	// Defining: an authority the deputy does not hold, "*", and a system-tier one.
	for _, auths := range [][]string{{"command:write"}, {"*"}, {"tenant:write"}} {
		_, err := e.s.CreateTenantRole(deputy, TenantRoleInput{Token: "x", Authorities: auths})
		require.Equalf(t, CodeEscalation, refusalCode(err), "defining %v: %v", auths, err)
	}

	// Assigning a role that grants what the deputy does not hold.
	_, err := e.s.SetMemberRoles(deputy, "pat@acme.example", []string{"operator"})
	require.Equal(t, CodeEscalation, refusalCode(err), "got %v", err)
	_, _, err = e.s.InviteMember(deputy, InviteInput{Email: "new@acme.example", RoleTokens: []string{iam.TenantAdminRoleToken}})
	require.Equal(t, CodeEscalation, refusalCode(err), "got %v", err)

	// Acting on someone above: the deputy may not strip, scope, disable or remove the boss.
	for name, op := range map[string]func() error{
		"roles": func() error { _, err := e.s.SetMemberRoles(deputy, "boss@acme.example", nil); return err },
		"scope": func() error { _, err := e.s.SetMemberScope(deputy, "boss@acme.example", customers("c1")); return err },
		"access": func() error {
			_, err := e.s.SetMemberAccess(deputy, "boss@acme.example", []string{iam.TenantAdminRoleToken}, customers("c1"))
			return err
		},
		"disable": func() error { _, err := e.s.SetMemberEnabled(deputy, "boss@acme.example", false); return err },
		"remove":  func() error { _, err := e.s.RemoveMember(deputy, "boss@acme.example"); return err },
	} {
		require.Equalf(t, CodeEscalation, refusalCode(op()), "%s on a member above the caller was not refused", name)
	}

	// The counterweight: what the deputy DOES hold, it may grant — including the viewer
	// role, which it holds only through the baseline every member has.
	_, err = e.s.SetMemberRoles(deputy, "pat@acme.example", []string{iam.ViewerRoleToken, "member-admin"})
	require.NoError(t, err)
	_, err = e.s.CreateTenantRole(deputy, TenantRoleInput{Token: "helpdesk", Authorities: []string{"member:read", "device:read"}})
	require.NoError(t, err)
}

// What the caller holds is its token's authorities INTERSECTED with its membership as
// stored now: a token minted before a demotion grants nothing the demotion took away.
func TestAStaleTokenCannotGrantWhatTheStoreNoLongerHolds(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "deputy@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "pat@acme.example", "acme", []string{iam.ViewerRoleToken})
	stale := as("deputy@acme.example", "acme", "*")

	_, err := e.s.SetMembershipRoles(context.Background(), "deputy@acme.example", "acme", []string{"member-admin"})
	require.NoError(t, err)

	_, err = e.s.SetMemberRoles(stale, "pat@acme.example", []string{"operator"})
	require.Equal(t, CodeEscalation, refusalCode(err), "a demoted administrator's still-valid token granted "+
		"an authority its membership no longer holds: %v", err)
	_, err = e.s.SetMemberAccess(stale, "pat@acme.example", []string{"operator"}, nil)
	require.Equal(t, CodeEscalation, refusalCode(err), "setMemberAccess: a demoted administrator's still-valid token granted "+
		"an authority its membership no longer holds: %v", err)
}

// ---- self-modification, last admin, IdP-managed --------------------------------------------

func TestNobodyChangesTheirOwnMembership(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "other@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	me := as("boss@acme.example", "acme", "*")
	for name, op := range map[string]func() error{
		"roles": func() error {
			_, err := e.s.SetMemberRoles(me, "boss@acme.example", []string{iam.TenantAdminRoleToken})
			return err
		},
		"scope": func() error { _, err := e.s.SetMemberScope(me, "boss@acme.example", nil); return err },
		"access": func() error {
			_, err := e.s.SetMemberAccess(me, "boss@acme.example", []string{iam.TenantAdminRoleToken}, nil)
			return err
		},
		"disable": func() error { _, err := e.s.SetMemberEnabled(me, "boss@acme.example", false); return err },
		"remove":  func() error { _, err := e.s.RemoveMember(me, "boss@acme.example"); return err },
	} {
		require.Equalf(t, CodeSelfModification, refusalCode(op()), "%s on the caller's own membership was not refused", name)
	}
}

func customers(tokens ...string) []ScopeAnchorInput {
	out := make([]ScopeAnchorInput, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, ScopeAnchorInput{Kind: auth.AnchorKindCustomer, Token: t})
	}
	return out
}

// The last enabled, unscoped member able to manage members cannot be removed, disabled,
// scoped or stripped — by a superuser acting on the tenant plane either, and not through a
// role edit. Whoever is left must be able to manage the tenant.
func TestLastAdminCannotBeRemovedScopedOrStripped(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "pat@acme.example", "acme", []string{iam.ViewerRoleToken})
	// A superuser with no membership, so self-modification cannot be what refuses.
	_, err := e.s.CreateIdentity(context.Background(), CreateIdentityInput{Email: "root@op.example", Password: "pw", Enabled: true})
	require.NoError(t, err)
	su := &iam.Role{Scope: iam.ScopeSystem, Token: iam.SuperuserRoleToken, Authorities: []string{"*"}}
	require.NoError(t, e.s.iam.CreateRole(context.Background(), su))
	_, err = e.s.SetSystemRoles(context.Background(), "root@op.example", []string{iam.SuperuserRoleToken})
	require.NoError(t, err)
	root := auth.WithClaims(context.Background(), &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme",
		Username: "root@op.example", Authorities: []string{"*"}, ActingAsSuperuser: true})

	for name, op := range map[string]func() error{
		"strip": func() error {
			_, err := e.s.SetMemberRoles(root, "boss@acme.example", []string{iam.ViewerRoleToken})
			return err
		},
		"scope": func() error { _, err := e.s.SetMemberScope(root, "boss@acme.example", customers("c1")); return err },
		// The combined change is decided on both halves at once: either one alone leaves no
		// administrator, so each is refused through it too.
		"access strip": func() error {
			_, err := e.s.SetMemberAccess(root, "boss@acme.example", []string{iam.ViewerRoleToken}, nil)
			return err
		},
		"access scope": func() error {
			_, err := e.s.SetMemberAccess(root, "boss@acme.example", []string{iam.TenantAdminRoleToken}, customers("c1"))
			return err
		},
		"disable": func() error { _, err := e.s.SetMemberEnabled(root, "boss@acme.example", false); return err },
		"remove":  func() error { _, err := e.s.RemoveMember(root, "boss@acme.example"); return err },
	} {
		require.Equalf(t, CodeLastAdmin, refusalCode(op()), "%s left the tenant with no administrator", name)
	}

	// The counterweight: with a second administrator, each change goes through.
	e.member(t, "deputy@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	_, err = e.s.SetMemberScope(root, "boss@acme.example", customers("c1"))
	require.NoError(t, err)
}

// The rule holds through a role edit too: the role that makes the tenant's only
// administrator one may not lose member:write.
func TestLastAdminCannotBeStrippedThroughARoleEdit(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	boss := as("boss@acme.example", "acme", "*")
	_, err := e.s.CreateTenantRole(boss, TenantRoleInput{Token: "admins", Authorities: []string{"member:write", "member:read", "role:write"}})
	require.NoError(t, err)
	e.member(t, "ann@acme.example", "acme", []string{"admins"})
	// The operator's plane is not bound by the rule: it demotes boss, leaving ann — through
	// the tenant role — as the only administrator.
	_, err = e.s.SetMembershipRoles(context.Background(), "boss@acme.example", "acme", []string{iam.ViewerRoleToken})
	require.NoError(t, err)

	ann := as("ann@acme.example", "acme", "member:write", "member:read", "role:write")
	_, err = e.s.UpdateTenantRole(ann, "admins", &RoleUpdateRequest{
		Authorities: dcgraphql.OptionalStringListOf([]string{"member:read", "role:write"})})
	require.Equal(t, CodeLastAdmin, refusalCode(err), "a role edit stripped the tenant's last administrator: %v", err)

	// The counterweight: an edit that keeps member:write goes through.
	_, err = e.s.UpdateTenantRole(ann, "admins", &RoleUpdateRequest{
		Authorities: dcgraphql.OptionalStringListOf([]string{"member:write", "role:write"})})
	require.NoError(t, err)
}

func TestManagedMembershipIsReadOnlyOnTenantPlane(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "idp@acme.example", "acme", []string{iam.ViewerRoleToken})
	conn := uint(7)
	require.NoError(t, e.db.Exec(`UPDATE iam_memberships SET managed_by = ? WHERE identity_id = (SELECT id FROM iam_identities WHERE email = ?)`,
		conn, "idp@acme.example").Error)
	me := as("boss@acme.example", "acme", "*")
	for name, op := range map[string]func() error{
		"roles": func() error { _, err := e.s.SetMemberRoles(me, "idp@acme.example", nil); return err },
		"scope": func() error { _, err := e.s.SetMemberScope(me, "idp@acme.example", customers("c1")); return err },
		"access": func() error {
			_, err := e.s.SetMemberAccess(me, "idp@acme.example", []string{iam.ViewerRoleToken}, customers("c1"))
			return err
		},
		"disable": func() error { _, err := e.s.SetMemberEnabled(me, "idp@acme.example", false); return err },
		"remove":  func() error { _, err := e.s.RemoveMember(me, "idp@acme.example"); return err },
	} {
		require.Equalf(t, CodeManagedByIdp, refusalCode(op()), "%s changed an IdP-managed membership", name)
	}
}

// ---- cross-tenant -------------------------------------------------------------------------

// A member of ANOTHER tenant is NOT_FOUND here — the same answer as an email with no
// account at all — and cannot be changed from here.
func TestAMemberElsewhereIsNotFound(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "gil@globex.example", "globex", []string{iam.ViewerRoleToken})
	me := as("boss@acme.example", "acme", "*")

	_, err := e.s.Member(me, "gil@globex.example")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, errNone := e.s.Member(me, "nobody@nowhere.example")
	require.ErrorIs(t, errNone, gorm.ErrRecordNotFound)
	require.Equal(t, errNone.Error(), err.Error(), "a member elsewhere is told apart from no account at all")

	_, err = e.s.SetMemberEnabled(me, "gil@globex.example", false)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = e.s.SetMemberAccess(me, "gil@globex.example", []string{iam.TenantAdminRoleToken}, nil)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	mem, err := e.s.iam.MembershipByIdentityTenant(context.Background(), mustID(t, e, "gil@globex.example"), "globex")
	require.NoError(t, err)
	require.True(t, mem.Enabled, "acme's administrator disabled a globex membership")
	require.Len(t, mem.TenantRoles, 1, "acme's administrator changed a globex membership's roles")
	require.Equal(t, iam.ViewerRoleToken, mem.TenantRoles[0].Token, "acme's administrator changed a globex membership's roles")

	page, _, err := e.s.Members(me, iam.MemberSearchCriteria{})
	require.NoError(t, err)
	for _, m := range page {
		require.Equal(t, "acme", m.Membership.TenantId)
	}
}

func mustID(t *testing.T, e *planeEnv, email string) uint {
	t.Helper()
	id, err := e.s.iam.IdentityByEmail(context.Background(), email)
	require.NoError(t, err)
	return id.ID
}

// ---- scope ---------------------------------------------------------------------------------

func TestScopeIsValidatedNotTruncated(t *testing.T) {
	// A group anchor is accepted, beside a customer one, in the stored form.
	got, err := NormalizeScope([]ScopeAnchorInput{{Kind: auth.AnchorKindGroup, Token: "north"},
		{Kind: auth.AnchorKindCustomer, Token: "acme"}, {Kind: auth.AnchorKindGroup, Token: "north"}})
	require.NoError(t, err)
	require.Equal(t, []string{"group:north", "customer:acme"}, got)

	_, err = NormalizeScope([]ScopeAnchorInput{{Kind: "area", Token: "north"}})
	cls, ok := integrity.Refused(err)
	require.True(t, ok && cls == integrity.ClassInvalid, "an unknown anchor kind: %v", err)

	many := make([]ScopeAnchorInput, 0, auth.MaxScopeAnchors+1)
	for i := 0; i <= auth.MaxScopeAnchors; i++ {
		many = append(many, ScopeAnchorInput{Kind: auth.AnchorKindCustomer, Token: fmt.Sprintf("c%d", i)})
	}
	_, err = NormalizeScope(many)
	_, isLimit := limit.As(err)
	require.True(t, isLimit, "17 anchors must be refused with LIMIT_EXCEEDED, never truncated: %v", err)

	got, err = NormalizeScope(many[:auth.MaxScopeAnchors])
	require.NoError(t, err)
	require.Len(t, got, auth.MaxScopeAnchors)

	// Duplicates collapse; an empty list is the whole tenant.
	got, err = NormalizeScope(customers("a", "a"))
	require.NoError(t, err)
	require.Equal(t, []string{"customer:a"}, got)
	got, err = NormalizeScope(nil)
	require.NoError(t, err)
	require.Nil(t, got)
}

// ---- revocation (ADR-081 decision 8, as amended 2026-10-16) ----------------------------------

// No membership change rotates the person's session epoch, narrowing included. A narrowing
// takes effect at the person's next token exchange, which re-reads the membership (pinned
// by value in identity's TestANarrowedMembersNextRefreshCarriesTheNarrowedScope); the epoch
// is per IDENTITY, so rotating it would sign the person out of every OTHER tenant too.
// Pinned for every route of the tenant plane, narrowing and widening alike.
func TestNoMembershipChangeRotatesTheEpoch(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "pat@acme.example", "acme", []string{iam.ViewerRoleToken, "operator"})
	me := as("boss@acme.example", "acme", "*")

	steps := []struct {
		name string
		op   func() error
	}{
		{"scope set from whole tenant", func() error { _, err := e.s.SetMemberScope(me, "pat@acme.example", customers("a", "b")); return err }},
		{"scope widened", func() error {
			_, err := e.s.SetMemberScope(me, "pat@acme.example", customers("a", "b", "c"))
			return err
		}},
		{"scope narrowed", func() error { _, err := e.s.SetMemberScope(me, "pat@acme.example", customers("a")); return err }},
		{"scope cleared (whole tenant)", func() error { _, err := e.s.SetMemberScope(me, "pat@acme.example", nil); return err }},
		{"role added", func() error {
			_, err := e.s.SetMemberRoles(me, "pat@acme.example", []string{iam.ViewerRoleToken, "operator", "member-admin"})
			return err
		}},
		{"role removed", func() error {
			_, err := e.s.SetMemberRoles(me, "pat@acme.example", []string{iam.ViewerRoleToken, "member-admin"})
			return err
		}},
		{"disabled", func() error { _, err := e.s.SetMemberEnabled(me, "pat@acme.example", false); return err }},
		{"enabled", func() error { _, err := e.s.SetMemberEnabled(me, "pat@acme.example", true); return err }},
		{"removed", func() error { _, err := e.s.RemoveMember(me, "pat@acme.example"); return err }},
	}
	for _, st := range steps {
		before := e.epoch(t, "pat@acme.example")
		require.NoErrorf(t, st.op(), st.name)
		require.Equalf(t, before, e.epoch(t, "pat@acme.example"),
			"%s rotated the session epoch, signing the person out of every tenant they belong to", st.name)
	}

	// The admin plane applies the same rule.
	e.member(t, "pat@acme.example", "acme", []string{iam.ViewerRoleToken, "operator"})
	before := e.epoch(t, "pat@acme.example")
	_, err := e.s.SetMembershipScope(context.Background(), "pat@acme.example", "acme", customers("a"))
	require.NoError(t, err)
	_, err = e.s.SetMembershipRoles(context.Background(), "pat@acme.example", "acme", []string{iam.ViewerRoleToken})
	require.NoError(t, err)
	_, err = e.s.SetMembershipEnabled(context.Background(), "pat@acme.example", "acme", false)
	require.NoError(t, err)
	_, err = e.s.RemoveMembership(context.Background(), "pat@acme.example", "acme")
	require.NoError(t, err)
	require.Equal(t, before, e.epoch(t, "pat@acme.example"), "an admin-plane narrowing rotated the session epoch")
}

// Removing an authority from a tenant role does not rotate the epoch of its holders: they
// pick the narrower role up at their next token exchange.
func TestNarrowingATenantRoleLeavesItsHoldersSignedIn(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	me := as("boss@acme.example", "acme", "*")
	_, err := e.s.CreateTenantRole(me, TenantRoleInput{Token: "tech", Authorities: []string{"command:write", "alarm:write"}})
	require.NoError(t, err)
	e.member(t, "pat@acme.example", "acme", []string{"tech"})
	e.member(t, "sam@acme.example", "acme", []string{iam.ViewerRoleToken})

	pat, sam := e.epoch(t, "pat@acme.example"), e.epoch(t, "sam@acme.example")
	_, err = e.s.UpdateTenantRole(me, "tech", &RoleUpdateRequest{Name: dcgraphql.OptionalStringOf("Technician")})
	require.NoError(t, err)
	require.Equal(t, pat, e.epoch(t, "pat@acme.example"), "a rename signed the role's holders out")

	_, err = e.s.UpdateTenantRole(me, "tech", &RoleUpdateRequest{Authorities: dcgraphql.OptionalStringListOf([]string{"command:write"})})
	require.NoError(t, err)
	require.Equal(t, pat, e.epoch(t, "pat@acme.example"), "removing an authority from a role signed its holder out of every tenant")
	require.Equal(t, sam, e.epoch(t, "sam@acme.example"), "a member NOT holding the role was signed out")
	r, err := e.s.iam.TenantOwnedRole(context.Background(), "acme", "tech")
	require.NoError(t, err)
	require.Equal(t, []string{"command:write"}, r.Authorities, "the narrowing itself was not applied")
}

// ---- roles in use ------------------------------------------------------------------------

func TestATenantRoleInUseCannotBeDeleted(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	me := as("boss@acme.example", "acme", "*")
	_, err := e.s.CreateTenantRole(me, TenantRoleInput{Token: "tech", Authorities: []string{"command:write"}})
	require.NoError(t, err)
	e.member(t, "pat@acme.example", "acme", []string{"tech"})

	_, err = e.s.DeleteTenantRole(me, "tech")
	cls, ok := integrity.Refused(err)
	require.True(t, ok && cls == integrity.ClassReference, "deleted a role a member holds: %v", err)

	_, err = e.s.SetMemberRoles(me, "pat@acme.example", nil)
	require.NoError(t, err)
	ok, err = e.s.DeleteTenantRole(me, "tech")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = e.s.DeleteTenantRole(me, "tech")
	require.NoError(t, err)
	require.False(t, ok, "a second delete of the same role reported deleting one")
}

// ---- invitations -------------------------------------------------------------------------

func TestInviteCreatesADisabledMembershipAndAnOpenInvitation(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	me := as("boss@acme.example", "acme", "*")

	m, code, err := e.s.InviteMember(me, InviteInput{Email: "New.Person@Acme.Example", RoleTokens: []string{iam.ViewerRoleToken}, Scope: customers("acme-corp")})
	require.NoError(t, err)
	require.NotEmpty(t, code)
	require.Equal(t, "new.person@acme.example", m.Identity.Email)
	require.False(t, m.Membership.Enabled)
	require.True(t, m.InvitationPending)
	require.Equal(t, []string{"customer:acme-corp"}, m.Membership.Scope)

	// The row keeps the digest, never the code.
	var rows []iam.Invitation
	require.NoError(t, e.db.WithContext(core.WithSystemContext(context.Background())).Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, InvitationDigest(code), rows[0].TokenDigest)
	require.NotContains(t, string(rows[0].TokenDigest), code)
	require.WithinDuration(t, time.Now().Add(InvitationTTL), rows[0].ExpiresAt, time.Minute)

	// A second invitation of a member is a CONFLICT; enabling a pending one is refused.
	_, _, err = e.s.InviteMember(me, InviteInput{Email: "new.person@acme.example"})
	require.True(t, conflict.Is(err), "got %v", err)
	_, err = e.s.SetMemberEnabled(me, "new.person@acme.example", true)
	require.ErrorIs(t, err, ErrInvitationPending)

	// Reissue revokes the old code; revoke closes the new one.
	_, code2, err := e.s.ReissueInvitation(me, "new.person@acme.example")
	require.NoError(t, err)
	require.NotEqual(t, code, code2)
	old, err := e.s.iam.InvitationByDigest(context.Background(), InvitationDigest(code))
	require.NoError(t, err)
	require.Equal(t, iam.InvitationRevoked, old.State(time.Now()))
	_, err = e.s.RevokeInvitation(me, "new.person@acme.example")
	require.NoError(t, err)
	_, err = e.s.RevokeInvitation(me, "new.person@acme.example")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "revoking with nothing open must say so")
}

// Even a holder of "*" may define only TENANT authorities: "*" means everything at the
// bearer's tier, and a tenant role carrying a system authority would be the inert lie the
// tiers exist to make unwritable.
func TestAStarHolderCannotDefineASystemAuthority(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	_, err := e.s.CreateTenantRole(as("boss@acme.example", "acme", "*"), TenantRoleInput{
		Token: "x", Authorities: []string{"tenant:write"}})
	require.Equal(t, CodeEscalation, refusalCode(err), "a tenant role was given a system authority: %v", err)
}

// The other half of the intersection: a token that grants LESS than the membership — an
// OAuth token capped to a scope, say — grants only what it carries, whatever the
// membership holds.
func TestATokenGrantsNoMoreThanItCarries(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "pat@acme.example", "acme", []string{iam.ViewerRoleToken})
	capped := as("boss@acme.example", "acme", "member:write", "member:read")
	_, err := e.s.SetMemberRoles(capped, "pat@acme.example", []string{"operator"})
	require.Equal(t, CodeEscalation, refusalCode(err), "a capped token granted what only its membership holds: %v", err)
}

// The service refuses a scoped caller itself, not only behind the resolvers' Authorize: a
// member who sees part of the tenant never administers the rest of it.
func TestTheServiceRefusesAScopedCaller(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	scoped := auth.WithClaims(context.Background(), &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme",
		Username: "boss@acme.example", Authorities: []string{"*"}, ScopeAnchors: []string{"customer:c1"}})
	_, err := e.s.TenantRoles(scoped)
	require.ErrorIs(t, err, auth.ErrScopeUnsupported)
	_, _, err = e.s.InviteMember(scoped, InviteInput{Email: "x@acme.example"})
	require.ErrorIs(t, err, auth.ErrScopeUnsupported)
}

// The admin plane's role operations are the operator's catalogue: they cannot reach a role
// a tenant owns, even by its exact token.
func TestTheAdminPlaneCannotEditATenantsOwnRole(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	_, err := e.s.CreateTenantRole(as("boss@acme.example", "acme", "*"), TenantRoleInput{Token: "tech", Authorities: []string{"device:read"}})
	require.NoError(t, err)
	_, err = e.s.UpdateRole(context.Background(), "tenant", "tech", &RoleUpdateRequest{Name: dcgraphql.OptionalStringOf("hijacked")})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	roles, err := e.s.ListRoles(context.Background(), nil)
	require.NoError(t, err)
	for _, r := range roles {
		require.NotEqual(t, "tech", r.Token, "the operator's catalogue lists a tenant's own role")
	}
}

// A tenant being deleted takes no administration: its row exists only to hold the token.
func TestAPurgingTenantRefusesAdministration(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "pat@acme.example", "acme", []string{iam.ViewerRoleToken})
	require.NoError(t, e.db.WithContext(core.WithSystemContext(context.Background())).Model(&iam.Tenant{}).
		Where("token = ?", "acme").Update("purge_state", iam.PurgePurging).Error)
	boss := as("boss@acme.example", "acme", "*")
	_, _, err := e.s.InviteMember(boss, InviteInput{Email: "x@acme.example"})
	require.ErrorIs(t, err, ErrTenantDeleted)
	// Every change to a member goes through the same refusal, the combined one included.
	for name, op := range map[string]func() error{
		"roles": func() error { _, err := e.s.SetMemberRoles(boss, "pat@acme.example", []string{"operator"}); return err },
		"scope": func() error { _, err := e.s.SetMemberScope(boss, "pat@acme.example", customers("c1")); return err },
		"access": func() error {
			_, err := e.s.SetMemberAccess(boss, "pat@acme.example", []string{"operator"}, customers("c1"))
			return err
		},
	} {
		require.ErrorIsf(t, op(), ErrTenantDeleted, "%s changed a member of a tenant being deleted", name)
	}
}

// The email-prefix filter matches literally: "_" and "%" in the prefix are not wildcards.
func TestTheMemberFilterMatchesThePrefixLiterally(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "a_b@acme.example", "acme", nil)
	e.member(t, "axb@acme.example", "acme", nil)
	page, pag, err := e.s.Members(as("boss@acme.example", "acme", "*"), iam.MemberSearchCriteria{EmailPrefix: "A_"})
	require.NoError(t, err)
	require.Len(t, page, 1, "an underscore in the prefix matched as a wildcard")
	require.Equal(t, "a_b@acme.example", page[0].Identity.Email)
	require.Equal(t, int32(1), pag.TotalRecords)
}

// The member list filters by role, by scope anchor and by who manages the membership, in the
// store and combined with AND, and never reaches into another tenant: the same person's
// membership elsewhere, with the same role and scope, is not matched.
func TestTheMemberFiltersByRoleScopeAndManager(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "ann@acme.example", "acme", []string{iam.ViewerRoleToken}, "c1")
	e.member(t, "bob@acme.example", "acme", []string{"operator"}, "c2")
	e.member(t, "cy@acme.example", "acme", []string{iam.ViewerRoleToken})
	e.member(t, "ann@acme.example", "globex", []string{"operator"}, "c1")
	ctx := context.Background()
	ann, err := e.s.iam.IdentityByEmail(ctx, "ann@acme.example")
	require.NoError(t, err)
	require.NoError(t, e.s.iam.MarkMembershipsManagedBy(ctx, ann.ID, []string{"acme"}, 1))

	caller := as("boss@acme.example", "acme", "*")
	emails := func(c iam.MemberSearchCriteria) []string {
		t.Helper()
		c.PageNumber, c.PageSize = 1, 50
		page, pag, err := e.s.Members(caller, c)
		require.NoError(t, err)
		out := []string{}
		for _, m := range page {
			out = append(out, m.Identity.Email)
		}
		require.Equal(t, int32(len(out)), pag.TotalRecords, "the total counts the filtered set")
		return out
	}
	yes, no := true, false

	require.Equal(t, []string{"ann@acme.example", "cy@acme.example"}, emails(iam.MemberSearchCriteria{RoleToken: iam.ViewerRoleToken}))
	require.Equal(t, []string{"bob@acme.example"}, emails(iam.MemberSearchCriteria{RoleToken: "operator"}),
		"ann's operator role in globex matched in acme")
	require.Equal(t, []string{"ann@acme.example"}, emails(iam.MemberSearchCriteria{ScopeAnchor: "customer:c1"}))
	require.Empty(t, emails(iam.MemberSearchCriteria{ScopeAnchor: "group:c1"}), "a customer anchor matched as a group")
	require.Empty(t, emails(iam.MemberSearchCriteria{ScopeAnchor: "customer:c"}), "an anchor matched as a prefix")
	require.Equal(t, []string{"ann@acme.example"}, emails(iam.MemberSearchCriteria{ManagedByIdp: &yes}))
	require.Equal(t, []string{"bob@acme.example", "boss@acme.example", "cy@acme.example"},
		emails(iam.MemberSearchCriteria{ManagedByIdp: &no}))
	require.Equal(t, []string{"ann@acme.example"},
		emails(iam.MemberSearchCriteria{RoleToken: iam.ViewerRoleToken, ScopeAnchor: "customer:c1"}), "filters combine with AND")
	require.Empty(t, emails(iam.MemberSearchCriteria{RoleToken: "operator", ScopeAnchor: "customer:c1"}))
}

// A member whose membership the operator has scoped holds nothing on this plane even while
// a token minted before the scoping still says otherwise.
func TestAMembershipScopedSinceTheTokenWasMintedHoldsNothing(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "deputy@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	_, err := e.s.SetMembershipScope(context.Background(), "deputy@acme.example", "acme", customers("c1"))
	require.NoError(t, err)
	stale := as("deputy@acme.example", "acme", "*") // minted unscoped, before the scoping
	_, _, err = e.s.InviteMember(stale, InviteInput{Email: "x@acme.example"})
	require.ErrorIs(t, err, auth.ErrForbidden, "a now-scoped member administered the tenant on a stale token")
}

// One tenant cannot reach another's own role by its token: not to edit it, not to delete it.
func TestATenantCannotReachAnotherTenantsRole(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "boss@globex.example", "globex", []string{iam.TenantAdminRoleToken})
	_, err := e.s.CreateTenantRole(as("boss@acme.example", "acme", "*"), TenantRoleInput{Token: "tech", Authorities: []string{"device:read"}})
	require.NoError(t, err)

	globex := as("boss@globex.example", "globex", "*")
	_, err = e.s.UpdateTenantRole(globex, "tech", &RoleUpdateRequest{Name: dcgraphql.OptionalStringOf("hijacked")})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "globex edited acme's own role")
	deleted, err := e.s.DeleteTenantRole(globex, "tech")
	require.NoError(t, err)
	require.False(t, deleted, "globex deleted acme's own role")

	acme, err := e.s.iam.TenantOwnedRole(context.Background(), "acme", "tech")
	require.NoError(t, err)
	require.False(t, acme.Name.Valid, "acme's role was changed from globex")
}

// 🔴 ONE TENANT'S ADMINISTRATOR CANNOT SIGN A PERSON OUT OF ANOTHER TENANT. The session
// epoch is per IDENTITY, so a change in one tenant that rotated it would sign the person out
// of EVERY tenant they belong to. The sharpest form of that was a pending invitation: an
// administrator of one tenant could invite any address on the instance and narrow or remove
// the pending member, repeatedly, with no acceptance needed. No membership change rotates
// the epoch now (ADR-081 decision 8, as amended 2026-10-16), so this holds trivially; it is
// kept as the regression guard for the route that made the rule matter.
func TestAPendingMemberIsNotSignedOutOfTheirOtherTenants(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	// The victim is a member of globex only.
	e.member(t, "victim@globex.example", "globex", []string{iam.ViewerRoleToken})
	me := as("boss@acme.example", "acme", "*")

	steps := []struct {
		name string
		op   func() error
	}{
		{"scoped while pending", func() error { _, err := e.s.SetMemberScope(me, "victim@globex.example", customers("a")); return err }},
		{"stripped while pending", func() error { _, err := e.s.SetMemberRoles(me, "victim@globex.example", nil); return err }},
		{"removed while pending", func() error { _, err := e.s.RemoveMember(me, "victim@globex.example"); return err }},
	}
	for _, st := range steps {
		if _, err := e.s.Member(me, "victim@globex.example"); errors.Is(err, ErrMemberNotFound) {
			_, _, err := e.s.InviteMember(me, InviteInput{Email: "victim@globex.example", RoleTokens: []string{"operator"}})
			require.NoError(t, err)
		}
		before := e.epoch(t, "victim@globex.example")
		require.NoErrorf(t, st.op(), st.name)
		require.Equalf(t, before, e.epoch(t, "victim@globex.example"),
			"%s: a membership that admitted no session rotated the person's epoch, signing them out of globex", st.name)
	}

	// A disabled holder of a tenant role is not signed out when the role is narrowed either.
	_, err := e.s.CreateTenantRole(me, TenantRoleInput{Token: "tech", Authorities: []string{"command:write", "alarm:write"}})
	require.NoError(t, err)
	_, _, err = e.s.InviteMember(me, InviteInput{Email: "victim@globex.example", RoleTokens: []string{"tech"}})
	require.NoError(t, err)
	before := e.epoch(t, "victim@globex.example")
	_, err = e.s.UpdateTenantRole(me, "tech", &RoleUpdateRequest{Authorities: dcgraphql.OptionalStringListOf([]string{"alarm:write"})})
	require.NoError(t, err)
	require.Equal(t, before, e.epoch(t, "victim@globex.example"), "narrowing a role a pending member holds signed them out")

	// The admin plane applies the same rule.
	before = e.epoch(t, "victim@globex.example")
	_, err = e.s.SetMembershipRoles(context.Background(), "victim@globex.example", "acme", nil)
	require.NoError(t, err)
	_, err = e.s.SetMembershipScope(context.Background(), "victim@globex.example", "acme", customers("a"))
	require.NoError(t, err)
	_, err = e.s.RemoveMembership(context.Background(), "victim@globex.example", "acme")
	require.NoError(t, err)
	require.Equal(t, before, e.epoch(t, "victim@globex.example"), "the admin plane signed out a person whose membership was disabled")
}

// ---- roles and scope together --------------------------------------------------------------

// SetMemberAccess changes roles and scope as one: a refusal of either half leaves the other
// untouched, which two separate calls cannot promise — the roles would already be saved.
func TestSetMemberAccessIsAllOrNothing(t *testing.T) {
	e := newPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "deputy@acme.example", "acme", []string{"member-admin"})
	e.member(t, "pat@acme.example", "acme", []string{iam.ViewerRoleToken})
	deputy := as("deputy@acme.example", "acme", "member:read", "member:write", "role:read")
	root := as("boss@acme.example", "acme", "*")
	pat := func() ([]string, []string) {
		t.Helper()
		v, err := e.s.Member(root, "pat@acme.example")
		require.NoError(t, err)
		roles := []string{}
		for _, r := range v.Membership.TenantRoles {
			roles = append(roles, r.Token)
		}
		return roles, v.Membership.Scope
	}

	// A role the deputy may not grant: the scope half does not land either.
	_, err := e.s.SetMemberAccess(deputy, "pat@acme.example", []string{"operator"}, customers("c1"))
	require.Equal(t, CodeEscalation, refusalCode(err), "got %v", err)
	roles, scope := pat()
	require.Equal(t, []string{iam.ViewerRoleToken}, roles)
	require.Empty(t, scope)

	// A scope that is refused: the role half does not land either.
	tooMany := make([]ScopeAnchorInput, 0, auth.MaxScopeAnchors+1)
	for i := 0; i <= auth.MaxScopeAnchors; i++ {
		tooMany = append(tooMany, ScopeAnchorInput{Kind: auth.AnchorKindCustomer, Token: fmt.Sprintf("c%d", i)})
	}
	_, err = e.s.SetMemberAccess(deputy, "pat@acme.example", []string{iam.ViewerRoleToken, "member-admin"}, tooMany)
	require.Error(t, err)
	roles, scope = pat()
	require.Equal(t, []string{iam.ViewerRoleToken}, roles)
	require.Empty(t, scope)

	// The counterweight: an allowed change lands both halves.
	_, err = e.s.SetMemberAccess(deputy, "pat@acme.example", []string{iam.ViewerRoleToken, "member-admin"}, customers("c1"))
	require.NoError(t, err)
	roles, scope = pat()
	require.ElementsMatch(t, []string{iam.ViewerRoleToken, "member-admin"}, roles)
	require.Equal(t, []string{"customer:c1"}, scope)

	// The shared checks apply: nobody changes their own membership, or a member above them.
	_, err = e.s.SetMemberAccess(deputy, "deputy@acme.example", nil, nil)
	require.Equal(t, CodeSelfModification, refusalCode(err), "got %v", err)
	_, err = e.s.SetMemberAccess(deputy, "boss@acme.example", nil, nil)
	require.Equal(t, CodeEscalation, refusalCode(err), "got %v", err)
}
