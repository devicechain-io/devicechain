// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// SLOW_RESOLVE_WARN is the resolution time above which a resolve is reported. Resolution
// is normally milliseconds; five seconds is one JetStream request timeout, and a twelfth
// of the acknowledgement window.
const SLOW_RESOLVE_WARN = 5 * time.Second

// slowResolveSummaryEvery is the most often a slow resolve is logged after the first.
const slowResolveSummaryEvery = 30 * time.Second

// slowResolveReporter makes a slow resolve visible, whatever it was waiting on.
//
// 🔑 IT EXISTS BECAUSE A STALLED RESOLVER USED TO SAY NOTHING. Resolution time went into
// the RED histogram and nowhere else, so when every resolver sat waiting out a JetStream
// timeout for a minute, the only trace was a histogram bucket nobody was looking at. One
// line for the first slow resolve says it is happening; after that one summary line per
// window, with a count and the slowest, says how bad it is without a line per event.
//
// The pool shares ONE reporter, for the reason it shares one location memo: the workers
// read the same channel, so a reporter per worker would log the same stall once per
// worker. It measures ResolveEvent only, not the hand-off of the result, so it reports
// resolution being slow rather than a full downstream channel.
type slowResolveReporter struct {
	threshold time.Duration
	every     time.Duration
	now       func() time.Time

	mu          sync.Mutex
	lastLog     time.Time
	count       int
	slowest     time.Duration
	correlation string
}

func newSlowResolveReporter() *slowResolveReporter {
	return &slowResolveReporter{threshold: SLOW_RESOLVE_WARN, every: slowResolveSummaryEvery, now: time.Now}
}

// observe records one resolve's duration. It is called for EVERY resolve, fast ones
// included, so the slow resolves counted since the last line are reported by the first
// resolve after the window closes, even when that one was fast.
func (s *slowResolveReporter) observe(d time.Duration, correlation string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d >= s.threshold {
		s.count++
		if d > s.slowest {
			s.slowest = d
		}
		s.correlation = correlation
	}
	if s.count == 0 {
		return
	}
	now := s.now()
	if !s.lastLog.IsZero() && now.Sub(s.lastLog) < s.every {
		return
	}
	log.Warn().Int("slow", s.count).Dur("slowest", s.slowest).Str("correlation", s.correlation).
		Dur("window", s.every).
		Msg("Event resolution is slow")
	s.count, s.slowest, s.correlation = 0, 0, ""
	s.lastLog = now
}
