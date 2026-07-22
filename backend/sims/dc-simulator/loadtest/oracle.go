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

// MeasurementEventType is the base-event enum value for a Measurement
// (esmodel.EventType Measurement == 2). The oracle counts base Event rows of
// this type, not measurement_events rows: one device POST becomes exactly one
// base Event row regardless of how many metrics it carries, while it becomes one
// measurement_events row PER metric — so base events are the reconciliation unit
// that matches the driver's per-POST ledger (a 4-metric buildingpulse emit is
// one accepted event, one base row, four measurement rows).
//
// It is exported so the one other place that must agree on this enum — the
// self-test's out-of-band perturber, which deletes a row of this exact type —
// shares the single definition rather than a copy that could drift under a
// pre-GA enum renumber (a drift would delete the wrong type, leave the count
// unchanged, and silently corrupt a non-measurement row).
const MeasurementEventType = 2

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
			"eventTypes": []int{MeasurementEventType},
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
// count until it converges on the target (the accepted ledger) or the timeout
// elapses, then hand the observed count to Reconcile.
type Oracle struct {
	Counter eventCounter
	Poll    time.Duration
	Timeout time.Duration
	// Settle is how long Await keeps watching the count AFTER it first reaches the
	// target, to catch a late over-persist (see Await). 0 disables the settle phase
	// — Await exits on first-reach, the historic behaviour.
	Settle time.Duration
}

// QuiesceResult is what Await observed: the last-seen persisted count, whether it
// reached the target within the timeout, and how long it took.
type QuiesceResult struct {
	Persisted int64
	Reached   bool
	Elapsed   time.Duration
	Polls     int
}

// Await polls the persisted count over the window until it REACHES the target
// (the accepted count) or the timeout elapses. The count is monotone under a
// clean drive (append-only + dedup, no window contamination), so:
//   - reaching the target means every accepted event landed — but Await does NOT
//     stop there: it watches a bounded settle window for a late over-persist
//     (below), then returns the MAXIMUM count seen;
//   - a count below the target is NOT concluded as settled — it may still be
//     draining (post-Nak-fix redelivery is AckWait-paced, ~tens of seconds), so
//     Await keeps polling until the target is reached or the timeout is hit.
//
// That is deliberately more conservative than an "unchanged for N reads" plateau,
// which would flake a FAIL on a pipeline that was merely lagging and would have
// caught up. A timeout below the target is a real finding (drop, or lag that
// never drained) the caller turns into a failed invariant — never a silent pass.
//
// The settle re-count closes a false-PASS blind spot on the OTHER side. Without
// it, Await concluded the instant the count equalled the accepted target — so a
// PROMPT duplicate that over-persisted a moment after first-reach was never
// observed, and Reconcile saw persisted == accepted and passed while the true
// state carried an extra row (the no-duplication half of ingest-completeness /
// gold-zero-loss / shed-no-corruption). After first-reach Await keeps polling for
// o.Settle and returns the maximum count seen, so such an over-persist surfaces
// as persisted > accepted and Reconcile flags it. Reporting the max is
// fail-closed: it can only turn a pass into a fail, and a real drop never reaches
// the target so it never enters the settle phase.
//
// BOUNDED OBSERVATION: this catches a prompt over-persist, NOT the full
// redelivery-window class — a redelivery double-persist lands up to ackWait (60s)
// later, and that induced-redelivery exactly-once proof is the durability rig's
// job (ADR-030), not this settle (see DefaultQuiesceSettle). The same
// bounded-window caveat the detection/command settles carry.
//
// A Poll of 0 disables the inter-poll sleep (tight loop) — used by tests to run
// the loop deterministically without wall-clock waits. A Settle of 0 skips the
// settle phase entirely (exit on first-reach, the historic behaviour).
func (o *Oracle) Await(ctx context.Context, w Window, target int64) (QuiesceResult, error) {
	start := time.Now()
	deadline := start.Add(o.Timeout)
	polls := 0
	var last int64
	for {
		count, err := o.Counter.Count(ctx, w)
		if err != nil {
			// Fail closed: a read-back error is a run error, never treated as a
			// zero count (which would masquerade as "all events dropped").
			return QuiesceResult{Polls: polls, Elapsed: time.Since(start)}, err
		}
		polls++
		last = count
		if count >= target {
			maxSeen, settlePolls, serr := o.awaitSettle(ctx, w, count)
			polls += settlePolls
			if serr != nil {
				return QuiesceResult{Polls: polls, Elapsed: time.Since(start)}, serr
			}
			return QuiesceResult{Persisted: maxSeen, Reached: true, Elapsed: time.Since(start), Polls: polls}, nil
		}
		if time.Now().After(deadline) {
			return QuiesceResult{Persisted: last, Reached: false, Elapsed: time.Since(start), Polls: polls}, nil
		}
		if o.Poll > 0 {
			select {
			case <-ctx.Done():
				return QuiesceResult{Persisted: last, Elapsed: time.Since(start), Polls: polls}, ctx.Err()
			case <-time.After(o.Poll):
			}
		} else if ctx.Err() != nil {
			return QuiesceResult{Persisted: last, Elapsed: time.Since(start), Polls: polls}, ctx.Err()
		}
	}
}

// awaitSettle watches the persisted count for a bounded window (o.Settle) AFTER it
// first reached the target and returns the MAXIMUM count observed (>= reached),
// plus how many extra reads it made. It is the fail-closed catch for a late
// over-persist Await would otherwise miss (see Await). Settle <= 0 does no extra
// reads and returns the reached count unchanged.
//
// It always makes at least one confirming read when Settle > 0 (the deadline is
// checked AFTER a read, so a Settle shorter than one Poll still confirms once
// rather than degrading to a no-op), paces re-reads on o.Poll like the reach loop
// (Poll 0 tight-loops for tests), and fails closed on a read error just as the
// reach loop does — a read error during settle is a run error, never a silent
// "no overshoot".
func (o *Oracle) awaitSettle(ctx context.Context, w Window, reached int64) (maxSeen int64, polls int, err error) {
	maxSeen = reached
	if o.Settle <= 0 {
		return maxSeen, 0, nil
	}
	settleDeadline := time.Now().Add(o.Settle)
	for {
		if o.Poll > 0 {
			select {
			case <-ctx.Done():
				return maxSeen, polls, ctx.Err()
			case <-time.After(o.Poll):
			}
		} else if ctx.Err() != nil {
			return maxSeen, polls, ctx.Err()
		}
		count, err := o.Counter.Count(ctx, w)
		if err != nil {
			return maxSeen, polls, err
		}
		polls++
		if count > maxSeen {
			maxSeen = count
		}
		if time.Now().After(settleDeadline) {
			return maxSeen, polls, nil
		}
	}
}

// Invariant names — the machine-readable keys in the report.
const (
	InvLoadApplied  = "load-applied"
	InvCleanDrive   = "clean-drive"
	InvCompleteness = "ingest-completeness"
)

// Reconcile is the pure aggregate-reconciliation verdict: given the driver's
// accepted/failed ledger and the oracle's observed persisted count, it decides
// the L1 invariants. It is deliberately total and side-effect-free so every
// branch is unit-tested and mutation-verified.
//
// The three invariants, and why each exists:
//   - load-applied: accepted >= minAccepted. `> 0` alone stops the vacuous
//     0 == 0 pass, but a release GATE must also refuse to certify a trivial run:
//     if a CI job lost its load flags and drove 7 events, "correctness UNDER LOAD"
//     was never exercised, and a green there is false confidence. minAccepted is
//     the floor the run had to actually apply.
//   - clean-drive: failed == 0. The accepted ledger counts HTTP 202s; a failed
//     emit is either a definitive rejection (4xx/429 — never accepted, correctly
//     absent) OR a transport timeout/reset, where the server may have accepted and
//     PERSISTED the event anyway. That second class makes the ledger ambiguous in
//     the +direction, and a coincident real drop in the −direction could then net
//     to persisted == accepted — a false pass at the worst moment. L1 refuses to
//     reconcile a drive that was not clean; the shed-counter reconciliation that
//     admits governed 429s is a later slice (ADR-063 contention profile).
//   - ingest-completeness: persisted == accepted. persisted < accepted is the
//     at-most-once dropped-event class (ADR-030) — the failure this layer exists
//     to catch — OR lag that never drained within the timeout; either fails.
//     persisted > accepted is duplication or window contamination — also a fail,
//     never quietly tolerated.
func Reconcile(accepted, failed, persisted, minAccepted int64) []Invariant {
	loadApplied := accepted >= minAccepted
	cleanDrive := failed == 0
	inv := []Invariant{{
		Name:   InvLoadApplied,
		Passed: loadApplied,
		Detail: fmt.Sprintf("driver accepted %d events (floor %d — a gate must apply real load, not a trivial smoke)", accepted, minAccepted),
	}, {
		Name:   InvCleanDrive,
		Passed: cleanDrive,
		Detail: fmt.Sprintf("%d emits failed — L1 needs 0: a transport timeout may have persisted server-side, making the accepted ledger ambiguous (governed-shed reconciliation is a later slice)", failed),
	}}

	// Completeness is only meaningful once real load was applied AND the drive was
	// clean; otherwise report it as failed-because-inconclusive rather than letting
	// a coincidental equality read as a pass.
	comp := Invariant{Name: InvCompleteness}
	switch {
	case !loadApplied:
		comp.Passed = false
		comp.Detail = fmt.Sprintf("inconclusive: load floor not met (accepted %d < %d)", accepted, minAccepted)
	case !cleanDrive:
		comp.Passed = false
		comp.Detail = fmt.Sprintf("inconclusive: %d failed emits make the accepted ledger ambiguous", failed)
	case persisted == accepted:
		comp.Passed = true
		comp.Detail = fmt.Sprintf("persisted == accepted == %d (no events dropped)", accepted)
	case persisted < accepted:
		comp.Passed = false
		comp.Detail = fmt.Sprintf("DROPPED: persisted %d < accepted %d (%d events lost or never drained within the timeout — at-most-once hole, ADR-030)", persisted, accepted, accepted-persisted)
	default:
		comp.Passed = false
		comp.Detail = fmt.Sprintf("UNEXPECTED: persisted %d > accepted %d (%d extra — duplication or window contamination)", persisted, accepted, persisted-accepted)
	}
	return append(inv, comp)
}
