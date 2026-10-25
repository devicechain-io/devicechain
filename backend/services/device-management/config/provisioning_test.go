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

// An omitted provisioning block loads as the documented defaults, and the defaults validate.
func TestProvisioningDefaults(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	assert.Equal(t, 10*time.Second, cfg.Provisioning.ReadDeadlineDuration())
	assert.Equal(t, 64, cfg.Provisioning.MaxInFlight)
	assert.Equal(t, 60, cfg.Provisioning.SourceBurst)
	assert.Equal(t, 2, cfg.Provisioning.MaxConcurrentRegistrations)
	assert.Equal(t, time.Second, cfg.Provisioning.SourceEveryDuration())
	assert.Equal(t, cfg.Provisioning, NewDeviceManagementConfiguration().Provisioning)
}

// Written values load as written.
func TestProvisioningLoadsAsWritten(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(
		`{"provisioning":{"readDeadline":"3s","maxInFlight":8,"sourceBurst":5,"sourceEvery":"250ms"}}`), cfg))
	assert.Equal(t, 3*time.Second, cfg.Provisioning.ReadDeadlineDuration())
	assert.Equal(t, 8, cfg.Provisioning.MaxInFlight)
	assert.Equal(t, 5, cfg.Provisioning.SourceBurst)
	assert.Equal(t, 250*time.Millisecond, cfg.Provisioning.SourceEveryDuration())
}

// The typed configuration fails closed: an unknown key, an unparseable duration and every
// out-of-range bound stop the load, naming the setting, and the edges are exact.
func TestProvisioningFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, doc, wantErr string }{
		{"a misspelled key", `{"provisioning":{"maxSlots":8}}`, `unknown field "maxSlots"`},
		{"a deadline that is not a duration", `{"provisioning":{"readDeadline":"ten"}}`, "provisioning.readDeadline"},
		{"a bare number for the deadline", `{"provisioning":{"readDeadline":10}}`, "cannot unmarshal"},
		{"a negative deadline", `{"provisioning":{"readDeadline":"-1s"}}`, "provisioning.readDeadline must be above 0"},
		{"a deadline over the ceiling", `{"provisioning":{"readDeadline":"61s"}}`, "provisioning.readDeadline must be above 0 and at most 1m0s"},
		{"a deadline at the ceiling", `{"provisioning":{"readDeadline":"60s"}}`, ""},
		{"negative slots", `{"provisioning":{"maxInFlight":-1}}`, "provisioning.maxInFlight must be between 1 and 1024"},
		{"slots over the ceiling", `{"provisioning":{"maxInFlight":1025}}`, "provisioning.maxInFlight must be between 1 and 1024"},
		{"slots at the ceiling", `{"provisioning":{"maxInFlight":1024}}`, ""},
		{"concurrent registrations at the pool", `{"provisioning":{"maxConcurrentRegistrations":20}}`, "provisioning.maxConcurrentRegistrations is 20, but the connection pool holds 20"},
		{"concurrent registrations below the pool", `{"provisioning":{"maxConcurrentRegistrations":19}}`, ""},
		{"concurrent registrations against a small pool", `{"rdbConfiguration":{"maxOpenConnections":12},"provisioning":{"maxConcurrentRegistrations":12}}`, "connection pool holds 12"},
		{"negative concurrent registrations", `{"provisioning":{"maxConcurrentRegistrations":-1}}`, "must be at least 1"},
		{"a negative burst", `{"provisioning":{"sourceBurst":-3}}`, "provisioning.sourceBurst must be at least 1"},
		{"an interval that is not a duration", `{"provisioning":{"sourceEvery":"fast"}}`, "provisioning.sourceEvery"},
		{"a negative interval", `{"provisioning":{"sourceEvery":"-1s"}}`, "provisioning.sourceEvery must be above 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := core.LoadConfiguration([]byte(tc.doc), &DeviceManagementConfiguration{})
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), tc.wantErr)
			}
		})
	}
}
