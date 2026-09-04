// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import (
	"strings"

	"github.com/devicechain-io/dc-microservice/rdb"
)

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
	// Class is ClassExempt (holds no single tenant's data), ClassDeferred (holds
	// tenant data this mechanism does not yet erase), or ClassExternal (holds tenant
	// data that a named store erases by another route).
	Class Class
	// Reason says why. For ClassDeferred it must name what is left behind.
	Reason string
	// Store is the purge store that erases this table, for ClassExternal only. It is
	// the coordinator's store name, not prose: the point of naming it is that the
	// name can be checked against the registered store set, which is what stops
	// "something else handles it" from being an unfalsifiable claim.
	Store string
}

// exemptions is every table the catalog cannot classify but the platform can explain.
//
// 🔴 THE RULE FOR ADDING TO THIS LIST: an entry is a claim about what a table HOLDS.
// Before adding one, look at the table's rows, not at its name, and then pick the class
// that is true rather than the one that is convenient:
//
//   - ClassExempt — it holds no data belonging to any single tenant.
//   - ClassExternal — it holds this tenant's data, but a named purge store erases it by
//     a route this sweep cannot take. The store name is not decoration: the coordinator's
//     side asserts that store is registered (see ExternalStores). Be exact about how far
//     that goes — it establishes that the NAMED store exists, not that it still covers
//     this table — but it is what turns "handled elsewhere" from an assertion into
//     something with a failing test behind it.
//   - ClassDeferred — it holds this tenant's data and NOTHING erases it. The reason must
//     name what survives, and every purge on the instance stops completing until it does.
//
// The three exist so the last case stays visible instead of blending into a wall of
// exemptions, and so the middle one does not have to masquerade as either neighbour: as
// exempt it would hide a real hole, as deferred it would block every purge over data that
// is in fact erased. Nothing is ClassDeferred today; that is a statement about where the
// arc got to, not a reason to stop reaching for it.
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
		Schema: "event-processing", Name: "detect_snapshots", Class: ClassExternal, Store: "detect",
		Reason: "🔴 HOLDS TENANT DATA THAT THIS SWEEP CANNOT REACH, and is erased by the `detect` " +
			"store instead. One row per DETECT partition, and GA ships a single partition per " +
			"instance, so this is one opaque blob containing EVERY tenant's open detection windows " +
			"and timers — including buffered event values for the purged tenant. It has no tenant " +
			"column because the engine keys tenancy inside the payload (on the rule id), which no " +
			"SQL predicate can address. Deleting the row is not the answer either: it is the " +
			"checkpoint every other tenant's replay-correct recovery depends on. So the erasure " +
			"runs where the state is owned — the `detect` store asks the engine that owns the " +
			"partition to evict the tenant in-process and re-checkpoint, and the row is rewritten " +
			"without it rather than deleted.",
	},
}

// DetectPartitionsQuery lists the DETECT partitions that hold a durable checkpoint.
//
// It lives here, beside the exemption entry for the same table, because those are two facts
// about ONE table and they have to move together: the entry says detect_snapshots is erased
// out of band, and this is the statement the store erasing it uses to find out who owes an
// answer. The alternative is a literal in the store and a copy of it in the test that proves
// the literal works — which proves it about the copy.
//
// It is a raw statement rather than a model read on purpose. The shape belongs to
// event-processing; one column of one table is a far smaller thing for another area to
// depend on than a struct that area is free to change, and the dependency is pinned by
// TestTheDetectPartitionQueryRunsOnTheRealSchema in the migration drill, which executes it
// against a database with every area's migrations applied.
const DetectPartitionsQuery = `SELECT partition_id FROM "event-processing"."detect_snapshots"`

// ExternalStores returns the purge-store names the registry's ClassExternal entries
// claim their tables are erased by, deduplicated.
//
// It exists so the claim can be checked from the other end. This package cannot see
// the coordinator's store set — core cannot import a service — so on its own an
// external entry is only an assertion that something, somewhere, erases the table.
// Exposing the names lets the side that OWNS the store set assert the correspondence,
// which is the difference between a documented hole and a closed one. Without it the
// failure is silent in exactly the direction that matters: renaming or dropping a
// store leaves the table classified external and swept by nobody, and every purge
// completes reporting success.
func ExternalStores() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, e := range exemptions {
		if e.Class != ClassExternal || seen[e.Store] {
			continue
		}
		seen[e.Store] = true
		out = append(out, e.Store)
	}
	return out
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
	if e, ok := fenceTableExemption(t); ok {
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

// fenceTableExemption covers the second table every area carries: the ADR-077 erasure
// fence, created by core in each schema it manages (see rdb.PurgedTenant).
//
// 🔑 THE HONEST PART IS THAT THIS TABLE DOES NAME A TENANT. It is exempt anyway, and not
// because the token in it is unimportant — it is the same identifier the deletion record
// deliberately retains, on the same reasoning: a record certifying that a tenant's data
// was erased cannot certify anything if it may not say whose. What it holds is evidence
// OF the erasure, not any of the data erased. There is no measurement, no device, no
// address and no name in it; the columns are a token, the purge cut, and two timestamps.
//
// 🔴 SWEEPING IT WOULD DELETE THE FENCE MID-PURGE. The fence is planted before the sweep
// precisely so writes stop while the sweep runs; a sweep that then deleted it would
// re-open the area on its own last statement, and every subsequent pass would repeat the
// cycle while reporting the area clean. The row is not left behind either — the pass that
// releases the token stamps it completed, which is what lets a successor at that token
// write. That is the mechanism, not an omission from it.
func fenceTableExemption(t Table) (Exemption, bool) {
	if t.Name != rdb.FenceTable {
		return Exemption{}, false
	}
	return Exemption{
		Schema: t.Schema, Name: t.Name, Class: ClassExempt,
		Reason: "the ADR-077 erasure fence for this functional area: one row per purge of a " +
			"token, carrying the token, the purge epoch and when the fence was planted and " +
			"lifted. It is evidence OF an erasure and holds none of the data erased. Sweeping " +
			"it would delete, inside the sweep's own transaction, the fence that stops writes " +
			"arriving while the sweep runs.",
	}, true
}
