// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func deletionCommand(t *testing.T, api *Api, ctx context.Context, token string, status CommandStatus, created time.Time) *Command {
	t.Helper()
	c := &Command{Model: gorm.Model{CreatedAt: created}, DeviceToken: "valve-7", Name: "reboot", Status: status.String()}
	c.Token = token
	c.Token = token
	require.NoError(t, api.RDB.DB(ctx).Create(c).Error)
	return c
}

func TestADeletedDevicesHeldCommandsAreCancelledAndANewDevicesAreNot(t *testing.T) {
	api := newTestApi(t)
	ctx := gateCtx("A")
	at := time.Now().UTC().Truncate(time.Second)
	var cancelled []*Command
	for _, status := range []CommandStatus{CommandQueued, CommandHeld, CommandParked} {
		cancelled = append(cancelled, deletionCommand(t, api, ctx, status.String(), status, at))
	}
	sent := deletionCommand(t, api, ctx, "sent", CommandSent, at.Add(-time.Second))
	newer := deletionCommand(t, api, ctx, "new", CommandQueued, at.Add(time.Second))
	other := deletionCommand(t, api, gateCtx("B"), "other-tenant", CommandQueued, at)
	n, err := api.CancelDeletedDeviceCommands(ctx, "valve-7", 0, at)
	require.NoError(t, err)
	assert.Equal(t, int64(3), n)
	for _, c := range cancelled {
		got := loadOrFail(t, api, ctx, c.ID)
		assert.Equal(t, CommandCancelled.String(), got.Status)
		assert.Equal(t, OutcomeNotDelivered.String(), got.Outcome.String)
	}
	assert.Equal(t, CommandSent.String(), loadOrFail(t, api, ctx, sent.ID).Status)
	assert.Equal(t, CommandQueued.String(), loadOrFail(t, api, gateCtx("B"), other.ID).Status)
	assert.Equal(t, CommandQueued.String(), loadOrFail(t, api, ctx, newer.ID).Status)
	_, claimed, err := api.MarkSent(ctx, newer.ID)
	require.NoError(t, err)
	assert.True(t, claimed)
}

func TestTheClaimRefusesACommandTheDeletionCovers(t *testing.T) {
	for _, path := range []string{"MarkSent", "MarkSentByToken", "claimForPull"} {
		t.Run(path, func(t *testing.T) {
			api := newTestApi(t)
			ctx := gateCtx("A")
			at := time.Now().UTC().Truncate(time.Second)
			_, err := api.CancelDeletedDeviceCommands(ctx, "valve-7", 0, at)
			require.NoError(t, err)
			old := deletionCommand(t, api, ctx, "old", CommandQueued, at.Add(-time.Second))
			newer := deletionCommand(t, api, ctx, "new", CommandQueued, at.Add(time.Second))
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
			assert.Equal(t, CommandQueued.String(), loadOrFail(t, api, ctx, old.ID).Status)
			assert.True(t, claim(newer))
			assert.Equal(t, CommandSent.String(), loadOrFail(t, api, ctx, newer.ID).Status)
		})
	}
}

func TestDeletionFloorIsMonotoneAndPreservesDisabled(t *testing.T) {
	api := newTestApi(t)
	ctx := gateCtx("A")
	t1 := time.Now().UTC().Truncate(time.Second)
	t2 := t1.Add(2 * time.Second)
	_, err := api.SetDeviceDisabled(ctx, "valve-7", true)
	require.NoError(t, err)
	_, err = api.CancelDeletedDeviceCommands(ctx, "valve-7", 0, t2)
	require.NoError(t, err)
	_, err = api.CancelDeletedDeviceCommands(ctx, "valve-7", 0, t1)
	require.NoError(t, err)
	var gate DeviceGate
	require.NoError(t, api.RDB.DB(ctx).Where("device_token = ?", "valve-7").First(&gate).Error)
	assert.True(t, gate.Disabled)
	require.True(t, gate.DeletedThrough.Valid)
	assert.True(t, t2.Equal(gate.DeletedThrough.Time))
	// A later roster update must preserve the floor too. Remove the disabled gate so the
	// refused claim below proves the floor, rather than the disabled flag, blocked it.
	_, err = api.SetDeviceDisabled(ctx, "valve-7", false)
	require.NoError(t, err)
	c := deletionCommand(t, api, ctx, "between", CommandQueued, t1.Add(time.Second))
	_, claimed, err := api.MarkSent(ctx, c.ID)
	require.NoError(t, err)
	assert.False(t, claimed)
}

func TestDeviceGateDeletedMigrationReplaysWithTablePrefix(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "area_"}})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE area_device_gates (id integer PRIMARY KEY, disabled numeric NOT NULL)").Error)
	require.NoError(t, db.Exec("INSERT INTO area_device_gates (disabled) VALUES (true)").Error)
	for range 2 {
		require.NoError(t, NewDeviceGateDeletedSchema().Migrate(db))
	}
	var rows []struct {
		Disabled       bool
		DeletedThrough sql.NullTime
	}
	require.NoError(t, db.Table("area_device_gates").Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.True(t, rows[0].Disabled)
	assert.False(t, rows[0].DeletedThrough.Valid)
	require.NoError(t, db.Exec("INSERT INTO area_device_gates (disabled) VALUES (false)").Error)
	assert.False(t, db.Migrator().HasTable("area_device_gate_deleted_snapshots"))
}

// Failure of the cancellation must roll the floor back with it.
func TestDeletedDeviceCancellationIsAtomic(t *testing.T) {
	api := newTestApi(t)
	ctx := gateCtx("A")
	at := time.Now().UTC().Truncate(time.Second)
	c := deletionCommand(t, api, ctx, "old", CommandQueued, at)
	require.NoError(t, api.RDB.DB(ctx).Exec(`CREATE TRIGGER refuse_cancel BEFORE UPDATE ON commands
 BEGIN SELECT RAISE(ABORT, 'write refused'); END`).Error)
	count, err := api.CancelDeletedDeviceCommands(ctx, "valve-7", 0, at)
	require.Error(t, err)
	assert.Zero(t, count)
	var gates int64
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceGate{}).Count(&gates).Error)
	assert.Zero(t, gates)
	assert.Equal(t, CommandQueued.String(), loadOrFail(t, api, ctx, c.ID).Status)
}

// The reads that OFFER commands for dispatch (the parked drain and the device's pull) leave a
// covered command out, so it is not offered on every pass only to have its claim refused. The
// newer command is offered by both, so neither read is vacuously empty.
func TestTheDispatchReadsDoNotOfferACommandTheDeletionCovers(t *testing.T) {
	for _, status := range []CommandStatus{CommandParked, CommandQueued} {
		t.Run(status.String(), func(t *testing.T) {
			api := newTestApi(t)
			ctx := gateCtx("A")
			at := time.Now().UTC().Truncate(time.Second)
			_, err := api.CancelDeletedDeviceCommands(ctx, "valve-7", 0, at)
			require.NoError(t, err)
			deletionCommand(t, api, ctx, "old", status, at.Add(-time.Second))
			deletionCommand(t, api, ctx, "new", status, at.Add(time.Second))
			if status == CommandParked {
				got, err := api.DrainableCommands(ctx, "valve-7", 32)
				require.NoError(t, err)
				require.Equal(t, []string{"new"}, tokensOf(got))
				return
			}
			got, err := api.PullCommands(ctx, "valve-7", 32)
			require.NoError(t, err)
			tokens := make([]string, 0, len(got))
			for _, p := range got {
				tokens = append(tokens, p.Command.Token)
			}
			require.Equal(t, []string{"new"}, tokens)
		})
	}
}

// A dispatch claimed BEFORE the delete is SENT, so the deletion's cancel does not reach it; the
// transport's confirmation is the last point before it acts, and must refuse it. A dispatch of
// the re-created device's command is confirmed as before.
func TestAConfirmationOfADispatchTheDeletionCoversIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := gateCtx("A")
	at := time.Now().UTC().Truncate(time.Second)
	old := deletionCommand(t, api, ctx, "old", CommandQueued, at.Add(-time.Second))
	oldNonce, ok, err := api.MarkSent(ctx, old.ID)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = api.CancelDeletedDeviceCommands(ctx, "valve-7", 0, at)
	require.NoError(t, err)
	require.Equal(t, CommandSent.String(), loadOrFail(t, api, ctx, old.ID).Status, "the cancel reached a SENT command")
	_, ok, err = api.ConfirmDispatch(ctx, "old", oldNonce)
	require.NoError(t, err)
	assert.False(t, ok, "a dispatch the deletion covers was confirmed")

	newer := deletionCommand(t, api, ctx, "new", CommandQueued, at.Add(time.Second))
	newNonce, ok, err := api.MarkSent(ctx, newer.ID)
	require.NoError(t, err)
	require.True(t, ok)
	_, ok, err = api.ConfirmDispatch(ctx, "new", newNonce)
	require.NoError(t, err)
	assert.True(t, ok, "the re-created device's dispatch was refused")
}

// The sweep's read and the nudge's per-device read do not offer a covered command, so a row the
// cancel missed (inserted after it, or released back to QUEUED) is not re-offered every pass.
func TestTheSweepReadsDoNotOfferACommandTheDeletionCovers(t *testing.T) {
	api := newTestApi(t)
	ctx := gateCtx("A")
	at := time.Now().UTC().Truncate(time.Second)
	_, err := api.CancelDeletedDeviceCommands(ctx, "valve-7", 0, at)
	require.NoError(t, err)
	deletionCommand(t, api, ctx, "old", CommandQueued, at.Add(-time.Second))
	deletionCommand(t, api, ctx, "new", CommandQueued, at.Add(time.Second))
	pending, err := api.PendingCommands(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"new"}, tokensOf(pending))
	queued, err := api.QueuedCommandsForDevice(ctx, "valve-7", 32)
	require.NoError(t, err)
	assert.Equal(t, []string{"new"}, tokensOf(queued))
}

func TestDeviceIncarnationMigrationReplaysPartialApplication(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "area_"}})
	require.NoError(t, err)
	// Simulate interruption after commands received its column but before gates did.
	require.NoError(t, db.Exec("CREATE TABLE area_commands (id integer PRIMARY KEY, device_incarnation bigint)").Error)
	require.NoError(t, db.Exec("CREATE TABLE area_device_gates (id integer PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("INSERT INTO area_commands (id, device_incarnation) VALUES (1, 42)").Error)
	for range 2 {
		require.NoError(t, NewDeviceIncarnationSchema().Migrate(db))
	}
	var command struct{ DeviceIncarnation sql.NullInt64 }
	require.NoError(t, db.Table("area_commands").First(&command).Error)
	assert.Equal(t, sql.NullInt64{Int64: 42, Valid: true}, command.DeviceIncarnation)
	// Old writers can still omit both new columns.
	require.NoError(t, db.Exec("INSERT INTO area_commands (id) VALUES (2)").Error)
	require.NoError(t, db.Exec("INSERT INTO area_device_gates (id) VALUES (1)").Error)
	var gate struct{ DeletedIncarnation sql.NullInt64 }
	require.NoError(t, db.Table("area_device_gates").First(&gate).Error)
	assert.False(t, gate.DeletedIncarnation.Valid)
	assert.False(t, db.Migrator().HasTable("area_command_incarnation_snapshots"))
	assert.False(t, db.Migrator().HasTable("area_gate_incarnation_snapshots"))
}
