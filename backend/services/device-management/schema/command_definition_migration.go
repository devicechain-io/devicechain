// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// NewCommandDefinitionSchema adds the command_definitions table (ADR-043): typed
// commands declared on a device profile, structurally parallel to
// metric_definitions. The parameter schema is a JSONB document read and validated
// as a whole (see model.CommandDefinition), so it is a single column rather than a
// child table. The snapshot is defined inline so the migration is frozen against
// future model changes; it must produce the same columns as
// model.CommandDefinition.
func NewCommandDefinitionSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260704120000",
		Migrate: func(tx *gorm.DB) error {
			// The (device_type_id, command_key) lookup index is unique so a profile
			// cannot declare the same command twice; tenant isolation is enforced by
			// the app-layer tenant scope on the embedded TenantScoped.
			type CommandDefinition struct {
				gorm.Model
				rdb.TenantScoped
				rdb.TokenReference
				rdb.NamedEntity
				rdb.MetadataEntity

				DeviceTypeId    uint   `gorm:"not null;index:idx_command_definition_key,unique,priority:1"`
				CommandKey      string `gorm:"not null;size:128;index:idx_command_definition_key,unique,priority:2"`
				ParameterSchema *datatypes.JSON
			}
			if err := tx.AutoMigrate(&CommandDefinition{}); err != nil {
				return err
			}
			// ADR-042 P1: per-tenant partial unique index on token.
			return rdb.CreateTenantTokenIndex(tx, &CommandDefinition{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("command_definitions")
		},
	}
}
