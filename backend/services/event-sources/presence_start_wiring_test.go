// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"
	"time"

	esconfig "github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These drive startBrokerPresence itself — the production caller of sysConnGuard — against
// the embedded auth broker. presence_closed_test.go pins the guard and the dial; a
// startBrokerPresence that dialled without the guard, or never armed it, passes every test
// there while bringing back the defect: the connection closed for good and the pod stayed
// live with presence frozen.

// startPresence runs startBrokerPresence as the Postprocess phase would, against b, with a
// stand-in user-management for the reconciler's calls. It returns the runtime the tap left
// behind, and stops it at cleanup if the test has not.
func startPresence(t *testing.T, b *authBroker) (*core.Microservice, *presenceRuntime) {
	t.Helper()
	savedConfig, savedMs, savedGateway := Configuration, Microservice, GatewaySourceId
	savedInbound, savedPresence := InboundEventsWriter, brokerPresence
	t.Cleanup(func() {
		stopBrokerPresence()
		Configuration, Microservice, GatewaySourceId = savedConfig, savedMs, savedGateway
		InboundEventsWriter, brokerPresence = savedInbound, savedPresence
	})

	um := newFakeUM(t)
	host, port := um.infra(t)
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	infra := &Microservice.InstanceConfiguration.Infrastructure
	infra.ServiceAuth.Secret = "shh"
	infra.UserManagement.Hostname, infra.UserManagement.Port = host, port
	infra.DeviceState.Hostname, infra.DeviceState.Port = host, port
	infra.Nats = b.cfg
	initializeMetrics()
	InboundEventsWriter = nopWriter{}
	Configuration = &esconfig.EventSourcesConfiguration{}
	Configuration.ApplyDefaults()
	GatewaySourceId = "mqtt1"
	brokerPresence = nil

	startBrokerPresence(context.Background())
	rt := brokerPresence
	require.NotNil(t, rt, "the tap must be running against a broker that accepts the credential")
	require.NotNil(t, rt.conn)
	require.NotNil(t, rt.guard, "a running tap's connection is guarded")
	return Microservice, rt
}

// The defect, through its production caller: the broker stops accepting the system-account
// credential, nats.go closes the tap's connection for good, and liveness must fail.
func TestStartBrokerPresenceRestartsThePodOnAnUnrequestedClose(t *testing.T) {
	b := runAuthBroker(t, "one")
	ms, rt := startPresence(t, b)
	require.NotNil(t, rt.conn.Opts.ClosedCB, "the tap's startup dial must install the guard's handler")
	require.True(t, rt.guard.armed.Load(), "a running tap must have armed its guard")
	require.NoError(t, ms.Live(), "a running tap starts live")

	b.revoke(t)
	waitHandled(t, rt.guard, 1)

	require.True(t, rt.conn.IsClosed())
	err := ms.Live()
	require.Error(t, err, "a permanently closed presence connection must fail liveness")
	assert.Contains(t, err.Error(), "system-account (presence) connection closed permanently")
}

// The counterweight: the tap's own shutdown closes the same connection, and the pod stays
// live.
func TestStoppingAStartedTapDoesNotMarkNotLive(t *testing.T) {
	b := runAuthBroker(t, "one")
	ms, rt := startPresence(t, b)

	stopBrokerPresence()
	waitHandled(t, rt.guard, 1)

	require.True(t, rt.conn.IsClosed(), "stopBrokerPresence must close the connection")
	require.NoError(t, ms.Live(), "the tap's own shutdown must not fail liveness")
}

// The recheck through its caller: a credential the broker refuses answers "not reachable"
// and never touches liveness; one it accepts answers "reachable".
func TestSystemAccountReachableNeverTouchesLiveness(t *testing.T) {
	ms := withLiveness(t)
	b := runAuthBroker(t, "one")

	refused := b.cfg
	refused.Auth.SysPassword = "wrong"
	assert.False(t, systemAccountReachable(context.Background(), refused))
	// The library closes a refused connection on its own goroutine; give a handler, had
	// one been installed and armed, time to have run before reading liveness.
	time.Sleep(200 * time.Millisecond)
	require.NoError(t, ms.Live(), "a refused credential on the recheck must not fail liveness")

	assert.True(t, systemAccountReachable(context.Background(), b.cfg))
	require.NoError(t, ms.Live())
}
