// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

const finalPlacementHoldLimit = 4096

// defaultFinalPlacementHoldMaxAge bounds how long one delivery is held. A placement
// that has not returned by then is not a blip, and a tenant whose placement never
// returns must not keep spending the per-source finalPlacementHoldLimit that other
// tenants' final refusals need. Past it the delivery falls back to the unheld
// behaviour: captured streams leave it unacked for their max-delivery recorder;
// the WAL, which has no recorder, falls back to failed-decode.
const defaultFinalPlacementHoldMaxAge = 15 * time.Minute

// finalPlacementFullLogEvery spaces the "hold is full" log; every refusal beyond
// it is counted instead.
const finalPlacementFullLogEvery = time.Minute

// Reasons a final delivery fell back to unheld, on the fallback counter.
const (
	placementFallbackFull     = "full"
	placementFallbackExpired  = "expired"
	placementFallbackShutdown = "shutdown"
)

type finalPlacementHold struct {
	owner   *finalPlacementHolds
	msg     messaging.Message
	tenant  string
	running bool // protected by owner.mu, including while queued
	since   time.Time
}

// finalPlacementHolds belongs to one pipeline run. No callback takes runMu.
// Shutdown abandons holds UNACKED. Captured streams have a max-delivery recorder;
// the WAL has none, and its retained entry needs recovery outside the stopped run.
type finalPlacementHolds struct {
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	entries map[*finalPlacementHold]struct{}
	queue   chan *finalPlacementHold
	check   func(string) error
	gauge   prometheus.Gauge
	source  string
	expired func(*finalPlacementHold)
	// maxAge caps one hold's age; fallback counts deliveries that were not held (or
	// stopped being) by reason. lastFullLog and fullSuppressed rate-limit the full log.
	maxAge         time.Duration
	fallback       *prometheus.CounterVec
	lastFullLog    time.Time
	fullSuppressed int
}

func (es *GatewayJetStreamSource) startFinalPlacementHolds() *finalPlacementHolds {
	ctx, cancel := context.WithCancel(context.Background())
	h := &finalPlacementHolds{ctx: ctx, cancel: cancel, done: make(chan struct{}),
		entries: make(map[*finalPlacementHold]struct{}), queue: make(chan *finalPlacementHold, finalPlacementHoldLimit),
		check: es.placementCheck, gauge: es.placementHeld, source: es.Id,
		maxAge: es.finalHoldMaxAge, fallback: es.placementHoldFallback}
	if es.fromStore {
		h.expired = func(entry *finalPlacementHold) {
			// Do not wrap ErrUnplacedTenant: this is the terminal fallback, not
			// another placement retry. Keep the hold's shutdown guard and owner.
			es.settler(entry.msg, entry.tenant, entry)(fmt.Errorf("final WAL placement hold expired: %s", messaging.ErrUnplacedTenant))
		}
	}
	if h.maxAge <= 0 {
		h.maxAge = defaultFinalPlacementHoldMaxAge
	}
	beat, poll := es.finalHoldBeat, es.finalHoldPoll
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
			case entry := <-h.queue:
				if ctx.Err() != nil {
					return
				}
				// An entry that expired while queued is no longer held.
				h.mu.Lock()
				_, held := h.entries[entry]
				h.mu.Unlock()
				if !held {
					continue
				}
				es.handle(entry.msg, entry)
			}
		}
	}()
	go func() {
		running.Wait()
		h.mu.Lock()
		// Count before the gauge drops, so abandoned holds never disappear silently.
		h.countFallback(placementFallbackShutdown, len(h.entries))
		if h.gauge != nil {
			h.gauge.Sub(float64(len(h.entries)))
		}
		clear(h.entries)
		h.mu.Unlock()
		close(h.done)
	}()
	return h
}

// hold reports whether this delivery is held, or was abandoned because the run
// stopped. False means the caller must apply its unheld final-delivery policy.
func (h *finalPlacementHolds) hold(msg messaging.Message, tenant string, entry *finalPlacementHold) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx.Err() != nil {
		return true
	}
	if entry != nil {
		if _, ok := h.entries[entry]; ok {
			entry.running = false
		}
		return true
	}
	if len(h.entries) >= finalPlacementHoldLimit {
		h.countFallback(placementFallbackFull, 1)
		if now := time.Now(); h.lastFullLog.IsZero() || now.Sub(h.lastFullLog) >= finalPlacementFullLogEvery {
			log.Error().Str("source", h.source).Str("tenant", tenant).Int("limit", finalPlacementHoldLimit).
				Int("suppressed", h.fullSuppressed).
				Msg("Final placement hold is full; using the source's unheld final-delivery policy.")
			h.lastFullLog, h.fullSuppressed = now, 0
		} else {
			h.fullSuppressed++
		}
		return false
	}
	entry = &finalPlacementHold{owner: h, msg: msg, tenant: tenant, since: time.Now()}
	h.entries[entry] = struct{}{}
	if h.gauge != nil {
		h.gauge.Inc()
	}
	// Renew immediately: the publish may already have spent most of AckWait.
	_ = msg.InProgress()
	return true
}

func (h *finalPlacementHolds) countFallback(reason string, n int) {
	if h.fallback != nil {
		h.fallback.WithLabelValues(h.source, reason).Add(float64(n))
	}
}

// expire gives up on holds older than maxAge. It drops them from the table (so they
// are no longer beaten and free their slot), then applies the source's unheld
// policy. Only captured streams have a max-delivery recorder; the WAL routes
// expired holds to failed-decode and acknowledges only after that publish.
// An entry that is queued or re-running (running) is NOT expired: it stays in the table
// until its re-run settles it or hands it back, so the bounded queue keeps its invariant
// (every queued entry is in the table) and a re-run in flight keeps its heartbeat owner.
// It is then judged on the next poll.
func (h *finalPlacementHolds) expire(now time.Time) {
	h.mu.Lock()
	if h.ctx.Err() != nil {
		h.mu.Unlock()
		return
	}
	var entries []*finalPlacementHold
	expired := 0
	var tenant string
	for entry := range h.entries {
		if !entry.running && now.Sub(entry.since) >= h.maxAge {
			delete(h.entries, entry)
			entries = append(entries, entry)
			expired++
			tenant = entry.tenant
		}
	}
	if expired > 0 {
		// Count BEFORE the gauge drops, so a scrape never shows a hold gone without its fallback.
		h.countFallback(placementFallbackExpired, expired)
		if h.gauge != nil {
			h.gauge.Sub(float64(expired))
		}
	}
	h.mu.Unlock()
	if expired > 0 {
		log.Warn().Str("source", h.source).Str("tenant", tenant).Int("expired", expired).Dur("maxAge", h.maxAge).
			Msg("Final placement holds exceeded their maximum age; using the source's unheld final-delivery policy.")
	}
	// Routing can finish a hold or call a publisher: never invoke it under mu.
	if h.expired != nil {
		for _, entry := range entries {
			h.expired(entry)
		}
	}
}

func (h *finalPlacementHolds) finish(entry *finalPlacementHold) {
	if h == nil || entry == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.entries[entry]; ok {
		delete(h.entries, entry)
		if h.gauge != nil {
			h.gauge.Dec()
		}
	}
}

func (h *finalPlacementHolds) loop(beat, poll time.Duration) {
	beats, polls := time.NewTicker(beat), time.NewTicker(poll)
	defer beats.Stop()
	defer polls.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-beats.C:
			// Include queued and asynchronously re-running entries. Serialize with
			// finish so no heartbeat follows the terminal Ack callback.
			h.mu.Lock()
			for entry := range h.entries {
				_ = entry.msg.InProgress()
			}
			h.mu.Unlock()
		case <-polls.C:
			h.expire(time.Now())
			h.mu.Lock()
			var idle []*finalPlacementHold
			for entry := range h.entries {
				if !entry.running {
					idle = append(idle, entry)
				}
			}
			h.mu.Unlock()
			for _, entry := range idle {
				if h.ctx.Err() != nil {
					return
				}
				if h.check(entry.tenant) != nil {
					continue
				}
				h.mu.Lock()
				if _, ok := h.entries[entry]; ok && !entry.running && h.ctx.Err() == nil {
					entry.running = true
					// At most N entries exist, each queued once: this bounded queue
					// never blocks the beat/poll loop behind decode or a publish.
					select {
					case h.queue <- entry:
					case <-h.ctx.Done():
					}
				}
				h.mu.Unlock()
			}
		}
	}
}
