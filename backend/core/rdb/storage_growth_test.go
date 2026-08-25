// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"gorm.io/gorm"
)

// The growth collector exists to answer "how fast is the append-only history growing", and
// its one hard requirement is that it must never answer that question WRONGLY while blind.
// These tests drive it through its injected catalog read.
//
// 🔴 WHAT THEY DO NOT COVER: the catalog SQL itself. The seam they use replaces it, so every
// test here would pass against a statement PostgreSQL rejects outright. The unit suite runs
// on SQLite, which has neither pg_class nor to_regclass, so there is nowhere in it that the
// real query can be exercised — it is established only against a live database. Said plainly
// rather than left for someone to infer from a green suite.

// The fully-qualified metric names, written out rather than rebuilt from the namespace and
// subsystem the collector is constructed with. A dashboard or alert matches this literal
// text, so if the prefix ever moves, this is where it should fail.
const (
	rowsMetric  = "devicechain_devicemanagement_append_only_table_rows_estimate"
	bytesMetric = "devicechain_devicemanagement_append_only_table_bytes"
)

// testMicroservice is a microservice whose functional area exercises the hyphen-stripping
// in MetricsSubsystem, so the fully-qualified names above are produced by the real rule
// rather than assembled by the test.
func testMicroservice() *core.Microservice {
	return &core.Microservice{FunctionalArea: "device-management"}
}

// collected reads the collector's output into a comparable form.
func collected(t *testing.T, c prometheus.Collector) map[string]float64 {
	t.Helper()
	// Buffered past any plausible table count: Collect is called synchronously here, so a
	// channel that fills would DEADLOCK rather than fail, and a deadlocked test reads as a
	// timeout with no explanation.
	ch := make(chan prometheus.Metric, 4*len(wantAppendOnlyTables)+16)
	c.Collect(ch)
	close(ch)

	out := map[string]float64{}
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatalf("write metric: %v", err)
		}
		// 🔴 MATCHED ON THE DELIMITED fqName, NOT ON A BARE SUBSTRING OF THE WHOLE Desc.
		// Desc.String() embeds the HELP text, so a substring test would reclassify every
		// rows series the day the rows help mentions the bytes metric by name — and since
		// both carry the same table label, the two would collide on one map key and the
		// last one written would silently win.
		desc := m.Desc().String()
		var name string
		switch {
		case strings.Contains(desc, `fqName: "`+rowsMetric+`"`):
			name = "rows"
		case strings.Contains(desc, `fqName: "`+bytesMetric+`"`):
			name = "bytes"
		default:
			t.Fatalf("collector emitted a series this test does not recognise: %s", desc)
		}
		var table string
		for _, l := range pb.GetLabel() {
			if l.GetName() == "table" {
				table = l.GetValue()
			}
		}
		out[name+"/"+table] = pb.GetGauge().GetValue()
	}
	return out
}

// withMeasure builds a collector whose catalog read is the given function.
func withMeasure(f func(ctx context.Context, db *gorm.DB, table string) (int64, int64, bool)) *StorageGrowthCollector {
	c := NewStorageGrowthCollector(testMicroservice(), nil,
		&GeoFenceSetVersion{}, &GeoFenceGeometryBlob{},
		&DeviceProfileVersion{}, &EntityGroupVersion{})
	c.measure = f
	return c
}

// Stand-ins for the caller-supplied models. core cannot import a service, so these carry
// the real models' TYPE NAMES — deliberately, and it is the whole point: the table label a
// dashboard matches is derived from the type name by gorm's naming strategy, so a stand-in
// named anything else would test the strategy against a string this platform never uses.
// Naming them exactly as device-management's models is what makes the expectations below a
// statement about the real tables.
type GeoFenceSetVersion struct{ ID uint }
type GeoFenceGeometryBlob struct{ ID uint }
type DeviceProfileVersion struct{ ID uint }
type EntityGroupVersion struct{ ID uint }

// 🔴 A TABLE THAT CANNOT BE MEASURED PRODUCES NO SERIES, NOT A ZERO. This is the entire
// reason the collector is a Collector rather than a GaugeFunc: a GaugeFunc must return a
// number, and the only number available on failure is 0 — which is indistinguishable from a
// genuinely empty table, so the instrument would report "nothing is growing" exactly when it
// had gone blind.
//
// The control is in the same test: one table succeeds while the other fails, so the absence
// is attributable to the failure and not to a collector that emits nothing at all.
func TestGrowthCollectorOmitsATableItCannotMeasure(t *testing.T) {
	c := withMeasure(func(_ context.Context, _ *gorm.DB, table string) (int64, int64, bool) {
		if table == "audit_events" {
			return 0, 0, false
		}
		return 12, 3400, true
	})

	got := collected(t, c)
	if _, present := got["rows/audit_events"]; present {
		t.Error("an unmeasurable table reported a row estimate; it must report nothing at all, " +
			"because 0 is a legitimate reading and would read as an empty table")
	}
	if _, present := got["bytes/audit_events"]; present {
		t.Error("an unmeasurable table reported a size")
	}
	// The control: every other table came through, so the absence above is about the failure.
	if got["rows/device_profile_versions"] != 12 || got["bytes/device_profile_versions"] != 3400 {
		t.Fatalf("a measurable table reported %v; the omission above proves nothing if the "+
			"collector emits nothing for everything", got)
	}
}

// A table PostgreSQL has never analyzed reports reltuples = -1, which means "unknown" and is
// not a count. The estimate is withheld; the size, which is exact either way, is not.
func TestGrowthCollectorWithholdsANeverAnalyzedEstimateButStillReportsSize(t *testing.T) {
	c := withMeasure(func(_ context.Context, _ *gorm.DB, table string) (int64, int64, bool) {
		return -1, 8192, true
	})

	got := collected(t, c)
	for _, table := range wantAppendOnlyTables {
		if v, present := got["rows/"+table]; present {
			t.Errorf("%s reported a row estimate of %v from an unanalyzed table; -1 means unknown "+
				"and would graph as a dip below zero on every fresh install", table, v)
		}
		if got["bytes/"+table] != 8192 {
			t.Errorf("%s did not report its size, which is exact regardless of the estimate", table)
		}
	}
}

// 🔴 THE EXPECTED TABLES ARE WRITTEN OUT HERE, NOT READ FROM THE COLLECTOR. Comparing
// the collector's output against the very list that produced it is a tautology: it holds
// for any list at all, including an empty one, so dropping a table from the production
// list passes unnoticed. Mutation testing found exactly that — this test read
// the collector's own list and survived the removal of entity_group_versions.
//
// Writing the names out means this test is a SPECIFICATION for the names themselves — a
// model renamed, or gorm's naming strategy changed, fails here rather than silently
// orphaning a dashboard that matches the old label.
//
// 🔴 IT CATCHES REMOVAL, NOT OMISSION. Adding an append-only table to a service and
// passing it to neither the collector nor this list still passes green. Nothing can catch
// that, because nothing can enumerate the tables a service MEANT to declare.
var wantAppendOnlyTables = []string{
	"audit_events",
	"device_profile_versions",
	"entity_group_versions",
	"geo_fence_geometry_blobs",
	"geo_fence_set_versions",
}

// Every append-only table in the schema is reported, and each is labelled with its own name.
func TestGrowthCollectorReportsEveryAppendOnlyTable(t *testing.T) {
	c := withMeasure(func(_ context.Context, _ *gorm.DB, table string) (int64, int64, bool) {
		return 1, 2, true
	})

	got := collected(t, c)
	seen := make([]string, 0, len(got))
	for key := range got {
		if strings.HasPrefix(key, "bytes/") {
			seen = append(seen, strings.TrimPrefix(key, "bytes/"))
		}
	}
	sort.Strings(seen)
	want := append([]string(nil), wantAppendOnlyTables...)
	sort.Strings(want)
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("collector reported %v, want %v", seen, want)
	}
}

// The metric names, help text and label names satisfy Prometheus's own conventions —
// checked with the client library's linter rather than by eye, since the failure mode of a
// hand-checked name is a metric that scrapes fine and cannot be queried the expected way.
func TestGrowthCollectorMetricsAreWellFormed(t *testing.T) {
	c := withMeasure(func(_ context.Context, _ *gorm.DB, table string) (int64, int64, bool) {
		return 5, 6, true
	})

	problems, err := testutil.CollectAndLint(c)
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	for _, p := range problems {
		t.Errorf("%s: %s", p.Metric, p.Text)
	}
	// Two series per table, and the control that the linter saw anything at all.
	if n := testutil.CollectAndCount(c); n != 2*len(wantAppendOnlyTables) {
		t.Fatalf("collected %d series, want %d", n, 2*len(wantAppendOnlyTables))
	}
}

// 🔴 AN ANALYZED-BUT-EMPTY TABLE REPORTS ZERO ROWS, AND ZERO IS A READING. The gate that
// withholds an unanalyzed estimate is `rows >= 0`, and every other case in this file feeds
// it a positive number or -1 — so narrowing it to `rows > 0` passes them all while making a
// genuinely empty table indistinguishable from one that could not be measured. That is a
// direct inversion of the absent-versus-zero distinction this collector exists to preserve,
// so it gets the case that pins it.
func TestGrowthCollectorReportsAZeroRowEstimate(t *testing.T) {
	c := withMeasure(func(_ context.Context, _ *gorm.DB, table string) (int64, int64, bool) {
		return 0, 8192, true
	})

	got := collected(t, c)
	for _, table := range wantAppendOnlyTables {
		v, present := got["rows/"+table]
		if !present {
			t.Errorf("%s reported no row estimate for an empty table; absent means UNMEASURABLE "+
				"here, and an empty table was measured perfectly well", table)
			continue
		}
		if v != 0 {
			t.Errorf("%s reported %v rows, want 0", table, v)
		}
	}
}
