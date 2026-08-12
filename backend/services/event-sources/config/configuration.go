// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/governance"
)

const (
	// DefaultIngestMessagesPerSecond and DefaultIngestBurst are the platform
	// per-tenant ingest ceiling applied when none is configured. They are a
	// generous safety ceiling — high enough not to shed a normally busy fleet,
	// low enough that a single runaway tenant cannot saturate the pipeline. A
	// genuinely high-volume tenant is raised past this by a per-tenant override
	// (a later slice); the platform default is deliberately never unlimited.
	DefaultIngestMessagesPerSecond = 1000
	DefaultIngestBurst             = 2000

	// Broker-presence defaults.
	//
	// The reconcile interval is the one with a real trade in it. It is how long a
	// device can read as connected after a broker restart that announced no deaths —
	// so shorter is more accurate — against the cost of a full cluster inventory plus
	// one projection read per tenant. Five minutes keeps the repair well inside the
	// window that matters for command delivery while leaving the steady-state cost
	// negligible; the advisories, not this pass, carry the normal case.
	DefaultPresenceReconcileSeconds = 300
	// The canary runs far more often than the reconciler: it is the only signal that
	// distinguishes a healthy quiet fleet from a dead subscription, and being an hour
	// late to notice a dead tap is being an hour late to every alarm behind it.
	DefaultPresenceCanarySeconds = 60
	// One probe's budget: a broker round trip plus an advisory, with generous room for
	// a loaded cluster. Too tight and the canary reports failures the tap does not have.
	DefaultPresenceCanaryDeadlineSeconds = 15
	// The fan-out collection window. Long enough that a busy server is not mistaken for
	// an absent one — which would withhold every disconnect that pass — and short
	// enough not to stretch the pass itself.
	DefaultPresenceInventoryGatherSeconds = 5
)

// Decodes event payloads into standardized format.
type EventDecoder struct {
	Type          string
	Configuration map[string]string
}

// Source that reads events from a protocol and decodes them.
type EventSource struct {
	Id            string
	Type          string
	Configuration map[string]string
	Decoder       EventDecoder
	Debug         bool
}

type KafkaEventBatching struct {
	MaxBatchSize   int
	BatchTimeoutMs int
}

// IngestRateLimit is the platform-default, per-tenant ingest ceiling. Every
// tenant is metered by an independent token bucket at these rates and events over
// the ceiling are shed at the receive point, before decode, so a noisy tenant
// spends no pipeline CPU past its allowance. It is fail-safe: an unset or
// non-positive value falls back to the platform default rather than to unlimited,
// so a misconfiguration cannot silently remove the protection. Per-tenant
// overrides that raise the ceiling for a legitimately high-volume tenant are a
// later slice.
type IngestRateLimit struct {
	// MessagesPerSecond is the sustained per-tenant event rate.
	MessagesPerSecond float64
	// Burst is the largest instantaneous batch a tenant may send before the
	// sustained rate applies — it absorbs bursty devices without raising the
	// sustained ceiling.
	Burst int
}

// Contention is the ADR-063 preferential-shedding control. At GA the whole trigger
// surface is one operator knob, ManualFloor: the composite level L = max(auto, manual)
// collapses to L = manualFloor because the automatic saturation controller is deferred
// post-GA (it would make the load-test gate's verdict flaky — ADR-063 amendment
// 2026-07-20). At the active level, event-sources lowers the effective ingest ceiling
// of the shed classes (best-effort first, then bronze, then silver; gold is never
// shed) so a premium tenant rides through while a lower-tier one sheds at the same 429
// path — reusing the ADR-023 limiter, never a new drop.
type Contention struct {
	// ManualFloor is the shed level L ∈ {0..3}. 0 (the default) sheds nothing —
	// preferential shedding is off. 1 sheds best-effort, 2 adds bronze, 3 adds silver.
	// Deliberately NOT defaulted through ApplyDefaults: 0 is the intended resting value
	// AND a legal explicit setting, so a "<=0 → default" clause would only make an
	// explicit floor of 0 impossible to express. Range-enforced in Validate.
	ManualFloor int
}

// BrokerPresence configures broker-asserted MQTT presence (ADR-067): the tap that
// turns the NATS broker's own connection advisories into authoritative device presence,
// replacing the "published within 600s" guess that alarms, dashboards and command
// delivery all inherit.
//
// 🔑 ENABLED IS NOT THE ONLY SWITCH, AND IT IS THE LEAST INTERESTING ONE. The tap also
// requires a system-account credential, a source pointed at the platform broker, and
// reachable service-to-service calls; any of those missing leaves it off with a log
// line saying which. This flag exists so an operator can turn it off deliberately —
// on an instance where the broker is shared with something that objects to a system
// account subscriber, say — rather than by removing a credential.
type BrokerPresence struct {
	// Enabled turns the tap on, defaulting to TRUE when unset: presence that is merely
	// inferred is a guess every downstream feature inherits, so the accurate answer is
	// the one to have by default.
	//
	// 🔑 IT IS A POINTER BECAUSE A bool CANNOT EXPRESS THIS. The zero value of a bool is
	// false, which is indistinguishable from an operator writing `enabled: false` — so a
	// plain bool defaulting to true could only be done by rewriting false to true in
	// ApplyDefaults, which would make the setting impossible to turn off. Same trap the
	// contention floor documents from the other direction, where 0 is a legal explicit
	// value. nil ⇒ unset ⇒ the default; a written value is honoured either way.
	Enabled *bool
	// ReconcileSeconds is how often the broker's live connection inventory is compared
	// against the projection. This is NOT a backstop — a graceful broker shutdown emits
	// no disconnect advisories at all, so this pass is the only account of those deaths.
	ReconcileSeconds int
	// CanarySeconds is how often the tap proves itself alive by opening its own MQTT
	// connection and observing the broker announce it. See presence.Canary for why the
	// obvious instrument cannot fail.
	CanarySeconds int
	// CanaryDeadlineSeconds bounds one canary probe.
	CanaryDeadlineSeconds int
	// InventoryGatherSeconds is how long to collect monitoring replies from the
	// cluster. It bounds a fan-out, so it trades pass latency against the risk of
	// judging the inventory incomplete because a server was merely slow — and an
	// inventory judged incomplete withholds every disconnect that pass.
	InventoryGatherSeconds int
}

// ReconcileInterval, CanaryInterval, CanaryDeadline and InventoryGatherWindow render
// the configured seconds as durations, applying the same fail-safe defaulting the rest
// of this file uses: a non-positive value falls back to the platform default rather
// than to zero, which would make a ticker panic or a gather window collect nothing.
func (b BrokerPresence) ReconcileInterval() time.Duration {
	return secondsOr(b.ReconcileSeconds, DefaultPresenceReconcileSeconds)
}
func (b BrokerPresence) CanaryInterval() time.Duration {
	return secondsOr(b.CanarySeconds, DefaultPresenceCanarySeconds)
}
func (b BrokerPresence) CanaryDeadline() time.Duration {
	return secondsOr(b.CanaryDeadlineSeconds, DefaultPresenceCanaryDeadlineSeconds)
}
func (b BrokerPresence) InventoryGatherWindow() time.Duration {
	return secondsOr(b.InventoryGatherSeconds, DefaultPresenceInventoryGatherSeconds)
}

// IsEnabled reports whether the tap should run, applying the default for an unset
// value. It is the only thing that reads Enabled, so the nil case is handled once.
func (b BrokerPresence) IsEnabled() bool { return b.Enabled == nil || *b.Enabled }

func secondsOr(v, fallback int) time.Duration {
	if v <= 0 {
		v = fallback
	}
	return time.Duration(v) * time.Second
}

type EventSourcesConfiguration struct {
	EventSources         []EventSource
	InboundEventBatching KafkaEventBatching
	IngestRateLimit      IngestRateLimit
	Contention           Contention
	BrokerPresence       BrokerPresence
}

// Creates the default event sources configuration
func NewEventSourcesConfiguration() *EventSourcesConfiguration {
	cfg := &EventSourcesConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults fills unset fields with their defaults so configuration loaded
// from a document that omits them is still well-formed (ADR-022 decision 1). It
// runs on both the constructor and the load path so there is one source of
// defaults. An empty event-source list is load-bearing: without a source the
// service ingests nothing, so it is populated with the default MQTT source.
func (c *EventSourcesConfiguration) ApplyDefaults() {
	if len(c.EventSources) == 0 {
		c.EventSources = []EventSource{
			{
				Id:   "mqtt1",
				Type: "mqtt",
				Configuration: map[string]string{
					"host": "dc-nats.dc-system",
					"port": "1883",
					// Device-plane topic (ADR-006): "{instanceId}/{tenant}/...". The
					// first level is the instance id and the second carries the tenant
					// the producer scopes on.
					//
					// This applies ONLY to an external-broker source. A source whose
					// host is the platform's own broker is not an MQTT client at all
					// any more (ADR-030 amendment) — it consumes the device-events
					// capture stream, whose subject filter is structural — so this
					// value is not read for it, at bind time or ever.
					//
					// Note "+/#" is deliberately permissive and is only defensible
					// against a broker we do not own, where the topic shape is the
					// operator's to choose. It will also match anything else on that
					// broker, so an external source pointed at a busy broker should be
					// narrowed.
					"topic": "+/#",
				},
				Decoder: EventDecoder{
					Type:          "json",
					Configuration: map[string]string{},
				},
				Debug: false,
			},
			{
				// HTTP ingest is on by default alongside MQTT (TB §2.9, the most
				// common integration after MQTT). Devices POST events to
				// "/{instanceId}/{tenant}/events"; the instance and tenant are taken
				// from the path, mirroring the MQTT topic convention (ADR-006/ADR-048).
				Id:   "http1",
				Type: "http",
				Configuration: map[string]string{
					"port": "8081",
				},
				Decoder: EventDecoder{
					Type:          "json",
					Configuration: map[string]string{},
				},
				Debug: false,
			},
		}
	}
	if c.InboundEventBatching.MaxBatchSize == 0 {
		c.InboundEventBatching.MaxBatchSize = 100
	}
	if c.InboundEventBatching.BatchTimeoutMs == 0 {
		c.InboundEventBatching.BatchTimeoutMs = 100
	}
	// Fail-safe defaulting: a non-positive rate or burst falls back to the
	// platform ceiling, never to unlimited, so an omitted or zeroed limit still
	// meters every tenant.
	if c.IngestRateLimit.MessagesPerSecond <= 0 {
		c.IngestRateLimit.MessagesPerSecond = DefaultIngestMessagesPerSecond
	}
	if c.IngestRateLimit.Burst <= 0 {
		c.IngestRateLimit.Burst = DefaultIngestBurst
	}
}

// Validate enforces semantic constraints after decoding and defaulting, failing
// the load closed on an invalid configuration (ADR-022 decision 1). Batching
// bounds must be positive; the source list is left to the source loaders.
func (c *EventSourcesConfiguration) Validate() error {
	if c.InboundEventBatching.MaxBatchSize <= 0 {
		return fmt.Errorf("inboundEventBatching.maxBatchSize must be positive (got %d)", c.InboundEventBatching.MaxBatchSize)
	}
	if c.InboundEventBatching.BatchTimeoutMs <= 0 {
		return fmt.Errorf("inboundEventBatching.batchTimeoutMs must be positive (got %d)", c.InboundEventBatching.BatchTimeoutMs)
	}
	// The shed floor names a level on the ADR-063 ladder (0..3); a value outside it
	// names no level. Fail the load closed rather than clamp — a floor of 7 is a
	// misconfiguration the operator must see, not one to silently reinterpret.
	if c.Contention.ManualFloor < 0 || c.Contention.ManualFloor > governance.MaxShedLevel {
		return fmt.Errorf("contention.manualFloor must be between 0 and %d (got %d)", governance.MaxShedLevel, c.Contention.ManualFloor)
	}
	return nil
}
