// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"github.com/devicechain-io/dc-microservice/config"
)

const (
	// DefaultInactivityTimeout is the per-device inactivity window in seconds
	// before a device is marked inactive.
	DefaultInactivityTimeout = 600
	// InactivityRecheckInterval is how often the background monitor re-evaluates
	// device activity.
	InactivityRecheckInterval = 60 // seconds
)

type DeviceStateConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration
}

// Creates the default device state configuration
func NewDeviceStateConfiguration() *DeviceStateConfiguration {
	cfg := &DeviceStateConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults is the ADR-022 decision-1 defaulting hook for this service. It
// has no defaults to apply today (SqlDebug is intentionally left at its zero
// value, SQL query logging off); it exists as the extension point future fields
// will use.
func (c *DeviceStateConfiguration) ApplyDefaults() {}

// Validate is the ADR-022 decision-1 validation hook for this service. It has no
// constraints to enforce today; it exists as the extension point future fields
// will use.
func (c *DeviceStateConfiguration) Validate() error {
	return nil
}
