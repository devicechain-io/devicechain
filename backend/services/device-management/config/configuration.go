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
	// SUBJECT_ALARM_EVENTS carries alarm state-change events (ADR-041) re-emitted on
	// each alarm transition — the substrate for graphql-ws subscriptions (ADR-037)
	// and notifications (ADR-017).
	SUBJECT_ALARM_EVENTS = "alarm-events"
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
	// device token. It is an explicit opt-out for trusted/bootstrapping transports;
	// it is not a secure posture for untrusted ones.
	AuthModeOptional = "optional"
	// AuthModeRequired rejects any event that does not present a valid credential.
	// This is the default, hardened posture: paired with the ADR-025 broker
	// auth-callout (which authenticates the connection), requiring a per-event
	// credential also closes intra-tenant spoofing — an authenticated device cannot
	// publish an event under another device's self-asserted token.
	AuthModeRequired = "required"
)

// Defaults for the hot-path resolution caches (ADR-022 review B2). A short TTL
// bounds staleness for entries that change rarely; the caches are NATS JetStream
// KV buckets (ADR-007), which are server-side and unbounded, so there is no
// client-side size to configure.
const (
	DefaultDeviceCacheTtlSeconds       = 60
	DefaultRelationshipCacheTtlSeconds = 60
	DefaultMetricDefCacheTtlSeconds    = 60
)

type DeviceManagementConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration
	// DeviceAuthMode selects how inbound events are authenticated (one of the
	// AuthMode* constants). Empty is treated as AuthModeRequired (the hardened
	// default); relax to "optional"/"disabled" only for a trusted transport.
	DeviceAuthMode string

	// Hot inbound-event resolution path caches (ADR-022 review B2).
	// DeviceCacheTtlSeconds bounds the device-by-token cache;
	// RelationshipCacheTtlSeconds bounds the tracked-relationships-by-source-device
	// cache; MetricDefCacheTtlSeconds bounds the per-device-type metric-definition
	// cache used by ingest-time metric validation (ADR-016). All are NATS KV bucket
	// TTLs, in seconds (ADR-007).
	DeviceCacheTtlSeconds       int
	RelationshipCacheTtlSeconds int
	MetricDefCacheTtlSeconds    int
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
		c.DeviceAuthMode = AuthModeRequired
	}
	if c.DeviceCacheTtlSeconds == 0 {
		c.DeviceCacheTtlSeconds = DefaultDeviceCacheTtlSeconds
	}
	if c.RelationshipCacheTtlSeconds == 0 {
		c.RelationshipCacheTtlSeconds = DefaultRelationshipCacheTtlSeconds
	}
	if c.MetricDefCacheTtlSeconds == 0 {
		c.MetricDefCacheTtlSeconds = DefaultMetricDefCacheTtlSeconds
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
	if c.RelationshipCacheTtlSeconds <= 0 {
		return fmt.Errorf("relationshipCacheTtlSeconds must be positive (got %d)", c.RelationshipCacheTtlSeconds)
	}
	if c.MetricDefCacheTtlSeconds <= 0 {
		return fmt.Errorf("metricDefCacheTtlSeconds must be positive (got %d)", c.MetricDefCacheTtlSeconds)
	}
	return nil
}
