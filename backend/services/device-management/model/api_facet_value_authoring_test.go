// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// 🔴 THIS FILE IS THE OTHER HALF OF A PROOF, AND ON ITS OWN IT IS NOT THE INTERESTING
// HALF.
//
// The console can now give a declared facet a value
// (frontend/apps/console/src/components/EntityAttributesPanel.tsx). That panel's own test
// proves what request LEAVES it — the scope and the value type it puts on the wire. It
// cannot prove those are the RIGHT ones, because the thing that decides is a SQL semi-join
// three services away, and the failure when they are wrong is not an error: the write
// succeeds, the value reads back, and Browse says "matches 0" behind a screen that looks
// correct.
//
// So this file takes the same three strings — the scope, the declared value type, and the
// CEL the console's selector composer emits — and runs them end to end: author a value the
// way the panel authors it, then ask PreviewSelector (the exact query behind Browse's live
// "matches N") whether the entity is in the result.
//
// Every negative control below exists because it is a way to write a value that is VALID,
// SAVES, AND MATCHES NOTHING:
//
//   - the wrong SCOPE — a CLIENT/SERVER row the lowering never reads;
//   - the wrong VALUE TYPE — "3" as STRING against a LONG axis;
//   - a value the numeric type cannot hold — the server COERCES it to unset rather than
//     refusing, so the mutation succeeds and stores nothing;
//   - clearing by writing "" instead of deleting the row.
//
// Without them a passing positive arm would say only "some attribute somewhere matched",
// which is the assertion a filter that matches everything also satisfies.

// consoleFacetScope is the scope literal the console writes, copied here on purpose rather
// than taken from AttributeScopeShared: a fixture derived from the constant it is checking
// cannot notice the constant moving. It is the same string as
// FACET_VALUE_SCOPE in frontend/apps/console/src/lib/api/entity-attributes.ts.
const consoleFacetScope = "SHARED"

// authoredSelector is the CEL the console's buildSelector composes for the value authored
// below — pinned identically in EntityAttributesPanel.test.tsx, which asserts the composer
// still emits exactly this. Written separately at the two ends deliberately: if either end
// drifts, one of the two tests is proving something about a string nobody sends.
const authoredSelector = `attr["climate"] == "arid"`

// newFacetAuthoringTestApi builds an Api over the tables the authoring→browse path spans:
// the member family, the facet-key registry the panel reads its declarations from, the
// attribute store the values land in, and the group/membership tables the selector engine
// touches.
func newFacetAuthoringTestApi(t *testing.T) (*Api, context.Context) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&Device{}, &DeviceType{}, &DeviceProfile{}, &DeviceProfileVersion{},
		&MetricDefinition{}, &CommandDefinition{}, &DetectionRule{}, &EntityAttribute{},
		&FacetKey{}, &EntityGroup{}, &EntityGroupVersion{}, &EntityGroupMembership{},
		&EntityGroupFacetRef{}, &DetectionRuleScopeRef{},
		&EntityRelationship{}, &EntityRelationshipType{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	api := NewApi(&rdb.RdbManager{Database: db})
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "sensor"}); err != nil {
		t.Fatalf("seed device type: %v", err)
	}
	return api, ctx
}

// declareFacet registers a classification axis the way the Facets screen does, and returns
// the declaration — the panel reads ValueType off exactly this row rather than guessing a
// type from the text the operator typed, so the test does too.
func declareFacet(t *testing.T, api *Api, ctx context.Context, key, valueType string) *FacetKey {
	t.Helper()
	fk, err := api.SetFacetKey(ctx, &FacetKeySetRequest{
		MemberType: entity.TypeDevice.String(), Key: key, ValueType: valueType,
	})
	if err != nil {
		t.Fatalf("declare facet %s: %v", key, err)
	}
	return fk
}

// newDevice creates a bare member entity with no attributes at all.
func newDevice(t *testing.T, api *Api, ctx context.Context, token string) {
	t.Helper()
	if _, err := api.CreateDevice(ctx, &DeviceCreateRequest{
		Token: token, DeviceTypeToken: "sensor",
	}); err != nil {
		t.Fatalf("create device %s: %v", token, err)
	}
}

// authorValue writes an attribute the way the panel's setFacetValue does. Scope and value
// type are PARAMETERS rather than constants so a negative control can supply the wrong one
// and the difference is visible at the call site — which is the only place in this file
// where a wrong answer is written down on purpose.
func authorValue(t *testing.T, api *Api, ctx context.Context,
	token, scope, key, valueType, value string) {
	t.Helper()
	if _, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
		EntityType: entity.TypeDevice.String(), Entity: token,
		Scope: scope, AttrKey: key, ValueType: valueType, Value: &value,
	}); err != nil {
		t.Fatalf("author %s=%s on %s: %v", key, value, token, err)
	}
}

// browseMatches runs the exact query behind Browse's live "matches N" — PreviewSelector,
// the unsaved-candidate path a saved dynamic group shares — and returns the member tokens.
func browseMatches(t *testing.T, api *Api, ctx context.Context, selector string) []string {
	t.Helper()
	res, err := api.PreviewSelector(ctx, entity.TypeDevice.String(), selector,
		rdb.Pagination{PageNumber: 1, PageSize: 100})
	if err != nil {
		t.Fatalf("preview %q: %v", selector, err)
	}
	tokens := make([]string, 0, len(res.Results))
	for _, m := range res.Results {
		tokens = append(tokens, m.Token)
	}
	return tokens
}

// A value authored the way the console authors it appears in Browse — and each of the four
// ways to author a value that saves cleanly and matches nothing does not.
func TestConsoleAuthoredFacetValueMatchesInBrowse(t *testing.T) {
	api, ctx := newFacetAuthoringTestApi(t)
	climate := declareFacet(t, api, ctx, "climate", string(AttributeValueString))

	// The control on the constant this whole slice turns on. If the lowering's facet scope
	// ever moves off SHARED, the positive arm below fails behaviourally; this says WHICH
	// string the console is holding, so the failure names the file that has to change.
	assert.Equal(t, consoleFacetScope, string(AttributeScopeShared),
		"the console writes this scope literal; the lowering must read the same one")

	for _, token := range []string{"d-authored", "d-temperate", "d-client", "d-server", "d-bare"} {
		newDevice(t, api, ctx, token)
	}

	// Authored exactly as the panel authors it: the facet scope, and the value type the
	// DECLARATION carries.
	authorValue(t, api, ctx, "d-authored", consoleFacetScope, climate.Key, climate.ValueType, "arid")
	// A different value on the same axis. A filter that matches everything also matches
	// your fixture, so the positive arm proves nothing without this one.
	authorValue(t, api, ctx, "d-temperate", consoleFacetScope, climate.Key, climate.ValueType, "temperate")
	// 🔴 THE WRONG SCOPE. Right key, right value, right type — and the lowering never reads
	// these rows. This is the shape a device reporting its own `climate` produces.
	authorValue(t, api, ctx, "d-client", string(AttributeScopeClient), climate.Key, climate.ValueType, "arid")
	authorValue(t, api, ctx, "d-server", string(AttributeScopeServer), climate.Key, climate.ValueType, "arid")

	assert.Equal(t, []string{"d-authored"}, browseMatches(t, api, ctx, authoredSelector),
		"only the entity authored at the facet scope, with the matching value, is a match")

	// Clearing is a DELETE. Writing "" leaves a row that still exists — it would keep
	// satisfying `"climate" in attr` and would match `attr["climate"] == ""`.
	authorValue(t, api, ctx, "d-authored", consoleFacetScope, climate.Key, climate.ValueType, "")
	assert.Equal(t, []string{}, browseMatches(t, api, ctx, authoredSelector),
		"an empty value no longer matches the authored one")
	assert.ElementsMatch(t, []string{"d-authored", "d-temperate"},
		browseMatches(t, api, ctx, `"climate" in attr`),
		"...but the row is still there, which is why Clear deletes rather than writes empty")

	removed, err := api.DeleteEntityAttribute(ctx, entity.TypeDevice.String(), "d-authored",
		consoleFacetScope, climate.Key)
	if err != nil {
		t.Fatalf("clear the facet value: %v", err)
	}
	assert.True(t, removed, "the row was there to remove")
	// d-temperate is still on the axis — the delete took one entity off it, not the axis.
	assert.Equal(t, []string{"d-temperate"}, browseMatches(t, api, ctx, `"climate" in attr`),
		"a cleared facet leaves the axis entirely, and takes nothing else with it")
}

// 🔴 THE VALUE TYPE IS PART OF THE MATCH, NOT DECORATION. A scalar leaf pins value_type, so
// the same characters stored under the wrong type are invisible to the axis they were
// authored for — with no error at any layer.
func TestFacetValueTypeIsPartOfTheMatch(t *testing.T) {
	api, ctx := newFacetAuthoringTestApi(t)
	elevation := declareFacet(t, api, ctx, "elevation", string(AttributeValueLong))
	assert.Equal(t, string(AttributeValueLong), elevation.ValueType,
		"the declaration is where the panel takes the value type from")

	for _, token := range []string{"d-long", "d-string", "d-refused"} {
		newDevice(t, api, ctx, token)
	}

	authorValue(t, api, ctx, "d-long", consoleFacetScope, elevation.Key, elevation.ValueType, "1500")
	// The same digits, stored as STRING — which is what a panel that inferred the type from
	// the text, or defaulted it, would write.
	authorValue(t, api, ctx, "d-string", consoleFacetScope, elevation.Key,
		string(AttributeValueString), "1500")

	assert.Equal(t, []string{"d-long"},
		browseMatches(t, api, ctx, `attr["elevation"] == 1500`),
		"only the row stored under the declared type satisfies a scalar leaf")
	assert.Equal(t, []string{"d-long"},
		browseMatches(t, api, ctx, `attr["elevation"] >= 1000`),
		"and the same holds for an ordered comparison")

	// 🔑 THE PRESENCE OPERATOR CANNOT SEE THE TYPE MISTAKE. `k in attr` emits no value
	// predicate at all, so both rows satisfy it. Recorded because it is precisely why the
	// equality arms above are the control: a suite that only exercised "has any value"
	// would pass with the value type wrong.
	assert.ElementsMatch(t, []string{"d-long", "d-string"},
		browseMatches(t, api, ctx, `"elevation" in attr`),
		"presence ignores the value type, so it can never be the check that catches this")

	// 🔴 THE THIRD DOOR, NOW CLOSED AT THE SERVER. A value the declared type cannot hold is
	// REFUSED. It used to be coerced to unset — a mutation that returned the row, reported
	// success, and stored nothing, so the axis matched nothing and the panel went on showing
	// the text. The console's own check is the friendly message; this is the gate, because
	// the client validates by pattern and the server by parser, and the two disagree exactly
	// where it matters (see TestSetAttribute_UnparseableNumberIsRefused).
	_, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
		EntityType: entity.TypeDevice.String(), Entity: "d-refused",
		Scope: consoleFacetScope, AttrKey: elevation.Key,
		ValueType: elevation.ValueType, Value: strp("high"),
	})
	assert.Error(t, err, "a LONG value that does not parse is refused")
	assert.Equal(t, []string{"d-long", "d-string"},
		browseMatches(t, api, ctx, `"elevation" in attr`),
		"and the refusal leaves no half-written row on the axis")
}

// The BOOLEAN arm of the same rule. `lower.go`'s string-equality and numeric branches each
// pin value_type, and so does the boolean one (`ea.value_type = 'BOOLEAN'`) — but until this
// test nothing gave that pin a row to exclude, which is the same hole the surviving M8b
// mutant named on the STRING branch, one arm over.
//
// 🔑 THE STORED FORM IS CANONICAL, AND THE CANONICALIZATION IS PART OF THE MATCH. A caller
// may write "True" or "1"; the server stores 'true', which is what a CEL `true` literal
// lowers to comparing against. A BOOLEAN row holding an uncanonicalized spelling would
// satisfy the type pin and fail the value compare, which is the type mistake wearing a
// different hat.
func TestBooleanFacetValueMatchesOnlyUnderItsOwnType(t *testing.T) {
	api, ctx := newFacetAuthoringTestApi(t)
	managed := declareFacet(t, api, ctx, "managed", string(AttributeValueBoolean))
	assert.Equal(t, string(AttributeValueBoolean), managed.ValueType)

	for _, token := range []string{"d-true", "d-spelled", "d-false", "d-string-true"} {
		newDevice(t, api, ctx, token)
	}
	authorValue(t, api, ctx, "d-true", consoleFacetScope, managed.Key, managed.ValueType, "true")
	// A non-canonical spelling the server accepts and normalizes — it must match exactly as
	// the canonical one does, or the axis would depend on how the operator typed it.
	authorValue(t, api, ctx, "d-spelled", consoleFacetScope, managed.Key, managed.ValueType, "True")
	authorValue(t, api, ctx, "d-false", consoleFacetScope, managed.Key, managed.ValueType, "false")
	// 🔴 The type mistake: the text "true" stored as STRING. It reads back identically and is
	// invisible to `attr["managed"] == true`.
	authorValue(t, api, ctx, "d-string-true", consoleFacetScope, managed.Key,
		string(AttributeValueString), "true")

	assert.ElementsMatch(t, []string{"d-true", "d-spelled"},
		browseMatches(t, api, ctx, `attr["managed"] == true`),
		"both spellings of the BOOLEAN match; the STRING row does not")
	assert.Equal(t, []string{"d-false"},
		browseMatches(t, api, ctx, `attr["managed"] == false`))
	// The inequality arm sees the same type pin, so the STRING row is not "different", it is
	// absent — a `!=` leaf is present-and-different, never a match on a missing facet.
	assert.Equal(t, []string{"d-false"},
		browseMatches(t, api, ctx, `attr["managed"] != true`))

	// And a value the type cannot hold is refused rather than stored as unset.
	_, err := api.SetEntityAttribute(ctx, &EntityAttributeSetRequest{
		EntityType: entity.TypeDevice.String(), Entity: "d-true",
		Scope: consoleFacetScope, AttrKey: managed.Key,
		ValueType: managed.ValueType, Value: strp("maybe"),
	})
	assert.Error(t, err, "a BOOLEAN value that does not parse is refused")
	assert.ElementsMatch(t, []string{"d-true", "d-spelled"},
		browseMatches(t, api, ctx, `attr["managed"] == true`),
		"the refused write left the previous value standing")
}

// 🔴 A FACET'S DECLARED TYPE CAN BE CHANGED, AND THE VALUES ALREADY AUTHORED DO NOT MOVE
// WITH IT. SetFacetKey upserts on (memberType, key), so re-declaring `climate` as STRING
// after values were authored under JSON leaves every one of those rows stranded: still
// stored, still readable, still shown in the panel — and no longer matched by the axis
// they belong to. The registry is a lens over the attribute store, never a constraint on
// it, so nothing rewrites them and nothing warns.
//
// 🔑 THIS CASE WAS ADDED BECAUSE A MUTANT SURVIVED. Deleting the `ea.value_type = 'STRING'`
// gate from the string-equality lowering left every test in this file green — the suite
// had no entity carrying the same key and the same text under a DIFFERENT non-numeric
// type, so there was nothing for the gate to exclude. That is a missing input class, not
// a missing assertion, and re-declaring a facet's type is how a real tenant produces it.
func TestRedeclaringAFacetTypeStrandsTheValuesAlreadyAuthored(t *testing.T) {
	api, ctx := newFacetAuthoringTestApi(t)
	newDevice(t, api, ctx, "d-legacy")
	newDevice(t, api, ctx, "d-current")

	// Authored while the axis was declared JSON.
	declareFacet(t, api, ctx, "climate", string(AttributeValueJson))
	authorValue(t, api, ctx, "d-legacy", consoleFacetScope, "climate",
		string(AttributeValueJson), "arid")

	// The tenant re-declares the axis as STRING; the upsert rewrites the DECLARATION only.
	redeclared := declareFacet(t, api, ctx, "climate", string(AttributeValueString))
	assert.Equal(t, string(AttributeValueString), redeclared.ValueType)
	authorValue(t, api, ctx, "d-current", consoleFacetScope, "climate",
		redeclared.ValueType, "arid")

	assert.Equal(t, []string{"d-current"}, browseMatches(t, api, ctx, authoredSelector),
		"the row authored under the OLD declared type is stranded, not silently migrated")
	// Both are still on the axis by presence — which is exactly why the stranding is
	// invisible without composing a value comparison.
	assert.ElementsMatch(t, []string{"d-legacy", "d-current"},
		browseMatches(t, api, ctx, `"climate" in attr`))
}

// The scope mistake is not visible from the read side either: the attribute is right there
// when you ask for it, which is why "I set it and I can see it" and "Browse says 0" are both
// true at once. The panel shows non-facet-scope rows read-only for exactly this reason.
func TestWrongScopeValueIsStoredAndReadableButNeverMatches(t *testing.T) {
	api, ctx := newFacetAuthoringTestApi(t)
	climate := declareFacet(t, api, ctx, "climate", string(AttributeValueString))
	newDevice(t, api, ctx, "d-client")
	authorValue(t, api, ctx, "d-client", string(AttributeScopeClient), climate.Key,
		climate.ValueType, "arid")

	deviceType := entity.TypeDevice.String()
	token := "d-client"
	found, err := api.EntityAttributes(ctx, EntityAttributeSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100},
		EntityType: &deviceType, Entity: &token,
	})
	if err != nil {
		t.Fatalf("read back the attributes: %v", err)
	}
	assert.Len(t, found.Results, 1, "the value is stored and readable")
	assert.Equal(t, "arid", found.Results[0].Value.String)

	assert.Equal(t, []string{}, browseMatches(t, api, ctx, authoredSelector),
		"and it is invisible to the axis it was authored for")
}
