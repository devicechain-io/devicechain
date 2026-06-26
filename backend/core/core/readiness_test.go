// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
)

// A fresh gate is closed: not ready, no validator, and WaitReady blocks.
func TestReadinessGate_StartsClosed(t *testing.T) {
	g := NewReadinessGate()

	assert.False(t, g.Ready())
	assert.Nil(t, g.Validator())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, g.WaitReady(ctx), context.DeadlineExceeded)
}

// MarkReady opens the gate, exposes the validator, and releases WaitReady.
func TestReadinessGate_MarkReadyOpens(t *testing.T) {
	g := NewReadinessGate()
	v := &auth.Validator{}

	g.MarkReady(v)

	assert.True(t, g.Ready())
	assert.Same(t, v, g.Validator())
	assert.NoError(t, g.WaitReady(context.Background()))
}

// MarkReady is idempotent: the first validator wins and the gate never flaps.
func TestReadinessGate_MarkReadyIdempotent(t *testing.T) {
	g := NewReadinessGate()
	first := &auth.Validator{}
	second := &auth.Validator{}

	g.MarkReady(first)
	g.MarkReady(second)

	assert.Same(t, first, g.Validator())
	assert.True(t, g.Ready())
}

// A WaitReady caller blocked on a closed gate is released when it opens.
func TestReadinessGate_WaitReadyReleasedOnOpen(t *testing.T) {
	g := NewReadinessGate()
	done := make(chan error, 1)
	go func() { done <- g.WaitReady(context.Background()) }()

	// Give the goroutine a moment to park, then open the gate.
	time.Sleep(10 * time.Millisecond)
	g.MarkReady(&auth.Validator{})

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("WaitReady was not released when the gate opened")
	}
}

// A fresh gate is not draining.
func TestReadinessGate_StartsNotDraining(t *testing.T) {
	g := NewReadinessGate()
	assert.False(t, g.Draining())
}

// BeginDrain flips Draining without disturbing the open gate or the validator,
// so /readyz reports 503 while in-flight requests still authenticate.
func TestReadinessGate_BeginDrain(t *testing.T) {
	g := NewReadinessGate()
	v := &auth.Validator{}
	g.MarkReady(v)

	g.BeginDrain()

	assert.True(t, g.Draining())
	// The gate stays open and the validator stays live for in-flight requests.
	assert.True(t, g.Ready())
	assert.Same(t, v, g.Validator())
}

// BeginDrain is idempotent.
func TestReadinessGate_BeginDrainIdempotent(t *testing.T) {
	g := NewReadinessGate()
	g.BeginDrain()
	g.BeginDrain()
	assert.True(t, g.Draining())
}

// StartAuthGate opens the gate once the background fetch succeeds, after retrying
// past transient failures.
func TestStartAuthGate_OpensAfterRetries(t *testing.T) {
	defer func(d time.Duration) { authGateRetryInterval = d }(authGateRetryInterval)
	authGateRetryInterval = 5 * time.Millisecond

	ms := &Microservice{Readiness: NewReadinessGate()}
	v := &auth.Validator{}
	attempts := 0
	fetch := func(context.Context) (*auth.Validator, error) {
		attempts++
		if attempts < 2 {
			return nil, context.DeadlineExceeded // stand-in transient error
		}
		return v, nil
	}

	ms.StartAuthGate(context.Background(), fetch)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	assert.NoError(t, ms.Readiness.WaitReady(ctx))
	assert.Same(t, v, ms.Readiness.Validator())
}

// StartAuthGate stops retrying when its context is cancelled and never opens.
func TestStartAuthGate_StopsOnCancel(t *testing.T) {
	ms := &Microservice{Readiness: NewReadinessGate()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ms.StartAuthGate(ctx, func(context.Context) (*auth.Validator, error) {
		return nil, context.Canceled
	})

	time.Sleep(20 * time.Millisecond)
	assert.False(t, ms.Readiness.Ready())
}
