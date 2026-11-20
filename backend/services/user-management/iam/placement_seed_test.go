// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func layout(groups []int) string {
	var b strings.Builder
	for _, g := range groups {
		b.WriteByte(byte('0' + g))
	}
	return b.String()
}

func countGroups(groups []int, n int) []int {
	out := make([]int, n)
	for _, g := range groups {
		out[g]++
	}
	return out
}

func seededStore(s *Store, weights ...int) *Store {
	s.NatsShardGroups = &config.NatsShardGroupsConfiguration{Count: len(weights), Suffixes: []string{"inbound-events"},
		Seed: &config.NatsShardGroupsSeed{Weights: weights}}
	return s
}

// TestSeedGroupsLayoutIsFixed pins the seed's layout: it is computed once per instance and stored,
// so a change here moves no existing tenant, but two installs of one level would then differ.
func TestSeedGroupsLayoutIsFixed(t *testing.T) {
	for _, tc := range []struct {
		weights []int
		want    string
	}{
		{[]int{22, 21, 21}, "0120120120120120120120120120120120120120120120120120120120120120"},
		{[]int{21, 22, 21}, "1021021021021021021021021021021021021021021021021021021021021021"},
		{[]int{12, 26, 26}, "1201212012120121201212120121201212012120121201212012121201212012"},
	} {
		groups, err := seedGroups(partition.DefaultP, tc.weights)
		require.NoError(t, err)
		require.Equal(t, tc.want, layout(groups), "weights %v", tc.weights)
		require.Equal(t, tc.weights, countGroups(groups, len(tc.weights)), "each group takes exactly its weight")
	}
	groups, err := seedGroups(partition.DefaultP, nil)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("0", partition.DefaultP), layout(groups), "no seed is the unseeded table")
}

// Exact weight counts hold for every split, not only the fixtures: a seed that dropped or doubled a
// vpartition would leave a hole the next start refuses, or a group short of its share.
func TestSeedGroupsTakesEveryWeightExactly(t *testing.T) {
	for a := 1; a < partition.DefaultP-1; a++ {
		for b := 1; a+b < partition.DefaultP; b++ {
			weights := []int{a, b, partition.DefaultP - a - b}
			groups, err := seedGroups(partition.DefaultP, weights)
			require.NoError(t, err)
			require.Equal(t, weights, countGroups(groups, 3), "weights %v", weights)
		}
	}
}

func TestSeedGroupsRefusesASeedThatDoesNotFitTheTable(t *testing.T) {
	for _, weights := range [][]int{{21, 21, 21}, {22, 22, 21}, {64, 0}, {65, -1}} {
		_, err := seedGroups(partition.DefaultP, weights)
		require.ErrorIs(t, err, ErrSeedInvalid, "weights %v", weights)
	}
}

// 🔑 First start with a seed lays the table out by it, before any event exists.
func TestEnsurePlacementSeedsTheTableItCreates(t *testing.T) {
	s, _ := newPlacementStore(t)
	table, err := seededStore(s, 22, 21, 21).EnsurePlacement(context.Background(), partition.DefaultP)
	require.NoError(t, err)
	want, err := seedGroups(partition.DefaultP, []int{22, 21, 21})
	require.NoError(t, err)
	require.Equal(t, want, table.Groups)
	require.Equal(t, uint64(0), table.Generation, "seeding is the table's creation, not a change")
	require.NoError(t, table.Validate())
}

// 🔑 The seed applies only in the transaction that created the config row: a later start with
// another seed, or with one where there was none, changes nothing.
func TestEnsurePlacementNeverReseedsAnExistingTable(t *testing.T) {
	ctx := context.Background()
	t.Run("seeded then reseeded", func(t *testing.T) {
		s, _ := newPlacementStore(t)
		first, err := seededStore(s, 22, 21, 21).EnsurePlacement(ctx, partition.DefaultP)
		require.NoError(t, err)
		again, err := seededStore(s, 12, 26, 26).EnsurePlacement(ctx, partition.DefaultP)
		require.NoError(t, err)
		require.Equal(t, first, again)
	})
	t.Run("unseeded then seeded", func(t *testing.T) {
		s, _ := newPlacementStore(t)
		first, err := s.EnsurePlacement(ctx, partition.DefaultP)
		require.NoError(t, err)
		require.Equal(t, 1, first.S())
		again, err := seededStore(s, 22, 21, 21).EnsurePlacement(ctx, partition.DefaultP)
		require.NoError(t, err)
		require.Equal(t, first, again, "a seed added to an existing instance must not move a tenant")
	})
}

// 🔑 A hole is refused, not filled. Filling it from the configuration of the day would leave a
// table that is part one seed and part another (or part unseeded), and route those vpartitions'
// tenants to a group that does not hold their data.
func TestEnsurePlacementRefusesAHoleRatherThanFillingIt(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		s, db := newPlacementStore(t)
		ctx := context.Background()
		if seeded {
			seededStore(s, 22, 21, 21)
		}
		_, err := s.EnsurePlacement(ctx, partition.DefaultP)
		require.NoError(t, err)
		require.NoError(t, db.Where("vpartition = ?", 7).Delete(&VPartitionAssignment{}).Error)

		_, err = s.EnsurePlacement(ctx, partition.DefaultP)
		require.ErrorIs(t, err, ErrPlacementInvalid, "seeded=%v", seeded)
		var n int64
		require.NoError(t, db.Model(&VPartitionAssignment{}).Count(&n).Error)
		require.Equal(t, int64(partition.DefaultP-1), n, "seeded=%v: the hole was filled", seeded)
	}
}

// A restored table exists before the service first starts, so it wins over the seed.
func TestEnsurePlacementKeepsARestoredTable(t *testing.T) {
	s, db := newPlacementStore(t)
	restored := make([]VPartitionAssignment, 0, partition.DefaultP)
	for vp := 0; vp < partition.DefaultP; vp++ {
		restored = append(restored, VPartitionAssignment{VPartition: vp, ShardGroup: vp % 2})
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&PartitionConfig{ID: partitionConfigID, P: partition.DefaultP, Generation: 4}).Error; err != nil {
			return err
		}
		return tx.Create(&restored).Error
	}))
	table, err := seededStore(s, 22, 21, 21).EnsurePlacement(context.Background(), partition.DefaultP)
	require.NoError(t, err)
	for vp, g := range table.Groups {
		require.Equal(t, vp%2, g, "vpartition %d", vp)
	}
	require.Equal(t, uint64(4), table.Generation)
}

// A seed that cannot be laid out fails the first start loudly and writes nothing, so the next start
// with a corrected seed still creates (and seeds) the table.
func TestEnsurePlacementRefusesABadSeedAndWritesNothing(t *testing.T) {
	s, db := newPlacementStore(t)
	ctx := context.Background()
	_, err := seededStore(s, 21, 21, 21).EnsurePlacement(ctx, partition.DefaultP)
	require.ErrorIs(t, err, ErrSeedInvalid)
	for _, model := range []interface{}{&PartitionConfig{}, &VPartitionAssignment{}} {
		var n int64
		require.NoError(t, db.Model(model).Count(&n).Error)
		require.Zero(t, n, "%T", model)
	}
	table, err := seededStore(s, 22, 21, 21).EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)
	require.Equal(t, []int{22, 21, 21}, countGroups(table.Groups, 3))
}
