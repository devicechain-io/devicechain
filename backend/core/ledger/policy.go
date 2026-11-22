// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ledger

// Safety contains contract limits, not the engine's effective runtime values.
// A nil rate or threshold means no ceiling. The deadline is always explicit.
type Safety struct {
	MaxPostsPerSecond     *int64 `json:"maxPostsPerSecond"`
	FailureThreshold      *int64 `json:"failureThreshold"`
	AmbiguityThreshold    *int64 `json:"ambiguityThreshold"`
	SettleDeadlineSeconds int64  `json:"settleDeadlineSeconds"`
}

// PolicyChange describes a comparison to the immediately preceding policy.
type PolicyChange string

const (
	PolicySame         PolicyChange = "SAME"
	PolicyNarrower     PolicyChange = "NARROWER"
	PolicyWider        PolicyChange = "WIDER"
	PolicyIncomparable PolicyChange = "INCOMPARABLE"
)

// StrictPolicyOrder compares limits without any remembered approval ceiling.
// Returning to an earlier, higher ceiling is therefore WIDER.
type StrictPolicyOrder struct{}

func (StrictPolicyOrder) Compare(from, to Safety) PolicyChange {
	if from.SettleDeadlineSeconds != to.SettleDeadlineSeconds {
		return PolicyIncomparable
	}
	narrower, wider := false, false
	for _, pair := range [][2]*int64{
		{from.MaxPostsPerSecond, to.MaxPostsPerSecond},
		{from.FailureThreshold, to.FailureThreshold},
		{from.AmbiguityThreshold, to.AmbiguityThreshold},
	} {
		switch compareLimit(pair[0], pair[1]) {
		case PolicyNarrower:
			narrower = true
		case PolicyWider:
			wider = true
		}
	}
	switch {
	case narrower && wider:
		return PolicyIncomparable
	case narrower:
		return PolicyNarrower
	case wider:
		return PolicyWider
	default:
		return PolicySame
	}
}

func compareLimit(from, to *int64) PolicyChange {
	switch {
	case from == nil && to == nil:
		return PolicySame
	case to == nil:
		return PolicyWider
	case from == nil:
		return PolicyNarrower
	case *to < *from:
		return PolicyNarrower
	case *to > *from:
		return PolicyWider
	default:
		return PolicySame
	}
}
