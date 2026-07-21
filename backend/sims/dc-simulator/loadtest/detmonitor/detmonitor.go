// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package detmonitor is the ADR-064 L2d-1 detection-probe watcher: it subscribes
// to a device profile's detectionStream over the real tenant wire (via
// core/graphqlws) and tallies the rule-firing EDGES the DETECT engine publishes,
// keyed by series (the firing device's token). It is the observation half of the
// planted-detection oracle; the run orchestration (loadtest.RunDetectionProbes)
// owns the policy — which series are safety probes (must never raise) and which
// are edge probes (must raise+resolve exactly K times).
//
// Unlike the measurement monitor (one bounded subscription per cohort device),
// detectionStream is a SINGLE per-profile subscription: every device on the
// profile that crosses the threshold surfaces here, discriminated by the event's
// `series` field. So this watcher holds one stream and fans its events out into a
// per-series edge tally, rather than one stream per device.
//
// It is fail-closed about its OWN blindness, the same discipline the measurement
// monitor applies: the subscription must end ONLY via the watcher's clean Stop
// (graphqlws.ErrClientClosed). Any other terminal state — a drop, a slow-consumer
// overflow, an in-band error, or even a nil-error server `complete` on an
// infinite subscription — is a lost view: the watcher went blind, so its silence
// for the rest of the run cannot be read as "no detection fired". A frame it
// cannot parse is likewise a violation, never a silently-skipped observation.
package detmonitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/devicechain-io/dc-microservice/graphqlws"
)

// Edge wire tokens, mirroring event-processing's runtime.EdgeRaised/EdgeResolved
// (ADR-057 two-edge model). Mirrored as literals here — the sim subsystem only
// ever speaks the wire, never imports the event-processing Go packages — exactly
// like emit.go mirrors the credential-type and event-type strings.
const (
	EdgeRaised   = "raised"
	EdgeResolved = "resolved"
)

// detectionProbeQuery subscribes to one device profile's detection edges. The
// server scopes the stream to profileToken; every firing device on that profile
// arrives here with its own `series`, so the fan-out to per-series tallies happens
// client-side. Only the fields the probe assertions need are selected.
const detectionProbeQuery = `subscription LoadTestDetectionProbe($profileToken: String!) {
  detectionStream(profileToken: $profileToken) {
    ruleToken
    series
    edge
    occurredTime
  }
}`

// detectionStreamData is the `data` object of a next frame.
type detectionStreamData struct {
	DetectionStream detectionEvent `json:"detectionStream"`
}

// detectionEvent is the streamed DetectionEvent (event-processing schema). edge is
// "raised"/"resolved"; series is the firing device's token; ruleToken is the
// authored rule's token (all rules on the harness profile are ours, but it is
// carried so a caller can distinguish rules if a profile ever hosts several).
type detectionEvent struct {
	RuleToken    string `json:"ruleToken"`
	Series       string `json:"series"`
	Edge         string `json:"edge"`
	OccurredTime string `json:"occurredTime"`
}

// Violation kinds.
const (
	// ViolMalformed: a next payload could not be parsed, or carried an edge value
	// that is neither "raised" nor "resolved".
	ViolMalformed = "malformed"
	// ViolLostView: the subscription ended in anything but the watcher's own clean
	// Stop — the watcher was BLIND for the rest of the run, so its silence proves
	// nothing about what did or did not fire (the fail-closed guard on the watcher
	// itself, mirroring the measurement monitor's lost-view).
	ViolLostView = "lost-view"
)

// Violation is one observed watcher-integrity breach. Note that a spurious raise
// or a missing edge is NOT a Violation here — those are per-series COUNT judgments
// the run orchestration makes against its probe expectations. This type only
// carries breaches that invalidate the watcher's own testimony.
type Violation struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// SeriesTally is one device's edge counts over the run.
type SeriesTally struct {
	Raised   int `json:"raised"`
	Resolved int `json:"resolved"`
}

// Monitor watches one profile's detectionStream and tallies edges per series. It
// is safe for concurrent use. Watch must complete before Stop (the only supported
// ordering: subscribe, apply load, then Stop).
type Monitor struct {
	client *graphqlws.Client

	mu         sync.Mutex
	bySeries   map[string]*SeriesTally
	observed   int
	violations []Violation
	watching   bool // a Watch subscription is live (so lost-view is meaningful)

	wg sync.WaitGroup
}

// New wraps an already-dialed graphqlws client (used by tests to point the watcher
// at a raw-ws test server). Production callers use Dial.
func New(client *graphqlws.Client) *Monitor {
	return &Monitor{client: client, bySeries: make(map[string]*SeriesTally)}
}

// Dial connects a graphqlws client to the event-processing ws endpoint with the
// tenant access token from token, and returns a Monitor over it.
func Dial(ctx context.Context, wsEndpoint string, token graphqlws.TokenProvider, opts ...graphqlws.Option) (*Monitor, error) {
	client, err := graphqlws.Dial(ctx, wsEndpoint, token, opts...)
	if err != nil {
		return nil, fmt.Errorf("detmonitor: dial %s: %w", wsEndpoint, err)
	}
	return New(client), nil
}

// Watch subscribes to the profile's detectionStream and starts the single watcher
// goroutine. It returns once the subscription frame is sent; a failed Subscribe
// aborts. NOTE: graphql-transport-ws has no subscribe-ack, so a returned nil only
// means the frame was sent — whether the server actually attached the stream is
// proven only by events (or, for the run, by the edge probes reaching their
// expected K raises; a dead stream shows the edge probes at zero, which the
// orchestration fails as a liveness break). Watch must complete before Stop.
func (m *Monitor) Watch(ctx context.Context, profileToken string) error {
	sub, err := m.client.Subscribe(ctx, detectionProbeQuery, map[string]interface{}{"profileToken": profileToken})
	if err != nil {
		return fmt.Errorf("detmonitor: subscribe profile %s: %w", profileToken, err)
	}
	m.mu.Lock()
	m.watching = true
	m.mu.Unlock()
	m.wg.Add(1)
	go m.watchOne(sub)
	return nil
}

// watchOne drains the subscription, tallying every edge until the stream ends. The
// only clean end is the watcher's own Stop (ErrClientClosed); any other terminal
// state is a lost view.
func (m *Monitor) watchOne(sub *graphqlws.Subscription) {
	defer m.wg.Done()
	for data := range sub.C() {
		var d detectionStreamData
		if err := json.Unmarshal(data, &d); err != nil {
			m.record(Violation{Kind: ViolMalformed, Detail: fmt.Sprintf("bad next payload: %v", err)})
			continue
		}
		m.recordEdge(d.DetectionStream)
	}
	if err := sub.Err(); !errors.Is(err, graphqlws.ErrClientClosed) {
		detail := "server completed the detection stream unexpectedly (an infinite subscription should not end on its own)"
		if err != nil {
			detail = err.Error()
		}
		m.record(Violation{Kind: ViolLostView, Detail: detail})
	}
}

// recordEdge tallies one detection edge for its series. An edge value that is
// neither raised nor resolved is malformed (the wire contract is a closed set) and
// must NOT be counted as either — miscounting it would corrupt the exact-K
// judgment the run makes against this tally.
func (m *Monitor) recordEdge(ev detectionEvent) {
	if ev.Edge != EdgeRaised && ev.Edge != EdgeResolved {
		m.record(Violation{Kind: ViolMalformed,
			Detail: fmt.Sprintf("series %q: unknown edge %q (expected %q or %q)", ev.Series, ev.Edge, EdgeRaised, EdgeResolved)})
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observed++
	t := m.bySeries[ev.Series]
	if t == nil {
		t = &SeriesTally{}
		m.bySeries[ev.Series] = t
	}
	if ev.Edge == EdgeRaised {
		t.Raised++
	} else {
		t.Resolved++
	}
}

// record appends a watcher-integrity violation (a lost view or an unparseable /
// unknown-edge payload).
func (m *Monitor) record(v Violation) {
	m.mu.Lock()
	m.violations = append(m.violations, v)
	m.mu.Unlock()
}

// Tally returns the current edge counts for a series (a zero SeriesTally if the
// series has produced no edge). Safe to call after Stop for a settled result.
func (m *Monitor) Tally(series string) SeriesTally {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t := m.bySeries[series]; t != nil {
		return *t
	}
	return SeriesTally{}
}

// Stop ends the subscription (by closing the client) and waits for the watcher
// goroutine to drain, so the report is stable and complete on return.
func (m *Monitor) Stop() error {
	err := m.client.Close()
	m.wg.Wait()
	return err
}

// Report is the settled detection-watch verdict: the per-series edge tallies plus
// any watcher-integrity violation. The run's PASS/FAIL over the probe expectations
// is computed by the orchestration, not here — this is the raw, honest tally.
type Report struct {
	Observed   int                     `json:"observed"`
	BySeries   map[string]*SeriesTally `json:"bySeries"`
	Violations []Violation             `json:"violations"`
}

// Report snapshots the current state; call after Stop for a settled result.
func (m *Monitor) Report() Report {
	m.mu.Lock()
	defer m.mu.Unlock()
	bySeries := make(map[string]*SeriesTally, len(m.bySeries))
	for k, v := range m.bySeries {
		cp := *v
		bySeries[k] = &cp
	}
	return Report{
		Observed:   m.observed,
		BySeries:   bySeries,
		Violations: append([]Violation(nil), m.violations...),
	}
}

// Intact reports whether the watcher's own testimony is trustworthy: it actually
// subscribed and never lost the view or saw a malformed frame. It says NOTHING
// about whether the right edges fired — that is the orchestration's per-series
// judgment. A run must treat a non-Intact watcher as an indeterminate/failed
// detection verdict, never as "no detections, therefore safe".
func (m *Monitor) Intact() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.watching && len(m.violations) == 0
}
