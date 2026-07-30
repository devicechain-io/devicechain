// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

// Migrations run in slice order (not by ID), so a new migration must be appended last.
//
// This area was collapsed to a single baseline pre-GA (see NewBaselineSchema): the
// seven-migration chain that preceded it existed only to walk developer databases forward, and
// until v1.0.0 there is no released version to upgrade from — an existing instance is recreated,
// not migrated. The IDs of those removed migrations stay recorded in any database that ran them,
// which gormigrate tolerates because the RdbManager runs with ValidateUnknownMigrations off
// (backend/core/rdb). Don't enable that validation without first reconciling those orphaned rows.
//
// CHANGING THE SCHEMA: append a migration here. Never edit the baseline — it builds from its own
// frozen snapshot types precisely so it does not track the live models (see baseline_snapshot.go
// and .agent-os/product/data-modeling.md). Anything appended must be individually re-runnable:
// this area's DDL is non-transactional (Timescale forbids it), so a half-applied migration is
// never rolled back and replays from the top on the next boot.
var (
	Migrations = []*gormigrate.Migration{
		NewBaselineSchema(),
	}
)
