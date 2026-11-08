// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewInstalledPositionSchema adds where a FIXED asset was installed to devices and assets, and
// a link from an area to the geofence that bounds it.
//
// A streetlight, a parking spot or a cabinet does not report GPS: its position is installation
// data, and until now the only position the platform held was a reported fix, so a city's fixed
// assets had no location at all.
//
//  1. devices and assets each gain installed_lat, installed_lon, installed_elev,
//     installed_accuracy, installed_source and installed_updated_at. The coordinate columns carry
//     the widths latest_locations does (decimal(10,8), decimal(11,8), decimal(12,4)) so an
//     installed position and a reported fix of the same place round-trip at the same precision.
//  2. areas gains boundary_fence_token: the token of the geofence that bounds the area. It names
//     a fence by token and carries no foreign key, like the other same-service references here;
//     the reader answers loudly when the named fence is gone.
//
// 🔴 NO CELL COLUMN. The spatial index lands with the spatial queries; adding the column here
// would ship a column nothing writes and a reader would take its NULL for "outside every cell".
//
// Every column is nullable with NO default: an existing row has no installed position and NULL
// says exactly that, a default would be stored in the catalog rather than in the rows (see
// migration_device_disabled.go), and a replica of the previous release writing the old shape
// keeps succeeding. The migration touches no rows, so it needs no snapshot struct; it names its
// tables literally rather than through the live models for the reason migrations.go gives, and
// every statement is ADD COLUMN IF NOT EXISTS, so a replay after a partial first pass is a no-op.
func NewInstalledPositionSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261112120000",
		Migrate: func(tx *gorm.DB) error {
			for _, table := range []string{`"device-management"."devices"`, `"device-management"."assets"`} {
				if err := tx.Exec(`ALTER TABLE ` + table +
					` ADD COLUMN IF NOT EXISTS installed_lat numeric(10,8),` +
					` ADD COLUMN IF NOT EXISTS installed_lon numeric(11,8),` +
					` ADD COLUMN IF NOT EXISTS installed_elev numeric(12,4),` +
					` ADD COLUMN IF NOT EXISTS installed_accuracy numeric(12,4),` +
					` ADD COLUMN IF NOT EXISTS installed_source text,` +
					` ADD COLUMN IF NOT EXISTS installed_updated_at timestamptz;`).Error; err != nil {
					return err
				}
			}
			return tx.Exec(`ALTER TABLE "device-management"."areas" ` +
				`ADD COLUMN IF NOT EXISTS boundary_fence_token text;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return errors.New("the installed-position migration cannot be rolled back: dropping the columns " +
				"would forget positions an operator surveyed")
		},
	}
}
