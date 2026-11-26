// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package testkit is the shared test harness for the install event core: a
// manual clock, a capture sink that can be stalled, a stream verifier, a
// scripted engine, a scripted responder and the secret-canary harness. It
// never touches Docker, kind, OpenTofu, Helm, Kubernetes or credentials.
package testkit

import (
	"sort"
	"sync"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// Epoch is the fixed start time tests use.
var Epoch = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

// ManualClock is a clock that moves only when Advance is called.
type ManualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualTimer
}

type manualTimer struct {
	c       *ManualClock
	at      time.Time
	f       func()
	stopped bool
}

// NewManualClock returns a clock at start.
func NewManualClock(start time.Time) *ManualClock { return &ManualClock{now: start} }

// Now returns the clock's time.
func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc schedules f for when the clock reaches now+d.
func (c *ManualClock) AfterFunc(d time.Duration, f func()) installflow.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &manualTimer{c: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *manualTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}

// Advance moves the clock forward by d and runs, in deadline order and on the
// calling goroutine, every timer that has come due.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due, rest []*manualTimer
	for _, t := range c.timers {
		switch {
		case t.stopped:
		case !t.at.After(c.now):
			t.stopped = true
			due = append(due, t)
		default:
			rest = append(rest, t)
		}
	}
	c.timers = rest
	c.mu.Unlock()
	sort.SliceStable(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
	for _, t := range due {
		t.f()
	}
}
