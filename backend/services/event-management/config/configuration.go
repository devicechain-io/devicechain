// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"github.com/devicechain-io/dc-microservice/config"
)

const (
	KAFKA_TOPIC_PERSISTED_EVENTS = "persisted-events"
)

type EventManagementConfiguration struct {
	TsdbConfiguration config.MicroserviceDatastoreConfiguration
}

// Creates the default device management configuration
func NewEventManagementConfiguration() *EventManagementConfiguration {
	return &EventManagementConfiguration{
		TsdbConfiguration: config.MicroserviceDatastoreConfiguration{
			SqlDebug: true,
		},
	}
}
