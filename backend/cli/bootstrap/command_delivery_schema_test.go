// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"
)

// command-delivery refuses a negative value for its five numeric settings at startup, and
// before that it defaulted one silently. The chart passes an area's config through as
// written, so a negative an operator put in the values would render cleanly and then stop
// the new pods mid-upgrade. The chart's schema refuses it first, which makes dcctl refuse
// the overlay and helm refuse the render before anything changes. Zero is still accepted:
// it means unset, and the service applies its default.
func TestChartSchemaRefusesNegativeCommandDeliverySettings(t *testing.T) {
	keys := map[string]interface{}{
		"defaultCommandTtlSeconds": -1,
		"heldCommandCeiling":       -1,
		"sweepIntervalSeconds":     -1,
		"maxDispatchFailures":      -1,
		"deliveryMachineryReserve": -0.5,
	}
	overlay := func(key string, v interface{}) map[string]interface{} {
		return map[string]interface{}{"functionalAreas": map[string]interface{}{
			"command-delivery": map[string]interface{}{"config": map[string]interface{}{key: v}},
		}}
	}
	for key, negative := range keys {
		t.Run(key, func(t *testing.T) {
			err := ValidateValuesOverlay(overlay(key, negative))
			if err == nil {
				t.Fatalf("functionalAreas.command-delivery.config.%s = %v passed the chart schema; it must be refused", key, negative)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("the refusal must name %s, got: %v", key, err)
			}
			if err := ValidateValuesOverlay(overlay(key, 0)); err != nil {
				t.Fatalf("%s = 0 means unset and must still pass the chart schema: %v", key, err)
			}
		})
	}
}
