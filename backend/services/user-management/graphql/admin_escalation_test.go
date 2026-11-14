// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
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
		&iam.IdpConnectionTenant{}, &iam.IdpRoleMapping{}, &iam.SsoLinkRequest{}, &iam.ServiceAccount{})
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
	err := e.mgr.ApproveSsoLinkRequestFor(ctx, request.ID, "", func(target *iam.Identity) error {
		judged = append(judged, target.Email)
		return e.svc.CheckAdminIdentityRecord(ctx, target)
	})
	require.NoError(t, err)
	require.Equal(t, []string{"plain@example.com"}, judged)

	request = &iam.SsoLinkRequest{ConnectionID: conn.ID, Subject: "racing-sub-2", Email: "root@example.com"}
	require.NoError(t, e.store.UpsertLinkRequest(context.Background(), request))
	err = e.mgr.ApproveSsoLinkRequestFor(ctx, request.ID, "", func(target *iam.Identity) error {
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
