// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import "strings"

// tenantSep separates the owning tenant from the rest of a rule id. Tenant tokens and rule
// tokens both satisfy the ADR-042 grammar (^[A-Za-z0-9][A-Za-z0-9_-]*$), which excludes
// "/", so a single "/" is an unambiguous, collision-free delimiter: the first segment is
// always exactly the tenant, no matter what the remainder encodes (slice 4b mints ids as
// "{tenant}/{profileVersion}/{ruleKey}").
const tenantSep = "/"

// ComposeRuleID builds a tenant-prefixed rule id. The tenant prefix is what the runtime
// tenant backstop validates before a detection is published to a tenant subject, and what
// scopes a rule's engine state to its owner.
func ComposeRuleID(tenant, rest string) string {
	return tenant + tenantSep + rest
}

// RuleTenant extracts the owning tenant from a rule id, returning ("", false) when the id
// is not tenant-prefixed (an unprefixed id is a mis-minted rule the backstop must reject
// rather than guess a tenant for).
func RuleTenant(id string) (string, bool) {
	tenant, _, ok := strings.Cut(id, tenantSep)
	if !ok || tenant == "" {
		return "", false
	}
	return tenant, true
}

// ProfileAndRuleToken extracts the profile token and rule token from a minted rule id
// "{tenant}/{profileToken}@{version}/{ruleToken}". It backs the detection-stream filter (slice
// 7c): a subscription scoped to a profile matches a detection whose rule carries that profile
// token, regardless of the (per-publish rotating) version. Returns ok=false for an id that does
// not parse into the minted shape. The token grammars exclude "/" and "@", so each Cut is
// unambiguous.
func ProfileAndRuleToken(id string) (profileToken, ruleToken string, ok bool) {
	_, rest, ok := strings.Cut(id, tenantSep) // drop the tenant prefix
	if !ok {
		return "", "", false
	}
	pvt, ruleToken, ok := strings.Cut(rest, tenantSep) // pvt = "{profileToken}@{version}"
	if !ok || ruleToken == "" {
		return "", "", false
	}
	profileToken, _, ok = strings.Cut(pvt, "@")
	if !ok || profileToken == "" {
		return "", "", false
	}
	return profileToken, ruleToken, true
}

// StableRuleKey returns a rule's VERSION-FREE, tenant-free identity "{profileToken}/{ruleToken}"
// from a minted id "{tenant}/{profileToken}@{version}/{ruleToken}". The profile version token rotates
// on EVERY profile publish (every enabled rule is re-emitted under the new version), but the profile
// token and rule token do not (ADR-045 / slice 4b-1 rename-blocked). It backs the REACT default alarm
// key so a rule's repeated firings escalate ONE alarm even across routine re-publishes, rather than
// forking a fresh alarm per version and orphaning the prior one ACTIVE (ADR-041 dec 3). It returns
// ok=false for an id that does not parse into that minted shape. The "/" separators make it a system
// key, not an ADR-042 authored token — intentional, so it stays both stable and per-rule unique.
func StableRuleKey(id string) (string, bool) {
	_, rest, ok := strings.Cut(id, tenantSep) // drop the tenant prefix
	if !ok {
		return "", false
	}
	// rest is "{profileToken}@{version}/{ruleToken}"; profile/rule/version tokens exclude "/" and "@"
	// (ADR-042), so the single remaining "/" splits the version token from the rule token unambiguously.
	pvt, ruleToken, ok := strings.Cut(rest, tenantSep)
	if !ok || ruleToken == "" {
		return "", false
	}
	profileToken, _, ok := strings.Cut(pvt, "@")
	if !ok || profileToken == "" {
		return "", false
	}
	return profileToken + tenantSep + ruleToken, true
}
