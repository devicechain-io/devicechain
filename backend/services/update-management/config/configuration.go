// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"github.com/devicechain-io/dc-microservice/config"
)

// UpdateManagementConfiguration is the typed configuration of the update-management
// service. Today it carries only the service's half of its relational datastore
// configuration; the artifact store and the download plane add their settings here as
// they land.
type UpdateManagementConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration
}

// NewUpdateManagementConfiguration creates the default update-management configuration.
func NewUpdateManagementConfiguration() *UpdateManagementConfiguration {
	cfg := &UpdateManagementConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults is the defaulting hook for this service (ADR-022 decision 1). It has no
// defaults to apply yet; it is the extension point future fields use.
func (c *UpdateManagementConfiguration) ApplyDefaults() {}

// Validate is the validation hook for this service (ADR-022 decision 1). It has no
// constraints to enforce yet; it is the extension point future fields use.
func (c *UpdateManagementConfiguration) Validate() error {
	return nil
}
