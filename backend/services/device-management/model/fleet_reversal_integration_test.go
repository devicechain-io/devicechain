// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// I-18 on a real server: the device lock, the contra index and the original's fence are what make a
// reversal safe under concurrency, and SQLite has none of them.

// newReversalPG is a configured device in a fresh tenant holding k = "v0", and an original operation
// "orig" that set k to "v1" on it, applied and DISPATCHED.
func newReversalPG(t *testing.T) *reversalEnv {
	t.Helper()
	openTheEngine(t)
	api := NewApi(newPostgresRdbManager(t))
	tenant := fmt.Sprintf("fleetrev%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), tenant), 3*time.Minute)
	t.Cleanup(cancel)
	f := &reversalEnv{api: api, ctx: ctx}
	f.devs = append(f.devs, seedConfiguredDevice(t, api, ctx, []ConfigurationKey{{Key: "k", ValueType: "STRING"}}))
	f.setK(t, f.devs[0], ptr("v0"))
	f.orig = f.runOriginal(t, "orig", false, true)
	// The dispatching budget is instance-wide and the database outlives the test: leave nothing running.
	t.Cleanup(func() {
		var live []FleetOperation
		if err := api.RDB.DB(ctx).Where("dispatch IN ?", []string{"READY", "RUNNING", "PAUSED"}).Find(&live).Error; err != nil {
			t.Errorf("list live operations: %v", err)
			return
		}
		for _, op := range live {
			if _, err := api.AbortFleetOperation(ctx, op.Token, alice.Name); err != nil {
				t.Errorf("abort %s: %v", op.Token, err)
			}
		}
	})
	return f
}

// postUntilDecided posts a line until it leaves PLANNED, retrying a busy device.
func postUntilDecided(t *testing.T, f *reversalEnv, op *FleetOperation) FleetPostResult {
	t.Helper()
	lease := FleetLease{Epoch: op.ExecutionEpoch, Grantee: alice.Name, Deadline: time.Now().Add(2 * time.Minute)}
	var line FleetOperationLine
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("operation_id = ? AND ordinal = 1", op.ID).First(&line).Error)
	for {
		r, err := f.api.PostFleetLine(f.ctx, op.ID, line.ID, lease)
		require.NoError(t, err)
		switch r.Outcome {
		case FleetPostBusy, FleetPostRetry, FleetPostFenceBusy:
			time.Sleep(5 * time.Millisecond)
			continue
		}
		return r
	}
}

// Two reversals of one original line, racing: exactly one contra is applied, and the other is refused
// ALREADY_REVERSED. The device lock serialises the two posts, so the second reads the first's claim.
func TestConcurrentReversalsProduceOneContra(t *testing.T) {
	for round := 0; round < 5; round++ {
		f := newReversalPG(t)
		ops := make([]*FleetOperation, 2)
		for i := range ops {
			rev, err := f.api.ReverseFleetOperation(f.ctx, revReq("orig", fmt.Sprintf("undo%d", i)), "creator")
			require.NoError(t, err)
			ops[i] = f.start(t, rev)
		}
		results := make([]FleetPostResult, len(ops))
		var wg sync.WaitGroup
		for i := range ops {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i] = postUntilDecided(t, f, ops[i])
			}(i)
		}
		wg.Wait()
		posted, refused := 0, 0
		for _, r := range results {
			switch {
			case r.Outcome == FleetPosted:
				posted++
			case r.Outcome == FleetPostRefused && r.Code == FleetLineAlreadyReversed:
				refused++
			default:
				t.Fatalf("round %d: unexpected outcome %+v", round, r)
			}
		}
		require.Equal(t, 1, posted, "exactly one contra")
		require.Equal(t, 1, refused)
		var contras int64
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&FleetOperationLine{}).
			Where("reverses_line_id IS NOT NULL AND posting = 'POSTED'").Count(&contras).Error)
		require.EqualValues(t, 1, contras)
		require.Equal(t, "v0", *f.k(t, f.devs[0]))
	}
}

// M19, database half: the contra index admits any number of unapplied reversal lines of one original
// line (the refused ones are history, not a claim) and at most one applied.
func TestTheContraIndexAdmitsOneAppliedReversalPerOriginalLine(t *testing.T) {
	f := newReversalPG(t)
	var lines []FleetOperationLine
	for i := 0; i < 2; i++ {
		rev, err := f.api.ReverseFleetOperation(f.ctx, revReq("orig", fmt.Sprintf("undo%d", i)), "creator")
		require.NoError(t, err, "a second unapplied reversal line of one original line is admitted")
		var l FleetOperationLine
		require.NoError(t, f.api.RDB.DB(f.ctx).Where("operation_id = ?", rev.ID).First(&l).Error)
		lines = append(lines, l)
	}
	require.Equal(t, lines[0].ReversesLineId, lines[1].ReversesLineId)
	db := f.api.RDB.DB(f.ctx)
	require.NoError(t, db.Model(&FleetOperationLine{}).Where("id = ?", lines[0].ID).Update("posting", "POSTED").Error)
	err := db.Model(&FleetOperationLine{}).Where("id = ?", lines[1].ID).Update("posting", "POSTED").Error
	require.Error(t, err, "a second applied contra violates the index")
	require.Equal(t, "23505", attributeSQLState(err))
}

// M17 on a real server: an interactive write that commits after the poster's unlocked read of the line,
// and before the poster holds the device, is seen by changed-since.
func TestAWriteBeforeTheDeviceLockIsSeenByChangedSince(t *testing.T) {
	f := newReversalPG(t)
	rev, err := f.api.ReverseFleetOperation(f.ctx, revReq("orig", "undo"), "creator")
	require.NoError(t, err)
	rev = f.start(t, rev)
	var once sync.Once
	fleetPostHook = func(stage string, _ *gorm.DB) {
		if stage == "read" {
			once.Do(func() { f.setK(t, f.devs[0], ptr("v2")) })
		}
	}
	t.Cleanup(func() { fleetPostHook = nil })
	r := postUntilDecided(t, f, rev)
	fleetPostHook = nil
	require.Equal(t, FleetPostRefused, r.Outcome)
	require.Equal(t, FleetLineChangedSince, r.Code)
	require.Equal(t, "v2", *f.k(t, f.devs[0]), "the interactive write survives")
}

// M30 under concurrency: retention and a reversal's create race on an original whose retention is due.
// Whichever wins, the original's evidence is never purged under a live reversal: either the reversal
// exists and every line is retained, or the lines are gone and the reversal was refused EVIDENCE_EXPIRED.
func TestRetentionNeverPurgesUnderAReversalItRacesWith(t *testing.T) {
	created, expired := 0, 0
	for round := 0; round < 8; round++ {
		f := newReversalPG(t)
		var now time.Time
		require.NoError(t, f.api.RDB.DB(f.ctx).Raw("SELECT clock_timestamp()").Scan(&now).Error)
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(f.orig).Updates(map[string]any{"evidence": "CLOSED",
			"closed_at": sql.NullTime{Time: now.Add(-fleetClosedRetention - time.Hour), Valid: true}}).Error)
		var wg sync.WaitGroup
		var revErr, retErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, revErr = f.api.ReverseFleetOperation(f.ctx, revReq("orig", "undo"), "creator")
		}()
		go func() {
			defer wg.Done()
			<-start
			retErr = f.api.RetainFleetOperations(f.ctx, now)
		}()
		close(start)
		wg.Wait()
		require.NoError(t, retErr)
		// A second pass after the race settles, so a reversal created after retention's first look is judged too.
		require.NoError(t, f.api.RetainFleetOperations(f.ctx, now))
		var lines int64
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&FleetOperationLine{}).Where("operation_id = ?", f.orig.ID).Count(&lines).Error)
		orig := f.op(t, "orig")
		if revErr == nil {
			created++
			require.EqualValues(t, 1, lines, "round %d: a live reversal's original keeps its lines", round)
			require.False(t, orig.LinesPurgedAt.Valid)
			continue
		}
		var fe *FleetOperationError
		require.True(t, errors.As(revErr, &fe), "round %d: %v", round, revErr)
		require.Equal(t, FleetCodeEvidenceExpired, fe.Code)
		expired++
		require.Zero(t, lines)
	}
	t.Logf("reversal first %d times, retention first %d times", created, expired)
}
