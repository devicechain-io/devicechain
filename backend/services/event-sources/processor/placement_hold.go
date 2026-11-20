// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

const finalPlacementHoldLimit = 4096

type finalPlacementHold struct {
	owner   *finalPlacementHolds
	msg     messaging.Message
	tenant  string
	running bool // protected by owner.mu, including while queued
}

// finalPlacementHolds belongs to one pipeline run. No callback takes runMu.
// Shutdown abandons holds UNACKED: a final delivery then exhausts and the
// max-delivery recorder letters it, the same residual as core/storehold.
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
}

func (es *GatewayJetStreamSource) startFinalPlacementHolds() *finalPlacementHolds {
	ctx, cancel := context.WithCancel(context.Background())
	h := &finalPlacementHolds{ctx: ctx, cancel: cancel, done: make(chan struct{}),
		entries: make(map[*finalPlacementHold]struct{}), queue: make(chan *finalPlacementHold, finalPlacementHoldLimit),
		check: es.placementCheck, gauge: es.placementHeld, source: es.Id}
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
				es.handle(entry.msg, entry)
			}
		}
	}()
	go func() {
		running.Wait()
		h.mu.Lock()
		if h.gauge != nil {
			h.gauge.Sub(float64(len(h.entries)))
		}
		clear(h.entries)
		h.mu.Unlock()
		close(h.done)
	}()
	return h
}

func (h *finalPlacementHolds) hold(msg messaging.Message, tenant string, entry *finalPlacementHold) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx.Err() != nil {
		return
	}
	if entry != nil {
		if _, ok := h.entries[entry]; ok {
			entry.running = false
		}
		return
	}
	if len(h.entries) >= finalPlacementHoldLimit {
		log.Error().Str("source", h.source).Str("tenant", tenant).Int("limit", finalPlacementHoldLimit).
			Msg("Final placement hold is full; leaving the delivery unacked for the max-delivery recorder.")
		return
	}
	entry = &finalPlacementHold{owner: h, msg: msg, tenant: tenant}
	h.entries[entry] = struct{}{}
	if h.gauge != nil {
		h.gauge.Inc()
	}
	// Renew immediately: the publish may already have spent most of AckWait.
	_ = msg.InProgress()
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
