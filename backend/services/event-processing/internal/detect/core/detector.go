// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import "time"

// Detector is the engine as the live DETECT loop drives it: the single-key-space *Engine, or the
// K-way *Sharded that answers every one of these exactly as one engine would. Everything that
// holds the live engine (the processor's term build and replay, snapshot restore, the dead-man
// armer, the tenant purge and the state gauges) takes this rather than a concrete type, so the
// shard count is chosen in one place and nothing else can tell.
//
// The replay preview deliberately does NOT use it: it builds a fresh *Engine per request.
type Detector interface {
	UpsertRule(r Rule)
	RemoveRule(id string)
	RemoveMatching(match func(ruleID string) bool) int
	Descope(ruleID, series string, at time.Time) bool
	ProcessEvent(ev Event)
	ProcessResolved(seq uint64, t time.Time, evs []Event)
	Skip(seq uint64) bool
	Advance(w time.Time) bool
	SetExpected(key SeriesKey, since time.Time)
	RemoveExpected(key SeriesKey)
	ClearRaised(key SeriesKey)
	Drain() []Detection
	DrainLateSamples() uint64
	Watermark() time.Time
	LastSeq() uint64
	HasPendingWork() bool
	PendingTimerCount() int
	LiveKeyCounts() map[string]int
	RetainedSampleCounts() map[string]int
	ExpectedKeys() []SeriesKey
	HeartbeatAbsenceKeys() []SeriesKey
	Snapshot() ([]byte, error)
}

var (
	_ Detector = (*Engine)(nil)
	_ Detector = (*Sharded)(nil)
)
