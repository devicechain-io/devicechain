// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/governance"
)

const (
	// RedeliveryInterval is the cadence (in seconds) of the expiry + redelivery
	// sweep that times out stale commands and re-publishes still-queued ones.
	RedeliveryInterval = 30

	// HoldReconcileInterval is the cadence (in seconds) of the pass that releases
	// WITHHELD commands whose devices have come back.
	//
	// 🔑 IT IS SLOW ON PURPOSE, AND IT IS NOT A LATENCY KNOB. The two passes ask
	// different questions: the sweep asks "is there new work?", where latency IS the
	// product, while this asks "has the world changed under a decision made earlier?",
	// where the answer is almost always no and asking cheaply beats asking quickly.
	// Turning this down to chase release latency would put the whole accumulated
	// withheld set — which is what the gate exists to let accumulate — back under a
	// near-continuous scan, which is the cost the gate was built to remove.
	//
	// The right fix for release latency is not a shorter interval but a wake: the
	// transports know the instant a device returns, and this is the net under it.
	HoldReconcileInterval = 120

	// DefaultCommandTTLSeconds is the fallback command time-to-live (168h = 7 days)
	// stamped onto a command whose creator supplies no explicit expiresAt. It is
	// deliberately aligned with the device-commands stream MaxAge (core/messaging
	// streamMaxAge = 7d): the stream already ages a command's *message* out at 7d, so
	// a command row that outlives that is undeliverable zombie state, never a promise
	// the platform can keep. Stamping a default closes the "stuck in SENT forever" gap
	// — a TTL-less command (every REACT send-command, ADR-051) previously never reached
	// a terminal state because ExpireStale only touches rows with a non-null expires_at.
	// It is also what makes the LwM2M queue-mode hold (ADR-075 L4b) bounded: a held
	// command a device never wakes to receive resolves to EXPIRED at this horizon —
	// EXPIRED rather than TIMEOUT because it was never dispatched, which is the
	// distinction HELD exists to preserve (see model.CommandStatus).
	DefaultCommandTTLSeconds = 168 * 60 * 60

	// MinCommandTTLSeconds floors the configured default so a fat-fingered tiny value
	// cannot expire commands out from under a device before it can answer.
	MinCommandTTLSeconds = 60

	// DefaultHeldCommandCeiling is the platform default bound on how many WITHHELD
	// (HELD) commands one tenant may accumulate. HELD is where an offline fleet's
	// backlog collects and it can sit for days, so it is the one lifecycle state that
	// grows without a natural brake: the delivery sweep reads every dispatchable row
	// into the pod on each tick, so an unbounded hold is both a per-tenant storage
	// problem and an instance-wide memory one.
	//
	// 🔴 A MISSING OR ZERO VALUE MEANS THIS DEFAULT, NEVER UNLIMITED — in the service
	// config (ApplyDefaults floors it), in a per-tenant override, and in a tier. A
	// governance bound whose absent value reads as "no bound" stops governing exactly
	// when the thing that carries it is unreachable.
	//
	// It is DERIVED from core's last-resort floor rather than restated, because the
	// two are the same number by intent: the resolver falls back to core's value when
	// this service supplies a non-positive default, so a lone edit here would leave a
	// tenant's ceiling depending on which of the two paths answered.
	DefaultHeldCommandCeiling = governance.DefaultHeldCommandCeiling

	// MinHeldCommandCeiling floors the configured ceiling. A handful of held commands
	// per tenant is not a bound, it is an outage for any fleet that sleeps: a device
	// waking to find its backlog was refused at enqueue never receives the commands
	// at all, and the refusal is invisible from the device's side.
	MinHeldCommandCeiling = 100
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

	// HeldCommandCeiling is this instance's fallback bound on withheld (HELD) commands
	// per tenant, used when no per-tenant or tier ceiling has been resolved. Fail-safe
	// in the same shape as the TTL above: a zero or negative value is replaced by the
	// platform default in ApplyDefaults, so the field can never mean "unlimited".
	HeldCommandCeiling int
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
	// A missing or zero ceiling is the platform default, NEVER unlimited — the same
	// fail-safe direction as the TTL: the harmless-looking value (absent, zero) must
	// land on the bound, not remove it.
	if c.HeldCommandCeiling <= 0 {
		c.HeldCommandCeiling = DefaultHeldCommandCeiling
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
	// Same shape for the held-command ceiling: a value below the floor is refused
	// rather than quietly accepted, because a tiny ceiling is not a conservative
	// setting — it refuses a sleeping fleet's backlog at enqueue, and the device
	// never learns the commands existed.
	if c.HeldCommandCeiling < MinHeldCommandCeiling {
		return fmt.Errorf("heldCommandCeiling must be at least %d (got %d)",
			MinHeldCommandCeiling, c.HeldCommandCeiling)
	}
	return nil
}
