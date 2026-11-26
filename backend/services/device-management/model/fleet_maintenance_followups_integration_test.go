// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

// These tests require real PostgreSQL and the integration build tag. They are
// deliberately excluded from mcp-gate; the companion file runs in the unit gate.
func TestFleetMaintenancePostgresPostedAbortSkipsHeldFence(t *testing.T) {
	p := newPostPG(t, "old-pod-value")
	// No cleanupFleetMaintenancePost: this test aborts the operation itself, and
	// aborting an ABORTED operation is refused.
	result, err := p.api.PostFleetLine(p.ctx, p.op.ID, p.lines[0].ID, p.lease)
	require.NoError(t, err)
	require.Equal(t, FleetPosted, result.Outcome)
	_, err = p.api.AbortFleetOperation(p.ctx, p.op.Token, "alice")
	require.NoError(t, err)
	// Explicitly emulate the header an old posting pod leaves behind, even if
	// a future release starts incrementing the counter in its own posting path.
	require.NoError(t, p.api.RDB.DB(p.ctx).Model(p.op).Update("posted_line_count", 0).Error)
	now, err := fleetMaintenanceNow(p.api.RDB.DB(p.ctx))
	require.NoError(t, err)
	require.NoError(t, p.api.RetainFleetOperations(p.ctx, now))
	holder := p.api.RDB.DB(p.ctx).Begin()
	require.NoError(t, holder.Error)
	defer holder.Rollback()
	require.NoError(t, fleetLock(holder, fleetOpKey(p.tenant, p.op.ID), true))
	restore := ShortenFleetFenceWaitForTest("100ms")
	defer restore()
	require.NoError(t, p.api.RetainFleetOperations(p.ctx, now), "an already examined posted abort must not wait on the operation fence")
	line := reloadFleetLine(t, p, p.lines[0].ID)
	require.Equal(t, "POSTED", line.Posting)
	require.True(t, line.Prior.Valid, "posted evidence stays until its normal retention deadline")
}

func TestFleetMaintenancePostgresRetentionBacklogCannotStarveRepair(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	prefix := "000bc8-" + rand.Text()
	// Keep the pair adjacent and before other integration fixtures' tenants.
	a, b := prefix+"-a", prefix+"-b"
	t.Cleanup(func() {
		for _, tenant := range []string{a, b} {
			ctx := core.WithTenant(context.Background(), tenant)
			for _, model := range []any{&FleetOperationLine{}, &FleetOperationEvent{}, &FleetOperationContract{}, &FleetOperationRuntime{}, &FleetOperation{}} {
				require.NoError(t, api.RDB.DB(ctx).Where("tenant_id = ?", tenant).Delete(model).Error)
			}
		}
	})
	assertFleetMaintenanceBacklogFairness(t, api, a, b)
}
