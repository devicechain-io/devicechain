// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDetectStreamTopologyMigrationReplay(t *testing.T) {
	db := sqlitetest.OpenMemory(t, &gorm.Config{Logger: logger.Discard})
	for _, m := range Migrations {
		require.NoError(t, m.Migrate(db))
	}
	row := DetectStreamTopology{ID: "singleton", BaseSuffix: "resolved-events", GroupCount: 2, CreatedAt: time.Now().UTC(), FormatVersion: 1}
	require.NoError(t, db.Create(&row).Error)
	// Direct replay, not gormigrate's already-recorded-ID no-op.
	require.NoError(t, NewDetectStreamTopology().Migrate(db))
	var got DetectStreamTopology
	require.NoError(t, db.Take(&got).Error)
	require.Equal(t, row, got)
	require.Error(t, db.Create(&DetectStreamTopology{ID: "other"}).Error, "the table permits only the singleton key")
}

func TestDetectStreamTopologyTransitions(t *testing.T) {
	for _, history := range []int64{0, 42} {
		t.Run(map[bool]string{true: "history", false: "fresh"}[history > 0], func(t *testing.T) {
			db := sqlitetest.OpenMemory(t, &gorm.Config{Logger: logger.Discard})
			require.NoError(t, rdb.RegisterTenantScoping(db))
			for _, m := range Migrations {
				require.NoError(t, m.Migrate(db))
			}
			s := NewSnapshotStore(&rdb.RdbManager{Database: db})
			ctx := context.Background()
			if history > 0 {
				require.NoError(t, s.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: history, Watermark: time.Now().UTC(), Payload: []byte(`{"lastSeq":42}`)}))
			}
			// Legacy startup neither writes topology nor modifies the checkpoint.
			require.NoError(t, s.EnsureStreamTopology(ctx, "resolved-events", 1, false))
			var count int64
			require.NoError(t, db.Model(&DetectStreamTopology{}).Count(&count).Error)
			require.Zero(t, count)
			err := s.EnsureStreamTopology(ctx, "resolved-events", 2, true)
			if history > 0 {
				require.ErrorIs(t, err, ErrShardingDetectionHistory)
				require.NoError(t, db.Model(&DetectStreamTopology{}).Count(&count).Error)
				require.Zero(t, count, "refusal must not leave a manifest")
				snap, ok, err := s.Load(ctx, "singleton")
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, history, snap.StreamSeq)
				return
			}
			require.NoError(t, err)
			var first DetectStreamTopology
			require.NoError(t, db.Take(&first).Error)
			require.Equal(t, 2, first.GroupCount)
			require.Equal(t, 1, first.FormatVersion)
			require.False(t, first.CreatedAt.IsZero())
			// Sharded checkpoints are history too, but a matching manifest permits restart.
			require.NoError(t, s.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 12, Watermark: time.Now().UTC(), Payload: []byte(`{"lastSeq":12}`)}))
			require.NoError(t, s.EnsureStreamTopology(ctx, "resolved-events", 2, true))
			for _, change := range []struct {
				suffix  string
				groups  int
				sharded bool
			}{
				{"resolved-events", 3, true}, {"other", 2, true}, {"resolved-events", 1, false},
			} {
				require.ErrorIs(t, s.EnsureStreamTopology(ctx, change.suffix, change.groups, change.sharded), ErrDetectStreamTopologyMismatch)
			}
			var after DetectStreamTopology
			require.NoError(t, db.Take(&after).Error)
			require.Equal(t, first, after)
		})
	}
}
