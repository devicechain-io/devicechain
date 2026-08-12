// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-device-state/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

// Subject carrying a parseable tenant ({instance}.{tenant}.{suffix}), which the merge
// path requires to build a tenant-scoped context (fail-closed).
const locationTestSubject = "instance1.tenant1.resolved-events"

// deviceStateMicroservice is the minimal Microservice the processor needs — it uses it
// only to construct its RED metrics.
var deviceStateMicroservice = &core.Microservice{
	StartTime:                    time.Now(),
	InstanceId:                   "instance1",
	MicroserviceId:               "device-state",
	MicroserviceName:             "Device State",
	FunctionalArea:               "device-state",
	InstanceConfiguration:        config.InstanceConfiguration{},
	MicroserviceConfigurationRaw: make([]byte, 0),
}

// newLocationProcessor builds a StateProcessor over a REAL sqlite-backed Api, so a merge
// runs the whole write path and lands rows rather than proving only that a mock was
// called. The reader is nil: these tests drive mergeOne directly with a synthetic
// message instead of running the read loop.
func newLocationProcessor(t *testing.T) *StateProcessor {
	t.Helper()

	// ProcessorMetrics registers collectors on the global default registry at
	// construction, so a fresh registry per test avoids a duplicate-registration panic.
	registry := prometheus.NewRegistry()
	prometheus.DefaultRegisterer = registry
	prometheus.DefaultGatherer = registry

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	// All three projections, so a non-location event exercises its own real write path
	// rather than failing on a missing table and looking like a skip.
	if err := db.AutoMigrate(&model.DeviceState{}, &model.LatestMeasurement{}, &model.LatestLocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	api := model.NewApi(&rdb.RdbManager{Database: db})
	return NewStateProcessor(deviceStateMicroservice, nil, core.NewNoOpLifecycleCallbacks(), api)
}

// str is a pointer to a literal, the shape every resolved location coordinate arrives in.
func str(s string) *string { return &s }

// locationMessage marshals a resolved location event onto the wire exactly as
// device-management publishes it, so the merge path unmarshals real bytes rather than
// reading a struct handed straight to it.
func locationMessage(t *testing.T, deviceToken string, occurredAt time.Time,
	entries []dmmodel.ResolvedLocationEntry) messaging.Message {
	t.Helper()
	// Stand in for the resolver's entry-versus-envelope rule: a fix that names no instant
	// of its own is stamped with the message's, because that is what a real resolved event
	// arriving here always carries. A fixture that left it zero would build an event this
	// pipeline can no longer receive, and every assertion below would be measuring a shape
	// that does not exist. A test needing DISTINCT per-fix instants sets them explicitly.
	stamped := make([]dmmodel.ResolvedLocationEntry, len(entries))
	for i, entry := range entries {
		if entry.OccurredTime.IsZero() {
			entry.OccurredTime = occurredAt
		}
		stamped[i] = entry
	}
	event := &dmmodel.ResolvedEvent{
		Source:            "mqtt",
		SourceDeviceToken: deviceToken,
		EventType:         esmodel.Location,
		OccurredTime:      occurredAt,
		// DISTINCT from the occurred time on purpose. With the two equal, an assertion that
		// a projection advanced to "the message's time" cannot tell which of them it read,
		// and the connectivity projection reading the SERVER's processed time instead of the
		// device's reported one would go unnoticed.
		ProcessedTime: occurredAt.Add(250 * time.Millisecond),
		Payload:       &dmmodel.ResolvedLocationsPayload{Entries: stamped},
	}
	encoded, err := dmproto.MarshalResolvedEvent(event)
	if err != nil {
		t.Fatalf("marshal resolved event: %v", err)
	}
	return messaging.Message{Subject: locationTestSubject, Value: encoded}
}

// loadState / loadLocation read the two projections a location event touches.
func loadState(t *testing.T, sp *StateProcessor, ctx context.Context, token string) model.DeviceState {
	t.Helper()
	api := sp.Api.(*model.Api)
	var ds model.DeviceState
	if err := api.RDB.DB(ctx).Where("device_token = ?", token).First(&ds).Error; err != nil {
		t.Fatalf("load device state for %s: %v", token, err)
	}
	return ds
}

func loadProjectedLocation(t *testing.T, sp *StateProcessor, ctx context.Context, token string) model.LatestLocation {
	t.Helper()
	api := sp.Api.(*model.Api)
	var l model.LatestLocation
	if err := api.RDB.DB(ctx).Where("device_token = ?", token).First(&l).Error; err != nil {
		t.Fatalf("load latest location for %s: %v", token, err)
	}
	return l
}

// TestLocationEventDrivesTheProjection runs a real resolved location event through
// mergeOne and asserts every field arrived, each from a distinct non-round value.
func TestLocationEventDrivesTheProjection(t *testing.T) {
	sp := newLocationProcessor(t)
	ctx := core.WithTenant(context.Background(), "tenant1")
	t0 := time.Date(2026, 8, 9, 14, 32, 17, 0, time.UTC)

	sp.mergeOne(context.Background(), locationMessage(t, "dozer-01", t0, []dmmodel.ResolvedLocationEntry{{
		Latitude:  str("41.87231954"),
		Longitude: str("-87.62819473"),
		Elevation: str("179.6231"),
		Accuracy:  str("3.8417"),
		Speed:     str("12.9053"),
		Heading:   str("217.6384"),
	}}))

	got := loadProjectedLocation(t, sp, ctx, "dozer-01")
	for _, f := range []struct {
		name string
		got  float64
		want float64
	}{
		{"latitude", got.Latitude.Float64, 41.87231954},
		{"longitude", got.Longitude.Float64, -87.62819473},
		{"elevation", got.Elevation.Float64, 179.6231},
		{"accuracy", got.Accuracy.Float64, 3.8417},
		{"speed", got.Speed.Float64, 12.9053},
		{"heading", got.Heading.Float64, 217.6384},
	} {
		if f.got != f.want {
			t.Errorf("%s = %v, want %v", f.name, f.got, f.want)
		}
	}
	if !got.OccurredTime.Equal(t0) {
		t.Errorf("occurredTime = %v, want %v", got.OccurredTime, t0)
	}
}

// TestLocationEventIsStillAHeartbeat is the no-regression assertion. A location is a
// data event like any other, so it must keep driving the liveness projection: it creates
// an active device, advances LastActivityTime, and therefore keeps the device out of the
// inactivity sweep. A device that only ever reports its position would otherwise be
// swept offline for saying nothing — while the platform knew exactly where it was.
func TestLocationEventIsStillAHeartbeat(t *testing.T) {
	sp := newLocationProcessor(t)
	ctx := core.WithTenant(context.Background(), "tenant1")
	t0 := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)

	fix := []dmmodel.ResolvedLocationEntry{{Latitude: str("41.87231954"), Longitude: str("-87.62819473")}}
	sp.mergeOne(context.Background(), locationMessage(t, "rover-01", t0, fix))

	// The fix reported no elevation, accuracy, speed or heading, and those must reach
	// the projection as NULL rather than 0 — sea level, perfect accuracy, stationary and
	// due north are all values a consumer would believe.
	loc := loadProjectedLocation(t, sp, ctx, "rover-01")
	for _, f := range []struct {
		name string
		got  sql.NullFloat64
	}{
		{"elevation", loc.Elevation},
		{"accuracy", loc.Accuracy},
		{"speed", loc.Speed},
		{"heading", loc.Heading},
	} {
		if f.got.Valid {
			t.Errorf("unreported %s arrived as %v instead of NULL", f.name, f.got.Float64)
		}
	}

	ds := loadState(t, sp, ctx, "rover-01")
	if !ds.Active {
		t.Fatalf("a location event did not create an ACTIVE device: %+v", ds)
	}
	if !ds.LastActivityTime.Valid || !ds.LastActivityTime.Time.Equal(t0) {
		t.Fatalf("a location event did not register as activity: %+v", ds.LastActivityTime)
	}

	// A later fix advances activity, which is what holds the sweep off.
	later := t0.Add(30 * time.Minute)
	sp.mergeOne(context.Background(), locationMessage(t, "rover-01", later, fix))
	if ds = loadState(t, sp, ctx, "rover-01"); !ds.LastActivityTime.Time.Equal(later) {
		t.Fatalf("a later location event did not advance activity: %v", ds.LastActivityTime.Time)
	}

	// The sweep, run just past the first fix's timeout but well within the second's,
	// leaves the device alone — the heartbeat is load-bearing, not merely recorded.
	api := sp.Api.(*model.Api)
	flipped, err := api.SweepInactive(core.WithSystemContext(ctx), later.Add(time.Minute))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if flipped != 0 {
		t.Fatalf("the inactivity sweep flipped %d device(s) that had just reported a position", flipped)
	}
}

// TestLocationRedeliveryDoesNotRollBack drives the out-of-order case through the REAL
// message path: the same two events a redelivering stream would hand the merge workers,
// newest first. The device must stay where it is.
func TestLocationRedeliveryDoesNotRollBack(t *testing.T) {
	sp := newLocationProcessor(t)
	ctx := core.WithTenant(context.Background(), "tenant1")
	t0 := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)

	newer := locationMessage(t, "dozer-01", t0.Add(10*time.Minute), []dmmodel.ResolvedLocationEntry{
		{Latitude: str("41.90118427"), Longitude: str("-87.61042956")},
	})
	older := locationMessage(t, "dozer-01", t0, []dmmodel.ResolvedLocationEntry{
		{Latitude: str("41.87231954"), Longitude: str("-87.62819473")},
	})
	// The older message is redelivered after the newer one already landed.
	older.NumDelivered = 2

	sp.mergeOne(context.Background(), newer)
	sp.mergeOne(context.Background(), older)

	got := loadProjectedLocation(t, sp, ctx, "dozer-01")
	if got.Latitude.Float64 != 41.90118427 || got.Longitude.Float64 != -87.61042956 {
		t.Fatalf("a redelivered OLDER fix teleported the device back to %v,%v",
			got.Latitude.Float64, got.Longitude.Float64)
	}
	if !got.OccurredTime.Equal(t0.Add(10 * time.Minute)) {
		t.Fatalf("occurredTime rolled back to %v", got.OccurredTime)
	}
}

// TestLocationEntryWithNoPositionIsSkipped: an entry that locates the device nowhere
// must not replace a real position with an empty one — and must not stamp itself as the
// newest fix, which would then block the next genuine older-but-valid fix.
func TestLocationEntryWithNoPositionIsSkipped(t *testing.T) {
	sp := newLocationProcessor(t)
	ctx := core.WithTenant(context.Background(), "tenant1")
	t0 := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)

	sp.mergeOne(context.Background(), locationMessage(t, "dozer-01", t0, []dmmodel.ResolvedLocationEntry{
		{Latitude: str("41.87231954"), Longitude: str("-87.62819473")},
	}))
	// A later entry carrying only a heading — no position at all.
	sp.mergeOne(context.Background(), locationMessage(t, "dozer-01", t0.Add(time.Minute),
		[]dmmodel.ResolvedLocationEntry{{Heading: str("217.6384")}}))

	got := loadProjectedLocation(t, sp, ctx, "dozer-01")
	if !got.Latitude.Valid || got.Latitude.Float64 != 41.87231954 {
		t.Fatalf("a position-less entry blanked the stored fix: %+v", got)
	}
	if got.Heading.Valid {
		t.Fatalf("a position-less entry wrote its heading onto the stored fix: %v", got.Heading.Float64)
	}
	if !got.OccurredTime.Equal(t0) {
		t.Fatalf("a position-less entry advanced the ordering key to %v", got.OccurredTime)
	}
}

// TestNonLocationEventLeavesTheProjectionAlone: the location merge is keyed to the event
// TYPE, so a plain data event is still only a heartbeat. Without this, the type switch
// could be dropped entirely and the location tests would all still pass.
func TestNonLocationEventLeavesTheProjectionAlone(t *testing.T) {
	sp := newLocationProcessor(t)
	ctx := core.WithTenant(context.Background(), "tenant1")
	t0 := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)

	event := &dmmodel.ResolvedEvent{
		Source:            "mqtt",
		SourceDeviceToken: "dozer-01",
		EventType:         esmodel.Measurement,
		OccurredTime:      t0,
		ProcessedTime:     t0,
		Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{{
			Entries: []dmmodel.ResolvedMeasurementEntry{{Name: "temp", Value: "21.5"}},
		}}},
	}
	encoded, err := dmproto.MarshalResolvedEvent(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sp.mergeOne(context.Background(), messaging.Message{Subject: locationTestSubject, Value: encoded})

	api := sp.Api.(*model.Api)
	var n int64
	if err := api.RDB.DB(ctx).Model(&model.LatestLocation{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("a measurement event wrote %d row(s) into the location projection", n)
	}
	// It is still a heartbeat, which is the behaviour that must be unchanged.
	if ds := loadState(t, sp, ctx, "dozer-01"); !ds.Active {
		t.Fatalf("a measurement event stopped registering as activity: %+v", ds)
	}
}
