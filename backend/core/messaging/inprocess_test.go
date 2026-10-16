// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// A replicated stream answers a publish whose Nats-Msg-Id is still being replicated with
// "duplicate message id is in process". That is a re-publish racing its own original, and
// the id resolves within a replication round trip, so every writer must send it again rather
// than report a failure. The broker's answer is reproduced here by a responder on the
// stream's subject that sends the exact JSON nats-server sends, so the error reaches the
// writers through the real client's decoding, as a *nats.APIError.

// inProcessReply is nats-server's JSStreamDuplicateMessageConflict, as it goes on the wire.
const inProcessReply = `{"error":{"code":409,"err_code":10158,"description":"duplicate message id is in process"}}`

// wrongLastSeqReply is another 409, for a conflict that must NOT be re-sent.
const wrongLastSeqReply = `{"error":{"code":409,"err_code":10071,"description":"wrong last sequence: 1"}}`

// idStub answers every publish on the resolved-events subject: with reply for the first
// conflicts requests carrying a given id (every request when conflicts is negative), and
// after that with a duplicate PubAck — the original committed.
type idStub struct {
	stream    string
	reply     string
	conflicts int

	mu   sync.Mutex
	sent map[string]int
}

func stubIDs(t *testing.T, srv *natsserver.Server, nmgr *NatsManager, reply string, conflicts int) *idStub {
	t.Helper()
	deleteStream(t, nmgr)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect stub: %v", err)
	}
	t.Cleanup(nc.Close)
	s := &idStub{
		stream:    StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents),
		reply:     reply,
		conflicts: conflicts,
		sent:      map[string]int{},
	}
	subject := ScopedSubject(nmgr.Microservice.InstanceId, orderedTenant, streams.ResolvedEvents)
	if _, err := SubscribeSynced(nc, subject, func(m *nats.Msg) {
		id := m.Header.Get(nats.MsgIdHdr)
		s.mu.Lock()
		s.sent[id]++
		n := s.sent[id]
		s.mu.Unlock()
		if s.conflicts < 0 || n <= s.conflicts {
			_ = m.Respond([]byte(s.reply))
			return
		}
		_ = m.Respond([]byte(fmt.Sprintf(`{"stream":%q,"seq":1,"duplicate":true}`, s.stream)))
	}); err != nil {
		t.Fatalf("subscribe stub: %v", err)
	}
	return s
}

// requests is how many publishes the stub has seen under id.
func (s *idStub) requests(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent[id]
}

// The classification is by the error code, which nats-server gives this conflict alone; the
// HTTP status 409 it shares with other conflicts that a re-send cannot resolve.
func TestDuplicateInProcessIsTheBrokersCodeAndNothingElse(t *testing.T) {
	inProcess := &nats.APIError{Code: 409, ErrorCode: 10158, Description: "duplicate message id is in process"}
	if !duplicateInProcess(inProcess) {
		t.Fatal("the broker's in-process answer was not recognised")
	}
	if !duplicateInProcess(fmt.Errorf("publish: %w", inProcess)) {
		t.Fatal("a wrapped in-process answer was not recognised")
	}
	for _, err := range []error{
		nil,
		nats.ErrTimeout,
		errors.New("nats: duplicate message id is in process"), // a string is not the broker's answer
		&nats.APIError{Code: 409, ErrorCode: 10071, Description: "wrong last sequence: 1"},
		&nats.APIError{Code: 400, ErrorCode: 10201, Description: "atomic publish batch contains duplicate message id"},
	} {
		if duplicateInProcess(err) {
			t.Fatalf("%v was taken for an id in process", err)
		}
	}
}

// An ordered writer's publish whose id is in process is sent again and settles on the
// PubAck the id resolves to: a success, in order, counted as the duplicate it is, and
// without starting the failure backoff.
//
// GUARD: kills a writer that reports the conflict (a failed outcome, and a backoff), one
// that resolves it without the duplicate's PubAck (the counter reads 0), and a classifier
// that does not recognise the broker's answer.
func TestOrderedWriterResendsAPublishWhoseIdIsInProcess(t *testing.T) {
	const n = 3
	srv := startEmbeddedServer(t)
	nmgr, _ := metricsManager(t, srv)
	w := newOrdered(t, nmgr, 8)
	stub := stubIDs(t, srv, nmgr, inProcessReply, 1)

	out := newOutcomes()
	for i := 0; i < n; i++ {
		w.Publish(orderedCtx(), Message{Value: []byte("e"), DedupID: dedupID(i)}, out.done(i))
	}
	if got := out.waitCount(n, 10*time.Second); got != n {
		t.Fatalf("%d of %d publishes were reported", got, n)
	}
	w.Close()
	for i := 0; i < n; i++ {
		if out.errs[i] != nil {
			t.Fatalf("publish %d, whose id was in process, failed: %v", i, out.errs[i])
		}
		if got := stub.requests(dedupID(i)); got != 2 {
			t.Fatalf("publish %d was sent %d times, want 2 (the conflict, then the re-send)", i, got)
		}
		if out.order[i] != i {
			t.Fatalf("outcomes reported in order %v, want submission order", out.order)
		}
	}
	if got := duplicateCount(nmgr); got != n {
		t.Fatalf("duplicates counted: %v, want %d (each id resolved to its original)", got, n)
	}
	if b := w.(*orderedWriter).backoff; b != 0 {
		t.Fatalf("the writer is backing off (%v) after publishes that all succeeded", b)
	}
}

// The AsyncWriter publishes through an ordered writer and so inherits its re-send: a Submit
// whose id is in process reports success.
//
// GUARD: kills a fan-in that takes the conflict for a failure.
func TestAsyncWriterResendsAPublishWhoseIdIsInProcess(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, _ := metricsManager(t, srv)
	w, err := nmgr.NewAsyncWriter(streams.ResolvedEvents, WriterResolution, 8, false)
	if err != nil {
		t.Fatal(err)
	}
	stub := stubIDs(t, srv, nmgr, inProcessReply, 2)
	result := make(chan error, 1)
	w.Submit(orderedCtx(), []Message{{Value: []byte("e"), DedupID: dedupID(0)}}, func(err error) { result <- err })
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("a submission whose id was in process failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the submission was never reported")
	}
	w.Close()
	if got := stub.requests(dedupID(0)); got != 3 {
		t.Fatalf("sent %d times, want 3 (two conflicts, then the re-send that resolved)", got)
	}
}

// The synchronous writer re-sends too, and returns success once the id resolves.
//
// GUARD: kills a writer that returns the conflict to its caller.
func TestMessageWriterResendsAPublishWhoseIdIsInProcess(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, _ := metricsManager(t, srv)
	w, err := nmgr.NewWriter(streams.ResolvedEvents)
	if err != nil {
		t.Fatal(err)
	}
	stub := stubIDs(t, srv, nmgr, inProcessReply, 2)
	// Past the gate whatever it reads with its stream deleted: the gate is not under test.
	msgs := []Message{
		{Value: []byte("e"), DedupID: dedupID(0), BypassBackpressure: true},
		{Value: []byte("e"), DedupID: dedupID(1), BypassBackpressure: true},
	}
	if err := w.WriteMessages(orderedCtx(), msgs...); err != nil {
		t.Fatalf("a write whose ids were in process failed: %v", err)
	}
	for i := range msgs {
		if got := stub.requests(dedupID(i)); got != 3 {
			t.Fatalf("message %d sent %d times, want 3 (two conflicts, then the re-send that resolved)", i, got)
		}
	}
}

// An id that stays in process is not re-sent forever: the publish fails at its deadline,
// with the conflict it last got, having been re-sent meanwhile.
//
// GUARD: kills a re-send loop that ignores the deadline (the watchdog fires), one that
// never re-sends (one request), and one that does not wait between re-sends (dozens).
func TestAnIdStillInProcessAtTheDeadlineFailsThePublish(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, _ := metricsManager(t, srv)
	sw, err := nmgr.NewWriter(streams.ResolvedEvents)
	if err != nil {
		t.Fatal(err)
	}
	ordered := newOrdered(t, nmgr, 8)
	stub := stubIDs(t, srv, nmgr, inProcessReply, -1)
	const limit = 300 * time.Millisecond

	syncErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(orderedCtx(), limit)
		defer cancel()
		syncErr <- sw.WriteMessages(ctx, Message{Value: []byte("e"), DedupID: dedupID(0), BypassBackpressure: true})
	}()
	orderedErr := make(chan error, 1)
	ctx, cancel := context.WithTimeout(orderedCtx(), limit)
	defer cancel()
	ordered.Publish(ctx, Message{Value: []byte("e"), DedupID: dedupID(1)}, func(err error) { orderedErr <- err })

	for i, ch := range []chan error{syncErr, orderedErr} {
		select {
		case err := <-ch:
			if !duplicateInProcess(err) {
				t.Fatalf("publish %d, in process past its deadline, reported %v, want the in-process conflict", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("publish %d, in process past its %v deadline, was still being re-sent 5 s later", i, limit)
		}
		if got := stub.requests(dedupID(i)); got < 2 {
			t.Fatalf("publish %d was sent %d times before failing, want it re-sent", i, got)
		}
		// Paced, not a hot loop against a broker that is still replicating: waits of 10, 20,
		// 40, 80 and 160 ms fit about five sends into the limit.
		if got := stub.requests(dedupID(i)); got > 8 {
			t.Fatalf("publish %d was sent %d times in %v, want the re-sends paced (about 5)", i, got, limit)
		}
	}
	ordered.Close()
}

// A conflict a re-send cannot resolve is reported at once by both writers, after one send.
//
// GUARD: kills a classifier that re-sends on the HTTP status (409) instead of on the code.
func TestAnotherConflictIsNotResent(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, _ := metricsManager(t, srv)
	sw, err := nmgr.NewWriter(streams.ResolvedEvents)
	if err != nil {
		t.Fatal(err)
	}
	ordered := newOrdered(t, nmgr, 8)
	stub := stubIDs(t, srv, nmgr, wrongLastSeqReply, -1)

	var apiErr *nats.APIError
	err = sw.WriteMessages(orderedCtx(), Message{Value: []byte("e"), DedupID: dedupID(0), BypassBackpressure: true})
	if !errors.As(err, &apiErr) || apiErr.ErrorCode != 10071 {
		t.Fatalf("the synchronous writer reported %v, want the wrong-last-sequence conflict", err)
	}
	result := make(chan error, 1)
	ordered.Publish(orderedCtx(), Message{Value: []byte("e"), DedupID: dedupID(1)}, func(err error) { result <- err })
	select {
	case err := <-result:
		if !errors.As(err, &apiErr) || apiErr.ErrorCode != 10071 {
			t.Fatalf("the ordered writer reported %v, want the wrong-last-sequence conflict", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the ordered publish was never reported")
	}
	ordered.Draining()
	ordered.Close()
	for i := 0; i < 2; i++ {
		if got := stub.requests(dedupID(i)); got != 1 {
			t.Fatalf("publish %d sent %d times, want 1: only an id in process is re-sent", i, got)
		}
	}
}
