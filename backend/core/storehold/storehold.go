// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package storehold is the one definition of "hold on store unavailable": what a stream
// consumer does with messages whose handling failed because the DATABASE could not take the
// write, as opposed to because the message is bad.
//
// 🔴 THIS EXISTS BECAUSE A DATABASE OUTAGE USED TO LOSE ACKNOWLEDGED DATA. A consumer that
// leaves a failed message unacked lets the broker redeliver it every AckWait; on the final
// delivery (messaging.MaxDeliver) most consumers give up, report the message to a letter
// (failed-events, a dead-letter, a counted drop) and ACK it. Five deliveries is four minutes,
// so any outage longer than that turned the whole in-flight window into acknowledged-then-
// discarded messages, one window per four minutes for as long as the outage lasted. The
// delivery budget is for a message that is the problem. When the STORE is the problem every
// message fails the same way, redelivery cannot be blamed on any of them, and they must not be
// counted against it. They are HELD.
//
// What a Holder gives a consumer:
//
//   - Outage: classifies an error. The server SAYING it cannot take writes
//     (rdb.IsStoreRefusingWrites) is an outage as is. A connection-class error
//     (rdb.IsConnectionFailure) is one only if a probe (Probe, bounded by Tuning.ProbeTimeout)
//     FAILS -- a payload that crashes its backend looks identical, and when the store answers
//     the message is the problem and takes its delivery budget as before.
//   - Run: a blocking wrapper for a single-message handler. It runs the store-touching
//     operation; on an outage it keeps the message(s) alive with InProgress (at once, then on a
//     third of AckWait, tracked across attempts), waits on a jittered probe backoff, and runs
//     the operation again once the store answers. Because the handler goroutine is blocked, the
//     consumer stops reading while it holds (back-pressure: the backlog waits unread in the
//     stream, where the stream's gate sees it). Shutdown (Halt closed) releases the message
//     unacked for the next pod.
//   - The pieces a batching consumer composes itself (event-management): StoreAnswers, Beat,
//     Jitter, Adjust.
//
// 🔴 THE OPERATION PASSED TO Run MUST BE IDEMPOTENT ON RE-EXECUTION: a redelivery runs it
// again, and so does Run after an outage. Each consumer documents why its own is.
package storehold

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// Tuning is the hold's timing. A consumer shares one Tuning across its Holders; tests shorten it.
type Tuning struct {
	// ProbeMin and ProbeMax bound the jittered, doubling wait between probes of a store that
	// is down.
	ProbeMin, ProbeMax time.Duration
	// ProbeTimeout bounds one probe, well under ProgressEvery so a probe against a blackholed
	// database cannot let a held message's ack-wait lapse.
	ProbeTimeout time.Duration
	// ProbeFresh is how long a successful probe is believed, so a run of dispositions after
	// one failed batch asks the store once, not once each.
	ProbeFresh time.Duration
	// ProgressEvery is how often held messages are told they are still in progress. A third
	// of AckWait leaves two missed beats of margin before the broker redelivers.
	ProgressEvery time.Duration
}

// DefaultTuning is the production timing.
func DefaultTuning() *Tuning {
	return &Tuning{
		ProbeMin:      2 * time.Second,
		ProbeMax:      30 * time.Second,
		ProbeTimeout:  5 * time.Second,
		ProbeFresh:    time.Second,
		ProgressEvery: messaging.AckWait / 3,
	}
}

// ErrReleased is returned by Run when shutdown ended the hold. The message is still unacked and
// must be left that way: it is not a failure of the message, so it takes no letter and no ack.
var ErrReleased = messaging.ErrHoldReleased

// Counter is the part of a gauge a Holder moves.
type Counter interface{ Add(float64) }

// NewGauge registers the per-consumer held gauge, devicechain_<service>_store_held_messages
// {consumer}. Build it once in the service's initialize phase (a collector belongs to the
// process and may be registered only once) and hand Gauge.WithLabelValues(consumer) to each Holder.
func NewGauge(ms *core.Microservice) *prometheus.GaugeVec {
	return ms.NewGaugeVec("store_held_messages",
		"Messages a consumer is holding unacked because its database cannot take writes (disk full, read-only, unreachable); they are processed when it recovers. Zero when healthy.",
		[]string{"consumer"})
}

var (
	gaugesMu sync.Mutex
	gauges   = map[*core.Microservice]*prometheus.GaugeVec{}
)

// GaugeFor returns the service's held gauge, registering it on first use. A collector belongs
// to the PROCESS and may be registered only once, while the processors that hold are rebuilt
// on every start, so the registration is cached per Microservice.
func GaugeFor(ms *core.Microservice) *prometheus.GaugeVec {
	gaugesMu.Lock()
	defer gaugesMu.Unlock()
	g, ok := gauges[ms]
	if !ok {
		g = NewGauge(ms)
		gauges[ms] = g
	}
	return g
}

// New builds the Holder for the consumer named consumer, counted on the service's
// store_held_messages{consumer} gauge, probing the store with probe.
func New(ms *core.Microservice, consumer string, probe func(ctx context.Context) error) *Holder {
	return &Holder{Name: consumer, Probe: probe, Held: GaugeFor(ms).WithLabelValues(consumer)}
}

// RDBProbe is the standard probe: SELECT 1 on the service's relational store.
func RDBProbe(r *rdb.RdbManager) func(ctx context.Context) error {
	return func(ctx context.Context) error { return r.DB(ctx).Exec("SELECT 1").Error }
}

// Holder holds one consumer's messages through a store outage. Safe for concurrent use.
type Holder struct {
	// Name labels the log lines.
	Name string
	// Probe asks the store a trivial question (SELECT 1) and returns nil when it answers.
	Probe func(ctx context.Context) error
	// Halt, when closed, ends every hold. Nil never closes.
	Halt <-chan struct{}
	// Held is the gauge moved by the number of messages held. Nil is allowed.
	Held Counter
	// Tune is the timing; nil takes DefaultTuning.
	Tune *Tuning

	mu       sync.Mutex
	probedOK time.Time
}

func (h *Holder) tune() *Tuning {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.Tune == nil {
		h.Tune = DefaultTuning()
	}
	return h.Tune
}

// StoreAnswers probes the store and reports whether it answered. A successful answer is
// believed for Tuning.ProbeFresh.
func (h *Holder) StoreAnswers(ctx context.Context) bool {
	t := h.tune()
	h.mu.Lock()
	if !h.probedOK.IsZero() && time.Since(h.probedOK) < t.ProbeFresh {
		h.mu.Unlock()
		return true
	}
	h.mu.Unlock()
	pctx, cancel := context.WithTimeout(ctx, t.ProbeTimeout)
	defer cancel()
	err := h.Probe(pctx)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		h.probedOK = time.Time{}
		return false
	}
	h.probedOK = time.Now()
	return true
}

// Outage reports whether err means the STORE, and not the message, is what failed.
func (h *Holder) Outage(ctx context.Context, err error) bool {
	switch {
	case err == nil:
		return false
	case rdb.IsStoreRefusingWrites(err):
		return true
	case rdb.IsConnectionFailure(err):
		return !h.StoreAnswers(ctx)
	}
	return false
}

// Beat tells the broker msg is in progress when it has not been told within ProgressEvery
// (or always, when force is set). progressAt is the caller's record of the last beat.
func (h *Holder) Beat(msg messaging.Message, progressAt *time.Time, force bool) {
	if !force && time.Since(*progressAt) < h.tune().ProgressEvery {
		return
	}
	if err := msg.InProgress(); err != nil {
		log.Warn().Err(err).Str("consumer", h.Name).Msg("Could not extend a held message's acknowledgement window")
		return
	}
	*progressAt = time.Now()
}

// NextBeat is when a message last told at progressAt next needs a beat.
func (h *Holder) NextBeat(progressAt time.Time) time.Time {
	return progressAt.Add(h.tune().ProgressEvery)
}

// ProgressEvery is the beat interval.
func (h *Holder) ProgressEvery() time.Duration { return h.tune().ProgressEvery }

// Adjust moves the held gauge by n. Nil-safe.
func (h *Holder) Adjust(n int) {
	if h.Held != nil && n != 0 {
		h.Held.Add(float64(n))
	}
}

// Jitter spreads d over 0.5x to 1.5x so holders do not probe in lock-step.
func Jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.5 + rand.Float64()))
}

// Run executes op and, while it fails because the store is down, holds msgs and executes it
// again once the store answers. It returns op's result: nil, or an error that is NOT an outage
// (which the caller dispositions as usual -- retry, letter, drop), or ErrReleased when
// shutdown ended the hold.
//
// msgs are the messages that stay alive meanwhile; pass every message the operation covers.
// op must be idempotent on re-execution.
//
// A nil Holder runs op once and holds nothing: the behaviour of a consumer built without one
// (a test, or a service that has not wired it), which production wiring tests pin as non-nil.
func (h *Holder) Run(ctx context.Context, msgs []messaging.Message, op func() error) error {
	if h == nil {
		return op()
	}
	var (
		start    time.Time
		attempts int
		beats    []time.Time
		held     bool
	)
	for {
		err := op()
		if err == nil || !h.Outage(ctx, err) {
			if held {
				h.Adjust(-len(msgs))
				if err == nil {
					log.Info().Str("consumer", h.Name).Int("messages", len(msgs)).Dur("heldFor", time.Since(start)).
						Msg("Store accepts writes again; held messages processed")
				}
			}
			return err
		}
		attempts++
		if !held {
			held = true
			start = time.Now()
			beats = make([]time.Time, len(msgs))
			h.Adjust(len(msgs))
			log.Warn().Err(err).Str("consumer", h.Name).Int("messages", len(msgs)).
				Msg("Store cannot take writes; holding messages (unacked, not counting against their delivery budget) until it recovers")
		}
		// Told at once, not at the next tick: the message may already have waited most of an
		// AckWait in a channel or the fetch buffer.
		for i, m := range msgs {
			h.Beat(m, &beats[i], attempts == 1)
		}
		if !h.waitStore(ctx, msgs, beats) {
			h.Adjust(-len(msgs))
			log.Info().Str("consumer", h.Name).Int("messages", len(msgs)).Dur("heldFor", time.Since(start)).
				Msg("Shutdown while holding; messages left unacked for redelivery")
			return ErrReleased
		}
	}
}

func (h *Holder) beatAll(msgs []messaging.Message, beats []time.Time) {
	for i, m := range msgs {
		h.Beat(m, &beats[i], false)
	}
}

// waitStore blocks on jittered, doubling probes (beating meanwhile) until the store answers.
// It reports false if Halt closed or ctx ended first.
func (h *Holder) waitStore(ctx context.Context, msgs []messaging.Message, beats []time.Time) bool {
	t := h.tune()
	wait := t.ProbeMin
	for {
		if !h.sleep(ctx, Jitter(wait), msgs, beats) {
			return false
		}
		h.beatAll(msgs, beats)
		answered := h.StoreAnswers(ctx)
		h.beatAll(msgs, beats)
		if answered {
			return true
		}
		wait = min(wait*2, t.ProbeMax)
	}
}

// sleep waits d, sending the beats that fall due. It reports false if Halt or ctx closed first.
func (h *Holder) sleep(ctx context.Context, d time.Duration, msgs []messaging.Message, beats []time.Time) bool {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	step := max(time.Millisecond, h.tune().ProgressEvery/2)
	for {
		t := time.NewTimer(step)
		select {
		case <-h.Halt:
			t.Stop()
			return false
		case <-ctx.Done():
			t.Stop()
			return false
		case <-deadline.C:
			t.Stop()
			return true
		case <-t.C:
			h.beatAll(msgs, beats)
		}
	}
}
