// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-lwm2m-ingest/config"
	"github.com/devicechain-io/dc-lwm2m-ingest/downlink"
	"github.com/devicechain-io/dc-lwm2m-ingest/registry"
	"github.com/devicechain-io/dc-lwm2m-ingest/server"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// recordingLease is a fake leaseTerm that makes what a leadership term did with its lease
// observable, in ORDER. Its KeepAlive returns once the term context is cancelled, exactly as the
// real renewer does on eviction, so evict() can join it without the test having to unblock
// anything.
type recordingLease struct {
	mu    sync.Mutex
	steps []string
}

func (l *recordingLease) Epoch() uint64 { return 11 }

func (l *recordingLease) KeepAlive(ctx context.Context, _ time.Duration) error {
	<-ctx.Done()
	return nil
}

func (l *recordingLease) Release() error {
	l.record("release")
	return nil
}

func (l *recordingLease) record(step string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.steps = append(l.steps, step)
}

func (l *recordingLease) recorded() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.steps...)
}

// leadershipTestGlobals installs a term-scoped test fixture over the package globals
// serveAsLeader reads — the builder, the process-fuse, the backoff and the two gauges — and puts
// every one of them back. The fuse counter is reset both ways: a leftover count would make the
// fuse fire early here, and a count left behind would make it fire early in the next test.
func leadershipTestGlobals(t *testing.T, build func(context.Context, map[string]config.PskBinding) (*server.Server, *registry.Registry, *downlink.Dispatcher, error)) *fuseRecorder {
	t.Helper()

	prevBuild, prevFail, prevBackoff := buildTermFn, failProcess, standbyRetryInterval
	prevCount, prevCfg := consecutiveTermBuildFailures, Configuration
	prevLeader, prevServing := leaderGauge, servingGauge
	t.Cleanup(func() {
		buildTermFn, failProcess, standbyRetryInterval = prevBuild, prevFail, prevBackoff
		consecutiveTermBuildFailures, Configuration = prevCount, prevCfg
		leaderGauge, servingGauge = prevLeader, prevServing
	})

	fuse := &fuseRecorder{fired: make(chan error, 1)}
	buildTermFn = build
	failProcess = fuse.fire
	standbyRetryInterval = time.Millisecond
	consecutiveTermBuildFailures = 0
	Configuration = &config.Lwm2mConfiguration{}
	leaderGauge = prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_is_leader"})
	servingGauge = prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_is_serving"})
	return fuse
}

// fuseRecorder stands in for the process-ending failProcess so the fuse can be driven without
// tearing down the test binary. It records the error the fuse reported and, so the ORDER against
// the lease release can be asserted, appends to the lease's own step log.
type fuseRecorder struct {
	fired chan error
	lease *recordingLease
}

func (f *fuseRecorder) fire(err error) {
	if f.lease != nil {
		f.lease.record("fail-process")
	}
	select {
	case f.fired <- err:
	default:
	}
}

// 🔴 A BLOWN FUSE MUST NOT TAKE THE PARTITION WITH IT.
//
// The fuse ends the process because this replica cannot build a term, and the whole point of
// ending it is that a replacement takes over. evict() is what calls Release, so ending the process
// before it runs leaves the lease entry in place: the replacement pod's Acquire is refused until
// the entry ages out a full DefaultLeaseTTL, and nothing serves the transport for that window — on
// the one exit path taken precisely because nothing here can serve.
//
// The assertion is an ORDERING one, not a "was Release called" one: a Release that happens after
// the process has been told to end is not a release the replacement can rely on.
func TestTermBuildFuseReleasesTheLeaseBeforeEndingTheProcess(t *testing.T) {
	buildErr := errors.New("listen udp 0.0.0.0:5684: bind: address already in use")
	var builds int
	fuse := leadershipTestGlobals(t, func(context.Context, map[string]config.PskBinding) (*server.Server, *registry.Registry, *downlink.Dispatcher, error) {
		builds++
		return nil, nil, nil, buildErr
	})

	lease := &recordingLease{}
	fuse.lease = lease

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Drive the fuse the way the acquire loop does: one serveAsLeader call per acquisition, each
	// with its own failing build. The count is NOT preset — the failures are real, so the test
	// exercises the counter that decides when the fuse blows as well as what it does then.
	for i := 1; i < maxConsecutiveTermBuildFailures; i++ {
		require.True(t, serveAsLeader(ctx, lease),
			"a sub-threshold build failure must return to the acquire loop, not end the process (attempt %d)", i)
		select {
		case err := <-fuse.fired:
			t.Fatalf("the fuse blew after %d of %d consecutive failures: %v", i, maxConsecutiveTermBuildFailures, err)
		default:
		}
	}

	require.False(t, serveAsLeader(ctx, lease),
		"the %dth consecutive build failure must stop the acquire loop; continuing would re-Acquire "+
			"the partition on a replica that is shutting down and cannot serve it", maxConsecutiveTermBuildFailures)
	require.Equal(t, maxConsecutiveTermBuildFailures, builds)

	var fired error
	select {
	case fired = <-fuse.fired:
	default:
		t.Fatal("the fuse did not end the process after the threshold was reached; a replica that " +
			"cannot build a term would sit Ready and serve nothing forever")
	}
	require.ErrorIs(t, fired, buildErr, "the fuse must report WHY the pod is going away")

	steps := lease.recorded()
	require.Len(t, steps, maxConsecutiveTermBuildFailures+1,
		"every failed term releases the lease, and the fuse adds one step: got %v", steps)
	assert.Equal(t, "release", steps[len(steps)-2],
		"the lease was not released before the fuse ended the process, so the replacement pod must "+
			"wait out a full lease TTL before it can Acquire: got %v", steps)
	assert.Equal(t, "fail-process", steps[len(steps)-1],
		"the fuse must be the LAST thing the term does: got %v", steps)
}

// 🔴 is_leader IS RAISED AT ACQUIRE, NOT AT THE END OF THE TERM BUILD.
//
// The build reads each bound tenant's asserted devices from device-state with reconcileQueryTimeout
// per tenant, serially, so raising the gauge afterwards reports this replica as LEADERLESS for up
// to that × the tenant count while it holds the lease and is the only replica that can serve. The
// no-leader alert the operations documentation asks for is written on this exact series
// (sum(devicechain_lwm2mingest_is_leader) != 1), so the later placement fires it through every one
// of those seconds on a HEALTHY takeover — indistinguishable from the outage it exists to catch.
//
// The test reads the gauge from INSIDE the build, which is the only window in which the two
// placements differ.
func TestIsLeaderIsRaisedAtAcquireNotAfterTheTermBuild(t *testing.T) {
	inBuild := make(chan struct{})
	finishBuild := make(chan struct{})
	leadershipTestGlobals(t, func(context.Context, map[string]config.PskBinding) (*server.Server, *registry.Registry, *downlink.Dispatcher, error) {
		close(inBuild)
		<-finishBuild
		return nil, nil, nil, errors.New("build abandoned once the gauge has been read")
	})

	lease := &recordingLease{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveAsLeader(ctx, lease)
	}()

	select {
	case <-inBuild:
	case <-time.After(5 * time.Second):
		close(finishBuild)
		t.Fatal("serveAsLeader never entered the term build")
	}

	assert.Equal(t, float64(1), testutil.ToFloat64(leaderGauge),
		"is_leader read 0 while this replica held the lease and was building its term; the no-leader "+
			"alert is written on this series and would fire through a healthy takeover")
	assert.Equal(t, float64(0), testutil.ToFloat64(servingGauge),
		"is_serving read 1 before the transport read loop was running, so the pair cannot name a "+
			"leader wedged in its term build — the one state neither gauge expresses alone")

	close(finishBuild)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serveAsLeader did not return after its build failed")
	}

	assert.Equal(t, float64(0), testutil.ToFloat64(leaderGauge),
		"a term that ended still reports this replica as the leader")
}

// The other half of the pair: is_serving goes up only once the transport's read loop is actually
// running, and both gauges come back down when the term ends. Without this, everything the pair
// claims is asserted on the 0 side alone — a gauge that is never raised satisfies every assertion
// in the test above.
//
// This is the only test that drives a SUCCESSFUL term, so it also covers the ordering that the
// build-failure tests cannot see: the read loop starts, and only then does the replica report
// itself as serving.
func TestIsServingIsRaisedOnceTheTransportServes(t *testing.T) {
	// Built out here, not inside the builder: the builder runs on serveAsLeader's goroutine, where
	// a require would call t.FailNow from the wrong goroutine and hang the test rather than fail it.
	srv, err := server.New(server.Config{Addr: "127.0.0.1:0", MaxSessions: 1}, server.Metrics{})
	require.NoError(t, err, "could not bind an ephemeral CoAP/DTLS socket for the term")
	reg := registry.New(&mainResolver{}, &mainEmitter{}, adapter.NewEpochSource(nil),
		registry.Metrics{}, registry.Options{Source: registry.SourceLwM2M})

	leadershipTestGlobals(t, func(context.Context, map[string]config.PskBinding) (*server.Server, *registry.Registry, *downlink.Dispatcher, error) {
		return srv, reg, nil, nil // no dispatcher: this term serves the transport and nothing else
	})

	lease := &recordingLease{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveAsLeader(ctx, lease)
	}()

	require.Eventually(t, func() bool { return testutil.ToFloat64(servingGauge) == 1 }, 5*time.Second, 5*time.Millisecond,
		"is_serving never went up on a term that built and served, so the gauge reports a healthy "+
			"leader as one wedged in its build")
	assert.Equal(t, float64(1), testutil.ToFloat64(leaderGauge), "is_leader came back down on a serving term")

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("serveAsLeader did not unwind after its term context was cancelled")
	}

	assert.Equal(t, float64(0), testutil.ToFloat64(servingGauge), "a term that ended still reports this replica as serving")
	assert.Equal(t, float64(0), testutil.ToFloat64(leaderGauge), "a term that ended still reports this replica as the leader")
	assert.Equal(t, []string{"release"}, lease.recorded(), "an orderly term end releases the lease exactly once")
}

// 🔴 RETURNING false IS NOT THE FIX; THE LOOP ACTING ON IT IS.
//
// serveAsLeader returning false and runLeadership honouring it are two properties, and only the
// first is pinned above. Ignore the return and the fuse path — which has no backoff before it
// returns, deliberately, because it is supposed to be the last thing this replica does — becomes a
// TIGHT loop: acquire, fail the build, evict, ask to end the process (ignored, the shutdown has
// already claimed it), acquire again. It runs for the whole teardown, and every iteration takes
// the partition back from whichever standby had just picked it up. That is a leader-election
// split, produced by a pod that is already dying.
//
// This is the only test that drives the REAL loop against a REAL lease, and the assertion that
// matters is the second one: not "the loop recorded that it stopped" but "a standby can take the
// partition", which is the whole point of releasing it and stays true through any refactor of how
// the loop signals itself.
func TestTheLeadershipLoopStopsAndLeavesThePartitionTakeableWhenTheFuseBlows(t *testing.T) {
	startRunningManager(t) // an embedded JetStream, and the Microservice/NatsManager globals

	distributed, err := NatsManager.NewDistributedLease(messaging.DefaultLeaseTTL)
	require.NoError(t, err)

	fuse := leadershipTestGlobals(t, func(context.Context, map[string]config.PskBinding) (*server.Server, *registry.Registry, *downlink.Dispatcher, error) {
		return nil, nil, nil, errors.New("listen udp 0.0.0.0:5684: bind: address already in use")
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // whichever way this ends, do not leave the loop running against the broker

	done := make(chan struct{})
	go func() {
		defer close(done)
		runLeadership(ctx, distributed)
	}()

	select {
	case <-fuse.fired:
	case <-time.After(30 * time.Second):
		t.Fatal("the fuse never blew: five consecutive term-build failures did not reach failProcess")
	}

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("runLeadership kept looping after the fuse blew, so this replica goes on re-acquiring " +
			"the partition for the whole teardown — taking it back from the standby on every pass")
	}

	// The consequence, from where a standby stands. First try, no retry: a partition that has to be
	// waited for is the failure this whole change is about.
	standby, err := NatsManager.NewDistributedLease(messaging.DefaultLeaseTTL)
	require.NoError(t, err)
	held, err := standby.Acquire(leasePartition)
	require.NoError(t, err, "a standby could not take the LwM2M partition after the fuse blew, so "+
		"nothing serves the transport until the lease entry ages out its full TTL")
	require.NoError(t, held.Release())
}

// failProcess must route through the microservice, not exit the process where it stands.
//
// log.Fatal is an immediate os.Exit: it skips every lifecycle callback, including
// beforeMicroserviceStopped — whose ordering (release the lease, THEN drain the NATS connection
// that release is written over) the two tests in nats_shutdown_test.go establish. Microservice
// .FailNow runs that teardown and still exits non-zero.
//
// The observable here is that the error reaches FailNow at all. What it does NOT pin is the `go`,
// and that gap is stated rather than papered over: this Microservice never started, so FailNow
// reports the outcome and returns instead of tearing anything down, and it returns just as
// promptly with the `go` removed. Core's own FailNow tests do not close it either — they pin that
// FailNow runs the hooks and exits non-zero, which is a property of core, whereas the `go` is
// about THIS service's own shape: beforeMicroserviceStopped waits on leadershipDone, and the
// goroutine that closes leadershipDone is the one calling failProcess. Drop the `go` and that
// goroutine parks inside FailNow, <-leadershipDone never fires, the teardown budget expires, and
// the process exits 1 over an undrained NATS connection. A delayed, untidy exit — not a split
// brain — which is why the gap is recorded here instead of being closed with a fake microservice
// that could not exhibit it anyway.
func TestFailProcessHandsTheErrorToTheMicroservice(t *testing.T) {
	prev := Microservice
	t.Cleanup(func() { Microservice = prev })
	// A struct literal, as every test in this package builds one: it has no root context and no
	// registry, and FailNow is nil-safe on both.
	Microservice = &core.Microservice{
		InstanceId:     uniqueIdentity("lwm2m-failnow"),
		FunctionalArea: uniqueIdentity("lwm2m-failnow"),
		Readiness:      core.NewReadinessGate(),
	}

	logged := captureLogs(t)
	failProcess(errors.New("the leadership term build failed repeatedly"))

	require.Eventually(t, func() bool {
		return strings.Contains(logged.String(), "declared this process unfit to continue")
	}, 5*time.Second, 5*time.Millisecond,
		"failProcess did not reach Microservice.FailNow, so the process would end without running "+
			"the shutdown hooks; logs were:\n"+logged.String())
	assert.Contains(t, logged.String(), "the leadership term build failed repeatedly",
		"the reason the pod is going away was dropped on the way to FailNow")
}

// A replica with no microservice handle cannot end the process, and must say so rather than
// silently returning as though the fuse had done its job.
func TestFailProcessWithoutAMicroserviceSaysSo(t *testing.T) {
	prev := Microservice
	t.Cleanup(func() { Microservice = prev })
	Microservice = nil

	logged := captureLogs(t)
	failProcess(errors.New("boom"))
	assert.Contains(t, logged.String(), "cannot end the process")
}

// newTermServer builds a real CoAP/DTLS transport on an ephemeral loopback port, plus the
// registry a term needs, for a test that drives a term through buildTermFn. It is a REAL
// server.Server because buildTermFn's signature is concrete: superviseServe takes an interface,
// but the builder it is fed from does not, so a transport death has to be provoked on the real
// thing — by Stopping the socket from OUTSIDE the term, which is precisely a Serve return with no
// serve-intent recorded, i.e. the death path.
func newTermServer(t *testing.T) (*server.Server, *registry.Registry) {
	t.Helper()
	srv, err := server.New(server.Config{Addr: "127.0.0.1:0", MaxSessions: 1}, server.Metrics{})
	require.NoError(t, err, "could not bind an ephemeral CoAP/DTLS socket for the term")
	reg := registry.New(&mainResolver{}, &mainEmitter{}, adapter.NewEpochSource(nil),
		registry.Metrics{}, registry.Options{Source: registry.SourceLwM2M})
	return srv, reg
}

// 🔴 A DEAD TRANSPORT MUST NOT TAKE THE PARTITION WITH IT EITHER.
//
// The term-build fuse is not the only path that ends this pod while it holds the lease. A Serve
// return under a live term is the other one, and it is the worse of the two: the build fuse fires
// on a replica that never managed to serve, whereas this one fires on the replica that WAS
// serving the whole fleet. Ending the process there without releasing first leaves the lease
// entry in place, so the replacement pod's Acquire is refused until it ages out a full
// DefaultLeaseTTL — nothing serves the transport for that window.
//
// Two properties, and the ordering one is what this test carries:
//
//   - the lease is released BEFORE the process is told to end. A Release that happens after is
//     not a release the replacement can rely on.
//   - the term actually unwinds at all. Nothing but onDeath observes a Serve return (superviseServe
//     reports it nowhere else), so a term that only records the error parks on its term context
//     with a dead socket — holding the partition, reporting is_serving=1, serving nothing.
//
// The wait below is bounded and its message names that second failure, because its signature is
// something NOT happening.
func TestATransportDeathReleasesTheLeaseBeforeEndingTheProcess(t *testing.T) {
	srv, reg := newTermServer(t)

	fuse := leadershipTestGlobals(t, func(context.Context, map[string]config.PskBinding) (*server.Server, *registry.Registry, *downlink.Dispatcher, error) {
		return srv, reg, nil, nil // no dispatcher: this term serves the transport and nothing else
	})
	lease := &recordingLease{}
	fuse.lease = lease

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	returned := make(chan bool, 1)
	go func() { returned <- serveAsLeader(ctx, lease) }()

	require.Eventually(t, func() bool { return testutil.ToFloat64(servingGauge) == 1 }, 10*time.Second, 5*time.Millisecond,
		"the term never started serving, so there is no live transport for this test to kill")

	// The death: the socket goes away underneath a term that still intends to serve. This is what
	// superviseServe calls an unexpected transport death — no serve-intent was recorded first.
	srv.Stop()

	var kept bool
	select {
	case kept = <-returned:
	case <-time.After(20 * time.Second):
		t.Fatal("serveAsLeader never returned after its transport died: nothing but onDeath observes " +
			"a Serve return, so a term that does not act on it holds the partition with a dead socket " +
			"until something else cancels it")
	}
	assert.False(t, kept, "the acquire loop must not continue after a transport death; the pod is "+
		"ending, and looping re-Acquires the partition it has just released")

	var fired error
	select {
	case fired = <-fuse.fired:
	case <-time.After(10 * time.Second):
		t.Fatal("the transport died and nothing ended the process; the pod would sit Ready with no " +
			"transport, which is a total ingest outage no other signal on it reports")
	}
	assert.ErrorContains(t, fired, "the CoAP/DTLS transport stopped serving",
		"the reason the pod is going away was dropped on the way to the fuse")
	// 🔴 AND SPECIFICALLY THE nil-ERROR WORDING. go-coap's DTLS Serve returns nil on both of the
	// ways a live listener stops — closed, and context cancelled — and continues its accept loop on
	// every other accept error, so nil is what a real transport death produces, not an edge case.
	// Assert the branch production actually takes: formatted with %w instead, this same message
	// would read "...to be replaced: %!w(<nil>)".
	assert.ErrorContains(t, fired, "(Serve returned without an error)",
		"a Serve that returned nil — which is what a closed or cancelled DTLS listener returns, i.e. "+
			"the ordinary production death — was formatted through the wrapping branch, so the only "+
			"line an operator gets names no cause and carries a %!w(<nil>) instead")

	steps := lease.recorded()
	require.Len(t, steps, 2, "a term ended by a transport death releases its lease once and then ends the process: got %v", steps)
	assert.Equal(t, "release", steps[0],
		"the lease was not released before the process was told to end, so the replacement pod must "+
			"wait out a full lease TTL before it can Acquire: got %v", steps)
	assert.Equal(t, "fail-process", steps[1],
		"ending the process must be the LAST thing the term does: got %v", steps)

	assert.Equal(t, float64(0), testutil.ToFloat64(servingGauge), "a term whose transport died still reports this replica as serving")
	assert.Equal(t, float64(0), testutil.ToFloat64(leaderGauge), "a term whose transport died still reports this replica as the leader")
}

// 🔴 THE CONSEQUENCE, FROM WHERE THE STANDBY STANDS.
//
// The ordering test above asserts against a fake lease, which can only ever show what the term
// CALLED. This one drives the real acquire loop against a real DistributedLease over an embedded
// JetStream and asks the question a replacement pod asks: can I take the partition, now, first
// try? That is the whole point of releasing it, and it stays true through any refactor of how the
// term signals itself.
//
// It also pins the half the ordering test cannot see: that the LOOP honours the stop. The builder
// hands out a FRESH transport per term deliberately — reusing one would make a second iteration
// die instantly and stop on its own, which would let a loop that ignores the stop look correct.
// With a fresh, healthy socket a loop that keeps going keeps the partition, and the wait below
// times out saying so.
func TestTheLeadershipLoopStopsAndLeavesThePartitionTakeableWhenTheTransportDies(t *testing.T) {
	startRunningManager(t) // an embedded JetStream, and the Microservice/NatsManager globals

	distributed, err := NatsManager.NewDistributedLease(messaging.DefaultLeaseTTL)
	require.NoError(t, err)

	built := make(chan *server.Server, 8)
	fuse := leadershipTestGlobals(t, func(context.Context, map[string]config.PskBinding) (*server.Server, *registry.Registry, *downlink.Dispatcher, error) {
		srv, err := server.New(server.Config{Addr: "127.0.0.1:0", MaxSessions: 1}, server.Metrics{})
		if err != nil {
			return nil, nil, nil, err
		}
		t.Cleanup(srv.Stop) // every term's socket goes away with the test, taken over or not
		reg := registry.New(&mainResolver{}, &mainEmitter{}, adapter.NewEpochSource(nil),
			registry.Metrics{}, registry.Options{Source: registry.SourceLwM2M})
		select {
		case built <- srv:
		default:
		}
		return srv, reg, nil, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // whichever way this ends, do not leave the loop running against the broker

	done := make(chan struct{})
	go func() {
		defer close(done)
		runLeadership(ctx, distributed)
	}()

	var first *server.Server
	select {
	case first = <-built:
	case <-time.After(30 * time.Second):
		t.Fatal("the loop never acquired the partition and built a term")
	}
	require.Eventually(t, func() bool { return testutil.ToFloat64(servingGauge) == 1 }, 10*time.Second, 5*time.Millisecond,
		"the first term never started serving, so there is no live transport for this test to kill")

	first.Stop() // the transport dies under a term that still intends to serve

	select {
	case <-fuse.fired:
	case <-time.After(30 * time.Second):
		t.Fatal("a transport death under a live term did not reach failProcess")
	}

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("runLeadership kept looping after its transport died, so this replica re-acquires the " +
			"partition it has just released — on a pod that is shutting down — and takes it back from " +
			"the standby that had just picked it up")
	}

	// The consequence. First try, no retry: a partition that has to be waited for is the failure
	// this whole change is about.
	standby, err := NatsManager.NewDistributedLease(messaging.DefaultLeaseTTL)
	require.NoError(t, err)
	held, err := standby.Acquire(leasePartition)
	require.NoError(t, err, "a standby could not take the LwM2M partition after the serving replica's "+
		"transport died, so nothing serves the transport until the lease entry ages out its full TTL")
	require.NoError(t, held.Release())
}

// 🔴 THE INERT TRANSPORT HAS TO END THE POD TOO, AND NOTHING ELSE WILL.
//
// An identity-less deployment takes no lease, so this path has nothing to release and none of the
// failover cost the leadership tests are about. What it has instead is the same total outage with
// no other signal: its transport is the whole service, so a socket that dies leaves a pod that is
// Ready, answers no probe, and is restarted by nothing. The line that prevents that is a single
// call inside superviseInertTransport, and a call is exactly the kind of thing a refactor drops
// silently — this test is what makes dropping it fail.
//
// Both directions are asserted. Only the first is the guard; the second is what stops the guard
// being satisfied by a failProcess that fires on every stop, which would make an ordinary
// shutdown report the pod as failed.
func TestTheInertTransportEndsTheProcessWhenItDiesAndNotWhenItIsStopped(t *testing.T) {
	prev := failProcess
	t.Cleanup(func() { failProcess = prev })
	fired := make(chan error, 1)
	failProcess = func(err error) {
		select {
		case fired <- err:
		default:
		}
	}

	// The graceful direction first, on its own supervision: an intended stop must NOT end the pod.
	graceful := newFakeServe()
	superviseInertTransport(graceful)()
	select {
	case err := <-fired:
		t.Fatalf("an intended stop of the inert transport ended the process: %v", err)
	default:
	}

	// The death direction: Serve returns with no intent recorded.
	dying := newFakeServe()
	superviseInertTransport(dying)
	dying.die()

	select {
	case err := <-fired:
		require.Error(t, err)
		assert.ErrorContains(t, err, "the CoAP/DTLS transport stopped serving",
			"the inert transport died and the process was ended without saying why")
	case <-time.After(10 * time.Second):
		t.Fatal("the inert transport died and nothing ended the process; the pod would sit Ready " +
			"answering no probe, with nothing to restart it — its transport IS the service")
	}
}
