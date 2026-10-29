// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/devicechain-io/dc-sparkplug-ingest/codec"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// errNoType is what the registrar returns while the source's device type does not exist.
var errNoType = errors.New(`createDevice: device type "sparkplug-pump" not found`)

type recoveryRig struct {
	t       *testing.T
	c       *Client
	fake    *fakeIngester
	pending prometheus.Gauge
	rebirth prometheus.Counter

	mu      sync.Mutex
	now     time.Time
	rebirts []nodeKey
}

func newRecoveryRig(t *testing.T) *recoveryRig {
	r := &recoveryRig{
		t:       t,
		fake:    &fakeIngester{err: errNoType},
		pending: prometheus.NewGauge(prometheus.GaugeOpts{Name: "birth_recovery_pending_test"}),
		rebirth: prometheus.NewCounter(prometheus.CounterOpts{Name: "birth_recovery_rebirths_test"}),
		now:     time.Unix(1_700_000_000, 0),
	}
	src := config.SparkplugSource{Tenant: "acme", HostId: "h1", AutoRegister: true, DeviceTypeToken: "sparkplug-pump"}
	r.c = newTestClient(src, Broker{}, r.fake, admitAllSamples{}, func() time.Time {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.now
	}, Metrics{BirthRecoveryPending: r.pending, BirthRecoveryRebirths: r.rebirth})
	r.c.rebirthPub = func(group, node string) bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.rebirts = append(r.rebirts, nodeKey{group, node})
		return true
	}
	return r
}

func (r *recoveryRig) advance(d time.Duration) {
	r.mu.Lock()
	r.now = r.now.Add(d)
	r.mu.Unlock()
}

func (r *recoveryRig) rebirths() []nodeKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]nodeKey(nil), r.rebirts...)
}

func (r *recoveryRig) nbirth(group, node string, bdSeq uint64) {
	r.t.Helper()
	enc, err := codec.Encode(&sppb.Payload{
		Seq:     proto.Uint64(0),
		Metrics: []*sppb.Payload_Metric{bdSeqM(bdSeq), valuedBirth("temperature", 1, 20)},
	})
	require.NoError(r.t, err)
	r.c.onMessage(nil, fakeMessage{topic: "spBv1.0/" + group + "/NBIRTH/" + node, payload: enc})
}

func (r *recoveryRig) tracked() int {
	r.c.recovery.mu.Lock()
	defer r.c.recovery.mu.Unlock()
	return len(r.c.recovery.nodes)
}

// The reported defect: a node that births before its source's device type exists is dropped, and
// it stays unknown until it rebirths on its own. Here the Host asks it to, and the answer
// registers it. The second NBIRTH carries the SAME bdSeq, exactly what a commanded rebirth
// sends; without the recovery's mark it would be skipped as a QoS-1 redelivery.
func TestANodeBornBeforeItsDeviceTypeIsAskedToRebirthAndRegisters(t *testing.T) {
	r := newRecoveryRig(t)

	r.nbirth("plant-a", "node-3", 1)
	require.Equal(t, 1, r.tracked(), "the dropped birth was not remembered")
	assert.Equal(t, float64(1), testutil.ToFloat64(r.pending))

	// Not due yet: nothing is sent.
	r.c.recoverOnce()
	assert.Empty(t, r.rebirths(), "asked before the initial delay")

	// The device type is created, and the node is due.
	r.fake.mu.Lock()
	r.fake.err = nil
	r.fake.calls, r.fake.presence = nil, nil
	r.fake.mu.Unlock()
	r.advance(recoveryInitialDelay + time.Second)
	r.c.recoverOnce()
	require.Equal(t, []nodeKey{{"plant-a", "node-3"}}, r.rebirths(), "the unregistered node was not asked to rebirth")
	assert.Equal(t, float64(1), testutil.ToFloat64(r.rebirth))

	// The node answers with the same bdSeq.
	r.nbirth("plant-a", "node-3", 1)
	require.Equal(t, 1, r.fake.count(), "the answering birth was skipped as a redelivery, so nothing registered")
	require.Equal(t, 1, r.fake.presenceCount(), "the answering birth did not re-assert the node CONNECTED")
	assert.Zero(t, r.tracked(), "a registered node is still being asked to rebirth")
	assert.Zero(t, testutil.ToFloat64(r.pending))

	// And it is not asked again.
	r.advance(10 * time.Minute)
	r.c.recoverOnce()
	assert.Len(t, r.rebirths(), 1)
}

// While the type still does not exist the node is asked on a doubling, capped interval, and the
// failure is visible as a standing gauge rather than a single dropped message.
func TestAStillUnregisteredNodeIsAskedOnAGrowingCappedInterval(t *testing.T) {
	r := newRecoveryRig(t)
	r.nbirth("g", "n", 1)
	r.c.recoveryInitial, r.c.recoveryMax = 10*time.Second, 40*time.Second
	r.c.recovery.mu.Lock()
	for _, st := range r.c.recovery.nodes {
		st.delay, st.nextAsk = 10*time.Second, r.c.now().Add(10*time.Second)
	}
	r.c.recovery.mu.Unlock()

	var gaps []time.Duration
	last := r.c.now()
	for i := 0; i < 400 && len(gaps) < 5; i++ {
		r.advance(time.Second)
		before := len(r.rebirths())
		r.c.recoverOnce()
		if len(r.rebirths()) > before {
			gaps = append(gaps, r.c.now().Sub(last))
			last = r.c.now()
		}
	}
	assert.Equal(t, []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 40 * time.Second, 40 * time.Second}, gaps)
	assert.Equal(t, float64(1), testutil.ToFloat64(r.pending), "the standing failure is not visible on the gauge")
	assert.Equal(t, float64(5), testutil.ToFloat64(r.rebirth))
}

// A failing node is tracked once, however many of its messages fail.
func TestAFailingNodeIsTrackedOnce(t *testing.T) {
	r := newRecoveryRig(t)
	r.nbirth("g", "n", 1)
	r.c.noteUnregistered([]string{"g/n", "g/n/dev"}, errNoType)
	assert.Equal(t, 1, r.tracked())
	assert.Equal(t, float64(1), testutil.ToFloat64(r.pending), "the gauge counted one node more than once")
}

// A node that has gone offline is forgotten rather than asked forever: its next birth is a fresh
// attempt.
func TestANodeThatWentOfflineIsNoLongerAsked(t *testing.T) {
	r := newRecoveryRig(t)
	r.nbirth("g", "n", 1)
	require.Equal(t, 1, r.tracked())

	death, err := codec.Encode(&sppb.Payload{Metrics: []*sppb.Payload_Metric{bdSeqM(1)}})
	require.NoError(t, err)
	r.c.onMessage(nil, fakeMessage{topic: "spBv1.0/g/NDEATH/n", payload: death})

	r.advance(time.Hour)
	r.c.recoverOnce()
	assert.Empty(t, r.rebirths(), "a dead node was asked to rebirth")
	assert.Zero(t, r.tracked())
	assert.Zero(t, testutil.ToFloat64(r.pending), "the gauge kept a node that is no longer tracked")
}

// A DISCONNECTED transition that cannot be stored is not a registration failure: a node that was
// never registered has nothing to register on its death.
func TestAFailedDeathIsNotARegistrationFailure(t *testing.T) {
	r := newRecoveryRig(t)
	r.c.ingestPresence([]PresenceEvent{{ExternalId: "g/n", Connected: false}})
	assert.Zero(t, r.tracked())
}

// The set is bounded.
func TestTheUnregisteredSetIsBounded(t *testing.T) {
	r := newRecoveryRig(t)
	for i := 0; i < maxUnregisteredNodes+10; i++ {
		r.c.noteUnregistered([]string{fmt.Sprintf("g/n%d", i)}, errNoType)
	}
	assert.Equal(t, maxUnregisteredNodes, r.tracked())
	assert.Equal(t, float64(maxUnregisteredNodes), testutil.ToFloat64(r.pending))
}

// A new leadership term starts empty, and the gauge goes with it.
func TestATermBoundaryEmptiesTheSet(t *testing.T) {
	r := newRecoveryRig(t)
	r.c.noteUnregistered([]string{"g/a", "g/b"}, errNoType)
	require.Equal(t, float64(2), testutil.ToFloat64(r.pending))
	r.c.resetRecovery()
	assert.Zero(t, r.tracked())
	assert.Zero(t, testutil.ToFloat64(r.pending))
}

func TestNodeKeyOfAnExternalId(t *testing.T) {
	for id, want := range map[string]nodeKey{"g/n": {"g", "n"}, "g/n/d": {"g", "n"}} {
		got, ok := nodeKeyOf(id)
		assert.True(t, ok, id)
		assert.Equal(t, want, got, id)
	}
	for _, id := range []string{"", "g", "/n", "g/"} {
		_, ok := nodeKeyOf(id)
		assert.False(t, ok, id)
	}
}
