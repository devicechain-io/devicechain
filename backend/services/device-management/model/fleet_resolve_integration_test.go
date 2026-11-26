// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// I-9, I-12: actual resolve, CLOSED evidence, then a deciding APPLIED report.
// The verdict and note survive every reconciler/deadline pass; the unique
// partial index admits exactly one contradiction event.
func TestFleetResolveI9I12OnPostgres(t *testing.T) {
	p := postedSettlementPG(t)
	db := p.api.RDB.DB(p.ctx)
	var now time.Time
	require.NoError(t, db.Raw("SELECT clock_timestamp()").Scan(&now).Error)
	p.ctx = resolveContext(p.ctx, now)
	require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id = ?", p.op.ID).
		Updates(map[string]any{"settlement": "AMBIGUOUS", "code": "NO_EVIDENCE"}).Error)
	_, err := p.api.ResolveAmbiguousFleetLines(resolveContext(p.ctx, now.Add(-6*time.Minute)), resolveRequest(p.op.Token))
	requireFleetCode(t, err, FleetCodeStepUpRequired)
	missingNote := resolveRequest(p.op.Token)
	missingNote.Note = ""
	_, err = p.api.ResolveAmbiguousFleetLines(p.ctx, missingNote)
	requireFleetCode(t, err, FleetCodeNoteRequired)
	require.NoError(t, db.Model(p.op).Update("driver_protocol", 2).Error)
	_, err = p.api.ResolveAmbiguousFleetLines(p.ctx, resolveRequest(p.op.Token))
	requireFleetCode(t, err, FleetCodeNewerThanServer)
	require.NoError(t, db.Model(p.op).Update("driver_protocol", 1).Error)
	var rejectedEvents int64
	require.NoError(t, db.Model(&FleetOperationEvent{}).Where("operation_id = ? AND kind IN ('RESOLUTION','VERDICT')", p.op.ID).Count(&rejectedEvents).Error)
	require.Zero(t, rejectedEvents)
	// A deliberately absurd process clock must affect neither freshness nor stored times.
	prev := fleetClock
	fleetClock = func() time.Time { return now.Add(24 * time.Hour) }
	t.Cleanup(func() { fleetClock = prev })
	op, err := p.api.ResolveAmbiguousFleetLines(p.ctx, resolveRequest(p.op.Token))
	require.NoError(t, err)
	require.Equal(t, "CLOSED", op.Evidence)
	var resolved []FleetOperationLine
	require.NoError(t, db.Where("operation_id = ?", p.op.ID).Order("ordinal").Find(&resolved).Error)
	require.Equal(t, resolved[0].ResolutionId, resolved[1].ResolutionId)
	require.WithinDuration(t, now, resolved[0].ResolvedAt.Time, 10*time.Second)
	var resolution FleetOperationEvent
	require.NoError(t, db.First(&resolution, resolved[0].ResolutionId.Int64).Error)
	require.Equal(t, "RESOLUTION", resolution.Kind)
	require.Equal(t, "operator investigated", resolution.Note.String)
	require.WithinDuration(t, now, resolution.At, 10*time.Second)
	for i, line := range resolved {
		var rev DeviceConfigurationRevision
		require.NoError(t, db.Where("device_id = ? AND revision = ?", line.TargetId.Int64, line.PostedRevision.Int64).First(&rev).Error)
		require.NoError(t, p.api.ApplyDeviceConfigurationReport(p.ctx, p.devs[i].Token, rev.Revision, rev.Digest, ConfigurationApplied, nil))
	}
	for pass := 0; pass < 5; pass++ {
		var wg sync.WaitGroup
		errs := make([]error, 3)
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if i == 2 {
					_, errs[i] = p.api.DeadlineFleetLines(p.ctx)
				} else {
					_, errs[i] = p.api.ReconcileFleetLines(p.ctx, false)
				}
			}(i)
		}
		wg.Wait()
		for _, err := range errs {
			require.NoError(t, err)
		}
	}
	for _, before := range resolved {
		var after FleetOperationLine
		require.NoError(t, db.First(&after, before.ID).Error)
		require.Equal(t, before.Settlement, after.Settlement)
		require.Equal(t, before.ResolvedAt, after.ResolvedAt)
		require.Equal(t, before.ResolutionId, after.ResolutionId)
		require.Equal(t, "EVIDENCE_CONTRADICTS_VERDICT", after.EvidenceFlag)
		var n int64
		require.NoError(t, db.Model(&FleetOperationEvent{}).Where("line_id = ? AND kind = 'CONTRADICTING_EVIDENCE'", before.ID).Count(&n).Error)
		require.EqualValues(t, 1, n)
	}
}

// The ordered locking read waits behind uncommitted evidence. PostgreSQL must
// re-evaluate its AMBIGUOUS candidate predicate after the evidence commits;
// resolution must neither overwrite it nor insert a per-line verdict event.
func TestFleetResolveLosesToEvidenceCASOnPostgres(t *testing.T) {
	p := postedSettlementPG(t)
	db := p.api.RDB.DB(p.ctx)
	line := &p.lines[0]
	require.NoError(t, db.Model(line).Update("settlement", "AMBIGUOUS").Error)
	var now time.Time
	require.NoError(t, db.Raw("SELECT clock_timestamp()").Scan(&now).Error)
	p.ctx = resolveContext(p.ctx, now)
	held := db.Begin()
	require.NoError(t, held.Error)
	defer held.Rollback()
	require.NoError(t, held.Model(line).Updates(map[string]any{"settlement": "CONFIRMED", "evidence_revision": line.PostedRevision}).Error)
	var holder int
	require.NoError(t, held.Raw("SELECT pg_backend_pid()").Scan(&holder).Error)
	done := make(chan error, 1)
	go func() {
		req := resolveRequest(p.op.Token)
		req.AllAmbiguous, req.Ordinals = false, []int32{line.Ordinal}
		_, err := p.api.ResolveAmbiguousFleetLines(p.ctx, req)
		done <- err
	}()
	require.Eventually(t, func() bool {
		var n int64
		err := db.Raw("SELECT count(*) FROM pg_locks WHERE NOT granted AND ? = ANY(pg_blocking_pids(pid))", holder).Scan(&n).Error
		return err == nil && n > 0
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, held.Commit().Error)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-p.ctx.Done():
		t.Fatal(p.ctx.Err())
	}
	var after FleetOperationLine
	require.NoError(t, db.First(&after, line.ID).Error)
	require.Equal(t, "CONFIRMED", after.Settlement.String)
	require.False(t, after.ResolutionId.Valid)
	var n int64
	require.NoError(t, db.Model(&FleetOperationEvent{}).Where("line_id = ? AND kind = 'VERDICT'", line.ID).Count(&n).Error)
	require.Zero(t, n)
}

func TestFleetResolveConcurrentOperatorsOnPostgres(t *testing.T) {
	p := postedSettlementPG(t)
	db := p.api.RDB.DB(p.ctx)
	require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id = ?", p.op.ID).Update("settlement", "AMBIGUOUS").Error)
	var now time.Time
	require.NoError(t, db.Raw("SELECT clock_timestamp()").Scan(&now).Error)
	p.ctx = resolveContext(p.ctx, now)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := resolveRequest(p.op.Token)
			if i == 1 {
				req.Verdict = "CONFIRMED"
			}
			// Independent API/transaction on the pool, as on separate replicas.
			_, errs[i] = NewApi(&rdb.RdbManager{Database: db.Session(&gorm.Session{NewDB: true})}).ResolveAmbiguousFleetLines(p.ctx, req)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	var n int64
	require.NoError(t, db.Model(&FleetOperationEvent{}).Where("operation_id = ? AND kind = 'VERDICT'", p.op.ID).Count(&n).Error)
	require.EqualValues(t, len(p.lines), n)
}
