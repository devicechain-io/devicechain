// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// NewNotificationChannelSchema adds the notification_channels table (ADR-017): a
// tenant's configured delivery endpoints (SMTP/webhook). The snapshot is inline so
// the migration is frozen against future model changes; it must produce the same
// columns as model.NotificationChannel.
//
// The channel's write-only delivery secret is NOT a column here: as of ADR-059 (S3)
// it lives in the envelope-encrypted secret store (the secrets table, added by
// secrets.NewSecretStoreSchema in this service's migration slice), keyed by the
// channel's tenant-scoped handle. This is a decisive pre-GA cutover — the earlier
// reversible plaintext column is simply gone, so a fresh bring-up never creates it.
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
				Enabled     bool `gorm:"not null;default:true"`
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
