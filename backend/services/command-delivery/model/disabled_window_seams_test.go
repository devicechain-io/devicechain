// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two seams where a read and the claim it feeds, or a claim and the confirm after it, disagreed
// about a disabled device or a maintenance window.

// The wake drain's read must not offer a disabled device's backlog: its claim refuses every such
// row, and the LwM2M drain read a page it could claim none of as a full page to ask for again at
// once. Per tenant, and lifted by an enable — so a read that excluded everything cannot pass.
func TestDrainableCommandsExcludesADisabledDevicesBacklog(t *testing.T) {
	api := newTestApi(t)
	a, b := core.WithTenant(context.Background(), "A"), core.WithTenant(context.Background(), "B")
	seedDrainable(t, api, a, 1, "dev-off", CommandHeld, sql.NullTime{})
	seedDrainable(t, api, a, 2, "dev-off", CommandParked, sql.NullTime{})
	seedDrainable(t, api, a, 3, "dev-on", CommandHeld, sql.NullTime{})
	seedDrainable(t, api, b, 4, "dev-off", CommandHeld, sql.NullTime{})

	// Control: before the disable the backlog is drainable.
	got, err := api.DrainableCommands(a, "dev-off", 32)
	require.NoError(t, err)
	require.Equal(t, []uint{1, 2}, idsOf(got), "control: an enabled device's backlog is drainable")

	_, err = api.SetDeviceDisabled(a, "dev-off", true)
	require.NoError(t, err)

	got, err = api.DrainableCommands(a, "dev-off", 32)
	require.NoError(t, err)
	assert.Empty(t, idsOf(got), "a disabled device's backlog was offered to a drain whose claim must refuse it")
	got, err = api.DrainableCommands(a, "dev-on", 32)
	require.NoError(t, err)
	assert.Equal(t, []uint{3}, idsOf(got), "another device of the same tenant still drains")
	got, err = api.DrainableCommands(b, "dev-off", 32)
	require.NoError(t, err)
	assert.Equal(t, []uint{4}, idsOf(got), "the same token in another tenant is not disabled")

	_, err = api.SetDeviceDisabled(a, "dev-off", false)
	require.NoError(t, err)
	got, err = api.DrainableCommands(a, "dev-off", 32)
	require.NoError(t, err)
	assert.Equal(t, []uint{1, 2}, idsOf(got), "an enable makes the backlog drainable again")
}

// sentInsideWindow seeds a QUEUED command bound to an opening that is open now and claims it, as
// the sweep does moments before the window closes. It returns the claim's nonce.
func sentInsideWindow(t *testing.T, api *Api, ctx context.Context, token string) string {
	t.Helper()
	cmd := &Command{
		DeviceToken:   "dev-1",
		Name:          "reboot",
		Status:        CommandQueued.String(),
		DispatchAfter: sql.NullTime{Time: time.Now().Add(-time.Hour), Valid: true},
		DispatchUntil: sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true},
		WindowToken:   sql.NullString{String: "mw", Valid: true},
	}
	cmd.Token = token
	require.NoError(t, api.RDB.DB(ctx).Create(cmd).Error)
	nonce, claimed, err := api.MarkSent(ctx, cmd.ID)
	require.NoError(t, err)
	require.True(t, claimed, "the sweep's claim inside the window")
	return nonce
}

// closeWindow moves the row's opening into the past, as the clock does.
func closeWindow(t *testing.T, api *Api, ctx context.Context, token string) {
	t.Helper()
	require.NoError(t, api.RDB.DB(ctx).Model(&Command{}).Where("token = ?", token).
		Update("dispatch_until", time.Now().Add(-time.Second)).Error)
}

// The race: the sweep claims inside the window, the window closes, the transport's confirm
// arrives. The confirm must refuse (nothing actuates outside the window) AND the row must leave
// SENT — before, it stayed there for the stranded pass, which runs only with presence wired, and
// otherwise reached TIMEOUT against a device it never reached.
func TestAConfirmAfterTheWindowClosedParksTheRow(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	nonce := sentInsideWindow(t, api, ctx, "late")
	closeWindow(t, api, ctx, "late")

	next, won, err := api.ConfirmDispatch(ctx, "late", nonce)
	require.NoError(t, err)
	require.False(t, won, "a confirm after the window closed must not actuate")
	require.Empty(t, next)

	row := commandRow(t, api, ctx, "late")
	assert.Equal(t, CommandParked.String(), row.Status, "a refused confirm outside the window strands the row in SENT")
	assert.False(t, row.SentTime.Valid, "a parked row sent nothing, so it carries no sent_time")
	assert.Equal(t, nonce, row.DispatchNonce.String, "the park keeps the nonce, as ParkClaim does")

	// The stranded pass has nothing to clean up, at any horizon.
	stranded, _, err := api.StrandedSentCommands(ctx, StrandedCursor{}, time.Now().Add(time.Hour), 10)
	require.NoError(t, err)
	assert.Empty(t, tokensOf(stranded), "the row is left for the stranded-SENT pass")

	// A redelivered copy of the same envelope is still refused, and changes nothing.
	_, won, err = api.ConfirmDispatch(ctx, "late", nonce)
	require.NoError(t, err)
	assert.False(t, won)
	assert.Equal(t, CommandParked.String(), commandRow(t, api, ctx, "late").Status)

	// It is not lost: once its window opens again it is the drain's to deliver.
	drained, err := api.DrainableCommands(ctx, "dev-1", 32)
	require.NoError(t, err)
	assert.Empty(t, tokensOf(drained), "not drainable while its window is closed")
	require.NoError(t, api.RDB.DB(ctx).Model(&Command{}).Where("token = ?", "late").
		Update("dispatch_until", time.Now().Add(time.Hour)).Error)
	drained, err = api.DrainableCommands(ctx, "dev-1", 32)
	require.NoError(t, err)
	assert.Equal(t, []string{"late"}, tokensOf(drained), "drainable again in the next opening")
}

// A stale confirm is not a closed window: the row it names has moved on, and the park must not
// touch the row's CURRENT dispatch — even when that dispatch is outside the window too.
func TestAStaleConfirmDoesNotParkTheCurrentDispatch(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	nonce := sentInsideWindow(t, api, ctx, "moved")
	closeWindow(t, api, ctx, "moved")

	_, won, err := api.ConfirmDispatch(ctx, "moved", nonce+"-superseded")
	require.NoError(t, err)
	require.False(t, won)
	row := commandRow(t, api, ctx, "moved")
	assert.Equal(t, CommandSent.String(), row.Status, "a confirm quoting another dispatch parked this one")
	assert.Equal(t, nonce, row.DispatchNonce.String)
}

// parkOutsideWindow parks only a row its window no longer admits. Driven directly, because no
// confirm reaches it for an admitted row except through a cancel racing the brake write.
func TestParkOutsideWindowLeavesAnAdmittedRowSent(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	nonce := sentInsideWindow(t, api, ctx, "open")

	require.NoError(t, api.parkOutsideWindow(ctx, "token = ? AND dispatch_nonce = ?", []any{"open", nonce}))
	assert.Equal(t, CommandSent.String(), commandRow(t, api, ctx, "open").Status,
		"a row inside its window was parked")

	closeWindow(t, api, ctx, "open")
	require.NoError(t, api.parkOutsideWindow(ctx, "token = ? AND dispatch_nonce = ?", []any{"open", nonce}))
	assert.Equal(t, CommandParked.String(), commandRow(t, api, ctx, "open").Status,
		"control: the same row is parked once its window has closed")
}

// A cancel that commits between the confirm's brake write and the park must not leave a
// called-off command PARKED (claimable): the row stays SENT for the stranded pass to cancel.
func TestParkOutsideWindowLeavesACalledOffBatchsCommandSent(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	batch := seedBatchWithCommands(t, api, ctx, "fleet-w", map[string]string{"pump-w": CommandQueued.String()})
	row := commandRow(t, api, ctx, "cmd-pump-w")
	nonce, claimed, err := api.MarkSent(ctx, row.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	closeWindow(t, api, ctx, "cmd-pump-w")
	_, err = api.CancelCommandBatch(ctx, batch)
	require.NoError(t, err)

	require.NoError(t, api.parkOutsideWindow(ctx, "token = ? AND dispatch_nonce = ?", []any{"cmd-pump-w", nonce}))
	assert.Equal(t, CommandSent.String(), commandRow(t, api, ctx, "cmd-pump-w").Status,
		"a called-off batch's command was parked, which makes it claimable again")
}

// The corrective walk's enable half is one statement per chunk; it must still report exactly the
// rows it moved, and only in its own tenant.
func TestReconcileDisabledEnablesInBulkWithinItsTenant(t *testing.T) {
	api := newTestApi(t)
	a, b := gateCtx("A"), gateCtx("B")
	for _, dev := range []string{"d1", "d2", "d3"} {
		_, err := api.SetDeviceDisabled(a, dev, true)
		require.NoError(t, err)
		_, err = api.SetDeviceDisabled(b, dev, true)
		require.NoError(t, err)
	}
	time.Sleep(5 * time.Millisecond)
	readStarted := time.Now()

	enabled, err := api.ReconcileDisabled(a, []string{"d2"}, readStarted)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"d1", "d3"}, enabled)

	got, err := api.DisabledAmong(a, []string{"d1", "d2", "d3"})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"d2": true}, got)
	got, err = api.DisabledAmong(b, []string{"d1", "d2", "d3"})
	require.NoError(t, err)
	assert.Len(t, got, 3, "another tenant's record was changed by this tenant's walk")
}
