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
	inst.ReleaseName = helmReleaseName
	inst.Namespace = "default"
	inst.DryRun = true
	inst.ClientOnly = true
	inst.APIVersions = []string{"monitoring.coreos.com/v1"}

	rel, err := inst.RunWithContext(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("rendering chart: %v", err)
	}

	var out []renderedContainer
	for _, doc := range releaseutil.SplitManifests(rel.Manifest) {
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
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj.Kind != "Deployment" {
			continue
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
	t.Run("percent 0 removes it everywhere", func(t *testing.T) {
		for _, c := range renderContainers(t, map[string]interface{}{"goMemLimitPercent": 0}) {
			if c.goMemLimit != "" {
				t.Errorf("%s still carries GOMEMLIMIT=%s after disabling the derivation",
					c.area, c.goMemLimit)
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
		inst.ReleaseName = helmReleaseName
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

// The shipped default must leave Go alone.
//
// This is the assertion that keeps the measurement honest. GOMEMLIMIT was expected
// to shrink the footprint and did not: across four workload shapes in a limited
// container it produced no reduction in heap_sys and cost GC CPU, because
// steady-state memory is governed by the live set and a soft limit cannot go below
// it. What it offers is a ceiling against an OOMKill during a live-heap spike,
// which is a survivability property rather than a footprint one — worth having,
// but not worth turning on by default until it is shown to be worth its GC cost on
// real services under real load.
//
// So the mechanism ships ready and inert. If someone later flips the default
// without that evidence, this test is what makes them say so out loud.
func TestGoMemLimitIsOffByDefault(t *testing.T) {
	for _, c := range renderContainers(t, nil) {
		if c.goMemLimit != "" {
			t.Errorf("%s carries GOMEMLIMIT=%s with the shipped defaults: the derivation "+
				"is off until measurement on real services justifies its GC cost, and a "+
				"published footprint number must never rest on it", c.area, c.goMemLimit)
		}
	}
}
