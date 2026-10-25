// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

// hungDevice is an alarm sink where one device's requests never answer within their deadline —
// they end when the dispatch context does, and a little later still, as a publish that gives up
// only after its deadline does — and every other device's are recorded and succeed.
type hungDevice struct {
	hung string
	late time.Duration

	hangs atomic.Int64

	mu      sync.Mutex
	healthy map[string]int // dispatches by device
	firstAt map[string]time.Time
}

func newHungDevice(hung string, late time.Duration) *hungDevice {
	return &hungDevice{hung: hung, late: late, healthy: map[string]int{}, firstAt: map[string]time.Time{}}
}

func (h *hungDevice) Dispatch(ctx context.Context, req react.AlarmRequest) error {
	if req.DeviceToken == h.hung {
		h.hangs.Add(1)
		<-ctx.Done()
		time.Sleep(h.late)
		return ctx.Err()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.healthy[req.DeviceToken]++
	if _, ok := h.firstAt[req.DeviceToken]; !ok {
		h.firstAt[req.DeviceToken] = time.Now()
	}
	return nil
}

func (h *hungDevice) count(device string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.healthy[device]
}

// 🔑 THE REVIEW FINDING, AS A TEST. A delivery's ack clock starts at fetch, so one waiting in its
// lane behind a hung dispatch spends its clock queued. Without a bound on what the hung dispatch
// may take, it runs to the whole of its own deadline, the ones behind it have none left when it
// ends, and each is skipped as expired: no sink was ever asked for them, and each burns a delivery
// attempt anyway. Five in a row dead-letters a HEALTHY event and its alarm is never raised.
//
// The hung device's event shares a lane with two healthy devices' events. The healthy events must
// be dispatched, once each, on their FIRST delivery: inside the ack deadline they share with the
// hung one, not on a redelivery after it, which is the first of the attempts the finding spends.
// Real broker, real capacity reader, the options main builds.
//
// Against the dispatcher this replaces, the hung dispatch holds the lane to the end of the shared
// deadline, both healthy events are skipped as expired, and neither is dispatched on that delivery.
func TestAHungDeviceDoesNotCostItsLaneMatesTheirAttempts(t *testing.T) {
	t.Parallel()
	// AckWait stays above the reader's one second long-poll: a delivery that arrives at the end of
	// a poll is stamped with the poll's start, and a shorter AckWait would call it expired.
	const ackWait = time.Second
	const workers = 4
	sink := newHungDevice("device-0", ackWait/20)
	publish, start, ms := reactBroker(t, ackWait, workers, reactFakeResolver{rule: alarmRule(), found: true},
		func(*messaging.NatsManager) react.AlarmSink { return sink }, nil)

	subject := ms.InstanceId + ".acme.derived-events"
	laneOf := func(dev string) int {
		return laneFor(messaging.Message{Subject: subject, Value: []byte(fmt.Sprintf(`{"series":%q}`, dev))}, workers)
	}
	var mates []string
	for d := 1; d < 500 && len(mates) < 2; d++ {
		if dev := fmt.Sprintf("device-%d", d); laneOf(dev) == laneOf("device-0") {
			mates = append(mates, dev)
		}
	}
	require.Len(t, mates, 2)

	publish(edgeEvent("device-0", 0))
	for _, m := range mates {
		publish(edgeEvent(m, 0))
	}
	began := time.Now()
	start()

	for _, m := range mates {
		require.Eventually(t, func() bool { return sink.count(m) >= 1 }, 2*ackWait, 5*time.Millisecond,
			"%s waited behind a hung dispatch in its lane and was never dispatched", m)
		sink.mu.Lock()
		first := sink.firstAt[m]
		sink.mu.Unlock()
		require.Less(t, first.Sub(began), ackWait, "%s was dispatched only on a redelivery, after its first ack deadline", m)
	}
	// Long enough for a stale copy of either to arrive if one were coming.
	time.Sleep(2 * ackWait)
	for _, m := range mates {
		require.Equal(t, 1, sink.count(m), "%s must be dispatched exactly once", m)
	}
	require.Positive(t, sink.hangs.Load(), "the hung device was never dispatched, so this test shows nothing")
}

// A lane holds only laneQueue deliveries behind the one it runs, so a hung lane cannot pin every
// slot of the reader: with the default eight workers it holds the running one, four queued and the
// one the read loop is blocked handing over, and two slots stay clear of it.
//
// The in-flight gauge counts exactly those: taken from the stream and not yet finished with.
func TestALaneHoldsAtMostItsQueueSoAHungLaneCannotPinEverySlot(t *testing.T) {
	t.Parallel()
	const workers = 8
	var acked atomic.Int64
	msgs := interleaved(t, 1, 24, &acked) // one device: every delivery hashes to one lane
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	sink := blockingSink{started: started, release: release}
	ms := reactMS()
	rd := NewReactDispatcher(ms, &scriptReader{msgs: msgs}, workers,
		reactFakeResolver{rule: alarmRule(), found: true}, nil, sink, nil, nil, nil, testShedBudget, NewReactMetrics(ms))
	require.NoError(t, rd.Start(context.Background()))
	<-started

	want := float64(laneQueue(workers) + 2)
	require.Eventually(t, func() bool {
		return gaugeValue(t, ms, "devicechain_eventprocessing_react_derived_in_flight") == want
	}, 5*time.Second, 5*time.Millisecond, "a lane did not fill to its queue")
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, want, gaugeValue(t, ms, "devicechain_eventprocessing_react_derived_in_flight"),
		"a lane held more than its queue, running one and the one being handed over")
	require.Less(t, want, float64(workers), "the bound must leave slots free")

	close(release)
	require.Eventually(t, func() bool { return acked.Load() == int64(len(msgs)) }, 10*time.Second, 5*time.Millisecond)
	require.NoError(t, rd.Stop(context.Background()))
}

// blockingSink holds every request until release is closed.
type blockingSink struct {
	started chan struct{}
	release chan struct{}
}

func (b blockingSink) Dispatch(ctx context.Context, _ react.AlarmRequest) error {
	select {
	case b.started <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// shareOf gives a dispatch with others behind it an even share of what is left of its deadline,
// and the whole of it with none behind.
func TestShareOfDividesTheRemainingDeadline(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(60 * time.Second)
	require.Equal(t, deadline, shareOf(deadline, now, 0))
	require.Equal(t, now.Add(30*time.Second), shareOf(deadline, now, 1))
	require.Equal(t, now.Add(12*time.Second), shareOf(deadline, now, 4))
	// An expired deadline is returned as it is: the context ends at once.
	require.Equal(t, now.Add(-time.Second), shareOf(now.Add(-time.Second), now, 3))
	// The default lane queue leaves a dispatch more than the slowest sink call's 10 s timeout.
	require.Greater(t, shareOf(deadline, now, laneQueue(8)).Sub(now), 10*time.Second)
}

// 🔴 Stop DRAINS the lanes: nothing is being dispatched once it returns. A sink that answers the
// dispatcher's cancellation only after a delay, and records that it did, is the dispatch Stop has
// to wait for. Without the wait on the lane goroutines Stop returns while it is still running.
func TestStopWaitsForTheDispatchesRunningInTheLanes(t *testing.T) {
	t.Parallel()
	var acked atomic.Int64
	var finished atomic.Bool
	started := make(chan struct{})
	var once sync.Once
	sink := alarmFunc(func(ctx context.Context, _ react.AlarmRequest) error {
		once.Do(func() { close(started) })
		<-ctx.Done()
		time.Sleep(200 * time.Millisecond)
		finished.Store(true)
		return ctx.Err()
	})
	ms := reactMS()
	rd := NewReactDispatcher(ms, &scriptReader{msgs: interleaved(t, 1, 1, &acked)}, 2,
		reactFakeResolver{rule: alarmRule(), found: true}, nil, sink, nil, nil, nil, testShedBudget, NewReactMetrics(ms))
	require.NoError(t, rd.Start(context.Background()))
	<-started

	require.NoError(t, rd.Stop(context.Background()))
	require.True(t, finished.Load(), "Stop returned while a lane was still dispatching")
}

// alarmFunc adapts a function to a react.AlarmSink.
type alarmFunc func(ctx context.Context, req react.AlarmRequest) error

func (f alarmFunc) Dispatch(ctx context.Context, req react.AlarmRequest) error { return f(ctx, req) }

// An old leader's lanes stop starting deliveries once the DETECT term is lost: the dispatch already
// running finishes, the ones queued behind it are released unstarted and unacked — left for the
// replica that holds the term now — and nothing new is read.
func TestLanesStartNothingNewOnceTheTermIsLost(t *testing.T) {
	t.Parallel()
	var acked atomic.Int64
	msgs := interleaved(t, 1, 5, &acked) // one device: one lane, one running and four queued
	var held atomic.Bool
	held.Store(true)
	var dispatched atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	sink := alarmFunc(func(ctx context.Context, _ react.AlarmRequest) error {
		dispatched.Add(1)
		once.Do(func() { close(started) })
		<-release
		return nil
	})
	ms := reactMS()
	rd := NewReactDispatcher(ms, &scriptReader{msgs: msgs}, 8,
		reactFakeResolver{rule: alarmRule(), found: true}, nil, sink, nil, nil, nil, testShedBudget, NewReactMetrics(ms))
	rd.SetTermGate(held.Load)
	require.NoError(t, rd.Start(context.Background()))
	<-started
	// The other four are queued behind the running one, which is what makes "not started" a finding
	// about the gate and not about there having been nothing to start.
	require.Eventually(t, func() bool {
		return gaugeValue(t, ms, "devicechain_eventprocessing_react_derived_in_flight") == 5
	}, 5*time.Second, 5*time.Millisecond)

	held.Store(false)
	close(release)
	require.Eventually(t, func() bool {
		return gaugeValue(t, ms, "devicechain_eventprocessing_react_derived_in_flight") == 0
	}, 5*time.Second, 5*time.Millisecond, "the queued deliveries were never released")
	require.NoError(t, rd.Stop(context.Background()))

	require.Equal(t, int64(1), dispatched.Load(), "a delivery started after the term was lost")
	require.Equal(t, int64(1), acked.Load(), "only the dispatch that was already running may be acked")
}
