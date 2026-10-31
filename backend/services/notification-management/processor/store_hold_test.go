// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/devicechain-io/dc-microservice/storehold/holdtest"
	"github.com/stretchr/testify/assert"
)

// outageNotifier refuses Notify with err for the first `failures` calls, then delivers. It
// records the ack deadline each call was handed, because a re-run after a hold must measure its
// budget from a fresh one.
type outageNotifier struct {
	failures  int32
	err       error
	calls     atomic.Int32
	deadlines []time.Time
}

func (o *outageNotifier) Notify(ctx context.Context, _ *dmmodel.AlarmStateChangeEvent) error {
	d, _ := messaging.AckDeadlineFrom(ctx)
	o.deadlines = append(o.deadlines, d)
	if o.calls.Add(1) <= o.failures {
		return o.err
	}
	return nil
}

func holdFor(store *holdtest.Store) *storehold.Holder {
	return &storehold.Holder{Name: "test", Probe: store.Probe, Tune: holdtest.FastTuning()}
}

func finishes(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not finish", what)
	}
}

// 🔴 THE REGRESSION: "nobody gets paged". An alarm event whose dispatch the store refuses, on its
// LAST delivery, used to be dead-lettered and acked after a database outage past the delivery
// budget. It must instead be held, dispatched when the store answers, and acked once. The
// control (no hold) shows it lettered.
func TestAnAlarmNotificationIsHeldThroughAStoreOutageNotLettered(t *testing.T) {
	for _, withHold := range []bool{true, false} {
		notifier := &outageNotifier{failures: 3, err: holdtest.DiskFull}
		dead := &deadRecorder{}
		np, _ := processorWithDeadLettersAndRegistry(notifier, dead)
		if withHold {
			np.Hold = holdFor(&holdtest.Store{})
		}
		ack := &recordingAck{}
		finishes(t, "dispatchOne", func() {
			np.dispatchOne(context.Background(), msgWith(testAlarmSubject, validEventBytes(t), messaging.MaxDeliver, ack))
		})
		if withHold {
			assert.Empty(t, dead.msgs, "an event refused by a full store must not be lettered")
			assert.Equal(t, 1, ack.acked)
			assert.Equal(t, int32(4), notifier.calls.Load(), "three refusals, then dispatched")
		} else {
			assert.Len(t, dead.msgs, 1, "control: without a hold the event is lettered at the cap")
		}
	}
}

// A connection-class failure with a LIVE store is a poison event: it takes its delivery budget
// and is lettered at the cap as before.
func TestAPoisonAlarmNotificationStillTakesItsDeliveryBudget(t *testing.T) {
	notifier := &outageNotifier{failures: 1 << 30, err: holdtest.ConnLost}
	dead := &deadRecorder{}
	np, _ := processorWithDeadLettersAndRegistry(notifier, dead)
	np.Hold = holdFor(&holdtest.Store{})
	ack := &recordingAck{}
	finishes(t, "dispatchOne", func() {
		np.dispatchOne(context.Background(), msgWith(testAlarmSubject, validEventBytes(t), messaging.MaxDeliver, ack))
	})
	assert.Len(t, dead.msgs, 1)
	assert.Equal(t, 1, ack.acked)
	assert.Equal(t, int32(1), notifier.calls.Load(), "a poison event is not re-run")
}

// A re-run after a hold measures its dispatch budget from a fresh ack deadline: the deadline a
// message was fetched with is long past by then and would refuse the work at once. (The test
// message comes from no capacity reader, so its first run has none; every re-run carries one.)
func TestAHeldAlarmNotificationIsRerunWithAFreshAckDeadline(t *testing.T) {
	notifier := &outageNotifier{failures: 2, err: holdtest.DiskFull}
	np, _ := processorWithDeadLettersAndRegistry(notifier, &deadRecorder{})
	np.Hold = holdFor(&holdtest.Store{})
	ack := &recordingAck{}
	finishes(t, "dispatchOne", func() {
		np.dispatchOne(context.Background(), msgWith(testAlarmSubject, validEventBytes(t), 1, ack))
	})
	assert.Equal(t, 1, ack.acked)
	if assert.Len(t, notifier.deadlines, 3) {
		assert.True(t, notifier.deadlines[0].IsZero())
		for _, d := range notifier.deadlines[1:] {
			assert.True(t, d.After(time.Now()), "a re-run after a hold must use a fresh, future ack deadline; got %v", d)
		}
	}
}

// Shutdown during a hold leaves the event unacked and unlettered even on its last delivery.
func TestAHeldAlarmNotificationIsLeftUnackedOnShutdown(t *testing.T) {
	notifier := &outageNotifier{failures: 1 << 30, err: holdtest.DiskFull}
	dead := &deadRecorder{}
	np, _ := processorWithDeadLettersAndRegistry(notifier, dead)
	np.Hold = holdFor(&holdtest.Store{})
	ctx, cancel := context.WithCancel(context.Background())
	np.procCtx = ctx
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	ack := &recordingAck{}
	finishes(t, "a held event on shutdown", func() {
		np.dispatchOne(context.Background(), msgWith(testAlarmSubject, validEventBytes(t), messaging.MaxDeliver, ack))
	})
	assert.Empty(t, dead.msgs)
	assert.Zero(t, ack.acked)
}
