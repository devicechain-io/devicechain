// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"fmt"
	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find device type by unique id.
func (r *SchemaResolver) DeviceTypesById(ctx context.Context, args struct {
	Ids []string
}) ([]*DeviceTypeResolver, error) {
	if err := auth.AuthorizeCatalogue(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := util.AsUintIds(args.Ids)
	if err != nil {
		return nil, err
	}

	found, err := api.DeviceTypesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceTypeResolver, 0)
	for _, dt := range found {
		dtr := &DeviceTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// Find device type by unique token.
func (r *SchemaResolver) DeviceTypesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*DeviceTypeResolver, error) {
	if err := auth.AuthorizeCatalogue(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DeviceTypesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceTypeResolver, 0)
	for _, dt := range found {
		dtr := &DeviceTypeResolver{
			M: *dt,
			S: r,
			C: ctx,
		}
		result = append(result, dtr)
	}
	return result, nil
}

// List all device types that match the given criteria.
func (r *SchemaResolver) DeviceTypes(ctx context.Context, args struct {
	Criteria model.DeviceTypeSearchCriteria
}) (*DeviceTypeSearchResultsResolver, error) {
	if err := auth.AuthorizeCatalogue(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DeviceTypes(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &DeviceTypeSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// Find devices by unique id.
func (r *SchemaResolver) DevicesById(ctx context.Context, args struct {
	Ids []string
}) ([]*DeviceResolver, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := util.AsUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.DevicesById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceResolver, 0)
	for _, dv := range found {
		dvr := &DeviceResolver{
			M: *dv,
			S: r,
			C: ctx,
		}
		result = append(result, dvr)
	}
	return result, nil
}

// Find devices by unique token.
func (r *SchemaResolver) DevicesByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*DeviceResolver, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DevicesByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceResolver, 0)
	for _, dv := range found {
		dvr := &DeviceResolver{
			M: *dv,
			S: r,
			C: ctx,
		}
		result = append(result, dvr)
	}
	return result, nil
}

// Find devices by their customer-owned external id (ADR-049).
func (r *SchemaResolver) DevicesByExternalId(ctx context.Context, args struct {
	ExternalIds []string
}) ([]*DeviceResolver, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.DevicesByExternalId(ctx, args.ExternalIds)
	if err != nil {
		return nil, err
	}

	result := make([]*DeviceResolver, 0)
	for _, dv := range found {
		dvr := &DeviceResolver{
			M: *dv,
			S: r,
			C: ctx,
		}
		result = append(result, dvr)
	}
	return result, nil
}

// List all devices that match the given criteria.
func (r *SchemaResolver) Devices(ctx context.Context, args struct {
	Criteria model.DeviceSearchCriteria
}) (*DeviceSearchResultsResolver, error) {
	scope, err := auth.AuthorizeScoped(ctx, auth.DeviceRead)
	if err != nil {
		return nil, err
	}
	// The provenance and earmark filters name provisioning profiles, which a scoped caller may
	// not read, and the scoped device rule cannot be combined with their subqueries. Refused up
	// front rather than left to fail in the database with its own wording.
	if scope.IsScoped() && (args.Criteria.ProvisioningProfile != nil || args.Criteria.ExpectedProvisioningProfile != nil ||
		args.Criteria.Alias != nil) {
		return nil, auth.ErrScopeUnsupported
	}

	api := r.GetApi(ctx)
	found, err := api.Devices(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &DeviceSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// CustomerDeviceSearchCriteriaInput is customerDevices' GraphQL input.
type CustomerDeviceSearchCriteriaInput struct {
	PageNumber       int32
	PageSize         int32
	Customer         string
	RelationshipType string
}

// CustomerDevices lists the devices linked to one customer by one relationship type: what a
// dashboard widget bound to a customer streams.
//
// A member limited to part of the tenant (ADR-081) is answered only about a customer its
// scope names, and only through an OWNERSHIP edge (a tracked type), which together mean
// the answer is never more than the devices that member already reads through its own
// customer:
//
//   - 🔴 THE CUSTOMER MUST BE IN THE SCOPE, AND THAT IS CHECKED HERE, NOT IN THE STORE. The
//     device rule beside the filter keeps every row one the caller can see, but a device may
//     be assigned to two customers at once, so a caller asking about a customer outside its
//     scope would be told which of ITS devices that other customer also holds — the
//     disclosure deviceScopeAnchors is closed to scoped callers to avoid. The refusal is
//     decided from the caller's claims alone, so a customer that exists and one that does
//     not are refused alike: the answer says nothing about the customer asked about.
//   - an untracked edge is not ownership, and the relationship graph is closed to a scoped
//     caller, so for one the type must be tracked (OwnershipOnly); an untracked type matches
//     nothing, as an unknown type does.
//
// An unscoped caller is answered for any customer through any type: the same members the
// relationship search gives for (device → customer, type).
func (r *SchemaResolver) CustomerDevices(ctx context.Context, args struct {
	Criteria CustomerDeviceSearchCriteriaInput
}) (*DeviceSearchResultsResolver, error) {
	scope, err := auth.AuthorizeScoped(ctx, auth.DeviceRead)
	if err != nil {
		return nil, err
	}
	criteria := model.CustomerDeviceSearchCriteria{
		Pagination:       rdb.Pagination{PageNumber: args.Criteria.PageNumber, PageSize: args.Criteria.PageSize},
		Customer:         args.Criteria.Customer,
		RelationshipType: args.Criteria.RelationshipType,
	}
	if scope.IsScoped() {
		if !scopeNamesCustomer(scope, args.Criteria.Customer) {
			return nil, auth.ErrForbidden
		}
		criteria.OwnershipOnly = true
	}
	found, err := r.GetApi(ctx).CustomerDevices(ctx, criteria)
	if err != nil {
		return nil, err
	}
	return &DeviceSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

// AnchorDeviceSearchCriteriaInput is anchorDevices' GraphQL input.
type AnchorDeviceSearchCriteriaInput struct {
	PageNumber       int32
	PageSize         int32
	TargetType       string
	Target           string
	RelationshipType *string
}

// AnchorDevices lists the devices on one anchor - a customer, an area or an asset - newest
// first, one page at a time: the members of a dashboard widget bound to that anchor, as the
// CALLER may read them.
//
// 🔴 EVERY ANSWER IS THE INTERSECTION OF THE ANCHOR AND THE DEVICES THE CALLER SEES, and that
// is enforced in the store (the Device rule beside the edge filter), not here: the page, and
// pagination.totalRecords, count only the caller's own devices, never the whole anchor
// (ADR-081 decision 12). For a member limited to part of the tenant:
//
//   - a CUSTOMER anchor must be one the scope names, checked here from the claims alone,
//     because a device may be assigned to two customers and asking about a customer outside
//     the scope would name the co-owner. An existing and a non-existent customer are
//     refused alike (FORBIDDEN). This is customerDevices' rule, unchanged.
//   - an AREA or ASSET anchor needs no pre-check: the intersection is the visibility rule.
//     An anchor none of the caller's devices is on answers an empty page, which is a fact
//     about the caller's own devices and confirms nothing about the anchor.
//   - only an ownership edge (a tracked type) matches, as the relationship graph is closed
//     to a scoped caller and an untracked edge would disclose what it hides. A scoped caller
//     may therefore not name a relationshipType at all: it is refused, not ignored.
//   - any other anchor kind is refused as unsupported for scoped access, never answered empty.
//
// An unscoped caller is answered for any anchor and any type, as the relationship search
// gives for (device -> anchor).
func (r *SchemaResolver) AnchorDevices(ctx context.Context, args struct {
	Criteria AnchorDeviceSearchCriteriaInput
}) (*DeviceSearchResultsResolver, error) {
	scope, err := auth.AuthorizeScoped(ctx, auth.DeviceRead)
	if err != nil {
		return nil, err
	}
	in := args.Criteria
	criteria := model.AnchorDeviceSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: in.PageNumber, PageSize: in.PageSize},
		TargetType: in.TargetType,
		Target:     in.Target,
	}
	if in.RelationshipType != nil {
		criteria.RelationshipType = *in.RelationshipType
	}
	if !model.IsAnchorDevicesTargetType(in.TargetType) {
		if scope.IsScoped() {
			return nil, auth.ErrScopeUnsupported
		}
		return nil, fmt.Errorf("targetType %q is not an anchor with devices: use customer, area or asset", in.TargetType)
	}
	if scope.IsScoped() {
		// A relationship type names an edge, and the relationship graph is closed to a scoped
		// caller: it must not be able to probe which types link its devices to an anchor.
		// Ownership (tracked) edges are the only ones it matches, so there is nothing to choose.
		if in.RelationshipType != nil {
			return nil, auth.ErrScopeUnsupported
		}
		if in.TargetType == auth.AnchorKindCustomer && !scopeNamesCustomer(scope, in.Target) {
			return nil, auth.ErrForbidden
		}
		criteria.OwnershipOnly = true
	}
	found, err := r.GetApi(ctx).AnchorDevices(ctx, criteria)
	if err != nil {
		return nil, err
	}
	return &DeviceSearchResultsResolver{M: *found, S: r, C: ctx}, nil
}

// scopeNamesCustomer reports whether the scope names the customer with this token.
func scopeNamesCustomer(scope auth.Scope, customer string) bool {
	for _, a := range scope.Anchors() {
		kind, token, err := auth.ParseAnchor(a)
		if err == nil && kind == auth.AnchorKindCustomer && token == customer {
			return true
		}
	}
	return false
}
