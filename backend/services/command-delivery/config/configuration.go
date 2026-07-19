// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"github.com/devicechain-io/dc-microservice/config"
)

const (
	// RedeliveryInterval is the cadence (in seconds) of the expiry + redelivery
	// sweep that times out stale commands and re-publishes still-queued ones.
	RedeliveryInterval = 30
)

// CommandDeliveryConfiguration is the microservice configuration. Commands are
// persisted to the relational store (ADR-012 #4).
type CommandDeliveryConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration
}

// NewCommandDeliveryConfiguration creates the default command delivery configuration.
func NewCommandDeliveryConfiguration() *CommandDeliveryConfiguration {
	cfg := &CommandDeliveryConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults is the ADR-022 decision-1 defaulting hook for this service. It
// has no defaults to apply today (SqlDebug is intentionally left at its zero
// value, SQL query logging off); it exists as the extension point future fields
// will use.
func (c *CommandDeliveryConfiguration) ApplyDefaults() {}

// Validate is the ADR-022 decision-1 validation hook for this service. It has no
// constraints to enforce today; it exists as the extension point future fields
// will use.
func (c *CommandDeliveryConfiguration) Validate() error {
	return nil
}
