// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
)

// The OrderedWriter keeps the deadline rule every publish in this package keeps (see
// publish_deadline_test.go): it waits until the earlier of the caller's deadline and the
// 5 s ceiling, measured from the send; a cancellation without a deadline is ignored; an
// expired deadline sends nothing. The timings are measured against ruledPublishCeiling, a
// literal, not against publishWait.

// orderedOutcome publishes one message and waits (bounded) for its outcome.
func orderedOutcome(t *testing.T, w OrderedWriter, ctx context.Context, limit time.Duration) (time.Duration, error) {
	t.Helper()
	type result struct {
		elapsed time.Duration
		err     error
	}
	got := make(chan result, 1)
	start := time.Now()
	w.Publish(ctx, Message{Value: []byte("payload")}, func(err error) {
		got <- result{time.Since(start), err}
	})
	select {
	case r := <-got:
		return r.elapsed, r.err
	case <-time.After(limit):
		t.Fatalf("HANG: no outcome within %v", limit)
		return 0, nil
	}
}

func TestOrderedPublishObeysCallerDeadline(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newOrdered(t, nmgr, 4)
	stubPublishes(t, srv, nmgr)
	defer w.Draining()

	ctx, cancel := context.WithTimeout(orderedCtx(), 300*time.Millisecond)
	defer cancel()
	elapsed, err := orderedOutcome(t, w, ctx, 10*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded (the caller's 300ms deadline)", err)
	}
	if elapsed >= time.Second {
		t.Errorf("outcome after %v, want well under 1s", elapsed)
	}
}

func TestOrderedPublishCeilingIsFiveSeconds(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newOrdered(t, nmgr, 4)
	stubPublishes(t, srv, nmgr)
	defer w.Draining()

	elapsed, err := orderedOutcome(t, w, orderedCtx(), 3*ruledPublishCeiling)
	if !errors.Is(err, nats.ErrTimeout) {
		t.Errorf("err = %v, want nats.ErrTimeout", err)
	}
	if elapsed < ruledPublishCeiling || elapsed > ruledPublishCeiling+1500*time.Millisecond {
		t.Errorf("outcome after %v, want the %v ceiling", elapsed, ruledPublishCeiling)
	}
}

func TestOrderedPublishIgnoresCancellation(t *testing.T) {
	_, nmgr := orderedManager(t)
	w := newOrdered(t, nmgr, 4)

	ctx, cancel := context.WithCancel(orderedCtx())
	cancel()
	_, err := orderedOutcome(t, w, ctx, 10*time.Second)
	if err != nil {
		t.Fatalf("a cancelled context without a deadline must still publish: %v", err)
	}
	w.Close()
	info, err := nmgr.js.StreamInfo(StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents))
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 1 {
		t.Errorf("stored: got %d, want 1", info.State.Msgs)
	}
}

func TestOrderedPublishWithExpiredDeadlineSendsNothing(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newOrdered(t, nmgr, 4)
	stub := stubPublishes(t, srv, nmgr)

	ctx, cancel := context.WithDeadline(orderedCtx(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := orderedOutcome(t, w, ctx, 10*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	// Read after the outcome, and after a beat for a request to arrive, so the zero is
	// reachable only by nothing having been sent.
	if got := stub.waitSeen(0); got != 0 {
		t.Errorf("requests sent past an expired deadline: got %d, want 0", got)
	}
	w.Close()
}
