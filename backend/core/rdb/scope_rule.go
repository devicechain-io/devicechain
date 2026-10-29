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
// AnchorColumn, AnchorTable, HeldAnchors, DeviceEdge, ReachedFromDevice, OwnAnchor, NamedAnchor or Catalogue.
type ScopeRule interface {
	rule() *scopeRule
}

type ruleKind uint8

const (
	ruleCatalogue ruleKind = iota + 1
	ruleAnchorColumn
	ruleAnchorTable
	ruleDeviceEdge
	ruleOwnAnchor
	ruleHeldAnchors
	ruleReachedFromDevice
)

// scopeRule is the one concrete rule type; kind says which of its fields apply.
type scopeRule struct {
	kind   ruleKind
	column string         // ruleAnchorColumn; ruleOwnAnchor (the token column)
	set    AnchorSet      // ruleAnchorTable
	holder AnchorHolder   // ruleHeldAnchors
	edges  OwnershipEdges // ruleDeviceEdge, ruleReachedFromDevice
	reach  reachSpec      // ruleReachedFromDevice
	// ruleOwnAnchor: the row's anchor kind is either a column (kindColumn) or one
	// constant every row of the model shares (kindConst). Exactly one is set.
	kindColumn, kindConst string
	writes                ScopedWrite
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

// AnchorHolder describes the row that holds ANOTHER row's anchors, in an AnchorColumn of
// its own: device-state's live view of a device stamps its owners once, on the device's
// state row, and the device's latest readings and position — rows of other tables, keyed
// by the same device — are visible exactly when that row is.
type AnchorHolder struct {
	// Model is the holder's model. Its table name and its tenant column come from its own
	// schema, so the subquery carries the tenant predicate on the holder too: a holder row
	// of another tenant can never make a row visible. It must be tenant-scoped.
	Model any
	// Keys join the holder row to the row being read: each pair is a holder column (Set)
	// and the row's column it must equal (Row). At least one. They must identify ONE holder
	// row per row read, or a row is visible through whichever holder row matches.
	Keys []KeyPair
	// Column is the holder's text[] anchor column ("scope_anchors").
	Column string
}

// HeldAnchors is the rule of a model whose anchors are held by another row (see
// AnchorHolder). A scoped caller sees a row when its holder, in the same tenant and not
// soft-deleted, carries an anchor in its scope:
//
//	EXISTS (SELECT 1 FROM <holder> AS dc_scope_holder
//	        WHERE dc_scope_holder.<tenant> = $tenant
//	          AND dc_scope_holder.<holder key> = "<table>".<row key> ...
//	          [AND dc_scope_holder.deleted_at IS NULL]
//	          AND dc_scope_holder.<column> && ARRAY[$1, $2, ...]::text[])
//
// A row with no holder, or whose holder's column is NULL or empty, matches no scope: it is
// invisible to scoped callers rather than visible to all of them. It is one stamp read
// through, never a copy kept in step: restamping the holder moves every row it holds at
// once. Scoped writes are refused.
func HeldAnchors(holder AnchorHolder) ScopeRule {
	return &scopeRule{kind: ruleHeldAnchors, holder: holder}
}

// OwnershipEdges describes a relationship graph for DeviceEdge: an EDGE table whose rows
// point from a source entity to a target entity, and a TYPE table that says, per
// relationship type, whether an edge of that type is TRACKED. Every name is a field or
// column of the model it belongs to; every value compared against is bound.
type OwnershipEdges struct {
	// Edge and Type are the edge and relationship-type models. Both must be
	// tenant-scoped: the predicate carries the tenant on BOTH tables, so neither an edge
	// row nor a type row of another tenant can make a row visible.
	Edge, Type any
	// SourceType and SourceID are the edge columns naming the source entity's type and
	// row id; SourceKind is the type value that names a row of the model being read
	// ("device"), and Row is that model's id column the source id must equal.
	SourceType, SourceID, SourceKind, Row string
	// TargetType and TargetToken are the edge columns naming the target's type and its
	// token. They are compared with a scope anchor's kind and token.
	TargetType, TargetToken string
	// EdgeType is the edge column holding its type's id; TypeID is that id column on the
	// type table; Tracked is the type table's boolean column.
	EdgeType, TypeID, Tracked string
	// TypeToken is the type table's token column. DeviceEdge does not read it (ownership
	// is keyed on the tracked flag, never on a type's token); EdgeTo, which matches one
	// type by its token, requires it.
	TypeToken string
	// Groups, when set, is a second way a device is reached: by being a member of a group
	// a scope names (see GroupMembers). Nil means a scope's group anchors match only edges.
	Groups *GroupMembers
}

// GroupMembers describes a group READ-MODEL for DeviceEdge: a MEMBER table holding one row
// per (member entity, group, group version) and a GROUP table naming each group's token, the
// version currently in force, and whether the group may be named by a scope at all. A device
// is reached through it when it is a member of a group whose anchor ("<Kind>:<token>") the
// scope names, at the version the group currently holds, while the group is enabled. Every
// name is a field or column of the model it belongs to; every value compared against is bound.
type GroupMembers struct {
	// Kind is the anchor kind this branch serves ("group"). Only a scope's anchors of this
	// kind are matched against it.
	Kind string
	// Member and Group are the member and group models. Both must be tenant-scoped: the
	// predicate carries the tenant on BOTH tables.
	Member, Group any
	// MemberType and MemberID are the member columns naming the member entity's type and row
	// id; MemberKind is the type value that names a row of the model being read ("device").
	// The row's id column is OwnershipEdges.Row.
	MemberType, MemberID, MemberKind string
	// MemberGroup and MemberVersion are the member columns naming the group's id and the
	// version the row was computed at.
	MemberGroup, MemberVersion string
	// GroupID, GroupToken and GroupVersion are the group's id, token and current-version
	// columns. A member row of any other version confers nothing.
	GroupID, GroupToken, GroupVersion string
	// GroupEnabled is the group's column that is non-NULL while a scope may reach devices
	// through it. A group with it NULL confers nothing, whatever its members.
	GroupEnabled string
}

// DeviceEdge is the rule of the DEVICE row itself (ADR-081): a scoped caller sees a device
// it currently OWNS, which is an edge of a TRACKED relationship type from the device to an
// entity its scope names. The rule keys on the tracked flag, never on a type token —
// "assigned" is only the default, and any tracked type to a customer is ownership — and an
// untracked edge (group membership, containment) is not ownership.
//
//	EXISTS (SELECT 1 FROM <edge> AS dc_scope_edge
//	        JOIN <type> AS dc_scope_type ON dc_scope_type.<id> = dc_scope_edge.<edge type>
//	        WHERE dc_scope_edge.<tenant> = $tenant AND dc_scope_type.<tenant> = $tenant
//	          [AND dc_scope_edge.deleted_at IS NULL] [AND dc_scope_type.deleted_at IS NULL]
//	          AND dc_scope_type.<tracked> = $true
//	          AND dc_scope_edge.<source type> = $kind AND dc_scope_edge.<source id> = "<table>".<row>
//	          AND ((dc_scope_edge.<target type> = $1 AND dc_scope_edge.<target token> = $2) OR ...))
//
// With Groups set, a device is ALSO visible while it is a member of a group a scope names
// (see GroupMembers), and the predicate is the edge EXISTS OR:
//
//	EXISTS (SELECT 1 FROM <member> AS dc_scope_member
//	        JOIN <group> AS dc_scope_group ON dc_scope_group.<id> = dc_scope_member.<group>
//	        WHERE dc_scope_member.<tenant> = $tenant AND dc_scope_group.<tenant> = $tenant
//	          [AND dc_scope_member.deleted_at IS NULL] [AND dc_scope_group.deleted_at IS NULL]
//	          AND dc_scope_group.<enabled> IS NOT NULL
//	          AND dc_scope_member.<version> = dc_scope_group.<version>
//	          AND dc_scope_member.<member type> = $kind AND dc_scope_member.<member id> = "<table>".<row>
//	          AND dc_scope_group.<token> IN ($1, $2, ...))
//
// added only when the scope names an anchor of Groups.Kind.
//
// It is CURRENT ownership: an edge removed is a device no longer visible, at once. History
// (events, alarms) is stamped when written and keeps the owner it had then. Scoped writes
// are refused.
func DeviceEdge(edges OwnershipEdges) ScopeRule {
	return &scopeRule{kind: ruleDeviceEdge, edges: edges}
}

// OwnAnchor is the rule of a model whose row IS an anchor, in a kind column and a token
// column (event-management's event_anchors). A scoped caller sees the rows naming an
// anchor in its scope and no other, so reading an event's anchors never discloses that a
// co-owner exists. Scoped writes are refused.
func OwnAnchor(kindColumn, tokenColumn string) ScopeRule {
	return &scopeRule{kind: ruleOwnAnchor, kindColumn: kindColumn, column: tokenColumn}
}

// NamedAnchor is the rule of the entity an anchor NAMES: every row of the model is the
// anchor "<kind>:<row's token>", so a scoped caller sees exactly the rows its scope names
// (a customer member reads its own customer record, and no other). Scoped writes are
// refused.
func NamedAnchor(kind, tokenColumn string) ScopeRule {
	return &scopeRule{kind: ruleOwnAnchor, kindConst: kind, column: tokenColumn}
}

// reachSpec is what ReachedFromDevice adds to the ownership graph: the model of the source
// devices (whose own rule is the visibility test), the kind of entity the rule's model is,
// and the column holding the token an edge names it by.
type reachSpec struct {
	device      any
	kind        string
	tokenColumn string
}

// ReachedFromDevice is the rule of a model whose rows are TARGETS of the ownership graph
// that a scoped caller may know of only through the devices it can see: an area, an asset
// (ADR-081 decision 12). A scoped caller sees a row while some device it can CURRENTLY see
// (the DeviceEdge rule over edges, evaluated on device, group branch included) has a live
// edge of a TRACKED type to it:
//
//	EXISTS (SELECT 1 FROM <edge> AS dc_reach_edge
//	        JOIN <type> AS dc_reach_type ON dc_reach_type.<id> = dc_reach_edge.<edge type>
//	        WHERE dc_reach_edge.<tenant> = $tenant AND dc_reach_type.<tenant> = $tenant
//	          [AND dc_reach_edge.deleted_at IS NULL] [AND dc_reach_type.deleted_at IS NULL]
//	          AND dc_reach_type.<tracked> = $true
//	          AND dc_reach_edge.<source type> = $deviceKind
//	          AND dc_reach_edge.<target type> = $kind
//	          AND dc_reach_edge.<target token> = "<table>"."<token column>"
//	          AND EXISTS (SELECT 1 FROM <device> AS dc_reach_device
//	                      WHERE dc_reach_device.<tenant> = $tenant
//	                        [AND dc_reach_device.deleted_at IS NULL]
//	                        AND dc_reach_device.<row> = dc_reach_edge.<source id>
//	                        AND <the DeviceEdge predicate on dc_reach_device>))
//
// Four conjuncts carry the guarantee, and each is one a mutant must break: the TENANT on
// every table, TRACKED on the edge's type, the edge's SOURCE being a device the caller sees
// (not any device), and the edge naming THIS row. Visibility is CURRENT: a device that
// moves or is sold stops conferring it on the next read. An untracked edge, or a second
// hop (device -> asset -> customer), confers nothing. A scoped caller with no anchors sees
// nothing. Scoped writes are refused.
//
// device is the source devices' model (tenant-scoped), kind the entity kind the rule's
// model is ("area"), tokenColumn the model's token column.
func ReachedFromDevice(edges OwnershipEdges, device any, kind, tokenColumn string) ScopeRule {
	return &scopeRule{kind: ruleReachedFromDevice, edges: edges,
		reach: reachSpec{device: device, kind: kind, tokenColumn: tokenColumn}}
}
