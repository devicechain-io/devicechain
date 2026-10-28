// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalloutDefaultsAndLoad(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	assert.Equal(t, 16, cfg.Callout.JitMaxInFlight)
	assert.False(t, cfg.Callout.JitSourceTrusted, "the per-source bucket is off by default")
	assert.Equal(t, cfg.Callout, NewDeviceManagementConfiguration().Callout)

	cfg = &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"callout":{"jitMaxInFlight":4,"jitSourceTrusted":true}}`), cfg))
	assert.Equal(t, 4, cfg.Callout.JitMaxInFlight)
	assert.True(t, cfg.Callout.JitSourceTrusted)
}

func TestCalloutFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, doc, wantErr string }{
		{"a misspelled key", `{"callout":{"jitSlots":4}}`, `unknown field "jitSlots"`},
		{"negative slots", `{"callout":{"jitMaxInFlight":-1}}`, "callout.jitMaxInFlight must be between 1 and 128"},
		{"slots over the ceiling", `{"callout":{"jitMaxInFlight":129}}`, "callout.jitMaxInFlight must be between 1 and 128"},
		{"slots at the ceiling", `{"callout":{"jitMaxInFlight":128}}`, ""},
		{"slots at the floor", `{"callout":{"jitMaxInFlight":1}}`, ""},
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
