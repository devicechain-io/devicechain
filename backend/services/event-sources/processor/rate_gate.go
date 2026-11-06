// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventlimit"
	"github.com/rs/zerolog/log"
)

// RateGate meters one inbound message against its tenant's ingest ceiling
// (ADR-023), returning true when it may proceed and false when it must be shed.
//
// sentAt is when the TENANT SENT the message, and passing it correctly is the
// whole point of the parameter existing. A gate metered on when the message
// ARRIVED measures how fast this service is currently processing, which equals
// the tenant's send rate only while the service is keeping up. A source draining
// a durable backlog is by definition not keeping up: after an outage the ingest
// capture stream (ADR-030) holds every message the broker PUBACKed while we were
// down, and it drains at fetch speed. Metered on arrival that backlog looks like
// one vast burst from every tenant at once and is almost entirely shed — the
// platform discarding messages it had already told the devices were safe.
//
// So each source passes the send time it actually knows:
//   - the capture-stream source passes the broker's append time — the BROKER's own
//     timestamp, stamped as it wrote the message and before it PUBACKed the device,
//     never a value the device supplies. So no device clock can influence the gate,
//     and a device that buffers locally and dumps on reconnect is still metered as
//     the burst it is;
//   - the HTTP and external-MQTT sources pass the zero time, meaning "now", which
//     is correct because both admit a message as it arrives with no durable
//     backlog behind them.
//
// A zero sentAt is read as now by core.TenantRateLimiter.AllowAt, so "I do not
// know when this was sent" degrades to today's behaviour rather than to unmetered.
//
// 🔴 A NON-ZERO sentAt IS A BROKER TIMESTAMP, AND THE MESSAGE IS METERED ON THAT TIMELINE. The
// gate does not judge age to choose a bucket: every message the broker stamped is charged to ONE
// allowance (the backlog limiter) at the time the broker stored it, which MeterAt has already
// capped at now. A stall of any length is therefore irrelevant to the metering, since a tenant
// within its ceiling is admitted in full on the timeline it sent on. Only the contention shed
// (ADR-063) looks at age: see FreshAge.
//
// redelivery says the broker is re-offering a message it has already delivered
// once. 🔴 IT IS A PARAMETER RATHER THAN A CONDITION AT THE CALL SITE, and that
// is the whole reason it exists. A redelivery is exempt from METERING and from
// nothing else — but a gate is a composition of independent refusals, so a
// caller that skips the gate to skip the metering silently skips every other
// refusal composed in front of it. That is not hypothetical: this parameter
// replaces a `msg.NumDelivered <= 1 &&` guard on the capture source that was
// reasoned about purely in metering terms and, once the lifecycle refusal was
// composed on, quietly stopped refusing a deleted tenant's redeliveries.
//
// Passing it in lets each layer answer for itself: the metering layer exempts a
// redelivery, the lifecycle layer does not, and a layer added later has to make
// its own decision rather than inherit one made for a different reason.
//
// origin says whether the tenant string came from a credential the platform checked.
// It is a parameter for the same reason redelivery is: the transport knows, and the
// gate — ONE gate for every transport — decides what that means.
type RateGate func(source string, tenant string, sentAt time.Time, redelivery bool, origin Origin) bool

// Origin says whether a message's tenant was established by a credential the platform
// checked. It decides only which bucket the message is metered in; every origin is
// metered, and every origin passes the lifecycle refusal.
type Origin uint8

const (
	// OriginAuthenticated: the tenant came from a source the platform or its operator
	// vouches for — the capture stream, which the platform broker writes only for a
	// device whose connection the auth callout bound to that tenant, or an MQTT broker
	// source the operator configured, whose topic tree the operator chose to trust. Such
	// a tenant always gets its own allowance.
	OriginAuthenticated Origin = iota + 1
	// OriginUntrusted: the tenant is a string the sender chose before any credential was
	// checked — the HTTP ingest path segment. It is metered in the HTTP allowance, never
	// in the one authenticated traffic spends. Within that allowance such a name gets a
	// bucket of its own only once the ceiling authority has confirmed it exists, or from
	// a fixed pool; past the pool it shares one bucket
	// (core.TenantRateLimiter.AllowUntrusted).
	//
	// The zero Origin is neither constant and is treated as untrusted, so a transport
	// that forgets to say is bounded, never unbounded.
	OriginUntrusted
	// OriginIntegration: the request came through an inbound integration (a LoRaWAN network server
	// or a webhook) whose bearer token the pre-check has just proven, so the tenant is a tenant that
	// exists. It is metered in allowances of its own, at the tenant's ceiling: it never draws on the
	// allowance the platform MQTT broker's captured traffic spends (shedding a captured message is
	// the permanent loss of data the broker already acknowledged), and never on the pool the
	// unauthenticated HTTP path shares.
	OriginIntegration
)

// RefuseDeletedTenants composes the ADR-077 lifecycle refusal in front of an ingest
// gate, so a tenant an operator has deleted stops ingesting on every transport at once.
//
// 🔴 It is composed HERE, onto the gate, rather than added to each source, and that is
// the whole point. Every inbound source — HTTP, external MQTT, and the platform-broker
// capture stream — already calls exactly one admission hook before it reads a body, and
// they are the only three places a tenant string is known before the write. Adding a
// second check to each would mean a fourth transport arrives one day with the rate gate
// wired (it is a constructor argument, so it cannot be forgotten) and the lifecycle
// check missing. Composed, there is nothing to remember.
//
// This front NEEDS its own gate. The Registrar's gate covers LwM2M and Sparkplug, which
// resolve a device by external id; nothing here goes through it. MQTT and NATS connects
// are separately stopped by the broker auth-callout — but that gates the CONNECT, and a
// session established before the delete keeps its minted JWT for its full TTL (12h by
// default), which is far longer than this gate's 60s cache. HTTP has no transport auth
// at all (credentials ride in the event body), so before this there was nothing.
//
// A refused message is shed exactly as a rate-limited one is, down to the status code
// the HTTP source returns. That is deliberate: answering differently would tell an
// unauthenticated caller which tenants exist and what has happened to them, and the
// device's behaviour on either answer — back off and retry — is the behaviour we want.
// onRefused counts it apart from a rate shed, because the two mean opposite things to an
// operator reading the metric.
func RefuseDeletedTenants(tenantDeleted func(string) bool, next RateGate, onRefused func(source, tenant string)) RateGate {
	if tenantDeleted == nil {
		return next // gate unconfigured; see governance.NewTenantLifecycleGate
	}
	return func(source string, tenant string, sentAt time.Time, redelivery bool, origin Origin) bool {
		// Deliberately NOT exempt on a redelivery. Metering is exempt because the
		// message already paid on delivery 1; this refusal is about whether the
		// tenant may be written to AT ALL, and that answer can have changed since.
		if tenantDeleted(tenant) {
			if onRefused != nil {
				onRefused(source, tenant)
			}
			return false
		}
		return next(source, tenant, sentAt, redelivery, origin)
	}
}

// FreshAge is how old a broker-stamped message may be and still count as NEW INGRESS rather than
// a drained backlog. The ADR-063 contention shed applies to fresh messages only: a message the
// broker stored longer ago than this was durably accepted (ADR-030) before this pod ever read it,
// and shedding it would retroactively discard accepted data, which ADR-063 forbids. It sits well
// above the lag of a caught-up capture consumer (milliseconds) and far below any stall worth
// recovering from, so the steady state is entirely fresh and a drain is entirely not.
const FreshAge = 250 * time.Millisecond

// maxShedUnits caps what one fresh message may be charged for the shed, so a hard drop (a factor
// of zero) can never overflow when multiplied by a reading count, and always exceeds any burst.
const maxShedUnits = 1 << 20

// ShedUnits says how many tokens one fresh message of the tenant costs under the ADR-063 shed
// factor: 1 at no contention or for a class that is never shed, ceil(1/factor) otherwise, and
// maxShedUnits for the hard drop. Charging 1/factor from a bucket of (rate, burst) is the same
// as charging 1 from a bucket of (rate*factor, burst*factor), which is how the live limiters
// shed, but it lets a message be shed without being metered on a different bucket than the
// backlog it may be interleaved with.
type ShedUnits func(tenant string) int

// GateOption configures NewRateGate and NewReadingGate.
type GateOption func(*gateOptions)

type gateOptions struct{ shedUnits ShedUnits }

// WithShedUnits gives the gate the contention shed it applies to fresh broker-stamped messages.
// Without it nothing is shed by contention.
func WithShedUnits(u ShedUnits) GateOption { return func(o *gateOptions) { o.shedUnits = u } }

func buildGateOptions(opts []GateOption) gateOptions {
	var o gateOptions
	for _, f := range opts {
		f(&o)
	}
	return o
}

// unitsFor is the number of tokens a charge of one costs: the shed cost for a fresh authenticated
// broker-stamped message, 1 for everything else (a backlog message is never shed by contention).
func (o gateOptions) unitsFor(tenant string, sentAt time.Time, origin Origin) int {
	if o.shedUnits == nil || origin != OriginAuthenticated || sentAt.IsZero() || time.Since(sentAt) >= FreshAge {
		return 1
	}
	return min(max(o.shedUnits(tenant), 1), maxShedUnits)
}

// NewRateGate builds the ingest gate over three per-tenant limiters, routing each
// message to the one that meters it. onShed, when non-nil, is called for each shed
// message so the caller can account for it. A nil limiter is a construction error and
// panics: a gate missing one of its allowances cannot meter the traffic routed to it.
//
// A message whose origin is not OriginAuthenticated is admitted through
// untrusted.AllowUntrustedN: always at now (an untrusted transport has no durable backlog
// behind it), and in a bounded pool of buckets unless its tenant is confirmed. It never
// reaches the live or the backlog limiter.
//
// # Why four limiters
//
// A fourth meters inbound integrations (OriginIntegration) apart from the other three, for the
// reason the HTTP one is apart from the live one: an integration's traffic must not be able to
// spend what the platform broker's captured traffic spends. It differs from the HTTP limiter in
// what it is spared: its tenant is proven by the pre-check, so its buckets are confirmed ones and
// not the bounded pool unproven names share.
//
// The first three:
//
// The live and backlog limiters meter authenticated traffic on two clocks; the untrusted
// limiter meters what HTTP ingest admits.
//
// HTTP names its tenant in a path segment BEFORE any credential is checked: the device
// credential rides in the event body and is checked asynchronously, downstream, after
// the message has been admitted. So anyone who can reach the HTTP port and knows a
// tenant's name can post as that tenant. Had those posts spent the allowance the
// tenant's authenticated traffic spends, such a caller could exhaust it and shed the
// tenant's captured MQTT telemetry — and shedding a capture-stream message means acking
// it away, the permanent loss of data the broker already PUBACKed to the device. With
// an allowance of its own, what such a caller can exhaust is the tenant's HTTP
// allowance, and nothing else. That part is unavoidable while HTTP admits before it
// authenticates.
//
// # Why the live and backlog limiters are two rather than two clocks on one
//
// A single bucket fed BOTH wall-clock arrivals and hours-old send times cannot meter
// either correctly. On the single-bucket design this replaced, the bucket's clock
// rewound on every backlog message and re-accrued on every live one, minting roughly
// `burst` admissions per interleave (one second of lag turned a 100/s ceiling into
// ~2000 admissions). core.TenantRateLimiter now keeps each bucket's clock from going
// backwards, so that interleave can no longer MINT — but it would still admit wrongly
// in the other direction: once a live post has moved the bucket's clock to now, every
// backlog message is charged at now too, and a compliant drain is shed as if it had
// all arrived at once. A live post must not pace against a drain's timeline, nor a
// drain against a live post's.
//
// Routing keeps each bucket on the clock it measures. The live bucket only ever sees
// now; the backlog bucket only ever sees broker append times. Those are NOT monotonic:
// the append time is stamped by the stream leader's wall clock, so it steps backwards
// on a leader change between servers whose clocks disagree, or on an NTP step. A
// backwards step costs over-shedding under the limiter's mark (the older times are
// charged at the latest time the bucket has seen), never minting.
//
// # The residual
//
// Each limiter resolves the tenant's ceiling independently, so the ceiling is a ceiling
// per allowance, not per tenant. Everything the platform broker stamped (live traffic and a
// drained backlog alike) shares ONE allowance, the backlog limiter, so a drain never doubles a
// tenant; the live limiter meters only what has no broker timestamp (broker presence). So a tenant
// also sending presence may be admitted up to twice its ceiling; one also sending over HTTP, up
// to one ceiling more, and one also posting through an inbound integration, one more again —
// four times its ceiling per replica at worst. That is bounded and predictable, and it multiplies with the exposure the
// platform already carries from running N replicas with independent limiters. The same
// holds, allowance by allowance, for the readings NewReadingGate charges.
func NewRateGate(live, backlog, untrusted, integration *core.TenantRateLimiter,
	onShed func(source string, tenant string), opts ...GateOption) RateGate {
	o := buildGateOptions(opts)
	if live == nil || backlog == nil || untrusted == nil || integration == nil {
		panic("processor.NewRateGate: the live, backlog, untrusted and integration limiters are all required")
	}
	return func(source string, tenant string, sentAt time.Time, redelivery bool, origin Origin) bool {
		// A redelivery already paid for its admission on delivery 1 — the broker is
		// re-offering it because the publish failed and the settler deliberately left
		// it unacked, not because the tenant sent anything new.
		//
		// Metering it again is not merely double-charging. A shed message is
		// ack-dropped, so re-metering converts a TRANSIENT downstream failure into
		// permanent loss of a message the broker already PUBACKed. And it bites in the
		// window that matters most: a post-outage drain runs the tenant's bucket at the
		// ceiling, so a redelivery arrives precisely when there is no token left to pay
		// with.
		//
		// The exemption is about double-charging, not about the bucket's clock: a
		// redelivery carries an older send time than messages already admitted, and the
		// limiter charges it at the latest time the bucket has seen, so metering it would
		// spend a token the tenant already paid and could never mint one.
		if redelivery {
			return true
		}
		if admitN(live, backlog, untrusted, integration, tenant, sentAt, origin, o.unitsFor(tenant, sentAt, origin)) {
			return true
		}
		if onShed != nil {
			onShed(source, tenant)
		}
		return false
	}
}

// MeterAt decides, for one broker-stamped message, the time it is metered at: the time the broker
// stored it, capped at now, or the zero time (meaning now, on the live limiter) when the source
// has no broker timestamp. It is computed once, before the first stage, and handed to both gates,
// so the message stage and the reading stage charge the same timeline.
//
// 🔴 THE CAP IS WHAT MAKES A BROKER CLOCK AHEAD OF THIS POD HARMLESS. The append time is stamped
// by the stream leader's wall clock, which can run ahead of this pod's. Left alone, such a time
// would move the backlog bucket's clock into the future and let the tenant mint tokens as the
// real clock caught up. Charged at min(appendTime, now) it is metered as live traffic. A clock
// BEHIND this pod's is indistinguishable from a backlog and is metered on its own consistent
// timeline, with the contention shed skipped for it (see FreshAge).
type MeterAt func(tenant string, sentAt time.Time) time.Time

// NewMeterAt builds the metering time decision.
func NewMeterAt() MeterAt {
	return func(_ string, sentAt time.Time) time.Time {
		if sentAt.IsZero() {
			return time.Time{}
		}
		if now := time.Now(); sentAt.After(now) {
			return now
		}
		return sentAt
	}
}

// admitN routes a charge of n units to the limiter that meters it, and is the one routing
// rule both gates share (see NewRateGate for why there are three limiters): an untrusted
// origin to the untrusted limiter, always at now; an authenticated message with a non-zero time
// (one MeterAt judged backlog) to the backlog limiter at that time; everything else to the live limiter at now.
func admitN(live, backlog, untrusted, integration *core.TenantRateLimiter, tenant string, sentAt time.Time,
	origin Origin, n int) bool {
	if origin == OriginIntegration {
		// Always at now: an integration request is admitted as it arrives, with no durable
		// backlog behind it. A confirmed bucket, because the pre-check proved the tenant.
		return integration.AllowNAt(tenant, time.Time{}, n)
	}
	if origin != OriginAuthenticated {
		return untrusted.AllowUntrustedN(tenant, n)
	}
	// sentAt is already the decision (MeterAt): non-zero is a broker timestamp, metered at that time.
	if !sentAt.IsZero() {
		return backlog.AllowNAt(tenant, sentAt, n)
	}
	return live.AllowNAt(tenant, time.Time{}, n)
}

// ReadingGate meters a DECODED message's readings against its tenant's ingest ceiling,
// returning true when it may proceed and false when it must be shed. It is the second
// stage of ingest admission: RateGate charges each message once before it is decoded, so
// a flood is shed before it costs a parse; this charges what the message turned out to
// carry, so a tenant cannot send 256 readings for the price of one.
//
// It takes the same sentAt, redelivery and origin as RateGate and routes them the same way
// (admitN), with the same redelivery exemption: delivery 1 paid. readings <= 0 admits and
// charges nothing.
//
// It carries no lifecycle refusal. That is RateGate's: it runs on every delivery before
// decode, and a message it refuses never reaches this stage.
type ReadingGate func(source, tenant string, sentAt time.Time, redelivery bool, origin Origin, readings int) bool

// NewReadingGate builds the reading stage over three READING limiters (live, backlog and
// untrusted), each built over governance.ReadingCeiling so a single full event always fits
// an idle bucket. They must be separate from the message limiters: one bucket charged both
// would spend a tenant's message allowance on its readings. onShed, when non-nil, receives
// each shed message's reading count. A nil limiter panics, as in NewRateGate.
func NewReadingGate(live, backlog, untrusted, integration *core.TenantRateLimiter,
	onShed func(source, tenant string, readings int), opts ...GateOption) ReadingGate {
	o := buildGateOptions(opts)
	if live == nil || backlog == nil || untrusted == nil || integration == nil {
		panic("processor.NewReadingGate: the live, backlog, untrusted and integration reading limiters are all required")
	}
	return func(source, tenant string, sentAt time.Time, redelivery bool, origin Origin, readings int) bool {
		if redelivery || readings <= 0 {
			return true
		}
		if admitN(live, backlog, untrusted, integration, tenant, sentAt, origin, readings*o.unitsFor(tenant, sentAt, origin)) {
			return true
		}
		if onShed != nil {
			onShed(source, tenant, readings)
		}
		return false
	}
}

// readingsOf is what a decoded payload is charged: model.ReadingCount, and never less than
// one, since even an event with no readings is an event the pipeline stores and evaluates.
//
// A kind model.ReadingCount does not know is charged the most one event may carry
// (eventlimit.MaxReadingsPerEvent) and logged, never charged nothing. The JSON decoder
// already refuses such a kind (model.CheckReadingCount), so this is reached only by a
// decoder that does not, which is a defect to fix rather than a path traffic takes.
func readingsOf(payload interface{}) int {
	_, n, ok := model.ReadingCount(payload)
	if !ok {
		log.Error().Str("payload", fmt.Sprintf("%T", payload)).
			Msg("Decoded payload has no reading count; charging the per-event maximum. Give model.ReadingCount a case for it.")
		return eventlimit.MaxReadingsPerEvent
	}
	return max(n, 1)
}
