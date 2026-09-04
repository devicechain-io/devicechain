// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewAnalyticsSurfaceSchema creates the governed, read-only SQL surface a BI tool
// connects to: the "analytics" schema, the reader_tenant() identity function, and one
// tenant-filtered view per event hypertable plus the measurement rollup.
//
// The design, the measurements behind it, and — importantly — the design that was
// tried and could NOT be built (row-level security, which TimescaleDB refuses to
// combine with compression in either order) are documented in analytics.go. Read that
// before changing anything here.
//
// It follows the three properties the first post-baseline migration established:
//
//  1. **It is individually re-runnable.** This area's DDL is non-transactional, so a
//     half-applied migration is never rolled back and replays from the top on the
//     next boot. Every statement here is idempotent by construction — CREATE SCHEMA
//     IF NOT EXISTS, CREATE OR REPLACE FUNCTION, CREATE OR REPLACE VIEW — and none
//     depends on a previous one having run in the same pass.
//
//  2. **It declares its own shape.** The view projections are frozen column lists in
//     analytics.go, not `SELECT *` and not derived from the live models. A star is
//     expanded ONCE at view-creation time, so it would give a fresh install and an
//     upgraded install different view bodies the first time a later migration appends
//     a column — the exact divergence the snapshot rule exists to prevent, reached
//     through a wildcard instead of a struct.
//
//  3. **It creates no roles and grants nothing.** Not for tidiness: the platform's
//     application role holds no CREATEROLE, so a CREATE ROLE here would fail in
//     production while passing every gate in this repo, all of which migrate as a
//     superuser. Roles are declared on the database cluster and the grants are
//     converged at boot by ReconcileAnalyticsSurface, which is also what makes the
//     cluster's asynchronous role creation an ordering that heals itself.
//
// 🔴 WHAT THE MIGRATION DIFFER CAN AND CANNOT SEE HERE, because the answer is not
// uniform and "the gate is green" must not stand in for "the surface is right":
//
//	the analytics schema, function and views   VISIBLE — but only because this
//	                                           change also teaches the differ to dump
//	                                           that schema (backend/tools/migrationdiff,
//	                                           area.extraSchemas). Before that it dumped
//	                                           `--schema <area>` only, so every object
//	                                           here would have been invisible to the one
//	                                           gate that exercises migrations at all.
//	roles                                      INVISIBLE. pg_dump does not emit roles;
//	                                           that is pg_dumpall --roles-only.
//	grants                                     INVISIBLE. The dump passes
//	                                           --no-privileges, so an ACL cannot move
//	                                           the golden. This is why the grant
//	                                           boundary is covered by an integration
//	                                           test that connects AS a reader rather
//	                                           than by a schema diff.
func NewAnalyticsSurfaceSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260904000000",
		Migrate: func(tx *gorm.DB) error {
			return execAnalyticsSurface(tx)
		},
		Rollback: func(tx *gorm.DB) error {
			for _, v := range analyticsViews {
				if err := tx.Exec(v.dropViewStmt()).Error; err != nil {
					return fmt.Errorf("dropping the %s analytics view: %w", v.name, err)
				}
			}
			if err := tx.Exec(fmt.Sprintf(
				"DROP FUNCTION IF EXISTS %q.reader_tenant();", AnalyticsSchema)).Error; err != nil {
				return fmt.Errorf("dropping reader_tenant: %w", err)
			}
			// RESTRICT, not CASCADE: if anything else has come to live in this
			// schema the rollback should say so rather than delete it.
			return tx.Exec(fmt.Sprintf("DROP SCHEMA IF EXISTS %q RESTRICT;", AnalyticsSchema)).Error
		},
	}
}
