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
	"github.com/devicechain-io/dc-microservice/presence"
	"github.com/stretchr/testify/require"
)

func TestLateDeletedEventNewOwnerSeesOnlyNewIncarnation(t *testing.T) {
	assertLateDeletedEventIsStampedAndReplaced(t, newPostgresProjectionApi(t, "lateincarnation"), true)
}

func TestIncarnationResetAndOrdinaryBatchDoNotDeadlock(t *testing.T) {
	api := newPostgresProjectionApi(t, "incarnation")
	replica := newPostgresReplica(t, api)
	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), "a"), 30*time.Second)
	defer cancel()
	for i := 0; i < 20; i++ {
		token, other := fmt.Sprintf("a-%d", i), fmt.Sprintf("b-%d", i)
		at := time.Now().UTC()
		old := withIncarnation(deletionUpdate("a", token, at), 17)
		require.NoError(t, api.MergeProjectionBatch(ctx, []ProjectionUpdate{old}))
		newEvent := withIncarnation(deletionUpdate("a", token, at.Add(-time.Hour)), 23)
		newEvent.Measurements[0].Value.Float64 = 23
		// Exercise both the real heartbeat SQL guard and the locked reset.
		if i%2 != 0 {
			newEvent.Presence = &PresenceTransition{Claim: presence.ClaimConnected, SessionId: 1}
		}
		ordinary := withIncarnation(deletionUpdate("a", other, at), 31)
		start, results := make(chan struct{}), make(chan error, 2)
		go func() { <-start; results <- api.MergeProjectionBatch(ctx, []ProjectionUpdate{newEvent, ordinary}) }()
		go func() { <-start; results <- replica.MergeProjectionBatch(ctx, []ProjectionUpdate{old, ordinary}) }()
		close(start)
		for j := 0; j < 2; j++ {
			select {
			case err := <-results:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("reset and ordinary batch did not complete")
			}
		}
		var row DeviceState
		require.NoError(t, api.RDB.DB(ctx).Where("device_token = ?", token).First(&row).Error)
		require.Equal(t, eventIncarnation(23), row.Incarnation)
		var reading LatestMeasurement
		require.NoError(t, api.RDB.DB(ctx).Where("device_token = ?", token).First(&reading).Error)
		require.Equal(t, float64(23), reading.Value.Float64)
	}
}
