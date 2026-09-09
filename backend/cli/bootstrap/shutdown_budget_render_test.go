// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/base64"
	"strings"
	"testing"
)

// The shutdown budget is the one instance-config block an operator does NOT write:
// the chart composes it from the top-level shutdownDrainSeconds and
// terminationGracePeriodSeconds values, so that the window a service validates and
// the grace period its pod is actually given come from the same two numbers.
//
// That makes the chart and the Go struct joined by nothing but spelling, in exactly
// the way the JetStream ceilings in instance_config_render_test.go are — and worse,
// because a discarded value here does not misbehave until a pod is being terminated.
// These render the real embedded chart through the real Helm engine and decode the
// result with the real config loader.

// renderShutdownBudget renders the chart with the given top-level overrides and
// returns the drain window and grace period that reached the instance config.
func renderShutdownBudget(t *testing.T, overrides map[string]interface{}) (drain *int, grace int) {
	t.Helper()

	vals := map[string]interface{}{
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
	}
	for k, v := range overrides {
		vals[k] = v
	}

	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}
	cfg, err := renderInstanceConfigFromChart(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("rendering instance config: %v", err)
	}
	if cfg == nil {
		t.Fatal("no rendered Secret carried an `instance` key: the instance config never " +
			"reached a pod, so nothing below is measuring the chart")
	}
	// Deliberately NOT ApplyDefaults: the point is to observe what the chart
	// delivered, and defaulting would fill a discarded key back in with the very
	// number the test is trying to distinguish it from.
	return cfg.Infrastructure.Shutdown.DrainSeconds, cfg.Infrastructure.Shutdown.TerminationGracePeriodSeconds
}

// The chart's own defaults must arrive in the struct, on the fields they name.
func TestChartShutdownBudgetReachesTheStruct(t *testing.T) {
	drain, grace := renderShutdownBudget(t, nil)

	if drain == nil {
		t.Fatal("the chart delivered no drain window at all: every service would fall back " +
			"to the built-in default and shutdownDrainSeconds would be inert")
	}
	if *drain != 5 {
		t.Errorf("drainSeconds arrived as %d, want the chart's default of 5", *drain)
	}
	if grace != 30 {
		t.Errorf("terminationGracePeriodSeconds arrived as %d, want the chart's default of 30", grace)
	}
}

// 🔴 And an OVERRIDDEN pair must arrive too. Defaults alone cannot catch a chart
// that hard-codes the numbers, because the hard-coded ones would be the defaults.
func TestChartOverriddenShutdownBudgetReachesTheStruct(t *testing.T) {
	drain, grace := renderShutdownBudget(t, map[string]interface{}{
		"shutdownDrainSeconds":          9,
		"terminationGracePeriodSeconds": 45,
	})

	if drain == nil || *drain != 9 {
		t.Errorf("drainSeconds arrived as %v, want the configured 9", drain)
	}
	if grace != 45 {
		t.Errorf("terminationGracePeriodSeconds arrived as %d, want the configured 45", grace)
	}
}

// 🔑 An explicit zero has to survive the render as a zero. It is the one value
// whose meaning depends on the config carrying a POINTER — absent means "the
// platform default", 0 means "do not drain" — and a chart that omitted the key
// instead of writing 0 would turn one into the other with nothing to say so.
func TestAZeroDrainWindowSurvivesTheRenderAsZero(t *testing.T) {
	drain, _ := renderShutdownBudget(t, map[string]interface{}{"shutdownDrainSeconds": 0})

	if drain == nil {
		t.Fatal("shutdownDrainSeconds: 0 arrived as an ABSENT key, so every service would " +
			"drain for the default instead of not draining at all")
	}
	if *drain != 0 {
		t.Errorf("drainSeconds arrived as %d, want 0", *drain)
	}
}

// Writing the block by hand is REFUSED, not silently overwritten. Two sources for
// one budget is the failure this arrangement exists to prevent: the operator would
// see their own numbers in `helm get values` and the pods would be running someone
// else's.
func TestAHandWrittenShutdownBlockIsRefusedByTheChart(t *testing.T) {
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}
	vals := map[string]interface{}{
		"instance": map[string]interface{}{
			"id": "dctest",
			"config": map[string]interface{}{
				"infrastructure": map[string]interface{}{
					"secrets": map[string]interface{}{
						"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
					},
					"shutdown": map[string]interface{}{"drainSeconds": 1},
				},
			},
		},
	}

	_, err = renderInstanceConfigFromChart(t.Context(), ch, vals)
	if err == nil {
		t.Fatal("a hand-written shutdown block rendered without complaint, so the operator's " +
			"numbers were discarded in silence")
	}
	if !strings.Contains(err.Error(), "shutdownDrainSeconds") {
		t.Errorf("error %q does not say which values to set instead", err)
	}
}

// The budget the chart renders is checked before anything is installed, so an
// operator who sets a drain window their pods cannot survive learns about it here
// rather than from a pod that was SIGKILLed mid-drain and logged nothing about why.
func TestBootstrapRejectsADrainWindowTheGracePeriodCannotHold(t *testing.T) {
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}
	vals := func(drain int) map[string]interface{} {
		return map[string]interface{}{
			"shutdownDrainSeconds": drain,
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
		}
	}

	// 60 seconds of drain inside the chart's 30-second grace period: the process
	// would sleep out the window and be killed halfway through it, so the broker
	// consumers and the database pool would never be closed at all.
	err = validateRenderedInstanceConfig(t.Context(), ch, vals(60))
	if err == nil {
		t.Fatal("a drain window longer than the grace period passed the bootstrap gate")
	}
	if !strings.Contains(err.Error(), "drainSeconds") {
		t.Errorf("error %q does not name the offending key", err)
	}

	// The counterweight, twice: the chart's own default budget, and a deliberately
	// larger window that still leaves room for the teardown.
	if err := validateRenderedInstanceConfig(t.Context(), ch, vals(5)); err != nil {
		t.Errorf("the chart's own default budget was rejected: %v", err)
	}
	if err := validateRenderedInstanceConfig(t.Context(), ch, vals(15)); err != nil {
		t.Errorf("a window taking exactly half the grace period was rejected: %v", err)
	}
}
