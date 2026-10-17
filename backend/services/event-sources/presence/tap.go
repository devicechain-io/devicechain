// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"fmt"
	"sync"
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
	EmitPresenceDemotion(ctx context.Context, tenant, source, deviceToken string, ev adapter.DemotionEvent) error
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
	// Deferred counts disconnects held back by the refusal-close debounce (see
	// RefusalCloseReason) rather than emitted at once.
	Deferred prometheus.Counter
	// DeferredDropped counts held-back disconnects the tap stopped before emitting. The
	// reconciler's next pass recovers them, as it does any transition a stopping pod drops.
	DeferredDropped prometheus.Counter
}

// Emitted's state labels. They mirror the wire vocabulary rather than the tap's internal
// bool, so a reader of the metric and a reader of the event see the same words — and so a
// claim that is not about connectivity cannot be forced into one of two connectivity
// buckets on its way to a dashboard.
const (
	labelConnected    = "connected"
	labelDisconnected = "disconnected"
	labelDemoted      = "demoted"
)

func (m Metrics) emitted(state string) {
	if m.Emitted == nil {
		return
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
// It holds no per-device state that any DECISION reads, and the qualifier is the whole
// point: the advisories are consumed through a queue group, so one device's connect and
// disconnect routinely land on different replicas. Anything the tap remembers is
// therefore a partial view, which is why the ordering rules live in presence.Decide
// against the projection's stored session and not here.
//
// 🔴 IT DOES HOLD PER-DEVICE STATE, AND SAYING OTHERWISE HID A BOUNDED HEAP. The embedded
// Publisher carries sessionWatermarks, a map keyed by (tenant, device) with a hard
// 50,000-entry ceiling — device-controlled keys, which is exactly why it has a ceiling.
// It is read ONLY to count a backwards session id (Metrics.RegressedSessions) and never
// to accept or reject a transition, so the constraint above holds as stated once it is
// stated accurately. An unqualified "holds no per-device state" reads as a licence to
// skip the ceiling the next time something per-device is added here.
type Tap struct {
	// Publisher is embedded rather than held in a named field so Tap.Apply, and every
	// caller that already had one, keep working unchanged. Tap is the BROKER-FACING half
	// — a subscription, a queue group, an advisory classifier — and it owns nothing about
	// admission or emission any more.
	*Publisher
	instanceId string

	// debounce is how long a refusal-close disconnect is held back (RefusalCloseDebounce; 0
	// holds nothing). See deferDeath.
	debounce time.Duration
	deferMu  sync.Mutex
	deferred map[*time.Timer]struct{}
	stopped  bool
}

// RefusalCloseReason is the reason the broker gives, on a DISCONNECT advisory, for a connection
// its MQTT gateway closed because the message store refused a QoS 1 publish or did not
// acknowledge it within the gateway's 5 s wait (nats-server v2.15.0's mqttAckLoop closes it as
// ProtocolViolation, whose text this is). The same text also names a connection closed for a
// malformed packet; such a device is disconnected for good, and is reported
// RefusalCloseDebounce late.
//
// 🔴 A REFUSAL-CLOSE IS NOT THE DEVICE GOING AWAY. The device kept the publish the store would
// not take and reconnects to send it again, in seconds. Under overload the store refuses many
// devices at once, so reporting each close at once would flip a fleet offline and back:
// command-delivery holds a device's commands while its presence reads offline, and DETECT's
// offline rules fire, both during the overload that caused it. The tap therefore holds such a
// disconnect back for RefusalCloseDebounce. A device that reconnects within it has a newer
// session by then, so the held disconnect, carrying the old one, is refused by presence.Decide
// wherever it lands (the projection, DETECT, command-delivery): the device never reads offline.
// A device that does not reconnect is reported offline at the end of the hold, with the
// broker's own stop time, as if the advisory had arrived late.
//
// 🔴 THE RECONCILER IS NOT DEBOUNCED, and that is a residual rather than an oversight. Its pass
// (every few minutes) reports offline a device the broker is not holding, and it cannot tell a
// device between a refusal and its reconnect from one that is gone. Such a device reads offline
// from the pass until its reconnect, which is a newer session and wins. One pass is one sample,
// so this touches the devices that happen to be between connections at that instant, not every
// refusal.
//
// Pinned against the broker by TestAStoreRefusalCloseCarriesTheReasonTheTapDebounces.
const RefusalCloseReason = "Protocol Violation"

// RefusalCloseDebounce is how long a refusal-close disconnect is held back: well over the few
// seconds a device takes to reconnect after the gateway closes it, and short beside what an
// offline rule or the command hold acts on.
const RefusalCloseDebounce = 30 * time.Second

// maxDeferredDeaths bounds the disconnects held back at once. Refusals are device-driven (a
// device can be refused and reconnect in a loop), so the set has a ceiling; past it a
// refusal-close is reported at once, as before the debounce existed.
const maxDeferredDeaths = 100_000

// WithWaker attaches the command wake to a tap. Separate from NewTap because the waker
// needs command-delivery's coordinates, which are resolved later than the tap itself and
// may legitimately be absent.
func (t *Tap) WithWaker(w Waker) *Tap {
	t.Publisher.waker = w
	return t
}

// WithAsyncEmitter makes the tap emit through e instead of waiting for each acknowledgement.
// Only Handle (the broker's advisories) uses it; Apply, which the reconciler calls, stays
// synchronous.
func (t *Tap) WithAsyncEmitter(e AsyncEmitter) *Tap {
	t.Publisher.async = e
	return t
}

// NewTap binds a tap for one instance, emitting under source. gate may be nil only in
// tests; the service always supplies the shared one.
func NewTap(instanceId, source string, emitter Emitter, gate Gate, metrics Metrics) *Tap {
	return &Tap{
		Publisher:  NewPublisher(source, emitter, gate, metrics),
		instanceId: instanceId,
		debounce:   RefusalCloseDebounce,
		deferred:   map[*time.Timer]struct{}{},
	}
}

// WithRefusalDebounce replaces RefusalCloseDebounce for this tap; 0 holds nothing back. A test
// seam: production keeps the constant.
func (t *Tap) WithRefusalDebounce(d time.Duration) *Tap {
	t.debounce = d
	return t
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
	// The stop also drops the disconnects still held back, before the caller closes the writer
	// they would be emitted through.
	return func() { unsubscribeAll(subs); t.StopDeferred() }, nil
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
//
// With an async emitter (WithAsyncEmitter) it returns once the transition is QUEUED, not
// written: the outcome, failure included, is counted and logged when the broker answers.
func (t *Tap) Handle(ctx context.Context, connected bool, raw []byte) {
	transition, skip := TransitionFor(t.instanceId, connected, raw)
	if skip != "" {
		t.metrics.skipped(skip)
		return
	}
	if !connected && transition.Event.Reason == RefusalCloseReason && t.deferDeath(transition) {
		return
	}
	t.emit(ctx, transition)
}

func (t *Tap) emit(ctx context.Context, transition Transition) {
	if t.async != nil {
		t.ApplyAsync(ctx, transition)
		return
	}
	t.Apply(ctx, transition)
}

// emitHeld emits a disconnect the debounce held back, without the session-regression check.
//
// 🔴 A HELD DISCONNECT IS OLDER THAN WHAT FOLLOWED IT BY DESIGN. Its device has usually
// reconnected within the hold, and when that reconnect's advisory reached this replica, the
// held disconnect's session is below the one already emitted for the device. The regression
// check reads exactly that as a broker node whose clock trails its peers, so counting it would
// put one RegressedSessions increment and one clock-skew warning on every debounced refusal:
// under overload, the whole fleet, pointing the operator at clocks during a store overload.
func (t *Tap) emitHeld(ctx context.Context, transition Transition) {
	if t.async != nil {
		t.applyAsync(ctx, transition, false)
		return
	}
	t.apply(ctx, transition, false)
}

// deferDeath holds a refusal-close disconnect back for the debounce (see RefusalCloseReason)
// and emits it then. It reports false when it did not hold it (no debounce, the tap is
// stopping, or the ceiling is reached), and the caller then emits it at once.
//
// Nothing about the held disconnect is changed, and nothing cancels it when the device
// reconnects: the advisories are spread over a queue group, so the reconnect usually lands on
// another replica. presence.Decide refusing the older session is what makes a held disconnect
// harmless wherever it is emitted.
func (t *Tap) deferDeath(transition Transition) bool {
	if t.debounce <= 0 {
		return false
	}
	t.deferMu.Lock()
	defer t.deferMu.Unlock()
	if t.stopped || len(t.deferred) >= maxDeferredDeaths {
		return false
	}
	if t.deferred == nil {
		t.deferred = map[*time.Timer]struct{}{}
	}
	var timer *time.Timer
	timer = time.AfterFunc(t.debounce, func() {
		t.deferMu.Lock()
		_, held := t.deferred[timer]
		delete(t.deferred, timer)
		t.deferMu.Unlock()
		if !held {
			return // dropped by StopDeferred, and counted there
		}
		t.emitHeld(context.Background(), transition)
	})
	t.deferred[timer] = struct{}{}
	incr(t.metrics.Deferred)
	return true
}

// StopDeferred drops the disconnects still held back and holds no more. A pod that is stopping
// loses them, as it loses the transitions its writer had not had acknowledged, and the
// reconciler recovers them. Safe to call more than once.
func (t *Tap) StopDeferred() {
	t.deferMu.Lock()
	defer t.deferMu.Unlock()
	t.stopped = true
	for timer := range t.deferred {
		timer.Stop()
		incr(t.metrics.DeferredDropped)
	}
	clear(t.deferred)
}

// DeferredDeaths is how many disconnects are held back right now.
func (t *Tap) DeferredDeaths() int {
	t.deferMu.Lock()
	defer t.deferMu.Unlock()
	return len(t.deferred)
}
