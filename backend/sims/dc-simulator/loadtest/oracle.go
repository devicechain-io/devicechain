// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// measurementEventType is the base-event enum value for a Measurement
// (esmodel.EventType Measurement == 2). The oracle counts base Event rows of
// this type, not measurement_events rows: one device POST becomes exactly one
// base Event row regardless of how many metrics it carries, while it becomes one
// measurement_events row PER metric — so base events are the reconciliation unit
// that matches the driver's per-POST ledger (a 4-metric buildingpulse emit is
// one accepted event, one base row, four measurement rows).
const measurementEventType = 2

// Window bounds the reconciliation query by occurred_time — the emit instant the
// driver controls (set client-side in EmitMeasurements), NOT processed_time — so
// persistence lag never moves an event out of the window while it drains. The
// bounds are padded (see deriveWindow) to absorb modest clock skew between the
// driver host and the DB, and the second granularity of the RFC3339 bound strings
// the query itself sends (occurred_time is stored sub-second).
type Window struct {
	Start time.Time
	End   time.Time
}

// eventCounter reads the number of persisted base events matching a window from
// platform truth. Abstracted so the quiesce/reconcile logic is unit-testable
// against a scripted counter with no cluster.
type eventCounter interface {
	Count(ctx context.Context, w Window) (int64, error)
}

// graphqlEventCounter counts base Measurement events over the real tenant-scoped
// event-management GraphQL API — the fidelity rule (research/load-test-harness.md
// §3): the oracle reads the same resolved/persisted truth a real client would,
// with no admin/backdoor access. pagination.totalRecords is a true COUNT(*) over
// the (tenant, eventType, occurred_time-window) filter, independent of pageSize.
type graphqlEventCounter struct {
	session  *userclient.TenantSession
	endpoint string
}

const countEventsQuery = `query LoadTestCount($c: EventSearchCriteria!) {
  events(criteria: $c) { pagination { totalRecords } }
}`

func (g *graphqlEventCounter) Count(ctx context.Context, w Window) (int64, error) {
	vars := map[string]any{
		"c": map[string]any{
			"pageNumber": 1,
			"pageSize":   1,
			"eventTypes": []int{measurementEventType},
			"startTime":  w.Start.UTC().Format(time.RFC3339),
			"endTime":    w.End.UTC().Format(time.RFC3339),
		},
	}
	var out struct {
		Events struct {
			Pagination struct {
				// Pointer so a null totalRecords is a fail-closed error, not a
				// silent zero the reconcile would read as "everything dropped".
				TotalRecords *int64 `json:"totalRecords"`
			} `json:"pagination"`
		} `json:"events"`
	}
	if err := g.session.Query(ctx, g.endpoint, countEventsQuery, vars, &out); err != nil {
		return 0, fmt.Errorf("count events: %w", err)
	}
	if out.Events.Pagination.TotalRecords == nil {
		return 0, fmt.Errorf("count events: server returned a null totalRecords")
	}
	return *out.Events.Pagination.TotalRecords, nil
}

// httpGraphQLFromWS derives event-management's HTTP GraphQL endpoint from the
// handshake's graphql-ws endpoint. core/graphql deliberately serves the HTTP
// POST and the WS upgrade on ONE shared path (only the scheme differs), so the
// query endpoint is the ws endpoint with ws->http / wss->https. Deriving it here
// keeps the oracle self-contained rather than adding a field to the dcctl<->sim
// handshake wire shape.
func httpGraphQLFromWS(wsURL string) (string, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return "", fmt.Errorf("parse event ws endpoint %q: %w", wsURL, err)
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	case "http", "https":
		// Already an HTTP endpoint; use as-is.
	default:
		return "", fmt.Errorf("event ws endpoint %q has unexpected scheme %q", wsURL, u.Scheme)
	}
	return u.String(), nil
}

// deriveWindow builds the reconciliation window from the drive's start/end,
// padded so neither the second granularity of the RFC3339 bound strings the query
// sends nor modest clock skew between the driver host and the DB can drop a
// boundary event. A fresh sim tenant has no prior events, so the pad is only a
// safety margin, never a source of contamination.
func deriveWindow(start, end time.Time) Window {
	const pad = 5 * time.Second
	return Window{Start: start.Add(-pad), End: end.Add(pad)}
}

// Oracle owns the aggregate-reconciliation strategy: poll the windowed persisted
// count until it settles (quiesce), then hand the settled count to Reconcile.
type Oracle struct {
	Counter eventCounter
	Poll    time.Duration
	Stable  int
	Timeout time.Duration
}

// QuiesceResult is what Await observed: the settled (or last-seen) persisted
// count, whether it actually settled within the timeout, and how long it took.
type QuiesceResult struct {
	Persisted int64
	Settled   bool
	Elapsed   time.Duration
	Polls     int
}

// Await polls the persisted count over the window until it has been unchanged
// for Stable consecutive reads (quiesced) or Timeout elapses. It returns the
// last-seen count either way; a timeout with the count still moving is a
// "lag never settled" finding the caller turns into a failed invariant, not a
// silent pass.
//
// A Poll of 0 disables the inter-poll sleep (tight loop) — used by tests to
// exercise the settle logic deterministically without wall-clock waits.
func (o *Oracle) Await(ctx context.Context, w Window) (QuiesceResult, error) {
	start := time.Now()
	deadline := start.Add(o.Timeout)
	var st plateau
	polls := 0
	for {
		count, err := o.Counter.Count(ctx, w)
		if err != nil {
			// Fail closed: a read-back error is a run error, never treated as a
			// zero count (which would masquerade as "all events dropped").
			return QuiesceResult{Polls: polls, Elapsed: time.Since(start)}, err
		}
		polls++
		settled := st.observe(count, o.Stable)
		if settled {
			return QuiesceResult{Persisted: count, Settled: true, Elapsed: time.Since(start), Polls: polls}, nil
		}
		if time.Now().After(deadline) {
			return QuiesceResult{Persisted: count, Settled: false, Elapsed: time.Since(start), Polls: polls}, nil
		}
		if o.Poll > 0 {
			select {
			case <-ctx.Done():
				return QuiesceResult{Persisted: count, Elapsed: time.Since(start), Polls: polls}, ctx.Err()
			case <-time.After(o.Poll):
			}
		} else if ctx.Err() != nil {
			return QuiesceResult{Persisted: count, Elapsed: time.Since(start), Polls: polls}, ctx.Err()
		}
	}
}

// plateau tracks how many consecutive reads have held the same value — the pure
// settle-detection core of Await, separated so it is trivially testable.
type plateau struct {
	last   int64
	streak int
	primed bool
}

// observe records one reading and reports whether the value has now been stable
// for stableTarget consecutive reads. A changed value resets the streak.
func (p *plateau) observe(v int64, stableTarget int) bool {
	if p.primed && v == p.last {
		p.streak++
	} else {
		p.streak = 1
		p.last = v
		p.primed = true
	}
	return p.streak >= stableTarget
}

// Invariant names — the machine-readable keys in the report.
const (
	InvLoadApplied  = "load-applied"
	InvQuiesced     = "pipeline-quiesced"
	InvCompleteness = "ingest-completeness"
)

// Reconcile is the pure aggregate-reconciliation verdict: given the driver's
// accepted-event ledger and the oracle's settled persisted count, it decides the
// L1 invariants. It is deliberately total and side-effect-free so every branch
// is unit-tested and mutation-verified.
//
// The three invariants, and why each exists:
//   - load-applied: accepted > 0. Without it the whole reconcile is vacuous —
//     0 persisted == 0 accepted would "pass" a run that emitted nothing (a broken
//     driver, a dead cluster). This is the guard that stops the check that
//     cannot fail. (verify-the-thing-that-matters)
//   - pipeline-quiesced: the count settled within the timeout. If it never
//     settled, the completeness comparison is against a still-moving number and
//     means nothing; an unsettled pipeline is itself a scale failure.
//   - ingest-completeness: persisted == accepted. persisted < accepted is the
//     at-most-once dropped-event class (ADR-030) — the failure this layer
//     exists to catch. persisted > accepted is duplication or window
//     contamination — also a failure, never quietly tolerated.
func Reconcile(accepted, persisted int64, settled bool) []Invariant {
	loadApplied := accepted > 0
	inv := []Invariant{{
		Name:   InvLoadApplied,
		Passed: loadApplied,
		Detail: fmt.Sprintf("driver accepted %d events (must be > 0 or the run is vacuous)", accepted),
	}, {
		Name:   InvQuiesced,
		Passed: settled,
		Detail: quiesceDetail(settled, persisted),
	}}

	// Completeness is only meaningful once load was applied AND the pipeline
	// settled; otherwise report it as failed-because-inconclusive rather than
	// letting a coincidental equality read as a pass.
	comp := Invariant{Name: InvCompleteness}
	switch {
	case !loadApplied:
		comp.Passed = false
		comp.Detail = "inconclusive: no load was applied"
	case !settled:
		comp.Passed = false
		comp.Detail = fmt.Sprintf("inconclusive: pipeline never quiesced (persisted still moving at %d, accepted %d)", persisted, accepted)
	case persisted == accepted:
		comp.Passed = true
		comp.Detail = fmt.Sprintf("persisted == accepted == %d (no events dropped)", accepted)
	case persisted < accepted:
		comp.Passed = false
		comp.Detail = fmt.Sprintf("DROPPED: persisted %d < accepted %d (%d events lost — at-most-once hole, ADR-030)", persisted, accepted, accepted-persisted)
	default:
		comp.Passed = false
		comp.Detail = fmt.Sprintf("UNEXPECTED: persisted %d > accepted %d (%d extra — duplication or window contamination)", persisted, accepted, persisted-accepted)
	}
	return append(inv, comp)
}

func quiesceDetail(settled bool, persisted int64) string {
	if settled {
		return fmt.Sprintf("persisted count settled at %d", persisted)
	}
	return fmt.Sprintf("pipeline did not quiesce within the timeout (last count %d, still moving)", persisted)
}
