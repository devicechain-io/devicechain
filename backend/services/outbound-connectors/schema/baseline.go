// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

// This file imports none of the service's own packages, and that is a property worth
// keeping rather than an accident. A migration that can reach a live model, a live
// vocabulary or a live validator is a migration whose behaviour changes when those do.
import (
	"fmt"
	"strings"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewBaselineSchema materializes outbound-connectors' OWN schema in one step: the versioned
// Connector entity's draft and published-version tables (ADR-060 C4).
//
// 🔴 IT DOES NOT CREATE THE SECRETS TABLE, and that is a boundary rather than an omission.
// The shared secret store (ADR-059) is migrated by core/secrets and stays a separate entry
// in the chain, deliberately: three services consume it and every one of them must seal
// with the same instance KEK, so that table IS core's schema and a change to it SHOULD
// change all three. Contrast the inlined mixins in baseline_snapshot.go, where core owning
// a fragment of THIS service's table is accidental and hazardous. The distinction is
// whose schema it is, not which module the code lives in.
//
// This REPLACES the former two-migration chain for this area's own tables, collapsed
// pre-GA. Until v1.0.0 there is no released version to upgrade FROM. Its equivalence to
// that chain is proven by hack/migration-diff.sh verify against the golden captured from
// it. Neither replaced migration carried data — no seed, no backfill.
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
			// The SNAPSHOT types in baseline_snapshot.go, never the live models.
			if err := tx.AutoMigrate(&connector{}, &connectorVersion{}); err != nil {
				return err
			}
			// ADR-042 P1: a token is unique within a tenant among LIVE rows only. Without
			// it two live connectors could share a token within a tenant.
			return createTenantTokenIndex(tx, &connector{})
		},
		Rollback: func(tx *gorm.DB) error {
			for _, table := range []string{"connector_versions", "connectors"} {
				if err := tx.Migrator().DropTable(table); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// createTenantTokenIndex creates the per-tenant partial unique index on a token-referenced,
// tenant-scoped, soft-deletable table: (tenant_id, token) unique among rows where
// deleted_at IS NULL.
//
// 🔴 THIS IS A DELIBERATE COPY OF rdb.CreateTenantTokenIndex, NOT AN OVERSIGHT. Calling the
// core helper would put this migration's output — an index NAME and a WHERE predicate, both
// of which are schema — under the control of code in another module. The day core renames
// that index or widens its predicate, this already-applied migration starts creating
// something different on fresh installs while every existing database keeps the old one,
// from a PR that never touched outbound-connectors. The snapshot rule outranks DRY inside a
// migration. Do not "clean this up".
func createTenantTokenIndex(tx *gorm.DB, model any) error {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("parse model for tenant-token index: %w", err)
	}
	bare := stmt.Table
	if i := strings.LastIndex(bare, "."); i >= 0 {
		bare = bare[i+1:]
	}
	return tx.Exec(fmt.Sprintf(
		"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s, %s) WHERE deleted_at IS NULL",
		stmt.Quote("uix_"+bare+"_tenant_token"), stmt.Quote(stmt.Table),
		stmt.Quote("tenant_id"), stmt.Quote("token"),
	)).Error
}
