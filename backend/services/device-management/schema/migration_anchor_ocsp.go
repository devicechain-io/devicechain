// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewAnchorOcspSchema adds the per-anchor OCSP settings to device_trust_anchors:
//
//   - ocsp_mode: OFF, ENFORCE_KNOWN or REQUIRE_CURRENT. NULL reads as OFF, which is every anchor
//     that predates this migration, so an anchor that does nothing new behaves exactly as before.
//   - ocsp_responder_url: the operator-set responder that replaces each certificate's own
//     Authority Information Access entry. NULL means "use the certificate's".
//
// # Nullable, no default, no backfill
//
// Both columns are NULL for every existing row, which is the truth: no anchor has asked for OCSP.
// An old replica that does not know the columns keeps reading and writing the rows it knows (a
// rolling upgrade overlaps old and new pods), and a default would put a value in the catalog's
// attmissingval (see migration_profile_location_declaration.go).
//
// # Re-runnability
//
// ADD COLUMN IF NOT EXISTS, so a replay after a partial failure is a no-op. DDL only: no row is
// written. The columns hang off an existing tenant-scoped table, so the catalog-driven tenant
// purge needs no new classification.
func NewAnchorOcspSchema() *gormigrate.Migration {
	const anchors = `"device-management"."device_trust_anchors"`
	return &gormigrate.Migration{
		ID: "20261113100000",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range []string{
				`ALTER TABLE ` + anchors + ` ADD COLUMN IF NOT EXISTS ocsp_mode varchar(32);`,
				`ALTER TABLE ` + anchors + ` ADD COLUMN IF NOT EXISTS ocsp_responder_url varchar(2048);`,
			} {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return errors.New("the trust-anchor OCSP migration cannot be rolled back: a column drop is expand/contract and belongs to a later major")
		},
	}
}
