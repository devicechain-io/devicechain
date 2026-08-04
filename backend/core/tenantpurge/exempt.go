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
	// Schema is the functional-area schema.
	Schema string
	// Name is the table name, matched exactly.
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
		Schema: "user-management", Name: "iam_tenant_purges", Class: ClassExempt,
		Reason: "the DELETION RECORD, which has to outlive the tenant it records — erasing it would " +
			"destroy the evidence that the erasure happened, and it is the only durable proof there " +
			"is once the tenant row is gone. What it retains about the tenant is stated rather than " +
			"waved at: the token, the purge epoch, a completion timestamp and a row count. No name, " +
			"no contact, no configuration. The token is unavoidable — a record that cannot say WHICH " +
			"tenant was erased is not a record.",
	},
	{
		Schema: "user-management", Name: "iam_tenant_purge_stores", Class: ClassExempt,
		Reason: "the deletion record's per-store ledger: one row per (purge, store) carrying a row " +
			"count, a completion flag and the text of anything left behind. It names no tenant except " +
			"through the record it belongs to, and it is the part an auditor reads to see WHAT was " +
			"erased rather than merely that something was.",
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
//
// Matching is EXACT on both schema and name — there is no pattern language, and that is
// a deliberate narrowing. The one repetition worth generalising is each area's own
// gormigrate bookkeeping table, and it is handled by migrationTableExemption below,
// which derives the exact name from the schema instead of matching a suffix. The
// difference is not cosmetic: a `*_migrations` rule would also exempt a future feature
// table that happened to end in that word — say a firmware `channel_migrations` — under
// a reason about gormigrate that has nothing to do with it, and the coverage gate would
// go green over real tenant data.
func exemptionFor(t Table) (Exemption, bool) {
	if e, ok := migrationTableExemption(t); ok {
		return e, true
	}
	for _, e := range exemptions {
		if e.Schema == t.Schema && e.Name == t.Name {
			return e, true
		}
	}
	return Exemption{}, false
}

// migrationTableExemption covers one table per area: the gormigrate ledger, whose name
// rdb.MigrationTableName derives from the functional area by replacing dashes with
// underscores. Deriving the name the same way means this matches that table and nothing
// else in the schema.
func migrationTableExemption(t Table) (Exemption, bool) {
	if t.Name != strings.ReplaceAll(t.Schema, "-", "_")+"_migrations" {
		return Exemption{}, false
	}
	return Exemption{
		Schema: t.Schema, Name: t.Name, Class: ClassExempt,
		Reason: "gormigrate bookkeeping: one row per applied migration id, for this functional " +
			"area. Records schema history, never tenant data.",
	}, true
}
