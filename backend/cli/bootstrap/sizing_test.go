// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"strconv"
	"strings"
	"testing"
)

// The shipped CPU limits have to carry the ingest rate the platform admits by
// default. At the old 500m everywhere, device-management was the ceiling of the
// whole ingest path: on a four-node --ha kind cluster it resolved at most ~720
// events a second, CPU-throttled in 99.9% of scheduling periods, below the 1000
// messages a second every tenant is allowed. The tests here render the chart the
// way dcctl installs it and hold each ingest-path area's CPU limit to its measured
// cost at that rate.

// measuredCPUPerEvent is each ingest-path area's CPU cost per event, in
// millicores, measured on a four-node --ha kind cluster (2026-09-26, with the CPU
// limits lifted to 4 cores so nothing was throttled): container CPU over the
// resolved rate (event-management: the stored rate) for the same 180 s window.
//
// Each value is the HIGHER of the two rungs that bracket the default ceiling,
// 800 and 1600 events/s. It is deliberately not the highest cost at any rate:
// the 100 events/s rung reads higher per event (device-management 1.01,
// event-management 0.66) because a fixed baseline is divided by a small rate,
// and that baseline does not scale with traffic.
//
//	area               800/s               1600/s
//	device-management  0.753 cores  0.94   1.535 cores  0.96
//	event-management   0.426 cores  0.53   0.736 cores  0.48 (1545 stored/s)
//	event-sources      0.128 cores  0.16   0.268 cores  0.17
//	event-processing   0.138 cores  0.17   0.270 cores  0.17
//
// Those runs had four Go threads (GOMAXPROCS follows the CPU limit). Under the
// shipped 2-core limit these services run two, and at the old 500m, where they
// also ran two, device-management measured 0.70-0.77 per event on the rungs it
// kept up with; so the figures here are the conservative side.
var measuredCPUPerEvent = map[string]float64{
	"device-management": 0.96,
	"event-management":  0.53,
	"event-sources":     0.17,
	"event-processing":  0.17,
}

// cpuHeadroom: at the default ceiling an area may use at most 1/cpuHeadroom of
// its CPU limit on average. A CFS quota is spent by bursts, not by the average:
// on the same cluster device-management was already throttled in 24% of periods
// averaging 0.29 cores of its 500m, and event-sources in 32% at 0.23 cores.
const cpuHeadroom = 2.0

// eventSourcesConfig is where the default per-tenant ceiling is defined. It is
// read from source rather than imported so dcctl does not take a module
// dependency on a service for one constant, and rather than restated so the
// requirement moves when the ceiling does. lwm2m-ingest carries a second
// definition of the same default (its config/configuration.go); this test follows
// the event-sources one, the path every JSON transport takes.
//
// The file lives outside this module and Go's test cache does not track it, so a
// plain `go test` can serve a stale PASS over an edit there; CI runs -count=1.
const eventSourcesConfig = "../../services/event-sources/config/configuration.go"

// defaultIngestCeiling parses DefaultIngestMessagesPerSecond out of the
// event-sources config, failing rather than returning a zero that would make
// every requirement below trivially met.
func defaultIngestCeiling(t *testing.T) float64 {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), eventSourcesConfig, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", eventSourcesConfig, err)
	}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if name.Name != "DefaultIngestMessagesPerSecond" {
					continue
				}
				if i >= len(vs.Values) {
					t.Fatalf("DefaultIngestMessagesPerSecond in %s has no value of its own", eventSourcesConfig)
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.INT {
					t.Fatalf("DefaultIngestMessagesPerSecond in %s is no longer an integer literal; "+
						"teach this test to read its new form", eventSourcesConfig)
				}
				n, err := strconv.ParseInt(lit.Value, 0, 64)
				if err != nil || n <= 0 {
					t.Fatalf("DefaultIngestMessagesPerSecond = %s is not a positive integer", lit.Value)
				}
				return float64(n)
			}
		}
	}
	t.Fatalf("no DefaultIngestMessagesPerSecond constant in %s", eventSourcesConfig)
	return 0
}

// The shipped CPU limit of every ingest-path area carries the default ceiling
// with headroom, for the install dcctl does by default and for --compact (which
// lowers requests and must leave limits alone). --ha is not a separate case: it
// changes no resources.
//
// event-sources and event-processing are the counterweight. They pass at 500m, so
// the rule does not simply raise everything.
func TestShippedCPULimitsCarryTheDefaultIngestCeiling(t *testing.T) {
	ceiling := defaultIngestCeiling(t)
	for _, tc := range []struct {
		name string
		st   *State
	}{{"default", compactState(false)}, {"compact", compactState(true)}} {
		t.Run(tc.name, func(t *testing.T) {
			got := byArea(t, renderContainers(t, helmValues(tc.st)))
			for area, perEvent := range measuredCPUPerEvent {
				c, ok := got[area]
				if !ok {
					t.Errorf("%s did not render: its requirement was not checked", area)
					continue
				}
				need := int64(math.Ceil(perEvent * ceiling * cpuHeadroom))
				if have := q(t, "cpu", c.limits["cpu"]); have < need {
					t.Errorf("%s: CPU limit %s (%dm) is below the %dm it needs to carry the default "+
						"ceiling of %.0f messages/s (%.2fm per event, using at most 1/%.0f of the limit)",
						area, c.limits["cpu"], have, need, ceiling, perEvent, cpuHeadroom)
				}
			}
		})
	}
}

// An area's own resources are merged over the top-level map key by key, rather
// than replacing it. Under replacement an area that set only limits.cpu rendered
// with no requests at all, so --compact's lowered requests (written at the top
// level) never reached it.
func TestAreaResourcesMergeOverTheDefaults(t *testing.T) {
	raised := map[string]bool{"device-management": true, "event-management": true}

	t.Run("shipped", func(t *testing.T) {
		got := byArea(t, renderContainers(t, nil))
		for area := range raised {
			if _, ok := got[area]; !ok {
				t.Fatalf("%s did not render: nothing below checks it", area)
			}
		}
		for area, c := range got {
			wantCPU := "500m"
			if raised[area] {
				wantCPU = "2"
			}
			// Every key but the raised CPU limit comes from the top-level map. For an
			// area rendered AFTER device-management, a wrong limit here means the merge
			// wrote into the shared defaults.
			want := map[string]string{
				"limits.cpu": wantCPU, "limits.memory": "256Mi",
				"requests.cpu": "100m", "requests.memory": "128Mi",
			}
			have := map[string]string{
				"limits.cpu": c.limits["cpu"], "limits.memory": c.limits["memory"],
				"requests.cpu": c.requests["cpu"], "requests.memory": c.requests["memory"],
			}
			for k, v := range want {
				if have[k] != v {
					t.Errorf("%s: %s = %q, want %q", area, k, have[k], v)
				}
			}
		}
	})

	t.Run("an operator's area override wins for that area only", func(t *testing.T) {
		got := byArea(t, renderContainers(t, map[string]interface{}{
			"functionalAreas": map[string]interface{}{
				"device-management": map[string]interface{}{
					"resources": map[string]interface{}{
						"limits": map[string]interface{}{"cpu": "4"},
					},
				},
			},
		}))
		for area, want := range map[string]string{
			"device-management": "4", "event-management": "2", "event-sources": "500m",
		} {
			if have := got[area].limits["cpu"]; have != want {
				t.Errorf("%s: limits.cpu = %q, want %q", area, have, want)
			}
		}
		if have := got["device-management"].requests["cpu"]; have != "100m" {
			t.Errorf("device-management: requests.cpu = %q, want the top-level 100m", have)
		}
	})

	t.Run("a top-level change reaches every area that does not set the key", func(t *testing.T) {
		got := byArea(t, renderContainers(t, map[string]interface{}{
			"resources": map[string]interface{}{
				"requests": map[string]interface{}{"cpu": "100m", "memory": "128Mi"},
				"limits":   map[string]interface{}{"cpu": "750m", "memory": "384Mi"},
			},
		}))
		for area, want := range map[string]string{
			"device-management": "2", "event-management": "2", "event-sources": "750m",
		} {
			if have := got[area].limits["cpu"]; have != want {
				t.Errorf("%s: limits.cpu = %q, want %q", area, have, want)
			}
			if have := got[area].limits["memory"]; have != "384Mi" {
				t.Errorf("%s: limits.memory = %q, want the top-level 384Mi", area, have)
			}
		}
	})
}

// A request above its limit is refused when the chart renders, naming the area,
// rather than by the API server when the ReplicaSet tries to create the pod, where
// `helm upgrade` only times out. The merge makes this reachable from a values file
// that used to be valid: an area that set only requests.memory: 512Mi used to get
// no limits, and now gets the top-level 256Mi.
func TestAreaRequestAboveItsLimitIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, dim, request string
	}{
		{"memory", "memory", "512Mi"},
		{"memory across units", "memory", "1Gi"},
		{"cpu", "cpu", "750m"},
		{"cpu in whole cores", "cpu", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := renderChart(t, map[string]interface{}{
				"functionalAreas": map[string]interface{}{
					"event-sources": map[string]interface{}{
						"resources": map[string]interface{}{
							"requests": map[string]interface{}{tc.dim: tc.request},
						},
					},
				},
			})
			if err == nil {
				t.Fatalf("a %s request of %s against the top-level limit rendered; the API "+
					"server would refuse the pod instead", tc.dim, tc.request)
			}
			for _, want := range []string{"event-sources", tc.dim, tc.request} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %q: %v", want, err)
				}
			}
		})
	}

	// The counterweight: a request equal to its limit (Guaranteed QoS) is valid, and
	// so is a raised request under a raised limit.
	t.Run("equal and raised together render", func(t *testing.T) {
		got := byArea(t, renderContainers(t, map[string]interface{}{
			"functionalAreas": map[string]interface{}{
				"event-sources": map[string]interface{}{
					"resources": map[string]interface{}{
						"requests": map[string]interface{}{"cpu": "500m", "memory": "512Mi"},
						"limits":   map[string]interface{}{"memory": "512Mi"},
					},
				},
			},
		}))
		es := got["event-sources"]
		if es.requests["memory"] != "512Mi" || es.limits["memory"] != "512Mi" || es.requests["cpu"] != "500m" {
			t.Errorf("event-sources rendered requests %v limits %v, want 500m/512Mi requested "+
				"and a 512Mi memory limit", es.requests, es.limits)
		}
	})
}

// device-management and event-management carry a CPU limit of their own, and a
// top-level limit does not replace it. So a top-level request above that limit
// (valid against the top-level limit) is refused, and the refusal has to say where
// the limit came from: the operator never wrote the key it names.
func TestTopLevelRequestAboveAnAreasOwnLimitNamesWhereTheLimitIsSet(t *testing.T) {
	_, err := renderChart(t, map[string]interface{}{
		"resources": map[string]interface{}{
			"requests": map[string]interface{}{"cpu": "3", "memory": "128Mi"},
			"limits":   map[string]interface{}{"cpu": "4", "memory": "256Mi"},
		},
	})
	if err == nil {
		t.Fatal("a top-level cpu request of 3 rendered against device-management's 2-core limit")
	}
	msg := err.Error()
	var area string
	for _, a := range []string{"device-management", "event-management"} {
		if strings.Contains(msg, "functionalAreas."+a+".resources.limits.cpu") {
			area = a
		}
	}
	if area == "" {
		t.Fatalf("the refusal does not name the area-level limit key it came from: %v", msg)
	}
	for _, want := range []string{
		"the top-level resources.requests.cpu", // where the request came from
		"the chart's own default",              // the operator may not have written the limit
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not say %q: %v", want, msg)
		}
	}
}

// The per-area resources schema refuses a key it does not know. Without that, a
// typo such as `limit:` for `limits:` is merged into the pod spec and the limit
// it meant is silently not applied.
func TestAreaResourcesRefuseAnUnknownKey(t *testing.T) {
	_, err := renderChart(t, map[string]interface{}{
		"functionalAreas": map[string]interface{}{
			"event-sources": map[string]interface{}{
				"resources": map[string]interface{}{
					"limit": map[string]interface{}{"cpu": "4"},
				},
			},
		},
	})
	if err == nil {
		t.Fatal("functionalAreas.event-sources.resources.limit rendered; the limit it meant was not applied")
	}
	for _, want := range []string{"event-sources/resources", "'limit'"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// A quantity the chart cannot read is refused, not read as zero: a zero request
// is below every limit, which is the one answer the comparison exists to withhold.
// ".5" is the counterweight, a valid form that must still render.
func TestAreaResourcesRefuseAnUnreadableQuantity(t *testing.T) {
	for _, tc := range []struct{ dim, quantity string }{
		{"memory", "1gi"},
		{"cpu", "2cores"},
	} {
		t.Run(tc.dim, func(t *testing.T) {
			_, err := renderChart(t, map[string]interface{}{
				"functionalAreas": map[string]interface{}{
					"event-sources": map[string]interface{}{
						"resources": map[string]interface{}{
							"requests": map[string]interface{}{tc.dim: tc.quantity},
						},
					},
				},
			})
			if err == nil {
				t.Fatalf("a %s request of %q rendered", tc.dim, tc.quantity)
			}
			for _, want := range []string{"event-sources", tc.quantity, "is not a form the chart reads"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %q: %v", want, err)
				}
			}
		})
	}

	t.Run("a leading decimal point renders", func(t *testing.T) {
		got := byArea(t, renderContainers(t, map[string]interface{}{
			"functionalAreas": map[string]interface{}{
				"event-sources": map[string]interface{}{
					"resources": map[string]interface{}{
						"requests": map[string]interface{}{"cpu": ".5"},
					},
				},
			},
		}))
		if have := got["event-sources"].requests["cpu"]; have != ".5" {
			t.Errorf("event-sources requests.cpu = %q, want .5", have)
		}
	})
}
