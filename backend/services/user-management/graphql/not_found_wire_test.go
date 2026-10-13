// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 A MISS ON THE ADMIN PLANE CARRIES NOT_FOUND. The GraphQL layer attaches
// extensions.code = NOT_FOUND when a resolver error's chain holds gorm.ErrRecordNotFound, and
// the public reference promises it for every miss. user-management used to replace the
// store's miss with its own plain sentinels, which dropped the chain and so the code: the
// client got prose only. This drives each sentinel through the REAL admin schema (the core
// executor, not graphql-go's bare one, because the code is attached by the core executor).
func TestAdminMissesCarryNotFound(t *testing.T) {
	db := putest.NewSQLiteDB(t, &iam.Role{}, &iam.TenantTier{}, &iam.Tenant{},
		&iam.OAuthClient{}, &iam.Identity{}, &iam.Membership{},
		&iam.IdpConnection{}, &iam.SsoLinkRequest{})
	rdbm := &rdb.RdbManager{Database: db}
	svc := admin.NewService(iam.NewStore(rdbm), 300*time.Second, 12*time.Hour, nil)
	mgr := identity.NewManager(nil, rdbm, nil, nil, 0, 0, "", identity.BootstrapConfig{}, nil)

	ctx := adminCtx(string(auth.RoleWrite), string(auth.TenantWrite), string(auth.ClientWrite),
		string(auth.UserWrite), string(auth.UserRead))
	ctx = context.WithValue(ctx, ContextAdminKey, svc)
	ctx = context.WithValue(ctx, ContextAdminSsoKey, mgr)
	schema := gqlcore.MustParseSchema(AdminSchemaContent, &AdminResolver{})

	cases := []struct{ name, query, wantMsg string }{
		{"role", `mutation { updateRole(scope: "tenant", token: "ghost", request: {}) { token } }`, "role not found"},
		{"tier", `mutation { updateTenantTier(token: "ghost", request: {}) { token } }`, "tenant tier not found"},
		{"identity", `mutation { setIdentityEnabled(email: "ghost@example.com", enabled: true) { email } }`, "identity not found"},
		{"oauth client", `mutation { setOauthClientEnabled(clientId: "ghost", enabled: true) { clientId } }`, "oauth client not found"},
		{"idp connection", `mutation { updateIdpConnection(token: "ghost", request: {}) { token } }`, "identity provider connection not found"},
		{"link request", `mutation { approveSsoLinkRequest(id: "999") }`, "link request not found"},
		{"link request bad id", `mutation { approveSsoLinkRequest(id: "nope") }`, "link request not found"},
		{"tenant", `mutation { setTenantSsoEnforced(tenant: "ghost", enforced: true) { token } }`, "tenant not found"},
		{"sessions of identity", `mutation { revokeSessions(email: "ghost@example.com") }`, "identity not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := schema.Exec(ctx, c.query, "", nil)
			require.Len(t, resp.Errors, 1, "expected exactly one error, got %v", resp.Errors)
			assert.Equal(t, "NOT_FOUND", resp.Errors[0].Extensions["code"],
				"a miss must carry extensions.code NOT_FOUND")
			assert.Contains(t, resp.Errors[0].Message, c.wantMsg, "the message must stay as the service wrote it")
		})
	}
}
