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
// Until v1.0.0 freezes this baseline, a purely ADDITIVE model change (a new column,
// a new table) needs no migration at all — the baseline's AutoMigrate converges an
// existing database on the current structs. Append a migration here only for what
// AutoMigrate cannot express: a backfill, or a constraint applied over existing
// rows. Anything appended must be individually re-runnable, since migrations run
// with UseTransaction:false and replay from the top after a failure.
var (
	Migrations = []*gormigrate.Migration{
		NewBaselineSchema(),
	}
)
