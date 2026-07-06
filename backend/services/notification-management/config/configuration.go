// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
)

// NotificationManagementConfiguration is the typed config for the notification
// service (ADR-017): the alarm→human last mile. The durable consumer over the
// alarm-events envelope (ADR-041) drives delivery; the persisted per-tenant
// notification configuration (channels + their write-only secrets, routing
// policies) is served from RDB. This struct is the ADR-022 decision-1 typed-config
// surface (defaults + fail-closed validation) the dispatcher (N.C) and the
// escalation scheduler (N.D) hang their tunables off, so the wiring never changes
// shape.
type NotificationManagementConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration

	// DeliverySeconds bounds a single channel delivery attempt (one SMTP send or
	// webhook POST): the adapter runs on a background context (drain-on-shutdown), so
	// a hung endpoint must not stall graceful shutdown. Unset (0) defaults to 10s. The
	// worst-case whole-dispatch time (DeliveryAttempts × DeliverySeconds + backoff) is
	// kept comfortably under the consumer's 60s AckWait so a slow-but-progressing
	// dispatch never trips a redelivery of the message it is still working.
	DeliverySeconds int
	// DeliveryAttempts is how many times the dispatcher tries a single channel before
	// giving up on it (the adapter owns its own retry, ADR-017 Notifier contract).
	// Unset (0) defaults to 3; a value of 1 disables in-dispatch retry.
	DeliveryAttempts int
	// StateRetentionSeconds is how long a cleared per-alarm NotificationState row is
	// kept before the retention sweep prunes it. Escalation is settled once an alarm
	// clears, so the row is only kept for a grace window; pruning stops the per-alarm
	// state from asymptotically becoming an alarm index in the wrong service (the
	// ADR-041 history-home concern). Unset (0) defaults to 7d.
	StateRetentionSeconds int
	// RetentionSweepSeconds is how often the retention sweep runs. Unset (0) defaults
	// to hourly; a negative value disables the sweep.
	RetentionSweepSeconds int
}

// NewNotificationManagementConfiguration returns a configuration with defaults
// applied. SqlDebug is intentionally left at its zero value (SQL query logging
// OFF): GORM's debug logger interpolates bound parameters into the traced
// statement, which would echo a channel's reversible write-only delivery secret
// (SMTP password / bearer token) into the pod log on every create/rotate —
// defeating the resolver-layer write-only guarantee. Enable it per-environment via
// instance config only for local debugging.
func NewNotificationManagementConfiguration() *NotificationManagementConfiguration {
	cfg := &NotificationManagementConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// Default dispatch tunables, applied when a field is left at its zero value.
const (
	defaultDeliverySeconds       = 10
	defaultDeliveryAttempts      = 3
	defaultStateRetentionSeconds = 7 * 24 * 60 * 60 // 7 days
	defaultRetentionSweepSeconds = 60 * 60          // hourly
)

// ApplyDefaults is the ADR-022 decision-1 defaulting hook. It defaults the dispatch
// tunables so a zero-value config (the production load path) yields safe, bounded
// delivery behavior.
func (c *NotificationManagementConfiguration) ApplyDefaults() {
	if c.DeliverySeconds == 0 {
		c.DeliverySeconds = defaultDeliverySeconds
	}
	if c.DeliveryAttempts == 0 {
		c.DeliveryAttempts = defaultDeliveryAttempts
	}
	if c.StateRetentionSeconds == 0 {
		c.StateRetentionSeconds = defaultStateRetentionSeconds
	}
	if c.RetentionSweepSeconds == 0 {
		c.RetentionSweepSeconds = defaultRetentionSweepSeconds
	}
}

// Validate is the ADR-022 decision-1 validation hook. It fails closed on a tunable
// that would make delivery unsafe (a non-positive timeout would let a hung endpoint
// stall shutdown; fewer than one attempt would never deliver).
func (c *NotificationManagementConfiguration) Validate() error {
	if c.DeliverySeconds < 0 {
		return fmt.Errorf("DeliverySeconds must not be negative, got %d", c.DeliverySeconds)
	}
	if c.DeliveryAttempts < 1 {
		return fmt.Errorf("DeliveryAttempts must be at least 1, got %d", c.DeliveryAttempts)
	}
	if c.StateRetentionSeconds < 0 {
		return fmt.Errorf("StateRetentionSeconds must not be negative, got %d", c.StateRetentionSeconds)
	}
	return nil
}

// DeliveryTimeout returns the per-attempt delivery timeout as a duration.
func (c *NotificationManagementConfiguration) DeliveryTimeout() time.Duration {
	return time.Duration(c.DeliverySeconds) * time.Second
}

// StateRetention returns the cleared-state retention window as a duration.
func (c *NotificationManagementConfiguration) StateRetention() time.Duration {
	return time.Duration(c.StateRetentionSeconds) * time.Second
}

// RetentionSweepInterval returns the retention-sweep tick period as a duration.
func (c *NotificationManagementConfiguration) RetentionSweepInterval() time.Duration {
	return time.Duration(c.RetentionSweepSeconds) * time.Second
}
