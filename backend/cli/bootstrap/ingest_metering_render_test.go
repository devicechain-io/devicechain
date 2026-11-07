// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "testing"

const ingestMeteringEnvName = "EVENT_SOURCES_INGEST_METERING"

// The ingest-metering switch reaches the event-sources pod alone, defaults to legacy, and renders even
// when the release has no eventSources block at all: the shape of `helm upgrade --reuse-values` from a
// release that predates the value.
func TestIngestMeteringSwitchRendersOnEventSourcesAlone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		vals map[string]interface{}
		want string
	}{
		{"default", nil, "legacy"},
		{"lagCredit", map[string]interface{}{"eventSources": map[string]interface{}{"ingestMetering": "lagCredit"}}, "lagCredit"},
		{"eventSources null", map[string]interface{}{"eventSources": nil}, "legacy"},
		{"eventSources empty", map[string]interface{}{"eventSources": map[string]interface{}{}}, "legacy"},
	} {
		manifest, err := renderChart(t, tc.vals)
		if err != nil {
			t.Fatalf("%s: rendering: %v", tc.name, err)
		}
		pods := podsOf(t, manifest)
		if got := pods["event-sources"].env[ingestMeteringEnvName]; got != tc.want {
			t.Errorf("%s: event-sources %s = %q, want %q", tc.name, ingestMeteringEnvName, got, tc.want)
		}
		for area, p := range pods {
			if _, ok := p.env[ingestMeteringEnvName]; ok && area != "event-sources" {
				t.Errorf("%s: %s carries %s", tc.name, area, ingestMeteringEnvName)
			}
		}
	}
}

// An unknown mode is refused by the schema at render, not at pod start.
func TestIngestMeteringUnknownModeIsRefusedAtRender(t *testing.T) {
	t.Parallel()
	if _, err := renderChart(t, map[string]interface{}{"eventSources": map[string]interface{}{"ingestMetering": "bogus"}}); err == nil {
		t.Fatal("an unknown ingestMetering rendered")
	}
}
