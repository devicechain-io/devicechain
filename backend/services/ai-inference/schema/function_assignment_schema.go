// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// aiFunctionAssignmentV1 is the ai_function_assignments shape frozen at this migration.
// It pins an explicit TableName for the reason every table in this service does: gorm's
// naming splits the "AI" initialism, so the derived name would not match the one the
// runtime model pins — and migrationdiff cannot catch that class of disagreement,
// because it never exercises CRUD.
//
// No DeletedAt: assignments are HARD-deleted (see model.AIFunctionAssignment), which is
// what lets uix_ai_function_assignment below be plain rather than partial-on-live. A
// soft delete would make the unique index count tombstones and refuse to re-assign a
// function that had previously been cleared.
//
// The Provider relation exists on this MIGRATION shape but deliberately NOT on the
// runtime model: it is here only so AutoMigrate emits the FOREIGN KEY inline at CREATE
// TABLE (the platform's established way, which works on Postgres and SQLite alike,
// whereas a raw ALTER TABLE ... ADD CONSTRAINT is Postgres-only and fails the unit
// tests). Keeping it off the runtime model avoids gorm's association auto-save, which
// would try to upsert the provider whenever an assignment is written.
type aiFunctionAssignmentV1 struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	rdb.TenantScoped
	Function   string        `gorm:"not null;size:64"`
	ProviderID uint          `gorm:"not null;index"`
	Provider   *aiProviderV1 `gorm:"foreignKey:ProviderID;constraint:OnDelete:RESTRICT"`
}

func (aiFunctionAssignmentV1) TableName() string { return "ai_function_assignments" }

// NewAIFunctionAssignmentsSchema adds the table that stores a tenant's chosen model per
// AI function — the mechanism that REPLACED the derived "default model".
//
// The point of the table is that the answer is STORED. Its predecessor inferred which
// model served a call from properties of the grant sets (the sole granted model, the
// first grant to a tier, whether the tier granted anything, whether a mark survived) —
// and every one of those inferences re-answered when an operator changed the set, which
// is a non-monotonic default and shipped as a bug five times. A row keyed by (tenant,
// function) cannot be re-derived by anything an operator does elsewhere.
//
// ONE ASSIGNMENT PER (TENANT, FUNCTION) is uix_ai_function_assignment. It is declared as
// raw SQL rather than by struct tag because rdb.TenantScoped contributes TenantId as an
// EMBEDDED field, so a composite uniqueIndex tag cannot name its column — the same
// reason uix_ai_tenant_grant_pair is declared this way.
//
// The provider_id FOREIGN KEY (ON DELETE RESTRICT) is declared by the Provider relation
// on the shape above. It is a real backstop, not decoration: Api.DeleteAIProvider is
// Unscoped(), so the DELETE reaches the database and the constraint fires. (That does
// NOT follow from AIProvider embedding gorm.Model — a soft delete would leave the row in
// place and the constraint would never trip.) assertProviderNotGranted checks first and
// refuses with a message naming the tenants that assigned the provider, so an operator
// gets a legible refusal rather than a constraint violation; the constraint is what
// still holds if application code is walked around. Note SQLite ignores foreign keys
// unless the pragma is on, so the unit tests prove the application refusal and Postgres
// enforces the constraint.
func NewAIFunctionAssignmentsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260716140000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&aiFunctionAssignmentV1{}); err != nil {
				return err
			}

			stmt := &gorm.Statement{DB: tx}
			if err := stmt.Parse(&aiFunctionAssignmentV1{}); err != nil {
				return err
			}

			// One model per (tenant, function): re-assigning replaces, never duplicates.
			return tx.Exec(fmt.Sprintf(
				"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s, %s)",
				stmt.Quote("uix_ai_function_assignment"), stmt.Quote(stmt.Table),
				stmt.Quote("tenant_id"), stmt.Quote("function"),
			)).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("ai_function_assignments")
		},
	}
}
