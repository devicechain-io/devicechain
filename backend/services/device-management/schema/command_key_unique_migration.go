// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// commandDefinitionV2 is this migration's SNAPSHOT of the command definition row —
// only what the index and the de-duplication touch. It is deliberately NOT the live
// model: a migration describes the table as it exists at this point in time, and
// pointing at the live struct would silently rewrite this migration every time that
// struct changes (breaking fresh installs while existing databases stay healthy).
// Fields beyond these are irrelevant here and so are omitted rather than mirrored.
type commandDefinitionV2 struct {
	TenantId        string
	DeviceProfileId uint
	CommandKey      string
}

// TableName pins the table this snapshot refers to. Without it GORM would derive
// "command_definition_v2s" from the struct name and the index would be created on a
// table that does not exist.
func (commandDefinitionV2) TableName() string { return "command_definitions" }

// NewCommandKeyUniqueSchema enforces at the STORAGE layer what ADR-043 decision 3
// already enforces in the API: one profile declares a given command key at most once.
//
// The application check (assertCommandKeyUnused) runs inside CreateCommandDefinition
// and UpdateCommandDefinition, but it is a read-then-write with no lock, so two
// concurrent creates can both read "unused" and both insert. The window is small and
// the consequence is quiet: the enqueue gate resolves a command by key and honours
// whichever row it sees first, so two definitions of "drive" with different parameter
// schemas make a payload's validity depend on row order. A unique index is the only
// thing that actually makes that unrepresentable.
//
// The index is PARTIAL (WHERE deleted_at IS NULL, via rdb.CreatePartialUniqueIndex).
// A plain unique index counts soft-deleted rows, so deleting a definition would lock
// its key forever and recreating it would fail — the tombstone-counting bug already
// parked on DeviceClaim, ProvisioningProfile and MetricDefinition. It leads with
// tenant_id to match the house pattern (ADR-042 P1) and the tenant-scope callback's
// own predicate, so it also serves the per-tenant lookups rather than only guarding.
//
// The de-duplication ahead of it is a decisive pre-GA data cutover, not a compat
// shim: a database written before the API check existed may already hold duplicates,
// and CREATE UNIQUE INDEX would simply fail against it. Keeping the lowest id per
// group preserves the row the enqueue gate was already resolving (it takes the first
// match in id order), so this retires the shadowed duplicates rather than changing
// which definition is in force.
func NewCommandKeyUniqueSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260718120000",
		Migrate: func(tx *gorm.DB) error {
			// Retire shadowed duplicates so the unique index can be built. Soft-delete
			// (not DELETE) keeps them recoverable and matches how every other retirement
			// in this service works.
			if err := tx.Exec(
				`UPDATE command_definitions SET deleted_at = CURRENT_TIMESTAMP ` +
					`WHERE deleted_at IS NULL AND id NOT IN ( ` +
					`SELECT MIN(id) FROM command_definitions WHERE deleted_at IS NULL ` +
					`GROUP BY tenant_id, device_profile_id, command_key)`).Error; err != nil {
				return err
			}
			return rdb.CreatePartialUniqueIndex(tx, &commandDefinitionV2{},
				"uix_command_definitions_tenant_profile_key",
				"tenant_id", "device_profile_id", "command_key")
		},
		Rollback: func(tx *gorm.DB) error {
			// The de-duplication is not reversed: which rows were shadowed is not
			// recorded, and resurrecting them would reintroduce the ambiguity.
			return tx.Exec(`DROP INDEX IF EXISTS uix_command_definitions_tenant_profile_key`).Error
		},
	}
}
