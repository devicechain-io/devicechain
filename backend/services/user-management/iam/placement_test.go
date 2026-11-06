// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newPlacementStore(t *testing.T) (*Store, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&PartitionConfig{}, &VPartitionAssignment{}, &TenantVPartitionOverride{}))
	return NewStore(&rdb.RdbManager{Database: db}), db
}

func TestEnsurePlacementStartsAsOneShardGroup(t *testing.T) {
	s, _ := newPlacementStore(t)
	table, err := s.EnsurePlacement(context.Background(), partition.DefaultP)
	require.NoError(t, err)
	require.Equal(t, partition.DefaultP, table.P)
	require.Equal(t, 1, table.S(), "an instance starts with every vpartition on shard group 0")
	require.Empty(t, table.Overrides)
	require.Equal(t, uint64(0), table.Generation)
	require.NoError(t, table.Validate())
}

func TestEnsurePlacementIsIdempotent(t *testing.T) {
	s, db := newPlacementStore(t)
	ctx := context.Background()
	first, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)
	require.NoError(t, s.AssignVPartition(ctx, 3, 2))

	again, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)
	require.Equal(t, 2, again.Groups[3], "a second Ensure must not reset an assignment")
	require.Equal(t, first.Generation+1, again.Generation)

	var n int64
	require.NoError(t, db.Model(&VPartitionAssignment{}).Count(&n).Error)
	require.Equal(t, int64(partition.DefaultP), n)
}

// 🔑 P IMMUTABILITY, at the store: a different P is refused loudly and nothing changes.
func TestEnsurePlacementRefusesToChangeP(t *testing.T) {
	s, db := newPlacementStore(t)
	ctx := context.Background()
	_, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)

	for _, other := range []int{1, 32, 128} {
		_, err = s.EnsurePlacement(ctx, other)
		require.Error(t, err)
		require.True(t, errors.Is(err, partition.ErrPChanged), "P=%d: %v", other, err)
	}

	var cfg PartitionConfig
	require.NoError(t, db.First(&cfg, partitionConfigID).Error)
	require.Equal(t, partition.DefaultP, cfg.P)
	var n int64
	require.NoError(t, db.Model(&VPartitionAssignment{}).Count(&n).Error)
	require.Equal(t, int64(partition.DefaultP), n, "a refused P must not seed another P's assignments")
}

func TestEnsurePlacementRefusesAnImpossibleP(t *testing.T) {
	s, _ := newPlacementStore(t)
	for _, p := range []int{0, -1, partition.MaxP + 1} {
		_, err := s.EnsurePlacement(context.Background(), p)
		require.Error(t, err, "P=%d", p)
	}
}

func TestPlacementBeforeEnsureIsAnErrorNotAnEmptyTable(t *testing.T) {
	s, _ := newPlacementStore(t)
	_, err := s.Placement(context.Background())
	require.True(t, errors.Is(err, ErrPlacementNotInitialized))
	require.True(t, errors.Is(s.AssignVPartition(context.Background(), 0, 1), ErrPlacementNotInitialized))
}

func TestAssignVPartitionMovesOneVPartitionAndBumpsTheGeneration(t *testing.T) {
	s, _ := newPlacementStore(t)
	ctx := context.Background()
	_, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)

	require.NoError(t, s.AssignVPartition(ctx, 10, 3))
	table, err := s.Placement(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, table.Groups[10])
	require.Equal(t, 4, table.S())
	require.Equal(t, uint64(1), table.Generation)
	for vp, g := range table.Groups {
		if vp != 10 {
			require.Equal(t, 0, g, "vpartition %d must not have moved", vp)
		}
	}
}

func TestAssignVPartitionRefusesWhatIsOutOfRange(t *testing.T) {
	s, _ := newPlacementStore(t)
	ctx := context.Background()
	_, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)
	require.Error(t, s.AssignVPartition(ctx, partition.DefaultP, 0))
	require.Error(t, s.AssignVPartition(ctx, -1, 0))
	require.Error(t, s.AssignVPartition(ctx, 0, -1))
	table, err := s.Placement(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(0), table.Generation, "a refused change must not bump the generation")
}

func TestTenantOverridesRoundTripThroughThePlacement(t *testing.T) {
	s, _ := newPlacementStore(t)
	ctx := context.Background()
	_, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)

	require.NoError(t, s.SetTenantOverride(ctx, "big-tenant", 7))
	require.NoError(t, s.SetTenantOverride(ctx, "big-tenant", 9), "a pin is replaced, not duplicated")
	require.NoError(t, s.SetTenantOverride(ctx, "other", 7))
	table, err := s.Placement(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]int{"big-tenant": 9, "other": 7}, table.Overrides)
	require.Equal(t, 9, table.VPartition("dc", "big-tenant"))

	require.NoError(t, s.ClearTenantOverride(ctx, "big-tenant"))
	table, err = s.Placement(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]int{"other": 7}, table.Overrides)
	require.Equal(t, uint64(4), table.Generation)
}

func TestTenantOverrideRefusesWhatIsNotAPlacement(t *testing.T) {
	s, _ := newPlacementStore(t)
	ctx := context.Background()
	_, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)
	require.Error(t, s.SetTenantOverride(ctx, "", 1))
	require.Error(t, s.SetTenantOverride(ctx, "has.dot", 1))
	require.Error(t, s.SetTenantOverride(ctx, "acme", partition.DefaultP))
	require.Error(t, s.SetTenantOverride(ctx, "acme", -1))
}

func TestPlacementRefusesAnIncompleteAssignment(t *testing.T) {
	s, db := newPlacementStore(t)
	ctx := context.Background()
	_, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)
	require.NoError(t, db.Delete(&VPartitionAssignment{}, "vpartition = ?", 5).Error)
	_, err = s.Placement(ctx)
	require.Error(t, err, "a table with a hole must not be handed out to route by")
}
