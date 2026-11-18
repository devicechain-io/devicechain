// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func incarnationCommand(t *testing.T, api *Api, ctx context.Context, token string, id uint64, at time.Time) *Command {
	t.Helper()
	c := deletionCommand(t, api, ctx, token, CommandQueued, at)
	c.DeviceIncarnation = sql.NullInt64{Int64: int64(id), Valid: id > 0}
	require.NoError(t, api.RDB.DB(ctx).Model(c).Update("device_incarnation", c.DeviceIncarnation).Error)
	return c
}

func TestDeletionIncarnationOverridesClockSkew(t *testing.T) {
	api := newTestApi(t)
	ctx := gateCtx("A")
	at := time.Now().UTC().Truncate(time.Second)
	newer := incarnationCommand(t, api, ctx, "new-device", 23, at.Add(-time.Hour))
	old := incarnationCommand(t, api, ctx, "old-device", 17, at.Add(time.Hour))
	legacy := incarnationCommand(t, api, ctx, "legacy", 0, at.Add(-time.Hour))
	futureLegacy := incarnationCommand(t, api, ctx, "future-legacy", 0, at.Add(time.Hour))
	n, err := api.CancelDeletedDeviceCommands(ctx, "valve-7", 17, at)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	for _, c := range []*Command{old, legacy} {
		got := loadOrFail(t, api, ctx, c.ID)
		assert.Equal(t, CommandCancelled.String(), got.Status)
		assert.Equal(t, OutcomeNotDelivered.String(), got.Outcome.String)
	}
	assert.Equal(t, CommandQueued.String(), loadOrFail(t, api, ctx, futureLegacy.ID).Status)
	_, claimed, err := api.MarkSent(ctx, newer.ID)
	require.NoError(t, err)
	assert.True(t, claimed, "a larger incarnation must survive a skewed creation clock")
}

func TestEveryClaimUsesTheDeletionIncarnation(t *testing.T) {
	for _, path := range []string{"MarkSent", "MarkSentByToken", "claimForPull"} {
		t.Run(path, func(t *testing.T) {
			api := newTestApi(t)
			ctx := gateCtx("A")
			at := time.Now().UTC().Truncate(time.Second)
			_, err := api.CancelDeletedDeviceCommands(ctx, "valve-7", 17, at)
			require.NoError(t, err)
			// Both rows arrive after the fact. Their clocks point the wrong way.
			old := incarnationCommand(t, api, ctx, "late-old", 17, at.Add(time.Hour))
			newer := incarnationCommand(t, api, ctx, "skewed-new", 23, at.Add(-time.Hour))
			claim := func(c *Command) bool {
				t.Helper()
				if path == "claimForPull" {
					got, err := api.claimForPull(ctx, c, c.DeviceToken)
					require.NoError(t, err)
					return got != nil
				}
				var ok bool
				var err error
				if path == "MarkSent" {
					_, ok, err = api.MarkSent(ctx, c.ID)
				} else {
					_, ok, err = api.MarkSentByToken(ctx, c.Token)
				}
				require.NoError(t, err)
				return ok
			}
			assert.False(t, claim(old))
			assert.True(t, claim(newer))
		})
	}
}

func TestUnknownDeletionIncarnationUsesTimeAndKnownFloorNeverDecreases(t *testing.T) {
	api := newTestApi(t)
	ctx := gateCtx("A")
	at := time.Now().UTC().Truncate(time.Second)
	before := incarnationCommand(t, api, ctx, "before", 23, at.Add(-time.Hour))
	after := incarnationCommand(t, api, ctx, "after", 17, at.Add(time.Hour))
	n, err := api.CancelDeletedDeviceCommands(ctx, "valve-7", 0, at)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	assert.Equal(t, CommandCancelled.String(), loadOrFail(t, api, ctx, before.ID).Status)
	_, ok, err := api.MarkSent(ctx, after.ID)
	require.NoError(t, err)
	assert.True(t, ok)
	// The persistent predicate also uses time when only the command has an identity.
	late := incarnationCommand(t, api, ctx, "late-before", 99, at.Add(-time.Hour))
	_, ok, err = api.MarkSent(ctx, late.ID)
	require.NoError(t, err)
	assert.False(t, ok)
	for _, id := range []uint64{23, 17, 0} {
		_, err = api.CancelDeletedDeviceCommands(ctx, "valve-7", id, at)
		require.NoError(t, err)
	}
	var gate DeviceGate
	require.NoError(t, api.RDB.DB(ctx).First(&gate).Error)
	assert.Equal(t, sql.NullInt64{Int64: 23, Valid: true}, gate.DeletedIncarnation)
}

func TestRetireCoveredCommandsIsBoundedAndTenantScoped(t *testing.T) {
	api := newTestApi(t)
	ctx, other := gateCtx("A"), gateCtx("B")
	at := time.Now().UTC().Truncate(time.Second)
	for _, tenant := range []context.Context{ctx, other} {
		_, err := api.CancelDeletedDeviceCommands(tenant, "valve-7", 17, at)
		require.NoError(t, err)
	}
	first := incarnationCommand(t, api, ctx, "covered-1", 17, at.Add(time.Hour))
	second := incarnationCommand(t, api, ctx, "covered-2", 16, at.Add(time.Hour))
	fresh := incarnationCommand(t, api, ctx, "uncovered", 23, at.Add(-time.Hour))
	foreign := incarnationCommand(t, api, other, "foreign", 17, at.Add(time.Hour))
	n, err := api.RetireCoveredCommands(ctx, 0)
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = api.RetireCoveredCommands(ctx, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	got := loadOrFail(t, api, ctx, first.ID)
	assert.Equal(t, CommandCancelled.String(), got.Status)
	assert.Equal(t, OutcomeNotDelivered.String(), got.Outcome.String)
	assert.Equal(t, CommandQueued.String(), loadOrFail(t, api, ctx, second.ID).Status)
	assert.Equal(t, CommandQueued.String(), loadOrFail(t, api, ctx, fresh.ID).Status)
	assert.Equal(t, CommandQueued.String(), loadOrFail(t, api, other, foreign.ID).Status)
	n, err = api.RetireCoveredCommands(ctx, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
}

func TestCreateCommandStampsDeviceIncarnation(t *testing.T) {
	for _, id := range []uint64{42, 0} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			api := newTestApi(t)
			api.EnqueueValidator = &stubValidator{incarnation: id}
			ctx := gateCtx("A")
			c, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "single", DeviceToken: "dev", Name: "reboot"})
			require.NoError(t, err)
			assert.Equal(t, sql.NullInt64{Int64: int64(id), Valid: id > 0}, loadOrFail(t, api, ctx, c.ID).DeviceIncarnation)
		})
	}
}

func TestBatchEnqueueStampsDeviceIncarnations(t *testing.T) {
	api := newBatchTestApi(t)
	api.BatchValidator = &countingValidator{incarnations: map[string]uint64{"known": 42}}
	ctx := gateCtx("A")
	_, err := api.CreateCommandBatch(ctx, batchRequest("batch-identities", []string{"known", "unknown"}))
	require.NoError(t, err)
	var commands []Command
	require.NoError(t, api.RDB.DB(ctx).Order("id").Find(&commands).Error)
	require.Len(t, commands, 2)
	assert.Equal(t, sql.NullInt64{Int64: 42, Valid: true}, commands[0].DeviceIncarnation)
	assert.False(t, commands[1].DeviceIncarnation.Valid)
}

func TestCancelHeldCommandCannotCancelAReleasedCommand(t *testing.T) {
	api := newTestApi(t)
	ctx := gateCtx("A")
	c := deletionCommand(t, api, ctx, "held-race", CommandHeld, time.Now().UTC())
	released, err := api.ReleaseHold(ctx, c.ID)
	require.NoError(t, err)
	require.True(t, released)
	cancelled, err := api.CancelHeldCommand(ctx, c.ID)
	require.NoError(t, err)
	assert.False(t, cancelled)
	assert.Equal(t, CommandQueued.String(), loadOrFail(t, api, ctx, c.ID).Status)
}
