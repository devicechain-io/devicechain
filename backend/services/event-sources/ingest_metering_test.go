// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func envOf(v string) func(string) string {
	return func(k string) string {
		if k == IngestMeteringEnv {
			return v
		}
		return ""
	}
}

func TestIngestMeteringDefaultsToLegacy(t *testing.T) {
	for _, v := range []string{"", "  ", "legacy"} {
		mode, err := resolveIngestMetering(envOf(v))
		require.NoError(t, err, "%q", v)
		require.Equal(t, IngestMeteringLegacy, mode)
	}
}

// lagCredit must refuse to start until the gateway runs it, and must never fall back to legacy.
func TestIngestMeteringLagCreditRefusesToStart(t *testing.T) {
	mode, err := resolveIngestMetering(envOf("lagCredit"))
	require.Error(t, err)
	require.Contains(t, err.Error(), IngestMeteringEnv)
	require.Contains(t, err.Error(), "not available in this build")
	require.Empty(t, mode, "an error must not come with a usable mode")
}

func TestIngestMeteringRejectsAnUnknownValue(t *testing.T) {
	for _, v := range []string{"lagcredit", "LEGACY", "on", "true", "lag-credit"} {
		_, err := resolveIngestMetering(envOf(v))
		require.Error(t, err, "%q must not be accepted", v)
		require.Contains(t, err.Error(), "is not a metering mode")
	}
}
