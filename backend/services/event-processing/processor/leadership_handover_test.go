// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type handoverReader struct{ binds, reads atomic.Int32 }

func (r *handoverReader) BindTerm() error {
	r.binds.Add(1)
	return nil
}
func (*handoverReader) UnbindTerm() error { return nil }
func (r *handoverReader) ReadMessage(context.Context) (messaging.Message, error) {
	r.reads.Add(1)
	return messaging.Message{}, errors.New("unexpected consumption")
}
func (*handoverReader) HandleResponse(error) {}

func TestAnUncleanPredecessorIsWaitedOutBeforeAnyStateIsRead(t *testing.T) {
	testHandover(t, false, false)
}

func TestACleanPredecessorNeedsNoHandoverWait(t *testing.T) {
	testHandover(t, true, false)
}

func TestCancellationDuringHandoverReleasesTheLeaseWithoutReadingState(t *testing.T) {
	testHandover(t, false, true)
}

func testHandover(t *testing.T, clean, cancelDuringWait bool) {
	t.Helper()
	b := startOutageBroker(t)
	dl, err := b.manager(t).NewDistributedLease(messaging.DefaultLeaseTTL)
	require.NoError(t, err)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DetectSnapshot{}))
	var loads atomic.Int32
	loadErr := errors.New("test: snapshot read reached after handover")
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("handover:load", func(tx *gorm.DB) {
		loads.Add(1)
		tx.AddError(loadErr)
	}))
	rp := newTestProcessor(model.NewSnapshotStore(&rdb.RdbManager{Database: db}), nil, 1)
	ms := &core.Microservice{InstanceId: "handover", FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	rp.metrics = NewDetectMetrics(ms)
	rp.Lease, rp.Gate = dl, NewTermGate()
	require.NoError(t, rp.ExecuteInitialize(context.Background()))
	r := &handoverReader{}
	rp.ResolvedEventsReader = r
	if clean {
		prior, err := dl.Acquire(rp.cfg.PartitionId)
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = prior.ReleaseWithin(ctx)
		cancel()
		require.NoError(t, err)
	}
	require.Equal(t, clean, dl.PriorOwnerReleasedCleanly(rp.cfg.PartitionId))

	waitAsked := make(chan time.Duration, 2)
	timer := make(chan time.Time)
	done := make(chan struct{})
	var termErr error
	t.Cleanup(func() {
		rp.supCancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("handover did not unwind after cancellation")
		}
	})
	go func() {
		defer close(done)
		_, termErr = rp.beginTermWithWait(func(d time.Duration) <-chan time.Time {
			waitAsked <- d
			return timer
		})
	}()

	assertUntouched := func() {
		t.Helper()
		require.Zero(t, loads.Load(), "snapshot read before handover finished")
		require.Zero(t, r.binds.Load(), "consumer bound before handover finished")
		require.Zero(t, r.reads.Load(), "consumer read before handover finished")
	}
	if !clean {
		select {
		case d := <-waitAsked:
			require.Equal(t, termSlack, d)
			require.Equal(t, 20*time.Second, d, "handover safety bound must not shrink")
		case <-done:
			t.Fatalf("term ended without asking for the handover wait: %v", termErr)
		case <-time.After(5 * time.Second):
			t.Fatal("term never asked for the handover wait")
		}
		assertUntouched()
		// Asking for a timer is insufficient: the term must remain parked until
		// that timer fires, even though acquisition and the holder watch succeeded.
		select {
		case <-done:
			t.Fatal("term did not wait for the handover timer")
		case <-time.After(100 * time.Millisecond):
		}
		assertUntouched()
		if cancelDuringWait {
			rp.pcancel()
		} else {
			close(timer)
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("term did not finish after handover or cancellation")
	}
	select {
	case d := <-waitAsked:
		t.Fatalf("unexpected extra handover wait (clean=%v): %v", clean, d)
	default:
	}
	if cancelDuringWait {
		require.ErrorIs(t, termErr, context.Canceled)
		assertUntouched()
	} else {
		require.ErrorIs(t, termErr, loadErr)
		require.ErrorIs(t, termErr, errTermBuildFailed)
		require.EqualValues(t, 1, r.binds.Load(), "consumer spy must observe the build")
		require.EqualValues(t, 1, loads.Load(), "snapshot spy must observe the build")
	}
	require.ErrorIs(t, rp.pctx().Err(), context.Canceled)
	require.False(t, rp.Gate.Held(), "ended term must close its gate")
	// Read the broker's release marker AND reacquire: a local gate alone does
	// not prove the successor can take over after a cancelled handover.
	require.True(t, dl.PriorOwnerReleasedCleanly(rp.cfg.PartitionId))
	next, err := dl.Acquire(rp.cfg.PartitionId)
	require.NoError(t, err, "handover must release the lease")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, next.ReleaseWithin(ctx))
}
