// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/config"
	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// alarmRule raises (and, on the falling edge, clears) one alarm: the shape of the r19 mix, where
// every derived event is a raise or a clear.
func alarmRule() rules.Rule {
	return rules.Rule{ID: "acme/p@1/r1", Name: "r", Type: rules.TypeThreshold, Severity: rules.SeverityMajor,
		Actions: []rules.Action{{Type: rules.ActionRaiseAlarm, RaiseAlarm: &rules.RaiseAlarmAction{AlarmKey: "overheat"}}}}
}

// edgeEvent is device's i-th derived event: even i raises, odd i clears, and each is one second
// after the last, so a device's events are totally ordered by OccurredTime.
func edgeEvent(device string, i int) runtime.DerivedEvent {
	edge := runtime.EdgeRaised
	if i%2 == 1 {
		edge = runtime.EdgeResolved
	}
	return runtime.DerivedEvent{
		RuleID: "acme/p@1/r1", Tenant: "acme", Kind: "threshold", Series: device, Edge: edge,
		OccurredTime: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second),
	}
}

// orderSink is an alarm sink that records, per device, the order its requests COMPLETED in — the
// order device-management would see them — and the most requests it ever had at once. A raise
// takes raiseDelay and a clear nothing, so a clear dispatched beside the raise before it finishes
// first: that is the inversion a lane exists to prevent, made certain rather than likely.
type orderSink struct {
	raiseDelay time.Duration
	// fail, when set, fails a request the first time it is seen (by token).
	fail func(req react.AlarmRequest) bool

	inFlight    atomic.Int64
	maxInFlight atomic.Int64
	done        atomic.Int64

	mu       sync.Mutex
	order    map[string][]time.Time
	attempts map[string][]react.AlarmRequest // by device, every attempt including failed ones
}

func newOrderSink(raiseDelay time.Duration) *orderSink {
	return &orderSink{raiseDelay: raiseDelay, order: map[string][]time.Time{}, attempts: map[string][]react.AlarmRequest{}}
}

func (s *orderSink) Dispatch(_ context.Context, req react.AlarmRequest) error {
	n := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for {
		m := s.maxInFlight.Load()
		if n <= m || s.maxInFlight.CompareAndSwap(m, n) {
			break
		}
	}
	if req.Edge != runtime.EdgeResolved && s.raiseDelay > 0 {
		time.Sleep(s.raiseDelay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts[req.DeviceToken] = append(s.attempts[req.DeviceToken], req)
	if s.fail != nil && s.fail(req) {
		return fmt.Errorf("raise-alarm publish failed")
	}
	s.order[req.DeviceToken] = append(s.order[req.DeviceToken], req.OccurredTime)
	s.done.Add(1)
	return nil
}

// inversions counts, across every device, a request that completed after one that followed it
// in the stream.
func (s *orderSink) inversions() (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, first := 0, ""
	for dev, ts := range s.order {
		for i := 1; i < len(ts); i++ {
			if !ts[i].After(ts[i-1]) {
				if n == 0 {
					first = fmt.Sprintf("%s: %s completed after %s", dev, ts[i].Format(time.TimeOnly), ts[i-1].Format(time.TimeOnly))
				}
				n++
			}
		}
	}
	return n, first
}

// scriptReader hands out a fixed list of messages and then parks until its context ends, as a
// drained durable does. One goroutine reads it (RunConsumer), as the MessageReader contract asks.
type scriptReader struct {
	msgs []messaging.Message
	idx  int
}

func (r *scriptReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	if r.idx < len(r.msgs) {
		m := r.msgs[r.idx]
		r.idx++
		return m, nil
	}
	<-ctx.Done()
	return messaging.Message{}, ctx.Err()
}

func (r *scriptReader) HandleResponse(error) {}

// countingAck counts acks; safe to call from a lane while the test reads it.
type countingAck struct{ n *atomic.Int64 }

func (a countingAck) Ack() error { a.n.Add(1); return nil }

// interleaved is devices x perDevice derived events in the order DETECT would publish them:
// round-robin across devices, each device's events in order.
func interleaved(t *testing.T, devices, perDevice int, acked *atomic.Int64) []messaging.Message {
	t.Helper()
	var out []messaging.Message
	for i := 0; i < perDevice; i++ {
		for d := 0; d < devices; d++ {
			body, err := json.Marshal(edgeEvent(fmt.Sprintf("device-%d", d), i))
			require.NoError(t, err)
			out = append(out, messaging.NewConsumedMessage("dc.acme.derived-events", body, 1, nil, countingAck{acked}))
		}
	}
	return out
}

func reactMS() *core.Microservice {
	ms := &core.Microservice{FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	return ms
}

// 🔑 THE GUARD THIS CHANGE RESTS ON. With lanes, derived events of DIFFERENT devices are dispatched
// at once (max in flight above 1, or the lanes buy nothing) but each device's raise and clear still
// complete in the order the stream holds them, even though every raise is slower than every clear.
// Route a device's events to more than one lane and a clear overtakes the raise before it.
func TestReactLanesKeepEachDevicesEventsInOrder(t *testing.T) {
	t.Parallel()
	const devices, perDevice, workers = 24, 10, 8
	var acked atomic.Int64
	sink := newOrderSink(2 * time.Millisecond)
	ms := reactMS()
	rd := NewReactDispatcher(ms, &scriptReader{msgs: interleaved(t, devices, perDevice, &acked)}, workers,
		reactFakeResolver{rule: alarmRule(), found: true}, nil, sink, nil, nil, nil, testShedBudget, NewReactMetrics(ms))
	require.NoError(t, rd.Start(context.Background()))
	require.Eventually(t, func() bool { return acked.Load() == devices*perDevice }, 20*time.Second, 5*time.Millisecond,
		"not every derived event was dispatched and acked")
	require.NoError(t, rd.Stop(context.Background()))

	n, first := sink.inversions()
	require.Zero(t, n, "a device's alarm edges completed out of stream order (%d inversions; first %s)", n, first)
	require.Greater(t, sink.maxInFlight.Load(), int64(1), "the lanes never overlapped: REACT is still serial")
	require.LessOrEqual(t, sink.maxInFlight.Load(), int64(workers), "more dispatches at once than workers")
	require.Equal(t, float64(0), gaugeValue(t, ms, "devicechain_eventprocessing_react_derived_in_flight"),
		"in-flight must return to zero once everything is dispatched")
	require.Equal(t, float64(workers), gaugeValue(t, ms, "devicechain_eventprocessing_react_workers"))
}

// The serial path (workers 1) is unchanged: one at a time, in order.
func TestReactOneWorkerIsSerial(t *testing.T) {
	t.Parallel()
	var acked atomic.Int64
	sink := newOrderSink(time.Millisecond)
	ms := reactMS()
	rd := NewReactDispatcher(ms, &scriptReader{msgs: interleaved(t, 6, 4, &acked)}, 1,
		reactFakeResolver{rule: alarmRule(), found: true}, nil, sink, nil, nil, nil, testShedBudget, NewReactMetrics(ms))
	require.NoError(t, rd.Start(context.Background()))
	require.Eventually(t, func() bool { return acked.Load() == 24 }, 10*time.Second, 5*time.Millisecond)
	require.NoError(t, rd.Stop(context.Background()))
	n, first := sink.inversions()
	require.Zero(t, n, first)
	require.Equal(t, int64(1), sink.maxInFlight.Load())
}

// laneFor sends every event of one device of one tenant to one lane, and spreads devices over the
// lanes rather than piling them onto one.
func TestLaneForIsPerDeviceAndSpreads(t *testing.T) {
	t.Parallel()
	const lanes = 8
	used := map[int]bool{}
	for d := 0; d < 64; d++ {
		var lane = -1
		for i := 0; i < 4; i++ {
			body, err := json.Marshal(edgeEvent(fmt.Sprintf("device-%d", d), i))
			require.NoError(t, err)
			l := laneFor(messaging.Message{Subject: "dc.acme.derived-events", Value: body}, lanes)
			if lane == -1 {
				lane = l
			}
			require.Equal(t, lane, l, "device-%d's events went to two lanes", d)
		}
		used[lane] = true
	}
	require.Greater(t, len(used), lanes/2, "64 devices landed on only %d of %d lanes", len(used), lanes)

	// The tenant is part of the key: the same device token under two tenants is two keys. Not
	// asserted to differ (a hash may collide), only to be computed from the subject at all.
	body, _ := json.Marshal(edgeEvent("device-1", 0))
	require.Equal(t, 0, laneFor(messaging.Message{Subject: "dc.acme.derived-events", Value: []byte("{not json")}, lanes))
	_ = laneFor(messaging.Message{Subject: "dc.other.derived-events", Value: body}, lanes)
}

func histogramOf(t *testing.T, ms *core.Microservice, name string) (uint64, float64) {
	t.Helper()
	g, ok := ms.MetricsRegisterer().(prometheus.Gatherer)
	require.True(t, ok)
	mfs, err := g.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() == name {
			h := mf.GetMetric()[0].GetHistogram()
			return h.GetSampleCount(), h.GetSampleSum()
		}
	}
	t.Fatalf("metric %s is not registered", name)
	return 0, 0
}

func gaugeValue(t *testing.T, ms *core.Microservice, name string) float64 {
	t.Helper()
	g, ok := ms.MetricsRegisterer().(prometheus.Gatherer)
	require.True(t, ok)
	mfs, err := g.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() == name {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatalf("metric %s is not registered", name)
	return 0
}

// reactBroker starts a REACT dispatcher on the shared test broker the way main.go wires it: the
// derived-events reader from ReactReaderOptions with workers slots, the same workers handed to the
// constructor. The durable exists before it returns, so what publish writes is delivered even if it
// is written before start is called; start launches the dispatcher.
func reactBroker(t *testing.T, ackWait time.Duration, workers int, resolver react.RuleResolver,
	alarms func(nmgr *messaging.NatsManager) react.AlarmSink, dead deadletter.Writer) (publish func(ev runtime.DerivedEvent), start func(), ms *core.Microservice) {
	t.Helper()
	b := startDetectBroker(t)
	ms = &core.Microservice{InstanceId: b.instance, FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats = b.natsConfig()
	var reader messaging.MessageReader
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(m *messaging.NatsManager) error {
		r, err := m.NewReader(streams.DerivedEvents, ReactReaderOptions(func() bool { return true }, workers)...)
		reader = r
		return err
	})
	dl := deadletter.NewProducer(ms)
	nmgr.RecordMaxDeliveries(deadletter.MaxDeliveryRecorder(dl))
	nmgr.SetAckWaitForTesting(t, ackWait)
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	w, err := nmgr.NewWriter(streams.DerivedEvents)
	require.NoError(t, err)
	var sink *deadletter.Sink
	if dead != nil {
		sink = dl.NewSink(dead)
	}
	rd := NewReactDispatcher(ms, reader, workers, resolver, nil, alarms(nmgr), nil, nil, sink, testShedBudget, NewReactMetrics(ms))
	publish = func(ev runtime.DerivedEvent) {
		t.Helper()
		body, err := json.Marshal(ev)
		require.NoError(t, err)
		require.NoError(t, w.WriteMessages(core.WithTenant(context.Background(), ev.Tenant), messaging.Message{Value: body}))
	}
	start = func() {
		require.NoError(t, rd.Start(context.Background()))
		t.Cleanup(func() { _ = rd.Stop(context.Background()) })
	}
	return publish, start, ms
}

// hangOnce is an alarm sink that, on the first request for one device, waits until its context
// ends and then a further late (a publish that gives up only after its deadline) and fails; every
// other request is recorded and succeeds. The late return is what makes the stale copy behind it
// visible: by the time the lane reaches that copy the broker has already redelivered it, so an ack
// of the stale copy cannot cancel the redelivery and both would be dispatched.
type hangOnce struct {
	device string
	late   time.Duration
	hung   atomic.Bool
	mu     sync.Mutex
	calls  map[string]int // by OccurredTime, for the device
	tokens map[string][]string
}

func (h *hangOnce) Dispatch(ctx context.Context, req react.AlarmRequest) error {
	if req.DeviceToken == h.device && h.hung.CompareAndSwap(false, true) {
		<-ctx.Done()
		time.Sleep(h.late)
		return ctx.Err()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	k := req.OccurredTime.Format(time.TimeOnly)
	h.calls[k]++
	h.tokens[k] = append(h.tokens[k], req.Token)
	return nil
}

func (h *hangOnce) count(k string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[k]
}

// A delivery that waited in its lane behind a hung one past its own ack deadline is NOT started:
// the broker is already redelivering it, and starting the stale copy too is the duplicate the
// capacity reader exists to prevent. The device's second event is dispatched once — its redelivery
// — not twice. Without the check in runQueued it is dispatched twice, a full AckWait apart.
//
// Real broker, real reader options: only a capacity reader's message carries an ack deadline.
func TestAQueuedDeliveryPastItsDeadlineIsNotStarted(t *testing.T) {
	t.Parallel()
	const ackWait = time.Second
	sink := &hangOnce{device: "device-1", late: ackWait / 2, calls: map[string]int{}, tokens: map[string][]string{}}
	publish, start, _ := reactBroker(t, ackWait, 2, reactFakeResolver{rule: alarmRule(), found: true},
		func(*messaging.NatsManager) react.AlarmSink { return sink }, nil)
	first, second := edgeEvent("device-1", 0), edgeEvent("device-1", 1)
	publish(first)
	publish(second)
	start() // both fetched together, so their deadlines are the same moment

	k1, k2 := first.OccurredTime.Format(time.TimeOnly), second.OccurredTime.Format(time.TimeOnly)
	require.Eventually(t, func() bool { return sink.count(k1) == 1 && sink.count(k2) >= 1 },
		6*ackWait, 20*time.Millisecond, "the hung event and the one behind it were never dispatched")
	// Long enough for a second copy of either to arrive if one were coming.
	time.Sleep(2 * ackWait)
	require.Equal(t, 1, sink.count(k2), "the queued copy was started after its deadline and its redelivery dispatched too")
	require.Equal(t, 1, sink.count(k1))
}

// A delivery left for redelivery (Retry) is re-dispatched under the same token, so the raise-alarm
// stream collapses the repeat, and it does not hold its lane: the device's next event and every other
// device's are dispatched while it waits out its AckWait.
func TestARetriedDeliveryRedispatchesUnderOneTokenWithoutHoldingItsLane(t *testing.T) {
	t.Parallel()
	const ackWait = time.Second
	sink := newOrderSink(0)
	failed := map[string]bool{}
	var fmu sync.Mutex
	sink.fail = func(req react.AlarmRequest) bool {
		if req.DeviceToken != "device-0" || req.Edge == runtime.EdgeResolved {
			return false
		}
		fmu.Lock()
		defer fmu.Unlock()
		if failed[req.Token] {
			return false
		}
		failed[req.Token] = true
		return true
	}
	publish, start, _ := reactBroker(t, ackWait, config.DefaultReactWorkers, reactFakeResolver{rule: alarmRule(), found: true},
		func(*messaging.NatsManager) react.AlarmSink { return sink }, nil)
	for d := 0; d < 4; d++ {
		publish(edgeEvent(fmt.Sprintf("device-%d", d), 0))
	}
	publish(edgeEvent("device-0", 1)) // the clear behind the raise that fails once
	start()

	// Everything but the failed raise is dispatched well inside the AckWait the raise waits out.
	require.Eventually(t, func() bool { return sink.done.Load() == 4 }, ackWait*3/4, 5*time.Millisecond,
		"a failed delivery held up the events behind it")
	require.Eventually(t, func() bool { return sink.done.Load() == 5 }, 4*ackWait, 20*time.Millisecond,
		"the failed raise was never redispatched")
	time.Sleep(ackWait + ackWait/2)
	require.Equal(t, int64(5), sink.done.Load(), "a delivery was dispatched again after it succeeded")

	sink.mu.Lock()
	defer sink.mu.Unlock()
	var raises []react.AlarmRequest
	for _, r := range sink.attempts["device-0"] {
		if r.Edge != runtime.EdgeResolved {
			raises = append(raises, r)
		}
	}
	require.Len(t, raises, 2, "the raise should be attempted exactly twice: failed, then redelivered")
	require.NotEmpty(t, raises[0].Token)
	require.Equal(t, raises[0].Token, raises[1].Token, "the redelivery must re-send under the token its stream collapses on")
}

// latentResolver is the rule read with a fixed round trip, standing in for the point read REACT
// makes against this service's database on every derived event.
type latentResolver struct {
	rule    rules.Rule
	latency time.Duration
}

func (r latentResolver) Resolve(ctx context.Context, _ string) (rules.Rule, bool, error) {
	if r.latency > 0 {
		select {
		case <-time.After(r.latency):
		case <-ctx.Done():
			return rules.Rule{}, false, ctx.Err()
		}
	}
	return r.rule, true, nil
}

// countingAlarms wraps the REAL raise-alarm client — a JetStream publish that waits for the
// broker's acknowledgement, as production does — counting completions and the most at once.
type countingAlarms struct {
	inner                       react.AlarmSink
	done, inFlight, maxInFlight atomic.Int64
	first, last                 atomic.Int64 // unix nanos
}

func (c *countingAlarms) Dispatch(ctx context.Context, req react.AlarmRequest) error {
	n := c.inFlight.Add(1)
	defer c.inFlight.Add(-1)
	for {
		m := c.maxInFlight.Load()
		if n <= m || c.maxInFlight.CompareAndSwap(m, n) {
			break
		}
	}
	c.first.CompareAndSwap(0, time.Now().UnixNano())
	if err := c.inner.Dispatch(ctx, req); err != nil {
		return err
	}
	c.last.Store(time.Now().UnixNano())
	c.done.Add(1)
	return nil
}

// reactDrainRate publishes devices x perDevice raise/clear events, then starts REACT and measures
// how fast it dispatches them through the real raise-alarm publish.
func reactDrainRate(t *testing.T, workers, devices, perDevice int, ruleLatency time.Duration) (rate float64, maxInFlight int64) {
	t.Helper()
	sink := &countingAlarms{}
	publish, start, ms := reactBroker(t, 30*time.Second, workers, latentResolver{rule: alarmRule(), latency: ruleLatency},
		func(nmgr *messaging.NatsManager) react.AlarmSink {
			w, err := nmgr.NewWriter(streams.RaiseAlarm)
			require.NoError(t, err)
			sink.inner = NewAlarmClient(w)
			return sink
		}, nil)
	for i := 0; i < perDevice; i++ {
		for d := 0; d < devices; d++ {
			publish(edgeEvent(fmt.Sprintf("device-%d", d), i))
		}
	}
	total := int64(devices * perDevice)
	start()
	require.Eventually(t, func() bool { return sink.done.Load() == total }, 120*time.Second, 10*time.Millisecond,
		"REACT did not drain %d derived events", total)
	elapsed := time.Duration(sink.last.Load() - sink.first.Load())
	// Every dispatch was timed against its broker append time, and none of them was free: the
	// events waited in the stream while REACT drained it.
	count, sum := histogramOf(t, ms, "devicechain_eventprocessing_react_dispatch_lag_seconds")
	require.Equal(t, uint64(total), count, "react_dispatch_lag_seconds must observe every dispatch")
	require.Greater(t, sum, 0.0)
	return float64(total) / elapsed.Seconds(), sink.maxInFlight.Load()
}

// 🔑 THE r19 CEILING, ON AN EMBEDDED BROKER. The G3 mix offered 194 derived events a second, half
// raises and half clears, and one serial worker drained ~151 (ack-pending 1). Here the same mix runs
// through the real reader, the real dispatcher and the real raise-alarm publish, with the rule read
// given a 5 ms round trip — about what one event's serial cost implies on GKE once the fetch and the
// replicated publish are added, and far above what the embedded broker charges. The serial rate is
// logged as the before; the default worker count must clear the offered rate several times over,
// with more than one dispatch in flight.
//
// Not parallel: it measures a rate, and a parallel neighbour's CPU would be measured with it.
func TestReactDrainsTheR19MixWellAboveItsRate(t *testing.T) {
	const ruleLatency = 5 * time.Millisecond
	const offered = 194.0

	serial, serialMax := reactDrainRate(t, 1, 50, 8, ruleLatency)
	t.Logf("workers=1: %.0f derived events/s, max in flight %d (rule read %s)", serial, serialMax, ruleLatency)
	require.Equal(t, int64(1), serialMax)

	lanes, lanesMax := reactDrainRate(t, config.DefaultReactWorkers, 500, 8, ruleLatency)
	t.Logf("workers=%d: %.0f derived events/s, max in flight %d (rule read %s)", config.DefaultReactWorkers, lanes, lanesMax, ruleLatency)
	require.Greater(t, lanesMax, int64(1), "more than one derived event must be in flight")
	require.Greater(t, lanes, 3*offered, "the default worker count must drain the r19 mix at well over its offered %.0f/s", offered)
	require.Greater(t, lanes, 3*serial, "lanes must multiply the serial rate, not match it")
}

// failingAlarms fails every request: a raise-alarm stream that never takes a publish.
type failingAlarms struct{ attempts atomic.Int64 }

func (f *failingAlarms) Dispatch(context.Context, react.AlarmRequest) error {
	f.attempts.Add(1)
	return fmt.Errorf("raise-alarm publish failed")
}

// Dead-lettering is unchanged by lanes: an event whose action fails on every delivery is attempted
// exactly MaxDeliver times, then written as ONE exhausted letter naming the action that failed, and
// acked, while the other devices' events, in other lanes, are dispatched beside it.
func TestLanesDeadLetterAnExhaustedEventOnce(t *testing.T) {
	t.Parallel()
	const ackWait = time.Second
	dead := &ctxDeadRecorder{}
	failing := &failingAlarms{}
	others := newOrderSink(0)
	router := alarmRouter{"device-0": failing}
	publish, start, _ := reactBroker(t, ackWait, config.DefaultReactWorkers, reactFakeResolver{rule: alarmRule(), found: true},
		func(*messaging.NatsManager) react.AlarmSink { return router.or(others) }, dead)
	for d := 0; d < 6; d++ {
		publish(edgeEvent(fmt.Sprintf("device-%d", d), 0))
	}
	start()

	require.Eventually(t, func() bool { return others.done.Load() == 5 }, ackWait/2, 5*time.Millisecond,
		"the other devices waited on the failing one")
	var letters []deadletter.Envelope
	require.Eventually(t, func() bool {
		letters = dead.letters(t)
		return len(letters) > 0
	}, time.Duration(messaging.MaxDeliver+2)*ackWait+5*time.Second, 20*time.Millisecond, "no exhausted letter was written")
	time.Sleep(2 * ackWait)
	letters = dead.letters(t)
	require.Len(t, letters, 1, "the exhausted event must be lettered exactly once")
	require.Equal(t, deadletter.ReasonExhausted, letters[0].Reason)
	require.Regexp(t, `^raiseAlarm/failed/[0-9a-f]{64}$`, letters[0].Detail)
	require.Equal(t, "acme/p@1/r1", letters[0].Reference)
	require.Equal(t, int64(messaging.MaxDeliver), failing.attempts.Load(), "attempted %d times, want MaxDeliver", failing.attempts.Load())
}

// alarmRouter sends a device's requests to its own sink and every other device's to a default.
type alarmRouter map[string]react.AlarmSink

func (r alarmRouter) or(def react.AlarmSink) react.AlarmSink { return routed{r, def} }

type routed struct {
	by  alarmRouter
	def react.AlarmSink
}

func (r routed) Dispatch(ctx context.Context, req react.AlarmRequest) error {
	if s, ok := r.by[req.DeviceToken]; ok {
		return s.Dispatch(ctx, req)
	}
	return r.def.Dispatch(ctx, req)
}
