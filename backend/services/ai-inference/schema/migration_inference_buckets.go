// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// inferenceBucketSnapshot belongs to this migration, never the runtime limiter.
type inferenceBucketSnapshot struct {
	TenantId  string  `gorm:"primaryKey"`
	Tokens    float64 `gorm:"type:double precision;not null"`
	UpdatedAt float64 `gorm:"type:double precision;not null"`
	Revision  int64   `gorm:"not null"`
}

func (inferenceBucketSnapshot) TableName() string { return "ai_inference_buckets" }

func NewInferenceBucketsMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261115120000",
		Migrate: func(tx *gorm.DB) error {
			// Create only when absent; replay must preserve already-spent budget.
			if tx.Migrator().HasTable(&inferenceBucketSnapshot{}) {
				return nil
			}
			return tx.Migrator().CreateTable(&inferenceBucketSnapshot{})
		},
		Rollback: func(*gorm.DB) error {
			return errors.New("inference admission state cannot be rolled back without resetting tenant budgets")
		},
	}
}
