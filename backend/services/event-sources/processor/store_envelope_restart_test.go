// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// restartEnvelopePublisher uses real JetStream duplicate acknowledgements and
// read-backs. Both interfaces share the connection replaced after the restart.
type restartEnvelopePublisher struct{ js nats.JetStreamContext }

type restartWALAck struct{ msg *nats.Msg }

func (a restartWALAck) Ack() error { return a.msg.AckSync() }

func (p *restartEnvelopePublisher) Publish(ctx context.Context, m messaging.Message) (messaging.PubAckInfo, error) {
	tenant, _ := core.TenantFromContext(ctx)
	nm := &nats.Msg{Subject: "inst-1." + tenant + ".inbound-events", Data: m.Value, Header: nats.Header{}}
	for k, v := range m.Headers {
		nm.Header.Set(k, v)
	}
	nm.Header.Set(nats.MsgIdHdr, m.DedupID)
	a, err := p.js.PublishMsg(nm, nats.Context(ctx))
	if err != nil {
		return messaging.PubAckInfo{}, err
	}
	return messaging.PubAckInfo{Sequence: a.Sequence, Duplicate: a.Duplicate}, nil
}

func (p *restartEnvelopePublisher) Get(ctx context.Context, _ string, seq uint64) (messaging.StoredMessage, error) {
	m, err := p.js.GetMsg("inbound", seq, nats.Context(ctx))
	if err != nil {
		return messaging.StoredMessage{}, err
	}
	h := map[string]string{}
	for k := range m.Header {
		h[k] = m.Header.Get(k)
	}
	return messaging.StoredMessage{Sequence: m.Sequence, Subject: m.Subject, Value: m.Data, Headers: h}, nil
}

type restartEnvelopeWriter struct{ *restartEnvelopePublisher }

func (p restartEnvelopeWriter) Publish(ctx context.Context, m messaging.Message, done func(error)) {
	_, err := p.restartEnvelopePublisher.Publish(ctx, m)
	done(err)
}
func (p restartEnvelopeWriter) PublishAcked(ctx context.Context, m messaging.Message, done func(messaging.PubAckInfo, error)) {
	a, err := p.restartEnvelopePublisher.Publish(ctx, m)
	done(a, err)
}
func (restartEnvelopeWriter) Fail(err error, done func(error)) { done(err) }
func (restartEnvelopeWriter) Draining()                        {}
func (restartEnvelopeWriter) Close()                           {}

// Withhold the original WAL acks after committing only part of a bucket, restart
// the embedded MQTT broker on its existing store, then regroup real redeliveries
// with unpublished members. Check every identity, rather than a total that could
// hide a missing member behind an extra copy. Delivery counters can reset across
// an embedded-broker restart: every identity must survive, and exactly-once
// recovery is required when all original deliveries retain their retry marker.
func TestStoreEnvelopesRecoverPartialPublicationAcrossBrokerRestart(t *testing.T) {
	for _, retainEngine := range []bool{false, true} {
		t.Run(fmt.Sprintf("retain-engine=%t", retainEngine), func(t *testing.T) {
			opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true,
				StoreDir: dctest.JetStreamStoreDir(t), NoLog: true, NoSigs: true,
				MQTT: natsserver.MQTTOpts{Host: "127.0.0.1", Port: -1}}
			var srv *natsserver.Server
			var nc *nats.Conn
			p := &restartEnvelopePublisher{}
			start := func() {
				var err error
				srv, err = natsserver.NewServer(opts.Clone())
				require.NoError(t, err)
				go srv.Start()
				require.True(t, srv.ReadyForConnections(10*time.Second))
				opts.Port = srv.Addr().(*net.TCPAddr).Port
				opts.MQTT.Port = dctest.MQTTPort(t, srv)
				nc, err = nats.Connect(srv.ClientURL())
				require.NoError(t, err)
				p.js, err = nc.JetStream()
				require.NoError(t, err)
			}
			start()
			t.Cleanup(func() { nc.Close(); srv.Shutdown() })
			_, err := p.js.AddStream(&nats.StreamConfig{Name: messaging.MqttMessageStore,
				Subjects: []string{"$MQTT.msgs.>"}, Storage: nats.FileStorage,
				Retention: nats.InterestPolicy, Discard: nats.DiscardNew, MaxBytes: 4 << 20})
			require.NoError(t, err)
			ci, err := p.js.AddConsumer(messaging.MqttMessageStore, &nats.ConsumerConfig{
				Durable: "events", AckPolicy: nats.AckExplicitPolicy, AckWait: 200 * time.Millisecond})
			require.NoError(t, err)
			_, err = p.js.AddStream(&nats.StreamConfig{Name: "inbound", Subjects: []string{"inst-1.*.inbound-events"},
				Storage: nats.FileStorage, Duplicates: 2 * time.Minute})
			require.NoError(t, err)
			info, err := p.js.StreamInfo(messaging.MqttMessageStore)
			require.NoError(t, err)
			epoch := info.Created
			publish := func(from, to int) {
				c := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", opts.MQTT.Port)).
					SetClientID("wal-envelope-restart").SetCleanSession(true).SetAutoReconnect(false))
				tok := c.Connect()
				require.True(t, tok.WaitTimeout(10*time.Second))
				require.NoError(t, tok.Error())
				defer c.Disconnect(100)
				for i := from; i <= to; i++ {
					device := fmt.Sprintf("acme-%d", i)
					tok = c.Publish("inst-1/acme/devices/"+device+"/events", 1, false, envBody(device))
					require.True(t, tok.WaitTimeout(10*time.Second))
					require.NoError(t, tok.Error(), "MQTT PUBACK for %s", device)
				}
			}
			fetch := func(n int) []*nats.Msg {
				sub, err := p.js.PullSubscribe("", "events", nats.Bind(messaging.MqttMessageStore, "events"))
				require.NoError(t, err)
				defer sub.Unsubscribe()
				var out []*nats.Msg
				for len(out) < n {
					ms, err := sub.Fetch(n-len(out), nats.MaxWait(5*time.Second))
					require.NoError(t, err)
					out = append(out, ms...)
				}
				return out
			}
			es := storeEnvSource(t, newEnvBroker(), true)
			es.SetWriter(restartEnvelopeWriter{p})
			es.envelopes.Rounds = p
			ce, err := es.newCaptureEngine()
			require.NoError(t, err)
			defer ce.cancel()
			batch := func(ms []*nats.Msg, acknowledge bool) messaging.FormedBatch {
				b := messaging.FormedBatch{Tenant: "acme"}
				for _, nm := range ms {
					md, err := nm.Metadata()
					require.NoError(t, err)
					m := messaging.NewConsumedMessage(strings.TrimPrefix(nm.Subject, "$MQTT.msgs."), nm.Data, int(md.NumDelivered), nil, restartWALAck{nm})
					m.StreamSeq, m.AppendTime = md.Sequence.Stream, md.Timestamp
					m.DedupID = messaging.MqttMessageStoreDedupID("acme", epoch, m.StreamSeq)
					key, ok := es.envelopeKey("acme", m)
					require.True(t, ok)
					_, pos := splitPosition("acme", positionToken(m.DedupID, key, m.NumDelivered > 1, 0))
					event, _, err := NewJsonDecoder(map[string]string{}).Decode(m.Value, m.AppendTime)
					require.NoError(t, err)
					done := es.settler(m, "acme")
					if !acknowledge {
						done = func(err error) { require.NoError(t, err) }
					}
					pp := pendingPublish{ctx: core.WithTenant(context.Background(), "acme"),
						msg: messaging.Message{Value: pbDevice(event.Device)}, pos: pos, done: done}
					b.Members = append(b.Members, messaging.FormedMember{Bytes: pp.msg.Value, Token: pp})
				}
				return b
			}
			publish(1, 3)
			original := fetch(3)
			es.publishGroup(ce, batch(original[:2], false))
			ce.engine.Wait()
			nc.Close()
			srv.Shutdown()
			srv.WaitForShutdown()
			start()
			info, err = p.js.StreamInfo(messaging.MqttMessageStore)
			require.NoError(t, err)
			require.Equal(t, epoch, info.Created)
			require.EqualValues(t, 3, info.State.LastSeq)
			restored, err := p.js.ConsumerInfo(messaging.MqttMessageStore, "events")
			require.NoError(t, err)
			require.Equal(t, ci.Created, restored.Created, "the restart must retain the durable")
			publish(4, 4)
			again := fetch(4)
			allRedelivered := true
			for _, nm := range again {
				md, err := nm.Metadata()
				require.NoError(t, err)
				if md.Sequence.Stream <= 3 && md.NumDelivered <= 1 {
					allRedelivered = false
				}
			}
			if !retainEngine {
				ce.cancel()
				ce, err = es.newCaptureEngine()
				require.NoError(t, err)
				defer ce.cancel()
			}
			es.publishGroup(ce, batch(again, true))
			ce.engine.Wait()
			require.NoError(t, nc.Flush())
			require.Eventually(t, func() bool {
				ci, err := p.js.ConsumerInfo(messaging.MqttMessageStore, "events")
				return err == nil && ci.NumPending == 0 && ci.NumAckPending == 0
			}, 5*time.Second, 10*time.Millisecond)
			inbound, err := p.js.StreamInfo("inbound")
			require.NoError(t, err)
			counts := map[string]int{}
			for seq := uint64(1); seq <= inbound.State.LastSeq; seq++ {
				stored, err := p.js.GetMsg("inbound", seq)
				require.NoError(t, err)
				if stored.Header.Get(eventenvelope.HeaderName) == "" {
					counts[string(stored.Data[2:])]++
					continue
				}
				r, err := eventenvelope.Parse(stored.Data)
				require.NoError(t, err)
				for i := 0; i < r.Len(); i++ {
					body, err := r.Member(i)
					require.NoError(t, err)
					counts[string(body[2:])]++
				}
			}
			require.Len(t, counts, 4)
			for i := 1; i <= 4; i++ {
				device := fmt.Sprintf("acme-%d", i)
				require.GreaterOrEqual(t, counts[device], 1, "every MQTT PUBACKed identity must survive")
				if allRedelivered {
					require.Equal(t, 1, counts[device], "retry markers must recover each identity exactly once")
				}
			}
		})
	}
}
