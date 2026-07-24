// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"slices"
	"testing"
)

// ResolveEnabledAreas is the whole safety of --enable-area: it must UNION the extras
// onto the profile (never replace it), dedup, and reject an unknown area or an unmet
// hard dependency BEFORE a cluster exists — so the failure is a one-line CLI error,
// not a 10-minute helm timeout.
func TestResolveEnabledAreas(t *testing.T) {
	t.Run("no extras leaves the profile path untouched", func(t *testing.T) {
		got, err := ResolveEnabledAreas("default", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil (so helmValues emits profile), got %v", got)
		}
	})

	t.Run("extra is additive on top of the profile, not a replacement", func(t *testing.T) {
		got, err := ResolveEnabledAreas("default", []string{"lwm2m-ingest"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Assert the WHOLE resolved set, not a subset: the entire standard profile must
		// survive (additive, never a filtered copy) plus exactly lwm2m-ingest. Checking
		// the full set kills a mutation that drops or replaces some base areas.
		want := []string{
			"user-management", "device-management", "event-sources", "event-management",
			"device-state", "dashboard-management", "command-delivery",
			"notification-management", "event-processing", "lwm2m-ingest",
		}
		gotSorted := slices.Clone(got)
		wantSorted := slices.Clone(want)
		slices.Sort(gotSorted)
		slices.Sort(wantSorted)
		if !slices.Equal(gotSorted, wantSorted) {
			t.Errorf("resolved set = %v, want exactly %v (default ∪ {lwm2m-ingest})", got, want)
		}
	})

	t.Run("empty profile expands to default, then unions the extra", func(t *testing.T) {
		got, err := ResolveEnabledAreas("", []string{"lwm2m-ingest"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !slices.Contains(got, "device-management") || !slices.Contains(got, "lwm2m-ingest") {
			t.Fatalf("empty profile should expand to default ∪ {lwm2m-ingest}: %v", got)
		}
	})

	t.Run("an extra already in the profile is deduped", func(t *testing.T) {
		got, err := ResolveEnabledAreas("default", []string{"device-management"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		n := 0
		for _, a := range got {
			if a == "device-management" {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("device-management should appear exactly once, appeared %d times: %v", n, got)
		}
	})

	t.Run("an unknown area is rejected up front", func(t *testing.T) {
		if _, err := ResolveEnabledAreas("default", []string{"not-a-real-area"}); err == nil {
			t.Fatal("expected an error for an unknown area, got nil")
		}
	})

	t.Run("an extra with an unmet hard dependency is rejected up front", func(t *testing.T) {
		// outbound-connectors hard-depends on event-processing, which the ingest-only
		// profile does not deploy — so the union must fail validation at the CLI, not
		// render a half-broken instance.
		if _, err := ResolveEnabledAreas("ingest-only", []string{"outbound-connectors"}); err == nil {
			t.Fatal("expected an error for an extra whose hard dependency is absent, got nil")
		}
	})

	t.Run("blank extras are ignored", func(t *testing.T) {
		got, err := ResolveEnabledAreas("default", []string{"  "})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Only whitespace extras: nothing was really added, but the flag WAS present,
		// so the result is the (non-nil) resolved default set — helmValues still emits
		// it as an explicit set. That is fine; it is deployment-equivalent to default.
		if !slices.Contains(got, "device-management") {
			t.Fatalf("expected the default set, got %v", got)
		}
	})
}

// helmValues must emit enabledFunctionalAreas IN PLACE OF profile when --enable-area
// resolved a set: the chart rejects both being present (_helpers.tpl), so emitting
// profile too would fail every additive render.
func TestHelmValuesProfileVsEnabledAreas(t *testing.T) {
	base := func() *State {
		return &State{
			Instance:      "dctest",
			Profile:       "default",
			ImageRegistry: DefaultImageRegistry,
			ImageVersion:  "v0.0.0-test",
			Values:        map[string]string{"ingressHost": "localhost"},
		}
	}

	t.Run("no extras: profile emitted, enabledFunctionalAreas absent", func(t *testing.T) {
		vals := helmValues(base())
		if _, ok := vals["profile"]; !ok {
			t.Error("expected profile to be emitted on the normal path")
		}
		if _, ok := vals["enabledFunctionalAreas"]; ok {
			t.Error("enabledFunctionalAreas must be absent when no extra area was requested")
		}
	})

	t.Run("with extras: enabledFunctionalAreas emitted, profile suppressed", func(t *testing.T) {
		st := base()
		enabled, err := ResolveEnabledAreas(st.Profile, []string{"lwm2m-ingest"})
		if err != nil {
			t.Fatalf("resolving: %v", err)
		}
		st.EnabledAreas = enabled
		vals := helmValues(st)
		if _, ok := vals["profile"]; ok {
			t.Error("profile must be suppressed when enabledFunctionalAreas is set (the chart rejects both)")
		}
		// Emitted as []interface{} of strings — the JSON-array shape the chart's values
		// schema validator accepts (a Go []string is rejected as an invalid jsonType).
		got, ok := vals["enabledFunctionalAreas"].([]interface{})
		if !ok {
			t.Fatalf("enabledFunctionalAreas missing or wrong type: %T", vals["enabledFunctionalAreas"])
		}
		if !slices.Contains(got, interface{}("lwm2m-ingest")) {
			t.Errorf("enabledFunctionalAreas should carry lwm2m-ingest: %v", got)
		}
	})
}

// The end-to-end proof: a State carrying --enable-area lwm2m-ingest, rendered through
// the SAME helmValues → embedded chart path a real bootstrap uses, actually produces
// a lwm2m-ingest workload — and a stock default render does NOT. This is what closes
// the loop from the CLI flag to a deployed area.
func TestEnableAreaRendersTheAreaWorkload(t *testing.T) {
	stock := &State{
		Instance:      "dctest",
		Profile:       "default",
		ImageRegistry: DefaultImageRegistry,
		ImageVersion:  "v0.0.0-test",
		Values:        map[string]string{"ingressHost": "localhost"},
	}
	stockAreas := containerAreas(renderContainers(t, helmValues(stock)))
	if slices.Contains(stockAreas, "lwm2m-ingest") {
		t.Fatalf("stock default must NOT deploy lwm2m-ingest (guards the additive test below): %v", stockAreas)
	}

	withArea := &State{
		Instance:      "dctest",
		Profile:       "default",
		ImageRegistry: DefaultImageRegistry,
		ImageVersion:  "v0.0.0-test",
		Values:        map[string]string{"ingressHost": "localhost"},
	}
	enabled, err := ResolveEnabledAreas(withArea.Profile, []string{"lwm2m-ingest"})
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	withArea.EnabledAreas = enabled

	gotAreas := containerAreas(renderContainers(t, helmValues(withArea)))
	if !slices.Contains(gotAreas, "lwm2m-ingest") {
		t.Fatalf("--enable-area lwm2m-ingest should render a lwm2m-ingest workload: %v", gotAreas)
	}
	// Additive: the standard areas the profile deploys must still be there.
	if !slices.Contains(gotAreas, "device-management") {
		t.Fatalf("the standard areas must survive the additive render: %v", gotAreas)
	}
}

func containerAreas(cs []renderedContainer) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.area)
	}
	return out
}
