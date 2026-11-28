// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"errors"
	"fmt"
)

// ActivationServed is false: this build cannot act on an intent. It is a constant, so nothing at
// run time can change it, and no lifecycle reconciles as ACTIVE while it is false
// (TestActivationServedCannotBeToggled).
const ActivationServed = false

// Lifecycle is an intent's stored lifecycle.
type Lifecycle string

const (
	LifecycleDraft     Lifecycle = "DRAFT"
	LifecycleShadow    Lifecycle = "SHADOW"
	LifecycleActive    Lifecycle = "ACTIVE" // in the vocabulary; unreachable while ActivationServed is false
	LifecycleSuspended Lifecycle = "SUSPENDED"
	LifecycleRetired   Lifecycle = "RETIRED"
)

// Plan modes, as recorded on a plan row.
const (
	ModeShadow = "SHADOW"
	ModeActive = "ACTIVE"
)

// ErrActivationNotServed is returned for any request to treat an intent as ACTIVE.
var ErrActivationNotServed = errors.New("intent activation is not served by this build")

// PlanMode is the mode an evaluation of an intent in lifecycle l records, or an error when l is
// not reconciled. SHADOW records SHADOW. ACTIVE is refused while ActivationServed is false.
func PlanMode(l Lifecycle) (string, error) {
	switch l {
	case LifecycleShadow:
		return ModeShadow, nil
	case LifecycleActive:
		if !ActivationServed {
			return "", ErrActivationNotServed
		}
		return ModeActive, nil
	}
	return "", fmt.Errorf("an intent in lifecycle %q is not reconciled", l)
}

// Reconciled reports whether an intent in lifecycle l is evaluated by the reconciler.
func Reconciled(l Lifecycle) bool {
	_, err := PlanMode(l)
	return err == nil
}

// State is a would-be status. Every status this build writes is a SHADOW status: what the intent
// would be if it were acting.
type State string

const (
	StateSatisfied  State = "SATISFIED"
	StateConverging State = "CONVERGING"
	StatePartial    State = "PARTIAL"
	StateBlocked    State = "BLOCKED"
)

// Reserved states: in the vocabulary, never written by this build.
const (
	StateDegraded    State = "DEGRADED"
	StateOverridden  State = "OVERRIDDEN"
	StateHeld        State = "HELD"
	StateStuck       State = "STUCK"
	StateOscillating State = "OSCILLATING"
)

// Status codes.
const (
	StatusDependencyUnavailable = "DEPENDENCY_UNAVAILABLE"
	StatusObservationUnstable   = "OBSERVATION_UNSTABLE"
	StatusTargetRejectedPrefix  = "TARGET_REJECTED:"
	StatusCeilingExceeded       = "CEILING_EXCEEDED"
	StatusOwnerNotQualified     = "OWNER_NOT_QUALIFIED"
	StatusOwnerStandingUnknown  = "OWNER_STANDING_UNKNOWN"
	StatusEmptyTarget           = "EMPTY_TARGET"
	StatusKeyUndeclared         = string(CodeKeyUndeclared)
	StatusDeclaredTypeMismatch  = string(CodeDeclaredTypeMismatch)
)

// Standing is the answer to "could the owner run the bulk change this intent would create?".
// The zero value is StandingUnknown: no answer is never "qualifies".
type Standing int

const (
	StandingUnknown Standing = iota
	StandingQualifies
	StandingNotQualified
)

// Health is the evaluation's dependency health.
type Health string

const (
	HealthOK                    Health = ""
	HealthDependencyUnavailable Health = "DEPENDENCY_UNAVAILABLE"
	HealthObservationUnstable   Health = "OBSERVATION_UNSTABLE"
)

// PlanFacts are the facts of the plan the status rests on: the last recorded plan.
type PlanFacts struct {
	// TargetRejectedCode is device-management's refusal code for the target ("" when accepted).
	TargetRejectedCode string
	// CeilingExceeded is true when the target had more members than the ceiling.
	CeilingExceeded bool
	// TargetCount is the number of members observed.
	TargetCount int64
	// Counts is the diff's count by code. It must total TargetCount.
	Counts Counts
}

// ErrNoPlan is returned when a healthy evaluation whose owner qualifies asks for a status with no
// plan to rest it on.
var ErrNoPlan = errors.New("a healthy evaluation has no plan facts")

// Status is the would-be status of an intent, recomputed from this evaluation's answers on every
// evaluation, so a BLOCKED that no longer applies clears by itself. The first rule that applies
// wins:
//
//  1. health DEPENDENCY_UNAVAILABLE or OBSERVATION_UNSTABLE: BLOCKED with that code;
//  2. the target rejected: BLOCKED(TARGET_REJECTED:<code>);
//  3. over the ceiling: BLOCKED(CEILING_EXCEEDED);
//  4. standing unknown: BLOCKED(OWNER_STANDING_UNKNOWN);
//  5. standing not qualified: BLOCKED(OWNER_NOT_QUALIFIED);
//  6. an empty target: SATISFIED(EMPTY_TARGET);
//  7. no member can take the value: BLOCKED(KEY_UNDECLARED or DECLARED_TYPE_MISMATCH);
//  8. some members cannot: PARTIAL(KEY_UNDECLARED or DECLARED_TYPE_MISMATCH);
//  9. some members drifted: CONVERGING;
//  10. otherwise SATISFIED.
//
// Where both declaration codes apply, KEY_UNDECLARED is named, as it precedes in the diff. With
// no plan facts, rules 2 and 3 cannot be judged and rules 4 and 5 still are: an unknown or
// unqualified owner is BLOCKED with or without a plan, and only a qualified owner with no plan is
// ErrNoPlan.
func Status(facts *PlanFacts, standing Standing, health Health) (State, string, error) {
	switch health {
	case HealthDependencyUnavailable:
		return StateBlocked, StatusDependencyUnavailable, nil
	case HealthObservationUnstable:
		return StateBlocked, StatusObservationUnstable, nil
	case HealthOK:
	default:
		return "", "", fmt.Errorf("status: unknown health %q", health)
	}
	switch standing {
	case StandingUnknown, StandingQualifies, StandingNotQualified:
	default:
		return "", "", fmt.Errorf("status: unknown standing %d", standing)
	}
	if facts == nil {
		// An owner who does not qualify records no plan, so a first evaluation can have none.
		// Standing is still answerable: no plan never hides OWNER_STANDING_UNKNOWN.
		switch standing {
		case StandingUnknown:
			return StateBlocked, StatusOwnerStandingUnknown, nil
		case StandingNotQualified:
			return StateBlocked, StatusOwnerNotQualified, nil
		}
		return "", "", ErrNoPlan
	}
	if facts.TargetRejectedCode != "" {
		if !validStatusCode(facts.TargetRejectedCode) {
			return "", "", fmt.Errorf("status: target rejection code %q is malformed", facts.TargetRejectedCode)
		}
		return StateBlocked, StatusTargetRejectedPrefix + facts.TargetRejectedCode, nil
	}
	if facts.CeilingExceeded {
		return StateBlocked, StatusCeilingExceeded, nil
	}
	switch standing {
	case StandingUnknown:
		return StateBlocked, StatusOwnerStandingUnknown, nil
	case StandingNotQualified:
		return StateBlocked, StatusOwnerNotQualified, nil
	}
	c := facts.Counts
	if c.KeyUndeclared < 0 || c.DeclaredTypeMismatch < 0 || c.InSync < 0 || c.Drifted < 0 || c.Total() != facts.TargetCount {
		return "", "", fmt.Errorf("status: counts %+v do not total the target count %d", c, facts.TargetCount)
	}
	if facts.TargetCount == 0 {
		return StateSatisfied, StatusEmptyTarget, nil
	}
	if unfit := c.KeyUndeclared + c.DeclaredTypeMismatch; unfit > 0 {
		code := StatusDeclaredTypeMismatch
		if c.KeyUndeclared > 0 {
			code = StatusKeyUndeclared
		}
		if unfit == facts.TargetCount {
			return StateBlocked, code, nil
		}
		return StatePartial, code, nil
	}
	if c.Drifted > 0 {
		return StateConverging, "", nil
	}
	return StateSatisfied, "", nil
}

// validStatusCode accepts the upper-case codes device-management uses: A-Z, 0-9 and underscore.
func validStatusCode(code string) bool {
	if code == "" || len(code) > 48 {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if !('A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
