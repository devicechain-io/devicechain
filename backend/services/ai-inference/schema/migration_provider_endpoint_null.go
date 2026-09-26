// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewProviderEndpointNullMigration converts every stored empty provider endpoint to NULL.
//
// The endpoint column has always been nullable, but the model held it as a bare string, so
// every provider registered without an override stored the empty string rather than NULL — "use the
// kind's built-in default" had two spellings, and the read path hid the second by
// rendering the empty string as null. The model now holds sql.NullString and a clear writes NULL, so this
// brings the rows already written into the same shape.
//
// DML only: the schema does not move, so migration-diff cannot see this migration at all.
// migration_provider_endpoint_null_test.go asserts the rows. Re-runnable: a second run
// matches nothing. The bare table name resolves through search_path, as the pinned
// TableName on the runtime model does.
func NewProviderEndpointNullMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260926120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec("UPDATE ai_providers SET endpoint = NULL WHERE endpoint = ''").Error
		},
		Rollback: func(tx *gorm.DB) error {
			// Unimplemented must fail loudly: which NULL endpoints were the empty string before this ran
			// is not recorded, and the previous release reads both as "the default".
			return errors.New("the provider-endpoint migration cannot be rolled back: which NULL endpoints were empty strings before it ran is not recorded, and the previous release reads both the same")
		},
	}
}
