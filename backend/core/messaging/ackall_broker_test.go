// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// These tests pin, against a real embedded broker, the AckAll behaviours a consumer that
// acknowledges only a floor would depend on. Each is asserted by VALUE (a pending count,
// the sequences redelivered, the refusal's text) and each carries a control run under
// explicit acknowledgement that must differ, so a test cannot pass because the broker
// stopped tracking acknowledgements at all.

type ackBroker struct {
	js jetstream.JetStream
}

// newAckBroker starts an embedded server and a stream holding n messages.
func newAckBroker(t *testing.T, n int) (*ackBroker, context.Context) {
	t.Helper()
	srv := startEmbeddedServer(t)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: "ACKALL", Subjects: []string{"ackall.>"}}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	for i := 1; i <= n; i++ {
		if _, err := js.Publish(ctx, "ackall.x", []byte(fmt.Sprint(i))); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	return &ackBroker{js: js}, ctx
}

func (a *ackBroker) consumer(ctx context.Context, t *testing.T, cfg jetstream.ConsumerConfig) jetstream.Consumer {
	t.Helper()
	c, err := a.js.CreateConsumer(ctx, "ACKALL", cfg)
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}
	return c
}

// fetchAll returns up to n messages in delivery order, waiting at most wait.
func fetchAll(t *testing.T, c jetstream.Consumer, n int, wait time.Duration) []jetstream.Msg {
	t.Helper()
	batch, err := c.Fetch(n, jetstream.FetchMaxWait(wait))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var out []jetstream.Msg
	for m := range batch.Messages() {
		out = append(out, m)
	}
	if err := batch.Error(); err != nil {
		t.Fatalf("fetch batch: %v", err)
	}
	return out
}

func seqs(t *testing.T, msgs []jetstream.Msg) []uint64 {
	t.Helper()
	out := make([]uint64, len(msgs))
	for i, m := range msgs {
		md, err := m.Metadata()
		if err != nil {
			t.Fatalf("metadata: %v", err)
		}
		out[i] = md.Sequence.Stream
	}
	return out
}

// waitPending polls the consumer until NumAckPending equals want. Acks are fire and
// forget, so the broker applies one a moment after the client returns.
func waitPending(ctx context.Context, t *testing.T, c jetstream.Consumer, want int) *jetstream.ConsumerInfo {
	t.Helper()
	var last *jetstream.ConsumerInfo
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		info, err := c.Info(ctx)
		if err != nil {
			t.Fatalf("consumer info: %v", err)
		}
		last = info
		if info.NumAckPending == want {
			return info
		}
	}
	t.Fatalf("NumAckPending = %d, want %d", last.NumAckPending, want)
	return nil
}

func rangeSeqs(lo, hi uint64) []uint64 {
	var out []uint64
	for s := lo; s <= hi; s++ {
		out = append(out, s)
	}
	return out
}

// TestAckAllFloorAckClearsPendingAndRedeliversOnlyAbove: acknowledging the message at seq 6
// of 10 delivered clears every pending entry at or below 6, and after AckWait only 7..10
// come back. Control: the same single ack under explicit acknowledgement clears one entry
// and the nine others redeliver.
func TestAckAllFloorAckClearsPendingAndRedeliversOnlyAbove(t *testing.T) {
	b, ctx := newAckBroker(t, 10)
	const ackWait = 2 * time.Second

	run := func(t *testing.T, policy jetstream.AckPolicy) (pending int, floor uint64, redelivered []uint64) {
		c := b.consumer(ctx, t, jetstream.ConsumerConfig{AckPolicy: policy, AckWait: ackWait})
		msgs := fetchAll(t, c, 10, 5*time.Second)
		if got := seqs(t, msgs); !slices.Equal(got, rangeSeqs(1, 10)) {
			t.Fatalf("first delivery = %v, want 1..10", got)
		}
		waitPending(ctx, t, c, 10)
		if err := msgs[5].Ack(); err != nil { // seq 6
			t.Fatalf("ack: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		info, err := c.Info(ctx)
		if err != nil {
			t.Fatalf("info: %v", err)
		}
		time.Sleep(ackWait + 300*time.Millisecond)
		again := fetchAll(t, c, 10, time.Second)
		for _, m := range again {
			md, _ := m.Metadata()
			if md.NumDelivered != 2 {
				t.Errorf("seq %d redelivered with NumDelivered %d, want 2", md.Sequence.Stream, md.NumDelivered)
			}
		}
		return info.NumAckPending, info.AckFloor.Stream, seqs(t, again)
	}

	t.Run("AckAll", func(t *testing.T) {
		pending, floor, again := run(t, jetstream.AckAllPolicy)
		if pending != 4 {
			t.Errorf("NumAckPending after acking seq 6 = %d, want 4 (7..10)", pending)
		}
		if floor != 6 {
			t.Errorf("ack floor stream seq = %d, want 6", floor)
		}
		if !slices.Equal(again, rangeSeqs(7, 10)) {
			t.Errorf("redelivered after AckWait = %v, want [7 8 9 10]", again)
		}
	})
	t.Run("control explicit", func(t *testing.T) {
		pending, _, again := run(t, jetstream.AckExplicitPolicy)
		if pending != 9 {
			t.Errorf("NumAckPending after acking only seq 6 = %d, want 9", pending)
		}
		if want := []uint64{1, 2, 3, 4, 5, 7, 8, 9, 10}; !slices.Equal(again, want) {
			t.Errorf("redelivered after AckWait = %v, want %v", again, want)
		}
	})
}

// TestAckAllAckBelowTheFloorIsANoOp: once the floor is at 8, acknowledging a lower
// delivered message changes nothing, so a stray ack of a redelivered low message cannot
// move the floor backwards.
func TestAckAllAckBelowTheFloorIsANoOp(t *testing.T) {
	b, ctx := newAckBroker(t, 10)
	c := b.consumer(ctx, t, jetstream.ConsumerConfig{AckPolicy: jetstream.AckAllPolicy, AckWait: time.Minute})
	msgs := fetchAll(t, c, 10, 5*time.Second)
	if len(msgs) != 10 {
		t.Fatalf("delivered %d, want 10", len(msgs))
	}
	if err := msgs[7].Ack(); err != nil { // seq 8
		t.Fatalf("ack 8: %v", err)
	}
	waitPending(ctx, t, c, 2)
	if err := msgs[2].Ack(); err != nil { // seq 3, below the floor
		t.Fatalf("ack 3: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	info, err := c.Info(ctx)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if info.NumAckPending != 2 || info.AckFloor.Stream != 8 {
		t.Errorf("after acking seq 3 below the floor: NumAckPending=%d floor=%d, want 2 and 8", info.NumAckPending, info.AckFloor.Stream)
	}
}

// TestAckAllFloorAckCoversMessagesTheHandlerNeverAcked: seq 1..5 are delivered and only 5
// is acknowledged; 1..4 leave pending without ever being acknowledged themselves.
func TestAckAllFloorAckCoversMessagesTheHandlerNeverAcked(t *testing.T) {
	b, ctx := newAckBroker(t, 5)
	c := b.consumer(ctx, t, jetstream.ConsumerConfig{AckPolicy: jetstream.AckAllPolicy, AckWait: time.Minute})
	msgs := fetchAll(t, c, 5, 5*time.Second)
	waitPending(ctx, t, c, 5)
	if err := msgs[4].Ack(); err != nil {
		t.Fatalf("ack: %v", err)
	}
	info := waitPending(ctx, t, c, 0)
	if info.AckFloor.Stream != 5 || info.NumRedelivered != 0 {
		t.Errorf("floor=%d redelivered=%d, want 5 and 0", info.AckFloor.Stream, info.NumRedelivered)
	}
}

// TestAckAllTermAcksEverythingBelow: under AckAll a Term on seq 3 acknowledges 1 and 2 as
// well, so only 4 and 5 stay pending and redeliver. Control: under explicit acknowledgement
// a Term on seq 3 removes only seq 3.
func TestAckAllTermAcksEverythingBelow(t *testing.T) {
	b, ctx := newAckBroker(t, 5)
	const ackWait = 2 * time.Second

	run := func(t *testing.T, policy jetstream.AckPolicy) (pending int, redelivered []uint64) {
		c := b.consumer(ctx, t, jetstream.ConsumerConfig{AckPolicy: policy, AckWait: ackWait})
		msgs := fetchAll(t, c, 5, 5*time.Second)
		waitPending(ctx, t, c, 5)
		if err := msgs[2].Term(); err != nil { // seq 3
			t.Fatalf("term: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		info, err := c.Info(ctx)
		if err != nil {
			t.Fatalf("info: %v", err)
		}
		time.Sleep(ackWait + 300*time.Millisecond)
		return info.NumAckPending, seqs(t, fetchAll(t, c, 5, time.Second))
	}

	t.Run("AckAll", func(t *testing.T) {
		pending, again := run(t, jetstream.AckAllPolicy)
		if pending != 2 {
			t.Errorf("NumAckPending after Term on seq 3 = %d, want 2 (4, 5)", pending)
		}
		if !slices.Equal(again, []uint64{4, 5}) {
			t.Errorf("redelivered = %v, want [4 5]", again)
		}
	})
	t.Run("control explicit", func(t *testing.T) {
		pending, again := run(t, jetstream.AckExplicitPolicy)
		if pending != 4 {
			t.Errorf("NumAckPending after Term on seq 3 = %d, want 4", pending)
		}
		if !slices.Equal(again, []uint64{1, 2, 4, 5}) {
			t.Errorf("redelivered = %v, want [1 2 4 5]", again)
		}
	})
}

// TestAckAllDurableCannotChangeAckPolicyOrStart: an existing durable refuses CreateConsumer
// with a different ack policy and refuses an update that changes the ack policy or the
// start sequence, and is left as it was. The refusal text is logged and matched. Control:
// an update that changes only an updatable field (AckWait) is accepted.
func TestAckAllDurableCannotChangeAckPolicyOrStart(t *testing.T) {
	b, ctx := newAckBroker(t, 5)
	base := jetstream.ConsumerConfig{
		Durable: "d", AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute,
		DeliverPolicy: jetstream.DeliverByStartSequencePolicy, OptStartSeq: 2,
	}
	b.consumer(ctx, t, base)

	apiErr := func(t *testing.T, err error) *jetstream.APIError {
		t.Helper()
		if err == nil {
			t.Fatal("changed config was accepted, want a refusal")
		}
		var ae *jetstream.APIError
		if !errors.As(err, &ae) {
			t.Fatalf("error is %T %v, want *jetstream.APIError", err, err)
		}
		return ae
	}

	t.Run("create with a different ack policy", func(t *testing.T) {
		cfg := base
		cfg.AckPolicy = jetstream.AckAllPolicy
		_, err := b.js.CreateConsumer(ctx, "ACKALL", cfg)
		if err == nil {
			t.Fatal("create over an existing durable with a different ack policy was accepted")
		}
		t.Logf("create AckAll over explicit: %v", err)
		if !errors.Is(err, jetstream.ErrConsumerExists) {
			t.Errorf("error = %v, want jetstream.ErrConsumerExists", err)
		}
	})
	t.Run("update the ack policy", func(t *testing.T) {
		cfg := base
		cfg.AckPolicy = jetstream.AckAllPolicy
		_, err := b.js.UpdateConsumer(ctx, "ACKALL", cfg)
		ae := apiErr(t, err)
		t.Logf("update to AckAll: code=%d err_code=%d %q", ae.Code, ae.ErrorCode, ae.Description)
		if !strings.Contains(ae.Description, "ack policy can not be updated") {
			t.Errorf("description = %q, want it to contain \"ack policy can not be updated\"", ae.Description)
		}
	})
	t.Run("update the start sequence", func(t *testing.T) {
		cfg := base
		cfg.OptStartSeq = 4
		_, err := b.js.UpdateConsumer(ctx, "ACKALL", cfg)
		ae := apiErr(t, err)
		t.Logf("update start seq 2 -> 4: code=%d err_code=%d %q", ae.Code, ae.ErrorCode, ae.Description)
		if !strings.Contains(ae.Description, "start sequence can not be updated") {
			t.Errorf("description = %q, want it to contain \"start sequence can not be updated\"", ae.Description)
		}
	})
	t.Run("the durable is unchanged", func(t *testing.T) {
		c, err := b.js.Consumer(ctx, "ACKALL", "d")
		if err != nil {
			t.Fatalf("consumer: %v", err)
		}
		cfg := c.CachedInfo().Config
		if cfg.AckPolicy != jetstream.AckExplicitPolicy || cfg.OptStartSeq != 2 {
			t.Errorf("durable became ack=%v startseq=%d, want explicit and 2", cfg.AckPolicy, cfg.OptStartSeq)
		}
	})
	t.Run("control: an allowed change is accepted", func(t *testing.T) {
		cfg := base
		cfg.AckWait = 2 * time.Minute
		if _, err := b.js.UpdateConsumer(ctx, "ACKALL", cfg); err != nil {
			t.Fatalf("updating AckWait was refused: %v", err)
		}
	})
}

// TestAckAllMaxAckPendingThrottlesDeliveryUntilTheFloorMoves: with MaxAckPending 5 and 12
// messages, five are delivered and then delivery stops while nothing is acknowledged. One
// floor ack at the fifth frees all five slots, so 6..10 follow. Control: under explicit
// acknowledgement the same single ack frees one slot, so only one more message follows.
func TestAckAllMaxAckPendingThrottlesDeliveryUntilTheFloorMoves(t *testing.T) {
	b, ctx := newAckBroker(t, 12)

	run := func(t *testing.T, policy jetstream.AckPolicy) []uint64 {
		c := b.consumer(ctx, t, jetstream.ConsumerConfig{AckPolicy: policy, AckWait: time.Minute, MaxAckPending: 5})
		first := fetchAll(t, c, 12, 2*time.Second)
		if got := seqs(t, first); !slices.Equal(got, rangeSeqs(1, 5)) {
			t.Fatalf("delivered with MaxAckPending 5 = %v, want [1 2 3 4 5]", got)
		}
		if more := fetchAll(t, c, 12, time.Second); len(more) != 0 {
			t.Fatalf("delivered %v past MaxAckPending with no ack", seqs(t, more))
		}
		if err := first[4].Ack(); err != nil { // seq 5
			t.Fatalf("ack: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
		return seqs(t, fetchAll(t, c, 12, 2*time.Second))
	}

	t.Run("AckAll", func(t *testing.T) {
		if got := run(t, jetstream.AckAllPolicy); !slices.Equal(got, rangeSeqs(6, 10)) {
			t.Errorf("after one floor ack at 5, delivered %v, want [6 7 8 9 10]", got)
		}
	})
	t.Run("control explicit", func(t *testing.T) {
		if got := run(t, jetstream.AckExplicitPolicy); !slices.Equal(got, []uint64{6}) {
			t.Errorf("after one ack of seq 5, delivered %v, want exactly [6]", got)
		}
	})
}
