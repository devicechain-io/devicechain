// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"github.com/devicechain-io/dc-microservice/config"
)

const (
	// SUBJECT_DEVICE_COMMANDS is the outbound subject suffix on which persisted
	// commands are delivered to devices.
	SUBJECT_DEVICE_COMMANDS = "device-commands"
	// SUBJECT_COMMAND_RESPONSES is the inbound subject suffix on which devices
	// respond to commands.
	SUBJECT_COMMAND_RESPONSES = "command-responses"

	// RedeliveryInterval is the cadence (in seconds) of the expiry + redelivery
	// sweep that times out stale commands and re-publishes still-queued ones.
	RedeliveryInterval = 30
)

// CommandDeliveryConfiguration is the microservice configuration. Commands are
// persisted to the relational store (ADR-012 #4 / ThingsBoard §2.6).
type CommandDeliveryConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration
}

// NewCommandDeliveryConfiguration creates the default command delivery configuration.
func NewCommandDeliveryConfiguration() *CommandDeliveryConfiguration {
	return &CommandDeliveryConfiguration{
		RdbConfiguration: config.MicroserviceDatastoreConfiguration{
			SqlDebug: true,
		},
	}
}
