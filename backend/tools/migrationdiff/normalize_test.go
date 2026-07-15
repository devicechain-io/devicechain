// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

func TestNormalizeStripsNoiseAndSorts(t *testing.T) {
	// Two dumps of the same two objects in a DIFFERENT order, with the usual pg_dump
	// preamble noise. They must normalize equal — object creation order is not schema.
	a := `--
-- PostgreSQL database dump
--
SET statement_timeout = 0;
SELECT pg_catalog.set_config('search_path', '', false);
\connect dcmigrationdiff

CREATE TABLE "area".b (id integer);

CREATE TABLE "area".a (id integer);
`
	b := `SET client_encoding = 'UTF8';
CREATE TABLE "area".a (id integer);
CREATE TABLE "area".b (id integer);
`
	if normalizeDump(a) != normalizeDump(b) {
		t.Fatalf("same objects in different order should normalize equal:\n--a--\n%s\n--b--\n%s", normalizeDump(a), normalizeDump(b))
	}
}

func TestNormalizeCatchesStructuralDifference(t *testing.T) {
	base := "CREATE TABLE \"area\".a (id integer);\n"
	withIndex := base + "CREATE INDEX a_idx ON \"area\".a (id);\n"
	if normalizeDump(base) == normalizeDump(withIndex) {
		t.Fatal("an extra index must NOT normalize equal — that is exactly what the harness must catch")
	}

	diffType := "CREATE TABLE \"area\".a (id bigint);\n"
	if normalizeDump(base) == normalizeDump(diffType) {
		t.Fatal("a different column type must NOT normalize equal")
	}
}

func TestNormalizeScrubsTimescaleInternalIDs(t *testing.T) {
	// The same continuous-aggregate view built after a different number of prior
	// hypertables gets a different internal id — not a schema difference.
	chain := `CREATE VIEW "area".rollups AS SELECT _materialized_hypertable_6.x FROM _timescaledb_internal._materialized_hypertable_6 WHERE (_materialized_hypertable_6.bucket < COALESCE(cagg_watermark(6), '-infinity'));`
	baseline := strings.ReplaceAll(strings.ReplaceAll(chain, "_materialized_hypertable_6", "_materialized_hypertable_9"), "cagg_watermark(6)", "cagg_watermark(9)")
	if normalizeDump(chain) != normalizeDump(baseline) {
		t.Fatalf("timescale internal ids must be scrubbed to compare equal:\n--chain--\n%s\n--baseline--\n%s", normalizeDump(chain), normalizeDump(baseline))
	}
}

func TestNormalizeCollapsesWhitespace(t *testing.T) {
	if normalizeDump("CREATE   TABLE\t\"area\".a (id   integer);") != normalizeDump("CREATE TABLE \"area\".a (id integer);") {
		t.Fatal("incidental whitespace must not create a false diff")
	}
}
