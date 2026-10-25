// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-simulator/loadtest/detmonitor"
	"github.com/rs/zerolog/log"
)

// The live detection signal used to be skipped for any run longer than an access token lives:
// the socket is dialled once, the server closes it when the token it was opened with expires,
// and the run refused to start one it could not finish ("a freshly issued token is valid for
// 15m0s, and 1h4m5s was needed"). So every long run, which is every release-candidate run,
// rested on the durable oracle alone.
//
// rotatingMonitor holds the signal across a run of any length by re-dialling with a fresh
// token before the old one expires. Each segment asks only for a token that outlives ITS
// window, so the token lifetime bounds the rotation interval and no longer bounds the run.
// A segment is stopped before the next is dialled, so no edge is counted twice; the instant
// between the two is a blind spot, which is acceptable for a signal that is evidence, not the
// verdict (the detection-lag invariant reads the durable alarm and does not depend on it).

// liveRotateEvery is how long one live socket is held before it is replaced. It plus
// tokenMargin must fit inside an access token's lifetime (15 minutes by default).
const liveRotateEvery = 5 * time.Minute

// liveRedialRetry is how soon a failed re-dial is tried again.
const liveRedialRetry = 10 * time.Second

// liveSocket is one dialled, subscribed detectionStream watcher.
type liveSocket interface {
	Stop() error
	Report() detmonitor.Report
}

// rotatingMonitor is a detectionStream watcher that survives past one token's life.
type rotatingMonitor struct {
	// dial fetches a fresh token, dials and subscribes. The window is how long the socket
	// must stay valid.
	dial  func(ctx context.Context, window time.Duration) (liveSocket, error)
	every time.Duration
	// retry is the wait before a failed re-dial is retried.
	retry time.Duration

	mu       sync.Mutex
	cur      liveSocket
	segments []detmonitor.Report
	redials  int
	failures int
	stopped  bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newRotatingMonitor(dial func(ctx context.Context, window time.Duration) (liveSocket, error)) *rotatingMonitor {
	return &rotatingMonitor{dial: dial, every: liveRotateEvery, retry: liveRedialRetry}
}

// window is how long each segment's token must outlive the dial.
func (m *rotatingMonitor) window() time.Duration { return m.every + tokenMargin }

// Start dials the first segment and, if it succeeds, rotates it every m.every until Stop. A
// failure on the first dial is returned (the run proceeds without a live signal and says why).
func (m *rotatingMonitor) Start(ctx context.Context) error {
	first, err := m.dial(ctx, m.window())
	if err != nil {
		return err
	}
	m.cur = first
	rctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.wg.Add(1)
	go m.rotate(rctx)
	return nil
}

func (m *rotatingMonitor) rotate(ctx context.Context) {
	defer m.wg.Done()
	wait := m.every
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = m.every
		m.mu.Lock()
		old := m.cur
		m.cur = nil
		m.mu.Unlock()
		if old != nil {
			_ = old.Stop()
			m.mu.Lock()
			m.segments = append(m.segments, old.Report())
			m.mu.Unlock()
		}
		next, err := m.dial(ctx, m.window())
		m.mu.Lock()
		if err != nil {
			m.failures++
			wait = m.retry
			log.Warn().Err(err).Msg("could not re-dial the detectionStream with a fresh token; the live signal is blind until it can")
			m.mu.Unlock()
			continue
		}
		if m.stopped || ctx.Err() != nil {
			m.mu.Unlock()
			_ = next.Stop()
			return
		}
		m.cur = next
		m.redials++
		m.mu.Unlock()
	}
}

// Stop ends rotation and the current segment.
func (m *rotatingMonitor) Stop() {
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	m.mu.Lock()
	cur := m.cur
	m.cur = nil
	m.mu.Unlock()
	if cur != nil {
		_ = cur.Stop()
		m.mu.Lock()
		m.segments = append(m.segments, cur.Report())
		m.mu.Unlock()
	}
}

// Report merges every segment's tallies. Call after Stop.
func (m *rotatingMonitor) Report() (detmonitor.Report, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := detmonitor.Report{BySeries: map[string]*detmonitor.SeriesTally{}}
	for _, seg := range m.segments {
		out.Observed += seg.Observed
		for k, v := range seg.BySeries {
			t := out.BySeries[k]
			if t == nil {
				t = &detmonitor.SeriesTally{}
				out.BySeries[k] = t
			}
			t.Raised += v.Raised
			t.Resolved += v.Resolved
		}
		out.Violations = append(out.Violations, seg.Violations...)
	}
	if m.failures > 0 {
		out.Violations = append(out.Violations, detmonitor.Violation{Kind: detmonitor.ViolLostView,
			Detail: fmt.Sprintf("%d re-dial(s) with a fresh token failed; the signal was blind until the next succeeded", m.failures)})
	}
	return out, m.redials
}
