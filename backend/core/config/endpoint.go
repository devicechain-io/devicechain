// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import "fmt"

// ServiceEndpoint is a derived coordinate, never a decoded instance field.
type ServiceEndpoint struct {
	Hostname string
	Port     uint32
	Key      string
}

func (e ServiceEndpoint) Configured() bool { return e.Hostname != "" && e.Port != 0 }

// GraphQLURL preserves the peer metric label bytes, including bare IPv6 hosts.
func (e ServiceEndpoint) GraphQLURL() string {
	return fmt.Sprintf("http://%s:%d/graphql", e.Hostname, e.Port)
}

func (c UserManagementConfiguration) Endpoint() ServiceEndpoint {
	return ServiceEndpoint{c.Hostname, c.Port, "infrastructure.userManagement"}
}

func (c DeviceManagementConfiguration) Endpoint() ServiceEndpoint {
	return ServiceEndpoint{c.Hostname, c.Port, "infrastructure.deviceManagement"}
}

func (c EventProcessingConfiguration) Endpoint() ServiceEndpoint {
	return ServiceEndpoint{c.Hostname, c.Port, "infrastructure.eventProcessing"}
}

func (c CommandDeliveryConfiguration) Endpoint() ServiceEndpoint {
	return ServiceEndpoint{c.Hostname, c.Port, "infrastructure.commandDelivery"}
}

func (c DeviceStateConfiguration) Endpoint() ServiceEndpoint {
	return ServiceEndpoint{c.Hostname, c.Port, "infrastructure.deviceState"}
}

func (c AiInferenceConfiguration) Endpoint() ServiceEndpoint {
	return ServiceEndpoint{c.Hostname, c.Port, "infrastructure.aiInference"}
}
