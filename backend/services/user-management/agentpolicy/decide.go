// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agentpolicy

import (
	"slices"
)

// Kind is the closed vocabulary of agent actions a decision is asked about.
type Kind string

const (
	KindAlarmAck      Kind = "ALARM_ACK"
	KindAlarmClear    Kind = "ALARM_CLEAR"
	KindCommandSend   Kind = "COMMAND_SEND"
	KindCommandBatch  Kind = "COMMAND_BATCH"
	KindRuleChangeset Kind = "RULE_CHANGESET"
	KindRolloutCreate Kind = "ROLLOUT_CREATE"
	KindAnnotation    Kind = "ANNOTATION"
)

// Kinds is every kind, for exhaustive tests.
var Kinds = []Kind{KindAlarmAck, KindAlarmClear, KindCommandSend, KindCommandBatch, KindRuleChangeset, KindRolloutCreate, KindAnnotation}

// Verdict is a decision's outcome.
type Verdict string

const (
	VerdictRefuse  Verdict = "REFUSE"
	VerdictAuto    Verdict = "AUTO"
	VerdictApprove Verdict = "APPROVE"
)

// Refusal and downgrade reasons. They are platform-authored constants: never formatted from
// anything a tenant, a device or an agent wrote.
const (
	ReasonInstanceOff        = "INSTANCE_ACTUATION_OFF"
	ReasonOperatorSuspended  = "OPERATOR_SUSPENDED"
	ReasonNoPolicy           = "NO_PUBLISHED_POLICY"
	ReasonActuationOff       = "TENANT_ACTUATION_OFF"
	ReasonClientNotAllowed   = "CLIENT_NOT_ALLOWED"
	ReasonUnknownKind        = "UNKNOWN_KIND"
	ReasonUnknownImpact      = "UNKNOWN_IMPACT"
	ReasonBadTargetCount     = "BAD_TARGET_COUNT"
	ReasonKindRefused        = "KIND_REFUSED_BY_POLICY"
	ReasonCommandNotAllowed  = "COMMAND_NOT_ALLOWED"
	ReasonGroupNotAllowed    = "GROUP_NOT_ALLOWED"
	ReasonOverPerActionCap   = "OVER_PER_ACTION_CAP"
	ReasonOverRolloutSize    = "OVER_ROLLOUT_GROUP_SIZE"
	ReasonFloorNoAuto        = "FLOOR_NEVER_AUTO"
	ReasonFloorTwoPerson     = "FLOOR_TWO_PERSON"
	ReasonPolicyTwoPerson    = "POLICY_TWO_PERSON"
	ReasonPublicClientNoAuto = "PUBLIC_CLIENT_NOT_AUTO"
	ReasonTooFewApprovers    = "TOO_FEW_ELIGIBLE_APPROVERS"
	ReasonAgentOnly          = "NOT_AN_AGENT_PRINCIPAL"
	ReasonAutoNotAvailable   = "AUTO_NOT_AVAILABLE"
)

// Request is what a decision is asked about. Client and Attested come from the caller's TOKEN and
// the client registry, never from an argument.
type Request struct {
	Kind        Kind
	Impact      Impact
	TargetCount int
	GroupTokens []string
	// Command is the command name, for the command kinds.
	Command string
	// Client is the token's client_id.
	Client string
	// Attested is whether Client is a confidential client (or an agent service account), whose
	// identity was authenticated rather than claimed.
	Attested bool
}

// State is the instance and tenant state a decision is made in.
type State struct {
	// InstanceEnabled is the instance flag.
	InstanceEnabled bool
	// OperatorSuspended is the operator's per-tenant override.
	OperatorSuspended bool
	// Policy is the published document, nil when none is published (or it cannot be read).
	Policy *Document
	// Version is the published version, 0 when none.
	Version int
	// EligibleApprovers counts the enabled members who are in an approver role and hold the raw
	// write authority the kind needs. An approval verdict that needs more approvers than this is a
	// refusal.
	EligibleApprovers int
}

// Decision is the verdict.
type Decision struct {
	Verdict           Verdict
	RequiredApprovals int
	Reasons           []string
	PolicyVersion     int
	// GroupRestriction, when non-nil, is the set of groups targets must belong to; the owning area
	// enforces it against device membership at a frozen version.
	GroupRestriction []string
}

func refuse(version int, reasons ...string) Decision {
	return Decision{Verdict: VerdictRefuse, Reasons: reasons, PolicyVersion: version}
}

// Decide is the tenant's verdict on one prospective agent action. It is total and fails closed:
// every input it does not recognise refuses, and the code floors apply whatever the policy says.
//
// The floors (D-14), which no policy can loosen:
//   - AUTO is never available for a DESTRUCTIVE command, a rule changeset or a rollout;
//   - a DESTRUCTIVE command, and a rollout over RolloutTwoPersonOver devices, need two approvers;
//   - a two-approver verdict with fewer than two eligible approvers is a refusal, never a quiet
//     downgrade to one.
func Decide(st State, req Request) Decision {
	v := st.Version
	switch {
	case !st.InstanceEnabled:
		return refuse(v, ReasonInstanceOff)
	case st.OperatorSuspended:
		return refuse(v, ReasonOperatorSuspended)
	case st.Policy == nil || st.Version <= 0:
		return refuse(v, ReasonNoPolicy)
	case !st.Policy.ActuationOn():
		return refuse(v, ReasonActuationOff)
	case req.Client == "":
		return refuse(v, ReasonAgentOnly)
	case !st.Policy.ClientAllowed(req.Client):
		return refuse(v, ReasonClientNotAllowed)
	case !slices.Contains(Kinds, req.Kind):
		return refuse(v, ReasonUnknownKind)
	case req.TargetCount < 0 || req.TargetCount > 1_000_000:
		return refuse(v, ReasonBadTargetCount)
	}
	impact := req.Impact
	if impact == "" {
		impact = ImpactNormal
	}
	if !validImpact(impact) {
		return refuse(v, ReasonUnknownImpact)
	}

	t := tools(st.Policy)
	d := Decision{PolicyVersion: v}
	var mode Mode
	auto := false // whether the policy would allow AUTO, before floors
	approvals := 1
	switch req.Kind {
	case KindAlarmAck:
		mode = modeOr(alarms(t).Ack, ModeApprove)
		auto = mode == ModeAuto
	case KindAlarmClear:
		mode = modeOr(alarms(t).Clear, ModeApprove)
		auto = mode == ModeAuto
	case KindCommandSend, KindCommandBatch:
		c := commands(t)
		mode = modeOr(c.Mode, ModeApprove)
		if mode != ModeRefuse {
			if req.Command == "" || !slices.Contains(c.Commands, req.Command) {
				return refuse(v, ReasonCommandNotAllowed)
			}
			if c.Groups != nil {
				for _, g := range req.GroupTokens {
					if !slices.Contains(c.Groups, g) {
						return refuse(v, ReasonGroupNotAllowed)
					}
				}
				d.GroupRestriction = append([]string{}, c.Groups...)
			}
			if cp := caps(c); cp.PerAction != nil && req.TargetCount > *cp.PerAction {
				return refuse(v, ReasonOverPerActionCap)
			}
			a := commandAuto(c)
			auto = slices.Contains(a.Impact, impact) && req.TargetCount <= intOr(a.MaxDevices, 0) && req.TargetCount > 0
			tp := twoPerson(c)
			if slices.Contains(tp.Impact, impact) || req.TargetCount > intOr(tp.OverDevices, DefaultTwoPersonOverDevices) {
				approvals = 2
				d.Reasons = append(d.Reasons, ReasonPolicyTwoPerson)
			}
		}
		if impact == ImpactDestructive {
			approvals = 2
			d.Reasons = append(d.Reasons, ReasonFloorTwoPerson)
		}
	case KindRuleChangeset:
		mode = modeOr(rules(t).Mode, ModeApprove)
	case KindRolloutCreate:
		r := rollouts(t)
		mode = modeOr(r.Mode, ModeApprove)
		if r.MaxGroupSize != nil && req.TargetCount > *r.MaxGroupSize {
			return refuse(v, ReasonOverRolloutSize)
		}
		if req.TargetCount > RolloutTwoPersonOver {
			approvals = 2
			d.Reasons = append(d.Reasons, ReasonFloorTwoPerson)
		}
	case KindAnnotation:
		mode = modeOr(t.Annotate, ModeAuto)
		auto = mode == ModeAuto
	}
	if mode == ModeRefuse {
		return refuse(v, ReasonKindRefused)
	}

	// The code floors on AUTO.
	if auto && (impact == ImpactDestructive || req.Kind == KindRuleChangeset || req.Kind == KindRolloutCreate) {
		auto = false
		d.Reasons = append(d.Reasons, ReasonFloorNoAuto)
	}
	if auto && approvals > 1 {
		auto = false
	}
	if auto && !req.Attested && !autoPublic(st.Policy) {
		auto = false
		d.Reasons = append(d.Reasons, ReasonPublicClientNoAuto)
	}
	if auto {
		// 🔴 NO AUTOMATIC PATH IN THIS RELEASE (red line AX-RL8: no auto path without holdouts and
		// invariants). The holdout sets and invariant coverage that would make automatic
		// execution answerable do not exist yet, so a policy that WOULD allow AUTO gets a verdict
		// that needs a person instead. VerdictAuto stays in the vocabulary for the release that
		// earns it; this function cannot return it until then.
		auto = false
		d.Reasons = append(d.Reasons, ReasonAutoNotAvailable)
	}
	if req.Kind == KindAnnotation {
		// An annotation never queues: it is automatic or it does not happen.
		return refuse(v, append(d.Reasons, ReasonKindRefused)...)
	}
	// A verdict nobody can satisfy is a refusal: two-person never quietly becomes one, and an
	// approval with no eligible approver would only wait out its expiry.
	if st.EligibleApprovers < approvals {
		return refuse(v, append(d.Reasons, ReasonTooFewApprovers)...)
	}
	d.Verdict = VerdictApprove
	d.RequiredApprovals = approvals
	return d
}
