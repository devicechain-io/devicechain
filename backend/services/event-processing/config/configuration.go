// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"math"
	"time"

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
	// DefaultWatermarkLatenessSeconds is how far the event-time watermark is held back from
	// the newest event before windows close and timers fire — the out-of-orderness tolerance.
	// The resolved stream is largely ordered, but network/ingest reordering is real; a small
	// buffer trades a little detection latency for not closing a window before a slightly-late
	// event lands. Zero would close windows on the newest timestamp seen, dropping any reorder.
	//
	// Lateness is ALSO the end-to-end pipeline-latency budget for wall-clock idle-advance
	// (ADR-051 slice 4c): idle-advance can confirm the DETECT consumer is drained (NumPending),
	// but it cannot see an event still in flight UPSTREAM of the resolved stream (device → MQTT
	// → event-sources decode → device-management resolution → publish). If that upstream path
	// stalls longer than Lateness during a quiet tail, idle-advance can fire an absence for a
	// device that did report — the report simply had not reached the stream yet. This is inherent
	// to absence-on-silence (the platform genuinely received nothing); size Lateness above the
	// worst tolerable upstream-outage window if such false absences must be avoided.
	DefaultWatermarkLatenessSeconds = 5
	// DefaultIdleAdvanceGuardSeconds is how long the read loop must be quiet — nothing delivered —
	// before DETECT tests broker emptiness and, if caught up, advances its logical clock off the
	// wall clock so a silent series' absence/duration/session timer fires (ADR-051 slice 4c). The
	// guard drains the reader's local fetch buffer; the AUTHORITATIVE caught-up signal is the
	// broker's zero pending + ack-pending backlog, because read-loop silence alone is also what an
	// outage or a consumer re-bind looks like. A few seconds is ample. A negative value disables
	// idle-advance (absence then fires only when a later event advances the watermark — pre-4c).
	//
	// Absence-detection latency floor (worst case) is therefore: the rule's timeout + Lateness +
	// max(this guard, the checkpoint interval) + one tick — a device that stops reporting is
	// flagged that long after its last event, not instantly.
	DefaultIdleAdvanceGuardSeconds = 5
	// DefaultMaxRulesPerTenant and DefaultMaxLiveKeysPerTenant are the per-tenant runtime state
	// budget ceilings (ADR-023 amendment, ADR-051 slice 6c). DETECT is a shared singleton: all
	// tenants' rules and keyed window/timer state live in one process, so one tenant's runaway
	// cardinality (rules × devices/anchors) could OOM the engine and take detection down for EVERY
	// tenant. The budget bounds each tenant so the offender is contained, not the whole process.
	// Fail-safe per ADR-023: an unset (0) budget defaults to these platform ceilings — NEVER
	// unlimited; a negative value is rejected at Validate. Slice 6c-1 measures + exposes usage
	// against these; slice 6c-2 enforces (reject over-budget rules / disable an offender's rules).
	// They are platform-operator tunable (raise for a genuinely large tenant).
	DefaultMaxRulesPerTenant    = 500
	DefaultMaxLiveKeysPerTenant = 1_000_000

	// DefaultMaxRuleDurationSeconds is the platform ceiling on an authored rule's temporal
	// extent — its window, hold, timeout or gap. A window-shaped rule retains one record per
	// sample for the whole window, so the authored duration is the only knob that bounds that
	// heap BEFORE it is allocated: at a 10s reporting interval one series on a 7-day window
	// retains ~2MB, and a rule runs per series, in a process shared by every tenant.
	//
	// A day matches the ceiling the rule-PREVIEW path has always enforced. Previewing a rule
	// over more than a day was already refused as too expensive, while publishing one — the
	// path that holds the memory for as long as the rule lives — was unbounded; this closes
	// that asymmetry. Fail-safe per ADR-023: unset (0) defaults to this ceiling, NEVER
	// unlimited; a negative value is rejected at Validate. Operator-tunable (raise it for a
	// tenant with a genuinely long-window rule, having sized the retention above).
	DefaultMaxRuleDurationSeconds = 86400

	// DefaultMaxRetainedSamplesPerTenant is the second runtime state-budget axis: the per-sample
	// records a tenant's window-shaped rules may hold across the shared engine. It exists because
	// the live-key ceiling above cannot see this overrun — a 30-day window on one chatty series is
	// ONE live key and however many samples it retains. A retained sample is ~32 bytes, so this
	// ceiling is roughly 160MB of window buffers per tenant, the same order as the live-key
	// ceiling's own footprint. Fail-safe per ADR-023: unset (0) defaults to this ceiling, NEVER
	// unlimited; a negative value is rejected at Validate.
	DefaultMaxRetainedSamplesPerTenant = 5_000_000

	// DefaultOutboundMessagesPerSecond and DefaultOutboundBurst are the platform-default
	// per-tenant OUTBOUND egress ceiling REACT charges at the SOURCE (ADR-060 SD-3): before
	// publishing a connector-dispatch (httpCall/publish), the dispatcher charges the tenant's
	// outbound budget and DROPS the action when over quota, so a runaway rule sheds at the source
	// rather than flooding the connector-dispatch stream and the downstream outbound-connectors
	// service. They deliberately MATCH outbound-connectors' egress defaults, so the source Allow-drop
	// and the sink's bounded Wait meter the same rate — the source (immediate, no smoothing) sheds a
	// sustained flood first, leaving the sink's egress Wait as the rare defense-in-depth backstop.
	// Fail-safe per ADR-023: an unset (0) rate/burst defaults to these platform ceilings — NEVER
	// unlimited; a negative configured value is rejected at Validate (0 means unset and is replaced
	// by ApplyDefaults, so it is not itself rejected). Per-tenant overrides are
	// fetched from user-management (the outboundMessagesPerSecond / outboundBurst governance fields).
	DefaultOutboundMessagesPerSecond = 100
	DefaultOutboundBurst             = 200
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

	// WatermarkLatenessSeconds is the event-time out-of-orderness tolerance: how far the
	// watermark lags the newest event before windows close and timers fire. Unset (0)
	// defaults to 5s; a negative value is treated as zero (no tolerance).
	WatermarkLatenessSeconds int

	// IdleAdvanceGuardSeconds is how long the resolved stream must be quiet before DETECT
	// advances its logical clock off the wall clock so a silent series' absence/duration
	// timer fires (ADR-051 slice 4c). Unset (0) defaults to 5s; a negative value disables
	// idle-advance entirely.
	IdleAdvanceGuardSeconds int

	// MaxRulesPerTenant and MaxLiveKeysPerTenant are the per-tenant runtime state budget (ADR-023
	// amendment, ADR-051 slice 6c): the max detection rules a tenant may run, and the max live keyed
	// windows/timers (rules × devices/anchors) its rules may hold, in the shared DETECT engine. Unset
	// (0) defaults to the platform ceilings (DefaultMaxRulesPerTenant / DefaultMaxLiveKeysPerTenant) —
	// fail-safe: never unlimited; a negative value is rejected. Slice 6c-1 measures usage against
	// these and exposes it (bounded gauges); slice 6c-2 enforces them.
	MaxRulesPerTenant    int
	MaxLiveKeysPerTenant int

	// MaxRuleDurationSeconds is the publish-time ceiling on an authored detection rule's
	// temporal extent — window, hold, timeout and gap alike. It bounds the retained heap a
	// window-shaped rule commits to (one record per sample for the whole window) before that
	// memory is allocated, which is the one thing the live-key budget above cannot see: a
	// 30-day window on a single chatty series is ONE live key. Unset (0) defaults to the
	// platform ceiling (DefaultMaxRuleDurationSeconds) — fail-safe: never unlimited; a
	// negative value is rejected. Enforced by rules.Compile at both the publish gate and the
	// runtime fact consumer, so a rule refused at publish is also refused on load.
	MaxRuleDurationSeconds int

	// MaxRetainedSamplesPerTenant is the per-tenant ceiling on retained per-sample records held
	// by window-shaped rules (repeating / sliding aggregate / correlation members). It is the
	// measured counterpart to MaxRuleDurationSeconds: the ceiling bounds what a rule may declare,
	// this reports what the running rules are holding right now. Note it does NOT report rules
	// that exceed the current ceiling — those do not run at all (CompilePublishedRules skips a
	// rule that fails to compile, and the startup rebuild goes through that same path), so they
	// hold nothing to measure. Unset (0) defaults to the platform ceiling
	// (DefaultMaxRetainedSamplesPerTenant) — fail-safe: never unlimited; negative is rejected.
	MaxRetainedSamplesPerTenant int

	// OutboundMessagesPerSecond and OutboundBurst are the platform-default per-tenant OUTBOUND
	// egress ceiling REACT charges at the SOURCE (ADR-060 SD-3): the sustained rate and burst of
	// connector-dispatch publishes (httpCall/publish actions) a tenant may emit before REACT drops
	// them. Unset (0) defaults to the platform ceilings (DefaultOutboundMessagesPerSecond /
	// DefaultOutboundBurst) — fail-safe: never unlimited; a non-positive configured value is rejected
	// at Validate. Per-tenant overrides read from user-management take precedence at runtime; this is
	// the floor used for every tenant when per-tenant overrides are not wired.
	OutboundMessagesPerSecond float64
	OutboundBurst             int
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
	if c.WatermarkLatenessSeconds == 0 {
		c.WatermarkLatenessSeconds = DefaultWatermarkLatenessSeconds
	}
	if c.IdleAdvanceGuardSeconds == 0 {
		c.IdleAdvanceGuardSeconds = DefaultIdleAdvanceGuardSeconds
	}
	if c.MaxRulesPerTenant == 0 {
		c.MaxRulesPerTenant = DefaultMaxRulesPerTenant
	}
	if c.MaxLiveKeysPerTenant == 0 {
		c.MaxLiveKeysPerTenant = DefaultMaxLiveKeysPerTenant
	}
	if c.MaxRuleDurationSeconds == 0 {
		c.MaxRuleDurationSeconds = DefaultMaxRuleDurationSeconds
	}
	if c.MaxRetainedSamplesPerTenant == 0 {
		c.MaxRetainedSamplesPerTenant = DefaultMaxRetainedSamplesPerTenant
	}
	if c.OutboundMessagesPerSecond == 0 {
		c.OutboundMessagesPerSecond = DefaultOutboundMessagesPerSecond
	}
	if c.OutboundBurst == 0 {
		c.OutboundBurst = DefaultOutboundBurst
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
	// The per-tenant budgets fail closed: a negative ceiling is rejected rather than silently treated
	// as unlimited (ADR-023 — an unset budget defaults to the platform ceiling in ApplyDefaults, never
	// unlimited; a negative value is an operator error, not an "unlimited" escape hatch).
	if c.MaxRulesPerTenant < 0 {
		return fmt.Errorf("maxRulesPerTenant must not be negative, got %d", c.MaxRulesPerTenant)
	}
	if c.MaxLiveKeysPerTenant < 0 {
		return fmt.Errorf("maxLiveKeysPerTenant must not be negative, got %d", c.MaxLiveKeysPerTenant)
	}
	if c.MaxRuleDurationSeconds < 0 {
		return fmt.Errorf("maxRuleDurationSeconds must not be negative, got %d", c.MaxRuleDurationSeconds)
	}
	// Reject a value that cannot survive the seconds→time.Duration conversion main does. This is
	// not pedantry: the conversion multiplies by 1e9, so an operator writing a deliberately huge
	// number to mean "effectively unlimited" WRAPS. It wraps two ways and both are silent —
	// negative (which withDefaults then floors to the STRICTEST 24h, the exact opposite of the
	// intent), or small-positive (18446744074 lands on ~290ms, which refuses every real rule at
	// publish AND drops every published rule at the next restart — detection dead behind a green
	// config load). Failing at startup is what the fail-closed posture promises.
	if maxSec := int64(math.MaxInt64) / int64(time.Second); int64(c.MaxRuleDurationSeconds) > maxSec {
		return fmt.Errorf("maxRuleDurationSeconds must not exceed %d, got %d", maxSec, c.MaxRuleDurationSeconds)
	}
	if c.MaxRetainedSamplesPerTenant < 0 {
		return fmt.Errorf("maxRetainedSamplesPerTenant must not be negative, got %d", c.MaxRetainedSamplesPerTenant)
	}
	// The outbound egress ceiling fails closed on a NEGATIVE rate/burst (an operator error, not an
	// unlimited escape hatch), matching the per-tenant-budget precedent above: an unset (0) value is
	// replaced by the platform ceiling in ApplyDefaults — never unlimited — so 0 cannot reach a live
	// limiter, and a 0 is not itself rejected (it is indistinguishable from unset). A negative value
	// is a misconfiguration and rejected.
	if c.OutboundMessagesPerSecond < 0 {
		return fmt.Errorf("outboundMessagesPerSecond must not be negative, got %g", c.OutboundMessagesPerSecond)
	}
	if c.OutboundBurst < 0 {
		return fmt.Errorf("outboundBurst must not be negative, got %d", c.OutboundBurst)
	}
	return nil
}
