// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// The stored inactivity_alarm_time behaves as it did in v1.0, which v1.1 changed and the
// deprecated GraphQL field must keep serving: the SWEEP sets it, becoming active again clears it,
// and a disconnect report never touches it. The v1.1 derivation from last_disconnect_time
// failed the first case below.
func TestInactivityAlarmTimeIsSetBySweepAndNotByADisconnectReport(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	sys := core.WithSystemContext(ctx)
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	// CONNECTED 12:00, DISCONNECTED 12:01, DEMOTED to INFERRED 12:02: no sweep ever fired.
	for _, step := range []struct {
		at time.Time
		pt *PresenceTransition
	}{
		{t0, conn(1, t0)},
		{t0.Add(time.Minute), disc(1, t0.Add(time.Minute))},
		{t0.Add(2 * time.Minute), demote(1, t0.Add(2*time.Minute))},
	} {
		if _, err := api.MergeDeviceState(ctx, "reported", step.at, step.pt, DeviceIdentity{}); err != nil {
			t.Fatalf("merge: %v", err)
		}
	}
	rows, err := api.DeviceStates(ctx, DeviceStateSearchCriteria{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]DeviceState{}
	for _, r := range rows.Results {
		got[r.DeviceToken] = r
	}
	rep := got["reported"]
	if rep.Active || !rep.LastDisconnectTime.Valid || rep.PresenceSource != PresenceSourceInferred {
		t.Fatalf("the fixture is wrong: %+v", rep)
	}
	if rep.InactivityAlarmTime.Valid {
		t.Fatalf("a disconnect report set the inactivity alarm time: %v", rep.InactivityAlarmTime.Time)
	}

	// Silence: the sweep sets it, to the sweep instant, alongside last_disconnect_time.
	if _, err := api.MergeDeviceState(ctx, "silent", t0, nil, DeviceIdentity{}); err != nil {
		t.Fatal(err)
	}
	sweepAt := t0.Add(2 * time.Hour)
	if n, err := api.SweepInactive(sys, sweepAt); err != nil || n < 1 {
		t.Fatalf("sweep: %v (flipped %d)", err, n)
	}
	silent := stateOf(t, api, ctx, "silent")
	if !silent.InactivityAlarmTime.Valid || !silent.InactivityAlarmTime.Time.Equal(sweepAt) {
		t.Fatalf("the sweep did not stamp the alarm time: %+v", silent)
	}

	// A disconnect report after the sweep moves last_disconnect_time and not the alarm time.
	if _, err := api.MergeDeviceState(ctx, "silent", sweepAt.Add(time.Minute), disc(2, sweepAt.Add(time.Minute)), DeviceIdentity{}); err != nil {
		t.Fatal(err)
	}
	if after := stateOf(t, api, ctx, "silent"); !after.InactivityAlarmTime.Valid || !after.InactivityAlarmTime.Time.Equal(sweepAt) {
		t.Fatalf("a disconnect report moved the alarm time: %+v", after)
	}

	// Active again: cleared. (A separate device: the disconnect report above made "silent" ASSERTED,
	// and an ASSERTED device takes Active from reports alone.)
	if _, err := api.MergeDeviceState(ctx, "quiet", t0, nil, DeviceIdentity{}); err != nil {
		t.Fatal(err)
	}
	if n, err := api.SweepInactive(sys, sweepAt); err != nil || n != 1 {
		t.Fatalf("second sweep: %v (flipped %d)", err, n)
	}
	if swept := stateOf(t, api, ctx, "quiet"); !swept.InactivityAlarmTime.Valid {
		t.Fatalf("the second sweep did not stamp the alarm time: %+v", swept)
	}
	if _, err := api.MergeDeviceState(ctx, "quiet", sweepAt.Add(time.Hour), nil, DeviceIdentity{}); err != nil {
		t.Fatal(err)
	}
	if back := stateOf(t, api, ctx, "quiet"); !back.Active || back.InactivityAlarmTime.Valid {
		t.Fatalf("becoming active did not clear the alarm time: %+v", back)
	}
}

func stateOf(t *testing.T, api *Api, ctx context.Context, token string) DeviceState {
	t.Helper()
	rows, err := api.DeviceStates(ctx, DeviceStateSearchCriteria{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows.Results {
		if r.DeviceToken == token {
			return r
		}
	}
	t.Fatalf("no state for %q", token)
	return DeviceState{}
}
