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
	assert.False(t, cfg.Callout.IssuerPinDisabled, "the issuer pin must be ON by default")
	assert.Equal(t, DefaultCalloutIssuerPinRefresh, cfg.Callout.IssuerPinRefreshDuration())
	assert.Equal(t, DefaultCalloutIssuerPinMinRefresh, cfg.Callout.IssuerPinMinRefreshDuration())
	assert.Equal(t, DefaultCalloutIssuerPinGatherWindow, cfg.Callout.IssuerPinGatherWindowDuration())
	assert.Equal(t, 16, cfg.Callout.MaxSessionsPerDevice)
	assert.Equal(t, 20, cfg.Callout.ConnectBurstPerDevice)
	assert.Equal(t, 60, cfg.Callout.ConnectsPerMinutePerDevice)

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
		{"a misspelled pin key", `{"callout":{"issuerPinDisable":true}}`, `unknown field "issuerPinDisable"`},
		{"the pin refresh is not a duration", `{"callout":{"issuerPinRefresh":"often"}}`, "callout.issuerPinRefresh must be a duration"},
		{"the pin refresh under its floor", `{"callout":{"issuerPinRefresh":"1s"}}`, "callout.issuerPinRefresh must be a duration"},
		{"the pin refresh over its ceiling", `{"callout":{"issuerPinRefresh":"1h"}}`, "callout.issuerPinRefresh must be a duration"},
		{"the min refresh over the refresh", `{"callout":{"issuerPinRefresh":"10s","issuerPinMinRefresh":"11s"}}`, "callout.issuerPinMinRefresh must be a duration"},
		{"the min refresh at zero", `{"callout":{"issuerPinMinRefresh":"0s"}}`, "callout.issuerPinMinRefresh must be a duration"},
		{"the gather window over the lookup timeout", `{"callout":{"issuerPinGatherWindow":"3s"}}`, "callout.issuerPinGatherWindow must be a duration"},
		{"a valid pin tuning", `{"callout":{"issuerPinRefresh":"1m","issuerPinMinRefresh":"5s","issuerPinGatherWindow":"1s"}}`, ""},
		{"the pin switched off", `{"callout":{"issuerPinDisabled":true}}`, ""},
		{"negative session cap", `{"callout":{"maxSessionsPerDevice":-1}}`, "callout.maxSessionsPerDevice must be between 1 and 1024"},
		{"session cap over the ceiling", `{"callout":{"maxSessionsPerDevice":1025}}`, "callout.maxSessionsPerDevice must be between 1 and 1024"},
		{"session cap at the ceiling", `{"callout":{"maxSessionsPerDevice":1024}}`, ""},
		{"session cap at the floor", `{"callout":{"maxSessionsPerDevice":1}}`, ""},
		{"a misspelled session key", `{"callout":{"maxSessionPerDevice":4}}`, `unknown field "maxSessionPerDevice"`},
		{"negative connect burst", `{"callout":{"connectBurstPerDevice":-1}}`, "callout.connectBurstPerDevice must be between 1 and 10000"},
		{"connect burst over the ceiling", `{"callout":{"connectBurstPerDevice":10001}}`, "callout.connectBurstPerDevice must be between 1 and 10000"},
		{"negative connect rate", `{"callout":{"connectsPerMinutePerDevice":-5}}`, "callout.connectsPerMinutePerDevice must be between 1 and 60000"},
		{"connect rate over the ceiling", `{"callout":{"connectsPerMinutePerDevice":60001}}`, "callout.connectsPerMinutePerDevice must be between 1 and 60000"},
		{"a connect rate that is not a number", `{"callout":{"connectsPerMinutePerDevice":"lots"}}`, "connectsPerMinutePerDevice"},
		{"valid session bounds", `{"callout":{"maxSessionsPerDevice":64,"connectBurstPerDevice":5,"connectsPerMinutePerDevice":10}}`, ""},
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
