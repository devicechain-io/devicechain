// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/presence"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"gorm.io/gorm"
)

// The batch's write-back of device rows it has locked and folded (writeFoldedStates): what it
// costs, and what it must keep from the per-device Save it replaced.

// countedApi is an Api over the same database as api whose statements counter counts; the
// batch's own handle (api.RDB.DB) is built from it, so every statement the batch sends is seen.
func countedApi(api *Api, counter *rdbtest.StatementCounter) *Api {
	return NewApi(&rdb.RdbManager{Database: api.RDB.Database.Session(&gorm.Session{Logger: counter})})
}

// loadRow reads one device's row as the system, whatever its tenant.
func loadRow(t *testing.T, api *Api, tenant, tok string) DeviceState {
	t.Helper()
	var ds DeviceState
	if err := api.RDB.DB(core.WithSystemContext(context.Background())).
		Where("tenant_id = ? AND device_token = ?", tenant, tok).First(&ds).Error; err != nil {
		t.Fatalf("load %s/%s: %v", tenant, tok, err)
	}
	return ds
}

// A batch for 20 devices that already have a row, across two tenants, plus two devices seen for
// the first time, writes device_states in 6 statements when it carries a presence claim per
// tenant (the locked path): per tenant one lock read and one write-back, and one insert per new
// device. A Save per seen device made it 24. A data-only batch is the heartbeat path and is
// the next test.
func TestABatchWritesEveryExistingDeviceStateInOneStatement(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	var updates []ProjectionUpdate
	for _, tenant := range []string{"acme", "beta"} {
		for i := 0; i < 10; i++ {
			tok := fmt.Sprintf("seen-%02d", i)
			if err := mergeOneAtATime(api, data(tenant, tok, t0)); err != nil {
				t.Fatalf("seed %s/%s: %v", tenant, tok, err)
			}
			if i == 0 {
				updates = append(updates, claim(tenant, tok, t1, presence.ClaimConnected, 1))
				continue
			}
			updates = append(updates, data(tenant, tok, t1))
		}
	}
	updates = append(updates, data("acme", "new-1", t1), data("acme", "new-2", t1))

	counter := rdbtest.NewStatementCounter("device_states")
	counter.Reset()
	if err := countedApi(api, counter).MergeProjectionBatch(context.Background(), updates); err != nil {
		t.Fatalf("batch: %v", err)
	}
	if _, marked := counter.Counts(); marked != 6 {
		t.Errorf("the batch sent %d statements on device_states; want 6 (2 lock reads, 2 inserts, 2 write-backs)", marked)
	}
	assertAllAt(t, api, updates, t1, 22)
}

// A data-only batch for the same 22 devices writes device_states in ONE statement per tenant:
// no lock read, no per-device insert, no separate write-back — the heartbeat upsert.
func TestADataOnlyBatchWritesEveryDeviceStateInOneStatementPerTenant(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	var updates []ProjectionUpdate
	for _, tenant := range []string{"acme", "beta"} {
		for i := 0; i < 10; i++ {
			tok := fmt.Sprintf("seen-%02d", i)
			if err := mergeOneAtATime(api, data(tenant, tok, t0)); err != nil {
				t.Fatalf("seed %s/%s: %v", tenant, tok, err)
			}
			updates = append(updates, data(tenant, tok, t1))
		}
	}
	updates = append(updates, data("acme", "new-1", t1), data("acme", "new-2", t1))

	counter := rdbtest.NewStatementCounter("device_states")
	counter.Reset()
	if err := countedApi(api, counter).MergeProjectionBatch(context.Background(), updates); err != nil {
		t.Fatalf("batch: %v", err)
	}
	if _, marked := counter.Counts(); marked != 2 {
		t.Errorf("the batch sent %d statements on device_states; want 2 (one heartbeat upsert per tenant)", marked)
	}
	assertAllAt(t, api, updates, t1, 22)
}

// assertAllAt asserts every update's device holds last activity at, under its own tenant, and
// that device_states holds total rows.
func assertAllAt(t *testing.T, api *Api, updates []ProjectionUpdate, at time.Time, total int64) {
	t.Helper()
	for _, u := range updates {
		ds := loadRow(t, api, u.Tenant, u.DeviceToken)
		if !ds.LastActivityTime.Valid || !ds.LastActivityTime.Time.Equal(at) {
			t.Errorf("%s/%s last activity = %v; want %v", u.Tenant, u.DeviceToken, ds.LastActivityTime, at)
		}
		if ds.TenantId != u.Tenant {
			t.Errorf("%s row holds tenant %q", u.DeviceToken, ds.TenantId)
		}
	}
	var n int64
	if err := api.RDB.DB(core.WithSystemContext(context.Background())).Model(&DeviceState{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != total {
		t.Errorf("device_states holds %d rows; want %d", n, total)
	}
}

// Save stamped updated_at on every write, and the GraphQL updatedAt shows it. A Create stamps
// it only when it is zero, which a row read back never is — so the write-back must stamp it,
// on EVERY row it writes. Three devices in each of two tenants, so each tenant's write-back
// carries rows after its first: a stamp on rows[0] alone would leave four rows behind.
func TestABatchAdvancesUpdatedAtOnTheRowsItWritesBack(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	sys := api.RDB.DB(core.WithSystemContext(context.Background()))
	var updates []ProjectionUpdate
	for _, tenant := range []string{"acme", "beta"} {
		for _, tok := range []string{"u-1", "u-2", "u-3"} {
			if err := mergeOneAtATime(api, data(tenant, tok, t0)); err != nil {
				t.Fatalf("seed %s/%s: %v", tenant, tok, err)
			}
			updates = append(updates, data(tenant, tok, t1))
		}
	}
	if err := sys.Exec(`UPDATE device_states SET updated_at = ?`, old).Error; err != nil {
		t.Fatalf("backdate: %v", err)
	}
	// Precondition: the read below can see the old stamp, on every row.
	for _, u := range updates {
		if got := loadRow(t, api, u.Tenant, u.DeviceToken).UpdatedAt; !got.Equal(old) {
			t.Fatalf("precondition: %s/%s updated_at = %v after backdating; want %v", u.Tenant, u.DeviceToken, got, old)
		}
	}

	if err := api.MergeProjectionBatch(context.Background(), updates); err != nil {
		t.Fatalf("batch: %v", err)
	}
	for _, u := range updates {
		ds := loadRow(t, api, u.Tenant, u.DeviceToken)
		if !ds.UpdatedAt.After(old.AddDate(0, 0, 1)) {
			t.Errorf("%s/%s updated_at = %v after the batch wrote the row; want the write time", u.Tenant, u.DeviceToken, ds.UpdatedAt)
		}
		if !ds.LastActivityTime.Time.Equal(t1) {
			t.Errorf("%s/%s last activity = %v; want %v", u.Tenant, u.DeviceToken, ds.LastActivityTime.Time, t1)
		}
	}
}

// The write-back writes back what the fold touched and nothing else, so a column the batch's
// events do not touch must come back as it was stored — including one an operator set that the
// lock read never carried (inactivity_timeout) — and the row's identity must not move. It is
// asserted for each way a batch writes a row it has locked: the locked path (a claim), and the
// heartbeat upsert's two arms (a live device, and a swept one folded by the fallback).
func TestABatchKeepsColumnsItDoesNotFold(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	for _, tc := range []struct {
		name  string
		sweep bool
		batch ProjectionUpdate
	}{
		{"locked path", false, claim("acme", "k-1", t1, presence.ClaimConnected, 1)},
		{"heartbeat upsert, live device", false, data("acme", "k-1", t1)},
		{"heartbeat upsert, swept device", true, data("acme", "k-1", t1.Add(2*time.Hour))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newSQLiteProjectionApi(t, "db")
			if err := mergeOneAtATime(api, data("acme", "k-1", t0)); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if tc.sweep {
				if _, err := api.SweepInactive(core.WithSystemContext(context.Background()), t0.Add(3*time.Hour)); err != nil {
					t.Fatalf("sweep: %v", err)
				}
			}
			sys := api.RDB.DB(core.WithSystemContext(context.Background()))
			if err := sys.Exec(`UPDATE device_states SET inactivity_timeout = 123, external_id = 'ext-k' WHERE device_token = ?`,
				"k-1").Error; err != nil {
				t.Fatalf("set columns: %v", err)
			}
			before := loadRow(t, api, "acme", "k-1")
			if before.InactivityTimeout != 123 || before.ExternalId != "ext-k" || before.Active == tc.sweep {
				t.Fatalf("precondition: timeout %d, external id %q, active %v", before.InactivityTimeout, before.ExternalId, before.Active)
			}

			if err := api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{tc.batch}); err != nil {
				t.Fatalf("batch: %v", err)
			}
			after := loadRow(t, api, "acme", "k-1")
			if after.InactivityTimeout != 123 || after.ExternalId != "ext-k" {
				t.Errorf("timeout %d, external id %q after the batch; want 123, ext-k", after.InactivityTimeout, after.ExternalId)
			}
			if after.ID != before.ID || !after.CreatedAt.Equal(before.CreatedAt) {
				t.Errorf("identity moved: id %d → %d, created %v → %v", before.ID, after.ID, before.CreatedAt, after.CreatedAt)
			}
			if !after.LastActivityTime.Time.Equal(tc.batch.OccurredAt) {
				t.Errorf("last activity = %v; want %v", after.LastActivityTime.Time, tc.batch.OccurredAt)
			}
		})
	}
}

// The lock read selects exactly the columns the fold touches (and the id and token that name the
// row), not the whole row: the same list the write-back writes. Recorded from the Query callback,
// where gorm still holds the Select. The two reads that lock are the locked path's, and the
// heartbeat upsert's fallback for a swept device.
func TestTheLockReadSelectsOnlyTheColumnsTheFoldTouches(t *testing.T) {
	want := []string{"active", "external_id", "id", "device_token", "inactivity_alarm_time", "last_activity_time",
		"last_connect_time", "last_disconnect_time", "presence_source", "presence_time", "session_id", "source",
		"scope_anchors", "scope_anchors_at"} // the owners stamp is folded too (applyOwners)
	sort.Strings(want)
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		sweep bool
		batch ProjectionUpdate
	}{
		{"locked path", false, claim("acme", "s-1", t0.Add(time.Minute), presence.ClaimConnected, 1)},
		{"heartbeat fallback", true, data("acme", "s-1", t0.Add(2*time.Hour+time.Minute))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newSQLiteProjectionApi(t, "db")
			if err := mergeOneAtATime(api, data("acme", "s-1", t0)); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if tc.sweep {
				if _, err := api.SweepInactive(core.WithSystemContext(context.Background()), t0.Add(2*time.Hour)); err != nil {
					t.Fatalf("sweep: %v", err)
				}
			}
			var reads [][]string
			if err := api.RDB.Database.Callback().Query().Before("gorm:query").Register("test:record_selects", func(tx *gorm.DB) {
				if tx.Statement.Table != "device_states" {
					return
				}
				if _, locking := tx.Statement.Clauses["FOR"]; locking {
					got := append([]string(nil), tx.Statement.Selects...)
					sort.Strings(got)
					reads = append(reads, got)
				}
			}); err != nil {
				t.Fatalf("register callback: %v", err)
			}
			if err := api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{tc.batch}); err != nil {
				t.Fatalf("batch: %v", err)
			}
			if len(reads) != 1 {
				t.Fatalf("%d lock reads of device_states; want 1", len(reads))
			}
			if strings.Join(reads[0], ",") != strings.Join(want, ",") {
				t.Errorf("the lock read selected %v; want %v", reads[0], want)
			}
		})
	}
}

// The write-back is a gorm Create, so the tenant-scope callback runs on it as it ran on Save:
// a row naming another tenant than the statement's is refused, and nothing is written.
func TestABatchWriteBackIsRefusedForAnotherTenantsRow(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if err := mergeOneAtATime(api, data("beta", "x-1", t0)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	row := loadRow(t, api, "beta", "x-1")
	row.LastActivityTime.Time = t0.Add(time.Hour)

	err := writeFoldedStates(api.RDB.DB(core.WithTenant(context.Background(), "acme")), []DeviceState{row})
	if !errors.Is(err, rdb.ErrTenantMismatch) {
		t.Fatalf("write-back of beta's row under acme: err = %v; want rdb.ErrTenantMismatch", err)
	}
	if got := loadRow(t, api, "beta", "x-1").LastActivityTime.Time; !got.Equal(t0) {
		t.Errorf("beta's row moved to %v; want %v", got, t0)
	}
}

// A Create writes a column's declared default in place of a zero value, where Save wrote the
// zero. The write-back refuses such a row rather than persist a value the fold never produced.
func TestABatchWriteBackRefusesAZeroUnderADefault(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if err := mergeOneAtATime(api, data("acme", "z-1", t0)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	row := loadRow(t, api, "acme", "z-1")
	row.PresenceSource = ""
	row.LastActivityTime.Time = t0.Add(time.Hour)

	err := writeFoldedStates(api.RDB.DB(core.WithTenant(context.Background(), "acme")), []DeviceState{row})
	if err == nil || !strings.Contains(err.Error(), "presence_source") {
		t.Fatalf("write-back of a zero presence source: err = %v; want a refusal naming presence_source", err)
	}
	if got := loadRow(t, api, "acme", "z-1").LastActivityTime.Time; !got.Equal(t0) {
		t.Errorf("the refused row was written: activity %v; want %v", got, t0)
	}
}

// The heartbeat upsert sends a row to the Go fold (a lock read and a write-back) only when the
// row's guard excludes it: an inferred, inactive device that the batch's latest event is fresh
// for. A swept device given only events older than its recorded activity cannot flip, so the
// upsert writes it and no fallback runs. Value tests cannot tell a guard that excludes too
// much from a correct one, because the fallback's fold produces the same row; only the
// statement count can.
func TestAHeartbeatBatchFallsBackOnlyForADeviceItCanBringBack(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		at     time.Time
		want   int64
		active bool
	}{
		{"an older event cannot bring it back: the upsert alone", t0.Add(-time.Second), 1, false},
		{"a fresh event brings it back: upsert, lock read, write-back", t0.Add(time.Second), 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newSQLiteProjectionApi(t, "db")
			if err := mergeOneAtATime(api, data("acme", "swept", t0)); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if n, err := api.SweepInactive(core.WithSystemContext(context.Background()), t0.Add(2*time.Hour)); err != nil || n != 1 {
				t.Fatalf("sweep flipped %d, err %v; want 1", n, err)
			}
			counter := rdbtest.NewStatementCounter("device_states")
			counter.Reset()
			if err := countedApi(api, counter).MergeProjectionBatch(context.Background(),
				[]ProjectionUpdate{data("acme", "swept", tc.at)}); err != nil {
				t.Fatalf("batch: %v", err)
			}
			if _, marked := counter.Counts(); marked != tc.want {
				t.Errorf("the batch sent %d statements on device_states; want %d", marked, tc.want)
			}
			if ds := loadRow(t, api, "acme", "swept"); ds.Active != tc.active {
				t.Errorf("active = %v; want %v", ds.Active, tc.active)
			}
		})
	}
}

// The heartbeat upsert takes its rows' locks in the order its VALUES list them, so it must list
// them in token order: two writers upserting overlapping devices in different orders would
// deadlock on PostgreSQL. The devices are given in reverse order, across enough of them that a
// map's iteration order would not come out sorted by chance.
func TestTheHeartbeatUpsertListsItsRowsInTokenOrder(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	var seen []string
	if err := api.RDB.Database.Callback().Create().Before("gorm:create").Register("test:values_order", func(tx *gorm.DB) {
		if rows, ok := tx.Statement.Dest.(*[]DeviceState); ok && tx.Statement.Table == "device_states" {
			for _, r := range *rows {
				seen = append(seen, r.DeviceToken)
			}
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	var updates []ProjectionUpdate
	for i := 31; i >= 0; i-- {
		updates = append(updates, data("acme", fmt.Sprintf("dev-%02d", i), t0))
	}
	if err := api.MergeProjectionBatch(context.Background(), updates); err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(seen) != 32 {
		t.Fatalf("the upsert carried %d rows; want 32", len(seen))
	}
	if !sort.StringsAreSorted(seen) {
		t.Errorf("the upsert listed its rows as %v; want token order", seen)
	}
}

// The heartbeat upsert is a gorm Create, so core/rdb's tenant rules apply to it: with no tenant
// bound it fails closed, and with one its update arm is confined to that tenant's rows (the
// guard core/rdb prepends to the statement's own WHERE).
func TestTheHeartbeatUpsertKeepsTheTenantRules(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	updates := []ProjectionUpdate{data("acme", "d1", t0)}
	devices := map[string][]int{"d1": {0}}
	if err := mergeHeartbeats(api.RDB.DB(context.Background()), []string{"d1"}, devices, updates); !errors.Is(err, core.ErrNoTenant) {
		t.Fatalf("a heartbeat upsert with no tenant in context returned %v; want core.ErrNoTenant", err)
	}
	var sql string
	if err := api.RDB.Database.Callback().Create().After("gorm:create").Register("test:upsert_sql", func(tx *gorm.DB) {
		if tx.Statement.Table == "device_states" {
			sql = tx.Statement.SQL.String()
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	if err := api.MergeProjectionBatch(context.Background(), updates); err != nil {
		t.Fatalf("batch: %v", err)
	}
	if !strings.Contains(sql, "`device_states`.`tenant_id` = `excluded`.`tenant_id` AND (") {
		t.Errorf("the heartbeat upsert's update arm is not confined to its tenant: %s", sql)
	}
}

// THE LOCKED PATH INSERTS THE BLANK ROW AND FOLDS EVERY EVENT INTO IT. lockOrCreate takes a new
// device's row by inserting blankDeviceState, and the fold then runs applyEvent over it — where
// merging one event at a time builds the row with newDeviceState. The two must leave the same
// row for EVERY possible first event, or the locked path would leave something one-at-a-time
// does not: data with and without identity, each claim at session zero and above, a claim with
// a compare-and-set, and the invalid claim.
func TestABlankRowFoldsToTheNewRow(t *testing.T) {
	at := storedTime(time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
	ids := []DeviceIdentity{{}, {ExternalId: "ext-1"}, {Source: "mqtt1"}, {ExternalId: "ext-1", Source: "mqtt1"}}
	events := []*PresenceTransition{nil}
	for _, c := range []presence.Claim{presence.ClaimUnset, presence.ClaimConnected, presence.ClaimDisconnected,
		presence.ClaimDemoted, presence.Claim(99)} {
		for _, session := range []uint64{0, 7} {
			events = append(events,
				&PresenceTransition{Claim: c, SessionId: session, OccurredAt: at},
				&PresenceTransition{Claim: c, SessionId: session, ExpectedSessionId: 3, OccurredAt: at})
		}
	}
	for _, id := range ids {
		for _, pt := range events {
			want := newDeviceState("d1", at, pt, id)
			got := blankDeviceState("d1")
			applyEvent(got, at, pt, id)
			if !reflect.DeepEqual(*got, *want) {
				t.Errorf("first event %+v with identity %+v:\n  blank+applyEvent = %+v\n  newDeviceState   = %+v",
					pt, id, *got, *want)
			}
		}
	}
}

// 🔴 EVERY BATCH TAKES ITS DEVICE ROWS IN ONE STATEMENT, IN TOKEN ORDER. Two batches can deadlock
// only by taking two rows in opposite orders, and the order an INSERT … ON CONFLICT takes rows in
// is its VALUES order — for a row it inserts and a row it conflicts on alike. So the first
// statement either path sends to device_states must be that INSERT, naming every device of the
// batch in ascending token order, and nothing may reach device_states before it. The batch here
// is the shape that deadlocked: devices that are NEW sort below devices that EXIST (the locked
// path used to lock the existing rows in a read, then insert the new ones, so it took lo-03
// before lo-00 where the heartbeat upsert takes lo-00 first). The updates arrive out of order,
// so a path that stopped sorting fails here too.
func TestEveryBatchPathTakesItsDeviceRowsInOneTokenOrderedStatement(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	tokenRE := regexp.MustCompile(`lo-\d\d`)
	for _, tc := range []struct {
		name      string
		withClaim bool
	}{
		{"locked path (a claim in the batch)", true},
		{"heartbeat path (data only)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newSQLiteProjectionApi(t, "order")
			existing := []string{"lo-03", "lo-04", "lo-05"}
			var seed []ProjectionUpdate
			for _, tok := range existing {
				seed = append(seed, data("acme", tok, t0))
			}
			if err := api.MergeProjectionBatch(context.Background(), seed); err != nil {
				t.Fatalf("seed: %v", err)
			}
			at := t0.Add(time.Minute)
			batch := []ProjectionUpdate{
				data("acme", "lo-05", at), data("acme", "lo-01", at), data("acme", "lo-03", at),
				data("acme", "lo-00", at), data("acme", "lo-04", at), data("acme", "lo-02", at),
			}
			if tc.withClaim {
				batch[2] = claim("acme", "lo-03", at, presence.ClaimConnected, 1)
			}
			want := []string{"lo-00", "lo-01", "lo-02", "lo-03", "lo-04", "lo-05"}

			counter := rdbtest.NewStatementCounter("device_states")
			counter.Record(true)
			if err := countedApi(api, counter).MergeProjectionBatch(context.Background(), batch); err != nil {
				t.Fatalf("MergeProjectionBatch: %v", err)
			}
			sqls := counter.MarkedSQL()
			if len(sqls) == 0 {
				t.Fatal("no statement reached device_states")
			}
			first := sqls[0]
			if !strings.HasPrefix(strings.TrimSpace(first), "INSERT") || !strings.Contains(first, "ON CONFLICT") {
				t.Fatalf("the batch's first statement on device_states is not its INSERT … ON CONFLICT; it is:\n  %s", first)
			}
			if got := tokenRE.FindAllString(first, -1); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("the first statement takes the devices in the order %v; want every device, ascending: %v", got, want)
			}
			for _, tok := range want {
				if row := loadRow(t, api, "acme", tok); !row.LastActivityTime.Time.Equal(at) {
					t.Errorf("%s: activity %v; want %v", tok, row.LastActivityTime.Time, at)
				}
			}
		})
	}
}
