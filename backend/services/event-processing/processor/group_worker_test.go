// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

type groupTestRouter struct{}

// Ready reports a placement is always adoptable: this router places every tenant statically.
func (groupTestRouter) Ready() error { return nil }

func (groupTestRouter) ShardGroup(tenant string) (int, error) {
	if tenant == "other" {
		return 1, nil
	}
	return 0, nil
}

type groupRig struct {
	b       *detectBroker
	nm      *messaging.NatsManager
	ids     []workerIdentity
	readers []messaging.MessageReader
	gate    *TermGate
	store   *model.SnapshotStore
}

func newGroupRig(t *testing.T, routers ...messaging.ShardGroupRouter) *groupRig {
	t.Helper()
	r := &groupRig{b: startDetectBroker(t), gate: NewTermGate(), store: brokerStore(t)}
	ms := &core.Microservice{InstanceId: r.b.instance, FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats = r.b.natsConfig()
	cfg := &mscfg.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{streams.ResolvedEvents}}
	ms.InstanceConfiguration.Infrastructure.Nats.ShardGroups = cfg
	var router messaging.ShardGroupRouter = groupTestRouter{}
	if len(routers) > 0 {
		router = routers[0]
	}
	r.nm = messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(nm *messaging.NatsManager) error {
		if err := nm.EnableStreamSharding(router, cfg); err != nil {
			return err
		}
		var err error
		r.ids, r.readers, err = groupInputs(nm, r.store, r.gate)
		return err
	})
	r.nm.RecordMaxDeliveries(deadletter.MaxDeliveryRecorder(deadletter.NewProducer(ms)))
	require.NoError(t, r.nm.Initialize(context.Background()))
	require.NoError(t, r.nm.Start(context.Background()))
	t.Cleanup(func() { r.nm.Conn().Close() })
	for i, rd := range r.readers {
		r.readers[i] = &trackedGroupReader{MessageReader: rd, TermBoundReader: rd.(messaging.TermBoundReader)}
	}
	return r
}

func (r *groupRig) worker(t *testing.T, g int) *ResolvedEventsProcessor {
	t.Helper()
	p := newTestProcessor(r.store, nil, 1000)
	ms := &core.Microservice{InstanceId: r.b.instance, FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	p.Microservice = ms
	p.metrics = NewDetectMetrics(ms)
	id := r.ids[g] // copy the immutable binding, including its authorized replay source
	p.worker = &id
	p.ResolvedEventsReader = r.readers[g]
	// Deliberately leave cfg at singleton and Replay unbound. Neither may override identity.
	p.Replay = r.nm
	p.cfg.Suffix = streams.ResolvedEvents
	return p
}
func (r *groupRig) publish(t *testing.T, g int, first, last int) {
	t.Helper()
	js, err := r.b.nc.JetStream()
	require.NoError(t, err)
	tenant := "acme"
	if g == 1 {
		tenant = "other"
	}
	for i := first; i <= last; i++ {
		ack, err := js.Publish(messaging.ScopedSubject(r.b.instance, tenant, r.ids[g].physical), resolvedBytes(t, testBase.Add(time.Duration(i)*time.Second)))
		require.NoError(t, err)
		require.EqualValues(t, i, ack.Sequence)
	}
}
func (r *groupRig) floor(t *testing.T, g int) (uint64, int) {
	t.Helper()
	js, err := r.b.nc.JetStream()
	require.NoError(t, err)
	physical := r.ids[g].physical
	ci, err := js.ConsumerInfo(messaging.StreamName(r.b.instance, physical), messaging.AckFloorDurableName(r.b.instance, "event-processing", physical))
	require.NoError(t, err)
	return ci.AckFloor.Stream, ci.NumAckPending
}
func (r *groupRig) row(t *testing.T, g int, want int64) model.DetectSnapshot {
	t.Helper()
	row, ok, err := r.store.Load(context.Background(), groupPartition(g))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, want, row.StreamSeq)
	return *row
}
func (r *groupRig) read(t *testing.T, g int) messaging.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg, err := r.readers[g].ReadMessage(ctx)
	require.NoError(t, err)
	return msg
}

// Independent scalar positions, rows, AckAll buffers, holes and captured heads through
// the real broker and SQLite. A g0 position of 100 must never hide g1 position 1.
func TestGroupPositionsFloorsGapsAndRestart(t *testing.T) {
	r := newGroupRig(t)
	r.gate.Enter(func() bool { return true })
	ps := []*ResolvedEventsProcessor{r.worker(t, 0), r.worker(t, 1)}
	for _, reader := range r.readers {
		require.NoError(t, reader.(messaging.TermBoundReader).BindTerm())
		defer reader.(messaging.TermBoundReader).UnbindTerm()
	}
	r.publish(t, 0, 1, 100)
	r.publish(t, 1, 1, 1)
	for g, p := range ps {
		require.NoError(t, p.restore(context.Background()))
		require.NoError(t, p.replayToHead())
		want := uint64(100)
		if g == 1 {
			want = 1
		}
		require.Equal(t, want, p.engine.LastSeq())
		r.row(t, g, int64(want))
	}
	// Both durables have delivered pending messages. Committing g0 cannot ack g1.
	for i := 0; i < 100; i++ {
		ps[0].handle(r.read(t, 0))
	}
	ps[1].handle(r.read(t, 1))
	require.True(t, ps[0].checkpoint(context.Background()))
	require.Eventually(t, func() bool { f, _ := r.floor(t, 0); return f == 100 }, 5*time.Second, 10*time.Millisecond)
	f, pending := r.floor(t, 1)
	require.Zero(t, f)
	require.Equal(t, 1, pending)
	require.True(t, ps[1].checkpoint(context.Background()))
	require.Eventually(t, func() bool { f, _ := r.floor(t, 1); return f == 1 }, 5*time.Second, 10*time.Millisecond)
	// Lose two live deliveries in EACH stream. Each gap must use that group's source.
	for g, first := range []int{101, 2} {
		r.publish(t, g, first, first+2)
		_ = r.read(t, g)
		_ = r.read(t, g)
		ps[g].handle(r.read(t, g))
		require.EqualValues(t, first+2, ps[g].engine.LastSeq())
		require.True(t, ps[g].checkpoint(context.Background()))
		row := r.row(t, g, int64(first+2))
		restored := r.worker(t, g)
		require.NoError(t, restored.restore(context.Background()))
		require.EqualValues(t, first+2, restored.engine.LastSeq())
		payload, err := restored.engine.Snapshot()
		require.NoError(t, err)
		require.Equal(t, row.Payload, payload)
		start, err := CommittedFloorStart(r.store, restored.partitionID())(context.Background())
		require.NoError(t, err)
		require.EqualValues(t, first+3, start)
		r.publish(t, g, first+3, first+3)
		require.NoError(t, restored.replayToHead())
		require.EqualValues(t, first+3, restored.engine.LastSeq())
		r.row(t, g, int64(first+3))
	}
	// g0's encoding is byte-identical to the legacy path for the same inputs.
	legacy := newTestProcessor(newTestStore(t), nil, 1000)
	require.NoError(t, legacy.restore(context.Background()))
	for i := uint64(1); i <= 104; i++ {
		legacy.applyResolved(msgAt(t, i, &fakeAck{}))
	}
	want, err := legacy.engine.Snapshot()
	require.NoError(t, err)
	require.Equal(t, want, r.row(t, 0, 104).Payload)
	golden, err := os.ReadFile("testdata/r7_s2_g0_snapshot.json")
	require.NoError(t, err)
	require.Equal(t, golden, r.row(t, 0, 104).Payload, "g0 snapshot bytes changed from parent")
	require.Equal(t, r.b.instance+"_event-processing_resolved-events_ackfloor", messaging.AckFloorDurableName(r.b.instance, "event-processing", streams.ResolvedEvents))
	require.True(t, r.row(t, 0, 104).Watermark.After(r.row(t, 1, 5).Watermark), "group clocks must stay independent")
	require.Equal(t, "singleton", r.ids[0].partition)
	require.Equal(t, streams.ResolvedEvents, r.ids[0].physical)
}

type countingGroupLease struct {
	PartitionLease
	acquires atomic.Int32
}

func (l *countingGroupLease) Acquire(p string) (*messaging.Lease, error) {
	l.acquires.Add(1)
	return l.PartitionLease.Acquire(p)
}

func TestGroupSupervisorJoinsAndCheckpointsBeforeRelease(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint("lost=", lost), func(t *testing.T) {
			r := newGroupRig(t)
			p0, p1 := r.worker(t, 0), r.worker(t, 1)
			dl, err := r.nm.NewDistributedLease(messaging.DefaultLeaseTTL)
			require.NoError(t, err)
			counted := &countingGroupLease{PartitionLease: dl}
			p0.Lease, p0.Gate = counted, r.gate
			require.NoError(t, p0.attachGroupWorkers(p1))
			require.NoError(t, p0.ExecuteInitialize(context.Background()))
			// Clean marker avoids the 20-second ambiguous-prior-owner wait.
			prior, err := dl.Acquire(singletonPartition)
			require.NoError(t, err)
			require.NoError(t, prior.ReleaseWithin(context.Background()))
			h, err := p0.beginTerm()
			require.NoError(t, err)
			var ended atomic.Bool
			end := func() {
				if ended.CompareAndSwap(false, true) {
					p0.endTerm(h)
				}
			}
			t.Cleanup(end)
			require.EqualValues(t, 1, counted.acquires.Load())
			require.Nil(t, p1.Lease, "a worker must never acquire")
			require.Error(t, p1.ExecuteStart(context.Background()))
			if lost {
				require.NoError(t, h.lease.ReleaseWithin(context.Background()))
				done := make(chan struct{})
				go func() { ended.Store(true); p0.awaitTermEnd(h); close(done) }()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("lease loss did not end all workers")
				}
				require.Error(t, p0.pctx().Err())
				require.Error(t, p1.pctx().Err())
				require.False(t, r.gate.Held())
				for _, rd := range r.readers {
					require.EqualValues(t, 1, rd.(*trackedGroupReader).unbound.Load())
				}
				return
			}
			// Join the real live loops before arranging dirty final state deterministically.
			p0.pcancel()
			for _, w := range p0.termWorkers() {
				w.readerWG.Wait()
			}
			require.Error(t, p1.pctx().Err(), "common term cancellation did not reach g1")
			// A background context is used only for these synchronous fixture mutations.
			for g, w := range []*ResolvedEventsProcessor{p0, p1} {
				w.procCtx = context.Background()
				m := msgAt(t, uint64(g+1), &fakeAck{})
				if g == 1 {
					m.Subject = messaging.ScopedSubject(r.b.instance, "other", r.ids[g].physical)
				}
				w.applyResolved(m)
			}
			var saves atomic.Int32
			for _, w := range []*ResolvedEventsProcessor{p0, p1} {
				w := w
				w.snapshotFn = func() ([]byte, error) {
					require.True(t, h.holder.Held(), "snapshot serialized after release/loss")
					saves.Add(1)
					return w.engine.Snapshot()
				}
			}
			end()
			require.False(t, r.gate.Held())
			require.EqualValues(t, 1, counted.acquires.Load())
			for _, rd := range r.readers {
				require.EqualValues(t, 1, rd.(*trackedGroupReader).bound.Load())
				require.EqualValues(t, 1, rd.(*trackedGroupReader).unbound.Load(), "every group must unbind before release")
			}
			r.row(t, 0, 1)
			r.row(t, 1, 2)
			require.GreaterOrEqual(t, saves.Load(), int32(2))
			require.True(t, dl.PriorOwnerReleasedCleanly(singletonPartition))
		})
	}
}

func TestGroupWorkersRefuseUnroutedFactProjections(t *testing.T) {
	r := newGroupRig(t)
	p0, p1 := r.worker(t, 0), r.worker(t, 1)
	p0.Lease = &messaging.DistributedLease{}
	p0.Gate = r.gate
	p1.RosterStore = &model.DeviceRosterStore{}
	require.ErrorContains(t, p0.attachGroupWorkers(p1), "S2b")
	p1.RosterStore = nil
	p1.RuleStore = &model.DetectRuleStore{}
	require.ErrorContains(t, p0.attachGroupWorkers(p1), "S2b")
	p1.RuleStore = nil
	require.NoError(t, p0.attachGroupWorkers(p1))
}

type trackedGroupReader struct {
	messaging.MessageReader
	messaging.TermBoundReader
	bound, unbound atomic.Int32
}

func (r *trackedGroupReader) BindTerm() error { r.bound.Add(1); return r.TermBoundReader.BindTerm() }
func (r *trackedGroupReader) UnbindTerm() error {
	r.unbound.Add(1)
	return r.TermBoundReader.UnbindTerm()
}
