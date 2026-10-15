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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// heldAsync is an AsyncEmitter that queues the transition and holds its completion, so a test
// decides WHEN and HOW each emit ends. It answers nothing on its own: that is what makes
// "the wake fired before the emit completed" observable instead of racy.
type heldAsync struct {
	mu   sync.Mutex
	held []heldEmit
}

type heldEmit struct {
	tenant, device string
	ev             adapter.PresenceEvent
	done           func(error)
}

func (h *heldAsync) EmitPresenceAsync(_ context.Context, tenant, _ string, device string,
	ev adapter.PresenceEvent, done func(error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.held = append(h.held, heldEmit{tenant, device, ev, done})
}

func (h *heldAsync) pending() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.held)
}

// finish completes the i-th held emit with err, on a goroutine of its own as a writer's
// completion would.
func (h *heldAsync) finish(t *testing.T, i int, err error) {
	t.Helper()
	h.mu.Lock()
	require.Less(t, i, len(h.held))
	done := h.held[i].done
	h.mu.Unlock()
	finished := make(chan struct{})
	go func() { defer close(finished); done(err) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("a completion callback blocked: it must be quick, it runs on the writer's goroutine")
	}
}

func asyncMetrics() (Metrics, *prometheus.CounterVec, prometheus.Counter) {
	emitted := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "e"}, []string{"state"})
	failed := prometheus.NewCounter(prometheus.CounterOpts{Name: "f"})
	return Metrics{Emitted: emitted, Failed: failed}, emitted, failed
}

func connectAdvisoryFor(device string, startNano int64) []byte {
	return []byte(fmt.Sprintf(`{"client":{"client_id":"inst-1:acme:%s","client_type":"mqtt","start":%q}}`,
		device, time.Unix(0, startNano).UTC().Format(time.RFC3339Nano)))
}

// 🔴 THE WAKE FIRES ONLY AFTER AN ACKNOWLEDGED CONNECT. Through the tap's real Handle, with
// completions the test controls: nothing wakes while the emit is merely queued, nothing wakes
// when it fails, and exactly one wake goes out when a connect is acknowledged.
func TestAsyncWakeFiresOnlyAfterTheConnectIsAcknowledged(t *testing.T) {
	waker := &spyWaker{}
	held := &heldAsync{}
	m, emitted, failed := asyncMetrics()
	tap := NewTap(testInstance, "mqtt1", failingEmitter{errors.New("the sync path must not be used")}, nil, m).
		WithWaker(waker).WithAsyncEmitter(held)

	base := time.Now().UnixNano()
	tap.Handle(context.Background(), true, connectAdvisoryFor("ok", base))
	tap.Handle(context.Background(), true, connectAdvisoryFor("lost", base+1))
	require.Equal(t, 2, held.pending(), "Handle must queue the emit and return")
	require.Empty(t, waker.seen(), "a wake went out for a transition that was only QUEUED")
	assert.Zero(t, testutil.ToFloat64(failed), "a failure was counted before any emit ended")
	assert.Zero(t, testutil.ToFloat64(emitted.WithLabelValues(labelConnected)), "an emit counted before it was acknowledged")

	held.finish(t, 1, errors.New("no PubAck"))
	require.Empty(t, waker.seen(), "a FAILED connect woke the device's commands")
	assert.Equal(t, 1.0, testutil.ToFloat64(failed), "a failed emit must be counted")
	assert.Zero(t, testutil.ToFloat64(emitted.WithLabelValues(labelConnected)), "a failed emit was counted as emitted")

	held.finish(t, 0, nil)
	got := waker.seen()
	require.Len(t, got, 1, "an acknowledged connect must wake exactly once")
	assert.Equal(t, wakeRequest{tenant: "acme", deviceToken: "ok"}, got[0])
	assert.Equal(t, 1.0, testutil.ToFloat64(emitted.WithLabelValues(labelConnected)))
	assert.Equal(t, 1.0, testutil.ToFloat64(failed), "the failure count must not move on a success")
}

// A disconnect has nothing to release: acknowledged or not, it wakes nothing, and an
// acknowledged one is counted under its own state.
func TestAsyncDisconnectWakesNothing(t *testing.T) {
	waker := &spyWaker{}
	held := &heldAsync{}
	m, emitted, _ := asyncMetrics()
	tap := NewTap(testInstance, "mqtt1", failingEmitter{errors.New("sync")}, nil, m).
		WithWaker(waker).WithAsyncEmitter(held)

	raw := []byte(fmt.Sprintf(`{"client":{"client_id":"inst-1:acme:dev","client_type":"mqtt","start":%q,"stop":%q},"reason":"Client Closed"}`,
		time.Unix(0, 1_000).UTC().Format(time.RFC3339Nano), time.Unix(0, 2_000).UTC().Format(time.RFC3339Nano)))
	tap.Handle(context.Background(), false, raw)
	require.Equal(t, 1, held.pending())
	held.finish(t, 0, nil)

	require.Empty(t, waker.seen())
	assert.Equal(t, 1.0, testutil.ToFloat64(emitted.WithLabelValues(labelDisconnected)))
}

// The reconciler's door stays SYNCHRONOUS: with an async emitter attached, Apply still goes
// through the plain Emitter and reports whether that write landed, which is what the repair
// counter and the demoter's pacing read.
func TestApplyStaysSynchronousWhenTheTapEmitsAsynchronously(t *testing.T) {
	held := &heldAsync{}
	plain := newRecordingEmitter()
	tap := NewTap(testInstance, "mqtt1", plain, nil, Metrics{}).WithAsyncEmitter(held)

	ok := tap.Apply(context.Background(), Transition{Tenant: "acme", DeviceToken: "pump-1",
		Event: presenceEventAt(true, 100, time.Now())})
	require.True(t, ok)
	require.Len(t, plain.all(), 1, "Apply must write through the synchronous emitter")
	require.Zero(t, held.pending(), "Apply must not queue on the async emitter")

	failing := NewTap(testInstance, "mqtt1", failingEmitter{errors.New("down")}, nil, Metrics{}).WithAsyncEmitter(held)
	require.False(t, failing.Apply(context.Background(), Transition{Tenant: "acme", DeviceToken: "pump-1",
		Event: presenceEventAt(true, 100, time.Now())}), "Apply must report a failed write")
	require.Zero(t, held.pending())
}

// An advisory that the admission gate refuses is not queued, and the refusal is counted as
// it is on the synchronous path.
func TestAsyncRefusedTransitionIsNotQueued(t *testing.T) {
	held := &heldAsync{}
	refused := prometheus.NewCounter(prometheus.CounterOpts{Name: "r"})
	tap := NewTap(testInstance, "mqtt1", failingEmitter{errors.New("sync")},
		func(string, string, time.Time, bool) bool { return false }, Metrics{Refused: refused}).WithAsyncEmitter(held)

	tap.Handle(context.Background(), true, connectAdvisoryFor("dev", time.Now().UnixNano()))
	assert.Zero(t, held.pending())
	assert.Equal(t, 1.0, testutil.ToFloat64(refused))
}

// A publisher asked to apply asynchronously with no async emitter FAILS LOUDLY: the
// transition is counted as failed, not silently dropped and not silently written
// synchronously.
func TestApplyAsyncWithoutAnEmitterIsCountedAsFailed(t *testing.T) {
	m, _, failed := asyncMetrics()
	waker := &spyWaker{}
	p := NewPublisher("mqtt1", failingEmitter{errors.New("sync")}, nil, m)
	p.waker = waker

	p.ApplyAsync(context.Background(), Transition{Tenant: "acme", DeviceToken: "d",
		Event: presenceEventAt(true, 100, time.Now())})
	assert.Equal(t, 1.0, testutil.ToFloat64(failed))
	assert.Empty(t, waker.seen())
}
