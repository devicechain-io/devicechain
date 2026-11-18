// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	corepresence "github.com/devicechain-io/dc-microservice/presence"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// refusalAdvisoryFor is a DISCONNECT advisory for device, for the connection that started at
// startNano and stopped at stopNano, with the broker's reason.
func refusalAdvisoryFor(device string, startNano, stopNano int64, reason string) []byte {
	return []byte(fmt.Sprintf(`{"client":{"client_id":"inst-1:acme:%s","client_type":"mqtt","start":%q,"stop":%q},"reason":%q}`,
		device, time.Unix(0, startNano).UTC().Format(time.RFC3339Nano),
		time.Unix(0, stopNano).UTC().Format(time.RFC3339Nano), reason))
}

func debounceMetrics() (Metrics, prometheus.Counter, prometheus.Counter) {
	m, _, _ := asyncMetrics()
	m.Deferred = prometheus.NewCounter(prometheus.CounterOpts{Name: "d"})
	m.DeferredDropped = prometheus.NewCounter(prometheus.CounterOpts{Name: "x"})
	return m, m.Deferred, m.DeferredDropped
}

// blockingAsync holds the writer call until released, optionally honouring cancellation.
type blockingAsync struct {
	entered   chan struct{}
	release   chan struct{}
	honourCtx bool
	once      sync.Once
	mu        sync.Mutex
	ctx       context.Context
	err       error
}

func (b *blockingAsync) EmitPresenceAsync(ctx context.Context, _, _, _ string,
	_ adapter.PresenceEvent, done func(error)) {
	b.mu.Lock()
	b.ctx = ctx
	b.mu.Unlock()
	b.once.Do(func() { close(b.entered) })
	var cancelled <-chan struct{}
	if b.honourCtx {
		cancelled = ctx.Done()
	}
	select {
	case <-b.release:
	case <-cancelled:
	}
	err := ctx.Err()
	b.mu.Lock()
	b.err = err
	b.mu.Unlock()
	done(err)
}

// 🔑 A REFUSAL-CLOSE IS HELD BACK, AND NOTHING ELSE IS. The broker closes a connection whose
// publish the full message store refused, with the reason RefusalCloseReason. That disconnect is
// emitted only after the debounce, unchanged (the broker's stop time and the connection's
// session); a disconnect for any other reason, and every connect, is emitted at once.
//
// GUARD: kills a debounce that is not applied, one applied to every disconnect (a device that
// hung up would read online for the hold), one that holds a connect, and one that rewrites the
// held disconnect's time or session.
func TestARefusalCloseDisconnectIsHeldBackForTheDebounce(t *testing.T) {
	held := &heldAsync{}
	m, deferred, _ := debounceMetrics()
	tap := NewTap(testInstance, "mqtt1", failingEmitter{errors.New("sync")}, nil, m).
		WithAsyncEmitter(held).WithRefusalDebounce(300 * time.Millisecond)
	require.Equal(t, RefusalCloseDebounce, NewTap(testInstance, "mqtt1", nil, nil, Metrics{}).debounce,
		"production holds for RefusalCloseDebounce")

	tap.Handle(context.Background(), false, refusalAdvisoryFor("refused", 1_000, 2_000, RefusalCloseReason))
	tap.Handle(context.Background(), false, refusalAdvisoryFor("gone", 1_000, 2_000, "Client Closed"))
	tap.Handle(context.Background(), true, connectAdvisoryFor("born", 3_000))
	require.Equal(t, 2, held.pending(), "only the refusal-close may be held back")
	require.Equal(t, 1, tap.DeferredDeaths())
	require.Equal(t, 1.0, testutil.ToFloat64(deferred))

	require.Eventually(t, func() bool { return held.pending() == 3 }, 5*time.Second, 10*time.Millisecond,
		"the held disconnect was never emitted")
	require.Zero(t, tap.DeferredDeaths())
	held.mu.Lock()
	last := held.held[2]
	held.mu.Unlock()
	require.Equal(t, "refused", last.device)
	require.False(t, last.ev.Connected)
	require.Equal(t, RefusalCloseReason, last.ev.Reason, "the broker's reason travels on the event")
	require.Equal(t, uint64(1_000), last.ev.SessionId)
	require.True(t, last.ev.OccurredAt.Equal(time.Unix(0, 2_000)), "the held disconnect keeps the broker's stop time")
}

// 🔑 WHAT THE HOLD IS FOR. A device refused and reconnecting within the debounce has a newer
// session by the time its held disconnect is emitted, so every consumer that orders presence
// through presence.Decide (the projection, DETECT's connectivity rules, command-delivery's gate)
// refuses the disconnect: the device never reads offline. A device that does not come back is
// read offline once the hold ends.
//
// GUARD: kills a debounce whose late disconnect would still flip a reconnected device offline
// (an emitted death carrying the NEW session, or a re-stamped one).
func TestADeviceThatReconnectsWithinTheDebounceNeverReadsOffline(t *testing.T) {
	held := &heldAsync{}
	m, _, _ := debounceMetrics()
	tap := NewTap(testInstance, "mqtt1", failingEmitter{errors.New("sync")}, nil, m).
		WithAsyncEmitter(held).WithRefusalDebounce(200 * time.Millisecond)

	base := time.Now().Add(-time.Minute).UnixNano()
	tap.Handle(context.Background(), true, connectAdvisoryFor("pump", base))
	tap.Handle(context.Background(), false, refusalAdvisoryFor("pump", base, base+1_000_000, RefusalCloseReason))
	tap.Handle(context.Background(), true, connectAdvisoryFor("pump", base+2_000_000))
	require.Eventually(t, func() bool { return held.pending() == 3 }, 5*time.Second, 10*time.Millisecond)

	var prior corepresence.Prior
	held.mu.Lock()
	emitted := append([]heldEmit(nil), held.held...)
	held.mu.Unlock()
	for _, e := range emitted {
		claim := corepresence.ClaimDisconnected
		if e.ev.Connected {
			claim = corepresence.ClaimConnected
		}
		d := corepresence.Decide(prior, corepresence.Incoming{SessionId: e.ev.SessionId, OccurredAt: e.ev.OccurredAt, Claim: claim})
		if d.Ordered {
			prior = corepresence.Prior{SessionId: e.ev.SessionId, Time: e.ev.OccurredAt, HasTime: true,
				Connected: e.ev.Connected}
		}
	}
	require.True(t, prior.Connected, "the held disconnect flipped a reconnected device offline")
	require.Equal(t, uint64(base+2_000_000), prior.SessionId)

	// The control: no reconnect, and the held disconnect is applied when the hold ends.
	prior = corepresence.Prior{SessionId: uint64(base), Time: time.Unix(0, base), HasTime: true, Connected: true}
	d := corepresence.Decide(prior, corepresence.Incoming{SessionId: emitted[2].ev.SessionId,
		OccurredAt: emitted[2].ev.OccurredAt, Claim: corepresence.ClaimDisconnected})
	require.True(t, d.Ordered, "control: a device that does not reconnect reads offline after the hold")
}

// A stopping tap drops what it holds (counted; the reconciler recovers it) and holds nothing
// more: a held disconnect must not be emitted through a writer the service has closed.
//
// GUARD: kills a stop that leaves the held disconnects to fire later, and one that keeps
// holding after it.
func TestAStoppedTapDropsWhatItHeldBack(t *testing.T) {
	held := &heldAsync{}
	m, _, dropped := debounceMetrics()
	tap := NewTap(testInstance, "mqtt1", failingEmitter{errors.New("sync")}, nil, m).
		WithAsyncEmitter(held).WithRefusalDebounce(100 * time.Millisecond)
	tap.Handle(context.Background(), false, refusalAdvisoryFor("a", 1_000, 2_000, RefusalCloseReason))
	tap.Handle(context.Background(), false, refusalAdvisoryFor("b", 1_000, 2_000, RefusalCloseReason))
	tap.StopDeferred()
	tap.StopDeferred()
	require.Equal(t, 2.0, testutil.ToFloat64(dropped))
	time.Sleep(300 * time.Millisecond)
	require.Zero(t, held.pending(), "a held disconnect fired after the tap stopped")

	tap.Handle(context.Background(), false, refusalAdvisoryFor("c", 1_000, 2_000, RefusalCloseReason))
	require.Equal(t, 1, held.pending(), "a stopped tap must emit at once, not hold")
}

// A stop joins a held disconnect whose hold has already ended, even when the writer ignores
// cancellation: the caller must be able to close the writer after the stop returns.
//
// GUARD: kills a stop that returns while a held disconnect is still being emitted (a stop that does not wait for it).
func TestStopDeferredWaitsForAHeldDisconnectAlreadyBeingEmitted(t *testing.T) {
	writer := &blockingAsync{entered: make(chan struct{}), release: make(chan struct{})}
	m, _, _ := debounceMetrics()
	tap := NewTap(testInstance, "mqtt1", failingEmitter{errors.New("sync")}, nil, m).
		WithAsyncEmitter(writer).WithRefusalDebounce(time.Millisecond)
	t.Cleanup(func() {
		select {
		case <-writer.release:
		default:
			close(writer.release)
		}
		tap.StopDeferred()
	})
	tap.Handle(context.Background(), false, refusalAdvisoryFor("a", 1_000, 2_000, RefusalCloseReason))
	select {
	case <-writer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the held disconnect never entered the writer")
	}

	returned := make(chan struct{})
	go func() {
		tap.StopDeferred()
		close(returned)
	}()
	select {
	case <-returned:
		t.Fatal("StopDeferred returned while a held disconnect was still being emitted")
	case <-time.After(200 * time.Millisecond):
	}
	close(writer.release)
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("StopDeferred did not return after the held emit finished")
	}
}

// A stop cancels an in-flight held emit so a slow writer that honours its context cannot
// hold shutdown indefinitely.
//
// GUARD: kills a stop that leaves the in-flight held emit on a context nothing cancels.
func TestStopDeferredCancelsAHeldDisconnectBeingEmitted(t *testing.T) {
	writer := &blockingAsync{
		entered: make(chan struct{}), release: make(chan struct{}), honourCtx: true,
	}
	m, _, _ := debounceMetrics()
	tap := NewTap(testInstance, "mqtt1", failingEmitter{errors.New("sync")}, nil, m).
		WithAsyncEmitter(writer).WithRefusalDebounce(time.Millisecond)
	t.Cleanup(func() {
		close(writer.release)
		tap.StopDeferred()
	})
	tap.Handle(context.Background(), false, refusalAdvisoryFor("a", 1_000, 2_000, RefusalCloseReason))
	select {
	case <-writer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the held disconnect never entered the writer")
	}

	returned := make(chan struct{})
	go func() {
		tap.StopDeferred()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("a slow writer held the stop: the in-flight held emit was not cancelled")
	}
	writer.mu.Lock()
	err := writer.err
	writer.mu.Unlock()
	require.ErrorIs(t, err, context.Canceled)
}

// Past the ceiling a refusal-close is emitted at once, as before the debounce existed: the set is
// bounded however many devices are refused.
func TestTheDebounceIsBounded(t *testing.T) {
	held := &heldAsync{}
	m, _, _ := debounceMetrics()
	tap := NewTap(testInstance, "mqtt1", failingEmitter{errors.New("sync")}, nil, m).
		WithAsyncEmitter(held).WithRefusalDebounce(time.Hour)
	t.Cleanup(tap.StopDeferred)
	tap.deferMu.Lock()
	for i := 0; i < maxDeferredDeaths; i++ {
		tap.deferred[time.NewTimer(time.Hour)] = struct{}{}
	}
	tap.deferMu.Unlock()
	tap.Handle(context.Background(), false, refusalAdvisoryFor("over", 1_000, 2_000, RefusalCloseReason))
	require.Equal(t, 1, held.pending(), "past the ceiling the disconnect must be emitted at once")
}

// 🔴 A HELD DISCONNECT IS NOT A CLOCK PROBLEM. A device refused and reconnected within the hold
// has its newer session emitted first, so its held disconnect's session is lower by design. The
// session-regression counter exists to point at a broker node whose clock trails its peers;
// counting every debounced refusal there would make it read as fleet-wide clock skew during the
// very overload that caused the refusals.
//
// GUARD: kills a held disconnect emitted through the regression check. The control (a genuine
// backwards step on a connect) proves the counter is wired and still counts.
func TestAHeldDisconnectIsNotCountedAsASessionRegression(t *testing.T) {
	held := &heldAsync{}
	m, _, _ := debounceMetrics()
	regressed := prometheus.NewCounter(prometheus.CounterOpts{Name: "r"})
	m.RegressedSessions = regressed
	tap := NewTap(testInstance, "mqtt1", failingEmitter{errors.New("sync")}, nil, m).
		WithAsyncEmitter(held).WithRefusalDebounce(100 * time.Millisecond)

	base := time.Now().Add(-time.Minute).UnixNano()
	tap.Handle(context.Background(), true, connectAdvisoryFor("pump", base))
	tap.Handle(context.Background(), false, refusalAdvisoryFor("pump", base, base+1_000_000, RefusalCloseReason))
	tap.Handle(context.Background(), true, connectAdvisoryFor("pump", base+2_000_000))
	require.Eventually(t, func() bool { return held.pending() == 3 }, 5*time.Second, 10*time.Millisecond)
	require.Zero(t, testutil.ToFloat64(regressed), "a held disconnect was counted as a broker clock going backwards")

	// The control: a connect whose session is genuinely below the one already emitted.
	tap.Handle(context.Background(), true, connectAdvisoryFor("pump", base+1_000))
	require.Equal(t, 1.0, testutil.ToFloat64(regressed), "control: a real backwards step is counted")
}

// The hold is the documented one: the operator documentation says a refusal-close is not
// reported for 30 seconds, so a change to it is a change to that page too.
func TestTheRefusalCloseDebounceIsTheDocumentedThirtySeconds(t *testing.T) {
	require.Equal(t, 30*time.Second, RefusalCloseDebounce)
}
