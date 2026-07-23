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
	ds, err := api.MergeDeviceState(ctx, "device-100", base, nil, DeviceIdentity{})
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
	ds, err = api.MergeDeviceState(ctx, "device-100", base.Add(1*time.Minute), nil, DeviceIdentity{})
	if err != nil {
		t.Fatalf("second merge failed: %v", err)
	}
	if !ds.LastActivityTime.Time.Equal(base.Add(1 * time.Minute)) {
		t.Fatalf("last activity not advanced: %v", ds.LastActivityTime.Time)
	}

	// An older event must NOT roll last-activity time backward.
	ds, err = api.MergeDeviceState(ctx, "device-100", base.Add(-1*time.Hour), nil, DeviceIdentity{})
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

	states, err := api.DeviceStatesByDeviceToken(ctx, []string{"device-100"})
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
	ds, err = api.MergeDeviceState(ctx, "device-100", now.Add(1*time.Minute), nil, DeviceIdentity{})
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

// TestPresenceNonEventSplit is the S3 correctness slice at the DB write path: the guard
// (presence.Decide, table-tested in core/presence) SPLITS "advance the ordering marker"
// from "the connectivity edge flipped". A day-late higher-session DISCONNECT over an
// already-dead device advances SessionId but must FREEZE LastDisconnectTime and not
// re-fire; a higher-session CONNECT over an already-connected device is a genuine
// reconnect and MUST refresh LastConnectTime. (The ordering table itself lives in
// github.com/devicechain-io/dc-microservice/presence.)
func TestPresenceNonEventSplit(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")
	t0 := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	conn := func(s uint64, tm time.Time) *PresenceTransition {
		return &PresenceTransition{Connected: true, SessionId: s, OccurredAt: tm}
	}
	disc := func(s uint64, tm time.Time) *PresenceTransition {
		return &PresenceTransition{Connected: false, SessionId: s, OccurredAt: tm}
	}

	t.Run("higher-session disconnect over dead device freezes LastDisconnectTime", func(t *testing.T) {
		api := newTestApi(t)
		// Establish a device that connected then disconnected at session 100.
		if _, err := api.MergeDeviceState(ctx, "d1", t0, conn(100, t0), DeviceIdentity{}); err != nil {
			t.Fatalf("connect: %v", err)
		}
		deathAt := t0.Add(time.Minute)
		ds, err := api.MergeDeviceState(ctx, "d1", deathAt, disc(100, deathAt), DeviceIdentity{})
		if err != nil {
			t.Fatalf("disconnect: %v", err)
		}
		if ds.Active || !ds.LastDisconnectTime.Time.Equal(deathAt) {
			t.Fatalf("first disconnect not recorded: %+v", ds)
		}
		// A day-late shadow-expiry DISCONNECT at a HIGHER session (L3b reconstruction backstop).
		lateAt := t0.Add(24 * time.Hour)
		ds, err = api.MergeDeviceState(ctx, "d1", lateAt, disc(200, lateAt), DeviceIdentity{})
		if err != nil {
			t.Fatalf("late disconnect: %v", err)
		}
		if ds.SessionId != 200 {
			t.Fatalf("ordering marker did not advance to the newer session: %+v", ds)
		}
		if !ds.LastDisconnectTime.Time.Equal(deathAt) {
			t.Fatalf("late higher-session disconnect MOVED LastDisconnectTime (%v) off the first-known death %v — the S3 non-event bug", ds.LastDisconnectTime.Time, deathAt)
		}
	})

	t.Run("first authoritative DISCONNECT over an inferred-swept-dead device records the authoritative time", func(t *testing.T) {
		api := newTestApi(t)
		// An INFERRED device: a plain data event creates it active.
		if _, err := api.MergeDeviceState(ctx, "d3", t0, nil, DeviceIdentity{}); err != nil {
			t.Fatalf("data event: %v", err)
		}
		// The data-silence sweep flips it inactive far later, writing a SYNTHETIC disconnect time.
		sweepAt := t0.Add(24 * time.Hour)
		if _, err := api.SweepInactive(core.WithSystemContext(ctx), sweepAt); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		// Its authoritative LWT (the FIRST StateChange) then arrives, dated EARLIER than the
		// sweep's guess — the device actually died at deathAt, the sweep only noticed at sweepAt.
		deathAt := t0.Add(time.Hour)
		ds, err := api.MergeDeviceState(ctx, "d3", deathAt, disc(5, deathAt), DeviceIdentity{})
		if err != nil {
			t.Fatalf("first authoritative disconnect: %v", err)
		}
		if ds.PresenceSource != PresenceSourceAsserted || ds.Active {
			t.Fatalf("promotion did not assert/deactivate: %+v", ds)
		}
		if !ds.LastDisconnectTime.Time.Equal(deathAt) {
			t.Fatalf("first authoritative word kept the SYNTHETIC swept time (%v) instead of the true death %v", ds.LastDisconnectTime.Time, deathAt)
		}
	})

	t.Run("higher-session connect over live device refreshes LastConnectTime", func(t *testing.T) {
		api := newTestApi(t)
		if _, err := api.MergeDeviceState(ctx, "d2", t0, conn(100, t0), DeviceIdentity{}); err != nil {
			t.Fatalf("connect: %v", err)
		}
		// A reconnect at a higher session with no intervening disconnect (missed DEATH): Active was
		// already true, but the new epoch is a genuine new physical connection.
		reAt := t0.Add(time.Hour)
		ds, err := api.MergeDeviceState(ctx, "d2", reAt, conn(200, reAt), DeviceIdentity{})
		if err != nil {
			t.Fatalf("reconnect: %v", err)
		}
		if !ds.Active || ds.SessionId != 200 {
			t.Fatalf("reconnect did not advance: %+v", ds)
		}
		if !ds.LastConnectTime.Time.Equal(reAt) {
			t.Fatalf("higher-session reconnect did NOT refresh LastConnectTime (%v, want %v) — a stale connect time claims continuous uptime across the outage", ds.LastConnectTime.Time, reAt)
		}
	})
}

// TestAssertedPresenceProjection exercises the authoritative-presence write path end
// to end on the DB: assert-on-first-sight, the resurrection guard (data can't revive
// a known-dead asserted device), and the stale-will guard (an old session's
// disconnect can't un-set a fresh connect — the SP4 leader-kill invariant, M17).
func TestAssertedPresenceProjection(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	t0 := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	conn := func(s uint64, tm time.Time) *PresenceTransition {
		return &PresenceTransition{Connected: true, SessionId: s, OccurredAt: tm}
	}
	disc := func(s uint64, tm time.Time) *PresenceTransition {
		return &PresenceTransition{Connected: false, SessionId: s, OccurredAt: tm}
	}

	// A CONNECTED StateChange creates an ASSERTED, active device.
	ds, err := api.MergeDeviceState(ctx, "sp-node", t0, conn(100, t0), DeviceIdentity{})
	if err != nil {
		t.Fatalf("connect merge: %v", err)
	}
	if ds.PresenceSource != PresenceSourceAsserted || !ds.Active || ds.SessionId != 100 {
		t.Fatalf("connect did not assert/activate: %+v", ds)
	}

	// A DISCONNECTED (same session, later) marks it inactive.
	ds, err = api.MergeDeviceState(ctx, "sp-node", t0.Add(2*time.Minute), disc(100, t0.Add(2*time.Minute)), DeviceIdentity{})
	if err != nil {
		t.Fatalf("disconnect merge: %v", err)
	}
	if ds.Active || !ds.LastDisconnectTime.Valid {
		t.Fatalf("disconnect did not deactivate: %+v", ds)
	}

	// RESURRECTION GUARD: a data event on an ASSERTED-dead device advances activity but
	// must NOT flip Active back on.
	ds, err = api.MergeDeviceState(ctx, "sp-node", t0.Add(3*time.Minute), nil, DeviceIdentity{})
	if err != nil {
		t.Fatalf("post-death data merge: %v", err)
	}
	if ds.Active {
		t.Fatalf("data event resurrected a known-dead asserted device: %+v", ds)
	}
	if !ds.LastActivityTime.Time.Equal(t0.Add(3 * time.Minute)) {
		t.Fatalf("data event did not advance activity on a dead device: %+v", ds)
	}

	// A CONNECTED with a NEWER session reactivates.
	ds, err = api.MergeDeviceState(ctx, "sp-node", t0.Add(4*time.Minute), conn(200, t0.Add(4*time.Minute)), DeviceIdentity{})
	if err != nil {
		t.Fatalf("reconnect merge: %v", err)
	}
	if !ds.Active || ds.SessionId != 200 {
		t.Fatalf("newer-session connect did not reactivate: %+v", ds)
	}

	// STALE-WILL GUARD (M17): a stale DISCONNECTED from the OLD session must NOT un-set
	// the freshly-connected device, even though it arrives later in wall-clock time.
	ds, err = api.MergeDeviceState(ctx, "sp-node", t0.Add(5*time.Minute), disc(100, t0.Add(5*time.Minute)), DeviceIdentity{})
	if err != nil {
		t.Fatalf("stale disconnect merge: %v", err)
	}
	if !ds.Active {
		t.Fatalf("a stale-session disconnect wrongly deactivated a fresh connect: %+v", ds)
	}
	if ds.SessionId != 200 {
		t.Fatalf("stale disconnect clobbered the session marker: %+v", ds)
	}
}

// TestFirstEventDisconnectRecordsDead covers Fable Major 5: the very first event for a
// device being a DISCONNECTED StateChange must create a DEAD row, not an active one.
func TestFirstEventDisconnectRecordsDead(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	t0 := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	ds, err := api.MergeDeviceState(ctx, "sp-dead", t0, &PresenceTransition{SessionId: 1, OccurredAt: t0}, DeviceIdentity{})
	if err != nil {
		t.Fatalf("first-disconnect merge: %v", err)
	}
	if ds.Active {
		t.Fatalf("first-ever DISCONNECT created an active device: %+v", ds)
	}
	if ds.PresenceSource != PresenceSourceAsserted || !ds.LastDisconnectTime.Valid {
		t.Fatalf("first-ever DISCONNECT did not record a dead asserted device: %+v", ds)
	}
}

// TestSweepSkipsAsserted: the inactivity sweep flips a silent INFERRED device but
// never an ASSERTED one (its offline is a DEATH/LWT, not a data-silence timeout).
func TestSweepSkipsAsserted(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	t0 := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

	if _, err := api.MergeDeviceState(ctx, "inferred-1", t0, nil, DeviceIdentity{}); err != nil {
		t.Fatalf("inferred merge: %v", err)
	}
	if _, err := api.MergeDeviceState(ctx, "asserted-1", t0, &PresenceTransition{Connected: true, SessionId: 1, OccurredAt: t0}, DeviceIdentity{}); err != nil {
		t.Fatalf("asserted merge: %v", err)
	}

	flipped, err := api.SweepInactive(core.WithSystemContext(ctx), t0.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if flipped != 1 {
		t.Fatalf("expected exactly the INFERRED device flipped, got %d", flipped)
	}
	states, _ := api.DeviceStatesByDeviceToken(ctx, []string{"inferred-1", "asserted-1"})
	for _, s := range states {
		switch s.DeviceToken {
		case "inferred-1":
			if s.Active {
				t.Fatalf("inferred device should be swept inactive: %+v", s)
			}
		case "asserted-1":
			if !s.Active {
				t.Fatalf("asserted device must NOT be swept: %+v", s)
			}
		}
	}
}

// TestMergeLatestMeasurementsBinding covers the denormalized unit/dataType on the
// latest-value projection (ADR-016): a bound reading persists them, an unbound one
// leaves them null, and a strictly-newer reading overwrites them.
func TestMergeLatestMeasurementsBinding(t *testing.T) {
	api := newTestApi(t)
	if err := api.RDB.Database.AutoMigrate(&LatestMeasurement{}); err != nil {
		t.Fatalf("migrate latest_measurements: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	t0 := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	cel, double := "Cel", "DOUBLE"

	num := func(f float64) sql.NullFloat64 { return sql.NullFloat64{Float64: f, Valid: true} }
	if err := api.MergeLatestMeasurements(ctx, "device-1", []LatestMeasurementInput{
		{Name: "temp", Value: num(21.5), Unit: &cel, DataType: &double, OccurredTime: t0},
		{Name: "humidity", Value: num(55), OccurredTime: t0},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}

	byName := func(name string) LatestMeasurement {
		var m LatestMeasurement
		if err := api.RDB.DB(ctx).Where("name = ?", name).First(&m).Error; err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return m
	}
	temp := byName("temp")
	if temp.Unit == nil || *temp.Unit != "Cel" || temp.DataType == nil || *temp.DataType != "DOUBLE" {
		t.Fatalf("bound temp did not persist unit/dataType: %+v", temp)
	}
	if hum := byName("humidity"); hum.Unit != nil || hum.DataType != nil {
		t.Fatalf("unbound humidity should carry no unit/dataType: %+v", hum)
	}

	// A strictly-newer reading overwrites the denormalized fields (a republish could
	// change the unit); an older one is ignored.
	kelvin, updated := "K", double
	if err := api.MergeLatestMeasurements(ctx, "device-1", []LatestMeasurementInput{
		{Name: "temp", Value: num(295), Unit: &kelvin, DataType: &updated, OccurredTime: t0.Add(time.Minute)},
	}); err != nil {
		t.Fatalf("newer merge: %v", err)
	}
	if temp = byName("temp"); temp.Unit == nil || *temp.Unit != "K" || temp.Value.Float64 != 295 {
		t.Fatalf("newer reading did not overwrite value+unit: %+v", temp)
	}

	// An older reading is ignored — it must not roll the unit back.
	old := cel
	if err := api.MergeLatestMeasurements(ctx, "device-1", []LatestMeasurementInput{
		{Name: "temp", Value: num(10), Unit: &old, DataType: &double, OccurredTime: t0.Add(-time.Minute)},
	}); err != nil {
		t.Fatalf("older merge: %v", err)
	}
	if temp = byName("temp"); *temp.Unit != "K" || temp.Value.Float64 != 295 {
		t.Fatalf("older reading wrongly overwrote the newer value/unit: %+v", temp)
	}
}
