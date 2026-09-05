// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/transport"
	"github.com/rs/zerolog/log"
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

	// DefaultMaxReadingsPerMessage is the platform ceiling on the readings ONE inbound
	// message may carry on the JSON transports (HTTP, external MQTT, and the platform
	// broker's capture stream). A reading is one stored datum: a metric key for
	// measurements, an entry for locations and alerts.
	//
	// 🔴 IT BOUNDS ONE MESSAGE'S COST, WHICH NO RATE CEILING CAN. The ADR-023 limiter
	// meters MESSAGES, so a single POST of 40 000 readings costs one token; a per-tenant
	// RATE gate cannot help either, because a bucket's first message always passes at
	// full burst. What is being bounded is fan-out: every reading becomes a stored row, a
	// projection write and an evaluation on the single DETECT goroutine every tenant
	// shares, and a sliding window folds each one into a time-sorted buffer. The 1 MiB
	// body/stream ceilings bound the BYTES and admit ~40 000 readings inside them, so
	// they are not this bound.
	//
	// 🔑 HOW THIS RELATES TO adapter.DefaultSamplesPerMessage (25), WHICH IS NOT A RIVAL
	// CEILING. That one is an assumed MEAN — the factor that scales a tenant's message
	// ceiling into its sample ceiling, so a compliant multi-sample fleet is not shed. This
	// is a hard MAXIMUM for a single message. A mean of 25 under a maximum of 1000 is one
	// coherent shape, not two ceilings 40x apart, and the two are meant to be wired
	// together: IngestLimiter takes a caller's per-message cap as its sampleBurstFloor
	// (LwM2M passes decode.MaxSamplesPerNotify there) so a single compliant message never
	// sheds on the burst edge. Both count the same unit — readings — which is why they can
	// be compared at all.
	//
	// ⏭ The JSON transports do not yet wire adapter.IngestLimiter's stage 2, and should.
	// Two things must exist first, and neither does: a send-time-aware AllowN (the stage-1
	// gate deliberately splits a LIVE from a BACKLOG limiter and meters the backlog on the
	// broker's append time, because metering a post-outage drain on ARRIVAL sheds data the
	// broker already acknowledged — AllowSamples has no equivalent), and a redelivery
	// signal at the post-decode hook, which today would double-charge every JetStream
	// redelivery. Neither is a substitute for this ceiling in any case: a rate gate bounds
	// sustained VOLUME, this bounds ONE message.
	//
	// 1000 leaves ample room for a store-and-forward device handing over a buffered batch
	// while sitting well below what the byte ceiling admits. A device with more to deliver
	// splits it across messages; it is REFUSED, never truncated, because a truncated batch
	// is data loss that reads to the device as success.
	DefaultMaxReadingsPerMessage = 1000

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
//
// It carried a `Debug` flag that nothing ever read — the transports log at debug level
// through the process-wide zerolog level, which is where a per-source flag would have had
// to be consulted and never was. It is gone rather than wired up: a per-source log level
// is a real feature and this was not it, and a field that accepts a value and ignores it
// is worse than one that does not exist.
type EventSource struct {
	Id            string
	Type          string
	Configuration map[string]string
	Decoder       EventDecoder
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
	EventSources    []EventSource
	IngestRateLimit IngestRateLimit
	// MaxReadingsPerMessage caps the readings one inbound message may carry on the JSON
	// transports. Like the rate ceiling it is fail-safe: a non-positive value falls back
	// to the platform default, never to unlimited.
	MaxReadingsPerMessage int
	Contention            Contention
	BrokerPresence        BrokerPresence
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
			},
		}
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
	if c.MaxReadingsPerMessage <= 0 {
		c.MaxReadingsPerMessage = DefaultMaxReadingsPerMessage
	}
}

// RetiredConfigKeys names the keys this service accepted in an earlier release and has
// since removed, so a document still carrying one is reported rather than refused.
//
// inboundEventBatching (a `KafkaEventBatching`, on a platform that has never had Kafka in
// it) named a producer batch size and a flush timeout for a producer that does not exist:
// nothing in this service ever read either value. It stayed harmless right up to its
// Validate clause, which REFUSED THE LOAD on a non-positive value — so an operator who
// wrote `maxBatchSize: 0` took down every event source and the presence tap on the
// instance over a number nothing would have consulted.
//
// It is listed here rather than simply deleted because the load is a strict decode: a
// document still carrying the key would otherwise be called unknown and fail closed, which
// is the same outage in a different sentence.
func (c *EventSourcesConfiguration) RetiredConfigKeys() map[string]string {
	return map[string]string{
		"inboundEventBatching": "Inbound events are not batched by this service and never were — " +
			"the key configured a Kafka producer this platform does not have, and no code read it. " +
			"Remove it; there is no replacement setting.",
	}
}

// Validate enforces semantic constraints after decoding and defaulting, failing
// the load closed on an invalid configuration (ADR-022 decision 1): a shed floor must
// name a level, and a source id must be usable AS the value it becomes.
//
// It used to say the source list was "left to the source loaders", and the loaders
// check only that a Type is known and that no source points at the platform broker
// — an Id was accepted whatever it said. That is what validateSourceIds closes.
func (c *EventSourcesConfiguration) Validate() error {
	// The shed floor names a level on the ADR-063 ladder (0..3); a value outside it
	// names no level. Fail the load closed rather than clamp — a floor of 7 is a
	// misconfiguration the operator must see, not one to silently reinterpret.
	if c.Contention.ManualFloor < 0 || c.Contention.ManualFloor > governance.MaxShedLevel {
		return fmt.Errorf("contention.manualFloor must be between 0 and %d (got %d)", governance.MaxShedLevel, c.Contention.ManualFloor)
	}
	return c.validateSourceIds()
}

// validateSourceIds refuses the ids that cannot serve as the value they become, and warns
// about the ones that merely should not have been chosen.
//
// 🔴 AN EventSource.Id IS NOT A LABEL, IT IS A STORED KEY. It is stamped onto every event
// this source ingests as the projected `source`, lands in device_states.source, and is read
// back by the asserted-presence reconciler through EXACT SQL EQUALITY. It is also a
// Prometheus label value on four counters. So an id is not free-form text that happens to be
// displayed; it is matched, grouped and reconciled on.
//
// ⚠️ AND THAT CUTS BOTH WAYS. Rejecting an id an operator is ALREADY RUNNING has the same
// effect as renaming it: this service refuses to start — taking down ALL ingest for the
// instance, since every source and the broker-presence tap live in this one process — and if
// they rename the id to get going again, every asserted device_states row filed under the old
// one is stranded, because the source-scoped reconciler will never match it and pre-GA there
// is no backfill.
//
// 🔴 SO BE HONEST ABOUT WHAT THE RESERVATION COSTS, because "it only rejects what was already
// broken" is NOT true and an earlier version of this comment said it was. Of the ids this
// turns away:
//
//   - exactly "lwm2m" IS already broken — it collides whole-value with the LwM2M
//     reconciler's own read, so the two sweep each other's rows today.
//   - "sparkplug" or "sparkplug:x" is broken only for an operator who SENDS COMMANDS; the
//     deny list makes those Undeliverable. A telemetry-only fleet on such an id works fine.
//   - "lwm2m:site-a" is broken by NOTHING today. The one transport-classified consumer denies
//     only Sparkplug, and the LwM2M reconciler matches the whole value, so this id is never
//     swept by it and never denied a command.
//
// 🔴 THAT ACCOUNTING IS WHAT SPLIT THE RULE IN TWO, and it is why only the first bullet
// refuses the load. The other two are groundwork for the allow list the stranded-SENT
// reconciler needs, where a collision stops being self-inflicted and starts making a foreign
// device look like a member. Groundwork is a defensible reason to reject a NEW configuration
// and a bad one for rejecting a RUNNING one: it spends the outage described above on a defect
// that does not exist yet. So those two now warn and start, and the reservation still reaches
// the operator — just at the cost of a log line rather than their ingest.
//
// This function is therefore the one place in the file that both fails closed and does not,
// deliberately. The test names say which is which.
func (c *EventSourcesConfiguration) validateSourceIds() error {
	seen := make(map[string]int, len(c.EventSources))
	for i, src := range c.EventSources {
		// An empty id projects an empty source, and device-state's projection deliberately
		// never lets an empty overwrite a non-empty. A device's FIRST event still creates its
		// row with an empty source — it is the merge path that refuses — so the result is not
		// "no source" but a source that can never be corrected by this gateway and can be
		// overwritten by any other, which is worse than either.
		if src.Id == "" {
			return fmt.Errorf("eventSources[%d].id must not be empty: it is stamped on every event as the projected source, and an empty source never updates the projection", i)
		}
		// 🔴 THE RESERVATION, AND IT IS DELIBERATELY ON THE CLASSIFICATION RATHER THAN ON THE
		// SPELLING. Every consumer that asks "what transport is this device on?" reduces the
		// source with transport.Of — cutting at the first ":" — so an id of "sparkplug" and
		// an id of "sparkplug:plant-a" are equally a Sparkplug device to all of them, while
		// "sparkplug-test" and "acme:line3" are equally not. Testing the reduction is
		// therefore both stricter and more permissive than a spelling rule, in exactly the
		// places that matter: it catches the qualified impostor a "no colons" rule would
		// miss, and it permits the operator's own namespaced id that such a rule would
		// reject for nothing.
		if name := transport.Of(src.Id); transport.IsMinted(name) {
			// 🔴 REFUSE WHAT IS BROKEN NOW; WARN ABOUT WHAT IS RESERVED FOR LATER. The two were
			// one rule and it made every id in the second class an upgrade that stops the
			// service — which for this process means ALL ingest for the instance, since every
			// source and the broker-presence tap live in it.
			//
			// The line between them is not a matter of degree. An id EQUAL to a name the
			// platform stamps bare shares a whole source value with rows the platform is
			// already writing, and the asserted-presence reconcilers match a source by plain
			// SQL equality — so the two sweep and re-file each other's rows, today, on every
			// pass. That is corruption in progress, and the fail-closed posture is right: an
			// operator in this state is already losing the presence data they think they have.
			//
			// Everything else in this class is groundwork. "lwm2m:site-a" is swept by nothing
			// and denied by nothing today; "sparkplug:plant-a" costs an operator only if they
			// send commands, and a telemetry-only fleet on it works. Taking those instances
			// down to enforce a rule that protects a list which has not landed spends an
			// outage on a defect that does not exist yet.
			//
			// ⚠️ THE REMEDY IS NAMED IN BOTH MESSAGES BECAUSE THE OBVIOUS ONE HAS A TRAP, and
			// the operator reads the string rather than this comment. Renaming the id starts
			// the service and silently orphans every presence row already filed under the old
			// name — so someone with live devices needs to know that before they type the new
			// id, not after.
			if transport.IsStampedBare(transport.Name(src.Id)) {
				return fmt.Errorf(
					"eventSources[%d].id %q collides with the platform's own: %q is the source value the %q transport files its device presence under, and the presence reconcilers match a source by exact equality — so this source and that transport re-file each other's rows on every pass. Choose another id — but note that if this source has been running, renaming it strands the device presence already recorded under the old id, which nothing backfills",
					i, src.Id, src.Id, name)
			}
			log.Warn().Int("index", i).Str("id", src.Id).Str("transport", string(name)).
				Msg("Event source id is RESERVED: it reads as a transport the platform mints, so every consumer that classifies a source will call this source's devices that transport. " +
					"Nothing refuses them today and the service is starting, but commands to them can be judged undeliverable, and a future allow list would treat a foreign transport's device as one of yours. " +
					"Rename it when you can — and note that renaming strands the device presence already recorded under the old id, which nothing backfills.")
		}
		// Two sources sharing an id are indistinguishable AFTER the fact: they project one
		// source value, merge into one Prometheus series, and reconcile as one emitter — so
		// each would re-file the other's asserted rows.
		if first, dup := seen[src.Id]; dup {
			return fmt.Errorf("eventSources[%d].id %q duplicates eventSources[%d]: two sources sharing an id are one source to everything downstream", i, src.Id, first)
		}
		seen[src.Id] = i
	}
	return nil
}
