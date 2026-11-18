// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	dmprocessor "github.com/devicechain-io/dc-device-management/processor"
	dsconfig "github.com/devicechain-io/dc-device-state/config"
	dsmodel "github.com/devicechain-io/dc-device-state/model"
	dsprocessor "github.com/devicechain-io/dc-device-state/processor"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-sources/adapter"
	espresence "github.com/devicechain-io/dc-event-sources/presence"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	dctest "github.com/devicechain-io/dc-microservice/test"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// Only transport is substituted: the inbound stream is a channel, and the resolved
// stream uses the existing finite reader. All presence mappings and both consumers
// run their production paths. SQLite proves the chain, not Postgres locking.
type presenceAcceptanceWriter struct{ messages chan messaging.Message }

func (w *presenceAcceptanceWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	for _, msg := range msgs {
		select {
		case w.messages <- msg:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func TestPresenceProjectionAndDetectAcceptance(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	dir := dctest.JetStreamStoreDir(t)
	conf := filepath.Join(dir, "nats.conf")
	require.NoError(t, os.WriteFile(conf, []byte(fmt.Sprintf(`
listen: "127.0.0.1:-1"
jetstream { store_dir: %q }
mqtt { listen: "127.0.0.1:-1" }
accounts {
 APP: { jetstream: enabled, users: [{user: device, password: device}] }
 SYS: { users: [{user: system, password: system}] }
}
system_account: SYS
`, dir)), 0o600))
	opts, err := natsserver.ProcessConfigFile(conf)
	require.NoError(t, err)
	opts.NoLog, opts.NoSigs = true, true
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go srv.Start()
	t.Cleanup(srv.Shutdown)
	require.True(t, srv.ReadyForConnections(20*time.Second))
	nc, err := nats.Connect(srv.ClientURL(), nats.UserInfo("system", "system"))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	inbound := &presenceAcceptanceWriter{messages: make(chan messaging.Message, 8)}
	emitter := adapter.NewEmitter(inbound, time.Now, "mq", false)
	tap := espresence.NewTap("acceptance", "mqtt1", emitter,
		func(string, string, time.Time, bool) bool { return true }, espresence.Metrics{})
	stop, err := tap.Subscribe(nc)
	require.NoError(t, err)
	t.Cleanup(stop)
	require.NoError(t, nc.Flush())

	db := putest.NewSQLiteDB(t, &dsmodel.DeviceState{})
	projection := dsmodel.NewApi(&rdb.RdbManager{Database: db})
	compiled, err := rules0.Compile(rules0.Rule{ID: "acme/offline", Name: "offline", Type: rules0.TypeConnectivity}, rules0.Limits{})
	require.NoError(t, err)
	g := newGapRigWith(t, &fakeReplayOpener{}, runtime.NewRuleRegistry([]runtime.ScopedRule{
		{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: compiled},
	}))

	connect := func() mqtt.Client {
		cl := mqtt.NewClient(mqtt.NewClientOptions().
			AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", dctest.MQTTPort(t, srv))).
			SetClientID("acceptance:acme:sensor-001").SetUsername("device").SetPassword("device").SetAutoReconnect(false))
		token := cl.Connect()
		require.True(t, token.WaitTimeout(20*time.Second))
		require.NoError(t, token.Error())
		t.Cleanup(func() { cl.Disconnect(50) })
		return cl
	}
	// Projection and DETECT are independent consumers of the SAME resolved bytes;
	// neither is fed a reconstructed presence claim from the other consumer's result.
	consume := func(seq uint64, active bool, detections int) {
		t.Helper()
		var raw messaging.Message
		select {
		case raw = <-inbound.messages:
		case <-time.After(20 * time.Second):
			t.Fatal("broker presence did not reach the inbound stream")
		}
		unresolved, err := esproto.UnmarshalUnresolvedEvent(raw.Value)
		require.NoError(t, err)
		payload, err := (&dmprocessor.EventResolver{}).ResolveStateChangeEventPayload(ctx, nil, nil, unresolved)
		require.NoError(t, err)
		body, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{
			Source: unresolved.Source, SourceDeviceToken: unresolved.Device,
			EventType: unresolved.EventType, OccurredTime: unresolved.OccurredTime,
			ProcessedTime: unresolved.OccurredTime, ProfileVersionToken: "p@1", Payload: payload,
		})
		require.NoError(t, err)
		ack := &fakeAck{acked: make(chan struct{}, 1)}
		msg := messaging.NewConsumedMessage(testSubject, body, 0, nil, ack)
		msg.StreamSeq = seq
		sp := dsprocessor.NewStateProcessor(&core.Microservice{FunctionalArea: "device-state"},
			&fakeReader{results: []readResult{{msg: msg}}}, core.LifecycleCallbacks{}, projection, nil, nil,
			dsprocessor.WithProjection(dsconfig.ProjectionConfiguration{Writers: 1, MaxBatch: 1}))
		require.NoError(t, sp.ExecuteInitialize(ctx))
		defer func() { require.NoError(t, sp.ExecuteStop(ctx)) }()
		require.NoError(t, sp.ExecuteStart(ctx))
		select {
		case <-ack.acked:
		case <-time.After(20 * time.Second):
			t.Fatal("projection did not acknowledge presence")
		}
		states, err := projection.DeviceStatesByDeviceToken(ctx, []string{"sensor-001"})
		require.NoError(t, err)
		require.Len(t, states, 1)
		require.Equal(t, active, states[0].Active)
		require.Equal(t, "ASSERTED", states[0].PresenceSource)
		require.NotZero(t, states[0].SessionId)
		detectAck := &fakeAck{}
		msg = messaging.NewConsumedMessage(testSubject, body, 0, nil, detectAck)
		msg.StreamSeq = seq
		registerPending(t, detectAck, seq)
		g.rp.handle(msg)
		g.commit(t)
		require.Len(t, g.rec.r.events, detections)
	}

	cl := connect()
	consume(1, true, 0)
	cl.Disconnect(50)
	consume(2, false, 1)
	require.Equal(t, "raised", g.rec.r.events[0].Edge)
	connect()
	consume(3, true, 2)
	require.Equal(t, "resolved", g.rec.r.events[1].Edge)
	for _, detection := range g.rec.r.events {
		require.Equal(t, "acme", detection.Tenant)
		require.Equal(t, "sensor-001", detection.Series)
		require.Equal(t, "acme/offline", detection.RuleID)
	}
}
