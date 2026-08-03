// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import "strings"

// Exemption excuses one table from the sweep, with the reason recorded next to it.
//
// A reason is mandatory and is not a formality: it is the only thing distinguishing
// "we checked, this holds nothing of any one tenant's" from "we could not work out
// what this was". The registry is read by a purge that is about to claim a tenant's
// data is gone, so an entry that cannot state why a table is safe to skip has not
// earned its place.
type Exemption struct {
	// Schema is the functional-area schema, or "*" to match any.
	Schema string
	// Name is the table name, or a "*"-suffixed prefix ("foo_*"), or "*_suffix".
	Name string
	// Class is ClassExempt (holds no single tenant's data) or ClassDeferred (holds
	// tenant data this mechanism does not yet erase).
	Class Class
	// Reason says why. For ClassDeferred it must name what is left behind.
	Reason string
}

// exemptions is every table the catalog cannot classify but the platform can explain.
//
// 🔴 THE RULE FOR ADDING TO THIS LIST: an entry is a claim that a table holds no data
// belonging to any single tenant. Before adding one, look at the table's rows, not at
// its name. If it holds tenant data and you are not erasing it, the entry is
// ClassDeferred with a reason naming what survives — not ClassExempt with a reason
// that sounds reassuring. The two classes exist so that the second case stays visible
// in every purge result instead of blending into a wall of exemptions.
//
// The list is checked only for tables the catalog could not classify on its own, so a
// stale entry naming a table that has since gained a tenant column is inert rather
// than dangerous — the catalog wins.
var exemptions = []Exemption{
	{
		Schema: "*", Name: "*_migrations", Class: ClassExempt,
		Reason: "gormigrate bookkeeping: one row per applied migration id, per functional area. " +
			"Records schema history, never tenant data.",
	},

	// ---- user-management: the instance's own control plane -------------------
	//
	// This area is where "no tenant column" most often means "genuinely shared"
	// rather than "unmarked", because it holds the catalog every tenant is defined
	// against. Each entry below says which.
	{
		Schema: "user-management", Name: "iam_identities", Class: ClassExempt,
		Reason: "an identity is a PERSON and is instance-global: the same login can hold memberships " +
			"in several tenants, so deleting one tenant must not delete the human being. Their link to " +
			"the purged tenant is the membership row, which is tenant-scoped and IS swept.",
	},
	{
		Schema: "user-management", Name: "iam_identity_system_roles", Class: ClassExempt,
		Reason: "joins an identity to an INSTANCE-level role (operator, superuser). Both ends are " +
			"instance-global; no tenant is expressible in this table.",
	},
	{
		Schema: "user-management", Name: "iam_roles", Class: ClassExempt,
		Reason: "the role catalog is defined once per instance and referenced by every tenant's " +
			"memberships. Roles outlive the tenants that used them.",
	},
	{
		Schema: "user-management", Name: "iam_tenant_tiers", Class: ClassExempt,
		Reason: "operator-defined packaging (governance ceilings, AI model entitlement). A tier is " +
			"authored by the operator and read by tenants; it is not owned by one.",
	},
	{
		Schema: "user-management", Name: "iam_oauth_clients", Class: ClassExempt,
		Reason: "an OAuth client is a platform-level registration; the tenant is chosen per grant at " +
			"the authorize step, so no row here belongs to a tenant.",
	},
	{
		Schema: "user-management", Name: "signing_keys", Class: ClassExempt,
		Reason: "the instance's JWT signing keys, shared by every tenant's tokens.",
	},
	{
		Schema: "user-management", Name: "system_settings", Class: ClassExempt,
		Reason: "instance-wide settings; no tenant dimension.",
	},
	{
		Schema: "user-management", Name: "iam_tenants", Class: ClassExempt,
		Reason: "the tenant's OWN row, and the one table the sweep must not touch. It holds the token " +
			"reservation that stops a successor being created mid-purge, so it is removed by the " +
			"completion step after every other area has been swept and verified — never by the sweep " +
			"that depends on it.",
	},

	// ---- ai-inference: operator-owned provider registry ----------------------
	{
		Schema: "ai-inference", Name: "ai_providers", Class: ClassExempt,
		Reason: "operator-registered inference providers (endpoint + write-only key handle). Shared " +
			"across tenants; a tenant's ACCESS to one is the grant row, which is tenant-scoped and " +
			"IS swept.",
	},
	{
		Schema: "ai-inference", Name: "ai_provider_tier_grants", Class: ClassExempt,
		Reason: "grants a provider to a TIER, not to a tenant. Per-tenant grants live in " +
			"ai_provider_tenant_grants, which carries a tenant column and is swept.",
	},

	// ---- event-processing: the DETECT engine's own state --------------------
	{
		Schema: "event-processing", Name: "detect_snapshots", Class: ClassDeferred,
		Reason: "🔴 HOLDS TENANT DATA THAT THIS SWEEP CANNOT REACH. One row per DETECT partition, " +
			"and GA ships a single partition per instance, so this is one opaque blob containing " +
			"EVERY tenant's open detection windows and timers — including buffered event values for " +
			"the purged tenant. It has no tenant column because the engine keys tenancy inside the " +
			"payload (on the rule id), which no SQL predicate can address. Deleting the row is not " +
			"the answer either: it is the checkpoint every other tenant's replay-correct recovery " +
			"depends on. Erasing it requires the engine to evict the tenant in-process and " +
			"re-checkpoint, which is a separate step tracked as part of ADR-077.",
	},
}

// exemptionFor returns the registry entry covering a table, if one exists.
func exemptionFor(t Table) (Exemption, bool) {
	for _, e := range exemptions {
		if e.Schema != "*" && e.Schema != t.Schema {
			continue
		}
		if matchName(e.Name, t.Name) {
			return e, true
		}
	}
	return Exemption{}, false
}

// matchName supports an exact name, a "prefix_*" pattern and a "*_suffix" pattern.
// Deliberately not a regular expression: the patterns in the registry are there to
// cover the per-area repetitions of one table (every area's *_migrations), and a
// richer matcher would make it easy to write an exemption broad enough to swallow a
// table nobody meant to exempt.
//
// A bare "*" is rejected rather than treated as match-everything. Schema:"*" is
// legitimate — one rule covering the same table in every area — but Name:"*" would
// exempt an entire area's tables in one line, which is not an exemption, it is
// switching the fail-closed coverage check off for that area.
func matchName(pattern, name string) bool {
	switch {
	case pattern == "*":
		return false
	case pattern == name:
		return true
	case strings.HasPrefix(pattern, "*") && strings.HasSuffix(name, pattern[1:]):
		return true
	case strings.HasSuffix(pattern, "*") && strings.HasPrefix(name, pattern[:len(pattern)-1]):
		return true
	}
	return false
}
