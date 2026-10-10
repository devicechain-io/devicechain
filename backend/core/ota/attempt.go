// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ota

import (
	"errors"
	"time"
)

var (
	// ErrBeyondRecall is returned by Cancel once installation has begun (UPDATING onward, or UNKNOWN).
	ErrBeyondRecall = errors.New("ota: update is beyond recall")
	// ErrTerminal is returned by Cancel on an attempt that has already finished.
	ErrTerminal = errors.New("ota: update attempt is already terminal")
)

// Target is what the attempt is meant to leave the component running.
type Target struct {
	Version        string
	Digest         string
	RequiresReboot bool
}

// Policy is how long the platform waits, measured from the last accepted report, in each phase.
type Policy struct {
	Acknowledge time.Duration // QUEUED, INITIATED
	Download    time.Duration // DOWNLOADING, DOWNLOADED
	Verify      time.Duration // VERIFIED
	Install     time.Duration // UPDATING
	Confirm     time.Duration // REBOOTING
}

// Validate fails unless every deadline is positive: a zero deadline would time every attempt out
// the instant it is checked.
func (p Policy) Validate() error {
	return ErrUnimplemented
}

// Failure records why an attempt FAILED or TIMED_OUT. Platform is true when the platform, not the
// device, decided (a deadline elapsed, or the device came back on the wrong version).
type Failure struct {
	Stage    ReportStage
	Code     string
	Detail   string
	Platform bool
}

// Attempt is one try at moving one component of one device to a target. It is a plain value: every
// function here returns a new Attempt and never mutates its input.
type Attempt struct {
	AttemptID, AssignmentID, Component string
	Target                             Target
	State                              State
	LastSeq                            uint64
	LastReportAt                       time.Time // platform receive time of the last APPLIED report (or creation)
	BootIDAtStart                      string    // first bootId seen on this attempt
	CancelRequested                    bool
	Reconciled                         bool // reached UPDATED/FAILED from UNKNOWN via evidence
	Progress                           *Progress
	Failure                            *Failure
}

// NewAttempt returns a QUEUED attempt, validating every identifier against the same grammar the
// wire uses.
func NewAttempt(attemptID, assignmentID, component string, t Target, now time.Time) (Attempt, error) {
	return Attempt{}, ErrUnimplemented
}

// Verdict is the reducer's answer for a report. Only APPLIED changes the attempt.
type Verdict string

const (
	VerdictApplied        Verdict = "APPLIED"
	VerdictDuplicate      Verdict = "DUPLICATE"       // same seq as LastSeq: idempotent no-op
	VerdictStaleAttempt   Verdict = "STALE_ATTEMPT"   // attemptId/assignmentId is not this attempt
	VerdictDigestMismatch Verdict = "DIGEST_MISMATCH" // artifactDigest differs from the target
	VerdictWrongComponent Verdict = "WRONG_COMPONENT"
	VerdictRegression     Verdict = "REGRESSION"      // lower stage rank, or seq below LastSeq
	VerdictUnconfirmed    Verdict = "UNCONFIRMED"     // no valid evidence that the target is running
	VerdictNotCancellable Verdict = "NOT_CANCELLABLE" // ABANDONED without a cancel request, or past VERIFIED
	VerdictLate           Verdict = "LATE"            // progress on a terminal attempt, or on UNKNOWN
	VerdictUnimplemented  Verdict = "UNIMPLEMENTED"
	VerdictWrongKind      Verdict = "WRONG_KIND" // wrong kind for this function, or a malformed report
)

func (a Attempt) clone() Attempt {
	if a.Progress != nil {
		p := *a.Progress
		a.Progress = &p
	}
	if a.Failure != nil {
		f := *a.Failure
		a.Failure = &f
	}
	return a
}

// Apply is a stub.
func Apply(a Attempt, r Report, receivedAt time.Time) (Attempt, Verdict) {
	return a, VerdictUnimplemented
}

// Tick is a stub.
func Tick(a Attempt, p Policy, now time.Time) (Attempt, bool) { return a, false }

// Cancel is a stub.
func Cancel(a Attempt, _ time.Time) (Attempt, error) { return a, ErrUnimplemented }

// Reconcile is a stub.
func Reconcile(a Attempt, inv Report, receivedAt time.Time) (Attempt, Verdict) {
	return a, VerdictUnimplemented
}
