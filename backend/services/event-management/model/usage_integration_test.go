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

var usageSeq byte

// usageEvent writes one base event row for a tenant. The same device token is deliberately reused
// across tenants by the callers: a distinct-device count that keyed on the token alone would merge
// two tenants' devices.
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
	rows, err := api.RollupUsageDay(ctx, day, now)
	require.NoError(t, err)
	assert.Equal(t, int64(4), rows, "acme's hours 10, 11 and 23 and globex's hour 10: only hours with traffic get a row")

	read := func(tenant string) TenantUsageDay {
		days, err := api.TenantUsage(ctx, tenant, day, day)
		require.NoError(t, err)
		require.Len(t, days, 1)
		return days[0]
	}

	acme := read("acme")
	assert.Equal(t, int64(5), acme.EventsIngested, "5 events dated the day; the midnight one belongs to the next")
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
		assert.Equal(t, TenantUsageDay{TenantId: quiet, Day: day, Complete: true}, u, "%s must read zero", quiet)
	}

	// The next day got only acme's midnight event: it was not rolled up yet, so it reads zero.
	next, err := api.TenantUsage(ctx, "acme", day.AddDate(0, 0, 1), day.AddDate(0, 0, 1))
	require.NoError(t, err)
	assert.Equal(t, int64(0), next[0].EventsIngested)
	assert.False(t, next[0].Complete)

	// Rolling the same day up again changes nothing.
	_, err = api.RollupUsageDay(ctx, day, now)
	require.NoError(t, err)
	assert.Equal(t, acme.EventsIngested, read("acme").EventsIngested)
	assert.Equal(t, globex.EventsIngested, read("globex").EventsIngested)

	// A late event for the day is picked up by the next pass and only moves its own tenant.
	usageEvent(t, db, "acme", "dev-3", at(12, 0, 0))
	_, err = api.RollupUsageDay(ctx, day, now.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int64(6), read("acme").EventsIngested)
	assert.Equal(t, 3, read("acme").DevicesActive)
	assert.Equal(t, int64(5), read("globex").EventsIngested, "a bystander's figures do not move")

	// The whole range comes back zero-filled, oldest first.
	span, err := api.TenantUsage(ctx, "idle", day.AddDate(0, 0, -2), day)
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
	assert.Equal(t, int64(6), read("acme").EventsIngested, "the record survives the prune")
	assert.Equal(t, 3, read("acme").DevicesActive)
}

// A day built up by passes that each see only some of its hours still counts a device once for the
// day, however many hours it was active in, and counts each device that was active only in an
// earlier pass's hours: the day's distinct devices come from its device set, not the latest hour.
func TestIntegrationUsageRollupAccumulatesDevicesAcrossHourlyPasses(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "usagehours"))
	api := NewApi(mgr)
	db := systemDB(mgr)
	ctx := context.Background()

	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	h := func(n int) time.Time { return day.Add(time.Duration(n) * time.Hour) }
	usageEvent(t, db, "acme", "dev-1", h(1).Add(time.Minute))
	usageEvent(t, db, "acme", "dev-2", h(2).Add(time.Minute))
	usageEvent(t, db, "acme", "dev-1", h(2).Add(2*time.Minute))

	_, err := api.RollupUsageHours(ctx, h(1), h(1), h(1).Add(30*time.Minute))
	require.NoError(t, err)
	_, err = api.RollupUsageHours(ctx, h(2), h(2), h(2).Add(30*time.Minute))
	require.NoError(t, err)

	days, err := api.TenantUsage(ctx, "acme", day, day)
	require.NoError(t, err)
	require.Len(t, days, 1)
	got := days[0]
	assert.Equal(t, int64(3), got.EventsIngested)
	assert.Equal(t, 2, got.DevicesActive, "dev-1 and dev-2, though the last pass saw dev-1 too")
	assert.Equal(t, 2, got.DevicesPeak, "hour 2 had both")
	assert.Equal(t, int64(3), got.DeviceHours, "hour 1: one device, hour 2: two")
	assert.False(t, got.Complete, "rolled up mid-day")

	// Hours computed while still open are not marked: the day is incomplete even once they close.
	_, err = api.RollupUsageHours(ctx, h(23), h(24), h(24).Add(5*time.Minute))
	require.NoError(t, err)
	days, err = api.TenantUsage(ctx, "acme", day, day)
	require.NoError(t, err)
	assert.False(t, days[0].Complete, "hours 1 and 2 were computed while open, hours 0 and 3-22 never")
	owed, ok, err := api.FirstUsageHourOwed(ctx, day, h(25))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, h(0), owed, "the ledger owes the day's first hour")

	// Measuring the owed hours closes the day, with the same figures.
	_, err = api.RollupUsageHours(ctx, h(0), h(22), h(24).Add(10*time.Minute))
	require.NoError(t, err)
	days, err = api.TenantUsage(ctx, "acme", day, day)
	require.NoError(t, err)
	assert.True(t, days[0].Complete)
	assert.Equal(t, int64(3), days[0].EventsIngested)
	assert.Equal(t, 2, days[0].DevicesActive)
	owed, ok, err = api.FirstUsageHourOwed(ctx, day, h(25))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, h(24), owed, "the next day's open hour is the first owed")
}
