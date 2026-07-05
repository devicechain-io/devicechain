// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import "github.com/devicechain-io/dc-microservice/config"

// NotificationManagementConfiguration is the typed config for the notification
// service (ADR-017): the alarm→human last mile. The durable consumer over the
// alarm-events envelope (ADR-041) drives delivery; the persisted per-tenant
// notification configuration (channels + their write-only secrets, routing
// policies) is served from RDB. Dispatch tunables (retry/throttle defaults) and
// the escalation scheduler land in later slices; this struct is the ADR-022
// decision-1 typed-config surface (defaults + fail-closed validation) they hang
// off, so the wiring never has to change shape.
type NotificationManagementConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration
}

// NewNotificationManagementConfiguration returns a configuration with defaults
// applied.
func NewNotificationManagementConfiguration() *NotificationManagementConfiguration {
	cfg := &NotificationManagementConfiguration{
		RdbConfiguration: config.MicroserviceDatastoreConfiguration{
			SqlDebug: true,
		},
	}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults is the ADR-022 decision-1 defaulting hook. It has no defaults to
// apply today; it exists as the extension point the dispatch tunables will use.
func (c *NotificationManagementConfiguration) ApplyDefaults() {}

// Validate is the ADR-022 decision-1 validation hook. It has no constraints to
// enforce today; it exists as the extension point the dispatch tunables will use.
func (c *NotificationManagementConfiguration) Validate() error {
	return nil
}
