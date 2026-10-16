// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// AssignmentScopeAnchorsID is the ID of the migration that adds update_assignments.scope_anchors.
const AssignmentScopeAnchorsID = "20261016000100"

// NewAssignmentScopeAnchorsSchema adds update_assignments.scope_anchors: the anchors of the
// device ("customer:acme", one per tracked relationship) when the assignment was made
// (ADR-081). A member limited to part of the tenant sees an assignment when one of them is in
// its scope.
//
// NULLABLE with no default: NULL is the honest reading of every assignment made before the
// column existed, and such a row is invisible to every scoped member and unchanged for everyone
// else. Pre-GA, nothing is backfilled.
//
// The GIN index serves the scope predicate (`scope_anchors && ARRAY[...]::text[]`), the one
// read that consults the column. It is PostgreSQL's alone: sqlite, which the unit suites run
// this chain on, has neither the access method nor the operator.
//
// Re-runnable (AutoMigrate, IF NOT EXISTS), as migrations run with UseTransaction:false and
// replay from the top after a failure.
func NewAssignmentScopeAnchorsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: AssignmentScopeAnchorsID,
		Migrate: func(tx *gorm.DB) error {
			// The snapshot names only the key and the new column, so AutoMigrate adds the one
			// and re-asserts nothing else of the table.
			type updateAssignment struct {
				ID           uint   `gorm:"primarykey"`
				ScopeAnchors string `gorm:"type:text[]"`
			}
			if err := tx.AutoMigrate(&updateAssignment{}); err != nil {
				return err
			}
			if tx.Dialector.Name() != "postgres" {
				return nil
			}
			table, name, cols, err := indexParts(tx, &updateAssignment{}, "idx_update_assignments_scope_anchors",
				[]string{"scope_anchors"})
			if err != nil {
				return err
			}
			return tx.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s USING gin (%s)", name, table, cols)).Error
		},
		// Only the index is dropped; the column is left, as dropping it destroys data.
		Rollback: func(tx *gorm.DB) error {
			if tx.Dialector.Name() != "postgres" {
				return nil
			}
			return tx.Exec(`DROP INDEX IF EXISTS "update-management".idx_update_assignments_scope_anchors;`).Error
		},
	}
}
