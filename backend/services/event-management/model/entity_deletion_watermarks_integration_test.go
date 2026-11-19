// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationDeletionResweepWaitsForOlderTransaction(t *testing.T) {
	api := newPostgresApi(t, freshInstance(t, "itwatermarktxn"))
	ctx := core.WithTenant(context.Background(), "acme")
	pool, err := api.RDB.Database.DB()
	require.NoError(t, err)
	// Pin a second connection in a transaction before arming the watermark. It
	// represents a persist that saw no watermark and then waited on unrelated rows.
	conn, err := pool.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	var started time.Time
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT transaction_timestamp()").Scan(&started))
	cutoff := time.Now().UTC().Truncate(time.Microsecond)
	_, err = api.DeleteAnchorsForEntity(ctx, "area", "floor", cutoff)
	require.NoError(t, err)
	var w EntityDeletionWatermark
	require.NoError(t, api.RDB.DB(ctx).Take(&w).Error)
	require.NotNil(t, w.ResweepAfter)
	require.True(t, started.Before(w.ResweepAfter.Add(-EntityDeletionResweepDelay)))
	plant := func(id string) {
		require.NoError(t, api.RDB.DB(ctx).Create(&EventAnchor{EventId: []byte(id), DeviceToken: "pump",
			OccurredTime: cutoff.Add(-time.Hour), AnchorType: "area", AnchorToken: "floor"}).Error)
	}
	assertClean := func() {
		var n int64
		require.NoError(t, api.RDB.DB(ctx).Model(&EventAnchor{}).Count(&n).Error)
		assert.Zero(t, n, "cleanup must run even when the schedule is re-armed")
	}
	plant("first")
	before := time.Now().UTC()
	require.NoError(t, api.ResweepEntityDeletionWatermark(context.Background(), w))
	assertClean()
	var rearmed EntityDeletionWatermark
	require.NoError(t, api.RDB.DB(ctx).Take(&rearmed).Error)
	require.NotNil(t, rearmed.ResweepAfter, "an older transaction can still persist late anchors")
	assert.WithinDuration(t, before.Add(time.Minute), *rearmed.ResweepAfter, 5*time.Second)
	assert.False(t, rearmed.ResweepAfter.Equal(*w.ResweepAfter), "CAS must replace the schedule")

	require.NoError(t, tx.Commit())
	plant("after-commit")
	require.NoError(t, api.ResweepEntityDeletionWatermark(context.Background(), rearmed))
	assertClean()
	var cleared EntityDeletionWatermark
	require.NoError(t, api.RDB.DB(ctx).Take(&cleared).Error)
	assert.Nil(t, cleared.ResweepAfter, "the next pass can clear after the old transaction ends")
}
