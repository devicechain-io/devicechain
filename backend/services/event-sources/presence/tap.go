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
}

// WithWaker attaches the command wake to a tap. Separate from NewTap because the waker
// needs command-delivery's coordinates, which are resolved later than the tap itself and
// may legitimately be absent.
func (t *Tap) WithWaker(w Waker) *Tap {
	t.Publisher.waker = w
	return t
}

// NewTap binds a tap for one instance, emitting under source. gate may be nil only in
// tests; the service always supplies the shared one.
func NewTap(instanceId, source string, emitter Emitter, gate Gate, metrics Metrics) *Tap {
	return &Tap{
		Publisher:  NewPublisher(source, emitter, gate, metrics),
		instanceId: instanceId,
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
