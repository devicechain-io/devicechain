// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

// 🔑 WHY THIS FILE EXISTS. Until this service moved onto core/service no test reached any
// of its four lifecycle callbacks, so the order its stopper depends on was pinned in
// lwm2m-ingest alone — the near-twin that shares the constraint. A hoist of Svc.Stop above
// the leadership unwind here, a dropped Svc.Terminate, or a starter that never called
// Svc.Start all passed every test in this package. These drive the real callbacks.

// identitySeq keeps each test's instance id — and so its lease bucket — to itself.
var identitySeq atomic.Int64

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

// installBrokerMicroservice installs a Microservice pointed at an embedded broker, and
// restores every global the callbacks read when the test ends.
func installBrokerMicroservice(t *testing.T) {
	t.Helper()
	host, port := startEmbeddedNats(t)

	prevMs, prevMgr, prevSvc, prevCfg := Microservice, NatsManager, Svc, Configuration
	prevHost, prevCancel, prevDone := Manager, leadershipCancel, leadershipDone
	prevPort := service.ProbesPort
	t.Cleanup(func() {
		Microservice, NatsManager, Svc, Configuration = prevMs, prevMgr, prevSvc, prevCfg
		Manager, leadershipCancel, leadershipDone = prevHost, prevCancel, prevDone
		service.ProbesPort = prevPort
	})
	Manager, leadershipCancel, leadershipDone = nil, nil, nil
	service.ProbesPort = 0

	identity := fmt.Sprintf("sparkplug-ingest-%d", identitySeq.Add(1))
	Microservice = &core.Microservice{
		InstanceId:     identity,
		FunctionalArea: identity,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{
		Hostname: host,
		Port:     port,
	}
}

// startRunningManager brings a real NatsManager up to Started and wraps it with
// service.FromManagers, so beforeMicroserviceStopped drives core/service's real sequence.
func startRunningManager(t *testing.T) {
	t.Helper()
	installBrokerMicroservice(t)

	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	require.NoError(t, NatsManager.Initialize(context.Background()))
	require.NoError(t, NatsManager.Start(context.Background()))
	require.False(t, NatsManager.Conn().IsClosed(), "connection should be live before shutdown")
	Svc = service.FromManagers(Microservice, service.Managers{Nats: NatsManager})

	// A failed require would otherwise leave this client reconnecting forever against a
	// broker that has been shut down.
	mgr := NatsManager
	t.Cleanup(func() {
		if c := mgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
}

// An orderly shutdown must stop AND terminate the broker: Terminate is legal only from
// Stopped, so a stopper that skips the stop has its terminate refused and leaves the
// connection open. core/service makes both calls now; this is what shows the callbacks
// still reach it.
func TestOrderlyShutdownStopsAndTerminatesTheNatsManager(t *testing.T) {
	startRunningManager(t)
	ctx := context.Background()

	require.NoError(t, beforeMicroserviceStopped(ctx))
	require.NoError(t, afterMicroserviceTerminated(ctx))
	require.True(t, NatsManager.Conn().IsClosed(),
		"orderly shutdown left the NATS connection open")
	require.ErrorContains(t, Svc.Terminate(ctx), "Terminated",
		"the terminator did not terminate the Service: a second terminate should be refused")
}

// 🔴 WHERE the broker stops matters as much as THAT it stops. The leadership unwind ends
// in a lease RELEASE — a KV write over the very connection Svc.Stop drains — so hoisting
// Svc.Stop above the unwind makes the release fail, and a standby then waits out a full
// lease TTL before it can take over. lwm2m-ingest pins the same constraint the same way.
//
// The unwind here stands in for runLeadership's: it releases a real lease over the real
// connection, which is the only part of that unwind that touches NATS.
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
			"lease TTL: the broker was stopped before the unwind ran")
	require.NoError(t, afterMicroserviceTerminated(ctx))
	require.True(t, NatsManager.Conn().IsClosed())
}

// The start callback is what brings up the probe surface and the broker, and it is driven
// here end to end through the Service the initializer assembles: a starter that stopped
// calling Svc.Start would leave the pod with no probes and no durable writer while
// returning nil.
//
// It takes the no-sources shape, which starts the Manager directly and takes no lease, so
// the test needs nothing but a broker.
func TestTheStartPhaseBringsUpTheBrokerAndTheProbes(t *testing.T) {
	installBrokerMicroservice(t)
	ctx := context.Background()

	Configuration = &config.SparkplugConfiguration{}
	Manager = host.NewManager(nil)
	Svc = service.New(Microservice, service.Spec{
		Nats: &service.NatsSpec{OnCreate: func(*messaging.NatsManager) error { return nil }},
	})
	require.NoError(t, Svc.Initialize(ctx))
	NatsManager = Svc.Nats

	require.NoError(t, afterMicroserviceStarted(ctx))
	t.Cleanup(func() {
		_ = beforeMicroserviceStopped(ctx)
		_ = afterMicroserviceTerminated(ctx)
	})

	require.False(t, NatsManager.Conn() == nil || NatsManager.Conn().IsClosed(),
		"the start phase did not start the broker")
	resp, err := http.Get("http://" + Svc.HttpAddr() + "/readyz")
	require.NoError(t, err, "the start phase did not bring up the probe surface")
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"/readyz is not 200 after start: readiness was not opened")
}
