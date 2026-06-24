// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// ----------------
// Entity interface
// ----------------

// EntityResolver resolves the GraphQL Entity interface — the polymorphic source
// or target of a relationship (ADR-013). It wraps a concrete entity loaded by the
// entity-type registry and exposes the common id/token plus a ToX method per
// entity type for graph-gophers fragment resolution.
type EntityResolver struct {
	value interface{}
	S     *SchemaResolver
	C     context.Context
}

// entityIdToken extracts the id and token shared by every entity type.
func entityIdToken(value interface{}) (uint, string) {
	switch e := value.(type) {
	case *model.Device:
		return e.ID, e.Token
	case *model.DeviceGroup:
		return e.ID, e.Token
	case *model.Asset:
		return e.ID, e.Token
	case *model.AssetGroup:
		return e.ID, e.Token
	case *model.Area:
		return e.ID, e.Token
	case *model.AreaGroup:
		return e.ID, e.Token
	case *model.Customer:
		return e.ID, e.Token
	case *model.CustomerGroup:
		return e.ID, e.Token
	}
	return 0, ""
}

func (r *EntityResolver) Id() gql.ID {
	id, _ := entityIdToken(r.value)
	return gql.ID(fmt.Sprint(id))
}

func (r *EntityResolver) Token() string {
	_, token := entityIdToken(r.value)
	return token
}

func (r *EntityResolver) ToDevice() (*DeviceResolver, bool) {
	if e, ok := r.value.(*model.Device); ok {
		return &DeviceResolver{M: *e, S: r.S, C: r.C}, true
	}
	return nil, false
}

func (r *EntityResolver) ToDeviceGroup() (*DeviceGroupResolver, bool) {
	if e, ok := r.value.(*model.DeviceGroup); ok {
		return &DeviceGroupResolver{M: *e, S: r.S, C: r.C}, true
	}
	return nil, false
}

func (r *EntityResolver) ToAsset() (*AssetResolver, bool) {
	if e, ok := r.value.(*model.Asset); ok {
		return &AssetResolver{M: *e, S: r.S, C: r.C}, true
	}
	return nil, false
}

func (r *EntityResolver) ToAssetGroup() (*AssetGroupResolver, bool) {
	if e, ok := r.value.(*model.AssetGroup); ok {
		return &AssetGroupResolver{M: *e, S: r.S, C: r.C}, true
	}
	return nil, false
}

func (r *EntityResolver) ToArea() (*AreaResolver, bool) {
	if e, ok := r.value.(*model.Area); ok {
		return &AreaResolver{M: *e, S: r.S, C: r.C}, true
	}
	return nil, false
}

func (r *EntityResolver) ToAreaGroup() (*AreaGroupResolver, bool) {
	if e, ok := r.value.(*model.AreaGroup); ok {
		return &AreaGroupResolver{M: *e, S: r.S, C: r.C}, true
	}
	return nil, false
}

func (r *EntityResolver) ToCustomer() (*CustomerResolver, bool) {
	if e, ok := r.value.(*model.Customer); ok {
		return &CustomerResolver{M: *e, S: r.S, C: r.C}, true
	}
	return nil, false
}

func (r *EntityResolver) ToCustomerGroup() (*CustomerGroupResolver, bool) {
	if e, ok := r.value.(*model.CustomerGroup); ok {
		return &CustomerGroupResolver{M: *e, S: r.S, C: r.C}, true
	}
	return nil, false
}

// resolveEntity loads an entity by (type, id) and wraps it as an EntityResolver.
// A relationship's source/target is non-null, so a missing entity is an error.
func (s *SchemaResolver) resolveEntity(ctx context.Context, etype string, id uint) (*EntityResolver, error) {
	value, err := s.GetApi(ctx).LoadEntity(ctx, etype, id)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("no %s entity with id %d", etype, id)
	}
	return &EntityResolver{value: value, S: s, C: ctx}, nil
}

// ------------------------------
// Entity relationship type resolver
// ------------------------------

type EntityRelationshipTypeResolver struct {
	M model.EntityRelationshipType
	S *SchemaResolver
	C context.Context
}

func (r *EntityRelationshipTypeResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *EntityRelationshipTypeResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *EntityRelationshipTypeResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *EntityRelationshipTypeResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *EntityRelationshipTypeResolver) Token() string {
	return r.M.Token
}

func (r *EntityRelationshipTypeResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *EntityRelationshipTypeResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *EntityRelationshipTypeResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *EntityRelationshipTypeResolver) Tracked() bool {
	return r.M.Tracked
}

type EntityRelationshipTypeSearchResultsResolver struct {
	M model.EntityRelationshipTypeSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *EntityRelationshipTypeSearchResultsResolver) Results() []*EntityRelationshipTypeResolver {
	resolvers := make([]*EntityRelationshipTypeResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &EntityRelationshipTypeResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *EntityRelationshipTypeSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

// ---------------------------
// Entity relationship resolver
// ---------------------------

type EntityRelationshipResolver struct {
	M model.EntityRelationship
	S *SchemaResolver
	C context.Context
}

func (r *EntityRelationshipResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *EntityRelationshipResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *EntityRelationshipResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *EntityRelationshipResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *EntityRelationshipResolver) Token() string {
	return r.M.Token
}

func (r *EntityRelationshipResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *EntityRelationshipResolver) SourceType() string {
	return r.M.SourceType
}

func (r *EntityRelationshipResolver) TargetType() string {
	return r.M.TargetType
}

func (r *EntityRelationshipResolver) Source() (*EntityResolver, error) {
	return r.S.resolveEntity(r.C, r.M.SourceType, r.M.SourceId)
}

func (r *EntityRelationshipResolver) Target() (*EntityResolver, error) {
	return r.S.resolveEntity(r.C, r.M.TargetType, r.M.TargetId)
}

func (r *EntityRelationshipResolver) RelationshipType() *EntityRelationshipTypeResolver {
	return &EntityRelationshipTypeResolver{M: r.M.RelationshipType, S: r.S, C: r.C}
}

type EntityRelationshipSearchResultsResolver struct {
	M model.EntityRelationshipSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *EntityRelationshipSearchResultsResolver) Results() []*EntityRelationshipResolver {
	resolvers := make([]*EntityRelationshipResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &EntityRelationshipResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *EntityRelationshipSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
