// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

func TestAdminRoleExpectedUpdatedAt(t *testing.T) {
	db := putest.NewSQLiteDB(t, &iam.Role{})
	// Unconditional saves must advance even when a clock has not moved.
	frozen := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	db.NowFunc = func() time.Time { return frozen }
	store := iam.NewStore(&rdb.RdbManager{Database: db})
	svc := admin.NewService(store, 0, 0, nil)
	ctx := context.WithValue(adminCtx(string(auth.RoleWrite)), ContextAdminKey, svc)
	role, err := svc.CreateRole(ctx, admin.RoleInput{Scope: "system", Token: "ops", Name: "original"})
	require.NoError(t, err)
	schema := gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	const doc = `mutation($expected: String, $request: AdminRoleUpdateRequest!) {
		updateRole(scope: "system", token: "ops", request: $request, expectedUpdatedAt: $expected) {
			name updatedAt authorities
		}
	}`
	version := func() string {
		r, err := store.CatalogueRoleByScopeToken(ctx, iam.ScopeSystem, "ops")
		require.NoError(t, err)
		return r.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	initial := version()
	// A valid precondition persists the authority set as JSON, including clears.
	response := schema.Exec(ctx, doc, "", map[string]any{
		"expected": initial, "request": map[string]any{"name": "winner", "authorities": []any{string(auth.RoleRead)}},
	})
	require.Empty(t, response.Errors)
	var data struct {
		UpdateRole struct {
			Name        string
			Authorities []string
		}
	}
	require.NoError(t, json.Unmarshal(response.Data, &data))
	require.Equal(t, "winner", data.UpdateRole.Name)
	require.Equal(t, []string{string(auth.RoleRead)}, data.UpdateRole.Authorities)
	require.NotEqual(t, initial, version())
	response = schema.Exec(ctx, doc, "", map[string]any{
		"expected": initial, "request": map[string]any{"name": "loser", "authorities": []any{}},
	})
	require.Len(t, response.Errors, 1)
	require.Contains(t, response.Errors[0].Message, "modified by another writer")
	stored, err := store.CatalogueRoleByScopeToken(ctx, iam.ScopeSystem, role.Token)
	require.NoError(t, err)
	require.Equal(t, "winner", stored.Name.String)
	require.Equal(t, []string{string(auth.RoleRead)}, stored.Authorities)
	// Existing documents with no new argument still work and invalidate old versions.
	before := version()
	response = schema.Exec(ctx, `mutation { updateRole(scope:"system", token:"ops", request:{name:"legacy"}) { name } }`, "", nil)
	require.Empty(t, response.Errors)
	beforeTime, err := time.Parse(time.RFC3339Nano, before)
	require.NoError(t, err)
	afterTime, err := time.Parse(time.RFC3339Nano, version())
	require.NoError(t, err)
	require.True(t, afterTime.After(beforeTime), "an unconditional write must advance its version even with a frozen clock")
	response = schema.Exec(ctx, doc, "", map[string]any{
		"expected": before, "request": map[string]any{"name": "stale"},
	})
	require.Len(t, response.Errors, 1)
	response = schema.Exec(ctx, doc, "", map[string]any{
		"expected": version(), "request": map[string]any{"authorities": []any{}},
	})
	require.Empty(t, response.Errors)
	stored, err = store.CatalogueRoleByScopeToken(ctx, iam.ScopeSystem, role.Token)
	require.NoError(t, err)
	require.Empty(t, stored.Authorities)
}
