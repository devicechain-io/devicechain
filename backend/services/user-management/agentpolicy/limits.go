// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agentpolicy

import "time"

// The figures a proposal's gate needs beside the verdict (agentic actions, S3): how long the
// proposal may wait, how fast the agent may ask, and who may approve under which step-up.

// ExpiryFor is how long a proposal of kind may wait for approval: the policy's rollout expiry for
// a rollout, its default expiry otherwise, never longer than the policy's own maximum, and never
// longer than MaxExpiry whatever the document says.
func (d *Document) ExpiryFor(kind Kind) time.Duration {
	e := Expiry{}
	if d != nil {
		e = expiry(d)
	}
	out := e.Default.D(DefaultExpiry)
	if kind == KindRolloutCreate {
		out = e.Rollout.D(DefaultRolloutExpiry)
	}
	if max := e.Max.D(MaxExpiry); out > max {
		out = max
	}
	if out > MaxExpiry {
		out = MaxExpiry
	}
	return out
}

// ActionsPerHourFor is the policy's narrowing of the platform rate for kind, or 0 when the policy
// sets none. Only the alarm tools carry a rate in this document version.
func (d *Document) ActionsPerHourFor(kind Kind) int {
	if d == nil {
		return 0
	}
	switch kind {
	case KindAlarmAck, KindAlarmClear:
		return intOr(alarms(tools(d)).RatePerHour, 0)
	}
	return 0
}

// ApproverRoles are the role tokens whose members may approve. None means nobody may.
func (d *Document) ApproverRoles() []string {
	if d == nil {
		return nil
	}
	return approvers(d).Roles
}

// SelfConfirmAllowed reports whether the delegating person may approve their own agent's
// single-approval, normal-impact proposal. Default true; never for a two-person proposal (the
// caller applies that).
func (d *Document) SelfConfirmAllowed() bool {
	return d != nil && selfConfirm(approvers(d))
}
