// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestIsInactive exercises the pure inactivity predicate (no DB).
func TestIsInactive(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	valid := func(d time.Duration) sql.NullTime {
		return sql.NullTime{Time: now.Add(d), Valid: true}
	}

	cases := []struct {
		name    string
		last    sql.NullTime
		timeout int
		want    bool
	}{
		{"invalid last is never inactive", sql.NullTime{}, 600, false},
		{"recent activity within timeout", valid(-5 * time.Minute), 600, false},
		{"old activity beyond timeout", valid(-20 * time.Minute), 600, true},
		{"exactly at boundary is not inactive", valid(-600 * time.Second), 600, false},
		{"one second past boundary is inactive", valid(-601 * time.Second), 600, true},
		{"zero timeout falls back to default (within)", valid(-5 * time.Minute), 0, false},
		{"zero timeout falls back to default (beyond)", valid(-20 * time.Minute), 0, true},
		{"negative timeout falls back to default (beyond)", valid(-20 * time.Minute), -5, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isInactive(tc.last, tc.timeout, now)
			if got != tc.want {
				t.Fatalf("isInactive(%v, %d) = %v, want %v", tc.last, tc.timeout, got, tc.want)
			}
		})
	}
}

// newTestApi spins up an in-memory sqlite database with the tenant-scope
// callbacks registered and the DeviceState table migrated, then wraps it in an
// Api so the write path can be exercised exactly as production does.
func newTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("failed to register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&DeviceState{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// TestMergeAndSweep exercises the create -> reconnect -> sweep transitions.
func TestMergeAndSweep(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	base := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)

	// First event creates an active row.
	ds, err := api.MergeDeviceState(ctx, 100, base)
	if err != nil {
		t.Fatalf("initial merge failed: %v", err)
	}
	if !ds.Active || !ds.LastActivityTime.Valid || !ds.LastConnectTime.Valid {
		t.Fatalf("new state not active/connected: %+v", ds)
	}
	if ds.InactivityTimeout != config.DefaultInactivityTimeout {
		t.Fatalf("expected default timeout %d, got %d", config.DefaultInactivityTimeout, ds.InactivityTimeout)
	}

	// A later event advances last-activity time but does not re-connect.
	ds, err = api.MergeDeviceState(ctx, 100, base.Add(1*time.Minute))
	if err != nil {
		t.Fatalf("second merge failed: %v", err)
	}
	if !ds.LastActivityTime.Time.Equal(base.Add(1 * time.Minute)) {
		t.Fatalf("last activity not advanced: %v", ds.LastActivityTime.Time)
	}

	// An older event must NOT roll last-activity time backward.
	ds, err = api.MergeDeviceState(ctx, 100, base.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("stale merge failed: %v", err)
	}
	if !ds.LastActivityTime.Time.Equal(base.Add(1 * time.Minute)) {
		t.Fatalf("stale event rolled activity back: %v", ds.LastActivityTime.Time)
	}

	// Sweep well past the inactivity window flips the device to inactive.
	now := base.Add(2 * time.Hour)
	flipped, err := api.SweepInactive(core.WithSystemContext(ctx), now)
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if flipped != 1 {
		t.Fatalf("expected 1 device flipped inactive, got %d", flipped)
	}

	states, err := api.DeviceStatesByDeviceId(ctx, []uint{100})
	if err != nil || len(states) != 1 {
		t.Fatalf("lookup after sweep failed: %v (n=%d)", err, len(states))
	}
	if states[0].Active {
		t.Fatalf("device should be inactive after sweep")
	}
	if !states[0].InactivityAlarmTime.Valid || !states[0].LastDisconnectTime.Valid {
		t.Fatalf("inactive device missing alarm/disconnect times: %+v", states[0])
	}

	// A new event reconnects the device and clears the inactivity alarm.
	ds, err = api.MergeDeviceState(ctx, 100, now.Add(1*time.Minute))
	if err != nil {
		t.Fatalf("reconnect merge failed: %v", err)
	}
	if !ds.Active || ds.InactivityAlarmTime.Valid {
		t.Fatalf("reconnect did not reactivate/clear alarm: %+v", ds)
	}
	if !ds.LastConnectTime.Time.Equal(now.Add(1 * time.Minute)) {
		t.Fatalf("reconnect did not update connect time: %v", ds.LastConnectTime.Time)
	}

	// A second sweep right after reconnect should flip nothing.
	flipped, err = api.SweepInactive(core.WithSystemContext(ctx), now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("post-reconnect sweep failed: %v", err)
	}
	if flipped != 0 {
		t.Fatalf("expected 0 flips right after reconnect, got %d", flipped)
	}
}
