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
	return nil
}
