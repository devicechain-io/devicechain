// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The member filters and the combined roles-and-scope change on a real PostgreSQL, through
// the real migration chain. The scope filter has a Postgres-only branch (jsonb @>) that the
// SQLite suite never runs, and the combined change's all-or-nothing promise is a property of
// the server's transaction, which SQLite only approximates.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 -run Postgres ./admin/ -v
package admin

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/internal/concurrencytest"
	"github.com/devicechain-io/dc-user-management/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newPostgresPlaneEnv(t *testing.T) *planeEnv {
	t.Helper()
	port := 5432
	if raw := os.Getenv("DC_IT_PGPORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		require.NoError(t, err)
		port = p
	}
	host := "localhost"
	if h := os.Getenv("DC_IT_PGHOST"); h != "" {
		host = h
	}
	ctx := context.Background()
	instance := fmt.Sprintf("umfilters%d", time.Now().UnixNano())
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
	mgr := &rdb.RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "user-management"},
		Migrations:   schema.Migrations,
		InstanceConfig: config.DatastoreConfiguration{Type: "postgres", Configuration: map[string]interface{}{
			"hostname": host, "port": port, "username": "postgres", "password": "postgres", "sslMode": "disable",
		}},
	}
	require.NoError(t, mgr.ExecuteInitialize(ctx))
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	s := NewService(iam.NewStore(mgr), 0, 0, nil)
	s.SetViewerBaseline(viewerBaselineForTests)
	e := &planeEnv{s: s, db: mgr.Database}
	tier := &iam.TenantTier{Token: "it-silver"}
	require.NoError(t, s.iam.CreateTenantTier(ctx, tier))
	for _, tok := range []string{"acme", "globex"} {
		require.NoError(t, s.iam.CreateTenant(ctx, &iam.Tenant{Token: tok, Enabled: true, TierID: tier.ID, PurgeState: iam.PurgeActive}))
	}
	for _, r := range []iam.Role{
		{Scope: iam.ScopeTenant, Token: iam.TenantAdminRoleToken, Authorities: []string{"*"}},
		{Scope: iam.ScopeTenant, Token: iam.ViewerRoleToken, Authorities: viewerBaselineForTests},
		{Scope: iam.ScopeTenant, Token: "member-admin", Authorities: []string{"member:read", "member:write", "role:read"}},
		{Scope: iam.ScopeTenant, Token: "operator", Authorities: []string{"alarm:write", "command:write"}},
	} {
		r := r
		require.NoError(t, s.iam.UpsertCatalogueRole(ctx, &r))
	}
	// A role globex owns: its token means nothing in acme.
	globex := "globex"
	require.NoError(t, s.iam.CreateRole(ctx, &iam.Role{Scope: iam.ScopeTenant, Token: "special", TenantID: &globex,
		Authorities: []string{"alarm:write"}}))
	return e
}

func TestPostgresTwoAdminsRemovingEachOtherLeaveOne(t *testing.T) {
	for _, unlocked := range []bool{false, true} {
		for round := 0; round < 5; round++ {
			t.Run(fmt.Sprintf("without_tenant_lock=%t/round=%d", unlocked, round), func(t *testing.T) {
				e := newPostgresPlaneEnv(t)
				e.member(t, "a@example.com", "acme", []string{iam.TenantAdminRoleToken})
				e.member(t, "b@example.com", "acme", []string{iam.TenantAdminRoleToken})
				// Both retain operator authority after losing their tenant role. This
				// isolates LAST_ADMIN from the earlier no-escalation check, which
				// otherwise refuses the demoted caller before reaching that rule.
				require.NoError(t, e.s.iam.UpsertCatalogueRole(context.Background(), &iam.Role{
					Scope: iam.ScopeSystem, Token: iam.SuperuserRoleToken, Authorities: []string{"*"},
				}))
				for _, email := range []string{"a@example.com", "b@example.com"} {
					_, err := e.s.SetSystemRoles(context.Background(), email, []string{iam.SuperuserRoleToken})
					require.NoError(t, err)
				}
				release := concurrencytest.Park(t, e.db, false, func(tx *gorm.DB) bool {
					_, ok := tx.Statement.Dest.(*[]iam.Membership)
					return ok && strings.Contains(tx.Statement.SQL.String(), "WHERE tenant_id =")
				})
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				start, results := make(chan struct{}), make(chan error, 2)
				for _, pair := range [][2]string{{"a@example.com", "b@example.com"}, {"b@example.com", "a@example.com"}} {
					go func(pair [2]string) {
						<-start
						caller := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess,
							Tenant: "acme", Username: pair[0], Authorities: []string{"*"}, ActingAsSuperuser: true})
						_, err := e.s.SetMemberRoles(concurrencytest.Context(caller, unlocked), pair[1], []string{iam.ViewerRoleToken})
						results <- err
					}(pair)
				}
				close(start)
				release(2)
				wins, refused := 0, 0
				for i := 0; i < 2; i++ {
					err := <-results
					if err == nil {
						wins++
					} else {
						require.Equal(t, CodeLastAdmin, refusalCode(err), "%v", err)
						refused++
					}
				}
				members, err := e.s.iam.TenantMemberships(ctx, "acme")
				require.NoError(t, err)
				admins := 0
				for i := range members {
					if isAdmin(&members[i]) {
						admins++
					}
				}
				if unlocked {
					require.Equal(t, 2, wins, "negative control must expose write skew")
					require.Zero(t, admins)
				} else {
					require.Equal(t, 1, wins)
					require.Equal(t, 1, refused)
					require.Equal(t, 1, admins)
				}
			})
		}
	}
}

func TestPostgresConcurrentRotationsLeaveAtMostTwoLiveSecrets(t *testing.T) {
	for _, unlocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("without_tenant_and_account_locks=%t", unlocked), func(t *testing.T) {
			e := newPostgresPlaneEnv(t)
			e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
			key, err := credential.DeriveServiceAccountSecretKey([]byte("concurrent-secret-rotation-test-key"))
			require.NoError(t, err)
			e.s.SetServiceAccountSecretKey(key)
			_, _, err = e.s.CreateServiceAccount(boss(), ServiceAccountInput{Token: "ci"})
			require.NoError(t, err)
			release := concurrencytest.Park(t, e.db, false, func(tx *gorm.DB) bool {
				return tx.Statement.Table == "iam_service_accounts" && len(tx.Statement.Preloads) > 0
			})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			caller := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "boss@acme.example", Authorities: []string{"*"}})
			const n = 8
			start, results := make(chan struct{}), make(chan error, n)
			for i := 0; i < n; i++ {
				go func() {
					<-start
					overlap := int32(6)
					_, _, err := e.s.RotateServiceAccountSecret(concurrencytest.Context(caller, unlocked), "ci", &overlap, nil)
					results <- err
				}()
			}
			close(start)
			release(n)
			for i := 0; i < n; i++ {
				require.NoError(t, <-results)
			}
			acct, err := e.s.ServiceAccount(caller, "ci")
			require.NoError(t, err)
			require.Len(t, acct.Secrets, n+1, "every rotation must commit")
			live := 0
			for _, sec := range acct.Secrets {
				if sec.Live(time.Now()) {
					live++
				}
			}
			if unlocked {
				require.GreaterOrEqual(t, live, 3, "negative control must overshoot")
			} else {
				require.Equal(t, 2, live)
			}
		})
	}
}

func (e *planeEnv) groupMember(t *testing.T, email, tenant string, roles []string, anchors []ScopeAnchorInput) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.s.iam.IdentityByEmail(ctx, email); err != nil {
		_, err := e.s.CreateIdentity(ctx, CreateIdentityInput{Email: email, Password: "pw", Enabled: true})
		require.NoError(t, err)
	}
	_, err := e.s.AddScopedMembership(ctx, email, tenant, roles, anchors)
	require.NoError(t, err)
}

// The filters on the server that answers them in production: the jsonb containment matches
// an anchor exactly (kind and token, never a prefix, never another tenant's membership), the
// role filter matches the token exactly and only roles this tenant can hold, and the filters
// combine with AND, with the page total counting the filtered set.
func TestPostgresMemberFilters(t *testing.T) {
	e := newPostgresPlaneEnv(t)
	group := func(tok string) ScopeAnchorInput { return ScopeAnchorInput{Kind: auth.AnchorKindGroup, Token: tok} }
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.groupMember(t, "ann@acme.example", "acme", []string{iam.ViewerRoleToken}, append(customers("c1"), group("north")))
	e.member(t, "bob@acme.example", "acme", []string{"operator"}, "c2", "c10")
	e.member(t, "cy@acme.example", "acme", []string{iam.ViewerRoleToken})
	e.groupMember(t, "dee@acme.example", "acme", []string{iam.ViewerRoleToken}, []ScopeAnchorInput{group("c1")})
	e.member(t, "ann@acme.example", "globex", []string{"operator", "special"}, "c2")
	e.member(t, "gus@globex.example", "globex", []string{"special"}, "c1")
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

	require.Equal(t, []string{"ann@acme.example"}, emails(iam.MemberSearchCriteria{ScopeAnchor: "customer:c1"}),
		"gus is limited to c1 in globex, and dee to a GROUP named c1")
	require.Equal(t, []string{"dee@acme.example"}, emails(iam.MemberSearchCriteria{ScopeAnchor: "group:c1"}))
	require.Equal(t, []string{"ann@acme.example"}, emails(iam.MemberSearchCriteria{ScopeAnchor: "group:north"}))
	require.Equal(t, []string{"bob@acme.example"}, emails(iam.MemberSearchCriteria{ScopeAnchor: "customer:c2"}),
		"ann is limited to c2 in globex only")
	require.Empty(t, emails(iam.MemberSearchCriteria{ScopeAnchor: "customer:c"}), "an anchor matched as a prefix")
	require.Empty(t, emails(iam.MemberSearchCriteria{ScopeAnchor: "c1"}), "a bare token matched an anchor")
	require.Empty(t, emails(iam.MemberSearchCriteria{ScopeAnchor: `customer:c1"]`}), "a quote in the anchor broke out of the literal")

	require.Equal(t, []string{"ann@acme.example", "cy@acme.example", "dee@acme.example"},
		emails(iam.MemberSearchCriteria{RoleToken: iam.ViewerRoleToken}))
	require.Equal(t, []string{"bob@acme.example"}, emails(iam.MemberSearchCriteria{RoleToken: "operator"}),
		"ann's operator role in globex matched in acme")
	require.Empty(t, emails(iam.MemberSearchCriteria{RoleToken: "special"}), "globex's own role matched in acme")
	require.Empty(t, emails(iam.MemberSearchCriteria{RoleToken: "oper"}), "a role token matched as a prefix")

	require.Equal(t, []string{"ann@acme.example"}, emails(iam.MemberSearchCriteria{ManagedByIdp: &yes}))
	require.Equal(t, []string{"bob@acme.example", "boss@acme.example", "cy@acme.example", "dee@acme.example"},
		emails(iam.MemberSearchCriteria{ManagedByIdp: &no}))

	require.Equal(t, []string{"ann@acme.example"},
		emails(iam.MemberSearchCriteria{RoleToken: iam.ViewerRoleToken, ScopeAnchor: "customer:c1"}), "filters combine with AND")
	require.Empty(t, emails(iam.MemberSearchCriteria{RoleToken: "operator", ScopeAnchor: "customer:c1"}))
	require.Empty(t, emails(iam.MemberSearchCriteria{ScopeAnchor: "customer:c1", ManagedByIdp: &no}))

	// Paging a filtered set: the total is the filtered count on every page.
	page, pag, err := e.s.Members(caller, iam.MemberSearchCriteria{RoleToken: iam.ViewerRoleToken,
		Pagination: rdb.Pagination{PageNumber: 2, PageSize: 2}})
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, "dee@acme.example", page[0].Identity.Email)
	require.Equal(t, int32(3), pag.TotalRecords)

	// The control from globex's side: its own role and anchor find its own members.
	found, _, err := e.s.iam.TenantMembers(ctx, "globex", iam.MemberSearchCriteria{RoleToken: "special", ScopeAnchor: "customer:c1",
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, "gus@globex.example", found[0].Identity.Email)
}

// The combined change is all-or-nothing on Postgres too, including for a refusal decided
// AFTER both writes have run inside the transaction: the last-administrator rule.
func TestPostgresSetMemberAccessRollsBackAfterItsWrites(t *testing.T) {
	e := newPostgresPlaneEnv(t)
	e.member(t, "boss@acme.example", "acme", []string{iam.TenantAdminRoleToken})
	e.member(t, "pat@acme.example", "acme", []string{iam.ViewerRoleToken})
	_, err := e.s.CreateIdentity(context.Background(), CreateIdentityInput{Email: "root@op.example", Password: "pw", Enabled: true})
	require.NoError(t, err)
	su := &iam.Role{Scope: iam.ScopeSystem, Token: iam.SuperuserRoleToken, Authorities: []string{"*"}}
	require.NoError(t, e.s.iam.UpsertCatalogueRole(context.Background(), su))
	_, err = e.s.SetSystemRoles(context.Background(), "root@op.example", []string{iam.SuperuserRoleToken})
	require.NoError(t, err)
	root := auth.WithClaims(context.Background(), &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme",
		Username: "root@op.example", Authorities: []string{"*"}, ActingAsSuperuser: true})

	// Narrowing the only administrator to a customer leaves no administrator: refused, and
	// neither the new roles nor the new scope survive.
	_, err = e.s.SetMemberAccess(root, "boss@acme.example", []string{iam.TenantAdminRoleToken, iam.ViewerRoleToken}, customers("c1"))
	require.Equal(t, CodeLastAdmin, refusalCode(err), "got %v", err)
	v, err := e.s.Member(root, "boss@acme.example")
	require.NoError(t, err)
	require.Len(t, v.Membership.TenantRoles, 1, "the roles half of a refused change was kept")
	require.Empty(t, v.Membership.Scope, "the scope half of a refused change was kept")

	// The counterweight: the same change to a member who is not the last administrator lands.
	_, err = e.s.SetMemberAccess(root, "pat@acme.example", []string{iam.ViewerRoleToken, "operator"}, customers("c1"))
	require.NoError(t, err)
	v, err = e.s.Member(root, "pat@acme.example")
	require.NoError(t, err)
	require.Len(t, v.Membership.TenantRoles, 2)
	require.Equal(t, []string{"customer:c1"}, v.Membership.Scope)
}
