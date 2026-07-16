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
	Active  bool `gorm:"not null;default:false"`
}

func (aiProviderV1) TableName() string { return "ai_providers" }

// NewAIProvidersSchema adds the ai_providers table (ADR-056 §4): the instance-scoped,
// operator-managed inference-provider list.
//
// Two partial unique indexes back the model's invariants (GORM cannot express either
// via struct tags):
//   - a GLOBAL unique token among live rows — the provider list is instance-global,
//     not per-tenant, so token uniqueness is a plain (not tenant-composite) index,
//     restricted to non-soft-deleted rows so a delete frees the token.
//   - AT MOST ONE active provider among live rows — a unique index on (active)
//     filtered to WHERE active, so a second provider cannot be set active without the
//     first being cleared (the storage backstop to the transactional SetActiveProvider).
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
			// At most one active provider among live rows. WHERE active leaves the many
			// inactive rows out of the uniqueness set entirely (Postgres + SQLite alike).
			// The table name is resolved through gorm so it carries the service's schema
			// prefix (CreatePartialUniqueIndex can't add the extra `AND active` predicate,
			// so the SQL is built here, mirroring rdb.CreateTenantExternalIdIndex).
			stmt := &gorm.Statement{DB: tx}
			if err := stmt.Parse(&aiProviderV1{}); err != nil {
				return err
			}
			return tx.Exec(fmt.Sprintf(
				"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s) WHERE deleted_at IS NULL AND active",
				stmt.Quote("uix_ai_providers_active"), stmt.Quote(stmt.Table), stmt.Quote("active"),
			)).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("ai_providers")
		},
	}
}
