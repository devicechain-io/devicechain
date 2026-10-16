// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-sparkplug-ingest/config"
)

// The periodic probe spreads a fleet's rebirths across its interval: the interval is cut
// into slots, each node is asked in its own slot, and every node is still judged once per
// interval with its own full answer window. These drive probeTick directly, one slot per
// call, so the rotation is pinned by value rather than by timing.

func TestProbeSlotsFitTwoWindowsPerSlot(t *testing.T) {
	assert.Equal(t, 90, probeSlots(1800*time.Second, 10*time.Second), "the defaults cut thirty minutes into twenty-second slots")
	assert.Equal(t, 3, probeSlots(60*time.Second, 10*time.Second))
	assert.Equal(t, 1, probeSlots(60*time.Second, 59*time.Second), "a window too long for two slots runs the whole fleet in one")
	assert.Equal(t, 1, probeSlots(60*time.Second, 0))
	for _, tc := range [][2]time.Duration{{1800 * time.Second, 10 * time.Second}, {60 * time.Second, 5 * time.Second}, {600 * time.Second, 7 * time.Second}} {
		n := probeSlots(tc[0], tc[1])
		assert.GreaterOrEqual(t, tc[0]/time.Duration(n), 2*tc[1], "a slot shorter than two windows lets one slot's verdict run into the next")
	}
}

func TestSlotOfIsStableInRangeAndSpread(t *testing.T) {
	const slots = 10
	counts := make([]int, slots)
	for i := 0; i < 1000; i++ {
		key := nodeKey{group: "plant", node: fmt.Sprintf("node-%d", i)}
		s := slotOf(key, slots)
		require.GreaterOrEqual(t, s, 0)
		require.Less(t, s, slots)
		assert.Equal(t, s, slotOf(key, slots), "a node moved slot between rounds")
		counts[s]++
	}
	for s, n := range counts {
		assert.Greater(t, n, 50, "slot %d holds %d of 1000 nodes: the fleet is not spread", s, n)
	}
	assert.Equal(t, 0, slotOf(nodeKey{"g", "n"}, 1))
}

// rotationRecorder records which nodes a probeTick call asked to rebirth.
type rotationRecorder struct {
	mu    sync.Mutex
	asked []string
}

func (r *rotationRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.asked
	r.asked = nil
	sort.Strings(out)
	return out
}

func TestProbeTickAsksEachNodeOncePerRoundInItsOwnSlot(t *testing.T) {
	var devices []AssertedDevice
	for i := 0; i < 24; i++ {
		devices = append(devices, AssertedDevice{ExternalId: fmt.Sprintf("g1/n%02d", i), SessionId: loopSession})
	}
	rec := &countingReconciler{devices: devices, max: loopSession}
	c := newTestClient(config.SparkplugSource{Tenant: "acme", HostId: "h1"}, Broker{}, &fakeIngester{}, admitAllSamples{}, time.Now, Metrics{})
	c.SetReconciler(rec)
	c.probeWindow = time.Millisecond
	c.probeInterval = time.Hour
	c.resetSeen()
	asked := &rotationRecorder{}
	c.rebirthPub = func(g, n string) bool {
		asked.mu.Lock()
		asked.asked = append(asked.asked, g+"/"+n)
		asked.mu.Unlock()
		return true
	}

	const slots = 4
	round := &probeRound{slots: slots}
	seen := map[string]int{}
	nonEmpty := 0
	for slot := 0; slot < slots; slot++ {
		c.probeTick(context.Background(), round)
		got := asked.take()
		var want []string
		for _, d := range devices {
			key, _ := parseReconcileNodeKey(d.ExternalId)
			if slotOf(key, slots) == slot {
				want = append(want, d.ExternalId)
			}
		}
		sort.Strings(want)
		assert.Equal(t, want, got, "slot %d asked the wrong nodes", slot)
		if len(got) > 0 {
			nonEmpty++
		}
		for _, n := range got {
			seen[n]++
		}
	}
	assert.Len(t, seen, len(devices), "a round did not ask every silent node")
	for n, k := range seen {
		assert.Equal(t, 1, k, "%s was asked %d times in one round", n, k)
	}
	assert.Greater(t, nonEmpty, 1, "every node was asked in one slot: the rebirths are not spread")
	assert.Equal(t, 1, rec.readCount(), "device-state is read once per round, at its first slot")

	c.probeTick(context.Background(), round)
	assert.Equal(t, 2, rec.readCount(), "the next round reads device-state again")
}

// A round whose device-state read fails consumes no slot: the next tick retries the read
// rather than running slot 1 against a stale (or empty) set.
func TestAFailedRoundReadConsumesNoSlot(t *testing.T) {
	c := newTestClient(config.SparkplugSource{Tenant: "acme", HostId: "h1"}, Broker{}, &fakeIngester{}, admitAllSamples{}, time.Now, Metrics{})
	rec := &fakeReconciler{err: assert.AnError}
	c.SetReconciler(rec)
	c.resetSeen()
	round := &probeRound{slots: 4}
	c.probeTick(context.Background(), round)
	c.probeTick(context.Background(), round)
	assert.Equal(t, 0, round.slot, "a failed read advanced the rotation")
	assert.Equal(t, 2, rec.callCount, "each tick of a failed round retries the read")
}

func TestSetPresenceProbeWindowRefusesNonPositive(t *testing.T) {
	c := newTestClient(config.SparkplugSource{Tenant: "acme", HostId: "h1"}, Broker{}, nil, nil, time.Now, Metrics{})
	assert.Equal(t, 10*time.Second, c.PresenceProbeWindow(), "a client is built at the platform default")
	assert.Panics(t, func() { c.SetPresenceProbeWindow(0) })
	assert.Panics(t, func() { c.SetPresenceProbeWindow(-time.Second) })
	c.SetPresenceProbeWindow(25 * time.Second)
	assert.Equal(t, 25*time.Second, c.PresenceProbeWindow())
}

// TestTheLoopRunsTheRotation: the loop itself cuts its interval into slots — the probeTick
// tests above would pass with a loop that ran the whole fleet as one slot. Four nodes, one
// per slot of a four-slot rotation, are each asked on a different tick, so the first round's
// rebirths span at least most of the interval rather than one burst.
func TestTheLoopRunsTheRotation(t *testing.T) {
	const slots = 4
	interval, window := 200*time.Millisecond, 25*time.Millisecond
	require.Equal(t, slots, probeSlots(interval, window))
	var devices []AssertedDevice
	filled := map[int]bool{}
	for i := 0; len(filled) < slots && i < 10000; i++ {
		key := nodeKey{group: "g1", node: fmt.Sprintf("n%d", i)}
		if s := slotOf(key, slots); !filled[s] {
			filled[s] = true
			devices = append(devices, AssertedDevice{ExternalId: "g1/" + key.node, SessionId: loopSession})
		}
	}
	require.Len(t, devices, slots, "no node name lands in some slot: slotOf does not spread")
	rec := &countingReconciler{devices: devices, max: loopSession}
	c, _ := loopClient(&fakeIngester{}, rec, Metrics{})
	c.probeInterval, c.probeWindow = interval, window
	c.resetSeen()
	var mu sync.Mutex
	var at []time.Time
	c.rebirthPub = func(_, _ string) bool {
		mu.Lock()
		at = append(at, time.Now())
		mu.Unlock()
		return true
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.presenceProbeLoop(ctx)
	require.True(t, eventually(5*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(at) >= slots }), "the loop asked nobody")
	cancel()
	mu.Lock()
	defer mu.Unlock()
	span := at[slots-1].Sub(at[0])
	assert.GreaterOrEqual(t, span, interval/2, "the first round's %d rebirths went out within %v of each other: one burst, not a rotation", slots, span)
	assert.Less(t, span, interval, "the first round's %d rebirths took %v, longer than the interval: the slots are not cut from it", slots, span)
}
