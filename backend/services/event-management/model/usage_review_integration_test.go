// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ingestEvent writes one base event row with its event time and its INGEST time (processed_time,
// when the platform received it) set independently.
func ingestEvent(t *testing.T, db *gorm.DB, tenant, device string, occurred, received time.Time) {
	t.Helper()
	usageSeq++
	require.NoError(t, db.Exec(`INSERT INTO "event-management".events
		(tenant_id, event_id, device_token, event_type, occurred_time, source, processed_time)
		VALUES (?, ?, ?, 2, ?, 'it', ?)`, tenant, []byte(fmt.Sprintf("%s-%s-r%d", tenant, device, usageSeq)),
		device, occurred, received).Error)
}

func hourlyEvents(t *testing.T, db *gorm.DB, tenant string, hour time.Time) (int64, bool) {
	t.Helper()
	var rows []struct{ Events int64 }
	require.NoError(t, db.Raw(`SELECT events FROM "event-management".tenant_usage_hourly
		WHERE tenant_id = ? AND hour = ?`, tenant, hour).Scan(&rows).Error)
	if len(rows) == 0 {
		return 0, false
	}
	return rows[0].Events, true
}

func ledgerHours(t *testing.T, db *gorm.DB, day time.Time) int64 {
	t.Helper()
	var rows []struct{ Hours int64 }
	require.NoError(t, db.Raw(`SELECT hours FROM "event-management".tenant_usage_ledger WHERE day = ?::date`,
		day.Format("2006-01-02")).Scan(&rows).Error)
	if len(rows) == 0 {
		return 0
	}
	return rows[0].Hours
}

// An event is counted in the hour (and so the day) the platform RECEIVED it, not the hour it is
// dated: a backfilled reading is counted once, on the day it was ingested, and a day already closed
// is never credited after the fact.
func TestIntegrationUsageCountsByIngestTime(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "usageingest"))
	api := NewApi(mgr)
	db := systemDB(mgr)
	ctx := context.Background()

	ingestDay := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	datedDay := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ingestEvent(t, db, "acme", "dev-1", datedDay.Add(10*time.Hour), ingestDay.Add(12*time.Hour+30*time.Minute))

	now := ingestDay.AddDate(0, 0, 1).Add(2 * time.Hour)
	_, err := api.RollupUsageHours(ctx, ingestDay, ingestDay.Add(23*time.Hour), now)
	require.NoError(t, err)
	_, err = api.RollupUsageHours(ctx, datedDay, datedDay.Add(23*time.Hour), now)
	require.NoError(t, err)

	got, err := api.TenantEventUsage(ctx, "acme", datedDay, ingestDay)
	require.NoError(t, err)
	require.Len(t, got, 10)
	assert.Equal(t, int64(0), got[0].EventsIngested, "the day the reading is DATED is not credited")
	assert.Equal(t, int64(1), got[9].EventsIngested, "the day it was RECEIVED is")
}

// An hour is final only once its settle window has passed after it closed, and once final it is
// never rewritten: an event stored for it later is not added to a figure that may have been billed.
func TestIntegrationUsageHourIsFinalOnlyAfterSettleAndNeverRewritten(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "usagesettle"))
	api := NewApi(mgr)
	db := systemDB(mgr)
	ctx := context.Background()

	day := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	h11 := day.Add(11 * time.Hour)
	ingestEvent(t, db, "acme", "dev-1", h11.Add(30*time.Minute), h11.Add(59*time.Minute))

	// Closed two minutes ago, inside the settle window: computed, but not final.
	_, err := api.RollupUsageHours(ctx, h11, h11, h11.Add(time.Hour+2*time.Minute))
	require.NoError(t, err)
	assert.Zero(t, ledgerHours(t, db, day)&(1<<11), "an hour still settling is not marked final")

	// After the settle window: final.
	_, err = api.RollupUsageHours(ctx, h11, h11, h11.Add(time.Hour+20*time.Minute))
	require.NoError(t, err)
	assert.NotZero(t, ledgerHours(t, db, day)&(1<<11), "a settled hour is final")
	n, ok := hourlyEvents(t, db, "acme", h11)
	require.True(t, ok)
	assert.Equal(t, int64(1), n)

	// A row stored for the hour after it went final does not move it.
	ingestEvent(t, db, "acme", "dev-2", h11.Add(40*time.Minute), h11.Add(59*time.Minute+30*time.Second))
	_, err = api.RollupUsageHours(ctx, h11, h11, h11.Add(2*time.Hour))
	require.NoError(t, err)
	n, _ = hourlyEvents(t, db, "acme", h11)
	assert.Equal(t, int64(1), n, "a final hour is never rewritten")
}

// A tenant whose erasure is in progress (its fence stands) is not rolled up: its events are being
// deleted, and a usage row written now would be tenant data the purge has to chase.
func TestIntegrationUsageSkipsAFencedTenant(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "usagefence"))
	api := NewApi(mgr)
	db := systemDB(mgr)
	ctx := context.Background()

	h := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	ingestEvent(t, db, "acme", "dev-1", h.Add(time.Minute), h.Add(time.Minute))
	ingestEvent(t, db, "gone", "dev-1", h.Add(time.Minute), h.Add(time.Minute))
	require.NoError(t, db.Exec(`INSERT INTO "event-management".purged_tenants (token, epoch, planted_at)
		VALUES ('gone', ?, ?)`, h, h).Error)

	_, err := api.RollupUsageHours(ctx, h, h, h.Add(time.Hour+20*time.Minute))
	require.NoError(t, err)
	_, ok := hourlyEvents(t, db, "acme", h)
	assert.True(t, ok, "a bystander is rolled up")
	_, ok = hourlyEvents(t, db, "gone", h)
	assert.False(t, ok, "a fenced tenant is not")
}

// Recomputing an hour that is not yet final replaces its rows: a tenant with no events in it any
// more (they were erased) keeps no stale row.
func TestIntegrationUsageRecomputeReplacesTheHoursRows(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "usagerecompute"))
	api := NewApi(mgr)
	db := systemDB(mgr)
	ctx := context.Background()

	h := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	ingestEvent(t, db, "acme", "dev-1", h.Add(time.Minute), h.Add(time.Minute))
	ingestEvent(t, db, "globex", "dev-1", h.Add(time.Minute), h.Add(time.Minute))
	_, err := api.RollupUsageHours(ctx, h, h, h.Add(time.Hour+time.Minute))
	require.NoError(t, err)
	_, ok := hourlyEvents(t, db, "globex", h)
	require.True(t, ok)

	require.NoError(t, db.Exec(`DELETE FROM "event-management".events WHERE tenant_id = 'globex'`).Error)
	_, err = api.RollupUsageHours(ctx, h, h, h.Add(time.Hour+2*time.Minute))
	require.NoError(t, err)
	_, ok = hourlyEvents(t, db, "globex", h)
	assert.False(t, ok, "the stale row is gone")
	_, ok = hourlyEvents(t, db, "acme", h)
	assert.True(t, ok)
}
