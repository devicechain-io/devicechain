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

	for _, token := range []string{"d-long", "d-string", "d-unparseable"} {
		newDevice(t, api, ctx, token)
	}

	authorValue(t, api, ctx, "d-long", consoleFacetScope, elevation.Key, elevation.ValueType, "1500")
	// The same digits, stored as STRING — which is what a panel that inferred the type from
	// the text, or defaulted it, would write.
	authorValue(t, api, ctx, "d-string", consoleFacetScope, elevation.Key,
		string(AttributeValueString), "1500")
	// 🔴 THE SERVER DOES NOT REFUSE THIS. A LONG write it cannot parse is coerced to unset
	// (normalizeAttributeValue), so the mutation SUCCEEDS and stores a row with no value.
	// That is why the panel validates before sending rather than trusting the write.
	authorValue(t, api, ctx, "d-unparseable", consoleFacetScope, elevation.Key,
		elevation.ValueType, "high")

	assert.Equal(t, []string{"d-long"},
		browseMatches(t, api, ctx, `attr["elevation"] == 1500`),
		"only the row stored under the declared type satisfies a scalar leaf")
	assert.Equal(t, []string{"d-long"},
		browseMatches(t, api, ctx, `attr["elevation"] >= 1000`),
		"and the same holds for an ordered comparison")

	// 🔑 THE PRESENCE OPERATOR CANNOT SEE THE TYPE MISTAKE. `k in attr` emits no value
	// predicate at all, so all three rows satisfy it — including the one holding nothing.
	// Recorded because it is precisely why the equality arms above are the control: a suite
	// that only exercised "has any value" would pass with the value type wrong.
	assert.ElementsMatch(t, []string{"d-long", "d-string", "d-unparseable"},
		browseMatches(t, api, ctx, `"elevation" in attr`),
		"presence ignores the value type, so it can never be the check that catches this")
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
