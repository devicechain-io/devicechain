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
// CHANGING THE SCHEMA: append a migration here. Do NOT rely on the baseline's
// AutoMigrate to converge a model change, and never edit the baseline — it builds
// from its own frozen snapshot types precisely so it does not track the live models
// (see baseline_snapshot.go and .agent-os/product/data-modeling.md). A new migration
// declares its own snapshot of just what it touches. Anything appended must be
// individually re-runnable, since migrations run with UseTransaction:false and replay
// from the top after a failure. These are all folded back into the baseline at the GA
// squash.
var (
	Migrations = []*gormigrate.Migration{
		NewBaselineSchema(),
		// ADR-065 S5c: display_order + color on iam_tenant_tiers (presentation only).
		NewTierPresentationSchema(),
	}
)
