// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const scopeAnchorsQuery = `query($tokens: [String!]!) {
  deviceScopeAnchors(deviceTokens: $tokens) { deviceToken anchors }
}`

func scopeAnchorsWireCtx(t *testing.T) context.Context {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.Device{}, &model.EntityRelationship{}, &model.PendingOwnershipFact{}, &model.EntityRelationshipType{},
		&model.EntityGroup{}, &model.EntityGroupMembership{}))
	ctx := core.WithTenant(context.Background(), "acme")
	api := model.NewApi(&rdb.RdbManager{Database: db})
	for _, token := range []string{"d1", "d2"} {
		d := model.Device{}
		d.Token, d.TenantId = token, "acme"
		require.NoError(t, db.WithContext(ctx).Create(&d).Error)
		if token != "d1" {
			continue
		}
		rt := model.EntityRelationshipType{Tracked: true}
		rt.Token, rt.TenantId = "assigned", "acme"
		require.NoError(t, db.WithContext(ctx).Create(&rt).Error)
		r := model.EntityRelationship{SourceType: string(entity.TypeDevice), SourceId: d.ID,
			TargetType: "customer", TargetId: 1, TargetToken: "acme-corp", RelationshipTypeId: rt.ID}
		r.Token, r.TenantId = "r1", "acme"
		require.NoError(t, db.WithContext(ctx).Create(&r).Error)
	}
	return context.WithValue(ctx, gqlcore.ContextApiKey, api)
}

// The service-plane read command-delivery and update-management stamp their rows from,
// through the real schema with the tokens sent as a variable: request order, duplicates
// collapsed, a device that does not exist omitted rather than answered with no anchors
// (which would read as "a device nobody owns").
func TestDeviceScopeAnchorsCrossTheWire(t *testing.T) {
	ctx := scopeAnchorsWireCtx(t)
	ctx = auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeService,
		Authorities: []string{string(auth.DeviceRead)}})
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, scopeAnchorsQuery, "", map[string]any{"tokens": []any{"d2", "ghost", "d1", "d2"}})
	require.Empty(t, res.Errors)
	var out struct {
		DeviceScopeAnchors []struct {
			DeviceToken string   `json:"deviceToken"`
			Anchors     []string `json:"anchors"`
		} `json:"deviceScopeAnchors"`
	}
	require.NoError(t, json.Unmarshal(res.Data, &out))
	require.Len(t, out.DeviceScopeAnchors, 2)
	require.Equal(t, "d2", out.DeviceScopeAnchors[0].DeviceToken)
	require.Equal(t, []string{}, out.DeviceScopeAnchors[0].Anchors)
	require.Equal(t, "d1", out.DeviceScopeAnchors[1].DeviceToken)
	require.Equal(t, []string{"customer:acme-corp"}, out.DeviceScopeAnchors[1].Anchors)
}

// 🔴 A member limited to part of the tenant is refused this read outright, holding
// device:read or not: a device may belong to several customers, and its anchors would name
// the others. The refusal is the scope one, not a missing authority.
func TestDeviceScopeAnchorsRefusesAScopedMember(t *testing.T) {
	ctx := scopeAnchorsWireCtx(t)
	ctx = auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.DeviceRead)}, ScopeAnchors: []string{"customer:acme-corp"}})
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, scopeAnchorsQuery, "", map[string]any{"tokens": []any{"d1"}})
	require.Len(t, res.Errors, 1)
	require.True(t, errors.Is(res.Errors[0].ResolverError, auth.ErrScopeUnsupported),
		"want the scope refusal, got %v", res.Errors[0])
	require.Equal(t, "null", string(res.Data))
}

// 🔴 The refusal above must be the RESOLVER's, not the storage layer's. Today a resolver
// that admitted a scoped member would still be refused by core/rdb, because Device declares
// no scope rule — so errors.Is alone cannot tell Authorize from AuthorizeScoped, and the day
// Device opts scoped callers in (device reads, part 3) that accidental cover is gone: a
// member owning d1 would then be handed its anchors, other customers' tokens included. The
// two refusals differ in their words — the resolver's is the bare sentinel, storage's names
// the table it refused — and that is what this test holds, so it keeps killing the admitting
// mutant whatever rules the models come to declare.
func TestDeviceScopeAnchorsRefusesAScopedMemberAtTheResolver(t *testing.T) {
	ctx := scopeAnchorsWireCtx(t)
	ctx = auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.DeviceRead)}, ScopeAnchors: []string{"customer:acme-corp"}})
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, scopeAnchorsQuery, "", map[string]any{"tokens": []any{"d1"}})
	require.Len(t, res.Errors, 1)
	require.Equal(t, auth.ErrScopeUnsupported.Error(), res.Errors[0].Message,
		"the scoped member was not refused by the resolver itself")

	// Contrast: what the storage layer says when it is the one refusing. It is only a
	// contrast — the model may one day admit the read — so it asserts nothing about the
	// model, only that a refusal from below never reads like the resolver's.
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	if _, err := api.DeviceScopeAnchors(ctx, []string{"d1"}); err != nil {
		require.NotEqual(t, auth.ErrScopeUnsupported.Error(), err.Error())
	}
}
