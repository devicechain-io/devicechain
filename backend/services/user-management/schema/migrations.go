// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

// Migrations run in slice order (not by ID), so a new migration must be appended
// last.
//
// This area was collapsed to a single baseline pre-GA (see NewBaselineSchema): the 14-migration
// chain that preceded it existed only to walk developer databases forward one column at a time,
// and until v1.0.0 there is no released version to upgrade from — an existing instance is
// recreated, not migrated ONTO THE BASELINE — which is not the same claim as "an existing
// instance is never migrated", and reading it as the second one is how device-management's
// geometry archive shipped without the backfill an upgraded instance needed (#838). Everything
// APPENDED below the baseline runs on live databases: v0.11.0 and v0.12.0 were each reached with
// `helm upgrade`. The IDs of those removed migrations stay recorded in any database that ran
// them, which gormigrate tolerates because the RdbManager runs with ValidateUnknownMigrations off
// (backend/core/rdb). Don't enable that validation without first reconciling those orphaned rows.
//
// The three migrations that had accumulated on top of that baseline — the tier presentation
// columns, the per-tenant shed-priority column, and the tier shed-priority seed — were folded
// back into it at the GA squash, which is what their own note here said would happen. Two were
// DDL and fold in as fields; the seed folded into seededTiers and is covered by
// baseline_seed_test.go, because migration-diff cannot see a row.
//
// CHANGING THE SCHEMA: append a migration here. Do NOT rely on the baseline's
// AutoMigrate to converge a model change, and never edit the baseline — it builds
// from its own frozen snapshot types precisely so it does not track the live models
// (see baseline_snapshot.go and .agent-os/product/data-modeling.md). A new migration
// declares its own snapshot of just what it touches. Anything appended must be
// individually re-runnable, since migrations run with UseTransaction:false and replay
// from the top after a failure.
var (
	Migrations = []*gormigrate.Migration{
		NewBaselineSchema(),
		// ADR-077 tenant lifecycle. The first migration appended after the GA squash,
		// and the worked example of the rule above: its own snapshot, no live models.
		NewTenantPurgeStateMigration(),
		// ADR-077 the deletion record + its per-store ledger. Completion removes the
		// tenant row to release the token, so completion cannot be recorded on it.
		NewTenantPurgeRecordMigration(),
		// ADR-077 the ledger's note column: what a store DECIDED NOT TO LOOK AT, which
		// two of them could previously omit while reporting clean.
		NewTenantPurgeNoteMigration(),
		// ADR-079 the per-tenant basemap override: the tile source, its credit line, and
		// a fallback view. Moves the basemap off every browser's localStorage and onto
		// the tenant that owns the provider credential.
		NewTenantBasemapMigration(),
		// ADR-024 the dead-letter store: the queryable record of work a consumer
		// accepted and gave up on. It lives here, on the operator plane, so the read
		// surface never has to answer whether a per-entry failure reason is safe to
		// show a tenant.
		NewDeadLettersMigration(),
		// The per-tenant HELD-command ceiling: how many commands a tenant may have parked
		// because the platform is deliberately withholding dispatch from an absent device.
		// An offline fleet's backlog sits there for days and nothing drains it, so it is
		// bounded per tenant above the tier's default.
		NewTenantHeldCommandCeilingMigration(),
		// The three per-tenant geofence caps: how many positions one fence may carry, how
		// many fences the tenant may hold, and how many positions its whole fence set may
		// carry. All three carry a platform maximum — the first governance overrides that do —
		// and the third is the one that bounds a resource the tenant does not own, a compiled
		// fence set in a DETECT process serving every tenant. The costs are stated once, in
		// core/governance; this comment deliberately does not retell them.
		NewTenantGeoFenceCapsMigration(),
	}
)
