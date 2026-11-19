// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/partition"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PartitionConfig is the one row recording the instance's vpartition count P and the generation
// of its placement. Instance-global: no tenant column.
type PartitionConfig struct {
	ID         uint `gorm:"primarykey;autoIncrement:false"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
	P          int   `gorm:"not null"`
	Generation int64 `gorm:"not null;default:0"`
}

func (PartitionConfig) TableName() string { return "iam_partition_config" }

// VPartitionAssignment says which shard group owns a vpartition.
type VPartitionAssignment struct {
	VPartition int `gorm:"column:vpartition;primarykey;autoIncrement:false"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ShardGroup int `gorm:"not null;default:0"`
}

func (VPartitionAssignment) TableName() string { return "iam_vpartition_assignments" }

// TenantVPartitionOverride pins a tenant token to a vpartition.
//
// The column is tenant_token on purpose, not tenant or tenant_id: those names make a table
// tenant-scoped to the isolation callback and swept by the tenant purge, and a pin has to
// outlive the purge (see the exemption in core/tenantpurge).
type TenantVPartitionOverride struct {
	TenantToken string `gorm:"primarykey;size:128"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	VPartition  int `gorm:"column:vpartition;not null"`
}

func (TenantVPartitionOverride) TableName() string { return "iam_tenant_vpartition_overrides" }

const partitionConfigID = 1

// ErrPlacementInvalid wraps every refusal of a stored placement that is not a usable table (an
// assignment outside P, a hole, a pin out of range, too many pins). It is distinct from
// partition.ErrPChanged on purpose: a broken table requires operator repair while the identity
// provider stays up. Sharded stream routing needs a usable mirror; a changed P stops startup.
var ErrPlacementInvalid = errors.New("iam: the stored placement is not a usable table")

// ErrPlacementNotInitialized is returned by reads of a placement nobody has set up. A reader
// never gets an empty table in its place: an empty table routes every tenant to nowhere.
var ErrPlacementNotInitialized = errors.New("iam: the placement table has not been initialized (EnsurePlacement has not run)")

// ErrShardGroupOutsideNatsCount identifies an assignment that stream routing cannot use.
var ErrShardGroupOutsideNatsCount = errors.New("iam: shard group is outside nats.shardGroups.count")

// EnsurePlacement initializes the placement on first use and verifies it on every later one,
// returning the table.
//
// 🔴 IT IS THE ONLY PLACE P IS WRITTEN, and it writes it once. A recorded P that differs from
// the one presented is partition.ErrPChanged, never an update: the caller (the service's start)
// must fail, because carrying on with a different P would rehash every tenant's lane.
//
// 🔴 THE ASSIGNMENT ROWS ARE WRITTEN ONLY BY THE TRANSACTION THAT CREATED THE CONFIG ROW. That
// row is the placement's authority: the transaction whose insert of it actually inserted it writes
// all P assignments, seeded from nats.shardGroups.seed when the instance configuration has one
// (seedGroups) and in group zero otherwise, and every later start writes nothing. So a seed
// applies exactly once in an instance's life, before any event exists, and can never move a
// tenant: on an instance whose table exists, with or without a seed key, the table is read as it
// stands, and a hole in it is refused (ErrPlacementInvalid, from Placement) rather than filled
// from whatever the configuration says now. A restored table wins the same way, since its config
// row exists.
//
// It is safe to run on several replicas at once: the config insert is DoNothing, so of two
// replicas racing the first start exactly one inserts it (the other waits on that row's unique
// key, then inserts nothing) and only that one writes the assignments; the row that won is
// re-read and compared before anything is believed.
func (s *Store) EnsurePlacement(ctx context.Context, p int) (partition.Table, error) {
	if p < 1 || p > partition.MaxP {
		return partition.Table{}, fmt.Errorf("iam: P must be between 1 and %d, got %d", partition.MaxP, p)
	}
	var seed []int
	if cfg := s.NatsShardGroups; cfg != nil && cfg.Seed != nil {
		seed = cfg.Seed.Weights
	}
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&PartitionConfig{ID: partitionConfigID, P: p})
		if res.Error != nil {
			return res.Error
		}
		created := res.RowsAffected == 1
		var cfg PartitionConfig
		if err := tx.First(&cfg, partitionConfigID).Error; err != nil {
			return err
		}
		if err := partition.CheckP(cfg.P, p); err != nil {
			return err
		}
		if !created {
			return nil
		}
		groups, err := seedGroups(p, seed)
		if err != nil {
			return err
		}
		rows := make([]VPartitionAssignment, 0, p)
		for vp := 0; vp < p; vp++ {
			rows = append(rows, VPartitionAssignment{VPartition: vp, ShardGroup: groups[vp]})
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
	})
	if err != nil {
		return partition.Table{}, err
	}
	return s.Placement(ctx)
}

// ErrSeedInvalid refuses a seed that does not place exactly the table it is asked to seed.
var ErrSeedInvalid = errors.New("iam: the placement seed does not fit the placement table")

// seedGroups is the shard group of every vpartition of a P-entry table seeded with the given
// weights, interleaved: vpartition order carries no meaning, so neighbours are spread rather than
// laid out in runs. No weights is the unseeded table, every vpartition in group zero.
//
// It is smooth weighted round-robin: each step adds every group's weight to its credit, takes the
// group with the most credit (the lowest group on a tie) and charges it the total. Over P steps,
// with weights summing to P, each group is taken exactly its weight's number of times.
//
// 🔴 THE ORDER IS PART OF WHAT A SEEDED INSTANCE IS. It is computed once per instance and stored,
// so changing it would not move an existing tenant; it would make two installs of the same level
// lay out differently. TestSeedGroupsLayoutIsFixed pins it.
func seedGroups(p int, weights []int) ([]int, error) {
	groups := make([]int, p)
	if len(weights) == 0 {
		return groups, nil
	}
	total := 0
	for g, w := range weights {
		if w < 1 {
			return nil, fmt.Errorf("%w: group %d has weight %d", ErrSeedInvalid, g, w)
		}
		total += w
	}
	if total != p {
		return nil, fmt.Errorf("%w: the weights sum to %d and the table has %d vpartitions", ErrSeedInvalid, total, p)
	}
	credit := make([]int, len(weights))
	for vp := range groups {
		best := 0
		for g, w := range weights {
			credit[g] += w
			if credit[g] > credit[best] {
				best = g
			}
		}
		credit[best] -= total
		groups[vp] = best
	}
	return groups, nil
}

// Placement reads the whole placement table as one consistent value.
func (s *Store) Placement(ctx context.Context) (partition.Table, error) {
	var table partition.Table
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		// 🔴 THE CONFIG ROW IS THE PLACEMENT'S LOCK. Under Postgres' default isolation the three
		// reads below are three snapshots, so a change committing between them would hand back a
		// table that never existed (new assignments under the old generation). Every change takes
		// this row FOR UPDATE first (changePlacement), so reading it FOR SHARE waits out a change
		// in flight and keeps one from starting until this read is done.
		var cfg PartitionConfig
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&cfg, partitionConfigID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPlacementNotInitialized
			}
			return err
		}
		var assignments []VPartitionAssignment
		if err := tx.Order("vpartition").Find(&assignments).Error; err != nil {
			return err
		}
		var overrides []TenantVPartitionOverride
		if err := tx.Find(&overrides).Error; err != nil {
			return err
		}
		table = partition.NewTable(cfg.P)
		table.Generation = uint64(cfg.Generation)
		seen := make(map[int]bool, len(assignments))
		for _, a := range assignments {
			if a.VPartition < 0 || a.VPartition >= cfg.P {
				return fmt.Errorf("%w: an assignment for vpartition %d, outside P = %d", ErrPlacementInvalid, a.VPartition, cfg.P)
			}
			table.Groups[a.VPartition] = a.ShardGroup
			seen[a.VPartition] = true
		}
		if len(seen) != cfg.P {
			return fmt.Errorf("%w: %d of its %d vpartitions are assigned", ErrPlacementInvalid, len(seen), cfg.P)
		}
		if len(overrides) > 0 {
			table.Overrides = make(map[string]int, len(overrides))
		}
		for _, o := range overrides {
			table.Overrides[o.TenantToken] = o.VPartition
		}
		if err := table.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrPlacementInvalid, err)
		}
		return nil
	})
	if err != nil {
		return partition.Table{}, err
	}
	return table, nil
}

// AssignVPartition moves a vpartition to a shard group. It is a placement CHANGE, the start of a
// cutover, and bumps the generation; it does not move any data. The table drives database
// shard routing and, when configured, NATS shard-group stream routing via its mirror.
func (s *Store) AssignVPartition(ctx context.Context, vpartition, shardGroup int) error {
	if shardGroup < 0 {
		return fmt.Errorf("iam: shard group %d is negative", shardGroup)
	}
	if cfg := s.NatsShardGroups; cfg != nil && shardGroup >= cfg.Count {
		return fmt.Errorf("%w: group %d, count %d; nats.shardGroups.count must be raised first",
			ErrShardGroupOutsideNatsCount, shardGroup, cfg.Count)
	}
	return s.changePlacement(ctx, func(tx *gorm.DB, cfg PartitionConfig) error {
		if vpartition < 0 || vpartition >= cfg.P {
			return fmt.Errorf("iam: vpartition %d is outside [0, %d)", vpartition, cfg.P)
		}
		res := tx.Model(&VPartitionAssignment{}).Where("vpartition = ?", vpartition).
			Update("shard_group", shardGroup)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("iam: vpartition %d has no assignment row", vpartition)
		}
		return nil
	})
}

// SetTenantOverride pins a tenant token to a vpartition, replacing any earlier pin.
func (s *Store) SetTenantOverride(ctx context.Context, tenantToken string, vpartition int) error {
	if err := core.ValidateToken(tenantToken); err != nil {
		return err
	}
	// 🔴 A PIN IS FOR A LIVE TENANT ONLY, AND THE TENANT ROW IS LOCKED BEFORE THE CONFIG ROW. The
	// purge's completion deletes the tenant row, then the pin, then bumps the config row (tenant
	// first, config second); taking the locks here in the same order is what keeps the two from
	// deadlocking, and holding the tenant row FOR SHARE is what stops a purge from completing
	// between the check and the insert and leaving a pin on a token that was just released.
	live := func(tx *gorm.DB) error {
		// Pluck, not Count: Postgres refuses FOR SHARE with an aggregate.
		var ids []uint
		if err := tx.Model(&Tenant{}).Clauses(clause.Locking{Strength: "SHARE"}).
			Where("token = ? AND purge_state = ?", tenantToken, PurgeActive).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) != 1 {
			return fmt.Errorf("iam: %q is not a live tenant; a pin is for a tenant that exists and is not being deleted", tenantToken)
		}
		return nil
	}
	return s.changePlacementAfter(ctx, live, func(tx *gorm.DB, cfg PartitionConfig) error {
		if vpartition < 0 || vpartition >= cfg.P {
			return fmt.Errorf("iam: vpartition %d is outside [0, %d)", vpartition, cfg.P)
		}
		var existing int64
		if err := tx.Model(&TenantVPartitionOverride{}).Where("tenant_token <> ?", tenantToken).
			Count(&existing).Error; err != nil {
			return err
		}
		if existing >= partition.MaxOverrides {
			return fmt.Errorf("iam: %d tenants are already pinned; the limit is %d", existing, partition.MaxOverrides)
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "tenant_token"}},
			DoUpdates: clause.AssignmentColumns([]string{"vpartition", "updated_at"}),
		}).Create(&TenantVPartitionOverride{TenantToken: tenantToken, VPartition: vpartition}).Error
	})
}

// ClearTenantOverride removes a tenant's pin, returning it to its hashed vpartition.
func (s *Store) ClearTenantOverride(ctx context.Context, tenantToken string) error {
	return s.changePlacement(ctx, func(tx *gorm.DB, _ PartitionConfig) error {
		return tx.Where("tenant_token = ?", tenantToken).Delete(&TenantVPartitionOverride{}).Error
	})
}

// changePlacement runs one change and bumps the generation in the same transaction, so a reader
// never sees a changed table under the old generation.
//
// It takes the config row FOR UPDATE before anything else, which serializes every change and every
// read of the placement: two overrides cannot both pass the count check at the limit and
// together exceed it, and a reader never straddles a change.
func (s *Store) changePlacement(ctx context.Context, change func(tx *gorm.DB, cfg PartitionConfig) error) error {
	return s.changePlacementAfter(ctx, nil, change)
}

// changePlacementAfter is changePlacement with a step that runs first, before the config row is
// locked, for the locks that must be taken earlier in the lock order.
func (s *Store) changePlacementAfter(ctx context.Context, first func(tx *gorm.DB) error,
	change func(tx *gorm.DB, cfg PartitionConfig) error) error {
	return s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		if first != nil {
			if err := first(tx); err != nil {
				return err
			}
		}
		var cfg PartitionConfig
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cfg, partitionConfigID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPlacementNotInitialized
			}
			return err
		}
		if err := change(tx, cfg); err != nil {
			return err
		}
		// Only the generation column: P is never in this statement.
		return tx.Model(&PartitionConfig{}).Where("id = ?", partitionConfigID).
			UpdateColumn("generation", gorm.Expr("generation + 1")).Error
	})
}
