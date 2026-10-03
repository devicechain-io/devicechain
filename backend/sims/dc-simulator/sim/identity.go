// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// EmitOutcome is what the ingress said about one emit, as the identity ledger files it.
//
// The three classes are what a reconciliation can conclude about the event store from the
// response alone. They are deliberately NOT the same split as Stats' accepted / shed /
// backpressured / failed, which is about accounting the drive: a 400 and a timeout are
// both Stats.Failed, but the first was never published and the second may have been
// stored, and only this split says so.
type EmitOutcome uint8

const (
	// OutcomeAccepted is a 202: the event was published into the pipeline and must be
	// stored exactly once.
	OutcomeAccepted EmitOutcome = iota + 1
	// OutcomeRefused is a response the ingress gives BEFORE it publishes: a 429, a 503
	// carrying a Retry-After (backpressure), or any other 4xx. The event must be absent.
	OutcomeRefused
	// OutcomeAmbiguous is a response that leaves the store's state unknown: a transport
	// error, a 503 without a Retry-After (the publish failed, and a publish that timed out
	// may have been stored), any other 5xx, or any other status. The event may be stored
	// once, or not at all, and never more than once.
	OutcomeAmbiguous
)

func (o EmitOutcome) String() string {
	switch o {
	case OutcomeAccepted:
		return "accepted"
	case OutcomeRefused:
		return "refused"
	case OutcomeAmbiguous:
		return "ambiguous"
	}
	return "unknown"
}

// classifyResponse is the ONE reading of an ingress response. postEvent files the
// ledger and builds its error from what this returns, so the refusal classification
// cannot be decided twice, once for the drive's counters and once for the reconciliation.
//
// refusal is the sentinel the drive's counters route on (ErrShed for a 429,
// ErrBackpressured and ErrShed for a backpressure 503); it is nil for an accept and for
// any other status. A non-429 4xx is therefore Refused here but still Stats.Failed, as
// it was before the ledger existed: it was not shed, it was rejected.
//
// 🔴 A 503 WITHOUT a Retry-After is Ambiguous, never Refused. The ingress answers a bare
// 503 when its publish failed, and a publish that timed out may have been stored. Filed
// as refused, a stored copy would read as a false refusal and fail a correct run; the
// opposite mistake, a backpressure 503 filed ambiguous, would hide a stored refusal.
func classifyResponse(status int, retryAfter string) (EmitOutcome, error) {
	switch {
	case status == http.StatusAccepted:
		return OutcomeAccepted, nil
	case status == http.StatusTooManyRequests:
		return OutcomeRefused, ErrShed
	case status == http.StatusServiceUnavailable && retryAfter != "":
		return OutcomeRefused, errBackpressuredShed
	case status >= 400 && status < 500:
		return OutcomeRefused, nil
	default:
		return OutcomeAmbiguous, nil
	}
}

// errBackpressuredShed carries both sentinels of a backpressure 503: it is counted as
// backpressured, and it is a clean refusal like a shed.
var errBackpressuredShed = fmt.Errorf("%w: %w", ErrBackpressured, ErrShed)

// IdentityLedger records what became of every event the simulator offered the ingress.
// It is the one recorder fed from postEvent, the wire tail every emitter shares, and it
// answers two questions:
//
//   - for the load test's identity reconciliation: the identity (device token,
//     occurredTime in Unix microseconds) of every MEASUREMENT emit, filed by outcome. The
//     reconciliation reads base Measurement events, so only that type is keyed;
//   - for the live-state check: the latest ACCEPTED occurredTime per device, of any type,
//     because the live projection reflects every event type.
//
// It also counts every outcome across all types, so the reconciliation can check the
// ledger against the driver's own counters (Stats) and refuse a run where they disagree.
//
// A nil ledger records nothing and costs nothing: the persistent sim leaves it unset.
type IdentityLedger struct {
	mu      sync.Mutex
	devices map[string]*DeviceIdentities
	latest  map[string]time.Time
	totals  OutcomeTotals
	// unparsed counts stamps that did not parse back. The simulator formats them itself,
	// so a non-zero count is a harness defect and the ledger cannot say what was sent.
	unparsed int64
	// subMicro counts stamps carrying a part below the microsecond. The store keeps
	// microseconds, so such a stamp is not the identity that would be stored.
	subMicro int64
}

// DeviceIdentities is one device's Measurement emits, by outcome, each in Unix
// microseconds, in the order they were recorded.
type DeviceIdentities struct {
	Accepted, Refused, Ambiguous []int64
}

// OutcomeTotals counts emits of EVERY type by outcome.
type OutcomeTotals struct {
	Accepted, Refused, Ambiguous int64
}

// NewIdentityLedger returns an empty ledger.
func NewIdentityLedger() *IdentityLedger {
	return &IdentityLedger{devices: map[string]*DeviceIdentities{}, latest: map[string]time.Time{}}
}

// measurementEventType is the envelope's event-type name for a Measurement, the one
// type the identity reconciliation keys.
const measurementEventType = "Measurement"

// Record files one emit. eventType is the envelope's ("Measurement", "Location", ...);
// occurredTime is the exact string sent. Nil-safe.
func (l *IdentityLedger) Record(o EmitOutcome, deviceToken, eventType, occurredTime string) {
	if l == nil {
		return
	}
	at, err := time.Parse(time.RFC3339Nano, occurredTime)
	l.mu.Lock()
	defer l.mu.Unlock()
	switch o {
	case OutcomeAccepted:
		l.totals.Accepted++
	case OutcomeRefused:
		l.totals.Refused++
	case OutcomeAmbiguous:
		l.totals.Ambiguous++
	}
	if err != nil {
		l.unparsed++
		return
	}
	if at.Nanosecond()%1000 != 0 {
		l.subMicro++
	}
	if o == OutcomeAccepted {
		if prev, ok := l.latest[deviceToken]; !ok || at.After(prev) {
			l.latest[deviceToken] = at
		}
	}
	if eventType != measurementEventType {
		return
	}
	d := l.devices[deviceToken]
	if d == nil {
		d = &DeviceIdentities{}
		l.devices[deviceToken] = d
	}
	us := at.UnixMicro()
	switch o {
	case OutcomeAccepted:
		d.Accepted = append(d.Accepted, us)
	case OutcomeRefused:
		d.Refused = append(d.Refused, us)
	case OutcomeAmbiguous:
		d.Ambiguous = append(d.Ambiguous, us)
	}
}

// IdentitySnapshot is a deep copy of a ledger, safe to read after the drive.
type IdentitySnapshot struct {
	// Devices holds each device's Measurement identities by outcome.
	Devices map[string]DeviceIdentities
	// All counts emits of every type by outcome.
	All      OutcomeTotals
	Unparsed int64
	SubMicro int64
}

// Snapshot returns a deep copy of the ledger. A nil ledger snapshots as empty.
func (l *IdentityLedger) Snapshot() IdentitySnapshot {
	out := IdentitySnapshot{Devices: map[string]DeviceIdentities{}}
	if l == nil {
		return out
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, d := range l.devices {
		out.Devices[k] = DeviceIdentities{
			Accepted:  append([]int64(nil), d.Accepted...),
			Refused:   append([]int64(nil), d.Refused...),
			Ambiguous: append([]int64(nil), d.Ambiguous...),
		}
	}
	out.All = l.totals
	out.Unparsed = l.unparsed
	out.SubMicro = l.subMicro
	return out
}

// Measurements sums the Measurement identities across devices, by outcome.
func (s IdentitySnapshot) Measurements() OutcomeTotals {
	var t OutcomeTotals
	for _, d := range s.Devices {
		t.Accepted += int64(len(d.Accepted))
		t.Refused += int64(len(d.Refused))
		t.Ambiguous += int64(len(d.Ambiguous))
	}
	return t
}

// LatestAccepted returns the latest accepted occurredTime per device, of any event type,
// and how many stamps could not be parsed back (a ledger carrying one cannot say what was
// accepted, so the caller refuses it).
func (l *IdentityLedger) LatestAccepted() (map[string]time.Time, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]time.Time, len(l.latest))
	for k, v := range l.latest {
		out[k] = v
	}
	return out, l.unparsed
}
