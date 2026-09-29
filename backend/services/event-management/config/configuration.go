// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// Default data-lifecycle policy values (ADR-026). Compression is on by default
// (it is the core reason ADR-004 chose TimescaleDB and is lossless); retention is
// off by default because it deletes telemetry.
const (
	DefaultChunkIntervalHours = 24
	DefaultCompressAfterDays  = 7
)

// Event persistence sizing. See PersistenceConfiguration.
const (
	// DefaultPersistenceWriters is the number of writers when none is configured.
	//
	// Measured on a three-node cloud cluster with a replicated event store (a synchronous
	// standby) and the other per-event services sized to keep up: at 5 writers committing
	// up to 32 events each, every batch was full from 4,400 events a second and each commit
	// took about 38 ms, so storing stopped near 5 x 32 / 38 ms, about 4,200 a second, while
	// the service itself was well inside its CPU limit. At 10 writers and batches of up to
	// 64, storing kept up to about 6,000 a second. There the service used about 1.7 cores
	// on average with its CPU limit raised to 4; it was not measured under its default limit
	// of 2. Past about 6,000, batches averaged 28 to 30 events, below the cap, while two of
	// the three nodes, one of them the event store's, were at 86 to 95% CPU; which of those
	// held the rate was not isolated.
	//
	// It is half the default connection pool, the most that rdb.CheckWriterCount accepts
	// without logging that reads compete with the writers. More is not better on its own:
	// every writer splits the same stream into smaller batches, and every commit costs the
	// event store CPU. In an earlier measurement, with a pool of 30, two replicas of 20
	// writers each cut batches to about 3 events, the event store's database used over 4
	// cores and its node saturated, and the whole pipeline slowed, storing less than 10
	// writers did.
	DefaultPersistenceWriters = 10
	// DefaultPersistenceMaxBatch is the most events one writer commits in one transaction.
	// It is rdb.MaxWriterBatch, the largest accepted.
	//
	// The cloud measurement above changed it together with the writer count, and batches
	// there averaged below 32, so it does not show 64 helping over 32. Measured in-process
	// (BenchmarkPersistBatch) with a synchronous standby, 64 bought little or nothing more
	// than 32 at 5 writers (about 1,400 events a second either way), for twice the longest
	// transaction. It is 64 so that a writer that finds a deep backlog can take more of it
	// in one commit. With lingerMillis at 0 a writer never waits for a batch to fill, so
	// at low rates it still commits the one event it finds.
	DefaultPersistenceMaxBatch = 64
)

type EventManagementConfiguration struct {
	TsdbConfiguration config.MicroserviceDatastoreConfiguration

	// AnchorSweepIntervalSeconds is how often the reconciliation sweep (ADR-044
	// decision 3) runs — the low-frequency backstop that drops event_anchors rows
	// whose referenced entity no longer resolves in device-management, catching any
	// entity-deletion event missed during an outage or a cache-window re-creation.
	// Unset (0) defaults to hourly; a negative value disables the sweep (the
	// entity.deleted consumer remains the primary path either way).
	AnchorSweepIntervalSeconds int

	// Lifecycle governs the TimescaleDB data-lifecycle policies applied to this
	// service's event hypertables (ADR-026): chunk sizing, columnar compression,
	// and retention. Policies are reconciled idempotently at startup.
	Lifecycle LifecycleConfiguration

	// Persistence sizes the event writers: how many run, and how many events each
	// commits in one transaction.
	Persistence PersistenceConfiguration
}

// PersistenceConfiguration sizes the writers that persist resolved events. Each writer
// takes the events already waiting for it, up to MaxBatch, and commits them in ONE
// transaction; an event is acknowledged only after that transaction commits. Every value
// has a default, and 0 means "use it".
type PersistenceConfiguration struct {
	// Writers is the number of writers running in parallel, each holding one pooled
	// connection while it writes. Unset (0) defaults to DefaultPersistenceWriters. It must
	// be below the event store's connection pool, which the GraphQL reads and the anchor
	// sweep share.
	Writers int

	// MaxBatch is the most events one transaction commits, 1 to rdb.MaxWriterBatch.
	// Unset (0) defaults to DefaultPersistenceMaxBatch; 1 turns batching off, so every
	// event gets a transaction of its own.
	MaxBatch int

	// LingerMillis is how long a writer holding a batch that is not full waits for more
	// events before committing it, 0 to rdb.MaxWriterLingerMillis. 0 (the default) takes
	// only what is already waiting, which adds no latency: under light load a writer finds
	// one event and commits it alone, and batches grow by themselves once events arrive
	// faster than single commits keep up with.
	LingerMillis int
}

// ApplyDefaults fills the writer count and the batch size when they are unset. It is the
// ONE definition of those defaults: the configuration load calls it, and so does the
// processor for a value built in code.
func (p *PersistenceConfiguration) ApplyDefaults() {
	if p.Writers == 0 {
		p.Writers = DefaultPersistenceWriters
	}
	if p.MaxBatch == 0 {
		p.MaxBatch = DefaultPersistenceMaxBatch
	}
}

// Validate refuses a value no reading makes sense of. pool is the event store's datastore
// configuration, whose connection pool the writers draw from. rdb.CheckWriterCount and
// rdb.CheckWriterBatch are the bounds, shared with every other service whose writers batch.
func (p PersistenceConfiguration) Validate(pool config.MicroserviceDatastoreConfiguration) error {
	if err := rdb.CheckWriterCount("persistence.writers", p.Writers, pool); err != nil {
		return err
	}
	return rdb.CheckWriterBatch("persistence", p.MaxBatch, p.LingerMillis)
}

// Linger is LingerMillis as a duration.
func (p PersistenceConfiguration) Linger() time.Duration {
	return time.Duration(p.LingerMillis) * time.Millisecond
}

// LifecycleConfiguration is the operator-facing surface for the TimescaleDB
// data-lifecycle policies (ADR-026). Values are service-global for now; per-tenant
// overrides await the per-tenant governance surface (ADR-023). The policies are
// applied uniformly to every event hypertable (events + the location/measurement/
// alert payload hypertables) and reconciled on every startup, so changing a value
// and restarting converges the live policy to it.
type LifecycleConfiguration struct {
	// ChunkIntervalHours sizes new hypertable chunks (set_chunk_time_interval). It
	// affects only chunks created after startup; existing chunks keep their interval.
	//
	// Unset (0) defaults to 24h, and writing `chunkIntervalHours: 0` in the document
	// means exactly the same thing as omitting the key — "use the default", not
	// "no chunking". There is no pointer here to tell the two apart, and none is
	// wanted: unlike CompressAfterDays and LocationRetentionDays, a chunk interval
	// has no meaningful "off" state for an explicit 0 to select, so the two cases
	// have the same correct answer. Only a NEGATIVE value is a mistake, and Validate
	// is where it is refused.
	ChunkIntervalHours int

	// CompressAfterDays enables lossless columnar compression on chunks older than
	// this many days (add_compression_policy). Compression is the core reason ADR-004
	// chose TimescaleDB, so it is on by default. Unset (nil) defaults to 7 days; an
	// explicit 0 disables the compression policy (existing compressed chunks are left
	// as-is — turning compression fully off is a manual operation).
	CompressAfterDays *int

	// RetentionDays drops chunks whose data is older than this many days
	// (add_retention_policy). Retention DELETES telemetry, so it is OPT-IN: unset or
	// 0 keeps data forever, and no default can silently start dropping data.
	RetentionDays int

	// LocationRetentionDays overrides RetentionDays for the location_events
	// hypertable alone. Unset (nil) inherits RetentionDays, so an operator who does
	// not set it sees no change at all.
	//
	// It exists for a regulatory reason, not a technical one: a vehicle or worker
	// track is personal data in a way a temperature series is not, and a tenant whose
	// lawful basis for holding position is shorter than its basis for holding
	// telemetry otherwise has only "delete everything or nothing" to offer an
	// auditor. An explicit 0 is a real setting distinct from unset — it disables
	// retention for location while leaving it on everywhere else.
	//
	// Note this is a WINDOW, not an erasure API: per-device/per-subject deletion is
	// deliberately out of scope (see model.DataLifecyclePolicy for why, and for the
	// tenant-level deletion that is already covered by construction).
	LocationRetentionDays *int

	// DisableRollupReads is a kill-switch for the continuous-aggregate read path
	// (ADR-026): when false (default) bucketed measurement reads whose interval is a
	// whole multiple of the rollup's base bucket are served from the pre-aggregated
	// measurement_rollups continuous aggregate; when true, every bucketed read scans
	// the raw measurement_events hypertable (the pre-rollup behavior). It exists so an
	// operator can fall back instantly if the rollup ever misbehaves, without a deploy.
	DisableRollupReads bool
}

// Creates the default event management configuration
func NewEventManagementConfiguration() *EventManagementConfiguration {
	cfg := &EventManagementConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults is the ADR-022 decision-1 defaulting hook for this service. It
// defaults the reconciliation-sweep interval to hourly when unset (a value of -1
// can be used to disable it explicitly without leaving the field at its zero value),
// fills the data-lifecycle policy defaults (ADR-026): 24h chunks, compression
// after 7 days, retention off, and fills the persistence writer defaults.
func (c *EventManagementConfiguration) ApplyDefaults() {
	if c.AnchorSweepIntervalSeconds == 0 {
		c.AnchorSweepIntervalSeconds = 3600
	}
	if c.Lifecycle.ChunkIntervalHours == 0 {
		c.Lifecycle.ChunkIntervalHours = DefaultChunkIntervalHours
	}
	// A nil pointer means "unset" → default on; an explicit 0 means "disabled" and is
	// left as-is. RetentionDays needs no defaulting: its zero value is the intended
	// off state.
	if c.Lifecycle.CompressAfterDays == nil {
		d := DefaultCompressAfterDays
		c.Lifecycle.CompressAfterDays = &d
	}
	c.Persistence.ApplyDefaults()
}

// Validate is the ADR-022 decision-1 validation hook for this service. It rejects
// nonsensical data-lifecycle values so a typo cannot produce a broken or
// data-destroying policy (fail closed).
//
// 🔴 IT RUNS AFTER ApplyDefaults, WHICH DECIDES WHAT EACH CHECK BELOW CAN ACTUALLY
// SEE. core.LoadConfiguration defaults first and validates second — deliberately, so
// that defaults are authoritative regardless of which keys the document supplied — so
// any field ApplyDefaults fills has already had its zero value replaced by the time a
// check here looks at it. Read every bound below as being about the DEFAULTED value,
// not the document's.
//
// The two are not the same thing, and reading them as the same thing is how a check
// gets written that cannot fire. ChunkIntervalHours is the case in point: `<= 0` looks
// like it refuses a zero, and it can never see one, because 0 was already replaced
// with DefaultChunkIntervalHours. That is the intended behaviour rather than a hole —
// 0 means "use the default" for this field (see LifecycleConfiguration) — so the check
// is kept as the floor it really is: it refuses a NEGATIVE interval, which is the typo
// there is no sensible reading of. It also still covers a caller that builds the
// struct itself and validates without defaulting, which is why it is `<= 0` and not
// `< 0`.
func (c *EventManagementConfiguration) Validate() error {
	if c.Lifecycle.ChunkIntervalHours <= 0 {
		return fmt.Errorf("lifecycle.chunkIntervalHours must be positive, got %d", c.Lifecycle.ChunkIntervalHours)
	}
	if c.Lifecycle.CompressAfterDays != nil && *c.Lifecycle.CompressAfterDays < 0 {
		return fmt.Errorf("lifecycle.compressAfterDays cannot be negative, got %d", *c.Lifecycle.CompressAfterDays)
	}
	if c.Lifecycle.RetentionDays < 0 {
		return fmt.Errorf("lifecycle.retentionDays cannot be negative, got %d", c.Lifecycle.RetentionDays)
	}
	// nil is the "inherit RetentionDays" case and is always valid; only a supplied
	// value is range-checked, and a negative one is the same typo class as above.
	if c.Lifecycle.LocationRetentionDays != nil && *c.Lifecycle.LocationRetentionDays < 0 {
		return fmt.Errorf("lifecycle.locationRetentionDays cannot be negative, got %d",
			*c.Lifecycle.LocationRetentionDays)
	}
	return c.Persistence.Validate(c.TsdbConfiguration)
}
