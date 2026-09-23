// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// A publish reads exactly one thing from its context: the deadline. It waits until
// the earlier of that deadline and publishWait, and it does NOT stop early because
// the context was cancelled. The two halves pull in opposite directions and both are
// load-bearing:
//
//   - a caller that carries a deadline must get it honoured — before this, every
//     publish waited the full 5 s the JetStream context defaulted to, whatever the
//     caller had asked for;
//   - a caller whose context is already CANCELLED must still publish. The drain stage
//     of a shutdown and the post-commit publishers run on a root or request context
//     that has been cancelled by then; honouring that would drop, on every rolling
//     restart, failed-events whose source had already been acked.

// publishDeadlineWriter builds a writer through the real constructors (which creates
// the stream) and returns it with the manager and the tenant-scoped subject it writes.
func publishDeadlineWriter(t *testing.T, srv *natsserver.Server) (*NatsManager, MessageWriter, string) {
	t.Helper()
	nmgr := startedManager(t, srv, "publish-deadline", nil)
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
	w, err := nmgr.NewWriter(streams.FailedEvents)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	return nmgr, w, ScopedSubject(nmgr.Microservice.InstanceId, "acme", streams.FailedEvents)
}

// swallowPublishes deletes the writer's stream and puts a plain core subscription on
// its subject that receives every publish request and never answers it — a broker
// that has the request and has not acked it. With no stream there is no JetStream
// responder, and with this subscriber there IS a responder, so the publish neither
// fails fast with "no responders" nor succeeds: it waits, which is the thing measured.
func swallowPublishes(t *testing.T, srv *natsserver.Server, nmgr *NatsManager, subject string) {
	t.Helper()
	if err := nmgr.js.DeleteStream(StreamName(nmgr.Microservice.InstanceId, streams.FailedEvents)); err != nil {
		t.Fatalf("delete stream: %v", err)
	}
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect swallower: %v", err)
	}
	t.Cleanup(nc.Close)
	if _, err := SubscribeSynced(nc, subject, func(*nats.Msg) {}); err != nil {
		t.Fatalf("subscribe swallower: %v", err)
	}
}

// storedCount is the writer stream's message count as the broker reports it.
func storedCount(t *testing.T, nmgr *NatsManager) uint64 {
	t.Helper()
	info, err := nmgr.js.StreamInfo(StreamName(nmgr.Microservice.InstanceId, streams.FailedEvents))
	if err != nil {
		t.Fatalf("stream info: %v", err)
	}
	return info.State.Msgs
}

// timedWrite runs one WriteMessages under a hard test-side deadline, so a publish that
// never returns fails the test as a HANG rather than stalling the package until the
// go test timeout.
func timedWrite(t *testing.T, w MessageWriter, ctx context.Context, limit time.Duration) (time.Duration, error) {
	t.Helper()
	if dl, ok := t.Deadline(); ok && time.Until(dl) < limit+5*time.Second {
		t.Fatalf("not enough test time left (%v) to bound a %v publish", time.Until(dl), limit)
	}
	type result struct {
		elapsed time.Duration
		err     error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		err := w.WriteMessages(ctx, Message{Value: []byte("payload")})
		done <- result{time.Since(start), err}
	}()
	select {
	case r := <-done:
		return r.elapsed, r.err
	case <-time.After(limit):
		t.Fatalf("HANG: WriteMessages did not return within %v", limit)
		return 0, nil
	}
}

// A caller's deadline shorter than the ceiling is the one the publish obeys, and the
// error it gets is the caller's own: context.DeadlineExceeded, not a broker timeout.
func TestPublishObeysCallerDeadline(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, w, subject := publishDeadlineWriter(t, srv)
	swallowPublishes(t, srv, nmgr, subject)

	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), "acme"), 200*time.Millisecond)
	defer cancel()
	elapsed, err := timedWrite(t, w, ctx, 10*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded (the caller's 200ms deadline)", err)
	}
	if elapsed >= time.Second {
		t.Errorf("publish took %v against a 200ms caller deadline, want < 1s: the deadline was "+
			"ignored and the publish waited out the default ceiling", elapsed)
	}
}

// A deadline that has already passed publishes nothing: the broker is not asked, so
// the stream's count does not move and the caller gets its own expiry back.
func TestExpiredDeadlinePublishesNothing(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, w, _ := publishDeadlineWriter(t, srv)
	before := storedCount(t, nmgr)

	ctx, cancel := context.WithDeadline(core.WithTenant(context.Background(), "acme"), time.Now().Add(-time.Second))
	defer cancel()
	_, err := timedWrite(t, w, ctx, 10*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded for an already-expired deadline", err)
	}
	if after := storedCount(t, nmgr); after != before {
		t.Errorf("stream holds %d messages after an expired-deadline publish, want %d (unchanged): "+
			"a caller told the write failed has a message on the stream anyway", after, before)
	}
}

// Cancellation is not a deadline. A cancelled context — with no deadline, or with one
// still in the future — must still publish, because the callers that hand one over are
// the drain and post-commit paths whose source message has already been acked.
func TestCancelledContextStillPublishes(t *testing.T) {
	cases := []struct {
		name string
		ctx  func() context.Context
	}{
		{"cancelled, no deadline", func() context.Context {
			ctx, cancel := context.WithCancel(core.WithTenant(context.Background(), "acme"))
			cancel()
			return ctx
		}},
		{"cancelled, deadline still in the future", func() context.Context {
			ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), "acme"), time.Minute)
			cancel()
			return ctx
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := startEmbeddedServer(t)
			nmgr, w, _ := publishDeadlineWriter(t, srv)
			before := storedCount(t, nmgr)
			ctx := tc.ctx()
			if ctx.Err() == nil {
				t.Fatal("test setup: the context is not cancelled")
			}
			if _, err := timedWrite(t, w, ctx, 10*time.Second); err != nil {
				t.Errorf("publish on a cancelled context returned %v, want nil: an already-acked "+
					"message would be dropped on every rolling restart", err)
			}
			if after := storedCount(t, nmgr); after != before+1 {
				t.Errorf("stream holds %d messages, want %d: the message is not on the stream", after, before+1)
			}
		})
	}
}

// A caller with no deadline still gets the ceiling: the publish does not wait forever
// on an unanswered request, and the error is the broker timeout it has always been.
func TestPublishWithoutDeadlineKeepsTheCeiling(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, w, subject := publishDeadlineWriter(t, srv)
	swallowPublishes(t, srv, nmgr, subject)

	elapsed, err := timedWrite(t, w, core.WithTenant(context.Background(), "acme"), 15*time.Second)
	if !errors.Is(err, nats.ErrTimeout) {
		t.Errorf("err = %v, want nats.ErrTimeout (the publish ceiling, not a caller deadline)", err)
	}
	if elapsed < publishWait || elapsed >= publishWait+1500*time.Millisecond {
		t.Errorf("publish took %v, want within [%v, %v)", elapsed, publishWait, publishWait+1500*time.Millisecond)
	}
}

// A caller deadline LONGER than the ceiling does not extend it, and when the ceiling is
// what fired the caller is told so with nats.ErrTimeout — its own deadline has not
// passed, so DeadlineExceeded would be a lie about whose limit ran out.
func TestLongerCallerDeadlineIsCappedAtTheCeiling(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, w, subject := publishDeadlineWriter(t, srv)
	swallowPublishes(t, srv, nmgr, subject)

	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), "acme"), 30*time.Second)
	defer cancel()
	elapsed, err := timedWrite(t, w, ctx, 15*time.Second)
	if !errors.Is(err, nats.ErrTimeout) {
		t.Errorf("err = %v, want nats.ErrTimeout: the ceiling fired, not the caller's 30s deadline", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v reports the caller's deadline as exceeded; it has %v left", err, time.Until(mustDeadline(t, ctx)))
	}
	if elapsed < publishWait || elapsed >= publishWait+1500*time.Millisecond {
		t.Errorf("publish took %v, want within [%v, %v): the caller's longer deadline replaced the ceiling",
			elapsed, publishWait, publishWait+1500*time.Millisecond)
	}
}

func mustDeadline(t *testing.T, ctx context.Context) time.Time {
	t.Helper()
	d, ok := ctx.Deadline()
	if !ok {
		t.Fatal("context has no deadline")
	}
	return d
}
