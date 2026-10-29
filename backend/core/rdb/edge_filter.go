// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EdgeTarget is the far end of the edges EdgeTo matches, and the type they must have.
type EdgeTarget struct {
	// Kind and Token name the target entity ("customer", "acme"). Both are required.
	Kind, Token string
	// Type is the token of the relationship type the edge must have. Empty means any type
	// (a dashboard bound to an area asks for its members, not for one kind of link).
	Type string
	// TrackedOnly additionally requires that type to be TRACKED: the edge is then an
	// ownership edge in DeviceEdge's sense. A read made for a scoped caller sets it, so
	// what the filter can tell that caller about a device is never more than the
	// ownership it already reads (see EdgeTo).
	TrackedOnly bool
}

// EdgeTo is a FILTER — not a scope rule — for a read of the model whose rows are the
// sources of edges: it keeps the rows with a live edge of target.Type, in the read's
// tenant, from the row to the entity target names. It is the "devices of this customer"
// read, and it is written here rather than by the service for the reason every scope
// predicate is: the edge and type tables are tenant-scoped, a service-side JOIN or a raw
// fragment naming them would have to carry the tenant by hand, and a gorm subquery over
// them is refused to a scoped caller (neither model opts in: the relationship graph names
// every customer's devices). So the EXISTS is the one DeviceEdge builds, with the tenant
// bound on the edge AND the type table, and its own scope rule still applies to the row
// beside it — this narrows a read, it never widens one.
//
//	EXISTS (SELECT 1 FROM <edge> AS dc_scope_edge
//	        JOIN <type> AS dc_scope_type ON dc_scope_type.<id> = dc_scope_edge.<edge type>
//	        WHERE dc_scope_edge.<tenant> = $tenant AND dc_scope_type.<tenant> = $tenant
//	          [AND dc_scope_edge.deleted_at IS NULL] [AND dc_scope_type.deleted_at IS NULL]
//	          [AND dc_scope_type.<tracked> = $true]
//	          [AND dc_scope_type.<token> = $type]
//	          AND dc_scope_edge.<source type> = $kind AND dc_scope_edge.<source id> = "<table>".<row>
//	          AND dc_scope_edge.<target type> = $targetKind AND dc_scope_edge.<target token> = $targetToken)
//
// 🔴 IT DECIDES NOTHING ABOUT WHO MAY ASK. A scoped caller filtering by a customer OUTSIDE
// its scope would be handed the devices it can see that are also related to that customer
// — a device shared by two customers names the other one. The resolver must refuse that
// before it gets here (the target in the caller's scope), and set TrackedOnly for a scoped
// caller so an untracked edge it could not otherwise read is not disclosed either.
//
// The tenant is read from the statement's context and is required: a read with none is
// refused with core.ErrNoTenant, as the tenant callback would refuse it.
func EdgeTo(edges OwnershipEdges, target EdgeTarget) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		expr, err := edgeToExpr(db, edges, target)
		if err != nil {
			_ = db.AddError(err)
			return db
		}
		return db.Clauses(clause.Where{Exprs: []clause.Expression{expr}})
	}
}

func edgeToExpr(db *gorm.DB, e OwnershipEdges, target EdgeTarget) (clause.Expression, error) {
	if target.Kind == "" || target.Token == "" {
		return nil, fmt.Errorf("an edge filter needs a target kind and a target token")
	}
	tenant, ok := core.TenantFromContext(db.Statement.Context)
	if !ok || tenant == "" {
		return nil, core.ErrNoTenant
	}
	if db.Statement.Model == nil {
		return nil, fmt.Errorf("an edge filter on a statement with no model")
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(db.Statement.Model); err != nil || stmt.Schema == nil {
		return nil, fmt.Errorf("an edge filter on a model that does not parse")
	}
	row := stmt.Schema.LookUpField(e.Row)
	if row == nil {
		return nil, fmt.Errorf("an edge filter naming no row column %q", e.Row)
	}
	match := edgeMatch{
		trackedOnly: target.TrackedOnly,
		targets:     []edgeTarget{{kind: target.Kind, token: target.Token}},
	}
	if target.Type != "" {
		match.typeToken = &target.Type
	}
	expr, err := edgeExistsExpr(db, clause.Column{Table: stmt.Schema.Table, Name: row.DBName}, e, tenant, match)
	if err != nil {
		return nil, err
	}
	return expr, nil
}
