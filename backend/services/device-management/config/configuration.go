// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
)

const (
	SUBJECT_FAILED_EVENTS   = "failed-events"
	SUBJECT_RESOLVED_EVENTS = "resolved-events"
)

// Device authentication policy applied to inbound events (transport security,
// ADR-014).
const (
	// AuthModeDisabled performs no authentication: the self-asserted device token
	// on the event is trusted. Appropriate only when the transport itself is
	// trusted (e.g. a broker that already authenticated the device).
	AuthModeDisabled = "disabled"
	// AuthModeOptional authenticates when a credential is presented (rejecting
	// bad credentials) but allows events that present none, falling back to the
	// device token. This is the migration default; it is not a secure posture for
	// untrusted transports.
	AuthModeOptional = "optional"
	// AuthModeRequired rejects any event that does not present a valid credential.
	// This is the hardened production posture.
	AuthModeRequired = "required"
)

// Defaults for the hot-path resolution caches (ADR-022 review B2). A short TTL
// bounds staleness for entries that change rarely; the size bounds memory.
const (
	DefaultDeviceCacheTtlSeconds       = 60
	DefaultDeviceCacheSize             = 1000
	DefaultRelationshipCacheTtlSeconds = 60
	DefaultRelationshipCacheSize       = 1000
)

type DeviceManagementConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration
	// DeviceAuthMode selects how inbound events are authenticated (one of the
	// AuthMode* constants). Empty is treated as AuthModeOptional.
	DeviceAuthMode string

	// Hot inbound-event resolution path caches (ADR-022 review B2). DeviceCache*
	// bounds the device-by-token cache; RelationshipCache* bounds the
	// tracked-relationships-by-source-device cache. TTLs are in seconds.
	DeviceCacheTtlSeconds       int
	DeviceCacheSize             int
	RelationshipCacheTtlSeconds int
	RelationshipCacheSize       int
}

// Creates the default device management configuration
func NewDeviceManagementConfiguration() *DeviceManagementConfiguration {
	cfg := &DeviceManagementConfiguration{
		RdbConfiguration: config.MicroserviceDatastoreConfiguration{
			SqlDebug: true,
		},
	}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults fills unset fields with their defaults so configuration loaded
// from a document that omits them is still well-formed (ADR-022 decision 1).
func (c *DeviceManagementConfiguration) ApplyDefaults() {
	if c.DeviceAuthMode == "" {
		c.DeviceAuthMode = AuthModeOptional
	}
	if c.DeviceCacheTtlSeconds == 0 {
		c.DeviceCacheTtlSeconds = DefaultDeviceCacheTtlSeconds
	}
	if c.DeviceCacheSize == 0 {
		c.DeviceCacheSize = DefaultDeviceCacheSize
	}
	if c.RelationshipCacheTtlSeconds == 0 {
		c.RelationshipCacheTtlSeconds = DefaultRelationshipCacheTtlSeconds
	}
	if c.RelationshipCacheSize == 0 {
		c.RelationshipCacheSize = DefaultRelationshipCacheSize
	}
}

// Validate enforces semantic constraints after decoding and defaulting, failing
// the load closed on an invalid configuration (ADR-022 decision 1).
func (c *DeviceManagementConfiguration) Validate() error {
	switch c.DeviceAuthMode {
	case AuthModeDisabled, AuthModeOptional, AuthModeRequired:
	default:
		return fmt.Errorf("deviceAuthMode must be one of %q, %q, %q (got %q)",
			AuthModeDisabled, AuthModeOptional, AuthModeRequired, c.DeviceAuthMode)
	}
	if c.DeviceCacheTtlSeconds <= 0 {
		return fmt.Errorf("deviceCacheTtlSeconds must be positive (got %d)", c.DeviceCacheTtlSeconds)
	}
	if c.DeviceCacheSize <= 0 {
		return fmt.Errorf("deviceCacheSize must be positive (got %d)", c.DeviceCacheSize)
	}
	if c.RelationshipCacheTtlSeconds <= 0 {
		return fmt.Errorf("relationshipCacheTtlSeconds must be positive (got %d)", c.RelationshipCacheTtlSeconds)
	}
	if c.RelationshipCacheSize <= 0 {
		return fmt.Errorf("relationshipCacheSize must be positive (got %d)", c.RelationshipCacheSize)
	}
	return nil
}
