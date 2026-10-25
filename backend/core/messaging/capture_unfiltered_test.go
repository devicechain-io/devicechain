// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
)

// THE CAPTURE DURABLE IS UNFILTERED ON THE BROKER, AND ITS READER FILTERS AT HAND-OUT.
//
// A filter list on the capture durable made the broker visit every device's subject in the
// current file block for each message it delivered to a reader that had fallen behind, so a
// backlog of a few thousand messages capped the reader below the rate devices publish and it
// never caught up (see NewReader). These hold the three halves of the change against a real
// broker: the durable has no filter, an older durable is moved onto none, a deep backlog over
// many device subjects drains at the speed of reading sequences, and an entry stored under a
// subject outside the stream's own list is still never handed out.

// captureDurable is the consumer the capture reader of area binds.
func captureDurable(t *testing.T, js nats.JetStreamContext, area string) *nats.ConsumerInfo {
	t.Helper()
	info, err := js.ConsumerInfo(captureStreamOn("test"), CaptureDurableName("test", area, captureSuffix))
	require.NoError(t, err)
	return info
}

// GUARD: a fresh capture durable carries no filter on the broker, and the reader hands out
// exactly the stream's two subjects.
func TestTheCaptureDurableIsUnfilteredOnTheBroker(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("cap-unfiltered")
	nmgr, readers := recorderRig(t, srv, area, recordNothing, captureSuffix)

	info := captureDurable(t, nmgr.js, area)
	require.Empty(t, info.Config.FilterSubject, "the capture durable has a broker filter")
	require.Empty(t, info.Config.FilterSubjects, "the capture durable has a broker filter list")

	r := readers[0].(*natsReader)
	require.ElementsMatch(t, DeviceEventsSubjects("test"), append([]string{r.forwards}, r.alsoForwards...),
		"the capture reader does not hand out exactly the stream's subjects")
}

// GUARD: the durable an earlier build created (old name, filter list) is DELETED when the reader
// binds, and the reader reads a new durable of its own name, created unfiltered; a message stored
// before the move is still delivered, and the new durable counts what is stored after it (a
// durable moved onto no filter in place would not: recreateUnfiltered).
func TestAnOlderCaptureDurableIsReplacedByANewUnfilteredOne(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("cap-move")
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)

	stream := captureStreamOn("test")
	durable := DurableName("test", area, captureSuffix)
	_, err = js.AddStream(&nats.StreamConfig{
		Name: stream, Subjects: DeviceEventsSubjects("test"), Retention: nats.WorkQueuePolicy,
	})
	require.NoError(t, err)
	// The durable exactly as the previous build created it, at the AckWait the rig below
	// configures (recorderRig), so the filter list is the only thing that differs.
	_, err = js.AddConsumer(stream, &nats.ConsumerConfig{
		Durable: durable, AckPolicy: nats.AckExplicitPolicy,
		AckWait: time.Second, MaxDeliver: MaxDeliver, MaxAckPending: readerMaxAckPending,
		FilterSubjects: DeviceEventsSubjects("test"),
	})
	require.NoError(t, err)
	staged, err := js.ConsumerInfo(stream, durable)
	require.NoError(t, err)
	require.Len(t, staged.Config.FilterSubjects, 2, "the staged durable is not the old shape; the test would prove nothing")
	_, err = js.Publish(DeviceEventsSubject("test", "acme", "before"), []byte("before"))
	require.NoError(t, err)

	nmgr, readers := recorderRig(t, srv, area, recordNothing, captureSuffix)

	moved := captureDurable(t, nmgr.js, area)
	require.Empty(t, moved.Config.FilterSubject)
	require.Empty(t, moved.Config.FilterSubjects, "the reader's durable has a filter list")
	require.True(t, moved.Created.After(staged.Created), "the reader's durable is not a new one")
	_, err = js.ConsumerInfo(stream, durable)
	require.ErrorIs(t, err, nats.ErrConsumerNotFound, "the old-named durable was left on the work queue")

	// Stored after the move, before anything is read: the durable must count them.
	for i := 0; i < 5; i++ {
		_, err = js.Publish(DeviceEventsSubject("test", "acme", "after"), []byte("after"))
		require.NoError(t, err)
	}
	waitWithin(t, 3*time.Second, "the moved durable to count what was stored after the move", func() bool {
		return captureDurable(t, nmgr.js, area).NumPending == 6
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	msg, err := readers[0].ReadMessage(ctx)
	require.NoError(t, err)
	require.Equal(t, "before", string(msg.Value))
	require.NoError(t, msg.Ack())
}

// GUARD: the reason the durable is unfiltered. A backlog spread over many device subjects, the
// shape a fleet leaves behind a stalled reader, is read back at the speed of reading
// sequences. The whole backlog sits in one file block (8 MB at this stream's ceiling), so with
// the filter list every delivery visits all 4,000 of its subjects: 96M visits, which took 20 s
// and more on a laptop (the 2,000-subject version of this test passed against the old filter
// at a busy moment, which is why it is this big). Unfiltered it is read in well under a second.
// The bound is wide so a slow runner does not fail it, and still several times below what the
// filtered lookup costs.
func TestACaptureBacklogOverManyDevicesDrainsAtSequenceSpeed(t *testing.T) {
	const devices, backlog = 4000, 24000
	srv := startEmbeddedServer(t)
	area := uniqueArea("cap-drain")
	nmgr, readers := recorderRig(t, srv, area, recordNothing, captureSuffix)

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream(nats.PublishAsyncMaxPending(4096))
	require.NoError(t, err)
	body := make([]byte, 260)
	for i := 0; i < backlog; i++ {
		_, err := js.PublishAsync(DeviceEventsSubject("test", "acme", fmt.Sprintf("dev-%04d", i%devices)), body)
		require.NoError(t, err)
		if i%2000 == 1999 {
			select {
			case <-js.PublishAsyncComplete():
			case <-time.After(30 * time.Second):
				t.Fatal("the backlog was not stored")
			}
		}
	}
	require.EqualValues(t, backlog, capturedHeld(t, nmgr.js), "the backlog is not all in the stream")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	start := time.Now()
	for n := 0; n < backlog; n++ {
		msg, err := readers[0].ReadMessage(ctx)
		require.NoError(t, err, "after %d of %d in %v", n, backlog, time.Since(start))
		require.NoError(t, msg.Ack())
	}
	took := time.Since(start)
	t.Logf("read %d captured messages over %d device subjects in %v (%.0f/s)",
		backlog, devices, took, float64(backlog)/took.Seconds())
	require.Less(t, took, 6*time.Second,
		"a backlog over many device subjects drained at %.0f/s: the broker is filtering per message", float64(backlog)/took.Seconds())
}

// GUARD: the reader still filters. A push consumer on another stream whose deliver subject is
// a capture subject stores its message in the capture stream under the SOURCE stream's
// subject; an unfiltered durable is delivered it. The reader acks and drops it, so it is
// neither handed out nor left in the work queue, and it does not stop the next device message.
func TestTheCaptureReaderDropsAnEntryStoredUnderAForeignSubject(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("cap-foreign")
	reg := prometheus.NewRegistry()
	nmgr, readers := captureRigWithRegistry(t, srv, area, reg)
	require.Zero(t, foreignDroppedSeries(t, reg, area, "the series exists at zero before any drop"))

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)

	target := DeviceEventsSubject("test", "acme", "dev-1")
	_, err = js.AddStream(&nats.StreamConfig{Name: "stuffing", Subjects: []string{"stuffing.x"}})
	require.NoError(t, err)
	_, err = js.Publish("stuffing.x", []byte("forged"))
	require.NoError(t, err)
	// A literal interest on the deliver subject is what makes the broker store the push (the
	// design's probe P5); without it the push is dropped and the test would prove nothing.
	lit, err := nc.SubscribeSync(target)
	require.NoError(t, err)
	// SubscribeSync returns before the server has registered the interest; the push below must
	// find it registered, so wait for the round trip.
	require.NoError(t, nc.Flush())
	t.Cleanup(func() { _ = lit.Unsubscribe() })
	_, err = js.AddConsumer("stuffing", &nats.ConsumerConfig{
		Durable: "push", DeliverSubject: target, AckPolicy: nats.AckNonePolicy,
	})
	require.NoError(t, err)
	waitWithin(t, 5*time.Second, "the pushed entry to be stored in the capture stream",
		func() bool { return capturedHeld(t, nmgr.js) == 1 })
	foreign, err := nmgr.js.GetMsg(captureStreamOn("test"), firstCapturedSeq(t, nmgr.js))
	require.NoError(t, err)
	require.Equal(t, "stuffing.x", foreign.Subject, "the planted entry is not stored under its source subject")

	// Then a device's own message and a gateway's.
	_, err = nmgr.js.Publish(target, []byte("device"))
	require.NoError(t, err)
	_, err = nmgr.js.Publish(GatewayEventsSubject("test", "acme", "gw-1", "child"), []byte("gateway"))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var got []string
	for len(got) < 2 {
		msg, err := readers[0].ReadMessage(ctx)
		require.NoError(t, err, "after %v", got)
		got = append(got, string(msg.Value))
		require.NoError(t, msg.Ack())
	}
	require.Equal(t, []string{"device", "gateway"}, got, "the reader handed out a message stored under a foreign subject")
	require.EqualValues(t, 1, foreignDroppedSeries(t, reg, area, "after the drop"), "the foreign entry was not counted exactly once")
	waitWithin(t, 3*time.Second, "the dropped entry to leave the work queue",
		func() bool { return capturedHeld(t, nmgr.js) == 0 })
}

func firstCapturedSeq(t *testing.T, js nats.JetStreamContext) uint64 {
	t.Helper()
	info, err := js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, err)
	return info.State.FirstSeq
}

// GUARD: the recreate is a move of the FILTER only. A durable that also differs in a field
// consumerConfig sets (here its AckWait) is still refused at create, by name, and is left
// exactly as it was: moving onto no filter must not become a way for a changed AckWait or
// MaxDeliver to converge silently (see consumerConfig).
func TestACaptureDurableThatDiffersInMoreThanItsFilterIsStillRefused(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("cap-refuse")
	ms := testMicroservice(t, srv, area)
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	ctx := context.Background()
	require.NoError(t, nmgr.Initialize(ctx))
	require.NoError(t, nmgr.Start(ctx))
	t.Cleanup(func() { _ = nmgr.Stop(ctx) })

	stream := captureStreamOn(ms.InstanceId)
	durable := CaptureDurableName(ms.InstanceId, area, captureSuffix)
	_, err := nmgr.js.AddStream(&nats.StreamConfig{
		Name: stream, Subjects: DeviceEventsSubjects(ms.InstanceId), Retention: nats.WorkQueuePolicy,
	})
	require.NoError(t, err)
	_, err = nmgr.js.AddConsumer(stream, &nats.ConsumerConfig{
		Durable: durable, AckPolicy: nats.AckExplicitPolicy,
		AckWait: nmgr.ackWait() + 7*time.Second, MaxDeliver: MaxDeliver, MaxAckPending: readerMaxAckPending,
		FilterSubjects: DeviceEventsSubjects(ms.InstanceId),
	})
	require.NoError(t, err)
	staged, err := nmgr.js.ConsumerInfo(stream, durable)
	require.NoError(t, err)

	_, err = nmgr.NewReader(captureSuffix)
	require.ErrorIs(t, err, nats.ErrConsumerNameAlreadyInUse, "a durable with a changed AckWait was not refused")
	after, err := nmgr.js.ConsumerInfo(stream, durable)
	require.NoError(t, err)
	require.Equal(t, staged.Created, after.Created, "the refused durable was recreated")
	require.Len(t, after.Config.FilterSubjects, 2, "the refused durable lost its filter")
}

// GUARD: recreateUnfiltered. A durable of the capture reader's OWN name that carries a filter
// (only a hand edit makes one) is recreated without it, never moved in place: an in-place move
// onto no filter leaves a durable the stream does not signal, which the num_pending check sees.
func TestAFilteredDurableUnderTheCaptureNameIsRecreatedNotUpdated(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("cap-recreate")
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	stream := captureStreamOn("test")
	_, err = js.AddStream(&nats.StreamConfig{Name: stream, Subjects: DeviceEventsSubjects("test"), Retention: nats.WorkQueuePolicy})
	require.NoError(t, err)
	_, err = js.AddConsumer(stream, &nats.ConsumerConfig{
		Durable: CaptureDurableName("test", area, captureSuffix), AckPolicy: nats.AckExplicitPolicy,
		AckWait: time.Second, MaxDeliver: MaxDeliver, MaxAckPending: readerMaxAckPending,
		FilterSubjects: DeviceEventsSubjects("test"),
	})
	require.NoError(t, err)
	staged := captureDurable(t, js, area)

	nmgr, _ := recorderRig(t, srv, area, recordNothing, captureSuffix)
	moved := captureDurable(t, nmgr.js, area)
	require.Empty(t, moved.Config.FilterSubjects, "the filter list was kept")
	require.True(t, moved.Created.After(staged.Created), "the durable was updated in place, not recreated")
	for i := 0; i < 3; i++ {
		_, err = js.Publish(DeviceEventsSubject("test", "acme", "after"), []byte("after"))
		require.NoError(t, err)
	}
	waitWithin(t, 3*time.Second, "the recreated durable to count what was stored after it", func() bool {
		return captureDurable(t, nmgr.js, area).NumPending == 3
	})
}

// captureRigWithRegistry is recorderRig for the capture reader, with the microservice's metrics on
// reg so a test reads them back the way a scrape does.
func captureRigWithRegistry(t *testing.T, srv *natsserver.Server, area string, reg *prometheus.Registry) (*NatsManager, []MessageReader) {
	t.Helper()
	ms := testMicroservice(t, srv, area)
	ms.UseMetricsRegistry(reg)
	var readers []MessageReader
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(n *NatsManager) error {
		r, err := n.NewReader(captureSuffix)
		readers = append(readers, r)
		return err
	})
	nmgr.RecordMaxDeliveries(recordNothing)
	ctx := context.Background()
	require.NoError(t, nmgr.Initialize(ctx))
	nmgr.SetAckWaitForTesting(t, time.Second)
	require.NoError(t, nmgr.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = nmgr.Stop(stopCtx)
	})
	return nmgr, readers
}

// foreignDroppedSeries reads devicechain_<area>_jetstream_reader_foreign_dropped_total for the
// capture reader's durable off reg, failing when the series is not exported at all.
func foreignDroppedSeries(t *testing.T, reg *prometheus.Registry, area, when string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	want := "devicechain_" + strings.ReplaceAll(area, "-", "") + "_jetstream_reader_foreign_dropped_total"
	for _, f := range families {
		if f.GetName() != want {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["stream"] == captureStreamOn("test") && labels["durable"] == CaptureDurableName("test", area, captureSuffix) {
				return m.GetCounter().GetValue()
			}
		}
	}
	t.Fatalf("%s{stream=%q,durable=%q} is not exported (%s)", want, captureStreamOn("test"),
		CaptureDurableName("test", area, captureSuffix), when)
	return 0
}
