// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
)

// Default checkpoint cadence (ADR-051). The DETECT engine holds window/timer state
// in memory and commits it to the Postgres snapshot store periodically; a message
// is acked only after the snapshot containing its effect is committed
// (ack-on-checkpoint). These bound how often that commit runs: whichever of the two
// thresholds is reached first triggers a checkpoint, so a busy stream does not
// snapshot every event (write-amplification) and a quiet stream still checkpoints
// on a timer (so a long silence's absence-timer state is made durable).
const (
	DefaultCheckpointEvents          = 1000
	DefaultCheckpointIntervalSeconds = 10
	// DefaultMaxEventFutureSkewSeconds bounds how far a device-reported occurred time may
	// lead the server-stamped processed time before DETECT clamps it (so one bad device clock
	// cannot advance the shared, snapshotted watermark far into the future and fire every
	// tenant's timers). Generous enough for legitimate device/server clock drift.
	DefaultMaxEventFutureSkewSeconds = 300
	// DefaultWatermarkLatenessSeconds is how far the event-time watermark is held back from
	// the newest event before windows close and timers fire — the out-of-orderness tolerance.
	// The resolved stream is largely ordered, but network/ingest reordering is real; a small
	// buffer trades a little detection latency for not closing a window before a slightly-late
	// event lands. Zero would close windows on the newest timestamp seen, dropping any reorder.
	DefaultWatermarkLatenessSeconds = 5
)

// Messaging subjects this service PRODUCES (ADR-051). Consumed subjects
// (resolved-events) are owned by device-management's config; these are
// event-processing's own emitted products, so the suffix constants live here.
const (
	// SUBJECT_DERIVED_EVENTS carries DETECT's first-class derived signal events —
	// one per detection — as a subscribe-able product (ADR-037). Published on the
	// per-tenant scoped subject "{instanceId}.{tenant}.derived-events"; the client
	// live-subscribes by tenant like any other event feed.
	SUBJECT_DERIVED_EVENTS = "derived-events"
)

// EventProcessingConfiguration is the typed configuration for the event-processing
// service (ADR-051): the DETECT + REACT pipeline extracted from device-management.
// It is loaded fail-closed (unknown keys rejected) via core.LoadConfiguration.
type EventProcessingConfiguration struct {
	// RdbConfiguration is the per-service datastore configuration for the Postgres
	// snapshot store (ADR-051). The snapshot store is a plain relational store (one
	// row of engine state per partition), not a TimescaleDB hypertable, so it binds
	// to the instance's Rdb persistence rather than Tsdb.
	RdbConfiguration config.MicroserviceDatastoreConfiguration

	// CheckpointEvents is the maximum number of applied events between snapshot
	// commits. Unset (0) defaults to 1000.
	CheckpointEvents int

	// CheckpointIntervalSeconds is the maximum wall-clock time between snapshot
	// commits, so a quiet stream still checkpoints. Unset (0) defaults to 10s.
	CheckpointIntervalSeconds int

	// MaxEventFutureSkewSeconds bounds device-reported future clock skew against the
	// server-stamped processed time (ADR-051 watermark integrity). Unset (0) defaults to
	// 300s; a negative value disables the clamp.
	MaxEventFutureSkewSeconds int

	// WatermarkLatenessSeconds is the event-time out-of-orderness tolerance: how far the
	// watermark lags the newest event before windows close and timers fire. Unset (0)
	// defaults to 5s; a negative value is treated as zero (no tolerance).
	WatermarkLatenessSeconds int
}

// NewEventProcessingConfiguration creates the default configuration.
func NewEventProcessingConfiguration() *EventProcessingConfiguration {
	cfg := &EventProcessingConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults is the ADR-022 decision-1 defaulting hook. It fills the checkpoint
// cadence defaults when unset.
func (c *EventProcessingConfiguration) ApplyDefaults() {
	if c.CheckpointEvents == 0 {
		c.CheckpointEvents = DefaultCheckpointEvents
	}
	if c.CheckpointIntervalSeconds == 0 {
		c.CheckpointIntervalSeconds = DefaultCheckpointIntervalSeconds
	}
	if c.MaxEventFutureSkewSeconds == 0 {
		c.MaxEventFutureSkewSeconds = DefaultMaxEventFutureSkewSeconds
	}
	if c.WatermarkLatenessSeconds == 0 {
		c.WatermarkLatenessSeconds = DefaultWatermarkLatenessSeconds
	}
}

// Validate is the ADR-022 decision-1 validation hook. It rejects a non-positive
// checkpoint cadence (fail closed): a zero/negative threshold would either never
// checkpoint or checkpoint every event, both of which break the ack-on-checkpoint
// contract or its write-amplification bound.
func (c *EventProcessingConfiguration) Validate() error {
	if c.CheckpointEvents <= 0 {
		return fmt.Errorf("checkpointEvents must be positive, got %d", c.CheckpointEvents)
	}
	if c.CheckpointIntervalSeconds <= 0 {
		return fmt.Errorf("checkpointIntervalSeconds must be positive, got %d", c.CheckpointIntervalSeconds)
	}
	return nil
}
