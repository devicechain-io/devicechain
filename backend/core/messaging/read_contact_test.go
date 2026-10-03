// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
)

// 🔴 WHAT THIS FILE IS FOR. A read loop's pacer ends the process when its reads have
// failed continuously for two minutes. On a quiet stream the loop went minutes without a
// message, and only a message ended a run, so two broker disturbances far apart — a server
// restart, then a consumer leader moving a quarter of an hour later — were timed as one
// run, and the second ended the process. Several services restarted together that way after
// a broker quorum loss that lasted seconds.
//
// These tests drive the REAL reader against a REAL embedded broker through the REAL loop
// (RunConsumer). The pacer's clock is the test's, so "three minutes apart" costs nothing,
// while the broker's own timings (fetches, probes, reconnects) run in real time — which is
// what makes the evidence real: an answer here is a round trip a server actually made.

// startBrokerKeepingStore starts an embedded broker on port (-1 for an ephemeral one)
// over storeDir, so a broker restarted on the same port and store comes back with the
// stream and the durable it had. startBrokerOnPort makes a fresh store per call, which
// would turn a restart into a deleted consumer — a different path (the reader re-binds).
// It retries for the reason startBrokerOnPort gives: a port just released is not at once
// re-bindable.
func startBrokerKeepingStore(t *testing.T, port int, storeDir string) *natsserver.Server {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		srv, err := natsserver.NewServer(&natsserver.Options{
			Host:      "127.0.0.1",
			Port:      port,
			JetStream: true,
			StoreDir:  storeDir,
		})
		if err == nil {
			go srv.Start()
			if srv.ReadyForConnections(5 * time.Second) {
				return srv
			}
			srv.Shutdown()
			err = errors.New("not ready for connections")
		}
		if port == -1 || time.Now().After(deadline) {
			t.Fatalf("embedded nats server on port %d: %v", port, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// errorTap is a reader whose HandleResponse also hands each error to the test, so a test
// can see that a disturbance actually produced a read error. Without that a test of "the
// loop survived" passes just as well when the disturbance never reached the loop at all.
type errorTap struct {
	MessageReader
	errs chan error
}

func (e *errorTap) HandleResponse(err error) {
	e.MessageReader.HandleResponse(err)
	select {
	case e.errs <- err:
	default:
	}
}

// idleLoop is RunConsumer over a real reader, paced on a clock the test advances by hand.
type idleLoop struct {
	offset  atomic.Int64 // nanoseconds the test has moved the pacer's clock on
	handled atomic.Int32
	done    chan struct{}
	tap     *errorTap
}

func startIdleLoop(t *testing.T, reader MessageReader) *idleLoop {
	t.Helper()
	l := &idleLoop{done: make(chan struct{}), tap: &errorTap{MessageReader: reader, errs: make(chan error, 64)}}
	base := time.Now()
	pacer := core.NewReadPacer(nil, "idle").UseClock(
		func() time.Time { return base.Add(time.Duration(l.offset.Load())) },
		// A short REAL pause, so a retry is not a hot spin, but never the backoff itself:
		// the pacer's sense of elapsed time is the test clock alone.
		func(ctx context.Context, d time.Duration) bool {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(min(d, 50*time.Millisecond)):
				return true
			}
		})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(l.done)
		RunConsumer(ctx, l.tap, pacer, func(msg Message) bool {
			_ = msg.Ack()
			l.handled.Add(1)
			return true
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-l.done:
		case <-time.After(30 * time.Second):
			t.Error("the read loop did not stop within 30s of its context being cancelled")
		}
	})
	return l
}

func (l *idleLoop) advance(d time.Duration) { l.offset.Add(int64(d)) }

func (l *idleLoop) ended() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// awaitBrokerAnswer waits until the reader holds evidence that the broker answered it
// since its last error (an idle reader's probe runs every few seconds). Polled rather than
// slept, so a broker slow to recover its store makes the test slower, not wrong.
func awaitBrokerAnswer(t *testing.T, reader MessageReader) {
	t.Helper()
	r := reader.(*natsReader)
	waitFor(t, "the idle reader's probe to be answered", r.answered.Load)
}

// settle waits until the idle reader is in its steady state against a live broker, so a
// disturbance that follows is measured from a known point: every error the loop has been
// handed so far is discarded, and the reader's evidence is cleared and then waited for
// again, which only a probe (or a delivery) made from now on can supply. Without it a
// disturbance can be credited with an earlier one's error, or with evidence gathered
// before a restart the test has already caused.
func (l *idleLoop) settle(t *testing.T, reader MessageReader) {
	t.Helper()
	for drained := false; !drained; {
		select {
		case <-l.tap.errs:
		default:
			drained = true
		}
	}
	r := reader.(*natsReader)
	r.answered.Store(false)
	waitFor(t, "the idle reader's probe to be answered", r.answered.Load)
}

// takeDown stops the broker and returns, with it still down, once the loop has been handed
// a read error for the stop.
//
// A stop does not always reach the loop as an error. A fetch sent while the server is
// stopping JetStream, before it closes its connections, is answered "no responders", and
// the reader treats that as a consumer gone: it re-binds inside its read, retrying until the
// broker is back, and hands the loop nothing. That run is correct and tests nothing, so the
// broker is brought back, the reader allowed to re-attach, and the stop repeated.
func (l *idleLoop) takeDown(t *testing.T, srv *natsserver.Server, storeDir string, nmgr *NatsManager,
	reader MessageReader, what string) (port int) {
	t.Helper()
	port = srv.Addr().(*net.TCPAddr).Port
	for attempt := 1; attempt <= 4; attempt++ {
		l.settle(t, reader)
		srv.Shutdown()
		srv.WaitForShutdown()
		select {
		case err := <-l.tap.errs:
			t.Logf("%s produced the read error %q (attempt %d)", what, err, attempt)
			// One stop can hand the loop more than one error at the same moment: the
			// server's own "Server Shutdown" to the pull it was holding, then the fetch
			// after it cut off by the disconnect. Let them all land before the caller moves
			// the pacer's clock, or the test itself splits one moment into two minutes.
			waitFor(t, "the client to see the broker gone", func() bool { return !nmgr.nc.IsConnected() })
			time.Sleep(time.Second)
			return port
		case <-time.After(8 * time.Second):
		}
		t.Logf("%s reached the reader as a re-bind rather than a read error (attempt %d); again", what, attempt)
		srv = startBrokerKeepingStore(t, port, storeDir)
		waitFor(t, "the client to reconnect", nmgr.nc.IsConnected)
	}
	t.Fatalf("%s never reached the loop as a read error in 4 stops, so the loop was never tested against it", what)
	return port
}

// singleBroker is a broker over a kept store and a manager dialled through the production
// ExecuteInitialize, so the connection carries the reconnect behaviour a service has.
func singleBroker(t *testing.T) (srv *natsserver.Server, storeDir string, nmgr *NatsManager) {
	t.Helper()
	storeDir = dctest.JetStreamStoreDir(t)
	srv = startBrokerKeepingStore(t, -1, storeDir)
	nmgr = managerFor(t, srv)
	if err := nmgr.ExecuteInitialize(t.Context()); err != nil {
		t.Fatalf("connecting to the embedded broker: %v", err)
	}
	t.Cleanup(func() { nmgr.nc.Close() })
	return srv, storeDir, nmgr
}

func (l *idleLoop) assertStillConsuming(t *testing.T, nmgr *NatsManager, why string) {
	t.Helper()
	subject := ScopedSubject(nmgr.Microservice.InstanceId, "acme", streams.InboundEvents)
	deadline := time.Now().Add(20 * time.Second)
	for l.handled.Load() == 0 && !l.ended() && time.Now().Before(deadline) {
		// Re-published until it is read: the publish itself may land while the stream's
		// store is still recovering from the restart.
		_, _ = nmgr.js.Publish(subject, []byte(`{"after":true}`), nats.AckWait(2*time.Second))
		time.Sleep(500 * time.Millisecond)
	}
	if l.ended() || l.handled.Load() == 0 {
		t.Fatalf("loop ended=%v, messages handled=%d; want a running loop that handled the message "+
			"published after the disturbances: %s", l.ended(), l.handled.Load(), why)
	}
}

// 🔴 THE DEFECT, END TO END: two broker restarts, three minutes apart on the pacer's clock,
// with an idle stream between them. Before the fix the second restart's read error landed
// three minutes into a run that began at the first, and the loop gave up.
func TestAnIdleConsumerSurvivesTwoBrokerRestartsFarApart(t *testing.T) {
	srv, storeDir, nmgr := singleBroker(t)
	reader, err := nmgr.NewReader(streams.InboundEvents)
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	l := startIdleLoop(t, reader)

	port := l.takeDown(t, srv, storeDir, nmgr, reader, "the first broker restart")
	srv = startBrokerKeepingStore(t, port, storeDir)
	waitFor(t, "the client to reconnect", nmgr.nc.IsConnected)

	l.advance(3 * time.Minute)
	// takeDown first waits for the idle reader's probe to be answered: the evidence that
	// makes the second restart a failure of its own.
	l.takeDown(t, srv, storeDir, nmgr, reader, "the second broker restart")
	srv = startBrokerKeepingStore(t, port, storeDir)
	defer srv.Shutdown()
	waitFor(t, "the client to reconnect", nmgr.nc.IsConnected)

	l.assertStillConsuming(t, nmgr, "the broker answered the idle reader between the two "+
		"restarts, so they are two failures, not one three-minute run")
}

// 🔴 AN OUTAGE LONGER THAN THE BUDGET MUST NOT END THE LOOP AT THE MOMENT IT ENDS. A fetch
// is interrupted by every change of connection status, so the reconnect itself hands the
// loop one more error. Counted as one more failure, it lands past the budget and the loop
// gives up the instant the broker is back — every idle loop in every service at once, for a
// restart that has nothing left to re-dial.
func TestABrokerOutageLongerThanTheBudgetDoesNotEndTheLoopWhenTheBrokerReturns(t *testing.T) {
	srv, storeDir, nmgr := singleBroker(t)
	reader, err := nmgr.NewReader(streams.InboundEvents)
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	l := startIdleLoop(t, reader)

	port := l.takeDown(t, srv, storeDir, nmgr, reader, "the broker going down")
	l.advance(3 * time.Minute) // the outage, as the pacer sees it
	srv = startBrokerKeepingStore(t, port, storeDir)
	defer srv.Shutdown()
	waitFor(t, "the client to reconnect", nmgr.nc.IsConnected)

	l.assertStillConsuming(t, nmgr, "the reconnect is the broker answering, so the outage's "+
		"run ends with it rather than ending the loop")
}

// The evidence is handed out once. It describes the interval BETWEEN two errors, so the
// error that reports it must also end that interval; read without clearing, one answer
// would excuse every error after it.
func TestAReaderHandsEachAnswerToOneErrorOnly(t *testing.T) {
	r := &natsReader{}
	answered := func(err error) bool {
		var c core.BrokerContact
		if !errors.As(err, &c) {
			t.Fatalf("%v carries no broker evidence at all", err)
		}
		return c.AnsweredSincePreviousError()
	}
	boom := errors.New("boom")
	if answered(r.readError(boom)) {
		t.Fatal("a reader the broker never answered reported an answer")
	}
	r.answered.Store(true)
	if !answered(r.readError(boom)) {
		t.Fatal("the broker answered and the next error did not say so")
	}
	if answered(r.readError(boom)) {
		t.Fatal("one answer was reported on two errors: it must end the interval it describes")
	}
	if err := r.readError(boom); !errors.Is(err, boom) || err.Error() != boom.Error() {
		t.Fatalf("the wrapped error reads %q and errors.Is=%v; want the reader's error unchanged to every "+
			"caller", err, errors.Is(err, boom))
	}
}

// 🔴 AN EMPTY FETCH IS NOT AN ANSWER. nats.go reports one from its own client-side deadline
// while disconnected, so an idle reader whose broker is gone keeps "succeeding" at empty
// fetches. Counting them would excuse a dead broker forever, and the loop would never give
// up. The read is held long enough for several empty fetches and a probe, and it is
// asserted AFTER the first error has been handed out — that error takes the evidence the
// bind left, so an assertion before it would pass for the wrong reason.
func TestAnIdleReaderWithItsBrokerDownGathersNoAnswer(t *testing.T) {
	srv, _, nmgr := singleBroker(t)
	reader, err := nmgr.NewReader(streams.InboundEvents)
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	r := reader.(*natsReader)

	srv.Shutdown()
	srv.WaitForShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := r.ReadMessage(ctx); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("ReadMessage returned %v with the broker going down, want the fetch's disconnect error", err)
	}
	if r.answered.Load() {
		t.Fatal("the evidence survived the error that reported it")
	}

	hold := time.Duration(livenessProbeAfterTimeouts+2)*fetchTimeout + 6*time.Second
	ctx2, cancel2 := context.WithTimeout(context.Background(), hold)
	defer cancel2()
	for {
		_, err := r.ReadMessage(ctx2)
		if errors.Is(err, io.EOF) {
			break
		}
		if err == nil {
			t.Fatal("a message was read with the broker down")
		}
	}
	if r.answered.Load() {
		t.Fatalf("over %s of empty fetches and a probe with the broker down, the reader recorded a broker "+
			"answer: a dead broker would then never end the loop", hold)
	}
}

// And the positive half: on a live, idle stream the probe IS an answer, so a reader that
// receives nothing still gathers evidence the broker is there.
func TestAnIdleReaderWithItsBrokerUpGathersAnAnswerFromItsProbe(t *testing.T) {
	_, _, nmgr := singleBroker(t)
	reader, err := nmgr.NewReader(streams.InboundEvents)
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	r := reader.(*natsReader)
	_ = r.readError(errors.New("clear the bind's evidence"))

	hold := time.Duration(livenessProbeAfterTimeouts+2) * fetchTimeout
	ctx, cancel := context.WithTimeout(context.Background(), hold)
	defer cancel()
	if _, err := r.ReadMessage(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("an idle read returned %v, want io.EOF at the end of its context", err)
	}
	if !r.answered.Load() {
		t.Fatalf("over %s on an idle stream with the broker up, the reader recorded no answer: its "+
			"liveness probe is the only evidence a quiet loop has", hold)
	}
}

// 🔴 THE PATH THE OUTAGE ACTUALLY TOOK. Most give-ups in that incident ended with
// `nats: Leadership Changed`: a consumer leader moving on a connection that stayed up, so
// no reconnect vouches for the broker. Only the idle reader's probe can. Two leader moves
// three minutes apart on a replicated consumer must be two failures.
func TestAnIdleConsumerSurvivesTwoLeaderMovesFarApart(t *testing.T) {
	nmgr, cleanup := newTestCluster(t)
	defer cleanup()
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3

	var reader MessageReader
	retryWhileGroupSettles(t, "create the replicated reader", func() error {
		var err error
		reader, err = nmgr.NewReader(streams.InboundEvents)
		return err
	})
	r := reader.(*natsReader)
	l := startIdleLoop(t, reader)

	// stepDown moves the consumer's leader until the move reaches the loop as a read error.
	// Not every move does: the new leader reports `Leadership Changed` only to the pull
	// requests the old one had replicated to it, and a 1s fetch can fall between them. So it
	// repeats, and a run with no error at all fails rather than passing on nothing.
	stepDown := func(what string) {
		t.Helper()
		subj := fmt.Sprintf("$JS.API.CONSUMER.LEADER.STEPDOWN.%s.%s", r.stream, r.durable)
		for attempt := 1; attempt <= 6; attempt++ {
			retryWhileGroupSettles(t, what, func() error {
				msg, err := nmgr.nc.Request(subj, nil, 5*time.Second)
				if err != nil {
					return err
				}
				var resp struct {
					Success bool `json:"success"`
				}
				if err := json.Unmarshal(msg.Data, &resp); err != nil || !resp.Success {
					return fmt.Errorf("the stepdown was refused: %s", msg.Data)
				}
				return nil
			})
			select {
			case err := <-l.tap.errs:
				if !errors.Is(err, nats.ErrConsumerLeadershipChanged) {
					t.Fatalf("%s produced %q, want the leadership-change error this test is about", what, err)
				}
				t.Logf("%s produced the read error %q (attempt %d)", what, err, attempt)
				time.Sleep(time.Second) // let any second error of the same move land first; see takeDown
				return
			case <-time.After(8 * time.Second):
			}
		}
		t.Fatalf("%s never reached the loop as a read error in 6 moves, so the loop was never tested against it", what)
	}

	awaitBrokerAnswer(t, reader)
	stepDown("the first consumer leader move")
	l.advance(3 * time.Minute)
	awaitBrokerAnswer(t, reader)
	stepDown("the second consumer leader move")

	time.Sleep(time.Second) // let a give-up, if there is one, land
	if l.ended() {
		t.Fatal("the loop gave up on the second of two consumer leader moves three minutes apart, " +
			"although the probe found the consumer's leader between them")
	}
}

// A (re)bind is a broker answer: AddConsumer and the subscribe both reached a server that
// answered. It is the one piece of evidence a reader parked between terms gets, because a
// new term's BindTerm goes through the same bind.
func TestABindIsABrokerAnswer(t *testing.T) {
	_, _, nmgr := singleBroker(t)
	reader, err := nmgr.NewReader(streams.InboundEvents)
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	r := reader.(*natsReader)
	_ = r.readError(errors.New("clear the first bind's evidence"))
	if err := r.bind(); err != nil {
		t.Fatalf("re-bind: %v", err)
	}
	if !r.answered.Load() {
		t.Fatal("a successful re-bind left no evidence that the broker answered")
	}
}

// A reader parked behind a refusing downstream stream fetches and probes nothing, for as
// long as the backlog takes to drain. The sampler's measurements are what show the broker
// is answering meanwhile — but only a FRESH one taken after the reader's last error. A
// stale gate is stale precisely because the sampler is not being answered.
func TestABackpressureParkCountsOnlyAFreshMeasurementAsAnAnswer(t *testing.T) {
	r := &natsReader{}
	_ = r.readError(errors.New("the error before the park"))
	erred := r.lastErrorAt

	for _, c := range []struct {
		name string
		bp   *BackpressureError
		want bool
	}{
		{"a stale gate", &BackpressureError{Stale: true}, false},
		{"a measurement older than the error", &BackpressureError{sampledAt: erred.Add(-time.Second)}, false},
		{"a fresh ratio refusal", &BackpressureError{sampledAt: erred.Add(time.Second)}, true},
		{"a fresh runway refusal", &BackpressureError{Runway: true, sampledAt: erred.Add(time.Second)}, true},
	} {
		r.answered.Store(false)
		r.noteBackpressureAnswer(c.bp)
		if got := r.answered.Load(); got != c.want {
			t.Errorf("%s: recorded a broker answer = %v, want %v", c.name, got, c.want)
		}
	}
}

// The measurement time has to reach the reader through Backpressure itself, or every
// fresh refusal reads as older than any error and the park gathers nothing.
func TestARefusingGateReportsWhenItWasMeasured(t *testing.T) {
	clk := newTestClock()
	nmgr, gs := gatedManager(t, clk)
	measured := clk.now().Add(-time.Second)

	for _, c := range []struct {
		name  string
		close func()
	}{
		{"the ratio rule", func() { gs.closed, gs.runwayClosed = true, false }},
		{"the runway rule", func() { gs.closed, gs.runwayClosed = false, true }},
	} {
		setGate(nmgr, gs, func() { gs.sampledAt = measured; c.close() })
		var be *BackpressureError
		if err := nmgr.Backpressure(streams.InboundEvents); !errors.As(err, &be) {
			t.Fatalf("%s: the closed gate reported %v, want a refusal", c.name, err)
		}
		if !be.sampledAt.Equal(measured) {
			t.Errorf("%s: the refusal says it was measured at %v, want %v", c.name, be.sampledAt, measured)
		}
	}
}
