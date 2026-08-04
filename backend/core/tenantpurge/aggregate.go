// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// A TimescaleDB continuous aggregate keeps a PHYSICAL COPY of its rows, and nothing in
// the schema scan can see it.
//
// The aggregate a service writes looks like a view, and Postgres agrees: relkind 'v',
// which loadColumns excludes because a view holds no rows of its own. That is true of an
// ordinary view and false of this one. The rows live in a MATERIALIZATION HYPERTABLE
// under _timescaledb_internal, which isSystemSchema skips — correctly, because that
// schema is otherwise full of chunks whose parents are already in the plan and would be
// erased twice.
//
// So without this file the aggregate falls through BOTH exclusions and is retained
// silently: a tenant's per-device, per-metric aggregate buckets survive a sweep that
// deleted every raw measurement they were computed from, and every check the purge has
// reports clean. The raw delete does not reach them (the materialization is a separate
// table), and the refresh policy never would (it recomputes a moving recent window, so a
// bucket outside it is never revisited).
//
// # Why it goes in the PLAN rather than being a step the caller runs afterwards
//
// A step is only ever as complete as the person who wrote it. Folding the materialization
// into the plan makes it subject to the same fail-closed rule as everything else: it is
// classified from its own columns, so an aggregate whose materialization carries no
// recognisable tenant column lands in ClassUnclassified and stops the purge, instead of
// being skipped by a step that only knew about the aggregates that existed the day it was
// written. It also means the coverage gate — which classifies a database with every
// area's migrations applied — sees a new aggregate the first time CI runs.

// aggregate is one continuous aggregate and the hypertable holding its materialized rows.
type aggregate struct {
	ViewSchema string
	ViewName   string
	MatSchema  string
	MatName    string
}

// view is the aggregate as a reader knows it — the name a migration wrote.
func (a aggregate) view() Table { return Table{Schema: a.ViewSchema, Name: a.ViewName} }

// materialization is the table the rows are actually in.
func (a aggregate) materialization() Table { return Table{Schema: a.MatSchema, Name: a.MatName} }

// origin is the provenance sentence carried on the materialization's plan entry, because
// "_timescaledb_internal._materialized_hypertable_7" names nothing a maintainer can act
// on. Whoever trips the fail-closed gate on one of these needs to be told which aggregate
// it belongs to, in the message itself.
func (a aggregate) origin() string {
	return "the materialized rows of continuous aggregate " + a.view().String()
}

// loadContinuousAggregates lists every continuous aggregate in the database.
//
// The extension probe is not defensive padding. timescaledb_information exists only where
// the extension is installed, and ten of the eleven functional areas share a plain
// Postgres cluster where it is not — so querying the view unconditionally would fail the
// purge of the relational store with "relation does not exist", i.e. would break the store
// that has no aggregates in order to handle the one that does. Probing pg_extension asks
// the question directly rather than swallowing an error whose text would also cover a
// genuine failure.
func loadContinuousAggregates(ctx context.Context, db *gorm.DB) ([]aggregate, error) {
	var installed bool
	if err := db.WithContext(ctx).Raw(
		`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb')`).
		Scan(&installed).Error; err != nil {
		return nil, fmt.Errorf("checking for the timescaledb extension: %w", err)
	}
	if !installed {
		return nil, nil
	}

	var out []aggregate
	err := db.WithContext(ctx).Raw(`
		SELECT view_schema                        AS view_schema,
		       view_name                          AS view_name,
		       materialization_hypertable_schema  AS mat_schema,
		       materialization_hypertable_name    AS mat_name
		FROM timescaledb_information.continuous_aggregates
		ORDER BY 1, 2`).Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("read continuous aggregates: %w", err)
	}
	return out, nil
}
