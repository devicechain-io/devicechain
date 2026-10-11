// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

// GOMEMLIMIT is derived from each container's own memory limit, which means the
// thing worth testing is not that an env var appears but that the DERIVATION is
// right — and specifically that it is right for the input that breaks naive
// arithmetic.
//
// Helm arithmetic is integer. Taking a percentage of a quantity's MAGNITUDE and
// reattaching the unit turns a 1Gi limit into floor(1 * 0.75) = "0Gi", which Go
// parses as a zero-byte soft limit and which nothing downstream would flag. That
// is the same magnitude-flooring trap that made sizing the JetStream PV as
// (sum / 0.9) unsafe, and it is invisible at the default 256Mi where the
// magnitude happens to be large enough to survive.

// renderedContainer is one container's derived-limit-relevant fields.
type renderedContainer struct {
	area        string
	memoryLimit string
	goMemLimit  string // "" when the env var was not rendered
	gogc        string // "" when GOGC was not rendered
	// requests/limits as rendered, for the compact preset's scheduling assertions
	// (compact_test.go). Kept here so both tests read one rendering of the pod spec
	// rather than each modelling the chart's resource block separately.
	requests map[string]string
	limits   map[string]string
}

// renderContainers renders the chart and returns one entry per container in every
// rendered Deployment.
func renderContainers(t *testing.T, vals map[string]interface{}) []renderedContainer {
	t.Helper()

	manifest, err := renderChart(t, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}
	return containersOf(t, manifest)
}

// renderChart renders the embedded chart the way the tests here need it and
// returns the manifest, or the render error for a test that expects a refusal.
func renderChart(t *testing.T, vals map[string]interface{}) (string, error) {
	t.Helper()

	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}
	if vals == nil {
		vals = map[string]interface{}{}
	}
	// Every render needs an instance id and a root key or the chart refuses.
	instance, _ := vals["instance"].(map[string]interface{})
	if instance == nil {
		instance = map[string]interface{}{}
		vals["instance"] = instance
	}
	instance["id"] = "dctest"
	instance["config"] = map[string]interface{}{
		"infrastructure": map[string]interface{}{
			"secrets": map[string]interface{}{
				"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
			},
		},
	}

	inst := action.NewInstall(&action.Configuration{})
	inst.ReleaseName = "dc-gomemlimit"
	inst.Namespace = "default"
	inst.DryRun = true
	inst.ClientOnly = true
	inst.APIVersions = []string{"monitoring.coreos.com/v1"}

	rel, err := inst.RunWithContext(t.Context(), ch, vals)
	if err != nil {
		return "", err
	}
	return rel.Manifest, nil
}

// containersOf decodes one entry per container in every Deployment of a manifest.
func containersOf(t *testing.T, manifest string) []renderedContainer {
	t.Helper()

	var out []renderedContainer
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var kind struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal([]byte(doc), &kind); err != nil || kind.Kind != "Deployment" {
			continue
		}
		var obj struct {
			Kind string `json:"kind"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Name string `json:"name"`
							Env  []struct {
								Name  string `json:"name"`
								Value string `json:"value"`
							} `json:"env"`
							Resources struct {
								Limits   map[string]string `json:"limits"`
								Requests map[string]string `json:"requests"`
							} `json:"resources"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		}
		// A Deployment this decoder cannot read is a failure, not a skip. Skipping it
		// drops that area from every test that ranges over the rendered areas, and
		// those tests then pass for the one they never saw.
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("decoding a rendered Deployment: %v\n%s", err, doc)
		}
		for _, c := range obj.Spec.Template.Spec.Containers {
			rc := renderedContainer{
				area:        c.Name,
				memoryLimit: c.Resources.Limits["memory"],
				requests:    c.Resources.Requests,
				limits:      c.Resources.Limits,
			}
			for _, e := range c.Env {
				if e.Name == "GOMEMLIMIT" {
					rc.goMemLimit = e.Value
				}
				if e.Name == "GOGC" {
					rc.gogc = e.Value
				}
			}
			out = append(out, rc)
		}
	}
	if len(out) == 0 {
		t.Fatal("no containers rendered — every assertion below would be vacuous")
	}
	return out
}

// enabled renders with the derivation turned on. It is off by default (see
// values.yaml for the measurement that settled that), so every test of the
// DERIVATION has to enable it explicitly — and the test that the default is off is
// a separate assertion below rather than an accident of these.
func enabled(extra map[string]interface{}) map[string]interface{} {
	vals := map[string]interface{}{"goMemLimitPercent": 75}
	for k, v := range extra {
		vals[k] = v
	}
	return vals
}

// frontendArea is the one rendered container that is not a Go binary: the console
// is served by nginx (templates/frontend.yaml). GOMEMLIMIT means nothing to it, and
// setting it anyway would be cargo cult — a Go knob on a C process, which a reader
// would reasonably take as evidence the frontend is a Go service.
const frontendArea = "frontend"

// goContainers is the subset the derivation is supposed to reach.
func goContainers(cs []renderedContainer) []renderedContainer {
	var out []renderedContainer
	for _, c := range cs {
		if c.area != frontendArea {
			out = append(out, c)
		}
	}
	return out
}

// mib parses a quantity this test understands, failing loudly on one it does not
// rather than returning a zero that would silently satisfy a comparison.
func mib(t *testing.T, q string) int64 {
	t.Helper()
	for suffix, scale := range map[string]int64{"Gi": 1024, "Mi": 1, "MiB": 1, "GiB": 1024} {
		if strings.HasSuffix(q, suffix) {
			n, err := strconv.ParseInt(strings.TrimSuffix(q, suffix), 10, 64)
			if err != nil {
				t.Fatalf("parsing quantity %q: %v", q, err)
			}
			return n * scale
		}
	}
	t.Fatalf("quantity %q uses a suffix this test cannot convert", q)
	return 0
}

// Every container with a memory limit gets a GOMEMLIMIT derived from THAT limit.
//
// The property that matters is the relationship, not the number: an area with a
// raised limit must get a raised GOMEMLIMIT without anyone editing a second value.
func TestGoMemLimitIsDerivedFromEachContainersOwnLimit(t *testing.T) {
	for _, c := range goContainers(renderContainers(t, enabled(nil))) {
		if c.memoryLimit == "" {
			continue
		}
		if c.goMemLimit == "" {
			t.Errorf("%s has a %s memory limit but no GOMEMLIMIT: Go will grow the heap "+
				"to roughly twice the live heap regardless of that limit", c.area, c.memoryLimit)
			continue
		}
		want := mib(t, c.memoryLimit) * 75 / 100
		if got := mib(t, c.goMemLimit); got != want {
			t.Errorf("%s: GOMEMLIMIT %s is %d MiB, want %d MiB (75%% of the %s limit)",
				c.area, c.goMemLimit, got, want, c.memoryLimit)
		}
	}
}

// A Gi-denominated limit must convert through MiB, not through its own magnitude.
//
// This is the case the default 256Mi cannot expose. Percentage-of-magnitude
// arithmetic yields floor(1 * 0.75) = "0Gi" here — a zero-byte soft limit that Go
// accepts and that no other test would notice, because every OTHER assertion about
// GOMEMLIMIT would still hold: the env var is present, it is below the container
// limit, and it is a valid quantity.
func TestGoMemLimitConvertsGibibytesBeforeTakingThePercentage(t *testing.T) {
	got := goMemLimitForArea(t, goContainers(renderContainers(t, enabled(map[string]interface{}{
		"resources": map[string]interface{}{
			"requests": map[string]interface{}{"cpu": "100m", "memory": "256Mi"},
			"limits":   map[string]interface{}{"cpu": "500m", "memory": "1Gi"},
		},
	}))))
	if n := mib(t, got); n != 768 {
		t.Errorf("a 1Gi limit produced GOMEMLIMIT %s (%d MiB), want 768 MiB: the "+
			"percentage was taken of the magnitude instead of the converted size, "+
			"which is the trap that made PV = sum/0.9 unsafe", got, n)
	}
}

// A fractional limit converts at its full size. The limit is read by the same
// parser as the request/limit comparison; the one before it took the leading
// integer, so 1.5Gi read as 1Gi and the derived GOMEMLIMIT was a third low.
func TestGoMemLimitReadsAFractionalLimit(t *testing.T) {
	got := goMemLimitForArea(t, goContainers(renderContainers(t, enabled(map[string]interface{}{
		"resources": map[string]interface{}{
			"requests": map[string]interface{}{"cpu": "100m", "memory": "256Mi"},
			"limits":   map[string]interface{}{"cpu": "500m", "memory": "1.5Gi"},
		},
	}))))
	if n := mib(t, got); n != 1152 {
		t.Errorf("a 1.5Gi limit produced GOMEMLIMIT %s (%d MiB), want 1152 MiB (75%% of 1536)", got, n)
	}
}

// A decimal limit is a valid Kubernetes quantity but is refused as a source for
// GOMEMLIMIT: 1G is 1000^3 bytes, and whoever wrote it most likely meant 1Gi. The
// refusal is a policy on top of the shared parser, which reads 1G for the
// request/limit comparison.
func TestGoMemLimitRefusesADecimalLimit(t *testing.T) {
	_, err := renderChart(t, enabled(map[string]interface{}{
		"resources": map[string]interface{}{
			"requests": map[string]interface{}{"cpu": "100m", "memory": "256Mi"},
			"limits":   map[string]interface{}{"cpu": "500m", "memory": "1G"},
		},
	}))
	if err == nil || !strings.Contains(err.Error(), "binary suffix") {
		t.Fatalf("a 1G memory limit with the derivation on: got %v, want a refusal asking for a binary suffix", err)
	}
}

// GOMEMLIMIT must always land strictly below the container limit it came from.
//
// This is the safety direction. GOMEMLIMIT is a soft target the collector aims at,
// while the cgroup limit is a hard kill — and the limit also covers goroutine
// stacks and runtime bookkeeping that sit outside the Go heap. A derivation that
// ever produced 100% or more would convert a collectable condition into an
// OOMKill, which reads as a crash rather than as a tuning mistake.
func TestGoMemLimitStaysBelowTheContainerLimit(t *testing.T) {
	for _, c := range goContainers(renderContainers(t, enabled(nil))) {
		if c.goMemLimit == "" || c.memoryLimit == "" {
			continue
		}
		if mib(t, c.goMemLimit) >= mib(t, c.memoryLimit) {
			t.Errorf("%s: GOMEMLIMIT %s is not below its %s container limit — the pod "+
				"will be OOMKilled where it should have collected",
				c.area, c.goMemLimit, c.memoryLimit)
		}
	}
}

// Turning the derivation off must actually leave Go alone, and a per-area override
// must actually win. Both are escape hatches, and an escape hatch that quietly
// does nothing is worse than not offering one.
func TestGoMemLimitEscapeHatches(t *testing.T) {
	t.Run("percent 0 removes it from every area that sets none of its own", func(t *testing.T) {
		for _, c := range renderContainers(t, map[string]interface{}{"goMemLimitPercent": 0}) {
			if eventPathAreas[c.area] {
				continue // ships its own 75; the next case turns that off
			}
			if c.goMemLimit != "" {
				t.Errorf("%s still carries GOMEMLIMIT=%s after disabling the derivation",
					c.area, c.goMemLimit)
			}
		}
	})

	t.Run("an event-path area's own 0 turns its GOMEMLIMIT and GOGC off, and only its own", func(t *testing.T) {
		got := byArea(t, renderContainers(t, map[string]interface{}{
			"functionalAreas": map[string]interface{}{
				"device-management": map[string]interface{}{"goMemLimitPercent": 0, "gogc": 0},
			},
		}))
		if c := got["device-management"]; c.goMemLimit != "" || c.gogc != "" {
			t.Errorf("device-management carries GOMEMLIMIT=%q GOGC=%q after turning both off",
				c.goMemLimit, c.gogc)
		}
		for area := range eventPathAreas {
			if area == "device-management" {
				continue
			}
			if c := got[area]; c.goMemLimit == "" || c.gogc != "400" {
				t.Errorf("%s lost its shipped tuning (GOMEMLIMIT=%q GOGC=%q) when device-management turned its own off",
					area, c.goMemLimit, c.gogc)
			}
		}
	})

	t.Run("per-area override wins", func(t *testing.T) {
		const want = "160MiB"
		cs := goContainers(renderContainers(t, enabled(map[string]interface{}{
			"functionalAreas": map[string]interface{}{
				"device-management": map[string]interface{}{"goMemLimit": want},
			},
		})))
		var checkedOverridden, checkedOther bool
		for _, c := range cs {
			if c.area == "device-management" {
				checkedOverridden = true
				if c.goMemLimit != want {
					t.Errorf("device-management: GOMEMLIMIT %q, want the override %q",
						c.goMemLimit, want)
				}
				continue
			}
			// The counterweight: overriding one area must not silently retune the rest.
			if c.goMemLimit == want {
				t.Errorf("%s picked up device-management's override", c.area)
			}
			if c.memoryLimit != "" && c.goMemLimit == "" {
				t.Errorf("%s lost its derived GOMEMLIMIT when another area was overridden", c.area)
			}
			checkedOther = true
		}
		if !checkedOverridden || !checkedOther {
			t.Fatal("did not see both an overridden and a non-overridden area — the " +
				"assertions above did not run on what they claim to cover")
		}
	})

	t.Run("no memory limit means no GOMEMLIMIT", func(t *testing.T) {
		// requests-only is a legal configuration (values.schema.json requires only
		// requests). There is nothing to derive from, and a guess would be worse than
		// Go's own default.
		// Helm COALESCES maps, so supplying only `requests` leaves the chart's own
		// `limits` in place — an explicit null is what actually removes the key. That
		// is worth stating because the naive version of this test passes for the wrong
		// reason: it renders the default limits and then asserts against them.
		for _, c := range goContainers(renderContainers(t, enabled(map[string]interface{}{
			"resources": map[string]interface{}{
				"requests": map[string]interface{}{"cpu": "100m", "memory": "128Mi"},
				"limits":   nil,
			},
		}))) {
			if c.goMemLimit != "" {
				t.Errorf("%s got GOMEMLIMIT=%s with no memory limit to derive it from",
					c.area, c.goMemLimit)
			}
		}
	})
}

// goMemLimitForArea returns the single distinct GOMEMLIMIT across the rendered
// containers, failing if they disagree — so a test asserting "the" value cannot
// pass by finding one agreeable container among several.
func goMemLimitForArea(t *testing.T, cs []renderedContainer) string {
	t.Helper()
	seen := map[string]bool{}
	for _, c := range cs {
		seen[c.goMemLimit] = true
	}
	if len(seen) != 1 {
		t.Fatalf("expected one GOMEMLIMIT across all containers, got %d distinct values: %v",
			len(seen), seen)
	}
	for v := range seen {
		if v == "" {
			t.Fatal("no GOMEMLIMIT was rendered at all")
		}
		return v
	}
	return ""
}

// The nginx container must NOT get a Go tuning knob.
//
// It carries a memory limit like every other container, so a derivation keyed on
// "has a limit" rather than on "is a Go binary" would reach it — and the result
// would be inert but misleading, the kind of detail a reader uses to infer what a
// service is written in.
func TestGoMemLimitIsNotSetOnTheNginxContainer(t *testing.T) {
	var seen bool
	for _, c := range renderContainers(t, enabled(nil)) {
		if c.area != frontendArea {
			continue
		}
		seen = true
		if c.goMemLimit != "" {
			t.Errorf("the nginx console container carries GOMEMLIMIT=%s, which does "+
				"nothing except imply it is a Go service", c.goMemLimit)
		}
	}
	if !seen {
		t.Fatalf("no %q container rendered — this test asserted nothing", frontendArea)
	}
}

// The schema must reject a GOMEMLIMIT at or above the container limit.
//
// goMemLimitPercent is the one value here an operator is likely to reach for and
// likely to get wrong in the dangerous direction — "make it use the whole limit"
// sounds like tuning and is actually the difference between a GC pause and an
// OOMKill. The chart's own guard is the cheapest place to say no.
func TestSchemaRejectsAGoMemLimitPercentThatWouldOOMKill(t *testing.T) {
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}
	for _, pct := range []int{100, 150} {
		inst := action.NewInstall(&action.Configuration{})
		inst.ReleaseName = "dc-gomemlimit"
		inst.Namespace = "default"
		inst.DryRun = true
		inst.ClientOnly = true
		_, err := inst.RunWithContext(t.Context(), ch, map[string]interface{}{
			"goMemLimitPercent": pct,
			"instance": map[string]interface{}{
				"id": "dctest",
				"config": map[string]interface{}{
					"infrastructure": map[string]interface{}{
						"secrets": map[string]interface{}{
							"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
						},
					},
				},
			},
		})
		if err == nil {
			t.Errorf("goMemLimitPercent=%d rendered cleanly: the pods would be OOMKilled "+
				"at the point they should have collected", pct)
		}
	}
}

// eventPathAreas are the five areas that ship Go runtime tuning: GOGC 400 and a GOMEMLIMIT at
// 75% of their own memory limit. They are the areas that set eventPathSpread.
var eventPathAreas = map[string]bool{
	"device-management": true, "event-management": true, "device-state": true,
	"event-sources": true, "event-processing": true,
}

// 🔑 THE SHIPPED DEFAULT TUNES THE FIVE EVENT-PATH AREAS AND LEAVES GO ALONE EVERYWHERE ELSE.
//
// GOMEMLIMIT alone was measured to shrink nothing and to cost GC CPU, which is why the top
// level stays 0. What changed is the other half: with GOGC 400 the collector ran 4 to 13 times
// less often on these five services and CPU per event fell 4 to 18%, and the soft limit is
// what keeps that larger heap target from passing the container limit. So the five ship both,
// together: GOGC 400 without the limit is an OOMKill waiting for a spike, and the limit
// without GOGC 400 is the cost with none of the benefit. This test holds the pair, and that
// no other area picked either up.
func TestEventPathAreasShipGoRuntimeTuningAndNoOtherAreaDoes(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range goContainers(renderContainers(t, nil)) {
		seen[c.area] = true
		if !eventPathAreas[c.area] {
			if c.goMemLimit != "" || c.gogc != "" {
				t.Errorf("%s carries GOMEMLIMIT=%q GOGC=%q with the shipped defaults: only the "+
					"five event-path areas were measured with them", c.area, c.goMemLimit, c.gogc)
			}
			continue
		}
		if c.gogc != "400" {
			t.Errorf("%s: GOGC = %q, want 400", c.area, c.gogc)
		}
		if c.memoryLimit == "" {
			t.Fatalf("%s has no memory limit, so nothing to derive GOMEMLIMIT from", c.area)
		}
		if want := mib(t, c.memoryLimit) * 75 / 100; c.goMemLimit == "" || mib(t, c.goMemLimit) != want {
			t.Errorf("%s: GOMEMLIMIT = %q, want %d MiB (75%% of the %s limit)",
				c.area, c.goMemLimit, want, c.memoryLimit)
		}
	}
	for area := range eventPathAreas {
		if !seen[area] {
			t.Errorf("%s did not render, so nothing above checked it", area)
		}
	}
}

// A raised TOP-LEVEL memory limit reaches event-processing: its 384Mi is a floor, applied only
// when the merged limit is lower, not an area-level limit that would silently beat it. An
// operator's own area-level limit is used as written, lower than the floor included.
func TestEventProcessingMemoryFloorYieldsToALargerTopLevelAndToItsOwnLimit(t *testing.T) {
	top := func(mem string) map[string]interface{} {
		return map[string]interface{}{"resources": map[string]interface{}{
			"requests": map[string]interface{}{"cpu": "100m", "memory": "128Mi"},
			"limits":   map[string]interface{}{"cpu": "500m", "memory": mem},
		}}
	}
	ep := func(vals map[string]interface{}) renderedContainer {
		return byArea(t, renderContainers(t, vals))["event-processing"]
	}
	if c := ep(nil); c.memoryLimit != "384Mi" || mib(t, c.goMemLimit) != 288 {
		t.Errorf("shipped: limit %q GOMEMLIMIT %q, want 384Mi and 288MiB", c.memoryLimit, c.goMemLimit)
	}
	if c := ep(top("256Mi")); c.memoryLimit != "384Mi" {
		t.Errorf("a top-level limit below the floor gave %q, want the floor 384Mi", c.memoryLimit)
	}
	if c := ep(top("1Gi")); c.memoryLimit != "1Gi" || mib(t, c.goMemLimit) != 768 {
		t.Errorf("a raised top-level limit gave %q / %q, want 1Gi / 768MiB: the floor beat the operator",
			c.memoryLimit, c.goMemLimit)
	}
	own := top("256Mi")
	own["functionalAreas"] = map[string]interface{}{"event-processing": map[string]interface{}{
		"resources": map[string]interface{}{"limits": map[string]interface{}{"memory": "300Mi"}}}}
	if c := ep(own); c.memoryLimit != "300Mi" {
		t.Errorf("the area's own 300Mi limit gave %q, want it used as written", c.memoryLimit)
	}
}

// An area-level goMemLimitPercent of 100 is refused by the schema, as the top-level one is.
func TestAnAreaGoMemLimitPercentOfOneHundredIsRefused(t *testing.T) {
	_, err := renderChart(t, map[string]interface{}{"functionalAreas": map[string]interface{}{
		"device-management": map[string]interface{}{"goMemLimitPercent": 100}}})
	if err == nil {
		t.Fatal("an area-level goMemLimitPercent of 100 rendered: the pod would be OOMKilled where it should collect")
	}
}

// An area's own percentage beats a non-zero top-level one that differs from 75, and the areas
// that set none of their own follow the top level.
func TestAnAreasPercentBeatsADifferentNonZeroTopLevel(t *testing.T) {
	got := byArea(t, renderContainers(t, map[string]interface{}{
		"goMemLimitPercent": 50,
		"functionalAreas": map[string]interface{}{
			"device-management": map[string]interface{}{"goMemLimitPercent": 60},
		},
	}))
	if c := got["device-management"]; mib(t, c.goMemLimit) != mib(t, c.memoryLimit)*60/100 {
		t.Errorf("device-management GOMEMLIMIT %q of a %s limit, want its own 60%%", c.goMemLimit, c.memoryLimit)
	}
	// event-management ships its own 75, which also beats the top-level 50.
	if c := got["event-management"]; mib(t, c.goMemLimit) != mib(t, c.memoryLimit)*75/100 {
		t.Errorf("event-management GOMEMLIMIT %q of a %s limit, want its shipped 75%%", c.goMemLimit, c.memoryLimit)
	}
	// an area with none follows the top level.
	if c := got["command-delivery"]; c.memoryLimit != "" && mib(t, c.goMemLimit) != mib(t, c.memoryLimit)*50/100 {
		t.Errorf("command-delivery GOMEMLIMIT %q of a %s limit, want the top-level 50%%", c.goMemLimit, c.memoryLimit)
	}
}
