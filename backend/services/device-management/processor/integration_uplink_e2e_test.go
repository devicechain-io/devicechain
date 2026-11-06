// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// END TO END, ON A REAL BROKER: an IntegrationUplink that event-sources publishes to the
// integration-uplinks stream is pulled by device-management's own durable on it, decoded in the
// resolver pool, and arrives on resolved-events as a stored reading. The paired run publishes an
// uplink the decoder cannot read: it ends as ONE DecodeFailed record on failed-events, delivered
// exactly once, with nothing resolved. The inbound-events durable, the one a build without
// payload decoders runs, never sees either.

const e2eInstance = "gate"

// e2eRig wires a device-management processor (area device-management) reading integration-uplinks
// over the integration rig's database, and an event-sources writer (its own manager, since only
// event-sources may write the stream).
type e2eRig struct {
	*integrationRig
	dm    *messaging.NatsManager
	js    nats.JetStreamContext
	uplk  messaging.MessageWriter
	iproc *InboundEventsProcessor
}

func newE2ERig(t *testing.T, opts ...InboundOption) *e2eRig {
	t.Helper()
	rig := newIntegrationRig(t)
	rig.publishDecoderForE2E()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dctest.JetStreamStoreDir(t)})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second))
	t.Cleanup(srv.Shutdown)
	host, port := brokerAddr(t, srv.ClientURL())

	// event-sources' manager, on the same server (only event-sources may write the stream).
	es := &core.Microservice{InstanceId: e2eInstance, FunctionalArea: "event-sources"}
	es.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
	esm := messaging.NewNatsManager(es, core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
	require.NoError(t, esm.Initialize(context.Background()))
	require.NoError(t, esm.Start(context.Background()))
	t.Cleanup(func() { _ = esm.Stop(context.Background()) })
	w, err := esm.NewWriter(streams.IntegrationUplinks)
	require.NoError(t, err)

	// device-management's, with its readers and writers built where the service builds them: in
	// the manager's create callback, so the manager starts them.
	ms := &core.Microservice{InstanceId: e2eInstance, FunctionalArea: "device-management"}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
	var (
		uplinkReader, inboundReader messaging.MessageReader
		resolved, failed            messaging.OrderedWriter
	)
	dm := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(n *messaging.NatsManager) error {
		var err error
		if resolved, err = n.NewOrderedWriter(streams.ResolvedEvents, messaging.WriterResolution, config.DefaultPublishWindow); err != nil {
			return err
		}
		if failed, err = n.NewOrderedWriter(streams.FailedEvents, messaging.WriterResolution, config.DefaultPublishWindow); err != nil {
			return err
		}
		if uplinkReader, err = n.NewReader(streams.IntegrationUplinks); err != nil {
			return err
		}
		inboundReader, err = n.NewReader(streams.InboundEvents)
		return err
	})
	dm.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	require.NoError(t, dm.Initialize(context.Background()))
	require.NoError(t, dm.Start(context.Background()))
	t.Cleanup(func() { _ = dm.Stop(context.Background()) })
	// The readers park until the stream they forward into has been measured.
	dm.MeasureBackpressureForTesting(t, streams.ResolvedEvents)

	iproc := NewInboundEventsProcessor(dm.Microservice, []messaging.MessageReader{inboundReader}, resolved, failed,
		core.NewNoOpLifecycleCallbacks(), rig.capi, config.AuthModeDisabled,
		time.Duration(config.DefaultMaxEventFutureSkewSeconds)*time.Second, NewResolveMetrics(dm.Microservice),
		append([]InboundOption{WithIntegrationUplinkReaders([]messaging.MessageReader{uplinkReader}),
			WithSecretTransit(testSecretTransit(t))}, opts...)...)
	require.NoError(t, iproc.Initialize(context.Background()))
	require.NoError(t, iproc.Start(context.Background()))
	t.Cleanup(func() { _ = iproc.Stop(context.Background()) })

	js, err := dm.Conn().JetStream()
	require.NoError(t, err)
	return &e2eRig{integrationRig: rig, dm: dm, js: js, uplk: w, iproc: iproc}
}

func (r *integrationRig) publishDecoderForE2E() { r.publishDecoder(tempDecoder) }

// publish is event-sources' publish of an uplink for tenant acme.
func (e *e2eRig) publish(up *esmodel.UnresolvedIntegrationUplinkPayload) {
	e.t.Helper()
	ev := e.uplink("chirp-1", e.token, up)
	ev.OccurredTime, ev.ProcessedTime = time.Now(), time.Now()
	wire, err := esproto.MarshalUnresolvedEvent(ev)
	require.NoError(e.t, err)
	require.NoError(e.t, e.uplk.WriteMessages(core.WithTenant(context.Background(), "acme"),
		messaging.Message{Key: []byte("dev"), Value: wire}))
}

// next waits for one message on stream's subject for acme, from the start of the stream.
func (e *e2eRig) next(suffix string) *nats.Msg {
	e.t.Helper()
	sub, err := e.js.SubscribeSync(messaging.ScopedSubject(e2eInstance, "acme", suffix), nats.OrderedConsumer(), nats.DeliverAll())
	require.NoError(e.t, err)
	defer sub.Unsubscribe()
	m, err := sub.NextMsg(10 * time.Second)
	require.NoError(e.t, err, "nothing arrived on %s", suffix)
	return m
}

func (e *e2eRig) consumerInfo(suffix, area string) *nats.ConsumerInfo {
	e.t.Helper()
	ci, err := e.js.ConsumerInfo(messaging.StreamName(e2eInstance, suffix), messaging.DurableName(e2eInstance, area, suffix))
	require.NoError(e.t, err)
	return ci
}

func TestAnUplinkOnTheStreamIsConsumedDecodedAndStoredAsReadings(t *testing.T) {
	e := newE2ERig(t)
	e.publish(frameUplink(0x00, 0xD7))

	m := e.next(streams.ResolvedEvents)
	re, err := resolvedevent.UnmarshalResolvedEvent(m.Data)
	require.NoError(t, err)
	require.Equal(t, "dev", re.SourceDeviceToken)
	p := re.Payload.(*resolvedevent.ResolvedMeasurementsPayload)
	require.Len(t, p.Entries, 1)
	require.Len(t, p.Entries[0].Entries, 1)
	require.Equal(t, "temp", p.Entries[0].Entries[0].Name)
	require.Equal(t, "21.5", p.Entries[0].Entries[0].Value)

	// Consumed exactly once and acknowledged; the older-build consumer (inbound-events) saw none.
	require.Eventually(t, func() bool {
		ci := e.consumerInfo(streams.IntegrationUplinks, "device-management")
		return ci.AckFloor.Consumer == 1 && ci.NumAckPending == 0 && ci.NumPending == 0
	}, 10*time.Second, 50*time.Millisecond)
	require.EqualValues(t, 1, e.consumerInfo(streams.IntegrationUplinks, "device-management").Delivered.Consumer)
	require.EqualValues(t, 0, e.consumerInfo(streams.InboundEvents, "device-management").Delivered.Consumer)
}

// The paired broken-decoder run: the same wiring, an uplink too short for the decoder. Exactly one
// DecodeFailed record, written before the source is acked, and the source is delivered once.
func TestABrokenDecoderEndsInOneDecodeFailedRecordAndNoRedelivery(t *testing.T) {
	e := newE2ERig(t)
	e.publish(frameUplink(0x00))

	m := e.next(streams.FailedEvents)
	fe, err := resolvedevent.UnmarshalFailedEvent(m.Data)
	require.NoError(t, err)
	require.Equal(t, uint(resolvedevent.FailureReason_DecodeFailed), fe.Reason, fe.Error)
	require.Contains(t, fe.Error, "decode_failed: eval_error")
	require.NotEmpty(t, fe.Payload, "the record archives the uplink, frame included")

	require.Eventually(t, func() bool {
		ci := e.consumerInfo(streams.IntegrationUplinks, "device-management")
		return ci.AckFloor.Consumer == 1 && ci.NumAckPending == 0
	}, 10*time.Second, 50*time.Millisecond)
	// Delivery 1 was the only one, and nothing was resolved.
	require.EqualValues(t, 1, e.consumerInfo(streams.IntegrationUplinks, "device-management").Delivered.Consumer)
	info, err := e.js.StreamInfo(messaging.StreamName(e2eInstance, streams.ResolvedEvents))
	if err == nil {
		require.Zero(t, info.State.Msgs, "a failed decode resolves nothing")
	}
}

func brokerAddr(t *testing.T, clientURL string) (string, uint32) {
	t.Helper()
	u, err := url.Parse(clientURL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return u.Hostname(), uint32(port)
}

// THE RECORD BUDGET, ON A REAL BROKER. A decoder that fails on every uplink must not write a
// failed-events record for every one: the first DecodeRecordBurst are recorded, the rest are acked
// and counted as suppressed. Nothing is redelivered and nothing is silent.
func TestABrokenDecoderCannotFloodFailedEvents(t *testing.T) {
	const offered = 30
	clock := newFakeClock()
	store := &fakeStore{}
	acct := newDecodeAccountant(store, "pod-a", nil, clock.now)
	e := newE2ERig(t, WithDecodeObserver(acct))
	for i := 0; i < offered; i++ {
		e.publish(frameUplink(0x00))
	}

	// Every source is settled (acked) exactly once, none redelivered.
	require.Eventually(t, func() bool {
		ci := e.consumerInfo(streams.IntegrationUplinks, "device-management")
		return ci.AckFloor.Consumer == offered && ci.NumAckPending == 0 && ci.NumPending == 0
	}, 20*time.Second, 50*time.Millisecond)
	require.EqualValues(t, offered, e.consumerInfo(streams.IntegrationUplinks, "device-management").Delivered.Consumer)

	// The failed-events stream holds the budget, not the flood.
	info, err := e.js.StreamInfo(messaging.StreamName(e2eInstance, streams.FailedEvents))
	require.NoError(t, err)
	require.EqualValues(t, DecodeRecordBurst, info.State.Msgs, "records are bounded by the budget")

	// And the tally accounts for every uplink offered.
	require.NoError(t, acct.Flush(context.Background()))
	r, found := store.last("acme", "p", 2)
	require.True(t, found, "%+v", store.writes)
	require.EqualValues(t, DecodeRecordBurst, r.Failed)
	require.EqualValues(t, offered-DecodeRecordBurst, r.Suppressed)
	require.EqualValues(t, offered, r.Decoded+r.Ignored+r.Failed+r.Suppressed)
}
