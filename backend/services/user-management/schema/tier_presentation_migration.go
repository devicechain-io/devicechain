// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewTierPresentationSchema adds the two PRESENTATION columns to iam_tenant_tiers
// (ADR-065 S5c): display_order, the operator's chosen listing order, and color, a token
// into a named palette shown as a pill beside a tenant.
//
// THIS IS THE FIRST MIGRATION APPENDED AFTER THE FLATTEN, and it is written the way every
// migration after a baseline must be (see baseline.go / data-modeling.md): it declares its
// OWN snapshot of just the columns it touches, never the live iam.TenantTier, and it never
// edits the baseline. AutoMigrate over a partial snapshot adds the two missing columns
// without touching the rest of the table — gorm never drops a column it does not see.
//
// display_order defaults to 0. Every existing tier lands at 0 and sorts by a stable
// secondary key (token) until an operator arranges them, which is a fine initial state:
// an unarranged catalog is alphabetical, not broken. color defaults to empty — no pill
// until the operator picks one, rather than a color the platform chose for them.
//
// A WORD ON WHAT display_order IS NOT. It is presentation only: the order tiers are shown
// in, nothing more. It is emphatically NOT a rank, a priority, or a claim that a
// higher-placed tier's entitlements contain a lower one's. ADR-065 rejected a tier ordinal
// for exactly that reason — real packaging does not nest, two tiers can offer almost the
// same set arranged differently, and neither contains the other. Nothing in the codebase
// may read this column to make a decision; ADR-063's shed priority is its own field. This
// note lives here because a bare integer column named "order" is precisely the shape a
// future reader mistakes for a ranking.
func NewTierPresentationSchema() *gormigrate.Migration {
	// The snapshot: iam_tenant_tiers as this migration needs to see it — its primary key
	// plus only the two new columns. Not the live model. The table is pinned with
	// tx.Table below rather than a TableName method: gorm would otherwise derive
	// "tenant_tiers" from this local type name and miss the iam_ prefix (which lives on
	// the live model's TableName, not in the name). tx.Table drives both the add and the
	// rollback drop.
	type tenantTier struct {
		ID           uint   `gorm:"primarykey"`
		DisplayOrder int    `gorm:"not null;default:0"`
		Color        string `gorm:"not null;default:'';size:32"`
	}
	return &gormigrate.Migration{
		ID: "20260717120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Table("iam_tenant_tiers").AutoMigrate(&tenantTier{})
		},
		Rollback: func(tx *gorm.DB) error {
			m := tx.Table("iam_tenant_tiers").Migrator()
			if err := m.DropColumn(&tenantTier{}, "DisplayOrder"); err != nil {
				return err
			}
			return m.DropColumn(&tenantTier{}, "Color")
		},
	}
}
