// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package preview is the replay-driven rule preview (ADR-053 slice 9d): run a DRAFT detection
// rule against REPLAYED history in a THROWAWAY in-memory DETECT core and collect the RAISE /
// RESOLVE edges it WOULD have produced — the "what would this rule have done?" headline — without
// publishing anything or perturbing the live singleton.
//
// THE ISOLATION CONTRACT (the correctness landmine, spec §7.2). A preview shares NOTHING mutable
// with production:
//   - a FRESH detect.Engine (core.NewEngine binds no persistence — there is no snapshot store to
//     write, and this harness never calls Snapshot/Restore), so a preview cannot write a checkpoint;
//   - a FRESH RuleRegistry holding only the draft rule, so it cannot touch the live rule set;
//   - an EPHEMERAL replay consumer (NewReplayReaderFromTime → empty durable name, InactiveThreshold-
//     reaped) that acks only its own throwaway consumer, so it never advances or rebinds the
//     production durable;
//   - it COLLECTS detections off engine.Drain() rather than routing them through the Publisher, so
//     nothing is ever written to a tenant subject.
//
// The harness therefore takes no Store, no live engine, and no MessageWriter — the isolation is
// structural, not a runtime check.
//
// A preview is an APPROXIMATION by design, in honest ways it documents rather than hides: it starts
// COLD at the window (no state carried from before the window, so a mid-window duration hold that
// began earlier is not seen, and an aggregate pane straddling the window End never closes); it
// resolves NO device attributes (dynamic-threshold rules preview as non-firing — nil attr); it
// processes events in delivered (publish) order with zero lateness; and it does not apply the live
// future-skew clamp, so an event whose device clock ran ahead of its arrival is placed at its raw
// occurred time (and, since the replay reader starts by PUBLISH time, a future-skewed event published
// before the window Start can be missed).
package preview

import (
	"context"
	"errors"
	"io"
	"sort"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// DefaultMaxWindow is the GA cap on a preview's replay range (ADR-023 posture): a bounded window
// keeps a single preview's replay cost finite. The caller clamps a longer window to this and flags
// the result degraded.
const DefaultMaxWindow = 24 * time.Hour

// DefaultMaxScan bounds how many in-scope events one preview will process, so a preview over a busy
// tenant's window is a finite, self-limiting job rather than a self-inflicted DoS (ADR-023). Hitting
// it degrades (truncates) the result rather than running unbounded.
const DefaultMaxScan = 500_000

// DefaultMaxRead bounds how many DELIVERED messages a preview reads regardless of whether they are in
// scope — the ceiling that actually bounds the work (ADR-023). The replay reader delivers EVERY
// tenant's events over the window's publish-time span up to the stream head, so without this a
// narrow-profile preview over a busy multi-tenant instance would decode the whole stream tail while
// EventsScanned (in-scope only) stayed near zero and never tripped DefaultMaxScan. Hitting it degrades.
const DefaultMaxRead = 5_000_000

// TimeRange is the preview's OCCURRED-time window [Start, End].
type TimeRange struct {
	Start time.Time
	End   time.Time
}

// Firing is one edge the draft rule would have emitted: a RAISE or a RESOLVE for a series at a time.
// It carries the triggering scalar (Value/HasValue) so the slice-9e node trace can reconstruct — off
// the engine — how each REACT branch/action would have gated on this firing (the same value/series a
// guard sees); it carries no triggering-event id (a firing is a state edge, not always one event).
type Firing struct {
	OccurredAt time.Time
	Series     string
	Raise      bool // true = RAISE (rising edge), false = RESOLVE (falling edge)
	// Value is the scalar the edge carried (a threshold-crossing sample, a folded deltaRate/aggregate
	// value); HasValue is false for a value-less edge (a silence/absence fire, a resolve, a metric-less
	// leaf), matching runtime.DerivedEvent.Value nil-ness so a guard reads it identically.
	Value    float64
	HasValue bool
}

// Stats are the preview's coverage counters.
type Stats struct {
	EventsScanned int
	FiringCount   int
	// EvalErrors counts in-scope samples a rule's leaf could not evaluate (a raw-CEL leaf tripping the
	// runtime cost ceiling, or an unguarded metric read). A rule that errors on every event would
	// otherwise preview as a genuinely-quiet zero-firing rule; the count drives a degraded note so the
	// author sees the difference.
	EvalErrors int
}

// Result is a completed preview: the firing timeline, coverage stats, and a non-empty Degraded reason
// when the result is partial (history aged out below the window, or the scan cap was hit).
type Result struct {
	Firings  []Firing
	Stats    Stats
	Degraded string
}

// ReplayOpener is the subset of messaging.NatsManager the preview needs: a time-started ephemeral
// replay reader that also reports the stream's earliest retained time (for aged-out detection).
type ReplayOpener interface {
	NewReplayReaderFromTime(suffix string, startTime time.Time) (messaging.ReplayReader, time.Time, error)
}

// Run drives an ephemeral DETECT engine over the replayed resolved-event history for one tenant and
// profile version across an occurred-time window, collecting the RAISE/RESOLVE edges reg's rules would
// have produced. It never publishes, snapshots, or touches the live singleton (see the package doc).
//
// reg is a registry holding ONLY the draft rule(s), filed under (tenant, profileVersion). suffix is the
// resolved-events stream suffix. lateness is the engine's allowed lateness (0 for a deterministic
// preview). maxScan bounds the in-scope events processed and maxRead the TOTAL messages delivered
// (0 ⇒ the defaults); either cap degrades (truncates) rather than running unbounded.
//
// fences is the seam onto the FROZEN geofence snapshots (ADR-078) and may be nil when no source
// is wired. It is what makes a geofence preview CORRECT rather than vacuous — see the fenceSets
// resolution below, and note that it is deliberately NOT modelled on the nil-attr degradation.
func Run(ctx context.Context, opener ReplayOpener, suffix string, reg *runtime.RuleRegistry,
	tenant, profileVersion string, tr TimeRange, lateness time.Duration, maxScan, maxRead int,
	fences runtime.FenceSetSource) (Result, error) {
	if maxScan <= 0 {
		maxScan = DefaultMaxScan
	}
	if maxRead <= 0 {
		maxRead = DefaultMaxRead
	}
	engine := core.NewEngine(reg.Cores(), lateness)
	// The fence-set resolver for this preview: one memoizing reader over the frozen snapshots,
	// scoped to this tenant for its whole life, so no replayed event can reach another tenant's
	// fences however it is stamped.
	fenceSets := runtime.NewLoadingFenceSets(fences, tenant)

	reader, firstTime, err := opener.NewReplayReaderFromTime(suffix, tr.Start)
	if err != nil {
		return Result{}, err
	}
	defer reader.Close()

	var res Result
	// Aged-out: the window starts before the earliest retained event, so history is evicted below it.
	// JetStream clamps the start to the first retained message; report the truncation rather than a
	// misleadingly-empty preview (spec §7.3 — degrade, don't error/hang).
	if !firstTime.IsZero() && tr.Start.Before(firstTime) {
		addDegraded(&res.Degraded, "history before the earliest retained event is unavailable; previewing from the earliest retained event")
	}

	read := 0
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		msg, err := reader.Read(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Result{}, err
		}
		// Total-read bound (H1): the wildcard replay delivers every tenant's events over the window's
		// publish-time span, so the in-scope EventsScanned cap alone does not bound the work — cap the
		// raw delivered count too, before any per-message work, so a narrow-profile preview over a busy
		// instance cannot decode the whole stream tail.
		read++
		if read > maxRead {
			addDegraded(&res.Degraded, "the window spans more history than the preview read limit; the result is truncated")
			break
		}
		// The wildcard replay delivers every tenant's events; keep only this tenant's.
		subjectTenant, ok := messaging.ParseTenantFromSubject(msg.Subject)
		if !ok || subjectTenant != tenant {
			continue
		}
		event, derr := dmproto.UnmarshalResolvedEvent(msg.Value)
		if derr != nil {
			continue // a corrupt payload is skipped, never fails the whole preview
		}
		// Only events resolved against the previewed profile version feed the draft rule (the same
		// scope key the live fan-out uses).
		if event.ProfileVersionToken != profileVersion {
			continue
		}
		// Already bounded at resolution and stored that way, so replay reads the SAME instant
		// live detection read. It used to call the clamp with a tolerance of 0, which disables
		// it — so the preview did not bound a device's clock skew at all while the live loop
		// bounded it with the configured tolerance. Two consumers of one stream disagreeing
		// about when an event happened, under the exact feature (replay preview) whose whole
		// value is answering what the rule WOULD have done.
		occurred := event.OccurredTime
		// The reader started at the window's lower edge by PUBLISH time; filter to the OCCURRED-time
		// window (a late-published-but-early-occurred event, or one occurring past the window, is out).
		//
		// 🔑 THE FILTER WIDENS; THE INSTANT HANDED TO THE ENGINE DOES NOT. `occurred` above stays
		// the envelope and is what reg.Plan and ProcessResolved receive below, exactly as before —
		// the plan fans a batch out per entry on its own, so changing the parent instant here would
		// alter what the engine computes rather than merely which events reach it. This change is
		// about ADMISSION only.
		//
		// 🔴🔴 THE ADMISSION TEST IS ON THE EVENT'S WHOLE SPAN, NOT ON ITS ENVELOPE, AND A BATCH IS WHY.
		// An event's envelope carries ONE instant, but a batched event carries a sample's own time
		// per entry — so a store-and-forward tracker that buffers 10:00–10:30 readings and uploads
		// them at 11:05 has an envelope of 11:05 and samples entirely inside a 10:00–11:00 window.
		// Filtering on the envelope dropped that event WHOLE: EventsScanned stayed 0, the preview
		// reported the rule as never firing, and no degraded flag was raised. A preview whose entire
		// job is "what would this rule have done" answered "nothing" about samples it never looked
		// at, with no indication it had skipped them — the confident wrong answer this package's
		// doc comment lists as the one failure mode worse than a truncation notice.
		//
		// 🔑 THIS IS AN ADMISSION FIX ONLY, AND DELIBERATELY NOT A CLAIM ABOUT THE LIVE ENGINE.
		// Whether the samples then produce edges is the engine's business: it advances a watermark
		// from the instant it is handed, so a batch's entries can still be dropped as LATE, and
		// that behaviour is engine-wide and identical here and in production. Fixing it needs
		// per-series watermarks and is not attempted here. What this change guarantees is narrower
		// and worth having on its own: an event that overlaps the window REACHES the engine and is
		// counted, so a preview's silence is the engine's answer rather than an artefact of the
		// filter in front of it.
		//
		// occurredSpan returns the earliest and latest instant the event carries (the envelope
		// twice, for an event that batches nothing), so this stays exactly the old comparison for
		// an unbatched event and widens it to an overlap test for a batched one. Widening a filter
		// is only safe while it still EXCLUDES: an event whose whole span sits outside the window
		// is still dropped, which is what the second test below holds.
		first, last := occurredSpan(event)
		if last.Before(tr.Start) || first.After(tr.End) {
			continue
		}
		// Check the in-scope cap BEFORE counting/processing, so EventsScanned reports events actually
		// processed rather than one past the cap.
		if res.Stats.EventsScanned >= maxScan {
			addDegraded(&res.Degraded, "the window holds more in-scope events than the preview scan limit; the result is truncated")
			break
		}
		res.Stats.EventsScanned++
		// nil attr: a preview resolves no device attributes, so a dynamic-threshold rule cleanly does
		// not fire (its presence-guarded comparison reads absent state as a non-match) — documented.
		//
		// 🔴 FENCES DO NOT DEGRADE THE SAME WAY, AND THE ASYMMETRY IS THE POINT. attr degrades
		// because a device attribute is CURRENT state with no historical record: there is no way to
		// know what a threshold was last Tuesday, so the honest preview is "does not fire". A fence
		// set is different in kind — the event NAMES the version it was resolved against, and that
		// version's fences are frozen and still on record — so the exact set that was live is
		// recoverable and the preview can be CORRECT. Copying attr's degradation here would render
		// every geofence rule as never-firing, which for a preview whose entire job is "what would
		// this rule have done" is worse than useless: it is a confident wrong answer about the
		// headline feature.
		plan := reg.Plan(msg.StreamSeq, tenant, event, occurred, nil, fenceSets.Resolve(ctx, event.FenceSetVersion))
		res.Stats.EvalErrors += plan.EvalErrors
		engine.ProcessResolved(msg.StreamSeq, occurred, plan.Events)
		res.Firings = appendFirings(res.Firings, engine.Drain(), tr)
	}
	// A cancellation mid-replay surfaces as io.EOF from the reader; re-check so a cancelled preview
	// returns the error rather than a truncated result dressed up as complete (L1).
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	// Advance the watermark to the window end to flush timer-driven edges (absence dead-man, a
	// duration hold that completed, an aggregate pane that closed) due within the window, then collect
	// the final batch. A cold-started engine only sees timers armed by in-window events, which is the
	// documented approximation.
	engine.Advance(tr.End)
	res.Firings = appendFirings(res.Firings, engine.Drain(), tr)

	// A rule whose leaf errored on in-scope events would otherwise be indistinguishable from a quiet
	// rule; surface it (M1).
	if res.Stats.EvalErrors > 0 {
		addDegraded(&res.Degraded, "some events could not be evaluated (a rule predicate error); firings may be incomplete")
	}

	// Deterministic order: by time, then series, then raise-before-resolve at a shared instant.
	sort.Slice(res.Firings, func(i, j int) bool {
		a, b := res.Firings[i], res.Firings[j]
		if !a.OccurredAt.Equal(b.OccurredAt) {
			return a.OccurredAt.Before(b.OccurredAt)
		}
		if a.Series != b.Series {
			return a.Series < b.Series
		}
		return a.Raise && !b.Raise
	})
	res.Stats.FiringCount = len(res.Firings)
	return res, nil
}

// occurredSpan returns the earliest and latest instant an event carries: the span of its
// samples' own times, or the envelope's time twice for an event that batches nothing.
// Both bounds come from times the resolver already decided and bounded — nothing here
// re-derives one.
func occurredSpan(event *dmmodel.ResolvedEvent) (time.Time, time.Time) {
	first, last := event.OccurredTime, event.OccurredTime
	observe := func(at time.Time) {
		if at.Before(first) {
			first = at
		}
		if at.After(last) {
			last = at
		}
	}
	switch payload := event.Payload.(type) {
	case *dmmodel.ResolvedMeasurementsPayload:
		for _, entry := range payload.Entries {
			observe(entry.OccurredTime)
		}
	case *dmmodel.ResolvedLocationsPayload:
		for _, entry := range payload.Entries {
			observe(entry.OccurredTime)
		}
	case *dmmodel.ResolvedAlertsPayload:
		for _, entry := range payload.Entries {
			observe(entry.OccurredTime)
		}
	}
	return first, last
}

// addDegraded appends a reason to the degraded string, joining multiple reasons with "; " so a
// preview that is partial for more than one cause (aged out AND read-capped) reports all of them.
func addDegraded(dst *string, reason string) {
	if *dst == "" {
		*dst = reason
		return
	}
	*dst += "; " + reason
}

// appendFirings maps drained detections to firings within the window. A timer flush from
// Advance(End) is within the window by construction; a stray edge outside it is dropped.
func appendFirings(dst []Firing, dets []core.Detection, tr TimeRange) []Firing {
	for _, d := range dets {
		if d.At.Before(tr.Start) || d.At.After(tr.End) {
			continue
		}
		dst = append(dst, Firing{OccurredAt: d.At, Series: d.Series, Raise: d.Edge == core.EdgeRaised, Value: d.Value, HasValue: d.HasValue})
	}
	return dst
}
