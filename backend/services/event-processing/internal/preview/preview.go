// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package preview is the replay-driven rule preview (ADR-053 slice 9d): run a DRAFT detection
// rule against REPLAYED history in a THROWAWAY in-memory DETECT core and collect the RAISE /
// RESOLVE edges it WOULD have produced â€” the "what would this rule have done?" headline â€” without
// publishing anything or perturbing the live singleton.
//
// THE ISOLATION CONTRACT (the correctness landmine, spec Â§7.2). A preview shares NOTHING mutable
// with production:
//   - a FRESH detect.Engine (core.NewEngine binds no persistence â€” there is no snapshot store to
//     write, and this harness never calls Snapshot/Restore), so a preview cannot write a checkpoint;
//   - a FRESH RuleRegistry holding only the draft rule, so it cannot touch the live rule set;
//   - a replay read BY SEQUENCE (NewReplayReaderFromTime: batched direct gets, no consumer created
//     and nothing acknowledged), so it never advances or rebinds the production durable and holds
//     no right over any consumer on the stream;
//   - it COLLECTS detections off engine.Drain() rather than routing them through the Publisher, so
//     nothing is ever written to a tenant subject.
//
// The harness therefore takes no Store, no live engine, and no MessageWriter â€” the isolation is
// structural, not a runtime check.
//
// DRAFT CALCULATED METRICS (ADR-082, preview parity). A replayed event carries the calculated
// values the version that resolved it computed, so a preview of the published profile reads them
// as stored and re-evaluates nothing. A preview of a DRAFT profile passes the draft's resolution
// as `draft`, and each replayed measurement event is handed to the engine with its calculated
// values re-derived under the draft's formulas by dmmodel.ProfileResolution.Recalculate, which
// runs the resolver's own Calculation: every stored calculated value is replaced, a formula the
// draft deleted produces nothing, and one it added or edited produces what it computes from the
// event's stored raw values â€” which are the device's own strings, so the value is the one the
// resolver would store under the draft, not an approximation of it. The re-derived event is a
// COPY built for the engine and dropped with it: the replayed message, its decoded event and the
// stream are never written, and the draft reaches no live engine, store or subject â€” it lives in
// this call's arguments and nowhere else (TestDraftCalculationLeaksNowhere).
//
// PER-DEVICE CONSTANTS (a formula's `device.name`) are the one input a replayed event does not
// carry, and event-processing holds no device records. The caller reads the CURRENT constants of
// the profile's devices (ConstantSource, from device-management's metadata) and hands them in with
// WithDeviceConstants; each replayed event is recalculated with the constants of the device that
// sent it. So a preview shows what the draft would compute NOW, with each device as it is NOW:
// a reading stored under an older calibration factor previews under today's, and may differ from
// the value that was stored then. A device the source did not list has none, and a formula that
// reads a constant skips on its readings as missing_input.
//
// A preview is an APPROXIMATION by design, in honest ways it documents rather than hides: it starts
// COLD at the window (no state carried from before the window, so a mid-window duration hold that
// began earlier is not seen, and an aggregate pane straddling the window End never closes); it
// resolves NO device attributes (nil attr: every device previews as having none, so a structured
// dynamic threshold previews as non-firing, while a raw-CEL fallback such as
// `!("k" in attr) && m["t"] > 80.0` previews its FALLBACK on every device, including devices that do
// have the attribute); it processes events in delivered (publish) order with zero lateness; and it does not apply the live
// future-skew clamp, so an event whose device clock ran ahead of its arrival is placed at its raw
// occurred time (and, since the replay reader starts by PUBLISH time, a future-skewed event published
// before the window Start can be missed).
package preview

import (
	"context"
	"fmt"
	"sort"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/replayinput"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

// DefaultMaxWindow is the GA cap on a preview's replay range (ADR-023 posture): a bounded window
// keeps a single preview's replay cost finite. The caller clamps a longer window to this and flags
// the result degraded.
const DefaultMaxWindow = 24 * time.Hour

// DefaultMaxScan bounds how many in-scope events one preview will process, so a preview over a busy
// tenant's window is a finite, self-limiting job rather than a self-inflicted DoS (ADR-023). Hitting
// it degrades (truncates) the result rather than running unbounded.
const DefaultMaxScan = replayinput.DefaultMaxScan

// DefaultMaxRead bounds how many DELIVERED messages a preview reads regardless of whether they are in
// scope â€” the ceiling that actually bounds the work (ADR-023). The replay reader delivers EVERY
// tenant's events over the window's publish-time span up to the stream head, so without this a
// narrow-profile preview over a busy multi-tenant instance would decode the whole stream tail while
// EventsScanned (in-scope only) stayed near zero and never tripped DefaultMaxScan. Hitting it degrades.
const DefaultMaxRead = replayinput.DefaultMaxRead

// TimeRange is the preview's OCCURRED-time window [Start, End].
type TimeRange struct {
	Start time.Time
	End   time.Time
}

// Firing is one edge the draft rule would have emitted: a RAISE or a RESOLVE for a series at a time.
// It carries the triggering scalar (Value/HasValue) so the slice-9e node trace can reconstruct â€” off
// the engine â€” how each REACT branch/action would have gated on this firing (the same value/series a
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
	// RecalculatedSamples counts the sample entries whose calculated values were re-derived
	// under the draft (zero when no draft was given), and CalculatedSkips the draft formula
	// results that produced no value for a sample (a missing input, a range skip, ...), so a
	// draft formula that never produces anything is visible rather than read as a quiet rule.
	RecalculatedSamples int
	CalculatedSkips     int
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

// ReplayOpener is the subset of messaging.NatsManager the preview needs: a time-started replay
// reader (a read by sequence) that also reports the stream's earliest retained time (for aged-out detection).
type ReplayOpener interface {
	NewReplayReaderFromTime(suffix string, startTime time.Time) (messaging.ReplayReader, time.Time, error)
}

// MaxConstantDevices bounds how many of a profile's devices a preview reads constants for. A
// profile with more devices previews the first MaxConstantDevices (by device id) with their
// constants and the rest without, and says so: reading every device of a large fleet to
// preview a window of its history is not a bounded job.
const MaxConstantDevices = 10_000

// ConstantSource reads the current per-device constants of a profile's devices: for each device
// that holds at least one of keys, its constants as device-management's resolver would read them
// from the device's metadata. It scans at most maxDevices devices and reports truncated when the
// profile has more. The tenant rides the source's own credentials (a service token on the
// request's tenant), never an argument.
type ConstantSource interface {
	DeviceConstants(ctx context.Context, tenant, profileToken string, keys []string, maxDevices int) (
		byDevice map[string][]calcfield.Constant, truncated bool, err error)
}

// Option adjusts a Run.
type Option func(*options)

type options struct {
	constants    map[string][]calcfield.Constant
	appliedGuard bool
}

// WithAppliedGuard says whether the instance runs live DETECT's duplicate-event guard, which is
// the case exactly when event envelopes are on (main.go: cfg.AppliedGuard). The preview must apply
// the rule live applies: with envelopes off live builds no guard and counts a retransmitted
// duplicate, so the preview counts it too. The default is OFF, live's default.
func WithAppliedGuard(enabled bool) Option {
	return func(o *options) { o.appliedGuard = enabled }
}

// WithDeviceConstants gives the replay each device's current constants, by device token, for the
// draft's formulas that read `device.name`. The map is read, never written, and may be nil.
func WithDeviceConstants(byDevice map[string][]calcfield.Constant) Option {
	return func(o *options) { o.constants = byDevice }
}

// Run drives an ephemeral DETECT engine over the replayed resolved-event history for one tenant and
// profile version across an occurred-time window, collecting the RAISE/RESOLVE edges reg's rules would
// have produced. It never publishes, snapshots, or touches the live singleton (see the package doc).
//
// reg is a registry holding ONLY the draft rule(s), filed under (tenant, profileVersion). suffix is the
// resolved-events stream suffix. lateness is the engine's allowed lateness (0 for a deterministic
// preview). maxScan bounds the in-scope events processed and maxRead the TOTAL messages delivered
// (0 â‡’ the defaults); either cap degrades (truncates) rather than running unbounded.
//
// fences is the seam onto the FROZEN geofence snapshots (ADR-078) and may be nil when no source
// is wired. It is what makes a geofence preview CORRECT rather than vacuous â€” see the fenceSets
// resolution below, and note that it is deliberately NOT modelled on the nil-attr degradation.
//
// draft, when non-nil, is a draft profile's resolution whose calculated metrics replace the
// stored ones on every replayed measurement event (see the package doc); nil previews the
// values as stored.
func Run(ctx context.Context, opener ReplayOpener, suffix string, reg *runtime.RuleRegistry,
	tenant, profileVersion string, tr TimeRange, lateness time.Duration, maxScan, maxRead int,
	fences runtime.FenceSetSource, draft *dmmodel.ProfileResolution, opts ...Option) (Result, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
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
	// misleadingly-empty preview (spec Â§7.3 â€” degrade, don't error/hang).
	if !firstTime.IsZero() && tr.Start.Before(firstTime) {
		addDegraded(&res.Degraded, "history before the earliest retained event is unavailable; previewing from the earliest retained event")
	}

	counts, stop, err := replayinput.Scan(ctx, reader, replayinput.Admission{
		Tenant: tenant, AdmitVersion: func(v string) bool { return v == profileVersion },
		Span: replayinput.TimeRange{Start: tr.Start, End: tr.End}, MaxScan: maxScan, MaxRead: maxRead, AppliedGuard: o.appliedGuard,
	}, func(msg messaging.Message, event *resolvedevent.ResolvedEvent, enveloped bool) {
		occurred := event.OccurredTime
		if draft != nil {
			event = recalculated(event, *draft, o.constants[event.SourceDeviceToken], &res.Stats)
		}
		// nil attr: a preview resolves no device attributes, so every device previews as having none.
		// A structured dynamic threshold therefore does not fire (its positive presence guard reads
		// absent state as a non-match), while a raw-CEL leaf written with a fallback for a missing
		// attribute previews that fallback on EVERY device, including devices that do have the
		// attribute live â€” documented.
		//
		// ðŸ”´ FENCES DO NOT DEGRADE THE SAME WAY, AND THE ASYMMETRY IS THE POINT. attr degrades
		// because a device attribute is CURRENT state with no historical record: there is no way to
		// know what a threshold was last Tuesday, so the honest preview is "as if unset". A fence
		// set is different in kind â€” the event NAMES the version it was resolved against, and that
		// version's fences are frozen and still on record â€” so the exact set that was live is
		// recoverable and the preview can be CORRECT. Copying attr's degradation here would render
		// every geofence rule as never-firing, which for a preview whose entire job is "what would
		// this rule have done" is worse than useless: it is a confident wrong answer about the
		// headline feature.
		plan := reg.Plan(msg.StreamSeq, tenant, event, occurred, nil, fenceSets.Resolve(ctx, event.FenceSetVersion))
		res.Stats.EvalErrors += plan.EvalErrors
		if enveloped {
			engine.ProcessReceivedMember(msg.StreamSeq, occurred, runtime.ReceiptOf(event, msg.AppendTime), plan.Events)
		} else {
			engine.ProcessReceived(msg.StreamSeq, occurred, runtime.ReceiptOf(event, msg.AppendTime), plan.Events)
		}
		res.Firings = appendFirings(res.Firings, engine.Drain(), tr)
	})
	if err != nil {
		return Result{}, err
	}
	res.Stats.EventsScanned = counts.Scanned
	switch stop {
	case replayinput.StopReadCap:
		addDegraded(&res.Degraded, "the window spans more history than the preview read limit; the result is truncated")
	case replayinput.StopScanCap:
		addDegraded(&res.Degraded, "the window holds more in-scope events than the preview scan limit; the result is truncated")
	case replayinput.StopDeadline:
		addDegraded(&res.Degraded, "the preview ran out of time before the whole window was replayed; the result is truncated")
	}

	if ctx.Err() == context.DeadlineExceeded && stop != replayinput.StopDeadline {
		addDegraded(&res.Degraded, "the preview ran out of time before the whole window was replayed; the result is truncated")
	}

	// Advance the watermark to the window end to flush timer-driven edges (absence dead-man, a
	// duration hold that completed, an aggregate pane that closed) due within the window, then collect
	// the final batch. A cold-started engine only sees timers armed by in-window events, which is the
	// documented approximation.
	//
	// The advance goes one nanosecond PAST End, not to it. The window is [Start, End], so a reading
	// at End is in it, and the rate and count-window kinds decide a reading only once the frontier is
	// strictly past it: advancing to End exactly would replay that reading and never decide it. A
	// timer or pane due in (End, End+1ns] stamps its edge after End, which appendFirings drops, so
	// nothing outside the window gets in.
	engine.Advance(tr.End.Add(time.Nanosecond))
	res.Firings = appendFirings(res.Firings, engine.Drain(), tr)

	// Readings the engine declined as late (a sliding window that had already passed them, a
	// duration reading further behind the frontier than its hold, or a threshold reading older than
	// an alarm's newest, or a redelivered threshold match behind the frontier) reached the engine and were not
	// used. Unreported, the preview would answer "the rule would not have fired" about readings it
	// never looked at â€” the confident wrong answer this package exists to avoid. The live engine
	// declines the same readings, but counts them on a metric; here the count is the only trace.
	if n := engine.DrainLateSamples(); n > 0 {
		noun := "readings"
		if n == 1 {
			noun = "reading"
		}
		addDegraded(&res.Degraded, fmt.Sprintf("%d %s arrived too far behind the other readings to be used by the rule; firings may be incomplete", n, noun))
	}

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

// recalculated returns event with its calculated values re-derived under draft, as a new event
// value sharing nothing writable with the one decoded off the stream: the payload and every
// sample's entry slice are new. A non-measurement event is returned as it is â€” no formula
// reads a location or an alert.
func recalculated(event *resolvedevent.ResolvedEvent, draft dmmodel.ProfileResolution, constants []calcfield.Constant,
	stats *Stats) *resolvedevent.ResolvedEvent {
	payload, ok := event.Payload.(*resolvedevent.ResolvedMeasurementsPayload)
	if !ok {
		return event
	}
	// A draft with no formulas changes only an event that carries a stored calculated value
	// (it removes it); any other event is handed on as it is, and not counted.
	if draft.CalculatedCount() == 0 && !carriesDerived(payload) {
		return event
	}
	obs := skipCounter{stats: stats}
	samples := make([]resolvedevent.ResolvedMeasurementsEntry, len(payload.Entries))
	for i, sample := range payload.Entries {
		samples[i] = resolvedevent.ResolvedMeasurementsEntry{
			Entries:      draft.Recalculate(sample.Entries, constants, obs),
			OccurredTime: sample.OccurredTime,
		}
	}
	stats.RecalculatedSamples += len(samples)
	cp := *event
	cp.Payload = &resolvedevent.ResolvedMeasurementsPayload{Entries: samples}
	return &cp
}

// carriesDerived reports whether any sample of payload holds a calculated value.
func carriesDerived(payload *resolvedevent.ResolvedMeasurementsPayload) bool {
	for _, sample := range payload.Entries {
		for _, e := range sample.Entries {
			if e.Derived {
				return true
			}
		}
	}
	return false
}

// skipCounter counts the draft formula results that produced no value.
type skipCounter struct{ stats *Stats }

func (skipCounter) Computed(string) {}

func (c skipCounter) Skipped(string, calcfield.SkipReason, error) { c.stats.CalculatedSkips++ }

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
