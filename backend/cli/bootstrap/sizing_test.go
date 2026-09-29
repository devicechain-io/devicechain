// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The shipped CPU limits have to carry the ingest rate the platform admits by
// default. At the old 500m everywhere, device-management was the ceiling of the
// whole ingest path: on a four-node --ha kind cluster it resolved at most ~720
// events a second, CPU-throttled in 99.9% of scheduling periods, below the 1000
// messages a second every tenant is allowed. The tests here render the chart the
// way dcctl installs it and hold each event-path area's CPU limit and CPU request
// to what it was measured to use.
//
// CPU per event depends on the hardware, so the measurements are kept by the
// cluster they were taken on, one table each, and each rule reads one table:
//
//   - kindCPUPerEvent (a laptop kind cluster) holds the LIMITS to the default
//     ceiling with headroom. It is the slower machine and reads higher per event
//     (device-management about twice the GKE figure): the conservative side for a
//     limit, which throttles a service that reaches it.
//   - gkeCPUPerEvent (Google Kubernetes Engine) holds the REQUESTS to the default
//     ceiling, and the LIMITS to the rate a default --ha install sustained there.
//     A request only places a pod, and the production-shaped cluster is the one
//     whose placement it is for.

// kindCPUPerEvent is each ingest-path area's CPU cost per event, in
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
var kindCPUPerEvent = map[string]float64{
	"device-management": 0.96,
	"event-management":  0.53,
	"event-sources":     0.17,
	"event-processing":  0.17,
}

// gkeCPUPerEvent is each event-path area's CPU cost per event, in millicores,
// measured on Google Kubernetes Engine: 3 x n2-standard-8, SSD persistent disks,
// --ha, v0.18.0. Container CPU averaged over a 180 s hold, over the resolved rate
// (event-management: the stored rate).
//
// Each value is the highest UNTHROTTLED reading among the 800 and 1600 events/s
// rungs that bracket the default ceiling and a 4000 events/s rung, so it holds at
// the default ceiling and at gkeSustainedRate alike. A throttled reading
// under-reads (the service did less than it was offered), so none is used:
//
//	area               800/s           1600/s          4000/s
//	device-management  0.38   0.475    0.72   0.450    1.70/3955  0.430
//	event-management   0.29   0.3625   0.56   0.350    1.22/3955  0.308
//	event-sources      0.10   0.125    (throttled)     0.55/3996  0.138 *
//	device-state       (throttled)     (throttled)     1.49/3991  0.374 *
//	event-processing   0.05   0.0625   0.10   0.0625   0.26/3955  0.066
//
// * from a run with those two services' CPU limits lifted to 4 cores, where
// nothing was throttled. At its then 500m limit device-state was throttled on
// every rung of the default install, so it has no reading at default settings.
var gkeCPUPerEvent = map[string]float64{
	"device-management": 0.475,
	"event-management":  0.3625,
	"event-sources":     0.138,
	"device-state":      0.374,
	"event-processing":  0.066,
}

// gkeSustainedRate is the ingest rate, in events/s, a default --ha install
// sustained on that cluster (v0.18.0, before device-management kept its lookups in
// process): resolution averaged 3858/s over a 10-minute run at 4000 offered, and
// 3955/s over a 180 s hold. At that rate device-management's pool of resolvers was
// the limit, not CPU, so no event-path area's CPU limit may be the limit first.
// Re-derive it when a change moves that pool's ceiling.
const gkeSustainedRate = 3800

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
// event-sources and event-processing are the counterweight: this rule alone would
// pass them at 500m, so it does not simply raise everything. (event-sources' own
// limit comes from the sustained-rate rule below.)
func TestShippedCPULimitsCarryTheDefaultIngestCeiling(t *testing.T) {
	ceiling := defaultIngestCeiling(t)
	for _, tc := range []struct {
		name string
		st   *State
	}{{"default", compactState(false)}, {"compact", compactState(true)}} {
		t.Run(tc.name, func(t *testing.T) {
			got := byArea(t, renderContainers(t, helmValues(tc.st)))
			for area, perEvent := range kindCPUPerEvent {
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

// millicores is perEvent x rate in millicores, rounded UP. The product is rounded
// to a thousandth first, so a value that is exact on paper (0.15 x 1000) is not
// pushed up a whole step by the float error in its last digit.
func millicores(perEvent, rate float64) int64 {
	return int64(math.Ceil(math.Round(perEvent*rate*1000) / 1000))
}

// cpuLimitShortfalls names each area in perEvent whose rendered CPU limit is below
// what it measurably uses at rate. An area missing from the render is a shortfall,
// never a skip: an area nobody checks passes every rule.
func cpuLimitShortfalls(t *testing.T, got map[string]renderedContainer, perEvent map[string]float64, rate float64) []string {
	t.Helper()

	var out []string
	for _, area := range slices.Sorted(maps.Keys(perEvent)) {
		c, ok := got[area]
		if !ok {
			out = append(out, fmt.Sprintf("%s did not render: its requirement was not checked", area))
			continue
		}
		need := millicores(perEvent[area], rate)
		if have := q(t, "cpu", c.limits["cpu"]); have < need {
			out = append(out, fmt.Sprintf("%s: CPU limit %s (%dm) is below the %dm it uses at %.0f "+
				"events/s (%.3fm per event), so the limit, not the resolvers, would cap the rate",
				area, c.limits["cpu"], have, need, rate, perEvent[area]))
		}
	}
	return out
}

// At the rate a default --ha install sustains, no event-path area may need more
// CPU than its limit on average. At the old 500m, event-sources was throttled in
// 84% of scheduling periods at 4000 events/s and device-state in all of them from
// 2400, merging live state at no more than ~2.3k/s. event-processing is the
// counterweight: it needs about half its 500m, so the rule does not raise it.
func TestShippedCPULimitsCarryTheMeasuredSustainedRate(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   *State
	}{{"default", compactState(false)}, {"compact", compactState(true)}} {
		t.Run(tc.name, func(t *testing.T) {
			got := byArea(t, renderContainers(t, helmValues(tc.st)))
			for _, s := range cpuLimitShortfalls(t, got, gkeCPUPerEvent, gkeSustainedRate) {
				t.Error(s)
			}
		})
	}

	// The rule has to be able to fail: the old event-sources limit is 25m short.
	t.Run("the check can fail", func(t *testing.T) {
		vals := helmValues(compactState(false))
		mergeFunctionalArea(vals, "event-sources", map[string]interface{}{
			"resources": map[string]interface{}{"limits": map[string]interface{}{"cpu": "500m"}},
		})
		got := cpuLimitShortfalls(t, byArea(t, renderContainers(t, vals)), gkeCPUPerEvent, gkeSustainedRate)
		if len(got) != 1 || !strings.Contains(got[0], "event-sources") || !strings.Contains(got[0], "525m") {
			t.Errorf("an event-sources limit of 500m reported %q, want exactly one shortfall "+
				"naming event-sources and the 525m it needs", got)
		}
	})
}

// Each event-path area REQUESTS the CPU it measurably uses at the default ingest
// ceiling, rounded up to a multiple of 50m and never below the chart's top-level
// request. With every area at the same 100m the scheduler could not tell the
// service that resolves every event from an idle one, and on a three-node cluster
// it put the busiest services on the node running the event store's primary.
//
// It renders what dcctl installs without --compact. The floor is read from an area
// with no measured request of its own, not restated. event-processing is the
// counterweight: it uses less than the floor and keeps it. Memory requests do not
// move: no event-path pod's working set passed 40Mi on that cluster.
func TestShippedCPURequestsAreTheMeasuredUseAtTheDefaultCeiling(t *testing.T) {
	ceiling := defaultIngestCeiling(t)
	got := byArea(t, renderContainers(t, helmValues(compactState(false))))

	const plain = "command-delivery"
	if _, ok := gkeCPUPerEvent[plain]; ok {
		t.Fatalf("%s is measured, so it cannot stand for the chart's own request", plain)
	}
	c, ok := got[plain]
	if !ok {
		t.Fatalf("%s did not render: there is no floor to read", plain)
	}
	floor := q(t, "cpu", c.requests["cpu"])

	for _, area := range slices.Sorted(maps.Keys(gkeCPUPerEvent)) {
		c, ok := got[area]
		if !ok {
			t.Errorf("%s did not render: its request was not checked", area)
			continue
		}
		want := max(floor, (millicores(gkeCPUPerEvent[area], ceiling)+49)/50*50)
		if have := q(t, "cpu", c.requests["cpu"]); have != want {
			t.Errorf("%s: requests.cpu %s (%dm), want %dm: %.4fm per event at the default "+
				"ceiling of %.0f messages/s, rounded up to 50m, at least the chart's %dm",
				area, c.requests["cpu"], have, want, gkeCPUPerEvent[area], ceiling, floor)
		}
	}
	for area, c := range got {
		if c.requests["memory"] != "128Mi" {
			t.Errorf("%s: requests.memory %q, want the chart's 128Mi", area, c.requests["memory"])
		}
	}
}

// A measured request is a chart default the operator never wrote. So when an
// operator lowers an area's CPU limit below it, the refusal has to say where the
// request came from and how to turn the measured requests off, and turning them
// off has to work.
func TestMeasuredRequestAboveAnOperatorsLimitNamesMeasuredRequests(t *testing.T) {
	lowered := func() map[string]interface{} {
		return map[string]interface{}{
			"functionalAreas": map[string]interface{}{
				"device-management": map[string]interface{}{
					"resources": map[string]interface{}{
						"limits": map[string]interface{}{"cpu": "300m"},
					},
				},
			},
		}
	}

	_, err := renderChart(t, lowered())
	if err == nil {
		t.Fatal("device-management rendered with a 300m CPU limit below its measured request")
	}
	for _, want := range []string{
		"functionalAreas.device-management.measuredRequests.cpu",
		"500m", "300m",
		"useMeasuredRequests: false",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}

	// The other side of the precedence: a request the operator DID write, on an area
	// that also has a measured one, is named as theirs. Naming measuredRequests here
	// would point them at a key they never set.
	t.Run("the area's own request is named over the measured one", func(t *testing.T) {
		_, err := renderChart(t, map[string]interface{}{
			"functionalAreas": map[string]interface{}{
				"device-management": map[string]interface{}{
					"resources": map[string]interface{}{
						"requests": map[string]interface{}{"cpu": "3"},
					},
				},
			},
		})
		if err == nil {
			t.Fatal("device-management rendered with a 3-core request above its 2-core limit")
		}
		msg := err.Error()
		if !strings.Contains(msg, "The request comes from functionalAreas.device-management.resources.requests.cpu") {
			t.Errorf("the refusal does not name the operator's own request key: %v", msg)
		}
		if strings.Contains(msg, "measuredRequests") {
			t.Errorf("the refusal names measuredRequests, which the operator did not set: %v", msg)
		}
	})

	t.Run("with the measured requests off it renders", func(t *testing.T) {
		vals := lowered()
		vals["useMeasuredRequests"] = false
		dm := byArea(t, renderContainers(t, vals))["device-management"]
		if dm.requests["cpu"] != "100m" || dm.limits["cpu"] != "300m" {
			t.Errorf("device-management rendered requests %v limits %v, want the top-level "+
				"100m request under the operator's 300m limit", dm.requests, dm.limits)
		}
	})
}

// An area's resources are three layers merged key by key: the top-level map, the
// area's measuredRequests (while useMeasuredRequests is on), and the area's own
// resources. Under replacement an area that set only limits.cpu rendered with no
// requests at all, so --compact's lowered requests (written at the top level) never
// reached it.
func TestAreaResourcesMergeOverTheDefaults(t *testing.T) {
	raised := map[string]bool{
		"device-management": true, "event-management": true,
		"event-sources": true, "device-state": true,
	}
	measured := map[string]string{
		"device-management": "500m", "event-management": "400m",
		"event-sources": "150m", "device-state": "400m",
	}

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
			wantReq := "100m"
			if r, ok := measured[area]; ok {
				wantReq = r
			}
			// Every key but the raised CPU limit and the measured CPU request comes
			// from the top-level map. For an area rendered AFTER device-management, a
			// wrong value here means the merge wrote into the shared defaults.
			want := map[string]string{
				"limits.cpu": wantCPU, "limits.memory": "256Mi",
				"requests.cpu": wantReq, "requests.memory": "128Mi",
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
				"event-management": map[string]interface{}{
					"resources": map[string]interface{}{
						"requests": map[string]interface{}{"cpu": "1"},
					},
				},
			},
		}))
		for area, want := range map[string]string{
			"device-management": "4", "event-management": "2", "event-processing": "500m",
		} {
			if have := got[area].limits["cpu"]; have != want {
				t.Errorf("%s: limits.cpu = %q, want %q", area, have, want)
			}
		}
		for area, want := range map[string]string{
			"device-management": "500m", // its measured request: it set only a limit
			"event-management":  "1",    // its own request wins over its measured one
			"event-processing":  "100m", // unmeasured: the top-level request
		} {
			if have := got[area].requests["cpu"]; have != want {
				t.Errorf("%s: requests.cpu = %q, want %q", area, have, want)
			}
		}
	})

	t.Run("a top-level change reaches every area that does not set the key", func(t *testing.T) {
		got := byArea(t, renderContainers(t, map[string]interface{}{
			"resources": map[string]interface{}{
				"requests": map[string]interface{}{"cpu": "200m", "memory": "128Mi"},
				"limits":   map[string]interface{}{"cpu": "750m", "memory": "384Mi"},
			},
		}))
		for area, want := range map[string]string{
			"device-management": "2", "event-management": "2", "event-sources": "2",
			"device-state": "2", "event-processing": "750m",
		} {
			if have := got[area].limits["cpu"]; have != want {
				t.Errorf("%s: limits.cpu = %q, want %q", area, have, want)
			}
			if have := got[area].limits["memory"]; have != "384Mi" {
				t.Errorf("%s: limits.memory = %q, want the top-level 384Mi", area, have)
			}
		}
		// A measured request is an area's own for this purpose: the top-level request
		// reaches only the areas without one.
		for area, want := range map[string]string{
			"event-sources": "150m", "event-processing": "200m",
		} {
			if have := got[area].requests["cpu"]; have != want {
				t.Errorf("%s: requests.cpu = %q, want %q", area, have, want)
			}
		}
	})

	t.Run("with the measured requests off the top-level request reaches every area", func(t *testing.T) {
		got := byArea(t, renderContainers(t, map[string]interface{}{"useMeasuredRequests": false}))
		for area, c := range got {
			if c.requests["cpu"] != "100m" {
				t.Errorf("%s: requests.cpu = %q, want the top-level 100m", area, c.requests["cpu"])
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
					"event-processing": map[string]interface{}{
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
			for _, want := range []string{"event-processing", tc.dim, tc.request} {
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
				"event-processing": map[string]interface{}{
					"resources": map[string]interface{}{
						"requests": map[string]interface{}{"cpu": "500m", "memory": "512Mi"},
						"limits":   map[string]interface{}{"memory": "512Mi"},
					},
				},
			},
		}))
		ep := got["event-processing"]
		if ep.requests["memory"] != "512Mi" || ep.limits["memory"] != "512Mi" || ep.requests["cpu"] != "500m" {
			t.Errorf("event-processing rendered requests %v limits %v, want 500m/512Mi requested "+
				"and a 512Mi memory limit", ep.requests, ep.limits)
		}
	})
}

// The event-path areas carry a CPU limit of their own, and a top-level limit does
// not replace it. So a top-level request above that limit (valid against the
// top-level limit) is refused, and the refusal has to say where the limit came
// from: the operator never wrote the key it names.
//
// With the measured requests on, each area that owns a limit also owns a request,
// so a top-level request reaches such an area only when they are off: which is
// exactly what --compact does, and so the case this refusal is for.
func TestTopLevelRequestAboveAnAreasOwnLimitNamesWhereTheLimitIsSet(t *testing.T) {
	_, err := renderChart(t, map[string]interface{}{
		"useMeasuredRequests": false,
		"resources": map[string]interface{}{
			"requests": map[string]interface{}{"cpu": "3", "memory": "128Mi"},
			"limits":   map[string]interface{}{"cpu": "4", "memory": "256Mi"},
		},
	})
	if err == nil {
		t.Fatal("a top-level cpu request of 3 rendered against the event-path areas' 2-core limit")
	}
	msg := err.Error()
	var area string
	for _, a := range []string{"device-management", "event-management", "event-sources", "device-state"} {
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

// An area's values refuse a key the chart does not read, and so does its
// measuredRequests. The switches an operator uses to opt out are the ones a typo
// hurts most: `avoidEventStorePrimry: false` would render, and the pod would keep
// the placement rule it was meant to drop.
func TestAreaValuesRefuseAnUnknownKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		area map[string]interface{}
		want []string
	}{
		{
			"a misspelled area key",
			map[string]interface{}{"avoidEventStorePrimry": false},
			[]string{"event-sources", "'avoidEventStorePrimry'"},
		},
		{
			"memory under measuredRequests",
			map[string]interface{}{"measuredRequests": map[string]interface{}{"memory": "1Gi"}},
			[]string{"event-sources/measuredRequests", "'memory'"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := renderChart(t, map[string]interface{}{
				"functionalAreas": map[string]interface{}{"event-sources": tc.area},
			})
			if err == nil {
				t.Fatalf("functionalAreas.event-sources %v rendered; the key is not read by anything", tc.area)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %q: %v", want, err)
				}
			}
		})
	}

	// The counterweight: every key the chart ships in its own area blocks, and a
	// cpu written as a bare number (what --set produces for "1"), still render.
	t.Run("known keys render", func(t *testing.T) {
		got := byArea(t, renderContainers(t, map[string]interface{}{
			"functionalAreas": map[string]interface{}{
				"event-sources": map[string]interface{}{
					"measuredRequests":       map[string]interface{}{"cpu": 1},
					"avoidEventStorePrimary": false,
				},
			},
		}))
		if have := got["event-sources"].requests["cpu"]; have != "1" {
			t.Errorf("event-sources requests.cpu = %q, want the measuredRequests value 1", have)
		}
	})
}

// The chart's top level refuses a key it does not read, like each area does. The
// switch for the measured requests sits there, and a typo in it
// (`useMeasuredRequest: false`) would render with the measured requests still on.
// The counterweight is every other test here: dcctl's own values, which set only
// declared keys, still render.
func TestTopLevelValuesRefuseAnUnknownKey(t *testing.T) {
	_, err := renderChart(t, map[string]interface{}{"useMeasuredRequest": false})
	if err == nil {
		t.Fatal("a top-level useMeasuredRequest rendered; the chart reads no such key")
	}
	if !strings.Contains(err.Error(), "'useMeasuredRequest'") {
		t.Errorf("the refusal does not name the unknown key: %v", err)
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
