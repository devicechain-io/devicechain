// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// seedRestoreRows creates, for tenant "bt", one row of the shape a v1.1 sweep leaves (inactive,
// INFERRED, last_disconnect_time stamped, no alarm time) for each token.
func seedRestoreRows(t *testing.T, api *Api, tokens ...string) {
	t.Helper()
	disconnected := sql.NullTime{Time: sweepT0.Add(2 * time.Hour), Valid: true}
	for _, tok := range tokens {
		row := DeviceState{DeviceToken: tok, PresenceSource: PresenceSourceInferred, LastDisconnectTime: disconnected}
		row.TenantId = "bt"
		if err := api.RDB.DB(core.WithTenant(context.Background(), "bt")).Create(&row).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func alarmTimeOf(t *testing.T, api *Api, token string) sql.NullTime {
	t.Helper()
	var ds DeviceState
	if err := api.RDB.DB(core.WithTenant(context.Background(), "bt")).Where("device_token = ?", token).First(&ds).Error; err != nil {
		t.Fatal(err)
	}
	return ds.InactivityAlarmTime
}

// holdRowLock locks one row FOR UPDATE on a connection of its own, as a v1.1 batch writer does, and
// returns the function that releases it.
func holdRowLock(t *testing.T, api *Api, token string) (release func()) {
	t.Helper()
	tx := api.RDB.Database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if err := tx.Exec(`SELECT id FROM "device-state".device_states WHERE device_token = ? FOR UPDATE`, token).Error; err != nil {
		tx.Rollback()
		t.Fatalf("lock the row: %v", err)
	}
	released := false
	release = func() {
		if !released {
			released = true
			tx.Rollback()
		}
	}
	t.Cleanup(release)
	return release
}

// The first restore skips a row a writer holds and is recorded as applied; the final pass does not
// finish the row while a writer still holds it (and does not fail the start for it), and finishes
// the row once the writer lets go. Negative
// controls: the first migration really does leave the held row (so the test shows the defect it
// fixes), and the final pass really leaves the row while it is held, and returns rather than failing the start.
func TestTheFinalPassRestoresARowAWriterHeldDuringTheFirstOne(t *testing.T) {
	api := newPostgresProjectionApi(t, "alarmfinal")
	seedRestoreRows(t, api, "held", "free")
	release := holdRowLock(t, api, "held")

	var db *gorm.DB = api.RDB.Database
	if err := NewInactivityAlarmTimeRestoreSchema().Migrate(db); err != nil {
		t.Fatalf("first restore: %v", err)
	}
	if !alarmTimeOf(t, api, "free").Valid {
		t.Fatal("the control: the first restore did not fill the free row")
	}
	if alarmTimeOf(t, api, "held").Valid {
		t.Fatal("the control: the first restore filled a row a writer holds, so the blind spot this test covers did not occur")
	}

	// Still held: the final pass leaves the row and returns.
	if err := newInactivityAlarmTimeRestoreFinalPass(2, 50*time.Millisecond).Migrate(db); err != nil {
		t.Fatalf("final pass with the row held: %v", err)
	}
	if alarmTimeOf(t, api, "held").Valid {
		t.Fatal("the held row was written while its lock was held")
	}

	// The writer lets go partway through the passes: the pass that follows finishes the row.
	go func() {
		time.Sleep(300 * time.Millisecond)
		release()
	}()
	if err := newInactivityAlarmTimeRestoreFinalPass(20, 100*time.Millisecond).Migrate(db); err != nil {
		t.Fatalf("final pass after the writer let go: %v", err)
	}
	got := alarmTimeOf(t, api, "held")
	want := sweepT0.Add(2 * time.Hour)
	if !got.Valid || !got.Time.Equal(want) {
		t.Fatalf("the held row's alarm time is %v, want %v", got, want)
	}

	// Re-runnable: a second run finds nothing.
	if err := newInactivityAlarmTimeRestoreFinalPass(1, 0).Migrate(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// The final pass reaches zero for every shape the first restore handles, including the one that
// could not settle under the first restore's own predicates: an inactive row whose last disconnect
// predates its last connect gets no value (the second statement would clear it again).
func TestTheFinalPassSettlesEveryShape(t *testing.T) {
	api := newPostgresProjectionApi(t, "alarmfinalshapes")
	at := func(h int) sql.NullTime {
		return sql.NullTime{Time: sweepT0.Add(time.Duration(h) * time.Hour), Valid: true}
	}
	rows := []DeviceState{
		{DeviceToken: "stale-active", Active: true, PresenceSource: PresenceSourceInferred, LastDisconnectTime: at(1), InactivityAlarmTime: at(1)},
		{DeviceToken: "swept-by-v11", PresenceSource: PresenceSourceInferred, LastDisconnectTime: at(2)},
		{DeviceToken: "older-episode", PresenceSource: PresenceSourceInferred, LastConnectTime: at(2), LastDisconnectTime: at(3), InactivityAlarmTime: at(1)},
		{DeviceToken: "disconnect-before-connect", PresenceSource: PresenceSourceInferred, LastConnectTime: at(5), LastDisconnectTime: at(4)},
		{DeviceToken: "asserted", PresenceSource: PresenceSourceAsserted, LastDisconnectTime: at(3)},
		{DeviceToken: "already-set", PresenceSource: PresenceSourceInferred, LastDisconnectTime: at(5), InactivityAlarmTime: at(4)},
	}
	for i := range rows {
		rows[i].TenantId = "bt"
		if err := api.RDB.DB(core.WithTenant(context.Background(), "bt")).Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	m := newInactivityAlarmTimeRestoreFinalPass(1, 0)
	for run := 0; run < 2; run++ {
		if err := m.Migrate(api.RDB.Database); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		for tok, w := range map[string]sql.NullTime{
			"stale-active": {}, "swept-by-v11": at(2), "older-episode": at(3), "disconnect-before-connect": {},
			"asserted": {}, "already-set": at(4),
		} {
			if g := alarmTimeOf(t, api, tok); g.Valid != w.Valid || (w.Valid && !g.Time.Equal(w.Time)) {
				t.Errorf("run %d, %s: alarm time %v, want %v", run, tok, g, w)
			}
		}
	}
}
