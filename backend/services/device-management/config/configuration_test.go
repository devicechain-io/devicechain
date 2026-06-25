// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// The default configuration is valid and defaults the auth mode to optional and
// the hot-path cache sizes/TTLs to their defaults (ADR-022 review B2).
func TestDefaultConfigurationValid(t *testing.T) {
	cfg := NewDeviceManagementConfiguration()
	assert.Equal(t, AuthModeOptional, cfg.DeviceAuthMode)
	assert.Equal(t, DefaultDeviceCacheTtlSeconds, cfg.DeviceCacheTtlSeconds)
	assert.Equal(t, DefaultRelationshipCacheTtlSeconds, cfg.RelationshipCacheTtlSeconds)
	assert.Equal(t, DefaultMetricDefCacheTtlSeconds, cfg.MetricDefCacheTtlSeconds)
	assert.NoError(t, cfg.Validate())
}

// Loading a document that omits the cache settings defaults them, and the result
// validates (the cache TTLs are positive).
func TestLoadDefaultsCacheSettings(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	err := core.LoadConfiguration([]byte(`{"RdbConfiguration":{"SqlDebug":true}}`), cfg)

	assert.NoError(t, err)
	assert.Equal(t, DefaultDeviceCacheTtlSeconds, cfg.DeviceCacheTtlSeconds)
	assert.Equal(t, DefaultRelationshipCacheTtlSeconds, cfg.RelationshipCacheTtlSeconds)
	assert.NoError(t, cfg.Validate())
}

// A non-positive cache TTL fails the load closed.
func TestLoadRejectsNonPositiveCacheBound(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	err := core.LoadConfiguration([]byte(`{"DeviceCacheTtlSeconds":-1}`), cfg)

	assert.Error(t, err)
}

// Loading a document that omits the auth mode defaults it rather than leaving it
// empty (ADR-022 decision 1 defaulting via core.LoadConfiguration).
func TestLoadDefaultsAuthMode(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	err := core.LoadConfiguration([]byte(`{"RdbConfiguration":{"SqlDebug":true}}`), cfg)

	assert.NoError(t, err)
	assert.Equal(t, AuthModeOptional, cfg.DeviceAuthMode)
}

// An invalid auth mode fails the load closed.
func TestLoadRejectsInvalidAuthMode(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	err := core.LoadConfiguration([]byte(`{"DeviceAuthMode":"bogus"}`), cfg)

	assert.Error(t, err)
}

// An unknown key is rejected at load time.
func TestLoadRejectsUnknownKey(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	err := core.LoadConfiguration([]byte(`{"DeviceAuthMode":"required","Bogus":1}`), cfg)

	assert.Error(t, err)
}
