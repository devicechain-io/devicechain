// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"errors"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// Publisher is the gated-emitter half of the presence machinery: everything between
// "here is a classified presence claim for a device" and "it is on the inbound-events
// stream, or it is not and we counted why". It owns no subscription, no broker
// connection and no clock, which is what lets three very different callers share it —
// the broker tap (an advisory just arrived), the reconciler (a repair pass found a
// divergence) and the demoter (a source is going away).
//
// 🔑 IT WAS EXTRACTED FROM Tap RATHER THAN WRITTEN BESIDE IT, and the alternative is
// why. A second emit path built alongside the tap would have its own gate call, its own
// failure counter and its own idea of what to do when the write fails — which is exactly
// how a repair becomes a way around the deleted-tenant refusal. There is one admission
// path; a caller chooses WHAT to publish, never WHETHER the rules apply.
type Publisher struct {
	source  string
	emitter Emitter
	gate    Gate
	metrics Metrics
	// lastSession is the best-effort regression detector described on
	// Metrics.RegressedSessions. Bounded by definition to the devices this replica has
	// seen, and deliberately never consulted for a decision — only for the counter.
	lastSession *sessionWatermarks
	// waker releases a returning device's withheld commands. MAY BE NIL, which simply
	// means no wake — held commands then wait for command-delivery's reconcile pass.
	//
	// 🔴 IT HANGS HERE, ON THE BROKER-SIDE PUBLISHER, RATHER THAN INSIDE EmitPresence,
	// AND THAT IS NOT AN ARBITRARY PLACEMENT. EmitPresence is shared: lwm2m-ingest calls
	// it too, and LwM2M already dispatches a returning device's backlog ITSELF over the
	// CoAP session it opens on Register. A wake there would race that drain — both would
	// put the same commands in front of a dispatcher, and a command is a physical
	// actuation. This publisher backs the broker-asserted MQTT path specifically, which
	// is exactly the transport that delivers by publishing and therefore has no drain of
	// its own.
	waker Waker
	// async queues an emit without waiting for its acknowledgement, for ApplyAsync. MAY BE
	// NIL, which is every publisher but the broker tap's.
	async AsyncEmitter
}

// NewPublisher binds a publisher emitting under source. gate may be nil only in tests;
// the service always supplies the shared one.
func NewPublisher(source string, emitter Emitter, gate Gate, metrics Metrics) *Publisher {
	return &Publisher{
		source:      source,
		emitter:     emitter,
		gate:        gate,
		metrics:     metrics,
		lastSession: newSessionWatermarks(),
	}
}

// admit runs the shared admission gate for one tenant, counting a refusal. Every public
// emit path goes through it, so there is exactly one place the ADR-077 deleted-tenant
// refusal and the ADR-023 ceiling can be bypassed — and it has no arguments a caller
// could use to ask for an exemption.
//
// 🔴 THE GATE IS METERED AS LIVE TRAFFIC, NOT AT THE EVENT'S OWN TIME. The gate routes
// anything older than BacklogThreshold to the BACKLOG limiter, and a reconcile-connect
// carries the connection's start — which can be days old. That bucket accrues from the
// last timestamp it saw, so feeding it a rewound mark re-accrues to burst on the next
// forward jump, which is the token-minting hazard the live/backlog split exists to close
// (measured at ~2000 admissions against a 100/s ceiling). A zero time means "now" to the
// gate and keeps each bucket on exactly one clock. The event's own OccurredAt is
// untouched.
func (p *Publisher) admit(tenant string) bool {
	if p.gate != nil && !p.gate(p.source, tenant, time.Time{}, false) {
		incr(p.metrics.Refused)
		return false
	}
	return true
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
func (p *Publisher) Apply(ctx context.Context, transition Transition) bool {
	return p.apply(ctx, transition, true)
}

// apply is Apply, with the regression check only when note is set (see Tap.emitHeld).
func (p *Publisher) apply(ctx context.Context, transition Transition, note bool) bool {
	if !p.admit(transition.Tenant) {
		return false
	}
	if note {
		p.noteSession(transition)
	}
	return p.settled(transition, p.emitter.EmitPresence(ctx, transition.Tenant, p.source, transition.DeviceToken, transition.Event))
}

// AsyncEmitter is the asynchronous half of Emitter: queue the transition and report the
// outcome later. Satisfied by adapter.Emitter once it has an async writer.
type AsyncEmitter interface {
	EmitPresenceAsync(ctx context.Context, tenant, source, deviceToken string, ev adapter.PresenceEvent, done func(error))
}

// ErrNoAsyncEmitter is what ApplyAsync counts as a failed emit when the publisher was never
// given an AsyncEmitter. It is loud on purpose: the alternative, a silent fall back to the
// synchronous write, would hand the caller a throughput it did not get.
var ErrNoAsyncEmitter = errors.New("presence: this publisher has no asynchronous emitter")

// ApplyAsync is Apply for a caller that must not wait for the broker's acknowledgement: the
// broker tap, whose delivery goroutine would otherwise be held for one acknowledgement per
// advisory (about 11 ms on a replicated broker, so some 88 transitions a second per
// subject). It runs the same admission and the same regression check synchronously, then
// queues the emit; everything that follows the emit (the emitted or failed counters, the
// log line, the command wake) happens in the completion callback, exactly once.
//
// 🔑 THE WAKE MOVED INTO THE COMPLETION, NOT BESIDE IT. It fires only on a CONNECT whose
// emit was ACKNOWLEDGED, as in Apply: a wake sent when the transition was merely queued
// would race a write that can still fail, and would release commands into a sweep that
// reads the device as absent. A failed emit wakes nothing; it is counted and logged, and the
// reconciler recovers the transition.
//
// The reconciler and the demoter keep the synchronous Apply: they report whether each
// repair landed, and pace themselves on it.
func (p *Publisher) ApplyAsync(ctx context.Context, transition Transition) {
	p.applyAsync(ctx, transition, true)
}

// applyAsync is ApplyAsync, with the regression check only when note is set (see Tap.emitHeld).
func (p *Publisher) applyAsync(ctx context.Context, transition Transition, note bool) {
	if !p.admit(transition.Tenant) {
		return
	}
	if note {
		p.noteSession(transition)
	}
	if p.async == nil {
		p.settled(transition, ErrNoAsyncEmitter)
		return
	}
	p.async.EmitPresenceAsync(ctx, transition.Tenant, p.source, transition.DeviceToken, transition.Event,
		func(err error) { p.settled(transition, err) })
}

// noteSession counts a session id that went backwards for the device. Emitted anyway: the
// projection's stored session is the authority on whether the transition is stale, and this
// replica's view is partial. The counter is what makes the clock-skew hazard visible.
func (p *Publisher) noteSession(transition Transition) {
	if p.lastSession.regressed(transition.Tenant, transition.DeviceToken, transition.Event.SessionId) {
		incr(p.metrics.RegressedSessions)
		log.Warn().Str("tenant", transition.Tenant).Str("device", transition.DeviceToken).
			Uint64("session", transition.Event.SessionId).
			Msg("Presence session id went backwards for a device; a broker node's clock may be trailing its peers.")
	}
}

// settled is what happens once an emit has an outcome, whichever path produced it, and it
// reports whether the transition reached the stream. It may run on a writer's goroutine, so
// it never blocks: counters, one log line and Waker.Wake, which is a non-blocking enqueue.
func (p *Publisher) settled(transition Transition, err error) bool {
	if errors.Is(err, messaging.ErrAsyncWriterClosed) {
		// 🔑 A STOP, NOT A FAILED WRITE, SO IT IS NOT COUNTED AS ONE. The writer closes only
		// in the stop hook, after the subscriptions stop, so what lands here is the one or two
		// advisories a delivery goroutine was still handing over at that instant: the same
		// loss as the advisories the unsubscribe drops from the client buffer, which were
		// never counted either, and repaired the same way. Counting them would put a failure
		// on presence_events_failed_total on every rolling restart, and an alert that fires on
		// routine deploys is one nobody reads by the time it means something.
		log.Warn().Str("transition", transition.describe()).
			Msg("A broker presence transition arrived as the tap was stopping and was not emitted; reconciliation will recover it.")
		return false
	}
	if err != nil {
		incr(p.metrics.Failed)
		log.Error().Err(err).Str("transition", transition.describe()).
			Msg("Failed to emit a broker presence transition; reconciliation will have to recover it.")
		return false
	}
	state := labelDisconnected
	if transition.Event.Connected {
		state = labelConnected
	}
	p.metrics.emitted(state)

	// 🔑 THE WAKE FIRES ONLY AFTER A SUCCESSFUL CONNECT EMIT, AND BOTH HALVES OF THAT
	// MATTER. Only on a CONNECT because a disconnect has nothing to release. Only after
	// the emit lands because the wake races it: command-delivery re-reads presence when
	// it dispatches, so a wake that arrived before the projection had the connect would
	// release the commands into a sweep that reads the device as still absent and simply
	// withholds them again — a wasted round trip that also looks, on the counters, like
	// the wake working.
	//
	// The ordering is not a guarantee, only the best this side can do: the emit is
	// asynchronous downstream, so the projection may still be behind. What makes that
	// acceptable is that the failure is benign and self-correcting — the commands stay
	// held and the reconcile pass releases them once presence has caught up.
	if transition.Event.Connected && p.waker != nil {
		p.waker.Wake(transition.Tenant, transition.DeviceToken)
	}
	return true
}

// Demotion is a source releasing custody of one device: the same (tenant, device)
// addressing as a Transition, carrying a claim that is not about connectivity.
type Demotion struct {
	Tenant      string
	DeviceToken string
	Event       adapter.DemotionEvent
}

// ApplyDemotion gates and emits one custody release, reporting whether it reached the
// stream. It runs the SAME admission gate as a transition — a source going away is not a
// licence to write into a deleted tenant, and a fleet-wide release is exactly the shape
// the per-tenant ceiling exists to meter, since a demoter walks every asserted row a
// source owns.
//
// Two things it deliberately does NOT do:
//
// It does not fire the waker. The wake exists to release a returning device's withheld
// commands, and a demotion is not a return — it is the platform admitting it no longer
// knows. What it does instead is better: returning the row to INFERRED lifts the delivery
// hold outright, because the hold is keyed on ASSERTED-and-not-active.
//
// It does not feed the session-regression watermark. That counter exists to surface clock
// skew BETWEEN BROKER NODES minting connect epochs. A demotion re-states a session the
// projection already holds rather than minting one, so feeding it in would either move
// the watermark for no reason or, on a redelivery, count a regression that says nothing
// about any broker's clock.
func (p *Publisher) ApplyDemotion(ctx context.Context, d Demotion) bool {
	if !p.admit(d.Tenant) {
		return false
	}
	if err := p.emitter.EmitPresenceDemotion(ctx, d.Tenant, p.source, d.DeviceToken, d.Event); err != nil {
		incr(p.metrics.Failed)
		log.Error().Err(err).Str("tenant", d.Tenant).Str("device", d.DeviceToken).
			Uint64("session", d.Event.SessionId).
			Msg("Failed to emit a presence demotion; the device's row stays asserted until the next pass retries it.")
		return false
	}
	p.metrics.emitted(labelDemoted)
	return true
}
