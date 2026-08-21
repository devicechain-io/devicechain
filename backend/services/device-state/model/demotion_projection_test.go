// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/presence"
)

// These tests cover the write path's DEMOTED arm: a source releasing custody of a
// device (ADR-067). The defect they exist to pin is that an ASSERTED row is repaired by
// exactly two mechanisms — the inactivity sweep and the implicit heartbeat — and the
// asserted path suppresses BOTH. With no source left to speak for it, such a row freezes
// at whatever it last held, in whichever direction, indefinitely. A demotion is the one
// transition that hands it back to both.

func conn(s uint64, tm time.Time) *PresenceTransition {
	return &PresenceTransition{Claim: presence.ClaimConnected, SessionId: s, OccurredAt: tm}
}

func disc(s uint64, tm time.Time) *PresenceTransition {
	return &PresenceTransition{Claim: presence.ClaimDisconnected, SessionId: s, OccurredAt: tm}
}

func demote(s uint64, tm time.Time) *PresenceTransition {
	return &PresenceTransition{Claim: presence.ClaimDemoted, SessionId: s, OccurredAt: tm}
}

// TestADemotionCarriesNoConnectivityEdge is the load-bearing one. A demotion says who
// has custody, never what the device is doing. Writing Active (or any of the
// connectivity stamps) from the demotion arm would fabricate a connectivity edge out of
// an administrative one — and because DETECT keys off the same predicate, it would raise
// an offline alarm for every device a disabled source demotes.
func TestADemotionCarriesNoConnectivityEdge(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	t0 := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	if _, err := api.MergeDeviceState(ctx, "sp-live", t0, conn(100, t0), DeviceIdentity{}); err != nil {
		t.Fatalf("connect merge: %v", err)
	}
	before, err := api.MergeDeviceState(ctx, "sp-live", t0.Add(time.Minute), nil, DeviceIdentity{})
	if err != nil {
		t.Fatalf("activity merge: %v", err)
	}

	after, err := api.MergeDeviceState(ctx, "sp-live", t0.Add(2*time.Minute), demote(100, t0.Add(2*time.Minute)), DeviceIdentity{})
	if err != nil {
		t.Fatalf("demotion merge: %v", err)
	}

	if after.PresenceSource != PresenceSourceInferred {
		t.Fatalf("demotion did not return the row to inferred: %+v", after)
	}
	if !after.PresenceTime.Valid || !after.PresenceTime.Time.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("demotion did not advance the ordering stamp: %+v", after)
	}
	// Everything below is what a demotion must NOT touch. Active is the one that costs
	// an alarm storm; the rest would silently corrupt the device's history.
	if after.Active != before.Active {
		t.Fatalf("demotion moved Active %v -> %v", before.Active, after.Active)
	}
	if after.SessionId != before.SessionId {
		t.Fatalf("demotion moved SessionId %d -> %d (the released session must stay named)", before.SessionId, after.SessionId)
	}
	if after.LastConnectTime != before.LastConnectTime {
		t.Fatalf("demotion moved LastConnectTime %v -> %v", before.LastConnectTime, after.LastConnectTime)
	}
	if after.LastDisconnectTime != before.LastDisconnectTime {
		t.Fatalf("demotion moved LastDisconnectTime %v -> %v", before.LastDisconnectTime, after.LastDisconnectTime)
	}
	if after.LastActivityTime != before.LastActivityTime {
		t.Fatalf("demotion moved LastActivityTime %v -> %v", before.LastActivityTime, after.LastActivityTime)
	}
	if after.InactivityAlarmTime != before.InactivityAlarmTime {
		t.Fatalf("demotion moved InactivityAlarmTime %v -> %v", before.InactivityAlarmTime, after.InactivityAlarmTime)
	}
}

// TestADemotionReturnsAStaleOnlineRowToTheSweep covers the MAJORITY frozen direction.
// With the tap off no disconnect advisory ever arrives either, so a device that was
// connected at disable time reads connected forever. SweepInactive filters ASSERTED out
// at the scan, which is why the row is unreachable until it is demoted. The first sweep
// here is the control: it must find nothing, or the test is measuring the wrong thing.
func TestADemotionReturnsAStaleOnlineRowToTheSweep(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	sys := core.WithSystemContext(ctx)
	t0 := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	if _, err := api.MergeDeviceState(ctx, "sp-stuck", t0, conn(100, t0), DeviceIdentity{}); err != nil {
		t.Fatalf("connect merge: %v", err)
	}

	flipped, err := api.SweepInactive(sys, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("control sweep: %v", err)
	}
	if flipped != 0 {
		t.Fatalf("control sweep flipped %d asserted rows; the sweep is supposed to skip them", flipped)
	}

	if _, err := api.MergeDeviceState(ctx, "sp-stuck", t0.Add(time.Minute), demote(100, t0.Add(time.Minute)), DeviceIdentity{}); err != nil {
		t.Fatalf("demotion merge: %v", err)
	}

	flipped, err = api.SweepInactive(sys, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("post-demotion sweep: %v", err)
	}
	if flipped != 1 {
		t.Fatalf("expected the demoted row to become sweepable, got %d flips", flipped)
	}

	states, err := api.DeviceStatesByDeviceToken(ctx, []string{"sp-stuck"})
	if err != nil || len(states) != 1 {
		t.Fatalf("lookup after sweep: %v (n=%d)", err, len(states))
	}
	if states[0].Active {
		t.Fatalf("swept device still active: %+v", states[0])
	}
}

// TestADemotionReturnsAWedgedOfflineRowToTheHeartbeat covers the other frozen direction:
// a device that disconnected before the tap went away, reconnected physically, and can
// never be marked live again because a data event does not flip Active on an ASSERTED
// row. This is the half with tenant-wide blast radius — commands to such a device are
// HELD, and HELD counts toward the per-tenant ceiling.
func TestADemotionReturnsAWedgedOfflineRowToTheHeartbeat(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	t0 := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	if _, err := api.MergeDeviceState(ctx, "sp-wedged", t0, conn(100, t0), DeviceIdentity{}); err != nil {
		t.Fatalf("connect merge: %v", err)
	}
	if _, err := api.MergeDeviceState(ctx, "sp-wedged", t0.Add(time.Minute), disc(100, t0.Add(time.Minute)), DeviceIdentity{}); err != nil {
		t.Fatalf("disconnect merge: %v", err)
	}

	// Control: data alone cannot reach it while it is asserted.
	ds, err := api.MergeDeviceState(ctx, "sp-wedged", t0.Add(2*time.Minute), nil, DeviceIdentity{})
	if err != nil {
		t.Fatalf("control data merge: %v", err)
	}
	if ds.Active {
		t.Fatalf("a data event resurrected an asserted-dead device: %+v", ds)
	}

	if _, err := api.MergeDeviceState(ctx, "sp-wedged", t0.Add(3*time.Minute), demote(100, t0.Add(3*time.Minute)), DeviceIdentity{}); err != nil {
		t.Fatalf("demotion merge: %v", err)
	}

	ds, err = api.MergeDeviceState(ctx, "sp-wedged", t0.Add(4*time.Minute), nil, DeviceIdentity{})
	if err != nil {
		t.Fatalf("post-demotion data merge: %v", err)
	}
	if !ds.Active {
		t.Fatalf("a demoted row was not repaired by its next data event: %+v", ds)
	}
	if ds.InactivityAlarmTime.Valid {
		t.Fatalf("repair did not clear the inactivity alarm: %+v", ds)
	}
}

// TestARejectedTransitionDoesNotPromoteToAsserted pins the promotion inside the ordering
// guard. Before the demotion existed, PresenceSource was stamped ASSERTED before
// presence.Decide had spoken, so a transition the guard REFUSED still promoted the row —
// latent only because no INFERRED row could hold a session and a presence time. A
// demotion creates exactly that shape, at which point a stale echo from the released
// session re-asserts the row and re-wedges it, undoing the repair.
func TestARejectedTransitionDoesNotPromoteToAsserted(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	t0 := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	if _, err := api.MergeDeviceState(ctx, "sp-echo", t0, conn(100, t0), DeviceIdentity{}); err != nil {
		t.Fatalf("connect merge: %v", err)
	}
	demoted, err := api.MergeDeviceState(ctx, "sp-echo", t0.Add(2*time.Minute), demote(100, t0.Add(2*time.Minute)), DeviceIdentity{})
	if err != nil {
		t.Fatalf("demotion merge: %v", err)
	}
	if demoted.PresenceSource != PresenceSourceInferred || demoted.SessionId != 100 || !demoted.PresenceTime.Valid {
		t.Fatalf("precondition: expected an inferred row holding (session, time), got %+v", demoted)
	}

	// A stale echo from the released session: lower-or-equal session, older stamp. The
	// guard refuses it, so it must leave no trace at all.
	after, err := api.MergeDeviceState(ctx, "sp-echo", t0.Add(3*time.Minute), disc(100, t0.Add(time.Minute)), DeviceIdentity{})
	if err != nil {
		t.Fatalf("stale echo merge: %v", err)
	}
	if after.PresenceSource != PresenceSourceInferred {
		t.Fatalf("a REFUSED transition promoted the row back to asserted: %+v", after)
	}
	if !after.PresenceTime.Time.Equal(t0.Add(2 * time.Minute)) {
		t.Fatalf("a refused transition moved the ordering stamp: %+v", after)
	}
}

// TestALateEchoAfterADemotionDoesNotOverwriteTheDisconnectTime is the killing test for
// the neverAsserted spelling. Derived from PresenceSource, a demoted row reads as "no
// source has ever spoken for this device", so the first-authoritative-word branch fires
// and a late higher-session DISCONNECT overwrites a real death time. Derived from
// PresenceTime it reads correctly, because a demotion keeps the stamp.
func TestALateEchoAfterADemotionDoesNotOverwriteTheDisconnectTime(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	t0 := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	death := t0.Add(time.Minute)

	if _, err := api.MergeDeviceState(ctx, "sp-late", t0, conn(100, t0), DeviceIdentity{}); err != nil {
		t.Fatalf("connect merge: %v", err)
	}
	if _, err := api.MergeDeviceState(ctx, "sp-late", death, disc(100, death), DeviceIdentity{}); err != nil {
		t.Fatalf("disconnect merge: %v", err)
	}
	if _, err := api.MergeDeviceState(ctx, "sp-late", t0.Add(2*time.Minute), demote(100, t0.Add(2*time.Minute)), DeviceIdentity{}); err != nil {
		t.Fatalf("demotion merge: %v", err)
	}

	after, err := api.MergeDeviceState(ctx, "sp-late", t0.Add(9*time.Minute), disc(200, t0.Add(9*time.Minute)), DeviceIdentity{})
	if err != nil {
		t.Fatalf("late echo merge: %v", err)
	}
	if !after.LastDisconnectTime.Time.Equal(death) {
		t.Fatalf("a late higher-session disconnect overwrote the real death time %v with %v", death, after.LastDisconnectTime.Time)
	}
	// The counterweight: the transition IS in order, so it must still re-assert and
	// advance the marker. A neverAsserted that simply refused everything would pass the
	// assertion above for the wrong reason.
	if after.PresenceSource != PresenceSourceAsserted || after.SessionId != 200 {
		t.Fatalf("an in-order transition after a demotion failed to re-assert: %+v", after)
	}
}

// TestAStaleDataEventDoesNotResurrectASweptDevice covers the second half of the same
// asymmetry: the activity advance was always guarded on freshness, the resurrect was
// not. A redelivered or store-and-forward event from before the silence could therefore
// bring a swept device back to life and leave it to be swept again.
func TestAStaleDataEventDoesNotResurrectASweptDevice(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	sys := core.WithSystemContext(ctx)
	t0 := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	if _, err := api.MergeDeviceState(ctx, "d-1", t0, nil, DeviceIdentity{}); err != nil {
		t.Fatalf("initial merge: %v", err)
	}
	if flipped, err := api.SweepInactive(sys, t0.Add(2*time.Hour)); err != nil || flipped != 1 {
		t.Fatalf("sweep: %v (flipped=%d)", err, flipped)
	}

	stale, err := api.MergeDeviceState(ctx, "d-1", t0.Add(-time.Hour), nil, DeviceIdentity{})
	if err != nil {
		t.Fatalf("stale merge: %v", err)
	}
	if stale.Active {
		t.Fatalf("an event older than the recorded activity resurrected a swept device: %+v", stale)
	}
	if !stale.LastActivityTime.Time.Equal(t0) {
		t.Fatalf("stale event rolled activity back: %+v", stale)
	}

	// Counterweight: a FRESH event must still resurrect it, or the gate is simply broken.
	fresh, err := api.MergeDeviceState(ctx, "d-1", t0.Add(3*time.Hour), nil, DeviceIdentity{})
	if err != nil {
		t.Fatalf("fresh merge: %v", err)
	}
	if !fresh.Active || fresh.InactivityAlarmTime.Valid {
		t.Fatalf("a fresh event failed to resurrect a swept device: %+v", fresh)
	}
}

// TestADemotionForAnUnknownDeviceDoesNotCreateAnAssertedRow covers the one path that
// never consults presence.Decide at all: row creation. acceptsDemotion's prior.HasTime
// conjunct cannot fire there, so without an explicit guard the very transition that
// releases custody would instead CLAIM it.
func TestADemotionForAnUnknownDeviceDoesNotCreateAnAssertedRow(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	t0 := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	ds, err := api.MergeDeviceState(ctx, "sp-unknown", t0, demote(100, t0), DeviceIdentity{})
	if err != nil {
		t.Fatalf("demotion merge: %v", err)
	}
	if ds.PresenceSource != PresenceSourceInferred {
		t.Fatalf("a demotion created an asserted row: %+v", ds)
	}
	if ds.Active || ds.PresenceTime.Valid || ds.SessionId != 0 {
		t.Fatalf("a demotion for an unknown device invented presence state: %+v", ds)
	}
	if ds.LastConnectTime.Valid || ds.LastDisconnectTime.Valid || ds.LastActivityTime.Valid {
		t.Fatalf("a demotion for an unknown device invented timestamps: %+v", ds)
	}
}

// TestAnInertRowIsPopulatedByItsFirstDataEvent guards newDeviceState's own promise about
// the row a demotion for an unknown device leaves behind: it is inert, and "the device's
// next data event populates it the ordinary inferred way". That row has no
// LastActivityTime at all, so a freshness test spelled as `Valid && After(...)` — rather
// than `!Valid || After(...)` — would make it inert permanently. The same shape is
// reachable without any demotion, from a row created by a first-event DISCONNECTED.
func TestAnInertRowIsPopulatedByItsFirstDataEvent(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	t0 := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	inert, err := api.MergeDeviceState(ctx, "sp-inert", t0, demote(100, t0), DeviceIdentity{})
	if err != nil {
		t.Fatalf("demotion merge: %v", err)
	}
	if inert.LastActivityTime.Valid {
		t.Fatalf("precondition: expected a row with no activity time, got %+v", inert)
	}

	ds, err := api.MergeDeviceState(ctx, "sp-inert", t0.Add(time.Minute), nil, DeviceIdentity{})
	if err != nil {
		t.Fatalf("first data merge: %v", err)
	}
	if !ds.Active {
		t.Fatalf("an inert row was not brought to life by its first data event: %+v", ds)
	}
	if !ds.LastActivityTime.Valid || !ds.LastActivityTime.Time.Equal(t0.Add(time.Minute)) {
		t.Fatalf("first data event did not record activity: %+v", ds)
	}
}

// TestAFirstAuthoritativeConnectStampsWithoutASessionId is the one shape in which
// neverAsserted is the ONLY thing that stamps a connect. A producer may legitimately send
// no session id — the resolver parses an empty one to zero and calls it absent — so an
// INFERRED-active device receiving its first authoritative CONNECTED sees neither a flip
// (it was already active) nor a new session (zero to zero). Without the first-word term
// the platform's first authoritative word about the device would leave no mark.
func TestAFirstAuthoritativeConnectStampsWithoutASessionId(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	t0 := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	seeded, err := api.MergeDeviceState(ctx, "sp-nosession", t0, nil, DeviceIdentity{})
	if err != nil {
		t.Fatalf("seed merge: %v", err)
	}
	if !seeded.Active || seeded.SessionId != 0 || seeded.PresenceTime.Valid {
		t.Fatalf("precondition: expected an inferred active row with no presence stamp, got %+v", seeded)
	}

	ds, err := api.MergeDeviceState(ctx, "sp-nosession", t0.Add(time.Minute), conn(0, t0.Add(time.Minute)), DeviceIdentity{})
	if err != nil {
		t.Fatalf("first connect merge: %v", err)
	}
	if ds.PresenceSource != PresenceSourceAsserted {
		t.Fatalf("first authoritative connect did not promote: %+v", ds)
	}
	if !ds.LastConnectTime.Time.Equal(t0.Add(time.Minute)) {
		t.Fatalf("first authoritative connect left LastConnectTime at %v: %+v", ds.LastConnectTime.Time, ds)
	}
}

// TestAnInvalidClaimDoesNotCreateAnAssertedRow closes the same hole the demotion guard
// closes, for the claim nobody writes on purpose. Row creation never consults
// presence.Decide, so the Claim type's central promise — that a literal which forgets the
// field is REFUSED rather than read as a death — held only on the update path. A struct
// literal omitting Claim is exactly what the mapper produces on a state it cannot map, and
// the resulting row is asserted-dead: commands to it are then HELD, on the strength of a
// death nobody ever reported.
func TestAnInvalidClaimDoesNotCreateAnAssertedRow(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "A")
	t0 := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	for name, claim := range map[string]presence.Claim{
		"the zero value a literal produces by omission": presence.ClaimUnset,
		"a claim from outside the vocabulary":           presence.Claim(99),
	} {
		t.Run(name, func(t *testing.T) {
			api := newTestApi(t)
			ds, err := api.MergeDeviceState(ctx, "sp-bogus", t0,
				&PresenceTransition{Claim: claim, SessionId: 5, OccurredAt: t0}, DeviceIdentity{})
			if err != nil {
				t.Fatalf("merge: %v", err)
			}
			if ds.PresenceSource != PresenceSourceInferred {
				t.Fatalf("an unusable claim created an asserted row: %+v", ds)
			}
			if ds.LastDisconnectTime.Valid {
				t.Fatalf("an unusable claim fabricated a death time: %+v", ds)
			}
			if ds.Active || ds.PresenceTime.Valid || ds.SessionId != 0 {
				t.Fatalf("an unusable claim invented presence state: %+v", ds)
			}
		})
	}
}

// TestAFrozenClockDeviceIsNotPermanentlyOffline pins the boundary of the freshness gate.
// A device whose clock is stuck stamps every event with the same time — broken, but still
// a device SENDING DATA. Gating the resurrect on a STRICT "newer than recorded activity"
// makes its first sweep its last state change: every later event compares equal, never
// resurrects, and the row reads inactive forever while telemetry keeps arriving. The gate
// exists to refuse events from the PAST, and an equal stamp is not the past.
func TestAFrozenClockDeviceIsNotPermanentlyOffline(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	sys := core.WithSystemContext(ctx)
	t0 := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	if _, err := api.MergeDeviceState(ctx, "stuck-clock", t0, nil, DeviceIdentity{}); err != nil {
		t.Fatalf("initial merge: %v", err)
	}
	if flipped, err := api.SweepInactive(sys, t0.Add(2*time.Hour)); err != nil || flipped != 1 {
		t.Fatalf("sweep: %v (flipped=%d)", err, flipped)
	}

	// The device keeps reporting, always stamping the same instant.
	ds, err := api.MergeDeviceState(ctx, "stuck-clock", t0, nil, DeviceIdentity{})
	if err != nil {
		t.Fatalf("post-sweep merge: %v", err)
	}
	if !ds.Active {
		t.Fatalf("a device that is still sending data reads permanently inactive: %+v", ds)
	}
	if ds.InactivityAlarmTime.Valid {
		t.Fatalf("the inactivity alarm was not cleared on resurrect: %+v", ds)
	}
}
