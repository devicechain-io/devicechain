// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// This file imports none of the service's own packages, and that is a property worth
// keeping rather than an accident. A migration that can reach a live model, a live
// vocabulary or a live validator is a migration whose behaviour changes when those do.
// Everything it needs — shapes, index DDL, helpers — it holds itself.
import (
	"fmt"
	"strings"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// baselineTables is every table this baseline owns, ordered so a Rollback drops
// dependents before what they reference.
var baselineTables = []string{
	"dashboard_versions",
	"dashboards",
}

// NewBaselineSchema materializes the whole dashboard-management schema in one step: the
// dashboard definition and its immutable published versions (ADR-039).
//
// This REPLACES the former two-migration chain, collapsed pre-GA. Until v1.0.0 there is
// no released version to upgrade FROM, so that chain only ever walked a maintainer's
// laptop forward one table at a time; the history has no value before there is something
// to migrate from. Its equivalence to this migration is proven by
// hack/migration-diff.sh verify against the golden captured from the chain.
//
// Consequence, and it is deliberate: an EXISTING instance is not migrated onto this
// baseline, it is recreated (dcctl destroy + bootstrap).
//
// CHANGING THE SCHEMA FROM HERE ON: append a new migration. Do not touch this one, and do
// not "update" the snapshot types it builds from — see baseline_snapshot.go. This file
// describes the schema as of the flatten and must keep describing exactly that, however
// far the live models move away from it. Anything appended must be individually
// re-runnable, since migrations run with UseTransaction:false (core/rdb: Timescale DDL
// cannot run in a transaction) and so replay from the top after a failure.
func NewBaselineSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260729000000",
		Migrate: func(tx *gorm.DB) error {
			// The SNAPSHOT types in baseline_snapshot.go, never the live models.
			if err := tx.AutoMigrate(&dashboard{}, &dashboardVersion{}); err != nil {
				return err
			}
			// ADR-042 P1: a token is unique within a tenant among LIVE rows only. Not
			// declared as a struct tag because gorm cannot express a partial index, and
			// a plain UNIQUE(token) would both collide across tenants and keep a
			// soft-deleted row's token locked forever.
			return createTenantTokenIndex(tx, &dashboard{})
		},
		Rollback: func(tx *gorm.DB) error {
			for _, table := range baselineTables {
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
// 🔴 THIS IS A DELIBERATE COPY OF rdb.CreateTenantTokenIndex, NOT AN OVERSIGHT. Calling
// the core helper would put this migration's output — an index NAME and a WHERE predicate,
// both of which are schema — under the control of code in another module. The day core
// renames that index or widens its predicate, this already-applied migration starts
// creating something different on fresh installs while every existing database keeps the
// old one, from a PR that never touched dashboard-management. That is the same hazard the
// inlined mixins in baseline_snapshot.go exist to remove, one level up, and the reason
// the snapshot rule outranks DRY inside a migration. Do not "clean this up".
//
// It resolves the table through gorm rather than hardcoding the schema-qualified name so
// the area's TablePrefix and quoting stay correct; the NAME and the PREDICATE — the parts
// that are schema — are literal here.
func createTenantTokenIndex(tx *gorm.DB, model any) error {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("parse model for tenant-token index: %w", err)
	}
	// stmt.Table carries the area prefix, so strip it for the index name: the index is
	// already scoped by the schema it lives in, and the golden it must reproduce was
	// built from the unprefixed form.
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
