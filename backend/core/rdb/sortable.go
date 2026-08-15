// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

// Sortable is the ordering half of the pagination contract, and every model handed
// to ListOf must implement it.
//
// LIMIT/OFFSET without an ORDER BY is UNSPECIFIED in Postgres. The planner may
// return rows in any order, and that order may differ between two calls to the same
// query — a different plan, a concurrent UPDATE, or a heap reshuffle is enough. So
// an unordered paged read can hand the same row out on two pages and never show
// another one at all. "Page 1" is not a stable concept without an ORDER BY, which
// makes it not a concept at all.
//
// This has bitten twice already. Both times it was patched at the one call site
// that complained: the alarm search reshuffled under the operator on every ack, and
// the LwM2M drain still over-reads a thousand rows per device wake to sort them
// client-side. Two symptoms, two local fixes, and every other list surface still
// carrying the same defect. Hence a contract rather than a third patch.
//
// 🔴 IT IS A COMPILE-TIME GATE, AND THAT IS THE WHOLE POINT. ListOf takes a
// Sortable, not an interface{}, so a model that declares no order cannot be passed
// to it. The next list endpoint someone adds does not silently inherit the bug —
// it does not build. Do not relax the parameter type back to interface{}, and do
// not add a fallback inside ListOf for a model that implements nothing: both turn
// a build error into a production defect that only appears under load.
type Sortable interface {
	// DefaultOrder returns the ORDER BY clause body applied to every paged read of
	// this model — no "ORDER BY" prefix, just the column list.
	//
	// Three rules, each of which has a failure mode behind it:
	//
	//  1. IT MUST BE TOTAL. Order by a non-unique column alone and the rows within
	//     a tie group are still free to move between calls, which is the original
	//     bug wearing an ORDER BY. End every clause with a unique column — the id,
	//     or the per-tenant token where there is no id.
	//
	//  2. IT MUST BE TABLE-QUALIFIED. A closure is free to add a JOIN, and the one
	//     that does joins entity_relationship_types, where BOTH sides have a token
	//     column. A bare `token ASC` there is not a wrong order, it is
	//     `ERROR: column reference "token" is ambiguous` — every static group's
	//     member list 500s. Qualification costs nothing on the tables with no join
	//     and is the difference between a query and an outage on the one with it.
	//     🔴 Qualify with the BARE table name and NEVER the schema. The schema is the
	//     service's functional area, which the NamingStrategy reads from a runtime
	//     env var — a clause that hard-codes it is wrong in any deployment that
	//     names the area differently. Postgres exposes the unqualified name as the
	//     implicit alias of a schema-qualified table, so `devices.id` resolves
	//     against `"device-management".devices`. That is also the form the existing
	//     joined queries already use.
	//
	//  3. NULLS MUST BE PLACED EXPLICITLY when the clause names a nullable column.
	//     Postgres puts NULLs last on ASC and first on DESC; SQLite — which is what
	//     the model tests run on — puts them first either way. Leave it implicit and
	//     the harness and production disagree silently, in the direction where the
	//     tests pass. SQLite honors an explicit NULLS LAST, so saying it makes the
	//     two agree rather than papering over a divergence.
	//
	// The clause is interpolated into the SQL verbatim (see the warning on
	// ListOf), so it may only ever be a literal this package's authors wrote. It
	// takes no arguments for exactly that reason.
	DefaultOrder() string
}

// DefaultOrder implements Sortable. The audit journal is read newest-first by its
// own documented contract, and this is where that contract now lives — audit_read.go
// used to say it in a closure, and said it incompletely: occurred_time alone is not
// total, because a burst of mutations inside one clock tick shares a timestamp and
// the rows within that tick were free to reshuffle between pages. Rows are immutable
// and id is monotonic, so id DESC is both the tiebreak and a truthful "newest".
//
// AuditEvent is the one model here with no created_at at all — occurred_time is its
// only time column — so it does not follow the registry default.
func (AuditEvent) DefaultOrder() string {
	return "audit_events.occurred_time DESC, audit_events.id DESC"
}
