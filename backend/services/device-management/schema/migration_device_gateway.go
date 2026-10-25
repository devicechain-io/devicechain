// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDeviceGatewaySchema adds devices.gateway_id: the id of the gateway device that fronts the
// row, or NULL for a device that is not behind one. It is the single authority the resolver
// checks when a gateway's broker login publishes telemetry for a child (a publish for a device
// whose gateway_id does not name that gateway is refused as NotGatewayChild).
//
// # Nullable, no default, no backfill
//
// NULL has to mean "no gateway", because it is what every existing device holds after this
// runs and what a reader that has not been told about the column sees: a cached device encoded
// by a replica of the previous version during a rolling upgrade decodes with it nil, and the
// safe reading of that is "refuse the gateway's publish". Nullable with no default needs no
// backfill (no device has ever been behind a gateway) and avoids the attmissingval catalog
// state migration_profile_location_declaration.go records a logical dump/restore loses.
//
// # No foreign key
//
// A plain reference, like the other same-service references in this schema: DeleteDevice
// refuses a gateway that still has children with a sentence that names the rule, and the
// tenant purge deletes a tenant's devices without ordering them parent-first.
//
// # The index
//
// (tenant_id, gateway_id), partial on the two conditions the read carries (the column is set,
// the row is not soft-deleted): a gateway's children are one index range, and the index stays
// as small as the set of devices that are behind a gateway.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure: both
// statements are IF NOT EXISTS. No snapshot struct is needed (it touches one column and one
// index and no rows), and it names the table literally rather than through the live model for
// the reason migrations.go gives. Not CREATE INDEX CONCURRENTLY, for the reason
// NewDeviceDisabledIndexSchema gives.
func NewDeviceGatewaySchema() *gormigrate.Migration {
	const devices = `"device-management"."devices"`

	return &gormigrate.Migration{
		ID: "20261022120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`ALTER TABLE ` + devices +
				` ADD COLUMN IF NOT EXISTS gateway_id bigint;`).Error; err != nil {
				return err
			}
			return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_devices_gateway ON ` + devices +
				` (tenant_id, gateway_id) WHERE gateway_id IS NOT NULL AND deleted_at IS NULL;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec(`DROP INDEX IF EXISTS "device-management".idx_devices_gateway;`).Error; err != nil {
				return err
			}
			return tx.Exec(`ALTER TABLE ` + devices + ` DROP COLUMN IF EXISTS gateway_id;`).Error
		},
	}
}
