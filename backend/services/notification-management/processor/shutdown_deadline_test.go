// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/stretchr/testify/require"
)

type shutdownReader struct {
	messages chan messaging.Message
}

type shutdownAck struct {
	done  chan struct{}
	count atomic.Int32
}

func (a *shutdownAck) Ack() error {
	if a.count.Add(1) == 1 {
		close(a.done)
	}
	return nil
}

// Always release and join the test's Stop caller before retrying teardown. This
// also cleans up the old implementation, whose Stop stays blocked until release.
func shutdownCleanup(t *testing.T, np *NotificationProcessor, finish func(), exited *chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		finish()
		if *exited != nil {
			shutdownWait(t, *exited)
		}
		if np.lifecycle.State == core.Stopped || np.lifecycle.State == core.Terminated {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, np.ExecuteStop(ctx))
	})
}

// Exercise the real clear claim and partial-delivery contract, including a new
// processor over the same persisted state. No delivery must be repeated after a
// successful channel is followed by a channel cancelled at the drain deadline.
func TestNotificationShutdownPartialClearDoesNotSendAgain(t *testing.T) {
	n, _, ctx := clearHarness(t, true)
	db := stateDB(n.api).WithContext(ctx)
	webhook := enabledChannel("webhook-1", model.ChannelTypeWebhook)
	require.NoError(t, db.Create(webhook).Error)
	var p model.NotificationPolicy
	require.NoError(t, db.Where("token = ?", "p").First(&p).Error)
	r := rule(model.SeverityAny, nil, "ops@x.com")
	r.PolicyId, r.ChannelId = p.ID, webhook.ID
	require.NoError(t, db.Create(&r).Error)
	n.adapters[model.ChannelTypeWebhook] = adapterFunc(func(context.Context, *RenderedNotification) error { return nil })
	raised := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)))

	gate := newShutdownNotifier()
	var sent atomic.Int32
	var attempts atomic.Int32
	ad := adapterFunc(func(c context.Context, _ *RenderedNotification) error {
		if attempts.Add(1) == 1 {
			sent.Add(1)
			return nil
		}
		return gate.Notify(c, nil)
	})
	n.adapters[model.ChannelTypeSMTP], n.adapters[model.ChannelTypeWebhook] = ad, ad
	// Only Stop may expire this dispatch; the adapter's ordinary timeout must
	// not satisfy the cancellation assertion on its own.
	n.budget, n.timeout, n.attempts = time.Hour, time.Hour, 1
	clear := cycleEvent(dmmodel.AlarmEventCleared, raised, raised.Add(time.Minute))
	value, err := dmproto.MarshalAlarmStateChangeEvent(clear)
	require.NoError(t, err)
	ack := &recordingAck{}
	msg := msgWith("instance1.acme.alarm-events", value, messaging.MaxDeliver, ack)
	np, dead := shutdownProcessor(t, n, msg)
	var finishOnce sync.Once
	finish := func() { gate.unblock(); finishOnce.Do(func() { close(gate.finish) }) }
	var exited chan struct{}
	shutdownCleanup(t, np, finish, &exited)
	shutdownWait(t, gate.active)
	require.Equal(t, int32(1), sent.Load(), "one channel must succeed before the blocked channel")
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	// The test-level timeout also protects the regression case that ignores Stop.
	stopped := make(chan error, 1)
	exited = make(chan struct{})
	go func() { defer close(exited); stopped <- np.Stop(stopCtx) }()
	select {
	case err := <-stopped:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(5 * time.Second):
		t.Fatal("Stop ignored its deadline")
	}
	shutdownWait(t, gate.cancelled)
	finish()
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cleanupCancel()
	require.NoError(t, np.Stop(cleanupCtx))
	require.Equal(t, 1, ack.acked, "partial delivery is final for the consumer")
	require.Empty(t, dead.msgs)

	// A replacement process starts with the same durable state. Even an
	// at-least-once redelivery must find the clear claim already spent.
	redelivery := adapterFunc(func(context.Context, *RenderedNotification) error {
		sent.Add(1)
		return nil
	})
	n.adapters[model.ChannelTypeSMTP], n.adapters[model.ChannelTypeWebhook] = redelivery, redelivery
	nextAck := &shutdownAck{done: make(chan struct{})}
	next, nextDead := shutdownProcessor(t, n, msgWith(msg.Subject, value, 1, nextAck))
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, next.ExecuteStop(c))
	})
	shutdownWait(t, nextAck.done)
	require.NoError(t, next.Stop(cleanupCtx))
	require.Equal(t, int32(1), nextAck.count.Load())
	require.Equal(t, int32(1), sent.Load(), "redelivered clear must not repeat a successful delivery")
	require.Empty(t, nextDead.msgs)
}

func (r *shutdownReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	select {
	case msg := <-r.messages:
		return msg, nil
	case <-ctx.Done():
		return messaging.Message{}, ctx.Err()
	}
}

func (*shutdownReader) HandleResponse(error) {}

type shutdownNotifier struct {
	active, cancelled, release, finish chan struct{}
	releaseOnce                        sync.Once
	err                                error
}

func newShutdownNotifier() *shutdownNotifier {
	return &shutdownNotifier{active: make(chan struct{}), cancelled: make(chan struct{}),
		release: make(chan struct{}), finish: make(chan struct{})}
}

func (n *shutdownNotifier) unblock() { n.releaseOnce.Do(func() { close(n.release) }) }

func (n *shutdownNotifier) Notify(ctx context.Context, _ *dmmodel.AlarmStateChangeEvent) error {
	close(n.active)
	select {
	case <-ctx.Done():
		close(n.cancelled)
		// Keep the worker here until the test has observed Stop returning. Stop
		// must be bounded even if a notifier takes time to unwind cancellation.
		<-n.release
		<-n.finish
		if n.err != nil {
			return n.err
		}
		return ctx.Err()
	case <-n.release:
		<-n.finish
		return nil
	}
}

func shutdownWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown handshake did not finish")
	}
}

func shutdownProcessor(t *testing.T, n Notifier, msg messaging.Message) (*NotificationProcessor, *deadRecorder) {
	t.Helper()
	dead := &deadRecorder{}
	np, _ := processorWithDeadLettersAndRegistry(n, dead)
	r := &shutdownReader{messages: make(chan messaging.Message, 1)}
	r.messages <- msg
	np.Reader = r
	require.NoError(t, np.Initialize(context.Background()))
	require.NoError(t, np.Start(context.Background()))
	return np, dead
}

func TestNotificationShutdownDeadline(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%v", partial), func(t *testing.T) {
			n := newShutdownNotifier()
			if partial {
				n.err = fmt.Errorf("one channel sent before cancellation: %w", core.ErrPassPartial)
			}
			ack := &recordingAck{}
			np, dead := shutdownProcessor(t, n, msgWith(testAlarmSubject, validEventBytes(t), messaging.MaxDeliver, ack))
			var finishOnce sync.Once
			finish := func() { n.unblock(); finishOnce.Do(func() { close(n.finish) }) }
			var exited chan struct{}
			shutdownCleanup(t, np, finish, &exited)
			shutdownWait(t, n.active)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			stopped := make(chan error, 1)
			exited = make(chan struct{})
			go func() { defer close(exited); stopped <- np.Stop(ctx) }()
			select {
			case err := <-stopped:
				require.ErrorIs(t, err, context.DeadlineExceeded)
			case <-time.After(5 * time.Second):
				t.Fatal("Stop ignored its deadline")
			}
			shutdownWait(t, n.cancelled)
			// Retrying an expired teardown does not close the channel twice or
			// start a new waiter while the original worker is still unwinding.
			for i := 0; i < 3; i++ {
				require.ErrorIs(t, np.Stop(ctx), context.DeadlineExceeded)
			}
			finish()
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			require.NoError(t, np.Stop(cleanupCtx))
			wantAck := 0
			if partial {
				wantAck = 1
			}
			require.Equal(t, wantAck, ack.acked)
			require.Empty(t, dead.msgs)
			require.NoError(t, np.Terminate(cleanupCtx))
		})
	}
}

func TestNotificationShutdownDrainsBeforeDeadline(t *testing.T) {
	n := newShutdownNotifier()
	ack := &recordingAck{}
	np, dead := shutdownProcessor(t, n, msgWith(testAlarmSubject, validEventBytes(t), messaging.MaxDeliver, ack))
	var finishOnce sync.Once
	finish := func() { n.unblock(); finishOnce.Do(func() { close(n.finish) }) }
	var exited chan struct{}
	shutdownCleanup(t, np, finish, &exited)
	shutdownWait(t, n.active)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopped := make(chan error, 1)
	exited = make(chan struct{})
	go func() { defer close(exited); stopped <- np.Stop(ctx) }()
	shutdownWait(t, np.procCtx.Done()) // Stop has begun, but dispatch must stay live.
	select {
	case <-n.cancelled:
		t.Fatal("graceful Stop cancelled an active dispatch")
	default:
	}
	finish()
	require.NoError(t, <-stopped)
	require.Equal(t, 1, ack.acked)
	require.Empty(t, dead.msgs)
	require.NoError(t, np.ExecuteStop(ctx))
	require.NoError(t, np.Terminate(ctx))
}
