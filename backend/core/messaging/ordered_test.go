// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// The OrderedWriter over a real embedded broker. Where the behaviour under test is what the
// broker says — held, refused, silent, absent — the stream is deleted and replaced by
// publishStub, a responder on its subject that answers each request only when told to.

const orderedTenant = "acme"

func orderedCtx() context.Context { return core.WithTenant(context.Background(), orderedTenant) }

// orderedManager is a started manager on a fresh embedded server, stopped at cleanup.
func orderedManager(t *testing.T) (*natsserver.Server, *NatsManager) {
	t.Helper()
	srv := startEmbeddedServer(t)
	nmgr := startedManager(t, srv, "ordered", nil)
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
	return srv, nmgr
}

func newOrdered(t *testing.T, nmgr *NatsManager, window int) OrderedWriter {
	t.Helper()
	w, err := nmgr.NewOrderedWriter(streams.ResolvedEvents, window)
	if err != nil {
		t.Fatalf("new ordered writer: %v", err)
	}
	return w
}

// publishStub records every publish request on one tenant subject and answers only when told.
type publishStub struct {
	stream string

	mu         sync.Mutex
	reqs       []*nats.Msg
	answeredAt []time.Time
	seq        uint64
}

// stubPublishes deletes the resolved-events stream and subscribes a stub on its subject, so a
// publish waits until the test answers it. Without a stub at all (deleteStream alone) a
// publish fails fast with "no responders".
func stubPublishes(t *testing.T, srv *natsserver.Server, nmgr *NatsManager) *publishStub {
	t.Helper()
	deleteStream(t, nmgr)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect stub: %v", err)
	}
	t.Cleanup(nc.Close)
	s := &publishStub{stream: StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents)}
	subject := ScopedSubject(nmgr.Microservice.InstanceId, orderedTenant, streams.ResolvedEvents)
	if _, err := SubscribeSynced(nc, subject, func(m *nats.Msg) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.reqs = append(s.reqs, m)
		s.answeredAt = append(s.answeredAt, time.Time{})
	}); err != nil {
		t.Fatalf("subscribe stub: %v", err)
	}
	return s
}

func deleteStream(t *testing.T, nmgr *NatsManager) {
	t.Helper()
	if err := nmgr.js.DeleteStream(StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents)); err != nil {
		t.Fatalf("delete stream: %v", err)
	}
}

func (s *publishStub) seen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

// answer replies to request i with a PubAck. The time is recorded before the reply goes.
func (s *publishStub) answer(i int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	s.answeredAt[i] = time.Now()
	_ = s.reqs[i].Respond([]byte(fmt.Sprintf(`{"stream":%q,"seq":%d}`, s.stream, s.seq)))
}

func (s *publishStub) answeredTime(i int) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.answeredAt[i]
}

// waitSeen waits up to 5s for the stub to hold want requests, then for 200ms more in which
// the count must not move, and returns it.
func (s *publishStub) waitSeen(want int) int {
	deadline := time.Now().Add(5 * time.Second)
	for s.seen() < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	return s.seen()
}

// outcomes records every done callback: its index, error and time, in call order.
type outcomes struct {
	mu    sync.Mutex
	order []int
	errs  map[int]error
	at    map[int]time.Time
}

func newOutcomes() *outcomes { return &outcomes{errs: map[int]error{}, at: map[int]time.Time{}} }

func (o *outcomes) done(i int) func(error) {
	return func(err error) {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.order = append(o.order, i)
		o.errs[i] = err
		o.at[i] = time.Now()
	}
}

func (o *outcomes) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.order)
}

func (o *outcomes) waitCount(want int, limit time.Duration) int {
	deadline := time.Now().Add(limit)
	for o.count() < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return o.count()
}

// At most window publishes are in flight: the broker holds exactly window requests, the
// submitter is blocked on the next, and one answer lets exactly one more through.
func TestOrderedWriterHoldsAtMostWindowPublishesInFlight(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newOrdered(t, nmgr, 8)
	stub := stubPublishes(t, srv, nmgr)
	out := newOutcomes()

	var returned sync.WaitGroup
	var mu sync.Mutex
	returnedCount := 0
	returned.Add(1)
	go func() {
		defer returned.Done()
		for i := 0; i < 20; i++ {
			w.Publish(orderedCtx(), Message{Value: []byte(strconv.Itoa(i))}, out.done(i))
			mu.Lock()
			returnedCount++
			mu.Unlock()
		}
	}()

	if got := stub.waitSeen(8); got != 8 {
		t.Fatalf("requests in flight with a window of 8: got %d, want 8", got)
	}
	mu.Lock()
	got := returnedCount
	mu.Unlock()
	if got != 8 {
		t.Fatalf("Publish calls returned with the window full: got %d, want 8 (the 9th must block)", got)
	}

	stub.answer(0)
	if got := stub.waitSeen(9); got != 9 {
		t.Fatalf("requests in flight after one answer: got %d, want 9", got)
	}
	for i := 1; i < 20; i++ {
		for stub.seen() <= i {
			time.Sleep(time.Millisecond)
		}
		stub.answer(i)
	}
	returned.Wait()
	w.Close()
	if got := out.count(); got != 20 {
		t.Errorf("outcomes reported by Close: got %d, want 20", got)
	}
}

// Outcomes are reported in SUBMISSION order, whatever order the broker answers in, and none
// is reported before its answer.
func TestOrderedWriterReportsOutcomesInSubmissionOrder(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newOrdered(t, nmgr, 16)
	stub := stubPublishes(t, srv, nmgr)
	out := newOutcomes()

	for i := 0; i < 16; i++ {
		w.Publish(orderedCtx(), Message{Value: []byte(strconv.Itoa(i))}, out.done(i))
	}
	if got := stub.waitSeen(16); got != 16 {
		t.Fatalf("requests in flight: got %d, want 16", got)
	}
	if got := out.count(); got != 0 {
		t.Fatalf("outcomes reported with nothing answered: got %d, want 0", got)
	}
	for i := 15; i >= 0; i-- {
		stub.answer(i)
	}
	w.Close()

	out.mu.Lock()
	defer out.mu.Unlock()
	want := make([]int, 16)
	for i := range want {
		want[i] = i
	}
	if fmt.Sprint(out.order) != fmt.Sprint(want) {
		t.Fatalf("outcome order: got %v, want %v", out.order, want)
	}
	for i := 0; i < 16; i++ {
		if out.errs[i] != nil {
			t.Errorf("outcome %d: %v, want nil", i, out.errs[i])
		}
		if out.at[i].Before(stub.answeredTime(i)) {
			t.Errorf("outcome %d reported at %v, before its PubAck was sent at %v", i, out.at[i], stub.answeredTime(i))
		}
	}
}

// What reaches the stream is what was submitted, in submission order — on a single server
// and on a replicated stream, where the publish is routed to a stream leader that may be on
// another server.
func TestOrderedWriterStreamOrderIsSubmissionOrder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		replicas int
	}{{"R1", 1}, {"R3", 3}} {
		t.Run(tc.name, func(t *testing.T) {
			var nmgr *NatsManager
			if tc.replicas == 1 {
				_, nmgr = orderedManager(t)
			} else {
				nmgr = clusterManager(t)
			}
			w := newOrdered(t, nmgr, 128)
			stream := StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents)
			// One probe message, stored first: a new replicated stream can have a leader before
			// the server this client is connected to sees its interest, and a publish in that
			// window is answered "no responders" — which is the broker, not the writer.
			probe := waitUntilPublishable(t, nmgr)
			const n = 5000
			out := newOutcomes()
			for i := 0; i < n; i++ {
				w.Publish(orderedCtx(), Message{Value: []byte(strconv.Itoa(i))}, out.done(i))
			}
			w.Draining()
			w.Close()
			for i := 0; i < n; i++ {
				if err := out.errs[i]; err != nil {
					t.Fatalf("publish %d failed: %v", i, err)
				}
			}
			info, err := nmgr.js.StreamInfo(stream)
			if err != nil {
				t.Fatal(err)
			}
			if info.Config.Replicas != tc.replicas {
				t.Fatalf("stream replicas: got %d, want %d", info.Config.Replicas, tc.replicas)
			}
			for seq := probe + 1; seq <= probe+n; seq++ {
				raw, err := nmgr.js.GetMsg(stream, seq)
				if err != nil {
					t.Fatalf("stream sequence %d: %v", seq, err)
				}
				if want := strconv.Itoa(int(seq - probe - 1)); string(raw.Data) != want {
					t.Fatalf("stream sequence %d holds %q, want %q", seq, raw.Data, want)
				}
			}
		})
	}
}

// waitUntilPublishable publishes one probe message to resolved-events, retrying until the
// broker stores it, and returns the stream sequence it was stored at.
func waitUntilPublishable(t *testing.T, nmgr *NatsManager) uint64 {
	t.Helper()
	w, err := nmgr.NewWriter(streams.ResolvedEvents)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := w.WriteMessages(core.WithTenant(context.Background(), "probe"), Message{Value: []byte("probe")})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resolved-events never accepted a publish: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	info, err := nmgr.js.StreamInfo(StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents))
	if err != nil {
		t.Fatal(err)
	}
	return info.State.LastSeq
}

// clusterManager is a started manager on a 3-node cluster, configured for R3 streams.
func clusterManager(t *testing.T) *NatsManager {
	t.Helper()
	servers := dctest.StartJetStreamCluster(t, 3)
	ms := testMicroservice(t, servers[0], uniqueArea("ordered-r3"))
	ms.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	nmgr.RecordMaxDeliveries(recordNothing)
	if err := nmgr.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := nmgr.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
	return nmgr
}

// fakeFuture is a PubAckFuture whose result is set by the test.
type fakeFuture struct {
	ok  chan *nats.PubAck
	err chan error
}

func (f *fakeFuture) Ok() <-chan *nats.PubAck { return f.ok }
func (f *fakeFuture) Err() <-chan error       { return f.err }
func (f *fakeFuture) Msg() *nats.Msg          { return nil }

// A PubAck already in hand wins over an expired deadline, every time. The mutant this pins
// is one blocking select over the PubAck and the timer, which picks between two ready cases
// at random and fails a stored message about half the time.
func TestAwaitPrefersAnArrivedPubAckOverAnExpiredDeadline(t *testing.T) {
	for i := 0; i < 1000; i++ {
		f := &fakeFuture{ok: make(chan *nats.PubAck, 1), err: make(chan error, 1)}
		f.ok <- &nats.PubAck{Stream: "s", Sequence: 1}
		if err := awaitPubAck(f, time.Now().Add(-time.Second), false); err != nil {
			t.Fatalf("iteration %d: a future holding its PubAck reported %v", i, err)
		}
	}
}

// A publish nobody answers does not fail the ones behind it: they are reported after it, in
// order, each on its own outcome.
func TestLaterPublishesAreNotFailedByAnEarlierTimeout(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newOrdered(t, nmgr, 16)
	stub := stubPublishes(t, srv, nmgr)
	out := newOutcomes()

	w.Publish(orderedCtx(), Message{Value: []byte("0")}, out.done(0))
	for i := 1; i < 10; i++ {
		ctx, cancel := context.WithTimeout(orderedCtx(), 500*time.Millisecond)
		defer cancel()
		w.Publish(ctx, Message{Value: []byte(strconv.Itoa(i))}, out.done(i))
	}
	if got := stub.waitSeen(10); got != 10 {
		t.Fatalf("requests: got %d, want 10", got)
	}
	for i := 1; i < 10; i++ {
		stub.answer(i)
	}
	if got := out.waitCount(10, 3*ruledPublishCeiling); got != 10 {
		t.Fatalf("outcomes: got %d, want 10", got)
	}
	w.Close()

	out.mu.Lock()
	defer out.mu.Unlock()
	if fmt.Sprint(out.order) != fmt.Sprint([]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}) {
		t.Fatalf("outcome order: got %v", out.order)
	}
	if !errors.Is(out.errs[0], nats.ErrTimeout) {
		t.Errorf("unanswered publish: got %v, want nats.ErrTimeout", out.errs[0])
	}
	for i := 1; i < 10; i++ {
		if out.errs[i] != nil {
			t.Errorf("publish %d, answered while an earlier one was pending: got %v, want nil", i, out.errs[i])
		}
	}
}

// 🔑 THE BACKOFF GATE. A stream that answers every publish with "no responders" fails each
// one at once. Without a backoff the window frees at CPU speed and the submitter pours its
// whole backlog through it — every source failed, and a delivery spent on each. With one, a
// failing stream is drained no faster than a synchronous writer's retries would drain it,
// plus the window that was already in flight: that window (8) fails together and shares the
// first backoff (500 ms), then one probe goes per backoff (the next waits 1 s, so the one
// after it is not reported until 3.5 s).
func TestAFailingStreamIsNotDrainedFasterThanTheSerialPath(t *testing.T) {
	_, nmgr := orderedManager(t)
	const window = 8
	w := newOrdered(t, nmgr, window)
	deleteStream(t, nmgr)
	out := newOutcomes()

	go func() {
		for i := 0; i < 500; i++ {
			w.Publish(orderedCtx(), Message{Value: []byte(strconv.Itoa(i))}, out.done(i))
		}
	}()
	time.Sleep(2 * time.Second)
	failed := out.count()
	w.Draining()
	if failed > window+3 {
		t.Errorf("publishes failed within 2s against a stream with no responders: got %d, want at most %d",
			failed, window+3)
	}
	if failed == 0 {
		t.Fatal("no publish failed at all within 2s: the stream is not failing, so this proves nothing")
	}
	out.mu.Lock()
	first := out.errs[out.order[0]]
	out.mu.Unlock()
	if !errors.Is(first, nats.ErrNoResponders) {
		t.Fatalf("first outcome: got %v, want nats.ErrNoResponders (the failure this test is about)", first)
	}
	// Draining ends the backoff, so the rest settle promptly.
	if got := out.waitCount(500, 10*time.Second); got != 500 {
		t.Fatalf("outcomes after Draining: got %d, want 500", got)
	}
}

// 🔑 THE RECONNECT GATE. A reconnect fails every publish in flight at the same instant. They
// are ONE failure episode and share one backoff: a publish sent after them, which the broker
// stores at once, is reported within about that backoff — not after a backoff for each of
// the window's failures, which at a window of 128 held its source for minutes, past AckWait.
func TestAReconnectCostsTheWindowOneBackoff(t *testing.T) {
	srv, nmgr := orderedManager(t)
	const window = 16
	w := newOrdered(t, nmgr, window)
	stub := stubPublishes(t, srv, nmgr)
	out := newOutcomes()

	for i := 0; i < window; i++ {
		w.Publish(orderedCtx(), Message{Value: []byte(strconv.Itoa(i))}, out.done(i))
	}
	if got := stub.waitSeen(window); got != window {
		t.Fatalf("requests in flight: got %d, want %d", got, window)
	}
	if err := nmgr.nc.ForceReconnect(); err != nil {
		t.Fatal(err)
	}
	// Blocks for a slot until the first failure's backoff has run.
	go w.Publish(orderedCtx(), Message{Value: []byte("late")}, out.done(window))
	if got := stub.waitSeen(window + 1); got != window+1 {
		t.Fatalf("requests after the reconnect: got %d, want %d", got, window+1)
	}
	stub.answer(window)
	answered := stub.answeredTime(window)
	if got := out.waitCount(window+1, 10*time.Second); got != window+1 {
		t.Fatalf("outcomes: got %d, want %d", got, window+1)
	}
	w.Close()

	out.mu.Lock()
	defer out.mu.Unlock()
	for i := 0; i < window; i++ {
		if out.errs[i] == nil {
			t.Fatalf("publish %d, in flight across the reconnect: reported a success", i)
		}
	}
	if err := out.errs[window]; err != nil {
		t.Fatalf("the publish sent after the reconnect: got %v, want nil (the stub acknowledged it)", err)
	}
	if lag := out.at[window].Sub(answered); lag > time.Second {
		t.Errorf("the publish sent after the reconnect was reported %v after its PubAck, want under 1s: "+
			"the window's failures must share one backoff", lag)
	}
}

// The backoff's floor is the synchronous path's: nats.go retries a failing synchronous
// publish twice, 250 ms apart, so the first backoff is at least 500 ms, and it doubles to a
// 2 s cap. The literals are the ruling, not the constants.
func TestTheBackoffMatchesTheSerialPath(t *testing.T) {
	w := &orderedWriter{draining: make(chan struct{})}
	for i, want := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 2 * time.Second} {
		start := time.Now()
		if n := w.pace(false, true, start); n != 1 {
			t.Fatalf("failure %d freed %d slots, want its own", i, n)
		}
		if w.backoff != want {
			t.Fatalf("backoff after failure %d: got %v, want %v", i, w.backoff, want)
		}
		if waited := time.Since(start); waited < want {
			t.Fatalf("failure %d waited %v, want at least %v", i, waited, want)
		}
	}
}

// Failures of publishes sent before the backoff ended share it: they wait for nothing more
// and keep their slots back, so only the probe goes. A success ends the episode, frees
// every held slot and resets the backoff, so a healthy stream never pays.
func TestAFailureEpisodeHoldsItsSlotsUntilASuccess(t *testing.T) {
	w := &orderedWriter{draining: make(chan struct{})}
	inFlight := time.Now()
	if n := w.pace(false, true, inFlight); n != 1 {
		t.Fatalf("the failure that starts the episode freed %d slots, want 1", n)
	}
	for i := 0; i < 3; i++ {
		start := time.Now()
		if n := w.pace(false, true, inFlight); n != 0 {
			t.Fatalf("a failure already in flight freed %d slots, want 0 (held)", n)
		}
		if waited := time.Since(start); waited > 50*time.Millisecond {
			t.Fatalf("a failure already in flight waited %v, want no wait", waited)
		}
	}
	if w.backoff != 500*time.Millisecond {
		t.Fatalf("backoff after failures that were in flight together: got %v, want 500ms (not escalated)", w.backoff)
	}
	if n := w.pace(false, false, time.Time{}); n != 1 {
		t.Fatalf("a local refusal freed %d slots, want its own", n)
	}
	if n := w.pace(true, false, time.Now()); n != 4 {
		t.Fatalf("the success that ends the episode freed %d slots, want 4 (its own and 3 held)", n)
	}
	if w.backoff != 0 {
		t.Fatalf("backoff after a success: got %v, want 0", w.backoff)
	}
	if n := w.pace(false, true, inFlight); n != 1 || w.backoff != 500*time.Millisecond {
		t.Fatalf("a failure after the success: freed %d, backoff %v; want a new episode (1, 500ms)", n, w.backoff)
	}
}

// Draining releases what an episode held and waits for nothing.
func TestDrainingReleasesAHeldEpisode(t *testing.T) {
	w := &orderedWriter{draining: make(chan struct{})}
	inFlight := time.Now()
	w.pace(false, true, inFlight)
	w.pace(false, true, inFlight)
	w.pace(false, true, inFlight)
	w.Draining()
	start := time.Now()
	if n := w.pace(false, true, time.Now()); n != 3 {
		t.Fatalf("a failure while draining freed %d slots, want 3 (its own and 2 held)", n)
	}
	if waited := time.Since(start); waited > 50*time.Millisecond {
		t.Fatalf("a failure while draining waited %v", waited)
	}
}

// 🔴 NO RETRY OPTIONS. nats.go retries an async no-responders publish on a timer, re-sending
// it BEHIND whatever went meanwhile. The stream disappears and comes back while publishes
// sent a millisecond apart are failing: whatever reaches the stream must still be in
// submission order, and exactly the publishes reported as successes.
func TestAStreamThatComesBackStoresInSubmissionOrder(t *testing.T) {
	_, nmgr := orderedManager(t)
	const window, n = 64, 160
	w := newOrdered(t, nmgr, window)
	deleteStream(t, nmgr)
	out := newOutcomes()

	submitted := make(chan struct{})
	go func() {
		defer close(submitted)
		for i := 0; i < n; i++ {
			w.Publish(orderedCtx(), Message{Value: []byte(strconv.Itoa(i))}, out.done(i))
			if i < window {
				time.Sleep(time.Millisecond)
			}
		}
	}()
	// Between a retrying writer's first retry (250 ms) and its second (500 ms).
	time.Sleep(375 * time.Millisecond)
	if _, err := nmgr.ensureStream(streams.ResolvedEvents); err != nil {
		t.Fatal(err)
	}
	<-submitted
	w.Draining()
	w.Close()

	stream := StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents)
	info, err := nmgr.js.StreamInfo(stream)
	if err != nil {
		t.Fatal(err)
	}
	var stored []int
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq && info.State.Msgs > 0; seq++ {
		raw, err := nmgr.js.GetMsg(stream, seq)
		if err != nil {
			t.Fatalf("stream sequence %d: %v", seq, err)
		}
		v, err := strconv.Atoi(string(raw.Data))
		if err != nil {
			t.Fatal(err)
		}
		stored = append(stored, v)
	}
	var succeeded []int
	for i := 0; i < n; i++ {
		if out.errs[i] == nil {
			succeeded = append(succeeded, i)
		}
	}
	for i := 1; i < len(stored); i++ {
		if stored[i] <= stored[i-1] {
			t.Fatalf("stream order breaks at position %d: %d stored after %d", i, stored[i], stored[i-1])
		}
	}
	if fmt.Sprint(stored) != fmt.Sprint(succeeded) {
		t.Fatalf("stored %v, but the publishes reported as successes were %v", stored, succeeded)
	}
	// Last, so an order break is what a writer that reorders is reported for.
	if len(succeeded) == 0 || len(succeeded) == n {
		t.Fatalf("successes: %d of %d, want some but not all: the stream was not both absent and back", len(succeeded), n)
	}
}

// A slot is freed only after its done has returned, so the window bounds the work done in
// the callbacks too: with a window of one, the next publish is not sent while done runs.
func TestASlotIsFreedOnlyAfterItsDoneReturns(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newOrdered(t, nmgr, 1)
	stub := stubPublishes(t, srv, nmgr)
	inDone, release := make(chan struct{}), make(chan struct{})
	w.Publish(orderedCtx(), Message{Value: []byte("0")}, func(error) {
		close(inDone)
		<-release
	})
	stub.waitSeen(1)
	go w.Publish(orderedCtx(), Message{Value: []byte("1")}, func(error) {})
	stub.answer(0)
	<-inDone
	time.Sleep(200 * time.Millisecond)
	if got := stub.seen(); got != 1 {
		t.Fatalf("requests sent while the first publish's done was running: got %d, want 1", got)
	}
	close(release)
	if got := stub.waitSeen(2); got != 2 {
		t.Fatalf("requests once done returned: got %d, want 2", got)
	}
	stub.answer(1)
	time.Sleep(50 * time.Millisecond)
	w.Close()
}

// A writer that is built and never used owns no goroutine, and closing it returns at once.
func TestAnUnusedOrderedWriterStartsNothing(t *testing.T) {
	_, nmgr := orderedManager(t)
	w := newOrdered(t, nmgr, 4).(*orderedWriter)
	if w.started.Load() {
		t.Fatal("an unused writer started its settle goroutine")
	}
	w.Close()
	if w.started.Load() {
		t.Fatal("Close started the settle goroutine")
	}
}

// Close returns only after the settle goroutine has run every done and exited.
func TestCloseEndsTheSettleGoroutine(t *testing.T) {
	_, nmgr := orderedManager(t)
	w := newOrdered(t, nmgr, 4).(*orderedWriter)
	out := newOutcomes()
	w.Publish(orderedCtx(), Message{Value: []byte("x")}, out.done(0))
	w.Close()
	select {
	case <-w.settled:
	default:
		t.Fatal("Close returned while the settle goroutine was still running")
	}
	if got := out.count(); got != 1 {
		t.Fatalf("outcomes reported by Close: got %d, want 1", got)
	}
}

// Misuse is refused loudly: at construction where it can be, by panic where it cannot.
func TestOrderedWriterRefusesMisuse(t *testing.T) {
	_, nmgr := orderedManager(t)
	for _, tc := range []struct {
		suffix string
		window int
		want   string
	}{
		{streams.ResolvedEvents, 0, "outside [1, 1024]"},
		{streams.ResolvedEvents, 1025, "outside [1, 1024]"},
		{streams.DeviceCommands, 8, "per-device subject"},
		{streams.DeviceEventsCapture, 8, "device-events capture"},
	} {
		_, err := nmgr.NewOrderedWriter(tc.suffix, tc.window)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("NewOrderedWriter(%q, %d): got %v, want an error containing %q", tc.suffix, tc.window, err, tc.want)
		}
	}

	mustPanic := func(what, want string, f func()) {
		t.Helper()
		defer func() {
			r := recover()
			if r == nil || !strings.Contains(fmt.Sprint(r), want) {
				t.Errorf("%s: got panic %v, want one containing %q", what, r, want)
			}
		}()
		f()
	}
	w := newOrdered(t, nmgr, 4)
	mustPanic("Fail(nil)", "non-nil error", func() { w.Fail(nil, func(error) {}) })
	mustPanic("Publish with no done", "done callback", func() { w.Publish(orderedCtx(), Message{}, nil) })
	w.Close()
	mustPanic("Publish after Close", "after Close", func() { w.Publish(orderedCtx(), Message{}, func(error) {}) })
}

// Two submitters have no order between them, so the second is refused while the first is
// inside a call.
func TestOrderedWriterRefusesAConcurrentSubmitter(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newOrdered(t, nmgr, 1)
	stub := stubPublishes(t, srv, nmgr)
	w.Publish(orderedCtx(), Message{Value: []byte("0")}, func(error) {})
	stub.waitSeen(1)
	// The window is full, so this Publish blocks inside the call, holding the writer.
	go w.Publish(orderedCtx(), Message{Value: []byte("1")}, func(error) {})
	time.Sleep(100 * time.Millisecond)
	func() {
		defer func() {
			if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "concurrently") {
				t.Errorf("a second submitter: got panic %v, want the concurrent-use refusal", r)
			}
		}()
		w.Fail(errors.New("x"), func(error) {})
	}()
	stub.answer(0)
	stub.waitSeen(2)
	stub.answer(1)
	time.Sleep(100 * time.Millisecond)
	w.Close()
}

// A publish with no tenant sends nothing and reports the refusal, in order.
func TestOrderedWriterRefusesAPublishWithNoTenant(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newOrdered(t, nmgr, 4)
	stub := stubPublishes(t, srv, nmgr)
	out := newOutcomes()
	w.Publish(context.Background(), Message{Value: []byte("x")}, out.done(0))
	w.Close()
	if !errors.Is(out.errs[0], core.ErrNoTenant) {
		t.Errorf("outcome: got %v, want core.ErrNoTenant", out.errs[0])
	}
	if got := stub.waitSeen(0); got != 0 {
		t.Errorf("requests sent for a publish with no tenant: got %d, want 0", got)
	}
}

// Fail's outcome is reported in order with the publishes around it.
func TestFailIsReportedInOrder(t *testing.T) {
	srv, nmgr := orderedManager(t)
	w := newOrdered(t, nmgr, 8)
	stub := stubPublishes(t, srv, nmgr)
	out := newOutcomes()
	boom := errors.New("could not encode")
	w.Publish(orderedCtx(), Message{Value: []byte("0")}, out.done(0))
	w.Fail(boom, out.done(1))
	w.Publish(orderedCtx(), Message{Value: []byte("2")}, out.done(2))
	stub.waitSeen(2)
	if got := out.count(); got != 0 {
		t.Fatalf("outcomes reported while the first publish was held: got %d, want 0", got)
	}
	stub.answer(1)
	stub.answer(0)
	w.Close()
	if fmt.Sprint(out.order) != "[0 1 2]" {
		t.Fatalf("outcome order: got %v, want [0 1 2]", out.order)
	}
	if out.errs[0] != nil || !errors.Is(out.errs[1], boom) || out.errs[2] != nil {
		t.Fatalf("outcomes: got %v", out.errs)
	}
}

// The ordered writer carries Message.DedupID the way the synchronous one does, so a second
// publish with an id the stream has seen is acknowledged as a success and stored once.
func TestOrderedWriterHonoursTheDedupID(t *testing.T) {
	_, nmgr := orderedManager(t)
	w := newOrdered(t, nmgr, 8)
	out := newOutcomes()
	w.Publish(orderedCtx(), Message{Value: []byte("a"), DedupID: "acme:1"}, out.done(0))
	w.Publish(orderedCtx(), Message{Value: []byte("a"), DedupID: "acme:1"}, out.done(1))
	w.Publish(orderedCtx(), Message{Value: []byte("b"), DedupID: "acme:2"}, out.done(2))
	w.Close()
	for i := 0; i < 3; i++ {
		if out.errs[i] != nil {
			t.Fatalf("outcome %d: %v, want nil (a duplicate is acknowledged as a success)", i, out.errs[i])
		}
	}
	info, err := nmgr.js.StreamInfo(StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents))
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 2 {
		t.Errorf("stored: got %d, want 2", info.State.Msgs)
	}
}
