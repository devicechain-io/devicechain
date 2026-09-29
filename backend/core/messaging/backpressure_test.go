// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
)

// The gate's arithmetic, without a broker: the ratio it closes on, the hysteresis, and what
// it answers when it has not been able to measure. The broker-backed tests beside these
// (backpressure_broker_test.go) show the same gate against JetStream's own accounting.

func TestGateHysteresis(t *testing.T) {
	for _, tc := range []struct {
		closed bool
		ratio  float64
		want   bool
	}{
		{closed: false, ratio: 0.89, want: false},
		{closed: false, ratio: 0.90, want: true}, // closes AT the threshold
		{closed: false, ratio: 1.20, want: true},
		{closed: true, ratio: 0.85, want: true}, // stays closed between the two thresholds
		{closed: true, ratio: 0.80, want: true}, // opens only BELOW the open threshold
		{closed: true, ratio: 0.79, want: false},
		{closed: false, ratio: 0.85, want: false}, // an open gate does not close in the band
	} {
		if got := gateNext(tc.closed, tc.ratio); got != tc.want {
			t.Errorf("gateNext(closed=%v, %.2f) = %v, want %v", tc.closed, tc.ratio, got, tc.want)
		}
	}
}

func TestUnreadRatio(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   nats.StreamState
		cfg  nats.StreamConfig
		ci   nats.ConsumerInfo
		want float64
	}{
		{name: "empty stream, nothing unread",
			cfg: nats.StreamConfig{MaxMsgs: 20, MaxBytes: 1 << 20}, want: 0},
		{name: "the message ceiling binds",
			st:  nats.StreamState{Msgs: 18, Bytes: 18 * 10},
			cfg: nats.StreamConfig{MaxMsgs: 20, MaxBytes: 1 << 20},
			ci:  nats.ConsumerInfo{NumPending: 18}, want: 0.9},
		{name: "the byte ceiling binds",
			st:  nats.StreamState{Msgs: 19, Bytes: 19 * 1024},
			cfg: nats.StreamConfig{MaxMsgs: 5_000_000, MaxBytes: 20 * 1024},
			ci:  nats.ConsumerInfo{NumPending: 19}, want: 0.95},
		{name: "messages handed out but not acked are unread",
			st:  nats.StreamState{Msgs: 18, Bytes: 18},
			cfg: nats.StreamConfig{MaxMsgs: 20, MaxBytes: 1 << 20},
			ci:  nats.ConsumerInfo{NumPending: 0, NumAckPending: 18}, want: 0.9},
		{name: "read history does not count",
			st:  nats.StreamState{Msgs: 20, Bytes: 20},
			cfg: nats.StreamConfig{MaxMsgs: 20, MaxBytes: 1 << 20},
			ci:  nats.ConsumerInfo{NumPending: 2}, want: 0.1},
		{name: "an unlimited dimension contributes nothing",
			st:  nats.StreamState{Msgs: 10, Bytes: 100},
			cfg: nats.StreamConfig{MaxMsgs: -1, MaxBytes: 1000},
			ci:  nats.ConsumerInfo{NumPending: 9}, want: 0.09},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unreadRatio(tc.st, tc.cfg, &tc.ci); abs(got-tc.want) > 1e-9 {
				t.Errorf("unreadRatio = %v, want %v", got, tc.want)
			}
		})
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// testClock is a clock the staleness tests move by hand.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Unix(1_700_000_000, 0)} }

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// gatedManager is a manager with no connection and one gate on inbound-events whose state
// the test writes directly, as a successful sample would. Nothing measures it afterwards
// (it was never registered, so no sampling loop runs), which is the condition staleness is
// for: a gate nobody has been able to measure.
func gatedManager(t *testing.T, clk *testClock) (*NatsManager, *gateState) {
	t.Helper()
	nmgr := &NatsManager{Microservice: &core.Microservice{InstanceId: "inst", FunctionalArea: "event-sources"}}
	g := nmgr.backpressure()
	g.now = clk.now
	gs := &gateState{suffix: streams.InboundEvents, stream: StreamName("inst", streams.InboundEvents)}
	g.gates[streams.InboundEvents] = gs
	return nmgr, gs
}

func setGate(nmgr *NatsManager, gs *gateState, f func()) {
	g := nmgr.backpressure()
	g.mu.Lock()
	defer g.mu.Unlock()
	f()
}

// A gate that cannot measure the backlog must not keep answering from its last good
// measurement: after backpressureStaleAfter it refuses, and says why.
func TestStaleSampleClosesTheGate(t *testing.T) {
	clk := newTestClock()
	nmgr, gs := gatedManager(t, clk)
	setGate(nmgr, gs, func() { gs.sampledAt = clk.now() })

	clk.advance(29 * time.Second)
	if err := nmgr.Backpressure(streams.InboundEvents); err != nil {
		t.Fatalf("29 s after an open sample the gate refused: %v", err)
	}
	clk.advance(2 * time.Second)
	err := nmgr.Backpressure(streams.InboundEvents)
	var bpe *BackpressureError
	if !errors.As(err, &bpe) || !bpe.Stale {
		t.Fatalf("31 s after the last sample, with every later sample failing, Backpressure = %v; want a stale refusal", err)
	}
	if !errors.Is(err, ErrStreamBackpressure) {
		t.Fatalf("a stale refusal does not match ErrStreamBackpressure: %v", err)
	}
}

// A gated stream nobody registered, or registered and never measured, is refused: a gate
// that has never seen the backlog cannot say it is safe to add to it.
func TestAnUnmeasuredGateRefuses(t *testing.T) {
	clk := newTestClock()
	nmgr, _ := gatedManager(t, clk)
	var bpe *BackpressureError
	if err := nmgr.Backpressure(streams.InboundEvents); !errors.As(err, &bpe) || !bpe.Stale {
		t.Fatalf("a registered gate with no successful sample answered %v; want a stale refusal", err)
	}
	if err := nmgr.Backpressure(streams.ResolvedEvents); !errors.As(err, &bpe) || !bpe.Stale {
		t.Fatalf("an unregistered gated stream answered %v; want a stale refusal", err)
	}
	if err := nmgr.Backpressure(streams.AlarmEvents); err != nil {
		t.Fatalf("a stream that applies no backpressure refused: %v", err)
	}
}

// Staleness is an overlay, not a state: it does not latch the hysteresis closed. A gate that
// was OPEN before a measurement outage is open again as soon as a sample lands inside the
// band between the thresholds, rather than waiting, as a gate that really closed does, for
// the backlog to fall below the open ratio.
func TestStalenessDoesNotLatchTheGateClosed(t *testing.T) {
	clk := newTestClock()
	nmgr, gs := gatedManager(t, clk)
	setGate(nmgr, gs, func() { gs.sampledAt = clk.now() })
	clk.advance(time.Minute)
	if err := nmgr.Backpressure(streams.InboundEvents); err == nil {
		t.Fatal("precondition: the gate should be stale")
	}
	// A sample lands at 0.85: in the band, from an open gate.
	setGate(nmgr, gs, func() {
		gs.closed = gateNext(gs.closed, 0.85)
		gs.ratio = 0.85
		gs.sampledAt = clk.now()
	})
	if err := nmgr.Backpressure(streams.InboundEvents); err != nil {
		t.Fatalf("after a stale spell a sample at 0.85 left the gate refusing (%v); staleness latched it closed", err)
	}
}

// A closed gate names the durable that closed it and how far behind it is, which is what
// the log line and a transport's refusal carry.
func TestAClosedGateNamesItsDurable(t *testing.T) {
	clk := newTestClock()
	nmgr, gs := gatedManager(t, clk)
	setGate(nmgr, gs, func() {
		gs.closed, gs.worst, gs.ratio, gs.sampledAt = true, "inst_device-management_inbound-events", 0.93, clk.now()
	})
	err := nmgr.Backpressure(streams.InboundEvents)
	var bpe *BackpressureError
	if !errors.As(err, &bpe) || bpe.Stale || bpe.Durable != "inst_device-management_inbound-events" || bpe.Ratio != 0.93 {
		t.Fatalf("Backpressure = %#v; want the closing durable and its ratio", err)
	}
	if !strings.Contains(err.Error(), "inst_device-management_inbound-events") {
		t.Errorf("the refusal does not name the durable: %q", err.Error())
	}
}

func TestBypassIsAllOrNothing(t *testing.T) {
	yes, no := Message{BypassBackpressure: true}, Message{}
	for _, tc := range []struct {
		name string
		msgs []Message
		want bool
	}{
		{"one presence transition", []Message{yes}, true},
		{"all presence", []Message{yes, yes}, true},
		{"telemetry", []Message{no}, false},
		{"a mixed batch is telemetry", []Message{yes, no}, false},
		{"an empty batch bypasses nothing", nil, false},
	} {
		if got := bypassesBackpressure(tc.msgs); got != tc.want {
			t.Errorf("%s: bypassesBackpressure = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A refused publish is counted, not logged: HandleResponse writes no line for it, while a
// real publish failure is still an Error.
func TestHandleResponseDoesNotLogARefusal(t *testing.T) {
	logs := captureLogs(t)
	w := &natsWriter{suffix: streams.InboundEvents}
	w.HandleResponse(&BackpressureError{Stream: "s", Durable: "d", Ratio: 0.95})
	if strings.Contains(logs.String(), "nats write operation failed") {
		t.Fatalf("a backpressure refusal was logged per message: %s", logs.String())
	}
	w.HandleResponse(errors.New("broker went away"))
	if !strings.Contains(logs.String(), "nats write operation failed") {
		t.Fatalf("a real publish failure is no longer logged: %q", logs.String())
	}
}
