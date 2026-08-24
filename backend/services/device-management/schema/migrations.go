// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
)

// Migrations run in slice order (not by ID), so a new migration must be appended last.
//
// This area was collapsed to a single baseline pre-GA (see NewBaselineSchema): the TWENTY-SIX
// migration chain that preceded it — the largest of the nine areas — existed only to walk
// developer databases forward, and until v1.0.0 there is no released version to upgrade from.
// An existing instance is recreated, not migrated. The IDs of those removed migrations stay
// recorded in any database that ran them, which gormigrate tolerates because the RdbManager runs
// with ValidateUnknownMigrations off (backend/core/rdb). Don't enable that validation without
// first reconciling those orphaned rows.
//
// CHANGING THE SCHEMA: append a migration here. Do NOT rely on the baseline's AutoMigrate to
// converge a model change, and never edit the baseline — it builds from its own frozen snapshot
// types precisely so it does not track the live models (see baseline_snapshot.go and
// .agent-os/product/data-modeling.md).
var (
	Migrations = []*gormigrate.Migration{
		NewBaselineSchema(),
		NewProfileLocationDeclarationSchema(),
		NewGeoFencesSchema(),
		NewListOrderIndexesSchema(),
		NewGeoFenceGeometryBlobsSchema(),
	}
)
