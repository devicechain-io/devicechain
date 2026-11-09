// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import "strings"

// PROPOSAL authorities: the capability to ASK for an effect, never to cause it. They are what an
// agent's token carries in place of the write authority a human holds (agentic actions, S2).
//
// 🔴 THEY ARE DERIVED, NEVER GRANTED. No role may name one (ValidAuthority refuses them, so the
// role editor never offers them and a role write naming one fails), no service-token request may
// name one (the same check), and the super-authority "*" never satisfies one (HasAuthority) and is
// never expanded into one (IntersectAuthorities). The ONLY way a token comes to carry one is
// DeriveProposalAuthorities, at an OAuth grant or refresh, from a write authority the delegating
// human holds at that moment. That keeps "an agent can do no more than the person it acts for"
// structural, the same way the read ceilings keep OAuth reads structural.
//
// Nothing accepts a proposal authority yet: the proposal mutations and their gate arrive with the
// first action (S3). Until then a token carrying one can do exactly what its read authorities
// allow, and every existing mutation refuses it because it holds no raw write.
const (
	// AlarmPropose asks for an alarm acknowledge or clear. Derived from alarm:write.
	AlarmPropose Authority = "alarm:propose"
	// CommandPropose asks for a command to be sent. Derived from command:write.
	CommandPropose Authority = "command:propose"
	// RulePropose asks for a detection-rule changeset. Derived from device:write held WITHOUT
	// scope anchors: a profile serves every device of its types, including ones adopted later, so
	// a member scoped to part of the tenant may not propose a change to it.
	RulePropose Authority = "rule:propose"
	// RolloutPropose asks for a firmware rollout. Derived from artifact:write.
	RolloutPropose Authority = "rollout:propose"
	// AnnotationPropose adds a note marked as written by an assistant. Derived from alarm:write, an
	// operator-level authority: a viewer cannot write tenant-visible text today, and letting a
	// viewer's agent do it would open a stored-injection channel.
	AnnotationPropose Authority = "annotation:propose"
)

// proposalSources maps each proposal authority to the write authority a subject must hold for it
// to be derived. A proposal authority that is not in this table does not exist.
var proposalSources = map[Authority]Authority{
	AlarmPropose:      AlarmWrite,
	CommandPropose:    CommandWrite,
	RulePropose:       DeviceWrite,
	RolloutPropose:    ArtifactWrite,
	AnnotationPropose: AlarmWrite,
}

// unscopedOnlyProposals are derived only for a grant with no scope anchors (see RulePropose).
var unscopedOnlyProposals = map[Authority]bool{RulePropose: true}

// IsProposalAuthority reports whether a names a proposal authority this build defines.
func IsProposalAuthority(a string) bool {
	_, ok := proposalSources[Authority(a)]
	return ok
}

// looksLikeProposal is the SHAPE test: anything ending in ":propose", defined or not. Used where
// the safe answer for an unknown proposal-shaped string is the restrictive one.
func looksLikeProposal(a string) bool { return strings.HasSuffix(a, ProposalAuthoritySuffix) }

// ProposalSource returns the write authority a proposal authority is derived from.
func ProposalSource(p Authority) (Authority, bool) {
	w, ok := proposalSources[p]
	return w, ok
}

// DeriveProposalAuthorities returns the proposal authorities a subject's grant may carry under a
// ceiling: for each proposal authority P in ceiling, P is emitted only if held contains P's source
// write authority W, or the super-authority "*". Holding "*" counts as holding W, but "*" itself is
// never copied into the result, and nor is W: the result holds proposal authorities and nothing else.
//
// scoped is whether the grant carries scope anchors (a customer or group membership). A scoped
// grant never derives an unscoped-only proposal (RulePropose).
//
// Anything in ceiling that is not a defined proposal authority is ignored, so a ceiling can never
// smuggle a raw authority through this function. The result is ordered by ceiling and deduplicated.
func DeriveProposalAuthorities(held, ceiling []string, scoped bool) []string {
	hasAll := false
	heldSet := make(map[string]struct{}, len(held))
	for _, h := range held {
		if h == string(AuthorityAll) {
			hasAll = true
		}
		heldSet[h] = struct{}{}
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range ceiling {
		src, ok := proposalSources[Authority(p)]
		if !ok || seen[p] {
			continue
		}
		if scoped && unscopedOnlyProposals[Authority(p)] {
			continue
		}
		if _, holds := heldSet[string(src)]; holds || hasAll {
			out = append(out, p)
			seen[p] = true
		}
	}
	return out
}
