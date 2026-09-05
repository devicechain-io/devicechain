// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
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
//     in core: graphql.TestOptionalStringCarriesThreeStates and
//     TestOptionalScalarsCarryThreeStates execute a real schema and assert
//     absent/null/value arrive distinguishably. Every field on every one of these
//     inputs is an Optional*, and core covers all five, so that proof carries here by
//     construction rather than by repetition. (It said "an OptionalString" until five
//     of these inputs grew an OptionalBool, OptionalInt32 or OptionalFloat64 — the
//     claim was still true of the coverage and had stopped being true of the inputs.)
//
// 🔴 The one case NOT covered end to end in a single test is a full wire-to-storage
// update of a device type, because that resolver routes through GetCachedApi and
// *model.CachedApi requires a live NATS KV for its eviction. Building one here would
// mean an embedded JetStream server per test; the seam left uncovered is the
// resolver's two-line hand-off, which the layers above bracket on either side.

type partialUpdateMutation struct {
	mutation  string
	input     string
	selection string
	probe     map[string]any
	forbidden []string
}

// partialUpdateMutations is every mutation carrying the partial-update semantic, with
// the input type its `request` argument takes. Converting a family adds a row — and
// TestEveryUpdateMutationIsCoveredByTheWireTests checks this list against the schema in
// both directions, so a family converted without a row here fails rather than going
// uncovered.
//
// selection is what the mutation asks for back, and probe is a well-formed field of the
// input. Both default to name, which most of these carry; the ones that do not say so,
// because a document naming a field the schema does not have would be rejected before
// the resolver and make the counterweight test pass for exactly the wrong reason.
//
// forbidden lists the fields deliberately ABSENT from the input, beyond `token` (which
// every one of them omits and which is checked for all). A field here is one whose only
// possible requests are a no-op or an error — an identity column, or a vocabulary with a
// single legal value — so the input has nowhere to write it and the schema says so.
var partialUpdateMutations = []partialUpdateMutation{
	{mutation: "updateDeviceType", input: "DeviceTypeUpdateRequest"},
	{mutation: "updateAssetType", input: "AssetTypeUpdateRequest"},
	{mutation: "updateCustomerType", input: "CustomerTypeUpdateRequest"},
	{mutation: "updateAreaType", input: "AreaTypeUpdateRequest"},
	{mutation: "updateAsset", input: "AssetUpdateRequest"},
	{mutation: "updateCustomer", input: "CustomerUpdateRequest"},
	{mutation: "updateArea", input: "AreaUpdateRequest"},
	{mutation: "updateDevice", input: "DeviceUpdateRequest"},
	{mutation: "updateMetricDefinition", input: "MetricDefinitionUpdateRequest"},
	{mutation: "updateCommandDefinition", input: "CommandDefinitionUpdateRequest"},
	{mutation: "updateDetectionRule", input: "DetectionRuleUpdateRequest"},
	{mutation: "updateGeoFence", input: "GeoFenceUpdateRequest"},
	{
		mutation: "updateEntityGroup", input: "EntityGroupUpdateRequest",
		// An entity group's member family and membership mode are identity: changing
		// either would leave the group collecting a family its members do not belong to,
		// or orphan them across a static/dynamic conversion.
		forbidden: []string{"memberType", "membershipMode"},
	},
	{
		mutation: "updateDeviceCredential", input: "DeviceCredentialUpdateRequest",
		// A credential has no name — the entity is identified by its token and resolved
		// by the id a device presents.
		selection: "token credentialId",
		probe:     map[string]any{"credentialId": "a-rotated-bearer"},
	},
	{
		mutation: "updateProvisioningProfile", input: "ProvisioningProfileUpdateRequest",
		// Provisioning mints exactly one credential type today, so naming the field could
		// only restate what is stored or be refused.
		forbidden: []string{"credentialType"},
	},
	{mutation: "updateEntityRelationshipType", input: "EntityRelationshipTypeUpdateRequest"},
}

func (m partialUpdateMutation) selectionOrDefault() string {
	if m.selection != "" {
		return m.selection
	}
	return "token name"
}

func (m partialUpdateMutation) probeOrDefault() map[string]any {
	if m.probe != nil {
		return m.probe
	}
	return map[string]any{"name": "Renamed"}
}

// 🔴 THE TEST NAME IS SANITIZED BEFORE IT GOES IN A URI, AND IT WAS NOT.
//
// This built the DSN from a raw t.Name(), which carries "/" for every subtest and "#01",
// "#02" … whenever one name repeats within a run — and a URI reads "#" as the start of a
// FRAGMENT. So the database name silently truncates at the "#" and sqlite falls back to
// an ON-DISK FILE in the working directory: a fixture that writes real files, shares
// state with every other test truncating to the same prefix, and outlives the run. It
// took no failure to be wrong, which is why it needed looking for rather than waiting for.
//
// 🔴 AND THE POOL IS CLOSED, for the reason spelled out in the model harness: a
// shared-cache in-memory database lives while a connection to it is open, gorm closes
// none, and the next test with the same name inherits the rows.
func newPartialUpdateWireCtx(t *testing.T) context.Context {
	t.Helper()
	dsn := "file:" + wireDSNName(t) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, cerr := db.DB()
		if cerr != nil {
			t.Errorf("reach the underlying pool to close it: %v", cerr)
			return
		}
		if cerr := sqlDB.Close(); cerr != nil {
			t.Errorf("close the fixture's pool: %v — the shared-cache database outlives this "+
				"test and the next one to reuse its name inherits these rows", cerr)
		}
	})
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.Device{}, &model.DeviceType{}, &model.DeviceProfile{},
		&model.DeviceProfileVersion{}, &model.MetricDefinition{}, &model.CommandDefinition{},
		&model.DetectionRule{}, &model.DetectionRuleScopeRef{},
		&model.AssetType{}, &model.Asset{}, &model.CustomerType{}, &model.Customer{},
		&model.AreaType{}, &model.Area{}, &model.GeoFence{}, &model.GeoFenceSetVersion{},
		&model.EntityGroup{}, &model.DeviceCredential{}, &model.ProvisioningProfile{},
		&model.EntityRelationshipType{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	// location:read joins the list because fence AUTHORING is gated on it as well as on
	// device:write — a caller without it is refused before the resolver runs, which would
	// make the counterweight test below read an authorization failure as a rejected
	// request.
	ctx = withAuthorities(ctx, auth.DeviceRead, auth.DeviceWrite, auth.LocationRead)
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
			assertFieldRejectedBeforeTheResolver(t, m, "token", "moved")
		})
	}
}

// 🔴 THE REJECTION HAS TO BE A REQUEST ERROR, AND CHECKING ONLY "there was an error"
// IS A FAIL-OPEN. This test used to read `len(res.Errors) == 0` and fail on that
// alone. Every one of these mutations is addressed to a token that names no row, so
// the resolver ALSO errors — which means the assertion was satisfied by the
// not-found, whether or not the schema had rejected anything. A mutant that re-added
// `token` to an update input in both the SDL and the Go struct sailed straight
// through it; what killed the mutant was the model-side declaration guard, and only
// because that guard exists.
//
// A request error — a validation failure, which is what an undeclared field produces
// — arrives with a nil Path, because it happened before any field was resolved. A
// resolver error carries the path of the field that failed. That distinction is what
// separates "the schema refused this" from "the schema accepted it and the row was
// missing", and it is the whole assertion.
func assertFieldRejectedBeforeTheResolver(t *testing.T, m partialUpdateMutation, field string, value any) {
	t.Helper()
	ctx := newPartialUpdateWireCtx(t)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, updateMutationDoc(m.mutation, m.input, m.selectionOrDefault()), "",
		map[string]any{
			"token":   "whatever",
			"request": map[string]any{field: value},
		})
	for _, e := range res.Errors {
		if e.Path == nil {
			return // a request error: the schema refused the field, which is the claim
		}
	}
	t.Fatalf("%s on %s reached the resolver instead of being refused by the schema (errors: %v) — "+
		"either the field was re-added to the input, or an undeclared field is being silently "+
		"dropped again", field, m.input, res.Errors)
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
			// A REQUEST error, for the same reason as the token check above: these
			// mutations are addressed to a token that names no row, so the resolver
			// errors too, and "there was an error" would be satisfied by the not-found
			// whether or not the null was refused.
			refused := false
			for _, e := range res.Errors {
				if e.Path == nil {
					refused = true
				}
			}
			if !refused {
				t.Fatalf("a null request reached the resolver instead of being refused by the "+
					"schema (errors: %v)", res.Errors)
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
			res := schema.Exec(ctx, updateMutationDoc(m.mutation, m.input, m.selectionOrDefault()), "", map[string]any{
				"token":   "whatever",
				"request": m.probeOrDefault(),
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

// 🔴 THE LIST ABOVE IS CHECKED AGAINST THE SCHEMA, BECAUSE A LIST IS NOT A GUARD.
//
// partialUpdateMutations is hand-written, and everything else in this file iterates it —
// so a row DELETED from it takes its mutation out of every check here and leaves the
// suite green. That is not a hypothetical: deleting updateEntityRelationshipType from it
// passed the whole package. The conversion would still be in the schema, still be in the
// model, and be certified by nothing on the wire.
//
// So the set of `update*` mutations is derived from the SDL — with the SERVER'S OWN
// introspection rather than a regex over the file, which is the same rule rather than an
// approximation of it — and every one must be listed or NAMED as an exemption. A
// mutation added tomorrow fails here on the day it is added; a row removed fails
// immediately.
func TestEveryUpdateMutationIsCoveredByTheWireTests(t *testing.T) {
	// The mutations deliberately outside the partial-update contract. Each is a full
	// replace for a stated reason, and naming it here is what makes the residual
	// countable instead of inferred.
	exempt := map[string]string{
		"updateDeviceProfile": "its payload token is a RENAME channel — a profile may be " +
			"renamed while nothing has adopted or published it — so a dedicated input dropping " +
			"the token would delete a capability rather than convert one",
	}

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	mutationType := schema.Inspect().MutationType()
	if mutationType == nil {
		t.Fatal("the schema declares no Mutation type; this guard is reading nothing")
	}
	fields := mutationType.Fields(&struct{ IncludeDeprecated bool }{})
	if fields == nil || len(*fields) == 0 {
		t.Fatal("the Mutation type reports no fields; this guard is reading nothing")
	}

	listed := map[string]bool{}
	for _, m := range partialUpdateMutations {
		listed[m.mutation] = true
	}

	found := 0
	for _, f := range *fields {
		if !strings.HasPrefix(f.Name(), "update") {
			continue
		}
		found++
		if listed[f.Name()] {
			continue
		}
		if _, ok := exempt[f.Name()]; ok {
			continue
		}
		t.Errorf("%s is an update mutation the schema serves, but it is in neither "+
			"partialUpdateMutations nor the exemption list — so nothing in this file sends it "+
			"a token, an identity field or a well-formed request", f.Name())
	}

	// The other direction: a row naming a mutation the schema no longer serves would
	// have every test in this file silently skip it (schema.Exec fails on an unknown
	// field, but the counterweight test only rejects errors with a nil path).
	served := map[string]bool{}
	for _, f := range *fields {
		served[f.Name()] = true
	}
	for _, m := range partialUpdateMutations {
		if !served[m.mutation] {
			t.Errorf("partialUpdateMutations lists %q, which the schema does not serve", m.mutation)
		}
	}
	for name := range exempt {
		if !served[name] {
			t.Errorf("the exemption names %q, which the schema does not serve — an exemption "+
				"that no longer describes the code is worse than none", name)
		}
	}

	// The anti-vacuity control. An introspection call that returned an empty or
	// prefix-less field set would certify everything.
	if found < 15 {
		t.Fatalf("only %d update* mutations were found in the schema; this guard has stopped "+
			"seeing the surface it certifies", found)
	}
}

func updateMutationDoc(mutation, input, selection string) string {
	return `mutation($token: String!, $request: ` + input + `!) {
	  ` + mutation + `(token: $token, request: $request) { ` + selection + ` }
	}`
}

// 🔴 A FIELD LEFT OUT OF AN UPDATE INPUT IS A DESIGN DECISION, AND THIS IS WHERE IT IS
// ENFORCED. Three fields were removed rather than converted — an entity group's
// memberType and membershipMode, a provisioning profile's credentialType — because under
// three-state semantics each could only ever be a no-op or an error. Each removal
// replaced a model-layer guard that refused the change, and a guard's test can go on
// passing while the guard has become unreachable; an absent field cannot.
//
// The rejection arrives from the unknown-input-field guard, exactly as it does for
// `token`, which makes this a check on that guard too: a silently dropped field would
// tell the caller their identity change succeeded.
func TestPartialUpdateInputsOmitTheirIdentityFields(t *testing.T) {
	drove := 0
	for _, m := range partialUpdateMutations {
		for _, field := range m.forbidden {
			drove++
			t.Run(m.mutation+"/"+field, func(t *testing.T) {
				assertFieldRejectedBeforeTheResolver(t, m, field, "anything")
			})
		}
	}
	// The anti-vacuity control. A table whose `forbidden` entries were all dropped in a
	// refactor would leave this test green having asserted nothing at all.
	if drove == 0 {
		t.Fatal("no forbidden field was driven; this test is asserting nothing")
	}
}

// wireDSNName turns a test's name into something safe to sit in a SQLite URI.
//
// It duplicates partialUpdateDSNName from the model harness rather than importing it:
// that one lives in a _test.go file, which Go does not export across packages, and
// promoting it to non-test code would ship a test helper in the service binary. The
// duplication is a few lines and the comment on newPartialUpdateWireCtx says what it is
// for, which is a better trade than either alternative.
func wireDSNName(t *testing.T) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, t.Name())
}
