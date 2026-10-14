// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"fmt"
	"reflect"

	"gorm.io/gorm"
)

// CommandOutcome is the class of a FINISHED command: what physically happened, as far as the
// platform can know it. It is stored on the row by the write that terminates it.
//
// 🔴 STATUS CANNOT CARRY THIS, AND DERIVING IT FROM STATUS + ERROR PROSE WAS REJECTED. FAILED has
// several writers and spans three different facts — the command never left the platform, the
// device answered no, or the device may have acted and the platform lost the answer — and the only
// thing that told them apart was free text in `error`. A retry affordance built on that text is a
// guard on English sentences. The terminating write is the only code that KNOWS which fact it is
// recording, so it records it.
//
//   - SUCCEEDED     — the device reported success.
//   - NOT_DELIVERED — proven never to have reached the device: expired or cancelled while the
//     platform still held it, undeliverable, or its publish was abandoned. Safe to send again.
//   - REJECTED      — the device received the command and answered that it did not do it. A device
//     refusing is an answer, not a mystery, so a retry is a deliberate re-ask.
//   - UNKNOWN       — the command may have reached the device and acted: sent and never answered
//     (TIMEOUT), or answered and the answer lost. Sending again may act twice.
//
// A command that has not finished carries NO outcome (NULL). There is no fifth value for "not
// classified": a terminal row without one is a defect, which the BeforeUpdate guard below refuses
// to write and which readers treat as UNKNOWN (the conservative class).
type CommandOutcome string

const (
	OutcomeSucceeded    CommandOutcome = "SUCCEEDED"
	OutcomeNotDelivered CommandOutcome = "NOT_DELIVERED"
	OutcomeRejected     CommandOutcome = "REJECTED"
	OutcomeUnknown      CommandOutcome = "UNKNOWN"
)

// allOutcomes is every class, in the order a retry preview lists them.
var allOutcomes = []CommandOutcome{
	OutcomeSucceeded, OutcomeNotDelivered, OutcomeRejected, OutcomeUnknown,
}

// Valid reports whether the value is one of the four classes.
func (o CommandOutcome) Valid() bool {
	for _, known := range allOutcomes {
		if o == known {
			return true
		}
	}
	return false
}

// String returns the outcome as a string.
func (o CommandOutcome) String() string { return string(o) }

// permittedOutcomes is which classes each terminal status may carry. A status with one entry has
// an intrinsic class; FAILED has three because it is the one status that spans them, and its
// writer must say which.
var permittedOutcomes = map[CommandStatus][]CommandOutcome{
	CommandSuccessful: {OutcomeSucceeded},
	CommandExpired:    {OutcomeNotDelivered},
	CommandCancelled:  {OutcomeNotDelivered},
	CommandTimeout:    {OutcomeUnknown},
	CommandFailed:     {OutcomeNotDelivered, OutcomeRejected, OutcomeUnknown},
}

// intrinsicOutcome is the class of a terminal status that has exactly one, and false for FAILED
// (and anything non-terminal), whose writer must name it.
func intrinsicOutcome(status CommandStatus) (CommandOutcome, bool) {
	allowed := permittedOutcomes[status]
	if len(allowed) != 1 {
		return "", false
	}
	return allowed[0], true
}

// terminalUpdate builds the column map for a write that terminates a command. It is the ONE way
// the writers in this package spell a terminal write, so status and outcome cannot be set apart.
func terminalUpdate(status CommandStatus, outcome CommandOutcome, extra map[string]any) map[string]any {
	updates := make(map[string]any, len(extra)+2)
	for k, v := range extra {
		updates[k] = v
	}
	updates["status"] = status.String()
	updates["outcome"] = outcome.String()
	return updates
}

// ErrUnclassifiedTerminal is returned by the write guard when a write would terminate a command
// without a valid, matching outcome class.
var ErrUnclassifiedTerminal = errors.New("a command write that terminates it must record its outcome class")

// BeforeUpdate is the structural backstop for "every terminal write stamps an outcome": gorm
// calls it ahead of every update of a Command, and it refuses one that would move a row to a
// terminal status without a valid outcome that status permits (or stamps an outcome beside a
// non-terminal status). A new writer that forgets fails its first test, and in production fails
// loudly rather than leaving a finished command that no retry can classify.
//
// It inspects only map and struct destinations, which is every shape this package writes with. A
// write that names neither column (a nonce rotation, a hold) passes untouched.
func (c *Command) BeforeUpdate(tx *gorm.DB) error {
	var status, outcome any
	var haveStatus, haveOutcome bool
	switch dest := tx.Statement.Dest.(type) {
	case map[string]any:
		status, haveStatus = dest["status"]
		outcome, haveOutcome = dest["outcome"]
	case Command:
		status, haveStatus = dest.Status, dest.Status != ""
		outcome, haveOutcome = dest.Outcome.String, dest.Outcome.Valid
	case *Command:
		status, haveStatus = dest.Status, dest.Status != ""
		outcome, haveOutcome = dest.Outcome.String, dest.Outcome.Valid
	default:
		return nil
	}
	if !haveStatus {
		if haveOutcome {
			return fmt.Errorf("%w: an outcome was written without the status that terminates it", ErrUnclassifiedTerminal)
		}
		return nil
	}
	st := CommandStatus(fmt.Sprint(valueOf(status)))
	if !st.Terminal() {
		if haveOutcome {
			return fmt.Errorf("%w: outcome written beside non-terminal status %s", ErrUnclassifiedTerminal, st)
		}
		return nil
	}
	if !haveOutcome {
		return fmt.Errorf("%w: status %s written with no outcome", ErrUnclassifiedTerminal, st)
	}
	out := CommandOutcome(fmt.Sprint(valueOf(outcome)))
	for _, ok := range permittedOutcomes[st] {
		if out == ok {
			return nil
		}
	}
	return fmt.Errorf("%w: outcome %q is not permitted for status %s", ErrUnclassifiedTerminal, out, st)
}

// valueOf unwraps the driver-level wrappers (sql.NullString and pointers) a map value can carry.
func valueOf(v any) any {
	rv := reflect.ValueOf(v)
	for rv.IsValid() && rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return ""
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return ""
	}
	return rv.Interface()
}

// expiredOutcome is the class of the terminal the expiry sweep chose (EXPIRED or TIMEOUT), both
// of which have exactly one. An unexpected status yields an empty class, which the write guard
// refuses — the sweep can never stamp a guess.
func expiredOutcome(next string) CommandOutcome {
	out, _ := intrinsicOutcome(CommandStatus(next))
	return out
}

// Reach says how far a FAILED response got, as the reporting transport observed it. It exists
// because "the device did not answer" and "the device answered no" are the same Success=false on
// the wire and opposite facts about the world.
//
// The zero value is the ordinary case — the device spoke — so a response from a transport that
// does not send the field (every native MQTT device) means exactly what it always meant.
type Reach string

const (
	// ReachAnswered: the device answered. A failure is the device's own verdict (REJECTED).
	ReachAnswered Reach = ""
	// ReachUnanswered: the command was put on the wire and nothing came back — the same physical
	// fact the expiry sweep records as TIMEOUT, and recorded the same way (UNKNOWN).
	ReachUnanswered Reach = "UNANSWERED"
	// ReachUnsent: the reporting transport refused the command locally; it never reached the
	// wire (NOT_DELIVERED).
	ReachUnsent Reach = "UNSENT"
)

// Valid reports whether the reach is one this build understands.
func (r Reach) Valid() bool {
	return r == ReachAnswered || r == ReachUnanswered || r == ReachUnsent
}
