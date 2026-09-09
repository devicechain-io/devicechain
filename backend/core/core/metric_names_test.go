// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// The metric constructors refuse a name that cannot appear in a Prometheus metric
// name, and they refuse it AT CONSTRUCTION rather than composing one and passing it
// on to client_golang.
//
// client_golang does not object. It accepts `raise-alarm` and registers the collector
// under the illegal name, and what a scrape then exports depends on the SCRAPER: with
// the default escaping the hyphen is rewritten to an underscore, and to a scraper that
// negotiates escaping=allow-utf-8 the name comes back hyphenated and quoted, selectable
// only through {__name__="..."}. Two names for one series, no error anywhere, and the
// difference is invisible from inside the process — which is why the refusal has to
// happen here.
//
// The hyphen is not hypothetical: "raise-alarm" is the exact argument the
// device-management raise-alarm consumer passed.
func TestMetricConstructorsRefuseAnIllegalName(t *testing.T) {
	illegal := []struct {
		name string
		why  string
	}{
		{"raise-alarm", "a hyphen, the case that shipped"},
		{"", "empty: BuildFQName returns nothing at all for an empty name"},
		{"1st_total", "leading digit"},
		{"batch refusals", "a space"},
		{"node:cpu_total", "a colon, which is reserved for recording rules"},
		{"queue.depth", "a dot"},
	}

	for _, tc := range illegal {
		t.Run(tc.name, func(t *testing.T) {
			// Each constructor separately: a guard added to one of five is a guard
			// four callers can still walk around.
			constructors := map[string]func(ms *Microservice){
				"NewProcessorMetrics": func(ms *Microservice) { ms.NewProcessorMetrics(tc.name) },
				"NewCounter":          func(ms *Microservice) { ms.NewCounter(tc.name, "help") },
				"NewCounterVec":       func(ms *Microservice) { ms.NewCounterVec(tc.name, "help", []string{"result"}) },
				"NewGauge":            func(ms *Microservice) { ms.NewGauge(tc.name, "help") },
				"NewGaugeVec":         func(ms *Microservice) { ms.NewGaugeVec(tc.name, "help", []string{"result"}) },
			}
			for ctor, call := range constructors {
				func() {
					ms := &Microservice{FunctionalArea: "name-probe"}
					ms.UseMetricsRegistry(prometheus.NewRegistry())
					defer func() {
						r := recover()
						if r == nil {
							t.Errorf("%s accepted the name %q (%s); client_golang will register it "+
								"and the exported series name then depends on the scraper's escaping",
								ctor, tc.name, tc.why)
							return
						}
						// The message has to name the offending argument, or the panic
						// sends the reader hunting through five constructors.
						msg, ok := r.(string)
						if !ok {
							t.Errorf("%s panicked with %T, want a string message naming the argument", ctor, r)
							return
						}
						for _, want := range []string{ctor, "name", tc.name} {
							if want == "" {
								continue
							}
							if !strings.Contains(msg, want) {
								t.Errorf("%s panic message %q does not mention %q", ctor, msg, want)
							}
						}
					}()
					call(ms)
				}()
			}
		})
	}
}

// A functional area that does not reduce to a legal subsystem is refused too.
//
// MetricsSubsystem only removes hyphens, so an area carrying anything else illegal
// reaches the exported name intact — the same defect one level up from the argument,
// and it would otherwise be caught by nothing.
func TestMetricConstructorsRefuseAnIllegalFunctionalArea(t *testing.T) {
	ms := &Microservice{FunctionalArea: "device management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a functional area with a space was accepted; every metric the service exports " +
				"would carry it in the middle of the name")
		}
		if msg, ok := r.(string); !ok || !strings.Contains(msg, "device management") {
			t.Errorf("panic %v does not name the offending functional area", r)
		}
	}()
	ms.NewCounter("probe_total", "help")
}

// 🔴 THE COUNTERWEIGHT. "Refuses an illegal name" is satisfied just as well by a
// constructor that refuses everything, which would leave every service exporting
// nothing. So each name a service actually passes has to construct AND land on the
// registry AND appear in the exposition, under exactly the name it composes to.
//
// The five are every argument NewProcessorMetrics is given in this repository. The
// sixth is the rename this test exists alongside.
func TestEveryProcessorLoopNameStillRegisters(t *testing.T) {
	loops := []string{"resolve", "notify", "persist", "state", "response", "raise_alarm"}

	ms := &Microservice{FunctionalArea: "device-management"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)

	for _, loop := range loops {
		pm := ms.NewProcessorMetrics(loop)
		if pm == nil {
			t.Fatalf("NewProcessorMetrics(%q) returned nil", loop)
		}
		// Exercise it: the CounterVec exports no sample until a label value is used,
		// which is why the hyphenated name looked like two series rather than three.
		done := pm.Start()
		done(ResultOK)
	}

	rec := httptest.NewRecorder()
	ms.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics answered %d", rec.Code)
	}
	body := rec.Body.String()

	for _, loop := range loops {
		for _, suffix := range []string{"_messages_total", "_inflight", "_duration_seconds_count"} {
			want := "devicechain_devicemanagement_" + loop + suffix
			if !strings.Contains(body, want) {
				t.Errorf("the exposition does not contain %q", want)
			}
		}
	}

	// And nothing exports the old hyphenated name, under either escaping. A scraper
	// taking the default escaping would see the renamed series under the SAME name the
	// hyphenated one used to be escaped to, so this half is what distinguishes "the
	// rename happened" from "the escaping hid it".
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "text/plain;version=0.0.4;escaping=allow-utf-8")
	ms.MetricsHandler().ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "raise-alarm") {
		t.Error("a UTF-8-negotiating scrape still exports a hyphenated raise-alarm series")
	}

	// The registry's own view, which is where an illegal name would survive escaping.
	//
	// The grammar is written out here rather than reusing metricNamePart: a control
	// built from the thing under test cannot detect that thing moving, and a
	// metricNamePart widened to accept anything would make this assertion vacuous at
	// the same moment it stopped being true. This is the Prometheus grammar, colon
	// included, since that is what a REGISTERED name may legally hold.
	legal := regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gathering: %v", err)
	}
	var names []string
	for _, f := range families {
		if !legal.MatchString(f.GetName()) {
			t.Errorf("registered family %q is not a legal metric name", f.GetName())
		}
		// promhttp's own scrape instrumentation registers here too (MetricsHandler
		// keeps it deliberately), so count this service's families only.
		if strings.HasPrefix(f.GetName(), METRICS_NAMESPACE+"_") {
			names = append(names, f.GetName())
		}
	}
	sort.Strings(names)
	if len(names) != len(loops)*3 {
		t.Errorf("registered %d %s_* families, want %d: %v", len(names), METRICS_NAMESPACE, len(loops)*3, names)
	}
}
