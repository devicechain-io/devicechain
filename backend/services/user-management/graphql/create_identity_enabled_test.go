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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 createIdentity(enabled: false) MUST CREATE A DISABLED IDENTITY. The column carried
// `default:true`, and gorm treats a bool's zero value as "unset" on Create and omits the
// column so the DB default wins: an explicit false was swallowed and the identity came back
// ENABLED. This drives the real admin schema, resolver, service and model, then reads the
// row back with raw SQL so the assertion is about the column.
func TestCreateIdentityHonoursExplicitEnabledFalse(t *testing.T) {
	db := putest.NewSQLiteDB(t, &iam.Role{}, &iam.Identity{}, &iam.Membership{})
	rdbm := &rdb.RdbManager{Database: db}
	svc := admin.NewService(iam.NewStore(rdbm), 300*time.Second, 12*time.Hour, nil)
	ctx := adminCtx(string(auth.UserWrite), string(auth.UserRead))
	ctx = context.WithValue(ctx, ContextAdminKey, svc)
	schema := gqlcore.MustParseSchema(AdminSchemaContent, &AdminResolver{})

	for _, enabled := range []bool{false, true} {
		email := "off@example.invalid"
		if enabled {
			email = "on@example.invalid"
		}
		resp := schema.Exec(ctx, `mutation($r: AdminIdentityCreateRequest!) {
			createIdentity(request: $r) { email enabled } }`, "", map[string]any{
			"r": map[string]any{"email": email, "password": "correct-horse-battery", "enabled": enabled, "systemRoles": []any{}},
		})
		require.Empty(t, resp.Errors)
		assert.JSONEq(t, `{"createIdentity":{"email":"`+email+`","enabled":`+boolStr(enabled)+`}}`,
			string(resp.Data))

		var stored bool
		require.NoError(t, db.Raw("SELECT enabled FROM iam_identities WHERE email = ?", email).
			Scan(&stored).Error)
		assert.Equal(t, enabled, stored, "the stored column must be what the caller asked for")
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
