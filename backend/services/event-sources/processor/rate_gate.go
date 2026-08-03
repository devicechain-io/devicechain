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
type RateGate func(source string, tenant string, sentAt time.Time) bool

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
	return func(source string, tenant string, sentAt time.Time) bool {
		if tenantDeleted(tenant) {
			if onRefused != nil {
				onRefused(source, tenant)
			}
			return false
		}
		return next(source, tenant, sentAt)
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

// NewRateGate builds the ingest gate over two per-tenant limiters, routing each
// message to the one that meters the clock it belongs to. onShed, when non-nil,
// is called for each shed message so the caller can account for it.
//
// # Why two limiters rather than two clocks on one
//
// This separation is the correctness property, not an optimization. A token bucket
// accrues from the last timestamp it saw, so a single bucket fed BOTH wall-clock
// arrivals and hours-old send times re-accrues from a stale mark on every jump
// forward to now — refilling to burst, which the following rewind then spends.
// That mints roughly `burst` admissions per interleave, so a tenant ingesting over
// HTTP while their capture backlog drains can pace live posts against the drain
// and bypass their ceiling outright. It is not a bounded rounding error: the
// minting scales with consumer lag, and on the single-bucket design this replaces,
// one second of lag was enough to turn a 100/s ceiling into ~2000 admissions.
//
// Routing keeps each bucket on exactly ONE clock. The live bucket only ever sees
// now; the backlog bucket only ever sees send times, which are monotonic in stream
// order. Neither can rewind, so neither can mint.
//
// The residual cost is that a tenant who is simultaneously live AND draining a
// genuine backlog may be admitted up to twice their ceiling until the drain
// catches up. That is bounded and predictable, and smaller than the exposure the
// platform already carries from running N replicas with independent limiters.
func NewRateGate(live *core.TenantRateLimiter, backlog *core.TenantRateLimiter,
	onShed func(source string, tenant string)) RateGate {
	return func(source string, tenant string, sentAt time.Time) bool {
		limiter, when := live, time.Time{}
		if !sentAt.IsZero() && time.Since(sentAt) > BacklogThreshold {
			limiter, when = backlog, sentAt
		}
		if limiter.AllowAt(tenant, when) {
			return true
		}
		if onShed != nil {
			onShed(source, tenant)
		}
		return false
	}
}
