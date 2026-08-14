// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
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
// bounds staleness for entries that change rarely.
//
// The caches are NATS JetStream KV buckets (ADR-007), so their SIZE is a
// server-side platform concern rather than a per-service one: each bucket carries
// a byte ceiling from the instance config's cache tier (kv.All / ADR-023), which
// is why there is no size to configure here. The TTL still matters to the budget
// even so — it is what bounds the working set, since a bucket only ever holds
// entries that have not yet expired.
// DefaultMaxEventFutureSkewSeconds bounds how far a device-reported occurred time may
// lead the server-stamped processed time before resolution replaces it with the ceiling.
// Generous enough for legitimate device/server clock drift, and small enough that a
// device cannot poison a shared, strictly-newer projection with a timestamp years out.
//
// It lives in device-management because resolution is the ONE place the platform decides
// what instant a reading happened at — the resolved event then travels with an already-
// bounded time, so no consumer configures or re-applies this (see core/eventtime).
const DefaultMaxEventFutureSkewSeconds = 300

const (
	DefaultDeviceCacheTtlSeconds       = 60
	DefaultRelationshipCacheTtlSeconds = 60
	DefaultMetricDefCacheTtlSeconds    = 60
	DefaultMembershipCacheTtlSeconds   = 60
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
	// MembershipCacheTtlSeconds bounds the per-entity dynamic-group membership cache
	// read on the hot resolve path when stamping scope memberships onto an event
	// (ADR-062). Negative results are cached (a non-member is the common case), and the
	// TTL is a self-healing backstop behind the explicit per-entity eviction on every
	// membership mutation.
	MembershipCacheTtlSeconds int

	// MaxEventFutureSkewSeconds bounds how far a device-reported occurred time may lead
	// the server-stamped processed time; a reading past that ceiling is stored AT the
	// ceiling. Unset (0) defaults to 300s; a negative value disables the bound, which
	// lets any device freeze its own presence and poison every strictly-newer projection
	// it feeds — see core/eventtime for what that costs.
	//
	// 🔴 UPGRADE ORDER MATTERS ONCE, AT THE RELEASE THAT MOVED THIS KEY HERE. The bound
	// used to be applied downstream, by the detection engine, on events it read off the
	// resolved stream; it is now applied here, before they are published. So a resolved
	// event published by an OLDER device-management and consumed by a NEWER
	// event-processing is bounded by neither — and if such an event carries a far-future
	// time it advances DETECT's shared, snapshotted watermark and fires every tenant's
	// timers, persistently. The exposure is the in-flight tail at the upgrade, or the
	// whole consumer backlog if detection was lagging; recovery is a snapshot reset.
	// Pre-GA an instance is recreated rather than upgraded in place, which is why this is
	// recorded rather than mitigated with a transitional bound.
	MaxEventFutureSkewSeconds int
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
	if c.MembershipCacheTtlSeconds == 0 {
		c.MembershipCacheTtlSeconds = DefaultMembershipCacheTtlSeconds
	}
	if c.MaxEventFutureSkewSeconds == 0 {
		c.MaxEventFutureSkewSeconds = DefaultMaxEventFutureSkewSeconds
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
	if c.MembershipCacheTtlSeconds <= 0 {
		return fmt.Errorf("membershipCacheTtlSeconds must be positive (got %d)", c.MembershipCacheTtlSeconds)
	}
	// 🔴 A NEGATIVE VALUE HERE USED TO START THE INSTANCE WITH THE SKEW BOUND OFF. eventtime
	// treats maxSkew <= 0 as "no ceiling" — a defensive read, since ApplyDefaults turns the
	// unset 0 into 300 — so a negative reached that branch and disabled the one place the
	// platform decides what instant a reading happened at. The cost is not a wrong chart: one
	// event dated years out pins a device's last-activity under the strictly-newer guard, its
	// inactivity sweep never fires again, and the device can never be seen to go offline. The
	// chart already tells operators not to do it; this is the half that refuses.
	//
	// Every sibling above rejects a non-positive value. This one is bounded BELOW at zero
	// rather than at one because 0 is the legitimate "unset" ApplyDefaults has already
	// replaced by the time Validate runs — so reaching Validate with 0 means a caller
	// constructed the config without defaulting it, which the sibling checks would catch
	// anyway. Only a value the operator wrote can be negative.
	if c.MaxEventFutureSkewSeconds < 0 {
		return fmt.Errorf("maxEventFutureSkewSeconds must not be negative (got %d): a negative "+
			"value disables the clock-skew bound, which lets one far-future timestamp freeze a "+
			"device's presence permanently", c.MaxEventFutureSkewSeconds)
	}
	return nil
}
