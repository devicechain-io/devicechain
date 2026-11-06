// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

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
	require.NoError(t, db.AutoMigrate(&Tenant{}, &TenantPurge{}, &TenantPurgeStore{},
		&PartitionConfig{}, &VPartitionAssignment{}, &TenantVPartitionOverride{}))
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

	liveTenants(t, s, "big-tenant", "other")
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
	liveTenants(t, s, "acme")
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

func liveTenants(t *testing.T, s *Store, tokens ...string) {
	t.Helper()
	for _, tok := range tokens {
		require.NoError(t, s.CreateTenant(context.Background(), &Tenant{Token: tok}))
	}
}

func TestAPinIsRefusedForATokenThatIsNotALiveTenant(t *testing.T) {
	s, _ := newPlacementStore(t)
	ctx := context.Background()
	_, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)

	require.Error(t, s.SetTenantOverride(ctx, "ghost", 3), "no such tenant")

	liveTenants(t, s, "going")
	tn, err := s.TenantByToken(ctx, "going")
	require.NoError(t, err)
	require.NoError(t, s.BeginTenantPurge(ctx, tn, time.Now().UTC().Truncate(time.Second)))
	require.Error(t, s.SetTenantOverride(ctx, "going", 3), "a tenant being deleted is not live")

	table, err := s.Placement(ctx)
	require.NoError(t, err)
	require.Empty(t, table.Overrides)
	require.Equal(t, uint64(0), table.Generation, "a refused pin must not bump the generation")
}

// 🔑 F1: a pin is retained through the purge and cleared at token release, so a successor that
// takes the token is placed by the hash, not by its predecessor's pin.
func TestACompletedPurgeClearsThePinAndTheSuccessorIsHashed(t *testing.T) {
	s := newPurgeTestStore(t)
	ctx := context.Background()
	_, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)

	tenant, epoch := beginPurge(t, s, "acme")
	// A pin set before the cut survives it...
	require.NoError(t, s.sys(ctx).Create(&TenantVPartitionOverride{
		TenantToken: "acme", VPartition: (partition.HashedVPartition("dc", "acme", partition.DefaultP) + 1) % partition.DefaultP,
	}).Error)
	before, err := s.Placement(ctx)
	require.NoError(t, err)
	require.Contains(t, before.Overrides, "acme", "the pin is retained through the purge")

	rec, err := s.EnsurePurgeRecord(ctx, "acme", epoch)
	require.NoError(t, err)
	require.NoError(t, s.CompleteTenantPurge(ctx, tenant, rec, 1, time.Now().UTC()))

	// ...and is gone with the token: the successor is hashed, and the generation moved.
	require.NoError(t, s.CreateTenant(ctx, &Tenant{Token: "acme"}))
	after, err := s.Placement(ctx)
	require.NoError(t, err)
	require.NotContains(t, after.Overrides, "acme")
	require.Equal(t, partition.HashedVPartition("dc", "acme", partition.DefaultP), after.VPartition("dc", "acme"))
	require.Equal(t, before.Generation+1, after.Generation)
}

func TestACompletedPurgeWithNoPinLeavesTheGenerationAlone(t *testing.T) {
	s := newPurgeTestStore(t)
	ctx := context.Background()
	_, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)
	tenant, epoch := beginPurge(t, s, "plain")
	rec, err := s.EnsurePurgeRecord(ctx, "plain", epoch)
	require.NoError(t, err)
	require.NoError(t, s.CompleteTenantPurge(ctx, tenant, rec, 1, time.Now().UTC()))
	after, err := s.Placement(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(0), after.Generation)
}

// F4b: the pin limit is exact. 4095 others leave room for one more; 4096 do not; re-pinning a
// tenant that already holds one is allowed at the limit.
func TestThePinLimitIsExact(t *testing.T) {
	s, db := newPlacementStore(t)
	ctx := context.Background()
	_, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)

	rows := make([]TenantVPartitionOverride, 0, partition.MaxOverrides)
	for i := 0; i < partition.MaxOverrides-1; i++ {
		rows = append(rows, TenantVPartitionOverride{TenantToken: fmt.Sprintf("p%d", i), VPartition: 1})
	}
	require.NoError(t, db.CreateInBatches(&rows, 500).Error)

	liveTenants(t, s, "last", "over")
	require.NoError(t, s.SetTenantOverride(ctx, "last", 2), "the 4096th pin fits")
	require.Error(t, s.SetTenantOverride(ctx, "over", 2), "the 4097th does not")
	require.NoError(t, s.SetTenantOverride(ctx, "last", 3), "re-pinning at the limit is not a new pin")
	table, err := s.Placement(ctx)
	require.NoError(t, err)
	require.Len(t, table.Overrides, partition.MaxOverrides)
}

// F4c: a stored placement that does not validate is ErrPlacementInvalid, which the service
// treats as non-fatal (only a changed P stops it).
func TestAnUnusableStoredPlacementIsNamedAsSuchAndIsNotAChangedP(t *testing.T) {
	s, db := newPlacementStore(t)
	ctx := context.Background()
	_, err := s.EnsurePlacement(ctx, partition.DefaultP)
	require.NoError(t, err)
	require.NoError(t, db.Create(&TenantVPartitionOverride{TenantToken: "bad", VPartition: partition.DefaultP + 5}).Error)

	_, err = s.EnsurePlacement(ctx, partition.DefaultP)
	require.ErrorIs(t, err, ErrPlacementInvalid)
	require.False(t, errors.Is(err, partition.ErrPChanged))
}
