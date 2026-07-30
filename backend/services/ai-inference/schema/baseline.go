// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

// This file imports none of the service's own packages, and that is a property worth
// keeping rather than an accident. A migration that can reach a live model, a live
// vocabulary or a live validator is a migration whose behaviour changes when those do.
import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewBaselineSchema materializes ai-inference's OWN schema in one step: the instance-scoped
// provider list (ADR-056 §4), the tier and per-tenant grant tables that carry a tenant's
// model menu, and the stored (tenant, function) → model assignment (ADR-065 decision 10).
//
// 🔴 IT DOES NOT CREATE THE SECRETS TABLE. The shared secret store (ADR-059) — which holds
// each provider's envelope-encrypted API key — is migrated by core/secrets and stays a
// separate entry in the chain, deliberately: three services consume it and every one must
// seal with the same instance KEK, so that table IS core's schema and a change to it SHOULD
// change all three. Contrast the inlined mixins in baseline_snapshot.go, where core owning a
// fragment of THIS service's table is accidental and hazardous. The test is whose schema it
// is, not which module the code lives in.
//
// This REPLACES the former three-migration chain for this area's own tables, collapsed
// pre-GA. Until v1.0.0 there is no released version to upgrade FROM. Equivalence to that
// chain is proven by hack/migration-diff.sh verify against the golden captured from it. None
// of the three carried data — no seed, no backfill — so nothing is dropped and nothing needs
// a row-level test.
//
// Consequence, and it is deliberate: an EXISTING instance is not migrated onto this
// baseline, it is recreated (dcctl destroy + bootstrap).
//
// CHANGING THE SCHEMA FROM HERE ON: append a new migration. Do not touch this one, and do
// not "update" the snapshot types it builds from. Anything appended must be individually
// re-runnable, since migrations run with UseTransaction:false and replay from the top after
// a failure.
func NewBaselineSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260729000000",
		Migrate: func(tx *gorm.DB) error {
			// The SNAPSHOT types in baseline_snapshot.go, never the live models. Providers
			// first: the three other tables carry a provider_id FOREIGN KEY, emitted inline
			// at CREATE TABLE by the Provider relations on those shapes.
			if err := tx.AutoMigrate(
				&aiProvider{},
				&aiProviderTierGrant{},
				&aiProviderTenantGrant{},
				&aiFunctionAssignment{},
			); err != nil {
				return err
			}

			// The four indexes gorm cannot express as struct tags. Two need a partial
			// predicate; two are composites whose leading column arrives from what was an
			// embedded field, which a uniqueIndex tag cannot name.
			//
			// Written out literally rather than through rdb.CreatePartialUniqueIndex for the
			// reason the snapshot rule exists: an index NAME and a WHERE predicate are
			// schema, and core must not be able to change what this applied migration
			// creates. The table is still resolved through gorm so the area prefix and
			// quoting stay correct.
			for _, idx := range []struct {
				model   any
				name    string
				columns string
				where   string
			}{
				// Global (NOT per-tenant) unique token among live rows: the provider list is
				// instance-global. Partial on deleted_at so a delete frees the token.
				{&aiProvider{}, "uix_ai_providers_token", "token", "deleted_at IS NULL"},
				// At most one default provider per tier, leaving non-default rows out of the
				// uniqueness set. No deleted_at clause is needed and that is a property of
				// the table rather than an omission: tier grants hard-delete, so there are
				// no tombstones for the index to count.
				{&aiProviderTierGrant{}, "uix_ai_tier_grant_default", "tier_token", "is_default"},
				// One additive grant per (tenant, provider).
				{&aiProviderTenantGrant{}, "uix_ai_tenant_grant_pair", "tenant_id, provider_id", ""},
				// One model per (tenant, function): re-assigning replaces, never duplicates.
				{&aiFunctionAssignment{}, "uix_ai_function_assignment", "tenant_id, function", ""},
			} {
				stmt := &gorm.Statement{DB: tx}
				if err := stmt.Parse(idx.model); err != nil {
					return fmt.Errorf("parse model for index %s: %w", idx.name, err)
				}
				sql := fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s)",
					stmt.Quote(idx.name), stmt.Quote(stmt.Table), idx.columns)
				if idx.where != "" {
					sql += " WHERE " + idx.where
				}
				if err := tx.Exec(sql).Error; err != nil {
					return fmt.Errorf("create index %s: %w", idx.name, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// Dependents before the table their foreign key references.
			for _, table := range []string{
				"ai_function_assignments",
				"ai_provider_tenant_grants",
				"ai_provider_tier_grants",
				"ai_providers",
			} {
				if err := tx.Migrator().DropTable(table); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
