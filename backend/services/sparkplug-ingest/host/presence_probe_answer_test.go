// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/devicechain-io/dc-sparkplug-ingest/codec"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// These pin what a probe's ANSWER does: the evidence that a node is alive, and what a late
// answer must undo. The probe now runs every interval on a quiet fleet rather than once
// per reconnect, so a rule it gets wrong is no longer a rare reconnect artifact.

func encodeBirth(t *testing.T, bdSeq uint64) []byte {
	t.Helper()
	enc, err := codec.Encode(&sppb.Payload{
		Seq:     proto.Uint64(0),
		Metrics: []*sppb.Payload_Metric{bdSeqM(bdSeq), valuedBirth("temperature", 1, 20)},
	})
	require.NoError(t, err)
	return enc
}

// lastPresenceFor returns the most recent presence event the fake recorded for id.
func lastPresenceFor(f *fakeIngester, id string) (PresenceEvent, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.presence) - 1; i >= 0; i-- {
		if f.presence[i].ExternalId == id {
			return f.presence[i], true
		}
	}
	return PresenceEvent{}, false
}

// TestALateRebirthAnswerReassertsTheNodeTheProbeDeclaredDead: a node that answers the
// probe's rebirth AFTER the ten-second window (a slow link, a busy node) has already been
// declared DISCONNECTED at the probe's stale epoch. Its answer is a same-bdSeq NBIRTH — a
// commanded rebirth — which keeps the node's session and, before this fix, emitted no
// CONNECTED at all. An asserted device takes Active ONLY from a StateChange, and a
// disconnected node is no longer in the asserted-active set the next probe reads, so the
// node read offline, while publishing, until its next genuine session (a new bdSeq) —
// possibly days. The answer must re-assert it at an epoch above the stale one.
func TestALateRebirthAnswerReassertsTheNodeTheProbeDeclaredDead(t *testing.T) {
	fake := &fakeIngester{}
	c, _ := reconcileClient(fake, &fakeReconciler{})
	c.resetSeen()
	const node = "spBv1.0/plant-a/NBIRTH/node-3"
	c.onMessage(nil, fakeMessage{topic: node, payload: encodeBirth(t, 1)})
	first, ok := lastPresenceFor(fake, "plant-a/node-3")
	require.True(t, ok)
	require.True(t, first.Connected, "the first birth asserts CONNECTED")

	// A periodic probe judging from after that birth: the node does not answer in time.
	since := fixedNow().Add(time.Second)
	c.reconcileProbe(context.Background(), []AssertedDevice{{ExternalId: "plant-a/node-3", SessionId: first.SessionId}}, since, ProbeTriggerPeriodic)
	verdict, ok := lastPresenceFor(fake, "plant-a/node-3")
	require.True(t, ok)
	require.False(t, verdict.Connected, "the unanswered probe declares the node DISCONNECTED")
	require.Greater(t, verdict.SessionId, first.SessionId)

	// The answer arrives late: the same bdSeq, seq reset — the commanded rebirth.
	c.onMessage(nil, fakeMessage{topic: node, payload: encodeBirth(t, 1)})
	after, ok := lastPresenceFor(fake, "plant-a/node-3")
	require.True(t, ok)
	assert.True(t, after.Connected, "a node that answered the probe late still reads DISCONNECTED: nothing re-asserts it")
	assert.Greater(t, after.SessionId, verdict.SessionId, "the re-assertion must outrank the probe's stale epoch, or the projection drops it")
}

// TestAnUnsolicitedDuplicateBirthStillEmitsNothing is the counterweight: only an answer to
// a PROBE's rebirth re-asserts the node. A QoS-1 redelivery of the same birth, with no
// rebirth asked, stays a silent no-op.
func TestAnUnsolicitedDuplicateBirthStillEmitsNothing(t *testing.T) {
	fake := &fakeIngester{}
	c, _ := reconcileClient(fake, &fakeReconciler{})
	c.resetSeen()
	const node = "spBv1.0/plant-a/NBIRTH/node-3"
	c.onMessage(nil, fakeMessage{topic: node, payload: encodeBirth(t, 1)})
	require.Equal(t, 1, fake.presenceCount())
	c.onMessage(nil, fakeMessage{topic: node, payload: encodeBirth(t, 1)})
	assert.Equal(t, 1, fake.presenceCount(), "a redelivered birth nobody asked for re-emitted presence")
}

// TestDeviceTrafficProvesItsNodeAlive: in Sparkplug a device's messages are published by
// its edge node's own MQTT connection, so a DDATA is proof the node is up. Before this
// fix only the device's id was stamped, so a node whose devices report constantly but
// which publishes no NDATA of its own — the common shape — read as silent on every tick
// and was rebirthed every other interval, re-sending every device's whole metric set and
// a fresh connect for each of them. The documentation's "a chatty fleet costs nothing" was
// false for it.
func TestDeviceTrafficProvesItsNodeAlive(t *testing.T) {
	c, _ := reconcileClient(&fakeIngester{}, &fakeReconciler{})
	c.resetSeen()
	enc, err := codec.Encode(&sppb.Payload{Seq: proto.Uint64(1), Metrics: []*sppb.Payload_Metric{valuedData(1, 1)}})
	require.NoError(t, err)
	c.onMessage(nil, fakeMessage{topic: "spBv1.0/plant-a/DDATA/node-3/dev-1", payload: enc})

	asserted := []AssertedDevice{{ExternalId: "plant-a/node-3"}, {ExternalId: "plant-a/node-3/dev-1"}}
	silent := c.silentDevices(asserted, fixedNow().Add(-time.Minute))
	assert.Empty(t, silent, "a node whose device just published was picked out as silent")
}

// TestPeriodicTickWithNothingAssertedStillCounts: the documented contract of the periodic
// series is "on a connected Host it must rise; flat means the probe is not running". A
// Host whose source has no asserted-active devices — a new install, a fleet that has not
// birthed yet — runs every tick and finds nothing to ask, and must not read as a dead
// probe. Before this fix such a tick returned before counting.
func TestPeriodicTickWithNothingAssertedStillCounts(t *testing.T) {
	fake := &fakeIngester{}
	rec := &countingReconciler{}
	metrics, probes, _ := probeMetrics()
	c, rebirths := loopClient(fake, rec, metrics)
	c.resetSeen()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.presenceProbeLoop(ctx)
	require.True(t, eventually(2*time.Second, func() bool { return rec.readCount() >= 3 }), "the loop is ticking")
	assert.True(t, eventually(time.Second, func() bool { return testutil.ToFloat64(probes.WithLabelValues(ProbeTriggerPeriodic)) >= 2 }),
		"ticks that read an empty asserted set left the periodic series flat (%v), which the docs tell an operator means the probe is not running",
		testutil.ToFloat64(probes.WithLabelValues(ProbeTriggerPeriodic)))
	cancel()
	assert.Equal(t, int32(0), rebirths.Load())
}

// TestAFailedProjectionReadDoesNotCount is that rule's counterweight: a tick that could
// not read device-state reached no verdict, and must leave the series flat — that IS the
// probe not working.
func TestAFailedProjectionReadDoesNotCount(t *testing.T) {
	fake := &fakeIngester{}
	metrics, probes, _ := probeMetrics()
	rec := &fakeReconciler{err: assert.AnError}
	c, _ := loopClient(fake, rec, metrics)
	c.resetSeen()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.presenceProbeLoop(ctx)
	time.Sleep(6 * c.probeInterval)
	cancel()
	assert.Equal(t, float64(0), testutil.ToFloat64(probes.WithLabelValues(ProbeTriggerPeriodic)),
		"a tick whose device-state read failed was counted as a probe that reached a verdict")
}

// TestNoProbeGoroutineStartsForAnEndedSession: onConnected runs on paho's goroutine and
// can still be finishing after Stop cancelled the term and entered its WaitGroup waits.
// A probe or loop launched then is an Add racing a Wait (a misuse the runtime may panic
// on) or a goroutine Stop never drains, so an ended session starts nothing; a live one
// starts and is tracked.
func TestNoProbeGoroutineStartsForAnEndedSession(t *testing.T) {
	c, _ := reconcileClient(&fakeIngester{}, &fakeReconciler{})
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	ran := make(chan struct{}, 1)
	assert.False(t, c.goWhileLive(ended, &c.loopWG, func() { ran <- struct{}{} }), "a goroutine was started for a session that had already ended")
	c.loopWG.Wait()
	select {
	case <-ran:
		t.Fatal("fn ran for an ended session")
	default:
	}

	assert.True(t, c.goWhileLive(context.Background(), &c.loopWG, func() { ran <- struct{}{} }), "a live session's goroutine was not started")
	c.loopWG.Wait()
	select {
	case <-ran:
	default:
		t.Fatal("the live session's fn had not run by the time its WaitGroup drained")
	}
}

// TestStopWaitsForTheProbeLoop: a leadership term ends with Stop, and "nothing of this
// term acts after the next leader has begun" holds only if the periodic loop — which can
// be mid-read of device-state, or mid-verdict, when the term is cancelled — has returned
// before Stop does. The stand-in loop below takes a moment to unwind after its session is
// cancelled, as a probe in its device-state read would.
func TestStopWaitsForTheProbeLoop(t *testing.T) {
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h"}, Broker{}, nil, nil, fixedNow, Metrics{})
	c.newClient = func(*mqtt.ClientOptions) mqtt.Client { return newFakeClient() }
	c.Connect()
	c.mu.Lock()
	session, cancel := context.WithCancel(c.runCtx)
	c.mu.Unlock()
	defer cancel()
	var returned atomic.Bool
	require.True(t, c.goWhileLive(session, &c.loopWG, func() {
		<-session.Done()
		time.Sleep(100 * time.Millisecond)
		returned.Store(true)
	}))
	stopWithin(t, c, 10*time.Second)
	assert.True(t, returned.Load(), "Stop returned while the term's probe loop was still running")
}
