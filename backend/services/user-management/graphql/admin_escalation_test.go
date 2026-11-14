// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	gql "github.com/graph-gophers/graphql-go"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type adminEscalationEnv struct {
	store  *iam.Store
	svc    *admin.Service
	schema *gql.Schema
	mgr    *identity.Manager
	db     *gorm.DB
}

func newAdminEscalationEnv(t *testing.T) *adminEscalationEnv {
	t.Helper()
	db := putest.NewSQLiteDB(t, &iam.Role{}, &iam.Identity{}, &iam.Membership{}, &iam.Tenant{}, &iam.TenantTier{},
		&iam.MfaFactor{}, &iam.MfaSourceFailure{}, &iam.MfaRecoveryCode{}, &iam.IdentityLink{}, &iam.IdpConnection{},
		&iam.IdpConnectionTenant{}, &iam.IdpConnectionDomain{}, &iam.IdpRoleMapping{}, &iam.SsoLinkRequest{}, &iam.ServiceAccount{})
	rdbm := &rdb.RdbManager{Database: db}
	store := iam.NewStore(rdbm)
	svc := admin.NewService(store, 0, 0, nil)
	ctx := context.Background()
	for _, role := range []iam.Role{
		{Scope: iam.ScopeSystem, Token: "helpdesk", Authorities: []string{"user:read", "user:write"}},
		{Scope: iam.ScopeSystem, Token: "superuser", Authorities: []string{"*"}},
		{Scope: iam.ScopeSystem, Token: "roleadmin", Authorities: []string{"role:read", "role:write"}},
		{Scope: iam.ScopeSystem, Token: "powerful", Authorities: []string{"user:write"}},
		{Scope: iam.ScopeTenant, Token: "catalogue", Authorities: []string{"role:read"}},
		{Scope: iam.ScopeTenant, Token: "tenant-admin", Authorities: []string{"member:write", "device:write"}},
		{Scope: iam.ScopeTenant, Token: "viewer", Authorities: []string{"user:read"}},
	} {
		role := role
		require.NoError(t, store.CreateRole(ctx, &role))
	}
	for _, person := range []struct{ name, role string }{{"helen", "helpdesk"}, {"root", "superuser"}, {"rob", "roleadmin"}, {"plain", ""}} {
		var roles []string
		if person.role != "" {
			roles = []string{person.role}
		}
		_, err := svc.CreateIdentity(ctx, admin.CreateIdentityInput{Email: person.name + "@example.com", Password: "original", Enabled: true, SystemRoles: roles})
		require.NoError(t, err)
	}
	require.NoError(t, store.CreateTenant(ctx, &iam.Tenant{Token: "acme", Enabled: true}))
	// An enabled instance connection makes clearing root's password a legitimate operation.
	conn := &iam.IdpConnection{Token: "instance", Kind: iam.IdpKindInstance, Enabled: true}
	require.NoError(t, db.WithContext(core.WithSystemContext(ctx)).Create(conn).Error)
	mgr := identity.NewManager(nil, rdbm, nil, nil, 0, 0, "", identity.BootstrapConfig{}, nil)
	mgr.SetSessionStore(emptyAdminSessionStore{})
	return &adminEscalationEnv{store: store, svc: svc, db: db, schema: gql.MustParseSchema(AdminSchemaContent, &AdminResolver{}), mgr: mgr}
}

func (e *adminEscalationEnv) ctx(name string) context.Context {
	// Carry the actual delegated grants on an identity token.
	authorities := map[string][]string{"helen": {"user:read", "user:write"}, "rob": {"role:read", "role:write"}}[name]
	if name == "root" || name == "unknown" {
		authorities = []string{"*"}
	}
	ctx := auth.WithClaims(core.WithSystemContext(context.Background()), &auth.Claims{TokenType: auth.TokenTypeIdentity, Username: name + "@example.com", Authorities: authorities})
	ctx = context.WithValue(ctx, ContextAdminKey, e.svc)
	return context.WithValue(ctx, ContextAdminSsoKey, e.mgr)
}

func TestAdminIdentityNoEscalationOnTheWire(t *testing.T) {
	operations := []string{
		`setSystemRoles(email:"helen@example.com", roleTokens:["superuser"]) { email }`,
		`createIdentity(request:{email:"new@example.com",password:"password",enabled:true,systemRoles:["superuser"]}) { email }`,
		`setPassword(email:"root@example.com",password:"changed") { email }`,
		`clearPassword(email:"root@example.com") { email }`,
		`linkIdentity(email:"root@example.com",connection:"instance",subject:"new-sub")`,
		`unlinkIdentity(email:"root@example.com",connection:"instance")`,
		`setIdentityEnabled(email:"root@example.com",enabled:false) { email }`,
		`deleteIdentity(email:"root@example.com")`,
		`revokeSessions(email:"root@example.com")`,
		`setSystemRoles(email:"root@example.com",roleTokens:[]) { email }`,
	}
	for i, operation := range operations {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			e := newAdminEscalationEnv(t)
			if strings.HasPrefix(operation, "clearPassword") || strings.HasPrefix(operation, "unlinkIdentity") {
				root, err := e.store.IdentityByEmail(context.Background(), "root@example.com")
				require.NoError(t, err)
				var conn iam.IdpConnection
				require.NoError(t, e.db.WithContext(core.WithSystemContext(context.Background())).First(&conn).Error)
				require.NoError(t, e.store.CreateIdentityLink(context.Background(), &iam.IdentityLink{IdentityID: root.ID, ConnectionID: conn.ID, Subject: "root-sub"}))
			}
			before, err := e.store.IdentityByEmail(context.Background(), "root@example.com")
			require.NoError(t, err)
			response := e.schema.Exec(e.ctx("helen"), "mutation {"+operation+"}", "", nil)
			require.Len(t, response.Errors, 1)
			require.Equal(t, "ESCALATION", response.Errors[0].Extensions["code"])
			after, err := e.store.IdentityByEmail(context.Background(), "root@example.com")
			require.NoError(t, err)
			require.Equal(t, before.PasswordHash, after.PasswordHash)
			require.True(t, after.Enabled)
			require.Equal(t, before.SystemRoles, after.SystemRoles)
			require.Equal(t, before.SessionEpoch, after.SessionEpoch)
			response = e.schema.Exec(e.ctx("root"), "mutation {"+operation+"}", "", nil)
			require.Empty(t, response.Errors, "superuser counterweight: %s", operation)
		})
	}
}

func TestAdminDelegateLegitimateIdentityChanges(t *testing.T) {
	e := newAdminEscalationEnv(t)
	for _, operation := range []string{
		`setPassword(email:"plain@example.com",password:"changed") { email }`,
		`setIdentityEnabled(email:"plain@example.com",enabled:false) { email }`,
		`setSystemRoles(email:"plain@example.com",roleTokens:["helpdesk"]) { email }`,
		`createIdentity(request:{email:"new@example.com",password:"password",enabled:true,systemRoles:["helpdesk"]}) { email }`,
	} {
		require.Empty(t, e.schema.Exec(e.ctx("helen"), "mutation {"+operation+"}", "", nil).Errors)
	}
	plain, err := e.store.IdentityByEmail(context.Background(), "plain@example.com")
	require.NoError(t, err)
	require.False(t, plain.Enabled)
	require.Equal(t, "helpdesk", plain.SystemRoles[0].Token)
}

func TestAdminRoleNoEscalationOnTheWire(t *testing.T) {
	for i, operation := range []string{
		`updateRole(scope:"system",token:"roleadmin",request:{authorities:["*"]}) { token }`,
		`createRole(request:{scope:"system",token:"new-role",authorities:["*"]}) { token }`,
		`updateRole(scope:"tenant",token:"catalogue",request:{authorities:["device:write"]}) { token }`,
		`deleteRole(scope:"system",token:"powerful")`,
		`updateRole(scope:"system",token:"powerful",request:{authorities:[]}) { token }`,
		`createRole(request:{scope:"tenant",token:"new-role",authorities:["device:write"]}) { token }`,
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			e := newAdminEscalationEnv(t)
			response := e.schema.Exec(e.ctx("rob"), "mutation {"+operation+"}", "", nil)
			require.Len(t, response.Errors, 1)
			require.Equal(t, "ESCALATION", response.Errors[0].Extensions["code"])
			role, err := e.store.CatalogueRoleByScopeToken(context.Background(), iam.ScopeSystem, "roleadmin")
			require.NoError(t, err)
			require.Equal(t, []string{"role:read", "role:write"}, role.Authorities)
			require.Empty(t, e.schema.Exec(e.ctx("root"), "mutation {"+operation+"}", "", nil).Errors)
		})
	}
	e := newAdminEscalationEnv(t)
	response := e.schema.Exec(e.ctx("rob"), `mutation { updateRole(scope:"system",token:"roleadmin",request:{authorities:["role:read"]}) { authorities } }`, "", nil)
	require.Empty(t, response.Errors)
	role, err := e.store.CatalogueRoleByScopeToken(context.Background(), iam.ScopeSystem, "roleadmin")
	require.NoError(t, err)
	require.Equal(t, []string{"role:read"}, role.Authorities)
}

func TestAdminCallerMustExistAndBeReadLive(t *testing.T) {
	e := newAdminEscalationEnv(t)
	for _, ctx := range []context.Context{context.Background(), adminCtx("*"), e.ctx("unknown")} {
		require.ErrorIs(t, e.svc.CheckAdminAuthorities(ctx, nil), auth.ErrForbidden)
	}
	// The same token loses its ability to act above a plain identity after live demotion.
	ctx := e.ctx("root")
	_, err := e.svc.SetSystemRoles(context.Background(), "root@example.com", []string{"helpdesk"})
	require.NoError(t, err)
	response := e.schema.Exec(ctx, `mutation { setSystemRoles(email:"plain@example.com",roleTokens:["superuser"]) { email } }`, "", nil)
	require.Len(t, response.Errors, 1)
	require.Equal(t, "ESCALATION", response.Errors[0].Extensions["code"])
	require.NoError(t, e.svc.CheckAdminIdentity(ctx, "plain@example.com"))
}

func TestAdminSsoApprovalChecksTheCurrentIdentity(t *testing.T) {
	e := newAdminEscalationEnv(t)
	var conn iam.IdpConnection
	require.NoError(t, e.db.WithContext(core.WithSystemContext(context.Background())).First(&conn).Error)
	request := &iam.SsoLinkRequest{ConnectionID: conn.ID, Subject: "approved-sub", Email: "root@example.com"}
	require.NoError(t, e.store.UpsertLinkRequest(context.Background(), request))
	operation := fmt.Sprintf(`mutation { approveSsoLinkRequest(id:"%d") }`, request.ID)
	response := e.schema.Exec(e.ctx("helen"), operation, "", nil)
	require.Len(t, response.Errors, 1)
	require.Equal(t, "ESCALATION", response.Errors[0].Extensions["code"])
	_, err := e.store.SsoLinkRequestByID(context.Background(), request.ID)
	require.NoError(t, err)
	link, err := e.store.LinkBySubject(context.Background(), conn.ID, "approved-sub")
	require.Error(t, err, "a refused approval linked %+v", link)
	require.Empty(t, e.schema.Exec(e.ctx("root"), operation, "", nil).Errors)
	link, err = e.store.LinkBySubject(context.Background(), conn.ID, "approved-sub")
	require.NoError(t, err)
	require.NotZero(t, link.IdentityID)
}

// The approval is judged on the identity the link is actually made to. A request's email
// can be replaced by a later callback for the same subject, so a check that read the
// request separately would judge a different identity than the write links.
func TestAdminSsoApprovalJudgesTheIdentityItLinks(t *testing.T) {
	e := newAdminEscalationEnv(t)
	var conn iam.IdpConnection
	require.NoError(t, e.db.WithContext(core.WithSystemContext(context.Background())).First(&conn).Error)
	request := &iam.SsoLinkRequest{ConnectionID: conn.ID, Subject: "racing-sub", Email: "plain@example.com"}
	require.NoError(t, e.store.UpsertLinkRequest(context.Background(), request))
	ctx := e.ctx("helen")
	var judged []string
	err := e.mgr.ApproveSsoLinkRequestFor(ctx, request.ID, "", func(target *iam.Identity, conn *iam.IdpConnection) error {
		judged = append(judged, target.Email)
		require.Equal(t, request.ConnectionID, conn.ID)
		if err := e.svc.CheckAdminIdentityRecord(ctx, target); err != nil {
			return err
		}
		return e.svc.CheckAdminConnectionRecord(ctx, conn)
	})
	require.NoError(t, err)
	require.Equal(t, []string{"plain@example.com"}, judged)

	request = &iam.SsoLinkRequest{ConnectionID: conn.ID, Subject: "racing-sub-2", Email: "root@example.com"}
	require.NoError(t, e.store.UpsertLinkRequest(context.Background(), request))
	err = e.mgr.ApproveSsoLinkRequestFor(ctx, request.ID, "", func(target *iam.Identity, conn *iam.IdpConnection) error {
		return e.svc.CheckAdminIdentityRecord(ctx, target)
	})
	require.Error(t, err)
	_, err = e.store.LinkBySubject(context.Background(), conn.ID, "racing-sub-2")
	require.Error(t, err)
}

// Only Watch is used: nil marks the end of the existing session index snapshot.
type emptyAdminSessionStore struct{ nats.KeyValue }

func (emptyAdminSessionStore) Watch(string, ...nats.WatchOpt) (nats.KeyWatcher, error) {
	ch := make(chan nats.KeyValueEntry, 1)
	ch <- nil
	return emptyAdminSessionWatcher{updates: ch}, nil
}

type emptyAdminSessionWatcher struct {
	nats.KeyWatcher
	updates chan nats.KeyValueEntry
}

func (w emptyAdminSessionWatcher) Updates() <-chan nats.KeyValueEntry { return w.updates }
func (emptyAdminSessionWatcher) Stop() error                          { return nil }

// storedAdminCtx supplies a real identity to pre-existing successful resolver fixtures.
func storedAdminCtx(t *testing.T, store *iam.Store, authorities ...string) context.Context {
	t.Helper()
	role := &iam.Role{Scope: iam.ScopeSystem, Token: "test-caller", Authorities: authorities}
	require.NoError(t, store.CreateRole(context.Background(), role))
	require.NoError(t, store.CreateIdentity(context.Background(), &iam.Identity{Email: "caller@example.com", Enabled: true, SystemRoles: []iam.Role{*role}}))
	return auth.WithClaims(context.Background(), &auth.Claims{TokenType: auth.TokenTypeIdentity, Username: "caller@example.com", Authorities: authorities})
}

func TestAdminMissingTargetsAndUnknownRolesKeepTheirErrors(t *testing.T) {
	e := newAdminEscalationEnv(t)
	for _, tc := range []struct{ email, token string }{
		{"absent@example.com", "superuser"}, {"plain@example.com", "unknown"}, {"root@example.com", "unknown"},
	} {
		_, want := e.svc.SetSystemRoles(context.Background(), tc.email, []string{tc.token})
		require.Error(t, want)
		doc := fmt.Sprintf(`mutation { setSystemRoles(email:%q,roleTokens:[%q]) { email } }`, tc.email, tc.token)
		response := e.schema.Exec(e.ctx("helen"), doc, "", nil)
		require.Len(t, response.Errors, 1)
		require.Equal(t, want.Error(), response.Errors[0].Message)
	}
	require.Empty(t, e.schema.Exec(e.ctx("helen"), `mutation { deleteIdentity(email:"absent@example.com") }`, "", nil).Errors)
}

func TestAdminAuthorityCoverageUsesLiveUnionAndHierarchy(t *testing.T) {
	e := newAdminEscalationEnv(t)
	ctx := context.Background()
	role := &iam.Role{Scope: iam.ScopeSystem, Token: "locations", Authorities: []string{"location:read"}}
	require.NoError(t, e.store.CreateRole(ctx, role))
	_, err := e.svc.SetSystemRoles(ctx, "helen@example.com", []string{"helpdesk", "locations"})
	require.NoError(t, err)
	require.NoError(t, e.svc.CheckAdminAuthorities(e.ctx("helen"), []string{"user:write", "location:installed:read"}))
	err = e.svc.CheckAdminAuthorities(e.ctx("helen"), []string{"*"})
	var refusal *admin.TenantRefusal
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, "ESCALATION", refusal.Code())
	require.Contains(t, err.Error(), `"*"`)
	require.NoError(t, e.svc.CheckAdminAuthorities(e.ctx("root"), []string{"*", "location:installed:read"}))
}

func TestAdminCoverageReturnsStoreErrors(t *testing.T) {
	e := newAdminEscalationEnv(t)
	db, err := e.db.DB()
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, want := e.store.IdentityByEmail(context.Background(), "helen@example.com")
	require.Error(t, want)
	require.Equal(t, want, e.svc.CheckAdminAuthorities(e.ctx("helen"), nil))
}

func TestAdminMembershipNoEscalationOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		name, role, operation string
	}{
		{"add", "", `addMembership(email:"helen@example.com",tenant:"acme",roleTokens:["tenant-admin"]) { email }`},
		{"grant", "viewer", `setMembershipRoles(email:"plain@example.com",tenant:"acme",roleTokens:["tenant-admin"]) { email }`},
		{"roles", "tenant-admin", `setMembershipRoles(email:"plain@example.com",tenant:"acme",roleTokens:[]) { email }`},
		{"scope", "tenant-admin", `setMembershipScope(email:"plain@example.com",tenant:"acme",scope:[]) { email }`},
		{"enabled", "tenant-admin", `setMembershipEnabled(email:"plain@example.com",tenant:"acme",enabled:false) { email }`},
		{"remove", "tenant-admin", `removeMembership(email:"plain@example.com",tenant:"acme") { email }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAdminEscalationEnv(t)
			ctx := context.Background()
			if tc.role != "" {
				_, err := e.svc.AddMembership(ctx, "plain@example.com", "acme", []string{tc.role})
				require.NoError(t, err)
			}
			plain, err := e.store.IdentityByEmail(ctx, "plain@example.com")
			require.NoError(t, err)
			helen, err := e.store.IdentityByEmail(ctx, "helen@example.com")
			require.NoError(t, err)
			before, beforeErr := e.store.MembershipByIdentityTenant(ctx, plain.ID, "acme")
			response := e.schema.Exec(e.ctx("helen"), "mutation {"+tc.operation+"}", "", nil)
			require.Len(t, response.Errors, 1)
			require.Equal(t, "ESCALATION", response.Errors[0].Extensions["code"])
			if tc.name == "add" {
				_, err := e.store.MembershipByIdentityTenant(ctx, helen.ID, "acme")
				require.ErrorIs(t, err, gorm.ErrRecordNotFound)
			} else {
				require.NoError(t, beforeErr)
				after, err := e.store.MembershipByIdentityTenant(ctx, plain.ID, "acme")
				require.NoError(t, err)
				require.Equal(t, before, after)
			}
			require.Empty(t, e.schema.Exec(e.ctx("root"), "mutation {"+tc.operation+"}", "", nil).Errors)
		})
	}
}

func TestAdminDelegateLegitimateMembershipChanges(t *testing.T) {
	e := newAdminEscalationEnv(t)
	// Even a superuser identity's membership is judged solely on its tenant roles.
	for _, operation := range []string{
		`addMembership(email:"root@example.com",tenant:"acme",roleTokens:["viewer"]) { email }`,
		`setMembershipRoles(email:"root@example.com",tenant:"acme",roleTokens:["viewer"]) { email }`,
		`setMembershipScope(email:"root@example.com",tenant:"acme",scope:[]) { email }`,
		`setMembershipEnabled(email:"root@example.com",tenant:"acme",enabled:false) { email }`,
		`removeMembership(email:"root@example.com",tenant:"acme") { email }`,
	} {
		require.Empty(t, e.schema.Exec(e.ctx("helen"), "mutation {"+operation+"}", "", nil).Errors, operation)
	}
}

func (e *adminEscalationEnv) tenantConnection(t *testing.T) *iam.IdpConnection {
	t.Helper()
	conn := &iam.IdpConnection{Token: "tenant-idp", Kind: iam.IdpKindTenant}
	ctx := core.WithSystemContext(context.Background())
	require.NoError(t, e.db.WithContext(ctx).Create(conn).Error)
	require.NoError(t, e.db.WithContext(ctx).Create(&iam.IdpConnectionTenant{ConnectionID: conn.ID, TenantId: "acme"}).Error)
	return conn
}

func TestAdminIdpGrantsNoEscalationOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		name, operation string
		replace         bool
	}{
		{"mapping grant", `setIdpRoleMappings(connection:"tenant-idp",tenant:"acme",mappings:[{group:"admins",role:"tenant-admin"}]) { token }`, false},
		{"jit grant", `setIdpJit(connection:"tenant-idp",enabled:true,defaultTenantRoles:["tenant-admin"]) { token }`, false},
		{"mapping replace", `setIdpRoleMappings(connection:"tenant-idp",tenant:"acme",mappings:[]) { token }`, true},
		{"jit replace", `setIdpJit(connection:"tenant-idp",enabled:false,defaultTenantRoles:[]) { token }`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAdminEscalationEnv(t)
			conn := e.tenantConnection(t)
			ctx := context.Background()
			if tc.replace {
				role, err := e.store.CatalogueRoleByScopeToken(ctx, iam.ScopeTenant, "tenant-admin")
				require.NoError(t, err)
				require.NoError(t, e.store.ReplaceIdpRoleMappings(ctx, conn.ID, "acme", []iam.IdpRoleMapping{{GroupValue: "admins", RoleID: role.ID}}))
				conn.DefaultTenantRoles = []string{"tenant-admin"}
				conn.JitEnabled = true
				require.NoError(t, e.store.UpdateIdpConnection(ctx, conn))
			}
			before, err := e.store.IdpConnectionByToken(ctx, conn.Token)
			require.NoError(t, err)
			mappings, err := e.store.ListIdpRoleMappings(ctx, conn.ID)
			require.NoError(t, err)
			response := e.schema.Exec(e.ctx("helen"), "mutation {"+tc.operation+"}", "", nil)
			require.Len(t, response.Errors, 1)
			require.Equal(t, "ESCALATION", response.Errors[0].Extensions["code"])
			after, err := e.store.IdpConnectionByToken(ctx, conn.Token)
			require.NoError(t, err)
			require.Equal(t, before, after)
			afterMappings, err := e.store.ListIdpRoleMappings(ctx, conn.ID)
			require.NoError(t, err)
			require.Equal(t, mappings, afterMappings)
			require.Empty(t, e.schema.Exec(e.ctx("root"), "mutation {"+tc.operation+"}", "", nil).Errors)
		})
	}
}

func TestAdminDelegateLegitimateIdpGrants(t *testing.T) {
	e := newAdminEscalationEnv(t)
	e.tenantConnection(t)
	for _, operation := range []string{
		`setIdpRoleMappings(connection:"tenant-idp",tenant:"acme",mappings:[{group:"viewers",role:"viewer"}]) { token }`,
		`setIdpJit(connection:"tenant-idp",enabled:true,defaultTenantRoles:["viewer"]) { token }`,
		`setIdpRoleMappings(connection:"tenant-idp",tenant:"acme",mappings:[]) { token }`,
		`setIdpJit(connection:"tenant-idp",enabled:false,defaultTenantRoles:[]) { token }`,
	} {
		require.Empty(t, e.schema.Exec(e.ctx("helen"), "mutation {"+operation+"}", "", nil).Errors, operation)
	}
}

func TestAdminMembershipAndIdpErrorsKeepTheirMessages(t *testing.T) {
	e := newAdminEscalationEnv(t)
	e.tenantConnection(t)
	ctx := context.Background()
	_, err := e.svc.AddMembership(ctx, "plain@example.com", "acme", []string{"tenant-admin"})
	require.NoError(t, err)
	for _, tc := range []struct{ email, tenant, role string }{
		{"absent@example.com", "acme", "tenant-admin"},
		{"helen@example.com", "absent", "tenant-admin"},
		{"helen@example.com", "acme", "unknown"},
		{"plain@example.com", "acme", "unknown"},
	} {
		_, want := e.svc.AddMembership(ctx, tc.email, tc.tenant, []string{tc.role})
		require.Error(t, want)
		doc := fmt.Sprintf(`mutation { addMembership(email:%q,tenant:%q,roleTokens:[%q]) { email } }`, tc.email, tc.tenant, tc.role)
		response := e.schema.Exec(e.ctx("helen"), doc, "", nil)
		require.Len(t, response.Errors, 1)
		require.Equal(t, want.Error(), response.Errors[0].Message)
	}
	for _, tc := range []struct{ email, tenant, role string }{
		{"absent@example.com", "acme", "tenant-admin"},
		{"helen@example.com", "acme", "tenant-admin"},
		{"plain@example.com", "absent", "tenant-admin"},
		{"plain@example.com", "acme", "unknown"},
	} {
		_, want := e.svc.SetMembershipRoles(ctx, tc.email, tc.tenant, []string{tc.role})
		require.Error(t, want)
		doc := fmt.Sprintf(`mutation { setMembershipRoles(email:%q,tenant:%q,roleTokens:[%q]) { email } }`, tc.email, tc.tenant, tc.role)
		response := e.schema.Exec(e.ctx("helen"), doc, "", nil)
		require.Len(t, response.Errors, 1)
		require.Equal(t, want.Error(), response.Errors[0].Message)
	}
	for _, target := range []struct{ email, tenant string }{
		{"absent@example.com", "acme"}, {"helen@example.com", "acme"}, {"plain@example.com", "absent"},
	} {
		for _, tc := range []struct {
			operation string
			call      func() (*iam.Identity, error)
		}{
			{fmt.Sprintf(`setMembershipScope(email:%q,tenant:%q,scope:[]) { email }`, target.email, target.tenant), func() (*iam.Identity, error) {
				return e.svc.SetMembershipScope(ctx, target.email, target.tenant, nil)
			}},
			{fmt.Sprintf(`setMembershipEnabled(email:%q,tenant:%q,enabled:false) { email }`, target.email, target.tenant), func() (*iam.Identity, error) {
				return e.svc.SetMembershipEnabled(ctx, target.email, target.tenant, false)
			}},
			{fmt.Sprintf(`removeMembership(email:%q,tenant:%q) { email }`, target.email, target.tenant), func() (*iam.Identity, error) {
				return e.svc.RemoveMembership(ctx, target.email, target.tenant)
			}},
		} {
			_, want := tc.call()
			require.Error(t, want)
			response := e.schema.Exec(e.ctx("helen"), "mutation {"+tc.operation+"}", "", nil)
			require.Len(t, response.Errors, 1)
			require.Equal(t, want.Error(), response.Errors[0].Message)
		}
	}
	for _, tc := range []struct{ connection, tenant, role string }{
		{"absent", "acme", "tenant-admin"},
		{"tenant-idp", "absent", "tenant-admin"},
		{"tenant-idp", "acme", "unknown"},
	} {
		_, want := e.mgr.SetIdpRoleMappings(ctx, tc.connection, tc.tenant, []identity.RoleMappingInput{{Group: "admins", Role: tc.role}}, false)
		require.Error(t, want)
		doc := fmt.Sprintf(`mutation { setIdpRoleMappings(connection:%q,tenant:%q,mappings:[{group:"admins",role:%q}]) { token } }`, tc.connection, tc.tenant, tc.role)
		response := e.schema.Exec(e.ctx("helen"), doc, "", nil)
		require.Len(t, response.Errors, 1)
		require.Equal(t, want.Error(), response.Errors[0].Message)
	}
	for _, tc := range []struct{ connection, role string }{
		{"absent", "tenant-admin"}, {"tenant-idp", "unknown"},
	} {
		_, want := e.mgr.SetIdpJit(ctx, tc.connection, true, []string{tc.role}, false)
		require.Error(t, want)
		doc := fmt.Sprintf(`mutation { setIdpJit(connection:%q,enabled:true,defaultTenantRoles:[%q]) { token } }`, tc.connection, tc.role)
		response := e.schema.Exec(e.ctx("helen"), doc, "", nil)
		require.Len(t, response.Errors, 1)
		require.Equal(t, want.Error(), response.Errors[0].Message)
	}
}

func TestAdminMembershipGrantsResolveTenantOwnedRoles(t *testing.T) {
	e := newAdminEscalationEnv(t)
	ctx := context.Background()
	tenant := "acme"
	// Catalogue-only resolution would miss both of these tenant-owned grants.
	for _, role := range []iam.Role{
		{Scope: iam.ScopeTenant, TenantID: &tenant, Token: "own-admin", Authorities: []string{"*"}},
		{Scope: iam.ScopeTenant, TenantID: &tenant, Token: "own-viewer", Authorities: []string{"user:read"}},
	} {
		role := role
		require.NoError(t, e.store.CreateRole(ctx, &role))
	}
	response := e.schema.Exec(e.ctx("helen"), `mutation { addMembership(email:"plain@example.com",tenant:"acme",roleTokens:["own-admin"]) { email } }`, "", nil)
	require.Len(t, response.Errors, 1)
	require.Equal(t, "ESCALATION", response.Errors[0].Extensions["code"])
	require.Empty(t, e.schema.Exec(e.ctx("helen"), `mutation { addMembership(email:"plain@example.com",tenant:"acme",roleTokens:["own-viewer"]) { email } }`, "", nil).Errors)
	response = e.schema.Exec(e.ctx("helen"), `mutation { setMembershipRoles(email:"plain@example.com",tenant:"acme",roleTokens:["own-admin"]) { email } }`, "", nil)
	require.Len(t, response.Errors, 1)
	require.Equal(t, "ESCALATION", response.Errors[0].Extensions["code"])
}

func TestAdminIdentityMembershipGrantsNoEscalationOnTheWire(t *testing.T) {
	for _, operation := range []string{
		`setPassword(email:"plain@example.com",password:"changed") { email }`,
		`clearPassword(email:"plain@example.com") { email }`,
		`setIdentityEnabled(email:"plain@example.com",enabled:false) { email }`,
	} {
		t.Run(operation, func(t *testing.T) {
			e := newAdminEscalationEnv(t)
			ctx := context.Background()
			_, err := e.svc.AddMembership(ctx, "plain@example.com", "acme", []string{"tenant-admin"})
			require.NoError(t, err)
			plain, err := e.store.IdentityByEmail(ctx, "plain@example.com")
			require.NoError(t, err)
			require.Empty(t, plain.SystemRoles)
			conn, err := e.store.IdpConnectionByToken(ctx, "instance")
			require.NoError(t, err)
			require.NoError(t, e.store.CreateIdentityLink(ctx, &iam.IdentityLink{IdentityID: plain.ID, ConnectionID: conn.ID, Subject: "plain-sub"}))
			response := e.schema.Exec(e.ctx("helen"), "mutation {"+operation+"}", "", nil)
			require.Len(t, response.Errors, 1)
			require.Equal(t, "ESCALATION", response.Errors[0].Extensions["code"])
			require.Contains(t, response.Errors[0].Message, "membership in acme")
			after, err := e.store.IdentityByEmail(ctx, plain.Email)
			require.NoError(t, err)
			require.Equal(t, plain.PasswordHash, after.PasswordHash)
			require.Equal(t, plain.Enabled, after.Enabled)
			require.Equal(t, plain.SessionEpoch, after.SessionEpoch)
			require.Empty(t, e.schema.Exec(e.ctx("root"), "mutation {"+operation+"}", "", nil).Errors)
		})
	}
	e := newAdminEscalationEnv(t)
	_, err := e.svc.AddMembership(context.Background(), "plain@example.com", "acme", []string{"viewer"})
	require.NoError(t, err)
	require.Empty(t, e.schema.Exec(e.ctx("helen"), `mutation { setPassword(email:"plain@example.com",password:"changed") { email } }`, "", nil).Errors)
}

func TestAdminConnectionGrantsNoEscalationOnTheWire(t *testing.T) {
	for _, grant := range []string{"defaults", "other tenant mapping"} {
		for _, operation := range []string{"link", "convert", "keep_manual", "extend", "remove", "groups"} {
			t.Run(grant+"/"+operation, func(t *testing.T) {
				e := newAdminEscalationEnv(t)
				ctx := context.Background()
				conn := e.tenantConnection(t)
				conn.Name = "Tenant connection"
				conn.GroupsClaim = "groups"
				if grant == "defaults" {
					conn.DefaultTenantRoles = []string{"tenant-admin"}
				} else {
					require.NoError(t, e.store.CreateTenant(ctx, &iam.Tenant{Token: "other", Enabled: true}))
					require.NoError(t, e.store.ReplaceConnectionTenants(ctx, conn, []string{"acme", "other"}))
					role, err := e.store.CatalogueRoleByScopeToken(ctx, iam.ScopeTenant, "tenant-admin")
					require.NoError(t, err)
					require.NoError(t, e.store.ReplaceIdpRoleMappings(ctx, conn.ID, "other", []iam.IdpRoleMapping{{GroupValue: "admins", RoleID: role.ID}}))
				}
				require.NoError(t, e.store.UpdateIdpConnection(ctx, conn))
				_, err := e.svc.AddMembership(ctx, "plain@example.com", "acme", []string{"viewer"})
				require.NoError(t, err)
				plain, err := e.store.IdentityByEmail(ctx, "plain@example.com")
				require.NoError(t, err)
				query := ""
				var request *iam.SsoLinkRequest
				switch operation {
				case "link":
					query = `linkIdentity(email:"plain@example.com",connection:"tenant-idp",subject:"plain-sub")`
				case "convert", "keep_manual":
					request = &iam.SsoLinkRequest{ConnectionID: conn.ID, Subject: "plain-sub", Email: plain.Email}
					require.NoError(t, e.store.UpsertLinkRequest(ctx, request))
					query = fmt.Sprintf(`approveSsoLinkRequest(id:"%d",membershipDisposition:%q)`, request.ID, operation)
				case "extend":
					require.NoError(t, e.store.CreateTenant(ctx, &iam.Tenant{Token: "extra", Enabled: true}))
					query = `setIdpConnectionTenants(token:"tenant-idp",tenants:["acme","extra"]) { token }`
				case "remove":
					require.NoError(t, e.store.MarkMembershipsManagedBy(ctx, plain.ID, []string{"acme"}, conn.ID))
					query = `setIdpConnectionTenants(token:"tenant-idp",tenants:[]) { token }`
				case "groups":
					query = `updateIdpConnection(token:"tenant-idp",request:{groupsClaim:"roles"}) { token }`
				}
				before, err := e.store.IdpConnectionByToken(ctx, conn.Token)
				require.NoError(t, err)
				tenants, err := e.store.ConnectionTenants(ctx, conn.ID)
				require.NoError(t, err)
				mappings, err := e.store.ListIdpRoleMappings(ctx, conn.ID)
				require.NoError(t, err)
				mem, err := e.store.MembershipByIdentityTenant(ctx, plain.ID, "acme")
				require.NoError(t, err)
				response := e.schema.Exec(e.ctx("helen"), "mutation {"+query+"}", "", nil)
				require.Len(t, response.Errors, 1)
				require.Equal(t, "ESCALATION", response.Errors[0].Extensions["code"])
				after, err := e.store.IdpConnectionByToken(ctx, conn.Token)
				require.NoError(t, err)
				require.Equal(t, before, after)
				afterTenants, err := e.store.ConnectionTenants(ctx, conn.ID)
				require.NoError(t, err)
				require.Equal(t, tenants, afterTenants)
				afterMappings, err := e.store.ListIdpRoleMappings(ctx, conn.ID)
				require.NoError(t, err)
				require.Equal(t, mappings, afterMappings)
				afterMem, err := e.store.MembershipByIdentityTenant(ctx, plain.ID, "acme")
				require.NoError(t, err)
				require.Equal(t, mem, afterMem)
				_, err = e.store.LinkBySubject(ctx, conn.ID, "plain-sub")
				require.ErrorIs(t, err, gorm.ErrRecordNotFound)
				if request != nil {
					_, err = e.store.SsoLinkRequestByID(ctx, request.ID)
					require.NoError(t, err)
				}
				require.Empty(t, e.schema.Exec(e.ctx("root"), "mutation {"+query+"}", "", nil).Errors)
				if operation == "convert" || operation == "remove" {
					afterMem, err = e.store.MembershipByIdentityTenant(ctx, plain.ID, "acme")
					require.NoError(t, err)
					require.Equal(t, &conn.ID, afterMem.ManagedBy)
					if operation == "remove" {
						require.False(t, afterMem.Enabled)
					}
				}
			})
		}
	}
}

func TestAdminConnectionUpdatesPreserveUnrelatedAndUnchangedFields(t *testing.T) {
	e := newAdminEscalationEnv(t)
	conn := e.tenantConnection(t)
	conn.Name = "Tenant connection"
	conn.GroupsClaim = "groups"
	conn.DefaultTenantRoles = []string{"tenant-admin"}
	require.NoError(t, e.store.UpdateIdpConnection(context.Background(), conn))
	for _, request := range []string{`{name:"Renamed"}`, `{groupsClaim:"groups"}`} {
		query := `mutation { updateIdpConnection(token:"tenant-idp",request:` + request + `) { token } }`
		require.Empty(t, e.schema.Exec(e.ctx("helen"), query, "", nil).Errors)
	}
}

func TestAdminProposedRoleLookupErrorsRefuseWrites(t *testing.T) {
	for _, operation := range []string{
		`setIdpJit(connection:"tenant-idp",enabled:true,defaultTenantRoles:["viewer"]) { token }`,
		`setIdpRoleMappings(connection:"tenant-idp",tenant:"acme",mappings:[{group:"viewers",role:"viewer"}]) { token }`,
		`addMembership(email:"plain@example.com",tenant:"acme",roleTokens:["viewer"]) { email }`,
	} {
		t.Run(operation, func(t *testing.T) {
			e := newAdminEscalationEnv(t)
			conn := e.tenantConnection(t)
			want := errors.New("proposed role lookup failed")
			failed := false
			// The caller's preloaded roles use an ID lookup. Fail only the first
			// token lookup, so falling through to the write would wrongly succeed.
			require.NoError(t, e.db.Callback().Query().After("gorm:query").Register("test:fail_role_lookup", func(db *gorm.DB) {
				if !failed && db.Statement.Table == "iam_roles" && strings.Contains(db.Statement.SQL.String(), "token IN") {
					failed = true
					db.AddError(want)
				}
			}))
			t.Cleanup(func() { e.db.Callback().Query().Remove("test:fail_role_lookup") })
			response := e.schema.Exec(e.ctx("helen"), "mutation {"+operation+"}", "", nil)
			require.True(t, failed)
			require.Len(t, response.Errors, 1)
			require.Contains(t, response.Errors[0].Message, want.Error())
			after, err := e.store.IdpConnectionByToken(context.Background(), conn.Token)
			require.NoError(t, err)
			require.False(t, after.JitEnabled)
			require.Empty(t, after.DefaultTenantRoles)
			mappings, err := e.store.ListIdpRoleMappings(context.Background(), conn.ID)
			require.NoError(t, err)
			require.Empty(t, mappings)
			plain, err := e.store.IdentityByEmail(context.Background(), "plain@example.com")
			require.NoError(t, err)
			require.Empty(t, plain.Memberships)
			require.Empty(t, e.schema.Exec(e.ctx("root"), "mutation {"+operation+"}", "", nil).Errors)
		})
	}
}
