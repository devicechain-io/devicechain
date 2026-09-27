// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"time"

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
	// DefaultProjectionMaxBatch is the most events one projection writer merges in one
	// transaction. 32 is event-management's persistence default too, and it is this
	// projection's measured best on a realistic fleet (see ProjectionConfiguration.MaxBatch).
	DefaultProjectionMaxBatch = 32
)

type DeviceStateConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration

	// Projection sizes the writers that merge resolved events into the live state.
	Projection ProjectionConfiguration
}

// ProjectionConfiguration sizes the writers that merge resolved events into the live state.
// Each writer takes the events already waiting for it, up to MaxBatch, and merges them in
// ONE transaction; an event is acknowledged only after that transaction commits. Every
// value has a default, and 0 means "use it" — the same settings, bounds and defaults as
// event-management's persistence writers.
type ProjectionConfiguration struct {
	// Writers is the number of writers merging events in parallel, each holding one pooled
	// connection while it merges. Unset (0) defaults to DefaultProjectionWriters. It must
	// be below the relational connection pool, which the GraphQL reads and the inactivity
	// monitor share.
	//
	// Batching, not more writers, is what carries capacity on a replicated database: every
	// commit waits for the standby, and a batch pays that wait once for many events. Merges
	// for one device wait for each other (each locks the device's row), so on a fleet whose
	// devices send in turn, more writers mean more batches waiting on each other's rows:
	// measured, 10 writers merged fewer events a second than 5 (numbers on MaxBatch). The
	// default stays at the count that was fixed before, because every writer holds a
	// connection from a pool the platform's connection budget sizes at 20.
	Writers int

	// MaxBatch is the most events one transaction merges, 1 to rdb.MaxWriterBatch. Unset (0)
	// defaults to DefaultProjectionMaxBatch; 1 turns batching off, so every event is merged
	// in transactions of its own.
	//
	// Measured in-process (BenchmarkProjectionWriters) against TimescaleDB with a synchronous
	// standby, events merged per second, on a fleet of 200 devices sending in turn (the
	// shape the load test drives): one event per transaction, about 85 with 5 writers before
	// batching existed; batched at 32, about 950 with 1 writer, 3500 with 5 and 2600 with 10.
	// At 5 writers, 16 reached about 2000 and 64 about 2700: past 32, concurrent batches
	// share more devices and wait on each other's rows. A fleet where every event is its own
	// device, which no batch ever waits on, did gain from 64 (about 4700 against 3750), which
	// is why the realistic fleet decides.
	MaxBatch int

	// LingerMillis is how long a writer holding a batch that is not full waits for more
	// events before merging it, 0 to rdb.MaxWriterLingerMillis. 0 (the default) takes only
	// what is already waiting, which adds no latency: under light load a writer finds one
	// event and merges it alone, and batches grow by themselves once events arrive faster
	// than single merges keep up with.
	LingerMillis int
}

// ApplyDefaults fills the writer count and the batch size when they are unset. It is the ONE
// definition of those defaults: the configuration load calls it, and so does the processor
// for a value built in code.
func (p *ProjectionConfiguration) ApplyDefaults() {
	if p.Writers == 0 {
		p.Writers = DefaultProjectionWriters
	}
	if p.MaxBatch == 0 {
		p.MaxBatch = DefaultProjectionMaxBatch
	}
}

// Validate bounds the writer count against the pool it draws from, and the batch size and
// linger: rdb.CheckWriterCount and rdb.CheckWriterBatch, the bounds every service whose
// writers batch shares.
func (p ProjectionConfiguration) Validate(pool config.MicroserviceDatastoreConfiguration) error {
	if err := rdb.CheckWriterCount("projection.writers", p.Writers, pool); err != nil {
		return err
	}
	return rdb.CheckWriterBatch("projection", p.MaxBatch, p.LingerMillis)
}

// Linger is LingerMillis as a duration.
func (p ProjectionConfiguration) Linger() time.Duration {
	return time.Duration(p.LingerMillis) * time.Millisecond
}

// Creates the default device state configuration
func NewDeviceStateConfiguration() *DeviceStateConfiguration {
	cfg := &DeviceStateConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults is the ADR-022 decision-1 defaulting hook for this service. It
// defaults the projection writer count and batch size (SqlDebug is intentionally left at its zero
// value, SQL query logging off).
func (c *DeviceStateConfiguration) ApplyDefaults() {
	c.Projection.ApplyDefaults()
}

// Validate is the ADR-022 decision-1 validation hook for this service. It bounds the
// projection writer count against the relational connection pool, and the batch settings.
func (c *DeviceStateConfiguration) Validate() error {
	return c.Projection.Validate(c.RdbConfiguration)
}
