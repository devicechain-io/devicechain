// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// aiProvider is the ai_providers shape frozen at this migration (the gormigrate
// convention pins the migration to a shape independent of later model changes). It
// declares an explicit TableName: gorm's default naming derives "AIProvider" to
// "a_iproviders" (its initialism handling splits "AI"), but the model pins the table
// to "ai_providers" — so the migration MUST pin the same name or it would create a
// table the runtime CRUD can't find. A package-level type (not a func-local one) is
// required because only a named type can carry the TableName method.
type aiProvider struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	Kind     string `gorm:"not null;size:64"`
	Endpoint string `gorm:"size:512"`
	ModelID  string `gorm:"column:model;not null;size:128"`
	Params   datatypes.JSON
	// No gorm `default` on Enabled: a `default:true` would make gorm substitute the DB
	// default for the Go zero value (false) on Create, so a provider could never be
	// persisted DISABLED. The GraphQL contract is `enabled: Boolean!` (always explicit),
	// so the create path always carries a value and no DB default is needed.
	Enabled bool `gorm:"not null"`
}

func (aiProvider) TableName() string { return "ai_providers" }

// NewAIProvidersSchema adds the ai_providers table (ADR-056 §4): the instance-scoped,
// operator-managed inference-provider list.
//
// One partial unique index backs the model's invariant (GORM cannot express it via
// struct tags): a GLOBAL unique token among live rows — the provider list is
// instance-global, not per-tenant, so token uniqueness is a plain (not
// tenant-composite) index, restricted to non-soft-deleted rows so a delete frees the
// token.
//
// THE PROVIDER ROW CARRIES NO INSTANCE-WIDE MARK, AND THAT IS THE POINT OF THIS TABLE'S
// SHAPE. It has twice carried one and twice given it up:
//
//   - `active` + uix_ai_providers_active — "at most one active provider, instance-wide".
//     ADR-065 retired it: it modeled "one model, globally" and could express no
//     packaging at all.
//   - `is_platform_baseline` + uix_ai_providers_baseline — the model a tenant got for a
//     function it never assigned. Retired in turn: a default that every tier shares is
//     not a per-tier default, and it made an operator's packaging decision reachable
//     from a column nobody had to grant.
//
// The fallback now rides a GRANT ROW (ai_provider_tier_grants.is_default), one per tier,
// which is what makes "AI is a tiered entitlement" a property of the schema rather than
// a check somebody has to remember to write: a tier that grants nothing has nowhere to
// put a default. Both columns are removed from the frozen shape here rather than dropped
// by a follow-on migration because the platform is pre-GA with no installations, so a
// drop-column migration would be scaffolding for a shape that has never existed anywhere
// durable (CLAUDE.md: prefer decisive cutovers). An existing dev cluster is rebuilt with
// dcctl destroy && bootstrap, which is already this arc's live-verify recipe.
func NewAIProvidersSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260715140000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&aiProvider{}); err != nil {
				return err
			}
			// Global (not per-tenant) unique token among live rows.
			return rdb.CreatePartialUniqueIndex(tx, &aiProvider{}, "uix_ai_providers_token", "token")
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("ai_providers")
		},
	}
}
