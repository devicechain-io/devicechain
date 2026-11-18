// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"strings"
	"testing"
)

// The chart refuses presence seconds that overflow before deployment, while
// preserving the safe duration boundary and the service's zero-as-default rule.
func TestChartSchemaRefusesSparkplugPresenceDurationOverflow(t *testing.T) {
	for _, key := range []string{"probeSeconds", "probeWindowSeconds"} {
		overlay := func(seconds int) map[string]interface{} {
			return map[string]interface{}{"functionalAreas": map[string]interface{}{
				"sparkplug-ingest": map[string]interface{}{"config": map[string]interface{}{
					"presence": map[string]interface{}{key: seconds},
				}},
			}}
		}
		for _, seconds := range []int{9223372037, -1} {
			t.Run(fmt.Sprintf("%s/%d/refused", key, seconds), func(t *testing.T) {
				err := ValidateValuesOverlay(overlay(seconds))
				if err == nil {
					t.Fatalf("presence.%s = %d passed the chart schema; it must be refused", key, seconds)
				}
				if !strings.Contains(err.Error(), "/presence/"+key) {
					t.Fatalf("the refusal must name /presence/%s, got: %v", key, err)
				}
			})
		}
		for _, seconds := range []int{9223372036, 0} {
			t.Run(fmt.Sprintf("%s/%d/accepted", key, seconds), func(t *testing.T) {
				if err := ValidateValuesOverlay(overlay(seconds)); err != nil {
					t.Fatalf("presence.%s = %d must pass the chart schema: %v", key, seconds, err)
				}
			})
		}
	}
}
