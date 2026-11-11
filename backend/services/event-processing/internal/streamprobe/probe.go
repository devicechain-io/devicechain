// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package streamprobe supplies the same embedded broker and message census to isolation tests
// and their deliberately publishing negative control. It is test support, not service wiring.
package streamprobe

import (
	"context"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

var Suffixes = []string{streams.ResolvedEvents, streams.DerivedEvents, streams.AlarmEvents, streams.RaiseAlarm, streams.DeviceCommands, streams.ConnectorDispatch}

type Probe struct {
	Manager  *messaging.NatsManager
	Writers  map[string]messaging.MessageWriter
	instance string
}

func New(t testing.TB) *Probe {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), JetStreamMaxStore: 256 << 20, JetStreamMaxMemory: 64 << 20, NoLog: true, NoSigs: true})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second))
	t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	require.NoError(t, err)
	ms := &core.Microservice{InstanceId: "reviewprobe", FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
	ms.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
	ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytes = 16 << 20
	ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytesCold = 4 << 20
	nm := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
	require.NoError(t, nm.Initialize(context.Background()))
	require.NoError(t, nm.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, nm.Stop(ctx))
		nm.Conn().Close()
	})
	p := &Probe{Manager: nm, Writers: map[string]messaging.MessageWriter{}, instance: ms.InstanceId}
	for _, suffix := range Suffixes {
		w, err := nm.NewWriter(suffix)
		require.NoError(t, err)
		p.Writers[suffix] = w
	}
	return p
}
func (p *Probe) Counts(t testing.TB) map[string]uint64 {
	t.Helper()
	js, err := p.Manager.Conn().JetStream()
	require.NoError(t, err)
	out := map[string]uint64{}
	for _, suffix := range Suffixes {
		info, err := js.StreamInfo(messaging.StreamName(p.instance, suffix))
		require.NoError(t, err)
		out[suffix] = info.State.Msgs
	}
	return out
}
