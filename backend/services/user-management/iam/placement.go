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
// partition.ErrPChanged on purpose: a broken table is an operator's repair, and since nothing
// routes by it yet it must not stop the service; a changed P does.
var ErrPlacementInvalid = errors.New("iam: the stored placement is not a usable table")

// ErrPlacementNotInitialized is returned by reads of a placement nobody has set up. A reader
// never gets an empty table in its place: an empty table routes every tenant to nowhere.
var ErrPlacementNotInitialized = errors.New("iam: the placement table has not been initialized (EnsurePlacement has not run)")

// EnsurePlacement initializes the placement on first use and verifies it on every later one,
// returning the table.
//
// 🔴 IT IS THE ONLY PLACE P IS WRITTEN, and it writes it once. A recorded P that differs from
// the one presented is partition.ErrPChanged, never an update: the caller (the service's start)
// must fail, because carrying on with a different P would rehash every tenant's lane.
//
// It is safe to run on several replicas at once: every insert is DoNothing, and the row that won
// is re-read and compared before anything is believed.
func (s *Store) EnsurePlacement(ctx context.Context, p int) (partition.Table, error) {
	if p < 1 || p > partition.MaxP {
		return partition.Table{}, fmt.Errorf("iam: P must be between 1 and %d, got %d", partition.MaxP, p)
	}
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&PartitionConfig{ID: partitionConfigID, P: p}).Error; err != nil {
			return err
		}
		var cfg PartitionConfig
		if err := tx.First(&cfg, partitionConfigID).Error; err != nil {
			return err
		}
		if err := partition.CheckP(cfg.P, p); err != nil {
			return err
		}
		rows := make([]VPartitionAssignment, 0, p)
		for vp := 0; vp < p; vp++ {
			rows = append(rows, VPartitionAssignment{VPartition: vp})
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
	})
	if err != nil {
		return partition.Table{}, err
	}
	return s.Placement(ctx)
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
// cutover, and bumps the generation; it does not move any data (nothing routes by it yet).
func (s *Store) AssignVPartition(ctx context.Context, vpartition, shardGroup int) error {
	if shardGroup < 0 {
		return fmt.Errorf("iam: shard group %d is negative", shardGroup)
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
