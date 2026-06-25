// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

// Authority is an atomic, capability-style permission of the form
// "resource:action" (e.g. "device:write"). Authorization is capability-based
// (ADR-008 RBAC): a Role bundles a set of authorities, a user's effective
// authorities are the union of their roles', and a resolver checks for the
// specific authority it requires — never a role name — so a role can be
// re-scoped without code changes.
type Authority string

const (
	// AuthorityAll is the super-authority: a subject holding it passes every
	// authorization check. The bootstrap admin role grants it.
	AuthorityAll Authority = "*"

	// User + role directory (user-management).
	UserRead  Authority = "user:read"
	UserWrite Authority = "user:write"
	RoleRead  Authority = "role:read"
	RoleWrite Authority = "role:write"

	// Device management (device-management). Enforcement is rolled out per service
	// in later PRs; the vocabulary is defined here up front so resolvers across
	// services reference one canonical set.
	DeviceRead  Authority = "device:read"
	DeviceWrite Authority = "device:write"

	// Event history (event-management) + live state (device-state).
	EventRead Authority = "event:read"
	StateRead Authority = "state:read"

	// Command delivery (command-delivery).
	CommandRead  Authority = "command:read"
	CommandWrite Authority = "command:write"
)

// vocabulary is the set of every known authority. A Role may only grant
// authorities in this set (plus AuthorityAll), so a typo in a role definition is
// rejected at write time rather than silently granting nothing. It is extended as
// each service's resolvers are brought under enforcement.
var vocabulary = map[Authority]struct{}{
	AuthorityAll: {},
	UserRead:     {},
	UserWrite:    {},
	RoleRead:     {},
	RoleWrite:    {},
	DeviceRead:   {},
	DeviceWrite:  {},
	EventRead:    {},
	StateRead:    {},
	CommandRead:  {},
	CommandWrite: {},
}

// ValidAuthority reports whether s names a known authority (including the
// super-authority "*").
func ValidAuthority(s string) bool {
	_, ok := vocabulary[Authority(s)]
	return ok
}
