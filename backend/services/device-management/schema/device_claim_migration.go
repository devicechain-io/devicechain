// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDeviceClaimSchema adds the device_claims table (ADR-012): a device's single
// possession claim — the one-time secret an eventual owner presents to assign the
// device to a customer. It is a post-initial migration, so it is added here rather
// than folded into the frozen initial schema. The snapshot is defined inline so
// the migration is frozen against future model changes; it must produce the same
// columns as model.DeviceClaim.
func NewDeviceClaimSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260625160000",
		Migrate: func(tx *gorm.DB) error {
			// One claim per device: the device_id index is unique. Tenant isolation
			// is enforced by the app-layer tenant scope on the embedded TenantScoped.
			type DeviceClaim struct {
				gorm.Model
				rdb.TenantScoped

				DeviceId            uint   `gorm:"not null;uniqueIndex:idx_device_claim_device"`
				ClaimSecret         string `gorm:"not null;size:256"`
				Status              string `gorm:"not null;size:32"`
				ExpiresAt           sql.NullTime
				ClaimedTime         sql.NullTime
				ClaimedByCustomerId sql.NullInt64
			}
			return tx.AutoMigrate(&DeviceClaim{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("device_claims")
		},
	}
}
