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
)

// The v1.1 repair, on real Postgres: an active row loses a stale alarm time, an inactive INFERRED
// row with a disconnect time and no alarm time gets it back, and an ASSERTED row, a row that
// already has one and a row never disconnected are left alone. Run twice: it is re-runnable.
func TestTheInactivityAlarmRestoreRepairsOnlyWhatV11Left(t *testing.T) {
	api := newPostgresProjectionApi(t, "alarmrestore")
	at := func(h int) sql.NullTime {
		return sql.NullTime{Time: sweepT0.Add(time.Duration(h) * time.Hour), Valid: true}
	}
	rows := []DeviceState{
		{DeviceToken: "stale-active", Active: true, PresenceSource: PresenceSourceInferred, LastDisconnectTime: at(1), InactivityAlarmTime: at(1)},
		{DeviceToken: "swept-by-v11", PresenceSource: PresenceSourceInferred, LastDisconnectTime: at(2)},
		{DeviceToken: "asserted", PresenceSource: PresenceSourceAsserted, LastDisconnectTime: at(3)},
		{DeviceToken: "already-set", PresenceSource: PresenceSourceInferred, LastDisconnectTime: at(5), InactivityAlarmTime: at(4)},
		{DeviceToken: "never-disconnected", PresenceSource: PresenceSourceInferred},
		// A v1.0 sweep at 1h, back at 2h under v1.1 (no clear), swept again at 3h (no stamp).
		// CONNECTED, DISCONNECTED, then demoted to INFERRED: no sweep ever fired, v1.0 answered null.
		{DeviceToken: "disconnect-then-demoted", PresenceSource: PresenceSourceInferred, LastDisconnectTime: at(6), PresenceTime: at(7)},
		// Demoted, then silent and swept: the sweep moved last_disconnect_time past presence_time.
		{DeviceToken: "demoted-then-swept", PresenceSource: PresenceSourceInferred, PresenceTime: at(8), LastDisconnectTime: at(9)},
		{DeviceToken: "older-episode", PresenceSource: PresenceSourceInferred, LastConnectTime: at(2), LastDisconnectTime: at(3), InactivityAlarmTime: at(1)},
		{DeviceToken: "older-episode-asserted", PresenceSource: PresenceSourceAsserted, LastConnectTime: at(2), LastDisconnectTime: at(3), InactivityAlarmTime: at(1)},
	}
	for i := range rows {
		rows[i].TenantId = "bt"
		if err := api.RDB.DB(core.WithTenant(context.Background(), "bt")).Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	m := NewInactivityAlarmTimeRestoreSchema()
	for run := 0; run < 2; run++ {
		if err := m.Migrate(api.RDB.Database); err != nil {
			t.Fatalf("migrate run %d: %v", run, err)
		}
		want := map[string]sql.NullTime{
			"stale-active": {}, "swept-by-v11": at(2), "asserted": {}, "already-set": at(4), "never-disconnected": {},
			"disconnect-then-demoted": {}, "demoted-then-swept": at(9), "older-episode": at(3), "older-episode-asserted": {},
		}
		for tok, w := range want {
			var ds DeviceState
			if err := api.RDB.DB(core.WithTenant(context.Background(), "bt")).Where("device_token = ?", tok).First(&ds).Error; err != nil {
				t.Fatal(err)
			}
			if ds.InactivityAlarmTime.Valid != w.Valid || (w.Valid && !ds.InactivityAlarmTime.Time.Equal(w.Time)) {
				t.Errorf("run %d, %s: alarm time %v, want %v", run, tok, ds.InactivityAlarmTime, w)
			}
		}
	}
}
