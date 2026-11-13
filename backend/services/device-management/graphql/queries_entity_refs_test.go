// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/entity"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Keep the resolver's anonymous input shape so these tests also run without the guard.
func entityRefsInput(etype string, n int) []struct{ Type, Token string } {
	refs := make([]struct{ Type, Token string }, n)
	for i := range refs {
		refs[i].Type, refs[i].Token = etype, "missing"
	}
	return refs
}

func TestExistingEntityRefsBounds(t *testing.T) {
	tests := []struct {
		name        string
		refs        []struct{ Type, Token string }
		wantQueries int
		limitGot    int
		unknown     string
	}{
		{
			name: "exactly one type limit",
			refs: entityRefsInput("device", rdb.MaxLookupKeys), wantQueries: 1,
		},
		{
			name: "one type over limit",
			refs: entityRefsInput("device", rdb.MaxLookupKeys+1), limitGot: rdb.MaxLookupKeys + 1,
		},
		{
			name:     "over limit after valid type",
			refs:     append(entityRefsInput("device", 1), entityRefsInput("asset", rdb.MaxLookupKeys+1)...),
			limitGot: rdb.MaxLookupKeys + 1,
		},
		{
			name: "600 devices and 600 assets",
			refs: append(entityRefsInput("device", 600), entityRefsInput("asset", 600)...), wantQueries: 2,
		},
		{
			// The refusal is the one the lookups in first-appearance order would give.
			name:     "over limit before unknown type",
			refs:     append(entityRefsInput("device", rdb.MaxLookupKeys+1), entityRefsInput("widget", 1)...),
			limitGot: rdb.MaxLookupKeys + 1,
		},
		{
			name: "unknown after valid type",
			refs: append(entityRefsInput("device", 1), entityRefsInput("widget", 1)...), unknown: "widget",
		},
		{
			// An unknown first entry makes this distinguish the pre-loop aggregate
			// check from the checks performed while grouping.
			name:     "aggregate before grouping",
			refs:     entityRefsInput("widget", len(entity.All)*rdb.MaxLookupKeys+1),
			limitGot: len(entity.All)*rdb.MaxLookupKeys + 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := withAuthorities(deviceIdTestCtx(t), auth.DeviceRead)
			api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
			require.NoError(t, api.RDB.Database.AutoMigrate(&model.Asset{}))
			queries := 0
			require.NoError(t, api.RDB.Database.Callback().Query().Before("gorm:query").Register("test:count_entity_lookups", func(*gorm.DB) {
				queries++
			}))

			found, err := (&SchemaResolver{}).ExistingEntityRefs(ctx, struct {
				Refs []struct{ Type, Token string }
			}{Refs: tt.refs})
			switch {
			case tt.limitGot != 0:
				le, ok := limit.As(err)
				require.True(t, ok, "want limit refusal, got %v", err)
				require.Equal(t, limit.Exceeded("lookup keys", tt.limitGot, rdb.MaxLookupKeys), le)
				require.Equal(t, limit.Code, le.Extensions()["code"])
				require.Nil(t, found)
			case tt.unknown != "":
				_, modelErr := api.ExistingEntityTokens(ctx, tt.unknown, []string{"missing"})
				require.EqualError(t, err, modelErr.Error())
				require.Nil(t, found)
			default:
				require.NoError(t, err)
				require.Empty(t, found)
			}
			require.Equal(t, tt.wantQueries, queries, "refused inputs must not run any per-type lookup query")
		})
	}
}
