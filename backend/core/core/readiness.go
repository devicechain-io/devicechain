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

// Validator returns the live JWT validator, or nil while the gate is still
// closed. Callers must treat nil as "not yet authenticated" and fail closed.
func (g *ReadinessGate) Validator() *auth.Validator {
	return g.validator.Load()
}

// MarkReady records the live validator and opens the gate. It is idempotent: the
// first call wins and later calls are no-ops, so the gate never flaps. Services
// whose validator is available synchronously (e.g. user-management, which signs
// and verifies with its own local key) call this directly; others reach it
// through StartAuthGate once the background fetch succeeds.
func (g *ReadinessGate) MarkReady(validator *auth.Validator) {
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
func (ms *Microservice) MarkReady(validator *auth.Validator) {
	ms.Readiness.MarkReady(validator)
	if ms.readyGauge != nil {
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
func (ms *Microservice) StartAuthGate(ctx context.Context, fetch func(context.Context) (*auth.Validator, error)) {
	go func() {
		for {
			if ms.authAttempts != nil {
				ms.authAttempts.Inc()
			}
			validator, err := fetch(ctx)
			if err == nil {
				ms.MarkReady(validator)
				log.Info().Msg("Auth is live; service is ready and the data plane is released.")
				return
			}
			if ms.authFailures != nil {
				ms.authFailures.Inc()
			}
			log.Warn().Err(err).Msg("Auth not yet live; service remains not-ready (degraded). Retrying.")
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
