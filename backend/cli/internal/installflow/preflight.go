// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"context"
	"fmt"
)

// KindPreflightObservation is one row of a preflight report.
const KindPreflightObservation Kind = "preflight.observation"

// ObservationKind says what a preflight observation is: the start of a group of
// checks, one check's verdict, or the report's closing tally.
type ObservationKind string

const (
	ObserveSection ObservationKind = "section"
	ObserveCheck   ObservationKind = "check"
	ObserveSummary ObservationKind = "summary"
)

func (k ObservationKind) valid() bool {
	return oneOf(k, ObserveSection, ObserveCheck, ObserveSummary)
}

// CheckVerdict is one check's verdict. It is a word on purpose: a presenter
// must be able to show every state without colour.
type CheckVerdict string

const (
	CheckPass CheckVerdict = "pass"
	// CheckInfo is a fact the doctor reports without judging it.
	CheckInfo CheckVerdict = "info"
	CheckWarn CheckVerdict = "warn"
	CheckFail CheckVerdict = "fail"
	// The three below are reserved for the presenters that show them; today's
	// doctor never reaches a verdict it cannot name, so it emits none of them.
	CheckUnknown    CheckVerdict = "unknown"
	CheckNotNeeded  CheckVerdict = "not_needed"
	CheckNotChecked CheckVerdict = "not_checked"
)

func (v CheckVerdict) valid() bool {
	return oneOf(v, CheckPass, CheckInfo, CheckWarn, CheckFail, CheckUnknown, CheckNotNeeded, CheckNotChecked)
}

// PreflightObservation is one typed row of the preflight doctor's report. The
// doctor's verdicts and thresholds live in the doctor; this carries what it
// decided, never how a presenter draws it.
//
// A section observation names its group by code (Section); a check carries a
// verdict, the text the doctor wrote, and a fix for a warning or a failure; a
// summary carries the counts the report closes with.
type PreflightObservation struct {
	Observation ObservationKind `json:"observation"`
	Section     MessageCode     `json:"section,omitempty"`
	Verdict     CheckVerdict    `json:"verdict,omitempty"`
	Text        SafeText        `json:"text,omitempty"`
	// Highlight is a value the doctor calls out, shown after Text and drawn
	// emphasised where the presenter can.
	Highlight SafeText `json:"highlight,omitempty"`
	Fix       SafeText `json:"fix,omitempty"`
	Fails     int      `json:"fails,omitempty"`
	Warns     int      `json:"warns,omitempty"`
}

func (PreflightObservation) Kind() Kind { return KindPreflightObservation }

func (p PreflightObservation) validate(r *redactor) error {
	if err := checkEnum("observation", p.Observation); err != nil {
		return err
	}
	switch p.Observation {
	case ObserveSection:
		if err := checkID(r, "section", string(p.Section)); err != nil {
			return err
		}
	case ObserveCheck:
		if err := checkEnum("verdict", p.Verdict); err != nil {
			return err
		}
	case ObserveSummary:
		if p.Fails < 0 || p.Warns < 0 {
			return fmt.Errorf("%w: negative preflight count", ErrInvalidEvent)
		}
	}
	return nil
}

func (p PreflightObservation) scrub(r *redactor) Payload {
	p.Text = r.text(p.Text)
	p.Highlight = r.text(p.Highlight)
	p.Fix = r.text(p.Fix)
	return p
}

// PreflightResult is the preflight's verdict: how many checks failed and how
// many warned. Any failure stops the run before it touches a cluster.
type PreflightResult struct {
	Fails int
	Warns int
}

// PreflightRunner runs the read-only preflight checks. It is injected into the
// engine by the command layer, which owns the doctor, so the engine needs no
// knowledge of how a host is inspected. observe is called once per
// observation, in order, from the calling goroutine, as the checks run.
type PreflightRunner interface {
	RunPreflight(ctx context.Context, observe func(PreflightObservation)) (PreflightResult, error)
}

// PreflightFunc adapts a function to PreflightRunner.
type PreflightFunc func(ctx context.Context, observe func(PreflightObservation)) (PreflightResult, error)

func (f PreflightFunc) RunPreflight(ctx context.Context, observe func(PreflightObservation)) (PreflightResult, error) {
	return f(ctx, observe)
}
