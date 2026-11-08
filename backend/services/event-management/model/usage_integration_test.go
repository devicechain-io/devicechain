// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Integration test for the per-tenant daily usage rollup, against a real TimescaleDB. Run as
// postgres_integration_test.go describes.
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

var usageSeq int

// usageEvent writes one base event row for a tenant, received at the time it is dated. The same
// device token is deliberately reused across tenants by the callers: a distinct-device count that
// keyed on the token alone would merge two tenants' devices.
func usageEvent(t *testing.T, db *gorm.DB, tenant, device string, at time.Time) {
	t.Helper()
	usageSeq++
	require.NoError(t, db.Exec(`INSERT INTO "event-management".events
		(tenant_id, event_id, device_token, event_type, occurred_time, source, processed_time)
		VALUES (?, ?, ?, 2, ?, 'it', ?)`, tenant, []byte(fmt.Sprintf("%s-%s-%d", tenant, device, usageSeq)), device, at, at).Error)
}

// TestIntegrationUsageRollupIsExactAndIsolatedPerTenant is the two-tenant test the rollup exists to
// pass, and its negative control: a tenant with no traffic reads zero, a bystander's events never
// land on another tenant's day, and the day's edges belong to the right day.
func TestIntegrationUsageRollupIsExactAndIsolatedPerTenant(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "usage"))
	api := NewApi(mgr)
	db := systemDB(mgr)
	ctx := context.Background()

	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	at := func(h, m, s int) time.Time { return time.Date(2026, 10, 10, h, m, s, 0, time.UTC) }

	// acme: hour 10 has dev-1 twice and dev-2 once; hour 11 has dev-1 once. Plus the two edges:
	// the last second of the day (counts) and midnight of the next day (does not).
	usageEvent(t, db, "acme", "dev-1", at(10, 0, 0))
	usageEvent(t, db, "acme", "dev-1", at(10, 30, 0))
	usageEvent(t, db, "acme", "dev-2", at(10, 59, 59))
	usageEvent(t, db, "acme", "dev-1", at(11, 0, 0))
	usageEvent(t, db, "acme", "dev-1", at(23, 59, 59))
	usageEvent(t, db, "acme", "dev-1", day.AddDate(0, 0, 1))
	// globex: the SAME device token as acme's, five events in one hour.
	for i := 0; i < 5; i++ {
		usageEvent(t, db, "globex", "dev-1", at(10, i, 0))
	}
	// idle sends nothing at all.

	now := day.AddDate(0, 0, 1).Add(2 * time.Hour)
	rows, err := api.RollupUsageHours(ctx, day, day.Add(23*time.Hour), now)
	require.NoError(t, err)
	assert.Equal(t, int64(4), rows, "acme's hours 10, 11 and 23 and globex's hour 10: only hours with traffic get a row")

	read := func(tenant string) TenantEventUsageDay {
		days, err := api.TenantEventUsage(ctx, tenant, day, day)
		require.NoError(t, err)
		require.Len(t, days, 1)
		return days[0]
	}

	acme := read("acme")
	assert.Equal(t, int64(5), acme.EventsIngested, "5 events received that day; the midnight one belongs to the next")
	assert.Equal(t, 2, acme.DevicesActive)
	assert.Equal(t, 2, acme.DevicesPeak, "hour 10 had two distinct devices")
	// hour 10: 2 devices, hour 11: 1, hour 23: 1.
	assert.Equal(t, int64(4), acme.DeviceHours)
	assert.True(t, acme.Complete)

	globex := read("globex")
	assert.Equal(t, int64(5), globex.EventsIngested)
	assert.Equal(t, 1, globex.DevicesActive, "dev-1 is one device for globex whatever acme calls its own")
	assert.Equal(t, 1, globex.DevicesPeak)
	assert.Equal(t, int64(1), globex.DeviceHours)

	// The negative control: a tenant with no traffic reads zero, and so does an unknown tenant.
	for _, quiet := range []string{"idle", "no-such-tenant"} {
		u := read(quiet)
		assert.Equal(t, TenantEventUsageDay{TenantId: quiet, Day: day, Complete: true}, u, "%s must read zero", quiet)
	}

	// The next day got only acme's midnight event: it was not rolled up yet, so it reads zero.
	next, err := api.TenantEventUsage(ctx, "acme", day.AddDate(0, 0, 1), day.AddDate(0, 0, 1))
	require.NoError(t, err)
	assert.Equal(t, int64(0), next[0].EventsIngested)
	assert.False(t, next[0].Complete)

	// A complete day is final: rolling it up again, even with a new row stored for it, moves
	// nothing, for either tenant.
	usageEvent(t, db, "acme", "dev-3", at(12, 0, 0))
	_, err = api.RollupUsageHours(ctx, day, day.Add(23*time.Hour), now.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, acme, read("acme"))
	assert.Equal(t, globex, read("globex"))

	// The whole range comes back zero-filled, oldest first.
	span, err := api.TenantEventUsage(ctx, "idle", day.AddDate(0, 0, -2), day)
	require.NoError(t, err)
	require.Len(t, span, 3)
	for _, d := range span {
		assert.Zero(t, d.EventsIngested+int64(d.DevicesActive)+int64(d.DevicesPeak)+d.DeviceHours)
	}
	assert.True(t, span[0].Day.Before(span[2].Day))

	// Pruning the working tables of a day long final leaves its record exactly as it was.
	require.NoError(t, api.PruneUsageWorkingTables(ctx, day.AddDate(0, 0, 3)))
	var working int64
	require.NoError(t, db.Raw(`SELECT (SELECT count(*) FROM "event-management".tenant_usage_hourly WHERE hour < ?) +
		(SELECT count(*) FROM "event-management".tenant_usage_devices WHERE day <= ?::date)`,
		day.AddDate(0, 0, 1), day.Format("2006-01-02")).Scan(&working).Error)
	assert.Zero(t, working, "the day's working rows are pruned")
	assert.Equal(t, acme, read("acme"), "the record survives the prune")
}

// The sweep's pass, end to end: it starts at the beginning of the current UTC day on its first
// run, finalizes only settled hours, at most maxHours per pass, resumes from the PERSISTED
// watermark (so a second replica, or a restart, does not redo an hour), and counts a device once
// per day across hours finalized by different passes.
func TestIntegrationUsagePassResumesFromThePersistedWatermark(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "usagepass"))
	db := systemDB(mgr)
	ctx := context.Background()

	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	h := func(n int) time.Time { return day.Add(time.Duration(n) * time.Hour) }
	usageEvent(t, db, "acme", "dev-1", h(1).Add(time.Minute))
	usageEvent(t, db, "acme", "dev-2", h(7).Add(time.Minute))
	usageEvent(t, db, "acme", "dev-1", h(7).Add(2*time.Minute))
	// Received the day before the rollup first ran: never scanned, so never counted.
	usageEvent(t, db, "acme", "dev-9", day.Add(-time.Hour))

	pass := func(now time.Time) int {
		t.Helper()
		// A fresh Api each time: nothing carries over in memory, as between replicas.
		n, err := NewApi(mgr).RollupUsagePass(ctx, db, now, 6)
		require.NoError(t, err)
		return n
	}

	now := h(10).Add(15 * time.Minute)
	assert.Equal(t, 6, pass(now), "first run: from midnight, capped at six hours")
	assert.Equal(t, 4, pass(now), "hours 6-9; hour 9 settled at 10:10")
	assert.Equal(t, 0, pass(now), "nothing owed: the watermark is in the database")
	assert.Equal(t, 0, pass(h(11).Add(UsageSettle-time.Minute)), "hour 10 is not settled yet")

	days, err := NewApi(mgr).TenantEventUsage(ctx, "acme", day.AddDate(0, 0, -1), day)
	require.NoError(t, err)
	assert.Equal(t, int64(0), days[0].EventsIngested, "the day before the first run is never scanned")
	assert.False(t, days[0].Complete, "and never final")
	got := days[1]
	assert.Equal(t, int64(3), got.EventsIngested)
	assert.Equal(t, 2, got.DevicesActive, "dev-1 in hours 1 and 7 is one device")
	assert.Equal(t, 2, got.DevicesPeak)
	assert.Equal(t, int64(3), got.DeviceHours, "hour 1: one device, hour 7: two")
	assert.False(t, got.Complete, "ten of 24 hours final")

	// Catching up after midnight finalizes the rest, a capped pass at a time.
	late := day.AddDate(0, 0, 1).Add(UsageSettle + time.Minute)
	total := 0
	for i := 0; i < 5; i++ {
		total += pass(late)
	}
	assert.Equal(t, 14, total)
	days, err = NewApi(mgr).TenantEventUsage(ctx, "acme", day, day)
	require.NoError(t, err)
	assert.True(t, days[0].Complete)
	assert.Equal(t, int64(3), days[0].EventsIngested)
}

// The scan floor leaves out events DATED inside chunks that were compressed before the ingest-time
// index existed (see usageFloorClause), and only those.
func TestIntegrationUsageScanFloorBoundsTheRead(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "usagefloor"))
	api := NewApi(mgr)
	db := systemDB(mgr)
	ctx := context.Background()

	h := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	floor := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ingestEvent(t, db, "acme", "dev-1", floor.Add(-time.Hour), h.Add(time.Minute))
	ingestEvent(t, db, "acme", "dev-2", floor.Add(time.Hour), h.Add(time.Minute))
	require.NoError(t, db.Exec(`INSERT INTO "event-management".tenant_usage_state (name, next_hour, scan_floor, updated_at)
		VALUES ('hourly', ?, ?, ?)`, h, floor, h).Error)

	_, err := api.RollupUsageHours(ctx, h, h, h.Add(time.Hour+UsageSettle))
	require.NoError(t, err)
	n, ok := hourlyEvents(t, db, "acme", h)
	require.True(t, ok)
	assert.Equal(t, int64(1), n, "the event dated below the floor is left out; the one above is counted")
}
