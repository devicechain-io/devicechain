// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewIdentityNamesNullMigration converts every stored empty first or last name to NULL.
//
// The first_name / last_name columns have always been nullable, but the model held them as
// a bare string, so every identity written without a name — the bootstrap superuser, an
// admin-created identity with no names, a cleared profile — stored the empty string rather than NULL. The
// read path hid that by rendering the empty string as null. The model now holds sql.NullString and a
// clear writes NULL, so this migration brings the rows already written into the same
// shape: "no name" is NULL, and there is one spelling of it.
//
// DML only — the schema does not move, so migration-diff cannot see this migration at all
// (it compares pg_dump --schema-only). migration_identity_names_null_test.go asserts the
// rows. Re-runnable: a second run matches nothing.
func NewIdentityNamesNullMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260926120100",
		Migrate: func(db *gorm.DB) error {
			return db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Exec("UPDATE iam_identities SET first_name = NULL WHERE first_name = ''").Error; err != nil {
					return err
				}
				return tx.Exec("UPDATE iam_identities SET last_name = NULL WHERE last_name = ''").Error
			})
		},
		Rollback: func(tx *gorm.DB) error {
			// Unimplemented must fail loudly. Which NULLs were the empty string before this ran is not
			// recorded, and writing the empty string into every NULL would invent a state some rows never
			// had. The previous release reads NULL and the empty string alike, so nothing needs undoing.
			return errors.New("the identity-names migration cannot be rolled back: which NULL names were empty strings before it ran is not recorded, and the previous release reads both the same")
		},
	}
}
