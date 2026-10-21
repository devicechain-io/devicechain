// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/service"
)

// TestStartsUnderItsDeclaredCatalog runs the service's REAL initialize and start callbacks
// over a Microservice marked Deployed (as core.NewMicroservice marks every production one),
// so the broker manager holds everything this service opens to what core/streams and core/kv
// declare for "sparkplug-ingest". Every other test here builds a Microservice with Deployed
// unset, which bypasses the catalog: a gap would crash-loop the pod with CI green.
//
// It covers, through the production functions: the synchronous inbound-events writer
// (afterMicroserviceInitialized), each source's WriterSparkplug async inbound-events writer
// (publisherFactory), the dead-letter recorder core/service installs, and the leadership lease
// (afterMicroserviceStarted -> startLeadership -> runLeadership -> Acquire, which opens the
// lease bucket). The source's broker is unreachable, which degrades that source only.
func TestStartsUnderItsDeclaredCatalog(t *testing.T) {
	host, port := startEmbeddedNats(t)

	prevMs, prevCfg, prevMgr, prevSvc, prevManager := Microservice, Configuration, NatsManager, Svc, Manager
	prevLease, prevWriters, prevGates, prevGauge := Lease, PublishWriters, safetyGates, leaderGauge
	prevCancel, prevDone, prevPort := leadershipCancel, leadershipDone, service.ProbesPort
	t.Cleanup(func() {
		Microservice, Configuration, NatsManager, Svc, Manager = prevMs, prevCfg, prevMgr, prevSvc, prevManager
		Lease, PublishWriters, safetyGates, leaderGauge = prevLease, prevWriters, prevGates, prevGauge
		leadershipCancel, leadershipDone, service.ProbesPort = prevCancel, prevDone, prevPort
	})
	leadershipCancel, leadershipDone, PublishWriters = nil, nil, nil
	service.ProbesPort = 0

	Microservice = &core.Microservice{
		InstanceId:     fmt.Sprintf("sparkplug-ingest-catalog-%d", identitySeq.Add(1)),
		FunctionalArea: "sparkplug-ingest",
		Deployed:       true,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.MicroserviceConfigurationRaw = []byte(`{
	  "sources": [{"tenant": "acme", "broker": {"url": "tcp://127.0.0.1:1"}}]
	}`)
	infra := &Microservice.InstanceConfiguration.Infrastructure
	infra.Nats.Hostname = host
	infra.Nats.Port = port
	infra.ServiceAuth.Secret = "shh"
	// Peer services nothing listens on: the ingest path only builds clients to them.
	infra.DeviceManagement.Hostname, infra.DeviceManagement.Port = "127.0.0.1", 1
	infra.DeviceState.Hostname, infra.DeviceState.Port = "127.0.0.1", 1
	infra.UserManagement.Hostname, infra.UserManagement.Port = "127.0.0.1", 1

	ctx := context.Background()
	require.NoError(t, afterMicroserviceInitialized(ctx),
		"initialize under the catalog (Svc.Initialize, inbound-events writer, per-source WriterSparkplug async writer)")
	require.NotNil(t, NatsManager)
	require.Len(t, PublishWriters, 1, "the source's inbound-events async writer (WriterSparkplug)")

	require.NoError(t, afterMicroserviceStarted(ctx), "start under the catalog (Svc.Start, the leadership lease)")
	t.Cleanup(func() { _ = beforeMicroserviceStopped(context.Background()) })
	require.NotNil(t, Lease, "the distributed lease (lease bucket)")

	// Acquire opens the lease bucket on first use, inside the leadership loop; the leader
	// gauge going up means it was acquired.
	require.Eventually(t, func() bool { return testutil.ToFloat64(leaderGauge) == 1 },
		30*time.Second, 50*time.Millisecond,
		"the leadership lease was never acquired under the catalog (runLeadership, main.go)")
}
