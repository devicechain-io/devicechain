// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

// Migrations is the ordered gormigrate migration list for the event-processing service, run
// under a Postgres advisory lock at startup (see rdb.RdbManager). They run in slice order (not
// by ID), so a new migration must be appended last.
//
// This area was collapsed to a single baseline pre-GA (see NewBaselineSchema): the six-migration
// chain that preceded it existed only to walk developer databases forward, and until v1.0.0 there
// is no released version to upgrade from — an existing instance is recreated, not migrated ONTO
// THE BASELINE — which is not the same claim as "an existing instance is never migrated", and
// reading it as the second one is how device-management's geometry archive shipped without the
// backfill an upgraded instance needed (#838). Everything APPENDED below the baseline runs on
// live databases: v0.11.0 and v0.12.0 were each reached with `helm upgrade`. The IDs of those
// removed migrations stay recorded in any database that ran them, which gormigrate tolerates
// because the RdbManager runs with ValidateUnknownMigrations off (backend/core/rdb). Don't enable
// that validation without first reconciling those orphaned rows.
//
// CHANGING THE SCHEMA: append a migration here. Do NOT rely on the baseline's AutoMigrate to
// converge a model change, and never edit the baseline — it builds from its own frozen
// snapshot types precisely so it does not track the live models (see baseline_snapshot.go and
// .agent-os/product/data-modeling.md).
var (
	Migrations = []*gormigrate.Migration{
		NewBaselineSchema(),
	}
)
