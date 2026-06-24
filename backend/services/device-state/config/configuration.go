// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"github.com/devicechain-io/dc-microservice/config"
)

const (
	// DefaultInactivityTimeout is the per-device inactivity window in seconds
	// before a device is marked inactive (ThingsBoard default).
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
	return &DeviceStateConfiguration{
		RdbConfiguration: config.MicroserviceDatastoreConfiguration{
			SqlDebug: true,
		},
	}
}
