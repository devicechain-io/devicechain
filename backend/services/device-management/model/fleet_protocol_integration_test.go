// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// F1a acceptance: a protocol-2 operation with PLANNED and POSTED lines; run the
// drive pass, an abort and a resume from this build. The line recount by
// (posting, settlement) before == after; the header epoch moves only by pause.
func TestFleetPostgresProtocol2F1aAcceptance(t *testing.T) {
	p := newPostPG(t, "v1")
	db := p.api.RDB.DB(p.ctx)
	r, err := p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[1].ID, p.lease)
	require.NoError(t, err)
	require.Equal(t, FleetPosted, r.Outcome)
	op := insertProtocol2OperationForTest(t, db, *p.op)
	var lines []FleetOperationLine
	require.NoError(t, db.Where("operation_id = ?", p.op.ID).Order("ordinal").Find(&lines).Error)
	for i, line := range lines {
		line.ID, line.OperationId = 0, int64(op.ID)
		require.NoError(t, db.Create(&line).Error)
		lines[i] = line
	}
	type lineCount struct {
		Posting    string
		Settlement sql.NullString
		N          int64
	}
	recount := func() []lineCount {
		var counts []lineCount
		require.NoError(t, db.Model(&FleetOperationLine{}).Select("posting, settlement, count(*) AS n").
			Where("operation_id = ?", op.ID).Group("posting, settlement").Order("posting, settlement").Scan(&counts).Error)
		return counts
	}
	before := recount()
	require.Equal(t, []lineCount{{Posting: FleetPostingPlanned, N: 1}, {Posting: FleetPostingPosted, N: 1}}, before)
	beforeBytes := protocolLinesForTest(t, db, op)
	runtimeBefore, err := p.api.FleetOperationRuntimeOf(p.ctx, op)
	require.NoError(t, err)
	assertUnchanged := func(epoch int64) {
		t.Helper()
		require.Equal(t, before, recount())
		require.Equal(t, beforeBytes, protocolLinesForTest(t, db, op))
		got, err := p.api.FleetOperationByToken(p.ctx, op.Token)
		require.NoError(t, err)
		require.Equal(t, epoch, got.ExecutionEpoch)
	}

	standing := &fakeStanding{answer: map[string]bool{alice.Name: true}}
	p.api.PrincipalStanding = standing
	driven, err := p.api.DriveFleetOperation(p.ctx, op.Token, DefaultFleetCaps)
	require.NoError(t, err)
	require.Equal(t, &FleetDriveResult{}, driven)
	require.Zero(t, standing.asked)
	assertUnchanged(op.ExecutionEpoch)
	runtimeAfter, err := p.api.FleetOperationRuntimeOf(p.ctx, op)
	require.NoError(t, err)
	require.Equal(t, runtimeBefore, runtimeAfter)

	r, err = p.api.PostFleetLine(p.ctx, op.ID, lines[0].ID, p.lease)
	require.NoError(t, err)
	require.Equal(t, FleetPostFenceRefused, r.Outcome)
	assertUnchanged(op.ExecutionEpoch)

	_, err = p.api.AbortFleetOperation(p.ctx, op.Token, alice.Name)
	assertUnchanged(op.ExecutionEpoch)
	requireFleetCode(t, err, "OPERATION_NEWER_THAN_SERVER")

	paused, err := p.api.PauseFleetOperation(p.ctx, op.Token, alice.Name)
	require.NoError(t, err)
	require.Equal(t, FleetDispatchPaused, paused.Dispatch)
	assertUnchanged(op.ExecutionEpoch + 1)

	_, err = p.api.ResumeFleetOperation(p.ctx, op.Token, op.ContractDigest, nil, alice)
	assertUnchanged(op.ExecutionEpoch + 1)
	requireFleetCode(t, err, "OPERATION_NEWER_THAN_SERVER")
	require.Equal(t, []string{fleetEventPause}, events(t, p.api, p.ctx, op))
}
