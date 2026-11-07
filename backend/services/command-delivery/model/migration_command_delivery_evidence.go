// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// Like the migrations beside it, this file imports none of the service's own packages. A
// migration that can reach a live model is a migration whose behaviour changes when that
// model does.
import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewCommandDeliveryEvidenceSchema adds the columns that record delivery evidence and the HTTP
// pull channel's options:
//
//   - commands.delivered_time and commands.delivery_evidence: when the device was first observed
//     to have the command, and by what (DEVICE_ACK, DEVICE_RESPONSE). NULL until then.
//   - commands.delivery_mode: NULL for an ordinary two-way command, ONEWAY for one the device
//     acknowledges and does not answer.
//   - commands.max_attempts: how many times the pull channel hands the command out before it
//     gives up on an acknowledgement. NULL is the platform default.
//   - commands.delivery_attempts: how many times a pull has handed the command out. NULL for a
//     command no pull ever handed out.
//
// All five are NULLABLE with no default. NULL is the true reading of every existing row, and a
// replica of the previous version that does not know the columns neither reads nor writes them,
// so a rolling upgrade has old and new pods sharing the table safely. A constant default would
// be stored in the catalog (attmissingval) rather than in the rows, which a logical dump and
// restore loses.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure; every
// statement is IF NOT EXISTS. No snapshot struct is declared: a column added to an existing
// table is literal, schema-qualified DDL, for the reason NewCommandDispatchNonceSchema gives.
func NewCommandDeliveryEvidenceSchema() *gormigrate.Migration {
	statements := []string{
		`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS delivered_time timestamptz;`,
		`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS delivery_evidence varchar(32);`,
		`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS delivery_mode varchar(16);`,
		`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS max_attempts integer;`,
		`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS delivery_attempts integer;`,
	}
	return &gormigrate.Migration{
		ID: "20261114120100",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range statements {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		// Nothing is dropped: dropping a column destroys data, and a rollback undoes a failed
		// forward migration rather than erasing what ran after it.
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
