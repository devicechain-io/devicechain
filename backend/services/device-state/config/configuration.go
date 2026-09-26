// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/rdb"
)

const (
	// DefaultInactivityTimeout is the per-device inactivity window in seconds
	// before a device is marked inactive.
	DefaultInactivityTimeout = 600
	// InactivityRecheckInterval is how often the background monitor re-evaluates
	// device activity.
	InactivityRecheckInterval = 60 // seconds

	// DefaultProjectionWriters is the number of projection writers when none is
	// configured: the count that was fixed in code before it was configurable.
	DefaultProjectionWriters = 5
)

type DeviceStateConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration

	// Projection sizes the writers that merge resolved events into the live state.
	Projection ProjectionConfiguration
}

// ProjectionConfiguration sizes the state projection's writers.
type ProjectionConfiguration struct {
	// Writers is the number of writers merging events in parallel, each holding one pooled
	// connection while it merges. Unset (0) defaults to DefaultProjectionWriters. It must
	// be below the relational connection pool, which the GraphQL reads and the inactivity
	// monitor share.
	//
	// The projection does not batch, so this is its only capacity lever. Measured in-process
	// (BenchmarkProjectionWriters) against TimescaleDB, merges per second at 1, 5 and 10
	// writers were about 60, 280 and 470 with local WAL flush, and about 43, 95 and 168 with
	// a synchronous standby — with events from only 8 devices, a little less. Each merge
	// locks its device's row, so writers merging the same device wait for each other. The
	// default stays at the count that was fixed before, because every writer holds a
	// connection from a pool the platform's connection budget sizes at 20.
	Writers int
}

// ApplyDefaults fills the writer count when it is unset. It is the ONE definition of that
// default: the configuration load calls it, and so does the processor for a value built in
// code.
func (p *ProjectionConfiguration) ApplyDefaults() {
	if p.Writers == 0 {
		p.Writers = DefaultProjectionWriters
	}
}

// Validate bounds the writer count against the pool it draws from (rdb.CheckWriterCount,
// the bound every service that sizes its writers shares).
func (p ProjectionConfiguration) Validate(pool config.MicroserviceDatastoreConfiguration) error {
	return rdb.CheckWriterCount("projection.writers", p.Writers, pool)
}

// Creates the default device state configuration
func NewDeviceStateConfiguration() *DeviceStateConfiguration {
	cfg := &DeviceStateConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults is the ADR-022 decision-1 defaulting hook for this service. It
// defaults the projection writer count (SqlDebug is intentionally left at its zero
// value, SQL query logging off).
func (c *DeviceStateConfiguration) ApplyDefaults() {
	c.Projection.ApplyDefaults()
}

// Validate is the ADR-022 decision-1 validation hook for this service. It bounds the
// projection writer count against the relational connection pool.
func (c *DeviceStateConfiguration) Validate() error {
	return c.Projection.Validate(c.RdbConfiguration)
}
