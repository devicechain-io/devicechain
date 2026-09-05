// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/rs/zerolog/log"
)

// authGateRetryInterval is how often the background auth bootstrap re-attempts
// the JWKS fetch while the service is degraded. The fetch itself is a single
// attempt (FetchValidatorForInstance); this loop owns the cadence. It is a var
// so tests can shorten it.
var authGateRetryInterval = 5 * time.Second

// ReadinessGate is the data-plane readiness latch for a service (ADR-022
// decision 3). A service starts not-ready: until the JWT validator is live, the
// readiness probe reports 503 and NATS consumers stay paused, so the service
// processes nothing without a verified-auth capability (preserving the ADR-015
// fail-closed invariant). Once the validator is fetched the gate opens once and
// stays open; it never closes again for the life of the process.
//
// Reads are lock-free: readiness is the closed state of readyCh and the
// validator is an atomic pointer, so the request and consume hot paths never
// contend on a mutex. once makes the open transition happen exactly once.
type ReadinessGate struct {
	once      sync.Once
	validator atomic.Pointer[auth.Validator]
	// readyCh is closed exactly once when the gate opens, so WaitReady can block
	// without polling and Ready can test without a lock. Created at construction
	// and never reassigned.
	readyCh chan struct{}
	// draining is set once at shutdown so the /readyz probe starts reporting 503
	// while the server can still serve in-flight requests. It is a separate,
	// one-way latch from the open gate: opening means "auth is live", draining
	// means "stop sending me new traffic" — a pod can be open *and* draining
	// during graceful shutdown (zero-downtime rollouts, methodology §10.2).
	draining atomic.Bool
}

// NewReadinessGate creates a closed (not-ready) gate.
func NewReadinessGate() *ReadinessGate {
	return &ReadinessGate{readyCh: make(chan struct{})}
}

// Ready reports whether the gate has opened (auth is live).
func (g *ReadinessGate) Ready() bool {
	select {
	case <-g.readyCh:
		return true
	default:
		return false
	}
}

// Validator returns the live JWT validator, or nil. Callers must treat nil as
// "not authenticated" and fail closed.
//
// 🔑 NIL HAS TWO CAUSES AND ONLY ONE OF THEM IS TEMPORARY. The gate may still be
// closed, in which case a validator arrives later; or the service opened the gate
// through MarkReadyWithoutAuthSurface because it verifies no tokens at all, in
// which case nil is the permanent, correct answer. Nothing here distinguishes
// them, and nothing needs to: both mean the same thing to a caller holding a
// token. What matters is that an earlier version of this comment said nil meant
// "still closed", which read as a promise that a READY service always has one —
// and two services have been opening the gate with nil since they were written.
func (g *ReadinessGate) Validator() *auth.Validator {
	return g.validator.Load()
}

// MarkReady records the live validator and opens the gate. It is idempotent: the
// first call wins and later calls are no-ops, so the gate never flaps. Services
// whose validator is available synchronously (e.g. user-management, which signs
// and verifies with its own local key) call this directly; others reach it
// through StartAuthGate once the background fetch succeeds.
//
// 🔴 A NIL VALIDATOR IS REFUSED AND THE GATE STAYS CLOSED. Storing it would open
// the gate on a service that has a token-verifying surface and no way to verify a
// token: /readyz answers 200, the endpoint controller sends it traffic, and every
// authenticated request is refused for the life of the process while the pod
// reports healthy. Refusing keeps the pod out of Service endpoints, which is the
// loud failure. A service that legitimately verifies nothing says so once, at its
// call site, with MarkReadyWithoutAuthSurface.
//
// 🔑 IT RETURNS WHETHER THE GATE IS OPEN AFTERWARDS, and that is not decoration. A
// caller that treats "I called MarkReady" as "auth is live" writes down the one
// claim this refusal exists to falsify — StartAuthGate did exactly that, logging
// "Auth is live" and leaving its retry loop on a refused call. The answer is the
// gate's state, not this call's effect, so a second call on an already-open gate
// still says true.
func (g *ReadinessGate) MarkReady(validator *auth.Validator) bool {
	if validator == nil {
		log.Error().Msg("Refusing to open the readiness gate with no JWT validator: this service " +
			"would report ready and then refuse every authenticated request. A service that verifies " +
			"no tokens must open the gate with MarkReadyWithoutAuthSurface instead.")
		return g.Ready()
	}
	g.markReady(validator)
	return true
}

// MarkReadyWithoutAuthSurface opens the gate with NO validator, for a service whose
// HTTP surface is health and metrics only and which therefore verifies no token.
//
// It exists so that "ready with no validator" is something a service STATES rather
// than something it falls into by passing nil. The two are indistinguishable once
// stored, so the difference has to be made at the call site or not at all — and the
// call site is also the only place that knows whether a token ever arrives.
//
// 🔴 IT IS NOT AN OPT-OUT FROM AUTHENTICATION. It asserts there is nothing to
// authenticate. Adding any token-verifying handler to such a service means moving
// it back onto StartInstanceAuthGate; Validator() will otherwise return nil to that
// handler forever, and a handler that fails closed on nil refuses every request.
func (g *ReadinessGate) MarkReadyWithoutAuthSurface() {
	g.markReady(nil)
}

// markReady is the one place the gate opens, so the two doors above differ only in
// what they are allowed to store.
func (g *ReadinessGate) markReady(validator *auth.Validator) {
	g.once.Do(func() {
		g.validator.Store(validator)
		close(g.readyCh)
	})
}

// BeginDrain marks the gate as draining so the /readyz probe begins reporting
// 503, causing the endpoint controllers to pull this pod from Service endpoints.
// It deliberately does NOT touch the live validator or close readyCh: in-flight
// requests still authenticate and any WaitReady'd NATS consumers unwind on
// context cancellation instead. One-way and idempotent.
func (g *ReadinessGate) BeginDrain() {
	g.draining.Store(true)
}

// Draining reports whether graceful shutdown has begun. The /readyz probe treats
// a draining pod as not-ready even though the auth gate is still open.
func (g *ReadinessGate) Draining() bool {
	return g.draining.Load()
}

// WaitReady blocks until the gate opens or ctx is cancelled. It is the pause
// point for NATS consumers: a degraded service parks here instead of draining
// messages. It returns ctx.Err() if the context is cancelled first (shutdown).
func (g *ReadinessGate) WaitReady(ctx context.Context) error {
	select {
	case <-g.readyCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// MarkReady opens the readiness gate and records the readiness metric (E17).
// Services call this rather than ms.Readiness.MarkReady directly so the ready
// signal is exported. It is idempotent (the gate's MarkReady is).
//
// 🔑 THE GAUGE FOLLOWS THE GATE, NOT THE CALL. The gate refuses to open on a nil
// validator, so setting the gauge because MarkReady was CALLED would export a 1
// for a service the gate had just kept closed — the exported signal disagreeing
// with the probe that governs traffic.
func (ms *Microservice) MarkReady(validator *auth.Validator) bool {
	open := ms.Readiness.MarkReady(validator)
	ms.exportReady()
	return open
}

// MarkReadyWithoutAuthSurface opens the gate for a service that verifies no tokens
// and records the readiness metric. See ReadinessGate.MarkReadyWithoutAuthSurface.
func (ms *Microservice) MarkReadyWithoutAuthSurface() {
	ms.Readiness.MarkReadyWithoutAuthSurface()
	ms.exportReady()
}

func (ms *Microservice) exportReady() {
	if ms.readyGauge != nil && ms.Readiness.Ready() {
		ms.readyGauge.Set(1)
	}
}

// StartAuthGate launches the background auth bootstrap and returns immediately
// (ADR-022 decision 3). The service starts not-ready; fetch is attempted
// repeatedly until it succeeds, at which point the gate opens and the data plane
// is released. A slow or absent user-management therefore degrades this service
// rather than failing its startup (amends ADR-008's fatal startup fetch), without
// ever processing traffic before auth is live. The loop exits on ctx
// cancellation (shutdown).
//
// 🔴 THE LOOP ENDS WHEN THE GATE IS OPEN, NOT WHEN THE FETCH RETURNED NO ERROR, and
// those came apart the moment MarkReady gained the right to refuse. A fetch yielding
// (nil, nil) would otherwise leave this loop, log "Auth is live", and leave the gate
// shut for the life of the process with nothing retrying and nothing counting it —
// a success message asserting exactly the invariant that had just failed.
// FetchValidatorForInstance cannot produce that pair today, so this is latent rather
// than live; it is fixed anyway because "the caller happens not to do it" is the
// property that keeps changing, and a log line that states an invariant nothing
// enforces is the shape this whole change exists to remove.
func (ms *Microservice) StartAuthGate(ctx context.Context, fetch func(context.Context) (*auth.Validator, error)) {
	go func() {
		for {
			if ms.authAttempts != nil {
				ms.authAttempts.Inc()
			}
			validator, err := fetch(ctx)
			if err == nil && ms.MarkReady(validator) {
				log.Info().Msg("Auth is live; service is ready and the data plane is released.")
				return
			}
			if ms.authFailures != nil {
				ms.authFailures.Inc()
			}
			if err != nil {
				log.Warn().Err(err).Msg("Auth not yet live; service remains not-ready (degraded). Retrying.")
			} else {
				log.Error().Msg("The auth bootstrap reported success but produced no validator, so the " +
					"readiness gate stayed CLOSED and this service is NOT ready. Retrying.")
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(authGateRetryInterval):
			}
		}
	}()
}

// StartInstanceAuthGate is the standard data-plane wiring: it starts the
// background auth bootstrap against this instance's user-management JWKS
// endpoint (ADR-022 decision 3). Every fetch-based service uses it; only
// user-management, whose validator is local, opens the gate synchronously via
// MarkReady instead.
func (ms *Microservice) StartInstanceAuthGate(ctx context.Context) {
	ms.StartAuthGate(ctx, func(ctx context.Context) (*auth.Validator, error) {
		return auth.FetchValidatorForInstance(ctx, ms.InstanceConfiguration.Infrastructure.UserManagement)
	})
}
