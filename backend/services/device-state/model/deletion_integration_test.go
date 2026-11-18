// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

func TestDeviceDeletionAndProjectionBatchSerializeWithoutDeadlock(t *testing.T) {
	api := newPostgresProjectionApi(t, "deletion")
	replica := newPostgresReplica(t, api)
	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), "a"), 20*time.Second)
	defer cancel()
	for i := 0; i < 20; i++ {
		token := fmt.Sprintf("truck-%d", i)
		at := time.Now().UTC()
		u := deletionUpdate("a", token, at)
		require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{u}))
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() { <-start; _, err := api.ApplyDeviceDeleted(ctx, token, 17, at.Add(time.Minute)); results <- err }()
		go func() { <-start; results <- replica.MergeProjectionBatch(ctx, []ProjectionUpdate{u}) }()
		close(start)
		for j := 0; j < 2; j++ {
			select {
			case err := <-results:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("delete and merge did not complete: possible deadlock")
			}
		}
		// The row and every child are either all gone, or recreated together by the batch.
		var n int64
		require.NoError(t, api.RDB.DB(ctx).Unscoped().Model(&DeviceState{}).Where("device_token = ?", token).Count(&n).Error)
		requireLiveCounts(t, api, "a", token, n)
		applied, err := api.ApplyDeviceDeleted(ctx, token, 17, at.Add(time.Minute))
		require.NoError(t, err)
		require.Equal(t, n > 0, applied)
		requireLiveCounts(t, api, "a", token, 0)
	}
}
