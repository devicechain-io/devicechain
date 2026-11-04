// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/config"
	"github.com/devicechain-io/dc-microservice/core"
)

// The Postgres form of the inactivity predicate, at its boundary. The unit tests run the
// SQLite twin; this is the test that executes the string Postgres runs. A device is swept
// strictly after the window: not one second short of it, not at EXACTLY the window, and yes
// one second past it. Each case has a database of its own, because a sweep judges every
// active row in the database.
func TestThePostgresSweepHonoursTheInactivityBoundary(t *testing.T) {
	window := time.Duration(config.InactivityTimeout) * time.Second
	sys := core.WithSystemContext(context.Background())

	for _, tc := range []struct {
		name  string
		name2 string
		age   time.Duration
		swept bool
	}{
		{"one second short of the window", "short", window - time.Second, false},
		{"exactly the window", "exact", window, false},
		{"one second past the window", "past", window + time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newPostgresProjectionApi(t, tc.name2)
			seedStale(t, api, "bound", "d")
			now := sweepT0.Add(tc.age)

			// Negative control: judged against a deliberately wrong window (590 s) the
			// same row IS stale, so the verdicts below could have come out the other way.
			sqldb, err := api.RDB.Database.DB()
			if err != nil {
				t.Fatalf("pool: %v", err)
			}
			var wrong int
			if err := sqldb.QueryRow(`SELECT count(*) FROM "device-state".device_states `+
				`WHERE last_activity_time < $1::timestamptz - 590 * INTERVAL '1 second'`, now).Scan(&wrong); err != nil {
				t.Fatalf("control query: %v", err)
			}
			if wrong != 1 {
				t.Fatalf("negative control matched %d rows, want 1", wrong)
			}

			n, err := api.SweepInactive(sys, now)
			if err != nil {
				t.Fatalf("sweep: %v", err)
			}
			if got := !activeOf(t, api, "bound", "d"); got != tc.swept || (n == 1) != tc.swept {
				t.Fatalf("age %v: swept=%v (rows %d), want %v", tc.age, got, n, tc.swept)
			}
		})
	}
}
