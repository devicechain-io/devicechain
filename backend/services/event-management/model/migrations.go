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
//
// 🔴 THE BASELINE WAS RE-CUT ONCE AFTER THE SQUASH, and this is the record of it: the base event
// gained event_id and its primary key moved from the natural key to (tenant_id, event_id,
// occurred_time). It was folded into the baseline rather than appended, deliberately and as a
// pre-GA decision, because an APPENDED version could not be made safe: compression is enabled on
// these hypertables by the data-lifecycle reconciler, and Timescale forbids altering a primary key
// on a hypertable with compressed chunks — so the migration would have had to decompress every
// chunk, alter, and recompress. The content digest also cannot be recomputed in SQL (the canonical
// payload is not reconstructible there), so existing rows had no correct backfill.
//
// The cost, accepted knowingly: an instance created before this change is RECREATED
// (dcctl destroy + bootstrap), not upgraded. That includes v0.9.x. Do not read this as licence to
// re-cut the baseline again — it was justified by a defect that made stored data wrong, on a
// schema no released instance can carry forward, and the next schema change appends.
//
// 🔴 IT HAS SINCE BEEN RE-CUT A SECOND TIME, ON A DIFFERENT AND NARROWER BASIS, and this record
// says so rather than leaving the count wrong. That one (PR #874) made the baseline survive its
// own replay — the collation skip, the derived index names, and create-if-absent for the
// aggregate — and changed RE-RUNNABILITY ONLY: verify and replay proved the schema
// byte-identical on both Postgres majors, so no golden moved and no instance was recreated. The
// paragraph above is about a re-cut that CHANGES what a fresh install builds, and that one still
// costs a recreate and still needs a defect of that size to justify it. See CLAUDE.md.
var (
	Migrations = []*gormigrate.Migration{
		NewBaselineSchema(),
		// The first migration appended after the baseline, and therefore the worked
		// example for the next one — read its doc comment before adding another.
		NewLocationFixFieldsSchema(),
	}
)
