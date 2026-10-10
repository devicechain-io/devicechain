// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ota

import (
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
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
	for _, f := range []struct {
		name string
		d    time.Duration
	}{{"acknowledge", p.Acknowledge}, {"download", p.Download}, {"verify", p.Verify}, {"install", p.Install}, {"confirm", p.Confirm}} {
		if f.d <= 0 {
			return fmt.Errorf("ota: policy %s deadline must be positive, got %s", f.name, f.d)
		}
	}
	return nil
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
	for _, f := range []struct{ name, v string }{{"attemptId", attemptID}, {"assignmentId", assignmentID}} {
		if err := checkLen(f.name, f.v, maxIDLen); err != nil {
			return Attempt{}, err
		}
		if err := core.ValidateToken(f.v); err != nil {
			return Attempt{}, fmt.Errorf("ota: %s: %w", f.name, err)
		}
	}
	if err := checkLen("component", component, maxComponentLen); err != nil {
		return Attempt{}, err
	}
	if !componentGrammar.MatchString(component) {
		return Attempt{}, fmt.Errorf("ota: component %q must match %s", component, componentGrammar)
	}
	if err := checkLen("target.version", t.Version, maxVersionLen); err != nil {
		return Attempt{}, err
	}
	if err := checkDigest("target.digest", t.Digest, true); err != nil {
		return Attempt{}, err
	}
	if now.IsZero() {
		return Attempt{}, errors.New("ota: now must be set")
	}
	return Attempt{
		AttemptID: attemptID, AssignmentID: assignmentID, Component: component,
		Target: t, State: StateQueued, LastReportAt: now,
	}, nil
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
	VerdictWrongKind      Verdict = "WRONG_KIND"      // wrong kind for this function, or a malformed report
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

// runsTarget: the reported version is the target, and any digest the device volunteers matches.
func runsTarget(t Target, run *Running) bool {
	return run != nil && run.Version == t.Version && (run.Digest == "" || run.Digest == t.Digest)
}

// confirms is the confirmation evidence for UPDATED: the target is running and, when the target
// needs a reboot, the device is on a different boot than the one the attempt started on. With no
// recorded starting boot there is nothing to compare with, so a reboot cannot be shown.
func confirms(t Target, run *Running, bootID, bootAtStart string) bool {
	if !runsTarget(t, run) {
		return false
	}
	if t.RequiresReboot {
		return bootAtStart != "" && bootID != "" && bootID != bootAtStart
	}
	return true
}

// Apply folds one update.progress report into the attempt. It is pure: the input is never mutated
// and the clock is never read (receivedAt is the platform's receive time). Callers pass the output of
// DecodeReport; Apply checks only the bindings it needs. Any verdict other than APPLIED returns the
// attempt unchanged.
func Apply(a Attempt, r Report, receivedAt time.Time) (Attempt, Verdict) {
	if r.Kind != KindProgress || !r.Stage.Valid() {
		return a, VerdictWrongKind
	}
	if r.Component != a.Component {
		return a, VerdictWrongComponent
	}
	if r.AttemptID != a.AttemptID || r.AssignmentID != a.AssignmentID {
		return a, VerdictStaleAttempt
	}
	if r.ArtifactDigest != a.Target.Digest {
		return a, VerdictDigestMismatch
	}
	if a.State.Terminal() {
		if r.Seq == a.LastSeq {
			return a, VerdictDuplicate
		}
		return a, VerdictLate
	}
	if r.Seq == a.LastSeq {
		return a, VerdictDuplicate
	}
	if r.Seq < a.LastSeq {
		return a, VerdictRegression
	}

	rl, ok := transitions[a.State][r.Stage]
	if !ok {
		panic(fmt.Sprintf("ota: no transition rule for state %s, stage %s", a.State, r.Stage))
	}
	out := a.clone()
	switch rl {
	case ruleLate:
		return a, VerdictLate
	case ruleRegress:
		return a, VerdictRegression
	case ruleProgress:
		next, _ := stateForStage(r.Stage)
		out.State = next
		out.Progress = nil
		if r.Progress != nil {
			p := *r.Progress
			out.Progress = &p
		}
	case ruleEvidence:
		if !confirms(a.Target, r.Running, r.BootID, a.BootIDAtStart) {
			return a, VerdictUnconfirmed
		}
		out.State = StateUpdated
		out.Reconciled = a.State == StateUnknown
	case ruleFail:
		if r.Result == nil {
			return a, VerdictWrongKind
		}
		out.State = StateFailed
		out.Failure = &Failure{Stage: stageForState(a.State), Code: r.Result.Code, Detail: r.Result.Detail}
		out.Reconciled = a.State == StateUnknown
	case ruleAbandon:
		rk, ranked := rank(a.State)
		if !a.CancelRequested || !ranked || rk > mustRank(StateVerified) {
			return a, VerdictNotCancellable
		}
		out.State = StateCancelled
	default:
		panic(fmt.Sprintf("ota: unhandled rule %d", rl))
	}
	if out.BootIDAtStart == "" {
		out.BootIDAtStart = r.BootID
	}
	out.LastSeq = r.Seq
	out.LastReportAt = receivedAt
	return out, VerdictApplied
}

func mustRank(s State) int {
	r, ok := rank(s)
	if !ok {
		panic("ota: state has no rank: " + string(s))
	}
	return r
}

// deadline is the phase deadline that governs an in-flight, non-UNKNOWN state.
func deadline(p Policy, s State) time.Duration {
	switch s {
	case StateQueued, StateInitiated:
		return p.Acknowledge
	case StateDownloading, StateDownloaded:
		return p.Download
	case StateVerified:
		return p.Verify
	case StateUpdating:
		return p.Install
	case StateRebooting:
		return p.Confirm
	}
	panic("ota: no deadline for state " + string(s))
}

// Tick enforces the stage deadlines, measured from the last accepted report. Before installation
// is reported started an elapsed deadline is TIMED_OUT; during UPDATING or REBOOTING it is UNKNOWN,
// because the device may well have finished — silence is not failure, and Tick never yields UPDATED.
// Terminal and UNKNOWN attempts are untouched. An invalid Policy changes nothing (check it with
// Policy.Validate when it is loaded): a zero deadline must not time everything out.
func Tick(a Attempt, p Policy, now time.Time) (Attempt, bool) {
	if p.Validate() != nil || a.State.Terminal() || a.State == StateUnknown {
		return a, false
	}
	if !(now.Sub(a.LastReportAt) > deadline(p, a.State)) {
		return a, false
	}
	out := a.clone()
	if mustRank(a.State) <= mustRank(StateVerified) {
		out.State = StateTimedOut
		out.Failure = &Failure{Stage: stageForState(a.State), Code: "DEADLINE", Platform: true}
	} else {
		out.State = StateUnknown
	}
	return out, true
}

// Cancel asks to stop an attempt. QUEUED is cancelled outright (nothing was ever sent to a device
// that could act on it); INITIATED..VERIFIED records CancelRequested and waits for the device's
// ABANDONED; from UPDATING on it is beyond recall. The time argument is accepted for symmetry with
// the other transitions and unused: a cancel request is not a device report, so it does not move
// LastReportAt.
func Cancel(a Attempt, _ time.Time) (Attempt, error) {
	switch {
	case a.State.Terminal():
		return a, ErrTerminal
	case a.State == StateQueued:
		out := a.clone()
		out.State = StateCancelled
		return out, nil
	case a.State == StateUnknown:
		return a, ErrBeyondRecall
	}
	rk, ok := rank(a.State)
	if !ok {
		return a, fmt.Errorf("ota: cannot cancel an attempt in state %q", a.State)
	}
	if rk > mustRank(StateVerified) {
		return a, ErrBeyondRecall
	}
	out := a.clone()
	out.CancelRequested = true
	return out, nil
}

// Reconcile resolves an UNKNOWN attempt from an update.inventory report, the only thing besides a
// RUNNING or FAILED progress report that can. Verdicts: WRONG_KIND for a report that is not an
// inventory; WRONG_COMPONENT; LATE for an attempt that is already terminal; UNCONFIRMED when the
// attempt is not UNKNOWN (there is nothing to reconcile) or the inventory proves nothing either
// way (the target is not running but the device has not rebooted since the attempt began).
func Reconcile(a Attempt, inv Report, receivedAt time.Time) (Attempt, Verdict) {
	if inv.Kind != KindInventory {
		return a, VerdictWrongKind
	}
	if inv.Component != a.Component {
		return a, VerdictWrongComponent
	}
	if a.State.Terminal() {
		return a, VerdictLate
	}
	if a.State != StateUnknown || inv.Running == nil {
		return a, VerdictUnconfirmed
	}
	out := a.clone()
	switch {
	case confirms(a.Target, inv.Running, inv.BootID, a.BootIDAtStart):
		out.State = StateUpdated
	case !runsTarget(a.Target, inv.Running) && a.BootIDAtStart != "" && inv.BootID != "" && inv.BootID != a.BootIDAtStart:
		// The device rebooted and is not on the target: rolled back, or never applied.
		out.State = StateFailed
		out.Failure = &Failure{Code: "NOT_RUNNING_TARGET", Platform: true}
	default:
		return a, VerdictUnconfirmed
	}
	out.Reconciled = true
	out.LastReportAt = receivedAt
	return out, VerdictApplied
}
