// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

func limitTop(mt MessageType, node string) Topic {
	return Topic{GroupID: "g", EdgeNodeID: node, MessageType: mt}
}

func TestPlaceholderFloodIsBoundedAndKeepsOnlineNode(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.Observe(nTop(NBIRTH), pl(0, bdSeqM(1)))
	for i := 0; i < 100_000; i++ {
		obs := h.tr.Observe(limitTop(NDATA, fmt.Sprint(i)), pl(1))
		if i >= maxPlaceholderSessions {
			require.Equal(t, "placeholder_cap", obs.Refused)
			require.Empty(t, obs.Samples)
			require.Empty(t, obs.Presence)
		}
	}
	require.Len(t, h.tr.sessions, maxPlaceholderSessions+1)
	require.Equal(t, maxPlaceholderSessions, h.rec.count())
	obs := h.tr.Observe(nTop(NDATA), pl(1, &sppb.Payload_Metric{
		Name: proto.String("temperature"), Datatype: proto.Uint32(10),
		Value: &sppb.Payload_Metric_DoubleValue{DoubleValue: 42},
	}))
	require.Len(t, obs.Samples, 1)
	require.Equal(t, maxPlaceholderSessions, h.rec.count())
}

func TestPlaceholderExpiryUsesLastTraffic(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.placeholderLimit = 1
	h.tr.Observe(nTop(NDATA), pl(1))
	h.now = h.now.Add(placeholderIdleTTL - time.Second)
	h.tr.Observe(nTop(NDEATH), pl(-1)) // traffic touches the placeholder without updating rebirth backoff
	h.now = h.now.Add(2 * time.Second)
	require.Equal(t, "placeholder_cap", h.tr.Observe(limitTop(NDATA, "new"), pl(1)).Refused)
	h.now = h.now.Add(placeholderIdleTTL)
	require.Empty(t, h.tr.Observe(limitTop(NDATA, "new"), pl(1)).Refused)
	require.Nil(t, h.sess())
	require.Len(t, h.tr.sessions, 1)
}

func TestBirthCapacityAndOfflineEviction(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.sessionLimit = 2
	a := h.tr.Observe(limitTop(NBIRTH, "a"), pl(0, bdSeqM(1)))
	h.tr.Observe(limitTop(NBIRTH, "b"), pl(0, bdSeqM(1)))
	refused := h.tr.Observe(limitTop(NBIRTH, "c"), pl(0, valuedBirth("temperature", 1, 42)))
	require.Equal(t, "session_cap", refused.Refused)
	require.Empty(t, refused.Samples)
	require.Empty(t, refused.Presence)
	require.Zero(t, h.rec.count())
	require.Empty(t, h.tr.Observe(limitTop(NBIRTH, "a"), pl(0, bdSeqM(2))).Refused)
	h.tr.Observe(limitTop(NDEATH, "a"), pl(-1, bdSeqM(2)))
	obs := h.tr.Observe(limitTop(NBIRTH, "c"), pl(0))
	require.Empty(t, obs.Refused)
	require.Len(t, obs.Presence, 1)
	require.Greater(t, obs.Presence[0].SessionId, a.Presence[0].SessionId)
	require.Nil(t, h.tr.sessions[nodeKey{"g", "a"}])
	require.True(t, h.tr.NodeOnline("g", "b"))
	require.Zero(t, h.tr.nonOnline)
	h.tr.Observe(limitTop(NDEATH, "c"), pl(-1))
	h.now = h.now.Add(offlineIdleTTL)
	h.tr.SweepIdle()
	require.Len(t, h.tr.sessions, 1)
	require.True(t, h.tr.NodeOnline("g", "b"))
	require.Zero(t, h.tr.nonOnline)
}

func TestPlaceholderPromotionAndTotalCap(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.sessionLimit = 1
	h.tr.Observe(nTop(NDATA), pl(1))
	require.Equal(t, "session_cap", h.tr.Observe(limitTop(NDATA, "other"), pl(1)).Refused)
	require.Empty(t, h.tr.Observe(nTop(NBIRTH), pl(0)).Refused)
	require.Zero(t, h.tr.placeholders)
	require.Zero(t, h.tr.nonOnline)
}

func TestStoredNodeKeyOwnsItsStrings(t *testing.T) {
	for _, mt := range []MessageType{NBIRTH, NDATA} {
		t.Run(string(mt), func(t *testing.T) {
			h := newHarness(defaultRebirthBackoff)
			topic := strings.Join([]string{Namespace, "group", string(mt), "node"}, "/")
			top, err := ParseTopic(topic)
			require.NoError(t, err)
			h.tr.Observe(top, pl(0))
			for key := range h.tr.sessions {
				require.False(t, unsafe.StringData(top.GroupID) == unsafe.StringData(key.group))
				require.False(t, unsafe.StringData(top.EdgeNodeID) == unsafe.StringData(key.node))
			}
		})
	}
}

func TestSeenFloodIsBoundedAndRecentTrafficSurvives(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_capacity_refusals"}, []string{"reason"})
	c := &Client{now: func() time.Time { return now }, probeWindow: time.Minute, probeInterval: time.Hour,
		metrics: Metrics{SessionRefused: counter}}
	c.resetSeen()
	for i := 0; i < 500_000; i++ {
		c.markSeen(fmt.Sprint(i))
	}
	require.Len(t, c.seen, maxSeenIDs)
	require.True(t, c.seenSince("0", now))
	// "499999" spoke while the map was full and was not recorded: absence is not silence,
	// so a probe must not declare it dead. A probe that began after the refusal can.
	require.True(t, c.seenSince("499999", now))
	require.False(t, c.seenSince("499999", now.Add(time.Second)))
	require.Equal(t, float64(500_000-maxSeenIDs), testutil.ToFloat64(counter.WithLabelValues("seen_cap")))
	now = now.Add(time.Hour + time.Second)
	c.markSeen("0") // an existing entry can always refresh
	c.markSeen("new")
	require.Len(t, c.seen, 2)
	require.True(t, c.seenSince("0", now))
	require.True(t, c.seenSince("new", now))
	require.False(t, c.seenSince("never", now))
}

func TestProbeRetiresOnlySilentOnlineSessions(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.Observe(nTop(NBIRTH), pl(0))
	h.now = h.now.Add(time.Second)
	cutoff := h.now
	h.tr.markProbeOffline(nodeKey{"g", "n"}, cutoff)
	require.False(t, h.tr.NodeOnline("g", "n"))
	require.Equal(t, 1, h.tr.nonOnline)
	h.tr.markProbeOffline(nodeKey{"g", "n"}, cutoff)
	require.Equal(t, 1, h.tr.nonOnline, "repeat verdict must not double count")
	h.now = h.now.Add(offlineIdleTTL)
	h.tr.SweepIdle()
	require.Empty(t, h.tr.sessions)
	h.tr.Observe(nTop(NBIRTH), pl(0))
	h.tr.markProbeOffline(nodeKey{"g", "n"}, cutoff)
	require.True(t, h.tr.NodeOnline("g", "n"), "traffic after probe start wins")
}

func TestCapacityWarningsAreQuiet(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := &Client{now: func() time.Time { return now }}
	require.True(t, c.warnCapacity(&c.queueWarnedAt))
	require.False(t, c.warnCapacity(&c.queueWarnedAt))
	now = now.Add(recoveryLogQuiet)
	require.True(t, c.warnCapacity(&c.queueWarnedAt))
}

func TestRecoveryTickSweepsIdleSessions(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.Observe(nTop(NDATA), pl(1))
	h.now = h.now.Add(placeholderIdleTTL)
	c := &Client{now: func() time.Time { return h.now }, sessions: h.tr}
	c.recoverOnce()
	require.Empty(t, h.tr.sessions)
}

func TestProbeVerdictRetiresTrackerSession(t *testing.T) {
	for _, id := range []string{"g/n", "g/n/d"} {
		t.Run(id, func(t *testing.T) {
			h := newHarness(defaultRebirthBackoff)
			h.tr.Observe(nTop(NBIRTH), pl(0))
			h.tr.Observe(dTop(DBIRTH, "d"), pl(1))
			h.now = h.now.Add(time.Second)
			c, _ := loopClient(&fakeIngester{}, nil, Metrics{})
			c.now = func() time.Time { return h.now }
			c.sessions = h.tr
			c.reconcileProbe(context.Background(), []AssertedDevice{{ExternalId: id}}, h.now, ProbeTriggerPeriodic)
			require.False(t, h.tr.NodeOnline("g", "n"))
		})
	}
}

func TestTopicIdentityLengthCeiling(t *testing.T) {
	for _, index := range []int{1, 3, 4} {
		parts := []string{Namespace, "g", "DBIRTH", "n", "d"}
		parts[index] = strings.Repeat("界", 256)
		_, err := ParseTopic(strings.Join(parts, "/"))
		require.NoError(t, err)
		parts[index] += "界"
		_, err = ParseTopic(strings.Join(parts, "/"))
		require.Error(t, err)
	}
}

func TestBirthReclaimsOldestOfflineBatch(t *testing.T) {
	h := newHarness(defaultRebirthBackoff)
	h.tr.sessionLimit = 71
	h.tr.Observe(nTop(NBIRTH), pl(0))
	for i := 0; i < 70; i++ {
		h.now = h.now.Add(time.Second)
		node := fmt.Sprint(i)
		h.tr.Observe(limitTop(NBIRTH, node), pl(0))
		h.tr.Observe(limitTop(NDEATH, node), pl(-1))
	}
	h.tr.Observe(limitTop(NBIRTH, "new"), pl(0))
	require.Len(t, h.tr.sessions, 8)
	require.Equal(t, 6, h.tr.nonOnline)
	require.True(t, h.tr.NodeOnline("g", "n"))
	require.Nil(t, h.tr.sessions[nodeKey{"g", "0"}])
	require.NotNil(t, h.tr.sessions[nodeKey{"g", "69"}])
}

func TestReceiveCountsSessionRefusals(t *testing.T) {
	for _, decodeOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(decodeOnly), func(t *testing.T) {
			counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "receive_capacity_refusals"}, []string{"reason"})
			fake := &fakeIngester{}
			c, _ := loopClient(fake, nil, Metrics{SessionRefused: counter})
			if decodeOnly {
				c.ingester = nil
			}
			c.sessions.placeholderLimit = 1
			for _, node := range []string{"a", "b", "b"} {
				c.onMessage(nil, fakeMessage{topic: "spBv1.0/g/NDATA/" + node, payload: encodeT(t, pl(1))})
			}
			require.Equal(t, float64(2), testutil.ToFloat64(counter.WithLabelValues("placeholder_cap")))
			require.Len(t, c.rebirthCh, 1)
			require.Zero(t, fake.ingestedSamples())
			require.Zero(t, fake.presenceCount())
		})
	}
}

func TestFloodWarningsAreRateLimited(t *testing.T) {
	logs := captureLog(t)
	now := time.Unix(1_700_000_000, 0)
	c, _ := loopClient(&fakeIngester{}, nil, Metrics{})
	c.now = func() time.Time { return now }
	c.rebirthCh = make(chan nodeKey)
	c.tenant = "r201-debug"
	c.logRecord(Record{Topic: nTop(NDATA)})
	require.Contains(t, logs.String(), `"level":"debug","tenant":"r201-debug"`)
	for i := 0; i < 5; i++ {
		c.enqueueRebirth("g", "n")
		c.countSessionRefusal("session_cap")
	}
	require.Equal(t, 1, strings.Count(logs.String(), "Rebirth queue full"))
	require.Equal(t, 1, strings.Count(logs.String(), "Sparkplug tracking capacity exhausted"))
	now = now.Add(recoveryLogQuiet)
	c.enqueueRebirth("g", "n")
	c.countSessionRefusal("session_cap")
	require.Equal(t, 2, strings.Count(logs.String(), "Rebirth queue full"))
	require.Equal(t, 2, strings.Count(logs.String(), "Sparkplug tracking capacity exhausted"))
}
