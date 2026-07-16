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

// aiProviderTierGrantV1 / aiProviderTenantGrantV1 are the grant shapes frozen at this
// migration. Both pin an explicit TableName for the same reason ai_providers does:
// gorm's naming would split the "AI" initialism and derive a table
// ("a_iprovider_tier_grants") that the runtime model — which pins the name below —
// could never find, and migrationdiff cannot catch that class of disagreement because
// it never exercises CRUD. A package-level type is required: only a named type can
// carry the method.
//
// Neither carries DeletedAt: grants are hard-deleted (see model.AIProviderTierGrant),
// which is what lets the unique indexes below be plain rather than partial-on-live.
// The Provider relation exists on these MIGRATION shapes but deliberately NOT on the
// runtime models: it is here only so AutoMigrate emits the FOREIGN KEY inline at
// CREATE TABLE (the platform's established way — user-management's tenant→tier FK is
// declared exactly like this), which works on Postgres and SQLite alike, whereas a raw
// ALTER TABLE ... ADD CONSTRAINT is Postgres-only and fails the unit tests. Keeping it
// off the runtime model avoids gorm's association auto-save, which would try to upsert
// the provider whenever a grant is created.
type aiProviderTierGrantV1 struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	// One grant per (tier, provider): re-granting is idempotent, not a duplicate row.
	TierToken  string        `gorm:"not null;size:128;uniqueIndex:uix_ai_tier_grant_pair,priority:1"`
	ProviderID uint          `gorm:"not null;uniqueIndex:uix_ai_tier_grant_pair,priority:2;index"`
	Provider   *aiProviderV1 `gorm:"foreignKey:ProviderID;constraint:OnDelete:RESTRICT"`
	IsDefault  bool          `gorm:"not null"`
}

func (aiProviderTierGrantV1) TableName() string { return "ai_provider_tier_grants" }

type aiProviderTenantGrantV1 struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	rdb.TenantScoped
	ProviderID uint          `gorm:"not null;index"`
	Provider   *aiProviderV1 `gorm:"foreignKey:ProviderID;constraint:OnDelete:RESTRICT"`
}

func (aiProviderTenantGrantV1) TableName() string { return "ai_provider_tenant_grants" }

// NewAIProviderGrantsSchema adds the two tables that carry a tenant's AI model menu
// (ADR-065 decision 10): tier→provider offers, and per-tenant additive exceptions.
// Together they replace the retired instance-wide single-active pointer.
//
// AT MOST ONE DEFAULT PER TIER is a unique index on (tier_token) filtered to WHERE
// is_default, leaving the non-default rows out of the uniqueness set — GORM cannot
// express a partial index by tag. It is the retired uix_ai_providers_active index
// re-scoped from "one globally" to "one per tier", which is precisely the shape change
// ADR-065 is about.
//
// The provider_id FOREIGN KEY (ON DELETE RESTRICT) is declared by the Provider
// relation on the shapes above rather than here. Be precise about what it buys,
// because a constraint reads stronger than it is: it rejects a grant naming a provider
// that does not exist, and it genuinely blocks the delete of a granted provider —
// Api.DeleteAIProvider is Unscoped(), so the DELETE reaches the database and the
// constraint fires. (Worth stating, because it does NOT follow from the model
// embedding gorm.Model: a soft delete would leave the row in place and the constraint
// would never trip.) Api.DeleteAIProvider checks first and returns ErrProviderInUse
// naming the tiers, so an operator gets a legible refusal rather than a constraint
// violation; the constraint is the backstop beneath that check, not a substitute for
// it. Note SQLite does not enforce foreign keys unless asked, so the unit tests prove
// the application refusal and Postgres enforces the constraint.
func NewAIProviderGrantsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260716120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&aiProviderTierGrantV1{}, &aiProviderTenantGrantV1{}); err != nil {
				return err
			}

			tierStmt := &gorm.Statement{DB: tx}
			if err := tierStmt.Parse(&aiProviderTierGrantV1{}); err != nil {
				return err
			}
			tenantStmt := &gorm.Statement{DB: tx}
			if err := tenantStmt.Parse(&aiProviderTenantGrantV1{}); err != nil {
				return err
			}

			// At most one default provider per tier.
			if err := tx.Exec(fmt.Sprintf(
				"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s) WHERE %s",
				tierStmt.Quote("uix_ai_tier_grant_default"), tierStmt.Quote(tierStmt.Table),
				tierStmt.Quote("tier_token"), tierStmt.Quote("is_default"),
			)).Error; err != nil {
				return err
			}

			// One additive grant per (tenant, provider). Declared here rather than by
			// tag because rdb.TenantScoped contributes TenantId as an embedded field and
			// the composite must name its column.
			return tx.Exec(fmt.Sprintf(
				"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s, %s)",
				tenantStmt.Quote("uix_ai_tenant_grant_pair"), tenantStmt.Quote(tenantStmt.Table),
				tenantStmt.Quote("tenant_id"), tenantStmt.Quote("provider_id"),
			)).Error
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable("ai_provider_tenant_grants"); err != nil {
				return err
			}
			return tx.Migrator().DropTable("ai_provider_tier_grants")
		},
	}
}
