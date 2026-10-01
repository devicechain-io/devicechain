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

	// DefaultProjectionWriters is the number of projection writers when none is configured:
	// half the default connection pool of 20, the most rdb.CheckWriterCount accepts without
	// logging that reads compete with the writers. See ProjectionConfiguration.Writers.
	DefaultProjectionWriters = 10
	// DefaultProjectionMaxBatch is the most events one projection writer merges in one
	// transaction: this projection's measured best on a small fleet sending in turn, and a
	// cap batches did not reach on a cloud cluster (see ProjectionConfiguration.MaxBatch).
	// event-management's persistence default is larger.
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
// value has a default, and 0 means "use it" — the same settings and bounds as
// event-management's persistence writers, with defaults of its own.
type ProjectionConfiguration struct {
	// Writers is the number of writers merging events in parallel, each holding one pooled
	// connection while it merges. Unset (0) defaults to DefaultProjectionWriters. It must
	// be below the relational connection pool, which the GraphQL reads and the inactivity
	// monitor share.
	//
	// Batching is what carries capacity on a replicated database: every commit waits for
	// the standby, and a batch pays that wait once for many events. Merges for one device
	// wait for each other (each locks the device's row), so on a small fleet whose devices
	// send in turn more writers can mean more batches waiting on each other's rows: measured
	// in-process on 200 devices, 10 writers merged fewer events a second than 5 (numbers on
	// MaxBatch).
	//
	// On a three-node cloud cluster with a replicated relational store and about 1,700 to
	// 1,900 devices each sending every 250 ms, 5 writers kept 95.7% of an offered 6,800
	// events a second over three minutes and fell further behind above it, with batches
	// averaging 17 to 19. 10 writers, with the service's CPU request raised to its use and
	// MaxBatch raised to 64 in the same run, kept pace at 7,600 with batches averaging about
	// 15. The three changes were shown only together; the chart now ships the request, and
	// MaxBatch stays at its default of 32 (see MaxBatch). In those runs the relational
	// primary used about 12% more CPU per merged event, and event-management, sharing a node
	// with this service, stored fewer events than with the earlier defaults (6,280 against
	// 6,796 a second at 6,800 offered, 5,252 against 7,463 at 7,600, 5,624 over a five-minute
	// hold at 6,800); none of it was separated from the other changes made at the same time,
	// and the runs followed a delete of about 10 million events, so inserts refilled freed
	// space and wrote more WAL than usual. Every writer holds a connection from a pool the
	// platform's connection budget sizes at 20, which is why the default is half of it.
	Writers int

	// MaxBatch is the most events one transaction merges, 1 to writerbatch.MaxSize. Unset (0)
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
	// is why the realistic fleet decides. On the cloud cluster described on Writers, batches
	// averaged 15 to 19 (with the cap at 32, and at 64 in the 10-writer run), so 32 did not
	// bind on average; an average does not show that it never did.
	MaxBatch int

	// LingerMillis is how long a writer holding a batch that is not full waits for more
	// events before merging it, 0 to writerbatch.MaxLingerMillis. 0 (the default) takes only
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
