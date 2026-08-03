// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

// Migrations run in slice order (not by ID), so a new migration must be appended
// last.
//
// This area was collapsed to a single baseline pre-GA (see NewBaselineSchema): the
// 14-migration chain that preceded it existed only to walk developer databases
// forward one column at a time, and until v1.0.0 there is no released version to
// upgrade from — an existing instance is recreated, not migrated. The IDs of those
// removed migrations stay recorded in any database that ran them, which gormigrate
// tolerates because the RdbManager runs with ValidateUnknownMigrations off
// (backend/core/rdb). Don't enable that validation without first reconciling those
// orphaned rows.
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
	}
)
