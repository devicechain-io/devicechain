// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package core is the DeviceChain-owned keyed-streaming detection core (ADR-051).
//
// It is deliberately NOT a general stream engine: it exists because no off-the-shelf
// library gives us per-(tenant, rule, series) keying, event-time windows/timers, and
// state that snapshots atomically with the JetStream sequence — the combination that
// makes replay-after-restart CORRECT rather than false-or-missed. This file holds the
// clock seam; the watermark (logical time) lives on the Engine and is advanced by
// event timestamps, with idle wall-clock advance in live operation supplied via Clock.
package core

import "time"

// Clock is the source of wall-clock time, used ONLY to advance the watermark when the
// input stream is idle (so absence/duration timers still fire live with no new events).
// During replay the watermark is driven entirely by event timestamps, so the Clock is
// not consulted — which is exactly why replay is deterministic and testable.
type Clock interface {
	Now() time.Time
}

// RealClock is the production wall clock.
type RealClock struct{}

// Now returns the current wall-clock time.
func (RealClock) Now() time.Time { return time.Now() }

// ManualClock is a test/replay clock whose time only moves when Set is called.
type ManualClock struct{ t time.Time }

// NewManualClock returns a ManualClock pinned at t.
func NewManualClock(t time.Time) *ManualClock { return &ManualClock{t: t} }

// Now returns the pinned time.
func (m *ManualClock) Now() time.Time { return m.t }

// Set advances (or moves) the pinned time.
func (m *ManualClock) Set(t time.Time) { m.t = t }
