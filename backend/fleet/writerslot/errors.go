// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package writerslot

import (
	"errors"
	"fmt"
)

var (
	// ErrDestroyNotImplemented is what acquiring a slot for a DESTROY run returns, in every build of
	// this slice. The kind is in the vocabulary so that the admission table is complete; executing
	// it is a typed refusal, never a plausible success. No unattended destroy can come out of here.
	ErrDestroyNotImplemented = errors.New("writerslot: DESTROY is not implemented: no destroy executes in this build")

	// ErrImportNotImplemented is what executing a recovery import returns until the runner exists.
	// The identity CHECK that decides whether an import is allowed IS implemented; running one is not.
	ErrImportNotImplemented = errors.New("writerslot: recovery import execution is not implemented in this build")

	// ErrLeaseLost: the manager does not hold a live lease with its own holder id and epoch. A
	// deposed manager's transaction fails here, in the database, not by convention.
	ErrLeaseLost = errors.New("writerslot: this manager does not hold the manager lease")

	// ErrLeaseHeld: another manager holds a live lease.
	ErrLeaseHeld = errors.New("writerslot: the manager lease is held by another manager")

	// ErrNotAdmitted wraps every admission refusal; the cause names which rule refused.
	ErrNotAdmitted = errors.New("writerslot: run not admitted")

	// ErrUnknownValue: a column held a value this build does not know. Every path that reads one
	// treats it as refusal, because a newer binary may have written it and an older one must not
	// guess what it means.
	ErrUnknownValue = errors.New("writerslot: unknown value")

	// ErrQuarantined is returned by a transition that quarantined the slot instead of completing.
	ErrQuarantined = errors.New("writerslot: the slot was quarantined")

	// ErrTransitionRefused: the requested transition is not permitted from the slot's state or on
	// the evidence given. The slot is unchanged.
	ErrTransitionRefused = errors.New("writerslot: transition refused")

	// ErrProofRefused: the exclusion proof was not sufficient. The slot is unchanged. There is no
	// waiver: leaving UNCERTAIN or QUARANTINED goes through proof checks, and only those.
	ErrProofRefused = errors.New("writerslot: exclusion proof refused")
)

// Refusal is an admission or transition refusal with a stable reason.
type Refusal struct {
	Err    error // ErrNotAdmitted, ErrTransitionRefused or ErrProofRefused
	Reason string
}

func (r *Refusal) Error() string { return fmt.Sprintf("%v: %s", r.Err, r.Reason) }
func (r *Refusal) Unwrap() error { return r.Err }

func admitRefused(format string, args ...any) error {
	return &Refusal{Err: ErrNotAdmitted, Reason: fmt.Sprintf(format, args...)}
}
func transitionRefused(format string, args ...any) error {
	return &Refusal{Err: ErrTransitionRefused, Reason: fmt.Sprintf(format, args...)}
}
func proofRefused(format string, args ...any) error {
	return &Refusal{Err: ErrProofRefused, Reason: fmt.Sprintf(format, args...)}
}
func unknown(column, value string) error {
	return fmt.Errorf("%w: %s = %q", ErrUnknownValue, column, value)
}
