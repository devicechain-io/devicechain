// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

type concurrencySnapshot struct {
	Name      *string
	UpdatedAt time.Time
}

// Exercise the served schema and the real tenant-scoped store for every entity.
func TestEntityExpectedUpdatedAt(t *testing.T) {
	for _, tc := range []struct {
		entity string
		table  string
		record func(string) any
	}{
		{"Area", "areas", func(token string) any { return &model.Area{TokenReference: rdb.TokenReference{Token: token}} }},
		{"Asset", "assets", func(token string) any { return &model.Asset{TokenReference: rdb.TokenReference{Token: token}} }},
		{"Customer", "customers", func(token string) any { return &model.Customer{TokenReference: rdb.TokenReference{Token: token}} }},
		{"EntityGroup", "entity_groups", func(token string) any {
			return &model.EntityGroup{TokenReference: rdb.TokenReference{Token: token}, MemberType: "device", MembershipMode: "static"}
		}},
	} {
		t.Run(tc.entity, func(t *testing.T) {
			ctx := newPartialUpdateWireCtx(t)
			other := core.WithTenant(ctx, "other")
			api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
			db := api.RDB.Database
			now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
			db.NowFunc = func() time.Time { return now }
			seed := func(ctx context.Context, token string) {
				require.NoError(t, api.RDB.DB(ctx).Create(tc.record(token)).Error)
			}
			read := func(ctx context.Context, token string) concurrencySnapshot {
				var row concurrencySnapshot
				require.NoError(t, api.RDB.DB(ctx).Model(tc.record(token)).Where("token = ?", token).Take(&row).Error)
				return row
			}
			seed(ctx, "shared")
			now = now.Add(time.Hour)
			seed(other, "shared")
			seed(other, "foreign-only")
			now = now.Add(-time.Hour) // a frozen, lagging clock must still advance the version
			foreign := read(other, "shared")
			schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
			mutation := "update" + tc.entity
			exec := func(ctx context.Context, token string, expected *string, request map[string]any) *gql.Response {
				vars := map[string]any{"token": token, "request": request}
				decl, arg := "", ""
				if expected != nil {
					decl, arg = ", $expected: String", ", expectedUpdatedAt: $expected"
					vars["expected"] = *expected
				}
				doc := "mutation($token: String!, $request: " + tc.entity + "UpdateRequest!" + decl + ") { " +
					mutation + "(token: $token, request: $request" + arg + ") { name updatedAt } }"
				return schema.Exec(ctx, doc, "", vars)
			}
			assertStale := func(res *gql.Response) {
				require.Len(t, res.Errors, 1)
				require.Equal(t, "STALE_WRITE", res.Errors[0].Extensions["code"])
				require.Contains(t, res.Errors[0].Message, "modified by another writer")
			}
			assertSuccess := func(res *gql.Response, want concurrencySnapshot) {
				require.Empty(t, res.Errors)
				var data map[string]struct {
					Name      *string
					UpdatedAt string
				}
				require.NoError(t, json.Unmarshal(res.Data, &data))
				require.Equal(t, want.Name, data[mutation].Name)
				require.Equal(t, want.UpdatedAt.Format(time.RFC3339Nano), data[mutation].UpdatedAt)
			}

			initial := read(ctx, "shared").UpdatedAt.Format(time.RFC3339Nano)
			res := exec(ctx, "shared", &initial, map[string]any{"name": "winner"})
			require.Empty(t, res.Errors)
			winner := read(ctx, "shared")
			require.Equal(t, "winner", *winner.Name)
			require.NotEqual(t, initial, winner.UpdatedAt.Format(time.RFC3339Nano))
			assertSuccess(res, winner)
			for _, stale := range []string{initial, "", "invalid", foreign.UpdatedAt.Format(time.RFC3339Nano)} {
				for _, request := range []map[string]any{{"name": "loser"}, {}} {
					assertStale(exec(ctx, "shared", &stale, request))
					require.Equal(t, winner, read(ctx, "shared"))
				}
			}
			matching := winner.UpdatedAt.Format(time.RFC3339Nano)
			assertSuccess(exec(ctx, "shared", &matching, map[string]any{}), winner)
			res = exec(ctx, "shared", nil, map[string]any{"name": "legacy"})
			require.Empty(t, res.Errors)
			legacy := read(ctx, "shared")
			require.Equal(t, "legacy", *legacy.Name)
			require.True(t, legacy.UpdatedAt.After(winner.UpdatedAt))
			assertSuccess(res, legacy)
			assertStale(exec(ctx, "shared", &matching, map[string]any{"name": "stale after legacy"}))
			matching = legacy.UpdatedAt.Format(time.RFC3339Nano)
			res = exec(ctx, "shared", &matching, map[string]any{"name": nil})
			require.Empty(t, res.Errors)
			require.Nil(t, read(ctx, "shared").Name)
			require.Equal(t, foreign, read(other, "shared"), "updates must leave the same token in another tenant untouched")
			foreignVersion := foreign.UpdatedAt.Format(time.RFC3339Nano)
			res = exec(other, "shared", &foreignVersion, map[string]any{"name": "other tenant"})
			require.Empty(t, res.Errors)
			require.Nil(t, read(ctx, "shared").Name)
			foreignOnly := read(other, "foreign-only")
			foreignVersion = foreignOnly.UpdatedAt.Format(time.RFC3339Nano)
			for _, expected := range []*string{nil, &foreignVersion} {
				res = exec(ctx, "foreign-only", expected, map[string]any{"name": "intruder"})
				require.Len(t, res.Errors, 1)
				require.Contains(t, res.Errors[0].Message, "record not found")
				require.Equal(t, foreignOnly, read(other, "foreign-only"))
			}

		})
	}
}
