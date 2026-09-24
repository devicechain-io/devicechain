// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"slices"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// The per-device GATE: what keeps one device's commands in order once any of them has been
// handed back to command-delivery instead of being dispatched live.
//
// 🔴 WHY A GATE AT ALL. A command reaches a device by one of two roads: the live road (the
// delivery stream, dispatched as it arrives) and the backlog road (parked in command-delivery,
// then fetched oldest-first and claimed by a drain). The moment one of a device's commands takes
// the backlog road, a LATER command taking the live road would overtake it. So once a device is
// gated, every further live command for it is parked too, and the gate lifts only when a drain
// has seen the device's backlog EMPTY with nothing still on its way into it. From then on the
// live road is safe again.
//
// The invariant, stated once: once a device is gated, no live command for it is actuated until a
// drain turn has fetched its backlog and found it drained, with no park for it in flight, none
// awaiting redelivery, and no park having settled since the turn began (the generation check).
//
// The gate is SET by:
//   - an overflow: the device's shard queue was full, so its command was parked (reason "full");
//   - an offline device: its command was parked because it had no live connection ("offline");
//   - Drain: the device (re)bound a connection, or a live delivery was found already re-armed
//     ("bind"). This is also the cross-term answer: the state is in memory, per leadership term,
//     and a new leader knows nothing of the old one's parks — so every device's first bind on it
//     gates the device until its backlog has been drained;
//   - a live confirmation that ERRORED ("unconfirmed"): that command is left unacked to redeliver
//     later, and without the gate a later command for the same device would be confirmed and
//     actuated first. The redelivered copy finds the device gated and is parked, so it rejoins the
//     backlog in its original place.
//
// It is CLEARED only by a drain turn, in finishTurn.
//
// Everything here is guarded by shardState.mu. Every one of a device's commands hashes to the
// same shard, so one mutex per shard is enough and no lock is shared across shards.

// Park reasons, the label on Metrics.OverflowParked. Each names why a live command was handed back
// instead of dispatched, and they are kept apart because they call for different readings: "full"
// is a slow device, "offline" is queue mode working, "bind" is a device reconnecting with (possibly)
// a backlog, and "unconfirmed" is command-delivery failing to answer a confirmation.
const (
	parkReasonFull        = "full"
	parkReasonOffline     = "offline"
	parkReasonBind        = "bind"
	parkReasonUnconfirmed = "unconfirmed"
)

// redeliveryBudget is how long a command left unacked can still come back: the broker redelivers it
// at most MaxDeliver times, AckWait apart. A gate holding for such a command waits this long at most
// before giving up on the redelivery. Derived from the messaging layer's own constants, never a copy.
//
// It overstates, and in the safe direction: the redelivery clock starts at the message's last
// DELIVERY, not at the moment its park or confirmation failed, and a message already on its last
// delivery never comes back at all. The gate can therefore hold for up to the whole budget longer
// than it needed to; it can never lift while a redelivery is still possible.
const redeliveryBudget = time.Duration(messaging.MaxDeliver) * messaging.AckWait

// gate is one device's state within its shard. An entry exists only while something is non-zero,
// and is deleted the moment nothing is (maybeDelete), so the map is bounded by the devices that are
// actually queued, gated or backlogged.
type gate struct {
	gated bool
	// reason is why the gate was set; later parks behind it are counted under the same reason.
	reason string
	// queuedLive counts this device's live tasks still sitting in the shard channel. A drain must
	// not run while one is there: it is OLDER than anything the gate parked since.
	queuedLive int
	// parksInFlight counts parks for this device not yet settled. A drain must wait for them: a
	// NEWER park may already have landed while an older one is still on its way.
	parksInFlight int
	// unsettled maps a command token to the moment its expected redelivery can no longer come: a
	// command whose park or confirmation failed and was left unacked. The drain waits for it, or for
	// the deadline, whichever is first.
	unsettled map[string]time.Time
	// gen moves on every park settle and every gate set, so a drain turn can tell that something
	// landed while it was fetching (finishTurn).
	gen uint64
	// wanted marks the device as already in the shard's ready list; notBefore defers its next turn
	// after a fetch or claim error.
	wanted    bool
	notBefore time.Time
}

// set gates the device, keeping the reason that set it first, and moves the generation on.
func (g *gate) set(reason string) {
	if !g.gated {
		g.gated, g.reason = true, reason
	}
	g.gen++
}

// prune forgets unsettled commands whose redelivery can no longer arrive.
func (g *gate) prune(now time.Time) {
	for tok, deadline := range g.unsettled {
		if !now.Before(deadline) {
			delete(g.unsettled, tok)
		}
	}
}

// blockedUntil is the earliest time this device can take a drain turn, as far as TIME is concerned:
// the retry deferral and every unsettled deadline. (Queued live tasks and parks in flight block it
// too, but those clear on an event that nudges the shard, not on a clock.)
func (g *gate) blockedUntil() time.Time {
	until := g.notBefore
	for _, deadline := range g.unsettled {
		if deadline.After(until) {
			until = deadline
		}
	}
	return until
}

// shardState is one dispatch shard: its worker's live queue, the nudge that wakes the worker for a
// drain turn, and the gates of the devices that hash to it.
type shardState struct {
	mu    sync.Mutex
	ch    chan task     // live tasks only; route is its ONLY sender, so a length check under mu is exact
	nudge chan struct{} // capacity 1: "a drain turn may be runnable"
	devs  map[deviceKey]*gate
	ready []deviceKey // devices wanting a drain turn, oldest first; de-duplicated by gate.wanted
	// stopTimer cancels the pending deferred-turn timer, if any.
	stopTimer func() bool
}

func newShardState() *shardState {
	return &shardState{
		ch:    make(chan task, workerQueueDepth),
		nudge: make(chan struct{}, 1),
		devs:  map[deviceKey]*gate{},
	}
}

// poke wakes the worker for a drain turn. Non-blocking: one pending nudge is as good as many.
func (s *shardState) poke() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// gateLocked returns the device's entry, creating it.
func (s *shardState) gateLocked(k deviceKey) *gate {
	g := s.devs[k]
	if g == nil {
		g = &gate{}
		s.devs[k] = g
	}
	return g
}

// maybeDeleteLocked drops an entry that holds nothing, so the map stays bounded.
func (s *shardState) maybeDeleteLocked(k deviceKey, g *gate) {
	if !g.gated && g.queuedLive == 0 && g.parksInFlight == 0 && len(g.unsettled) == 0 && !g.wanted {
		delete(s.devs, k)
	}
}

// wantLocked puts the device on the ready list (once) with the given deferral.
func (s *shardState) wantLocked(k deviceKey, g *gate, notBefore time.Time) {
	g.notBefore = notBefore
	if !g.wanted {
		g.wanted = true
		s.ready = append(s.ready, k)
	}
}

// admit decides, for one live command, whether it goes onto the shard's live queue or must be
// parked. It returns the park reason, or "" once the task is queued.
//
// 🔴 THE FULL CHECK AND THE SEND HAPPEN UNDER ONE LOCK, and route is the only sender, so a queue
// found with room still has room when the send runs: the worker only ever takes from it. That is
// what lets the reader never block on a shard.
func (s *shardState) admit(k deviceKey, reach Reach, t task) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.gateLocked(k)
	reason := ""
	switch {
	case reach == ReachOffline:
		reason = parkReasonOffline
	case g.gated:
		reason = g.reason
	case len(s.ch) == cap(s.ch):
		reason = parkReasonFull
	}
	if reason == "" {
		g.queuedLive++
		s.ch <- t
		return ""
	}
	g.set(reason)
	g.parksInFlight++
	return reason
}

// beginPark records a park about to be made for the device, gating it.
func (s *shardState) beginPark(k deviceKey, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.gateLocked(k)
	g.set(reason)
	g.parksInFlight++
}

// dequeue accounts for a live task the worker has just taken off the queue, and reports whether it
// must be PARKED rather than dispatched: the device was gated after the task was queued. Parking it
// keeps the invariant whatever set the gate — a bind (an older backlog may be waiting) or an
// unconfirmed command (which is older than this one) both need this task behind them, not in front.
func (s *shardState) dequeue(k deviceKey) (reason string, park bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.gateLocked(k)
	g.queuedLive--
	if g.gated {
		g.gen++
		g.parksInFlight++
		return g.reason, true
	}
	s.maybeDeleteLocked(k, g)
	return "", false
}

// parkOutcome is how a park ended, which decides what the gate does next.
type parkOutcome int

const (
	// parkDone: parked, or settled because the row had already moved on. Acked.
	parkDone parkOutcome = iota
	// parkErrored: command-delivery could not be reached. Left unacked to redeliver.
	parkErrored
	// parkSkipped: not attempted (no parker, or no dispatch nonce). Acked, and so never redelivered.
	parkSkipped
	// parkEvicted: the term ended mid-park. Left unacked for the next leader.
	parkEvicted
)

// settlePark records how one of the device's parks ended.
//
//   - Done: the command is in the backlog. Wake a drain for it.
//   - Errored: its redelivery will park it; hold the gate until then (or until the redelivery
//     budget has run out, which is the one place per-device order can break: it takes a
//     command-delivery outage longer than the whole budget).
//   - Skipped: the command will not come back, and a later one must not overtake it silently. Hold
//     the gate for the same budget. On a configured instance this does not happen.
//   - Evicted: the term is ending and this state ends with it.
func (s *shardState) settlePark(k deviceKey, commandToken string, out parkOutcome, now time.Time) {
	s.mu.Lock()
	g := s.gateLocked(k)
	g.parksInFlight--
	switch out {
	case parkDone:
		delete(g.unsettled, commandToken)
		g.gen++
		s.wantLocked(k, g, g.notBefore)
	case parkErrored, parkSkipped:
		s.holdLocked(g, commandToken, now)
		s.wantLocked(k, g, g.notBefore)
	case parkEvicted:
	}
	s.mu.Unlock()
	s.poke()
}

// holdLocked marks a command as expected back, holding the device's gate for it.
func (s *shardState) holdLocked(g *gate, commandToken string, now time.Time) {
	if g.unsettled == nil {
		g.unsettled = map[string]time.Time{}
	}
	g.unsettled[commandToken] = now.Add(redeliveryBudget)
	g.gen++
}

// gateUnconfirmed gates a device whose live confirmation errored, holding the gate for that
// command's redelivery.
func (s *shardState) gateUnconfirmed(k deviceKey, commandToken string, now time.Time) {
	s.mu.Lock()
	g := s.gateLocked(k)
	g.set(parkReasonUnconfirmed)
	s.holdLocked(g, commandToken, now)
	s.wantLocked(k, g, g.notBefore)
	s.mu.Unlock()
	s.poke()
}

// wake gates a device and asks for a drain turn now: Drain's half. It never drops the request —
// the ready list is bounded by the devices on this shard, not by a channel's capacity.
func (s *shardState) wake(k deviceKey) {
	s.mu.Lock()
	g := s.gateLocked(k)
	g.set(parkReasonBind)
	s.wantLocked(k, g, time.Time{})
	s.mu.Unlock()
	s.poke()
}

// pickEligible takes the first ready device that may drain now and returns it with its generation.
// A device is ineligible while an older live task of its is still queued, while a park of its is in
// flight, or until its deferral and unsettled deadlines have passed. The first two clear on an event
// that pokes the shard; for the last, a timer is armed at the earliest such time, because without it
// a device with no further traffic would never be retried.
func (s *shardState) pickEligible(clk dispatchClock) (deviceKey, uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := clk.Now()
	var earliest time.Time
	for i, k := range s.ready {
		g := s.devs[k]
		g.prune(now)
		if g.queuedLive > 0 || g.parksInFlight > 0 {
			continue
		}
		if until := g.blockedUntil(); until.After(now) {
			if earliest.IsZero() || until.Before(earliest) {
				earliest = until
			}
			continue
		}
		s.ready = slices.Delete(s.ready, i, i+1)
		g.wanted = false
		return k, g.gen, true
	}
	if !earliest.IsZero() {
		if s.stopTimer != nil {
			s.stopTimer()
		}
		s.stopTimer = clk.AfterFunc(earliest.Sub(now), s.poke)
	}
	return deviceKey{}, 0, false
}

// turnResult is what one drain turn found.
type turnResult struct {
	fetched int  // rows the page held
	failed  bool // a fetch or claim error: retry after drainRetryDelay
	stopped bool // the device was not live, the term ended, the tenant is deleted, or draining is off
}

// finishTurn decides what the gate does after a turn, and reports whether the shard has more
// devices wanting a turn.
//
// The gate lifts only when every condition of the invariant holds: the page was short (so it was the
// whole backlog), nothing failed, nothing settled since the turn read the generation, no park is in
// flight and nothing awaits redelivery. Anything else keeps the device gated — and, unless the turn
// stopped because the device is gone, ready for another turn.
func (s *shardState) finishTurn(k deviceKey, gen0 uint64, res turnResult, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.gateLocked(k)
	g.prune(now)
	switch {
	case res.stopped:
		// Not live (the next bind gates and wakes it again), or the term is ending. It stays
		// gated, which is harmless: an offline device's commands park anyway.
	case res.failed:
		s.wantLocked(k, g, now.Add(drainRetryDelay))
	case res.fetched >= drainTurnMax, g.gen != gen0, g.parksInFlight > 0, len(g.unsettled) > 0, g.queuedLive > 0:
		s.wantLocked(k, g, time.Time{})
	default:
		g.gated, g.reason = false, ""
		g.notBefore = time.Time{}
		s.maybeDeleteLocked(k, g)
	}
	return len(s.ready) > 0
}

// dispatchClock is the dispatcher's time source: the gate's deadlines and the deferred-turn timer.
// A seam so the deferral and the redelivery deadline are testable without waiting them out.
type dispatchClock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}
