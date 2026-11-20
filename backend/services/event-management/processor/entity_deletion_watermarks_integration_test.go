// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package processor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationGroupedOptimisticDeletionWatermarkFilter(t *testing.T) {
	r := newPgBatchRig(t, "embatchtest", 8)
	tenant := fmt.Sprintf("watermark%d", time.Now().UnixNano())
	ctx := core.WithTenant(context.Background(), tenant)
	cutoff := time.Now().UTC().Truncate(time.Hour)
	_, err := r.api.DeleteAnchorsForEntity(ctx, "area", "floor", cutoff)
	require.NoError(t, err)
	r.ep.metrics = NewPersistMetrics(r.mgr.Microservice)
	fallbacks := r.ep.metrics.insertFallbacks
	old := measurementOf("old-device", cutoff.Add(-time.Hour), "", 1, "21.5")
	old.ProcessedTime = cutoff.Add(-time.Minute)
	old.Anchors[0].AnchorToken = "floor"
	fresh := measurementOf("new-device", cutoff.Add(-2*time.Hour), "", 1, "22.5")
	fresh.ProcessedTime = cutoff.Add(time.Minute)
	fresh.Anchors[0].AnchorToken = "floor"
	send := func() {
		r.run([]messaging.Message{consumed(t, r.acks, 0, tenant, 1, old), consumed(t, r.acks, 1, tenant, 1, fresh)})
	}
	assertStored := func() {
		db := r.mgr.DB(ctx)
		var anchors []model.EventAnchor
		require.NoError(t, db.Find(&anchors).Error)
		require.Len(t, anchors, 1)
		assert.Equal(t, "new-device", anchors[0].DeviceToken)
		assert.Equal(t, int64(2), r.count(t, tenant, ""))
		var n int64
		require.NoError(t, db.Model(&model.MeasurementEvent{}).Count(&n).Error)
		assert.Equal(t, int64(2), n)
	}
	// No duplicates: the grouped plain-INSERT pass succeeds under the savepoint.
	send()
	assertStored()
	assert.Zero(t, testutil.ToFloat64(fallbacks))
	assert.Equal(t, int64(1), r.api.txs.Load())
	// No alternate id: duplicates reach the optimistic insert, force rollback to
	// the savepoint, and rerun the grouped writes with ON CONFLICT. Reverse the
	// incoming receipt times to prove the stored parents decide in the rerun.
	old.ProcessedTime, fresh.ProcessedTime = cutoff.Add(time.Hour), cutoff.Add(-time.Hour)
	send()
	assertStored()
	assert.Equal(t, float64(1), testutil.ToFloat64(fallbacks))
	assert.Equal(t, int64(2), r.api.txs.Load(), "savepoint rerun stays inside the batch transaction")
	assert.Equal(t, map[int]int{0: 2, 1: 2}, r.acks.acked(), "each message is acked once per batch")
	assert.Empty(t, r.failures)
}
