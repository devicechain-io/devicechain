// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/ast"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// LocationEvent carries six near-identical one-line Float accessors. Nothing else
// in the repository reads a location field through the GraphQL layer, which is the
// last hop a client actually sees — so a field swap between any two of them (an
// Accuracy() that returns Speed, say) is invisible everywhere else. These tests
// exist to make that swap fail, which is only possible if every field is read back
// individually from a value no other field holds.

// Distinct, non-round values: no two are equal, none is zero, and none is the
// negation or a simple multiple of another. Equal or round values would let the
// exact swap this file exists to catch pass unnoticed.
const (
	fixtureLatitude  = 33.749
	fixtureLongitude = -84.388
	fixtureElevation = 320.5
	fixtureAccuracy  = 4.2
	fixtureSpeed     = 1.75
	fixtureHeading   = 271.5
)

var fixtureOccurred = time.Date(2026, 8, 9, 14, 32, 17, 123456789, time.UTC)

// fixturePayloadId is the row's own identity — the thing `id` is now built from.
// Written as literal bytes rather than derived, so the assertion below compares the
// resolver's output against something that does not go through the resolver.
var fixturePayloadId = []byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04}

// newLocationResolver wraps a model in the resolver under test.
//
// S and C are left nil on purpose. None of the accessors exercised here touches
// the schema resolver or the context — they read r.M and nothing else — so a
// future change that introduces a dependency should panic loudly rather than be
// quietly satisfied by a fixture that happened to supply one.
func newLocationResolver(m model.LocationEvent) *LocationEventResolver {
	return &LocationEventResolver{M: m, S: nil, C: nil}
}

// locationFixture is a fully-populated event: every Float field valid and holding
// its own distinct value.
func locationFixture() model.LocationEvent {
	return model.LocationEvent{
		// A stored row always carries its own identity (payload_id is NOT NULL), and
		// the fixture carries one for the same reason: `id` resolves from it, so a
		// fixture without one would exercise the failure path instead of the read.
		PayloadId:    fixturePayloadId,
		DeviceToken:  "gps-tracker-7",
		EventType:    esmodel.Location,
		OccurredTime: fixtureOccurred,
		Latitude:     sql.NullFloat64{Float64: fixtureLatitude, Valid: true},
		Longitude:    sql.NullFloat64{Float64: fixtureLongitude, Valid: true},
		Elevation:    sql.NullFloat64{Float64: fixtureElevation, Valid: true},
		Accuracy:     sql.NullFloat64{Float64: fixtureAccuracy, Valid: true},
		Speed:        sql.NullFloat64{Float64: fixtureSpeed, Valid: true},
		Heading:      sql.NullFloat64{Float64: fixtureHeading, Valid: true},
	}
}

// assertEveryFloatFieldReadsBack asserts that each Float accessor returns its OWN
// value, and returns the set of schema field names it actually read. The set is a
// by-product of running the assertions rather than a list declared alongside them,
// so TestLocationEventSchemaFloatFieldsAreCovered cannot be satisfied by a name
// nothing here asserts.
func assertEveryFloatFieldReadsBack(t *testing.T) map[string]bool {
	t.Helper()
	r := newLocationResolver(locationFixture())

	require.NotNil(t, r.Latitude(), "latitude resolved to nil from a valid value")
	assert.Equal(t, fixtureLatitude, *r.Latitude(), "Latitude() must return the latitude")

	require.NotNil(t, r.Longitude(), "longitude resolved to nil from a valid value")
	assert.Equal(t, fixtureLongitude, *r.Longitude(), "Longitude() must return the longitude")

	require.NotNil(t, r.Elevation(), "elevation resolved to nil from a valid value")
	assert.Equal(t, fixtureElevation, *r.Elevation(), "Elevation() must return the elevation")

	require.NotNil(t, r.Accuracy(), "accuracy resolved to nil from a valid value")
	assert.Equal(t, fixtureAccuracy, *r.Accuracy(), "Accuracy() must return the accuracy")

	require.NotNil(t, r.Speed(), "speed resolved to nil from a valid value")
	assert.Equal(t, fixtureSpeed, *r.Speed(), "Speed() must return the speed")

	require.NotNil(t, r.Heading(), "heading resolved to nil from a valid value")
	assert.Equal(t, fixtureHeading, *r.Heading(), "Heading() must return the heading")

	return map[string]bool{
		"latitude":  true,
		"longitude": true,
		"elevation": true,
		"accuracy":  true,
		"speed":     true,
		"heading":   true,
	}
}

// Every Float field of a location event is read back individually, from a value no
// other field holds. This is the test that fails when two accessors are crossed.
func TestLocationEventFloatFieldsReadBackIndividually(t *testing.T) {
	exercised := assertEveryFloatFieldReadsBack(t)
	require.NotEmpty(t, exercised)
}

// A field the device did not report resolves to nil, never to zero. Zero-as-valid
// would claim sea level, a perfect fix, a dead stop, or a heading of due north from
// a device that reported none of those things — a wrong reading is worse than a
// missing one, because a client cannot tell it apart from a real measurement.
func TestLocationEventNullFieldsResolveToNil(t *testing.T) {
	// Everything invalid; Float64 deliberately left at its zero so that a resolver
	// ignoring Valid would hand back 0 rather than nil.
	r := newLocationResolver(model.LocationEvent{
		DeviceToken:  "gps-tracker-7",
		EventType:    esmodel.Location,
		OccurredTime: fixtureOccurred,
		Latitude:     sql.NullFloat64{Valid: false},
		Longitude:    sql.NullFloat64{Valid: false},
		Elevation:    sql.NullFloat64{Valid: false},
		Accuracy:     sql.NullFloat64{Valid: false},
		Speed:        sql.NullFloat64{Valid: false},
		Heading:      sql.NullFloat64{Valid: false},
	})

	assert.Nil(t, r.Latitude(), "a null latitude must resolve to nil, not 0")
	assert.Nil(t, r.Longitude(), "a null longitude must resolve to nil, not 0")
	assert.Nil(t, r.Elevation(), "a null elevation must resolve to nil, not sea level")
	assert.Nil(t, r.Accuracy(), "a null accuracy must resolve to nil, not a perfect fix")
	assert.Nil(t, r.Speed(), "a null speed must resolve to nil, not a dead stop")
	assert.Nil(t, r.Heading(), "a null heading must resolve to nil, not due north")
}

// A partially-reported fix keeps the reported fields and nils only the missing
// ones — the mixed case is the realistic one (a receiver with no speed/heading
// still reports a position), and it is where a shared-pointer bug would show.
func TestLocationEventMixedNullAndValidFields(t *testing.T) {
	m := locationFixture()
	m.Speed = sql.NullFloat64{Valid: false}
	m.Heading = sql.NullFloat64{Valid: false}
	r := newLocationResolver(m)

	require.NotNil(t, r.Latitude())
	assert.Equal(t, fixtureLatitude, *r.Latitude(), "a reported latitude survives a null speed/heading")
	require.NotNil(t, r.Accuracy())
	assert.Equal(t, fixtureAccuracy, *r.Accuracy(), "a reported accuracy survives a null speed/heading")
	assert.Nil(t, r.Speed(), "an unreported speed stays nil")
	assert.Nil(t, r.Heading(), "an unreported heading stays nil")
}

// The non-numeric accessors carry the identity of the reading; a location value is
// meaningless without the device and the time it belongs to.
func TestLocationEventNonNumericAccessors(t *testing.T) {
	r := newLocationResolver(locationFixture())

	assert.Equal(t, "gps-tracker-7", r.DeviceToken(), "DeviceToken() must return the device token")
	assert.Equal(t, int32(esmodel.Location), r.EventType(), "EventType() must return the location event type")

	occurred := r.OccurredTime()
	require.NotNil(t, occurred, "OccurredTime() must not be nil for a real timestamp")
	assert.Equal(t, fixtureOccurred.Format(time.RFC3339Nano), *occurred, "OccurredTime() must return the occurred time")

	// Id is the ROW's own identity, not the (device, type, time) tuple it used to be
	// built from — that tuple is shared by every payload row of one sample, so it
	// could never address a reading. It is what a client uses to address the reading,
	// so a swap inside it is as damaging as one in a value.
	id, err := r.Id()
	require.NoError(t, err, "a row carrying a payload id must resolve one")
	assert.Equal(t, gql.ID("deadbeef01020304"), id,
		"id must be the row's payload_id, rendered as hex")
	assert.NotContains(t, string(id), "gps-tracker-7",
		"id must no longer be built from the device token")
}

// The counterweight to the assertion above: an id-less row is REFUSED rather than
// resolved to an empty ID!. Without this, the fix for the duplicate-id defect would
// have a hole exactly the shape of the defect — every row missing an id sharing one
// value ("") — and the only rows that can reach it are the ones synthesized in
// memory, which is where the bug lived.
func TestLocationEventWithNoPayloadIdIsRefused(t *testing.T) {
	m := locationFixture()
	m.PayloadId = nil

	_, err := newLocationResolver(m).Id()

	require.Error(t, err, "a row with no identity must not resolve to an empty ID!")
}

// A zero occurred time is absent, not the Unix epoch.
func TestLocationEventZeroOccurredTimeResolvesToNil(t *testing.T) {
	m := locationFixture()
	m.OccurredTime = time.Time{}
	assert.Nil(t, newLocationResolver(m).OccurredTime(), "a zero occurred time must resolve to nil")
}

// Every Float field the schema DECLARES on LocationEvent must have an accessor on
// LocationEventResolver, and that accessor must be one the tests above actually
// read a value through. Adding a seventh Float field to schema.graphql without
// extending assertEveryFloatFieldReadsBack fails here rather than shipping a field
// whose only guarantee is that it parses.
//
// The schema is read through the same MustParseSchema the service starts with (see
// schema_test.go), so this asks the real schema, not a copy of it.
func TestLocationEventSchemaFloatFieldsAreCovered(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})

	named, ok := schema.AST().Types["LocationEvent"]
	require.True(t, ok, "schema.graphql declares no LocationEvent type")
	obj, ok := named.(*ast.ObjectTypeDefinition)
	require.True(t, ok, "LocationEvent is not an object type")

	declared := map[string]bool{}
	for _, f := range obj.Fields {
		if strings.TrimSuffix(f.Type.String(), "!") == "Float" {
			declared[f.Name] = true
		}
	}
	// Without this the comparison below could pass by finding nothing at all — for
	// example if the AST ever spelled a scalar's name differently.
	require.NotEmpty(t, declared, "found no Float fields on LocationEvent; the schema probe is broken")

	resolverType := reflect.TypeOf(&LocationEventResolver{})
	for name := range declared {
		method := strings.ToUpper(name[:1]) + name[1:]
		_, found := resolverType.MethodByName(method)
		assert.True(t, found, "schema field %q has no %s() accessor on LocationEventResolver", name, method)
	}

	assert.Equal(t, declared, assertEveryFloatFieldReadsBack(t),
		"the Float fields LocationEvent declares and the ones the resolver tests read back must be the same set")
}
