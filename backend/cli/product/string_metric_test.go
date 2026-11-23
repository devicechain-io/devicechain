// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import (
	"path/filepath"
	"strings"
	"testing"
)

// stringMetricFixture is a profile whose only metric is a STRING one: no unit, bounds, allow-list
// or expression, which is the whole declaration the server accepts for text.
func stringMetricFixture(t *testing.T) *Bundle {
	t.Helper()
	b := &Bundle{
		Profiles: []Profile{{
			Token: "washer", Name: "Washer",
			Metrics: []Metric{
				{Token: "m-cycle", Key: "cycle", DataType: "STRING"},
				{Token: "m-rpm", Key: "rpm", DataType: "INT", Unit: "/min"},
			},
		}},
		DeviceTypes: []DeviceType{{Token: "washer-v1", Name: "Washer v1", Profile: "washer"}},
	}
	if err := b.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("a STRING metric must validate: %v", err)
	}
	return b
}

// A STRING metric survives the bundle: it validates, builds an input that carries no unit or
// bounds, applies, reads back, and then neither plans a change nor exports differently.
func TestAStringMetricSurvivesApplyPlanAndExport(t *testing.T) {
	b := stringMetricFixture(t)

	in := b.Profiles[0].Metrics[0].input()
	if in["dataType"] != "STRING" || in["metricKey"] != "cycle" {
		t.Fatalf("input = %v", in)
	}
	created := createInput(in, nil)
	for _, k := range []string{"unit", "minValue", "maxValue", "enum", "expression"} {
		if v, ok := created[k]; ok {
			t.Errorf("the create request carries %s=%v for a STRING metric", k, v)
		}
	}

	dir := filepath.Join(t.TempDir(), "bundle")
	if err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
	f := newFake(t)
	apply(t, f, dir, env(nil))

	var found bool
	for _, m := range f.metrics {
		if m["metricKey"] == "cycle" {
			found = true
			if m["dataType"] != "STRING" {
				t.Errorf("stored dataType = %v", m["dataType"])
			}
		}
	}
	if !found {
		t.Fatal("the STRING metric was not created")
	}

	// A live read-back diffs clean: no phantom drift on the absent unit and bounds.
	before := f.writes
	p := plan(t, f, dir, env(nil))
	if !p.Empty() || len(p.Blockers) != 0 {
		var sb strings.Builder
		p.Fprint(&sb)
		t.Fatalf("a STRING metric reads back as drift:\n%s", sb.String())
	}
	apply(t, f, dir, env(nil))
	if f.writes != before {
		t.Fatalf("a re-apply wrote %d time(s)", f.writes-before)
	}

	// And the export of the tenant is the bundle, byte for byte.
	out := filepath.Join(t.TempDir(), "out")
	export(t, f, out)
	sameTree(t, tree(t, dir), tree(t, out))
}

// A STRING metric is a measured one: it cannot be written as a calculated metric.
func TestAStringMetricStillNeedsItsKeyAndType(t *testing.T) {
	b := stringMetricFixture(t)
	b.Profiles[0].Metrics[0].Key = ""
	if err := b.Validate(); err == nil {
		t.Fatal("a metric with no key validated")
	}
}
