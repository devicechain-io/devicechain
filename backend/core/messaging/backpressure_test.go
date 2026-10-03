// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"errors"
	"math"
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

// The runway rule's arithmetic. historyAhead is a LOWER bound on the read messages ahead of a
// durable's first unacknowledged one, from sequences alone.
func TestHistoryAhead(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   nats.StreamState
		ci   nats.ConsumerInfo
		want uint64
	}{
		{name: "caught up: every message is history",
			st:   nats.StreamState{Msgs: 100, FirstSeq: 1, LastSeq: 100},
			ci:   nats.ConsumerInfo{Delivered: nats.SequenceInfo{Stream: 100}, AckFloor: nats.SequenceInfo{Stream: 100}},
			want: 100},
		{name: "unread and awaiting ack are above the floor",
			st: nats.StreamState{Msgs: 100, FirstSeq: 1, LastSeq: 100},
			ci: nats.ConsumerInfo{NumPending: 10, NumAckPending: 10,
				Delivered: nats.SequenceInfo{Stream: 90}, AckFloor: nats.SequenceInfo{Stream: 80}},
			want: 80},
		// Seven of the ten above the floor were acked out of order: they are not unread, but
		// the bound does not know which seven, so it does not count them as history either.
		{name: "acked out of order above the floor is not counted",
			st: nats.StreamState{Msgs: 100, FirstSeq: 1, LastSeq: 100},
			ci: nats.ConsumerInfo{NumPending: 10, NumAckPending: 3,
				Delivered: nats.SequenceInfo{Stream: 90}, AckFloor: nats.SequenceInfo{Stream: 80}},
			want: 80},
		// The broker counts NumPending from a signal sent after the PubAck, so straight after a
		// burst it reads low. A bound built on it would read 85 here and invent five messages
		// of history.
		{name: "a NumPending that reads low does not inflate it",
			st: nats.StreamState{Msgs: 100, FirstSeq: 1, LastSeq: 100},
			ci: nats.ConsumerInfo{NumPending: 5, NumAckPending: 10,
				Delivered: nats.SequenceInfo{Stream: 90}, AckFloor: nats.SequenceInfo{Stream: 80}},
			want: 80},
		{name: "messages deleted from the middle make it under-count",
			st:   nats.StreamState{Msgs: 90, FirstSeq: 1, LastSeq: 100, NumDeleted: 10},
			ci:   nats.ConsumerInfo{AckFloor: nats.SequenceInfo{Stream: 80}},
			want: 70},
		{name: "unread messages are already being discarded",
			st:   nats.StreamState{Msgs: 10, FirstSeq: 11, LastSeq: 20},
			ci:   nats.ConsumerInfo{AckFloor: nats.SequenceInfo{Stream: 7}},
			want: 0},
		{name: "empty stream", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := historyAhead(tc.st, &tc.ci); got != tc.want {
				t.Errorf("historyAhead = %d, want %d", got, tc.want)
			}
		})
	}
	if hasUnread(nats.StreamState{LastSeq: 100}, &nats.ConsumerInfo{AckFloor: nats.SequenceInfo{Stream: 100}}) {
		t.Error("hasUnread with the ack floor at the last sequence = true, want false")
	}
	if !hasUnread(nats.StreamState{LastSeq: 100}, &nats.ConsumerInfo{AckFloor: nats.SequenceInfo{Stream: 99}}) {
		t.Error("hasUnread with one message above the ack floor = false, want true")
	}
}

func TestAtCeiling(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   nats.StreamState
		cfg  nats.StreamConfig
		want bool
	}{
		{name: "messages: outside the 5% margin",
			st: nats.StreamState{Msgs: 949}, cfg: nats.StreamConfig{MaxMsgs: 1000, MaxBytes: -1}, want: false},
		{name: "messages: at the 5% margin",
			st: nats.StreamState{Msgs: 950}, cfg: nats.StreamConfig{MaxMsgs: 1000, MaxBytes: -1}, want: true},
		{name: "bytes: outside the 5% margin",
			st:  nats.StreamState{Bytes: 949_999},
			cfg: nats.StreamConfig{MaxMsgs: -1, MaxBytes: 1_000_000, MaxMsgSize: 1000}, want: false},
		{name: "bytes: at the 5% margin",
			st:  nats.StreamState{Bytes: 950_000},
			cfg: nats.StreamConfig{MaxMsgs: -1, MaxBytes: 1_000_000, MaxMsgSize: 1000}, want: true},
		// A stream that accepts messages larger than 5% of its ceiling is full whenever the
		// next one may not fit.
		{name: "bytes: within one largest message",
			st:  nats.StreamState{Bytes: 900_000},
			cfg: nats.StreamConfig{MaxMsgs: -1, MaxBytes: 1_000_000, MaxMsgSize: 100_000}, want: true},
		{name: "unlimited",
			st: nats.StreamState{Msgs: 1 << 40, Bytes: 1 << 50}, cfg: nats.StreamConfig{MaxMsgs: -1, MaxBytes: -1}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := atCeiling(tc.st, tc.cfg); got != tc.want {
				t.Errorf("atCeiling = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNextDrain(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	near := func(t *testing.T, what string, got, want float64) {
		t.Helper()
		if abs(got-want) > 1e-9 {
			t.Errorf("%s: rate = %v, want %v", what, got, want)
		}
	}

	d0 := nextDrain(drain{}, t0, 1000, 1, true, true, false)
	near(t, "first sample", d0.rate, 0)
	// 60 read messages discarded from the front in 5 s.
	d1 := nextDrain(d0, at(5*time.Second), 940, 61, true, true, false)
	near(t, "60 discarded in 5 s", d1.rate, 12)
	// A minute with nothing discarded, while the gate is open: the rate fades by e.
	near(t, "a quiet minute, open", nextDrain(d1, at(65*time.Second), 940, 61, true, true, false).rate, 12*math.Exp(-1))
	// The same minute while the gate refuses: a refusing stream discards nothing, so the
	// rate must not fade, or the gate would reopen on a reader that has not moved.
	near(t, "a quiet minute, refusing", nextDrain(d1, at(65*time.Second), 940, 61, true, true, true).rate, 12)
	if got := nextDrain(d1, at(10*time.Second), 2000, 61, true, true, false).rate; got < 0 {
		t.Errorf("history that grew gave a negative rate %v", got)
	}
	if got := nextDrain(d1, d1.at, 0, 1000, true, true, false); got != d1 {
		t.Errorf("a sample at the same instant changed the drain: %+v, want %+v", got, d1)
	}
	if got := nextDrain(d1, at(10*time.Second), 900, 101, true, false, false); got.rate != 0 {
		t.Errorf("with nothing unread the rate is %v; want it forgotten", got.rate)
	}
	// A tenant's messages purged from the middle: the history falls by 300 in 100 ms while
	// FirstSeq moves by one. That frees space; it is not a discard rate of 3000 a second.
	purged := nextDrain(d1, at(5100*time.Millisecond), 640, 62, true, true, false)
	if purged.rate > 12.0001 {
		t.Errorf("a purge from the middle of a full stream read as a discard rate of %v a second", purged.rate)
	}
	// An interval that ends, or starts, below the ceiling measures nothing.
	near(t, "ends below the ceiling", nextDrain(d1, at(10*time.Second), 880, 121, false, true, false).rate,
		12*math.Exp(-5.0/60))
	notFull := d1
	notFull.full = false
	near(t, "starts below the ceiling", nextDrain(notFull, at(10*time.Second), 880, 121, true, true, false).rate,
		12*math.Exp(-5.0/60))
	// The hysteresis state is carried.
	closed := d1
	closed.closed = true
	if !nextDrain(closed, at(10*time.Second), 940, 61, true, true, true).closed {
		t.Error("nextDrain dropped the durable's closed state")
	}
}

func TestRunwayHysteresis(t *testing.T) {
	for _, tc := range []struct {
		name         string
		closed       bool
		history      uint64
		rate         float64
		full, unread bool
		want         bool
	}{
		{"open, under 30 s of history", false, 2999, 100, true, true, true},
		{"open, exactly 30 s", false, 3000, 100, true, true, false},
		{"closed, under 60 s", true, 5999, 100, true, true, true},
		{"closed, exactly 60 s", true, 6000, 100, true, true, false},
		{"closed, nothing unread", true, 1, 100, true, false, false},
		{"nothing being discarded", false, 0, 0, true, true, false},
		{"not at the ceiling", false, 0, 100, false, true, false},
	} {
		if got := runwayNext(tc.closed, tc.history, tc.rate, tc.full, tc.unread); got != tc.want {
			t.Errorf("%s: runwayNext = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := runwaySeconds(3000, 100, true, true); got != 30 {
		t.Errorf("runwaySeconds = %v, want 30", got)
	}
	for _, c := range [][2]bool{{false, true}, {true, false}} {
		if got := runwaySeconds(3000, 100, c[0], c[1]); !math.IsInf(got, 1) {
			t.Errorf("runwaySeconds(full=%v, unread=%v) = %v, want +Inf", c[0], c[1], got)
		}
	}
	if got := runwaySeconds(3000, 0, true, true); !math.IsInf(got, 1) {
		t.Errorf("runwaySeconds with nothing discarded = %v, want +Inf", got)
	}
}

// A refusal the runway rule made says so, with its numbers, and still matches
// ErrStreamBackpressure, which is what every transport tests for.
func TestARunwayRefusalSaysWhy(t *testing.T) {
	clk := newTestClock()
	nmgr, gs := gatedManager(t, clk)
	setGate(nmgr, gs, func() {
		gs.runwayClosed, gs.runwayDurable, gs.runwayHistory, gs.runwayRate = true, "inst_device-management_inbound-events", 320, 11.6
		gs.ratio, gs.sampledAt = 0.45, clk.now()
	})
	err := nmgr.Backpressure(streams.InboundEvents)
	if !errors.Is(err, ErrStreamBackpressure) {
		t.Fatalf("a runway refusal does not match ErrStreamBackpressure: %v", err)
	}
	var bpe *BackpressureError
	if !errors.As(err, &bpe) || !bpe.Runway || bpe.Stale || bpe.Durable != "inst_device-management_inbound-events" ||
		bpe.History != 320 || bpe.Rate != 11.6 || bpe.Ratio != 0.45 {
		t.Fatalf("Backpressure = %#v; want the runway durable, its history and rate", err)
	}
	for _, want := range []string{"320 messages", "inst_device-management_inbound-events", "at 12 a second"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not contain %q", err.Error(), want)
		}
	}
	// The ratio rule, when it also holds the gate closed, is the one named.
	setGate(nmgr, gs, func() { gs.closed, gs.worst, gs.ratio = true, "inst_device-management_inbound-events", 0.93 })
	if err := nmgr.Backpressure(streams.InboundEvents); !errors.As(err, &bpe) || bpe.Runway || bpe.Ratio != 0.93 {
		t.Fatalf("with both rules closed Backpressure = %#v; want the ratio refusal", err)
	}
}

func TestKickThreshold(t *testing.T) {
	for _, tc := range []struct{ ceiling, want int64 }{
		{1 << 30, 1 << 20},
		{5_000_000, 4882},
		{100, 1},
		{0, math.MaxInt64},
		{-1, math.MaxInt64},
	} {
		if got := kickThreshold(tc.ceiling, backpressureKickShare); got != tc.want {
			t.Errorf("kickThreshold(%d) = %d, want %d", tc.ceiling, got, tc.want)
		}
	}
}

// A writer's publishes ask for a measurement once their volume reaches a threshold, in bytes
// or in messages, and ask once while a request is waiting.
func TestPublishVolumeAsksForAMeasurement(t *testing.T) {
	kick := make(chan string, 4)
	gs := &gateState{suffix: streams.InboundEvents, kick: kick}
	gs.kickBytes.Store(100)
	gs.kickMsgs.Store(3)
	gs.notePublished(60)
	if len(kick) != 0 {
		t.Fatal("60 of 100 bytes asked for a measurement")
	}
	gs.notePublished(60)
	if len(kick) != 1 {
		t.Fatalf("120 of 100 bytes made %d requests; want 1", len(kick))
	}
	gs.notePublished(60)
	if len(kick) != 1 {
		t.Fatalf("a request already waiting was sent again: %d", len(kick))
	}
	if s := <-kick; s != streams.InboundEvents {
		t.Fatalf("the request named %q", s)
	}
	gs.kickQueued.Store(false)
	gs.pubBytes.Store(0)
	gs.pubMsgs.Store(0)
	gs.notePublished(1)
	if len(kick) != 0 {
		t.Fatal("1 of 3 messages asked for a measurement")
	}
	gs.notePublished(1)
	gs.notePublished(1)
	if len(kick) != 1 {
		t.Fatal("3 of 3 messages did not ask for a measurement")
	}
	// A stream with no gate, and a gate with volume-triggered measurement off, count nothing.
	var none *gateState
	none.notePublished(1 << 20)
	off := &gateState{}
	off.notePublished(1 << 20)
	if off.pubBytes.Load() != 0 {
		t.Fatal("a gate with no request channel counted volume")
	}
}
