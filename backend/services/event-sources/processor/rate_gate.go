// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"time"

	core "github.com/devicechain-io/dc-microservice/core"
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

// BacklogThreshold is how far behind a message must be before it is metered as
// BACKLOG on the send timeline rather than as live traffic at now.
//
// It sits well above the lag of a caught-up capture consumer (milliseconds) and
// well below any outage worth recovering from, so the steady state routes entirely
// to the live limiter and the backlog limiter engages only when there is a real
// backlog. Between those two scales the exact value is not sensitive: a message
// either clearly is or clearly is not part of one.
const BacklogThreshold = 5 * time.Second

// NewRateGate builds the ingest gate over three per-tenant limiters, routing each
// message to the one that meters it. onShed, when non-nil, is called for each shed
// message so the caller can account for it. A nil limiter is a construction error and
// panics: a gate missing one of its allowances cannot meter the traffic routed to it.
//
// A message whose origin is not OriginAuthenticated is admitted through
// untrusted.AllowUntrusted: always at now (an untrusted transport has no durable backlog
// behind it), and in a bounded pool of buckets unless its tenant is confirmed. It never
// reaches the live or the backlog limiter.
//
// # Why three limiters
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
// per allowance, not per tenant. A tenant simultaneously live AND draining a genuine
// backlog may be admitted up to twice its ceiling until the drain catches up; one also
// sending over HTTP, up to one ceiling more — three times its ceiling per replica at
// worst. That is bounded and predictable, and it multiplies with the exposure the
// platform already carries from running N replicas with independent limiters.
func NewRateGate(live, backlog, untrusted *core.TenantRateLimiter,
	onShed func(source string, tenant string)) RateGate {
	if live == nil || backlog == nil || untrusted == nil {
		panic("processor.NewRateGate: the live, backlog and untrusted limiters are all required")
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
		var admitted bool
		switch {
		case origin != OriginAuthenticated:
			admitted = untrusted.AllowUntrusted(tenant)
		case !sentAt.IsZero() && time.Since(sentAt) > BacklogThreshold:
			admitted = backlog.AllowAt(tenant, sentAt)
		default:
			admitted = live.AllowAt(tenant, time.Time{})
		}
		if admitted {
			return true
		}
		if onShed != nil {
			onShed(source, tenant)
		}
		return false
	}
}
