// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"math"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// DefaultSamplesPerMessage scales a tenant's message-rate ceiling into its sample-rate
// ceiling: the sample budget is the message ceiling times this factor. It exists because
// one message legitimately carries several measurement samples, so metering samples at the
// raw message rate would shed a compliant multi-sample fleet. A factor of 25 gives generous
// headroom over a typical IPSO Notify (~5-20 numeric resources) while still bounding the
// pathological case — a slow trickle of enormous packs — that the message-rate gate alone
// cannot see (it counts messages, not samples). It is a platform constant, never per-tenant:
// a per-tenant sample ceiling is the deferred dedicated-dimension follow-up (ADR-023).
const DefaultSamplesPerMessage = 25

// IngestLimiter is the shared, two-stage, per-tenant admission gate for a device-facing
// ingest source (ADR-023, ADR-075 L2c). It fronts the durable emit path so an authenticated
// device that floods — the LwM2M exposure, where devices reach the socket directly — cannot
// spend unbounded pipeline CPU or unbounded downstream write volume, and cannot evade its
// tenant's ingest ceiling:
//
//   - STAGE 1, AllowMessage — a coarse per-tenant MESSAGE-rate gate charged ONCE per inbound
//     message BEFORE decode, so a message flood is shed before it costs a parse. It is the
//     only meter that sees every message, including undecodable garbage (a message that never
//     yields samples is metered nowhere else), which is why it is a separate bucket rather
//     than a pre-charge on the sample bucket.
//   - STAGE 2, AllowSamples — a per-tenant SAMPLE-rate gate charged with the decoded sample
//     COUNT AFTER decode, so a slow trickle of enormous packs (which sails through a
//     per-message gate) is bounded by measurement VOLUME, the thing that actually reaches the
//     time-series store.
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
//
// It lives in adapter (not in any one protocol's package) so LwM2M wires it now and Sparkplug
// can adopt it later without re-deriving the label-free/fail-safe discipline.
type IngestLimiter struct {
	message *core.TenantRateLimiter // stage 1: messages/sec per tenant
	sample  *core.TenantRateLimiter // stage 2: samples/sec per tenant (message ceiling × factor)
	metrics IngestLimiterMetrics
}

// IngestLimiterMetrics are the optional shed counters the limiter updates; a nil field is
// skipped so the limiter is usable in tests without a registry. NEITHER is labeled by tenant
// (the label-free guarantee is co-located with the mechanism so an adopter inherits it by
// construction). SamplesShed counts SAMPLES shed (the volume), not shed events.
type IngestLimiterMetrics struct {
	MessagesShed prometheus.Counter // inbound messages shed at the per-tenant message-rate ceiling
	SamplesShed  prometheus.Counter // decoded samples shed at the per-tenant sample-rate ceiling
}

// NewIngestLimiter builds the two-stage limiter over a single ceiling resolver (so one cache
// / one authority query per TTL serves both buckets, and an override change retunes both).
// resolve returns a tenant's (messagesPerSecond, burst) — typically
// governance.TenantLimitResolver.Resolve, or a flat platform-default closure when no
// authority is configured. It MUST fail safe (a missing/unusable override → the positive
// platform default), because a non-positive ceiling yields a bucket that admits nothing.
//
// samplesPerMessage scales the message ceiling into the sample ceiling (see
// DefaultSamplesPerMessage). sampleBurstFloor floors the sample bucket's burst so a single
// decoded batch no larger than the floor always fits — the caller passes its per-message
// sample cap here (the SAME symbol it truncates decode at), so AllowN sheds on sustained
// RATE and never permanently on a single compliant batch striking the burst edge.
func NewIngestLimiter(resolve func(tenant string) (float64, int), samplesPerMessage float64,
	sampleBurstFloor int, metrics IngestLimiterMetrics) *IngestLimiter {
	if samplesPerMessage <= 0 {
		samplesPerMessage = DefaultSamplesPerMessage
	}
	return &IngestLimiter{
		message: core.NewTenantRateLimiter(resolve),
		sample: core.NewTenantRateLimiter(func(tenant string) (float64, int) {
			rps, burst := resolve(tenant)
			sampleBurst := satMulInt(burst, samplesPerMessage)
			if sampleBurst < sampleBurstFloor {
				sampleBurst = sampleBurstFloor
			}
			if sampleBurst < 1 {
				// A caller that passes a non-positive sampleBurstFloor (the shared-mechanism
				// footgun the LwM2M path avoids by passing MaxSamplesPerNotify) must not get a
				// zero-burst bucket, which admits NOTHING — the fail-safe floor is a real limit,
				// never a silent black-hole.
				sampleBurst = 1
			}
			return rps * samplesPerMessage, sampleBurst
		}),
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
func (l *IngestLimiter) AllowMessage(tenant string) bool {
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
// the durable emit. A non-positive n admits and charges nothing. A shed batch counts n against
// SamplesShed (the volume shed) and is logged at debug with the tenant as a FIELD.
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
			Msg("Shed a decoded sample batch at the per-tenant ingest sample-rate ceiling.")
	}
	return false
}

// satMulInt multiplies an int count by a positive float factor, saturating at math.MaxInt
// instead of overflowing to a negative value. A per-tenant burst override may be as large as
// math.MaxInt (governance floors only non-positive values), and burst × factor would overflow
// int — handing core.TenantRateLimiter a NEGATIVE burst, a bucket that admits NOTHING, which
// would silently black-hole that tenant's samples while presence and stage 1 kept working.
func satMulInt(n int, factor float64) int {
	if n <= 0 || factor <= 0 {
		return 0
	}
	scaled := float64(n) * factor
	if scaled >= float64(math.MaxInt) {
		return math.MaxInt
	}
	return int(scaled)
}
