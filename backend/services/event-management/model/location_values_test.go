// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 🔴 WHY THIS FILE EXISTS. Before it, NOTHING in this repository read a coordinate
// back out of anything. It was measured rather than assumed: deleting Latitude,
// Longitude and Elevation from the location resolver left the whole workspace test
// suite green, four runs out of four. Every test that named Location used it as an
// inert enum label or as an envelope with an empty Entries list, so the location
// spine was carried by tests that could not fail if it were deleted.
//
// The tests here therefore carry REAL VALUES and read them back. Constructing a
// LocationEvent and asserting it is non-nil is precisely the shape that already
// existed and already caught nothing.

// newEventTablesApi builds an in-memory sqlite Api carrying the base events table
// and the payload tables, each with the production identity index the batch creates
// name as their ON CONFLICT target. Shared with event_ordering_test.go.
func newEventTablesApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err, "open sqlite")
	require.NoError(t, rdb.RegisterTenantScoping(db), "register tenant scoping")
	// All THREE payload tables, not just the two a location test needs. The
	// ordering fix landed on all four reads, so the harness has to be able to seed
	// all four or the untestable ones quietly go untested — which is how two of
	// them were left unpinned in the first place.
	require.NoError(t, db.AutoMigrate(&Event{}, &LocationEvent{}, &MeasurementEvent{}, &AlertEvent{}), "migrate")
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX idx_events_identity `+
		`ON events (tenant_id, event_id, occurred_time);`).Error, "event identity key")
	for _, table := range []string{"location_events", "measurement_events", "alert_events"} {
		require.NoError(t, db.Exec(
			`CREATE UNIQUE INDEX uq_`+table+`_idem ON `+table+
				` (tenant_id, payload_id, occurred_time);`).Error, "payload identity key on "+table)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// eventOfType builds a base event of the given type whose identity is derived exactly
// as the ingest path derives it, so distinct content yields distinct parents.
func eventOfType(tenant, device string, kind esmodel.EventType, occurred time.Time, payload string) Event {
	ev := Event{
		DeviceToken:  device,
		EventType:    kind,
		OccurredTime: occurred,
		Source:       "test",
	}
	ev.EventId = DeriveEventId(tenant, &ev, []byte(payload))
	return ev
}

// fix is one GPS fix's six stored numbers, kept together so a test can vary exactly
// one of them and leave the rest identical.
type fix struct {
	lat, lon, elev, accuracy, speed, heading float64
}

// request renders the fix as a create request under the given parent event.
func (f fix) request(ev Event) *LocationEventCreateRequest {
	return &LocationEventCreateRequest{
		Event:     ev,
		Latitude:  f64(f.lat),
		Longitude: f64(f.lon),
		Elevation: f64(f.elev),
		Accuracy:  f64(f.accuracy),
		Speed:     f64(f.speed),
		Heading:   f64(f.heading),
	}
}

// atlanta is the reference fix. 🔴 Every one of the six numbers is DISTINCT and none
// is round. That is load-bearing, not decorative: with all six set to 1.0 a mapping
// that assigned accuracy to speed — or latitude to longitude — would read back
// perfectly and the test would pass while the column contents were scrambled.
var atlanta = fix{lat: 33.749, lon: -84.388, elev: 320.5, accuracy: 4.2, speed: 1.75, heading: 271.5}

// TestLocationEventRoundTripsEveryFixFieldWithItsOwnValue drives the real create path
// and the real read path, then asserts each of the six numbers INDIVIDUALLY against
// its own expected value.
func TestLocationEventRoundTripsEveryFixFieldWithItsOwnValue(t *testing.T) {
	api := newEventTablesApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 8, 1, 10, 15, 30, 0, time.UTC)

	ev := eventOfType("acme", "device-1", esmodel.Location, occurred, "fix-1")
	created, err := api.CreateLocationEvents(ctx, api.RDB.DB(ctx),
		[]*LocationEventCreateRequest{atlanta.request(ev)})
	require.NoError(t, err, "the fix must persist")
	require.Len(t, created, 1)

	results, err := api.LocationEvents(ctx, EventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
	})
	require.NoError(t, err, "read the fix back")
	require.Len(t, results.Results, 1, "exactly the one fix that was written")
	got := results.Results[0]

	for _, c := range []struct {
		field string
		want  float64
		got   sql.NullFloat64
	}{
		{"latitude", atlanta.lat, got.Latitude},
		{"longitude", atlanta.lon, got.Longitude},
		{"elevation", atlanta.elev, got.Elevation},
		{"accuracy", atlanta.accuracy, got.Accuracy},
		{"speed", atlanta.speed, got.Speed},
		{"heading", atlanta.heading, got.Heading},
	} {
		require.Truef(t, c.got.Valid, "%s was written and must read back non-NULL", c.field)
		assert.Equalf(t, c.want, c.got.Float64, "%s must read back the value that was written", c.field)
	}
}

// TestLocationEventOmittedFixFieldsReadBackAsNullNotZero pins the other half of the
// contract. A bare lat/lon fix — every device that reports a position and nothing
// else — must leave the four optional columns NULL. Zero-as-valid is not the same
// answer: an accuracy of 0 m claims a perfect fix and a heading of 0 claims due
// north, so collapsing "not reported" into "reported as zero" invents readings.
func TestLocationEventOmittedFixFieldsReadBackAsNullNotZero(t *testing.T) {
	api := newEventTablesApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 8, 1, 10, 15, 30, 0, time.UTC)

	ev := eventOfType("acme", "device-1", esmodel.Location, occurred, "bare-fix")
	_, err := api.CreateLocationEvents(ctx, api.RDB.DB(ctx), []*LocationEventCreateRequest{{
		Event:     ev,
		Latitude:  f64(atlanta.lat),
		Longitude: f64(atlanta.lon),
	}})
	require.NoError(t, err)

	results, err := api.LocationEvents(ctx, EventSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
	})
	require.NoError(t, err)
	require.Len(t, results.Results, 1)
	got := results.Results[0]

	require.True(t, got.Latitude.Valid, "the position that WAS reported must survive")
	assert.Equal(t, atlanta.lat, got.Latitude.Float64)
	require.True(t, got.Longitude.Valid)
	assert.Equal(t, atlanta.lon, got.Longitude.Float64)

	for _, c := range []struct {
		field string
		got   sql.NullFloat64
	}{
		{"elevation", got.Elevation},
		{"accuracy", got.Accuracy},
		{"speed", got.Speed},
		{"heading", got.Heading},
	} {
		assert.Falsef(t, c.got.Valid,
			"%s was not reported and must be NULL, not zero-as-valid (got %v)", c.field, c.got.Float64)
	}
}

// locationPayloadIdOf runs one request through the real create path and returns the
// identity it derived.
func locationPayloadIdOf(t *testing.T, api *Api, ctx context.Context, req *LocationEventCreateRequest) []byte {
	t.Helper()
	created, err := api.CreateLocationEvents(ctx, api.RDB.DB(ctx), []*LocationEventCreateRequest{req})
	require.NoError(t, err)
	require.Len(t, created, 1)
	return created[0].PayloadId
}

// TestLocationPayloadIdDiscriminatesOnEveryFixField is the test that catches a field
// left out of the canonical tuple.
//
// 🔴 The consequence of an omission is silent DATA LOSS, not a cosmetic hash defect.
// The payload rows are inserted ON CONFLICT (tenant_id, payload_id, occurred_time)
// DO NOTHING, so two genuinely different readings that hash alike are not two rows —
// the second is swallowed, no error, nothing logged. A device that reports the same
// coordinates with a new heading (a machine slewing on the spot) would keep only the
// first reading.
//
// Both fixes share ONE parent event, so the only thing that can move the identity is
// the field under test.
func TestLocationPayloadIdDiscriminatesOnEveryFixField(t *testing.T) {
	occurred := time.Date(2026, 8, 1, 10, 15, 30, 0, time.UTC)
	ev := eventOfType("acme", "device-1", esmodel.Location, occurred, "fix-batch")

	for _, c := range []struct {
		field string
		vary  func(fix) fix
	}{
		{"latitude", func(f fix) fix { f.lat = 33.751; return f }},
		{"longitude", func(f fix) fix { f.lon = -84.391; return f }},
		{"elevation", func(f fix) fix { f.elev = 321.75; return f }},
		{"accuracy", func(f fix) fix { f.accuracy = 9.6; return f }},
		{"speed", func(f fix) fix { f.speed = 2.25; return f }},
		{"heading", func(f fix) fix { f.heading = 88.5; return f }},
	} {
		t.Run(c.field, func(t *testing.T) {
			api := newEventTablesApi(t)
			ctx := core.WithTenant(context.Background(), "acme")

			varied := c.vary(atlanta)
			require.NotEqual(t, atlanta, varied,
				"negative control: the case must actually change something")

			base := locationPayloadIdOf(t, api, ctx, atlanta.request(ev))
			other := locationPayloadIdOf(t, api, ctx, varied.request(ev))
			assert.NotEqualf(t, base, other,
				"two fixes differing only in %s are different readings and must derive different "+
					"identities — sharing one lets ON CONFLICT DO NOTHING swallow the second", c.field)
		})
	}
}

// TestLocationPayloadIdIsStableForAnIdenticalFix is the counterweight to the test
// above, and it is not optional: an identity that differs for everything is not an
// identity. If the digest moved on each call the dedup would never engage and every
// redelivery of a fix would insert another row.
func TestLocationPayloadIdIsStableForAnIdenticalFix(t *testing.T) {
	api := newEventTablesApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 8, 1, 10, 15, 30, 0, time.UTC)
	ev := eventOfType("acme", "device-1", esmodel.Location, occurred, "fix-batch")

	first := locationPayloadIdOf(t, api, ctx, atlanta.request(ev))
	second := locationPayloadIdOf(t, api, ctx, atlanta.request(ev))
	assert.Equal(t, first, second, "the same fix re-presented must derive the same identity")

	// And the redelivery really did converge on one stored row, which is what that
	// stable identity buys.
	var rows []LocationEvent
	require.NoError(t, api.RDB.DB(ctx).Find(&rows).Error)
	assert.Len(t, rows, 1, "two deliveries of one fix must leave one row")
}
