// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestShrinkRefusalDecision(t *testing.T) {
	have := nats.StreamConfig{MaxBytes: 10 << 20, MaxMsgs: 1000}
	holds := nats.StreamState{Bytes: 8 << 20, Msgs: 800}

	// Lowered below what it holds, on a stream sized by time: refused, naming stream and shortfall.
	msg := shrinkRefusal(streams.ResolvedEvents, "dc_resolved-events", have, streamBounds{maxBytes: 4 << 20, maxMsgs: 1000}, holds)
	require.Contains(t, msg, "dc_resolved-events")
	require.Contains(t, msg, "4 MiB over")
	require.Contains(t, msg, "--allow-ceiling-shrink")
	msg = shrinkRefusal(streams.DeviceEventsCapture, "cap", have, streamBounds{maxBytes: 10 << 20, maxMsgs: 500}, holds)
	require.Contains(t, msg, "300 over")

	// Negative controls: raised, lowered to something it still fits under, unchanged, and a
	// stream that is not a runway (bounded history, as before).
	require.Empty(t, shrinkRefusal(streams.ResolvedEvents, "n", have, streamBounds{maxBytes: 20 << 20, maxMsgs: 5000}, holds))
	require.Empty(t, shrinkRefusal(streams.ResolvedEvents, "n", have, streamBounds{maxBytes: 9 << 20, maxMsgs: 900}, holds))
	require.Empty(t, shrinkRefusal(streams.ResolvedEvents, "n", have, have2(have), holds))
	require.Empty(t, shrinkRefusal(streams.DeviceRoster, "n", have, streamBounds{maxBytes: 1 << 20, maxMsgs: 10}, holds))
}

func have2(c nats.StreamConfig) streamBounds {
	return streamBounds{maxBytes: c.MaxBytes, maxMsgs: c.MaxMsgs}
}

// The reconcile refuses to lower a per-event stream below what it holds, leaves it as it was,
// and does it only when the override is given.
func TestTheReconcileRefusesToShrinkBelowWhatAStreamHolds(t *testing.T) {
	nmgr := startDedupManager(t, "runway-shrink")
	n := &nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	n.StreamDesignRate, n.StreamRunwayMinutes = 0, 0
	suffix := streams.ResolvedEvents
	name := StreamName(nmgr.Microservice.InstanceId, suffix)
	legacy := &nats.StreamConfig{
		Name: name, Subjects: StreamSubjects(nmgr.Microservice.InstanceId, suffix), Storage: nats.FileStorage,
		Retention: nats.LimitsPolicy, Discard: nats.DiscardOld, MaxAge: streamMaxAge, Replicas: nmgr.effectiveStreamReplicas(),
	}
	applyStreamBounds(legacy, nmgr.streamBounds(suffix))
	_, err := nmgr.js.AddStream(legacy)
	require.NoError(t, err)
	payload := []byte(strings.Repeat("x", 1024))
	for range 8 {
		_, err = nmgr.js.Publish(tenantSubjectFor(t, nmgr, suffix), payload)
		require.NoError(t, err)
	}

	// The configuration now asks for far less than the stream holds.
	n.StreamMaxBytes = 2048
	_, err = nmgr.NewWriter(suffix)
	require.Error(t, err)
	require.Contains(t, err.Error(), name)
	require.Contains(t, err.Error(), "--allow-ceiling-shrink")
	info, err := nmgr.js.StreamInfo(name)
	require.NoError(t, err)
	require.EqualValues(t, 8, info.State.Msgs, "a refused reconcile must not evict anything")
	require.NotEqual(t, int64(2048), info.Config.MaxBytes)

	// The explicit override applies it.
	nmgr.AllowCeilingShrink()
	_, err = nmgr.NewWriter(suffix)
	require.NoError(t, err)
	info, err = nmgr.js.StreamInfo(name)
	require.NoError(t, err)
	require.EqualValues(t, 2048, info.Config.MaxBytes)
	require.Less(t, info.State.Msgs, uint64(8), "the override is the deliberate loss")
}
