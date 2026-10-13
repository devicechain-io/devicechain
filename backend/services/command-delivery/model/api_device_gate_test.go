// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gateCtx(tenant string) context.Context {
	return core.WithTenant(context.Background(), tenant)
}

func enqueueFor(t *testing.T, api *Api, ctx context.Context, token, device string) *Command {
	t.Helper()
	created, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: token, DeviceToken: device, Name: "reboot"})
	require.NoError(t, err)
	return created
}

// Both claims refuse a disabled device's command and win again once the device is enabled. The
// claim is the one choke point every dispatch path passes (the sweep, the nudge, the LwM2M wake
// drain), so this is what makes "never sent while disabled" hold without each path knowing.
func TestClaimsRefuseADisabledDevice(t *testing.T) {
	api := newTestApi(t)
	ctx := gateCtx("A")
	byID := enqueueFor(t, api, ctx, "cmd-id", "dev-1")
	byToken := enqueueFor(t, api, ctx, "cmd-token", "dev-1")
	other := enqueueFor(t, api, ctx, "cmd-other", "dev-2")

	_, err := api.SetDeviceDisabled(ctx, "dev-1", true)
	require.NoError(t, err)

	_, claimed, err := api.MarkSent(ctx, byID.ID)
	require.NoError(t, err)
	assert.False(t, claimed, "MarkSent claimed a command for a disabled device")
	_, claimed, err = api.MarkSentByToken(ctx, byToken.Token)
	require.NoError(t, err)
	assert.False(t, claimed, "MarkSentByToken claimed a command for a disabled device")
	assert.Equal(t, CommandQueued.String(), loadOrFail(t, api, ctx, byID.ID).Status, "a refused claim must leave the row as it was")

	_, claimed, err = api.MarkSent(ctx, other.ID)
	require.NoError(t, err)
	assert.True(t, claimed, "a disabled device must not stop another device's claim")

	wasDisabled, err := api.SetDeviceDisabled(ctx, "dev-1", false)
	require.NoError(t, err)
	assert.True(t, wasDisabled)
	_, claimed, err = api.MarkSent(ctx, byID.ID)
	require.NoError(t, err)
	assert.True(t, claimed, "an enabled device's command must be claimable again")
}

// The record is per tenant: a device token is unique within a tenant, not across them.
func TestDeviceGateIsPerTenant(t *testing.T) {
	api := newTestApi(t)
	_, err := api.SetDeviceDisabled(gateCtx("A"), "dev-1", true)
	require.NoError(t, err)

	got, err := api.DisabledAmong(gateCtx("A"), []string{"dev-1", "dev-2"})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"dev-1": true}, got)
	got, err = api.DisabledAmong(gateCtx("B"), []string{"dev-1"})
	require.NoError(t, err)
	assert.Empty(t, got, "tenant B must not see tenant A's disabled device")

	b := enqueueFor(t, api, gateCtx("B"), "cmd-b", "dev-1")
	_, claimed, err := api.MarkSent(gateCtx("B"), b.ID)
	require.NoError(t, err)
	assert.True(t, claimed, "tenant B's device of the same token is not disabled")
}

// A command held for a disabled device still lapses by its TTL, as EXPIRED (it never went out).
func TestAHeldCommandForADisabledDeviceStillExpires(t *testing.T) {
	api := newTestApi(t)
	ctx := gateCtx("A")
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	created, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "lapsing", DeviceToken: "dev-1", Name: "x", ExpiresAt: &past})
	require.NoError(t, err)
	_, err = api.SetDeviceDisabled(ctx, "dev-1", true)
	require.NoError(t, err)
	held, err := api.HoldCommand(ctx, created.ID)
	require.NoError(t, err)
	require.True(t, held)

	count, _, err := api.ExpireStale(core.WithSystemContext(ctx), time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	assert.Equal(t, CommandExpired.String(), loadOrFail(t, api, ctx, created.ID).Status)
}

// The corrective walk adds what the facts missed, never overwrites a fact newer than its read,
// and reports the devices it found enabled.
func TestReconcileDisabled(t *testing.T) {
	api := newTestApi(t)
	ctx := gateCtx("A")

	// Recorded disabled by a fact; the list no longer names it (enabled while a fact was lost).
	_, err := api.SetDeviceDisabled(ctx, "was-disabled", true)
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)
	readStarted := time.Now()
	time.Sleep(5 * time.Millisecond)
	// A fact lands AFTER the walk began reading: it is newer than the list the walk holds.
	_, err = api.SetDeviceDisabled(ctx, "fresh-fact", true)
	require.NoError(t, err)
	_, err = api.SetDeviceDisabled(ctx, "enabled-by-fact", false)
	require.NoError(t, err)

	enabled, err := api.ReconcileDisabled(ctx, []string{"never-heard", "enabled-by-fact"}, readStarted)
	require.NoError(t, err)
	assert.Equal(t, []string{"was-disabled"}, enabled, "only the row the list contradicts, and is older than the read, is enabled")

	got, err := api.DisabledAmong(ctx, []string{"never-heard", "was-disabled", "fresh-fact", "enabled-by-fact"})
	require.NoError(t, err)
	assert.True(t, got["never-heard"], "a device the facts never named is added")
	assert.True(t, got["fresh-fact"], "a fact newer than the read must not be undone")
	assert.False(t, got["was-disabled"])
	assert.False(t, got["enabled-by-fact"], "the walk's older list must not re-disable a device a newer fact enabled")
}

// The hold count is the set cancel-all would cancel, and an enabled device holds nothing.
func TestDeviceCommandHoldAndCancelAll(t *testing.T) {
	api := newTestApi(t)
	ctx := gateCtx("A")
	queued := enqueueFor(t, api, ctx, "q", "dev-1")
	heldCmd := enqueueFor(t, api, ctx, "h", "dev-1")
	sent := enqueueFor(t, api, ctx, "s", "dev-1")
	otherDevice := enqueueFor(t, api, ctx, "o", "dev-2")
	_, claimed, err := api.MarkSent(ctx, sent.ID)
	require.NoError(t, err)
	require.True(t, claimed)

	disabled, held, err := api.DeviceCommandHold(ctx, "dev-1")
	require.NoError(t, err)
	assert.False(t, disabled)
	assert.Zero(t, held, "an enabled device has nothing held on its account")

	_, err = api.SetDeviceDisabled(ctx, "dev-1", true)
	require.NoError(t, err)
	ok, err := api.HoldCommand(ctx, heldCmd.ID)
	require.NoError(t, err)
	require.True(t, ok)

	disabled, held, err = api.DeviceCommandHold(ctx, "dev-1")
	require.NoError(t, err)
	assert.True(t, disabled)
	assert.Equal(t, int64(2), held, "QUEUED and HELD count; the SENT command is at the device")

	cancelled, err := api.CancelDeviceCommands(ctx, "dev-1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), cancelled)
	assert.Equal(t, CommandCancelled.String(), loadOrFail(t, api, ctx, queued.ID).Status)
	assert.Equal(t, CommandCancelled.String(), loadOrFail(t, api, ctx, heldCmd.ID).Status)
	assert.Equal(t, CommandSent.String(), loadOrFail(t, api, ctx, sent.ID).Status, "a sent command is never cancelled")
	assert.Equal(t, CommandQueued.String(), loadOrFail(t, api, ctx, otherDevice.ID).Status, "another device is untouched")

	_, held, err = api.DeviceCommandHold(ctx, "dev-1")
	require.NoError(t, err)
	assert.Zero(t, held)
}
