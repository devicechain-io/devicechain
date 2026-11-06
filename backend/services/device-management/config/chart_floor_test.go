// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestChartMemoryFloorCoversTheDefaultCacheBounds ties the chart's device-management memory
// floor to the cache bounds above, in this module, where the constants are. Round 30 on GKE
// found the per-device caches' 24 MiB default smaller than a 12,000 to 20,000 device fleet;
// raising the bound without the limit it fills would have left the pod's soft limit under the
// worst case (see the memory budget at DefaultPerDeviceCacheMiB). The rule is the one the
// deployment guide gives: 2 x (3 x perDeviceCacheMiB + credentialCacheMiB + 36 + 60) MiB,
// where 36 is the by-tenant and by-device-type caches and 60 the rest of the service, rounded
// up to a 256Mi step. The chart's floor must be at least that, and not more than a step above
// it. The values file is read from outside this module, so the test is run with -count=1.
func TestChartMemoryFloorCoversTheDefaultCacheBounds(t *testing.T) {
	raw, err := os.ReadFile("../../../../deploy/helm/devicechain/values.yaml")
	require.NoError(t, err)
	// The first memoryLimitFloor after the device-management area's key.
	m := regexp.MustCompile(`(?s)\n  device-management:\n.*?\n    memoryLimitFloor: ([0-9]+)Mi\n`).FindSubmatch(raw)
	require.NotNil(t, m, "no memoryLimitFloor in MiB under functionalAreas.device-management")
	floor, err := strconv.Atoi(string(m[1]))
	require.NoError(t, err)

	live := 3*DefaultPerDeviceCacheMiB + DefaultCredentialCacheMiB + 36 + 60
	rule := (2*live + 255) / 256 * 256
	require.GreaterOrEqual(t, floor, rule,
		"the chart floors device-management at %dMi, under the %dMi the sizing rule gives for the default cache bounds (%d MiB live when full)", floor, rule, live)
	require.LessOrEqual(t, floor, rule+256,
		"the chart floors device-management at %dMi, more than a step over the %dMi the rule gives", floor, rule)
}
