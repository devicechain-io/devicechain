// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// NewInitialSchema creates the initial schema migration for this functional
// area. Commands persist to a plain relational table (NOT a hypertable).
func NewInitialSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20220701000000",
		Migrate: func(tx *gorm.DB) error {
			// A persisted, lifecycle-tracked command to a device (ADR-012 #4 /
			// ThingsBoard §2.6).
			type Command struct {
				gorm.Model
				rdb.TenantScoped
				rdb.TokenReference
				rdb.MetadataEntity

				DeviceToken     string `gorm:"index;not null;size:128"`
				Name            string `gorm:"not null;size:128"`
				Payload         *datatypes.JSON
				Status          string `gorm:"index;not null;size:32"`
				QueuedTime      time.Time
				SentTime        sql.NullTime
				DeliveredTime   sql.NullTime
				RespondedTime   sql.NullTime
				ExpiresAt       sql.NullTime
				ResponsePayload *datatypes.JSON
				Error           sql.NullString
			}

			return tx.AutoMigrate(&Command{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("commands")
		},
	}
}
