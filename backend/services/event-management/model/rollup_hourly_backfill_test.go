// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestHourlyReadersDiscoverAnotherReplicasBackfill(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	conn, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, db.AutoMigrate(&rollupLevel{}))
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	initial := now
	require.NoError(t, db.Create(&rollupLevel{Level: "1h", CompleteFrom: initial}).Error)
	readers := []*Api{NewApi(&rdb.RdbManager{Database: db}), NewApi(&rdb.RdbManager{Database: db})}
	for _, api := range readers {
		api.now = func() time.Time { return now }
		from, ok, err := api.rollup1hCompleteFrom(context.Background())
		require.NoError(t, err)
		require.True(t, ok)
		require.True(t, initial.Equal(from))
	}
	// A worker on another replica commits two historical windows.
	completed := initial.Add(-2 * time.Hour)
	require.NoError(t, db.Model(&rollupLevel{}).Where("level = ?", "1h").Update("complete_from", completed).Error)
	now = now.Add(2 * time.Minute)
	for _, api := range readers {
		from, ok, err := api.rollup1hCompleteFrom(context.Background())
		require.NoError(t, err)
		require.True(t, ok)
		require.True(t, completed.Equal(from), "running replicas must discover completed history without a restart")
	}
}
