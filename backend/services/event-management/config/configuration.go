// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"github.com/devicechain-io/dc-microservice/config"
)

type EventManagementConfiguration struct {
	TsdbConfiguration config.MicroserviceDatastoreConfiguration
}

// Creates the default event management configuration
func NewEventManagementConfiguration() *EventManagementConfiguration {
	cfg := &EventManagementConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults is the ADR-022 decision-1 defaulting hook for this service. It
// has no defaults to apply today (SqlDebug is intentionally left at its zero
// value, SQL query logging off); it exists as the extension point future fields
// will use.
func (c *EventManagementConfiguration) ApplyDefaults() {}

// Validate is the ADR-022 decision-1 validation hook for this service. It has no
// constraints to enforce today; it exists as the extension point future fields
// will use.
func (c *EventManagementConfiguration) Validate() error {
	return nil
}
