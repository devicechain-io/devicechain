// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package runtime is DETECT's live fan-out: it turns each resolved event into the
// per-rule core events the keyed-streaming engine consumes, and turns the engine's
// detections into subscribe-able derived events (ADR-051 slice 4). It is the seam that
// binds the slice-3 compiler (rules.CompiledRule) to the slice-2 engine (core.Engine) on
// the live, multi-tenant resolved stream — honoring the metric-scoped feed contract the
// compiler exposes and the per-tenant subject isolation the backstop enforces.
package runtime

import (
	"math"
	"strconv"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/detect/predicate"
)

// BuildInputs projects a resolved event onto one predicate.Input PER measurement sample it
// carries. A resolved measurement event may batch several samples (a store-and-forward device
// uploading buffered readings as one message); each outer entry is one sample — a coherent set
// of named readings — so it becomes one Input, and a rule evaluates against every sample rather
// than only the last. Folding a batch to a single map (last-value-wins) would silently drop the
// intermediate readings: a `temp > 100` threshold would miss a `[120, 80]` batch, and aggregates
// would undercount. Every sample is stamped at `occurred` (the message's clamped event time),
// matching event-management, which persists every reading but flattens the per-entry sub-times to
// the message time — so DETECT and the persisted history (and ADR-053 replay authoring) agree.
//
// A non-measurement event (location/relationship/alert) or an empty batch yields a single
// heartbeat Input with nil M: it carries no metric (matching no metric-gated rule) but is still a
// heartbeat for an absence rule. The common one-sample event yields exactly one Input.
func BuildInputs(ev *dmmodel.ResolvedEvent, occurred time.Time) []predicate.Input {
	anchors := make(map[string]string, len(ev.Anchors))
	for _, a := range ev.Anchors {
		if _, seen := anchors[a.AnchorType]; !seen {
			anchors[a.AnchorType] = a.AnchorToken
		}
	}
	base := predicate.Input{Device: ev.SourceDeviceToken, Anchors: anchors, Occurred: occurred}

	p, ok := ev.Payload.(*dmmodel.ResolvedMeasurementsPayload)
	if !ok || len(p.Entries) == 0 {
		return []predicate.Input{base} // heartbeat: nil M
	}
	out := make([]predicate.Input, 0, len(p.Entries))
	for _, entry := range p.Entries {
		in := base
		in.M = sampleMetrics(entry)
		out = append(out, in)
	}
	return out
}

// sampleMetrics folds one sample's numeric readings into an M map. ADR-016: measurements are
// numeric by design, so a non-numeric value is not a metric and is skipped rather than surfaced
// as a spurious 0; a non-finite reading is likewise skipped so a NaN/±Inf can never fold into
// serializable aggregate state (the core guards this too — defense in depth). A duplicate name
// within a single sample keeps the last reading (a degenerate case; distinct samples are separate
// Inputs).
func sampleMetrics(entry dmmodel.ResolvedMeasurementsEntry) map[string]float64 {
	if len(entry.Entries) == 0 {
		return nil
	}
	m := make(map[string]float64, len(entry.Entries))
	for _, mx := range entry.Entries {
		v, err := strconv.ParseFloat(mx.Value, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		m[mx.Name] = v
	}
	return m
}

// EffectiveEventTime bounds a device-reported occurred time against the server-stamped
// processed time plus a tolerance, so a single device with a wildly-future clock (or a
// spoofed timestamp) cannot advance the engine's shared watermark far into the future and
// fire EVERY tenant's absence/duration/session timers at once (the shared frontier is
// monotonic and snapshotted, so the poisoning would persist). The processed time is stamped
// by the server at ingest and travels immutably in the payload, so the clamp is deterministic
// under replay. Only future skew is bounded — a past (late/out-of-order) reading is left to
// the watermark's normal bounded-lateness handling. Disabled (returns occurred unchanged) when
// the processed time is unset or maxSkew is non-positive.
func EffectiveEventTime(occurred, processed time.Time, maxSkew time.Duration) time.Time {
	if processed.IsZero() || maxSkew <= 0 {
		return occurred
	}
	if limit := processed.Add(maxSkew); occurred.After(limit) {
		return limit
	}
	return occurred
}
