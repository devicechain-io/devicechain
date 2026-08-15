// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-management/internal/selector"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// This file resolves an EntityGroup's members (ADR-061 G3). A static group's members are
// its "member" relationship edges (unchanged from G1); a dynamic group's members are the
// family entities whose facet attributes satisfy its CEL selector, lowered to an indexed
// SQL predicate (the selector package) and run as a correctly-paginated query — the
// eval-on-read path, never a per-row CEL evaluation.

// EntityMember is the lightweight identity of a resolved group member — enough for a
// consumer to hydrate the full entity. The family is the group's MemberType (the caller
// already knows it), so it is not repeated per row.
type EntityMember struct {
	Id    uint
	Token string
}

// EntityMemberSearchResults is a paginated page of resolved members.
type EntityMemberSearchResults struct {
	Results    []EntityMember
	Pagination rdb.SearchResultsPagination
}

// memberModelAndTable maps a member family to the gorm model (for tenant-scoped querying)
// and its SQL table name (for the ea.entity_id = <table>.id correlation). The table name is
// an internal constant, never user input — it is the one identifier the lowering
// concatenates rather than binds.
// The model is returned as an rdb.Sortable, not an any, because ListOf calls
// DefaultOrder() on it — so the false branch's nil is now a PANIC rather than a gorm
// error, and every caller must check the ok bool.
func memberModelAndTable(memberType string) (rdb.Sortable, string, bool) {
	switch entity.Type(memberType) {
	case entity.TypeDevice:
		return &Device{}, "devices", true
	case entity.TypeAsset:
		return &Asset{}, "assets", true
	case entity.TypeArea:
		return &Area{}, "areas", true
	case entity.TypeCustomer:
		return &Customer{}, "customers", true
	default:
		return nil, "", false
	}
}

// numericCastFor renders the value-column numeric cast for the running dialect: Postgres
// "col::numeric" in production, sqlite "CAST(col AS REAL)" for the oracle/unit tests. The
// value_type IN ('LONG','DOUBLE') gate in the lowering guarantees the cast only ever sees
// writer-validated numeric text (SetEntityAttribute enforces it, §3.4).
func numericCastFor(db *gorm.DB) func(string) string {
	if db.Dialector.Name() == "sqlite" {
		return func(col string) string { return "CAST(" + col + " AS REAL)" }
	}
	return func(col string) string { return col + "::numeric" }
}

// lowerParamsFor recompiles the group's stored (already-vetted) selector and lowers it,
// returning the SQL WHERE fragment, its args, and the member table. Fails closed if the
// group is not a resolvable dynamic group; otherwise delegates to lowerSelectorSource.
func (api *Api) lowerParamsFor(ctx context.Context, group *EntityGroup) (frag string, args []any, table string, err error) {
	if MembershipMode(group.MembershipMode) != MembershipDynamic || !group.Selector.Valid {
		return "", nil, "", fmt.Errorf("entity group %q is not a dynamic group", group.Token)
	}
	return api.lowerSelectorSource(ctx, group.MemberType, group.Selector.String)
}

// lowerSelectorSource compiles a selector source for a member family and lowers it to the
// SQL WHERE fragment, its args, and the member table. It is shared by a saved group's
// resolve (lowerParamsFor) and the unsaved-selector preview (PreviewSelector) so both take
// the identical publish-gate + lowering path. Recompiling per call is simple and correct
// for v1; a compiled-selector cache is a later optimization keyed on SelectorSchema. A
// compile/lowering rejection (non-lowerable, over-budget) is returned as the selector
// package's typed error so a caller can surface it inline.
func (api *Api) lowerSelectorSource(ctx context.Context, memberType, source string) (frag string, args []any, table string, err error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return "", nil, "", core.ErrNoTenant
	}
	_, table, ok = memberModelAndTable(memberType)
	if !ok {
		return "", nil, "", fmt.Errorf("unresolvable member family %q", memberType)
	}
	sel, err := selector.Compile(source, memberType, 0)
	if err != nil {
		return "", nil, "", err
	}
	frag, args, err = sel.Lower(selector.LowerParams{
		TenantId:    tenant,
		MemberType:  memberType,
		MemberTable: table,
		FacetScope:  string(AttributeScopeShared), // v1 pins facets to SHARED (§3.4)
		NumericCast: numericCastFor(api.RDB.DB(ctx)),
	})
	if err != nil {
		return "", nil, "", err
	}
	return frag, args, table, nil
}

// ResolveGroupMembers returns a paginated page of a group's members, transparently over
// either mode: a static group reads its "member" edges; a dynamic group runs its lowered,
// indexed selector query. The page is always bounded (Unbounded is forced off so a caller
// can never request an unbounded scan of a dynamic group); the standard rdb page-size clamp
// (ADR-029) applies. A per-tenant resolution rate limiter + metric are a G4 follow-up.
func (api *Api) ResolveGroupMembers(ctx context.Context, group *EntityGroup,
	pagination rdb.Pagination) (*EntityMemberSearchResults, error) {
	pagination.Unbounded = false // never an unbounded scan, even if a caller asks

	if MembershipMode(group.MembershipMode) != MembershipDynamic {
		return api.staticGroupMembers(ctx, group, pagination)
	}

	frag, args, _, err := api.lowerParamsFor(ctx, group)
	if err != nil {
		return nil, err
	}
	return api.queryDynamicMembers(ctx, group.MemberType, frag, args, pagination)
}

// queryDynamicMembers runs a lowered selector fragment as the paginated, indexed member
// query — the eval-on-read path shared by a saved dynamic group's resolve and the unsaved
// previewSelector. The page is always bounded (Unbounded forced off) so no caller can turn
// it into an unbounded scan of the member family.
func (api *Api) queryDynamicMembers(ctx context.Context, memberType, frag string, args []any,
	pagination rdb.Pagination) (*EntityMemberSearchResults, error) {
	pagination.Unbounded = false // never an unbounded scan, even if a caller asks
	// Fail closed on an unrecognized family rather than handing ListOf a nil Sortable,
	// which would panic on the DefaultOrder() call. Every caller reaching here has
	// already validated the family, so this is unreachable — the point is that it is now
	// unreachable BY CONSTRUCTION and not merely by caller discipline.
	memberModel, _, ok := memberModelAndTable(memberType)
	if !ok {
		return nil, fmt.Errorf("unresolvable member family %q", memberType)
	}

	results := make([]EntityMember, 0)
	db, pag := api.RDB.ListOf(ctx, memberModel, func(q *gorm.DB) *gorm.DB {
		// Order by id inside the CLOSURE, not chained after ListOf: gorm merges ORDER BY
		// by appending, so a clause added here leads and the model's DefaultOrder becomes
		// the trailing tiebreak. Chained after the call it would instead DOMINATE the
		// injected default, which is backwards — the per-query intent should refine the
		// model's order, not replace it.
		return q.Where(frag, args...).Order("id ASC")
	}, pagination)
	// Select only the identity columns; the order is already established above.
	db.Select("id", "token").Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &EntityMemberSearchResults{Results: results, Pagination: pag}, nil
}

// PreviewSelector compiles + cost-gates + lowers a CANDIDATE selector for a member family
// and returns its matching members WITHOUT persisting a group (ADR-061 G4 §4) — the console's
// live "matches N" authoring preview, the browse-panel analogue of the ADR-053 replay preview.
// It takes the identical publish gate + lowering + bounded query as a saved dynamic group, so
// what the author previews is exactly what the group would resolve. A compile/lowering
// rejection (a non-lowerable or over-budget selector) surfaces as the selector package's typed
// error for the caller to show inline; it is an author error, not a server fault.
func (api *Api) PreviewSelector(ctx context.Context, memberType, source string,
	pagination rdb.Pagination) (*EntityMemberSearchResults, error) {
	if !ValidGroupMemberType(entity.Type(memberType)) {
		return nil, fmt.Errorf("%q is not a valid group member family", memberType)
	}
	frag, args, _, err := api.lowerSelectorSource(ctx, memberType, source)
	if err != nil {
		return nil, err
	}
	return api.queryDynamicMembers(ctx, memberType, frag, args, pagination)
}

// staticGroupMembers pages the targets of a static group's "member" edges. TargetToken is
// denormalized on the edge (ADR-044), so no per-member lookup is needed.
func (api *Api) staticGroupMembers(ctx context.Context, group *EntityGroup,
	pagination rdb.Pagination) (*EntityMemberSearchResults, error) {
	results := make([]EntityMember, 0)
	db, pag := api.RDB.ListOf(ctx, &EntityRelationship{}, func(q *gorm.DB) *gorm.DB {
		return q.Joins("JOIN entity_relationship_types rt ON rt.id = entity_relationships.relationship_type_id").
			Where("rt.token = ?", MembershipRelationshipType).
			Where("entity_relationships.source_type = ?", string(entity.TypeGroup)).
			Where("entity_relationships.source_id = ?", group.ID).
			// Constrain to the group's family: edge creation does not yet enforce that a
			// member's family matches the group, so a stray group→area edge on a device
			// group must not surface here (and target ids are per-table, so an unfiltered
			// target_id would collide across families).
			Where("entity_relationships.target_type = ?", group.MemberType).
			// Ordered inside the CLOSURE so it LEADS and EntityRelationship's
			// DefaultOrder tiebreaks behind it; chained after ListOf it would dominate
			// the injected default instead. QUALIFIED because of the JOIN above: this
			// clause and the injected default land in the same ORDER BY, and the
			// default's token/created_at ARE ambiguous across the two joined tables —
			// so qualification is not optional in this statement, and writing the
			// leading key unqualified next to a qualified tiebreak would only invite
			// someone to "simplify" the one that cannot be. It is also the form every
			// predicate above already uses.
			Order("entity_relationships.target_id ASC")
	}, pagination)
	edges := make([]EntityRelationship, 0)
	db.Select("target_id", "target_token").Find(&edges)
	if db.Error != nil {
		return nil, db.Error
	}
	for _, e := range edges {
		results = append(results, EntityMember{Id: e.TargetId, Token: e.TargetToken})
	}
	return &EntityMemberSearchResults{Results: results, Pagination: pag}, nil
}

// IsGroupMember answers the single-entity membership test — "is the entity with this row id
// a member of the group?" — off the SAME lowered predicate as ResolveGroupMembers (§3.5).
// It is the forward-compat primitive the deferred DETECT-scoping incremental recompute
// needs (re-evaluate one entity when its attribute changes), which is why it exists from G3
// rather than being retrofitted. A static group degrades to a "member"-edge existence check,
// so a caller need not branch on mode.
func (api *Api) IsGroupMember(ctx context.Context, group *EntityGroup, entityId uint) (bool, error) {
	if MembershipMode(group.MembershipMode) != MembershipDynamic {
		var count int64
		err := api.RDB.DB(ctx).Model(&EntityRelationship{}).
			Joins("JOIN entity_relationship_types rt ON rt.id = entity_relationships.relationship_type_id").
			Where("rt.token = ?", MembershipRelationshipType).
			Where("entity_relationships.source_type = ?", string(entity.TypeGroup)).
			Where("entity_relationships.source_id = ?", group.ID).
			// target_type guards against a cross-family id collision — target ids are
			// per-table, so an area with the same row id as the queried device must not
			// answer true for a device group (see staticGroupMembers).
			Where("entity_relationships.target_type = ?", group.MemberType).
			Where("entity_relationships.target_id = ?", entityId).
			Limit(1).Count(&count).Error
		return count > 0, err
	}

	frag, args, table, err := api.lowerParamsFor(ctx, group)
	if err != nil {
		return false, err
	}
	// Fail closed on an unrecognized family. lowerParamsFor above already resolved the
	// same family (it is where the table name came from), so this cannot fire — but a
	// nil model reaching gorm's Model() is a silent wrong answer, so check it here
	// rather than rely on that coupling holding.
	memberModel, _, ok := memberModelAndTable(group.MemberType)
	if !ok {
		return false, fmt.Errorf("unresolvable member family %q", group.MemberType)
	}
	var count int64
	err = api.RDB.DB(ctx).Model(memberModel).
		Where(table+".id = ?", entityId).
		Where(frag, args...).
		Limit(1).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
