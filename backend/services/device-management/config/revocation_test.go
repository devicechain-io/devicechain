// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRevocationDefaults(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	r := cfg.Callout.Revocation
	assert.Equal(t, 2<<20, r.MaxCrlBytes)
	assert.Equal(t, 100_000, r.MaxCrlEntries)
	assert.Equal(t, 16, r.MaxListsPerAnchor)
	assert.Equal(t, 100_000, r.MaxManualEntriesPerTenant)
	assert.Equal(t, 24*time.Hour, r.DefaultStaleGraceDuration())
	assert.Equal(t, 2<<20, r.MaxGraphqlCrlBytes)
	assert.Equal(t, 1, r.MaxConcurrentWrites)
	assert.Equal(t, cfg.Callout, NewDeviceManagementConfiguration().Callout)

	cfg = &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"callout":{"revocation":{"maxCrlBytes":4194304,"defaultStaleGrace":"0s"}}}`), cfg))
	assert.Equal(t, 4<<20, cfg.Callout.Revocation.MaxCrlBytes)
	assert.Equal(t, time.Duration(0), cfg.Callout.Revocation.DefaultStaleGraceDuration(), "0s is a grace, not an absence")
	assert.Equal(t, 100_000, cfg.Callout.Revocation.MaxCrlEntries, "the rest keep their defaults")
}

// Each bound is held at both ends, and nothing is clamped.
func TestRevocationFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, doc, wantErr string }{
		{"a misspelled key", `{"callout":{"revocation":{"maxCrls":1}}}`, `unknown field "maxCrls"`},
		{"crl bytes under the floor", `{"callout":{"revocation":{"maxCrlBytes":65535}}}`, "maxCrlBytes must be between"},
		{"crl bytes at the floor", `{"callout":{"revocation":{"maxCrlBytes":65536,"maxGraphqlCrlBytes":65536}}}`, ""},
		{"crl bytes over the ceiling", `{"callout":{"revocation":{"maxCrlBytes":33554433}}}`, "maxCrlBytes must be between"},
		{"crl bytes at the ceiling", `{"callout":{"revocation":{"maxCrlBytes":33554432}}}`, ""},
		{"negative crl bytes", `{"callout":{"revocation":{"maxCrlBytes":-1}}}`, "maxCrlBytes must be between"},
		{"entries over the ceiling", `{"callout":{"revocation":{"maxCrlEntries":2000001}}}`, "maxCrlEntries must be between"},
		{"entries at the ceiling", `{"callout":{"revocation":{"maxCrlEntries":2000000}}}`, ""},
		{"negative entries", `{"callout":{"revocation":{"maxCrlEntries":-5}}}`, "maxCrlEntries must be between"},
		{"lists over the ceiling", `{"callout":{"revocation":{"maxListsPerAnchor":65}}}`, "maxListsPerAnchor must be between"},
		{"lists at the ceiling", `{"callout":{"revocation":{"maxListsPerAnchor":64}}}`, ""},
		{"manual entries over the ceiling", `{"callout":{"revocation":{"maxManualEntriesPerTenant":1000001}}}`, "maxManualEntriesPerTenant must be between"},
		{"manual entries at the ceiling", `{"callout":{"revocation":{"maxManualEntriesPerTenant":1000000}}}`, ""},
		{"grace not a duration", `{"callout":{"revocation":{"defaultStaleGrace":"a day"}}}`, "defaultStaleGrace must be a duration"},
		{"negative grace", `{"callout":{"revocation":{"defaultStaleGrace":"-1h"}}}`, "defaultStaleGrace must be a duration"},
		{"grace over 7 days", `{"callout":{"revocation":{"defaultStaleGrace":"169h"}}}`, "defaultStaleGrace must be a duration"},
		{"grace at 7 days", `{"callout":{"revocation":{"defaultStaleGrace":"168h"}}}`, ""},
		{"graphql bytes over its ceiling", `{"callout":{"revocation":{"maxGraphqlCrlBytes":2097153}}}`, "maxGraphqlCrlBytes must be between"},
		{"graphql bytes under its floor", `{"callout":{"revocation":{"maxGraphqlCrlBytes":65535}}}`, "maxGraphqlCrlBytes must be between"},
		{"graphql bytes over the crl bound", `{"callout":{"revocation":{"maxCrlBytes":65536}}}`, "cannot exceed maxCrlBytes"},
		{"writes over the ceiling", `{"callout":{"revocation":{"maxConcurrentWrites":9}}}`, "maxConcurrentWrites must be between"},
		{"writes at the ceiling", `{"callout":{"revocation":{"maxConcurrentWrites":8}}}`, ""},
		{"negative writes", `{"callout":{"revocation":{"maxConcurrentWrites":-1}}}`, "maxConcurrentWrites must be between"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := core.LoadConfiguration([]byte(tc.doc), &DeviceManagementConfiguration{})
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
