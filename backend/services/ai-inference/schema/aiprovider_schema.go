// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// aiProviderV1 is the ai_providers shape frozen at this migration (the gormigrate
// convention pins the migration to a shape independent of later model changes). It
// declares an explicit TableName: gorm's default naming derives "AIProvider" to
// "a_iproviders" (its initialism handling splits "AI"), but the model pins the table
// to "ai_providers" — so the migration MUST pin the same name or it would create a
// table the runtime CRUD can't find. A package-level type (not a func-local one) is
// required because only a named type can carry the TableName method.
type aiProviderV1 struct {
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
	// The instance's baseline model designation — at most one live row carries it. No
	// gorm `default` tag, for the same reason as Enabled above.
	IsPlatformBaseline bool `gorm:"not null"`
}

func (aiProviderV1) TableName() string { return "ai_providers" }

// NewAIProvidersSchema adds the ai_providers table (ADR-056 §4): the instance-scoped,
// operator-managed inference-provider list.
//
// One partial unique index backs the model's invariant (GORM cannot express it via
// struct tags): a GLOBAL unique token among live rows — the provider list is
// instance-global, not per-tenant, so token uniqueness is a plain (not
// tenant-composite) index, restricted to non-soft-deleted rows so a delete frees the
// token.
//
// A SECOND partial unique index backs the platform-baseline designation: at most one
// live provider instance-wide may be the baseline (model.AIProvider.IsPlatformBaseline
// — the model a tenant gets for a function it never assigned, if it is entitled to it).
//
// Both `WHERE` clauses matter, and the second one is the easy one to forget: aiProviderV1
// embeds gorm.Model, so it SOFT-deletes, and an index filtered on is_platform_baseline
// alone would count TOMBSTONES. Deleting the baseline provider and designating a new one
// would then be refused by a unique violation against a dead row — the tombstone-counting
// unique-index bug this repo already carries in three other places. uix_ai_providers_token
// above dodges it via rdb.CreatePartialUniqueIndex's built-in `WHERE deleted_at IS NULL`;
// this index cannot use that helper (it needs a second predicate), so it states both
// itself.
//
// This migration once carried an `active` column and a uix_ai_providers_active partial
// unique index enforcing "at most one active provider, instance-wide". Both are gone:
// ADR-065 replaced the single global pointer with a per-tier MENU. Note what the
// baseline index below is NOT: it is not that pointer returning. `active` decided which
// model every tenant used regardless of packaging; the baseline is a fallback for a
// tenant that chose nothing, and it is filtered through that tenant's menu, so it can
// never serve a tenant its tier was not sold. The column is removed from the frozen shape here rather
// than dropped by a follow-on migration because the platform is pre-GA with no
// installations, so a drop-column migration would be scaffolding for a shape that has
// never existed anywhere durable (CLAUDE.md: prefer decisive cutovers). An existing
// dev cluster is rebuilt with dcctl destroy && bootstrap, which is already this arc's
// live-verify recipe.
func NewAIProvidersSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260715140000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&aiProviderV1{}); err != nil {
				return err
			}
			// Global (not per-tenant) unique token among live rows.
			if err := rdb.CreatePartialUniqueIndex(tx, &aiProviderV1{}, "uix_ai_providers_token", "token"); err != nil {
				return err
			}

			// At most one platform-baseline provider among LIVE rows. Hand-rolled rather
			// than via CreatePartialUniqueIndex because the filter carries two predicates
			// (see the doc above): the designation AND the not-deleted guard.
			stmt := &gorm.Statement{DB: tx}
			if err := stmt.Parse(&aiProviderV1{}); err != nil {
				return err
			}
			return tx.Exec(fmt.Sprintf(
				"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s) WHERE %s AND %s IS NULL",
				stmt.Quote("uix_ai_providers_baseline"), stmt.Quote(stmt.Table),
				stmt.Quote("is_platform_baseline"), stmt.Quote("is_platform_baseline"),
				stmt.Quote("deleted_at"),
			)).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("ai_providers")
		},
	}
}
