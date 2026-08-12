// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-microservice/natsauth"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// Emitter writes a presence transition onto the inbound-events stream. Satisfied by
// adapter.Emitter; an interface here so the tap's tests observe what was emitted
// rather than what was published.
type Emitter interface {
	EmitPresence(ctx context.Context, tenant, source, deviceToken string, ev adapter.PresenceEvent) error
}

// Gate is the service's shared ingest admission gate (processor.RateGate): the
// ADR-077 refusal for a deleted tenant composed over the ADR-023 per-tenant ceiling.
//
// 🔑 THE TAP GOES THROUGH THE SAME GATE AS EVERY OTHER TRANSPORT, and both halves
// matter here for reasons that are specific to this source:
//
//   - Lifecycle: a purging tenant's devices keep their callout-minted JWTs for up to
//     12h (natsauth.DefaultUserJWTTTL), so their connects and disconnects keep arriving
//     well after the tenant was deleted. Emitting them writes StateChange rows into a
//     tenant being erased, and re-seeds an entity root the purge has already removed.
//   - Ceiling: connection churn is device-controlled and free. A device reconnecting
//     in a loop mints a distinct session per connect — hence a distinct dedup id per
//     connect — so nothing downstream collapses them. Without the ceiling that is an
//     unmetered durable-write amplifier that the ingest limiter never sees, because
//     none of it arrives as ingest.
type Gate func(source, tenant string, sentAt time.Time, redelivery bool) bool

// Metrics are the tap's operator signals. Label sets are deliberately small and
// entirely OURS: no tenant label, and no broker-supplied text (ADR-023 G.3) — the
// disconnect reason travels on the event, where cardinality is not a memory cost.
type Metrics struct {
	// Emitted counts presence events written, labelled by state.
	Emitted *prometheus.CounterVec
	// Skipped counts advisories that produced nothing, labelled by SkipReason. A
	// healthy instance's largest bucket is not_a_device_connection — every service
	// connection lands there — so this is a shape to read, not a number to alarm on.
	Skipped *prometheus.CounterVec
	// Refused counts transitions the admission gate rejected (deleted tenant, or over
	// the tenant's ceiling). Distinct from Skipped because a refusal is the platform
	// declining to act, not the tap deciding an advisory was not about a device.
	Refused prometheus.Counter
	// Failed counts emission errors — the write to the stream failed. These are the
	// ones that lose a transition outright, and the reconciler is what recovers them.
	Failed prometheus.Counter
	// RegressedSessions counts transitions the projection is expected to reject as
	// stale because the session id went backwards. See SessionIDFrom's residual
	// hazard: a reconnect onto a broker node with a trailing clock. The tap cannot see
	// the projection's stored session, so this counts only what it can know — a
	// transition whose session is lower than the last one this replica emitted for the
	// same device — which makes the hazard visible without pretending to detect every
	// instance of it.
	RegressedSessions prometheus.Counter
}

func (m Metrics) emitted(connected bool) {
	if m.Emitted == nil {
		return
	}
	state := "disconnected"
	if connected {
		state = "connected"
	}
	m.Emitted.WithLabelValues(state).Inc()
}

func (m Metrics) skipped(r SkipReason) {
	if m.Skipped != nil {
		m.Skipped.WithLabelValues(r.String()).Inc()
	}
}

func incr(c prometheus.Counter) {
	if c != nil {
		c.Inc()
	}
}

// Tap turns the broker's connection advisories into presence events.
//
// It holds no per-device state, and that is a constraint rather than a simplification:
// the advisories are consumed through a queue group, so one device's connect and
// disconnect routinely land on different replicas. Anything the tap remembers is
// therefore a partial view, which is why the ordering rules live in presence.Decide
// against the projection's stored session and not here.
type Tap struct {
	instanceId string
	source     string
	emitter    Emitter
	gate       Gate
	metrics    Metrics
	// lastSession is the best-effort regression detector described on
	// Metrics.RegressedSessions. Bounded by definition to the devices this replica has
	// seen, and deliberately never consulted for a decision — only for the counter.
	lastSession *sessionWatermarks
}

// NewTap binds a tap for one instance, emitting under source. gate may be nil only in
// tests; the service always supplies the shared one.
func NewTap(instanceId, source string, emitter Emitter, gate Gate, metrics Metrics) *Tap {
	return &Tap{
		instanceId:  instanceId,
		source:      source,
		emitter:     emitter,
		gate:        gate,
		metrics:     metrics,
		lastSession: newSessionWatermarks(),
	}
}

// QueueGroup is the name every replica of one instance shares so an advisory is
// handled exactly once across them.
//
// 🔴 THE INSTANCE ID IS IN THE NAME, AND ON A SHARED BROKER IT IS LOAD-BEARING. Two
// instances (ADR-048) share one broker, therefore one system account and one
// $SYS.ACCOUNT.APP.CONNECT subject. With a fixed group name their taps would join the
// SAME queue group, and the broker would hand each advisory to exactly one of them —
// so roughly half of instance A's device connects would be delivered only to instance
// B, which correctly discards them as another instance's. Both instances would then
// assert presence for about half their fleet, intermittently, with nothing in either
// one's logs to say why.
func QueueGroup(instanceId string) string { return "dc-presence-" + instanceId }

// Subscribe binds the tap to a SYS connection with a queue group, so exactly one
// replica handles each advisory.
//
// The subscriptions are FLUSHED before returning. An un-flushed Subscribe is only
// queued client-side, so a caller that reported success on it would be reporting that
// the request was written to a buffer, not that the broker had accepted it — and a
// presence tap that is silently unsubscribed looks exactly like a fleet nobody is
// connecting to.
func (t *Tap) Subscribe(nc *nats.Conn) (func(), error) {
	group := QueueGroup(t.instanceId)
	subs := make([]*nats.Subscription, 0, 2)
	for _, s := range []struct {
		subject   string
		connected bool
	}{
		{natsauth.SysConnectSubject, true},
		{natsauth.SysDisconnectSubject, false},
	} {
		connected := s.connected
		sub, err := nc.QueueSubscribe(s.subject, group, func(m *nats.Msg) {
			t.Handle(context.Background(), connected, m.Data)
		})
		if err != nil {
			unsubscribeAll(subs)
			return nil, fmt.Errorf("subscribing to %s: %w", s.subject, err)
		}
		subs = append(subs, sub)
	}
	if err := nc.Flush(); err != nil {
		unsubscribeAll(subs)
		return nil, fmt.Errorf("flushing the presence subscriptions: %w", err)
	}
	log.Info().Str("instance", t.instanceId).Str("queueGroup", group).Str("source", t.source).
		Msg("Broker-asserted MQTT presence is live: reading connection advisories from the system account.")
	return func() { unsubscribeAll(subs) }, nil
}

func unsubscribeAll(subs []*nats.Subscription) {
	for _, s := range subs {
		_ = s.Unsubscribe()
	}
}

// Handle classifies one advisory and emits the presence transition it means, if any.
//
// It never returns an error, because there is no caller to return one to: this runs on
// a NATS delivery callback for a subject with no acknowledgement and no redelivery. A
// transition lost here is lost, which is precisely the hole reconciliation exists to
// fill — so the failure is COUNTED and logged rather than swallowed.
func (t *Tap) Handle(ctx context.Context, connected bool, raw []byte) {
	transition, skip := TransitionFor(t.instanceId, connected, raw)
	if skip != "" {
		t.metrics.skipped(skip)
		return
	}
	t.Apply(ctx, transition)
}

// Apply gates and emits an already-classified transition, reporting whether it actually
// reached the stream. Shared with reconciliation, so a synthetic transition passes
// exactly the same admission checks as one the broker announced — a repair must not be a
// way around the deleted-tenant refusal.
//
// 🔑 THE RETURN VALUE IS WHAT KEEPS THE REPAIR COUNTER HONEST. Refusals and write
// failures are both ordinary here, and a caller that counted attempts would report
// repairs it did not make — which is worst precisely when something is wrong, since a
// tenant over its ceiling would show a healthy repair rate while nothing was written.
func (t *Tap) Apply(ctx context.Context, transition Transition) bool {
	// 🔴 THE GATE IS METERED AS LIVE TRAFFIC, NOT AT THE EVENT'S OWN TIME. The gate
	// routes anything older than BacklogThreshold to the BACKLOG limiter, and a
	// reconcile-connect carries the connection's start — which can be days old. That
	// bucket accrues from the last timestamp it saw, so feeding it a rewound mark
	// re-accrues to burst on the next forward jump, which is the token-minting hazard
	// the live/backlog split exists to close (measured at ~2000 admissions against a
	// 100/s ceiling). A zero time means "now" to the gate and keeps each bucket on
	// exactly one clock. The event's own OccurredAt is untouched.
	if t.gate != nil && !t.gate(t.source, transition.Tenant, time.Time{}, false) {
		incr(t.metrics.Refused)
		return false
	}
	if t.lastSession.regressed(transition.Tenant, transition.DeviceToken, transition.Event.SessionId) {
		// Emitted anyway: the projection's stored session is the authority on whether
		// this is stale, and this replica's view is partial. The counter is what makes
		// the clock-skew hazard visible.
		incr(t.metrics.RegressedSessions)
		log.Warn().Str("tenant", transition.Tenant).Str("device", transition.DeviceToken).
			Uint64("session", transition.Event.SessionId).
			Msg("Presence session id went backwards for a device; a broker node's clock may be trailing its peers.")
	}
	if err := t.emitter.EmitPresence(ctx, transition.Tenant, t.source, transition.DeviceToken, transition.Event); err != nil {
		incr(t.metrics.Failed)
		log.Error().Err(err).Str("transition", transition.describe()).
			Msg("Failed to emit a broker presence transition; reconciliation will have to recover it.")
		return false
	}
	t.metrics.emitted(transition.Event.Connected)
	return true
}
