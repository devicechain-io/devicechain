// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// syncBuffer collects log output written from the NATS client's own callback
// goroutine as well as the test's.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
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
	host, port := startEmbeddedNats(t)

	var logged syncBuffer
	prevLogger := zlog.Logger
	zlog.Logger = zerolog.New(&logged)
	t.Cleanup(func() { zlog.Logger = prevLogger })

	// The stopper reads package-level state; give it exactly the shape main() leaves
	// behind for a replica with no leadership loop and no HTTP server, and put it back
	// so no other test inherits it.
	prevMs, prevMgr, prevSrv := Microservice, NatsManager, httpServer
	prevCancel, prevInert := leadershipCancel, inertStop
	t.Cleanup(func() {
		Microservice, NatsManager, httpServer = prevMs, prevMgr, prevSrv
		leadershipCancel, inertStop = prevCancel, prevInert
	})
	httpServer, leadershipCancel, inertStop = nil, nil, nil

	Microservice = &core.Microservice{
		InstanceId:     "shutdown-test",
		FunctionalArea: "lwm2m-ingest",
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{
		Hostname: host,
		Port:     port,
	}

	ctx := context.Background()
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	require.NoError(t, NatsManager.Initialize(ctx))
	require.NoError(t, NatsManager.Start(ctx))
	require.NotNil(t, NatsManager.Conn())
	require.False(t, NatsManager.Conn().IsClosed(), "connection should be live before shutdown")

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
