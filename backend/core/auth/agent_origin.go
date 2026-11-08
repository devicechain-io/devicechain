// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import "strings"

// ProposalAuthoritySuffix ends the name of every PROPOSAL authority: a capability to ask for an
// effect rather than to cause it ("alarm:propose", "command:propose"). No such authority exists
// yet; IsAgentOrigin already recognises the shape so that the first one added is treated as the
// mark of an agent from the day it exists, not from the day someone remembers to teach the
// predicate.
const ProposalAuthoritySuffix = ":propose"

// IsAgentOrigin reports whether claims describe a principal that must be treated as an AI agent
// (or any other delegated, non-interactive client) by every gate and every approval path.
//
// 🔴 TRUE ONLY EVER RESTRICTS. It is a RESTRICTION signal, never a proof of identity and never a
// grant: a caller that gets true applies the agent rules, and nothing may be allowed on the
// strength of it. That is what lets it be a deliberately blunt superset.
//
// It is true when ANY of these holds:
//
//   - the claims are nil (an undecidable origin is an agent origin);
//   - client_id is non-empty: the token was minted for an OAuth client;
//   - the OAuth scope string is non-empty;
//   - aud is non-empty. The precise rule is "aud contains a registered MCP resource", but this
//     function is pure and cannot see the registry, and the audience planes that CAN see it
//     run in observe mode today, so the predicate must not depend on them. Any audience at all
//     counts. A console token carries none;
//   - any authority ends in ProposalAuthoritySuffix.
//
// It is total: no input panics and none is "unknown". The OAuth mint paths stamp client_id and
// scope on every token they issue (TestOAuthMintAlwaysStampsClientIdAndScope), which is what
// makes the first two arms reliable and the others redundant.
func IsAgentOrigin(c *Claims) bool {
	if c == nil {
		return true
	}
	if c.ClientId != "" || c.Scope != "" || len(c.Audience) > 0 {
		return true
	}
	for _, a := range c.Authorities {
		if strings.HasSuffix(a, ProposalAuthoritySuffix) {
			return true
		}
	}
	return false
}

// SameSession reports whether two session ids name the same sign-in.
//
// 🔴 A MISSING SID IS "UNKNOWN", NEVER A MATCH. The "sid" claim rides OAuth access tokens only
// from the release that added it, and never rides a service-account token, so a token without
// one is ordinary. Comparing two absent values with == would call them the same session, which
// would let a record written by an old token satisfy a check made by another. Equality of ids
// is also only a correlation: it authenticates nothing.
func SameSession(a, b SessionID) bool {
	return a != "" && b != "" && a == b
}
