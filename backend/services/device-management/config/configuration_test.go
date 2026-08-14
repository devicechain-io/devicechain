// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// The default configuration is valid and defaults the auth mode to required and
// the hot-path cache sizes/TTLs to their defaults (ADR-022 review B2).
func TestDefaultConfigurationValid(t *testing.T) {
	cfg := NewDeviceManagementConfiguration()
	assert.Equal(t, AuthModeRequired, cfg.DeviceAuthMode)
	assert.Equal(t, DefaultDeviceCacheTtlSeconds, cfg.DeviceCacheTtlSeconds)
	assert.Equal(t, DefaultRelationshipCacheTtlSeconds, cfg.RelationshipCacheTtlSeconds)
	assert.Equal(t, DefaultMetricDefCacheTtlSeconds, cfg.MetricDefCacheTtlSeconds)
	assert.Equal(t, DefaultMembershipCacheTtlSeconds, cfg.MembershipCacheTtlSeconds)
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
	assert.Equal(t, AuthModeRequired, cfg.DeviceAuthMode)
}

// An explicitly-set auth mode is preserved through load/default — the operator's
// opt-out to "optional" is not clobbered by the new "required" default.
func TestLoadPreservesExplicitAuthMode(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	err := core.LoadConfiguration([]byte(`{"DeviceAuthMode":"optional"}`), cfg)

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

// A negative maxEventFutureSkewSeconds fails the load closed. It used to be accepted and
// silently DISABLE the clock-skew bound — eventtime reads maxSkew <= 0 as "no ceiling" — which
// costs far more than a wrong chart: one event dated years out pins a device's last-activity
// under the strictly-newer guard, so its inactivity sweep never fires again and it can never be
// seen to go offline. Every sibling bound in the same Validate was checked; this one was not.
func TestLoadRejectsNegativeEventSkew(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	err := core.LoadConfiguration([]byte(`{"MaxEventFutureSkewSeconds":-1}`), cfg)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "maxEventFutureSkewSeconds")
}

// NEGATIVE CONTROL for the check above, in both directions. The unset value must still DEFAULT
// (0 is "the operator said nothing", not "the operator asked for no bound"), and a positive
// override must still be preserved rather than swept up by a check written as `<= 0`.
func TestEventSkewUnsetDefaultsAndPositiveIsKept(t *testing.T) {
	unset := &DeviceManagementConfiguration{}
	assert.NoError(t, core.LoadConfiguration([]byte(`{"RdbConfiguration":{"SqlDebug":true}}`), unset))
	assert.Equal(t, DefaultMaxEventFutureSkewSeconds, unset.MaxEventFutureSkewSeconds)
	assert.NoError(t, unset.Validate())

	explicit := &DeviceManagementConfiguration{}
	assert.NoError(t, core.LoadConfiguration([]byte(`{"MaxEventFutureSkewSeconds":30}`), explicit))
	assert.Equal(t, 30, explicit.MaxEventFutureSkewSeconds)
	assert.NoError(t, explicit.Validate())
}
