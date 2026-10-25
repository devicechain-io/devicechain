// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"math"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/rulehealth"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
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
	// DefaultCheckpointTimeoutSeconds is the floor of the deadline on each network call a
	// checkpoint makes; MaxCheckpointTimeoutSeconds is the most it may be set to.
	DefaultCheckpointTimeoutSeconds = 10
	MaxCheckpointTimeoutSeconds     = 120
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

	// DefaultDetectShards is how many ways the live detection engine is split by key: one, the
	// unsplit engine. The upper bound is detectcore.MaxShards, the largest the engine will build.
	DefaultDetectShards = 1
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

	// DefaultOutboundCallsPerSecond and DefaultOutboundBurst are the platform-default
	// per-tenant OUTBOUND egress ceiling REACT charges at the SOURCE (ADR-060 SD-3): before
	// publishing a connector-dispatch (httpCall/publish), the dispatcher charges the tenant's
	// outbound budget and SHEDS the action when over quota, so a runaway rule sheds at the source
	// rather than flooding the connector-dispatch stream and the downstream outbound-connectors
	// service. They deliberately MATCH outbound-connectors' egress defaults, and both ends meter the
	// same time — when the triggering telemetry reached the platform — so the source (no smoothing)
	// sheds a sustained flood first, leaving the sink's bounded wait as the defense-in-depth backstop.
	// Fail-safe per ADR-023: an unset (0) rate/burst defaults to these platform ceilings — NEVER
	// unlimited; a negative configured value is rejected at Validate (0 means unset and is replaced
	// by ApplyDefaults, so it is not itself rejected). Per-tenant overrides are
	// fetched from user-management (the outboundCallsPerSecond / outboundBurst governance fields).
	DefaultOutboundCallsPerSecond = 100
	DefaultOutboundBurst          = 200

	// DefaultShedLetterPerSecond / DefaultShedLetterBurst are the per-tenant budget for dead
	// letters REACT writes about connector actions its source gate shed, and
	// DefaultShedLetterGlobalPerSecond / DefaultShedLetterGlobalBurst the budget across every
	// tenant. A shed past either budget is counted (react_connector_shed_unlettered_total) and
	// summarised in one letter per tenant per minute instead of recorded individually: a tenant
	// flooding its outbound ceiling must not turn the dead-letter stream into a second copy of the
	// flood. The global budget bounds the synchronous letter writes on REACT's goroutine.
	DefaultShedLetterPerSecond       = 1
	DefaultShedLetterBurst           = 60
	DefaultShedLetterGlobalPerSecond = 10
	DefaultShedLetterGlobalBurst     = 100
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
	// commits, so a quiet stream still checkpoints. Unset (0) defaults to 10s. It must
	// be at most half of messaging.AckWait: the engine acknowledges its input only when
	// it checkpoints, so an interval near or past the broker's acknowledgement window
	// redelivers messages, and half leaves room for the checkpoint itself. See
	// validateCheckpointInterval.
	CheckpointIntervalSeconds int

	// CheckpointTimeoutSeconds is the floor of the deadline on each network call a checkpoint
	// makes (the derived-event publish, and the snapshot save), so a database or broker that
	// stops answering fails the checkpoint instead of hanging the detection loop. The save's
	// deadline grows past this on its own once a slow save has been seen to complete (see
	// saveTimeout), so this needs raising only for a store whose FIRST save is slower than it,
	// such as a very large engine on a throttled database. Unset (0) defaults to 10s; at most 120.
	CheckpointTimeoutSeconds int

	// WatermarkLatenessSeconds is the event-time out-of-orderness tolerance: how far the
	// watermark lags the newest event before windows close and timers fire. Unset (0)
	// defaults to 5s; a negative value is treated as zero (no tolerance).
	WatermarkLatenessSeconds int

	// DetectShards is how many ways the live detection engine's keyed state is split (by each
	// rule's own series key, a device or a correlation anchor). Unset (0) defaults to 1, the
	// unsplit engine; accepted range 1 to 64, anything else is refused at startup. Detections,
	// the watermark and the committed snapshot are identical for every value, so it may be changed
	// at any restart in either direction with no migration: the snapshot is re-split on restore.
	// In this release the shards are applied one after another, not in parallel: the setting
	// exists so the shard count can be chosen and observed before it buys anything.
	DetectShards int

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

	// OutboundCallsPerSecond and OutboundBurst are the platform-default per-tenant OUTBOUND
	// egress ceiling REACT charges at the SOURCE (ADR-060 SD-3): the sustained rate and burst of
	// connector-dispatch publishes (httpCall/publish actions) a tenant may emit before REACT drops
	// them. Unset (0) defaults to the platform ceilings (DefaultOutboundCallsPerSecond /
	// DefaultOutboundBurst) — fail-safe: never unlimited; a non-positive configured value is rejected
	// at Validate. Per-tenant overrides read from user-management take precedence at runtime; this is
	// the floor used for every tenant when per-tenant overrides are not wired.
	OutboundCallsPerSecond float64
	OutboundBurst          int

	// ShedLetterPerSecond / ShedLetterBurst are the per-tenant budget, and ShedLetterGlobalPerSecond
	// / ShedLetterGlobalBurst the all-tenant budget, for dead letters about connector actions REACT's
	// source gate shed (see DefaultShedLetterPerSecond). Unset (0) defaults to the platform values;
	// a negative, NaN or infinite value is rejected at Validate.
	ShedLetterPerSecond       float64
	ShedLetterBurst           int
	ShedLetterGlobalPerSecond float64
	ShedLetterGlobalBurst     int

	// RuleHealth bounds the rule-health fire counters (the console's per-rule "last fired" and
	// fire count), which are coalesced in memory and written in the background rather than on
	// the detection loop. See RuleHealthConfiguration.
	RuleHealth RuleHealthConfiguration

	// DerivedPublishing bounds how many detections a checkpoint has waiting for the broker at
	// once. See DerivedPublishingConfiguration.
	DerivedPublishing DerivedPublishingConfiguration

	// React sizes the REACT stage's consumer of the derived-event stream. See
	// ReactConfiguration.
	React ReactConfiguration
}

// DefaultReactWorkers is how many derived events REACT dispatches at once when react.workers is
// absent. MaxReactWorkers is the most it may be set to.
//
// One derived event costs REACT a few sequential network round trips and almost no CPU: the
// fetch, the rule's point read from this service's database, and the publish of each action to
// its stream, which waits for the broker's replicated acknowledgement. Dispatched one at a time
// that is a latency-bound ceiling, and GKE round 19 measured it at about 151 events a second
// against 194 offered (ack-pending 1 for an hour, the backlog growing by 43 a second). Eight
// overlap those waits about eight ways, leaving device-management's raise-alarm consumer, not
// REACT, as the next stage to bind, and hold at most eight of the default pool's twenty
// connections for the rule reads.
const (
	DefaultReactWorkers = 8
	MaxReactWorkers     = 64
)

// ReactConfiguration is functionalAreas.event-processing.config.react. Its one field is optional
// and takes its default when absent; a value that IS given must be inside its bounds, zero
// included, or the service refuses to start.
//
// Derived events of one device always dispatch in the order the stream holds them, whatever the
// count: each device's events go to one worker, chosen by hashing the tenant's subject and the
// device, and that worker handles them one after another. Only different devices overlap.
type ReactConfiguration struct {
	// Workers is how many derived events are dispatched at once: 1 to 64, default 8 (or half the
	// connection pool, when that is smaller). At 1 they are dispatched one after another, as REACT
	// did before this setting existed. Each worker holds at most one of this service's database
	// connections at a time (the rule read), so a count that is set must also be below the
	// connection pool's size.
	Workers *int
}

// applyDefaults fills an absent worker count: DefaultReactWorkers, capped at half the connection
// pool and floored at 1, so a deployment that sized a small pool before this setting existed still
// starts, with the rule reads holding no more than half its connections. An explicit count is
// never adjusted, only validated.
func (r *ReactConfiguration) applyDefaults(pool config.MicroserviceDatastoreConfiguration) {
	if r.Workers == nil {
		r.Workers = intPtr(max(1, min(DefaultReactWorkers, rdb.EffectiveMaxOpenConnections(pool)/2)))
	}
}

// validate refuses a worker count outside its bounds, or one the connection pool cannot carry
// beside everything else this service reads and writes. pool is the service's datastore
// configuration.
func (r ReactConfiguration) validate(pool config.MicroserviceDatastoreConfiguration) error {
	w := r.WorkerCount()
	if w < 1 || w > MaxReactWorkers {
		return fmt.Errorf("react.workers must be between 1 and %d, got %d", MaxReactWorkers, w)
	}
	// One worker is REACT as it ran before this setting existed, one rule read at a time beside
	// whatever pool the deployment has; refusing it would refuse a configuration that always started.
	if w == 1 {
		return nil
	}
	return rdb.CheckWriterCount("react.workers", w, pool)
}

// WorkerCount is the worker count in effect, an absent one as its default.
func (r ReactConfiguration) WorkerCount() int {
	if r.Workers == nil {
		return DefaultReactWorkers
	}
	return *r.Workers
}

// DerivedPublishingConfiguration is functionalAreas.event-processing.config.derivedPublishing.
// Every field is optional, and an absent one takes its default; a field that IS given must be
// inside its bounds, zero included, or the service refuses to start.
//
// A checkpoint publishes the detections it covers before it saves the snapshot. MaxInFlight 32,
// the default, lets detections of different devices (or correlation anchors) wait for their
// acknowledgements at the same time, never more than one per device at once, so each device's
// detections still reach the broker in order. MaxInFlight 1 sends one and waits for its
// acknowledgement before the next.
type DerivedPublishingConfiguration struct {
	// MaxInFlight is the most publishes waiting for an acknowledgement at once: 1 to 1024,
	// default 32.
	MaxInFlight *int
	// MaxBufferedBytes bounds the payload bytes held for publishing together (in flight, waiting
	// for room, or kept for a retry): 65536 to 268435456, default 4194304. A single event larger
	// than this is sent alone rather than never.
	MaxBufferedBytes *int64
}

// applyDefaults fills every absent field.
func (d *DerivedPublishingConfiguration) applyDefaults() {
	def := runtime.DefaultOutputConfig()
	if d.MaxInFlight == nil {
		d.MaxInFlight = intPtr(def.MaxInFlight)
	}
	if d.MaxBufferedBytes == nil {
		d.MaxBufferedBytes = int64Ptr(def.MaxBufferedBytes)
	}
}

// validate refuses a field outside its bounds, naming the key as an operator writes it.
func (d DerivedPublishingConfiguration) validate() error {
	if v := d.MaxInFlight; v != nil && (*v < 1 || *v > runtime.MaxMaxInFlight) {
		return fmt.Errorf("derivedPublishing.maxInFlight must be between 1 and %d, got %d", runtime.MaxMaxInFlight, *v)
	}
	if b := d.MaxBufferedBytes; b != nil && (*b < runtime.MinMaxBufferedBytes || *b > runtime.MaxMaxBufferedBytes) {
		return fmt.Errorf("derivedPublishing.maxBufferedBytes must be between %d and %d, got %d",
			runtime.MinMaxBufferedBytes, runtime.MaxMaxBufferedBytes, *b)
	}
	return nil
}

// Config is the configuration as the processor takes it, an absent field as its default.
func (d DerivedPublishingConfiguration) Config() runtime.OutputConfig {
	d.applyDefaults() // d is a copy: the caller's absent fields stay absent
	return runtime.OutputConfig{MaxInFlight: *d.MaxInFlight, MaxBufferedBytes: *d.MaxBufferedBytes}
}

// RuleHealthConfiguration is functionalAreas.event-processing.config.ruleHealth. Every field is
// optional, and an absent one takes its default; a field that IS given must be inside its
// bounds, zero included, or the service refuses to start. The bounds are finite on purpose:
// each one sizes memory or a database transaction.
//
// Accounting model for the two budgets: a pending entry is one (tenant, rule) pair with fires
// not yet written, wherever it is (being added to, detached for the next write, or inside the
// write in flight); a rule pending in two of those places counts twice. Its accounted size is
// 160 bytes plus its rule id, plus 320 bytes and the tenant token for a tenant's first entry,
// which overstates what it really retains. A fire that would take either budget past its
// bound is not admitted and is counted as discarded (reason full).
type RuleHealthConfiguration struct {
	// FlushIntervalMillis is how often pending fire counts are written: 50 to 60000, default
	// 1000. It is not a detection timer; it is roughly how stale the console's counts may be.
	FlushIntervalMillis *int
	// MaxPendingKeys bounds the distinct rules with fires not yet written: 1 to 1048576,
	// default 4096.
	MaxPendingKeys *int
	// MaxPendingBytes bounds their accounted memory: 65536 to 268435456, default 4194304.
	MaxPendingBytes *int64
	// MaxBatchRows is the most rules one tenant's write carries; a tenant with more pending is
	// written in several transactions: 1 to 4096, default 256.
	MaxBatchRows *int
	// WriteTimeoutMillis bounds one tenant write's whole transaction: 100 to 60000, default
	// 2000. A write that fails or times out is discarded, never retried, because it may have
	// committed and a retry would count its fires twice.
	WriteTimeoutMillis *int
}

func intPtr(v int) *int       { return &v }
func int64Ptr(v int64) *int64 { return &v }

// applyDefaults fills every absent field.
func (r *RuleHealthConfiguration) applyDefaults() {
	d := rulehealth.DefaultConfig()
	if r.FlushIntervalMillis == nil {
		r.FlushIntervalMillis = intPtr(int(d.FlushInterval / time.Millisecond))
	}
	if r.MaxPendingKeys == nil {
		r.MaxPendingKeys = intPtr(d.MaxPendingKeys)
	}
	if r.MaxPendingBytes == nil {
		r.MaxPendingBytes = int64Ptr(d.MaxPendingBytes)
	}
	if r.MaxBatchRows == nil {
		r.MaxBatchRows = intPtr(d.MaxBatchRows)
	}
	if r.WriteTimeoutMillis == nil {
		r.WriteTimeoutMillis = intPtr(int(d.WriteTimeout / time.Millisecond))
	}
}

// validate refuses a field outside its bounds, naming the key as an operator writes it.
func (r RuleHealthConfiguration) validate() error {
	millis := func(key string, v *int, lo, hi time.Duration) error {
		if v == nil {
			return nil // absent: the default
		}
		if *v < int(lo/time.Millisecond) || *v > int(hi/time.Millisecond) {
			return fmt.Errorf("ruleHealth.%s must be between %d and %d, got %d", key, lo/time.Millisecond, hi/time.Millisecond, *v)
		}
		return nil
	}
	count := func(key string, v *int, lo, hi int) error {
		if v == nil {
			return nil
		}
		if *v < lo || *v > hi {
			return fmt.Errorf("ruleHealth.%s must be between %d and %d, got %d", key, lo, hi, *v)
		}
		return nil
	}
	if err := millis("flushIntervalMillis", r.FlushIntervalMillis, rulehealth.MinFlushInterval, rulehealth.MaxFlushInterval); err != nil {
		return err
	}
	if err := count("maxPendingKeys", r.MaxPendingKeys, 1, rulehealth.MaxMaxPendingKeys); err != nil {
		return err
	}
	if b := r.MaxPendingBytes; b != nil && (*b < rulehealth.MinMaxPendingBytes || *b > rulehealth.MaxMaxPendingBytes) {
		return fmt.Errorf("ruleHealth.maxPendingBytes must be between %d and %d, got %d",
			rulehealth.MinMaxPendingBytes, rulehealth.MaxMaxPendingBytes, *b)
	}
	if err := count("maxBatchRows", r.MaxBatchRows, 1, rulehealth.MaxMaxBatchRows); err != nil {
		return err
	}
	if err := millis("writeTimeoutMillis", r.WriteTimeoutMillis, rulehealth.MinWriteTimeout, rulehealth.MaxWriteTimeout); err != nil {
		return err
	}
	return nil
}

// Config is the configuration as the accumulator takes it, an absent field as its default.
func (r RuleHealthConfiguration) Config() rulehealth.Config {
	r.applyDefaults() // r is a copy: the caller's absent fields stay absent
	return rulehealth.Config{
		FlushInterval:   time.Duration(*r.FlushIntervalMillis) * time.Millisecond,
		MaxPendingKeys:  *r.MaxPendingKeys,
		MaxPendingBytes: *r.MaxPendingBytes,
		MaxBatchRows:    *r.MaxBatchRows,
		WriteTimeout:    time.Duration(*r.WriteTimeoutMillis) * time.Millisecond,
	}
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
	if c.CheckpointTimeoutSeconds == 0 {
		c.CheckpointTimeoutSeconds = DefaultCheckpointTimeoutSeconds
	}
	if c.WatermarkLatenessSeconds == 0 {
		c.WatermarkLatenessSeconds = DefaultWatermarkLatenessSeconds
	}
	if c.DetectShards == 0 {
		c.DetectShards = DefaultDetectShards
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
	if c.OutboundCallsPerSecond == 0 {
		c.OutboundCallsPerSecond = DefaultOutboundCallsPerSecond
	}
	if c.OutboundBurst == 0 {
		c.OutboundBurst = DefaultOutboundBurst
	}
	if c.ShedLetterPerSecond == 0 {
		c.ShedLetterPerSecond = DefaultShedLetterPerSecond
	}
	if c.ShedLetterBurst == 0 {
		c.ShedLetterBurst = DefaultShedLetterBurst
	}
	if c.ShedLetterGlobalPerSecond == 0 {
		c.ShedLetterGlobalPerSecond = DefaultShedLetterGlobalPerSecond
	}
	if c.ShedLetterGlobalBurst == 0 {
		c.ShedLetterGlobalBurst = DefaultShedLetterGlobalBurst
	}
	c.RuleHealth.applyDefaults()
	c.DerivedPublishing.applyDefaults()
	c.React.applyDefaults(c.RdbConfiguration)
}

// RetiredConfigKeys names the keys this service accepted in an earlier release and has
// since removed, so a document still carrying one is reported rather than refused.
//
// maxEventFutureSkewSeconds was DETECT's own bound on how far a device-reported time may
// lead the server's clock. It is gone because that decision moved to the single place the
// event time is now decided — event resolution in device-management — and every consumer,
// live and replay alike, reads a time that arrives already bounded. Keeping a second copy
// here is what allowed live detection and the replay preview to disagree about when an
// event happened, under the one feature whose value is answering what a rule would have
// done.
//
// 🔴 IT IS LISTED HERE BECAUSE WE PUBLISHED IT. v0.11.0's detection-engine deployment page
// documented it as an event-processing key, in both locales. Without this entry the strict
// decode calls it unknown, the load fails closed, and an operator who did exactly what the
// documentation said gets a crash-looping DETECT engine on upgrade — detection down for
// every tenant on the instance, with an error message that reads like they typed it wrong.
//
// The value is NOT forwarded to device-management. It cannot be: this process does not
// configure that one, and quietly honouring a stale copy of a setting whose whole purpose
// was to stop existing in two places would rebuild the defect the move fixed.
func (c *EventProcessingConfiguration) RetiredConfigKeys() map[string]string {
	return map[string]string{
		"maxEventFutureSkewSeconds": "Device clock skew is now bounded once, where the event time is decided: " +
			"set functionalAreas.device-management.config.maxEventFutureSkewSeconds instead. " +
			"The detection engine no longer has a bound of its own, and this instance is running with " +
			"whatever device-management is configured for (300 seconds by default).",
	}
}

// checkpointIntervalCeiling is the longest checkpoint interval, in whole seconds, whose held
// messages are still acknowledged before the broker redelivers them: half of ackWait, which
// leaves room for the checkpoint tick, the snapshot save and the detection publish. It is
// derived from AckWait rather than written down, so the two cannot drift apart.
func checkpointIntervalCeiling(ackWait time.Duration) int {
	return int(ackWait / 2 / time.Second)
}

// validateCheckpointInterval refuses a non-positive interval and one past the ceiling.
//
// 🔴 A REFUSAL, NOT A SOFT CAP. The detection engine acknowledges its input only when it
// checkpoints, so on a quiet stream every message is held until the interval elapses. Past
// the acknowledgement window each is delivered again, and after MaxDeliver windows it is
// counted as an exhausted delivery — on a perfectly healthy engine. Clamping would run with
// a value the operator did not write; refusing at startup says so.
//
// The comparison is in SECONDS: converting secs to a Duration first would overflow for a
// huge value and wrap to something that passes.
func validateCheckpointInterval(secs int, ackWait time.Duration) error {
	if secs <= 0 {
		return fmt.Errorf("checkpointIntervalSeconds must be positive, got %d", secs)
	}
	if ceiling := checkpointIntervalCeiling(ackWait); secs > ceiling {
		return fmt.Errorf("checkpointIntervalSeconds must be at most %d (half the broker's %s acknowledgement "+
			"window), got %d: messages are acknowledged only at a checkpoint, so an interval near or past the window "+
			"redelivers them and, after repeated windows, reports them as exhausted deliveries; half the window "+
			"leaves room for the checkpoint itself", ceiling, ackWait, secs)
	}
	return nil
}

// Validate is the ADR-022 decision-1 validation hook. It rejects a non-positive
// checkpoint cadence (fail closed): a zero/negative threshold would either never
// checkpoint or checkpoint every event, both of which break the ack-on-checkpoint
// contract or its write-amplification bound. It also rejects a checkpoint interval
// longer than half the broker's acknowledgement window (validateCheckpointInterval).
func (c *EventProcessingConfiguration) Validate() error {
	if c.CheckpointEvents <= 0 {
		return fmt.Errorf("checkpointEvents must be positive, got %d", c.CheckpointEvents)
	}
	if err := validateCheckpointInterval(c.CheckpointIntervalSeconds, messaging.AckWait); err != nil {
		return err
	}
	if c.CheckpointTimeoutSeconds <= 0 || c.CheckpointTimeoutSeconds > MaxCheckpointTimeoutSeconds {
		return fmt.Errorf("checkpointTimeoutSeconds must be between 1 and %d, got %d",
			MaxCheckpointTimeoutSeconds, c.CheckpointTimeoutSeconds)
	}
	// Refused rather than clamped: the engine would quietly run a count the operator did not write.
	// 0 is unset, and ApplyDefaults has already made it 1 by the time the service validates.
	if c.DetectShards < 0 || c.DetectShards > detectcore.MaxShards {
		return fmt.Errorf("detectShards must be between 1 and %d (or unset), got %d", detectcore.MaxShards, c.DetectShards)
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
	if c.OutboundCallsPerSecond < 0 {
		return fmt.Errorf("outboundCallsPerSecond must not be negative, got %g", c.OutboundCallsPerSecond)
	}
	if c.OutboundBurst < 0 {
		return fmt.Errorf("outboundBurst must not be negative, got %d", c.OutboundBurst)
	}
	// The shed-letter budgets follow the same convention (0 is unset and defaulted), and a rate
	// must also be finite: NaN compares false against everything, so it would pass a sign check
	// and reach a limiter that then admits nothing, or everything.
	for _, r := range []struct {
		key string
		v   float64
	}{{"shedLetterPerSecond", c.ShedLetterPerSecond}, {"shedLetterGlobalPerSecond", c.ShedLetterGlobalPerSecond}} {
		if math.IsNaN(r.v) || math.IsInf(r.v, 0) || r.v < 0 {
			return fmt.Errorf("%s must be a finite, non-negative number, got %g", r.key, r.v)
		}
	}
	if c.ShedLetterBurst < 0 {
		return fmt.Errorf("shedLetterBurst must not be negative, got %d", c.ShedLetterBurst)
	}
	if c.ShedLetterGlobalBurst < 0 {
		return fmt.Errorf("shedLetterGlobalBurst must not be negative, got %d", c.ShedLetterGlobalBurst)
	}
	// Bounds checked here against the accumulator's own: the two must agree, and a config that
	// passed here and was then refused by rulehealth.New would fail at the first term, not at load.
	if err := c.RuleHealth.validate(); err != nil {
		return err
	}
	if err := c.RuleHealth.Config().Validate(); err != nil {
		return err
	}
	if err := c.DerivedPublishing.validate(); err != nil {
		return err
	}
	if err := c.DerivedPublishing.Config().Validate(); err != nil {
		return err
	}
	if err := c.React.validate(c.RdbConfiguration); err != nil {
		return err
	}
	return nil
}

// RenamedConfigKeys refuses the old spelling of a renamed key with the new one named
// (core.ConfigRenamer): the outbound ceiling meters connector CALLS, and the key now says so. It is refused rather than
// retired because the setting still exists — stripping it would start the service on
// the platform default instead of the ceiling the operator chose.
func (c *EventProcessingConfiguration) RenamedConfigKeys() map[string]string {
	return map[string]string{"outboundMessagesPerSecond": "outboundCallsPerSecond"}
}
