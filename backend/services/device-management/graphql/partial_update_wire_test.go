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

// THE WIRE HALF of the partial-update guarantee, for every converted mutation.
//
// The guarantees split across three layers, and the split is worth stating because
// no one of them is sufficient:
//
//   - THE SHAPE OF THE INPUT is only observable here, against the real schema:
//     that `token` is not a member of the update input, and that `request` is
//     required. Both are rejections the schema performs before any resolver runs,
//     so a model test cannot see them.
//   - THE THREE STATES REACHING STORAGE live in the model harness
//     (model/partial_update_harness_test.go), which drives the real Api against a
//     real database.
//   - THAT THE THREE STATES SURVIVE THE WIRE AT ALL is proved once, generically,
//     in core: graphql.TestOptionalStringCarriesThreeStates executes a real schema
//     and asserts absent/null/value arrive distinguishably. Every field on every
//     one of these inputs is an OptionalString, so that proof carries here by
//     construction rather than by repetition.
//
// 🔴 The one case NOT covered end to end in a single test is a full wire-to-storage
// update of a device type, because that resolver routes through GetCachedApi and
// *model.CachedApi requires a live NATS KV for its eviction. Building one here would
// mean an embedded JetStream server per test; the seam left uncovered is the
// resolver's two-line hand-off, which the layers above bracket on either side.

// partialUpdateMutations is every mutation carrying the partial-update semantic,
// with the input type its `request` argument takes. Converting a family adds a row.
var partialUpdateMutations = []struct {
	mutation string
	input    string
}{
	{"updateDeviceType", "DeviceTypeUpdateRequest"},
	{"updateAssetType", "AssetTypeUpdateRequest"},
	{"updateCustomerType", "CustomerTypeUpdateRequest"},
	{"updateAreaType", "AreaTypeUpdateRequest"},
	{"updateAsset", "AssetUpdateRequest"},
	{"updateCustomer", "CustomerUpdateRequest"},
	{"updateArea", "AreaUpdateRequest"},
	{"updateDevice", "DeviceUpdateRequest"},
}

func newPartialUpdateWireCtx(t *testing.T) context.Context {
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
		&model.DetectionRule{}, &model.DetectionRuleScopeRef{},
		&model.AssetType{}, &model.Asset{}, &model.CustomerType{}, &model.Customer{},
		&model.AreaType{}, &model.Area{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	ctx = withAuthorities(ctx, auth.DeviceRead, auth.DeviceWrite)
	return context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
}

// The token is the mutation's own argument and is deliberately not a member of any
// update input, so moving an entity's token is UNREPRESENTABLE rather than merely
// refused. It also closes the defect these conversions were built on: seven of
// these mutations used to locate the row by the PAYLOAD token and ignore the
// argument, so a request whose two tokens disagreed silently updated the other row.
//
// The rejection arrives from the unknown-input-field guard, which makes this a
// check on that guard too: it must still cover a converted input, where a silently
// dropped field would tell the caller their update succeeded.
func TestPartialUpdateInputsCannotCarryAToken(t *testing.T) {
	for _, m := range partialUpdateMutations {
		t.Run(m.mutation, func(t *testing.T) {
			ctx := newPartialUpdateWireCtx(t)
			schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
			res := schema.Exec(ctx, updateMutationDoc(m.mutation, m.input), "", map[string]any{
				"token":   "whatever",
				"request": map[string]any{"token": "moved"},
			})
			if len(res.Errors) == 0 {
				t.Fatal("a token field on the update input was accepted; either it was " +
					"re-added to the input, or an undeclared field is being silently dropped again")
			}
		})
	}
}

// `request` is non-null, so a caller who sends nothing gets a request error rather
// than a silently successful no-op that returns the entity unchanged. Four of these
// arguments were NULLABLE before the conversion (`request: DeviceCreateRequest`),
// which is a different way of spelling the same fail-open.
func TestPartialUpdateRequiresARequest(t *testing.T) {
	for _, m := range partialUpdateMutations {
		t.Run(m.mutation, func(t *testing.T) {
			ctx := newPartialUpdateWireCtx(t)
			schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
			res := schema.Exec(ctx, `mutation($token: String!) {
			  `+m.mutation+`(token: $token, request: null) { token }
			}`, "", map[string]any{"token": "whatever"})
			if len(res.Errors) == 0 {
				t.Fatal("a null request was accepted")
			}
		})
	}
}

// THE COUNTERWEIGHT, and it is the reason the two rejections above mean anything.
// They are only safe while a well-formed partial update still parses and reaches
// the resolver. Without this, renaming an input or mistyping a field name would
// make both tests above pass for the wrong reason — every request rejected, the
// guarantee "held" vacuously.
func TestPartialUpdateAcceptsAWellFormedPartialRequest(t *testing.T) {
	for _, m := range partialUpdateMutations {
		t.Run(m.mutation, func(t *testing.T) {
			ctx := newPartialUpdateWireCtx(t)
			schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
			res := schema.Exec(ctx, updateMutationDoc(m.mutation, m.input), "", map[string]any{
				"token":   "whatever",
				"request": map[string]any{"name": "Renamed"},
			})
			// The resolver is reached and fails on the missing row (or, for device
			// types, on the caching decorator this test cannot build) — either way the
			// REQUEST was accepted. What must not happen is a validation error naming
			// the input or one of its fields, which arrives with a nil path.
			for _, e := range res.Errors {
				if e.Path == nil {
					t.Fatalf("a well-formed partial update was rejected before the resolver: %v", e)
				}
			}
		})
	}
}

func updateMutationDoc(mutation, input string) string {
	return `mutation($token: String!, $request: ` + input + `!) {
	  ` + mutation + `(token: $token, request: $request) { token name }
	}`
}
