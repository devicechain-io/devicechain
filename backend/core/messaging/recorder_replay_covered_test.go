// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/streams"
)

// A durable declared replay-covered for its area (streams.Stream.ReplayCovered) is counted,
// not recorded, and a durable on the SAME stream that is not declared is recorded as before.
//
// Both halves use the one stream the tree declares this way, because the declaration is per
// area and the failure it exists to prevent is a per-stream reading of it: resolved-events is re-read from a checkpoint by event-processing, while event-management
// persists it through an ordinary durable, where an exhausted delivery is an event that was
// never stored. The areas are named literally because the declaration is. Each half has its
// own broker only so that each durable's first message is the one it exhausts.
func TestAReplayCoveredDurableIsCountedNotRecorded(t *testing.T) {
	srv := startEmbeddedServer(t)
	stream := StreamName("test", streams.ResolvedEvents)

	covered := &capturedDeliveries{}
	coveredMgr, coveredReaders := recorderRig(t, srv, "event-processing", covered.build, streams.ResolvedEvents)
	require.Equal(t, 2, testutil.CollectAndCount(coveredMgr.metrics.maxDeliveryRecords),
		"a replay-covered durable's series are replay-covered and malformed, at zero, and nothing else")
	require.Zero(t, testutil.ToFloat64(coveredMgr.metrics.maxDeliveryRecords.WithLabelValues(stream, "replay-covered")))

	seq := exhaust(t, coveredMgr, coveredReaders[0], streams.ResolvedEvents)
	keepPulling(t, coveredReaders[0])
	waitWithin(t, 8*time.Second, "the replay-covered exhaustion to be counted", func() bool {
		return testutil.ToFloat64(coveredMgr.metrics.maxDeliveryRecords.WithLabelValues(stream, "replay-covered")) == 1
	})
	waitWithin(t, 3*time.Second, "the advisory to be acked off the work queue", func() bool {
		return captureHeld(t, coveredMgr) == 0
	})
	require.Empty(t, covered.all(),
		"the record func was handed seq %d from a replay-covered durable: it would be dead-lettered as lost", seq)

	lettered := &capturedDeliveries{}
	letteredMgr, letteredReaders := recorderRig(t, startEmbeddedServer(t), "event-management", lettered.build, streams.ResolvedEvents)
	exhaust(t, letteredMgr, letteredReaders[0], streams.ResolvedEvents)
	keepPulling(t, letteredReaders[0])
	waitWithin(t, 8*time.Second, "event-management's exhaustion to be recorded", func() bool {
		return len(lettered.all()) >= 1
	})
	require.Equal(t, DurableName("test", "event-management", streams.ResolvedEvents), lettered.all()[0].Consumer)
	require.Equal(t, 1.0, testutil.ToFloat64(letteredMgr.metrics.maxDeliveryRecords.WithLabelValues(stream, "lettered")))
	require.Zero(t, testutil.ToFloat64(letteredMgr.metrics.maxDeliveryRecords.WithLabelValues(stream, "replay-covered")),
		"event-management's exhaustion was counted as replay-covered")
}
