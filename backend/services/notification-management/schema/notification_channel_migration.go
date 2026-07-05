// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// NewNotificationChannelSchema adds the notification_channels table (ADR-017): a
// tenant's configured delivery endpoints (SMTP/webhook). The snapshot is inline so
// the migration is frozen against future model changes; it must produce the same
// columns as model.NotificationChannel.
func NewNotificationChannelSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260705120000",
		Migrate: func(tx *gorm.DB) error {
			type NotificationChannel struct {
				gorm.Model
				rdb.TenantScoped
				rdb.TokenReference
				rdb.NamedEntity
				rdb.MetadataEntity

				ChannelType string `gorm:"not null;size:32;index"`
				Config      *datatypes.JSON
				// Secret is the write-only delivery secret (SMTP password, bearer
				// token); stored reversibly (the sender presents it at delivery), kept
				// out of the read API at the resolver layer.
				Secret  sql.NullString `gorm:"size:4096"`
				Enabled bool           `gorm:"not null;default:true"`
			}
			if err := tx.AutoMigrate(&NotificationChannel{}); err != nil {
				return err
			}
			// ADR-042 P1: per-tenant partial unique index on token.
			return rdb.CreateTenantTokenIndex(tx, &NotificationChannel{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("notification_channels")
		},
	}
}
