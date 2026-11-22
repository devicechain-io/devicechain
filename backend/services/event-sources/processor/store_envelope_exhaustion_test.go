// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// Exhaust the real WAL durable, including its final delivery. There is no
// max-delivery recorder for this stream to rescue an unacknowledged failure.
func TestWALMaxDeliverFailuresReachFailedDecode(t *testing.T) {
	for _, envelopes := range []bool{false, true} {
		for _, failure := range []string{"round", "full-hold", "expired-hold", "unwired-hold"} {
			if failure == "round" && !envelopes {
				continue
			}
			t.Run(fmt.Sprintf("%s/envelopes=%t", failure, envelopes), func(t *testing.T) {
				srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1,
					JetStream: true, StoreDir: dctest.JetStreamStoreDir(t), NoLog: true, NoSigs: true})
				require.NoError(t, err)
				go srv.Start()
				require.True(t, srv.ReadyForConnections(10*time.Second))
				t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })
				nc, err := nats.Connect(srv.ClientURL())
				require.NoError(t, err)
				t.Cleanup(nc.Close)
				js, err := nc.JetStream()
				require.NoError(t, err)
				si, err := js.AddStream(&nats.StreamConfig{Name: messaging.MqttMessageStore,
					Subjects: []string{"$MQTT.msgs.>"}, Retention: nats.InterestPolicy, Storage: nats.FileStorage})
				require.NoError(t, err)
				d := messaging.MqttFailClosedDurables("inst-1")[0]
				cfg := messaging.MqttFailClosedConsumerConfig(d, 1)
				cfg.AckWait = 40 * time.Millisecond
				_, err = js.AddConsumer(messaging.MqttMessageStore, cfg)
				require.NoError(t, err)
				sub, err := js.PullSubscribe("", d.Name, nats.Bind(messaging.MqttMessageStore, d.Name))
				require.NoError(t, err)
				defer sub.Unsubscribe()
				_, err = js.AddStream(&nats.StreamConfig{Name: "failed", Subjects: []string{"failed.decode"}})
				require.NoError(t, err)
				payload := []byte(envBody("wal-exhausted"))
				_, err = js.Publish("$MQTT.msgs.inst-1.acme.devices.wal-exhausted.events", payload)
				require.NoError(t, err)

				b := newEnvBroker()
				b.rejectAll.Store(true)
				es := storeEnvSource(t, b, envelopes)
				es.poison = &poisonRoutes{slots: make(chan struct{}, POISON_ROUTE_LIMIT)}
				es.failed = func(_ string, tenant string, body []byte, reason error) error {
					if tenant != "acme" || reason == nil {
						return errors.New("missing failed-decode diagnostic")
					}
					_, err := js.Publish("failed.decode", body)
					return err
				}
				es.UsePlacementCheck(func(string) error { return messaging.ErrUnplacedTenant })
				es.finalHoldBeat, es.finalHoldPoll = time.Hour, time.Hour
				es.finalHolds = es.startFinalPlacementHolds()
				t.Cleanup(func() { es.finalHolds.cancel(); <-es.finalHolds.done; es.poison.running.Wait() })
				var ce *captureEngine
				if envelopes {
					ce, err = es.newCaptureEngine()
					require.NoError(t, err)
					defer ce.cancel()
					ce.engine.SetGiveUp(time.Millisecond)
				}

				for attempt := 1; attempt <= messaging.MaxDeliver; attempt++ {
					ms, err := sub.Fetch(1, nats.MaxWait(2*time.Second))
					require.NoError(t, err)
					md, err := ms[0].Metadata()
					require.NoError(t, err)
					require.EqualValues(t, attempt, md.NumDelivered)
					m := messaging.NewConsumedMessage(strings.TrimPrefix(ms[0].Subject, "$MQTT.msgs."),
						ms[0].Data, int(md.NumDelivered), nil, restartWALAck{ms[0]})
					m.StreamSeq, m.AppendTime = md.Sequence.Stream, md.Timestamp
					m.DedupID = messaging.MqttMessageStoreDedupID("acme", si.Created, m.StreamSeq)
					if failure == "round" {
						key, ok := es.envelopeKey("acme", m)
						require.True(t, ok)
						_, pos := splitPosition("acme", positionToken(m.DedupID, key, attempt > 1, 0))
						pp := pendingPublish{ctx: core.WithTenant(context.Background(), "acme"),
							msg: messaging.Message{Value: pbDevice("wal-exhausted")}, pos: pos, done: es.settler(m, "acme")}
						es.publishGroup(ce, messaging.FormedBatch{Tenant: "acme",
							Members: []messaging.FormedMember{{Bytes: pp.msg.Value, Token: pp}}})
						ce.engine.Wait()
					} else {
						if messaging.FinalDelivery(m) && failure == "full-hold" {
							es.finalHolds.mu.Lock()
							for i := 0; i < finalPlacementHoldLimit; i++ {
								es.finalHolds.entries[&finalPlacementHold{running: true}] = struct{}{}
							}
							es.finalHolds.mu.Unlock()
						}
						if failure == "unwired-hold" {
							es.placementCheck = nil
						}
						es.settler(m, "acme")(messaging.ErrUnplacedTenant)
						if messaging.FinalDelivery(m) && failure == "expired-hold" {
							es.finalHolds.expire(time.Now().Add(defaultFinalPlacementHoldMaxAge + time.Second))
						}
					}
					es.poison.running.Wait()
					failed, err := js.StreamInfo("failed")
					require.NoError(t, err)
					if attempt < messaging.MaxDeliver {
						require.Zero(t, failed.State.Msgs, "ordinary failures must wait for redelivery")
					} else {
						require.EqualValues(t, 1, failed.State.Msgs, "the exhausted WAL member needs a durable terminal route")
						stored, err := js.GetMsg("failed", failed.State.FirstSeq)
						require.NoError(t, err)
						require.Equal(t, payload, stored.Data)
					}
				}
				require.Eventually(t, func() bool {
					ci, err := js.ConsumerInfo(messaging.MqttMessageStore, d.Name)
					return err == nil && ci.NumPending == 0 && ci.NumAckPending == 0
				}, time.Second, time.Millisecond)
			})
		}
	}
}

func TestWALFinalEnvelopeDeletionAndShutdownStayUnacked(t *testing.T) {
	for _, reason := range []error{messaging.ErrTenantGone, messaging.ErrRoundWalkStopped} {
		es := storeEnvSource(t, newEnvBroker(), true)
		es.poison = &poisonRoutes{slots: make(chan struct{}, POISON_ROUTE_LIMIT)}
		es.failed = func(string, string, []byte, error) error {
			t.Error("deletion or shutdown must not republish the payload")
			return nil
		}
		a := &recordingAck{}
		m := storeCaptured(1, messaging.MaxDeliver, midBucket().Add(-time.Hour), midBucket(), a)
		es.settler(m, "acme")(errors.Join(errStoreEnvelopeUnsettled, reason))
		es.poison.running.Wait()
		require.Zero(t, a.counts())
	}
}
