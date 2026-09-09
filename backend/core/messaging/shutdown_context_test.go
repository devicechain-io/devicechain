// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// managerOnServer builds a NatsManager against a JetStream server the caller can
// still reach — the server is returned so a test can KILL it, which is the condition
// every bound in this file is measured under.
//
// It is separate from newTestManager because that one hands back only a cleanup
// closure, and a broker that goes away while the client keeps trying is exactly the
// state these tests need to create.
func managerOnServer(t *testing.T) (*NatsManager, *natsserver.Server) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new embedded nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded nats server not ready")
	}
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "shutdownctx"}
	nmgr := &NatsManager{Microservice: ms, nc: nc, js: js, metrics: newStreamMetrics(ms)}
	return nmgr, srv
}

// sampleSuffixes is more than one on purpose: the cost this bounds is per-stream, so
// a single name could not tell a bound that aborts the request in flight apart from
// one that merely stops before the NEXT name.
var sampleSuffixes = []string{"inbound-events", "resolved-events", "failed-events"}

// A SAMPLE MUST ABANDON THE REQUEST IT IS INSIDE, not just the ones after it.
//
// This is the bound that decides whether a terminating pod unsubscribes its readers.
// The sampler join runs BEFORE every reader unsubscribe and before the drain, and a
// pass over the streams and buckets a service tracks is one JetStream request each.
// Against a broker that has stopped answering, each of those runs to the client's
// default request timeout — measured at 5s per name here — so a pass costs
// 5s x names, all of it inside the join that shutdown waits on.
//
// The context threaded into StreamInfo is what changes that, and the assertion is the
// ELAPSED time rather than the returned values: a sample that gives up on the same
// schedule while still waiting out each request would look identical from the gauges.
func TestSamplingAbortsInFlightRequestsWhenItsContextExpires(t *testing.T) {
	nmgr, srv := managerOnServer(t)
	var names []string
	for _, suffix := range sampleSuffixes {
		name, err := nmgr.ensureStream(suffix)
		if err != nil {
			t.Fatalf("ensureStream(%q): %v", suffix, err)
		}
		names = append(names, name)
	}

	// The broker goes away with the client still attached and still retrying, which is
	// what a rolling broker restart looks like from a pod that is also terminating.
	srv.Shutdown()
	srv.WaitForShutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		nmgr.metrics.sample(ctx, nmgr.js, names, nil, 1, false)
		done <- time.Since(start)
	}()

	// 🔴 Bounded, and the message names the absence. Without the fix this takes
	// ~5s per name; with several names an unbounded wait here would simply hang the
	// suite, which reports nothing at all.
	var elapsed time.Duration
	select {
	case elapsed = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("sample never returned against a dead broker: the context is not reaching " +
			"the StreamInfo calls, so a shutdown blocks on this for as long as the broker stays silent")
	}
	if elapsed > 2*time.Second {
		t.Errorf("sample took %s against a 200ms deadline and %d streams. It is waiting out each "+
			"request rather than cancelling the one in flight, which is the whole cost this bounds",
			elapsed, len(names))
	}
}

// 🔴 THE COUNTERWEIGHT. Every assertion above is satisfied by a sample that does
// nothing at all — returning immediately is the fastest possible way to finish. This
// drives the same function against a broker that IS answering, on a context that is
// not cancelled, and requires the full set of gauges to be written.
func TestSamplingOnALiveContextStillRecordsEveryStream(t *testing.T) {
	nmgr, _ := managerOnServer(t)
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 1
	var names []string
	for _, suffix := range sampleSuffixes {
		name, err := nmgr.ensureStream(suffix)
		if err != nil {
			t.Fatalf("ensureStream(%q): %v", suffix, err)
		}
		names = append(names, name)
	}

	nmgr.metrics.sample(context.Background(), nmgr.js, names, nil, 1, false)

	for _, name := range names {
		if got := testutil.ToFloat64(nmgr.metrics.replicasActual.WithLabelValues(name)); got != 1 {
			t.Errorf("replicas_actual for %s = %v, want 1: an uncancelled sample must visit every "+
				"stream, not stop early", name, got)
		}
		if got := testutil.ToFloat64(nmgr.metrics.limitBytes.WithLabelValues(name)); got == 0 {
			t.Errorf("limit_bytes for %s was never written, so the fill gauges this sampler "+
				"exists for are absent", name)
		}
	}
}

// A STOP MUST NOT WAIT ON THE SAMPLER FOREVER.
//
// The sampler is joined before any reader is unsubscribed, so whatever the join costs
// is taken directly out of the pod's remaining grace period. The join now runs against
// the caller's context; this drives the case the select exists for by presenting a
// sampler that never finishes, which is what the WaitGroup would look like if the
// goroutine were wedged inside a request that outlives the whole budget.
func TestStopDoesNotWaitOnASamplerThatWillNotFinish(t *testing.T) {
	nmgr, _ := managerOnServer(t)

	// A sampler that is counted but never completes. Released at the end so the
	// harness goroutine watching the WaitGroup unwinds with the test.
	nmgr.samplerWg.Add(1)
	defer nmgr.samplerWg.Done()
	_, cancel := context.WithCancel(context.Background())
	nmgr.samplerCancel = cancel

	ctx, cancelStop := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelStop()

	done := make(chan error, 1)
	go func() {
		start := time.Now()
		err := nmgr.ExecuteStop(ctx)
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Logf("ExecuteStop took %s", elapsed)
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ExecuteStop returned %v; giving up on the sampler is not an error, the "+
				"readers still have to be unsubscribed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ExecuteStop never returned with a sampler that will not finish: the join is " +
			"unbounded, so no reader is unsubscribed and no connection is drained before SIGKILL")
	}
	if nmgr.samplerCancel != nil {
		t.Error("samplerCancel was not cleared, so a later start would refuse to launch a sampler")
	}
}

// 🔴 THE COUNTERWEIGHT. Giving up on the sampler is the exceptional path; the ordinary
// one must still JOIN it, because the ordering exists so that nothing is mid-StreamInfo
// when the connection closes. A stop that always skipped the join would satisfy the
// test above perfectly.
func TestStopJoinsALiveSamplerWhenNothingCancels(t *testing.T) {
	nmgr, _ := managerOnServer(t)
	nmgr.oncreate = func(*NatsManager) error { return nil }
	if _, err := nmgr.ensureStream("inbound-events"); err != nil {
		t.Fatalf("ensureStream: %v", err)
	}
	if err := nmgr.ExecuteStart(context.Background()); err != nil {
		t.Fatalf("ExecuteStart: %v", err)
	}
	if nmgr.samplerCancel == nil {
		t.Fatal("no sampler was started, so this asserts nothing about joining one")
	}

	done := make(chan struct{})
	go func() {
		_ = nmgr.ExecuteStop(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("ExecuteStop did not return against a live broker: the sampler was told to stop " +
			"but never did, or was never told")
	}

	// The sampler has to be GONE, not merely unwaited-for. A WaitGroup that is already
	// at zero returns from Wait immediately; one whose goroutine is still running does
	// not, which is what makes this distinguish a join from a skipped join.
	joined := make(chan struct{})
	go func() {
		nmgr.samplerWg.Wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("the sampler goroutine was still running after ExecuteStop returned, so the stop " +
			"did not join it — a sample can now be in flight while the connection closes")
	}
}

// The sampler is a long-running loop started by a lifecycle transition, so cancelling
// the ROOT context has to end it — nothing else in the shutdown sequence has run at
// the point that cancellation happens, and a loop that survives it is one that keeps
// talking to a broker the process is walking away from.
//
// This is the property that makes ExecuteStart derive the sampler's context from the
// one it is handed rather than from context.Background(). Deriving it from Background
// leaves every assertion in TestStopJoinsALiveSamplerWhenNothingCancels satisfied,
// because that one cancels through ExecuteStop instead.
func TestCancellingTheStartContextEndsTheSampler(t *testing.T) {
	nmgr, _ := managerOnServer(t)
	nmgr.oncreate = func(*NatsManager) error { return nil }
	if _, err := nmgr.ensureStream("inbound-events"); err != nil {
		t.Fatalf("ensureStream: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := nmgr.ExecuteStart(ctx); err != nil {
		t.Fatalf("ExecuteStart: %v", err)
	}
	if nmgr.samplerCancel == nil {
		t.Fatal("no sampler was started, so this asserts nothing")
	}

	// Only the root context is cancelled — no stop, no lifecycle transition, exactly
	// the ordering shutDown uses.
	cancel()

	stopped := make(chan struct{})
	go func() {
		nmgr.samplerWg.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(20 * time.Second):
		nmgr.samplerCancel() // let the goroutine unwind with the test
		t.Fatal("the sampler outlived the cancellation of the context it was started on, so " +
			"it keeps polling the broker for as long as teardown takes")
	}
}

// Initialization's connection wait must end when the caller stops wanting the service.
//
// The wait is a deliberate 30s against a broker that may still be starting, and that
// is right — but it used to be 30s no matter what, because the context the transition
// was handed stopped at the signature. What observes the cancellation is the select
// the poll now sleeps in, plus the check before the first look.
func TestWaitForConnectedEndsWhenTheContextIsCancelled(t *testing.T) {
	// RetryOnFailedConnect against a port nothing is listening on: a non-nil conn that
	// never connects, which is what makes the wait actually wait.
	nc, err := nats.Connect("nats://127.0.0.1:1",
		nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1))
	if err != nil {
		t.Fatalf("RetryOnFailedConnect is supposed to return a conn and no error: %v", err)
	}
	defer nc.Close()
	if nc.IsConnected() {
		t.Fatal("the fixture is wrong: this conn must NOT be connected")
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	type result struct {
		err     error
		elapsed time.Duration
	}
	got := make(chan result, 1)
	go func() {
		start := time.Now()
		err := waitForConnected(ctx, nc, 30*time.Second)
		got <- result{err, time.Since(start)}
	}()

	select {
	case r := <-got:
		if r.err == nil {
			t.Fatal("a cancelled wait must not report a connection that was never established")
		}
		if r.elapsed > 5*time.Second {
			t.Errorf("the wait took %s after a cancellation at 150ms; it is running out its own "+
				"timeout instead of observing the context", r.elapsed)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("waitForConnected never returned after its context was cancelled, so a service " +
			"asked to stop during initialization sits here for the full connect wait")
	}
	// The counterweight for this one already exists and must keep passing:
	// TestWaitForConnectedReturnsOnAConnectedConn (an uncancelled wait still succeeds
	// immediately) and TestWaitForConnectedTimesOutOnAStillDiallingConn (an uncancelled
	// wait still runs to its own deadline rather than returning early).
}
