// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"github.com/devicechain-io/dc-microservice/config"
)

const (
	KAFKA_TOPIC_FAILED_EVENTS   = "failed-events"
	KAFKA_TOPIC_RESOLVED_EVENTS = "resolved-events"
)

type DeviceManagementConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration
}

// Creates the default device management configuration
func NewDeviceManagementConfiguration() *DeviceManagementConfiguration {
	return &DeviceManagementConfiguration{
		RdbConfiguration: config.MicroserviceDatastoreConfiguration{
			SqlDebug: true,
		},
	}
}
