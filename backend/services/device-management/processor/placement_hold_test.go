// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

// placementWriter is an OrderedWriter that settles every submission in order on a goroutine of
// its own (as the real writer's settle goroutine does), with the outcome refuse decides.
type placementWriter struct {
	mu        sync.Mutex
	refuse    func(messaging.Message) error
	published []messaging.Message
	settles   chan func()
	done      chan struct{}
	closeOnce sync.Once
}

func newPlacementWriter(refuse func(messaging.Message) error) *placementWriter {
	w := &placementWriter{refuse: refuse, settles: make(chan func(), 1024), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for f := range w.settles {
			f()
		}
	}()
	return w
}

func (w *placementWriter) Publish(ctx context.Context, msg messaging.Message, done func(error)) {
	w.mu.Lock()
	w.published = append(w.published, msg)
	refuse := w.refuse
	w.mu.Unlock()
	err := refuse(msg)
	w.settles <- func() { done(err) }
}

func (w *placementWriter) Fail(err error, done func(error)) { w.settles <- func() { done(err) } }
func (w *placementWriter) Draining()                        {}
func (w *placementWriter) Close() {
	w.closeOnce.Do(func() { close(w.settles) })
	<-w.done
}

func (w *placementWriter) ids() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.published))
	for _, m := range w.published {
		out = append(out, m.DedupID)
	}
	return out
}

// beatAck counts the acks and InProgress beats of one source.
type beatAck struct{ acks, beats atomic.Int32 }

func (a *beatAck) Ack() error        { a.acks.Add(1); return nil }
func (a *beatAck) InProgress() error { a.beats.Add(1); return nil }

const holdTenant = "acme"

func holdSource(seq uint64, delivery int, ack *beatAck) messaging.Message {
	src := messaging.NewConsumedMessage(messaging.ScopedSubject("inst", holdTenant, streams.InboundEvents),
		nil, delivery, nil, ack)
	src.StreamSeq = seq
	return src
}

// startHoldProcessor starts a processor over w whose placement check answers ready().
func startHoldProcessor(t *testing.T, w messaging.OrderedWriter, ready func(string) error) *InboundEventsProcessor {
	t.Helper()
	return startHoldProcessorWith(t, w, ready, ResolveMetrics{})
}

// startHoldProcessorWith is startHoldProcessor with the given instruments.
func startHoldProcessorWith(t *testing.T, w messaging.OrderedWriter, ready func(string) error, metrics ResolveMetrics) *InboundEventsProcessor {
	t.Helper()
	failed := newPlacementWriter(func(messaging.Message) error { return nil })
	iproc := NewInboundEventsProcessor(&core.Microservice{InstanceId: "inst", FunctionalArea: "device-management"},
		[]messaging.MessageReader{&sliceReader{}}, w, failed, core.NewNoOpLifecycleCallbacks(), nil,
		config.AuthModeOptional, time.Duration(config.DefaultMaxEventFutureSkewSeconds)*time.Second, metrics,
		WithPlacementCheck(ready))
	iproc.placementHoldBeat, iproc.placementHoldPoll = 5*time.Millisecond, 5*time.Millisecond
	require.NoError(t, iproc.Initialize(context.Background()))
	require.NoError(t, iproc.Start(context.Background()))
	return iproc
}

func unplaced() error { return fmt.Errorf("publish: %w", messaging.ErrUnplacedTenant) }

func countID(ids []string, id string) int {
	n := 0
	for _, x := range ids {
		if x == id {
			n++
		}
	}
	return n
}

// A FINAL delivery whose resolved fan-out is partly refused for shard placement is held, not
// exhausted: the broker never redelivers a final delivery, so left unacked it would be lettered
// by the max-delivery recorder and its event never resolved. Once the tenant's placement answers,
// ONLY the refused outputs are published again (under the same dedup ids), and the source is
// acked once, after they are stored.
func TestAFinalDeliveryRefusedForPlacementRepublishesOnlyTheRefusedOutputs(t *testing.T) {
	var placed atomic.Bool
	w := newPlacementWriter(func(m messaging.Message) error {
		if !placed.Load() && m.DedupID == "r:acme:1:1" {
			return unplaced()
		}
		return nil
	})
	iproc := startHoldProcessor(t, w, func(tenant string) error {
		if tenant != holdTenant {
			return errors.New("asked about the wrong tenant")
		}
		if !placed.Load() {
			return unplaced()
		}
		return nil
	})
	ack := &beatAck{}
	iproc.OnResolvedEvent(holdSource(1, messaging.MaxDeliver, ack), holdTenant,
		[]EventResolutionResults{gateEvent(0), gateEvent(1), gateEvent(2)})

	require.Eventually(t, func() bool { return len(w.ids()) == 3 }, 2*time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return ack.beats.Load() >= 3 }, 2*time.Second, time.Millisecond,
		"a held final delivery is kept alive with InProgress")
	require.Zero(t, ack.acks.Load(), "nothing is acked while an output is unstored")
	require.Len(t, w.ids(), 3, "nothing is republished while placement is unavailable")

	placed.Store(true)
	require.Eventually(t, func() bool { return ack.acks.Load() == 1 }, 2*time.Second, time.Millisecond,
		"the source is acked once its refused output is stored")
	ids := w.ids()
	require.Equal(t, 2, countID(ids, "r:acme:1:1"), "the refused output is published again, same dedup id")
	require.Equal(t, 1, countID(ids, "r:acme:1:0"), "a stored output is not published again")
	require.Equal(t, 1, countID(ids, "r:acme:1:2"), "a stored output is not published again")
	stopWithin(t, iproc, 5*time.Second)
	require.EqualValues(t, 1, ack.acks.Load())
}
