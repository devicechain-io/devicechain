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

// A window the pass did not finish is read again: a device whose repair fails keeps the cursor
// where it was, so the orphan is not skipped for a whole cycle.
func TestFleetErasureRepairRereadsAWindowItCouldNotFinish(t *testing.T) {
	api, ctx := fleetFixture(t, 3)
	prev := fleetErasureRepairWindow
	fleetErasureRepairWindow = 1
	t.Cleanup(func() { fleetErasureRepairWindow = prev })
	var devs []Device
	require.NoError(t, api.RDB.DB(ctx).Order("id").Find(&devs).Error)
	op := seedExpiryOperation(t, api, ctx, "erasure-retry", FleetDispatchDispatched, time.Now())
	line := FleetOperationLine{OperationId: int64(op.ID), Ordinal: 2, Posting: FleetPostingPosted,
		TargetId: sql.NullInt64{Int64: int64(devs[0].ID), Valid: true}, TargetKey: devs[0].Token}
	require.NoError(t, api.RDB.DB(ctx).Create(&line).Error)
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Delete(&devs[0]).Error)

	const hook = "test:erasure-fail"
	require.NoError(t, api.RDB.Database.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "fleet_operation_lines" {
			_ = tx.AddError(fmt.Errorf("redaction unavailable"))
		}
	}))
	require.Error(t, api.RepairFleetErasure(ctx))
	require.NoError(t, api.RDB.Database.Callback().Update().Remove(hook))
	require.NoError(t, api.RepairFleetErasure(ctx))
	var got FleetOperationLine
	require.NoError(t, api.RDB.DB(ctx).First(&got, line.ID).Error)
	require.True(t, got.RedactedAt.Valid, "the failed window is read again on the next pass, not after a cycle")
}
