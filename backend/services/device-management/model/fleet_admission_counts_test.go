// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// retainedLinesOracleForTest is the retained-lines count admission made before it read the
// headers: every line in scope, plus the target count PLANNING headers have reserved.
func retainedLinesOracleForTest(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var lines int64
	require.NoError(t, db.Model(&FleetOperationLine{}).Count(&lines).Error)
	var planning struct{ N sql.NullInt64 }
	require.NoError(t, db.Model(&FleetOperation{}).Select("COALESCE(SUM(target_count), 0) AS n").Where("dispatch = ?", FleetDispatchPlanning).Scan(&planning).Error)
	return lines + planning.N.Int64
}

// The headers answer the retained-lines budget exactly, through every state an operation's lines
// pass through: reserved while PLANNING, written by create step two, never written when planning
// is abandoned, deleted a chunk at a time by retention, and gone once the purge is stamped.
func TestFleetRetainedLinesFromHeadersMatchesTheLinesThroughTheirLifecycle(t *testing.T) {
	openTheEngine(t)
	api, ctx := fleetFixture(t, 3)
	db := func(c context.Context) *gorm.DB { return api.RDB.DB(c) }
	system := core.WithSystemContext(ctx)
	check := func(stage string) {
		t.Helper()
		for _, c := range []context.Context{ctx, system} {
			got, err := fleetRetainedLines(db(c))
			require.NoError(t, err)
			require.Equal(t, retainedLinesOracleForTest(t, db(c)), got, stage)
		}
	}
	check("empty")

	// Create: step two writes the lines and leaves READY.
	_, err := api.CreateFleetOperation(ctx, setReq("ready", "d1", "d2", "d3"), alice.Name)
	require.NoError(t, err)
	check("ready")

	// A PLANNING header reserves its target count; abandoned and aborted, it holds nothing.
	now := time.Now()
	planning := &FleetOperation{Token: "planning", Kind: FleetKindConfigSet, Name: "p", Dispatch: FleetDispatchPlanning,
		RequestDigest: "sha256:x", DriverProtocol: 1, Intent: "{}", TargetKind: "DEVICE_LIST", TargetCount: 7,
		MaxPostsPerSecond: 10, EffPostsPerSecond: 10, SettleDeadlineS: 60, Initiator: "u", CreatedAt: now.Add(-time.Hour)}
	require.NoError(t, db(ctx).Create(planning).Error)
	check("planning")
	require.NoError(t, db(ctx).Model(planning).Update("dispatch", FleetDispatchAborted).Error)
	check("planning abandoned")

	// Retention deletes a chunk of a large operation's lines, then the rest, then stamps it.
	big := &FleetOperation{Token: "big", Kind: FleetKindConfigSet, Name: "b", Dispatch: FleetDispatchAborted,
		RequestDigest: "sha256:x", PlanDigest: "sha256:p", DriverProtocol: 1, Intent: "{}", TargetKind: "DEVICE_LIST",
		TargetCount: fleetMaintenanceChunk + 5, MaxPostsPerSecond: 10, EffPostsPerSecond: 10, SettleDeadlineS: 60, Initiator: "u",
		CreatedAt: now.Add(-time.Hour)}
	require.NoError(t, db(ctx).Create(big).Error)
	lines := make([]FleetOperationLine, big.TargetCount)
	for i := range lines {
		lines[i] = FleetOperationLine{OperationId: int64(big.ID), Ordinal: int32(i + 1), Posting: FleetPostingCancelled}
	}
	require.NoError(t, db(ctx).CreateInBatches(&lines, 500).Error)
	check("big")
	require.NoError(t, api.RetainFleetOperations(ctx, now))
	var got FleetOperation
	require.NoError(t, db(ctx).First(&got, big.ID).Error)
	require.Equal(t, int32(fleetMaintenanceChunk), got.PurgedLineCount, "the chunk's delete is counted in its own transaction")
	require.False(t, got.LinesPurgedAt.Valid)
	check("big, one chunk purged")
	require.NoError(t, api.RetainFleetOperations(ctx, now))
	check("big, purged")
	require.NoError(t, db(ctx).First(&got, big.ID).Error)
	require.True(t, got.LinesPurgedAt.Valid)

	// A previous-release pod purges without counting what it deleted: while the purge runs the
	// header overstates what is held (never understates it), and once it is stamped it holds nothing.
	require.NoError(t, db(ctx).Model(&got).UpdateColumn("purged_line_count", 0).Error)
	check("big, purged by a previous-release pod")
	old := &FleetOperation{Token: "old", Kind: FleetKindConfigSet, Name: "o", Dispatch: FleetDispatchAborted,
		RequestDigest: "sha256:x", PlanDigest: "sha256:p", DriverProtocol: 1, Intent: "{}", TargetKind: "DEVICE_LIST",
		TargetCount: 4, MaxPostsPerSecond: 10, EffPostsPerSecond: 10, SettleDeadlineS: 60, Initiator: "u", CreatedAt: now.Add(-time.Hour)}
	require.NoError(t, db(ctx).Create(old).Error)
	for i := 1; i <= 4; i++ {
		require.NoError(t, db(ctx).Create(&FleetOperationLine{OperationId: int64(old.ID), Ordinal: int32(i), Posting: FleetPostingCancelled}).Error)
	}
	require.NoError(t, db(ctx).Where("operation_id = ? AND ordinal <= 2", old.ID).Delete(&FleetOperationLine{}).Error)
	held, err := fleetRetainedLines(db(ctx))
	require.NoError(t, err)
	require.Equal(t, retainedLinesOracleForTest(t, db(ctx))+2, held, "an uncounted purge overstates by what it deleted")
}

// The evidence-open count is two disjoint counts now; their sum is the one OR'd count it replaced.
func TestFleetEvidenceOpenCountIsTheCountItReplaced(t *testing.T) {
	api, ctx := fleetFixture(t, 0)
	op := seedExpiryOperation(t, api, ctx, "open", FleetDispatchPaused, time.Now())
	other := seedExpiryOperation(t, api, ctx, "other", FleetDispatchPaused, time.Now())
	for i, shape := range []map[string]any{
		{"posting": "POSTED"},
		{"posting": "POSTED", "settlement": "AMBIGUOUS", "code": "NO_EVIDENCE"},
		{"posting": "POSTED", "settlement": "AMBIGUOUS", "code": "TARGET_ERASED"},
		{"posting": "POSTED", "settlement": "CONFIRMED"},
		{"posting": "PLANNED"},
		{"posting": "CANCELLED"},
	} {
		for _, o := range []*FleetOperation{op, other} {
			line := FleetOperationLine{OperationId: int64(o.ID), Ordinal: int32(10 + i), Posting: shape["posting"].(string)}
			if s, ok := shape["settlement"]; ok {
				line.Settlement = sql.NullString{String: s.(string), Valid: true}
			}
			if c, ok := shape["code"]; ok {
				line.Code = c.(string)
			}
			require.NoError(t, api.RDB.DB(ctx).Create(&line).Error)
		}
	}
	for _, c := range []context.Context{ctx, core.WithSystemContext(ctx)} {
		var want int64
		require.NoError(t, api.RDB.DB(c).Model(&FleetOperationLine{}).Where(`((posting = 'POSTED' AND (settlement IS NULL OR settlement = 'AMBIGUOUS') AND code <> 'TARGET_ERASED')
		OR (operation_id = ? AND posting = 'PLANNED'))`, op.ID).Count(&want).Error)
		got, err := fleetEvidenceOpenCount(api.RDB.DB(c), op)
		require.NoError(t, err)
		require.Equal(t, want, got)
		require.Equal(t, int64(4+2), got, "two open lines in each operation, and the candidate's two plans (seeded and shaped)")
	}
}
