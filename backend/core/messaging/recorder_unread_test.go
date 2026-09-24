// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// GUARD: the max-delivery recorder's capture durable is never sampled for unread loss.
//
// The unread-loss counter (sampleDurable) rests on one precondition: the durable filters on its
// stream's WHOLE subject, so every sequence its cursor passes is one it would have been handed.
// The recorder's durable breaks that by design. The capture stream holds every area's
// advisories and each area's recorder filters to its own durables' subjects, so its cursor
// steps over every other area's advisory without a delivery — and each of those would be
// counted as a message this service lost, on the counter the critical
// JetStreamDurableLostUnread alert reads.
//
// Nothing filters it out of the sampler. It stays out because the sampler reads the durables
// in nmgr.readers and the recorder is not one: NewReader refuses the capture stream, and
// startRecorder keeps its durable on nmgr.recorder. So this checks the OUTPUT a scrape reads,
// with a real broker and another area's advisory on the capture stream ahead of this area's
// own — the state in which a sampled recorder durable would report a loss.
func TestTheRecorderDurableIsNotSampledForUnreadLoss(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("unsampled")
	ms := testMicroservice(t, srv, area)
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	var reader MessageReader
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(n *NatsManager) error {
		r, err := n.NewReader(streams.RaiseAlarm)
		reader = r
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
	nmgr.sampleNow(ctx) // the baseline every durable's counter is differenced against

	// Another area's advisory, captured and never taken by this area's filter.
	_, err := nmgr.js.Publish(AdvisorySubject(StreamName("test", streams.RaiseAlarm), "test_another-area_raise-alarm"),
		[]byte(`{"type":"io.nats.jetstream.advisory.v1.max_deliver"}`))
	require.NoError(t, err)
	// Then this area's own, which its recorder takes — stepping its cursor over the one above.
	exhaust(t, nmgr, reader, streams.RaiseAlarm)
	keepPulling(t, reader)
	recorder := DurableName("test", area, streams.MaxDeliveries)
	capture := StreamName("test", streams.MaxDeliveries)
	waitWithin(t, 8*time.Second, "the recorder to take its own advisory", func() bool {
		ci, err := nmgr.js.ConsumerInfo(capture, recorder)
		return err == nil && ci.Delivered.Consumer >= 1 && ci.Delivered.Stream >= 2
	})
	nmgr.sampleNow(ctx)

	families, err := reg.Gather()
	require.NoError(t, err)
	prefix := "devicechain_" + strings.ReplaceAll(area, "-", "") + "_"
	readerSeen := false
	for _, f := range families {
		name := strings.TrimPrefix(f.GetName(), prefix)
		if name != skippedSeries && name != gapSeries {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			require.NotEqual(t, capture, labels["stream"],
				"%s is exported for the capture stream (durable %q): the recorder's filtered cursor "+
					"steps over every other area's advisory and would page as unread loss", name, labels["durable"])
			if labels["durable"] == DurableName("test", area, streams.RaiseAlarm) {
				readerSeen = true
			}
		}
	}
	require.True(t, readerSeen, "vacuous: no unread series for the area's own reader, so absence proves nothing")
	for _, d := range nmgr.trackedDurables() {
		require.NotEqual(t, recorder, d.durable, "the recorder's durable is among the sampled readers")
	}
}
