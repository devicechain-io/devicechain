// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"errors"
	"testing"
)

func facts(c Counts) *PlanFacts { return &PlanFacts{TargetCount: c.Total(), Counts: c} }

func TestStatusEveryRule(t *testing.T) {
	healthy := facts(Counts{InSync: 3})
	for _, tc := range []struct {
		name     string
		facts    *PlanFacts
		standing Standing
		health   Health
		state    State
		code     string
	}{
		// Health first: a missing answer is never satisfied, whatever the last plan said.
		{"dependency unavailable over a satisfied plan", healthy, StandingQualifies, HealthDependencyUnavailable, StateBlocked, "DEPENDENCY_UNAVAILABLE"},
		{"dependency unavailable with no plan yet", nil, StandingUnknown, HealthDependencyUnavailable, StateBlocked, "DEPENDENCY_UNAVAILABLE"},
		{"observation unstable", healthy, StandingQualifies, HealthObservationUnstable, StateBlocked, "OBSERVATION_UNSTABLE"},

		// The target.
		{"target rejected", &PlanFacts{TargetRejectedCode: "GROUP_NOT_VERSIONED"}, StandingQualifies, HealthOK, StateBlocked, "TARGET_REJECTED:GROUP_NOT_VERSIONED"},
		{"target rejected outranks standing", &PlanFacts{TargetRejectedCode: "NOT_FOUND"}, StandingNotQualified, HealthOK, StateBlocked, "TARGET_REJECTED:NOT_FOUND"},
		{"ceiling exceeded", &PlanFacts{CeilingExceeded: true}, StandingQualifies, HealthOK, StateBlocked, "CEILING_EXCEEDED"},

		// Standing, on a fleet that is otherwise satisfied: no device change is needed to block.
		{"standing unknown", healthy, StandingUnknown, HealthOK, StateBlocked, "OWNER_STANDING_UNKNOWN"},
		{"owner not qualified", healthy, StandingNotQualified, HealthOK, StateBlocked, "OWNER_NOT_QUALIFIED"},
		{"owner reinstated", healthy, StandingQualifies, HealthOK, StateSatisfied, ""},

		// The diff.
		{"empty target", facts(Counts{}), StandingQualifies, HealthOK, StateSatisfied, "EMPTY_TARGET"},
		{"all undeclared", facts(Counts{KeyUndeclared: 2}), StandingQualifies, HealthOK, StateBlocked, "KEY_UNDECLARED"},
		{"all mismatched", facts(Counts{DeclaredTypeMismatch: 2}), StandingQualifies, HealthOK, StateBlocked, "DECLARED_TYPE_MISMATCH"},
		{"all unfit, both kinds", facts(Counts{KeyUndeclared: 1, DeclaredTypeMismatch: 1}), StandingQualifies, HealthOK, StateBlocked, "KEY_UNDECLARED"},
		{"some undeclared", facts(Counts{KeyUndeclared: 1, InSync: 1}), StandingQualifies, HealthOK, StatePartial, "KEY_UNDECLARED"},
		{"some mismatched", facts(Counts{DeclaredTypeMismatch: 1, Drifted: 1}), StandingQualifies, HealthOK, StatePartial, "DECLARED_TYPE_MISMATCH"},
		{"some of both", facts(Counts{KeyUndeclared: 1, DeclaredTypeMismatch: 1, InSync: 1}), StandingQualifies, HealthOK, StatePartial, "KEY_UNDECLARED"},
		{"drifted", facts(Counts{InSync: 5, Drifted: 1}), StandingQualifies, HealthOK, StateConverging, ""},
		{"satisfied", healthy, StandingQualifies, HealthOK, StateSatisfied, ""},
	} {
		state, code, err := Status(tc.facts, tc.standing, tc.health)
		if err != nil || state != tc.state || code != tc.code {
			t.Errorf("%s: Status = %s(%s), %v; want %s(%s)", tc.name, state, code, err, tc.state, tc.code)
		}
	}
}

// Every standing crossed with every health crossed with each plan shape gives a status from the
// written vocabulary, never a reserved one; and nothing but StandingQualifies with a healthy
// evaluation is ever SATISFIED or CONVERGING.
func TestStatusIsTotalAndFailsClosed(t *testing.T) {
	shapes := []*PlanFacts{
		facts(Counts{}), facts(Counts{InSync: 1}), facts(Counts{Drifted: 1}), facts(Counts{KeyUndeclared: 1}),
		facts(Counts{DeclaredTypeMismatch: 1, InSync: 1}), {TargetRejectedCode: "X"}, {CeilingExceeded: true},
	}
	written := map[State]bool{StateSatisfied: true, StateConverging: true, StatePartial: true, StateBlocked: true}
	for _, f := range shapes {
		for _, s := range []Standing{StandingUnknown, StandingQualifies, StandingNotQualified} {
			for _, h := range []Health{HealthOK, HealthDependencyUnavailable, HealthObservationUnstable} {
				state, code, err := Status(f, s, h)
				if err != nil {
					t.Fatalf("Status(%+v, %d, %q): %v", f, s, h, err)
				}
				if !written[state] {
					t.Errorf("Status(%+v, %d, %q) = %s, outside the written vocabulary", f, s, h, state)
				}
				if (state == StateSatisfied || state == StateConverging || state == StatePartial) && (s != StandingQualifies || h != HealthOK) {
					t.Errorf("Status(%+v, %d, %q) = %s(%s) without a qualified owner and a healthy evaluation", f, s, h, state, code)
				}
				if len(code) > 64 {
					t.Errorf("status code %q exceeds its 64-byte column", code)
				}
			}
		}
	}
}

func TestStatusRefusesWhatItCannotJudge(t *testing.T) {
	if _, _, err := Status(nil, StandingQualifies, HealthOK); !errors.Is(err, ErrNoPlan) {
		t.Errorf("a healthy evaluation with no plan = %v, want ErrNoPlan", err)
	}
	if _, _, err := Status(facts(Counts{InSync: 1}), StandingQualifies, Health("FINE")); err == nil {
		t.Error("Status accepted an unknown health")
	}
	if _, _, err := Status(facts(Counts{InSync: 1}), Standing(7), HealthOK); err == nil {
		t.Error("Status accepted an unknown standing")
	}
	if _, _, err := Status(&PlanFacts{TargetCount: 3, Counts: Counts{InSync: 2}}, StandingQualifies, HealthOK); err == nil {
		t.Error("Status accepted counts that do not total the target")
	}
	if _, _, err := Status(&PlanFacts{TargetCount: 0, Counts: Counts{InSync: 1, Drifted: -1}}, StandingQualifies, HealthOK); err == nil {
		t.Error("Status accepted a negative count")
	}
	for _, code := range []string{"lower", "A:B", "A B", "A_VERY_LONG_TARGET_REJECTION_CODE_OVER_FORTY_EIGHT"} {
		if _, _, err := Status(&PlanFacts{TargetRejectedCode: code}, StandingQualifies, HealthOK); err == nil {
			t.Errorf("Status accepted the target rejection code %q", code)
		}
	}
	var zero Standing
	if zero != StandingUnknown {
		t.Error("the zero Standing must be StandingUnknown: no answer is never qualifies")
	}
}
