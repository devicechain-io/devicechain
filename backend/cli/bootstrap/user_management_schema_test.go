// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chartutil"
)

// validateAgainstChartSchemaOnly runs values.schema.json over the chart's defaults with
// the overlay on top, WITHOUT dcctl's overlay allowlist. dcctl refuses any overlay that
// sets functionalAreas.user-management.config.auth (valuesoverlay.go), so the schema is
// what stands between a plain `helm install/upgrade` and a pod that will not start.
func validateAgainstChartSchemaOnly(t *testing.T, overlay map[string]interface{}) error {
	t.Helper()
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading the embedded chart: %v", err)
	}
	probe := overlaySchemaProbe()
	mergeOverlay(probe, deepCopyValues(overlay))
	coalesced, err := chartutil.CoalesceValues(ch, probe)
	if err != nil {
		t.Fatalf("coalescing the overlay: %v", err)
	}
	return chartutil.ValidateAgainstSchema(ch, coalesced)
}

// user-management refuses a negative signing-key age or retention, or one that overflows
// the day-to-Duration conversion, at startup; before, a negative max age silently turned
// age-based key rotation off. The chart passes the area's config through as written, so a
// helm user's bad value would render cleanly and then stop the new pods mid-upgrade. The
// chart's schema refuses it first. dcctl never renders these keys and refuses an overlay
// that sets them, which TestDcctlOverlayStillRefusesUserManagementAuth pins.
func TestChartSchemaRefusesOutOfRangeSigningKeyDays(t *testing.T) {
	// This mirrors config.MaxSigningKeyDays in user-management, which is not a
	// dependency of dcctl. Both bounds must stay in step with the service.
	const maxSigningKeyDays = 106751
	overlay := func(auth map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"functionalAreas": map[string]interface{}{
			"user-management": map[string]interface{}{"config": map[string]interface{}{"auth": auth}},
		}}
	}
	for _, key := range []string{"signingKeyMaxAgeDays", "signingKeyRetentionDays"} {
		for _, days := range []int{-1, maxSigningKeyDays + 1} {
			t.Run(fmt.Sprintf("%s/%d/refused", key, days), func(t *testing.T) {
				err := validateAgainstChartSchemaOnly(t, overlay(map[string]interface{}{key: days}))
				if err == nil {
					t.Fatalf("functionalAreas.user-management.config.auth.%s = %d passed the chart schema; it must be refused", key, days)
				}
				if !strings.Contains(err.Error(), key) {
					t.Fatalf("the refusal must name %s, got: %v", key, err)
				}
			})
		}
		for _, days := range []int{0, maxSigningKeyDays} {
			t.Run(fmt.Sprintf("%s/%d/accepted", key, days), func(t *testing.T) {
				if err := validateAgainstChartSchemaOnly(t, overlay(map[string]interface{}{key: days})); err != nil {
					t.Fatalf("%s = %d must still pass the chart schema: %v", key, days, err)
				}
			})
		}
		t.Run(key+"/issuerUrl", func(t *testing.T) {
			if err := validateAgainstChartSchemaOnly(t, overlay(map[string]interface{}{
				key:         maxSigningKeyDays,
				"issuerUrl": "https://dc.example.com/api/user-management",
			})); err != nil {
				t.Fatalf("other auth keys alongside valid %s must still pass the chart schema: %v", key, err)
			}
		})
	}
}

// dcctl does not author user-management's auth block and refuses an overlay that sets it,
// so the only operator who can reach the new startup refusal is one using helm directly.
func TestDcctlOverlayStillRefusesUserManagementAuth(t *testing.T) {
	err := ValidateValuesOverlay(map[string]interface{}{"functionalAreas": map[string]interface{}{
		"user-management": map[string]interface{}{"config": map[string]interface{}{
			"auth": map[string]interface{}{"signingKeyMaxAgeDays": 90},
		}},
	}})
	if err == nil || !strings.Contains(err.Error(), "functionalAreas.user-management.config.auth") {
		t.Fatalf("dcctl must keep refusing an overlay that sets user-management's auth block, got: %v", err)
	}
}

// dcctl allows dead-letter tuning, so its overlay validation must enforce the duration bounds.
func TestChartSchemaRefusesDeadLetterSettingsThatOverflowADuration(t *testing.T) {
	// These mirror config.MaxDurationDays and config.MaxDurationSeconds in user-management.
	const maxDurationDays = 106751
	const maxDurationSeconds = 9223372036
	for _, tc := range []struct {
		key      string
		refused  []int
		accepted []int
	}{
		{"retentionDays", []int{maxDurationDays + 1, -1}, []int{maxDurationDays, 0}},
		{"sweepSeconds", []int{maxDurationSeconds + 1}, []int{maxDurationSeconds, 0, -1}},
	} {
		overlay := func(value int) map[string]interface{} {
			return map[string]interface{}{"functionalAreas": map[string]interface{}{
				"user-management": map[string]interface{}{"config": map[string]interface{}{
					"deadLetters": map[string]interface{}{tc.key: value},
				}},
			}}
		}
		for _, value := range tc.refused {
			t.Run(fmt.Sprintf("%s/%d/refused", tc.key, value), func(t *testing.T) {
				err := ValidateValuesOverlay(overlay(value))
				if err == nil {
					t.Fatalf("deadLetters.%s = %d passed the chart schema; it must be refused", tc.key, value)
				}
				if !strings.Contains(err.Error(), "/deadLetters/"+tc.key) {
					t.Fatalf("the refusal must name /deadLetters/%s, got: %v", tc.key, err)
				}
			})
		}
		for _, value := range tc.accepted {
			t.Run(fmt.Sprintf("%s/%d/accepted", tc.key, value), func(t *testing.T) {
				if err := ValidateValuesOverlay(overlay(value)); err != nil {
					t.Fatalf("deadLetters.%s = %d must pass the chart schema: %v", tc.key, value, err)
				}
			})
		}
	}
}
