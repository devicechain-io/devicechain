// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewRosterSchema adds the two durable read-models the dead-man roster needs (ADR-051 slice
// 4c-2b): DeviceRoster (the devices expected to report, so absence can be armed for a device
// that never has) and ProfileActive (which published version is active per stable profile
// token, plus its publish time — the grace-period base). Both are rebuilt-from at startup so
// the arming survives a restart independent of the finite-retention fact streams, exactly like
// the rule projection. This slice lands and maintains them; the engine arming that reads them
// is slice 4c-2b-2.
func NewRosterSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260711000000",
		Migrate: func(tx *gorm.DB) error {
			type DeviceRoster struct {
				Tenant        string    `gorm:"primaryKey;size:256;not null"`
				DeviceToken   string    `gorm:"primaryKey;size:256;not null"`
				ProfileToken  string    `gorm:"size:256;not null;index"`
				ExpectedSince time.Time `gorm:"not null"`
				Deleted       bool      `gorm:"not null"`
				LastEventAt   time.Time `gorm:"not null"`
				UpdatedAt     time.Time
			}
			type ProfileActive struct {
				Tenant             string    `gorm:"primaryKey;size:256;not null"`
				ProfileToken       string    `gorm:"primaryKey;size:256;not null"`
				ActiveVersionToken string    `gorm:"size:256;not null"`
				PublishedAt        time.Time `gorm:"not null"`
				UpdatedAt          time.Time
			}
			return tx.AutoMigrate(&DeviceRoster{}, &ProfileActive{})
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable("profile_actives"); err != nil {
				return err
			}
			return tx.Migrator().DropTable("device_rosters")
		},
	}
}
