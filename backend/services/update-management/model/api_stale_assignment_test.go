// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pausedAssignmentLookup captures the first answer before waiting, as a device-management
// response already in flight would. Subsequent lookups see the replacement immediately.
type pausedAssignmentLookup struct {
	mu      sync.Mutex
	facts   DeviceFacts
	found   bool
	first   bool
	started chan struct{}
	release chan struct{}
}

func (f *pausedAssignmentLookup) DeviceOf(ctx context.Context, _, _ string) (DeviceFacts, bool, error) {
	f.mu.Lock()
	facts, found, first := f.facts, f.found, f.first
	f.first = false
	f.mu.Unlock()
	if first {
		close(f.started)
		select {
		case <-f.release:
		case <-ctx.Done():
			return DeviceFacts{}, false, ctx.Err()
		}
	}
	return facts, found, nil
}

// exerciseStaleAssignment is shared with the PostgreSQL integration test so both databases
// exercise the public write and device sync paths with exactly the same interleaving.
func exerciseStaleAssignment(t *testing.T, api *Api, parent context.Context, device, artifact string,
	oldWrite func(context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	f := &pausedAssignmentLookup{facts: DeviceFacts{Incarnation: 17, CreatedAt: created, Profile: "gateway"},
		found: true, first: true, started: make(chan struct{}), release: make(chan struct{})}
	api.Devices = f
	release := sync.OnceFunc(func() { close(f.release) })
	defer release()
	done := make(chan error, 1)
	go func() { done <- oldWrite(ctx) }()
	select {
	case <-f.started:
	case <-ctx.Done():
		t.Fatal("old writer did not reach device lookup")
	}
	f.mu.Lock()
	f.found = false // delete the original device
	f.facts = DeviceFacts{Incarnation: 23, CreatedAt: created.Add(time.Hour), Profile: "gateway"}
	f.found = true // recreate the token with a new, never-reused row id
	f.mu.Unlock()
	replacement, err := api.AssignArtifact(ctx, device, artifact)
	require.NoError(t, err)
	require.EqualValues(t, 23, replacement.Assignment.DeviceIncarnation.Int64)
	before, err := api.AssignmentByToken(ctx, replacement.Assignment.Token)
	require.NoError(t, err)
	release()
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatal("old writer did not finish")
	}
	after, readErr := api.AssignmentByToken(ctx, replacement.Assignment.Token)
	require.NoError(t, readErr)
	assert.Equal(t, before, after, "stale writer must change neither replacement assignment nor attempt")
	assert.False(t, after.Assignment.SupersededAt.Valid, "replacement must remain current")
	views, readErr := api.CurrentUpdates(ctx, device)
	require.NoError(t, readErr)
	if assert.Len(t, views, 1, "replacement must still be served by device sync") {
		assert.Equal(t, replacement.Assignment.Token, views[0].Assignment.Token)
	}
	var oldRows, allRows int64
	require.NoError(t, api.RDB.DB(ctx).Model(&UpdateAssignment{}).
		Where("device_token = ? AND device_incarnation = ?", device, 17).Count(&oldRows).Error)
	assert.Zero(t, oldRows, "stale incarnation must never be inserted")
	require.NoError(t, api.RDB.DB(ctx).Model(&UpdateAssignment{}).Where("device_token = ?", device).Count(&allRows).Error)
	assert.EqualValues(t, 1, allRows, "only the replacement assignment may exist")
	return err
}

func TestAssignArtifactRefusesPausedOlderIncarnation(t *testing.T) {
	e := newUpdateEnv(t)
	e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware"))
	err := exerciseStaleAssignment(t, e.api, tenantCtx("acme"), "dev-1", "fw-1", func(ctx context.Context) error {
		_, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
		return err
	})
	requireRefusal(t, err, "DEVICE_REPLACED")
	var refusal *RefusalError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, "DEVICE_REPLACED", refusal.Extensions()["code"])
}

func TestAssignArtifactRefusesOlderIncarnationFromHistoryWithEmptySlot(t *testing.T) {
	for _, component := range []string{"firmware", "another-component"} {
		t.Run(component, func(t *testing.T) {
			e := newUpdateEnv(t)
			ctx, cancel := context.WithTimeout(tenantCtx("acme"), 30*time.Second)
			defer cancel()
			e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware"))
			f := &pausedAssignmentLookup{facts: DeviceFacts{Incarnation: 17, Profile: "gateway"},
				found: true, first: true, started: make(chan struct{}), release: make(chan struct{})}
			e.api.Devices = f
			release := sync.OnceFunc(func() { close(f.release) })
			defer release()
			done := make(chan error, 1)
			go func() { _, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1"); done <- err }()
			select {
			case <-f.started:
			case <-ctx.Done():
				t.Fatal("stale writer never reached lookup")
			}
			f.mu.Lock()
			f.facts.Incarnation = 23
			f.mu.Unlock()
			v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
			require.NoError(t, err)
			// A terminal, superseded row in ANY component must retain the replacement fence.
			db := e.api.RDB.DB(ctx)
			require.NoError(t, db.Model(&UpdateAttempt{}).Where("id = ?", v.Attempt.ID).
				Update("state", string(ota.StateUpdated)).Error)
			require.NoError(t, db.Model(&UpdateAssignment{}).Where("id = ?", v.Assignment.ID).
				Updates(map[string]any{"superseded_at": e.clock, "component": component}).Error)
			var before []UpdateAssignment
			var attemptsBefore []UpdateAttempt
			require.NoError(t, db.Find(&before).Error)
			require.NoError(t, db.Find(&attemptsBefore).Error)
			release()
			select {
			case err = <-done:
			case <-ctx.Done():
				t.Fatal("stale writer did not finish")
			}
			requireRefusal(t, err, CodeDeviceReplaced)
			var after []UpdateAssignment
			var attemptsAfter []UpdateAttempt
			require.NoError(t, db.Find(&after).Error)
			require.NoError(t, db.Find(&attemptsAfter).Error)
			require.Equal(t, before, after, "nothing may be inserted or superseded")
			require.Equal(t, attemptsBefore, attemptsAfter)
		})
	}
}

func TestRolloutSkipsPausedOlderIncarnation(t *testing.T) {
	e := newRolloutEnv(t, 1)
	e.create(t, "r1", 1, nil)
	err := exerciseStaleAssignment(t, e.api, tenantCtx("acme"), "dev-0", "fw-2", func(ctx context.Context) error {
		_, err := e.api.StartRollout(ctx, "r1")
		return err
	})
	require.NoError(t, err, "assignment refusals must become target skips")
	targets := e.targets(t, "r1", nil)
	require.Len(t, targets, 1)
	require.Equal(t, TargetSkipped, targets[0].Target.Status)
	require.Equal(t, "DEVICE_REPLACED", targets[0].Target.SkipCode)
}

func TestAssignArtifactHoldsUnknownIncarnation(t *testing.T) {
	for _, current := range []string{"none", "stamped", "legacy"} {
		t.Run(current, func(t *testing.T) {
			e := newUpdateEnv(t)
			ctx := tenantCtx("acme")
			e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware"))
			if current != "none" {
				v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
				require.NoError(t, err)
				if current == "legacy" {
					require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAssignment{}).Where("id = ?", v.Assignment.ID).
						Update("device_incarnation", nil).Error)
				}
			}
			var before []UpdateAssignment
			var attemptsBefore []UpdateAttempt
			require.NoError(t, e.api.RDB.DB(ctx).Find(&before).Error)
			require.NoError(t, e.api.RDB.DB(ctx).Find(&attemptsBefore).Error)
			e.api.Devices = deviceLookupFunc(func(context.Context, string, string) (DeviceFacts, bool, error) {
				return DeviceFacts{CreatedAt: e.clock.Add(-time.Hour), Profile: "gateway"}, true, nil
			})
			_, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
			requireRefusal(t, err, CodeDeviceLookupUnavailable)
			var after []UpdateAssignment
			var attemptsAfter []UpdateAttempt
			require.NoError(t, e.api.RDB.DB(ctx).Find(&after).Error)
			require.NoError(t, e.api.RDB.DB(ctx).Find(&attemptsAfter).Error)
			require.Equal(t, before, after)
			require.Equal(t, attemptsBefore, attemptsAfter)
		})
	}
}

func TestAssignArtifactAllowsSameIncarnationAndLegacyTerminalReplacement(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "stamped"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			e := newUpdateEnv(t)
			ctx := tenantCtx("acme")
			e.devices.incarnations = map[string]uint64{"acme/dev-1": 23}
			e.devices.createdAt = map[string]time.Time{"acme/dev-1": e.clock.Add(-time.Hour)}
			e.availableArtifact(t, "acme", "fw-1", "1.0.0", []byte("firmware one"))
			e.availableArtifact(t, "acme", "fw-2", "2.0.0", []byte("firmware two"))
			v, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
			require.NoError(t, err)
			if legacy {
				require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAssignment{}).Where("id = ?", v.Assignment.ID).
					Update("device_incarnation", nil).Error)
			}
			again, err := e.api.AssignArtifact(ctx, "dev-1", "fw-1")
			require.NoError(t, err)
			require.Equal(t, v.Assignment.ID, again.Assignment.ID)
			_, err = e.api.AssignArtifact(ctx, "dev-1", "fw-2")
			requireRefusal(t, err, CodeUpdateInProgress)
			require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAttempt{}).Where("assignment_id = ?", v.Assignment.ID).
				Update("state", string(ota.StateCancelled)).Error)
			next, err := e.api.AssignArtifact(ctx, "dev-1", "fw-2")
			require.NoError(t, err)
			require.NotEqual(t, v.Assignment.ID, next.Assignment.ID)
			require.EqualValues(t, 23, next.Assignment.DeviceIncarnation.Int64)
		})
	}
}
