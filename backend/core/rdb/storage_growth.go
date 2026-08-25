// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// tableNameOf resolves a model to its BARE table name — no schema prefix.
//
// 🔴 BARE, NOT THE NAME GORM ACTUALLY USES, and the difference is deliberate. The live
// naming strategy carries a TablePrefix of "<functional-area>." so gorm addresses tables
// as "device-management".foo; a name built that way would have to be quoted to survive
// to_regclass, because a functional area contains a hyphen. Every connection pins its
// search_path to its own service's schema, and a service reports only its OWN tables, so
// the bare name resolves to exactly the right relation with no quoting to get wrong.
func tableNameOf(model any) (string, error) {
	parsed, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		return "", fmt.Errorf("unable to resolve a table name for %T: %w", model, err)
	}
	return parsed.Table, nil
}

// storageGrowthTimeout bounds a scrape's catalog reads — ALL of them together, not each in
// turn. Both values come from the catalog rather than from the tables, so this is generous
// by a wide margin; it exists so a database that has stopped answering costs the scrape a
// bounded wait and an ABSENT series, not a hung handler.
//
// The shared budget means one pathological read can starve the tables after it into absence
// as well. That is tolerable precisely because absent-when-blind is the design here — the
// blindness is reported either way — but it does mean absence attributes the fault to the
// wrong table. A per-table budget would attribute it correctly and is the change to make if
// that ever matters.
const storageGrowthTimeout = 2 * time.Second

// StorageGrowthCollector reports the size of a service's append-only tables — the ones
// that only ever GROW, because nothing prunes them: each is a history whose rows are
// deleted only when the whole tenant is purged.
//
// They are instrumented together rather than one at a time because they are one problem
// wearing several hats — a tenant's accumulated history is an uncapped at-rest resource no
// matter which entity it belongs to, and an instrument watching only the table somebody
// happened to be looking at would report on the wrong one first.
//
// 🔴 IT IS A Collector, NOT A GaugeFunc, AND THE DIFFERENCE IS THE WHOLE POINT. A GaugeFunc
// must return a float64 on every scrape, so a failed query can only report 0 — and 0 is a
// perfectly legitimate reading here. The instrument would report "nothing is growing"
// exactly when it had gone blind. A Collector may emit NOTHING for a table it could not
// measure, which is a state a rule can see (`absent()`) and a human can tell apart from an
// empty table.
//
// 🔴 THE NUMBERS ARE INSTANCE-WIDE, SPANNING EVERY TENANT, and deliberately carry no tenant
// label. One database per instance, one schema per service, one table shared by all tenants
// under a tenant_id predicate — so the catalog answers for the instance and cannot answer
// per tenant without a GROUP BY scan. A tenant label would also be unbounded cardinality,
// which is the trap this codebase avoids everywhere else. When something here fires, the
// question "which tenant" is answered by the runbook query on the alert, not by the metric.
//
// 🔴 EVERY REPLICA EXPORTS THE SAME VALUE. These describe a shared table, not this pod's
// share of it, so anything consuming them must aggregate with max (max by (namespace)),
// never sum — a sum multiplies the truth by the replica count and jumps during a rollout.
type StorageGrowthCollector struct {
	db     *gorm.DB
	tables []string
	// measure is the catalog read, held as a field so a test can drive the emission rules
	// below — absent-on-failure, the negative-estimate gate, the table label — without a
	// live PostgreSQL. Production always uses measureFromCatalog.
	//
	// 🔴 THE SEAM DOES NOT VALIDATE THE SQL, AND NOTHING IN THE UNIT SUITE DOES. Substituting
	// this function tests the logic AROUND the query and never the query itself; the tests
	// here would pass against a statement PostgreSQL rejects. What the catalog read actually
	// returns is established only where a real database runs.
	measure   func(ctx context.Context, db *gorm.DB, table string) (rows int64, bytes int64, ok bool)
	rowsDesc  *prometheus.Desc
	bytesDesc *prometheus.Desc
}

// NewStorageGrowthCollector builds the collector for one service, over the models whose
// tables only ever grow there. AuditEvent is prepended unconditionally, so a service that
// declares nothing still reports the one append-only table it cannot avoid having.
//
// 🔴 AuditEvent IS THE REASON THIS LIVES IN core AND NOT IN A SERVICE. It is AutoMigrated
// into EVERY service's schema and takes a row per entity mutation by construction, and
// nothing prunes it — which plausibly makes it the fastest-growing append-only relational
// table on the platform. A list written inside any one service was never going to contain
// it, and the first version of this collector did not.
//
// It takes the UNSCOPED handle on purpose. This is an instance-wide measurement with no
// tenant in play, and the catalog query names no tenant-scoped model, so the tenant-scope
// callback lets it through untouched rather than refusing it — the same route the tenant
// purge's own catalog probe takes.
//
// A model whose table name cannot be resolved is logged and dropped rather than failing
// startup: an instrument is never worth refusing to boot for.
func NewStorageGrowthCollector(ms *core.Microservice, db *gorm.DB, models ...any) *StorageGrowthCollector {
	tables := make([]string, 0, len(models)+1)
	for _, model := range append([]any{&AuditEvent{}}, models...) {
		name, err := tableNameOf(model)
		if err != nil {
			log.Error().Err(err).Msg("Unable to instrument an append-only table; it will not be reported")
			continue
		}
		tables = append(tables, name)
	}
	namespace, subsystem := core.METRICS_NAMESPACE, ms.MetricsSubsystem()
	return &StorageGrowthCollector{
		db:      db,
		tables:  tables,
		measure: measureFromCatalog,
		rowsDesc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "append_only_table_rows_estimate"),
			"Estimated live rows in an append-only history table, from the planner statistics "+
				"(pg_class.reltuples) rather than a count. Absent when the estimate could not be read, "+
				"including before the table has first been analyzed. Instance-wide across all tenants; "+
				"aggregate with max, never sum.",
			[]string{"table"}, nil),
		bytesDesc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, subsystem, "append_only_table_bytes"),
			"Total on-disk bytes of an append-only history table including indexes and TOAST. Nothing "+
				"prunes these tables, so this only rises within the life of a tenant. Instance-wide "+
				"across all tenants; aggregate with max, never sum.",
			[]string{"table"}, nil),
	}
}

// Describe implements prometheus.Collector.
func (c *StorageGrowthCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.rowsDesc
	ch <- c.bytesDesc
}

// Collect implements prometheus.Collector, reading both figures for each table from the
// system catalog. It emits nothing for a table it cannot measure.
func (c *StorageGrowthCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), storageGrowthTimeout)
	defer cancel()

	for _, table := range c.tables {
		rows, bytes, ok := c.measure(ctx, c.db, table)
		if !ok {
			continue
		}
		// A table that has never been analyzed reports reltuples = -1, which is "unknown"
		// and not a count. Emitting it would draw a graph dipping below zero on every fresh
		// install; the size below is exact regardless, so only the estimate is withheld.
		if rows >= 0 {
			ch <- prometheus.MustNewConstMetric(c.rowsDesc, prometheus.GaugeValue, float64(rows), table)
		}
		ch <- prometheus.MustNewConstMetric(c.bytesDesc, prometheus.GaugeValue, float64(bytes), table)
	}
}

// measureFromCatalog reads one table's row estimate and total size from the catalog, reporting
// whether it could be read at all.
//
// to_regclass rather than a ::regclass cast: the cast RAISES for a name that does not
// resolve, which would make a service whose migrations had not yet run log an error per
// table per scrape. to_regclass answers NULL, the join finds nothing, and the table is
// simply not reported — which is what an unmeasurable table should look like.
//
// The name is resolved against the connection's search_path, which is pinned to this
// service's schema. That matters because the instance database hosts EVERY service's
// schema: matching on relname instead would be ambiguous by construction the moment two
// services name a table the same way.
func measureFromCatalog(ctx context.Context, db *gorm.DB, table string) (rows int64, bytes int64, ok bool) {
	const query = `SELECT c.reltuples::bigint, pg_total_relation_size(c.oid)
		FROM pg_class c WHERE c.oid = to_regclass(?)`
	row := db.WithContext(ctx).Raw(query, table).Row()
	if row == nil {
		return 0, 0, false
	}
	if err := row.Scan(&rows, &bytes); err != nil {
		// Debug, not warn: on a database that is down this fires once per table per scrape,
		// and the ABSENT series is the signal an operator acts on. A log line per table per
		// fifteen seconds would bury it.
		log.Debug().Err(err).Str("table", table).Msg("Unable to measure append-only table growth")
		return 0, 0, false
	}
	return rows, bytes, true
}
