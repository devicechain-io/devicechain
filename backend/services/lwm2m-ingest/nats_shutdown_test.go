// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/service"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// sink is the process-wide destination for the global zerolog logger, installed once
// by TestMain.
//
// 🔴 It is a sink with a switch rather than a logger a test swaps in and out, and
// that is the whole point. zerolog's global logger is a plain variable with no
// synchronization, while core/messaging's connection handlers log from the NATS
// client's own async callback goroutine — so assigning to it while a connection is
// live is a genuine data race, which -race reports against zerolog's own read.
// Installing the sink before any connection exists and never reassigning removes the
// race entirely; what a test toggles instead is a mutex-guarded capture flag.
//
// This package used to carry its own copy of that type. The shared one in core/test
// supersedes it, and not only to avoid a duplicate: the local copy passed each line
// through to stderr AFTER releasing the lock, which is safe only in front of an
// os.File, whose Write is a single syscall. The shared sink writes through under the
// same lock, so it is safe in front of any writer — including the next caller who
// hands it a buffer.
var sink *dctest.LogSink

// TestMain installs the sink before any test runs, which is the only moment at which
// writing zerolog's global logger is safe. It is never restored: nothing after m.Run
// needs the original, and reassigning there would race the same callback goroutines.
func TestMain(m *testing.M) {
	sink = dctest.InstallLogSink()
	os.Exit(m.Run())
}

// identitySeq makes every microservice identity in this file unique within the test
// binary.
//
// What it is still needed FOR is the lease: the instance id names the shared lease
// bucket, so a unique one keeps each test's lease state to itself.
//
// ⚠️ It was written for a second reason that no longer exists, and the difference is
// worth stating. NewNatsManager used to register its stream-metrics gauges against the
// process-global default registry, keyed by the functional area, so two managers built
// with the same area panicked with "duplicate metrics collector registration" — a test
// that passed under `go test` and blew up under `-count=2`. A Microservice registers
// into a registry it owns now, so a fixed area is no longer a hazard, and this helper
// no longer has to be re-derived by every service that writes its first such test.
var identitySeq atomic.Int64

func uniqueIdentity(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, identitySeq.Add(1))
}

// startEmbeddedNats runs an in-process NATS server and returns its host and port.
func startEmbeddedNats(t *testing.T) (string, uint32) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1, // ephemeral
		JetStream: true,
		StoreDir:  dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)

	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return u.Hostname(), uint32(port)
}

// startRunningManager brings a real NatsManager up to Started against an in-process
// broker and installs the package-level state the stopper reads — including Svc, which
// wraps it with service.FromManagers so beforeMicroserviceStopped drives core/service's
// real sequence rather than a copy of it.
//
// The Svc it installs holds no probe server, which is a test simplification rather than
// a claim about production: a real replica always has one, and it is stopped after NATS,
// where it cannot affect what these tests assert. Each test starts from a known-empty
// baseline and puts back whatever it found, so nothing here leaks into another test.
func startRunningManager(t *testing.T) {
	t.Helper()
	host, port := startEmbeddedNats(t)

	prevMs, prevMgr, prevSvc := Microservice, NatsManager, Svc
	prevCancel, prevDone, prevInert := leadershipCancel, leadershipDone, inertStop
	t.Cleanup(func() {
		Microservice, NatsManager, Svc = prevMs, prevMgr, prevSvc
		leadershipCancel, leadershipDone, inertStop = prevCancel, prevDone, prevInert
	})
	leadershipCancel, leadershipDone, inertStop = nil, nil, nil

	identity := uniqueIdentity("lwm2m-ingest")
	Microservice = &core.Microservice{
		InstanceId:     identity,
		FunctionalArea: identity,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{
		Hostname: host,
		Port:     port,
	}

	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	require.NoError(t, NatsManager.Initialize(context.Background()))
	require.NoError(t, NatsManager.Start(context.Background()))
	require.NotNil(t, NatsManager.Conn())
	require.False(t, NatsManager.Conn().IsClosed(), "connection should be live before shutdown")
	Svc = service.FromManagers(Microservice, service.Managers{Nats: NatsManager})

	// A require between here and the end of the test unwinds through Cleanup without
	// ever closing this connection, and the client reconnects forever — so a single
	// failed assertion would spend the rest of the package run logging retries against
	// a broker that has been shut down. Close it on the way out if the test did not.
	mgr := NatsManager
	t.Cleanup(func() {
		if c := mgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
}

// captureLogs starts recording into the process-wide sink for the life of the test.
// Package tests run sequentially (nothing here calls t.Parallel), so one capture is
// live at a time — which the sink itself now checks rather than leaving to convention.
func captureLogs(t *testing.T) *dctest.LogSink {
	t.Helper()
	return sink.Capture(t)
}

// An orderly shutdown must STOP the NATS manager, not merely terminate it.
//
// The two are not interchangeable and the difference is invisible from the call
// site. Terminate is legal only from Stopped (core/core/lifecycle.go), so a
// stopper that skips Stop gets its Terminate REFUSED by the state machine. This
// service's hand-written stopper once did exactly that (#924): the refusal was
// logged rather than returned, so teardown still reported success while the
// metrics sampler kept running and the connection was never drained or closed.
// core/service makes the stop now and returns a refusal, which is what the
// NoError on the terminate below reads.
//
// The operator-visible symptom is a lie in the logs, and a failed liveness probe. The
// manager marks its close as requested only inside ExecuteStop/ExecuteTerminate, so with
// neither having run the connection's ClosedHandler takes its alarm branch and reports a
// clean stop as a permanent, unasked-for close — and marks the process not live.
//
// So this asserts both halves of what a correct stopper produces, because either
// on its own is satisfiable by a broken one: the connection is really closed (a
// skipped Stop leaves it open), and the close was recognized as ours (the ERROR
// branch never fires).
func TestOrderlyShutdownStopsAndTerminatesTheNatsManager(t *testing.T) {
	startRunningManager(t)
	logged := captureLogs(t)

	ctx := context.Background()
	require.NoError(t, beforeMicroserviceStopped(ctx))
	require.NoError(t, afterMicroserviceTerminated(ctx))

	requireClosedAndTerminated(t)

	// The ClosedHandler runs on the client's callback goroutine, so wait for the line
	// rather than racing it. Its arrival is also what proves the handler ran at all —
	// without it, "no alarm was logged" would be vacuously true.
	require.Eventually(t, func() bool {
		return bytes.Contains([]byte(logged.String()), []byte("NATS connection closed during shutdown"))
	}, 5*time.Second, 10*time.Millisecond,
		"the ClosedHandler did not recognize the close as part of a shutdown; logs were:\n"+logged.String())
	require.NotContains(t, logged.String(), "CLOSED permanently",
		"a clean shutdown logged the permanent-close alarm")
	require.NoError(t, Microservice.Live(),
		"a clean shutdown marked the process not live, which would restart every pod on its way out")
}

// WHERE the NATS manager is stopped matters as much as THAT it is stopped, and the
// constraint runs the other way from the obvious one.
//
// The leadership unwind is not merely bookkeeping that can be done in any order: it
// ends in evict(), which RELEASES THE LEASE — a KV write over the very connection
// Svc.Stop drains. Hoisting Svc.Stop above the unwind therefore makes the release
// fail on a draining connection, and a lease that is not released is a lease a
// standby cannot take until it ages out a full TTL. That is a failover gap, not a
// tidiness issue, and the test above cannot see it: it runs the NATS-only branch,
// with no leadership loop installed at all.
//
// So this installs an unwind that does what evict() does — release a real lease over
// the real connection — and asserts it succeeded. It deliberately stands in for
// serveAsLeader rather than running it: the release is the only part of that unwind
// which touches NATS, so it is the whole of what the ordering has to protect.
func TestOrderlyShutdownUnwindsLeadershipBeforeStoppingNats(t *testing.T) {
	startRunningManager(t)
	ctx := context.Background()

	distributed, err := NatsManager.NewDistributedLease(messaging.DefaultLeaseTTL)
	require.NoError(t, err)
	held, err := distributed.Acquire(leasePartition)
	require.NoError(t, err)

	lctx, cancel := context.WithCancel(context.Background())
	leadershipCancel = cancel
	leadershipDone = make(chan struct{})
	var releaseErr error
	go func() {
		defer close(leadershipDone)
		<-lctx.Done()
		releaseErr = held.Release()
	}()

	// beforeMicroserviceStopped waits on leadershipDone, so the close above
	// happens-before this read.
	require.NoError(t, beforeMicroserviceStopped(ctx))
	require.NoError(t, releaseErr,
		"the leadership unwind could not release its lease, so a standby must wait out the full "+
			"lease TTL to take over: the NATS manager was stopped before the unwind ran, leaving the "+
			"release to execute on a draining connection")

	require.NoError(t, afterMicroserviceTerminated(ctx))
	requireClosedAndTerminated(t)
}

// requireClosedAndTerminated asserts the orderly-shutdown end state: the broker connection
// closed, and the Service actually TERMINATED rather than merely stopped.
//
// 🔴 IT READS THE CLOSE ONCE, WITHOUT WAITING, AND THAT IS NOW SOUND. The NATS manager's
// Stop waits for its drain to finish, and the drain's own final Close is the last thing
// the library's drain goroutine does, so once Stop has returned nothing moves the status
// off CLOSED again. This used to need a fifteen-second wait: Stop returned with the drain
// still running, Terminate's Close landed first, and the drain goroutine then moved the
// status to DRAINING_PUBS over CLOSED and spent a five-second flush against a gone socket
// before closing again — reading it once flaked about once in a hundred runs. That
// reversal still exists on exactly one path, when the stop budget runs out and the
// manager closes the connection under a live drain; these tests stop with
// context.Background(), which has no deadline and never takes it.
//
// 🔴 AND BECAUSE A DRAIN CLOSES THE CONNECTION ON ITS OWN, "eventually closed" cannot tell
// a terminated Service from one whose terminator was dropped. The second half does: a
// Service that really terminated refuses to terminate again.
func requireClosedAndTerminated(t *testing.T) {
	t.Helper()
	require.True(t, NatsManager.Conn().IsClosed(),
		"orderly shutdown left the NATS connection open: Stop was skipped, so Terminate was "+
			"refused from the Started state and never closed it")
	require.ErrorContains(t, Svc.Terminate(context.Background()), "Terminated",
		"the terminator did not terminate the Service: a second terminate should be refused")
}
