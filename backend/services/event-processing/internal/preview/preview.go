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
// A preview is an APPROXIMATION by design, in three honest ways it documents rather than hides: it
// starts COLD at the window (no state carried from before the window, so a mid-window duration hold
// that began earlier is not seen); it resolves NO device attributes (dynamic-threshold rules preview
// as non-firing — nil attr); and it processes events in delivered (publish) order with zero lateness.
package preview

import (
	"context"
	"errors"
	"io"
	"sort"
	"time"

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

// TimeRange is the preview's OCCURRED-time window [Start, End].
type TimeRange struct {
	Start time.Time
	End   time.Time
}

// Firing is one edge the draft rule would have emitted: a RAISE or a RESOLVE for a series at a time.
// It carries no triggering-event ids — per-event provenance is the slice-9e node-trace concern; this
// is the firing TIMELINE.
type Firing struct {
	OccurredAt time.Time
	Series     string
	Raise      bool // true = RAISE (rising edge), false = RESOLVE (falling edge)
}

// Stats are the preview's coverage counters.
type Stats struct {
	EventsScanned int
	FiringCount   int
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
// preview). maxScan bounds the work (0 ⇒ DefaultMaxScan).
func Run(ctx context.Context, opener ReplayOpener, suffix string, reg *runtime.RuleRegistry,
	tenant, profileVersion string, tr TimeRange, lateness time.Duration, maxScan int) (Result, error) {
	if maxScan <= 0 {
		maxScan = DefaultMaxScan
	}
	engine := core.NewEngine(reg.Cores(), lateness)

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
		res.Degraded = "history before the earliest retained event is unavailable; previewing from the earliest retained event"
	}

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
		occurred := runtime.EffectiveEventTime(event.OccurredTime, event.ProcessedTime, 0)
		// The reader started at the window's lower edge by PUBLISH time; filter to the OCCURRED-time
		// window (a late-published-but-early-occurred event, or one occurring past the window, is out).
		if occurred.Before(tr.Start) || occurred.After(tr.End) {
			continue
		}
		res.Stats.EventsScanned++
		if res.Stats.EventsScanned > maxScan {
			res.Degraded = "the window holds more events than the preview scan limit; the result is truncated"
			break
		}
		// nil attr: a preview resolves no device attributes, so a dynamic-threshold rule cleanly does
		// not fire (its presence-guarded comparison reads absent state as a non-match) — documented.
		plan := reg.Plan(msg.StreamSeq, tenant, event, occurred, nil)
		engine.ProcessResolved(msg.StreamSeq, occurred, plan.Events)
		res.Firings = appendFirings(res.Firings, engine.Drain(), tr)
	}

	// Advance the watermark to the window end to flush timer-driven edges (absence dead-man, a
	// duration hold that completed, an aggregate pane that closed) due within the window, then collect
	// the final batch. A cold-started engine only sees timers armed by in-window events, which is the
	// documented approximation.
	engine.Advance(tr.End)
	res.Firings = appendFirings(res.Firings, engine.Drain(), tr)

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

// appendFirings maps drained detections to firings within the window. A timer flush from
// Advance(End) is within the window by construction; a stray edge outside it is dropped.
func appendFirings(dst []Firing, dets []core.Detection, tr TimeRange) []Firing {
	for _, d := range dets {
		if d.At.Before(tr.Start) || d.At.After(tr.End) {
			continue
		}
		dst = append(dst, Firing{OccurredAt: d.At, Series: d.Series, Raise: d.Edge == core.EdgeRaised})
	}
	return dst
}
