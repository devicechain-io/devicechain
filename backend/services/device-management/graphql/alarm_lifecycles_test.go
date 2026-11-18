// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestAlarmLifecyclesServicePlane(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, model.RegisterPositionWithholding(db))
	require.NoError(t, db.AutoMigrate(&model.Alarm{}))
	api := model.NewApi(&rdb.RdbManager{Database: db})
	base := core.WithTenant(context.Background(), "t1")
	alarm := &model.Alarm{State: "ACTIVE", PositionDerived: new(bool)}
	*alarm.PositionDerived = true
	alarm.Token = "fence"
	require.NoError(t, api.RDB.DB(base).Create(alarm).Error)
	base = context.WithValue(base, gqlcore.ContextApiKey, api)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	for _, tc := range []struct {
		name        string
		kind        string
		authorities []string
		admitted    bool
	}{
		{"member", auth.TokenTypeAccess, []string{"alarm:read", "location:read"}, false},
		{"admin", auth.TokenTypeAccess, []string{"*"}, false},
		{"service-without-authority", auth.TokenTypeService, []string{"tenant:read"}, false},
		{"service", auth.TokenTypeService, []string{"alarm:read"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := auth.WithClaims(base, &auth.Claims{TokenType: tc.kind, Tenant: "t1", Authorities: tc.authorities})
			response := schema.Exec(ctx, `query { alarmLifecycles(tokens: ["fence"]) { token state } }`, "", nil)
			if !tc.admitted {
				require.NotEmpty(t, response.Errors)
				return
			}
			require.Empty(t, response.Errors)
			var data map[string]any
			require.NoError(t, json.Unmarshal(response.Data, &data))
			require.Equal(t, map[string]any{"alarmLifecycles": []any{map[string]any{"token": "fence", "state": "ACTIVE"}}}, data)
		})
	}
}
