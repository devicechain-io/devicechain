// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/detect/predicate"
)

// BuildEvent turns one resolved-event view into the core Event this rule feeds to the
// engine: it evaluates the leaf predicate to set Match, reads the value metric (guaranteed
// present when Match is true for a value-consuming kind, by the predicate's presence
// guard) to set Value, and assembles the SeriesKey from the caller-resolved series token
// (the device token for every kind except correlation, where it is the anchor token) plus
// the member token (the contributing device, for correlation; ignored otherwise).
//
// This is the seam the runtime fan-out (slice 4) drives, and the harness the slice-3 tests
// use to prove each rule type compiles to a config that fires correctly in the real core.
// An evaluation error (e.g. a raw-CEL leaf tripping the runtime cost limit) is surfaced;
// the caller counts it and treats the event as a non-match rather than feeding a bad Match.
func (cr *CompiledRule) BuildEvent(seq uint64, series, member string, in predicate.Input) (core.Event, error) {
	match, err := cr.Predicate.Eval(in)
	if err != nil {
		return core.Event{}, err
	}
	// The scalar this event carries: the value metric for a value-consuming kind (deltaRate,
	// non-count aggregate), else the GATE metric for a structured threshold/repeating leaf — the
	// crossing sample the comparison tested, which is what a raiseAlarm action stamps on the alarm.
	// The metric-scoped feed gate (fanout) guarantees the chosen metric is present, so read via the
	// comma-ok form and only claim HasValue when it truly is: a metric-less shape (raw-CEL leaf,
	// count aggregate, absence, correlation) then carries no value rather than a fabricated 0.
	var value float64
	var hasValue bool
	switch {
	case cr.ValueMetric != "":
		value, hasValue = in.M[cr.ValueMetric]
	case cr.GateMetric != "":
		value, hasValue = in.M[cr.GateMetric]
	}
	return core.Event{
		Seq:      seq,
		Key:      core.SeriesKey{Rule: cr.ID, Series: series},
		Time:     in.Occurred,
		Value:    value,
		HasValue: hasValue,
		Match:    match,
		Member:   member,
	}, nil
}
