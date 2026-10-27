// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/devicechain-io/dc-sparkplug-ingest/codec"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// END TO END: a REAL host.Client and a REAL Dispatcher over the embedded MQTT broker, with a
// scripted edge node on the other side. A write is published as a DCMD, the node reports the value
// in a DDATA, and the command is confirmed from that report; a rebirth is published as an NCMD, the
// node answers with the same-bdSeq NBIRTH, and the rebirth is confirmed from it. Nothing between
// the broker and the response is a fake except the collaborators that need a database or another
// service (device resolution, command-delivery's claim, the response stream).

type e2eIngester struct{}

func (e2eIngester) PrepareIngest(context.Context, string, host.IngestPolicy, string, []host.Sample) (*host.Publication, error) {
	return nil, nil
}

func (e2eIngester) PreparePresence(_ context.Context, tenant string, _ host.IngestPolicy, _ []host.PresenceEvent) (*host.Publication, error) {
	return &adapter.Publication{Tenant: tenant, Messages: []messaging.Message{{Value: []byte("presence")}}, Delivered: func() {}}, nil
}

type e2ePublisher struct{}

func (e2ePublisher) Submit(_ context.Context, _ []messaging.Message, done func(error)) { done(nil) }
func (e2ePublisher) Settle(context.Context) error                                      { return nil }

type e2eLimiter struct{}

func (e2eLimiter) AdmitSamples(_ string, n int) int { return n }

type e2eResolver struct{}

func (e2eResolver) ResolveToken(_ context.Context, _, ext string, _ host.IngestPolicy) (string, bool, error) {
	switch ext {
	case "plant-a/node-3":
		return "node-3", true, nil
	case "plant-a/node-3/pump-1":
		return "pump-1", true, nil
	}
	return "", false, nil
}

func encodeP(t *testing.T, p *sppb.Payload) []byte {
	t.Helper()
	b, err := codec.Encode(p)
	require.NoError(t, err)
	return b
}

func TestARealClientAndARealDispatcherConfirmAWriteAndARebirthOverTheBroker(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, NoLog: true, NoSigs: true, ServerName: "dl-e2e",
		StoreDir: dctest.JetStreamStoreDir(t), MQTT: natsserver.MQTTOpts{Host: "127.0.0.1", Port: -1},
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(15*time.Second))
	t.Cleanup(srv.Shutdown)
	url := fmt.Sprintf("tcp://127.0.0.1:%d", dctest.MQTTPort(t, srv))

	// The real Client.
	src := config.SparkplugSource{Tenant: "acme", HostId: "h1", Groups: []string{"plant-a"}, AutoRegister: true,
		DeviceTypeToken: "sp-node", Commands: config.SourceCommands{
			Enabled: true, HostRole: config.HostRolePrimary, WritableMetrics: []string{"Run"},
			ConfirmWindowSeconds: 120, RebootWindowSeconds: 600, ClockSkewSeconds: 30, PublishPerSecond: 1000, Burst: 1000,
		}}
	client := host.NewClient(src, host.Broker{URL: url, ClientID: "dl-e2e-host"}, e2eIngester{}, e2eLimiter{}, time.Now, host.Metrics{})
	client.SetPublisher(e2ePublisher{})
	client.SetTokenResolver(e2eResolver{})

	// The real Dispatcher over it (the collaborators that would need other services are the rig's).
	r := newRig(t)
	r.d = New(nil, r.responses, r.fetcher, r.claimer, r.parker, r.store, []Source{client}, Metrics{Commands: r.commands, Awaiting: r.awaiting},
		Options{Workers: 2, SweepEvery: 50 * time.Millisecond})
	ctx, _ := r.term(t)

	client.Connect()
	t.Cleanup(func() { client.Stop() })
	require.Eventually(t, client.SessionUp, 20*time.Second, 10*time.Millisecond, "the Host never announced ONLINE")

	// The scripted edge node.
	var rebirths atomic.Int32
	opts := mqtt.NewClientOptions().AddBroker(url).SetClientID("dl-e2e-node")
	node := mqtt.NewClient(opts)
	require.True(t, node.Connect().WaitTimeout(15*time.Second))
	t.Cleanup(func() { node.Disconnect(100) })
	pub := func(topic string, p *sppb.Payload) {
		tok := node.Publish(topic, 1, false, encodeP(t, p))
		require.True(t, tok.WaitTimeout(15*time.Second))
		require.NoError(t, tok.Error())
	}
	nbirth := func() {
		pub("spBv1.0/plant-a/NBIRTH/node-3", &sppb.Payload{Seq: proto.Uint64(0), Metrics: []*sppb.Payload_Metric{
			{Name: proto.String("bdSeq"), Value: &sppb.Payload_Metric_LongValue{LongValue: 7}},
			{Name: proto.String("Node Control/Rebirth"), Datatype: proto.Uint32(11), Value: &sppb.Payload_Metric_BooleanValue{BooleanValue: false}},
		}})
	}
	var seq atomic.Uint64
	seq.Store(1)
	// Subscribe to the commands BEFORE the births: the node must hear them.
	//subconfirm:ok the subscriptions are confirmed on the next lines
	tokD := node.Subscribe("spBv1.0/plant-a/DCMD/node-3/pump-1", 1, func(_ mqtt.Client, m mqtt.Message) {
		p, err := codec.Decode(m.Payload())
		if err != nil || len(p.GetMetrics()) != 1 || p.GetMetrics()[0].GetAlias() != 50 || !p.GetMetrics()[0].GetBooleanValue() {
			return
		}
		// The node obeys, and reports the new value.
		pub("spBv1.0/plant-a/DDATA/node-3/pump-1", &sppb.Payload{Seq: proto.Uint64(seq.Add(1)), Metrics: []*sppb.Payload_Metric{
			{Alias: proto.Uint64(50), Value: &sppb.Payload_Metric_BooleanValue{BooleanValue: true}}}})
	})
	require.True(t, tokD.WaitTimeout(15*time.Second))
	//subconfirm:ok confirmed on the next line
	tokN := node.Subscribe("spBv1.0/plant-a/NCMD/node-3", 1, func(_ mqtt.Client, m mqtt.Message) {
		p, err := codec.Decode(m.Payload())
		if err != nil || len(p.GetMetrics()) != 1 || p.GetMetrics()[0].GetName() != "Node Control/Rebirth" {
			return
		}
		rebirths.Add(1)
		nbirth() // the same bdSeq: the node re-announcing the session it is in
	})
	require.True(t, tokN.WaitTimeout(15*time.Second))

	nbirth()
	pub("spBv1.0/plant-a/DBIRTH/node-3/pump-1", &sppb.Payload{Seq: proto.Uint64(1), Metrics: []*sppb.Payload_Metric{
		{Name: proto.String("Run"), Alias: proto.Uint64(50), Datatype: proto.Uint32(11), Value: &sppb.Payload_Metric_BooleanValue{BooleanValue: false}}}})
	require.Eventually(t, func() bool { return client.IndexSize() == 2 }, 20*time.Second, 10*time.Millisecond,
		"the births never put the node and its device in the index")

	// A write: DCMD out, DDATA back, confirmed.
	w, _ := r.msg("acme", "pump-1", "w", CommandWrite, writeCmd("Run", true), nil)
	r.d.live(ctx, w)
	res := r.responseTo(t, "w")
	assert.True(t, res.env.Success, "the write was not confirmed: %v", errorOf(res))
	ev := evidenceOf(t, res)
	assert.Equal(t, "DDATA", ev.Via)
	assert.JSONEq(t, `true`, string(ev.Value))

	// A rebirth: NCMD out, the same-bdSeq NBIRTH back, confirmed.
	rb, _ := r.msg("acme", "node-3", "rb", CommandRebirth, ``, nil)
	r.d.live(ctx, rb)
	res = r.responseTo(t, "rb")
	assert.True(t, res.env.Success, "the rebirth was not confirmed: %v", errorOf(res))
	assert.Equal(t, "NBIRTH", evidenceOf(t, res).Via)
	assert.GreaterOrEqual(t, int(rebirths.Load()), 1)
	require.Eventually(t, func() bool { return r.store.len() == 0 }, confirmWait, 10*time.Millisecond)
}
