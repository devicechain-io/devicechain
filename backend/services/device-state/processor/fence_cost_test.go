// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// The erasure fence reads purged_tenants before a tenant-bearing create or update, at most
// once per tenant per transaction. These tests pin, BY VALUE, what that costs on
// device-state's per-event write path — the statements mergeOne makes and how many of them are fence reads — and
// prove the fence actually fires on this area's real models along that path.
//
// 🔴 THE COUNTS ARE HALF OF IT, AND THE WEAKER HALF. A later change that makes the path
// cheaper by disarming the fence LOWERS every count, so a count test alone would go
// green over the defect. The refusal and fail-closed tests beside them are what turn
// that change red: they are the guard on the side the defect is on.
//
// The numbers are STATEMENTS, not round trips: gorm does not trace BEGIN/COMMIT, and a
// merge opens two transactions (MergeDeviceState, then the latest-value merge) — which is
// also why a merge that writes both pays two fence reads, never fewer.

// fenceTenant is the tenant the shared message subject carries (locationTestSubject).
const fenceTenant = "tenant1"

// recordingAck counts acks, so a test can tell a merged message (acked) from one left
// for redelivery (unacked) — the only observable difference mergeOne leaves behind.
type recordingAck struct{ n atomic.Int32 }

func (a *recordingAck) Ack() error {
	a.n.Add(1)
	return nil
}

// consumed turns a produced message into a consumed one carrying ack. NumDelivered is 1,
// below MaxDeliver, so a refused write is left UNACKED for redelivery rather than dropped.
func consumed(m messaging.Message, ack *recordingAck) messaging.Message {
	return messaging.NewConsumedMessage(m.Subject, m.Value, 1, nil, ack)
}

// newFencedStateProcessor builds a StateProcessor over sqlite with the PRODUCTION
// callback chain, in production order (core/rdb/postgres.go): scoping, token grammar,
// audit journal, erasure fence. Parity is the point — core's own fence test registers the
// journal for the same reason, and a count taken over a different callback set is a
// count of something that does not run.
//
// The database is shared-cache and named per test: a plain ":memory:" gives each pool
// connection a database of its own, so a fence planted outside a transaction could land
// in a different database from the one the merge reads.
func newFencedStateProcessor(t *testing.T) (*StateProcessor, *gorm.DB, *rdbtest.StatementCounter) {
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
		&model.DeviceState{}, &model.LatestMeasurement{}, &model.LatestLocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sp := NewStateProcessor(deviceStateMicroservice, nil, core.NewNoOpLifecycleCallbacks(),
		model.NewApi(&rdb.RdbManager{Database: db}),
		NewStateMetrics(deviceStateMicroservice),
		deviceStateMicroservice.NewPeriodicTaskMetrics("fence_cost"))
	return sp, db, counter
}

// systemDB is a session under a system context: the fence plant and lift run that way,
// as the purge coordinator does, because the audit journal's own row is tenant-scoped and
// would otherwise fail closed with no tenant in context.
func systemDB(db *gorm.DB) *gorm.DB {
	return db.Session(&gorm.Session{NewDB: true}).WithContext(core.WithSystemContext(context.Background()))
}

// plantFence stands a fence for token, as the purge's first pass does.
func plantFence(t *testing.T, db *gorm.DB, token string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := systemDB(db).Create(&rdb.PurgedTenant{Token: token, Epoch: now, PlantedAt: now}).Error; err != nil {
		t.Fatalf("plant a fence for %q: %v", token, err)
	}
}

// liftFence completes every standing fence for token, as the releasing pass does.
func liftFence(t *testing.T, db *gorm.DB, token string) {
	t.Helper()
	if err := systemDB(db).Model(&rdb.PurgedTenant{}).
		Where("token = ? AND completed_at IS NULL", token).
		Update("completed_at", time.Now().UTC()).Error; err != nil {
		t.Fatalf("lift the fence for %q: %v", token, err)
	}
}

// threeMetrics is a 3-metric measurement message, every sample at one instant.
func threeMetrics(t *testing.T, device string, at time.Time) messaging.Message {
	t.Helper()
	return measurementMessage(t, device, at, []struct {
		at    time.Time
		name  string
		value string
	}{
		{at, "temperature", "21.5"},
		{at, "humidity", "40.25"},
		{at, "pressure", "1013.5"},
	})
}

// aFix is a location message carrying one fix at at.
func aFix(t *testing.T, device string, at time.Time, lat string) messaging.Message {
	t.Helper()
	return locationMessage(t, device, at, []dmmodel.ResolvedLocationEntry{{Latitude: str(lat), Longitude: str("-81.5")}})
}

// mergeCounted merges one message with the counter reset around exactly that merge, and
// returns the statements it made, the fence reads among them, and whether it was acked.
func mergeCounted(sp *StateProcessor, counter *rdbtest.StatementCounter, m messaging.Message) (all, fence int64, acked int32) {
	ack := &recordingAck{}
	counter.Reset()
	sp.mergeOne(context.Background(), consumed(m, ack))
	all, fence = counter.Counts()
	return all, fence, ack.n.Load()
}

// seed merges m outside any measurement and fails the test unless it was acked.
func seed(t *testing.T, sp *StateProcessor, m messaging.Message) {
	t.Helper()
	ack := &recordingAck{}
	sp.mergeOne(context.Background(), consumed(m, ack))
	if ack.n.Load() != 1 {
		t.Fatalf("seeding merge was not acked (acks=%d)", ack.n.Load())
	}
}

var metricNames = []string{"temperature", "humidity", "pressure"}

// assertProjectedAt fails unless the device's activity and every metric's latest value
// sit at want — the evidence that a counted merge actually WROTE, so a write that
// silently failed cannot pass on the same statement count.
func assertProjectedAt(t *testing.T, sp *StateProcessor, device string, want time.Time) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), fenceTenant)
	if got := loadState(t, sp, ctx, device).LastActivityTime.Time; !got.Equal(want) {
		t.Errorf("LastActivityTime = %v, want %v", got, want)
	}
	for _, name := range metricNames {
		if got := loadProjectedMeasurement(t, sp, ctx, device, name).OccurredTime; !got.Equal(want) {
			t.Errorf("latest %s OccurredTime = %v, want %v", name, got, want)
		}
	}
}

func TestFenceCostOfAMeasurementEvent(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)

	// Each subtest builds its own database (the DSN is per test name), so each one seeds
	// what it needs itself and never inherits a sibling's rows.
	cases := []struct {
		name string
		// seed runs before the counter is reset; nil for none.
		seed func(t *testing.T, sp *StateProcessor)
		msg  func(t *testing.T) messaging.Message
		// wantAll / wantFence are the statements and fence reads of the ONE counted merge.
		wantAll, wantFence int64
		// check asserts the projection moved (or, for the stale case, did not).
		check func(t *testing.T, sp *StateProcessor)
	}{
		{
			// Rows created: device state (lock read, fence, insert), then per metric a lock
			// read and an insert. One fence read per transaction: device state, then latest
			// measurements, whose three inserts share the one read.
			name:    "first sight",
			msg:     func(t *testing.T) messaging.Message { return threeMetrics(t, "fc-01", t0) },
			wantAll: 10, wantFence: 2,
			check: func(t *testing.T, sp *StateProcessor) { assertProjectedAt(t, sp, "fc-01", t0) },
		},
		{
			// The steady state: every reading newer than the stored one, so every row is
			// updated. One fence read per transaction: device state, then latest
			// measurements, whose three updates share the one read.
			name:    "every reading newer",
			seed:    func(t *testing.T, sp *StateProcessor) { seed(t, sp, threeMetrics(t, "fc-01", t0)) },
			msg:     func(t *testing.T) messaging.Message { return threeMetrics(t, "fc-01", t1) },
			wantAll: 10, wantFence: 2,
			check: func(t *testing.T, sp *StateProcessor) { assertProjectedAt(t, sp, "fc-01", t1) },
		},
		{
			// A redelivery of the message already applied. MergeDeviceState still saves
			// its row (and pays the fence read); the latest-value merge only reads, because
			// no reading is strictly newer, so it pays none.
			name:    "stale redelivery",
			seed:    func(t *testing.T, sp *StateProcessor) { seed(t, sp, threeMetrics(t, "fc-01", t1)) },
			msg:     func(t *testing.T) messaging.Message { return threeMetrics(t, "fc-01", t1) },
			wantAll: 6, wantFence: 1,
			check: func(t *testing.T, sp *StateProcessor) { assertProjectedAt(t, sp, "fc-01", t1) },
		},
		{
			// A newer fix: device state and the last-known position, one fence read each
			// (two transactions, one fenced write in each).
			name:    "location event",
			seed:    func(t *testing.T, sp *StateProcessor) { seed(t, sp, aFix(t, "fc-01", t0, "28.5")) },
			msg:     func(t *testing.T) messaging.Message { return aFix(t, "fc-01", t1, "28.75") },
			wantAll: 6, wantFence: 2,
			check: func(t *testing.T, sp *StateProcessor) {
				ctx := core.WithTenant(context.Background(), fenceTenant)
				if got := loadProjectedLocation(t, sp, ctx, "fc-01"); !got.OccurredTime.Equal(t1) ||
					got.Latitude.Float64 != 28.75 {
					t.Errorf("latest location = %v @ %v, want 28.75 @ %v", got.Latitude.Float64, got.OccurredTime, t1)
				}
				if got := loadState(t, sp, ctx, "fc-01").LastActivityTime.Time; !got.Equal(t1) {
					t.Errorf("LastActivityTime = %v, want %v", got, t1)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp, _, counter := newFencedStateProcessor(t)
			if tc.seed != nil {
				tc.seed(t, sp)
			}
			all, fence, acked := mergeCounted(sp, counter, tc.msg(t))
			if all != tc.wantAll || fence != tc.wantFence {
				t.Errorf("one merge made %d statements with %d fence reads; want %d with %d",
					all, fence, tc.wantAll, tc.wantFence)
			}
			if acked != 1 {
				t.Errorf("the merge was acked %d times, want 1", acked)
			}
			tc.check(t, sp)
		})
	}
}

// A fenced tenant's event is refused on device-state's real models and left for
// redelivery, and the projection it would have advanced does not move.
//
// This is the test that stops a later change from making the path cheaper by disarming
// the fence: the counts above would go DOWN under that change and still pass their own
// arithmetic if rewritten, while this one goes red.
func TestAMeasurementEventForAFencedTenantIsRefusedAndLeftForRedelivery(t *testing.T) {
	sp, db, counter := newFencedStateProcessor(t)
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	seed(t, sp, threeMetrics(t, "fc-02", t0))

	plantFence(t, db, fenceTenant)
	next := threeMetrics(t, "fc-02", t1)
	all, fence, acked := mergeCounted(sp, counter, next)
	// The lock read, then the fence read that refuses. The latest-value merge never runs.
	if all != 2 || fence != 1 {
		t.Errorf("a refused merge made %d statements with %d fence reads; want 2 with 1", all, fence)
	}
	if acked != 0 {
		t.Fatalf("a refused merge was acked %d time(s); it must be left for redelivery", acked)
	}
	assertProjectedAt(t, sp, "fc-02", t0)

	// Negative control: with the fence lifted, the SAME message lands and the SAME reads
	// see it — so the assertions above could have seen a write had one happened.
	liftFence(t, db, fenceTenant)
	if _, _, acked := mergeCounted(sp, counter, next); acked != 1 {
		t.Fatalf("with the fence lifted the merge was acked %d time(s), want 1", acked)
	}
	assertProjectedAt(t, sp, "fc-02", t1)
}

// An unreadable fence is not an absent one: the merge is refused and left unacked.
func TestAMeasurementEventFailsClosedOnAnUnreadableFence(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)

	t.Run("through mergeOne", func(t *testing.T) {
		sp, db, counter := newFencedStateProcessor(t)
		seed(t, sp, threeMetrics(t, "fc-03", t0))
		if err := db.Migrator().DropTable(&rdb.PurgedTenant{}); err != nil {
			t.Fatalf("drop the fence table: %v", err)
		}
		if _, _, acked := mergeCounted(sp, counter, threeMetrics(t, "fc-03", t1)); acked != 0 {
			t.Fatalf("a merge over an unreadable fence was acked %d time(s), want 0", acked)
		}
		assertProjectedAt(t, sp, "fc-03", t0)
	})

	// mergeOne folds the error into its retry disposition, so the REASON is only visible
	// one level down. This asserts it is the fence read that refused, not some other fault.
	t.Run("the reason, through the Api", func(t *testing.T) {
		sp, db, _ := newFencedStateProcessor(t)
		seed(t, sp, threeMetrics(t, "fc-03", t0))
		if err := db.Migrator().DropTable(&rdb.PurgedTenant{}); err != nil {
			t.Fatalf("drop the fence table: %v", err)
		}
		ctx := core.WithTenant(context.Background(), fenceTenant)
		_, err := sp.Api.MergeDeviceState(ctx, "fc-03", t1, nil, model.DeviceIdentity{Source: mqttTestSource})
		if err == nil || !strings.Contains(err.Error(), "reading the erasure fence") {
			t.Fatalf("MergeDeviceState over an unreadable fence returned %v; want a fence-read error", err)
		}
	})
}
