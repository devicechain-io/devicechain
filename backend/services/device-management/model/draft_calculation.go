// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"slices"
	"strings"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/governance"
)

// Calculated metrics under a DRAFT (ADR-082, preview parity): what the rule preview uses to
// show a draft rule the values a draft profile's formulas would compute, over history that
// was resolved — and had its calculated values computed — under the version published at
// the time.
//
// The contract, precisely. A stored (replayed) resolved event already carries the calculated
// values of the version that resolved it, as Derived entries. Recalculate re-derives them
// under another resolution, the draft's:
//
//   - every Derived entry is removed, whatever its key: the draft's formulas are the whole
//     set, so a formula the draft deleted produces nothing, as it would once published;
//   - a raw entry under a key the draft declares CALCULATED is removed, exactly as the
//     resolver drops a device-sent value under a calculated key;
//   - every raw entry the draft declares MEASURED is fed to the draft's formulas, as its
//     stored value — which for a DOUBLE or an INT is the string the device sent, unchanged,
//     and for a BOOLEAN the "1"/"0" the resolver normalized it to, which parses to the same
//     boolean;
//   - the draft's formulas run through the SAME Calculation the resolver uses
//     (BeginCalculation, Set, Append), so their values, skips and formatting are the
//     resolver's by construction, and the entries are sorted by name as the resolver sorts
//     them.
//
// Per-device constants (`device.name` in a formula) are the one input a stored event does not
// carry. A recalculation takes them from its CALLER, which supplies the device's CURRENT
// constants (DeviceConstants over the device's metadata as it is now): a replayed reading is
// recomputed with today's calibration factor, not the one the device had when it was
// resolved, so it can differ from the value that was stored then — and from what an edit
// made after the replay window will compute. The stored value is not rewritten; only the
// preview shows it. A device whose constants are not supplied has none, and a formula
// reading one skips as missing_input.
//
// What it cannot reproduce, and does not pretend to: a value the version that resolved the
// event DROPPED (an undeclared non-numeric value, a device-sent value under a key that was
// calculated then) is not in the event, so a draft that would have kept it computes without
// it; and raw entries keep the classifier, unit and data type they were stamped with. Only
// calculated values are re-derived. When the draft's measured metrics equal the version's —
// the case of a draft that adds or edits formulas — the result equals what the resolver
// computes from the device's reading under the draft
// (TestRecalculateEqualsResolvingUnderTheDraft).

// CheckDraftCalculatedMetrics runs the publish gate's checks on a draft's metric
// definitions — compile, type, inputs measured only, Min/Max, the per-formula and
// per-profile cost ceilings, duplicate keys and the platform maximum — and returns one
// line per problem, or none. It reads no tenant ceiling: a preview stores nothing, so the
// tier's count limit is the publish's business, but the platform maximum still bounds how
// much one request compiles.
func CheckDraftCalculatedMetrics(defs []*MetricDefinition) []string {
	p := newProfileFormulas(defs)
	if n := len(p.calculated); n > governance.MaxCalculatedMetricsPerProfile {
		return []string{fmt.Sprintf("the draft has %d calculated metrics, above the maximum of %d",
			n, governance.MaxCalculatedMetricsPerProfile)}
	}
	return p.problems()
}

// Recalculate re-derives one stored sample entry's calculated values under r (see the
// contract above). It never writes to entries: it returns a new slice, and the entries it
// keeps are copied by value with their pointers shared, as the resolver's own copies are.
// obs is told every formula's outcome, as the resolver's observer is.
func (r ProfileResolution) Recalculate(entries []ResolvedMeasurementEntry, constants []calcfield.Constant,
	obs CalculationObserver) []ResolvedMeasurementEntry {
	out := make([]ResolvedMeasurementEntry, 0, len(entries)+r.CalculatedCount())
	calc := r.BeginCalculation(constants)
	defer calc.Release()
	for _, e := range entries {
		if e.Derived {
			continue
		}
		def, declared := r.Metric(e.Name)
		if declared && def.Calculated() {
			continue
		}
		if declared {
			calc.Set(&def, e.Value)
		}
		out = append(out, e)
	}
	out = calc.Append(out, obs)
	slices.SortFunc(out, func(a, b ResolvedMeasurementEntry) int { return strings.Compare(a.Name, b.Name) })
	return out
}
