// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// These registry deletes share a model helper, but each served mutation must
// preserve its result, reference refusal, and tenant scope through the schema.
func TestDeleteTypesOverTheWire(t *testing.T) {
	for _, tc := range []struct {
		mutation string
		query    string
		parent   func(string) any
		child    func(any) any
	}{
		{
			mutation: "deleteAreaType", query: "areaTypesByToken",
			parent: func(token string) any {
				return &model.AreaType{TokenReference: rdb.TokenReference{Token: token}}
			},
			child: func(parent any) any {
				return &model.Area{TokenReference: rdb.TokenReference{Token: "instance"}, AreaTypeId: parent.(*model.AreaType).ID}
			},
		},
		{
			mutation: "deleteCustomerType", query: "customerTypesByToken",
			parent: func(token string) any {
				return &model.CustomerType{TokenReference: rdb.TokenReference{Token: token}}
			},
			child: func(parent any) any {
				return &model.Customer{TokenReference: rdb.TokenReference{Token: "instance"}, CustomerTypeId: parent.(*model.CustomerType).ID}
			},
		},
		{
			mutation: "deleteEntityRelationshipType", query: "entityRelationshipTypesByToken",
			parent: func(token string) any {
				return &model.EntityRelationshipType{TokenReference: rdb.TokenReference{Token: token}}
			},
			child: func(parent any) any {
				return &model.EntityRelationship{TokenReference: rdb.TokenReference{Token: "instance"},
					SourceType: "area", SourceId: 1, TargetType: "customer", TargetId: 1,
					RelationshipTypeId: parent.(*model.EntityRelationshipType).ID}
			},
		},
	} {
		t.Run(tc.mutation, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			require.NoError(t, rdb.RegisterTenantScoping(db))
			require.NoError(t, db.AutoMigrate(tc.parent(""), tc.child(tc.parent(""))))
			require.NoError(t, rdb.CreateTenantTokenIndex(db, tc.parent("")))
			api := model.NewApi(&rdb.RdbManager{Database: db})
			operator := func(tenant string) context.Context {
				ctx := core.WithTenant(context.Background(), tenant)
				ctx = auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: tenant,
					Authorities: []string{string(auth.DeviceRead), string(auth.DeviceWrite)}})
				return context.WithValue(ctx, gqlcore.ContextApiKey, api)
			}
			acme, globex := operator("acme"), operator("globex")
			schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
			doc := fmt.Sprintf(`mutation ($token: String!) { %s(token: $token) }`, tc.mutation)
			read := fmt.Sprintf(`query ($tokens: [String!]!) { %s(tokens: $tokens) { token } }`, tc.query)
			seed := func(t *testing.T, ctx context.Context, token string) any {
				t.Helper()
				row := tc.parent(token)
				require.NoError(t, api.RDB.DB(ctx).Create(row).Error)
				return row
			}
			assertPresent := func(t *testing.T, ctx context.Context, token string, present bool) {
				t.Helper()
				resp := schema.Exec(ctx, read, "", map[string]any{"tokens": []any{token}})
				require.Empty(t, resp.Errors)
				var data map[string][]struct{ Token string }
				require.NoError(t, json.Unmarshal(resp.Data, &data))
				if present {
					require.Len(t, data[tc.query], 1)
					require.Equal(t, token, data[tc.query][0].Token)
				} else {
					require.Empty(t, data[tc.query])
				}
			}
			assertDelete := func(t *testing.T, ctx context.Context, token string, deleted bool) {
				t.Helper()
				resp := schema.Exec(ctx, doc, "", map[string]any{"token": token})
				require.Empty(t, resp.Errors)
				require.JSONEq(t, fmt.Sprintf(`{%q:%t}`, tc.mutation, deleted), string(resp.Data))
			}

			t.Run("success", func(t *testing.T) {
				seed(t, acme, "unused")
				assertPresent(t, acme, "unused", true)
				assertDelete(t, acme, "unused", true)
				assertPresent(t, acme, "unused", false)
			})
			t.Run("not_found", func(t *testing.T) {
				assertDelete(t, acme, "missing", false)
				assertDelete(t, acme, "unused", false)
			})
			t.Run("in_use", func(t *testing.T) {
				parent := seed(t, acme, "used")
				child := tc.child(parent)
				require.NoError(t, api.RDB.DB(acme).Create(child).Error)
				resp := schema.Exec(acme, doc, "", map[string]any{"token": "used"})
				qe := onlyError(t, resp.Errors)
				require.Equal(t, "REFERENCE_VIOLATION", qe.Extensions["code"])
				require.Equal(t, `entity is still referenced and cannot be deleted: 1 row(s) reference "used"`, qe.Message)
				assertPresent(t, acme, "used", true)
				require.NoError(t, api.RDB.DB(acme).First(child).Error, "the referencing row survives the refusal")
				require.NoError(t, api.RDB.DB(acme).Unscoped().Delete(child).Error)
				assertDelete(t, acme, "used", true)
				assertPresent(t, acme, "used", false)
			})
			t.Run("tenant_isolation", func(t *testing.T) {
				seed(t, acme, "foreign")
				assertDelete(t, globex, "foreign", false)
				assertPresent(t, acme, "foreign", true)
				// A scoped lookup alone cannot protect the final delete when both
				// tenants have the requested token. Only the caller's row may go.
				seed(t, acme, "shared")
				seed(t, globex, "shared")
				assertDelete(t, globex, "shared", true)
				assertPresent(t, globex, "shared", false)
				assertPresent(t, acme, "shared", true)
			})
		})
	}
}
