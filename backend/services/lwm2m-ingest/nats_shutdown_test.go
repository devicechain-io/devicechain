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
		StoreDir:  t.TempDir(),
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
// broker and installs the package-level state the stopper reads.
//
// The globals are a test simplification, not a claim about production: main() always
// builds the HTTP server, so no real replica runs with httpServer nil. Each test
// starts from a known-empty baseline and puts back whatever it found, so nothing here
// leaks into another test.
func startRunningManager(t *testing.T) {
	t.Helper()
	host, port := startEmbeddedNats(t)

	prevMs, prevMgr, prevSrv := Microservice, NatsManager, httpServer
	prevCancel, prevDone, prevInert := leadershipCancel, leadershipDone, inertStop
	t.Cleanup(func() {
		Microservice, NatsManager, httpServer = prevMs, prevMgr, prevSrv
		leadershipCancel, leadershipDone, inertStop = prevCancel, prevDone, prevInert
	})
	httpServer, leadershipCancel, leadershipDone, inertStop = nil, nil, nil, nil

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
// stopper that skips Stop gets its Terminate REFUSED by the state machine — and
// because the refusal is logged rather than returned, teardown still reports
// success. What actually happens is that the metrics sampler keeps running and
// the connection is never drained or closed.
//
// The operator-visible symptom is a lie in the logs. shuttingDown is set inside
// ExecuteStop/ExecuteTerminate, so with neither having run the connection's
// ClosedHandler takes its alarm branch and reports a clean stop as a permanent,
// unasked-for close that "must be restarted".
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

	require.True(t, NatsManager.Conn().IsClosed(),
		"orderly shutdown left the NATS connection open: Stop was skipped, so Terminate was "+
			"refused from the Started state and never closed it")

	// The ClosedHandler runs on the client's callback goroutine, so wait for the line
	// rather than racing it. Its arrival is also what proves the handler ran at all —
	// without it, "no alarm was logged" would be vacuously true.
	require.Eventually(t, func() bool {
		return bytes.Contains([]byte(logged.String()), []byte("NATS connection closed during shutdown"))
	}, 5*time.Second, 10*time.Millisecond,
		"the ClosedHandler did not recognize the close as part of a shutdown; logs were:\n"+logged.String())
	require.NotContains(t, logged.String(), "CLOSED permanently",
		"a clean shutdown logged the permanent-close alarm")
	require.NotContains(t, logged.String(), "Error terminating the NATS manager",
		"Terminate was refused by the lifecycle state machine")
}

// WHERE the NATS manager is stopped matters as much as THAT it is stopped, and the
// constraint runs the other way from the obvious one.
//
// The leadership unwind is not merely bookkeeping that can be done in any order: it
// ends in evict(), which RELEASES THE LEASE — a KV write over the very connection
// Stop drains. Hoisting the Stop block above the unwind therefore makes the release
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
	require.True(t, NatsManager.Conn().IsClosed())
}
