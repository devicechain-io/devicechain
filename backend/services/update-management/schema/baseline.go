// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

// This file imports none of the service's own packages, and that is a property worth
// keeping. A migration that can reach a live model is a migration whose behaviour changes
// when that model does.
import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// BaselineID is the baseline migration's frozen ID. It is recorded in every database
// that ran it, so it never changes.
const BaselineID = "20261010000000"

// NewBaselineSchema is the update-management baseline, and it creates NO TABLES of the
// area's own.
//
// That is deliberate rather than a placeholder that forgot its body. The area is
// scaffolded before any model lands in it: the artifact catalogue, assignments and the
// download plane each arrive as an APPENDED migration carrying its own snapshot structs.
// What a fresh install of this baseline holds is therefore only what core/rdb creates in
// every area schema — the migrations table, the audit journal and the erasure fence's
// purged_tenants table — which is exactly what the committed golden
// (backend/tools/migrationdiff/golden/update-management.sql) pins.
//
// It exists at all because a chain cannot be empty: gormigrate refuses to run with no
// migrations defined, so the area's schema and its core tables would never be created.
//
// Never edit it. A table added here instead of in an appended migration would be created
// on fresh installs and silently missing from every database that already applied this
// ID.
func NewBaselineSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID:       BaselineID,
		Migrate:  func(*gorm.DB) error { return nil },
		Rollback: func(*gorm.DB) error { return nil },
	}
}
