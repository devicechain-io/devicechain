// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// A FINAL delivery whose resolved fan-out was refused for shard placement (ErrUnplacedTenant)
// is HELD here rather than left unacked. Left unacked, the broker never redelivers it (the
// delivery budget is spent): the max-delivery recorder letters the source and its events are
// never resolved. While held, the source is kept alive with InProgress, and once the tenant's
// placement answers again ONLY the outputs that were refused are handed back to the resolved
// loop, through the same path, with the same source, tenant, fan-out index and correlation: the
// same dedup id, or the same envelope key and member index walked as a redelivery. Outputs
// already stored are not published again. The source is acked by settleResolved once every
// re-sent output is stored; a second placement refusal holds it again, and any other failure
// releases it unacked (exhausted, as before).
//
// Every final delivery is WATCHED from the moment its fan-out is handed to the resolved loop,
// not from the moment its last output settles: a publish can wait behind the writer's window
// or for an envelope walker for longer than AckWait, and a final delivery whose AckWait lapses
// meanwhile is given up on by the broker before it could be held. A watched source is beaten
// like a held one; it is polled for placement only once its outputs have all settled.
//
// The guarantee is at-least-once, not exactly-once. A refused output was not stored by THIS
// attempt (the refusal is decided before the send), but an earlier delivery may have stored it
// and lost the PubAck; once the hold outlasts the duplicate window (and, for envelopes, the
// round engine's memory of the round) that output can be stored a second time.
//
// The hold belongs to one processor run and is bounded in sources. Shutdown abandons what it
// holds UNACKED: a final delivery then exhausts and is lettered, the same residual as
// core/storehold.
//
// Placement readiness is placement only, never backpressure: the resolved writer refuses a
// publish for placement but not for backpressure (the readers park before the fetch for that).
// The check must be local and cheap (the router's in-memory placement): the loop that runs it
// also beats every source.

// placementHoldLimit bounds the sources one processor run watches.
const placementHoldLimit = 4096

// heldOutput is a refused output kept for re-sending: its place in the source's fan-out and the
// resolved event itself.
type heldOutput struct {
	index int
	event *resolvedevent.ResolvedEvent
}

// holdEntry is one watched source. idle: every output has settled and some were refused, so it
// waits for placement; otherwise its outputs are in flight (first attempt, queued, or re-sent).
// held: it has been refused at least once (the gauge counts it).
type holdEntry struct {
	idle, held bool
}

type placementHolds struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	entries map[*ackCoord]*holdEntry
	queue   chan *ackCoord

	check  func(tenant string) error
	gauge  prometheus.Gauge
	gaveUp prometheus.Counter
}

// startPlacementHolds starts the hold of one processor run: a loop that beats and polls, and
// a worker that re-sends with send. send must return false once it gave up (shutdown).
func startPlacementHolds(check func(string) error, gauge prometheus.Gauge, gaveUp prometheus.Counter,
	beat, poll time.Duration, send func(ctx context.Context, item resolvedItem) bool) *placementHolds {
	ctx, cancel := context.WithCancel(context.Background())
	h := &placementHolds{ctx: ctx, cancel: cancel, done: make(chan struct{}),
		entries: make(map[*ackCoord]*holdEntry), queue: make(chan *ackCoord, placementHoldLimit),
		check: check, gauge: gauge, gaveUp: gaveUp}
	if beat <= 0 {
		beat = messaging.AckWait / 3
	}
	if poll <= 0 {
		poll = time.Second
	}
	var running sync.WaitGroup
	running.Add(2)
	go func() { defer running.Done(); h.loop(beat, poll) }()
	go func() {
		defer running.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case c := <-h.queue:
				h.resend(c, send)
			}
		}
	}()
	go func() {
		running.Wait()
		// Abandoned, whether or not their outputs ever settle: a cancelled walk or an
		// interrupted re-send may never call back.
		h.mu.Lock()
		held := 0
		for _, e := range h.entries {
			if e.held {
				held++
			}
		}
		if h.gauge != nil {
			h.gauge.Sub(float64(held))
		}
		if h.gaveUp != nil {
			h.gaveUp.Add(float64(held))
		}
		clear(h.entries)
		h.mu.Unlock()
		close(h.done)
	}()
	return h
}

// watch starts beating c, a final delivery whose fan-out is about to be handed to the resolved
// loop. Past the limit it is not watched (it can still be held when it is refused).
func (h *placementHolds) watch(c *ackCoord) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx.Err() != nil || len(h.entries) >= placementHoldLimit {
		return
	}
	h.entries[c] = &holdEntry{}
	// Renew at once: the next beat can be AckWait/3 away, and this delivery may already have
	// spent most of its AckWait waiting for a resolver. Under mu, like every beat, so none
	// follows finish.
	_ = c.src.InProgress()
}

// hold makes c, whose outputs have all settled with only placement refusals, wait for placement:
// it is registered if it was not watched, or marked idle again when it was (a re-send refused
// again). It reports whether c is held; false leaves the source unacked for the max-delivery
// recorder. The caller holds c.mu.
func (h *placementHolds) hold(c *ackCoord) bool {
	if h == nil {
		log.Error().Str("tenant", c.tenant).
			Msg("A final delivery's resolved event was refused for shard placement and no placement hold is wired; leaving it unacked for the max-delivery recorder.")
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx.Err() != nil {
		return false
	}
	e, ok := h.entries[c]
	if !ok {
		if len(h.entries) >= placementHoldLimit {
			log.Error().Str("tenant", c.tenant).Int("limit", placementHoldLimit).
				Msg("Resolved-event placement hold is full; leaving the final delivery unacked for the max-delivery recorder.")
			if h.gaveUp != nil {
				h.gaveUp.Inc()
			}
			return false
		}
		e = &holdEntry{}
		h.entries[c] = e
		// Renew at once: the publish may already have spent most of AckWait.
		_ = c.src.InProgress()
	}
	e.idle = true
	if !e.held {
		e.held = true
		if h.gauge != nil {
			h.gauge.Inc()
		}
	}
	return true
}

// finish ends c's watch, if it has one, and reports whether c had been held. It is called
// before the source is acked, so no beat follows the ack.
func (h *placementHolds) finish(c *ackCoord) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.entries[c]
	if !ok {
		return false
	}
	delete(h.entries, c)
	if e.held && h.gauge != nil {
		h.gauge.Dec()
	}
	return e.held
}

// release ends c's watch on a failure the hold cannot cure, counting a source it had held as
// given up.
func (h *placementHolds) release(c *ackCoord) {
	if h.finish(c) && h.gaveUp != nil {
		h.gaveUp.Inc()
	}
}

func (h *placementHolds) loop(beat, poll time.Duration) {
	beats, polls := time.NewTicker(beat), time.NewTicker(poll)
	defer beats.Stop()
	defer polls.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-beats.C:
			// Every watched source, idle or in flight. Under mu, so no beat follows finish
			// (and so the ack).
			h.mu.Lock()
			for c := range h.entries {
				_ = c.src.InProgress()
			}
			h.mu.Unlock()
		case <-polls.C:
			h.mu.Lock()
			var idle []*ackCoord
			for c, e := range h.entries {
				if e.idle {
					idle = append(idle, c)
				}
			}
			h.mu.Unlock()
			for _, c := range idle {
				if h.ctx.Err() != nil {
					return
				}
				if h.check(c.tenant) != nil {
					continue
				}
				h.mu.Lock()
				if e, ok := h.entries[c]; ok && e.idle && h.ctx.Err() == nil {
					e.idle = false
					// At most placementHoldLimit entries exist, each queued at most once at a
					// time: this send never blocks the loop.
					select {
					case h.queue <- c:
					case <-h.ctx.Done():
					}
				}
				h.mu.Unlock()
			}
		}
	}
}

// resend hands c's refused outputs back to the resolved loop. They are taken, and the
// outstanding count reset, under c.mu: nothing of c is in flight while it is held, so every
// settle after this belongs to the re-send. No lock is held while sending.
func (h *placementHolds) resend(c *ackCoord, send func(context.Context, resolvedItem) bool) {
	c.mu.Lock()
	outputs := c.refused
	c.refused = nil
	c.resending = outputs
	c.remaining = len(outputs)
	c.mu.Unlock()
	if len(outputs) == 0 {
		// Not reachable: a coord is held only with refusals. Never leave it beaten forever.
		h.release(c)
		return
	}
	for _, o := range outputs {
		item := resolvedItem{tenant: c.tenant, event: *o.event, coord: c, index: o.index, correlation: c.correlation}
		if !send(h.ctx, item) {
			return
		}
	}
}
