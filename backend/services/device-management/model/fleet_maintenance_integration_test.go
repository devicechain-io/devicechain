// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/attrprotocol"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/devicechain-io/dc-microservice/tenantpurge"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func cleanupFleetMaintenancePost(t *testing.T, p *postPG) {
	t.Helper()
	t.Cleanup(func() {
		_, err := p.api.AbortFleetOperation(p.ctx, p.op.Token, "test-cleanup")
		require.NoError(t, err)
	})
}

// I-19: one resolution note is shared with a surviving device. Deleting either
// target clears the shared note while preserving both verdicts and provenance.
func TestFleetI19PostgresErasureAndDisabledHookRepair(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprint(disabled), func(t *testing.T) {
			p := newPostPG(t, "secret-value")
			cleanupFleetMaintenancePost(t, p)
			for i := range p.lines {
				_, err := p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[i].ID, p.lease)
				require.NoError(t, err)
			}
			note := FleetOperationEvent{OperationId: int64(p.op.ID), At: time.Now(), Kind: "RESOLUTION",
				Note: sql.NullString{String: "device " + p.devs[0].Token, Valid: true}}
			require.NoError(t, p.api.RDB.DB(p.ctx).Create(&note).Error)
			resolvedAt := time.Now().UTC().Truncate(time.Microsecond)
			require.NoError(t, p.api.RDB.DB(p.ctx).Model(&FleetOperationLine{}).Where("operation_id = ?", p.op.ID).
				Updates(map[string]any{"settlement": "RESOLVED_CONFIRMED", "resolution_id": note.ID, "resolved_by": "alice", "resolved_at": resolvedAt}).Error)
			p.api.skipFleetErasureForOldPodTest = disabled
			deleted, err := p.api.DeleteDevice(p.ctx, p.devs[0].Token)
			require.NoError(t, err)
			require.True(t, deleted)
			var line FleetOperationLine
			require.NoError(t, p.api.RDB.DB(p.ctx).First(&line, p.lines[0].ID).Error)
			require.Equal(t, !disabled, line.RedactedAt.Valid)
			if disabled {
				require.Equal(t, p.devs[0].Token, line.TargetKey)
				require.NoError(t, p.api.RepairFleetErasure(p.ctx))
			}
			require.NoError(t, p.api.RDB.DB(p.ctx).First(&line, p.lines[0].ID).Error)
			require.Empty(t, line.TargetKey)
			require.False(t, line.Prior.Valid)
			require.False(t, line.PostedValue.Valid)
			require.Empty(t, line.Detail)
			require.True(t, line.RedactedAt.Valid)
			require.Equal(t, "RESOLVED_CONFIRMED", line.Settlement.String)
			require.Equal(t, "alice", line.ResolvedBy.String)
			require.True(t, line.ResolvedAt.Time.Equal(resolvedAt))
			require.NoError(t, p.api.RDB.DB(p.ctx).First(&note, note.ID).Error)
			require.Empty(t, note.Note.String)
			require.True(t, note.NoteRedactedAt.Valid)
			var survivor FleetOperationLine
			require.NoError(t, p.api.RDB.DB(p.ctx).First(&survivor, p.lines[1].ID).Error)
			require.Equal(t, p.devs[1].Token, survivor.TargetKey)
			require.Equal(t, line.ResolutionId, survivor.ResolutionId)
		})
	}
}

func TestFleetI19PostgresV19DeleteSQLRepair(t *testing.T) {
	p := newPostPG(t, "v19")
	cleanupFleetMaintenancePost(t, p)
	_, err := p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[0].ID, p.lease)
	require.NoError(t, err)
	fixture, err := os.ReadFile("testdata/v19_delete_device.sql")
	require.NoError(t, err)
	var attribute EntityAttribute
	require.NoError(t, p.api.RDB.DB(p.ctx).Where("entity_type = 'device' AND entity_id = ? AND scope = 'SHARED' AND attr_key = 'k'", p.devs[0].ID).First(&attribute).Error)
	require.NoError(t, p.api.runDeviceAttributeTx(p.ctx, "delete", func(tx *gorm.DB) error {
		if err := lockDeviceAttributes(p.ctx, tx, p.devs[0].ID); err != nil {
			return err
		}
		for _, statement := range strings.Split(string(fixture), ";") {
			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}
			if err := tx.Exec(statement, sql.Named("tenant", p.tenant), sql.Named("id", p.devs[0].ID), sql.Named("attribute_id", attribute.ID), sql.Named("token", p.devs[0].Token)).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	var line FleetOperationLine
	require.NoError(t, p.api.RDB.DB(p.ctx).First(&line, p.lines[0].ID).Error)
	require.Equal(t, p.devs[0].Token, line.TargetKey)
	require.False(t, line.RedactedAt.Valid)
	require.NoError(t, p.api.RepairFleetErasure(p.ctx))
	require.NoError(t, p.api.RDB.DB(p.ctx).First(&line, p.lines[0].ID).Error)
	require.Empty(t, line.TargetKey)
	require.True(t, line.RedactedAt.Valid)
	require.Equal(t, "TARGET_ERASED", line.Code)
}

func TestFleetI25PostgresAbortRetentionAndReplay(t *testing.T) {
	api, ctx, _, tokens := fleetPGFixture(t, 2)
	for i := 0; i < 3; i++ {
		token := fmt.Sprintf("aborted-%d", i)
		req := setReq(token, tokens...)
		op, err := api.CreateFleetOperation(ctx, req, "alice")
		require.NoError(t, err)
		_, err = api.AbortFleetOperation(ctx, token, "alice")
		require.NoError(t, err)
		require.NoError(t, api.RetainFleetOperations(ctx, time.Now()))
		var n int64
		require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperationLine{}).Where("operation_id = ?", op.ID).Count(&n).Error)
		require.Zero(t, n)
		replayed, err := api.CreateFleetOperation(ctx, req, "alice")
		require.NoError(t, err)
		require.Equal(t, op.ID, replayed.ID)
		req.ConfigSet.Value = sptr("different")
		_, err = api.CreateFleetOperation(ctx, req, "alice")
		requireFleetCode(t, err, FleetCodeConflict)
	}
}

func TestFleetI25PostgresQuotasSurviveLineRetention(t *testing.T) {
	t.Run("create-rate", func(t *testing.T) {
		api, ctx, _, tokens := fleetPGFixture(t, 1)
		for i := 0; i < ledger.TenantCreatesPerHour; i++ {
			token := fmt.Sprintf("rate-%d", i)
			_, err := api.CreateFleetOperation(ctx, setReq(token, tokens...), "alice")
			require.NoError(t, err)
			_, err = api.AbortFleetOperation(ctx, token, "alice")
			require.NoError(t, err)
			require.NoError(t, api.RetainFleetOperations(ctx, time.Now()))
		}
		_, err := api.CreateFleetOperation(ctx, setReq("over-rate", tokens...), "alice")
		var refusal *FleetOperationError
		require.ErrorAs(t, err, &refusal)
		require.Equal(t, "tenant.createsPerHour", refusal.Quota)
	})
	t.Run("retained-lines", func(t *testing.T) {
		api, ctx, tenant, tokens := fleetPGFixture(t, 1)
		op := seedExpiryOperation(t, api, ctx, "retained", "ABORTED", time.Now())
		t.Cleanup(func() {
			require.NoError(t, api.RDB.DB(ctx).Where("operation_id = ?", op.ID).Delete(&FleetOperationLine{}).Error)
			require.NoError(t, api.RDB.DB(ctx).Delete(op).Error)
		})
		// The quota counts retained rows in every posting state, not merely
		// PLANNED rows or headers which are still dispatching.
		require.NoError(t, api.RDB.DB(ctx).Exec(`INSERT INTO fleet_operation_lines (tenant_id, operation_id, ordinal, posting)
SELECT ?, ?, n, 'CANCELLED' FROM generate_series(2, ?) AS n`, tenant, op.ID, ledger.TenantRetainedLines).Error)
		_, err := api.CreateFleetOperation(ctx, setReq("over-retained", tokens...), "alice")
		var refusal *FleetOperationError
		require.ErrorAs(t, err, &refusal)
		require.Equal(t, "tenant.retainedLines", refusal.Quota)
	})
}

func TestFleetMaintenancePostgresFenceAndTenantPurge(t *testing.T) {
	api, ctx, tenant, tokens := fleetPGFixture(t, 2)
	op, err := api.CreateFleetOperation(ctx, setReq("purge", tokens...), "alice")
	require.NoError(t, err)
	_, err = api.AbortFleetOperation(ctx, "purge", "alice")
	require.NoError(t, err)
	holder := api.RDB.DB(ctx).Begin()
	require.NoError(t, holder.Error)
	defer holder.Rollback()
	require.NoError(t, fleetLock(holder, fleetOpKey(tenant, op.ID), true))
	restore := ShortenFleetFenceWaitForTest("100ms")
	defer restore()
	require.Error(t, api.RetainFleetOperations(ctx, time.Now()), "retention must wait for a shared poster fence")
	require.NoError(t, holder.Rollback().Error)
	for _, model := range []any{&FleetOperation{}, &FleetOperationRuntime{}, &FleetOperationContract{}, &FleetOperationLine{}, &FleetOperationEvent{}} {
		var n int64
		require.NoError(t, api.RDB.DB(ctx).Model(model).Count(&n).Error)
		require.Positive(t, n, "purge must exercise every populated journal table")
	}
	// Catalog-driven purge must classify and remove all five tables, even while
	// operations are unserved, just as a previous-minor pod's purge does.
	plan, err := tenantpurge.Classify(core.WithSystemContext(context.Background()), api.RDB.Database)
	require.NoError(t, err)
	attrprotocol.RegisterPurge(plan)
	res, err := tenantpurge.Sweep(core.WithSystemContext(ctx), api.RDB.Database, plan, tenant, time.Now(), nil)
	require.NoError(t, err)
	require.True(t, res.Complete())
	for _, model := range []any{&FleetOperation{}, &FleetOperationRuntime{}, &FleetOperationContract{}, &FleetOperationLine{}, &FleetOperationEvent{}} {
		var n int64
		require.NoError(t, api.RDB.DB(ctx).Model(model).Count(&n).Error)
		require.Zero(t, n)
	}
}
