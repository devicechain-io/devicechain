// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

// NotificationManagementConfiguration is the typed config for the notification
// service (ADR-017): the alarm→human last mile. This first slice (the durable
// consumer over the alarm-events envelope, ADR-041) has no persistence and no
// tunables yet — the per-tenant notification policy, channel credentials, and
// dispatch settings land in later slices. The struct exists now as the ADR-022
// decision-1 typed-config surface (defaults + fail-closed validation) that those
// fields will hang off, so the wiring never has to change shape.
type NotificationManagementConfiguration struct {
}

// NewNotificationManagementConfiguration returns a configuration with defaults
// applied.
func NewNotificationManagementConfiguration() *NotificationManagementConfiguration {
	cfg := &NotificationManagementConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults is the ADR-022 decision-1 defaulting hook. It has no defaults to
// apply today; it exists as the extension point the policy/channel fields will use.
func (c *NotificationManagementConfiguration) ApplyDefaults() {}

// Validate is the ADR-022 decision-1 validation hook. It has no constraints to
// enforce today; it exists as the extension point the policy/channel fields will use.
func (c *NotificationManagementConfiguration) Validate() error {
	return nil
}
