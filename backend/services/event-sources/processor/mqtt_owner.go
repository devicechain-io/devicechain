// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// SourceHolder answers whether this pod still owns a source. *messaging.Holder satisfies it.
type SourceHolder interface {
	// Held never blocks and never makes a round trip, so it is asked per message.
	Held() bool
	// Lost closes on the first takeover or release the lease's watch sees.
	Lost() <-chan struct{}
}

// SourceLease is one held term of an external source's ownership. A *messaging.Lease,
// adapted by DistributedSourceLeases, satisfies it.
type SourceLease interface {
	// KeepAlive renews until ctx ends (nil) or ownership is definitively lost
	// (messaging.ErrNotHolder).
	KeepAlive(ctx context.Context, interval time.Duration) error
	// WatchHolder starts the per-message ownership gate; its watch runs until ctx ends.
	WatchHolder(ctx context.Context) (SourceHolder, error)
	Release() error
}

// SourceLeases acquires a term; messaging.ErrLeaseHeld means another pod owns the source.
type SourceLeases interface {
	Acquire(partition string) (SourceLease, error)
}

// DistributedSourceLeases adapts the instance lease bucket.
func DistributedSourceLeases(l *messaging.DistributedLease) SourceLeases {
	return distributedSourceLeases{l}
}

type distributedSourceLeases struct{ l *messaging.DistributedLease }

// Acquire returns a NIL interface on error, never a typed-nil lease.
func (d distributedSourceLeases) Acquire(partition string) (SourceLease, error) {
	lease, err := d.l.Acquire(partition)
	if err != nil {
		return nil, err
	}
	return distributedSourceLease{lease}, nil
}

type distributedSourceLease struct{ *messaging.Lease }

func (d distributedSourceLease) WatchHolder(ctx context.Context) (SourceHolder, error) {
	holder, err := d.Lease.WatchHolder(ctx)
	if err != nil {
		return nil, err
	}
	return holder, nil
}

// TermSource is what one ownership term runs: a lifecycle component with a client id.
// *MqttEventSource satisfies it.
//
// ⚠️ THE VERBS ARE SPELLED OUT RATHER THAN EMBEDDING core.LifecycleComponent, and that is
// load-bearing for hack/check-lifecycle-phase.sh. A term builds a FRESH source and
// initialises it on the start path. The guard expands an interface call to every type
// that satisfies the interface, so an Initialize through core.LifecycleComponent expands
// to every component in the tree, core/service's among them, whose Initialize builds
// metrics, and is reported; through this interface it expands only to the term sources.
//
// 🔴 WHAT THE GUARD DOES NOT SEE HERE, so that nobody reads its silence as a proof: it
// treats every ExecuteInitialize as an initialize-phase entry point and stops there, so a
// metric registered inside MqttEventSource.ExecuteInitialize would NOT be reported
// (checked by planting one). It would panic on a pod's second term. That initialisation
// builds a paho client and nothing else, and must stay that way: anything process-wide a
// term needs is built once, by main, and handed to the factory.
type TermSource interface {
	Initialize(ctx context.Context) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Terminate(ctx context.Context) error
	ClientID() string
	// Owns asks the source's ownership gate, the one it asks for every message.
	Owns() bool
}

// NewTermSource builds the source for one term. owns is the term's ownership gate, which
// the source must ask for every message (see MqttEventSource.owns).
type NewTermSource func(clientID string, owns func() bool) (TermSource, error)

// OwnershipPartition is the lease partition for an external source. Unique across the
// instance's lease users (detect:<tenant>, sparkplug, lwm2m); the lease bucket encodes
// the key, so any character a source id may carry is safe in it.
func OwnershipPartition(sourceId string) string { return "event-sources:mqtt:" + sourceId }

// Package variables, not constants, only so a test can shorten them.
var (
	// ownerRetryInterval is how often a standby asks for the lease. Short, because a
	// graceful handover waits this long and the source is at-most-once: what the broker
	// delivers while nobody reads is gone.
	ownerRetryInterval = 2 * time.Second
	// ownerFailedStartBackoff is how long a pod that took the source over but could not
	// connect waits before asking again, so that another pod gets the chance and a broker
	// that is down is not hammered by every pod in turn.
	ownerFailedStartBackoff = 15 * time.Second
)

// ownerRenewInterval is the renewal period, TTL/3 as for every other lease user.
const ownerRenewInterval = messaging.DefaultLeaseTTL / 3

// OwnerHooks are what an owned source reports through. Every one is required.
type OwnerHooks struct {
	// Fail ends the process. Used only for a takeover whose broker REFUSED the
	// subscription: the next pod would meet the same refusal.
	Fail func(error)
	// SetOwner records whether this pod reads the source (the owner gauge).
	SetOwner func(source string, owned bool)
	// NotOwner counts one message dropped because this pod no longer owns the source.
	NotOwner func(source string)
}

var errIncompleteOwnerHooks = errors.New("an owned MQTT source needs every hook: fail, setOwner and notOwner")

// OwnedMqttSource runs an external-broker source on exactly one pod at a time.
//
// 🔴 WHY ONE POD, AND NOT ONE SESSION PER POD. Every pod's session would be given every
// message (distinct client ids subscribing to one filter each receive all of it), and this
// path carries no deduplication id, so each extra pod would store every event again. One
// shared client id is no answer either: a broker keeps one session per id, so the pods take
// it from each other in a loop and lose what arrives meanwhile, which is the defect this
// type replaced. So the pods take a per-source lease, and the one holding it connects.
// Shared subscriptions ("$share/...") are not used: they are an MQTT 3.1.1 extension, and a
// broker without it — NATS among them — accepts the subscription and delivers nothing.
//
// The lease is per SOURCE, not per service. A pod that does not own a source is still
// Ready and still serves HTTP ingest, the platform gateway and presence.
//
// A term: acquire → start the renewer and the ownership watch → build a fresh source and
// start it under the term's context → read until the lease is lost or the service stops →
// stop the source (disconnect and drain) → join the term's goroutines → release. The
// renewer runs BEFORE the source starts, because a start can outlast the lease's TTL
// (connect and subscribe are each bounded at 30 s), and a start under an unrenewed lease
// would connect a second reader beside the pod that took the source over. Every message
// is also gated on the lease's Holder, so a pod that has lost the source drops rather
// than stores what it is still delivered.
type OwnedMqttSource struct {
	id string
	// want is the id every term is built with; clientID is the id the factory actually
	// built with, read once from the probe at construction. They are equal (the
	// constructor refuses otherwise).
	want, clientID string
	newSource      NewTermSource
	newLeases      func() (SourceLeases, error)
	hooks          OwnerHooks
	lifecycle      core.LifecycleManager

	cancel context.CancelFunc
	done   chan struct{}
	// askFailing is whether the last ask for the lease failed for a reason other than
	// another pod holding it, so that an unreachable lease bucket is reported once when it
	// goes and once when it comes back, not on every retry. Only the start path and the
	// standby goroutine touch it, and never at the same time.
	askFailing bool
}

// NewOwnedMqttSource builds an owned external source. newSource is called once here, to
// surface a configuration error (a bad port) at build time as an unowned source always
// did, and to refuse a source that does not connect under its own client id or does not
// gate on the ownership gate it is handed; and then once per term. newLeases is called at start, when the platform broker is up.
func NewOwnedMqttSource(id, instanceId, replica string, newSource NewTermSource,
	newLeases func() (SourceLeases, error), hooks OwnerHooks) (*OwnedMqttSource, error) {
	if hooks.Fail == nil {
		return nil, errNoFailHook
	}
	if hooks.SetOwner == nil || hooks.NotOwner == nil {
		return nil, errIncompleteOwnerHooks
	}
	if newSource == nil || newLeases == nil {
		return nil, errors.New("an owned MQTT source needs a source factory and a lease factory")
	}
	want, err := ExternalMqttClientID(instanceId, id, replica)
	if err != nil {
		return nil, err
	}
	// The probe's gate answers "not owned" and records that it was asked, so asking the
	// probe shows whether the source gates on the gate it is handed. main's factory is a
	// closure no test can call; a factory that wired any other gate (an always-true one)
	// would have a pod that lost the source go on storing what its broker still delivers.
	asked := false
	probe, err := newSource(want, func() bool { asked = true; return false })
	if err != nil {
		return nil, err
	}
	if probe.ClientID() != want {
		return nil, fmt.Errorf("external MQTT source %q was built with client id %q, not its own %q: "+
			"a broker keeps one session per id, so a shared one is taken over", id, probe.ClientID(), want)
	}
	if probe.Owns() || !asked {
		return nil, fmt.Errorf("external MQTT source %q was built with an ownership gate other than the one "+
			"it was handed: a pod that lost the source would go on storing what its broker delivers", id)
	}
	o := &OwnedMqttSource{id: id, want: want, clientID: probe.ClientID(), newSource: newSource,
		newLeases: newLeases, hooks: hooks}
	o.lifecycle = core.NewLifecycleManager("owned-mqtt-event-source", o, core.NewNoOpLifecycleCallbacks())
	return o, nil
}

// ClientID is the MQTT client id every term of this source connects with.
func (o *OwnedMqttSource) ClientID() string { return o.clientID }

func (o *OwnedMqttSource) Initialize(ctx context.Context) error { return o.lifecycle.Initialize(ctx) }

// ExecuteInitialize does nothing: each term initialises its own source.
func (o *OwnedMqttSource) ExecuteInitialize(context.Context) error { return nil }

func (o *OwnedMqttSource) Start(ctx context.Context) error { return o.lifecycle.Start(ctx) }

// ExecuteStart asks for the lease once. A pod that gets it starts reading before it
// returns, and a start that fails fails the service's start, as an external source always
// has: a refused subscription or an unreachable broker is reported at startup, not
// discovered later. A pod that does not get it returns at once and stands by.
func (o *OwnedMqttSource) ExecuteStart(ctx context.Context) error {
	leases, err := o.newLeases()
	if err != nil {
		return fmt.Errorf("external MQTT source %q cannot agree with the other pods which one reads the broker: %w",
			o.id, err)
	}
	// Before anything else, so the series exists on every pod and a sum across pods counts
	// a standby as 0 rather than not at all.
	o.hooks.SetOwner(o.id, false)

	loopCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lease, err := leases.Acquire(OwnershipPartition(o.id))
	if err != nil {
		o.logNotAcquired(err)
		o.cancel, o.done = cancel, done
		go func() {
			defer close(done)
			o.standby(loopCtx, leases, ownerRetryInterval)
		}()
		return nil
	}
	o.askSucceeded()
	t, err := o.beginTerm(loopCtx, lease)
	if err != nil {
		cancel()
		return fmt.Errorf("external MQTT source %q: %w", o.id, err)
	}
	o.cancel, o.done = cancel, done
	go func() {
		defer close(done)
		o.serve(t)
		o.standby(loopCtx, leases, ownerRetryInterval)
	}()
	return nil
}

func (o *OwnedMqttSource) logNotAcquired(err error) {
	if errors.Is(err, messaging.ErrLeaseHeld) {
		o.askSucceeded()
		log.Debug().Str("source", o.id).Msg("Another pod reads this external MQTT source; standing by.")
		return
	}
	if o.askFailing {
		log.Debug().Err(err).Str("source", o.id).
			Msg("Still cannot ask for the lease on this external MQTT source; asking again.")
		return
	}
	o.askFailing = true
	log.Warn().Err(err).Str("source", o.id).
		Msg("Could not ask for the lease on this external MQTT source; standing by and asking again.")
}

// askSucceeded records an answer from the lease bucket, held or not.
func (o *OwnedMqttSource) askSucceeded() {
	if o.askFailing {
		log.Info().Str("source", o.id).Msg("The lease on this external MQTT source can be asked for again.")
	}
	o.askFailing = false
}

// standby asks for the lease every ownerRetryInterval, first after wait, and runs a term
// whenever it gets it, until ctx ends.
func (o *OwnedMqttSource) standby(ctx context.Context, leases SourceLeases, wait time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = ownerRetryInterval
		lease, err := leases.Acquire(OwnershipPartition(o.id))
		if err != nil {
			o.logNotAcquired(err)
			continue
		}
		o.askSucceeded()
		t, err := o.beginTerm(ctx, lease)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// The term's own context ended under the start: the lease was lost (taken over,
			// or not renewed) while it connected. That says nothing about the broker, so it
			// is neither an error nor a reason to back off; ask again as a standby does.
			if errors.Is(err, context.Canceled) {
				log.Warn().Err(err).Str("source", o.id).
					Msg("Lost the lease on this external MQTT source while starting to read it; standing by.")
				continue
			}
			// 🔴 ONLY A REFUSAL ENDS THE PROCESS. It is the broker's answer about this
			// filter, every pod would be given the same one, and it is what ends the process
			// on a reconnect too (see classifyResubscribe). An unreachable broker is not: it
			// is the operator's broker, it may be down for maintenance, and ending this pod
			// for it would take HTTP and gateway ingest down with it, and then crash-loop
			// every pod in turn. The lease is already released; the owner gauge reads 0
			// on every pod, which is what the no-reader alert watches.
			if errors.Is(err, messaging.ErrSubscriptionRefused) {
				o.hooks.Fail(fmt.Errorf("external MQTT source %q took over from another pod, but its broker "+
					"refused the subscription: %w", o.id, err))
				return
			}
			log.Error().Err(err).Str("source", o.id).Dur("retry_in", ownerFailedStartBackoff).
				Msg("Took over an external MQTT source but could not start reading it; released it and will try again.")
			wait = ownerFailedStartBackoff
			continue
		}
		o.serve(t)
	}
}

// term is one held ownership of the source.
type term struct {
	ctx    context.Context
	cancel context.CancelFunc
	lease  SourceLease
	src    TermSource
	// running is the term's own goroutines: the renewer and the loss watcher.
	running sync.WaitGroup
}

// beginTerm starts a term on a lease just acquired. On error, everything it started has
// been stopped and the lease released.
func (o *OwnedMqttSource) beginTerm(ctx context.Context, lease SourceLease) (*term, error) {
	termCtx, cancel := context.WithCancel(ctx)
	t := &term{ctx: termCtx, cancel: cancel, lease: lease}
	// The renewer first, before anything that can take time: see the type comment.
	t.running.Add(1)
	go func() {
		defer t.running.Done()
		if errors.Is(lease.KeepAlive(termCtx, ownerRenewInterval), messaging.ErrNotHolder) {
			log.Error().Str("source", o.id).Msg("Lost the lease on this external MQTT source; stopping reading it.")
			cancel()
		}
	}()
	holder, err := lease.WatchHolder(termCtx)
	if err != nil {
		o.endTerm(t)
		return nil, fmt.Errorf("could not watch its lease: %w", err)
	}
	t.running.Add(1)
	go func() {
		defer t.running.Done()
		select {
		case <-holder.Lost():
			log.Error().Str("source", o.id).Msg("Another pod took this external MQTT source over; stopping reading it.")
			cancel()
		case <-termCtx.Done():
		}
	}()
	src, err := o.newSource(o.want, func() bool {
		if holder.Held() {
			return true
		}
		o.hooks.NotOwner(o.id)
		return false
	})
	if err != nil {
		o.endTerm(t)
		return nil, err
	}
	t.src = src
	if err = src.Initialize(termCtx); err == nil {
		err = src.Start(termCtx)
	}
	if err != nil {
		o.endTerm(t)
		return nil, err
	}
	o.hooks.SetOwner(o.id, true)
	log.Info().Str("source", o.id).Str("client_id", o.want).Msg("This pod now reads this external MQTT source.")
	return t, nil
}

// serve holds a term until its context ends, then ends it.
func (o *OwnedMqttSource) serve(t *term) {
	<-t.ctx.Done()
	o.endTerm(t)
}

// endTerm tears a term down. 🔴 THE ORDER IS THE POINT: stop reading (disconnect, and
// finish what was already taken) BEFORE releasing, so nothing is read for this term once
// another pod can acquire the source; join the term's goroutines before the release, so
// nothing of the term outlives it.
func (o *OwnedMqttSource) endTerm(t *term) {
	o.hooks.SetOwner(o.id, false)
	if t.src != nil {
		if err := t.src.Stop(context.Background()); err != nil {
			log.Warn().Err(err).Str("source", o.id).Msg("Error stopping an external MQTT source's term.")
		}
		_ = t.src.Terminate(context.Background())
	}
	t.cancel()
	t.running.Wait()
	if err := t.lease.Release(); err != nil {
		log.Warn().Err(err).Str("source", o.id).
			Msg("Could not release the lease on this external MQTT source; it ages out on its own.")
	}
}

func (o *OwnedMqttSource) Stop(ctx context.Context) error { return o.lifecycle.Stop(ctx) }

// ExecuteStop returns only once the current term's source has disconnected and its lease
// is released, which is what lets another pod take over within one retry interval.
func (o *OwnedMqttSource) ExecuteStop(context.Context) error {
	if o.cancel == nil {
		return nil
	}
	o.cancel()
	<-o.done
	o.cancel = nil
	return nil
}

func (o *OwnedMqttSource) Terminate(ctx context.Context) error { return o.lifecycle.Terminate(ctx) }

func (o *OwnedMqttSource) ExecuteTerminate(context.Context) error { return nil }
