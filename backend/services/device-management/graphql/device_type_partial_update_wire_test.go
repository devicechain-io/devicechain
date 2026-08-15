// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"
)

// updateDeviceType is the first mutation converted to the platform-wide PARTIAL
// update semantic. The guarantees split across two layers, and the split is worth
// stating because neither half is sufficient alone:
//
//   - THE SHAPE OF THE INPUT is only observable here, against the real schema:
//     that `token` is not a member of the update input, and that `request` is
//     required. Both are rejections the schema performs before any resolver runs,
//     so a model test cannot see them.
//   - THE THREE STATES REACHING STORAGE live in
//     model/api_device_type_partial_update_test.go, which drives the real Api
//     against a real database.
//   - THAT THE THREE STATES SURVIVE THE WIRE AT ALL is proved once, generically,
//     in core: graphql.TestOptionalStringCarriesThreeStates executes a real schema
//     and asserts absent/null/value arrive distinguishably. Every field on
//     DeviceTypeUpdateRequest is an OptionalString, so that proof carries here by
//     construction rather than by repetition.
//
// 🔴 The one case NOT covered end to end in a single test is a full wire-to-storage
// update, because the resolver routes through GetCachedApi and *model.CachedApi
// requires a live NATS KV for its eviction. Building one here would mean an
// embedded JetStream server per test; the seam left uncovered is the resolver's
// two-line hand-off, which the two layers above bracket on either side.

func newDeviceTypeWireCtx(t *testing.T) context.Context {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.Device{}, &model.DeviceType{}, &model.DeviceProfile{},
		&model.DeviceProfileVersion{}, &model.MetricDefinition{}, &model.CommandDefinition{},
		&model.DetectionRule{}, &model.DetectionRuleScopeRef{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	ctx = withAuthorities(ctx, auth.DeviceRead, auth.DeviceWrite)
	return context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
}

const deviceTypeUpdateMutation = `mutation($token: String!, $request: DeviceTypeUpdateRequest!) {
  updateDeviceType(token: $token, request: $request) { token name }
}`

// The token is the mutation's own argument and is deliberately not a member of the
// update input, so moving a type's token is UNREPRESENTABLE rather than merely
// refused. The rejection arrives from the unknown-input-field guard, which makes
// this a check on that guard too: it must still cover a converted input, where a
// silently dropped field would tell the caller their update succeeded.
func TestUpdateDeviceTypeCannotMoveTheToken(t *testing.T) {
	ctx := newDeviceTypeWireCtx(t)

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, deviceTypeUpdateMutation, "", map[string]any{
		"token":   "sensor",
		"request": map[string]any{"token": "moved"},
	})
	if len(res.Errors) == 0 {
		t.Fatal("a token field on the update input was accepted; either it was re-added " +
			"to the input, or an undeclared field is being silently dropped again")
	}
}

// `request` is non-null, so a caller who sends nothing gets a request error rather
// than a silently successful no-op that returns the entity unchanged.
func TestUpdateDeviceTypeRequiresARequest(t *testing.T) {
	ctx := newDeviceTypeWireCtx(t)

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, `mutation($token: String!) {
	  updateDeviceType(token: $token, request: null) { token }
	}`, "", map[string]any{"token": "sensor"})
	if len(res.Errors) == 0 {
		t.Fatal("a null request was accepted")
	}
}

// The counterweight: the two rejections above are only safe while a well-formed
// partial update still parses and reaches the resolver. Without this, renaming the
// input or mistyping a field name would make both tests above pass for the wrong
// reason — every request rejected, guarantee "held" vacuously.
func TestUpdateDeviceTypeAcceptsAWellFormedPartialRequest(t *testing.T) {
	ctx := newDeviceTypeWireCtx(t)

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, deviceTypeUpdateMutation, "", map[string]any{
		"token":   "sensor",
		"request": map[string]any{"name": "Renamed"},
	})
	// The resolver is reached and fails on the caching decorator this test cannot
	// build, or on the missing row — either way the REQUEST was accepted. What must
	// not happen is a validation error naming the input or its fields.
	for _, e := range res.Errors {
		if e.Path == nil {
			t.Fatalf("a well-formed partial update was rejected before the resolver: %v", e)
		}
	}
}
