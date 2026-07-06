// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"github.com/devicechain-io/dc-microservice/config"
)

type EventManagementConfiguration struct {
	TsdbConfiguration config.MicroserviceDatastoreConfiguration

	// AnchorSweepIntervalSeconds is how often the reconciliation sweep (ADR-044
	// decision 3) runs — the low-frequency backstop that drops event_anchors rows
	// whose referenced entity no longer resolves in device-management, catching any
	// entity-deletion event missed during an outage or a cache-window re-creation.
	// Unset (0) defaults to hourly; a negative value disables the sweep (the
	// entity.deleted consumer remains the primary path either way).
	AnchorSweepIntervalSeconds int
}

// Creates the default event management configuration
func NewEventManagementConfiguration() *EventManagementConfiguration {
	cfg := &EventManagementConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults is the ADR-022 decision-1 defaulting hook for this service. It
// defaults the reconciliation-sweep interval to hourly when unset (a value of -1
// can be used to disable it explicitly without leaving the field at its zero value).
func (c *EventManagementConfiguration) ApplyDefaults() {
	if c.AnchorSweepIntervalSeconds == 0 {
		c.AnchorSweepIntervalSeconds = 3600
	}
}

// Validate is the ADR-022 decision-1 validation hook for this service. It has no
// constraints to enforce today; it exists as the extension point future fields
// will use.
func (c *EventManagementConfiguration) Validate() error {
	return nil
}
