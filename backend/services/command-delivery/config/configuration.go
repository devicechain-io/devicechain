// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
)

const (
	// RedeliveryInterval is the cadence (in seconds) of the expiry + redelivery
	// sweep that times out stale commands and re-publishes still-queued ones.
	RedeliveryInterval = 30

	// DefaultCommandTTLSeconds is the fallback command time-to-live (168h = 7 days)
	// stamped onto a command whose creator supplies no explicit expiresAt. It is
	// deliberately aligned with the device-commands stream MaxAge (core/messaging
	// streamMaxAge = 7d): the stream already ages a command's *message* out at 7d, so
	// a command row that outlives that is undeliverable zombie state, never a promise
	// the platform can keep. Stamping a default closes the "stuck in SENT forever" gap
	// — a TTL-less command (every REACT send-command, ADR-051) previously never reached
	// a terminal state because ExpireStale only touches rows with a non-null expires_at.
	// It is also what makes the LwM2M queue-mode hold (ADR-075 L4b) bounded: a held
	// command a device never wakes to receive resolves to TIMEOUT at this horizon.
	DefaultCommandTTLSeconds = 168 * 60 * 60

	// MinCommandTTLSeconds floors the configured default so a fat-fingered tiny value
	// cannot expire commands out from under a device before it can answer.
	MinCommandTTLSeconds = 60
)

// CommandDeliveryConfiguration is the microservice configuration. Commands are
// persisted to the relational store (ADR-012 #4).
type CommandDeliveryConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration

	// DefaultCommandTTLSeconds is the TTL stamped on a command whose creator omits an
	// explicit expiresAt (a caller-supplied value always wins). Fail-safe: a zero or
	// negative value is replaced by the platform default in ApplyDefaults, so the field
	// can never disable expiry (which would resurrect the stuck-in-SENT-forever gap).
	DefaultCommandTTLSeconds int
}

// NewCommandDeliveryConfiguration creates the default command delivery configuration.
func NewCommandDeliveryConfiguration() *CommandDeliveryConfiguration {
	cfg := &CommandDeliveryConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults is the ADR-022 decision-1 defaulting hook for this service. It
// floors the command TTL to the platform default when unset or non-positive — the
// fail-safe that keeps a missing/zero value from disabling expiry entirely.
func (c *CommandDeliveryConfiguration) ApplyDefaults() {
	if c.DefaultCommandTTLSeconds <= 0 {
		c.DefaultCommandTTLSeconds = DefaultCommandTTLSeconds
	}
}

// Validate is the ADR-022 decision-1 validation hook for this service. It rejects a
// command TTL below the floor: a sub-minute default would expire commands before a
// device on a marginal radio could ever answer — a check that cannot fail silently
// (the whole point of the default is that every command reaches a terminal state).
func (c *CommandDeliveryConfiguration) Validate() error {
	if c.DefaultCommandTTLSeconds < MinCommandTTLSeconds {
		return fmt.Errorf("defaultCommandTtlSeconds must be at least %d (got %d)",
			MinCommandTTLSeconds, c.DefaultCommandTTLSeconds)
	}
	return nil
}
