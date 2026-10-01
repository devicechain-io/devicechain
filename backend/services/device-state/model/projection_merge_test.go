// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
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

// A batch of data events for 20 devices that already have a row, across two tenants, plus two
// devices seen for the first time, writes device_states in 6 statements: per tenant one lock
// read and one write-back, and one insert per new device. A Save per seen device made it 24.
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

	for _, u := range updates {
		ds := loadRow(t, api, u.Tenant, u.DeviceToken)
		if !ds.LastActivityTime.Valid || !ds.LastActivityTime.Time.Equal(t1) {
			t.Errorf("%s/%s last activity = %v; want %v", u.Tenant, u.DeviceToken, ds.LastActivityTime, t1)
		}
		if ds.TenantId != u.Tenant {
			t.Errorf("%s row holds tenant %q", u.DeviceToken, ds.TenantId)
		}
	}
	var n int64
	if err := api.RDB.DB(core.WithSystemContext(context.Background())).Model(&DeviceState{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 22 {
		t.Errorf("device_states holds %d rows; want 22", n)
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

// The write-back writes the whole folded row over the stored one, so a column the batch's
// events do not touch must come back as it was stored, and the row's identity must not move.
func TestABatchKeepsColumnsItDoesNotFold(t *testing.T) {
	api := newSQLiteProjectionApi(t, "db")
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	if err := mergeOneAtATime(api, data("acme", "k-1", t0)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sys := api.RDB.DB(core.WithSystemContext(context.Background()))
	if err := sys.Exec(`UPDATE device_states SET inactivity_timeout = 123, external_id = 'ext-k' WHERE device_token = ?`,
		"k-1").Error; err != nil {
		t.Fatalf("set columns: %v", err)
	}
	before := loadRow(t, api, "acme", "k-1")
	if before.InactivityTimeout != 123 || before.ExternalId != "ext-k" {
		t.Fatalf("precondition: timeout %d, external id %q", before.InactivityTimeout, before.ExternalId)
	}

	if err := api.MergeProjectionBatch(context.Background(), []ProjectionUpdate{data("acme", "k-1", t1)}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	after := loadRow(t, api, "acme", "k-1")
	if after.InactivityTimeout != 123 || after.ExternalId != "ext-k" {
		t.Errorf("timeout %d, external id %q after the batch; want 123, ext-k", after.InactivityTimeout, after.ExternalId)
	}
	if after.ID != before.ID || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("identity moved: id %d → %d, created %v → %v", before.ID, after.ID, before.CreatedAt, after.CreatedAt)
	}
	if !after.LastActivityTime.Time.Equal(t1) {
		t.Errorf("last activity = %v; want %v", after.LastActivityTime.Time, t1)
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
