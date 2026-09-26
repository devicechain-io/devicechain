// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// --- the per-device gate: park on a full shard, drain in order -------------------------------
//
// 🔴 WHAT THESE PIN. A live command that cannot be dispatched now (its device's queue is full,
// the device is offline, or an earlier command of its is in the backlog) is PARKED in
// command-delivery and delivered by a drain, and from that moment the device's later commands
// follow it into the backlog until a drain has seen the backlog empty. Two properties, and every
// test below asserts on at least one of them as a VALUE:
//   - the reader never waits on a device (only on command-delivery itself);
//   - a device's commands reach it in the order they were published — asserted as the exact
//     sequence of ops, because "every command ran" is equally true of a reordered run.

func tokens(prefix string, from, to int) []string {
	var out []string
	for i := from; i <= to; i++ {
		out = append(out, fmt.Sprintf("%s%d", prefix, i))
	}
	return out
}

// overflowOneDevice sends n+1 commands to one live device on a one-worker dispatcher whose ops are
// held, so the first is in flight, the next workerQueueDepth fill the queue and the rest overflow.
// It returns the release for the held ops and the acks.
func overflowOneDevice(t *testing.T, h *harness, n int) (release func(), acks []*fakeAck) {
	t.Helper()
	h.look.set("pump-1", ReachLive)
	holding, release := h.holdOps()
	acks = append(acks, h.send("c0", "pump-1"))
	require.Equal(t, "c0", recv(t, holding, "c0's op to start"))
	for i := 1; i <= n; i++ {
		acks = append(acks, h.send(fmt.Sprintf("c%d", i), "pump-1"))
	}
	return release, acks
}

// TestAFullShardDoesNotBlockTheReader: one device's queue is full behind a held op. Before the
// gate, the reader blocked on that queue, so every other device on the instance waited on this
// one. Now every message is read at once and the overflow is parked, each quoting the dispatch
// nonce its envelope carried (the store's Park refuses any other).
func TestAFullShardDoesNotBlockTheReader(t *testing.T) {
	h := newHarness(t, 1, nil)
	release, acks := overflowOneDevice(t, h, workerQueueDepth+3)
	defer release()

	require.Eventually(t, func() bool { return h.rdr.count() == workerQueueDepth+4 }, time.Second, time.Millisecond,
		"the reader must not wait behind one device's full queue; it read %d of %d", h.rdr.count(), workerQueueDepth+4)
	// A park moves the row to PARKED before it counts the park and acks the message, so wait for
	// the acks, the last thing each park does, before reading the counter.
	require.Eventually(t, func() bool { return acks[9].acked() && acks[10].acked() && acks[11].acked() },
		time.Second, time.Millisecond, "the overflow must be parked and settled")
	for _, tok := range []string{"c9", "c10", "c11"} {
		assert.Equal(t, statusParked, h.store.status(tok), "%s is parked with the nonce its envelope carried", tok)
	}
	assert.Equal(t, 3.0, testutil.ToFloat64(h.m.OverflowParked.WithLabelValues(parkReasonFull)),
		"all three overflow parks are counted as full: the first set the gate, the others followed it")
	assert.Equal(t, []string{"c0"}, h.exec.order(), "nothing else ran while the op was held")
	assert.Equal(t, 0.0, testutil.ToFloat64(h.m.OverflowBlocked), "the park pool had room: the reader never waited")
}

// TestOverflowPreservesPerDeviceOrder: the same overflow, released. The queued commands are older
// than the parked ones and the gate went up behind them, so they are parked as they leave the queue
// and the drain then serves the whole backlog oldest-first. The exact sequence is the property.
//
// It also shows the drain needs NO WAKE: nothing here calls Drain or binds a connection. A device
// that stays connected never sends one (a keepalive on its live connection fires nothing), so a
// backlog that waited for a wake would sit until it expired. The park settles and the shard's own
// loop are the trigger, and the backlog here is deeper than one turn, so it takes several.
func TestOverflowPreservesPerDeviceOrder(t *testing.T) {
	h := newHarness(t, 1, nil)
	release, acks := overflowOneDevice(t, h, workerQueueDepth+3)
	require.Eventually(t, func() bool { return h.store.status("c11") == statusParked }, time.Second, time.Millisecond)
	release()

	h.waitOrder(tokens("c", 0, 11)...)
	// The order is recorded as an op starts; Drained counts it once it has returned and been
	// answered. Wait for the count, not only for c11 to start.
	require.Eventually(t, func() bool { return testutil.ToFloat64(h.m.Drained) >= 11 }, time.Second, time.Millisecond,
		"c1..c11 were delivered by the drain")
	for i, a := range acks {
		assert.Equal(t, 1, a.count(), "c%d is settled exactly once", i)
	}
	assert.GreaterOrEqual(t, testutil.ToFloat64(h.m.DrainTurns), 3.0,
		"eleven backlogged commands take at least three bounded turns, with no wake in between")
	assert.Equal(t, 11.0, testutil.ToFloat64(h.m.Drained), "c1..c11 were delivered by the drain")

	// The backlog is empty and nothing is on its way: the gate is down, and the next command goes
	// straight to the device instead of through command-delivery.
	require.Eventually(t, func() bool { return !h.gated("pump-1") }, time.Second, time.Millisecond, "the gate must lift")
	h.send("c12", "pump-1")
	h.waitOrder(tokens("c", 0, 12)...)
	assert.NotContains(t, h.store.parked(), "c12", "with the gate down a live command is dispatched live")
}

// TestAfterOverflowLaterCommandsForTheDeviceAlsoPark: the queue has room again, but the device's
// backlog is not drained yet (one of its parks is still in flight), so a new command for it must
// follow the backlog rather than take the free slot and overtake it.
func TestAfterOverflowLaterCommandsForTheDeviceAlsoPark(t *testing.T) {
	hold := make(chan struct{})
	h := newHarness(t, 1, func(h *harness) { h.store.parkHolds["c9"] = hold })
	release, _ := overflowOneDevice(t, h, workerQueueDepth+1) // c9 overflows; its park is held
	require.Eventually(t, func() bool { return len(h.store.parked()) == 1 }, time.Second, time.Millisecond)
	release()
	// The queued c1..c8 leave the queue gated: parked, not run.
	require.Eventually(t, func() bool { return h.queuedLive() == 0 && h.store.status("c8") == statusParked },
		time.Second, time.Millisecond)

	h.send("c10", "pump-1") // the queue is empty now
	require.Eventually(t, func() bool { return h.store.status("c10") == statusParked }, time.Second, time.Millisecond,
		"a command for a gated device is parked even when its queue has room")
	never(t, func() bool { return len(h.exec.order()) > 1 }, "nothing may run while c9's park is in flight")

	close(hold)
	h.waitOrder(tokens("c", 0, 10)...)
}

// TestDrainWaitsForParksInFlight: two commands for an offline device are parked concurrently, the
// newer one lands first, and then the device wakes. A drain now would find only the newer row and
// deliver it ahead of the older one, so it waits for the older park to settle.
func TestDrainWaitsForParksInFlight(t *testing.T) {
	hold := make(chan struct{})
	h := newHarness(t, 1, func(h *harness) { h.store.parkHolds["c2"] = hold })
	h.look.set("pump-1", ReachOffline)
	h.send("c2", "pump-1")
	h.send("c3", "pump-1")
	require.Eventually(t, func() bool {
		return h.store.status("c3") == statusParked && len(h.store.parked()) == 2
	}, time.Second, time.Millisecond, "c3's park lands while c2's is held")

	h.look.set("pump-1", ReachLive)
	h.d.Drain("acme", "pump-1")
	never(t, func() bool { return len(h.store.claimed()) > 0 || len(h.exec.order()) > 0 },
		"no drain may claim or run anything while an older park is in flight")

	close(hold)
	h.waitOrder("c2", "c3")
	assert.Equal(t, 2.0, testutil.ToFloat64(h.m.OverflowParked.WithLabelValues(parkReasonOffline)))
}

// TestGateSurvivesAParkThatLandsDuringTheTurn: a park settles while a turn is fetching. The fetch
// may have been taken before that row was PARKED, so an empty page proves nothing about it; the
// generation moved, so the gate stays up and the device gets another turn, which is what finds it.
//
// It is driven at the shard's own methods because the window is the inside of one fetch, which a
// running dispatcher gives no deterministic way to land in.
func TestGateSurvivesAParkThatLandsDuringTheTurn(t *testing.T) {
	s := newShardState()
	clk := newFakeClock()
	k := deviceKey{"acme", "pump-1"}

	s.wake(k)
	got, gen0, ok := s.pickEligible(clk)
	require.True(t, ok)
	require.Equal(t, k, got)

	// During the fetch: a park for the device begins and lands.
	s.beginPark(k, parkReasonBind)
	s.settlePark(k, "c2", parkDone, clk.Now())

	more := s.finishTurn(k, gen0, turnResult{fetched: 0}, clk.Now())
	assert.True(t, s.devs[k].gated, "a park that landed during the turn keeps the gate up")
	assert.True(t, more, "and the device is ready for another turn")

	_, gen1, ok := s.pickEligible(clk)
	require.True(t, ok, "the second turn is eligible at once")
	require.NotEqual(t, gen0, gen1)
	s.finishTurn(k, gen1, turnResult{fetched: 0}, clk.Now())
	_, present := s.devs[k]
	assert.False(t, present, "a quiet turn with an empty page lifts the gate and forgets the device")
}

// TestTheGateCarriesItsLatestCause: a device gated because its queue was full that then
// reconnects is gated by its bind from that moment. Its next live command is counted under "bind"
// — what it is waiting for now — not under the overflow that raised the gate first.
func TestTheGateCarriesItsLatestCause(t *testing.T) {
	s := newShardState()
	k := deviceKey{"acme", "pump-1"}
	s.beginPark(k, parkReasonFull)
	s.wake(k)
	assert.Equal(t, parkReasonBind, s.admit(k, ReachLive, task{deviceToken: k.deviceToken}))
}

// TestADrainTurnIsBounded: a device with a deep backlog shares a shard with another device whose
// live command arrives during the first turn. That command runs after at most one turn's worth of
// the backlog, not after all of it.
//
// It is REPEATED because the defect it guards against is a coin toss: a worker that selects over
// its live queue and its nudge in one select picks between them at random when both are ready.
// Measured with the defect in, a single run failed on 127 of 260 attempts, so one run lets it pass
// about as often as not. Forty runs put a pass under it near one in 2^40, and each run takes
// milliseconds.
func TestADrainTurnIsBounded(t *testing.T) {
	for i := range 40 {
		t.Run(fmt.Sprintf("run %d", i), testADrainTurnIsBoundedOnce)
	}
}

func testADrainTurnIsBoundedOnce(t *testing.T) {
	h := newHarness(t, 1, nil)
	h.look.set("pump-a", ReachLive)
	h.look.set("pump-b", ReachLive)
	for _, tok := range tokens("a", 1, 10) {
		h.store.add(tok, "pump-a", statusParked)
	}
	injected := false
	h.exec.mu.Lock()
	h.exec.onOp = func(token string) {
		if token == "a1" && !injected {
			injected = true
			h.send("b1", "pump-b")
			// On the worker goroutine, so no require here: wait (bounded) for b1 to be queued.
			for deadline := time.Now().Add(time.Second); h.queuedLive() != 1; time.Sleep(time.Millisecond) {
				if time.Now().After(deadline) {
					t.Error("b1 was never queued")
					return
				}
			}
		}
	}
	h.exec.mu.Unlock()

	h.d.Drain("acme", "pump-a")

	h.waitOrder("a1", "a2", "a3", "a4", "b1", "a5", "a6", "a7", "a8", "a9", "a10")
	assert.Equal(t, 3.0, testutil.ToFloat64(h.m.DrainTurns), "ten rows take three turns of at most four")
}

// TestAWakeIsNeverDroppedOnAFullShard: a device wakes while its shard's queue is full of another
// device's commands. Drain used to enqueue onto that queue and drop the wake when it was full,
// promising the next wake would re-trigger it; a device that stays connected never sends one. The
// wake is recorded, and served the moment the worker is free — ahead of the queued commands, since
// a turn runs after every live task.
func TestAWakeIsNeverDroppedOnAFullShard(t *testing.T) {
	h := newHarness(t, 1, nil)
	h.look.set("pump-b", ReachLive)
	h.store.add("b1", "pump-b", statusParked)
	release, _ := overflowOneDevice(t, h, workerQueueDepth)
	require.Eventually(t, func() bool { return h.queuedLive() == workerQueueDepth }, time.Second, time.Millisecond,
		"fixture: pump-1's commands fill the queue")

	h.d.Drain("acme", "pump-b")
	release()

	want := append([]string{"c0", "b1"}, tokens("c", 1, workerQueueDepth)...)
	h.waitOrder(want...)
}

// TestBindGatesLiveCommandsUntilTheBacklogIsEmpty: a device (re)connects with an older command
// waiting in the backlog — left PARKED by a previous leader, or re-armed by command-delivery — and
// a new live command arrives around the same moment. Nothing in this term's memory knows about the
// old row, so the bind gates the device until a turn has looked, and the old row goes first.
func TestBindGatesLiveCommandsUntilTheBacklogIsEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		// queuedBeforeBind: the new command is already in the queue when the bind lands (the
		// registration raced it), rather than arriving after the bind.
		queuedBeforeBind bool
	}{
		{name: "arrives after the bind", queuedBeforeBind: false},
		{name: "queued before the bind", queuedBeforeBind: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 1, nil)
			h.look.set("pump-x", ReachLive)
			h.look.set("pump-1", ReachLive)
			h.store.add("d-old", "pump-1", statusParked)
			holding, release := h.holdOps()
			defer release()
			h.send("x1", "pump-x")
			require.Equal(t, "x1", recv(t, holding, "x1's op to start"))

			if tc.queuedBeforeBind {
				h.send("d-new", "pump-1")
				require.Eventually(t, func() bool { return h.queuedLive() == 1 }, time.Second, time.Millisecond)
				h.d.Drain("acme", "pump-1")
			} else {
				h.d.Drain("acme", "pump-1")
				h.send("d-new", "pump-1")
				require.Eventually(t, func() bool { return h.store.status("d-new") == statusParked }, time.Second, time.Millisecond,
					"a live command for a device whose bind drain has not run yet is parked")
			}
			release()

			h.waitOrder("x1", "d-old", "d-new")
			assert.Equal(t, 1.0, testutil.ToFloat64(h.m.OverflowParked.WithLabelValues(parkReasonBind)),
				"the new command was parked behind the bind, and counted as such")
		})
	}
}

// TestAnErroredParkHoldsTheGateUntilRedeliveryOrDeadline: a park that errors leaves its message
// unacked to redeliver, and the row stays SENT, invisible to the drain. A drain that ran now would
// deliver the device's later parked commands ahead of it. So the gate waits for the redelivery,
// which parks it and lets the drain serve both in order — or, if the broker gives up on it, for
// the redelivery budget to run out, after which the rest are no longer held hostage.
func TestAnErroredParkHoldsTheGateUntilRedeliveryOrDeadline(t *testing.T) {
	setup := func(t *testing.T) (*harness, *fakeAck) {
		h := newHarness(t, 1, func(h *harness) { h.store.parkErrs["c1"] = 1 })
		h.look.set("pump-1", ReachOffline)
		first := h.send("c1", "pump-1")
		h.send("c2", "pump-1")
		require.Eventually(t, func() bool {
			return testutil.ToFloat64(h.m.ParkErrors) == 1 && h.store.status("c2") == statusParked
		}, time.Second, time.Millisecond)
		h.look.set("pump-1", ReachLive)
		h.d.Drain("acme", "pump-1")
		never(t, func() bool { return len(h.exec.order()) > 0 },
			"c2 must not be delivered while c1's redelivery is still possible")
		assert.Equal(t, 0, first.count(), "the errored park left c1 unacked")
		return h, first
	}

	t.Run("the redelivery parks it and both drain in order", func(t *testing.T) {
		h, _ := setup(t)
		again := h.deliver("c1", "pump-1", h.nonceOf("c1"))
		h.waitOrder("c1", "c2")
		assert.Equal(t, 1, again.count(), "the redelivered copy is parked and settled")
	})

	t.Run("the budget runs out and the rest are released", func(t *testing.T) {
		h, _ := setup(t)
		require.Contains(t, h.clk.pending(), h.clk.Now().Add(redeliveryBudget),
			"a timer is armed at the redelivery deadline, or the device would wait for traffic that may never come")
		h.clk.Advance(redeliveryBudget - time.Second)
		never(t, func() bool { return len(h.exec.order()) > 0 }, "not before the deadline")
		h.clk.Advance(time.Second)
		h.waitOrder("c2")
		assert.Equal(t, "SENT", h.store.status("c1"),
			"c1 is left to command-delivery's stranded pass; this is one of the two places order can break")

		// And the gate LIFTS, once, rather than sticking. A deadline that has passed but is still
		// counted as unsettled would keep the device gated with a turn wanted, and the shard would
		// fetch from command-delivery as fast as it could, forever, while every later command for
		// the device parked behind a gate nothing can lift.
		require.Eventually(t, func() bool { return !h.gated("pump-1") }, time.Second, time.Millisecond,
			"the gate must lift once the redelivery budget has run out")
		fetches := h.store.fetchCount()
		never(t, func() bool { return h.store.fetchCount() > fetches },
			"a device whose backlog is drained takes no further turns")
		assert.LessOrEqual(t, fetches, 2, "one turn served c2 and found the backlog drained")
	})
}

// TestAFetchErrorDefersAndRetries: a drain fetch that fails is retried after drainRetryDelay — not
// at once (an outage would spin every shard) and not never (a connected device sends nothing that
// would retry it).
func TestAFetchErrorDefersAndRetries(t *testing.T) {
	h := newHarness(t, 1, func(h *harness) { h.store.fetchErrs = 1 })
	h.look.set("pump-1", ReachLive)
	h.store.add("c1", "pump-1", statusParked)

	h.d.Drain("acme", "pump-1")
	require.Eventually(t, func() bool { return h.store.fetchCount() == 1 }, time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		p := h.clk.pending()
		return len(p) == 1 && p[0].Equal(h.clk.Now().Add(drainRetryDelay))
	}, time.Second, time.Millisecond, "the retry is armed at drainRetryDelay")
	h.clk.Advance(drainRetryDelay - time.Millisecond)
	never(t, func() bool { return h.store.fetchCount() > 1 }, "no retry before drainRetryDelay")

	h.clk.Advance(time.Millisecond)
	h.waitOrder("c1")
	assert.Equal(t, 2, h.store.fetchCount(), "exactly one retry")
	assert.Equal(t, 1.0, testutil.ToFloat64(h.m.DrainErrors))
}

// TestAClaimErrorStopsTheTurnAndRetriesInOrder: the first row's claim fails. The turn stops there
// rather than claiming the second row and delivering it first, and the retry starts from the same
// row.
func TestAClaimErrorStopsTheTurnAndRetriesInOrder(t *testing.T) {
	h := newHarness(t, 1, func(h *harness) { h.store.claimErrs["c1"] = 1 })
	h.look.set("pump-1", ReachLive)
	h.store.add("c1", "pump-1", statusParked)
	h.store.add("c2", "pump-1", statusParked)

	h.d.Drain("acme", "pump-1")
	require.Eventually(t, func() bool { return len(h.clk.pending()) == 1 }, time.Second, time.Millisecond)
	assert.Equal(t, []string{"c1"}, h.store.claimed(), "the turn stopped at the row it could not claim")

	h.clk.Advance(drainRetryDelay)
	h.waitOrder("c1", "c2")
	assert.Equal(t, []string{"c1", "c1", "c2"}, h.store.claimed())
}

// TestAFullOverflowBlocksAndCounts: command-delivery stops answering parks. The pool's workers and
// its queue fill, and then the reader waits — counted — rather than dropping a command or leaving
// it unacked to come back after newer ones. Nothing is acknowledged until it is parked.
func TestAFullOverflowBlocksAndCounts(t *testing.T) {
	hold := make(chan struct{})
	total := overflowWorkers + overflowDepth + 2
	h := newHarness(t, 1, func(h *harness) {
		for i := 0; i < total; i++ {
			h.store.parkHolds[fmt.Sprintf("p%d", i)] = hold
		}
	})
	h.look.set("pump-1", ReachOffline)
	var acks []*fakeAck
	for i := 0; i < total; i++ {
		acks = append(acks, h.send(fmt.Sprintf("p%d", i), "pump-1"))
	}

	absorbed := overflowWorkers + overflowDepth
	require.Eventually(t, func() bool { return testutil.ToFloat64(h.m.OverflowBlocked) == 1 }, 2*time.Second, time.Millisecond,
		"the reader must record that it waited")
	never(t, func() bool { return h.rdr.count() > absorbed+1 },
		"the reader holds the command it could not hand over and reads nothing further")
	for i, a := range acks {
		require.Equal(t, 0, a.count(), "p%d acknowledged before it was parked", i)
	}

	close(hold)
	require.Eventually(t, func() bool {
		for _, a := range acks {
			if a.count() != 1 {
				return false
			}
		}
		return true
	}, 3*time.Second, time.Millisecond, "every command is parked and settled once command-delivery answers")
	assert.Len(t, h.store.parked(), total)
}

// TestAnUnconfirmedCommandIsNotOvertaken: a live command's confirmation fails, so it is left
// unacked to redeliver at the ack deadline. If command-delivery recovers before then, the device's
// NEXT command would be confirmed and actuated first. The failure gates the device: the next
// command is parked, the redelivered one is parked behind nothing, and the drain delivers them in
// their original order.
func TestAnUnconfirmedCommandIsNotOvertaken(t *testing.T) {
	h := newHarness(t, 1, func(h *harness) { h.store.dispatchErrs["c1"] = 1 })
	h.look.set("pump-1", ReachLive)
	first := h.send("c1", "pump-1")
	require.Eventually(t, func() bool { return testutil.ToFloat64(h.m.LiveClaimErrors) == 1 }, time.Second, time.Millisecond)

	h.send("c2", "pump-1")
	require.Eventually(t, func() bool { return h.store.status("c2") == statusParked }, time.Second, time.Millisecond,
		"the next command for the device is parked behind the unconfirmed one")
	never(t, func() bool { return len(h.exec.order()) > 0 }, "c2 must not overtake c1")
	assert.Equal(t, 0, first.count(), "c1 is left unacked, to redeliver")

	h.deliver("c1", "pump-1", h.nonceOf("c1")) // the redelivery: its confirmation never rotated the nonce
	h.waitOrder("c1", "c2")
	assert.Equal(t, 2.0, testutil.ToFloat64(h.m.OverflowParked.WithLabelValues(parkReasonUnconfirmed)),
		"c2 and the redelivered c1 were both parked behind the unconfirmed command")
}

// TestADispatcherThatReadsMustBeAbleToDrain: every command the dispatcher parks is delivered by a
// drain, and only a drain lifts a gate, so a reading dispatcher with no fetcher would park a gated
// device's commands forever. It is refused at construction.
func TestADispatcherThatReadsMustBeAbleToDrain(t *testing.T) {
	assert.PanicsWithValue(t,
		"lwm2m-ingest: NewDispatcher needs a drain fetcher when it is given a reader; every "+
			"command it parks is delivered by a drain, and a device's gate is lifted only by one, "+
			"so without it a gated device's commands would be parked and never delivered",
		func() {
			NewDispatcher(&chanReader{ch: make(chan messaging.Message)}, &fakePublisher{}, &switchLookup{}, &orderExecutor{},
				nil, &fakeClaimer{}, Metrics{}, Options{ReadPacer: core.NewReadPacer(nil, "device commands").UseClock(core.VirtualClock())})
		})
}

// TestParkReasonFollowsTheDevicesCurrentCause: the park label and ServedOffline say what happened
// to THIS command. A device gated "offline" that reconnects is gated by its bind from then on, so
// its live commands are counted under "bind" and never as served-offline: a connected device must
// not be reported as absent, and "bind" is the reading an operator expects after a failover.
func TestParkReasonFollowsTheDevicesCurrentCause(t *testing.T) {
	counts := func(h *harness) (offline, bind, servedOffline float64) {
		return testutil.ToFloat64(h.m.OverflowParked.WithLabelValues(parkReasonOffline)),
			testutil.ToFloat64(h.m.OverflowParked.WithLabelValues(parkReasonBind)),
			testutil.ToFloat64(h.m.ServedOffline)
	}
	parkOffline := func(t *testing.T, h *harness, token string) {
		t.Helper()
		h.look.set("pump-1", ReachOffline)
		ack := h.send(token, "pump-1")
		// The ack follows the park's counters; the row goes PARKED before them.
		require.Eventually(t, ack.acked, time.Second, time.Millisecond, "%s's park never settled", token)
		require.Equal(t, statusParked, h.store.status(token))
	}

	t.Run("a command during the bind's drain", func(t *testing.T) {
		h := newHarness(t, 1, nil)
		parkOffline(t, h, "c1")
		h.look.set("pump-1", ReachLive)
		holding, release := h.holdOps()
		defer release()
		h.d.Drain("acme", "pump-1")
		require.Equal(t, "c1", recv(t, holding, "c1's drained op to start"))

		c2 := h.send("c2", "pump-1")
		require.Eventually(t, c2.acked, time.Second, time.Millisecond, "c2's park never settled")
		require.Equal(t, statusParked, h.store.status("c2"))
		offline, bind, served := counts(h)
		assert.Equal(t, []float64{1, 1, 1}, []float64{offline, bind, served},
			"c1 was parked offline; c2, sent to a connected device during its bind drain, is a bind park")
		release()
		h.waitOrder("c1", "c2")
	})

	t.Run("a command after the reconnect, before its wake lands", func(t *testing.T) {
		h := newHarness(t, 1, nil)
		parkOffline(t, h, "c1")
		// c1's park settle asks for a drain turn, which must find the device OFFLINE and leave the
		// gate up. If the device reads live before that turn looks, the turn drains c1 and lifts
		// the gate, and c2 goes straight to the device. So wait until the turn has been taken,
		// then run an op for another device on the same (only) shard: its worker starts that op
		// only once the turn has ended.
		require.Eventually(t, func() bool { return h.drainTaken("pump-1") }, time.Second, time.Millisecond,
			"c1's park never settled into a drain turn")
		h.look.set("pump-x", ReachLive)
		h.send("x1", "pump-x")
		h.waitOrder("x1")
		require.True(t, h.gated("pump-1"), "the turn found the device offline and left its gate up")
		h.look.set("pump-1", ReachLive) // the conn table says live; the wake has not run yet

		h.send("c2", "pump-1")
		// Not the row's status: once c2's park lands, the device (live) is drained on its own.
		require.Eventually(t, func() bool {
			offline, bind, _ := counts(h)
			return offline+bind == 2
		}, time.Second, time.Millisecond)
		offline, bind, served := counts(h)
		assert.Equal(t, []float64{1, 1, 1}, []float64{offline, bind, served},
			"the device's own lookup said live, so the gate it is parked behind is its bind, not its absence")
		h.d.Drain("acme", "pump-1")
		h.waitOrder("x1", "c1", "c2")
	})

	// A live task queued before the device dropped is parked on the gate as it leaves the queue,
	// with no lookup of its own on the way in. Its label is decided by the lookup at that moment.
	for _, tc := range []struct {
		name        string
		reach       Reach
		wantOffline float64
		wantBind    float64
	}{
		{name: "queued, then gated offline, still offline", reach: ReachOffline, wantOffline: 2, wantBind: 0},
		{name: "queued, then gated offline, back before its wake", reach: ReachLive, wantOffline: 1, wantBind: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 1, nil)
			h.look.set("pump-x", ReachLive)
			h.look.set("pump-1", ReachLive)
			holding, release := h.holdOps()
			defer release()
			h.send("x1", "pump-x")
			require.Equal(t, "x1", recv(t, holding, "x1's op to start"))
			d1 := h.send("d1", "pump-1")
			require.Eventually(t, func() bool { return h.queuedLive() == 1 }, time.Second, time.Millisecond)
			parkOffline(t, h, "d2")

			h.look.set("pump-1", tc.reach)
			release()
			// d1 is acked on every path it can take (a park acks after its counters), and the
			// counts below say which path that was.
			require.Eventually(t, d1.acked, time.Second, time.Millisecond, "d1 never settled")
			offline, bind, served := counts(h)
			assert.Equal(t, []float64{tc.wantOffline, tc.wantBind, tc.wantOffline}, []float64{offline, bind, served})

			h.look.set("pump-1", ReachLive)
			h.d.Drain("acme", "pump-1")
			h.waitOrder("x1", "d1", "d2")
		})
	}
}

// TestAWorkerParkDoesNotHoldItsShard: a device drops after its command was queued, so the shard
// worker finds it offline and parks it. That park is a command-delivery round trip, and it goes to
// the park pool: the next device's command on the same shard runs while it is still in flight,
// instead of waiting out command-delivery's answer.
func TestAWorkerParkDoesNotHoldItsShard(t *testing.T) {
	hold := make(chan struct{})
	h := newHarness(t, 1, func(h *harness) { h.store.parkHolds["a1"] = hold })
	for _, d := range []string{"pump-x", "pump-a", "pump-b"} {
		h.look.set(d, ReachLive)
	}
	holding, release := h.holdOps()
	h.send("x1", "pump-x")
	require.Equal(t, "x1", recv(t, holding, "x1's op to start"))
	a1 := h.send("a1", "pump-a")
	h.send("b1", "pump-b")
	require.Eventually(t, func() bool { return h.queuedLive() == 2 }, time.Second, time.Millisecond)
	h.look.set("pump-a", ReachOffline)
	release()

	// b1 runs while a1's park cannot have returned: its hold is still open. That is the claim.
	h.waitOrder("x1", "b1")
	// The park is handed to the pool, which reaches command-delivery on its own goroutine, so wait
	// for it to get there rather than reading the call list the moment b1 has run.
	require.Eventually(t, func() bool { return len(h.store.parked()) >= 1 }, time.Second, time.Millisecond,
		"a1's park never reached command-delivery")
	assert.Equal(t, []string{"a1"}, h.store.parked(), "a1, and only a1, is parked")
	assert.Equal(t, "SENT", h.store.status("a1"), "a1's park is still in flight while b1 runs")
	assert.Equal(t, 0, a1.count(), "a1 is not settled while its park is in flight")

	close(hold)
	// The ack is the last thing a park does, after its counters: wait for it, not for the row.
	require.Eventually(t, a1.acked, time.Second, time.Millisecond, "a1's park never settled")
	assert.Equal(t, statusParked, h.store.status("a1"))
	assert.Equal(t, 1.0, testutil.ToFloat64(h.m.ServedOffline))
}
