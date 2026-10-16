// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

// A SCOPE RULE is how a tenant-scoped model admits a scoped caller (ADR-081): a member
// whose access is limited to part of the tenant, by a short list of anchors
// ("customer:acme", "group:north") carried on its token. The scope callback
// (scope_callback.go) asks the statement's model for its rule and either adds the
// predicate the rule describes — beside the tenant predicate, never in its place — or,
// when the model declares none, refuses the statement with auth.ErrScopeUnsupported.
//
// A model opts in by implementing ScopedModel:
//
//	func (Alarm) ScopedBy() rdb.ScopeRule { return rdb.AnchorColumn("scope_anchors", rdb.AllowScopedUpdate) }
//
// The rule types are SEALED: a rule is built only by the constructors in this file, so
// every predicate a scoped caller is held to is written here, once, with its own tenant
// clause, rather than by each service.

// ScopedModel is implemented by a tenant-scoped model that scoped callers may reach.
// ScopedBy is called on a zero value of the model, so it must depend on nothing but the
// type. A model that does not implement it — or returns nil — is refused to every scoped
// caller: unopted is FORBIDDEN, never "visible".
type ScopedModel interface {
	ScopedBy() ScopeRule
}

// ScopedWrite is a write a rule may permit a scoped caller. Reads are what a rule is
// for; writes are refused unless the rule names them. Deletes are never permitted.
type ScopedWrite uint8

const (
	// AllowScopedCreate lets a scoped caller INSERT rows of the model, each of which
	// must carry an anchor in the caller's scope (so a scoped caller can only create
	// rows it will itself be able to see). Command enqueue is the case it exists for.
	// An INSERT ... ON CONFLICT is refused whatever the rule says: its update arm
	// would reach a row the predicate never saw.
	AllowScopedCreate ScopedWrite = 1 << iota
	// AllowScopedUpdate lets a scoped caller UPDATE rows of the model that are in its
	// scope (the predicate applies), provided the update does not write the anchor
	// column itself — a caller rewriting the stamp that decides who can see a row
	// could hand it to anyone. Alarm acknowledge/clear is the case it exists for.
	AllowScopedUpdate
)

// ScopeRule is the visibility rule of a model for scoped callers. Build one with
// AnchorColumn, AnchorTable or Catalogue.
type ScopeRule interface {
	rule() *scopeRule
}

type ruleKind uint8

const (
	ruleCatalogue ruleKind = iota + 1
	ruleAnchorColumn
	ruleAnchorTable
)

// scopeRule is the one concrete rule type; kind says which of its fields apply.
type scopeRule struct {
	kind   ruleKind
	column string    // ruleAnchorColumn
	set    AnchorSet // ruleAnchorTable
	writes ScopedWrite
}

func (r *scopeRule) rule() *scopeRule { return r }

func (r *scopeRule) allows(w ScopedWrite) bool { return r.writes&w != 0 }

// Catalogue is the rule of tenant CONFIGURATION that every member may read, scoped or
// not: device types, profile versions and their vocabularies, units. A scoped read gets
// no predicate beyond the tenant's. Scoped writes are refused — a customer user
// configures nothing. Declare it only on a model whose rows are not device-derived; it
// is the storage half of auth.AuthorizeCatalogue.
func Catalogue() ScopeRule {
	return &scopeRule{kind: ruleCatalogue}
}

// AnchorColumn is the rule of a model whose rows carry their own anchors, in column (a
// PostgreSQL text[] of "kind:token", stamped when the row is written — alarms at raise,
// commands at enqueue). A scoped caller sees a row when the column shares an anchor
// with its scope:
//
//	"<table>"."<column>" && ARRAY[$1, $2, ...]::text[]
//
// A row whose column is NULL or empty matches no scope, so rows written before the stamp
// existed are invisible to scoped callers rather than visible to all of them. writes
// names the scoped writes the model permits (none by default).
func AnchorColumn(column string, writes ...ScopedWrite) ScopeRule {
	r := &scopeRule{kind: ruleAnchorColumn, column: column}
	for _, w := range writes {
		r.writes |= w
	}
	return r
}

// AnchorSet describes a set table that holds a row's anchors one per row, as
// event-management's event_anchors does for events, or a relationship table does for a
// device's owners.
type AnchorSet struct {
	// Model is the set table's model. Its table name and its tenant column come from
	// its own schema, so the subquery carries the tenant predicate on the set table too:
	// a set row of another tenant can never make a row visible. It must be
	// tenant-scoped.
	Model any
	// Keys join a set row to the row being read: each pair is a set-table column and
	// the row's column it must equal. At least one.
	Keys []KeyPair
	// KindColumn and TokenColumn are the set table's columns holding the anchor's kind
	// ("customer") and token ("acme").
	KindColumn, TokenColumn string
	// Where is an optional CONSTANT condition on the set table, written against the
	// alias dc_scope_set ("dc_scope_set.deleted_at IS NULL"). It is SQL, spliced as
	// written: it takes no arguments and must never be built from input.
	Where string
}

// KeyPair joins one set-table column (Set) to one column of the row being read (Row).
type KeyPair struct {
	Set, Row string
}

// AnchorTable is the rule of a model whose anchors live in a set table (see AnchorSet).
// A scoped caller sees a row when a set row for it, in the same tenant, names an anchor
// in its scope:
//
//	EXISTS (SELECT 1 FROM <set> AS dc_scope_set
//	        WHERE dc_scope_set.<tenant> = $tenant
//	          AND dc_scope_set.<set key> = "<table>".<row key> ...
//	          [AND (<Where>)]
//	          AND ((dc_scope_set.<kind> = $1 AND dc_scope_set.<token> = $2) OR ...))
//
// Scoped writes are refused.
func AnchorTable(set AnchorSet) ScopeRule {
	return &scopeRule{kind: ruleAnchorTable, set: set}
}
