// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
)

// These tests are the durability half of the deeper publish window: a wide window holds more
// sources unacked, so what protects them is the dedup id, and the evidence that it did is the
// duplicates counter. They run on a real three-node, replicated cluster because the stream's
// duplicate window and the PubAck's Duplicate flag are the broker's, not the client's.
//
// The cluster fixture is implemented only on unix, so these run in CI and not on a Windows
// workstation.

// clusterManagerOn is a started manager on servers, R3 streams, its metrics in its own registry.
// Two managers built with the same area share the stream names, which is how a replacement pod
// sees the stream its predecessor wrote.
func clusterManagerOn(t *testing.T, servers []*natsserver.Server, area string) *NatsManager {
	t.Helper()
	ms := testMicroservice(t, servers[0], area)
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	nmgr.RecordMaxDeliveries(recordNothing)
	if err := nmgr.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := nmgr.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
	return nmgr
}

// storedDedupIDs reads every message in the resolved stream and returns how many there are and
// how many distinct Nats-Msg-Id values they carry.
func storedDedupIDs(t *testing.T, nmgr *NatsManager) (stored, distinct int) {
	t.Helper()
	stream := StreamName(nmgr.Microservice.InstanceId, streams.ResolvedEvents)
	info, err := nmgr.js.StreamInfo(stream)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq; seq++ {
		raw, err := nmgr.js.GetMsg(stream, seq)
		if err != nil {
			t.Fatalf("stream sequence %d: %v", seq, err)
		}
		ids[raw.Header.Get(nats.MsgIdHdr)] = true
	}
	return int(info.State.Msgs), len(ids)
}

func dedupID(i int) string { return fmt.Sprintf("resolved:acme:%d:0", i) }

// A pod that dies between publishing and hearing the PubAck leaves the broker holding some of
// its events and the sources unacked. The replacement receives the sources again and
// publishes them again under the same ids: the stream must end up with exactly one copy of
// each, and the duplicates counter must show the republish was absorbed rather than skipped.
//
// The republish races the first run's publishes the broker is still replicating, and the
// broker answers a republish whose original is not yet committed with "duplicate message id
// is in process". That is not a failure — the writer sends it again until the id resolves —
// so every republish must still succeed: a writer that reported the conflict failed this
// test intermittently, by how many of the first run's proposals were still uncommitted.
// TestOrderedWriterResendsAPublishWhoseIdIsInProcess pins the same behaviour every time.
//
// GUARD: kills a window or writer that publishes without the dedup id (more than N stored),
// and a duplicates counter that is never incremented (reads 0).
func TestCrashBetweenPublishAndAckStoresEachEventOnce(t *testing.T) {
	const n, window = 400, 512
	servers := dctest.StartJetStreamCluster(t, 3)
	area := uniqueArea("ordered-crash")

	first := clusterManagerOn(t, servers, area)
	w := newOrdered(t, first, window)
	waitUntilPublishable(t, first)
	base, _ := storedDedupIDs(t, first)

	for i := 0; i < n; i++ {
		w.Publish(orderedCtx(), Message{Value: []byte("e"), DedupID: dedupID(i)}, func(error) {})
	}
	// The pod dies as soon as the broker holds some of them, with the rest still awaiting a
	// PubAck: the connection goes without a drain, so no settle callback can ack a source.
	stream := StreamName(first.Microservice.InstanceId, streams.ResolvedEvents)
	var heldAtCrash uint64
	deadline := time.Now().Add(30 * time.Second)
	for heldAtCrash < 100 {
		info, err := first.js.StreamInfo(stream)
		if err != nil {
			t.Fatal(err)
		}
		heldAtCrash = info.State.Msgs - uint64(base)
		if time.Now().After(deadline) {
			t.Fatalf("the broker held only %d of the first run's events", heldAtCrash)
		}
	}
	first.nc.Close()

	// The replacement is redelivered every source and publishes them all again.
	second := clusterManagerOn(t, servers, area)
	w2 := newOrdered(t, second, window)
	out := newOutcomes()
	for i := 0; i < n; i++ {
		w2.Publish(orderedCtx(), Message{Value: []byte("e"), DedupID: dedupID(i)}, out.done(i))
	}
	w2.Draining()
	w2.Close()
	for i := 0; i < n; i++ {
		if out.errs[i] != nil {
			t.Fatalf("republish %d failed: %v", i, out.errs[i])
		}
	}

	stored, distinct := storedDedupIDs(t, second)
	if want := n + base; stored != want || distinct != n+base {
		t.Fatalf("stream holds %d messages with %d distinct ids, want exactly %d of each (one per event)",
			stored, distinct, want)
	}
	if got := duplicateCount(second); got < float64(heldAtCrash) {
		t.Fatalf("duplicates counted on the replacement: %v, want at least the %d events the broker already held",
			got, heldAtCrash)
	}
}

// lossyJetStream stores every publish and then loses its acknowledgement while lose is set:
// the broker did its part and the pod never heard.
type lossyJetStream struct {
	nats.JetStreamContext
	lose bool
}

func (l *lossyJetStream) PublishMsgAsync(m *nats.Msg, opts ...nats.PubOpt) (nats.PubAckFuture, error) {
	fut, err := l.JetStreamContext.PublishMsgAsync(m, opts...)
	if err != nil || !l.lose {
		return fut, err
	}
	return &fakeFuture{ok: make(chan *nats.PubAck), err: make(chan error)}, nil
}

// A PubAck that never arrives fails the publish, which leaves its source unacked and so
// redelivered. The broker already stored it, so the republish must come back as a duplicate:
// one copy in the stream and one duplicate counted, per event.
//
// GUARD: kills a writer that drops the dedup id (two copies) and a counter that never counts.
func TestALostPubAckIsOneCopyAndOneCountedDuplicate(t *testing.T) {
	const n = 20
	servers := dctest.StartJetStreamCluster(t, 3)
	nmgr := clusterManagerOn(t, servers, uniqueArea("ordered-lost"))
	w := newOrdered(t, nmgr, 64).(*orderedWriter)
	waitUntilPublishable(t, nmgr)
	base, _ := storedDedupIDs(t, nmgr)

	lossy := &lossyJetStream{JetStreamContext: w.js, lose: true}
	w.js = lossy
	out := newOutcomes()
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithTimeout(orderedCtx(), 500*time.Millisecond)
		defer cancel()
		w.Publish(ctx, Message{Value: []byte("e"), DedupID: dedupID(i)}, out.done(i))
	}
	w.Draining()
	if out.waitCount(n, 15*time.Second) != n {
		t.Fatal("the unanswered publishes were never reported")
	}
	for i := 0; i < n; i++ {
		if out.errs[i] == nil {
			t.Fatalf("publish %d reported success without a PubAck", i)
		}
	}
	if got := duplicateCount(nmgr); got != 0 {
		t.Fatalf("duplicates before any republish: %v", got)
	}

	lossy.lose = false
	redo := newOutcomes()
	for i := 0; i < n; i++ {
		w.Publish(orderedCtx(), Message{Value: []byte("e"), DedupID: dedupID(i)}, redo.done(i))
	}
	if redo.waitCount(n, 15*time.Second) != n {
		t.Fatal("the republishes were never reported")
	}
	w.Close()
	for i := 0; i < n; i++ {
		if redo.errs[i] != nil {
			t.Fatalf("republish %d failed: %v", i, redo.errs[i])
		}
	}

	stored, distinct := storedDedupIDs(t, nmgr)
	if stored != n+base || distinct != n+base {
		t.Fatalf("stream holds %d messages with %d distinct ids, want %d of each", stored, distinct, n+base)
	}
	if got := duplicateCount(nmgr); got != n {
		t.Fatalf("duplicates counted: %v, want %d (one per republished event)", got, n)
	}
}
