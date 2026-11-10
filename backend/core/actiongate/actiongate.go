// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package actiongate is the PROPOSAL LEDGER every area that lets an AI agent ask for an effect
// assembles (agentic actions, S3). An agent's token never carries a write authority: it carries
// a proposal authority derived at mint time from a write the delegating person holds. So every
// agent write is a proposal, stored in the area that owns the resource, decided by the tenant's
// published agent policy, and approved by people from their own sessions.
//
// The package owns what must be the same in every area, so that it is written once:
//
//   - who may PROPOSE (AuthorizeProposer): an agent's access token carrying the one proposal
//     authority the operation accepts, never a service token, never a person's console token,
//     never a scoped grant in this release;
//   - who may APPROVE or REJECT (AuthorizeApprover, CheckStepUp): a person's own access token,
//     never an agent's, holding the raw write authority, with a second factor that is fresh by
//     the policy's measure;
//   - the canonical form and the DIGEST an approval binds to (Canonical, Digest, DigestEqual);
//   - the server-generated tokens (NewToken);
//   - the state machine (State, CanTransition) and the one conditional transition every state
//     change goes through (Transition), through the ORM so the journal records it;
//   - the counters' serialization point (LockCounters) and their limits (Limits);
//   - the decisions, asked of user-management UNDER THE CALLER'S OWN TOKEN and never cached
//     (Decider, CallerDecider);
//   - the approval tally (Tally).
//
// Each area owns its own agent_proposals table, in an appended migration of its own, because the
// safety property is atomic consume-and-execute in the area that owns the effect: a central
// service would need the area to execute FOR it, under a service token.
//
// Approved ACK/CLEAR proposals execute on final human approval in one transaction.
// There is no automatic path: an AUTO verdict is read as a refusal.
package actiongate

import (
	"errors"
	"fmt"
	"slices"

	"github.com/devicechain-io/dc-microservice/auth"
)

// Kind is the closed vocabulary of proposal kinds. The values are the user-management decision
// kinds (AgentActionKind), so a kind is passed to a decision as it is stored.
type Kind string

const (
	KindAlarmAck   Kind = "ALARM_ACK"
	KindAlarmClear Kind = "ALARM_CLEAR"
)

// Kinds is every kind this release can propose.
var Kinds = []Kind{KindAlarmAck, KindAlarmClear}

// Valid reports whether k is a kind this release can propose.
func (k Kind) Valid() bool { return slices.Contains(Kinds, k) }

// State is a proposal's lifecycle state.
type State string

const (
	// StateProposed waits for approval. It is the only non-terminal state.
	StateProposed State = "PROPOSED"
	// StateExecuted: the final approval was consumed by the effect, in the same transaction.
	StateExecuted State = "EXECUTED"
	// StateRejected: a person rejected it.
	StateRejected State = "REJECTED"
	// StateExpired: nobody approved it in time.
	StateExpired State = "EXPIRED"
	// StateRefused: the gate refused it, at proposal or at a re-check.
	StateRefused State = "REFUSED"
	// StateCancelled: withdrawn by its proposer.
	StateCancelled State = "CANCELLED"
	// StateKilled: ended by a kill switch.
	StateKilled State = "KILLED"
)

// States is every state.
var States = []State{StateProposed, StateExecuted, StateRejected, StateExpired, StateRefused, StateCancelled, StateKilled}

// Valid reports whether s is a state.
func (s State) Valid() bool { return slices.Contains(States, s) }

// Terminal reports whether s is final. Every state but PROPOSED is.
func (s State) Terminal() bool { return s.Valid() && s != StateProposed }

// CanTransition reports whether from -> to is a transition of the state machine. The machine is
// one step deep: PROPOSED moves to any terminal state, and a terminal state never moves. In
// particular nothing ever returns to PROPOSED, so a rejected, expired, refused or killed proposal
// cannot be revived by any later write.
//
// A REFUSED row may also be WRITTEN directly (a proposal the gate refused on arrival is stored
// refused, for the record); that is an insert, not a transition, and never passes through here.
func CanTransition(from, to State) bool {
	return from == StateProposed && to.Terminal()
}

// Error is a gate refusal with a stable code a client can branch on. The message is
// platform-authored; it never carries text a tenant, a device or an agent wrote.
type Error struct {
	Code string
	// Retryable reports whether the refusal can clear when a dependency recovers.
	Retryable bool
	msg       string
	err       error
}

func (e *Error) Error() string { return e.msg }

// Unwrap lets errors.Is see the sentinel it wraps (auth.ErrForbidden, for the authorization
// refusals).
func (e *Error) Unwrap() error { return e.err }

// Extensions carries the stable code and retryability to GraphQL clients.
func (e *Error) Extensions() map[string]any {
	return map[string]any{"code": e.Code, "retryable": e.Retryable}
}

func gateErr(code string, wrap error, format string, a ...any) *Error {
	return &Error{Code: code, msg: fmt.Sprintf(format, a...), err: wrap}
}

// The refusal codes.
const (
	CodeAgentOnly             = "AGENT_ONLY"
	CodeServiceTokenRefused   = "SERVICE_TOKEN_REFUSED"
	CodeScopedNotSupported    = "SCOPED_GRANT_NOT_SUPPORTED"
	CodeApproverIsAgent       = "APPROVER_IS_AN_AGENT"
	CodeMfaRequired           = "SECOND_FACTOR_REQUIRED"
	CodeStepUpRequired        = "STEP_UP_REQUIRED"
	CodeDigestMismatch        = "DIGEST_MISMATCH"
	CodeNotPending            = "NOT_PENDING"
	CodeExpired               = "PROPOSAL_EXPIRED"
	CodeNotAnApprover         = "NOT_AN_APPROVER"
	CodeSelfApproval          = "SELF_APPROVAL_REFUSED"
	CodeExecutionNotAvailable = "EXECUTION_NOT_AVAILABLE"
	CodePolicyUnavailable     = "POLICY_UNAVAILABLE"
	CodeFenceClosed           = "FENCE_CLOSED"
	CodeFenceUnavailable      = "FENCE_UNAVAILABLE"
	CodeRequestConflict       = "CONFLICT"
	CodeConcurrentApproval    = "CONCURRENT_APPROVAL"
)

// Refusal builds a coded refusal for an area's own checks. forbidden makes it an
// auth.ErrForbidden (errors.Is), which is what a refusal about the caller's standing is; a refusal
// about the proposal itself (expired, digest mismatch) is not.
func Refusal(code, msg string, forbidden bool) *Error {
	var wrap error
	if forbidden {
		wrap = auth.ErrForbidden
	}
	return &Error{Code: code, msg: msg, err: wrap}
}

// ErrExecutionNotAvailable is retained for error-code compatibility.
// Deprecated: approved ACK/CLEAR proposals execute on final human approval.
var ErrExecutionNotAvailable = gateErr(CodeExecutionNotAvailable, nil,
	"this approval would complete the proposal, and executing approved agent actions is not available in this release; the proposal stays pending until it expires or is rejected")

// ErrPolicyUnavailable is the refusal when the tenant's decision could not be obtained or read:
// an unreachable or unreadable policy is a refusal, never a default.
var ErrPolicyUnavailable = gateErr(CodePolicyUnavailable, nil,
	"the tenant's agent policy could not be read; agent actions are refused until it can be")

// IsCode reports whether err is a gate Error carrying code.
func IsCode(err error, code string) bool {
	var ge *Error
	return errors.As(err, &ge) && ge.Code == code
}

// ErrFenceClosed refuses a decision at or before the local kill version.
var ErrFenceClosed = Refusal(CodeFenceClosed, "the local agent actuation fence is closed", false)

// ErrFenceUnavailable refuses execution until the local fence can be read.
var ErrFenceUnavailable = func() *Error {
	e := Refusal(CodeFenceUnavailable, "the local agent actuation fence could not be read", false)
	e.Retryable = true
	return e
}()
