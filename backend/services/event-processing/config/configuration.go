// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

// EventProcessingConfiguration is the typed configuration for the event-processing
// service (ADR-051): the DETECT + REACT pipeline extracted from device-management.
// It is loaded fail-closed (unknown keys rejected) via core.LoadConfiguration.
//
// The service has no persistent store yet — the Postgres snapshot store (ADR-051)
// arrives in a later slice, at which point a datastore-configuration field lands
// here. For now the struct is empty but carries the ADR-022 defaulting/validation
// hooks so future fields have a home.
type EventProcessingConfiguration struct {
}

// NewEventProcessingConfiguration creates the default configuration.
func NewEventProcessingConfiguration() *EventProcessingConfiguration {
	cfg := &EventProcessingConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults is the ADR-022 decision-1 defaulting hook. It has no defaults to
// apply today; it exists as the extension point future fields will use.
func (c *EventProcessingConfiguration) ApplyDefaults() {}

// Validate is the ADR-022 decision-1 validation hook. It has no constraints to
// enforce today; it exists as the extension point future fields will use.
func (c *EventProcessingConfiguration) Validate() error {
	return nil
}
