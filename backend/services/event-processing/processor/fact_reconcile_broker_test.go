// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	dmprocessor "github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// dropNthWriter forwards to a real broker writer, except for its nth write, which it refuses the
// way a broker outage longer than the publish wait does — the fact never reaches the stream.
type dropNthWriter struct {
	inner messaging.MessageWriter
	n     int64
	count atomic.Int64
}

func (w *dropNthWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	if w.count.Add(1) == w.n {
		return errors.New("nats: timeout")
	}
	return w.inner.WriteMessages(ctx, msgs...)
}

func (w *dropNthWriter) WriteToDevice(ctx context.Context, device string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}

func (w *dropNthWriter) HandleResponse(err error) { w.inner.HandleResponse(err) }

// The whole failure, on a real broker: device-management publishes two versions of a profile and
// the second announcement never reaches the stream; this service's real consumer, reading the real
// durable, persists the first and never hears of the second. The stream itself is the witness that
// the drop happened where production drops it. The sweep then heals it.
func TestAFactDroppedOnTheStreamIsHealedByTheSweep(t *testing.T) {
	b := startDetectBroker(t)
	var writer messaging.MessageWriter
	var reader messaging.MessageReader
	b.brokerManager(t, func(m *messaging.NatsManager) error {
		w, err := m.NewWriter(streams.DetectionRulesPublished)
		if err != nil {
			return err
		}
		r, err := m.NewReader(streams.DetectionRulesPublished)
		writer, reader = w, r
		return err
	})

	dm := newDmWorld(t)
	failures := prometheus.NewCounter(prometheus.CounterOpts{Name: "fact_publish_failures_total", Help: "test"})
	dm.api.DetectionRulesPublishedPublisher = dmprocessor.NewDetectionRulesPublishedWriter(
		&dropNthWriter{inner: writer, n: 2}, failures)
	dm.profile("p", map[string]string{"hot": hotRule})
	dm.deviceType("sensor", "p")
	dm.device("d1", "sensor")

	rig := newReconcileRig(t, dm)
	consumerCtx, cancel := context.WithCancel(context.Background())
	rig.rp.procCtx, rig.rp.procCancel = consumerCtx, cancel
	rig.rp.RuleUpdatesReader = reader
	rig.rp.readerWG.Add(1)
	go rig.rp.runRuleConsumer()
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			cancel()
			rig.rp.readerWG.Wait()
		}
	}
	defer stop()

	dm.publish("p")
	require.Eventually(t, func() bool {
		active, found, err := rig.rp.ProfileActiveStore.Load(context.Background(), "acme", "p")
		return err == nil && found && active.ActiveVersionToken == "p@1"
	}, 10*time.Second, 20*time.Millisecond, "the consumer never persisted the delivered p@1 fact")
	dm.publish("p") // this announcement is refused by the broker writer

	// VERIFY THE HARNESS: the stream holds exactly the one delivered fact, and device-management
	// counted the one it could not send. Without these, "the sweep repaired it" could be a
	// consumer that simply had not caught up yet.
	require.Equal(t, uint64(1), b.streamHeld(t, streams.DetectionRulesPublished))
	require.Equal(t, 1.0, testutil.ToFloat64(failures))
	time.Sleep(200 * time.Millisecond)
	stop() // the consumer has had every chance; nothing more is coming
	rig.rp.procCtx = context.Background()
	rig.pump()
	if n := rig.measure("d1", "p@2", "90"); n != 0 {
		t.Fatalf("control: the unannounced version fired %d derived events", n)
	}

	rig.sweep()
	if n := rig.measure("d1", "p@2", "90"); n != 1 {
		t.Fatalf("after the sweep an event on p@2 published %d derived events, want 1", n)
	}
	require.Equal(t, 1.0, rig.repairs(projectionRules))
	require.Equal(t, 1.0, rig.repairs(projectionProfileActive))
}
