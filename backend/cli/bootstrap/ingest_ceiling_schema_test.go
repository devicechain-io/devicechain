// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"
)

// event-sources, lwm2m-ingest and sparkplug-ingest refuse a negative ingest ceiling at
// startup, and before that they silently replaced it with the platform ceiling. The chart
// passes an area's config through as written, so a negative an operator put in the values
// would render cleanly and then stop the new pods mid-upgrade. The chart's schema refuses
// it first, which makes dcctl refuse the overlay and helm refuse the render before
// anything changes. Zero is still accepted: it means unset, and the service applies the
// platform default.
func TestChartSchemaRefusesANegativeIngestCeiling(t *testing.T) {
	overlay := func(area, key string, v interface{}) map[string]interface{} {
		return map[string]interface{}{"functionalAreas": map[string]interface{}{
			area: map[string]interface{}{"config": map[string]interface{}{
				"ingestRateLimit": map[string]interface{}{key: v},
			}},
		}}
	}
	for _, area := range []string{"event-sources", "lwm2m-ingest", "sparkplug-ingest"} {
		for _, key := range []string{"readingsPerSecond", "burst"} {
			t.Run(area+"/"+key, func(t *testing.T) {
				err := ValidateValuesOverlay(overlay(area, key, -1))
				if err == nil {
					t.Fatalf("functionalAreas.%s.config.ingestRateLimit.%s = -1 passed the chart schema; it must be refused", area, key)
				}
				if !strings.Contains(err.Error(), key) {
					t.Fatalf("the refusal must name %s, got: %v", key, err)
				}
				if err := ValidateValuesOverlay(overlay(area, key, 0)); err != nil {
					t.Fatalf("ingestRateLimit.%s = 0 means unset and must still pass the chart schema: %v", key, err)
				}
			})
		}
	}
}
