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
