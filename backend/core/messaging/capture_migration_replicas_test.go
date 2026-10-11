// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	dctest "github.com/devicechain-io/dc-microservice/test"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// replicaManager starts one replica of an area over a shared broker. The first replica to
// run creates the capture stream under Limits retention with preload messages; later ones
// find it there.
func replicaManager(t *testing.T, srv *natsserver.Server, area string, preload int) (*NatsManager, MessageReader) {
	t.Helper()
	var reader MessageReader
	nmgr := NewNatsManager(testMicroservice(t, srv, area), core.NewNoOpLifecycleCallbacks(),
		func(n *NatsManager) error {
			r, err := n.NewReader(captureSuffix)
			reader = r
			return err
		})
	nmgr.RecordMaxDeliveries(recordNothing)
	ctx := context.Background()
	require.NoError(t, nmgr.Initialize(ctx))
	if _, err := nmgr.js.StreamInfo(captureStreamOn("test")); errors.Is(err, nats.ErrStreamNotFound) {
		_, err = nmgr.js.AddStream(&nats.StreamConfig{
			Name: captureStreamOn("test"), Subjects: StreamSubjects("test", captureSuffix),
			Retention: nats.LimitsPolicy, Storage: nats.FileStorage,
		})
		require.NoError(t, err)
		for i := 0; i < preload; i++ {
			publishCaptured(t, nmgr.js, "preloaded")
		}
	}
	nmgr.SetAckWaitForTesting(t, time.Second)
	require.NoError(t, nmgr.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = nmgr.Stop(stopCtx)
	})
	return nmgr, reader
}

// Two replicas of one area both find the older stream and both run the migration. Exactly one
// of them recreates it, and the sequence is moved past the old stream once, not twice: a
// second recreate would reuse sequences the first had already forwarded.
func TestTwoReplicasMigrateTheStreamOnce(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("replicas")
	a, readerA := replicaManager(t, srv, area, 3)
	b, readerB := replicaManager(t, srv, area, 3)
	require.Equal(t, []string{captureSuffix}, a.PendingRetentionMigrations())
	require.Equal(t, []string{captureSuffix}, b.PendingRetentionMigrations())
	consumeCaptured(t, readerA)
	consumeCaptured(t, readerB)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, m := range []*NatsManager{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = m.MigratePendingRetention(ctx)
		}()
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	info, err := a.js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, err)
	require.Equal(t, nats.WorkQueuePolicy, info.Config.Retention)
	next := publishCaptured(t, a.js, "after")
	require.EqualValues(t, 1_000_004, next,
		"the sequence moved past the old stream more than once, or not at all")
}

// A replica that finds the stream already moved leaves it alone: it does not delete the
// stream the other replica recreated, which would lose whatever it holds and restart the
// sequence beneath ids already forwarded.
func TestAReplicaFindingTheStreamAlreadyMovedLeavesItAlone(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("late-replica")
	a, readerA := replicaManager(t, srv, area, 2)
	b, readerB := replicaManager(t, srv, area, 2)
	consumeCaptured(t, readerA)
	consumeCaptured(t, readerB)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, a.MigratePendingRetention(ctx))

	before, err := b.js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, err)
	first := publishCaptured(t, b.js, "between")
	require.NoError(t, b.MigratePendingRetention(ctx))
	after, err := b.js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, err)
	require.True(t, before.Created.Equal(after.Created), "the second replica recreated a stream that was already moved")
	require.EqualValues(t, first+1, publishCaptured(t, b.js, "next"), "the sequence was moved a second time")
	require.Empty(t, b.PendingRetentionMigrations())
}

// A message handed to the reader and not yet acked is work in flight; the stream is not
// drained until it is acked, and nothing is deleted while it is out.
func TestADeliveredButUnackedMessageBlocksTheRecreate(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, reader := replicaManager(t, srv, uniqueArea("inflight"), 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	msg, err := reader.ReadMessage(ctx)
	cancel()
	require.NoError(t, err)

	nmgr.retentionDrainBound = 400 * time.Millisecond
	nmgr.retentionDrainPoll = 20 * time.Millisecond
	err = nmgr.MigratePendingRetention(context.Background())
	require.Error(t, err)
	info, serr := nmgr.js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, serr)
	require.Equal(t, nats.LimitsPolicy, info.Config.Retention)

	require.NoError(t, msg.Ack())
	nmgr.retentionDrainBound = 30 * time.Second
	require.NoError(t, nmgr.MigratePendingRetention(context.Background()))
}

// A consumer some other service or build left on the stream is not this service's work and
// does not hold the migration up.
func TestAForeignConsumerDoesNotBlockTheMigration(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, reader := replicaManager(t, srv, uniqueArea("foreign"), 2)
	_, err := nmgr.js.AddConsumer(captureStreamOn("test"), &nats.ConsumerConfig{
		Durable: "stale-leftover", AckPolicy: nats.AckExplicitPolicy, FilterSubject: StreamSubject("test", captureSuffix),
	})
	require.NoError(t, err)
	consumeCaptured(t, reader)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, nmgr.MigratePendingRetention(ctx))
	info, err := nmgr.js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, err)
	require.Equal(t, nats.WorkQueuePolicy, info.Config.Retention)
}

// flakyJS fails the first AddStream calls, as a broker timeout or a leader change would, and
// fails every SecureDeleteMsg while failDeletes is set.
type flakyJS struct {
	nats.JetStreamContext
	failAdds    atomic.Int32
	addCalls    atomic.Int32
	failDeletes atomic.Bool
}

func (f *flakyJS) AddStream(cfg *nats.StreamConfig, opts ...nats.JSOpt) (*nats.StreamInfo, error) {
	f.addCalls.Add(1)
	if f.failAdds.Add(-1) >= 0 {
		return nil, nats.ErrTimeout
	}
	return f.JetStreamContext.AddStream(cfg, opts...)
}

func (f *flakyJS) SecureDeleteMsg(name string, seq uint64, opts ...nats.JSOpt) error {
	if f.failDeletes.Load() {
		return nats.ErrTimeout
	}
	return f.JetStreamContext.SecureDeleteMsg(name, seq, opts...)
}

// The create that follows the delete is retried: until it lands there is no capture stream,
// and the devices publishing into the gap are being lost.
func TestTheCreateAfterTheDeleteIsRetried(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, reader := replicaManager(t, srv, uniqueArea("retry-add"), 1)
	consumeCaptured(t, reader)
	flaky := &flakyJS{JetStreamContext: nmgr.js}
	flaky.failAdds.Store(3)
	nmgr.js = flaky
	nmgr.retentionAddBackoff = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, nmgr.MigratePendingRetention(ctx))
	require.GreaterOrEqual(t, flaky.addCalls.Load(), int32(4))
	info, err := nmgr.js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, err)
	require.Equal(t, nats.WorkQueuePolicy, info.Config.Retention)
}

// A capture stream created with no predecessor to read (the restart after a failed recreate,
// or a fresh install) does not start at 1.
func TestACaptureStreamWithNoPredecessorStartsPastAnyOldSequence(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	_, err := nmgr.ensureStream(captureSuffix)
	require.NoError(t, err)
	seq := publishCaptured(t, nmgr.js, "first")
	require.Greater(t, seq, uint64(1_000_000_000_000), "a stream with no predecessor started at a small sequence")
	// And the stream survives its own reconcile on a later start.
	_, err = nmgr.ensureStream(captureSuffix)
	require.NoError(t, err)
}

// A give-up on a LIMITS stream keeps its original: the letter's pointer still points at it.
func TestALimitsStreamOriginalSurvivesLettering(t *testing.T) {
	srv := startEmbeddedServer(t)
	rec := &capturedDeliveries{}
	nmgr, readers := recorderRig(t, srv, uniqueArea("limits-keep"), rec.build, streams.RaiseAlarm)
	seq := exhaust(t, nmgr, readers[0], streams.RaiseAlarm)
	keepPulling(t, readers[0])
	waitWithin(t, 10*time.Second, "the give-up to be recorded", func() bool { return len(rec.all()) == 1 })
	waitWithin(t, 3*time.Second, "the advisory to be acked", func() bool { return captureHeld(t, nmgr) == 0 })
	_, err := nmgr.js.GetMsg(StreamName("test", streams.RaiseAlarm), seq)
	require.NoError(t, err, "a lettered original on a Limits stream was removed")
}

// A work-queue give-up for a deleted tenant is removed too: nothing is owed to anyone, and
// it would otherwise sit unacked until it ages out.
func TestADeletedTenantGiveUpIsRemovedFromAWorkQueue(t *testing.T) {
	srv := startEmbeddedServer(t)
	var seen atomic.Int32
	build := func(*NatsManager) (MaxDeliveryFunc, error) {
		return func(context.Context, MaxDelivery) (MaxDeliveryOutcome, error) {
			seen.Add(1)
			return MaxDeliveryTenantDeleted, nil
		}, nil
	}
	nmgr, readers := recorderRig(t, srv, uniqueArea("tenant-gone"), build, captureSuffix)
	seq := publishCaptured(t, nmgr.js, `{"n":1}`)
	for i := 1; i <= MaxDeliver; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := readers[0].ReadMessage(ctx)
		cancel()
		require.NoError(t, err)
	}
	keepPulling(t, readers[0])
	waitWithin(t, 10*time.Second, "the give-up to be handled", func() bool { return seen.Load() >= 1 })
	waitWithin(t, 5*time.Second, "the original to be removed", func() bool { return capturedHeld(t, nmgr.js) == 0 })
	_, err := nmgr.js.GetMsg(captureStreamOn("test"), seq)
	require.ErrorIs(t, err, nats.ErrMsgNotFound)
}

// A removal that fails leaves the advisory unacked, so the step is retried instead of the
// original being left behind with its advisory gone.
func TestAFailedRemovalLeavesTheAdvisoryUnacked(t *testing.T) {
	srv := startEmbeddedServer(t)
	rec := &capturedDeliveries{}
	nmgr, readers := recorderRig(t, srv, uniqueArea("delete-fails"), rec.build, captureSuffix)
	flaky := &flakyJS{JetStreamContext: nmgr.js}
	flaky.failDeletes.Store(true)
	nmgr.js = flaky
	seq := publishCaptured(t, flaky, `{"n":1}`)
	for i := 1; i <= MaxDeliver; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := readers[0].ReadMessage(ctx)
		cancel()
		require.NoError(t, err)
	}
	keepPulling(t, readers[0])
	waitWithin(t, 10*time.Second, "the give-up to be handled", func() bool { return len(rec.all()) >= 1 })
	time.Sleep(1500 * time.Millisecond)
	require.EqualValues(t, 1, captureHeld(t, nmgr), "the advisory was acked although the original was not removed")
	_, err := flaky.GetMsg(captureStreamOn("test"), seq)
	require.NoError(t, err)

	flaky.failDeletes.Store(false)
	waitWithin(t, 15*time.Second, "the retried removal", func() bool { return capturedHeld(t, nmgr.js) == 0 })
	waitWithin(t, 5*time.Second, "the advisory to be acked", func() bool { return captureHeld(t, nmgr) == 0 })
}

func storeFilesHold(t *testing.T, dir, needle string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if err != nil && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			if os.IsNotExist(rerr) {
				return nil
			}
			return rerr
		}
		if bytes.Contains(b, []byte(needle)) {
			found = true
		}
		return nil
	})
	require.NoError(t, err)
	return found
}

// A removed give-up is overwritten, not just unlinked: its bytes are gone from the stream's
// files even while another, unread message keeps the same block in use. The control is the
// scan finding both bodies before the removal.
func TestAGiveUpIsErasedFromDiskEvenWithAnUnreadNeighbour(t *testing.T) {
	dir := dctest.JetStreamStoreDir(t)
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dir})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(15*time.Second))
	t.Cleanup(srv.Shutdown)

	rec := &capturedDeliveries{}
	nmgr, readers := recorderRig(t, srv, uniqueArea("erase"), rec.build, captureSuffix)
	nmgr.stopRecorder(context.Background())

	const given, neighbour = "GIVEN-UP-0123456789", "NEIGHBOUR-9876543210"
	seq := publishCaptured(t, nmgr.js, given)
	for i := 1; i <= MaxDeliver; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := readers[0].ReadMessage(ctx)
		cancel()
		require.NoError(t, err)
	}
	// Pull until the broker has advised, then stop pulling so the neighbour stays unread.
	pctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for pctx.Err() == nil {
			if m, err := readers[0].ReadMessage(pctx); err == nil {
				_ = m.Ack()
			}
		}
	}()
	waitWithin(t, 10*time.Second, "the advisory to be captured", func() bool { return captureHeld(t, nmgr) == 1 })
	stop()
	<-done
	publishCaptured(t, nmgr.js, neighbour)

	waitWithin(t, 10*time.Second, "both bodies to reach the files", func() bool {
		return storeFilesHold(t, dir, given) && storeFilesHold(t, dir, neighbour)
	})
	require.NoError(t, nmgr.startRecorder(context.Background()))
	waitWithin(t, 10*time.Second, "the given-up message to be removed", func() bool {
		_, err := nmgr.js.GetMsg(captureStreamOn("test"), seq)
		return errors.Is(err, nats.ErrMsgNotFound)
	})
	waitWithin(t, 5*time.Second, "the given-up bytes to be erased", func() bool { return !storeFilesHold(t, dir, given) })
	require.True(t, storeFilesHold(t, dir, neighbour), "the unread neighbour must still be stored: the block was still in use")
}

// A replica does not delete the stream while another holds the migration lock: the check, the
// delete and the create are one step, and the lock is what makes them one across replicas.
func TestTheRecreateWaitsForTheMigrationLock(t *testing.T) {
	srv := startEmbeddedServer(t)
	nmgr, reader := replicaManager(t, srv, uniqueArea("locked"), 1)
	consumeCaptured(t, reader)
	waitWithin(t, 10*time.Second, "the backlog to be forwarded", func() bool {
		info, err := nmgr.js.ConsumerInfo(captureStreamOn("test"), nmgr.ownDurable(captureSuffix))
		return err == nil && info.NumPending == 0 && info.NumAckPending == 0 && info.AckFloor.Stream == 1
	})
	lock, err := nmgr.NewDistributedLock(retentionLockTTL)
	require.NoError(t, err)

	released := make(chan struct{})
	holding := make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- lock.WithLock(context.Background(), "stream-retention:"+captureStreamOn("test"), func(context.Context) error {
			close(holding)
			<-released
			return nil
		})
	}()
	<-holding
	before, err := nmgr.js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, err)

	migrated := make(chan error, 1)
	go func() { migrated <- nmgr.MigratePendingRetention(context.Background()) }()
	time.Sleep(1500 * time.Millisecond)
	mid, err := nmgr.js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, err)
	require.Equal(t, nats.LimitsPolicy, mid.Config.Retention, "the stream was recreated while another replica held the lock")
	require.True(t, before.Created.Equal(mid.Created))

	close(released)
	require.NoError(t, <-held)
	select {
	case err := <-migrated:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the migration did not proceed once the lock was released")
	}
	after, err := nmgr.js.StreamInfo(captureStreamOn("test"))
	require.NoError(t, err)
	require.Equal(t, nats.WorkQueuePolicy, after.Config.Retention)
}
