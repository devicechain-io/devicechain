// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

// TapOffReason names why broker-asserted presence is not running, and it is the label on
// the gauge an operator reads. The values are the bail paths in start-up order.
//
// 🔴 THE GAUGE IS THE POINT OF THIS WHOLE VOCABULARY. Before it, "presence is silently not
// being asserted on this instance" and "a healthy idle fleet" were indistinguishable from
// outside: a long-lived MQTT fleet legitimately emits no advisories for days, so the
// presence counters read identically either way. The canary covers the case where the tap
// STARTED and then stopped working; nothing covered the case where it never started.
type TapOffReason string

const (
	// TapOffDisabled is a written `enabled: false`.
	TapOffDisabled TapOffReason = "disabled"
	// TapOffNoSystemCredential is a missing NATS system-account user or password.
	TapOffNoSystemCredential TapOffReason = "no_system_credential"
	// TapOffNoGatewaySource is no event source pointed at the platform broker.
	TapOffNoGatewaySource TapOffReason = "no_gateway_source"
	// TapOffNoServiceAuth is service-to-service calls not being configured.
	TapOffNoServiceAuth TapOffReason = "no_service_auth"
	// TapOffBrokerUnreachable is a failed system-account dial.
	TapOffBrokerUnreachable TapOffReason = "broker_unreachable"
	// TapOffSubscribeFailed is a failed advisory subscription.
	TapOffSubscribeFailed TapOffReason = "subscribe_failed"
)

// AllTapOffReasons is every reason, so the gauge can be zeroed across the whole label set
// on the healthy path. A gauge that is only ever SET leaves the last reason standing
// forever once the condition clears, which for a restart-scoped signal is worse than no
// gauge — it reports a problem the operator has already fixed.
func AllTapOffReasons() []TapOffReason {
	return []TapOffReason{
		TapOffDisabled, TapOffNoSystemCredential, TapOffNoGatewaySource,
		TapOffNoServiceAuth, TapOffBrokerUnreachable, TapOffSubscribeFailed,
	}
}

// AssertedWalker streams a source's asserted rows one at a time. Satisfied by
// GraphQLProjectionReader.
type AssertedWalker interface {
	WalkAsserted(ctx context.Context, tenant, source string, fn func(StoredDevice) error) error
}

// Waiter paces the drain. It is an interface rather than a *rate.Limiter because a limiter
// reads time.Now internally, which makes a fake-clock test of the pacing impossible — and
// pacing is the one property of this component that a test cannot get at any other way.
type Waiter interface {
	Wait(ctx context.Context) error
}

// DemoterMetrics are the drain's operator signals. The refusal and failure counters are
// deliberately absent: ApplyDemotion already reports both under the shared presence
// metrics, and a second pair would tell the same story in different words.
type DemoterMetrics struct {
	// Released counts rows successfully handed back toward inferred. It is emissions, not
	// confirmed demotions — the projection applies them asynchronously.
	Released prometheus.Counter
	// Remaining is how many rows the drain still found ASSERTED at the START of its last
	// pass. It is the drain's progress signal and its termination condition made visible:
	// the work empties itself, because a demoted row leaves the set the walk reads, so a
	// healthy drain walks this to zero and stays there.
	Remaining prometheus.Gauge
}

// demoteRate is how many rows a second the drain releases. It is deliberately slow and
// deliberately not configurable. The drain is a background repair with no deadline; what
// it must not do is turn a disabled tap into a durable-write burst across an entire
// fleet, which is a real cost paid by every tenant on the instance at once.
const demoteRate = 25

// SettleWindow is how long the drain waits before its first pass when the tap is off for a
// reason that could plausibly be a transient of the run that is starting the instance.
//
// 🔑 IT APPLIES TO THE AMBIGUOUS REASONS ONLY. `enabled: false` is a written value —
// Enabled is a *bool precisely so false is distinguishable from unset — so an operator who
// wrote it meant it and the drain acts immediately. A MISSING system-account credential is
// ambiguous: a bring-up mints it in the same run that starts the services, so an absent
// value can simply be a race with the run that is creating it, and demoting a whole fleet
// on the strength of a value that appears ninety seconds later would be a self-inflicted
// outage of exactly the kind this arc exists to prevent. An UNREACHABLE BROKER is ambiguous
// for the same reason and on the same clock: the bring-up rolls the NATS StatefulSet
// alongside the Deployments, so a pod that could not reach the system account for its whole
// startup window may be looking at a broker that is thirty seconds from being back.
const SettleWindow = 120 * time.Second

// Demoter hands a source's asserted device-state rows back to inferred presence.
//
// 🔑 IT IS A DRAIN, NOT A JOB, AND THAT IS WHY IT NEEDS NO CURSOR. The set it walks is
// "rows this source still has asserted", and a successful demotion REMOVES a row from that
// set. So the work empties itself: an interrupted pass resumes for free on the next tick,
// a refused row is retried because it is still in the set, and there is nothing to persist
// between passes. The alternative — a bounded number of retries with a backoff — has to
// decide when to give up, and giving up here means leaving rows frozen forever while the
// condition that froze them is still true.
//
// It also has no lease and takes no lock. Every replica of a disabled instance runs the
// same drain, which means N replicas emit the same demotion for the same row. That is
// harmless by construction: the emissions carry the same dedup key, and even past the
// duplicate window a redelivered demotion is refused by the ordering guard as not being
// newer than the row it already moved. Paying for a coordination mechanism to avoid a cost
// the ordering rules already absorb would be the more fragile design.
type Demoter struct {
	source    string
	publisher *Publisher
	tenants   TenantLister
	walker    AssertedWalker
	waiter    Waiter
	metrics   DemoterMetrics
}

// NewDemoter binds a drain for one source.
func NewDemoter(source string, publisher *Publisher, tenants TenantLister, walker AssertedWalker,
	waiter Waiter, metrics DemoterMetrics) *Demoter {
	return &Demoter{
		source: source, publisher: publisher, tenants: tenants,
		walker: walker, waiter: waiter, metrics: metrics,
	}
}

// Run makes one pass: list the instance's tenants, and for each, walk the rows this source
// still has asserted and release every one.
//
// A tenant whose walk fails is SKIPPED for the pass rather than failing it, and the
// distinction matters: the walk is all-or-nothing per tenant (a half-read page set is not
// a shorter answer, it is a wrong one), but one unreachable tenant must not stop the
// others being repaired. Its rows are still asserted, so the next pass finds them again.
func (d *Demoter) Run(ctx context.Context, now time.Time) error {
	tenants, err := d.tenants.TenantTokens(ctx)
	if err != nil {
		return fmt.Errorf("listing tenants to release asserted presence: %w", err)
	}

	var walked, released int
	for _, tenant := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := d.drainTenant(ctx, tenant, now)
		walked += n.walked
		released += n.released
		if err != nil {
			// 🔑 A CANCELLED WALK IS NOT A TENANT FAILURE, and telling them apart is not
			// cosmetic. Cancellation happens on every shutdown of a disabled instance, so
			// reporting it as an unreachable tenant would make a normal stop look like an
			// outage in the logs — and worse, letting the pass finish would publish a
			// Remaining gauge computed from a walk that stopped half way, which reads as
			// the drain having nearly finished.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			log.Warn().Err(err).Str("tenant", tenant).Str("source", d.source).
				Msg("Could not read this tenant's asserted devices to release them; its rows stay " +
					"asserted until the next pass.")
		}
	}

	if d.metrics.Remaining != nil {
		d.metrics.Remaining.Set(float64(walked))
	}
	if walked > 0 {
		log.Info().Str("source", d.source).Int("stillAsserted", walked).Int("released", released).
			Msg("Released devices from asserted presence because this source is no longer reading the broker.")
	}
	return nil
}

type drainCount struct{ walked, released int }

// drainTenant walks one tenant's asserted rows and releases each. It returns the walk's
// error unchanged rather than deciding what it means; only Run knows whether the pass as a
// whole is being cancelled.
func (d *Demoter) drainTenant(ctx context.Context, tenant string, now time.Time) (drainCount, error) {
	var n drainCount
	err := d.walker.WalkAsserted(ctx, tenant, d.source, func(row StoredDevice) error {
		n.walked++
		// Paced before the emit, not after, so a pass that is cancelled mid-drain has
		// already spent only what it emitted.
		if err := d.waiter.Wait(ctx); err != nil {
			return err
		}
		if d.publisher.ApplyDemotion(ctx, Demotion{
			Tenant:      tenant,
			DeviceToken: row.DeviceToken,
			Event: adapter.DemotionEvent{
				// The row's OWN stored session, because a demotion applies only against the
				// session on file. Zero is a legitimate value here — a producer that sent no
				// session id asserted this row with zero, and zero is what releases it.
				SessionId:  row.SessionId,
				OccurredAt: now,
				Reason:     "source-release: " + d.source + " is no longer reading broker presence",
			},
		}) {
			n.released++
			incr(d.metrics.Released)
		}
		// A refusal is NOT an error and must not stop the walk. The row stays asserted, so
		// the next pass finds it again — which is the whole reason this is a self-emptying
		// drain rather than a job with a retry budget.
		return nil
	})
	return n, err
}

// DemoteRunner is the loop's seam onto the drain, so the loop's own properties — that it
// honours the start delay, that it runs immediately after it, that it keeps going after a
// failed pass — are testable without a projection or a stream.
type DemoteRunner interface {
	Run(ctx context.Context, now time.Time) error
}

// StartDelayFor is how long the drain waits before its FIRST pass, given why the tap is
// off. A missing system-account credential and an unreachable broker get the settle window;
// see SettleWindow. The jitter is added in every case and is the caller's to choose — it
// spreads the first pass across replicas that all restarted together, which is every
// replica of an instance whose configuration just changed.
func StartDelayFor(reason TapOffReason, jitter time.Duration) time.Duration {
	if reason == TapOffNoSystemCredential || reason == TapOffBrokerUnreachable {
		return SettleWindow + jitter
	}
	return jitter
}

// RunDemoteLoop drains on a slow ticker until ctx ends.
//
// 🔑 THE FIRST PASS RUNS AFTER THE START DELAY AND THEN IMMEDIATELY, not after a full
// interval. The interval here is the reconciler's, measured in minutes, and it is the
// right pace for a repair that never finishes; it is the wrong pace for the first look at
// a fleet that is frozen right now.
func RunDemoteLoop(ctx context.Context, d DemoteRunner, interval, startDelay time.Duration, now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	if startDelay > 0 {
		timer := time.NewTimer(startDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	runOnce := func() {
		if err := d.Run(ctx, now()); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("A pass to release asserted presence failed; the devices it did " +
				"not reach stay asserted until the next pass.")
		}
	}
	runOnce()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

// RateWaiter paces the drain at demoteRate rows a second, with a burst of one so a long
// idle stretch cannot bank tokens and then release a fleet in a single second.
type RateWaiter struct{ limiter *rate.Limiter }

// NewRateWaiter builds the drain's default pacing.
func NewRateWaiter() *RateWaiter {
	return &RateWaiter{limiter: rate.NewLimiter(demoteRate, 1)}
}

// Wait blocks until the next row may be released, or until ctx ends.
func (w *RateWaiter) Wait(ctx context.Context) error { return w.limiter.Wait(ctx) }
