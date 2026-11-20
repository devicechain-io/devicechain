// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersistencePathsFilterLateEntityAnchors(t *testing.T) {
	for _, batchSize := range []int{1, 8} {
		t.Run(fmt.Sprintf("batch_%d", batchSize), func(t *testing.T) {
			r := newBatchRig(t, batchSize)
			ctx := core.WithTenant(context.Background(), fenceCostTenant)
			cutoff := batchT0
			_, err := r.api.DeleteAnchorsForEntity(ctx, "area", "floor", cutoff)
			require.NoError(t, err)
			old := measurementOf("old-device", cutoff.Add(-time.Hour), "", 1, "21.5")
			old.Anchors[0].AnchorToken = "floor"
			old.ProcessedTime = cutoff.Add(-time.Minute)
			fresh := measurementOf("new-device", cutoff.Add(-2*time.Hour), "", 1, "22.5")
			fresh.Anchors[0].AnchorToken = "floor"
			fresh.ProcessedTime = cutoff
			r.run(r.consume(t, []sent{{fenceCostTenant, old}, {fenceCostTenant, fresh}}))
			assert.Equal(t, int64(2), count(t, r.db, &model.Event{}, ""))
			assert.Equal(t, int64(2), count(t, r.db, &model.MeasurementEvent{}, ""))
			assert.Equal(t, int64(1), count(t, r.db, &model.EventAnchor{}, ""))
			assert.Equal(t, int64(1), count(t, r.db, &model.EventAnchor{}, "device_token = ?", "new-device"))
			// No alternate id: replay reaches the parent insert. The Postgres
			// integration test also covers its optimistic savepoint rerun. Reverse the
			// incoming receipt times; the original stored parents must still decide.
			old.ProcessedTime, fresh.ProcessedTime = cutoff.Add(time.Hour), cutoff.Add(-time.Hour)
			r.run(r.consume(t, []sent{{fenceCostTenant, old}, {fenceCostTenant, fresh}}))
			assert.Equal(t, int64(2), count(t, r.db, &model.Event{}, ""))
			assert.Equal(t, int64(1), count(t, r.db, &model.EventAnchor{}, ""))
			assert.Equal(t, int64(1), count(t, r.db, &model.EventAnchor{}, "device_token = ?", "new-device"))
			assert.Len(t, r.acks.snapshot(), 4)
			assert.Empty(t, r.reported())
		})
	}
}

func TestEntityDeletionResweepPassAndErasureFence(t *testing.T) {
	ep, db, _ := newFencedPersistenceWorker(t)
	api := ep.Api.(*model.Api)
	ctx := core.WithTenant(context.Background(), fenceCostTenant)
	cutoff := batchT0
	_, err := api.DeleteAnchorsForEntity(ctx, "device", "pump", cutoff)
	require.NoError(t, err)
	due := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, api.RDB.DB(ctx).Model(&model.EntityDeletionWatermark{}).
		Where("entity_type = ? AND entity_token = ?", "device", "pump").Update("resweep_after", due).Error)
	require.NoError(t, api.RDB.DB(ctx).Create(&model.EventAnchor{EventId: []byte("late"), DeviceToken: "pump",
		OccurredTime: cutoff.Add(-time.Hour), AnchorType: "area", AnchorToken: "floor"}).Error)
	s := &EntityDeletionResweep{Api: api}
	require.NoError(t, s.runOnce(context.Background()))
	assert.Zero(t, count(t, db, &model.EventAnchor{}, ""))
	var w model.EntityDeletionWatermark
	require.NoError(t, api.RDB.DB(ctx).Take(&w).Error)
	assert.Nil(t, w.ResweepAfter)

	plantFence(t, db, fenceCostTenant)
	_, err = api.DeleteAnchorsForEntity(ctx, "device", "another", cutoff)
	require.Error(t, err, "watermark upsert must honor the erasure fence")
	assert.Equal(t, int64(1), count(t, db, &model.EntityDeletionWatermark{}, ""))
	err = api.ResweepEntityDeletionWatermark(context.Background(), model.EntityDeletionWatermark{
		TenantScoped: w.TenantScoped, EntityType: w.EntityType, EntityToken: w.EntityToken,
		DeletedTime: w.DeletedTime, ResweepAfter: &due,
	})
	require.Error(t, err, "resweep CAS must honor the erasure fence")
}
