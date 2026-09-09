// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"reflect"
	"sort"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// labelNamesArg is the type a metric constructor takes label names in. Held as a variable
// rather than written inline so the two tests below agree on what they are looking for.
var labelNamesArg = reflect.TypeOf([]string(nil))

// TestUnlabelledMetricConstructorsTakeNoLabelNames is the gate on the rule that a metric
// constructor accepts a label-name argument IF AND ONLY IF the collector it returns has
// label dimensions to put them on.
//
// 🔴 It is a signature assertion, and it is a signature assertion on purpose. NewCounter
// and NewGauge each used to take a labels []string and never read it. Nothing could
// observe that from the outside: the collector they return has no dimensions, so there is
// no series, no error and no log line in which the dropped argument shows up — the
// evidence is the ABSENCE of a label on a metric that never had one. A caller writing
//
//	ms.NewCounter("events_total", "…", []string{"tenant"})
//
// got a counter that registered, incremented and exported with no tenant label, and found
// out when a dashboard grouped by a label the series did not carry, at which point the
// dashboard is the natural suspect. Go is no help either, because an unused PARAMETER is
// not a compile error the way an unused local is.
//
// So the fix was to take the argument away, which turns that call site into a compile
// error, and this test is what stops it coming back. The pressure to bring it back is
// real: NewCounterVec sits twenty lines below NewCounter with the identical parameter
// list, which makes the shorter signature look like an oversight rather than the point.
//
// The two Vec rows are the counterweight. Without them this test would be satisfied by
// removing label support from the library altogether.
func TestUnlabelledMetricConstructorsTakeNoLabelNames(t *testing.T) {
	cases := []struct {
		name string
		fn   any
		// wantLabelNames is whether this constructor's return type has label dimensions,
		// and therefore whether it may accept label names.
		wantLabelNames bool
		returns        string
	}{
		{"NewCounter", (*Microservice).NewCounter, false, "prometheus.Counter, one series with no dimensions"},
		{"NewGauge", (*Microservice).NewGauge, false, "prometheus.Gauge, one series with no dimensions"},
		{"NewCounterVec", (*Microservice).NewCounterVec, true, "*prometheus.CounterVec"},
		{"NewGaugeVec", (*Microservice).NewGaugeVec, true, "*prometheus.GaugeVec"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ft := reflect.TypeOf(tc.fn)
			got := false
			for i := 0; i < ft.NumIn(); i++ {
				if ft.In(i) == labelNamesArg {
					got = true
				}
			}
			switch {
			case got && !tc.wantLabelNames:
				t.Errorf("%s takes a %s of label names, but it returns %s. There is nothing "+
					"for those names to be applied to, so the argument can only be dropped — "+
					"silently, at every call site, with no error on any path. Take the "+
					"parameter away and let the compiler report the call sites; a caller that "+
					"wants labels wants %sVec.", tc.name, labelNamesArg, tc.returns, tc.name)
			case !got && tc.wantLabelNames:
				t.Errorf("%s no longer takes a %s of label names, so nothing can build a "+
					"labelled metric through it", tc.name, labelNamesArg)
			}
		})
	}
}

// TestMetricConstructorsExportTheLabelsTheyPromise is the same rule measured at the other
// end — on the exposition, where a consumer of these metrics meets them.
//
// It is the recorded cost of the defect above as much as a guard: the labelled
// constructors put exactly the names they were given on the exported series, and the
// unlabelled ones export a series with no label pairs at all. That second half is what a
// caller passing label names to NewCounter used to receive, and gathering is the only
// place it was ever visible.
func TestMetricConstructorsExportTheLabelsTheyPromise(t *testing.T) {
	ms := &Microservice{FunctionalArea: "labelprobe"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())

	ms.NewCounter("plain_counter_total", "An unlabelled counter.").Inc()
	ms.NewGauge("plain_gauge", "An unlabelled gauge.").Set(1)
	ms.NewCounterVec("labelled_counter_total", "A labelled counter.", []string{"tenant"}).
		WithLabelValues("acme").Inc()
	ms.NewGaugeVec("labelled_gauge", "A labelled gauge.", []string{"tenant", "device"}).
		WithLabelValues("acme", "d1").Set(1)

	want := map[string][]string{
		"devicechain_labelprobe_plain_counter_total":    {},
		"devicechain_labelprobe_plain_gauge":            {},
		"devicechain_labelprobe_labelled_counter_total": {"tenant"},
		"devicechain_labelprobe_labelled_gauge":         {"device", "tenant"},
	}

	families, err := ms.metricsReg.Gather()
	if err != nil {
		t.Fatalf("gathering: %v", err)
	}
	seen := map[string]bool{}
	for _, f := range families {
		exp, ok := want[f.GetName()]
		if !ok {
			continue
		}
		seen[f.GetName()] = true
		if len(f.GetMetric()) != 1 {
			t.Fatalf("%s exported %d series, want 1", f.GetName(), len(f.GetMetric()))
		}
		var got []string
		for _, lp := range f.GetMetric()[0].GetLabel() {
			got = append(got, lp.GetName())
		}
		sort.Strings(got)
		if len(got) != len(exp) {
			t.Errorf("%s exported label names %v, want %v", f.GetName(), got, exp)
			continue
		}
		for i := range exp {
			if got[i] != exp[i] {
				t.Errorf("%s exported label names %v, want %v", f.GetName(), got, exp)
				break
			}
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("%s was not exported at all; the assertion above never ran for it", name)
		}
	}
}
