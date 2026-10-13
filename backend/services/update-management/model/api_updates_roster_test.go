// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/require"
)

// 🔴 A roster fact is a prompt to ask again, so a disable or an enable is felt by the very next
// message instead of up to DisabledCacheTTL later.
func TestARosterFactDropsTheCachedDisabledAnswer(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	ctx := tenantCtx("acme")
	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	report := func(seq uint64) error {
		_, err := e.api.ApplyReport(ctx, "dev-1", progress(v, seq, ota.StageReceived, "boot-1"))
		return err
	}
	require.NoError(t, report(1)) // caches "enabled"

	// Disabled inside the TTL: still unseen, until the fact arrives.
	e.devices.disabled = map[string]bool{"acme/dev-1": true}
	require.NoError(t, report(2), "fixture: the stale answer is the premise")
	require.NoError(t, e.api.DeviceRosterChanged(ctx, "dev-1", false))
	require.ErrorIs(t, report(3), ErrDeviceDisabled, "the disable fact must take effect at once")

	// And the enable, inside the TTL of the cached disabled answer.
	e.devices.disabled = nil
	require.NoError(t, e.api.DeviceRosterChanged(ctx, "dev-1", true))
	require.NoError(t, report(4), "the enable fact must take effect at once")
}

// An ENABLE re-offers the device its current assignment, as a sync answer would: the device's
// syncs while disabled were refused in silence.
func TestAnEnableFactReOffersTheCurrentAssignment(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	ctx := tenantCtx("acme")
	_, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	e.devices.disabled = map[string]bool{"acme/dev-1": true}
	e.advance(DisabledCacheTTL)

	sent := len(e.desired.sent)
	// An enable fact the device-management lookup does not bear out offers nothing.
	require.NoError(t, e.api.DeviceRosterChanged(ctx, "dev-1", true))
	require.Len(t, e.desired.sent, sent, "a still-disabled device was re-offered its assignment")

	e.devices.disabled = nil
	require.NoError(t, e.api.DeviceRosterChanged(ctx, "dev-1", true))
	require.Len(t, e.desired.sent, sent+1, "the enable must re-offer the assignment exactly once")
	require.Equal(t, "dev-1", e.desired.sent[sent].device)
}

// An enable for a device with nothing assigned costs nothing and offers nothing; a lookup that
// cannot be answered is an error (the fact is redelivered), never a silent skip.
func TestAnEnableFactWithNothingAssignedOffersNothingAndAFailedLookupIsRetried(t *testing.T) {
	e := newUpdateEnv(t)
	ctx := tenantCtx("acme")
	require.NoError(t, e.api.DeviceRosterChanged(ctx, "dev-2", true))
	require.Empty(t, e.desired.sent)
	require.Zero(t, e.devices.lookups)

	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	_, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	e.devices.err = errUnreachable
	require.ErrorIs(t, e.api.DeviceRosterChanged(ctx, "dev-1", true), errUnreachable)
}

// The hold on a disabled device's desired channel covers the messages that DELIVER work (an
// assignment, a grant, a sync answer): none is published to it. A CANCEL still goes out, on
// purpose: it only ever tells the device to stop, so it cannot start anything a quarantine is meant
// to prevent, and an operator who cancels an update on a quarantined device wants it stopped.
func TestACancelStillReachesADisabledDeviceButNothingElseDoes(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	ctx := tenantCtx("acme")
	v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	e.devices.disabled = map[string]bool{"acme/dev-1": true}
	e.advance(DisabledCacheTTL)
	sent := len(e.desired.sent)

	_, err = e.api.GrantDownload(ctx, "dev-1", v.Assignment.Token)
	require.ErrorIs(t, err, ErrDeviceDisabled)
	_, err = e.api.CurrentUpdates(ctx, "dev-1")
	require.ErrorIs(t, err, ErrDeviceDisabled)
	require.Len(t, e.desired.sent, sent, "work was published to a disabled device")

	_, err = e.api.CancelUpdate(ctx, v.Assignment.Token)
	require.NoError(t, err)
	require.Len(t, e.desired.sent, sent+1, "the cancel was held back")
}

// Only the enable TOGGLE re-offers. A disable fact and a re-type / create / type re-point fact (enabled
// false) drop the cached answer and cost no lookup and no publish, however many arrive: a campaign's
// bulk re-point must not become one lookup and one duplicate assignment per device.
func TestOnlyAnEnableToggleReOffersAndOtherFactsCostNoLookup(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
	ctx := tenantCtx("acme")
	_, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
	require.NoError(t, err)
	lookups, sent := e.devices.lookups, len(e.desired.sent)

	for range 3 {
		require.NoError(t, e.api.DeviceRosterChanged(ctx, "dev-1", false))
	}
	require.Equal(t, lookups, e.devices.lookups, "a non-enable fact asked device-management")
	require.Len(t, e.desired.sent, sent, "a non-enable fact re-offered the assignment")

	require.NoError(t, e.api.DeviceRosterChanged(ctx, "dev-1", true))
	require.Len(t, e.desired.sent, sent+1)
}
