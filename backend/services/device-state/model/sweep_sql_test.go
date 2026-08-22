// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// sweepCase is one (age, timeout) pair fed through BOTH implementations of the
// inactivity rule.
type sweepCase struct {
	name    string
	age     time.Duration // how long ago this device was last heard from, at `now`
	timeout int           // the row's inactivity_timeout column, verbatim
}

// TestSweepAgreesWithIsInactive is the instrument that makes it safe for the rule to
// have two implementations. isInactive decides in Go; inactivitySQL decides in the
// database; SweepInactive ships the second one. Nothing else in the suite would fail
// if they diverged — the pre-existing sweep tests all run at +2h, far past every
// boundary, so they pass under a predicate that is wrong by a second.
//
// 🔴 THAT IS NOT HYPOTHETICAL. The obvious SQLite spelling of this predicate —
// (julianday(now) - julianday(last)) * 86400.0 > timeout — is correct at +2h and
// WRONG at exactly the timeout, because the float product lands a hair above the
// integer and flips a device one tick early. This table is what catches it.
func TestSweepAgreesWithIsInactive(t *testing.T) {
	cases := []sweepCase{
		{"one second past the timeout", 601 * time.Second, 600},
		{"exactly at the timeout is NOT inactive", 600 * time.Second, 600},
		{"one second short of the timeout", 599 * time.Second, 600},
		{"zero timeout falls back to the default, past it", 601 * time.Second, 0},
		{"zero timeout falls back to the default, short of it", 599 * time.Second, 0},
		{"negative timeout falls back to the default, past it", 601 * time.Second, -5},
		{"negative timeout falls back to the default, short of it", 599 * time.Second, -5},
		{"custom short timeout, past it", 61 * time.Second, 60},
		{"custom short timeout, exactly at it", 60 * time.Second, 60},
		{"custom long timeout, well short of it", 601 * time.Second, 3600},
		{"long silence under a long timeout", 4 * time.Hour, 3600},
	}

	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)

	// Seed every case as its own device, so ONE sweep exercises the multi-row
	// statement and the returned count can be checked against the whole table.
	wantFlipped := int64(0)
	for i, tc := range cases {
		token := deviceTokenFor(i)
		if _, err := api.MergeDeviceState(ctx, token, now.Add(-tc.age), nil, DeviceIdentity{}); err != nil {
			t.Fatalf("%s: seed failed: %v", tc.name, err)
		}
		if err := api.RDB.DB(ctx).Model(&DeviceState{}).
			Where("device_token = ?", token).
			Update("inactivity_timeout", tc.timeout).Error; err != nil {
			t.Fatalf("%s: setting timeout failed: %v", tc.name, err)
		}
		if isInactive(sql.NullTime{Time: now.Add(-tc.age), Valid: true}, tc.timeout, now) {
			wantFlipped++
		}
	}

	flipped, err := api.SweepInactive(core.WithSystemContext(ctx), now)
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}

	for i, tc := range cases {
		token := deviceTokenFor(i)
		states, err := api.DeviceStatesByDeviceToken(ctx, []string{token})
		if err != nil || len(states) != 1 {
			t.Fatalf("%s: lookup failed: %v (n=%d)", tc.name, err, len(states))
		}
		last := sql.NullTime{Time: now.Add(-tc.age), Valid: true}
		wantInactive := isInactive(last, tc.timeout, now)
		gotInactive := !states[0].Active
		if gotInactive != wantInactive {
			t.Errorf("%s: SQL says inactive=%v, isInactive says %v (age=%s timeout=%d)",
				tc.name, gotInactive, wantInactive, tc.age, tc.timeout)
		}
	}

	if flipped != wantFlipped {
		t.Errorf("sweep reported %d flipped, the case table expects %d", flipped, wantFlipped)
	}

	// The table is only evidence while it contains both answers. A table that is all
	// one polarity would pass against a predicate hardwired to that answer.
	if wantFlipped == 0 || wantFlipped == int64(len(cases)) {
		t.Fatalf("case table is one-sided (%d of %d flip) — it cannot detect a constant predicate",
			wantFlipped, len(cases))
	}
}

// TestSweepSkipsRowsItMustNotTouch covers the three exclusions the predicate carries
// that isInactive knows nothing about: a device with no recorded activity, a device
// whose presence is ASSERTED, and a row that is already inactive.
func TestSweepSkipsRowsItMustNotTouch(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	long := now.Add(-24 * time.Hour) // silent far past any timeout

	seed := func(token string, mutate map[string]any) {
		if _, err := api.MergeDeviceState(ctx, token, long, nil, DeviceIdentity{}); err != nil {
			t.Fatalf("%s: seed failed: %v", token, err)
		}
		if len(mutate) == 0 {
			return
		}
		if err := api.RDB.DB(ctx).Model(&DeviceState{}).
			Where("device_token = ?", token).Updates(mutate).Error; err != nil {
			t.Fatalf("%s: mutate failed: %v", token, err)
		}
	}

	seed("no-activity", map[string]any{"last_activity_time": nil})
	seed("asserted", map[string]any{"presence_source": PresenceSourceAsserted})
	seed("already-inactive", map[string]any{"active": false})
	// The positive control: without it, a sweep that silently did nothing at all would
	// pass every assertion below.
	seed("plain-inferred", nil)

	flipped, err := api.SweepInactive(core.WithSystemContext(ctx), now)
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if flipped != 1 {
		t.Fatalf("expected exactly the one sweepable device to flip, got %d", flipped)
	}

	for _, token := range []string{"no-activity", "asserted"} {
		states, err := api.DeviceStatesByDeviceToken(ctx, []string{token})
		if err != nil || len(states) != 1 {
			t.Fatalf("%s: lookup failed: %v (n=%d)", token, err, len(states))
		}
		if !states[0].Active {
			t.Errorf("%s was flipped inactive by the sweep and must not have been", token)
		}
	}
}

// TestInactivitySQLRefusesAnUnknownDialect pins the fail-closed branch. A dialect with
// no predicate must produce an error, never an empty fragment: an empty fragment is a
// sweep with no time bound, which flips the entire fleet offline in one statement.
func TestInactivitySQLRefusesAnUnknownDialect(t *testing.T) {
	now := time.Now()
	for _, known := range []string{"postgres", "sqlite"} {
		frag, args, err := inactivitySQL(known, now)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", known, err)
		}
		if frag == "" || len(args) != 2 {
			t.Fatalf("%s: degenerate predicate frag=%q args=%v", known, frag, args)
		}
	}
	frag, _, err := inactivitySQL("mysql", now)
	if err == nil {
		t.Fatalf("an unknown dialect returned a predicate %q instead of refusing", frag)
	}
	if frag != "" {
		t.Fatalf("a refused dialect still returned a fragment: %q", frag)
	}
}

func deviceTokenFor(i int) string {
	return "sweep-case-" + string(rune('a'+i))
}
