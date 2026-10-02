// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventlimit"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// IngestLimiter is the shared, two-stage, per-tenant admission gate for a device-facing
// gateway ingest source (ADR-023, ADR-075 L2c). It fronts the durable emit path so an
// authenticated device that floods — the LwM2M exposure, where devices reach the socket
// directly — cannot spend unbounded pipeline CPU or unbounded downstream write volume, and
// cannot evade its tenant's ingest ceiling:
//
//   - STAGE 1, AllowMessage — a coarse per-tenant MESSAGE-rate gate charged ONCE per inbound
//     message BEFORE decode, so a message flood is shed before it costs a parse. It is the
//     only meter that sees every message, including undecodable garbage (a message that never
//     yields samples is metered nowhere else), which is why it is a separate bucket rather
//     than a pre-charge on the sample bucket.
//   - STAGE 2, AdmitSamples — a per-tenant READING-rate gate charged with the decoded sample
//     COUNT AFTER decode, so a slow trickle of enormous packs (which sails through a
//     per-message gate) is bounded by measurement VOLUME, the thing that actually reaches the
//     time-series store. It meters readings at the tenant's ingest ceiling itself, through
//     governance.ReadingCeiling — the same definition event-sources' JSON transports charge
//     their readings against, so a tenant's ceiling means the same number of readings on
//     every transport.
//
// LwM2M uses both stages (NewIngestLimiter). Sparkplug uses stage 2 alone
// (NewSampleLimiter): its session machine must observe every message, because sequence
// numbers and rebirth depend on it, so it has no message stage to shed at.
//
// Both stages are per-tenant token buckets (core.TenantRateLimiter): independent per tenant
// so one tenant's flood consumes only its own allowance, and LABEL-FREE — the buckets are
// keyed by tenant internally but NO per-tenant metric label is ever emitted (the ADR-023
// cardinality-DoS lesson: a device-driven metric labeled by an unverified tenant is an
// unbounded, attacker-influenceable cardinality vector). The shed counters carry no label.
//
// It is fail-safe: both buckets resolve their ceiling through the same resolver, which fails
// open to the platform default (never zero/unlimited) — a missing or unusable per-tenant
// override meters at the default, never removes the gate.
type IngestLimiter struct {
	message *core.TenantRateLimiter // stage 1: messages/sec per tenant; nil for a sample-only limiter
	sample  *core.TenantRateLimiter // stage 2: readings/sec per tenant, at the same ceiling
	metrics IngestLimiterMetrics
}

// IngestLimiterMetrics are the optional shed counters the limiter updates; a nil field is
// skipped so the limiter is usable in tests without a registry. NEITHER is labeled by tenant
// (the label-free guarantee is co-located with the mechanism so an adopter inherits it by
// construction). SamplesShed counts SAMPLES shed (the volume), not shed events.
type IngestLimiterMetrics struct {
	MessagesShed prometheus.Counter // inbound messages shed at the per-tenant message-rate ceiling
	SamplesShed  prometheus.Counter // decoded samples shed at the per-tenant reading ceiling
}

// NewIngestLimiter builds the two-stage limiter over a single ceiling resolver (so one cache
// / one authority query per TTL serves both buckets, and an override change retunes both).
// resolve returns a tenant's ingest ceiling — typically governance.TenantLimitResolver.Ceiling,
// or core.StaticCeiling when no authority is configured. It MUST fail safe (a
// missing/unusable override → the positive platform default), because a non-positive ceiling
// yields a bucket that admits nothing.
//
// The sample bucket meters readings at that same ceiling through governance.ReadingCeiling,
// which floors its burst at eventlimit.MaxReadingsPerEvent. AdmitSamples charges once per
// EVENT (at most that many samples; a message larger than one event is charged piece by
// piece), so every charge fits an idle bucket and a message is shed on sustained RATE, never
// permanently on its size.
//
// unresolved, when non-nil, is counted for each MESSAGE admitted at the platform default for
// want of the tenant's own ceiling (core.WithUnresolvedAdmissions). It rides on the message
// stage only: the sample stage sees the same messages again after decode, so counting there
// too would count one message twice.
func NewIngestLimiter(resolve core.TenantCeilingResolver, metrics IngestLimiterMetrics,
	unresolved func(core.CeilingSource)) *IngestLimiter {
	var messageOpts []core.TenantRateLimiterOption
	if unresolved != nil {
		messageOpts = append(messageOpts, core.WithUnresolvedAdmissions(unresolved))
	}
	return &IngestLimiter{
		message: core.NewTenantRateLimiter(resolve, messageOpts...),
		sample:  core.NewTenantRateLimiter(governance.ReadingCeiling(resolve)),
		metrics: metrics,
	}
}

// NewSampleLimiter builds a stage-2-only limiter for a source with no message stage
// (Sparkplug). Its sample bucket is the one NewIngestLimiter builds. unresolved, when
// non-nil, is counted on the sample stage instead — there is no message stage to count it
// on — once per admitted CHARGE (one event's worth of samples), so a tenant metered at the
// platform default is still visible on the shared counter. AllowMessage panics on it.
func NewSampleLimiter(resolve core.TenantCeilingResolver, metrics IngestLimiterMetrics,
	unresolved func(core.CeilingSource)) *IngestLimiter {
	var sampleOpts []core.TenantRateLimiterOption
	if unresolved != nil {
		sampleOpts = append(sampleOpts, core.WithUnresolvedAdmissions(unresolved))
	}
	return &IngestLimiter{
		sample:  core.NewTenantRateLimiter(governance.ReadingCeiling(resolve), sampleOpts...),
		metrics: metrics,
	}
}

// AllowMessage is STAGE 1: it reports whether one inbound message from the tenant may proceed
// to decode, consuming one token from the tenant's message bucket. Call it once per inbound
// device message, BEFORE decode. A shed message is counted and (at debug) logged with the
// tenant as a log FIELD — never a metric label.
//
// Charge discipline for the LwM2M Notify path: a message that reaches this gate is charged
// whether or not it later decodes — an undecodable, unknown-format, or zero-sample message is
// still a message the tenant sent, and this is the only bucket that sees it. A protocol-state
// message that is NOT tenant telemetry (e.g. an RFC 7641 terminal notification) must be
// handled before this gate, so it is neither charged nor shed.
//
// It panics on a limiter built by NewSampleLimiter, which has no message stage: a caller
// that asks has been wired to the wrong constructor, and admitting would hide it.
func (l *IngestLimiter) AllowMessage(tenant string) bool {
	if l.message == nil {
		panic("adapter.IngestLimiter.AllowMessage: this limiter was built without a message stage (NewSampleLimiter)")
	}
	if l.message.Allow(tenant) {
		return true
	}
	incr(l.metrics.MessagesShed, 1)
	if log.Debug().Enabled() {
		log.Debug().Str("tenant", tenant).Msg("Shed an inbound message at the per-tenant ingest message-rate ceiling.")
	}
	return false
}

// AllowSamples is STAGE 2: it reports whether a decoded batch of n samples from the tenant may
// be emitted, consuming n tokens from the tenant's sample bucket. Call it after decode, before
// the durable emit, once per event's worth of samples (n ≤ eventlimit.MaxReadingsPerEvent —
// see NewIngestLimiter). A non-positive n admits and charges nothing. A shed batch counts n
// against SamplesShed (the volume shed) and is logged at debug with the tenant as a FIELD.
func (l *IngestLimiter) AllowSamples(tenant string, n int) bool {
	if n <= 0 {
		return true
	}
	if l.sample.AllowN(tenant, n) {
		return true
	}
	incr(l.metrics.SamplesShed, n)
	if log.Debug().Enabled() {
		log.Debug().Str("tenant", tenant).Int("samples", n).
			Msg("Shed a decoded sample batch at the per-tenant ingest ceiling (counted in readings).")
	}
	return false
}

// AdmitSamples is STAGE 2 for a decoded message that may be larger than one event. It charges
// the tenant's sample bucket once per event's worth of samples — consecutive pieces of at most
// eventlimit.MaxReadingsPerEvent, the same pieces Emitter.Emit publishes as events — and stops
// at the first piece the bucket refuses. It returns how many LEADING samples were admitted:
// always a whole number of pieces (so the admitted prefix splits into exactly the events
// charged), n when all of it fits, 0 when none does. Every sample not admitted is counted on
// SamplesShed, the refused piece by AllowSamples and the pieces after it, which were never
// offered, here.
//
// 🔴 WHY PER EVENT: governance.ReadingCeiling floors the burst at MaxReadingsPerEvent, so
// every charge fits an idle bucket. One charge for the whole message would be refused every
// time for a message larger than the tenant's burst — shed permanently, not on rate — which
// is the one thing the floor exists to prevent.
func (l *IngestLimiter) AdmitSamples(tenant string, n int) int {
	admitted := 0
	for admitted < n {
		piece := min(n-admitted, eventlimit.MaxReadingsPerEvent)
		if !l.AllowSamples(tenant, piece) {
			incr(l.metrics.SamplesShed, n-admitted-piece)
			break
		}
		admitted += piece
	}
	return admitted
}
