// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 THE LOOP'S EXIT CONDITION AND ITS SUCCESS MESSAGE CAME APART the moment MarkReady
// gained the right to refuse. "fetch returned no error" used to mean "the gate is open";
// it no longer does, and the version that kept the old reading would leave the loop, log
// "Auth is live; service is ready and the data plane is released", and leave a permanently
// shut gate behind it with nothing retrying and nothing counting the failure.
//
// FetchValidatorForInstance cannot return (nil, nil) today, so this is latent. It is
// pinned anyway: the property that keeps changing is what the caller happens to do, and
// the whole point of the refusal is that the gate stops trusting its callers.

// TestStartAuthGateKeepsRetryingWhenTheGateRefuses drives both halves in one loop, and
// the second half is what makes the first half's evidence complete: it is not enough that
// the gate stayed closed, the loop has to still be ALIVE and able to open it when a real
// validator finally arrives. Running both phases against one goroutine also lets the loop
// terminate before the test restores authGateRetryInterval — a version that left it
// spinning was caught by -race writing that var under the goroutine's read.
func TestStartAuthGateKeepsRetryingWhenTheGateRefuses(t *testing.T) {
	defer func(d time.Duration) { authGateRetryInterval = d }(authGateRetryInterval)
	authGateRetryInterval = 2 * time.Millisecond

	ms := &Microservice{Readiness: NewReadinessGate()}
	v := &auth.Validator{}

	var mu sync.Mutex
	attempts := 0
	refusing := true
	fetch := func(context.Context) (*auth.Validator, error) {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if refusing {
			// The pair that reads as success and carries nothing.
			return nil, nil
		}
		return v, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ms.StartAuthGate(ctx, fetch)

	// Phase 1: while every fetch yields no validator, the gate must stay shut for as long
	// as anyone looks — not merely be shut at one instant after the loop gave up.
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		require.False(t, ms.Readiness.Ready(),
			"the gate opened on a fetch that produced no validator")
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	refused := attempts
	mu.Unlock()
	require.Greater(t, refused, 1,
		"the fixture is wrong: the loop stopped fetching, so nothing was being retried")

	// Phase 2: a real validator arrives. A loop that had exited on the refused open would
	// never see it, so this is what proves the retry was real rather than the gate merely
	// being slow.
	mu.Lock()
	refusing = false
	mu.Unlock()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer waitCancel()
	require.NoError(t, ms.Readiness.WaitReady(waitCtx),
		"the loop must still have been running to pick up the validator")
	assert.Same(t, v, ms.Readiness.Validator())

	mu.Lock()
	defer mu.Unlock()
	assert.Greater(t, attempts, refused,
		"the loop must have fetched again after the refusals")
}

// TestMarkReadyReportsWhetherTheGateIsOpen pins the signal the loop now reads. It answers
// about the GATE, not about this call: a second call on an already-open gate is still
// "auth is live", which is what keeps an idempotent re-open from reading as a failure.
func TestMarkReadyReportsWhetherTheGateIsOpen(t *testing.T) {
	g := NewReadinessGate()
	assert.False(t, g.MarkReady(nil), "a refused open must not report the gate as open")

	v := &auth.Validator{}
	assert.True(t, g.MarkReady(v))
	assert.True(t, g.MarkReady(&auth.Validator{}),
		"a second open is a no-op, but the gate IS open and must say so")
	assert.True(t, g.MarkReady(nil),
		"even a refused call reports the gate's own state, which is open")

	ms := &Microservice{Readiness: NewReadinessGate()}
	assert.False(t, ms.MarkReady(nil))
	assert.True(t, ms.MarkReady(v))
}
