// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package monitor is the ADR-064 live safety-continuous monitor: it subscribes to
// a BOUNDED cohort of probe devices' measurementStream over the real tenant wire
// (via core/graphqlws) and asserts "nothing bad ever happens" invariants
// CONTINUOUSLY during load — not at quiesce. A safety violation is instant and
// lag-independent, so it is caught the moment it occurs (research/load-test-harness.md
// §4.0). Whole-fleet completeness is covered separately and cheaply by the L1
// aggregate reconciliation; this monitor deliberately watches only a cohort so it
// never becomes the bottleneck — and, crucially, so it can always distinguish "I
// fell behind" from "the platform dropped it" (the graphqlws fail-closed contract).
//
// The two invariants here need no planted fixtures, so they run against any driven
// scenario:
//   - per-device occurred_time monotonicity — a device's events must not regress
//     in time. The streamed occurredTime is second-truncated, so ties are expected
//     and the check is non-strict; a STRICT regression is a real platform
//     reordering (transport preserves per-device ingest order, ADR-044).
//   - bounded-cohort membership/isolation — every event delivered on device X's
//     subscription must carry deviceToken==X. The server enforces this by
//     construction, so a violation is a genuine filter/isolation breach — the
//     passive tenant-isolation safety check at cohort granularity.
//
// The monitor is also fail-closed about its OWN blindness: a subscription that
// ends in anything but the monitor's clean Stop is a lost view, and a cohort
// device that delivered NOTHING over the whole run is a blind device — both fail,
// because a monitor that did not watch cannot certify that nothing bad happened.
package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/graphqlws"
)

// measurementProbeQuery scopes the subscription to one device server-side (the
// deviceToken filter), so each probe subscription is a bounded stream, not the
// firehose.
const measurementProbeQuery = `subscription LoadTestProbe($deviceToken: String!) {
  measurementStream(deviceToken: $deviceToken) {
    deviceToken
    occurredTime
    name
    value
  }
}`

// measurementStreamData is the `data` object of a next frame.
type measurementStreamData struct {
	MeasurementStream measurementEvent `json:"measurementStream"`
}

// measurementEvent is the streamed MeasurementEvent (event-management schema).
// occurredTime is a nullable RFC3339 (whole-second) string; value is nullable.
type measurementEvent struct {
	DeviceToken  string   `json:"deviceToken"`
	OccurredTime string   `json:"occurredTime"`
	Name         string   `json:"name"`
	Value        *float64 `json:"value"`
}

// Violation kinds.
const (
	// ViolMonotonicity: a device's occurredTime went strictly backwards.
	ViolMonotonicity = "monotonicity"
	// ViolMembership: an event for another device arrived on this device's
	// (server-filtered) subscription — an isolation/filter breach.
	ViolMembership = "membership"
	// ViolMalformed: a next payload could not be parsed, or carried an
	// unparseable/absent occurredTime.
	ViolMalformed = "malformed"
	// ViolLostView: a subscription ended in anything but the monitor's own clean
	// Stop — the monitor was BLIND for the rest of the run, so its silence proves
	// nothing (the fail-closed guard on the monitor itself).
	ViolLostView = "lost-view"
	// ViolBlindDevice: a cohort device delivered zero events over the whole run —
	// the monitor never actually watched it, so a clean verdict for it is a
	// verdict about nothing. Every driven scenario emits for every device every
	// tick, so a zero-delivery cohort member is never legitimate on a good drive.
	ViolBlindDevice = "blind-device"
)

// Violation is one observed safety breach.
type Violation struct {
	Kind        string `json:"kind"`
	DeviceToken string `json:"deviceToken"`
	Detail      string `json:"detail"`
}

// deviceChecker holds one subscription's per-device state and runs the invariants.
// It is touched only by that subscription's single watcher goroutine, so it needs
// no lock of its own.
type deviceChecker struct {
	token        string
	lastOccurred time.Time
	haveLast     bool
}

// check runs the per-event invariants and returns any violations. It is a pure
// function of (prior state, event) so it is exhaustively unit-testable.
func (d *deviceChecker) check(ev measurementEvent) []Violation {
	// Membership first: a foreign event is not this device's, so it must NOT touch
	// this device's monotonicity baseline (advancing lastOccurred with a leaked
	// event's stamp would misattribute the breach as widespread reordering). Return
	// immediately — the breach already fails the run.
	if ev.DeviceToken != d.token {
		return []Violation{{Kind: ViolMembership, DeviceToken: d.token,
			Detail: fmt.Sprintf("subscription for %s received an event for %s", d.token, ev.DeviceToken)}}
	}

	// Monotonicity: occurredTime is second-truncated in the stream, so equal
	// consecutive stamps are expected under load; only a STRICT regression is a
	// reordering. An empty/unparseable stamp is malformed (a measurement always
	// carries occurred_time — it is part of the natural key) and must not advance
	// the baseline either.
	t, err := time.Parse(time.RFC3339, ev.OccurredTime)
	if err != nil {
		return []Violation{{Kind: ViolMalformed, DeviceToken: d.token,
			Detail: fmt.Sprintf("unparseable occurredTime %q: %v", ev.OccurredTime, err)}}
	}
	var vs []Violation
	if d.haveLast && t.Before(d.lastOccurred) {
		vs = append(vs, Violation{Kind: ViolMonotonicity, DeviceToken: d.token,
			Detail: fmt.Sprintf("occurredTime regressed to %s (last %s)",
				t.Format(time.RFC3339), d.lastOccurred.Format(time.RFC3339))})
	}
	if !d.haveLast || t.After(d.lastOccurred) {
		d.lastOccurred = t
		d.haveLast = true
	}
	return vs
}

// Monitor watches a bounded cohort of probe devices for safety violations while
// load is applied. It is safe for concurrent use. Watch must complete before Stop
// (the only supported ordering: subscribe the cohort, apply load, then Stop).
type Monitor struct {
	client *graphqlws.Client

	mu         sync.Mutex
	violations []Violation
	observed   int
	byDevice   map[string]int // observed events per cohort token
	watched    []string       // the cohort, in subscribe order

	wg sync.WaitGroup
}

// New wraps an already-dialed graphqlws client (used by tests to point the
// monitor at a raw-ws test server). Production callers use Dial.
func New(client *graphqlws.Client) *Monitor {
	return &Monitor{client: client, byDevice: make(map[string]int)}
}

// Dial connects a graphqlws client to the event-management ws endpoint with the
// tenant access token from token, and returns a Monitor over it.
func Dial(ctx context.Context, wsEndpoint string, token graphqlws.TokenProvider, opts ...graphqlws.Option) (*Monitor, error) {
	client, err := graphqlws.Dial(ctx, wsEndpoint, token, opts...)
	if err != nil {
		return nil, fmt.Errorf("monitor: dial %s: %w", wsEndpoint, err)
	}
	return New(client), nil
}

// Watch subscribes to each token's measurementStream and starts a per-device
// watcher goroutine. It returns once every subscription frame is sent; a failed
// Subscribe aborts (the monitor cannot claim to watch a cohort it did not fully
// subscribe). NOTE: graphql-transport-ws has no subscribe-ack, so a returned nil
// only means the frame was sent — whether the server actually attached the
// stream is proven only by events arriving, which is exactly why a zero-delivery
// cohort device is failed as blind (see Stop/Passed). Watch must complete before
// Stop.
func (m *Monitor) Watch(ctx context.Context, tokens []string) error {
	for _, tok := range tokens {
		sub, err := m.client.Subscribe(ctx, measurementProbeQuery, map[string]interface{}{"deviceToken": tok})
		if err != nil {
			return fmt.Errorf("monitor: subscribe %s: %w", tok, err)
		}
		m.mu.Lock()
		m.watched = append(m.watched, tok)
		m.mu.Unlock()
		m.wg.Add(1)
		go m.watchOne(tok, sub)
	}
	return nil
}

// watchOne drains one subscription, checking every event, until the subscription
// ends. The only clean end is the monitor's own Stop (ErrClientClosed); any other
// terminal state is a lost view — the monitor went blind, and its silence for the
// rest of the run must not read as "nothing bad happened".
func (m *Monitor) watchOne(token string, sub *graphqlws.Subscription) {
	defer m.wg.Done()
	dc := &deviceChecker{token: token}
	for data := range sub.C() {
		var d measurementStreamData
		if err := json.Unmarshal(data, &d); err != nil {
			// A frame we cannot parse is malformed but was NOT a valid observation,
			// so it must not count toward coverage.
			m.record(Violation{Kind: ViolMalformed, DeviceToken: token,
				Detail: fmt.Sprintf("bad next payload: %v", err)})
			continue
		}
		m.recordEvent(token, dc.check(d.MeasurementStream))
	}
	if err := sub.Err(); !errors.Is(err, graphqlws.ErrClientClosed) {
		detail := "server completed the stream unexpectedly (an infinite subscription should not end on its own)"
		if err != nil {
			detail = err.Error()
		}
		m.record(Violation{Kind: ViolLostView, DeviceToken: token, Detail: detail})
	}
}

// recordEvent counts one observed event for token and appends any violations it
// produced.
func (m *Monitor) recordEvent(token string, vs []Violation) {
	m.mu.Lock()
	m.observed++
	m.byDevice[token]++
	m.violations = append(m.violations, vs...)
	m.mu.Unlock()
}

// record appends a violation not tied to a counted observation (a lost view, a
// blind device, or an unparseable payload).
func (m *Monitor) record(v Violation) {
	m.mu.Lock()
	m.violations = append(m.violations, v)
	m.mu.Unlock()
}

// finalize adds a blind-device violation for every cohort member that delivered
// zero events. Run once, after all watchers have drained.
func (m *Monitor) finalize() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, tok := range m.watched {
		if m.byDevice[tok] == 0 {
			m.violations = append(m.violations, Violation{Kind: ViolBlindDevice, DeviceToken: tok,
				Detail: "delivered zero events over the whole run — the monitor was blind to this cohort device"})
		}
	}
}

// Stop ends every subscription (by closing the client), waits for all watcher
// goroutines to drain, then finalizes per-device coverage — so the report is
// stable and complete on return.
func (m *Monitor) Stop() error {
	err := m.client.Close()
	m.wg.Wait()
	m.finalize()
	return err
}

// Report is the settled safety verdict.
type Report struct {
	Cohort     int            `json:"cohort"`
	Observed   int            `json:"observed"`
	ByDevice   map[string]int `json:"observedByDevice"`
	Violations []Violation    `json:"violations"`
}

// Report snapshots the current state; call after Stop for a settled result
// (blind-device violations are only added by Stop's finalize).
func (m *Monitor) Report() Report {
	m.mu.Lock()
	defer m.mu.Unlock()
	byDevice := make(map[string]int, len(m.byDevice))
	for k, v := range m.byDevice {
		byDevice[k] = v
	}
	return Report{
		Cohort:     len(m.watched),
		Observed:   m.observed,
		ByDevice:   byDevice,
		Violations: append([]Violation(nil), m.violations...),
	}
}

// Passed reports a clean safety run: at least one event was observed overall (the
// same "silence is not a pass" discipline as an empty invariant set) AND no
// violation was recorded — where a blind cohort device and a lost view are both
// violations, so per-device coverage is enforced, not just the aggregate.
func (r Report) Passed() bool {
	return r.Observed > 0 && len(r.Violations) == 0
}
