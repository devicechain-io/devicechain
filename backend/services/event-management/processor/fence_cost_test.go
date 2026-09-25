// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// The erasure fence reads purged_tenants once before every tenant-bearing create or
// update. These tests pin, BY VALUE, what that costs on event-management's per-message
// write path — the statements PersistEvent makes and how many of them are fence reads —
// and prove the fence fires on this area's real models (events, measurement_events,
// event_anchors) along that path.
//
// 🔴 A change that makes the path cheaper by disarming the fence LOWERS every count. The
// refusal and fail-closed tests are what turn that change red; the counts alone would not.
//
// The numbers are STATEMENTS, not round trips: gorm does not trace BEGIN/COMMIT, and
// PersistEvent runs in one transaction.

const fenceCostTenant = "acme"

// newFencedPersistenceWorker builds a worker over sqlite with the PRODUCTION callback
// chain in production order (core/rdb/postgres.go): scoping, token grammar, audit
// journal, erasure fence. A count taken over any other callback set is a count of
// something that does not run.
//
// The database is shared-cache and named per test, so a fence planted outside a
// transaction lands in the same database the persist reads.
func newFencedPersistenceWorker(t *testing.T) (*EventPersistenceWorker, *gorm.DB, *rdbtest.StatementCounter) {
	t.Helper()
	counter := rdbtest.NewStatementCounter(rdb.FenceTable)
	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: counter})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if sqldb, err := db.DB(); err == nil {
			_ = sqldb.Close()
		}
	})
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := rdb.RegisterTokenGrammar(db); err != nil {
		t.Fatalf("register token grammar: %v", err)
	}
	if err := rdb.RegisterAuditJournal(db); err != nil {
		t.Fatalf("register audit journal: %v", err)
	}
	if err := rdb.RegisterTenantFence(db); err != nil {
		t.Fatalf("register erasure fence: %v", err)
	}
	if err := db.AutoMigrate(&rdb.PurgedTenant{}, &rdb.AuditEvent{},
		&model.Event{}, &model.MeasurementEvent{}, &model.EventAnchor{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// The ON CONFLICT arbiters, restated for sqlite exactly as model/persistence_test.go
	// restates them (production gets them from the Postgres migration). Copied rather than
	// shared because that helper lives in another package's _test file, and exporting a
	// test fixture from model would put it in the production package.
	for _, stmt := range []string{
		`CREATE UNIQUE INDEX idx_events_identity ON events (tenant_id, event_id, occurred_time);`,
		`CREATE UNIQUE INDEX uq_measurement_events_idem ON measurement_events (tenant_id, payload_id, occurred_time);`,
		`CREATE UNIQUE INDEX uq_event_anchors_idem ON event_anchors (tenant_id, event_id, occurred_time, anchor_type, anchor_token);`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("create index: %v", err)
		}
	}
	return &EventPersistenceWorker{Api: model.NewApi(&rdb.RdbManager{Database: db})}, db, counter
}

func sysDB(db *gorm.DB) *gorm.DB {
	return db.Session(&gorm.Session{NewDB: true}).WithContext(core.WithSystemContext(context.Background()))
}

// plantFence / liftFence stand and complete a fence under a system context, as the purge
// coordinator does (the audit journal's own row would otherwise fail closed).
func plantFence(t *testing.T, db *gorm.DB, token string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := sysDB(db).Create(&rdb.PurgedTenant{Token: token, Epoch: now, PlantedAt: now}).Error; err != nil {
		t.Fatalf("plant a fence for %q: %v", token, err)
	}
}

func liftFence(t *testing.T, db *gorm.DB, token string) {
	t.Helper()
	if err := sysDB(db).Model(&rdb.PurgedTenant{}).Where("token = ? AND completed_at IS NULL", token).
		Update("completed_at", time.Now().UTC()).Error; err != nil {
		t.Fatalf("lift the fence for %q: %v", token, err)
	}
}

// rowCounts counts the three tables under a system context, so a count is a fact about
// the table rather than about whether a tenant-scoped query was allowed to run.
func rowCounts(t *testing.T, db *gorm.DB) (events, measurements, anchors int64) {
	t.Helper()
	for _, c := range []struct {
		model any
		n     *int64
	}{{&model.Event{}, &events}, {&model.MeasurementEvent{}, &measurements}, {&model.EventAnchor{}, &anchors}} {
		if err := sysDB(db).Model(c.model).Count(c.n).Error; err != nil {
			t.Fatalf("count rows: %v", err)
		}
	}
	return events, measurements, anchors
}

// fenceCostMeasurement is a resolved 3-metric measurement with an alternate id and
// anchors anchors. Every entry carries its own instant: a zero entry time is refused
// (ErrZeroEntryTime), which is why the suite's buildMeasurementsEvent cannot be reused.
func fenceCostMeasurement(altId string, at time.Time, anchors int) dmodel.ResolvedEvent {
	alt := altId
	ev := dmodel.ResolvedEvent{
		Source:            "mqtt1",
		AltId:             &alt,
		SourceDeviceToken: "fc-device",
		EventType:         esmodel.Measurement,
		OccurredTime:      at,
		ProcessedTime:     at,
		Payload: &dmodel.ResolvedMeasurementsPayload{Entries: []dmodel.ResolvedMeasurementsEntry{{
			OccurredTime: at,
			Entries: []dmodel.ResolvedMeasurementEntry{
				{Name: "temperature", Value: "21.5"},
				{Name: "humidity", Value: "40.25"},
				{Name: "pressure", Value: "1013.5"},
			},
		}}},
	}
	for i := 0; i < anchors; i++ {
		ev.Anchors = append(ev.Anchors, dmodel.ResolvedAnchor{AnchorType: "area", AnchorToken: "area-" + string(rune('a'+i))})
	}
	return ev
}

func TestFenceCostOfAMeasurementMessage(t *testing.T) {
	ctx := core.WithTenant(context.Background(), fenceCostTenant)
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		// prior events persisted before the counted one; the counted one is last.
		prior []dmodel.ResolvedEvent
		event dmodel.ResolvedEvent
		// statements and fence reads of the ONE counted persist.
		wantAll, wantFence int64
		// rows the counted persist must ADD to each table.
		wantEvents, wantMeasurements, wantAnchors int64
		wantPersisted                             bool
	}{
		{
			// The altId probe (a read, not fenced), then events, measurement_events and
			// event_anchors inserts, each behind its own fence read.
			name:    "altId and one anchor",
			event:   fenceCostMeasurement("fc-1", t0, 1),
			wantAll: 7, wantFence: 3,
			wantEvents: 1, wantMeasurements: 3, wantAnchors: 1, wantPersisted: true,
		},
		{
			name:    "altId and no anchors",
			event:   fenceCostMeasurement("fc-2", t0, 0),
			wantAll: 5, wantFence: 2,
			wantEvents: 1, wantMeasurements: 3, wantAnchors: 0, wantPersisted: true,
		},
		{
			// A redelivery of an event already persisted: the altId probe finds it and
			// nothing is written, so the fence is never consulted. Each subtest has its own
			// database, so this one persists the original itself.
			name:    "altId redelivery",
			prior:   []dmodel.ResolvedEvent{fenceCostMeasurement("fc-3", t0, 1)},
			event:   fenceCostMeasurement("fc-3", t0, 1),
			wantAll: 1, wantFence: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep, db, counter := newFencedPersistenceWorker(t)
			for _, p := range tc.prior {
				if _, err := ep.PersistEvent(ctx, p); err != nil {
					t.Fatalf("persist the prior event: %v", err)
				}
			}
			e0, m0, a0 := rowCounts(t, db)

			counter.Reset()
			results, err := ep.PersistEvent(ctx, tc.event)
			all, fence := counter.Counts()
			if err != nil {
				t.Fatalf("PersistEvent: %v", err)
			}
			if all != tc.wantAll || fence != tc.wantFence {
				t.Errorf("one persist made %d statements with %d fence reads; want %d with %d",
					all, fence, tc.wantAll, tc.wantFence)
			}
			if got := len(results.Events) > 0; got != tc.wantPersisted {
				t.Errorf("persist returned %d event(s); want persisted=%v", len(results.Events), tc.wantPersisted)
			}
			e1, m1, a1 := rowCounts(t, db)
			if e1-e0 != tc.wantEvents || m1-m0 != tc.wantMeasurements || a1-a0 != tc.wantAnchors {
				t.Errorf("the persist added %d/%d/%d event/measurement/anchor rows; want %d/%d/%d",
					e1-e0, m1-m0, a1-a0, tc.wantEvents, tc.wantMeasurements, tc.wantAnchors)
			}
		})
	}
}

// A fenced tenant's message is refused on event-management's real models, and nothing
// lands in any of the three tables.
func TestAMeasurementMessageForAFencedTenantIsRefused(t *testing.T) {
	ep, db, counter := newFencedPersistenceWorker(t)
	ctx := core.WithTenant(context.Background(), fenceCostTenant)
	event := fenceCostMeasurement("fc-4", time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC), 1)

	plantFence(t, db, fenceCostTenant)
	counter.Reset()
	_, err := ep.PersistEvent(ctx, event)
	all, fence := counter.Counts()
	if !errors.Is(err, rdb.ErrTenantPurged) {
		t.Fatalf("persist for a fenced tenant returned %v; want ErrTenantPurged", err)
	}
	// The altId probe, then the fence read that refuses the first insert.
	if all != 2 || fence != 1 {
		t.Errorf("a refused persist made %d statements with %d fence reads; want 2 with 1", all, fence)
	}
	if e, m, a := rowCounts(t, db); e != 0 || m != 0 || a != 0 {
		t.Errorf("a refused persist left %d/%d/%d event/measurement/anchor rows; want none", e, m, a)
	}

	// Negative control: with the fence lifted the same message lands and the same counts
	// see it, so the zeros above could have been anything else.
	liftFence(t, db, fenceCostTenant)
	if _, err := ep.PersistEvent(ctx, event); err != nil {
		t.Fatalf("persist with the fence lifted: %v", err)
	}
	if e, m, a := rowCounts(t, db); e != 1 || m != 3 || a != 1 {
		t.Errorf("with the fence lifted the persist left %d/%d/%d rows; want 1/3/1", e, m, a)
	}
}

// An unreadable fence is not an absent one: the persist is refused with a fence-read
// error — NOT ErrTenantPurged, the same split core makes — and writes nothing.
func TestAMeasurementMessageFailsClosedOnAnUnreadableFence(t *testing.T) {
	ep, db, _ := newFencedPersistenceWorker(t)
	ctx := core.WithTenant(context.Background(), fenceCostTenant)
	if err := db.Migrator().DropTable(&rdb.PurgedTenant{}); err != nil {
		t.Fatalf("drop the fence table: %v", err)
	}
	_, err := ep.PersistEvent(ctx, fenceCostMeasurement("fc-5", time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC), 1))
	if err == nil || !strings.Contains(err.Error(), "reading the erasure fence") {
		t.Fatalf("persist over an unreadable fence returned %v; want a fence-read error", err)
	}
	if errors.Is(err, rdb.ErrTenantPurged) {
		t.Errorf("an unreadable fence was reported as a purged tenant: %v", err)
	}
	if e, m, a := rowCounts(t, db); e != 0 || m != 0 || a != 0 {
		t.Errorf("a refused persist left %d/%d/%d event/measurement/anchor rows; want none", e, m, a)
	}
}
