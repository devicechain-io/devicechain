// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
)

// Default data-lifecycle policy values (ADR-026). Compression is on by default
// (it is the core reason ADR-004 chose TimescaleDB and is lossless); retention is
// off by default because it deletes telemetry.
const (
	DefaultChunkIntervalHours = 24
	DefaultCompressAfterDays  = 7
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
	// Unset (0) defaults to 24h.
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
// and fills the data-lifecycle policy defaults (ADR-026): 24h chunks, compression
// after 7 days, retention off.
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
}

// Validate is the ADR-022 decision-1 validation hook for this service. It rejects
// nonsensical data-lifecycle values so a typo cannot produce a broken or
// data-destroying policy (fail closed).
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
	return nil
}
