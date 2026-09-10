// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-sparkplug-ingest/config"
)

// queueCounters is the pair the rebirth queue reports itself with, plus the published
// counter that used to be the only one — the tests below read all three together
// because the reading of any one of them depends on the others.
type queueCounters struct {
	enqueued  prometheus.Counter
	dropped   prometheus.Counter
	published prometheus.Counter
}

func newQueueCounters() queueCounters {
	return queueCounters{
		enqueued:  prometheus.NewCounter(prometheus.CounterOpts{Name: "rebirth_enqueued"}),
		dropped:   prometheus.NewCounter(prometheus.CounterOpts{Name: "rebirth_dropped"}),
		published: prometheus.NewCounter(prometheus.CounterOpts{Name: "rebirth_requests"}),
	}
}

func (q queueCounters) metrics() Metrics {
	return Metrics{
		RebirthEnqueued: q.enqueued,
		RebirthDropped:  q.dropped,
		RebirthRequests: q.published,
	}
}

// An accepted rebirth request is counted, and it is counted on the path a real node
// actually takes: the session machine's callback, not a direct call to enqueueRebirth.
//
// 🔴 THE MESSAGE IS DRIVEN THROUGH c.sessions ON PURPOSE. NewClient is what binds
// enqueueRebirth to the tracker, and a test that called the method directly would keep
// passing if that binding were replaced with a callback that counted nothing.
//
// The dropped assertion is the counterweight: an implementation that incremented both
// arms of the select, or the dropped arm unconditionally, would satisfy "enqueued
// moves" and would make the drop series useless.
func TestEnqueueRebirthCountsAnAcceptedRequest(t *testing.T) {
	q := newQueueCounters()
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h"}, Broker{}, nil, fixedNow, q.metrics())

	// Data before any birth: the Host missed the NBIRTH, which is one of the session
	// errors that funnel into a rebirth command.
	c.sessions.Observe(nTop(NDATA), pl(1, dataMetric(1)))

	assert.Equal(t, float64(1), testutil.ToFloat64(q.enqueued),
		"a rebirth accepted onto the queue is counted")
	assert.Equal(t, float64(0), testutil.ToFloat64(q.dropped),
		"an accepted rebirth must not also count as dropped")
	require.Len(t, c.rebirthCh, 1, "the request reached the queue")

	// 🔑 AND THE READING THAT MAKES THE PAIR WORTH HAVING: nothing has been published,
	// because the worker is not running. rebirth_requests_total therefore cannot stand in
	// for "a rebirth was asked for" — which is the gap the enqueued counter closes.
	assert.Equal(t, float64(0), testutil.ToFloat64(q.published),
		"nothing is on the wire yet; the published counter is a different fact")
}

// A full queue drops, and the drop is counted rather than only logged.
//
// It fills the queue the way production does — a FAN-OUT, one node per request, driven
// through c.sessions. That is what fills this queue: the per-node backoff in wantRebirth
// collapses repeats for a single node, so one node cannot fill anything however hard it
// misbehaves (257 observations for one node leave the queue holding exactly 1).
//
// 🔴 THAT COLLAPSE LIVES IN THE TRACKER, ABOVE enqueueRebirth, so it is a property of
// THIS path and not of the method. Calling enqueueRebirth directly 257 times for one node
// fills the queue just as a fan-out does — measured, not assumed — which is why the fill
// is driven through the session machine rather than by direct calls with distinct names
// that would look like they were exercising a backoff they never reach.
func TestEnqueueRebirthCountsADropWhenTheQueueIsFull(t *testing.T) {
	q := newQueueCounters()
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h"}, Broker{}, nil, fixedNow, q.metrics())

	// Data before any birth for each of N distinct nodes: N rebirth requests, none of
	// them collapsed, exactly as a site coming back on line produces.
	fanOut := func(n int) {
		c.sessions.Observe(Topic{GroupID: "g", EdgeNodeID: fmt.Sprintf("n%d", n), MessageType: NDATA},
			pl(1, dataMetric(1)))
	}
	for i := 0; i < rebirthQueueDepth; i++ {
		fanOut(i)
	}
	require.Len(t, c.rebirthCh, rebirthQueueDepth, "the queue is full")
	// The counterweight for the drop assertion below: filling the queue exactly to its
	// depth drops NOTHING. Without this, a drop counter wired to the accepted arm — or
	// incremented on every call — would pass the assertion that follows.
	require.Equal(t, float64(rebirthQueueDepth), testutil.ToFloat64(q.enqueued),
		"every request that fit was counted as accepted")
	require.Equal(t, float64(0), testutil.ToFloat64(q.dropped),
		"a queue filled exactly to its depth has dropped nothing")

	fanOut(rebirthQueueDepth) // one more node than the queue can hold

	assert.Equal(t, float64(1), testutil.ToFloat64(q.dropped),
		"the request the full queue discarded is counted")
	assert.Equal(t, float64(rebirthQueueDepth), testutil.ToFloat64(q.enqueued),
		"a dropped request must not also count as accepted")
}

// The counters are optional, exactly as every other field of Metrics is: a host built
// for a test with no registry must not panic on either arm of the select.
func TestEnqueueRebirthToleratesAbsentCounters(t *testing.T) {
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h"}, Broker{}, nil, fixedNow, Metrics{})

	for i := 0; i <= rebirthQueueDepth; i++ {
		c.enqueueRebirth("g", fmt.Sprintf("n%d", i))
	}
	assert.Len(t, c.rebirthCh, rebirthQueueDepth, "the queue still bounds itself with no metrics")
}
