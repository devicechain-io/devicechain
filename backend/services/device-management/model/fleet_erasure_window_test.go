// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The erasure repair reads one window of a tenant's lines a pass, in device order, and wraps: an
// orphan outside the window waits for the pass that reaches it, and an orphan made BEHIND the
// cursor (an old line whose device a previous-minor pod deletes later) is found once the cursor
// wraps. Every line is examined once a cycle; no pass reads them all.
func TestFleetErasureRepairWalksAWindowOfLinesAndWraps(t *testing.T) {
	api, ctx := fleetFixture(t, 5)
	prev := fleetErasureRepairWindow
	fleetErasureRepairWindow = 2
	t.Cleanup(func() { fleetErasureRepairWindow = prev })

	var devs []Device
	require.NoError(t, api.RDB.DB(ctx).Order("id").Find(&devs).Error)
	require.Len(t, devs, 5)
	op := seedExpiryOperation(t, api, ctx, "erasure-window", FleetDispatchDispatched, time.Now())
	lines := map[uint]uint{} // device -> line
	for i, d := range devs {
		line := FleetOperationLine{OperationId: int64(op.ID), Ordinal: int32(i + 2), Posting: FleetPostingPosted,
			TargetId: sql.NullInt64{Int64: int64(d.ID), Valid: true}, TargetKey: d.Token}
		require.NoError(t, api.RDB.DB(ctx).Create(&line).Error)
		lines[d.ID] = line.ID
	}
	redacted := func(d Device) bool {
		var l FleetOperationLine
		require.NoError(t, api.RDB.DB(ctx).First(&l, lines[d.ID]).Error)
		return l.RedactedAt.Valid
	}
	// What each pass's candidate read covers: the statements that anti-join the devices.
	var windows []string
	require.NoError(t, api.RDB.Database.Callback().Query().After("gorm:query").Register("test:erasure-window", func(tx *gorm.DB) {
		if stmt := tx.Statement.SQL.String(); strings.Contains(stmt, "NOT EXISTS") {
			windows = append(windows, fmt.Sprint(tx.Statement.Vars...))
		}
	}))
	pass := func() { t.Helper(); require.NoError(t, api.RepairFleetErasure(ctx)) }

	// A previous-minor pod deletes the fourth device: its line is an orphan in the second window.
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Delete(&devs[3]).Error)
	pass()
	require.False(t, redacted(devs[3]), "the first window (devices 1-2) does not reach device 4")
	pass()
	require.True(t, redacted(devs[3]), "the second window (devices 3-4) repairs it")

	// Now an old pod deletes the FIRST device, behind the cursor.
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Delete(&devs[0]).Error)
	pass() // the tail (device 5), then the cursor wraps
	require.False(t, redacted(devs[0]))
	pass()
	require.True(t, redacted(devs[0]), "the wrapped cursor repairs an orphan made behind it")
	require.False(t, redacted(devs[1]), "a live device's line is untouched")
	require.Len(t, windows, 4)
	for _, w := range windows {
		require.Contains(t, w, "acme", "each candidate read is tenant-scoped")
	}
}

// A device whose repair keeps failing does not hold the cursor: the pass reports the failure and
// moves on, so an orphan after it is still repaired, and the failing device is tried again when the
// cursor comes back round to it.
func TestFleetErasureRepairMovesPastADeviceThatKeepsFailing(t *testing.T) {
	api, ctx := fleetFixture(t, 3)
	prev := fleetErasureRepairWindow
	fleetErasureRepairWindow = 1
	t.Cleanup(func() { fleetErasureRepairWindow = prev })
	var devs []Device
	require.NoError(t, api.RDB.DB(ctx).Order("id").Find(&devs).Error)
	op := seedExpiryOperation(t, api, ctx, "erasure-retry", FleetDispatchDispatched, time.Now())
	lines := map[uint]uint{}
	for i, d := range devs[:2] {
		line := FleetOperationLine{OperationId: int64(op.ID), Ordinal: int32(i + 2), Posting: FleetPostingPosted,
			TargetId: sql.NullInt64{Int64: int64(d.ID), Valid: true}, TargetKey: d.Token}
		require.NoError(t, api.RDB.DB(ctx).Create(&line).Error)
		lines[d.ID] = line.ID
	}
	// Both devices are deleted by a previous-minor pod; the first one's repair always fails.
	for _, d := range devs[:2] {
		require.NoError(t, api.RDB.DB(ctx).Unscoped().Delete(&d).Error)
	}
	failing := lines[devs[0].ID]
	// The repair re-reads its device inside the transaction; that read fails for the first device.
	const hook = "test:erasure-fail"
	require.NoError(t, api.RDB.Database.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table != "devices" || !strings.Contains(strings.ToLower(tx.Statement.SQL.String()), "count(") {
			return
		}
		for _, v := range tx.Statement.Vars {
			if fmt.Sprint(v) == fmt.Sprint(devs[0].ID) {
				_ = tx.AddError(fmt.Errorf("repair unavailable"))
			}
		}
	}))
	redacted := func(id uint) bool {
		var l FleetOperationLine
		require.NoError(t, api.RDB.DB(ctx).First(&l, id).Error)
		return l.RedactedAt.Valid
	}

	require.Error(t, api.RepairFleetErasure(ctx), "the failing device's repair is the pass's error")
	require.False(t, redacted(failing))
	require.NoError(t, api.RepairFleetErasure(ctx))
	require.True(t, redacted(lines[devs[1].ID]), "the orphan after a failing device is repaired on the next pass")

	// The failure clears; the cursor comes back round (device 3's window, then the wrap) to it.
	require.NoError(t, api.RDB.Database.Callback().Query().Remove(hook))
	for range 3 {
		_ = api.RepairFleetErasure(ctx)
	}
	require.True(t, redacted(failing), "the failed device is tried again within one cycle")
}

// A window whose orphans fill a whole chunk of candidates is read again before the cursor moves:
// the pass did not reach the rest of them.
func TestFleetErasureRepairRereadsAWindowWhoseCandidatesFilledTheChunk(t *testing.T) {
	api, ctx := fleetFixture(t, 3)
	prevWindow, prevChunk := fleetErasureRepairWindow, fleetErasureRepairChunk
	fleetErasureRepairWindow, fleetErasureRepairChunk = 3, 1
	t.Cleanup(func() { fleetErasureRepairWindow, fleetErasureRepairChunk = prevWindow, prevChunk })
	var devs []Device
	require.NoError(t, api.RDB.DB(ctx).Order("id").Find(&devs).Error)
	op := seedExpiryOperation(t, api, ctx, "erasure-chunk", FleetDispatchDispatched, time.Now())
	var lines []uint
	for i, d := range devs {
		line := FleetOperationLine{OperationId: int64(op.ID), Ordinal: int32(i + 2), Posting: FleetPostingPosted,
			TargetId: sql.NullInt64{Int64: int64(d.ID), Valid: true}, TargetKey: d.Token}
		require.NoError(t, api.RDB.DB(ctx).Create(&line).Error)
		lines = append(lines, line.ID)
	}
	// Two orphans in the one window, a chunk of one: each pass repairs one and stays.
	for _, d := range devs[:2] {
		require.NoError(t, api.RDB.DB(ctx).Unscoped().Delete(&d).Error)
	}
	for range 2 {
		require.NoError(t, api.RepairFleetErasure(ctx))
	}
	var l FleetOperationLine
	require.NoError(t, api.RDB.DB(ctx).First(&l, lines[1]).Error)
	require.True(t, l.RedactedAt.Valid, "the second orphan of a full chunk's window is repaired on the next pass, not a cycle later")
}
