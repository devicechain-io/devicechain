// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// These tests stop a DETECT leader while its broker is unavailable and read what the
// NEXT leader would see. They need a server of their own, not sharedBroker: taking it
// away would break every parallel test on the shared one.
//
// Two outages, as in the messaging package's lease tests:
//
//   - "connected, JetStream gone" (jetStreamOff, srv.ShutdownJetStream): the state a
//     server is in from the start of its lame-duck mode. Used only where JetStream does
//     not come back: srv.EnableJetStream races the server's own API dispatch for any
//     request in flight, which the race detector reports inside nats-server.
//   - "the server restarted" (down, then up on the same port and store directory).
//     These replicas connect the way production does, so a write made while the
//     server is down is BUFFERED and flushed on reconnect. A test that brings the
//     server back therefore closes the departing leader's connection as soon as its
//     stop returns, as the process exiting does — otherwise a single release attempt
//     buffered during the outage would land on reconnect anyway, and a one-attempt
//     release would pass the test meant to show it does not wait for the broker.
//     The outage also outlasts one JetStream API timeout (5s) for the same reason: a
//     single attempt waits that long for its reply.

// outageBroker is one dedicated embedded server that a test can stop and start again
// on the same port and store directory.
type outageBroker struct {
	srv    *natsserver.Server
	port   int
	dir    string
	leader *messaging.NatsManager
}

func startOutageBroker(t *testing.T) *outageBroker {
	t.Helper()
	b := &outageBroker{dir: dctest.JetStreamStoreDir(t)}
	b.up(t)
	t.Cleanup(func() { b.srv.Shutdown() })
	return b
}

// up starts the server, on the port it had before when there was one.
func (b *outageBroker) up(t *testing.T) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: b.port, JetStream: true, StoreDir: b.dir,
		NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded nats server not ready")
	b.srv = srv
	b.port = srv.Addr().(*net.TCPAddr).Port
}

// down stops the server; its clients start reconnecting.
func (b *outageBroker) down(t *testing.T) {
	t.Helper()
	b.srv.Shutdown()
	b.srv.WaitForShutdown()
}

// leaderExits closes the departing leader's connection, as its process exiting does,
// which discards anything it buffered while the server was down.
func (b *outageBroker) leaderExits() {
	if c := b.leader.Conn(); c != nil {
		c.Close()
	}
}

// manager builds a real NatsManager on the server — one replica's broker side.
func (b *outageBroker) manager(t *testing.T) *messaging.NatsManager {
	t.Helper()
	ms := &core.Microservice{InstanceId: "aa2", FunctionalArea: "event-processing"}
	ms.InstanceConfiguration.Infrastructure.Nats.Hostname = "127.0.0.1"
	ms.InstanceConfiguration.Infrastructure.Nats.Port = uint32(b.port)
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	return nmgr
}

// jetStreamOff takes JetStream away with the clients still connected: lame-duck mode.
// It is never undone in a test (see the file comment).
func (b *outageBroker) jetStreamOff(t *testing.T) {
	t.Helper()
	require.NoError(t, b.srv.ShutdownJetStream())
}

// successor asks, as a second replica on its own connection, the two questions the
// supervisor asks before it reads any state: did the prior owner release cleanly (skip
// termSlack) and can I acquire. It retries only a broker ERROR, while JetStream
// finishes recovering its streams, never an answer.
func (b *outageBroker) successor(t *testing.T, partition string) (clean bool, acquireErr error) {
	t.Helper()
	nmgr := b.manager(t)
	deadline := time.Now().Add(10 * time.Second)
	for {
		dl, err := nmgr.NewDistributedLease(messaging.DefaultLeaseTTL)
		if err == nil {
			clean = dl.PriorOwnerReleasedCleanly(partition)
			_, acquireErr = dl.Acquire(partition)
			if acquireErr == nil || errors.Is(acquireErr, messaging.ErrLeaseHeld) {
				return clean, acquireErr
			}
			err = acquireErr
		}
		require.True(t, time.Now().Before(deadline), "the successor could not reach the lease bucket: %v", err)
		time.Sleep(100 * time.Millisecond)
	}
}

// liveTerm is a leader in a running term on a real lease: the processor initialized,
// the partition acquired, the holder watch and the renewer running, and the supervisor
// goroutine parked in awaitTermEnd — so ExecuteStop and a term-context cancel reach
// endTerm the way they do in production.
func (b *outageBroker) liveTerm(t *testing.T) *ResolvedEventsProcessor {
	t.Helper()
	rp := newTestProcessor(newTestStore(t), nil, 1)
	ms := &core.Microservice{InstanceId: "aa2", FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	rp.metrics = NewDetectMetrics(ms)
	b.leader = b.manager(t)
	dl, err := b.leader.NewDistributedLease(messaging.DefaultLeaseTTL)
	require.NoError(t, err)
	rp.Lease = dl
	rp.Gate = NewTermGate()
	require.NoError(t, rp.restore(context.Background()))
	require.NoError(t, rp.ExecuteInitialize(context.Background()))

	lease, err := dl.Acquire(rp.cfg.PartitionId)
	require.NoError(t, err)
	rp.newTermContext()
	handle := &termHandle{lease: lease, ctx: rp.pctx()}
	holder, err := lease.WatchHolder(handle.ctx)
	require.NoError(t, err)
	handle.holder = holder
	rp.Gate.Enter(holder.Held)
	keepAliveDone := make(chan struct{})
	go func() {
		defer close(keepAliveDone)
		if lease.KeepAlive(handle.ctx, renewInterval) != nil {
			rp.pcancel()
		}
	}()
	handle.keepAliveDone = keepAliveDone
	rp.metrics.setLeader(true)
	rp.metrics.setDetectLive(true)

	rp.supWG.Add(1)
	go func() {
		defer rp.supWG.Done()
		rp.awaitTermEnd(handle)
	}()
	t.Cleanup(func() {
		rp.supCancel()
		rp.supWG.Wait()
	})
	return rp
}

// TestAGracefulStopReleasesThePartitionAcrossABrokerOutage is the upgrade case: the
// leader is told to stop while its broker is down, and the broker comes back within
// the stop's budget. The release must land before the process exits, so the next
// leader reads a clean release and acquires at once — it skips the handover wait.
//
// Before the fix the stop made ONE release attempt, which waited one API timeout for
// a reply and gave up, and the process exited with nothing released: the successor's
// Acquire read ErrLeaseHeld, so it stood by until the entry expired (up to 30s) and
// then paid termSlack (20s) before detecting anything.
func TestAGracefulStopReleasesThePartitionAcrossABrokerOutage(t *testing.T) {
	b := startOutageBroker(t)
	rp := b.liveTerm(t)
	b.down(t)
	back := make(chan struct{})
	go func() {
		defer close(back)
		time.Sleep(7 * time.Second) // longer than one API timeout: see the file comment
		b.up(t)
	}()

	stopCtx, cancel := context.WithTimeout(context.Background(), releaseShutdownHeadroom+15*time.Second)
	defer cancel()
	start := time.Now()
	require.NoError(t, rp.ExecuteStop(stopCtx))
	el := time.Since(start)
	b.leaderExits()
	<-back
	if el > 20*time.Second {
		t.Fatalf("ExecuteStop took %v across a 7s broker outage with a 15s release budget", el)
	}

	clean, acquireErr := b.successor(t, rp.cfg.PartitionId)
	if acquireErr != nil {
		t.Fatalf("the successor's Acquire after a stop during a 7s broker outage = %v, want nil: "+
			"the departing leader's release did not land, so its entry blocks the partition until it expires", acquireErr)
	}
	if !clean {
		t.Fatal("PriorOwnerReleasedCleanly = false after the stop, want true: the successor would wait out termSlack")
	}
}

// TestAStopThatOutlastsTheBrokerOutageBudgetStillEnds is the negative control: an
// outage longer than the stop's budget must not hold the stop past it, and nothing is
// released — the successor still reads the hold.
func TestAStopThatOutlastsTheBrokerOutageBudgetStillEnds(t *testing.T) {
	b := startOutageBroker(t)
	rp := b.liveTerm(t)
	b.down(t)

	stopCtx, cancel := context.WithTimeout(context.Background(), releaseShutdownHeadroom+time.Second)
	defer cancel()
	start := time.Now()
	require.NoError(t, rp.ExecuteStop(stopCtx))
	el := time.Since(start)
	b.leaderExits()
	// 1s of release budget, plus one attempt that may run one API timeout past it.
	if el > 8*time.Second {
		t.Fatalf("ExecuteStop took %v; its release budget was 1s", el)
	}

	b.up(t)
	if _, acquireErr := b.successor(t, rp.cfg.PartitionId); !errors.Is(acquireErr, messaging.ErrLeaseHeld) {
		t.Fatalf("the successor's Acquire after a release that gave up = %v, want ErrLeaseHeld", acquireErr)
	}
}

// TestAStopDuringATermEndRetryIsBoundedByTheStopDeadline: a term that ends for its own
// reasons (here its context, as a stale checkpoint or the renewer ends it) with the
// broker down starts a release bounded only by the lease window, up to 30s. A stop
// arriving during that release must bound it by the stop's deadline, or the processor
// overruns the teardown budget and the NATS and database managers are never stopped.
//
// It also pins what the gauges say meanwhile: not consuming (detect_live 0) from the
// moment the readers stop, still the leader (detect_is_leader 1) while the entry may
// still be ours.
func TestAStopDuringATermEndRetryIsBoundedByTheStopDeadline(t *testing.T) {
	b := startOutageBroker(t)
	rp := b.liveTerm(t)
	b.jetStreamOff(t)

	rp.pcancel() // the term ends; the supervisor goroutine runs endTerm, which retries
	time.Sleep(time.Second)
	if live := testutil.ToFloat64(rp.metrics.detectLive); live != 0 {
		t.Fatalf("detect_live while the release retries = %v, want 0: the term's readers have stopped", live)
	}
	if leader := testutil.ToFloat64(rp.metrics.isLeader); leader != 1 {
		t.Fatalf("detect_is_leader while the release retries = %v, want 1: the entry may still be ours", leader)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), releaseShutdownHeadroom+time.Second)
	defer cancel()
	start := time.Now()
	require.NoError(t, rp.ExecuteStop(stopCtx))
	if el := time.Since(start); el > 8*time.Second {
		t.Fatalf("ExecuteStop took %v while a term-end release was retrying; its release budget was 1s", el)
	}
	if leader := testutil.ToFloat64(rp.metrics.isLeader); leader != 0 {
		t.Fatalf("detect_is_leader after the stop = %v, want 0", leader)
	}
}

// TestReleaseContextIsTheStopDeadlineLessHeadroom pins the bound itself, in both of the
// ways it is set: already stopping, and a stop arriving after the release began.
func TestReleaseContextIsTheStopDeadlineLessHeadroom(t *testing.T) {
	t.Run("stopping already", func(t *testing.T) {
		rp := leasedProcessor(t)
		require.NoError(t, rp.ExecuteInitialize(context.Background()))
		t0 := time.Now().Add(time.Minute)
		rp.stopDeadline.Store(t0.UnixNano())
		ctx, cancel := rp.releaseContext()
		defer cancel()
		dl, ok := ctx.Deadline()
		if !ok {
			t.Fatal("release context has no deadline while the process is stopping")
		}
		if want := time.Unix(0, t0.UnixNano()).Add(-releaseShutdownHeadroom); !dl.Equal(want) {
			t.Fatalf("release deadline = %v, want the stop deadline less releaseShutdownHeadroom, %v", dl, want)
		}
	})
	t.Run("a stop arriving later", func(t *testing.T) {
		rp := leasedProcessor(t)
		require.NoError(t, rp.ExecuteInitialize(context.Background()))
		ctx, cancel := rp.releaseContext()
		defer cancel()
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("release context has a deadline while the process is not stopping")
		}
		select {
		case <-ctx.Done():
			t.Fatal("release context ended while the process is not stopping")
		case <-time.After(200 * time.Millisecond):
		}

		stopAt := time.Now()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), releaseShutdownHeadroom+300*time.Millisecond)
		defer stopCancel()
		require.NoError(t, rp.ExecuteStop(stopCtx))
		select {
		case <-ctx.Done():
			if el := time.Since(stopAt); el < 200*time.Millisecond {
				t.Fatalf("release context ended %v after the stop, want about 300ms (the stop deadline less "+
					"releaseShutdownHeadroom)", el)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("release context did not end after a stop arrived; the release would wait out the lease window")
		}
	})
	// haltStaleWriter cancels the supervisor BEFORE the process is stopped: the term
	// ends, its release starts with no deadline recorded, and the stop that FailNow's
	// teardown sends arrives afterwards. A release armed on supCtx's edge saw that edge
	// already gone, with nothing recorded, and never looked again, so the stop's
	// deadline did not reach it and only the lease window (up to 30s) bounded it.
	t.Run("a stop following a supervisor cancel", func(t *testing.T) {
		rp := leasedProcessor(t)
		require.NoError(t, rp.ExecuteInitialize(context.Background()))
		rp.supCancel()
		ctx, cancel := rp.releaseContext()
		defer cancel()
		select {
		case <-ctx.Done():
			t.Fatal("release context ended on a supervisor cancel with no stop deadline recorded")
		case <-time.After(200 * time.Millisecond):
		}

		stopCtx, stopCancel := context.WithTimeout(context.Background(), releaseShutdownHeadroom+300*time.Millisecond)
		defer stopCancel()
		require.NoError(t, rp.ExecuteStop(stopCtx))
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("release context did not end after a stop that followed a supervisor cancel; " +
				"the release would wait out the lease window past the teardown budget")
		}
	})
}

// TestExecuteStopRecordsItsDeadline: the stop deadline reaches the release only through
// what ExecuteStop records.
func TestExecuteStopRecordsItsDeadline(t *testing.T) {
	rp := leasedProcessor(t)
	require.NoError(t, rp.ExecuteInitialize(context.Background()))
	d := time.Now().Add(42 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), d)
	defer cancel()
	require.NoError(t, rp.ExecuteStop(ctx))
	if got := rp.stopDeadline.Load(); got != d.UnixNano() {
		t.Fatalf("stopDeadline after ExecuteStop = %d, want the stop context's deadline %d", got, d.UnixNano())
	}
}
