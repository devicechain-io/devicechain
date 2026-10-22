// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/service"
)

// TestStartsUnderItsDeclaredCatalog runs the service's REAL initialize and start callbacks
// over a Microservice marked Deployed (as core.NewMicroservice marks every production one),
// so the broker manager holds everything this service opens to what core/streams and core/kv
// declare for "lwm2m-ingest". Every other test here builds a Microservice with Deployed unset,
// which bypasses the catalog: a gap would crash-loop the pod with CI green.
//
// It covers, through the production functions: the inbound-events writer and the
// command-responses writer and the device-commands reader (afterMicroserviceInitialized,
// main.go), the dead-letter recorder core/service installs, and the leadership lease
// (afterMicroserviceStarted -> NewDistributedLease -> runLeadership -> Acquire, which opens
// the lease bucket). Waiting for the transport to be serving also covers the leader-only
// dispatcher's run over the command reader.
func TestStartsUnderItsDeclaredCatalog(t *testing.T) {
	identity := uniqueIdentity("lwm2m-ingest-catalog")
	// The broker holds the area's login to exactly the permissions natsauth generates for it, so
	// this is also the area's permission test: the real wiring runs against it and the broker's
	// log of refusals is asserted empty at the end.
	broker := permtest.Start(t, identity)

	prevMs, prevCfg, prevMgr, prevSvc := Microservice, Configuration, NatsManager, Svc
	prevCreds, prevWriter, prevReader, prevResp := Credentials, Writer, CommandReader, ResponseWriter
	prevLease, prevGates := Lease, safetyGates
	prevCancel, prevDone, prevInert := leadershipCancel, leadershipDone, inertStop
	prevPort, prevFail := service.ProbesPort, failProcess
	t.Cleanup(func() {
		Microservice, Configuration, NatsManager, Svc = prevMs, prevCfg, prevMgr, prevSvc
		Credentials, Writer, CommandReader, ResponseWriter = prevCreds, prevWriter, prevReader, prevResp
		Lease, safetyGates = prevLease, prevGates
		leadershipCancel, leadershipDone, inertStop = prevCancel, prevDone, prevInert
		service.ProbesPort, failProcess = prevPort, prevFail
	})
	leadershipCancel, leadershipDone, inertStop = nil, nil, nil
	service.ProbesPort = 0

	// failProcess would end the test binary; a catalog refusal in a lease or term path must
	// surface as a failure of this test instead.
	failed := make(chan error, 1)
	failProcess = func(err error) {
		select {
		case failed <- err:
		default:
		}
	}

	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	udpPort := udp.LocalAddr().(*net.UDPAddr).Port
	require.NoError(t, udp.Close())

	const pskEnv = "DC_CATALOG_BOOT_LWM2M_PSK"
	t.Setenv(pskEnv, base64.StdEncoding.EncodeToString([]byte(strings.Repeat("p", 16))))

	Microservice = &core.Microservice{
		InstanceId:     identity,
		FunctionalArea: "lwm2m-ingest",
		Deployed:       true,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.MicroserviceConfigurationRaw = []byte(fmt.Sprintf(`{
	  "listen": {"host": "127.0.0.1", "port": %d},
	  "security": {"identities": [{"identity": "id1", "pskEnv": %q, "tenant": "acme", "externalId": "dev-1"}]}
	}`, udpPort, pskEnv))
	infra := &Microservice.InstanceConfiguration.Infrastructure
	infra.Nats = broker.NatsConfiguration("lwm2m-ingest")
	infra.ServiceAuth.Secret = "shh"
	// Peer services nothing listens on: the term build treats them as best-effort reads.
	infra.DeviceManagement.Hostname, infra.DeviceManagement.Port = "127.0.0.1", 1
	infra.DeviceState.Hostname, infra.DeviceState.Port = "127.0.0.1", 1
	infra.CommandDelivery.Hostname, infra.CommandDelivery.Port = "127.0.0.1", 1
	infra.UserManagement.Hostname, infra.UserManagement.Port = "127.0.0.1", 1

	ctx := context.Background()
	require.NoError(t, afterMicroserviceInitialized(ctx),
		"initialize under the catalog (Svc.Initialize, inbound-events + command-responses writers, device-commands reader)")
	require.NotNil(t, NatsManager)
	require.NotNil(t, Writer, "inbound-events writer")
	require.NotNil(t, CommandReader, "device-commands reader")
	require.NotNil(t, ResponseWriter, "command-responses writer")

	require.NoError(t, afterMicroserviceStarted(ctx), "start under the catalog (Svc.Start, the leadership lease)")
	t.Cleanup(func() { _ = beforeMicroserviceStopped(context.Background()) })
	require.NotNil(t, Lease, "the distributed lease (lease bucket)")

	// Acquire opens the lease bucket on first use, inside the leadership loop; serving means it
	// was acquired and the term (including the command dispatcher over the reader) was built.
	require.Eventually(t, func() bool {
		select {
		case err := <-failed:
			t.Fatalf("the service ended its own process under the catalog: %v", err)
		default:
		}
		return testutil.ToFloat64(leaderGauge) == 1 && testutil.ToFloat64(servingGauge) == 1
	}, 30*time.Second, 50*time.Millisecond,
		"the leadership lease was never acquired, or its term never reached serving, under the catalog (runLeadership, main.go)")
	broker.RequireNoViolations(t)
}
