// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// Frozen migration snapshot. Never replace this with the live topology model.
type detectStreamTopologySchema struct {
	ID            string    `gorm:"column:id;type:varchar(32);primaryKey;check:detect_stream_topology_singleton,id = 'singleton'"`
	BaseSuffix    string    `gorm:"column:base_suffix;type:varchar(128);not null"`
	GroupCount    int       `gorm:"column:group_count;type:integer;not null"`
	CreatedAt     time.Time `gorm:"column:created_at;not null"`
	FormatVersion int       `gorm:"column:format_version;type:integer;not null"`
}

func (detectStreamTopologySchema) TableName() string { return "detect_stream_topology" }

// NewDetectStreamTopology appends metadata only; it neither creates checkpoint
// partitions nor converts existing engine state. AutoMigrate tolerates replay.
func NewDetectStreamTopology() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261115000000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&detectStreamTopologySchema{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&detectStreamTopologySchema{})
		},
	}
}
