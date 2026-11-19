// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAdvanceRolloutsPrunesCursorsEndedByAnotherReplica(t *testing.T) {
	e := newRolloutEnv(t, 1)
	ctx := tenantCtx("acme")
	db := e.api.RDB.DB(ctx)
	states := []string{RolloutRunning, RolloutRunning, RolloutCompleted, RolloutAborted, RolloutPaused}
	var ids []uint
	for i, state := range states {
		token := fmt.Sprintf("r%d", i)
		e.create(t, token, 1, nil)
		id := e.rollout(t, token).Rollout.ID
		ids = append(ids, id)
		require.NoError(t, db.Model(&UpdateRollout{}).Where("id = ?", id).
			Updates(map[string]any{"state": state, "target_count": 1}).Error)
		// Unknown legacy identity holds both RUNNING rollouts without assignments.
		require.NoError(t, db.Create(&UpdateRolloutTarget{RolloutId: id, DeviceToken: "dev-0", Status: TargetPending}).Error)
		e.api.rolloutTargetAfter.Store(id, rolloutCursor{tenant: "acme", after: 1})
	}
	missing := ids[len(ids)-1] + 100
	e.api.rolloutTargetAfter.Store(missing, rolloutCursor{tenant: "acme", after: 1})
	e.api.rolloutsPerPass = 1 // only one of the two RUNNING rollouts is listed
	res, err := e.api.AdvanceRollouts(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, res.Stepped)
	for i, id := range ids {
		_, exists := e.api.rolloutTargetAfter.Load(id)
		require.Equal(t, states[i] == RolloutRunning, exists, "state %s", states[i])
	}
	_, exists := e.api.rolloutTargetAfter.Load(missing)
	require.False(t, exists, "a deleted rollout also has no cursor to retain")
	require.Zero(t, e.assignmentCount(t, "dev-0"))
	// Another replica ends the remaining rollouts; even an empty pass must clean up.
	require.NoError(t, db.Model(&UpdateRollout{}).Where("state = ?", RolloutRunning).Update("state", RolloutCompleted).Error)
	_, err = e.api.AdvanceRollouts(context.Background())
	require.NoError(t, err)
	e.api.rolloutTargetAfter.Range(func(key, _ any) bool {
		t.Errorf("stale cursor %v survived an empty pass", key)
		return true
	})
}

func TestRolloutCursorPruneHoldsOnUnknownState(t *testing.T) {
	e := newRolloutEnv(t, 1)
	e.create(t, "r1", 1, nil)
	id := e.rollout(t, "r1").Rollout.ID
	e.api.rolloutTargetAfter.Store(id, rolloutCursor{tenant: "acme", after: 7})
	db := e.api.RDB.Database
	failure := errors.New("rollout states unavailable")
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("fail_cursor_query", func(tx *gorm.DB) {
		tx.AddError(failure)
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove("fail_cursor_query") })
	require.ErrorIs(t, e.api.pruneRolloutTargetAfter(context.Background()), failure)
	cursor, exists := e.api.rolloutTargetAfter.Load(id)
	require.True(t, exists)
	require.Equal(t, rolloutCursor{tenant: "acme", after: 7}, cursor)
}

// Pruning asks about each cursor's rollout under that cursor's own tenant, never across tenants:
// a rollout id stored under another tenant is invisible there and its cursor is dropped.
func TestRolloutCursorPruneIsTenantScoped(t *testing.T) {
	e := newRolloutEnv(t, 1)
	db := e.api.RDB.DB(tenantCtx("acme"))
	e.create(t, "r1", 1, nil)
	e.create(t, "r2", 1, nil)
	own, foreign := e.rollout(t, "r1").Rollout.ID, e.rollout(t, "r2").Rollout.ID
	require.NoError(t, db.Model(&UpdateRollout{}).Where("id IN ?", []uint{own, foreign}).
		Update("state", RolloutRunning).Error)
	e.api.rolloutTargetAfter.Store(own, rolloutCursor{tenant: "acme", after: 3})
	e.api.rolloutTargetAfter.Store(foreign, rolloutCursor{tenant: "globex", after: 3})
	require.NoError(t, e.api.pruneRolloutTargetAfter(context.Background()))
	_, kept := e.api.rolloutTargetAfter.Load(own)
	require.True(t, kept, "a RUNNING rollout's cursor is kept under its own tenant")
	_, leaked := e.api.rolloutTargetAfter.Load(foreign)
	require.False(t, leaked, "another tenant's rollout must not be visible to the prune")
}
