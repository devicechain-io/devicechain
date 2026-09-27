// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"sync"
	"time"
)

// AcceptedLedger records, per device token, the latest occurredTime of every event the
// ingress ACCEPTED (202). It is what a load test compares the live-state projection against:
// a device's live state has caught up once it reflects that device's last accepted event.
//
// Only accepted events are recorded. A shed (429) or failed emit never entered the pipeline
// (or may not have), so waiting for the projection to reflect it would wait for something
// that is not coming.
type AcceptedLedger struct {
	mu   sync.Mutex
	last map[string]time.Time
	// unparsed counts accepted events whose occurredTime could not be parsed back. The
	// simulator formats that string itself, so a non-zero count is a harness defect, and a
	// ledger carrying one cannot say what was accepted: the reader refuses it.
	unparsed int
}

// NewAcceptedLedger returns an empty ledger.
func NewAcceptedLedger() *AcceptedLedger {
	return &AcceptedLedger{last: map[string]time.Time{}}
}

// Record notes one accepted event, keeping the latest time per device. occurredTime is the
// RFC 3339 string the event was sent with. Nil-safe, so an emitter can call it on a runtime
// that keeps no ledger.
func (l *AcceptedLedger) Record(deviceToken, occurredTime string) {
	if l == nil {
		return
	}
	at, err := time.Parse(time.RFC3339Nano, occurredTime)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		l.unparsed++
		return
	}
	if prev, ok := l.last[deviceToken]; !ok || at.After(prev) {
		l.last[deviceToken] = at
	}
}

// Snapshot returns a copy of the latest accepted time per device, and how many accepted
// events could not be recorded.
func (l *AcceptedLedger) Snapshot() (map[string]time.Time, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]time.Time, len(l.last))
	for k, v := range l.last {
		out[k] = v
	}
	return out, l.unparsed
}
