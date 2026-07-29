// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// 🔴 WHAT pg_dump CANNOT SEE, AND WHY THAT MATTERS MORE HERE THAN ANYWHERE ELSE.
//
// `pg_dump --schema-only --schema <area>` captures relations in the area's schema. A
// TimescaleDB hypertable's identity does not live there — it lives in
// _timescaledb_catalog, in a different schema the dump never visits. So a plain table and
// a hypertable dump IDENTICALLY except for two incidental traces: the
// `<table>_occurred_time_idx` index Timescale creates for you, and (for a continuous
// aggregate) the generated view that references _timescaledb_internal.
//
// That is a false green waiting to happen, and a worse one than the line-diff defect this
// harness just had fixed. A flatten author works FROM the golden. The golden says there is
// an index named `events_occurred_time_idx`; writing `CREATE INDEX` for it and omitting
// `create_hypertable` reproduces the golden exactly, and `verify` says ok — while the
// service has six plain tables where it should have six hypertables. Five of the six were
// unprotected; only measurement_events was safe, and only incidentally, because creating
// the continuous aggregate over a plain table errors out.
//
// The consequence is not abstract. Losing Timescale's machinery on a real instance was
// measured during the ADR-028 event-store restore drill: the cluster reported healthy,
// the extension was still listed, the API answered, and every row in a compressed chunk
// returned as nothing at all.
//
// So the golden carries a probe of the Timescale catalog, emitted as pseudo-DDL lines
// that flow through the same normalize/split/compare pipeline as the dump. They are not
// replayable SQL and are not meant to be — a golden is a comparison artefact, not a
// restore script.
//
// WHAT IS DELIBERATELY LEFT OUT: compression and retention policies. Those are not
// migration state. event-management reconciles them at RUNTIME from configuration
// (model/lifecycle.go, ApplyDataLifecyclePolicies), so a migration chain never installs
// one and capturing them would make the golden depend on config that this harness does
// not set. Chunk-interval and hypertable-ness ARE migration state, created by
// create_hypertable in the chain, and those are what this captures.
const timescaleProbeSQL = `
SELECT line FROM (
  SELECT format('TIMESCALE HYPERTABLE %s DIMENSION %s INTERVAL %s COMPRESSION %s;',
                h.hypertable_name, d.column_name,
                coalesce(d.time_interval::text, d.integer_interval::text, 'none'),
                h.compression_enabled) AS line
    FROM timescaledb_information.hypertables h
    JOIN timescaledb_information.dimensions d
      ON d.hypertable_schema = h.hypertable_schema
     AND d.hypertable_name = h.hypertable_name
   WHERE h.hypertable_schema = $SCHEMA$
  UNION ALL
  SELECT format('TIMESCALE CONTINUOUS AGGREGATE %s MATERIALIZED_ONLY %s MATERIALIZATION %s;',
                view_name, materialized_only, materialization_hypertable_name)
    FROM timescaledb_information.continuous_aggregates
   WHERE view_schema = $SCHEMA$
  UNION ALL
  SELECT format('TIMESCALE POLICY %s ON %s SCHEDULE %s CONFIG %s;',
                j.proc_name, j.hypertable_name, j.schedule_interval, j.config)
    FROM timescaledb_information.jobs j
   WHERE j.hypertable_schema = $SCHEMA$
) probe ORDER BY line`

// probeTimescale returns the pseudo-DDL description of the area's TimescaleDB objects,
// or "" for an area that has none (which is eight of the ten).
//
// It runs through `docker exec ... psql` for the same reason dumpSchema does: the query
// executes against the server's own client, so it cannot drift from the server version,
// and it needs no second connection pool.
//
// It fails LOUDLY rather than degrading to "". A probe that silently returns nothing when
// the catalog view it needs has moved would hand back exactly the coverage this file
// exists to remove, while every area still printed ok — the failure mode being fixed, in
// the code doing the fixing.
func probeTimescale(container, user, db, schema string) (string, error) {
	// The schema name is interpolated as a dollar-quoted literal rather than passed as a
	// psql parameter because -Atc takes no bind parameters. Area names come from
	// registry.go, not from input, and are matched against the literal set below so a
	// name that could break out of the quoting cannot reach the server.
	if strings.ContainsAny(schema, `$'"\;`) {
		return "", fmt.Errorf("refusing to probe a schema name containing quoting characters: %q", schema)
	}
	query := strings.ReplaceAll(timescaleProbeSQL, "$SCHEMA$", "$dc$"+schema+"$dc$")

	cmd := exec.Command("docker", "exec", container,
		"psql", "-U", user, "-d", db, "-Atc", query)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("probing the timescale catalog: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
