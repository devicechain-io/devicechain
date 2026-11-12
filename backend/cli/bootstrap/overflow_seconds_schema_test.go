// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"
)

// The chart must refuse duration overflow before deployment while preserving safe bounds and defaults.
func TestChartSchemaRefusesSecondsSettingsThatOverflowADuration(t *testing.T) {
	for _, tc := range []struct {
		area    string
		path    string
		disable bool
	}{
		{"update-management", "updates.sweepIntervalSeconds", false},
		{"update-management", "updates.deadlines.acknowledgeSeconds", false},
		{"update-management", "updates.deadlines.downloadSeconds", false},
		{"update-management", "updates.deadlines.verifySeconds", false},
		{"update-management", "updates.deadlines.installSeconds", false},
		{"update-management", "updates.deadlines.confirmSeconds", false},
		{"notification-management", "deliverySeconds", false},
		{"notification-management", "stateRetentionSeconds", false},
		{"notification-management", "attemptRetentionSeconds", false},
		{"notification-management", "retentionSweepSeconds", true},
		{"notification-management", "escalationSweepSeconds", true},
	} {
		t.Run(tc.area+"/"+tc.path, func(t *testing.T) {
			overlay := func(seconds int) map[string]interface{} {
				var value interface{} = seconds
				parts := strings.Split(tc.path, ".")
				for i := len(parts) - 1; i >= 0; i-- {
					value = map[string]interface{}{parts[i]: value}
				}
				return map[string]interface{}{"functionalAreas": map[string]interface{}{
					tc.area: map[string]interface{}{"config": value},
				}}
			}
			err := ValidateValuesOverlay(overlay(9223372037))
			if err == nil {
				t.Fatalf("functionalAreas.%s.config.%s = 9223372037 passed the chart schema; it must be refused", tc.area, tc.path)
			}
			// The schema names the key by its JSON pointer: updates/deadlines/acknowledgeSeconds.
			if !strings.Contains(err.Error(), "/"+strings.ReplaceAll(tc.path, ".", "/")) {
				t.Fatalf("the refusal must name %s, got: %v", tc.path, err)
			}
			for _, seconds := range []int{9223372036, 0} {
				if err := ValidateValuesOverlay(overlay(seconds)); err != nil {
					t.Fatalf("%s = %d must pass the chart schema: %v", tc.path, seconds, err)
				}
			}
			if tc.disable {
				if err := ValidateValuesOverlay(overlay(-1)); err != nil {
					t.Fatalf("%s = -1 disables the sweep and must pass the chart schema: %v", tc.path, err)
				}
			}
		})
	}
}
